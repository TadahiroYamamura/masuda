# ADR-0055: ゲートマーカーは「未消費の決定」だけを意味し、待機はその存在を条件とし、消費は原子的に行う

## Status

Accepted (2026-08-28)

## Context

GitHub Issue #42は、`cmd/masuda`のテストが`go test ./...`（パッケージ並列）でハングする件として起票された。調査の結果、並列実行は原因ではなく、`internal/statedaemon.Store.WaitForChange`の構造的な欠陥が表面化していただけだと判明した。1CPUに固定（`taskset -c 3`）しただけで、外部負荷なし・単一テストプロセスで2回目の反復から再現する。並列実行中の他パッケージはCPU圧の供給源にすぎなかった。

`WaitForChange`は「呼び出し時点以降の次の`Put`/`Delete`」だけを表現できるエッジトリガであり、`notify`はその瞬間に登録されている待ち受けだけを起こして待ち受けリストを消す。したがって**待ち受けが登録されるより前に届いた決定は、誰にも届かないまま消える**。

この形は[[0017-inotifywait-for-gate-wait-polling]]から原理ごと引き継がれたものである。inotifyの観測対象は事象しかなく「今どうなっているか」を問う手段が無いため、「変化を待つ」以外に書きようがなかった。[[0040-per-workspace-state-daemon-for-masuda-owned-state]]・[[0042-mcp-tool-call-replaces-inotifywait-gate-wait]]でこれがインメモリStoreへのRPCになった時点でその制約は消えていたが、APIの形だけが残った。0017・0042のいずれも、このレースには言及していない。

本番側には2つの経路がある。

- **承認レース**: オーケストレーターが`TASK.md`に`GATE:plan`を書いて終了し、Claudeがそれを読んでゲート待機ツールを発行し、relay/TCP/UDS/MCPを経て`WaitForChange`に届いて初めて待ち受けが登録される。この間はLLMの推論時間そのもので、数秒から数十秒ある。人間が`masuda plan show`を見ながら待っていて即座に承認すると、通知は空振りし、Claudeは`--mcp-config`の`"timeout"`（7日）までブロックする
- **再開時デッドロック**: `Put`はディスクへ永続化され`Open`で復元されるが、待ち受けは復元されない。人間の承認が`Put`された後、オーケストレーターがそれを消費する前にループが死ぬ（ホスト再起動・VMクラッシュ・tmux kill）と、再開時に`TASK.md`はまだ`GATE:plan`、マーカーはディスク上に解決済みで残っている。Claudeはループ仕様どおりゲート待機ツールを呼び、**もう起きない変化を永久に待つ**。運ではなく確定的に発生する。[[0040-per-workspace-state-daemon-for-masuda-owned-state]]が用意した「PIDファイル＋signal 0で冪等に自己修復」はデーモンプロセスを蘇生させるが、待ち受けは蘇生させない

同時に、マーカーの寿命が4通りに分かれていることも問題を作っていた。rejectedは消費時に消す、approvedは消さない、haltedは消さない、プラン再オープン時は防御的に消す。approvedを消さないのは意図的で、`detect_phase`が「ディスク上の状態から今の段階を再導出する」設計である以上、承認済みという事実が消えると再導出のたびに`await_g1`へ戻ってしまうためである。つまりマーカーが「未消費の決定」と「このゲートは承認された」という2つの役割を兼ねていた。その代償は実機で踏まれており、`implement_review_graph.py`には数時間前のapprovedマーカーが新しい機械的逸脱に対する今日の答えとして通ってしまった経緯と、その場しのぎの`delete`が残っていた。

さらに消費そのものが原子的でなかった。`get`と`delete`が別のRPCで、間に新しい決定が入るとそれを見ないまま消える。`delete`してから`put`（却下フィードバックの記録）という順序の箇所があり、間で落ちると決定が丸ごと失われる。

## Decision

**`gate:<name>`が存在する ⟺ 人間が下した決定のうち、まだ消費されていないものがある。** これを例外のない不変条件とし、その上に待機と消費を組み直す。

- 生成は人間の決定のみ（`internal/gate`の`Approve`/`Reject`/`Halt`、curated setの`resolve_gate_from_chat`）
- 削除は消費者が耐久記録を書いたのと**同じ原子的操作の中でのみ**
- `internal/gate.Status`から`Pending`を削除する。未解決はキーの不在で表す。`Read`（呼び出し元ゼロ、マーカーが無いとき`Marker{Status: Pending}`を捏造して返していた）も削除する

### 待機はレベルトリガにする

`Store.WaitForChange`を`WaitForPresence(ctx, key)`に置き換える。キーが存在するまでブロックし、**既に存在すれば即座に返る**。`notify`の意味は「待機を終わらせる」から「もう一度見に行かせる」に変わり、`Delete`で起こされた待機は不在を見て待ち直す。curated setのゲート待機ツールはこれを呼ぶだけになり、到達不能になった「マーカーが解決ではなく削除された」というエラー分岐が消える。

呼び出し元がゼロだったtrusted setの`state_wait_for_change`（`mcpclient`・`masuda internal state wait`・`state_client.wait()`まで配線されていた）は、基準点を持たない不健全なプリミティブなので削除する。

### 消費は原子的にする

`Store.Apply(ops)`を新設する。`check`/`put`/`delete`を1回の`s.mu`保持で適用し、`check`が1つでも成立しなければ何も変えずに`applied=false`を返す。checkの失敗はエラーではなく通常の結末（読み直して判断し直す）として扱う。

