# HANDOFF
## 作業項目
M4（Runnerとゲスト起動）。masuda側の実装は済んでいるが、C-M4はengineの振る舞い1点（下の「契約への提案」）で赤のまま止めた。コミット: `e3f7104`（実装一式）、このHANDOFFの更新
- `internal/runner`: `engine.Runner`の実装（`runner.New(Options{Workspace, Sandbox, SandboxID, Author, Reviews, AlwaysHosts, AlwaysSecrets})`）と`FileStore`（`engine.Store`、`records/engine.json`）
  - `SetPolicy`: sandboxの`SetPolicy`。`AlwaysHosts`（`api.anthropic.com`）と`AlwaysSecrets`（`CLAUDE_CODE_OAUTH_TOKEN`）を常に足す
  - `PutData`/`GetData`: `data/<occ>/<name>`、実行開始時の入力（Occurrence ""）は`data/_run/<name>`
  - `ReadOutput`: ゲストの`/masuda/out/<occ>/<name>`をReadFile。無ければok=false
  - `RunCommand`: 入力を`/masuda/in/<occ>/`へ、cwd `/`で`mkdir -p masuda/out/<occ>`、argvをcwd `/workspace`でExec（env `MASUDA_IN`・`MASUDA_OUT`・`MASUDA_OCCURRENCE`）、宣言した出力をReadFile、LogTailは末尾8KiB
  - `Snapshot`: ゲストのcwd `/workspace`で`git add -A`→`write-tree`→`commit-tree -p HEAD`→`refs/masuda/snapshot`→`git bundle create ../masuda/snapshots/<label>.bundle refs/masuda/snapshot ^HEAD`→ReadFile→`staging.FetchBundle`で`refs/masuda/wip/<occ>`。**戻り値はコミットハッシュ**（refではない。engineは同じ出現で進入時と終了時に2回取るのでrefは上書きされる）
  - `ChangedSince`/`Diff`/`Commit`: どれも直前に今の作業ツリーを取り込み直して（`refs/masuda/worktree`）それと比べる。engineは書き込めないエージェントの終了時にSnapshotより**先に**ChangedSinceを呼ぶので、境界のスナップショットを使うと変更を見落とす。`ChangedSince`のハッシュは`staging.Changes`（`diff-tree --raw`のsha256。blobを含むので同じファイルの再変更も別ハッシュ）
  - `Commit`: `staging.Commit`。Deviationsなら`Changes(head, 今)`のハッシュを添えて返す。新しい先頭ができたらbundle（`refs/heads/<b> ^old`）を`/masuda/sync.bundle`へ置き、ゲストで`git fetch`+`reset --soft FETCH_HEAD`。`scope: step`なら`records/committed-steps.json`に記録し、`Items(steps)`の`Done`に使う
  - `Items`: `steps`（planのsteps、Input `step`、Keyは`number`）、`findings`（`autofix: true`だけ、Input `finding`）、`perspectives`（同梱＋`.masuda/reviews/*.md`、Input `perspective`）、`perspectives(from=...)`（selected-perspectivesのid順）、`<data>[]`（JSON配列、文字列要素は引用符を外す、Inputはengineに任せる）
  - `OpenGate`/`OpenQuestion`: `records/gates/<occ>-<seq>.json`・`records/questions/<occ>-<seq>.json`（`internal/workspace/records.go`）。target=diffのゲートには開いた時点の`refs/heads/<branch>`を`StagingCommit`に入れる
  - `Publish`: `PublishLocal`（承認ハッシュとstagingの先頭の一致確認込み）/`remote`はoriginへ`PushRemote`→exports→sandbox破棄。`Discard`はexports→破棄
  - exports: `exports/<name>`（exportに書かれたデータの最新値）と`exports/execution-log.jsonl`。完了・blockedになったときにserveがログを写し直す
  - `Log`: `records/execution-log.jsonl`へ追記
  - `MaterializeTask`（入力と`/masuda/in/<occ>/task.md`を書く）・`WriteOutput`・`Validate`（engineの`validateData`と同じ規則、問題を行で返す）も公開している
