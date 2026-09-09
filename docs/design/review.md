# Review（レビュー）

Review段階（フェーズ5、G2＝review gate）の内部設計。`orchestrator/implement_review_graph.py`（Build/Reviewを1つのオーケストレーターにまとめている、ADR-0013）が状態機械として駆動し、実際の判定は毎回新規コンテキストのサブエージェントにTask委譲する。オーケストレーター自身はLLMを呼ばない。

対象リポジトリの`.masuda/reviews/*.md`から動的ロードする**14個**のレビュー観点（`internal/perspectives/builtin/`が`masuda init`/`masuda update`経由で展開する内蔵観点の数。ユーザーが独自観点を追加・無効化すれば数は変わる）による機械的レビューと、diffだけでは検知できない横断的チェックの2系統で構成する。両者の指摘は`review_results/final_report.md`に統合され、review gateで人間が最終判断する。

## 観点定義の動的ロード

`REVIEWS_DIR = Path(".masuda/reviews")`（cwdはこのスクリプトが動く間常にworktree、`implement_review_graph.py:80`）配下の`*.md`をファイル名昇順で読み込む。

- `_parse_perspective_file`（`:107`）: `---\n`区切りのYAML frontmatterと本文に分割する。本文はそのまま`review_prompt`になる。frontmatterのフィールドは`name`（必須。省略時はファイル名から補う）、`trigger`（任意、自然言語。Build段階の途中レビューがどの観点を発火させるかの判定に使う——詳細は`docs/design/build.md`）、`enable`（任意、既定`true`。`false`のファイルは存在するがロード対象から除外される）の3つだけ（ADR-0025）
- `checker_prompt`は観点ファイルに一切書かれておらず、`_checker_prompt(name, review_prompt)`（`:83`）が`review_prompt`から機械的に生成する。導入文＋「見落とし／誤検知／説明の具体性」という固定チェックリスト＋`review_prompt`自体への参照、という1つの型に常に埋め込む
- `_load_perspectives`（`:136`）が`enable`フィルタを適用し、`{ファイル名(拡張子なし): {name, trigger, enable, review_prompt, checker_prompt}}`の辞書`PERSPECTIVES`を作る。この辞書のキー（＝ファイル名）が観点の恒久的なID。`PERSPECTIVE_IDS = sorted(PERSPECTIVES)`はサブエージェント向けの「観点 N/TOTAL」という表示用の並び順を作るためだけに存在し、識別そのものには使わない
- `PERSPECTIVES`はモジュールimport時に一度だけロードされる（`:161`）。`REVIEWS_DIR`が存在しない/空の場合は空辞書を返し、実際の「`masuda init`していない」エラーは`_detect_review_phase`（後述）が`TOTAL_PERSPECTIVES == 0`を見た時点で送出する

## review/check/fix/recheckエンジン

1つの観点を「レビュー→検証→（issueがあれば）修正→再検証」まで進める状態遷移ロジック（ADR-0004・ADR-0021）。**Review段階の本レビューと、Build段階の途中レビュー（`docs/design/build.md`、ADR-0027）はこの同じエンジンを共有する**——差分は「どこに結果を書くか」（`results_dir`パラメータ）と「どの観点集合・どのdiffを対象にするか」だけで、遷移ロジック自体（`_advance_and_next_task`）に分岐はない。このドキュメントがこのエンジンの唯一の所有者。

状態は`{"redo_counts": {}, "fix_counts": {}, "unresolved": [], "fixed": [], "clean": []}`という形（Review本体は`internal:review-state`daemonキー、途中レビューはステップごとの`internal:interim-review-state-step{N}`daemonキー）。結果ファイルは`results_dir`配下に`result_{pid}_attempt{N}.json` / `check_{pid}_attempt{N}.json` / `fix_{pid}_fixattempt{N}.json` / `recheck_{pid}_fixattempt{N}.json`という命名で置かれる（`_result_path`等、`:660-673`）。

`_advance_and_next_task(results_dir, pid, rs)`（`:676`）は、ディスク上に既にある結果だけでどこまで進められるかをその場で（サブエージェント無しで）進め、サブエージェントが実際に必要になった瞬間だけタスク記述子`{"id", "kind", "attempt", ["fix_attempt"]}`を返す。

