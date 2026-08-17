# ADR-0043: 子MCPサーバーのアグリゲータ化は宣言(`.masuda/settings.json`)と承認(`.masuda/settings.local.json`)を分離し、宣言のハッシュ値で承認を紐付ける

## Status

Accepted (2026-08-17)

## Context

[[0041-mcp-protocol-with-trusted-and-curated-surfaces]]は、状態デーモンのワイヤプロトコルをMCPに統一した動機として「Claudeへ外部tool（例: GitHub Issue読み取り）を持たせたい場合、既製のMCPサーバーをデーモンの下にアグリゲータとして取り込める」拡張性を挙げ、Consequencesで「curated setは現状意図的に2toolしかない。子MCPサーバーのアグリゲータ化はGitHub Issue #35のコメントに設計方針のみ記録、未実装」「trusted側の設定（子MCPサーバーの起動コマンド・トークン等）を対象リポジトリの`.masuda/settings.json`に書けるようにする案も検討したが、これは対象リポジトリ側の`settings.json`が無条件に信頼される既存の未解決課題（GitHub Issue #19）を悪化させるため、プロジェクトの宣言とユーザーの承認・秘密情報を分離する設計（`repoRoot/.masuda/settings.local.json`）を構想したのみで、本ADR・本セッションでは未実装」と明記していた。本ADRはその実装で、Issue #35のclose要件として着手した。

## Decision

子MCPサーバーの設定を2ファイルに分離する。

- **プロジェクト側**（`internal/config.Config.MCPServers`、対象リポジトリがコミットする`.masuda/settings.json`）: `MCPServerDecl{Command, Args, Env []string（環境変数の名前のみ、値は書かない）, Tools []string（公開してよいtool名のallowlist）}`。宣言だけでは何も起動しない
- **ユーザー側**（新規`internal/config.LocalSettings`、`repoRoot/.masuda/settings.local.json`、gitignore対象）: `MCPServerApproval{Approved bool, DeclHash string, Env map[string]string（実際の値）}`

対応する承認が無い宣言、または`Approved: false`の宣言は、デーモンが一切起動しない。承認は宣言のフィンガープリント（`config.DeclHash`、`MCPServerDecl`のcanonical JSONをsha256したもの）に紐付け、`internal/statedaemon/mcpaggregator.resolveApproved`（純粋関数）が起動のたびに現在の宣言のハッシュと承認時のハッシュを比較する。不一致は「未承認」と同じ扱いにする——プロジェクト側が承認後にコマンド・引数を書き換えても、ユーザーが再承認するまで古い（承認済みの）内容のまま起動し続ける、または起動しなくなる。

デーモン起動時（`cmd/masuda/statedaemon.go`の`runStatedaemon`、`--repo-root`フラグ経由。`startDaemon`が`workspace.Load(id).RepoRoot`を渡す）、`mcpaggregator.Start`が承認済みの宣言ごとに`exec.Command`+`mcp.CommandTransport`（`github.com/modelcontextprotocol/go-sdk`）で子プロセスを起動し、`ListTools`の結果を`Tools`のallowlistで絞り込んだ上で、Claude向けcurated `*mcp.Server`（`internal/statedaemon/mcpserver.NewCurated`が返すもの、`internal/statedaemon/mcpserver/uds.go`の新設`ServeCuratedServerUDS`で配信）へ`"<serverName>__<toolName>"`という名前で透過的にプロキシ登録する。呼び出しはそのまま子セッションの`CallTool`へ転送するだけで、引数・戻り値のいずれも変換しない。

プロキシ登録には、go-sdkの低レベル非ジェネリックAPI `(*mcp.Server).AddTool(t *Tool, h ToolHandler)`を使う。既存コード（`mcpserver.New`・`NewCurated`）が使うジェネリック`mcp.AddTool[In, Out]`はコンパイル時に決まったGo構造体を要求するが、子MCPサーバーのtoolスキーマは`ListTools`で初めて分かる実行時の値であり、事前に型を書けない。低レベルAPIの`Tool.InputSchema`は`any`型で、クライアント側で受け取った`map[string]any`をそのまま再セットできる。

