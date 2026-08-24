# 配布と自己更新

## バージョン識別子

masuda自身のバージョンはgitタグ（例: `v0.3.0`）を正とする。`cmd/masuda/main.go`の`var version = "dev"`が実体で、CIビルド時のみ`-ldflags "-X main.version=<タグ名>"`で書き換わる（`.github/workflows/release.yml:42`）。ローカルの`go build`/`make build`は`version`を渡さないため、常に`"dev"`のままビルドされる。この値は`masuda`コマンドの`--version`出力（cobraの`Version`フィールド）にそのまま使われるほか、`masuda update`が自分は最新かどうかを判定するキー、`masuda init`がどのReleaseを取得するかの選択キーとしても使われる。

## CI/CDリリースパイプライン

`.github/workflows/release.yml`は`v*`パターンのタグpushをトリガーに起動する。`linux/amd64`・`linux/arm64`のみが対象（macOS非対応）。

- `build`ジョブ（`:18-61`）: OS/アーキ別に`masuda_<goos>_<goarch>`をビルドし、`cosign sign-blob --yes --new-bundle-format --bundle masuda_<goos>_<goarch>.bundle`で署名する。CIワークフロー自身の`id-token: write`権限によるGitHub Actions OIDCを使ったkeyless署名で、署名鍵の生成・保管は発生しない。バイナリと`.bundle`の両方をartifactとしてアップロードする
- `reviews`ジョブ（`:68-88`）: `internal/perspectives/builtin/*.md`を`masuda_reviews.zip`にzip化し、同様に`masuda_reviews.zip.bundle`として署名する
- `release`ジョブ（`:89-107`、`build`・`reviews`に依存）: 両ジョブの成果物をGitHub Releaseへアップロードする。同じタグに対する再実行に備え、`gh release create`は既存Releaseがあればスキップし、`gh release upload --clobber`で上書きアップロードする
- `docker`ジョブ（`:113-135`、他ジョブと独立、`needs`なし）: リポジトリルート直下の`Dockerfile`（サンドボックスbaseイメージ）を`linux/amd64`・`linux/arm64`のマルチアーキでビルドし、Docker Hubの公開リポジトリ`tadahiroyamamura/masuda`へ`<タグ名>`と`latest`の2タグでpushする。このイメージはコンテナとして実行されることはなく、VMのrootfsへ変換する変換元としてのみ使われる（`docs/design/images-and-rootfs.md`参照）

このパイプラインが1回のタグpushで生成するGitHub Release資産は、CLIバイナリ×2アーキ＋その署名バンドル×2、`masuda_reviews.zip`＋その署名バンドルの、計6ファイル。

## 署名バンドルの命名規約と検証の要否

署名バンドルのファイル名は`<アセット名>.bundle`（`internal/selfupdate.BundleAssetName`）で、対象アセットと同じReleaseに同梱される。ダウンロード側（`masuda update`・`masuda init`）は次の2段階で扱いを分ける。

- **アセット自体（`masuda_reviews.zip`等）がReleaseに存在しない**: 警告を出して処理を継続する。pre-ADR-0038のReleaseや、reviewsジョブが失敗した回への配慮
- **アセットは存在するが対応する`.bundle`が無い、または検証に失敗する**: 常にハードフェイルする。「アセットが無いから警告」という緩和はここには適用されない。未検証のコンテンツを書き込む経路を残さないため

CLIバイナリの`.bundle`欠如・検証失敗は常にハードフェイル（警告緩和の対象外）。

## Sigstore署名検証（`internal/verify`）

`internal/verify`はcosign keyless署名（Sigstore）をFulcio/Rekorの信頼ルートに対して検証する。3ファイルで構成される。

- `trust.go`: `TrustedMaterial()`が`root.FetchTrustedRoot()`でSigstore公開TUFリポジトリ（`tuf-repo-cdn.sigstore.dev`）から信頼ルート（Fulcio CA証明書・CT log鍵・Rekor鍵）を都度取得する。masuda自身は信頼ルートを生成・保管・配布しない。TUF取得に失敗した場合はフォールバックせずエラーにする
- `identity.go`: `ExpectedIdentity(repo)`が、証明書のSAN（Subject Alternative Name）が次の正規表現に一致することを要求する証明書アイデンティティを組み立てる。

  ```
  ^https://github\.com/<repo>/\.github/workflows/release\.yml@refs/tags/.+$
  ```

  issuerは`https://token.actions.githubusercontent.com`固定。タグ部分（`refs/tags/.+`）はワイルドカードで、どのタグでの署名でも許可する
