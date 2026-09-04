#!/usr/bin/env bash
# Starts Claude in a tmux session. Safe to run even on re-entry (skips if a
# session is already running).

SESSION="claude-work"
WORKDIR="/workspace"

if tmux has-session -t "$SESSION" 2>/dev/null; then
    echo "[start_claude] session '$SESSION' already running, skip"
    exit 0
fi

# The host-side relay VMBackend.Start runs, for this VM's whole lifetime --
# resolve the same address entrypoint.sh already did rather than starting
# anything of our own. Missing is fatal for the same reason it is there (see
# runtime/entrypoint.sh): without the relay this session has no gate tools
# and no next_task. timeout (ms, 7 days): also see entrypoint.sh --
# confirmed live that without this, Claude Code aborts a
# wait_for_gate_resolution call on its own hard wall-clock MCP tool timeout
# well under a minute.
MCP_RELAY_ADDR=$(sed -n 's/.*masuda\.mcp_relay=\([^ ]*\).*/\1/p' /proc/cmdline)
if [ -z "$MCP_RELAY_ADDR" ]; then
    echo "[start_claude] masuda.mcp_relay= missing from /proc/cmdline -- no MCP relay to reach the host's state daemon" >&2
    exit 1
fi
MCP_CONFIG="{\"mcpServers\":{\"masuda-gate\":{\"type\":\"http\",\"url\":\"http://$MCP_RELAY_ADDR/\",\"timeout\":604800000}}}"

# See runtime/entrypoint.sh for why this merge happens before --settings.
MERGED_SETTINGS=/tmp/masuda-claude-settings.json
python3 /opt/masuda/runtime/merge_claude_settings.py > "$MERGED_SETTINGS"

# See runtime/entrypoint.sh for what this is and why it's export'd rather
# than inlined into the tmux command string.
if [ -r /masuda-secrets/token ]; then
    export CLAUDE_CODE_OAUTH_TOKEN
    CLAUDE_CODE_OAUTH_TOKEN=$(cat /masuda-secrets/token)
fi

# See runtime/entrypoint.sh for what this is and why it's two plain lines,
# not sourced as shell.
GIT_IDENTITY_FILE=/masuda-state/.masuda-git-identity
if [ -r "$GIT_IDENTITY_FILE" ]; then
    GIT_AUTHOR_NAME=$(sed -n '1p' "$GIT_IDENTITY_FILE")
    GIT_AUTHOR_EMAIL=$(sed -n '2p' "$GIT_IDENTITY_FILE")
    if [ -n "$GIT_AUTHOR_NAME" ]; then
        export GIT_AUTHOR_NAME GIT_COMMITTER_NAME="$GIT_AUTHOR_NAME"
    fi
    if [ -n "$GIT_AUTHOR_EMAIL" ]; then
        export GIT_AUTHOR_EMAIL GIT_COMMITTER_EMAIL="$GIT_AUTHOR_EMAIL"
    fi
fi

# MERGED_SETTINGS carries skipDangerousModePermissionPrompt: true (ADR-0034),
# so the bypass-permissions-mode disclaimer dialog never appears here — no
# tmux capture-pane/send-keys polling needed to get past it.
# CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=0: see runtime/entrypoint.sh (defense in
# depth alongside MCP_CONFIG's per-server "timeout" above).
tmux new-session -d -s "$SESSION" -c "$WORKDIR" \
    "CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=0 claude --dangerously-skip-permissions --settings '$MERGED_SETTINGS' --mcp-config '$MCP_CONFIG'"
