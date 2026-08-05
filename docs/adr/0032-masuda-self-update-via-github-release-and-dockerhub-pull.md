# ADR-0032: `masuda update`はGitHub Releaseのバイナリダウンロードと公開Docker Hub baseイメージで自己更新する（masuda自身のソースからの再ビルドはしない）

## Status

Accepted (2026-08-05)

## Context

GitHub Issue #8（組み込みレビュー観点を、既にinit済みのプロジェクトへ後から取り込む手段）の検討中に、そもそもmasuda自体に「バージョンを識別し、最新化する」手段が一切存在しないことが判明した。masudaには`--version`フラグもなく、`go.mod`はGoツールチェインバージョンのみでモジュール自体のセマンティックバージョンを持たず、gitタグ運用もない。現状の運用実態は、開発者自身が`git clone`→`make build`/`make install`（バイナリを`/usr/local/bin`へ配置）→`make docker-images`（ローカル`docker build`、レジストリなし）で完結させる手動プロセスであり、READMEもほぼ空で公開配布の仕組みは存在しない。

観点の追加・更新を既存プロジェクトへ反映する（Issue #8）ことは、実質的に「masudaのバージョンアップに追従する」作業の一部である。バージョンを識別し最新化する仕組みが無ければ、「新しい組み込み観点が増えた」こと自体を検知する手段もない。そのため、Issue #8に着手する前提としてIssue #17（本ADRが解決するIssue）を先に切り出し、独立に解決することにした。

なお`cmd/masuda/main.go`の`repoRoot()`（`git rev-parse --show-toplevel`）は「`masuda`コマンドを実行しているカレントディレクトリのgit checkout」を返す関数だが、これは`workspace create`等が対象とする**操作対象プロジェクト**のチェックアウトであり、masuda自身のソースツリーとは無関係である。本ADRが導入する`masuda update`は、この`repoRoot()`とは独立した設計にする必要がある——同じ関数を誤って転用すると、「masudaを更新する」つもりが「今操作しているプロジェクトのリポジトリ」に対して動いてしまう。

[[0015-native-lsp-plugins-and-repo-declared-image]]は「使用するDockerイメージの選定は対象repo側の責任」という原則を既に確立している。本ADRはこの原則を維持したまま、masuda自身が提供するbaseイメージ（現行の`masuda-loop:latest`相当）をバージョン管理・配布可能にする話であり、対象repo側の裁量を制限するものではない。

[[0024-file-based-perspectives-mechanical-checker-prompt]]が確立した「`masuda init`が内蔵コンテンツを対象repoへ一度きり実体化する」パターン（`.masuda/reviews/`）と、[[0031-claude-settings-field-init-materialized-no-implicit-default]]が同パターンを`.masuda/settings.json`の`claudeSettings`フィールドに適用した先例は、本ADRが新設する`.masuda/Dockerfile`にもそのまま踏襲する。

## Decision

### バージョン識別子: gitタグ

masuda自身のバージョンはgitタグ（例: `v0.3.0`、セマンティックバージョニング）を正とする。`cmd/masuda`のビルド時に`ldflags`でバージョン文字列をバイナリへ埋め込む。**ソースツリーの絶対パスは埋め込まない** — バージョンだけを持たせれば、`masuda update`はソースツリーの所在に依存せず動作できる。

### CLIバイナリの配布: GitHub Actions → GitHub Releases

gitタグのpushをトリガーにGitHub Actionsが起動し、`cmd/masuda`をOS・アーキテクチャ別にビルドしてGitHub Releasesへartifactとして登録する。masuda本体のリポジトリは公開のため、Releaseアセットは認証なしでHTTPS経由で直接ダウンロードできる。`masuda update`はこれを`gh` CLIのような追加の外部ツールに依存せず、標準のHTTPSクライアントで完結させる。

### `masuda update`のCLIバイナリ更新

実行中のバイナリに埋め込まれたバージョンと、GitHubの公開REST API（`/repos/<owner>/<repo>/releases/latest`、未認証で利用可能）から取得した最新リリースタグを比較し、新しければ対応OS/アーキテクチャのアセットをダウンロードして自分自身の実行ファイルを差し替える（同一ディレクトリへの一時ファイルダウンロード後、`os.Rename`によるatomic置換）。

