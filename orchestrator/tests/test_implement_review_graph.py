"""
Layer 1 tests for implement_review_graph.py: pure state-machine +
mechanical-backstop logic, no LLM calls, no Docker, no real Claude
invocations.
"""
import importlib
import json
import pathlib
import subprocess

import pytest

import implement_review_graph as irg

SAMPLE_PLAN = """# PLAN.md

## アプローチの要約
テスト用のプラン。

## 変更するファイル一覧

- `README.md`
  - 変更内容: 1行追記する。インラインコード例: `echo hello`
- `cmd/masuda/main.go`
  - 変更内容: フラグを1つ追加する

## 実装のステップ分解
1. README.mdを直す
2. main.goを直す
"""


@pytest.fixture(autouse=True)
def in_tmp_workspace(tmp_path, monkeypatch):
    # Mirrors production's split (roadmap step 7): git commands run against
    # worktree_dir (this process's cwd, same as before), while masuda's own
    # control files (irg.PLAN_MD, irg.IMPLEMENTATION_RESULT_JSON, ...) resolve
    # under a separate state_dir via MASUDA_STATE_DIR. irg reads that env var
    # once at import time (STATE_DIR is a module-level constant), so it must
    # be reload()ed after monkeypatching for each test to get its own
    # isolated state directory.
    worktree_dir = tmp_path / "worktree"
    state_dir = tmp_path / "state"
    worktree_dir.mkdir()
    state_dir.mkdir()
    monkeypatch.chdir(worktree_dir)
    monkeypatch.setenv("MASUDA_STATE_DIR", str(state_dir))
    importlib.reload(irg)
    yield worktree_dir


def init_git_repo():
    subprocess.run(["git", "init", "-q"], check=True)
    subprocess.run(["git", "config", "user.email", "test@example.com"], check=True)
    subprocess.run(["git", "config", "user.name", "test"], check=True)
    # A baseline commit so `git status --porcelain` / `git diff` report
    # new/modified files relative to something, matching a real worktree
    # (which always starts from a base branch commit). PLAN.md itself is
    # masuda's own control file and lives in STATE_DIR (roadmap step 7), never
    # inside the git-managed worktree, so it's written separately here rather
    # than committed as part of the repo's baseline.
    pathlib.Path("README.md").write_text("baseline", encoding="utf-8")
    subprocess.run(["git", "add", "README.md"], check=True)
    subprocess.run(["git", "commit", "-q", "-m", "init"], check=True)
    irg.PLAN_MD.write_text(SAMPLE_PLAN, encoding="utf-8")


def write_result(idx, attempt, has_issues=False):
    irg.REVIEW_RESULTS_DIR.mkdir(exist_ok=True)
    irg._result_path(idx, attempt).write_text(
        json.dumps({"perspective_id": idx, "has_issues": has_issues, "issues": [], "summary": "ok"}),
        encoding="utf-8",
    )


def write_check(idx, attempt, ok, feedback=""):
    irg.REVIEW_RESULTS_DIR.mkdir(exist_ok=True)
    irg._check_path(idx, attempt).write_text(
        json.dumps({"perspective_id": idx, "ok": ok, "feedback": feedback}), encoding="utf-8"
    )


def write_fix(idx, fix_attempt):
    irg.REVIEW_RESULTS_DIR.mkdir(exist_ok=True)
    irg._fix_path(idx, fix_attempt).write_text(json.dumps({"status": "fixed"}), encoding="utf-8")


def write_recheck(idx, fix_attempt, resolved, feedback=""):
    irg.REVIEW_RESULTS_DIR.mkdir(exist_ok=True)
    irg._recheck_path(idx, fix_attempt).write_text(
        json.dumps({"resolved": resolved, "feedback": feedback}), encoding="utf-8"
    )


def write_all_perspectives_clean():
    for idx in range(irg.TOTAL_PERSPECTIVES):
        write_result(idx, 1, has_issues=False)
        write_check(idx, 1, ok=True)


def write_cross_cutting_findings(findings):
    irg.REVIEW_RESULTS_DIR.mkdir(exist_ok=True)
    irg.CROSS_CUTTING_FINDINGS_JSON.write_text(json.dumps(findings, ensure_ascii=False), encoding="utf-8")


def write_cross_cutting_verified(findings):
    irg.REVIEW_RESULTS_DIR.mkdir(exist_ok=True)
    irg.CROSS_CUTTING_VERIFIED_JSON.write_text(json.dumps(findings, ensure_ascii=False), encoding="utf-8")


