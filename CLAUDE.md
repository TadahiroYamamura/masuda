# masuda

AIとの協同開発（調査→プラン作成→git worktree作成→プロジェクト初期化→実装→レビュー）を、ローカルPC上のサンドボックスで自己ループ実行するためのツール群。

## 作業を始める前に読むもの

- `docs/design/sandbox-workflow.md`: 全体アーキテクチャ（6フェーズ+2ゲート、メインエージェント/サブエージェントの役割分担、レビューフェーズの内部設計、リポジトリの目標構造等）
- `docs/adr/0001`〜`0011`: 個々の設計判断とその理由・却下した代替案。番号順に読むと議論の経緯が追える
- `docs/implementation-roadmap.md`: 上記を踏まえた実装の大まかな順序

これらは実装より前に固めた設計であり、現在のリポジトリの中身（後述）はまだこの設計に追随していない。

## 現状（実装ロードマップ1〜3・4（機械的チェックのみ）・5・6完了）

- `runtime/CLAUDE.md`: 作業ループ仕様。`GATE:<name>`終了条件（ADR-0006、ロードマップ5番）を実装済み——終了条件（`DONE`）とゲート条件（`GATE:<name>`）は排他で、ゲート条件を満たす場合はセッションを終了せず`.masuda-gate/<name>.json`のstatusがpendingでなくなるまで待機する。単発Bashの`while`ループは動的な文字列を含むコマンドとして確認を求められ無人ループで詰まることを実機で確認したため、Monitorのような監視系ツールを使うよう指示している
- `runtime/entrypoint.sh`・`runtime/start_claude.sh`: ルート直下から移動済み
- `runtime/claude-settings.json`: `~/.claude/settings.json`にビルド時焼き込み。テーマ未設定だと新規コンテナの初回`claude`起動が対話式のテーマ選択ウィザードで止まることが実機で判明したための対策
- `orchestrator/investigate_plan_graph.py`: フェーズ0-2（worktree作成→調査→プラン作成→G1）の状態遷移ロジック。ADR-0012によりDockerサンドボックスなしでホスト上で動く。G1到達（`await_g1`）はGATE:planとして待機する（旧: セッション終了）
- `orchestrator/implement_review_graph.py`: フェーズ4-5（実装・レビュー）の状態遷移ロジック。ADR-0013により1つのオーケストレーターにまとめている。Dockerサンドボックス内で動く
  - フェーズ4: ADR-0009のビルド/テスト自己修正はサブエージェント内で完結させ、オーケストレーターは`implementation_result.json`の結果（done/needs_plan_review/build_test_failed）だけを見る。ADR-0010の機械的バックストップ（PLAN.mdの「変更するファイル一覧」と`git status --porcelain`の突き合わせ、LLM不使用）は実機で逸脱検知を確認済み。逸脱検知時は`DEVIATION.md`を書き出しG1のゲートマーカーを削除して再オープン（GATE:planとして待機）する
  - **G1再オープンの承認/却下分岐**: 当初`plan_reopened`検知時に即座にゲートマーカー・implementation_result.jsonをクリアしていたが、これだと承認された逸脱も次回チェックで同一の逸脱として検知され無限に再オープンし続けるバグがあった（GATE導入でセッションが自動再開するようになって顕在化）。`.masuda-approved-deviations.json`に承認済み逸脱のファイル一覧を記録し、以後の機械的バックストップから除外する形に修正した。却下時・自己申告（needs_plan_review/build_test_failed）の承認時はフェーズ4を再実行する
  - フェーズ5: `feat/github-actions-langgraph-nodes`の13観点review/checkループ（`perspectives/config.py`）を、直接API呼び出しからサブエージェント委譲（Task tool、diffのみを見せる機械的チェック）に移植。review/checkの往復・redo・unresolved・synthesizeまで実機で確認済み。G2到達（`await_g2`）はGATE:reviewとして待機する。G2却下時はフィードバックを持ってフェーズ4に差し戻し、レビューはperspective 0からやり直す（ADR-0013、実機確認済み）
  - checker/fixer自動修正ループ（ADR-0004）: checkがhas_issues=trueの指摘を確認すると、指摘箇所のみのfixerサブエージェントが修正し、新規のcheckerで再検証する。解決すればfixed一覧へ、MAX_RETRIES到達で未解決としてsynthesizeに引き継ぐ。実際にAPIキーのハードコードを注入し、fixerが環境変数読み取りに修正、recheckが解決確認するところまで実機確認済み
  - `masuda plan show`はDEVIATION.mdがあれば表示、`masuda plan approve/reject`が消費する。`masuda review show`はfinal_report.md、`review approve`が既存通りマージ・後片付け、`review reject`がフェーズ4差し戻しをトリガーする
