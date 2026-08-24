# イメージとrootfs

サンドボックス（VMBackend、Cloud Hypervisor microVM）が実行するファイルシステムは、Dockerイメージのビルド→`docker export`→ext4変換という経路で作られる。**Dockerイメージ自体はコンテナとして起動されない**（ADR-0044）。VMのrootfsを作るための変換元としてのみ使う。

## ベースサンドボックスイメージ

リポジトリルート直下の`Dockerfile`がベースイメージ`masuda-loop:latest`を定義する。

マルチステージビルド（`Dockerfile:6-11`）:

1. `masuda-builder`ステージ（`FROM golang:1.26`）: `CGO_ENABLED=0 go build -o /out/masuda ./cmd/masuda`でmasuda CLIバイナリをビルドする。このステージだけがGoツールチェーンを持ち、最終イメージには残らない
2. 最終ステージ（`FROM ubuntu:24.04`）: builderステージから`/out/masuda`だけを`COPY --from=masuda-builder`で取り込む（`/usr/local/bin/masuda`）

最終ステージが`apt-get install`する主なパッケージ（`Dockerfile:61-68`）:

- `build-essential`: C toolchain。全言語バリアント共通の基盤としてbaseに1回だけ入れる（ADR-0022）
- Node.js 22（`@anthropic-ai/claude-code`を`npm install -g`するため）、Python3 + venv
- `tmux`・`ttyd`（自己ループセッションのアタッチ・Web端末）
- `systemd`・`systemd-sysv`・`kmod`・`openssh-server`・`sudo`: いずれもVM起動後（ゲストとしてブートした時）にのみ意味を持つ。イメージ自体はコンテナとして起動されないため、`docker build`時点でこれらのサービスが動作することはない
  - `systemd`はVMゲストのPID1として動く
  - `kmod`（`modprobe`/`depmod`）はゲストの`systemd-udevd`が`virtiofs.ko`を自動ロードするために必要
  - `sudo`は`/etc/sudoers.d/masuda-vm-poweroff`で`systemctl poweroff`のみに絞って許可している（VM停止時の正常シャットダウン用）
  - `openssh-server`は`masuda chat`のVM向けSSHアタッチ用。ビルド時に生成されるホスト鍵（`ssh-keygen -A`のapt postinst）は削除しており（`rm -f /etc/ssh/ssh_host_*`）、各VMが初回起動時に`runtime/ssh-host-keys.service`で自分の鍵を生成する

ユーザー`ubuntu`（uid=1000、`ubuntu:24.04`に既存）を使う。`/workspace`（対象リポジトリのworktree用）・`/masuda-state`（状態ディレクトリ用）はvirtiofs共有のマウントポイントとして空けてあり、masuda自身の制御ファイル（venv・`orchestrator/`・`runtime/`）は`/opt/masuda`に置く。

### orchestrator/・runtime/の焼き込み（`Dockerfile:99-124`）

`COPY --chown=ubuntu:ubuntu orchestrator/ orchestrator/`と`COPY --chown=ubuntu:ubuntu runtime/entrypoint.sh runtime/start_claude.sh runtime/merge_claude_settings.py runtime/`で、`/opt/masuda`以下にビルド時焼き込みする。

VM起動関連ファイル（`runtime/masuda-loop.service`・`runtime/fstab.vm`・`runtime/vm-dhcp.network`・`runtime/ssh-host-keys.service`・`runtime/resolv-conf.service`）は`/etc/systemd/system/`・`/etc/fstab`・`/etc/systemd/network/`へ配置し、`systemctl enable`でunit有効化する。`systemctl enable`はディスク上のシンボリックリンク操作のみで実際にsystemdが動いている必要はないため、`docker build`内で完結する。

`runtime/CLAUDE.md`（ループプロトコル本体）はこのCOPY対象に**含まれない**。`~/.claude/CLAUDE.md`として`masuda sandbox start`実行時にmasuda CLI側から配置される（VM起動の詳細は`docs/design/sandbox-vm.md`）。

**制約: `orchestrator/`と`runtime/`はイメージビルド時に`COPY`で焼き込まれるため、これらを変更した後はbaseイメージと、それを`FROM`する対象リポジトリのイメージエントリの両方を再ビルドしないと反映されない。** 再ビルドを忘れると、古いコードのままVMが起動し、新しい段階が実行されずに次の段階へ直行するなど原因が分かりにくい形で不具合が出る。

