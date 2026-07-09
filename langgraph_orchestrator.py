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
# ITERATION_BUDGETは観点数（PERSPECTIVES）に応じた固定値。観点を追加・削除したら手動で見直すこと。
# 現在は13観点構成: 13観点 × 最大6回(review3回+check3回) = 現在のグラフ構造上の理論最大値。
# COST_BUDGET_USD等のロジックが正しく機能している限り発火しない、最終防衛ラインとしての保険。
ITERATION_BUDGET = 78     # review_node/check_nodeの累計実行回数がこれを超えたら中断する（無限ループ対策）
# redo累計回数によるABORT_THRESHOLDは撤廃した。観点単位のMAX_RETRIESと
# 全体のCOST_BUDGET_USD/ITERATION_BUDGETで既に無限ループ・過剰コストは防げるため不要と判断。
# 撤廃前は「redoが多い=異常」とみなして早期に全体を中断していたが、実際には単に個々の観点で
# review/checkの意見が収束しにくいだけのケースが多く、後半の観点が一度も実行されないまま
# 中断される弊害があった。MAX_RETRIES超過でスキップされた観点はunresolved_idsに記録し、
# 最終レポートに「未解決の意見対立」として明示することで、判断を人間に委ねる。

# prompt cachingを導入した結果、LangChainのusage_metadata.total_tokensはcache_read/cache_creation分も
# 加算した「実質フルサイズ」を報告する仕様であることが分かった（キャッシュヒットしても減らない）。
# トークン数ベースの予算では「redoが一度も起きない正常運転」でも発火してしまうため、実際の課金額（USD概算）
# ベースの予算に変更した。cache_read/cache_write の単価を反映することで、キャッシュの効果が正しく予算に反映される。
COST_BUDGET_USD = float(os.environ.get("COST_BUDGET_USD", "5.0"))

# $/MTok（Anthropic公式の通常価格。Sonnet 5の導入価格(2026-08-31まで有効)は反映していない=保守的に見積もる）
# cache write(5分TTL)は入力単価の1.25倍、cache readは入力単価の0.1倍という共通ルールを掛け合わせる
PRICING_USD_PER_MTOK: dict[str, dict[str, float]] = {
    "claude-sonnet-5": {"input": 3.00, "output": 15.00},
    "claude-haiku-4-5-20251001": {"input": 1.00, "output": 5.00},
    "claude-haiku-4-5": {"input": 1.00, "output": 5.00},
}

# review/synthesizeは指摘の質が重要なため上位モデル、checkは合否判定という単純な分類タスクのため軽量モデルを使う
REVIEW_MODEL = os.environ.get("REVIEW_MODEL", "claude-sonnet-5")
CHECK_MODEL = os.environ.get("CHECK_MODEL", "claude-haiku-4-5-20251001")

TOTAL = len(PERSPECTIVES)


def _estimate_cost_usd(model: str, usage_metadata: dict | None) -> float:
    if not usage_metadata:
        return 0.0
    if model not in PRICING_USD_PER_MTOK:
        raise ValueError(
            f"モデル '{model}' の料金がPRICING_USD_PER_MTOKに登録されていません。"
            "REVIEW_MODEL/CHECK_MODELを変更した場合はコスト予算の単価も追加してください。"
        )
    price = PRICING_USD_PER_MTOK[model]
    details = usage_metadata.get("input_token_details") or {}
    cache_read = details.get("cache_read") or 0
    cache_write_5m = details.get("ephemeral_5m_input_tokens") or 0
    cache_write_1h = details.get("ephemeral_1h_input_tokens") or 0
    # usage_metadata["input_tokens"]はcache_read/cache_write分も含めた実質合計のため、
    # 通常単価で課金される「非キャッシュ分」は差し引いて求める
    regular_input = usage_metadata.get("input_tokens", 0) - cache_read - cache_write_5m - cache_write_1h
    output_tokens = usage_metadata.get("output_tokens", 0)

    cost = (
        regular_input * price["input"]
        + cache_write_5m * price["input"] * 1.25
        + cache_write_1h * price["input"] * 2.0
        + cache_read * price["input"] * 0.1
        + output_tokens * price["output"]
    ) / 1_000_000
    return cost


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


def _pr_content_block(pr_content: str) -> dict:
    """全観点で共通のPR全文ブロック。先頭に固定し cache_control を付けることで、
    観点ごとに異なる指示文（このブロックの後に続く別ブロック）が変わってもキャッシュヒットを狙える。
    """
    return {
        "type": "text",
        "text": f"## レビュー対象のPR内容\n\n{pr_content}",
        "cache_control": {"type": "ephemeral"},
    }


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
    cost_total_usd: float
    status: str              # "in_progress" | "done" | "aborted"
    abort_reason: dict | None
    next_step: str
    single_perspective: bool  # Trueの場合、指定した1観点のみ処理してsynthesizeへ進む
    unresolved_ids: list[int]  # MAX_RETRIES超過でスキップされた観点のid一覧（review/checkの意見が収束しなかった）


