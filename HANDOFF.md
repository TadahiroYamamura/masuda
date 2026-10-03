# HANDOFF
## 作業項目
2026-10-03の1日分（develop `32ff0b7`〜`d528e8d`、**push済み・CI緑**。engineは`bd9515b`〜`fd33f3c`をpush済み）。最後の項目は「publish・discardを通らずに終わったrunのVMが残る」問題の修正と、Issue #15の解決（**#15はクローズ済み**）。1日の全体: 計画スキーマの階層化と`gate show`、行コメントの却下時配達、レビューの一括化、`continues`（fixerが実装者の続きで反論）、`done`以外の出力の保存、`workflows/fix`、計画の問い立て（plan-questions/plan-reviser/plan-interviewer）と`pitfalls.jsonl`、`comment-manifest`と観点`comment-criteria`、`.masuda/claude/`、Claude Codeの版固定（2.1.287）とリリース手順、継続の能力検査、run終了時のVM破棄。contract変更あり→**次のリリースはv0.2.0**。protoは変えていない。契約`docs/guest-protocol.md`は`/masuda/reviews/*.md`の行の「同梱の14観点」を「同梱の観点」にしただけ（ユーザー承認済み）。
- 片付けを`internal/runner`の`Runner.Cleanup(ctx, export)`に1つにまとめた。sandboxがあれば（`GetSandbox`で確かめる）会話ログ→exports→実行ログの順に書き出してから壊し、無ければ実行ログだけ写す。壊した後は`destroyed`で二重にしない。旧`finish`（publish・discard）はこれを呼ぶだけになったので消した
- `serve/run.go`の`reflect`: DONE（outcomeを問わない）を状態に書く**前に**`Cleanup(c.ctx, nil)`。`end`・`end:<ラベル>`で終わったrunもVMが壊れる。BLOCKEDは従来どおり実行ログの写し直しだけ（VMは残す）
- `serve/lifecycle.go`の`stopRun`（Stop・Remove両方が通る）: `removeRun`の後に`Cleanup`（2分の上限）してから従来の`destroySandbox`。runCtlが無い（serve再起動後のBLOCKED、STOPPED・DONEのRemove）ときはその場で`runner.New`して使う。Removeは`RemoveKeepExports`の前に実行ログが写る
- `AttachInfo`（`masuda chat`）はDONEなら`FailedPrecondition`（「exports/transcriptsを読め」）。DONEでもrunCtl（MCP）は残るので、状態で断る
- #15は案1・案2の実装で**クローズ済み**（serveの再起動で残ったVMは従来どおり書き出さずに壊す。#15の範囲外）
- docs: user/operations.md・troubleshooting.md（新節`{#why-stopped}`）・cli.md、design/overview.md（exportsの回収時機の記述が逆になっていたので直した）
## 完了した契約テスト
- 2026-10-03: `GOWORK=off go build ./... && go vet ./... && go test -count=1 ./...`緑。契約テストC-Mは無修正で緑。足したテスト（`serve/run_test.go`）: `TestEndWithoutPublishCleansUpSandbox`（`workflows/fix`を`needs_human`で終え、DONEの後に`GetSandbox`がNotFound・exportsに実行ログと会話ログ・活動はIDLE・その後のnext_taskはdoneを返す・AttachInfoはFailedPrecondition）、`TestStopAndRemoveExportLogs`（Stopで実行ログと会話ログが写りVMが壊れる、写した実行ログを消してからRemoveすると再び写る）。どちらも修正前のコードで落ちることを確かめた
- 実機（sandbox serveが落ちていたのでHANDOFFの手順で起動した。`MASUDA_LIVE_KEEP=1 MASUDA_LIVE_TEST=1 GOWORK=off go test -count=1 -timeout 20m -v -run 'TestClaudeDirReachesSubagent|TestEngineContinuationKeepsMemory' ./live/`、60秒で全合格）。終了後`pgrep -c qemu-system`は0。両テストに`assertCleanedUpAtDone`（DONEでGetSandboxがNotFound、`exports/transcripts/`に*.jsonlがある）を足し、どちらもメイン1・サブエージェント1の計2本が書き出された
- 前回までの記録は`git log`（`52182c1`以前のHANDOFF）を参照
## 未完と理由
- `comment-criteria`と`comment-manifest`は実機で1周させていない（implementerが実際に一覧を書くか、基準を言えないコメントを消すか、reviewerが照合するかは未確認）
- 人間への質問（plan-interviewerの`ask_human`→`question answer`→`revise-answered`）は実機で一度も通っていない（前回から持ち越し）
- 記憶の無いサブエージェントに「前に書いた文字列」を求める課題はAPIの安全分類器に止められる件（前回から持ち越し。本番の続きは入力を持つので同じ形にはならない見込み）
## 次の一手
**v0.2の目標は「masudaを使ってmasudaが作れる体制」（ユーザー決定、2026-10-03）。スコープの正はGitHubマイルストーンv0.2**（masuda #68 #69 #70 #61、engine #8）。#67（レビュー段階）とengine #7（withdrawn）はv0.3へ。
1. **#68**: masuda自身の`.masuda/`を整える（Dockerfileの版固定、egress、`checks.test`を`GOWORK=off go test ./...`相当に、`pitfalls.jsonl`）→ゲストで契約テストの`unshare -Urm`が通るか→作業ツリーのserve（別ソケット・別data-dir）で`workflows/fix`の予行。**指示書は先に`docs/work-orders.md`の項目として書き、それを`instructions`に渡す**（今日の振り返りで決めた運用）
2. **#69**: `claudeSettings.model`でサブエージェントのモデルまで変わるかを実機で確認。足りなければengineへ契約の提案（`Agent.Model`）
3. **#70**: リリース手順にハーネスの更新と開発版との分離の決まり
4. **#61の残り**: chat（走行中・BLOCKEDで）、会話ログ、曖昧な指示書でplan-interviewerの質問→`question answer`→`revise-answered`を実機で通す
5. 実機の確認が未了のもの: implementer・fixerの`comment-manifest`と`comment-criteria`の指摘、`pitfalls.jsonl`を置いた1周でplan-questionsが落とし穴を問いに加えるか、review gateの`gate comment`で却下して行コメントが反映されるか
6. v0.2.0のリリース: SKILL.mdの1-0でClaude Codeを最新版へ上げ、継続テストと`TestClaudeDirReachesSubagent`→1周で検証。engineにタグを打ったらそのタグへ`go get`。quickstartの8・9節の出力例を実走に差し替え。v0.1のマイルストーンは閉じてよい
## 注意点
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
なし（guest-protocolの表への追記と`report_result`の備考の修正は反映済み。engine側の提案はengine #7とengineのHANDOFFに）