- `blob.go`: `VerifyBlob(artifact, bundleJSON, identity, trusted)`がバンドルをパースし、`sigverify.NewVerifier`を`WithTransparencyLog(1)`・`WithObserverTimestamps(1)`で構築して検証する。Rekor transparency log inclusionを必須とする一方、`WithSignedCertificateTimestamps`（証明書埋め込みのCT SCT）は要求しない——テストフィクスチャ（`VirtualSigstore`）がSCTを生成しないことと、Rekor inclusionが実質的に同等の「公開ログに記録された」という保証を既に与えていることが理由

呼び出し側（`cmd/masuda/update.go`の`verifiedBundleFunc`）は、ダウンロード済みバイナリ/zipのバイト列と署名バンドルのバイト列、`ExpectedIdentity`・`TrustedMaterial`の結果を`VerifyBlob`に渡すクロージャを`internal/selfupdate.DownloadAndReplace`・`SyncReviews`の`verify`引数として渡す。検証は実際のファイル書き込み（`os.Rename`・zip展開）の直前、ダウンロード完了後に呼ばれる。検証失敗時は書き込み前に中断するため、`execPath`・`reviewsDir`が未検証の内容で汚染されることはない。

## 初期化CLI（`masuda init`）

`cmd/masuda/init.go`が実装する。対象リポジトリの`.masuda/`ディレクトリに以下の3点を一度きり展開する。

1. `settings.json`: `image`（常に`config.DefaultImageEntry`＝`"default"`。フラグは無い）・`base`（`--base`）・`claudeSettings`（固定のデフォルト値、`defaultClaudeSettings`）を書き込む
2. `reviews/`: 内蔵14観点のzipを取得・検証して展開する（後述「レビュー観点の配布・同期」）
3. `Dockerfile`: `FROM tadahiroyamamura/masuda:<タグ>`を書いたテンプレート

`.masuda/`が既に存在する場合はエラーで停止し、再実行を拒否する（マージ・同期はしない一度きりの操作）。

**取得するReleaseの選び方**: 実行バイナリの`version`が`"dev"`でなければ、`FetchReleaseByTag(apiBase, repo, version)`で**自分自身のバージョンに一致するタグ**のReleaseを取得する。`version == "dev"`（ローカルビルド）の場合のみ`FetchLatestRelease`にフォールバックする。この選び方により、イメージエントリのFROMタグと展開される観点セットは常にその`masuda init`を実行したCLIバイナリ自身のバージョンと対応する。

`masuda init`の実行には**ネットワーク接続が必須**。reviews資産の取得に署名バンドルが伴わない場合はハードフェイルする（アセット自体が無い場合の警告緩和は`masuda init`には適用されない——`init`は`reviewsAsset`が見つからない時点でエラーを返す）。

## 自己更新CLI（`masuda update`）

`cmd/masuda/update.go`が実装する。3ステップを順に実行する。

1. **CLIバイナリの更新**（`updateBinary`）: `FetchLatestRelease`で最新Releaseを取得し、`release.TagName == version`なら何もせず終了する。異なれば実行中OS/アーキ用アセットと対応する`.bundle`をダウンロードし、`internal/selfupdate.DownloadAndReplace`で検証・置換する。**`.bundle`が見つからない場合はバイナリ置換自体を拒否する**（ハードフェイル、警告緩和なし）。置換は同一ディレクトリへの一時ファイル書き込み＋`os.Rename`によるatomic置換で、`os.Executable()`（シンボリックリンクなら`EvalSymlinks`で解決）が指すパスを直接上書きする
2. **イメージエントリの再ビルド**（`refreshProjectImages`）: 現在のカレントディレクトリが対象プロジェクトのチェックアウト内で、かつ`.masuda/images/`にエントリがある場合のみ動く。無ければ何もしない（ADR-0054以前に作られたプロジェクトはこのディレクトリを持たない）。**宣言された全エントリ**が対象で、`settings.json`の`image`が指す1つに限らない——特権コマンド用のイメージはトップレベルの`image`が指さないエントリだが、そのコマンドを実行する前にビルド済みである必要がある。各エントリのFROM行のタグを最新Releaseの`TagName`へ書き換えてから`docker build --pull`し、`config.ImageTag`が導出するタグとしてtagする
3. **レビュー観点の同期**（`syncProjectReviews`）: `.masuda/reviews/`が存在する場合のみ動く。最新Releaseの`masuda_reviews.zip`を取得・検証し、`.masuda/reviews/`に**存在しないファイルだけ**追加する

