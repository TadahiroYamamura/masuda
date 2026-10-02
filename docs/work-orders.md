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

## M11. ドキュメント整備

読者は3系統。利用者（CLIでmasudaを使う）、統合開発者（GUIや他ツールからAPIを使う）、開発者（masuda自体を直す）。置き場所は`docs/user/`・`docs/api/`・`docs/design/`。MkDocs Material + Mermaidで`docs/`をそのままソースにし、GitHub Pagesで公開する。正は1つに保つ: APIリファレンスは`masuda.proto`から生成、ワークフロー定義の仕様は`../masuda-engine/docs/workflow-schema.md`から取り込む。3セッションに分ける。

### M11a. サイトの土台とdesignの反映

- `mkdocs.yml`（Material、日本語、検索、Mermaid、ナビは`user/`・`api/`・`design/`の3セクション）。`requirements-docs.txt`（mkdocs-material・mike）。`docs/index.md`（3系統への入口）
- 取り込み: ビルド前スクリプト`scripts/docs-prepare.sh`が、`buf generate`のドキュメントプラグイン（`protoc-gen-doc`のMarkdown出力）で`docs/api/reference.md`を作り、`../masuda-engine/docs/workflow-schema.md`を`docs/user/reference/workflow-schema.md`へ写す（CIでは`actions/checkout`でengineを隣に取る）。生成物はコミットしない（`.gitignore`）
- GitHub Actions `.github/workflows/docs.yml`: トリガーは`main`・`develop`へのpush、タグ`v*`のpush、`workflow_dispatch`（手動。現在の作業ブランチ`redesign`から試すため）。`mike deploy --push --update-aliases`で、タグ`vX.Y.Z`→バージョン`X.Y`+エイリアス`latest`、`main`→`main`、`develop`→`dev`、手動→`dev`。既定バージョンは`latest`（`mike set-default`）。`permissions: contents: write`。Pagesの有効化（Source: `gh-pages`）はユーザーが行う
- `docs/design/`の反映: M4〜M10のHANDOFFに溜まった事項をoverview.mdに現在形で書く（exportsの場所と中身、活動判定の優先順と打ち切り、Watchの初回status、ServeNotice、settings.local.jsonの項目一覧（`stallAfter`・`diskWarnBytes`・`vars`・`claudeToken`・各承認）、観点のスナップショットと`/masuda/reviews/`、`images.<entry>.diskMiB`、BLOCKEDからの再開、WIP復元と質問の破棄、特権コマンドの記録の構成と`outputs_error`、AttachInfoの鍵の置き場所、`records/`の構成）。`docs/design/README.md`の「触る対象から引く」表を新しいパッケージ構成（`serve/`・`internal/{staging,workspace,runner,mcp,guest,fakesandbox,privileged,config,secrets,perspectives}`・`cmd/masuda`）で書き直す
- 完了の判定: `mkdocs build --strict`が通り、`workflow_dispatch`で`dev`が公開される（Pagesの有効化後）。契約テストは触らない

### M11b. 利用者向け（`docs/user/`）

- 導入: 前提（QEMU・Node 22.19以上・Docker・git、Linux x86_64/WSL2とmacOS arm64の手順）、`masuda-sandbox serve`と`masuda serve`の起動、`masuda init`、Claudeトークンの登録（`masuda secret set CLAUDE_CODE_OAUTH_TOKEN`）、最初の1周（`masuda run workflows/develop`→`watch`→`gate approve`）
- 概念: ワークスペース・staging・VM・ゲート（plan/review/interim/deviation/triage）・質問・publishとdiscard、何がホストに触れるか（commit/publishだけ）、秘密がゲストに入らないこと
- リファレンス: CLI全サブコマンド、`settings.json`と`settings.local.json`の全項目、ワークフロー定義（取り込んだ`workflow-schema.md`）、エージェント定義、レビュー観点（`.masuda/reviews/*.md`のfrontmatter）、スキーマ、同梱ワークフロー（`develop`・`review`）の図（`masuda workflow show`のMermaidを貼る）
- 運用: 秘密とegressと特権コマンドの宣言と承認、`.env`の生成、再開（`resume`）とWIP、`chat`、exportsの読み方、トラブルシューティング（VMが起動しない・ディスク不足・`No space left`・MITMのCA・`stalled`の意味）
- 正しさの確認: 書いた手順を実際に打って確かめる（liveテストの環境がある）

### M11c. 統合開発者向け（`docs/api/`）

- 接続: UDS/ループバック、Connect（gRPC・HTTP+JSON）の呼び方（ブラウザからの`fetch`例、Go/TSのクライアント生成）、認証が無くローカル前提であること
- リファレンス: `reference.md`（生成）。RPCごとのエラーコードの約束（`NotFound`・`FailedPrecondition`・`InvalidArgument`・`Unimplemented`）を実装から洗い出し、HANDOFFの「契約への提案」として一覧を出す（protoのコメントへの反映は監督が行う）
- 流れのガイド: ワークスペースの起動と`Watch`（`seq`・`after_seq`・初回status・`ServeNotice`）、ゲートの表示と判断（`target_hash`・`StagingCommit`・deviationの`approved_files`・triage）、stagingの差分とコメントで差分ビューを組む、質問への回答、`Activity`の各状態の表示指針、`Resume`/`Stop`
- TSクライアント: `masuda.proto`から生成したTypeScriptクライアントを`clients/ts/`に置き、`npm pack`できる形にする（公開はしない）

