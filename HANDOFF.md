# HANDOFF
## 作業項目
実機テスト`TestGuestSubagentContinuation`（`live/continuation_test.go`・`live/testdata/continuation.sh`）を足した（masuda、develop、未push）。ゲストのClaude Codeで、メインセッションが`Agent`で起動したサブエージェントに、`Bash`の`sleep 10`を挟んでターンをまたいだあと`SendMessage`で続きを送り、前の文脈（自分で考えた12文字の文字列）を覚えているかを確かめる。engineのTaskRequestに「出現Xのエージェントを続ける」を足す案の前提の検査。masudaの製品コード・契約・engine・sandboxは変えていない。
- 使い捨てリポジトリに`.masuda/workflows/continuation.yaml`（`exec`ノード1つ）と`scripts/continuation.sh`を置き、`workflows/continuation`をRunしてDONE・outcome `done`で合格。スクリプトはメインの`claude-work`とは別のtmuxセッション`continuation`でclaudeを起こす。サブエージェント`rememberer`は`tools: Write`だけ（token.txtを読んで当てる抜け道を塞ぐ）
- 結果の要約（版、トークンの出どころ、一致したか、メインセッションのツール回数、token.txt・recall.txtを書いたサブエージェントの記録ファイル名）はexecの出力`continuation-report`に書き、テストがログに出す。失敗時はexecのfinish行（LogTail）を出す。exit 2（トークンが無い）は環境の不備として区別する
- `live/live_test.go`冒頭のパッケージ説明に、このテストと単独で回すコマンドを足した

前の作業（コミット済み・未push）: engineのmain `78818dd`に追従し、文書を同梱のレビュー工程の新しい構成に合わせた。
- `go.mod`のengineを`v0.1.1-0.20261003005719-78818dd04f82`（mainの`78818dd`、タグなし）に上げた。`go get ...@main`はプロキシのキャッシュで`1581849`のままだったので、`GOPROXY=direct`で取り直した
- engine側の変更: 同梱のレビュー工程が「観点ごとに1セッション」から「役割ごとに1セッション」になった
  - `workflows/review/perspectives`: review（reviewer、全観点を1セッション）→check-review（review-checker）。reviewerの`clean`もcheckerを通す
  - `workflows/implement/interim-review`（新規）: interim-review→interim-check。reviewerの`clean`はcheckerを経ずに`end:clean`
  - `workflows/develop`: review→cross-cutting→fix（fixer、一括）→recheck（rechecker）→review-commit→report→approve-review→…。fixerの`nothing_to_fix`・`cannot_fix`・`exhausted`とrecheckの`exhausted`はreview-commitへ
  - `workflows/implement/build-step`: implement→test→review（interim-reviewを`with: {diff: step-diff}`）→fix→recheck→commit。reviewの`clean`とfixerの`nothing_to_fix`はcommitへ直結、fixerの`cannot_fix`・`exhausted`とrecheckの`exhausted`はapprove-interimへ
  - `workflows/review`: review→cross-cutting→report→cleanup（discard）
  - 削除: `workflows/review/perspective-review`・`workflows/fix-finding`・`agents/trigger-matcher`。同梱エージェントは11個