def resolve_other_perspectives_as_clean(skip):
    """ADR-0021: _detect_review_phase scans every perspective each round, not
    just one idx cursor, so a test isolating one perspective's transition
    must resolve every other perspective first (as clean/no-issues) or they
    show up alongside it in the batch. `skip` is the idx (or set of idxs)
    under test, left untouched."""
    skip_idxs = {skip} if isinstance(skip, int) else set(skip)
    for idx in range(irg.TOTAL_PERSPECTIVES):
        if idx in skip_idxs:
            continue
        write_result(idx, 1, has_issues=False)
        write_check(idx, 1, ok=True)


def batch_state(*tasks):
    return {"phase": "review_batch", "reason": json.dumps({"tasks": list(tasks)})}


def mark_implementation_done_and_clean():
    init_git_repo()
    import pathlib
    pathlib.Path("README.md").write_text("updated", encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")


# --- _extract_planned_files ------------------------------------------------

def test_extract_planned_files_reads_top_level_bullets_only():
    files = irg._extract_planned_files(SAMPLE_PLAN)
    assert files == {"README.md", "cmd/masuda/main.go"}


def test_extract_planned_files_ignores_inline_code_in_sub_bullets():
    assert "echo hello" not in irg._extract_planned_files(SAMPLE_PLAN)


def test_extract_planned_files_missing_section_returns_empty():
    assert irg._extract_planned_files("# PLAN.md\n\nno such section here\n") == set()


# --- _actual_changed_files / _mechanical_deviation -------------------------

def test_actual_changed_files_ignores_masuda_state_dir_files():
    """masuda's own control files live in STATE_DIR (roadmap step 7), a
    directory entirely separate from the git worktree this runs `git status`
    in -- so writing them can never show up as a changed file, without any
    explicit exclusion list (an earlier version of this file needed one,
    back when these files lived inside the worktree itself)."""
    init_git_repo()
    irg.TASK_MD.write_text("...", encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text('{"status": "done"}', encoding="utf-8")
    irg.PLAN_GATE_MARKER.parent.mkdir(parents=True, exist_ok=True)
    irg.PLAN_GATE_MARKER.write_text('{"status": "approved"}', encoding="utf-8")

    assert irg._actual_changed_files() == set()


def test_mechanical_deviation_none_when_changes_within_plan():
    init_git_repo()
    import pathlib
    pathlib.Path("README.md").write_text("updated", encoding="utf-8")
    assert irg._mechanical_deviation() is None


def test_mechanical_deviation_detects_unplanned_file():
    init_git_repo()
    import pathlib
    pathlib.Path("README.md").write_text("updated", encoding="utf-8")
    pathlib.Path("secrets.txt").write_text("oops", encoding="utf-8")

    reason = irg._mechanical_deviation()

    assert reason is not None
    assert "secrets.txt" in reason


def test_mechanical_deviation_skipped_without_a_plan_md():
    """Standalone review (`masuda review start`, roadmap step 6) never goes
    through G1, so there's no PLAN.md and nothing to have deviated from —
    the backstop must not raise FileNotFoundError trying to read one."""
    init_git_repo()
    irg.PLAN_MD.unlink()
    import pathlib
    pathlib.Path("anything.txt").write_text("whatever", encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")
    resolve_other_perspectives_as_clean(skip=0)

    assert irg._mechanical_deviation() is None
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"idx": 0, "kind": "review", "attempt": 1}]


# --- _read_base_ref / _compute_diff ---------------------------------------

def test_read_base_ref_defaults_to_develop_when_file_absent():
    assert irg._read_base_ref() == "develop"


def test_read_base_ref_reads_recorded_ref():
    irg.BASE_REF_FILE.write_text("main\n", encoding="utf-8")
    assert irg._read_base_ref() == "main"


def test_compute_diff_uses_recorded_base_ref_not_bare_head():
    """A standalone review target (roadmap step 6) is already fully
    committed -- `git diff HEAD` would show nothing since HEAD *is* the tip.
    Diffing against the recorded base ref (not the default "develop") is what
    makes the branch's actual committed changes visible."""
    subprocess.run(["git", "init", "-q"], check=True)
    subprocess.run(["git", "config", "user.email", "test@example.com"], check=True)
    subprocess.run(["git", "config", "user.name", "test"], check=True)
    import pathlib
    pathlib.Path("README.md").write_text("base version\n", encoding="utf-8")
    subprocess.run(["git", "add", "-A"], check=True)
    subprocess.run(["git", "commit", "-q", "-m", "base"], check=True)
    subprocess.run(["git", "branch", "main-for-test"], check=True)

    pathlib.Path("README.md").write_text("reviewed branch version\n", encoding="utf-8")
    subprocess.run(["git", "commit", "-q", "-am", "the actual change under review"], check=True)

    irg.BASE_REF_FILE.write_text("main-for-test", encoding="utf-8")
    diff = irg._compute_diff()

    assert "reviewed branch version" in diff


def test_compute_diff_never_includes_masuda_state_dir_files():
    """`git add -A`/`git diff --cached` run against the worktree (cwd); since
    STATE_DIR (roadmap step 7) is a separate directory, masuda's own scratch
    files there are never staged in the first place -- unlike an earlier
    version of this file, where they lived inside the worktree and had to be
    explicitly unstaged again before diffing (confirmed live that without
    that, review subagents were shown .masuda-base-ref and
    implementation_result.json verbatim as if part of the change under
    review)."""
    init_git_repo()
    pathlib.Path("README.md").write_text("a real change reviewers should see", encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")
    irg.BASE_REF_FILE.write_text("HEAD", encoding="utf-8")
    irg.REVIEW_RESULTS_DIR.mkdir()
    (irg.REVIEW_RESULTS_DIR / "result_0_attempt1.json").write_text("{}", encoding="utf-8")

    diff = irg._compute_diff()

    assert "a real change reviewers should see" in diff
    assert "implementation_result.json" not in diff
    assert ".masuda-base-ref" not in diff
    assert "result_0_attempt1.json" not in diff


# --- detect_phase: phase 4 -----------------------------------------------

def test_no_result_means_implement():
    assert irg.detect_phase({"phase": "", "reason": ""})["phase"] == "implement"


def test_done_with_no_deviation_enters_review():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip=0)
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"idx": 0, "kind": "review", "attempt": 1}]


