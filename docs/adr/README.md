# ADR インデックス（decision log）

## 運用ルール

- **ADRの本文（Context / Decision / Alternatives Considered）は、一度Acceptedになったら書き換えない。** 判断が変わった場合は新しいADRを起票し、古い方は Status 欄だけを更新する
- **ADRは削除しない。** 却下した代替案の記録も、同じ検討を将来やり直さないための資産である
- Status の語彙は以下の4つ
  - `Accepted (日付)` — 決定がそのまま有効
  - `Accepted (日付)` ＋ `一部改訂:` — 決定の骨子は有効だが、具体的な一部が後続ADRで置き換わっている。**どこが変わったかは各ADRのStatus欄に書いてある**
  - `Accepted (日付)` ＋ `訂正:` — 決定は有効だが、本文の記述が**書かれた時点から事実として誤っている**。後続の決定で変わった`一部改訂:`とは別物。本文は凍結したままStatus欄で訂正する
  - `Superseded by [[...]]` — 決定全体が無効。歴史的経緯としてのみ読む
- **`Superseded by` になったADRは、下のテーマ別インデックスから行ごと削除する。** ファイル自体は残す。二度と開く必要のないものを索引に載せておくと、開くべきかの判断コストを毎回払うことになるため。到達経路は覆した側のADRのContextからの `[[...]]` リンクで足りる

## 用語の注意

段階名・ゲート名・エスカレーション区分（フェーズ0〜5 / G1・G2 / 層1〜3）の旧称は [`../glossary.md`](../glossary.md) の「旧称対応表」を引く。そこに載っていない、ADR本文にだけ出てくる旧称は以下。

- **PLAN.md** → `plan/summary.md` ＋ `plan/steps.json`（ADR-0026以降）
- **`.masuda.json`** → `.masuda/settings.json`（ADR-0024以降）
- **Dockerサンドボックス / コンテナ** → Cloud Hypervisor microVM（ADR-0044以降）

## テーマ別インデックス

`— 一部改訂: NNNN` が付いているADRは、決定の一部が後続ADRで置き換わっている。何がどう変わったかは各ADRのStatus欄を見る。

### 全体アーキテクチャ・実行方式

- **[0001](0001-self-loop-over-direct-api.md)** レビュー実行方式を直接API呼び出しから自己ループ方式に変更する
- **[0002](0002-workflow-orchestrator-with-subagent-delegation.md)** メインエージェントはワークフロー管理に専念し、実作業はサブエージェントに委譲する — *一部改訂: 0057*
- **[0007](0007-loop-protocol-claude-md-in-user-scope.md)** ループ仕様CLAUDE.mdは対象リポジトリではなく`~/.claude/CLAUDE.md`に置く — *一部改訂: 0044*
- **[0012](0012-worktree-created-before-investigation.md)** worktree作成は調査より前に行う — *一部改訂: 0044*
- **[0047](0047-file-placement-decided-by-consumer.md)** ファイルの配置は「誰が消費するか」で決め、「masudaの動作に必要かどうか」では決めない
- **[0050](0050-design-docs-hold-current-state-adrs-hold-rationale.md)** 現在の設計は`docs/design/`が、判断の経緯は`docs/adr/`が持ち、CLAUDE.mdはどちらも持たない
- **[0057](0057-build-review-orchestrator-runs-on-the-host-guest-pulls-next-task.md)** Build/Reviewのオーケストレーターをホストで動かし、ゲストはcurated MCPの`next_task`で次のタスクを受け取る

### ゲート・エスカレーション

- **[0006](0006-interactive-chat-plus-fast-path-gates.md)** ゲート承認は対話（chat）と即決（approve/reject）の2パスを用意する — *一部改訂: 0042, 0044*
- **[0010](0010-plan-deviation-reopens-plan-gate.md)** プラン逸脱は自己申告+機械的バックストップの二段構えで検知しplan gateを再オープンする — *一部改訂: 0027, 0028, 0037*
- **[0013](0013-review-rejection-reopens-implementation.md)** review gate却下はBuild段階を再オープンする — *一部改訂: 0027*
- **[0029](0029-immediate-stop-escalation-dedicated-gate.md)** 最優先即時停止エスカレーションは専用ゲート（`masuda triage`）で解決する
- **[0039](0039-redo-pending-marker-bridges-gate-consume-and-subagent-rewrite.md)** redoの遷移は専用pendingマーカーでゲートマーカー消費と成果物書き換えを橋渡しする — *一部改訂: 0055*
- **[0042](0042-mcp-tool-call-replaces-inotifywait-gate-wait.md)** GATE待機はinotifywaitではなくMCP tool呼び出しで行う — *一部改訂: 0050, 0055*

