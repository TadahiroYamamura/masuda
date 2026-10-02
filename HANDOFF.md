# HANDOFF
## 作業項目
M11a（サイトの土台とdesignの反映）。M11b・M11cの本文は書いていない。
- `80a7213` サイトの土台
  - `mkdocs.yml`: Material、`language: ja`、検索（`lang: ja`）、`pymdownx.superfences`のmermaidフェンス、mikeのバージョン選択（`extra.version.provider: mike`、既定`latest`）。navは「ホーム」「利用者向け」`user/`・「統合開発者向け」`api/`・「開発者向け」`design/`（`guest-protocol.md`を含む）
  - `exclude_docs`で`work-orders.md`・`research/`・`CONTRIBUTING.md`・`INSTALLATION.md`をサイトから外した
  - `requirements-docs.txt`: `mkdocs>=1.6,<2`（MkDocs 2.0はMaterialが動かないので固定）・`mkdocs-material`・`mike`・`pymdown-extensions`
  - `docs/index.md`（3行の説明と3系統の入口）、`docs/user/index.md`・`docs/api/index.md`（「準備中」と予定の項目）
  - `scripts/docs-prepare.sh`: `buf generate --template buf.gen.docs.yaml`で`docs/api/reference.md`を生成し、`$MASUDA_ENGINE_DIR`（既定`../masuda-engine`）の`docs/workflow-schema.md`を`docs/user/reference/workflow-schema.md`へ注記（admonition）付きで写す。`buf`が無ければ`go run github.com/bufbuild/buf/cmd/buf@latest`
  - `buf.gen.docs.yaml`: プラグインはBSRの`buf.build/community/pseudomuto-doc`（指示にあった`pseudomuto-protoc-gen-doc`はBSRに無い名前だった）、`opt: markdown,reference.md`
  - `.gitignore`: 生成物2つ、`/site/`、`/.venv-docs/`
  - `.github/workflows/docs.yml`: push（main・develop・タグ`v*`）と`workflow_dispatch`。engineは`path: masuda-engine`にcheckoutして`MASUDA_ENGINE_DIR`で渡す。バージョンは、手動実行→`dev`（どのrefから起動しても）、タグ`vX.Y.Z`→`X.Y`+`latest`、main→`main`、develop→`dev`。タグのpushのときだけ`mike set-default --push latest`。`permissions: contents: write`、`concurrency: docs-deploy`（取り消さず直列）
- `2bc9d8d` designの反映
  - `overview.md`: HANDOFF（M10）の「docs/design/へ反映すべき事項」とM11aの列挙をすべて現在形で書いた（第3・4・5・6・8・9章）
  - `README.md`: 「触る対象から引く」表を新設（`cmd/masuda`・`serve/`・`internal/*`10個・`contract/`・`live/`・proto・サイト）。リポジトリへのリンクをGitHubのURLに
  - `contracts.md`: 変更の手続きに「契約を変えたら`docs/user/`・`docs/api/`の該当箇所も同じコミットで直す」
## 完了した契約テスト
契約テスト・契約ファイル（両proto、`docs/guest-protocol.md`）は触っていない。`go build ./...`・`go vet ./...`は通る。ローカルで`.venv-docs/`に入れて`scripts/docs-prepare.sh`→`mkdocs build --strict`が通る（INFOが1件: 生成した`reference.md`の`#google-protobuf-Timestamp`へのアンカーが無い。strictでは失敗しない）。`mike deploy`・`mike set-default`は使い捨ての複製で動作を確かめた（push無し）
## 未完と理由
- `workflow_dispatch`で`dev`が公開されることは未確認。pushしておらず、Pagesも未有効化のため
- GitHub Pagesの有効化はユーザーが行う（下記）
## 次の一手
1. ユーザー: `redesign`をpushし、Actionsの「docs」を`workflow_dispatch`で実行する。成功するとgh-pagesブランチができる
2. ユーザー: リポジトリのSettings → Pages → Build and deployment で、Source: **Deploy from a branch**、Branch: **`gh-pages`**、フォルダ: **`/ (root)`** を選んで保存する。`https://tadahiroyamamura.github.io/masuda/dev/`で見られる（タグを打つまで`latest`が無いので、ルートはmikeの既定が無く404になりうる。必要なら一度だけ`mike set-default --push dev`）
3. M11b（`docs/user/`）、M11c（`docs/api/`）
## 注意点
- 生成物（`docs/api/reference.md`・`docs/user/reference/workflow-schema.md`）はコミットしない。`mkdocs serve`・`mkdocs build`の前に必ず`scripts/docs-prepare.sh`を走らせる。navが両方を参照しているので、走らせ忘れると`--strict`で落ちる
- M11b: `user/index.md`の予定の項目を本文のページに置き換え、navの「利用者向け」に足す。`docs/INSTALLATION.md`・`CONTRIBUTING.md`は今はサイトから外している（`exclude_docs`）ので、中身を`user/`へ移したら`exclude_docs`から消すか削除する。`README.md`・`CONTRIBUTING.md`からのリンクも追従させる
- M11b: 表の中で`|`を使うと、GitHubとPython-Markdownでエスケープの扱いが違う（コード内の`\|`がMkDocsでは`\|`のまま出る）。表のセルにパイプを書かない
- M11c: `reference.md`は`pseudomuto-doc`の既定テンプレートの英語出力。気になるなら`opt`でテンプレートを渡せる（`markdown`の代わりに`<tmpl>,reference.md`）。BSRのリモートプラグインは版を固定していない
- design/overview.mdに書いた「ServeNoticeはまだ出さない（disk-warningはEngineEventで流す）」と「`settings.local.json`の`stallAfter`・`diskWarnBytes`」は、M12で変わる。M12の実装と同じコミットでoverview.mdの第6章（設定ファイル）・第8章（無活動のしきい値・ディスク使用量）・第9章（Watch）を直す
- `docs/design/contracts.md`の「通信の前提」は「設定で有効にしたときだけ`127.0.0.1:<port>`」と書いているが、実装はUDSだけ。overview.mdは実装どおりに書き、contracts.mdは契約の記述なので変えていない
- design/overview.md第13章（マイルストーン）はwork-orders.mdのM番号と一致しない古い表のまま残している
## docs/design/へ反映すべき事項
- なし（M10までの事項はすべて反映済み）。反映しきれなかったもの: contracts.mdの待ち受けの記述と実装のずれ（上記）
## 契約への提案
- `contracts.md`「通信の前提」のループバック待ち受けは未実装。実装するか、記述を「UDSのみ（ループバックは将来）」にするかの判断（M11cの「接続」の章の前提になる）
- 前回からの持ち越し（判断状況はこちらでは未確認）: serve全体の設定の置き場所（M12で予定）、sandboxの応答前に切られたHTTPリクエストに終わりのイベントが無い、sandboxのExecの既定環境、engineのfixerに「直せない」終わり方が無い