def test_done_with_unplanned_file_reopens_plan():
    init_git_repo()
    import pathlib
    pathlib.Path("unplanned.txt").write_text("oops", encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "plan_reopened"
    assert "unplanned.txt" in state["reason"]


def test_needs_plan_review_reopens_plan():
    irg.IMPLEMENTATION_RESULT_JSON.write_text(
        json.dumps({"status": "needs_plan_review", "reason": "設計を変える必要がある"}), encoding="utf-8"
    )
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "plan_reopened"
    assert "設計を変える必要がある" in state["reason"]


def test_build_test_failed_reopens_plan():
    irg.IMPLEMENTATION_RESULT_JSON.write_text(
        json.dumps({"status": "build_test_failed", "details": "3回試したがテストが通らない"}), encoding="utf-8"
    )
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "plan_reopened"
    assert "3回試したがテストが通らない" in state["reason"]


def test_unknown_status_raises():
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "???"}), encoding="utf-8")
    with pytest.raises(ValueError):
        irg.detect_phase({"phase": "", "reason": ""})


# --- detect_phase: phase 5 (review) ---------------------------------------

def test_review_advances_to_check_once_result_written():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip=0)
    write_result(0, 1)
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"idx": 0, "kind": "check", "attempt": 1}]


def test_review_ok_no_issues_dropped_from_next_batch():
    """A clean perspective (no issues, check passed) drops out of future
    rounds' batches -- ADR-0021 replaces the old single-idx "advance to next
    perspective" cursor with "recompute the remaining set every round"."""
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip=1)  # marks 0 (and 2..13) clean, leaves 1 pending
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"idx": 1, "kind": "review", "attempt": 1}]


def test_review_failed_check_redoes_same_perspective():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip=0)
    write_result(0, 1)
    write_check(0, 1, ok=False, feedback="見落としがある")
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"idx": 0, "kind": "review", "attempt": 2}]


def test_review_exhausted_retries_marks_unresolved():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip=0)
    # attempt 1 and 2 both fail -> MAX_REVIEW_RETRIES (2) reached -> perspective 0 unresolved
    write_result(0, 1)
    write_check(0, 1, ok=False, feedback="ng1")
    write_result(0, 2)
    write_check(0, 2, ok=False, feedback="ng2")
    write_result(0, 3)
    write_check(0, 3, ok=False, feedback="ng3")

    state = irg.detect_phase({"phase": "", "reason": ""})

    # perspective 0 is now the only one left unresolved -- nothing left to
    # batch, so review moves on to cross-cutting.
    assert state["phase"] == "cross_cutting_explore"
    rs = irg._read_review_state()
    assert rs["unresolved"] == [{"idx": 0, "reason": "review_check_not_converged"}]


def test_multiple_pending_perspectives_at_different_stages_batch_together():
    """ADR-0021's actual point: independent perspectives sitting at different
    stages (one fresh, one awaiting check, one confirmed-issue awaiting fix)
    all land in the same round's batch instead of being processed one at a
    time."""
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip={0, 1, 2})
    # idx 0: needs its first review (nothing written)
    write_result(1, 1)  # idx 1: needs check
    write_result(2, 1, has_issues=True)
    write_check(2, 1, ok=True)  # idx 2: confirmed issue, needs fix

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "review_batch"
    tasks = json.loads(state["reason"])["tasks"]
    assert {t["idx"]: t["kind"] for t in tasks} == {0: "review", 1: "check", 2: "fix"}


