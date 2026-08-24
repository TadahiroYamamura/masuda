# 状態デーモンとMCP

ワークスペースごとに1プロセス常駐する状態デーモン（`masuda internal statedaemon`）が、masuda自身のコード（Go CLI・`orchestrator/*.py`）が読み書きする状態と、Claude自身がゲート待機に使う状態を管理する。実体は`internal/statedaemon.Store`という汎用KVストアで（ADR-0040）、その上に2つのMCPサーバー（トラステッド／キュレート）が乗る（ADR-0041）。

## 2つのソケット

デーモンはワークスペースの状態ディレクトリ直下に2つのUnix domain socketを開く（`internal/statedaemon/store.go`の`SocketPath`/`CuratedSocketPath`）。

| ソケット | ファイル名 | 公開するツール | 誰が繋ぐか |
|---|---|---|---|
| trusted | `daemon.sock` | `state_get`/`state_put`/`state_delete`/`state_list`/`state_wait_for_change`（5tool、`internal/statedaemon/mcpserver.New`） | ホストCLI（`cmd/masuda`、in-process import）、`orchestrator/*.py`（`masuda internal state`をsubprocessで叩く） |
| curated | `daemon-curated.sock` | `wait_for_gate_change`/`resolve_gate_from_chat`、および対象リポジトリとworktreeが分かっている場合のみ`run_privileged_command`（`internal/statedaemon/mcpserver.NewCurated`） | Claude自身（Discovery/Blueprint段階のホストループ、Build/Review段階はVM内の`claude`プロセス） |

両ソケットとも`internal/statedaemon/mcpserver/uds.go`の`serveUDS`が待ち受ける。バインド前に同名の残存ソケットファイルを削除してから`net.Listen("unix", ...)`し、`os.Chmod(socketPath, 0o600)`で他ユーザーからのアクセスを塞ぐ。`mcp.NewStreamableHTTPHandler`でMCPサーバーをHTTP over UDSとして配線しており、tool定義の中身には関知しない——`ServeCuratedServerUDS`はどんな`*mcp.Server`でも受け取れる形になっている。

curatedソケットはUDSのため`--mcp-config`（`http://host:port`形式のURLしか受け付けない）へ直接は渡せない。`masuda internal mcp-relay`がTCP↔UDS中継を行い、Claude側からはTCP経由でこのcuratedソケットへ届く（中継の実装詳細は`docs/design/networking.md`参照）。

`daemon.sock`へ承認済みの子MCPサーバーのtoolがプロキシ登録される仕組み（`internal/statedaemon/mcpaggregator`）は`docs/design/mcp-child-servers.md`を参照。

`run_privileged_command`は`NewCurated`の第2引数（`PrivilegedRunner`）がnilでない場合にのみ登録される。`runStatedaemon`が`--repo-root`と`--worktree-dir`の両方を受け取ったときだけ実体を渡すため、対象リポジトリを持たない単独起動（pytestフィクスチャ等）ではツール自体が現れない。実体は`cmd/masuda/statedaemon.go`の`privilegedRunner`が組み立てる——`internal/statedaemon/mcpserver`から`internal/sandbox`をimportすると、後者のテストが前者をimportしているためテストで循環参照になる。ツールの中身は`docs/design/privileged-commands.md`を参照。

## 汎用KVストア

`internal/statedaemon/store.go`の`Store`型。キーは`"<namespace>:<rest>"`という文字列で、`namespace`は`gate`/`artifact`/`internal`のいずれか（`keyPath`が`:`区切りをパースし、`rest`は相対パスでなければならない——`..`や絶対パスは拒否される）。オンディスクでは`<stateDir>/store/<namespace>/<rest>`というファイルに対応し、`Open(dir)`がディレクトリを`filepath.WalkDir`で再帰的に読み直して`values`マップを復元する。

提供する操作は5つ。

- `Get(key) (value []byte, ok bool)`
- `Put(key, value) error`: ディスクへ書き込んでから`values`マップを更新し、`key`を待っている全goroutineを起こす
- `Delete(key) error`: ファイルを削除。存在しないキーの削除はエラーにしない
- `List(prefix) []string`: `prefix`前方一致のキーをソートして返す
- `WaitForChange(ctx, key) (value []byte, ok bool, err error)`: 呼び出し時点以降の次の`Put`/`Delete`まで1回だけブロックする。ポーリングはしない（`inotifywait`単発呼び出しの置き換え、ADR-0042）。`ctx`がキャンセルされれば待機を中断する

