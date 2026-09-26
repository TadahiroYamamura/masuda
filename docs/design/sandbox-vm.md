# サンドボックスVM

masudaのサンドボックスはCloud Hypervisor microVM（`internal/sandbox.VMBackend`、VM自体の起動・停止は`internal/microvm`）として動く。Dockerはこのmicrovmのrootfs元イメージを作る（`docker build`/`docker export`）ためだけに使い、コンテナとして起動することはない（ADR-0044）。

ネットワーク（TAPデバイス・bridge・NAT・ゲストIP解決、SSH接続のargv組み立て）は`docs/design/networking.md`を参照。rootfsのext4変換・Dockerイメージの中身は`docs/design/images-and-rootfs.md`を参照。状態デーモンの中身は`docs/design/state-daemon-mcp.md`を参照。ゲストの外向きTLS通信を制限するegressフィルタの仕組みは`docs/design/egress-filter.md`を参照。

## パッケージの分担

VMの起動・停止は2層に分かれる。

- **`internal/microvm`**: masudaを知らない汎用層。Dockerイメージからrootfsを作ってCloud Hypervisor microVMを起動・停止し、virtiofsによるディレクトリ共有・TAPデバイス・SSH鍵・ゲストIP解決・pidfileによるプロセス管理を持つ。呼び出し側はVMを`microvm.Spec`（ID・イメージ・rootfsへの注入物・カーネルモジュールの範囲・共有ディレクトリ・カーネル引数）として記述し、ホスト側のセットアップ（データディレクトリ・bridge名・DHCPリースファイル・net-helperのバイナリ名・ゲストユーザー）を`microvm.Host`として渡す。依存してよいmasudaのパッケージは`internal/rootfs`だけで、`TestNoMasudaImports`（`internal/microvm/imports_test.go`）がこれを強制する
- **`internal/sandbox`**: masuda固有の層。イメージエントリの解決、Claude OAuthトークンの共有、git identity、mcp-relay、egress-proxy、特権コマンドの承認と成果物回収を持ち、それらを`microvm.Spec`に詰めて`internal/microvm`を呼ぶ。`vmHost()`（`internal/sandbox/vmbackend.go`）がmasudaのホスト設定（`workspace.DataHome()`・`br-masuda0`・`/var/lib/misc/masuda-dnsmasq.leases`・`masuda-net-helper`・ゲストユーザー`ubuntu`（uid/gid 1000））を`microvm.Host`として組み立てる

`internal/microvm`がゲストイメージに要求するもの（systemd、`GuestUser`の`authorized_keys`を受け付けるsshd、最初のNICのDHCPクライアント、各共有タグのfstabエントリ、`GuestUser`がパスワード無しで実行できる`sudo systemctl poweroff`）はパッケージ冒頭のdoc commentにまとめてある。masudaのイメージがこれをどう満たしているかは後述の「VMゲスト起動シーケンス」を参照。

## Backendインターフェース

`internal/sandbox.Backend`（`internal/sandbox/backend.go`）は`Start`/`Stop`/`IsRunning`/`AttachArgs`の4メソッドを定義する。`cmd/masuda/sandbox.go`が`var sandboxBackend sandbox.Backend = sandbox.VMBackend{}`としてパッケージ変数を持ち、`sandbox`・`chat`・`gate`・`review`・`workspace`・`update`の各サブコマンドはこの変数経由でのみサンドボックスを操作する。`VMBackend`は空structで、各メソッドは同名の`vmStart`/`vmStop`/`vmIsRunning`/`vmAttachArgs`関数（`internal/sandbox/vmbackend.go`）へそのまま委譲する。

`masuda sandbox start <workspace-id> [--image]`・`masuda sandbox stop <workspace-id>`がCLIから直接叩ける操作。`sandbox start`は`Handle{ContainerName, HostPort}`を`container=%s host_port=%d`の形式で出力するが、`VMBackend`は`HostPort`を一切設定しないため常に`0`が出力される（`ContainerName`にはTAP名が入る）。`IsRunning`/`AttachArgs`はCLIサブコマンドを持たず、`chat`（生きているセッションの判別）・`gate`（ゲート解決時の後始末）・`update`/`workspace list`（実行中判定）から内部的に呼ばれる。

