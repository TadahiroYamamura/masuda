# HANDOFF
## 作業項目
**M11b（利用者向け`docs/user/`）**。M11c（`docs/api/`・`clients/`）は別セッションが並行して書いており、このセッションは触っていない（`mkdocs.yml`の`nav`の`api`セクションも触っていない）。
- `6bb47ab` 入口・導入・はじめての1周・概念（`user/index.md`・`install.md`・`quickstart.md`・`concepts.md`）
- `20cf9d4` CLIと設定ファイルのリファレンス（`user/cli.md`・`settings.md`）
- `1ca8c7e` ワークフローとレビュー観点（`user/workflows.md`・`reviews.md`）。developとreviewの図は`masuda workflow show`の出力をそのまま貼った
- `a179f4d` 秘密・egress・特権コマンド、運用、トラブルシューティング（`user/secrets-and-egress.md`・`operations.md`・`troubleshooting.md`）
- `15b26f6` `docs/INSTALLATION.md`・`docs/CONTRIBUTING.md`を削除。開発者向けの中身は`docs/design/README.md`の「開発環境」へ。`exclude_docs`から2ファイルを外し、`README.md`のリンクと使い方（`masuda-sandbox serve --socket`）を直した
- `mkdocs.yml`の`nav`の「利用者向け」: index→導入→はじめての1周→概念→CLI→設定→ワークフロー→ワークフロー定義の仕様→レビュー観点→秘密・egress・特権コマンド→運用→トラブルシューティング
## 完了した契約テスト
契約ファイル（両proto、`docs/guest-protocol.md`）・契約テスト・コードは触っていない。`scripts/docs-prepare.sh && mkdocs build --strict`は最終コミットで通る（INFOはM11aからの`api/reference.md`の`#google-protobuf-Timestamp`1件だけ）。途中のコミット（`6bb47ab`〜`1ca8c7e`）はまだ無いページへのリンクがあり、単体ではstrictで落ちる
## 未完と理由
- VMを起動する手順（`masuda-sandbox`の`pnpm install && pnpm build`と`serve`、`claude setup-token`、`masuda image build`、実VMでの`run`→`watch`→`gate show/approve`→publish、`resume`、実VMへの`chat`、特権コマンド）は打っていない（指示どおり）。live_test.goとM8のHANDOFFの記録に合わせて書いた
- 確かめた手順: `masuda --help`・`serve -h`・`run -h`・`version`、一時リポジトリでの`masuda init`（2回目の「nothing to do」と`.gitignore`）、`--fake-sandbox`のserveに対する`workflow list/check/show`（同梱と自作の例）、`egress list/approve`（宣言外のエラー）、`secret set`（パイプ）・`list`、`image list`、`privileged-command list`、`run`（成功と、ブランチ既存・入力不足・未定義ワークフローのエラー）、`list`・`list --all`、`watch`の初回status、`gate list`、`question list`、`stop`、`remove`（`--force`の要否）、`chat`（フェイクでのエラー）、`settings.json`の知らないキーのエラー、`ubuntu:24.04`に`resize2fs`があること
- 確かめていない記述: macOSの手順、KVMのグループ設定、ツールごとのCAバンドルの環境変数（`REQUESTS_CA_BUNDLE`等）の案内、`masuda-sandbox images prune`、DockerfileのENV（PATH以外）が今のsandbox＋masudaでチェック・エージェントに届くか
## 次の一手
1. ユーザー（M11aから持ち越し）: `redesign`をpushし、Actionsの「docs」を`workflow_dispatch`で実行→Settings → Pagesで`gh-pages`・`/ (root)`を選ぶ。`https://tadahiroyamamura.github.io/masuda/dev/`で見られる
2. 実機の環境で`docs/user/quickstart.md`を頭から通し、出力例（`masuda list`・`watch`・`gate show`）を実物に差し替える
3. 下の「実装と文書の食い違い」のうち実装側のものを直す（直したら`docs/user/`の警告を消す）
4. M12（設定の整理）で`stallAfter`・`diskWarnBytes`が`config.json`へ移ったら、`user/settings.md`・`operations.md`・`troubleshooting.md`の該当箇所も同じコミットで直す
## 注意点
- 生成物（`docs/api/reference.md`・`docs/user/reference/workflow-schema.md`）はコミットしない。`mkdocs build`の前に`scripts/docs-prepare.sh`
- 表のセルに`|`を書かない（M11aの注意）。`docs/user/`の全表で列数を機械的に確かめてある
- ページ間のリンクは日本語見出しの自動ID（`#_2`等）に頼らず、`{#id}`で明示したアンカーへ張っている。見出しを変えるときはIDを残す
- `user/workflows.md`の2つのMermaid図は`masuda workflow show`の出力の貼り付け。engineの同梱定義が変わったら貼り直す
- 実装と文書の食い違い（括弧内はどちらを直すべきか）:
  - **観点の`enable`が効かない**（実装を直す）: frontmatterの`enable`はmasuda（`perspectives.Merge`・`runner.perspectiveItems`）もengine（trigger-matcher）も読まない。`enable: false`でも使われ、同梱の観点を外す手段も無い。`user/reviews.md`に警告を書いた
  - **`workflows/review`を単独で動かすと差分が空**（設計・実装を直す）: `staging.Create`はブランチを分岐元と同じコミットに置き、`diff`は`refs/masuda/base`からなので、既存の変更をレビューする手段が無い。例えば「既存のブランチを起点にし、`--base`を差分の基準にする」実行の形が要る。`user/workflows.md`に警告を書いた
  - **引数無しの`masuda workflow check`が同梱の定義で終了コード1**（実装を直す）: 全ワークフローをrootとして検査するので、部品（`implement/build-step`・`fix-finding`）で「承認済み計画が無い」が出る。engineの仕様（rootは「どのワークフローのReachableにも含まれないもの」）に合わせ、rootだけを検査すべき。`overview.md`第9章の「全ワークフローをそれぞれrootにして検査し」も合わせて直す
  - **`run --base`のヘルプ**（実装の文言を直す）: 「空ならリポジトリの既定のブランチ」とあるが、実装は今チェックアウトしているブランチ（detachedならそのコミット）。文書は実装どおりに書いた
  - **雛形Dockerfileのコメントが古い**（実装＝雛形を直す）: masuda-sandboxのS10以降、イメージのENVはExecへ引き継がれ、読み取り専用ディレクトリのあるイメージのビルド（`-modcacherw`）も直っている。一方masudaは`guest.BaseEnv`でPATH・XDG_*をExec.envに上書きし、チェックは`sh -el`なのでENVのPATHは効かない。雛形のコメントと`guest.BaseEnv`の要否（sandboxのS10 HANDOFFは「不要になった」と書いている）を見直す。文書は「PATHは効かない、それ以外は確実な渡し方を勧める」で書いた
  - **publishの`target: remote`の送り先**（設計文書か実装を直す）: `overview.md`第4章は「設定したremoteへpush」だが、実装（`runner.Publish`）は常に`origin`で、設定の項目は無い。利用者向けは実装どおり`origin`と書いた
  - **`overview.md`第9章のCLI表**（文書を直す）: `masuda list`の`--repo`が載っていない
  - **Claudeのトークンがリポジトリごと**（提案）: 秘密ストアはリポジトリの絶対パスで分かれるので、リポジトリごとに`secret set CLAUDE_CODE_OAUTH_TOKEN`が要り、移動すると失われる。M4の暫定ファイル`<DataDir>/claude-oauth-token`へのフォールバックが残っているが、文書には書いていない。マシン全体の既定のトークン（M12の`config.json`等）を検討する価値がある
  - **雛形の`egress: ["api.anthropic.com"]`**（提案）: Claude APIは常に許可なので宣言・承認は意味を持たない。quickstartでは「無くても動く」と書いて承認の手順を残した。雛形から外すか、意味を持たせるかの判断
  - **エージェントの`feedback`が実行ログに出ない**（提案）: `finish`イベントにoutcomeしか無く、out_of_scope・stuckの理由は`records/engine.json`を掘るしかない。`execution-log.jsonl`の`detail`に載せると利用者が読める
## 契約への提案
- なし（このセッションで契約に関わる新しい発見は無い）。M11aからの持ち越し: `contracts.md`「通信の前提」のループバック待ち受け（M12で実装予定）、serve全体の設定の置き場所（M12）、engineのfixerの終わり方（engine側で`cannot_fix`が入っている）