`Put`/`Delete`の通知はチャネルのクローズで実装されており、1回通知したら`waiters`から即座に削除される（次に待つ側は改めて`WaitForChange`を呼び直す必要がある——一発勝負のワンショット設計）。

## デーモンへ移った状態・ファイルのまま残る状態

判断基準は「書き手と読み手が両方ともmasuda自身のコード（Go CLIまたは`orchestrator/*.py`）か」。Claudeのメインセッションやサブエージェントが自身のRead/Edit/Bashツールで直接触るファイルは、デーモンに到達する手段を持たないため対象外のまま残る。masuda全体で「ファイルをどこに置くかは誰が消費するかで決める」という同じ軸が使われている（ADR-0047）。

**デーモンへ移った状態（`internal:`/`gate:`/`artifact:`名前空間）**

- ゲートマーカー: `gate:plan`・`gate:review`・`gate:triage`（`internal/gate/gate.go`が読み書き。承認/却下/halt状態と人間のフィードバックを持つ）
- `artifact:DEVIATION.md`（`internal/gate/gate.go`。plan gate再オープン時の逸脱理由。従来ファイルだった`DEVIATION.md`をこの1件だけ移した）
- `internal:task-brief`・`internal:tdd-requested`・`internal:plan-retries`・`internal:iteration-count`・`internal:triage-redo-feedback`・`internal:plan-redo-pending`（`orchestrator/investigate_plan_graph.py`のカウンタ・フラグ類）
- `internal:approved-deviations`・`internal:review-state`・`internal:review-feedback`・`internal:interim-carried-findings`・`internal:interim-review-state-step{N}`・`internal:tdd-cycle-step{N}`（`orchestrator/implement_review_graph.py`のカウンタ・状態類。`iteration-count`は投稿段階間で共有せず独立）

同じディレクトリ内でも書き手が違えば分かれる例がある。`interim_review/step{N}/`ディレクトリは`trigger_match.json`・`result`/`check`/`fix`/`recheck`系のJSONをサブエージェントが直接書くのでファイルのまま、一方オーケストレーター自身だけが読み書きする進行状態（`redo_counts`等）は`internal:interim-review-state-step{N}`としてデーモン側に置かれる。`tdd_cycle/step{N}/`の`check_cycle*.json`（サブエージェント書き込み）と`internal:tdd-cycle-step{N}`（オーケストレーター専用）も同じ分かれ方をする。

**ファイルのまま残る状態**（サブエージェントがRead/Edit/Bashで直接触る成果物、または主セッション自身がReadする成果物）

- Discovery/Blueprint段階: `INVESTIGATION.md`・`plan/summary.md`・`plan/steps.json`・`plan_result.json`・`triage_concern.json`・`.masuda-investigate-redo-pending.json`・`INSTRUCTIONS.md`
- Build/Review段階: `implementation_result.json`・`triage_concern.json`・`review_results/*`（`final_report.md`含む）・`interim_review/step{N}/*`（進行状態を除く）・`tdd_cycle/step{N}/check_cycle*.json`・`.masuda-commit-message`・`.masuda-step-commit-message`・`.masuda-tdd-cycle-commit-message`
- 両段階共通: `TASK.md`（メインのClaude Codeセッションが自身のReadツールで読むループ制御チャネル。ADR-0006）

`internal/gate/gate.go`はこの境界の上で動く代表例で、`review_results/final_report.md`（`artifactPaths[Review]`）と`plan/summary.md`＋`plan/steps.json`（`renderPlan`が組み立てる）はサブエージェントのEdit書き込みを前提にファイルのまま読み、ゲートマーカーだけをデーモンから読む。

## トラステッドMCPツール・クライアント

トラステッド側5toolの定義は`internal/statedaemon/mcpserver/server.go`の`New(store)`。`state_get`/`state_list`/`state_wait_for_change`は`store`の対応メソッドをそのまま呼ぶだけ、`state_put`/`state_delete`はエラーを`fmt.Errorf`でラップして返す。

クライアント実装は`internal/statedaemon/mcpclient/client.go`の`Client`型ひとつだけで、Go側・Python側両方がこれを最終的に経由する——ただし経由の仕方が非対称である。