## VM起動

`vmStart(id, worktreeDir, stateDir, repoRoot, image)`（`internal/sandbox/vmbackend.go`）はワークスペースID単位で冪等に動く。既に`IsRunning(id)`なら何もせず即座に`Handle`を返す。新規起動時はまずmasuda側の準備をする。

1. `Host.Check()`で`cloud-hypervisor`バイナリとカーネル（`~/.local/share/masuda/vmlinuz-*`）の存在を確かめる
2. `WriteGitIdentity(stateDir, repoRoot)`でgit identityをstateDir直下に書き込む（後述）
3. `resolveImageEntry(repoRoot, image)`で、渡されたイメージ**エントリ名**（ADR-0054）をローカルDockerタグとそのエントリのビルドパラメータへ解決する。エントリの`Dockerfile`が存在しなければ、`masuda init`か`masuda sandbox build`を促すエラーで止まる
4. `ClaudeOAuthTokenPath()`にトークンファイルがあれば、VMの作業ディレクトリ内の`claude-secrets/token`へコピーし、`claude-secrets`タグの共有に加える。トークンが未登録なら共有自体をスキップし、これはエラー扱いにしない
5. `freePort()`でmcp-relay用ポートを取り、`StartMCPRelay(statedaemon.CuratedSocketPath(stateDir), vmBridgeGatewayIP, relayPort, ...)`をホスト側プロセスとして起動する（mcp-relay自体の中身は`networking.md`/`state-daemon-mcp.md`参照）。`relayPort`はランダム割り当てのため`mcp-relay.port`に書き残す。VMより先に起動するのは、そのアドレスをゲストのカーネル引数`masuda.mcp_relay=`に載せるため
6. `EnsureEgressProxy()`でホスト共有のegress-proxyプロセスが起動済みか確認し、無ければ起動する（`internal/sandbox/egressproxy.go`、冪等——2台目以降のVMは既に起動済みのものを見つけるだけ）。ゲスト側にこのプロキシのアドレスを渡す必要は無い——REDIRECTルールが自動的に443番宛のトラフィックをそこへ届けるため。仕組み自体は`docs/design/egress-filter.md`を参照

そのうえで`Host.Start(spec)`（`internal/microvm/vm.go`）を呼ぶ。`spec`は、共有に`workspace`（worktreeDir）・`masuda-state`（stateDir）・[`claude-secrets`]、rootfsへの注入物に`~/.claude/CLAUDE.md`（`masuda.ClaudeMD`、`go:embed`されたループ仕様、ADR-0007）、カーネルモジュールに`VirtiofsModuleOnly`、カーネル引数に`masuda.mcp_relay=<relay.Addr>`を持つ。`Host.Start`の中身は次の順に並ぶ。

