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

### orchestrator/・runtime/の焼き込み（`Dockerfile:99-121`）

`COPY --chown=ubuntu:ubuntu orchestrator/ orchestrator/`と`COPY --chown=ubuntu:ubuntu runtime/entrypoint.sh runtime/start_claude.sh runtime/merge_claude_settings.py runtime/`で、`/opt/masuda`以下にビルド時焼き込みする。

VM起動関連ファイル（`runtime/masuda-loop.service`・`runtime/fstab.vm`・`runtime/vm-dhcp.network`・`runtime/ssh-host-keys.service`）は`/etc/systemd/system/`・`/etc/fstab`・`/etc/systemd/network/`へ配置し、`systemctl enable`でunit有効化する。`systemctl enable`はディスク上のシンボリックリンク操作のみで実際にsystemdが動いている必要はないため、`docker build`内で完結する。

`runtime/CLAUDE.md`（ループプロトコル本体）はこのCOPY対象に**含まれない**。`~/.claude/CLAUDE.md`として`masuda sandbox start`実行時にmasuda CLI側から配置される（VM起動の詳細は`docs/design/sandbox-vm.md`）。

**制約: `orchestrator/`と`runtime/`はイメージビルド時に`COPY`で焼き込まれるため、これらを変更した後は`masuda-loop:latest`（base）と使用する言語バリアントイメージの両方を再ビルドしないと反映されない。** 再ビルドを忘れると、古いコードのままVMが起動し、新しい段階が実行されずに次の段階へ直行するなど原因が分かりにくい形で不具合が出る。

また、`orchestrator/`・`runtime/`はリポジトリルート直下に置く必要がある。ルート直下の`assets.go`が`go:embed`で`orchestrator/investigate_plan_graph.py`・`orchestrator/state_client.py`・`requirements.txt`・`runtime/CLAUDE.md`をmasuda CLIバイナリ自体に埋め込んでおり（Discovery/Blueprint段階のホスト側自己ループ用）、`go:embed`は宣言ファイル自身のディレクトリ以下（`..`不可）しか参照できないため、`assets.go`はリポジトリルートに置かれ、結果として埋め込み対象の`orchestrator/`・`runtime/`もルート直下という位置が固定されている。これはDockerfileの`COPY`が読む場所（ビルドコンテキストのルート）とも一致している。

### Claudeプラグイン・マーケットプレイス登録（`Dockerfile:149`）

`USER ubuntu`に切り替えた後、`claude install`でネイティブClaudeバイナリをインストールし、`claude plugin marketplace add anthropics/claude-plugins-official`で公式マーケットプレイスを登録する。プラグイン本体（`gopls-lsp`等）はこの時点ではインストールしない。マーケットプレイス登録は公開GitHubリポジトリの`git clone`のみで完結し認証不要なため、ビルド時に行う。

### エントリーポイント（`Dockerfile:157`）

`ENTRYPOINT ["/opt/masuda/runtime/entrypoint.sh"]`。`WORKDIR /workspace`。

## 言語別サンドボックスイメージバリアント

`docker/{go,python,typescript,full}/Dockerfile`は`FROM masuda-loop:latest`から派生し、言語別のツールチェーンとClaude公式LSPプラグインを追加する（ADR-0015）。対象リポジトリは`.masuda/settings.json`の`image`フィールドまたは`--image`フラグでどのバリアントを使うか選ぶ（解決順序は`docs/design/config.md`参照）。

| バリアント | 追加するツールチェーン | インストールするプラグイン |
|---|---|---|
| `docker/go/Dockerfile` | Go（`/usr/local/go`）+ `gopls` | `gopls-lsp@claude-plugins-official` |
| `docker/python/Dockerfile` | `pyright`（npm配布、base既存のNode.jsに乗る） | `pyright-lsp@claude-plugins-official` |
| `docker/typescript/Dockerfile` | `typescript` + `typescript-language-server`（npm配布） | `typescript-lsp@claude-plugins-official` |
| `docker/full/Dockerfile` | 上記3言語すべて（3つのDockerfileをFROMで合成するのではなく、各インストール手順をこのファイル1つに repeat したもの） | 上記3プラグインすべて |

各バリアントは`USER root`でツールチェーンをインストールした後`USER ubuntu`に戻し、`claude plugin install <name>@claude-plugins-official --scope user`でユーザースコープにプラグインを入れる。

