"""
Phase 4-5 (implement -> review -> G2) orchestrator.

Runs inside the Docker sandbox (see ADR-0012's phase table), invoked by
runtime/CLAUDE.md's loop against a worktree that already has an approved
PLAN.md (G1 passed in phase 1-2). Phase 4 and phase 5 share one orchestrator
because they run in the same sandbox / same self-loop session (ADR-0013) --
this mirrors investigate_plan_graph.py covering both phase 1 and phase 2.

Responsibilities:
  - Phase 4: delegate implementation to a subagent, read back its
    self-reported outcome (implementation_result.json), and run the ADR-0010
    mechanical backstop (PLAN.md's declared file list vs `git status`, no
    LLM involved)
  - Phase 5: run the ported 13-perspective mechanical review/check loop
    (originally feat/github-actions-langgraph-nodes's direct-API graph, now
    delegated to subagents via TASK.md instead of calling the Anthropic API
    directly), then a cross-cutting explorer/verifier pass (ADR-0003 /
    ADR-0011 -- LSP-assisted consistency checks a diff-only mechanical
    perspective can't see), then synthesize a final report
  - On any phase 4 non-clean outcome (self-reported plan deviation, exhausted
    build/test retries, or the mechanical mismatch), reopen G1 by writing
    DEVIATION.md and resetting its gate marker (ADR-0009, ADR-0010)
  - On a G2 rejection, reopen phase 4 with the rejection feedback (ADR-0013)
    instead of inventing a new gate type; review starts over from scratch
    once the redo produces a clean implementation again
  - Overwrite TASK.md with the next instruction; exit -- the self-looping
    Claude session picks it up from there

No LLM calls happen in this process -- pure state machine over the
filesystem, same design as investigate_plan_graph.py.

All of masuda's own control files live under STATE_DIR (roadmap step 7's
workspace state directory, `MASUDA_STATE_DIR` env var, bind-mounted at
/masuda-state in the sandbox), never inside the worktree (/workspace) itself.
This is why _actual_changed_files()/_compute_diff() no longer need to exclude
a list of masuda-owned paths from `git status`/`git diff` output the way an
earlier version of this file did: those files simply never exist inside the
git-managed worktree in the first place, so there's nothing to filter out.
"""
import json
import os
import re
import subprocess
from pathlib import Path
from typing import TypedDict

from langgraph.graph import END, StateGraph

from perspectives.config import PERSPECTIVES

TOTAL_PERSPECTIVES = len(PERSPECTIVES)
# ADR-0008 used 3 for the investigate<->plan redo; this mirrors the original
# ported review graph's own MAX_RETRIES (2) for review<->check redo instead --
# an independent, per-domain constant, not shared with phase 1-2's.
MAX_REVIEW_RETRIES = 2

STATE_DIR = Path(os.environ["MASUDA_STATE_DIR"])

PLAN_MD = STATE_DIR / "PLAN.md"
BASE_REF_FILE = STATE_DIR / ".masuda-base-ref"
IMPLEMENTATION_RESULT_JSON = STATE_DIR / "implementation_result.json"
DEVIATION_MD = STATE_DIR / "DEVIATION.md"
APPROVED_DEVIATIONS_JSON = STATE_DIR / ".masuda-approved-deviations.json"
PLAN_GATE_MARKER = STATE_DIR / ".masuda-gate" / "plan.json"
REVIEW_GATE_MARKER = STATE_DIR / ".masuda-gate" / "review.json"
REVIEW_STATE_JSON = STATE_DIR / ".masuda-review-state.json"
REVIEW_FEEDBACK_MD = STATE_DIR / ".masuda-review-feedback.md"
REVIEW_RESULTS_DIR = STATE_DIR / "review_results"
FINAL_REPORT_MD = REVIEW_RESULTS_DIR / "final_report.md"
CROSS_CUTTING_FINDINGS_JSON = REVIEW_RESULTS_DIR / "cross_cutting_findings.json"
CROSS_CUTTING_VERIFIED_JSON = REVIEW_RESULTS_DIR / "cross_cutting_verified.json"
TASK_MD = STATE_DIR / "TASK.md"


class State(TypedDict):
    phase: str
    reason: str


def _read_plan_md() -> str:
    if not PLAN_MD.exists():
        raise FileNotFoundError(f"{PLAN_MD} not found — phase 4 requires an approved PLAN.md from G1")
    return PLAN_MD.read_text(encoding="utf-8")


