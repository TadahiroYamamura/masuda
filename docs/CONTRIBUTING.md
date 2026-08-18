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
  - **既知の制限**: インストール直後の`/boot/vmlinuz-<version>`はroot:root所有・mode 600で、一般ユーザーからは読めない。読み取り可能な場所へ一度だけ複製する（自動化はしていない、手動での回避が前提）:
    ```bash
    sudo install -m 0644 -o "$USER" -g "$USER" \
      /boot/vmlinuz-$(uname -r) \
      ~/.local/share/masuda/vmlinuz-$(uname -r)
    ```
- **Cloud Hypervisor・virtiofsd**（`~/.local/bin/`等、`$PATH`が通った場所に配置）
- **TAP＋ブリッジ＋NAT**（ホスト単位・一度きりのセットアップ。`eth1`は環境のデフォルトルート向きインターフェース名に読み替える）:
  ```bash
  sudo ip link add br-masuda0 type bridge
  sudo ip addr add 192.168.200.1/24 dev br-masuda0
  sudo ip link set br-masuda0 up
  sudo sysctl -w net.ipv4.ip_forward=1
  sudo iptables -t nat -A POSTROUTING -s 192.168.200.0/24 -o eth1 -j MASQUERADE
  sudo iptables -A FORWARD -i br-masuda0 -o eth1 -j ACCEPT
  sudo iptables -A FORWARD -i eth1 -o br-masuda0 -m state --state RELATED,ESTABLISHED -j ACCEPT
  ```
  個々のワークスペース（VM）用のTAPデバイスはブリッジに接続する形で動的に作成・削除される（`masuda-net-helper`、下記）。ブリッジ自体は複数VMで共有される
- **`masuda-net-helper`のビルド＋setcap**（Issue #31 M5-2）: `internal/sandbox`のTAP管理（`EnsureTap`/`ReleaseTap`）が使う専用ヘルパーバイナリ。`CAP_NET_ADMIN`をこのバイナリ単体に付与する（masuda本体には付与しない——ブラスト半径を絞るため、詳細は`cmd/masuda-net-helper/main.go`のパッケージdocコメント参照）:
  ```bash
  go build -o ~/.local/bin/masuda-net-helper ./cmd/masuda-net-helper
  sudo setcap cap_net_admin+ep ~/.local/bin/masuda-net-helper
  ```
  バイナリを再ビルドするとcapabilityは失われるため、`go build`のたびに`setcap`をやり直す必要がある
