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
# - /run・/root・/tmp・/var/tmp・/var/cache・/var/logは、VMの起動のたびに空になる。
#   ここに置いたもの（/var/run/postgresql、rootで入れた/root/goなど）はVMでは見えない
# - このファイルのENV（例 ENV GOCACHE=/tmp/go-cache、ENV PATH=/usr/local/go/bin:$PATH）は
#   VMの中のプロセスに引き継がれる。PATHの先頭にはいつも~/.local/bin（Claude Codeの置き場所）が
#   足される。ENVで決めていない変数は、HOME=/home/ubuntu、XDG_*_HOMEはその下になる。
#   チェックとエージェントはログインシェルで動くので、/etc/profileがPATHを置き換えるイメージ
#   （Debian等）ではENVのPATHが消える。そのときはツールを/usr/local/binに置く
# - 実行中に外へ出られるのは許可したホストだけなので、依存はここで取っておく
#   （例 Goなら非rootで`go mod download all`）

# ubuntu（uid 1000）はubuntu:24.04に最初からある。
RUN mkdir -p /workspace /masuda && chown ubuntu:ubuntu /workspace /masuda
USER ubuntu
WORKDIR /home/ubuntu
# Claude Codeの版は固定している。__CLAUDE_CODE_VERSION__はmasuda __MASUDA_VERSION__が実機で検証した版
# （masudaは各リリースでその時点の最新版を検証する）。上げるときはこの数字を変えて`masuda image build`する。
RUN curl -fsSL https://claude.ai/install.sh | bash -s -- __CLAUDE_CODE_VERSION__
ENV PATH=/home/ubuntu/.local/bin:$PATH
USER root
