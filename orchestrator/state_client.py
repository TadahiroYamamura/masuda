"""
Client for masuda's per-workspace state daemon (Issue #35).

Rather than embed an MCP client in Python, this shells out to `masuda
internal state ...` once per operation -- the wire protocol is implemented
exactly once, in Go (cmd/masuda/internalstate.go), and this module is a thin
subprocess wrapper around it. See that file's package doc for the rationale.

The socket to talk to is resolved by the `masuda` binary itself from
$MASUDA_STATE_DIR (the same env var orchestrator/*.py already reads STATE_DIR
from), so nothing here needs to know the daemon's on-disk layout.
"""
import json
import subprocess


class StateError(RuntimeError):
    """Raised when `masuda internal state ...` exits non-zero."""


def _run(*args: str) -> str:
    result = subprocess.run(
        ["masuda", "internal", "state", *args],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise StateError(f"masuda internal state {' '.join(args)}: {result.stderr.strip()}")
    return result.stdout.strip()


def get(key: str) -> str | None:
    """Returns key's current value, or None if it doesn't exist."""
    out = json.loads(_run("get", key))
    return out["value"] if out["found"] else None


def exists(key: str) -> bool:
    return get(key) is not None


def put(key: str, value: str) -> None:
    _run("put", key, value)


def delete(key: str) -> None:
    """Deletes key. Not an error if it doesn't exist."""
    _run("delete", key)


def list_keys(prefix: str) -> list[str]:
    """Returns every key currently starting with prefix, sorted."""
    out = json.loads(_run("list", prefix))
    return out["keys"]


def wait(key: str) -> str | None:
    """Blocks until the next put/delete on key, returning its new value (None
    if it was deleted). The subprocess equivalent of ADR-0017's single
    blocking inotifywait call."""
    out = json.loads(_run("wait", key))
    return out["value"] if out["found"] else None