### Discovery / Blueprint（調査・プラン）

- **[0008](0008-investigate-plan-agent-separation-with-redo.md)** 調査・プラン作成は別エージェントとし、大きな調査不足はredoで差し戻す — *一部改訂: 0026, 0039*
- **[0016](0016-pre-written-instructions-file-for-investigate.md)** `plan start --file`で事前準備済みの指示書を渡しファクトチェックさせる
- **[0026](0026-plan-md-as-prose-plus-per-step-json.md)** PLAN.mdを`plan/summary.md`と`plan/steps.json`に分離する — *一部改訂: 0028, 0037*
- **[0028](0028-plan-predicted-byproducts-exempt-from-backstop.md)** プラン段階で予想する副産物パターンを機械的バックストップの除外対象にする

### Build（実装）

- **[0009](0009-implementation-self-verification-loop.md)** 実装はビルド/テストを自己修正ループで検証し、上限到達で停止する — *一部改訂: 0027, 0037*
- **[0027](0027-phase4-step-based-implement-review-commit-loop.md)** Build段階をステップ単位のimplement→バックストップ→トリガー式軽量レビュー→commitのループにする — *一部改訂: 0029, 0037, 0058*
- **[0035](0035-four-task-categories-scope-tdd-to-new-features.md)** タスクを3軸で4カテゴリに分類し、TDDモードの対象を「新機能追加」に限定する
- **[0037](0037-tdd-mode-red-green-refactor-subloop-in-phase4.md)** TDDモード（Red/Green/Refactorサブループ）をBuild段階に追加する
- **[0058](0058-commit-scope-measured-from-git-not-self-reported.md)** commitに含めるファイルは実装エージェントの自己申告ではなくgitの実測から決める

### Review（レビュー）

- **[0003](0003-mechanical-vs-complex-review-nodes.md)** レビューノードを「機械的チェック」と「横断的・複雑なチェック」の2区分にする
- **[0004](0004-checker-fixer-role-separation.md)** 機械的な指摘の自動修正でcheckerとfixerの役割を分離する
- **[0011](0011-iteration-budget-per-subagent-invocation.md)** 予算管理の単位を「サブエージェント起動1回」に統一し、横断的チェックにredoを持たせない — *一部改訂: 0021, 0027*
- **[0020](0020-structured-file-line-schema-for-findings.md)** 指摘のlocationをfile+startLine+endLineの構造化スキーマに変える
- **[0021](0021-parallel-perspective-review-batching.md)** 観点のreview/check/fix/recheckを1ラウンドごとにバッチ並列委譲する
- **[0024](0024-file-based-perspectives-mechanical-checker-prompt.md)** レビュー観点を`.masuda/reviews/`のファイル群として展開し、checker_promptは機械的テンプレートで生成する — *一部改訂: 0025, 0027, 0033*
- **[0025](0025-drop-unused-category-severity-from-perspective-frontmatter.md)** 観点frontmatterから未使用のcategory・severityを削除する
- **[0033](0033-perspective-enable-flag-and-release-asset-sync.md)** 観点の無効化は`enable`フィールドで表現し、`masuda update`はReleaseアセットから追加のみ同期する — *一部改訂: 0038*
- **[0046](0046-review-only-entrypoint-reuses-provision-to-review.md)** `masuda review start`はProvision〜Reviewの既存機構をそのまま再利用し、専用のレビュー実行パスは作らない
- **[0052](0052-remove-hunk-integration.md)** `masuda review hunk`（Hunk統合）を削除する

### ワークスペース・git操作

- **[0005](0005-manual-push-automatic-worktree-ops.md)** git pushは常に手動、worktree操作は自動化してよいという権限境界 — *一部改訂: 0023*
- **[0014](0014-workspace-id-and-external-state-directory.md)** ワークスペースIDと外部状態ディレクトリの導入 — *一部改訂: 0030, 0044*
- **[0018](0018-git-clone-local-over-linked-worktree.md)** worktreeは`git worktree add`ではなく`git clone --local`で作る — *一部改訂: 0036*
- **[0023](0023-review-approve-fast-forward-not-local-merge.md)** review承認時のブランチ反映をローカルmergeからfast-forward限定に変える
- **[0030](0030-workspace-id-drops-branch-name-prefix.md)** ワークスペースIDからbranch名プレフィックスを外し乱数のみにする
- **[0036](0036-sync-uncommitted-masuda-config-into-clone.md)** clone作成時にrepoRootの`.masuda/`設定を常に上書きコピーする — *一部改訂: 0043, 0054*

