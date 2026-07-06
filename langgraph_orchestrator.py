"""
LangGraph による PR レビューグラフ。

各観点について review_node → check_node を実行し、check の結果に応じて
同じ観点をやり直す（redo）か、次の観点に進むかをグラフ内部の条件分岐エッジで判断する。
全観点が終わったら synthesize_node で最終レポートを生成する。

GitHub Actions上での非対話実行を前提とし、`.invoke()` 一発でレビュー全体を完走させる
（Anthropicの従量課金APIを直接呼ぶ。サブエージェントへの委譲は行わない）。
"""

import argparse
import json
import os
import sys
from pathlib import Path
from typing import TypedDict

from langchain_anthropic import ChatAnthropic
from langchain_core.messages import HumanMessage, SystemMessage
from langgraph.graph import END, StateGraph
from pydantic import BaseModel, Field

from perspectives.config import PERSPECTIVES

MAX_RETRIES = 2
# 以下3つのしきい値は観点数（PERSPECTIVES）に応じた固定値。観点を追加・削除したら手動で見直すこと。
# 現在は13観点構成: ABORT_THRESHOLD=8（約半数の観点がredoしたら異常とみなす早期警告）、
# ITERATION_BUDGET=78（13観点 × 最大6回(review3回+check3回) = 現在のグラフ構造上の理論最大値。
# redo_total等のロジックが正しく機能している限り発火しない、最終防衛ラインとしての保険）
ABORT_THRESHOLD = 8       # redo（差戻し）の累計発生回数がこれに達したら無限ループとみなして中断する
TOKEN_BUDGET = 500_000    # 1回のレビューで消費できるトークン数（review/check/synthesizeのLLM呼び出し合計）の上限
ITERATION_BUDGET = 78     # review_node/check_nodeの累計実行回数がこれを超えたら中断する（redo以外の原因での無限ループ対策）

# review/synthesizeは指摘の質が重要なため上位モデル、checkは合否判定という単純な分類タスクのため軽量モデルを使う
REVIEW_MODEL = os.environ.get("REVIEW_MODEL", "claude-sonnet-5")
CHECK_MODEL = os.environ.get("CHECK_MODEL", "claude-haiku-4-5-20251001")

TOTAL = len(PERSPECTIVES)


# ── LLM呼び出し用スキーマ ──────────────────────────────────────────────────────

class Issue(BaseModel):
    severity: str = Field(description="深刻度: 高/中/低")
    location: str = Field(description="問題箇所（ファイル名・行番号など、不明な場合は 'PR全体'）")
    description: str = Field(description="問題の説明")
    suggestion: str = Field(description="修正の提案")


class ReviewResult(BaseModel):
    perspective_id: int
    perspective_name: str
    has_issues: bool
    issues: list[Issue] = Field(default_factory=list)
    summary: str = Field(description="レビュー結果の1〜2文の要約")


class CheckResult(BaseModel):
    perspective_id: int
    ok: bool
    feedback: str = Field(
        default="",
        description="ok=false の場合のフィードバック（見落とし・誤検知の具体的な説明）。ok=true の場合は空文字。",
    )


SYNTHESIS_SYSTEM_PROMPT = """\
あなたはPRレビューの最終レポートを作成するエージェントです。
複数の観点からのレビュー結果を統合し、開発者向けの分かりやすいレポートをMarkdown形式で作成してください。

レポートの構成:
1. ## サマリー（問題の総数、深刻度の内訳、1〜2文の総評）
2. ## 問題一覧（問題があった観点のみ。深刻度 高→低 の順）
3. ## 問題なし（問題が検出されなかった観点の一覧）

箇条書きを活用し、開発者がすぐに修正に着手できる具体的な記述にしてください。
"""


def get_llm(model: str) -> ChatAnthropic:
    return ChatAnthropic(model=model)


# ── State ─────────────────────────────────────────────────────────────────────

