# 実装ロードマップ

`docs/design/sandbox-workflow.md`と`docs/adr/`で決めた設計を実装するための大まかな順序。詳細な実装方法（言語内の具体的な構成、エラーハンドリングの粒度等）はコーディングエージェントの判断に委ねる。ここでは「何を」「どの順番で」作るかのみを示す。

## 1. リポジトリ構造の移行

design doc「リポジトリ構造」・ADR-0007参照。

- `runtime/CLAUDE.md`: 旧ルートCLAUDE.md（作業ループ仕様）の原文を複製済み。ADR-0006の`GATE:<name>`終了条件の追加など、内容自体の実装上の変更はまだ未着手
- `runtime/`配下に`entrypoint.sh`、`start_claude.sh`、MCP経由のLSP設定
- `orchestrator/`配下に`langgraph_orchestrator.py`、`perspectives/config.py`
- `cmd/masuda/`: Go製CLIの雛形

## 2. Go CLI（`masuda`コマンド）の骨格

design doc「リポジトリ構造」「ゲート（G1/G2）のUX」、ADR-0005・0006参照。

- worktree管理（作成・ローカルマージ・削除。pushは含めない）
- サンドボックス起動（`docker run`、worktreeのbind mount、`~/.claude/CLAUDE.md`へのruntime/CLAUDE.md配置）
- ゲート操作: `masuda plan/review show|chat|approve|reject`

## 3. LangGraphオーケストレーターを6フェーズの親グラフに再設計

design doc「全体構成」参照。

- フェーズ1-2（調査・プラン、ADR-0008: エージェント分離+調査不足時のredo）
- フェーズ5（実装、ADR-0009: ビルド/テスト自己修正ループ、ADR-0010: プラン逸脱検知とG1再オープン）
- フェーズ6は既存の`feat/github-actions-langgraph-nodes`のレビューグラフ（review/checkの往復、13観点）を子グラフとして組み込む

## 4. レビュー内部の再設計

design doc「フェーズ6（レビュー）の内部設計」、ADR-0003・0004・0011参照。

- 機械的チェック / 横断的チェックの2区分
- 機械的チェック: checker/fixerの役割分離、unresolved_idsのフォールバック
- 横断的チェック: LSP(gopls)をMCP経由でセットアップ、explorer→verifierの1パス構成
- 予算管理: `ITERATION_BUDGET`の単位を「サブエージェント起動1回」に統一、サブエージェントごとの内部ターン数上限を追加

## 5. ゲート機構

ADR-0006参照。

- ゲート到達時にコンテナ/tmuxセッションを終了させず待機させる仕組み（`GATE:<name>`終了条件をCLAUDE.mdのループルールに追加）
- `docker exec -it ... tmux attach`によるchatパス、対話内での承認マーカー書き込み

## 6. レビュー単体エントリーポイント

design doc「レビュー単体での再利用」参照。

- `masuda review <branch-or-ref>`（デフォルトは現在ブランチ vs develop）
- フェーズ3〜6の機構を、新規ブランチではなく既存refのworktreeに対して使い回す
