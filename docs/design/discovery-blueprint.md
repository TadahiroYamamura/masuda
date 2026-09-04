# Discovery / Blueprint（調査・プラン）

Discovery（調査）・Blueprint（プラン作成）はサンドボックス（VM）を起動せず、ホストOS上で直接動く。`masuda plan start`がworktree作成後にホストループ（`internal/hostloop`）を起動し、そのループの各反復がホスト側オーケストレーター（`orchestrator/investigate_plan_graph.py`）を子プロセスとして呼び出してTASK.mdを書き換える、という構成。段階・ゲートの正式名称は`docs/glossary.md`を参照。

## Plan開始CLI（`masuda plan start`）

`cmd/masuda/plan.go`の`newPlanStartCommand`。第1引数が既存ワークスペースID（`workspace.Exists`で判定）かどうかで新規/再開の2経路に分岐する。

- **新規**（`masuda plan start <branch> "<task>"`）: `resolveBase`でbaseブランチを解決し`newWorkspace`でワークスペースID発行＋worktree作成。`--file`が指定されていれば`hostloop.WriteInstructions`で内容をstateDirへコピーする。`hostloop.Start(id, worktreeDir, stateDir, task)`より前に完了させる
- **再開**（`masuda plan start <workspace-id>`）: taskを省略しなければならない。`--file`・`--name`のいずれかが指定されていればエラー（新規作成時にしか意味を持たないため）。`workspace.Load`でbranch名等を引き、`startDaemon(info.ID)`で状態デーモンが落ちていれば再起動してから`hostloop.Start`を呼ぶ

新規・再開どちらも最終的に`hostloop.Start`を呼ぶ点は共通で、`task`引数が空文字列かどうかだけがStart側の分岐材料になる（後述）。

## ホストランタイム構築（`internal/hostloop/bootstrap.go`）

`ensureRuntime()`が`renderSystemPrompt`（後述）から呼ばれ、オーケストレーター実行に必要なファイル一式を揃えて`(pythonPath, runtimeDir)`を返す。展開先は`runtimeDir()` = `workspace.DataHome()/runtime`（対象リポジトリの種類に依存しない、masuda自身の固定ディレクトリ）。

1. `masuda.InvestigatePlanScript`（`orchestrator/investigate_plan_graph.py`の`go:embed`）を`investigate_plan_graph.py`として**毎回**上書き展開する（実行中のmasudaバイナリと常に同期させるため、venvの再構築要否とは無関係に無条件で行う）
2. `masuda.ImplementReviewScript`（`orchestrator/implement_review_graph.py`）も同じディレクトリへ展開する。Build/Reviewのオーケストレーターもホストで動くようになり（ADR-0057）、venvとstate_client.pyをこのDiscovery/Blueprint用ランタイムと共有するため——展開はまとめて1回で行い、`hostloop.EnsureImplementReviewOrchestrator()`がそのパスを返す
3. `masuda.StateClientScript`（`orchestrator/state_client.py`）も同様に`state_client.py`として展開する。両オーケストレーターがsiblingモジュールとしてimportするため、同じディレクトリに置く必要がある
4. venv: `<runtimeDir>/venv`の`bin/python`が既に存在し、かつ`.masuda-requirements-sha256`マーカー（このファイルだけは`atomicWrite`ではなく素の`os.WriteFile`、`bootstrap.go:158`）が`masuda.Requirements`のSHA-256と一致していれば再利用する。一致しなければ`buildVenv`が`<runtimeDir>/venv.tmp-*`という一時ディレクトリに`python3 -m venv`＋`pip install -r requirements.txt`でフルビルドし、成功時にのみ`os.Rename`で`venv`へ差し替える（失敗や中断で壊れたvenvが「有効なvenv」と誤認されることはない）

