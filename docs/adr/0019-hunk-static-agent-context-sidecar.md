# ADR-0019: Hunk統合はホストCLIが起動する静的agent-contextサイドカー方式を採る

## Status

Accepted (2026-07-30)

## Context

masudaのG2（レビュー承認ゲート）は現状、人間が`final_report.md`というMarkdownの要約テキストを読んで承認/却下を判断する。AIの指摘とdiff上の実際のコード位置との対応付けは人間が目視で追う必要がある。[Hunk](https://github.com/modem-dev/hunk)（ターミナルベースの差分ビューア）を導入すれば、指摘をdiffの該当箇所に注釈として重ねて表示できる。

Hunkはエージェント連携について性質の異なる2つのモードを提供する。

- **ライブセッション制御**: 人間が先に`hunk diff`等でTUIを起動し、別プロセス（エージェント）がローカルloopbackデーモン経由で`hunk session comment apply`等を叩いてコメントを追加する。Hunk自身のドキュメント（`skills/hunk-review/SKILL.md`）は「TUIはユーザーのためのものであり、エージェントが`hunk diff`/`hunk show`を勝手に起動して操作すべきではない」と明記しており、人間が先にセッションを開いていることが前提の設計になっている
- **静的サイドカーファイル**: 事前に生成したJSON（`--agent-context <file>`）を渡して`hunk diff`/`hunk show`を起動する。起動主体を問わず、機械的に生成した注釈データを読み込ませるだけで完結する

masudaは既に、ホスト側CLIが対話ツールをexecで直接起動するパターンを持つ（`masuda chat`が`docker exec -it ... tmux attach`を起動する、[[0006-interactive-chat-plus-fast-path-gates]]）。worktreeは`internal/worktree.Dir`によりホスト上の実ディレクトリとして存在し（[[0018-git-clone-local-over-linked-worktree]]）、フェーズ5の成果物（`review_results/`）が置かれる状態ディレクトリもホスト側から直接読める（[[0014-workspace-id-and-external-state-directory]]）。一方、レビューを行うサブエージェント自体はDockerサンドボックス内で動く。

## Decision

`masuda review hunk <workspace-id>`という新規サブコマンドを追加する。ホストのGo CLIが以下を行う。

1. 状態ディレクトリの`review_results/result_*.json`（`.masuda-review-state.json`のunresolvedリストに載っている観点のみ）と`cross_cutting_verified.json`を読み、Hunkの`agent-context.json`スキーマ（`{version, summary, files: [{path, summary, annotations: [{newRange:[start,end], summary, rationale, author}]}]}`）に変換し、状態ディレクトリ内に書き出す
2. worktreeディレクトリをカレントディレクトリとして`hunk diff --agent-context <生成したパス>`を`syscall.Exec`する（`cmd/masuda/gate.go`の既存`attach()`と同じ、プロセス置き換え方式）

既存の`masuda review show`（`final_report.md`のテキスト表示）はそのまま残し、置き換えない。人間はどちらを使ってもよい。

## Alternatives Considered

- **ライブセッション制御（`hunk session comment apply`）**: Hunk自身が「TUIはユーザーが起動するもので、エージェントが無断で起動・操作すべきではない」と明記しており、`review hunk`一発でTUI起動とコメント追加を一体化する体験には本来即さない。加えてレビューを行うサブエージェントはDockerサンドボックス内で動くため、ホスト側で起動したHunkのloopbackデーモン（127.0.0.1）にコンテナ内から到達させるにはネットワーク設定（`--network host`やhost.docker.internal経由）が追加で必要になり、静的サイドカー方式より複雑になる。不採用。
- **`masuda review show`自体をHunk起動に置き換える**: ユーザー判断により不採用。`final_report.md`の要約プレーンテキスト（問題なし一覧・自動修正済み一覧等）とHunkの行単位の注釈は役割が異なり、どちらかを削ると欠ける情報がある。

## Consequences

- Hunkはnpm/Homebrew/Nixでのインストールが前提の外部依存になる。未インストール環境では`review hunk`はエラーで案内するのみとし、`review show`は引き続き無条件に機能する（Hunkをmasudaの必須依存にしない）
- `hunk diff --agent-context`はサイドカーJSON生成時点のスナップショットであり、生成後に人間がworktreeを直接編集する等でファイルが変化すると注釈がずれうる。`review hunk`起動のたびに毎回サイドカーJSONを再生成する運用で実用上は吸収する
- Hunkの`--agent-context`が要求する`newRange`（行番号範囲）を得るには、指摘データを構造化file+line形式に変える必要がある。この変更は[[0020-structured-file-line-schema-for-findings]]で決定する
