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
import state_client

# ADR-0026/0027: the plan is plan/summary.md (prose) + plan/steps.json
# (structured, machine-parseable), with each step declaring its own file
# list rather than one flat plan-wide list.
SAMPLE_STEPS = [
    {
        "description": "README.mdを直す",
        "files": [{"path": "README.md", "description": "1行追記する"}],
    },
]

TWO_STEP_PLAN = [
    {
        "description": "README.mdを直す",
        "files": [{"path": "README.md", "description": "1行追記する"}],
    },
    {
        "description": "main.goを直す",
        "files": [{"path": "cmd/masuda/main.go", "description": "フラグを1つ追加する"}],
    },
]

# TDD mode (Issue #3): a step marked mode: "tdd" goes through the
# Red/Green/Refactor sub-loop (_detect_tdd_phase) instead of the normal
# single-shot implement_step path.
TDD_STEP_PLAN = [
    {
        "description": "新機能をTDDで実装",
        "mode": "tdd",
        "files": [
            {"path": "feature.go", "description": "新機能本体"},
            {"path": "feature_test.go", "description": "対応するテスト"},
        ],
    },
]

MIXED_MODE_PLAN = [
    {
        "description": "依存関係を追加",
        "files": [{"path": "go.mod", "description": "依存追加"}],
    },
    {
        "description": "新機能をTDDで実装",
        "mode": "tdd",
        "files": [
            {"path": "feature.go", "description": "新機能本体"},
            {"path": "feature_test.go", "description": "対応するテスト"},
        ],
    },
]


TEST_PERSPECTIVE_COUNT = 14
# Zero-padded so string-sort order (irg.PERSPECTIVE_IDS is `sorted(...)`)
# matches numeric order -- p00, p01, ..., p13, not p0, p1, p10, p11, ...
TEST_PERSPECTIVE_IDS = [f"p{i:02d}" for i in range(TEST_PERSPECTIVE_COUNT)]


def _write_test_perspectives(reviews_dir, triggered_ids=(), disabled_ids=()):
    """Seeds .masuda/reviews/ (ADR-0024) with a minimal synthetic set of
    perspectives for tests -- production's real 14 built-ins live in
    internal/perspectives/builtin (Go side) and shouldn't be duplicated into
    this Python test suite; only the id scheme and count matter here.
    `triggered_ids` (ADR-0027) adds a `trigger` frontmatter field to the
    named perspectives so tests can exercise phase 4's interim review;
    perspectives are otherwise untriggered by default (mirrors most of
    masuda's real built-ins, which are trigger-less at seed time).
    `disabled_ids` (ADR-0033) writes `enable: false` for the named
    perspectives -- the file still exists, only excluded from
    _load_perspectives()'s result."""
    reviews_dir.mkdir(parents=True, exist_ok=True)
    for pid in TEST_PERSPECTIVE_IDS:
        trigger_line = f'trigger: "{pid}に関する変更"\n' if pid in triggered_ids else ""
        enable_line = "enable: false\n" if pid in disabled_ids else ""
        (reviews_dir / f"{pid}.md").write_text(
            f'---\nname: "テスト観点{pid}"\n{trigger_line}{enable_line}---\n'
            f"テスト観点{pid}のreview_prompt本文。\n",
            encoding="utf-8",
        )


@pytest.fixture(autouse=True)
def in_tmp_workspace(tmp_path, monkeypatch, state_daemon):
    # Mirrors production's split (roadmap step 7): git commands run against
    # worktree_dir (this process's cwd, same as before), while masuda's own
    # control files resolve under a separate state directory. Some of that
    # state (irg.PLAN_STEPS_JSON, irg.IMPLEMENTATION_RESULT_JSON, ...) is
    # still plain files there; the rest (gate markers, DEVIATION.md, redo/
    # iteration counters -- Issue #35 phase A) now lives in a real state
    # daemon the state_daemon fixture starts and points MASUDA_STATE_DIR at.
    # irg reads that env var once at import time (STATE_DIR is a
    # module-level constant), so it must be reload()ed after monkeypatching
    # for each test to get its own isolated state directory + daemon.
    #
    # Perspectives (ADR-0024) are now also read at import time, from
    # .masuda/reviews/ under cwd -- so that directory must exist before the
    # reload too, or the module import itself raises FileNotFoundError.
    worktree_dir = tmp_path / "worktree"
    worktree_dir.mkdir()
    _write_test_perspectives(worktree_dir / ".masuda" / "reviews")
    monkeypatch.chdir(worktree_dir)
    importlib.reload(irg)
    yield worktree_dir


def write_plan(steps=None, summary="テスト用のプラン。", expected_byproducts=None):
    """ADR-0028 wraps steps.json's content in {"steps": [...],
    "expected_byproducts": [...]}."""
    if steps is None:
        steps = SAMPLE_STEPS
    irg.PLAN_DIR.mkdir(parents=True, exist_ok=True)
    irg.PLAN_SUMMARY_MD.write_text(summary, encoding="utf-8")
    data = {"steps": steps, "expected_byproducts": expected_byproducts or []}
    irg.PLAN_STEPS_JSON.write_text(json.dumps(data), encoding="utf-8")


def init_git_repo(steps=None, expected_byproducts=None):
    subprocess.run(["git", "init", "-q"], check=True)
    subprocess.run(["git", "config", "user.email", "test@example.com"], check=True)
    subprocess.run(["git", "config", "user.name", "test"], check=True)
    # A baseline commit so `git status --porcelain` / `git diff` report
    # new/modified files relative to something, matching a real worktree
    # (which always starts from a base branch commit). The plan itself is
    # masuda's own control data and lives in STATE_DIR (roadmap step 7), never
    # inside the git-managed worktree, so it's written separately here rather
    # than committed as part of the repo's baseline. .masuda/reviews/ (ADR-0024)
    # is committed here, though -- it's part of the target repo proper (like
    # .masuda.json used to be), and the fixture already wrote it before this
    # runs; leaving it untracked would make the ADR-0010 mechanical backstop
    # mistake it for an unplanned file change on every test.
    pathlib.Path("README.md").write_text("baseline", encoding="utf-8")
    subprocess.run(["git", "add", "README.md", ".masuda"], check=True)
    subprocess.run(["git", "commit", "-q", "-m", "init"], check=True)
    # Pin BASE_REF_FILE to this exact commit rather than relying on
    # _read_base_ref()'s "develop" fallback string -- some environments'
    # `git init` defaults new repos to a branch literally named "develop",
    # which would make `develop..HEAD` always empty (develop *is* HEAD) and
    # silently break _completed_step_count()/_compute_diff() for any test
    # that commits further steps.
    base_sha = subprocess.run(
        ["git", "rev-parse", "HEAD"], capture_output=True, text=True, check=True
    ).stdout.strip()
    irg.BASE_REF_FILE.write_text(base_sha, encoding="utf-8")
    write_plan(steps=steps, expected_byproducts=expected_byproducts)


def mark_step_done(changed_files, commit_message="update"):
    """ADR-0027: the implementation subagent self-reports both a commit
    message and the files it intentionally changed, so _finalize_step's
    eventual `git commit` has something to stage/land."""
    irg.IMPLEMENTATION_RESULT_JSON.write_text(
        json.dumps({"status": "done", "changed_files": changed_files}), encoding="utf-8"
    )
    irg.STEP_COMMIT_MESSAGE_FILE.write_text(commit_message, encoding="utf-8")


def mark_tdd_phase_done(changed_files, commit_message="update", tdd_next_phase=None):
    """A TDD phase's (Red/Green/Refactor) implementation subagent self-report
    -- mirrors mark_step_done's shape, plus the optional tdd_next_phase key
    only a Refactor turn ever needs (see _advance_tdd_cycle). The commit
    message file is only written when there's something to commit -- an
    honestly-reported "no changes needed" Refactor turn (changed_files=[])
    never gets committed, matching what a real subagent would do per
    _tdd_completion_section's instructions."""
    result = {"status": "done", "changed_files": changed_files}
    if tdd_next_phase is not None:
        result["tdd_next_phase"] = tdd_next_phase
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps(result), encoding="utf-8")
    if changed_files:
        irg.TDD_CYCLE_COMMIT_MESSAGE_FILE.write_text(commit_message, encoding="utf-8")


def write_tdd_check(step_index, cycle, phase, attempt, ok, feedback=""):
    path = irg._tdd_check_path(step_index, cycle, phase, attempt)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps({"ok": ok, "feedback": feedback}), encoding="utf-8")


def mark_implementation_done_and_clean():
    """A single-step plan (SAMPLE_STEPS) whose one step is implemented,
    self-reported done, and ready to cleanly land -- the baseline most phase
    5 (G2 review) tests build on, since detect_phase will commit this step
    and fall straight through to phase 5 the moment it's invoked."""
    init_git_repo()
    pathlib.Path("README.md").write_text("updated", encoding="utf-8")
    mark_step_done(["README.md"])


def write_result(pid, attempt, has_issues=False, results_dir=None):
    results_dir = results_dir or irg.REVIEW_RESULTS_DIR
    results_dir.mkdir(parents=True, exist_ok=True)
    irg._result_path(results_dir, pid, attempt).write_text(
        json.dumps({"perspective_id": pid, "has_issues": has_issues, "issues": [], "summary": "ok"}),
        encoding="utf-8",
    )


def write_check(pid, attempt, ok, feedback="", results_dir=None):
    results_dir = results_dir or irg.REVIEW_RESULTS_DIR
    results_dir.mkdir(parents=True, exist_ok=True)
    irg._check_path(results_dir, pid, attempt).write_text(
        json.dumps({"perspective_id": pid, "ok": ok, "feedback": feedback}), encoding="utf-8"
    )


