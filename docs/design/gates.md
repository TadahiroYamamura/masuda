# ゲート

plan gate・review gate・triage gateの3つについて、承認マーカーの管理、表示アーティファクトの整形、操作CLI、ループ側の待機プロトコルを扱う。待機の実体（`wait_for_gate_change`・KVストア）は`docs/design/state-daemon-mcp.md`、バックストップが逸脱を検知して再オープンする側の処理は`docs/design/build.md`、worktreeのcommit/pull/削除の実装は`docs/design/workspace.md`を参照。

## ゲートマーカー

マーカーは状態デーモンのキーであり、ファイルではない（`internal/gate/gate.go`）。

| ゲート | キー |
|---|---|
| plan | `gate:plan` |
| review | `gate:review` |
| triage | `gate:triage` |

`internal/gate.Marker`（`{status, feedback, decided_at}`のJSON）が値。`Status`は`pending`（未設定＝キー不在として扱う）・`approved`・`rejected`・`halted`の4値だが、`halted`はtriage専用（後述）で plan/review では使わない。

- `Approve(ctx, stateDir, n, feedback)` / `Reject(ctx, stateDir, n, feedback)`: マーカーを`approved`/`rejected`で書き込む。両方とも先に`clearDeviation`（後述の`artifact:DEVIATION.md`削除）を呼ぶ
- `Halt(ctx, stateDir, n, reason)`: `halted`で書き込む。他のマーカー・ファイルは一切変更しない（triage_concern.jsonも残す）
- `Read(ctx, stateDir, n)`: キー不在なら`Marker{Status: Pending}`のゼロ値を返す

懸念内容そのもの（`triage_concern.json`）だけは例外で、サブエージェントが直接書くプレーンファイルのまま状態ディレクトリ直下に置かれる。これはmasuda自身のコード（Go CLI・`orchestrator/*.py`）だけがデーモンキーの読み書きをする、という設計境界の外側にある（サブエージェントはデーモンへの書き込み手段を持たない）。

### 誰が開き、誰が閉じ、閉じたら何が起きるか

**plan gate**

- 開く: `orchestrator/investigate_plan_graph.py`の`detect_phase`が`plan/summary.md`＋`plan/steps.json`の存在を検知し、マーカーが`pending`（未設定）のとき`await_g1`状態に遷移する（初回オープン）。Build段階での再オープンは`docs/design/build.md`の機械的バックストップ・自己申告逸脱を参照
- 閉じる: `masuda plan approve|reject <workspace-id> [feedback]`（人間、CLIから）、または`masuda chat`での対話中に`resolve_gate_from_chat`（Claude自身、`name: "plan"`）
- 閉じた後: `approved`なら`investigate_plan_graph.py`の`detect_phase`が`g1_approved`へ進み、Docker/VMサンドボックス起動を経てBuild段階へ進む。`rejected`なら`GATE_KEY`を削除し`plan/summary.md`・`plan/steps.json`を削除したうえで`internal:plan-redo-pending`キーにfeedbackを書き、`plan_redo`状態（Blueprintのやり直し）に遷移する（ADR-0039: 削除直後・再作成前の「redo未完了」はファイル存在では判定できないため、このキーで橋渡しする）

**review gate**

- 開く: Review段階が`final_report.md`を書き終えた時点で`orchestrator/implement_review_graph.py`が`await_g2`状態に遷移する
- 閉じる: `masuda review approve|reject <workspace-id> [feedback]`、または`resolve_gate_from_chat`（`name: "review"`）
- 閉じた後: `rejected`ならBuild段階へ差し戻し、全観点のレビューをやり直す（ADR-0013）。`approved`なら`cmd/masuda/gate.go`の`finalizeReviewApproval`が走る——サンドボックス停止（起動中なら）→`worktree.Commit`（Review段階のfixerが加えた分だけ、Build段階の各ステップは既に個別commit済み）→`worktree.Pull`（fast-forwardのみ、ADR-0023）→`worktree.Remove`（ブランチは残す、`deleteBranch=false`——ADR-0023でブランチ自体がユーザーに渡す成果物になったため）→`workspace.Remove`。いずれかのステップが失敗すると後続は実行されない

**triage gate**

