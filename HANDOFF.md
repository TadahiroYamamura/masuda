# HANDOFF
## 作業項目
engineのmain `0049e12`（push済み）に追従した（masuda、develop、未push）。契約は変えていない。
- `go.mod`のengineは`v0.1.1-0.20261003015328-0049e12eeb09`。engine側の変更は2つ: implementerの入力が`[plan, investigation]`になった（`2324d1d`）、小さな修正向けの`workflows/fix`と役`agents/quick-planner`が同梱された（`0049e12`。同梱エージェントは12個）
- `needs_human`の見せ方: `end:<ラベル>`で終わると、これまでも`Workspace.outcome`にラベルが入り`masuda list`のPOSITIONに`outcome needs_human`は出ていたが、役のfeedback（疑問）は`masuda watch`のfinish行（engineが200字で切った1行目）と`records/engine.json`にしか無かった。serveが受け付けた最後の`report_result`のfeedbackを覚え（`runCtl.endFeedback`）、done以外の終わり方で`done`になったら`Workspace.reason`に入れるようにした（`serve/run.go`の`reflect`）。`masuda list`はoutcomeとreasonが両方あれば`outcome needs_human: <理由の1行目>`を出す（`cmd/masuda/client.go`の`listRow`）。`watch`のstatus行は元から`reason=`と`outcome=`を出す。`out_of_scope`・`stuck`も同じ経路で理由が見えるようになった
- docs: `docs/user/workflows.md`（同梱の表に`workflows/fix`・`workflows/fix/build-step`、`### fix {#fix}`の節と`masuda workflow show workflows/fix`の図、developにimplementerが`investigation`を読む1行、上書きの節にimplementerを自前で使うときの注意、エージェント12個）、`quickstart.md`（6節に`workflows/fix`の案内1行、「途中で止まったら」に`needs_human`）、`operations.md`（一覧の節に`end:<ラベル>`の終わり方と`reason`、ホストの記録に`{#host-records}`）、`cli.md`（list）、`troubleshooting.md`（`out_of_scope`の理由の見方を差し替え、`needs_human`の行）、`reviews.md`（`fix`も観点で行い、途中レビューが無い）

その前（コミット済み・未push）: ゲストのClaude Codeの版を固定し、リリース手順を「リリースのたびに最新版へ上げて実機で検証する」形にした。
- 正は`internal/guest`の`ClaudeCodeVersion = "2.1.287"`。`masuda init`の雛形のDockerfile（`cmd/masuda/templates/Dockerfile`の`__CLAUDE_CODE_VERSION__`・`__MASUDA_VERSION__`を`init.go`の`renderTemplate`で置換）、liveの`pythonRepoFiles`のDockerfile（継続テストも同じものを使う）、`masuda version`の`  claude code: 2.1.287 (guest, verified)`の行が参照する
- `masuda image build`の開始時（`cmd/masuda/claudecode.go`）に、Dockerfileのinstall行が無引数・`stable`・`latest`か検証済みと違う版なら標準エラーに`note:`の1行
- リリース手順（`.claude/skills/release/SKILL.md`）の「1-0. ゲストのClaude Codeの版を上げる」、`claude-code-latest.sh`、`precheck.sh --claude-code`、`release.yml`のノートの行

前の作業（コミット済み・未push）: 実機テスト`TestGuestSubagentContinuation`（`live/continuation_test.go`・`live/testdata/continuation.sh`）を足した。メインセッションが`Agent`で起動したサブエージェントに、ターンをまたいで`SendMessage`で続きを送り、前の文脈が残るかを確かめる。engineのTaskRequestに「出現Xのエージェントを続ける」を足す案の前提の検査。結果はexecの出力`continuation-report`に書き、テストがログに出す。

