# Egressフィルタ

サンドボックスVMの外向きTLS通信をSNIホスト名で選別する仕組み。実装は`internal/egressproxy`（プロキシ本体）・`cmd/masuda-egress-proxy`（実行バイナリ）・`internal/sandbox/egressproxy.go`（起動・allowlist解決）、CLIは`cmd/masuda/egress.go`。

パケットがVMブリッジからこのプロキシへ届くまでの経路（iptables）は`scripts/setup-vm-host.sh`が一度きりでセットアップする、`docs/design/networking.md`が扱うホスト共有インフラの一部。bridge・NAT・TAP自体の仕組みは`networking.md`を参照。`.masuda/settings.json`の`egressAllowlist`フィールド自体のスキーマは`docs/design/config.md`を参照（本書は宣言をVM側でどう解決・強制するかのみ扱う）。

## 何を遮断し何を通すか

`internal/egressproxy.Proxy`はTLSを終端しない。各接続のTLS ClientHelloを`ExtractSNI`（`internal/egressproxy/sni.go:51-71`）でパースし、`server_name`拡張（RFC 6066 §3）が宣言するホスト名だけを見て通す/切るを決める。ClientHello本体の中身（証明書・アプリケーションデータ等）には一切触れない。

- **デフォルト拒否、フォールバック無し**: ClientHelloとして正しく読めない接続（`ErrNotTLS`）、SNI拡張が無い接続（`ErrNoSNI`）、SNIホスト名が許可リストに無い接続は、いずれも即座にクローズされる（`Proxy.handle`、`proxy.go:99-131`）。TLS以外のプロトコル・SNI無しのTLSを素通しする経路は存在しない
- **実宛先はSNIホスト名から決まる**: クライアントが実際にダイヤルした宛先IPは使わない。`dest, err := net.DialTimeout("tcp", net.JoinHostPort(hostname, p.destPort()), ...)`（`proxy.go:114`）がSNIホスト名そのものを解決・接続する。iptables REDIRECTが元の宛先IPを書き換えても（後述）プロキシの動作に影響しないのはこのため。クライアント側がSNIホスト名を偽って実際には別サーバーへ到達する、という迂回はできない
- **ClientHelloの再送**: `ExtractSNI`が読み取った生バイト列（`raw`）は接続からは既に消費済みなので、許可判定後に`dest.Write(raw)`（`proxy.go:124`）で実宛先へそのまま書き戻してからバイトリレーを始める。以降は`relay`（`proxy.go:148-162`）が単純な双方向`io.Copy`
- **1レコードの上限**: `ExtractSNI`は1つのTLSレコード（`maxRecordLength`＝2^14バイト、`sni.go:32`）しか読まない。ClientHelloが複数レコードにまたがる場合はエラー（`ErrNotTLS`）としてクローズする
- **443以外・TLS以外のポート**: iptables側でREDIRECTの対象は`--dport 443`のTCPのみ（後述）。80番などの平文HTTPやその他のポート宛の接続はこのプロキシに一切届かない。ブリッジのFORWARDチェーンにはDNS（UDP/TCP 53）向けのACCEPTルールしか無く（後述）、443以外の宛先ポートに対する明示的なACCEPTルールが無いため、ホストのFORWARDチェーンのデフォルトポリシー任せになる——`setup-vm-host.sh`自身はこのデフォルトポリシーを変更しない

## パケットがプロキシへ届く経路

`scripts/setup-vm-host.sh`の`step_egress_filtering`（156-212行目）がiptablesルールを設定する。REDIRECT方式を採用した理由（TPROXYが機能しなかった経緯）は`docs/design/networking.md`ではなく本書の関連ADR節を参照（ADR-0045）。

