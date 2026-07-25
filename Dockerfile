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
RUN mkdir -p /workspace && chown ubuntu:ubuntu /workspace

WORKDIR /workspace

# Python venv + LangGraph (baked into image)
COPY --chown=ubuntu:ubuntu requirements.txt ./
RUN python3 -m venv venv \
 && venv/bin/pip install --no-cache-dir -r requirements.txt

# Project files
# runtime/CLAUDE.md (loop protocol) is intentionally not baked in here — it belongs at
# ~/.claude/CLAUDE.md, placed at container startup (masuda CLI's job, not the image build).
# See docs/adr/0007-loop-protocol-claude-md-in-user-scope.md
COPY --chown=ubuntu:ubuntu orchestrator/ orchestrator/
COPY --chown=ubuntu:ubuntu runtime/entrypoint.sh runtime/start_claude.sh runtime/
RUN chmod +x runtime/start_claude.sh runtime/entrypoint.sh

USER ubuntu

# Install Claude native binary as ubuntu user (baked into image)
ENV PATH="/home/ubuntu/.local/bin:$PATH"
RUN claude install

# ttyd web terminal port
EXPOSE 7682

ENTRYPOINT ["/workspace/runtime/entrypoint.sh"]
