# 状態デーモンとMCP

ワークスペースごとに1プロセス常駐する状態デーモン（`masuda internal statedaemon`）が、masuda自身のコード（Go CLI・`orchestrator/*.py`）が読み書きする状態と、Claude自身がゲート待機に使う状態を管理する。実体は`internal/statedaemon.Store`という汎用KVストアで（ADR-0040）、その上に2つのMCPサーバー（トラステッド／キュレート）が乗る（ADR-0041）。

## 2つのソケット

デーモンはワークスペースの状態ディレクトリ直下に2つのUnix domain socketを開く（`internal/statedaemon/store.go`の`SocketPath`/`CuratedSocketPath`）。

| ソケット | ファイル名 | 公開するツール | 誰が繋ぐか |
|---|---|---|---|
| trusted | `daemon.sock` | `state_get`/`state_put`/`state_delete`/`state_list`/`state_apply`（5tool、`internal/statedaemon/mcpserver.New`） | ホストCLI（`cmd/masuda`、in-process import）、`orchestrator/*.py`（`masuda internal state`をsubprocessで叩く）。**いずれもホスト上のプロセスで、VMからこのソケットへ到達する経路は無い** |
| curated | `daemon-curated.sock` | `wait_for_gate_resolution`/`resolve_gate_from_chat`、および対象リポジトリとworktreeが分かっている場合のみ`run_privileged_command`・`next_task`（`internal/statedaemon/mcpserver.NewCurated`） | Claude自身（Discovery/Blueprint段階のホストループ、Build/Review段階はVM内の`claude`プロセス） |

両ソケットともUDSであり、ホストのファイルシステム上にしか存在しない。VM内から届くのはcuratedだけで、それも`masuda internal mcp-relay`がホスト側でTCPへ中継しているからである（後述）。trustedソケットにはこの中継が無い——これは実装漏れではなく、**VMがtrusted setに触れないこと自体が設計**である（ADR-0057）。

両ソケットとも`internal/statedaemon/mcpserver/uds.go`の`serveUDS`が待ち受ける。バインド前に同名の残存ソケットファイルを削除してから`net.Listen("unix", ...)`し、`os.Chmod(socketPath, 0o600)`で他ユーザーからのアクセスを塞ぐ。`mcp.NewStreamableHTTPHandler`でMCPサーバーをHTTP over UDSとして配線しており、tool定義の中身には関知しない——`ServeCuratedServerUDS`はどんな`*mcp.Server`でも受け取れる形になっている。

curatedソケットはUDSのため`--mcp-config`（`http://host:port`形式のURLしか受け付けない）へ直接は渡せない。`masuda internal mcp-relay`がTCP↔UDS中継を行い、Claude側からはTCP経由でこのcuratedソケットへ届く（中継の実装詳細は`docs/design/networking.md`参照）。

`daemon.sock`へ承認済みの子MCPサーバーのtoolがプロキシ登録される仕組み（`internal/statedaemon/mcpaggregator`）は`docs/design/mcp-child-servers.md`を参照。

`run_privileged_command`と`next_task`は、`NewCurated`が受け取るランナー（`PrivilegedRunner`・`OrchestratorRunner`）がnilでない場合にのみ登録される。`runStatedaemon`が`--repo-root`と`--worktree-dir`の両方を受け取ったときだけ実体を渡すため、対象リポジトリを持たない単独起動（pytestフィクスチャ等）ではツール自体が現れない。実体は`cmd/masuda/statedaemon.go`の`privilegedRunner`が組み立てる——`internal/statedaemon/mcpserver`から`internal/sandbox`をimportすると、後者のテストが前者をimportしているためテストで循環参照になる。ツールの中身は`docs/design/privileged-commands.md`を参照。

## 汎用KVストア

`internal/statedaemon/store.go`の`Store`型。キーは`"<namespace>:<rest>"`という文字列で、`namespace`は`gate`/`artifact`/`internal`のいずれか（`keyPath`が`:`区切りをパースし、`rest`は相対パスでなければならない——`..`や絶対パスは拒否される）。オンディスクでは`<stateDir>/store/<namespace>/<rest>`というファイルに対応し、`Open(dir)`がディレクトリを`filepath.WalkDir`で再帰的に読み直して`values`マップを復元する。

提供する操作は5つ。

