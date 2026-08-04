"""
Phase 1-2 (investigate -> plan -> G1) orchestrator.

Runs on the HOST (no Docker — see docs/adr/0012) against a worktree that
`masuda plan start` already created. Responsibilities:

  - Inspect on-disk state (INVESTIGATION.md / plan/summary.md+plan/steps.json
    (ADR-0026) / plan_result.json / the G1 gate marker
    `masuda plan approve|reject` writes) to derive the current phase
  - Overwrite TASK.md with instructions delegating the next step to a subagent
  - Exit -- the self-looping Claude session picks TASK.md up from there

No LLM calls happen in this process: it's a pure state machine over the
filesystem. The main Claude Code session (reading TASK.md per its loop
protocol) is the one that actually spawns subagents via its own Task tool.

All state that needs to survive across invocations (the redo counter) is
persisted to disk, not carried in the LangGraph state dict -- this script is
re-invoked as a fresh process every loop iteration, so anything not on disk is
lost. See ADR-0008 for the investigate<->plan redo protocol this implements.

All of masuda's own control files live under STATE_DIR (roadmap step 7's
workspace state directory, `MASUDA_STATE_DIR` env var), never inside the
worktree itself -- the worktree is the target repository's own git-managed
checkout, and nothing masuda writes should show up in its `git status`. The
worktree is still this process's cwd (investigator/planner subagents need
that to read repository content via relative paths), so every masuda-owned
path below is built as an absolute path under STATE_DIR.
"""
import json
import os
from pathlib import Path
from typing import TypedDict

from langgraph.graph import END, StateGraph

# ADR-0008: investigate<->plan redo budget -- bounds the investigate_redo
# loop specifically. Independent of ITERATION_BUDGET below (ADR-0011), which
# is a coarser, final-defense-line cap across every subagent invocation in
# this phase (investigate/plan/their redos), the same role
# feat/github-actions-langgraph-nodes's ITERATION_BUDGET played before this
# project moved off direct API calls (ADR-0001) -- that branch derived its
# 78 from "13 perspectives x up to 6 review/check calls each"; MAX_RETRIES
# already keeps investigate_redo tightly bounded here, so this budget mainly
# exists to cap plan_redo, which is gated by a human rejecting G1 repeatedly
# (ADR-0006) rather than by MAX_RETRIES, and so isn't otherwise bounded at
# all short of the human simply stopping.
MAX_RETRIES = 3
ITERATION_BUDGET = 20

STATE_DIR = Path(os.environ["MASUDA_STATE_DIR"])

TASK_BRIEF = STATE_DIR / ".masuda-task.md"
# Optional pre-written instructions/investigation document (masuda plan
# start --file, ADR-0016). When present, _investigate_task tells the
# investigator to fact-check it against the actual codebase rather than
# follow it blindly.
INSTRUCTIONS_MD = STATE_DIR / "INSTRUCTIONS.md"
INVESTIGATION_MD = STATE_DIR / "INVESTIGATION.md"
# ADR-0026: PLAN.md is no longer one Markdown file. Prose lives in
# summary.md; the mechanically-consumed step/file breakdown lives in
# steps.json (one JSON array, each element a {"description", "files"} step --
# `internal/gate/gate.go`'s renderPlan assembles both into the Markdown
# `masuda plan show` prints).
PLAN_DIR = STATE_DIR / "plan"
PLAN_SUMMARY_MD = PLAN_DIR / "summary.md"
PLAN_STEPS_JSON = PLAN_DIR / "steps.json"
PLAN_RESULT_JSON = STATE_DIR / "plan_result.json"
RETRIES_FILE = STATE_DIR / ".masuda-plan-retries"
ITERATION_COUNT_FILE = STATE_DIR / ".masuda-iteration-count"
GATE_MARKER = STATE_DIR / ".masuda-gate" / "plan.json"
TASK_MD = STATE_DIR / "TASK.md"

# Phases that write_task_md delegates to an actual subagent Task call --
# every other phase (gate waits, terminal DONE states) doesn't invoke one,
# so isn't counted against ITERATION_BUDGET.
_SUBAGENT_PHASES = {"investigate", "investigate_redo", "plan", "plan_redo"}


