# HANDOFF
## 作業項目
**M12（設定の整理と、ドキュメント整備で見つかった不備）**。work-ordersのM12の箇条書きすべてと「M12（旧）」を消化した（M12（旧）の節は削除）。
- `f471098` review gate: `Runner.Diff`の`DiffCommitted`（`refs/masuda/base..refs/heads/<branch>`、作業ツリーを取り込まない）、`Runner.Log`の`decision`/`superseded`でその出現の最後のゲート記録を閉じる、review gate（`target: diff`）を開くとき`findings`を`staging_commit`へのコメントに取り込む（authorはidの観点名か`cross-cutting`、二重取り込みは元のidで防ぐ）、ゲートの種類に合わないoutcomeは`InvalidArgument`、`gate show`は`superseded`を「triageで無効」と出す
- `fa2e4ea` 既存ブランチでのRun: rootから`Set.Reachable`で辿れるワークフローにpublishノードが無ければ`AllowExisting`。stagingのブランチは実リポジトリのその先頭、`refs/masuda/base`は分岐元（`--base`、無ければ既定ブランチ: originのHEAD→`init.defaultBranch`→main→master→今のブランチ、ブランチ自身は除く）とのmerge-base。`run --base`のヘルプを実装に合わせた
- `959b9bd` エラーコード: Show・Checkの未定義ワークフロー→`InvalidArgument`、Run・Resumeの読めない`settings.json`→`FailedPrecondition`（Configと同じ）、Watchの`after_seq`>最新→`OutOfRange`。`docs/api/errors.md`を契約の表に合わせ「揃っていないところ」を削除
- `9686ded` 観点の`enable: false`を重ねた後で除く（同梱も外せる。frontmatterが読めなければRunで`InvalidArgument`）、引数なし`workflow check`はroot（他のReachableに含まれないもの）だけ、engineが止めたBLOCKEDへの`Stop`はVMを片付けて状態をBLOCKEDのまま残す（Resumeは`FailedPrecondition`のまま）
- `faf5963` `$XDG_CONFIG_HOME/masuda/config.json`（`listen`・`sandboxSocket`・`stallAfter`・`diskWarnBytes`、`masuda serve --config`）。`listen`はループバックIPのみ、CORSは任意オリジン（Originを返す、プリフライトは要求ヘッダーをそのまま許す）、加えてHostヘッダーがループバック名でない要求は403（DNS rebinding対策）。stallAfterは`--stall-after`>settings.local.json>config.json>10m。`diskWarnBytes`はconfig.jsonだけ（settings.local.jsonでは知らないキー）。disk-warningは`ServeNotice{kind, detail, value=使用量}`。`buf generate`で生成し直した（sandbox側の生成物も`GetServerInfo`等が増えた。clients/tsは既にServeNoticeを含み差分なし）
- `d0f0cfb` Claudeトークンのユーザー単位: `<DataDir>/secrets/_user/<NAME>`。読む順はリポジトリ→ユーザー→M4の暫定ファイル。APIは`SetSecret`・`ListSecrets`の`repo_root`が空ならユーザー単位（契約は変えていない）。CLIは`CLAUDE_CODE_OAUTH_TOKEN`を`--repo`無しで登録するとユーザー単位
- `a5b8fe2` `settings.json`の`publish.remote`（既定origin）、init雛形のegressを空にし`masuda init`の出力で「Claude APIは常に許可」と伝える（JSONにコメントを書けないため）、雛形Dockerfileのコメントを今の実態に、overviewのCLI表に`list --repo`
- 文書は各コミットで同時に直した（`user/reviews.md`・`workflows.md`の警告削除、`api/connect.md`のlisten/CORS、`api/errors.md`、`api/flows.md`・`services.md`、`overview.md`第4・6・8・9章、`user/settings.md`に`config.json`の節、`design/README.md`の対応表）
## 完了した契約テスト
C-M1〜C-M8すべて緑（`go test -count=1 ./contract/`）。`go build ./... && go vet ./...`指摘なし、`go test -count=1 ./...`全部緑（liveはskip）。`scripts/docs-prepare.sh && mkdocs build --strict`は通る（INFOは既知の`api/reference.md`の`#google-protobuf-Timestamp`1件だけ）。契約ファイル（両proto、`docs/guest-protocol.md`）と契約テストのassertionは変更していない
## 未完と理由
- 実VMでの確認はしていない（指示どおり。`live/live_test.go`は監督が実行）。engineのHANDOFFの次の一手（実機1周でreview gateのSubjectと、triage後にゲート一覧から古いゲートが消えること）はliveで確かめる
- `guest.BaseEnv`（PATH・XDG_*_HOMEをExecのenvで上書き）の要否は見直していない。sandboxのS10以降は既定環境がPATHに`/usr/local/bin`と`~/.local/bin`を含みイメージのENVも引き継ぐので、BaseEnvのPATHはイメージのENVのPATHを隠している。外すとフェイクsandboxでの挙動も変わるので範囲外とした（雛形Dockerfileのコメントは今の実態どおり「PATHはmasudaが決める」と書いた）
- M11cからの持ち越し: QuestionService・BuildImage・特権コマンドの承認のエラーは実装の読み取りだけで実際には投げていない。Watchの`after_seq`が再送バッファ（10000件）より古いときは黙って最古から続く（OutOfRangeは「最新より大きい」ときだけ）
## 次の一手
1. 監督: 下の「契約への提案」のinterimゲートの件をengineで判断する
2. 監督: liveテストを実行する（このセッションの変更でサンドボックスイメージの再ビルドが要る変更は無い。Go側のみ）
3. ユーザー（M11aから持ち越し）: `redesign`をpushし、Actionsの「docs」を`workflow_dispatch`で実行→Settings → Pagesで`gh-pages`・`/ (root)`
4. 実機で`docs/user/quickstart.md`を頭から通し、出力例を実物に差し替える（quickstartの手順4は`secret set`だけになった）
5. M13（配布）
## 注意点
- work-ordersのM12は「壊れた`settings.json`は`Config`も`InvalidArgument`」と書いていたが、後から定めた`contracts.md`「エラーコードの約束」は「設定ファイルが読めない→`FailedPrecondition`（Run・Configで統一）」なので契約に従い、Run・Resume側を`FailedPrecondition`にした
- `listen`のCORSは任意オリジンなので、設定している間はブラウザで開いたどのサイトのスクリプトもポートを当てればAPIを呼べる（`api/connect.md`に注意として書いた）。Hostヘッダーの検査はwork-ordersに無い追加の防御
- この変更より前に`Stop`してSTOPPEDになったengine-BLOCKEDのワークスペースは、`Resume`が通ってしまう（起動後にengineがまたBLOCKEDを返す）
- `diskWarnBytes`を書いた既存の`settings.local.json`は知らないキーとして読めなくなる（v0.1前なので移行は用意していない）
- M13への引き継ぎ:
  - `masuda serve`は`config.json`を起動時に読む。`masuda doctor`で表示するなら`config.ServeConfigPath()`と`config.LoadServe`を使う。`sandboxSocket`もconfig.jsonで変えられるので、doctorのsandbox到達確認はconfig.jsonを読んでから行う
  - `buf generate`でsandbox側の生成物に`GetServerInfo`（`ServerInfo.contract_sha256`等）が入った。M13の互換性確認はこれを使える（今はどこからも呼んでいない）
  - Claudeトークンの有無の確認は`secrets.Store.ClaudeToken(repoRoot, name)`（repoRootが空ならユーザー単位だけ）
  - `docs/user/install.md`のトークンの節はユーザー単位の登録に書き換えた。Releaseのtarball中心に書き直すときに残す
