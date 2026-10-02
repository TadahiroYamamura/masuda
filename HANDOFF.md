# HANDOFF
## 作業項目
M9のうち「M8の実機1周で見つかったもの（優先）」の6項目と、「元から残っていたもの」のResumeのWIP復元。
- `83e6459` **disk_mib**: `buf generate`で`CreateSandboxRequest.disk_mib`を取り込み、`.masuda/settings.json`の`images: {<entry>: {diskMiB}}`（既定4096）をメインVM・特権VMの`CreateSandbox`に渡す。置き場所はsettings.json側を選んだ（egress・secrets・privilegedCommandsと同じく宣言を1ファイルに集め、厳格デコードと検査に載せるため。`.masuda/images/<entry>/settings.json`は宣言が2か所に分かれるので採らなかった）
- `589ea1a` **観点の置き場所**: 起動時に同梱14観点へ定義の写しの`reviews/*.md`を重ねて`records/reviews/`にスナップショット（一度作ったら作り直さない。写しの無い既存ワークスペースは再開時に作る）。ゲストの`/masuda/reviews/`へ`WriteFile`し、`Runner.Items(perspectives)`・`perspectives(from=…)`もこの写しだけから返す。重ね方は従来どおり「同じidは丸ごと置き換え、新しいidは足す」（`masuda init`で全観点を書き出したリポジトリでは実リポジトリのものだけになる）
- `faae24f` **活動の判定**: 原因はフックではなく、sandboxが`http_started`だけを出して`http_finished`を出さなかったリクエストが進行中のまま残ったこと（下の調査結果）。2分経っても終わらないリクエストは進行中に数えず、`idle_prompt`が来たら60秒より前に始まった未完了のものを捨てる。ループ規約（`internal/guest/loop-claude.md`）に「会話で問いかけて待たない」「人間に聞くのは`ask_human`（questionタスクのサブエージェントだけ）、それ以外は宣言済みoutcome＋`feedback`」「`next_task`が返るまで他のことをしない」を足した
- `69b39f6` 起動失敗（Reasonが`sandbox boot failed: `）でBLOCKEDになったワークスペースを`stop`なしで`resume`できる。engineが止めたBLOCKEDは対象外（従来どおりstop→resume）
- `4cc9190` `masuda init`は`.gitignore`に`.masuda`・`.masuda/`・`.masuda/*`・`.masuda/**`（先頭`/`付きも）があれば行を足さない。雛形Dockerfileのコメントに、キャッシュを/tmpへ向ける案内（DockerfileのENVはゲストに引き継がれないので、settings.jsonの`checks`のコマンドと`claudeSettings.env`で渡す）と`-modcacherw`の注意を書いた
- `1dd8eb0` `live/`: `TestDevelopLapOnPythonRepo`（M8段階1の自動化）。**実行はしていない**
- `54218b6` **ResumeのWIP復元**: 再cloneの後、`refs/masuda/wip/*`のうちコミット日時が最新のもの（同秒は出現IDの大きい方）を`<wip> ^<branch>`のbundleでゲストへ渡し、`git read-tree -m -u HEAD FETCH_HEAD`。HEADはブランチのまま、変更はindexに載った未コミットの状態で戻る。再開前に開いていたask_humanの質問（engineがStatusQuestionで待っていないもの）は`discardedAt`・`discardReason: "再開で破棄"`を記録して閉じ、OpenQuestions・Answerの対象から外す。再開後はengineが同じ出現のタスクを渡し直す

### フックが届いていたかの調査結果
- 届いていた。`b7bd968ccd00`の`records/hooks.jsonl`は354行（PostToolUse 272、SubagentStop 41、Stop 38、Notification/idle_prompt 3）、`6020d80e8259`は317行。設定（マッチャー無し、`curl -s -X POST … -d @-`）は変えていない
- `idle_prompt`はメインセッションがターンを終えてから約60秒後に1回出ていた（例 10:40:56 Stop→10:41:53）。バックグラウンドのサブエージェントが動いている間にも出る（その後の活動で消えるので正しい）
- input_waitにならなかった原因: scratchpad `m8/watch2.log`（fixerで止まった回）と`watch4.log`で、`http_started`に対応する`http_finished`が1件ずつ欠けていた（watch2は19:22:16、SubagentStop直後のPOST /v1/messages）。sandboxの`egress.ts`はレスポンスを受けた時だけ`httpFinished`を出すので、応答前に切られたリクエストは進行中のまま残り、`inflight > 0`が`inputWait`より優先されて`working(idle)`に張り付いた。`http_finished`は応答ヘッダーの時点で出るため、実機の最長は約20秒（M8の全ログ）

