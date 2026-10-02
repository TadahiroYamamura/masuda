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
  サブエージェントが作業
  write_output / report_result ──▶ /masuda/out/<occ>/ をホストが読み出し、スキーマ検証
                                   ノード境界: WIPスナップショットを staging へ pull
                                   次ノード: egress/秘密の方針を SetPolicy で切替
  ...
  （approval ノード）              ゲートを開き、人間の判断を待つ（API/CLI）
  （exec ノード）                  sandbox.Exec で決定論コマンドを実行、出力を検証
  （commit ノード）                staging 上でホストがコミットを作る（計画の範囲内のみ）
  （publish ノード）               staging → 実リポジトリへ fast-forward、または remote へ push
                                   → VM 破棄、exports を書き出し
```

- 入口は`masuda run <workflow>`だけ。途中で止まったタスクは`masuda run <workspace-id>`で、記録から計算した位置で再開する。VMは使い捨てなので、再開時は新しいVMにstagingから再cloneする
- `masuda chat <workspace-id>`は、sandbox serviceのSSH経由でゲストのtmuxへアタッチする。chatからゲートは閉じられない

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
| `refs/heads/<branch>` | ワークスペースのブランチ。commitノードが進める |
| `refs/masuda/wip/<出現ID>` | ノード境界のWIPスナップショット（作業ツリー全体を`git add -A`したtree、gitignore対象は含まない）。クラッシュ復旧と、exec/特権ノードへの受け渡しに使う |
| `refs/masuda/base` | 分岐元。diffの基準 |

### commitとpublish

- **commit**（エンジン固定ノード）: 最新のWIP treeと`HEAD`の差分を取り、計画の対象ファイル（`scope: step`ならそのステップ、`plan`なら計画全体）だけからなるtreeをホストが`commit-tree`で作り、`refs/heads/<branch>`を進める。対象外の変更があれば`deviation`ゲートで人間に回す。ゲストには`fetch`と`reset --soft`で新しいHEADを知らせる（作業ツリーは触らない）
- **publish**: `target: local`なら実リポジトリの同名ブランチへfast-forward、`target: remote`なら設定したremoteへpush（PR作成は将来のオプション）。publishするcommitハッシュはreview gateで承認されたものと同じでなければならず、違えばpublishしない
- **discard**: 反映せずに片付ける

publishとdiscardの後、`exports/<id>/`に`export:`で指定したデータ、実行ログ、会話ログを書き出し、VMを破棄してワークスペースを閉じる。stagingは`masuda workspace remove`まで残す。

## 5. ワークフロー

### ノードの種類

| type | 誰が動かすか | 書けるキー | 概要 |
|---|---|---|---|
| `agent` | エージェント | `role`、`max`、`inputs`、`outputs`、`egress`、`secrets` | 役割（エージェント定義）に1つのタスクをさせる |
| `exec` | エンジン（サンドボックス内で実行） | `command`、`inputs`、`outputs`、`max`、`egress`、`secrets`、`timeout` | 決定論のコマンド。`/workspace`をcwdに、入力を`/masuda/in/<occ>/`、出力を`/masuda/out/<occ>/`で受け渡す。終了コード0で`done`、それ以外で`failed` |
| `approval` | 人間 | `gate`、`target` | 人間の承認を待つ。`target`は`plan`・`diff`・任意のデータ名 |
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

## 6. サンドボックス

### VM

- Gondolin（QEMU、LinuxはKVM、macOSはHVF）。1ワークスペースにつき1VM。メモリ・CPUは設定で決める（既定4GiB・4）
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
| `/masuda/privileged/<run-id>/` | 特権コマンドの結果の写し | ホスト |
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
- Claude APIのトークンも同じ仕組みの1つ（名前`CLAUDE_CODE_OAUTH_TOKEN`、ホスト`api.anthropic.com`）。`claudeToken`でどの登録トークンを使うかを選ぶ
- `.env`はコピーしない。`envFiles`の宣言から、秘密はプレースホルダ・公開値は実値で**生成**する
- `plaintext`モードはローカルの承認が無ければ起動を拒否し、`masuda secret list`で一目で分かる

### 特権コマンド

対象リポジトリのテストがrootやDockerを要する場合、その実行だけを**2つ目のVM**（root、使い捨て）へ切り出す。

- 宣言: `privilegedCommands`に名前・イメージエントリ・コマンド・`inputs`（gitignore対象で運ぶ必要があるパスのglob）・`outputs`・タイムアウト。承認はローカル
- 受け渡し: 直近のWIPスナップショット（`refs/masuda/wip/<occ>`）をbundleで特権VMへ渡して`checkout`し、`inputs`に当たるファイルをメインVMから`ReadFile`→`WriteFile`で運ぶ。gitignoreの内容が暗黙に境界を決めることはない
- 結果: 終了コード・ログ・`outputs`をホストが回収し、写しをメインVMの`/masuda/privileged/<run-id>/`へ置く。特権VMはAPIトークンもMCPも持たない
- 呼び出し口はMCPツール`run_privileged_command(name)`。コマンド文字列を渡す口は無い

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

ワークスペースの状態は`working / waiting_input / waiting_gate / waiting_question / stalled / dead / done`と最終活動時刻で表し、公開APIのサーバーストリーミングで配信する。2つの情報源を合成する。

| 情報源 | 分かること | 信頼性 |
|---|---|---|
| sandbox serviceのHTTPフック（Claude APIへの全リクエストがホストを通る） | 推論中（リクエスト進行中）、ツール実行中（直近に完了）、無活動の継続時間 | ゲストが汚染されていても偽装できない |
| ゲストのClaude Codeフック（`/hooks`） | 許可待ち、idle、質問の内容、正常終了 | ゲストの協力が前提 |
| sandbox serviceの`Exec`でのプロセス確認 | tmuxセッションと`claude`の生存 | 確実 |

無活動がしきい値を超えれば`stalled`、プロセスが無ければ`dead`。「ツールを呼び続けるが進捗がない」暴走は活動として見えるので、進入回数の上限で止める。

## 9. 公開API

`masuda serve`がConnectで提供する。UIもCLIも同じAPIを使う。Protobufで定義し、gRPCとHTTP+JSONの両方で呼べる。

- Workspace: `Run`、`Resume`、`Get`、`List`、`Watch`（状態とイベントのストリーム）、`Remove`
- Gate: `ListOpen`、`Get`、`Decide`（approve/reject/dismiss/halt、コメント付き）
- Question: `ListOpen`、`Answer`
- Staging: `ListRefs`、`GetCommit`、`Diff`、`GetBlob`、`ListComments`、`AddComment`（commit+file+line）
- Config: `ListEgress`/`ApproveEgress`、`ListSecrets`/`SetSecret`、`ListPrivilegedCommands`/`Approve`
- Workflow: `List`、`Show`（合成した図）、`Check`

認証はローカル利用のみを前提とし、UDSまたはループバックで待ち受ける。リモートから使う場合はユーザーがリバースプロキシ等で守る。

## 10. 脅威モデル

Gondolinの前提をそのまま採り、masudaの不変条件を足す。

- **守るもの**: ホストのファイルシステム、ホストの秘密（APIトークン、外部サービスの認証情報）、実リポジトリとリモート
- **敵**: ゲスト内のコード全部。プロンプトインジェクション等で汚染されたエージェントが、ゲスト内のroot相当を取ったとしても成り立つこと
- **信頼するもの**: ホストOS、`masuda serve`と`masuda-sandbox serve`のプロセス、QEMUの隔離、ゲストイメージの供給元
- **守備範囲外**: QEMU/KVM/HVFの脆弱性によるVM脱出、同じホストアカウントの別プロセス、サイドチャネル、DoS（VM内のCPU/メモリ消費、ホストのNodeプロセスへの負荷、stagingの肥大）。masudaはstagingとexportsのディスク使用量だけ監視する
- **不変条件**: 秘密はゲストに入らない。ゲストが書いたものはすべて未検証データ。ホストFSへの経路はcommit/publishと宣言した`outputs`だけ。外部への経路は許可リストとSSHの`execPolicy`だけ。許可したホストへは、ゲストが読めるものは何でも送れる（これは許容する）

## 11. 対応プラットフォームと依存

| | Linux x86_64（WSL2含む） | macOS arm64 |
|---|---|---|
| 仮想化 | QEMU + KVM（`/dev/kvm`） | QEMU + HVF |
| 必要なもの | `qemu-system-x86`、`qemu-utils`、Node 22.19以上、Docker（イメージビルド）、git | `brew install qemu node`、Docker Desktop等 |
| sudo | 不要 | 不要 |

masuda自身の開発にはGo 1.26以上とbuf。

## 12. 旧設計から残すもの・捨てるもの

**残す（アイデアまたは移植）**: ワークフローエンジンの中核（定義・検査・出現IDによる記録、`v1-frozen-workflow-engine`の`internal/workflow`）、14のレビュー観点とchecker/fixerの往復、ゲートとtriageの意味論、`git clone --bare --local`とfast-forward publish、宣言と承認の二重構造。

**捨てる**: `internal/microvm`、rootfsビルダー、`internal/egressproxy`、`masuda-net-helper`、`setup-vm-host.sh`、APIゲートウェイ、mcp-relay、trusted/写しの照合、`sharedfs`、ワークスペースごとの状態デーモン、Python資産、ADR群。

## 13. マイルストーン

| | 内容 | 完了の判定 |
|---|---|---|
| M1 | 契約の凍結。3つの契約・契約テスト・作業指示書 | 3リポジトリが同時に着手できる |
| M2 | sandbox serviceとengineの並列実装。masudaはstagingとworkspace | 各リポジトリの契約テストが緑 |
| M3 | masudaで結線。`masuda run`で同梱developワークフローが1周する | 実機で1周 |
| M4 | 同梱ワークフロー・観点の移植、`question`ノード、停止検知、公開APIの残り | dogfooding再開 |
| M5 | macOSでの検証 | チームメイトの環境で1周 |
