# HANDOFF
## 作業項目
M14e（`docs/work-orders.md`。#69の続き、ユーザー決定2026-10-03）: `.masuda/settings.json`の`agents`で役ごとの`model`・`effort`を上書きする。develop `80f4dc1`〜`89782be`（**未push**）。
- `80f4dc1` `internal/config`: `Settings.Agents map[string]AgentOverride`（`Model`・`Effort`は`*string`）、`config.Efforts`。検査は「modelもeffortも無い（`{}`・`null`）」「空白だけのmodel」「一覧外のeffort」を拒否、知らないキーは既存の`DisallowUnknownFields`。`serve/settings.go`の`unknownAgentOverrides`で、定義（`set.Agents`＝同梱＋`.masuda/agents/`のすべて。到達可能な役に限らない）に無い名前を`planBoot`の冒頭でInvalidArgument（知っている役の名前を列挙）。`serve/workflows.go`の`Check`に`settingsProblems`（作業ツリーのsettings.jsonを読み、同じ文を`Path: settings.json`の問題に。読めなければその理由を問題に）
- `45494c2` `serve/boot.go`の`prepareGuest`: `withOverride`でengine.Agentの写しに`plan.agents[name]`を重ねてから`guest.AgentFile`。engineの`Set`は書き換えない。`bootPlan.agents`は実行開始時の写しのsettings.jsonから
- `5cd0dac` docs: `docs/user/settings.md`に`### agents`（例・優先順位・名前の調べ方・`continues`）、冒頭の表と全体例に追加、`claudeSettings`から参照。`docs/user/workflows.md`の`model`・`effort`の箇条から参照
- `89782be` `.masuda/settings.json`: `claudeSettings.model`を`sonnet`、`agents`で`reviewer`・`cross-cutting-explorer`・`cross-cutting-verifier`を`{"model": "opus"}`
## 完了した契約テスト
- `GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test -count=1 ./...` 緑（HEAD `89782be`）。契約テストC-M1〜C-M10は無修正で緑
- 新しいテスト（名前は日本語の文）: `internal/config`の`TestAgentOverrides`（効く値・effort全5値・不正effort・大文字違い・空model・知らないキー・`{}`・`null`）、`serve`の`TestRunAppliesAgentOverridesToGuestAgents`（同梱のechoにmodel・effortの行が出る／`.masuda/agents/echo.md`のfrontmatter `model: sonnet`・`effort: low`に対し設定`opus`が勝ちeffortはlowのまま／設定無しなら行が出ない）、`TestUnknownAgentOverrideRefusesRunAndShowsInCheck`（`agents/reviewer`と書いた名前でRunがInvalidArgument・ワークスペースを作らない・checkに出る・正しい名前なら問題0）
- 分岐を壊して落ちることを確認して戻した: configの3検査とeffort一覧から`xhigh`を抜く、`withOverride`のModel・Effortの各分岐と`*o.Model`→`a.Model`、`unknownAgentOverrides`の判定、planBootの呼び出し、エラーコード（FailedPreconditionに変える）、Checkへの`settingsProblems`の追加
- 一時のフェイクserve（scratchpadの一時data-dir・一意なソケット、終了済み）で、このリポジトリに`masuda workflow check`（全root）と`workflows/develop`・`fix`・`review`がいずれも`ok`。打ち間違い（`reviwer`）の一時リポジトリでは問題1件・終了コード1
## 未完と理由
- 実機（live）では確かめていない（作業指示の検証に含まれない）。frontmatterに`model`・`effort`があればゲストのClaude Codeが従うことはM14bのliveで確認済みで、今回はその書き出しの手前で値を差し替えるだけ
- `comment-criteria`・`comment-manifest`の実機1周、`ask_human`の実機、記憶の無いサブエージェントへの課題（前回から持ち越し）
## 次の一手
1. 開発版serve（`masuda-dev`）をM14e入りでビルドし直してから、このリポジトリを対象にrunを始める（下の注意点）
2. 予行でSonnet化の効果（時間・キャッシュ読出）を測る
3. **#70**・**#61の残り**・v0.2.0のリリース
## 注意点
- **`.masuda/settings.json`に`agents`が入ったので、M14eより前のビルドのmasuda（公開済みv0.1.0、それより古い開発版serve）はこのリポジトリのsettings.jsonを知らないキーとして断る**。HANDOFFの「masuda自身の`.masuda/`は入っているハーネスの版で読める範囲に留める」には反するが、指示書（ユーザー決定）どおりにした。ハーネスの導入はv0.2.0公開後なので、v0.2.0に入れば解消する
- 監督のscratchpadの`masuda`（バイナリ）を今回のビルドで上書きした（`masuda-dev`と`devserve.pid`・動いている開発版serveには触れていない）。`serve.pid`も自分の一時serveのPIDで上書きした
- `agents`の照合先はそのrunで読み込んだ定義すべて。ワークフローが使わない役の名前（例: `develop`に無い`synthesizer`）を書いても拒否しない。同じsettings.jsonを全ワークフローで共有するため
- `settings.json`が読めない（壊れたJSON等）とき、`workflow check`はこれまで何も出さなかったが、今回から`settings.json`の問題として出る
- `docs/user/concepts.md`の「エージェントへの指示をどこに書くか」の表は指示の置き場所なので行を足していない
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
- なし（M14eでは契約を変えていない）