1. VMの作業ディレクトリ（`<DataDir>/vm/<id>/`）を確保する。stateDirとは別で、rootfsイメージ・virtiofsdのソケットとログ・cloud-hypervisorのpidfileなど、ゲストに見える必要のないものだけを置く。masuda側のmcp-relayのpidfile・ログ・ポートファイルとClaudeトークンの置き場もここにある
2. `EnsureSSHKeypair()`でホスト全体で共有するVM用SSH鍵ペア（初回のみ生成）を確保し、公開鍵を`home/<GuestUser>/.ssh/authorized_keys`として注入物に加える
3. `VirtiofsModuleOnly`なら、ホストの`/lib/modules/<version>/kernel/fs/fuse/virtiofs.ko*`をゲストの`usr/lib/modules/<version>/...`として注入物に加える。ゲストパスを`usr/lib/modules/...`にしているのは、masuda-loopイメージ（Ubuntu 24.04、usrmerge）で`/lib`が`/usr/lib`へのシンボリックリンクであり、`lib/...`のまま実ディレクトリを重ねようとするとrootfs構築側の`cp -a`がシンボリックリンクをディレクトリで上書きしようとして失敗するため
4. `rootfs.Build`でrootfsイメージを毎回ゼロから構築する
5. `ensureTap`でネットワークインターフェースを確保（詳細は`networking.md`）
6. 共有ごとに`StartVirtiofs`でvirtiofsdを起動する。ソケットは`virtiofs-<tag>.sock`
7. `cloud-hypervisor`をカーネル・rootfs・`--fs`・`--net`（TAP＋`MACFor(id)`のMACアドレス）・`--cmdline`（`console=ttyS0 root=/dev/vda rw`に`spec.KernelArgs`を足したもの）付きで起動し、`StartBackgroundProcess`でpidfile化する
8. `LookupGuestIP(mac, LeaseFile, 30秒)`でDHCPリースが付くまで待つ

`Host.Start`の手順5以降は、失敗時にそこまでに確保したTAPとvirtiofsdを解放してからエラーを返す。DHCPリース待ちの失敗だけは`Shutdown`と`Remove`をまるごと呼ぶ形でロールバックする。`Host.Start`が失敗すると、`vmStart`は自分で起動したmcp-relayを止める。egress-proxyは他のワークスペースのVMが使っている可能性があるため止めない。

## VM停止とステータス確認

`vmStop(id)`は`vmStart`とは別のCLI呼び出し（`masuda sandbox stop`）として動くため、`vmStart`が保持していたプロセスハンドルは一切引き継がない。idから決定的に導出できるパスと、作業ディレクトリに残したpidfileだけを頼りに再発見して止める。`Host.Shutdown`→mcp-relay停止→`Host.Remove`の順に呼ぶ。mcp-relayを間に挟むのは、そのpidfileが`Remove`の消す作業ディレクトリにあるため。

`Host.Shutdown(id)`（`internal/microvm/vm.go`）の手順は次のとおり。

1. `attemptGracefulShutdown`: DHCPリースからゲストIPを引き、VM用SSH鍵で`sudo -n systemctl poweroff`をSSH実行する。DHCPリースが無い・SSH到達不能などいずれの失敗もベストエフォートで無視して次に進む
2. `waitForProcessExit`（15秒）でcloud-hypervisorプロセス自身の終了を待つ
3. `KillStalePID`＋pidfile削除でcloud-hypervisorを強制終了（グレースフル停止が間に合わなかった場合のフォールバック）
4. 作業ディレクトリの`virtiofs-*.sock.pid`をすべて探し、それぞれのvirtiofsdを止める。`Shutdown`は起動時の`Spec`を持たないため、共有のタグを知らなくても止められるようにpidfileから探す
5. `releaseTap(id)`でネットワークインターフェースを解放

mcp-relayは、`vmStart`が`StartMCPRelay`に渡したcurated socketのパスを目印に`KillStalePID`で止める。状態ディレクトリが解決できない場合は目印が空になり、何も止めない。`Host.Remove(id)`は作業ディレクトリをまるごと削除する（rootfsイメージ・ソケット・ログ含め、次回`Start`時にゼロから作り直される前提でephemeral）。`Shutdown`がエラーを返した場合、`vmStop`はmcp-relayだけ止めて`Remove`は呼ばない。

グレースフルシャットダウンの手順（1・2）を省いてSIGTERM直行にすると、ゲストがアンマウント・syncする前にプロセスが落ちディスクイメージが壊れる。そのため`Shutdown`は必ず先にSSH経由の`systemctl poweroff`を試み、それでプロセスが終了しなかった場合にのみ手順3のkillへフォールバックする。

