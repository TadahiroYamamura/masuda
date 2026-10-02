# HANDOFF
## 作業項目
M5（公開API: ワークスペース・ゲート・質問・活動、CLI）。完了。コミット: `3f33a90`（serve側一式）、`f05d1d5`（CLI）、このHANDOFFの更新
- `serve/events.go`: `eventBus`（全ワークスペースで1本のseq、直近1万件をメモリに保持）と`WorkspaceService.Watch`
  - `after_seq=0`でも最初に今の状態を`status`で1つ送る（seqは今の最新。新しい番号は振らない）。購読開始と状態変化が前後したときの取りこぼし対策。`after_seq>0`ならバッファから再送してから続きを流す
  - `status`は状態・活動の種類/待ち/detail・位置・開いたゲート/質問が変わったときだけ流す（`updated_at`・`last_activity`だけの違いでは流さない）。発行は`backend.statusChanged(id)`。workspace.jsonや記録を書き換えたら呼ぶこと（`runCtl.update`は自動で呼ぶ）
  - `engine`: `runner.Options.OnLog`（`Runner.Log`が1行書くたび）から。`guest_hook`: `/hooks`から。`http`: sandboxの`WatchEvents`から
- `serve/activity.go`: 活動の合成。メモリだけに持つ（`activities`）
  - 優先順: 状態（DONE/STOPPED/BLOCKED→IDLE、WAITING_GATE/QUESTION→同名）→`dead`→進行中のHTTP→フックの`input_wait`→無活動がしきい値超え（`STALLED`）→`WORKING`
  - フック: `Notification`の`idle_prompt`→`idle`、`permission_prompt`→`permission`、`elicitation_dialog`→`question`。`PostToolUse`・HTTP・MCPツールの呼び出しは活動（`input_wait`を消す）。`Stop`/`SubagentStop`は時刻だけ更新。`SessionEnd`→DEAD
  - sandboxの`StateChanged`がSTOPPED/FAILEDで、こちらが止めたのでなければDEAD
  - `patrol`: しきい値/4（1秒〜30秒）ごとにstatusを再評価。実VMだけ30秒ごとに`/usr/bin/tmux has-session -t claude-work`をExecし、失敗ならDEAD（Exec自体の失敗は生存扱い）。しきい値は`serve.Options.StallAfter`／`masuda serve --stall-after`（既定10分）
- `serve/lifecycle.go`
  - `Stop`: runCtlのctxを取り消し、bootの戻りを待ち、MCPを閉じてsandboxを破棄→STOPPED。DONEはFailedPrecondition、STOPPEDはそのまま返す。BLOCKEDもSTOPPEDにする（Reasonは残す）
  - `Resume`: STOPPEDだけ。`records/definitions/`から定義を読み直し、`records/engine.json`でengineを組み直す（`Start`は呼ばない）→新しいMCP→前のsandboxを念のため破棄→作成→stagingのブランチを再clone→（実VMならtmux起動）→`advance()`で今の位置をstateに写す
  - `Remove`: 動いている状態（STARTING/RUNNING/WAITING_*）はforce無しならFailedPrecondition。止めてから`Store.RemoveKeepExports`（`exports/`以外を消す。workspace.jsonを最初に消すので一覧から消える）
  - `AttachInfo`: sandboxの`EnableSsh`の`ssh_argv`に`-t "tmux attach -t claude-work"`を足す。sandboxのエラーコードをそのまま返す（フェイクはUnimplemented）
  - `recoverInterrupted`: serve起動時に動いている状態のワークスペースをSTOPPEDにし、残っているsandboxを裏で破棄。自動再開はしない
