import shutil
import subprocess
import tempfile
import time
from pathlib import Path

import pytest


@pytest.fixture
def state_daemon(monkeypatch):
    """Starts a real state daemon (Issue #35) bound to a short-lived
    directory under /tmp -- not pytest's own tmp_path, whose longer paths
    risk exceeding AF_UNIX's ~108 byte sun_path limit once daemon.sock's own
    path is appended (see internal/statedaemon/mcpserver's uds_test.go on
    the Go side) -- and points MASUDA_STATE_DIR at it. Requires the `masuda`
    binary on PATH; see the root conftest.py's masuda_binary_on_path
    fixture. Yields the state directory.
    """
    state_dir = Path(tempfile.mkdtemp(prefix="ms"))
    monkeypatch.setenv("MASUDA_STATE_DIR", str(state_dir))

    proc = subprocess.Popen(
        ["masuda", "internal", "statedaemon", "--state-dir", str(state_dir)],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
        text=True,
    )
    socket_path = state_dir / "daemon.sock"
    deadline = time.monotonic() + 5
    while time.monotonic() < deadline and not socket_path.exists():
        if proc.poll() is not None:
            raise RuntimeError(f"state daemon exited early: {proc.stderr.read()}")
        time.sleep(0.01)
    if not socket_path.exists():
        proc.terminate()
        raise RuntimeError("state daemon socket never appeared")

    try:
        yield state_dir
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
        shutil.rmtree(state_dir, ignore_errors=True)
