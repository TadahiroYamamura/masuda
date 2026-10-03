# HANDOFF
## 作業項目
M14a（`docs/work-orders.md`。#68の前半）: masuda自身の`.masuda/`を整える。develop `31ffdef`〜`efd78f3`（**未push**）。
- `31ffdef` `.gitignore`の`.masuda/`・`.masuda-gate/`をやめ、`masuda init`の`localIgnores`と同じ2行（`.masuda/settings.local.json`・`.masuda/claude.local/`）だけ無視。`.masuda/`を追跡に入れた
- `.masuda/reviews/`の14ファイルは**すべて**同梱（`internal/perspectives/builtin`）とバイト単位で同じだったので消した（追跡に入れていない）。**残した観点は無い**
- `1844336` Dockerfileを`install.sh | bash -s -- 2.1.287`に固定（「新設計」を削除、版の出どころの1行コメント）。`ctx/go.mod`・`go.sum`を今のもの（engine fd33f3c）に更新
- `4813c63` settings.json: `checks.test`を`GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...`、`claudeSettings`を`{"model": "opus"}`、`egress`は空のまま
- `efd78f3` `.masuda/pitfalls.jsonl`（11件。指示書の列挙どおり）
- `.masuda/claude/`は作らなかった（リポジトリの`CLAUDE.md`はcloneで届き、追加で要るものが無い）
- `docs/work-orders.md`のM14の追記（監督が書いた指示書）は未コミットのまま触っていない
## 完了した契約テスト
- `GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test -count=1 ./...`緑（全コミット後に再実行）。契約テストは無修正
- `masuda workflow check`（フェイクserve、scratchpadの一時data-dir・ソケット）: `ok`。不正なcategoryの行を一時的に足すと`pitfalls.jsonl: line 16: category "edge" ...`の問題1件・終了コード1になり、pitfallsを検査していることを確かめた（行は戻した）
- `masuda image build default`（実物のserve: `--sandbox-socket $XDG_RUNTIME_DIR/masuda-sandbox-dev.sock --data-dir ~/.local/share/masuda-dev --socket $XDG_RUNTIME_DIR/masuda-dev.sock`）: 成功、`note:`無し。ビルドログで`Installing Claude Code native build 2.1.287`。Build ID `1ca46682-d790-5029-9da7-0e5e8a7a4254`。serveは終わってから止めた
- ゲストのunshare: 下の注意点。**通った**
- 終了時`pgrep -c qemu-system`は0。自分で立てたserve（フェイク・実物）は止めた。開発版sandbox serve（`masuda-sandbox-dev.sock`）は動かしたまま
## 未完と理由
- `comment-criteria`と`comment-manifest`は実機で1周させていない（implementerが実際に一覧を書くか、基準を言えないコメントを消すか、reviewerが照合するかは未確認）
- 人間への質問（plan-interviewerの`ask_human`→`question answer`→`revise-answered`）は実機で一度も通っていない（前回から持ち越し）
- 記憶の無いサブエージェントに「前に書いた文字列」を求める課題はAPIの安全分類器に止められる件（前回から持ち越し。本番の続きは入力を持つので同じ形にはならない見込み）
- `claudeSettings.model`がサブエージェントまで効くかは未確認（#69の範囲）
## 次の一手
1. **#68の後半**: 作業ツリーのserve（`~/.local/share/masuda-dev`・`$XDG_RUNTIME_DIR/masuda-dev.sock`、sandboxは`masuda-sandbox-dev.sock`）で`workflows/fix`の予行。指示書は`docs/work-orders.md`に項目を書いて`instructions`に渡す。egressの承認（今は宣言なし）とトークンの置き場所（開発版data-dirの`secrets/_user/`）を先に確かめる
2. **#69**: `claudeSettings.model`（今回`opus`を書いた）でサブエージェントのモデルまで変わるかを実機で確認
3. **#70**・**#61の残り**・v0.2.0のリリース（前回のHANDOFFの次の一手3〜6のまま）
## 注意点
- **unshareの確認結果**: `.masuda/images/default`のイメージ（上のBuild ID）で作ったVM（ubuntuユーザー、egress無し、disk 8192MiB）で、フェイクsandboxの`asRoot`と同じ形`/usr/bin/unshare -Urm /bin/sh -c <rootExecScriptの写し> sh /tmp/gr /workspace /usr/bin/id -u`が終了コード0で`0`を出した。カーネル`6.18.54-0-virt`（Alpine linux-virt）、`unshare from util-linux 2.39.3`、`/proc/sys/user/max_user_namespaces`=15492、`unprivileged_userns_clone`・`apparmor_restrict_unprivileged_userns`はどちらも存在しない。さらに作業ツリーをtarでVMに入れ、egress無しのまま`checks.test`と同じ`GOWORK=off go build/vet/test ./...`が全パッケージ緑（contract 4.6秒・serve 6.3秒、C-M7と特権コマンドのテストを含む）。確認に使った`live/zz_m14a_unshare_test.go`はコミットせず消した。VMは`DestroySandbox`で壊した
- **判断した点**:
  - `egress`は空: Goモジュールはイメージで`go mod download all`済み、goplsは`~/go/bin/gopls`にある。上のegress無しのVMでビルド・テストが通った。bufはイメージに無いが、`buf generate`は`buf.build`のリモートプラグインと隣の`../masuda-sandbox/proto`を要するのでゲストではどのみち動かず、宣言も導入もしていない（プロトを変える作業は契約変更なのでゲストではやらない前提）
  - `checks.test`の`GOWORK=off`はゲストでは効かない（go.workはgitignoreで届かない）が、指示書どおり明示した
  - 落とし穴`test-case-names`: 指示書の「日本語で文で書く」と「既存テストに倣う」が関数名では食い違う（既存は英語の関数名＋直前の日本語コメント）。関数名は既存に倣い、直前のコメントと`t.Run`のサブテスト名を日本語の文にする問いにした
  - `.gitignore`の`*.pid`等ほかの行は旧実装の名残の可能性があるが、範囲外なので触っていない
