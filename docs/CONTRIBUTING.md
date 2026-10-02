# masuda自体の開発に参加する

masudaを**使う**手順は[INSTALLATION.md](INSTALLATION.md)。全体設計は[design/overview.md](design/overview.md)、作業単位は[work-orders.md](work-orders.md)。

## 開発環境

- Go 1.26以上。`go build ./...`・`go vet ./...`・`go test ./...`
- `../masuda-engine`に[masuda-engine](https://github.com/TadahiroYamamura/masuda-engine)をチェックアウトしておく（`go.mod`の`replace`で参照する）
- protoを変えたら`buf generate`で`gen/`を作り直してコミットする。`buf`が無ければ`go run github.com/bufbuild/buf/cmd/buf@latest generate`。sandbox APIのクライアントは`../masuda-sandbox/proto`から生成するので、[masuda-sandbox](https://github.com/TadahiroYamamura/masuda-sandbox)も隣にチェックアウトしておく
- GitHub操作（Issue作成等）は`gh`を直接使わず`scripts/gh.sh`を使う。このリポジトリ専用のトークンを`.env`から読み込んで`gh`に渡すラッパー

## 契約と契約テスト

部品の間の契約（`proto/`、`docs/guest-protocol.md`、他リポジトリの契約）と変更の手続きは[design/contracts.md](design/contracts.md)。各作業単位の完了は`contract/`の契約テストが緑であることで判定する。

```sh
go test ./contract/ -run TestCM1
```
