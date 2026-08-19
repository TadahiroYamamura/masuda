# 設定ファイル

対象リポジトリのルート直下に置く`.masuda/settings.json`（コミット対象）と`.masuda/settings.local.json`（gitignore対象）のスキーマ・読み込み・優先順位、およびそれらをClaude Codeの`--settings`へ渡すまでのマージ処理。

## settings.json

`internal/config.Config`（`internal/config/config.go:57-91`）がオンディスク形式。`config.Load(repoRoot)`が読む——ファイルが存在しない場合はエラーにせずゼロ値を返すため、呼び出し側は全フィールドを「未設定なら組み込みデフォルトへフォールバック」として扱える。パーミッションは0644で書かれる（`masuda init`）。

対象リポジトリがコミットするファイルであり、masuda自身が発行・署名するものではない。したがって**信用されない宣言**として扱う（Issue #19）——読み込み側はここに書かれた値を無条件に実行してよい設定とみなさない。この原則が最も直接に効くのは`mcpServers`フィールドで、宣言だけでは何も起動せずユーザー側の別ファイルでの承認を要求する（後述）。

フィールドは4つ。

- **`image`**（string）: `masuda sandbox start`・`masuda review start`等サンドボックスを起動するコマンドが使うDockerイメージのタグ名。ここが指すのは**実行時に使う、既にビルド済みのローカルイメージタグ名**であり、Dockerfile自体（`.masuda/Dockerfile`）ではない——Dockerfileのビルド・タグ付けは`docs/design/distribution-and-update.md`を参照
- **`base`**（string）: リポジトリのtrunk branch名。`--base`（新規ワークスペースの起点ブランチ）と`--into`（mergeの着地先ブランチ）の両方のデフォルトに使われる。実務上この2つはほぼ常に同じブランチのため、フィールドは1つ
- **`claudeSettings`**（`json.RawMessage`）: サンドボックス内で起動する`claude`コマンドの`--settings`フラグへ渡す不透明ペイロード。masuda自身はこの中身を一切解釈しない——キーの意味・妥当性はClaude Code側の仕様であり、masudaのコード上は`json.RawMessage`のまま素通りする
- **`mcpServers`**（`map[string]MCPServerDecl`）: 子MCPサーバーの宣言。フィールドの存在とスキーマ（`MCPServerDecl{Command, Args, Env []string, Tools []string}`）はここで扱うが、承認フロー・状態デーモンへの取り込みは`docs/design/mcp-child-servers.md`を参照。承認は`settings.local.json`という別ファイルに分離されている

### 優先順位解決

`image`・`base`は`resolveImage`・`resolveBase`（`cmd/masuda/main.go:92,142`）がCLIフラグとマージする。両者とも同じ形——`cmd.Flags().Changed(flagName)`でフラグが明示的に渡されたかを見て、渡されていれば`config.Load`を呼ぶことすらせずフラグ値をそのまま返す。渡されていなければ`config.Load(root)`の値を使い、それも空なら呼び出し側が渡した`fall`（組み込みデフォルト）を返す。優先順位はCLIフラグ＞`settings.json`＞組み込みデフォルトの一本の連鎖。

`claudeSettings`にはこの優先順位解決がない——対応するCLIフラグ自体が存在せず、`settings.json`の値がある場合だけ`--settings`に渡り、無ければ`--settings`自体を省略する（後述）。masuda側の組み込みデフォルト値は存在しない。

### `masuda init`が書き込むデフォルト値

`masuda init`（`cmd/masuda/init.go`）は`.masuda/`を新規作成する一度きりの操作で、`claudeSettings`に以下のJSONをそのまま書き込む（`defaultClaudeSettings`定数）。

```json
{
  "theme": "dark-ansi",
  "enableAllProjectMcpServers": false,
  "enabledMcpjsonServers": [],
  "disabledMcpjsonServers": [],
  "skipDangerousModePermissionPrompt": true
}
```

書き込んだ後はファイルの中身であり、ユーザーが自由に編集・削除できる（ADR-0031）。各キーの意図は次の通り。

