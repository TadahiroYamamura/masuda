# 導入

masudaは2つの常駐プロセスで動く。

- `masuda-sandbox serve`: VM（QEMU）を作り、中でコマンドを動かし、VMから外への通信を見張る
- `masuda serve`: ワークフローを進め、ゲートや質問を人間に出し、gitへの反映を行う。`masuda`の各コマンドはこれに話しかける

どちらもsudo無しで動く。Claude Codeはホストではなく、VMのイメージの中に入る。

配布物は[GitHub Release](https://github.com/TadahiroYamamura/masuda/releases)に添付したtarballだけで、npmレジストリやパッケージマネージャーには公開していない。**masudaとmasuda-sandboxは同じバージョンを組で入れる**（違うと`masuda serve`が起動しない）。

## 前提

| | Linux x86_64（WSL2を含む） | macOS arm64（実験的） |
|---|---|---|
| 仮想化 | QEMU + KVM（`/dev/kvm`） | QEMU + HVF |
| パッケージ | `sudo apt install qemu-system-x86 qemu-utils lz4` | `brew install qemu node@22` |
| Node | 22.19以上（npmを含む） | 22.19以上 |
| Docker | VMのイメージのビルドに使う。sudo無しで`docker`を叩けること | Docker Desktop等 |
| git | 必須 | 必須 |
| 検証状況 | 実機で1周を確認済み（WSL2 Ubuntu 24.04） | **未検証** |

!!! warning "macOSは実験的"
    macOS arm64は設計上の対応先で、Releaseにもバイナリを添付しているが、まだ実機で1周させていない。手順は同じになるはずだが、動かなかった場合はIssueで知らせてほしい。

!!! note "Nodeの版"
    VMを動かすGondolinには、Node 24.17以上で外への通信が502になる既知の問題（Gondolin #134）がある。22.19以上・24.17未満を使うのが無難。

### KVMを使えるか確かめる（Linux）

```sh
ls -l /dev/kvm
# crw-rw---- 1 root kvm 10, 232 ... /dev/kvm
id -nG | grep -w kvm
```

`/dev/kvm`が無ければ、BIOSの仮想化支援（Intel VT-x・AMD-V）か、WSL2の入れ子の仮想化が無効になっている。自分が`kvm`グループに入っていなければ`sudo usermod -aG kvm "$USER"`のあとログインし直す。

## 入れる

以下では入れるバージョンを`VERSION`に置く（[Releaseの一覧](https://github.com/TadahiroYamamura/masuda/releases)の最新。先頭の`v`は付けない）。

```sh
VERSION=0.1.0
```

### masuda-sandbox

```sh
mkdir -p /tmp/masuda-sandbox-$VERSION && cd /tmp/masuda-sandbox-$VERSION
BASE=https://github.com/TadahiroYamamura/masuda-sandbox/releases/download/v$VERSION
curl -fLO "$BASE/masuda-sandbox-$VERSION.tgz"
curl -fLO "$BASE/SHA256SUMS"
sha256sum -c SHA256SUMS        # macOS: shasum -a 256 -c SHA256SUMS
npm install -g "./masuda-sandbox-$VERSION.tgz"
masuda-sandbox --version       # X.Y.Z
```

`npm install -g`が権限で失敗するなら、npmのグローバルの置き場所を自分のディレクトリにする（`npm config set prefix ~/.local`。`~/.local/bin`をPATHに入れる）。sudoは使わない。

### masuda

```sh
mkdir -p /tmp/masuda-$VERSION && cd /tmp/masuda-$VERSION
case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) TARGET=linux_amd64 ;;
  Darwin/arm64) TARGET=darwin_arm64 ;;
  *) echo "このプラットフォーム向けの配布物は無い" >&2 ;;
esac
BASE=https://github.com/TadahiroYamamura/masuda/releases/download/v$VERSION
curl -fLO "$BASE/masuda_${VERSION}_${TARGET}.tar.gz"
curl -fLO "$BASE/SHA256SUMS"
grep " masuda_${VERSION}_${TARGET}.tar.gz\$" SHA256SUMS | sha256sum -c -   # macOS: shasum -a 256 -c -
tar -xzf "masuda_${VERSION}_${TARGET}.tar.gz"
mkdir -p ~/.local/bin
install -m 0755 "masuda_${VERSION}_${TARGET}/masuda" ~/.local/bin/masuda
masuda version
```

`~/.local/bin`がPATHに入っていなければ足す。`SHA256SUMS`にはそのリリースの全部の添付物が載っているので、`grep`で自分のものの行だけを確かめる。

### 確かめる

```sh
masuda doctor
```

QEMU・KVM（macOSはHVF）・Node・Docker・gitと、`masuda-sandbox serve`への到達、Claudeのトークンを1項目ずつ確かめ、足りないものに直し方を添える。この時点では`masuda-sandbox`（まだ起動していない）とClaudeトークン（まだ登録していない）がNGになる。次の「起動」と「Claudeのトークンを用意する」のあとでもう一度打ち、すべて`ok`になることを確かめる（[CLIリファレンス](cli.md#doctor)）。

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
- `masuda serve`は起動時に`masuda-sandbox serve`のバージョンと契約を確かめ、組が合わなければ起動しない。`masuda-sandbox serve`がまだ起動していなければ警告を出して起動し、`masuda run`のときにもう一度確かめる
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

トークンはユーザーごとに1回masudaへ登録すれば、どのリポジトリでも使われる。登録は[はじめての1周](quickstart.md)の中で行う（`masuda serve`が動いている必要がある）。

```sh
masuda secret set CLAUDE_CODE_OAUTH_TOKEN   # どこで打ってもよい。値は入力を求められる（画面に出ない）
```

特定のリポジトリだけ別のトークンを使うなら、そのリポジトリで`--repo .`を付けて登録する（リポジトリごとの登録が優先される）。

トークンの本物の値はホストの`~/.local/share/masuda/secrets/`にだけ置かれ、VMには入らない（[概念](concepts.md#secrets)）。

## 更新と削除

更新は、両方を**同じ新しいバージョン**で入れ直す（上の手順を新しい`VERSION`で繰り返す）。入れ直す前に`masuda serve`と`masuda-sandbox serve`を止める。ワークスペースの状態（`~/.local/share/masuda/`）はそのまま残るが、v1.0までは版をまたいだ互換性を保証しない。

削除は`npm uninstall -g masuda-sandbox`と`rm ~/.local/bin/masuda`。状態とトークンも消すなら`~/.local/share/masuda/`と`~/.config/masuda/`も消す。

masuda自体を直す人がソースからビルドする手順は[開発者向け](../design/README.md#source)にある。

次は[はじめての1周](quickstart.md)。
