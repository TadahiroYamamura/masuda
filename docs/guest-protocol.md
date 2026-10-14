# ゲストプロトコル

VM内のエージェントとmasudaの間の取り決め。masudaが所有する契約の一部（[design/contracts.md](design/contracts.md)）。

## 到達経路

- ゲストからは`http://masuda.internal:7000/`。sandbox serviceの`tcp.hosts`で、そのワークスペース専用のホスト側ローカルポートへ対応付ける。別のVMからは届かない
- `/mcp`がMCP（Streamable HTTP）、`/hooks`がClaude Codeフックの受け口
- MCPの`timeout`は7日。`ask_human`は答えが来るまで、`run_privileged_command`は宣言の`timeoutSeconds`（既定3600秒）まで待つため。`next_task`は最大90秒で返る（下記）

## 起動時にホストがゲストへ置くもの

| ゲストのパス | 中身 |
|---|---|
| `/workspace` | stagingからのclone（bundleを置いてゲストで`git clone`） |
| `~/.claude/CLAUDE.md` | ループ規約（下記）。対象リポジトリの`.masuda/claude/`（`.masuda/claude.local/`を重ねたもの）に`CLAUDE.md`があれば、見出し`# プロジェクトのルール（.masuda/claude）`とループ規約が優先する旨の1行を挟んで後ろに連結する |
| `~/.claude/rules/*.md` | `.masuda/claude/rules/*.md`（同じパスは`.masuda/claude.local/`が勝つ）。無ければ置かない |
| `~/.claude/skills/<name>/` | `.masuda/claude/skills/<name>/`以下をディレクトリごと（同じパスは`.masuda/claude.local/`が勝つ）。無ければ置かない |
| `~/.claude/agents/*.md` | その実行で使うサブエージェント定義。エージェント定義（engine）から`name`・`description`・`tools`・本文と、あれば`model`・`effort`を写す。`model`・`effort`は`.masuda/settings.json`の`agents`に上書きがあればその値 |
| `~/.claude/settings.json` | フック設定（下記）と、対象リポジトリの`claudeSettings` |
| `/workspace/.env`等 | `envFiles`宣言から生成（秘密はプレースホルダ） |
| `/masuda/prepare`・`/masuda/prepare.log` | `.masuda/settings.json`の`prepare`（準備のシェルの1行）を実行可能スクリプトとして置き、起動時（再開ならWIPを戻した後）に`/workspace`で一度流した出力を`prepare.log`に置く。起動時の失敗では起動を止めない。`/masuda/checks/*`は先頭でこれを呼ぶ。エージェントも定義を変えた後に呼び直してよい。特権VMでは、宣言のコマンドの前に`HOME=/home/ubuntu`で流れる（失敗はコマンドの失敗）。宣言が無ければ置かない |
| `/masuda/reviews/<集合>/*.md` | レビュー観点の集合。ホストの実リポジトリの`.masuda/reviews/<集合>/`をタスク開始時にスナップショットしたもの。`implement`の集合だけは同梱の観点に重ねる（リポジトリに無ければ同梱の観点だけ）。同梱のreviewer・review-checkerと`Runner.Items(perspectives)`は**`/masuda/reviews/implement/`**を読む。ほかの集合は、それを読むよう書いた利用者の役が使う。`/masuda/reviews/`の直下にファイルは置かない。ゲストのcloneの`.masuda/reviews/`は読まない（`.masuda/`をコミットしていないリポジトリでも観点が揃うように） |
| `/masuda/pitfalls.jsonl` | プロジェクト固有の落とし穴。ホストの`.masuda/pitfalls.jsonl`を実行開始時に写して検査し、空行と`#`の行を除いたもの（1行1件`{id, category, trigger, question, background}`）。無ければ置かない。同梱のplan-questionsが読む |
| `/masuda/privileged-commands.json` | 特権コマンドの宣言。実行開始時に定義の写し（`records/definitions/`）の`privilegedCommands`から作る。名前→`{description?, command, image, inputs, outputs, timeoutSeconds}`（`timeoutSeconds`は省略時の既定3600を埋めた値）。秘密を使う宣言（`secrets`を持つもの）はエージェントから呼べないので載せない。載せる宣言が無ければ置かない。承認の状態は含まない。同梱のimplementerが読む。ホストはこのファイルを読み戻さない（実行の判断は名前と、定義の写しの宣言と、作業ツリーの`settings.local.json`の承認で行う） |
| `/masuda/privileged/<run-id>/` | 特権コマンドの結果の写し（`exit-code`・`log`・`outputs/`）。`run_privileged_command`か`privileged`ノードで動かすたびに置く。ただし秘密を使う宣言の結果は置かない（記録はホストにだけ残る）。`<run-id>`は`0001`からの連番 |
| `~/.claude/.mcp.json`相当 | `masuda`サーバー1つ（`http://masuda.internal:7000/mcp`） |