**putをdeleteより先に適用する。** マーカーの削除は「決定を引き受けた」という確認応答なので、それを果たす記録より後でなければならない。この順序が、apply途中で落ちたときに「決定が残る＝再配送」側へ倒れることを保証する。

`state_apply` MCPツール（trustedのみ）、`masuda internal state apply`（ops JSONをstdinから読む。JSONをシェルのクォートに通さないため）、`state_client.consume(key, follow_up)`として配線する。`consume`は値を読み、`follow_up(value)`が返す耐久記録のputとマーカーのdeleteを1回のapplyに束ねる。

### 承認・haltという耐久事実を分離する

`internal:plan-approved`・`internal:review-approved`・`internal:triage-halted`へ移す。値はマーカーJSONそのままで、情報は失わない。却下は既に`internal:plan-redo-pending`・`internal:review-feedback`・`internal:triage-redo-feedback`という耐久事実を持っていたので、同じapplyに入れる。

想定外のstatusを持つマーカーは、消費する前に例外で落とす。`follow_up`は`apply`より先に呼ばれるため、何も消費されないままエラーが伝播し、マーカーは人間が見られる状態で残る。

## Alternatives Considered

- **リビジョン付きの汎用待機（etcd風の`WaitForChange(key, since)`）**: キーごとに単調増加のリビジョンを持たせ、「自分が見た版より新しければ即返す」形にする案。汎用でCASにも使い回せる利点があったが、**再開時デッドロックが直らない**。Claudeは自分が何を見たかを覚えていないので、ツールが構築できる基準点は「今このとき」しかなく、「呼ぶ前から条件を満たしている」を表現できない。加えてリビジョンの永続化かデーモンインスタンスのepoch導入が必要になる
- **`WaitForChange`に「最後に見た値」を基準点として渡す**: 内容ベースの基準点にすればリビジョンの永続化問題は消える。しかし依然として事象待ちの延命であり、Claude側が基準点を持てない以上、再開時デッドロックは残る
- **マーカーの寿命は現状のまま、不変条件を規約として文書化する**: 変更は最小で済むが、「ゲートを開く経路は必ず事前にdeleteする」を人が守り続ける話のままになる。それは既に一度実機で破られている
- **フェーズ判定の主体をオーケストレーターからデーモンへ移す**: `detect_phase`が毎回ディスクから再導出する形なのは、オーケストレーターが短命なサブプロセスでメモリを持たないからであり、常駐プロセスならその制約は無い——という診断自体は正しい。しかし(1)フェーズの入力の大半（プラン成果物の有無、gitのタグ・diff・変更ファイル集合）がデーモンの領域外にあり、汎用KVストアでなくなる、(2)状態機械はPython・デーモンはGoという言語境界を越える必要がある、(3)デーモンにはスーパーバイザが無く（[[0040-per-workspace-state-daemon-for-masuda-owned-state]]のConsequences）、ループの現在位置という最も失えないものを、落ちても誰も気付かないプロセスに預けることになる。デーモンが短命なオーケストレーターに対して独占しているのは「自分のKV状態に対する原子的な遷移」だけなので、移すのはフェーズではなく原子性に限定した

## Consequences

- 待機が冪等になった。接続断後に同じ呼び出しを投げ直すことも、デーモン再起動後に待機を張り直すことも、同じ問いを発して同じ答えを得る。スーパーバイザが無い現状ではこの性質が効く
- `implement_review_graph.py`の防御的な`delete(PLAN_GATE_KEY)`が不要になり削除できた。マーカーの寿命が1通りになったことで、今後ゲートを開く経路が増えても後始末を足す必要がない
- 逆に「消費者が必ず消す」ことへの依存度は上がった。消し忘れると、従来はデッドロックだった状況が待機の即時復帰の連続（ホットループ）になる。トークン消費という意味では悪化しうる
- `Store.Apply`はこのStoreの他の呼び出し元に対しては原子的だが、**ディスク上は原子的でない**（opごとに別のファイル書き込み）。デーモンがapply途中で死ぬと部分適用が残る。put→deleteの順序固定により途中死は必ず再配送側へ倒れるが、この窓を完全に閉じるにはwrite-aheadジャーナルが要る
- ファイル成果物（`triage_concern.json`、G2却下時の`.masuda-commit-message`）はapplyに載せられない。「再入をせき止めるファイル削除」は確認応答より前に置く必要があり、その結果、値を覗いてから消費するまでの間に人間が決定を覆すと、覆される前の判断でファイルを消してしまう窓が残る
- [[0039-redo-pending-marker-bridges-gate-consume-and-subagent-rewrite]]がpendingマーカーで橋渡ししている非atomic性のうち、KV側（マーカー消費と却下フィードバックの記録）だけが原子的になった。成果物はファイルのままなので0039の機構は引き続き必要である
- Claude向けのツール名も`wait_for_gate_change`から`wait_for_gate_resolution`へ改めた。変化ではなく解決を待っているうえ、対になる`resolve_gate_from_chat`と「resolutionを待つ／resolveする」で語彙が揃う。名前が古いまま残っていたことは、この設計が疑われずに生き延びた一因でもある。AIに見えるプロトコル面（`runtime/CLAUDE.md`・`system_prompt.md.tmpl`）に触る変更なので、[[0042-mcp-tool-call-replaces-inotifywait-gate-wait]]が実機検証で2件の不具合を踏んだ前例に倣い、実機の`claude`で確認する
