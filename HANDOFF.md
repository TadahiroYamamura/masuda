# HANDOFF
## 作業項目
2026-10-07: ホストで動くClaude Codeにmasudaの使い方を教える仕組みを入れた（Beadsの`bd prime`・`bd setup claude`を参考にした）。develop `96124b5`（`0c6c5fa`のマージ）。push済み。

| 追加・変更 | 内容 |
|---|---|
| `masuda prime [--hook-json]` | ホストのエージェント向けの使い方（`cmd/masuda/templates/prime.md`、約5.2KB）を出す。`--hook-json`でSessionStartフックの`additionalContext`の形 |
| `masuda init` | `CLAUDE.local.md`に`<!-- BEGIN MASUDA -->`の短い節。`.claude/settings.local.json`にSessionStartフック（`masuda prime --hook-json`）、`permissions.deny`13件（gate・secret・egress・privileged-commandの判断系とremove）、`permissions.ask`1件（question answer）。`.gitignore`に両ファイル |

決まったこと（ユーザーの判断）:
- 書き先は個人用ファイル。共有の`CLAUDE.md`・`.claude/settings.json`はcloneでゲストに届き、ゲストにはmasudaのバイナリが無いため。Beadsは共有ファイル（`settings.local.json`は旧方式として移行元）なので、逆の判断
- 対象はClaude Codeだけ（AGENTS.mdには個人用ファイルもimportも無い）。primeは固定文面（serveの状態は載せない）、英訳しない
- 人間が承認する前提のコマンドはdeny。`gate show`・各`list`は打てる。`question answer`はエージェントが打ってよいがask
- serveはエージェントがバックグラウンドで起動してよい（セッションを閉じると止まりうる旨をprimeに書いた）。`watch`はバックグラウンドでだけ、詳細はワークスペースの記録を読む
- 並行ワークスペースのdevelopへの取り込みは人間の仕事とし、primeにも書かない

新規Issue: #92（サブディレクトリで`--repo`を省くと、`init`は黙ってそこに書き、他はエラー。解決したら`prime.md`の「トップで打つ」を消す）、#93（`workflow list`に出すワークフローをYAMLで選べるように。定義はengineの持ち物）、#94（`masuda doc`。リリースのビルドでサイトのHTMLと準備後のMarkdownを埋め込み、Markdownは標準出力へ、HTMLは`--serve`で`127.0.0.1`に公開。埋め込みの無いビルドは版のURLを案内。`doctor`の案内と`prime.md`も合わせて直す）。#95（特権コマンドの宣言をゲストへ写す。契約の変更でv0.3.0。下の「契約への提案」）。#96（reviewゲートで承認したコミットだけを、新しいVMで秘密付きで動かす。ハーネスから「特権コマンドで秘密を使いたい」と依頼があったが、承認するのはcommandの文字列だけで動くのはエージェントのコードなので取りやめた。当面はモック相手のE2EとCIでの本物との結合を勧めた）。`docs/design/overview.md`の特権コマンドの受け渡しの記述を実装に合わせた（`7a581d7`、v0.2.3に入れる）。

