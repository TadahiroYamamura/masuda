# ADR-0042: GATE待機はinotifywaitではなくMCP tool呼び出しで行う

## Status

Accepted (2026-08-17)

- 一部改訂: [[0050-design-docs-hold-current-state-adrs-hold-rationale]] — Consequencesが「CLAUDE.mdの『ADRは番号順に読むと議論の経緯が追える』という既存の案内が[[0017-inotifywait-for-gate-wait-polling]]と本ADRを繋ぐ」としている点。その案内はCLAUDE.mdから削除され、現在は`docs/adr/README.md`の索引が入口になっている。0017は索引から削除済み（Supersededのため）で、到達経路は本ADRのContextからの`[[...]]`リンクのみ
- 一部改訂: [[0055-gate-marker-is-an-unconsumed-decision-waited-on-by-presence]] — 本ADRが導入した`wait_for_gate_change`は`wait_for_gate_resolution`へ改名され、意味論も「次の変化まで待つ」から「ゲートマーカーが存在するまで待ち、既に存在すれば即座に返す」へ変わった。trusted setの`state_wait_for_change`は削除された

## Context

[[0017-inotifywait-for-gate-wait-polling]]は、`runtime/CLAUDE.md`・`internal/hostloop/system_prompt.md.tmpl`両方のGATE:\<name\>待機ステップを、ゲートマーカーファイルに対する単発ブロッキングの`inotifywait`呼び出しとして実装していた。[[0040-per-workspace-state-daemon-for-masuda-owned-state]]がゲートマーカーを状態デーモンへ移したことで、Dockerサンドボックス・フェーズ1-2ホストループのどちらの経路でも、もはや監視対象のファイル自体が存在しない——[[0040-per-workspace-state-daemon-for-masuda-owned-state]]・[[0041-mcp-protocol-with-trusted-and-curated-surfaces]]が実装されコミットされた時点で、GATE待機は実際には機能しなくなっていた。本ADRはその置き換えの記録である。

## Decision

`runtime/CLAUDE.md`・`system_prompt.md.tmpl`のGATE待機ステップを、[[0041-mcp-protocol-with-trusted-and-curated-surfaces]]のcurated set`wait_for_gate_change`ツール呼び出しに置き換える。ブロッキングの実体をClaude Code自身のMCPクライアント機構に委ねる形になり、結果として[[0017-inotifywait-for-gate-wait-polling]]がそもそも避けようとしていた問題（`while`ループやMonitorツールがBashの許可リスク評価に引っかかり無人ループが確認プロンプトで詰まる）のクラス自体から外れる——このツール呼び出しはBashコマンドではない。

`masuda chat`での対話中に人間から「進めていい」と言われた場合の自己承認（[[0006-interactive-chat-plus-fast-path-gates]]のchat経路）は、`resolve_gate_from_chat`ツール呼び出しに置き換える。このツールは`name`に`"triage"`が渡されるとサーバー側でエラーを返す実装にした。従来この禁止は`runtime/CLAUDE.md`の指示文（「絶対にしないこと」）という規約上の取り決めに留まり、GitHub Issue #13が指摘する技術的強制の欠如の一例だった。今回この一点に限り技術的な強制に変わる。

`--mcp-config`（Claude Codeへ`wait_for_gate_change`等を配線するフラグ）は`http://host:port`形式のURLしか受け付けず、Unix domain socketを直接指定できない。この橋渡しに`masuda internal mcp-relay --socket <path> --port <port>`という新設の隠しサブコマンド（単純なバイト単位の双方向TCP↔UDS中継）を使う。Docker側は`runtime/entrypoint.sh`が固定ポート（39217、コンテナは独立したネットワーク名前空間を持つため固定で問題ない）で1回だけ起動し、`start_claude.sh`は同じポートを再利用する。フェーズ1-2ホストループ側（`internal/hostloop.startMCPRelay`）は、複数ワークスペースが同一ホストで並行しうるため、ワークスペースごとに空きポートを動的に選ぶ。