def write_fix(pid, fix_attempt, results_dir=None):
    results_dir = results_dir or irg.REVIEW_RESULTS_DIR
    results_dir.mkdir(parents=True, exist_ok=True)
    irg._fix_path(results_dir, pid, fix_attempt).write_text(json.dumps({"status": "fixed"}), encoding="utf-8")


def write_recheck(pid, fix_attempt, resolved, feedback="", results_dir=None):
    results_dir = results_dir or irg.REVIEW_RESULTS_DIR
    results_dir.mkdir(parents=True, exist_ok=True)
    irg._recheck_path(results_dir, pid, fix_attempt).write_text(
        json.dumps({"resolved": resolved, "feedback": feedback}), encoding="utf-8"
    )


def write_all_perspectives_clean():
    for pid in irg.PERSPECTIVE_IDS:
        write_result(pid, 1, has_issues=False)
        write_check(pid, 1, ok=True)


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
    show up alongside it in the batch. `skip` is the id (or set of ids)
    under test, left untouched."""
    skip_ids = {skip} if isinstance(skip, str) else set(skip)
    for pid in irg.PERSPECTIVE_IDS:
        if pid in skip_ids:
            continue
        write_result(pid, 1, has_issues=False)
        write_check(pid, 1, ok=True)


def batch_state(*tasks):
    return {"phase": "review_batch", "reason": json.dumps({"tasks": list(tasks)})}


# --- _step_planned_files / _all_planned_files / _completed_step_count -----

def test_step_planned_files_reads_this_steps_files_only():
    assert irg._step_planned_files(TWO_STEP_PLAN[0]) == {"README.md"}
    assert irg._step_planned_files(TWO_STEP_PLAN[1]) == {"cmd/masuda/main.go"}


def test_all_planned_files_unions_every_step():
    assert irg._all_planned_files(TWO_STEP_PLAN) == {"README.md", "cmd/masuda/main.go"}


def test_completed_step_count_is_zero_before_any_step_commit():
    init_git_repo()
    assert irg._completed_step_count() == 0


def test_completed_step_count_counts_tags_not_raw_commits():
    """TDD mode (Issue #3) can land several commits within a single step, so
    _completed_step_count() no longer treats "commits ahead of base_ref" as
    synonymous with "steps landed" -- only a masuda-step-<workspace-id>-<N>
    tag (_tag_step) marks a step as actually complete."""
    init_git_repo()
    pathlib.Path("extra.txt").write_text("x", encoding="utf-8")
    subprocess.run(["git", "add", "extra.txt"], check=True)
    subprocess.run(["git", "commit", "-q", "-m", "one step landed"], check=True)
    assert irg._completed_step_count() == 0
    irg._tag_step(0)
    assert irg._completed_step_count() == 1


def test_completed_step_count_ignores_tags_outside_base_ref_range():
    """A masuda-step-* tag that leaked in from an unrelated task (e.g. via
    `git clone --local` copying repoRoot's tags wholesale, ADR-0018) must not
    be counted just because its name happens to match this workspace's id --
    only tags on commits actually within this branch's own base_ref..HEAD
    range count."""
    init_git_repo()
    # A tag on the baseline commit itself -- outside base_ref..HEAD's
    # (exclusive of base_ref) range, standing in for a foreign tag that
    # predates this branch's own history.
    subprocess.run(["git", "tag", f"masuda-step-{irg._workspace_id()}-99"], check=True)
    assert irg._completed_step_count() == 0

    pathlib.Path("extra.txt").write_text("x", encoding="utf-8")
    subprocess.run(["git", "add", "extra.txt"], check=True)
    subprocess.run(["git", "commit", "-q", "-m", "one step landed"], check=True)
    irg._tag_step(0)
    assert irg._completed_step_count() == 1


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
    state_client.put(irg.PLAN_GATE_KEY, '{"status": "approved"}')

    assert irg._actual_changed_files() == set()


def test_mechanical_deviation_none_when_changes_within_plan():
    init_git_repo()
    pathlib.Path("README.md").write_text("updated", encoding="utf-8")
    assert irg._mechanical_deviation({"README.md"}) is None


def test_mechanical_deviation_detects_unplanned_file():
    init_git_repo()
    pathlib.Path("README.md").write_text("updated", encoding="utf-8")
    pathlib.Path("secrets.txt").write_text("oops", encoding="utf-8")

    reason = irg._mechanical_deviation({"README.md"})

    assert reason is not None
    assert "secrets.txt" in reason


def test_standalone_review_skips_backstop_without_a_plan():
    """Standalone review (`masuda review start`, roadmap step 6) never goes
    through G1, so there's no plan/steps.json and nothing to have deviated
    from -- detect_phase must skip the backstop entirely rather than raise
    trying to read a plan that doesn't exist."""
    init_git_repo()
    irg.PLAN_STEPS_JSON.unlink()
    irg.PLAN_SUMMARY_MD.unlink()
    pathlib.Path("anything.txt").write_text("whatever", encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")
    resolve_other_perspectives_as_clean(skip="p00")

    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"id": "p00", "kind": "review", "attempt": 1}]


# --- expected_byproducts (ADR-0028) -----------------------------------------

def test_read_expected_byproducts_returns_empty_without_a_plan():
    assert irg._read_expected_byproducts() == []


def test_read_expected_byproducts_reads_plan_data():
    init_git_repo(expected_byproducts=["**/__pycache__/**", "**/*.pyc"])
    assert irg._read_expected_byproducts() == ["**/__pycache__/**", "**/*.pyc"]


# --- _glob_to_regex / _is_expected_byproduct: standard glob, not fnmatch ---
# (ADR-0028: a lone `*` must not cross `/`, unlike fnmatch -- pinned down
# explicitly since an fnmatch-based first version silently matched far more
# broadly than its patterns implied.)

def test_single_star_does_not_cross_path_separator():
    assert irg._is_expected_byproduct("a.pyc", ["*.pyc"])
    assert not irg._is_expected_byproduct("dir/a.pyc", ["*.pyc"])


def test_double_star_crosses_path_separators():
    patterns = ["**/*.pyc"]
    assert irg._is_expected_byproduct("a.pyc", patterns), "**/ must also match zero directories"
    assert irg._is_expected_byproduct("dir/a.pyc", patterns)
    assert irg._is_expected_byproduct("dir/sub/a.pyc", patterns)


def test_double_star_directory_pattern_matches_root_and_nested():
    patterns = ["**/__pycache__/**"]
    assert irg._is_expected_byproduct("__pycache__/mod.cpython-312.pyc", patterns)
    assert irg._is_expected_byproduct("mathutils/__pycache__/ops.cpython-312.pyc", patterns)
    assert not irg._is_expected_byproduct("mathutils/ops.py", patterns)


def test_literal_pattern_without_wildcards_matches_only_that_exact_path():
    assert irg._is_expected_byproduct("merged.yaml", ["merged.yaml"])
    assert not irg._is_expected_byproduct("api/merged.yaml", ["merged.yaml"])


def test_question_mark_matches_exactly_one_non_separator_character():
    assert irg._is_expected_byproduct("a1c", ["a?c"])
    assert not irg._is_expected_byproduct("ac", ["a?c"])
    assert not irg._is_expected_byproduct("a/c", ["a?c"])


def test_literal_regex_metacharacters_are_not_treated_as_regex():
    """The dot in "merged.yaml" must match a literal dot, not "any character"
    -- otherwise "mergedXyaml" would incorrectly count as a match too."""
    assert not irg._is_expected_byproduct("mergedXyaml", ["merged.yaml"])


def test_mechanical_deviation_exempts_predicted_byproducts():
    """The exact scenario found on a live run: pytest, executed by the
    implementation subagent's own self-verification loop (ADR-0009), leaves
    __pycache__ behind. A pattern the planner predicted at G1 must not
    reopen the gate for it."""
    init_git_repo(expected_byproducts=["**/__pycache__/**"])
    pathlib.Path("README.md").write_text("updated", encoding="utf-8")
    pathlib.Path("__pycache__").mkdir()
    pathlib.Path("__pycache__/mod.cpython-312.pyc").write_text("bytecode", encoding="utf-8")

    assert irg._mechanical_deviation({"README.md"}) is None


def test_mechanical_deviation_still_flags_unpredicted_files():
    """A predicted pattern only exempts what it matches -- an unrelated
    unplanned file must still be caught."""
    init_git_repo(expected_byproducts=["**/__pycache__/**"])
    pathlib.Path("README.md").write_text("updated", encoding="utf-8")
    pathlib.Path("secrets.txt").write_text("oops", encoding="utf-8")

    reason = irg._mechanical_deviation({"README.md"})

    assert reason is not None
    assert "secrets.txt" in reason


def test_step_with_predicted_byproduct_lands_without_gate_reopen():
    """End-to-end: a step whose only "extra" change matches a predicted
    byproduct pattern commits cleanly, no G1 reopen at all -- unlike the
    approved-deviation path, which still requires one human round-trip."""
    init_git_repo(expected_byproducts=["**/__pycache__/**"])
    pathlib.Path("README.md").write_text("updated", encoding="utf-8")
    pathlib.Path("__pycache__").mkdir()
    pathlib.Path("__pycache__/mod.cpython-312.pyc").write_text("bytecode", encoding="utf-8")
    mark_step_done(["README.md"])
    resolve_other_perspectives_as_clean(skip="p00")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert irg._completed_step_count() == 1
    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"id": "p00", "kind": "review", "attempt": 1}]


def test_render_plan_text_includes_expected_byproducts_section():
    init_git_repo(expected_byproducts=["**/__pycache__/**"])
    content = irg._render_plan_text()
    assert "生成される可能性のある副産物ファイル" in content
    assert "**/__pycache__/**" in content