その前（コミット済み・未push）: engineのmain `78818dd`に追従し、文書を同梱のレビュー工程の新しい構成（役割ごとに1セッション。reviewer→review-checker、fixer→rechecker、`interim-review`の新設、trigger-matcher・perspective-review・fix-findingの削除）に合わせた。`docs/user/workflows.md`の図は`masuda workflow show`の出力で差し替え済み。
## 完了した契約テスト
- 2026-10-03（engine `0049e12`追従）: `GOWORK=off go build ./... && go vet ./... && go test -count=1 ./...`緑（C-M*を含む。新しい同梱定義のままテストの直しは不要だった）。足したテスト: `serve/run_test.go`の`TestNeedsHumanEndCarriesFeedbackAsReason`（同梱の`workflows/fix`を実際に始め、quick-plannerが`needs_human`で報告すると`outcome needs_human`・`reason`＝feedbackの`done`になり、写し直しても消えない。変更前のコードでは落ちることを確認）、`TestListRow`に`outcome needs_human: <1行目>`の形
- 2026-10-03: `GOWORK=off go build ./... && go vet ./... && go test -count=1 ./...`緑（C-M*を含む）。足したテスト: `TestClaudeCodeNote`・`TestImageClaudeCodeNoteFollowsSettingsImage`・`TestPrintVersionShowsVerifiedClaudeCode`（cmd/masuda）、`TestLiveDockerfilePinsClaudeCode`（live、VM無しで常に走る）、`TestInitRepoWritesTemplatesAndKeepsExistingFiles`に版の検査
- `precheck.sh --claude-code`が通る（2.1.287のmanifestは200。最新版は2.1.288なのでinfoが出る）。`claude-code-latest.sh`は2.1.288を出した
- 実機: 版付きのDockerfileで`TestGuestSubagentContinuation`が合格（1m35s、イメージの作り直し込み）。`claude --version: 2.1.287`、recallは一致
- 前の作業: 継続テストは2.1.287で2回合格（55秒・50秒）。メインセッションのツールは`Agent=1, Bash=1, SendMessage=1, ToolSearch=1, Write=1`、token.txtとrecall.txtを書いたサブエージェントの記録は同じ`agent-*.jsonl`。今の版では完了したサブエージェントへターンをまたいでSendMessageで続きを送れ、文脈が残る
## 未完と理由
- 実機で`workflows/fix`を1周させていない（quick-plannerの調査結果と計画の質、所要時間、`needs_human`を選ぶ判断の妥当さ）
- `reason`に入るfeedbackは、serveが覚えている「最後に受け付けた報告」のもの。serveを起こし直した後に初めて`done`へ写る場合は空なので、前の`reason`を残す（`needs_human`は計画の役の報告の直後に終わるので実際には起きない）
- 実機1周（`TestDevelopLapOnPythonRepo`）は版付きのDockerfileでは回していない（指示どおり。次のリリースの1-0で最新版にして回す）
- 実機（live・quickstart）で、新しいレビュー工程の所要時間と指摘の質（1セッションで全観点を回したときの取りこぼし、`id`の形）は未確認
- 実機で、plannerが新スキーマの計画を書けるか、手直しのエージェントが行コメントを踏まえて直すかは未確認
## 次の一手
1. 実機（quickstartの三角形の課題等）で`masuda run workflows/fix`を1周させる。あわせて曖昧な指示書で`needs_human`になり、`masuda list --all`に疑問が出ることを見る
2. 次のリリースで、SKILL.mdの1-0に従ってClaude Codeを最新版（2026-10-03時点で2.1.288）へ上げ、継続テスト→1周で検証する
3. engineのTaskRequestに「出現Xのエージェントを続ける」を足す案を、継続テストの結果（今の版で継続が効く）を前提に設計する。masuda側ではメインセッションのループ規約（`internal/guest/loop-claude.md`）で、タスクが継続を求めたら`Agent`ではなく`SendMessage`で委譲する形になる見込み。agentIdの受け渡しは`report_result`の`agent_id?`が既にある
4. 実機1周（quickstart）で、レビュー工程の所要時間（前回は観点レビューだけで9分16秒）と、途中レビューで`clean`直結・`nothing_to_fix`直結が効くかを見る
5. 同じ周回で、テスト漏れの2観点（`trigger`無し）が途中レビューで当たったときの指摘が妥当かを見る（下の注意点）
6. review gateに`gate comment`を付けて却下し、rework/implementのタスクに行コメントが届いて反映されるかを見る
## 注意点
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
