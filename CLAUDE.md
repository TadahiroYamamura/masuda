# masuda

AIとの協同開発（調査→プラン作成→git worktree作成→プロジェクト初期化→実装→レビュー）を、ローカルPC上のサンドボックスで自己ループ実行するためのツール群。

## 作業を始める前に読むもの

- `docs/design/sandbox-workflow.md`: 全体アーキテクチャ（6フェーズ+2ゲート、メインエージェント/サブエージェントの役割分担、レビューフェーズの内部設計、リポジトリの目標構造等）
- `docs/adr/0001`〜`0015`: 個々の設計判断とその理由・却下した代替案。番号順に読むと議論の経緯が追える
- 「何を実装したか・実機で何が起きたか」というログは、実装ロードマップという形では保持していない。各コミットメッセージ（`意図`・`設計上の考慮点`・`懸念事項`を含む）が実質的にその役割を担っているため、`git log`を参照すること

これらは実装より前に固めた設計であり、現在のリポジトリの中身（後述）はまだこの設計に追随していない。

## 現状（実装ロードマップ1〜8 完了）

### ループ機構・ゲート（ロードマップ5番）

- `runtime/CLAUDE.md`: 作業ループ仕様。`GATE:<name>`終了条件（ADR-0006、ロードマップ5番）を実装済み——終了条件（`DONE`）とゲート条件（`GATE:<name>`）は排他で、ゲート条件を満たす場合はセッションを終了せず`<state-dir>/.masuda-gate/<name>.json`のstatusがpendingでなくなるまで待機する。単発Bashの`while`ループは動的な文字列を含むコマンドとして確認を求められ無人ループで詰まることを実機で確認したため、Monitorのような監視系ツールを使うよう指示している——ただしロードマップ7番の実機テストでも、フェーズ1-2の自己ループが指示に反して素のBash `while`ループを書いてしまい同じ確認プロンプトで詰まる場面を再確認した。Monitorツールの使用を徹底させる指示の強化は今回のスコープ外として未着手のまま残っている
- `runtime/entrypoint.sh`・`runtime/start_claude.sh`: ルート直下から移動済み
- `runtime/claude-settings.json`: `~/.claude/settings.json`にビルド時焼き込み。テーマ未設定だと新規コンテナの初回`claude`起動が対話式のテーマ選択ウィザードで止まることが実機で判明したための対策

### オーケストレーター（ロードマップ1〜4・8番）

