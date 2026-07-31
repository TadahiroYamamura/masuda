# masuda

AIとの協同開発（調査→プラン作成→git worktree作成→プロジェクト初期化→実装→レビュー）を、ローカルPC上のサンドボックスで自己ループ実行するためのツール群。

## 作業を始める前に読むもの

- `docs/design/sandbox-workflow.md`: 全体アーキテクチャ（6フェーズ+2ゲート、メインエージェント/サブエージェントの役割分担、レビューフェーズの内部設計、リポジトリの目標構造等）
- `docs/adr/0001`〜`0018`: 個々の設計判断とその理由・却下した代替案。番号順に読むと議論の経緯が追える
- 「何を実装したか・実機で何が起きたか」というログは、実装ロードマップという形では保持していない。各コミットメッセージ（`意図`・`設計上の考慮点`・`懸念事項`を含む）が実質的にその役割を担っているため、`git log`を参照すること

設計ドキュメント・ADRで定義された範囲は実装済み（ロードマップ1〜8番完了）。今後の拡張（横断的チェックの観点追加、他言語イメージバリアントの追加等）は都度ADRを起票して進める。

## 現状の構成

### ループ機構・ゲート

- `runtime/CLAUDE.md`: サンドボックス内で自己ループするClaudeの作業ループ仕様。`GATE:<name>`終了条件（ADR-0006）を実装している——終了条件（`DONE`）とゲート条件（`GATE:<name>`）は排他で、ゲート条件を満たす場合はセッションを終了せず`<state-dir>/.masuda-gate/<name>.json`のstatusがpendingでなくなるまで待機する。この待機は`inotifywait`の単発ブロッキング呼び出しで実装している（ADR-0017、`while`ループやMonitorツールでのポーリングは無人ループが確認プロンプトで詰まるため不採用）
- `runtime/claude-settings.json`: `~/.claude/settings.json`にビルド時に焼き込む。テーマ未設定だと新規コンテナの初回`claude`起動が対話式のテーマ選択ウィザードで止まるための対策

### オーケストレーター

- `orchestrator/investigate_plan_graph.py`: フェーズ0-2（worktree作成→調査→プラン作成→G1）の状態遷移ロジック。ADR-0012によりDockerサンドボックスなしでホスト上で動く。G1到達（`await_g1`）はGATE:planとして待機する
- `orchestrator/implement_review_graph.py`: フェーズ4-5（実装・レビュー）の状態遷移ロジック。ADR-0013により1つのオーケストレーターにまとめている。Dockerサンドボックス内で動く
  - フェーズ4: ADR-0009のビルド/テスト自己修正はサブエージェント内で完結させ、オーケストレーターは`implementation_result.json`の結果（done/needs_plan_review/build_test_failed）だけを見る。ADR-0010の機械的バックストップ（PLAN.mdの「変更するファイル一覧」と`git status --porcelain`の突き合わせ、LLM不使用）が計画外のファイル変更を検知すると`DEVIATION.md`を書き出しG1を再オープンする（GATE:planとして待機）
  - ゲートマーカーの消費（承認/却下の反映）は、再オープンを新規検知した瞬間ではなく、実際に人間の判断が下された解決時点でのみ行う（新規検知のたびに即座に消費すると、承認済みの逸脱が次回チェックで同一の逸脱として検知され無限に再オープンし続けるため）。新規検知時に古いゲートマーカーが残っていれば削除する——最初のG1承認時のマーカーは`investigate_plan_graph.py`側で削除されず残り続けるため、削除しないと過去の別の承認が今回の逸脱の判断として誤って消費されてしまう。承認済み逸脱は`.masuda-approved-deviations.json`に記録し、以後の機械的バックストップから除外する
  - フェーズ5: 14観点review/checkループ（`perspectives/config.py`）をサブエージェント委譲（Task tool、diffのみを見せる機械的チェック）で実行する。まだ解決していない観点は毎ラウンド全件スキャンし、独立している観点をまとめて1メッセージ内で並列委譲する（ADR-0021）。G2到達（`await_g2`）はGATE:reviewとして待機する。G2却下時はフィードバックを持ってフェーズ4に差し戻し、レビューは全観点やり直す（ADR-0013）
  - checker/fixer自動修正ループ（ADR-0004）: checkがhas_issues=trueの指摘を確認すると、指摘箇所のみのfixerサブエージェントが修正し、新規のcheckerで再検証する。解決すればfixed一覧へ、MAX_RETRIES到達で未解決としてsynthesizeに引き継ぐ
  - 横断的チェック（ADR-0003・ADR-0011）: 14観点収束後、explorer→verifierの1パス構成（redoなし）を実行する。explorerはBash/Read/Grep/Glob+ネイティブLSPツールへのフルアクセスを持つサブエージェントにdiff起点の多ターン探索を委譲し、`review_results/cross_cutting_findings.json`に書き出させる。findingsが空ならverifierをスキップしてsynthesizeへ直行、findingsがあれば独立したverifierサブエージェントが妥当性のみを検証し`cross_cutting_verified.json`に確認済み分だけ残す。確認済みの指摘は自動修正せず、常に最終レポートの「横断的チェックの指摘」セクションに上げてG2で人間が判断する
    - **サブエージェント向けプロンプトで「探索の観点の例」を書く際の指針**: 列挙する項目のカテゴリ粒度が揃っているか（並列に見える項目が本当に同じ種類の判断か）を確認する。また、新しい（コストの高い）チェック機構向けの例が、既存の安価な機構（ビルドの型検査、既存のredoループ等）で既に検知されてしまわないか確認する（例: 静的型付け言語ではシグネチャの引数過不足はビルドエラーになりADR-0009の自己検証で既に弾かれる）
  - `masuda plan show`はDEVIATION.mdがあれば表示、`masuda plan approve/reject`が消費する。`masuda review show`はfinal_report.md、`review approve`がブランチのfast-forward反映・後片付け（ADR-0023、develop等へのローカルmergeはしない）、`review reject`がフェーズ4差し戻しをトリガーする
  - `masuda review hunk <workspace-id>`: `review show`の代替として、`review_results/`の未解決指摘をHunk（外部ツール、要ホスト側インストール）の`--agent-context`サイドカー形式に変換しdiff上へ注釈表示する（ADR-0019、変換スキーマはADR-0020、`internal/hunkcontext`）