1. `result_{pid}_attempt{attempt}.json`が無ければ`kind: "review"`
2. `check_{pid}_attempt{attempt}.json`が無ければ`kind: "check"`
3. checkが`ok: false`なら、`redo_counts[pid] < MAX_REVIEW_RETRIES`（`= 2`、`:179`）の間は`redo_counts`を1増やして1.に戻る（＝次のattemptで観点を再レビュー）。上限に達したら`unresolved`に`{"id": pid, "reason": "review_check_not_converged"}`を積んで終了
4. checkが`ok: true`かつresultが`has_issues: false`なら`clean`に積んで終了
5. issueが確認された場合、`fix_{pid}_fixattempt{fix_attempt}.json`が無ければ`kind: "fix"`、`recheck_{pid}_fixattempt{fix_attempt}.json`が無ければ`kind: "recheck"`
6. recheckが`resolved: true`なら`fixed`に積んで終了。`false`なら`fix_counts[pid] < MAX_REVIEW_RETRIES`の間は`fix_counts`を1増やして5.に戻る（＝再修正）。上限に達したら`unresolved`に`{"id": pid, "reason": "fix_not_resolved"}`を積んで終了

4種のタスクそれぞれに1つずつ、決定的なプロンプト生成関数がある（`:1832-1976`）。いずれも新規コンテキストのサブエージェントへの委譲を指示し、末尾に`_TRIAGE_SELF_REPORT_SECTION`（ADR-0029の自己申告フォーマット、詳細は`docs/design/gates.md`）を含める。

- `_review_perspective_task`（`:1832`）: `p["review_prompt"]`＋対象diffを見せ、`{perspective_id, perspective_name, has_issues, issues:[{severity, file, startLine, endLine, description, suggestion}], summary}`を書かせる（ADR-0020の構造化スキーマ）。`attempt > 1`なら前回checkのフィードバックを追記する
- `_check_perspective_task`（`:1873`）: レビューした本人とは別コンテキストのサブエージェントに`p["checker_prompt"]`とdiff・レビュー結果を見せ、`{perspective_id, ok, feedback}`を書かせる
- `_fix_perspective_task`（`:1909`）: 指摘箇所（`issues[].file`）のみ書き込み可能な軽量サブエージェント（ADR-0004。指摘したcheckerでも重量級の実装サブエージェントでもない）に修正させ、`{"status": "fixed"}`を書かせる
- `_recheck_perspective_task`（`:1948`）: 同じ`p["checker_prompt"]`を「修正後のdiffで元の指摘が解消されたか」という問いに転用し、修正した本人とは別コンテキストのサブエージェントに`{resolved, feedback}`を書かせる

`_TASK_RENDERERS_AND_PATHS`（`:1983`）が`kind`→(プロンプト生成関数, 完了条件パス)の対応表を持ち、`_render_batch(results_dir, diff, label_fn, tasks, header)`（`:2003`）がバッチ内の各タスクをこの対応表経由でレンダリングして1つのTASK.mdに連結する（ADR-0021）。

## Review段階本レビュー

`_detect_review_phase`（`:1066`）が呼ばれるたびに、まだ`clean`/`fixed`/`unresolved`のいずれにも属さない全観点を毎回スキャンし、`_advance_and_next_task`がサブエージェントを要求した観点だけを1ラウンド分のバッチとしてまとめる（ADR-0021。観点間に依存はなく、review待ちの観点とrecheck待ちの観点が同じラウンドに混在してもよい）。バッチが空でなければ`phase: "review_batch"`、`reason`に`{"tasks": [...]}`を積んで返す。

`_review_batch_task`（`:2018`）が実際のTASK.mdを生成する。`_compute_diff()`（base_refに対する差分、`git add -A`で新規/削除ファイルも含めてstageするがcommitはしない、`:554`）で対象repoの全体diffを1回だけ計算し、バッチ内の全タスクに使い回す。ヘッダーで「このN件は互いに独立、**この1メッセージ内で並列に**Task委譲すること」と明示し、全完了条件ファイルが揃うまで待ってから次のTASK.mdへ進むよう指示する。

全観点が`clean`/`fixed`/`unresolved`のいずれかに収束すると、`_detect_review_phase`はバッチを返さず横断的チェック（次節）へ進む。

Build段階の途中レビュー（ADR-0027）は`_detect_interim_review_phase`（`docs/design/build.md`が所有）から同じ`_advance_and_next_task`/`_render_batch`を`results_dir=_interim_step_dir(N)`・diffはそのステップだけの差分で呼び出す、この節のスコープを1ステップ分に縮めた別インスタンス。

