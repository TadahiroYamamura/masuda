# HANDOFF
## 作業項目
2026-10-08〜09。ハーネス（oncall_pf_template）がsandboxを使う間に、実機の要らないIssueを片付け、3リポジトリの開いているIssueを棚卸ししてv0.4.0のVM無しの分を入れた。developは`9d5dd2b`、engineの`main`は`042dcd5`、sandboxの`main`は`0ecc6b4`（どれもpush済み）。

| Issue | コミット | 中身 |
|---|---|---|
| #92 | `bd0ec60`・`9a0af45` | `--repo`を省いたら今いる作業ツリーのトップを使う（外ならエラー）。明示したサブディレクトリは`init`・`privileged-command run`も含めて拒む（`absRepo`＋`requireWorkTreeTop`）。`prime.md`の「トップで打つ」の1行を削除 |
| #87 | `93c2ca8` | 定義の写し（`records/definitions/`）を元の権限のまま作る。0600だとDockerの`COPY`後に`USER`を切り替えた手順から読めなかった |
| #64 | （`55eaafe`で実装済み） | 確かめて閉じた |
| （#101の懸念） | `ae06696` | `buf.gen.yaml`のプラグインの版を固定（protoc-gen-go v1.36.12、connect v1.21.0）。固定した版で生成し直して`gen/`と一致を確認 |
| #76 | `101b853`・`72d0e7d` | CLIの出力（フラグの説明・使い方・doctor・`gate show`・エラー）を英語に、serveの利用者向けエラーに「; 次の手」。serveに繋がらないときは`serveUnreachableError`でソケットと次の手、引数の数の誤りは使い方の1行、`-v`は`version`。`init.go`が書く`CLAUDE.local.md`の文面・`prime.md`・ゲスト向けの文は日本語のまま |
| #93 | engine `6fff69f`、masuda `db34f45`・`d6667fe` | **engineの契約の変更**（ユーザー判断）: ワークフローのトップに`user_invocable`（省略時true、`Workflow.UserInvocable`）。同梱の部品と`smoke`はfalse。公開APIは`WorkflowEntry.user_invocable`の追加。`workflow list`は既定でtrueだけ、`--all`で全部。隠したものも`run`・`show`・`check`できる |
| #72 | `605fa83` | `question answer --note <id>=<text>`。補足は答えの次の行。選択肢のある項目は1行目を検査。補足は役が`ask_human`で組み立てた質問（記録の`ByRole`）だけ。固定の質問には付けられない |
| #94 | `d9079f3` | `masuda doc`（一覧・ページ・`#<id>`の節・`--serve`）。`internal/docsembed/content/`にサイトと準備後のMarkdownを埋め込む（`release.yml`の`Embed documents`が写す。コミットは目印の`README.md`だけ）。埋め込みが無ければ版のURL。doctorの案内は`docRef` |
| engine#11 | engine `20fdcb8`、masuda `3300fe2` | 同梱のfixのplan gateの却下を、前回の計画と調査結果を受け取るreplan（quick-plannerの続き）へ戻す。サブエージェントがengineのworktreeで実装し、監督が差分・テスト・壊す確認をした。masudaはgo.modを上げ、`docs/user/workflows.md`のfixの説明を合わせた |
| （聞き取り） | `d3d87b1` | `masuda wait <id> [--timeout] [--ignore <event>,...]`。人の出番（done・stopped・suspended・blocked → gate・question → dead・auth_rejected・stalled の順）まで待ち、`<きっかけ> <id> ...; next: <コマンド>`の1行で終わる。時間切れは終了コード3。serveとの接続が切れたら繋ぎ直す（1つでもイベントを受けた後の誤りは切断とみなす。serveを強制終了するとinvalid_argumentの「incomplete envelope」になるため）。`prime.md`は「`sleep`と`list`を繰り返さず`wait`をバックグラウンドで動かす」に変えた。きっかけ: ONCALL-1321のセッションが`sleep 15; masuda list`の空振りでトークンを使っていた |
| engine#3＋#66 | engine `ff0145e`、masuda `3143f05` | 副産物（`expected_byproducts`／`Byproducts`）を「完全一致、またはdoublestarのglob」で照合する（依存の追加はユーザー承認）。engineは`matchesByproduct`（非公開）、masudaは`staging.matchPath`。同じ入力と期待値の表のテストを両方に置いて規則がそろっていることを確かめる |
| engine#9 | engine `042dcd5` | fixerの出力に`commit-message`を足し、レビュー後の修正コミットのメッセージを書かせる（summaryに落ちていた） |
| #71 | `b29893c` | liveテストのトークンを`Store.ClaudeToken`（ユーザー単位→暫定ファイル）で読む |
| #65（緩和） | `125c017` | ゲストのフックのcurlに`--connect-timeout 3 --max-time 15 --retry 2 --retry-connrefused --retry-max-time 20`、フックの`timeout`を40秒で明示。根本の原因は上流待ちで#65は開けたまま |
| #88・#51（文書） | `ee5e54e`・`62b75d6` | キャッシュをtmpfsの`/tmp`へ逃がす勧めをやめ、ディスクに置いて`diskMiB`を増やす勧めに（文書・雛形のDockerfile・masuda自身の`.masuda/images/default/Dockerfile`）。走行中のワークスペースは分岐元に追従しないことを書いた。#88の警告の部分はv0.4.1 |
| sandbox#6 | sandbox `0ecc6b4` | `createHttpHooks`に`blockInternalRanges: true`を明示 |
| #101 | `9a5ce3a` | **契約の変更**（ユーザー判断）: `ACTIVITY_KIND_AUTH_REJECTED = 8`。`api.anthropic.com`の`/v1/messages`の応答が401・403なら`auth_rejected`（`dead`の次、進行中より前）、2xxで解除。`detail`に直し方（登録し直してstop→resume） |