def _read_base_ref() -> str:
    """The ref `masuda worktree create` / `masuda review start` recorded this
    worktree as branching from (worktree.BaseRefFileName on the Go side),
    defaulting to "develop" for worktrees created before this file existed.
    Used as the diff base -- both for phase 4-5's implementation diff and for
    a standalone `masuda review start` of an already fully-committed branch,
    where diffing against bare HEAD would show nothing (roadmap step 6)."""
    if not BASE_REF_FILE.exists():
        return "develop"
    return BASE_REF_FILE.read_text(encoding="utf-8").strip() or "develop"


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
        files.add(path.strip().strip('"'))
    return files


def _read_approved_deviations() -> set[str]:
    if not APPROVED_DEVIATIONS_JSON.exists():
        return set()
    return set(json.loads(APPROVED_DEVIATIONS_JSON.read_text(encoding="utf-8")))


def _write_approved_deviations(paths: set[str]) -> None:
    APPROVED_DEVIATIONS_JSON.write_text(json.dumps(sorted(paths), ensure_ascii=False), encoding="utf-8")


def _extra_changed_files() -> set[str]:
    """Files touched outside PLAN.md's declared list, minus any deviation a
    human has already approved through a prior G1 reopen -- without this
    exclusion, an approved deviation would look identical to a brand new one
    on the very next check and reopen the gate forever."""
    planned = _extract_planned_files(_read_plan_md())
    approved = _read_approved_deviations()
    return _actual_changed_files() - planned - approved


def _mechanical_deviation() -> str | None:
    """Returns a human-readable reason if files were touched outside PLAN.md's
    declared list (and not already an approved deviation), or None otherwise.
    LLM-free by design (ADR-0010) -- this must not depend on the
    implementation subagent's own judgment to be a real backstop.

    Skips entirely when there's no PLAN.md at all -- standalone review
    (`masuda review start`, roadmap step 6) never went through G1, so there's
    no plan to have deviated from; nothing to back-stop.
    """
    if not PLAN_MD.exists():
        return None
    extra = _extra_changed_files()
    if not extra:
        return None
    planned = _extract_planned_files(_read_plan_md())
    return (
        "計画外のファイルへの変更を検知しました（機械的バックストップ、ADR-0010）:\n"
        + "\n".join(f"- {f}" for f in sorted(extra))
        + "\n\nPLAN.mdの「変更するファイル一覧」:\n"
        + ("\n".join(f"- {f}" for f in sorted(planned)) if planned else "(空 — PLAN.mdの構成を確認してください)")
    )


def _compute_diff() -> str:
    """Stages everything (including new/deleted files) so the diff covers the
    full implementation, not just already-tracked modifications, then reports
    it via `git diff --cached <base ref>` -- nothing is committed.

    Diffing against the worktree's recorded base ref (not bare HEAD) matters
    for two cases: phase 4's uncommitted changes still show up (`git diff
    <ref>` compares a commit against the working tree, same as `git diff
    HEAD` would), and a standalone review's fully-committed branch (roadmap
    step 6) shows its real diff instead of nothing -- `git diff HEAD` on an
    already-committed branch has nothing to show since HEAD *is* the tip.

    `git add -A` used to also stage masuda's own scratch files (they lived
    inside the worktree, so `git status`/`git add -A` saw them as new,
    untracked paths just like real implementation files) which had to be
    unstaged again before diffing. Since roadmap step 7 moved all of masuda's
    control files out to STATE_DIR, they're outside this git worktree
    entirely and never appear here in the first place.
    """
    subprocess.run(["git", "add", "-A"], check=True)
    return subprocess.run(
        ["git", "diff", "--cached", _read_base_ref()], capture_output=True, text=True, check=True
    ).stdout


def _read_gate_marker(path: Path) -> dict | None:
    if not path.exists():
        return None
    return json.loads(path.read_text(encoding="utf-8"))


# --- phase 5 (review) state -------------------------------------------------

def _read_review_state() -> dict:
    if not REVIEW_STATE_JSON.exists():
        return {"idx": 0, "redo_counts": {}, "fix_counts": {}, "unresolved": [], "fixed": []}
    return json.loads(REVIEW_STATE_JSON.read_text(encoding="utf-8"))


def _write_review_state(rs: dict) -> None:
    REVIEW_STATE_JSON.write_text(json.dumps(rs, ensure_ascii=False), encoding="utf-8")


def _clear_review_state() -> None:
    """Wipes phase 5 state so a post-redo review starts from perspective 0
    (ADR-0013 — previous review/check results aren't reused after a fix)."""
    if REVIEW_STATE_JSON.exists():
        REVIEW_STATE_JSON.unlink()
    if REVIEW_RESULTS_DIR.exists():
        for f in REVIEW_RESULTS_DIR.iterdir():
            f.unlink()
        REVIEW_RESULTS_DIR.rmdir()


