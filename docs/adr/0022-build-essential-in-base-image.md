# ADR-0022: build-essential（Cツールチェーン）はDockerバリアントごとではなく共通baseイメージに1回だけ入れる

## Status

Accepted (2026-07-31)

## Context

Goで実装中の実機検証で、`go test -race`がgcc不在で実行できないという不具合が見つかった。`docker/go/Dockerfile`（[[0015-native-lsp-plugins-and-repo-declared-image]]で導入した言語別バリアント）を確認したところ、Goツールチェーン＋gopls-lspプラグインのみで、Cコンパイラを含む`build-essential`相当のパッケージが一切入っていなかった。`-race`はcgoに依存するため、これはGoツールチェーン単体では完結しない既知の制約であり、cgoを使う他の依存パッケージのビルドでも同様に失敗しうる。

この場でGo向けにだけ直す前に、他の言語バリアントで同種の問題が起きないかを検討した。`docker/python/Dockerfile`はpyright（npm配布）を足すだけでCツールチェーンを持たず、`docker/typescript/Dockerfile`も同様である。Pythonは多くの主要パッケージがPyPI上にmanylinuxの事前ビルド済みwheelを持つためコンパイラなしで動くことが多いが、wheelが提供されていないパッケージは実行時にソースビルドへフォールバックしgccを要求する。Node.js（TypeScriptバリアントの基盤）はより顕著で、`sqlite3`・`bcrypt`・`sharp`・`canvas`など、node-gyp経由のネイティブアドオンを持つ一般的なnpmパッケージは、プラットフォーム一致の事前ビルドバイナリがない場合にビルド時Cコンパイルを要求する。つまりこれはGo固有の問題ではなく、3バリアントに共通する「コンパイル済みでない依存関係を持ち込んだ瞬間に踏む」という性質の欠落だった。

## Decision

`build-essential`を`docker/go`・`docker/python`・`docker/typescript`それぞれに個別に追加するのではなく、共通baseの`Dockerfile`（リポジトリルート直下）の既存`apt-get install`行に1回だけ追加する。これにより`FROM masuda-loop:latest`から派生する4バリアント（go/python/typescript/full）すべてが自動的にCツールチェーンを持つ。

## Alternatives Considered

- **3つの言語別Dockerfileにそれぞれ`build-essential`を追加する**: 「その言語を使わない人はbuild-essentialも不要」という原則は保てるが、同じ`RUN apt-get install -y build-essential`相当の行が3箇所に重複し、将来の変更（バージョン固定・パッケージ追加等）のたびに3箇所を同期させる必要がある。また今回の実害はGoだけだったが、Python・TypeScript側にも同種の潜在的な欠落が既に存在していたため、Go分だけ直しても後で同じ調査を2回繰り返すことになる。
- **今回はGoバリアントだけ直し、Python/TypeScriptは実害が出るまで様子見する**: Cツールチェーンの必要性は個々の言語の設計判断というより「コンパイル済みでないネイティブ依存を扱えるか」という共通基盤の欠落であり、しかも`docker/full`は3言語を1イメージに束ねるkitchen sinkバリアント（[[0015-native-lsp-plugins-and-repo-declared-image]]）なので、per-variant追加を選んでも`full`は結局3つ分を合算して持つことになり、重複を避ける効果がない。実害が顕在化してから都度対応する運用は、今回のGoの一件がまさにその形で表面化しており、同じ調査コストを言語の数だけ払うことになるため不採用とした。

## Consequences

- baseイメージ（延いては全4バリアント＋full）のビルド時間・サイズが、`build-essential`分（数十〜100MB程度）増える。Cコンパイルを一切必要としないプロジェクト（例: 純粋なPythonアプリでwheel提供パッケージのみ使う場合）にとっても、この増分は避けられないトレードオフとして受け入れる
- 言語別Dockerfile（`docker/go`・`docker/python`・`docker/typescript`）自体は変更不要で、baseの1行追加のみで全バリアントに波及する
- [[0015-native-lsp-plugins-and-repo-declared-image]]で確立した「バリアント固有のものは`docker/*/Dockerfile`、共有インフラはbase」という配置方針（例: プラグインマーケットプレイス登録はbase、個々のLSPプラグインインストールはバリアント側）と同じ整理に従っている