- `Get(key) (value []byte, ok bool)`
- `Put(key, value) error`: ディスクへ書き込んでから`values`マップを更新し、`key`を待っている全goroutineを起こす
- `Delete(key) error`: ファイルを削除。存在しないキーの削除はエラーにしない
- `List(prefix) []string`: `prefix`前方一致のキーをソートして返す
- `WaitForPresence(ctx, key) (value []byte, err error)`: `key`が存在するまでブロックし、**既に存在すれば即座に返る**。ポーリングはしない（ADR-0042の`inotifywait`置き換えを、事象待ちから条件待ちへ改めたもの。ADR-0055）。`ctx`がキャンセルされれば待機を中断する
- `Apply(ops) (applied bool, err error)`: `check`/`put`/`delete`を1回の`s.mu`保持で適用する。`check`が1つでも成立しなければ何も変えずに`applied=false`を返す（エラーではない）。`put`は`delete`より先に適用される

`Put`/`Delete`の通知はチャネルのクローズで実装されている。通知の意味は「待機を終わらせる」ではなく「もう一度見に行かせる」で、起こされた`WaitForPresence`は条件を再評価し、まだ満たされていなければ待ち直す（`Delete`で起こされた場合がこれにあたる）。

`Apply`が原子的なのはこの`Store`の他の呼び出し元に対してであって、プロセスの死に対してではない——opごとに別のファイル書き込みになるため、途中で落ちれば部分適用が残る。`put`を`delete`より先に適用するのは、その場合に「マーカーが残る＝決定が再配送される」側へ倒すため（ADR-0055）。

## デーモンへ移った状態・ファイルのまま残る状態

判断基準は「書き手と読み手が両方ともmasuda自身のコード（Go CLIまたは`orchestrator/*.py`）か」。Claudeのメインセッションやサブエージェントが自身のRead/Edit/Bashツールで直接触るファイルは、デーモンに到達する手段を持たないため対象外のまま残る。masuda全体で「ファイルをどこに置くかは誰が消費するかで決める」という同じ軸が使われている（ADR-0047）。

**デーモンへ移った状態（`internal:`/`gate:`/`artifact:`名前空間）**

- ゲートマーカー: `gate:plan`・`gate:review`・`gate:triage`（`internal/gate/gate.go`が読み書き。承認/却下/halt状態と人間のフィードバックを持つ）
- `artifact:DEVIATION.md`（`internal/gate/gate.go`。plan gate再オープン時の逸脱理由。従来ファイルだった`DEVIATION.md`をこの1件だけ移した）
- `internal:task-brief`・`internal:plan-retries`・`internal:iteration-count`・`internal:triage-redo-feedback`・`internal:plan-redo-pending`（`orchestrator/investigate_plan_graph.py`のカウンタ・フラグ類）
- `internal:approved-deviations`・`internal:review-state`・`internal:review-feedback`・`internal:interim-carried-findings`・`internal:interim-review-state-step{N}`（`orchestrator/implement_review_graph.py`のカウンタ・状態類。`iteration-count`は投稿段階間で共有せず独立）

同じディレクトリ内でも書き手が違えば分かれる例がある。`interim_review/step{N}/`ディレクトリは`trigger_match.json`・`result`/`check`/`fix`/`recheck`系のJSONをサブエージェントが直接書くのでファイルのまま、一方オーケストレーター自身だけが読み書きする進行状態（`redo_counts`等）は`internal:interim-review-state-step{N}`としてデーモン側に置かれる。

**ファイルのまま残る状態**（サブエージェントがRead/Edit/Bashで直接触る成果物、または主セッション自身がReadする成果物）

- Discovery/Blueprint段階: `INVESTIGATION.md`・`plan/summary.md`・`plan/steps.json`・`plan_result.json`・`triage_concern.json`・`.masuda-investigate-redo-pending.json`・`INSTRUCTIONS.md`
- Build/Review段階: `implementation_result.json`・`triage_concern.json`・`review_results/*`（`final_report.md`含む）・`interim_review/step{N}/*`（進行状態を除く）・`.masuda-commit-message`・`.masuda-step-commit-message`
- 両段階共通: `TASK.md`（メインのClaude Codeセッションが自身のReadツールで読むループ制御チャネル。ADR-0006）

`internal/gate/gate.go`はこの境界の上で動く代表例で、`review_results/final_report.md`（`artifactPaths[Review]`）と`plan/summary.md`＋`plan/steps.json`（`renderPlan`が組み立てる）はサブエージェントのEdit書き込みを前提にファイルのまま読み、ゲートマーカーだけをデーモンから読む。

## トラステッドMCPツール・クライアント

トラステッド側5toolの定義は`internal/statedaemon/mcpserver/server.go`の`New(store)`。`state_get`/`state_list`は`store`の対応メソッドをそのまま呼ぶだけ、`state_put`/`state_delete`/`state_apply`はエラーを`fmt.Errorf`でラップして返す。ブロッキング待機はこちらには無い——待つ対象はゲートだけであり、それはcurated setの`wait_for_gate_resolution`が「何を待っているか」を言える形で持つ（ADR-0055）。

