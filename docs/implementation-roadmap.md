# 実装ロードマップ

`docs/design/sandbox-workflow.md`と`docs/adr/`で決めた設計を実装するための大まかな順序。詳細な実装方法（言語内の具体的な構成、エラーハンドリングの粒度等）はコーディングエージェントの判断に委ねる。ここでは「何を」「どの順番で」作るかのみを示す。

## 1. リポジトリ構造の移行

design doc「リポジトリ構造」・ADR-0007参照。

- `runtime/CLAUDE.md`: 旧ルートCLAUDE.md（作業ループ仕様）の原文を複製済み。ADR-0006の`GATE:<name>`終了条件の追加など、内容自体の実装上の変更はまだ未着手
- `runtime/`配下に`entrypoint.sh`、`start_claude.sh`、MCP経由のLSP設定
- `orchestrator/`配下に`langgraph_orchestrator.py`、`perspectives/config.py`
- `cmd/masuda/`: Go製CLIの雛形

## 2. Go CLI（`masuda`コマンド）の骨格

design doc「リポジトリ構造」「ゲート（G1/G2）のUX」、ADR-0005・0006参照。

- worktree管理（作成・ローカルマージ・削除。pushは含めない）
- サンドボックス起動（`docker run`、worktreeのbind mount、`~/.claude/CLAUDE.md`へのruntime/CLAUDE.md配置）
- ゲート操作: `masuda plan/review show|chat|approve|reject`

## 3. LangGraphオーケストレーターを6フェーズの親グラフに再設計

design doc「全体構成」参照。

- フェーズ0（worktree作成）は2番で作ったGo CLI（`masuda worktree create`）を呼び出す
- フェーズ1-2（調査・プラン、ADR-0008: エージェント分離+調査不足時のredo）
- フェーズ4（実装、ADR-0009: ビルド/テスト自己修正ループ、ADR-0010: プラン逸脱検知とG1再オープン）
- フェーズ5は既存の`feat/github-actions-langgraph-nodes`のレビューグラフ（review/checkの往復、13観点）を子グラフとして組み込む。フェーズ4と同一オーケストレーターにまとめ、G2却下時はフェーズ4に差し戻す（ADR-0013）

## 4. レビュー内部の再設計（機械的チェックのみ・完了）

design doc「フェーズ5（レビュー）の内部設計」、ADR-0004参照。

- 機械的チェック: checker/fixerの役割分離、unresolved_idsのフォールバック

当初は横断的チェック（ADR-0003・0011）・予算管理も本ステップに含める想定だったが、
実装時にADR-0003が前提としていた「LSPをMCP経由でセットアップ」がClaude Code自体の
ネイティブLSPプラグイン機構（例: `gopls-lsp@claude-plugins-official`）と置き換わる
可能性が判明し、Dockerサンドボックス内でのプラグイン導入方法に追加調査が必要な
ことが分かった。確度の高い機械的チェックのみを先に完了させ、横断的チェックは
ステップ8に切り出した。

## 5. ゲート機構

ADR-0006参照。

- ゲート到達時にコンテナ/tmuxセッションを終了させず待機させる仕組み（`GATE:<name>`終了条件をCLAUDE.mdのループルールに追加）
- `docker exec -it ... tmux attach`によるchatパス、対話内での承認マーカー書き込み

## 6. レビュー単体エントリーポイント

design doc「レビュー単体での再利用」参照。

- `masuda review start <branch-or-ref> [--base develop]`（design docは`masuda review <branch-or-ref>`と書いているが、既存の`masuda review show|chat|approve|reject`サブコマンド構成に合わせて`start`を追加する形にした）
- フェーズ0〜5の機構を、新規ブランチではなく既存refのworktreeに対して使い回す
- worktreeのパス・サンドボックスのコンテナ名は現状branch名だけをキーにしているため、同じbranchに対してフルパイプライン（`masuda plan/sandbox start`）が並行して動いていると衝突する。今回は「既存のPLAN.mdがある・既にセッションが動いている場合は拒否する」という安全策のみ実装し、根本解決（ワークスペースIDの導入）はステップ7に切り出した

## 7. ワークスペースIDによる並列実行対応（完了）

ステップ6（レビュー単体エントリーポイント）の実装中に見つかった課題。同じbranchに
対して複数の作業（フルパイプラインと`review start`、あるいは同じbranchへの複数の
`plan start`）を並行して走らせたいというニーズがあり、現状のbranch名だけをキーに
したworktree/コンテナのアドレッシングでは衝突する。

