"""
Layer 1 tests for investigate_plan_graph.py: pure state-machine logic, no LLM
calls, no Docker, no real Claude invocations. Every case sets up state (on
disk, or in a real state daemon for the subset Issue #35 phase A moved
there -- see conftest.py's state_daemon fixture) and asserts the resulting
phase / TASK.md content.
"""
import importlib
import json

import pytest

import investigate_plan_graph as ipg
import state_client


@pytest.fixture(autouse=True)
def in_tmp_workspace(state_daemon):
    # ipg reads MASUDA_STATE_DIR once at import time (STATE_DIR is a module-level
    # constant, not resolved lazily -- see the module docstring). reload() re-runs
    # that top-level code with the freshly-set env var (state_daemon already set
    # it before yielding) so every test gets its own isolated state directory +
    # daemon instead of all tests sharing whatever STATE_DIR happened to be set
    # when this module was first imported.
    importlib.reload(ipg)
    yield state_daemon


def write_task_brief(text="タスクの説明"):
    state_client.put(ipg.TASK_BRIEF_KEY, text)


def write_plan(summary="...", steps=None, expected_byproducts=None):
    """ADR-0026: the plan is plan/summary.md (prose) + plan/steps.json
    (structured), not a single PLAN.md file. ADR-0028 wraps steps.json's
    content in {"steps": [...], "expected_byproducts": [...]}. Both stay
    plain files -- the planner subagent writes them with its own Edit tool
    (see the module docstring's writer/reader trust boundary)."""
    if steps is None:
        steps = [{"description": "step 1", "files": []}]
    ipg.PLAN_DIR.mkdir(parents=True, exist_ok=True)
    ipg.PLAN_SUMMARY_MD.write_text(summary, encoding="utf-8")
    data = {"steps": steps, "expected_byproducts": expected_byproducts or []}
    ipg.PLAN_STEPS_JSON.write_text(json.dumps(data), encoding="utf-8")


def write_gate_marker(status, feedback=""):
    state_client.put(ipg.GATE_KEY, json.dumps({"status": status, "feedback": feedback}))


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
    assert ipg._read_retries() == 1, "retry count must persist across process restarts"


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
    write_plan()
    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})
    assert state["phase"] == "await_g1"


def test_plan_done_without_a_marker_means_await_g1():
    """Unresolved is the *absence* of a marker, not a marker saying so:
    internal/gate never writes a "pending" one, and nothing may treat a
    present marker as anything but a decision waiting to be taken -- the
    daemon's own wait_for_gate_resolution returns the moment the key exists, so a
    marker that detect_phase decided to ignore would spin the loop.
    """
    write_plan()
    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})
    assert state["phase"] == "await_g1"


def test_plan_marker_with_an_unrecognised_status_fails_loudly():
    """...and one that does exist but carries a status nobody planned for is
    not guessed at. It fails before anything is consumed, so the marker is
    still there to look at."""
    write_plan()
    write_gate_marker("nonsense")

    with pytest.raises(ValueError):
        ipg.detect_phase({"phase": "", "retries": 0, "questions": []})

    assert state_client.exists(ipg.GATE_KEY)


def test_plan_approved_means_g1_approved():
    write_plan()
    write_gate_marker("approved", feedback="lgtm")
    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})
    assert state["phase"] == "g1_approved"


def test_g1_approval_survives_the_marker_being_taken():
    """detect_phase re-derives the phase from scratch on every invocation, so
    taking the marker must not lose the fact that G1 was approved. That is
    what PLAN_APPROVED_KEY carries -- previously the marker itself was left
    behind to serve as both the decision and the record of it."""
    write_plan()
    write_gate_marker("approved", feedback="lgtm")

    first = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})
    second = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})

    assert first["phase"] == second["phase"] == "g1_approved"
    assert not state_client.exists(ipg.GATE_KEY)
    assert state_client.exists(ipg.PLAN_APPROVED_KEY)


def test_g1_rejection_records_the_redo_as_it_takes_the_marker():
    """The redo feedback and the marker's removal are one atomic step: a
    crash between them used to be able to lose the rejection entirely, since
    the marker went away before anything recorded what it said."""
    write_plan()
    write_gate_marker("rejected", feedback="やり直し")

    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})

    assert state["phase"] == "plan_redo"
    assert state["questions"] == ["やり直し"]
    assert not state_client.exists(ipg.GATE_KEY)
    assert state_client.get(ipg.PLAN_REDO_PENDING_KEY) == "やり直し"


