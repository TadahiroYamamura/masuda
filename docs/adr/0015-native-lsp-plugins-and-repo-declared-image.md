# ADR-0015: 横断的チェックはネイティブLSPプラグイン方式を採用し、使用するDockerイメージはrepo側の宣言に委ねる

## Status

Accepted (2026-07-27)

## Context

[[0003-mechanical-vs-complex-review-nodes]]・[[0011-iteration-budget-per-subagent-invocation]]は、横断的チェック（LSPによる整合性検証）をGo言語のLSPサーバー（gopls）をMCP経由でサブエージェントに持たせる方式で実現する前提だった。

ロードマップ8番（横断的チェック）着手にあたり、Dockerサンドボックス内でのLSP導入方法を調査したところ、Claude Code自体がネイティブLSPプラグイン機構（`gopls-lsp`・`pyright-lsp`・`typescript-lsp`等、公式マーケットプレイス`claude-plugins-official`経由）を持ち、TypeScript・Python含む主要13言語すべてに公式プラグインが用意されていることが判明した。いずれも`lspServers`という共通スキーマ（`command`/`args`/`extensionToLanguage`）に従っており、自前のMCPサーバー実装より導入コストが低い。

これに伴い新しい判断が必要になった。対象repoの言語ごとにLSPツールを同梱したDockerイメージ（例: `masuda-loop:go`）をどう選ぶか——masuda側で言語を検出するヒューリスティックを持つか、対象repo自身に判断を委ねるか、という論点である。

## Decision

MCP経由の自前LSP実装ではなく、Claude Codeのネイティブプラグイン機構を採用する。Dockerイメージ側に言語ごとのツールチェーン＋対応LSPプラグインを同梱したバリアント（`docker/go`・`docker/python`・`docker/typescript`・複数言語混在repo向けの`docker/full`）を用意する。ベースイメージのビルド時に`claude plugin marketplace add anthropics/claude-plugins-official`で公式マーケットプレイスを登録し、各バリアントは対応する`claude plugin install <name>@claude-plugins-official`を実行するだけで済む。

masuda自身は対象repoの言語を検出するヒューリスティックを一切持たない。ユーザー（または対象repoの保守者）が、対象リポジトリのルート直下にコミットする設定ファイル`.masuda.json`の`image`フィールドで使用するDockerイメージを明示する（`masuda sandbox start`/`masuda review start`の`--image`フラグでも上書き可能。優先順位は`--image`フラグ＞`.masuda.json`＞デフォルトイメージ）。これは秘密情報の扱い（対象repoの`.claude/settings.json`が宣言するdeny設定にmasudaが従う、[[0007-loop-protocol-claude-md-in-user-scope]]以来の一貫した思想）と同じ「repoの設定はrepo自身の責任」という原則に基づく。

## Alternatives Considered

- **全13言語のツールチェーンを1つのイメージに常備する**: JVM（Java/Kotlin）・.NET SDK・Swiftツールチェーンはそれぞれ数百MB〜1GB級であり、`masuda sandbox start`のたびに作り捨てるコンテナという運用実態と噛み合わない。実際に必要になるのは通常1〜2言語のみであるため不採用。
- **ワークスペース起動のたびに必要な言語ツールを動的にインストールする**: 無人ループの途中に外部レジストリ（Goモジュールプロキシ・npmレジストリ等）へのネットワーク依存を持ち込むことになる。このプロジェクトは既に複数回、「無人ループ中の外部依存は壊れる」という教訓を実機で踏んでおり（テーマ選択ウィザード・認証切れ・Docker名衝突などをいずれもビルド時固定・起動時の冪等な後始末で解決してきた）、同じ轍を踏むことになるため不採用。バージョンも都度変わり再現性がない。
- **masuda側で対象repoの言語をファイル検出（`go.mod`・`package.json`等の存在）で自動判定する**: monorepoでの多言語混在への対応や誤検知など判定ロジック自体の複雑さに加え、既存の`--image`フラグの仕組みをそのまま使えば判定ロジックを一切書かずに済むことに気づき、よりシンプルな「repoが宣言する」方式を採用した。

## Consequences

- `internal/config`パッケージを新設し、`.masuda.json`（`image`・`base`の2フィールド）を読む`resolveImage`/`resolveBase`（`cmd/masuda/main.go`）を`masuda sandbox start`・`masuda review start`・`masuda workspace create|merge`・`masuda plan start`に配線した
- Dockerイメージのビルド・保守対象が1つ（base）から5つ（base + go/python/typescript/full）に増えた
- 複数言語混在repoは`docker/full`（kitchen sink）で対応するが、5言語以上が混在するケースなど、バリアントの組み合わせ爆発には対応していない
- 実機テストで、`claude plugin marketplace add`・`claude plugin install`はいずれも認証不要（公開GitHubリポジトリへの`git clone`のみ）でDockerビルド時に動作すること、プラグイン状態（`~/.claude/settings.json`・`~/.claude/plugins/`）はコンテナ起動時にホストからbind mountされる`~/.claude.json`・`~/.claude/.credentials.json`とは別ファイルで上書きされないことを確認済み