def _abort_reason(state: ReviewState, idx: int) -> dict:
    p = PERSPECTIVES[idx]
    if state["cost_total_usd"] >= COST_BUDGET_USD:
        return {
            "step": f"観点 {idx + 1}/{TOTAL}: {p['name']} の処理中",
            "reason": (
                f"1回のレビューで消費した金額が上限（${COST_BUDGET_USD:.2f}）に達したため中断しました"
                f"（実測: ${state['cost_total_usd']:.2f}、{state['token_total']}トークン）。"
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
        state["cost_total_usd"] >= COST_BUDGET_USD
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
        SystemMessage(
            content=[
                _pr_content_block(state["pr_content"]),
                {"type": "text", "text": p["review_prompt"] + feedback_section},
            ]
        ),
        HumanMessage(content="上記PR内容を、指定された観点でレビューしてください。"),
    ]
    raw = structured_llm.invoke(messages)
    result: ReviewResult = raw["parsed"]
    result.perspective_id = idx
    result.perspective_name = p["name"]

    state["token_total"] += (raw["raw"].usage_metadata or {}).get("total_tokens", 0)
    state["cost_total_usd"] += _estimate_cost_usd(REVIEW_MODEL, raw["raw"].usage_metadata)
    state["iteration_total"] += 1

    results = dict(state["results"])
    results[idx] = result.model_dump()
    state["results"] = results

    attempt = state["redo_counts"].get(idx, 0) + 1
    Path("review_results").mkdir(exist_ok=True)
    Path(f"review_results/result_{idx}_attempt{attempt}.json").write_text(
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
        SystemMessage(
            content=[
                _pr_content_block(state["pr_content"]),
                {"type": "text", "text": p["checker_prompt"]},
            ]
        ),
        HumanMessage(
            content=f"## レビュー結果\n\n```json\n{json.dumps(result, ensure_ascii=False, indent=2)}\n```"
        ),
    ]
    raw = structured_llm.invoke(messages)
    check_result: CheckResult = raw["parsed"]
    check_result.perspective_id = idx

    state["token_total"] += (raw["raw"].usage_metadata or {}).get("total_tokens", 0)
    state["cost_total_usd"] += _estimate_cost_usd(CHECK_MODEL, raw["raw"].usage_metadata)
    state["iteration_total"] += 1

    checks = dict(state["checks"])
    checks[idx] = check_result.model_dump()
    state["checks"] = checks

    attempt = state["redo_counts"].get(idx, 0) + 1
    Path(f"review_results/check_{idx}_attempt{attempt}.json").write_text(
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
        if state["single_perspective"]:
            state["next_step"] = "synthesize"
        else:
            state["next_step"] = "synthesize" if next_idx >= TOTAL else "review"
        return state

    redo_count = state["redo_counts"].get(idx, 0)
    if redo_count < MAX_RETRIES:
        state["redo_counts"] = {**state["redo_counts"], idx: redo_count + 1}
        state["redo_total"] += 1
        state["next_step"] = "review"  # 同じ観点をやり直す（idxは変えない）
        return state

    print(f"[review-graph] 観点 {idx} は最大リトライ回数を超えました。スキップします。", file=sys.stderr)
    state["unresolved_ids"] = [*state["unresolved_ids"], idx]
    next_idx = idx + 1
    state["idx"] = next_idx
    if state["single_perspective"]:
        state["next_step"] = "synthesize"
    else:
        state["next_step"] = "synthesize" if next_idx >= TOTAL else "review"
    return state


def _unresolved_section(state: ReviewState) -> str:
    """MAX_RETRIES超過でスキップされた観点を、LLMを介さず確定的にレポートへ追記する。
    review/checkの意見が収束しなかった箇所なので、AIの要約に頼らず人間の確認を促す。
    """
    if not state["unresolved_ids"]:
        return ""
    lines = [
        "\n\n---\n\n## 未解決の意見対立（人間の確認が必要）\n",
        f"以下の観点は、review/checkの意見が最大リトライ回数（{MAX_RETRIES}回）を超えても収束しませんでした。"
        "AIの判定を鵜呑みにせず、人間が直接確認してください。\n",
    ]
    for idx in state["unresolved_ids"]:
        p = PERSPECTIVES[idx]
        last_check = state["checks"].get(idx, {})
        lines.append(f"- **{p['name']}**: {last_check.get('feedback', '(フィードバックなし)')}")
    return "\n".join(lines) + "\n"


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
    state["cost_total_usd"] += _estimate_cost_usd(REVIEW_MODEL, response.usage_metadata)

    report = response.content + _unresolved_section(state)
    Path("review_results/final_report.md").write_text(report, encoding="utf-8")
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
    parser.add_argument(
        "--perspective-id",
        type=int,
        default=None,
        help="指定したid（perspectives/config.pyのid、0始まり）の観点のみを実行する。省略時は全観点を順に実行する。",
    )
    args = parser.parse_args()

    if args.perspective_id is not None and not (0 <= args.perspective_id < TOTAL):
        parser.error(f"--perspective-id は 0〜{TOTAL - 1} の範囲で指定してください")

    initial_state: ReviewState = {
        "pr_content": Path(args.pr_file).read_text(encoding="utf-8"),
        "idx": args.perspective_id if args.perspective_id is not None else 0,
        "results": {},
        "checks": {},
        "redo_counts": {},
        "redo_total": 0,
        "iteration_total": 0,
        "token_total": 0,
        "cost_total_usd": 0.0,
        "status": "in_progress",
        "abort_reason": None,
        "next_step": "",
        "single_perspective": args.perspective_id is not None,
        "unresolved_ids": [],
    }

    graph = build_graph()
    final_state = graph.invoke(initial_state, config={"recursion_limit": 100})

    if final_state["status"] == "aborted":
        print(json.dumps(final_state["abort_reason"], ensure_ascii=False))
        sys.exit(1)

    print("review_results/final_report.md")


if __name__ == "__main__":
    main()
