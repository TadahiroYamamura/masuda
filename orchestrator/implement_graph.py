"""
Phase 4 (implement) orchestrator.

Runs inside the Docker sandbox (see ADR-0012's phase table), invoked by
runtime/CLAUDE.md's loop against a worktree that already has an approved
PLAN.md (G1 passed in phase 1-2). Responsibilities:

  - Delegate implementation to a subagent and read back its self-reported
    outcome (implementation_result.json)
  - Run the ADR-0010 mechanical backstop: compare files actually touched
    against PLAN.md's declared file list, with no LLM involved
  - On any non-clean outcome (self-reported plan deviation, exhausted
    build/test retries, or a mechanical mismatch), reopen G1 (ADR-0009,
    ADR-0010) by writing DEVIATION.md and resetting the gate marker, rather
    than inventing a new gate type
  - Overwrite TASK.md with the next instruction; exit -- the self-looping
    Claude session (this time inside Docker, with full write/Edit/Bash) picks
    it up from there

No LLM calls happen in this process -- pure state machine over the
filesystem, same design as investigate_plan_graph.py.
"""
import json
import re
import subprocess
from pathlib import Path
from typing import TypedDict

from langgraph.graph import END, StateGraph

PLAN_MD = Path("PLAN.md")
IMPLEMENTATION_RESULT_JSON = Path("implementation_result.json")
DEVIATION_MD = Path("DEVIATION.md")
GATE_MARKER = Path(".masuda-gate/plan.json")
TASK_MD = Path("TASK.md")

# Files masuda's own machinery writes into the worktree -- never part of what
# the mechanical backstop (ADR-0010) judges as an "implementation change".
_MASUDA_INTERNAL_FILES = {
    "TASK.md",
    "PLAN.md",
    "INVESTIGATION.md",
    "plan_result.json",
    ".masuda-task.md",
    ".masuda-plan-system-prompt.md",
    str(IMPLEMENTATION_RESULT_JSON),
    str(DEVIATION_MD),
}
_MASUDA_INTERNAL_PREFIXES = (".masuda-gate/",)


class State(TypedDict):
    phase: str
    reason: str


def _read_plan_md() -> str:
    if not PLAN_MD.exists():
        raise FileNotFoundError(f"{PLAN_MD} not found — phase 4 requires an approved PLAN.md from G1")
    return PLAN_MD.read_text(encoding="utf-8")


def _read_implementation_result() -> dict | None:
    if not IMPLEMENTATION_RESULT_JSON.exists():
        return None
    return json.loads(IMPLEMENTATION_RESULT_JSON.read_text(encoding="utf-8"))


def _extract_planned_files(plan_md: str) -> set[str]:
    """Pulls the backtick-quoted paths out of PLAN.md's "## 変更するファイル一覧"
    section (top-level bullets only -- indented sub-bullets are the per-file
    explanation, not additional files). This is a hard requirement on that
    section's format, not a heuristic: ADR-0010's mechanical backstop only
    works because PLAN.md is required to spell out the file list concretely.
    """
    match = re.search(r"^## 変更するファイル一覧\s*\n(.*?)(?=\n## |\Z)", plan_md, re.MULTILINE | re.DOTALL)
    if not match:
        return set()
    files = set()
    for line in match.group(1).splitlines():
        if not line.startswith("- "):
            continue
        m = re.search(r"`([^`]+)`", line)
        if m:
            files.add(m.group(1).strip())
    return files


def _actual_changed_files() -> set[str]:
    out = subprocess.run(
        ["git", "status", "--porcelain"], capture_output=True, text=True, check=True
    ).stdout
    files = set()
    for line in out.splitlines():
        if not line.strip():
            continue
        path = line[3:]
        if " -> " in path:  # rename: "old -> new"
            path = path.split(" -> ", 1)[1]
        path = path.strip().strip('"')
        if path in _MASUDA_INTERNAL_FILES or path.startswith(_MASUDA_INTERNAL_PREFIXES):
            continue
        files.add(path)
    return files


def _mechanical_deviation() -> str | None:
    """Returns a human-readable reason if files were touched outside PLAN.md's
    declared list, or None if the diff stays within plan. LLM-free by design
    (ADR-0010) -- this must not depend on the implementation subagent's own
    judgment to be a real backstop.
    """
    planned = _extract_planned_files(_read_plan_md())
    actual = _actual_changed_files()
    extra = actual - planned
    if not extra:
        return None
    return (
        "計画外のファイルへの変更を検知しました（機械的バックストップ、ADR-0010）:\n"
        + "\n".join(f"- {f}" for f in sorted(extra))
        + "\n\nPLAN.mdの「変更するファイル一覧」:\n"
        + ("\n".join(f"- {f}" for f in sorted(planned)) if planned else "(空 — PLAN.mdの構成を確認してください)")
    )


