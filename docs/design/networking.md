# ネットワーク

VMゲストとホストの間の到達性を扱う。ホスト共有のbridge+NATとワークスペースごとの動的TAPに分離し、CAP_NET_ADMINは専用ヘルパーバイナリに隔離している（ADR-0048）。セットアップとして何を実行するかは`docs/INSTALLATION.md`を参照——ここに書くのは仕組みだけ。

## TAPデバイス管理・ゲストIP解決

### 命名・アドレス

- `TapName(id)`はワークスペースIDから決定的なTAPデバイス名を導出する: `"tap-" + sanitize(id)`（`internal/sandbox/vmnet.go:29-31`。sanitizeは`nameSanitizer`、`[^a-zA-Z0-9_.-]+`を`-`に置換）。
- `MACFor(id)`はワークスペースIDから決定的なMACアドレスを導出する: `52:54:00:xx:xx:xx`（先頭3バイトは固定、残り3バイトは`sha256(id)`の先頭3バイト、`internal/sandbox/sshattach.go:19-22`）。

いずれもID一つにつき値一つで、他の状態（実行中かどうか等）を参照しない純粋な導出関数。

### 確保・解放・クラッシュ復旧

- `EnsureTap(id, bridge, ownerUser)`（`internal/sandbox/vmnet.go:39-48`）がTAP確保の唯一の入口。呼ぶたびに必ず`delete-tap`→`create-tap`の順で実行する。同じ名前の既存TAP（前回実行のクラッシュ等で残ったもの）があれば先に消してから作るため、この一つの関数呼び出しが「確保」と「クラッシュ復旧」を兼ねる。`VMBackend`の起動処理（`internal/sandbox/vmbackend.go:259`）から呼ばれる。
- `ReleaseTap(id)`（`vmnet.go:52-55`）は`delete-tap`のみを行う。既に存在しない場合はエラーにならない。`VMBackend`の停止処理（`vmbackend.go:429`）から呼ばれる。
- `EnsureTap`・`ReleaseTap`はどちらもnetlink操作を直接行わず、別バイナリ`masuda-net-helper`を`exec.Command`で起動して結果を待つだけ（`runNetHelper`、`vmnet.go:57-69`）。`masuda-net-helper`がPATH上に無ければ、その旨のエラーで即座に失敗する。

### `masuda-net-helper`（`cmd/masuda-net-helper/main.go`）

CAP_NET_ADMINを要するTAP操作だけを行う専用バイナリ。サブコマンドは2つのみ。

- `create-tap <name> <bridge> <owner-user>`
- `delete-tap <name>`

すべてのTAP/bridge操作は`github.com/vishvananda/netlink`をプロセス内で直接呼ぶ形で行い、`ip`コマンドは呼ばない。

- **所有者マーカー**: このヘルパーが作るTAPは全て、インターフェースの`alias`に`"masuda-managed-tap"`（`tapAliasMarker`）を設定する（`main.go:80-84`の`isMasudaTap`が判定に使う）。`create-tap`・`delete-tap`はいずれも、名前が既存で自分のマーカーが付いていない場合は操作を拒否してエラーを返す——名前が衝突しているだけの無関係なリンクを黙って削除・再利用しない。
- `create-tap`（`main.go:94-166`）: 名前が既存かつ自分のマーカー付きなら何もせず成功する（冪等）。新規作成時は、指定bridgeへ`MasterIndex`で接続し、`owner-user`のuid/gidをTAPの`Owner`/`Group`に設定し（`Group`は`owner-user`自身のプライマリgid。netlinkの`LinkAdd`は`TUNSETGROUP`を必ず発行するため未設定のままにはできない）、フラグに`TUNTAP_DEFAULTS | TUNTAP_NO_PI`を使い、alias設定後に`LinkSetUp`でリンクを起動する。
- `delete-tap`（`main.go:175-186`）: 名前が存在しなければ何もせず成功する。存在してマーカーが無ければエラーを返す。

### ゲストIP解決

masuda自身はゲストIPを割り当てない。ゲストは起動時にDHCPでIPを取得し、そのリースをdnsmasqのリースファイルから読み取る。

- `LookupGuestIP(mac, leaseFilePath, timeout)`（`internal/sandbox/sshattach.go:36-51`）は`leaseFilePath`を`guestIPPollInterval`（200ms）間隔で`timeout`まで読み直し、`mac`に一致するリースが現れた時点でそのIPを返す。
- リースファイルの1行のフォーマットは`<expiry-epoch> <mac> <ip> <hostname-or-*> <client-id-or-*>`（`findLeaseIP`、`sshattach.go:56-71`、大文字小文字を区別せずMACを比較）。
- 実運用でのリースファイルパスは`/var/lib/misc/masuda-dnsmasq.leases`（`internal/sandbox/vmbackend.go`の`vmDHCPLeaseFile`定数。`scripts/setup-vm-host.sh`が書き出すdnsmasq設定の`dhcp-leasefile`と一致）。
- 呼び出し元ごとにタイムアウトが異なる: VM起動待ち（`vmStart`）は`vmBootTimeout`（30秒）、`masuda chat`用の`AttachArgs`は`vmDHCPTimeout`（20秒）、`vmStop`の正常シャットダウンSSHは2秒（ゲストが既に落ちている可能性があるためベストエフォート）。

