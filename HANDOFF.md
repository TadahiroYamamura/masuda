# HANDOFF
## 作業項目
engineのmain `4abd0c4`（push済み）の新しい契約`continues`をゲストまで配達し、続けて`ba1d49c`へ追従した（masuda、develop、未push）。**契約`docs/guest-protocol.md`を変えた**（ユーザー承認済み）。`masuda.proto`は変えていない。
- 契約: `next_task`に任意の引数`agent_id`（直前に完了したタスクを担当したサブエージェントのID。続けたときもそのID）、戻り`kind: task`に任意の`continues: {occurrence, agent_id?}`、ループ規約の手順2（`continues`の`agent_id`のサブエージェントがいれば`SendMessage`でタスクファイルのパスだけ、いなければ新しく起動）と手順4（`next_task`に直前のサブエージェントの`agent_id`）、タスクファイルの「続き」、`report_result`の`agent_id`は残して「通常は空」と備考
- IDの記録: `records/subagents.json`（出現→ID、`internal/workspace/records.go`の`SubagentIDs`・`SetSubagentID`・`ClearSubagentIDs`）。`next_task(agent_id)`を受けたら、そのrunCtlが最後に渡したタスクの出現（`runCtl.lastTask`、メモリのみ）に結び付ける。runCtlはRun・Resumeのたびに作り直され、どちらも新しいVMなので、再開直後のメインセッションが古いIDを渡しても結び付かない。Resume（`serve/lifecycle.go`）で`subagents.json`を消す
- `next_task`の応答（`serve/run.go`の`continuesOf`）: `AgentTask.Continues`が空でなければ`continues`を付け、記録にIDがあれば`agent_id`も
- タスクファイル（`internal/runner/task.go`）: `Continues`があれば役割の指示の前に「## 続き」（覚えていれば前提にしてよい、覚えていなければ入力だけで、従うのはこのタスクの指示で`occurrence`は今の出現ID）
- ループ規約の実体（`internal/guest/loop-claude.md`）: 上の手順に「サブエージェントの続き」の節（agentIdを控える、`SendMessage`が遅延読み込みなら`ToolSearch`、IDが無い・見当たらない・エラーなら新しく起動、送るのはパスだけ）
- MCP（`internal/mcp/mcp.go`）: `next_task`のスキーマに`agent_id`、`Host.NextTask(ctx, agentID)`
- docs: `docs/design/overview.md`の3節の図に続きの経路、`docs/user/workflows.md`に`continues`の段落・検査の1行・developのfixerの説明（続きと反論）、`docs/user/reviews.md`にfixerの反論とrecheckerの取り下げ
- live: `live/engine_continuation_test.go`（下の「完了した契約テスト」）。fallbackのテストは、本番の続きと同じく記憶が無くても成り立つ入力を渡す形（`second`は役`copier`、入力`token`を`recall`へ写す）に作り直した。これらのliveは、メインセッションの入力待ち（WAITING_INPUT）が60秒続いたら上限を待たずに失敗にする（`waitWorkspace`。既存の1周の`driveLap`は変えていない）
- engine `ba1d49c`: done以外の終わり方でも書かれた出力を検証して保存する（`9a16b1e`）。masuda側はdone以外の出力をengineに任せていたので、コードの変更は無し。契約`docs/guest-protocol.md`の`report_result`の備考だけ直した（「`done`なのに未出力なら拒否。`done`以外は書かれた出力だけを検証」、ユーザー承認済み）

