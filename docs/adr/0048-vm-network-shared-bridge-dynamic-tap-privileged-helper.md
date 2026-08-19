# ADR-0048: VMネットワークはホスト共有のbridge+NATとワークスペースごとの動的TAPに分離し、CAP_NET_ADMINは専用ヘルパーバイナリに隔離する

## Status

Accepted (2026-08-19)

## Context

> 本ADRはIssue #31の作業中に下された判断を、コードおよび`scripts/setup-vm-host.sh`のコメントから再構成したものである。判断が下された時点のセッションに立ち会っていないため、Context・Decision・Alternatives
> Consideredはいずれもコード・コメントの記述内容のみを根拠とする。

[[0044-remove-docker-execution-runtime-vmbackend-only]]でサンドボックスの実行基盤がCloud Hypervisor
microVM（`VMBackend`）に一本化された。VMがゲストとして動作するには、ホスト側にゲストを外部ネットワークへ到達させるための橋渡し（bridge・NAT・DHCP）と、ワークスペースごとにVMへ割り当てるネットワークインターフェース（TAP）の両方が必要になる。

TAPデバイスの作成・削除にはLinuxの`CAP_NET_ADMIN`capabilityが要る。masuda自身は`scripts/setup-vm-host.sh`のコメントにあるとおり「masudaの own code never runs sudo on its own initiative」という原則を持ち、実行時に自己昇格しない設計になっている。この原則と、複数ワークスペースが同時に走りうる（ADR-0030でワークスペースIDがbranch名から独立している設計等を参照）状況が組み合わさることで、(1) ネットワーク特権をどこに持たせるか、(2) ホスト共有インフラとワークスペース固有リソースの境界をどこに引くか、(3) ゲストIPをどう解決するか、(4) TAPの確保・解放・異常終了後の後始末をどう扱うか、という4点の設計判断が必要になった。

## Decision

### 共有bridge + ワークスペースごとの動的TAP

bridge（`br-masuda0`、`192.168.200.1/24`）とoutbound NAT（`iptables`のMASQUERADE + FORWARDルール）は、`scripts/setup-vm-host.sh`の`step_network`が一度だけ作るホスト共有インフラとして扱う。個々のワークスペース用TAPデバイスはこのスクリプトでは作らない。ワークスペースごとのTAPは、VMの起動・停止のたびに`internal/sandbox.EnsureTap`/`ReleaseTap`（`internal/sandbox/vmnet.go`）が`masuda-net-helper`を呼び出して動的に作成・破棄する。

### CAP_NET_ADMINを専用バイナリに隔離

`CAP_NET_ADMIN`は`cmd/masuda-net-helper`という小さな単一目的バイナリにのみ`setcap`で付与し、masuda本体（CLIバイナリ）には決して付けない。Linuxのファイルcapabilityはバイナリ単位で付与され、コードパス単位では絞れないため、これをmasudaの単一の大きなCLIバイナリに統合するとCAP_NET_ADMINが全サブコマンド・全サブエージェント起動ヘルパーに及んでしまう（`cmd/masuda-net-helper/main.go`パッケージdocコメント）。バイナリを再ビルドするとcapabilityは失われる（Linuxの性質でmasudaの選択ではない）ため、`scripts/setup-vm-host.sh`の`step_net_helper`はビルド後に`sudo setcap cap_net_admin+ep`を毎回無条件に実行する。

`masuda-net-helper`内部のTAP/bridge操作はすべて`github.com/vishvananda/netlink`をプロセス内で直接呼ぶ形で行い、`ip`コマンドへのシェルアウトはしない。setcapされたcapabilityはプロセス自身のeffective setにしか無く、`exec.Command`で起動する子プロセスには伝播しない（呼び出し元プロセスのinheritable setに事前に入っている必要があるが、無権限のシェルから起動した場合はそこに入っていない）ため、netlink呼び出しをcapabilityを実際に持つプロセス内で完結させている（`cmd/masuda-net-helper/main.go`パッケージdocコメント）。

