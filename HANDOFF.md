# HANDOFF
## 作業項目
M6（設定と秘密）。完了。コミット: `3683aeb`（config・secrets・ConfigService・Run時の結線）、`de9b0d2`（CLI）、このHANDOFFの更新
- `.masuda/settings.json`（`internal/config.Settings`、知らないフィールドは拒否）
  ```
  {
    "image": "default",                        // .masuda/images/<entry>/Dockerfile。省略時default
    "egress": ["api.linear.app", "*.x.com"],   // ホスト名。先頭の`*.`だけワイルドカード。ポートは不可
    "secrets": [{"name": "LINEAR_API_KEY", "hosts": ["api.linear.app"],
                 "mode": "placeholder|plaintext",   // 既定placeholder。placeholderはhosts必須
                 "in": ["header", "body"]}],         // 既定[header]
    "envFiles": [{"path": ".env", "vars": ["LINEAR_API_KEY", "PUBLIC_URL"]}],  // /workspaceからの相対、.git/の下は不可
    "privilegedCommands": {"itest": {"image": "default", "command": "...", "inputs": ["build/**"],
                                     "outputs": ["out.txt"], "timeoutSeconds": 60}},  // 検証・実行はM7
    "checks": {"test": "go test ./..."},
    "claudeSettings": {...}                    // JSONオブジェクト
  }
  ```
  秘密名・変数名は`^[A-Za-z_][A-Za-z0-9_]*$`、`CLAUDE_CODE_OAUTH_TOKEN`は予約（宣言不可）。チェック名・イメージのエントリ・特権コマンド名は`^[A-Za-z0-9][A-Za-z0-9._-]*$`
- `.masuda/settings.local.json`（`config.LocalSettings`、0600で原子的に保存、知らないフィールドは拒否）
  ```
  {
    "egressApproved": ["api.linear.app"],
    "secretsApproved": ["LEGACY"],                       // plaintextの承認（手編集。下の「契約への提案」）
    "privilegedCommandsApproved": {"itest": {"declHash": "<sha256>"}},
    "claudeToken": "CLAUDE_CODE_OAUTH_TOKEN",            // 使うトークンの秘密ストア上の名前
    "vars": {"PUBLIC_URL": "http://..."}                 // envFilesの公開値
  }
  ```
  - envFilesの公開値の出所は**settings.local.jsonの`vars`**に決めた。環境変数にしなかったのは、serveが常駐プロセスでCLIを叩いたシェルの環境を持たないため
  - 無活動しきい値はまだ`masuda serve --stall-after`のまま（work-ordersでM9の項目）
- `internal/secrets`: `<DataDir>/secrets/<repo-hash>/<NAME>`（ファイル0600・ディレクトリ0700、tmp→rename）。`repo-hash`は`filepath.Clean(repo_root)`のsha256先頭16桁（Runと同じく`repoTop`を通した値）。Claudeトークンは`ClaudeToken(repo, name)`で読み、無ければ`<DataDir>/claude-oauth-token`（M4の暫定）を読む。前後の空白は落とす。値を読み戻すAPIは無い
- `ConfigService`（`serve/config.go`）: 宣言は作業ツリーの`.masuda/settings.json`を読む
  - egress: 一覧は宣言の順。未宣言のApproveはInvalidArgument。Rejectは宣言か承認のどちらかにあれば受け付ける（宣言から消えたホストの承認の掃除）
  - secrets: 一覧は宣言の順で、Claudeトークンは含めない（C-M6が件数を見る）。`approved`はplaintextなら承認の有無、placeholderは承認不要なのでtrue。SetSecretは宣言した名前と`CLAUDE_CODE_OAUTH_TOKEN`／`claudeToken`の名前だけ、空値はInvalidArgument
  - 特権コマンド: 名前順。`approved`は記録したdeclHashが今の宣言と一致、`stale`は記録があって不一致。Approveは今の宣言のハッシュを記録（未宣言はInvalidArgument）
  - images: `.masuda/images/*/Dockerfile`を持つエントリを列挙。build_idは`<DataDir>/images/<repo-hash>/<entry>.json`（最後のビルドの記録）から、`built`はsandboxのListImagesにそのbuild_idがあること。BuildImageは作業ツリーの`.masuda/images/<entry>/`を文脈にsandboxのBuildImageを呼び、ログ行を流して最後に`build_id`