def test_render_plan_text_omits_expected_byproducts_section_when_empty():
    init_git_repo()
    content = irg._render_plan_text()
    assert "生成される可能性のある副産物ファイル" not in content


# --- _read_base_ref / _compute_diff / _compute_step_diff -------------------

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
    (irg.REVIEW_RESULTS_DIR / "result_p00_attempt1.json").write_text("{}", encoding="utf-8")

    diff = irg._compute_diff()

    assert "a real change reviewers should see" in diff
    assert "implementation_result.json" not in diff
    assert ".masuda-base-ref" not in diff
    assert "result_p00_attempt1.json" not in diff


def test_compute_step_diff_diffs_against_head_not_base_ref():
    """ADR-0027: once a prior step is already committed, the step diff must
    show only the new, not-yet-committed change -- not the cumulative diff
    since the branch's base ref, which _compute_diff() still reports."""
    init_git_repo()
    pathlib.Path("already-landed.txt").write_text("committed in a prior step", encoding="utf-8")
    subprocess.run(["git", "add", "already-landed.txt"], check=True)
    subprocess.run(["git", "commit", "-q", "-m", "step 1 landed"], check=True)

    pathlib.Path("README.md").write_text("this step's change", encoding="utf-8")
    diff = irg._compute_step_diff()

    assert "this step's change" in diff
    assert "already-landed.txt" not in diff


# --- commit scoping (ADR-0027) ---------------------------------------------

def test_commit_scoped_stages_only_declared_files():
    """The core fix ADR-0027 makes over blind `git add -A`: a build/test
    byproduct left in the working tree (e.g. `merged.yaml`-style codegen
    output) must not ride along in the step's commit just because it exists
    on disk."""
    init_git_repo()
    pathlib.Path("README.md").write_text("intentional change", encoding="utf-8")
    pathlib.Path("byproduct.txt").write_text("accidental codegen output", encoding="utf-8")
    irg.STEP_COMMIT_MESSAGE_FILE.write_text("update readme", encoding="utf-8")

    irg._commit_scoped(["README.md"], irg.STEP_COMMIT_MESSAGE_FILE)

    committed = subprocess.run(
        ["git", "show", "--stat", "--format=", "HEAD"], capture_output=True, text=True, check=True
    ).stdout
    assert "README.md" in committed
    assert "byproduct.txt" not in committed
    # left uncommitted in the working tree -- a known limitation (ADR-0027),
    # not silently discarded.
    assert "byproduct.txt" in irg._actual_changed_files()
    assert not irg.STEP_COMMIT_MESSAGE_FILE.exists()


def test_commit_scoped_survives_a_prior_add_dash_a_staging_everything():
    """_compute_step_diff() (used by trigger_match/interim review earlier in
    the same round) stages everything via `git add -A` for diffing purposes;
    _commit_scoped must not let that leftover staged byproduct ride into the
    commit just because it's already in the index."""
    init_git_repo()
    pathlib.Path("README.md").write_text("intentional change", encoding="utf-8")
    pathlib.Path("byproduct.txt").write_text("accidental codegen output", encoding="utf-8")
    irg._compute_step_diff()  # stages everything, including byproduct.txt
    irg.STEP_COMMIT_MESSAGE_FILE.write_text("update readme", encoding="utf-8")

    irg._commit_scoped(["README.md"], irg.STEP_COMMIT_MESSAGE_FILE)

    committed = subprocess.run(
        ["git", "show", "--stat", "--format=", "HEAD"], capture_output=True, text=True, check=True
    ).stdout
    assert "byproduct.txt" not in committed


# --- detect_phase: phase 4 step progression (ADR-0027) ---------------------

def test_no_result_means_implement_step():
    init_git_repo()
    assert irg.detect_phase({"phase": "", "reason": ""})["phase"] == "implement_step"


def test_step_done_with_no_deviation_and_no_triggers_lands_and_enters_review():
    """No perspective in the test fixture declares a `trigger` by default, so
    a clean step commits immediately and falls straight through to phase 5."""
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip="p00")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"id": "p00", "kind": "review", "attempt": 1}]
    assert irg._completed_step_count() == 1
    assert not irg.IMPLEMENTATION_RESULT_JSON.exists()


def test_second_step_only_reviews_its_own_diff_after_first_lands():
    init_git_repo(steps=TWO_STEP_PLAN)
    pathlib.Path("README.md").write_text("step 1 change", encoding="utf-8")
    mark_step_done(["README.md"], "step 1")

    state = irg.detect_phase({"phase": "", "reason": ""})  # lands step 1, moves on to step 2

    assert irg._completed_step_count() == 1
    assert state["phase"] == "implement_step"

    pathlib.Path("cmd/masuda").mkdir(parents=True)
    pathlib.Path("cmd/masuda/main.go").write_text("step 2 change", encoding="utf-8")
    mark_step_done(["cmd/masuda/main.go"], "step 2")
    resolve_other_perspectives_as_clean(skip="p00")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert irg._completed_step_count() == 2
    assert state["phase"] == "review_batch"  # both steps landed -> phase 5 starts


def test_step_deviation_outside_this_steps_files_reopens_plan():
    """ADR-0027: the backstop compares against *this step's* declared files,
    not the whole plan's -- touching a file that belongs to a later step is
    still a deviation."""
    init_git_repo(steps=TWO_STEP_PLAN)
    pathlib.Path("cmd/masuda").mkdir(parents=True)
    pathlib.Path("cmd/masuda/main.go").write_text("touched a later step's file early", encoding="utf-8")
    mark_step_done(["cmd/masuda/main.go"])

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "plan_reopened"
    assert "cmd/masuda/main.go" in state["reason"]


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


# --- detect_phase: trigger_match / interim review (ADR-0027) ---------------

def test_no_triggered_perspectives_by_default():
    """Already exercised by
    test_step_done_with_no_deviation_and_no_triggers_lands_and_enters_review
    above, but pinned down explicitly: the default test fixture seeds no
    perspective with a `trigger`, mirroring how most of masuda's real
    built-ins may never opt into interim review at all."""
    assert irg.TRIGGERED_PERSPECTIVE_IDS == []


def test_disabled_perspective_excluded_from_perspective_ids():
    """ADR-0033: enable: false keeps the file on disk but drops it from
    PERSPECTIVE_IDS/TOTAL_PERSPECTIVES, same as if it were absent -- this is
    what lets `masuda update`'s add-only sync tell "not yet introduced"
    apart from "deliberately turned off"."""
    worktree_dir = pathlib.Path.cwd()
    _write_test_perspectives(worktree_dir / ".masuda" / "reviews", disabled_ids={"p00"})
    importlib.reload(irg)

    assert "p00" not in irg.PERSPECTIVE_IDS
    assert irg.TOTAL_PERSPECTIVES == TEST_PERSPECTIVE_COUNT - 1


def test_all_perspectives_disabled_raises_on_review_phase_detection():
    """Every perspective disabled behaves like an empty/missing reviews dir
    (ADR-0024's original FileNotFoundError), not like "nothing to review"."""
    worktree_dir = pathlib.Path.cwd()
    _write_test_perspectives(worktree_dir / ".masuda" / "reviews", disabled_ids=set(TEST_PERSPECTIVE_IDS))
    importlib.reload(irg)

    mark_implementation_done_and_clean()
    with pytest.raises(FileNotFoundError, match="enable: false"):
        irg.detect_phase({"phase": "", "reason": ""})


def test_trigger_match_phase_requested_when_a_perspective_declares_trigger(tmp_path, monkeypatch):
    worktree_dir = pathlib.Path.cwd()
    _write_test_perspectives(worktree_dir / ".masuda" / "reviews", triggered_ids={"p00"})
    importlib.reload(irg)

    mark_implementation_done_and_clean()
    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "trigger_match"
    assert json.loads(state["reason"]) == {"step": 0}


def test_trigger_match_task_lists_only_triggered_perspectives(tmp_path, monkeypatch):
    worktree_dir = pathlib.Path.cwd()
    _write_test_perspectives(worktree_dir / ".masuda" / "reviews", triggered_ids={"p00"})
    importlib.reload(irg)
    mark_implementation_done_and_clean()

    irg.write_task_md({"phase": "trigger_match", "reason": json.dumps({"step": 0})})

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert 'id: "p00"' in content
    assert 'id: "p01"' not in content


def test_trigger_match_result_with_no_matches_lands_the_step(tmp_path, monkeypatch):
    worktree_dir = pathlib.Path.cwd()
    _write_test_perspectives(worktree_dir / ".masuda" / "reviews", triggered_ids={"p00"})
    importlib.reload(irg)
    mark_implementation_done_and_clean()
    irg._interim_step_dir(0).mkdir(parents=True)
    irg._trigger_match_path(0).write_text("[]", encoding="utf-8")
    write_all_perspectives_clean()  # nothing under test needs a phase 5 batch

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert irg._completed_step_count() == 1
    assert state["phase"] in ("review_batch", "cross_cutting_explore")


def test_trigger_match_result_with_a_match_starts_interim_review(tmp_path, monkeypatch):
    worktree_dir = pathlib.Path.cwd()
    _write_test_perspectives(worktree_dir / ".masuda" / "reviews", triggered_ids={"p00"})
    importlib.reload(irg)
    mark_implementation_done_and_clean()
    irg._interim_step_dir(0).mkdir(parents=True)
    irg._trigger_match_path(0).write_text(json.dumps(["p00"]), encoding="utf-8")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "interim_review_batch"
    payload = json.loads(state["reason"])
    assert payload["step"] == 0
    assert payload["tasks"] == [{"id": "p00", "kind": "review", "attempt": 1}]
    # the step must not have landed yet -- interim review still pending
    assert irg._completed_step_count() == 0


