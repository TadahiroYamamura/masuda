# CLI

`cmd/masuda`が実装するホスト側CLI（`masuda`コマンド）の配線と、各サブコマンドが内部で何を行うか。個々のサブコマンドの網羅的な引数リファレンスは`--help`を参照。各機構の内部設計（ゲート・レビュー・設定ファイル・状態デーモン等）は該当する設計ドキュメントを参照し、本書はCLI層（`cmd/masuda/*.go`）が何をどの順で呼ぶかに閉じる。

## ルートコマンドの組み立て

`newRootCommand`（`cmd/masuda/main.go:37`）がcobraのルートコマンドを組み立てる。トップレベルへ直接ぶら下がるのは`init`・`internal`（隠しコマンド、後述）・`workspace`・`sandbox`・`chat`・`update`・`triage`・`mcp`の各コマンドグループ。`plan`・`review`は`newGateCommand(gate.Name)`（`cmd/masuda/gate.go`）が組み立てる共通のshow/approve/rejectサブコマンド群に、`plan`側は`plan start`、`review`側は`review start`をそれぞれ追加登録したもの——「plan/reviewのゲート操作」と「plan/reviewの開始」はコード上別ファイル（`plan.go`/`review.go`）の別関数で、`main.go`がこの2つを1つのコマンドグループへ合流させている。

## 共通ヘルパー

- **`resolveImage`/`resolveBase`**（`cmd/masuda/main.go:92,142`）: `--image`/`--base`（または`--into`）系フラグの解決。フラグが明示的に渡されていれば（`cmd.Flags().Changed`）その値を最優先で返し、渡されていなければ`.masuda/settings.json`の値、それも空なら呼び出し側が渡した組み込みデフォルトを返す。優先順位はCLIフラグ＞`settings.json`＞デフォルトの一本の連鎖。スキーマ・`settings.json`側の詳細は`docs/design/config.md`を参照
- **`completeWorkspaceIDs`**（`cmd/masuda/main.go:115`）: 先頭引数が`<workspace-id>`であるほぼ全サブコマンド（chat、plan/review/triage show|approve|reject等、sandbox start|stop、workspace merge|remove等）が共有するシェル補完関数。`internal/workspace.List`が返す現在のリポジトリのワークスペース一覧からprefix一致するIDを返すだけで、リポジトリ外や一覧取得失敗時は補完候補なしにフォールバックする（エラーを表面化しない）
- **`repoRoot`**（`cmd/masuda/main.go:68`）: `git rev-parse --show-toplevel`でカレントの masuda チェックアウトのルートを引く。worktree自体ではなく、worktreeの作成元になる「メインチェックアウト」を指す
- **`gateStateDir`**（`cmd/masuda/gate.go:40`）: ワークスペースIDから状態ディレクトリを引く、plan/review/triageのゲート操作コマンド共通の入口。解決に使うのはIDだけで`repoRoot`を経由しない（状態ディレクトリはリポジトリ外のグローバルな場所にあるため）——したがってゲート操作はgitリポジトリの外からでも動く。`review approve`が反映先とするリポジトリも同様に`workspace.json`の`repo_root`から引く（`finalizeReviewApproval`）

## `masuda chat`: セッションアタッチ

`newChatCommand`（`cmd/masuda/chat.go:21`）はゲート名を引数に取らない唯一のセッション操作コマンド。1ワークスペースにつき生きているtmuxセッションは常に高々1つだが、それがDiscovery/Blueprint段階のホストループ（`internal/hostloop`）にあるかScaffold/Build/Review段階のサンドボックスVMにあるかは呼び出し時点では分からない——plan gateは初回到達時はホストループ側で待つが、Build段階での再オープン（ADR-0010の逸脱検知）はサンドボックス側で待つため、同じ「plan gate待ち」でもどちらのセッションが生きているかが変わる。そのため`hostloop.IsRunning(id)`を先に試し、なければ`sandboxBackend.IsRunning(id)`を試すという順に固定して判別し、見つかった方の`AttachArgs`を`syscall.Exec`で自プロセスに被せる（`attach`ヘルパー、`cmd/masuda/gate.go:164`）。どちらも生きていなければエラーで、次に打つべきコマンド（`plan start`か`sandbox start`)を案内する。

## 利用者向けコマンド

### `masuda init`

