# 子MCPサーバーの集約

対象リポジトリが宣言した外部MCPサーバーを、ワークスペースの状態デーモンがChatクライアント（Claude）向けのcurated `*mcp.Server`へ集約し、tool群として取り込む仕組み。実装は`internal/statedaemon/mcpaggregator`（`aggregator.go`・`proxy.go`）、CLIは`cmd/masuda/mcp.go`。

curated `*mcp.Server`自体・`wait_for_gate_change`等の組み込みtool・KVストアは`docs/design/state-daemon-mcp.md`を参照。`.masuda/settings.json`全体のスキーマは`docs/design/config.md`を参照（本書は`mcpServers`フィールドのみ扱う）。`.masuda/`のクローン同期全体は`docs/design/workspace.md`を参照。

## 宣言と承認の分離

子MCPサーバーの設定は2つのファイルに分かれる。

- **宣言**（`internal/config.Config.MCPServers`、対象リポジトリがコミットする`.masuda/settings.json`）: サーバー名をキーに`MCPServerDecl{Command, Args, Env []string, Tools []string}`を持つ。`Env`は環境変数の**名前のみ**（値は書かない）、`Tools`は公開してよいtool名のallowlist。宣言だけでは何も起動しない
- **承認**（`internal/config.LocalSettings`、gitignore対象の`.masuda/settings.local.json`）: サーバー名をキーに`MCPServerApproval{Approved bool, DeclHash string, Env map[string]string}`を持つ。`Env`は実際の値を持つ、masuda設定ファイル群の中で唯一秘密情報を保持する場所

`config.Load`/`config.LoadLocal`はそれぞれのファイルを読む。どちらもファイルが存在しない場合はゼロ値（「何も宣言/承認されていない」）を返し、エラーにしない。

## ハッシュによる紐付け

`config.DeclHash(decl)`は`MCPServerDecl`のJSONエンコードのsha256を返す。構造体のフィールド順は固定なので同じ内容は常に同じハッシュになる。

`masuda mcp approve`は承認時点の宣言から計算した`DeclHash`を`MCPServerApproval.DeclHash`へ書き込む。デーモン起動のたびに`mcpaggregator.resolveApproved`（純粋関数、`aggregator.go`）が**現在の**`.masuda/settings.json`の宣言から`DeclHash`を再計算し、承認時のハッシュと突き合わせる。以下のいずれかに該当するサーバーは起動をスキップし、理由を`log.Printf`経由でログへ出す（デーモンの起動自体は失敗させない）。

- 承認エントリが存在しない、または`Approved: false`
- ハッシュが不一致（宣言が承認後に変更された）
- `decl.Env`に列挙された名前のいずれかが`approval.Env`で空文字のまま（未設定）

## 起動とツール登録

`mcpaggregator.Start(ctx, repoRoot, curated, logDir)`はワークスペースの状態デーモン起動時（`cmd/masuda/statedaemon.go`の`runStatedaemon`）に1回だけ呼ばれる。`repoRoot`が空文字の場合はアグリゲータ自体を作らない（standalone/テスト用のデーモン起動には対応するリポジトリがないため）。

`Start`は`resolveApproved`が返した承認済みサーバーごとに`startChild`をgoroutineとして起動し、呼び出し元をブロックせずに返る。`Start`自体はエラーを返さない——設定ファイル読み取り失敗も含め、すべての失敗はログに書いてそのサーバーを諦めるだけで、デーモン全体の起動を妨げない。

`startChild`（1サーバーにつき1回）:

1. `<logDir>/mcp-<name>.log`をオープンし、子プロセスの標準エラー出力の書き込み先にする
2. `exec.Command(decl.Command, decl.Args...)`を組み立てる。環境変数はデーモン自身の`os.Environ()`（PATH・HOME等）に、承認済みの`decl.Env`名分のKEY=VALUEを追加したもの
3. `mcp.NewClient` + `mcp.CommandTransport`で子プロセスとstdin/stdout経由のMCPセッションを確立する。`childStartTimeout`（30秒、`aggregator.go`）が接続からツール登録完了までの上限
4. `ListTools`をカーソルが尽きるまで呼び出し（`listAllTools`）、`decl.Tools`のallowlistに含まれるものだけを`registerProxy`でcurated `*mcp.Server`へ登録する。allowlist外のtoolは黙って無視する（default-deny）

登録された子サーバーの`*mcp.ClientSession`とログファイルハンドルは`Aggregator.children[name]`に保持される。`Aggregator.Close()`は進行中の`startChild`をすべて`wg.Wait()`で待ってから、確立済みセッションを`session.Close()`（子プロセスの停止まで内包）する。デーモンは2本のUDSリスナー（trusted/curated）のどちらかが止まった時点で`Close()`を呼ぶ。

## tool名のプロキシと登録ガード