- 生成物（`docs/api/reference.md`・`docs/user/reference/workflow-schema.md`）はコミットしない。`mkdocs build`の前に`scripts/docs-prepare.sh`
- `buf`はPATHに無い。`go run github.com/bufbuild/buf/cmd/buf@latest generate`で生成した（go1.26.8へ自動で切り替わる）
- `clients/ts`は`masuda.proto`を変えたら`cd clients/ts && npm ci && npm run generate && npm run check`で作り直してコミットする
## 契約への提案
- **interimゲートの承認対象が空になる（engine側、E10の帰結）**: E10は`target: diff`のゲートすべてで`Runner.Diff(DiffCommitted)`を使う。同梱の`implement/build-step`の`approve-interim`（`target: diff`）は`commit`より**前**にあるので、subjectの差分部分はそれまでのステップのコミット済みの差分だけで、このステップの変更はファイル名だけが「publishされない変更（未コミット）」の見出しの下に並ぶ（実際には承認するとコミットされる）。`target_hash`もこのステップの変更を含まない。人間が途中レビューで見るべきステップの差分が見えない。engineで、commitの前にあるゲートは従来の`DiffFromBase`（作業ツリー）か`step-diff`を使う、または`target: step-diff`のような別の対象を定義で選べるようにする、等の判断が要る。masuda側は`target: diff`のゲートでfindingsをブランチ先頭のコミットへ取り込むので、interimでは指摘の行番号がコメントの付くコミットの内容と合わないことがある（engineの判断に合わせて直す）。`docs/user/concepts.md`のinterimの説明（「中身はそこまでの差分」）はengineの判断が出るまで変えていない
