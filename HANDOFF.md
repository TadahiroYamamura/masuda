# HANDOFF
## 作業項目
M7（特権コマンド）と、契約追加3件（`ApproveSecret`/`RejectSecret`・`SecretEntry.approval_required`・`ListSecretsResponse.claude_token_set`）。完了。コミット: `1499172`（秘密の承認RPC・CLI）、`fd5bb1e`（特権コマンド）、このHANDOFFの更新
- 生成コード: `go run github.com/bufbuild/buf/cmd/buf@latest generate`で作り直した（bufはローカルに無い。remoteプラグインなのでネットワークが要る）。`gen/masuda/sandbox/v1/sandbox.pb.go`はsandbox側のコメント更新が入っただけ
- 秘密の承認（`serve/config.go`）: `ApproveSecret`はplaintextだけ（placeholder・未宣言はInvalidArgument）、`settings.local.json`の`secretsApproved`へ。`RejectSecret`はRejectEgressと同じく宣言から消えた名前の承認も掃除できる。`approval_required`はplaintextでtrue。`claude_token_set`は`secrets.Store.ClaudeToken`（M4の暫定ファイルも見る、Runと同じ判定）。CLI: `masuda secret approve|reject <NAME>`、`secret list`の末尾に`Claude token: set|unset`。planBootの拒否文も`masuda secret approve`を案内する
- 特権コマンド
  - `internal/privileged`: `Validate`（command非空・imageのDockerfileが実在・timeoutSeconds非負・inputs/outputsは相対で`..`/`.`/空セグメント不可）、`Match`（`**`対応の自前glob。ディレクトリ名だけのパターンは中身に当たらない＝`dir/**`と書く）、`Run`（下の手順）
  - `serve/privileged.go`の`runCtl.RunPrivilegedCommand`: 宣言は定義の写し（`records/definitions/settings.json`）、承認は作業ツリーの`settings.local.json`、`DeclHash`照合。未宣言・未承認・stale（承認後に宣言が変わった）はMCPのエラーで、文面に`masuda privileged-command approve <name>`を出す。ワークスペースごとに`privMu`で1つずつ
  - 手順: run-idは`records/privileged/`の連番4桁（`0001`）→ `Runner.SnapshotNamed`で今の作業ツリーを`refs/masuda/wip/privileged-<run-id>`へ → `buildWorkspaceImage(decl.image)` → `CreateSandbox`（id `<ws-id>-p<run-id>`、`default_user: root`、egressは`config.AllowedEgress`（Claude APIは足さない）、秘密・tcp_maps・env無し）→ stagingのbundleを`/masuda/privileged.bundle`へ、`git init`→`fetch +ref:ref`→`checkout --detach`（cloneはブランチ以外のrefを取らないため）、`safe.directory`を設定 → inputsをメインVMで`find -printf '%m %p\0'`して`ReadFile`→`WriteFile`（許可ビットを保つ）→ `Exec`（root、cwd `/workspace`、shell、timeout。0なら1時間）。stdout/stderrは届いた順に1本のログ、末尾200KiB → outputsを特権VMで列挙して回収 → ホスト`records/privileged/<run-id>/{exit-code,log,result.json,outputs/...}`、メインVM`/masuda/privileged/<run-id>/{exit-code,log,outputs/...}` → `DestroySandbox`（defer、ctx切り離し）
  - 戻り値: `exit_code`（シグナルなら-1）・`log`・`truncated`・`results_dir`・`outputs`（回収できた相対パス）・`outputs_error`（当たらなかったパターン・読めなかったファイル。コマンドの失敗とは独立）・`timed_out`・`signal`（契約の表に無い。下の提案）
  - 実行中は30秒ごとに活動を記録する（無活動判定に引っかからないように）
  - `ApprovePrivilegedCommand`も承認前に`privileged.Validate`する。未使用になった`config.ValidateOutputPath`は削除
- フェイクsandbox: `user: "root"`（明示か既定ユーザー）のExecは`unshare -Urm`の中でホストの`/usr /etc /dev /opt /var /run`（と`/bin`等のシンボリックリンク／ディレクトリ）をゲストrootへbindしてchrootして動かす。`id -u`が0になり、`/workspace/...`の絶対パスもゲストのものになる。このホスト（WSL2）では`unshare -Ur`が使える。ゲストrootにマウントポイントの空ディレクトリが残るが害は無い
- テスト: `internal/privileged`（glob・検証）、`serve/privileged_test.go`（失敗コマンドの終了コード・ログ・outputs_error・ホストとゲストの記録・未承認・stale）、`serve/config_test.go`（秘密の承認）、`internal/fakesandbox`（rootのExec）
## 完了した契約テスト
C-M1〜C-M7すべて緑（`go test -count=1 ./contract/`、`-race`でも緑）。`go build ./...`・`go vet ./...`・`go test ./...`は通る
## 未完と理由
- serveが特権コマンドの途中で落ちると特権sandbox（`<ws-id>-p<run-id>`）が残る。Stop・serve停止はctxの取り消し→deferのDestroyで消えるが、クラッシュ後の掃除（ListSandboxesで`<ws-id>-p`を探して壊す）は入れていない
- 特権コマンドはexecノード（engineの`CommandTask`）からは呼べない。MCPツールだけ（契約どおり）
- `docs/design/`への反映はしていない（`records/privileged/`の構成、`outputs_error`の意味、timeoutの既定、フェイクのroot実行）。前回からの持ち越し（settings.local.jsonの`vars`・`claudeToken`等）も同じ
- 前回から持ち越し: ResumeのWIP復元、会話ログのexport、`publish target: remote`、観点の`enable`、フェイクで絶対パスargv（`/masuda/checks/<名前>`）を実行できない件（rootのExecだけはchrootで解決したが、ubuntuユーザーのExecは従来どおり写像しない）
## 次の一手
1. M8（実機1周）。`MASUDA_LIVE_TEST=1 go test ./live/`
2. 実機で特権コマンドを1回通す（下の注意点）
## 注意点
- 実機のゲストのclaudeはMCPツールの呼び出しにタイムアウトがある（M4で`next_task`で問題になった）。特権コマンドは数分〜1時間かかりうるので、`MCP_TOOL_TIMEOUT`等の設定が足りているか確認すること。タイムアウトでリクエストが切れるとctxが取り消され、特権VMは壊されて結果は返らない（記録の`result.json`には`error`が残る）
- inputs・outputsの列挙はGNU findの`-printf`に頼る。initの雛形Dockerfile（Ubuntu系）なら入っているが、alpine系のイメージでは動かない
- 実VMの`Exec.shell`は`/bin/sh -lc`。特権VMのrootのログインシェルの設定が宣言のコマンドに影響しうる
- 特権VMのイメージにもgitが要る（checkoutに使う）。`/masuda`ディレクトリが無いイメージでもWriteFileが親を作る前提（sandboxのPREPAREの挙動を実機で確認）
- 特権VMのWriteFileは既定ユーザー（root）所有で書く。メインVMの`/masuda/privileged/`はubuntu所有
- `Runner.SnapshotNamed`は`guestMu`を取る。engineのスナップショットと並ぶと待つ
## 契約への提案
- `docs/guest-protocol.md`の`run_privileged_command`の戻り値の表に`outputs_error`が無い（監督の指示にはある）。実装は返している。あわせて、シグナルで終わったときの`signal`も返しているので表に足すか、不要なら消す判断をください。`outputs_error`の意味（当たらなかったパターン・読めなかったファイル。コマンドの終了コードとは独立）も一行あると迷わない
