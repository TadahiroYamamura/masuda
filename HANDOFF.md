# HANDOFF
## 作業項目
engineのmain `8af56b2`（push済み、計画に問いを立てて答える工程の同梱）に追従した（masuda、develop、未push）。契約（`masuda.proto`・`docs/guest-protocol.md`）は変えていない。
- `chore(deps)`: `go get ...@8af56b2`。`plan`のスキーマに必須の`checks`が増えたので、契約テスト・クライアントのテストの計画の固定値に`"checks":[]`
- `feat(cli)`: `gate show`（target: plan）が`checks`をstepsの後・alternativesの前に`<id> [addressed|out_of_scope|open] <問い>`＋答え（空なら省く）で出す。`question list`は本文の2行目以降を字下げし、最後に`answer: masuda question answer <id> <occ> '<qid>=<answer>' ...`を添える（`formatQuestion`）。QuestionService（サーバー側）は複数の問いを既に扱えていたので変えていない
- `feat(pitfalls)`: `.masuda/pitfalls.jsonl`（`internal/pitfalls.Parse`。1行1件`{id, category, trigger, question, background}`、全必須・空白だけも不可・余分なキー不可・idに空白不可・categoryは8値、空行と`#`行は飛ばす）。Runは写した定義（`records/definitions/`）で検査し、不正ならワークスペースを作る前にInvalidArgument（全誤り行の行番号と理由）。ゲストの`/masuda/pitfalls.jsonl`（`guest.PitfallsPath`）へ注釈を除いて置く（無い・注釈だけなら置かない）。再開は写しから読み直す（`serve/boot.go`の`loadPitfalls`）。`workflow check`は誤り行ごとに`Path: pitfalls.jsonl`の問題を返す。`masuda init`は作らない
- `docs(user)`: workflows.md（developの工程・図の再生成・エージェント15個・planner差し替え時の`checks`）、quickstart.md（8節と「途中で止まったら」`{#stuck}`）、cli.md（gate show・question）、settings.md（`pitfalls.jsonl` `{#pitfalls}`）、reviews.md（観点と落とし穴の違い）
- `test(live)`: `driveLap`が開いた質問に固定の答えを返す（`answerOpenQuestions`）、`MASUDA_LIVE_KEEP=1`で成功しても残す、`TestFixLapOnPythonRepo`（`runLap`を共有）

その前（コミット済み・未push）: engine `4abd0c4`の`continues`をゲストまで配達し（契約`docs/guest-protocol.md`を変更、ユーザー承認済み）、`ba1d49c`へ追従した。`next_task(agent_id)`、`records/subagents.json`、タスクファイルの「## 続き」、ループ規約の「サブエージェントの続き」。その前: `needs_human`等のfeedbackを`Workspace.reason`に、ゲストのClaude Codeの版を`internal/guest.ClaudeCodeVersion`（2.1.287）で固定。
## 完了した契約テスト
- 2026-10-03（engine `8af56b2`追従）: `GOWORK=off go build ./... && go vet ./... && go test -count=1 ./...`緑。足したテスト: `TestFormatGatePlanChecks`・`TestFormatQuestionWithSeveralItems`（cmd/masuda）、`TestAskHumanWithSeveralQuestions`（serve。1回のask_humanの2問が1つの質問の2項目に見え、片方だけの答えはInvalidArgument、両方でask_humanに返る）、`internal/pitfalls`の単体テスト、`TestRunPlacesPitfallsInGuest`・`TestRunWithoutPitfallsPlacesNothing`・`TestInvalidPitfallsRefuseRunAndShowInCheck`（serve、フェイクsandbox）
- 実機（engine `8af56b2`、`MASUDA_LIVE_KEEP=1 MASUDA_LIVE_TEST=1 GOWORK=off go test -count=1 -timeout 100m -run 'TestDevelopLapOnPythonRepo|TestFixLapOnPythonRepo' ./live/`）: develop 7m48s・fix 2m6s で両方合格
  - develop: plan-questionsが6件（SPEC 3・REGRESSION 1・MAINTAINABILITY 2）。plan-reviserは全件`addressed`で`done`（NaN/infの`isfinite`検査とsqrtの`max(0.0, ...)`を計画に足した）。`open`が無く質問は来なかった（人間への質問の経路は実機では未通過）。途中レビューの指摘1件は`autofix: false`でfixerが`nothing_to_fix`、interimゲートは開かなかった。fixer（0000012・0000020）はimplementer（0000007）と同じagentIdで続いた
  - fix: quick-plannerの計画の`checks`は`[]`、レビューは`clean`