def _result_path(idx: int, attempt: int) -> Path:
    return REVIEW_RESULTS_DIR / f"result_{idx}_attempt{attempt}.json"


def _check_path(idx: int, attempt: int) -> Path:
    return REVIEW_RESULTS_DIR / f"check_{idx}_attempt{attempt}.json"


def _fix_path(idx: int, fix_attempt: int) -> Path:
    return REVIEW_RESULTS_DIR / f"fix_{idx}_fixattempt{fix_attempt}.json"


def _recheck_path(idx: int, fix_attempt: int) -> Path:
    return REVIEW_RESULTS_DIR / f"recheck_{idx}_fixattempt{fix_attempt}.json"


def _detect_review_phase() -> State:
    """Advances the perspective/redo/fix bookkeeping (pure, no LLM) until it
    lands on a phase that actually needs a subagent round.

    Two nested loops, mirroring two different ADRs:
      - review <-> check redo (ADR-0008-style, ported from the original
        review_node/check_node): is the review's *assessment* accurate?
      - fix <-> recheck redo (ADR-0004): once an issue is confirmed real,
        can a narrow-write fixer resolve it? checker and fixer stay separate
        roles so the fix is verified independently, not self-graded.

    Once all 13 mechanical perspectives are done, runs the cross-cutting
    explorer/verifier pass (ADR-0003 / ADR-0011) before synthesize -- a
    single pass with no redo loop, unlike the mechanical perspectives above:
    ADR-0011 deliberately keeps complex/cross-cutting findings out of the
    auto-fix loop (they always need a human's judgment at G2), so there's
    no "assessment accuracy" to converge on the way review<->check redo
    exists for. explorer runs once; if it found nothing, verify is skipped
    entirely (nothing to independently confirm).
    """
    rs = _read_review_state()
    idx = rs["idx"]
    redo_counts = rs["redo_counts"]
    fix_counts = rs["fix_counts"]
    unresolved = rs["unresolved"]
    fixed = rs["fixed"]

    def persist():
        _write_review_state(
            {"idx": idx, "redo_counts": redo_counts, "fix_counts": fix_counts, "unresolved": unresolved, "fixed": fixed}
        )

    while idx < TOTAL_PERSPECTIVES:
        attempt = redo_counts.get(str(idx), 0) + 1
        if not _result_path(idx, attempt).exists():
            return {"phase": "review_perspective", "reason": json.dumps({"idx": idx, "attempt": attempt})}
        if not _check_path(idx, attempt).exists():
            return {"phase": "check_perspective", "reason": json.dumps({"idx": idx, "attempt": attempt})}

        check = json.loads(_check_path(idx, attempt).read_text(encoding="utf-8"))
        if not check.get("ok"):
            if redo_counts.get(str(idx), 0) < MAX_REVIEW_RETRIES:
                redo_counts[str(idx)] = redo_counts.get(str(idx), 0) + 1
            else:
                unresolved.append({"idx": idx, "reason": "review_check_not_converged"})
                idx += 1
            persist()
            continue

        result = json.loads(_result_path(idx, attempt).read_text(encoding="utf-8"))
        if not result.get("has_issues"):
            idx += 1
            persist()
            continue

        # Confirmed real issue(s) -- ADR-0004's fix <-> recheck loop.
        fix_attempt = fix_counts.get(str(idx), 0) + 1
        if not _fix_path(idx, fix_attempt).exists():
            return {
                "phase": "fix_perspective",
                "reason": json.dumps({"idx": idx, "attempt": attempt, "fix_attempt": fix_attempt}),
            }
        if not _recheck_path(idx, fix_attempt).exists():
            return {"phase": "recheck_perspective", "reason": json.dumps({"idx": idx, "fix_attempt": fix_attempt})}

        recheck = json.loads(_recheck_path(idx, fix_attempt).read_text(encoding="utf-8"))
        if recheck.get("resolved"):
            fixed.append(idx)
            idx += 1
        elif fix_counts.get(str(idx), 0) < MAX_REVIEW_RETRIES:
            fix_counts[str(idx)] = fix_counts.get(str(idx), 0) + 1
        else:
            unresolved.append({"idx": idx, "reason": "fix_not_resolved"})
            idx += 1
        persist()

    if not CROSS_CUTTING_FINDINGS_JSON.exists():
        return {"phase": "cross_cutting_explore", "reason": ""}
    findings = json.loads(CROSS_CUTTING_FINDINGS_JSON.read_text(encoding="utf-8"))
    if findings and not CROSS_CUTTING_VERIFIED_JSON.exists():
        return {"phase": "cross_cutting_verify", "reason": ""}

    return {"phase": "synthesize", "reason": ""}


