# ADR-0052: `masuda review hunk`（Hunk統合）を削除する

## Status

Accepted (2026-08-19)

## Context

[[0019-hunk-static-agent-context-sidecar]]は、G2（レビュー承認ゲート）での人間の判断を助けるため、AIの指摘をdiff上の実際のコード位置に注釈として重ねて表示する目的で、外部ツール[Hunk](https://hunk.dev)を`masuda review hunk <workspace-id>`として統合した。

実際の運用では、この機能は一度も使われていなかった。実装した本人（ユーザー）が改めて起動して確認したところ、表示される注釈がどれも「よくわからない場所」に見え、実用に耐えなかった。

さらに調査したところ、この機能は状態デーモンへの移行（Issue #35）の際に完全に壊れたまま放置されていたことも判明した——`internal/hunkcontext.readReviewState`が読む`<stateDir>/.masuda-review-state.json`という平ファイルは、レビュー状態が状態デーモンのキー`internal:review-state`へ移行して以降、書き手が存在しない。`hunkcontext.Build`はこのファイル読み込みに失敗した時点でエラーを返す実装になっているため、少なくとも状態デーモン移行以降は`masuda review hunk`を実行すると必ずエラーで終了していたはずである。つまり「よくわからない場所が表示された」という実際の体験は、この既知のバグより前——レビュー状態がまだ平ファイルとして書かれていた時期——の記憶であり、バグを直しても再現しない。

その体験を踏まえて元の目的を見直すと、目的と実装がそもそもズレていたことが分かる。ユーザーが本来欲しかったのは「エージェントの作業計画が指定した**受け入れ条件**が正しく実装されているかを目視で確認する手段」だったが、`hunkcontext.Build`が実際に注釈化していたのは14観点レビュー・横断的チェックの**指摘**（コード品質・正しさの問題として見つかったもの）であり、受け入れ条件の充足有無とは別の情報である。masudaのパイプラインは現状、受け入れ条件を`plan/steps.json`のステップ単位より細かい粒度（行範囲等）で追跡するデータを持っていないため、たとえバグを直しても目的通りには機能しない。

## Decision

`masuda review hunk`コマンドと`internal/hunkcontext`パッケージを削除する。

- `cmd/masuda/review.go`から`newReviewHunkCommand`を削除、`cmd/masuda/main.go`の登録（`reviewCmd.AddCommand(newReviewHunkCommand())`）も削除
- `internal/hunkcontext/`パッケージ（`hunkcontext.go`）を削除
- `docs/design/review.md`の「Hunkコンテキスト連携」節、`docs/design/cli.md`・`docs/design/README.md`の関連する参照を削除
- `masuda review show`（`final_report.md`のテキスト表示）は変更なし。指摘を確認する手段はこちらに一本化される

[[0020-structured-file-line-schema-for-findings]]（指摘のfile+startLine+endLine構造化スキーマ）はこのADRの対象外——Hunk向けに導入されたものだが、現在は`masuda review show`自体のレンダリング（`orchestrator/implement_review_graph.py`の`_render`系関数）が同じスキーマに依存しており、Hunk統合とは独立して有効なため、そのままAcceptedとして残る。

## Alternatives Considered

- **バグだけ直して機能を維持する**（`readReviewState`を状態デーモンのキー経由の読み取りに書き換え、スキーマを現行のID文字列ベースに合わせる）: 技術的には可能で、いったんこの方向で計画していた。しかし「よくわからない場所が表示される」という根本的なUX上の不満は、バグとは独立した「指摘の注釈≠受け入れ条件の充足確認」という目的と実装のズレに起因しており、バグを直しただけでは解消しない。実際に一度も運用で使われていない機能に、目的を達成しないまま直す労力を割く理由がない
- **`hunkcontext`のデータソースを指摘から受け入れ条件へ差し替えて存続させる**: 本来やりたかったことに近いが、masudaのパイプラインが受け入れ条件を行単位で追跡する仕組みを持たないため、まず「受け入れ条件をどう構造化して追跡するか」という別の設計課題に取り組む必要がある。これは本ADRのスコープを超える別の要件整理であり、GitHub Issueとして別途起票し、必要になった時点で改めてゼロから設計する

## Consequences

- `masuda review show`のみが指摘確認の手段になる。ホストに`hunk` CLIをインストールする必要がなくなる
- `internal/hunkcontext`が使っていた`review_results/hunk-context.json`という出力先も無くなる（元々生成されなかったか、生成されても使われていなかった）
- 「受け入れ条件を目視で確認したい」という元々の要望自体は解決していない。別のGitHub Issueとして起票し、要件整理から再出発する
