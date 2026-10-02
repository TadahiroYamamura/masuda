# Gondolinスパイク（2026-10-02）

再設計の前提を実機で確かめたスクリプト群。結果は`docs/design/redesign-background.md`。

| ファイル | 確かめたこと |
|---|---|
| `image/Dockerfile`・`build-config.json` | Ubuntu 24.04 + native Claude Code + tmuxのゲストを`gondolin build`で資産化 |
| `claude-test.mjs` | Claude CodeがMITM越しにプレースホルダトークンでAPIへ届くか。レート制限ヘッダでサブスク枠を確認。長いストリーミング |
| `policy-test.mjs` | 実行中の許可リスト切替、`tcp.hosts`でホストのローカルポートへ到達 |
| `attach-test.mjs` | tmuxへのpty attachとSSH |
| `cow-fork.mjs` | チェックポイントからの同時分岐起動と隔離 |
| `keepalive.mjs` | 長時間生存の監視 |

実行手順: `npm install @earendil-works/gondolin` した作業ディレクトリに置き、`docker build -t spike1-guest:latest image && npx gondolin build --config build-config.json --output ./guest-assets` のあと `node <script>`。`claude-test.mjs`は`~/.local/share/masuda/claude-oauth-token`を読む。