def detect_phase(state: State) -> State:
    result = _read_implementation_result()

    if result is None:
        return {"phase": "implement", "reason": ""}

    status = result.get("status")
    if status == "needs_plan_review":
        return {"phase": "plan_reopened", "reason": "実装エージェントの自己申告（一次防御）:\n" + result.get("reason", "")}
    if status == "build_test_failed":
        return {"phase": "plan_reopened", "reason": "ビルド/テストの自己修正が上限に達しました（ADR-0009）:\n" + result.get("details", "")}
    if status == "done":
        deviation = _mechanical_deviation()
        if deviation:
            return {"phase": "plan_reopened", "reason": deviation}
        return {"phase": "implementation_complete", "reason": ""}

    raise ValueError(f"unknown implementation_result.json status: {status!r}")


def _implement_task() -> str:
    plan = _read_plan_md()
    return f"""# TASK: 実装（フェーズ4）

新規コンテキストのサブエージェントにPLAN.mdに基づく実装を委譲せよ
（Dockerサンドボックス内で完結するため、フェーズ1-2のようなBash制限は不要。
write/Edit/Bash権限を持つ通常のサブエージェントでよい）。

## 実装前の準備
環境が未セットアップの場合、CLAUDE.md・README等を参照して依存解決
（`go mod download`・`npm install`等）を行ってから実装に入ること。

## 参照するPLAN.md
{plan}

## 逸脱時の対応（一次防御、ADR-0010）
実装中に計画から外れる必要があると気づいた場合、勝手に進めず作業を止め、
`implementation_result.json`に以下を書き出して終了せよ:
{{"status": "needs_plan_review", "reason": "<なぜ計画から外れる必要があるか>"}}

## ビルド/テストの自己修正ループ（ADR-0009）
実装後、自分でビルド・テストを実行し、失敗したら自己修正して再実行せよ。
最大3回まで試し、それでもグリーンにならない場合は
`implementation_result.json`に以下を書き出して終了せよ:
{{"status": "build_test_failed", "details": "<何を試し、なぜ失敗したか>"}}

## 完了条件
ビルド・テストがグリーンになったら、`implementation_result.json`に
{{"status": "done"}}を書き出すこと。
"""


_TERMINAL = {
    "implementation_complete": """# DONE (implementation complete)

実装が完了し、PLAN.mdの範囲内であることを機械的に確認しました。
フェーズ5（レビュー）はまだ実装されていません。
""",
}


def _plan_reopened_task(reason: str) -> str:
    return f"""# DONE (GATE: plan — reopened)

G1（プラン承認ゲート）を再オープンしました（ADR-0010）。

## 理由
{reason}

人間は `masuda plan show <branch>` で理由（DEVIATION.md）とPLAN.mdを確認し、
`masuda plan approve <branch>` / `masuda plan reject <branch> "<feedback>"` で応答してください。
承認・却下後は `masuda plan start <branch>` でフェーズ1-2に戻るか、
`masuda sandbox start <branch>` で実装をやり直してください。
"""


def write_task_md(state: State) -> State:
    phase = state["phase"]
    if phase == "implement":
        content = _implement_task()
    elif phase == "plan_reopened":
        DEVIATION_MD.write_text(state["reason"], encoding="utf-8")
        if GATE_MARKER.exists():
            GATE_MARKER.unlink()
        if IMPLEMENTATION_RESULT_JSON.exists():
            IMPLEMENTATION_RESULT_JSON.unlink()
        content = _plan_reopened_task(state["reason"])
    elif phase in _TERMINAL:
        content = _TERMINAL[phase]
    else:
        raise ValueError(f"unknown phase: {phase}")

    TASK_MD.write_text(content, encoding="utf-8")
    print(f"[orchestrator] TASK.md written (phase={phase})")
    return state


def build_graph():
    g = StateGraph(State)
    g.add_node("detect_phase", detect_phase)
    g.add_node("write_task_md", write_task_md)
    g.set_entry_point("detect_phase")
    g.add_edge("detect_phase", "write_task_md")
    g.add_edge("write_task_md", END)
    return g.compile()


if __name__ == "__main__":
    app = build_graph()
    app.invoke({"phase": "", "reason": ""})

    print("\n--- TASK.md ---")
    print(TASK_MD.read_text(encoding="utf-8"))