def test_interim_review_clean_lands_the_step():
    worktree_dir = pathlib.Path.cwd()
    _write_test_perspectives(worktree_dir / ".masuda" / "reviews", triggered_ids={"p00"})
    importlib.reload(irg)
    mark_implementation_done_and_clean()
    irg._interim_step_dir(0).mkdir(parents=True)
    irg._trigger_match_path(0).write_text(json.dumps(["p00"]), encoding="utf-8")
    write_result("p00", 1, has_issues=False, results_dir=irg._interim_step_dir(0))
    write_check("p00", 1, ok=True, results_dir=irg._interim_step_dir(0))

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert irg._completed_step_count() == 1
    assert not irg.IMPLEMENTATION_RESULT_JSON.exists()


def test_interim_review_confirmed_issue_enters_fix_loop():
    worktree_dir = pathlib.Path.cwd()
    _write_test_perspectives(worktree_dir / ".masuda" / "reviews", triggered_ids={"p00"})
    importlib.reload(irg)
    mark_implementation_done_and_clean()
    irg._interim_step_dir(0).mkdir(parents=True)
    irg._trigger_match_path(0).write_text(json.dumps(["p00"]), encoding="utf-8")
    write_result("p00", 1, has_issues=True, results_dir=irg._interim_step_dir(0))
    write_check("p00", 1, ok=True, results_dir=irg._interim_step_dir(0))

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "interim_review_batch"
    payload = json.loads(state["reason"])
    assert payload["tasks"] == [{"id": "p00", "kind": "fix", "attempt": 1, "fix_attempt": 1}]
    assert irg._completed_step_count() == 0


def test_interim_review_unresolved_reopens_plan_gate():
    worktree_dir = pathlib.Path.cwd()
    _write_test_perspectives(worktree_dir / ".masuda" / "reviews", triggered_ids={"p00"})
    importlib.reload(irg)
    mark_implementation_done_and_clean()
    step_dir = irg._interim_step_dir(0)
    step_dir.mkdir(parents=True)
    irg._trigger_match_path(0).write_text(json.dumps(["p00"]), encoding="utf-8")
    for attempt in (1, 2, 3):
        write_result("p00", attempt, has_issues=False, results_dir=step_dir)
        write_check("p00", attempt, ok=False, feedback=f"ng{attempt}", results_dir=step_dir)

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "plan_reopened"
    assert "p00" in irg.PERSPECTIVES["p00"]["name"] or irg.PERSPECTIVES["p00"]["name"] in state["reason"]
    assert irg._completed_step_count() == 0


def test_interim_review_unresolved_approved_carries_finding_and_lands_step():
    worktree_dir = pathlib.Path.cwd()
    _write_test_perspectives(worktree_dir / ".masuda" / "reviews", triggered_ids={"p00"})
    importlib.reload(irg)
    mark_implementation_done_and_clean()
    step_dir = irg._interim_step_dir(0)
    step_dir.mkdir(parents=True)
    irg._trigger_match_path(0).write_text(json.dumps(["p00"]), encoding="utf-8")
    for attempt in (1, 2, 3):
        write_result("p00", attempt, has_issues=False, results_dir=step_dir)
        write_check("p00", attempt, ok=False, feedback=f"ng{attempt}", results_dir=step_dir)
    # DEVIATION.md is only written by write_task_md's "plan_reopened" branch,
    # not by detect_phase itself (same split ADR-0010 established) -- write
    # it directly here to simulate "the gate was already opened in a prior
    # round," same pattern the mechanical-deviation tests above use.
    state_client.put(irg.DEVIATION_KEY, "既存の理由")
    state_client.put(irg.PLAN_GATE_KEY, json.dumps({"status": "approved"}))
    write_all_perspectives_clean()

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert irg._completed_step_count() == 1, "approval lets the step land as-is"
    carried = json.loads(state_client.get(irg.INTERIM_CARRIED_FINDINGS_KEY))
    assert carried == [{"step": 0, "id": "p00", "reason": "review_check_not_converged"}]
    assert state["phase"] in ("review_batch", "cross_cutting_explore")


def test_interim_review_unresolved_rejected_redoes_step():
    worktree_dir = pathlib.Path.cwd()
    _write_test_perspectives(worktree_dir / ".masuda" / "reviews", triggered_ids={"p00"})
    importlib.reload(irg)
    mark_implementation_done_and_clean()
    step_dir = irg._interim_step_dir(0)
    step_dir.mkdir(parents=True)
    irg._trigger_match_path(0).write_text(json.dumps(["p00"]), encoding="utf-8")
    for attempt in (1, 2, 3):
        write_result("p00", attempt, has_issues=False, results_dir=step_dir)
        write_check("p00", attempt, ok=False, feedback=f"ng{attempt}", results_dir=step_dir)
    # DEVIATION.md is only written by write_task_md's "plan_reopened" branch,
    # not by detect_phase itself (same split ADR-0010 established) -- write
    # it directly here to simulate "the gate was already opened in a prior
    # round," same pattern the mechanical-deviation tests above use.
    state_client.put(irg.DEVIATION_KEY, "既存の理由")
    state_client.put(irg.PLAN_GATE_KEY, json.dumps({"status": "rejected", "feedback": "直して"}))

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "implement_step"
    assert "直して" in state["reason"]
    assert irg._completed_step_count() == 0
    assert not irg.IMPLEMENTATION_RESULT_JSON.exists()
    assert not step_dir.exists(), "interim review state must be wiped so the redo starts fresh"


# --- detect_phase: G2 rejection -> implement_g2_redo (ADR-0013/0027) -------

def test_final_report_rejected_reopens_as_g2_redo_not_step_flow():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip="p00")
    irg.detect_phase({"phase": "", "reason": ""})  # lands the step, enters phase 5
    write_result("p00", 1)
    write_check("p00", 1, ok=True)
    irg.detect_phase({"phase": "", "reason": ""})  # settles p00 clean

    irg.FINAL_REPORT_MD.parent.mkdir(exist_ok=True)
    irg.FINAL_REPORT_MD.write_text("# report", encoding="utf-8")
    irg.COMMIT_MESSAGE_FILE.write_text("commit message", encoding="utf-8")
    state_client.put(irg.REVIEW_GATE_KEY, json.dumps({"status": "rejected", "feedback": "セキュリティ観点を見直して"}))

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "implement_g2_redo"
    assert "セキュリティ観点を見直して" in state["reason"]
    assert not state_client.exists(irg.REVIEW_GATE_KEY), "rejection must be consumed"
    assert not irg.IMPLEMENTATION_RESULT_JSON.exists(), "must force a fresh implementation round"
    assert not state_client.exists(irg.REVIEW_STATE_KEY), "review must restart from perspective 0 (ADR-0013)"
    assert not irg.REVIEW_RESULTS_DIR.exists(), "stale review results must not be reused (ADR-0013)"
    assert not irg.COMMIT_MESSAGE_FILE.exists(), "stale commit message must not be reused for the redo's changes"


def test_g2_redo_detected_on_fresh_process_via_feedback_file():
    """detect_phase is re-invoked as a fresh process each loop iteration --
    the reopened-implementation instruction must survive that, not just live
    in the in-memory state dict."""
    state_client.put(irg.REVIEW_FEEDBACK_KEY, "直して")
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "implement_g2_redo"
    assert state["reason"] == "直して"


def test_g2_redo_backstop_uses_whole_plan_union_not_a_single_step():
    init_git_repo(steps=TWO_STEP_PLAN)
    state_client.put(irg.REVIEW_FEEDBACK_KEY, "直して")
    # cmd/masuda/main.go belongs to step 2, not step 1 -- but implement_g2_redo
    # isn't decomposed into steps, so touching it must be fine.
    pathlib.Path("cmd/masuda").mkdir(parents=True)
    pathlib.Path("cmd/masuda/main.go").write_text("g2 redo touches step 2's file", encoding="utf-8")
    mark_step_done(["cmd/masuda/main.go"], "g2 redo")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] != "plan_reopened"
    assert not state_client.exists(irg.REVIEW_FEEDBACK_KEY)


def test_g2_redo_backstop_still_flags_a_file_outside_the_whole_plan():
    init_git_repo(steps=TWO_STEP_PLAN)
    state_client.put(irg.REVIEW_FEEDBACK_KEY, "直して")
    pathlib.Path("totally-unplanned.txt").write_text("oops", encoding="utf-8")
    mark_step_done(["totally-unplanned.txt"], "g2 redo")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "plan_reopened"
    assert "totally-unplanned.txt" in state["reason"]


# --- detect_phase: phase 5 (review) ---------------------------------------

def test_review_advances_to_check_once_result_written():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip="p00")
    write_result("p00", 1)
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"id": "p00", "kind": "check", "attempt": 1}]


def test_review_ok_no_issues_dropped_from_next_batch():
    """A clean perspective (no issues, check passed) drops out of future
    rounds' batches -- ADR-0021 replaces the old single-idx "advance to next
    perspective" cursor with "recompute the remaining set every round"."""
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip="p01")  # marks p00 (and p02..p13) clean, leaves p01 pending
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"id": "p01", "kind": "review", "attempt": 1}]


def test_review_failed_check_redoes_same_perspective():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip="p00")
    write_result("p00", 1)
    write_check("p00", 1, ok=False, feedback="見落としがある")
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"id": "p00", "kind": "review", "attempt": 2}]


def test_review_exhausted_retries_marks_unresolved():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip="p00")
    # attempt 1 and 2 both fail -> MAX_REVIEW_RETRIES (2) reached -> perspective p00 unresolved
    write_result("p00", 1)
    write_check("p00", 1, ok=False, feedback="ng1")
    write_result("p00", 2)
    write_check("p00", 2, ok=False, feedback="ng2")
    write_result("p00", 3)
    write_check("p00", 3, ok=False, feedback="ng3")

    state = irg.detect_phase({"phase": "", "reason": ""})

    # perspective p00 is now the only one left unresolved -- nothing left to
    # batch, so review moves on to cross-cutting.
    assert state["phase"] == "cross_cutting_explore"
    rs = irg._read_review_state()
    assert rs["unresolved"] == [{"id": "p00", "reason": "review_check_not_converged"}]