## 横断的チェック

`_detect_review_phase`は全観点収束後、`CROSS_CUTTING_FINDINGS_JSON`（`review_results/cross_cutting_findings.json`）が無ければ`phase: "cross_cutting_explore"`を返す。explorer→verifierの1パス構成で、redoループを持たない（ADR-0003・ADR-0011。複雑な指摘は常に人間判断に委ねる方針のため、「意見が収束するまで往復する」という発想自体が適用されない）。

横断的チェック（explorer・verifier）と14観点の機械的チェック（前節）は手段が異なる2区分になっている。機械的チェックはdiffのみを見せる単発判定でBash/Read等のツールを持たず、LSPも使わない。横断的チェックのexplorer・verifierはBash・Read・Grep・Globに加え、依存解決とLSPの利用方法を指示する共有節`_LSP_AND_DEPENDENCY_SECTION`を持つ——利用可能ならClaude Code純正のLSPツール（find references・go to definition等）を使い、LSPが正しく機能するには依存解決が必要な場合があるため、環境が未セットアップならCLAUDE.md・README等を参照して依存解決（`go mod download`等）を行う。依存解決が外部ネットワーク（このVMのegress既定拒否）に阻まれた場合は再試行せず、LSP無しでRead/Grep/Globのみで進める（縮退）。依存解決を担うはずのScaffold段階は未実装の予約名のため、現状はexplorer・verifier自身が必要に応じて自分のタスク内で依存解決を行う（ADR-0003）。

- `_cross_cutting_explore_task`（`:1725`）: 探索の起点は対象diffで、「diffで変更されたファイルが依拠する既存コードとの不整合」を探すのが目的——リポジトリ全体を無制限に彷徨うことは避けるようプロンプトで指示する。探索の観点として性質の異なる2種類を例示している: (1) 実装パターンの一貫性（同役割のファイル間でエラーハンドリング等の流儀が食い違う）、(2) ビルドでは検知されない変更の伝播漏れ（ただしGo等の静的型付け言語の単純な引数過不足はビルドエラーとしてBuild段階の自己検証で既に弾かれるため、この観点が意味を持つのは主に動的型付け言語や文字列ベースディスパッチ等に限られる、と明記）。結果は`[{"description", "file", "startLine", "endLine", "severity"}]`（空配列可）として`CROSS_CUTTING_FINDINGS_JSON`に書かせる
- findingsが空配列なら、verifierを起動せずそのままsynthesizeへ直行する（`_detect_review_phase`の`if findings and not CROSS_CUTTING_VERIFIED_JSON.exists()`）
- findingsが1件以上あれば`phase: "cross_cutting_verify"`。`_cross_cutting_verify_task`（`:1773`）はexplorerとは別コンテキストの独立したサブエージェントに、LSPや実コードを確認させて各指摘が誤検知でないか判定させる。redoはせず、確信が持てない指摘は破棄する（人間に無駄な確認をさせないため）。確認できたものだけを`CROSS_CUTTING_VERIFIED_JSON`に書かせる（空配列可）

横断的チェックの指摘は確認できたものであっても**自動修正しない**。常に最終レポートに上がり、review gateで人間が判断する（ADR-0011）。

なお、explorerには1起動内の探索ターン数の上限が機構として存在せず、探索範囲を絞る指示による自主規制だけが効いている（`docs/design/pipeline.md`の予算管理を参照）。

## synthesize（最終レポート統合）

全観点収束かつ横断的チェック完了後、`_detect_review_phase`は`phase: "synthesize"`を返す。`_synthesize_task`（`:2266`）が生成するタスクは`FINAL_REPORT_MD`（`review_results/final_report.md`）と`COMMIT_MESSAGE_FILE`（`.masuda-commit-message`）という別々の2ファイルをサブエージェントに書かせる。

レポート本文のうち以下4セクションは、Pythonの純粋関数がオーケストレーター側で確定的に組み立て（LLM要約を経由しない）、サブエージェントには「内容を変更・要約せずそのまま追記せよ」と指示する。人間への報告を正確に保つための決定的な記述であり、AIの解釈を挟まない。

