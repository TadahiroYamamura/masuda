#!/usr/bin/env python3
"""Merges the sandbox's build-time ~/.claude/settings.json (plugin
marketplace state from `claude plugin marketplace add`, ADR-0015) with the
target repository's .masuda/settings.json claudeSettings field, and writes
the result to stdout.

--settings takes priority over every other settings source, including a
repo's own .claude/settings.json -- passing claudeSettings to it directly
would silently drop the baked-in plugin marketplace registration, which
isn't a masuda-side implicit default (unlike the old theme bake-in this
mechanism replaces) but build-time infrastructure a documented feature
depends on. Merging here, before --settings ever sees the result, keeps
both: the baked file supplies keys claudeSettings doesn't mention, and
claudeSettings wins on any actual conflict.

Either input may be absent -- an unmodified image (no plugin state yet) or a
repository that hasn't run `masuda init` under this field's introduction are
both valid and treated as {}.
"""
import json
import sys
from pathlib import Path

BAKED_SETTINGS_PATH = Path.home() / ".claude" / "settings.json"
REPO_SETTINGS_PATH = Path("/workspace/.masuda/settings.json")


def _read_json(path):
    try:
        return json.loads(path.read_text())
    except FileNotFoundError:
        return {}


# Written by the Notification hook masuda always installs (ADR-0076): the
# host shows it as "waiting for input" until the next next_task call clears
# it. The file only notifies; it approves nothing, so it may sit in the
# guest-writable state directory.
INPUT_WAIT_HOOK = {
    "matcher": "permission_prompt|idle_prompt|elicitation_dialog|agent_needs_input",
    "hooks": [{
        "type": "command",
        "command": "mkdir -p /masuda-state/wf && cat > /masuda-state/wf/input-wait.json",
    }],
}


def main():
    merged = _read_json(BAKED_SETTINGS_PATH)
    user = _read_json(REPO_SETTINGS_PATH).get("claudeSettings", {})
    hooks = {**merged.get("hooks", {})}
    for event, entries in user.get("hooks", {}).items():
        hooks[event] = hooks.get(event, []) + list(entries)
    merged.update(user)
    # hooks is merged per event rather than replaced, so a repository that
    # declares its own hooks does not drop the one masuda relies on.
    hooks["Notification"] = hooks.get("Notification", []) + [INPUT_WAIT_HOOK]
    merged["hooks"] = hooks
    json.dump(merged, sys.stdout)


if __name__ == "__main__":
    main()