- 開く: どちらのオーケストレーターも`detect_phase`の先頭で`triage_concern.json`の存在を最優先チェックする（他の進行中フェーズより先に評価される）。サブエージェントが懸念を検知してこのファイルを書いた時点で、次の`detect_phase`呼び出しで`await_triage`状態に強制遷移する
- 閉じる: `masuda triage dismiss|redo|halt <workspace-id> [feedback/reason]`のみ。**`resolve_gate_from_chat`は`name: "triage"`をサーバー側で拒否する**（`internal/statedaemon/mcpserver/curated.go`の`chatResolvableGateNames = {"plan", "review"}`が`"triage"`を含まない）。懸念の対象になっているエージェント自身が、chatでの会話を理由に自分でこのゲートを閉じることを技術的に禁止している（ADR-0029）
- 閉じた後:
  - `dismiss`（`gate.Approve`のエイリアス、`cmd/masuda/triage.go`の`newTriageDismissCommand`）: 誤検知として続行。`triage_concern.json`を削除し`gate:triage`キーも削除したうえで、中断していたフェーズを`resume_phase_fn`で再導出する（何も状態を進めず、割り込み前の状態をゼロから再計算するだけ）
  - `redo`（`gate.Reject`のエイリアス）: 同様に削除・再導出するが、feedbackを`internal:triage-redo-feedback`キーに書いてから再導出する。次の`write_task_md`がこのキーを一度だけ読み、TASK.mdの先頭に「triage対応後の申し送り」として差し込む（一発読み切りのマーカー）
  - `halt`（`gate.Halt`）: 自動再開経路なし。`triage_concern.json`・`gate:triage`キーとも一切削除しない（`masuda triage show`が事後もそのまま見られる）。ループ側は`DONE (triage halted)`としてセッションを終了する

## ゲートマーカーの消費タイミング

マーカーの削除（消費）は、再オープン・新規オープンを検知した瞬間ではなく、実際に人間の判断（`approved`/`rejected`/`halted`のいずれかがマーカーとして書かれる)が下された時点でのみ行う。

plan gateの再オープン（`orchestrator/implement_review_graph.py`の`_resolve_gate_reopen`）を例に取ると、逸脱を新規検知した際にまず`artifact:DEVIATION.md`キーの存在で「今回の逸脱はまだ処理していないか」を判定する。存在しなければ、`gate:plan`キーを削除してから`plan_reopened`状態に入る——この削除は「新規検知時に古いマーカーが残っていれば消す」ためのものであり、今回の判断を消費しているわけではない。これは、初回のplan gate承認時のマーカーが`investigate_plan_graph.py`側では削除されず残り続けることに対応する処置で、削除しないと過去の別の承認（何時間も前の`approved`マーカー）が今回の逸脱に対する判断として誤って消費されてしまう。

`artifact:DEVIATION.md`が存在する状態（＝逸脱を記録済み）でマーカーがまだ`pending`なら、削除も消費もせず同じ`plan_reopened`状態を返し続ける。マーカーが`approved`または`rejected`になって初めて、`artifact:DEVIATION.md`と`gate:plan`の両方を削除し（＝ここが実際の消費タイミング）、承認済み逸脱なら`internal:approved-deviations`キーに追記して以後の機械的バックストップから除外する（バックストップ側の詳細は`docs/design/build.md`）。

この2段階（新規検知時の掃除／解決時の消費）を1つの関数にまとめたものが`_resolve_gate_reopen`（`orchestrator/implement_review_graph.py`）で、ADR-0010のBuild段階の逸脱に加え、ADR-0027の途中レビュー未解決エスカレーションもこの同じプリミティブを再利用する。

triageゲートは`_resolve_triage`が同じ2段階構造を独自実装で持つ（`dismiss`/`redo`時のみ`triage_concern.json`・`gate:triage`を削除、`halt`時は両方とも残す）。

## ゲート表示アーティファクトの整形

`internal/gate/gate.go`の`Show(ctx, stateDir, n)`が、そのゲートが判定対象にしているアーティファクトを人間向けMarkdownとして返す（`masuda plan/review/triage show`が呼ぶ）。

