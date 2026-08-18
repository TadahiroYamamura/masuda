# ADR-0007: ループ仕様CLAUDE.mdは対象リポジトリのルートではなく`~/.claude/CLAUDE.md`に配置する

## Status

Accepted (2026-07-25)

- 一部改訂: [[0044-remove-docker-execution-runtime-vmbackend-only]] — `runtime/CLAUDE.md`をゲストの`~/.claude/CLAUDE.md`へ置く方針は不変だが、注入方法はコンテナ起動時の`docker cp`ではなくrootfsビルド時の`ExtraFile`焼き込みになった

## Context

`develop`ブランチのCLAUDE.mdは「作業ループ仕様」（TASK.mdの読み書きルール、終了条件等）を記述しており、サンドボックスコンテナ内で自己ループするClaude Codeセッションに読ませることを意図している。一方`feat/github-actions-langgraph-nodes`ブランチのCLAUDE.mdは「masuda自身のツールがどう動くか」を説明する、人間・開発者向けの一般的なプロジェクトドキュメントである。masudaリポジトリのルートに置くCLAUDE.mdとして、両者は役割が異なり同じ場所に共存できない。

さらに、ループ仕様は本来「サンドボックス環境がどう動くか」という、サンドボックス自体に紐づく設定であり、特定の対象リポジトリに紐づくものではない。ところがこれをサンドボックス起動時に対象リポジトリ（例: oncall-platformのような実際のレビュー・実装対象）のworktreeのルートに`CLAUDE.md`として配置しようとすると、対象リポジトリに既に存在するプロジェクト固有のCLAUDE.md（アーキテクチャ説明等の実際に必要な指示）を上書きしてしまう。

Claude Codeはユーザーレベル（`~/.claude/CLAUDE.md`）とプロジェクトレベルのCLAUDE.mdを両方読み込んで重ね合わせる機能を持っている。

## Decision

masudaリポジトリのルートCLAUDE.mdは「masuda自身を編集するAI/開発者向け」の標準的な意味に一本化する。ループ仕様の内容は`runtime/CLAUDE.md`としてmasudaのリポジトリ内で管理し、サンドボックスコンテナ起動時にコンテナ内の`~/.claude/CLAUDE.md`（対象リポジトリのworktreeとは独立した場所）に配置する。対象リポジトリ側のCLAUDE.mdには一切触れない。

## Alternatives Considered

- **対象リポジトリのworktreeルートの`CLAUDE.md`を上書きコピーする**: 対象リポジトリの既存のプロジェクト指示が失われるため不採用。
- **ループ仕様と対象リポジトリの指示を、コンテナ起動時に1つのファイルへマージする**: Claude Codeが既にユーザーレベル/プロジェクトレベルのCLAUDE.mdを重ね合わせる仕組みを持っているため、わざわざ独自にマージ処理を実装する必要はないと判断し不採用。

## Consequences

- ループ仕様は特定の対象リポジトリから完全に独立し、どんな対象リポジトリのworktreeに対しても、リポジトリ側の設定を壊さずにそのまま使い回せる
- masudaのリポジトリ構造上、「ループ仕様（コンテナに焼き込まれ、実行時に配置されるファイル）」と「masuda自身の開発用ドキュメント」を明確に分けて管理する必要がある（`runtime/` vs ルートのCLAUDE.md）
