# ADR-0034: bypass permissionsダイアログはtmuxポーリングではなく`skipDangerousModePermissionPrompt`設定キーで回避する

## Status

Accepted (2026-08-05)

## Context

`runtime/entrypoint.sh`・`runtime/start_claude.sh`は、`claude --dangerously-skip-permissions`初回起動時に一度だけ出る「WARNING: Claude Code running in Bypass Permissions mode」同意ダイアログを、`tmux capture-pane`でペイン内容を10回までポーリングしテキストマッチ、`tmux send-keys`で選択肢（"2"）を自動送信する方式で突破している。この方式は、ダイアログの文言・選択肢番号というClaude Code側のUI実装詳細に依存しており、将来のバージョンでUI文言が変わると静かに壊れる（テキストが一致しなくなり10回のポーリングがすべて空振りし、ダイアログが放置されたまま次のステップ＝ttyd起動に進んでしまう）。

GitHub Issue #16の調査で、`claude`バイナリ（v2.1.222）の文字列を直接調べたところ、ダイアログの表示要否を判定する内部関数が`userSettings`・`localSettings`・`flagSettings`という3つの設定スコープを`skipDangerousModePermissionPrompt`というbooleanキーでOR判定していることが分かった。ダイアログで"Yes, I accept"を選択した際の実処理も同じキーに`true`を書き込んでおり、内部専用の読み取り専用フラグではなく、Zodスキーマで`.describe()`付きで定義された正規の設定項目である。また`flagSettings`スコープは`--settings`フラグ（インラインJSON・ファイルパスいずれも可）由来であることを、バイナリ内のエラーメッセージ生成ロジック（`source==="flagSettings"`のとき表示元を`"--settings"`と表示する分岐）から確認した。

masudaは既に`[[0031-claude-settings-field-init-materialized-no-implicit-default]]`により、`.masuda/settings.json`の`claudeSettings`フィールドを`runtime/merge_claude_settings.py`でビルド時焼き込み設定とマージし、`--settings`として`claude`起動コマンドに渡す仕組みを持っている。つまりこのキーを渡す経路は既に存在しており、あとは値を追加するだけで良い。

Dockerコンテナ（`masuda-loop:latest`）で実機検証した。`claude --dangerously-skip-permissions`単体ではダイアログが表示されたが、`claude --dangerously-skip-permissions --settings '{"skipDangerousModePermissionPrompt":true}'`ではダイアログが一切表示されず、直接ウェルカム画面（ステータスバーに`⏵⏵ bypass permissions on`表示）まで到達した。

なお`~/.claude.json`側に`bypassPermissionsModeAccepted`という別の関連キーも見つかったが、これは非推奨の旧キーで、起動時に内部的に`skipDangerousModePermissionPrompt`（`userSettings`側）へ自動移行され削除される実装になっていた。現行の正しいキーは`skipDangerousModePermissionPrompt`である。

## Decision

`cmd/masuda/init.go`の`defaultClaudeSettings`（`[[0031-claude-settings-field-init-materialized-no-implicit-default]]`が定義する、`masuda init`が`.masuda/settings.json`の`claudeSettings`に書き出すデフォルト値リテラル）に`"skipDangerousModePermissionPrompt": true`を追加する。これにより、`masuda init`直後のリポジトリでは`--settings`経由でこのキーが`flagSettings`スコープに渡り、ダイアログ自体が発生しなくなる。

ダイアログが発生しなくなる以上、`runtime/entrypoint.sh`・`runtime/start_claude.sh`の`tmux capture-pane`/`tmux send-keys`によるポーリングfor文（"Yes, I accept"を待つ分岐・"bypass permissions on"を待つ分岐の両方）を削除し、`tmux new-session`で`claude`を起動した直後にそのまま次のステップ（`entrypoint.sh`なら`ttyd`起動、`start_claude.sh`ならスクリプト終了）へ進む。待つべきダイアログそのものが無くなるため、ポーリングという実装そのものが不要になる。

## Alternatives Considered

- **現状のtmuxポーリング方式を維持する**: 設定キーを使わずダイアログの出現を許容し続ける案。動的なUI文言に依存する実装がこの箇所だけ残り、Claude Code側のダイアログ文言・選択肢が変わった場合に静かに壊れるリスクを放置することになるため却下。
- **ポーリングと設定キーの両方を残す（設定キーを主、ポーリングを保険として併用）**: ダイアログが出ないことを保証しきれない場合の防御線にはなるが、実機検証で設定キー単体により確実にダイアログが省略されることを確認できており、二重の仕組みを維持するコストに見合わないと判断し却下。既に`.masuda/settings.json`を持つ既存リポジトリがこのデフォルト値を持たないケース（後述のConsequences）は、ポーリングの保険では救えない（そもそも`masuda update`は既存の`.masuda/settings.json`を書き換えない）ため、保険として機能する場面自体が限定的だった。

## Consequences

- 新規に`masuda init`したリポジトリでは、`entrypoint.sh`・`start_claude.sh`の起動シーケンスからダイアログ待ちのtmuxポーリングが消え、`claude`起動後ただちに次のステップに進む
- 既に`masuda init`済みで`.masuda/settings.json`に`skipDangerousModePermissionPrompt`を持たないリポジトリは、ポーリングの削除後もダイアログを回避できず、`claude`が対話待ちで停止したまま無人ループが進行しなくなる。これは`[[0031-claude-settings-field-init-materialized-no-implicit-default]]`のConsequencesで既に触れられている「既存リポジトリは`masuda update`で新規デフォルトを後から取り込めない」（GitHub Issue #8）と同根の問題であり、本ADRのスコープでは対処せずIssue #8側に委ねる
- `skipDangerousModePermissionPrompt`はドキュメント化されていない挙動ではなくZodスキーマ上の正規フィールドだが、バイナリ文字列解析による把握であり公式ドキュメントでの確証ではない。将来のClaude Codeバージョンでキー名・判定スコープが変わった場合、この仕組みは静かに機能しなくなる（ポーリングという迂回路がなくなるため、機能しなくなった場合の症状は「ダイアログが出て無人ループが停止する」という分かりやすい形で現れる）
