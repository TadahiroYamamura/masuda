# ADR-0045: egressプロキシへのパケット転送はTPROXYではなくiptables REDIRECTを使う

## Status

Accepted (2026-08-19)

## Context

Issue #11 M3（VMゲストの outbound TLS を`masuda-egress-proxy`へ強制的に通す仕組み）は、当初カーネルのTPROXY機構（`tproxy(8)`）で実装した。`iptables -t mangle -A PREROUTING -i $BRIDGE -p tcp --dport 443 -j TPROXY --tproxy-mark ... --on-port ... --on-ip ...`でパケットにfwmarkを付けつつプロキシのリスニングソケットへ紐付け、`ip rule add fwmark ... lookup <table>` + `ip route add local 0.0.0.0/0 dev lo table <table>`でそのマーク付きパケットを常にローカル配送扱いにする、という標準的な構成（`internal/egressproxy.Proxy`のdocコメントに設計意図あり）。リスニングソケット側は`IP_TRANSPARENT`（`internal/egressproxy.ListenTransparent`）が必要で、これがCAP_NET_ADMINを要求するため、`masuda-egress-proxy`を`cmd/masuda-net-helper`と同様の単独setcap済みバイナリに分離していた。

この構成を`scripts/setup-vm-host.sh`で実機セットアップし、実VM（Cloud Hypervisor microVM、`internal/sandbox.VMBackend`）から`curl -4 https://example.com/`を発行して検証したところ、一貫して接続タイムアウトになった。切り分けの結果:

- `iptables -t mangle -L PREROUTING -n -v`でTPROXYルール自体は新規SYNに毎回正しくマッチしていた（カウンタが増加）
- `ip route get <dest> mark 1 from <vm-ip> iif $BRIDGE`のユーザー空間シミュレーションは常に`local ... dev lo table <table>`を正しく返した
- にもかかわらず、`sudo iptables -Z INPUT`直後から`iptables -L INPUT -n -v`のポリシーヒットカウンタは、SYNが継続的にTPROXYへマッチしている間もずっと0のままだった――つまりカーネルの実際のルーティング決定では、マークされたパケットが一度も「ローカル配送」として扱われていなかった
- 実VMを介さず、`br-masuda0`に直結した`veth`+`ip netns`だけで同じ現象が再現した(Cloud Hypervisor/virtioは無関係と判明)
- `sudo nft -a list ruleset`でraw/mangle/nat全てのPREROUTINGフックを確認したが、干渉する別ルールは無かった(Dockerのnat/rawテーブルは無関係のIP・0カウンタ)
- `rp_filter`(該当インターフェースを含め全て0)、`net.ipv4.ip_forward=1`も問題なし

このホストは`Linux 6.6.114.1-microsoft-standard-WSL2`(WSL2上のネストされた環境)であり、`ip rule show`にWSL2自身が使う`ipproto tcp/udp lookup 127/128`等の独自ポリシールーティングエントリが見えていた。原因はWSL2のネストされたカーネル/ネットワークスタック側の何かだと推測されるが、これ以上の特定はできなかった(masuda側で直せる範囲の問題ではないと判断)。

## Decision

TPROXY + fwmark + カスタムルーティングテーブルによる「ローカル配送のふり」をやめ、iptables REDIRECT(natテーブル、宛先を実際にローカルアドレスへ書き換えるDNATの一種)に切り替える。

```
iptables -t nat -A PREROUTING -i $BRIDGE -p tcp --dport 443 \
	-j REDIRECT --to-port $EGRESS_PROXY_PORT
```

REDIRECTは宛先アドレスそのものを書き換えるため、その後のルーティング決定は「本当にローカル宛のパケット」という通常のケースとして処理される――TPROXYが依存していた「マーク+ポリシールーティングでカーネルにローカル配送だと錯覚させる」ステップが丸ごと不要になり、そこが機能しない今回の環境でも問題なく動く。

`internal/egressproxy.Proxy`はもともと実宛先をIPヘッダではなくTLS ClientHelloのSNIから読み取る設計(クライアントが偽装できないようにするため)なので、REDIRECTが元の宛先IPを破壊してもプロキシの動作に影響しない。

副次的な変更:

- リスニングソケットに`IP_TRANSPARENT`が不要になったため、`internal/egressproxy/transparent.go`(`ListenTransparent`)を削除し、`cmd/masuda-egress-proxy`は素の`net.Listen`を使う
- `masuda-egress-proxy`バイナリがCAP_NET_ADMINを必要としなくなったため、`scripts/setup-vm-host.sh`の`sudo setcap cap_net_admin+ep`ステップを削除した(バイナリを単独プロセスに分離している設計自体は`cmd/masuda-net-helper`と揃える目的で維持)
- `step_egress_filtering()`に、旧TPROXY構成の残留物(mangle DIVERTチェーン、TPROXYルール、`ip rule`/`ip route`のtable 100エントリ)を検出して削除するクリーンアップを追加した(このADRが書かれた時点で既にTPROXY構成をセットアップ済みのホストが存在するため)

## Alternatives Considered

- **TPROXY構成のまま原因究明を続ける**: `veth`+`netns`での高速再現ループまで用意して切り分けたが、WSL2のネストされたカーネル内部までは踏み込めず、これ以上の追跡は費用対効果が見合わないと判断した。masudaの開発ホスト自体がWSL2という前提を踏まえると、本番相当の非WSL2環境では動く可能性はあるが、それを検証する手段がこのプロジェクトには無い
- **per-workspaceプロキシ+動的nftablesルール**: `internal/egressproxy.Proxy`のdocコメントに記録済みの、そもそもM3以前に却下していた設計。ワークスペースのライフサイクルに合わせてnftablesルールを追加/削除する必要があり、`exec.Command("iptables", ...)`はCAP_NET_ADMINがsetcap済みバイナリから子プロセスへ伝播しないため使えず、低レベルのnftables netlinkライブラリが必要になる。実機のカーネルnetfilter状態に対してしか検証できずテスト不可能という理由で不採用(この判断はREDIRECTへの切り替えでも覆らない)
- **`iptables -j DNAT --to-destination <proxy-ip>:<port>`**: REDIRECTは「受信インターフェースの自アドレス」へ自動的に書き換えるのに対し、DNATは宛先を明示的に指定する必要がある。今回のプロキシは常に単一の固定アドレス(`$BRIDGE_ADDR`)で待ち受けるため差はほぼ無いが、その固定アドレスをスクリプト側の変数と二重管理する必要が生まれるだけでREDIRECTに対する利点がないため不採用

## Consequences

- `masuda-egress-proxy`がCAP_NET_ADMIN不要になったことで、「setcap済みの小さい単独バイナリに分離する」という当初の設計上の理由(`cmd/masuda-net-helper`と同じ理由付け)が消えた。現状は`cmd/masuda-net-helper`と形を揃えるためにバイナリ分離を維持しているが、将来的に`masuda`本体のサブコマンドへ統合する余地がある(本ADRの時点では実施しない――スコープ外の判断のため)
- 旧TPROXY構成が既にセットアップ済みのホストでは、`scripts/setup-vm-host.sh`の再実行時に自動でクリーンアップされる。手動でクリーンアップコマンドを案内する必要はない
- WSL2上のTPROXYがなぜ機能しないかという根本原因は未解明のまま残っている。将来masudaが非WSL2環境(素のLinuxホスト等)をサポート対象に含める場合、TPROXY方式が今度は問題なく動く可能性があるが、それを理由にREDIRECTから再度TPROXYへ戻す積極的な動機は無い(REDIRECTの方が依存する仕組みが単純で、CAP_NET_ADMINも不要という利点がある)