また、`orchestrator/`・`runtime/`はリポジトリルート直下に置く必要がある。ルート直下の`assets.go`が`go:embed`で`orchestrator/investigate_plan_graph.py`・`orchestrator/state_client.py`・`requirements.txt`・`runtime/CLAUDE.md`をmasuda CLIバイナリ自体に埋め込んでおり（Discovery/Blueprint段階のホスト側自己ループ用）、`go:embed`は宣言ファイル自身のディレクトリ以下（`..`不可）しか参照できないため、`assets.go`はリポジトリルートに置かれ、結果として埋め込み対象の`orchestrator/`・`runtime/`もルート直下という位置が固定されている。これはDockerfileの`COPY`が読む場所（ビルドコンテキストのルート）とも一致している。

### Claudeプラグイン・マーケットプレイス登録（`Dockerfile:152`）

`USER ubuntu`に切り替えた後、`claude install`でネイティブClaudeバイナリをインストールし、`claude plugin marketplace add anthropics/claude-plugins-official`で公式マーケットプレイスを登録する。プラグイン本体（`gopls-lsp`等）はこの時点ではインストールしない。マーケットプレイス登録は公開GitHubリポジトリの`git clone`のみで完結し認証不要なため、ビルド時に行う。

### エントリーポイント（`Dockerfile:160`）

`ENTRYPOINT ["/opt/masuda/runtime/entrypoint.sh"]`。`WORKDIR /workspace`。

## 対象リポジトリのイメージエントリ

対象リポジトリは、使うイメージを`.masuda/images/<entry>/`というディレクトリ単位で宣言する（ADR-0054）。エントリ1つにつき2ファイル。

| ファイル | 内容 |
|---|---|
| `.masuda/images/<entry>/Dockerfile` | イメージの中身。`masuda init`が`default`エントリの雛形を書き出し、以後はユーザーが編集する |
| `.masuda/images/<entry>/settings.json` | そのイメージのビルドパラメータ（`internal/config.ImageConfig`）。現在のフィールドは`rootfsSizeMiB`のみ |

`.masuda/settings.json`の`image`フィールドと`--image`フラグが指すのは、Dockerのタグ名ではなく**このエントリ名**である（解決順序は`docs/design/config.md`参照）。エントリ名は`[a-z0-9][a-z0-9-]*`に制約される（`config.ValidateImageEntry`）。

ローカルのDockerタグはmasudaが導出する（`config.ImageTag`）。形は`masuda-<repoRootのディレクトリ名>-<repoRootの絶対パスのsha256先頭6桁>:<entry>`で、ユーザーが目にする識別子ではない。

雛形は2種類あり、CLIバイナリに埋め込まれている（`assets.go`）。

| テンプレート | 実体 | 内容 |
|---|---|---|
| `default` | `cmd/masuda/init.go`の`dockerfileTemplate` | 公開baseイメージへの`FROM`（タグ固定）＋ツールチェーン追加のコメント例 |
| `docker` | `templates/docker.Dockerfile` | `FROM ubuntu:24.04`から組む、Dockerデーモンを動かせるゲスト（特権コマンド用、`docs/design/privileged-commands.md`） |

エントリの追加は`masuda image add <entry> [--template default|docker]`、一覧は`masuda image list`（`cmd/masuda/image.go`）。宣言されたエントリは`masuda update`・`masuda sandbox build`がすべてビルドする（`docs/design/distribution-and-update.md`）。

`.masuda/images/`はワークスペースのcloneへ同期されない。ビルドは常に`repoRoot`基準で行われる（`internal/worktree`の`syncMasudaConfig`、ADR-0036）。

## Dockerイメージ→VM rootfs変換

`internal/rootfs.Build(image, outputPath string, opts Options) error`が、指定したDockerイメージのファイルシステムをブート可能なext4ディスクイメージへ変換する。VMBackend（`internal/sandbox/vmbackend.go`）が起動のたびにこの関数を直接呼ぶほか、使い捨て特権VM（`internal/sandbox/disposablevm.go`）と、デバッグ用の隠しCLIサブコマンド`masuda internal rootfs build --image --output [--size-mib]`（`cmd/masuda/internalrootfs.go`）も同じ関数を呼ぶ。

`Options`の4フィールドが、Dockerイメージに無いものをイメージへ足す経路になる。

| フィールド | 用途 |
|---|---|
| `ExtraFiles []ExtraFile` | 個々のファイル。所有権（UID/GID）を指定でき、fakerootセッション内で明示的に`chown`される |
| `ExtraDirs []ExtraDir` | ホストのディレクトリを丸ごと。ステージングを経由せず直接コピーされ、所有権はコピー元のまま（fakeroot内ではコピー主体がuid 0のため） |
| `ExtraSymlinks []ExtraSymlink` | シンボリックリンク。すべての内容を配置した後に作られる |
| `MinSizeMiB int` | イメージサイズの**下限**。自動計算値との大きい方が使われる。上限は`maxImageSizeMiB`（64GiB）で、超過は`Build`がエラーにする |