- `Run`・`Resume`の結線（`serve/settings.go`の`bootPlan`）
  - Run: 定義の写し→`checkDefinitions`→`planBoot`→ワークスペース作成。Resume: `records/definitions/`から`planBoot`し直す（承認・値は再開のたびに作業ツリーと秘密ストアから読む）
  - `planBoot`が足りないものを**まとめて**FailedPreconditionで返す: plaintextの未承認（名前を含む、C-M6）・値の無い秘密・`vars`に無い公開値・ワークフローが`/masuda/checks/<名前>`で呼ぶのに未宣言のチェック・Dockerfileの欠落・（実VMのみ）Claudeトークンの欠落。ワークスペースは作らない
  - チェックの使用は`Set.Reachable`で届くワークフローのexecノードの`command[0]`が`/masuda/checks/`で始まるかで見る（同梱`develop`は`checks.test`を要る）
  - egress: 上限は宣言∩承認（`config.AllowedEgress`）＋Claude API。`Runner.SetPolicy`が`CheckPolicy`でノードの`egress:`・`secrets:`を検査し、外れていれば`runner.ErrPolicy`を返す→engineのエラーとしてBLOCKED、Reasonにホスト名と`masuda egress approve`の案内
  - 秘密: placeholderの宣言とClaudeトークンを`CreateSandbox.secrets`へ（`in`→`substitute_in`）。ノードの`secrets:`はplaceholderのものだけ`enabled_secrets`に入れる（plaintextは常にenvにあるので選んでも何もしない）。plaintextの値は`CreateSandbox.env`
  - イメージ: bootのたびに定義の写しの`images/<entry>/`でsandboxの`BuildImage`を呼び（同じOCIダイジェストならsandbox側で再利用）、`build_id`を`CreateSandbox`へ。ログは`records/image-build.log`。`Workspace.Meta.Image`には解決したエントリを書く
  - ゲスト: `~/.claude/settings.json`は`claudeSettings`にmasudaのフックを重ねる（masudaが使う5イベントは置き換え、他のイベントのフックは残す）。envFilesは`/workspace/<path>`に0600で生成（秘密はプレースホルダ、plaintextは実値、公開値は`vars`。値はダブルクォートでエスケープ）し、`/workspace/.git/info/exclude`に足す。checksは`/masuda/checks/<名前>`に0755で`#!/bin/sh -e`＋`cd /workspace`＋コマンド
  - プレースホルダ（トークン以外）はtmuxのメインセッションのenvと、execノードのenv（`Runner.SetGuestEnv`、起動のたびに設定し直す）に入る
  - 定義の写し（`copyDefinitions`）から`settings.local.json`を除いた
- CLI: `init`（serve不要、ローカルで書く。既にあるファイルは上書きしない）、`egress list/approve/reject`、`secret list/set`（値は標準入力。端末ならエコーなし＝`golang.org/x/term`を追加、パイプなら末尾の改行1つを落とす）、`privileged-command list/approve`、`image list/build`。いずれも`--repo`（既定`.`）
  - initが置くもの: `settings.json`（egressに`api.anthropic.com`、`checks.test`は「未設定」と出して失敗する雛形）、`images/default/Dockerfile`（スパイクのものからsudo・python3・調査用ツールを抜いた雛形）、`reviews/`に14観点、`.gitignore`に`.masuda/settings.local.json`
