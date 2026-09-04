# パイプライン全体像

masudaのAI協同開発パイプラインは6段階+2ゲートで構成される。実行はAnthropic APIの直接呼び出しではなく、Claude Code CLIをtmux上で自己ループさせる方式で行う（ADR-0001）。本ドキュメントは`docs/design/`配下で最初に読む全体像ドキュメントで、段階間のデータフロー・実行主体の切り替え・予算管理という横断的な仕組みをまとめる。各段階の正式名称の定義・旧称対応は`docs/glossary.md`を参照。各段階の内部設計は後述の各ドキュメントへの導線を参照。

## 6段階+2ゲート

| 段階/ゲート | 何をするか | 実行者 | サンドボックス | 入力 | 出力 |
|---|---|---|---|---|---|
| **Provision** | worktree作成 | Go CLI（ホスト側） | 不要 | タスク内容 | worktree・ワークスペースID |
| **Discovery** | 関連ファイル・制約の洗い出し | サブエージェント（investigator、read-only） | 不要 | タスク内容 | `INVESTIGATION.md` |
| **Blueprint** | 変更方針・ステップ分解の作成 | サブエージェント（planner） | 不要 | `INVESTIGATION.md` | `plan/summary.md`・`plan/steps.json` |
| *plan gate* | プランの承認判断 | 人間 | — | `plan/summary.md`・`plan/steps.json` | 承認 or 差し戻し |
| **Scaffold**（未実装・予約名） | — | — | — | — | — |
| **Build** | ステップ単位の実装・commit | サブエージェント（implementer、write/Edit/Bash可） | Cloud Hypervisor microVM | `plan/steps.json` | worktree上のコード変更（ステップごとにcommit） |
| **Review** | 観点review/check・横断的チェック | サブエージェント群 | Cloud Hypervisor microVM | worktreeのdiff | `review_results/final_report.md` |
| *review gate* | レビュー結果の承認判断 | 人間 | — | `final_report.md`・diff | 承認（マージ・後片付け） or 差し戻し |

worktree（Provisionの出力）はDiscoveryより前に作られる（ADR-0012）。サンドボックス（microVM）の起動はScaffold相当のタイミングまで不要で、Discovery/Blueprintはホスト側のメインエージェントが直接worktreeを読み書きする。Scaffoldは未実装の予約名で、現状はBuild/Reviewの各サブエージェントが自タスク内で必要な依存解決を行う。サンドボックスの起動・停止・ネットワークの詳細は`docs/design/sandbox-vm.md`・`networking.md`を参照。

Discovery↔Blueprintの往復、Build内のステップループ、Reviewのcheck/fix/recheckループはいずれも各段階の内部設計であり、この6段階の分解には現れない。

## 役割分担: メインエージェント / サブエージェント

各段階でホスト側またはサンドボックス内で自己ループするメインのClaude Codeセッションは、ワークフロー管理に徹する。次のタスクの取得、サブエージェントへの作業委譲と結果の検証のみを行い、実際の調査・プラン作成・実装・レビューはすべて都度起動するサブエージェントに委譲する（ADR-0002）。

**オーケストレーター自体は2段階対ともホスト側で動く。** Discovery/Blueprintはメインセッションもホストなので同一マシン内だが、Build/Reviewのメインセッションはサンドボックス内にいるため、curated MCPの`next_task`ツールでホストのオーケストレーターを1回進めてタスク本文を受け取る形になる（ADR-0057）。ループの状態機械・予算・ステップcommitはサンドボックスの外にあり、中のセッションから書き換えられない。

サブエージェントは成果物をJSONファイル（`plan_result.json`・`implementation_result.json`等）として書き出す。メインエージェント・オーケストレーターはファイルの存在とスキーマを機械的に確認し、不正または未達なら再試行させてから次の`TASK.md`を生成する。サブエージェントに渡すツール権限は用途ごとに絞る（例: Discovery/Blueprintはread-only、Buildはwrite/Edit/Bash可）。

## TASK.md書き出しとLangGraph配線

Discovery/Blueprint段階は`orchestrator/investigate_plan_graph.py`、Build/Review段階は`orchestrator/implement_review_graph.py`が担当する。どちらもホスト上のPython venv（`internal/hostloop`の`ensureRuntime`が埋め込みから展開して用意する）で実行される。両ファイルは互いにimportし合わない独立したモジュールで、それぞれが同じ配線パターンを別々に持つ。

