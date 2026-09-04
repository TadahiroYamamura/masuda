# ゲート

plan gate・review gate・triage gateの3つについて、承認マーカーの管理、表示アーティファクトの整形、操作CLI、ループ側の待機プロトコルを扱う。待機の実体（`wait_for_gate_resolution`・KVストア）は`docs/design/state-daemon-mcp.md`、バックストップが逸脱を検知して再オープンする側の処理は`docs/design/build.md`、worktreeのcommit/pull/削除の実装は`docs/design/workspace.md`を参照。

## ゲートマーカー

マーカーは状態デーモンのキーであり、ファイルではない（`internal/gate/gate.go`）。

| ゲート | キー |
|---|---|
| plan | `gate:plan` |
| review | `gate:review` |
| triage | `gate:triage` |

`internal/gate.Marker`（`{status, feedback, decided_at}`のJSON）が値。`Status`は`approved`・`rejected`・`halted`の3値で、`halted`はtriage専用（後述）で plan/review では使わない。**`pending`は存在しない**——まだ誰も判断していないゲートはキーそのものが無い（ADR-0055）。

- `Approve(ctx, stateDir, n, feedback)` / `Reject(ctx, stateDir, n, feedback)`: マーカーを`approved`/`rejected`で書き込むだけで、`artifact:DEVIATION.md`を含む他のキーには触らない。逸脱の理由は決定に付属するものであり、決定を消費する側がマーカーと一緒に消す（後述）
- `Halt(ctx, stateDir, n, reason)`: `halted`で書き込む。他のマーカー・ファイルは一切変更しない（triage_concern.jsonも残す）

懸念内容そのもの（`triage_concern.json`）だけは例外で、サブエージェントが直接書くプレーンファイルのまま状態ディレクトリ直下に置かれる。これはmasuda自身のコード（Go CLI・`orchestrator/*.py`）だけがデーモンキーの読み書きをする、という設計境界の外側にある（サブエージェントはデーモンへの書き込み手段を持たない）。

### 誰が開き、誰が閉じ、閉じたら何が起きるか

**plan gate**

- 開く: `orchestrator/investigate_plan_graph.py`の`detect_phase`が`plan/summary.md`＋`plan/steps.json`の存在を検知し、マーカーが無いとき`await_g1`状態に遷移する（初回オープン）。Build段階での再オープンは`docs/design/build.md`の機械的バックストップ・自己申告逸脱を参照
- 閉じる: `masuda plan approve|reject <workspace-id> [feedback]`（人間、CLIから）、または`masuda chat`での対話中に`resolve_gate_from_chat`（Claude自身、`name: "plan"`）
- 閉じた後: `approved`なら`investigate_plan_graph.py`の`detect_phase`が`internal:plan-approved`キーへマーカーを移して`g1_approved`へ進み、VMサンドボックス起動を経てBuild段階へ進む。`rejected`なら`internal:plan-redo-pending`キーへfeedbackを書くのと同時に`gate:plan`を消し、`plan/summary.md`・`plan/steps.json`を削除して`plan_redo`状態（Blueprintのやり直し）に遷移する（ADR-0039: 削除直後・再作成前の「redo未完了」はファイル存在では判定できないため、このキーで橋渡しする）

**review gate**

- 開く: Review段階が`final_report.md`を書き終えた時点で`orchestrator/implement_review_graph.py`が`await_g2`状態に遷移する
- 閉じる: `masuda review approve|reject <workspace-id> [feedback]`のみ。**`resolve_gate_from_chat`は`name: "review"`をサーバー側で拒否する**（ADR-0060）。承認は`finalizeReviewApproval`（後述）まで含み、作業ブランチを人間の実リポジトリへ反映してワークスペースを削除するため、ホスト側でしか行えず、人間が自分の手で起動すべき操作でもある
- 閉じた後: `rejected`ならBuild段階へ差し戻し、全観点のレビューをやり直す（ADR-0013）。`approved`なら`cmd/masuda/gate.go`の`finalizeReviewApproval`が走る——サンドボックス停止（起動中なら）→`worktree.Commit`（Review段階のfixerが加えた分だけ、Build段階の各ステップは既に個別commit済み）→`worktree.Pull`（fast-forwardのみ、ADR-0023）→`worktree.Remove`（ブランチは残す、`deleteBranch=false`——ADR-0023でブランチ自体がユーザーに渡す成果物になったため）→`workspace.Remove`。いずれかのステップが失敗すると後続は実行されない

