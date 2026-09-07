# Build（実装）

Build段階は`orchestrator/implement_review_graph.py`（**ホスト上で動く**Pure state machine、
LLM呼び出しは一切行わない）が、`plan/steps.json`（ADR-0026）のステップを1つずつ
処理する。`detect_phase`がファイルシステム/状態デーモンの状態からフェーズを判定し、
`write_task_md`がそのフェーズ向けのTASK.mdを書き出してサブエージェントへの委譲内容を
決める、という2ノードのLangGraphを1回のオーケストレーター起動ごとに1往復させる構成。

実装するセッションはサンドボックスVMの中にいて、curated MCPの`next_task`ツールで
このオーケストレーターを1回進め、タスク本文を戻り値で受け取る（ADR-0057）。ホスト側の
実体は`cmd/masuda/statedaemon.go`の`orchestratorRunner`——worktreeをcwd、ワークスペースの
状態ディレクトリを`MASUDA_STATE_DIR`として`implement_review_graph.py`を1回実行し、
書かれた`TASK.md`を読んで返す。gitコマンド（ステップcommit・タグ・diff・`git status`に
よる機械的バックストップ）はすべてこのホスト側プロセスが、VMと共有している同じworktreeに
対して実行する。

**プロンプトに埋め込むパスだけはゲスト視点に読み替える。** `MASUDA_STATE_DIR`はホストの
パスだが、指示を受け取るセッションが開けるのはVM内の`/masuda-state`である。
`GUEST_STATE_DIR`（`MASUDA_GUEST_STATE_DIR`環境変数、`internal/sandbox.GuestStateDir`が
渡す）と`_agent_path()`が先頭のプレフィックスだけを差し替える。読み替えるのは
プロンプトへ描画するパスだけで、このスクリプト自身が開くパスは常にホスト側のままである。
シェル変数の形（`$MASUDA_STATE_DIR/...`）でプロンプトに書かない理由は、受け手がLLMであり、
Write/Editツールがリテラルのパスしか取らないうえ、レビュー観点のサブエージェントのように
Bashを持たない相手が展開できないため。

全ステップがcommit済みになると自動的にReview段階（G2）へ合流する
（`_detect_post_implementation_phase`）。review/check/fix/recheckのエンジン自体・観点
frontmatterの動的ロード・ゲートマーカーの読み書きは本ファイルの対象外
（それぞれ`docs/design/review.md`・`docs/design/gates.md`が扱う）。予算
（`ITERATION_BUDGET`）はステップ数に応じて動的に計算されるとだけ触れる
（計算式の詳細は`docs/design/pipeline.md`）。

## Buildステップ実行ループ

- `detect_phase`（`implement_review_graph.py:1387`）がエントリポイント。現在のステップ
  indexは`_completed_step_count()`（`:409-436`）が返す、既に完了したステップ数
  `steps[_completed_step_count()]`として決まる。
- ステップ位置の判定は`git rev-list --count`ではなく、git tag
  `masuda-step-<workspace-id>-<N>`（`_step_tag_name`）の数を使う。`base_ref..HEAD`の範囲に
  scopeしているのは、`git clone --local`（ADR-0018）が既存tagを複製するため、workspace-id
  でスコープしても別タスクのtagが範囲的に混入しうる余地への保険。tagは`_finalize_step`
  からのみ`_tag_step`が打つ——スコープ内が無変更のステップは空commitになるなど、
  commit数とステップ数は一般には一致しないため。
- 1ステップの処理順序: 実装への委譲（`_implement_step_task`、`:1320`）→
  `implementation_result.json`の自己申告状態で分岐（`done`/`needs_plan_review`/
  `build_test_failed`）→ `done`なら機械的バックストップ（次節）→ 逸脱なしなら
  trigger式途中レビュー（後述の節）→ `_finalize_step`がcommitしてtagを打つ。commit対象は
  `_committable_files`が決める——**実測（`git status`）∩（このステップの計画ファイル ∪
  承認済み逸脱）**であって、エージェントの申告ではない（ADR-0058）。`git add -A`は使わない。
