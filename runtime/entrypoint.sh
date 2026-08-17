#!/usr/bin/env bash
set -euo pipefail

SESSION="claude-work"
PROMPT="CLAUDE.mdのルールに従い作業を開始せよ"

# Bridges the curated MCP tool set's Unix domain socket
# (/masuda-state/daemon-curated.sock, Issue #35) to a local TCP port --
# Claude Code's --mcp-config only understands http://host:port URLs. A
# fixed port is fine here (unlike the phase 1-2 host loop's dynamically
# chosen one, internal/hostloop.startMCPRelay) since this container has its
# own network namespace; started once for the container's whole lifetime,
# start_claude.sh's later `claude` invocations reuse the same port without
# starting a second relay.
MCP_RELAY_PORT=39217
masuda internal mcp-relay --socket /masuda-state/daemon-curated.sock --port "$MCP_RELAY_PORT" &
MCP_CONFIG="{\"mcpServers\":{\"masuda-gate\":{\"type\":\"http\",\"url\":\"http://127.0.0.1:$MCP_RELAY_PORT/\"}}}"

# --settings overrides every other settings source, including the target
# repo's own .claude/settings.json (needed to reliably suppress e.g. the MCP
# trust prompt regardless of what the repo declares) -- so this merges in
# the build-time-baked plugin marketplace state first (see
# merge_claude_settings.py) rather than letting --settings silently drop it.
MERGED_SETTINGS=/tmp/masuda-claude-settings.json
python3 /opt/masuda/runtime/merge_claude_settings.py > "$MERGED_SETTINGS"

# Pass the initial prompt as a positional argument so Claude starts working immediately.
# MERGED_SETTINGS carries skipDangerousModePermissionPrompt: true (ADR-0034),
# so the bypass-permissions-mode disclaimer dialog never appears here — no
# tmux capture-pane/send-keys polling needed to get past it.
tmux new-session -d -s "$SESSION" \
    "claude --dangerously-skip-permissions --settings '$MERGED_SETTINGS' --mcp-config '$MCP_CONFIG' '$PROMPT'"

echo "[entrypoint] ttyd starting on :7682"

# Run ttyd in background so we can watch for session termination
ttyd --writable --port 7682 tmux attach -t "$SESSION" &
TTYD_PID=$!

# Wait until Claude kills the tmux session (loop complete)
while tmux has-session -t "$SESSION" 2>/dev/null; do
    sleep 2
done

echo "[entrypoint] loop complete — shutting down"
kill "$TTYD_PID" 2>/dev/null
exit 0
