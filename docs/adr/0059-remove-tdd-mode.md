# ADR-0059: TDDモード（Red/Green/Refactorサブループ）をBuild段階から削除する

## Status

Accepted (2026-09-04)

## Context

[[0037-tdd-mode-red-green-refactor-subloop-in-phase4]]がBuild段階に導入したTDDモードは、`plan/steps.json`のステップに`"mode": "tdd"`が付いているとき、通常の単発implementの代わりにRed→Green→Refactorのサブループを駆動する機構である。動機は決定論に寄せることにあった——[[0002-workflow-orchestrator-with-subagent-delegation]]の構図では実装の進め方はエージェントの判断に委ねられるが、TDDの三原則は機械的に検証可能な手順であり、オーケストレーター側でフェーズ遷移を強制できる。外部プラグイン`claudecode-tdd`のスラッシュコマンドを参照点として確認したところ、あちらのフェーズ遷移は指示文であって強制する仕組みが無く、そこがmasuda側に実装した理由だった。

導入から1か月が経ち、状況が変わった。

- masudaはまだv0.1に達していない。合格基準は「dogfoodingが再開できる」ことで、パイプラインを実機で1周通すことすらできていない
- TDDモードは`implement_review_graph.py`の**409行（全体の15%）**を占め、加えて`since_ref`というオプション引数を`_actual_changed_files`・`_extra_changed_files`・`_mechanical_deviation`・`_compute_step_diff`の4関数に染み出させていた（26箇所）。これは「1ステップ＝複数commit」という、TDDモードだけが持つ性質のために存在していた
- このコードベース唯一の破壊的git操作（`_reset_tdd_step`の`git reset --hard`）もTDDモード専用だった
- [[0058-commit-scope-measured-from-git-not-self-reported]]の実装中に、TDDステップの途中レビューが往復すると`detect_phase`が`tdd_refactor`へ戻ってfixループが完了しないバグ（GitHub Issue #48）が見つかった。TDDモードは実際に使われた形跡がなく、このバグも誰にも踏まれていなかった

決定論に寄せたいという動機自体は今も正しい。ただ、v0.1に達していない段階で、実際には一度も回っていない機能のために機構の15%と唯一の破壊的操作を抱え続けることは、プロジェクトを不必要に複雑にしている。

## Decision

TDDモードを削除する。`plan/steps.json`の`mode`フィールドも予約せず完全に消す。

- `orchestrator/implement_review_graph.py`: TDD関数17個、サイクル状態のキーと定数、`mode == "tdd"`分岐、`tdd_*`フェーズのdispatchと予算計上、フェーズ別プロンプト。あわせて`since_ref`引数と`_step_diff_base`も削除する——TDDモードが無ければ、1ステップの差分は常に「現在未commitのもの」である
- `orchestrator/investigate_plan_graph.py`: プランナーへのTDD節と`"mode": "tdd"`のスキーマ例、`TDD_REQUESTED_KEY`
- Go側: `masuda plan start --tdd`、`hostloop.WriteTDDIntent`と`internal:tdd-requested`キー、`internal/gate`の`planStep.Mode`と`（TDDモード）`表示
- [[0035-four-task-categories-scope-tdd-to-new-features]]が定めた「TDDは新機能追加カテゴリに限定」という運用上の結論は失効する。4カテゴリの分類自体はmasudaが何を得意とするかの整理として有効で、元々コードには実装されていないため、そちらは残す

再導入する場合に必要になるものを、ここに書き残しておく。プラグイン（`github.com/TadahiroYamamura/claudecode-tdd`）側で実現するなら、**フェーズ遷移の強制**（現状は指示文のみ）に加えて、**サブエージェントへのskill継承**が要る——フェーズ4のサブエージェントはVM内の新規コンテキストで動くため、メインセッションにインストールされたプラグインを継承しない。rootfsイメージに焼き込めば渡せるが、それは対象リポジトリ依存の条件になる。masuda側に再実装するなら、本ADRが削除したものがそのまま必要量の目安になる。

## Alternatives Considered

- **残したままIssue #48だけ直す**: 発火条件（TDDステップ＋`trigger`宣言のある観点＋実際に指摘が出る）が揃わないと踏まないので、直さなくてもv0.1には影響しない。だが直しても「使われていない機能の15%」は残る。修正には「TDDサイクルは終わったが途中レビューは終わっていない」という状態を新たに表現する必要があり、複雑さはむしろ増える
- **`mode`フィールドだけ予約として残す**: 将来ステップ単位のモード分岐を入れる余地は残せる。だが使い手のいないフィールドはプランナーが埋めようとする余地になり、G1で人間が見る情報も増える。必要になった時点で足すほうがよい
- **プラグイン側へ移してから削除する**: 順序としては安全だが、プラグイン側にフェーズ遷移の強制を実装する作業がv0.1の前に挟まる。決定論の担保を一時的に手放してでも、v0.1到達を優先すると判断した

## Consequences

- `implement_review_graph.py`が2606行から2040行へ減った（−22%）。`since_ref`という「1ステップが複数commitを持ちうる」前提が消え、ステップの差分は常に未commit分という単純な定義に戻った
- このコードベースから破壊的git操作（`git reset --hard`）が無くなった
- GitHub Issue #48は対象が消滅したためクローズ。`docs/design/gates.md`が抱えていた「Goの`renderPlan`とPythonの`_render_plan_text()`が`mode`の扱いで食い違う」という既知の問題も同時に解消した
- **決定論の担保を一段手放した。** 実装の進め方はエージェントの判断に戻り、masudaが機構として強制するのはプランのステップ分解・ファイル範囲（機械的バックストップ）・レビュー観点までになる
- 進行中のワークスペースで`"mode": "tdd"`を含む`plan/steps.json`があれば、そのフィールドは無視される（`_read_plan_steps`は未知のキーを読み飛ばす）。0.xで互換を約束しない方針に沿い、移行処理は用意しない
