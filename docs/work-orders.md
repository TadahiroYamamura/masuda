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

## M13. 配布（v0.1.0に向けて）

決定（ユーザー）: npm公開はv1.0以降でそれまではtarball。macOSは「実験的」と明記。v0.1.0は今の機能で出し、互換性の保証はv1.0以降。

- **バージョン埋め込み**: `-ldflags -X main.version=X.Y.Z`。`masuda version`はこれとGoのバージョン、接続先sandboxの`GetServerInfo`（届けば）を出す
- **互換性の確認**: `masuda serve`は起動時と各ワークスペースの起動前にsandboxの`GetServerInfo`を呼び、`contract_sha256`が自分の生成元`sandbox.proto`のSHA-256（ビルド時に埋める）と違えば`FailedPrecondition`で止める（理由にバージョンを含める）。`masuda doctor`でも表示
- **`masuda doctor`**: 前提の確認（`qemu-system-*`・`/dev/kvm`またはHVF・Node 22.19以上・Docker・git、sandbox serviceの到達と`GetServerInfo`、Claudeトークンの有無）。足りないものと直し方を出す
- **GitHub Actions `release.yml`**: タグ`v*`で`linux/amd64`・`darwin/arm64`（darwinは「実験的」。macOSの検証はM5）をクロスビルドし、`masuda_X.Y.Z_<os>_<arch>.tar.gz`とSHA-256をGitHub Releaseに添付。`clients/ts`の`npm pack`のtgzも添付。**CI `ci.yml`**: `main`/`develop`へのpushとPRで`go build`・`go vet`・`go test ./...`（契約テストはフェイクで回るのでCIで回す。liveはskip）
- **リリース手順書** `docs/design/release.md`: (1) 3リポジトリの契約テストが緑、liveテストが完走していること、(2) `redesign`→`develop`（初回のみ: `git branch -f develop redesign && git push -f origin develop`、旧developはタグ`v1-frozen-develop`。`v1-frozen-*`の3タグもpush）、(3) `main`を`develop`に合わせる、(4) sandbox→masudaの順にタグ`vX.Y.Z`を打つ（masudaのリリースノートに対応するsandboxのバージョンを書く）、(5) docsワークフローが`X.Y`と`latest`を公開することの確認、(6) Releaseの添付物でインストール手順（`docs/user/install.md`）を1回なぞる
- `docs/user/install.md`を「ソースからビルド」から「Releaseのtarballを入れる」中心に書き直し、ソースからの手順は開発者向け（`design/README.md`）へ
- **3リポジトリに同じタグ**（ユーザーの指示）: masudaのリリース`vX.Y.Z`は、依存するengineとsandboxのコミットにも同じタグ`vX.Y.Z`を打つ。順序はengine→sandbox→masuda。masudaのリリースノートに両方のコミットハッシュを書く。リリース手順書に明記する
- **engineの依存をタグで固定**: `go.mod`の`replace => ../masuda-engine`をやめ、`require github.com/TadahiroYamamura/masuda-engine vX.Y.Z`にする（リリースのたびに上げる）。開発中の隣接ディレクトリ参照はgitignoreした`go.work`（`go work use . ../masuda-engine`）で行い、`CLAUDE.md`にその手順を書く。CIとreleaseは`GOFLAGS=-mod=mod`無しで、タグ付きのengineを取る
- **`guest.BaseEnv`のPATH上書きをやめる**: sandboxはS10/S12で既定PATH（`$HOME/.local/bin`を先頭に足す、イメージのENVを引き継ぐ）を保証するので、masudaがPATHを上書きする必要が無くなった。フェイクsandboxは同じ既定を模倣する
- **E11後**: 同梱`build-step`の`approve-interim`が`target: step-diff`になるので、`gate show`とUI向けの`subject`の扱い、`docs/user/concepts.md`のinterimの説明、findingsのコメント取り込み（interimでは`staging_commit`がHEADで行番号は作業ツリー基準）を合わせる
- 契約テスト: C-M1〜C-M8が緑のまま

## M14. v0.2（masudaでmasudaを作る体制）

v0.2の目標は「masudaを使ってmasudaが作れる体制」（マイルストーンv0.2: #68 #69 #70 #61、engine #8）。ハーネス（公開物の`masuda`・`masuda-sandbox serve`・既定ソケット・`~/.local/share/masuda`）の導入は**v0.2.0の公開後**に行う（`docs/user/install.md`の検証を兼ねる）。それまで開発版は既定の場所を使わない: データディレクトリ`~/.local/share/masuda-dev`、ソケット`$XDG_RUNTIME_DIR/masuda-dev.sock`・`$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock`。`~/.local/share/masuda`と`$XDG_RUNTIME_DIR/masuda.sock`・`masuda-sandbox.sock`には触らない。