- 2026-10-03（engine `4abd0c4`追従・continues）: `GOWORK=off go build ./... && go vet ./... && go test -count=1 ./...`緑。足したテスト: 契約テスト`TestCM9_ContinuesCarriesReportedAgentID`（`continues`付きのワークフローで、続きのタスクの`continues`に宛先の出現と`next_task(agent_id)`で報告したIDが出る、タスクファイルに「## 続き」、Stop→Resumeの後は`occurrence`だけで、古いIDを渡しても結び付かない）、`TestTaskFileContinues`（runner）、`TestLoopRulesDescribeContinuation`（guest）、`TestEngineContinuationDefinitionsCheck`（live、VM不要）
- 実機（engine `ba1d49c`）: `TestEngineContinuationKeepsMemory`合格（32秒、recall一致）、`TestEngineContinuationFallsBackAfterResume`合格（64秒。recall＝token、再開前のIDの記録`{0000001: ade1…}`が再開で消え、後に`{0000003: ab31…}`（別のID）、メインセッションはSendMessageを使わず`Agent`でcopierを起動）
- 実機: `TestEngineContinuationKeepsMemory`はそれ以前にも3回合格（62秒・37秒・37秒）。recallはtokenと一致（例`k7Qm2xV9bR4t`）。メインセッションのツールは`Agent=1, SendMessage=1, ToolSearch=2, Read=3`、`subagents.json`に`{"0000001": "<agentId>"}`。Claude Code 2.1.287の`Agent`は非同期起動（`async_launched`）で、完了は通知で届く
- 2026-10-03（engine `0049e12`追従）: `GOWORK=off go build ./... && go vet ./... && go test -count=1 ./...`緑（C-M*を含む。新しい同梱定義のままテストの直しは不要だった）。足したテスト: `serve/run_test.go`の`TestNeedsHumanEndCarriesFeedbackAsReason`（同梱の`workflows/fix`を実際に始め、quick-plannerが`needs_human`で報告すると`outcome needs_human`・`reason`＝feedbackの`done`になり、写し直しても消えない。変更前のコードでは落ちることを確認）、`TestListRow`に`outcome needs_human: <1行目>`の形
- 2026-10-03: `GOWORK=off go build ./... && go vet ./... && go test -count=1 ./...`緑（C-M*を含む）。足したテスト: `TestClaudeCodeNote`・`TestImageClaudeCodeNoteFollowsSettingsImage`・`TestPrintVersionShowsVerifiedClaudeCode`（cmd/masuda）、`TestLiveDockerfilePinsClaudeCode`（live、VM無しで常に走る）、`TestInitRepoWritesTemplatesAndKeepsExistingFiles`に版の検査
- `precheck.sh --claude-code`が通る（2.1.287のmanifestは200。最新版は2.1.288なのでinfoが出る）。`claude-code-latest.sh`は2.1.288を出した
- 実機: 版付きのDockerfileで`TestGuestSubagentContinuation`が合格（1m35s、イメージの作り直し込み）。`claude --version: 2.1.287`、recallは一致
- 前の作業: 継続テストは2.1.287で2回合格（55秒・50秒）。メインセッションのツールは`Agent=1, Bash=1, SendMessage=1, ToolSearch=1, Write=1`、token.txtとrecall.txtを書いたサブエージェントの記録は同じ`agent-*.jsonl`。今の版では完了したサブエージェントへターンをまたいでSendMessageで続きを送れ、文脈が残る
## 未完と理由
- 人間への質問（plan-interviewerの`ask_human`→`question answer`→`revise-answered`）は実機で一度も通っていない。三角形の課題ではplan-reviserが全問に答えてしまう。曖昧な指示書（例: 0の辺の扱いを書かない、公開APIを決めさせる）で開かせて確かめる
- 記憶の無いサブエージェントに「前に考えた／書いた文字列」を求める課題は、APIの安全分類器（`[reasoning_extraction]`）に止められる（2026-10-03に3回）。fallbackのテストは入力を渡す形に作り直して解消済み。本番の続きのタスクは常に入力を持つので同じ形にはならない見込みだが、分類器で止まったメインセッションは入力待ちのまま進まない（STALLED扱いになるかは未確認）
## 次の一手
1. 曖昧な指示書でdevelopを回し、plan-interviewerの質問（複数の問い）が`masuda question list`に出て、`question answer`で答えると`revise-answered`が答えを`checks`に反映してplan gateが開くことを実機で見る（liveの`answerOpenQuestions`はそのまま使える）
2. `.masuda/pitfalls.jsonl`を置いた1周で、plan-questionsが落とし穴を問いに加えるか（categoryの引き継ぎ・具体化）を見る
3. 実機1周（quickstart・`workflows/develop`か`fix`）で、反論（`disputed`）とrecheckerの裁定が妥当かを見る（fixerがimplementerの続きとして動くことは2026-10-03の1周で確認済み）
4. 次のリリース（**v0.2.0**）で、SKILL.mdの1-0に従ってClaude Codeを最新版へ上げ、継続テスト（`TestGuestSubagentContinuation`・`TestEngineContinuation*`）→1周で検証する。engineにタグを打ったらそのタグへ`go get`し直す
5. review gateに`gate comment`を付けて却下し、rework/implementのタスクに行コメントが届いて反映されるかを見る
6. `docs/user/quickstart.md`の8節・9節の出力例を実走の出力へ差し替える（8節のplan gateの出現IDは今のdevelopでは`0000005`前後にずれる）
## 注意点
- **plan-questionsが計画の`checks`と`settings.json`の`checks`（テストのコマンド）を取り違えた**（2026-10-03の1周、MAINTAINABILITY-1「planのchecksが空配列だが、settings.jsonの検査コマンドをchecksに載せなくても検証工程で実行されると言えるか」）。plan-questionsは計画の`checks`が空（plannerは問いを書かない）なのを見て問いにした。engineの役の本文で「`checks`はこの後の工程が書く問いの答え欄」と伝えるか、名前を変えるかはengine側の判断（直していない）
- live 2本を続けて回すときは`-timeout 100m`。2本目は開始時に`lapBudget`（45分）の残りを求めるので、`-timeout 60m`では1本目が15分を超えると2本目が失敗する
- `/masuda/pitfalls.jsonl`は契約`docs/guest-protocol.md`の「起動時にホストがゲストへ置くもの」の表に無い（下の「契約への提案」）
- 落とし穴の写しは定義の写し（`records/definitions/pitfalls.jsonl`）がそのまま兼ねる。観点（`records/reviews/`）のような別のスナップショットは作っていない（同梱が無く重ねる相手がいないため）
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
- `docs/user/workflows.md`の図は`masuda workflow show`の出力の貼り付け（`develop`・`fix`・`review`の3つ）。同梱定義が変わったら、`masuda serve --fake-sandbox --data-dir <tmp> --socket <tmp>/m.sock`を立て、リポジトリの外のディレクトリで`masuda workflow show workflows/<名前> --socket <tmp>/m.sock`を取り直して差し替える。`8af56b2`でdevelopの図を取り直した（fix・reviewは一致していた）
- `../masuda-engine`は別のエージェントが編集中のことがある。gitignore済みの`go.work`があると編集中のengineが混ざるので、ビルド・テストは`GOWORK=off`で行う
- `../masuda-engine`の作業ツリーは別のエージェントが`0049e12`の先で編集中だった。追従はコミット指定（`go get ...@0049e12`）で行った
- `go get ...@main`はプロキシが古いmainを返すことがある。版が上がらなければ`GOPROXY=direct`。次のリリースでengineにタグを打ったら、そのタグへ`go get`し直す
- `docs/user/quickstart.md`の8節・9節の出力例は新しい描画の形で書いたもので、公開物（v0.1.0）の実走ではない。9節の`gate comment`もv0.1.0には無い。リリース時に実走の出力へ差し替えるとよい
- 行コメントの合成はserve側（engineへ渡す直前）で行い、engineの`Decision`や契約は変えていない
- 旧スキーマの計画はstepに`title`が無いので、ステップの見出しは`  1.`だけになる
- `serve`の`TestStallAfterFromLocalSettings`は`./...`一括実行で稀に落ちる（5秒以内にSTALLEDにならない）。単体と再実行では緑。今回は一括でも緑
## 契約への提案
- `docs/guest-protocol.md`の「起動時にホストがゲストへ置くもの」の表に1行足す: `/masuda/pitfalls.jsonl` | プロジェクト固有の落とし穴。ホストの`.masuda/pitfalls.jsonl`を実行開始時に写して検査し、空行と`#`の行を除いたもの（1行1件`{id, category, trigger, question, background}`）。無ければ置かない。同梱のplan-questionsが読む。実装は既にこの通りに置いている（表に無いだけ）
