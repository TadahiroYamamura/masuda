# 全体設計

masudaは、Claude Codeに調査・計画・実装・レビューなどの作業をさせ、人間が要所で判断する流れを、ローカルPC上のVMで無人実行するためのツール群。実行はClaude Code CLIをサブスクリプション認証のままtmux上で自己ループさせる方式で行い、Anthropic APIを直接は呼ばない。

本書は3つのリポジトリにまたがる全体像を1か所にまとめる。各リポジトリ内部の設計は、それぞれのリポジトリの`docs/`にある。

## 1. 部品

| 部品 | リポジトリ・言語 | 動き方 | 責務 |
|---|---|---|---|
| **sandbox service** | `masuda-sandbox`、TypeScript | ホストで常駐する1プロセス（`masuda-sandbox serve`）。Gondolinを`@earendil-works/gondolin`として取り込む | VMの作成・破棄、VM内でのコマンド実行、SSHアタッチ、ノード単位のegress/秘密の方針切替、ゲスト↔ホストのファイル転送、APIリクエストの観測、イメージのビルド |
| **workflow engine** | `masuda-engine`、Go | ライブラリ。masudaのプロセス内で動く | ワークフロー定義（YAML）とエージェント定義（Markdown）の読み込み・検査、記録からの現在位置の計算、次に何をするかの決定。VMもgitも知らず、`Runner`インターフェース越しに外へ出る |
| **masuda** | `masuda`、Go | ホストで常駐する1プロセス（`masuda serve`）と、それを叩くCLI（`masuda run`等） | ワークスペース（staging bareリポジトリ、publish）、`Runner`の実装（engineの要求をsandbox serviceとgitへ翻訳）、ゲスト向けMCPエンドポイント、公開API（Connect）、秘密と承認のローカル保存 |
| UI | 別リポジトリ（任意） | ブラウザ等 | 公開APIのクライアント。CLIと同じAPIを使う |
| メインエージェント | VM内 | tmux上の`claude`。ループ規約（`CLAUDE.md`）に従う | MCPツール`next_task`で受け取ったタスクをサブエージェントへ渡し、結果を報告するだけ。自分では作業しない |
| サブエージェント | VM内 | Claude Codeのサブエージェント | 役割ごとに1つのタスクをこなし、`write_output`と`report_result`で報告する |

ホストで常駐するプロセスは`masuda serve`と`masuda-sandbox serve`の2つだけ。ワークスペースごとのプロセスは持たない。

## 2. 契約

部品の間は3つの契約で結ぶ。詳細と変更手続きは[contracts.md](contracts.md)。

| 契約 | 所有 | 形 | 使う側 |
|---|---|---|---|
| sandbox API | `masuda-sandbox` | `proto/masuda/sandbox/v1/sandbox.proto`（Connect、h2c、ローカルのUDSまたはループバック） | masuda |
| engine API | `masuda-engine` | Goパッケージ`engine`の公開型（`Runner`・`Store`・`Engine`）と、ワークフロー定義のYAMLスキーマ、エージェント定義のfrontmatter | masuda、ユーザー（定義を書く） |
| masuda API | `masuda` | `proto/masuda/api/v1/masuda.proto`（Connect、UIとCLIが叩く） | CLI、UI |

ゲスト内のエージェントとmasudaの間はMCP（HTTP）で、これはmasudaのリポジトリが`docs/guest-protocol.md`で定義する。

## 3. 1つのタスクの流れ

```
人間  masuda run workflows/develop --branch feat/x --input instructions=@todo.md
  │
masuda
  ├─ 定義を読み込み検査（問題があればここで止まる）
  ├─ ワークスペース作成: id、staging bare repo（実リポジトリから clone --bare）、入力をホスト側に保存
  ├─ 定義・設定・入力をスナップショットとして固定
  ├─ sandbox service に CreateSandbox（イメージ・env・初期方針・tcp対応付け）
  ├─ ゲストへ staging の bundle を書き込み、/workspace に clone させる
  └─ ゲストで tmux 上の claude を起動（ループ規約）
  │
ゲスト（メインエージェント）         ホスト（masuda / engine）
  next_task ─────────────────────▶ engine.Advance → Runner.RunAgent
  ◀──── タスクファイルのパス ─────  /masuda/in/<occ>/ に入力を書き込み済み
  サブエージェントが作業              （continues のノードなら応答に宛先の出現とその agent_id を添える）
  （続きは SendMessage で同じサブエージェントへ。いなければ新しく起動。次の next_task で agent_id を報告）
  write_output / report_result ──▶ /masuda/out/<occ>/ をホストが読み出し、スキーマ検証
                                   ノード境界: WIPスナップショットを staging へ pull
                                   次ノード: egress/秘密の方針を SetPolicy で切替
  ...
  （approval ノード）              ゲートを開き、人間の判断を待つ（API/CLI）
  （exec ノード）                  sandbox.Exec で決定論コマンドを実行、出力を検証
  （commit ノード）                staging 上でホストがコミットを作る（計画の範囲内のみ）
  （publish ノード）               staging → 実リポジトリへ fast-forward、または remote へ push
                                   → exports を書き出し、VM 破棄
```

- 入口は`masuda run <workflow>`だけ。途中で止まったタスクは`masuda resume <workspace-id>`で、記録から計算した位置で再開する。VMは使い捨てなので、再開時は新しいVMにstagingから再cloneする（「再開」の節）
- `masuda chat <workspace-id>`は、sandbox serviceのSSH経由でゲストのtmuxのメインセッション（`claude-work`）へアタッチする。`C-b d`で切り離せばセッションは動き続ける。chatからゲートは閉じられない

## 4. ワークスペースとstaging

ワークスペースは、ID・staging bareリポジトリ・ホスト側の状態ディレクトリ・（稼働中なら）サンドボックス1つ、の組。

- **staging**は`$XDG_DATA_HOME/masuda/workspaces/<id>/staging.git`。実リポジトリから`git clone --bare --local`で作る。masudaだけが読み書きする
- **ゲストのclone**は`/workspace`。VM内ディスクにあり、ホストとは共有しない。ゲストはstagingへpushする権利を持たない
- **ゲスト→ホスト**は常にホストが取りに行く。`git bundle create`をゲストで実行し、sandbox APIの`ReadFile`でホストが読み、stagingへ`fetch`する
- **ホスト→ゲスト**はbundleを`WriteFile`で置き、ゲストで`fetch`する
- **実リポジトリに触るのはpublishだけ**

### stagingのref

