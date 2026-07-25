#!/usr/bin/env bash
set -euo pipefail

SESSION="claude-work"
PROMPT="CLAUDE.mdのルールに従い作業を開始せよ"

# Pass the initial prompt as a positional argument so Claude starts working immediately
tmux new-session -d -s "$SESSION" \
    "claude --dangerously-skip-permissions '$PROMPT'"

# Poll for the bypass permissions dialog (up to 10s) and accept it if shown
for i in $(seq 1 10); do
    sleep 1
    pane=$(tmux capture-pane -t "$SESSION" -p 2>/dev/null || true)
    if echo "$pane" | grep -q "Yes, I accept"; then
        tmux send-keys -t "$SESSION" "2" Enter
        echo "[entrypoint] bypass dialog accepted"
        break
    fi
    if echo "$pane" | grep -q "bypass permissions on"; then
        echo "[entrypoint] Claude started (no dialog)"
        break
    fi
done

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