def _detect_post_implementation_phase() -> State:
    """Implementation is clean (or already was) -- figure out where phase 5 /
    G2 currently stands."""
    if not FINAL_REPORT_MD.exists():
        return _detect_review_phase()

    marker = _read_gate_marker(REVIEW_GATE_MARKER)
    status = (marker or {}).get("status", "pending")
    if status == "approved":
        return {"phase": "g2_approved", "reason": ""}
    if status == "rejected":
        feedback = marker.get("feedback", "")
        reason = f"G2（レビュー承認ゲート）で却下されました（ADR-0013）:\n{feedback}\n\n修正後はレビューを最初の観点からやり直す。"
        REVIEW_GATE_MARKER.unlink()
        _clear_review_state()
        IMPLEMENTATION_RESULT_JSON.unlink()
        REVIEW_FEEDBACK_MD.write_text(reason, encoding="utf-8")
        return {"phase": "implement_redo", "reason": reason}
    return {"phase": "await_g2", "reason": ""}


def _resolve_plan_reopen(reason: str, mechanical: bool) -> State:
    """Shared by all three G1-reopen triggers (self-reported deviation,
    exhausted build/test retries, mechanical file-list mismatch, ADR-0009 /
    ADR-0010): open the gate on first detection (no DEVIATION.md yet), then
    branch on the eventual human decision once DEVIATION.md already exists --
    meaning this call happened because the gate was just resolved (the
    GATE:plan poll only re-invokes the orchestrator after that), not because
    we're seeing something new.

    Without branching on approve vs. reject here, an *approved* deviation
    would still look identical to a brand new one on the very next mechanical
    check and reopen the gate forever -- confirmed while wiring up GATE:plan's
    auto-resume (roadmap step 5); the previous design only worked because a
    human manually re-ran the right command instead of the loop resuming
    itself.
    """
    if not DEVIATION_MD.exists():
        return {"phase": "plan_reopened", "reason": reason}

    marker = _read_gate_marker(PLAN_GATE_MARKER)
    gate_status = (marker or {}).get("status", "pending")
    if gate_status == "pending":
        return {"phase": "plan_reopened", "reason": DEVIATION_MD.read_text(encoding="utf-8")}

    feedback = (marker or {}).get("feedback", "")
    DEVIATION_MD.unlink()
    if PLAN_GATE_MARKER.exists():
        PLAN_GATE_MARKER.unlink()

    if gate_status == "approved" and mechanical:
        # The deviation itself is accepted as-is; record it so future
        # mechanical checks don't flag the same files again, and proceed as
        # if implementation had been clean all along.
        approved = _read_approved_deviations()
        approved |= _extra_changed_files()
        _write_approved_deviations(approved)
        return _detect_post_implementation_phase()

    # Either rejected, or approved-but-self-reported (the subagent stopped
    # *before* acting, per its own instructions -- approval means "go do what
    # you proposed", which still needs another implementation turn).
    IMPLEMENTATION_RESULT_JSON.unlink()
    if gate_status == "approved":
        redo_reason = "G1再オープンが承認されました。提案した対応をそのまま進めてください。"
    else:
        redo_reason = f"G1再オープンが却下されました（ADR-0010）:\n{feedback}"
    return {"phase": "implement_redo", "reason": redo_reason}


def detect_phase(state: State) -> State:
    result = _read_implementation_result()

    if result is not None:
        status = result.get("status")
        if status == "needs_plan_review":
            reason = "実装エージェントの自己申告（一次防御）:\n" + result.get("reason", "")
            return _resolve_plan_reopen(reason, mechanical=False)
        if status == "build_test_failed":
            reason = "ビルド/テストの自己修正が上限に達しました（ADR-0009）:\n" + result.get("details", "")
            return _resolve_plan_reopen(reason, mechanical=False)
        if status == "done":
            deviation = _mechanical_deviation()
            if deviation:
                return _resolve_plan_reopen(deviation, mechanical=True)
            return _detect_post_implementation_phase()
        raise ValueError(f"unknown implementation_result.json status: {status!r}")

    if REVIEW_FEEDBACK_MD.exists():
        return {"phase": "implement_redo", "reason": REVIEW_FEEDBACK_MD.read_text(encoding="utf-8")}
    return {"phase": "implement", "reason": ""}


# --- TASK.md rendering -------------------------------------------------------