`.masuda/`を対象リポジトリへ一度きり展開する（`settings.json`・`reviews/`・`Dockerfile`）。既に存在すればエラーで拒否し、再実行や差分反映はしない。取得元Releaseの選び方・各ファイルの中身は`docs/design/distribution-and-update.md`の「初期化CLI」節を参照。

### `masuda workspace`

`create <branch>`は新規ワークスペースID発行→`workspace.Create`（状態ディレクトリ作成）→`worktree.Create`（git clone）→`startDaemon`（状態デーモンを検知プロセスとして起動）の順で行う（`newWorkspace`、`cmd/masuda/workspace.go:44`）。この関数は`plan start`・`review start`の「新規」経路とも共有される、新しいワークスペースを起こす際の唯一の入口。`merge`/`remove`/`list`/`info`/`rebase`/`rename`はワークスペースIDから状態・worktreeを引いて操作するだけで非自明な分岐はない——`rebase`は`review approve`のfast-forward失敗時に人間が明示的に叩く手動リカバリで、`review approve`自体には組み込まれない（非fast-forwardの判断は人間に委ねる）。worktree・状態ディレクトリ・ワークスペースIDの実体は`docs/design/workspace.md`を参照。

### `masuda sandbox`

`start <workspace-id>`/`stop <workspace-id>`はVMBackend（`internal/sandbox.VMBackend`）へそのまま委譲する。内部手順・仕組みは`docs/design/sandbox-vm.md`を参照。

`build`は`.masuda/images/`配下の全エントリを最新公開Releaseに対して単体で再ビルドするコマンドで、`masuda update`の3ステップ（バイナリ更新→イメージ再ビルド→レビュー観点同期）のうちイメージ再ビルドの部分（`rebuildImagesForRelease`）だけを`masuda update`から独立して呼ぶ。手順は次の通り。

1. `config.ListImageEntries`が空ならエラー（`masuda update`と違い、無言でスキップしない——明示的にbuildを頼んだ以上、エントリが無いのはユーザーの誤りとして報告する）
2. `FetchLatestRelease`で最新Releaseを取得
3. 各エントリの`Dockerfile`のFROM行のタグを最新Releaseのタグへ書き換える（`selfupdate.UpdateDockerfileFromTag`。masudaのbaseイメージを参照していない行は書き換えない）
4. `docker build --pull`で各エントリをビルドし、`config.ImageTag`が導出するタグを付ける（`selfupdate.RebuildDockerfile`）

`masuda update`のイメージ再ビルドステップは対象プロジェクトが未初期化なら無言でスキップするのに対し、`build`は`masuda update`が持つ「稼働中ワークスペースがあれば全ステップ拒否」という機械全体の締め出しを経由しない——それはCLIバイナリ置換のために存在する制約で、プロジェクト単位のイメージ再ビルドには関係がないため。詳細な共有ロジックは`docs/design/distribution-and-update.md`を参照。

### `masuda image`

`add <entry> [--template default|docker]`が`.masuda/images/<entry>/`を作り、`list`が宣言済みエントリと導出されるDockerタグ・rootfsサイズを並べる（`cmd/masuda/image.go`）。`add`は既存エントリを上書きしない。テンプレートとエントリの実体は`docs/design/images-and-rootfs.md`を参照。

### `masuda privileged-command`

`list`/`approve <name>`/`reject <name>`。`masuda mcp`・`masuda egress`と同型で、対象リポジトリの宣言に対するこのユーザーの承認を`.masuda/settings.local.json`へ記録する。承認が固定するもの・実行の流れは`docs/design/privileged-commands.md`を参照。

### `masuda chat`

前述。

### `masuda update`

masuda自身のCLIバイナリ置換→対象プロジェクトの`.masuda/images/`配下の全エントリ再ビルド（宣言があれば）→`.masuda/reviews/`への新規組み込み観点の追加同期（存在すれば）、の3ステップを順に実行する。稼働中ワークスペースが1つでもあれば全ステップとも実行せず拒否する。詳細は`docs/design/distribution-and-update.md`を参照。

### `masuda plan`

`show|approve|reject <id>`はゲート共通コマンド（`newGateCommand`）が生成する。中身・マーカーの消費タイミングは`docs/design/gates.md`を参照。

