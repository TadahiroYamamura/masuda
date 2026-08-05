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


def main():
    merged = _read_json(BAKED_SETTINGS_PATH)
    merged.update(_read_json(REPO_SETTINGS_PATH).get("claudeSettings", {}))
    json.dump(merged, sys.stdout)


if __name__ == "__main__":
    main()
