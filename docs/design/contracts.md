# 契約

3つのリポジトリを結ぶ境界の定義と、その変更手続き。

## 一覧

| 契約 | 所有リポジトリ | 定義ファイル | 契約テスト |
|---|---|---|---|
| sandbox API | `masuda-sandbox` | `proto/masuda/sandbox/v1/sandbox.proto` | `test/contract/*.test.ts`（実VMでサービスを叩く） |
| engine API | `masuda-engine` | `engine/api.go`（`Runner`・`Store`・`Engine`）、`docs/workflow-schema.md`（エージェント定義の形式を含む） | `contract/*_test.go`（`Runner`のスタブで定義を歩く） |
| masuda API | `masuda` | `proto/masuda/api/v1/masuda.proto`、`docs/guest-protocol.md` | `contract/*_test.go`（sandbox serviceとengineのフェイクで叩く） |

契約テストは**完了の定義**。緑なら完了、赤なら未完で、実装者の自己申告では判定しない。

## 変更の手続き

- 契約を所有するリポジトリであっても、実装者は契約ファイルを勝手に変えない。変えたくなったら、理由と提案を`HANDOFF.md`の「契約への提案」に書いて止まり、監督（Fable）の判断を待つ
- 監督が契約を変えたら、使う側のリポジトリにも同じコミットで追従を入れる（protoなら生成コードの再生成、Goなら型の追従）
- 契約ファイルは後方互換を気にしない。3リポジトリは常に同じ時点の契約で揃える（M3までは特に）。バージョン番号は`v1`固定

## 生成コード

- Go: `buf generate`で`gen/`へ。`protoc-gen-go`と`protoc-gen-connect-go`
- TypeScript: `buf generate`で`src/gen/`へ。`@bufbuild/protoc-gen-es`（v2）のみ。Connect v2はサービス記述子を`*_pb.ts`から直接使うので、connect-es用プラグインは使わない
- 生成コードはコミットする（使う側がbufを持たなくてよいように）

## 通信の前提

- sandbox APIは`$XDG_RUNTIME_DIR/masuda-sandbox.sock`（UDS）でh2cを待ち受ける。クライアント・サーバーの両方のストリーミングを使うのでHTTP/2が要る
- masuda APIは`$XDG_RUNTIME_DIR/masuda.sock`（UDS）と、設定で有効にしたときだけ`127.0.0.1:<port>`で待ち受ける。ブラウザからはループバックのHTTP+JSONで呼ぶ
- どちらも認証は持たない。同じホストアカウントのプロセスは信頼する（脅威モデルのとおり）