def test_all_perspectives_done_means_cross_cutting_explore():
    """Once all 13 mechanical perspectives converge, the cross-cutting
    explorer/verifier pass (ADR-0003/ADR-0011) runs before synthesize."""
    mark_implementation_done_and_clean()
    for idx in range(irg.TOTAL_PERSPECTIVES):
        write_result(idx, 1, has_issues=False)
        write_check(idx, 1, ok=True)
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "cross_cutting_explore"


# --- detect_phase: cross-cutting explorer/verifier (ADR-0003/ADR-0011) ----

def test_cross_cutting_explore_with_no_findings_skips_verify():
    """ADR-0011: no redo loop for cross-cutting checks. If explorer found
    nothing, there's nothing for verify to independently confirm -- go
    straight to synthesize instead of spawning a verifier for no reason."""
    mark_implementation_done_and_clean()
    write_all_perspectives_clean()
    write_cross_cutting_findings([])

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "synthesize"


def test_cross_cutting_explore_with_findings_advances_to_verify():
    mark_implementation_done_and_clean()
    write_all_perspectives_clean()
    write_cross_cutting_findings([{"description": "不整合あり", "file": "a.go", "startLine": 10, "endLine": 10, "severity": "中"}])

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "cross_cutting_verify"


def test_cross_cutting_verify_done_means_synthesize():
    mark_implementation_done_and_clean()
    write_all_perspectives_clean()
    write_cross_cutting_findings([{"description": "不整合あり", "file": "a.go", "startLine": 10, "endLine": 10, "severity": "中"}])
    write_cross_cutting_verified([])

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "synthesize"


def test_cross_cutting_explore_task_includes_diff():
    init_git_repo()
    pathlib.Path("README.md").write_text("updated content", encoding="utf-8")

    irg.write_task_md({"phase": "cross_cutting_explore", "reason": ""})

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "updated content" in content
    assert "DONE" not in content


def test_cross_cutting_verify_task_includes_findings_and_diff():
    init_git_repo()
    pathlib.Path("README.md").write_text("updated content", encoding="utf-8")
    write_cross_cutting_findings([{"description": "不整合あり", "file": "a.go", "startLine": 10, "endLine": 10, "severity": "中"}])

    irg.write_task_md({"phase": "cross_cutting_verify", "reason": ""})

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "不整合あり" in content
    assert "updated content" in content


def test_synthesize_includes_cross_cutting_section_when_verified_nonempty():
    init_git_repo()
    write_all_perspectives_clean()
    write_cross_cutting_verified([{"description": "不整合あり", "file": "a.go", "startLine": 10, "endLine": 10, "severity": "中"}])

    irg.write_task_md({"phase": "synthesize", "reason": ""})

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "横断的チェックの指摘" in content
    assert "不整合あり" in content


def test_synthesize_omits_cross_cutting_section_when_no_findings():
    init_git_repo()
    write_all_perspectives_clean()
    write_cross_cutting_verified([])

    irg.write_task_md({"phase": "synthesize", "reason": ""})

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "横断的チェックの指摘なし" in content


def test_g2_rejection_clears_cross_cutting_files_too():
    """ADR-0013: G2 rejection restarts review from perspective 0, so a stale
    cross-cutting explore/verify result from the rejected round must not
    leak into the redo -- it lives under REVIEW_RESULTS_DIR, so the existing
    wipe in _clear_review_state() already covers it; this pins that down."""
    mark_implementation_done_and_clean()
    write_all_perspectives_clean()
    write_cross_cutting_findings([{"description": "x", "file": "y", "startLine": 1, "endLine": 1, "severity": "低"}])
    write_cross_cutting_verified([{"description": "x", "file": "y", "startLine": 1, "endLine": 1, "severity": "低"}])
    irg.FINAL_REPORT_MD.parent.mkdir(exist_ok=True)
    irg.FINAL_REPORT_MD.write_text("# report", encoding="utf-8")
    irg.COMMIT_MESSAGE_FILE.write_text("commit message", encoding="utf-8")
    irg.REVIEW_GATE_MARKER.parent.mkdir(parents=True, exist_ok=True)
    irg.REVIEW_GATE_MARKER.write_text(json.dumps({"status": "rejected", "feedback": "却下"}), encoding="utf-8")

    irg.detect_phase({"phase": "", "reason": ""})

    assert not irg.CROSS_CUTTING_FINDINGS_JSON.exists()
    assert not irg.CROSS_CUTTING_VERIFIED_JSON.exists()


# --- detect_phase: phase 5 fix<->recheck loop (ADR-0004) ------------------

def test_confirmed_issue_enters_fix_loop():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip=0)
    write_result(0, 1, has_issues=True)
    write_check(0, 1, ok=True)

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"idx": 0, "kind": "fix", "attempt": 1, "fix_attempt": 1}]