| ref | 意味 |
|---|---|
| `refs/heads/<branch>` | ワークスペースのブランチ。commitノードが進める。新しいブランチなら分岐元と同じコミットから、既存のブランチ（下記）ならその先頭から始まる |
| `refs/masuda/wip/<出現ID>` | ノード境界のWIPスナップショット（作業ツリー全体を`git add -A`したtree、gitignore対象は含まない）。クラッシュ復旧と、exec/特権ノードへの受け渡しに使う |
| `refs/masuda/base` | 分岐元。diffの基準。既存のブランチでは分岐元とブランチの分岐点（merge-base） |

### 既存のブランチで動かす

publishノードを含まないワークフロー（rootから辿れるワークフローのどれにもpublishが無いもの。engineの`Set.Reachable`で判定）に限り、`Run`の`branch`に実リポジトリに既にあるブランチを指定できる。実リポジトリには書かないので上書きの心配が無く、既にある変更のレビュー（`workflows/review`）に使う。

- stagingの`refs/heads/<branch>`は実リポジトリのそのブランチの先頭
- 分岐元は`base`、空なら実リポジトリの既定のブランチ（originのHEADが指すもの、`init.defaultBranch`、`main`・`master`の順に、実在してそのブランチ自身でないもの）。`refs/masuda/base`はその分岐元とブランチのmerge-baseに置く。分岐の後に分岐元へ入った変更を差分に混ぜないため
- publishを含むワークフローで既存のブランチを指定すると`AlreadyExists`

### commitとpublish

- **commit**（エンジン固定ノード）: 最新のWIP treeと`HEAD`の差分を取り、計画の対象ファイル（`scope: step`ならそのステップ、`plan`なら計画全体）だけからなるtreeをホストが`commit-tree`で作り、`refs/heads/<branch>`を進める。対象外の変更があれば`deviation`ゲートで人間に回す。ゲストには`fetch`と`reset --soft`で新しいHEADを知らせる（作業ツリーは触らない）
- **publish**: `target: local`なら実リポジトリの同名ブランチへfast-forward、`target: remote`なら`settings.json`の`publish.remote`（既定`origin`）が指す実リポジトリのremoteへpush（PR作成は将来のオプション）。publishするcommitハッシュはreview gateで承認されたものと同じでなければならず、違えばpublishしない
- **discard**: 反映せずに片付ける

publishとdiscardの最後に、exportsを書き出してからVMを破棄し、ワークスペースを閉じる。`end`で終わった（publish・discardを通らない）実行も、DONEを状態に写す前に同じ順で会話ログと実行ログを書き出してVMを破棄する（`Runner.Cleanup`に1つにまとめてあり、壊した後に呼ぶと実行ログの写し直しだけになる）。DONEのVMは再開にも使わない（Resumeは新しいVMを作る）ので残す理由が無く、残すとDONEにはStopが効かないためRemoveまで残り続ける。stagingは`masuda remove`まで残す。

### exports

置き場所は`<DataDir>/workspaces/<id>/exports/`。中身は次の3つ。

| パス | 中身 |
|---|---|
| `exports/<データ名>` | publish・discardノードの`export:`に書いたデータの最新の値 |
| `exports/execution-log.jsonl` | 実行ログ（`records/execution-log.jsonl`の写し） |
| `exports/transcripts/<project>/…/*.jsonl` | ゲストのClaude Codeの会話ログ。ゲストの`~/.claude/projects/`からの相対パスのまま写す |

- 会話ログは、ゲストのホームをcwdにして`find .claude/projects -type f -name '*.jsonl'`を`Exec`し、各ファイルを`ReadFile`で読んで写す。一覧が取れない・読めない・書けないファイルは実行ログに`kind: export-warning`として記録し、publish・discardは止めない
- 会話ログを回収するのは、DONE（publish・discard・`end`）とStop（`Remove`の`force`を含む）のとき。どれもVMを壊す前に書き出す。BLOCKEDはVMを残すので実行ログだけ写し直す（SUSPENDEDは写さない。Stop・Resumeで書き出す）。Removeは消す前に実行ログを写す（VMが無ければ会話ログは取れない）。serveの再起動で残ったVMは回収せずに壊す
- 再開後に書き出すと、同じパスのファイルは新しいもので置き換わる
- `masuda remove`はワークスペースのディレクトリのうち`exports/`だけを残して消す

### ホスト側のディレクトリ

`<DataDir>`は`$XDG_DATA_HOME/masuda`（未設定なら`~/.local/share/masuda`）。ワークスペース1つは`<DataDir>/workspaces/<id>/`にまとまる。

| パス | 中身 |
|---|---|
| `workspace.json` | ID・対象リポジトリ・ブランチ・分岐元・ワークフロー・イメージ・状態・結果・理由・現在位置 |
| `staging.git/` | staging bareリポジトリ |
| `data/_run/<名前>` | 実行開始時の入力データ |
| `data/<出現ID>/<名前>` | 各ノードの出力データ（検証済み） |
| `records/engine.json` | engineの記録（`Store`の実体）。現在位置はここから計算する |
| `records/execution-log.jsonl` | 実行ログ。engineのイベントと`export-warning`等 |
| `records/hooks.jsonl` | ゲストのClaude Codeフックの受信記録 |
| `records/gates/<出現ID>-<seq>.json` | 開いたゲートと判断 |
| `records/questions/<出現ID>-<seq>.json` | 開いた質問と回答（または閉じた理由） |
| `records/comments.jsonl` | stagingの差分へのコメント |
| `records/committed-steps.json` | `scope: step`のcommitでコミットした計画のステップ。再開時にforeachがコミット済みのステップを飛ばすのに使う |
| `records/definitions/` | 実行開始時に写した対象リポジトリの`.masuda/` |
| `records/reviews/` | 実行開始時に固定したレビュー観点 |
| `records/privileged/<run-id>/` | 特権コマンドの記録（「特権コマンド」の節） |
| `records/image-build.log` | ゲストイメージのビルドログ |
| `ssh/id` | `masuda chat`用の秘密鍵（0600）。`AttachInfo`のたびに書き直し、Stopで消す |
| `exports/` | 上記 |

### 再開

止まった状態は2つに分ける。

| 状態 | いつ | 再開 |
|---|---|---|
| SUSPENDED | engineの記録の外で止まった。sandboxの起動の失敗（`reason`が`sandbox boot failed: `）と、engineへの呼び出し（`Advance`等）のエラー（`reason`が`engine: `。未承認の特権ノード、承認されていない通信先を選んだノード、execやpublishの基盤の失敗等） | できる。engineは何も記録していないので、原因を直して再開すれば同じノードをやり直す |
| BLOCKED | engineが記録した行き止まり（`engine.StatusBlocked`。進入回数の上限を使い切って行き先が無い、triageの`halt`等） | できない（`Resume`は`FailedPrecondition`）。再開してもengineがまたBLOCKEDを返すだけ |

