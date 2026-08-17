#!/usr/bin/env bash
# Starts Claude in a tmux session. Safe to run even on re-entry (skips if a
# session is already running).

SESSION="claude-work"
WORKDIR="/workspace"

if tmux has-session -t "$SESSION" 2>/dev/null; then
    echo "[start_claude] session '$SESSION' already running, skip"
    exit 0
fi

# Same fixed port entrypoint.sh started the MCP relay on, once, for this
# container's whole lifetime -- see that script for why a fixed port is
# fine here.
MCP_RELAY_PORT=39217
MCP_CONFIG="{\"mcpServers\":{\"masuda-gate\":{\"type\":\"http\",\"url\":\"http://127.0.0.1:$MCP_RELAY_PORT/\"}}}"

# See runtime/entrypoint.sh for why this merge happens before --settings.
MERGED_SETTINGS=/tmp/masuda-claude-settings.json
python3 /opt/masuda/runtime/merge_claude_settings.py > "$MERGED_SETTINGS"

# MERGED_SETTINGS carries skipDangerousModePermissionPrompt: true (ADR-0034),
# so the bypass-permissions-mode disclaimer dialog never appears here — no
# tmux capture-pane/send-keys polling needed to get past it.
tmux new-session -d -s "$SESSION" -c "$WORKDIR" \
    "claude --dangerously-skip-permissions --settings '$MERGED_SETTINGS' --mcp-config '$MCP_CONFIG'"