- テスト: `internal/config`・`internal/secrets`・`cmd/masuda/init_test.go`・`serve/settings_test.go`（envFiles/checks/claudeSettingsの配置、まとめて拒否、未承認egressでBLOCKED、承認済みは通る）。`serve`のテスト用リポジトリにDockerfileを足した（Runが要るため）
## 完了した契約テスト
C-M1〜C-M6すべて緑（`go test -count=1 ./contract/ -run 'TestCM1|TestCM2|TestCM3|TestCM4|TestCM5|TestCM6'`、`-race`でも緑）。`go build ./...`・`go vet ./...`は通る。C-M7は想定どおり赤（`run_privileged_command`が未実装）
## 未完と理由
- plaintextの秘密の承認はsettings.local.jsonの`secretsApproved`を手で編集するしかない（契約にRPCが無い。下の提案）。拒否のエラー文はそう案内する
- Claudeトークンの設定状況はAPIから見えない（ListSecretsに含めるとC-M6の件数と食い違う）。未設定ならRunのFailedPreconditionで分かる
- 実VMでのBuildImage・envFiles・checksの実行は未確認（M8）。フェイクのExecは絶対パスのargvを写像しないので、フェイクで`/masuda/checks/test`を実行するとホストの`/masuda/...`を探して失敗する（M7/M8でexecノードを通すなら要確認）
- `docs/design/`への反映はしていない（settings.local.jsonの`vars`・`claudeToken`、bootPlanの拒否条件、イメージ記録の置き場所）
- 対象リポジトリの`.gitignore`が`.masuda/`ごと無視している場合（このリポジトリ自身がそう）、initの雛形はコミットされない。initは警告しない
- 前回から持ち越し: ResumeのWIP復元、会話ログのexport、`publish target: remote`の送り先、観点の`enable`（いずれもwork-ordersのM9等）
## 次の一手
1. M7（特権コマンド）。C-M7は`runCtl.RunPrivilegedCommand`から
2. M8の前に、フェイクのExecで絶対パスのargv（`/masuda/checks/<名前>`）をどう扱うか決める
## 注意点
- 特権コマンドの宣言は`config.PrivilegedCommandDecl`（command・image・inputs・outputs・timeoutSeconds）。承認の判定は`config.DeclHash(decl) == local.PrivilegedCommandsApproved[name].DeclHash`。フィールドを足すとハッシュが変わり既存の承認はstaleになる（安全側）。`ValidateOutputPath`はM1から残してある
- 実行時の宣言は`runCtl.plan`ではなく定義の写し（`records/definitions/settings.json`、`config.LoadDir`）から読むこと。承認は作業ツリーの`settings.local.json`（`config.LoadLocal(w.RepoRoot)`）。特権VMのイメージは`b.buildWorkspaceImage(ctx, w, entry)`で同じようにビルドできる（定義の写しに`images/`がある）
- `planBoot`はRunの同期部分で呼ぶので、遅い処理（ビルド等）を入れないこと。ビルドはbootの中
- egressの判定は`config.HostAllowed`（完全一致か、許可側の`*.suffix`）。engineはノードの`egress:`に`host:port`を許すが、settings.jsonはポートを許さないので、ポート付きのノードは常にBLOCKEDになる
- `Runner`のOptions `Egress`・`Secrets`・`Plaintext`は`newRunCtl`が`plan`から入れる。`newRunCtl`の引数に`plan`が増えた
- `configService.mu`はsettings.local.jsonの読み→書きを直列にする。承認を書く口を足すならこのロックの中で
## 契約への提案
- `ConfigService`にplaintextの秘密を承認・取り消すRPC（例: `ApproveSecret(NameRequest) returns (ListSecretsResponse)`）が無い。overviewは「`plaintext`モードはローカルの承認が無ければ起動を拒否」としているが承認する口が手編集しか無い。CLI（`masuda secret approve`）を出すにはRPCが要る
- `SecretEntry.approved`はplaintext用と書かれているが、placeholderのときの値が決まっていない。今はtrue（承認不要）を返している。明記するか、`approval_required`を足すとUIが迷わない
- Claude APIのトークンの設定状況を返す口が無い（ListSecretsは宣言だけ、C-M6が件数を固定）。`ListSecretsResponse`に`claude_token_set`を足すか、トークンを別の項目で返す案