`atomicWrite`（同ディレクトリの一時ファイル＋rename）をスクリプト展開と`requirements.txt`書き出しに使っており（`bootstrap.go:82,91,142`）、書き込み中に他プロセスが不完全なファイルを読むことはない。

## セッション起動・ライフサイクル（`internal/hostloop/hostloop.go`）

`Start(id, worktreeDir, stateDir, task)`はtmuxセッション`SessionName(id)`（`masuda-plan-<sanitized-id>`）を起動する。`IsRunning(id)`（`tmux has-session`）が真なら即座にno-opで返る。

- taskブリーフが未保存（`internal:task-brief`daemonキーが無い）状態で`task`が空文字列なら`fmt.Errorf`でエラーを返す。新規開始では`plan.go`が必ず`task`を渡すため通常は発生しないが、再開経路で万一ブリーフが失われていた場合の防御になっている
- 再開時は`TASK.md`を無条件で削除してから起動する。ループ仕様（後述）はTASK.mdが**存在しない**場合にのみオーケストレーターを再起動する規則になっており、削除しないと前回セッションが残した`DONE`のTASK.mdをそのまま読んで即終了してしまう
- `renderSystemPrompt(stateDir)`が`ensureRuntime()`から得た`(pythonPath, scriptPath)`と`stateDir`を`system_prompt.md.tmpl`に埋め込み、`<stateDir>/.masuda-plan-system-prompt.md`へ書き出す
- `customAgentsJSON()`が`--agents`用JSONを組み立てる（後述）
- `config.Load(worktreeDir)`で対象リポジトリの`.masuda/settings.json`を読み、`ClaudeSettings`が空でなければ`--settings`に付与する。ファイル・フィールドが無ければ`--settings`自体を省略する（ADR-0031、masuda側フォールバック値は持たない）
- `startMCPRelay(stateDir)`が`masuda internal mcp-relay`をデタッチ起動し、curatedソケット（`docs/design/state-daemon-mcp.md`参照）を新規発行したTCPポートへ中継する。Dockerサンドボックスと異なりホストループは同一ホスト上で複数ワークスペース分並行しうるため、ポートは固定値ではなくワークスペースごとに`freeTCPPort()`で毎回新規発行する
- 組み立てた`claude`起動コマンドは`tmux new-session -d -s <session> -c <worktreeDir> <cmd>`でworktreeDirをcwdとして起動する。`CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=0`環境変数を前置し、`--mcp-config`の直後に`--`を挟んでから初期プロンプト`'作業を開始せよ'`を渡す（`--`が無いと`--mcp-config`がプロンプト文字列自体を追加のconfigエントリとして誤飲み込みし起動に失敗する）

`AttachArgs(id)`は`["tmux", "attach", "-t", SessionName(id)]`のみ（`masuda chat`から使われる、SSH越しのVMサンドボックスと違いホスト上のtmuxへ直接attachする）。

## セッション権限（`--allowedTools`・サブエージェント定義）

`allowedTools(stateDir)`が返す文字列は次の固定形:

```
Bash,Task,Read,Grep,Glob,Edit(//<stateDir>/INVESTIGATION.md),Edit(//<stateDir>/plan/summary.md),Edit(//<stateDir>/plan/steps.json),Edit(//<stateDir>/plan_result.json)
```

メインセッション（ループ制御を担う`claude`本体）はBash・Task・Read・Grep・Globを無制限に持つが、Editは上記4成果物パスのみに絞られる。書き込み先はいずれもworktree外（`stateDir`配下）の絶対パスである点に注意。

