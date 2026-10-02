# 作業単位（masuda）

1項目を1セッションで終える。完了の判定は対応する契約テスト（`contract/`）が緑であること。契約（`proto/masuda/api/v1/masuda.proto`・`docs/guest-protocol.md`）は変えない。他リポジトリの契約（`../masuda-sandbox/proto`・`../masuda-engine/engine/api.go`）も変えない。

共通の前提: `docs/design/overview.md`全体。engineは`go.mod`の`replace github.com/TadahiroYamamura/masuda-engine => ../masuda-engine`で取り込む（公開するまで）。sandboxのクライアントは`../masuda-sandbox/proto`から`buf generate`する。

## M1. 旧コードの削除と骨組み

- `redesign`ブランチで、次を残して旧コードを`git rm`する: `docs/`、`scripts/gh.sh`、`proto/`、`buf.*`、`CLAUDE.md`、`HANDOFF.md`、`.gitignore`、`.claude/`。流用するものは消す前に新しい場所へ写す: `internal/worktree`の`git clone --bare --local`・fast-forward（→`internal/staging`の種）、`internal/perspectives/builtin/*.md`（→`internal/perspectives`）、`internal/config`の宣言/承認の形（→`internal/config`）。`Dockerfile`・`runtime/`・`orchestrator/`・`templates/`・`assets.go`・`venv`・Python資産はすべて削除
- `buf generate`（masuda.protoとsandbox.proto）。`go.mod`を書き直す: `connectrpc.com/connect`、`golang.org/x/net`（h2c）、`github.com/TadahiroYamamura/masuda-engine`（`replace => ../masuda-engine`）。`contract/contract_test.go`がコンパイルできるようになるのがこの項目の出口の1つ
- `package serve`: `serve.Start(ctx, serve.Options{Socket, DataDir, FakeSandbox bool, SandboxSocket}) (*serve.Server, error)`と`Server.Stop()`。Connectで全サービスを登録（未実装は`Unimplemented`）
- `cmd/masuda`: `serve`、`version`。他のサブコマンドはAPIクライアントとして後で足す
- `README.md`・`docs/INSTALLATION.md`・`docs/CONTRIBUTING.md`は旧設計の記述なので、再設計版の最小限（何か・3リポジトリ・依存・`masuda serve`と`masuda run`）に書き直す
- `scripts/gh.sh`はそのまま
- 契約テスト: C-M1

## M2. ワークスペースとstaging

- `internal/staging`: 作成（`git clone --bare --local <repo> staging.git`、`refs/masuda/base`）、bundleの取り込み（`refs/masuda/wip/<occ>`）、bundleの作成（ゲストへ渡す用）、`commit-tree`による計画範囲のコミット（`Allowed`外は含めない。`Byproducts`は無視）、fast-forward publish（`git -C <repo> fetch staging <branch>`→`update-ref`は`--ff-only`相当で）、remote push
- `internal/workspace`: ID、`$XDG_DATA_HOME/masuda/workspaces/<id>/`の構成（`staging.git`、`data/`、`records/`、`exports/`）、一覧、削除
- `StagingService`（ListRefs/GetCommit/Diff/GetBlob/コメント）
- 契約テスト: C-M2

## M3. フェイクsandbox

- `internal/fakesandbox`: `sandbox.proto`をプロセス内で実装する。VMの代わりに`<DataDir>/fake/<id>/`をゲストのrootとして、`Exec`はホストで`sh -c`（cwdをゲストrootへ写像、`/workspace`→`<root>/workspace`）、`ReadFile`/`WriteFile`はそのディレクトリへ、`SetPolicy`/placeholders/`WatchEvents`は帳簿だけ持つ。`BuildImage`はダミーのbuild_id
- 契約テストが前提にするフェイクの配置: ゲストrootは`<DataDir>/fake/<sandbox-id>/root/`（`/workspace`→`root/workspace`、`/home/ubuntu`→`root/home/ubuntu`、`/masuda`→`root/masuda`）。ワークスペースのMCPポートは`<DataDir>/fake/<sandbox-id>/mcp.port`に数字だけ書く（実VMではsandboxの`tcp_maps`に渡すので不要。フェイクのときだけ書く）
- `serve --fake-sandbox`でこれを使う。契約テストはこれで動く
- 契約テスト: C-M3

