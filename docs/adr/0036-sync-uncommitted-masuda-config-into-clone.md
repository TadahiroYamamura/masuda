# ADR-0036: workspace clone作成時に、repoRootの`.masuda/settings.json`・`.masuda/reviews/`・`.masuda/.gitignore`を常に上書きコピーする

## Status

Accepted (2026-08-06)

- 一部改訂: [[0043-child-mcp-server-aggregator-with-project-user-config-split]] — 新設された`.masuda/settings.local.json`（秘密情報を含む）は、意図的にコピー対象から除外する

## Decision

`internal/worktree.Create`内、`git clone --local`成功後・`return dir, nil`の直前（branch-existsパスと新規branchパスの両方）で、新設のヘルパー`syncMasudaConfig(repoRoot, dir string) error`を呼ぶ。これは`repoRoot`の作業ツリー上の`.masuda/settings.json`（`config.SettingsPath`）・`.masuda/reviews/`（`perspectives.ReviewsDir`、配下を再帰的に`RemoveAll`→再構築でミラー）・存在すれば`.masuda/.gitignore`（`config.GitignorePath`）を、cloneが既に持っている内容の有無やtracked/untrackedの別に関わらず**常に上書き**でコピーする。

- 対象は`settings.json`・`reviews/`・`.gitignore`の3つだけの許可リスト方式とし、`.masuda/`ディレクトリを丸ごとコピーする方式は採らない
- `.masuda/.gitignore`（存在すれば。`masuda init`は生成しないが、`.masuda/`配下を丸ごとgit管理外にしたいユーザーが独自に置くファイル）を対象に含めるのは単なる一貫性のためではない。masuda自身の`Commit`（フェーズ4/5のステップcommit）はclone内で`git add -A`を実行するため、`.masuda/.gitignore`をcloneへ複製し忘れると、上記2つがコピーした`settings.json`・`reviews/*.md`がclone内では単なる無視されないuntrackedファイルとして扱われ、`git add -A`でworkspaceのブランチ履歴に意図せず混入してしまう。`repoRoot`が既に`.masuda/`をコミットしている場合は通常`.masuda/.gitignore`自体もコミット済みで`git clone`が複製するため、これが問題になるのは`settings.json`と同じ「`.masuda/`がまだコミットされていない」ケースに限られる
- `.masuda/Dockerfile`はコピー対象外とする——`docker build`は常に`repoRoot`基準で読まれ（`cmd/masuda/sandbox.go`・`update.go`）、cloneの中身が参照されることはないため、同期する意味がない
- `.masuda/worktrees/`（他workspaceのclone格納場所そのもの。`Dir(repoRoot, id)`もこの配下）はコピー対象外とする——丸ごとコピー方式を採らなかった直接の理由で、含めると新しいcloneが自分自身を含む全workspaceのclone群を再帰的に抱え込むことになる
- コピー元が存在しない場合（`masuda init`未実行等）はスキップし、エラーにしない。コピー自体が失敗した場合は`Create`をエラーで返す
- `Create`の早期return（`os.Stat(dir); err == nil`で該当workspaceのcloneが既に存在する場合、resume時の呼び出し）ではこの同期を実行しない——clone内で直接行われたローカルな変更を、再度の`Create`呼び出しで上書きしてしまわないため

## Context

`masuda plan start`実行時にMCP信頼確認プロンプトが表示され無人ループが止まる、という報告を調査したところ、原因は対象リポジトリで`.masuda/`ディレクトリが一切コミットされていなかったことだった（`git ls-files -- .masuda/`が空）。[[0018-git-clone-local-over-linked-worktree]]の`git clone --local`はgitの管理下にある内容しか複製しないため、生成されたworkspace cloneには`.masuda/settings.json`が存在せず、`internal/config.Load`がゼロ値`Config`を返し、[[0031-claude-settings-field-init-materialized-no-implicit-default]]が`--settings`フラグに渡すはずだった`claudeSettings`（MCP信頼設定を含む）が渡されないまま、Claude Codeのデフォルト挙動（対話式のMCP承認プロンプト）にフォールバックしていた。masuda自身のバグではなく、対象リポジトリ側の設定不備だったことを実機調査で確認済み。

