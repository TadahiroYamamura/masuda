# HANDOFF
## 作業項目
M2（ワークスペースとstaging）完了。コミット: `5896eef`（internal/staging）、`cfc3df7`（internal/workspace・StagingService・Run）、このHANDOFFの更新
- `internal/staging`（`Repo`）
  - `Create(ctx, CreateOptions{RepoRoot, Dir, Branch, Base})`: `clone --bare --local`、cloneが残す`origin`を削除、`refs/masuda/base`と`refs/heads/<branch>`を分岐元へ。`Base`が空なら実リポジトリのHEADが指すブランチ（detachedならそのコミット）。実リポジトリに同名ブランチがあれば`ErrBranchExists`
  - `ImportBundle(ctx, bundlePath, srcRef, occ)`→`refs/masuda/wip/<occ>`（同じoccは上書き）、`CreateBundle(ctx, out, refs...)`
  - `Commit(ctx, CommitOptions{Branch, WIP, Allowed, Byproducts, Message, Author})`: WIPのtreeとブランチ先頭の差分を取り、Allowed/Byproducts外の変更があれば何も書かず`Deviations`を返す。なければ先頭のtreeにAllowedのパスだけを重ね（使い捨てGIT_INDEX_FILE）、`commit-tree`→旧値付き`update-ref`。パターンは完全一致か`path.Match`
  - `PublishLocal(ctx, repoRoot, branch, commit)`（fetch→FETCH_HEADが承認済みハッシュと一致を確認→チェックアウト中なら`merge --ff-only`、そうでなければ祖先検査＋旧値付き`update-ref`）、`PushRemote(ctx, url, branch, commit)`（非強制）、`RemoteURL(ctx, repoRoot, name)`
  - 閲覧: `ListRefs`・`GetCommit`・`Diff`（`diff-tree -p`）・`Blob`・`ResolveCommit`・`TopLevel`
  - エラー: `ErrNotFound`・`ErrInvalid`・`ErrBranchExists`
- `internal/workspace`: `Store`（`NewStore(dataDir)`・`Create`・`Get`・`List(repoRoot)`・`Remove`）、`Workspace`（`StagingDir/DataDir/RecordsDir/ExportsDir`・`Save`・`AddComment`・`Comments`）。メタは`<id>/workspace.json`、コメントは`<id>/records/comments.jsonl`。IDは12桁16進
- `serve`: `WorkspaceService.Run`（検査→ワークスペース作成→staging作成→STARTINGのWorkspaceを返す。失敗したらディレクトリごと消す）、`Get`/`List`はStoreから。`StagingService`全RPC（ListRefs/GetCommit/Diff/GetBlob（64KiBチャンク）/ListComments/AddComment）
- テスト: `internal/staging/staging_test.go`、`internal/workspace/workspace_test.go`、`serve/staging_test.go`
## 完了した契約テスト
C-M1・C-M2（`go test -count=1 ./contract/ -run 'TestCM1|TestCM2'`が緑。`go build ./...`・`go vet ./...`も通る）。C-M3〜C-M7は想定どおり赤
## 未完と理由
- `RunRequest.inputs`はまだ保存していない（`image`はメタに保存済み）。engine.Startへ渡すところ（M4/M5）で扱う
- `WorkspaceService.Remove`は未接続。`Store.Remove`はあるが、sandboxの停止と組にするM5で繋ぐ
- 前セッションから引き継いだ未完（`venv/`等の追跡外ファイル、`CLAUDE.md`の旧「開発環境」、`.claude/skills/`）はそのまま
## 次の一手
`docs/work-orders.md`のM3（フェイクsandbox）
## 注意点
- 契約テストのハーネスはフェイクのパスを`<DataDir>/fake/<wsID>/`で引く（`mcp.port`・`root/`）。sandbox IDをワークスペースIDと同じにするのが一番素直
- ゲスト→ホストのWIPは「コミットを指すref」をbundleに入れる前提にした（bundleはコミットしか運べない）。M4のSnapshotはゲストで`git add -A`→`commit-tree`（親はHEAD）→そのコミットを任意のrefに置いて`git bundle create`し、`ImportBundle`の`srcRef`にそのref名を渡す。`Commit`の`WIP`には`refs/masuda/wip/<occ>`を渡せばよい（`^{tree}`で解く）
- ホスト→ゲストのcloneは`CreateBundle(out, "refs/heads/<branch>")`→ゲストで`git clone -b <branch> <bundle> /workspace`で通ることを`staging_test.go`で確認済み
- `Commit`はAllowedに当たる変更が無いと新しいコミットを作らず、元の先頭を`Commit`として返す。エンジン側が「変更なし」を別扱いしたいならRunner側で判定する
- deviationで承認されなかったファイルを外すのはエンジン側の仕事（`Byproducts`に回す想定）。stagingは`Allowed`/`Byproducts`外が1つでもあれば何も書かない
- Allowed/Byproductsのグロブは`**`を解釈しない。計画の`files`や`expected_byproducts`が`**`を使うならM4で合わせる
- stagingのコミットの作者は`Author`が空なら`masuda <masuda@localhost>`。実リポジトリの`user.name`/`user.email`を使うかはM4で決める
- stagingには実リポジトリの全ブランチとタグも入っている（`ListRefs`にも出る）。UIで邪魔なら絞る
- `go mod tidy`後の`masuda-engine`のrequireの扱いはM1の注意点のまま
- 作業開始時点で`docs/design/contracts.md`等にこのセッション外の未コミット変更は無かった
## 契約への提案
なし
