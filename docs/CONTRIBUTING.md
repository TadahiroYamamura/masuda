# masuda自体の開発に参加する

これはmasudaを**使う**ための手順（`docs/INSTALLATION.md`）ではなく、masuda自身のコード・ドキュメントに変更を加える人向けの手順。全体アーキテクチャは`docs/design/sandbox-workflow.md`、用語は`docs/glossary.md`、個々の設計判断は`docs/adr/`、現状の実装構成は`CLAUDE.md`「現状の構成」節を参照。

## 開発環境

`docs/INSTALLATION.md`の「前提条件」に加え、以下が必要。

- Python: `venv/`に依存関係インストール済み（`requirements.txt`）。テスト: `pytest orchestrator/tests/`
- Go: `go build ./...`・`go vet ./...`・`go test ./...`（標準の`go`ツールチェーンのみ、追加セットアップ不要）
- GitHub操作（Issue作成等）は`gh`を直接使わず`scripts/gh.sh`を使うこと。このリポジトリ専用のトークンを`.env`から読み込んで`gh`に渡すラッパー

## VM実行基盤（Issue #31、開発中）の追加要件

masudaはサンドボックスの実行基盤をDockerからCloud Hypervisor（MicroVM）へ移行する作業を進めている（Issue #31）。以下は**この移行作業に参加する場合のみ**必要——`masuda internal rootfs build`等、開発中のツールでのみ使う。VM backend自体（`masuda sandbox start`のVM版）はまだ存在せず、通常のmasuda利用には一切関係ない。

- **fakeroot**（`internal/rootfs.Build`がDockerイメージのファイル所有権を保ったままext4イメージへ変換するために使用。Debian/Ubuntu系は`apt install fakeroot`）
- **e2fsprogs**（`mkfs.ext4`・`debugfs`コマンド。通常プリインストール済み）
- **linux-image-generic**（ゲストOS用カーネル`vmlinuz`の入手のため。Debian/Ubuntu系は`apt install linux-image-generic`）
  - **既知の制限**: インストール直後の`/boot/vmlinuz-<version>`はroot:root所有・mode 600で、一般ユーザーからは読めない。Cloud Hypervisorはsudoなしで起動する設計のため、読み取り可能な場所へ複製する一手間が必要（M3で対応予定、現状未対応）
- Cloud Hypervisor・virtiofsd・KVM（`/dev/kvm`）・vhost-vsock（`/dev/vhost-vsock`）は将来的な前提条件だが、現時点ではmasudaのコード側に依存箇所はまだない（Issue #31のspikeでの手動検証のみ）
