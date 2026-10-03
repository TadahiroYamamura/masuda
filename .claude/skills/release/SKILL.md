---
name: release
description: masuda・masuda-engine・masuda-sandboxの3リポジトリに同じタグvX.Y.Zを打って1つのリリースにする手順。「リリースする」「vX.Y.Zを出す」「タグを打つ」「Releaseを作る」「配布物（tarball）を更新する」「ハーネスを更新する」と言われたとき、または版の話にpush・タグ・GitHub Release・go.modのengineの版上げが絡むときは、作業を始める前に必ずこれを読む。開発版との分離、打つ前の確認、順序、エージェントとユーザーの分担、公開後の確認、ハーネスの更新、記録までを含む。人間が読んでそのまま手で実行できる手順書でもある。
---

# リリース手順

masudaは3つのリポジトリ（masuda・[masuda-engine](https://github.com/TadahiroYamamura/masuda-engine)・[masuda-sandbox](https://github.com/TadahiroYamamura/masuda-sandbox)）からなり、**3つに同じタグ`vX.Y.Z`を打って**1つのリリースにする。利用者はmasudaとmasuda-sandboxを同じバージョンで組にして入れる（`docs/user/install.md`）。

- 順序は**engine→sandbox→masuda**。masudaの`release.yml`は、`go.mod`が同じタグのengineを要求していること、同じタグのmasuda-sandboxの`sandbox.proto`と契約（SHA-256）が合うことを確かめるので、先の2つが打たれていないと失敗する
- 配布物はGitHub Releaseの添付物だけ。npmレジストリには公開しない（v1.0まで）。互換性の保証もv1.0から
- macOS（darwin/arm64）は「実験的」。ビルドして添付するが、実機での確認はしていない
- タグは動かさない。打ったあとに直したいものが出たら、パッチ版を上げて打ち直す（配布物とモジュールプロキシのキャッシュはタグ名で引かれる）

| リポジトリ | タグで動くもの | Releaseの添付物 |
|---|---|---|
| masuda-engine | 無し（Goのモジュールとしてタグを引くだけ） | — |
| masuda-sandbox | `.github/workflows/release.yml`（手順は向こうの`docs/release.md`） | `masuda-sandbox-X.Y.Z.tgz`、`SHA256SUMS` |
| masuda | `.github/workflows/release.yml`と`docs.yml` | `masuda_X.Y.Z_linux_amd64.tar.gz`、`masuda_X.Y.Z_darwin_arm64.tar.gz`、`masuda-api-client-X.Y.Z.tgz`（`clients/ts`の`npm pack`）、`SHA256SUMS` |

masudaの`release.yml`は、`go vet`・`go test ./...`、`go.mod`のengineの版がタグと同じか、同じタグのmasuda-sandboxで`internal/sandboxcontract/sha.go`を生成し直して差分が無いかを確かめてから、`-ldflags "-X main.version=X.Y.Z"`でクロスビルドする。リリースノートには3リポジトリのタグとコミットハッシュの表と、検証したゲストのClaude Codeの版（`masuda version`の`claude code:`の行）を自動で書く。`docs.yml`はサイトを`X.Y`として公開し`latest`の別名を付ける。

## 分担

エージェントが進める場合、**タグのpush（公開物を作る操作）と強制pushはエージェントからは実行できない**（自動モードの安全判定が止める。別の経路で同じ結果を狙うことも禁じられている）。これは不便ではなく、取り消しの効かない一打を人間が打つ形として残す。

- エージェント: 打つ前の確認、`go.mod`の版上げとそのコミット、ブランチの通常のpush、Actionsの監視、Releaseと添付物の検証、添付物を一時的に入れる確認、追跡Issueと`HANDOFF.md`の更新
- ユーザー: 3つのタグのpush。エージェントは打つべきコマンドを、コピーしてそのまま打てる形（`! cd ... && git tag ... && git push origin vX.Y.Z`）で、**打つ直前に1つずつ**出す。前のタグのワークフローが終わってから次を出す（順序の依存があるため）。ハーネスの更新（手順6）も、人間の常用環境を入れ替える操作なのでユーザーが打つ。エージェントはコマンドを出して結果を確かめる

v0.1.0の初回にだけ要った作業（`redesign`→`develop`の付け替え、`main`の新設、`v1-frozen-*`のpush）は済んでいる。以後は`main`の先頭に打つ。

## 開発版との分離 {#dev-separation}

この節は番号の付く手順ではなく、以下の全手順に掛かる決まり。

**ハーネス**は、masuda自身の開発を回している公開物の組で、次のものを指す。

- 公開物の`~/.local/bin/masuda`と、`npm install -g`した`masuda-sandbox`
- 既定のソケット`$XDG_RUNTIME_DIR/masuda.sock`・`$XDG_RUNTIME_DIR/masuda-sandbox.sock`
- 既定のデータディレクトリ`~/.local/share/masuda`

**開発版**（チェックアウトからビルドしたもの、リリース前に確かめるもの）をハーネスに混ぜない。ワークスペースの記録の形は版で変わりうるので、開発版がハーネスのバイナリやデータディレクトリを置き換えると、ハーネスで走行中のワークスペースが読めなくなる。

- **開発版のmasuda**: チェックアウト直下の`./masuda`（`GOWORK=off go build ./cmd/masuda`の出力。`/masuda`はgitignore済み）を使い、`~/.local/bin`に置かない。開発版のserveは既定のソケットとデータディレクトリで待ち受けない

  ```sh
  ./masuda serve --socket "$XDG_RUNTIME_DIR/masuda-dev.sock" --data-dir ~/.local/share/masuda-dev \
    --sandbox-socket "$XDG_RUNTIME_DIR/masuda-sandbox.sock"   # sandbox.protoを変える作業ではmasuda-sandbox-dev.sock
  ```

  - `~/.config/masuda/config.json`はハーネスと共有で、`sandboxSocket`が書いてあれば明示しない限りそちらが使われる。開発版は`--sandbox-socket`を必ず明示する
  - 開発版のCLIは毎回`--socket "$XDG_RUNTIME_DIR/masuda-dev.sock"`を付ける（既定はハーネスの`masuda.sock`）
  - 秘密はデータディレクトリに置かれるので、開発版のserveにはトークンを別に登録する: `./masuda secret set CLAUDE_CODE_OAUTH_TOKEN --socket "$XDG_RUNTIME_DIR/masuda-dev.sock"`（値は標準入力から）
- **sandbox**: 普段は共有（ハーネスの`$XDG_RUNTIME_DIR/masuda-sandbox.sock`）で、開発版のmasudaの`--sandbox-socket`もそこを指してよい。`masuda version`の`contract: ok`はハーネスのmasudaと共有のsandboxの組で見る。`sandbox.proto`を変える作業では、開発版の`masuda-sandbox serve`（`../masuda-sandbox`の`node dist/cli.js serve`）を`$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock`で立て、開発版のmasudaはそちらを指す
- **masuda自身の`.masuda/`**: 入っているハーネスの版で読める範囲に留める。新しい設定・スキーマは、その版をハーネスにしてから使う
- **gate・questionの操作**: serveとCLIは同じ版の組で使う。ハーネスのserveのgate・questionはハーネスのCLI（`masuda`）で操作し、開発版のCLI（`./masuda`）をハーネスのserveに向けない。開発版のserveは開発版のCLIで操作する
- **liveテスト（`live/`）**: VMの中では回せないので、ホストで開発版として回す。liveは既定で共有のsandboxに繋ぐ。`sandbox.proto`が変わる作業とリリース前の確認（手順1-0・1-2）では、`MASUDA_SANDBOX_SOCKET="$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock"`で開発版のsandboxを指す

## 0. 版と追跡Issueを決める

- 版は`vX.Y.Z`。v1.0までは互換性を保証しないので、機能追加でもYを上げてよい。契約（`masuda.proto`・`sandbox.proto`・`guest-protocol.md`）が変わったときは必ずYを上げる
- 追跡Issueを1つ作る（v0.1.0は#59）。この手順の各段をチェックボックスで書き、進めながら更新する。つまずいた点はそのIssueに書き、別の不具合は別Issueに切る
- 3リポジトリとも、打つコミットがpush済みで作業ツリーがcleanであること。masudaは`develop`に全部入っていて、`main`を`develop`に合わせてから打つ
- ゲストのClaude Codeの版。既定は「その時点の最新版」（手順1-0）。追跡Issueに、上げる前の版・最新版・検証した版を書く

## 1. 打つ前の確認

### 1-0. ゲストのClaude Codeの版を上げる

ゲストのClaude Codeの版は`internal/guest`の`ClaudeCodeVersion`に固定してあり、`masuda init`の雛形のDockerfile・liveのDockerfile・`masuda version`の表示がこれを使う。各リリースは「その時点の最新のClaude Codeで動く」を既定にする。版はmasudaの外で毎日のように上がり、サブエージェントの継続（SendMessage）やフックの形が変わると無人の周回が壊れるため、上げるたびに実機で確かめてから打つ。

最新版を知る（`install.sh`が引数なしで入れる版と同じURLから引く）。

```sh
.claude/skills/release/scripts/claude-code-latest.sh   # 例 2.1.288
```

`internal/guest/guest.go`の定数をその版にし、短いものから順に回す（落ちるなら早く落とす。VMを使うものは同時に走らせない）。

```sh
GOWORK=off go test -count=1 ./cmd/masuda/ ./internal/guest/ ./live/
.claude/skills/release/scripts/precheck.sh --claude-code    # 版が雛形・liveに入り、配布元に実在する
# 開発版のsandboxを立てる（liveは既定でハーネスのsandboxに繋ぎ、sandbox.protoが変わるリリースでは契約が合わず起動しない）
(cd ../masuda-sandbox && pnpm build && exec node dist/cli.js serve --socket "$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock") & sbpid=$!
# 継続テスト（約1分。ログのcontinuation-reportの1行目が新しい版であること）
MASUDA_SANDBOX_SOCKET="$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock" MASUDA_LIVE_TEST=1 GOWORK=off go test -count=1 -timeout 20m -v -run TestGuestSubagentContinuation ./live/
# 実機1周（15〜20分）
MASUDA_SANDBOX_SOCKET="$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock" MASUDA_LIVE_TEST=1 GOWORK=off go test -count=1 -timeout 60m -v -run TestDevelopLapOnPythonRepo ./live/
```

liveのイメージはDockerfileが変われば作り直される（初回は数分余計にかかる）。この2つは下の「遅い確認」のliveを兼ねる（版を上げなかったときも下で同じ順に回す）。

- **通れば**（既定）: `chore(guest): Claude Codeを<版>に上げる`の1コミットにする。本文の`## 意図`に、上げる前の版と、継続テスト・1周が通ったことを書く。`develop`へpushする
- **通らなければ**: 定数を前の版に戻す（コミットしない）。追跡Issueと、公開後にReleaseのノートへ「最新版 X では〜が動かないため Y で検証」と書く。原因は別Issueに切る（再現手順・落ちたテスト・ログの該当行）。前の版で両方が通ることは下の「遅い確認」で確かめる

### 1-1. 速い確認

速い確認は`scripts/precheck.sh vX.Y.Z`にまとめてある。masudaのチェックアウトから打つ（隣に`../masuda-engine`と`../masuda-sandbox`がある前提。無ければ`MASUDA_ENGINE_DIR`・`MASUDA_SANDBOX_DIR`で場所を渡す）。

```sh
.claude/skills/release/scripts/precheck.sh vX.Y.Z
```

確かめること: 3リポジトリの作業ツリーがcleanでoriginと一致、`vX.Y.Z`のタグが未使用、masudaの`GOWORK=off`でのbuild・vet・test（契約テストC-M*を含む）、ゲストのClaude Codeの版（`--claude-code`と同じ段。最新版と違えばinfoで出す）、engineのtest、sandboxの単体テスト、`internal/sandboxcontract/sha.go`の再生成に差分が無いこと、`release.yml`と同じクロスビルドで`masuda version`が版を出すこと、`clients/ts`の`npm pack --dry-run`。

`go.work`があるとテストは隣の`../masuda-engine`の作業ツリーで走る。固定した版で確かめたいので、スクリプトは`GOWORK=off`を付ける。

### 1-2. 遅い確認

遅い確認は手で回す。VMを使うものは同時に走らせない（ハーネスで走っているワークスペースも含む。契約テストは資産ストアを書き換え、開発版とハーネスのsandboxが資産ストアを共有するかはこのリポジトリからは確かめられない）。1-0で継続テストと1周を今の定数で通していれば、liveの2行は回し直さなくてよい。1-0で立てた開発版のsandboxが動いていれば、sandboxの起動行は打たない（同じソケットに2つ立てない）。

```sh
# masuda-sandboxの契約テスト（実VM。serveを起動した状態で。向こうのdocs/release.md「タグを打つ前に」）
(cd ../masuda-sandbox && pnpm build && exec node dist/cli.js serve --socket "$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock") & sbpid=$!
(cd ../masuda-sandbox && MASUDA_SANDBOX_SOCKET="$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock" pnpm test:contract)
# masudaの継続テスト（約1分）→実機1周（15〜20分）。同じserveを使う。短いほうを先に回して早く落とす
cd ../masuda && MASUDA_SANDBOX_SOCKET="$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock" MASUDA_LIVE_TEST=1 GOWORK=off go test -count=1 -timeout 20m -v -run TestGuestSubagentContinuation ./live/
MASUDA_SANDBOX_SOCKET="$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock" MASUDA_LIVE_TEST=1 GOWORK=off go test -count=1 -timeout 60m -v -run TestDevelopLapOnPythonRepo ./live/
# 開発版のsandboxを止める（下の予行のpnpm buildが走行中のserveのdistを作り直さないように。以後は使わない）
kill "$sbpid"
# sandboxのtarball予行（distを一時的に版付きで作る。途中で失敗しても、サブシェルを抜けるときにtrapがpackage.jsonを戻し、pnpm buildでdistをdevに戻す）
# ハーネスのグローバルのmasuda-sandboxを上書きしないよう--prefixで入れる。置き場はmktempの自分専用のディレクトリ（共有の/tmpの予測できる名前に置いたものは実行しない）
t=$(mktemp -d)
(
  set -e
  cd ../masuda-sandbox
  cp package.json "$t/package.json.bak"
  trap 'cp "$t/package.json.bak" package.json; pnpm build' EXIT
  MASUDA_SANDBOX_VERSION=vX.Y.Z pnpm build
  npm pkg set version=X.Y.Z
  npm pack --pack-destination "$t"
  npm install -g --prefix "$t/prefix" "$t/masuda-sandbox-X.Y.Z.tgz"
  "$t/prefix/bin/masuda-sandbox" --version
)
rm -rf "$t"
pgrep -af qemu-system   # 孤児が無いこと
```

liveが完走したら、ログに2分以上の無反応が無いかも見る（#65の停止は1周に2回ほど出る。完走を妨げないが、増えていたら記録する）。

## 2. masuda-engineのタグ

engineには`main`しか無く、タグで動くワークフローも無い。ユーザーが打つ。

```sh
! cd ~/work/masuda-engine && git switch main && git pull --ff-only && git log --oneline -1 && git tag -a vX.Y.Z -m "masuda-engine vX.Y.Z" && git push origin vX.Y.Z
```

打てたら、モジュールプロキシが知るまで少し待つ。`curl -s https://proxy.golang.org/github.com/!tadahiro!yamamura/masuda-engine/@v/vX.Y.Z.info`がJSONを返せばよい（数分かかることがある。手順4では待たずに`GOPROXY=direct`で取れる）。

## 3. masuda-sandboxのタグ

ユーザーが打つ。pushで向こうの`release.yml`が走る。

```sh
! cd ~/work/masuda-sandbox && git switch main && git pull --ff-only && git log --oneline -1 && git tag -a vX.Y.Z -m "masuda-sandbox vX.Y.Z" && git push origin vX.Y.Z
```

エージェントは完走と添付物を確かめる。

```sh
scripts/gh.sh run list -R TadahiroYamamura/masuda-sandbox --workflow release --limit 1
scripts/gh.sh run watch -R TadahiroYamamura/masuda-sandbox <run-id> --exit-status
scripts/gh.sh release view vX.Y.Z -R TadahiroYamamura/masuda-sandbox --json assets -q '.assets[].name'   # tgzとSHA256SUMS
```

## 4. masudaの版上げとタグ {#engine}

まず`go.mod`のengineを、いま打ったタグに上げる（エージェント）。

```sh
GOWORK=off GOPROXY=direct go get github.com/TadahiroYamamura/masuda-engine@vX.Y.Z
GOWORK=off go mod tidy
GOWORK=off go test -count=1 ./...
go generate ./internal/sandboxcontract/ && git diff --exit-code -- internal/sandboxcontract/
git commit -am "chore(deps): masuda-engineをvX.Y.Zに上げる"   # ## 意図: release.ymlがタグと同じ版を要求する
git push origin develop
```

- 開発中は`go.mod`がengineの`main`の擬似バージョン（`v0.0.0-<日時>-<コミット>`）を要求していることがある。リリースでは必ずタグにする（`release.yml`がタグと違えば止まる）
- `develop`のciが緑になるのを見てから`main`を合わせる。`main`は`develop`のfast-forwardであること（`git merge-base --is-ancestor main develop`）。違えば先に`develop`へ取り込む

```sh
git branch -f main develop && git push origin main
scripts/gh.sh run list --branch main --limit 3   # ci・docsが緑
```

タグはユーザーが打つ。

```sh
! cd ~/work/masuda && git tag -a vX.Y.Z -m "masuda vX.Y.Z" main && git push origin vX.Y.Z
```

エージェントは`release`と`docs`の2つのワークフローを待つ。

```sh
scripts/gh.sh run list --limit 4
scripts/gh.sh run watch <release-run-id> --exit-status
scripts/gh.sh run watch <docs-run-id> --exit-status
```

## 5. 公開後の確認

- Releaseのノートの表（masuda・masuda-engine・masuda-sandboxのタグとコミット）が、手順2〜4で打った3つのコミットと一致する。`scripts/gh.sh release view vX.Y.Z --json body,assets`と、各リポジトリの`git rev-parse vX.Y.Z^{commit}`で突き合わせる
- 添付物が4つ（tarball2種、`clients/ts`のtgz、`SHA256SUMS`）
- Releaseのノートに「ゲストのClaude Code: X で実機検証した」の行があり、Xが1-0で検証した版と同じ（`release.yml`が`masuda version`の表示から書く）。最新版で通らず前の版に留めたときは、`scripts/gh.sh release edit vX.Y.Z --notes-file <file>`でその理由（「最新版 X では〜が動かないため Y で検証」と別Issueの番号）を書き足す
- ドキュメントサイト: `curl -s https://tadahiroyamamura.github.io/masuda/versions.json`に`X.Y`があり、aliasに`latest`が付いている。`/latest/`と`/X.Y/`が200
- **添付物を一時的に入れて確かめる**。`mktemp -d`の自分専用のディレクトリ`$t`に両方のReleaseの添付物を落として`sha256sum -c`し、そこに入れた`masuda-sandbox`とtarballのmasudaで、`version`が`X.Y.Z`と`contract: ok`、`doctor`が全部`ok`（Claudeトークンの項目は一時データディレクトリでは未登録になるので、手順6で確かめる）。ソケットとデータディレクトリをすべて`$t`の下に置くので、ハーネスや開発版のソケットとぶつからず、`config.json`の`sandboxSocket`にも引かれない。ハーネス（`~/.local/bin`・グローバルの`masuda-sandbox`・既定のソケットとデータディレクトリ）には触れない

  ```sh
  t=$(mktemp -d) && cd "$t"
  curl -fLO https://github.com/TadahiroYamamura/masuda-sandbox/releases/download/vX.Y.Z/masuda-sandbox-X.Y.Z.tgz
  curl -fL -o SHA256SUMS.sandbox https://github.com/TadahiroYamamura/masuda-sandbox/releases/download/vX.Y.Z/SHA256SUMS
  curl -fLO https://github.com/TadahiroYamamura/masuda/releases/download/vX.Y.Z/masuda_X.Y.Z_linux_amd64.tar.gz
  curl -fL -o SHA256SUMS.masuda https://github.com/TadahiroYamamura/masuda/releases/download/vX.Y.Z/SHA256SUMS
  sha256sum -c SHA256SUMS.sandbox && grep ' masuda_X.Y.Z_linux_amd64.tar.gz$' SHA256SUMS.masuda | sha256sum -c -
  npm install -g --prefix "$t/prefix" "$t/masuda-sandbox-X.Y.Z.tgz" && "$t/prefix/bin/masuda-sandbox" --version
  tar -xzf masuda_X.Y.Z_linux_amd64.tar.gz && cp masuda_X.Y.Z_linux_amd64/masuda "$t/masuda"
  "$t/prefix/bin/masuda-sandbox" serve --socket "$t/sandbox.sock" &
  "$t/masuda" version --sandbox-socket "$t/sandbox.sock"   # X.Y.Zとcontract: ok
  "$t/masuda" doctor --sandbox-socket "$t/sandbox.sock" --data-dir "$t/data"
  pkill -f "$t/sandbox.sock"; cd - && rm -rf "$t"
  ```

- 時間があれば、公開物で`docs/user/quickstart.md`を頭から1周する（15〜20分。サブエージェントに出してよい。出力例の差し替えは作業ツリーに置いて、コミットは人間の承認後）

## 6. ハーネスの更新 {#harness}

公開物をハーネスとして本導入する。ユーザーが打つ（人間の常用環境を入れ替える操作のため）。エージェントはコマンドを出して結果を確かめる。初回はv0.2.0の公開後で、`docs/user/install.md`を実機で検証する役割も兼ねる。install.mdと食い違えば、install.mdを直す別コミットにするか別Issueに切る。

1. **更新前の確認**: ハーネスのserveで`masuda list --all`し、走行中のものは終わらせ、stoppedも含めてすべて`masuda remove`する。記録の形が変わると走行中のものが読めなくなり、版をまたぐresumeも保証されないため（監督の判断）。残したい成果はremoveの前に取り出しておく
    - 初回（ハーネスがまだ無い）は、確かめる先のserveが無いのでこの確認は飛ばす。既定のソケットで動いている開発版のsandbox（ソースから起こしたもの）があれば止め、以後の開発版のsandboxは`masuda-sandbox-dev.sock`で起こす
    - 初回は既定のデータディレクトリに既にある中身（M4暫定の`claude-oauth-token`、過去の記録）を消さない。`claude-oauth-token`はliveが読むため残す（新しく置かないだけ）。過去の記録が起動後の`masuda list --all`に出れば、上と同じに扱う
2. **install.mdをなぞる**: 動いている`masuda serve`→`masuda-sandbox serve`の順に止め（どちらもCtrl-C）、`docs/user/install.md`の「入れる」の`VERSION=`だけを`VERSION=X.Y.Z`（`v`は付けない）に差し替えて、「入れる」（masuda-sandbox・masuda）と「起動」のブロックをそのまま打つ。install.mdを実機で検証する役割を兼ねるので、ほかは書き換えない

3. **Claudeのトークン**: `masuda secret set CLAUDE_CODE_OAUTH_TOKEN`で正規の置き場所（`<DataDir>/secrets/_user/`）に登録する（登録済みなら不要。`docs/user/install.md`の「Claudeのトークンを登録する」）。M4暫定の`<DataDir>/claude-oauth-token`は読めるが、新しく置かない
4. **確かめる**: `masuda version`で`X.Y.Z`と`contract: ok`、`masuda doctor`が全部`ok`
5. **イメージの作り直し**: ハーネスで使うリポジトリ（masuda自身を含む）で、Dockerfileのinstall行のClaude Codeの版が`masuda version`の`claude code:`と違えば合わせ（masuda自身の`.masuda/images/default/Dockerfile`なら`chore(masuda)`の1コミット）、`masuda image build`する。イメージの記録はデータディレクトリごとで、初回はハーネスのデータディレクトリにまだ無いため必ず打つ

## 7. 記録

- 追跡Issueのチェックボックスを埋め、3タグとコミットの表、つまずいた点、残り（quickstartの1周など）を書く
- 追跡Issueに、ハーネスを更新したか（したなら版、しなかったなら理由）を書く
- `HANDOFF.md`に、公開した版、検証したゲストのClaude Codeの版（最新版に上げられなかったならその理由とIssue）、残りを書く
- 途中で直したもの（CIの設定、doctorの判定など）は、リリースのコミットとは別の`fix`/`chore`コミットにしてある。追跡Issueからたどれるようにハッシュを書く

## つまずきやすい点

- **モジュールプロキシのキャッシュ**: `go get ...@main`は古いコミットを返すことがある。タグ直後は`GOPROXY=direct`を付ける
- **GitHubのランナー（Ubuntu 24.04）**: AppArmorが非特権ユーザー名前空間を禁じているため、フェイクsandboxの`unshare -Urm`を使うテスト（C-M7・serveの特権コマンド）が落ちる。`ci.yml`・`release.yml`に`sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0`のステップがある。ランナーの像が変わったら最初に疑う
- **同じソケットに2つのserve**: 既定のソケットとデータディレクトリはハーネスが使う。確かめるもの・開発版のserveは別のソケットとデータディレクトリで立てる。データディレクトリを2つのmasuda serveで共有しない（「開発版との分離」）
- **`docs.yml`のengineの文書**: サイトはmasuda-engineの既定ブランチ（`main`）から`workflow-schema.md`を取り込む。engineの`main`の先頭がタグと同じ時点で打てば一致する
- **sandboxの契約テストと資産ストア**: 契約テストはサービス稼働中に共有の資産ストアを書き換える（masuda-sandbox #5）。liveとも、ハーネスで走っているワークスペースとも同時に走らせない
