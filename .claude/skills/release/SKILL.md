---
name: release
description: masuda・masuda-engine・masuda-sandboxの3リポジトリに同じタグvX.Y.Zを打って1つのリリースにする手順。「リリースする」「vX.Y.Zを出す」「タグを打つ」「Releaseを作る」「配布物（tarball）を更新する」と言われたとき、または版の話にpush・タグ・GitHub Release・go.modのengineの版上げが絡むときは、作業を始める前に必ずこれを読む。打つ前の確認、順序、エージェントとユーザーの分担、公開後の確認、記録までを含む。人間が読んでそのまま手で実行できる手順書でもある。
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

- エージェント: 打つ前の確認、`go.mod`の版上げとそのコミット、ブランチの通常のpush、Actionsの監視、Releaseと添付物の検証、`install.md`をなぞる確認、追跡Issueと`HANDOFF.md`の更新
- ユーザー: 3つのタグのpush。エージェントは打つべきコマンドを、コピーしてそのまま打てる形（`! cd ... && git tag ... && git push origin vX.Y.Z`）で、**打つ直前に1つずつ**出す。前のタグのワークフローが終わってから次を出す（順序の依存があるため）

v0.1.0の初回にだけ要った作業（`redesign`→`develop`の付け替え、`main`の新設、`v1-frozen-*`のpush）は済んでいる。以後は`main`の先頭に打つ。

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
# 継続テスト（約1分。ログのcontinuation-reportの1行目が新しい版であること）
MASUDA_LIVE_TEST=1 GOWORK=off go test -count=1 -timeout 20m -v -run TestGuestSubagentContinuation ./live/
# 実機1周（15〜20分）
MASUDA_LIVE_TEST=1 GOWORK=off go test -count=1 -timeout 60m -v -run TestDevelopLapOnPythonRepo ./live/
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

遅い確認は手で回す。VMを使うものは同時に走らせない。1-0で継続テストと1周を今の定数で通していれば、liveの2行は回し直さなくてよい。

```sh
# masuda-sandboxの契約テスト（実VM。serveを起動した状態で。向こうのdocs/release.md「タグを打つ前に」）
cd ../masuda-sandbox && pnpm build && node dist/cli.js serve --socket "$XDG_RUNTIME_DIR/masuda-sandbox.sock" &
MASUDA_SANDBOX_SOCKET="$XDG_RUNTIME_DIR/masuda-sandbox.sock" pnpm test:contract
# masudaの継続テスト（約1分）→実機1周（15〜20分）。同じserveを使う。短いほうを先に回して早く落とす
cd ../masuda && MASUDA_LIVE_TEST=1 GOWORK=off go test -count=1 -timeout 20m -v -run TestGuestSubagentContinuation ./live/
MASUDA_LIVE_TEST=1 GOWORK=off go test -count=1 -timeout 60m -v -run TestDevelopLapOnPythonRepo ./live/
# sandboxのtarball予行（distを一時的に版付きで作る。終わったらpnpm buildでdevに戻す）
cd ../masuda-sandbox && cp package.json /tmp/package.json.bak && MASUDA_SANDBOX_VERSION=vX.Y.Z pnpm build \
  && npm pkg set version=X.Y.Z && npm pack --pack-destination /tmp && cp /tmp/package.json.bak package.json \
  && npm install -g /tmp/masuda-sandbox-X.Y.Z.tgz && masuda-sandbox --version && npm uninstall -g masuda-sandbox && pnpm build
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

## 4. masudaの版上げとタグ

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
- **添付物で`docs/user/install.md`をなぞる**。両方のReleaseの添付物をダウンロードし、`sha256sum -c`、`npm install -g`した`masuda-sandbox --version`、tarballのmasudaで`masuda version`が`X.Y.Z`と`contract: ok`、`masuda doctor`が全部`ok`。開発用のserveが同じソケットで動いていれば、公開物のserveは別のソケットで起動して`--sandbox-socket`で指す。確かめたら`npm uninstall -g masuda-sandbox`で外し、`~/.local/bin`のバイナリを勝手に置き換えない
- 時間があれば、公開物で`docs/user/quickstart.md`を頭から1周する（15〜20分。サブエージェントに出してよい。出力例の差し替えは作業ツリーに置いて、コミットは人間の承認後）

## 6. 記録

- 追跡Issueのチェックボックスを埋め、3タグとコミットの表、つまずいた点、残り（quickstartの1周など）を書く
- `HANDOFF.md`に、公開した版、検証したゲストのClaude Codeの版（最新版に上げられなかったならその理由とIssue）、残りを書く
- 途中で直したもの（CIの設定、doctorの判定など）は、リリースのコミットとは別の`fix`/`chore`コミットにしてある。追跡Issueからたどれるようにハッシュを書く

## つまずきやすい点

- **モジュールプロキシのキャッシュ**: `go get ...@main`は古いコミットを返すことがある。タグ直後は`GOPROXY=direct`を付ける
- **GitHubのランナー（Ubuntu 24.04）**: AppArmorが非特権ユーザー名前空間を禁じているため、フェイクsandboxの`unshare -Urm`を使うテスト（C-M7・serveの特権コマンド）が落ちる。`ci.yml`・`release.yml`に`sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0`のステップがある。ランナーの像が変わったら最初に疑う
- **同じソケットに2つのserve**: 開発用のserveを動かしたまま公開物を確かめるときは、ソケットを分ける。データディレクトリも同じものを2つのmasuda serveで共有しない
- **`docs.yml`のengineの文書**: サイトはmasuda-engineの既定ブランチ（`main`）から`workflow-schema.md`を取り込む。engineの`main`の先頭がタグと同じ時点で打てば一致する
- **sandboxの契約テストと資産ストア**: 契約テストはサービス稼働中に共有の資産ストアを書き換える（masuda-sandbox #5）。liveと同時に走らせない