- `_implement_step_task`が生成するプロンプトは、依存解決とLSPの利用方法を指示する
  共有節`_LSP_AND_DEPENDENCY_SECTION`（利用可能ならClaude Code純正のLSPツールを使う、
  LSPが正しく機能するには依存解決が必要な場合があり未セットアップならCLAUDE.md・
  README等を参照して行う、外部ネットワークに阻まれた場合は再試行せずLSP無しで
  進める、という3点）に加えて、このステップの`files`一覧・`_render_plan_text()`による
  プラン全体の参考情報・逸脱時の自己申告手順（ADR-0010）・ビルド/テスト自己修正
  ループの手順（ADR-0009、最大3回）・完了条件（`_implementation_completion_section`:
  commitメッセージファイルと`{"status":"done"}`を書き出す。変更ファイルの申告は
  求めない——commit対象は実測から決まるため）を含む。同じ共有節は、G2却下時のredo
  （後述、`_implement_g2_redo_task`）が生成するプロンプトにも含まれる。
- ビルド/テスト自己修正ループの節には`_PRIVILEGED_COMMAND_SECTION`が続く。root権限や
  Dockerデーモンを要するテストはこのVMでは動かないため、宣言・承認済みの特権コマンドを
  `run_privileged_command`で実行するか、それが無ければ自己修正ループを空回りさせずに
  `build_test_failed`で人間の承認が要る旨を報告する、という指示（`docs/design/privileged-commands.md`）。
  レビュー指摘の修正プロンプトにも同じ節が入る。
- `_finalize_step`の後、`detect_phase`をゼロから呼び直して次のフェーズを再導出する
  （commit/tagという実際のgit状態を再度読むだけで、次に見るべきステップが1つ進む）。

## 機械的バックストップ・plan gate再オープン

- `_mechanical_deviation`（`:532`）がADR-0010のバックストップ本体。
  `git status --porcelain --untracked-files=all`を計画済み`files`集合と突き合わせ、計画外ファイルが
  あれば逸脱理由の文字列を返す。commit範囲を決める`_committable_files`とは同じ実測を
  見ているが役割が違う——こちらは「承認された範囲の外に出たか」を人間に上げるための判定、
  向こうは「承認された範囲のうち何をcommitするか」の決定（ADR-0058）。
- 逸脱判定から除外されるのは2種類: (a) 過去にこのgate再オープンで承認済みの逸脱
  （`APPROVED_DEVIATIONS_KEY`、`_read_approved_deviations`/`_write_approved_deviations`）、
  (b) `plan/steps.json`の`expected_byproducts`にマッチするファイル
  （`_is_expected_byproduct`、`_glob_to_regex`が標準globセマンティクス——`*`は`/`を
  跨がず`**`は跨ぐ——でマッチ、ADR-0028）。
- gate再オープンの解決ロジックは`_resolve_gate_reopen`（`:1146`）に共通化されている。
  呼び出し元は5箇所: `_resolve_self_report_reopen`（自己申告の逸脱・ビルド/テスト
  失敗）、`_resolve_mechanical_reopen`（`:1255`、機械的検知の逸脱）、
  `_resolve_interim_unresolved_reopen`（次節）。
- `_resolve_gate_reopen`は3状態を扱う: (1) `DEVIATION_KEY`未設定＝初回検知——
  `plan_reopened`フェーズへ（`write_task_md`が`DEVIATION_KEY`を書いてゲートを開く）、
  (2) マーカー未着——同じ理由を出し続けて待機、(3) 解決済み——承認なら`on_approved()`、却下なら
  `on_rejected(feedback)`を呼ぶ。この2つのコールバックは呼び出し元ごとに「何をもって
  approved/rejectedとするか」が異なる（例: 機械的逸脱の承認は「そのまま次のフェーズへ
  進む」）。
  `DEVIATION_KEY`と`PLAN_GATE_KEY`の消費はこの解決タイミングでのみ、1回の
  `state_apply`で同時に行う。逆に言えば、決定を書く側（`masuda plan approve`等）は
  `DEVIATION_KEY`に触ってはならない——触ると(3)ではなく(1)と判定され、ゲートが
  開き直される（ADR-0055、`internal/gate`の
  `TestDecisionsLeaveTheDeviationForItsConsumer`が固定している）。