`Resume`できるのはSTOPPEDとSUSPENDED。SUSPENDEDはStopを挟まずに再開できる。engineへの呼び出しのエラーで止まったSUSPENDEDはVMと実行の窓口が残っている（`chat`で中を見られる）ので、再開の前提の確認（下の2と、特権ノードの事前検査）が通ってから、Stopと同じ手順（会話ログと実行ログを書き出してVMを壊す）で片付けて作り直す。確認で断ったときはVMを残す。SUSPENDEDへの`Stop`はVMを壊してSTOPPEDにする。BLOCKEDへの`Stop`はVMを壊すだけで状態はBLOCKEDのまま残し、`Stop`を挟んで再開できてしまうことを防ぐ。

ゲストの`next_task`には、engineへの呼び出しのエラーをそのままMCPのエラーとして返す（`blocked`とは返さない）。ループ規約は`done`・`blocked`でだけメインセッションを終えるので、SUSPENDEDの間もメインセッションは残り、人間が原因を直した後に`next_task`を呼び直せばResumeを待たずに進むこともある。

1. 定義は`records/definitions/`の写しから読み直す。作業ツリーの`.masuda/`がその後変わっていても、始めたときと同じ定義で進む
2. 承認・秘密の値・`stallAfter`は作業ツリーの`settings.local.json`と秘密ストアから読み直す（取り消し・値の入れ替えを反映するため）。ワークフローが届く特権ノードの宣言（写しから）と承認もここで確かめる（`Run`と同じ）
3. 再開前に開いていた`ask_human`の質問を、記録に理由「再開で破棄」を書いて閉じる。聞いていたサブエージェントは前のVMと共に無くなっており、再開後はengineが同じ出現のタスクを渡し直すので、新しいエージェントが改めて聞く。`questions:`を書いた固定の質問はengine自身が待っているので閉じない
4. 新しいVMを作り、stagingから再cloneする
5. 最新の`refs/masuda/wip/<出現ID>`のtreeを`git read-tree -m -u HEAD <wip>`で作業ツリーへ戻す。HEADはブランチのまま動かさず、コミットしていない作業はindexに載った変更として戻る。engineの基準点（WIPスナップショット）と作業ツリーを揃えるため
6. engineを1回進め、位置に応じてゲート待ち・質問待ち・RUNNINGになる

`masuda serve`を再起動すると、前のプロセスで動いていたワークスペースはSTOPPEDになる。自動では再開しない（止まっていた間に対象リポジトリや定義が変わっているかもしれず、再開するかは人間が決める）。

## 5. ワークフロー

### ノードの種類

| type | 誰が動かすか | 書けるキー | 概要 |
|---|---|---|---|
| `agent` | エージェント | `role`、`max`、`inputs`、`outputs`、`egress`、`secrets` | 役割（エージェント定義）に1つのタスクをさせる |
| `exec` | エンジン（サンドボックス内で実行） | `command`、`inputs`、`outputs`、`max`、`egress`、`secrets`、`timeout` | 決定論のコマンド。`/workspace`をcwdに、入力を`/masuda/in/<occ>/`、出力を`/masuda/out/<occ>/`で受け渡す。終了コード0で`done`、それ以外で`failed` |
| `approval` | 人間 | `gate`、`target` | 人間の承認を待つ。`target`は`plan`・`diff`（publishされるコミット済みの差分。commitの後の承認）・`step-diff`（これからcommitされる差分。commitの前の承認）・任意のデータ名 |
| `question` | 人間 | `role`または`questions`、`outputs` | エージェント（または固定の質問）が構造化した質問を出し、人間がAPIで答える。答えは次ノードの入力データになる |
| `foreach` | エンジン | `over`、`body`、`on_incomplete`、`with`、`max` | 項目ごとに`body`のワークフローを直列に実行する |
| `workflow` | エンジン | `workflow`、`with`、`max` | 別のワークフローを呼ぶ |
| `commit` | エンジン（ホスト） | `scope` | 上記 |
| `publish` | エンジン（ホスト） | `target`、`export` | 上記 |
| `discard` | エンジン（ホスト） | `export` | 上記 |

旧設計の工程型（investigate/plan/implement/review）は廃止し、同梱ワークフローが`agent`と`exec`の組み合わせとして同じ流れを提供する。コードを変えないワークフロー（外部サービスから課題を取り、調査し、人間と議論して書き戻す等）も同じ語彙で書ける。

### データ

ワークフローの中で受け渡すものはすべて**名前付きのデータ**で、実体はホスト側のファイル。

- ワークフローは`inputs:`で受け取るデータ名を宣言し、ノードは`outputs:`で書くデータ名を宣言する
- データ名には任意で**JSON Schema**を結びつけられる（`.masuda/schemas/<name>.json`）。スキーマがあるデータは、エンジンが受け取る境界で検証し、通らなければそのノードへ差し戻す。スキーマがないデータは空でないことだけ確かめる
- エンジンが自分で用意するデータ: `diff`（baseからの差分）、`step-diff`（HEADからの差分）、`fix-diff`（修正開始時点からの差分）。これらはstagingのrefから計算する
- 同梱の定義が使う`plan`・`findings`・`commit-message`・`selected-perspectives`は、同梱のスキーマを持つ普通のデータであって、エンジンの特別扱いではない

### ノード単位の方針

`egress:`（許可ホスト）と`secrets:`（使える秘密の名前）はノードに書く。エンジンはノードに入るたびに`Runner.SetPolicy`を呼び、sandbox serviceがVMの許可リストと置換対象を切り替える。書かなければ、Claude APIへの経路以外は閉じている。

### エンジンが固定する不変条件

ユーザーが書くのは方針だけで、次はエンジンが常に適用し、定義で外せない。

- **triage**: エージェントが`report_concern`で懸念を報告したら、どの状態からでも割り込んで`triage`ゲートを開く
- **計画外変更の検出**: `commit`の直前に必ず走り、対象外の変更は`deviation`ゲートへ
- **書き込めるエージェントの判定**: エージェント定義の自己申告でなく`tools`から判定する。書き込めないエージェントの実行中に作業ツリーが変われば`deviation`ゲートを開く
- **進入回数の上限**（`max`、既定3）と、設定できない**ヒューズ**（1実行の出現20000）
- **実行場所**: エージェントも`exec`もすべてサンドボックス内。commit/publish/discardだけがホストで動く
- **出力は未検証データ**: サンドボックスから出てくるものはすべて検証を経る。ホストFSへ届く経路はcommit/publishだけ

