# リリース手順

masudaは3つのリポジトリ（masuda・[masuda-engine](https://github.com/TadahiroYamamura/masuda-engine)・[masuda-sandbox](https://github.com/TadahiroYamamura/masuda-sandbox)）からなり、**3つに同じタグ`vX.Y.Z`を打って**1つのリリースにする。利用者はmasudaとmasuda-sandboxを同じバージョンで組にして入れる（[導入](../user/install.md)）。

- 順序は**engine→sandbox→masuda**。masudaのリリースは、`go.mod`が同じタグのengineを要求していること、同じタグのmasuda-sandboxの`sandbox.proto`と契約（SHA-256）が合うことをワークフローで確かめるので、先の2つが打たれていないと失敗する
- 配布物はGitHub Releaseの添付物だけ。npmレジストリには公開しない（v1.0まで）。互換性の保証もv1.0から
- macOS（darwin/arm64）は「実験的」。ビルドして添付するが、実機での確認はしていない

| リポジトリ | タグで動くもの | Releaseの添付物 |
|---|---|---|
| masuda-engine | 無し（Goのモジュールとしてタグを引くだけ） | — |
| masuda-sandbox | `.github/workflows/release.yml`（手順は向こうの`docs/release.md`） | `masuda-sandbox-X.Y.Z.tgz`、`SHA256SUMS` |
| masuda | `.github/workflows/release.yml` | `masuda_X.Y.Z_linux_amd64.tar.gz`、`masuda_X.Y.Z_darwin_arm64.tar.gz`、`masuda-api-client-X.Y.Z.tgz`（`clients/ts`の`npm pack`）、`SHA256SUMS` |

masudaの`release.yml`は、`go vet`・`go test ./...`、`go.mod`のengineの版がタグと同じか、同じタグのmasuda-sandboxで`internal/sandboxcontract/sha.go`を生成し直して差分が無いかを確かめてから、`-ldflags "-X main.version=X.Y.Z"`でクロスビルドする。リリースノートには3リポジトリのタグとコミットハッシュの表を自動で書く。

## 1. 打つ前の確認

タグを打つコミットで、3リポジトリとも次が通っていること。

- masuda: `go build ./... && go vet ./... && go test -count=1 ./...`（契約テストC-M*を含む）。`go.work`があれば`GOWORK=off`を付けて、`go.mod`で固定した版で確かめる
- masuda-engine: `go test -count=1 ./...`（契約テストを含む）
- masuda-sandbox: 単体テストと契約テスト（実VMが要る。向こうの`docs/release.md`「タグを打つ前に」）
- 実機の1周: `masuda-sandbox serve`を起動し、`MASUDA_LIVE_TEST=1 go test -count=1 -timeout 60m -v ./live/`が完走する（前提は`live/live_test.go`の冒頭）
- `go generate ./internal/sandboxcontract/`で差分が出ない（隣の`../masuda-sandbox`がタグを打つコミットにあること）

## 2. 初回だけ: `redesign`を`develop`にする

再設計は`redesign`ブランチで進めた。最初のリリースの前に、これを`develop`にする。旧`develop`はタグ`v1-frozen-develop`で残してある。

```sh
git fetch origin
git rev-parse v1-frozen-develop origin/develop   # 同じコミットであること（違えば旧developに未凍結の変更がある）
git push origin v1-frozen-develop v1-frozen-workflow-engine v1-frozen-sandbox-independence
git branch -f develop redesign
git push -f origin develop
```

`v1-frozen-*`の3つのタグは、旧実装（`develop`・`feat/workflow-engine`・`feat/sandbox-independence`）を読むためのもの。push後は旧ブランチを消してよい。

## 3. `main`を`develop`に合わせる

3リポジトリとも、リリースは`main`の先頭に打つ。

```sh
git switch main && git pull --ff-only   # masudaに初めてmainを作るときは git switch -c main develop
git merge --ff-only develop
git push origin main
```

engine・sandboxは`main`だけで開発しているので、この手順は要らない（`git switch main && git pull`で先頭にいることだけ確かめる）。

## 4. タグを打つ（engine→sandbox→masuda）

### masuda-engine

```sh
cd ../masuda-engine
git switch main && git pull --ff-only
git tag -a vX.Y.Z -m "masuda-engine vX.Y.Z"
git push origin vX.Y.Z
```

### masuda-sandbox

向こうの`docs/release.md`のとおり。タグのpushで`release.yml`が走り、tgzがReleaseに添付される。

```sh
cd ../masuda-sandbox
git switch main && git pull --ff-only
git tag -a vX.Y.Z -m "masuda-sandbox vX.Y.Z"
git push origin vX.Y.Z
```

### masuda {#engine}

まず`go.mod`のengineを、いま打ったタグに上げる。

```sh
cd ../masuda
GOWORK=off go get github.com/TadahiroYamamura/masuda-engine@vX.Y.Z
GOWORK=off go mod tidy
GOWORK=off go test -count=1 ./...
go generate ./internal/sandboxcontract/ && git diff --exit-code -- internal/sandboxcontract/
git commit -am "chore(deps): masuda-engineをvX.Y.Zに上げる"
git push origin develop
```

- 開発中は`go.mod`がengineの`main`の擬似バージョン（`v0.0.0-<日時>-<コミット>`）を要求していることがある。`go get ...@main`でも上げられるが、リリースでは必ずタグにする（`release.yml`がタグと違えば止まる）
- タグを打った直後はモジュールプロキシ（proxy.golang.org）がまだ知らないことがある。`go get`が`unknown revision`で失敗したら、少し待つか`GOPROXY=direct`を付ける

`main`を`develop`に合わせ（手順3）、タグを打つ。

```sh
git switch main && git merge --ff-only develop && git push origin main
git tag -a vX.Y.Z -m "masuda vX.Y.Z"
git push origin vX.Y.Z
```

`release.yml`が終わったら、Releaseのノートの表（masuda・masuda-engine・masuda-sandboxのタグとコミット）が、手順4で打った3つのコミットと一致することを確かめる。

## 5. ドキュメントサイト

タグのpushで`docs.yml`が、サイトを`X.Y`として公開し`latest`の別名を付ける（mike）。<https://tadahiroyamamura.github.io/masuda/>でバージョンの選択に`X.Y`が出て、既定が`latest`になっていることを確かめる。

`docs.yml`は`masuda-engine`の既定ブランチ（`main`）から`workflow-schema.md`を取り込む。手順4の時点でengineの`main`の先頭がタグと同じなら、タグのものと一致する。

## 6. 添付物で導入をなぞる

Releaseの添付物だけを使って、[導入](../user/install.md)を頭から1回なぞる（別のマシンかユーザーが望ましい）。少なくとも次を確かめる。

- `sha256sum -c SHA256SUMS`が通る（masuda・masuda-sandboxの両方）
- `masuda version`が`X.Y.Z`と`contract: ok`を出す（`masuda-sandbox serve`を起動した状態で）
- `masuda doctor`がすべて`ok`（Nodeの版によってはwarn）
- [はじめての1周](../user/quickstart.md)が通る