`Host.IsRunning(id)`はcloud-hypervisorのpidfileを読み、そのPIDが**このVMの**cloud-hypervisorかを`/proc/<pid>/cmdline`で同定する（argvに載る`<workDir>/rootfs.img`を目印にする。ADR-0061——シグナル0では再起動後に再利用されたPIDを見分けられず、`vmStart`が冒頭でこれを見て早期returnするため`sandbox start`が無言で何もしなくなる）。`vmAttachArgs(id)`は`Host.AttachArgs(id, "tmux", "attach", "-t", "claude-work")`を呼ぶ。`AttachArgs`は`LookupGuestIP`（タイムアウト20秒）でゲストIPを引き、SSHのargvの末尾に渡されたコマンドを付けて返す（SSH接続オプション自体は`networking.md`参照）。

## VMゲスト起動シーケンス

ゲストのrootfsはsystemdをPID1として起動する。関係するユニット・設定ファイルは`runtime/`配下にあり、いずれもrootfsビルド時にイメージへ焼き込まれる（`images-and-rootfs.md`参照）。

- `runtime/vm-dhcp.network`: `eth0`（cloud-hypervisorのvirtio-netデバイス、predictable-naming無効な最小イメージのためこの名前で出る）に対しsystemd-networkdのDHCPクライアントを有効化する。ホスト側dnsmasqがアドレスを払い出す
- `runtime/ssh-host-keys.service`: `ssh.service`より前（`Before=`）に走るoneshot。`ConditionPathExists=!/etc/ssh/ssh_host_rsa_key`によりホスト鍵が存在しない場合のみ`ssh-keygen -A`を実行する。rootfsビルド時にopensshパッケージ導入時の自動生成ホスト鍵を意図的に削除しているため、これが無いと`sshd`はホスト鍵無しで起動に失敗する
- `/etc/fstab`（`runtime/fstab.vm`がビルド時に追記）: `workspace`・`masuda-state`タグをvirtiofsとして`/workspace`・`/masuda-state`にマウントする。`claude-secrets`タグは`/masuda-secrets`にマウントするが、`vmStart`がトークン未登録時にこのタグ自体を共有に加えないことがあるため`nofail`を付けている
- `runtime/masuda-loop.service`: `User=ubuntu`のsimpleサービスとして`/opt/masuda/runtime/entrypoint.sh`を実行する。`Requires=workspace.mount masuda\x2dstate.mount`（この2つは必須）、`After=`にはこれへ`masuda\x2dsecrets.mount`も加える（claude-secretsマウントは無くても起動をブロックしない、順序だけ後段に置く）

`runtime/entrypoint.sh`（16-111行目）がPID1配下でのゲスト起動の実処理を担う。

1. `/proc/cmdline`から`masuda.mcp_relay=`を`sed`で取り出す。値がある（＝VM経路）場合はゲスト内でmcp-relayを起動せず、その値をそのまま使う。値が無い場合はDockerパスと同じ動きにフォールバックし、`masuda internal mcp-relay --socket /masuda-state/daemon-curated.sock --port 39217`をゲスト内で自前起動する
2. `python3 /opt/masuda/runtime/merge_claude_settings.py`の出力を`/tmp/masuda-claude-settings.json`に書き、`claude --settings`にはこのマージ済み設定を渡す（ビルド時に焼き込まれたプラグイン状態と`.masuda/settings.json`の`claudeSettings`をマージする処理そのものの詳細はスクリプト側参照）
3. `/masuda-secrets/token`が読めれば`CLAUDE_CODE_OAUTH_TOKEN`としてexportする（`ps`出力に載らないよう、tmuxコマンド文字列へのインライン展開ではなく環境変数export）
4. `/masuda-state/.masuda-git-identity`が読めれば1行目を`GIT_AUTHOR_NAME`/`GIT_COMMITTER_NAME`、2行目を`GIT_AUTHOR_EMAIL`/`GIT_COMMITTER_EMAIL`としてexportする
5. `tmux new-session -d -s claude-work`で`claude --dangerously-skip-permissions --settings <merged> --mcp-config <config> -- <初期プロンプト>`を起動する。`--mcp-config`は複数設定をスペース区切りで受け取る仕様のため、その直後に`--`を挟まないと初期プロンプト文字列が誤って追加の`--mcp-config`引数として解釈され起動に失敗する
6. `ttyd --writable --port 7682 tmux attach -t claude-work`をバックグラウンドで起動し、`claude-work`セッションが無くなるまで2秒間隔でポーリングし続ける。セッション終了を検知したら`ttyd`をkillして`exit 0`する

