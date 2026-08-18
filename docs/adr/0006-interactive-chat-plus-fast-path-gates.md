# ADR-0006: ゲートの承認は対話（chat）と即決（approve/reject）の2パスを用意する

## Status

Accepted (2026-07-25)

- 一部改訂: [[0042-mcp-tool-call-replaces-inotifywait-gate-wait]] — chat内での自己承認は`resolve_gate_from_chat` MCP tool呼び出しに置き換わり、`triage`ゲートに対しては技術的に禁止された
- 一部改訂: [[0044-remove-docker-execution-runtime-vmbackend-only]] — アタッチ手段は`docker exec -it ... tmux attach`ではなくVMゲストへのSSH。CLIコマンドも`masuda plan chat`/`masuda review chat`ではなく`masuda chat <workspace-id>`に統一されている

## Context

G1（プラン承認）・G2（レビュー承認）の当初案は、ファイル経由の非同期承認フローだった。ゲートに到達したコンテナは成果物（PLAN.md等）を書き出して終了し、人間はCLIでそれを確認して承認/却下ファイルを書き込み、CLIが新しいコンテナを起動して再開する、という設計。

しかし実際の承認の運用は、AIが提示したプランに対して質疑応答を繰り返し、懸念が全てクリアになってから承認する、という対話的なプロセスである。ファイル経由の非同期フローでは、1回の質疑応答のたびにコンテナの起動・終了が挟まり、対話としては遅すぎる。

masudaには既に`ttyd`によるWebターミナルの仕組みがあり、tmuxセッションに人間が接続して覗く・操作することができる基盤が存在する。

## Decision

ゲートに到達しても、コンテナ（tmuxセッション）は終了させずに待機させる。人間は以下の2通りの方法で応答できる。

- `masuda plan chat` / `masuda review chat`: `docker exec -it ... tmux attach`で直接tmuxセッションにアタッチし、プランやレビューを生成した本人（同じコンテキスト）と直接質疑応答する。納得したら会話の中で「進めていい」と伝えると、Claude自身が承認マーカーを書いてループを再開する
- `masuda plan approve|reject "<feedback>"` / `masuda review approve|reject "<feedback>"`: 質疑応答が不要な場合の即決パス。別ターミナルから承認マーカー・却下フィードバックをファイルに書き込む

## Alternatives Considered

- **ファイル経由の非同期承認のみ**: 対話的な質疑応答には往復が遅すぎるため不採用。
- **ttydによるブラウザ経由のアタッチのみを対話手段とする**: ブラウザを開く必要があり、普段のCLI操作の流れから外れる。`docker exec -it`によるターミナルへの直接アタッチを主経路とし、ttydは「今何が起きているか覗きたい時」の補助手段として残す。

## Consequences

- ゲート待ち中のコンテナは終了せず起動したままになる。ただしLLM呼び出しは発生していないアイドル状態のため、リソース消費はほぼ無視できる
- ループ仕様（CLAUDE.md）に「DONE」と並んで「GATE:\<name\>」という、対話待ちのための終了条件を追加する必要がある
- 承認そのものがコマンド操作ではなく対話の中で完結する場合があるため、承認マーカーをClaude自身が書く経路（chat経由）と、CLIが書く経路（approve経由）の2種類を両立させる設計が必要になる
