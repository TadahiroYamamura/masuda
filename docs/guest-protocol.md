# ゲストプロトコル

VM内のエージェントとmasudaの間の取り決め。masudaが所有する契約の一部（[design/contracts.md](design/contracts.md)）。

## 到達経路

- ゲストからは`http://masuda.internal:7000/`。sandbox serviceの`tcp.hosts`で、そのワークスペース専用のホスト側ローカルポートへ対応付ける。別のVMからは届かない
- `/mcp`がMCP（Streamable HTTP）、`/hooks`がClaude Codeフックの受け口
- MCPの`timeout`は7日。`next_task`はゲート待ち・質問待ちの間ブロックするため

## 起動時にホストがゲストへ置くもの

| ゲストのパス | 中身 |
|---|---|
| `/workspace` | stagingからのclone（bundleを置いてゲストで`git clone`） |
| `~/.claude/CLAUDE.md` | ループ規約（下記） |
| `~/.claude/agents/*.md` | その実行で使うサブエージェント定義。エージェント定義（engine）から`name`・`description`・`tools`・本文を写す |
| `~/.claude/settings.json` | フック設定（下記）と、対象リポジトリの`claudeSettings` |
| `/workspace/.env`等 | `envFiles`宣言から生成（秘密はプレースホルダ） |
| `/masuda/reviews/*.md` | レビュー観点。ホストの実リポジトリの`.masuda/reviews/`（無ければ同梱の14観点）をタスク開始時にスナップショットしたもの。同梱のreviewer・review-checkerと`Runner.Items(perspectives)`は**ここ**を読む。ゲストのcloneの`.masuda/reviews/`は読まない（`.masuda/`をコミットしていないリポジトリでも観点が揃うように） |
| `~/.claude/.mcp.json`相当 | `masuda`サーバー1つ（`http://masuda.internal:7000/mcp`） |

環境変数（tmuxサーバーに継承させる）: `CLAUDE_CODE_OAUTH_TOKEN=<プレースホルダ>`、`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`、`GIT_AUTHOR_*`/`GIT_COMMITTER_*`。

メインセッションは`tmux new-session -d -s claude-work 'claude --dangerously-skip-permissions -- <開始プロンプト>'`で起動する。`ANTHROPIC_BASE_URL`は設定しない（通信はMITMで素通しし、トークンだけ置換される）。

## ループ規約（`~/.claude/CLAUDE.md`の要旨）

1. `next_task`を呼ぶ。返ってきたタスクファイルのパスを読む
2. タスクに`continues`があり、その`agent_id`のサブエージェントがまだいれば、そのサブエージェントに`SendMessage`でタスクファイルのパスだけを送る。いなければ（VMの再開後など）、タスクが指す役（`role`）のサブエージェントを新しく起動し、タスクファイルのパスだけを渡して委譲する。自分では調査・実装・レビューをしない
3. サブエージェントが`write_output`と`report_result`を呼び終えるのを待つ。呼んでいなければ、呼ぶよう差し戻す
4. `next_task`に直前のサブエージェントの`agent_id`を渡して1へ戻る。`next_task`が`done`または`blocked`を返したら終了する

続きは「記憶はあれば使う。無くても成立する入力を常に渡す」。続けられたかどうかでタスクの入力は変わらないので、送る相手がいない・送れないときに新しく起動しても実行は成り立つ。

メインセッションがゲートや質問を自分で解決する経路は無い。

## MCPツール

| ツール | 引数 | 戻り | 備考 |
|---|---|---|---|
| `next_task` | `agent_id?` | `{kind: "task", occurrence, role, task_path, continues?}` / `{kind: "done", outcome}` / `{kind: "blocked", reason}` | engine.Advanceを1回進める。待ちの間はブロック。`agent_id`と`continues`は下記 |
| `write_output` | `occurrence`, `name`, `content` | `{accepted: true}` / `{accepted: false, problems: [...]}` | ホストが`/masuda/out/<occ>/<name>`へ置いた内容をスキーマ検証して受け付ける。受け付けない場合は理由を返す |
| `report_result` | `occurrence`, `outcome`, `feedback?`, `agent_id?` | `{accepted: true}` / `{accepted: false, reason}` | 宣言外のoutcome、未出力のoutputがあれば拒否。`agent_id`はサブエージェント自身が自分のIDを知らないので通常は空。続きの宛先との結び付けは`next_task`の`agent_id`で行う |
| `report_concern` | `occurrence`, `text` | `{recorded: true}` | triageゲートを開く。以後`next_task`は人間の判断までブロック |
| `ask_human` | `occurrence`, `questions: [{id, text, options?}]` | `{answers: {id: answer}}` | `question`ノードのエージェントだけが使う。答えが来るまでブロック |
| `run_privileged_command` | `name` | `{exit_code, signal?, log, truncated, results_dir, outputs, outputs_error?, timed_out}` | 宣言済み・承認済みの名前のみ。`results_dir`は`/masuda/privileged/<run-id>/`。`outputs_error`は宣言した`outputs`のうち回収できなかったもの（当たらなかったパターン、読めなかったファイル）の説明で、コマンドの終了コードとは独立。回収できた分は`outputs`に返る |

ツールは`occurrence`で今のタスクを指す。現在待っている出現と一致しない呼び出しは拒否する。

`next_task`の`agent_id`と`continues`:

- 引数`agent_id`（任意）: 直前に完了したタスクを担当したサブエージェントのID。メインセッションが`Agent`ツールの結果から得る。続きのタスクで同じサブエージェントを続けたときも、そのサブエージェントのIDを渡す。masudaはそのメインセッションに最後に渡したタスクの出現にIDを結び付けて記録する。VMを作り直す再開では記録を消す（前のVMのIDは通じないため）
- 戻り`continues`（任意）: `{occurrence, agent_id?}`。このタスクを続けて渡す宛先。`occurrence`はengineが決めた出現（ワークフローの`continues: agents/<役>`）、`agent_id`はその出現について`next_task`で報告されたID（報告が無ければ省く）

## タスクファイル

`next_task`が返す`task_path`は`/masuda/in/<occ>/task.md`。構成は、役割と実行位置、続き（宛先の出現ID。`continues`があるときだけ）、エージェント定義の本文、入力データのパス（`/masuda/in/<occ>/<name>`）、前のノードからの差し戻し、書くべき出力の名前、終わり方（宣言したoutcomeと説明）、`report_concern`の案内。大きなものはすべてパスで渡し、埋め込まない。

## フック（`/hooks`）

`~/.claude/settings.json`に次のフックを入れる。いずれも`curl -s -X POST http://masuda.internal:7000/hooks -d @-`でstdinのJSONをそのまま送る。

| hook | 用途 |
|---|---|
| `Notification` | 許可待ち・idle・質問。`input_wait`の判定 |
| `PostToolUse` | 活動のハートビート。ツール名だけ記録する |
| `Stop` / `SubagentStop` | ターンの終了 |
| `SessionEnd` | セッション終了（`dead`判定の補助） |

フックは補助情報で、masudaは「APIリクエストの観測」（sandbox serviceの`WatchEvents`）を一次情報として扱う。

## 旧設計からの差分

mcp-relay、`masuda.mcp_relay=`カーネル引数、`/masuda-state`共有、APIゲートウェイのプレースホルダ（`masuda-sandbox-placeholder-token`）、`resolve_gate_from_chat`はいずれも無い。
