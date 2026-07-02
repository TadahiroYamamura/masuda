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
COPY --chown=ubuntu:ubuntu CLAUDE.md langgraph_orchestrator.py start_claude.sh entrypoint.sh ./
RUN chmod +x start_claude.sh entrypoint.sh

USER ubuntu

# Install Claude native binary as ubuntu user (baked into image)
ENV PATH="/home/ubuntu/.local/bin:$PATH"
RUN claude install

# ttyd web terminal port
EXPOSE 7682

ENTRYPOINT ["/workspace/entrypoint.sh"]
