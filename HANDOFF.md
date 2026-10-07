# HANDOFF
## 作業項目
2026-10-07（1日分）。**v0.2.3とv0.3.0を公開し、ハーネスをv0.3.0へ更新した**（追跡#97・#102、どちらも閉じた）。

| 版 | masuda | engine | sandbox | 中身 |
|---|---|---|---|---|
| v0.2.3 | `ce39569` | `64a8e69` | `34577e9` | #78〜#86の対応、`masuda prime`と`init`のホスト向け設定（`CLAUDE.local.md`・`.claude/settings.local.json`のSessionStartフックとdeny/ask）、ゲストのClaude Code 2.1.292 |
| v0.3.0 | `4ec83bd` | `ce9a878` | `0d31786` | 特権コマンドをsandboxの`RunJob`へ載せ替え（sandbox#10）、`DeleteImage`（sandbox#5）、engineの`type: privileged`ノード（#99）、宣言のゲストへの写し`/masuda/privileged-commands.json`と`description`（#95）、`masuda privileged-command run`（#100）、公開APIの`suspended`（直せばresumeで続く停止。`blocked`は行き止まりだけ）と、run・resumeの開始時の特権ノードの事前検査、`resume`が時計の巻き戻りで古いWIPを戻す不具合（`312ae7a`）、stagingのgitの自動メンテナンスを同期に（#98、`4ec83bd`）、VMの起動のたびに空になるディレクトリと承認の範囲の文書 |

v0.3.0の作り方: 設計の材料集め・実装はサブエージェントに出し、監督（このセッション）が差分・テスト・壊す確認・実機の確認をした。

ハーネス（v0.3.0）: ワークスペースは全部removeした（exportsは残る）。gondolinのTCPの修正を当て直した。トークンを`masuda secret set`で登録し直した。masuda自身のイメージ（Claude Code 2.1.292、`247bc1d`）をbuild済み。oncall_pf_templateの2つのDockerfileの版も2.1.292に書き換え、作り直しはoncall-pf-template-20に任せた。

ハーネスのセッション（oncall-pf-template-20）とのやり取りで決めたこと:
- 特権コマンドに秘密を渡す案は取りやめた（承認するのは`command`の文字列だけで、動くのはエージェントが書き換えられる`/workspace`のコード）。承認済みのコミットだけを新しいVMで秘密付きで動かす案は#96
- 記録（`records/definitions/`）は不変の扱い
- 「観察してから承認する」一般の道具（学習モード）はsandboxの仕事

起票したIssue（開いているもの）: #92（サブディレクトリでの`--repo`）、#93（`workflow list`に出すものをYAMLで）、#94（`masuda doc`）、#96（承認済みのコミットだけを秘密付きで）、#101（Claudeのトークンが401で拒否されても`list`から分からない）。
## 完了した契約テスト
- v0.3.0: `go build`・`go vet`・`go test ./...`（`GOWORK=off`）とCIが緑（`4ec83bd`）。sandboxの契約テスト15件、engineのテスト
- 実機: 継続テスト（55秒）・実機1周（7分7秒）。`privileged-command run`（root・inputs・outputs・終了コード・拒否した通信先・利用者のリポジトリに書かない）、privilegedノードのfailed/done、承認の取り消しで`suspended`→承認してresumeで続く
## 未完と理由
- #85・#83のC案: 契約（`docs/guest-protocol.md`）の変更が要るため、下の提案の承認待ち
- #84のコード: sandbox#8（sshでアタッチ中だとDestroySandboxが終わらない）を先に直す必要がある。ハーネスでも`masuda chat`の接続が残って承認が約10分戻らなかった
- Gondolinの#155〜#160: メンテナの返事待ち（`gondolin-watch.py`のSessionStartフックが知らせる。外部への書き込みは毎回ユーザーの了解、英文は日本語の草案の承認後）
- 同梱のdevelopで特権コマンドを強制する方法（engineのHANDOFFの未決）
- primeの実物のClaude Codeでの確認（SessionStartフックが末尾まで読まれるか、deny/askが効くか）
- 開いているIssue: #87〜#89・#91〜#94・#96・#101
## 次の一手
1. このリポジトリで`masuda init`し、Claude Codeを開き直してprimeが読み込まれること、`masuda gate approve`がdenyされることを確かめる
2. 下の#85・#83の提案の承認を得たら実装する
3. Gondolinの返事が来たら対応する。マージされたら、ハーネスに手で当てた修正を外し、sandboxのgondolinを上げる
## 注意点
- VMを使う確認（live・sandbox契約テスト）は、各段の直前にハーネスで`masuda list`し、走行中のワークスペースが無いことを確かめてから回す
- CIの結果で止めたい手順は`&&`でつなぐ（v0.3.0で、CIが赤いまま`main`を合わせた。タグの前に気づいて直した）
- CIのランナーのgitは2.55（手元は2.43）。gitの既定の変化でCIだけ落ちることがある（#98がそれだった）
- この端末（WSL2）は時計が前後に約10秒跳ぶ。壁時計の順に頼る処理・テストは壊れうる（`resume`のWIPの選び方、特権コマンドの記録の`started_at`）
- `scripts/gh.sh`のトークンにはActionsの再実行の権限が無い。再実行はユーザーがWebで行う
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