- `_fixed_section`（`:2213`）: `rs["fixed"]`——自動修正・再検証まで確認済みの観点一覧
- `_unresolved_section`（`:2193`）: `rs["unresolved"]`——`review_check_not_converged`/`fix_not_resolved`という理由付きで、人間の直接確認を促す
- `_cross_cutting_section`（`:2225`）: `CROSS_CUTTING_VERIFIED_JSON`——独立検証済みの横断的チェック指摘
- `_interim_carried_section`（`:2243`）: `INTERIM_CARRIED_FINDINGS_KEY`——Build段階の途中レビューで自動修正が収束せず、G1再オープンで「そのまま進める」と承認された指摘（`docs/design/build.md`が所有する経路の結果をここで表示するだけ）

サブエージェントが自分の判断で書くのは「## サマリー」（自動修正件数・未解決件数・横断的チェック件数と1〜2文の総評）と「## 問題なし」（`rs["clean"]`——review/checkの往復を経ても問題が検出されなかった観点）の2セクションのみ。

コミットメッセージは`git diff --cached {base_ref}`で見える全変更（Build段階以降の全ステップ分の累積）に対して書く。対象リポジトリ自身のコミット規約（CLAUDE.md・CONTRIBUTING.md、無ければ`git log`の直近コミット）を確認して従うよう指示し、masuda固有の文言（「masudaによる自動commit」等）は含めないよう明示する——このコミットは`masuda review approve`実行時にワークスペースの成果をそのまま1つの開発者コミットとして記録するためのもので（`docs/design/gates.md`）、通常の開発コミットと区別する情報を書く理由がない。

`_detect_post_implementation_phase`（`:1113`）は`FINAL_REPORT_MD`と`COMMIT_MESSAGE_FILE`が両方揃うまでは`_detect_review_phase`に戻り続ける。両方揃った後はreview gateマーカー（`gate:review`）を見る: `pending`なら`phase: "await_g2"`（`GATE:review`終了条件、`masuda review show/approve/reject`待ち）。`rejected`なら`_clear_review_state()`が`REVIEW_RESULTS_DIR`配下の全ファイルとdaemon側の`internal:review-state`キーを削除し（ADR-0013——却下後の再レビューはreview/check結果を一切再利用せず観点0から完全にやり直す）、`implement_g2_redo`という単一の再実装パスへ差し戻す（このパス自体の実装は`docs/design/build.md`が所有）。

`.masuda/reviews/*.md`ファイル自体の配布・`masuda update`による新規観点の追加同期（追加のみ・上書きなし）は`docs/design/distribution-and-update.md`「レビュー観点の配布・同期」節が所有する。このドキュメントが扱うのは、既にリポジトリに存在するファイルをオーケストレーターがどう解釈・実行するかまで。

## レビュー単体実行

`masuda review start <branch-or-ref> [--base develop]`（`cmd/masuda/review.go:30`）は、Provision（worktree作成）〜Review（レビュー）の機構をそのまま使い回しつつ、Build段階を丸ごとスキップして直接Reviewから始める既存ブランチ向けの入口（ADR-0046）。

- `seedReviewOnly(id)`（`cmd/masuda/review.go:126`）が状態ディレクトリに`implementation_result.json`を`{"status": "done"}`で直接書き込む（daemon経由ではなく平ファイル——ワークスペースの状態daemonがまだ起動していない時点でGo側から呼ばれるため）。これにより`detect_phase`（`:1387`）は最初の呼び出しから`_detect_post_implementation_phase`に直行し、Build段階の実装・機械的バックストップ・途中レビューを一切経由しない
- `plan/steps.json`が存在しないため、`_iteration_budget()`（`:635`）は`BASE_BUDGET`のみにフォールバックし、ADR-0010の機械的バックストップも（`PLAN_STEPS_JSON.exists()`チェックにより）自動的にスキップされる——このワークスペースはG1（plan gate）を一度も経由していないので、比較対象となる「承認された計画」自体が無い
- diffの基準refが通常と異なる。`_read_base_ref()`（`:439`）は状態ディレクトリの`.masuda-base-ref`（`worktree.Create`が作成時に記録）を読む点は共通だが、`_compute_diff()`が`git diff --cached <base_ref>`で見るのは、通常のBuild段階なら「まだcommitされていない実装差分」であるのに対し、レビュー単体では対象ブランチは既にcommit済みなので、この差分がそのままブランチの実装内容そのものになる（`git diff HEAD`では常に空になってしまうため、bare HEADではなくbase_ref基準であることがここで意味を持つ）

## 既知の問題

未調査。修正時はここから消す。

- **`orchestrator/implement_review_graph.py:19` のモジュールdocstringが「13-perspective」のまま**。実際は14観点