def _implement_task(redo_feedback: str | None = None) -> str:
    plan = _read_plan_md()
    redo_section = ""
    if redo_feedback:
        redo_section = f"""

## 差し戻し・追加対応の指示
{redo_feedback}

上記を踏まえて対応すること。
"""
    return f"""# TASK: 実装（フェーズ4）

新規コンテキストのサブエージェントに以下のPLAN.mdに基づく実装を委譲せよ
（Dockerサンドボックス内で完結するため、フェーズ1-2のようなBash制限は不要。
write/Edit/Bash権限を持つ通常のサブエージェントでよい。実装対象のコードは
カレントディレクトリ＝`/workspace`に対して行うこと）。

## 実装前の準備
環境が未セットアップの場合、CLAUDE.md・README等を参照して依存解決
（`go mod download`・`npm install`等）を行ってから実装に入ること。

## 参照するPLAN.md
{plan}
{redo_section}
## 逸脱時の対応（一次防御、ADR-0010）
実装中に計画から外れる必要があると気づいた場合、勝手に進めず作業を止め、
`{IMPLEMENTATION_RESULT_JSON}`に以下を書き出して終了せよ:
{{"status": "needs_plan_review", "reason": "<なぜ計画から外れる必要があるか>"}}

## ビルド/テストの自己修正ループ（ADR-0009）
実装後、自分でビルド・テストを実行し、失敗したら自己修正して再実行せよ。
最大3回まで試し、それでもグリーンにならない場合は
`{IMPLEMENTATION_RESULT_JSON}`に以下を書き出して終了せよ:
{{"status": "build_test_failed", "details": "<何を試し、なぜ失敗したか>"}}

## 完了条件
ビルド・テストがグリーンになったら、`{IMPLEMENTATION_RESULT_JSON}`に
{{"status": "done"}}を書き出すこと。
"""


def _plan_reopened_task(reason: str) -> str:
    return f"""# GATE:plan

G1（プラン承認ゲート）を再オープンしました（ADR-0010）。セッションは終了せず、
`{PLAN_GATE_MARKER}`のstatusがpendingでなくなるまで待機してください。

## 理由
{reason}

人間は `masuda plan show <workspace-id>` で理由（{DEVIATION_MD.name}）とPLAN.mdを確認し、
`masuda plan chat <workspace-id>` で対話するか、
`masuda plan approve <workspace-id>` / `masuda plan reject <workspace-id> "<feedback>"` で応答してください。
"""


def _review_perspective_task(idx: int, attempt: int) -> str:
    p = PERSPECTIVES[idx]
    diff = _compute_diff()
    feedback_section = ""
    if attempt > 1:
        prev_check = json.loads(_check_path(idx, attempt - 1).read_text(encoding="utf-8"))
        feedback_section = f"""

## 前回レビューへのフィードバック（要反映）
{prev_check.get("feedback", "")}
"""
    return f"""# TASK: レビュー（フェーズ5、観点 {idx + 1}/{TOTAL_PERSPECTIVES}: {p["name"]}）

新規コンテキストのサブエージェント（Bash/Read/Grep等は不要、diffのみで判断する
機械的チェック — 探索させないこと）に以下を委譲し、レビュー結果を
`{_result_path(idx, attempt)}`に書き出させよ。

## レビュー観点の指示
{p["review_prompt"]}
{feedback_section}
## レビュー対象のdiff
```diff
{diff}
```

## 出力するJSONのスキーマ
{{
  "perspective_id": {idx},
  "perspective_name": "{p["name"]}",
  "has_issues": <bool>,
  "issues": [{{"severity": "高|中|低", "location": "...", "description": "...", "suggestion": "..."}}],
  "summary": "<1〜2文の要約>"
}}

## 完了条件
`{_result_path(idx, attempt)}` が存在すること
"""


def _check_perspective_task(idx: int, attempt: int) -> str:
    p = PERSPECTIVES[idx]
    diff = _compute_diff()
    result = _result_path(idx, attempt).read_text(encoding="utf-8")
    return f"""# TASK: レビュー結果の検証（フェーズ5、観点 {idx + 1}/{TOTAL_PERSPECTIVES}: {p["name"]}）

新規コンテキストのサブエージェントに以下を委譲し、検証結果を
`{_check_path(idx, attempt)}`に書き出させよ。
レビューした本人（同じコンテキスト）ではなく、独立した視点で検証すること。

## 検証観点の指示
{p["checker_prompt"]}

## レビュー対象のdiff
```diff
{diff}
```

## 検証するレビュー結果
```json
{result}
```

## 出力するJSONのスキーマ
{{
  "perspective_id": {idx},
  "ok": <bool、レビュー結果が妥当なら true>,
  "feedback": "<ok=falseの場合、見落とし・誤検知の具体的な説明。ok=trueなら空文字>"
}}

## 完了条件
`{_check_path(idx, attempt)}` が存在すること
"""


