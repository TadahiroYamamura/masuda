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
| `~/.claude/.mcp.json`相当 | `masuda`サーバー1つ（`http://masuda.internal:7000/mcp`） |

環境変数（tmuxサーバーに継承させる）: `CLAUDE_CODE_OAUTH_TOKEN=<プレースホルダ>`、`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`、`GIT_AUTHOR_*`/`GIT_COMMITTER_*`。

メインセッションは`tmux new-session -d -s claude-work 'claude --dangerously-skip-permissions -- <開始プロンプト>'`で起動する。`ANTHROPIC_BASE_URL`は設定しない（通信はMITMで素通しし、トークンだけ置換される）。

## ループ規約（`~/.claude/CLAUDE.md`の要旨）

1. `next_task`を呼ぶ。返ってきたタスクファイルのパスを読む
2. タスクが指すサブエージェント（`role`）に、タスクファイルのパスだけを渡して委譲する。自分では調査・実装・レビューをしない
3. サブエージェントが`write_output`と`report_result`を呼び終えるのを待つ。呼んでいなければ、呼ぶよう差し戻す
4. 1へ戻る。`next_task`が`done`または`blocked`を返したら終了する

メインセッションがゲートや質問を自分で解決する経路は無い。

## MCPツール

| ツール | 引数 | 戻り | 備考 |
|---|---|---|---|
| `next_task` | — | `{kind: "task", occurrence, role, task_path}` / `{kind: "done", outcome}` / `{kind: "blocked", reason}` | engine.Advanceを1回進める。待ちの間はブロック |
| `write_output` | `occurrence`, `name`, `content` | `{accepted: true}` / `{accepted: false, problems: [...]}` | ホストが`/masuda/out/<occ>/<name>`へ置いた内容をスキーマ検証して受け付ける。受け付けない場合は理由を返す |
| `report_result` | `occurrence`, `outcome`, `feedback?`, `agent_id?` | `{accepted: true}` / `{accepted: false, reason}` | 宣言外のoutcome、未出力のoutputがあれば拒否 |
| `report_concern` | `occurrence`, `text` | `{recorded: true}` | triageゲートを開く。以後`next_task`は人間の判断までブロック |
| `ask_human` | `occurrence`, `questions: [{id, text, options?}]` | `{answers: {id: answer}}` | `question`ノードのエージェントだけが使う。答えが来るまでブロック |
| `run_privileged_command` | `name` | `{exit_code, log, truncated, results_dir, outputs, timed_out}` | 宣言済み・承認済みの名前のみ。`results_dir`は`/masuda/privileged/<run-id>/` |

ツールは`occurrence`で今のタスクを指す。現在待っている出現と一致しない呼び出しは拒否する。

## タスクファイル

`next_task`が返す`task_path`は`/masuda/in/<occ>/task.md`。構成は、役割と実行位置、エージェント定義の本文、入力データのパス（`/masuda/in/<occ>/<name>`）、前のノードからの差し戻し、書くべき出力の名前、終わり方（宣言したoutcomeと説明）、`report_concern`の案内。大きなものはすべてパスで渡し、埋め込まない。

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