[[0015-native-lsp-plugins-and-repo-declared-image]]・[[0024-file-based-perspectives-mechanical-checker-prompt]]・ADR-0031はいずれも、`.masuda/`配下（`settings.json`・`reviews/*.md`）を「対象リポジトリがコミットする設定」という前提で設計している。しかし今回の調査対象のユーザーは、masudaを自分一人でdog fooding中であり、`.masuda/`をチームへ展開してコミット共有する段階にはまだない。そのため「対象リポジトリの`.gitignore`を直して`.masuda/`をコミットする」という対応ではなく、「`.masuda/`が未コミットのままでも、workspace clone作成時にmasuda側で確実にコピーする」という方向で解決したいという要望を受けた。

さらに検討の過程で、「将来これらのファイルをコミットするようになった後も、`repoRoot`側でローカルに変更中（チームへ共有する前に検証中）の差分は、常に新しいworkspaceへ反映してほしい」という要望が追加された。これは「初回のみ、未trackedなファイルだけを補完的にコピーする」という当初案では満たせない要求で、tracked/untrackedの判定そのものをやめ、`repoRoot`の作業ツリーの現在の状態を常に正としてcloneへミラーする設計に落ち着いた。

## Alternatives Considered

- **対象リポジトリの`.gitignore`を修正し、`.masuda/`を実際にコミットする**: `git clone --local`が素直に機能する最も単純な解決だが、ユーザーは現在dog fooding中でチームへの`.masuda/`共有をまだ望んでおらず、この時点でコミットを強制する理由がないため不採用
- **`.masuda/`ディレクトリを丸ごとコピーする**: 対象を個別に把握する必要がなく単純だが、`.masuda/worktrees/`（他workspaceのclone格納場所）を含んでしまい、新しいcloneが既存の全workspaceを自己再帰的に抱え込む。除外リストで`worktrees/`だけを弾く方式も考えたが、「今後`.masuda/`直下に増える未知のファイル・ディレクトリを毎回無条件でコピーしてよいか」を都度判断せずに済む許可リスト方式（`settings.json`・`reviews/`・`.gitignore`のみ）の方が安全と判断し不採用
- **git statusでtracked/untrackedを判定し、未trackedのファイルだけをコピーする**: 今回の直接の不具合（未コミットで丸ごと存在しない）は解決できるが、「将来コミットした後もローカルの未コミット変更を反映してほしい」という要望を満たせない。コミット済みファイルは`git clone`が正しく複製するため一見安全に見えるが、要望の核心が「コミット有無に関わらず`repoRoot`の今の作業ツリーを正とする」ことだったため不採用とし、判定なしの常時上書き方式にした

## Consequences

- `internal/worktree`パッケージに初めてのテストファイル（`worktree_test.go`）が追加された。従来このパッケージは実機運用のみで検証されていた
- `Create`が呼ばれるたびに、clone本体のgit操作に加えて最大3ファイル系統（`settings.json`・`reviews/`配下・`.gitignore`）のファイルI/Oが増えるが、いずれも小さなテキストファイルであり無視できるオーバーヘッドである
- `.masuda/`を実際にコミットして複数人で共有する運用に移行した場合でも、`repoRoot`側の未コミット変更が常に新しいworkspaceへ漏れ出るという特性は残る。現状masudaは各ユーザーが自分専用の`repoRoot`チェックアウトを持つ前提（[[0014-workspace-id-and-external-state-directory]]・ADR-0018）であり、他人の`repoRoot`の未コミット状態が意図せず影響することはないため実害は小さいと判断したが、将来複数人が同一の`repoRoot`を共有する運用が生まれた場合はこの前提を再検討する必要がある
- `.masuda/Dockerfile`はこの仕組みの対象外のまま残る。将来、Dockerfileについても未コミットのローカル変更を都度反映したいという要求が出た場合は、`docker build`の参照先を`repoRoot`からcloneへ切り替えるか、この同期処理に対象を追加するかを別途検討する
