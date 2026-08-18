# ADR-0014: ワークスペースIDと外部状態ディレクトリの導入

## Status

Accepted (2026-07-27)

- 一部改訂: [[0030-workspace-id-drops-branch-name-prefix]] — ワークスペースIDは`<sanitized-branch>-<hex>`ではなく乱数のみ
- 一部改訂: [[0044-remove-docker-execution-runtime-vmbackend-only]] — `/workspace`・`/masuda-state`はDockerのbind mountではなくVMゲストへのvirtiofs共有

## Context

ロードマップ6番（`masuda review start`、レビュー単体エントリーポイント）の実装中、worktreeのパス・サンドボックスのコンテナ名がbranch名だけをキーにしていることに気づいた。同じbranchに対してフルパイプライン（`masuda plan start`→`sandbox start`）と単体レビュー（`masuda review start`）を並行稼働させると、同じworktree・同じコンテナを奪い合って衝突する。

この課題をユーザーと検討する過程で、対応範囲について2段階の見直しがあった。

1. 当初は「`masuda review start`だけを一意化する」案で合意しかけたが、「同じbranchに対して複数の`plan start`を並行させたい」というニーズもあり得ることに気づき、ユーザーが承認を撤回。「ワークスペース開始時点で一意な識別子を用意したい。同じブランチに複数の修正が走る可能性はあるため」という、より広い範囲（あらゆる新規開始コマンド）での対応が必要と判明した。
2. 並行してもう一つの課題が見つかった。masuda自身の制御ファイル（TASK.md・PLAN.md・INVESTIGATION.md・ゲートマーカー等）が対象リポジトリのworktree内に置かれていたため、フェーズ5の`_compute_diff()`が`git add -A`でこれらのファイルまでレビュー対象のdiffに混入させてしまうバグが実機で発生していた（除外リストで対症療法済み）。ユーザーから「masudaが動作するために作るファイルをworktree内に作るのをやめないか」という提案があり、この2つの課題を同じ仕組みで解決できることに気づいた。

## Decision

一意なワークスペースID（`<sanitized-branch>-<ランダム6桁hex>`形式、`internal/workspace.NewID`）を導入し、worktreeのパス・サンドボックスのコンテナ名・ホストループのtmuxセッション名の共通のアドレッシングキーとする。git上のbranch名そのものとは独立した値であり、同じbranchを対象にした複数のワークスペースが衝突せず並行して存在できる。

masuda自身の制御ファイル一式は、対象リポジトリのworktreeの外、`~/.local/share/masuda/workspaces/<workspace-id>/`（`XDG_DATA_HOME`を尊重）という独立した状態ディレクトリに完全に移す。Dockerサンドボックスには`/workspace`（worktree）とは別に`/masuda-state`をこの状態ディレクトリのbind mount先として用意する。

`internal/workspace`パッケージが、ID発行・状態ディレクトリ管理・メタデータ（`workspace.json`: branch/base/repo_root/created_at）の永続化を一元的に担う。

## Alternatives Considered

- **`masuda review start`だけを一意化する**: 当初この範囲で合意しかけたが、フルパイプライン同士（同じbranchへの複数の`plan start`）も衝突しうることに気づき、対応範囲が狭すぎると判断して撤回した。
- **branch名とUNIXタイムスタンプの組み合わせ、またはUUIDを識別子にする**: ユーザーから代替案として挙がったが、`masuda workspace list`で一覧表示した際にどのbranchに対するワークスペースか一目でわかる可読性を優先し、`<branch>-<短いランダム文字列>`形式を採用した。
- **masuda自身の制御ファイルを引き続きworktree内に残し、除外リストで対応し続ける**: 除外リストは当面動くが、masuda内部に新しいファイルが増えるたびにリストのメンテナンスが必要になり、根本解決にならない。ワークスペースIDを導入するなら、そのID自体をそのまま制御ファイル置き場のキーとして使えることに気づき、この機に完全分離する方を選んだ。

## Consequences

- `internal/worktree`・`internal/sandbox`・`internal/hostloop`は、branch名ではなくワークスペースIDでアドレッシングするようになった。branch名は`workspace.Info.Branch`経由でのみ参照する
- CLIの`show|chat|approve|reject`系コマンドの引数がbranch名からworkspace-idに変わった。新規作成系コマンド（`masuda workspace create <branch>`・`masuda plan start <branch> "<task>"`・`masuda review start <branch-or-ref>`）は引き続きbranch名を取り、内部でワークスペースIDを新規発行する
- 実装直後のレビュー指摘を受け、CLIコマンドグループ名を`worktree`から`workspace`に改名した。「worktree」はgit用語であり、状態ディレクトリ・メタデータまで含む広い概念（ワークスペース）の操作を持つコマンドグループ名としては違和感がある、との理由。`internal/worktree`パッケージ自体はgitチェックアウトの実装詳細として維持し、CLIコマンド名としては表に出さない
- masuda自身の制御ファイルがworktree外に出たことで、`git add -A`がこれらをdiffに混入させるバグが構造的に解消され、`implement_review_graph.py`の除外リストロジック（`_MASUDA_INTERNAL_FILES`等）を削除できた
- ワークスペースの寿命管理は手動ベースのまま（G2承認時の自動後片付け、または`masuda workspace remove`の明示実行でのみ状態ディレクトリが消える）。TTLのような自動期限切れの仕組みは未実装
