# Kitchen-sink sandbox variant (roadmap step 8): Go + Python + TypeScript
# toolchains and all three official LSP plugins, for polyglot repos (e.g. a
# Go backend + TypeScript frontend) where a single-language variant
# (docker/go, docker/python, docker/typescript) would leave part of the repo
# without diagnostics/navigation. Not built by combining those three
# Dockerfiles via multiple FROMs (Docker doesn't support merging separate
# image lineages that way) -- this just repeats their install steps against
# one base.
FROM masuda-loop:latest

USER root
RUN curl -fsSL https://go.dev/dl/go1.26.5.linux-amd64.tar.gz -o /tmp/go.tar.gz \
 && echo "5c2c3b16caefa1d968a94c1daca04a7ca301a496d9b086e17ad77bb81393f053  /tmp/go.tar.gz" | sha256sum -c - \
 && tar -C /usr/local -xzf /tmp/go.tar.gz \
 && rm /tmp/go.tar.gz
RUN npm install -g pyright typescript typescript-language-server

USER ubuntu
ENV PATH="/usr/local/go/bin:/home/ubuntu/go/bin:$PATH"
RUN go install golang.org/x/tools/gopls@latest \
 && go clean -modcache -cache

RUN claude plugin install gopls-lsp@claude-plugins-official --scope user \
 && claude plugin install pyright-lsp@claude-plugins-official --scope user \
 && claude plugin install typescript-lsp@claude-plugins-official --scope user