def _fix_perspective_task(idx: int, attempt: int, fix_attempt: int) -> str:
    """ADR-0004: the fixer is a fresh, narrow-write subagent -- never the
    checker that flagged the issue (checking one's own fix isn't independent),
    and never the full phase-4 implementation subagent (too heavy for what's
    usually a small, localized fix)."""
    p = PERSPECTIVES[idx]
    result = _result_path(idx, attempt).read_text(encoding="utf-8")
    retry_note = ""
    if fix_attempt > 1:
        prev_recheck = json.loads(_recheck_path(idx, fix_attempt - 1).read_text(encoding="utf-8"))
        retry_note = f"""

## 前回の修正では解決しませんでした
{prev_recheck.get("feedback", "")}
"""
    return f"""# TASK: 指摘の自動修正（フェーズ5、観点 {idx + 1}/{TOTAL_PERSPECTIVES}: {p["name"]}）

新規コンテキストのサブエージェント（指摘箇所のみ書き込み可、軽量な修正専用。
指摘そのものを出したレビューア/checkerとは別コンテキストで実行すること）に
以下の指摘を修正させ、完了したら`{_fix_path(idx, fix_attempt)}`
に`{{"status": "fixed"}}`を書き出させよ。

## 修正対象の指摘
```json
{result}
```
{retry_note}
## 注意
指摘箇所（`issues[].location`）以外のファイルは変更しないこと。

## 完了条件
`{_fix_path(idx, fix_attempt)}` が存在すること
"""


def _recheck_perspective_task(idx: int, fix_attempt: int) -> str:
    p = PERSPECTIVES[idx]
    diff = _compute_diff()
    return f"""# TASK: 修正の再検証（フェーズ5、観点 {idx + 1}/{TOTAL_PERSPECTIVES}: {p["name"]}）

新規コンテキストのサブエージェントに以下を委譲し、検証結果を
`{_recheck_path(idx, fix_attempt)}`に書き出させよ。
修正した本人（fixer）ではなく、独立した視点で検証すること。

## 検証観点の指示
{p["checker_prompt"]}

上記観点について、以下のdiff（修正後の最新状態）を確認し、元の指摘が解消されたか判定せよ。

## 修正後のdiff
```diff
{diff}
```

## 出力するJSONのスキーマ
{{
  "resolved": <bool、指摘が解消されていれば true>,
  "feedback": "<resolved=falseの場合、何が未解決かの具体的な説明。trueなら空文字>"
}}

## 完了条件
`{_recheck_path(idx, fix_attempt)}` が存在すること
"""


def _cross_cutting_explore_task() -> str:
    diff = _compute_diff()
    return f"""# TASK: 横断的チェック（フェーズ5、explorer、ADR-0003・ADR-0011）

新規コンテキストのサブエージェントに以下を委譲し、コードベース横断的な一貫性の
問題を探索させ、結果を`{CROSS_CUTTING_FINDINGS_JSON}`に書き出させよ。

これは13観点の機械的チェックとは異なる種類のチェックである。機械的チェックは
diffのみを見せる単発呼び出しだが、こちらはBash・Read・Grep・Glob、および
利用可能ならClaude Code純正のLSPツール（find references・go to definition等）を
使い、diffだけでは見えない「ファイルAとファイルBで実装方法が違う」といった
問題を多ターンで探索してよい。

## 準備
LSPが正しく機能するには依存解決が必要な場合がある。`go mod download`・
`npm install`等、必要なら実行してから探索に入ること。

## 探索の起点・範囲
以下のdiffを起点にすること。diffで変更されたファイルが依拠する既存コード
（呼び出し元、同じ役割を持つ他ファイル等）との不整合を探すのが目的で、
リポジトリ全体を無制限に彷徨うことは避けること。

```diff
{diff}
```

## 探索の観点の例（性質が異なる2種類）
- **実装パターンの一貫性**: 同じ役割のファイルAとファイルBで実装方法・エラー
  ハンドリングの流儀が食い違っている。既存の類似実装と明らかに異なるパターンを
  理由なく採用している
- **ビルドでは検知されない変更の伝播漏れ**: 変更した関数のシグネチャ変更が、
  一部の呼び出し元に意味的に反映されていない。ただしGo等の静的型付け言語では
  単純な引数の過不足はコンパイルエラーになりフェーズ4のビルド自己検証
  （ADR-0009）で既に弾かれているはずなので、この観点が意味を持つのは主に
  動的型付け言語（Python/TypeScriptの型なしコード等）や、文字列ベースの
  ディスパッチ・リフレクション経由の呼び出しなど、ビルドでは検知できない
  ケースに限られる

## 出力するJSONのスキーマ（配列。指摘がなければ空配列でよい）
[
  {{"description": "<問題の説明>", "location": "<ファイル:行等>", "severity": "高|中|低"}}
]

## 完了条件
`{CROSS_CUTTING_FINDINGS_JSON}` が存在すること（指摘なしなら`[]`）
"""


