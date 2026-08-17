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
# fine here. timeout (ms, 7 days): see runtime/entrypoint.sh -- confirmed
# live that without this, Claude Code aborts a wait_for_gate_change call on
# its own hard wall-clock MCP tool timeout well under a minute.
MCP_RELAY_PORT=39217
MCP_CONFIG="{\"mcpServers\":{\"masuda-gate\":{\"type\":\"http\",\"url\":\"http://127.0.0.1:$MCP_RELAY_PORT/\",\"timeout\":604800000}}}"

# See runtime/entrypoint.sh for why this merge happens before --settings.
MERGED_SETTINGS=/tmp/masuda-claude-settings.json
python3 /opt/masuda/runtime/merge_claude_settings.py > "$MERGED_SETTINGS"

# MERGED_SETTINGS carries skipDangerousModePermissionPrompt: true (ADR-0034),
# so the bypass-permissions-mode disclaimer dialog never appears here — no
# tmux capture-pane/send-keys polling needed to get past it.
# CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=0: see runtime/entrypoint.sh (defense in
# depth alongside MCP_CONFIG's per-server "timeout" above).
tmux new-session -d -s "$SESSION" -c "$WORKDIR" \
    "CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=0 claude --dangerously-skip-permissions --settings '$MERGED_SETTINGS' --mcp-config '$MCP_CONFIG'"
