# ADR-0020: 指摘のlocationを自由記述からfile+startLine+endLineの構造化スキーマに変える

## Status

Accepted (2026-07-30)

## Context

[[0019-hunk-static-agent-context-sidecar]]でHunk統合（静的`--agent-context`サイドカー方式）を決めたが、Hunkの注釈は`newRange: [startLine, endLine]`という正確な行番号を要求する。

現行の指摘データは以下のいずれも「ファイル:行等」という自由記述文字列（`location`）に一本化されている。

- 14観点（`orchestrator/perspectives/config.py`）の`review_prompt`が書き出す`review_results/result_*.json`の`issues[].location`（`_review_perspective_task`、`implement_review_graph.py:580`）
- 横断的チェックの`cross_cutting_findings.json`/`cross_cutting_verified.json`の`location`（`_cross_cutting_explore_task`/`_cross_cutting_verify_task`、`implement_review_graph.py:731,769`）

プロンプト上も"ファイル:行等"という緩い指示のみで統一フォーマットを強制しておらず、`location`を機械的にパースする仕組みはどこにも存在しない。

## Decision

両スキーマの`location`フィールドを廃し、以下の構造化フィールドに統一する。単一行の指摘は`startLine == endLine`とする。`file`はdiffに現れるパス表記と一致させる。

14観点（`issues[]`）:

```json
{"severity": "高|中|低", "file": "...", "startLine": 0, "endLine": 0, "description": "...", "suggestion": "..."}
```

横断的チェック（配列要素）:

```json
{"description": "...", "file": "...", "startLine": 0, "endLine": 0, "severity": "高|中|低"}
```

14観点のチェッカーは元々diffのみを見て判定する機械的チェックのため、行番号はdiffの`@@ -a,b +c,d @@`ハンクヘッダーから数えられる新ファイル側の行番号として書かせる。横断的チェックのexplorer/verifierはRead/LSPツールを持つため、実ファイルを直接確認して行番号を書ける（機械的チェックより高精度になりうる）。

あわせて以下も更新する。

- `_fixer_task`のプロンプト（`implement_review_graph.py:652`、「指摘箇所（`issues[].location`）以外のファイルは変更しないこと」）を`issues[].file`を参照する記述に変更
- `_unresolved_section`/`_cross_cutting_section`（`final_report.md`生成ロジック、`implement_review_graph.py:823`）を`location`ではなく`file:startLine`形式の文字列を組み立てるよう変更
- `orchestrator/tests/test_implement_review_graph.py`のテストフィクスチャ（367-439行目）を新フィールドに合わせて更新

## Alternatives Considered

- **既存の`location`自由記述文字列のまま、正規表現でfile:lineをベストエフォート抽出する**: プロンプトが"ファイル:行等"という緩い指示のみで統一フォーマットを強制していないため、パース失敗時のフォールバック（ファイル全体を対象にする等）が別途必要になり複雑化する。masudaは元々サブエージェントの成果物を機械的にスキーマ検証する方式（[[0002-workflow-orchestrator-with-subagent-delegation]]）を採っており、緩い自由文字列を後からパースするより最初から構造化させる方が一貫している。不採用。
- **diffのハンク番号（`--hunk <n>`）ベースの位置指定にする**: Hunkの`--agent-context`静的サイドカーのスキーマ自体が`newRange`（行番号）のみをサポートし、ハンク番号による位置指定は`hunk session comment apply`のライブセッションAPI専用（[[0019-hunk-static-agent-context-sidecar]]で不採用と決めた方式）にしか存在しない。[[0019-hunk-static-agent-context-sidecar]]で静的サイドカー方式を選んだ時点で、ハンク番号ベースの指定は選択肢として成立しない。
- **横断的チェックのみ構造化し、14観点は`location`のまま据え置く**: G2の`final_report.md`には両者の指摘が混在して現れるため、片方だけ構造化してもHunk注釈は部分的にしか出せない。ユーザー判断により不採用。

## Consequences

- 14観点・横断的チェック双方のタスク生成プロンプト、fixerタスク、`final_report.md`生成ロジック、テストフィクスチャの変更が必要になり、横断的チェックのみ構造化する案より影響範囲が広い
- 指摘位置の正確性はサブエージェントの申告に依存し続ける。行番号が実際の該当箇所とずれていても、Hunk・masuda側どちらにもそれを検知する仕組みはない。位置情報が構造化されたことは「機械的に扱える形式になった」ことを意味するだけで、内容が「必ず正しい」ことを保証しない
- `location`という1フィールドの自由記述に比べ出力項目が増えるため、サブエージェントがスキーマ通りに出力できない（JSON不正）ケースがわずかに増える可能性がある。既存の「不正なら再試行」の仕組み（`docs/design/sandbox-workflow.md`のスキーマ検証・redo）でそのまま吸収できる想定