def test_multiple_pending_perspectives_at_different_stages_batch_together():
    """ADR-0021's actual point: independent perspectives sitting at different
    stages (one fresh, one awaiting check, one confirmed-issue awaiting fix)
    all land in the same round's batch instead of being processed one at a
    time."""
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip={"p00", "p01", "p02"})
    # p00: needs its first review (nothing written)
    write_result("p01", 1)  # p01: needs check
    write_result("p02", 1, has_issues=True)
    write_check("p02", 1, ok=True)  # p02: confirmed issue, needs fix

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "review_batch"
    tasks = json.loads(state["reason"])["tasks"]
    assert {t["id"]: t["kind"] for t in tasks} == {"p00": "review", "p01": "check", "p02": "fix"}


def test_all_perspectives_done_means_cross_cutting_explore():
    """Once all mechanical perspectives converge, the cross-cutting
    explorer/verifier pass (ADR-0003/ADR-0011) runs before synthesize."""
    mark_implementation_done_and_clean()
    for pid in irg.PERSPECTIVE_IDS:
        write_result(pid, 1, has_issues=False)
        write_check(pid, 1, ok=True)
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


def test_synthesize_includes_interim_carried_section_when_present():
    init_git_repo()
    write_all_perspectives_clean()
    state_client.put(irg.INTERIM_CARRIED_FINDINGS_KEY, json.dumps([{"step": 0, "id": "p00", "reason": "review_check_not_converged"}]))

    irg.write_task_md({"phase": "synthesize", "reason": ""})

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "途中レビューで持ち越された指摘" in content
    assert irg.PERSPECTIVES["p00"]["name"] in content


def test_synthesize_omits_interim_carried_section_when_absent():
    init_git_repo()
    write_all_perspectives_clean()

    irg.write_task_md({"phase": "synthesize", "reason": ""})

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "途中レビューで持ち越された指摘なし" in content


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
    state_client.put(irg.REVIEW_GATE_KEY, json.dumps({"status": "rejected", "feedback": "却下"}))

    irg.detect_phase({"phase": "", "reason": ""})

    assert not irg.CROSS_CUTTING_FINDINGS_JSON.exists()
    assert not irg.CROSS_CUTTING_VERIFIED_JSON.exists()


# --- detect_phase: phase 5 fix<->recheck loop (ADR-0004) ------------------

def test_confirmed_issue_enters_fix_loop():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip="p00")
    write_result("p00", 1, has_issues=True)
    write_check("p00", 1, ok=True)

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"id": "p00", "kind": "fix", "attempt": 1, "fix_attempt": 1}]


def test_fix_written_advances_to_recheck():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip="p00")
    write_result("p00", 1, has_issues=True)
    write_check("p00", 1, ok=True)
    write_fix("p00", 1)

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"id": "p00", "kind": "recheck", "attempt": 1, "fix_attempt": 1}]


def test_recheck_resolved_dropped_from_next_batch_and_records_fixed():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip={"p00", "p01"})  # leave p01 pending; p00 is driven through fix/recheck below, not "clean"
    write_result("p00", 1, has_issues=True)
    write_check("p00", 1, ok=True)
    write_fix("p00", 1)
    write_recheck("p00", 1, resolved=True)

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"id": "p01", "kind": "review", "attempt": 1}]
    assert irg._read_review_state()["fixed"] == ["p00"]


def test_recheck_unresolved_redoes_fix():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip="p00")
    write_result("p00", 1, has_issues=True)
    write_check("p00", 1, ok=True)
    write_fix("p00", 1)
    write_recheck("p00", 1, resolved=False, feedback="まだ直っていない")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"id": "p00", "kind": "fix", "attempt": 1, "fix_attempt": 2}]


def test_fix_exhausted_retries_marks_unresolved():
    mark_implementation_done_and_clean()
    resolve_other_perspectives_as_clean(skip="p00")
    write_result("p00", 1, has_issues=True)
    write_check("p00", 1, ok=True)
    for fix_attempt in (1, 2, 3):
        write_fix("p00", fix_attempt)
        write_recheck("p00", fix_attempt, resolved=False, feedback=f"ng{fix_attempt}")

    state = irg.detect_phase({"phase": "", "reason": ""})

    # perspective p00 is now the only one left unresolved -- nothing left to
    # batch, so review moves on to cross-cutting.
    assert state["phase"] == "cross_cutting_explore"
    rs = irg._read_review_state()
    assert rs["unresolved"] == [{"id": "p00", "reason": "fix_not_resolved"}]
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
    state_client.put(irg.REVIEW_GATE_KEY, json.dumps({"status": "approved"}))

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "g2_approved"


# --- write_task_md -----------------------------------------------------------

def test_implement_step_task_includes_plan_and_step_content():
    init_git_repo()
    irg.write_task_md({"phase": "implement_step", "reason": ""})
    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "変更するファイル一覧" in content
    assert "README.mdを直す" in content
    assert "DONE" not in content


def test_implement_step_redo_includes_feedback():
    init_git_repo()
    irg.write_task_md({"phase": "implement_step", "reason": "セキュリティ観点を見直して"})
    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "セキュリティ観点を見直して" in content


def test_implement_g2_redo_task_includes_feedback_and_plan():
    init_git_repo()
    irg.write_task_md({"phase": "implement_g2_redo", "reason": "セキュリティ観点を見直して"})
    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "セキュリティ観点を見直して" in content
    assert "変更するファイル一覧" in content


def test_implement_step_missing_plan_raises():
    subprocess.run(["git", "init", "-q"], check=True)
    subprocess.run(["git", "config", "user.email", "test@example.com"], check=True)
    subprocess.run(["git", "config", "user.name", "test"], check=True)
    pathlib.Path("README.md").write_text("baseline", encoding="utf-8")
    subprocess.run(["git", "add", "-A"], check=True)
    subprocess.run(["git", "commit", "-q", "-m", "init"], check=True)
    with pytest.raises(FileNotFoundError):
        irg.write_task_md({"phase": "implement_step", "reason": ""})


def test_plan_reopened_writes_deviation_as_a_gate_not_a_terminal_done():
    """write_task_md's job here is only to open the gate (write DEVIATION.md,
    render the GATE:plan message) -- it must NOT also clear the gate marker or
    implementation_result.json anymore. That clearing now happens in
    detect_phase/_resolve_gate_reopen, only once the gate is actually
    resolved; doing it eagerly here was the bug that made an *approved*
    deviation reopen the gate forever once the loop could auto-resume
    (GATE:plan, roadmap step 5) instead of a human always restarting by hand.
    """
    state_client.put(irg.PLAN_GATE_KEY, json.dumps({"status": "approved"}))
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")

    irg.write_task_md({"phase": "plan_reopened", "reason": "計画外のファイル変更"})

    assert state_client.get(irg.DEVIATION_KEY) == "計画外のファイル変更"
    assert state_client.exists(irg.PLAN_GATE_KEY)
    assert irg.IMPLEMENTATION_RESULT_JSON.exists()
    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "GATE:plan" in content
    assert "DONE" not in content
    assert "計画外のファイル変更" in content


# --- detect_phase: resolving a reopened G1 (ADR-0010, ADR-0013, ADR-0027) --