def test_plan_rejected_triggers_redo_and_consumes_marker():
    write_plan()
    write_gate_marker("rejected", feedback="この案は却下")

    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})

    assert state["phase"] == "plan_redo"
    assert state["questions"] == ["この案は却下"]
    assert not state_client.exists(ipg.GATE_KEY), "rejection must be consumed so it doesn't re-trigger forever"


# --- ADR-0039 (Issue #21): redo-pending markers survive a triage interrupt --

def test_plan_rejected_deletes_stale_plan_and_leaves_pending_marker():
    write_plan()
    write_gate_marker("rejected", feedback="この案は却下")

    ipg.detect_phase({"phase": "", "retries": 0, "questions": []})

    assert not ipg.PLAN_SUMMARY_MD.exists(), "the rejected plan must not linger to be mistaken for a fresh one"
    assert not ipg.PLAN_STEPS_JSON.exists()
    assert state_client.get(ipg.PLAN_REDO_PENDING_KEY) == "この案は却下"


def test_plan_redo_interrupted_before_rewrite_resumes_plan_redo_not_await_g1():
    """Simulates Issue #21: GATE_KEY already consumed (plan_redo pending),
    but the planner hasn't rewritten plan/summary.md + plan/steps.json yet
    (e.g. a triage interrupt landed first). A from-scratch re-derivation must
    not mistake this for a completed, awaiting-approval plan."""
    state_client.put(ipg.PLAN_REDO_PENDING_KEY, "この案は却下")
    assert not ipg.PLAN_SUMMARY_MD.exists()

    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})

    assert state["phase"] == "plan_redo"
    assert state["questions"] == ["この案は却下"]
    assert state_client.exists(ipg.PLAN_REDO_PENDING_KEY), "must stay pending until the planner actually rewrites the plan"


def test_plan_redo_completed_after_pending_clears_marker_and_reaches_await_g1():
    state_client.put(ipg.PLAN_REDO_PENDING_KEY, "この案は却下")
    write_plan(summary="改訂版")

    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})

    assert state["phase"] == "await_g1"
    assert not state_client.exists(ipg.PLAN_REDO_PENDING_KEY)


def test_investigate_redo_interrupted_before_rewrite_resumes_investigate_redo():
    """Same scenario as the plan_redo case above, for needs_more_investigation:
    plan_result.json already consumed, but the investigator hasn't updated
    INVESTIGATION.md yet. Falling through to "plan" here would silently drop
    the redo questions and proceed with the insufficient investigation."""
    ipg.INVESTIGATION_MD.write_text("古い調査内容", encoding="utf-8")
    ipg.INVESTIGATE_REDO_PENDING_JSON.write_text(
        json.dumps({"retries": 1, "questions": ["Q1", "Q2"]}), encoding="utf-8"
    )

    state = ipg.detect_phase({"phase": "", "retries": 1, "questions": []})

    assert state["phase"] == "investigate_redo"
    assert state["questions"] == ["Q1", "Q2"]
    assert state["retries"] == 1
    assert ipg.INVESTIGATE_REDO_PENDING_JSON.exists(), "must stay pending until the investigator clears it itself"


def test_investigate_redo_completed_after_investigator_clears_pending_marker():
    ipg.INVESTIGATION_MD.write_text("更新済みの調査内容", encoding="utf-8")
    # No INVESTIGATE_REDO_PENDING_JSON -- the investigator subagent already
    # deleted it as its own completion step (_investigate_task's instruction).

    state = ipg.detect_phase({"phase": "", "retries": 1, "questions": []})

    assert state["phase"] == "plan"


def test_investigate_redo_pending_marker_includes_deletion_instruction():
    write_task_brief()
    ipg.write_task_md({"phase": "investigate_redo", "retries": 1, "questions": ["未解決の疑問A"]})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert str(ipg.INVESTIGATE_REDO_PENDING_JSON) in content


def test_plan_redo_triage_interrupt_then_dismiss_resumes_plan_redo():
    """End-to-end regression test for Issue #21's exact repro: G1 reject ->
    triage interrupt before the planner rewrites the plan -> dismiss. Must
    land back on plan_redo (with the original feedback), never await_g1."""
    write_plan()
    write_gate_marker("rejected", feedback="駐車場・タイトルも含めて修正")
    first = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})
    assert first["phase"] == "plan_redo"

    # The planner hasn't rewritten the plan yet -- a triage concern interrupts.
    write_triage_concern(agent="planner", phase="plan_redo", description="誤検知の疑い")
    write_triage_marker("approved", feedback="誤検知でした")

    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})

    assert state["phase"] == "plan_redo"
    assert state["questions"] == ["駐車場・タイトルも含めて修正"]


