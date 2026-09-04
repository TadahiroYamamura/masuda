# ADR-0026: PLAN.mdをMarkdown単一ファイルから、prose（summary.md）とステップ単位のJSON（steps.json）に分離する

## Status

Accepted (2026-08-04)

- 一部改訂: [[0028-plan-predicted-byproducts-exempt-from-backstop]] — `plan/steps.json`のトップレベルはステップの配列ではなく`{"steps": [...], "expected_byproducts": [...]}`というオブジェクト
- 一部改訂: [[0037-tdd-mode-red-green-refactor-subloop-in-phase4]] — 各ステップに`"mode": "tdd"`が付きうる
- 一部改訂: [[0059-remove-tdd-mode]] — 上の0037による改訂は失効した。`mode`フィールドはTDDモードとともに削除された

## Context

`PLAN.md`の「変更するファイル一覧」は、[[0010-plan-deviation-reopens-plan-gate]]の機械的
バックストップ（`git status --porcelain`との突き合わせ）の判定材料になっている。この
突き合わせは`_extract_planned_files`（`orchestrator/implement_review_graph.py`）が
「## 変更するファイル一覧」セクション配下のバッククォート付き箇条書きを正規表現で
パースする実装だった。

実運用でこのパースが時々失敗する問題が確認された。プランナーサブエージェントの出力が
毎回同じ書式になるとは限らず、テーブル形式になったり、箇条書きの番号付け・インデントが
崩れたりすることがあり、そのたびに機械的バックストップ自体が機能しなくなっていた。

これとは別に、Issue #2（[[0027-phase4-step-based-implement-review-commit-loop]]）で
フェーズ4がPLAN.mdのステップ単位でimplement→バックストップ→commitを繰り返す設計に
変わることになった。この設計では、バックストップを「そのステップで実際に何を変更する
計画だったか」という単位で検証できたほうが、計画全体に対する緩い突き合わせより厳密に
scope disciplineを検証できる。現行のPLAN.mdは「変更するファイル一覧」を計画全体で1つ
しか持っておらず、この単位に対応していない。

## Decision

`PLAN.md`という単一のMarkdownファイルを廃止し、ワークスペース状態ディレクトリに
`plan/`サブディレクトリを新設して以下の2ファイルに分離する。

- `plan/summary.md`: 人間向けの自由記述prose（アプローチの要約／テスト方針／検討したが
  採用しなかった代替案／リスク・懸念事項）。機械的に消費されないので書式は自由なまま
  Markdownで書く
- `plan/steps.json`: 実装のステップ分解を、ステップごとに「そのステップで変更する
  ファイル一覧」まで含めて構造化データとして持つ

  ```json
  [
    {
      "description": "ステップ1の説明（このステップで何を実装するか）",
      "files": [
        {"path": "internal/foo/bar.go", "description": "〜のため〜を追加"},
        {"path": "internal/foo/bar_test.go", "description": "上記のテスト"}
      ]
    },
    { "description": "ステップ2の説明", "files": [...] }
  ]
  ```

計画全体のフラットなファイル一覧（旧`changelist.json`に相当するもの）は別途持たない。
全体像が必要な場面（`masuda plan show`の表示、G2却下時の再実装 —
[[0027-phase4-step-based-implement-review-commit-loop]]参照）では、各ステップの`files`
を機械的に集約（union）して導出する。単一の情報源をステップ側にのみ持たせることで、
「全体の一覧」と「ステップごとの一覧」が食い違う余地を無くす。

機械的に消費される部分（ファイル一覧・ステップ分解）は、Markdown箇条書きの正規表現
パースではなく、独立したJSONファイルとして持たせる。「有効なJSON配列/オブジェクトを
1個出力させる」ことを要求するほうが、番号付きリストや箇条書きの書式ゆれを正規表現で
追いかけるより、LLM出力として安定する。

`masuda plan show`は`internal/gate/gate.go`の`Show()`がこの2ファイルを読み、人間向けの
Markdownを組み立てて表示する（summary.mdの内容＋ファイル一覧の箇条書き＋ステップ分解の
番号リストを機械的に連結する、LLMを介さない処理）。DEVIATION.mdのprepend（G1再オープン時
に理由を先頭に出す既存挙動）はそのまま維持する。

`orchestrator/implement_review_graph.py`側にも、サブエージェント向けプロンプトに埋め込む
ための同種の組み立てロジックを持たせる。Go側とは実装を共有せず、それぞれの言語で機械的な
組み立て処理を独立に持つ（10行程度の単純な処理であり、共通化のための抽象化を導入するコスト
の方が高いと判断した）。

`.masuda.json`から`.masuda/settings.json`への移行（[[0024-file-based-perspectives-mechanical-checker-prompt]]）
と同様、後方互換の読み込みは用意しない破壊的変更として扱う。実行中ワークスペースへの影響は
考慮せず、新規に`masuda plan start`するワークスペースから新形式が適用される。

## Alternatives Considered

- **Markdown本文中に埋め込みJSONコードブロックを置く（`## 変更するファイル一覧`の下に
  \`\`\`json ... \`\`\` を書かせる）**: 正規表現でセクション境界とコードフェンスを両方
  探す必要があり、依然としてMarkdown構造への依存が残る。ファイルを分離すれば
  「JSONとしてパースできるかどうか」だけを気にすればよくなり、失敗モードが単純になるため
  不採用。
- **既存の正規表現パーサーを維持し、プロンプト側の指示文をより厳格にするだけで対応する**:
  出力ゆれの根本原因（自由記述Markdownの中に構造化データを埋め込もうとしていること）を
  解消しないため、プロンプトをどれだけ厳密に書いても再発リスクが残る。不採用。
- **ファイル一覧をプラン全体で1つのまま持ち、ステップ単位のバックストップは計画全体の
  一覧に対して緩く検証する**: 実装は単純だが、あるステップが「本来は別ステップ向けの
  ファイル」を先取りして触っても検知できない。ステップ単位でscope disciplineを検証する
  という今回の目的（[[0027-phase4-step-based-implement-review-commit-loop]]）に対して
  弱すぎるため不採用。

## Consequences

- `PLAN.md`という単一ファイル名はワークスペース状態ディレクトリ上に存在しなくなる。
  `internal/hostloop`が持つセッション許可ルール（`Edit(/PLAN.md)`）・plannerサブエージェント
  のプロンプト文言を新しい2ファイルパスに合わせて更新する必要がある
- `masuda plan show`はJSONのパースエラーというこれまでなかった失敗モードを持つようになる
  （Markdownをそのまま表示するだけだった従来より、プランナーの出力形式への依存が「セクション
  見出しの有無」から「有効なJSONかどうか」に変わる）
- ステップの説明とそのステップで変更するファイルの理由（description）が同じJSONオブジェクト
  内に同居するため、プランナーはステップ分解とファイル一覧を同時に、対応関係を意識して
  書く必要がある（従来は別々のセクションとして独立に書けた）