def test_fix_written_advances_to_recheck():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip=0)
    write_result(0, 1, has_issues=True)
    write_check(0, 1, ok=True)
    write_fix(0, 1)

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"idx": 0, "kind": "recheck", "attempt": 1, "fix_attempt": 1}]


def test_recheck_resolved_dropped_from_next_batch_and_records_fixed():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip={0, 1})  # leave 1 pending; 0 is driven through fix/recheck below, not "clean"
    write_result(0, 1, has_issues=True)
    write_check(0, 1, ok=True)
    write_fix(0, 1)
    write_recheck(0, 1, resolved=True)

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"idx": 1, "kind": "review", "attempt": 1}]
    assert irg._read_review_state()["fixed"] == [0]


def test_recheck_unresolved_redoes_fix():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip=0)
    write_result(0, 1, has_issues=True)
    write_check(0, 1, ok=True)
    write_fix(0, 1)
    write_recheck(0, 1, resolved=False, feedback="まだ直っていない")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"idx": 0, "kind": "fix", "attempt": 1, "fix_attempt": 2}]


def test_fix_exhausted_retries_marks_unresolved():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip=0)
    write_result(0, 1, has_issues=True)
    write_check(0, 1, ok=True)
    for fix_attempt in (1, 2, 3):
        write_fix(0, fix_attempt)
        write_recheck(0, fix_attempt, resolved=False, feedback=f"ng{fix_attempt}")

    state = irg.detect_phase({"phase": "", "reason": ""})

    # perspective 0 is now the only one left unresolved -- nothing left to
    # batch, so review moves on to cross-cutting.
    assert state["phase"] == "cross_cutting_explore"
    rs = irg._read_review_state()
    assert rs["unresolved"] == [{"idx": 0, "reason": "fix_not_resolved"}]
    assert rs["fixed"] == []


def test_final_report_no_marker_means_await_g2():
    mark_implementation_done_and_clean()
    irg.FINAL_REPORT_MD.parent.mkdir(exist_ok=True)
    irg.FINAL_REPORT_MD.write_text("# report", encoding="utf-8")
    irg.COMMIT_MESSAGE_FILE.write_text("commit message", encoding="utf-8")
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "await_g2"


def test_final_report_without_commit_message_reruns_synthesize():
    """The synthesize subagent is instructed to write both files in one
    delegation (ADR-0023 follow-up); if only the report landed, treat
    synthesize as unfinished rather than proceeding to await_g2 without a
    commit message for finalizeReviewApproval to use."""
    mark_implementation_done_and_clean()
    write_all_perspectives_clean()
    write_cross_cutting_findings([])
    irg.FINAL_REPORT_MD.parent.mkdir(exist_ok=True)
    irg.FINAL_REPORT_MD.write_text("# report", encoding="utf-8")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "synthesize"


def test_final_report_approved_means_g2_approved():
    mark_implementation_done_and_clean()
    irg.FINAL_REPORT_MD.parent.mkdir(exist_ok=True)
    irg.FINAL_REPORT_MD.write_text("# report", encoding="utf-8")
    irg.COMMIT_MESSAGE_FILE.write_text("commit message", encoding="utf-8")
    irg.REVIEW_GATE_MARKER.parent.mkdir(parents=True, exist_ok=True)
    irg.REVIEW_GATE_MARKER.write_text(json.dumps({"status": "approved"}), encoding="utf-8")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "g2_approved"


def test_final_report_rejected_reopens_implementation():
    mark_implementation_done_and_clean()
    irg.FINAL_REPORT_MD.parent.mkdir(exist_ok=True)
    irg.FINAL_REPORT_MD.write_text("# report", encoding="utf-8")
    irg.COMMIT_MESSAGE_FILE.write_text("commit message", encoding="utf-8")
    write_result(0, 1)
    write_check(0, 1, ok=True)
    irg.REVIEW_GATE_MARKER.parent.mkdir(parents=True, exist_ok=True)
    irg.REVIEW_GATE_MARKER.write_text(json.dumps({"status": "rejected", "feedback": "セキュリティ観点を見直して"}), encoding="utf-8")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "implement_redo"
    assert "セキュリティ観点を見直して" in state["reason"]
    assert not irg.REVIEW_GATE_MARKER.exists(), "rejection must be consumed"
    assert not irg.IMPLEMENTATION_RESULT_JSON.exists(), "must force a fresh implementation round"
    assert not irg.REVIEW_STATE_JSON.exists(), "review must restart from perspective 0 (ADR-0013)"
    assert not irg.REVIEW_RESULTS_DIR.exists(), "stale review results must not be reused (ADR-0013)"
    assert not irg.COMMIT_MESSAGE_FILE.exists(), "stale commit message must not be reused for the redo's changes"