class State(TypedDict):
    phase: str
    retries: int
    questions: list[str]


def _read_task_brief() -> str:
    if not TASK_BRIEF.exists():
        raise FileNotFoundError(f"{TASK_BRIEF} not found — `masuda plan start` should have written it")
    return TASK_BRIEF.read_text(encoding="utf-8").strip()


def _read_retries() -> int:
    if not RETRIES_FILE.exists():
        return 0
    return int(RETRIES_FILE.read_text(encoding="utf-8").strip() or "0")


def _write_retries(n: int) -> None:
    RETRIES_FILE.write_text(str(n), encoding="utf-8")


def _read_plan_result() -> dict | None:
    if not PLAN_RESULT_JSON.exists():
        return None
    return json.loads(PLAN_RESULT_JSON.read_text(encoding="utf-8"))


def _read_iteration_count() -> int:
    if not ITERATION_COUNT_FILE.exists():
        return 0
    return int(ITERATION_COUNT_FILE.read_text(encoding="utf-8").strip() or "0")


def _record_iteration() -> int:
    """Call exactly once per subagent-invoking phase write_task_md renders
    (ADR-0011: the budget counts subagent invocations, not LLM calls -- one
    write_task_md call for a _SUBAGENT_PHASES phase is exactly one Task
    delegation the main session is about to make). Returns the new total."""
    n = _read_iteration_count() + 1
    ITERATION_COUNT_FILE.write_text(str(n), encoding="utf-8")
    return n


def _read_gate_marker() -> dict | None:
    """Reads the G1 marker `masuda plan approve|reject` writes.

    Must stay compatible with internal/gate/gate.go's Marker struct
    ({"status", "feedback", "decided_at"}) on the Go CLI side — see
    orchestrator/tests/test_investigate_plan_graph.py's
    test_gate_marker_schema_matches_go_cli for the contract check.
    """
    if not GATE_MARKER.exists():
        return None
    return json.loads(GATE_MARKER.read_text(encoding="utf-8"))


# ---------------------------------------------------------------------------
# Nodes
# ---------------------------------------------------------------------------

def detect_phase(state: State) -> State:
    """Derive the current phase from file-system state.

    Has two side effects on transition, both "consuming" a one-shot signal so
    the next invocation doesn't re-trigger the same transition forever:
      - a rejected G1 marker is deleted once folded into a plan_redo
      - a needs_more_investigation plan_result.json is deleted once folded
        into an investigate_redo
    """
    if PLAN_SUMMARY_MD.exists() and PLAN_STEPS_JSON.exists():
        marker = _read_gate_marker()
        status = (marker or {}).get("status", "pending")
        if status == "approved":
            return {"phase": "g1_approved", "retries": _read_retries(), "questions": []}
        if status == "rejected":
            feedback = marker.get("feedback", "")
            GATE_MARKER.unlink()
            return {"phase": "plan_redo", "retries": _read_retries(), "questions": [feedback]}
        return {"phase": "await_g1", "retries": _read_retries(), "questions": []}

    plan_result = _read_plan_result()
    if plan_result and plan_result.get("status") == "needs_more_investigation":
        retries = _read_retries()
        if retries >= MAX_RETRIES:
            return {"phase": "retries_exhausted", "retries": retries, "questions": []}
        questions = plan_result.get("questions", [])
        PLAN_RESULT_JSON.unlink()
        _write_retries(retries + 1)
        return {"phase": "investigate_redo", "retries": retries + 1, "questions": questions}

    if not INVESTIGATION_MD.exists():
        return {"phase": "investigate", "retries": _read_retries(), "questions": []}

    return {"phase": "plan", "retries": _read_retries(), "questions": []}