**triage gate**

- 開く: どちらのオーケストレーターも`detect_phase`の先頭で`triage_concern.json`の存在を最優先チェックする（他の進行中フェーズより先に評価される）。サブエージェントが懸念を検知してこのファイルを書いた時点で、次の`detect_phase`呼び出しで`await_triage`状態に強制遷移する
- 閉じる: `masuda triage dismiss|redo|halt <workspace-id> [feedback/reason]`のみ。**`resolve_gate_from_chat`は`name: "triage"`をサーバー側で拒否する**（`internal/statedaemon/mcpserver/curated.go`の`chatResolvableGateNames`は`"plan"`のみ）。懸念の対象になっているエージェント自身が、chatでの会話を理由に自分でこのゲートを閉じることを技術的に禁止している（ADR-0029）
- 閉じた後:
  - `dismiss`（`gate.Approve`のエイリアス、`cmd/masuda/triage.go`の`newTriageDismissCommand`）: 誤検知として続行。`triage_concern.json`を削除し`gate:triage`キーも消費したうえで、中断していたフェーズを`resume_phase_fn`で再導出する（何も状態を進めず、割り込み前の状態をゼロから再計算するだけ）
  - `redo`（`gate.Reject`のエイリアス）: 同様に消費・再導出するが、feedbackを`internal:triage-redo-feedback`キーに書くのと同時に消費する。次の`write_task_md`がこのキーを一度だけ読み、TASK.mdの先頭に「triage対応後の申し送り」として差し込む（一発読み切りのマーカー）
  - `halt`（`gate.Halt`）: 自動再開経路なし。マーカーは`internal:triage-halted`キーへ移して消費するが、`triage_concern.json`は削除しない（`masuda triage show`が事後もそのまま見られる）。ループ側は`DONE (triage halted)`としてセッションを終了する


### chat内で解決できるゲートの規則

`resolve_gate_from_chat`が受け付けるのは`plan`だけである（`chatResolvableGateNames`）。基準は「**そのゲートの承認がワークスペースの外に影響しないか**」の一点で、3つとも同じ規則で説明できる（ADR-0060）。

| ゲート | chat自己解決 | 承認が引き起こすこと |
|---|---|---|
| `plan` | 可 | ループが次の段階へ進むだけ |
| `review` | 不可 | ブランチを実リポジトリへfast-forward反映し、clone・状態ディレクトリを削除する |
| `triage` | 不可 | 懸念の対象が自分で閉じることになる（ADR-0029） |

ADR-0057以降、ゲストがゲートマーカーを書く経路はこのツールしか無いため、この表は規約ではなく技術的な境界になっている。

## ゲートマーカーの消費タイミング

`gate:<name>`キーが存在することは、**人間が下した決定のうち、まだ誰も消費していないものがある**ことだけを意味する（ADR-0055）。生成するのは人間の決定のみ、削除するのは消費者が「その決定をどう扱ったか」を耐久キーへ書いたのと同じ`state_apply`の中だけ、という1つの規則で3ゲートすべてが動く。

承認・haltという事実はマーカーを残して表すのではなく、専用キーが持つ。マーカーが無いとき`detect_phase`はこれらを見て、消費済みの決定を再導出する。

| キー | 何の記録か |
|---|---|
| `internal:plan-approved` | G1承認 |
| `internal:review-approved` | G2承認 |
| `internal:triage-halted` | triage halt |
| `internal:plan-redo-pending` | G1却下（ADR-0039） |
| `internal:review-feedback` | G2却下（G2 redoサイクル中であることも兼ねる） |
| `internal:triage-redo-feedback` | triage redo |

消費は`state_client.consume(key, follow_up)`（`orchestrator/state_client.py`）が行う。値を読み、`follow_up(value)`が返す耐久記録のputとマーカーのdeleteを1回の`state_apply`に束ねる。checkが外れたら——読んでから適用するまでに人間が決定を覆したら——何も変えずに読み直す。想定外の`status`を持つマーカーは、消費する前に例外で落とす。