#92・#87・#64・#101・#76・#93・#72・#94・#66・#71・#51、engine#3・#9・#11、sandbox#6は閉じた。前のセッションの`masuda env import`（`394222d`）もdevelopに入っている（未リリース）。契約を変えたので、次のリリースは**v0.4.0**の扱い。go.modのengineは今`main`の擬似バージョン（`v0.3.1-0.20261009033421-042dcd526de2`）なので、リリースではengineにもv0.4.0を打ってgo.modをタグに上げる（`release.yml`の版の検査が止める）。

ワークフローの形への外部の指摘（ワークフローのoutputs・outcomesの宣言、revise系3ノードの統合、optionalなinput）は検討の上、ユーザーが却下した。

**Issueの棚卸し（2026-10-09）**: 3リポジトリの計画外の30件をサブエージェントで調べ、根拠を抜き取りで確かめてユーザーの判断で振り分けた。閉じたもの: masuda#63・#61・#7、engine#8・#10・#6・#4・#1・#2。engine#5は粒度だけのIssueに書き直した。3リポジトリに`v0.4.0`・`v0.4.1`・`v0.5`のマイルストーンを作り、`v0.2`は閉じた。今の振り分け:

| マイルストーン | masuda | engine | sandbox |
|---|---|---|---|
| v0.4.0（週末にハーネスへ反映） | #65（緩和は入った。根本は上流待ち） | — | — |
| v0.4.1（VMの作業） | #84・#74・#88（警告）・#85・#83 | — | #8・#11 |
| v0.5 | #67・#23・#89・#91・#9 | #7・#5 | #4・#1 |
| 無し（保留） | #96・#60 | — | #9・#2・#3（上流待ち・上流への報告は慎重に） |

