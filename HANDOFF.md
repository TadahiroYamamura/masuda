# HANDOFF
## 作業項目
engineのmain `f58bb1b`（push済み）へ追従し、4つの作業をした（masuda、develop、未push）。protoは変えていない。契約`docs/guest-protocol.md`は「起動時にホストがゲストへ置くもの」の表に行を足しただけ（ユーザー承認済み）。
- `chore(deps)`: engineを`f58bb1b`へ（`0300b66`: implementer・fixerが`comment-manifest`を出力しreviewerが入力に取る、`54cadac`: 同梱の役15個のtoolsに`Skill`、`f58bb1b`: plan-questionsが計画の`checks`と`settings.json`の`checks`を取り違えないように）。masudaのテストの直しは不要だった
- `feat(guest)`（A）: `.masuda/claude/`（共有）と`.masuda/claude.local/`（個人、gitignore）を重ねて（同じ相対パスは`.local`が勝つ。CLAUDE.mdも置き換え）ゲストの`~/.claude/`へ置く。新パッケージ`internal/claudedir`（`Merge`・`Warning`・`Write`・`Load`）。置くのは`CLAUDE.md`・`rules/*.md`・`skills/<name>/`以下（再帰、シンボリックリンクは除く）だけで、ほかは無視してserveの標準エラーに1行の警告。`copyDefinitions`は生の2ディレクトリを写さず、重ねた結果を`records/definitions/claude/`に置き、再開もそれを使う。`guest.ClaudeMD`がループ規約の後ろに`# プロジェクトのルール（.masuda/claude）`＋「ループ規約が優先」の1行＋本文を連結。`masuda init`は`.gitignore`に`.masuda/claude.local/`を足す（雛形のディレクトリは作らない）
- `feat(perspectives)`（B）: 同梱の観点`comment-criteria`（15個目）。`comment-manifest`と差分のコメントを`file`と近くの内容で照合し、一覧に無い・基準が成り立たない・テストヘルパーのコメントは`autofix: true`で削除、nolintの書式違いは`autofix: true`で書式。一覧が空なら基準だけで判定。経緯の混入は`comment-history-leakage`に任せる
- `test(live)`（C・D）: `destroyVMOnCleanup`（成功時はRemove、失敗時・`MASUDA_LIVE_KEEP=1`はStop＋sandbox serviceへ直接DestroySandboxでVMだけ壊す）を全VMテストに。`live/claude_dir_test.go`の`TestClaudeDirReachesSubagent`・`TestClaudeDirDefinitionsCheck`
- `docs(user)`: settings.md（`{#claude-dir}`）、concepts.md（「エージェントへの指示をどこに書くか」`{#where-to-write}`）、reviews.md（15観点・comment-criteria）、workflows.md（`comment-manifest`、自前の役は`tools`に`Skill`、見出しに`{#write-agent}`）
## 完了した契約テスト
- 2026-10-03（engine `f58bb1b`）: `GOWORK=off go build ./... && go vet ./... && go test -count=1 ./...`緑。足したテスト: 契約テスト`TestCM10_ClaudeDirPlacedInGuest`（共有と`.local`の合成・CLAUDE.mdの連結・rules/skillsの配置・`agents/`と`settings.json`を置かない・作業ツリーを書き換えてStop→Resumeしても開始時の写しのまま）、`internal/claudedir`の単体テスト（上書き・無視・警告・往復）、`TestClaudeMDAppendsProjectRulesAfterLoopRules`（guest）、観点の数のテストを15へ、`TestInitRepoWritesTemplatesAndKeepsExistingFiles`に`.masuda/claude.local/`の行
- 実機（`MASUDA_LIVE_TEST=1 GOWORK=off go test -count=1 -timeout 20m -v -run 'TestClaudeDir|TestEngineContinuation|TestGuestSubagentContinuation' ./live/`、1回目で全合格、193秒）。終了後`pgrep -c qemu-system`は0
  - `TestClaudeDirReachesSubagent`（33秒）: `probe`は`RULE-MARK-7f3a`／`echo-markの結果: SKILL-MARK-9c1d`／`LOCAL-MARK-2b8e`。ツールは`Agent=1, Read=3, Skill=1, ToolSearch=2, next_task=1, write_output=1, report_result=1`。ユーザースコープの`~/.claude/rules/`・`~/.claude/skills/`・`~/.claude/CLAUDE.md`の連結部分がサブエージェントに効く（Claude Code 2.1.287）
  - `TestGuestSubagentContinuation`（57秒）、`TestEngineContinuationKeepsMemory`（44秒、recall一致、SendMessage=1）、`TestEngineContinuationFallsBackAfterResume`（56秒、SendMessage=0、Agent=2）