def _investigate_task(task: str, questions: list[str]) -> str:
    extra = ""
    if questions:
        qlist = "\n".join(f"- {q}" for q in questions)
        extra = f"""

## 追加調査事項（プランエージェントからの差し戻し）
以下の疑問点を追加で調査し、INVESTIGATION.mdに反映せよ:
{qlist}
"""

    instructions_section = ""
    if INSTRUCTIONS_MD.exists():
        instructions_section = f"""

## 事前に用意された指示書の検証（`masuda plan start --file`で渡された）
`{INSTRUCTIONS_MD}` にユーザーが事前に用意した指示書がある。まずこれを読み、
記載内容（前提・指示している変更内容・参照しているファイルや関数など）が
実際のコードベースと矛盾しないか、実現可能かを検証せよ。
問題（事実誤認・矛盾・実現困難な点・不足している考慮事項など）が見つかった
場合は、INVESTIGATION.mdに「指示書の検証結果」という節を設けて具体的に指摘
すること。問題がなければその旨を明記した上で、指示書の内容を調査の前提として
活用してよい。
"""
    verification_bullet = "\n- 指示書の検証結果（問題点の指摘、または問題なしの明記）" if INSTRUCTIONS_MD.exists() else ""

    return f"""# TASK: 調査（フェーズ1）

Task toolで `subagent_type: investigator` を指定し、新規コンテキストのサブエージェントに
以下のタスクの調査を委譲し、`{INVESTIGATION_MD}`を生成させよ（コードの調査自体はカレント
ディレクトリ＝worktreeに対して行うが、成果物はこの絶対パスに書き出すこと）。
（investigatorはBashを持たないread-onlyエージェントとして定義済み。他のsubagent_typeは使わないこと）

## タスク内容
{task}
{extra}{instructions_section}
## {INVESTIGATION_MD.name}の構成
- タスクの要約
- 関連ファイル・モジュール一覧（役割の説明付き）
- 既存の類似実装・従うべきパターン
- 制約・注意点
- 未解決の疑問点{verification_bullet}

## 完了条件
`{INVESTIGATION_MD}` が存在すること
"""


def _plan_task(feedback: str | None) -> str:
    # PLAN_DIR is created here (idempotent) rather than left to the planner
    # subagent's Edit tool -- this process runs unsandboxed on the host, so
    # there's no reason to gamble on Edit's own parent-directory handling for
    # a brand new subdirectory (ADR-0026).
    PLAN_DIR.mkdir(parents=True, exist_ok=True)

    redo_note = ""
    if feedback:
        redo_note = f"""

## 差し戻し理由（G1で却下）
{feedback}

上記を踏まえてプランを見直せ。
"""
    return f"""# TASK: プラン作成（フェーズ2）

Task toolで `subagent_type: planner` を指定し、新規コンテキストのサブエージェントに
`{INVESTIGATION_MD}`を渡し、`{PLAN_SUMMARY_MD}`と`{PLAN_STEPS_JSON}`を生成させよ
（コードの追加調査自体はカレントディレクトリ＝worktreeに対して行うが、成果物は
この絶対パスに書き出すこと）。
（plannerはBashを持たないread-onlyエージェントとして定義済み。他のsubagent_typeは使わないこと）
プランエージェントはRead/Grep/Globアクセスを持つため、
{INVESTIGATION_MD.name}の軽微な不足は自分で追加調査して自己解決してよい。

ただし調査の前提が崩れるような大きなギャップがある場合は、独自に調査をやり直さず
`{PLAN_RESULT_JSON}`に`{{"status": "needs_more_investigation", "questions": [...]}}`
を書き出させること（この場合{PLAN_SUMMARY_MD.name}・{PLAN_STEPS_JSON.name}は書かない）。
{redo_note}
## {PLAN_SUMMARY_MD.name}の構成（自由記述のprose、人間向け）
- アプローチの要約
- テスト方針
- 検討したが採用しなかった代替案
- リスク・懸念事項

## {PLAN_STEPS_JSON.name}の構成（機械的にパースされるJSON。有効なJSONオブジェクトを1個だけ書くこと）
実装のステップ分解を、ステップごとに「そのステップで変更するファイル一覧」まで含めて
以下の形で書くこと。ファイルの追加・変更理由は各ファイルの`description`に書く
（{PLAN_SUMMARY_MD.name}側には書かない）。

```json
{{
  "steps": [
    {{
      "description": "ステップ1の説明（このステップで何を実装するか）",
      "files": [
        {{"path": "internal/foo/bar.go", "description": "〜のため〜を追加"}},
        {{"path": "internal/foo/bar_test.go", "description": "上記のテスト"}}
      ]
    }},
    {{"description": "ステップ2の説明", "files": [...]}}
  ],
  "expected_byproducts": ["**/__pycache__/**", "**/*.pyc"]
}}
```

ステップは実装を安全に区切れる単位（1コミットとして意味を持つまとまり）に分けること。
各ステップの`files`はそのステップで実際に変更するファイルに絞り、他のステップで扱う
ファイルを含めないこと（フェーズ4の機械的バックストップがステップ単位で検証するため）。

`expected_byproducts`は、このプロジェクトのビルド・テストツールチェーンが副作用として
生成しうるファイルパターンの配列。調査（INVESTIGATION.md）で判明した範囲で予想すること
（例: Pythonプロジェクトなら`**/__pycache__/**`、コード生成ツールが既存ファイルを書き換える
ならそのファイル名）。実装エージェントがテスト実行等で意図せず生成・変更してしまう
この種のファイルは、機械的バックストップの逸脱判定から除外される（実装エージェント
自身の事後申告ではなく、G1で人間が事前承認したこの予想だけが除外に使われる）。心当たり
が無ければ空配列でよい——予想が外れて実際に副産物が生成された場合は従来通りG1再オープン
で人間が判断するだけで、安全側に倒れる。

パターンは一般的なglob記法（シェルや`.gitignore`と同じ）で書くこと。`*`は`/`をまたがず
1階層内だけにマッチし、`**`は0階層以上のディレクトリをまたいでマッチする。ルート直下・
任意の深さのネスト先どちらの`__pycache__`も対象にしたいなら`**/__pycache__/**`のように
明示的に`**`を使うこと（`*__pycache__*`のような単独`*`では階層をまたげず、ネストした
場所を取りこぼす）。

## 完了条件
（`{PLAN_SUMMARY_MD}` と `{PLAN_STEPS_JSON}` の両方）または `{PLAN_RESULT_JSON}` が存在すること
"""