**上流（Gondolin）**: 新しいIssueは、こちらの#155〜#160への反応が来るまで出さない（ユーザー承認）。その間に、他の人のgondolin#161（sandbox#11と同じ件）へ、Ubuntu 24.04でも起きる・ビルドの設定では避けにくい・回避策でdockerdまで動く、をGondolin 0.12.0だけの再現スクリプト付きでコメントした（TadahiroYamamura、issuecomment-6073639966。記録は`~/work/gondolin-notes/`）。次に出す候補はsandbox#8の件、その後に#3の件。
## 完了した契約テスト
- `go build`・`go vet`・`go test ./...`（`GOWORK=off`、契約テストを含む）とCI（ci・docs）が緑（`815f7ed`の後、`9d5dd2b`はDockerfileのコメントと1行だけ）。engineも緑（`042dcd5`）、sandboxは単体149件とCIが緑（`0ecc6b4`。契約テストは実VMなので回していない）
- `masuda wait`: フェイクのserveで、無いID・時間切れ・stopでstopped・serveの`kill -9`と再起動を跨いだ繋ぎ直しを確認。VMの実機（ゲート・質問で終わること）は未確認。engineも緑（`20fdcb8`）
- 実機（#87）: 開発版のserveを別のソケット・データディレクトリで動かし、`USER root`で`COPY`したファイルを`USER ubuntu`で読むDockerfileで`run`。修正前（`0a33e8d`）は起動時のビルドが`Permission denied`で`suspended`、修正後は通ってVMが起動した。後片付け済み
- #94: `release.yml`の`Embed documents`と同じ手順を手元で回し（`.venv-docs`のmkdocs）、埋め込んだバイナリで一覧・節・誤りの案内・doctorの案内・`--serve`を確認。サイト約4.9MB、Markdown約600KB、バイナリ30MB。実際のリリースで走るのは次のタグから
- 実機（#101）: ダミーのトークンの`run`で、起動の2秒後に`auth_rejected`、Notificationフックの後も`auth_rejected(idle)`のまま。観測では、Claude Codeは起動時に`/api/claude_code/settings`・`policy_limits`も呼び、ダミーではそれらも401（正しいトークンでの応答は未確認なので判定に使っていない）
## 未完と理由
- #85・#83のC案: 契約（`docs/guest-protocol.md`）の変更が要るため、下の提案の承認待ち
- #84のコード: sandbox#8（sshでアタッチ中だとDestroySandboxが終わらない）を先に直す必要がある
- Gondolinの#155〜#160: メンテナの返事待ち（`gondolin-watch.py`のSessionStartフックが知らせる。外部への書き込みは毎回ユーザーの了解、英文は日本語の草案の承認後）
- 同梱のdevelopで特権コマンドを強制する方法（engineのHANDOFFの未決）
- primeの実物のClaude Codeでの確認（SessionStartフックが末尾まで読まれるか、deny/askが効くか）
- v0.4.0のリリース（engineのタグとgo.modの版上げを含む。Skill `release`）。週末、ハーネスが空いたときに実機の確認（約1時間）→タグ→公開後の確かめ→ハーネスの更新を続けて行う（ユーザー承認）。VMの確認で兼ねて見ること: #65のフックの`timeout`が効くか（1周のログに2分以上の無反応が無いか）、#71のliveが秘密ストアのトークンで走るか、engine#9でfixerが`commit-message`を書くか、masuda自身のイメージ（GOCACHEをディスクへ）が`diskMiB 8192`で足りるか
- ハーネス（v0.3.0）のエージェントには`masuda wait`の案内が届いていない。v0.4.0でハーネスを更新して届ける（週末）
## 次の一手
1. このリポジトリで`masuda init`し、Claude Codeを開き直してprimeが読み込まれること、`masuda gate approve`がdenyされることを確かめる
2. 下の#85・#83の提案の承認を得たら実装する
3. 週末にv0.4.0を出す（Skill `release`）。上の「VMの確認で兼ねて見ること」を見る。`release.yml`の`Embed documents`が初めて走るので、公開後に配布物の`masuda doc`を確かめる
4. v0.4.1（マイルストーン）に入る。先頭はsandbox#8（masuda#84の前提）
5. Gondolinの返事が来たら対応する。マージされたら、ハーネスに手で当てた修正を外し、sandboxのgondolinを上げる
## 注意点
- ハーネスのセッションと決めたこと（2026-10-07）: 特権コマンドに秘密を渡す案は取りやめ（承認済みのコミットだけを新しいVMで秘密付きで動かす案は#96）。記録（`records/definitions/`）は不変の扱い。「観察してから承認する」一般の道具（学習モード）はsandboxの仕事
- VMを使う確認（live・sandbox契約テスト）は、各段の直前にハーネスで`masuda list`し、走行中のワークスペースが無いことを確かめてから回す
- CIの結果で止めたい手順は`&&`でつなぐ（v0.3.0で、CIが赤いまま`main`を合わせた。タグの前に気づいて直した）
- CIのランナーのgitは2.55（手元は2.43）。gitの既定の変化でCIだけ落ちることがある（#98がそれだった）
- この端末（WSL2）は時計が前後に約10秒跳ぶ。壁時計の順に頼る処理・テストは壊れうる（`resume`のWIPの選び方、特権コマンドの記録の`started_at`）
- `buf`は入っていない。`go run github.com/bufbuild/buf/cmd/buf@v1.73.0 generate`（キャッシュ済み）で生成する。プラグインの版は`buf.gen.yaml`で固定した。BSRは未認証だと短時間に数回の生成でレート制限（`resource_exhausted`）になり、1時間ほど解けない
- `scripts/docs-prepare.sh`は`docs/`の`__MASUDA_VERSION__`をその場で書き換える。手元で回したら`git checkout -- docs/`で戻す（自分の未コミットの変更と混ざっていないか先に確かめる）
- `scripts/gh.sh`のトークンにはActionsの再実行の権限が無い。再実行はユーザーがWebで行う
- ハーネスと並べて実機で確かめるときは、ブランチのバイナリで`masuda serve --socket <別> --data-dir <別> --config <無いパス>`を立て、トークンはダミーを`secret set`する（起動時の検査は有無だけ）。イメージの名前は`<リポジトリ名>-<パスのハッシュ>:<entry>`なので、スクラッチのリポジトリならハーネスと衝突しない。`--socket`はサブコマンドの後ろに書く
- `prime.md`のdenyの一覧と`init.go`の`primeDenyRules`は、gate・secret・egress・privileged-commandにサブコマンドを足したら一緒に直す
- ほかのセッションとはSendMessageで話せる（このリポジトリのセッションは`masuda-d3`、ハーネスは`oncall-pf-template-20`）。相手の依頼でIssueを起票・返事するときは、こちらのユーザーの判断を取ってから
- Gondolinの作業の記録は`~/work/gondolin-notes/`（README.mdが入口）。ハーネスのgondolinは手で差し替えてあり、`masuda-sandbox`を入れ直すと元に戻る（`~/work/gondolin-notes/harness-hotfix/`の手順で当て直す）
- 上流（Gondolin）への投稿は、ユーザーがWebの画面から個人のアカウント（TadahiroYamamura）で行う。この端末の`gh`は社用のアカウント（yamamura-tadahiro-oncall）、`scripts/gh.sh`のトークンは自分のリポジトリだけで、どちらも使わない（2026-10-09に`gh`を案内して社用で投稿され、消して出し直した）
- この端末の`~/.gitconfig`は会社用。masuda・masuda-sandbox・masuda-engine・gondolinのcloneにはローカルで個人用の作者を入れてある
- 「`masuda serve`がsandboxを子プロセスとして起動する」案は、ユーザーの判断で取りやめた
## 契約への提案
### 長い待ちを「待ちを返して起こしてもらう」に変える（#85・#83、ユーザー承認済みの案。契約の変更として判断待ち）