`masuda workflow show`は、ユーザーの定義にエンジンが差し込む割り込みを合成した図を出す。

### 定義の置き場所

| 種類 | 同梱 | 対象リポジトリでの上書き |
|---|---|---|
| ワークフロー | masuda-engineに埋め込み | `.masuda/workflows/**.yaml` |
| エージェント | 同上 | `.masuda/agents/**.md` |
| スキーマ | 同上 | `.masuda/schemas/*.json` |
| レビュー観点 | masudaに埋め込み（`masuda init`が書き出す） | `.masuda/reviews/*.md` |

同じパスのファイルがリポジトリにあれば同梱のものを丸ごと置き換える。タスク開始時にすべてホスト側へスナップショットし、以後そのタスクは固定した定義だけを使う。

- ワークフロー・エージェント・スキーマ・設定は、作業ツリーの`.masuda/`を`records/definitions/`へ写したものから読む
- レビュー観点は、同梱の観点に`records/definitions/reviews/`を重ねたものを`records/reviews/`へ固定する。ホストが返す観点の一覧（`Runner.Items(perspectives)`）と、ゲストの`/masuda/reviews/`はどちらもこの写しから作る。ゲストのcloneの`.masuda/reviews/`は使わない（`.masuda/`をコミットしていないリポジトリでも観点が揃うように）

## 6. サンドボックス

### VM

- Gondolin（QEMU、LinuxはKVM、macOSはHVF）。1ワークスペースにつき1VM。メモリ・CPUは設定で決める（既定4GiB・4）
- VMの書き込めるルートディスクの最小容量は`settings.json`の`images.<entry>.diskMiB`（既定4096MiB）。`CreateSandbox`の`disk_mib`として渡す。特権VMも、使うイメージのエントリの値で作る（`RunJob`の`disk_mib`）
- ゲストイメージは対象リポジトリの`.masuda/images/<entry>/Dockerfile`からDockerでビルドし、Gondolinの`oci.image`で資産化する。カーネルと起動層はGondolinのAlpine資産で、ホストに依存しない
- イメージに必要なもの: Claude Code（native）、tmux、git、openssh-server、非rootユーザー`ubuntu`（uid 1000）、`ca-certificates`。masudaがrootfsへ注入するものは無い
- ゲストの中にmasudaのバイナリは無い。ゲストが知っているのはMCPのURLとループ規約だけ
- VMは使い捨て。停止・クラッシュ・ホスト再起動の後は新しいVMをstagingから作り直す。VM内ディスクに残したものは失われる前提

### ゲスト内の配置

| パス | 中身 | 誰が書くか |
|---|---|---|
| `/workspace` | stagingからのclone | ゲスト |
| `/masuda/in/<出現ID>/` | そのノードの入力データ・タスクファイル | ホスト（`WriteFile`） |
| `/masuda/out/<出現ID>/` | そのノードの出力データ | ゲスト。ホストが`ReadFile`で読み出して検証する |
| `/masuda/privileged/<run-id>/` | 特権コマンドの結果の写し（`exit-code`・`log`・`outputs/`） | ホスト |
| `/masuda/reviews/*.md` | 実行開始時に固定したレビュー観点 | ホスト（起動時） |
| `/masuda/checks/<名前>` | `settings.json`の`checks`を実行可能スクリプトにしたもの | ホスト（起動時） |
| `/masuda/privileged-commands.json` | 特権コマンドの宣言（承認の状態を除く）。ホストは読み戻さない | ホスト（起動時） |
| `~/.claude/CLAUDE.md`、`~/.claude/agents/` | ループ規約、サブエージェント定義 | ホスト（起動時） |

ホストはゲストが書いた場所を読むとき、そのノードの出力ディレクトリ以外は読まない。読んだものは必ず検証を通す。

### ネットワークとegress

- Gondolinのユーザー空間ネットワークスタックが、HTTP/1.xとTLS（MITM）以外を落とす。DNSは合成モード。ゲストには汎用NATが無い
- 許可ホストは**宣言**（`.masuda/settings.json`の`egress`）と**承認**（`.masuda/settings.local.json`）の積集合が上限で、各ノードの`egress:`がその部分集合を選ぶ。Claude APIのホストは常に許可
- ゲスト→ホストは`tcp.hosts`で`masuda.internal`→そのワークスペース専用のローカルポートへ対応付ける。ポートはサンドボックスごとに別で、別のVMからは届かない
- git over SSHが要る場合はGondolinのSSHプロキシ（exec専用、ホスト鍵検証はホスト側）を使い、`execPolicy`で「pushできるのは自分のブランチだけ」を強制する

### 秘密

本物の秘密はゲストに入れない。

- 宣言: `.masuda/settings.json`の`secrets`に、名前・送ってよいホスト・置換場所（`header`既定、`body`は明示）・モード（`placeholder`既定、`plaintext`は例外）を書く
- 値: `masuda secret set <NAME>`で`$XDG_DATA_HOME/masuda/secrets/<repo-hash>/<NAME>`（0600）に置く。チームメイトごとに別の値を持てる
- Claude APIのトークンも同じ仕組みの1つ（名前`CLAUDE_CODE_OAUTH_TOKEN`、ホスト`api.anthropic.com`）。`claudeToken`でどの登録トークンを使うかを選ぶ。トークンはアカウントに付くものなので、既定の置き場所はユーザー単位の`$XDG_DATA_HOME/masuda/secrets/_user/<NAME>`（`SetSecret`の`repo_root`が空、`masuda secret set CLAUDE_CODE_OAUTH_TOKEN`の`--repo`無し）。リポジトリの置き場所に同じ名前があればそちらを優先する
- `.env`はコピーしない。`envFiles`の宣言から、秘密はプレースホルダ・公開値は実値で**生成**する
- `plaintext`モードはローカルの承認が無ければ起動を拒否し、`masuda secret list`で一目で分かる

### 特権コマンド

対象リポジトリのテストがrootやDockerを要する場合、その実行だけを**2つ目のVM**（root、使い捨て）へ切り出す。

