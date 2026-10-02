# 統合開発者向け

masudaの上にGUIや他のツールを作る人のためのドキュメント。

`masuda serve`は、ワークフローの実行・人間の判断・stagingの中身・リポジトリの設定を、[Connect](https://connectrpc.com/)の公開APIで提供する。CLI（`masuda run`・`masuda gate approve`等）もこのAPIを叩くクライアントの1つで、APIでできることとCLIでできることは同じ。

## APIでできること

- **実行**: 対象リポジトリでワークフローを始める・止める・再開する・消す。状態とイベントをストリームで受け取り続ける
- **人間の判断**: 計画やレビューのゲートを見せて承認・却下する。計画外の変更（deviation）やエージェントの懸念（triage）に判断する。エージェントからの質問に答える
- **差分とコメント**: ワークスペースのstaging（gitのbareリポジトリ）のref・コミット・差分・ファイルを読み、差分の行にコメントを付ける
- **設定**: リポジトリが宣言したegress・秘密・特権コマンドを承認し、秘密の値を登録し、ゲストのイメージをビルドする
- **定義の確認**: ワークフローの一覧・図（Mermaid）・検査

APIでできないこと: ゲストの端末の中で動くClaude Codeへの入力（許可の確認等）は、`AttachInfo`が返す`ssh`のコマンドで端末から入って行う。ワークスペースの実行記録・exportsのファイルは、ホストの`$XDG_DATA_HOME/masuda/workspaces/<id>/`を直接読む。

## 読む順

1. [接続](connect.md): どこで待ち受けているか、gRPC・Connect・HTTP+JSONの呼び方、ブラウザからの`fetch`、Go・TypeScriptのクライアント、認証が無いことの意味
2. [サービスとRPC](services.md): 6つのサービスの各RPCが何のためにあり、どの順で呼ぶか
3. [典型的な流れ](flows.md): ワークスペースの起動と`Watch`、ゲートの判断、差分ビュー、質問、活動の表示、停止と再開、設定
4. [エラーコード](errors.md): RPCごとに、どの条件でどのコードを返すか
5. [APIリファレンス](reference.md): `masuda.proto`から生成したメッセージとフィールドの一覧（英語）

TypeScript・JavaScriptからは、リポジトリの[`clients/ts/`](https://github.com/TadahiroYamamura/masuda/tree/main/clients/ts)にある生成済みのクライアントを使える。

## 契約

APIの正は[`proto/masuda/api/v1/masuda.proto`](https://github.com/TadahiroYamamura/masuda/blob/main/proto/masuda/api/v1/masuda.proto)で、protoのコメントが各フィールドの意味を定める。このセクションの文書はその読み方と、実装が守っている約束（エラーコード等）を書いたもの。契約の変え方は[契約](../design/contracts.md)にある。
