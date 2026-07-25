# masuda

AIとの協同開発（調査→プラン作成→git worktree作成→プロジェクト初期化→実装→レビュー）を、ローカルPC上のサンドボックスで自己ループ実行するためのツール群。

## 作業を始める前に読むもの

- `docs/design/sandbox-workflow.md`: 全体アーキテクチャ（6フェーズ+2ゲート、メインエージェント/サブエージェントの役割分担、レビューフェーズの内部設計、リポジトリの目標構造等）
- `docs/adr/0001`〜`0011`: 個々の設計判断とその理由・却下した代替案。番号順に読むと議論の経緯が追える
- `docs/implementation-roadmap.md`: 上記を踏まえた実装の大まかな順序

これらは実装より前に固めた設計であり、現在のリポジトリの中身（後述）はまだこの設計に追随していない。

## 現状（実装ロードマップ1〜2完了、3はフェーズ1-2+G1・フェーズ4完了）

- `runtime/CLAUDE.md`: 旧ルートCLAUDE.md（作業ループ仕様）の原文を複製済み。ADR-0006の`GATE:<name>`終了条件の追加など、内容自体の実装上の変更はまだ未着手
- `runtime/entrypoint.sh`・`runtime/start_claude.sh`: ルート直下から移動済み
- `runtime/claude-settings.json`: `~/.claude/settings.json`にビルド時焼き込み。テーマ未設定だと新規コンテナの初回`claude`起動が対話式のテーマ選択ウィザードで止まることが実機で判明したための対策
- `orchestrator/investigate_plan_graph.py`: フェーズ0-2（worktree作成→調査→プラン作成→G1）の状態遷移ロジック。ADR-0012によりDockerサンドボックスなしでホスト上で動く
- `orchestrator/implement_graph.py`: フェーズ4（実装）の状態遷移ロジック。Dockerサンドボックス内で動く（旧`langgraph_orchestrator.py`のFizzBuzz PoCを置き換え、ファイル自体削除済み）。ADR-0009のビルド/テスト自己修正はサブエージェント内で完結させ、オーケストレーターは`implementation_result.json`の結果（done/needs_plan_review/build_test_failed）だけを見る。ADR-0010の機械的バックストップ（PLAN.mdの「変更するファイル一覧」と`git status --porcelain`の突き合わせ、LLM不使用）は実機で逸脱検知を確認済み。逸脱検知時は`DEVIATION.md`を書き出しG1のゲートマーカーを削除して再オープンする（`masuda plan show`が理由を表示、`masuda plan approve/reject`が消費する）
- `orchestrator/tests/`: 上記2つのLayer 1テスト（pytest、ファイルシステム状態を模擬、LLM呼び出しなし、計37件）
- `orchestrator/perspectives/`: `feat/github-actions-langgraph-nodes`ブランチの13観点（`config.py`）を置き場所だけ移植済み。まだどこからも参照されておらず、review/checkの往復ロジックへの組み込みはロードマップ4番
- `cmd/masuda/`: cobraベースのGo CLI
  - `masuda worktree create|merge|remove`: `git clone --local`によるローカルクローン方式（`git worktree add`ではない）。理由: linked worktreeは対象worktree自身の絶対パスとメインリポジトリの`.git/worktrees/<name>`が双方向に絶対パス参照し合う構造で、`/workspace`のような別パスへのbind mountに耐えられないことが実機で判明したため。`merge`はクローン側のブランチをメインリポジトリへ`git fetch`してから`git merge`する
  - `masuda sandbox start|stop`: worktreeのbind mountに加え、ホストの`~/.claude/.credentials.json`・`~/.claude.json`を bind mount してサブスク認証を引き継ぐ（ADR-0001）。無ければ新規コンテナがログインウィザードで止まることを実機で確認した上での対応
  - `masuda plan start <branch> [task]`: worktree作成＋フェーズ1-2のホスト側自己ループ起動（`internal/hostloop`。`claude --append-system-prompt-file`でループ仕様を注入）。メインセッションは`Bash,Task,Read,Edit(成果物3ファイルのみ)`だけを持ち、実際にリポジトリ内容を読み回る調査・プラン作成は`--agents`で定義したBashなしのカスタムエージェント（`investigator`/`planner`、`Read,Grep,Glob,Edit`のみ）にTask委譲する。Bashは`--allowedTools`/`--disallowedTools`では確実に絞り込めない（`ls`は許可リストになくても素通りする一方`rm -rf`は止まるなど、Claude Code側の独自リスク判定が優先されるため）ことを実験で確認した上での設計。`--print`（非対話、従量課金でADR-0001に反する）は不採用。対象リポジトリの`.claude/settings.json`のdeny設定は上書きしない。いずれも実機で確認済み。G1到達でセッションは終了する（`masuda plan chat`はロードマップ5番のGATE:<name>実装まで未対応）
  - `masuda plan|review show|chat|approve|reject`: `masuda plan start`→調査→プラン作成→G1到達→`plan approve`→`masuda plan start`での再開→`g1_approved`終了、まで実機（本物のサブエージェント委譲込み）で一通り確認済み。`masuda sandbox start`→実装→`implementation_complete`終了、および機械的バックストップによるG1再オープンも実機確認済み
- `Dockerfile`: masuda自身の制御ファイル（`venv`・`orchestrator`・`runtime`）は`/opt/masuda`に配置し、`/workspace`は対象worktree専用のbind mount先として空けてある。`git`を追加済み（対象repoがgit操作を必要とするため）
- `webhook_server.py`: 設計ドキュメントの目標構造に存在しないため削除済み

未着手: フェーズ5（レビュー、13観点の子グラフ統合、ロードマップ4番相当）、`GATE:<name>`終了条件（ロードマップ5番）、`masuda review <branch-or-ref>`単体エントリーポイント（ロードマップ6番）。

## 開発環境

- Python: `venv/`に依存関係インストール済み（`requirements.txt`）
- テスト: `pytest`