`state_apply`のopは`{"op": "check"|"put"|"delete", "key": ..., "value": ...}`で、`value`は省略可能。`check`で省略すると「そのキーが存在しないこと」の表明になるため、「不在」と「空文字列」は区別される。

クライアント実装は`internal/statedaemon/mcpclient/client.go`の`Client`型ひとつだけで、Go側・Python側両方がこれを最終的に経由する——ただし経由の仕方が非対称である。

- **Go側**: `cmd/masuda`・`internal/gate`が`mcpclient.Dial(ctx, statedaemon.SocketPath(stateDir))`をin-processでimportして直接呼ぶ
- **Python側**: `orchestrator/state_client.py`はMCPクライアントを自前実装せず、`masuda internal state get/put/delete/list/apply`（`cmd/masuda/internalstate.go`の`newInternalStateCommand`、hidden subcommand）を`subprocess.run`で1操作1回呼び出す。`apply`だけはopのJSON配列を引数ではなくstdinから受け取る（任意のJSON値がシェルのクォートを通らずに済む）。この上に`state_client.consume(key, follow_up)`があり、ゲートマーカーの消費はすべてこれを経由する（`docs/design/gates.md`）。ソケットパスは`--socket`省略時`$MASUDA_STATE_DIR`から`statedaemon.SocketPath`で解決される

両オーケストレーターはホスト上で動くため、この解決はホスト内のUDSに閉じている。VMのrootfsには`masuda`バイナリも`orchestrator/`もPython venvも同梱されていない（ADR-0057）——ゲストがtrusted setへ到達できないことを、規約ではなくイメージの性質として持たせるためである。MCPワイヤプロトコルの実装はGo側の`mcpclient`一箇所に集約されており、Pythonは`_run()`のJSONパース以上のことをしない。

## キュレートMCPツールセット

`internal/statedaemon/mcpserver/curated.go`の`NewCurated`が返すツールセット。Claudeのメインセッション（サブエージェントには渡らない）が使う。常設は以下の2tool。

- **`wait_for_gate_resolution`**（`waitForGateResolution`）: `name`は`gateNames = {"plan", "review", "triage"}`のいずれかのみ許可。`store.WaitForPresence(ctx, "gate:"+name)`をブロッキング呼び出しし、マーカーJSON（`status`/`feedback`）をパースして返す。マーカーの不在が未解決を意味するため、待つ条件は「キーが存在すること」そのものになる——既に解決済みのゲートに対しては即座に返り、接続断後の呼び直しも安全（ADR-0055）
- **`resolve_gate_from_chat`**（`resolveGateFromChat`）: `masuda chat`での対話中に人間が「進めていい」と言った場合の自己承認用。`name`は`chatResolvableGateNames = {"plan", "review"}`のみ許可——**`"triage"`はサーバー側で拒否される**（ADR-0029: triage対象のエージェント自身がtriageゲートを閉じてはならないという規約を、この1点だけ技術的に強制する）。`status`は`"approved"`/`"rejected"`のみ許容し、`gate:`+nameへ`{status, feedback, decided_at}`をPutする

- **`next_task`**（`nextTask`、`OrchestratorRunner`がある場合のみ）: Build/Review段階のループを1回分進め、次のタスク本文を返す。実体はホスト上で`implement_review_graph.py`を1回走らせる`cmd/masuda/statedaemon.go`の`orchestratorRunner`（`docs/design/build.md`）。**タスク本文をファイルではなく戻り値で返す**のは、ホストが書いた`TASK.md`がゲストから見えるまでvirtiofsの属性キャッシュ分（実測0.5〜0.6秒）遅れるためで、直前のターンの内容を読んでしまう窓を無くしている

`cmd/masuda/statedaemon.go`の`runStatedaemon`は`curated`を1個だけ構築し、`ServeCuratedServerUDS`とマウント後の`mcpaggregator.Start`（子MCPサーバーのプロキシtool登録）の両方がこの同一インスタンスへツールを追加登録していく。子MCPサーバーの承認・集約の詳細は`docs/design/mcp-child-servers.md`を参照。

## MCPツール呼び出しのタイムアウト対策

Claude Code自身のMCPクライアントは、ツール呼び出しに対して1分未満のハード・ウォールクロックタイムアウトを持つ（MCP側のprogress通知では延長されない）。人間のゲート承認待ちがこれを超えるのは通常のことなので、`wait_for_gate_resolution`を配線する`--mcp-config`の**サーバーごとの`"timeout"`フィールド**に`604800000`（7日、ミリ秒）を設定して上書きする必要がある。設定箇所は3つ、いずれも欠かすとゲート待機が数十秒で静かに失敗する。