各項目に共通: ビルド・テストは`GOWORK=off`（`go.work`があると隣の`../masuda-engine`の作業中のコードが混ざる）。契約（`proto/masuda/api/v1/masuda.proto`・`docs/guest-protocol.md`・`../masuda-sandbox/proto`・`../masuda-engine/engine/api.go`）は変えない。pushしない。コミットは目的ごとに分け、メッセージはCLAUDE.mdの形。終わったら`HANDOFF.md`を上書きし、最終報告は「コミット・検証・指示から外れた点」を10行以内（詳細はHANDOFFへ）。

### M14a. masuda自身の`.masuda/`を整える（#68の前半）

- **追跡に入れる**: `.gitignore`の`.masuda/`・`.masuda-gate/`（旧実装の名残。`.masuda-gate/`はもう無い）をやめ、`masuda init`が足す行（`cmd/masuda/init.go`の`localIgnores`）と同じものだけ無視する。`.masuda/`の中身をgitに加える
- **`.masuda/reviews/`**: 同梱の観点（`internal/perspectives`）と内容が同じ写しは消す（無ければ同梱が使われる。写しを持つと同梱の更新が効かなくなる）。同梱と差があるものだけ残し、残したものはHANDOFFに列挙する
- **`.masuda/images/default/Dockerfile`**: Claude Codeの版を`internal/guest.ClaudeCodeVersion`と同じ版に固定する。形は`cmd/masuda/templates/Dockerfile`と同じ`bash -s -- <版>`（数字は直書き。テンプレートの`__CLAUDE_CODE_VERSION__`の置換は`masuda init`のときだけ）。`masuda image build`の`note:`（版の不一致）が出なくなること。コメントの「新設計」は消す
- `.masuda/images/default/ctx/go.mod`・`go.sum`を今の`go.mod`・`go.sum`で更新する（Dockerfileの冒頭のコメントどおり）
- **`.masuda/settings.json`**: `checks.test`を`GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...`相当にする。`egress`はイメージで前取りしているものは宣言しない（Goモジュール・gopls・bufはイメージで取れているか確かめ、足りなければ宣言）。`claudeSettings`に`{"model": "opus"}`を書く（ユーザー決定: 既定の役をOpusにする。Claude Codeはfrontmatterに`model`の無いサブエージェントにメインセッションのモデルを使うので、これで役にも効く。`settings.json`の`model`は`CLAUDE_CODE_SUBAGENT_MODEL`が無い限りサブエージェントに直接は効かない、という公式の解決順序を前提にしている）
- **`.masuda/pitfalls.jsonl`**（形式は`docs/user/settings.md`の`pitfalls.jsonl`の節。全キー必須）。少なくとも次を書く。`background`は`HANDOFF.md`・`CLAUDE.md`・`git log`から実際にあったことを引く:
  - 契約ファイル（`masuda.proto`・`guest-protocol.md`・sandbox.proto・engineの`api.go`）は変えない（`HANDOFF.md`の「契約への提案」に書いて止まる）
  - `buf generate`でsandboxのクライアントを作り直したら`internal/sandboxcontract/sha.go`を`go generate`で作り直して一緒にコミットする
  - `go.work`があると隣のengineの未コミットの変更が混ざる。固定した版で確かめるときは`GOWORK=off`
  - engineの版を上げるのは`go get ...@<tag|main>`と`go mod tidy`。プロキシが古いmainを返すことがある（`GOPROXY=direct`）
  - `HANDOFF.md`はセッション終了時に決まった見出しで上書きする
  - コメントの基準（CLAUDE.mdの「コメント」）
  - ゲストのClaude Codeの版は`internal/guest.ClaudeCodeVersion`で固定。上げたら継続テスト（`TestGuestSubagentContinuation`）を先に回す
  - `internal/runner/task.go`が出す「実行位置: ワークフロー…のノード…」の形は、engineのreviewerが途中レビューの判別（ノード名が`interim-`で始まるか）に使っている。変えると壊れる
  - `docs/user/workflows.md`の図は`masuda workflow show`の出力の貼り付け。同梱定義が変わったら取り直す
  - `docs/user/reference/workflow-schema.md`はengineの写し（`scripts/docs-prepare.sh`）。直接編集しない
  - テストケース名は日本語で何を確かめるかを文で書く（`~/.claude/rules/testing.md`相当。既存テストに倣う）
