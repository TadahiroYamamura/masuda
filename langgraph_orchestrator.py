"""
LangGraph orchestrator for AI PR review.

ステートマシンのフェーズ:
  review → check → review（次の観点）→ ... → synthesize → done
                ↓ ok=false
              redo → check（再実行）

各フェーズで TASK.md を書き出して終了。TASK.md の指示自体はサブエージェントが実行する
（レビューの推論もサブエージェント自身が行う。Anthropic/OpenAIの従量課金APIは使わず、
Claude Codeのサブスクリプションの範囲内で完結させるための設計）。
Main Claude がサブエージェント経由でタスクを実行後に本スクリプトを再度呼び出す。
"""

import json
import sys
from pathlib import Path
from typing import TypedDict

from langgraph.graph import END, StateGraph

from perspectives.config import PERSPECTIVES

MAX_RETRIES = 2
ABORT_THRESHOLD = 5  # redo（差戻し）の累計発生回数がこれに達したら無限ループとみなして中断する
ITERATION_BUDGET = 30  # detect_stepの累計呼び出し回数がこれを超えたら中断する（redo以外の原因での無限ループ対策）
DEFAULT_PR = "pull-requests/0001.md"
STATE_FILE = Path("review_results/review_state.json")
TASK_FILE = Path("TASK.md")

TOTAL = len(PERSPECTIVES)


# ── State ─────────────────────────────────────────────────────────────────────

class OrchestratorState(TypedDict):
    action: str        # "review" | "redo" | "check" | "synthesize" | "done" | "abort"
    idx: int           # 現在の観点インデックス（synthesize/done 時は -1。abort 時は中断契機となった観点）
    pr_file: str


# ── State file helpers ────────────────────────────────────────────────────────

def _read_state() -> dict:
    return json.loads(STATE_FILE.read_text(encoding="utf-8"))


def _write_state(s: dict) -> None:
    STATE_FILE.write_text(json.dumps(s, ensure_ascii=False, indent=2), encoding="utf-8")


def _init_state() -> dict:
    return {
        "pr_file": DEFAULT_PR,
        "phase": "review",
        "current_idx": 0,
        "redo_counts": {},
        "redo_total": 0,
        "iteration_total": 0,
    }


# ── TASK.md templates ──────────────────────────────────────────────────────────
# レビューの推論自体をサブエージェント自身に行わせる（従量課金APIを使わず、
# Claude Codeのサブスクリプションの範囲内で完結させるため）。

def _task_review(idx: int, pr_file: str, is_redo: bool = False) -> str:
    p = PERSPECTIVES[idx]
    prefix = "やり直し: " if is_redo else ""
    redo_note = (
        f"\nやり直しの場合、`review_results/check_{idx}.json` に前回レビューへのフィードバック"
        "が記録されているので、それを確認し反映すること。\n"
        if is_redo else ""
    )
    return f"""\
# TASK: {prefix}PRレビュー — 観点 {idx + 1}/{TOTAL}: {p['name']}

## 指示
`{pr_file}` の内容を読み、以下の観点でレビューせよ。
{redo_note}
{p['review_prompt']}

レビュー結果を次のJSON形式で `review_results/result_{idx}.json` に書き出せ（問題が無い場合は `has_issues: false`, `issues: []` とすること）:

```json
{{
  "perspective_id": {idx},
  "perspective_name": "{p['name']}",
  "has_issues": true または false,
  "issues": [
    {{"severity": "高/中/低", "location": "問題箇所（ファイル名・行番号など）", "description": "問題の説明", "suggestion": "修正の提案"}}
  ],
  "summary": "レビュー結果の1〜2文の要約"
}}
```

## 完了条件
`review_results/result_{idx}.json` が存在すること
"""


def _task_check(idx: int, pr_file: str) -> str:
    p = PERSPECTIVES[idx]
    return f"""\
# TASK: レビュー検証 — 観点 {idx + 1}/{TOTAL}: {p['name']}

## 指示
`{pr_file}` の内容と `review_results/result_{idx}.json` のレビュー結果を確認し、以下の観点で検証せよ。

{p['checker_prompt']}

検証結果を次のJSON形式で `review_results/check_{idx}.json` に書き出せ（ok=trueの場合、feedbackは空文字とすること）:

```json
{{"perspective_id": {idx}, "ok": true または false, "feedback": "ok=falseの場合、見落とし・誤検知の具体的な説明"}}
```

## 完了条件
`review_results/check_{idx}.json` が存在すること
"""


def _task_synthesize(pr_file: str) -> str:
    return f"""\
# TASK: 最終レポート生成

## 指示
`{pr_file}` の内容と、`review_results/result_0.json` 〜 `review_results/result_{TOTAL - 1}.json`（存在するもののみ）の各観点のレビュー結果を確認し、複数の観点からのレビュー結果を統合した、開発者向けの最終レポートをMarkdown形式で作成せよ。

レポートの構成:
1. ## サマリー（問題の総数、深刻度の内訳、1〜2文の総評）
2. ## 問題一覧（問題があった観点のみ。深刻度 高→低 の順）
3. ## 問題なし（問題が検出されなかった観点の一覧）

箇条書きを活用し、開発者がすぐに修正に着手できる具体的な記述にすること。

作成したレポートを `review_results/final_report.md` に書き出せ。

## 完了条件
`review_results/final_report.md` が存在すること
"""


# ── Nodes ─────────────────────────────────────────────────────────────────────

