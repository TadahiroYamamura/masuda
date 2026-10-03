# HANDOFF
## 作業項目
M14b（`docs/work-orders.md`。#69、契約変更・ユーザー承認2026-10-03）: 役定義の`model`・`effort`をゲストのサブエージェント定義に写す。develop `b9a363b`〜（**未push**）。
- `b9a363b` `internal/guest.AgentFile`: `a.Model`・`a.Effort`が空でなければfrontmatterに`model:`・`effort:`を`yamlString`で書く。関数コメントを直した。`guest_test.go`に`TestAgentFileModelAndEffort`（サブテスト名は日本語の文）。契約`docs/guest-protocol.md`の`~/.claude/agents/*.md`の行に「あれば`model`・`effort`を写す」
- `256bd0a` `docs/user/workflows.md`「エージェントの書き方」に`model`・`effort`（値、省略時の継承、`continues`では起動時の設定のまま）。`docs/user/settings.md`の`claudeSettings`に「`model`はメインセッションと`model`を書いていない役のモデルになる」。`docs/user/reference/workflow-schema.md`（写し）は触っていない
- `b5f419a` `live/claude_dir_test.go`: `TestClaudeDirReachesSubagent`を2ノード（prober→echoer）にし、proberに`model: sonnet`・`effort: low`、echoerは無し、`.masuda/settings.json`に`claudeSettings: {"model": "opus"}`（pythonRepoFilesの写しをやめ、`checks`も外した）。予算9分→12分。`exports/transcripts/**/subagents/*.jsonl`の応答から役ごとに`message.model`・`effort`を集めて検査
## 完了した契約テスト
- `go build ./... && go vet ./... && go test -count=1 ./...`（**`go.work`有効**、engineは隣の`../masuda-engine` `9c39140`）緑。契約テストC-M1〜C-M10は無修正で緑
- `TestAgentFileModelAndEffort`: `Model`の条件の反転・`Effort`の行を書かない・`Model`の行を常に書く、の3通りで落ちることを確かめて戻した
- liveの判定関数（`subagentModels`・`checkSubagentModel`）は過去のliveの会話ログ（`/tmp/masuda-live-data-3942533590`、sonnet・medium）で、model接頭辞の検査・effortの検査・役が無いときの検査・`assistant`の絞り込みをそれぞれ壊すと落ちることを一時テストで確かめた（一時テストは消した）
- live `MASUDA_SANDBOX_SOCKET=$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock MASUDA_LIVE_TEST=1 go test -count=1 -timeout 20m -v -run TestClaudeDirReachesSubagent ./live/`: **PASS（51秒）**。会話ログの記録: prober `claude-sonnet-5-5`×5・effort `low`×5、echoer `claude-opus-5-5`×6・effort `medium`×6。**ゲストのClaude Code 2.1.287でもfrontmatterの`effort`は効いた**。終了後`pgrep -af qemu-system`は自分の分なし
## 未完と理由
- `go.mod`のengineの固定は未（engineのE13がpush前のため。監督が行う）。今のコミットは`GOWORK=off`ではビルドできない（`engine.Agent`に`Model`・`Effort`が無い）
- `comment-criteria`・`comment-manifest`の実機1周、`ask_human`の実機、記憶の無いサブエージェントへの課題（前回から持ち越し）
## 次の一手
1. engineのE13をpushし、`go get github.com/TadahiroYamamura/masuda-engine@main && go mod tidy`（古ければ`GOPROXY=direct`）→`GOWORK=off go test ./...`
2. **#68の後半**: 開発版serveで`workflows/fix`の予行（M14c）
3. **#70**・**#61の残り**・v0.2.0のリリース
## 注意点
- echoer（`effort`を書かない役）の記録は`medium`。セッションの既定がそのまま継承された値で、`claudeSettings`に`effortLevel`等は書いていない
- 役の見分けは応答の`attributionAgent`（サブエージェント定義の`name`）。2.1.287の会話ログには`agentType`が無く、`records/subagents.json`はDONEに至った最後のタスク（今回はechoerの`0000002`）のIDを持たない
- メインセッション（`claudeSettings.model: opus`）の会話ログの値は、成功時にliveのdata-dirが消えるため確かめていない（サブエージェントのechoerがopusなので継承元はopusのはず）
- `TestClaudeDirReachesSubagent`の対象リポジトリの`settings.json`は`pythonRepoFiles`の写しではなくなった（`image`・`egress`は同じ、`checks`無し、`claudeSettings`あり）。Dockerfileは同じなのでイメージのキャッシュは効いた
- **unshareの確認結果**: `.masuda/images/default`のイメージ（M14aのBuild ID `1ca46682-d790-5029-9da7-0e5e8a7a4254`）で作ったVM（ubuntuユーザー、egress無し、disk 8192MiB）で、フェイクsandboxの`asRoot`と同じ形`/usr/bin/unshare -Urm /bin/sh -c <rootExecScriptの写し> sh /tmp/gr /workspace /usr/bin/id -u`が終了コード0で`0`を出した。カーネル`6.18.54-0-virt`（Alpine linux-virt）、`unshare from util-linux 2.39.3`、`/proc/sys/user/max_user_namespaces`=15492、`unprivileged_userns_clone`・`apparmor_restrict_unprivileged_userns`はどちらも存在しない。さらに作業ツリーをtarでVMに入れ、egress無しのまま`checks.test`と同じ`GOWORK=off go build/vet/test ./...`が全パッケージ緑（contract 4.6秒・serve 6.3秒、C-M7と特権コマンドのテストを含む）。確認に使った`live/zz_m14a_unshare_test.go`はコミットせず消した。VMは`DestroySandbox`で壊した
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
- M14bで`docs/guest-protocol.md`の`~/.claude/agents/*.md`の行に`model`・`effort`を足した（承認済み、`b9a363b`）。ほかの提案はなし