- **`.masuda/claude/`**: リポジトリの`CLAUDE.md`はcloneで届く。追加で要るものが無ければ作らない（作るなら理由をHANDOFFに）
- **ゲストで非特権のユーザー名前空間が使えるか**: 契約テストC-M7とserveの特権コマンドのテストは、フェイクsandboxの`internal/fakesandbox/exec.go`の`asRoot`（`unshare -Urm /bin/sh -c <chrootするスクリプト> ...`）を使う。`.masuda/images/default`のイメージで作ったVMで、同じ形の`unshare -Urm`が通るか確かめる。手段は`live/`のヘルパー（イメージのビルド・sandboxの作成・`Exec`）を流用した使い捨てのプログラムかテストでよく、コミットしない。開発版のsandbox serveは`$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock`で動いている（`MASUDA_SANDBOX_SOCKET`で渡す）。作ったVMは必ず壊し、終わったら`pgrep -af qemu-system`に自分の分が残っていないこと。**通らなければ**: 直さずに、落ち方（エラーの全文）とカーネル・`unshare`の版をHANDOFFに書き、`checks.test`はそのまま（`-skip`で外さない。判断は監督が行う）
- **検証**: `GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test -count=1 ./...`が緑。`masuda workflow check`（フェイクserve: `GOWORK=off go run ./cmd/masuda serve --fake-sandbox --data-dir <一時dir> --socket <一時dir>/m.sock`を立て、`--socket`で指す）が`pitfalls.jsonl`を通し、問題を出さないこと。`masuda image build default`（開発版sandbox serveを`--sandbox-socket $XDG_RUNTIME_DIR/masuda-sandbox-dev.sock`で指したフェイクでない`masuda serve`。`--data-dir ~/.local/share/masuda-dev --socket $XDG_RUNTIME_DIR/masuda-dev.sock`）が`note:`無しで通ること。立てたserveは終わったら止める
- 禁止: `$XDG_RUNTIME_DIR/masuda.sock`・`masuda-sandbox.sock`・`~/.local/share/masuda`に触れる、`docker rm -f`・`docker system prune`、他人のqemu・node・serveプロセスを`kill`する（止めてよいのは自分が立てたserveだけ）、`git push`、`go.work`の削除
- 完了の判定: 上の検証がすべて緑で、`.masuda/`がgitに追跡されていること。契約テストC-M1〜C-M10は無修正で緑のまま

### M14b. 役定義の`model`・`effort`をゲストのサブエージェント定義に写す（#69、契約変更）

**契約変更（ユーザー承認 2026-10-03）**: `docs/guest-protocol.md`の「起動時にホストがゲストへ置くもの」の`~/.claude/agents/*.md`の行に、`model`・`effort`（役定義にあれば）を足す。engine側はE13で`Agent.Model`・`Agent.Effort`を足した（`../masuda-engine`の`main`、未push）。

背景: Claude Codeのサブエージェント定義はfrontmatterの`model`（`sonnet`・`opus`・`haiku`等の別名、フルのモデルID、`inherit`）と`effort`（`low`・`medium`・`high`・`xhigh`・`max`）を受け付け、どちらも効き、会話ログ（JSONL）の各応答に`model`・`effort`が記録されることをホストのClaude Code 2.1.288で実測した。`model`の無いサブエージェントはメインセッションのモデルを継承する（`settings.json`の`model`は直接は効かない）ので、`claudeSettings.model`が「既定の役のモデル」になる。指定の単位は役定義（ノード単位の上書きは入れない。ユーザー決定）。

