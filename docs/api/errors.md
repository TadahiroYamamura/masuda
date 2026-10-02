# エラーコード

各RPCが失敗したときに返す[Connectのエラーコード](https://connectrpc.com/docs/protocol/#error-codes)と、その条件。`masuda serve`の実装から洗い出した一覧で、クライアントはこのコードで分岐してよい。メッセージ（`message`）は人間向けの英語で、文言は変わりうるので分岐に使わない。

## コードの使い分け

| コード | HTTP | 意味 | クライアントがすること |
|---|---|---|---|
| `invalid_argument` | 400 | 要求そのものが誤り。何度送っても同じ結果になる | 入力を直す。定義の問題・宣言に無い名前もここ |
| `not_found` | 404 | 指したもの（ワークスペース・ゲート・質問・rev・パス・ワークフロー）が無い | 一覧を取り直す |
| `failed_precondition` | 400 | 要求は正しいが、今の状態では受け付けられない | 状態を取り直してから、条件を満たして送り直す（Stopしてから、承認してから、等） |
| `already_exists` | 409 | 作ろうとしたものが既にある（Runのブランチ） | 別の名前にする |
| `unimplemented` | 501 | その組み合わせはまだ実装が無い・このsandboxでは提供しない | 機能を出さない・別の手段を案内する |
| `unavailable` | 503 | `masuda-sandbox serve`に届かない | sandbox serviceの起動を案内する |
| `canceled` | 499 | 要求が取り消された（クライアントの切断等） | 必要なら送り直す |
| `internal` | 500 | ホスト側の読み書きの失敗など、masudaの側の問題 | 利用者に見せ、`masuda serve`のログを確かめてもらう |
| `unknown` | 500 | 分類していない失敗（sandbox serviceからの想定外のエラー等） | `internal`と同じ |

- HTTP+JSONで呼ぶときは、ステータスでなく本文の`code`で分岐する（`invalid_argument`と`failed_precondition`はどちらも400）
- サーバーストリーミング（`Watch`・`GetBlob`・`BuildImage`）のエラーは、HTTP 200のまま終わりのエンベロープで届く（[接続](connect.md#streaming)）。ストリームの途中でもエラーで終わりうる
- `failed_precondition`の多くは、`Run`・`Resume`の前提（秘密・承認・イメージ等）の不足。足りないものは1回の応答に`; `区切りでまとめて入る

## すべてのRPCに共通

- ワークスペースのIDを受け取るRPCは、そのIDのワークスペースが無ければ`not_found`。IDとして不正な文字列（空文字列を含む）も`not_found`
- `repo_root`を受け取るRPC（`Run`・ConfigService・WorkflowService）は、絶対パスでない・gitの作業ツリーでない・作業ツリーのトップでない（サブディレクトリを渡した）なら`invalid_argument`。WorkflowServiceだけは`repo_root`を空にでき、そのときは同梱の定義だけを読む
- ホスト側のファイルの読み書きに失敗すれば`internal`

## WorkspaceService

| RPC | コード | 条件 |
|---|---|---|
| `Run` | `invalid_argument` | `repo_root`が不正。`workflow`か`branch`が空。`.masuda/`を読めない。定義が読み込めない・検査で問題がある（問題の一覧がメッセージに入る）。`workflow`が定義に無い。ワークフローの`inputs`が足りない。`settings.json`が読めない・知らないキーがある。ブランチ名が不正。`base`が実リポジトリに無い |
| | `failed_precondition` | 起動に要るものが足りない: Claudeのトークン・宣言した秘密の値が無い、`plaintext`の秘密が未承認、イメージのDockerfileが無い、`envFiles`の公開値が`vars`に無い、ワークフローが使う`checks`が宣言されていない、`settings.local.json`が読めない・`stallAfter`が不正 |
| | `already_exists` | `branch`が実リポジトリに既にある |
| | `canceled` | stagingを作っている間に要求が取り消された |
| | `internal` | ワークスペース・stagingの作成、engineの開始に失敗した |
| `Resume` | `not_found` | ワークスペースが無い |
| | `failed_precondition` | 再開できる状態でない（STOPPEDと、sandboxの起動に失敗したBLOCKEDだけが再開できる）。既に動いている。定義の写しが無い・読み込めない。起動に要るものが足りない（`Run`と同じ） |
| | `invalid_argument` | 定義の写しの`settings.json`が読めない |
| | `internal` | 実行の窓口の用意・質問の破棄の記録に失敗した |
| `Get` | `not_found` | ワークスペースが無い |
| `List` | `internal` | 一覧を読めない。`repo_root`は検査しない（一致するものが無ければ空の一覧） |
| `Watch` | `not_found` | `id`を指定し、そのワークスペースが無い（ストリームの最初に終わる） |
| | （正常な終わり） | `masuda serve`が止まると、エラーでなく正常な終わりでストリームが閉じる |
| `Stop` | `not_found` | ワークスペースが無い |
| | `failed_precondition` | DONE（publish・discardで終わっていて、止めるものが無い）。STOPPEDへの`Stop`はエラーにせずそのまま返す |
| `Remove` | `not_found` | ワークスペースが無い |
| | `failed_precondition` | 動いている（STARTING・RUNNING・WAITING_GATE・WAITING_QUESTION）のに`force`が無い |
| `AttachInfo` | `not_found` | ワークスペースが無い |
| | `failed_precondition` | sandboxが無い（止まっている等）、またはまだ起動中 |
| | `unimplemented` | sandbox serviceがSSHを提供しない（フェイクsandbox） |
| | sandboxのコード | sandbox serviceの`EnableSsh`が返したコードをそのまま返す。届かなければ`unavailable` |

## GateService

| RPC | コード | 条件 |
|---|---|---|
| `ListOpen` | `not_found` | `workspace_id`を指定し、そのワークスペースが無い |
| `Get` | `not_found` | ワークスペースが無い。その`occurrence`のゲートが無い |
| `Decide` | `invalid_argument` | `decision`か`decision.outcome`が空。ゲートの種類に合わない`outcome`（plan・reviewなど定義のゲートとdeviationは`approved`・`rejected`、triageは`dismiss`・`halt`・`redo`だけを受け付ける） |
| | `not_found` | ワークスペースが無い。その`occurrence`のゲートが無い |
| | `failed_precondition` | ゲートが判断済み。`approved`で`target_hash`がゲートのものと違う。ワークスペースが動いていない（STOPPED等）。engineが受け付けない: そのゲートが今待っているものでない、deviationの`approved_files`にゲートが挙げていないファイルがある |

## QuestionService

| RPC | コード | 条件 |
|---|---|---|
| `ListOpen` | `not_found` | `workspace_id`を指定し、そのワークスペースが無い |
| `Answer` | `not_found` | ワークスペースが無い。その`occurrence`の開いた質問が無い（答え済み・再開で破棄された質問を含む） |
| | `invalid_argument` | 答えの無い質問がある。選択肢（`options`）のある質問に選択肢以外を答えた。聞かれていない`id`への答えがある |
| | `failed_precondition` | ワークスペースが動いていない。engineが受け付けない |

## StagingService

`rev`・`commit`・`from`・`to`には、コミットに解決できるもの（ref名・ハッシュ・`HEAD~1`等）を渡す。

| RPC | コード | 条件 |
|---|---|---|
| `ListRefs` | `not_found` | ワークスペースが無い |
| `GetCommit` | `invalid_argument` | `rev`が空、または`-`で始まる |
| | `not_found` | ワークスペースが無い。`rev`がコミットに解決できない |
| `Diff` | `invalid_argument` | `to`が空、または`from`・`to`が`-`で始まる |
| | `not_found` | ワークスペースが無い。`from`・`to`がコミットに解決できない |
| `GetBlob` | `invalid_argument` | `rev`が空、または`-`で始まる |
| | `not_found` | ワークスペースが無い。`rev`が解決できない。その`path`がファイルでない・無い |
| | `internal` | 読み出しの途中の失敗（いくつかのチャンクを送った後でも起こりうる） |
| `ListComments` | `invalid_argument` | `commit`が`-`で始まる |
| | `not_found` | ワークスペースが無い。`commit`を指定し、それが解決できない |
| `AddComment` | `invalid_argument` | `body`が空。`commit`が空、または`-`で始まる |
| | `not_found` | ワークスペースが無い。`commit`が解決できない |

`AddComment`は`path`・`line`がそのコミットに実在するかを確かめない。

## ConfigService

すべてのRPCは、最初に`repo_root`の検査と設定の読み込みをする。

- `repo_root`が不正なら`invalid_argument`
- `.masuda/settings.json`・`.masuda/settings.local.json`が読めない（JSONとして壊れている、知らないキーがある）なら`failed_precondition`

| RPC | コード | 条件 |
|---|---|---|
| `ListEgress` | | 共通のものだけ |
| `ApproveEgress` | `invalid_argument` | `host`が`settings.json`の`egress`に宣言されていない |
| `RejectEgress` | `invalid_argument` | `host`が宣言にも承認にも無い（宣言から消えたホストの承認は取り消せる） |
| `ListSecrets` | | 共通のものだけ |
| `SetSecret` | `invalid_argument` | `name`が宣言した秘密でもClaudeのトークンの名前でもない。`value`が空 |
| `ApproveSecret` | `invalid_argument` | `name`が宣言されていない。`placeholder`モードで承認が要らない |
| `RejectSecret` | `invalid_argument` | `name`が宣言にも承認にも無い。`placeholder`モードで承認の記録も無い |
| `ListPrivilegedCommands` | | 共通のものだけ |
| `ApprovePrivilegedCommand` | `invalid_argument` | `name`が宣言されていない。宣言の形が壊れている（コマンドが空、イメージのDockerfileが無い、`inputs`・`outputs`が作業ツリーの外を指す） |
| `ListImages` | sandboxのコード | sandbox serviceの`ListImages`の失敗をそのまま返す（届かなければ`unavailable`） |
| `BuildImage` | `invalid_argument` | `entry`（空なら`settings.json`の`image`）がエントリ名として不正 |
| | `not_found` | `.masuda/images/<entry>/Dockerfile`が無い |
| | sandboxのコード | ビルドの失敗はsandbox serviceのコードをそのまま返す。ビルド結果の記録に失敗すれば`unknown` |

## WorkflowService

作業ツリーの`.masuda/`（と同梱の定義）を読む。`repo_root`が空なら同梱だけ。

| RPC | コード | 条件 |
|---|---|---|
| `List` | `invalid_argument` | `repo_root`が不正。定義が読み込めない |
| `Show` | `invalid_argument` | `workflow`が空。`repo_root`が不正。定義が読み込めない。図にできない |
| | `not_found` | `workflow`が定義に無い |
| `Check` | `invalid_argument` | `repo_root`が不正 |
| | `not_found` | `workflow`を指定し、それが定義に無い |

`Check`は、定義が読み込めないときもエラーにせず、その理由を`problems`の1つとして返す。

## 揃っていないところ

次は今の実装の振る舞いで、将来揃える可能性がある。クライアントは両方のコードを同じに扱っておくと安全。

- 定義に無いワークフロー: `Run`は`invalid_argument`、`Show`・`Check`は`not_found`
- 壊れた`settings.json`: `Run`は`invalid_argument`、ConfigServiceは`failed_precondition`
