# ADR-0027: フェーズ4をPLAN.mdのステップ単位でimplement→バックストップ→トリガー式軽量レビュー→commitのループにする

## Status

Accepted (2026-08-04)

## Context

現行のフェーズ4（実装）は、`plan/steps.json`（[[0026-plan-md-as-prose-plus-per-step-json]]）
に複数ステップが書かれていても、実装サブエージェントの判断で最後に1回分の未コミット差分
としてまとめてしまう。masuda自身が`git commit`を実行するのは`review approve`時（G2承認と
同時の1回）だけで（`cmd/masuda/gate.go`の`finalizeReviewApproval` → `worktree.Commit`）、
ステップ間のレビューも存在しない。そのため大きな変更を安全に進めるには、人間がタスクを
細かく分割してmasudaに渡す必要があった（GitHub Issue #2）。

Issue #2は「PLAN.mdのステップ単位で実装→軽量レビュー→commitを繰り返す」という大枠を
決定済みだったが、以下の点を実装時の判断に委ねていた。

1. トリガー判定を誰が・何回のLLM呼び出しで行うか（観点ごとの個別判定はコスト・レイテンシが
   懸念されていた）
2. 途中レビューで指摘が解決しない場合のエスカレーション方法（専用のエスカレーション体系
   issueは本ADR作成時点で未着手）
3. `ITERATION_BUDGET`（[[0011-iteration-budget-per-subagent-invocation]]）の再設計
   （固定値200は「実装1回+14観点フルレビュー1回」という前提のサイジングで、ステップ数が
   可変になると成立しない）

これに加え、設計を詰める過程で2つの追加の課題が見つかった。

- ステップ単位でcommitするなら、[[0013-review-rejection-reopens-implementation]]が
  定めたG2却下時の再実装（`implement_redo`）は、そのままでは意味が合わなくなる。
  ステップ機構は「まだレビューしていない作業の続き」を表すが、G2却下は「フルレビュー
  済みの成果への指摘」であり、全ステップがcommit済みの時点でG2却下が来た場合「次の
  ステップ」は存在しない
- commit時に`git add -A`で無差別にstageすると、ビルド・テスト・コード生成の副作用で
  出力されるリポジトリ固有の中間ファイル（例: OpenAPIから型を生成する度に更新される
  `merged.yaml`のようなファイル）まで巻き込んでcommitしてしまう。実装サブエージェントは
  元々ビルド/テストの自己修正ループ（[[0009-implementation-self-verification-loop]]）で
  Bashを実行するため、こうした副産物が意図せず作業ツリーに残ることがある

## Decision

### ステップ位置の追跡: gitのコミット数から導出する

専用のカウンターファイルを持たせず、`_completed_step_count() = git rev-list --count
<base_ref>..HEAD`でステップ位置を導出する。現在のステップは
`steps[_completed_step_count()]`（`steps`は`plan/steps.json`）。既存の`_actual_changed_files()`
が`git status --porcelain`から直接状態を導出する設計思想と一貫させる（自己修復的:
手動でgit操作されても状態がズレない）。全ステップがcommit済みになったら、既存の
`_detect_post_implementation_phase()`（フェーズ5・G2）にそのまま合流する。

### 1ステップの処理フロー

```
現在のステップを実装（新規: _implement_step_task）
  → implementation_result.json の status で分岐（既存のneeds_plan_review/
    build_test_failed処理はそのまま再利用、G1再オープンに合流）
  → status=done なら機械的バックストップ（既存 _mechanical_deviation()を、
    「このステップのfiles」だけを基準に比較するよう変更）
  → 逸脱なしなら trigger_match フェーズ（後述）
  → トリガー該当観点の途中レビュー（後述）
  → 全て解決したら git commit（このステップの内容のみ）→ 次のステップへ
```

### トリガー判定: ステップごとに1回のバッチ判定

`.masuda/reviews/*.md`のfrontmatterに`trigger`（自然言語、Claude Skillsの`description`
と同じ書き方）を追加する。`trigger`は任意項目とし、未設定の観点は途中レビューでは
一切トリガーされず、G2の全観点フルレビュー（既存のまま）でのみ実行される。

