# ADR-0058: commitに含めるファイルは実装エージェントの自己申告ではなくgitの実測から決める

## Status

Accepted (2026-09-04)

## Context

[[0027-phase4-step-based-implement-review-commit-loop]]は、ステップcommitの範囲を実装サブエージェントの自己申告（`implementation_result.json`の`changed_files`）で決めることにした。目的は、ビルド/テストの自己修正ループ（[[0009-implementation-self-verification-loop]]）が残す副産物ファイルを`git add -A`で巻き込まないことにあった。

この自己申告は**実装が終わった瞬間のスナップショット**である。ところが同じADR-0027が導入したトリガー式軽量途中レビューは、その後に走る——review/check/fix/recheckのループでfixerがファイルを直しても、`changed_files`は更新されない。`_finalize_step`はその古いリストでcommitするため、fixerの修正が計画内のファイルへのものであってもcommitから漏れ、作業ツリーに残り続ける（GitHub Issue #30）。

実機で再現して分かったのは、Issue #30が書いた原因分析が2点ずれていたことである。

- **途中レビューは必要条件ですらない。** 実装者が計画内のファイルを変更したのに申告し忘れるだけで同じことが起きる。2ファイルを宣言したプランで両方を変更し片方だけ申告すると、commitは片方だけ、もう片方はdirtyのままフェーズ5へ進んだ
- **「バックストップが途中レビュー後に再実行されない」は誤り。** `detect_phase`は毎往復の先頭で走り、途中レビューのバッチ1回ごとにも走る。fixerが計画外のファイルを触れば次の往復で`plan_reopened`になる（実機確認済み）。計画内かどうかで発火が決まるので、**計画内のファイルの取りこぼしだけが検知網の外**にあった

TDDモード（[[0037-tdd-mode-red-green-refactor-subloop-in-phase4]]）ではさらに確実に起きる。各Red/Green/Refactorフェーズが個別にcommit済みのため`_finalize_step`は何もcommitせず、途中レビューのfixerが直したものは**必ず**置き去りになる。

## Decision

commitに含めるファイルを、gitの実測から決める。

```
_committable_files(planned) = _actual_changed_files() ∩ (planned ∪ 承認済み逸脱)
```

`planned`は文脈ごとに変わる——通常ステップとTDDフェーズはそのステップの宣言ファイル、`implement_g2_redo`はプラン全体のunion（ADR-0027の区別をそのまま踏襲）。commitを行う5箇所すべて（TDDフェーズcommit、TDD完了時の最終フェーズcommit、その逸脱承認版、`_finalize_step`、`_finalize_g2_redo`）がこれを使う。

副産物を除外する役目は交差が引き継ぐ。予想副産物（[[0028-plan-predicted-byproducts-exempt-from-backstop]]）は`planned`にも承認済みにも入らないので落ちる。予想外の副産物はcommitに到達する前に機械的バックストップがG1を再オープンする。commit直前は`実測 ⊆ 計画 ∪ 承認済み ∪ 予想副産物`が保証されているので、この式は実質「実際に変わったもの全部から予想副産物を除いたもの」になる。

付随して以下が変わる。

- **`changed_files`をスキーマから削除する。** commit範囲の決定から降りると、残る用途はRefactorフェーズの「改善点なし→process checkerを飛ばす」判定だけになる。これも実測（スコープ内が無変更）で判定するほうが強い——申告だけでcheckerを飛ばせると、実際には変更しているのに素通りできてしまう。プロンプトの完了条件は`{"status": "done"}`だけになる
- **TDDステップの`_finalize_step`もcommitする。** 途中レビューの取りこぼしを回収するため。そのcommitには書き手がいないので、メッセージはオーケストレーターが固定文言で生成する（`fix: 途中レビューの指摘を反映する（ステップN）`。LLMは呼ばない、[[0002-workflow-orchestrator-with-subagent-delegation]]）
- **スコープ内が無変更でもcommitメッセージがあれば空commitを作る。** ステップの境界tagは`base_ref..HEAD`の中のcommitに乗っていないと完了として数えられず（`_completed_step_count`）、commitが1つも無いと同じステップが永久に再発行される。メッセージが無い場合（no-opのRefactorターン）は何もcommitしない

## Alternatives Considered

- **fixerが触ったファイルを`result`へ合流させる（Issue #30の提案2）**: 自己申告の枠組みを保ったまま穴を塞ぐ案。fixerに与えている制約は「指摘箇所（`issues[].file`）以外は変更しない」であって、実際に触ったファイルの申告ではない。合流させた集合が実測と一致する保証がなく、同じ穴が形を変えて残る
- **commit直前にバックストップを再実行する（Issue #30が示唆した方向）**: 実機で確認したとおり、これは既に毎往復で行われている。計画外の変更は現状でも検知されており、追加しても何も変わらない
- **`git add -A`に戻す**: ADR-0027が解いた問題（副産物の巻き込み）がそのまま戻る
- **`changed_files`を残し、実測との不一致を警告に使う**: 申告の正確さを測れるが、誰も読まない警告が増えるだけで、commitの正しさは実測だけで決まる。フィールドを残す理由にならない

## Consequences

- 計画されたパスに副産物が生成された場合、これまでは実装者が申告から外せば除外できたが、今後はcommitされる。計画されたパスに副産物が出るのは計画自体の矛盾であり、隠すより出すべきという判断
- 計画外のファイルがTDDのフェーズcommitに載らなくなった。以前は自己申告に含まれれば載り、ステップ完了時のバックストップが`since_ref`付きの差分で拾っていた。今は作業ツリーに残るので同じバックストップが直接見る——**逸脱がgit履歴に入らなくなった**ぶん強くなったが、`_actual_changed_files(since_ref)`のunionはバックストップの検知にとって実質的な役割を失い、多重防御として残る形になった
- 同じ理由で、TDDステップ却下時の`_reset_tdd_step`（`git reset --hard`）が計画外ファイルを消さなくなった。以前はそれがcommit済みだったから消えていただけで、未追跡ファイルは元から対象外である。人間の却下フィードバックが削除を指示し、従わなければバックストップが再び発火する
- `implementation_result.json`が`status`（とTDDの`tdd_next_phase`）だけになり、サブエージェントが申告すべきことが減った
- **未解決**: TDDステップの途中レビューが往復を要する場合、`detect_phase`が`tdd_refactor`へ戻ってしまう既存バグがある（`_detect_tdd_step_completion`が`IMPLEMENTATION_RESULT_JSON`だけ消してサイクル状態を残すため、`_detect_tdd_phase`の`result is None`分岐に落ちる）。本ADRの変更とは独立で、TDDステップのfixループが完了できない。GitHub Issue #48
