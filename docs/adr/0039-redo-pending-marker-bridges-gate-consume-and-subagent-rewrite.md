# ADR-0039: G1/investigate redoの遷移は、ゲートマーカー消費から成果物書き換えまでを専用のpendingマーカーで橋渡しする

## Status

Accepted (2026-08-10)

- 一部改訂: [[0055-gate-marker-is-an-unconsumed-decision-waited-on-by-presence]] — plan_redoにおけるゲートマーカーの消費と`PLAN_REDO_PENDING`の書き込みは、`state_apply`により1つの原子的操作になった。pendingマーカー自体は成果物書き換えとの橋渡しとして引き続き必要

## Context

GitHub Issue #21は、`investigate_plan_graph.py`の`detect_phase`で発生した実際のバグを報告している。G1で人間がプランをreject（フィードバック付き）すると、`detect_phase`は`GATE_MARKER`をunlinkし、`plan_redo`フェーズへ遷移してplannerサブエージェントに再プランを指示する。ところがplannerが`plan/summary.md`・`plan/steps.json`を上書きする前に、[[0029-immediate-stop-escalation-dedicated-gate]]のtriageゲートによる中断（プロンプトインジェクション疑いの自己申告）が起きることがある。

`_resolve_triage`はtriage解決時、中断前に何をしていたかを保持せず`resume_phase_fn()`（`detect_phase({"phase": "", ...})`）でファイルシステム状態からゼロに再導出する（この設計自体は[[0008-investigate-plan-agent-separation-with-redo]]のredoパターンおよびこのファイル自身のdocstringが明記する前提であり、プロセスがループごとに使い捨てられても自己修復的に動く利点を持つ）。しかしplan_redoへの遷移そのものは非atomicである——`GATE_MARKER`のunlink（マーカー消費）と、plannerによる`plan/summary.md`・`plan/steps.json`の上書き（成果物の実際の更新）は1ステップではない。triageの中断がこの間（マーカーは消費済み・ファイルはまだ却下済みの古い内容のまま）に起こると、ゼロからの再導出は「`plan/summary.md`・`plan/steps.json`が存在し、`GATE_MARKER`が存在しない」という状態だけから`status`のデフォルト`"pending"`を経由して`await_g1`と誤判定する。却下済みの古いプラン内容が、あたかも修正済みであるかのように人間へ再提示され、誤って承認されうる。実際にこの手順が発生し、人間がタイムスタンプで気づいて事なきを得た実例がある。

同じファイルの`investigate_redo`遷移（`plan_result.json`が`needs_more_investigation`を返した際の、調査フェーズへの差し戻し）も、今回のADR起票にあたるコード確認で同型の非atomic構造であることを確認した。`PLAN_RESULT_JSON.unlink()`（マーカー消費）の後、investigatorサブエージェントが`INVESTIGATION.md`を書き換える前にtriage中断が起きると、`INVESTIGATION.md`は（古い内容のまま）既に存在するため、ゼロからの再導出は`investigate_redo`を経由せずいきなり`plan`フェーズへ落ちる。プランエージェントへの追加調査事項（`questions`）も握り潰され、不十分と判定された古い`INVESTIGATION.md`のままプランが作られる。

`implement_review_graph.py`側（`_resolve_self_report_reopen`・`_resolve_mechanical_reopen`・`_resolve_interim_unresolved_reopen`のいずれも、`IMPLEMENTATION_RESULT_JSON`をunlinkしてから`{"phase": redo_phase, "reason": feedback}`を返す構造）も同様のパターンを持つことを確認したが、triage中断からの再開時に失われるのは差し戻しの理由テキストのみで、`investigate_plan_graph.py`のように「却下済みの古い成果物が承認待ちとして誤って再提示される」という安全性上の問題には至らない（まだ実装がコミットされていない状態で、フィードバックなしの通常フローに静かに格下げされるだけ）。このADRは`investigate_plan_graph.py`の2つの遷移（plan_redo・investigate_redo）のみを対象とし、`implement_review_graph.py`側の同型パターンは深刻度が異なるため対象外とする（別途Issue化を検討する）。

Issue #21のコメント欄では、対処方針として以下2案を比較検討した。

- **案A**: 状態ディレクトリに「次のステップ」を表す汎用の状態ファイルを新設し、明示的に管理する
- **案B**: 本バグの範囲（ゲートマーカーのconsumeとredoサブエージェントによる成果物書き換えの間）だけを埋める、狭いスコープの「このredoは実行待ち（未完了）」マーカーを追加する

案Aのように汎用の「次はこのフェーズ」フラグへ広げると、そのフラグ自体が実態とズレるリスク（フラグは残っているがredoは実は完了していた、等）を新たに持ち込み、`detect_phase`が本来持つ「ディスク上の成果物だけが信頼できる唯一の状態源」という単一責任を薄める。triageのように任意のタイミングで割り込む機構がある以上、影響範囲をこのバグの発生源だけに絞れる案Bの方が、既存の「ゼロから再導出」という設計哲学と衝突しない。

## Decision

`investigate_plan_graph.py`の`plan_redo`・`investigate_redo`それぞれについて、ゲートマーカー消費の時点で「このredoはまだ完了していない」ことを示す専用のpendingマーカーファイルを書き込み、`detect_phase`の冒頭でその存在を最優先にチェックする。