- `orchestrator/tests/`: 上記2つのLayer 1テスト（pytest、ファイルシステム状態を模擬、LLM呼び出しなし）
- `orchestrator/perspectives/`: 14観点の定義（`config.py`）。`implement_review_graph.py`のフェーズ5から参照

#### 予算管理（ADR-0011）

`investigate_plan_graph.py`・`implement_review_graph.py`それぞれに独立した`ITERATION_BUDGET`定数（ホスト側・Docker側で別プロセス・別環境として動くため共有していない）。`write_task_md`が実際にサブエージェントへ委譲するフェーズ（`_SUBAGENT_PHASES`）でのみ`.masuda-iteration-count`を1加算し、超過時はredoループ（`MAX_RETRIES`・`MAX_REVIEW_RETRIES`）とは独立した最終防衛ラインとして`DONE (blocked)`で停止する。現在の設定値: フェーズ1-2は20（調査/プランの往復`MAX_RETRIES=3`に加えG1再オープンの余裕）、フェーズ4-5は200（14観点×review/check・fix/recheckの往復＋横断的チェック＋synthesize＋実装再オープンの余裕）。サブエージェント単体の内部ターン数上限（ADR-0011の2層目）はオーケストレーターから可視でないため、横断的チェックのexplorerタスクへのプロンプト指示（探索範囲を絞ること）による自主規制のみで対応している。

### ワークスペースID・リポジトリ設定ファイル（ADR-0014・0015）

- `internal/workspace/`: ワークスペースID（`<sanitized-branch>-<ランダム6桁hex>`形式、`NewID`）とその状態ディレクトリ（`~/.local/share/masuda/workspaces/<id>/`、XDG_DATA_HOME尊重）を管理するパッケージ。`Create`/`Load`/`Exists`/`List`/`Remove`を提供する。branch名ではなくこのIDが以後すべてのCLIサブコマンドの引数・worktree/コンテナ/tmuxセッションのアドレッシングキーになる——同じbranchに対して複数のワークスペースが並行して存在できるようにするため
- `internal/config/`: 対象リポジトリのルート直下に置く`.masuda.json`（ユーザーが手で編集してコミットする、任意ファイル）を読む。`image`（使うDockerイメージ）と`base`（trunk branch名）の2フィールドを定義する。`resolveImage`/`resolveBase`（`cmd/masuda/main.go`）が`--image`フラグ/`--base`・`--into`フラグ＞`.masuda.json`の値＞デフォルトの優先順位で解決する。`image`フィールドの設計判断（masuda側で言語検出ヒューリスティックを持たずrepo側に委ねる理由）はADR-0015を参照