**`runtime/start_claude.sh`は`entrypoint.sh`の1〜5相当のロジックを重複して持っており、手動同期が必要**。既存のtmuxセッションがあれば即座に何もせず終了する点（再入時のガード）以外はentrypoint.shと同じ手順（mcp-relayアドレス解決・設定マージ・トークンexport・git identity export・`tmux new-session`）を踏む。片方だけを修正すると挙動が食い違う。

## virtiofs共有ディレクトリ管理

`internal/microvm/virtiofs.go`の`StartVirtiofs(dir, socketPath, logPath)`が1呼び出しにつき1つのvirtiofsdプロセスを起動し、1つのvhost-user UDSソケットで1ディレクトリを共有する。`Host.Start`/`Host.Run`は`Spec.Shares`の要素ごとにこれを呼ぶ。masudaの常駐VMでは`/workspace`・`/masuda-state`・（トークン登録時のみ）`/masuda-secrets`の最大3つになる。

- `KillStalePID(pidPath(socketPath), socketPath)`で前回の孤児プロセスを片付けてから、既存のソケットファイルを削除し、`virtiofsd --socket-path=<socketPath> --shared-dir=<dir> --sandbox=none`を起動する
- `--sandbox=none`を使う。virtiofsd既定の`--sandbox=namespace`は`newuidmap`/`newgidmap`（uidmapパッケージ）を要求するが、これはmasudaのホスト前提条件に含まれていない（ADR-0049）
- `waitForSocket(socketPath, virtiofsStartupTimeout)`（5秒）でソケットファイルの出現を待ち、間に合わなければ起動したプロセスを止めてエラーを返す
- `pidPath(identity string) string`は`identity + ".pid"`を返す。`VirtiofsProcess.Stop()`はpidfile経由でのプロセス停止とソケットファイル削除の両方を行う

## VM SSH鍵管理

`internal/microvm/sshkey.go`はVM単位ではなくホスト単位（ホスト全体で1組）のed25519鍵ペアを`<Host.DataDir>/vm-ssh-key`・`vm-ssh-key.pub`に持つ。masudaでは`DataDir`が`workspace.DataHome()`になる。

- `Host.EnsureSSHKeypair()`は`Host.Start`/`Host.Run`から呼ばれ、鍵が無ければ`GenerateSSHKeypair()`で生成し、あれば既存のものをそのまま返す
- `Host.GenerateSSHKeypair()`は`masuda vm-ssh-key rotate`（`cmd/masuda/vmsshkey.go`、`internal/sandbox`の同名関数経由）から無条件に呼ばれ、既存鍵を問答無用で上書きする。公開鍵だけがrootfsビルド時に`home/<GuestUser>/.ssh/authorized_keys`へExtraFileとして注入され、秘密鍵はホストの外に一切出ない
- ローテーションは既にビルド済みのrootfsイメージ・起動中のVMには遡って反映されない。それらは再ビルド・再起動されるまで旧公開鍵を信頼し続ける
- `sshBaseArgs`（`internal/microvm/ssh.go`）はホスト鍵検証を`StrictHostKeyChecking=no`＋`UserKnownHostsFile=/dev/null`で意図的に無効化している。ゲストIPはDHCP払い出しでVMのライフサイクルをまたいで使い回されるため、known_hostsベースの検証はスプリアスな警告を生むだけで、この接続の安全性は秘密鍵の保持だけに依っている

## 認証情報受け渡し