- `masuda worktree create`・`masuda plan start`・`masuda review start`は、呼び出す
  たびに一意なワークスペースID（`<branch>-<ランダム短縮文字列>`形式を想定）を発行し、
  標準出力に表示する
- `masuda plan|review show|chat|approve|reject`・`masuda sandbox start|stop`・
  `masuda worktree merge|remove`は、branch名ではなくワークスペースIDを引数に取る
  よう変更する
- 再開（resume）の意味が変わる: 現状`masuda plan start <branch>`（taskを省略）で
  「そのbranchの既存セッションを再開」としていたが、同じbranchに複数のワーク
  スペースがありうる以上、再開は`masuda plan start <workspace-id>`とID明示に変える
  必要がある
- 現在動いているワークスペースの一覧を確認する`masuda worktree list`のような
  コマンドが新たに必要になる
- マージ時に実際にmergeする対象のgitブランチ名はワークスペースIDと独立して変わらない
  （ワークスペースIDはディレクトリ・コンテナ名の一意化のためのものであり、
  git上のbranch名そのものではない）

**追加スコープ（ステップ6実装中に判明）**: masuda自身の制御ファイル（TASK.md・
PLAN.md・INVESTIGATION.md・`.masuda-gate/`・`.masuda-base-ref`・
`.masuda-review-state.json`・`review_results/`等）を、worktree（＝対象リポジトリの
git管理下）の中に置くのをやめ、`~/.local/share/masuda/workspaces/<workspace-id>/`
のような独立したディレクトリに移す。理由:

- worktree内に置いていたことで、`_compute_diff()`がレビュー対象のdiffにmasuda自身の
  ファイルを混入させてしまうバグが実際に発生した（除外リストで対症療法したが、
  そもそも置かなければ発生しない）
- TASK.md等が対象リポジトリの`git status`に常に出現し、対象リポジトリ側の
  `git add -A`等で誤ってコミットされるリスクがある
- ワークスペースIDを導入するなら、そのディレクトリ名自体をこの制御ファイル置き場
  としてそのまま使える（worktreeのパスとは完全に分離する）

この変更に伴う影響:

- Dockerサンドボックスは`/workspace`（worktree）とは別に、この状態ディレクトリ用の
  bind mountがもう一つ必要になる
- `investigate_plan_graph.py`・`implement_review_graph.py`は、相対パス
  （例: `Path("TASK.md")`）でcwd＝worktree前提の実装になっているため、状態
  ディレクトリを指す絶対パスを受け取って使うよう作り直しが必要
- git操作（`git status`・`git diff`・`git add`等）は引き続きworktreeに対して行う
  （cwdの使い分け、または`git -C <worktree>`への統一が必要）

**実装結果**: 上記の通り`internal/workspace`パッケージを新設し、`worktree`/`sandbox`/
`hostloop`・両オーケストレーター・`cmd/masuda`各コマンドをワークスペースID対応に
書き換えた。副次効果として、masuda自身の制御ファイルがworktree外に出たことで
`implement_review_graph.py`の内部ファイル除外ロジック（`_MASUDA_INTERNAL_FILES`等）
と`masuda review start`の衝突拒否安全策が丸ごと不要になり削除できた。

実機テストで、同一branchに対する2つの並行ワークスペースが衝突なくG1ゲートまで
進むこと、成果物が状態ディレクトリ側にのみ生成されることを確認済み。途中で
Claude Codeの許可ルール`Edit(/abs/path)`（先頭スラッシュ1つ）がworktree外の絶対パス
に対して常に確認プロンプトを出し自動承認されない実装上の罠を発見し、
`Edit(//abs/path)`（先頭スラッシュ2つ）に修正して解消した
（[anthropics/claude-code#25137](https://github.com/anthropics/claude-code/issues/25137)、
[#18200](https://github.com/anthropics/claude-code/issues/18200)）。

## 8. 横断的チェック（LSP経由の整合性検証）

design doc「フェーズ5（レビュー）の内部設計」、ADR-0003・0011参照。ステップ4から
切り出し。着手前に以下を調査する必要がある。

- Claude CodeのネイティブLSPプラグイン機構（MCP経由の自前実装ではなく）を使う場合、
  Dockerサンドボックス内でLSPプラグイン（gopls-lsp等）をどう導入するか
  （ビルド時にマーケットプレイス経由でインストールするか、gopls本体のみ入れて
  別の方法でLSP登録するか）
- explorer→verifierの1パス構成（redoなし、ADR-0011）
- 予算管理: `ITERATION_BUDGET`の単位を「サブエージェント起動1回」に統一、
  サブエージェントごとの内部ターン数上限を追加
