# masuda自身をmasudaで開発するためのゲストイメージ。
# `masuda image build default` または `masuda run` の起動時に sandbox service が Docker でビルドし、
# VMのrootfsにする。カーネルと起動層は sandbox 側の資産で、ここには含めない。
#
# ビルド前に go.mod/go.sum の写しを更新すること（依存をイメージに前取りするため。
# VMの外向き通信は許可リストだけなので、proxy.golang.org には出られない）:
#   cp go.mod go.sum .masuda/images/default/ctx/
FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl git tmux openssh-server \
 && rm -rf /var/lib/apt/lists/*

# Go toolchain（masuda自身のビルドとテスト。goplsはLSP用）
RUN curl -fsSL https://go.dev/dl/go1.26.8.linux-amd64.tar.gz | tar -C /usr/local -xz \
 && ln -s /usr/local/go/bin/go /usr/local/bin/go \
 && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt

RUN mkdir -p /workspace /masuda && chown ubuntu:ubuntu /workspace /masuda
COPY --chown=ubuntu:ubuntu ctx/go.mod ctx/go.sum /tmp/modprefetch/
USER ubuntu
WORKDIR /home/ubuntu
# Claude Codeの版はinternal/guest.ClaudeCodeVersion（masudaが実機で検証した版）に合わせる。
RUN curl -fsSL https://claude.ai/install.sh | bash -s -- 2.1.287
ENV PATH=/home/ubuntu/.local/bin:/home/ubuntu/go/bin:$PATH
# GOCACHE は tmpfs の /tmp へ（ルートディスクを圧迫しないため）。モジュールキャッシュは前取りして残す
ENV GOCACHE=/tmp/go-cache
RUN cd /tmp/modprefetch && go mod download all && go install golang.org/x/tools/gopls@latest && rm -rf /tmp/modprefetch
USER root