- `internal/mcp`: `mcp.Start(host)`で127.0.0.1の空きポート。`/mcp`はgo-sdk v1.7.0のStreamable HTTP（`Stateless`・`JSONResponse`・`DisableLocalhostProtection`）、ツールは生のAddToolで引数を自分で読む（`arguments: null`を通すため）。`/hooks`はPOSTのbodyを`Host.Hook`へ渡すだけ
- `serve`
  - `serve/run.go`の`runCtl`: 1ワークスペースの実行（engine・Runner・MCPサーバー）。`mcp.Host`の実装。engineの呼び出しは`mu`で直列。`advance()`はbackendのctxで進めて状態を`workspace.json`に写す（agent→RUNNING、gate→WAITING_GATE、question→WAITING_QUESTION、done→DONE+Outcome、blocked→BLOCKED+Reason、Advanceのエラー→BLOCKED `engine: ...`）
  - `next_task`: Advance→agentならMaterializeTaskして`{kind:task, occurrence, role(=Agent.Name), task_path}`、gate/questionなら`changed`チャネルで起こされるまで待つ（7日）
  - `write_output`: 待っている出現か・宣言した出力名かを見て、`runner.Validate(set.Schemas)`、通ればゲストへWriteFile
  - `report_result`: 待っている出現でなければ`{accepted:false}`。done報告で未出力があればengineに渡さず拒否。engineのエラー（宣言外outcome等）も`{accepted:false, reason}`。受け付けたら次の待ちまで同期でAdvance
  - `report_concern`: `engine.ReportConcern`を呼ぶだけ（E6が入るまでErrNotImplemented→ツールエラー）
  - `ask_human`: questionノードのタスクだけ。`records/questions/`に書いてWAITING_QUESTIONにし、記録に答えが入るまで待つ（答えを入れるのはM5のQuestionService.Answer。engine.Answerを呼んでから記録を書いて`notify()`すること）
  - `run_privileged_command`: ツールエラー（M7）
  - `/hooks`: `records/hooks.jsonl`へ`{time, input}`で追記
  - `serve/gates.go`: `GateService.ListOpen/Get/Decide`。Decideは記録のTargetHashと比べて不一致ならFailedPrecondition、engineの拒否もFailedPrecondition、ErrNotImplementedはUnimplemented。受け付けたら記録に判断を書き、次の待ちまで同期でAdvanceしてから返す
  - `serve/boot.go`: Runは同期で定義を読み込み検査（`engine.Load(<repo>/.masuda, Bundled)`→ワークフローの存在→`Set.Check`→入力の過不足、どれもInvalidArgument）。バックグラウンドで: FileStore・Runner・engine・MCP起動→`CreateSandbox`（`secrets`に`CLAUDE_CODE_OAUTH_TOKEN`、初期policyでAPIへの経路、`tcp_maps` `masuda.internal:7000`→MCP）→フェイクなら`<DataDir>/fake/<id>/mcp.port`→`guest.Prepare`→入力を`PutData`→`engine.Start`→実VMだけ`guest.Launch`（tmux）→RUNNING
  - `Workspace.open_gates`を記録から埋める