- 宣言: `privilegedCommands`に名前・イメージエントリ・コマンド・`inputs`（gitignore対象で運ぶ必要があるパスのglob）・`outputs`・タイムアウト。承認はローカル
- 実行: sandbox serviceの`RunJob`を1回呼ぶ。VMの作成・ファイルの投入・実行・`outputs`の回収・破棄はsandboxが持ち、masudaは方針（宣言・承認・通信先・どのツリーを渡すか・結果の置き先）だけを持つ
- 受け渡し: 呼ばれた時点でメインVMの作業ツリーのスナップショットを取り（`refs/masuda/wip/privileged-<run-id>`）、ホストの一時ファイルのbundleにして`RunJob`の`HostFile`で特権VMへ置く。前処理（`setup_shell`）で`/workspace`へ`git init`→`fetch`→`checkout`して展開し、bundleを消す。展開に失敗したらコマンドは動かさず、基盤の失敗としてエラーにする。ノードの境界のWIPスナップショットではないので、ノードの途中で書いたテストもそのまま流せる。`inputs`に当たるファイルは`RunJob`の`FromSandbox`で、sandboxの中でメインVMから特権VMへ直接写す（masudaのプロセスを通らない）。gitignoreの内容が暗黙に境界を決めることはない
- 結果: `outputs`は`RunJob`がホストの`records/privileged/<run-id>/outputs/`へ回収する。masudaは終了コード・ログを記録に書き、写しをメインVMの`/masuda/privileged/<run-id>/`へ置く。特権VMはAPIトークンもMCPも持たない
- 呼び出し口はMCPツール`run_privileged_command(name)`と、ワークフローの`privileged`ノード（`name`で宣言を指し、終了コードで`done`/`failed`に分岐する。engineの`Runner.RunPrivileged`）。どちらも同じ実行の手順を通る。コマンド文字列を渡す口は無い。宣言は`records/definitions/`の写しから読み、承認は作業ツリーの`settings.local.json`のハッシュと照らす。実行中にエージェントが作業ツリーの宣言を書き換えても、承認と食い違って断られるだけになる。ワークフロー（呼び出す部品のワークフローを含む）が届く`privileged`ノードの名前は、`Run`・`Resume`の始めにすべて宣言済み・検査に通る・承認済み（ハッシュが一致）であることを確かめ（`privileged.Resolve`）、足りなければ何も作らずにまとめて`FailedPrecondition`で返す。それでも実行中に承認が取り消されれば、ノードはengineのエラーとして実行をSUSPENDEDで止め、承認の後の`Resume`（または`Advance`）で同じ出現をやり直す
- 宣言（承認の状態を除く）は実行開始時にゲストの`/masuda/privileged-commands.json`へ置く（[guest-protocol.md](../guest-protocol.md)）。エージェントが使える名前を知るためだけのもので、ホストは読み戻さない
- 同じワークスペースでは1つずつ動かす。`<run-id>`は`0001`からの連番

ホストの記録は`records/privileged/<run-id>/`に置く。

| ファイル | 中身 |
|---|---|
| `result.json` | 名前・呼んだ`privileged`ノードの出現（MCPからなら無し）・宣言のハッシュ・渡したWIPのref・開始と終了の時刻・終了コード・シグナル・タイムアウトの有無・回収した`outputs`・`outputs_error`、または失敗の理由 |
| `exit-code` | 終了コード |
| `log` | 標準出力と標準エラーの末尾 |
| `outputs/` | 回収した`outputs`のファイル |

`outputs_error`は、宣言した`outputs`のうち回収できなかったもの（当たらなかったパターン、読めなかったファイル）の説明で、コマンドの終了コードとは独立。回収できた分は`outputs`に入る。

### 設定ファイル

対象リポジトリの`.masuda/`に2つ置く。`settings.json`はコミットされる宣言で無条件には信頼せず、`settings.local.json`（gitignore対象）の利用者ごとの承認と揃ったときだけ効く。どちらも知らないキーがあれば読み込みを断る。

`settings.json`（宣言）:

| キー | 意味 |
|---|---|
| `image` | ゲストイメージのエントリ（`.masuda/images/<entry>/Dockerfile`）。既定`default` |
| `images.<entry>.diskMiB` | そのエントリで作るVMのルートディスクの最小容量。既定4096 |
| `egress` | ゲストが届いてよいホストの宣言（先頭の`*.`だけワイルドカード） |
| `secrets` | 秘密の宣言（`name`・`hosts`・`mode`・`in`）。`CLAUDE_CODE_OAUTH_TOKEN`は予約済みで宣言できない |
| `envFiles` | ゲストの作業ツリーに生成するdotenv（`path`・`vars`）。`.git/`の下と作業ツリーの外は書けない |
| `privilegedCommands` | 特権コマンドの宣言（`description`・`command`・`image`・`inputs`・`outputs`・`timeoutSeconds`。`description`は承認のハッシュに含めない） |
| `checks` | チェック名→シェルコマンド。ゲストの`/masuda/checks/<名前>`になる |
| `claudeSettings` | ゲストの`~/.claude/settings.json`へ合成するオブジェクト（フックはmasudaのものが優先） |
| `agents` | 役の名前→`model`・`effort`の上書き。エージェント定義のfrontmatterより優先。定義に無い役の名前は`run`・`resume`・`workflow check`が断る |
| `publish.remote` | `target: remote`のpublishがpushする実リポジトリのremoteの名前。既定`origin` |

`settings.local.json`（利用者ごとの承認と値）:

| キー | 意味 |
|---|---|
| `egressApproved` | `egress`のうち承認したホスト。宣言との積集合が許可の上限 |
| `secretsApproved` | `plaintext`モードの秘密のうち、本物の値をゲストに置いてよいと承認した名前 |
| `privilegedCommandsApproved.<名前>.declHash` | 特権コマンドの承認。承認した時点の宣言の正準JSONのsha256で、宣言が変われば失効する |
| `claudeToken` | Claude APIのトークンとして使う秘密ストアの名前。既定`CLAUDE_CODE_OAUTH_TOKEN` |
| `vars` | `envFiles`の公開値（秘密として宣言していない変数の値） |
| `stallAfter` | 無活動のしきい値（Goのduration）のこのリポジトリでの上書き。無ければ`config.json`の値（「活動の観測と停止の検知」の節） |

秘密の値はどちらにも置かない（`<DataDir>/secrets/<repo-hash>/<NAME>`、Claudeのトークンは既定で`<DataDir>/secrets/_user/<NAME>`）。

リポジトリに依らない`masuda serve`全体の設定は`$XDG_CONFIG_HOME/masuda/config.json`（未設定なら`~/.config/masuda/config.json`。`masuda serve --config`で変えられる）。無ければすべて既定で、知らないキーがあれば`masuda serve`は起動しない。

