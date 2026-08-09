# ADR-0038: `masuda update`/`masuda init`が取得するCLIバイナリ・reviewsアセットをcosign keyless署名で検証する

## Status

Accepted (2026-08-09)

## Context

GitHub Issue #18は、[[0032-masuda-self-update-via-github-release-and-dockerhub-pull]]が確立した`masuda update`の取得経路（GitHub Releasesの未認証REST API・HTTPSダウンロード）に、ダウンロードしたCLIバイナリ・reviewsアセットの正当性検証（チェックサム・署名）が一切ないことを指摘した。masudaは`--dangerously-skip-permissions`で無人実行されるツールであり、`masuda update`は自分自身の実行ファイルを`os.Rename`でその場に置き換える（ADR-0032）。GitHubリポジトリが侵害された場合、次に`masuda update`を実行した全ユーザーが無条件に悪意あるバイナリを取り込むことになる。

Issueは検証鍵自体の信頼起点をどこに置くかという「鶏と卵」問題も懸念していた——masuda自身が検証用の鍵ペアを生成・保管・配布する方式では、その鍵自体をどう検証するかという同じ問題が一段ずれて再発するだけになる。

Dockerベースイメージ（Docker Hub公開リポジトリ`tadahiroyamamura/masuda`）側の検証は、実装コストの質が異なる（後述）ため別Issue #32として切り出し、本ADRのスコープ外とした。

## Decision

### 検証方式: cosign keyless署名（Sigstore、GitHub Actions OIDC identity）

CIの`release.yml`ワークフローが、`id-token: write`権限で取得したGitHub Actions自身のOIDCトークンを使い、`sigstore/cosign-installer`で導入した`cosign sign-blob --yes --new-bundle-format --bundle`で各アセット（CLIバイナリ・`masuda_reviews.zip`）を署名する。鍵ペアの生成・GitHub Actions Secretsへの保管は一切発生しない（keyless）。署名はSigstoreのFulcio CAが発行する短命証明書と、Rekor透明性ログへの記録によって成立する。

masuda側（`internal/verify`、新規パッケージ）は、この証明書がmasuda自身の`release.yml`ワークフロー・任意のタグに対して発行されたものであることを、以下の正規表現でSAN（Subject Alternative Name）を検証する（`ExpectedIdentity`）:

```
^https://github\.com/TadahiroYamamura/masuda/\.github/workflows/release\.yml@refs/tags/.+$
```

issuerは`https://token.actions.githubusercontent.com`固定。信頼ルート（Fulcio CA証明書・CT log鍵・Rekor鍵）は`internal/verify.TrustedMaterial`が`sigstore-go`の`root.FetchTrustedRoot()`経由でSigstore公開TUFリポジトリから都度取得する。masuda自身はこの信頼ルートを生成も保管も配布もしない——Sigstoreの公開Fulcio CAそのものが信頼の起点であり、これがIssue #18の「鶏と卵」問題への回答になる。

### 検証プロパティ: Rekor transparency log inclusionを必須、embedded SCTは不要

`internal/verify.VerifyBlob`（`verifyEntity`経由）は`sigstore-go`の`verify.NewVerifier`を`WithTransparencyLog(1)`・`WithObserverTimestamps(1)`で構築する。`WithSignedCertificateTimestamps`（証明書に埋め込まれたCertificate Transparency SCTの検証）は意図的に要求しない。理由は二つある: (1) masuda自身のテストフィクスチャ（`sigstore-go`の`pkg/testing/ca.VirtualSigstore`）が生成する証明書にはSCTが埋め込まれず、要求すると自前のテストが書けなくなる、(2) Rekor transparency logへの記録は「この署名が公開ログに記録された」という、SCT検証が証明しようとしている性質と実質的に同じ保証を既に与えている。

### 失敗時の挙動: 常にハードフェイル

`masuda update`のCLIバイナリ置き換え・reviews同期、`masuda init`のreviews初期展開のいずれも、対応する署名バンドル（`<asset>.bundle`という命名、`internal/selfupdate.BundleAssetName`）が存在しない、または検証に失敗した場合は処理全体を中断する。既存の「reviewsアセット自体が丸ごと無い場合は警告して継続」という後方互換パス（ADR-0033由来、pre-0038リリースを指す場合の緩和）は残すが、アセットが存在するのにバンドルが無い・検証に失敗するケースには適用しない——黙って未検証のコンテンツを取り込む経路を残さないため。`internal/selfupdate.DownloadAndReplace`・`SyncReviews`は`verify func([]byte) error`引数を取り、ダウンロード完了後・実際の書き込み（`os.Rename`・zip展開）前にこれを呼ぶ。検証失敗時は既存の一時ファイルクリーンアップ経路がそのまま働き、`execPath`・`reviewsDir`は一切変更されない。

