# HANDOFF
## 作業項目
2026-10-07（1日分）。次のセッションは**v0.3.0**（特権コマンドまわりの作り直し）から始める。

| 段 | 内容 |
|---|---|
| `masuda prime`と`init` | ホストのClaude Codeに使い方を教える。`init`が`CLAUDE.local.md`と`.claude/settings.local.json`（SessionStartフック、`permissions.deny`13件、`ask`1件）を書く（`96124b5`）。決まったこと: 個人用ファイルに書く（共有ファイルはcloneでゲストに届くため）、Claude Codeだけ、人間が判断するコマンドはdeny、`question answer`はask、serveはエージェントがバックグラウンドで起動してよい、`watch`はバックグラウンドでだけ |
| v0.2.3 | 公開した（追跡#97）。masuda `ce39569`・engine `64a8e69`・sandbox `34577e9`、ゲストのClaude Code 2.1.292。releaseは1回目にCM5の後片付けで落ち（#98）、Webで再実行して通った。**手順6（ハーネスの更新）だけ保留** |
| ハーネス（oncall-pf-template-20）との相談 | 特権コマンドを実機で初めて動かした（`cfc91fb026a7`で`db-verify`が通った。起動・受け渡し・rootでの実行・回収を確認。特権VMの中のdockerdは未確認）。そこから出た件を下のIssueに切った |
| 文書 | 特権コマンドへ渡すスナップショット（`7a581d7`、v0.2.3に入った）。VMの起動のたびに空になるディレクトリ（`/run`・`/tmp`・`/root`・`/var/tmp`・`/var/cache`・`/var/log`。GondolinのROOTFS_INIT_SCRIPTより）・承認の範囲（イメージの中身を含まない）・「実機では未検証」の書き換え（`5c7d562`、v0.3.0に入る） |

起票したIssue: #92（サブディレクトリでの`--repo`）、#93（`workflow list`に出すものをYAMLで）、#94（`masuda doc`）、#95（特権コマンドの宣言をゲストへ）、#96（承認済みのコミットだけを秘密付きで）、#98（CM5がたまに落ちる）、#99（特権コマンドの成否をワークフローで扱う）、#100（特権コマンドの単体実行）、masuda-sandbox#10（`RunJob`）。

ユーザーの判断:
- 特権コマンドに秘密を渡す案は取りやめた。承認するのは`command`の文字列だけで、動くのはエージェントが書き換えられる`/workspace`のコードだから。代わりにモック相手のE2Eと、CIでの本物との結合（#96は将来の案）
- 記録（`records/definitions/`）は不変の扱い。ハーネスが手で書き換えたのは例外として止めない
- 特権コマンドの仕組みはmasuda-sandboxの`RunJob`へ移し、masudaは方針（宣言・承認・通信先・どのツリーか・結果の置き先）だけを持つ（下の「契約への提案」）。#100はその上に作るのでv0.3.0。「観察してから承認する」一般の道具（学習モード）はsandboxの仕事
## 完了した契約テスト
- `go build ./... && go vet ./... && go test ./...`を`go.work`あり・`GOWORK=off`の両方で緑（`96124b5`の時点。その後の変更は文書と雛形のDockerfileのコメントだけで、`go test ./cmd/masuda/`は緑）
- v0.2.3の打つ前の確認一式（継続テスト・実機1周・sandbox契約テスト・`precheck.sh`・tarballの予行）と公開後の確認
## 未完と理由
- **v0.2.3へのハーネスの更新（Skill `release`の手順6）**: ハーネスで`cfc91fb026a7`（oncall_pf_template、ONCALL-1321/db-1）が走行中。更新の前に全ワークスペースをremoveする決まりなので、oncall-pf-template-20が終了を知らせてくるのを待っている
- **v0.3.0**: #95・#99・#100とmasuda-sandbox#10。契約（`sandbox.proto`・`guest-protocol.md`）の変更なので、下の「契約への提案」の承認を得てから。sandbox側の実装はmasuda-sandboxでの作業
- 実物のClaude Codeで、primeのSessionStartフックが末尾まで読み込まれるか・deny/askが効くかは確かめていない
- #85・#83のC案（下の提案）、#84（sandbox#8待ち）、Gondolinの#155〜#160（メンテナの返事待ち。`gondolin-watch.py`のSessionStartフックが知らせる。外部への書き込みは毎回ユーザーの了解、英文は日本語の草案の承認後）
- #87・#88・#89・#91〜#94・#96・#98: 手を付けていない
## 次の一手
1. v0.3.0の設計: 下の「`RunJob`」「#95」の提案と#99（ゲストの`/masuda/bin/run-privileged`案b）を、1つの設計としてまとめてユーザーの承認を得る。`/masuda/`の配置（#95の`privileged-commands.json`、#99の`bin/`）は一緒に決める
2. `cfc91fb026a7`の終了の連絡が来たら、ハーネスをv0.2.3へ更新する（ユーザーが打つ）。終わったら#97の6を埋めて閉じる
3. このリポジトリで`masuda init`し、Claude Codeを開き直してprimeが読み込まれること、`masuda gate approve`がdenyされることを確かめる
4. 下の#85・#83の提案の承認を得たら実装する
## 注意点
- VMを使う確認（live・sandbox契約テスト）は、各段の直前にハーネスで`masuda list`し、走行中のワークスペースが無いことを確かめてから回す（2026-10-07は確かめ直さずに約3分重なった。影響は出なかった）
- `scripts/gh.sh`のトークンにはActionsの再実行の権限が無い。再実行はユーザーがWebで行う
- `prime.md`のdenyの一覧と`init.go`の`primeDenyRules`は、gate・secret・egress・privileged-commandにサブコマンドを足したら一緒に直す（#100の`privileged-command run`も足す）
- deny規則はコマンドの文面に対する歯止めで、フルパスや`sh -c`では迂回できる
- ほかのセッションとはSendMessageで話せる（このリポジトリのセッションは`masuda-d3`、ハーネスは`oncall-pf-template-20`）。相手の依頼でIssueを起票・返事するときは、こちらのユーザーの判断を取ってから
- Gondolinの作業の記録は`~/work/gondolin-notes/`（README.mdが入口）。cloneは`~/work/gondolin`、forkはremote `fork`（SSHの`github.com_my`）。PRのブランチのworktreeは`~/work/gondolin-wt/a1〜a5`
- この端末の`~/.gitconfig`は会社用。masuda・masuda-sandbox・masuda-engine・gondolinのcloneにはローカルで個人用の作者を入れてある。新しいcloneでは確かめる
- `scripts/gh.sh`のトークンはmasuda関連のリポジトリにしか使えない
- ハーネスのgondolin（`~/.nvm/versions/node/v24.16.0/lib/node_modules/masuda-sandbox/node_modules/@earendil-works/gondolin/dist/src/qemu/`の`network-stack.js`・`net.js`）は手で差し替えてある。masuda-sandboxを入れ直すと元に戻る（v0.2.3への更新でも戻る。`~/work/gondolin-notes/harness-hotfix/`の手順で当て直す）
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

