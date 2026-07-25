# masuda

AIとの協同開発（調査→プラン作成→git worktree作成→プロジェクト初期化→実装→レビュー）を、ローカルPC上のサンドボックスで自己ループ実行するためのツール群。

## 作業を始める前に読むもの

- `docs/design/sandbox-workflow.md`: 全体アーキテクチャ（6フェーズ+2ゲート、メインエージェント/サブエージェントの役割分担、レビューフェーズの内部設計、リポジトリの目標構造等）
- `docs/adr/0001`〜`0011`: 個々の設計判断とその理由・却下した代替案。番号順に読むと議論の経緯が追える
- `docs/implementation-roadmap.md`: 上記を踏まえた実装の大まかな順序

これらは実装より前に固めた設計であり、現在のリポジトリの中身（後述）はまだこの設計に追随していない。

## 現状（未移行）

このファイル（`CLAUDE.md`）は元々「自己ループの作業ループ仕様」（`TASK.md`の読み書きルール等）を記述していた。この内容はADR-0007の決定により`runtime/CLAUDE.md`へ移設し、サンドボックスコンテナ内では`~/.claude/CLAUDE.md`に配置する設計に変わっている。旧内容はそのまま`runtime/CLAUDE.md`に複製済み（原文のまま、加筆なし）。ADR-0006で決めた`GATE:<name>`終了条件の追加など、`runtime/CLAUDE.md`自体への実装上の変更はまだ行われていない。

同様に`langgraph_orchestrator.py`（現状はFizzBuzzのトイPoC）、`perspectives/`はいずれも設計ドキュメントに沿って作り直す対象。`feat/github-actions-langgraph-nodes`ブランチのレビューグラフ（review/checkの往復、`perspectives/config.py`の13観点）はロジックとして再利用する資産。

## 開発環境

- Python: `venv/`に依存関係インストール済み（`requirements.txt`）
- テスト: `pytest`
