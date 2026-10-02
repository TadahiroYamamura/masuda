# 再設計の経緯

このファイルだけは過去形で書く。再設計の動機と、方針を決める前に実機で確かめたことの記録。

## 旧設計とその限界

旧masuda（タグ`v1-frozen-develop`・`v1-frozen-sandbox-independence`・`v1-frozen-workflow-engine`）は、「Claude CodeをTASK.mdでループさせればサブスクリプションの範囲でオーケストレーターと協調できる」という発見から継ぎ足しで育った。最終形は次のとおり。

- Cloud Hypervisor microVMを自前の`internal/microvm`で起動し、rootfsはDockerイメージからext4へ自前変換、カーネルはホストのvmlinuzを流用
- egress制御はSNIを見る自前プロキシ + iptables REDIRECT + bridge + dnsmasq + setcap付きヘルパー
- 認証トークンはホストのAPIゲートウェイで差し替え
- ワークスペースごとに状態デーモン（UDS 2本）、mcp-relay、api-gateway、virtiofsd 2本、cloud-hypervisorの計6プロセス
- worktreeをvirtiofsでホストと共有し、ゲストが書ける領域に置くものはすべて「正本と写し」の二重化と照合が必要
- ワークフローはPython製LangGraphから、GoのYAMLエンジン（ADR-0062〜0082）へ移行途中

無理が出ていた点は4つ。(1) ホスト側の配管が多く、接続元の同定が「IP→DHCPリース→MAC」でゲストから偽装できた。(2) ホストのカーネルを借りるためWSL2/Ubuntu以外で動かず、Macは不可能だった。(3) 共有ディレクトリの写し照合。(4) tmux + ループ規約 + MCPリレーの配管。オープンIssueは環境依存（#57 #58 #40 #41 #47）、信頼境界が規約止まり（#13 #19 #20 #44 #56）、運用（#15 #26 #51 #9 #49）、機能（#28 #39 #7 #23 #34）に分かれていた。

## 再設計の動機（ユーザーの言葉）

1. 今の形はもっとシンプルに実現できる
2. egress制御は自作でなく既存ツールを流用したい
3. Macに対応したい（チームメイトがMacユーザー）
4. ワークフローはもっと柔軟に。「入出力をユーザー定義できない」「ノードの実行がエージェント前提で、100%決定論のノードを定義できない」
5. 「サンドボックスだけ」「ワークフロー制御だけ」を独立して構築・改善したい

## 前提として決めたこと

- サブスクリプション縛りは維持。tmux上のClaude Codeの自己ループも維持（API従量課金・Agent SDKは不採用）
- 配布を見据える。UIは公開APIの上に別途構築（CLIとGUIが同じAPIを叩く）
- サンドボックスはVM必須。Gondolin（earendil-works/gondolin）を採用し、TLS終端（MITM）を受け入れる
- エージェントに特権は極力与えない
- 決定論ノードはサンドボックス内で走らせる。エージェントが結果を汚染できるのは前提とし、外部への通信前とホストFSへの出力前に検証を置く
- ホスト↔ゲストのリアルタイム共有をやめる。cloneはVM内にだけ置き、ホストにはmasuda専用のstaging bareリポジトリ。永続化はgitのみ（VMは完全使い捨て）
- ADRは書かない。既存ADRは捨てる

## スパイク（2026-10-02、WSL2 Ubuntu 24.04、x86_64、KVM、QEMU 8.2.2、Gondolin v0.12.0、Claude Code 2.1.287）

| 確かめたこと | 結果 |
|---|---|
| Gondolinの既定イメージがWSL2上のKVMで起動する | 成功。初回は資産ダウンロード込みで17秒 |
| `ubuntu:24.04` + native Claude Code + tmuxのDockerイメージを`oci.image`で資産化 | 22秒。起動約5秒 |
| **Bun製Claude CodeがMITM越しに動く** | `/v1/messages`が200、SSE受信。Gondolin Issue #73（Bun TLSハング）は再現せず |
| トークンをゲストに入れない | プレースホルダ（`sk-ant-oat01-`形式）を`api.anthropic.com`宛てだけホストが置換して動作 |
| **サブスク枠で処理されている** | 応答に`anthropic-ratelimit-unified-5h/7d-*`ヘッダ |
| 多段階エージェント実行 | 非rootの`ubuntu`で`--dangerously-skip-permissions`、12モジュール+61テストを56秒で完走 |
| 長時間ストリーミング | 1本の`/v1/messages`が525秒SSEを流して200で完了（Gondolin #67は再発せず） |
| ノード単位のegress切替 | `isRequestAllowed`を可変な集合にすると実行中に403↔200が切り替わる |
| ゲスト→ホストの経路 | `tcp.hosts`で合成ホスト名→ホストのローカルポートへ到達。ゲートウェイIP直叩きは403 |
| tmuxへのattach | `vm.exec` pty（C-b dで切断可）とSSH（`enableSsh({user:"ubuntu"})`）の両方で動作 |
| ディスクのcopy-on-write複製 | checkpoint 129ms/14MB、同一チェックポイントからroot VMと継続VMを同時起動、双方向に漏れなし。**ただしcheckpointは元VMを停止し、再開は新規ブート** |
| 長時間生存 | 検証継続中（`keepalive.log`） |

スパイクで分かった設計上の注意:

- SSHフォワーダはNodeプロセス内で動く。同じプロセスで同期的に子プロセスを待つと詰まる。sandbox serviceは非同期に徹する
- 制御側のNodeプロセスが死ぬとQEMUが孤児で残る。起動時に`gcSessions()`を呼ぶ
- MITMのCA証明書はFUSE配下にあり非rootから読めない。システムCAバンドルには取り込まれるので動作には支障ないが、イメージ側で写しを置く
- rootが`ubuntu`所有のリポジトリで`git`を叩くと`dubious ownership`で拒否される。特権VMでは`safe.directory`が要る
- checkpointが元VMを止めるため、特権VMへのワークスペース受け渡しはディスク複製でなくgit（WIPスナップショット）と宣言した`inputs`で行う

## Gondolinの既知の制限（採用時点）

HTTP/1.xのみ（HTTP/2・QUIC不可）、HTTP CONNECT不可、UDPはDNSのみ、ARM64が最も検証されていてLinux x86_64はCIスモークのみ、VMはNodeプロセスの子、`experimental`を自称。オープンIssue: #72（hostfsのO_TRUNC）、#131（attachのpty入力停止）、#134（Node 24.17以上で502）。
