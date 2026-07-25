# ADR-0001: レビュー実行方式を直接API呼び出しから自己ループ方式に変更する

## Status

Accepted (2026-07-25)

## Context

`feat/github-actions-langgraph-nodes`ブランチのレビュー実装は、`langchain_anthropic.ChatAnthropic`経由でAnthropic APIを直接呼び出している。GitHub Actions上での非対話実行を前提としており、観点(perspective)ごとに`review_node`→`check_node`を実行し、redoを含めると1回のレビューで多数のAPI呼び出しが発生する。この方式は従量課金（USD建て）であり、円安の影響もあってコストが高額になっている。

一方`develop`ブランチには、tmux上でClaude Code CLIを`--dangerously-skip-permissions`で自己ループさせるPoCが既にある。`CLAUDE.md`のループ仕様に従い、LangGraphオーケストレーターが`TASK.md`を書き換えて次の指示を渡す方式で、Claude Code CLIはサブスクリプション認証で動くため従量課金が発生しない。

## Decision

レビューの実行方式を、直接API呼び出しから`develop`ブランチの自己ループ方式（サブスクリプション認証のClaude Code CLIをtmuxで動かし、TASK.md経由で指示を渡す）に全面移行する。あわせてトリガーもGitHub Actions/Webhookからローカル実行に変更する（実際のユースケースがローカルPCでの実行であるため）。

`perspectives/config.py`の13観点定義や、review→check→redoのグラフ構造自体はロジックとして流用する。

## Alternatives Considered

- **LangGraphの構造は維持し、バックエンド呼び出しだけをheadlessな`claude -p --output-format json`に差し替える**: 既存コードの9割を再利用でき、Pydanticによる構造化出力の検証も維持できる小さな移行で済む。しかし`develop`ブランチで既に検証済みのアーキテクチャ（tmux上の対話的セッションが自己完結してループする）と一貫性が取れないため不採用。
- **GitHub Actionsトリガーを維持する**: そもそも`feat`ブランチを作った理由がGitHub上でのトリガーだったが、実際のユースケースはローカルPCでの実行でありGitHub Actionsを使わないため不採用。

## Consequences

- 従量課金（USD建て）が発生しなくなる
- 直接APIのtool-calling強制によって得ていた「必ずスキーマ準拠のJSONが返る」保証が失われる。サブエージェントの成果物をファイル経由で機械的に検証し、不正なら再試行させる仕組みが別途必要になる（[[0002-workflow-orchestrator-with-subagent-delegation]]参照）
- 観点間で共有していたプロンプトキャッシュ（`cache_control`）の恩恵が失われ、レビュー全体のウォールクロック時間が伸びる可能性がある。ただし従量課金ではないためコストには影響しない
- `COST_BUDGET_USD`のようなトークン課金前提の予算管理は不要になり、反復回数ベースの予算管理のみで足りる
