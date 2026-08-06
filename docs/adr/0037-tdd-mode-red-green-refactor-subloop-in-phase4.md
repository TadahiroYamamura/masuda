# ADR-0037: TDDモード（Red/Green/Refactorサブループ）をフェーズ4に追加する

## Status

Accepted (2026-08-06)

## Context

Issue #3は「新機能追加ではTDD（Red→Green→Refactor）で実装してほしい、バグ修正の既存フローは変更しない」という要望から出発した。[[0035-four-task-categories-scope-tdd-to-new-features]]で、masudaが扱うタスクを検証可能性・解法既知性・リスクの3軸で4カテゴリに分類し、TDDモードのスコープを「新機能の追加」カテゴリに限定した。本ADRは、そのスコープを踏まえた実際の実装機構を決定する。

[[0027-phase4-step-based-implement-review-commit-loop]]は、フェーズ4を「PLAN.mdのステップ単位でimplement→バックストップ→トリガー式軽量レビュー→commit」というループにする設計を確立した。この設計は暗黙に「1ステップ＝1commit」を前提にしている（`_completed_step_count()`が`git rev-list --count <base_ref>..HEAD`でステップ位置を導出する）。

TDDでは、この前提が成立しない。Kent Beckの実践では、Refactorフェーズは0回以上繰り返され、さらに1つのプランステップの中に複数回のRed→Green→Refactorサイクルが含まれる（例: 「ログイン機能を実装する」という1ステップが、複数の小さな振る舞いをそれぞれRed→Green→Refactorで積み上げて完成する）。したがって1ステップのcommit数は可変であり、[[0027-phase4-step-based-implement-review-commit-loop]]の「commit数＝ステップ数」という前提と真っ向から対立する。以下、この対立を解消するために必要になった一連の関連する決定を1本のADRにまとめる（[[0027-phase4-step-based-implement-review-commit-loop]]自身が「ステップ位置追跡・トリガー判定・エスカレーション・commit範囲限定・ITERATION_BUDGET再設計」という複数のサブ決定を1本に束ねているのと同じ形式）。

## Decision

### ステップ境界の追跡: git commit数からgit tagへ

`_completed_step_count()`（`orchestrator/implement_review_graph.py`）を、`git rev-list --count`ではなく`masuda-step-<workspace-id>-<N>`という名前のgit tagの数（`base_ref..HEAD`の範囲にscope）に置き換えた。このtagは、ステップが完全に完了した時点でオーケストレーターが1回だけ打つ（`_tag_step`、呼び出しは`_finalize_step`・`_detect_tdd_step_completion`の成功パスのみ）。TDDモードでステップ内に何commit積まれようと、tagが打たれるまでは未完了として扱われる。

tag名をworkspace-idでスコープしたのは、`git clone --local`（[[0018-git-clone-local-over-linked-worktree]]）がリポジトリの既存tagをすべて新規ワークスペースのクローンへ複製するため、workspace-idを含めないと過去の別タスクのtagと名前が衝突・誤カウントされうるためである。さらに`internal/worktree.Merge`/`Pull`の`git fetch`はどちらも`--no-tags`を指定していないため、tagのauto-follow（fetch対象のcommitに付いたtagを既定で追従する挙動）により、マージ/pull済みのワークスペースのtagがrepoRoot側に残存しうる。この対策として、`masuda workspace remove`と`masuda review approve`の後片付けの両方が呼ぶ唯一の削除経路である`internal/worktree.Remove`に`removeLeakedStepTags`を追加し、削除時にrepoRoot側の該当workspace-idのtagを掃除するようにした。`_completed_step_count()`自体のカウントも`base_ref..HEAD`の範囲にscopeしており（bareな`git tag --merged HEAD`ではない）、これはさらなる保険として範囲外のtagを除外する。

### 機械的バックストップ・レビューdiffの基準を「前ステップのtagから」に一般化

TDDステップは各フェーズ（Red/Green/Refactor）ごとに実commitが積まれるため、ステップ完了（finalize）時点では作業ツリーはclean・`git diff HEAD`も空になる。このままでは[[0010-plan-deviation-reopens-plan-gate]]の機械的バックストップがTDDステップに対して常に無反応になってしまう——設計上必須の派生課題として実装中に見つかった。

`_actual_changed_files`・`_extra_changed_files`・`_mechanical_deviation`・`_compute_step_diff`にオプショナルな`since_ref`引数を追加した（デフォルト`None`で非TDDステップの挙動は完全に無変更）。TDDステップの場合、`_step_diff_base(step_index)`（前ステップのtag、最初のステップなら`base_ref`）を`since_ref`として渡し、`git status --porcelain`（作業ツリー）と`git diff --name-only <since_ref> HEAD`（既にcommit済みの差分）を合算した集合を「実際に変更されたファイル」として扱う。

