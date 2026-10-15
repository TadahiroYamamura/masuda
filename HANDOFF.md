# HANDOFF
## 作業項目
2026-10-14〜15。v0.5.0を公開し、ハーネスを0.3.0から0.5.0へ上げた（追跡Issue #116）。

| リポジトリ | タグ | コミット |
|---|---|---|
| masuda | v0.5.0 | `7c75d28` |
| masuda-engine | v0.5.0 | `25f2947` |
| masuda-sandbox | v0.5.0 | `8fcd146` |

v0.5.0に入れたもの（このセッション分）:
- #88: VMの中の資源の警告（`Activity.resource_warning`、契約変更）
- #113: 観点を段階ごとの集合に分ける（`.masuda/reviews/<集合>/`、`implement`だけ同梱と重なる。直下の`.md`は`run`が断る。契約変更）
- #115: execの出力と、エージェントの理由の全文を残す（`records/results/<出現>/`。finishの`detail`の先頭から在りかを指す）
- 承認のコメントの見出しを「前のノード・ゲートからの申し送り」に変えた（`30c67c2`、契約の記述を直した）
- masuda-sandbox#11: 起動の後にカーネルのモジュールを展開する（`cac186f`）
- ゲストのClaude Codeを2.1.299へ（`82dcb9d`）

Issueの整理（10-14）:
- #85・#83・#74・#88・#115を閉じた。v0.4.0の追跡Issue #107も閉じた（ハーネスの更新はv0.5.0でまとめて行った）
- #65・#84は閉じた。根治はsandbox#9・#8で追う。sandbox#8はv0.6へ移した

ハーネスの更新（10-15）:
- ワークスペース17件を`remove`した
- oncall_pf_templateの`.masuda/reviews/*.md`を`implement/`へ移した
- 0.5.0を入れ、`doctor`がすべてokであることを確かめた
- Dockerfileのゲストの版を2.1.299にし、3つのイメージを作り直した
  - masuda: `default`（`0df7411`）
  - oncall_pf_template: `oncall-platform`・`oncall-platform-e2e`
## 完了した契約テスト
- リリース前の確認（#116の1-0〜1-2）
  - 継続テスト: 1分40秒
  - 実機1周: 6分22秒。2分以上の無反応なし
  - `precheck.sh`: すべてok
  - sandboxの契約テスト: 16件
  - tarballの予行、qemuの孤児なし
- 公開後の確認（#116の5）
  - ノートの3タグ・コミットが一致
  - 添付物5つ
  - サイトの`0.5`に`latest`
  - 一時ディレクトリに入れて`version`・`doctor`
## 未完と理由
- ハーネスで軽いタスクを動かして確かめる（ユーザーが行う予定）。確かめたら閉じる
  - #113: `/masuda/reviews/implement/`に15観点が届き、reviewerがそこを読む
  - sandbox#11: 特権VMでdockerdが動く
  - 併せて見ること: #88のACTIVITYの警告、#115のfinishの`[全文: ...]`、2.1.299のアイドル時の圧縮（ゲート待ちのメインセッション）
- sandboxの契約テストのC-S3（`timedOut`）が、3回のうち1回落ちた。`exec.ts`の`guestTimedOut`のコメントにある、起動直後の時計のずれと見ている（未確認）。続くようならIssueにする
- 持ち越し:
  - sandboxの`fix/destroy-with-ssh`（sandbox#8）の判断
  - マイルストーンの無いIssue（#103〜#106・#108・#60）の振り分け
  - Gondolin #155への返事
## 次の一手
1. ハーネスで軽いタスクを動かした結果を受けて、#113・sandbox#11を閉じる
2. v0.6のIssueの選定
## 注意点
- **engineの差分の承認が記録するコミットは、その実行の`commit`ノードが作ったものだけ**。`commit`ノードには承認済みの計画が要るので、エージェントを使わないワークフローでは秘密付きの特権ノードを通せない。実機で試すときは、`privileged.Run`を直接呼ぶプローブにする（今回の方法）
- 自動モードの権限判定：masuda-sandboxの編集は`.claude/settings.local.json`の`Edit(/home/ubuntu/work/masuda-sandbox/**)`で通る。秘密関連のcommitは止まるので、ユーザーに`!`で打ってもらう。判定を迂回しない
- 実VMの中を直接試すときは、`serve`パッケージに一時の`_test.go`を置き、`DialSandbox(<開発版のソケット>)`で`CreateSandbox`（`ListImages`の既存のbuild）→確かめたい関数を呼ぶ→`DestroySandbox`の形が速い（#88はこれで確かめた）。終わったら消す
- 開発版のsandboxの起動：`(cd ../masuda-sandbox && pnpm build && XDG_DATA_HOME="$HOME/.local/share/masuda-sandbox-dev-xdg" GONDOLIN_IMAGE_STORE="$HOME/.cache/gondolin-dev/images" exec node dist/cli.js serve --socket "$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock")`
  - liveの実行：`MASUDA_SANDBOX_SOCKET=$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock MASUDA_LIVE_TEST=1 go test ./live/`
  - 止めるとき：`pgrep`では見つからないことがある（プロセス名が`MainThread`）。`/proc/*/cmdline`でソケットのパスを確かめてから、そのpidを止める
- ハーネスは10-15にv0.5.0へ上げた。serveとsandboxはユーザーが端末で起動している（pidは控えていない）。ハーネスの`RunJob`のVMは、ジョブが終われば消える（消えても異常ではない）
- 走っているプローブを止めるときは、プローブのserveのソケットへ`go run ./cmd/masuda stop -socket <dataDir>/masuda.sock <id>`を送る。テストの後始末がVMを壊す。プローブの一時ディレクトリにはClaudeのトークンの写しが入るので、終わったら消す
- チェックアウトの状態：masudaは`develop`、masuda-sandboxとmasuda-engineは`main`（いずれもoriginと同じ）。masudaの`go.work`は隣のengineを使う
- フェイクのsandboxは、cwdとHOMEだけをゲストのrootへ写す。コマンドの中の絶対パス（`/masuda/checks/X`等）は写らないので、フェイクでは動かない
- `buf`は入っていない。`go run github.com/bufbuild/buf/cmd/buf@v1.73.0 generate`で作る。BSRは未認証だと短時間に数回でレート制限になる
- mkdocsは入っていない。`scripts/docs-prepare.sh`は`docs/`をその場で書き換えるので、手元で回したら`git checkout -- docs/`で戻す
- `prime.md`のdenyの一覧と`init.go`の`primeDenyRules`は、gate・secret・egress・privileged-commandにサブコマンドを足したら一緒に直す
- ほかのセッションとはSendMessageで話せる（このリポジトリのセッションは`masuda-d3`、ハーネスは`oncall-pf-template-20`）。相手の依頼でIssueを起票・返事するときは、こちらのユーザーの判断を取ってから
- 上流（Gondolin）への投稿は、ユーザーがWebから個人のアカウントで行う。この端末の`gh`は社用で、`scripts/gh.sh`は自分のリポジトリだけ
- この端末の`~/.gitconfig`は会社用。masuda・masuda-sandbox・masuda-engine・gondolinのcloneには、ローカルで個人用の作者を入れてある
- この端末（WSL2）は時計が前後に約10秒跳ぶ
## 契約への提案
無し。v0.5.0の契約の変更は、すべてユーザー承認のうえで入れた:
- `Activity.resource_warning`
- `guest-protocol.md`の`/masuda/reviews/<集合>/`
- `guest-protocol.md`のタスクファイルの申し送りの記述