`AddTool`は`InputSchema`が`nil`か`type`が`"object"`でない場合に**panicする**（go-sdk自身の実装）。子MCPサーバーは信頼境界の外側（プロジェクトはtool名のallowlistで宣言するだけで、実際に返してくるスキーマの中身までは検証していない）にあるため、`internal/statedaemon/mcpaggregator.registerProxy`はこの呼び出しを`recover()`で必ずガードする。1つの不正・悪意あるスキーマを返す子サーバーが、`wait_for_gate_change`等の既存curated toolも道連れにしてstate daemonプロセス全体を落とすことを防ぐためである。

`.masuda/settings.local.json`は`internal/worktree.syncMasudaConfig`のコピー対象から意図的に除外する。デーモンは`workspace.json`の`RepoRoot`経由でこのファイルをrepoRootから直接読むため、ワークスペースのclone（worktree）側にコピーする必要が無いばかりか、コピーしてしまうとフェーズ4/5の`git add -A`ステップコミットへ秘密情報が漏れ出す。

`masuda mcp approve <server-name> [--env KEY=VALUE ...]`が`SaveLocal`（temp+rename、パーミッション0600）で承認・環境変数値・`DeclHash`を書き込んだ直後、`git check-ignore -q`を実行し、対象ファイルがどの`.gitignore`にも無視されていなければ警告を出す。gitignoreへの自動追記はしない。

## Alternatives Considered

- **子MCPサーバーの設定を対象リポジトリの`.masuda/settings.json`に直接書けるようにする**: [[0041-mcp-protocol-with-trusted-and-curated-surfaces]]で既に検討・却下済み（Issue #19、`settings.json`は無条件には信頼できない対象リポジトリ由来のファイルであるため）。本ADRはその却下を受けた実装。
- **承認を単純な`Approved bool`だけで管理し、`DeclHash`のような宣言への紐付けを持たない**: 実装コストは小さいが、承認後にプロジェクト側が悪意を持って（あるいは無警戒に）コマンド・引数を書き換えても検知できず、名前ベースの承認が実質的に「このリポジトリの`settings.json`は今後も無条件に信頼する」という状態に近づいてしまう。これはIssue #19が問題視している前提そのものを骨抜きにするため不採用。
- **子サーバーのtoolスキーマをmasuda側で事前検証し、`AddTool`のpanicを回避する**: go-sdk自身が持つスキーマ検証ロジック（`type`が`"object"`かどうか等）を二重実装することになり、go-sdkのバージョンアップで検証内容が変わるたびに追随する保守コストが発生する。`recover()`による防御で実害（daemonプロセス全体のクラッシュ）は十分に防げると判断し、検証そのものの二重実装は避けた。
- **`.masuda/.gitignore`へ`settings.local.json`を自動追記する**: [[0036-sync-uncommitted-masuda-config-into-clone]]が前提とする「`.masuda/.gitignore`はmasudaが一切書き込まない、完全ユーザー管理」という既存の原則を、この1ファイルのためだけに破ることになる。シークレット誤コミットのリスクは看過できないため、`git check-ignore`による警告のみに留め、原則自体は維持した。

## Consequences

- `.masuda/settings.json`に子MCPサーバーを追加宣言しただけでは何も起動しない。`masuda mcp approve`によるユーザーの明示的な承認が別途必要で、かつプロジェクト側の宣言が変わるたびに再承認が要る——これは意図した摩擦であり、バグではない。
- `masuda mcp approve`は`--env KEY=VALUE`をシェルの引数として受け取るため、シェル履歴にトークンが残る経路が生まれる。`.masuda/settings.local.json`を直接エディタで編集して`approve`を`--env`無しで叩き直す（`Env`マップは上書きでなくマージ）という代替経路はあるが、マスク入力プロンプト等のUX改善は本ADRの対象外。
- `internal/statedaemon/mcpaggregator`は子プロセスの標準エラー出力を`<stateDir>/mcp-<name>.log`へ書くのみで、子プロセスの異常終了・再起動は行わない。子サーバーが途中で落ちた場合、そのワークスペースのデーモンを再起動するまでtoolは使えないままになる。
- `git check-ignore`はbest-effort（gitが無い・リポジトリでない等の失敗はすべて無視）であり、警告が出なかったことは「安全」を保証しない。ユーザーが`.gitignore`を後から書き換えて対象を外した場合も検知しない。
