# masuda

Claude Codeに調査・計画・実装・レビューなどの作業をさせ、人間が要所で判断する流れを、ローカルPC上の使い捨てVMで無人実行するためのツール。Claude Code CLIをサブスクリプション認証のままVM内のtmuxで自己ループさせ、ホスト側のmasudaがワークフローの進行・ゲート・gitへの反映を受け持つ。

> 再設計の途中。現在動くのは`masuda serve`（公開APIの骨組み）と`masuda version`だけで、`masuda run`等は順に実装している（[docs/work-orders.md](docs/work-orders.md)）。

## 3つのリポジトリ

| リポジトリ | 言語 | 役割 |
|---|---|---|
| masuda（このリポジトリ） | Go | `masuda serve`（常駐プロセス・公開API）とCLI。ワークスペースとstaging、2つを結線する統合層 |
| [masuda-sandbox](https://github.com/TadahiroYamamura/masuda-sandbox) | TypeScript | `masuda-sandbox serve`。Gondolin（QEMU）でVMを作り、コマンド実行・egress/秘密の方針切替・ファイル転送を行う |
| [masuda-engine](https://github.com/TadahiroYamamura/masuda-engine) | Go | ワークフロー定義の読み込み・検査・実行を行うライブラリ。masudaのプロセス内で動く |

全体設計は[docs/design/overview.md](docs/design/overview.md)。

## 使い方

ホストで2つのプロセスを常駐させ、CLIから公開APIを叩く。

```sh
masuda-sandbox serve &   # VMを扱うサービス
masuda serve &           # 公開API（$XDG_RUNTIME_DIR/masuda.sock）

masuda run workflows/develop --branch feat/x --input instructions=@todo.md
```

導入手順は[docs/INSTALLATION.md](docs/INSTALLATION.md)、開発への参加は[docs/CONTRIBUTING.md](docs/CONTRIBUTING.md)。