- 前回までの記録は`git log`（`52182c1`以前のHANDOFF）を参照
## 未完と理由
- `comment-criteria`と`comment-manifest`は実機で1周させていない（implementerが実際に一覧を書くか、基準を言えないコメントを消すか、reviewerが照合するかは未確認）
- 人間への質問（plan-interviewerの`ask_human`→`question answer`→`revise-answered`）は実機で一度も通っていない（前回から持ち越し）
- 記憶の無いサブエージェントに「前に書いた文字列」を求める課題はAPIの安全分類器に止められる件（前回から持ち越し。本番の続きは入力を持つので同じ形にはならない見込み）
## 次の一手
1. 実機でdevelopを1周させ、implementer・fixerの`comment-manifest`と、`comment-criteria`の指摘（一覧に無い・基準が成り立たないコメントの削除）を出力で見る。review-checkerは一覧を読めないので、誤検知の判定がぶれないかも見る
2. 曖昧な指示書でdevelopを回し、plan-interviewerの質問が`masuda question list`に出て、答えが`checks`に反映されることを実機で見る
3. `.masuda/pitfalls.jsonl`を置いた1周で、plan-questionsが落とし穴を問いに加えるかを見る
4. 次のリリース（**v0.2.0**）で、SKILL.mdの1-0に従ってClaude Codeを最新版へ上げ、継続テストと`TestClaudeDirReachesSubagent`→1周で検証する。engineにタグを打ったらそのタグへ`go get`し直す
5. review gateに`gate comment`を付けて却下し、行コメントが反映されるかを見る
6. `docs/user/quickstart.md`の8節・9節の出力例を実走の出力へ差し替える
## 注意点
- **`end`で終わる（publish・discardを通らない）ワークフローではVMが残る**。DONEのワークスペースにはStopが効かず（FailedPrecondition）、Remove（または`masuda remove`）でしか壊れない。liveは後始末で対処したが、利用者の`workflows/review`等も同じはず。serveがengineのdoneでsandboxを壊すべきかは未判断（直していない）
- `.masuda/claude/`で無視したものの警告は`masuda serve`の標準エラーにしか出ない（protoを変えないため`masuda run`の応答には載らない）。`masuda workflow check`も検査しない
- `docs/guest-protocol.md`の`/masuda/reviews/*.md`の行は「同梱の14観点」のままにした（表は指定された行だけを触る約束のため）。次に契約を触るときに15へ
- `contract/contract_test.go`の冒頭には「監督が所有、実装者は書き換えない」とあるが、依頼によりC-M10を足した。`docs/work-orders.md`の対応表にはC-M9・C-M10が無い
- liveの後始末: VMを使うテストはserveとctxの後始末を`t.Cleanup`で`destroyVMOnCleanup`より先に登録すること（deferにするとAPIが先に閉じ、VMを壊せない）
- `docs/user/reference/workflow-schema.md`はengineの写し（`scripts/docs-prepare.sh`）で、`comment-manifest`はまだ反映していない。サイトのビルド時に取り込み直す
- live 2本を続けて回すときは`-timeout 100m`。2本目は開始時に`lapBudget`（45分）の残りを求めるので、`-timeout 60m`では1本目が15分を超えると2本目が失敗する
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
- `go get ...@main`はプロキシが古いmainを返すことがある。版が上がらなければ`GOPROXY=direct`。次のリリースでengineにタグを打ったら、そのタグへ`go get`し直す
- `docs/user/quickstart.md`の8節・9節の出力例は新しい描画の形で書いたもので、公開物（v0.1.0）の実走ではない。9節の`gate comment`もv0.1.0には無い。リリース時に実走の出力へ差し替えるとよい
- 行コメントの合成はserve側（engineへ渡す直前）で行い、engineの`Decision`や契約は変えていない
- 旧スキーマの計画はstepに`title`が無いので、ステップの見出しは`  1.`だけになる
- `serve`の`TestStallAfterFromLocalSettings`は`./...`一括実行で稀に落ちる（5秒以内にSTALLEDにならない）。単体と再実行では緑。今回は一括でも緑
## 契約への提案
- `docs/guest-protocol.md`の「起動時にホストがゲストへ置くもの」の表への追記（`/masuda/pitfalls.jsonl`、`~/.claude/CLAUDE.md`への`.masuda/claude/`の`CLAUDE.md`の連結、`~/.claude/rules/`、`~/.claude/skills/`）は**反映済み**（ユーザー承認済み、`feat(guest)`のコミット）
