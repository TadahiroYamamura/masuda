# ADR-0056: VMホストのネットワーク設定は、セットアップスクリプト自身を`--runtime-only`で再実行するsystemd oneshot unitで永続化する

## Status

Accepted (2026-08-31)

## Context

[[0048-vm-network-shared-bridge-dynamic-tap-privileged-helper]]が確立したホスト共有インフラ——bridge`br-masuda0`とそのIP、`net.ipv4.ip_forward`、MASQUERADE・FORWARD・REDIRECTのiptablesルール一式——は、**すべてカーネルのランタイム状態**であり再起動で消える。再起動後も残るのはファイルとパッケージだけ（`masuda-net-helper`とその`cap_net_admin`、`$DATA_HOME`のvmlinuz、`/etc/dnsmasq.d/masuda-vm.conf`、aptで入れたもの）。結果として`scripts/setup-vm-host.sh`の冒頭が謳う「One-time host setup」は実際には一度きりではなく、電源を入れ直すたびに再実行が必要だった。

この開発ホストはWSL2であり、`wsl --shutdown`だけでなくウィンドウを閉じた後のアイドル停止でもVMごと落ちる。実機のワークステーションより頻度が桁違いに高い。

外形から分かりにくい中間状態も生む。`/etc/dnsmasq.d/masuda-vm.conf`はファイルなので残り、dnsmasqのsystemdサービスは起動時に立ち上がろうとするが、`br-masuda0`がまだ無いためbindに失敗する。「設定ファイルはあるがDHCPは死んでいる」という状態になる。

実害は2度出ている。2026-08-20（Issue #31のM4）に、前日セットアップ済みの環境で電源を落としたところ翌日`br-masuda0`が存在せず、`masuda-net-helper`の生エラーで止まった。2026-08-31にも、GitHub Issue #43の実機検証の前提を整える段で同じ状態に突き当たっている——このときは症状がTAP作成の失敗としてしか現れず、原因がホストセットアップの消失であると特定するまでに時間を要した。

GitHub Issue #40がこれを起票しており、その本文はディストリ標準の永続化機構（systemd-networkdの`.netdev`/`.network`、`netfilter-persistent`、`/etc/sysctl.d/`）への書き出しを提案していた。

## Decision

**永続化の実体は、セットアップスクリプト自身をboot時に再実行するsystemd oneshot unitとする。** 設定を別形式へ書き出すのではなく、既にある冪等なスクリプトを呼び直す。

### スクリプトを2モードに分ける

`scripts/setup-vm-host.sh`に`--runtime-only`を追加し、カーネルが忘れる3ステップ（`step_network`・`step_egress_filtering`・`step_dnsmasq`）だけを実行できるようにする。dnsmasqのapt導入は`step_dnsmasq_install`として切り出し、boot経路がaptに触れないようにした——パッケージの導入にはネットワークが要るが、それはこのスクリプトがこれから用意するものである。

環境依存の前提もモード別に分ける。`$HOME`を要する変数（`DATA_HOME`・`NET_HELPER`）と`go`/`cloud-hypervisor`/`virtiofsd`の`require_cmd`は、それらを実際に使うフル実行のみが要求する。

### フル実行がunitを設置する

`step_boot_unit`が`/etc/systemd/system/masuda-vm-host.service`を書き、`systemctl enable --now`する。`ExecStart`は実行時に`readlink -f "$0"`で解決した絶対パス＋`--runtime-only`、`WorkingDirectory`はそこから導いたリポジトリルート（スクリプトが冒頭でリポジトリルート以外での実行を拒否するため）。`Type=oneshot`・`RemainAfterExit=yes`・`After=`/`Wants=network-online.target`。

`RemainAfterExit=yes`により`systemctl status`が「ホストはセットアップ済みか」に答えるようになる（`active (exited)`＝適用済み、`inactive`＝未適用）。その代わり適用済みの状態では`start`がno-opになるため、再適用の動詞は`restart`である。`enable`ではなく`enable --now`にしたのは、そうしないとセットアップ済みのホストでunitが`inactive`のまま座り、この`RemainAfterExit`を付けた理由そのものと矛盾するため。