### Dockerベースイメージの配布: 同じCI → Docker Hub 公開リポジトリ

同じGitHub Actionsワークフローが、リポジトリルート直下の`Dockerfile`（base、`masuda-loop:latest`相当）をビルドし、Docker Hubの**公開（public）**リポジトリ`tadahiroyamamura/masuda`へプッシュする。`masuda update`・`.masuda/Dockerfile`雛形のどちらからも認証なしの単純な`docker pull`/`FROM`だけで参照できるようにするため公開リポジトリとする。公開対象は**baseイメージのみ**——既存の`docker/{go,python,typescript,full}`という同梱の言語別バリアントは今回のCI公開対象に含めない（今後の検討事項として後述）。CIはGitHub Actionsのamd64ランナー上で`docker/setup-qemu-action`によるarm64エミュレーションと`docker buildx`を使い、CLIバイナリと同じlinux/amd64・linux/arm64のマルチアーキでビルド・pushする。

### `.masuda/Dockerfile`の新設: `masuda init`が雛形を実体化する

`masuda init`は、既存の`.masuda/settings.json`・`.masuda/reviews/`に加えて`.masuda/Dockerfile`の初動雛形を対象repoへ書き出す（[[0024-file-based-perspectives-mechanical-checker-prompt]]と同じmaterialize方式——一度きり展開し、以後はユーザーが自由に編集する対象repo側の資産になる）。雛形の`FROM`行はDocker Hub上の公開baseイメージを、`masuda init`実行時点のCLI自身のバージョン（`cmd/masuda`にldflags埋め込み済みの値。ローカルビルドで未設定＝`dev`の場合は`latest`にフォールバック）へ**固定**参照する（例: `FROM tadahiroyamamura/masuda:v0.3.0`）。floatingタグ（`latest`）に依存しない——同じ`.masuda/Dockerfile`を変更せず2回`docker build`した結果が食い違わないようにするため。

`masuda update`は`.masuda/Dockerfile`を再ビルドする前に、そのFROM行を最新リリースタグへ書き換える（`internal/selfupdate.UpdateDockerfileFromTag`、`tadahiroyamamura/masuda:<旧タグ>`という行を正規表現で検出し置換する。該当行が無ければ何もしない——ユーザーが独自のbaseイメージに差し替えている場合、masuda updateはそれを上書きしない）。書き換えた上で`docker build`する。ビルド引数（`ARG`）でバージョンを注入する方式ではなく、Dockerfile本文の文字列を直接書き換える方式を採る——Dockerfile側に`ARG`の記述規約を守らせる必要がなく、単純な文字列置換で足りるため（詳細はAlternatives Considered）。

### `.masuda/settings.json`の`image`フィールドとの関係

`image`フィールドの意味は変更しない。`sandbox start`/`review start`が実行時に使う、既にビルド済みのローカルイメージタグ名を指す。`.masuda/Dockerfile`が対象repoに存在する場合、`masuda update`はそれをビルドし、結果を`image`フィールドが指すタグとしてtagする。`.masuda/Dockerfile`が存在しないプロジェクト（`masuda init`実行以前に作られた既存プロジェクト等）では、`masuda update`はDocker再ビルドの工程を丸ごとスキップする。

### 進行中ワークスペースがある場合: ハードブロック

`masuda update`はCLIバイナリを機械全体で共有する単一の実行ファイルとして置き換える。実行中のワークスペース（起動中のホストループプロセス・Dockerコンテナ等、`~/.local/share/masuda/workspaces/`配下に存在する全ワークスペース、対象repoを問わず横断的に見る）が1つでも存在する場合、`masuda update`は更新処理を実行せず拒否する。警告して続行を許可する設計にはしない。

## Alternatives Considered