def test_gate_marker_schema_matches_go_cli():
    """internal/gate/gate.go's Marker struct marshals to exactly this shape
    (json.Marshal with `status`, `feedback`, `decided_at` fields) — this is
    the daemon-value contract between the Go CLI and this orchestrator. A
    change to either side that breaks this shape must fail here first.
    """
    go_cli_output = """{
  "status": "approved",
  "feedback": "looks good",
  "decided_at": "2026-07-25T15:30:25.532891232+09:00"
}"""
    write_plan()
    state_client.put(ipg.GATE_KEY, go_cli_output)

    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})

    assert state["phase"] == "g1_approved"
    # The decision is taken off the gate; PLAN_APPROVED_KEY is what carries
    # the Go-side shape forward, so the contract is checked there now.
    assert not state_client.exists(ipg.GATE_KEY)
    recorded = json.loads(state_client.get(ipg.PLAN_APPROVED_KEY))
    assert recorded["status"] == "approved"
    assert recorded["feedback"] == "looks good"


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
    with pytest.raises(RuntimeError):
        ipg.write_task_md({"phase": "investigate", "retries": 0, "questions": []})


def test_investigate_redo_includes_questions():
    write_task_brief()
    ipg.write_task_md({"phase": "investigate_redo", "retries": 1, "questions": ["未解決の疑問A"]})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "未解決の疑問A" in content


def test_investigate_task_fact_checks_pre_written_instructions_when_present():
    write_task_brief()
    ipg.INSTRUCTIONS_MD.write_text("既存の指示書の内容", encoding="utf-8")
    ipg.write_task_md({"phase": "investigate", "retries": 0, "questions": []})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "指示書の検証結果" in content
    assert str(ipg.INSTRUCTIONS_MD) in content


def test_investigate_task_omits_instructions_section_when_absent():
    write_task_brief()
    assert not ipg.INSTRUCTIONS_MD.exists()
    ipg.write_task_md({"phase": "investigate", "retries": 0, "questions": []})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "指示書の検証結果" not in content


def test_plan_redo_includes_rejection_feedback():
    ipg.write_task_md({"phase": "plan_redo", "retries": 0, "questions": ["設計が複雑すぎる"]})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "設計が複雑すぎる" in content


def test_g1_approved_tells_the_human_how_to_reach_build():
    """G1承認はホスト側ループの終わりであってパイプラインの終わりではない。
    Build/ReviewはサンドボックスVMで動き、その起動は人間の操作なので、
    次に打つコマンドをワークスペースID込みで示す。

    以前の文面は「フェーズ3（プロジェクト初期化）以降はまだ実装されていません」
    で止まっており、事実として古い（未実装なのはScaffoldという予約名だけ）うえ、
    人間に次の一手を示していなかった。"""
    ipg.write_task_md({"phase": "g1_approved", "retries": 0, "questions": []})
    content = ipg.TASK_MD.read_text(encoding="utf-8")

    assert f"masuda sandbox start {ipg.STATE_DIR.name}" in content
    assert "まだ実装されていません" not in content


def test_investigate_task_omits_lsp():
    """investigator（フェーズ1）はADR-0012によりホストOS上で実行され、以下の
    3点からLSPに触れない: (1) ホスト実行のため対象リポジトリの
    `.masuda/images/<entry>/Dockerfile`が入れるLSPプラグインがそもそも存在
    しない、(2) `internal/hostloop/hostloop.go`がエージェント定義で
    `Tools: []string{"Read","Grep","Glob","Edit"}`という明示allowlistを持ち、
    ここに無いツールは呼べない（セッションレベルの`--allowedTools`にも無い
    ツールを勧めると無人ループが停止する危険がある）、(3) Bashが無いため
    依存解決（`go mod download`等）自体も実行できない。フェーズ4-5（サンドボックス
    内、implement_review_graph.py）とは対照的な、意図的な非対称。
    """
    write_task_brief()
    ipg.write_task_md({"phase": "investigate", "retries": 0, "questions": []})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "LSP" not in content


def test_plan_task_omits_lsp():
    """plannerもinvestigatorと同じ理由（ADR-0012、上記
    test_investigate_task_omits_lspのdocstring参照）でLSPに触れない。"""
    ipg.write_task_md({"phase": "plan", "retries": 0, "questions": []})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "LSP" not in content


