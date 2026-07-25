# masuda

AIとの協同開発（調査→プラン作成→git worktree作成→プロジェクト初期化→実装→レビュー）を、ローカルPC上のサンドボックスで自己ループ実行するためのツール群。

## 作業を始める前に読むもの

- `docs/design/sandbox-workflow.md`: 全体アーキテクチャ（6フェーズ+2ゲート、メインエージェント/サブエージェントの役割分担、レビューフェーズの内部設計、リポジトリの目標構造等）
- `docs/adr/0001`〜`0011`: 個々の設計判断とその理由・却下した代替案。番号順に読むと議論の経緯が追える
- `docs/implementation-roadmap.md`: 上記を踏まえた実装の大まかな順序

これらは実装より前に固めた設計であり、現在のリポジトリの中身（後述）はまだこの設計に追随していない。

## 現状（実装ロードマップ1. リポジトリ構造の移行 完了、2以降は未着手）

`docs/implementation-roadmap.md`の1番（リポジトリ構造の移行）まで完了している。

- `runtime/CLAUDE.md`: 旧ルートCLAUDE.md（作業ループ仕様）の原文を複製済み。ADR-0006の`GATE:<name>`終了条件の追加など、内容自体の実装上の変更はまだ未着手
- `runtime/entrypoint.sh`・`runtime/start_claude.sh`: ルート直下から移動済み
- `orchestrator/langgraph_orchestrator.py`: ルート直下から移動済み。中身は現状もFizzBuzzのトイPoCのまま（設計ドキュメントに沿った作り直しはロードマップ3番）
- `orchestrator/perspectives/`: `feat/github-actions-langgraph-nodes`ブランチの13観点（`config.py`）を置き場所だけ移植済み。`langgraph_orchestrator.py`からはまだ参照されておらず、review/checkの往復ロジックへの組み込みはロードマップ3・4番
- `cmd/masuda/`: Go CLIの雛形（`main.go`が未実装メッセージを出すだけ）。リポジトリルートに`go.mod`（`module github.com/TadahiroYamamura/masuda`）を配置。worktree管理・サンドボックス起動・ゲート操作の実装はロードマップ2番
- `Dockerfile`: 新しいパス構成に追従。ロードマップ2番でのGo CLI実装（`~/.claude/CLAUDE.md`へのruntime/CLAUDE.md配置）が終わるまでは、自己ループを起動しても`~/.claude/CLAUDE.md`が存在せず動作しない状態が続く
- `webhook_server.py`: 設計ドキュメントの目標構造に存在しないため削除済み

## 開発環境

- Python: `venv/`に依存関係インストール済み（`requirements.txt`）
- テスト: `pytest`