`registerProxy(curated, serverName, session, t)`（`proxy.go`）は子サーバーのtool `t`を`"<serverName>__<t.Name>"`という名前でcuratedへ登録する。ハンドラは`req.Params.Arguments`をそのまま子セッションの`CallTool`へ転送するだけで、引数・戻り値のいずれも変換しない。

登録前に以下をチェックし、満たさない場合はログに書いて登録をスキップする（`ok=false`を返す、エラーにはしない）。

- プロキシ名の長さが`maxToolNameLen`（128、go-sdkの`validateToolName`と同じ上限）以内
- `t.InputSchema`が`nil`でない

`(*mcp.Server).AddTool`は低レベルの非ジェネリックAPIで、`InputSchema`が`nil`または`type`が`"object"`でない場合に**panicする**（go-sdk自身の実装）。子サーバーはtool名のallowlistで宣言されるだけでスキーマの中身までは検証されない信頼境界の外側にあるため、`registerProxy`は`AddTool`呼び出しを`defer recover()`で必ずガードする。1つの不正な子サーバーのスキーマが`wait_for_gate_change`等の既存curated toolごとデーモンプロセス全体を落とすことを防ぐためのガードであり、外すと他のワークスペースの操作にも波及しうる。

## CLI: `masuda mcp`

`masuda mcp`は`repoRoot()`から解決した対象リポジトリのルート直下を直接操作する。ワークスペース単位ではなく、リポジトリに1つの`settings.local.json`をすべてのワークスペースの状態デーモンが共有して読む。

- **`list`**: `cfg.MCPServers`の各エントリについて`mcpStatus(decl, approval)`の結果を表示する。判定ロジックは`resolveApproved`と同じ4分岐（未承認／ハッシュ不一致で再承認要／env不足／承認済み）で、`masuda mcp list`の表示は次回デーモン起動時の実際の挙動と一致する
- **`approve <server-name> [--env KEY=VALUE]...`**: `cfg.MCPServers`に宣言が存在しないサーバー名はエラーにする。既存の承認エントリを読み込み、`Approved`をtrueにし、`--env`で渡された値を`approval.Env`へ**マージ**（上書きではなく既存キーを保持したまま追加・更新）した上で、現在の宣言から計算した`DeclHash`を書き込んで`SaveLocal`する。承認直後に`warnIfNotGitignored`を実行する。`decl.Env`に対して値がまだ足りない名前があれば、それを標準出力に列挙して知らせる（コマンド自体は成功扱い）
- **`reject <server-name>`**: `local.MCPServers`から該当エントリを削除する（保存済みのenv値も含めて完全に消える）。エントリが元々無ければ何もしない旨だけ出力する

`config.SaveLocal`は`.masuda/`ディレクトリ内に一時ファイルを作り、書き込み後に`os.Chmod(0o600)`してから同ディレクトリ内で`os.Rename`する（temp+rename）。パーミッションは`settings.json`の0644と異なり0600固定——このファイルは実際の秘密情報を持つため。

`warnIfNotGitignored(cmd, root, path)`は`git -C root check-ignore -q path`を実行し、終了コード1（=無視されていない）の場合だけ標準エラーへ警告を出す。それ以外（終了コード0=無視されている、またはgit自体の失敗）は何もしない。masudaはこの警告を出すだけで`.masuda/.gitignore`への自動追記は行わない。

## ワークスペースのクローンに`settings.local.json`は同期されない

`internal/worktree`の`syncMasudaConfig`は新規ワークスペース作成時に`settings.json`・`reviews/`・（存在すれば）`.masuda/.gitignore`をクローンへコピーするが、`settings.local.json`は対象から意図的に除外されている。状態デーモンは`workspace.Info.RepoRoot`経由でこのファイルをリポジトリルートから直接読むため、クローン側にコピーする必要自体がない。加えて、`internal/worktree.Commit`（review gate承認時にfixerが加えた分をまとめてcommitする、`finalizeReviewApproval`から呼ばれる）はクローン内を無条件に`git add -A`するため、もし`settings.local.json`がクローンにコピーされていれば、この経路で承認済みの秘密情報がワークスペースのブランチ履歴へ漏れる。

関連ADR: 0043（子MCPサーバーのアグリゲータ化）、0041（MCPのtrusted/curated 2面）。

## 既知の問題

未調査。修正時はここから消す。

- **`internal/worktree/worktree.go:119,135` のコメントが誤った漏洩経路を書いている**: `settings.local.json` をクローンへ複製した場合の漏洩経路を「Build/Review段階の `git add -A` ステップcommit」としているが、Build段階のステップcommitは `git add -- <changed_files>` の限定stage（ADR-0027）で、無条件の `git add -A` を行うのは review gate 承認時の `internal/worktree.Commit` だけ。同じ誤りが ADR-0043 本文にもあるが、そちらは Status 欄の `訂正:` で対応済み
