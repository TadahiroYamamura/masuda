# インストール手順

masudaをローカルPCで動かすためのセットアップ手順。全体設計は
`docs/design/pipeline.md`、個々の設計判断は`docs/adr/README.md`（索引）を参照。

## 前提条件

masudaはホスト側CLI（Go製）とサンドボックスVM（Cloud Hypervisor製MicroVM、Issue #31）の2層構成。VMのrootfsはDockerイメージから変換して作る（`docker build`/`docker export`は使うが、`docker run`でコンテナとして実行することはない）。以下がホスト側に必要。

- **Go 1.26.3以上**（`go.mod`が要求するバージョン。CLIのビルドに使用）
- **Docker**（サンドボックスVMのrootfsの元となるイメージのビルドに使用。コンテナとしての実行には使わない）
- **git**（worktree操作。2.5以上、`git worktree`が使えるバージョン）
- **tmux**（フェーズ1-2のホスト側自己ループ用セッション。`masuda chat`でのアタッチにも使用）
- **inotify-tools**（`inotifywait`コマンド。Debian/Ubuntu系は`apt install inotify-tools`）。フェーズ1-2のホストループがG1ゲート待機で使用する。`while`ループやMonitorツールでのポーリングはClaude Code自身の許可リスク評価に引っかかり無人ループが確認プロンプトで詰まることが実機で確認されているため、単発のブロッキング`inotifywait`呼び出しに置き換えている
- **Python 3**（`python3`コマンドと`venv`モジュールが使えること。Debian/Ubuntu系では`python3-venv`パッケージが別途必要な場合がある）。フェーズ1-2のオーケストレーターはホスト上で直接Pythonスクリプトとして動くため必要。オーケストレータースクリプトと依存関係定義は`masuda`バイナリ自体に埋め込まれており、初回の`masuda plan start`実行時に`~/.local/share/masuda/runtime/`配下へ自動でvenvを構築する（手動セットアップ不要。以後のPythonバージョンアップ時などrequirements変更時のみ自動で再構築される）
- **Claude Code CLI**がホスト上にインストール済み、かつ`claude`でログイン済みであること（`masuda internal claude-token set`用に`claude setup-token`が使えることの確認も兼ねる。詳細は下記「VM実行基盤のセットアップ」参照）
- **VM実行基盤のセットアップ**（Cloud Hypervisor・virtiofsd・ネットワーク等）: 下記「VM実行基盤のセットアップ」節を先に済ませておくこと。`masuda sandbox start`はこれが無いと動かない

## 1. リポジトリの取得

```bash
git clone <このリポジトリのURL> masuda
cd masuda
```

## 2. masuda CLIのビルド

```bash
go build -o masuda ./cmd/masuda
```

`$PATH`の通ったディレクトリに置くと以後`masuda`コマンドとして直接呼べる。

```bash
sudo mv masuda /usr/local/bin/masuda
# または
go install ./cmd/masuda   # $(go env GOPATH)/bin/masuda に配置される
```

masuda自身のオーケレータースクリプト・`CLAUDE.md`はこのバイナリにgo:embedで焼き込まれているため、ビルド後はこのリポジトリのチェックアウトを`masuda`コマンドの隣に置いておく必要はない（`masuda`コマンドを対象リポジトリ側から実行しても、masuda自身のファイルの場所が対象リポジトリのパスと混同されることはない）。

## 3. サンドボックスVMの元イメージのビルド

フェーズ4-5はCloud Hypervisor製のMicroVM内で動く（Issue #31）。そのVMのrootfsはDockerイメージを変換して作るため、まずベースイメージをビルドする。

```bash
docker build -t masuda-loop .
```

対象リポジトリの言語に応じてLSPツール同梱の言語バリアント（ADR-0015）を追加でビルドできる。いずれもベースイメージ`masuda-loop:latest`から派生するため、先に上記のベースビルドを済ませておくこと。

```bash
docker build -f docker/go/Dockerfile         -t masuda-loop:go         .
docker build -f docker/python/Dockerfile     -t masuda-loop:python     .
docker build -f docker/typescript/Dockerfile -t masuda-loop:typescript .
docker build -f docker/full/Dockerfile       -t masuda-loop:full       .
```

言語バリアントを使わない場合、この手順は不要（`masuda-loop`のみで動く）。

## 3.5. VM実行基盤のセットアップ（Issue #31）

サンドボックスVMを起動するには、ホスト側に以下が必要。masuda自体の開発に参加する場合の追加要件は`docs/CONTRIBUTING.md`を参照。

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
- `masuda-net-helper`のビルド＋`setcap`: `internal/sandbox`のTAP管理（`EnsureTap`/`ReleaseTap`）が使う専用ヘルパーバイナリ。`CAP_NET_ADMIN`をこのバイナリ単体に付与する（masuda本体には付与しない——ブラスト半径を絞るため、詳細は`cmd/masuda-net-helper/main.go`のパッケージdocコメント参照）
- `dnsmasq`のインストール＋設定＋有効化: `br-masuda0`だけにバインドしたDHCPサーバー。VMゲストのIPアドレスは`systemd-networkd`のDHCPクライアントで自動取得する（複数ワークスペースが並行稼働してもmasuda側で独自のIP割り当て機構を持たずに済む）