- **nat/PREROUTING**: `-i br-masuda0 -p tcp --dport 443 -j REDIRECT --to-port $EGRESS_PROXY_PORT`（`setup-vm-host.sh:191-192`）。ブリッジから来たTCP 443宛のパケットの宛先アドレスを、受信インターフェース自身のアドレスへその場で書き換える。書き換え後のルーティング決定は「ローカル宛のパケット」という通常の経路をたどるため、FORWARDチェーンを経由せずINPUTチェーン側で配送される——REDIRECTを使う一番の理由がこれで、ゲスト側から見た宛先IPが何であっても、443番であれば必ずこのプロキシのリスニングソケットに届く
- **filter/FORWARD**: `-i br-masuda0 -o $uplink -p udp --dport 53 -j ACCEPT`・同tcp版（`setup-vm-host.sh:208-209`）のみを追加する。REDIRECTされる443番はそもそもFORWARDチェーンを通らないため、ここにルールは無い。RELATED,ESTABLISHEDの戻りトラフィックは`step_network`が既に許可済み（`docs/design/networking.md`）
- **`$EGRESS_PROXY_PORT`（39218）はホスト全体で固定**: `internal/sandbox.egressProxyPort`（`internal/sandbox/egressproxy.go:40`）と`setup-vm-host.sh`冒頭の`EGRESS_PROXY_PORT`変数の両方に同じ値がハードコードされており、片方だけ変更すると壊れる。REDIRECTルール自体がホスト起動時に静的に設定される固定ルールのため、動的な検出は行わない
- 再実行時、`step_egress_filtering`は旧TPROXY構成の残留物（mangleテーブルのDIVERTチェーン・TPROXYルール・`ip rule`のfwmarkエントリ）を検出して削除するクリーンアップも行う（`setup-vm-host.sh:167-187`）

関連ADR: [ADR-0045](../adr/0045-redirect-over-tproxy-for-egress-interception.md)（TPROXYではなくREDIRECTを使う理由、旧per-workspaceプロキシ案の却下理由）。

## プロキシプロセス本体

`masuda-egress-proxy`（`cmd/masuda-egress-proxy/main.go`）はホスト全体で1プロセスだけ動く常駐バイナリで、ワークスペースごとには起動しない。`--bind`（デフォルト`vmBridgeGatewayIP`＝`192.168.200.1`）・`--port`（`39218`固定）で`net.Listen`するだけの素の待受ソケットで、REDIRECTが宛先を書き換えてくれるため特別なcapabilityは不要。

- `internal/sandbox.EnsureEgressProxy()`（`egressproxy.go:70-113`）が`VMBackend.Start`（`vmbackend.go`のvmStart）から毎回呼ばれる。`net.DialTimeout`で待受ポートへの接続を試み、成功すれば「既に起動済み」として何もしない。起動していなければ`exec.LookPath`でバイナリの存在を確認し、`workspace.DataHome()/egress-proxy.pid`・`egress-proxy.log`にpidfile/ログを出しながらバックグラウンド起動し、`egressProxyStartupTimeout`（5秒）以内にリスニングを確認する
- `VMBackend.Stop`（vmStop）はこのプロセスを一切止めない。他のワークスペースのVMが依存し続けている可能性があり、かつワークスペースごとの参照カウントを持たないため。ホストの再起動か、人間が手動でkillするまで動き続ける
- 1接続ごとに`go p.handle(conn)`（`proxy.go:95`）でgoroutineを起こす。接続元IP（`remoteIP`、`proxy.go:137-143`）が`*net.TCPAddr`として取れない場合は拒否扱いになる

## 宣言と承認の分離

`.masuda/settings.json`の`egressAllowlist`（宣言、対象リポジトリがコミット）と`.masuda/settings.local.json`の`egressAllowlist`（承認、gitignore対象）の両方に同じホスト名が載っている場合だけ、そのホスト名への接続が許可される。スキーマ自体は`docs/design/config.md`を参照——本書が扱うのは、この2つのファイルをVM起動時にどう突き合わせるかという実行時の解決ロジック。