- **plan**: `renderPlan(stateDir)`が`plan/summary.md`（自由記述のプレーンテキスト）と`plan/steps.json`（構造化データ、ADR-0026）を読み、次の順でMarkdownを組み立てる。
  1. `plan/summary.md`の内容をそのまま
  2. 「変更するファイル一覧」: 全ステップの`files`のdedup union（別途著者が書く一覧ではなく機械的に導出（ADR-0026））
  3. 「実装のステップ分解」: ステップごとの説明＋`files`一覧（TDDモードのステップは末尾に「（TDDモード）」を付記）
  4. 「生成される可能性のある副産物ファイル」: `expected_byproducts`（ADR-0028）が1件以上あるときだけ追加
  さらに`gate.Show`側で、`artifact:DEVIATION.md`キーが存在すれば`# G1 reopened — deviation reported (ADR-0010)`を先頭に前置する（`Show`本体のPlan分岐の外、共通処理として）
- **review**: `artifactPaths[Review] = "review_results/final_report.md"`をそのまま読んで返す（整形なし）
- **triage**: `renderTriageConcern(stateDir)`が`triage_concern.json`（`agent`/`phase`/`description`/`evidence`/`reported_at`）を読み、Markdownの見出し＋箇条書きに整形する。planと異なり前置ロジックはなく、`triage_concern.json`の内容をそのまま整形するだけ

**同期制約**: `renderPlan`と同じ組み立てロジックが`orchestrator/implement_review_graph.py`の`_render_plan_text()`にも独立実装として存在する。実装フェーズのサブエージェントに`masuda plan show`相当のplan全体コンテキストを与えるために使われ、Go側とは共通コード化されていない。両者が組み立てる本体（summary.md本文→「変更するファイル一覧」→「実装のステップ分解」→`expected_byproducts`、の順・見出し文言）は一致していなければならない（`_render_plan_text`は`artifact:DEVIATION.md`の前置は行わない——`gate.Show`側だけがCLI表示用にこれを追加で前置する別レイヤーの処理）。`renderPlan`または`_render_plan_text`の組み立て順・見出し・フィールド名のどれかを変更するときは、もう一方も追随させること。

## ゲート操作CLI

`cmd/masuda/gate.go`の`newGateCommand(n gate.Name)`が plan/review 共通のshow/approve/rejectサブコマンド群を構築する（`cmd/masuda/main.go`が`gate.Plan`/`gate.Review`それぞれに対して呼び出し、`masuda plan ...`・`masuda review ...`として登録する）。

- `show <workspace-id>`: `gate.Show`をそのまま標準出力へ
- `approve <workspace-id> [feedback]`: `gate.Approve`を呼び、`n == gate.Review`のときだけ追加で`finalizeReviewApproval`（前述）を実行する
- `reject <workspace-id> <feedback>`: `gate.Reject`。feedbackは必須引数（次のBuild/Blueprintパスへ渡る唯一の入力のため）

いずれも`gateStateDir(id)`でワークスペースIDを状態ディレクトリの絶対パスへ解決してから`internal/gate`パッケージへ委譲する。worktree自体のパスではなく状態ディレクトリを扱う。

triageは別コマンドグループ`cmd/masuda/triage.go`の`newTriageCommand`（`masuda triage ...`）で、show/dismiss/redo/haltの4つ。`dismiss`は`gate.Approve`、`redo`は`gate.Reject`をそのまま呼ぶラッパーで、`halt`だけが`gate.Halt`という別関数を呼ぶ（他の3つと違い、サンドボックス停止もworktree操作も一切行わない——ADR-0029の設計で、人間が手動で調査する以外の自動復帰経路を持たせないため）。

## トリアージゲート機構の重複実装

トリアージゲートのdismiss/redo/haltの解決ロジックは`cmd/masuda/triage.go`（Go、CLI側）に加え、`orchestrator/investigate_plan_graph.py`の`_resolve_triage`（`detect_phase`から最優先で呼ばれる、195行目付近の`TRIAGE_CONCERN_JSON.exists()`チェック起点）と`orchestrator/implement_review_graph.py`の`_resolve_triage`（`detect_phase`から同様に呼ばれる）の2箇所に、**意図的に非共有で重複実装**されている。