| キー | 意味 |
|---|---|
| `listen` | UDSに加えてConnectを待ち受けるループバックのアドレス（例`"127.0.0.1:7788"`）。ループバック以外は断る。無ければUDSだけ（「9. 公開API」の節） |
| `sandboxSocket` | `masuda-sandbox serve`のUDS。既定`$XDG_RUNTIME_DIR/masuda-sandbox.sock`。`--sandbox-socket`を明示すればそちらが勝つ |
| `stallAfter` | 無活動のしきい値の既定（Goのduration）。既定`10m` |
| `diskWarnBytes` | ワークスペース置き場の使用量の警告しきい値（バイト）。既定20GiB。置き場はserve全体で1つなのでここだけに置く |

## 7. ゲストとホストの間

ゲストのエージェントが使うMCPツール（`masuda.internal`経由、HTTP）。中身は`docs/guest-protocol.md`。

| ツール | 用途 |
|---|---|
| `next_task` | メインエージェントが次のタスクを受け取る。ゲート待ち・質問待ちの間はブロックする |
| `write_output(name, content)` | サブエージェントが出力データを書く。ホストが検証して受け付ける |
| `report_result(outcome, feedback?)` | ノードの終わり方を報告する |
| `report_concern(text)` | セキュリティ上の懸念を自己申告する（triage） |
| `ask_human(questions)` | `question`ノードの質問を出す |
| `run_privileged_command(name)` | 上記 |

同じエンドポイントに`POST /hooks`があり、ゲストのClaude Codeフック（`Notification`・`PostToolUse`・`Stop`・`SessionEnd`）がイベントを送る。

## 8. 活動の観測と停止の検知

ワークスペースの活動は`working / waiting_input / waiting_gate / waiting_question / stalled / dead / idle`と最終活動時刻で表し、公開APIの`Watch`で配信する。次の情報源を合成する。

| 情報源 | 分かること | 信頼性 |
|---|---|---|
| sandbox serviceのHTTPフック（Claude APIへの全リクエストがホストを通る） | 推論中（リクエスト進行中）、ツール実行中（直近に完了）、無活動の継続時間 | ゲストが汚染されていても偽装できない |
| ゲストのClaude Codeフック（`/hooks`） | 許可待ち、idle、質問の内容、正常終了 | ゲストの協力が前提 |
| sandbox serviceの`Exec`でのプロセス確認 | tmuxセッションと`claude`の生存 | 確実 |

無活動がしきい値を超えれば`stalled`、プロセスが無ければ`dead`。「ツールを呼び続けるが進捗がない」暴走は活動として見えるので、進入回数の上限で止める。

### 活動の決め方

上から順に見て、最初に当たったものが活動になる。

1. ワークスペースの状態: DONE・STOPPED・SUSPENDED・BLOCKEDなら`idle`、ゲート待ちなら`waiting_gate`、質問待ちなら`waiting_question`。STARTINGもエージェントがまだ動いていないので`idle`で、`detail`に起動の段階（`building image (log: ...)`→`booting the VM`→`preparing the guest`→`starting Claude Code`）を入れ、段階が変わるたびに`status`のイベントを流す
2. `claude`（tmuxのセッション）が無い: `dead`。`SessionEnd`フック、sandboxの停止・失敗、`Exec`での生存確認のどれかで分かる
3. 進行中のAPIリクエストがある: `working`
4. ゲストの`Notification`フックが待ちを言っている（`idle_prompt`→`idle`、`permission_prompt`→`permission`、`elicitation_dialog`→`question`。`input_wait`に入る）: `waiting_input`。その後にHTTP・ツール・MCPの活動があれば消える
5. RUNNINGで、最終活動からしきい値を超えた: `stalled`
6. それ以外: `working`

APIリクエストを入力待ちより先に見るのは、フックがゲストの協力を前提とする補助情報で、ホストが観測したリクエストの方が確かなため。ただしsandboxはクライアントが応答前に切ったリクエストの終わりを知らせないので、進行中のリクエストは次のように打ち切る。

- 始まってから2分を超えたものは進行中とみなさない
- `idle_prompt`の通知が来たら、その60秒より前に始まって終わっていないものを捨てる（Claude Codeは入力待ちが60秒続くと`idle_prompt`を出すので、それより前のリクエストは切られている）

活動の観測はメモリにだけ持つ。serveを再起動したワークスペースはSTOPPED（活動は`idle`）になり、Resumeで観測をやり直す。

### 無活動のしきい値

- `config.json`の`stallAfter`（Goのduration、既定`10m`）を、対象リポジトリの`settings.local.json`の`stallAfter`が上書きする。何分黙れば異常かは利用者のマシンの速さやClaudeのプランで変わるので、コミットされる`settings.json`には置かない
- `masuda serve --stall-after`が0でなければ、全ワークスペースでそちらが勝つ。既定は0（設定に従う）
- 読むのはRun・Resumeで実行を組み立てるとき。読めない値・正でない値はFailedPreconditionで断る。変更はResumeか次のRunから効く
- 見回りの間隔は最も短いしきい値の1/4（1秒〜30秒）

### ディスク使用量

- serveは60秒ごとに`<DataDir>/workspaces/`の通常ファイルの合計と、その内数の`<id>/exports/`の合計を測る
- しきい値は`config.json`の`diskWarnBytes`（既定20GiB）。置き場はserve全体で1つなので、リポジトリごとの設定には置かない
- 下回っていた状態から超えたときにだけ、標準エラーへのログとWatchの`ServeNotice{kind: "disk-warning", value: 使用量}`（`workspace_id`が空）を1回出す。超えたままなら繰り返さない（serveを再起動すると、超えたままなら起動直後にもう一度出る）
- 何も消さない。何を残すかは利用者が`masuda remove`で決める

## 9. 公開API

`masuda serve`がConnectで提供する。UIもCLIも同じAPIを使う。Protobufで定義し、gRPCとHTTP+JSONの両方で呼べる。

- Workspace: `Run`、`Resume`、`Get`、`List`、`Watch`（状態とイベントのストリーム）、`Stop`、`Remove`、`AttachInfo`
- Gate: `ListOpen`、`Get`、`Decide`（approved/rejected/dismiss/halt/redo、コメント付き）
- Question: `ListOpen`、`Answer`
- Staging: `ListRefs`、`GetCommit`、`Diff`、`GetBlob`、`ListComments`、`AddComment`（commit+file+line）
- Config: `ListEgress`/`ApproveEgress`/`RejectEgress`、`ListSecrets`/`SetSecret`/`ApproveSecret`/`RejectSecret`、`ListPrivilegedCommands`/`ApprovePrivilegedCommand`、`ListImages`/`BuildImage`
- Workflow: `List`、`Show`（合成した図）、`Check`