- 承認された機械的逸脱は`APPROVED_DEVIATIONS_KEY`に積まれ、以後の同じファイルへの
  逸脱を再検知しない。

## trigger式途中レビュー

- `_detect_interim_review_phase`（`:1314`）がステップのdiffに対する軽量レビューを
  駆動する。`trigger`をfrontmatterに持つ観点（`TRIGGERED_PERSPECTIVE_IDS`）が1つも
  なければ、判定自体をスキップして直接`_finalize_step`へ進む。
- トリガー判定はステップごとに1回のバッチ呼び出し（`_trigger_match_task`、`:2061`）。
  `trigger`付き全観点の一覧とそのステップのdiffを1つのサブエージェント呼び出しに渡し、
  該当する観点idの配列を`trigger_match.json`へ書かせる（観点ごとの個別呼び出しはしない、
  ADR-0027）。
- 該当した観点は、Review段階と同じreview/check/fix/recheckエンジン
  （`_advance_and_next_task`、詳細は`docs/design/review.md`）を、
  `results_dir=_interim_step_dir(step_index)`（`interim_review/step{N}/`、
  `review_results/`とは別ディレクトリ）に向けて再利用して解決する。往復回数の上限
  （`MAX_REVIEW_RETRIES`=2）も共通。
- 自動修正で収束しない指摘は`_resolve_interim_unresolved_reopen`（`:1277`）が
  plan gate再オープンへ合流する（専用のエスカレーション体系が未着手なための暫定措置、
  ADR-0027。triageゲート・ADR-0029とは別経路）。承認→指摘を
  `INTERIM_CARRIED_FINDINGS_KEY`に積んでそのステップをそのままcommit、却下→
  `_clear_interim_step`でこのステップの途中レビュー状態を消し、
  `implement_step`からそのステップを再実行する。
- 承認された持ち越し指摘は、Review段階のsynthesizeが生成する最終レポートに「途中
  レビューで持ち越された指摘」セクションとして反映される（`_interim_carried_section`、
  詳細は`docs/design/review.md`）。

## G2却下時のredo

- `_detect_post_implementation_phase`（`:1113`）が、全ステップcommit済み後のReview段階
  進行を判定する。`REVIEW_GATE_KEY`のstatusが`rejected`なら、review状態を
  `_clear_review_state()`で消去し、`REVIEW_FEEDBACK_KEY`に却下理由を書いて
  `implement_g2_redo`フェーズへ遷移する（ADR-0013）。
- `implement_g2_redo`はステップ機構を経由しない単発の再実装パス
  （`_implement_g2_redo_task`、`:1747`）。全ステップが既にcommit済みで「次のステップ」が
  存在しないため、プラン全体スコープで却下フィードバックへの対応を1回のサブエージェント
  呼び出しに委譲する（ADR-0027）。
- `implement_g2_redo`中の機械的バックストップは、単一ステップの`files`ではなく全ステップ
  の`files`のunion（`_all_planned_files`）と突き合わせる。逸脱時は同じ
  `_resolve_mechanical_reopen`に合流するが、`redo_phase`は`implement_g2_redo`になる。
- `detect_phase`は`REVIEW_FEEDBACK_KEY`の有無（`in_g2_redo`）で、
  `implementation_result.json`の自己申告処理をimplement_step用/implement_g2_redo用の
  どちらの文脈で扱うか分岐する。
- クリーンな完了は`_finalize_g2_redo`が担う。`_finalize_step`と同様に`_committable_files`で
  commitするが、スコープはプラン全体の`files`のunionで、加えて`REVIEW_FEEDBACK_KEY`を削除し、
  Review段階を最初の観点からやり直す状態に戻す。

## 既知の問題

未調査。修正時はここから消す。

（現在なし）
