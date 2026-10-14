# HANDOFF
## 作業項目
2026-10-14。v0.5.0の作業を進めた。いずれもdevelop（sandboxは`main`）へ入れてpush済みで、作業ブランチは消した。

| Issue | 中身 | masudaのコミット |
|---|---|---|
| #88 | VMの中のメモリ・ディスクの逼迫を、公開APIの`Activity.resource_warning`に載せる（契約変更・承認済み）。しきい値は`config.json`の`vmMemoryWarnPercent`・`vmDiskWarnPercent` | `a7dad1f`・`3d5e9ba`（マージ`8003855`）。Issueは閉じた |
| #85・#83・#74 | 10-11までに実装済みだった。修正が`origin/develop`に入っていることを確かめて閉じた | ― |
| #65・#84 | 閉じた。根治はsandbox#9（gondolinのTCP）・sandbox#8（ssh転送）で追う。新しいIssueは立てない（ユーザー判断） | ― |
| sandbox#11 | VMの起動の後（`vm.start()`の直後）に、ゲストで`.ko.gz`を展開して`depmod`する。depmodの無いイメージと、空きが足りないディスクでは何もしない。masudaの文書に「特権VMでDockerを動かす」節 | sandbox `cac186f`（`main`）、masuda `ad97d24`。**Issueはまだ閉じていない** |
| #113 | レビュー観点を段階ごとの集合（`.masuda/reviews/<集合>/`）に分け、ゲストの`/masuda/reviews/<集合>/`へ届ける（契約変更・承認済み） | `d978e2d`・`e13237e`・`e55effa`（マージ`651b146`）。**Issueはまだ閉じていない** |

#113の形（ユーザーの判断、10-14）:
- 実装のレビューの観点も`implement`の集合に移し、3つの段階を対称にする（案2）。直下の`.md`は置かない
- 同梱の15観点は`implement`の集合に入り、そこにだけ重なる。ほかの集合は置いたものだけ
- 直下の`.md`・集合の中のディレクトリ・`.`で始まる集合は、`run`が`InvalidArgument`で断る
- `Runner.Items(perspectives)`は`implement`の集合を返す。engineの`over`の文法は変えない
- 後方互換は考えない（利用者はユーザーだけ）。以前の版で始めたワークスペースの写しは読まない
sandbox#11の形（ユーザー判断、10-14）: 案A'。ビルドの設定の`init.rootfsInitExtra`に書く案Aは、Gondolinがディスクを拡げるのが`start()`の中（rootfsInitExtraより後）なので空きが常に足りず、展開できなかった。ビルドの時点で展開する案（B〜E）は、Gondolinの内部に頼り、版上げで黙って元に戻るので取らない。代わりに、VMごとに1〜2秒と約110MBのディスクを使う
- engineは`25f2947`（同梱のreviewer・review-checkerが`/masuda/reviews/implement/*.md`を読む）を`main`へ入れてpushした。masudaの`go.mod`はその擬似バージョン（`v0.4.1-0.20261014110642-25f2947937da`）
## 完了した契約テスト
- `go build ./...`・`go vet ./...`・`go test ./...`が、`go.work`のもとでも`GOWORK=off`でも緑（契約テストを含む）
- #88: 単体テスト（`serve/resources_test.go`）。判定の分岐13か所を壊し、すべてテストが落ちることを確かめた。実機（開発版のsandbox、2048MiBのVM）で、`/tmp over 90%`・`memory low`・`oom-kill x1`が出て、`/tmp`を消すと戻ることを確かめた
- #113: 単体テスト（`internal/perspectives`・`internal/guest`の`TestReviewPath`・`internal/runner`の`TestPerspectiveItemsAreTheImplementSet`）と、フェイクを使ったserveのテスト（`serve/reviews_test.go`。ゲストに集合ごとに置かれること、直下の`.md`は断ること）。判定の分岐を壊すと12か所すべてでテストが落ちる（残る1か所は以前の写しを読む分岐で、後方互換を考えないので消した）
## 未完と理由
- sandbox#11は、masuda経由で特権VMのdockerdを動かす1周を回していない（確かめたのはモジュールの読み込みまで）。v0.5.0を出したらハーネスで確かめて閉じる
- #113は実VMで確かめていない。ゲストの`WriteFile`が親ディレクトリを作ること（`sandbox.proto`）に頼っている。次の実機の周回で、`/masuda/reviews/implement/`に15観点があり、reviewerがそこを読むことを見る。見たら#113を閉じる
- **ハーネスを上げるときは、ハーネスのリポジトリの`.masuda/reviews/*.md`を`.masuda/reviews/implement/`へ移す**。移さないと`run`がエラーになる。reviewerを`.masuda/agents/`で上書きしていれば、その中の`/masuda/reviews/*.md`も直す
- #88: 本物の`masuda serve`の見回りの中で警告が出る様子は、実機で見ていない（エージェントを使う1周が要る）
- v0.5.0で開いているのは、実機の確認待ちの#113とsandbox#11だけ。sandbox#8の保険`8670a19`（`fix/destroy-with-ssh`）は、sandboxのmainに入っていない
- 前のセッションから持ち越し：
  - engineのタグを打ったら、masudaの`go.mod`をそのタグに上げる
  - v0.4.0の手順6・7
  - sandboxの`fix/destroy-with-ssh`（sandbox#8）の判断
  - マイルストーンの無いIssue（#103〜#106・#108・#60）の振り分け
  - Gondolin #155への返事