認証はローカル利用のみを前提とする。`$XDG_RUNTIME_DIR/masuda.sock`（UDS）で待ち受け、`config.json`に`listen`があるときだけ、そのループバックのアドレスでも同じハンドラで待ち受ける（ブラウザのGUIはUDSに繋げないため）。ループバックの待ち受けはCORSで任意のオリジンを許し、`Host`ヘッダーがループバックの名前でない要求は断る（DNS rebinding対策）。リモートから使う場合はユーザーがリバースプロキシ等で守る。

### Watch

- イベントの`seq`は全ワークスペースで1本の通し番号。`after_seq`を渡せばその続きから受け取れる。再送用にメモリに持つのは直近10000件だけで、`after_seq`の次のイベントがそこに残っていなければ`OutOfRange`で終わる（黙って最古から続けると、取りこぼしにクライアントが気づけない）。serveを再起動すると再送できるのは再起動後のイベントだけになる（実行記録そのものは`records/execution-log.jsonl`に残る）
- `after_seq`が0（新しいものだけ）でも、最初に対象のワークスペースごとの今の`status`を1つずつ送る。この`status`は新しい番号を振らず、`seq`に今の最新の番号を入れる。購読の開始と状態の変化が前後しても、変化を取りこぼしたまま次の変化まで何も見えなくなることがない
- `status`は内容が前に流したものと変わったときだけ流す。時刻（`updated_at`・`last_activity`）だけの違いでは流さない
- `workspace_id`が空のイベントはserve全体についての通知（`ServeNotice{kind, detail, value}`）で、どのワークスペースを指定したWatchにも届く。今出すのはディスク使用量の警告（`disk-warning`）だけ
- `after_seq`が最新の`seq`より大きい（serveの再起動で番号が振り直された後に古い番号で繋いだ等）か、`after_seq+1`が再送できる最古の`seq`より小さければ`OutOfRange`で終わる。後者の理由には再送できる最古の`seq`を入れる。クライアントは`after_seq: 0`で繋ぎ直し、最初の`status`で状態を組み立て直す。`after_seq: 0`は常に通る

### Gateの判断

- 判断は`approved`・`rejected`・`dismiss`・`halt`・`redo`。`dismiss`・`halt`・`redo`はtriageゲートへの判断
- `approved`のときだけ`target_hash`がゲートのものと一致しなければならない（見た内容と違うものを承認させない）。triageの判断に`target_hash`は要らない
- 判断済みのゲートへの`Decide`はFailedPrecondition

### Workflow

`Run`と違い写しは取らず、作業ツリーの`.masuda/`（と同梱の定義）を直接読む。`repo_root`が空なら同梱だけを読む。

- `List`: ワークフローごとに、どこから来たか（`origin`、engineの`Set.Origins`）と受け取る`inputs`
- `Show`: engineが割り込みを合成したMermaidの図（`Set.Mermaid`）
- `Check`: `Set.Check`の問題の一覧。`workflow`が空ならrootのワークフロー（他のどのワークフローの`Set.Reachable`にも含まれないもの）をそれぞれ検査し、重複を除く。部品はrootから辿って検査される。定義が読み込めないときはエラーにせず、その理由を問題の1つとして返す

### AttachInfo

- sandboxの`EnableSsh`（ユーザー`ubuntu`）を呼び、返った秘密鍵を`<DataDir>/workspaces/<id>/ssh/id`へ0600で置く。sandbox serviceが書いた鍵ファイルは使わない（置き場所と寿命がsandbox側の都合で決まり、masudaから見えないため）
- 返す`ssh_argv`は、`-i`をその鍵へ差し替え、接続先の前に`-t`、後ろに`tmux attach -t claude-work`を置いたもの
- 鍵は`EnableSsh`のたびに替わる。前の接続はそのまま残る。鍵はStopで消す
- 起動中（boot前）・止まっているワークスペースはFailedPrecondition。フェイクsandboxは`Unimplemented`を返す

### CLI

`masuda serve`・`masuda init`・`masuda version`・`masuda doctor`・`masuda privileged-command run`以外のサブコマンドは、`--socket`で指定した`masuda serve`の公開APIを叩くだけのクライアント。

| コマンド | 動き |
|---|---|
| `masuda run <workflow>` | ワークフローを新しいワークスペースで始める（`--repo`・`--branch`・`--base`・`--image`・`--input <名前>=<値>`／`<名前>=@<ファイル>`） |
| `masuda resume <id>` | 再開 |
| `masuda list [--all] [--repo <dir>]` | `--repo`を付けるとそのリポジトリのワークスペースだけ。1行1ワークスペースで`ID BRANCH STATE ACTIVITY POSITION OPEN`。ACTIVITYは活動の種類と最終活動からの経過、OPENは開いているもの（`gate:<名前>`・`question:<出現ID>`）。DONE・STOPPEDは`--all`のときだけ出し、SUSPENDED・BLOCKEDは常に出す |
| `masuda chat <id>` | `AttachInfo`の`ssh_argv`を`exec`する。`Unimplemented`なら「このsandboxではsshで接続できません（フェイクsandbox等）」 |
| `masuda watch [<id>]` | 状態とイベントを流し続ける。`workspace_id`が空のイベントは種類と本文だけで出す |
| `masuda gate list / show / approve / reject / dismiss / halt / redo` | ゲートの一覧・内容・判断。`dismiss`・`halt`・`redo`は`<id> <出現ID> [--comment]`で`target_hash`を要らない。`show`はtriageなら懸念の本文を字下げで、deviationなら対象外のファイルを箇条書きで出し、未判断ならそのゲートで打てるコマンドを添える |
| `masuda question list / answer` | 質問の一覧・回答 |
| `masuda stop <id>` / `masuda remove <id>` | sandboxを止める（記録は残す）／exports以外を消す（動いているものは`--force`で止めて消す） |
| `masuda workflow list / show / check [<workflow>] [--repo <dir>]` | `--repo`を省略すると今いる作業ツリーのトップ（`git rev-parse --show-toplevel`）、作業ツリーの外なら同梱だけ。`check`は問題があれば終了コード1 |
| `masuda egress / secret / privileged-command / image ...` | 宣言の一覧と承認、秘密の値の登録（標準入力から）、イメージのビルド |
| `masuda privileged-command run <name>` | 特権コマンドの単体実行。serveを通さず`masuda-sandbox serve`へ直接つなぐ（`doctor`と同じ）。宣言・承認・通信先は作業ツリーの`settings.json`・`settings.local.json`から`privileged.Resolve`で、イメージは作業ツリーの`.masuda/images/`から（`serve.BuildImage`。ビルドの記録は`--data-dir`へ）。ツリーは一時ディレクトリへcloneしたリポジトリで作り（`staging.SnapshotWorktree`。GIT_DIRを一時リポジトリ、GIT_WORK_TREEを利用者の作業ツリー、GIT_INDEX_FILEを一時ファイルにして`add -A`→`write-tree`→`commit-tree`）、利用者のリポジトリにrefもオブジェクトも書かない。`inputs`は作業ツリーのgitignoreされたファイルから`HostFile`で渡す。実行は`privileged.Run`（メインのゲストが無いので結果はホストにだけ置く）。終了コードは特権コマンドのもの（シグナルは128+番号、時間切れは124、masuda自体の失敗は1） |
| `masuda init` | 対象リポジトリに`.masuda/`の雛形を置く |
| `masuda serve` | 常駐プロセス（`--socket`・`--data-dir`・`--sandbox-socket`・`--stall-after`・`--config`・`--fake-sandbox`） |
| `masuda version` | masudaのバージョン（`-ldflags -X main.version`）・Goの版・sandbox.protoのSHA-256と、届けば接続先のsandboxの`GetServerInfo`と契約の一致 |
| `masuda doctor` | 前提（config.json・git・Docker・Node・QEMU・KVM/HVF・sandbox serviceの到達と契約・Claudeトークン）を確かめ、足りないものと直し方を出す。足りなければ終了コード1 |

