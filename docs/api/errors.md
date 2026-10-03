# エラーコード

各RPCが失敗したときに返す[Connectのエラーコード](https://connectrpc.com/docs/protocol/#error-codes)と、その条件。コードの意味は契約（`docs/design/contracts.md`「エラーコードの約束」）が定め、この一覧はRPCごとの条件をそれに沿って並べたもの。クライアントはこのコードで分岐してよい。メッセージ（`message`）は人間向けの英語で、文言は変わりうるので分岐に使わない。

## コードの使い分け

| コード | HTTP | 意味 | クライアントがすること |
|---|---|---|---|
| `invalid_argument` | 400 | 要求そのものが誤り。何度送っても同じ結果になる | 入力を直す。定義の読み込み・検査の失敗、宣言に無い名前、未定義のワークフロー名、ゲートの種類に合わない`outcome`もここ |
| `not_found` | 404 | 指したもの（ワークスペース・ゲート・質問・rev・パス）が無い | 一覧を取り直す |
| `failed_precondition` | 400 | 要求は正しいが、今の状態では受け付けられない。設定ファイル（`settings.json`・`settings.local.json`）が読めないのもここ | 状態を取り直してから、条件を満たして送り直す（Stopしてから、承認してから、設定ファイルを直してから、等） |
| `already_exists` | 409 | 作ろうとしたものが既にある（publishするワークフローのRunのブランチ） | 別の名前にする |
| `out_of_range` | 400 | `Watch`の`after_seq`が最新のseqより大きい（serveの再起動で番号が振り直された等）、または`after_seq`の次のイベントが再送用に持つ直近10000件に残っていない | `after_seq: 0`で繋ぎ直し、最初の`status`で状態を組み立て直す（`after_seq: 0`は常に通る） |
| `unimplemented` | 501 | その構成では提供しない（フェイクsandboxの`AttachInfo`） | 機能を出さない・別の手段を案内する |
| `unavailable` | 503 | `masuda-sandbox serve`に届かない | sandbox serviceの起動を案内する（`masuda doctor`） |
| `canceled` | 499 | 要求が取り消された（クライアントの切断等） | 必要なら送り直す |
| `internal` | 500 | ホスト側の読み書きの失敗など、masudaの側の問題 | 利用者に見せ、`masuda serve`のログを確かめてもらう |
| `unknown` | 500 | 分類していない失敗（sandbox serviceからの想定外のエラー等） | `internal`と同じ |

- HTTP+JSONで呼ぶときは、ステータスでなく本文の`code`で分岐する（`invalid_argument`と`failed_precondition`はどちらも400）
- サーバーストリーミング（`Watch`・`GetBlob`・`BuildImage`）のエラーは、HTTP 200のまま終わりのエンベロープで届く（[接続](connect.md#streaming)）。ストリームの途中でもエラーで終わりうる
- `failed_precondition`の多くは、`Run`・`Resume`の前提（秘密・承認・イメージ等）の不足。足りないものは1回の応答に`; `区切りでまとめて入る

## すべてのRPCに共通

- ワークスペースのIDを受け取るRPCは、そのIDのワークスペースが無ければ`not_found`。IDとして不正な文字列（空文字列を含む）も`not_found`
- `repo_root`を受け取るRPC（`Run`・ConfigService・WorkflowService）は、絶対パスでない・gitの作業ツリーでない・作業ツリーのトップでない（サブディレクトリを渡した）なら`invalid_argument`。WorkflowServiceは`repo_root`を空にでき、そのときは同梱の定義だけを読む。ConfigServiceの`SetSecret`・`ListSecrets`も空にでき、そのときはユーザー単位の秘密（Claudeのトークン）を扱う
- ホスト側のファイルの読み書きに失敗すれば`internal`

## WorkspaceService

| RPC | コード | 条件 |
|---|---|---|
| `Run` | `invalid_argument` | `repo_root`が不正。`workflow`か`branch`が空。`.masuda/`を読めない。定義が読み込めない・検査で問題がある（問題の一覧がメッセージに入る）。`workflow`が定義に無い。ワークフローの`inputs`が足りない。`.masuda/reviews/`の観点ファイルのfrontmatterが読めない。`settings.json`の`agents`に定義に無い役の名前がある。ブランチ名が不正。`base`が実リポジトリに無い。既存のブランチで分岐元を決められない（`base`を渡す） |
| | `failed_precondition` | `settings.json`・`settings.local.json`が読めない（JSONとして壊れている、知らないキーがある、`stallAfter`が不正）。起動に要るものが足りない: Claudeのトークン・宣言した秘密の値が無い、`plaintext`の秘密が未承認、イメージのDockerfileが無い、`envFiles`の公開値が`vars`に無い、ワークフローが使う`checks`が宣言されていない。sandbox serviceの契約（`GetServerInfo`の`contract_sha256`）がmasudaと違う、または`GetServerInfo`を持たない古いsandbox（理由に両方のバージョンが入る） |
| | `unavailable` | sandbox serviceに届かない（ワークスペースは作らない） |
| | `already_exists` | ワークフローがpublishを含み、`branch`が実リポジトリに既にある（publishを含まないワークフローは既存のブランチで動かせる） |
| | `canceled` | stagingを作っている間に要求が取り消された |
| | `internal` | ワークスペース・stagingの作成、engineの開始に失敗した |
| `Resume` | `not_found` | ワークスペースが無い |
| | `failed_precondition` | 再開できる状態でない（STOPPEDと、sandboxの起動に失敗したBLOCKEDだけが再開できる。engineが止めたBLOCKEDは`Stop`した後も再開できない）。既に動いている。定義の写しが無い・読み込めない。定義の写しの`settings.json`や`settings.local.json`が読めない。定義の写しの`settings.json`の`agents`に定義に無い役の名前がある。起動に要るものが足りない、sandbox serviceの契約が違う（`Run`と同じ） |
| | `unavailable` | sandbox serviceに届かない（状態は変えない） |
| | `internal` | 実行の窓口の用意・質問の破棄の記録に失敗した |
| `Get` | `not_found` | ワークスペースが無い |
| `List` | `internal` | 一覧を読めない。`repo_root`は検査しない（一致するものが無ければ空の一覧） |
| `Watch` | `not_found` | `id`を指定し、そのワークスペースが無い（ストリームの最初に終わる） |
| | `out_of_range` | `after_seq`が最新のseqより大きい、または`after_seq+1`が再送できる最古のseqより小さい（続きが再送用の直近10000件に残っていない。メッセージに再送できる最古のseqが入る）。どちらもストリームの最初に終わる。`after_seq: 0`で繋ぎ直す |
| | （正常な終わり） | `masuda serve`が止まると、エラーでなく正常な終わりでストリームが閉じる |
| `Stop` | `not_found` | ワークスペースが無い |
| | `failed_precondition` | DONE（publish・discardで終わっていて、止めるものが無い）。STOPPEDへの`Stop`はエラーにせずそのまま返す。engineが止めたBLOCKEDへの`Stop`はVMを片付けてBLOCKEDのまま返す |
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

すべてのRPCは、最初に`repo_root`の検査と設定の読み込みをする（`repo_root`が空の`SetSecret`・`ListSecrets`を除く。リポジトリを読まない）。

- `repo_root`が不正なら`invalid_argument`
- `.masuda/settings.json`・`.masuda/settings.local.json`が読めない（JSONとして壊れている、知らないキーがある）なら`failed_precondition`

| RPC | コード | 条件 |
|---|---|---|
| `ListEgress` | | 共通のものだけ |
| `ApproveEgress` | `invalid_argument` | `host`が`settings.json`の`egress`に宣言されていない |
| `RejectEgress` | `invalid_argument` | `host`が宣言にも承認にも無い（宣言から消えたホストの承認は取り消せる） |
| `ListSecrets` | | 共通のものだけ |
| `SetSecret` | `invalid_argument` | `name`が宣言した秘密でもClaudeのトークンの名前でもない（`repo_root`が空なら、名前の形が不正）。`value`が空 |
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
| `Show` | `invalid_argument` | `workflow`が空。`repo_root`が不正。定義が読み込めない。図にできない。`workflow`が定義に無い |
| `Check` | `invalid_argument` | `repo_root`が不正。`workflow`を指定し、それが定義に無い |

`Check`は、定義が読み込めないときもエラーにせず、その理由を`problems`の1つとして返す。
