# masuda

AIとの協同開発（調査→プラン作成→git worktree作成→プロジェクト初期化→実装→レビュー）を、ローカルPC上のサンドボックスで自己ループ実行するためのツール群。

## 作業を始める前に読むもの

- `docs/design/sandbox-workflow.md`: 全体アーキテクチャ（6フェーズ+2ゲート、メインエージェント/サブエージェントの役割分担、レビューフェーズの内部設計、リポジトリの目標構造等）
- `docs/adr/0001`〜`0011`: 個々の設計判断とその理由・却下した代替案。番号順に読むと議論の経緯が追える
- `docs/implementation-roadmap.md`: 上記を踏まえた実装の大まかな順序

これらは実装より前に固めた設計であり、現在のリポジトリの中身（後述）はまだこの設計に追随していない。

## 現状（実装ロードマップ1〜2. リポジトリ構造の移行・Go CLIの骨格 完了、3以降は未着手）

`docs/implementation-roadmap.md`の2番（Go CLIの骨格）まで完了している。

- `runtime/CLAUDE.md`: 旧ルートCLAUDE.md（作業ループ仕様）の原文を複製済み。ADR-0006の`GATE:<name>`終了条件の追加など、内容自体の実装上の変更はまだ未着手
- `runtime/entrypoint.sh`・`runtime/start_claude.sh`: ルート直下から移動済み
- `orchestrator/langgraph_orchestrator.py`: ルート直下から移動済み。中身は現状もFizzBuzzのトイPoCのまま（設計ドキュメントに沿った作り直しはロードマップ3番）
- `orchestrator/perspectives/`: `feat/github-actions-langgraph-nodes`ブランチの13観点（`config.py`）を置き場所だけ移植済み。`langgraph_orchestrator.py`からはまだ参照されておらず、review/checkの往復ロジックへの組み込みはロードマップ3・4番
- `cmd/masuda/`: cobraベースのGo CLI。`masuda worktree create|merge|remove`・`masuda sandbox start|stop`・`masuda plan|review show|chat|approve|reject`を実装済み（`internal/worktree`・`internal/sandbox`・`internal/gate`）。実機（`docker build`/`docker run`含む）で一連の動作を確認済み
- `Dockerfile`: masuda自身の制御ファイル（`venv`・`orchestrator`・`runtime`）は`/opt/masuda`に配置し、`/workspace`は対象worktree専用のbind mount先として空けてある。`~/.claude/CLAUDE.md`への`runtime/CLAUDE.md`配置は`masuda sandbox start`が`docker create`→`docker cp`→`docker start`の順で行う
- このCLIはまだ6フェーズのLangGraph親グラフ（ロードマップ3番）には配線されておらず、`GATE:<name>`終了条件（ロードマップ5番）も未実装のため、`plan/review chat`で対話中にClaude自身が承認マーカーを書く経路はまだ動作しない
- `webhook_server.py`: 設計ドキュメントの目標構造に存在しないため削除済み

## 開発環境

- Python: `venv/`に依存関係インストール済み（`requirements.txt`）
- テスト: `pytest`
