# HANDOFF
## 作業項目
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
