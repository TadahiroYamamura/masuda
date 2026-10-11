# HANDOFF
## 作業項目
2026-10-10〜11。v0.4.0を出し（手順5まで）、v0.5.0の#85・#83を実装してdevelopに入れた（`7b778c1`、push済み）。

**v0.4.0**（#107）: masuda・engine・sandboxにv0.4.0のタグ（masuda `04daf1f`、engine `457039b`、sandbox `4dd3fb4`）。公開後の確認まで済み。残りは手順6（ハーネスの更新、v0.3.0→v0.4.0）と手順7（記録）。手順6はハーネスのワークスペースが空いてから、ユーザーがコマンドを打つ。リリース中に入れたもの:

| コミット | 中身 |
|---|---|
| `53513d6`（#74） | 確認（許可）が要る操作をPermissionRequestのフックで拒否し、理由（無人実行であること、`mktemp -d`等の代わり）を返す。契約のフックの表に追加（ユーザー承認）。#74は閉じていない（下の次の一手） |
| `15cc0c4` | 開発版のsandboxを`XDG_DATA_HOME`・`GONDOLIN_IMAGE_STORE`で切り離し、ハーネスと並べて確かめる手順をリリースのSkillに |
| `281cf77` | リリースの1-0で、Claude Codeのchannelsの状態を見て#109に残す |

**#85・#83**（ブランチ`feat/next-task-waiting`、5段＋liveの修正。契約の変更はユーザー承認）:

| コミット | 中身 |
|---|---|
| `26b83f1` | `next_task`は最大90秒（`serve.Options.NextTaskWait`）待ち、無ければ`{kind: "waiting", reason: gate/question/triage/running, position}`。`report_result`・`report_concern`は記録した時点で返し、ホスト側のノードは`runCtl.kick()`の`advanceLoop`で回す（実行中に来た分はまとめる）。`stopRun`は`waitAdvance`で終わりを待つ |
| `1807ee3` | `waiting`を返した後に状態が進む（タスク・done・blocked、ゲート・質問の解決）と、ゲストの`tmux send-keys -t claude-work`で`guest.WakeText`を送って起こす（`serve/wake.go`）。取りこぼしを防ぐため、`waiting`を決めた時点の`seq`と比べる。`masuda chat`でアタッチ中は送らず「起こす待ち（chat中）」、60秒で送り直し、3回で打ち切り。見回り（patrol）も拾う。フェイクは`fake/<id>/wakes`に記録 |
| `8c325bc` | ループ規約（`loop-claude.md`の「待ち」）・`docs/guest-protocol.md`・`docs/design/overview.md`を書き換え。`MCP_TOOL_TIMEOUT`の7日は`ask_human`・特権コマンドのために残す |
| `585bee1` | `next_task`の結果を返す前にゲストが切断していたら起こす。go-sdk v1.7.0（stateless）は切断してもハンドラーのctxを取り消さないので、`internal/mcp`の`withDisconnect`でHTTPリクエストのctxに結ぶ（`next_task`だけ） |
| `1d4e05d` | live: planゲートを8分開けたままにしてから承認し、1周が終わる（`TestFixLapHoldsPlanGate`） |
| `a1a46f7` | live: develop以前から落ちていた`TestEngineContinuationFallsBackAfterResume`を直す。DONEでVMが壊れて最後の`next_task`のIDが記録されないため、その確認を外した |

ユーザーの判断: 起こすときに入力待ちかは見ない、`resume`の後は起こさない、protoは変えない（活動の`detail`だけ）、gate・questionのdecide/answerからのadvanceは同期のまま。`ask_human`の長い待ちは#110（v0.6）。
## 完了した契約テスト
- `GOWORK=off go build ./... && go vet ./... && go test ./...`がマージ後のdevelop（`7b778c1`）で緑。serve・contract・mcpは`-race`も緑。判定のテストは分岐を壊して落ちることを確かめた
- 契約テスト: CM4（planゲートで`waiting`、待っている`next_task`とdecideの並行、reviewゲートで起こす）、CM12（triageで`waiting`→切断→起こす）、CM13（`report_result`がノードを待たずに返り、実行中なら`waiting`）
- 実機（切り離した開発版のsandbox）: `TestFixLapHoldsPlanGate`（8分開けても止まらない、#85の再現が消えた）、アタッチ中は送らずデタッチ後に見回りが送る、継続テスト一式、`TestEngineContinuationFallsBackAfterResume`（59秒）。開発版のsandboxは止め、ハーネスのプロセス・ソケットが無事なことを確かめた
## 未完と理由
- v0.4.0の手順6・7: ハーネスのワークスペースが空くのを待っている
- #85・#83・#74を閉じていない（コメントを付けて閉じる。#85・#83は配布されるまで開けておくかをユーザーに確かめる）
- masuda-sandboxの`fix/destroy-with-ssh`（`8670a19`、sandbox#8、アタッチ中でも破棄が有限時間で終わる）: mainへのマージ・pushは未判断。masuda#84の前提
- マイルストーンの無いIssue: #103・#104・#105・#106・#108（ハーネスから起票）、#96・#60。計画外は置き去りになるので振り分けが要る
- Gondolinの#155への返事が未回答
## 次の一手
1. ハーネスが空いたらv0.4.0の手順6（Skill `release`）→手順7（記録、#107を閉じる）。手順6の後に、engine#9のfixerの`commit-message`とmasuda自身のイメージが`diskMiB 8192`で足りるかを見る
2. #85・#83・#74を閉じる（上の確認の後）
3. sandbox#8（`fix/destroy-with-ssh`）をmainへ入れる判断→masuda#84
4. v0.5.0の残り: #84、#88、sandbox#11、#65（根本は上流待ち）
5. マイルストーンの無いIssueをユーザーと振り分ける
6. Gondolin #155に返事する（日本語の草案から）
## 注意点
- VMを使う確認は、ハーネスと並べて切り離した開発版のsandboxで回す（リリースのSkillに手順）:
  `(cd ../masuda-sandbox && pnpm build && XDG_DATA_HOME="$HOME/.local/share/masuda-sandbox-dev-xdg" GONDOLIN_IMAGE_STORE="$HOME/.cache/gondolin-dev/images" exec node dist/cli.js serve --socket "$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock")`、liveは`MASUDA_SANDBOX_SOCKET=$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock MASUDA_LIVE_TEST=1 GOWORK=off go test ./live/`。ハーネスの更新（手順6）だけはハーネスを空にしてから
