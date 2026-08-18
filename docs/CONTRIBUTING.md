# masuda自体の開発に参加する

これはmasudaを**使う**ための手順（`docs/INSTALLATION.md`）ではなく、masuda自身のコード・ドキュメントに変更を加える人向けの手順。全体アーキテクチャは`docs/design/sandbox-workflow.md`、用語は`docs/glossary.md`、個々の設計判断は`docs/adr/`、現状の実装構成は`CLAUDE.md`「現状の構成」節を参照。

## 開発環境

`docs/INSTALLATION.md`の「前提条件」に加え、以下が必要。

- Python: `venv/`に依存関係インストール済み（`requirements.txt`）。テスト: `pytest orchestrator/tests/`
- Go: `go build ./...`・`go vet ./...`・`go test ./...`（標準の`go`ツールチェーンのみ、追加セットアップ不要）
- GitHub操作（Issue作成等）は`gh`を直接使わず`scripts/gh.sh`を使うこと。このリポジトリ専用のトークンを`.env`から読み込んで`gh`に渡すラッパー

## VM実行基盤（Issue #31、開発中）の追加要件

masudaはサンドボックスの実行基盤をDockerからCloud Hypervisor（MicroVM）へ移行する作業を進めている（Issue #31）。以下は**この移行作業に参加する場合のみ**必要——`masuda internal rootfs build`等、開発中のツールでのみ使う。VM backend自体（`masuda sandbox start`のVM版）はまだ存在せず、通常のmasuda利用には一切関係ない。

### 事前に手動で用意するもの

- **Cloud Hypervisor・virtiofsd**（`~/.local/bin/`等、`$PATH`が通った場所に配置。標準のaptパッケージが無く、ダウンロード元がバージョン依存のためスクリプト化していない）
- **linux-image-genericの初回インストール**（未導入の場合）: `sudo apt-get install -y linux-image-generic`（インストール後の権限修正は下記スクリプトが行う）

### ホスト側の一度きりのセットアップ

上記2点を用意したら、以下を実行する（再実行しても安全な冪等スクリプト）。

```bash
bash scripts/setup-vm-host.sh
```

行っている内容（詳細・理由は各手順に対応するコミット・スクリプト自身のコメントを参照）:

- `fakeroot`・`e2fsprogs`のインストール（`internal/rootfs.Build`がDockerイメージの所有権を保ったままext4イメージへ変換するために使用）
- `vmlinuz`（ゲストOS用カーネル）を、一般ユーザーが読める場所へ複製（インストール直後はroot:root・mode 600のため）
- TAP＋ブリッジ（`br-masuda0`）＋outbound NATのセットアップ（個々のワークスペース用TAPデバイスは`masuda-net-helper`が動的に作成・削除する、ブリッジ自体が複数VMで共有されるホスト単位のインフラ）
- `masuda-net-helper`のビルド＋`setcap`（Issue #31 M5-2）: `internal/sandbox`のTAP管理（`EnsureTap`/`ReleaseTap`）が使う専用ヘルパーバイナリ。`CAP_NET_ADMIN`をこのバイナリ単体に付与する（masuda本体には付与しない——ブラスト半径を絞るため、詳細は`cmd/masuda-net-helper/main.go`のパッケージdocコメント参照）。**バイナリを再ビルドするとcapabilityは失われるため、`go build`のたびに`setcap`のやり直しが必要**（スクリプトは毎回再実行する前提で書かれている）
- `dnsmasq`のインストール＋設定＋有効化（Issue #31 M5-5）: `br-masuda0`だけにバインドしたDHCPサーバー。VMゲストのIPアドレスは`systemd-networkd`のDHCPクライアントで自動取得する（複数ワークスペースが並行稼働してもmasuda側で独自のIP割り当て機構を持たずに済む）

### VMゲストSSH鍵（Issue #31 M5-5）

`masuda chat`のVM版は`docker exec`の代わりにSSHでゲストへ接続する。鍵は`masuda`のインストール単位で1組（ワークスペースごとではない）。初回は自動生成される（`EnsureSSHKeypair`）が、明示的に再生成したい場合（鍵の流出が疑われる場合等）:

```bash
masuda internal vm-ssh-key rotate
```

秘密鍵はホスト側にしか存在せず、ゲストのrootfsには公開鍵だけが`internal/rootfs.Build`のExtraFile機構でビルド時に注入される（Dockerfileには焼き込まない——鍵を再生成してもDockerイメージの再ビルドが不要なようにするため）。**既知の制限**: 再生成しても、既にビルド済みのrootfsイメージ・起動中のVMは古い公開鍵を信頼し続ける（rebuild/restartまで遡及しない）。masudaのワークスペースは使い捨てなので許容している。
