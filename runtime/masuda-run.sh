#!/bin/sh
# Guest-side runner for one masuda privileged command (ADR-0053).
#
# Injected into the disposable VM's rootfs by masuda, not COPYed by the
# image's Dockerfile: that Dockerfile belongs to the target repository
# (ADR-0054), and this is masuda's own protocol for getting a result back
# out. A user editing their image must not be able to break -- or quietly
# reshape -- how the exit code and log are reported.
#
# Everything it needs arrives in the results share, written by the host
# before boot; everything it produces goes back the same way. There is no
# network path back to masuda from here, by design: this VM holds no
# credentials and gets no MCP relay.
set -u

RESULTS=/masuda-results
LOG="$RESULTS/log"
EXIT_CODE_FILE="$RESULTS/exit-code"

fail() {
	printf '%s\n' "$1" >> "$LOG"
	printf '%s\n' "$2" > "$EXIT_CODE_FILE"
	systemctl poweroff
	exit 0
}

: > "$LOG"

if [ ! -r "$RESULTS/command" ]; then
	# 125: distinct from anything the command itself is likely to return,
	# so the host can tell "masuda's own plumbing failed" from "the command
	# failed" without a second channel.
	fail "masuda: no command was staged at $RESULTS/command" 125
fi
if ! [ -d /workspace ]; then
	fail "masuda: /workspace was not mounted" 125
fi

COMMAND=$(cat "$RESULTS/command")
TIMEOUT=0
if [ -r "$RESULTS/timeout-seconds" ]; then
	TIMEOUT=$(cat "$RESULTS/timeout-seconds")
fi
MAX_LOG_BYTES=$(cat "$RESULTS/max-log-bytes" 2>/dev/null || echo 8388608)

cd /workspace || fail "masuda: /workspace is not usable as a working directory" 125

# The exit code is written from inside the pipeline's left side rather than
# read from $? afterwards: this has to work under a plain POSIX shell, where
# there is no pipefail and $? would report the log filter's status instead.
#
# The filter keeps reading after the cap so the command never takes a
# SIGPIPE and dies half-way through -- a hard cap on what masuda stores must
# not become a way to kill a long, chatty test run.
{
	if [ "$TIMEOUT" -gt 0 ]; then
		timeout --signal=TERM --kill-after=10s "${TIMEOUT}s" sh -c "$COMMAND"
	else
		sh -c "$COMMAND"
	fi
	printf '%s\n' "$?" > "$EXIT_CODE_FILE"
} 2>&1 | awk -v max="$MAX_LOG_BYTES" '
	{ if (written < max) { print; written += length($0) + 1 } }
	END { if (written >= max) print "masuda: log truncated at " max " bytes" }
' >> "$LOG"

sync
systemctl poweroff