- `orchestrator/investigate_plan_graph.py`: フェーズ0-2（worktree作成→調査→プラン作成→G1）の状態遷移ロジック。ADR-0012によりDockerサンドボックスなしでホスト上で動く。G1到達（`await_g1`）はGATE:planとして待機する（旧: セッション終了）
- `orchestrator/implement_review_graph.py`: フェーズ4-5（実装・レビュー）の状態遷移ロジック。ADR-0013により1つのオーケストレーターにまとめている。Dockerサンドボックス内で動く
  - フェーズ4: ADR-0009のビルド/テスト自己修正はサブエージェント内で完結させ、オーケストレーターは`implementation_result.json`の結果（done/needs_plan_review/build_test_failed）だけを見る。ADR-0010の機械的バックストップ（PLAN.mdの「変更するファイル一覧」と`git status --porcelain`の突き合わせ、LLM不使用）は実機で逸脱検知を確認済み。逸脱検知時は`DEVIATION.md`を書き出しG1のゲートマーカーを削除して再オープン（GATE:planとして待機）する
  - **G1再オープンの承認/却下分岐**: 当初`plan_reopened`検知時に即座にゲートマーカー・implementation_result.jsonをクリアしていたが、これだと承認された逸脱も次回チェックで同一の逸脱として検知され無限に再オープンし続けるバグがあった（GATE導入でセッションが自動再開するようになって顕在化）。`.masuda-approved-deviations.json`に承認済み逸脱のファイル一覧を記録し、以後の機械的バックストップから除外する形に修正した。却下時・自己申告（needs_plan_review/build_test_failed）の承認時はフェーズ4を再実行する
  - フェーズ5: `feat/github-actions-langgraph-nodes`の13観点review/checkループ（`perspectives/config.py`）を、直接API呼び出しからサブエージェント委譲（Task tool、diffのみを見せる機械的チェック）に移植。review/checkの往復・redo・unresolved・synthesizeまで実機で確認済み。G2到達（`await_g2`）はGATE:reviewとして待機する。G2却下時はフィードバックを持ってフェーズ4に差し戻し、レビューはperspective 0からやり直す（ADR-0013、実機確認済み）
  - checker/fixer自動修正ループ（ADR-0004）: checkがhas_issues=trueの指摘を確認すると、指摘箇所のみのfixerサブエージェントが修正し、新規のcheckerで再検証する。解決すればfixed一覧へ、MAX_RETRIES到達で未解決としてsynthesizeに引き継ぐ。実際にAPIキーのハードコードを注入し、fixerが環境変数読み取りに修正、recheckが解決確認するところまで実機確認済み
  - **横断的チェック（ロードマップ8番、ADR-0003・ADR-0011）**: 13観点収束後、explorer→verifierの1パス構成（redoなし）を実行。explorerはBash/Read/Grep/Glob+ネイティブLSPツールへのフルアクセスを持つサブエージェントにdiff起点の多ターン探索を委譲し、`review_results/cross_cutting_findings.json`に書き出させる。findingsが空ならverifierをスキップしてsynthesizeへ直行、findingsがあれば独立したverifierサブエージェントが妥当性のみを検証し`cross_cutting_verified.json`に確認済み分だけ残す。確認済みの指摘は自動修正せず、常に最終レポートの「横断的チェックの指摘」セクションに上げてG2で人間が判断する。実機テスト（`masuda-loop:go`）で、意図的に仕込んだコード上の矛盾（stderr握りつぶし・標準ライブラリで済む処理の外部コマンド化等）に対しexplorerが実際にLSPの`findReferences`を使って5件検出（仕込んだ本命2件＋偶発的なgofmt違反・デッドコード・潜在バグの計3件）、verifierが独立した追加検証を経て全件確認、最終レポートが13観点の「デッドコード」観点では見えなかった問題を横断的チェックが検出したことまで言及するのを確認済み——ADR-0003が想定した価値が実際に機能することを実機で確認できた
    - **サブエージェント向けプロンプトを書く際の教訓（レビュー指摘を受けて修正）**: `_cross_cutting_explore_task()`の「探索の観点の例」を当初3つの箇条書きで並列に書いていたが、性質の異なる観点（「実装パターンの一貫性」と「変更の伝播漏れ」）を無自覚に混ぜていた。例を列挙する際はカテゴリの粒度が揃っているか（並列に見える項目が本当に同じ種類の判断か）を確認すること。またそのうちの一例（シグネチャ変更が呼び出し元の一部に反映されていない）は、Go等の静的型付け言語では単純な引数の過不足がコンパイルエラーになりフェーズ4のビルド自己検証（ADR-0009）で既に弾かれてしまう、という指摘も受けた。新しい（コストの高い）チェック機構向けの例を書くときは、それが既存の安価な機構（ビルド・既存のmax_retries付きループ等）で既に検知されてしまわないか確認すること
  - `masuda plan show`はDEVIATION.mdがあれば表示、`masuda plan approve/reject`が消費する。`masuda review show`はfinal_report.md、`review approve`が既存通りマージ・後片付け、`review reject`がフェーズ4差し戻しをトリガーする
- `orchestrator/tests/`: 上記2つのLayer 1テスト（pytest、ファイルシステム状態を模擬、LLM呼び出しなし、計80件）
- `orchestrator/perspectives/`: `feat/github-actions-langgraph-nodes`ブランチの13観点（`config.py`）。`implement_review_graph.py`のフェーズ5から参照済み

#### 予算管理（ロードマップ8番、ADR-0011）

`investigate_plan_graph.py`・`implement_review_graph.py`それぞれに独立した`ITERATION_BUDGET`定数（ホスト側・Docker側で別プロセス・別環境として動くため共有していない）。`write_task_md`が実際にサブエージェントへ委譲するフェーズ（`_SUBAGENT_PHASES`）でのみ`.masuda-iteration-count`を1加算し、超過時はredoループ（`MAX_RETRIES`・`MAX_REVIEW_RETRIES`）とは独立した最終防衛ラインとして`DONE (blocked)`で停止する。フェーズ1-2は調査/プランの往復（`MAX_RETRIES=3`）に加えG1再オープン（human-gated）を考慮し20、フェーズ4-5は13観点×最大12回（review/check最大6＋fix/recheck最大6）+横断的チェック(2)+synthesize(1)+実装再オープンの余裕を見て200とした（`feat/github-actions-langgraph-nodes`のITERATION_BUDGET=78の算出方法を踏襲しつつ、fix/recheckループ分を追加）。サブエージェント単体の内部ターン数上限（ADR-0011の2層目）はオーケストレーターから可視でないため、横断的チェックのexplorerタスクへのプロンプト指示（探索範囲を絞ること）による自主規制のみで対応している。Layer 1テストで両ファイルとも「カウントされる/されないフェーズ」「超過時に上書きされること」を確認済み。