- 開発版のsandboxを止めるときは`pgrep -f`で探さない（自分のシェルにもマッチして止めた）。ソケットのパスで相手を確かめ、pidを指定する
- masuda-sandboxのチェックアウトは`fix/destroy-with-ssh`のまま
- Claude CodeはMCPの呼び出しを120秒でバックグラウンドへ回し、その300秒後に打ち切る。`next_task`の90秒はその内側に収めるため
- `tmux send-keys`は、Claude Codeが作業中なら入力を溜めて後で送る。`tmux -C attach`もlist-clientsのクライアントに数えられる
- ハーネスのセッションと決めたこと（2026-10-07）: 特権コマンドに秘密を渡す案は取りやめ（承認済みのコミットだけを新しいVMで秘密付きで動かす案は#96）。記録（`records/definitions/`）は不変の扱い。「観察してから承認する」一般の道具（学習モード）はsandboxの仕事
- CIの結果で止めたい手順は`&&`でつなぐ（v0.3.0で、CIが赤いまま`main`を合わせた。タグの前に気づいて直した）
- CIのランナーのgitは2.55（手元は2.43）。gitの既定の変化でCIだけ落ちることがある（#98がそれだった）
- この端末（WSL2）は時計が前後に約10秒跳ぶ。壁時計の順に頼る処理・テストは壊れうる（`resume`のWIPの選び方、特権コマンドの記録の`started_at`）
- `buf`は入っていない。`go run github.com/bufbuild/buf/cmd/buf@v1.73.0 generate`（キャッシュ済み）で生成する。プラグインの版は`buf.gen.yaml`で固定した。BSRは未認証だと短時間に数回の生成でレート制限（`resource_exhausted`）になり、1時間ほど解けない
- `scripts/docs-prepare.sh`は`docs/`の`__MASUDA_VERSION__`をその場で書き換える。手元で回したら`git checkout -- docs/`で戻す（自分の未コミットの変更と混ざっていないか先に確かめる）
- `scripts/gh.sh`のトークンにはActionsの再実行の権限が無い。再実行はユーザーがWebで行う
- `prime.md`のdenyの一覧と`init.go`の`primeDenyRules`は、gate・secret・egress・privileged-commandにサブコマンドを足したら一緒に直す
- ほかのセッションとはSendMessageで話せる（このリポジトリのセッションは`masuda-d3`、ハーネスは`oncall-pf-template-20`）。相手の依頼でIssueを起票・返事するときは、こちらのユーザーの判断を取ってから
- Gondolinの作業の記録は`~/work/gondolin-notes/`（README.mdが入口）。ハーネスのgondolinは手で差し替えてあり、`masuda-sandbox`を入れ直すと元に戻る（`~/work/gondolin-notes/harness-hotfix/`の手順で当て直す。手順6で要る）
- 上流（Gondolin）への投稿は、ユーザーがWebの画面から個人のアカウント（TadahiroYamamura）で行う。この端末の`gh`は社用のアカウント、`scripts/gh.sh`のトークンは自分のリポジトリだけで、どちらも使わない
- この端末の`~/.gitconfig`は会社用。masuda・masuda-sandbox・masuda-engine・gondolinのcloneにはローカルで個人用の作者を入れてある
- 「`masuda serve`がsandboxを子プロセスとして起動する」案は、ユーザーの判断で取りやめた
## 契約への提案
無し（#85・#83の提案は承認されて実装した）。