### 変換の流れ

1. 前提コマンド（`docker`・`fakeroot`・`mkfs.ext4`・`depmod`）の存在チェック
2. `dockerExport`（`internal/rootfs/build.go:185`）: `docker create <image>`で（起動はしない）コンテナを作り、`docker export -o rootfs.tar`でマージ済みファイルシステムをtar化する。コンテナはexport後（失敗時も）必ず`docker rm -f`で削除する
3. tarのレギュラーファイル合計バイト数に、`Options`が注入する分（`ExtraFiles`の内容量と`ExtraDirs`配下の通常ファイル量、`injectedBytes`）を足してイメージサイズを見積もる。ext4メタデータ分の余裕として実サイズの20%（`sizeSlackNumerator/Denominator = 6/5`）+ 固定256MiBを加算し、下限512MiB（`minImageSizeMiB`）を保証する。`Options.MinSizeMiB`が指定されていれば、その値との大きい方を採る
4. `ExtraFile`（下記）をホスト上のステージングディレクトリに書き出し、所有権を`"<uid>\t<gid>\t<path>"`形式のマニフェスト（TSV）に記録する
5. `extractAndFormat`（`internal/rootfs/build.go:277`）: 単一の`fakeroot`セッション内で
   - tarを展開（`tar -xpf`、tar内の所有権情報をfakerootが偽装保持）
   - ステージング済みextraファイルを`cp -a`で上書きオーバーレイし、マニフェストに従って`chown`し直す（`fakeroot`セッション内では`cp -a`自身の所有権保持が信頼できず、コピー先は「コピーを実行していると`fakeroot`が思っているuid」で作られてしまうため、明示的な`chown`が必須）
   - `/lib/modules/<version>`ツリーが存在すれば（＝`ExtraFile`経由でカーネルモジュールを注入した場合のみ。素のDockerイメージにはこのツリー自体が存在しない）`depmod -b`でモジュール依存関係DBを再生成する
   - `mkfs.ext4 -q -F -d <展開先> -L masuda-rootfs <出力先> <サイズ>M`でext4イメージを作成する
6. `<output>.tmp`に書いてから`os.Rename`で最終パスへ移動する（mkfs.ext4成功まで最終パスにファイルが現れない、アトミックな置き換え）

tar展開とmkfs.ext4を同一`fakeroot`セッション内で行っているのは、`/etc/shadow`（`root:shadow`）やsetuidバイナリなど、非特権ユーザーの`tar -x`では再現できない所有権を保ったままイメージへ焼き込むため。

### 注入されるもの

`GuestPath`はいずれもイメージルートからの相対パス（先頭スラッシュなし）。

| 型 | フィールド | 使われ方 |
|---|---|---|
| `ExtraFile` | `{GuestPath, Content, Mode, UID, GID}` | VMBackendがゲストの`~/.ssh/authorized_keys`・`~/.claude/CLAUDE.md`・virtiofsカーネルモジュールを注入する（`docs/design/sandbox-vm.md`）。使い捨て特権VMはrunnerとそのunitを注入する（`docs/design/privileged-commands.md`） |
| `ExtraDir` | `{HostPath, GuestPath}` | 使い捨て特権VMがゲストカーネルのモジュールツリー全体（約155MiB）を入れる |
| `ExtraSymlink` | `{GuestPath, Target}` | 使い捨て特権VMがrunner unitの`multi-user.target.wants`リンクを作る |

`ExtraDir`が所有権フィールドを持たないのは、コピーがfakerootセッション内で行われ、そこではコピー主体が既にuid 0であるため。`ExtraFile`はmasudaの非特権プロセスが先にステージングするので、明示的な`chown`が要る。

`usr/lib/...`と`lib/...`の使い分けに注意する。Ubuntuイメージはusrmergeで`/lib`がシンボリックリンクのため、モジュールツリーの注入先は`usr/lib/modules/<version>`でなければならない。

### rootfsLabel

生成されるext4イメージには`masuda-rootfs`というボリュームラベル（`rootfsLabel`定数）が焼き込まれる。ゲストカーネルがブートデバイスをデバイス順ではなく`LABEL=`で参照するために使う（詳細は`docs/design/sandbox-vm.md`）。

## 既知の問題

現在把握しているものは無い。
