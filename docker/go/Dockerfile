# Go-flavored sandbox variant (roadmap step 8): adds the Go toolchain,
# gopls, and the official gopls-lsp Claude Code plugin on top of the base
# masuda-loop image, so Claude gets automatic diagnostics and code
# navigation for Go code without any masuda-side language detection --
# a repo picks this image explicitly via .masuda.json's "image" field
# (internal/config) or `--image masuda-loop:go`.
#
# Lives at docker/go/Dockerfile, not a repo-root Dockerfile.go: naming it
# Dockerfile.go breaks `go build ./...`/`go vet ./...` at the repo root --
# Go's tooling treats any *.go file in the module tree as a source file
# regardless of directory or intent, and fails on the very first line.
# Confirmed empirically before settling on this layout.
FROM masuda-loop:latest

# Installing to /usr/local/go needs root; swap back to ubuntu afterward for
# the user-scoped `go install` and `claude plugin install` steps below.
USER root
RUN curl -fsSL https://go.dev/dl/go1.26.5.linux-amd64.tar.gz -o /tmp/go.tar.gz \
 && echo "5c2c3b16caefa1d968a94c1daca04a7ca301a496d9b086e17ad77bb81393f053  /tmp/go.tar.gz" | sha256sum -c - \
 && tar -C /usr/local -xzf /tmp/go.tar.gz \
 && rm /tmp/go.tar.gz

USER ubuntu
ENV PATH="/usr/local/go/bin:/home/ubuntu/go/bin:$PATH"

# `rm -rf` on GOPATH/pkg/mod fails: Go marks the module cache read-only
# (including the containing directories) to guard against accidental edits,
# so only `go clean -modcache` (which knows to chmod first) can remove it.
RUN go install golang.org/x/tools/gopls@latest \
 && go clean -modcache -cache

RUN claude plugin install gopls-lsp@claude-plugins-official --scope user