plan gateの再オープン（`orchestrator/implement_review_graph.py`の`_resolve_gate_reopen`）では、まず`artifact:DEVIATION.md`キーの存在で「今回の逸脱を記録済みか」を判定する。存在しなければ`plan_reopened`状態に入る。記録済みでマーカーがまだ無ければ、同じ`plan_reopened`状態を返し続ける。マーカーが現れて初めて`artifact:DEVIATION.md`と`gate:plan`を同じ`state_apply`で消し、承認済み逸脱なら`internal:approved-deviations`キーに追記して以後の機械的バックストップから除外する（バックストップ側の詳細は`docs/design/build.md`）。ADR-0010のBuild段階の逸脱に加え、ADR-0027の途中レビュー未解決エスカレーションもこの同じプリミティブを再利用する。

ファイル成果物の削除（`triage_concern.json`、G2却下時の`.masuda-commit-message`）は`state_apply`に載せられない。これらは「次の`detect_phase`がその分岐へ再入するのをせき止める」役割を持つため、**マーカーの消費より前**に行う。

## トリアージゲート機構の重複実装

トリアージゲートのdismiss/redo/haltの解決ロジックは`cmd/masuda/triage.go`（Go、CLI側）に加え、`orchestrator/investigate_plan_graph.py`の`_resolve_triage`（`detect_phase`から最優先で呼ばれる、195行目付近の`TRIAGE_CONCERN_JSON.exists()`チェック起点）と`orchestrator/implement_review_graph.py`の`_resolve_triage`（`detect_phase`から同様に呼ばれる）の2箇所に、**意図的に非共有で重複実装**されている。

両者はほぼ同一のロジック（マーカーが無ければ待機状態（または`internal:triage-halted`があればhalted状態）を返す、`halted`ならマーカーを消費してhalted状態を返す、それ以外は`triage_concern.json`を削除しマーカーを消費して中断前のフェーズを再導出する）だが、返す`State`の形が異なる（`investigate_plan_graph.py`は`{phase, retries, questions}`、`implement_review_graph.py`は`{phase, reason}`）ため、共通関数化されていない。Discovery/Blueprint段階とBuild/Review段階は別プロセス・別オーケストレーターとして動くため、この2つは共有モジュールを持たない構成が前提になっている——**triageゲートのロジックを変更するときは、`orchestrator/investigate_plan_graph.py`と`orchestrator/implement_review_graph.py`の両方を同期して直すこと**。片方だけ直すと、Discovery/Blueprint段階とBuild/Review段階のどちらかでtriageゲートの挙動が食い違う。

`masuda triage dismiss/redo/halt`自体（Go CLI側）は段階を問わず共通の1実装（`internal/gate`パッケージへの薄いラッパー）であり、重複の対象はPython側の2オーケストレーターだけである点に注意。

## ループ終了・ゲート条件仕様

`runtime/CLAUDE.md`（Build/Review段階、サンドボックス内で動くClaudeセッション向け）が定義するループ本体は次の通り。

1. `/masuda-state/TASK.md`が存在しなければオーケストレーターを起動して生成させる
2. `TASK.md`を読み、指示に従って作業する
3. 作業完了後、**終了条件**（`TASK.md`本文に`DONE`という文字列を含む）と**ゲート条件**（`GATE:<name>`という文字列を含む、`<name>`は`plan`/`review`/`triage`のいずれか）を確認する。両者は排他——`DONE`ならtmuxセッションをkillしてセッション終了（コミット・質問・確認は不要）。`GATE:<name>`なら4へ。どちらもなければオーケストレーターを起動してTASK.mdを上書きさせ2へ戻る
4. `mcp__masuda-gate__wait_for_gate_resolution`ツールを`name`にゲート名を渡して呼び、人間がゲートを解決するまでブロッキング待機する（ADR-0042。ゲートマーカーが存在するまで待つ1回のブロッキング呼び出しで、既に存在すれば即座に返る（ADR-0055）。ポーリングもwhileループも不要）
   - 待機中に`masuda chat`で接続した人間が「進めていい」と伝えた場合、Claude自身が`mcp__masuda-gate__resolve_gate_from_chat`を呼んでよい（`name`・`status`（`"approved"`/`"rejected"`）・`feedback`）。ただしこの経路を使えるのは`plan`だけで、`review`・`triage`はサーバー側で拒否される（前述、ADR-0060・ADR-0029）
   - `wait_for_gate_resolution`が返ったら（`resolve_gate_from_chat`経由・別ターミナルの`masuda plan/review/triage approve|reject|dismiss|redo|halt`経由のどちらでも）2へ戻る

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