def test_implement_redo_detected_on_fresh_process_via_feedback_file():
    """detect_phase is re-invoked as a fresh process each loop iteration --
    the reopened-implementation instruction must survive that, not just live
    in the in-memory state dict."""
    irg.REVIEW_FEEDBACK_MD.write_text("直して", encoding="utf-8")
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "implement_redo"
    assert state["reason"] == "直して"


# --- write_task_md -----------------------------------------------------------

def test_implement_task_includes_plan_content():
    irg.PLAN_MD.write_text(SAMPLE_PLAN, encoding="utf-8")
    irg.write_task_md({"phase": "implement", "reason": ""})
    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "変更するファイル一覧" in content
    assert "DONE" not in content


def test_implement_redo_includes_g2_feedback():
    irg.PLAN_MD.write_text(SAMPLE_PLAN, encoding="utf-8")
    irg.write_task_md({"phase": "implement_redo", "reason": "セキュリティ観点を見直して"})
    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "セキュリティ観点を見直して" in content


def test_implement_missing_plan_raises():
    with pytest.raises(FileNotFoundError):
        irg.write_task_md({"phase": "implement", "reason": ""})


def test_plan_reopened_writes_deviation_as_a_gate_not_a_terminal_done():
    """write_task_md's job here is only to open the gate (write DEVIATION.md,
    render the GATE:plan message) -- it must NOT also clear the gate marker or
    implementation_result.json anymore. That clearing now happens in
    detect_phase/_resolve_plan_reopen, only once the gate is actually
    resolved; doing it eagerly here was the bug that made an *approved*
    deviation reopen the gate forever once the loop could auto-resume
    (GATE:plan, roadmap step 5) instead of a human always restarting by hand.
    """
    irg.PLAN_GATE_MARKER.parent.mkdir(parents=True)
    irg.PLAN_GATE_MARKER.write_text(json.dumps({"status": "approved"}), encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")

    irg.write_task_md({"phase": "plan_reopened", "reason": "計画外のファイル変更"})

    assert irg.DEVIATION_MD.read_text(encoding="utf-8") == "計画外のファイル変更"
    assert irg.PLAN_GATE_MARKER.exists()
    assert irg.IMPLEMENTATION_RESULT_JSON.exists()
    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "GATE:plan" in content
    assert "DONE" not in content
    assert "計画外のファイル変更" in content


# --- detect_phase: resolving a reopened G1 (ADR-0010, ADR-0013) ----------

def test_mechanical_deviation_first_detection_opens_gate_without_clearing():
    init_git_repo()
    import pathlib
    pathlib.Path("unplanned.txt").write_text("oops", encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "plan_reopened"
    assert not irg.DEVIATION_MD.exists(), "DEVIATION.md is written by write_task_md, not detect_phase"


def test_mechanical_deviation_first_detection_clears_stale_gate_marker():
    """Confirmed on a live run: the original G1 approval's marker is never
    unlinked on the "approved" path (investigate_plan_graph.py's
    detect_phase), so it's still sitting on disk, "approved", the first time
    a later mechanical deviation reopens the gate. Left alone, the GATE:plan
    wait condition ("not pending") would already be satisfied before a human
    has looked at *this* deviation -- stale history silently standing in for
    today's answer."""
    init_git_repo()
    import pathlib
    pathlib.Path("unplanned.txt").write_text("oops", encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")
    irg.PLAN_GATE_MARKER.parent.mkdir(parents=True)
    irg.PLAN_GATE_MARKER.write_text(json.dumps({"status": "approved"}), encoding="utf-8")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "plan_reopened"
    assert not irg.PLAN_GATE_MARKER.exists()


def test_mechanical_deviation_still_pending_reflects_same_reason():
    init_git_repo()
    import pathlib
    pathlib.Path("unplanned.txt").write_text("oops", encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")
    irg.DEVIATION_MD.write_text("既存の理由", encoding="utf-8")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "plan_reopened"
    assert state["reason"] == "既存の理由"


def test_mechanical_deviation_approved_is_recorded_and_review_proceeds():
    init_git_repo()
    import pathlib
    pathlib.Path("unplanned.txt").write_text("oops", encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")
    irg.DEVIATION_MD.write_text("既存の理由", encoding="utf-8")
    irg.PLAN_GATE_MARKER.parent.mkdir(parents=True)
    irg.PLAN_GATE_MARKER.write_text(json.dumps({"status": "approved"}), encoding="utf-8")
    resolve_other_perspectives_as_clean(skip=0)

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert not irg.DEVIATION_MD.exists()
    assert not irg.PLAN_GATE_MARKER.exists()
    assert "unplanned.txt" in irg._read_approved_deviations()
    # Approval means "accept the deviation as-is" -- implementation_result.json
    # must survive so the next check treats it as already done, not redone.
    assert irg.IMPLEMENTATION_RESULT_JSON.exists()
    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"idx": 0, "kind": "review", "attempt": 1}]


def test_mechanical_deviation_approved_does_not_reflag_on_next_check():
    """The bug this whole mechanism exists to fix: without recording the
    approval, the very next mechanical check would see the same extra file
    and reopen the gate forever."""
    init_git_repo()
    import pathlib
    pathlib.Path("unplanned.txt").write_text("oops", encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")
    irg.DEVIATION_MD.write_text("既存の理由", encoding="utf-8")
    irg.PLAN_GATE_MARKER.parent.mkdir(parents=True)
    irg.PLAN_GATE_MARKER.write_text(json.dumps({"status": "approved"}), encoding="utf-8")
    irg.detect_phase({"phase": "", "reason": ""})  # resolves the approval

    assert irg._mechanical_deviation() is None


def test_mechanical_deviation_rejected_forces_fresh_implementation():
    init_git_repo()
    import pathlib
    pathlib.Path("unplanned.txt").write_text("oops", encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")
    irg.DEVIATION_MD.write_text("既存の理由", encoding="utf-8")
    irg.PLAN_GATE_MARKER.parent.mkdir(parents=True)
    irg.PLAN_GATE_MARKER.write_text(json.dumps({"status": "rejected", "feedback": "計画通りにして"}), encoding="utf-8")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert not irg.DEVIATION_MD.exists()
    assert not irg.PLAN_GATE_MARKER.exists()
    assert not irg.IMPLEMENTATION_RESULT_JSON.exists()
    assert irg._read_approved_deviations() == set()
    assert state["phase"] == "implement_redo"
    assert "計画通りにして" in state["reason"]


def test_self_reported_deviation_approved_still_needs_a_redo_turn():
    """Unlike the mechanical case, the subagent stopped *before* acting
    (that's the point of the self-report) -- approval means "go ahead and do
    what you proposed", which still requires another implementation turn,
    not a direct pass into review."""
    irg.IMPLEMENTATION_RESULT_JSON.write_text(
        json.dumps({"status": "needs_plan_review", "reason": "設計を変えたい"}), encoding="utf-8"
    )
    irg.DEVIATION_MD.write_text("既存の理由", encoding="utf-8")
    irg.PLAN_GATE_MARKER.parent.mkdir(parents=True)
    irg.PLAN_GATE_MARKER.write_text(json.dumps({"status": "approved"}), encoding="utf-8")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "implement_redo"
    assert not irg.IMPLEMENTATION_RESULT_JSON.exists()


def test_review_perspective_task_includes_diff_and_perspective_prompt():
    init_git_repo()
    import pathlib
    pathlib.Path("README.md").write_text("updated content", encoding="utf-8")

    irg.write_task_md(batch_state({"idx": 0, "kind": "review", "attempt": 1}))

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "updated content" in content
    assert irg.PERSPECTIVES[0]["review_prompt"][:20] in content
    assert "DONE" not in content


def test_review_perspective_task_includes_prior_feedback_on_redo():
    init_git_repo()
    write_result(0, 1)
    write_check(0, 1, ok=False, feedback="前回の見落とし")

    irg.write_task_md(batch_state({"idx": 0, "kind": "review", "attempt": 2}))

    assert "前回の見落とし" in irg.TASK_MD.read_text(encoding="utf-8")


def test_check_perspective_task_includes_review_result():
    init_git_repo()
    write_result(0, 1, has_issues=True)

    irg.write_task_md(batch_state({"idx": 0, "kind": "check", "attempt": 1}))

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "has_issues" in content
    assert irg.PERSPECTIVES[0]["checker_prompt"][:20] in content


def test_fix_perspective_task_includes_flagged_issue():
    init_git_repo()
    write_result(0, 1, has_issues=True)

    irg.write_task_md(batch_state({"idx": 0, "kind": "fix", "attempt": 1, "fix_attempt": 1}))

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "has_issues" in content
    assert "fix_0_fixattempt1.json" in content


def test_fix_perspective_task_includes_prior_recheck_feedback_on_retry():
    init_git_repo()
    write_result(0, 1, has_issues=True)
    write_recheck(0, 1, resolved=False, feedback="まだ直っていない")

    irg.write_task_md(batch_state({"idx": 0, "kind": "fix", "attempt": 1, "fix_attempt": 2}))

    assert "まだ直っていない" in irg.TASK_MD.read_text(encoding="utf-8")


def test_recheck_perspective_task_includes_diff_and_checker_prompt():
    init_git_repo()
    import pathlib
    pathlib.Path("README.md").write_text("fixed content", encoding="utf-8")

    irg.write_task_md(batch_state({"idx": 0, "kind": "recheck", "fix_attempt": 1}))

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "fixed content" in content


def test_review_batch_task_delegates_multiple_independent_tasks_in_parallel():
    """ADR-0021: a round with several independent perspectives at different
    stages renders all of them into one TASK.md instructing the main
    session to delegate every one as a separate, parallel Task tool call."""
    init_git_repo()
    write_result(1, 1, has_issues=True)

    irg.write_task_md(
        batch_state(
            {"idx": 0, "kind": "review", "attempt": 1},
            {"idx": 1, "kind": "check", "attempt": 1},
        )
    )

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "並列に" in content
    assert irg.PERSPECTIVES[0]["review_prompt"][:20] in content
    assert irg.PERSPECTIVES[1]["checker_prompt"][:20] in content
    assert str(irg._result_path(0, 1)) in content
    assert str(irg._check_path(1, 1)) in content


def test_review_batch_iteration_budget_counts_every_task_in_the_batch():
    """ADR-0021/ADR-0011: the budget still counts one unit per subagent
    invocation, so a batch of N tasks must advance the counter by N, not by
    1 per write_task_md call."""
    init_git_repo()

    irg.write_task_md(batch_state(*({"idx": i, "kind": "review", "attempt": 1} for i in range(5))))

    assert irg._read_iteration_count() == 5


def test_synthesize_task_includes_fixed_and_unresolved_sections():
    init_git_repo()
    irg._write_review_state(
        {
            "redo_counts": {},
            "fix_counts": {},
            "unresolved": [{"idx": 1, "reason": "fix_not_resolved"}],
            "fixed": [0],
            "clean": list(range(2, irg.TOTAL_PERSPECTIVES)),
        }
    )
    for idx in range(irg.TOTAL_PERSPECTIVES):
        write_result(idx, 1, has_issues=(idx in (0, 1)))

    irg.write_task_md({"phase": "synthesize", "reason": ""})

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "自動修正済みの指摘" in content
    assert "未解決の指摘" in content
    assert irg.PERSPECTIVES[0]["name"] in content
    assert irg.PERSPECTIVES[1]["name"] in content


def test_g2_approved_is_a_terminal_done():
    irg.write_task_md({"phase": "g2_approved", "reason": ""})
    assert "DONE" in irg.TASK_MD.read_text(encoding="utf-8")


def test_await_g2_is_a_gate_not_a_terminal_done():
    irg.write_task_md({"phase": "await_g2", "reason": ""})
    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "GATE:review" in content
    assert "DONE" not in content


def test_await_g2_mentions_review_cli_commands():
    irg.write_task_md({"phase": "await_g2", "reason": ""})
    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "masuda review chat" in content
    assert "masuda review approve" in content
    assert "masuda review reject" in content


# --- ITERATION_BUDGET (ADR-0011) -------------------------------------------

def test_write_task_md_increments_iteration_count_for_subagent_phases():
    init_git_repo()
    assert irg._read_iteration_count() == 0
    irg.write_task_md({"phase": "implement", "reason": ""})
    assert irg._read_iteration_count() == 1
    irg.write_task_md({"phase": "cross_cutting_explore", "reason": ""})
    assert irg._read_iteration_count() == 2


def test_write_task_md_does_not_increment_for_gate_or_terminal_phases():
    """await_g2/g2_approved/plan_reopened don't delegate a subagent Task
    call, so they must not count against the budget."""
    irg.write_task_md({"phase": "await_g2", "reason": ""})
    irg.write_task_md({"phase": "g2_approved", "reason": ""})
    irg.write_task_md({"phase": "plan_reopened", "reason": "計画外の変更"})
    assert irg._read_iteration_count() == 0


def test_iteration_budget_exceeded_overrides_phase():
    """Once the persisted count already exceeds ITERATION_BUDGET, write_task_md
    must render the blocked message instead of another subagent delegation --
    a final defense line independent of MAX_REVIEW_RETRIES (ADR-0011)."""
    irg.PLAN_MD.write_text(SAMPLE_PLAN, encoding="utf-8")
    irg.ITERATION_COUNT_FILE.write_text(str(irg.ITERATION_BUDGET), encoding="utf-8")

    irg.write_task_md({"phase": "implement", "reason": ""})

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "DONE" in content
    assert "ITERATION_BUDGET" in content


def test_full_graph_run_writes_task_md():
    irg.PLAN_MD.write_text(SAMPLE_PLAN, encoding="utf-8")
    app = irg.build_graph()
    app.invoke({"phase": "", "reason": ""})
    assert irg.TASK_MD.exists()
    assert "実装" in irg.TASK_MD.read_text(encoding="utf-8")