- `detect_phase`ノード: 状態ディレクトリ・状態デーモンの中身から現在の`phase`（文字列）を判定する
- `write_task_md`ノード: `phase`を見て対応するレンダラー関数（`_investigate_task`・`_plan_task`・`_implement_step_task`等）を呼び分ける単純なif/elif dispatchで`TASK.md`の内容を組み立て、書き出す（`investigate_plan_graph.py:516`、`implement_review_graph.py:2352`）。ゲート待機・`DONE`系の終端状態は`_TERMINAL`という`phase → 固定文面`の辞書で表現し、同じdispatchの中で分岐する
- グラフ自体は`detect_phase → write_task_md → END`の2ノードのみ（`investigate_plan_graph.py:553`の`build_graph`、`implement_review_graph.py:2418`の`build_graph`）

メインエージェントは受け取ったタスクに従うだけで、どちらの段階でもこの1往復（detect→render）がオーケストレーターの実行単位になる。Discovery/Blueprintのメインセッションは`TASK.md`をファイルとして読む（同一マシン）。Build/Reviewのメインセッションは`next_task`の戻り値を読む——`TASK.md`も従来どおり書かれるが、ホストの書き込みがゲストから見えるまでvirtiofsの属性キャッシュ分（実測0.5〜0.6秒）遅れるため、ファイルではなく戻り値が正となる。

## 予算管理

予算は「サブエージェント起動1回」を1単位として数える（ADR-0011）。Discovery/Blueprint（`investigate_plan_graph.py`）とBuild/Review（`implement_review_graph.py`）は同じホスト上とはいえ別プロセス・別の状態キーで動くため、予算を共有しない。

- Discovery/Blueprint: 固定値`ITERATION_BUDGET = 20`（調査/プランの往復`MAX_RETRIES = 3`に加え、plan gate再オープンの余裕を見込んだ値）
- Build/Review: `_iteration_budget() = BASE_BUDGET(200) + PER_STEP_BUDGET(5 + 14観点 × 12) × plan/steps.jsonのステップ数`（ADR-0027）

加算は実際にサブエージェントへ委譲する段階（`_SUBAGENT_PHASES`）でのみ行われ、ゲート待機や`DONE`系の終端状態は加算しない。`write_task_md`が`_record_iteration()`を呼ぶタイミングでカウントする（`investigate_plan_graph.py:161`が加算処理本体、呼び出しは`write_task_md`内。`implement_review_graph.py:624`）。Build/Review側の`_record_iteration(n)`は1ラウンドでn件のサブエージェントを並列委譲する場合（`review_batch`等、ADR-0021）にnをまとめて加算できるよう引数を取る。

超過時は、各段階が持つredoループ（`MAX_RETRIES`・`MAX_REVIEW_RETRIES`）とは独立した最終防衛ラインとして、`write_task_md`冒頭で`phase`を`iteration_budget_exceeded`に差し替えて`DONE (blocked)`で停止する（`investigate_plan_graph.py:518`・`implement_review_graph.py:2358`）。redoループが無限ループを起こしていても、この上限だけは必ず効く。

この予算が数えるのは**起動回数**だけで、1回の起動の内側は数えない。サブエージェントが1起動の中で何ターン探索しようと1単位として計上される。1起動内の暴走を止める機構はオーケストレーター側に存在せず、探索範囲を絞るようプロンプトで指示する自主規制のみで対応している（ADR-0011が想定する2層構造のうち、外側＝起動回数の上限だけが機構として実装された状態。内側の上限が必要になるのは主に横断的チェックのexplorerで、`docs/design/review.md`を参照）。

## 各段階の詳細設計への導線

- Discovery/Blueprintの内部設計（調査/プランの往復、成果物の構成）: `docs/design/discovery-blueprint.md`
- Buildの内部設計（ステップループ、バックストップ、途中レビュー）: `docs/design/build.md`
- Reviewの内部設計（観点review/checkループ、横断的チェック）: `docs/design/review.md`
- plan gate/review gate/triage gateの操作方法・却下時の差し戻し: `docs/design/gates.md`
- サンドボックス（microVM）の起動・停止: `docs/design/sandbox-vm.md`
- ネットワーク（TAP・SSH・mcp-relay）: `docs/design/networking.md`
- Dockerイメージ・rootfs変換: `docs/design/images-and-rootfs.md`
- 状態デーモン・MCP: `docs/design/state-daemon-mcp.md`
- 子MCPサーバー: `docs/design/mcp-child-servers.md`
- 配布・アップデート: `docs/design/distribution-and-update.md`