### masuda側は不在を検知して案内する

`internal/sandbox.EnsureTap`の入口で`requireBridge`がbridgeの存在を`net.InterfaceByName`で確認し、無ければ`sudo systemctl restart masuda-vm-host`を名指しで案内するエラーを返す。VMサンドボックスと使い捨て特権VMの両方がこの1関数を通る。

## Alternatives Considered

- **Issue #40自身が提案していたディストリ標準への書き出し（systemd-networkdの`.netdev`/`.network`＋`netfilter-persistent`＋`/etc/sysctl.d/`）**: 決め手はこのホストがWSL2であることだった。`systemd-networkd`は現状disabledで、有効化するとWSL2自身が管理する`eth0`をnetworkdが奪いに行き、masudaと無関係なホストのネットワークを壊しうる。加えてiptablesルールの正がスクリプトと`iptables-save`の写しの2箇所になり、スクリプトを直したときに写しが古いまま残る二重管理を生む。さらに現在のスクリプトの冪等性は「毎回ランタイム状態を作り直す」ことで成立しており、宣言的な設定ファイルを置く形に変えると、既存ホスト設定との衝突（NetworkManager管理下のインターフェース、別プロジェクトのiptablesルール）を新たに考慮する必要が出る
- **unitがフルのスクリプトを実行する**: `systemctl restart`ひとつで全部済む利点があったが、boot経路に`apt-get`と`go build`が乗る。作業ツリーがコンパイルできない状態で再起動すると`masuda-net-helper`のビルドが失敗してunitが落ち、ホストのネットワークが上がらない——マシンのネットワークをソースチェックアウトの状態に縛ることになる。加えて再ビルドのたびに`setcap`が失われて付け直しになる無駄が毎起動発生する
- **永続化せず、masuda側の検知と案内だけを入れる**: Issue #40自身が「別解」として挙げていたもので、実装は小さく永続化とは独立に有効。ただし再セットアップの手間は残る。実際には両方採った——案内はunitが未設置のホストでも、boot時にunitが失敗したときにも必要であり、永続化の代替ではなく補完だと判断した

## Consequences

- 再起動後の手作業が無くなる。残る手作業は初回・スクリプト変更後・`masuda-net-helper`再ビルド後（capabilityが失われるため）の3つで、これは元の「One-time host setup」という謳い文句が実際にそうなることを意味する
- `systemctl status`・`journalctl -u`が使えるようになり、Issue #40が指摘する「設定ファイルはあるがDHCPは死んでいる、外形からは分かりにくい状態」がunitの状態として観測できる
- masudaが`/etc/systemd/system`へ書き込むようになった。sudoを打つのは人間のまま（スクリプト経由）で、`scripts/setup-vm-host.sh`のヘッダが引く「masuda自身は決してsudoを打たない」線の内側ではあるが、書き換える範囲は以前より広い。後片付けは`systemctl disable`とunitの削除の2手
- `ExecStart`にリポジトリの絶対パスを焼き込むため、リポジトリを移動するとunitは存在しないパスを指す。フル実行が毎回unitを書き直すことで追随する
- unitはrootかつsystemdの最小環境（`$HOME`なし、既定PATHのみ）で走る。スクリプトのトップレベルがそれに耐える必要があり、実際にこの制約で2度失敗した（`HOME: unbound variable`、`go not found on PATH`）。今後トップレベルに環境依存を足すときは同じ制約がかかる
- **boot時に実際に発火するかは、この決定を記録した時点では未検証である。** 手動の`systemctl restart`が成功するところまでは確認済み。`step_network`は`ip route show default`でNATの出口インターフェースを決めるため、`network-online.target`がWSL2でどこまで当てになるかが残るリスクで、次回のホスト起動時に`systemctl status masuda-vm-host`で確認する必要がある