**CIでは`docker/{go,python,typescript,full}/Dockerfile`は一切ビルドされない。** `.github/workflows/release.yml`の`docker`ジョブが`docker buildx build`でDocker Hub（`tadahiroyamamura/masuda`）へ公開するのはリポジトリルート直下の`Dockerfile`（ベースイメージ）のみで、言語バリアントは公開対象に含まれていない。

## Dockerイメージ→VM rootfs変換

`internal/rootfs.Build`（`internal/rootfs/build.go:98`）が、指定したDockerイメージのファイルシステムをブート可能なext4ディスクイメージへ変換する。VMBackend（`internal/sandbox/vmbackend.go`）が起動のたびにこの関数を直接呼ぶほか、隠しCLIサブコマンド`masuda internal rootfs build --image --output`（`cmd/masuda/internalrootfs.go`）からも同じ関数を呼べる。

### 変換の流れ

1. 前提コマンド（`docker`・`fakeroot`・`mkfs.ext4`・`depmod`）の存在チェック
2. `dockerExport`（`internal/rootfs/build.go:182`）: `docker create <image>`で（起動はしない）コンテナを作り、`docker export -o rootfs.tar`でマージ済みファイルシステムをtar化する。コンテナはexport後（失敗時も）必ず`docker rm -f`で削除する
3. tarのレギュラーファイル合計バイト数からイメージサイズを見積もる。ext4メタデータ分の余裕として実サイズの20%（`sizeSlackNumerator/Denominator = 6/5`）+ 固定256MiBを加算し、下限512MiB（`minImageSizeMiB`）を保証する
4. `ExtraFile`（下記）をホスト上のステージングディレクトリに書き出し、所有権を`"<uid>\t<gid>\t<path>"`形式のマニフェスト（TSV）に記録する
5. `extractAndFormat`（`internal/rootfs/build.go:277`）: 単一の`fakeroot`セッション内で
   - tarを展開（`tar -xpf`、tar内の所有権情報をfakerootが偽装保持）
   - ステージング済みextraファイルを`cp -a`で上書きオーバーレイし、マニフェストに従って`chown`し直す（`fakeroot`セッション内では`cp -a`自身の所有権保持が信頼できず、コピー先は「コピーを実行していると`fakeroot`が思っているuid」で作られてしまうため、明示的な`chown`が必須）
   - `/lib/modules/<version>`ツリーが存在すれば（＝`ExtraFile`経由でカーネルモジュールを注入した場合のみ。素のDockerイメージにはこのツリー自体が存在しない）`depmod -b`でモジュール依存関係DBを再生成する
   - `mkfs.ext4 -q -F -d <展開先> -L masuda-rootfs <出力先> <サイズ>M`でext4イメージを作成する
6. `<output>.tmp`に書いてから`os.Rename`で最終パスへ移動する（mkfs.ext4成功まで最終パスにファイルが現れない、アトミックな置き換え）

tar展開とmkfs.ext4を同一`fakeroot`セッション内で行っているのは、`/etc/shadow`（`root:shadow`）やsetuidバイナリなど、非特権ユーザーの`tar -x`では再現できない所有権を保ったままイメージへ焼き込むため。

### ExtraFile

`rootfs.ExtraFile{GuestPath, Content, Mode, UID, GID}`は、共有の`Dockerfile`/Dockerイメージには属さない、VM boot専用のファイルをrootfsへ追加注入する仕組み。`GuestPath`はイメージルートからの相対パス（先頭スラッシュなし）。呼び出し側（例: VMBackendがゲストの`~/.ssh/authorized_keys`やvirtiofsカーネルモジュールを注入する経路）の詳細は`docs/design/sandbox-vm.md`を参照。

### rootfsLabel

生成されるext4イメージには`masuda-rootfs`というボリュームラベル（`rootfsLabel`定数）が焼き込まれる。ゲストカーネルがブートデバイスをデバイス順ではなく`LABEL=`で参照するために使う（詳細は`docs/design/sandbox-vm.md`）。

## 既知の問題

未調査。修正時はここから消す。

- **`cmd/masuda/internalrootfs.go:12-18` のdocコメントが陳腐化している**: 「masudaにはまだこのイメージを使うVMサンドボックスバックエンドが無い（M2はイメージ作成のみ、M3でVM起動、M5でVMBackendを配線）」と書かれているが、VMBackendは実装済みで唯一のバックエンド（ADR-0044）
