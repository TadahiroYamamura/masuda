# HANDOFF
## 作業項目
2026-10-03（午後）: マイルストーンv0.2「masudaでmasudaを作る体制」の作業項目をすべて終え、v0.2.0のリリース手順に入った（追跡Issue #73）。監督（Fable）が`docs/work-orders.md`のM14a〜M14f・engineのE12〜E13に指示書を書き、準備はOpusのサブエージェント、masuda自身の変更はmasudaのrun（予行1〜4）で監督がgate・questionを扱った。

- M14a（#68前半）: masuda自身の`.masuda/`をgitで追跡（旧実装の`.gitignore`で丸ごと無視されていた）。Dockerfileの版固定、`checks.test`を`GOWORK=off go build/vet/test`に、`pitfalls.jsonl`（11件）、同梱と同一だった`reviews/`14件は削除。ゲストで`unshare -Urm`が通り、契約テストもゲストで緑
- E12（engine #8）: engineリポジトリに`.masuda/`（settings・Dockerfile・pitfalls 8件・.gitignore）
- E13（engine、契約変更、ユーザー承認）: `Agent.Model`・`Agent.Effort`。役定義のfrontmatter`model`・`effort`（effortは`low`・`medium`・`high`・`xhigh`・`max`）。engine main `077256e`〜`9c39140`、push済み
- M14b（#69、契約変更）: `guest.AgentFile`が`model:`・`effort:`を書く。`docs/guest-protocol.md`の`~/.claude/agents/*.md`の行を更新。liveの`TestClaudeDirReachesSubagent`で、`model: sonnet`/`effort: low`の役はsonnet・low、無指定の役はメインのモデル（opus）・mediumを実測。`go.mod`はengine main `9c39140`に固定（`de57bb0`。リリースで`v0.2.0`へ）
- M14e（ユーザー決定）: `settings.json`の`agents`（役の名前→`model`・`effort`の上書き。frontmatterより優先。知らない役の名前は`Run`が`InvalidArgument`、`Resume`が`FailedPrecondition`、`workflow check`が問題として出す）。masuda自身は`claudeSettings.model: sonnet`（既定の役とメインセッション）、`reviewer`・`cross-cutting-explorer`・`cross-cutting-verifier`は`opus`
- 予行1（M14c、`workflows/fix`）: #64（`Watch`の`after_seq`が再送バッファより古いとき`OutOfRange`）。マージ`d67e735`
- 予行2（M14d、`workflows/develop`）: #70（リリース手順に開発版との分離とハーネスの更新）。マージ`1a1d012`、残指摘の手直し`d5a1e54`
- 予行3（`workflows/review`×3）: M14eの差分でreviewer/cross-cuttingのSonnet/Opus比較
- 予行4（M14f、`workflows/fix`）: M14eへのレビュー指摘7点。マージ`12dd6d0`
- ほか: `.masuda/claude/rules/`（coding・comments・testing・information-placement・communication・masuda-run）、`docs/user/workflows.md`に「まずfix」の目安、Claude Codeを2.1.288へ（`cd3203e`）
- 計測と気づきは#68のコメント（2026-10-03）に表で書いた
## 完了した契約テスト
- `GOWORK=off go build ./... && go vet ./... && go test -count=1 ./...`緑（C-M1〜C-M10無修正）。`serve`の`TestStallAfterFromLocalSettings`は一括で稀に落ちる既知のもの（単体では緑）
- live（開発版sandbox `masuda-sandbox-dev.sock`、Claude Code 2.1.288）: `TestClaudeDirReachesSubagent`（M14b、model・effortの検査付き）、`TestGuestSubagentContinuation`（100秒）、`TestDevelopLapOnPythonRepo`（470秒）。終了後`qemu-system`は0
- masuda-sandboxの契約テスト8件緑（1回目はC-S3の`timedOut`が落ちたが再現せず）。tarball予行ok
- engine: `go test ./...`緑（C-E1〜C-E9）
## 未完と理由
- **v0.2.0のリリース**: 1-0・1-2まで済み。残りはdevelopのpush（ユーザー）→1-1 `precheck.sh v0.2.0`→engine・sandbox・masudaのタグ（ユーザー）→`go.mod`を`v0.2.0`へ→`main`をdevelopに合わせる→公開後の確認→ハーネスの更新（手順6、ユーザー）→v0.1マイルストーンを閉じる。#73のチェックボックス
- quickstartの8・9節の出力例の実走への差し替え（任意。公開物で1周するとき）
- #61の「特権コマンドの実機動作」は未確認のまま（今日の予行では特権コマンドを使う題材が無かった）
## 次の一手
1. リリースの続き（上）。打つ直前に`git log origin/develop..develop`が空であること、`precheck.sh v0.2.0`が緑であることを確かめる
2. ハーネス導入後: `~/.local/share/masuda`を日付付きで退避、`masuda secret set CLAUDE_CODE_OAUTH_TOKEN`、既定のソケットで公開物のserveを起こす（SKILL.mdの手順6）。開発版は`masuda-dev`の場所のまま
3. 次のdevelop周回でreviewerをSonnetに下げてよいか再確認（予行3では観点レビューの差は小さかった）
4. v0.3の題材: #67（レビュー段階）、engine #7（withdrawn）、engine #9（fixerのcommit-message）、engine #10（却下の全工程やり直し、文書だけの途中レビュー省略）、masuda #71（liveが秘密ストアを読む）、#72（質問に補足を付ける）
## 注意点
- **開発版の置き場所**: データディレクトリ`~/.local/share/masuda-dev`、ソケット`$XDG_RUNTIME_DIR/masuda-dev.sock`・`masuda-sandbox-dev.sock`。バイナリはscratchpadの`masuda-dev`（HEADから`git archive`してビルド。作業ツリーのビルドだと未コミットの変更が混ざる）。`masuda-sandbox serve`は`cd ~/work/masuda-sandbox && node dist/cli.js serve --socket $XDG_RUNTIME_DIR/masuda-sandbox-dev.sock`。再起動後は落ちているので`masuda doctor --sandbox-socket ...`で見る
- **`~/.local/bin/masuda`は旧v1のバイナリ（9月7日）**でv0.1.0の公開物は入っていない。`~/.local/share/masuda`には旧走行のワークスペース13個とM4暫定の`claude-oauth-token`。ハーネス導入時に退避する（ユーザー決定: 導入はリリース後）
- Claudeトークンは`masuda secret set CLAUDE_CODE_OAUTH_TOKEN`（標準入力）で登録する。暫定ファイルの`cp`は安全判定で止まる。liveは暫定ファイルか`MASUDA_LIVE_CLAUDE_TOKEN`しか読まない（#71）
- **masudaのrunでmasudaを作るときの決まり**: 指示書は`docs/work-orders.md`の項目として書き、`---`以降を切り出して`--input instructions=@file`で渡す。`.masuda/`は作業ツリーから読まれ、リポジトリはHEAD（`--base`）からbare cloneされる。developに未固定のengineの変更を使うコードがあるとゲストでビルドできない（`go.mod`を先に固定する）。runの中の役はHANDOFF.md・work-orders.mdを書かない（`.masuda/claude/rules/masuda-run.md`）
- **会話ログの集計**: `exports/transcripts/`のJSONLは各応答に`usage`・`model`・`effort`・`perTurnEffort`を持ち、サブエージェントの役は`attributionAgent`で分かる。scratchpadの`tokens.py`で役ごとに集計した
- `workflows/review`はゲートが無く、指摘は`exports/findings`、レポートは`exports/report`。`records/comments.jsonl`はreview gateを開くときにしか作られない
- 選択肢付きの質問は`'ID=(b) 選択肢の文字列'`のように選択肢そのもので答える（#72）
- `gate reject`の行コメントは`gate comment <id> <occ> <path>:<line> <text>`で先に付けてから却下する。fixerに届き直った
- gate却下は工程を最初からやり直す（plan 12分、review 30分。engine #10）。小さい残指摘は承認して手で直すほうが速い
- review-commit（`scope: plan`）のメッセージは計画の`summary`全文になる（engine #9。fixerが`commit-message`を書かない）
- 途中レビュー（interim-review）が`clean`なら interim gate は開かない（文書だけの変更では毎回clean）
- `waiting_input(idle)`はメインセッションがサブエージェントの完了通知（非同期Agent）を待つ状態で、停止ではない
- 1-2のsandbox契約テストと`pnpm build`（tarball予行）は走行中の開発版sandboxのdistを作り直すので、先に止める（SKILL.mdに書いた）
- `masuda-engine`のmainはpush済み（`9c39140`）。masudaのdevelopは`7e4f580`…`cd3203e`までの40コミット超が未push（2026-10-03 16:45時点）
## 契約への提案
- なし。今日の契約変更（engine `Agent.Model`・`Effort`、`docs/guest-protocol.md`の`model`・`effort`の行と`settings.json`の`agents`の優先）はユーザー承認済みで反映済み