def detect_step(state: OrchestratorState) -> OrchestratorState:
    Path("review_results").mkdir(exist_ok=True)

    if not STATE_FILE.exists():
        s = _init_state()
        s["iteration_total"] = 1
        _write_state(s)
        return {"action": "review", "idx": 0, "pr_file": s["pr_file"]}

    s = _read_state()
    s["iteration_total"] = s.get("iteration_total", 0) + 1
    if s["iteration_total"] > ITERATION_BUDGET:
        _write_state(s)
        return {"action": "abort", "idx": s["current_idx"], "pr_file": s["pr_file"]}
    _write_state(s)

    phase = s["phase"]
    idx = s["current_idx"]
    pr_file = s["pr_file"]

    # ── review フェーズ: result_N.json の生成を待つ ──────────────────────────
    if phase == "review":
        if not Path(f"review_results/result_{idx}.json").exists():
            is_redo = s["redo_counts"].get(str(idx), 0) > 0
            return {"action": "redo" if is_redo else "review", "idx": idx, "pr_file": pr_file}
        # result が存在 → check フェーズへ遷移
        s["phase"] = "check"
        _write_state(s)
        return {"action": "check", "idx": idx, "pr_file": pr_file}

    # ── check フェーズ: check_N.json の生成を待つ ────────────────────────────
    if phase == "check":
        check_file = Path(f"review_results/check_{idx}.json")
        if not check_file.exists():
            return {"action": "check", "idx": idx, "pr_file": pr_file}

        check_data = json.loads(check_file.read_text(encoding="utf-8"))

        if check_data["ok"]:
            return _advance(s, idx, pr_file)

        redo_count = s["redo_counts"].get(str(idx), 0)
        if redo_count < MAX_RETRIES:
            redo_total = s.get("redo_total", 0) + 1
            s["redo_total"] = redo_total
            if redo_total >= ABORT_THRESHOLD:
                _write_state(s)
                return {"action": "abort", "idx": idx, "pr_file": pr_file}

            # リドー: result を削除（check はサブエージェントがフィードバック参照用に使う）
            Path(f"review_results/result_{idx}.json").unlink(missing_ok=True)
            s["redo_counts"][str(idx)] = redo_count + 1
            s["phase"] = "review"
            _write_state(s)
            return {"action": "redo", "idx": idx, "pr_file": pr_file}

        # 最大リトライ超過 → スキップして次へ
        print(f"[orchestrator] 観点 {idx} は最大リトライ回数を超えました。スキップします。", file=sys.stderr)
        return _advance(s, idx, pr_file)

    # ── synthesize フェーズ ───────────────────────────────────────────────────
    if phase == "synthesize":
        if not Path("review_results/final_report.md").exists():
            return {"action": "synthesize", "idx": -1, "pr_file": pr_file}
        s["phase"] = "done"
        _write_state(s)
        return {"action": "done", "idx": -1, "pr_file": pr_file}

    return {"action": "done", "idx": -1, "pr_file": pr_file}


def _advance(s: dict, idx: int, pr_file: str) -> OrchestratorState:
    """現在の観点を完了し、次の観点または synthesize に遷移する。"""
    next_idx = idx + 1
    if next_idx >= TOTAL:
        s["phase"] = "synthesize"
        _write_state(s)
        return {"action": "synthesize", "idx": -1, "pr_file": pr_file}
    s["current_idx"] = next_idx
    s["phase"] = "review"
    _write_state(s)
    return {"action": "review", "idx": next_idx, "pr_file": pr_file}


def write_task_md(state: OrchestratorState) -> OrchestratorState:
    action = state["action"]
    idx = state["idx"]
    pr_file = state["pr_file"]

    if action in ("done", "abort"):
        TASK_FILE.unlink(missing_ok=True)
        return state

    match action:
        case "review":
            content = _task_review(idx, pr_file)
        case "redo":
            content = _task_review(idx, pr_file, is_redo=True)
        case "check":
            content = _task_check(idx, pr_file)
        case _:  # synthesize
            content = _task_synthesize(pr_file)

    TASK_FILE.write_text(content, encoding="utf-8")
    print(f"[orchestrator] TASK.md written (action={action}, idx={idx})", file=sys.stderr)
    return state


def _execution_directive(action: str) -> dict:
    return {"run": "claude", "agent": "general-purpose", "context": "new"}


def _abort_reason(idx: int) -> dict:
    s = _read_state()
    if s.get("iteration_total", 0) > ITERATION_BUDGET:
        step = f"観点 {idx + 1}/{TOTAL}: {PERSPECTIVES[idx]['name']} の処理中" if 0 <= idx < TOTAL else "レビュー全体"
        return {
            "step": step,
            "reason": (
                f"ループの反復回数が上限（{ITERATION_BUDGET}回）に達したため、無限ループ防止のため中断しました"
                f"（実測: {s['iteration_total']}回）。"
            ),
        }
    p = PERSPECTIVES[idx]
    return {
        "step": f"観点 {idx + 1}/{TOTAL}: {p['name']} のレビュー検証（check）フェーズ",
        "reason": f"redo（差戻し）が累計{ABORT_THRESHOLD}回発生したため、無限ループ防止のため中断しました。",
    }


# ── Graph ─────────────────────────────────────────────────────────────────────

def build_graph():
    g = StateGraph(OrchestratorState)
    g.add_node("detect_step", detect_step)
    g.add_node("write_task_md", write_task_md)
    g.set_entry_point("detect_step")
    g.add_edge("detect_step", "write_task_md")
    g.add_edge("write_task_md", END)
    return g.compile()


if __name__ == "__main__":
    app = build_graph()
    final_state = app.invoke({"action": "", "idx": 0, "pr_file": DEFAULT_PR})

    if final_state["action"] == "done":
        print("DONE")
    elif final_state["action"] == "abort":
        print("ABORT")
        print(json.dumps(_abort_reason(final_state["idx"]), ensure_ascii=False))
    else:
        print(json.dumps(_execution_directive(final_state["action"]), ensure_ascii=False))