- **この項目に限り`go.work`を使う**（`GOWORK=off`を付けない）。隣の`../masuda-engine`のE13が要るため。`go.mod`のengineの固定はengineのpush後に監督が行う。契約テスト（`contract/`）も`go.work`で回す
- `internal/guest.AgentFile`: `a.Model`・`a.Effort`が空でなければfrontmatterに`model:`・`effort:`を書く（`yamlString`で）。関数コメントの「Claude Codeが読むのはname・description・toolsと本文だけ」を直す
- `internal/guest/guest_test.go`: `model`・`effort`のある役はその行が出る、無い役は出ない（テスト名は日本語の文）
- docs: `docs/user/workflows.md`「エージェントの書き方」に`model`・`effort`を足す（値、省略時: `model`はメインセッションのモデル＝`claudeSettings.model`を継承、`effort`はセッションの既定を継承。`continues`で続きが成立したサブエージェントは起動時の設定のまま）。`docs/user/settings.md`の`claudeSettings`に「`model`を書くとメインセッションと、`model`を書いていない役のモデルになる」を足す。`docs/user/reference/workflow-schema.md`は写しなので触らない（サイトのビルドで取り込まれる）
- **実機確認（#69の本体）**: `live/claude_dir_test.go`の`TestClaudeDirReachesSubagent`で、使う役の1つに`model: sonnet`・`effort: low`を書き、対象リポジトリの`.masuda/settings.json`の`claudeSettings`を`{"model": "opus"}`にする。完走後の`exports/transcripts/`で、その役のサブエージェントのJSONLに`"model":"claude-sonnet`と`"effort":"low"`が、`model`の無い役のJSONLに`"model":"claude-opus`が記録されていることを確かめる検査を足す（どのJSONLがどの役かは、中身の`agentType`や`records/subagents.json`等、既存の結び付け方を調べて使う）。実装者が回す: `MASUDA_SANDBOX_SOCKET=$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock MASUDA_LIVE_TEST=1 go test -count=1 -timeout 20m -v -run TestClaudeDirReachesSubagent ./live/`。ゲストのClaude Codeは2.1.287（`internal/guest.ClaudeCodeVersion`）で、frontmatterの`effort`が効かなければ（記録に`effort`が無い・値が違う）**直さずに**HANDOFFへ事実を書く（リリースで最新版に上げるときに再確認する）。終わったら`pgrep -af qemu-system`に自分の分が残っていないこと
- 検証: `go build ./... && go vet ./... && go test -count=1 ./...`（`go.work`有効）が緑。契約テストC-M1〜C-M10は無修正で緑のまま。上記liveが緑
- 禁止: M14aと同じ（既定のソケット・`~/.local/share/masuda`に触れない、`docker rm -f`、他プロセスの`kill`、`git push`、`go.work`の削除、新しい依存の追加）

### M14c. 予行1: `Watch`の`after_seq`が再送バッファより古いときは`OutOfRange`を返す（#64）

この項目は**masudaのrun（`workflows/fix`）で実装する予行**（#68の後半）。監督（Fable）が開発版のserveで`masuda run workflows/fix --branch fix/watch-after-seq --input instructions=@<この項目を書き出したファイル>`を回し、plan gate・review gateを扱う。以下がゲストの実装者への指示書。

---

`WorkspaceService.Watch`の`after_seq`は、最新のseqより大きければ`OutOfRange`を返す（`serve/events.go`）。しかし再送バッファ（直近の一定件数）より古いときは、黙って最古から続く。クライアントは間の取りこぼしに気づけない。

決定（契約`docs/design/contracts.md`「エラーコードの約束」に反映する）: `after_seq`が再送バッファの最古のseqより小さい（＝`after_seq+1`から再送できない）ときも`OutOfRange`を返し、理由の文に「再送できる最古のseq」を含める。クライアントは`after_seq: 0`で繋ぎ直し、ストリームの最初に届く`status`から状態を組み立て直す（既存の「最新より大きい」の扱いと同じ）。`after_seq: 0`はこれまでどおり常に通る。

やること:
- `serve/events.go`の`Watch`で上の判定を足す。`proto/masuda/api/v1/masuda.proto`は変えない（`OutOfRange`は既存のコード）
- `docs/design/contracts.md`の`OutOfRange`の行と、`docs/api/errors.md`の2箇所（`out_of_range`の表と`Watch`の行）を、両方の条件を書く形に直す。`docs/api/`にWatchの繋ぎ直しの流れを説明している箇所があれば（`grep -rn after_seq docs/api`）合わせる
- `cmd/masuda`の`watch`が繋ぎ直しに`after_seq`を使っているなら、`OutOfRange`を受けたら`after_seq: 0`で繋ぎ直す（既に「最新より大きい」でそうしていれば、その経路に乗るだけでよい）
- テスト（`serve/`の既存のWatchのテスト、`serve/m10_test.go`の「after_seqが最新より先ならOutOfRange」の隣に足す。テスト名は日本語の文）: バッファが溢れるだけイベントを起こしてから、最古より小さい`after_seq`で`Watch`すると`OutOfRange`になり、理由に最古のseqが含まれる。最古のseqちょうどなら通る。判定をわざと壊して落ちることを確かめてから戻す。バッファの大きさがテストで扱いにくければ、テストから小さくできる手段を足してよい（公開APIは変えない）
- `GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...`が緑。契約テスト`contract/`は無修正で緑のまま

やらないこと: 再送バッファの大きさの変更、`Watch`の他の振る舞いの変更、protoの変更。

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