git identityとClaude OAuthトークンの2種類を、rootfsへの焼き込みではなくvirtiofs共有経由でゲストへ渡す（rootfsが起動のたびに毎回作り直されるため、焼き込みだと登録・更新のたびに再ビルドが要る）。

- **git identity**（`internal/sandbox/gitidentity.go`）: `WriteGitIdentity(stateDir, repoRoot)`が`git -C <repoRoot> config --local --get user.name/user.email`を試し、値が無ければ`git config --global --get`にフォールバックして`stateDir/.masuda-git-identity`へ「1行目name・2行目email」の2行プレーンテキストとして書く。シェルソース可能な形式にしていないのは、値に空白・引用符が含まれてもエスケープ処理なしで安全に読めるようにするため（ゲスト側は`sed -n '1p'/'2p'`で読む）。この共有は新規のvirtiofsタグを増やさず、既存の`/masuda-state`共有に相乗りする
- **Claude OAuthトークン**（`internal/sandbox/claudetoken.go`）: `masuda claude set-token`（`cmd/masuda/claude.go`、標準入力から読む）が`SetClaudeOAuthToken`で`workspace.DataHome()/claude-oauth-token`（mode 0600）へホスト全体で1つ保存する。`claude setup-token`が発行する長期（1年）OAuthトークンで、ホストの`~/.claude/.credentials.json`のようなファイルをそのまま渡すのではなく、`CLAUDE_CODE_OAUTH_TOKEN`環境変数としてゲストへ渡す前提の値。`vmStart`は登録済みならこの値をVMの作業ディレクトリ内の`claude-secrets/token`へコピーし、専用のvirtiofs共有（`/masuda-secrets`）で渡す。未登録でもエラーにはせず、その場合ゲストは単に未ログイン状態で起動する（`sandbox start`・`review start`は起動前に警告を出す——`docs/design/cli.md`）

## バックグラウンドプロセス管理基盤

`internal/microvm/bgprocess.go`はvirtiofsd・cloud-hypervisor、および`internal/sandbox`のmcp-relay・egress-proxyに使う、pidfileベースの起動/停止/孤児回収の共通実装。

- `StartBackgroundProcess(cmd, pidFilePath, marker)`: まず`KillStalePID(pidFilePath, marker)`で前回の孤児を片付けてから`cmd.Start()`し、実際に起動したPIDを`pidFilePath`へ書く。書き込みに失敗したら起動したプロセスをkillしてロールバックする
- `StopBackgroundProcess(proc, pidFilePath)`: `SIGTERM`を送って`proc.Wait()`し、pidfileを削除する。プロセスが既に終了済み（`os.ErrProcessDone`）でもエラーにしない
- `KillStalePID(pidFilePath, marker)`: pidfileが指すPIDが`marker`を含むargvを持つ場合にだけ`SIGTERM`を送るベストエフォート処理。同定を経ないと、再起動でPIDが再利用されている場合に無関係なプロセスを殺す（ADR-0061）。`marker`はプロセスを1つに絞れる値でなければならない——virtiofsd・mcp-relayはソケットパス、cloud-hypervisorはrootfsイメージのパス、egress-proxyだけはホスト全体で1プロセスなのでバイナリ名。空文字列は「何もしない」を意味する。`Process.Wait()`を呼ばない——このPIDは呼び出し元プロセスの子ではなく（masuda自体が前回異常終了して再起動した場合等）、`Wait`は実子にしか使えず`ECHILD`で失敗するため

## 既知の問題

未調査。修正時はここから消す。

- **`Handle.HostPort` が常に0で、`sandbox start` が誤った情報を表示する**: `cmd/masuda/sandbox.go:65` は `container=%s host_port=%d` を出力するが、`HostPort`（`internal/sandbox/sandbox.go:29`）はVM経路のどこからも代入されない。ラベルの `container=` もコンテナ実行時代の名残。ADR-0044 は `Handle` 型を共有基盤として残したが、`HostPort` がVMでは死にフィールドになる点には触れていない
