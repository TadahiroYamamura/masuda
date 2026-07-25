FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

# Node.js 22 (LTS) + system tools
RUN apt-get update \
 && apt-get install -y ca-certificates curl gnupg \
 && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
 && apt-get install -y nodejs python3 python3-venv tmux ttyd \
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
# be shadowed the moment a worktree is mounted over it.
RUN mkdir -p /workspace /opt/masuda /home/ubuntu/.claude \
 && chown ubuntu:ubuntu /workspace /opt/masuda /home/ubuntu/.claude

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

USER ubuntu

# Install Claude native binary as ubuntu user (baked into image)
ENV PATH="/home/ubuntu/.local/bin:$PATH"
RUN claude install

# ttyd web terminal port (mapped to a per-container host port by masuda sandbox start,
# since multiple sandboxes run in parallel)
EXPOSE 7682

WORKDIR /workspace

ENTRYPOINT ["/opt/masuda/runtime/entrypoint.sh"]
