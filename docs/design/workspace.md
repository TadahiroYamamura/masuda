# ワークスペース

ワークスペースID・状態ディレクトリの管理（`internal/workspace`）、そのライフサイクルを操作する`masuda workspace`サブコマンド群（`cmd/masuda/workspace.go`）、そしてワークスペース専用のgitクローン作成・`.masuda`設定同期・ブランチ統合・commit/削除（`internal/worktree`）を扱う。

`.masuda/settings.json`自体のスキーマは`docs/design/config.md`、状態デーモンの起動・ソケット・KVストアは`docs/design/state-daemon-mcp.md`、ゲート承認時に`worktree.Commit`/`Pull`/`Remove`が呼ばれる一連の流れ（`finalizeReviewApproval`）は`docs/design/gates.md`、TDDモードのステップtag自体の書き込みは`docs/design/build.md`を参照。本書はそれらから呼ばれる`internal/worktree`側の実装のみを扱う。

## ワークスペースID・状態ディレクトリ

`internal/workspace/workspace.go`。

- **ID発行**: `NewID()`（`:124`）が3バイトの乱数を16進エンコードした6桁hex文字列を生成する。`Exists(id)`で既存ワークスペースとの衝突を確認し、衝突時は再試行する（`maxNewIDAttempts = 100`で打ち切り）。branch名は含まない（ADR-0030）。
- **状態ディレクトリ**: `StateDir(id)`（`:98`）は`DataHome()/workspaces/<id>`を返す。`DataHome()`（`:80`）は`$XDG_DATA_HOME`（未設定時`~/.local/share`）配下の`masuda`。対象リポジトリのworktree外に置かれる——masuda自身の制御ファイル（TASK.md・plan/・review_results/等）がこのディレクトリに集約される。
- **メタデータ**: `Info`構造体（`:37`、`ID`/`Name`/`Branch`/`Base`/`RepoRoot`/`CreatedAt`）が`workspace.json`としてJSON永続化される。`Create(repoRoot, id, branch, base, name)`（`:144`）が状態ディレクトリを作成し、`workspace.json`と`.masuda-base-ref`（`BaseRefFileName`、baseブランチ名だけを書いたプレーンテキスト——`orchestrator/*.py`がGo側のJSONをパースせず直接読むための専用ファイル）の2つを書く。
- **`Load(id)`**（`:167`）: `workspace.json`を読み戻す。`Exists(id)`（`:207`）は`Load`のエラー有無だけを見る——`masuda plan start <arg>`/`masuda review start <arg>`が`arg`を「既存ワークスペースIDとして再開」と「新規ブランチ名」のどちらとして扱うかを、この関数1つで判別している。
- **`Rename(id, name)`**（`:186`）: `Info.Name`だけを書き換える唯一の変更操作。`Name`はid/branch/baseと違い表示専用ラベルで、ワークスペースの名前解決には一切関与しない。
- **`List(repoRoot)`**（`:253`）/**`ListAll()`**（`:224`）: 状態ディレクトリは全リポジトリ共有の1箇所（`~/.local/share/masuda/workspaces/`）にフラットに並ぶため、`List`は`Info.RepoRoot`が一致するものだけへ絞り込むフィルタでしかない。`ListAll`は`masuda update`（ADR-0032、CLIバイナリ差し替え前の「進行中ワークスペースが無いか」チェック）がリポジトリを問わず全件を見る必要があるために存在する。どちらも`CreatedAt`降順ソート——IDが完全な乱数（ADR-0030）になったため、ディレクトリ名（`os.ReadDir`のファイル名順）はもはや意味のある順序を持たない。
- **`Remove(id)`**（`:271`）: 状態ディレクトリを`os.RemoveAll`するだけ。対応するgitクローン（`internal/worktree.Remove`）とサンドボックス/ホストループの停止は呼び出し側の責任——このパッケージは関知しない。
- **`Status(id)`**（`:287`）: `TASK.md`の先頭行（`#`見出し）を読んで返すだけの薄い関数。オーケストレーター（`orchestrator/*.py`）が毎ループ`TASK.md`を書き換えるため、フェーズ判定ロジックをGo側で再実装せずに済む。`workspace list`表示用。
- **`FormatEntries`**（`:314`）: `masuda workspace list`が表示する`text/tabwriter`整形テーブル。`EntryStatus`（`:305`）は`Info`に`TaskStatus`（`Status(id)`）と`Running`を足したもの——`Running`はこのパッケージでは計算しない（`internal/hostloop`・`internal/sandbox`を使う必要があるが、`internal/hostloop`が既に`internal/workspace`をimportしているため、逆方向にimportすると循環importになる。呼び出し元の`cmd/masuda`が計算して渡す）。

## `masuda workspace` CLI

`cmd/masuda/workspace.go`の`newWorkspaceCommand()`（`:24`）。サブコマンドは7つ。

| サブコマンド | 引数・フラグ | 実装 |
|---|---|---|
| `create <branch>` | `--base`（既定`develop`）、`--name` | `:67` |
| `merge <workspace-id>` | `--into`（既定`develop`） | `:95` |
| `remove <workspace-id>` | `--keep-branch` | `:122` |
| `list` | なし | `:235` |
| `info <workspace-id>` | なし | `:162` |
| `rebase <workspace-id>` | なし | `:215` |
| `rename <workspace-id> <name>` | なし | `:194` |

コマンドグループ名が`worktree`ではなく`workspace`である点に注意——`internal/worktree`はgitチェックアウト操作の実装詳細としてこのパッケージから呼ばれるだけで、CLIサブコマンドとしては露出しない。

### 新規開始の共有シーケンス

`newWorkspace(root, branch, base, name)`（`:44`）が、新規ワークスペースを始める全エントリポイント（`workspace create`・`plan start`・`review start`のうち再開ではない方）に共通の手順を持つ。

1. `workspace.NewID()`でID発行
2. `workspace.Create(root, id, branch, base, name)`で状態ディレクトリ・メタデータ作成
3. `worktree.Create(root, id, branch, base)`でgitクローン作成
4. `startDaemon(id)`で状態デーモンを起動（起動シーケンスの詳細は`docs/design/state-daemon-mcp.md`）

3が2の後に呼ばれる非atomicな順序であることは「既知の問題」節を参照。4が3より後段にあるのは意図的——2の完了を待たずに3が失敗すれば、起動済みデーモンをkillする後始末が余分に必要になる。

### 各サブコマンドの処理

- **`create`**: `resolveBase`（`.masuda/settings.json`の値とCLIフラグの優先順位解決、`docs/design/config.md`参照）でbaseを確定し`newWorkspace`を呼ぶ。標準出力に`workspace=<id> worktree=<dir>`を1回だけ出す——後から`create`の出力を見返す手段がなかったため、パス確認用に`info`サブコマンドが別途追加されている。
- **`merge`**: `workspace.Load`でIDからbranch/base情報を引き、`worktree.Merge(root, info.ID, info.Branch, resolvedInto)`を呼ぶだけ。
- **`remove`**: `stopDaemon(info.ID)`（state-daemon-mcp.md参照）→`worktree.Remove(root, info.ID, info.Branch, !keepBranch)`→`workspace.Remove(info.ID)`の順。`stopDaemon`を先に呼ぶのは、`workspace.Remove`が`daemon.pid`ごと状態ディレクトリを削除してしまうと、プロセスを見つけてシグナルを送る手段が失われるため。daemon.pidを持たない旧世代のワークスペースでも`stopDaemon`はエラーにならず、削除処理は止まらない。
- **`info`**: `worktree.Dir`（クローンの絶対パス）・`workspace.StateDir`（状態ディレクトリの絶対パス）・`workspace.Status`・`hostloop.IsRunning`または`sandboxBackend.IsRunning`を1画面にまとめて出す。`create`が一度しか出さないパス情報を後から引く手段として追加された。
- **`rename`**: `workspace.Rename`をそのまま呼ぶ薄いラッパー。
- **`rebase`**: `worktree.Rebase(root, info.ID, info.Branch)`をそのまま呼ぶ。
- **`list`**: `workspace.List(root)`の各エントリに`workspace.Status`と`Running`（`hostloop.IsRunning(info.ID) || sandboxBackend.IsRunning(info.ID)`）を付与し、`workspace.FormatEntries`で整形出力する。

## クローン作成・`.masuda`設定同期

`internal/worktree/worktree.go`。パッケージ名は`worktree`だが、実体は`git clone --local`によるワークスペース専用の自己完結クローンであり、`git worktree add`のリンクドworktreeではない（ADR-0018）。

- **`Dir(repoRoot, id)`**（`:42`）: クローンの配置先は`repoRoot/.masuda/worktrees/<id>`。
- **`Create(repoRoot, id, branch, base)`**（`:76`）: `Dir`が既に存在すればそのパスをそのまま返す（冪等、エラーにしない）。branchが既存なら`git clone --local --branch <branch> <repoRoot> <dir>`。branchが未作成なら`git clone --local --branch <base> <repoRoot> <dir>`してからクローン内で`git checkout -b <branch>`する。どちらの経路でも最後に`syncMasudaConfig(repoRoot, dir)`を呼ぶ。
- **`syncMasudaConfig`**（`:140`）: 次の3つをrepoRootの現在のワーキングツリー内容で**常に上書き**する（ADR-0036）。
  - `.masuda/settings.json`（`config.SettingsPath`）
  - `.masuda/reviews/`（`perspectives.ReviewsDir`。`copyDirIfExists`が先にdst側を`RemoveAll`してから丸ごとミラーする——リポジトリ側で削除された観点ファイルがクローン側に残り続けることを防ぐ）
  - `.masuda/.gitignore`（存在する場合のみ、`config.GitignorePath`）

  `git clone`は既にcommit済みの内容ならこれらを複製済みだが、これは「repoRoot側にまだcommitしていないローカル編集」（`.masuda/`をコミットする前にdogfoodingしている最中の設定変更など）も新規ワークスペースへ届けるための、常時無条件の上書きである。

  **明示的に対象外**:
  - `.masuda/settings.local.json`（`config.SettingsLocalPath`）: `config.MCPServerApproval.Env`など実秘密情報を持つファイル。クローンへコピーすると、`Commit`（後述）の無条件`git add -A`でワークスペースのブランチ履歴に秘密情報が漏れる経路になるため、意図的に同期対象から外している。状態デーモンは`workspace.Info.RepoRoot`経由でrepoRootから直接このファイルを読む。
  - `.masuda/Dockerfile`: `docker build`は常にrepoRootから直接読む（`cmd/masuda/sandbox.go`・`update.go`）。
  - `.masuda/worktrees/`: 自分自身（他ワークスペースのクローンを含む）を巻き込まないための除外。

## 自動化されるgit操作の範囲

worktreeのライフサイクル操作（作成・ローカル統合・削除）はすべてmasudaが自動で行ってよい。一方 **`git push`（およびPR作成・コメント投稿）はmasudaの自動化フローに一切含まれず、常に人間が別途明示的に行う**（ADR-0005）。`review approve` の後片付けにもpushは含まれない。

この境界はplan gate・review gateの存在理由とは別で、ゲートは「AIの設計判断が妥当かを人間が舵取りする」ために置かれている。

## ブランチ統合操作

用途の異なる3つの操作があり、混同しないこと。

| 操作 | 呼び出し元 | 方向 | 自動/手動 |
|---|---|---|---|
| `Merge` | `masuda workspace merge --into` | クローン→repoRootの任意ブランチへ`--no-ff`マージ | 手動 |
| `Pull` | `review approve`の`finalizeReviewApproval`（`docs/design/gates.md`） | クローン→repoRootの同名branchへfast-forward | 自動 |
| `Rebase` | `masuda workspace rebase` | repoRoot→クローン側へreplay（Pullと逆方向） | 手動 |

- **`Merge(repoRoot, id, branch, into)`**（`:210`）: repoRootに`into`が現在チェックアウトされていなければ拒否する（masudaはユーザー自身のチェックアウトを勝手に切り替えない）。`git fetch <cloneDir> +<branch>:<branch>`でクローン専用のbranchをrepoRoot側へ持ち込んでから、`git merge --no-ff branch -m "merge: <branch> into <into>"`。クローンのbranchはこの`fetch`が実行されるまでrepoRoot側からは見えない（リンクドworktreeと違い、クローンはrepoRootのref/オブジェクトストアを共有しない）。
- **`Pull(repoRoot, id, branch)`**（`:302`）: ADR-0023により`review approve`の自動フローで`Merge`の代わりに使われる。別の統合ブランチへマージするのではなく、クローンのcommitをrepoRoot自身の`branch`へそのまま持ち込む（無ければ新規作成）。repoRootで`branch`が現在チェックアウトされている場合、gitは「チェックアウト中のブランチのrefへ直接fetchする」ことを許さないため、`FETCH_HEAD`経由で`git merge --ff-only FETCH_HEAD`する。チェックアウトされていなければ`git fetch <cloneDir> branch:branch`のプレーンrefspecで済む（新規作成もfast-forwardもこれで足り、非fast-forwardはgit自身が拒否する）。fast-forwardできない場合は必ずエラーで返り、部分適用や強制解決は一切しない。
- **`Rebase(repoRoot, id, branch)`**（`:383`）: `Pull`のfast-forward要求がrepoRootの先行によって満たせなくなった場合（別ワークスペースが同じbranchへ先にマージ済み、等）の手動リカバリ手段。`review approve`には組み込まれない（ADR-0023が明示的に却下——本当の分岐は人間が両者の互換性を判断すべきで、自動リベースの対象にしない）。クローンの`origin`リモートは`Create`時点でrepoRootの絶対パスに設定されているため、ここでの`fetch`はネットワークを介さないローカル操作。クローン内で`git fetch origin branch`→`git rebase FETCH_HEAD`。コンフリクトで止まった場合、gitのエラーをそのまま返しクローンをコンフリクト状態のまま放置する（自動abort/resolveはしない）。呼び出し元は`masuda workspace info <id>`でクローンの絶対パスを確認し、そこで直接`git rebase --continue`/`--abort`してから再実行することになる。

## コミット・削除

- **`Commit(repoRoot, id, message)`**（`:253`）: クローン内で`git add -A`してから、`hasStagedChanges`（`git diff --cached --quiet`の終了コード判定、`:232`）が真の場合のみcommitする。Build段階の各ステップは既に個別commit済み（ADR-0027、限定的な`git add -- <files>`）なので、ここでの`add -A`が拾うのはReview段階のfixerがcommitせずに残した差分だけになる。呼び出し元は`review approve`の`finalizeReviewApproval`（`docs/design/gates.md`）で、`Pull`の直前に呼ばれる——先にcommitしておかないと、fixerの変更がクローンの未commitワーキングツリーだけに残った状態のまま次の`Remove`で消えてしまう。
  - **`identityOverride(repoRoot)`**（`:277`）: repoRootの*local*（globalではない）`user.name`/`user.email`設定がある場合、そのcommitにだけ`-c user.name=... -c user.email=...`として渡す。`git clone`はソース側のlocal設定を複製しないため、これが無いとクローン内のcommitはgitのglobal設定へ暗黙にフォールバックしてしまう。
- **`Remove(repoRoot, id, branch, deleteBranch)`**（`:330`）:
  1. `removeLeakedStepTags(repoRoot, id)`（`:353`）: `masuda-step-<id>-*`タグ（TDDモードのステップ境界マーカー、書き込み側は`docs/design/build.md`）がrepoRootへ漏れ込んでいれば削除する。これらのタグは本来クローン自身の`.git`内にしか存在しないはずだが（次のステップでどのみち`RemoveAll`される）、`Merge`/`Pull`の`git fetch`はどちらも`--no-tags`を渡していないため、新規fetchしたcommitから到達可能なタグを自動的に追従してrepoRootへ持ち込んでしまう。タグはワークスペースIDでscopeされているためベストエフォートな後始末で、1件も無くてもエラーにしない。
  2. クローンディレクトリ（`Dir(repoRoot, id)`）を`os.RemoveAll`。
  3. `deleteBranch`が真なら`git branch -D branch`をrepoRootで実行（ベストエフォート、エラー無視——`Merge`/`Pull`が一度もbranchをrepoRootへ持ち込んでいない、マージされないまま放棄されたタスクではno-op）。

  `masuda workspace remove --keep-branch`は`deleteBranch=false`を渡す。`review approve`の`finalizeReviewApproval`も常に`deleteBranch=false`で呼ぶ（branch自体がユーザーへ渡す成果物になるため——詳細は`docs/design/gates.md`）。

## 既知の問題

未調査。修正時はここから消す。

- **`newWorkspace()`の非atomicな作成順序**（`cmd/masuda/workspace.go:44`）: `workspace.Create`（`:49`、状態ディレクトリ作成）の後に`worktree.Create`（`:53`、gitクローン）を呼ぶ。後者が失敗する（例: 存在しない`base`を指定）と、状態ディレクトリだけが孤児として残る。コードを確認したところ、現時点でもこの順序のままであり未解消。
