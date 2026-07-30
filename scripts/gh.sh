#!/usr/bin/env bash
# Wraps `gh` so it authenticates with a repo-scoped token kept in .env,
# without that token ever passing through a tool Claude can read (.env
# itself is denied to Claude at the permission level, see
# .claude/settings.json). Expects .env to define GH_TOKEN (gh's supported
# override, takes precedence over any stored `gh auth login` session);
# falls back to the ambient `gh` auth if .env or GH_TOKEN is absent.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

exec gh "$@"