## M4. Runnerとゲスト起動

- `internal/runner`: `engine.Runner`の実装。`SetPolicy`→sandbox、`RunCommand`→入力を`/masuda/in/<occ>/`へ`WriteFile`、`Exec`、`/masuda/out/<occ>/<name>`を`ReadFile`、`Snapshot`→ゲストで`git add -A && git write-tree`と`git bundle`→stagingへ、`Diff`/`ChangedSince`→staging上で計算、`Commit`/`Publish`/`Discard`→staging、`Items`、`PutData`/`GetData`→`data/`、`Log`→`records/execution-log.jsonl`
- `internal/guest`: 起動手順（`docs/guest-protocol.md`「起動時にホストがゲストへ置くもの」）。bundleを置いてclone、`CLAUDE.md`、エージェント定義、`settings.json`（フック）、`.env`生成、tmux起動
- `internal/mcp`: ワークスペースごとのローカルポートでMCP（`next_task`・`write_output`・`report_result`・`report_concern`・`ask_human`・`run_privileged_command`）と`/hooks`。`next_task`は`engine.Advance`を呼び、`StatusAgent`ならタスクファイルを書いてパスを返し、ゲート/質問待ちならブロック
- 契約テストが前提にする配置: `Decide`の`TargetHash`不一致は`FailedPrecondition`、宣言外の秘密やホストの承認は`InvalidArgument`。exportsは`<DataDir>/workspaces/<id>/exports/`で`execution-log.jsonl`を含む。`/hooks`はClaude Codeのフック入力JSON（`hook_event_name`・`notification_type`・`message`・`tool_name`）をそのまま受ける
- 契約テスト: C-M4（フェイクsandbox + engine実物 + テスト用の最小ワークフローで、MCPを直接叩いて1周）

## M5. 公開API（ワークスペース・ゲート・質問・活動）

- `WorkspaceService`全RPC。`Run`は定義の読み込み→検査→ワークスペース作成→スナップショット→sandbox作成→ゲスト起動→`engine.Start`
- `GateService`・`QuestionService`→`engine.Decide`/`Answer`
- `Activity`: sandboxの`WatchEvents`と`/hooks`から合成。しきい値は設定（既定10分で`stalled`）
- `Watch`のイベント配信（seq、再送）
- CLI: `run`・`resume`・`list`・`watch`・`gate list/show/approve/reject`・`question list/answer`・`stop`・`remove`
- 契約テスト: C-M5

## M6. 設定と秘密

- `checks` の実体化: 同梱ワークフローの `exec` ノードは `command: ["/masuda/checks/<名前>"]` でチェックを呼ぶ。masuda はワークスペース起動時に `.masuda/settings.json` の `checks.<名前>`（シェルコマンド文字列）を `/masuda/checks/<名前>` の実行可能スクリプト（`#!/bin/sh -e` + `cd /workspace` + コマンド）として `WriteFile` する。宣言されていないチェック名を使う定義は `Set.Check` では分からないので、起動時に masuda が確かめて止める
- `internal/config`: `.masuda/settings.json`（`image`、`egress`、`secrets`、`envFiles`、`privilegedCommands`、`checks`、`claudeSettings`）と`.masuda/settings.local.json`（`egressApproved`、`secretsApproved`（plaintext用）、`privilegedCommandsApproved`、`claudeToken`）。宣言ハッシュで承認の失効を判定
- `internal/secrets`: `$XDG_DATA_HOME/masuda/secrets/<repo-hash>/<NAME>`（0600）。Claudeトークンも同じ仕組み（`masuda secret set CLAUDE_CODE_OAUTH_TOKEN`。旧`masuda claude set-token`は無い）
- イメージの再ビルド判定: Gondolinの`build_id`は同じDockerイメージからでもビルドのたびに変わり、1回約390MBの資産が溜まる。sandboxの`ListImages`に同じ`oci_digest`（`docker build`の結果）の資産があれば再ビルドせずそれを使う。資産の削除はsandbox APIに将来`DeleteImage`を足して対処する（今は無い）
- `ConfigService`とCLI（`egress`・`secret`・`privileged-command`・`image`）、`masuda init`（`.masuda/`の雛形、既定の`egress`に`api.anthropic.com`、`images/default/Dockerfile`の雛形）
- 契約テスト: C-M6

