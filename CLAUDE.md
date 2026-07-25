# masuda

AIとの協同開発（調査→プラン作成→git worktree作成→プロジェクト初期化→実装→レビュー）を、ローカルPC上のサンドボックスで自己ループ実行するためのツール群。

## 作業を始める前に読むもの

- `docs/design/sandbox-workflow.md`: 全体アーキテクチャ（6フェーズ+2ゲート、メインエージェント/サブエージェントの役割分担、レビューフェーズの内部設計、リポジトリの目標構造等）
- `docs/adr/0001`〜`0011`: 個々の設計判断とその理由・却下した代替案。番号順に読むと議論の経緯が追える
- `docs/implementation-roadmap.md`: 上記を踏まえた実装の大まかな順序

これらは実装より前に固めた設計であり、現在のリポジトリの中身（後述）はまだこの設計に追随していない。

## 現状（実装ロードマップ1〜2完了、3はフェーズ1-2+G1のみ完了）

- `runtime/CLAUDE.md`: 旧ルートCLAUDE.md（作業ループ仕様）の原文を複製済み。ADR-0006の`GATE:<name>`終了条件の追加など、内容自体の実装上の変更はまだ未着手
- `runtime/entrypoint.sh`・`runtime/start_claude.sh`: ルート直下から移動済み
- `orchestrator/langgraph_orchestrator.py`: ルート直下から移動済み。中身は現状もFizzBuzzのトイPoCのまま（このファイル自体はフェーズ3以降＝Dockerサンドボックス側の再設計対象。まだ未着手）
- `orchestrator/investigate_plan_graph.py`: フェーズ0-2（worktree作成→調査→プラン作成→G1）の状態遷移ロジック。ADR-0012によりDockerサンドボックスなしでホスト上で動く。`orchestrator/tests/`にLayer 1テスト（pytest、ファイルシステム状態を模擬、LLM呼び出しなし）あり
- `orchestrator/perspectives/`: `feat/github-actions-langgraph-nodes`ブランチの13観点（`config.py`）を置き場所だけ移植済み。まだどこからも参照されておらず、review/checkの往復ロジックへの組み込みはロードマップ4番
- `cmd/masuda/`: cobraベースのGo CLI
  - `masuda worktree create|merge|remove`・`masuda sandbox start|stop`（ロードマップ2番、実機確認済み）
  - `masuda plan start <branch> [task]`: worktree作成＋フェーズ1-2のホスト側自己ループ起動（`internal/hostloop`。`claude --append-system-prompt-file`でループ仕様を注入）。メインセッションは`Bash,Task,Read,Edit(成果物3ファイルのみ)`だけを持ち、実際にリポジトリ内容を読み回る調査・プラン作成は`--agents`で定義したBashなしのカスタムエージェント（`investigator`/`planner`、`Read,Grep,Glob,Edit`のみ）にTask委譲する。Bashは`--allowedTools`/`--disallowedTools`では確実に絞り込めない（`ls`は許可リストになくても素通りする一方`rm -rf`は止まるなど、Claude Code側の独自リスク判定が優先されるため）ことを実験で確認した上での設計。`--print`（非対話、従量課金でADR-0001に反する）は不採用。対象リポジトリの`.claude/settings.json`のdeny設定は上書きしない。いずれも実機で確認済み。G1到達でセッションは終了する（`masuda plan chat`はロードマップ5番のGATE:<name>実装まで未対応）
  - `masuda plan|review show|chat|approve|reject`: `masuda plan start`→調査→プラン作成→G1到達→`plan approve`→`masuda plan start`での再開→`g1_approved`終了、まで実機（本物のサブエージェント委譲込み）で一通り確認済み
- `Dockerfile`: masuda自身の制御ファイル（`venv`・`orchestrator`・`runtime`）は`/opt/masuda`に配置し、`/workspace`は対象worktree専用のbind mount先として空けてある。フェーズ3（プロジェクト初期化）以降でのみ使う
- `webhook_server.py`: 設計ドキュメントの目標構造に存在しないため削除済み

未着手: フェーズ3（プロジェクト初期化・Docker起動）以降、レビュー内部の再設計（ロードマップ4番）、`GATE:<name>`終了条件（ロードマップ5番）、`masuda review <branch-or-ref>`単体エントリーポイント（ロードマップ6番）。

## 開発環境

- Python: `venv/`に依存関係インストール済み（`requirements.txt`）
- テスト: `pytest`