環境変数（tmuxサーバーに継承させる）: `CLAUDE_CODE_OAUTH_TOKEN=<プレースホルダ>`、`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`、`GIT_AUTHOR_*`/`GIT_COMMITTER_*`。

メインセッションは`tmux new-session -d -s claude-work 'claude --dangerously-skip-permissions -- <開始プロンプト>'`で起動する。`ANTHROPIC_BASE_URL`は設定しない（通信はMITMで素通しし、トークンだけ置換される）。

## ループ規約（`~/.claude/CLAUDE.md`の要旨）

1. `next_task`を呼ぶ。返ってきたタスクファイルのパスを読む
2. タスクに`continues`があり、その`agent_id`のサブエージェントがまだいれば、そのサブエージェントに`SendMessage`でタスクファイルのパスだけを送る。いなければ（VMの再開後など）、タスクが指す役（`role`）のサブエージェントを新しく起動し、タスクファイルのパスだけを渡して委譲する。自分では調査・実装・レビューをしない
3. サブエージェントが`write_output`と`report_result`を呼び終えるのを待つ。呼んでいなければ、呼ぶよう差し戻す
4. `next_task`に直前のサブエージェントの`agent_id`を渡して1へ戻る。`next_task`が`done`または`blocked`を返したら終了する
5. `next_task`が`waiting`を返したら、何も呼ばず待たずにターンを終える。masudaに起こされたら（下記「masudaがメインセッションを起こす」）、同じ`agent_id`で`next_task`を呼んで続ける
6. `next_task`が失敗した（接続の失敗等）ときは、間を置いて数回呼び直す。それでも失敗するならターンを終える。自分の判断でループをやめない

続きは「記憶はあれば使う。無くても成立する入力を常に渡す」。続けられたかどうかでタスクの入力は変わらないので、送る相手がいない・送れないときに新しく起動しても実行は成り立つ。

メインセッションがゲートや質問を自分で解決する経路は無い。

## MCPツール

| ツール | 引数 | 戻り | 備考 |
|---|---|---|---|
| `next_task` | `agent_id?` | `{kind: "task", occurrence, role, task_path, continues?}` / `{kind: "done", outcome}` / `{kind: "blocked", reason}` / `{kind: "waiting", reason, position}` | engine.Advanceを1回進める。次のタスクがまだ無い（ゲート・質問・triageの待ち、ホスト側のノードの実行中）ときは最大90秒待ち、その間に用意できなければ`waiting`を返す。`reason`は`gate` / `question` / `triage` / `running`、`position`は人間向けの位置（`gate plan (occ 0002)`等）。engineのエラーはエラーで返す。`agent_id`と`continues`は下記 |
| `write_output` | `occurrence`, `name`, `content` | `{accepted: true}` / `{accepted: false, problems: [...]}` | ホストが`/masuda/out/<occ>/<name>`へ置いた内容をスキーマ検証して受け付ける。受け付けない場合は理由を返す |
| `report_result` | `occurrence`, `outcome`, `feedback?`, `agent_id?` | `{accepted: true}` / `{accepted: false, reason}` | engineに記録した時点で返し、その後のホスト側のノード（exec・commit・publish等）の実行は待たない。宣言外のoutcome、`done`なのに未出力のoutputがあれば拒否。`done`以外の終わり方では、書かれた出力だけを検証して受け付ける（不正なら拒否）。`agent_id`はサブエージェント自身が自分のIDを知らないので通常は空。続きの宛先との結び付けは`next_task`の`agent_id`で行う |
| `report_concern` | `occurrence`, `text` | `{recorded: true}` | triageゲートを開く。以後、人間が判断するまで`next_task`は`{kind: "waiting", reason: "triage"}`を返す |
| `ask_human` | `occurrence`, `questions: [{id, text, options?}]` | `{answers: {id: answer}}` | `question`ノードのエージェントだけが使う。答えが来るまでブロック |
| `run_privileged_command` | `name` | `{exit_code, signal?, log, truncated, results_dir, outputs, outputs_error?, timed_out}` | 宣言済み・承認済みの名前のみ。秘密を使う宣言の名前は断る（ワークフローの`privileged`ノードからだけ動く）。`results_dir`は`/masuda/privileged/<run-id>/`。`outputs_error`は宣言した`outputs`のうち回収できなかったもの（当たらなかったパターン、読めなかったファイル）の説明で、コマンドの終了コードとは独立。回収できた分は`outputs`に返る |