サイクルを完了させる最後のフェーズのcommit自体は、機械的バックストップが通過するまで意図的に遅延させる（`_land_or_finalize_tdd_phase`→`_detect_tdd_step_completion`）。先にcommitしてしまうと、バックストップが逸脱を検知してG1ゲートを再オープンした際、`IMPLEMENTATION_RESULT_JSON`（実装エージェントの自己申告ファイル）を既に消費済みになり、次の`detect_phase`呼び出しで「G1再オープンがpending中」という状態を副作用なく再導出できなくなる（実装中に見つかった再入可能性のバグ）。既存の非TDDステップ向け`_resolve_mechanical_reopen`は、人間の決定（承認/却下）が下るまで自己申告ファイルを消費せずに残すことで、`detect_phase`が呼ばれるたびに同じ判定を再導出できる設計になっている。TDDステップの最後のフェーズもこの原則に揃え、`_actual_changed_files(since_ref)`が作業ツリー（未commitのままの最後のフェーズの差分）と既存commit（それ以前のフェーズ）の両方を見られることを利用して、commit自体をバックストップ通過後まで遅延させることで整合性を保った。

### `--tdd`はプランナーへの助言シグナルに留め、ステップ単位の適用可否はプランナー判断＋G1レビューに委ねる

`masuda plan start --tdd`という新規フラグを追加した（`cmd/masuda/plan.go`、ワークスペース作成時に`internal/hostloop.WriteTDDIntent`で状態ディレクトリへ書き込み、`orchestrator/investigate_plan_graph.py`の`TDD_REQUESTED_MARKER`として読まれる）。このフラグは「このタスクではTDDを使ってほしい」という人間の意図表明に過ぎず、コード上で強制するゲートではない。`plan/steps.json`の各ステップに`"mode": "tdd"`を付けるかどうかの実際の判断はプランナーサブエージェントに委ね、その判断の正しさはG1（人間のプランレビュー）が担保する——ステップ分解・ファイル一覧など、プランナーの他の判断と同じ扱いである。`internal/gate/gate.go`の`planStep.Mode`フィールドが`masuda plan show`でこの選択を「（TDDモード）」として表示する。

### TDD process checkerは14観点システムとは別の狭い機構とし、実装の質・十分性は問わない

Red/Green/Refactorの各フェーズについて、実装エージェントの後に軽量な「TDDプロセス遵守チェック」専用のサブエージェント（`_tdd_process_check_task`）を挟み、承認されたらそのフェーズだけをオーケストレーターがcommitする（`_detect_tdd_phase`）。このチェックはRobert C. Martinの「TDDの三原則」（失敗するテストを書くまでプロダクションコードを書かない／失敗するのに十分な量以上のテストを書かない／今失敗しているテストを通す以上のプロダクションコードを書かない）と、1つのRedで複数の振る舞いを一度にテストしていないかという粒度逸脱のみを判定する。「このテストの筋が良いか」「この実装は綺麗か」「リファクタリングとして十分か」といった実装の質・十分性の判断は対象外とし、実装エージェント自身の判断に委ねる。

### Green→Refactorの遷移をオーケストレーターが強制する

`_advance_tdd_cycle`は、Redの次はGreenへ（TDDの法則1が要求する唯一の合法な行動であり判断の余地がない）、**Greenの次は必ずRefactorへ**（実装エージェントの自己申告`tdd_next_phase`には委ねない）と、両方の遷移をオーケストレーター側で決め打つ。1サイクルにつき最低1回、Refactorフェーズへの移行そのものを強制する。ただし移行後に実装エージェントが「改善の余地なし」と判断した場合は`changed_files: []`で自己申告させ、この場合はcommitさせず・process checkerも呼ばずに直接次の分岐（次サイクルのRed、またはステップ完了）へ進む——Refactorへの移行そのものは強制するが、無意味なcommitまでは強制しない。

Refactor後の分岐（もう一段階リファクタリングするか、次のサイクルへ進むか、ステップ完了か）だけが実装エージェントの自己申告（`tdd_next_phase`）に委ねられる、正当な実装判断である。

### finalize時バックストップが却下された場合、中間commit群を`reset --hard`で前ステップの境界まで巻き戻す

TDDステップはサイクル完了までに複数の実commitを積む。機械的バックストップが逸脱を検知しG1が再オープンされ、人間が却下した場合、`_reset_tdd_step`が`git reset --hard <前ステップのtag>`を実行し、そのステップの中間commit群と未commitの最後のフェーズの差分を両方まとめて巻き戻し、`tdd_red`・cycle 1・attempt 1から再開する。

## Alternatives Considered

