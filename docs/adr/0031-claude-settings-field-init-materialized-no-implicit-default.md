# ADR-0031: Claude Code設定は`.masuda/settings.json`の`claudeSettings`フィールドとして`init`が実体化し、masuda自身は暗黙のデフォルトを持たない

## Status

Accepted (2026-08-05)

- 一部改訂: [[0034-skip-dangerous-mode-permission-prompt-over-tmux-polling]] — `masuda init`が書き出すデフォルト値に`skipDangerousModePermissionPrompt: true`が加わった
- 一部改訂: [[0043-child-mcp-server-aggregator-with-project-user-config-split]] — `.masuda/settings.json`に`mcpServers`フィールドが加わり、その承認・秘密情報は別ファイル`.masuda/settings.local.json`が持つ
- 一部改訂: [[0044-remove-docker-execution-runtime-vmbackend-only]] — Scaffold/Build/Review段階で`merge_claude_settings.py`が動く先はDockerコンテナではなくVMゲスト

## Context

対象リポジトリに`.mcp.json`があると、`claude`の初回起動時にMCP信頼確認プロンプトが出て無人ループが止まる（GitHub Issue #10）。実機検証により、`--settings <file-or-json>`フラグが対象リポジトリの`.claude/settings.json`を含む他の全設定ファイルより優先されることを確認済みで、これを使えば解決できることまでは判明していた。

一方、masudaは既に類似の問題（初回起動時の対話式テーマ選択ウィザードが無人ループを止める）を`runtime/claude-settings.json`（`{"theme": "dark-ansi"}`）としてDockerイメージのビルド時に`~/.claude/settings.json`へ焼き込むことで回避していたが、この実装は「動けばいい」で決めた場当たり的なもので模範にすべきではない、という認識で一致していた。理由は、ビルド時焼き込みは設定変更のたびにイメージの再ビルドが必要なこと、そして`enableAllProjectMcpServers`のようなセキュリティ上の含意を持つ設定をmasuda側が既定値として一律強制するのは、ユーザーの同意なくMCPサーバー（任意コード実行が可能）を信頼させることになり、テーマのような無害な既定値と同列に扱うべきではないことの2点である。

Issue #10の議論の中で、この2つの問題を一度に解決する方針として「`.masuda/settings.json`にユーザー自身のClaude Code設定を書ける場所を用意する」という方向で合意した。その具体化の過程で以下の派生論点が持ち上がった。

1. このフィールドを`.masuda/claude/settings.json`のようにエージェント名前空間で分離すべきか（将来masudaが他のAIエージェントや複数エージェントに対応する可能性を見据えて）
2. masuda自身がこのフィールドの既定値（テーマ等）をどこで・どう持つか
3. Dockerサンドボックス（フェーズ3-5）には、[[0015-native-lsp-plugins-and-repo-declared-image]]のビルド時`claude plugin marketplace add`が書き込むプラグインマーケットプレイス状態が既に`~/.claude/settings.json`に存在する。`--settings`が他の設定ファイルより優先されることの帰結として、ユーザー設定をそのまま`--settings`に渡すとこの状態を意図せず握りつぶすリスクがある

## Decision

`internal/config.Config`に`ClaudeSettings json.RawMessage`（JSONキー`claudeSettings`）を追加する。masuda自身はこの中身を一切解釈しない、Claude Code向けの不透明なペイロードとして扱う。

**エージェント名前空間化は見送り、フラットなフィールドとする。** masuda全体（Dockerfile・オーケストレーターのTask tool委譲モデル・`internal/hostloop`の起動コマンド）が現状Claude Code専用に作られており、設定ファイルの置き場所だけを名前空間で分離しても多エージェント対応を実質的に先取りできない。多エージェント対応は現時点でどのIssue・ADRにも計画されていない仮説であり、[[0030-workspace-id-drops-branch-name-prefix]]同様「まだ存在しないニーズのために現行の識別子・構造を複雑にしない」という判断を踏襲した。