- docs: `docs/user/workflows.md`の2つのmermaid図は`masuda serve --fake-sandbox`に対する`masuda workflow show workflows/develop`・`workflows/review`の出力で差し替えた（手書きではない）。同梱ワークフローの表・developの工程の説明・エージェントの個数（12→11）を直した。`docs/user/reviews.md`（trigger-matcherの記述、観点ごとのセッション、`trigger`の無い観点の扱い）、`docs/user/quickstart.md`の8節・9節の工程の説明を直した。出力例は触っていない
- `internal/guest/guest.go`の`ReviewsDir`のコメントのtrigger-matcherをreviewer・review-checkerに直した
- 前の作業（差分ゲート却下時の行コメントの合成、`gate comment`、planゲートの計画の描画）はコミット済み・未push
## 完了した契約テスト
- 実機（2026-10-03、ゲストのClaude Code 2.1.287）: `MASUDA_LIVE_TEST=1 GOWORK=off go test -count=1 -timeout 20m -v -run TestGuestSubagentContinuation ./live/`が2回とも合格（55秒・50秒、VM起動込み）。recallはtoken.txtと一致。メインセッションのツールは`Agent=1, Bash=1, SendMessage=1, ToolSearch=1, Write=1`（SendMessageは遅延読み込みでToolSearchを経た）、token.txtとrecall.txtを書いたサブエージェントの記録は同じ`agent-*.jsonl`だった。つまり今の版では、完了したサブエージェントへターンをまたいでSendMessageで続きを送れ、文脈が残る
- `GOWORK=off go vet ./live/`指摘なし、`GOWORK=off go test -count=1 ./live/`はMASUDA_LIVE_TEST無しで2つともskip
- 前の作業時点: C-M1〜C-M8と`TestCM4_RejectedReviewCarriesLineComments`を含め、`GOWORK=off go test -count=1 ./...`緑、`go build ./...`・`go vet ./...`も指摘なし（今回は製品コードを触っていないので回し直していない）
## 未完と理由
- 実機（live・quickstart）で、新しいレビュー工程の所要時間と指摘の質（1セッションで全観点を回したときの取りこぼし、`id`の形）は未確認
- 実機で、plannerが新スキーマの計画を書けるか、手直しのエージェントが行コメントを踏まえて直すかは未確認
## 次の一手
1. engineのTaskRequestに「出現Xのエージェントを続ける」を足す案を、この結果（今の版で継続が効く）を前提に設計する。masuda側ではメインセッションのループ規約（`internal/guest/loop-claude.md`）で、タスクが継続を求めたら`Agent`ではなく`SendMessage`で委譲する形になる見込み。agentIdの受け渡しは`report_result`の`agent_id?`が既にある
2. 実機1周（quickstart）で、レビュー工程の所要時間（前回は観点レビューだけで9分16秒）と、途中レビューで`clean`直結・`nothing_to_fix`直結が効くかを見る
3. 同じ周回で、テスト漏れの2観点（`trigger`無し）が途中レビューで当たったときの指摘が妥当かを見る（下の注意点）
4. review gateに`gate comment`を付けて却下し、rework/implementのタスクに行コメントが届いて反映されるかを見る
## 注意点
- **ゲストのClaude Codeは`claude.ai/install.sh`の最新版**（2026-10-03時点で2.1.287）。版が上がったら`TestGuestSubagentContinuation`を回す。サブエージェントの継続（SendMessage）の仕組みが変わればここが落ち、「同じエージェントにレビュー指摘の修正を続けさせる」設計の前提が崩れる。版はテストログの`continuation-report`の1行目に出る
- execノードの環境に`CLAUDE_CODE_OAUTH_TOKEN`（プレースホルダ）が入っていた。`serve/settings.go`の`guestEnv`のコメントは「トークンを除く」と書いているので食い違う（経路は未調査。sandboxが秘密のプレースホルダをExecの環境に入れている可能性）。スクリプトは無ければ`tmux show-environment`（`-t claude-work`→`-g`）で取る
- `end:failed`の`failed`はengineの予約ラベルで読み込みが拒否される。continuationの失敗は`end:not_continued`
- 継続テストのclaudeは`--settings '{"disableAllHooks":true}'`で起こす。フックはmasudaの`/hooks`に届き、このセッションのSessionEndがメインセッションの死（DEAD）と区別できないため
- 2026-10-03のこの作業の開始時、`masuda-sandbox serve`は起動していなかった（ソケットが無い）。`cd ~/work/masuda-sandbox && node dist/cli.js serve --socket $XDG_RUNTIME_DIR/masuda-sandbox.sock`で起こし、そのまま動かしてある
- `trigger`の無い観点の扱いが変わった。旧trigger-matcherは「`trigger`を持たない観点は選ばない」だったが、engine `78818dd`のreviewer.mdは「途中レビュー: `trigger`を持たない観点は常に当てる」。同梱の`missing-tests-guard-clauses`・`missing-tests-new-code`も途中レビューで毎回当たる。これはユーザー判断で現状のまま確定（計画で実装とテストを同じステップに入れる方針になったため、テスト漏れの観点を途中で当てる意味がある）。`docs/user/reviews.md`はこの挙動で書いてある
- reviewerは「実行位置」のノード名が`interim-`で始まるかで途中レビューを判別する。masudaの`internal/runner/task.go`が出す「実行位置: ワークフロー…のノード…」の形を変えると壊れる
- `docs/user/workflows.md`の図は`masuda workflow show`の出力の貼り付け。同梱定義が変わったら、`masuda serve --fake-sandbox --data-dir <tmp> --socket <tmp>/m.sock`を立て、リポジトリの外のディレクトリで`masuda workflow show workflows/develop --socket <tmp>/m.sock`を取り直して差し替える
- `../masuda-engine`は別のエージェントが編集中のことがある。gitignore済みの`go.work`があると編集中のengineが混ざるので、ビルド・テストは`GOWORK=off`で行う
- `go get ...@main`はプロキシが古いmainを返すことがある。版が上がらなければ`GOPROXY=direct`。次のリリースでengineにタグを打ったら、そのタグへ`go get`し直す
- `docs/user/quickstart.md`の8節・9節の出力例は新しい描画の形で書いたもので、公開物（v0.1.0）の実走ではない。9節の`gate comment`もv0.1.0には無い。リリース時に実走の出力へ差し替えるとよい
- 行コメントの合成はserve側（engineへ渡す直前）で行い、engineの`Decision`や契約は変えていない
- 旧スキーマの計画はstepに`title`が無いので、ステップの見出しは`  1.`だけになる
- `serve`の`TestStallAfterFromLocalSettings`は`./...`一括実行で稀に落ちる（5秒以内にSTALLEDにならない）。単体と再実行では緑。今回は一括でも緑
## 契約への提案
なし
