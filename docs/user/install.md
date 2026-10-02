# 導入

masudaは2つの常駐プロセスで動く。

- `masuda-sandbox serve`: VM（QEMU）を作り、中でコマンドを動かし、VMから外への通信を見張る
- `masuda serve`: ワークフローを進め、ゲートや質問を人間に出し、gitへの反映を行う。`masuda`の各コマンドはこれに話しかける

どちらもsudo無しで動く。Claude Codeはホストではなく、VMのイメージの中に入る。

## 前提

| | Linux x86_64（WSL2を含む） | macOS arm64 |
|---|---|---|
| 仮想化 | QEMU + KVM（`/dev/kvm`） | QEMU + HVF |
| パッケージ | `sudo apt install qemu-system-x86 qemu-utils lz4` | `brew install qemu node` |
| Node | 22.19以上 | 22.19以上（上のbrewで入る） |
| pnpm | `masuda-sandbox`のビルドに使う | 同左 |
| Docker | VMのイメージのビルドに使う。sudo無しで`docker`を叩けること | Docker Desktop等 |
| git | 必須 | 必須 |
| Go | 1.26.3以上（`masuda`をソースからビルドするため） | 同左 |
| 検証状況 | 実機で1周を確認済み（WSL2 Ubuntu 24.04） | **未検証** |

!!! warning "macOSは未検証"
    macOS arm64は設計上の対応先だが、まだ実機で1周させていない。手順は同じになるはずだが、動かなかった場合はIssueで知らせてほしい。

!!! note "Nodeの版"
    VMを動かすGondolinには、Node 24.17以上で外への通信が502になる既知の問題（Gondolin #134）がある。22.19以上・24.17未満を使うのが無難。

### KVMを使えるか確かめる（Linux）

```sh
ls -l /dev/kvm
# crw-rw---- 1 root kvm 10, 232 ... /dev/kvm
id -nG | grep -w kvm
```

`/dev/kvm`が無ければ、BIOSの仮想化支援（Intel VT-x・AMD-V）か、WSL2の入れ子の仮想化が無効になっている。自分が`kvm`グループに入っていなければ`sudo usermod -aG kvm "$USER"`のあとログインし直す。

## 取得とビルド

masudaは3つのリポジトリからなる。まだリリース物が無いので、同じディレクトリに並べてチェックアウトし、ソースからビルドする（`masuda`は隣の`masuda-engine`をソースとして取り込む）。

```sh
mkdir -p ~/src && cd ~/src
git clone https://github.com/TadahiroYamamura/masuda.git
git clone https://github.com/TadahiroYamamura/masuda-engine.git
git clone https://github.com/TadahiroYamamura/masuda-sandbox.git
```

`masuda-sandbox`（TypeScript）:

```sh
cd ~/src/masuda-sandbox
pnpm install && pnpm build
```

`dist/cli.js`ができる。以下では`masuda-sandbox`と書くが、実体は`node ~/src/masuda-sandbox/dist/cli.js`。毎回打つのが面倒なら、シェルの設定に`alias masuda-sandbox='node ~/src/masuda-sandbox/dist/cli.js'`を足す。

`masuda`（Go）:

```sh
cd ~/src/masuda
go build -o ~/.local/bin/masuda ./cmd/masuda
masuda version
```

`~/.local/bin`がPATHに入っていなければ足す。

## 起動

ターミナルを2つ使う（またはtmux等で両方を常駐させる）。先に`masuda-sandbox serve`を起動する。

```sh
# 1つめ
masuda-sandbox serve --socket "$XDG_RUNTIME_DIR/masuda-sandbox.sock"
```

```sh
# 2つめ
masuda serve
# masuda: serving on /run/user/1000/masuda.sock
```

- `masuda serve`は既定で`$XDG_RUNTIME_DIR/masuda.sock`で待ち受け、`$XDG_RUNTIME_DIR/masuda-sandbox.sock`の`masuda-sandbox serve`に繋ぐ。状態は`~/.local/share/masuda/`（`$XDG_DATA_HOME/masuda`）に置く
- `masuda-sandbox serve`には既定のソケットが無いので、`--socket`を必ず渡す
- `XDG_RUNTIME_DIR`が設定されていない環境（WSL2で起こる）では、masudaは代わりに`/tmp/masuda-<uid>/`を使う。その場合は`masuda-sandbox serve --socket /tmp/masuda-$(id -u)/masuda-sandbox.sock`のように同じ場所を渡す（先にそのディレクトリを作っておく）
- 初めてVMを起動するとき、Gondolinがカーネル等の資産をダウンロードする

止めるときは、どちらもCtrl-C。`masuda serve`を止めると動いていたワークスペースは「止めた（stopped）」扱いになり、自動では再開しない（[運用](operations.md#serve-restart)）。

## Claudeのトークンを用意する {#claude-token}

VMの中のClaude Codeは、あなたのClaudeのサブスクリプションで動く。そのためのOAuthトークンを作っておく。

```sh
claude setup-token
```

`claude setup-token`は、Claude Codeが入っているマシンならどこで実行してもよい（ホストに入れていなければ、一時的に入れるか、別のマシンで作る）。出てきたトークン（`sk-ant-oat01-`で始まる）を控えておく。

トークンは対象リポジトリごとにmasudaへ登録する。登録は[はじめての1周](quickstart.md)の中で行う。

```sh
masuda secret set CLAUDE_CODE_OAUTH_TOKEN   # 対象リポジトリのトップで。値は入力を求められる（画面に出ない）
```

トークンの本物の値はホストの`~/.local/share/masuda/secrets/`にだけ置かれ、VMには入らない（[概念](concepts.md#secrets)）。

次は[はじめての1周](quickstart.md)。