class ReviewState(TypedDict):
    pr_content: str
    idx: int
    results: dict          # perspective_id(int) -> ReviewResult(dict)
    checks: dict            # perspective_id(int) -> CheckResult(dict)
    redo_counts: dict        # perspective_id(int) -> int
    redo_total: int
    iteration_total: int
    token_total: int
    status: str              # "in_progress" | "done" | "aborted"
    abort_reason: dict | None
    next_step: str


def _abort_reason(state: ReviewState, idx: int) -> dict:
    p = PERSPECTIVES[idx]
    if state["redo_total"] >= ABORT_THRESHOLD:
        return {
            "step": f"観点 {idx + 1}/{TOTAL}: {p['name']} のレビュー検証（check）フェーズ",
            "reason": f"redo（差戻し）が累計{ABORT_THRESHOLD}回発生したため、無限ループ防止のため中断しました。",
        }
    if state["token_total"] >= TOKEN_BUDGET:
        return {
            "step": f"観点 {idx + 1}/{TOTAL}: {p['name']} の処理中",
            "reason": (
                f"1回のレビューで消費したトークン数が上限（{TOKEN_BUDGET}トークン）に達したため中断しました"
                f"（実測: {state['token_total']}トークン）。"
            ),
        }
    return {
        "step": f"観点 {idx + 1}/{TOTAL}: {p['name']} の処理中",
        "reason": (
            f"ループの反復回数が上限（{ITERATION_BUDGET}回）に達したため、無限ループ防止のため中断しました"
            f"（実測: {state['iteration_total']}回）。"
        ),
    }


def _over_budget(state: ReviewState) -> bool:
    return (
        state["redo_total"] >= ABORT_THRESHOLD
        or state["token_total"] >= TOKEN_BUDGET
        or state["iteration_total"] > ITERATION_BUDGET
    )


# ── Nodes ─────────────────────────────────────────────────────────────────────

def review_node(state: ReviewState) -> ReviewState:
    idx = state["idx"]
    p = PERSPECTIVES[idx]

    feedback_section = ""
    prev_check = state["checks"].get(idx)
    if prev_check and not prev_check["ok"] and prev_check.get("feedback"):
        feedback_section = f"\n\n## 前回レビューへのフィードバック（要反映）\n{prev_check['feedback']}"

    structured_llm = get_llm(REVIEW_MODEL).with_structured_output(ReviewResult, include_raw=True)
    messages = [
        SystemMessage(content=p["review_prompt"] + feedback_section),
        HumanMessage(content=f"## PR内容\n\n{state['pr_content']}"),
    ]
    raw = structured_llm.invoke(messages)
    result: ReviewResult = raw["parsed"]
    result.perspective_id = idx
    result.perspective_name = p["name"]

    state["token_total"] += (raw["raw"].usage_metadata or {}).get("total_tokens", 0)
    state["iteration_total"] += 1

    results = dict(state["results"])
    results[idx] = result.model_dump()
    state["results"] = results

    Path("review_results").mkdir(exist_ok=True)
    Path(f"review_results/result_{idx}.json").write_text(
        result.model_dump_json(indent=2, ensure_ascii=False), encoding="utf-8"
    )

    if _over_budget(state):
        state["status"] = "aborted"
        state["abort_reason"] = _abort_reason(state, idx)
        state["next_step"] = "abort"
    else:
        state["next_step"] = "check"
    return state


