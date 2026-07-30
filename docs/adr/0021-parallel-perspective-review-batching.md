# ADR-0021: 14観点のreview/check/fix/recheckを1ラウンドごとにバッチ並列委譲する

## Status

Accepted (2026-07-30)

## Context

フェーズ5（レビュー）は`_detect_review_phase()`（`orchestrator/implement_review_graph.py`）の単一`idx`カーソルで14観点を逐次処理している。1観点をreview→check→（issueありなら）fix→recheckまで完全に終わらせてから次の観点へ進むため、ウォールクロックタイムは14観点×最大12ステップ（review/check最大3試行×2 + fix/recheck最大3試行×2）の合計に比例して伸びる。14観点はいずれもdiffのみを見る独立した機械的チェックであり（[[0003-mechanical-vs-complex-review-nodes]]）、観点間に依存関係はない。

並列化にあたり、実行基盤側の制約を確認した。

- `runtime/CLAUDE.md`（コンテナ内メインセッションのループ仕様）は「TASK.mdを読み指示に従う」という自由記述の委譲をメインセッションに委ねているだけで、「1回のTASK.md＝1回のサブエージェント委譲」という前提は`_record_iteration()`のdocstringや[[0011-iteration-budget-per-subagent-invocation]]にのみ存在する運用上の慣習であり、ランタイム側のハードな制約ではない。TASK.mdの本文で「以下のN件を1メッセージ内で並列にTask委譲せよ」と明示すれば、メインセッションはそのまま従える設計になっている
- Claude Code公式ドキュメント（Agent SDK Workflows節）は「Subagents work well for a few delegated tasks per turn」としており、14件同時委譲についての公式な動作保証はない
- masudaのメインセッションは素のClaude Code CLIセッション（[[0001-self-loop-over-direct-api]]の自己ループ方式）であり、`Workflow`ツール（内部的に同時実行数を`min(16, CPUコア数-2)`で制御する別の外部オーケストレーション機構）の外側で動いているため、そちらの並列実行基盤はそのままでは使えない

## Decision

14観点のreview・check・fix・recheckのすべてのラウンドをバッチ化する。`_detect_review_phase()`は、まだ解決していない観点全件を毎回スキャンし、ディスク上のファイル有無だけで判定できる遷移（redo回数の加算、無issue/fixed/unresolvedへの確定）はサブエージェントなしでその場で処理し、それでもなお次の一手にサブエージェントが必要な観点だけを1ラウンド分のバッチとして集める。新しいphase種別`review_batch`を導入し、そのTASK.mdは「以下のN件をそれぞれ独立したTask tool呼び出しとして、この1メッセージ内で並列に委譲すること」と明示する。

1ラウンドあたりの並列数はv1では上限を設けない。実機（Dockerサンドボックス）で実際に並列委譲・完了待ちが機能するか、エラーやキューイングが起きないかを検証してから、必要であれば上限導入を別途検討する。

`ITERATION_BUDGET`（[[0011-iteration-budget-per-subagent-invocation]]）が定める「予算の単位はサブエージェント起動1回」という定義自体は変えず、1ラウンドのバッチがN件のサブエージェントを起動する場合はN加算するよう数え方だけを直す。

`runtime/CLAUDE.md`は変更しない。TASK.md本文の指示だけで並列委譲が実現できるため、ループ仕様自体に手を入れる必要がない。

## Alternatives Considered

- **初回reviewラウンドのみ並列化し、check/fix/recheckは逐次のまま残す部分適用**: 実装は小さく済むが、「レビュー全体を変更する」というスコープに反し、check/fix/recheckの往復コストが結局逐次のまま残ってしまう。不採用。
- **`Workflow`ツールへの移行**: 同時実行数の自動制御（`min(16, CPUコア数-2)`）を得られるが、masudaのメインセッションは素のClaude Code CLIセッションであり、`Workflow`ツールはその外側で動く別の実行基盤のため、フェーズ5の委譲ロジックをそちらに移すには自己ループ方式（[[0001-self-loop-over-direct-api]]）自体の見直しが必要になる。今回のスコープを超えるため不採用。
- **1ラウンドあたりの並列数に固定上限（例: 5件）を設ける**: API負荷・レート制限のバーストを避けられるが、実機で問題が起きるかどうか未検証の段階で先回りして複雑さを持ち込むことになる。ユーザーの判断により、まず上限なしで実機検証し、問題が見つかってから導入する順序を採った。

## Consequences

- ウォールクロックタイムは14観点の合計ステップ数ではなく、最長の1観点あたりのステップ数（最大12ステップ）に近づく見込みだが、実際の短縮幅は実機検証で確認する
- `.masuda-review-state.json`から`idx`（単一カーソル）を廃し、無issueで終わった観点を明示的に記録する`clean`フィールドを新設する必要がある（`idx`が暗黙に持っていた「ここまで処理済み」という意味を、他の状態と同じ形の明示的なリストで表現し直す）
- 1ラウンドで最大14件のサブエージェントを同時に起動しうるため、API呼び出しのバースト・レート制限への抵触リスクが上限なし運用の間は残る。実機検証で問題が見つかった場合、別途上限導入を検討する
- `orchestrator/tests/test_implement_review_graph.py`の既存テスト（単一idxの逐次進行を前提にしたもの）はバッチ形式の戻り値に合わせた書き換えが必要になる
