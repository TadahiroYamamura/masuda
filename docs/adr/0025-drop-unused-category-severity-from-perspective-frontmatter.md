# ADR-0025: 観点ファイルのfrontmatterから未使用のcategory・severityを削除する

## Status

Accepted (2026-08-03)

## Context

[[0024-file-based-perspectives-mechanical-checker-prompt]]は、`.masuda/reviews/*.md`のYAML frontmatterを「name/category/severity」の3フィールドと決めた。この3フィールドは、元々`orchestrator/perspectives/config.py`にハードコードされていた各観点の辞書（`id`/`name`/`category`/`severity`/`review_prompt`/`checker_prompt`）をそのままファイル形式に移し替えただけで、frontmatterに何を含めるべきかを個別に検討した結果ではなかった。`category`・`severity`の値自体も、遡ると`feat/github-actions-langgraph-nodes`という別ブランチから「置き場所のみ移植」された（2026-07-25のコミットメッセージより）ものであり、severityの割り振り基準を説明する記録はこのリポジトリのどこにも存在しない。

ユーザーから「severityの基準は何か、masudaはこの値をどう使うか」と問われて実装を確認したところ、`orchestrator/implement_review_graph.py`の`_parse_perspective_file()`が`category`・`severity`をパースして`PERSPECTIVES[pid]`辞書に格納してはいるものの、それ以降どこからも読み返されていないことが判明した（Go側`internal/`・`cmd/`にも参照なし）。一方`name`は`_checker_prompt()`の導入文、`_fixed_section`/`_unresolved_section`のレポート表示、タスクプロンプトのタイトル等で実際に使われている。

なお、レビュー結果JSON（`issues[].severity`）や横断的チェックの指摘が持つ「指摘（issue）ごとのseverity」は[[0020-structured-file-line-schema-for-findings]]が定義した別物で、`internal/hunkcontext`がHunk上の注釈生成に実際に使っており、これは今回の削除対象ではない。削除するのはあくまで観点（perspective）自体が持つcategory・severityフィールドである。

masuda initが今後、ユーザー（対象リポジトリの保守者）に独自の観点ファイルを書かせる想定である以上、実際には使われないメタデータの入力を求めることは、観点を書く上でのノイズにしかならない。

## Decision

`.masuda/reviews/*.md`のfrontmatter必須フィールドを`name`のみにする。

- `internal/perspectives/builtin/*.md`（Go側embed元、14ファイル）から`category:`・`severity:`の行を削除する
- `orchestrator/implement_review_graph.py`の`_parse_perspective_file()`は`name`のみをfrontmatterから読み、`category`・`severity`は`PERSPECTIVES[pid]`辞書に含めない
- `orchestrator/tests/test_implement_review_graph.py`の合成テスト観点fixtureも`name`のみのfrontmatterに揃える

## Alternatives Considered

- **`category`・`severity`をそのまま残し、将来の拡張（レビュー観点の分類表示・severityによるフィルタ等）に備える**: 使われていないフィールドを「将来使うかもしれない」という理由だけで観点ファイルの必須項目として維持すると、実際に機能が実装されるまでの間ずっと、観点を書くユーザーに実体のない入力を強いることになる。必要になった時点で改めてfrontmatterに追加する方が、今ノイズを抱え込むより筋が良いため不採用。
- **`severity`だけ残し`category`のみ削除する（あるいはその逆）**: 両方とも「パースされるが読み返されない」という点で状況が完全に同一であり、片方だけ残す理由がない。

## Consequences

- `masuda init`が生成する観点ファイルのfrontmatterは`name`の1行のみになり、ユーザーが独自の観点ファイルを書く際の最小構成も同様に`name`だけで済む
- `PERSPECTIVES[pid]`辞書から`category`・`severity`キーが消えるため、将来これらの情報を使う機能（観点の分類表示、severityによる絞り込み等）を追加する場合は、frontmatterのフィールドを再度増やすところから始めることになる
- 既に`.masuda/reviews/`にcategory/severity付きのfrontmatterを書いてしまったプロジェクトがあっても、`_parse_perspective_file()`はfrontmatter全体をYAMLとして読んだ上で使うキーだけを取り出す実装のため、未知のキーがあってもエラーにはならない（無視されるだけ）
