# 特権コマンド

対象リポジトリのテストがroot権限やDockerデーモンを必要とする場合に、**その実行だけを使い捨てのVMへ切り出す**機構（ADR-0053）。メインのサンドボックスVM（AIセッションが動く側）は`ubuntu`ユーザーのままで、一般sudoもDockerデーモンも持たない。

宣言・承認のスキーマは`docs/design/config.md`、イメージエントリは`docs/design/images-and-rootfs.md`、VM起動の共通部品は`docs/design/sandbox-vm.md`を参照。ここが書くのは、それらを繋いで1回の実行が成立するまでの流れ。

## 宣言と承認

`.masuda/settings.json`の`privilegedCommands`が宣言、`.masuda/settings.local.json`の`privilegedCommands`が承認。操作は`masuda privileged-command list|approve <name>|reject <name>`（`cmd/masuda/privilegedcommand.go`）。

`approve`は承認を記録する前に宣言を検証する（`validatePrivilegedCommandDecl`）。

- `command`が空でない
- `image`が空でなく、エントリ名として正当で、`.masuda/images/<image>/Dockerfile`が実在する
- `timeoutSeconds`が負でない
- `outputs`の各要素が相対パスで、`..`でワークスペースの外へ出ない（`config.ValidateOutputPath`）

記録される`DeclHash`は`config.PrivilegedCommandHash(repoRoot, decl)`で、**宣言のハッシュとイメージディレクトリの内容（`Dockerfile`とイメージ側`settings.json`）のダイジェストを合成したもの**。`list`は宣言・承認・ハッシュを突き合わせ、実行要求が通る状態かをそのまま表示する。

## 実行

`internal/sandbox.RunPrivilegedCommand`（`internal/sandbox/disposablevm.go`）が1回の実行を最初から最後まで担う。呼び出し元は状態デーモンのcuratedツール（後述）。

1. `ResolveApprovedPrivilegedCommand`が宣言を引き、承認とハッシュ一致を確認する。ここで弾かれた要求はVMを起動しない
2. 実行ごとのディレクトリを作る: `<stateDir>/privilegedCommands/<name>/<run-id>/`。`run-id`はワークスペースIDと同じ形式の乱数（`workspace.NewRandomID`）。既存の実行を上書きしない
3. そのディレクトリへ実行指示を書く（`command`・`timeout-seconds`・`max-log-bytes`）
4. worktreeを`cp -a`でスナップショットする（VMが見るのはこのコピーで、ライブのworktreeではない）
5. rootfsをビルドする。イメージエントリのDockerタグから変換し、runnerスクリプト・そのunit・`multi-user.target.wants`リンク・ゲストカーネルのモジュールツリー全体を注入する
6. TAPを確保し、MACとリポジトリの対応をegressレジストリへ記録する（後述）
7. virtiofsdを2つ起動する（`workspace`=スナップショット、`masuda-results`=実行ディレクトリ）
8. cloud-hypervisorを起動し、VMが自分でpoweroffするまで待つ。宣言のタイムアウト＋余裕を過ぎたらVMを落とす（`hostTimeout`）
9. 実行ディレクトリから`exit-code`と`log`を読み、`outputs`を回収する
10. TAP・レジストリ・virtiofsd・スナップショット・rootfsイメージをすべて片付ける

VMが受け取らないもの: `/masuda-secrets`（Claude OAuthトークン）、MCPリレー、SSH鍵。対話アタッチの経路も無く、コンソールは実行ディレクトリの`console.log`へ落ちる。

### ゲスト側

`runtime/masuda-run.sh`と`runtime/masuda-run.service`。イメージのDockerfileには含まれず、masudaがrootfsへ注入する。

- `/masuda-results/command`を読み、`/workspace`をカレントディレクトリにして実行する
- 宣言にタイムアウトがあれば`timeout`コマンドで囲む
- 標準出力と標準エラーを`awk`のフィルタ経由で`/masuda-results/log`へ書く。上限に達した後も読み続けるため、コマンドがSIGPIPEで死ぬことはない
- exit codeを`/masuda-results/exit-code`へ書く。masuda自身の配線が失敗した場合（コマンドが配置されていない、`/workspace`が無い）は125を書く
- 最後に`systemctl poweroff`

unitは`After=multi-user.target`のみで、`docker.service`への依存を持たない——イメージにDockerが入っているとは限らないため。Dockerが在る場合、`docker.service`は`Type=notify`なので`multi-user.target`の到達がデーモン起動後であることを意味する。

有効化は`[Install]`ではなく、masudaが`etc/systemd/system/multi-user.target.wants/masuda-run.service`のシンボリックリンクを注入して行う（`systemctl enable`が書くものと同じ）。

## 結果と成果物

戻るのはexit codeとログ、そして宣言された成果物。

ログの上限は2段。ゲスト側の`awk`フィルタが実行ディレクトリのファイルを`maxPrivilegedLogBytes`（8MiB）で打ち切り、curatedツールが応答へ載せるのは末尾`maxToolLogBytes`（200KiB）まで。切れた続きはセッションが実行ディレクトリのファイルとして読める。

`outputs`の回収（`collectOutputs`）はVMが消えた後にホストが行う。

- 回収先は実行ディレクトリの`outputs/`。ライブのworktreeへは決して書かない
- **symlinkは辿らない。** 通常ファイルとディレクトリ以外はスキップし、スキップした事実を結果に載せる
- 合計`maxCollectedOutputBytes`（64MiB）・`maxCollectedOutputFiles`（1000件）を超えるとエラーにする。打ち切らない
- 回収先ディレクトリは作り直す（ゲストが同じ名前のディレクトリを自分で作れるため）
- タイムアウトした実行でも回収する

実行ディレクトリは自動削除されない。ワークスペースの状態ディレクトリごと、ワークスペース削除時に消える。

## AIセッションからの呼び出し口

curated MCPツール`run_privileged_command(name)`（`internal/statedaemon/mcpserver/curated.go`）。入力は宣言の名前だけで、コマンド文字列を渡す口は無い。

状態デーモンは`--repo-root`と`--worktree-dir`の両方を渡された場合にのみこのツールを登録する（`runStatedaemon`）。実行の実体は`cmd/masuda/statedaemon.go`の`privilegedRunner`が注入する。

戻り値は`exitCode`・`log`（切り詰め済み）・`truncated`・`resultsDir`（**メインVMから見たパス**、`/masuda-state/privilegedCommands/<name>/<run-id>`）・`outputs`・`outputsError`・`timedOut`。

## ネットワーク

使い捨てVMはワークスペースではないため、egressプロキシの「IP→DHCPリース→MAC→ワークスペース」という解決に載らない。実行中だけMACとリポジトリの対応を`$XDG_DATA_HOME/masuda/privileged-vms/<mac>`へ記録し、`ResolveWorkspaceByIP`がワークスペースに一致しなかった場合に参照する（`internal/sandbox/egressproxy.go`）。許可リスト自体はそのリポジトリの`egressAllowlist`をそのまま使う（`docs/design/egress-filter.md`）。

レコードには監督プロセスのpidが入っている。プロセスが消えているレコードは不在として扱われ、参照時に削除される。

## 既知の問題

- **ツール呼び出しは実行が終わるまでブロックする。** 長いテストスイートではMCPクライアント側のタイムアウトに当たる可能性がある。実機で確認できているのは30秒程度の実行まで
