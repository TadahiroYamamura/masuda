# HANDOFF
## 作業項目
M3（フェイクsandbox）完了。コミット: `c879fc6`（internal/fakesandbox）、`99d6602`（internal/guest・serveのsandbox起動・staging.ListBlobs）、このHANDOFFの更新
- `internal/fakesandbox`: SandboxServiceのプロセス内実装（`New(dir)`・`StartInProcess(dir)`→`InProcess{Service, Client}`・`Close`）
  - ゲストrootは`<dir>/<id>/root/`（serveは`dir=<DataDir>/fake`、`serve.FakeDir(dataDir)`）。Createで`masuda/`・`tmp/`・`root/`・`home/<default_user>/`を作る。同じidの前回のrootは消してから作る。Destroyは帳簿から外すだけでrootは残す
  - Exec: shellは`/bin/sh -c`、argvは`argv[0]`が絶対パスならホストのそれをそのまま起動。cwd（空なら`$HOME`）とHOME（`/home/<user>`、rootは`/root`）だけをゲストroot下へ写像。環境はPATHだけホストから取り、HOME・USER・Createのenv・Execのenvを重ねる。`timeout_ms`はプロセスグループごとSIGKILLして`timed_out`。`pty`は出力をstdoutにまとめるだけ。userは無視
  - ReadFile/WriteFile: 途中のディレクトリも含めsymlinkを辿らない（読みはNotFound、書きは途中がsymlinkならInvalidArgument、最後のsymlinkは一時ファイル＋renameで置き換え）。`max_bytes`既定64MiB
  - 帳簿: Create/Get/List/Destroy/SetPolicy（宣言外のenabled_secretsはInvalidArgument）。placeholdersは名前から決定的（`placeholder_prefix`既定`masuda-fake-placeholder-`、`placeholder_length`既定prefix+32）。WatchEventsはStateChangedのみ（Createで STARTING→RUNNING、Destroyで STOPPED を出してストリーム終了）。BuildImageは`fake-<16桁>`のbuild_id、ListImagesはそれを返す。EnableSshはUnimplemented、DisableSshは何もしない
  - serveとはnet.Pipe越しのh2c（UDSを使わないのはパス長の上限を避けるため）
- `internal/guest`: `WriteFile`/`WriteBytes`/`Exec`/`Shell`（sandboxクライアントの薄いヘルパー。M4のRunnerでも使える）、`Prepare(ctx, client, Layout{SandboxID, Branch, Bundle, Agents})`、`Settings()`、`User`/`Home`/`HooksURL`。ループ規約の本文は`internal/guest/loop-claude.md`（embed）
- `serve`
  - `serve/sandbox.go`の`connectSandbox(opts)`: FakeSandboxならフェイク、そうでなければ`SandboxSocket`のUDSへh2cのConnectクライアント。どちらも`sandboxv1connect.SandboxServiceClient`で返す
  - `backend`（store・sandboxクライアント・バックグラウンド処理の寿命）。`Server.Stop`がctxを取り消してwgを待ち、フェイクを閉じる。`Server.Done`は後始末まで済んでから閉じるように変えた
  - `Run`はSTARTINGで返した後、`backend.boot`をバックグラウンドで実行: stagingの`refs/heads/<branch>`でbundle作成→`CreateSandbox{id=ワークスペースID, default_user=ubuntu}`→`guest.Prepare`→RUNNING。失敗したらDestroyしてBLOCKED（Reason=`sandbox boot failed: ...`）
  - `Prepare`の中身: bundleを`/masuda/bootstrap.bundle`へWriteFile→cwd `/`で`git clone --quiet -b <branch> masuda/bootstrap.bundle workspace && rm -f masuda/bootstrap.bundle`→`~/.claude/CLAUDE.md`→`~/.claude/agents/<name>.md`（stagingのブランチ先頭の`.masuda/agents/*.md`をそのまま）→`~/.claude/settings.json`（Notification/PostToolUse/Stop/SubagentStop/SessionEndの各フックが`curl -s -X POST http://masuda.internal:7000/hooks -d @-`）
- `internal/staging`: `ListBlobs(ctx, rev, dir)`（dir直下のblobのパス）を追加
- テスト: `internal/fakesandbox/fakesandbox_test.go`、`internal/staging`の`TestListBlobs`、`serve`の`TestRunBootFailureBlocksWorkspace`（serveのテストもフェイクを使うよう`newTestAPI`を変更）
## 完了した契約テスト
C-M1・C-M2・C-M3（`go test -count=1 ./contract/ -run 'TestCM1|TestCM2|TestCM3'`が緑。`go build ./...`・`go vet ./...`も通る）。C-M4〜C-M7は想定どおり赤
## 未完と理由
- `mcp.port`はまだ書かない。MCPサーバーがM4なので、ポートを確保するM4で`<DataDir>/fake/<id>/mcp.port`へ書く（フェイクのときだけ）
- トークン（プレースホルダ）・環境変数・`.env`生成・`~/.claude/.mcp.json`相当・tmux起動・対象リポジトリの`claudeSettings`の合成は置いていない（M4以降。指示どおり）
- `CreateSandbox`の`build_id`は空。イメージのビルド・解決が入る項目で埋める。実物のsandboxでは今のままだと起動できないはず
- `RunRequest.inputs`は引き続き保存していない（M4/M5）
- `WorkspaceService.Remove`は未接続（M5でsandboxのDestroyと組にする）
- 前セッションから引き継いだ未完（`venv/`等の追跡外ファイル、`CLAUDE.md`の旧「開発環境」、`.claude/skills/`）はそのまま
## 次の一手
`docs/work-orders.md`のM4（Runnerとゲスト起動）
## 注意点
- **フェイクのExecはコマンド文字列中の絶対パスを写像しない**。ゲストで動かすコマンドはcwdを`/`（または`/workspace`等）にして相対パスで書くこと。例: Snapshotは`cwd=/workspace`で`git add -A && ...`、`/masuda/out/...`へ書かせるならcwd `/`で`masuda/out/...`。`git -C /workspace`のような書き方はフェイクではホストの`/workspace`を見てしまう
- フェイクのExecのHOMEは`<root>/home/ubuntu`なので、ゲストで`git commit`等をするならidentityは`GIT_AUTHOR_*`等をenvで渡す（ホストの`~/.gitconfig`は見えない）
- ゲストrootに書く/読むのはsandboxのWriteFile/ReadFileを通すこと（`guest.WriteBytes`等）。`serve.FakeDir`配下を直接触るのはテストと`mcp.port`だけにする
- `guest.Prepare`はM4の`internal/guest`の起動手順の前半にあたる。.env・MCP設定・環境変数・tmuxはここへ足し、tmux起動はフェイクのときは飛ばす（フェイクのExecはホストでtmuxを起動してしまう）
- サブエージェント定義はstagingのファイルをそのまま写しているだけ。M4でengineの定義（`name`・`description`・`tools`・本文）から生成するよう置き換える。今は`.`で始まる名前があると起動失敗（BLOCKED）にしている
- 起動処理はバックグラウンド（`backend.goBackground`）。engineのループ等の長生きする処理も同じ仕組みに載せればStopで止まる
- 実VMでは`ubuntu`が`/`直下に`workspace`を作れる必要がある。イメージ側で用意するか、rootでcloneしてchownするかM4/M8で決める
- `ListBlobs`で読むのはブランチ先頭（＝分岐元）なので、実リポジトリの未コミットの`.masuda/agents`は反映されない
## 契約への提案
なし