### ワークスペースID・リポジトリ設定ファイル（ロードマップ7・8番、ADR-0014・0015）

- `internal/workspace/`（ロードマップ7番、新設、ADR-0014）: ワークスペースID（`<sanitized-branch>-<ランダム6桁hex>`形式、`NewID`）とその状態ディレクトリ（`~/.local/share/masuda/workspaces/<id>/`、XDG_DATA_HOME尊重）を管理するパッケージ。`Create`がメタデータ（`workspace.json`: branch/base/repo_root/created_at）と`.masuda-base-ref`を書き込み、`Load`/`Exists`/`List`/`Remove`を提供する。branch名ではなくこのIDが以後すべてのCLIサブコマンドの引数・worktree/コンテナ/tmuxセッションのアドレッシングキーになる——同じbranchに対して複数のワークスペースが並行して存在できるようにするため
- `internal/config/`（ロードマップ8番着手前の下準備、新設、ADR-0015）: 対象リポジトリのルート直下に置く`.masuda.json`（ユーザーが手で編集してコミットする、任意ファイル）を読む。現状は`image`（`masuda sandbox start`/`masuda review start`実行時に使うDockerイメージ）と`base`（trunk branch名）の2フィールドのみ定義
  - `image`: `resolveImage`（`cmd/masuda/main.go`）が`--image`フラグ（明示指定があれば最優先）→`.masuda.json`の`image`→`sandbox.DefaultImage`の順で解決する。ロードマップ8番（LSP経由の横断的チェック）で「対象repoの言語ごとにLSPツールを同梱したDockerイメージ（例: `masuda-loop:go`）をどう選ぶか」を検討した結果、masuda側で言語検出のヒューリスティックを持たず、ユーザー（または対象repoの保守者）が`.masuda.json`に使うイメージを明示する方式に決めた——秘密情報の扱いと同様、repo自身の設定に委ねる思想（`internal/hostloop`の`allowedTools`コメント参照）
  - `base`: `resolveBase`（`cmd/masuda/main.go`）が同様の優先順位で解決し、`masuda workspace create`・`plan start`・`review start`の`--base`と`masuda workspace merge`の`--into`の両方のデフォルトとして使う（両者は実運用ではほぼ同じbranchを指すため1フィールドで共有）。ハードコードされた`defaultBase = "develop"`は、このリポジトリ自身のtrunk branchが実際には`main`であるにもかかわらず一致しないというズレが元々あり、`.masuda.json`はこれをrepoごとに正しい値に上書きできるようにするためのものでもある
  - **既知の制約**: `newWorkspace()`（`cmd/masuda/workspace.go`）は`workspace.Create`（状態ディレクトリ作成）の後に`worktree.Create`（gitクローン）を呼ぶため非atomicで、後者が失敗する（例: 存在しない`base`を指定）と状態ディレクトリだけが孤児として残る。実機で確認済みだが未修正

### Go CLI（cmd/masuda）

