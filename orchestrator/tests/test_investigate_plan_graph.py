"""
Layer 1 tests for investigate_plan_graph.py: pure state-machine logic, no LLM
calls, no Docker, no real Claude invocations. Every case sets up on-disk state
in a temp directory and asserts the resulting phase / TASK.md content.
"""
import json

import pytest

import investigate_plan_graph as ipg


@pytest.fixture(autouse=True)
def in_tmp_worktree(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    yield tmp_path


def write_task_brief(text="タスクの説明"):
    ipg.TASK_BRIEF.write_text(text, encoding="utf-8")


# --- detect_phase -------------------------------------------------------

def test_no_files_means_investigate():
    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})
    assert state["phase"] == "investigate"


def test_investigation_present_no_plan_means_plan():
    ipg.INVESTIGATION_MD.write_text("...", encoding="utf-8")
    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})
    assert state["phase"] == "plan"


def test_needs_more_investigation_triggers_redo_and_consumes_signal():
    ipg.INVESTIGATION_MD.write_text("...", encoding="utf-8")
    ipg.PLAN_RESULT_JSON.write_text(
        json.dumps({"status": "needs_more_investigation", "questions": ["Q1", "Q2"]}),
        encoding="utf-8",
    )

    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})

    assert state["phase"] == "investigate_redo"
    assert state["questions"] == ["Q1", "Q2"]
    assert state["retries"] == 1
    assert not ipg.PLAN_RESULT_JSON.exists(), "signal must be consumed so it doesn't re-trigger forever"
    assert ipg._read_retries() == 1, "retry count must persist to disk across process restarts"


def test_retries_exhausted_stops_and_preserves_plan_result_for_inspection():
    ipg.INVESTIGATION_MD.write_text("...", encoding="utf-8")
    ipg._write_retries(ipg.MAX_RETRIES)
    ipg.PLAN_RESULT_JSON.write_text(
        json.dumps({"status": "needs_more_investigation", "questions": ["Q1"]}),
        encoding="utf-8",
    )

    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})

    assert state["phase"] == "retries_exhausted"
    assert ipg.PLAN_RESULT_JSON.exists(), "kept for human inspection, unlike the mid-retry case"


def test_plan_done_no_marker_means_await_g1():
    ipg.PLAN_MD.write_text("...", encoding="utf-8")
    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})
    assert state["phase"] == "await_g1"


def test_plan_done_pending_marker_means_await_g1():
    ipg.PLAN_MD.write_text("...", encoding="utf-8")
    ipg.GATE_MARKER.parent.mkdir(parents=True)
    ipg.GATE_MARKER.write_text(json.dumps({"status": "pending"}), encoding="utf-8")
    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})
    assert state["phase"] == "await_g1"


def test_plan_approved_means_g1_approved():
    ipg.PLAN_MD.write_text("...", encoding="utf-8")
    ipg.GATE_MARKER.parent.mkdir(parents=True)
    ipg.GATE_MARKER.write_text(json.dumps({"status": "approved", "feedback": "lgtm"}), encoding="utf-8")
    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})
    assert state["phase"] == "g1_approved"


def test_plan_rejected_triggers_redo_and_consumes_marker():
    ipg.PLAN_MD.write_text("...", encoding="utf-8")
    ipg.GATE_MARKER.parent.mkdir(parents=True)
    ipg.GATE_MARKER.write_text(
        json.dumps({"status": "rejected", "feedback": "この案は却下"}), encoding="utf-8"
    )

    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})

    assert state["phase"] == "plan_redo"
    assert state["questions"] == ["この案は却下"]
    assert not ipg.GATE_MARKER.exists(), "rejection must be consumed so it doesn't re-trigger forever"


def test_gate_marker_schema_matches_go_cli():
    """internal/gate/gate.go's Marker struct marshals to exactly this shape
    (json.MarshalIndent with `status`, `feedback`, `decided_at` fields) —
    this is the file-based contract between the Go CLI and this orchestrator.
    A change to either side that breaks this shape must fail here first.
    """
    go_cli_output = """{
  "status": "approved",
  "feedback": "looks good",
  "decided_at": "2026-07-25T15:30:25.532891232+09:00"
}"""
    ipg.PLAN_MD.write_text("...", encoding="utf-8")
    ipg.GATE_MARKER.parent.mkdir(parents=True)
    ipg.GATE_MARKER.write_text(go_cli_output, encoding="utf-8")

    marker = ipg._read_gate_marker()

    assert marker["status"] == "approved"
    assert marker["feedback"] == "looks good"


# --- write_task_md --------------------------------------------------------

def test_investigate_task_includes_task_brief():
    write_task_brief("FizzBuzzを実装する")
    ipg.write_task_md({"phase": "investigate", "retries": 0, "questions": []})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "FizzBuzzを実装する" in content
    assert "DONE" not in content


def test_investigate_task_delegates_to_no_bash_investigator_agent():
    """The investigation subagent must be the Bash-less `investigator` custom
    agent (internal/hostloop.go defines it with no Bash tool) — general-purpose
    delegation would inherit Bash and defeat the point. See ADR discussion:
    Claude Code applies its own risk judgment to Bash regardless of
    --allowedTools, so keeping Bash off entirely for content-reading subagents
    is the only reliable boundary.
    """
    write_task_brief()
    ipg.write_task_md({"phase": "investigate", "retries": 0, "questions": []})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "subagent_type: investigator" in content


def test_plan_task_delegates_to_no_bash_planner_agent():
    ipg.write_task_md({"phase": "plan", "retries": 0, "questions": []})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "subagent_type: planner" in content


def test_investigate_missing_task_brief_raises():
    with pytest.raises(FileNotFoundError):
        ipg.write_task_md({"phase": "investigate", "retries": 0, "questions": []})


def test_investigate_redo_includes_questions():
    write_task_brief()
    ipg.write_task_md({"phase": "investigate_redo", "retries": 1, "questions": ["未解決の疑問A"]})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "未解決の疑問A" in content


def test_plan_redo_includes_rejection_feedback():
    ipg.write_task_md({"phase": "plan_redo", "retries": 0, "questions": ["設計が複雑すぎる"]})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "設計が複雑すぎる" in content


@pytest.mark.parametrize("phase", ["await_g1", "g1_approved", "retries_exhausted"])
def test_terminal_phases_contain_done(phase):
    ipg.write_task_md({"phase": phase, "retries": 0, "questions": []})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "DONE" in content


def test_await_g1_mentions_plan_cli_commands():
    ipg.write_task_md({"phase": "await_g1", "retries": 0, "questions": []})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "masuda plan approve" in content
    assert "masuda plan reject" in content


# --- build_graph end-to-end (still no LLM calls: pure file-driven) --------

def test_full_graph_run_writes_task_md():
    write_task_brief("何かのタスク")
    app = ipg.build_graph()
    app.invoke({"phase": "", "retries": 0, "questions": []})
    assert ipg.TASK_MD.exists()
    assert "調査" in ipg.TASK_MD.read_text(encoding="utf-8")