@pytest.mark.parametrize("phase", ["g1_approved", "retries_exhausted", "iteration_budget_exceeded"])
def test_terminal_phases_contain_done(phase):
    ipg.write_task_md({"phase": phase, "retries": 0, "questions": []})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "DONE" in content


def test_await_g1_is_a_gate_not_a_terminal_done():
    """await_g1 keeps the session alive (GATE:plan, roadmap step 5) rather
    than ending it — it must not also say DONE, since the loop protocol
    treats the two conditions as mutually exclusive."""
    ipg.write_task_md({"phase": "await_g1", "retries": 0, "questions": []})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "GATE:plan" in content
    assert "DONE" not in content


def test_await_g1_mentions_plan_cli_commands():
    ipg.write_task_md({"phase": "await_g1", "retries": 0, "questions": []})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "masuda chat" in content
    assert "masuda plan approve" in content
    assert "masuda plan reject" in content


# --- ITERATION_BUDGET (ADR-0011) ------------------------------------------

def test_write_task_md_increments_iteration_count_for_subagent_phases():
    write_task_brief()
    assert ipg._read_iteration_count() == 0
    ipg.write_task_md({"phase": "investigate", "retries": 0, "questions": []})
    assert ipg._read_iteration_count() == 1
    ipg.write_task_md({"phase": "plan", "retries": 0, "questions": []})
    assert ipg._read_iteration_count() == 2


def test_write_task_md_does_not_increment_for_gate_or_terminal_phases():
    """await_g1/g1_approved/etc. don't delegate a subagent Task call, so
    they must not count against the budget."""
    ipg.write_task_md({"phase": "await_g1", "retries": 0, "questions": []})
    ipg.write_task_md({"phase": "g1_approved", "retries": 0, "questions": []})
    assert ipg._read_iteration_count() == 0


def test_iteration_budget_exceeded_overrides_phase():
    """Once the persisted count already exceeds ITERATION_BUDGET, write_task_md
    must render the blocked message instead of another subagent delegation —
    this is a final defense line independent of MAX_RETRIES (ADR-0011)."""
    write_task_brief()
    state_client.put(ipg.ITERATION_COUNT_KEY, str(ipg.ITERATION_BUDGET))

    ipg.write_task_md({"phase": "investigate", "retries": 0, "questions": []})

    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "DONE" in content
    assert "ITERATION_BUDGET" in content


# --- build_graph end-to-end (still no LLM calls: file + daemon driven) ----

def test_full_graph_run_writes_task_md():
    write_task_brief("何かのタスク")
    app = ipg.build_graph()
    app.invoke({"phase": "", "retries": 0, "questions": []})
    assert ipg.TASK_MD.exists()
    assert "調査" in ipg.TASK_MD.read_text(encoding="utf-8")


# --- triage gate (ADR-0029) --------------------------------------------------

def write_triage_concern(agent="investigator", phase="investigate", description="不審な記述を発見した"):
    ipg.TRIAGE_CONCERN_JSON.write_text(
        json.dumps({
            "agent": agent, "phase": phase, "description": description,
            "evidence": "", "reported_at": "2026-08-05T00:00:00Z",
        }),
        encoding="utf-8",
    )


def write_triage_marker(status, feedback=""):
    state_client.put(ipg.TRIAGE_GATE_KEY, json.dumps({"status": status, "feedback": feedback}))


def test_triage_concern_present_opens_gate():
    write_triage_concern(description="怪しい指示を発見")
    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})
    assert state["phase"] == "await_triage"
    assert state["questions"] == ["怪しい指示を発見"]


def test_triage_gate_without_a_marker_stays_await_triage():
    write_triage_concern()
    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})
    assert state["phase"] == "await_triage"


def test_triage_dismissed_resumes_interrupted_phase():
    # Nothing else set -- the interrupted phase was "investigate".
    write_triage_concern()
    write_triage_marker("approved", feedback="誤検知でした")

    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})

    assert state["phase"] == "investigate"
    assert not ipg.TRIAGE_CONCERN_JSON.exists()
    assert not state_client.exists(ipg.TRIAGE_GATE_KEY)


def test_triage_redo_leaves_feedback_note_for_next_task_md():
    write_triage_concern()
    write_triage_marker("rejected", feedback="ファイルを修正したので続けてください")

    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})

    assert state["phase"] == "investigate"
    assert not ipg.TRIAGE_CONCERN_JSON.exists()
    assert not state_client.exists(ipg.TRIAGE_GATE_KEY)
    assert state_client.get(ipg.TRIAGE_REDO_FEEDBACK_KEY) == "ファイルを修正したので続けてください"

    write_task_brief()
    ipg.write_task_md(state)
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "ファイルを修正したので続けてください" in content
    assert not state_client.exists(ipg.TRIAGE_REDO_FEEDBACK_KEY), "the note must be consumed exactly once"