## 次の一手
1. v0.5.0をリリースする（Skill `release`。sandbox→engine→masudaの順にタグを打つ。タグのpushはユーザー）。engineのタグを打ったら、masudaの`go.mod`をそのタグに上げる
2. ハーネスを上げる。その前に、ハーネスのリポジトリの`.masuda/reviews/*.md`を`implement/`へ移す
3. ハーネスで軽いタスクを動かし、#113（`/masuda/reviews/implement/`に15観点、reviewerがそこを読む）を確かめて閉じる（ユーザーが行う予定）。特権VMでdockerdを動かせるならsandbox#11も確かめて閉じる
## 注意点
- **engineの差分の承認が記録するコミットは、その実行の`commit`ノードが作ったものだけ**。`commit`ノードには承認済みの計画が要るので、エージェントを使わないワークフローでは秘密付きの特権ノードを通せない。実機で試すときは、`privileged.Run`を直接呼ぶプローブにする（今回の方法）
- 自動モードの権限判定：masuda-sandboxの編集は`.claude/settings.local.json`の`Edit(/home/ubuntu/work/masuda-sandbox/**)`で通る。秘密関連のcommitは止まるので、ユーザーに`!`で打ってもらう。判定を迂回しない
- 実VMの中を直接試すときは、`serve`パッケージに一時の`_test.go`を置き、`DialSandbox(<開発版のソケット>)`で`CreateSandbox`（`ListImages`の既存のbuild）→確かめたい関数を呼ぶ→`DestroySandbox`の形が速い（#88はこれで確かめた）。終わったら消す
- 開発版のsandboxの起動：`(cd ../masuda-sandbox && pnpm build && XDG_DATA_HOME="$HOME/.local/share/masuda-sandbox-dev-xdg" GONDOLIN_IMAGE_STORE="$HOME/.cache/gondolin-dev/images" exec node dist/cli.js serve --socket "$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock")`
  - liveの実行：`MASUDA_SANDBOX_SOCKET=$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock MASUDA_LIVE_TEST=1 go test ./live/`
  - 止めるとき：`pgrep`では見つからないことがある（プロセス名が`MainThread`）。`/proc/*/cmdline`でソケットのパスを確かめてから、そのpidを止める
- ハーネス（sandbox 1008368・serve 1008554）は10-13 12:42から動いている。ハーネスの`RunJob`のVMは、ジョブが終われば消える（消えても異常ではない）
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
無し（#88の`Activity.resource_warning`と、#113の`guest-protocol.md`の`/masuda/reviews/<集合>/*.md`は、いずれもユーザー承認のうえで変更済み）。
