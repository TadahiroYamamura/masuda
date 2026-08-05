# ADR-0033: 観点の無効化はfrontmatterの`enable`フィールドで表現し、`masuda update`はGitHub Releaseアセットから新規組み込み観点を追加のみで同期する

## Status

Accepted (2026-08-05)

## Context

GitHub Issue #8は、masuda自体に新しい組み込み観点が追加されたとき、既に`masuda init`済みのプロジェクトへ後から取り込む手段が無いという課題を扱う。[[0032-masuda-self-update-via-github-release-and-dockerhub-pull]]でmasuda自身のバージョン識別・`masuda update`という更新導線が確立したことで、Issue #8に着手できる前提が整った。

Issue #8には当初、以下の論点が未解決として残っていた。

1. 新規追加観点の「追加」と、ユーザーが意図的に削除した観点の「復活」をどう区別するか
2. ユーザーがカスタマイズ済みの観点ファイルと、masuda同梱の未編集ファイルをどう区別するか
3. 差分検知の仕組み・コマンド名
4. 複数バージョンのmasudaを跨いで運用しているプロジェクトでの取り込みタイミング

調査の結果、「観点の無効化はファイル削除のみで表現する」という現行の運用は、[[0024-file-based-perspectives-mechanical-checker-prompt]]が正式に決定した事項ではなく、`docs/INSTALLATION.md`に「1観点1ファイル」の副次的な帰結として素朴に書かれていただけだと判明した（ADR-0024自体の論点は移行パス・checker_prompt自動生成・ID方式の3つのみ）。この副次的な帰結を見直すことで、論点1・2・4が一挙に解消できることが分かった。

また実装方式の検討の過程で、masuda内蔵観点を`go:embed`でCLIバイナリに焼き込む現行方式（ADR-0024）に技術的な問題があることが判明した。`masuda update`がバイナリファイルを新しいものに差し替えても、その差し替えを実行している**プロセス自体**は旧バイナリのままメモリ上で動き続ける（Unixの`rename`は実行中プロセスのコードを書き換えない）。そのため同一プロセス内で観点同期を行おうとすると、旧バイナリに埋め込まれた「更新前の観点セット」しか参照できず、当該リリースで追加された新規観点の反映に1回分のラグが生じる。[[0032-masuda-self-update-via-github-release-and-dockerhub-pull]]で確立した「GitHub Releaseアセットをダウンロードする」という配布方式をここにも適用すれば、この問題は起きない。

## Decision

### 観点の無効化: frontmatterの`enable`フィールド

観点ファイル（`.masuda/reviews/*.md`）のfrontmatterに`enable`（真偽値、省略時`true`）を追加する。`orchestrator/implement_review_graph.py`の`_parse_perspective_file()`が読み取り、`_load_perspectives()`が`enable: false`の観点をフェーズ5の対象から除外する（`PERSPECTIVES`/`PERSPECTIVE_IDS`/`TOTAL_PERSPECTIVES`/`TRIGGERED_PERSPECTIVE_IDS`すべてに反映される）。ファイル自体は削除せず残る。全観点が無効化された場合、`_detect_review_phase()`は「ディレクトリが無い/空」の場合と同じ`FileNotFoundError`を、無効化のケースも含めた文言で発生させる。

`docs/INSTALLATION.md`の案内も「不要な観点はファイルを削除する」から「不要な観点は`enable: false`を設定する」へ更新し、ファイル削除は無効化の手段として案内しない（プロジェクト固有に追加した観点自体を完全に消したい場合は引き続きファイル削除で行えるが、それは「無効化」ではなく「削除」として別に扱う）。

### 新規観点の同期: `masuda update`による追加のみの反映

`masuda update`は、対象プロジェクトの`.masuda/reviews/`に**存在しない**builtin観点だけを追加する。既存ファイルには一切触れない（上書きしない・削除しない）。この単純な規則により:

- `enable: false`で無効化した観点は常にファイルとして存在し続けるため、「ファイルが無い」＝「まだ導入されていない新規観点」と確実に判定できる（論点1の解消）
- 既存ファイルを一切上書きしないため、ユーザーがカスタマイズ済みかどうかを判定する必要が無い（論点2の解消）
- 同期は「今何が足りないか」を毎回突き合わせるだけなので、バージョンを跨いだタイミング管理が不要（冪等。論点4の解消）