いずれのステップも対象は`repoRoot()`（`masuda`コマンドを実行しているカレントディレクトリのチェックアウト）だが、ステップ1（CLIバイナリ置換）だけは`repoRoot()`を使わない——masuda自身の実行ファイルを更新する操作であり、対象プロジェクトのリポジトリとは無関係のため。

**稼働中ワークスペースが1つでもあれば、`masuda update`は3ステップとも一切実行せず拒否する**（`selfupdate.BlockingWorkspaces`が`internal/workspace.ListAll()`の全ワークスペースを対象repoを問わず横断的に見て、ホストループ実行中（`hostloop.IsRunning`）またはサンドボックス起動中（`sandboxBackend.IsRunning`）のものを検出する）。警告して続行する経路はない。CLIバイナリが機械全体で共有される単一の実行ファイルであるため。

## イメージエントリのFROM行

`masuda init`が書き出すテンプレート（`dockerfileTemplate`、`cmd/masuda/init.go`）は次の1行を含む。

```
FROM tadahiroyamamura/masuda:<タグ>
```

このタグは常に固定のバージョンタグで、`latest`のようなfloatingタグは使わない——同じDockerfileを変更せず2回`docker build`した結果が一致することを保つため。

`masuda update`の`internal/selfupdate.UpdateDockerfileFromTag`は、`FROM tadahiroyamamura/masuda:\S+`にマッチする行を正規表現で検出し、最新Releaseのタグへ書き換える。**マッチする行が無ければ何もしない**——ユーザーが独自のベースイメージへ差し替えている場合も、masudaのbaseから派生しないエントリ（`docker`テンプレート由来の`FROM ubuntu:24.04`など）も、この条件で自然に対象外になる。書き換え後、`docker build --pull -t <config.ImageTag(repoRoot, entry)> -f <Dockerfile> <repoRoot>`を実行する。

## レビュー観点の配布・同期

内蔵レビュー観点のソースは`internal/perspectives/builtin/*.md`だが、CLIバイナリに`go:embed`されることはない。`internal/perspectives/perspectives.go`が持つのは`ReviewsDir(repoRoot)`（`<repoRoot>/.masuda/reviews`を返すパスヘルパー）のみで、観点データそのものは持たない。配布経路はCIの`reviews`ジョブが生成する`masuda_reviews.zip`（Release資産）を都度ダウンロードする方式に統一されている。

`internal/selfupdate.SyncReviews(url, reviewsDir, verify)`が同期の実体。

1. `url`からzipをダウンロードする
2. `verify`（署名検証クロージャ）でzip全体を検証する。失敗すれば`reviewsDir`には一切触れない
3. zipの各エントリについて、`reviewsDir`に同名ファイルが**既に存在すれば何もしない**（上書きしない）。存在しなければ書き込む
4. 実際に新規追加したファイル名のリストを返す

この「追加のみ・上書きなし・削除なし」という規則により、ユーザーがカスタマイズした観点ファイルや、frontmatterの`enable: false`で無効化した観点ファイル（無効化はファイル削除ではなくこのフラグで表現する）は、`masuda update`を何度実行しても変化しない。同期は毎回冪等で、バージョンを跨いだ実行順序の管理を必要としない。

`masuda init`と`masuda update`はどちらも同じ`SyncReviews`を呼ぶが、投入するzipのURL（＝参照するReleaseのタグ）が異なる（前掲「初期化CLI」「自己更新CLI」の各節を参照）。

## 関連ADR

- [ADR-0024](../adr/0024-file-based-perspectives-mechanical-checker-prompt.md): `.masuda/`展開の基本パターン（一度きりmaterialize、以後はユーザー資産）
- [ADR-0032](../adr/0032-masuda-self-update-via-github-release-and-dockerhub-pull.md): GitHub ReleaseとDocker Hubによる自己更新の全体設計
- [ADR-0033](../adr/0033-perspective-enable-flag-and-release-asset-sync.md): `enable`フラグとReleaseアセット同期
- [ADR-0038](../adr/0038-cosign-keyless-blob-verification-for-self-update.md): cosign keyless検証