_TERMINAL = {
    "await_g1": f"""# GATE:plan

プランが完成し、G1（プラン承認ゲート）の判断待ちです。セッションは終了せず、
`{GATE_MARKER}`のstatusがpendingでなくなるまで待機してください。

人間は `masuda plan show <workspace-id>` でプランを確認し、
`masuda plan chat <workspace-id>` で対話するか、
`masuda plan approve <workspace-id>` / `masuda plan reject <workspace-id> "<feedback>"` で応答してください。
""",
    "g1_approved": """# DONE (G1 approved)

G1が承認されました。フェーズ3（プロジェクト初期化）以降はまだ実装されていません。
""",
    "retries_exhausted": f"""# DONE (blocked)

調査とプラン作成の往復が上限（MAX_RETRIES={MAX_RETRIES}）に達しました。
`plan_result.json`の内容を確認し、人間の判断が必要です。
""",
    "iteration_budget_exceeded": f"""# DONE (blocked)

サブエージェント起動回数が上限（ITERATION_BUDGET={ITERATION_BUDGET}）に
達しました（ADR-0011、無限ループ防止の最終防衛ライン）。個々のredoループ
（MAX_RETRIES等）は正常に機能しているはずで、これはそれとは独立した
全体の保険です。人間の判断が必要です。
""",
}


def write_task_md(state: State) -> State:
    phase = state["phase"]
    if phase in _SUBAGENT_PHASES and _record_iteration() > ITERATION_BUDGET:
        phase = "iteration_budget_exceeded"
    if phase == "investigate":
        content = _investigate_task(_read_task_brief(), [])
    elif phase == "investigate_redo":
        content = _investigate_task(_read_task_brief(), state["questions"])
    elif phase == "plan":
        content = _plan_task(None)
    elif phase == "plan_redo":
        content = _plan_task(state["questions"][0] if state["questions"] else None)
    elif phase in _TERMINAL:
        content = _TERMINAL[phase]
    else:
        raise ValueError(f"unknown phase: {phase}")

    TASK_MD.write_text(content, encoding="utf-8")
    print(f"[orchestrator] TASK.md written (phase={phase})")
    return state


# ---------------------------------------------------------------------------
# Graph
# ---------------------------------------------------------------------------

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
    app.invoke({"phase": "", "retries": 0, "questions": []})

    print("\n--- TASK.md ---")
    print(TASK_MD.read_text(encoding="utf-8"))