## Alternatives Considered

- **GPG鍵による署名**: masuda自身が署名鍵ペアを生成し、GitHub Actions Secretsに秘密鍵を保管し、公開鍵をCLIに埋め込んで検証する方式。鍵の生成・保管・ローテーション・失効という運用が新たに発生し、その鍵自体の正当性をどう保証するかという、Issue #18が問題視した「鶏と卵」を形を変えて再導入するだけになるため却下した。
- **チェックサム（SHA256SUMS等）のみ**: ダウンロード経路の完全性（転送中の破損検知）は証明できるが、そのチェックサムファイル自体もGitHub Releasesの同じアセット群として配布されるため、GitHubリポジトリが侵害された場合はチェックサムファイルごと差し替えられ、正当性の証明にならない。
- **インストール済みcosign CLIへのシェルアウト**（`cosign verify-blob`）: go.modへの追加依存もバイナリサイズの増加も回避できるが、ADR-0032が明示的に避けた「`gh` CLIのような外部ツールへの依存」を新たに持ち込むことになり、`masuda update`を使う全ユーザーに事前の`cosign`インストールを強制する。ユーザーと協議した結果、後述のバイナリサイズ増加を受け入れる方を選んだ。
- **`github.com/sigstore/cosign/v2`をGoライブラリとして直接import**: OCI署名検証・blob検証のどちらも実装済みで検証済みのコードを再利用できるが、cosignのCLI機能（KMSバックエンドでの署名、`policy-controller`向けのOPA/cuelangベースのポリシー評価等、masudaが必要としない機能）向けの依存が`sigstore-go`よりさらに重く乗る。`sigstore-go`は`pkg/verify`・`pkg/bundle`・`pkg/root`・`pkg/tuf`という検証に必要な部分だけを提供する。

## Consequences

- `go.mod`/`go.sum`の依存モジュール数が3（`spf13/cobra`関連のみ）から368へ増加した。実測で確認した内訳としては、AWS/Azure/GCP/HashiCorp Vault等のKMS SDK・PKCS11ライブラリは`sigstore-go`の`pkg/root`が参照する`timestamp-authority/v2/pkg/verification`の依存グラフ経由で`go.sum`の解決対象には現れるが、masudaは実際にはそれらのコードパスを一切呼ばず、`go tool nm`でのシンボル検索でも実行バイナリには含まれていないことを確認した。実際にリンクされ配布バイナリのサイズに寄与するのは、gRPC・OpenTelemetry・protobuf・certificate-transparency-go・Rekorクライアントのコードで、これによりmasudaの配布バイナリは約13.1MBから約26MBへとほぼ倍増した。この増分はFulcio証明書検証・Rekor透明性ログ照会・TUF信頼ルート取得というプロトコルを正しく実装する上での本質的コストであり、`cosign/v2`をライブラリとして採用しても同程度発生すると判断し、外部ツールへの依存を増やすよりはこちらを受け入れることにした。
- `masuda update`・`masuda init`は、GitHub API・Docker Hubに加えてSigstoreの公開TUF CDN（`tuf-repo-cdn.sigstore.dev`）への到達性にも新たに依存するようになった。TUFフェッチに失敗した場合はフォールバックせずエラーとする（`internal/verify.TrustedMaterial`）。
- CI（`.github/workflows/release.yml`）の`build`・`reviews`ジョブに`sigstore/cosign-installer`ステップと署名ステップが追加され、ビルド時間がわずかに伸びる。新規のGitHub Actions Secretsは不要（keyless）。
- pre-0038のリリース（署名バンドルアセットを持たない過去のタグ）に対しては、`masuda update`の対象バージョンとしては原理上は到達しうるが、実運用では「最新リリースの取得」（`FetchLatestRelease`）が前提のため通常は起こらない。`masuda init`（`FetchReleaseByTag`でCLI自身のバージョンにピン留めする経路）でpre-0038バイナリ自身がpre-0038タグを指定した場合は、そのタグにバンドルが無いため`masuda init`はエラーで停止する——これは意図した挙動であり、そのようなCLIは`masuda update`で0038以降のバージョンに上げてから`masuda init`を使う必要がある。
- Dockerベースイメージの署名検証・`.masuda/Dockerfile`のFROM行のverified digest pin化は本ADRのスコープ外（GitHub Issue #32）。
