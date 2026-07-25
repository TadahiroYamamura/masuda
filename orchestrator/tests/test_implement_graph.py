"""
Layer 1 tests for implement_graph.py: pure state-machine + mechanical-backstop
logic, no LLM calls, no Docker, no real Claude invocations.
"""
import json
import subprocess

import pytest

import implement_graph as ig

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
    # An initial commit so `git status --porcelain` reports new/modified files
    # relative to something, matching a real worktree (which always starts
    # from a base branch commit).
    ig.PLAN_MD.write_text(SAMPLE_PLAN, encoding="utf-8")
    subprocess.run(["git", "add", "PLAN.md"], check=True)
    subprocess.run(["git", "commit", "-q", "-m", "init"], check=True)


# --- _extract_planned_files ------------------------------------------------

def test_extract_planned_files_reads_top_level_bullets_only():
    files = ig._extract_planned_files(SAMPLE_PLAN)
    assert files == {"README.md", "cmd/masuda/main.go"}


def test_extract_planned_files_ignores_inline_code_in_sub_bullets():
    files = ig._extract_planned_files(SAMPLE_PLAN)
    assert "echo hello" not in files


def test_extract_planned_files_missing_section_returns_empty():
    assert ig._extract_planned_files("# PLAN.md\n\nno such section here\n") == set()


# --- _actual_changed_files / _mechanical_deviation -------------------------

def test_actual_changed_files_excludes_masuda_internal_files():
    init_git_repo()
    ig.TASK_MD.write_text("...", encoding="utf-8")
    ig.IMPLEMENTATION_RESULT_JSON.write_text('{"status": "done"}', encoding="utf-8")
    (ig.GATE_MARKER.parent).mkdir(parents=True, exist_ok=True)
    ig.GATE_MARKER.write_text('{"status": "approved"}', encoding="utf-8")

    assert ig._actual_changed_files() == set()


def test_mechanical_deviation_none_when_changes_within_plan():
    init_git_repo()
    import pathlib
    pathlib.Path("README.md").write_text("updated", encoding="utf-8")

    assert ig._mechanical_deviation() is None


def test_mechanical_deviation_detects_unplanned_file():
    init_git_repo()
    import pathlib
    pathlib.Path("README.md").write_text("updated", encoding="utf-8")
    pathlib.Path("secrets.txt").write_text("oops", encoding="utf-8")

    reason = ig._mechanical_deviation()

    assert reason is not None
    assert "secrets.txt" in reason


# --- detect_phase -----------------------------------------------------------

def test_no_result_means_implement():
    state = ig.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "implement"


def test_done_with_no_deviation_means_complete():
    init_git_repo()
    import pathlib
    pathlib.Path("README.md").write_text("updated", encoding="utf-8")
    ig.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")

    state = ig.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "implementation_complete"


def test_done_with_unplanned_file_reopens_plan():
    init_git_repo()
    import pathlib
    pathlib.Path("unplanned.txt").write_text("oops", encoding="utf-8")
    ig.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")

    state = ig.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "plan_reopened"
    assert "unplanned.txt" in state["reason"]


def test_needs_plan_review_reopens_plan():
    ig.IMPLEMENTATION_RESULT_JSON.write_text(
        json.dumps({"status": "needs_plan_review", "reason": "設計を変える必要がある"}), encoding="utf-8"
    )
    state = ig.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "plan_reopened"
    assert "設計を変える必要がある" in state["reason"]


def test_build_test_failed_reopens_plan():
    ig.IMPLEMENTATION_RESULT_JSON.write_text(
        json.dumps({"status": "build_test_failed", "details": "3回試したがテストが通らない"}), encoding="utf-8"
    )
    state = ig.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "plan_reopened"
    assert "3回試したがテストが通らない" in state["reason"]


def test_unknown_status_raises():
    ig.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "???"}), encoding="utf-8")
    with pytest.raises(ValueError):
        ig.detect_phase({"phase": "", "reason": ""})


# --- write_task_md -----------------------------------------------------------

def test_implement_task_includes_plan_content():
    ig.PLAN_MD.write_text(SAMPLE_PLAN, encoding="utf-8")
    ig.write_task_md({"phase": "implement", "reason": ""})
    content = ig.TASK_MD.read_text(encoding="utf-8")
    assert "変更するファイル一覧" in content
    assert "DONE" not in content


def test_implement_missing_plan_raises():
    with pytest.raises(FileNotFoundError):
        ig.write_task_md({"phase": "implement", "reason": ""})


def test_plan_reopened_writes_deviation_and_clears_state():
    ig.GATE_MARKER.parent.mkdir(parents=True)
    ig.GATE_MARKER.write_text(json.dumps({"status": "approved"}), encoding="utf-8")
    ig.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")

    ig.write_task_md({"phase": "plan_reopened", "reason": "計画外のファイル変更"})

    assert ig.DEVIATION_MD.read_text(encoding="utf-8") == "計画外のファイル変更"
    assert not ig.GATE_MARKER.exists(), "reopening must reset the gate so a stale approval isn't reused"
    assert not ig.IMPLEMENTATION_RESULT_JSON.exists(), "stale result must be cleared so a retry starts clean"
    content = ig.TASK_MD.read_text(encoding="utf-8")
    assert "DONE" in content
    assert "計画外のファイル変更" in content


def test_implementation_complete_contains_done():
    ig.write_task_md({"phase": "implementation_complete", "reason": ""})
    assert "DONE" in ig.TASK_MD.read_text(encoding="utf-8")


def test_full_graph_run_writes_task_md():
    ig.PLAN_MD.write_text(SAMPLE_PLAN, encoding="utf-8")
    app = ig.build_graph()
    app.invoke({"phase": "", "reason": ""})
    assert ig.TASK_MD.exists()
    assert "実装" in ig.TASK_MD.read_text(encoding="utf-8")