def test_mechanical_deviation_first_detection_opens_gate_without_clearing():
    init_git_repo()
    pathlib.Path("unplanned.txt").write_text("oops", encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "plan_reopened"
    assert not state_client.exists(irg.DEVIATION_KEY), "DEVIATION.md is written by write_task_md, not detect_phase"


def test_mechanical_deviation_first_detection_clears_stale_gate_marker():
    """Confirmed on a live run: the original G1 approval's marker is never
    unlinked on the "approved" path (investigate_plan_graph.py's
    detect_phase), so it's still sitting on disk, "approved", the first time
    a later mechanical deviation reopens the gate. Left alone, the GATE:plan
    wait condition ("not pending") would already be satisfied before a human
    has looked at *this* deviation -- stale history silently standing in for
    today's answer."""
    init_git_repo()
    pathlib.Path("unplanned.txt").write_text("oops", encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")
    state_client.put(irg.PLAN_GATE_KEY, json.dumps({"status": "approved"}))

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "plan_reopened"
    assert not state_client.exists(irg.PLAN_GATE_KEY)


def test_mechanical_deviation_still_pending_reflects_same_reason():
    init_git_repo()
    pathlib.Path("unplanned.txt").write_text("oops", encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")
    state_client.put(irg.DEVIATION_KEY, "既存の理由")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "plan_reopened"
    assert state["reason"] == "既存の理由"


def test_mechanical_deviation_approved_is_recorded_and_step_lands():
    """Unlike a self-reported deviation, the agent already finished acting --
    approval means "accept the extra file as-is" and continue forward. The
    step's own declared changed_files still land normally; the approved
    extra file (not part of any step's self-report) stays uncommitted, a
    known limitation noted in ADR-0027."""
    init_git_repo()
    pathlib.Path("README.md").write_text("updated", encoding="utf-8")
    pathlib.Path("unplanned.txt").write_text("oops", encoding="utf-8")
    mark_step_done(["README.md"])
    state_client.put(irg.DEVIATION_KEY, "既存の理由")
    state_client.put(irg.PLAN_GATE_KEY, json.dumps({"status": "approved"}))
    resolve_other_perspectives_as_clean(skip="p00")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert not state_client.exists(irg.DEVIATION_KEY)
    assert not state_client.exists(irg.PLAN_GATE_KEY)
    assert "unplanned.txt" in irg._read_approved_deviations()
    assert irg._completed_step_count() == 1, "approval must not force a redo -- the step lands"
    assert state["phase"] == "review_batch"
    assert json.loads(state["reason"])["tasks"] == [{"id": "p00", "kind": "review", "attempt": 1}]


def test_mechanical_deviation_approved_does_not_reflag_on_next_check():
    """The bug this whole mechanism exists to fix: without recording the
    approval, the very next mechanical check would see the same extra file
    and reopen the gate forever."""
    init_git_repo()
    pathlib.Path("README.md").write_text("updated", encoding="utf-8")
    pathlib.Path("unplanned.txt").write_text("oops", encoding="utf-8")
    mark_step_done(["README.md"])
    state_client.put(irg.DEVIATION_KEY, "既存の理由")
    state_client.put(irg.PLAN_GATE_KEY, json.dumps({"status": "approved"}))

    irg.detect_phase({"phase": "", "reason": ""})  # resolves the approval, lands the step

    assert irg._mechanical_deviation({"README.md"}) is None


def test_mechanical_deviation_rejected_forces_fresh_implementation():
    init_git_repo()
    pathlib.Path("unplanned.txt").write_text("oops", encoding="utf-8")
    irg.IMPLEMENTATION_RESULT_JSON.write_text(json.dumps({"status": "done"}), encoding="utf-8")
    state_client.put(irg.DEVIATION_KEY, "既存の理由")
    state_client.put(irg.PLAN_GATE_KEY, json.dumps({"status": "rejected", "feedback": "計画通りにして"}))

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert not state_client.exists(irg.DEVIATION_KEY)
    assert not state_client.exists(irg.PLAN_GATE_KEY)
    assert not irg.IMPLEMENTATION_RESULT_JSON.exists()
    assert irg._read_approved_deviations() == set()
    assert state["phase"] == "implement_step"
    assert "計画通りにして" in state["reason"]


def test_self_reported_deviation_approved_still_needs_a_redo_turn():
    """Unlike the mechanical case, the subagent stopped *before* acting
    (that's the point of the self-report) -- approval means "go ahead and do
    what you proposed", which still requires another implementation turn,
    not a direct pass into review."""
    irg.IMPLEMENTATION_RESULT_JSON.write_text(
        json.dumps({"status": "needs_plan_review", "reason": "設計を変えたい"}), encoding="utf-8"
    )
    state_client.put(irg.DEVIATION_KEY, "既存の理由")
    state_client.put(irg.PLAN_GATE_KEY, json.dumps({"status": "approved"}))

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "implement_step"
    assert not irg.IMPLEMENTATION_RESULT_JSON.exists()


def test_review_perspective_task_includes_diff_and_perspective_prompt():
    init_git_repo()
    pathlib.Path("README.md").write_text("updated content", encoding="utf-8")

    irg.write_task_md(batch_state({"id": "p00", "kind": "review", "attempt": 1}))

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "updated content" in content
    assert irg.PERSPECTIVES["p00"]["review_prompt"][:20] in content
    assert "DONE" not in content


def test_review_perspective_task_includes_prior_feedback_on_redo():
    init_git_repo()
    write_result("p00", 1)
    write_check("p00", 1, ok=False, feedback="前回の見落とし")

    irg.write_task_md(batch_state({"id": "p00", "kind": "review", "attempt": 2}))

    assert "前回の見落とし" in irg.TASK_MD.read_text(encoding="utf-8")


def test_check_perspective_task_includes_review_result():
    init_git_repo()
    write_result("p00", 1, has_issues=True)

    irg.write_task_md(batch_state({"id": "p00", "kind": "check", "attempt": 1}))

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "has_issues" in content
    assert irg.PERSPECTIVES["p00"]["checker_prompt"][:20] in content


def test_fix_perspective_task_includes_flagged_issue():
    init_git_repo()
    write_result("p00", 1, has_issues=True)

    irg.write_task_md(batch_state({"id": "p00", "kind": "fix", "attempt": 1, "fix_attempt": 1}))

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "has_issues" in content
    assert "fix_p00_fixattempt1.json" in content


def test_fix_perspective_task_includes_prior_recheck_feedback_on_retry():
    init_git_repo()
    write_result("p00", 1, has_issues=True)
    write_recheck("p00", 1, resolved=False, feedback="まだ直っていない")

    irg.write_task_md(batch_state({"id": "p00", "kind": "fix", "attempt": 1, "fix_attempt": 2}))

    assert "まだ直っていない" in irg.TASK_MD.read_text(encoding="utf-8")


def test_recheck_perspective_task_includes_diff_and_checker_prompt():
    init_git_repo()
    pathlib.Path("README.md").write_text("fixed content", encoding="utf-8")

    irg.write_task_md(batch_state({"id": "p00", "kind": "recheck", "fix_attempt": 1}))

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "fixed content" in content


def test_review_batch_task_delegates_multiple_independent_tasks_in_parallel():
    """ADR-0021: a round with several independent perspectives at different
    stages renders all of them into one TASK.md instructing the main
    session to delegate every one as a separate, parallel Task tool call."""
    init_git_repo()
    write_result("p01", 1, has_issues=True)

    irg.write_task_md(
        batch_state(
            {"id": "p00", "kind": "review", "attempt": 1},
            {"id": "p01", "kind": "check", "attempt": 1},
        )
    )

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "並列に" in content
    assert irg.PERSPECTIVES["p00"]["review_prompt"][:20] in content
    assert irg.PERSPECTIVES["p01"]["checker_prompt"][:20] in content
    assert str(irg._result_path(irg.REVIEW_RESULTS_DIR, "p00", 1)) in content
    assert str(irg._check_path(irg.REVIEW_RESULTS_DIR, "p01", 1)) in content


def test_interim_review_batch_task_uses_step_diff_and_results_dir():
    init_git_repo(steps=TWO_STEP_PLAN)
    pathlib.Path("README.md").write_text("this step's change", encoding="utf-8")

    irg.write_task_md(
        {
            "phase": "interim_review_batch",
            "reason": json.dumps({"step": 0, "tasks": [{"id": "p00", "kind": "review", "attempt": 1}]}),
        }
    )

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "this step's change" in content
    assert str(irg._interim_step_dir(0)) in content


def test_review_batch_iteration_budget_counts_every_task_in_the_batch():
    """ADR-0021/ADR-0011: the budget still counts one unit per subagent
    invocation, so a batch of N tasks must advance the counter by N, not by
    1 per write_task_md call."""
    init_git_repo()

    irg.write_task_md(
        batch_state(*({"id": pid, "kind": "review", "attempt": 1} for pid in irg.PERSPECTIVE_IDS[:5]))
    )

    assert irg._read_iteration_count() == 5


def test_synthesize_task_includes_fixed_and_unresolved_sections():
    init_git_repo()
    irg._write_review_state(
        {
            "redo_counts": {},
            "fix_counts": {},
            "unresolved": [{"id": "p01", "reason": "fix_not_resolved"}],
            "fixed": ["p00"],
            "clean": irg.PERSPECTIVE_IDS[2:],
        }
    )
    for pid in irg.PERSPECTIVE_IDS:
        write_result(pid, 1, has_issues=(pid in ("p00", "p01")))

    irg.write_task_md({"phase": "synthesize", "reason": ""})

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "自動修正済みの指摘" in content
    assert "未解決の指摘" in content
    assert irg.PERSPECTIVES["p00"]["name"] in content
    assert irg.PERSPECTIVES["p01"]["name"] in content


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
    assert "masuda chat" in content
    assert "masuda review approve" in content
    assert "masuda review reject" in content


# --- ITERATION_BUDGET (ADR-0011/ADR-0027) ----------------------------------

def test_write_task_md_increments_iteration_count_for_subagent_phases():
    init_git_repo()
    assert irg._read_iteration_count() == 0
    irg.write_task_md({"phase": "implement_step", "reason": ""})
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


def test_iteration_budget_scales_with_step_count():
    """ADR-0027: the fixed 200 no longer holds once phase 4 walks a
    variable-length list of steps. _iteration_budget() only reads
    plan/steps.json, so no git repo is needed here."""
    write_plan(steps=SAMPLE_STEPS)
    one_step_budget = irg._iteration_budget()

    write_plan(steps=TWO_STEP_PLAN)
    two_step_budget = irg._iteration_budget()

    assert two_step_budget > one_step_budget
    assert two_step_budget - one_step_budget == irg.PER_STEP_BUDGET


def test_iteration_budget_falls_back_to_base_without_a_plan():
    assert irg._iteration_budget() == irg.BASE_BUDGET


def test_iteration_budget_exceeded_overrides_phase():
    """Once the persisted count already exceeds the iteration budget,
    write_task_md must render the blocked message instead of another
    subagent delegation -- a final defense line independent of
    MAX_REVIEW_RETRIES (ADR-0011)."""
    init_git_repo()
    state_client.put(irg.ITERATION_COUNT_KEY, str(irg._iteration_budget()))

    irg.write_task_md({"phase": "implement_step", "reason": ""})

    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "DONE" in content
    assert "ITERATION_BUDGET" in content


def test_full_graph_run_writes_task_md():
    init_git_repo()
    app = irg.build_graph()
    app.invoke({"phase": "", "reason": ""})
    assert irg.TASK_MD.exists()
    assert "実装" in irg.TASK_MD.read_text(encoding="utf-8")


# --- triage gate (ADR-0029) --------------------------------------------------

def write_triage_concern(agent="implementer", phase="implement_step", description="不審な記述を発見した"):
    irg.TRIAGE_CONCERN_JSON.write_text(
        json.dumps({
            "agent": agent, "phase": phase, "description": description,
            "evidence": "", "reported_at": "2026-08-05T00:00:00Z",
        }),
        encoding="utf-8",
    )


def write_triage_marker(status, feedback=""):
    state_client.put(irg.TRIAGE_GATE_KEY, json.dumps({"status": status, "feedback": feedback}))


def test_triage_concern_present_opens_gate():
    init_git_repo()
    write_triage_concern(description="怪しい指示を発見")
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "await_triage"
    assert state["reason"] == "怪しい指示を発見"


def test_triage_gate_pending_marker_stays_await_triage():
    init_git_repo()
    write_triage_concern()
    write_triage_marker("pending")
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "await_triage"


def test_triage_dismissed_resumes_interrupted_phase():
    init_git_repo()  # baseline (no implementation_result.json yet): "implement_step"
    write_triage_concern()
    write_triage_marker("approved", feedback="誤検知でした")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "implement_step"
    assert not irg.TRIAGE_CONCERN_JSON.exists()
    assert not state_client.exists(irg.TRIAGE_GATE_KEY)


def test_triage_redo_leaves_feedback_note_for_next_task_md():
    init_git_repo()
    write_triage_concern()
    write_triage_marker("rejected", feedback="ファイルを修正したので続けてください")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "implement_step"
    assert not irg.TRIAGE_CONCERN_JSON.exists()
    assert not state_client.exists(irg.TRIAGE_GATE_KEY)
    assert state_client.exists(irg.TRIAGE_REDO_FEEDBACK_KEY)
    assert state_client.get(irg.TRIAGE_REDO_FEEDBACK_KEY) == "ファイルを修正したので続けてください"

    irg.write_task_md(state)
    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "ファイルを修正したので続けてください" in content
    assert not state_client.exists(irg.TRIAGE_REDO_FEEDBACK_KEY), "the note must be consumed exactly once"


def test_triage_halted_is_a_terminal_done_not_a_gate():
    init_git_repo()
    write_triage_concern()
    write_triage_marker("halted", feedback="深刻な懸念")

    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "triage_halted"
    assert state["reason"] == "深刻な懸念"

    irg.write_task_md(state)
    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "DONE (triage halted)" in content
    assert "GATE:" not in content


def test_triage_halted_does_not_consume_concern_or_marker():
    """halt must leave everything for post-halt forensics (`masuda triage
    show`) and be idempotent if detect_phase is somehow re-invoked (ADR-0029:
    mirrors gate.Halt's Go-side contract of touching nothing else)."""
    init_git_repo()
    write_triage_concern(description="深刻な懸念の詳細")
    write_triage_marker("halted", feedback="深刻な懸念")

    first = irg.detect_phase({"phase": "", "reason": ""})
    second = irg.detect_phase({"phase": "", "reason": ""})

    assert first["phase"] == second["phase"] == "triage_halted"
    assert irg.TRIAGE_CONCERN_JSON.exists()
    assert state_client.exists(irg.TRIAGE_GATE_KEY)


def _setup_g1_reopen_needs_plan_review():
    irg.IMPLEMENTATION_RESULT_JSON.write_text(
        json.dumps({"status": "needs_plan_review", "reason": "設計を変える必要がある"}), encoding="utf-8"
    )


def _setup_interim_unresolved_reopen():
    worktree_dir = pathlib.Path.cwd()
    _write_test_perspectives(worktree_dir / ".masuda" / "reviews", triggered_ids={"p00"})
    importlib.reload(irg)
    mark_implementation_done_and_clean()
    step_dir = irg._interim_step_dir(0)
    step_dir.mkdir(parents=True)
    irg._trigger_match_path(0).write_text(json.dumps(["p00"]), encoding="utf-8")
    for attempt in (1, 2, 3):
        write_result("p00", attempt, has_issues=False, results_dir=step_dir)
        write_check("p00", attempt, ok=False, feedback=f"ng{attempt}", results_dir=step_dir)


def _setup_await_g2():
    mark_implementation_done_and_clean()
    irg.FINAL_REPORT_MD.parent.mkdir(exist_ok=True)
    irg.FINAL_REPORT_MD.write_text("# report", encoding="utf-8")
    irg.COMMIT_MESSAGE_FILE.write_text("commit message", encoding="utf-8")


def _setup_cross_cutting_explore():
    mark_implementation_done_and_clean()
    for pid in irg.PERSPECTIVE_IDS:
        write_result(pid, 1, has_issues=False)
        write_check(pid, 1, ok=True)


@pytest.mark.parametrize("setup_phase,setup", [
    ("plan_reopened", _setup_g1_reopen_needs_plan_review),
    ("plan_reopened", _setup_interim_unresolved_reopen),
    ("await_g2", _setup_await_g2),
    ("cross_cutting_explore", _setup_cross_cutting_explore),
])
def test_triage_preempts_every_other_phase(setup_phase, setup):
    """Whatever phase would otherwise be detected -- including an in-flight
    G1 reopen -- a pending triage concern must win (ADR-0029: this is the
    most-urgent layer)."""
    setup()
    baseline = irg.detect_phase({"phase": "", "reason": ""})
    assert baseline["phase"] == setup_phase

    write_triage_concern()
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "await_triage"


def test_await_triage_mentions_triage_cli_commands():
    irg.write_task_md({"phase": "await_triage", "reason": "懸念の概要"})
    content = irg.TASK_MD.read_text(encoding="utf-8")
    assert "GATE:triage" in content
    assert "masuda triage dismiss" in content
    assert "masuda triage redo" in content
    assert "masuda triage halt" in content
    assert "DONE" not in content


def test_implement_step_task_includes_triage_self_report_section():
    write_plan()
    content = irg._implement_step_task(0)
    assert str(irg.TRIAGE_CONCERN_JSON) in content


def test_implement_g2_redo_task_includes_triage_self_report_section():
    write_plan()
    content = irg._implement_g2_redo_task("フィードバック")
    assert str(irg.TRIAGE_CONCERN_JSON) in content


def test_review_perspective_task_includes_triage_self_report_section():
    content = irg._review_perspective_task(irg.REVIEW_RESULTS_DIR, "diff", "p00", 1, "label")
    assert str(irg.TRIAGE_CONCERN_JSON) in content


def test_check_perspective_task_includes_triage_self_report_section():
    write_result("p00", 1)
    content = irg._check_perspective_task(irg.REVIEW_RESULTS_DIR, "diff", "p00", 1, "label")
    assert str(irg.TRIAGE_CONCERN_JSON) in content


def test_fix_perspective_task_includes_triage_self_report_section():
    write_result("p00", 1, has_issues=True)
    content = irg._fix_perspective_task(irg.REVIEW_RESULTS_DIR, "p00", 1, 1, "label")
    assert str(irg.TRIAGE_CONCERN_JSON) in content


def test_recheck_perspective_task_includes_triage_self_report_section():
    content = irg._recheck_perspective_task(irg.REVIEW_RESULTS_DIR, "diff", "p00", 1, "label")
    assert str(irg.TRIAGE_CONCERN_JSON) in content


def test_cross_cutting_explore_task_includes_triage_self_report_section():
    init_git_repo()
    content = irg._cross_cutting_explore_task()
    assert str(irg.TRIAGE_CONCERN_JSON) in content


def test_cross_cutting_verify_task_includes_triage_self_report_section():
    init_git_repo()
    write_cross_cutting_findings([])
    content = irg._cross_cutting_verify_task()
    assert str(irg.TRIAGE_CONCERN_JSON) in content


# --- TDD mode (Issue #3): Red/Green/Refactor sub-loop -----------------------

def test_new_tdd_step_starts_at_tdd_red():
    init_git_repo(steps=TDD_STEP_PLAN)
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "tdd_red"


def test_tdd_red_checker_approval_advances_to_green_without_tagging():
    init_git_repo(steps=TDD_STEP_PLAN)
    pathlib.Path("feature_test.go").write_text("failing test", encoding="utf-8")
    mark_tdd_phase_done(["feature_test.go"], "red: add failing test")
    write_tdd_check(0, cycle=1, phase="red", attempt=1, ok=True)

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "tdd_green"
    assert irg._completed_step_count() == 0, "a phase commit is not a step tag"
    log = subprocess.run(["git", "log", "--oneline"], capture_output=True, text=True, check=True).stdout
    assert "red: add failing test" in log
    assert irg._read_tdd_cycle_state(0)["phase"] == "green"


def test_tdd_checker_rejection_redoes_same_phase_with_feedback():
    init_git_repo(steps=TDD_STEP_PLAN)
    pathlib.Path("feature_test.go").write_text("bad test", encoding="utf-8")
    mark_tdd_phase_done(["feature_test.go"], "red: bad test")
    write_tdd_check(0, 1, "red", 1, ok=False, feedback="複数の振る舞いをテストしている")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "tdd_red"
    assert state["reason"] == "複数の振る舞いをテストしている"
    assert not irg.IMPLEMENTATION_RESULT_JSON.exists()
    assert irg._read_tdd_cycle_state(0)["attempt"] == 2
    log = subprocess.run(["git", "log", "--oneline"], capture_output=True, text=True, check=True).stdout
    assert "red: bad test" not in log, "a rejected phase must not be committed"


def test_tdd_checker_rejection_exhausted_reopens_plan():
    init_git_repo(steps=TDD_STEP_PLAN)
    pathlib.Path("feature_test.go").write_text("bad test", encoding="utf-8")
    mark_tdd_phase_done(["feature_test.go"], "red: bad test")
    write_tdd_check(0, 1, "red", 1, ok=False, feedback="1回目却下")
    irg.detect_phase({"phase": "", "reason": ""})  # attempt -> 2, redo tdd_red

    pathlib.Path("feature_test.go").write_text("still bad", encoding="utf-8")
    mark_tdd_phase_done(["feature_test.go"], "red: still bad")
    write_tdd_check(0, 1, "red", 2, ok=False, feedback="2回目も却下")

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "plan_reopened"
    assert "2回目も却下" in state["reason"]


def test_tdd_green_always_advances_to_refactor_regardless_of_self_report():
    """Green never leaves the choice of what's next to self-report -- the
    orchestrator forces every cycle through at least one Refactor turn
    (agents are prone to skipping it if left to choose, docs/adr/00xx)."""
    init_git_repo(steps=TDD_STEP_PLAN)
    pathlib.Path("feature_test.go").write_text("failing test", encoding="utf-8")
    mark_tdd_phase_done(["feature_test.go"], "red")
    write_tdd_check(0, 1, "red", 1, ok=True)
    irg.detect_phase({"phase": "", "reason": ""})

    pathlib.Path("feature.go").write_text("impl", encoding="utf-8")
    # Even a self-report that tries to skip straight to "complete" is
    # ignored -- _advance_tdd_cycle hardcodes green -> refactor.
    mark_tdd_phase_done(["feature.go"], "green", tdd_next_phase="complete")
    write_tdd_check(0, 1, "green", 1, ok=True)

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "tdd_refactor"
    assert irg._completed_step_count() == 0


def test_tdd_refactor_no_changes_skips_commit_and_checker():
    init_git_repo(steps=TDD_STEP_PLAN)
    irg._write_tdd_cycle_state(0, {"cycle": 1, "phase": "refactor", "attempt": 1, "redo_feedback": ""})
    before = subprocess.run(["git", "rev-parse", "HEAD"], capture_output=True, text=True, check=True).stdout
    mark_tdd_phase_done([], tdd_next_phase="red")

    state = irg.detect_phase({"phase": "", "reason": ""})

    after = subprocess.run(["git", "rev-parse", "HEAD"], capture_output=True, text=True, check=True).stdout
    assert before == after, "an honestly-reported no-op refactor must not be committed"
    assert state["phase"] == "tdd_red"
    cs = irg._read_tdd_cycle_state(0)
    assert cs["cycle"] == 2
    assert not irg._tdd_check_path(0, 1, "refactor", 1).exists(), "the checker must never be invoked for a no-op"


def test_tdd_refactor_loops_on_self_reported_refactor():
    init_git_repo(steps=TDD_STEP_PLAN)
    irg._write_tdd_cycle_state(0, {"cycle": 1, "phase": "refactor", "attempt": 1, "redo_feedback": ""})
    pathlib.Path("feature.go").write_text("refactored once", encoding="utf-8")
    mark_tdd_phase_done(["feature.go"], "refactor: extract helper", tdd_next_phase="refactor")
    write_tdd_check(0, 1, "refactor", 1, ok=True)

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "tdd_refactor"
    assert irg._read_tdd_cycle_state(0)["cycle"] == 1, "still the same cycle, just another refactor round"
    assert irg._completed_step_count() == 0


def test_tdd_cycle_completes_and_tags_step():
    """End-to-end happy path: red -> green (forced) -> refactor (reports
    complete) -> mechanical backstop passes -> falls through to the
    existing, unmodified trigger_match/interim-review/G2 flow, and the step
    gets tagged."""
    init_git_repo(steps=TDD_STEP_PLAN)

    pathlib.Path("feature_test.go").write_text("failing test", encoding="utf-8")
    mark_tdd_phase_done(["feature_test.go"], "red")
    write_tdd_check(0, 1, "red", 1, ok=True)
    irg.detect_phase({"phase": "", "reason": ""})

    pathlib.Path("feature.go").write_text("impl", encoding="utf-8")
    mark_tdd_phase_done(["feature.go"], "green")
    write_tdd_check(0, 1, "green", 1, ok=True)
    irg.detect_phase({"phase": "", "reason": ""})

    pathlib.Path("feature.go").write_text("impl, refactored", encoding="utf-8")
    mark_tdd_phase_done(["feature.go"], "refactor: tidy up", tdd_next_phase="complete")
    write_tdd_check(0, 1, "refactor", 1, ok=True)

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert irg._completed_step_count() == 1
    assert state["phase"] == "review_batch", "no triggered perspectives in the test fixture -> straight to phase 5"
    assert not irg.IMPLEMENTATION_RESULT_JSON.exists()
    assert not irg._tdd_step_dir(0).exists()


def test_tdd_step_mechanical_backstop_diffs_since_previous_tag():
    """A TDD step's commits already landed by finalization time, so `git
    status --porcelain` alone is clean -- the backstop must diff since the
    previous step's tag (or base_ref, for the first step) to still catch an
    out-of-plan file touched during one of the RGR phases."""
    init_git_repo(steps=TDD_STEP_PLAN)

    pathlib.Path("feature_test.go").write_text("failing test", encoding="utf-8")
    mark_tdd_phase_done(["feature_test.go"], "red")
    write_tdd_check(0, 1, "red", 1, ok=True)
    irg.detect_phase({"phase": "", "reason": ""})

    # Green touches an out-of-plan file too -- the phase-level process
    # checker only judges TDD process adherence, not file scope, so it still
    # approves and the commit lands.
    pathlib.Path("feature.go").write_text("impl", encoding="utf-8")
    pathlib.Path("unplanned.go").write_text("oops", encoding="utf-8")
    mark_tdd_phase_done(["feature.go", "unplanned.go"], "green")
    write_tdd_check(0, 1, "green", 1, ok=True)
    irg.detect_phase({"phase": "", "reason": ""})

    mark_tdd_phase_done([], tdd_next_phase="complete")  # no further refactor
    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "plan_reopened"
    assert "unplanned.go" in state["reason"]
    status = subprocess.run(["git", "status", "--porcelain"], capture_output=True, text=True, check=True).stdout
    assert status.strip() == "", "the deviation was caught via the since-tag diff, not a dirty working tree"


def test_mixed_plan_non_tdd_step_then_tdd_step_both_finalize_correctly():
    init_git_repo(steps=MIXED_MODE_PLAN)

    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "implement_step", "step 0 (non-TDD) uses the normal single-shot flow"

    pathlib.Path("go.mod").write_text("module updated", encoding="utf-8")
    mark_step_done(["go.mod"])
    irg.detect_phase({"phase": "", "reason": ""})
    assert irg._completed_step_count() == 1

    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "tdd_red", "step 1 (TDD) enters the Red/Green/Refactor sub-loop"

    pathlib.Path("feature_test.go").write_text("failing test", encoding="utf-8")
    mark_tdd_phase_done(["feature_test.go"], "red")
    write_tdd_check(1, 1, "red", 1, ok=True)
    irg.detect_phase({"phase": "", "reason": ""})

    pathlib.Path("feature.go").write_text("impl", encoding="utf-8")
    mark_tdd_phase_done(["feature.go"], "green")
    write_tdd_check(1, 1, "green", 1, ok=True)
    irg.detect_phase({"phase": "", "reason": ""})

    mark_tdd_phase_done([], tdd_next_phase="complete")
    irg.detect_phase({"phase": "", "reason": ""})

    assert irg._completed_step_count() == 2


def test_needs_plan_review_during_tdd_phase_reopens_plan_via_existing_mechanism():
    init_git_repo(steps=TDD_STEP_PLAN)
    irg.IMPLEMENTATION_RESULT_JSON.write_text(
        json.dumps({"status": "needs_plan_review", "reason": "計画にない依存が必要"}), encoding="utf-8"
    )

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "plan_reopened"
    assert "計画にない依存が必要" in state["reason"]


def test_tdd_finalization_backstop_rejection_resets_intermediate_commits():
    init_git_repo(steps=TDD_STEP_PLAN)

    pathlib.Path("feature_test.go").write_text("failing test", encoding="utf-8")
    mark_tdd_phase_done(["feature_test.go"], "red")
    write_tdd_check(0, 1, "red", 1, ok=True)
    irg.detect_phase({"phase": "", "reason": ""})

    pathlib.Path("feature.go").write_text("impl", encoding="utf-8")
    pathlib.Path("unplanned.go").write_text("oops", encoding="utf-8")
    mark_tdd_phase_done(["feature.go", "unplanned.go"], "green")
    write_tdd_check(0, 1, "green", 1, ok=True)
    irg.detect_phase({"phase": "", "reason": ""})

    mark_tdd_phase_done([], tdd_next_phase="complete")
    state = irg.detect_phase({"phase": "", "reason": ""})
    assert state["phase"] == "plan_reopened"

    base_sha = irg.BASE_REF_FILE.read_text(encoding="utf-8").strip()
    commits_before_reject = subprocess.run(
        ["git", "rev-list", "--count", f"{base_sha}..HEAD"], capture_output=True, text=True, check=True
    ).stdout.strip()
    assert commits_before_reject == "2", "red + green already landed"

    # DEVIATION.md is only written by write_task_md's "plan_reopened" branch
    # (same split ADR-0010 established) -- simulate it directly, matching
    # every other mechanical-deviation test in this file.
    state_client.put(irg.DEVIATION_KEY, state["reason"])
    state_client.put(irg.PLAN_GATE_KEY, json.dumps({"status": "rejected", "feedback": "計画外ファイルを削除してやり直して"}))

    state = irg.detect_phase({"phase": "", "reason": ""})

    assert state["phase"] == "tdd_red"
    commits_after_reject = subprocess.run(
        ["git", "rev-list", "--count", f"{base_sha}..HEAD"], capture_output=True, text=True, check=True
    ).stdout.strip()
    assert commits_after_reject == "0", "reset --hard must discard both landed commits"
    assert not pathlib.Path("unplanned.go").exists()
    assert irg._read_tdd_cycle_state(0) == {"cycle": 1, "phase": "red", "attempt": 1, "redo_feedback": ""}
    assert not irg.IMPLEMENTATION_RESULT_JSON.exists()
