# masuda 用語集

masudaのパイプライン・ゲート・エスカレーションは、開発初期から「フェーズ0」「G1」「層3」のような連番ベースの暫定名で運用されてきた。連番を識別子そのものに使うと、間に新しい段階を挟みたくなった時に「フェーズ-1」のような名前しか作れなくなる。本ドキュメントは、意味ベースの正式名称と、ADR等の過去文書に出てくる旧称との対応表をまとめたリファレンス。

## なぜ連番をやめたか

[[0012-worktree-created-before-investigation]]は、design docのフェーズ表を「0始まりに振り直した（旧フェーズ3が0、旧4が3、旧5が4、旧6が5）」と記録している。この結果、[[0012-worktree-created-before-investigation]]より前に書かれた[[0009-implementation-self-verification-loop]]は今も「フェーズ5（実装）」「フェーズ6（レビュー）」という、現行の番号とは一致しない表記のまま残っている。連番を識別子として使う設計は、masuda自身の歴史の中で既に一度破綻していた。

以後、パイプラインの各段階・ゲート・エスカレーション種別は、順序に依存しない意味ベースの名称で呼ぶ。表示上の順序が必要な場面（`masuda workspace list`のSTATUS表示等）は、名称とは独立に、実装側のリスト・配列の並び順から機械的に導出する。

## パイプライン段階

| 正式名 | 内容 | 実行者 | サンドボックス |
|---|---|---|---|
| **Provision** | git worktree（ワークスペースのgitチェックアウト）作成 | Go CLI（ホスト側） | 不要 |
| **Discovery** | 調査。タスク内容から`INVESTIGATION.md`を作る | サブエージェント（read-only） | 不要 |
| **Blueprint** | プラン作成。`INVESTIGATION.md`から`plan/summary.md`・`plan/steps.json`を作る | サブエージェント | 不要 |
| *(plan gateで人間承認)* | | | |
| **Scaffold** | プロジェクト初期化（依存解決・LSP起動）。**未実装・予約名**。現状はBuild/Reviewの各サブエージェントが自タスク内で必要に応じて行う | Go CLI / entrypoint.sh | microVM起動 |
| **Build** | 実装。`plan/steps.json`のステップを1つずつ「実装→バックストップ→トリガー式軽量レビュー→commit」で処理する | サブエージェント（write/Edit/Bash可） | microVM |
| **Review** | レビュー。14観点のreview/check往復＋横断的チェックで`final_report.md`を作る | サブエージェント群 | microVM |
| *(review gateで人間承認)* | | | |

Discovery↔Blueprintの往復（調査不足時のredo）、Build内でのステップループ、Review内でのcheck/fix/recheckループは、それぞれの段階の内部設計であり別段階ではない。

## ゲート

| 正式名 | 内容 | 対応する旧称 |
|---|---|---|
| **plan gate** | Blueprintの成果物（`plan/summary.md`・`plan/steps.json`）を人間が承認するまでBuildへ進めない関所 | G1 |
| **review gate** | Reviewの成果物（`final_report.md`）を人間が承認するまでマージしない関所 | G2 |
| **triage gate** | Panic escalation専用の関所。疑わしいエージェント自身に解決を委ねない設計（[[0029-immediate-stop-escalation-dedicated-gate]]） | （同ADRで新設、旧称なし） |

技術的な実体（`GATE:<name>`終了条件、[[0006-interactive-chat-plus-fast-path-gates]]・[[0042-mcp-tool-call-replaces-inotifywait-gate-wait]]）は`GATE:plan`・`GATE:review`という文字列で、この正式名と既に一致している。

## エスカレーション

エージェントの判断だけに任せず人間の介入を挟む3種類の仕組み。GitHub Issue #4で「ブロッキング」「非ブロッキング・記録型」の2種に加え、セキュリティ上の懸念に対する第3の型として整理された。

| 正式名 | 内容 | 解決経路 | 対応する旧称 |
|---|---|---|---|
| **blocking escalation** | ビルド/テスト失敗の上限到達、または承認済みプランからの逸脱。plan gateを再オープンして人間が判断する | plan gate再オープン | 層1（暗黙・未命名） |
| **recorded escalation** | 横断的チェック（explorer→verifier）が見つけた、diffだけでは検知できない指摘。自動修正せず常にreview gateで人間に提示する | review gateでの提示のみ（専用の停止はしない） | 層2 |
| **Panic escalation** | エージェント自身の判断が汚染されている（プロンプトインジェクション等）疑いを自己申告した場合の最優先即時停止。`halt`選択時は自動再開経路なし | triage gate（[[0029-immediate-stop-escalation-dedicated-gate]]） | 層3 |

blocking escalationとrecorded escalationはどちらも既存のゲート（plan gate/review gate）を再利用する。Panic escalationだけが専用ゲート（triage gate）を持つ。理由は、blocking/recorded escalationが問うのが「AIの設計判断は妥当か」（＝plan gate/review gateと同じ論点）であるのに対し、Panic escalationが問うのは「このエージェントの自己申告・自己判断がそもそも信頼できるか」という別種の論点であるため（[[0029-immediate-stop-escalation-dedicated-gate]]）。

## 旧称対応表（ADRを読む際の変換）

過去のADR本文は原則として当時の表記のまま残す（歴史的記録のため書き換えない）。以下はADRを読む際の早見表。

| ADR等での表記 | 正式名 |
|---|---|
| フェーズ0 | Provision |
| フェーズ1 | Discovery |
| フェーズ2 | Blueprint |
| フェーズ3 | Scaffold |
| フェーズ4 | Build |
| フェーズ5 | Review |
| G1 | plan gate |
| G2 | review gate |
| 層1 | blocking escalation |
| 層2 | recorded escalation |
| 層3 | Panic escalation |

※ [[0012-worktree-created-before-investigation]]のように、番号の振り直しそのものを記述している箇所は例外的に番号表記のまま残る。フェーズ番号は[[0012-worktree-created-before-investigation]]の前後で意味が変わっている（振り直し前後で同じ番号が指す段階が異なる）ため、古いADRの番号を鵜呑みにせず、内容（何をする段階か）で正式名に読み替えること。

## その他の確立済み用語

連番の問題を持たないため今回リネームしていない用語。詳細な定義は`docs/backlog-agent.md`「用語集」節を参照。

- **ワークスペース (workspace)**: ワークスペースID・gitチェックアウト・状態ディレクトリ（Scaffold以降はサンドボックスVMも含む）をまとめた複合的な単位
- **サンドボックス (sandbox)**: Scaffold以降で使うCloud Hypervisor microVM
- **観点 (perspective)**: Reviewが読み込むレビュー観点（`.masuda/reviews/*.md`）
- **バックストップ (backstop)**: 自己申告に頼らない機械的な逸脱検知（Build段階、[[0010-plan-deviation-reopens-plan-gate]]）
- **redo**: 前段への差し戻し（Discovery↔Blueprint、check_nodeのredo等）
- **DEVIATION.md**: Build段階で計画外の変更が検知された時に現れる、plan gate再オープンのトリガーファイル

## 適用範囲についてのメモ

この用語集の正式名称は`CLAUDE.md`・`docs/design/`配下には反映済み。ADR本文は凍結対象（`docs/adr/README.md`の運用ルール）のため、旧称のまま残る。ADRを読む際は上記の「旧称対応表」を翻訳表として使うこと。

`docs/backlog-agent.md`は未反映。