### VMゲストSSH鍵

`masuda chat`はSSHでVMゲストへ接続する。鍵は`masuda`のインストール単位で1組（ワークスペースごとではない）。初回は自動生成される（`EnsureSSHKeypair`）ので、通常は何もしなくてよい。明示的に再生成したい場合（鍵の流出が疑われる場合等）:

```bash
masuda internal vm-ssh-key rotate
```

秘密鍵はホスト側にしか存在せず、ゲストのrootfsには公開鍵だけが`internal/rootfs.Build`のExtraFile機構でビルド時に注入される。**既知の制限**: 再生成しても、既にビルド済みのrootfsイメージ・起動中のVMは古い公開鍵を信頼し続ける（rebuild/restartまで遡及しない）。masudaのワークスペースは使い捨てなので許容している。

### VMゲストのClaude認証（必須）

VMゲストは別カーネルのため、ホストの`~/.claude/.credentials.json`・`~/.claude.json`をそのまま共有する方式が使えない。代わりに、CI/ヘッドレス環境向けに用意されている長期OAuthトークン（`claude setup-token`、サブスクリプション連携・有効期限1年）を使う。**これを登録しないと、VMゲスト内の`claude`は「ログインしていません」と表示するだけで動かない。**

```bash
claude setup-token   # 出力されたトークン文字列をコピー
echo "<コピーしたトークン>" | masuda internal claude-token set
```

保存先は`~/.local/share/masuda/claude-oauth-token`（mode 0600）。`VMBackend.Start`はこのファイルが存在する場合のみ、専用のvirtiofs共有でゲストへ渡す。

## 4. 対象リポジトリ側の設定（任意）

masudaで作業したい対象リポジトリのルートで`masuda init`を実行すると、`.masuda/`が生成される（ADR-0024）。

```bash
cd <対象リポジトリ>
masuda init --image masuda-loop:go --base main
```

- `.masuda/settings.json`: `--image`・`--base`フラグの毎回指定を省略できる（ADR-0015）
  - `image`: `masuda sandbox start` / `masuda review start`が使うサンドボックスVMの元イメージ（未指定時は`masuda-loop`）
  - `base`: worktree作成・マージ先のデフォルトブランチ（未指定時は`develop`）
- `.masuda/reviews/`: フェーズ5（レビュー）が使う観点をMarkdownファイルとして1観点1ファイルで持つ。masuda内蔵の14観点が展開される。プロジェクト固有の観点を追加したい場合はファイルを追加し、不要な観点はfrontmatterに`enable: false`を設定する（ADR-0033。ファイルは残るので、後から`masuda update`が新しい組み込み観点を取り込む際に「まだ導入されていない観点」と正しく区別できる）

`masuda init`は一度きりの操作で、`.masuda/`が既に存在するリポジトリに対しては再実行できない。masuda自体に追加された新しい組み込み観点は、`masuda init`の再実行ではなく`masuda update`（ADR-0033）が取り込む——`.masuda/reviews/`に無い観点だけを追加し、既存ファイル（`enable: false`にしたものも含む）には一切触れない。

`.masuda/`はmasuda自体のリポジトリではなく、**masudaで作業したい対象リポジトリ**の直下に生成される。

旧`.masuda.json`（単一ファイル）は読まれなくなった（破壊的変更、ADR-0024）。それを使っていた場合は`masuda init`で作り直すこと。

## 5. 動作確認

```bash
cd <対象リポジトリのルート>
masuda workspace list
```

エラーなく（空でも）一覧が表示されればセットアップは完了。

## 6. 最短の使い方

```bash
# 1. 新しいタスクを開始（worktree作成 + フェーズ1-2の自己ループ起動）
masuda plan start <branch> "実装したいタスクの説明"

# 2. プランを確認し、承認 or 差し戻し
masuda plan show <workspace-id>
masuda plan approve <workspace-id>
# もしくは対話で相談したい場合
masuda chat <workspace-id>

# 3. G1承認後、実装・レビュー用サンドボックスを起動（フェーズ4-5が自動で進む)
masuda sandbox start <workspace-id>

# 4. 最終レポート(final_report.md)を確認し、承認 or 差し戻し
masuda review show <workspace-id>
masuda review approve <workspace-id>   # ローカルマージ + worktree/VM/状態ディレクトリの後片付け（push はしない)
# もしくは対話で相談したい場合
masuda chat <workspace-id>
```

既存ブランチをレビューだけしたい場合は、フェーズ0-4を全てスキップして直接フェーズ5に入る`masuda review start <branch>`が使える。

```bash
masuda review start <branch> --base develop
```

`workspace-id`は`masuda workspace list`で確認できる。各コマンドの詳細は`masuda <command> --help`、内部設計は`CLAUDE.md`の「現状」節を参照。

masuda自体の開発に参加する場合の追加要件は`docs/CONTRIBUTING.md`を参照。
