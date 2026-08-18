# ADR-0017: ゲート待機はinotifywaitの単発ブロッキング呼び出しで実装する

## Status

Superseded by [[0042-mcp-tool-call-replaces-inotifywait-gate-wait]] (2026-08-17)

Accepted (2026-07-29) — ゲート待機はMCP tool（`wait_for_gate_change`）呼び出しに置き換わり、`inotify-tools`もDockerfileから削除済み。本ADRが避けようとした「`while`ループがBashの許可リスク評価に引っかかる」問題は、ツール呼び出しがそもそもBashコマンドでないことにより問題のクラスごと消えた。

## Context

[[0006-interactive-chat-plus-fast-path-gates]]で`GATE:<name>`という「セッションを終了せず、承認/却下ファイルの状態が変わるまで待つ」終了条件を定めたが、その待機自体をどう実装するかは決めていなかった。

単発Bashの`while`ループでファイルの中身をポーリングする実装は、動的な文字列を含むコマンドとして毎回確認プロンプトを要求され、無人ループが確認待ちで詰まることを実機で確認した。切り分けのため、ファイル変化を監視するMonitorツールに切り替えたが、ロードマップ7番の実機テストで、Monitorツール自体も内部で同種のポーリング（`status=$(...)`のようなbash的な危険パターン）を組み立てるため、同じ確認プロンプトで詰まる場面を確認した。

さらに切り分けるため、同一の`--allowedTools`設定で`claude --print`サブプロセスを起動し、単発ブロッキングの`inotifywait`呼び出し（動的な絶対パスを含む）を試したところ、確認プロンプトは一切発生しなかった（`permission_denials: []`を実機確認）。

## Decision

ゲート待機の実装を、`inotifywait -e modify,close_write,move_self $GATE_FILE`のような、ループを伴わない単発のブロッキング呼び出しに統一する（`system_prompt.md.tmpl`・`runtime/CLAUDE.md`両方に指示として明記）。Dockerイメージに`inotify-tools`を追加し、ホスト側の前提条件にも追記した。

問題の本質は「動的パスを含むコマンドだから確認を求められる」ことではなく、「`while`ループ構文（Monitorツールの内部実装も含む）自体がClaude Codeの許可リスク評価に引っかかる」ことだと判明した。単発のブロッキング呼び出しであれば、コマンド自体に動的な絶対パスが含まれていても確認は要求されない。

## Alternatives Considered

- **単発Bashの`while`ループでポーリングする**: 動的文字列を含むコマンドとして確認プロンプトが必ず出るため不採用。
- **Monitorツールでファイル変化を監視する**: 内部実装が同種のポーリングパターンを組み立てるため、同じ確認プロンプトに引っかかる。ツールの抽象化レベルを上げても、内部実装がbashのwhileループ的パターンである限り回避できないと判明したため不採用。

## Consequences

- ゲート待機の指示文（`system_prompt.md.tmpl`・`runtime/CLAUDE.md`）は`inotifywait`の単発呼び出しコマンドを明記する
- Dockerイメージ・ホスト側インストール要件の両方に`inotify-tools`（`inotifywait`コマンド）が前提条件として追加される
- 今後、同様の「ファイル変化を待つ」実装を追加する際は、whileループやその場しのぎのポーリングではなく、単発ブロッキング呼び出しを使う設計を踏襲する必要がある
