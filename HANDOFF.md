# HANDOFF
## 作業項目
**M11b（利用者向け`docs/user/`）**。M11c（`docs/api/`・`clients/`）は別セッションが並行して書いており、このセッションは触っていない（`mkdocs.yml`の`nav`の`api`セクションも触っていない）。
- `6bb47ab` 入口・導入・はじめての1周・概念（`user/index.md`・`install.md`・`quickstart.md`・`concepts.md`）
- `20cf9d4` CLIと設定ファイルのリファレンス（`user/cli.md`・`settings.md`）
- `1ca8c7e` ワークフローとレビュー観点（`user/workflows.md`・`reviews.md`）。developとreviewの図は`masuda workflow show`の出力をそのまま貼った
- `a179f4d` 秘密・egress・特権コマンド、運用、トラブルシューティング（`user/secrets-and-egress.md`・`operations.md`・`troubleshooting.md`）
- `15b26f6` `docs/INSTALLATION.md`・`docs/CONTRIBUTING.md`を削除。開発者向けの中身は`docs/design/README.md`の「開発環境」へ。`exclude_docs`から2ファイルを外し、`README.md`のリンクと使い方（`masuda-sandbox serve --socket`）を直した
- `mkdocs.yml`の`nav`の「利用者向け」: index→導入→はじめての1周→概念→CLI→設定→ワークフロー→ワークフロー定義の仕様→レビュー観点→秘密・egress・特権コマンド→運用→トラブルシューティング
## 完了した契約テスト
契約ファイル（両proto、`docs/guest-protocol.md`）・契約テスト・コードは触っていない。`scripts/docs-prepare.sh && mkdocs build --strict`は最終コミットで通る（INFOはM11aからの`api/reference.md`の`#google-protobuf-Timestamp`1件だけ）。途中のコミット（`6bb47ab`〜`1ca8c7e`）はまだ無いページへのリンクがあり、単体ではstrictで落ちる
## 未完と理由
- VMを起動する手順（`masuda-sandbox`の`pnpm install && pnpm build`と`serve`、`claude setup-token`、`masuda image build`、実VMでの`run`→`watch`→`gate show/approve`→publish、`resume`、実VMへの`chat`、特権コマンド）は打っていない（指示どおり）。live_test.goとM8のHANDOFFの記録に合わせて書いた
- 確かめた手順: `masuda --help`・`serve -h`・`run -h`・`version`、一時リポジトリでの`masuda init`（2回目の「nothing to do」と`.gitignore`）、`--fake-sandbox`のserveに対する`workflow list/check/show`（同梱と自作の例）、`egress list/approve`（宣言外のエラー）、`secret set`（パイプ）・`list`、`image list`、`privileged-command list`、`run`（成功と、ブランチ既存・入力不足・未定義ワークフローのエラー）、`list`・`list --all`、`watch`の初回status、`gate list`、`question list`、`stop`、`remove`（`--force`の要否）、`chat`（フェイクでのエラー）、`settings.json`の知らないキーのエラー、`ubuntu:24.04`に`resize2fs`があること
- 確かめていない記述: macOSの手順、KVMのグループ設定、ツールごとのCAバンドルの環境変数（`REQUESTS_CA_BUNDLE`等）の案内、`masuda-sandbox images prune`、DockerfileのENV（PATH以外）が今のsandbox＋masudaでチェック・エージェントに届くか
## 次の一手
1. ユーザー（M11aから持ち越し）: `redesign`をpushし、Actionsの「docs」を`workflow_dispatch`で実行→Settings → Pagesで`gh-pages`・`/ (root)`を選ぶ。`https://tadahiroyamamura.github.io/masuda/dev/`で見られる
2. 実機の環境で`docs/user/quickstart.md`を頭から通し、出力例（`masuda list`・`watch`・`gate show`）を実物に差し替える
3. 下の「実装と文書の食い違い」のうち実装側のものを直す（直したら`docs/user/`の警告を消す）
4. M12（設定の整理）で`stallAfter`・`diskWarnBytes`が`config.json`へ移ったら、`user/settings.md`・`operations.md`・`troubleshooting.md`の該当箇所も同じコミットで直す
## 注意点
- 生成物（`docs/api/reference.md`・`docs/user/reference/workflow-schema.md`）はコミットしない。`mkdocs build`の前に`scripts/docs-prepare.sh`
- 表のセルに`|`を書かない（M11aの注意）。`docs/user/`の全表で列数を機械的に確かめてある
- ページ間のリンクは日本語見出しの自動ID（`#_2`等）に頼らず、`{#id}`で明示したアンカーへ張っている。見出しを変えるときはIDを残す
- `user/workflows.md`の2つのMermaid図は`masuda workflow show`の出力の貼り付け。engineの同梱定義が変わったら貼り直す
- 実装と文書の食い違い（括弧内はどちらを直すべきか）:
  - **観点の`enable`が効かない**（実装を直す）: frontmatterの`enable`はmasuda（`perspectives.Merge`・`runner.perspectiveItems`）もengine（trigger-matcher）も読まない。`enable: false`でも使われ、同梱の観点を外す手段も無い。`user/reviews.md`に警告を書いた
  - **`workflows/review`を単独で動かすと差分が空**（設計・実装を直す）: `staging.Create`はブランチを分岐元と同じコミットに置き、`diff`は`refs/masuda/base`からなので、既存の変更をレビューする手段が無い。例えば「既存のブランチを起点にし、`--base`を差分の基準にする」実行の形が要る。`user/workflows.md`に警告を書いた
  - **引数無しの`masuda workflow check`が同梱の定義で終了コード1**（実装を直す）: 全ワークフローをrootとして検査するので、部品（`implement/build-step`・`fix-finding`）で「承認済み計画が無い」が出る。engineの仕様（rootは「どのワークフローのReachableにも含まれないもの」）に合わせ、rootだけを検査すべき。`overview.md`第9章の「全ワークフローをそれぞれrootにして検査し」も合わせて直す
  - **`run --base`のヘルプ**（実装の文言を直す）: 「空ならリポジトリの既定のブランチ」とあるが、実装は今チェックアウトしているブランチ（detachedならそのコミット）。文書は実装どおりに書いた
  - **雛形Dockerfileのコメントが古い**（実装＝雛形を直す）: masuda-sandboxのS10以降、イメージのENVはExecへ引き継がれ、読み取り専用ディレクトリのあるイメージのビルド（`-modcacherw`）も直っている。一方masudaは`guest.BaseEnv`でPATH・XDG_*をExec.envに上書きし、チェックは`sh -el`なのでENVのPATHは効かない。雛形のコメントと`guest.BaseEnv`の要否（sandboxのS10 HANDOFFは「不要になった」と書いている）を見直す。文書は「PATHは効かない、それ以外は確実な渡し方を勧める」で書いた
  - **publishの`target: remote`の送り先**（設計文書か実装を直す）: `overview.md`第4章は「設定したremoteへpush」だが、実装（`runner.Publish`）は常に`origin`で、設定の項目は無い。利用者向けは実装どおり`origin`と書いた
  - **`overview.md`第9章のCLI表**（文書を直す）: `masuda list`の`--repo`が載っていない
  - **Claudeのトークンがリポジトリごと**（提案）: 秘密ストアはリポジトリの絶対パスで分かれるので、リポジトリごとに`secret set CLAUDE_CODE_OAUTH_TOKEN`が要り、移動すると失われる。M4の暫定ファイル`<DataDir>/claude-oauth-token`へのフォールバックが残っているが、文書には書いていない。マシン全体の既定のトークン（M12の`config.json`等）を検討する価値がある
  - **雛形の`egress: ["api.anthropic.com"]`**（提案）: Claude APIは常に許可なので宣言・承認は意味を持たない。quickstartでは「無くても動く」と書いて承認の手順を残した。雛形から外すか、意味を持たせるかの判断
  - **エージェントの`feedback`が実行ログに出ない**（提案）: `finish`イベントにoutcomeしか無く、out_of_scope・stuckの理由は`records/engine.json`を掘るしかない。`execution-log.jsonl`の`detail`に載せると利用者が読める