前回（2026-10-06〜07）の#78〜#86の対応と上記を含めて、**v0.2.3を公開した**（追跡#97）。masuda `ce39569`・engine `64a8e69`（v0.2.2と同じ）・sandbox `34577e9`。ゲストのClaude Codeは2.1.292（`1de0bd2`）。masudaのreleaseは1回目に契約テストCM5の後片付けで落ち（#98、たまに起きる）、Webで失敗したジョブを再実行して通った。**ハーネスの更新は保留**（下の「未完と理由」）。
## 完了した契約テスト
- v0.2.3の打つ前の確認: 継続テスト・実機1周（2.1.292）、sandbox契約テスト8件、`precheck.sh v0.2.3`、tarballの予行。公開物を一時ディレクトリに入れて`version`（0.2.3、`contract: ok`）・`doctor`・`prime`を確かめた
- `go build ./... && go vet ./... && go test ./...`を`go.work`あり・`GOWORK=off`の両方で緑（develop `96124b5`）
- initの新しい分岐（マーカー・フック・規則の重複、型の検査、書き直さない条件、deny/askの振り分け）は、壊すとすべてテストが落ちることを確かめた
- 一時ディレクトリで実際のバイナリの`init`を2回流し、2回目は何も変えないことを確かめた
- liveは回していない
## 未完と理由
- **v0.2.3へのハーネスの更新（手順6）**: ハーネスで`oncall_pf_template`のワークスペース`cfc91fb026a7`（ONCALL-1321/db-1）が走行中。更新の前に全ワークスペースをremoveする決まりなので、終わるまで待つ。oncall-pf-template-20が終了（publishか終了）を知らせてくる
- リリースの確認がハーネスの`cfc91fb026a7`と約3分重なった（16:41〜16:43のsandbox契約テスト）。oncall-pf-template-20が異常（特に特権VMの初回起動）を見ている。異常が出たらstop→resume、だめならログが来る
- #98（契約テストCM5がたまに後片付けで落ちる）
- 実物のClaude Codeで、SessionStartフックの出力が末尾まで読み込まれるか・deny/askが効くかは確かめていない
- #92・#93・#94・#96: 起票だけ
- #95: 契約の変更のため、v0.2.3の後にv0.3.0で実装する
- #85・#83のC案: 契約（`docs/guest-protocol.md`）の変更が要るため、下の提案の承認待ち
- #84のコード: sandbox#8（sshでアタッチ中だとDestroySandboxが終わらない）を先に直す必要がある
- Gondolinの#155〜#160: メンテナの返事待ち（返事の論点はB1〜B9）。`.claude/settings.local.json`のSessionStartフック（`~/.claude/scripts/gondolin-watch.py`）が動きを知らせる。外部への書き込み（コメント・PRの更新）は、毎回ユーザーの了解を取ってから。英文は日本語の草案の承認後に訳す
- #87・#88・#89・#91: 手を付けていない
## 次の一手
1. このリポジトリ（またはハーネス）で`masuda init`し、Claude Codeを開き直してprimeが読み込まれること、`masuda gate approve`がdenyされることを確かめる
2. `cfc91fb026a7`の終了の連絡が来たら、v0.2.3へハーネスを更新する（Skill `release`の手順6。ユーザーが打つ）。終わったら追跡#97の6を埋めて閉じる
3. 下の提案の承認を得たら、#85・#83のC案を実装する
4. Gondolinの返事が来たら対応する。マージされたら、ハーネスに手で当てた修正（`~/work/gondolin-notes/harness-hotfix/`のREADMEに戻し方）を外し、sandboxのgondolinを上げる
## 注意点
- VMを使う確認（live・sandbox契約テスト）は、各段の直前にハーネスで`masuda list`し、走行中のワークスペースが無いことを確かめてから回す（今回は確かめ直さずに重なった）
- `scripts/gh.sh`のトークンにはActionsの再実行の権限が無い。再実行はユーザーがWebで行う
- `prime.md`のdenyの一覧と`init.go`の`primeDenyRules`は、gate・secret・egress・privileged-commandにサブコマンドを足したら一緒に直す（denyはallowで例外を作れないので、サブコマンドごとに並べている）
- deny規則はコマンドの文面に対する歯止めで、フルパスや`sh -c`では迂回できる
- Gondolinの作業の記録は`~/work/gondolin-notes/`（README.mdが入口）。cloneは`~/work/gondolin`、forkはremote `fork`（SSHの`github.com_my`）。PRのブランチのworktreeは`~/work/gondolin-wt/a1〜a5`
- この端末の`~/.gitconfig`は会社用。masuda・masuda-sandbox・masuda-engine・gondolinのcloneにはローカルで個人用の作者を入れてある。新しいcloneでは確かめる
- `scripts/gh.sh`のトークンはmasuda関連のリポジトリにしか使えない（Gondolinへの投稿はユーザーがWebで行った）
- ハーネスのgondolin（`~/.nvm/versions/node/v24.16.0/lib/node_modules/masuda-sandbox/node_modules/@earendil-works/gondolin/dist/src/qemu/`の`network-stack.js`・`net.js`）は手で差し替えてある。masuda-sandboxを入れ直すと元に戻る
- masuda-sandboxはgondolinをbundleしない（external）ので、`pnpm patch`では利用者に届かない
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