- `cmd/masuda/`: cobraベースのGo CLI
  - `masuda workspace create|merge|remove|list`（コマンド名: ロードマップ7番の実装直後にレビュー指摘を受けて`worktree`から改名。「worktree」というgit用語のコマンドグループの下にbranch名だけでなく状態ディレクトリ・メタデータまで含む広い概念の操作が混在するのは違和感がある、との理由。`internal/worktree`パッケージ自体はgitチェックアウトの実装詳細として維持し、CLIコマンド名としては出さない）
    - `create <branch> [--base]`: 新規ワークスペースID発行＋`git clone --local`によるローカルクローン方式（`git worktree add`ではない）。理由: linked worktreeは対象worktree自身の絶対パスとメインリポジトリの`.git/worktrees/<name>`が双方向に絶対パス参照し合う構造で、`/workspace`のような別パスへのbind mountに耐えられないことが実機で判明したため
    - `merge|remove <workspace-id>`: `workspace.Load`でbranch名を引き、`merge`はクローン側のブランチをメインリポジトリへ`git fetch`してから`git merge`する。`remove`はworktree削除に続けて状態ディレクトリも削除する
    - `list`: 現在のリポジトリに紐づく全ワークスペースの一覧（ロードマップ7番で新設）
  - `masuda sandbox start|stop <workspace-id>`: worktreeのbind mountに加え、状態ディレクトリを`/masuda-state`に、ホストの`~/.claude/.credentials.json`・`~/.claude.json`を bind mount してサブスク認証を引き継ぐ（ADR-0001）。前回のコンテナが（tmuxセッション終了により）Exited状態で残っていると`docker create`が名前衝突で失敗するため、`start`は同名の既存コンテナを`docker rm -f`してから作り直す。既に起動中なら冪等に既存コンテナの情報を返す
  - `masuda plan start <branch> "<task>"`（新規）/ `masuda plan start <workspace-id>`（再開）: 引数が既存ワークスペースIDかどうか（`workspace.Exists`）で新規/再開を判別する。新規はワークスペースID発行＋worktree作成＋フェーズ1-2のホスト側自己ループ起動（`internal/hostloop`。`claude --append-system-prompt-file`でループ仕様を注入、`MASUDA_STATE_DIR`環境変数でオーケストレーターに状態ディレクトリを伝える）。メインセッションは`Bash,Task,Read,Edit(成果物3ファイルのみ、状態ディレクトリの絶対パス)`だけを持ち、実際にリポジトリ内容を読み回る調査・プラン作成は`--agents`で定義したBashなしのカスタムエージェント（`investigator`/`planner`、`Read,Grep,Glob,Edit`のみ）にTask委譲する
    - **Edit許可ルールの罠（実機で判明）**: 状態ディレクトリ（worktree外の絶対パス）への書き込みを事前承認する`Edit(/abs/path)`ルールは、パスが完全一致していても常に確認プロンプトが出て自動承認されない。原因はClaude Codeの許可ルールが単一の先頭スラッシュを「ルール自身が置かれた場所からの相対アンカー」として解釈するためで、真に絶対パスとして固定するには`Edit(//abs/path)`のように先頭スラッシュを2つ重ねる必要がある（既知のアップストリーム課題: [anthropics/claude-code#25137](https://github.com/anthropics/claude-code/issues/25137)、[#18200](https://github.com/anthropics/claude-code/issues/18200)）。修正前は無人ループが永久に確認待ちで詰まっていた
  - `masuda plan|review chat <workspace-id>`: `internal/hostloop.AttachArgs`（ホスト、`tmux attach`）と`internal/sandbox.AttachArgs`（Docker、`docker exec -it ... tmux attach`）の両方を用意し、`plan chat`はG1がフェーズ1-2ホストループ・フェーズ4-5のDocker再オープンどちらで待機していても対応する。実機でfast-path（`plan/review approve`）・chatパス（対話でClaude自身がマーカーを書く）の両方をG1・G2で確認済み
- `Dockerfile`: masuda自身の制御ファイル（`venv`・`orchestrator`・`runtime`）は`/opt/masuda`に配置し、`/workspace`（worktree）・`/masuda-state`（状態ディレクトリ、ロードマップ7番で追加）は対象ワークスペース専用のbind mount先として空けてある。`git`を追加済み
- `webhook_server.py`: 設計ドキュメントの目標構造に存在しないため削除済み

### レビュー単体エントリーポイント（ロードマップ6番）

- `masuda review start <branch-or-ref> [--base develop]`（ロードマップ6番、design docは`masuda review <branch-or-ref>`だが既存のサブコマンド構成に合わせて`start`を追加）: 既存の（新規作成ではない）ブランチに対し、フェーズ0（worktree作成）〜フェーズ5（レビュー）の機構をそのまま使い回す。`implementation_result.json`を`{"status":"done"}`で事前投入することでフェーズ1-4を丸ごとスキップし、フェーズ5に直行する。PLAN.mdが存在しないため機械的バックストップ（ADR-0010）は自動スキップ
  - `_compute_diff()`はworktree作成時に記録した基準ref（状態ディレクトリの`.masuda-base-ref`、`workspace.Create`が書き込む）に対して差分を取るよう変更。理由: 実装フェーズの差分はbare HEAD（未コミット分）で正しく見えるが、レビュー単体は既にコミット済みのブランチなのでbare HEADでは差分が空になる
  - **ロードマップ7番で解消**: masuda自身の制御ファイル（TASK.md・PLAN.md・INVESTIGATION.md・`.masuda-gate/`・`.masuda-base-ref`・`.masuda-review-state.json`・`review_results/`等）をworktree外の状態ディレクトリ（`internal/workspace`）に完全に移した。これにより`git add -A`がmasuda自身の内部ファイルを差分に混入させてしまうバグ（除外リストで対症療法していた）が構造的に解消され、`_actual_changed_files()`/`_compute_diff()`の除外ロジック（`_MASUDA_INTERNAL_FILES`等）を削除できた。また`masuda review start`実行のたびに新規ワークスペースIDを発行するようになったため、旧実装にあった「同じbranchへのフルパイプライン並行稼働時の衝突」の拒否安全策も不要になり削除した

#### 実機確認（ロードマップ6・7番）

- 同一branchに対する2つの並行ワークスペース（`masuda plan start`を同じbranch名で2回起動）がそれぞれ独立したworktree/状態ディレクトリ/tmuxセッションで衝突なく調査・プラン作成からG1ゲート到達まで進み、`INVESTIGATION.md`/`PLAN.md`等が状態ディレクトリ側にのみ生成されworktree側・互いのワークスペース間に一切漏れ出さないことを確認済み
- 既存ブランチにコミット済みの変更を用意し`masuda review start`→フェーズ5直行→機密情報なしと正しく判定、まで確認済み

### Dockerイメージ・LSPプラグイン（ロードマップ8番、ADR-0015）

- `Dockerfile`（base）: `claude plugin marketplace add anthropics/claude-plugins-official`を追加（ロードマップ8番）。公式マーケットプレイスは認証不要（公開GitHubリポジトリへの`git clone`のみ）でビルド時に問題なく登録できることを実機で確認済み。プラグイン状態（`extraKnownMarketplaces`・`enabledPlugins`）は`~/.claude/settings.json`（ビルド時にCOPYで焼き込み）・`~/.claude/plugins/`に保存され、コンテナ起動時にホストからbind mountされる`~/.claude.json`・`~/.claude/.credentials.json`（`~/.claude.json`の中身はinstallMethod・machineID等の汎用メタデータのみでプラグイン関連キーを含まないことを実機確認済み）とは別ファイルなので、起動時のbind mountで焼き込んだ設定が上書きされる心配はない
- `docker/{go,python,typescript,full}/Dockerfile`（ロードマップ8番、新設）: `masuda-loop:latest`から派生する言語別バリアント。`.masuda.json`の`image`フィールド（`internal/config`）または`--image`でユーザーが選ぶ
  - `docker/go/Dockerfile`: Goツールチェーン（1.26.5、sha256固定）＋`go install golang.org/x/tools/gopls@latest`＋`gopls-lsp`プラグイン。**注意**: リポジトリ直下に`Dockerfile.go`という名前で置くと、Goの`go build ./...`・`go vet ./...`がそれを`.go`ソースファイルとして誤認しビルドが壊れることを実機で発見したため、`docker/go/Dockerfile`という配置にした。また`go install`後の`rm -rf`によるモジュールキャッシュ削除は権限エラーで失敗する（Goがモジュールキャッシュを読み取り専用にするため）ことも実機で発見し、`go clean -modcache -cache`に変更した
  - `docker/python/Dockerfile`・`docker/typescript/Dockerfile`: `pyright`・`typescript-language-server`はnpm配布のためbase imageに既にあるNode.jsに乗るだけで済み、新規システムトゥールチェーン不要。npmのグローバルインストール先（`/usr/lib/node_modules`）がroot所有のため、その1ステップだけ`USER root`に戻す必要があった（base imageの最後のUSERディレクティブは`ubuntu`のため）
  - `docker/full/Dockerfile`: 上記3つを1つにまとめたkitchen sinkバリアント。複数言語混在repo向け
  - 実機ビルドで確認したイメージサイズ: base 1.64GB → go +410MB → python +50MB → typescript +80MB → full（3つ合計）+550MB。各バリアントとも対応LSPバイナリの実行可能性と`claude plugin list`でのプラグイン有効化を確認済み
- **要注意（開発時のはまりどころ）**: `orchestrator/`はDockerイメージのビルド時に`COPY`で焼き込まれ、コンテナ起動後に中身が変わることはない。オーケストレーター（`investigate_plan_graph.py`・`implement_review_graph.py`）のコードを変更した後にDockerでの実機テストを行う場合、`masuda-loop:latest`（base）と使用する言語バリアントイメージの両方を必ず再ビルドしてからコンテナを起動し直すこと。再ビルドを忘れると、古いコードのままコンテナが動き続け、新しいフェーズが一切実行されずに次のフェーズへ直行するなど、原因が分かりにくい形で不具合が出る（実機で発生・時間を要して原因究明した）

未着手: 設計ドキュメント・ADRで定義された範囲はここまでで完了。今後の拡張（横断的チェックの観点追加、他言語イメージバリアントの追加等）は都度ADRを起票して進める。

## 開発環境

- Python: `venv/`に依存関係インストール済み（`requirements.txt`）
- テスト: `pytest`
