# HANDOFF
## 作業項目
engineのmain `78818dd`に追従し、文書を同梱のレビュー工程の新しい構成に合わせた（masuda、develop、未push）。
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
C-M1〜C-M8と`TestCM4_RejectedReviewCarriesLineComments`を含め、`GOWORK=off go test -count=1 ./...`緑（`./contract/`は新しい同梱定義のままテストの修正なしで通った）。`GOWORK=off go build ./...`・`go vet ./...`も指摘なし
## 未完と理由
- 実機（live・quickstart）で、新しいレビュー工程の所要時間と指摘の質（1セッションで全観点を回したときの取りこぼし、`id`の形）は未確認
- 実機で、plannerが新スキーマの計画を書けるか、手直しのエージェントが行コメントを踏まえて直すかは未確認
## 次の一手
1. 実機1周（quickstart）で、レビュー工程の所要時間（前回は観点レビューだけで9分16秒）と、途中レビューで`clean`直結・`nothing_to_fix`直結が効くかを見る
2. 同じ周回で、テスト漏れの2観点（`trigger`無し）が途中レビューで毎回当たって、直せない指摘で`interim`ゲートが開かないかを見る（下の注意点）
3. review gateに`gate comment`を付けて却下し、rework/implementのタスクに行コメントが届いて反映されるかを見る
## 注意点
- `trigger`の無い観点の扱いが変わった。旧trigger-matcherは「`trigger`を持たない観点は選ばない」だったが、engine `78818dd`のreviewer.mdは「途中レビュー: `trigger`を持たない観点は常に当てる」。同梱の`missing-tests-guard-clauses`・`missing-tests-new-code`は「テストは後のステップで書く計画では途中で指摘しても直せない」ので`trigger`を持たせていなかったもので、今は途中レビューでも毎回当たる。engineのコミットに意図の記述が無く、意図しない反転の可能性が高い。`docs/user/reviews.md`は今の挙動（常に当てる）で書いた。engine側でreviewer.md（とreview-checker.md）を「`trigger`を持たない観点は途中レビューでは当てない」に戻すなら、reviews.mdの`trigger`の行・場面の表・同梱14観点の表のテスト漏れ2行・その下の段落を元の意味に戻す
- reviewerは「実行位置」のノード名が`interim-`で始まるかで途中レビューを判別する。masudaの`internal/runner/task.go`が出す「実行位置: ワークフロー…のノード…」の形を変えると壊れる
- `docs/user/workflows.md`の図は`masuda workflow show`の出力の貼り付け。同梱定義が変わったら、`masuda serve --fake-sandbox --data-dir <tmp> --socket <tmp>/m.sock`を立て、リポジトリの外のディレクトリで`masuda workflow show workflows/develop --socket <tmp>/m.sock`を取り直して差し替える
- `../masuda-engine`は別のエージェントが編集中のことがある。gitignore済みの`go.work`があると編集中のengineが混ざるので、ビルド・テストは`GOWORK=off`で行う
- `go get ...@main`はプロキシが古いmainを返すことがある。版が上がらなければ`GOPROXY=direct`。次のリリースでengineにタグを打ったら、そのタグへ`go get`し直す
- `docs/user/quickstart.md`の8節・9節の出力例は新しい描画の形で書いたもので、公開物（v0.1.0）の実走ではない。9節の`gate comment`もv0.1.0には無い。リリース時に実走の出力へ差し替えるとよい
- 行コメントの合成はserve側（engineへ渡す直前）で行い、engineの`Decision`や契約は変えていない
- 旧スキーマの計画はstepに`title`が無いので、ステップの見出しは`  1.`だけになる
- `serve`の`TestStallAfterFromLocalSettings`は`./...`一括実行で稀に落ちる（5秒以内にSTALLEDにならない）。単体と再実行では緑。今回は一括でも緑
## 契約への提案
- `docs/guest-protocol.md`の`/masuda/reviews/*.md`の行が「trigger-matcherと`Runner.Items(perspectives)`は**ここ**を読む」と書いている。trigger-matcherは削除されたので「同梱のreviewer・review-checkerと`Runner.Items(perspectives)`」に直すことを提案する（意味の変更ではなく記述の追随）。契約なので触っていない
