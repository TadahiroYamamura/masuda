"""
Layer 1 tests for implement_review_graph.py: pure state-machine +
mechanical-backstop logic, no LLM calls, no Docker, no real Claude
invocations.
"""
import json
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
def in_tmp_worktree(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    yield tmp_path


def init_git_repo():
    subprocess.run(["git", "init", "-q"], check=True)
    subprocess.run(["git", "config", "user.email", "test@example.com"], check=True)
    subprocess.run(["git", "config", "user.name", "test"], check=True)
    # An initial commit so `git status --porcelain` / `git diff` report
    # new/modified files relative to something, matching a real worktree
    # (which always starts from a base branch commit).
    irg.PLAN_MD.write_text(SAMPLE_PLAN, encoding="utf-8")
    subprocess.run(["git", "add", "PLAN.md"], check=True)
    subprocess.run(["git", "commit", "-q", "-m", "init"], check=True)


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

def test_actual_changed_files_excludes_masuda_internal_files():
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


# --- detect_phase: phase 4 -----------------------------------------------

def test_no_result_means_implement():
    assert irg.detect_phase({"phase": "", "reason": ""})["phase"] == "implement"


def test_done_with_no_deviation_enters_review():
    mark_implementation_done_and_clean()
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "review_perspective"
    info = json.loads(state["reason"])
    assert info == {"idx": 0, "attempt": 1}


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
    write_result(0, 1)
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "check_perspective"
    assert json.loads(state["reason"]) == {"idx": 0, "attempt": 1}


def test_review_ok_no_issues_advances_to_next_perspective():
    mark_implementation_done_and_clean()
    write_result(0, 1, has_issues=False)
    write_check(0, 1, ok=True)
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "review_perspective"
    assert json.loads(state["reason"]) == {"idx": 1, "attempt": 1}


def test_review_failed_check_redoes_same_perspective():
    mark_implementation_done_and_clean()
    write_result(0, 1)
    write_check(0, 1, ok=False, feedback="見落としがある")
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "review_perspective"
    assert json.loads(state["reason"]) == {"idx": 0, "attempt": 2}


def test_review_exhausted_retries_marks_unresolved_and_advances():
    mark_implementation_done_and_clean()
    # attempt 1 and 2 both fail -> MAX_REVIEW_RETRIES (2) reached -> perspective 0 unresolved, move to 1
    write_result(0, 1)
    write_check(0, 1, ok=False, feedback="ng1")
    write_result(0, 2)
    write_check(0, 2, ok=False, feedback="ng2")
    write_result(0, 3)
    write_check(0, 3, ok=False, feedback="ng3")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "review_perspective"
    assert json.loads(state["reason"]) == {"idx": 1, "attempt": 1}
    rs = irg._read_review_state()
    assert rs["unresolved"] == [{"idx": 0, "reason": "review_check_not_converged"}]


def test_all_perspectives_done_means_synthesize():
    mark_implementation_done_and_clean()
    for idx in range(irg.TOTAL_PERSPECTIVES):
        write_result(idx, 1, has_issues=False)
        write_check(idx, 1, ok=True)
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "synthesize"


# --- detect_phase: phase 5 fix<->recheck loop (ADR-0004) ------------------

def test_confirmed_issue_enters_fix_loop():
    mark_implementation_done_and_clean()
    write_result(0, 1, has_issues=True)
    write_check(0, 1, ok=True)

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "fix_perspective"
    assert json.loads(state["reason"]) == {"idx": 0, "attempt": 1, "fix_attempt": 1}


def test_fix_written_advances_to_recheck():
    mark_implementation_done_and_clean()
    write_result(0, 1, has_issues=True)
    write_check(0, 1, ok=True)
    write_fix(0, 1)

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "recheck_perspective"
    assert json.loads(state["reason"]) == {"idx": 0, "fix_attempt": 1}


def test_recheck_resolved_advances_to_next_perspective_and_records_fixed():
    mark_implementation_done_and_clean()
    write_result(0, 1, has_issues=True)
    write_check(0, 1, ok=True)
    write_fix(0, 1)
    write_recheck(0, 1, resolved=True)

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "review_perspective"
    assert json.loads(state["reason"]) == {"idx": 1, "attempt": 1}
    assert irg._read_review_state()["fixed"] == [0]


def test_recheck_unresolved_redoes_fix():
    mark_implementation_done_and_clean()
    write_result(0, 1, has_issues=True)
    write_check(0, 1, ok=True)
    write_fix(0, 1)
    write_recheck(0, 1, resolved=False, feedback="まだ直っていない")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "fix_perspective"
    assert json.loads(state["reason"]) == {"idx": 0, "attempt": 1, "fix_attempt": 2}


def test_fix_exhausted_retries_marks_unresolved_and_advances():
    mark_implementation_done_and_clean()
    write_result(0, 1, has_issues=True)
    write_check(0, 1, ok=True)
    for fix_attempt in (1, 2, 3):
        write_fix(0, fix_attempt)
        write_recheck(0, fix_attempt, resolved=False, feedback=f"ng{fix_attempt}")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "review_perspective"
    assert json.loads(state["reason"]) == {"idx": 1, "attempt": 1}
    rs = irg._read_review_state()
    assert rs["unresolved"] == [{"idx": 0, "reason": "fix_not_resolved"}]
    assert rs["fixed"] == []


def test_final_report_no_marker_means_await_g2():
    mark_implementation_done_and_clean()
    irg.FINAL_REPORT_MD.parent.mkdir(exist_ok=True)
    irg.FINAL_REPORT_MD.write_text("# report", encoding="utf-8")
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "await_g2"


def test_final_report_approved_means_g2_approved():
    mark_implementation_done_and_clean()
    irg.FINAL_REPORT_MD.parent.mkdir(exist_ok=True)
    irg.FINAL_REPORT_MD.write_text("# report", encoding="utf-8")
    irg.REVIEW_GATE_MARKER.parent.mkdir(parents=True, exist_ok=True)
    irg.REVIEW_GATE_MARKER.write_text(json.dumps({"status": "approved"}), encoding="utf-8")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "g2_approved"


def test_final_report_rejected_reopens_implementation():
    mark_implementation_done_and_clean()
    irg.FINAL_REPORT_MD.parent.mkdir(exist_ok=True)
    irg.FINAL_REPORT_MD.write_text("# report", encoding="utf-8")
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


def test_plan_reopened_writes_deviation_and_clears_state():
    irg.PLAN_GATE_MARKER.parent.mkdir(parents=True)
    irg.PLAN_GATE_MARKER.write_text(json.dumps({"status": "approved"}), encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")

    irg.write_task_md({"phase": "plan_reopened", "reason": "計画外のファイル変更"})

    assert irg.DEVIATION_MD.read_text(encoding="utf-8") == "計画外のファイル変更"
    assert not irg.PLAN_GATE_MARKER.exists(), "reopening must reset the gate so a stale approval isn't reused"
    assert not irg.IMPLEMENTATION_RESULT_JSON.exists(), "stale result must be cleared so a retry starts clean"
    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "DONE" in content
    assert "計画外のファイル変更" in content


def test_review_perspective_task_includes_diff_and_perspective_prompt():
    init_git_repo()
    import pathlib
    pathlib.Path("README.md").write_text("updated content", encoding="utf-8")

    irg.write_task_md({"phase": "review_perspective", "reason": json.dumps({"idx": 0, "attempt": 1})})

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "updated content" in content
    assert irg.PERSPECTIVES[0]["review_prompt"][:20] in content
    assert "DONE" not in content


def test_review_perspective_task_includes_prior_feedback_on_redo():
    init_git_repo()
    write_result(0, 1)
    write_check(0, 1, ok=False, feedback="前回の見落とし")

    irg.write_task_md({"phase": "review_perspective", "reason": json.dumps({"idx": 0, "attempt": 2})})

    assert "前回の見落とし" in irg.TASK_MD.read_text(encoding="utf-8")


def test_check_perspective_task_includes_review_result():
    init_git_repo()
    write_result(0, 1, has_issues=True)

    irg.write_task_md({"phase": "check_perspective", "reason": json.dumps({"idx": 0, "attempt": 1})})

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "has_issues" in content
    assert irg.PERSPECTIVES[0]["checker_prompt"][:20] in content


def test_fix_perspective_task_includes_flagged_issue():
    init_git_repo()
    write_result(0, 1, has_issues=True)

    irg.write_task_md({"phase": "fix_perspective", "reason": json.dumps({"idx": 0, "attempt": 1, "fix_attempt": 1})})

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "has_issues" in content
    assert "fix_0_fixattempt1.json" in content


def test_fix_perspective_task_includes_prior_recheck_feedback_on_retry():
    init_git_repo()
    write_result(0, 1, has_issues=True)
    write_recheck(0, 1, resolved=False, feedback="まだ直っていない")

    irg.write_task_md({"phase": "fix_perspective", "reason": json.dumps({"idx": 0, "attempt": 1, "fix_attempt": 2})})

    assert "まだ直っていない" in irg.TASK_MD.read_text(encoding="utf-8")


def test_recheck_perspective_task_includes_diff_and_checker_prompt():
    init_git_repo()
    import pathlib
    pathlib.Path("README.md").write_text("fixed content", encoding="utf-8")

    irg.write_task_md({"phase": "recheck_perspective", "reason": json.dumps({"idx": 0, "fix_attempt": 1})})

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "fixed content" in content
    assert irg.PERSPECTIVES[0]["checker_prompt"][:20] in content


def test_synthesize_task_includes_fixed_and_unresolved_sections():
    init_git_repo()
    irg._write_review_state(
        {
            "idx": irg.TOTAL_PERSPECTIVES,
            "redo_counts": {},
            "fix_counts": {},
            "unresolved": [{"idx": 1, "reason": "fix_not_resolved"}],
            "fixed": [0],
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


@pytest.mark.parametrize("phase", ["await_g2", "g2_approved"])
def test_terminal_phases_contain_done(phase):
    irg.write_task_md({"phase": phase, "reason": ""})
    assert "DONE" in irg.TASK_MD.read_text(encoding="utf-8")


def test_await_g2_mentions_review_cli_commands():
    irg.write_task_md({"phase": "await_g2", "reason": ""})
    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "masuda review approve" in content
    assert "masuda review reject" in content


def test_full_graph_run_writes_task_md():
    irg.PLAN_MD.write_text(SAMPLE_PLAN, encoding="utf-8")
    app = irg.build_graph()
    app.invoke({"phase": "", "reason": ""})
    assert irg.TASK_MD.exists()
    assert "実装" in irg.TASK_MD.read_text(encoding="utf-8")
