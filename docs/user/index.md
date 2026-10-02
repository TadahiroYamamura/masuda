# 利用者向け

masudaは、Claude Codeに調査・計画・実装・レビューをさせ、人間は要所（計画の承認、レビュー結果の承認など）でだけ判断する、という流れをローカルPCで無人実行するツール。

- エージェントは使い捨てのVMの中で動き、ホストのファイルや秘密には触れない
- 作業はmasudaが持つ作業用のgitリポジトリ（staging）に溜まり、人間が承認したcommitだけが手元のリポジトリへ反映される
- 流れはYAMLのワークフローで書ける。新機能の開発（`develop`）とレビュー（`review`）を同梱している

## 最初に読む順

1. [導入](install.md): 必要なものを入れ、`masuda-sandbox serve`と`masuda serve`を起動する
2. [はじめての1周](quickstart.md): 手元のリポジトリで`develop`ワークフローを1回通す
3. [概念](concepts.md): ワークスペース・staging・ゲートなど、画面に出てくる言葉の意味

ここまでで使い始められる。あとは必要になったときに引く。

| 知りたいこと | ページ |
|---|---|
| コマンドとフラグ | [CLIリファレンス](cli.md) |
| `.masuda/settings.json`・`settings.local.json`の項目 | [設定ファイル](settings.md) |
| ワークフローを変える・自分で書く | [ワークフロー](workflows.md)、[ワークフロー定義の仕様](reference/workflow-schema.md) |
| レビューで何を見るかを変える | [レビュー観点](reviews.md) |
| APIキー等の秘密、外部への通信、rootが要るテスト | [秘密・egress・特権コマンド](secrets-and-egress.md) |
| 止める・再開する・中を覗く・結果を読む | [運用](operations.md) |
| うまく動かない | [トラブルシューティング](troubleshooting.md) |