- `theme`: 初回起動時の対話的テーマ選択ウィザードを回避する。セキュリティ上の含意はない
- `enableAllProjectMcpServers`・`enabledMcpjsonServers`・`disabledMcpjsonServers`: いずれも「何も信用しない」側の値（false・空・空）で明示的に埋める。これらは対象リポジトリ側のMCPサーバーをClaude Codeがどう扱うかの選択的信頼の入り口（Issue #10）で、何かを事前承認するためではなく、この設定項目の存在自体をユーザーの目に触れる場所へ出すために書く
- `skipDangerousModePermissionPrompt`: `--dangerously-skip-permissions`使用時に出る免責ダイアログを初回起動時に抑制する（ADR-0034）

## settings.local.json

`internal/config.LocalSettings`（`internal/config/local.go:26-105`）がオンディスク形式。`settings.json`が対象リポジトリの委託する宣言であるのに対し、こちらはその宣言に対する**ユーザー本人の承認と、承認に紐づく実際の秘密情報**を持つ、gitignore対象の別ファイル。

現時点で唯一のフィールドは`mcpServers`（`map[string]MCPServerApproval`）で、`MCPServerApproval{Approved bool, DeclHash string, Env map[string]string}`を持つ。`DeclHash`は承認対象の宣言（`config.DeclHash`が計算するsha256）への紐付け、`Env`は`settings.json`側の`MCPServerDecl.Env`が名前だけ列挙する環境変数の実値——masudaの設定ファイル群の中で唯一、実際の秘密情報を保持する場所になる。承認の判定ロジック・状態デーモンへの取り込みは`docs/design/mcp-child-servers.md`を参照。

`config.LoadLocal`は`settings.json`同様、ファイル不在をエラーにせずゼロ値（「何も承認されていない」）を返す。書き込みは`config.SaveLocal`が担う。

- パーミッションは0600固定（`settings.json`の0644と異なる）。実際の秘密情報を持つファイルのため、umaskに関係なく所有ユーザーのみに絞る
- 書き込みは同一ディレクトリ内でのtemp+rename（`os.CreateTemp`→書き込み→`os.Chmod(0o600)`→`os.Rename`）。クラッシュ時に中途半端な内容のファイルが残ることも、複数ターミナルからの`masuda mcp approve`同時実行が内容を混在させることもない

**ワークスペースのクローンへは同期されない。** 新規ワークスペース作成時にコピーされるのは`settings.json`・`.masuda/reviews/`・（存在すれば）`.masuda/.gitignore`のみで、`settings.local.json`は対象外——状態デーモンはリポジトリルート（`workspace.Info.RepoRoot`）から直接このファイルを読むため、クローン側に複製する必要自体がない。同期の全体像は`docs/design/workspace.md`を参照。

## Claude設定のマージ

`claude`コマンドの`--settings`フラグに何を渡すかは、段階によって非対称。

- **Discovery/Blueprint段階（ホスト側）**: マージせず、`.masuda/settings.json`の`claudeSettings`をそのまま`--settings`へ渡す。フィールドが空ならフラグ自体を省略する（`internal/hostloop`、詳細は`docs/design/discovery-blueprint.md`）
- **Scaffold/Build/Review段階（サンドボックス側）**: `runtime/merge_claude_settings.py`（`:24,36`）が、ビルド時に焼き込まれた`~/.claude/settings.json`（`claude plugin marketplace add`によるプラグインマーケットプレイス状態を含む）と`.masuda/settings.json`の`claudeSettings`を浅くマージしてから渡す。`merged = _read_json(BAKED_SETTINGS_PATH)`のdictに`_read_json(REPO_SETTINGS_PATH).get("claudeSettings", {})`を`update()`する1階層のマージで、トップレベルキーが衝突すれば`claudeSettings`側の値が勝つ。結果は一時ファイルに書いて`--settings`へ渡す。どちらの入力ファイルも欠落し得る（未改造イメージにはプラグイン状態がない、`masuda init`前のリポジトリには`claudeSettings`がない）が、どちらも`{}`として扱いエラーにしない

この非対称が生じるのは、サンドボックス側だけがビルド時焼き込みのプラグイン状態を持つため——ホスト側にはマージすべき相手が最初から存在しない。