全文: `~/work/gondolin-notes/drafts/masuda-85-wait-proposal.ja.md`。要点:

1. **MCPの呼び出しは長く待たない**: `report_result`はengineに結果を記録した時点で返し、`advance()`（ホスト側のノード）は呼び出しの外で回す。`next_task`は次のタスクが無ければ最大90秒待ち、用意できなければ`{kind: "waiting", reason: "gate" | "question" | "triage" | "running", position}`を返す。`MCP_TOOL_TIMEOUT`の7日は不要になる
2. **ループの規約**: `waiting`ならターンを終える。起こされたら同じ`agent_id`で`next_task`。失敗したら間を置いて数回呼び直し、だめならターンを終える（ループを捨てない）
3. **masudaがメインセッションを起こす**: ゲートの判断・質問の回答が入ったとき、ホスト側のノードが終わったとき、`resume`の後。メインセッションが入力待ちのときだけ、sandboxのExecで`tmux send-keys -t claude-work`に決まった1文を送る。来なければもう一度。`masuda chat`でアタッチ中は送らない
4. **対象外**: `ask_human`（engineの変更が要る）。そのときに`AddQuestion`の重複も直す

| 文書 | 変更 |
|---|---|
| `docs/guest-protocol.md` :9 | MCPの`timeout`を7日にしている理由を削る |
| 同 :30-39 | ループの規約を上の2に |
| 同 :45 `next_task` | 戻り値に`waiting`を足し、「最大90秒待つ」に |
| 同 :47 `report_result` | 記録した時点で返す |
| 同 :48 `report_concern` | `waiting`を返す形に合わせる |
| 同（新しい節） | masudaがメインセッションを起こすこと |
| `masuda.proto` | 新しいRPCは要らない見込み。「起こす待ち（chat中）」を出すなら`ActivityKind`の値かコメント |

選ばなかった案: 今のまま（長い接続に弱く、Claude Codeの文書に無い120秒の振る舞いに頼る）、`waiting`を返して間を置いて呼び直す（トークンが掛かる）、Claude Codeのchannels（research previewで、無人の起動では確認ダイアログが出て使えない）。

実機で確かめていないこと: Execの`tmux send-keys`がClaude Codeの入力欄に入って送信されるか、作業中・アタッチ中に送ってしまったときの振る舞い。