- **commitメッセージにステップ境界のtrailerを埋め込む方式**: Refactorフェーズは0回以上繰り返される開放的なカーディナリティを持つため、あるcommitを作る時点で「これが本当に最後のcommitか」を確信できない。後から「もう1回リファクタリングが必要だった」となった場合、既にtrailerを付けたcommitからtrailerを剥がすには履歴の書き換え（amend/rebase）が要り、immutableなcommitの性質と相性が悪い。tagは単なるポインタなので、ステップ完了と判断した瞬間に一度だけ打てばよく、この問題が生じない
- **`plan/steps.json`に各ステップの想定commit数を事前宣言させ、累積オフセットと突き合わせる方式**: 実際のRed/Green/Refactorサイクルが必ず宣言通りのcommit数になるとは限らない（リファクタリング不要で早く終わる、テストがなかなか安定せず複数回書き直す等）ため不採用
- **プランエージェントがタスク内容から新機能/バグ修正を自動判別し、`--tdd`のような明示指定を持たない**: バグ修正か新機能追加かの判別はしばしば曖昧で、自動判別の誤りが「無駄なTDD強制」または「TDDを期待したのに通常フロー」という形で表面化するリスクがあった。`plan/steps.json`はステップ単位のデータ構造（[[0026-plan-md-as-prose-plus-per-step-json]]）であり、1つのタスクの中に複数種類のステップが混在しうるため「タスク全体がTDDかどうか」という単純な二択にもできなかった
- **既存の`.masuda/reviews/*.md`駆動の14観点システムに15番目の観点として追加する**: 14観点はdiffの内容（バグ・命名・テスト漏れ等）を機械的にチェックするためのものであり、TDDのプロセス（手順の順序・粒度）を判定するのとは性質が異なる。14観点はプロジェクトごとにカスタマイズ可能な設定だが、TDDプロセスの遵守チェックはTDDモードを使う限り常に同じ基準で行うべきものであり、混在させるべきではないと判断した
- **process checkerが却下した際、[[0004-checker-fixer-role-separation]]のchecker/fixer分離パターンに倣い、別のfixerエージェントに修正させる**: あの分離は「既にcommitされた変更」への指摘を独立した視点を保ったまま直すためのものだった。TDDのRGR各フェーズはまだcommit前の作業中のもので、性質としては[[0009-implementation-self-verification-loop]]のビルド/テスト自己修正ループや[[0027-phase4-step-based-implement-review-commit-loop]]の却下時再実行（同じ役割をfeedback付きで再実行）に近いと判断し、同じ実装エージェントロールを新規コンテキストでfeedback付きに再実行する方式にした
- **Green→Refactorの遷移も実装エージェントの自己申告に委ねる**: 参考に確認した既存のClaude Codeプラグイン`claudecode-tdd`の`/tdd:red`・`/tdd:green`・`/tdd:refactor`スラッシュコマンドは、Structural-only・Tidy First・重複除去優先といった内容面のガイドラインは充実している一方、フェーズ遷移自体はスラッシュコマンドの連鎖任せで「満足したら次は`/tdd:red`へ」と自己申告のみに委ねており、Refactorへの移行を強制する仕組みを持たない。エージェントは一般にRefactorフェーズを飛ばしがちであり、これはまさにその失敗モードそのものだったため、masudaはこの点でプラグインの方式を踏襲せず、オーケストレーター（LLMを介さない決定的なコード）側で構造的に強制する設計にした。ガイドラインの文言（`_TDD_REFACTOR_CRITERION`）自体は参考にした
- **finalize時バックストップ却下時、中間commitはそのまま残しfeedbackを添えて同じサイクル位置から再開する**: 非破壊的だが、却下された変更が履歴に混在し続け、機械的バックストップが次回の判定でも同じ逸脱を再検知する可能性が残る

## Consequences

- `git reset --hard`はこのコードベースで唯一の破壊的git操作になった。サンドボックス内のローカルクローンに閉じた、まだ一切pushされていないcommitのみが対象であり（[[0005-manual-push-automatic-worktree-ops]]の「pushは常に手動」の原則により、この時点のcommitは他の誰にも影響していない）、共有履歴に影響しないため許容できると判断したが、この一点においてこのコードベースの「破壊的git操作を避ける」という一般原則から意図的に外れている
- [[0011-iteration-budget-per-subagent-invocation]]・[[0027-phase4-step-based-implement-review-commit-loop]]の`ITERATION_BUDGET`/`PER_STEP_BUDGET`は、TDDサイクルの増加（1ステップが実装1回ではなく複数回のRed/Green/Refactor往復になりうる）を考慮しておらず、本ADRでは未調整のまま残した。新フェーズ名（`tdd_red`・`tdd_green`・`tdd_refactor`・`tdd_process_check`）は`_SUBAGENT_PHASES`に加え既存の予算カウント機構には乗せたが、予算式自体の再設計は別途必要になる
- [[0035-four-task-categories-scope-tdd-to-new-features]]の「パフォーマンス改善」カテゴリは引き続き本ADRのスコープ外
- tag自体の恒久的なクリーンアップ（`removeLeakedStepTags`）はbest-effortであり、workspace-idスコープが正常に機能していれば通常は不要になる保険的な後始末に過ぎない
- TDDモードのステップは、既存の非TDDステップと比べて大幅に多くのサブエージェント呼び出し（フェーズごとの実装＋process check）とcommitを生成するため、`git log`上の履歴が細かくなる。人間がレビューする際の粒度がステップ単位から個々のRed/Green/Refactorコミット単位に変わる