Dockerfileから`inotify-tools`パッケージを削除した（もう使われないため）。

### 実機検証で見つかった2つの不具合

本番相当の実装が一見完成した後、実際の`claude`バイナリ（`--print`モード）に対して素朴なテストを行ったところ、単体テスト・機構的な検証だけでは検出できない2つの不具合が見つかった。

1. **MCPツール呼び出しのハード・ウォールクロックタイムアウト**: Claude Code自身がMCPツール呼び出しに対して独自のタイムアウト（デフォルトで1分未満）を持っており、これはMCP側のprogress通知でも延長されない（Claude Code自身のエラーメッセージ文言で確認済み: `... sent no response or progress for ...s; aborting`は別系統の"idle timeout"の文言であり、`--mcp-config`のサーバーごとの`"timeout"`フィールドで上書きするハード・タイムアウトとは別物と判明した）。実機で70秒のブロッキング待機がこの対処なしでは失敗し、`"timeout": 604800000`（7日、ミリ秒）をサーバー設定へ加えると成功することを確認した。念のため`CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=0`（別系統のアイドルタイムアウトの無効化）も多層防御として併用する
2. **`--mcp-config`の可変長引数境界**: `--mcp-config`は空白区切りの複数値を取る可変長引数であるため、直後にプロンプト文字列を続けると、プロンプトが検証対象のMCP設定として誤って飲み込まれ、`claude`が「MCP config file not found: \<プロンプト全文\>」というエラーで起動直後に落ちる。`entrypoint.sh`・`internal/hostloop.go`の実際のコマンド構築コードでこれを再現した。プロンプトの直前に`--`を挟むことで解決する（プロンプト引数を持たない`start_claude.sh`は対象外）

いずれも人間のレビュー待ちが1分を超えることはほぼ確実であり、修正なしでは出荷版のGATE待機がほぼ全てのケースで静かに壊れていた。

## Alternatives Considered

- **UDS↔TCP中継にsocatを使う**: masudaは現状Dockerイメージ・ホスト両方に単一のGoバイナリとして存在する（[[0041-mcp-protocol-with-trusted-and-curated-surfaces]]でDockerイメージにも`masuda`バイナリが加わった）。socatを新規のOSパッケージ依存としてどちらにも追加するより、既存の配布モデルを再利用する約40行のGoサブコマンドの方が一貫性がある
- **`resolve_gate_from_chat`をtriageゲートにも使えるようにする（inotifywait時代の生ファイル書き込みと同じ挙動を維持する）**: [[0029-immediate-stop-escalation-dedicated-gate]]の規約を技術的に強制する数少ない機会であり、実装コストもほぼゼロ（`name`の許可リストから`"triage"`を除くだけ）だったため、あえて緩めない選択をした
- **MCPのprogress通知を実装してタイムアウトを回避する**: Claude Code自身のエラー文言が、ハード・ウォールクロックタイムアウトは"progress"通知では延長されないと明記している。progress通知が効くのは別系統のidle timeoutのみで、今回問題になったハードタイムアウトへの対処にはならないため、タイムアウト値そのものを引き上げる以外の選択肢がなかった

## Consequences

- [[0017-inotifywait-for-gate-wait-polling]]は編集せず、Docker + bind mount前提の記録として残る。本ADRが実質的にその機構を置き換えるが、CLAUDE.mdの「ADRは番号順に読むと議論の経緯が追える」という既存の案内がこの2つを繋ぐ
- `Dockerfile`から`inotify-tools`を削除した
- 7日というタイムアウト値は経験に基づく判断であり保証ではない。人間のレビューがこれより長く放置された場合、ゲート待機は静かにタイムアウトする。現状これを検知・通知する仕組みはない
- 本ADRの対象範囲は`claude --print`を使った的を絞った実機確認（本物のcurated MCPデーモン・中継・`claude`バイナリを実際に繋いだ検証）に留まる。`masuda plan start`から実際のワークスペース・Dockerサンドボックスを介した完全な無人パイプラインとしての通し検証はまだ行っていない