- **既知の落とし穴（先頭スラッシュ2つ必須）**: worktree外の絶対パスへの`Edit`許可ルールは、`Edit(/abs/path)`のように先頭スラッシュ1個だと「ルール自身の置き場所からの相対アンカー」と解釈され、パスが完全一致していても確認プロンプトが出る。`Edit(//abs/path)`と2つ重ねる必要がある（[anthropics/claude-code#25137](https://github.com/anthropics/claude-code/issues/25137)、[#18200](https://github.com/anthropics/claude-code/issues/18200)）。このgotchaは`allowedTools`関数直上のコメントに詳しい経緯とともに既に記載済み
- **既知の落とし穴（サブエージェントへのツール追加はセッションレベルにも要る）**: `customAgentsJSON`側でエージェントにRead/Grep/Glob/Editを与えるだけでは足りない。セッションレベルの`--allowedTools`に同じツールを（パス制限なしで）含めておかないと、worktree外の絶対パスに対する呼び出しがClaude Codeのデフォルト確認プロンプトへ落ちる。これも`allowedTools`関数直上のコメント（Grep/Globに関する段落）に既に記載済み

`customAgentsJSON()`は`investigatorAgentName`(`"investigator"`)・`plannerAgentName`(`"planner"`)の2エージェントを定義する。両方とも`Tools: []string{"Read", "Grep", "Glob", "Edit"}`のみでBashを持たない。プロンプト文字列には固定の日本語指示（読み取り専用調査/プラン作成であること、指定ファイルをEditで書き出すこと、ADR-0029のプロンプトインジェクション自己申告義務）を埋め込んでいる。エージェント名（Task toolの`subagent_type`に渡す文字列）は`investigatorAgentName = "investigator"`・`plannerAgentName = "planner"`で、`orchestrator/investigate_plan_graph.py`が生成するTASK.md本文もこの名前を指定する。

## タスクブリーフ・指示書き込み

- `WriteTaskBrief(stateDir, task)`: `masuda plan start`に渡されたタスク文を状態デーモンへ`internal:task-brief`キーとして書く。読み手は`orchestrator/investigate_plan_graph.py`の`_read_task_brief()`
- `WriteInstructions(stateDir, content)`: `masuda plan start --file`で渡された事前指示書を`<stateDir>/INSTRUCTIONS.md`へそのままコピーする（プレーンファイル、daemonキーではない）。この時点でスナップショットするため、元ファイルの後編集・移動・削除は起動済みワークスペースに影響しない。**内容はTASK.mdへ展開されず、パスだけが調査サブエージェントへの指示文に埋め込まれる**——investigator自身がReadツールで`INSTRUCTIONS.md`を開く（ADR-0016）。サブエージェントは状態デーモンに到達する手段を持たないため、この経路はプレーンファイルのままである必要がある
- `renderSystemPrompt(stateDir)`: `ensureRuntime()`の結果と`stateDir`を`system_prompt.md.tmpl`（`text/template`）へ描画し、`<stateDir>/.masuda-plan-system-prompt.md`に書き出す。このパスが`claude --append-system-prompt-file`に渡る

## ループ仕様（`system_prompt.md.tmpl`）

テンプレートが描画する内容はループ制御の手順そのもの（4ステップ）:

1. `<stateDir>/TASK.md`が存在しなければオーケストレーター（後述の`MASUDA_STATE_DIR=<stateDir> <python> <script>`コマンド）を起動してTASK.mdを生成させ、2へ
2. TASK.mdを読み、指示に従って作業する
3. 終了条件（本文に`DONE`を含む）・ゲート条件（本文に`GATE:<name>`を含む、`plan`または`triage`）を確認する。終了条件ならtmuxセッションをkillして終了、ゲート条件なら4へ、どちらでもなければオーケストレーターを再起動して2へ戻る
4. `mcp__masuda-gate__wait_for_gate_resolution`でブロッキング待機する

ゲート待機・解決（`resolve_gate_from_chat`、triageゲートの特別扱い等）の詳細は`docs/design/gates.md`を参照。このテンプレートは「オーケストレーター起動コマンドの組み立て」と「終了/ゲート条件の文字列規約」だけを担い、ゲートの承認/却下ロジック自体は持たない。

## 調査/プランサブエージェント委譲プロンプト生成（`orchestrator/investigate_plan_graph.py`）

`write_task_md`がフェーズ名（後述の`detect_phase`が導出）ごとにTASK.md本文を組み立てる。フェーズと生成関数の対応は次の通り。

| フェーズ | 生成関数 |
|---|---|
| `investigate` / `investigate_redo` | `_investigate_task(task, questions)` |
| `plan` / `plan_redo` | `_plan_task(feedback)` |
| `await_triage` / `triage_halted` | `_triage_task` / `_triage_halted_task` |
| 上記以外の終端フェーズ | `_TERMINAL`辞書の固定文字列 |

**`_investigate_task`**: Task toolで`subagent_type: investigator`を指定して委譲する指示文を組み立てる。`questions`が非空（investigate_redoの場合）なら「追加調査事項」節を追加し、完了条件に`INVESTIGATE_REDO_PENDING_JSON`（`.masuda-investigate-redo-pending.json`）の削除を明示的な最終ステップとして含める。`INSTRUCTIONS_MD`（`masuda plan start --file`）が存在すれば「事前に用意された指示書の検証」節を追加し、内容を鵜呑みにせず矛盾・実現困難な点をINVESTIGATION.mdに書かせる。`INVESTIGATION.md`に要求する構成（タスク要約・関連ファイル一覧・既存パターン・制約・未解決の疑問点、指示書検証時はその結果も）と、ADR-0029のプロンプトインジェクション自己申告節（`_TRIAGE_SELF_REPORT_SECTION`、investigate/plan両方の生成関数で共通）を含む。

**`_plan_task`**: `PLAN_DIR`（`<stateDir>/plan`）をこの関数自身が`mkdir(parents=True, exist_ok=True)`で先に作る（オーケストレーターはホスト上で無サンドボックス実行のため、planner subagentのEditツールの親ディレクトリ自動作成に賭ける理由がない）。`feedback`が非Noneなら「差し戻し理由（G1で却下）」節を追加。

生成する指示は`plan/summary.md`（自由記述prose: アプローチ要約・テスト方針・不採用の代替案・リスク）と`plan/steps.json`（機械的パース対象のJSON1個）を分けて要求する。`plan/steps.json`のトップレベルスキーマ:

```json
{
  "steps": [
    {
      "description": "...",
      "files": [{"path": "...", "description": "..."}]
    }
  ],
  "expected_byproducts": ["**/__pycache__/**", "**/*.pyc"]
}
```

各ステップの`files`はそのステップで実際に変更するファイルに限定し他ステップの分を含めないよう指示する（Build段階の機械的バックストップがステップ単位で突き合わせるため）。`expected_byproducts`はビルド/テストツールチェーンが副作用生成しうるファイルパターンで、`*`は`/`をまたがず1階層のみ、`**`は0階層以上をまたぐという一般的なglob規約に従うよう明記している。

追加調査で解決できない大きなギャップがある場合は、`plan/summary.md`・`plan/steps.json`の代わりに`{"status": "needs_more_investigation", "questions": [...]}`を`plan_result.json`へ書かせる。完了条件は「（`plan/summary.md`と`plan/steps.json`の両方）または`plan_result.json`」。

## フェーズ判定ロジック（`detect_phase`）

LLM呼び出しを一切行わない純粋な状態機械。ファイルシステム（worktree外、`STATE_DIR`配下）と状態デーモンのキーだけを読み、遷移時に一部のマーカーを消費（削除）する副作用を持つ。優先順位は次の通り（上から順に判定、`TRIAGE_CONCERN_JSON`が最優先）。

1. `TRIAGE_CONCERN_JSON`（`triage_concern.json`）が存在する: `_resolve_triage`へ委譲し、`gate:triage`マーカーの`status`（`masuda triage dismiss`→`approved`、`redo`→`rejected`、`halt`→`halted`、未解決なら`pending`）に応じて`await_triage`（pending時）・`triage_halted`（halted時）・もとのフェーズへの復帰（approved/rejected時、`triage_concern.json`削除＋`gate:triage`削除。rejected時のみ`internal:triage-redo-feedback`にfeedbackを積んでから復帰）のいずれかを返す（ADR-0029）
2. `internal:plan-redo-pending`キーが存在する: `plan/summary.md`・`plan/steps.json`が両方揃っていればキーを消費（削除）して次の判定へ進む。揃っていなければ`plan_redo`のまま留まる（ADR-0039: G1却下時点で`gate:plan`キーは既に消費・削除済みのため、redo未完了かどうかはこのマーカーの存在でしか判定できない）
3. `.masuda-investigate-redo-pending.json`が存在する: `investigate_redo`を返す（このマーカー自体はinvestigatorサブエージェントが自分でファイルを削除するまで消費されない——`_investigate_task`の完了条件に明記）
4. `plan/summary.md`・`plan/steps.json`が両方存在する: `gate:plan`マーカーを読み、`approved`なら`g1_approved`、`rejected`ならマーカー削除＋`internal:plan-redo-pending`書き込み＋両ファイル削除の上で`plan_redo`、`pending`（未設定含む）なら`await_g1`
5. `plan_result.json`が`needs_more_investigation`: リトライ回数（`internal:plan-retries`）が`MAX_RETRIES`(3)以上なら`retries_exhausted`。そうでなければリトライ加算・`plan_result.json`削除・`.masuda-investigate-redo-pending.json`書き込みの上で`investigate_redo`
6. `INVESTIGATION.md`が存在しない: `investigate`
7. それ以外: `plan`

`_SUBAGENT_PHASES = {investigate, investigate_redo, plan, plan_redo}`のいずれかを`write_task_md`が実際に描画するたび、`internal:iteration-count`キーを1加算する（`_record_iteration`）。加算後の値が`ITERATION_BUDGET`(20)を超えていればフェーズを`iteration_budget_exceeded`へ強制上書きし、`DONE (blocked)`として停止する。この上限は`MAX_RETRIES`(3、investigate↔plan redoの往復回数のみを制限、ADR-0008)とは独立した、サブエージェント起動回数全体に対する最終防衛ライン（ADR-0011）。

## 関連ADR

- [ADR-0002](../adr/0002-workflow-orchestrator-with-subagent-delegation.md): オーケストレーター＋サブエージェント委譲の基本形
- [ADR-0008](../adr/0008-investigate-plan-agent-separation-with-redo.md): investigate/plan分離とredoプロトコル
- [ADR-0012](../adr/0012-worktree-created-before-investigation.md): worktree作成の前倒し、Discovery/Blueprintがサンドボックス不要である理由
- [ADR-0016](../adr/0016-pre-written-instructions-file-for-investigate.md): `--file`指示書とINSTRUCTIONS.mdの検証フロー
- [ADR-0026](../adr/0026-plan-md-as-prose-plus-per-step-json.md): `plan/summary.md`＋`plan/steps.json`への分割
- [ADR-0031](../adr/0031-claude-settings-field-init-materialized-no-implicit-default.md): `claudeSettings`フィールドと`--settings`
- [ADR-0034](../adr/0034-skip-dangerous-mode-permission-prompt-over-tmux-polling.md): 権限プロンプト回避の設計
- [ADR-0039](../adr/0039-redo-pending-marker-bridges-gate-consume-and-subagent-rewrite.md): redo pendingマーカーの役割

## 他ファイルとの境界

- ゲートの承認/却下・待機の仕組み: `docs/design/gates.md`
- 状態デーモンのキー・tool一覧: `docs/design/state-daemon-mcp.md`
- ワークスペース作成・状態ディレクトリの構造: `docs/design/workspace.md`
- 予算計算式（Build/Review段階側の動的計算含む）の全体像: `docs/design/pipeline.md`
