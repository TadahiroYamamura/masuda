# HANDOFF
## 作業項目
M10（M9の「元から残っていたもの」のうち、`docs/design/`への反映を除いた分）。
- `de85558` serve側
  - **WorkflowService**: `List`（作業ツリーの`.masuda/`＋同梱。`origin`はengineの`Set.Origins`、`inputs`はワークフローの`inputs`。repo_rootが空なら同梱だけ）、`Show`（`Set.Mermaid`）、`Check`（`Set.Check`。workflowが空なら全ワークフローをそれぞれrootにして重複を除く。定義が読み込めないときは理由を`Problem`の1つとして返す）。Runと違い写しは取らない
  - **AttachInfo**: `EnableSsh(user: ubuntu)`の`private_key_pem`を`<DataDir>/workspaces/<id>/ssh/id`へ0600で置き、`ssh_argv`の`-i`をそこへ差し替え、接続先の前に`-t`、後ろに`tmux attach -t claude-work`。起動中（boot前）・止まっているものはFailedPrecondition。フェイクのUnimplementedは「fake sandbox?」を含む文面のUnimplementedで返す。鍵はStop（stopRun）で消す
  - **会話ログのexport**: `Runner.finish`（publish・discard）の最初に、ゲストのホームをcwdにして`find .claude/projects -type f -name '*.jsonl'`をExecし、各ファイルを`ReadFile`で`exports/transcripts/<projects/からの相対パス>`へ写す。一覧が取れない・読めない・書けないものは実行ログに`kind: export-warning`で記録して続ける
  - **stallAfter**: `.masuda/settings.local.json`の`stallAfter`（Goのduration、既定`10m`）。Run・Resumeの組み立て（`planBoot`）で読み、読めない・正でない値はFailedPreconditionで断る。`masuda serve --stall-after`（既定を0＝settingsに従う、に変更）が0でなければ全ワークスペースでそちらが勝つ。見回りの間隔は最短のしきい値の1/4（1秒〜30秒）
  - **ディスク使用量**: serveが60秒ごとに`<DataDir>/workspaces/`の通常ファイルの合計（と内数の`<id>/exports/`）を測る。しきい値は`settings.local.json`の`diskWarnBytes`（既定20GiB）で、ワークスペースのあるリポジトリのうち最小の値。下回っていた状態から超えたときだけ、標準エラーへのログと、`workspace_id`空・`EngineEvent{kind: "disk-warning", detail}`のイベントを出す。`workspace_id`空のイベントはどのワークスペースのWatchにも流すよう`eventBus.since`を変えた。削除はしない
- `14b5aae` CLI側
  - `masuda chat <id>`: AttachInfoの`ssh_argv`を`syscall.Exec`。Unimplementedなら「このsandboxではsshで接続できません（フェイクsandbox等）」
  - `masuda workflow list|show|check [<workflow>] [--repo <dir>]`: --repo省略時は今いる作業ツリーのトップ（`git rev-parse --show-toplevel`）、その外なら同梱だけ。checkは問題があれば終了コード1
  - `masuda gate dismiss|halt|redo <id> <occ> [--comment]`（target_hash不要）。`gate show`はtriageなら懸念の本文を字下げで、deviationならファイルを箇条書きで出し、未判断ならそのゲートで打てるコマンドを添える
  - `masuda list [--all]`: `ID BRANCH STATE ACTIVITY(種類＋最終活動からの経過) POSITION OPEN(gate:<名前>,question:<出現>)`。DONE・STOPPEDは`--all`のときだけ（BLOCKEDは常に出す）
  - `masuda watch`は`workspace_id`空のイベントを種類と本文だけで出す