### plan_redo

- G1 rejectを検知した時点（`GATE_MARKER`のunlinkと同時）で、却下フィードバックを`PLAN_REDO_PENDING_MD`（新設、`STATE_DIR / ".masuda-plan-redo-pending.md"`）に書き込み、かつ却下済みの古い`PLAN_SUMMARY_MD`・`PLAN_STEPS_JSON`を削除する
- `detect_phase`冒頭（triageの自己申告チェックの直後）で`PLAN_REDO_PENDING_MD`の存在を確認する
  - `PLAN_SUMMARY_MD`・`PLAN_STEPS_JSON`が両方存在する（plannerが新しい成果物を書き終えた） → redo完了。`PLAN_REDO_PENDING_MD`をunlinkし、以降の通常フローへ進む
  - 存在しない（plannerがまだ書き終えていない、あるいはtriage中断でその途中だった） → redo未完了。`PLAN_REDO_PENDING_MD`の内容をフィードバックとして`plan_redo`フェーズへ戻す

この設計では、古いプラン成果物を即座に削除することで「成果物が存在する」という状態そのものが「redoが完了した」ことの証拠になる。triageが何度中断しても、`PLAN_REDO_PENDING_MD`が存在する限り`plan_redo`への差し戻しが再導出され続ける。

### investigate_redo

同じ構造を`investigate_redo`にも適用する。`plan_result.json`が`needs_more_investigation`を返した時点（`PLAN_RESULT_JSON`のunlinkと同時）で、追加調査事項（questions）を`INVESTIGATE_REDO_PENDING_JSON`（新設、`STATE_DIR / ".masuda-investigate-redo-pending.json"`）に書き込む。ただし`investigate_redo`は`plan_redo`と異なり、`INVESTIGATION.md`自体を「未完了の印」として削除できない——investigatorは既存の`INVESTIGATION.md`に追記・修正する形で動作を続けられる設計（[[0008-investigate-plan-agent-separation-with-redo]]）であり、ゼロから再生成させる必要はない。そのため「完了」の判定は成果物の存在ではなく、専用マーカー自体の削除で行う: `_investigate_task`が生成するTASK.mdの完了条件に「`INVESTIGATE_REDO_PENDING_JSON`をunlinkすること」を明示的な作業手順として含め、investigatorサブエージェント自身に消させる。

## Alternatives Considered

- **案A（汎用の「次のステップ」フラグ）**: 状態ディレクトリに`.masuda-next-phase`のような汎用マーカーを1つ持たせ、`detect_phase`より優先して参照する案。実態とズレるリスク（フラグは残っているが実際には完了していた等）を新たに持ち込み、`detect_phase`の「ディスク上の成果物のみが信頼できる状態源」という設計原則と衝突するため不採用。バグの発生源が特定のgapに限定されている以上、汎用化する理由がない。
- **investigate_redoの完了判定も「成果物の存在」で行う（plan_redoと同型に統一する）**: `plan_redo`と同じく`INVESTIGATION.md`を削除してから差し戻す案。しかし`investigate_redo`はプランエージェントからの部分的な追加質問への回答であり、既存の調査内容全体を破棄して一から書き直させる必要はない（[[0008-investigate-plan-agent-separation-with-redo]]の設計とも矛盾する）。ファイル全体を消す代償（investigatorが既存の調査を再利用できなくなる）が、pendingマーカー方式より大きいため不採用。
- **`implement_review_graph.py`側の同型パターンも本ADRで一括対応する**: 実機調査で同じ非atomicパターンを確認したが、triage中断時に失われるのは差し戻し理由のテキストのみで、`investigate_plan_graph.py`のような「却下済み成果物の誤承認」に至る安全性上の問題はない。対応の要否・具体案は別途検討する方が、今回のバグ修正のスコープを不必要に広げずに済む。

## Consequences

- `plan_redo`・`investigate_redo`それぞれに専用のpendingマーカーファイルが増え、`detect_phase`の分岐がその分複雑になる。ただし影響範囲はこの2つの遷移に限定される
- `plan_redo`のpendingマーカー方式は、却下されたプランの成果物ファイルを即座に削除する副作用を持つ。redo待ちの間、`masuda plan show`のような外部コマンドが`plan/summary.md`・`plan/steps.json`を読もうとした場合の挙動（存在しないファイルとして扱われる）を実装時に確認する必要がある
- `investigate_redo`のpendingマーカーの削除をinvestigatorサブエージェント自身の作業手順に委ねるため、[[0010-plan-deviation-reopens-plan-gate]]が確立した「自己申告に頼り切らない」機械的検証の原則からは外れる。ただしこのマーカーは承認/却下のような人間の判断を左右するものではなく、単なる「redo未完了」の印であり、investigatorが消し忘れても（次のtriage中断時にもう一度investigate_redoへ差し戻されるだけで）安全側に倒れるため、この妥協は許容できると判断した
- `implement_review_graph.py`側の同型パターン（`_resolve_self_report_reopen`等）は未対応のまま残る。将来これが実際に問題化した場合は、本ADRとは別に新しいADRを起票して対応する
