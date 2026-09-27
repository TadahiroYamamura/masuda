#!/usr/bin/env bash
set -euo pipefail

SESSION="claude-work"
PROMPT="CLAUDE.mdのルールに従い作業を開始せよ"

# VMBackend.Start runs mcp-relay on the *host*, bound to the bridge gateway
# IP, and passes its address via the masuda.mcp_relay= kernel command line
# parameter (--cmdline). The guest cannot run its own relay: virtiofs can't
# share a Unix domain socket special file across host/guest kernels
# (confirmed live in M3), so there is no local socket to bridge from.
#
# Missing is fatal rather than falling back. The fallback used to be the
# Docker path's own guest-side relay, which went away with ADR-0044 -- and
# without a relay this session has no gate tools and no next_task, so it
# could not run the loop at all. Failing here says why; starting Claude
# without its tools would not.
MCP_RELAY_ADDR=$(sed -n 's/.*masuda\.mcp_relay=\([^ ]*\).*/\1/p' /proc/cmdline)
if [ -z "$MCP_RELAY_ADDR" ]; then
    echo "[entrypoint] masuda.mcp_relay= missing from /proc/cmdline -- no MCP relay to reach the host's state daemon" >&2
    exit 1
fi
# timeout (ms, 7 days): confirmed live that without a generous per-server
# override, Claude Code aborts a wait_for_gate_resolution call on its own hard
# wall-clock MCP tool timeout well under a minute -- long before any real
# human gets around to approving a gate.
MCP_CONFIG="{\"mcpServers\":{\"masuda-gate\":{\"type\":\"http\",\"url\":\"http://$MCP_RELAY_ADDR/\",\"timeout\":604800000}}}"

# --settings overrides every other settings source, including the target
# repo's own .claude/settings.json (needed to reliably suppress e.g. the MCP
# trust prompt regardless of what the repo declares) -- so this merges in
# the build-time-baked plugin marketplace state first (see
# merge_claude_settings.py) rather than letting --settings silently drop it.
MERGED_SETTINGS=/tmp/masuda-claude-settings.json
python3 /opt/masuda/runtime/merge_claude_settings.py > "$MERGED_SETTINGS"

# The real Claude token never enters the guest. Claude Code here holds a
# placeholder and talks to the API only through the host-side gateway
# VMBackend.Start runs for this VM (`masuda internal api-gateway`, address
# via masuda.api_gateway=), which swaps the placeholder for the real token.
# The placeholder must match cmd/masuda/apigateway.go's
# guestPlaceholderToken. Missing is fatal for the same reason as the relay
# above: Claude could not reach the API at all.
#
# CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: the calls Claude Code still makes
# straight to api.anthropic.com (telemetry, bootstrap, the MCP registry)
# bypass the gateway, carry only the placeholder, and are refused by the
# egress proxy anyway -- turning them off keeps them from failing on every
# start. export'd rather than inlined into the tmux command string: tmux's
# new-session inherits the server's environment, which is this script's
# environment at the point the server first starts.
API_GATEWAY_ADDR=$(sed -n 's/.*masuda\.api_gateway=\([^ ]*\).*/\1/p' /proc/cmdline)
if [ -z "$API_GATEWAY_ADDR" ]; then
    echo "[entrypoint] masuda.api_gateway= missing from /proc/cmdline -- no way to reach the Anthropic API" >&2
    exit 1
fi
export ANTHROPIC_BASE_URL="http://$API_GATEWAY_ADDR"
export CLAUDE_CODE_OAUTH_TOKEN=masuda-sandbox-placeholder-token
export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1

# VM boot path: git identity for the Build stage's per-step commits inside
# the guest (see internal/sandbox/gitidentity.go's WriteGitIdentity) --
# a VM's rootfs (built from the same Docker image) has no ~/.gitconfig any
# more than a container did, so git itself has no identity to commit with
# otherwise. Read as two plain lines (name, email), not sourced as shell,
# since either value may contain characters that would need escaping to
# embed safely in a script. Comes in for free over the existing
# /masuda-state virtiofs mount, already required by masuda-loop.service.
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

# Pass the initial prompt as a positional argument so Claude starts working immediately.
# MERGED_SETTINGS carries skipDangerousModePermissionPrompt: true (ADR-0034),
# so the bypass-permissions-mode disclaimer dialog never appears here — no
# tmux capture-pane/send-keys polling needed to get past it.
#
# CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=0 disables Claude Code's separate idle-
# timeout abort (distinct from MCP_CONFIG's per-server "timeout" above,
# which covers the hard wall-clock one) as defense in depth -- belt and
# suspenders, since only the hard timeout was confirmed live to matter for
# wait_for_gate_resolution specifically.
#
# The `--` before the prompt is required: --mcp-config takes a
# space-separated *list* of configs, so without a terminator Claude Code
# silently swallows the prompt string as an extra (invalid) --mcp-config
# entry and refuses to start ("MCP config file not found: <prompt text>") --
# confirmed live.
tmux new-session -d -s "$SESSION" \
    "CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=0 claude --dangerously-skip-permissions --settings '$MERGED_SETTINGS' --mcp-config '$MCP_CONFIG' -- '$PROMPT'"

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