### 既知の課題（未修正）

- `newWorkspace()`（`cmd/masuda/workspace.go`）は`workspace.Create`（状態ディレクトリ作成）の後に`worktree.Create`（gitクローン）を呼ぶため非atomicで、後者が失敗する（例: 存在しない`base`を指定）と状態ディレクトリだけが孤児として残る

### Go CLI（cmd/masuda）

- `masuda workspace create|merge|remove|list`: ワークスペースのライフサイクル管理。`internal/worktree`パッケージ自体はgitチェックアウトの実装詳細として維持し、CLIコマンド名としては出さない（「worktree」というgit用語のコマンドグループの下に、状態ディレクトリ・メタデータまで含む広い概念の操作が混在するのは違和感がある、というレビュー指摘による改名）
  - `create <branch> [--base]`: 新規ワークスペースID発行＋`git clone --local`によるローカルクローン方式（ADR-0018、`git worktree add`ではない）
  - `merge|remove <workspace-id>`: `workspace.Load`でbranch名を引き、`merge`はクローン側のブランチをメインリポジトリへ`git fetch`してから`git merge`する（ユーザーが明示的に叩く手動のローカル統合。`review approve`が自動で行うfast-forward限定の反映＝ADR-0023の`worktree.Pull`とは別物）。`remove`はworktree削除に続けて状態ディレクトリも削除する
  - `list`: 現在のリポジトリに紐づく全ワークスペースの一覧
