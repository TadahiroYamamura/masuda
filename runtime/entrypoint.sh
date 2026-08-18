#!/usr/bin/env bash
set -euo pipefail

SESSION="claude-work"
PROMPT="CLAUDE.mdのルールに従い作業を開始せよ"

# VM boot path (Issue #31 M5-6): VMBackend.Start already runs mcp-relay on
# the *host*, bound to the bridge gateway IP, and passes its address via the
# masuda.mcp_relay= kernel command line parameter (--cmdline). The guest
# can't run its own relay bridging /masuda-state/daemon-curated.sock the way
# the Docker path does -- virtiofs can't share a Unix domain socket special
# file across host/guest kernels (confirmed live in M3), so there's no local
# socket for a guest-side relay to bridge from in the first place. When this
# parameter is present, skip starting a relay entirely and just point
# Claude Code straight at the host's.
MCP_RELAY_ADDR=$(sed -n 's/.*masuda\.mcp_relay=\([^ ]*\).*/\1/p' /proc/cmdline)
if [ -z "$MCP_RELAY_ADDR" ]; then
    # Docker path: bridges the curated MCP tool set's Unix domain socket
    # (/masuda-state/daemon-curated.sock, Issue #35) to a local TCP port --
    # Claude Code's --mcp-config only understands http://host:port URLs. A
    # fixed port is fine here (unlike the phase 1-2 host loop's dynamically
    # chosen one, internal/hostloop.startMCPRelay) since this container has
    # its own network namespace; started once for the container's whole
    # lifetime, start_claude.sh's later `claude` invocations reuse the same
    # port without starting a second relay.
    MCP_RELAY_PORT=39217
    masuda internal mcp-relay --socket /masuda-state/daemon-curated.sock --port "$MCP_RELAY_PORT" &
    MCP_RELAY_ADDR="127.0.0.1:$MCP_RELAY_PORT"
fi
# timeout (ms, 7 days): confirmed live that without a generous per-server
# override, Claude Code aborts a wait_for_gate_change call on its own hard
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

# VM boot path (Issue #31 M5-6): a `claude setup-token` OAuth token,
# registered on the host via `masuda internal claude-token set` and shared
# in read-only over virtiofs at /masuda-secrets (runtime/fstab.vm's
# claude-secrets tag, only present when a token was actually registered --
# see internal/sandbox/claudetoken.go for why the Docker path's
# ~/.claude file bind mounts don't translate to a VM guest). export'd here
# (not inlined into the tmux command string below, unlike
# CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT) so the token doesn't show up in `ps`
# output -- tmux's new-session inherits the server's environment, which is
# this script's environment at the point the server first starts.
if [ -r /masuda-secrets/token ]; then
    export CLAUDE_CODE_OAUTH_TOKEN
    CLAUDE_CODE_OAUTH_TOKEN=$(cat /masuda-secrets/token)
fi

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
# wait_for_gate_change specifically.
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
