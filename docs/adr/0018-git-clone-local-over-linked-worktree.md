# ADR-0018: worktreeは`git worktree add`ではなく`git clone --local`で作る

## Status

Accepted (2026-07-29)

## Context

[[0012-worktree-created-before-investigation]]で「worktreeの作成タイミング」を決めたが、「どう作るか」は未決定だった。

素直な選択肢はgit標準の`git worktree add`によるlinked worktreeである。しかし実装・実機検証したところ、linked worktreeは対象worktree自身の`.git`ファイルとメインリポジトリの`.git/worktrees/<name>`ディレクトリが、互いの絶対パスを直接参照し合う構造であることが判明した。masudaのDockerサンドボックス（`internal/sandbox`）はworktreeを`/workspace`のような、ホスト上のパスとは異なるコンテナ内パスにbind mountする運用のため、この双方向の絶対パス参照がコンテナ内で解決できず壊れることを実機で確認した。

## Decision

`internal/worktree.Create`は`git worktree add`ではなく`git clone --local <repoRoot> <dir>`によるローカルクローンを使う。`--local`はrepoRootとdirが同一ファイルシステム上にある場合オブジェクトをハードリンクするため、フルクローンでありながらコストは低く抑えられる。

パッケージ名・CLIの概念としては引き続き「worktree」という言葉を使うが（[[0014-workspace-id-and-external-state-directory]]のワークスペース概念の実装詳細として)、実体はgitの`git worktree`機能ではなく独立したクローンである。

## Alternatives Considered

- **`git worktree add`によるlinked worktree**: ホストとコンテナでパスが異なるbind mount運用に耐えられないことが実機で判明したため不採用。
- **メインチェックアウトを直接読み書きする（worktreeを作らない）**: [[0012-worktree-created-before-investigation]]で既に「複数タスクの並行実行」を理由に不採用と判断済み。

## Consequences

- クローンされたブランチはメインリポジトリの参照グラフに含まれないため、`masuda workspace merge`はクローン側のブランチを`git fetch`でメインリポジトリへ引き込んでから`git merge`する必要がある（linked worktreeならこの手順は不要だった）
- `--local`のハードリンクにより、フルクローンでも実用上のディスク・時間コストは低く保てる
- パッケージ名・CLIの概念としては「worktree」という言葉を使い続けるが、内部実装はgitの`git worktree`機能を一切使わないという食い違いが生じる。意図的なトレードオフであり、コード側（`internal/worktree`パッケージdoc）に明記して混乱を避ける
