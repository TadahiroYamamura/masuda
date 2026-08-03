# ADR-0024: masuda initはプロジェクト固有レビュー観点を.masuda/reviews/ファイル群として展開し、checker_promptは機械的テンプレートで生成する

## Status

Accepted (2026-08-03)

## Context

フェーズ5（レビュー）の14観点は`orchestrator/perspectives/config.py`にハードコードされており、全プロジェクトで同一の観点セットを適用している（[[0003-mechanical-vs-complex-review-nodes]]が定義した「機械的チェック」側の実体）。実プロジェクトで運用したところ、プロジェクト固有の規約に基づく観点を追加したい、AIが繰り返す失敗パターンを再発防止の観点として蓄積したい、という需要が明らかになった（GitHub Issue #1）。

Issue #1で「設定ファイルを`.masuda.json`（単一ファイル）から`.masuda/`ディレクトリ構成に再編し、新規`masuda init`コマンドで展開する」という大枠は既に決定済みだった（`.masuda/settings.json`が既存`.masuda.json`の`image`・`base`を引き継ぎ、`.masuda/reviews/`配下にレビュー観点を1観点1ファイルで格納する）。`.masuda.json`によるrepo宣言方式そのものの先例は[[0015-native-lsp-plugins-and-repo-declared-image]]。観点数が動的になっても14観点のバッチ処理ロジック（[[0021-parallel-perspective-review-batching]]）が支障なく動くことは実装済みで確認されている。

このADRが記録するのは、Issue #1の段階では「実装時に検討」として残されていた3つの具体的な論点についての決定である。

1. 既存`.masuda.json`ユーザーの移行パスをどう扱うか
2. 観点ファイルの自由記述（`review_prompt`相当）から、独立検証用の`checker_prompt`をどう自動生成するか
3. `.masuda/reviews/`内の各観点をどう識別するか（`review_results/result_{id}_attempt{N}.json`・`review_state.json`のキー。現行は`orchestrator/perspectives/config.py`の`PERSPECTIVES`が持つ整数`id`、0-13）

なお、Issue #1の「未決定の論点」に含まれていたもう1件（将来masudaに新しい組み込み観点が追加された場合、既に`masuda init`済みのプロジェクトへ後から取り込む手段）は、このADRのスコープには含めず、別Issue #8として独立して追跡する。

## Decision

### 移行パス: 破壊的変更として扱う

`.masuda.json`から`.masuda/settings.json`への後方互換読み込みは用意しない。`internal/config`パッケージは`.masuda.json`を読む実装から`.masuda/settings.json`を読む実装に置き換える。既存`.masuda.json`ユーザーは`masuda init`（または手動での配置）で移行する必要がある。

### checker_promptの自動生成: 機械的テンプレート

既存14観点の`checker_prompt`を全件確認した結果、以下の定型骨格が全観点で完全に共通しており、観点ごとに異なるのは「見落とし」「誤検知」の判定基準にあたる文言の中身だけだった。

```
あなたはレビュー品質を検証するエージェントです。
以下のPR内容と、それに対する{name}に関するレビュー結果を確認してください。

チェック観点:
1. 見落とし: <観点固有の見落とし基準>
2. 誤検知: <観点固有の誤検知基準>
3. 説明の具体性: 問題箇所と修正方法が明確に示されているか

レビューが適切であれば ok=true、問題があれば ok=false とフィードバックを返してください。
```

プロジェクト側が観点ファイルに書くのは`review_prompt`相当の自由記述（「何を確認するか」の説明文）のみで、`checker_prompt`はこの固定骨格に観点の`name`と`review_prompt`本文をそのまま埋め込んで機械的に組み立てる。「見落とし」「誤検知」の基準文言を観点ごとに個別化する代わりに、「上記の定義に該当する問題があるのに指摘していない」「上記の定義に該当しないものを誤って問題と判断している」という形で`review_prompt`本文への参照に一般化する。LLM呼び出しは行わない。

### 観点の識別方法: ファイル名をIDにする

`.masuda/reviews/`配下の各観点ファイルは、拡張子を除いたファイル名をそのままIDとする（例: `.masuda/reviews/secret-hardcode.md` → id=`secret-hardcode`）。`review_results/result_{id}_attempt{N}.json`・`check_{id}_attempt{N}.json`、および`review_state.json`の`clean`/`fixed`/`unresolved`は、現行の整数`idx`ではなくこの文字列IDでキーする。

## Alternatives Considered

- **`.masuda.json`との後方互換読み込みを残す**: 移行期間中どちらの設定ファイルが有効かが曖昧になり、実装も両対応で複雑化する。現時点でのユーザー数の少なさを踏まえ、互換性維持のコストに見合わないため不採用。
- **checker_promptを`masuda init`時にLLMで生成する**: `review_prompt`の自由記述から観点固有の「見落とし」「誤検知」文言をLLMに推定させれば、既存14観点と同水準の作り込まれたchecker_promptになりうる。しかし`masuda init`実行時にLLM呼び出しが必要になり、生成結果が非決定的でコストも発生する。[[0010-plan-deviation-reopens-plan-gate]]系の機械的バックストップ（LLM不使用）と方針が一貫しないため不採用。
- **観点IDをディレクトリ列挙のソート順の連番にする**: 実装変更は小さく済むが、レビュー実行中に観点ファイルが追加・削除・リネームされると連番がズレ、既存の`result_*.json`が別の観点を指してしまう。ファイル名IDならファイルの増減・並べ替えに対して安定して同じ観点を指し続けられるため不採用。

## Consequences

- `internal/config`が`.masuda.json`を読まなくなるため、既存の`.masuda.json`ユーザー（あれば）は`masuda init`を実行して`.masuda/settings.json`へ移行するまで`image`/`base`の解決が効かなくなる
- `orchestrator/perspectives/config.py`の`PERSPECTIVES`固定リストを廃し、`.masuda/reviews/`配下のファイルを都度読み込んで`review_prompt`・`checker_prompt`を構築する形に変更する必要がある
- [[0021-parallel-perspective-review-batching]]のバッチ処理ロジック（整数`idx`での走査・`review_state.json`のキー）を文字列ID前提に書き換える必要がある
- `checker_prompt`の検証精度は、既存14観点で手書きされていた観点固有の「見落とし」「誤検知」基準文言（作り込まれた具体例）より画一的になる。「上記の定義に該当する問題」という一般化された表現に統一される分、`review_prompt`本文の書き方の質がchecker_promptの実効性に直結するようになる
- 観点ファイルをリネームすると、リネーム前後で別観点として扱われる（`review_state.json`上の`clean`/`fixed`等の記録は引き継がれない）。ファイル名がそのままIDになる設計上の直接的な帰結
