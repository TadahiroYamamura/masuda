# masuda

AIとの協同開発（Provision→Discovery→Blueprint→Scaffold→Build→Review）を、ローカルPC上のサンドボックス（Cloud Hypervisor microVM）で自己ループ実行するためのツール群。

## 開発環境

- Python: `venv/`に依存関係インストール済み（`requirements.txt`）。テスト: `pytest orchestrator/tests/`
- Go: `go build ./...`・`go vet ./...`・`go test ./...`（標準の`go`ツールチェーンのみ、追加セットアップ不要）
- GitHub操作（Issue作成等）は`gh`を直接使わず`scripts/gh.sh`を使うこと。このリポジトリ専用のトークンを`.env`（Claudeからは読み書き不可、`.claude/settings.json`参照）から読み込んで`gh`に渡すラッパー
- rootfsイメージビルド（`masuda internal rootfs build`）: `docker`・`fakeroot`・`mkfs.ext4`（e2fsprogsパッケージ）が必要。`internal/sandbox`の統合テスト同様、無ければ`go test`は自動でskipする
- `orchestrator/`・`runtime/`を変更したら、実機テスト前にサンドボックスイメージを再ビルドすること（詳細は`docs/design/images-and-rootfs.md`）

## どこに何があるか

- **現在の設計**: [`docs/design/`](docs/design/README.md) — 触る対象からファイルを引ける対応表がREADMEにある。全体像は`docs/design/pipeline.md`
- **用語**: [`docs/glossary.md`](docs/glossary.md) — 段階名・ゲート名・エスカレーション区分と、ADRに出てくる旧称の対応表
- **設計判断の経緯・却下した代替案**: [`docs/adr/README.md`](docs/adr/README.md)（索引）
- **何がいつ変わったか**: `git log`。実装ロードマップという形のログは持っておらず、各コミットメッセージ（`意図`・`設計上の考慮点`・`懸念事項`）がその役割を担う

**`docs/adr/` は作業前に読むものではない。** 現在何がどう動いているかは`docs/design/`が正となる。ADRを開くのは「なぜこうなっているのか」を問われたとき、または既存の設計判断を覆すときに限り、その場合も番号順に読まず索引から必要な番号だけを開く。
