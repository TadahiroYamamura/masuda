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
  （`:1356`）からのみ`_tag_step`が打つ——TDDモード（後述）で1ステップ内に複数commitが
  積まれてもcommit数とステップ数が一致しないため（ADR-0037）。
- 1ステップの処理順序: 実装への委譲（`_implement_step_task`、`:1502`）→
  `implementation_result.json`の自己申告状態で分岐（`done`/`needs_plan_review`/
  `build_test_failed`）→ `done`なら機械的バックストップ（次節）→ 逸脱なしなら
  trigger式途中レビュー（後述の節）→ `_finalize_step`がcommitしてtagを打つ。commit対象は
  `_committable_files`が決める——**実測（`git status`）∩（このステップの計画ファイル ∪
  承認済み逸脱）**であって、エージェントの申告ではない（ADR-0058）。`git add -A`は使わない。
- `_implement_step_task`が生成するプロンプトは、このステップの`files`一覧・
  `_render_plan_text()`によるプラン全体の参考情報・逸脱時の自己申告手順（ADR-0010）・
  ビルド/テスト自己修正ループの手順（ADR-0009、最大3回）・完了条件
  （`_implementation_completion_section`: commitメッセージファイルと`{"status":"done"}`を
  書き出す。変更ファイルの申告は求めない——commit対象は実測から決まるため）を含む。
- ビルド/テスト自己修正ループの節には`_PRIVILEGED_COMMAND_SECTION`が続く。root権限や
  Dockerデーモンを要するテストはこのVMでは動かないため、宣言・承認済みの特権コマンドを
  `run_privileged_command`で実行するか、それが無ければ自己修正ループを空回りさせずに
  `build_test_failed`で人間の承認が要る旨を報告する、という指示（`docs/design/privileged-commands.md`）。
  TDDモード（`_tdd_self_verify_section`）の3フェーズすべてと、レビュー指摘の修正プロンプトにも
  同じ節が入る。
- `_finalize_step`の後、`detect_phase`をゼロから呼び直して次のフェーズを再導出する
  （commit/tagという実際のgit状態を再度読むだけで、次に見るべきステップが1つ進む）。

## TDD Red/Green/Refactorサブループ

- `plan/steps.json`の対象ステップが`"mode": "tdd"`を持つ場合（Issue #3、ADR-0035が
  対象を「新機能の追加」カテゴリに限定、ADR-0037が本節の機構を決定）、通常の単発実装
  ではなく`_detect_tdd_phase`（`:949`）がRed→Green→Refactorのサブループを駆動する。
  このサブループはステップの実装だけでなく、そのステップの`needs_plan_review`/
  `build_test_failed`自己申告処理も内包する（`redo_phase`が`tdd_red`等になる点だけが
  通常ステップと異なる）。
- フェーズ遷移は`_advance_tdd_cycle`（`:853`）がオーケストレーター側で強制する。
  Red→GreenとGreen→Refactorは常にオーケストレーターが決め、実装エージェントの判断には
  委ねない。Refactor後の遷移（もう一段Refactorする/次サイクルのRedへ進む/ステップ完了）
  だけが実装エージェントの自己申告`tdd_next_phase`に従う。
- 各フェーズは`_tdd_phase_task`（`:1655`）が生成するプロンプトで実装エージェントに
  委譲される。フェーズ別のcriteria文言（`_TDD_RED_CRITERION`/`_TDD_GREEN_CRITERION`/
  `_TDD_REFACTOR_CRITERION`）と完了条件（`_tdd_completion_section`）を持つ。
- 実装エージェントの後、フェーズごとに専用の`tdd_process_check`サブエージェント
  （`_tdd_process_check_task`）を挟む。判定対象はTDDの三原則（法則1〜3）と
  1振る舞い1テストの粒度のみで、実装の質・十分性は対象外。`ok:false`が
  `MAX_REVIEW_RETRIES`（=2）回続くと`_resolve_tdd_process_reopen`が次節のplan gate
  再オープンへ合流する。
