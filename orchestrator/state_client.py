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


def _run(*args: str, stdin: str | None = None) -> str:
    result = subprocess.run(
        ["masuda", "internal", "state", *args],
        input=stdin,
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



def apply(ops: list[dict]) -> bool:
    """Applies ops as one atomic step, returning whether it applied.

    False means one of the "check" ops did not hold and *nothing* was
    changed -- an ordinary outcome (someone wrote the key between your read
    and this call), not an error. Read again and decide again.
    """
    out = json.loads(_run("apply", stdin=json.dumps(ops)))
    return out["applied"]


def consume(key: str, follow_up=None) -> str | None:
    """Takes key's current value and deletes it as one atomic step, returning
    what was taken (None if key does not exist).

    follow_up(value) returns extra ops to land in the same step: the durable
    record of what the caller is about to do with the value. Those are puts,
    which apply() lands *before* the delete, so a crash in the middle leaves
    the value in place to be taken again rather than losing it. That is what
    makes "the key exists" mean "there is a decision nobody has taken yet".

    The loop only turns when the value was replaced between the read and the
    apply, which takes a fresh write by someone else each time round.
    """
    while True:
        value = get(key)
        if value is None:
            return None
        ops = [{"op": "check", "key": key, "value": value}]
        if follow_up is not None:
            ops.extend(follow_up(value))
        ops.append({"op": "delete", "key": key})
        if apply(ops):
            return value