def _cross_cutting_verify_task() -> str:
    findings = CROSS_CUTTING_FINDINGS_JSON.read_text(encoding="utf-8")
    diff = _compute_diff()
    return f"""# TASK: 横断的チェックの検証（フェーズ5、verifier、ADR-0011）

探索した本人（explorer、同じコンテキスト）ではなく、独立した視点の新規コンテキスト
サブエージェントに以下の指摘を検証させ、妥当性が確認できたものだけを
`{CROSS_CUTTING_VERIFIED_JSON}`に書き出させよ。

ADR-0011により、この種の複雑な指摘は自動修正しない（常にG2で人間が判断する）。
このステップの役割は「本当に妥当な指摘か（誤検知でないか）」を1回だけ独立検証
することであり、redo（往復）は行わない——確認できなければその指摘は破棄する。

## 検証対象の指摘
```json
{findings}
```

## レビュー対象のdiff
```diff
{diff}
```

## 検証方針
LSP（find references・go to definition等）や実際のコードを確認し、指摘が
誤検知でないか判断すること。判断に確信が持てない指摘は含めないこと
（人間に無駄な確認をさせないため、確信のあるものだけを残す）。

## 出力するJSONのスキーマ（配列。確認できたものだけ抽出、全て誤検知なら空配列）
[
  {{"description": "<問題の説明>", "location": "<ファイル:行等>", "severity": "高|中|低"}}
]

## 完了条件
`{CROSS_CUTTING_VERIFIED_JSON}` が存在すること（確認できたものがなければ`[]`）
"""


def _unresolved_section(unresolved: list[dict]) -> str:
    """MAX_REVIEW_RETRIES超過で解決しなかった観点を、LLMを介さず確定的に
    レポートへ追記する。review/checkの意見が収束しなかった、または自動修正が
    収束しなかった箇所なので、AIの要約に頼らず人間の確認を促す。"""
    if not unresolved:
        return ""
    reason_labels = {
        "review_check_not_converged": "review/checkの意見が収束しなかった",
        "fix_not_resolved": f"自動修正（最大{MAX_REVIEW_RETRIES}回）を試みたが解決しなかった",
    }
    lines = [
        "\n\n---\n\n## 未解決の指摘（人間の確認が必要）\n",
        "以下の観点は自動では解決できませんでした。AIの判定を鵜呑みにせず、人間が直接確認してください。\n",
    ]
    for entry in unresolved:
        p = PERSPECTIVES[entry["idx"]]
        lines.append(f"- **{p['name']}**: {reason_labels.get(entry['reason'], entry['reason'])}")
    return "\n".join(lines) + "\n"


def _fixed_section(fixed_ids: list[int]) -> str:
    """自動修正できた観点も、透明性のため確定的にレポートへ追記する
    （ADR-0004: 機械的な指摘はG2を経由せず自動的に解決できる）。"""
    if not fixed_ids:
        return ""
    lines = ["\n\n---\n\n## 自動修正済みの指摘\n", "以下の観点は指摘後、自動修正・再検証により解決を確認済みです。\n"]
    for idx in fixed_ids:
        p = PERSPECTIVES[idx]
        lines.append(f"- **{p['name']}**")
    return "\n".join(lines) + "\n"


def _cross_cutting_section() -> str:
    """独立検証済みの横断的チェック指摘を、LLMを介さず確定的にレポートへ追記する
    （ADR-0011: 複雑な指摘は自動修正せず、常にG2で人間が判断するため）。"""
    if not CROSS_CUTTING_VERIFIED_JSON.exists():
        return ""
    findings = json.loads(CROSS_CUTTING_VERIFIED_JSON.read_text(encoding="utf-8"))
    if not findings:
        return ""
    lines = [
        "\n\n---\n\n## 横断的チェックの指摘（人間の判断が必要）\n",
        "diffだけでは検知できないコードベース横断的な問題として検出され、独立した"
        "verifierによる検証を経たものです。複雑な指摘のため自動修正はしていません。\n",
    ]
    for f in findings:
        lines.append(f"- **{f.get('severity', '?')}**: {f.get('description', '')}（{f.get('location', '')}）")
    return "\n".join(lines) + "\n"