その前（コミット済み・未push）: engineのmain `0049e12`に追従し、`needs_human`等のdone以外の終わり方で役のfeedbackを`Workspace.reason`に入れて`masuda list`に出した。ゲストのClaude Codeの版を`internal/guest.ClaudeCodeVersion`（2.1.287）で固定し、リリース手順に「リリースのたびに最新版へ上げて検証する」を足した。実機テスト`TestGuestSubagentContinuation`（SendMessageで続きを送って文脈が残るかの前提検査）を足した。
## 完了した契約テスト
- 2026-10-03（engine `4abd0c4`追従・continues）: `GOWORK=off go build ./... && go vet ./... && go test -count=1 ./...`緑。足したテスト: 契約テスト`TestCM9_ContinuesCarriesReportedAgentID`（`continues`付きのワークフローで、続きのタスクの`continues`に宛先の出現と`next_task(agent_id)`で報告したIDが出る、タスクファイルに「## 続き」、Stop→Resumeの後は`occurrence`だけで、古いIDを渡しても結び付かない）、`TestTaskFileContinues`（runner）、`TestLoopRulesDescribeContinuation`（guest）、`TestEngineContinuationDefinitionsCheck`（live、VM不要）
- 実機（engine `ba1d49c`）: `TestEngineContinuationKeepsMemory`合格（32秒、recall一致）、`TestEngineContinuationFallsBackAfterResume`合格（64秒。recall＝token、再開前のIDの記録`{0000001: ade1…}`が再開で消え、後に`{0000003: ab31…}`（別のID）、メインセッションはSendMessageを使わず`Agent`でcopierを起動）
- 実機: `TestEngineContinuationKeepsMemory`はそれ以前にも3回合格（62秒・37秒・37秒）。recallはtokenと一致（例`k7Qm2xV9bR4t`）。メインセッションのツールは`Agent=1, SendMessage=1, ToolSearch=2, Read=3`、`subagents.json`に`{"0000001": "<agentId>"}`。Claude Code 2.1.287の`Agent`は非同期起動（`async_launched`）で、完了は通知で届く
- 2026-10-03（engine `0049e12`追従）: `GOWORK=off go build ./... && go vet ./... && go test -count=1 ./...`緑（C-M*を含む。新しい同梱定義のままテストの直しは不要だった）。足したテスト: `serve/run_test.go`の`TestNeedsHumanEndCarriesFeedbackAsReason`（同梱の`workflows/fix`を実際に始め、quick-plannerが`needs_human`で報告すると`outcome needs_human`・`reason`＝feedbackの`done`になり、写し直しても消えない。変更前のコードでは落ちることを確認）、`TestListRow`に`outcome needs_human: <1行目>`の形
- 2026-10-03: `GOWORK=off go build ./... && go vet ./... && go test -count=1 ./...`緑（C-M*を含む）。足したテスト: `TestClaudeCodeNote`・`TestImageClaudeCodeNoteFollowsSettingsImage`・`TestPrintVersionShowsVerifiedClaudeCode`（cmd/masuda）、`TestLiveDockerfilePinsClaudeCode`（live、VM無しで常に走る）、`TestInitRepoWritesTemplatesAndKeepsExistingFiles`に版の検査
- `precheck.sh --claude-code`が通る（2.1.287のmanifestは200。最新版は2.1.288なのでinfoが出る）。`claude-code-latest.sh`は2.1.288を出した
- 実機: 版付きのDockerfileで`TestGuestSubagentContinuation`が合格（1m35s、イメージの作り直し込み）。`claude --version: 2.1.287`、recallは一致
- 前の作業: 継続テストは2.1.287で2回合格（55秒・50秒）。メインセッションのツールは`Agent=1, Bash=1, SendMessage=1, ToolSearch=1, Write=1`、token.txtとrecall.txtを書いたサブエージェントの記録は同じ`agent-*.jsonl`。今の版では完了したサブエージェントへターンをまたいでSendMessageで続きを送れ、文脈が残る
## 未完と理由
- 記憶の無いサブエージェントに「前に考えた／書いた文字列」を求める課題は、APIの安全分類器（`[reasoning_extraction]`）に止められる（2026-10-03に3回）。fallbackのテストは入力を渡す形に作り直して解消済み。本番の続きのタスクは常に入力を持つので同じ形にはならない見込みだが、分類器で止まったメインセッションは入力待ちのまま進まない（STALLED扱いになるかは未確認）
## 次の一手
1. （解消済み）`TestEngineContinuationFallsBackAfterResume`の作り直しと実機の合格
2. 実機1周（quickstart・`workflows/develop`か`fix`）で、fixerがimplementerの続きとして動くか（`subagents.json`、メインセッションの`SendMessage`）、反論（`disputed`）とrecheckerの裁定が妥当かを見る
3. 実機（quickstartの三角形の課題等）で`masuda run workflows/fix`を1周させる。あわせて曖昧な指示書で`needs_human`になり、`masuda list --all`に疑問が出ることを見る
4. 次のリリース（**v0.2.0**）で、SKILL.mdの1-0に従ってClaude Codeを最新版へ上げ、継続テスト（`TestGuestSubagentContinuation`・`TestEngineContinuation*`）→1周で検証する。engineにタグを打ったらそのタグへ`go get`し直す
5. 実機1周で、レビュー工程の所要時間と、途中レビューで`clean`直結・`nothing_to_fix`直結が効くか、`trigger`無しのテスト漏れ観点の指摘が妥当かを見る
6. review gateに`gate comment`を付けて却下し、rework/implementのタスクに行コメントが届いて反映されるかを見る
## 注意点
- **契約（`docs/guest-protocol.md`）が変わったので次のリリースはv0.2.0**（engineも同じ。engineのHANDOFFより）
- engineの制約1「出力は`done`の報告でしか保存されない」は**反映済み（engine `9a16b1e`）**。done以外でも書かれた出力は検証して保存される（recheckerは取り下げをunresolvedと同じ報告で書ける、`66cd4f7`）
- **ユーザー判断待ち（engineの制約）**: recheckerの`withdrawn`（取り下げ）は累積データの保存時に捨てられるので、synthesizerは「反論して取り下げられた指摘」を台帳から読めずレポートに載らない
- サブエージェントのIDはVMの中のClaude Codeでしか通じない。IDの結び付けは「そのrunCtlが最後に渡したタスクの出現」なので、メインセッションが`next_task`を2回呼ぶ（同じタスクが返る）と同じ出現に上書きされるだけで害は無い。serveを起こし直すとrunCtlも作り直される（その時点でVMも作り直し）ので、メモリの`lastTask`が消えても困らない
- liveの続きの役（rememberer・recaller・copier）は`tools: Read`。`Write`を持たせるとengineが書き込める役とみなし、承認済みの計画を求めて検査で拒否する（出力は`write_output`なのでWriteは要らない）
- ゲストのClaude Code 2.1.287では`Agent`ツールが非同期で起動し（`async_launched`）、完了は`<task-notification>`で届く。メインセッションはその間ターンを終える（Stopフック）。続き（`SendMessage`）は完了済みのサブエージェントを`Resuming agent`で再開する
- **次のリリースノートに書く**: 同梱の`agents/implementer`の入力が`[plan, investigation]`になった。対象リポジトリの自前のワークフローでimplementerを使い、それより前に`investigation`を用意していないと、`masuda run`・`masuda workflow check`の検査（engineの`Set.Check`）で拒否される。同梱では`develop`のinvestigatorと`fix`のquick-plannerが用意する。`docs/user/workflows.md`の上書きの節にも書いた
- `fix`のreworkが読む`investigation`は最初の計画時のまま（reworkの前に調べ直さない）。`fix`の`plan`（`max: 3`）には`exhausted`の行き先が無く、上限に達するとblockedで止まる（engineのHANDOFFより）
- done以外の終わり方の`reason`はserveが`report_result`で受け取ったfeedbackの全文。CLIは1行目しか出さないので、全文は`workspace.json`の`reason`（またはAPIの`Get`）。docsにはそう書いた
- **ゲストのClaude Codeの版は`internal/guest.ClaudeCodeVersion`で固定**（2.1.287）。上げたら`TestGuestSubagentContinuation`を先に回す。サブエージェントの継続（SendMessage）の仕組みが変わればここが落ち、「同じエージェントにレビュー指摘の修正を続けさせる」設計の前提が崩れる。版はテストログの`continuation-report`の1行目に出る
- `install.sh`は版を渡しても、まず`latest`の版のブートストラップを落としてから指定の版を入れる（install.shの148行目付近）。固定されるのは最終的に入る版で、インストーラ自体の振る舞いは上流次第
- 利用者のリポジトリの既存のDockerfileは`masuda init`が書き換えないので、masudaを上げても版は変わらない。`masuda image build`の`note:`で気づける
- `serve/settings.go`の`guestEnv`のコメントは「トークンを除く」だが、execの環境に`CLAUDE_CODE_OAUTH_TOKEN`（プレースホルダ）が入っていた（継続テストのレポートで`token source: exec-env`）。出どころ未調査（sandboxが秘密のプレースホルダをExecの環境に入れている可能性）。直していない
- `end:failed`の`failed`はengineの予約ラベルで読み込みが拒否される。continuationの失敗は`end:not_continued`
- 継続テストのclaudeは`--settings '{"disableAllHooks":true}'`で起こす。フックはmasudaの`/hooks`に届き、このセッションのSessionEndがメインセッションの死（DEAD）と区別できないため
- `masuda-sandbox serve`は`cd ~/work/masuda-sandbox && node dist/cli.js serve --socket $XDG_RUNTIME_DIR/masuda-sandbox.sock`で起こす（2026-10-03は起動したまま）
- `trigger`の無い観点の扱いが変わった。旧trigger-matcherは「`trigger`を持たない観点は選ばない」だったが、engine `78818dd`のreviewer.mdは「途中レビュー: `trigger`を持たない観点は常に当てる」。同梱の`missing-tests-guard-clauses`・`missing-tests-new-code`も途中レビューで毎回当たる。これはユーザー判断で現状のまま確定（計画で実装とテストを同じステップに入れる方針になったため、テスト漏れの観点を途中で当てる意味がある）。`docs/user/reviews.md`はこの挙動で書いてある
- reviewerは「実行位置」のノード名が`interim-`で始まるかで途中レビューを判別する。masudaの`internal/runner/task.go`が出す「実行位置: ワークフロー…のノード…」の形を変えると壊れる
- `docs/user/workflows.md`の図は`masuda workflow show`の出力の貼り付け（`develop`・`fix`・`review`の3つ）。同梱定義が変わったら、`masuda serve --fake-sandbox --data-dir <tmp> --socket <tmp>/m.sock`を立て、リポジトリの外のディレクトリで`masuda workflow show workflows/<名前> --socket <tmp>/m.sock`を取り直して差し替える。`0049e12`でdevelopの図は変わっていない
- `../masuda-engine`は別のエージェントが編集中のことがある。gitignore済みの`go.work`があると編集中のengineが混ざるので、ビルド・テストは`GOWORK=off`で行う
- `../masuda-engine`の作業ツリーは別のエージェントが`0049e12`の先で編集中だった。追従はコミット指定（`go get ...@0049e12`）で行った
- `go get ...@main`はプロキシが古いmainを返すことがある。版が上がらなければ`GOPROXY=direct`。次のリリースでengineにタグを打ったら、そのタグへ`go get`し直す
- `docs/user/quickstart.md`の8節・9節の出力例は新しい描画の形で書いたもので、公開物（v0.1.0）の実走ではない。9節の`gate comment`もv0.1.0には無い。リリース時に実走の出力へ差し替えるとよい
- 行コメントの合成はserve側（engineへ渡す直前）で行い、engineの`Decision`や契約は変えていない
- 旧スキーマの計画はstepに`title`が無いので、ステップの見出しは`  1.`だけになる
- `serve`の`TestStallAfterFromLocalSettings`は`./...`一括実行で稀に落ちる（5秒以内にSTALLEDにならない）。単体と再実行では緑。今回は一括でも緑
## 契約への提案
なし