## 完了した契約テスト
C-M1〜C-M7は緑のまま。`go build ./...`・`go vet ./...`・`go test -count=1 ./...`すべて通る。契約ファイル（両proto、`docs/guest-protocol.md`）と契約テストのassertionは変えていない。追加したテストは`serve/m10_test.go`（WorkflowService・AttachInfo・argvと鍵・会話ログ・stallAfter・ディスク警告・triageの判断）、`cmd/masuda/client_test.go`（gate showの整形・listの1行）、`internal/config/local_test.go`（stallAfter・diskWarnBytes）
## 未完と理由
- `docs/design/`への反映（指示により今回の範囲外）。反映すべき事項は下の「docs/design/へ反映すべき事項」
- 実VMでの確認はしていない: `masuda chat`のアタッチ（実sandboxの`EnableSsh`）と会話ログの回収はフェイクと単体テストでしか見ていない。liveテストにも足していない
- `docs/work-orders.md`のM9の記述は更新していない
## 次の一手
1. 実機で`masuda chat <id>`を試す（tmuxにアタッチでき、`C-b d`で戻れるか）。liveテストの終わりに`exports/transcripts/`に*.jsonlがあるかを見る
2. 下の「契約への提案」の判断（特にserve全体の設定の置き場所）
3. `docs/design/`への反映
## 注意点
- **diskWarnBytesの解釈は指示に無い判断**: 置き場（`<DataDir>/workspaces/`）はserve全体で1つだがsettings.local.jsonはリポジトリごとにあるので、「ワークスペースのあるリポジトリのうち最小の値」をしきい値にした。読めないsettings.local.jsonは既定扱い。警告は超えた瞬間だけで、下回ってからまた超えるまで繰り返さない（serveを再起動すると、超えたままなら起動直後にもう一度出る）
- `--stall-after`の既定を`10m`から`0`（settings.local.jsonに従う）に変えた。`serve.Options.StallAfter`も0ならリポジトリごと。stallAfterは実行の組み立て時に読むので、変更はResumeか次のRunから効く
- 会話ログはpublish/discardの時点のもの。VMを壊すStop・serveの再起動・Removeでは回収しない（その時点のゲストは無くなる）
- `exports/`は設計書の`exports/<id>/`ではなく実装どおり`<DataDir>/workspaces/<id>/exports/`
- ssh鍵はEnableSshのたびに替わる（sandboxの契約）。前の`masuda chat`の接続はそのまま残る
- engineの不具合と思われる挙動には当たらなかった
## docs/design/へ反映すべき事項
- overview 第4章: exportsの中身と置き場所（`workspaces/<id>/exports/`に`<export:のデータ名>`・`execution-log.jsonl`・`transcripts/<project>/…/*.jsonl`）、会話ログの回収方法（find＋ReadFile、失敗は`export-warning`として実行ログへ）、回収はpublish/discardのときだけ
- overview 第8章: 無活動のしきい値の置き場所（settings.local.jsonの`stallAfter`、`--stall-after`が上書き）、活動の優先順（状態→dead→進行中のAPI（2分で打ち切り、idle_promptで60秒より前のものを捨てる）→入力待ち→stalled）、Watchの初回status、ディスク使用量の監視（60秒、`diskWarnBytes`既定20GiB、リポジトリの最小値、`disk-warning`イベント、削除しない）
- overview 第9章: Gateの判断にredoを足す（approve/reject/dismiss/halt/redo。triageはtarget_hash不要）、Workflow `List`/`Show`/`Check`の意味（作業ツリーを直接読む、repo_root空は同梱だけ、Check空は全ワークフロー、読み込み失敗も問題として返す）、`AttachInfo`（鍵は`workspaces/<id>/ssh/id`、Stopで消す）、`workspace_id`が空のイベントはすべてのWatchに届く
- settings.local.jsonの項目一覧に`stallAfter`・`diskWarnBytes`
- CLI: `masuda chat`、`masuda workflow list|show|check`、`masuda gate dismiss|halt|redo`、`masuda list --all`と列
- 前回からの持ち越し（未反映）: 観点の写し（`records/reviews/`→ゲストの`/masuda/reviews/`）、`images.<entry>.diskMiB`、BLOCKED（起動失敗）からの再開、WIP復元と質問の「再開で破棄」、「定義の置き場所」表のレビュー観点の行
## 契約への提案
- **masuda API: serve全体のイベントの型が無い**。ディスク使用量の警告を`EngineEvent{kind: "disk-warning"}`・`workspace_id`空で流している。`WorkspaceEvent`のoneofに`ServeNotice{kind, detail}`（または`DiskUsage{workspaces_bytes, exports_bytes, threshold_bytes}`）を足し、「workspace_idが空のイベントはどのWatchにも届く」を契約に書く提案
- **serve全体の設定の置き場所が無い**。`diskWarnBytes`はリポジトリごとのsettings.local.jsonに置くよう指示されたが、監視は全リポジトリ共通なので最小値を取る解釈にした。`$XDG_CONFIG_HOME/masuda/config.json`のような利用者単位の設定（diskWarnBytes・stallAfterの既定）を設ける提案
- 前回からの持ち越し（判断状況はこちらでは未確認）: sandboxの応答前に切られたHTTPリクエストに終わりのイベントが無い（`http_finished`に`aborted`等）、sandboxのExecの既定環境（PATHに/usr/local/binが無い、`XDG_CACHE_HOME=/tmp/.cache`がroot所有）、engineのfixerに「直せない」終わり方が無い