- `masuda sandbox start|stop <workspace-id>`: worktreeのbind mountに加え、状態ディレクトリを`/masuda-state`に、ホストの`~/.claude/.credentials.json`・`~/.claude.json`をbind mountしてサブスク認証を引き継ぐ（ADR-0001）。前回のコンテナが（tmuxセッション終了により）Exited状態で残っていると`docker create`が名前衝突で失敗するため、`start`は同名の既存コンテナを`docker rm -f`してから作り直す
- `masuda plan start <branch> "<task>"`（新規）/ `masuda plan start <workspace-id>`（再開）: 引数が既存ワークスペースIDかどうか（`workspace.Exists`）で新規/再開を判別する。新規はワークスペースID発行＋worktree作成＋フェーズ1-2のホスト側自己ループ起動（`internal/hostloop`）。メインセッションは`Bash,Task,Read,Edit`（成果物3ファイルのみ、状態ディレクトリの絶対パス）だけを持ち、実際にリポジトリ内容を読み回る調査・プラン作成はBashなしのカスタムエージェント（`investigator`/`planner`、`Read,Grep,Glob,Edit`のみ）にTask委譲する（ADR-0002の具体化）
  - **Claude Codeの許可ルールの罠**: 状態ディレクトリ（worktree外の絶対パス）への書き込みを事前承認する`Edit(/abs/path)`ルールは、単一の先頭スラッシュが「ルール自身が置かれた場所からの相対アンカー」と解釈されるため、パスが完全一致していても常に確認プロンプトが出る。真に絶対パスとして固定するには`Edit(//abs/path)`のように先頭スラッシュを2つ重ねる必要がある（既知のアップストリーム課題: [anthropics/claude-code#25137](https://github.com/anthropics/claude-code/issues/25137)、[#18200](https://github.com/anthropics/claude-code/issues/18200)）
  - サブエージェントに新しいツールを追加したら、セッションレベルの`allowedTools`にも同じツールをパス制限なしで追加すること。エージェント定義側（`customAgentsJSON`）にツールを持たせるだけでは、セッションレベルの許可ルールに乗っていない限りデフォルトの確認プロンプトに落ちる（`Grep`/`Glob`が未許可でplannerが停止した実例あり）
  - `go:embed`のパターンは宣言ファイル自身のディレクトリ以下にしか到達できず`..`を挟めないため、`assets.go`でembedするファイル（`orchestrator/`・`runtime/`）はリポジトリルート直下に置く必要がある。また`orchestrator/investigate_plan_graph.py`や`runtime/CLAUDE.md`を編集した後は`go build`し直さないと埋め込み内容が更新されない（Dockerイメージの再ビルド忘れと対になる注意点、後述）
- `masuda chat <workspace-id>`: フェーズ1-2ホストループかフェーズ3-5サンドボックスか、どちらのセッションが生きているかだけを見てattachする（ゲート名を引数に取らない）。G1がフェーズ1-2ホストループ・フェーズ4-5のDocker再オープンどちらで待機していても対応する
- `Dockerfile`: masuda自身の制御ファイル（`venv`・`orchestrator`・`runtime`）は`/opt/masuda`に配置し、`/workspace`（worktree）・`/masuda-state`（状態ディレクトリ）は対象ワークスペース専用のbind mount先として空けてある

### レビュー単体エントリーポイント

- `masuda review start <branch-or-ref> [--base develop]`: 既存の（新規作成ではない）ブランチに対し、フェーズ0（worktree作成）〜フェーズ5（レビュー）の機構をそのまま使い回す。`implementation_result.json`を`{"status":"done"}`で事前投入することでフェーズ1-4を丸ごとスキップし、フェーズ5に直行する。PLAN.mdが存在しないため機械的バックストップ（ADR-0010）は自動スキップ
  - `_compute_diff()`はworktree作成時に記録した基準ref（状態ディレクトリの`.masuda-base-ref`、`workspace.Create`が書き込む）に対して差分を取る——実装フェーズの差分はbare HEAD（未コミット分）で正しく見えるが、レビュー単体は既にコミット済みのブランチなのでbare HEADでは差分が空になるため
  - masuda自身の制御ファイル（TASK.md・PLAN.md・INVESTIGATION.md・`.masuda-gate/`・`.masuda-base-ref`・`.masuda-review-state.json`・`review_results/`等）はworktree外の状態ディレクトリ（`internal/workspace`）に置かれるため、`git add -A`にmasuda自身の内部ファイルが混入することはない

### Dockerイメージ・LSPプラグイン（ADR-0015）

- `Dockerfile`（base）: `claude plugin marketplace add anthropics/claude-plugins-official`で公式マーケットプレイスを登録する（認証不要、公開GitHubリポジトリへの`git clone`のみ）。プラグイン状態（`extraKnownMarketplaces`・`enabledPlugins`）は`~/.claude/settings.json`（ビルド時にCOPYで焼き込み）・`~/.claude/plugins/`に保存され、コンテナ起動時にホストからbind mountされる`~/.claude.json`・`~/.claude/.credentials.json`とは別ファイルなので上書きされない
- `docker/{go,python,typescript,full}/Dockerfile`: `masuda-loop:latest`から派生する言語別バリアント。`.masuda.json`の`image`フィールドまたは`--image`でユーザーが選ぶ
  - `docker/go/Dockerfile`: Goツールチェーン＋`gopls`＋`gopls-lsp`プラグイン。リポジトリ直下に`Dockerfile.go`という名前で置くと、Goの`go build ./...`・`go vet ./...`がそれを`.go`ソースファイルとして誤認しビルドが壊れるため、`docker/go/Dockerfile`という配置にしている。`go install`後のモジュールキャッシュ削除は`rm -rf`だと権限エラーになる（Goがモジュールキャッシュを読み取り専用にするため）ため`go clean -modcache -cache`を使う
  - `docker/python/Dockerfile`・`docker/typescript/Dockerfile`: `pyright`・`typescript-language-server`はnpm配布のためbase imageのNode.jsに乗るだけで済むが、npmのグローバルインストール先（`/usr/lib/node_modules`）がroot所有のため、そのステップだけ`USER root`に戻す必要がある
  - `docker/full/Dockerfile`: 上記3つを1つにまとめたkitchen sinkバリアント。複数言語混在repo向け
- **開発時の注意**: `orchestrator/`はDockerイメージのビルド時に`COPY`で焼き込まれ、コンテナ起動後に中身が変わることはない。オーケストレーター（`investigate_plan_graph.py`・`implement_review_graph.py`）のコードを変更した後にDockerでの実機テストを行う場合、`masuda-loop:latest`（base）と使用する言語バリアントイメージの両方を必ず再ビルドしてからコンテナを起動し直すこと。再ビルドを忘れると、古いコードのままコンテナが動き続け、新しいフェーズが一切実行されずに次のフェーズへ直行するなど、原因が分かりにくい形で不具合が出る

## 開発環境

- Python: `venv/`に依存関係インストール済み（`requirements.txt`）。テスト: `pytest orchestrator/tests/`
- Go: `go build ./...`・`go vet ./...`・`go test ./...`（標準の`go`ツールチェーンのみ、追加セットアップ不要）
- GitHub操作（Issue作成等）は`gh`を直接使わず`scripts/gh.sh`を使うこと。このリポジトリ専用のトークンを`.env`（Claudeからは読み書き不可、`.claude/settings.json`参照）から読み込んで`gh`に渡すラッパー