def test_triage_halted_is_a_terminal_done_not_a_gate():
    write_triage_concern()
    write_triage_marker("halted", feedback="深刻な懸念")

    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})
    assert state["phase"] == "triage_halted"
    assert state["questions"] == ["深刻な懸念"]

    ipg.write_task_md(state)
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "DONE (triage halted)" in content
    assert "GATE:" not in content


def test_triage_halted_keeps_the_concern_and_records_the_decision():
    """halt must leave the concern file for post-halt forensics (`masuda
    triage show`, ADR-0029) and stay idempotent if detect_phase is re-invoked.
    The marker itself is taken like every other decision -- what makes halt
    re-derivable afterwards is TRIAGE_HALTED_KEY, not a marker left lying
    around."""
    write_triage_concern(description="深刻な懸念の詳細")
    write_triage_marker("halted", feedback="深刻な懸念")

    first = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})
    second = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})

    assert first["phase"] == second["phase"] == "triage_halted"
    assert first["questions"] == second["questions"] == ["深刻な懸念"]
    assert ipg.TRIAGE_CONCERN_JSON.exists()
    assert not state_client.exists(ipg.TRIAGE_GATE_KEY)
    assert json.loads(state_client.get(ipg.TRIAGE_HALTED_KEY))["feedback"] == "深刻な懸念"


def _setup_investigate_redo():
    ipg.INVESTIGATION_MD.write_text("...", encoding="utf-8")
    ipg.PLAN_RESULT_JSON.write_text(
        json.dumps({"status": "needs_more_investigation", "questions": ["Q"]}), encoding="utf-8",
    )


def _setup_plan_redo():
    write_plan()
    write_gate_marker("rejected", feedback="却下")


@pytest.mark.parametrize("setup_phase,setup", [
    ("investigate_redo", _setup_investigate_redo),
    ("plan_redo", _setup_plan_redo),
    ("await_g1", write_plan),
])
def test_triage_preempts_every_other_phase(setup_phase, setup):
    """Whatever phase would otherwise be detected, a pending triage concern
    must win (ADR-0029: this is the most-urgent layer, strictly ahead of
    ordinary G1 flow)."""
    setup()
    # Sanity check: without the concern, we really would land on setup_phase.
    baseline = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})
    assert baseline["phase"] == setup_phase

    write_triage_concern()
    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})
    assert state["phase"] == "await_triage"


def test_gate_marker_schema_matches_go_cli_for_halted_status():
    """internal/gate/gate.go's Halted status must round-trip the same way
    Approved/Rejected already do (test_gate_marker_schema_matches_go_cli)."""
    go_cli_output = """{
  "status": "halted",
  "feedback": "深刻な懸念のため停止",
  "decided_at": "2026-08-05T15:30:25.532891232+09:00"
}"""
    write_triage_concern()
    state_client.put(ipg.TRIAGE_GATE_KEY, go_cli_output)

    state = ipg.detect_phase({"phase": "", "retries": 0, "questions": []})

    assert state["phase"] == "triage_halted"
    assert not state_client.exists(ipg.TRIAGE_GATE_KEY)
    recorded = json.loads(state_client.get(ipg.TRIAGE_HALTED_KEY))
    assert recorded["status"] == "halted"
    assert recorded["feedback"] == "深刻な懸念のため停止"


def test_investigate_task_includes_triage_self_report_section():
    write_task_brief()
    ipg.write_task_md({"phase": "investigate", "retries": 0, "questions": []})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert str(ipg.TRIAGE_CONCERN_JSON) in content


def test_plan_task_includes_triage_self_report_section():
    ipg.write_task_md({"phase": "plan", "retries": 0, "questions": []})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert str(ipg.TRIAGE_CONCERN_JSON) in content


def test_await_triage_mentions_triage_cli_commands():
    ipg.write_task_md({"phase": "await_triage", "retries": 0, "questions": ["懸念の概要"]})
    content = ipg.TASK_MD.read_text(encoding="utf-8")
    assert "GATE:triage" in content
    assert "masuda triage dismiss" in content
    assert "masuda triage redo" in content
    assert "masuda triage halt" in content
    assert "DONE" not in content