`start <branch-or-workspace-id> [task]`は第一引数が既存ワークスペースID（`workspace.Exists`）かどうかで新規/再開を判別する——ブランチ名では判別しない。同じブランチに対して複数のワークスペースが並行して存在できる設計（ADR-0014・ADR-0030）のため、再開はワークスペースIDを名指しする必要があり、ブランチ名だけでは一意に定まらない。

- 新規（第一引数が未知のID＝ブランチ名として扱う）: `task`か`--file`の**少なくとも一方**が必要（`taskBriefFor`）。`--file`だけを渡した場合、`internal:task-brief`には指示書の存在と次節への参照だけを述べた既定文が入る（CLIのフラグ名は含めない——読み手はコマンドラインを見ないサブエージェントなので、フラグ名は解決できない文字列でしかない）——investigateプロンプトは`## タスク内容`の直後に指示書の検証節（ADR-0016）を置くので、実体はそちらが持ち、briefはそこへの受け渡しだけを担う。**入力が既存の文書（過去の調査レポート、保存したGitHub Issue等）であることが多いため、文書だけで成立する形にしてある。**`newWorkspace`でワークスペースを起こし、`--file`があれば投入する調査済み文書を`hostloop.WriteInstructions`で書き込む（調査サブエージェントはこれを鵜呑みにせず実コードと突き合わせてから`INVESTIGATION.md`を作る、ADR-0016）。`--base`/`--name`/`--file`はいずれもこの新規経路でのみ有効で、再開経路に渡すとエラーになる
- 再開（第一引数が既存ID）: 上記フラグはすべて拒否。`startDaemon`（既に生きていれば無視される冪等呼び出し）のあと`hostloop.Start`をtaskなしで呼び、オンディスクの状態から続きを進める

ホストループの内部（Discovery/Blueprint段階の自己ループ）は`docs/design/discovery-blueprint.md`を参照。

### `masuda review`

`show|approve|reject <id>`もゲート共通コマンド。`approve`だけは`n == gate.Review`のとき追加で`finalizeReviewApproval`（`cmd/masuda/gate.go:119`）を実行する——サンドボックス停止（起動中なら）→`worktree.Commit`（Review段階のfixerが加えた分だけ。Build段階の各ステップは既に個別commit済み）→`worktree.Pull`（fast-forwardのみ）→`worktree.Remove`（`deleteBranch=false`でブランチ自体は残す、ADR-0023）→`workspace.Remove`、の順。いずれかが失敗すると後続は実行されない。詳細は`docs/design/gates.md`を参照。

`start <branch-or-ref>`は既存の（新規作成ではない）ブランチに対し、Provision〜Reviewの機構を使い回してReviewだけを単体実行する入口。`branch-or-ref`が存在しなければエラー（`masuda plan start`と違い、存在しないブランチを新規作成することはしない）。`seedReviewOnly`で`implementation_result.json`を`{"status":"done"}`で事前投入することでBuild段階を丸ごとスキップし、直接サンドボックスを起動してReviewへ入る。中身の詳細（diff基準refの違い、機械的バックストップが自動スキップされる理由）は`docs/design/review.md`「レビュー単体実行」節を参照。

### `masuda triage`

`show|dismiss|redo|halt <id>`。`dismiss`/`redo`はそれぞれ`gate.Approve`/`gate.Reject`のエイリアス、`halt`だけ専用の`gate.Halt`を呼び、サンドボックス停止・worktree操作を一切行わない（自動再開経路を持たせない設計、ADR-0029）。中身は`docs/design/gates.md`「triage gate」節を参照。

### `masuda mcp`

`list|approve|reject <server-name>`。ワークスペース単位ではなくリポジトリ直下の`.masuda/settings.local.json`を直接操作する。`approve`は`--env KEY=VALUE`（繰り返し可、既存キーへは上書きでなくマージ）で不足する環境変数値を埋められる。中身は`docs/design/mcp-child-servers.md`を参照。

### `masuda egress`

`list`・`approve <hostname>|--all`・`reject <hostname>`。`mcp`と同じdeclare/approve構造（`.masuda/settings.json`の`egressAllowlist`が宣言、`.masuda/settings.local.json`の`egressAllowlist`がこのユーザーの承認）で、`mcp`同様ワークスペース単位ではなくリポジトリ直下のファイルを直接読み書きする。`mcp approve`の`--env`に相当するフラグは無い——ホスト名エントリには埋めるべき可変値が無いため。`approve`は`.masuda/settings.json`側に未宣言のホスト名を渡すとエラーになる。承認は稼働中のVMへ即座には伝わらず、対象ワークスペースのVMを再起動して初めて反映される（`approve`自身がその旨を出力する）。宣言・承認がサンドボックスVMのegressフィルタへどう反映されるかは`docs/design/egress-filter.md`を参照。