### サンドボックス実行基盤

- **[0015](0015-native-lsp-plugins-and-repo-declared-image.md)** 横断的チェックはネイティブLSPプラグイン方式を採用し、イメージはrepo側の宣言に委ねる — *一部改訂: 0024, 0044, 0054*
- **[0022](0022-build-essential-in-base-image.md)** build-essentialはバリアントごとではなく共通baseイメージに1回だけ入れる
- **[0044](0044-remove-docker-execution-runtime-vmbackend-only.md)** Dockerの実行基盤を完全に削除しサンドボックスはVMBackendのみにする
- **[0045](0045-redirect-over-tproxy-for-egress-interception.md)** egressプロキシへのパケット転送はTPROXYではなくiptables REDIRECTを使う
- **[0048](0048-vm-network-shared-bridge-dynamic-tap-privileged-helper.md)** VMネットワークはホスト共有のbridge+NATとワークスペースごとの動的TAPに分離し、CAP_NET_ADMINは専用ヘルパーバイナリに隔離する
- **[0049](0049-virtiofsd-sandbox-none.md)** virtiofsdを`--sandbox=none`で起動し、`newuidmap`/`newgidmap`をホスト前提条件に加えない
- **[0051](0051-resolv-conf-fixed-at-vm-boot-in-sysinit-layer.md)** `/etc/resolv.conf`の差し替えはVM起動時のoneshot unitで行い、systemd-resolvedと同じsysinit層に置く
- **[0053](0053-privileged-commands-declared-and-run-in-disposable-vm.md)** target repoが要求する特権操作は、宣言＋承認された使い捨てVMで実行し、メインサンドボックスVMには一切root/Docker権限を与えない
- **[0054](0054-vm-images-declared-as-directories-under-masuda-images.md)** VMイメージは`.masuda/images/<name>/`のディレクトリ単位で宣言し、`settings.json`の`image`はそのエントリ名を指す
- **[0056](0056-vm-host-network-persisted-by-oneshot-unit-rerunning-the-setup-script.md)** VMホストのネットワーク設定は、セットアップスクリプト自身を`--runtime-only`で再実行するsystemd oneshot unitで永続化する

### 設定・配布・自己更新

- **[0031](0031-claude-settings-field-init-materialized-no-implicit-default.md)** Claude Code設定は`claudeSettings`フィールドとして`init`が実体化し、暗黙のデフォルトを持たない — *一部改訂: 0034, 0043, 0044*
- **[0032](0032-masuda-self-update-via-github-release-and-dockerhub-pull.md)** `masuda update`はGitHub Releaseのバイナリと公開Docker Hub baseイメージで自己更新する — *一部改訂: 0033, 0038, 0044, 0054*
- **[0034](0034-skip-dangerous-mode-permission-prompt-over-tmux-polling.md)** bypass permissionsダイアログは`skipDangerousModePermissionPrompt`設定キーで回避する
- **[0038](0038-cosign-keyless-blob-verification-for-self-update.md)** 自己更新が取得するアセットをcosign keyless署名で検証する

### 状態デーモン・MCP

- **[0040](0040-per-workspace-state-daemon-for-masuda-owned-state.md)** masuda自身が読み書きする状態はワークスペース単位の常駐デーモンで管理する — *一部改訂: 0041, 0042, 0055*
- **[0041](0041-mcp-protocol-with-trusted-and-curated-surfaces.md)** デーモンの通信はMCPに統一し、trusted用フルセットとcurated setの2面をUDSで公開する — *一部改訂: 0043*
- **[0043](0043-child-mcp-server-aggregator-with-project-user-config-split.md)** 子MCPサーバーのアグリゲータ化は宣言と承認を分離し、宣言のハッシュ値で承認を紐付ける
- **[0055](0055-gate-marker-is-an-unconsumed-decision-waited-on-by-presence.md)** ゲートマーカーは「未消費の決定」だけを意味し、待機はその存在を条件とし、消費は原子的に行う

## 新しいADRを書くとき

`adr-author` skill を使う。索引の保守手順もそちらにある。ADRにすべき内容かどうかがまだ決まっていない場合は `doc-placement` skill が先。
