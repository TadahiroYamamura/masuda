# HANDOFF
## 作業項目
2026-10-03（夕）: **M14g**（同梱`develop`の見直し＝engine E14への、利用者向け文書と図の追従）。`go.work`で隣の`../masuda-engine`（E14: `a77af38`・`e0bd292`・`1c281ae`・`64a8e69`）を使った。masudaのコード・契約・`go.mod`は変えていない。

- `7ad995a` workflows.md: 同梱の表から`workflows/implement/interim-review`を削除、`implement/build-step`を「実装・テスト・コミット」に。`develop`の流れの行、途中レビューの段落を削り、plan gate却下（`revise-rejected`）とreview gate却下（`rework`→`rework-test`→`rework-commit`→`approve-review`、最終レビューはやり直さない）を追加。「まずfix」の段落を書き直し（数字は削除）。`develop`の図を取り直して差し替え（`fix`・`review`の図は差分なし）
- `990128d` reviews.md: `trigger`は自分のワークフローで`interim-`で始まるノードにreviewerを置いたときだけ効く、同梱は最終レビューで全観点、の形に
- `6d8ded3` concepts.md（plan・interim・reviewの行）、quickstart.md（fixの説明、8節の流れ、9節の却下）、troubleshooting.md（interimの行）

それ以前（v0.2の作業項目M14a〜M14f・E12〜E13・予行1〜4）の記録は前回のHANDOFF（`9ba887c`）と#68のコメントにある。

## 完了した契約テスト
- M14g: `go build ./... && go vet ./... && go test -count=1 ./...`（`go.work`有効、E14のengine）緑。`mkdocs`は入っていないので`mkdocs build --strict`は飛ばした。図はscratchpadのフェイクserve（一意なソケット、終了後に停止）からリポジトリ外で`workflow show`して取り直し、取り直し前の`git diff --stat docs/user/workflows.md`は空、後は`develop`の図の差し替え分のみ
- 以下は前回（M14a〜M14f）の記録
- `GOWORK=off go build ./... && go vet ./... && go test -count=1 ./...`緑（C-M1〜C-M10無修正）。`serve`の`TestStallAfterFromLocalSettings`は一括で稀に落ちる既知のもの（単体では緑）
- live（開発版sandbox `masuda-sandbox-dev.sock`、Claude Code 2.1.288）: `TestClaudeDirReachesSubagent`（M14b、model・effortの検査付き）、`TestGuestSubagentContinuation`（100秒）、`TestDevelopLapOnPythonRepo`（470秒）。終了後`qemu-system`は0
- masuda-sandboxの契約テスト8件緑（1回目はC-S3の`timedOut`が落ちたが再現せず）。tarball予行ok
- engine: `go test ./...`緑（C-E1〜C-E9）
## 未完と理由
- **M14gのgo.modの固定**: E14はengineのdevelop側にあり、masudaの`go.mod`は未だ`9c39140`。`GOWORK=off`だと同梱developは旧版（途中レビューあり）のままで、文書と食い違う。engineのpush後に監督が`go get`で固定する（指示書どおり）
- **docs/apiの残り**: `docs/api/flows.md`136行の「`target: "step-diff"`のゲート（同梱の定義では`interim`）」は同梱で使われなくなった。指示書の範囲（`docs/user/`）外なので未修正
- `mkdocs build --strict`は未実行（mkdocs未導入）。アンカー`workflows.md#develop`は既存のもの
- **v0.2.0のリリース**: 1-0・1-2まで済み。残りはdevelopのpush（ユーザー）→1-1 `precheck.sh v0.2.0`→engine・sandbox・masudaのタグ（ユーザー）→`go.mod`を`v0.2.0`へ→`main`をdevelopに合わせる→公開後の確認→ハーネスの更新（手順6、ユーザー）→v0.1マイルストーンを閉じる。#73のチェックボックス
- quickstartの8・9節の出力例の実走への差し替え（任意。公開物で1周するとき）
- #61の「特権コマンドの実機動作」は未確認のまま（今日の予行では特権コマンドを使う題材が無かった）
## 次の一手
0. engineのE14をpushし、masudaの`go.mod`を固定する（M14gの文書はそれで`GOWORK=off`でも正しくなる）。ついでに`docs/api/flows.md`のinterimの記述を直すか判断する
1. リリースの続き（上）。打つ直前に`git log origin/develop..develop`が空であること、`precheck.sh v0.2.0`が緑であることを確かめる
2. ハーネス導入後: `~/.local/share/masuda`を日付付きで退避、`masuda secret set CLAUDE_CODE_OAUTH_TOKEN`、既定のソケットで公開物のserveを起こす（SKILL.mdの手順6）。開発版は`masuda-dev`の場所のまま
3. 次のdevelop周回でreviewerをSonnetに下げてよいか再確認（予行3では観点レビューの差は小さかった）
4. v0.3の題材: #67（レビュー段階）、engine #7（withdrawn）、engine #9（fixerのcommit-message）、engine #10（却下の全工程やり直し、文書だけの途中レビュー省略）、masuda #71（liveが秘密ストアを読む）、#72（質問に補足を付ける）
## 注意点
- M14gで指示書の対象外も直した: `docs/user/troubleshooting.md`の`interim`ゲートの行（同梱では開かなくなった）、workflows.mdの`fix`の節の「developとの違い」の途中レビューの行（却下の戻り先の違いに置換）、同梱の表の途中に挟まっていた「まずfix」の段落を表の後へ移した（smokeの行が表から外れていた）
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