- **Go側**: `cmd/masuda`・`internal/gate`が`mcpclient.Dial(ctx, statedaemon.SocketPath(stateDir))`をin-processでimportして直接呼ぶ
- **Python側**: `orchestrator/state_client.py`はMCPクライアントを自前実装せず、`masuda internal state get/put/delete/list/wait <key>`（`cmd/masuda/internalstate.go`の`newInternalStateCommand`、hidden subcommand）を`subprocess.run`で1操作1回呼び出す。ソケットパスは`--socket`省略時`$MASUDA_STATE_DIR`から`statedaemon.SocketPath`で解決される

この非対称の帰結として、`orchestrator/*.py`を実行するVMのrootfsに`masuda`バイナリ自体が同梱されている必要がある（イメージビルド時にマルチステージビルドで焼き込み、ADR-0041）。MCPワイヤプロトコルの実装はGo側の`mcpclient`一箇所に集約されており、Pythonは`_run()`のJSONパース以上のことをしない。

## キュレートMCPツールセット

`internal/statedaemon/mcpserver/curated.go`の`NewCurated(store)`が返す2toolのみ。Claudeのメインセッション（サブエージェントには渡らない）が使う。

- **`wait_for_gate_change`**（`waitForGateChange`）: `name`は`gateNames = {"plan", "review", "triage"}`のいずれかのみ許可。`store.WaitForChange(ctx, "gate:"+name)`をブロッキング呼び出しし、マーカーJSON（`status`/`feedback`）をパースして返す。マーカーは常に上書き（Put）で解決されるため、`found=false`（削除されたケース）はエラー扱い
- **`resolve_gate_from_chat`**（`resolveGateFromChat`）: `masuda chat`での対話中に人間が「進めていい」と言った場合の自己承認用。`name`は`chatResolvableGateNames = {"plan", "review"}`のみ許可——**`"triage"`はサーバー側で拒否される**（ADR-0029: triage対象のエージェント自身がtriageゲートを閉じてはならないという規約を、この1点だけ技術的に強制する）。`status`は`"approved"`/`"rejected"`のみ許容し、`gate:`+nameへ`{status, feedback, decided_at}`をPutする

`cmd/masuda/statedaemon.go`の`runStatedaemon`は`curated := mcpserver.NewCurated(store)`を1個だけ構築し、`ServeCuratedServerUDS`とマウント後の`mcpaggregator.Start`（子MCPサーバーのプロキシtool登録）の両方がこの同一インスタンスへツールを追加登録していく。子MCPサーバーの承認・集約の詳細は`docs/design/mcp-child-servers.md`を参照。

## MCPツール呼び出しのタイムアウト対策

Claude Code自身のMCPクライアントは、ツール呼び出しに対して1分未満のハード・ウォールクロックタイムアウトを持つ（MCP側のprogress通知では延長されない）。人間のゲート承認待ちがこれを超えるのは通常のことなので、`wait_for_gate_change`を配線する`--mcp-config`の**サーバーごとの`"timeout"`フィールド**に`604800000`（7日、ミリ秒）を設定して上書きする必要がある。設定箇所は3つ、いずれも欠かすとゲート待機が数十秒で静かに失敗する。

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

- **`startDaemon(id)`**: `exec.Command`で自分自身（`os.Executable()`）を`internal statedaemon --state-dir <dir> --repo-root <repoRoot>`付きで`Setsid: true`のデタッチプロセスとして起動し、標準出力/標準エラーを`daemon.log`へ、PIDを`daemon.pid`へ書く。`daemonAlive(stateDir)`（PIDファイル読み取り＋signal 0による生死確認、実際にはシグナルを送らないPOSIXの存在確認）が真なら何もしない冪等な操作——`masuda workspace create`（`cmd/masuda/workspace.go`）と`masuda plan start`（`cmd/masuda/plan.go`、新規・再開どちらも）の両方から無条件に呼ばれる
- **`stopDaemon(id)`**: `daemon.pid`のPIDへ`SIGTERM`を送る。PIDファイルが無ければ（この機能追加以前に作られたワークスペース）成功扱い。`masuda workspace remove`（`cmd/masuda/workspace.go`）から呼ばれる

状態ディレクトリ直下の関連ファイルは`daemon.pid`・`daemon.log`・`store/`（KVストアの永続化先ディレクトリ）の3つ（`daemonPIDName`/`daemonLogName`/`daemonStoreDirName`）。デーモンプロセス自体にスーパーバイザはなく、クラッシュしても次回`startDaemon`が呼ばれるまで気付かれない。