## 契約への提案
- なし（このセッションで契約に関わる新しい発見は無い）。M11aからの持ち越し: `contracts.md`「通信の前提」のループバック待ち受け（M12で実装予定）、serve全体の設定の置き場所（M12）、engineのfixerの終わり方（engine側で`cannot_fix`が入っている）

<!-- 以下は M11c（別worktree）の記述 -->

M11c（統合開発者向け`docs/api/`とTypeScriptクライアント）。
- `2cc8354` `clients/ts/`: `@masuda/api-client`（private、公開しない）。`buf.gen.yaml`はnode_modulesの`protoc-gen-es`（v2.16、package-lock.jsonで固定）で`target=js+dts`・`import_extension=js`を`gen/`へ。生成物と`package-lock.json`をコミット。依存は`@bufbuild/protobuf`、peerに`@connectrpc/connect`。`npm run generate`・`npm run check`（tscで生成物と`examples/`を型検査）。`examples/node-uds.ts`（connect-nodeでUDSへ`nodeOptions.socketPath`）、`examples/browser.ts`（connect-web、再接続つきWatch）。READMEに入れ方・`createClient(WorkspaceService, createConnectTransport({baseUrl, httpVersion: "1.1"}))`・ストリーム・エラー・型の注意
- `03afd37` `docs/api/`: `index.md`（入口・読む順）、`connect.md`（UDS/`listen`、認証が無いことと共用マシン・リモートの注意、Connect/gRPC/gRPC-Web、JSONの形、`fetch`の単発とサーバーストリーミングのエンベロープの読み方、curl、TS/Goの生成）、`services.md`（6サービスの各RPCの目的・前後関係・状態遷移図・stagingのref一覧）、`flows.md`（起動とWatch・seq/afterSeq/初回status/空のworkspace_id/再接続、ゲートのsubject・target_hash・staging_commit・deviationのapproved_files・triage、差分ビュー、質問、Activityの表示指針、Stop/Resume/Remove、設定の画面の順）、`errors.md`（RPCごとのコードと条件）。`mkdocs.yml`のnavは`api`セクションだけ変更
- `.github/workflows/docs.yml`は変更不要（clients/tsは生成物をコミット済みで、サイトのビルドに関係しない）
## 完了した契約テスト
契約テスト・契約ファイル・`serve/`等の実装・`docs/user/`は触っていない。`scripts/docs-prepare.sh && mkdocs build --strict`は通る（`.venv-docs/`を新規作成。INFOは既知の`#google-protobuf-Timestamp`の1件だけ）。fake sandboxの`masuda serve`（worktreeでは`go.mod`の`replace ../masuda-engine`が解決できないので、絶対パスに直したgo.modを`-modfile`で渡してscratchpadにビルド）に対して、エラーコード・JSONの形・Watchのエンベロープ・fetchの例・TSクライアント（examples/node-uds.tsと、`npm pack`したtarballを別プロジェクトへ入れたもの）を実際に動かして確かめた
## 未完と理由
- ブラウザからの例（connect.mdの`fetch`、clients/tsのbrowser.ts）はループバックの待ち受けが前提で、`listen`はM12で入るまで実装に無い。文書は指示どおり「listenを設定したときだけループバックでも待ち受ける。既定はUDSのみ」と書いた
- QuestionService・BuildImage・特権コマンドの承認のエラーは実装の読み取りだけで、実際に投げてはいない
## 次の一手
1. 監督: 下の「契約への提案」のエラーコード一覧をprotoのコメントへ反映するか判断する。反映したら`docs/api/errors.md`と揃える
2. M12: `listen`の実装と同じコミットで、`docs/api/connect.md`のCORSの記述（「同じホストで開いたページのオリジンを許す」）を実装に合わせて直す。`ServeNotice`で`disk-warning`を流すようにしたら、`docs/api/flows.md`「workspaceIdが空のイベント」の「今のserveはengineで流す」を消す
## 注意点
- `clients/ts`は`masuda.proto`を変えたら`cd clients/ts && npm ci && npm run generate && npm run check`で作り直してコミットする。CIでの差分検査は無い
- 文書中のリポジトリ内ファイルへのリンク（clients/ts、proto）はGitHubの`main`ブランチのURL。`redesign`が`main`に入るまでは404になる
- `docs/api/`の見出しへのリンクは`{#id}`の明示idを使う（日本語だけの見出しは自動idが空になる）
- 実機で気づいた点: 出現IDは7桁（`"0000001"`）。HTTP+JSONで`FailedPrecondition`と`InvalidArgument`はどちらも400なので、文書では`code`で分岐するよう書いた
## 契約への提案
### RPCごとのエラーコード（protoのコメントへの反映案。詳細の条件は`docs/api/errors.md`）
- 共通: ワークスペースIDが無い・不正→NotFound。`repo_root`が絶対パスでない・作業ツリーのトップでない→InvalidArgument。ホストのI/O失敗→Internal
- Workspace.Run: InvalidArgument（必須欠落、定義の読み込み・検査の失敗、未定義のworkflow、inputs不足、settings.jsonが壊れている、ブランチ名不正、baseが無い）／FailedPrecondition（秘密・トークンの値、plaintextの承認、Dockerfile、vars、checks、settings.local.jsonの不備。`; `区切りでまとめる）／AlreadyExists（ブランチが実リポジトリにある）／Canceled。起動失敗はエラーでなくBLOCKED（reason`sandbox boot failed: `）
- Workspace.Resume: NotFound／FailedPrecondition（再開できない状態、実行中、定義の写しが無い・読めない、前提不足）／InvalidArgument（写しのsettings.jsonが壊れている）
- Workspace.Get: NotFound。List: Internalのみ（repo_rootは検査しない）
- Workspace.Watch: NotFound（指定idが無い。ストリームのエラー）。serveの停止は正常な終わり
- Workspace.Stop: NotFound／FailedPrecondition（DONE）。STOPPEDは冪等に成功
- Workspace.Remove: NotFound／FailedPrecondition（動いていてforce無し）
- Workspace.AttachInfo: NotFound／FailedPrecondition（sandbox無し・起動中）／Unimplemented（SSH非提供）／その他はsandboxのコードを透過、届かなければUnavailable
- Gate.ListOpen・Get: NotFound（ワークスペース、その出現のゲート）
- Gate.Decide: InvalidArgument（outcome空）／NotFound／FailedPrecondition（判断済み、approvedのtarget_hash不一致、ワークスペースが動いていない、今待っているゲートでない、ゲートの種類に合わないoutcome、approved_filesにゲートが挙げていないファイル）／Unimplemented（deviationにapproved・rejected以外）
- Question.ListOpen: NotFound。Answer: NotFound（開いた質問が無い）／InvalidArgument（答えの過不足・選択肢外）／FailedPrecondition（動いていない、engineが拒否）
- Staging: GetCommit・Diff・GetBlob・ListComments・AddCommentはrevが空（Diffのto、AddCommentのcommit、GetCommit・GetBlobのrev）・`-`始まり→InvalidArgument、解決できない→NotFound。GetBlobはパスがファイルでない→NotFound。AddCommentのbody空→InvalidArgument
- Config: 共通でsettings.json・settings.local.jsonが読めない→FailedPrecondition。宣言に無いhost・name、placeholderへの承認・取り消し、空のvalue、壊れた特権コマンドの宣言→InvalidArgument。BuildImageのentry不正→InvalidArgument、Dockerfile無し→NotFound。ListImages・BuildImageのsandboxの失敗はsandboxのコードを透過（記録の失敗はUnknown）
- Workflow: List・Showは定義の読み込み失敗→InvalidArgument。Show・Checkは未定義のworkflow→NotFound。Checkは読み込み失敗を問題の1つとして返す
### コードの揃え方の提案
- 未定義のworkflow: RunはInvalidArgument、Show・CheckはNotFound。どちらかに揃える
- 壊れたsettings.json: RunはInvalidArgument、ConfigServiceはFailedPrecondition。どちらかに揃える
- ゲートの種類に合わないoutcome（未知の文字列を含む）: deviationはUnimplemented、他はFailedPrecondition。serve側でゲートの種類を見てInvalidArgumentに揃えるのが妥当
### 実装と文書（契約）の食い違い
- `Gate.staging_commit`のprotoコメントは「diffを計算したstagingのコミット/tree」だが、`target=diff`の`subject`は`refs/masuda/base`から作業ツリー（`refs/masuda/worktree`）までの差分で、`staging_commit`はブランチ先端。deviationで加えなかったファイルは`subject`に出るがpublishされない（fakeで確認: subjectにnotes.txt、staging_commitはb.goのみ）。人間が見て承認する内容とpublishされる内容がずれる。subjectをbase..staging_commitにするか、コメントを直すかの判断
- triageの`dismiss`・`redo`で割り込まれた出現が入り直すと、元の出現のゲートの記録が未判断のまま残り、`ListOpen`・`Workspace.open_gates`に出続ける（DONE後も）。それへのDecideはFailedPrecondition。入り直したときに古いゲートを閉じる（理由付き）べき
- Watchの`after_seq`: 再送バッファ（10000件）より古い続きを求めても、serve再起動で番号が振り直された後に古い番号を渡しても、黙って続く（後者は番号が追いつくまで何も届かない）。OutOfRangeを返すか、初回statusを送り直すかの判断
- `Stop`はengineが止めたBLOCKEDもSTOPPEDにするので、その後`Resume`が通り、VMを起動してからまたBLOCKEDになる。overview.mdの「engineが止めたBLOCKEDは再開できない」と食い違う。Stopを断るか、Resumeで弾くか
- `StagingService`のprotoコメントは「agent findings and human notes share this model」だが、今コメントを書くのは`AddComment`（author `human`）だけで、エージェントの所見（findings）はコメントにならない。`severity`は常に空
- 既知の持ち越し: `ServeNotice`は出さず`EngineEvent{kind: "disk-warning"}`で流す（M12）。`contracts.md`のループバック待ち受けは未実装（M12）
- 前回からの持ち越し（判断状況はこちらでは未確認）: sandboxの応答前に切られたHTTPリクエストに終わりのイベントが無い、sandboxのExecの既定環境、engineのfixerに「直せない」終わり方が無い