### M12. 設定の整理と、ドキュメント整備で見つかった不備

M10・M11b・M11cが見つけたもの。`docs/api/errors.md`と`docs/user/`の警告（`reviews.md`・`workflows.md`）が直す対象を指している。直したら該当する文書の警告も消す（同じコミットで）。

- **エラーコードの統一**（`docs/design/contracts.md`「エラーコードの約束」）: 未定義ワークフローは`Run`も`InvalidArgument`、壊れた`settings.json`は`Config`も`InvalidArgument`、ゲートの種類に合わないoutcomeは`InvalidArgument`（`Unimplemented`をやめる）。`Watch`の`after_seq`が最新より大きければ`OutOfRange`
- **review gateの承認対象**: engine（E10）が`Runner.Diff(DiffCommitted, …)`を呼ぶようになる。masudaの`Runner.Diff`に`DiffCommitted`（`refs/masuda/base..refs/heads/<branch>`）を実装し、`Gate.staging_commit`はそのブランチ先頭、`Gate.subject`はその差分。CLIの`gate show`とUIが「publishされない変更」の一覧を区別して出せるよう、`subject`の形は engine が決めるものをそのまま通す
- **既存ブランチのレビュー**（`workflows/review`が単独で意味を持つように）: publishノードを含まないワークフロー（engineの`Set`から判定）に限り、`Run`で実リポジトリに既にあるブランチを指定できる。stagingはそのブランチを`refs/heads/<branch>`に持ち、base は実リポジトリの既定ブランチ（または`--base`）。`AlreadyExists`はpublishを含むワークフローだけ。`docs/user/workflows.md`の警告を消す
- **観点の`enable`**: `.masuda/reviews/*.md`のfrontmatter `enable: false`の観点をスナップショットから除く（同梱の観点を外す手段にもなる）。`docs/user/reviews.md`の警告を消す
- **`workflow check`（引数なし）**: rootのワークフローだけを検査する（engineの`Reachable`で部品を除く）。`overview.md`第9章も直す
- **findingsをstagingのコメントへ**: review gateを開くとき、累積データ`findings`の各要素を`staging_commit`に対するコメント（author=観点名または`cross-cutting`、`severity`、`path`・`line`）として`records/comments.jsonl`に取り込む。UIが差分ビューに重ねられるように
- **BLOCKEDの扱い**: engineが止めたBLOCKED（起動失敗以外）は`Stop`できるが`Resume`は`FailedPrecondition`。`overview.md`第4章の記述と揃える
- **ServeNotice**: `disk-warning`を`EngineEvent`流用から`ServeNotice`へ。**ループバック待ち受け**: `$XDG_CONFIG_HOME/masuda/config.json`の`listen`があるときだけ`127.0.0.1:<port>`でもConnectを待ち受け、CORSは任意オリジン許可（ローカル前提）。サーバー全体の設定（`diskWarnBytes`・`stallAfter`の既定・`sandboxSocket`）も`config.json`へ。`settings.local.json`の`stallAfter`はリポジトリごとの上書き
- **Claudeトークンの置き場所**: リポジトリごとではなくユーザー単位（`<DataDir>/secrets/_user/CLAUDE_CODE_OAUTH_TOKEN`）を既定にし、リポジトリごとの登録があればそれを優先。`masuda secret set CLAUDE_CODE_OAUTH_TOKEN`は`--repo`無しならユーザー単位へ
- 細かい点: `run --base`のヘルプを実装に合わせる（今チェックアウトしているブランチ）、`init`の雛形から意味の無い`egress: ["api.anthropic.com"]`を外しコメントで「Claude APIは常に許可」と書く、雛形DockerfileのコメントをS10以降の実態（イメージのENVは引き継がれる、PATHには`~/.local/bin`が足される、`-modcacherw`は不要）に直す、publishの`target: remote`の送り先を`settings.json`の`publish.remote`（既定`origin`）で設定できるようにしoverviewを合わせる、`overview.md`のCLI表に`list --repo`を足す
- 契約テスト: C-M1〜C-M7が緑のまま。`contract/`に「publishを含まないワークフローは既存ブランチで`Run`できる」「review gateの`subject`が未コミットの変更を分けて載せる」ケースを**監督が足す**ので、着手時に確認

### M12（旧）設定の整理（M10の提案）

- サーバー全体の設定`$XDG_CONFIG_HOME/masuda/config.json`（`diskWarnBytes`・`stallAfter`の既定・`sandboxSocket`）を設け、`settings.local.json`の`stallAfter`はリポジトリごとの上書きに、`diskWarnBytes`は`config.json`だけにする
- `ServeNotice`（契約に追加済み）で`disk-warning`を流す。`EngineEvent`の流用をやめる
- `--stall-after`の既定`0`（設定に従う）はそのまま
- **ループバックの待ち受け**: `config.json`の`listen`（例: `"127.0.0.1:7788"`）があるときだけ、UDSに加えてそのアドレスでもConnectを待ち受ける（`contracts.md`「通信の前提」どおり）。ブラウザのGUIはUDSに繋げないので、これが無いとAPIを使えない。CORSは同一ホストの任意オリジンを許す（ローカル前提）。実装と同じコミットで`overview.md`第9章を直す

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
| C-M8 | M12（既存ブランチでの publish 無しワークフロー、エラーコード） |
