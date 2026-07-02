#!/usr/bin/env bash
# Starts Claude in a tmux session and auto-accepts the bypass permissions dialog
# if it appears. Safe to run even when the dialog is skipped (e.g. re-runs).

SESSION="claude-work"
WORKDIR="$(cd "$(dirname "$0")" && pwd)"

if tmux has-session -t "$SESSION" 2>/dev/null; then
    echo "[start_claude] session '$SESSION' already running, skip"
    exit 0
fi

tmux new-session -d -s "$SESSION" -c "$WORKDIR" \
    "claude --dangerously-skip-permissions"

# Poll until the dialog appears or Claude is already at the prompt (no dialog).
for i in $(seq 1 10); do
    sleep 1
    pane=$(tmux capture-pane -t "$SESSION" -p 2>/dev/null)
    if echo "$pane" | grep -q "Yes, I accept"; then
        tmux send-keys -t "$SESSION" "2" Enter
        echo "[start_claude] bypass dialog accepted"
        break
    fi
    if echo "$pane" | grep -q "bypass permissions on"; then
        echo "[start_claude] Claude already running (no dialog)"
        break
    fi
done