- `internal/guest`: `ReadFile`/`IsNotFound`、`AgentFile(*engine.Agent)`（name・description・toolsと本文。サブエージェントは`Set.Reachable(root)`のエージェントだけ）、`~/.claude.json`（`mcpServers.masuda`をhttpで、`hasCompletedOnboarding`）、`Launch`（tmux、環境変数はguest-protocol.mdのとおり＋`MCP_TOOL_TIMEOUT=604800000`）
- `internal/staging`: `FetchBundle`（任意のrefへ取り込み。`ImportBundle`はこれを使う）、`Changes`（変更パスとダイジェスト）
- テスト: `internal/runner/runner_test.go`（FileStore・Validate）。`serve`の既存テストは同梱`workflows/smoke`で走るよう変え、起動失敗のテストはCreateSandboxが失敗するクライアントで起こすようにした（エージェント定義をstagingからコピーしなくなったので`.hidden.md`では失敗しない）
## 完了した契約テスト
C-M1・C-M2・C-M3は緑。**C-M4は赤**（`contract_test.go:430`、commitに`notes.txt`が入る）。原因はengineの振る舞いで、下の「契約への提案」の修正をengineの写し（スクラッチ）に当てて走らせるとC-M1〜C-M4すべて緑になることを確かめた（masuda側は無変更で）。`go build ./...`・`go vet ./...`は通る。C-M5〜C-M7は想定どおり赤
## 未完と理由
- C-M4の緑化: engine側の修正待ち（「契約への提案」）。指示どおりengineは触っていない
- 定義の`Set`はメモリにだけ持つ（ワークスペースへのスナップショット保存をしていない）。serveを再起動すると`runCtl`が無く、Decideは`FailedPrecondition: workspace is not running`になる。再開（`Resume`）はM5以降の項目
- `.env`生成（envFiles）・対象リポジトリの`claudeSettings`の合成: 設定の項目（M6）で入れる
- 会話ログのexport: 未実装（ゲストの`~/.claude/projects`をどう回収するか未定）
- `publish target: remote`の送り先は`origin`固定（設定はM6）
- 観点の`enable`（frontmatter）は`Items(perspectives)`で見ていない
- `Workspace.position`・`open_questions`・活動は未設定（M5）
- `CreateSandbox`の`build_id`は空のまま（イメージのビルド・解決が入る項目で埋める）。実VMではこのままでは起動できないはず
- トークンは`<DataDir>/claude-oauth-token`から読む（DataDirの既定が`~/.local/share/masuda`なので指示の場所と同じ。M6で秘密ストアに統合するまでの暫定）。プレースホルダの形は`PlaceholderPrefix: "sk-ant-oat01-"`だけ指定した（Claude Codeがトークンの形を見るかは実機で未確認）
## 次の一手
1. 監督がengine側の扱い（「契約への提案」）を決め、engineが直ったら`go test -count=1 ./contract/ -run 'TestCM1|TestCM2|TestCM3|TestCM4'`を流し直す（masuda側の変更は要らない見込み）
2. M5（公開API: ワークスペース・ゲート・質問・活動）
## 注意点
- ゲストで動かすコマンドは引き続き「cwdを決めて相対パス」（フェイクは絶対パスを写さない）。Runnerはこれに従っている（`../masuda/...`、`masuda/out/...`）
- engineの呼び出しは必ず`runCtl.mu`の中で。`Advance`を呼ぶのは`runCtl.advance()`だけにして、状態の写しを漏らさない
- `notify()`はブロック中のnext_task・ask_humanを起こす。人間の判断・答えを記録に入れたら必ず呼ぶ。`advance()`自身はnotifyしない（next_taskが自分の起こしたAdvanceで起きて空回りしないため）
- report_result・Decideは次の待ちまで同期でAdvanceしてから返す。publishやexecを含むとその分戻りが遅くなる（MCPのタイムアウトは7日なので問題ない想定）
- `engine.Status`はreport直後などに`StatusPending`を返すことがある。`waitingAgent`はそれを「待っていない」として拒否する。ReportResult・Decideの後は同期でAdvanceするので通常は出ない
- ゲートの記録は`records/gates/`が正（ListOpenはここを読む）。engineの状態と食い違わないよう、判断はengineが受け付けた後に記録へ書いている
- 実リポジトリに`user.name`が無いと`git config --get`はグローバル設定を拾う（stagingのコミットの作者になる）。無ければ`masuda`
- M5でQuestionService.Answerを作るとき: role付きquestionは`ask_human`が待っている記録（`records/questions/<occ>-<seq>.json`）に`Answers`を入れて`notify()`、固定質問は`OpenQuestion`の記録。どちらも`engine.Answer`を先に呼ぶ（E6でengine側が実装中）
- `go.mod`にgo-sdk v1.7.0・jsonschema v6を足した（engineと同じ検証をwrite_outputで先に行うため）
## 契約への提案
**deviationゲートで`approved`かつ`approved_files`が空のときの意味が、engineとmasudaの契約テストで食い違っている。**

- 契約テスト C-M4（`contract/contract_test.go`の手順3）: deviationゲート（Subjectは`notes.txt`）を`Outcome: approved, ApprovedFiles: []`で承認し、コメントは「notes.txtは並べないことで却下する→未コミットのまま残る」。その後のコミットは`b.go`だけで、review gateへ進むことを期待している
- engine（E5、`engine/commit.go`の`records.approvedFiles`）: 「ファイルを並べない承認はゲートのファイル全部を承認したとみなす」。そのため`CommitRequest.Allowed`に`notes.txt`が入り、ホストは`b.go`と`notes.txt`をコミットする（実際の失敗: `files:"b.go" files:"notes.txt"`）
- 公開API（proto3の`repeated string approved_files`）では空と未指定を区別できないので、masuda側で「空＝なし」と「空＝全部」を書き分けることはできない
- さらに、engineの空=全部を外すだけでは足りない。`Allowed`から`notes.txt`が外れると、ホストの`Runner.Commit`は`notes.txt`を計画外として`Deviations`で返し、engineが同じ内容のdeviationゲートを開き直す（スクラッチで確認: 「want review gate, got deviation notes.txt」）。契約テストの意図（承認したが並べなかったファイルは、コミットにも逸脱にも数えず作業ツリーに残す）を満たすには、**承認済みのdeviationゲートのファイルのうち`ApprovedFiles`に無いものを`CommitRequest.Byproducts`に入れる**必要がある
- 提案（engine側の変更）: (1) `approvedFiles`の「空なら全部」をやめ、並べたファイルだけを`Allowed`に足す。(2) 承認されたdeviationゲートのファイルで`ApprovedFiles`に無いものは`Byproducts`に足す。この2点をengineの写し（`engine/commit.go`のみ）に当てると、masudaは無変更でC-M1〜C-M4が緑になった。engineの契約テストC-E5は`ApprovedFiles: []string{"c.go"}`と明示しており「空の承認＝全部」には依存していない（`masuda-engine/contract/contract_test.go:769`）。逆に「空＝全部」を正とするなら、C-M4の手順3のassertion（またはApprovedFilesの渡し方）を監督が直す必要がある