- `internal/sandbox.resolveEgressAllowlist(repoRoot)`（`vmbackend.go:189-208`）が`config.Load`（宣言）と`config.LoadLocal`（承認）を両方読み、`local.EgressAllowlist`に含まれる`cfg.EgressAllowlist`のホスト名だけを返す（積集合）。どちらのファイルも存在しなければエラーにせず空を返す
- `internal/sandbox.NewEgressAllowlistFunc()`（`egressproxy.go:153-165`）が`egressproxy.AllowlistFunc`の実体。接続ごとの`clientIP`を`ResolveWorkspaceByIP`（`egressproxy.go:128-143`）でワークスペースへ逆引きし（dnsmasqのリースファイルからMACを引き、`MACFor(id)`と一致するワークスペースを`workspace.ListAll()`から探す）、そのワークスペースの`repoRoot`で`resolveEgressAllowlist`を呼ぶ。ワークスペースに一致しなかったMACは、実行中の使い捨て特権VMのレジストリ（`$XDG_DATA_HOME/masuda/privileged-vms/<mac>`）へフォールバックして引く——そのVMもワークスペースの`egressAllowlist`をそのまま使う（`docs/design/privileged-commands.md`）。**この関数呼び出し自体は接続のたびに毎回ディスクから読み直す**（キャッシュを持たない）ため、`masuda egress approve/reject`の反映に理論上VM再起動は不要——ただし`cmd/masuda/egress.go`の`approve`コマンド自身は`restart this workspace's VM to pick it up`と表示する（下記「触る人が事故る制約」参照）
- clientIPがどのワークスペースにも一致しない場合（不明なリース、削除済みワークスペースの古いリース等）は拒否。ホスト全体で共有されるデフォルト許可リストは無い
- **`mcpServers`（ADR-0043）との違い**: `MCPServerDecl`と対になる`MCPServerApproval`は宣言のフィンガープリント（`DeclHash`）を承認に紐付け、宣言が承認後に書き換わったら再承認を要求する。`egressAllowlist`にはこの紐付けが無い——ホスト名エントリ自体には`Command`/`Args`/`Env`のような別途変化しうるペイロードが無く、宣言側のエントリが変われば承認側と文字列として一致しなくなる（＝それ自体が「未承認」と同じ扱いになる）ため、ハッシュで守るべき対象がそもそも存在しない

## DNSの扱い

DNS解決自体はホスト名で制限しない。ブリッジのFORWARDチェーンはUDP/TCP 53を無条件にuplinkへ通す（前述）ため、許可リストに無いホストのAレコードも普通に引ける。実際に効くのは、そのホストへTLS接続（443番）を試みた時点でのSNIチェックのみ。

ゲスト内部の名前解決は`runtime/resolv-conf.service`が担う（ADR-0051）。

- ゲストのrootfsに焼き込まれた`/etc/resolv.conf`はDockerビルドホスト由来の値で、VM内では意味を持たない。このunitは起動時に`/etc/resolv.conf`を削除し、`/run/systemd/resolve/stub-resolv.conf`へのシンボリックリンクに差し替える（`ExecStart`、`resolv-conf.service:42`）
- 実際にDNSサーバーのアドレス（ブリッジのゲートウェイIP）を教えるのは`systemd-networkd`のDHCPクライアント（`runtime/vm-dhcp.network`、`docs/design/networking.md`参照）。このunitはstub resolverへの向き先を切り替えるだけで、DNSサーバー自体の設定は行わない
- `DefaultDependencies=no`・`WantedBy=sysinit.target`で`systemd-resolved.service`と同じ層に置く（`resolv-conf.service:36-37,45`）。`multi-user.target`側から`systemd-resolved.service`をBefore=で待たせようとすると起動順序が循環し、resolvedの起動ジョブごと削除される

## CLI: `masuda egress`

`cmd/masuda/egress.go`。対象リポジトリのルートを直接操作する（`mcp`コマンドと同じくワークスペース単位ではない）。