- `serve/questions.go`: `QuestionService.ListOpen/Answer`。Answerは記録の質問と答えの過不足・選択肢を先に見てInvalidArgument、動いていなければFailedPrecondition、`runCtl.answer`（`engine.Answer`→記録に`Answers`/`AnsweredAt`→`notify()`→`advance()`）。engineの拒否はFailedPrecondition
- `Run`の変更: 定義（`.masuda/`）を一時ディレクトリへ写してから読み込み・検査し、ワークスペースを作ったら`records/definitions/`へ移す。MCPの起動とフェイクの`mcp.port`書き出し・入力の`PutData`・`engine.Start`を**Runの中で同期に**行う（C-M5はRunの直後にmcp.portを読んで`/hooks`へPOSTする）。sandboxの作成以降だけがバックグラウンド（`backend.boot`）
- `runCtl`: 実行ごとの`ctx`（Stop・Remove・serve停止で取り消し）。`advance`・`Decide`・`ReportResult`等のengine呼び出しはこのctx。`update(f)`がworkspace.jsonの書き換えとstatus発行をまとめる。`reflect`は`Workspace.Position`（"agent smoke-planner (occ 0000001)"等）も書く
- `Workspace`のproto: `activity`・`position`・`open_questions`（未回答の質問の出現ID）を埋める
- `internal/workspace`: `Meta.Position`、`DefinitionsDir()`、`OpenQuestions()`、`Store.RemoveKeepExports`
- 起動用bundleは毎回`<ws>/.bootstrap-*/`に作って消す（Stopで殺されたgitの`.lock`が残り、Resumeが失敗したため）
- CLI（`cmd/masuda/client.go`）: `run`・`resume`・`list`・`watch`・`gate list/show/approve/reject`・`question list/answer`・`stop`・`remove`。`--socket`は全サブコマンド共通。`gate approve`は`--hash`省略時に今のゲートのtarget_hashを使う、`--file`でApprovedFiles
- テスト: `serve/lifecycle_test.go`（serve再起動でSTOPPED→Resume、Removeでexportsが残る、活動の種類の判定）
## 完了した契約テスト
C-M1〜C-M5すべて緑（`go test -count=1 ./contract/ -run 'TestCM1|TestCM2|TestCM3|TestCM4|TestCM5'`、`-race -count=5`でも緑）。`go build ./...`・`go vet ./...`は通る。C-M6・C-M7は想定どおり赤（ConfigServiceがUnimplemented）。M4のHANDOFFにあったC-M4の赤はengine側（E8）で解消済み
## 未完と理由
- Resumeはstagingのブランチから再cloneするので、ゲストの未コミットの作業ツリー（直前の出現の途中の変更、deviationで残したbyproducts）は失われる。WIPスナップショット（`refs/masuda/wip/<occ>`）からの復元はしていない。engineの`ChangedSince`の基準とずれる可能性がある（下の注意点）
- 再開前に`ask_human`で開いていた質問の記録は未回答のまま残る。再開後のエージェントが同じ質問をし直すと二重に並ぶ
- Watchの再送バッファはメモリだけで、serveの再起動をまたがない
- 実VMでのAttachInfo・tmuxの生存確認・HTTPイベントの写しは未確認（M8）
- CLIからdismiss・halt・redoは出せない（APIでは送れる）
- `docs/design/`への反映はしていない（Watchの初回status、活動の優先順など。今の記述と矛盾はしない）
- M4から持ち越し: `.env`生成・`claudeSettings`の合成（M6）、会話ログのexport、`publish target: remote`の送り先、観点の`enable`、`CreateSandbox`の`build_id`、トークンの暫定置き場
## 次の一手
1. M6（設定と秘密）。C-M6はConfigServiceの`ListEgress`から
2. 実機（M8）の前に、Resume時のWIP復元を入れるか決める（下の注意点）
## 注意点
- engineの呼び出しは引き続き`runCtl.mu`の中で。`Advance`を呼ぶのは`runCtl.advance()`だけ
- workspace.jsonを書き換えるときは`runCtl.update`（またはStop/Resumeのように`lifeMu`の中で保存して`statusChanged`）を使う。直接`w.Save()`するとWatchにstatusが流れない
- Run・Resume・Stop・Removeは`backend.lifeMu`で直列。Stopは`lifeMu`を持ったまま`bootDone`を待つので、bootの中から`lifeMu`を取らないこと
- `runCtl.ctx`はbackendのctxの子。Stopで取り消すと進行中のcommit・publishも止まる（engineは記録から再計算するので再開できる想定）
- フェイクの`mcp.port`は`newRunCtl`で書く。Resumeのたびに新しいポートへ書き換わる
- ゲストの未コミットの変更を復元するなら: Runner.Snapshotの最後の戻り値を記録に残し、再clone後に`git reset --hard <wip>`→`git reset <branch-head>`（mixed）で作業ツリーだけ戻すのが素直。WIPコミットは`<branch-head>`を親にしているので、commitを挟んでいなければ一致する
- `Activity.last_activity`は実行開始・再開の時刻で初期化する。serveの再起動後（STOPPED）は観測が無いので空
- フェイクではHTTPイベントが出ないので、フェイクで`STALLED`を見たいときは`--stall-after`を短くする
## 契約への提案
なし