観点ごとに1回ずつLLM判定するのではなく、**ステップごとに1回のサブエージェント呼び出し**
で、`trigger`付き全観点のリストとそのステップのdiffを渡し、「該当する観点idの配列」を
返させる。[[0021-parallel-perspective-review-batching]]が採用した「個別ではなくバッチで
委譲する」という発想を踏襲したもので、観点数に比例したLLM呼び出しの増加を避ける。

途中レビュー自体（review→check→fix→recheckの往復）は、既存のG2フルレビューのロジック
（`_advance_and_next_task`等、[[0004-checker-fixer-role-separation]]）を、(a) 対象diff
（ベースref基準ではなく、そのステップの未コミット差分）、(b) 結果ファイルの置き場所、
(c) 対象観点（トリガー該当分のみ）を引数化して再利用する。`MAX_REVIEW_RETRIES`等の
往復ロジックは変更しない。

### エスカレーション: 専用の体系が決まるまでG1再オープンを暫定的に流用する

途中レビューの自動修正ループが収束しない場合、専用のエスカレーション体系issueが
本ADR作成時点で未着手のため、新しいゲート種別は作らず既存のG1再オープン機構
（[[0010-plan-deviation-reopens-plan-gate]]と同じ`plan`ゲートマーカー・chat/approve/reject
UX）を暫定的に再利用する。

- 承認 → 指摘を持ち越し記録に積み、そのステップをそのままcommitして次のステップへ進む
  （最終レポートに「途中レビューで持ち越された指摘」として反映する）
- 却下（feedback付き）→ 同じステップ番号のまま実装をfeedback付きで再実行する
  （ステップはまだ未commitなので、単にステップのやり直しになる）

これは暫定策であり、専用のエスカレーション体系issueが決まり次第、そちらの仕組みに
置き換わる可能性がある。

### G2却下時の再実行は「ステップ再開」と別ルートにする

[[0013-review-rejection-reopens-implementation]]のG2却下ハンドリングは、全ステップが
commit済みの時点で来た場合「次のステップ」が存在しないため成立しない。ステップ機構
（まだレビューしていない作業の続き）とG2却下（フルレビュー済みの成果への指摘）は
意味が異なるため、新フェーズ`implement_g2_redo`を新設し、PLAN.md全体スコープ・却下
feedbackを渡す単発実装（ステップ分解しない、旧来の一括実装と同じ形）として扱う。
この場合の機械的バックストップは特定ステップの`files`ではなく、全ステップの`files`を
unionした集合と突き合わせる。完了後はフェーズ5をやり直す（既存のreview state clear
をそのまま再利用）。

### commit対象は実装サブエージェントの自己申告ファイル一覧で絞る

`implementation_result.json`の`status: done`スキーマに`"changed_files"`（そのステップで
意図的に変更したファイルパスの配列）を追加する。オーケストレーターは`git add -A`では
なく`git add -- <changed_filesを1つずつ>`で、申告されたファイルだけをstageしてcommitする。

[[0010-plan-deviation-reopens-plan-gate]]の機械的バックストップは変更しない。バックストップ
は引き続き`git status --porcelain`のground truthをPLAN.mdの計画済みファイル一覧と
突き合わせる、自己申告に頼らない独立検証のままとする。自己申告（`changed_files`）は
あくまでcommit対象の絞り込みにのみ使い、逸脱検知には使わない。両者を混同すると「自己申告
に頼り切らない」というADR-0010の存在意義が崩れるため、明確に別の役割として扱う。

申告に含まれない副産物ファイルは、そのステップのcommitからは除外されるが、作業ツリーには
未commitのまま残り続ける。`implement_g2_redo`も同じ理由でこのパターン（自己申告＋
`git add --`）を流用する。

既存の[[0023-review-approve-fast-forward-not-local-merge]]・`worktree.Commit`・
`finalizeReviewApproval`は変更しない。G2承認時のcommitは「その時点でstageされているが
未commitな差分」だけを拾う設計（stageされた差分がなければ何もしない）のため、フェーズ4が
先にN個のステップcommitを積んでいても、G2フルレビューのfixerが追加修正した分だけが
最後の1コミットとして乗る、という形で自然に両立する。

### ITERATION_BUDGET: ステップ数に応じて動的に計算する

