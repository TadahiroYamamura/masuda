# HANDOFF
## 作業項目
2026-10-03（午後〜夕方）: マイルストーンv0.2「masudaでmasudaを作る体制」の作業項目をすべて終え、計測を受けて同梱`develop`を見直し（engine E14）、v0.2.0のリリース手順に入った（追跡Issue #73）。監督（Fable）が`docs/work-orders.md`のM14a〜M14g・engineのE12〜E14に指示書を書き、準備はOpusのサブエージェント、masuda自身の変更はmasudaのrun（予行1〜4）で監督がgate・questionを扱った。

- M14a（#68前半）: masuda自身の`.masuda/`をgitで追跡（旧実装の`.gitignore`で丸ごと無視されていた）。Dockerfileの版固定、`checks.test`を`GOWORK=off go build/vet/test`に、`pitfalls.jsonl`（11件）、同梱と同一の`reviews/`14件は削除。ゲストで`unshare -Urm`が通り、契約テストもゲストで緑
- E12（engine #8）: engineに`.masuda/`
- E13（engine、契約変更、ユーザー承認）: `Agent.Model`・`Agent.Effort`（effortは`low`・`medium`・`high`・`xhigh`・`max`）
- M14b（#69、契約変更）: `guest.AgentFile`が`model:`・`effort:`を書く。`docs/guest-protocol.md`の`~/.claude/agents/*.md`の行。liveの`TestClaudeDirReachesSubagent`で実測（指定した役はsonnet・low、無指定はメインのモデル・medium）
- M14e（ユーザー決定）: `settings.json`の`agents`（役名→`model`・`effort`。frontmatterより優先。知らない役名は`Run`が`InvalidArgument`、`Resume`が`FailedPrecondition`、`workflow check`が問題に）。masuda自身は`claudeSettings.model: sonnet`、`reviewer`・`cross-cutting-explorer`・`cross-cutting-verifier`は`opus`
- E14（engine、ユーザー決定）: 同梱`develop`からステップごとの途中レビューと途中の自動修正・interim gateを外し（`implement/build-step`は`implement`→`test`→`commit`、`implement/interim-review`は削除）、plan gateの却下は`revise-rejected`（plan-reviser）へ、review gateの却下は`rework`（implementerの続き）→`rework-test`→`rework-commit`→`approve-review`へ直接。M14gで`docs/user/`の説明・図・`interim`の記述を追従
- 予行1（M14c、fix）: #64。予行2（M14d、develop）: #70。予行3（review×3）: M14eの差分でSonnet/Opus比較。予行4（M14f、fix）: M14eへの指摘7点。計測は#68のコメント（表）
- ほか: `.masuda/claude/rules/`（coding・comments・testing・information-placement・communication・masuda-run）、`docs/user/workflows.md`に「まずfix」の目安、Claude Codeを2.1.288へ（`cd3203e`）、`go.mod`はengine main `64a8e69`（`590028a`。リリースで`v0.2.0`へ）
## 完了した契約テスト
- `GOWORK=off go build ./... && go vet ./... && go test -count=1 ./...`緑（C-M1〜C-M10無修正）。`serve`の`TestStallAfterFromLocalSettings`は一括で稀に落ちる既知のもの
- live（開発版sandbox `masuda-sandbox-dev.sock`、Claude Code 2.1.288）: `TestClaudeDirReachesSubagent`、`TestGuestSubagentContinuation`（100秒）、`TestDevelopLapOnPythonRepo`（E14後の`develop`で348秒。E14前は470秒）。終了後`qemu-system`は0
- masuda-sandboxの契約テスト8件緑（1回目はC-S3の`timedOut`が落ちたが再現せず、#73に記録）。tarball予行ok
- engine: `go test ./...`緑（C-E1〜C-E9、歩行テストはE14後の形）
## 未完と理由
- **v0.2.2も公開済み**（2026-10-03 20:25頃。masuda `608408c`。追跡Issue #77）。`masuda completion bash|zsh`（旧実装にあった補完の復活。ハーネスで初めて回したrun `543799595910`で実装。review gateで注入の指摘（動的候補を`compgen -W`に渡す）を行コメントで却下→修正）。ハーネスの更新はユーザーがインストーラで
- **v0.2.1も公開済み**（2026-10-03 19:00頃。masuda `887a82e`、engine・sandboxはv0.2.0と同じコミット。追跡Issue #75）。中身は文書の版のタグからの置き換え（`__MASUDA_VERSION__`、`scripts/docs-prepare.sh`）と`curl | sh`のインストーラ（`scripts/install.sh`→添付物`masuda_installer.sh`）。masudaのrunで実装（予行5、`workflows/fix`、`3ed39857b68b`）。途中で#74（bypass modeでも`rm -rf`の許可を求めて止まる。`masuda chat`で`1`を送って進めた）
- **v0.2.0は公開済み**（2026-10-03 17:35頃。masuda `841f270`、engine `64a8e69`、sandbox `a9dce82`。追跡Issue #73に表）。release・docs完走、添付物4つ、サイトは`0.2`が`latest`。公開物の一時的な導入で`contract: ok`・doctor ok（トークン以外）を確認。リリースノートに変更点を追記した
- **ハーネスの初回導入も完了**（19:10、v0.2.1のインストーラ）。既定のソケットで`masuda-sandbox serve`と`masuda serve`が動いている（ログは`~/.local/share/masuda/logs/`）。`contract: ok`・doctor全部ok。#73・#75は閉じた。残り（v0.2.2の後）: ハーネスを0.2.2に更新（ユーザー、インストーラ）→両serveを起こし直す→`source <(masuda completion bash)`。quickstartの8・9節の出力例の差し替え（任意）。#61の「特権コマンドの実機動作」はv0.3へ
- `go.mod`はengine `v0.2.0`に固定済み（`841f270`）。developとmainは同じコミット
## 次の一手
1. masuda自身のrunをハーネス（既定のソケット、公開物0.2.1）で回す。開発版serveは必要なときだけ`masuda-dev`の場所で
2. ハーネス導入後（SKILL.mdの手順6）: `~/.local/share/masuda`を日付付きで退避、`masuda secret set CLAUDE_CODE_OAUTH_TOKEN`、既定のソケットで公開物のserve。開発版は`masuda-dev`の場所のまま
3. 次のdevelop周回で、reviewerをSonnetに下げてよいか再確認（予行3では観点レビューの差は小さい、横断はOpusが要る）。E14後の`develop`の所要・トークンを予行2（90分・32M）と比べる
4. v0.3の題材: #67（レビュー段階のpr-review-guide。最終レビューの段の形はE14で保った）、engine #7、engine #9（fixerのcommit-message）、engine #10の残り（文書だけの差分のレビュー省略）、masuda #71（liveが秘密ストアを読む）、#72（質問に補足）
## 注意点
- **開発版の置き場所**: `~/.local/share/masuda-dev`、`$XDG_RUNTIME_DIR/masuda-dev.sock`・`masuda-sandbox-dev.sock`。バイナリはscratchpadの`masuda-dev`（HEADを`git archive`してビルド）。`masuda-sandbox serve`は`cd ~/work/masuda-sandbox && node dist/cli.js serve --socket $XDG_RUNTIME_DIR/masuda-sandbox-dev.sock`。再起動後は落ちている
- **ハーネス**: `~/.local/bin/masuda`=0.2.1、`masuda-sandbox`=0.2.1（nvmのnode 24のグローバル）、既定ソケット、`~/.local/share/masuda`（退避せず。旧走行はDONEの4件が見える。トークンはM4暫定ファイルのまま認識。正規の`secret set`は任意）。**以後、masuda自身のrunはハーネスで回す**（`masuda run ... --repo ~/work/masuda`、gate・questionもハーネスのCLI）。開発版は`masuda-dev`の場所
- Claudeトークンは`masuda secret set CLAUDE_CODE_OAUTH_TOKEN`（標準入力）。暫定ファイルの`cp`は安全判定で止まる。liveは暫定ファイルか`MASUDA_LIVE_CLAUDE_TOKEN`しか読まない（#71）
- **masudaのrunでmasudaを作る**: 指示書は`docs/work-orders.md`の項目として書き、`---`以降を`--input instructions=@file`で渡す。`.masuda/`は作業ツリーから、リポジトリはHEAD（`--base`）からbare clone。developに未固定のengineの変更を使うコードがあるとゲストでビルドできない（先に`go.mod`を固定）。runの中の役はHANDOFF.md・work-orders.mdを書かない（`.masuda/claude/rules/masuda-run.md`）
- **会話ログの集計**: `exports/transcripts/`のJSONLは各応答に`usage`・`model`・`effort`、役は`attributionAgent`。scratchpadの`tokens.py`
- `workflows/review`はゲートが無く、指摘は`exports/findings`、レポートは`exports/report`。`records/comments.jsonl`はreview gateを開くときだけ
- 選択肢付きの質問は選択肢の文字列そのもので答える（#72）。行コメントは`gate comment`で先に付けてから`gate reject`
- E14後も`interim`はゲート名として予約のまま（自分のワークフローで使える）。reviewerは「実行位置」のノード名が`interim-`で始まるかで途中レビューを判別する（`internal/runner/task.go`の形を変えると壊れる）
- review-commit（`scope: plan`）のメッセージは計画の`summary`全文（engine #9）
- `waiting_input(idle)`はメインセッションがサブエージェントの完了通知を待つ状態
- 1-2のsandbox契約テストと`pnpm build`は走行中の開発版sandboxのdistを作り直すので先に止める（SKILL.md）
- 3リポジトリとも`v0.2.0`のタグまでpush済み。このHANDOFFのコミットはdevelopに乗るのでpushはユーザー指示で
## 契約への提案
- なし（今日の契約変更はすべてユーザー承認済み・反映済み: engine `Agent.Model`・`Effort`、`docs/guest-protocol.md`の`model`・`effort`の行と`settings.json`の`agents`の優先）