- 1フェーズ＝1commit（`_land_or_finalize_tdd_phase`）。ただしサイクルを完了させる最後の
  フェーズだけは、機械的バックストップの通過（`_detect_tdd_step_completion`）まで
  commitを意図的に遅延させる。Refactorフェーズで改善の余地が無かった場合
  （`_committable_files`が空＝スコープ内が実際に無変更）はcheckerを経由せず直接次の分岐に
  進み、commitも行わない。無変更かどうかは申告ではなく実測で判定する（ADR-0058）——
  申告だけでcheckerを飛ばせると、実際には変更しているのに素通りできてしまう。

  各フェーズのcommitも`_committable_files`で絞るため、計画外のファイルはフェーズcommitに
  載らず作業ツリーに残る。ステップ完了時のバックストップがそれを見て逸脱として扱う。
- ステップ完了時（`tdd_next_phase: "complete"`）は`_detect_tdd_step_completion`
  （`:1042`）が、前ステップのtagを基準にした機械的バックストップ
  （`since_ref=_step_diff_base(step_index)`、`_actual_changed_files`が作業ツリーと
  既commit分の両方をunionして見る）を行ってから、通常ステップと同じ
  `_detect_interim_review_phase`に合流する。
- 却下されたTDDステップは`_reset_tdd_step`（`:998`）が
  `git reset --hard <前ステップのtag>`で中間commit群と未commit差分をまとめて巻き戻し、
  `tdd_red`・cycle 1・attempt 1から再開する。ローカルクローン内に閉じた未push commitのみ
  対象で、このコードベース唯一の破壊的git操作（ADR-0037）。**未追跡ファイルは残る**
  ——巻き戻すのはこの試行の履歴であって作業ツリーの掃除ではないため。計画外ファイルは
  そもそもcommitされていないので、ここに残る側になる。

## 機械的バックストップ・plan gate再オープン

- `_mechanical_deviation`（`:532`）がADR-0010のバックストップ本体。
  `git status --porcelain --untracked-files=all`（TDDステップは`since_ref`指定で
  commit済み差分も追加でunion）を計画済み`files`集合と突き合わせ、計画外ファイルが
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
  `_resolve_tdd_process_reopen`、`_resolve_tdd_finalization_reopen`
  （TDDステップ固有の2箇所）、`_resolve_interim_unresolved_reopen`（次節）。
- `_resolve_gate_reopen`は3状態を扱う: (1) `DEVIATION_KEY`未設定＝初回検知——
  `plan_reopened`フェーズへ（`write_task_md`が`DEVIATION_KEY`を書いてゲートを開く）、
  (2) マーカー未着——同じ理由を出し続けて待機、(3) 解決済み——承認なら`on_approved()`、却下なら
  `on_rejected(feedback)`を呼ぶ。この2つのコールバックは呼び出し元ごとに「何をもって
  approved/rejectedとするか」が異なる（例: 機械的逸脱の承認は「そのまま次のフェーズへ
  進む」、TDDステップ最終化の却下は「`_reset_tdd_step`で巻き戻してRedから再開」）。
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
  （TDDモードのステップなら`_reset_tdd_step`も併せて）`implement_step`または
  `tdd_red`からそのステップを再実行する。
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
  どちらの文脈で扱うか分岐する。TDDモードのステップ判定（`steps[completed].get("mode")
  == "tdd"`）は`in_g2_redo`のときは行われない——G2却下時の再実装は常に非TDD経路として
  扱う。
- クリーンな完了は`_finalize_g2_redo`が担う。`_finalize_step`と同様に`_committable_files`で
  commitするが、スコープはプラン全体の`files`のunionで、加えて`REVIEW_FEEDBACK_KEY`を削除し、
  Review段階を最初の観点からやり直す状態に戻す。

## 既知の問題

未調査。修正時はここから消す。

（現在なし）