### 特権コマンドの宣言をゲストの`/masuda/privileged-commands.json`へ写す（#95、ユーザー承認済みの案。v0.3.0）

`.masuda/`をコミットしないリポジトリでは、同梱の`implementer`が読む`/workspace/.masuda/settings.json`が無く、特権コマンドを知る手段が無い（ハーネスの`oncall-pf-template-20`からの指摘）。レビュー観点・落とし穴と同じく、実行開始時に定義の写しから`/masuda/`へ置く。

| 文書・コード | 変更 |
|---|---|
| `docs/guest-protocol.md`「起動時にホストがゲストへ置くもの」 | `/masuda/privileged-commands.json`の行を足す（宣言の`description`・`command`・`image`・`inputs`・`outputs`・`timeoutSeconds`。宣言が無ければ置かない） |
| `internal/config`の`PrivilegedCommandDecl` | 任意の`description`を足す。`DeclHash`には含めない（空にした写しでハッシュを取る。既存の承認のハッシュは変わらない） |
| engineの`defaults/agents/implementer.md` | `/masuda/privileged-commands.json`を読むように |
| `docs/user/settings.md`・`secrets-and-egress.md` | `description`と、「コミットしておく」の記述 |

守る条件: ホストはこのファイルを読み戻さない（実行の判断は今どおり名前・`records/definitions/`・作業ツリーの`settings.local.json`の`DeclHash`）。承認の状態は写さない。

### 特権コマンドの仕組みをmasuda-sandboxの`RunJob`へ移す（masuda-sandbox#10・#100、ユーザー承認済みの方向。v0.3.0）

使い捨てVMの作成→ファイル投入→実行→`outputs`の回収→破棄を、sandboxの1つのRPC（仮称`RunJob`）にする。今はmasudaの`internal/privileged`が低水準のRPCを組み合わせている。

| 持ち主 | 持つもの |
|---|---|
| sandbox（`RunJob`） | VMの作成・破棄、ホストからのファイル投入、**VMからVMへの直接の写し**（今は`inputs`をmasudaのプロセス経由で運んでいる）、実行中のログの中継、時間切れ、通信の観測（拒否した通信先。学習モードの材料）、`outputs`の回収。sandboxのCLI（`masuda-sandbox run`仮）から単体でも呼べる |
| masuda | 宣言の読み込み、承認（`DeclHash`）との照合、通信先の計算、どのツリーを渡すか（stagingのスナップショット・作業ツリー・承認済みのコミット）、結果の置き先（ゲストの`/masuda/privileged/<run-id>/`、ホストの`records/privileged/`） |

`masuda privileged-command run`（#100）は、作業ツリーの宣言と承認を照らして`RunJob`を呼ぶだけになる（CLIからsandboxへ直接。公開APIは変えない）。#100で決めた細部: 利用者のリポジトリにrefもオブジェクトも書かない、`inputs`はホストの作業ツリーから、結果は既定で一時ディレクトリ（`--out`）、CLIの終了コードは特権コマンドと同じ、イメージは作業ツリーの`.masuda/images/`から。