- `orchestrator/tests/`: 上記2つのLayer 1テスト（pytest、ファイルシステム状態を模擬、LLM呼び出しなし、計67件）
- `orchestrator/perspectives/`: `feat/github-actions-langgraph-nodes`ブランチの13観点（`config.py`）。`implement_review_graph.py`のフェーズ5から参照済み
- `cmd/masuda/`: cobraベースのGo CLI
  - `masuda worktree create|merge|remove`: `git clone --local`によるローカルクローン方式（`git worktree add`ではない）。理由: linked worktreeは対象worktree自身の絶対パスとメインリポジトリの`.git/worktrees/<name>`が双方向に絶対パス参照し合う構造で、`/workspace`のような別パスへのbind mountに耐えられないことが実機で判明したため。`merge`はクローン側のブランチをメインリポジトリへ`git fetch`してから`git merge`する
  - `masuda sandbox start|stop`: worktreeのbind mountに加え、ホストの`~/.claude/.credentials.json`・`~/.claude.json`を bind mount してサブスク認証を引き継ぐ（ADR-0001）。前回のコンテナが（tmuxセッション終了により）Exited状態で残っていると`docker create`が名前衝突で失敗するため、`start`は同名の既存コンテナを`docker rm -f`してから作り直す。既に起動中なら冪等に既存コンテナの情報を返す
  - `masuda plan start <branch> [task]`: worktree作成＋フェーズ1-2のホスト側自己ループ起動（`internal/hostloop`。`claude --append-system-prompt-file`でループ仕様を注入）。メインセッションは`Bash,Task,Read,Edit(成果物3ファイルのみ)`だけを持ち、実際にリポジトリ内容を読み回る調査・プラン作成は`--agents`で定義したBashなしのカスタムエージェント（`investigator`/`planner`、`Read,Grep,Glob,Edit`のみ）にTask委譲する
  - `masuda plan|review chat`: `internal/hostloop.AttachArgs`（ホスト、`tmux attach`）と`internal/sandbox.AttachArgs`（Docker、`docker exec -it ... tmux attach`）の両方を用意し、`plan chat`はG1がフェーズ1-2ホストループ・フェーズ4-5のDocker再オープンどちらで待機していても対応する。実機でfast-path（`plan/review approve`）・chatパス（対話でClaude自身がマーカーを書く）の両方をG1・G2で確認済み
- `Dockerfile`: masuda自身の制御ファイル（`venv`・`orchestrator`・`runtime`）は`/opt/masuda`に配置し、`/workspace`は対象worktree専用のbind mount先として空けてある。`git`を追加済み
- `webhook_server.py`: 設計ドキュメントの目標構造に存在しないため削除済み

- `masuda review start <branch-or-ref> [--base develop]`（ロードマップ6番、design docは`masuda review <branch-or-ref>`だが既存のサブコマンド構成に合わせて`start`を追加）: 既存の（新規作成ではない）ブランチに対し、フェーズ0（worktree作成）〜フェーズ5（レビュー）の機構をそのまま使い回す。`implementation_result.json`を`{"status":"done"}`で事前投入することでフェーズ1-4を丸ごとスキップし、フェーズ5に直行する。PLAN.mdが存在しないため機械的バックストップ（ADR-0010）は自動スキップ
  - `_compute_diff()`はworktree作成時に記録した基準ref（`.masuda-base-ref`、`worktree.Create`が書き込む）に対して差分を取るよう変更。理由: 実装フェーズの差分はbare HEAD（未コミット分）で正しく見えるが、レビュー単体は既にコミット済みのブランチなのでbare HEADでは差分が空になる
  - 実機テストで、`git add -A`がmasuda自身の内部ファイル（`.masuda-base-ref`・`implementation_result.json`等）まで差分に混入させてしまうバグを発見。ステージ後に内部ファイルだけ`git reset`で除外するよう修正
  - **既知の制約（ロードマップ8番で解消予定）**: worktreeパス・コンテナ名はbranch名だけがキーのため、同じbranchに対してフルパイプラインが並行稼働していると衝突する。今回は「PLAN.mdが既にある」「セッションが既に起動中」の場合に拒否する安全策のみ実装した
- 実機で、既存ブランチにコミット済みの変更を用意し`masuda review start`→フェーズ5直行→機密情報なしと正しく判定、まで確認済み

未着手: ワークスペースIDによる並列実行対応（ロードマップ8番）、横断的チェック（LSP経由の整合性検証、ロードマップ7番。Dockerサンドボックス内でのLSPプラグイン導入方法に追加調査が必要）。

## 開発環境

- Python: `venv/`に依存関係インストール済み（`requirements.txt`）
- テスト: `pytest`