## 10. 脅威モデル

Gondolinの前提をそのまま採り、masudaの不変条件を足す。

- **守るもの**: ホストのファイルシステム、ホストの秘密（APIトークン、外部サービスの認証情報）、実リポジトリとリモート
- **敵**: ゲスト内のコード全部。プロンプトインジェクション等で汚染されたエージェントが、ゲスト内のroot相当を取ったとしても成り立つこと
- **信頼するもの**: ホストOS、`masuda serve`と`masuda-sandbox serve`のプロセス、QEMUの隔離、ゲストイメージの供給元
- **ホストで動く対象リポジトリ由来のもの**: 作業ツリーの`.masuda/images/<entry>/Dockerfile`を`docker build`することだけ（`RUN`はホストのDockerの中で、egressの制限なしに動く）。定義は`masuda run`を打った作業ツリーの`.masuda/`から写し、`--branch`で指したブランチの`.masuda/`は使わない。したがってホストで動くのは、人間が自分でチェックアウトした定義だけで、チェックアウトしたリポジトリでビルドツールを打つのと同じ信頼になる。信頼できないブランチは作業ツリーに取り出さず`--branch`で指す。定義をブランチから取る設計に変えるなら、承認を挟むのではなく、特権コマンドと同じく使い捨てVMの中でビルドする（sandboxの契約変更）
- **守備範囲外**: QEMU/KVM/HVFの脆弱性によるVM脱出、同じホストアカウントの別プロセス、サイドチャネル、DoS（VM内のCPU/メモリ消費、ホストのNodeプロセスへの負荷、stagingの肥大）。masudaはstagingとexportsのディスク使用量だけ監視する
- **不変条件**: 秘密はゲストに入らない。ゲストが書いたものはすべて未検証データ。ホストFSへの経路はcommit/publishと宣言した`outputs`だけ。外部への経路は許可リストとSSHの`execPolicy`だけ。許可したホストへは、ゲストが読めるものは何でも送れる（これは許容する）

## 11. 対応プラットフォームと依存

| | Linux x86_64（WSL2含む） | macOS arm64 |
|---|---|---|
| 仮想化 | QEMU + KVM（`/dev/kvm`） | QEMU + HVF |
| 必要なもの | `qemu-system-x86`、`qemu-utils`、Node 22.19以上、Docker（イメージビルド）、git | `brew install qemu node`、Docker Desktop等 |
| sudo | 不要 | 不要 |

masuda自身の開発にはGo 1.26以上とbuf。

### sandboxとの互換性

masudaとmasuda-sandboxは同じタグでリリースし、組で使う（[リリース手順](release.md)）。組が合っているかは契約の文面で確かめる。masudaはビルド時に、sandboxクライアントの生成元`sandbox.proto`のSHA-256を`internal/sandboxcontract`の定数に埋める（`go generate ./internal/sandboxcontract/`、CIが生成し直して差分が無いことを確かめる）。sandbox serviceは`GetServerInfo`の`contract_sha256`に自分の生成元の同じ値を返す。

- `masuda serve`は起動時に`GetServerInfo`を呼ぶ。値が違えば（`GetServerInfo`を持たない古いsandboxも）起動しない。届かないだけなら警告を出して起動を続ける（sandboxを後から起動する順序も許すため）
- `Run`・`Resume`はワークスペースを作る・STARTINGにする前にもう一度呼ぶ。値が違えば`FailedPrecondition`（理由に両方のバージョン）、届かなければ`Unavailable`
- フェイクsandboxは常にmasudaの値を返す
- `masuda version`・`masuda doctor`も同じ確認をして表示する

## 12. 旧設計から残すもの・捨てるもの

**残す（アイデアまたは移植）**: ワークフローエンジンの中核（定義・検査・出現IDによる記録、`v1-frozen-workflow-engine`の`internal/workflow`）、14のレビュー観点とchecker/fixerの往復、ゲートとtriageの意味論、`git clone --bare --local`とfast-forward publish、宣言と承認の二重構造。

**捨てる**: `internal/microvm`、rootfsビルダー、`internal/egressproxy`、`masuda-net-helper`、`setup-vm-host.sh`、APIゲートウェイ、mcp-relay、trusted/写しの照合、`sharedfs`、ワークスペースごとの状態デーモン、Python資産、ADR群。

## 13. マイルストーン

| | 内容 | 状態（2026-10-02） |
|---|---|---|
| M1 | 契約の凍結。3つの契約・契約テスト・作業指示書 | 完了 |
| M2 | sandbox serviceとengineの並列実装。masudaはstagingとworkspace | 完了（契約テスト3リポジトリとも全部緑） |
| M3 | masudaで結線。`masuda run`で同梱developワークフローが実機で1周する | 完了（2周。各リポジトリの`docs/work-orders.md`のM8とliveテスト） |
| M4 | ドキュメント（利用者・統合開発者・開発者の3系統）、設定の整理、ストリーミングの復旧 | 進行中 |
| M5 | macOSでの検証 | 未着手（チームメイトの環境で1周） |

細かい作業単位は各リポジトリの`docs/work-orders.md`にある。
