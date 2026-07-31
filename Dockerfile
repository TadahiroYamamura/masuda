FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

# Node.js 22 (LTS) + system tools
# inotify-tools: runtime/CLAUDE.md's gate-wait step uses a single blocking
# `inotifywait` call instead of a `while` loop or the Monitor tool -- both of
# those got flagged by Claude Code's own permission risk-evaluation and stalled
# an unattended loop waiting on a confirmation that never comes (confirmed
# live); a lone inotifywait call doesn't trigger that even with a dynamic
# absolute path embedded in it.
# build-essential: a C toolchain is needed across all language variants, not
# just one -- Go's `go test -race`/cgo, Python packages without prebuilt
# wheels, and Node native addons (node-gyp) all fall back to compiling from
# source. Baking it in here once (ADR-0022) avoids repeating the same RUN
# line in docker/{go,python,typescript}/Dockerfile.
RUN apt-get update \
 && apt-get install -y ca-certificates curl gnupg build-essential \
 && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
 && apt-get install -y nodejs python3 python3-venv tmux ttyd git inotify-tools \
 && rm -rf /var/lib/apt/lists/*

# Claude CLI
RUN npm install -g @anthropic-ai/claude-code

# ubuntu user (uid=1000) already exists in ubuntu:24.04.
# Using it avoids root restriction and matches host file ownership on mounted ~/.claude/.credentials.json (mode 600)
#
# masuda's own control files (venv/orchestrator/runtime) live under /opt/masuda, not
# /workspace: /workspace is reserved as the bind-mount point for the target repository's
# worktree (masuda sandbox start mounts a different worktree there per run), and a bind
# mount replaces the mount point's entire contents — anything baked in at /workspace would
# be shadowed the moment a worktree is mounted over it. /masuda-state is a second,
# separate bind-mount point for that workspace's state directory (roadmap step 7):
# masuda's own TASK.md/PLAN.md/gate markers/etc. live there instead of in /workspace, so
# they never show up in the target repository's own `git status`.
RUN mkdir -p /workspace /masuda-state /opt/masuda /home/ubuntu/.claude \
 && chown ubuntu:ubuntu /workspace /masuda-state /opt/masuda /home/ubuntu/.claude

WORKDIR /opt/masuda

# Python venv + LangGraph (baked into image)
COPY --chown=ubuntu:ubuntu requirements.txt ./
RUN python3 -m venv venv \
 && venv/bin/pip install --no-cache-dir -r requirements.txt

# Project files
# runtime/CLAUDE.md (loop protocol) is intentionally not baked in here — it belongs at
# ~/.claude/CLAUDE.md, placed at container startup by masuda sandbox start (masuda CLI's
# job, not the image build), so it can be iterated on without rebuilding the image.
# See docs/adr/0007-loop-protocol-claude-md-in-user-scope.md
COPY --chown=ubuntu:ubuntu orchestrator/ orchestrator/
COPY --chown=ubuntu:ubuntu runtime/entrypoint.sh runtime/start_claude.sh runtime/
RUN chmod +x runtime/start_claude.sh runtime/entrypoint.sh

# Pre-set the theme so a fresh container's first `claude` launch doesn't stop
# at the interactive first-run theme-selection wizard (confirmed empirically —
# --dangerously-skip-permissions does not skip this, only the separate
# bypass-permissions dialog entrypoint.sh already handles). This is a fixed
# onboarding-bypass setting, unlike runtime/CLAUDE.md, so it's fine to bake
# into the image rather than place at container start.
COPY --chown=ubuntu:ubuntu runtime/claude-settings.json /home/ubuntu/.claude/settings.json

USER ubuntu

# Install Claude native binary as ubuntu user (baked into image)
ENV PATH="/home/ubuntu/.local/bin:$PATH"
RUN claude install

# Register (but don't install anything from) the official plugin marketplace,
# so language-variant images (Dockerfile.go, Dockerfile.python, ...) built
# `FROM` this one only need a plain `claude plugin install <name>@claude-
# plugins-official`, not a marketplace add too (roadmap step 8). Confirmed
# empirically that both `marketplace add` and `plugin install` need no
# Claude Code auth at all -- they're just a `git clone` of the (public)
# marketplace repo and a copy out of its cache, so this is safe to bake in
# at build time despite ADR-0001's no-API-billing constraint (nothing here
# talks to the Anthropic API). Plugin/marketplace state lands in
# ~/.claude/settings.json and ~/.claude/plugins/ -- neither is one of the
# two files `sandbox.Start` bind-mounts from the host (~/.claude.json,
# ~/.claude/.credentials.json), confirmed by inspecting ~/.claude.json's
# contents after a real install: it holds only generic client metadata
# (installMethod, machineID, ...), nothing plugin-related. So the bind
# mount at container start can't shadow what's baked in here.
RUN claude plugin marketplace add anthropics/claude-plugins-official

# ttyd web terminal port (mapped to a per-container host port by masuda sandbox start,
# since multiple sandboxes run in parallel)
EXPOSE 7682

WORKDIR /workspace

ENTRYPOINT ["/opt/masuda/runtime/entrypoint.sh"]