[[0011-iteration-budget-per-subagent-invocation]]の固定値200は「実装1回+14観点フル
レビュー1回」という前提のサイジングで、ステップ数が可変になると成立しない。
`ITERATION_BUDGET`をモジュールレベル定数から関数に変え、以下の式で計算する。

```
BASE_BUDGET = 200  # 既存のフェーズ5（G2フルレビュー＋横断的チェック＋synthesize）分は変更なし
PER_STEP_BUDGET = 5 + TOTAL_PERSPECTIVES * 12  # trigger_match(1) + implement redo余裕(4) + 全観点が該当した最悪ケース（ADR-0011の既存見積もりと同じ12/観点）
ITERATION_BUDGET = BASE_BUDGET + PER_STEP_BUDGET * ステップ数
```

`plan/steps.json`を読み込んだ後にのみステップ数が分かるため、この計算は遅延評価にする。

## Alternatives Considered

- **トリガー判定を観点ごとに個別のサブエージェント呼び出しで行う**: 観点数に比例して
  LLM呼び出しが増え、Issue #2自身が懸念していたコスト・レイテンシの問題をそのまま
  抱え込むため不採用。ステップごとに1回のバッチ判定にした。
- **途中レビューの未解決指摘専用の新しいゲート種別を作る**: [[0010-plan-deviation-reopens-plan-gate]]・
  [[0013-review-rejection-reopens-implementation]]が既に「新しいゲート種別を作らず既存
  ゲートを再オープンする」という方針を確立しており、これに合わせてG1再オープンを暫定的に
  流用する方が運用の一貫性が保てるため不採用。専用のエスカレーション体系issueが決まれば
  そちらに置き換わる想定。
- **G2却下時もステップ機構の一部として扱い、却下フィードバックを「追加の1ステップ」として
  `plan/steps.json`に事後追加する**: 却下フィードバックは人間の判断であり、プランナーが
  事前に計画したステップとは性質が異なる。既存の計画データを実行時に書き換える必要が
  生じ、`plan/steps.json`を「プランナーが書いた不変の計画」として扱ってきた前提が崩れる
  ため不採用。ステップ機構を経由しない別ルート（`implement_g2_redo`）とした。
- **commit時に`git status --porcelain`が示す全ファイルをそのままstageし、事後に
  副産物と思われるファイルをヒューリスティックで除外する**: 「副産物らしいファイル」を
  機械的に判定する信頼できる基準がなく、正規の変更を誤って除外するリスクがある。
  実装サブエージェント自身に「意図して変更したファイル」を申告させる方が確実なため不採用。
- **機械的バックストップも自己申告（`changed_files`）ベースに変える**: 自己申告に頼り
  切らないという[[0010-plan-deviation-reopens-plan-gate]]の存在意義そのものが崩れるため
  不採用。バックストップはground truth（`git status --porcelain`）を使い続ける。
- **ITERATION_BUDGETを固定の大きな値に単純に引き上げる**: ステップ数がPLAN.md次第で
  大きく変動するため、どんな固定値を選んでも「小さいプランには過大」「大きいプランには
  過小」のどちらかになる。ステップ数に応じた動的計算にした。

## Consequences

- フェーズ4はステップ数分のgit commitを生成するようになる。従来「フェーズ4-5は終始
  未commitのまま進む」という前提だった箇所（`_compute_diff()`等）は、内部的には
  「N個のcommit＋フェーズ5のfixerによる未commit差分」という状態を扱うことになるが、
  `_compute_diff()`自体（`git add -A`後`git diff --cached <base_ref>`）は無変更のまま
  正しく動作し続ける
- 途中レビューの未解決指摘がG1再オープンという形で人間に上がってくる頻度が増える
  可能性がある。専用のエスカレーション体系が整うまでの暫定運用であることを踏まえ、
  実際の頻度は運用しながら見直す必要がある
- 申告されなかった副産物ファイル（`merged.yaml`等）は作業ツリーに残り続け、最終的には
  フェーズ5の`_compute_diff()`やG2承認時の最終commitには依然として混入しうる。この
  `git add -A`利用箇所自体の見直しは本ADRのスコープ外とした
- `orchestrator/tests/test_implement_review_graph.py`の既存テスト（単一commitを前提に
  したもの）はステップベースの遷移に合わせた書き換えが必要になる
