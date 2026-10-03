# 設計ドキュメント（再設計版）

masudaは2026-10-02にゼロから再設計した。ここにあるのは**再設計後の**設計で、現在形で書く。再設計に至った経緯とスパイクの結果は[redesign-background.md](redesign-background.md)にまとめてあり、それ以外のファイルは経緯を書かない。

判断の理由は各コミットメッセージの`## 意図`・`## 設計上の考慮点`に残す。ADRは書かない（決定履歴の恒久的な置き場所は未決）。

## 読む順

1. [overview.md](overview.md) — 全体設計。部品・契約・データの流れ・不変条件・脅威モデル。最初に読む
2. [contracts.md](contracts.md) — 3つの契約の所在と、変更の手続き
3. [redesign-background.md](redesign-background.md) — なぜ作り直したか、何を実機で確かめたか

## リポジトリ

| リポジトリ | 言語 | 役割 |
|---|---|---|
| [masuda-sandbox](https://github.com/TadahiroYamamura/masuda-sandbox) | TypeScript | Gondolinを包む常駐サービス。VMの作成・実行・方針切替・秘密・転送 |
| [masuda-engine](https://github.com/TadahiroYamamura/masuda-engine) | Go | ワークフロー定義の読み込み・検査・実行。ライブラリ |
| masuda（このリポジトリ） | Go | CLI・公開API・ワークスペースとstaging・2つを結線する統合層 |

各リポジトリの作業単位は、それぞれの`docs/work-orders.md`にある。

## 開発環境

masuda自体を直す人向け。masudaを**使う**手順は[利用者向け](../user/index.md)。

- Go 1.26以上。`go build ./...`・`go vet ./...`・`go test ./...`
- CI（`.github/workflows/ci.yml`）は`main`・`develop`へのpushとPRで、build・vet・test（契約テストを含む）と、`internal/sandboxcontract/sha.go`が`masuda-sandbox`の`main`の`sandbox.proto`と合っていることを確かめる。リリースは[リリース手順](release.md)
- [masuda-engine](https://github.com/TadahiroYamamura/masuda-engine)は`go.mod`で版（タグ、無い間は`main`の擬似バージョン）に固定している。隣の`../masuda-engine`の作業中のコードで試すときは、gitignoreした`go.work`を作る: `go work init . && go work use ../masuda-engine`（固定した版で確かめるときは`GOWORK=off`）。版の上げ方は[リリース手順](release.md#engine)
- protoを変えたら`buf generate`で`gen/`を作り直してコミットする。`buf`が無ければ`go run github.com/bufbuild/buf/cmd/buf@latest generate`。sandbox APIのクライアントは`../masuda-sandbox/proto`から生成するので、[masuda-sandbox](https://github.com/TadahiroYamamura/masuda-sandbox)も隣にチェックアウトしておく。そのときは`go generate ./internal/sandboxcontract/`で`internal/sandboxcontract/sha.go`（sandbox.protoのSHA-256）も作り直す
- 契約テスト: `go test ./contract/`（例 `go test ./contract/ -run TestCM1`）。フェイクのsandbox（`masuda serve --fake-sandbox`、VMなし）とengineの実物で公開APIを叩く。各作業単位の完了は契約テストが緑であることで判定する（[contracts.md](contracts.md)）
- 実機テスト: `MASUDA_LIVE_TEST=1 go test -count=1 -timeout 60m -v ./live/`。実際の`masuda-sandbox serve`とClaudeのトークンが要る（前提は`live/live_test.go`の冒頭）。`sandbox.proto`を変えた作業では`MASUDA_SANDBOX_SOCKET`で開発版のsandboxを指す
- 開発版（チェックアウトからビルドしたもの）は、ハーネス（masuda自身の開発を回している公開物）の`~/.local/bin/masuda`・既定のソケット・既定のデータディレクトリを使わない。決まりは[リリース手順](release.md#dev-separation)
- ドキュメントサイト: `.venv-docs/`に`requirements-docs.txt`を入れ、`scripts/docs-prepare.sh`（生成物を作る）→`mkdocs build --strict`。文書中の`__MASUDA_VERSION__`は、同スクリプトがタグの版に置き換える（元ファイルを書き換える）
- GitHub操作（Issue作成等）は`gh`を直接使わず`scripts/gh.sh`を使う。このリポジトリ専用のトークンを`.env`から読み込んで`gh`に渡すラッパー
- 作業単位は`docs/work-orders.md`（サイトには載せない）

### ソースからビルドして使う {#source}

利用者向けの[導入](../user/install.md)はReleaseのtarballを入れる。masuda自体を直しながら使うときは、3つのリポジトリを同じディレクトリに並べてチェックアウトし、ソースからビルドする。前提は導入と同じに加え、Go 1.26.3以上とpnpm。

```sh
mkdir -p ~/src && cd ~/src
git clone https://github.com/TadahiroYamamura/masuda.git
git clone https://github.com/TadahiroYamamura/masuda-engine.git
git clone https://github.com/TadahiroYamamura/masuda-sandbox.git

cd ~/src/masuda-sandbox
pnpm install && pnpm build          # dist/cli.js。`node ~/src/masuda-sandbox/dist/cli.js serve --socket ...`で動かす

cd ~/src/masuda
go work init . && go work use ../masuda-engine   # engineも手元のものを使うときだけ
go build ./cmd/masuda
./masuda version --sandbox-socket "$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock"   # dev。契約が隣のmasuda-sandboxと合っていればcontract: ok
```

- serveは開発版のソケットとデータディレクトリで起こす: `./masuda serve --socket "$XDG_RUNTIME_DIR/masuda-dev.sock" --data-dir ~/.local/share/masuda-dev --sandbox-socket "$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock"`。CLIの`--socket`やトークンの登録は[リリース手順](release.md#dev-separation)

- ソースからのビルドはバージョンが`dev`になる。リリースと同じに埋めるなら`go build -ldflags "-X main.version=X.Y.Z"`
- masudaとmasuda-sandboxの契約（`sandbox.proto`）がずれていると`masuda serve`が起動しない。両方を同じ時点に揃えるか、`buf generate`と`go generate ./internal/sandboxcontract/`で作り直す

## 触る対象から引く

| 触る対象 | パッケージ・ファイル | 読む節 |
|---|---|---|
| CLIのサブコマンド・表示 | `cmd/masuda/`（`main.go`が一覧、`client.go`がAPIを叩く各コマンド、`chat.go`・`workflow.go`・`init.go`・`config.go`・`serve.go`・`version.go`・`doctor.go`、`templates/`が`masuda init`の雛形） | [overview.md](overview.md)「9. 公開API」のCLI |
| 公開APIの実装（Connectのハンドラ）、実行の組み立てと起動、活動・Watch・ディスク監視 | `serve/`（`serve.go`・`sandbox.go`・`workspace.go`・`run.go`・`boot.go`・`lifecycle.go`・`gates.go`・`questions.go`・`staging.go`・`config.go`・`settings.go`・`images.go`・`privileged.go`・`workflows.go`・`activity.go`・`events.go`・`disk.go`・`listen.go`（ループバックの待ち受けとCORS）） | 「8. 活動の観測と停止の検知」「9. 公開API」「4. 再開」 |
| `engine.Runner`の実装（ゲストとのやり取り・データ・ゲートの記録・実行ログ・exports・WIP復元） | `internal/runner/` | 「3. 1つのタスクの流れ」「4. exports」 |
| staging bareリポジトリ（clone・WIP取り込み・commit・publish・差分の表示） | `internal/staging/` | 「4. ワークスペースとstaging」 |
| ワークスペースのディレクトリ・`workspace.json`・`records/`のゲート・質問・コメント | `internal/workspace/` | 「4. ホスト側のディレクトリ」 |
| ゲスト向けMCP（`/mcp`）とフックの受け口（`/hooks`） | `internal/mcp/`（プロトコルの形）、ツールの中身は`serve/run.go` | [guest-protocol.md](../guest-protocol.md)、「7. ゲストとホストの間」 |
| 起動時にゲストへ置くもの・ループ規約・メインセッションの起動 | `internal/guest/`（`loop-claude.md`がループ規約） | [guest-protocol.md](../guest-protocol.md) |
| sandboxとの互換性の確認（`sandbox.proto`のSHA-256の定数と`GetServerInfo`との比較） | `internal/sandboxcontract/`（`sha.go`は`go generate`の生成物）、`serve/sandbox.go`の`SandboxInfo`・`CheckContract` | 「11. 対応プラットフォームと依存」の「sandboxとの互換性」 |
| VM無しのsandbox（契約テスト・`--fake-sandbox`） | `internal/fakesandbox/` | 「6. サンドボックス」 |
| 特権コマンドの実行手順 | `internal/privileged/`（宣言の読み込みと承認の確認は`serve/privileged.go`） | 「6. 特権コマンド」 |
| `settings.json`・`settings.local.json`・serve全体の`config.json`の形と検査 | `internal/config/`（`config.json`は`serve.go`） | 「6. 設定ファイル」 |
| 秘密の値の保存 | `internal/secrets/` | 「6. 秘密」 |
| 同梱のレビュー観点とスナップショット | `internal/perspectives/`（`builtin/`が同梱の観点） | 「5. 定義の置き場所」 |
| 契約テスト（完了の定義） | `contract/` | [contracts.md](contracts.md) |
| 実物のsandboxと本物のClaude Codeで1周させるテスト（`MASUDA_LIVE_TEST=1`） | `live/` | — |
| 公開APIの契約 | `proto/masuda/api/v1/masuda.proto`（生成コードは`gen/`） | [contracts.md](contracts.md) |
| ドキュメントサイト | `mkdocs.yml`、`scripts/docs-prepare.sh`、`buf.gen.docs.yaml`、`.github/workflows/docs.yml` | — |
| CI・リリース（クロスビルド、tarball、`clients/ts`の`npm pack`） | `.github/workflows/ci.yml`・`release.yml` | [release.md](release.md) |

## 書くときの決まり

- 現在形だけで書く。「当初は」「〜という理由で」が出てきたら、それはコミットメッセージの内容
- 1ファイル1関心事。他ファイルの範囲は参照1行で済ませる
- コードが正。記述と実装が食い違ったら実装を正とし、ドキュメントを直す
- 行番号は引用しない（すぐずれる）。関数名・ファイル名で指す