- 開発版のdata-dir `~/.local/share/masuda-dev`は今回の`image build`で初めて作られた（中身はビルドの記録だけ）
- ゲストイメージのgoplsは`@latest`（ビルド時はv0.23.0）で固定していない
- DONEでVMを壊すのは`reflect`の中（＝engineを進めた呼び出しの中）。実VMでは、DONEに至ったnext_task・report_resultの応答はVMが先に壊れるのでゲストに届かない（メインセッションは終わるだけなので害は無い）。会話ログはその時点までのもので、最後のツール呼び出しの行が入らないことがある
- DONEでもrunCtl（ゲスト向けMCPサーバー）はRemoveまで残る（publishの後と同じ。フェイクの契約テストがdoneの後にnext_taskを呼ぶため、閉じていない）
- 依頼文では「`workflows/review`も`end`で終わる」とされていたが、同梱のreviewは`discard`（`export: [report, findings]`）を通る。`end`で終わるのはdevelopの`end:needs_human`等、fixの`needs_human`、利用者の自前ワークフロー、liveの継続テスト
- `.masuda/claude/`で無視したものの警告は`masuda serve`の標準エラーにしか出ない（protoを変えないため`masuda run`の応答には載らない）。`masuda workflow check`も検査しない
- `docs/guest-protocol.md`の`/masuda/reviews/*.md`の行は「同梱の観点」に直した（個数は書かない）
- `contract/contract_test.go`の冒頭には「監督が所有、実装者は書き換えない」とあるが、依頼によりC-M10を足した。`docs/work-orders.md`の対応表にはC-M9・C-M10が無い
- liveの後始末: VMを使うテストはserveとctxの後始末を`t.Cleanup`で`destroyVMOnCleanup`より先に登録すること（deferにするとAPIが先に閉じ、VMを壊せない）
- `docs/user/reference/workflow-schema.md`はengineの写し（`scripts/docs-prepare.sh`）で、`comment-manifest`はまだ反映していない。サイトのビルド時に取り込み直す
- live 2本を続けて回すときは`-timeout 100m`。2本目は開始時に`lapBudget`（45分）の残りを求めるので、`-timeout 60m`では1本目が15分を超えると2本目が失敗する
- 落とし穴の写しは定義の写し（`records/definitions/pitfalls.jsonl`）がそのまま兼ねる。観点（`records/reviews/`）のような別のスナップショットは作っていない（同梱が無く重ねる相手がいないため）
- **契約（`docs/guest-protocol.md`）が変わったので次のリリースはv0.2.0**（engineも同じ。engineのHANDOFFより）
- engineの制約1「出力は`done`の報告でしか保存されない」は**反映済み（engine `9a16b1e`）**。done以外でも書かれた出力は検証して保存される（recheckerは取り下げをunresolvedと同じ報告で書ける、`66cd4f7`）
- recheckerの`withdrawn`が保存時に捨てられ、synthesizerが「反論して取り下げられた指摘」を載せられない件は**engine #7**（v0.3、masuda #67と一緒に）
- サブエージェントのIDはVMの中のClaude Codeでしか通じない。IDの結び付けは「そのrunCtlが最後に渡したタスクの出現」なので、メインセッションが`next_task`を2回呼ぶ（同じタスクが返る）と同じ出現に上書きされるだけで害は無い。serveを起こし直すとrunCtlも作り直される（その時点でVMも作り直し）ので、メモリの`lastTask`が消えても困らない
- liveの続きの役（rememberer・recaller・copier）は`tools: Read`。`Write`を持たせるとengineが書き込める役とみなし、承認済みの計画を求めて検査で拒否する（出力は`write_output`なのでWriteは要らない）
- ゲストのClaude Code 2.1.287では`Agent`ツールが非同期で起動し（`async_launched`）、完了は`<task-notification>`で届く。メインセッションはその間ターンを終える（Stopフック）。続き（`SendMessage`）は完了済みのサブエージェントを`Resuming agent`で再開する
- **次のリリースノートに書く**: 同梱の`agents/implementer`の入力が`[plan, investigation]`になった。対象リポジトリの自前のワークフローでimplementerを使い、それより前に`investigation`を用意していないと、`masuda run`・`masuda workflow check`の検査（engineの`Set.Check`）で拒否される。同梱では`develop`のinvestigatorと`fix`のquick-plannerが用意する。`docs/user/workflows.md`の上書きの節にも書いた
- `fix`のreworkが読む`investigation`は最初の計画時のまま（reworkの前に調べ直さない）。`fix`の`plan`（`max: 3`）には`exhausted`の行き先が無く、上限に達するとblockedで止まる（engineのHANDOFFより）
- done以外の終わり方の`reason`はserveが`report_result`で受け取ったfeedbackの全文。CLIは1行目しか出さないので、全文は`workspace.json`の`reason`（またはAPIの`Get`）。docsにはそう書いた
- **ゲストのClaude Codeの版は`internal/guest.ClaudeCodeVersion`で固定**（2.1.287）。上げたら`TestGuestSubagentContinuation`を先に回す。サブエージェントの継続（SendMessage）の仕組みが変わればここが落ち、「同じエージェントにレビュー指摘の修正を続けさせる」設計の前提が崩れる。版はテストログの`continuation-report`の1行目に出る
- `install.sh`は版を渡しても、まず`latest`の版のブートストラップを落としてから指定の版を入れる（install.shの148行目付近）。固定されるのは最終的に入る版で、インストーラ自体の振る舞いは上流次第
- 利用者のリポジトリの既存のDockerfileは`masuda init`が書き換えないので、masudaを上げても版は変わらない。`masuda image build`の`note:`で気づける
- `serve/settings.go`の`guestEnv`のコメントは「トークンを除く」だが、execの環境に`CLAUDE_CODE_OAUTH_TOKEN`（プレースホルダ）が入っていた（継続テストのレポートで`token source: exec-env`）。出どころ未調査（sandboxが秘密のプレースホルダをExecの環境に入れている可能性）。直していない
- `end:failed`の`failed`はengineの予約ラベルで読み込みが拒否される。continuationの失敗は`end:not_continued`
- 継続テストのclaudeは`--settings '{"disableAllHooks":true}'`で起こす。フックはmasudaの`/hooks`に届き、このセッションのSessionEndがメインセッションの死（DEAD）と区別できないため
- `masuda-sandbox serve`は`cd ~/work/masuda-sandbox && node dist/cli.js serve --socket $XDG_RUNTIME_DIR/masuda-sandbox.sock`で起こす（2026-10-03夕方時点で起動したまま。`masuda serve`の開発版は動いていない）。liveで`MASUDA_LIVE_KEEP=1`にして残した`/tmp/masuda-live-*`は調査用で消してよい
- **開発版とハーネスの分離の決まり**（#68・#70に本文あり）: 公開物のmasuda/serve/sandbox＋既定ソケット＋`~/.local/share/masuda`がハーネス。開発版は既定のソケット・data-dirを使わない。masuda自身の`.masuda/`は入っているハーネスの版で読める範囲に留める。sandbox.protoを変える作業は開発版sandboxを別ソケットで
- **監督（Fable）がOpusに依頼する形の教訓**（2026-10-03の振り返り）: 指示書はファイルに残す、engineのpush回数を減らす（engineの作業が終わったらmasuda側は`go.work`で結合して進め、`go.mod`の固定は最後に1回）、liveを回したら`pgrep -c qemu-system`を見る、報告は短く詳細はHANDOFFへ
- 今日の実機で見つかった未修正の小さな事象: 継続テストの`records/subagents.json`はDONEで壊したVMのIDを持ったまま（無害）。`docs/user/reference/workflow-schema.md`はサイトのビルドで更新
- `trigger`の無い観点の扱いが変わった。旧trigger-matcherは「`trigger`を持たない観点は選ばない」だったが、engine `78818dd`のreviewer.mdは「途中レビュー: `trigger`を持たない観点は常に当てる」。同梱の`missing-tests-guard-clauses`・`missing-tests-new-code`も途中レビューで毎回当たる。これはユーザー判断で現状のまま確定（計画で実装とテストを同じステップに入れる方針になったため、テスト漏れの観点を途中で当てる意味がある）。`docs/user/reviews.md`はこの挙動で書いてある
- reviewerは「実行位置」のノード名が`interim-`で始まるかで途中レビューを判別する。masudaの`internal/runner/task.go`が出す「実行位置: ワークフロー…のノード…」の形を変えると壊れる
- `docs/user/workflows.md`の図は`masuda workflow show`の出力の貼り付け（`develop`・`fix`・`review`の3つ）。同梱定義が変わったら、`masuda serve --fake-sandbox --data-dir <tmp> --socket <tmp>/m.sock`を立て、リポジトリの外のディレクトリで`masuda workflow show workflows/<名前> --socket <tmp>/m.sock`を取り直して差し替える。`8af56b2`でdevelopの図を取り直した（fix・reviewは一致していた）
- `../masuda-engine`は別のエージェントが編集中のことがある。gitignore済みの`go.work`があると編集中のengineが混ざるので、ビルド・テストは`GOWORK=off`で行う
- `go get ...@main`はプロキシが古いmainを返すことがある。版が上がらなければ`GOPROXY=direct`。次のリリースでengineにタグを打ったら、そのタグへ`go get`し直す
- `docs/user/quickstart.md`の8節・9節の出力例は新しい描画の形で書いたもので、公開物（v0.1.0）の実走ではない。9節の`gate comment`もv0.1.0には無い。リリース時に実走の出力へ差し替えるとよい
- 行コメントの合成はserve側（engineへ渡す直前）で行い、engineの`Decision`や契約は変えていない
- 旧スキーマの計画はstepに`title`が無いので、ステップの見出しは`  1.`だけになる
- `serve`の`TestStallAfterFromLocalSettings`は`./...`一括実行で稀に落ちる（5秒以内にSTALLEDにならない）。単体と再実行では緑。今回は一括でも緑
## 契約への提案
なし
