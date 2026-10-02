# masuda

AIとの協同開発を、ローカルPC上のVMで無人実行するためのツール群の**統合層**。CLI、公開API（Connect）、ワークスペースとstaging、ゲスト向けMCP。ワークフローの実行は[masuda-engine](../masuda-engine)（Goライブラリ）、VMは[masuda-sandbox](../masuda-sandbox)（TypeScriptの常駐サービス）が担う。

2026-10-02にゼロから再設計した。`redesign`ブランチが現在の正。旧実装はタグ`v1-frozen-*`に凍結してある。

## どこに何があるか

- **全体設計**: `docs/design/overview.md`。最初に読む
- **契約**: `docs/design/contracts.md`。このリポジトリが所有するのは`proto/masuda/api/v1/masuda.proto`と`docs/guest-protocol.md`
- **再設計の経緯とスパイク**: `docs/design/redesign-background.md`、検証スクリプトは`docs/research/spike-gondolin/`
- **作業単位**: `docs/work-orders.md`
- 判断の理由はコミットメッセージ（`## 意図`・`## 設計上の考慮点`）。ADRは書かない

## 開発

- `go build ./... && go vet ./... && go test ./...`
- engineは`go.mod`でタグ（タグが無い間は`main`の擬似バージョン）に固定している。隣の`../masuda-engine`の作業中のコードで試すときは、gitignoreした`go.work`を作る: `go work init . && go work use ../masuda-engine`。`go.work`があるとビルドもテストも隣のチェックアウトを使う（engineの未コミットの変更も入る）ので、固定した版で確かめるときは`GOWORK=off go test ./...`。engineの版を上げるのは`go get github.com/TadahiroYamamura/masuda-engine@<tagまたはmain> && go mod tidy`
- `internal/sandboxcontract/sha.go`（sandbox.protoのSHA-256）は`go generate ./internal/sandboxcontract/`で作り直してコミットする。`buf generate`でsandboxのクライアントを作り直したときは必ず一緒に
- protoからの生成: `buf generate`（`gen/`、コミットする）。sandboxのクライアントは`../masuda-sandbox/proto`からも生成する（`buf.gen.yaml`参照）
- 契約テスト: `go test ./contract/`。sandbox serviceの**フェイク**（`masuda serve --fake-sandbox`、VMなし）とengineの実物で公開APIを叩く。これが緑なら作業項目は完了
- 実機テスト: `MASUDA_LIVE_TEST=1 go test ./live/`。実際の`masuda-sandbox serve`とVMが要る
- GitHub操作は`gh`を直接使わず`scripts/gh.sh`（`.env`のトークンを渡すラッパー。`.env`はClaudeから読めない）

## 契約の扱い

- `proto/masuda/api/v1/masuda.proto`と`docs/guest-protocol.md`は契約。**変えない**。sandbox.protoとengineのAPIは他リポジトリの所有で、こちらからも変えない
- 変えたくなったら`HANDOFF.md`の「契約への提案」に書いて止まる

## 作業の進め方

- 作業単位は`docs/work-orders.md`の1項目。1項目を1セッションで終える
- セッション開始時: `HANDOFF.md`→`docs/work-orders.md`の該当項目→契約の順に読む
- 探索はサブエージェントに出し、実装は自分で書く
- 旧実装から流用してよいもの: `internal/worktree`（`git clone --bare --local`、fast-forward）、`internal/perspectives`（14観点）、`internal/config`の宣言/承認の形。`git show v1-frozen-develop:<path>`で読む。それ以外の旧コードは`redesign`ブランチで削除済みで、復活させない
- コミットはユーザーの承認を得てから。メッセージは`<type>(<scope>): <summary>`に`## 意図`・`## 設計上の考慮点`・（あれば）`## 懸念事項`

## HANDOFF.md

セッション終了時に、次の見出しで**上書き**する（スキルは使わない。読むのはエージェント）。

```
# HANDOFF
## 作業項目
## 完了した契約テスト
## 未完と理由
## 次の一手
## 注意点
## 契約への提案
```

## コメント

コードのコメントは、10行以上の要約、他の選択肢がある中での選択理由、コードから読めない背景、トレードオフ、のいずれかを満たすものだけ書く。