## 完了した契約テスト
C-M1〜C-M7は緑のまま（`go test -count=1 ./contract/`）。`go build ./...`・`go vet ./...`・`go test ./...`も通る。契約ファイル（両proto、`docs/guest-protocol.md`）と契約テストのassertionは変えていない（`gen/`は`buf generate`で作り直しただけ）
## 未完と理由
- `live/`は書いただけで実行していない（指示どおり。実行は監督）
- M9の残り（`masuda chat`・`WorkflowService`・exports・`docs/design/`への反映等）は次のセッションの範囲
- `docs/design/`への反映はしていない: 観点の写し（`records/reviews/`）、`images.<entry>.diskMiB`、活動の判定の打ち切り（2分・idle_prompt）、BLOCKEDからの再開、WIP復元と質問の破棄。`docs/design/overview.md`の「定義の置き場所」表や「活動の観測」はまだ旧い記述のまま
- 活動の判定は単体テスト（`TestActivityKinds`）とM8のログの読み合わせでしか確かめていない。実機で「問いかけで止まる→60秒後にwaiting_input」になるかはliveテストでは見ていない（ゲートでは止めないため）
## 次の一手
1. `MASUDA_LIVE_TEST=1 go test -count=1 -timeout 60m -v ./live/`を実機で回す（sandboxのS10が入っていればdisk_mibも効く）
2. 下の「契約への提案」の判断
3. M9の残り（`masuda chat`、`WorkflowService`、docs/design/への反映）
## 注意点
- liveテストの前提: `masuda-sandbox serve`が`$XDG_RUNTIME_DIR/masuda-sandbox.sock`（`MASUDA_SANDBOX_SOCKET`で上書き）、トークンは`MASUDA_LIVE_CLAUDE_TOKEN`か`~/.local/share/masuda/claude-oauth-token`。一時データディレクトリ（`/tmp/masuda-live-data-*`）と対象リポジトリ（`/tmp/masuda-live-repo-*`）は失敗時に残る。承認するゲートはplan・review・interimだけで、deviation・triage・質問が開いたら失敗にする。go testの締め切りが45分未満なら即失敗する（`-timeout 60m`が要る）
- 観点の写しは一度作ったら作り直さない。実行中に`.masuda/reviews/`を直しても、その実行には効かない（定義の写しと同じ）
- WIP復元は「最新のWIP」をコミット日時（ゲストの時計）で選ぶ。特権コマンド直前の`refs/masuda/wip/privileged-<run-id>`も候補に入る
- 起動失敗からの再開は、bootの失敗を`Reason`の頭（`bootFailedReason`）で見分けている。Reasonの文言を変えるときは定数を使うこと
- engineの不具合と思われる挙動には当たらなかった
## 契約への提案
- **sandbox: 応答前に切られたHTTPリクエストに終わりのイベントが無い**。`src/egress.ts`の`onResponse`だけが`httpFinished`を出すので、クライアントが応答前に切った（エラーになった）リクエストは`http_started`だけで終わる。masudaは2分の打ち切りで回避した（`serve/activity.go`の`inflightStale`）。再現: M8のscratchpad `m8/watch2.log`（19:22:16のPOST /v1/messages）・`watch4.log`（19:37:30）。`http_finished`に`status=0`（または`aborted`）を出すか、`HttpRequestAborted`を足す提案
- 前回からの持ち越し（判断状況はこちらでは未確認）: sandboxのExecの既定環境（PATHに/usr/local/binが無い、`XDG_CACHE_HOME=/tmp/.cache`がroot所有）、engineのfixerに「直せない」終わり方が無い（`cannot_fix`→`end:unresolved`のような出口）