### SSH接続

- `SSHAttachArgs(guestIP, privateKeyPath)`（`sshattach.go:90-92`）は`masuda chat`がゲストのtmuxセッションへ対話的にアタッチするためのargvを返す（`ssh ... tmux attach -t <session>`）。
- 接続オプションは`StrictHostKeyChecking=no`・`UserKnownHostsFile=/dev/null`固定（`sshBaseArgs`、`sshattach.go:98-107`）。クライアント認証は秘密鍵（`privateKeyPath`）側で行われ、ホスト鍵検証はしない。
- 秘密鍵の生成・配置（`EnsureSSHKeypair`）自体は`docs/design/sandbox-vm.md`の範囲。

## MCPリレー（TCP↔UDS中継）

### 存在理由（機構のみ）

Claude Codeの`--mcp-config`は`http://host:port`形式のURLしか受け付けず、Unix domain socketを直接指定できない。一方、Claudeが実際に呼ぶ`mcp__masuda-gate__wait_for_gate_change`等のcurated tool set（ADR-0042）は、ワークスペースごとの状態デーモンがUnix domain socket（`statedaemon.CuratedSocketPath`）でのみ提供する。この間を橋渡しするのが`masuda internal mcp-relay`。

### 中継本体

- `masuda internal mcp-relay --socket <path> --bind <addr> --port <port>`（`cmd/masuda/mcprelay.go`、隠しサブコマンド）は`bind:port`でTCP待受し、接続を受けるたびに`socketPath`へダイヤルして双方向に`io.Copy`する単純なバイト中継（`runMCPRelay`/`relayConn`、`mcprelay.go:65-104`）。`--bind`のデフォルトは`127.0.0.1`。
- `internal/sandbox.StartMCPRelay(socketPath, bind, port, logPath)`（`internal/sandbox/mcprelay.go:47-78`）は、現在のmasudaバイナリ自身を`masuda internal mcp-relay ...`として再exec（自己exec）する形でこれをバックグラウンド起動し、標準出力/標準エラーを`logPath`へ流す。起動後、`mcpRelayStartupTimeout`（5秒）以内にTCP待受が開始するのを確認してから返る。`MCPRelayProcess.Stop()`（`mcprelay.go:82-84`）はpidファイル経由でプロセスを終了する。

### 呼び出し元による違い

MCPリレーは2箇所から起動され、bindアドレスとportの決め方が異なる。

- **ホストループ側**（Discovery/Blueprint段階、`internal/hostloop.startMCPRelay`、`internal/hostloop/hostloop.go:238-263`）: `127.0.0.1`にbindし、`freeTCPPort()`（`hostloop.go:214-221`。`127.0.0.1:0`で一時的にリスンして空きポート番号だけを取得しすぐ閉じる）で毎回のStart呼び出しごとに新たに空きポートを選ぶ。同一ワークスペースの前回起動が残したリレーとの重複排除は行わない（`Start()`のIsRunningガードにより、生きているtmuxセッションが無い時しかこの経路は動かないため）。得られたポートは`mcpConfigJSON(relayPort)`（`hostloop.go:201-208`）が`--mcp-config`のJSONへ埋め込む。per-serverの`"timeout"`には`mcpToolTimeoutMillis`（7日、`hostloop.go:191-199`）を設定する。
- **サンドボックスVM側**（Build/Review段階、`VMBackend.vmStart`、`internal/sandbox/vmbackend.go:309-322`）: リレーはゲスト内ではなく**ホスト側**で動く——virtiofsはUnix domain socketのスペシャルファイルをカーネルをまたいで共有できないため、ゲスト側にブリッジ元となるローカルソケットがそもそも存在しない。bindアドレスはループバックではなくブリッジのゲートウェイIP（`vmBridgeGatewayIP`＝`192.168.200.1`）、ポートはホストループ側と同じ`freePort()`（`internal/sandbox/sandbox.go:43`）で都度選ぶ。選んだ`<ブリッジゲートウェイIP>:<port>`はcloud-hypervisorの`--cmdline`に`masuda.mcp_relay=<addr>`として渡す（`vmbackend.go:348-349`）。ゲスト内の`runtime/entrypoint.sh`・`runtime/start_claude.sh`はこのカーネルコマンドライン引数を`/proc/cmdline`から`sed`で読み取り、その値をそのまま自分の`--mcp-config`に使う——ゲスト内でリレープロセスが動くことはない。
- **ポートの永続化**: VM側は選んだポートを`workDir/mcp-relay.port`（`vmRelayPortFile`）に書き出す（`vmbackend.go:337`）。`masuda sandbox stop`は起動時の`*MCPRelayProcess`を持たない別プロセスとして実行されるため、このファイルを読んでkillすべきアドレスを再構成する（`vmbackend.go:424-427`）。ホストループ側には同等のファイルは無い——そのリレーはホストループプロセス自身の子プロセスとして存在し、明示的な永続化なしにホストループの終了と運命を共にする。