- **`list`**: `cfg.EgressAllowlist`の各ホスト名について、`local.EgressAllowlist`に含まれていれば`approved`、無ければ`not approved`を表示する（`egress.go:32-66`）。宣言が空なら「declared in ... なし」の1行のみ出力する
- **`approve <hostname>`**: `cfg.EgressAllowlist`に宣言されていないホスト名はエラー。既に承認済みなら何もせず成功扱い。未承認なら`local.EgressAllowlist`へ追記して`config.SaveLocal`し、`warnIfNotGitignored`で`.masuda/settings.local.json`がgitignore対象かを確認する（`egress.go:68-103`）
- **`reject <hostname>`**: `local.EgressAllowlist`から該当ホスト名を削除する。元々承認されていなければ何もしない旨だけ出力する（`egress.go:105-129`）

## 触る人が事故る制約

- `EGRESS_PROXY_PORT`は`setup-vm-host.sh`と`internal/sandbox/egressproxy.go`の2箇所にハードコードされている。片方だけ変更するとREDIRECT先とプロキシの待受ポートがずれ、全接続がタイムアウトする
- `masuda egress approve`は`restart this workspace's VM to pick it up`と表示するが、`resolveEgressAllowlist`は接続のたびにディスクから読み直す純粋な解決ロジックであり、明示的なキャッシュは無い。VM再起動が本当に必要かどうかはこのメッセージだけでは判断できない
- 443以外のポート（平文HTTPの80番等）は、REDIRECT・FORWARD ACCEPTのいずれの対象にもなっていない。ブリッジからの到達可否はホストのFORWARDチェーンのデフォルトポリシー（`setup-vm-host.sh`は設定しない）に依存する
- **egress関連のコードを変更したら、`masuda-egress-proxy`プロセスを手動でkillしてから再起動すること。** `EnsureEgressProxy`は「既にリスンしているか」だけを見る冪等性しか持たず、バイナリのビルド日時を見ないため、`scripts/setup-vm-host.sh`（バイナリを再ビルドするだけ）を実行しても既存プロセスは古いロジックのまま動き続ける。`VMBackend.Stop`でも止まらないので、pidfile（`workspace.DataHome()/egress-proxy.pid`）を手動でkillするかホストを再起動する必要がある。踏むと「実装したはずの許可判定が効かず常に拒否される」といった、コードを読んでも原因が分からない形で出る（`orchestrator/`・`runtime/`変更後のイメージ再ビルドと同種の罠——`docs/design/images-and-rootfs.md`）

## 実機検証

`internal/sandbox/zz_manual_egress_filter_test.go`の`TestManualEgressFiltering`が、REDIRECTルールと`masuda-egress-proxy`がゲストのTLS通信を実際に横取りし、宣言/承認の許可リストをend-to-endで強制することを確認する（Issue #11 M3〜M5）。宣言・承認の両方に含まれるホスト名へは到達でき、どちらにも無いホスト名は拒否される、という2点を実VMで検証する。

環境変数`MASUDA_MANUAL_VM_TEST=1`を設定したときだけ実行され、通常の`go test ./...`ではskipされる（実VMの起動とホスト側の一次セットアップ完了を前提とするため）。実行前に上記の「触る人が事故る制約」——特に`masuda-egress-proxy`の手動再起動——を確認すること。

`step_egress_filtering`は、ブリッジ発のFORWARDトラフィックに対する明示的なcatch-all DROPルール（`-i $BRIDGE -j DROP`、DNS ACCEPTルールの後に追加）を持つ。これが無いと「443・DNS以外はすべて既定で拒否」という保証は、このホストの FORWARD チェーンがたまたまDROPを既定にしている（Dockerのインストールが設定する副作用）ことに依存してしまい、masuda自身のスクリプトが保証するものではなくなる——実機で80番ポートへの到達がこのルール追加前後どちらでも拒否されることを確認済み（前者はDockerの副作用、後者はmasuda自身のルールによる）。