- **CLIバイナリ更新をMakefileターゲット（`make update` = `git pull && make install && make docker-images`）として実装する**: masuda自身のソースツリー内でしか実行できず、操作対象プロジェクトのディレクトリから叩けない。`masuda update`という一貫したCLI体験を優先し不採用。
- **masuda自身のソースツリーの絶対パスをldflagsでバイナリに埋め込み、そこへ`cd`して`git pull`＋再ビルドする自己更新方式**: ソースツリーを移動・削除すると壊れる制約が残る。CIビルド済みバイナリを直接配布する方が単純であるため、リリースartifactダウンロード方式に切り替えた。
- **DockerイメージをGHCR（GitHub Container Registry）へプッシュする**: masuda本体が公開リポジトリのためGHCR側も技術的には公開設定にできるが、Docker Hubの方が運用上シンプルという判断でDocker Hubを選択した。
- **`.masuda/settings.json`に新規`dockerfile`フィールドを追加し、Dockerfileのパスを明示する**: 固定パス`.masuda/Dockerfile`にする方が、`.masuda/reviews/`と同じ規約（ディレクトリ・ファイル名固定、設定ファイルでの参照不要）に揃い発見しやすいため、フィールド追加は不採用。
- **`FROM`を`latest`固定にし、`masuda update`は`docker build --pull`だけで最新化する（floatingタグ方式）**: 検討の初期段階ではこちらを採用していたが、`latest`は同じ`.masuda/Dockerfile`のまま2回ビルドしても結果が同一である保証がない（pull元のレジストリ側の`latest`が進んでいれば変わる）。`masuda update`という明示的な更新操作の時だけバージョンが進み、それ以外は再現可能であってほしいため、`FROM`をバージョンタグへ固定しmasuda update自身がそのタグを書き換える方式に変更した。
- **`ARG`でbaseイメージのバージョンをpinし、`masuda update`が`--build-arg`で具体的なタグを注入する**: バージョン固定という結論自体は同じだが、対象repo側のDockerfileに`ARG`の記述規約（`ARG`宣言＋`FROM ${ARG}`という書き方）を守らせる必要がある。`masuda update`が`.masuda/Dockerfile`のFROM行を正規表現で直接書き換える方式なら、ユーザー側に特別な記法を要求せず、生成された雛形をそのまま素朴な`FROM tadahiroyamamura/masuda:<tag>`として読める。
- **進行中ワークスペースがあっても警告のみで続行を許可する**: 実行中コンテナは古いイメージタグのまま動作し続けるため実害は小さいという考え方もあったが、CLIバイナリは機械全体で共有される単一の実行ファイルであり、更新中に別プロセスから参照される状態を避けるため、警告ではなく拒否を選んだ。

## Consequences

- masuda自身のCIパイプライン（GitHub Actions、タグpushトリガー）を新設する必要がある。既存のMakefile（`build`・`install`・`docker-images`ターゲット）は開発時のローカルビルド用途として残るが、エンドユーザー向けの正規の配布経路ではなくなる
- `masuda update`はGitHubの未認証REST API（`/repos/<owner>/<repo>/releases/latest`）を利用する。IPアドレスあたり1時間60リクエストというレート制限があるが、手動実行が前提のため通常は問題にならない
- 既に`masuda init`済みの既存プロジェクトは`.masuda/Dockerfile`を持たない。この変更後、そうしたプロジェクトで`masuda update`を実行してもDocker再ビルド工程はスキップされ続ける（`.masuda/Dockerfile`が無いため）。これは[[0031-claude-settings-field-init-materialized-no-implicit-default]]が指摘した「既にinit済みのリポジトリは新フィールドを持たない」問題と同根であり、GitHub Issue #8のスコープとして残す
- 既存の`docker/{go,python,typescript,full}`という同梱の言語別Dockerfileバリアントの今後の位置づけ（CI公開対象に加えるか、`.masuda/Dockerfile`雛形生成方式に一本化して段階的に廃止するか）は本ADRのスコープ外とし、別途判断する
- `.masuda/Dockerfile`の`image`フィールド未設定時のtag先は`internal/sandbox.DefaultImage`（`masuda-loop`）にフォールバックする。`.masuda/Dockerfile`導入前から`sandbox start`/`review start`が使っていたローカルタグ名と同じにすることで、既存プロジェクトの挙動を変えない
- GitHub Issue #8（組み込みレビュー観点の後追い取り込み）は、本ADRが確立するバージョン識別・`masuda update`の仕組みに依存する後続作業として位置づけられる
