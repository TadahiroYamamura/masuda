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
#
# ビルド・テストのキャッシュについて:
# - VMのルートディスクは小さい（既定はsettings.jsonの`images.<entry>.diskMiB`、4096MiB）。
#   GOCACHE・pipやnpmのキャッシュなど大きくなるものは、tmpfsの/tmpへ向けると収まりやすい
# - このファイルのENVはVMの中のプロセスに引き継がれない。キャッシュの場所はsettings.jsonで
#   渡す: チェックはコマンドの頭に（例 "checks": {"test": "GOCACHE=/tmp/go-cache go test ./..."}）、
#   エージェントには"claudeSettings": {"env": {"GOCACHE": "/tmp/go-cache"}}
# - 実行中に外へ出られるのは許可したホストだけなので、依存はここで取っておく。Goのモジュールは
#   非rootで`go mod download -modcacherw all`のように取る。`-modcacherw`を付けないとモジュール
#   キャッシュが読み取り専用になり、イメージのビルド（masuda image build）が権限エラーで失敗する

# ubuntu（uid 1000）はubuntu:24.04に最初からある。
RUN mkdir -p /workspace /masuda && chown ubuntu:ubuntu /workspace /masuda
USER ubuntu
WORKDIR /home/ubuntu
RUN curl -fsSL https://claude.ai/install.sh | bash
ENV PATH=/home/ubuntu/.local/bin:$PATH
USER root
