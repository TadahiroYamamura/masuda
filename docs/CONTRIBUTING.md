# masuda自体の開発に参加する

これはmasudaを**使う**ための手順（`docs/INSTALLATION.md`）ではなく、masuda自身のコード・ドキュメントに変更を加える人向けの手順。全体アーキテクチャは`docs/design/sandbox-workflow.md`、用語は`docs/glossary.md`、個々の設計判断は`docs/adr/`、現状の実装構成は`CLAUDE.md`「現状の構成」節を参照。

## 開発環境

`docs/INSTALLATION.md`の「前提条件」に加え、以下が必要。

- Python: `venv/`に依存関係インストール済み（`requirements.txt`）。テスト: `pytest orchestrator/tests/`
- Go: `go build ./...`・`go vet ./...`・`go test ./...`（標準の`go`ツールチェーンのみ、追加セットアップ不要）
- GitHub操作（Issue作成等）は`gh`を直接使わず`scripts/gh.sh`を使うこと。このリポジトリ専用のトークンを`.env`から読み込んで`gh`に渡すラッパー

## VM実行基盤（Issue #31）を扱う開発上の注意

VM実行基盤そのもののセットアップ手順（Cloud Hypervisor・virtiofsd配置、`scripts/setup-vm-host.sh`、VMゲストSSH鍵、VMゲストのClaude認証）は利用者向け手順として`docs/INSTALLATION.md`「VM実行基盤のセットアップ」節に統合済み——masudaを使うだけなら、このリポジトリの開発に参加していなくても必要になるため。以下は`internal/sandbox`・`internal/rootfs`・`cmd/masuda-net-helper`等、masuda自身のGoコードを変更する開発者だけが意識すればよい注意点。

- `masuda-net-helper`は`CAP_NET_ADMIN`をsetcapで単体付与している（masuda本体には付与しない）。**バイナリを再ビルドするとcapabilityは失われるため、`go build`のたびに`setcap`のやり直しが必要**（`bash scripts/setup-vm-host.sh`を再実行すればよい、冪等）
- `internal/rootfs.Build`を変更した場合、`internal/rootfs/build_test.go`（実docker daemonが必要、無ければ自動skip）で確認すること
- `internal/sandbox/vmbackend.go`を変更した場合の実機検証は、`masuda-loop:latest`イメージの再ビルド（`docker build -t masuda-loop:latest .`）を忘れないこと——Dockerfile自体は変更していなくても、`runtime/`配下のファイル（`entrypoint.sh`等）はCOPYで焼き込まれているため