`masuda init`が生成する`settings.json`は、**サンドボックス内のClaude Code自身が到達できないと起動しないホスト**（`api.anthropic.com`・`platform.claude.com`、`cmd/masuda/init.go`の`requiredEgressHosts`）を`egressAllowlist`に宣言する。egressプロキシは既定拒否でホスト全体のフォールバックを持たないため（`internal/sandbox.NewEgressAllowlistFunc`）、これが無いとゲストの`claude`が証明書検証エラーで20秒ほどで終了し、理由もどこにも出ない。プロキシ側の常時許可にせず宣言として置くのは、サンドボックスが何に到達してよいかを対象リポジトリ自身が明示する形を保つため（ADR-0031）。

宣言は承認ではないので、initの直後に`masuda egress approve --all`が要る。`--all`はプロジェクトが宣言した全ホストを承認するショートカットで、宣言内容を読まずに済ませるためのものではない（`masuda egress list`で確認してから使う）。

### `masuda claude`

`set-token`のみ。標準入力から`claude setup-token`のOAuthトークンを読み、`internal/sandbox.SetClaudeOAuthToken`でホスト全体に1つ保存する（VMゲストへの渡し方は`docs/design/sandbox-vm.md`）。標準入力から読むのは、トークンがシェル履歴や`ps`の出力に残らないようにするため。

未登録のままVMを起動すると、ゲストの`claude`が認証できずに即終了し、`masuda-loop.service`が「loop complete」を報告する——実際にループが1周した場合と区別が付かない。このため`sandbox start`・`review start`は起動前に`warnIfNoClaudeToken`（`cmd/masuda/claude.go`）で警告を出す。**エラーにはしない**: トークンが無くてもVM自体は起動し、SSHでの調査や特権コマンドの実行には使えるため。

### `masuda vm-ssh-key`

`rotate`のみ。masudaインストール単位（ホスト全体で1組）で持つVMゲスト接続用SSH鍵ペアを再生成する。ローテーションは既にビルド済みのrootfsイメージ・起動中のVMには遡って反映されない。

## 内部コマンド（`masuda internal ...`）

masuda自身のコード（Go CLI・ホスト上で動く`orchestrator/*.py`）だけが呼ぶ配管用コマンド群。サンドボックスVMの中からは呼ばれない——rootfsに`masuda`バイナリ自体が入っていない（ADR-0057）。`newInternalCommand`（`cmd/masuda/statedaemon.go`）に`Hidden: true`でぶら下がり、`--help`には出ない。**人間が直接叩くものはこの下に置かない**——`claude set-token`・`vm-ssh-key rotate`は`docs/INSTALLATION.md`の手順として人間が叩くため、ここではなくトップレベルに置いてある。

- **`statedaemon`**: ワークスペースの状態デーモンをフォアグラウンドで起動する。通常は`workspace create`が`startDaemon`（`cmd/masuda/statedaemon.go:153`）でこのコマンド自身をデタッチしたサブプロセスとして起動する形でのみ動き、人間が直接打つことは想定していない。状態デーモン自体の中身は`docs/design/state-daemon-mcp.md`を参照
- **`state get|put|delete|list|apply`**: 状態デーモンのtrusted MCPツールをワンショットで叩く薄いCLIラッパー（`get/delete <key>`・`put <key> <value>`・`list <prefix>`・`apply`はopのJSON配列をstdinから読む）。`orchestrator/*.py`（Python）が自前のMCPクライアントを持たずに済むよう、1操作につき1回このサブコマンドをsubprocess起動する形で使う
- **`mcp-relay`**: `<bind>:<port>`のTCP接続をUnix domain socketへバイト単位で中継するだけのプロキシ。Claude Codeの`--mcp-config`がUDSを直接指せない制約を回避するために存在する。ネットワーク経路の詳細は`docs/design/networking.md`を参照
- **`rootfs build`**: Dockerイメージのファイルシステムをbootableなext4ディスクイメージへ変換する。VMのrootfsを作る手順の一部。詳細は`docs/design/images-and-rootfs.md`を参照