`masuda update`は`.masuda/reviews/`が存在する（＝`masuda init`済みの）プロジェクトでのみこの同期を行う。リリースに観点アセットが無い場合はコマンド全体を失敗させず、警告を出して継続する（CLIバイナリ・Dockerfile更新は既に成功している可能性があるため）。

### 組み込み観点の配布方式: `go:embed`を廃止しGitHub Releaseアセットへ統一

`internal/perspectives/builtin/*.md`はCLIバイナリへの`go:embed`をやめる。CIの`release.yml`に新設した`reviews`ジョブが同ディレクトリをzip化し、`masuda_reviews.zip`としてGitHub Releaseへ添付する（`build`・`docker`ジョブと並列、`release`ジョブの`needs`に追加するだけで既存の`gh release upload dist/*`がそのまま拾う）。

`internal/selfupdate`に以下を追加する。

- `FetchReleaseByTag(apiBase, repo, tag)`: `FetchLatestRelease`と共通の`fetchRelease`ヘルパーを使い、特定タグのReleaseを取得する
- `ReviewsAssetName`（`"masuda_reviews.zip"`）
- `SyncReviews(url, reviewsDir)`: zipをダウンロードして展開し、`reviewsDir`に存在しないファイルだけを書き込む（追加のみ原則の実装本体）

**`masuda init`もこの方式に統一する**（ユーザー確認済み）。`masuda init`は自身の`version`（ldflags埋め込み、ADR-0032）に対応するReleaseを`FetchReleaseByTag`で取得し（ローカルビルドで`version == "dev"`の場合は`FetchLatestRelease`にフォールバック）、その`SyncReviews`で観点を展開する。さらに`.masuda/Dockerfile`のFROMタグ（ADR-0032）も同じReleaseの`TagName`を使うことで、「`dockerTag == "dev"`なら文字列`"latest"`にフォールバックする」という以前の特別扱いが不要になった——`masuda init`は既にネットワークへ問い合わせる前提になったため、常に実在するタグを書ける。

この結果、**`masuda init`は今後ネットワーク接続が必須になる**（従来はオフラインで完結していた）。現時点でmasudaのユーザーは開発者本人のみのため、後方互換性は考慮していない。

## Alternatives Considered

- **バイナリ差し替え直後に自分自身をre-execして新バイナリで観点同期を行う**: `syscall.Exec`で正確性は確保できるが、無限ループ防止フラグなど実装が複雑化する。GitHub Releaseアセットに観点を分離配布すれば、re-execなしで同じ正確性が得られるため不採用。
- **`masuda init`は`go:embed`のまま残し、`masuda update`の同期だけをRelease資産化する**: 実装は1つの配布経路（`go:embed`と`SyncReviews`の二重管理）よりシンプルになるが、`masuda init`と`masuda update`が異なる観点セットを参照しうる状態が生まれる。統一する方が構成が単純になるためこちらを採用した。
- **既存プロジェクトの後方互換性（`enable`フィールド導入前にファイル削除で無効化済みの観点を復活させない対応）を作り込む**: 現時点でmasudaのユーザーは開発者本人のみであり、既存プロジェクトへの移行コストは自分で吸収できるため、そのための特別な仕組みは作らなかった。
- **観点の同期に失敗したら`masuda update`全体を失敗扱いにする**: バイナリ・Dockerfileの更新は既に成功している可能性があり、観点アセット欠如のような副次的な失敗でコマンド全体を失敗として扱うのは過剰と判断し、警告を出して継続する設計にした。

## Consequences

- `masuda init`はオフラインで完結しなくなる。ネットワーク接続が無い環境では実行できない
- `internal/perspectives`パッケージは`ReviewsDir`パスヘルパーのみを持つ薄いパッケージになった。`builtin/*.md`はソースとして残るが、CIがzip化する入力としての役割のみを持ち、Goコードから読まれることはなくなった
- `.github/workflows/release.yml`に`reviews`ジョブが増え、CIの管理対象が1つ増えた
- 既に`enable`フィールド導入前にファイル削除で観点を無効化しているプロジェクトがあった場合、その観点は「まだ導入されていない新規観点」として`masuda update`実行時に復活する（Alternatives Consideredの通り、現状唯一のユーザーであるため許容している）
- GitHub Issue #8はこのADRで解決される