### ゲストIPはDHCPで配る

ゲストVMのIPアドレスはVMごとの静的設定ではなく、`step_dnsmasq`が起動するdnsmasqのDHCPで配布する。dnsmasqは`$BRIDGE`（`br-masuda0`）にのみbindするため、ホストの他のネットワークインターフェースには影響しない。DHCPにすることで、複数ワークスペースが同時に走りうる状況でもmasuda自身が独自のIPアロケータを持たずに済む（`scripts/setup-vm-host.sh`の`step_dnsmasq`直前コメント）。

### TAP名はワークスペースID由来で決定的、確保は「削除してから作成」

`TapName(id)`（`internal/sandbox/vmnet.go`）はワークスペースIDから決定的にTAPデバイス名を導出する。`EnsureTap`はこの名前に対し、既存の同名TAP（前回実行のクラッシュ等で残ったもの）を先に削除してから新規作成する、という「削除してから作成」のシーケンスを常に踏む。この一つの機構が確保・解放・クラッシュ復旧（masudaが落ちた、あるいはホストが再起動した場合の孤児TAP）をまとめて片付け、別途プールやPID生存確認の帳簿を持つ必要が無い（`internal/sandbox/vmnet.go`の`TapName`docコメント）。

## Alternatives Considered

- **masuda本体にCAP_NET_ADMINを付与する**: masudaのはるかに大きなコードベースのどこかにあるバグからでもcapabilityへ到達できてしまうため却下。TAP操作という単一目的のためだけに、CLI全体の攻撃対象領域を広げる選択はしなかった。
- **VMごとの静的IP設定＋masuda自前のIPアロケータ**: 複数ワークスペースが同時に走りうる状況でmasuda自身がIP割り当ての整合性（重複回避・解放漏れの追跡等）を管理する必要が生じるため却下。既存のDHCP（dnsmasq）にゲストIP割り当てを完全に委譲した。
- **TAPのプール管理やPID生存確認による帳簿**: どのTAPがどのプロセスにまだ使われているかを別途追跡する仕組みは却下。ワークスペースID由来の決定的な命名＋「確保のたびに同名の既存TAPを削除してから作成する」という単純な規則だけで、正常な確保・解放・クラッシュ後の孤児TAP回収のすべてを同じコードパスが処理できるため、別立ての帳簿を持たなかった。

## Consequences

- ホスト共有インフラ（bridge・NAT・dnsmasq・`masuda-net-helper`へのsetcap）のセットアップはmasuda自身が実行時に行わず、`scripts/setup-vm-host.sh`を人間がsudo付きで明示的に一度（および`masuda-net-helper`再ビルド時は再度）実行する運用に固定される。masuda本体が自己昇格しない原則を守る代わりに、初回セットアップの自動化はmasudaのCLI操作の外に置かれる。
- `masuda-net-helper`を再ビルドするたびに`setcap`をやり直す必要があり、これを怠るとVM起動時のTAP作成がcapability不足で失敗する。`step_net_helper`が毎回無条件にsetcapし直す設計により運用上の抜け漏れは吸収されるが、再ビルド後に`setup-vm-host.sh`を再実行し忘れると気づきにくい形で壊れる。
- 全ワークスペースのVMが単一の共有bridge（`br-masuda0`）上に載るため、ワークスペース間でネットワークレベルの分離（同一L2セグメント上の到達性を相互に遮断する等）は行われていない。TAPの所有権分離とゲストOS内部の隔離のみに依拠する。
- ゲストのIP確定はDHCPリースの成立を待つ必要があり、`internal/sandbox`側はリースファイルをポーリングして解決する（`LookupGuestIP`）。VM起動直後にゲストIPが即座に分かるわけではなく、SSH接続開始までに待ち時間が発生する。
