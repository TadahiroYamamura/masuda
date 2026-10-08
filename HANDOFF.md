# HANDOFF
## 作業項目
2026-10-08。ハーネス（oncall_pf_template）がsandboxを使う間に、実機の要らないIssueと#101を片付けた。developは`f5f99ab`以降（push済み）。

| Issue | コミット | 中身 |
|---|---|---|
| #92 | `bd0ec60`・`9a0af45` | `--repo`を省いたら今いる作業ツリーのトップを使う（外ならエラー）。明示したサブディレクトリは`init`・`privileged-command run`も含めて拒む（`absRepo`＋`requireWorkTreeTop`）。`prime.md`の「トップで打つ」の1行を削除 |
| #87 | `93c2ca8` | 定義の写し（`records/definitions/`）を元の権限のまま作る。0600だとDockerの`COPY`後に`USER`を切り替えた手順から読めなかった |
| #64 | （`55eaafe`で実装済み） | 確かめて閉じた |
| #101 | `9a5ce3a` | **契約の変更**（ユーザー判断）: `ACTIVITY_KIND_AUTH_REJECTED = 8`。`api.anthropic.com`の`/v1/messages`の応答が401・403なら`auth_rejected`（`dead`の次、進行中より前）、2xxで解除。`detail`に直し方（登録し直してstop→resume） |

#92・#87・#64・#101は閉じた。前のセッションの`masuda env import`（`394222d`）もdevelopに入っている（未リリース）。契約を変えたので、次のリリースは**v0.4.0**の扱い。

ワークフローの形への外部の指摘（ワークフローのoutputs・outcomesの宣言、revise系3ノードの統合、optionalなinput）は検討の上、ユーザーが却下した。
## 完了した契約テスト
- `go build`・`go vet`・`go test ./...`（`GOWORK=off`、契約テストを含む）が緑（`9a5ce3a`）
- 実機（#87）: 開発版のserveを別のソケット・データディレクトリで動かし、`USER root`で`COPY`したファイルを`USER ubuntu`で読むDockerfileで`run`。修正前（`0a33e8d`）は起動時のビルドが`Permission denied`で`suspended`、修正後は通ってVMが起動した。後片付け済み
- 実機（#101）: ダミーのトークンの`run`で、起動の2秒後に`auth_rejected`、Notificationフックの後も`auth_rejected(idle)`のまま。観測では、Claude Codeは起動時に`/api/claude_code/settings`・`policy_limits`も呼び、ダミーではそれらも401（正しいトークンでの応答は未確認なので判定に使っていない）
## 未完と理由
- #85・#83のC案: 契約（`docs/guest-protocol.md`）の変更が要るため、下の提案の承認待ち
- #84のコード: sandbox#8（sshでアタッチ中だとDestroySandboxが終わらない）を先に直す必要がある
- Gondolinの#155〜#160: メンテナの返事待ち（`gondolin-watch.py`のSessionStartフックが知らせる。外部への書き込みは毎回ユーザーの了解、英文は日本語の草案の承認後）
- 同梱のdevelopで特権コマンドを強制する方法（engineのHANDOFFの未決）
- primeの実物のClaude Codeでの確認（SessionStartフックが末尾まで読まれるか、deny/askが効くか）
- 実機の要らない残り（着手前に決めること）: #76（英語化、量が多いので分けて）、#72（補足の書き方とengineへの渡し方）、#94（埋め込みの置き場所）、#93（YAMLのキーか、rootだけを出すか）
## 次の一手
1. このリポジトリで`masuda init`し、Claude Codeを開き直してprimeが読み込まれること、`masuda gate approve`がdenyされることを確かめる
2. 下の#85・#83の提案の承認を得たら実装する
3. 実機の要らない残り（上）を、決めることを決めてから進める
4. Gondolinの返事が来たら対応する。マージされたら、ハーネスに手で当てた修正を外し、sandboxのgondolinを上げる
## 注意点
- ハーネスのセッションと決めたこと（2026-10-07）: 特権コマンドに秘密を渡す案は取りやめ（承認済みのコミットだけを新しいVMで秘密付きで動かす案は#96）。記録（`records/definitions/`）は不変の扱い。「観察してから承認する」一般の道具（学習モード）はsandboxの仕事
- VMを使う確認（live・sandbox契約テスト）は、各段の直前にハーネスで`masuda list`し、走行中のワークスペースが無いことを確かめてから回す
- CIの結果で止めたい手順は`&&`でつなぐ（v0.3.0で、CIが赤いまま`main`を合わせた。タグの前に気づいて直した）
- CIのランナーのgitは2.55（手元は2.43）。gitの既定の変化でCIだけ落ちることがある（#98がそれだった）
- この端末（WSL2）は時計が前後に約10秒跳ぶ。壁時計の順に頼る処理・テストは壊れうる（`resume`のWIPの選び方、特権コマンドの記録の`started_at`）
- `buf`は入っていない。`go run github.com/bufbuild/buf/cmd/buf@v1.73.0 generate`（キャッシュ済み）で生成する。`buf.gen.yaml`のリモートプラグインの版が固定されていないと、connectの生成物がv2向けに変わる（#101ではconnectの生成物を戻した）
- `scripts/gh.sh`のトークンにはActionsの再実行の権限が無い。再実行はユーザーがWebで行う
- ハーネスと並べて実機で確かめるときは、ブランチのバイナリで`masuda serve --socket <別> --data-dir <別> --config <無いパス>`を立て、トークンはダミーを`secret set`する（起動時の検査は有無だけ）。イメージの名前は`<リポジトリ名>-<パスのハッシュ>:<entry>`なので、スクラッチのリポジトリならハーネスと衝突しない。`--socket`はサブコマンドの後ろに書く
- `prime.md`のdenyの一覧と`init.go`の`primeDenyRules`は、gate・secret・egress・privileged-commandにサブコマンドを足したら一緒に直す
- ほかのセッションとはSendMessageで話せる（このリポジトリのセッションは`masuda-d3`、ハーネスは`oncall-pf-template-20`）。相手の依頼でIssueを起票・返事するときは、こちらのユーザーの判断を取ってから
- Gondolinの作業の記録は`~/work/gondolin-notes/`（README.mdが入口）。ハーネスのgondolinは手で差し替えてあり、`masuda-sandbox`を入れ直すと元に戻る（`~/work/gondolin-notes/harness-hotfix/`の手順で当て直す）
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