- `internal/hostloop/hostloop.go`の`mcpConfigJSON`（`mcpToolTimeoutMillis`定数）: Discovery/Blueprint段階のホストループ自身の`claude`プロセス向け
- `runtime/entrypoint.sh`の`MCP_CONFIG`変数
- `runtime/start_claude.sh`の`MCP_CONFIG`変数

あわせて別系統の"idle timeout"（progress通知の有無に関わらずアイドル時間で切る方の機構）も`CLAUDE_CODE_MCP_TOOL_IDLE_TIMEOUT=0`環境変数で無効化しており、上記3箇所いずれも`claude`起動コマンドの前置きにこの環境変数を付けている（多層防御、ADR-0042）。新しくcurated MCPツールを呼ぶ`claude`起動経路を追加する場合は、この2つ（`"timeout"`フィールドと環境変数）をセットで設定すること。

## 状態デーモン起動・プロセス管理CLI

`cmd/masuda/statedaemon.go`の`runStatedaemon(ctx, stateDir, repoRoot)`が実処理本体（cobraのRunEから分離済みで、サブプロセスを起動せず直接テストできる）。

1. `statedaemon.Open(filepath.Join(stateDir, "store"))`でストアを開く
2. `curated := mcpserver.NewCurated(store)`を1個構築
3. `mcpserver.ServeUDS`（trusted）と`mcpserver.ServeCuratedServerUDS`（curated、上のcuratedインスタンスを渡す）をそれぞれ別goroutineで並行サーブ
4. `repoRoot != ""`なら`mcpaggregator.Start(ctx, repoRoot, curated, stateDir)`で子MCPサーバー集約を追加で開始（`repoRoot == ""`はpytestフィクスチャ等、対象リポジトリを持たない standalone 用途で集約を無効化する）
5. 2つのgoroutineのうちどちらか一方がエラーで停止するかctxがキャンセルされたら、`cancel()`でもう一方も道連れに停止させる（片方のソケットだけ生き残ることはない）

CLIとしては`masuda internal statedaemon --state-dir <dir> --repo-root <path>`（hidden subcommand、`newInternalStatedaemonCommand`）がフォアグラウンドでこれを実行する。実際の起動・停止はこれを包む2つの関数から行われる。

- **`startDaemon(id)`**: `exec.Command`で自分自身（`os.Executable()`）を`internal statedaemon --state-dir <dir> --repo-root <repoRoot> --worktree-dir <dir>`付きで`Setsid: true`のデタッチプロセスとして起動し、標準出力/標準エラーを`daemon.log`へ、PIDを`daemon.pid`へ書く。起動前に2段階を踏む冪等な操作（ADR-0061）:
  1. `daemonServing(stateDir)`がtrustedソケットへ`net.DialTimeout`（1秒）。接続できれば何もしない。**PIDは生存判定に使わない**
  2. 接続できなければ`reclaimStaleDaemon(stateDir)`。`daemon.pid`のPIDが`isDaemonProcess`（`/proc/<pid>/cmdline`のargvに`statedaemon`と当該状態ディレクトリが独立した引数として現れるか）で同定できた場合だけ`SIGTERM`を送り、終了を待つ（`daemonStopTimeout`＝5秒）。同定できなければ何もしない
- **`stopDaemon(id)`**: `daemon.pid`のPIDを`isDaemonProcess`で同定してから`SIGTERM`を送る。PIDファイルが無ければ（この機能追加以前に作られたワークスペース）成功扱い。同定できないPIDには何もしない——PID再利用時に無関係なプロセスを殺さないため（ADR-0061）

呼び出し元は、デーモンを必要とする入口すべて。起動側が`masuda workspace create`（`cmd/masuda/workspace.go`）・`masuda plan start`（`cmd/masuda/plan.go`、新規・再開どちらも）・`ensureGateWorkspace`（`cmd/masuda/gate.go`。plan/review/triageのゲート操作コマンドが必ず通る）・`masuda sandbox start`（`cmd/masuda/sandbox.go`。ゲストのmcp-relayがcuratedソケットを叩くため）。停止側が`masuda workspace remove`と`finalizeReviewApproval`（`cmd/masuda/gate.go`。承認はワークスペースが終わるもう一方の経路）

状態ディレクトリ直下の関連ファイルは`daemon.pid`・`daemon.log`・`store/`（KVストアの永続化先ディレクトリ）の3つ（`daemonPIDName`/`daemonLogName`/`daemonStoreDirName`）。デーモンプロセス自体にスーパーバイザはなく、クラッシュしても次回`startDaemon`が呼ばれるまで気付かれない。