## VMホスト一次セットアップ

`scripts/setup-vm-host.sh`は再実行しても安全な冪等スクリプト。**masuda自身のコードはこのスクリプトを呼ばない**——sudoを要する手順は人間が明示的に一度（またはmasuda-net-helperを再ビルドしてcapabilityが失われた時に再度）実行するものとして切り離されている。実行コマンド自体は`docs/INSTALLATION.md`「3.5. VM実行基盤のセットアップ」節を参照。ここでは各ステップが何をするかだけを示す（`setup-vm-host.sh:278-284`の実行順、7ステップ）。

- **`step_rootfs_build_deps`**（`:56-67`）: `fakeroot`・`mkfs.ext4`（`e2fsprogs`）が無ければaptでインストールする。`internal/rootfs.Build`が使う。
- **`step_kernel`**（`:72-90`）: `/boot/vmlinuz-*-generic`の最新版を探し（無ければ`linux-image-generic`をaptでインストールしてから再取得）、`$XDG_DATA_HOME/masuda/vmlinuz-<version>`へmode 0644・実行ユーザー所有でコピーする。
- **`step_network`**（`:96-129`）: bridge `br-masuda0`（`192.168.200.1/24`）が無ければ作成・起動し、`net.ipv4.ip_forward=1`を設定する。デフォルトルートのインターフェースを`ip route show default`から検出し、`192.168.200.0/24`向けのoutbound NAT（MASQUERADE）とFORWARDルールを`iptables`へ追加する（`iptables -t nat -C`での存在チェック込みの冪等）。
- **`step_egress_filtering`**（`:156-212`）: ブリッジ発のTCP 443を`masuda-egress-proxy`へREDIRECTするiptablesルール、およびDNS（UDP/TCP 53）以外のブリッジegressを制限するFORWARDルールを設定する。仕組みの詳細・関連ADRは`docs/design/egress-filter.md`を参照
- **`step_net_helper`**（`:219-225`）: `cmd/masuda-net-helper`を`~/.local/bin/masuda-net-helper`へ`go build`し、`sudo setcap cap_net_admin+ep`を無条件に毎回適用する。
- **`step_egress_proxy`**（`:231-237`）: `cmd/masuda-egress-proxy`を`~/.local/bin/masuda-egress-proxy`へ`go build`する（setcap不要、詳細は`docs/design/egress-filter.md`）。
- **`step_dnsmasq`**（`:242-276`）: `dnsmasq`が無ければaptでインストールする。`/etc/dnsmasq.d/masuda-vm.conf`を次の内容で書く（既存内容と一致していれば書き換えない）: `interface=br-masuda0`・`bind-interfaces`・`except-interface=lo`・`dhcp-range=192.168.200.10,192.168.200.200,12h`・`dhcp-leasefile=/var/lib/misc/masuda-dnsmasq.leases`。書いた後`systemctl enable --now dnsmasq`・`systemctl restart dnsmasq`を実行する。

スクリプト冒頭の`require_cmd`（`:46-51`）は`sudo`・`ip`・`iptables`・`go`・`cloud-hypervisor`・`virtiofsd`の存在を確認するのみで、後二者（標準aptパッケージが無い）は自動インストールしない。

## 既知の問題

未調査。修正時はここから消す。

- **`entrypoint.sh`・`start_claude.sh` に到達不能なDockerパス分岐が残っている**: `runtime/entrypoint.sh:17-29` と `runtime/start_claude.sh:20-23` は、`masuda.mcp_relay=` カーネルコマンドライン引数が見つからない場合に固定ポート39217でリレーを自前起動するフォールバックを持つ。しかし `VMBackend.vmStart`（`internal/sandbox/vmbackend.go:309-322`）は常にこの引数を設定するため、この分岐には到達しない。rootfsビルドが使う `docker create` は ENTRYPOINT を実行しないので、そちらからも到達しない。ADR-0044 でDocker実行基盤を削除した際の掃除漏れと見られる