def _synthesize_task() -> str:
    rs = _read_review_state()
    redo_counts = rs["redo_counts"]
    results = []
    for idx in range(TOTAL_PERSPECTIVES):
        attempt = redo_counts.get(str(idx), 0) + 1
        results.append(json.loads(_result_path(idx, attempt).read_text(encoding="utf-8")))
    results_json = json.dumps(results, ensure_ascii=False, indent=2)
    fixed_note = _fixed_section(rs["fixed"])
    unresolved_note = _unresolved_section(rs["unresolved"])
    cross_cutting_note = _cross_cutting_section()

    return f"""# TASK: レビュー結果の統合（フェーズ5、最終レポート作成）

新規コンテキストのサブエージェントに以下を委譲し、`{FINAL_REPORT_MD}`
を生成させよ。

## 指示
複数の観点からのレビュー結果を統合し、開発者向けの分かりやすいレポートをMarkdown
形式で作成すること。機械的な指摘で自動修正・解決が確認できたものは
「自動修正済みの指摘」セクションに記載済みのため、別途「問題一覧」のような
形で重複して記載しないこと。

レポートの構成:
1. ## サマリー（自動修正した件数・未解決件数・横断的チェックの指摘件数、1〜2文の総評）
2. ## 問題なし（review/checkの往復を経ても問題が検出されなかった観点の一覧）

以下の「自動修正済みの指摘」「未解決の指摘」「横断的チェックの指摘」セクションが
空でなければ、レポートの末尾にそのまま追記すること（内容を変更・要約しないこと。
人間への報告を正確に保つための確定的な記述のため）:
{fixed_note if fixed_note else "(自動修正済みの指摘なし)"}
{unresolved_note if unresolved_note else "(未解決の指摘なし)"}
{cross_cutting_note if cross_cutting_note else "(横断的チェックの指摘なし)"}

## 各観点のレビュー結果（自動修正前の最終レビュー内容）
```json
{results_json}
```

## 完了条件
`{FINAL_REPORT_MD}` が存在すること
"""


_TERMINAL = {
    "await_g2": f"""# GATE:review

レビューが完了し、G2（最終承認ゲート）の判断待ちです。セッションは終了せず、
`{REVIEW_GATE_MARKER}`のstatusがpendingでなくなるまで待機してください。

人間は `masuda review show <workspace-id>` で{FINAL_REPORT_MD.name}を確認し、
`masuda review chat <workspace-id>` で対話するか、
`masuda review approve <workspace-id>` / `masuda review reject <workspace-id> "<feedback>"` で応答してください。
承認時はローカルmerge・worktree削除まで自動で行われます（ADR-0005）。
却下時はフェーズ4に差し戻され、フィードバックを踏まえて再実装します（ADR-0013）。
""",
    "g2_approved": """# DONE (G2 approved)

G2が承認されました。masuda review approveによるマージ・後片付けをお待ちください。
""",
}


def write_task_md(state: State) -> State:
    phase = state["phase"]
    if phase == "implement":
        content = _implement_task()
    elif phase == "implement_redo":
        content = _implement_task(redo_feedback=state["reason"])
    elif phase == "plan_reopened":
        # Only clearing/consuming the gate happens here on *resolution*
        # (_resolve_plan_reopen, called from detect_phase) -- writing
        # DEVIATION.md here is idempotent for the still-pending re-check case
        # (same content already on disk) and is the actual first write on
        # fresh detection.
        DEVIATION_MD.write_text(state["reason"], encoding="utf-8")
        content = _plan_reopened_task(state["reason"])
    elif phase == "review_perspective":
        info = json.loads(state["reason"])
        content = _review_perspective_task(info["idx"], info["attempt"])
    elif phase == "check_perspective":
        info = json.loads(state["reason"])
        content = _check_perspective_task(info["idx"], info["attempt"])
    elif phase == "fix_perspective":
        info = json.loads(state["reason"])
        content = _fix_perspective_task(info["idx"], info["attempt"], info["fix_attempt"])
    elif phase == "recheck_perspective":
        info = json.loads(state["reason"])
        content = _recheck_perspective_task(info["idx"], info["fix_attempt"])
    elif phase == "cross_cutting_explore":
        content = _cross_cutting_explore_task()
    elif phase == "cross_cutting_verify":
        content = _cross_cutting_verify_task()
    elif phase == "synthesize":
        content = _synthesize_task()
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