両者はほぼ同一のロジック（`pending`なら待機状態を返す、`halted`ならhalted状態を返す、それ以外は`triage_concern.json`と`gate:triage`キーを削除して中断前のフェーズを再導出する）だが、返す`State`の形が異なる（`investigate_plan_graph.py`は`{phase, retries, questions}`、`implement_review_graph.py`は`{phase, reason}`）ため、共通関数化されていない。Discovery/Blueprint段階とBuild/Review段階は別プロセス・別オーケストレーターとして動くため、この2つは共有モジュールを持たない構成が前提になっている——**triageゲートのロジックを変更するときは、`orchestrator/investigate_plan_graph.py`と`orchestrator/implement_review_graph.py`の両方を同期して直すこと**。片方だけ直すと、Discovery/Blueprint段階とBuild/Review段階のどちらかでtriageゲートの挙動が食い違う。

`masuda triage dismiss/redo/halt`自体（Go CLI側）は段階を問わず共通の1実装（`internal/gate`パッケージへの薄いラッパー）であり、重複の対象はPython側の2オーケストレーターだけである点に注意。

## ループ終了・ゲート条件仕様

`runtime/CLAUDE.md`（Build/Review段階、サンドボックス内で動くClaudeセッション向け）が定義するループ本体は次の通り。

1. `/masuda-state/TASK.md`が存在しなければオーケストレーターを起動して生成させる
2. `TASK.md`を読み、指示に従って作業する
3. 作業完了後、**終了条件**（`TASK.md`本文に`DONE`という文字列を含む）と**ゲート条件**（`GATE:<name>`という文字列を含む、`<name>`は`plan`/`review`/`triage`のいずれか）を確認する。両者は排他——`DONE`ならtmuxセッションをkillしてセッション終了（コミット・質問・確認は不要）。`GATE:<name>`なら4へ。どちらもなければオーケストレーターを起動してTASK.mdを上書きさせ2へ戻る
4. `mcp__masuda-gate__wait_for_gate_change`ツールを`name`にゲート名を渡して呼び、人間がゲートを解決するまでブロッキング待機する（ADR-0042、`state_wait_for_change`の一発勝負のワンショット待機をそのまま使う。ポーリングもwhileループも不要）
   - 待機中に`masuda chat`で接続した人間が「進めていい」と伝えた場合、Claude自身が`mcp__masuda-gate__resolve_gate_from_chat`を呼んでよい（`name`・`status`（`"approved"`/`"rejected"`）・`feedback`）。ただし`triage`はこの経路を使えない（前述、サーバー側で拒否）
   - `wait_for_gate_change`が返ったら（`resolve_gate_from_chat`経由・別ターミナルの`masuda plan/review/triage approve|reject|dismiss|redo|halt`経由のどちらでも）2へ戻る

Discovery/Blueprint段階（ホスト側で動く、サンドボックスなし）は`internal/hostloop/system_prompt.md.tmpl`が同じ仕組みを別テンプレートとして持つ。差分は次の2点のみ。

- ゲート名の選択肢が`plan`/`triage`の2つだけ（review gateはBuild/Review段階でしか開かないため、このテンプレートには出てこない）
- オーケストレーター起動コマンドが`{{.Python}} {{.Orchestrator}}`のテンプレート変数展開（VM内固定パスではなくホスト側のPython/オーケストレーターパスを注入する）

いずれのテンプレートも、終了条件とゲート条件は`TASK.md`本文の文字列マッチという同一の仕組みで判定される。TASK.md自体を書くのは各オーケストレーター（`write_task_md`、State遷移から`# GATE:plan`等の見出しを含むMarkdownを生成する）で、ゲート側のコード（`internal/gate`・`cmd/masuda`）はTASK.mdの生成には関与しない。

## 既知の問題

未調査。修正時はここから消す。

- **ゲート待機時の案内が、存在しないコマンドを提示する**: `masuda plan chat` / `masuda review chat` は CLI に存在せず（`masuda chat <workspace-id>` に統一済み）、以下の4箇所が生成する指示文にこの旧コマンド名が残っている。人間に打てないコマンドを案内している状態。
  - `internal/hostloop/system_prompt.md.tmpl:20`（エージェントのシステムプロンプト）
  - `orchestrator/investigate_plan_graph.py:494`（plan gate到達時のTASK.md）
  - `orchestrator/implement_review_graph.py:1797`（plan gate再オープン時のTASK.md）
  - `orchestrator/implement_review_graph.py:2333`（review gate到達時のTASK.md）

  あわせて `orchestrator/tests/test_investigate_plan_graph.py:322` と `test_implement_review_graph.py:1588` が旧コマンド名をアサートしているため、修正時はテストも直す必要がある
