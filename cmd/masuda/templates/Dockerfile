# masudaのゲストイメージ。`masuda image build`（または`masuda run`の起動時）にsandbox serviceが
# Dockerでビルドし、VMのrootfsにする。カーネルと起動層はsandbox側の資産で、ここには含めない。
#
# masudaが要るもの: Claude Code（native）・tmux・git・openssh-server・ca-certificates、
# uid 1000の`ubuntu`、ubuntu所有の/workspaceと/masuda。対象リポジトリのビルド・テストに
# 要るツール（言語のツールチェーン等）は下の「ここに足す」に書く。
FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl git tmux openssh-server \
 && rm -rf /var/lib/apt/lists/*

# ここに足す（例: RUN apt-get update && apt-get install -y --no-install-recommends golang-go）

# ubuntu（uid 1000）はubuntu:24.04に最初からある。
RUN mkdir -p /workspace /masuda && chown ubuntu:ubuntu /workspace /masuda
USER ubuntu
WORKDIR /home/ubuntu
RUN curl -fsSL https://claude.ai/install.sh | bash
ENV PATH=/home/ubuntu/.local/bin:$PATH
USER root
