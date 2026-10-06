# 運用

## 一覧を読む {#list}

```sh
masuda list          # 動いているもの・止まったもの（blocked）
masuda list --all    # 終わったもの（done）・止めたもの（stopped）も
masuda list --repo . # このリポジトリのものだけ
```

```text
ID            BRANCH         STATE         ACTIVITY                 POSITION         OPEN
4f1c2a9e8b3d  feat/triangle  running       working 12s ago          ...              -
9a0b1c2d3e4f  fix/login      waiting_gate  waiting_gate 3m ago      ...              gate:plan
5e6f7a8b9c0d  feat/export    running       waiting_input(idle) 2m ago ...            -
```

| 列 | 中身 |
|---|---|
| ID | ワークスペースのID |
| BRANCH | 作っているブランチ |
| STATE | ワークスペースの状態（[概念](concepts.md#workspace)） |
| ACTIVITY | VMの中のClaude Codeが今何をしているか（下表）と、最後に活動してからの経過 |
| POSITION | ワークフローの今の位置。止まった理由（`reason`）か結果（`outcome ...`）があれば、そちらの1行目 |
| OPEN | 開いているゲート（`gate:<ゲート名>`）と質問（`question:<出現ID>`） |

`done`でも、ワークフローが`end:<ラベル>`で終わったときは`outcome`がそのラベルになる（`needs_human`・`out_of_scope`・`stuck`等）。このとき終わらせたエージェントが書いた理由（`feedback`）が`reason`に入り、POSITIONは`outcome needs_human: <理由の1行目>`の形になる。`needs_human`なら理由はエージェントが人間に確かめたい疑問なので、答える形で指示書を直して`run`し直す。理由の全文は`workspace.json`の`reason`にある（下の[ホストの記録](#host-records)）。

### 活動（ACTIVITY）

| 活動 | 意味 | することは |
|---|---|---|
| `working` | Claude APIへのリクエストが進行中、または最近ツールを使った | 待つ |
| `waiting_input(...)` | Claude Code自身が人の入力を待っていると言っている。括弧の中は`idle`（ターンを終えて入力待ち）、`permission`（許可待ち）、`question`（選択肢の画面） | `masuda chat <id>`で画面を見る（[トラブルシューティング](troubleshooting.md#stalled)） |
| `waiting_gate` | ゲートの判断待ち | `masuda gate show`して判断する |
| `waiting_question` | 質問への回答待ち | `masuda question list`して答える |
| `stalled` | 生きているが、しきい値（既定10分）を超えて何も起きていない | 画面を見る。masudaは自動では止めない |
| `dead` | VMの中のClaude Code（tmuxのセッション）が無くなった | `stop`して`resume`する |
| `idle` | 何もすることが無い（`done`・`stopped`・`blocked`） | — |

活動は、ホストが見ているClaude APIへの通信（VMの中から偽れない）を一番に信じ、VMの中のClaude Codeのフックの知らせを補助に使って決める。

## 流れを見る {#watch}

```sh
masuda watch            # 全ワークスペース
masuda watch <id>       # 1つだけ
masuda watch --after 1520   # 番号1520より後のイベントから再送
```

各行は`<番号> <時刻> <ワークスペースID>`で始まり、続きは種類ごとに次の形。

| 種類 | 形 | 意味 |
|---|---|---|
| 状態 | `status <状態> <活動> [位置] [reason=...] [outcome=...]` | 状態か活動が変わった |
| ワークフロー | `engine <種類> occ=<出現ID> <ワークフロー>/<ノード> [outcome=...] [詳細]` | `enter`（ノードに入った）、`finish`（終わった）、`gate-open`、`decision`、`question-open`、`answer`、`concern`、`triage`、`invalid`（出力が検証で落ちた）、`blocked`、`end`等 |
| フック | `hook <フック名> <詳細>` | VMの中のClaude Codeのフック |
| 通信 | `http <メソッド> <ホスト><パス> <ステータス> <時間>ms`、進行中は末尾が`...` | VMからの通信 |
| 拒否 | `http denied <ホスト>` | 許可していないホストへの通信を断った |
| serve全体 | `notice <種類> <詳細>`（ワークスペースIDが空） | ディスク使用量の警告（`disk-warning`）等 |

`--after`無しで始めると、まず対象のワークスペースの今の`status`が1行ずつ出る。再送できるのは`masuda serve`のメモリにある直近10000件で、`masuda serve`を再起動するとそれより前は再送できない（実行の記録そのものは`records/execution-log.jsonl`に残る）。直近10000件より前の番号を`--after`に渡すとエラーになるので、`--after`無しで始め直す。

## 中を覗く（chat）

```sh
masuda chat <id>
```

VMの中のtmuxのセッション`claude-work`（メインのClaude Code）に、sshでアタッチする。

- `C-b d`で切り離す。切り離してもセッションは動き続ける
- runが終わる（publish・discard）までに切り離す。アタッチしたままだとVMの破棄が終わらず、runが完了しない（[トラブルシューティング](troubleshooting.md#chat-blocks-destroy)）
- 打ち込めば、Claude Codeに直接話しかけられる。ただしゲートや質問はchatからは閉じられない。判断は`masuda gate`・`masuda question`で行う
- 使えるのは動いているワークスペースと、ワークフローが進めなくなって`blocked`になったもの（VMが残っている）だけ。起動中・`stopped`・`done`には使えない。`done`ではVMが壊れているので、会話は`exports/transcripts/`で読む（[exports](#exports)）
- 接続の鍵は`chat`のたびに作り直され、`stop`で消える

## 止める・再開する {#resume}

```sh
masuda stop <id>
masuda resume <id>
```

`stop`は会話ログと実行ログを`exports/`へ書き出してからVMを壊し、ワークスペースを`stopped`にする。stagingと記録は残る。`resume`した後に止めたり終わったりすると、`exports/`の同じファイルは新しいもので置き換わる。

`resume`できるのは、`stopped`のワークスペースと、VMの起動に失敗して`blocked`になったワークスペース（理由が`sandbox boot failed: `で始まるもの）。ワークフローが進めなくなって`blocked`になったものは再開できない。

再開すると次のように進む。

1. 定義（ワークフロー・エージェント・スキーマ・`settings.json`・レビュー観点）は、始めたときに写したものを使う。作業ツリーの`.masuda/`をその後に変えていても効かない
2. 承認・秘密の値・`stallAfter`は、作業ツリーの`settings.local.json`と秘密ストアから読み直す。取り消した承認、入れ替えた値はここで効く。足りないものがあれば再開を断る
3. 再開前に開いていた、エージェントからの質問（`question`ノードでエージェントが聞いていたもの）は「再開で破棄」として閉じる。再開後に、新しいエージェントが改めて聞く。ワークフローに書いた固定の質問は閉じない
4. 新しいVMを作り、stagingからcloneし直す
5. 最後のスナップショット（WIP）の作業ツリーを、**コミットしていない変更として**戻す。ブランチ（HEAD）はコミット済みの位置のまま
6. 止まっていた位置から進める。ゲートや質問の待ちならその状態に、エージェントのタスクの途中ならそのタスクを最初からやり直す

VMの中にだけあったもの（gitignoreされたキャッシュや生成物、エージェントの会話の続き）は戻らない。

### `masuda serve`を再起動したとき {#serve-restart}

`masuda serve`を止めると、動いていたワークスペースはVMごと止まり、次に起動したとき`stopped`になっている。自動では再開しない（止まっていた間にリポジトリや定義が変わっているかもしれないので、続けるかはあなたが決める）。続けるなら`masuda resume <id>`。

## 片付ける

```sh
masuda remove <id>            # 止まっている・終わったもの
masuda remove <id> --force    # 動いているものを止めてから消す
```

`~/.local/share/masuda/workspaces/<id>/`のうち`exports/`だけを残して消す。消す前に実行ログを`exports/`へ写す（VMが残っていれば会話ログも）。exportsも要らなければ、そのディレクトリを手で消す。

masudaはディスクを自動では消さない。ワークスペース置き場の使用量が[`config.json`](settings.md#serve-config)の`diskWarnBytes`（既定20GiB）を超えたとき、`masuda serve`の標準エラーと`masuda watch`に1回だけ警告を出す。VMのイメージは`masuda-sandbox`側にあり、`masuda-sandbox images prune --dry-run`で消せるものを確かめてから`masuda-sandbox images prune`で消せる。

## 結果を読む（exports） {#exports}

ワークフローが終わったとき（`done`、終わり方を問わない）、masudaは`~/.local/share/masuda/workspaces/<id>/exports/`へ次を書き出してからVMを壊す。`stop`と`remove`のときも書き出す（下の注意）。

| パス | 中身 |
|---|---|
| `exports/<データ名>` | ワークフローのpublish・discardのノードが書き出すと決めたデータ。`develop`は`report`（レビューのレポート、Markdown）、`review`は`report`と`findings`（指摘の一覧、JSON） |
| `exports/execution-log.jsonl` | 実行ログ。1行1イベント |
| `exports/transcripts/<project>/…/*.jsonl` | VMの中のClaude Codeの会話ログ（メインとサブエージェント）。VMの`~/.claude/projects/`からの相対パスのまま |

実行ログの各行は`time`・`kind`・`run`・`occurrence`・`workflow`・`node`・`outcome`・`detail`を持つ。どのノードに何回入り、どう終わったかを追える。

```sh
jq -c 'select(.kind=="finish") | {time, workflow, node, outcome}' exports/execution-log.jsonl
jq -r 'select(.kind=="invalid" or .kind=="blocked") | .detail' exports/execution-log.jsonl
```

会話ログが読めなかったファイルは、実行ログに`kind: export-warning`として残る（publish・discardは止めない）。

- `end`・`end:<ラベル>`で終わったとき（`needs_human`・`out_of_scope`・`stuck`等）は、`<データ名>`は無く、実行ログと会話ログだけが残る
- `stop`（`remove --force`も）は、VMを壊す前に実行ログと会話ログを書き出す。`remove`は消す前に実行ログを書き出す（VMが既に無ければ会話ログは取れない）
- `masuda serve`の再起動では書き出さない。残っていたVMは会話ログを写さずに壊す
- `blocked`のときはVMを残すので、書き出されるのは実行ログだけ。会話ログは`stop`したときに写る
- 終わる前に中間の結果を見たいときは、ホストの記録を直接読む（下記）

## ホストの記録 {#host-records}

ワークスペース1つは`~/.local/share/masuda/workspaces/<id>/`にまとまる。調べものに使う主なもの:

| パス | 中身 |
|---|---|
| `workspace.json` | 対象リポジトリ・ブランチ・分岐元・ワークフロー・状態・結果（`outcome`）・理由（`reason`）・今の位置 |
| `staging.git/` | staging。`git -C staging.git log --oneline <ブランチ>`でコミットを見られる |
| `data/<出現ID>/<データ名>` | 各ノードの出力（検証済み）。計画は`plan`、レポートは`report` |
| `records/execution-log.jsonl` | 実行ログ（exportsのものと同じ） |
| `records/gates/`・`records/questions/` | 開いたゲート・質問と、その判断・回答 |
| `records/hooks.jsonl` | VMの中のClaude Codeのフックの記録 |
| `records/image-build.log` | イメージのビルドログ |
| `records/definitions/`・`records/reviews/` | 始めたときに写した`.masuda/`と、固定したレビュー観点 |
| `records/privileged/<run-id>/` | 特権コマンドの記録 |