## M7. 特権コマンド

- 宣言の検証、2つ目のsandbox（root、`inputs`の運搬、WIPスナップショットのbundleで`checkout`、`outputs`の回収、写しをメインVMの`/masuda/privileged/<run-id>/`へ）
- 契約テスト: C-M7（フェイクsandbox）

## M8. 実機で1周

- `MASUDA_LIVE_TEST=1 go test ./live/`: 実際の`masuda-sandbox serve`で、同梱`develop`をこのリポジトリ自身に対して1周（調査→計画→plan gate→実装→レビュー→review gate→publish）。ゲートはテストがAPIで承認する
- ここで出た契約の穴はHANDOFFに書いて止まる（監督が契約を直す）

## M9. 残り

M8の実機1周で見つかったもの（優先）:

- **観点の置き場所**: 起動時に`.masuda/reviews/*.md`（実リポジトリ側、無ければ同梱）を`/masuda/reviews/`へ`WriteFile`し、`Runner.Items(perspectives)`も同じスナップショットから返す（`docs/guest-protocol.md`に追記済み）。engine側のtrigger-matcherのプロンプトはE9で`/masuda/reviews/`を読むように変わる
- **活動の判定**: メインセッションが人間に問いかけたまま止まっても`input_wait`にならなかった。ゲストのフックが`/hooks`に届いているか（`records/hooks.jsonl`）、`Notification`の`idle_prompt`が出る条件を実機で確かめて直す。あわせてループ規約（`~/.claude/CLAUDE.md`）に「人間に聞きたいことは`ask_human`で。会話で問いかけて待たない」を明記する
- 起動失敗でBLOCKEDになったワークスペースを`stop`なしで`resume`できるようにする
- `masuda init`が`.gitignore`に`.masuda/`があっても`settings.local.json`の行を足す重複を直す。雛形のDockerfileのコメントに`GOCACHE`等のキャッシュを`/tmp`へ向ける案内と、`-modcacherw`の注意（S10が直すまで）を書く
- `CreateSandbox`に`disk_mib`を渡す（sandboxの契約に追加済み。`settings.json`の`image`エントリ側に`diskMiB`を持たせる。既定4096）
- `live/`（`MASUDA_LIVE_TEST=1`）に、M8の段階1を自動化したテストを置く

元から残っていたもの:

- **Resumeで未コミットの作業ツリーを復元する**: 再cloneのあと、最新の`refs/masuda/wip/<occ>`のtreeを作業ツリーに展開する（`git read-tree -m -u <wip>` 相当。HEADはブランチのまま、WIPは未コミットの変更として戻す）。engineの基準点と作業ツリーがずれないため。再開前に開いていた`ask_human`の質問は閉じて（記録に「再開で破棄」）、再開後のエージェントに聞き直させる
- `masuda chat`（`AttachInfo`→ssh）、`WorkflowService`、exports、`masuda workspace list`の表示、ディスク使用量の監視、CLIからのtriage判断（dismiss/halt/redo）、無活動しきい値のsettings.jsonへの統合、`docs/design/`への反映（Watchの初回status・活動の優先順）

## 契約テストの対応表

| テスト | 項目 |
|---|---|
| C-M1 | M1 |
| C-M2 | M2 |
| C-M3 | M3 |
| C-M4 | M4 |
| C-M5 | M5 |
| C-M6 | M6 |
| C-M7 | M7 |
