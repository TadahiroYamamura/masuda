# ADR-0041: 状態デーモンの通信はMCPに統一し、trusted用フルセットとClaude向けcurated setの2面をUDSで公開する

## Status

Accepted (2026-08-17)

## Context

[[0040-per-workspace-state-daemon-for-masuda-owned-state]]で状態デーモンの存在自体は決まったが、それを誰がどう呼ぶかは未決定だった。呼び手は大きく2種類ある——ホストCLI（`cmd/masuda`）・`orchestrator/*.py`のような「信頼された、masuda自身のコード」と、Claudeセッション自身（`runtime/CLAUDE.md`のGATE待機ステップ）という「エージェント本体」である。

検討の初期段階では、trusted側は独自の薄いプロトコル（改行区切りJSON、いわゆるJSONL）、Claude向けだけMCPを話す、という2プロトコル案を採用しかけていた。この案を撤回した理由は将来の拡張性にある——masudaにはClaudeへ外部ツール（例: GitHub Issueの読み取り）を持たせたいという構想があり、それを実現する自然な方法は「masuda本体が実装を持つのではなく、既製のMCPサーバーをデーモンの下にアグリゲータとして取り込む」ことだと判断した。trusted側だけ別プロトコルのままだと、この種の拡張のたびに2つのプロトコル実装を保守することになる。

## Decision

デーモンの通信は`github.com/modelcontextprotocol/go-sdk`によるMCP（JSON-RPC 2.0）に統一する。[[0040-per-workspace-state-daemon-for-masuda-owned-state]]の`Store`から、性質の異なる2つの`mcp.Server`インスタンスを構成する。

- **trusted用フルセット**（`internal/statedaemon/mcpserver.New`）: `state_get`/`state_put`/`state_delete`/`state_list`/`state_wait_for_change`の5tool。`daemon.sock`というUDSソケットで公開する
- **Claude向けcurated set**（`internal/statedaemon/mcpserver.NewCurated`）: `wait_for_gate_change`・`resolve_gate_from_chat`の2toolのみ。`daemon-curated.sock`という別のUDSソケットで公開する

同じデーモンプロセス（`masuda internal statedaemon`）が両方のソケットを別goroutineで待ち受け、片方が停止したらもう片方もcancelして道連れに止める。

trusted側の実際の呼び手は2種類: ホストCLI（`cmd/masuda`、Go）は`internal/statedaemon/mcpclient`を直接importしてin-processで呼ぶ。`orchestrator/*.py`はMCPクライアントをPython側に実装せず、`masuda internal state get/put/delete/list/wait <key>`という新設の隠しサブコマンドをsubprocessで叩く——プロトコル実装をGo側1箇所に集約するためである。この結果、`orchestrator/*.py`を実行するDockerサンドボックス内に`masuda`バイナリ自体が必要になり、`Dockerfile`にマルチステージビルド（`golang:1.26`でビルドし、成果物だけ最終イメージへ`COPY --from`）を追加した。従来この最終イメージにはGoツールチェーンはおろか`masuda`バイナリ自体も含まれておらず（`orchestrator/`・`runtime/`・Python venvのみ）、今回が初めての追加になる。

## Alternatives Considered

- **trusted側は独自の薄いJSONL、Claude向けだけMCP（当初案）**: 実装コストは小さいが、将来的な子MCPサーバーのアグリゲータ化（Context参照）を見据えると、プロトコルを2つ保守する理由がない。撤回した。
- **trusted用フルセットをそのままClaudeにも公開する（curated setを分けない）**: [[0029-immediate-stop-escalation-dedicated-gate]]は、triageゲートに懸念の対象となっているエージェント自身が自己承認することを規約上禁止している。フルセットの`state_put`を素通しすると、Claude自身が`gate:triage`キーへ任意の内容を書けてしまい、この規約を技術的に破れる状態になる。curated setを別に用意し、triageゲートに触れない2toolだけに絞ることで、この規約を（部分的に）技術的に強制できる（詳細は[[0042-mcp-tool-call-replaces-inotifywait-gate-wait]]）
- **ホスト全体で単一のデーモンが全ワークスペースのMCPサーバーを多重化する**: [[0040-per-workspace-state-daemon-for-masuda-owned-state]]のAlternatives Consideredと同じ理由（masudaは常駐サービスを持たない運用モデル、ワークスペース単位のアドレッシングとの整合性）で不採用

## Consequences

- `github.com/modelcontextprotocol/go-sdk`がmasudaにとって新規の直接依存になる
- Dockerイメージが初めて`masuda`バイナリ自体を含むようになり、マルチステージビルドの保守（Goのバージョン更新等）が新たに発生する
- curated setは現状意図的に2toolしかない。Claudeに新しい能力（外部ツール等）を持たせたくなった場合、子MCPサーバーのアグリゲータ化（GitHub Issue #35のコメントに設計方針のみ記録、未実装）が必要になる
- trusted側の設定（子MCPサーバーの起動コマンド・トークン等）を対象リポジトリの`.masuda/settings.json`に書けるようにする案も検討したが、これは対象リポジトリ側の`settings.json`が無条件に信頼される既存の未解決課題（GitHub Issue #19）を悪化させるため、プロジェクトの宣言とユーザーの承認・秘密情報を分離する設計（`repoRoot/.masuda/settings.local.json`）を構想したのみで、本ADR・本セッションでは未実装
