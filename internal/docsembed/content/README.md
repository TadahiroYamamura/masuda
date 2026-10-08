リリースのビルド（`.github/workflows/release.yml`）が、`go build`の前にここへ`site/`（mkdocsで組んだサイト）と`md/`（`scripts/docs-prepare.sh`を通した後の`docs/`のMarkdown）を写す。手元のビルドと`go install`ではこのファイルだけが埋め込まれ、`masuda doc`は版のURLを案内する。

`go:embed`はパターンが1つ以上のファイルに当たらないとビルドが通らないので、このファイルをコミットしておく。