ツールは`occurrence`で今のタスクを指す。現在待っている出現と一致しない呼び出しは拒否する。

`next_task`の`agent_id`と`continues`:

- 引数`agent_id`（任意）: 直前に完了したタスクを担当したサブエージェントのID。メインセッションが`Agent`ツールの結果から得る。続きのタスクで同じサブエージェントを続けたときも、そのサブエージェントのIDを渡す。masudaはそのメインセッションに最後に渡したタスクの出現にIDを結び付けて記録する。VMを作り直す再開では記録を消す（前のVMのIDは通じないため）
- 戻り`continues`（任意）: `{occurrence, agent_id?}`。このタスクを続けて渡す宛先。`occurrence`はengineが決めた出現（ワークフローの`continues: agents/<役>`）、`agent_id`はその出現について`next_task`で報告されたID（報告が無ければ省く）

## masudaがメインセッションを起こす

`next_task`が`waiting`を返した後、次のどれかが起きると、masudaはメインセッションのtmux（`claude-work`）の入力欄に決まった1文を送って起こす。

- ゲートの判断・質問の回答・エージェントの報告が記録された
- ホスト側のノードが終わり、次のタスクか終わり（`done`・`blocked`）に進んだ
- `waiting`を返すのとほぼ同時に上のどちらかが起き、`next_task`がそれを見ずに`waiting`を返した

送る文は「masudaからの知らせ: 待っていたものが進みました。next_taskを呼んで続けてください。」。sandboxのExecで`tmux send-keys -t claude-work -l <文>`を打ち、続けて`Enter`を押す。メインセッションが作業中でも、Claude Codeは入力を溜めて今の作業の後に処理する。

- `tmux list-clients -t claude-work`にクライアントがいる（`masuda chat`でアタッチ中）間は送らない。人間が打っている入力に混ざるため。活動の`detail`に「起こす待ち（chat中）」と出し、見回り（長くても30秒ごと）でデタッチの後に送る
- 送ってから60秒たっても`next_task`が来なければ送り直す。3回送っても来なければやめる
- `next_task`が来れば、起こす必要も回数も忘れる
- `resume`では起こさない。新しく起動したメインセッションが開始の文で自分から`next_task`を呼ぶ

## タスクファイル

`next_task`が返す`task_path`は`/masuda/in/<occ>/task.md`。構成は、役割と実行位置、続き（宛先の出現ID。`continues`があるときだけ）、エージェント定義の本文、入力データのパス（`/masuda/in/<occ>/<name>`）、前のノード・ゲートからの申し送り（失敗・却下の差し戻しの理由や、承認に付けたコメント）、書くべき出力の名前、終わり方（宣言したoutcomeと説明）、`report_concern`の案内。大きなものはすべてパスで渡し、埋め込まない。

## フック（`/hooks`）

`~/.claude/settings.json`に次のフックを入れる。いずれも`curl -s -X POST http://masuda.internal:7000/hooks -d @-`でstdinのJSONをそのまま送る。

| hook | 用途 |
|---|---|
| `Notification` | 許可待ち・idle・質問。`input_wait`の判定 |
| `PostToolUse` | 活動のハートビート。ツール名だけ記録する |
| `Stop` / `SubagentStop` | ターンの終了 |
| `SessionEnd` | セッション終了（`dead`判定の補助） |
| `PermissionRequest` | 確認が必要な操作（bypass modeでも出る、重要なパスを狙う`rm`等）。送った上で、`hookSpecificOutput.decision`を`deny`にし、無人実行であることと代わりの手段を`message`で返す。答える人がいないため待たない。許可はしない |

フックは補助情報で、masudaは「APIリクエストの観測」（sandbox serviceの`WatchEvents`）を一次情報として扱う。

## 旧設計からの差分

mcp-relay、`masuda.mcp_relay=`カーネル引数、`/masuda-state`共有、APIゲートウェイのプレースホルダ（`masuda-sandbox-placeholder-token`）、`resolve_gate_from_chat`はいずれも無い。