**masuda側は`claudeSettings`の暗黙のデフォルトを一切持たない。** `runtime/claude-settings.json`とそのDockerfileへの焼き込みは廃止する。代わりに`masuda init`が、`.masuda/reviews/`（[[0024-file-based-perspectives-mechanical-checker-prompt]]、masuda内蔵14観点をリポジトリへ実体化するパターン）と同じ考え方で、`.masuda/settings.json`の`claudeSettings`にデフォルト値`{"theme": "dark-ansi", "enableAllProjectMcpServers": false, "enabledMcpjsonServers": []}`をリテラルとして書き出す（`cmd/masuda/init.go`の`defaultClaudeSettings`）。`enableAllProjectMcpServers`/`enabledMcpjsonServers`はfalse/空配列という、Claude Code自身のno-trust-by-default挙動と同じ何も許可しない値で初期化する——値そのものは何も付与しないが、Issue #10のMCP信頼確認プロンプトに対する選択的トラスト（`enabledMcpjsonServers`にサーバー名を追加して個別承認、または`enableAllProjectMcpServers`をtrueにして一括承認）という逃げ道の存在を、Claude Codeの設定スキーマを知らなくても見える形でユーザーの前に置くための実体化である。以後この値はmasudaのコードではなく対象リポジトリがコミットするデータであり、ユーザーが自由に編集・削除できる。`Image`/`Base`と異なり、`--claude-settings`のようなCLIフラグでの上書きは設けない（コミットされたJSONを直接編集すれば足りるため）。

`claudeSettings`は各フェーズの`claude`起動時に`--settings`へ渡す。

- フェーズ1-2（ホスト、`internal/hostloop.Start`）: `worktreeDir`（対象リポジトリのクローンそのもの）に対して`config.Load`を呼び、`ClaudeSettings`が非空なら`--settings <値>`を追加する。ホスト側にはビルド時に焼き込まれた設定が存在しないため、マージは不要でそのまま渡す
- フェーズ3-5（Docker、`runtime/entrypoint.sh`・`runtime/start_claude.sh`）: `claude`起動直前に新設の`runtime/merge_claude_settings.py`を実行する。これはビルド時焼き込みの`~/.claude/settings.json`（プラグインマーケットプレイス状態を含む）をベースに、`/workspace/.masuda/settings.json`（`/workspace`は既存のbind mountで既にコンテナ内から見えている）の`claudeSettings`を浅くマージ（キー衝突時は後者が優先）し、その結果を一時ファイルに書いて`--settings`へ渡す。これにより、`--settings`の「他の設定ファイルより優先される」という性質を保ったまま、ビルド時のプラグイン状態を失わずに済む

プラグインマーケットプレイス状態は「masuda側の暗黙のデフォルト値」ではなく、ADR-0015の機能が動作するために必要なビルド時インフラとして区別し、この決定の対象外として維持する。

## Alternatives Considered

- **`claudeSettings`を`--settings`にそのまま渡す（マージしない）**: フェーズ3-5でビルド時のプラグインマーケットプレイス状態を握りつぶし、ADR-0015のネイティブLSPプラグイン機能を壊すため却下
- **masuda側に何らかの既定値（テーマ等）を実装として持ち続ける**: ビルド時焼き込みは設定変更のたびにイメージ再ビルドが必要な上、「masuda自身が暗黙の設定判断をしない」という今回の方針そのものに反するため却下。`masuda init`によるリポジトリ側への実体化に置き換えた
- **`.masuda/claude/settings.json`のようなエージェント名前空間でのフィールド配置**: Contextで述べた理由により時期尚早と判断し却下
- **コンテナ側のマージをGo側（`internal/sandbox.Start`）でdocker cp前に行う**: `worktreeDir`はホスト側のパスであり、ビルド時に焼き込まれた`~/.claude/settings.json`の中身はコンテナ内にしか存在しないため、ホスト側Goコードでは両方の入力に同時にアクセスできない。マージはコンテナ内、`claude`起動直前に行うほかない

## Consequences

- `internal/sandbox.Start`・`docker create`には変更が不要だった。`/workspace`bind mountが既に`.masuda/settings.json`をコンテナ内に公開しているため
- 既に`masuda init`済みの既存リポジトリは`claudeSettings`フィールドを持たない。この変更後、そうしたリポジトリでは初回`claude`起動時のテーマ選択ウィザードが復活しうる（`runtime/claude-settings.json`によるビルド時の一律バイパスがなくなるため）。これはGitHub Issue #8（`masuda init`再実行時に新規デフォルトを後から取り込めない）と同根の問題であり、そちらのスコープとして残す
- `.mcp.json`を持つリポジトリでMCP信頼確認プロンプトを回避したいユーザーは、`masuda init`が書き出した`enabledMcpjsonServers`（個別承認）・`enableAllProjectMcpServers`（一括承認）のいずれかを自分の判断でtrue/値ありに変更する必要がある。masudaが書き出すのはfalse/空配列という無害な初期値までで、実際に何かを信頼する判断は常にユーザーが行う
- `runtime/claude-settings.json`は削除し、`runtime/merge_claude_settings.py`（Python、`orchestrator/`同様Dockerイメージのビルド時に焼き込まれる）が新たな依存として加わった