def check_node(state: ReviewState) -> ReviewState:
    idx = state["idx"]
    p = PERSPECTIVES[idx]
    result = state["results"][idx]

    structured_llm = get_llm(CHECK_MODEL).with_structured_output(CheckResult, include_raw=True)
    messages = [
        SystemMessage(content=p["checker_prompt"]),
        HumanMessage(
            content=(
                f"## PR内容\n\n{state['pr_content']}\n\n"
                f"## レビュー結果\n\n```json\n{json.dumps(result, ensure_ascii=False, indent=2)}\n```"
            )
        ),
    ]
    raw = structured_llm.invoke(messages)
    check_result: CheckResult = raw["parsed"]
    check_result.perspective_id = idx

    state["token_total"] += (raw["raw"].usage_metadata or {}).get("total_tokens", 0)
    state["iteration_total"] += 1

    checks = dict(state["checks"])
    checks[idx] = check_result.model_dump()
    state["checks"] = checks

    Path(f"review_results/check_{idx}.json").write_text(
        check_result.model_dump_json(indent=2, ensure_ascii=False), encoding="utf-8"
    )

    if _over_budget(state):
        state["status"] = "aborted"
        state["abort_reason"] = _abort_reason(state, idx)
        state["next_step"] = "abort"
        return state

    if check_result.ok:
        next_idx = idx + 1
        state["idx"] = next_idx
        state["next_step"] = "synthesize" if next_idx >= TOTAL else "review"
        return state

    redo_count = state["redo_counts"].get(idx, 0)
    if redo_count < MAX_RETRIES:
        state["redo_counts"] = {**state["redo_counts"], idx: redo_count + 1}
        state["redo_total"] += 1
        state["next_step"] = "review"  # 同じ観点をやり直す（idxは変えない）
        return state

    print(f"[review-graph] 観点 {idx} は最大リトライ回数を超えました。スキップします。", file=sys.stderr)
    next_idx = idx + 1
    state["idx"] = next_idx
    state["next_step"] = "synthesize" if next_idx >= TOTAL else "review"
    return state


def synthesize_node(state: ReviewState) -> ReviewState:
    results_json = json.dumps(
        [state["results"][i] for i in sorted(state["results"])], ensure_ascii=False, indent=2
    )
    messages = [
        SystemMessage(content=SYNTHESIS_SYSTEM_PROMPT),
        HumanMessage(
            content=(
                f"## PR内容\n\n{state['pr_content']}\n\n"
                f"## 各観点のレビュー結果\n\n```json\n{results_json}\n```"
            )
        ),
    ]
    response = get_llm(REVIEW_MODEL).invoke(messages)
    state["token_total"] += (response.usage_metadata or {}).get("total_tokens", 0)

    Path("review_results/final_report.md").write_text(response.content, encoding="utf-8")
    state["status"] = "done"
    return state


# ── Graph ─────────────────────────────────────────────────────────────────────

def build_graph():
    g = StateGraph(ReviewState)
    g.add_node("review_node", review_node)
    g.add_node("check_node", check_node)
    g.add_node("synthesize_node", synthesize_node)
    g.set_entry_point("review_node")
    g.add_conditional_edges("review_node", lambda s: s["next_step"], {"check": "check_node", "abort": END})
    g.add_conditional_edges(
        "check_node", lambda s: s["next_step"], {"review": "review_node", "synthesize": "synthesize_node", "abort": END}
    )
    g.add_edge("synthesize_node", END)
    return g.compile()


def main() -> None:
    parser = argparse.ArgumentParser(description="LangGraphベースのPRレビュー")
    parser.add_argument("--pr-file", required=True)
    args = parser.parse_args()

    initial_state: ReviewState = {
        "pr_content": Path(args.pr_file).read_text(encoding="utf-8"),
        "idx": 0,
        "results": {},
        "checks": {},
        "redo_counts": {},
        "redo_total": 0,
        "iteration_total": 0,
        "token_total": 0,
        "status": "in_progress",
        "abort_reason": None,
        "next_step": "",
    }

    graph = build_graph()
    final_state = graph.invoke(initial_state, config={"recursion_limit": 100})

    if final_state["status"] == "aborted":
        print(json.dumps(final_state["abort_reason"], ensure_ascii=False))
        sys.exit(1)

    print("review_results/final_report.md")


if __name__ == "__main__":
    main()
