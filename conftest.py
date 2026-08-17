import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parent / "orchestrator"))

# investigate_plan_graph.py / implement_review_graph.py read MASUDA_STATE_DIR
# as a module-level constant at import time. Collection imports these modules
# before any test fixture runs, so a placeholder must already be set here --
# each test then overrides it per-test and reload()s the module (see
# orchestrator/tests/test_*.py's autouse fixtures) for real isolation.
os.environ.setdefault("MASUDA_STATE_DIR", tempfile.mkdtemp(prefix="masuda-state-placeholder-"))


@pytest.fixture(scope="session", autouse=True)
def masuda_binary_on_path():
    """Builds masuda's own CLI binary once per test session and prepends its
    directory to PATH, so orchestrator/state_client.py's `masuda internal
    state ...` subprocess calls (and orchestrator/tests/conftest.py's
    state_daemon fixture) resolve a real binary. Layer 1 tests exercise a
    real state daemon (Issue #35 phase A) for the subset of state that moved
    there, rather than writing files directly.
    """
    repo_root = Path(__file__).resolve().parent
    bin_dir = Path(tempfile.mkdtemp(prefix="masuda-bin-"))
    subprocess.run(
        ["go", "build", "-o", str(bin_dir / "masuda"), "./cmd/masuda"],
        cwd=repo_root,
        check=True,
    )
    os.environ["PATH"] = f"{bin_dir}{os.pathsep}{os.environ.get('PATH', '')}"
    yield
    shutil.rmtree(bin_dir, ignore_errors=True)
