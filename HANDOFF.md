# HANDOFF
## 作業項目
2026-10-13。v0.5.0に入れる#96（レビューで承認したコミットだけを、秘密を使う特権コマンドで新しいVMで動かす）のmasuda側（段1〜4）を実装した。前のセッション（10-11〜12）で#111（`prepare`）と#96の段0a・0bを済ませている。

**ブランチ**: masudaは`feat/privileged-secrets`（`feat/prepare`から切った。#111の5コミットを含む）。10-14にdevelopへマージしてpushし、ローカルの`feat/prepare`・`feat/privileged-secrets`は消した。

| コミット | 中身 |
|---|---|
| `fd79ef0` | 段1: `privilegedCommands.<name>`に`egress`・`secrets`。読み込みで次を弾く：placeholder以外、`CLAUDE_CODE_OAUTH_TOKEN`、トップレベルの`secrets`と同じ名前、秘密の`hosts`がそのコマンドの`egress`に無い、`inputs`との同時の宣言。`omitempty`なので既存の承認のハッシュは変わらない |
| `9f2097b` | 段2: engineの`ApprovedCommit`をrunner→serveへ渡す。秘密付きの宣言では次のとおり：<br>・MCPからと`ApprovedCommit`が空のときは断る<br>・`staging.CheckHead`（公開した）で先頭と突き合わせる<br>・`refs/masuda/approved/<run-id>`を張ってbundleにする<br>・`RunJob.secrets`に値を入れて渡す<br>特権VMのegressは、承認の積に宣言の`egress`を足したもの。実行開始時に値の未登録を断る。`secret set`・`list`は特権コマンドの秘密も扱う。`privileged-command run`は秘密付きの宣言を断る。フェイクの`RunJob`はplaceholderを環境に置く。`buf generate`と`sha.go`も含む |
| `e488e3d` | 段3: 秘密付きの宣言では、結果をメインのゲストに置かない（`MainID`を空にする）。engineへ返すログを決まった文に差し替える（記録には残す） |
| `132ceaf` | `/masuda/privileged-commands.json`から秘密付きの宣言を除く（ユーザー承認） |
| `c21df60` | 文書。`guest-protocol.md`の3か所は承認済みの契約変更。ほかに`settings.md`・`secrets-and-egress.md`（「秘密を使う特権コマンド」の節）・`overview.md` |

ユーザーの判断（10-13）:
- 秘密はトップレベルの`secrets`を参照せず、宣言に直接書く（案A）。参照にするとエージェントのVMにも渡るため
- 通信先も宣言に`egress`を持たせる（案b）。トップレベルに書くと、エージェントのVMから本物の宛先へ届き、ロックやレート制限を招きうるため
- 別のコマンドが同じ名前の秘密を違う`hosts`で宣言するのは弾かない
- `secret list`に特権コマンドの秘密を並べる（protoは変えない）
- `privileged-command run`では秘密付きの宣言を断る
- 実機の確認では外部へ送信しない
## 完了した契約テスト
- `go.work`のもとで、`go build ./...`・`go vet ./...`・`go test ./...`が緑。serve・contract・privilegedは`-race`も緑。`go.mod`のengineは`main`の擬似バージョン（`v0.4.1-0.20261012072726-d0c00e8c738e`）で、`GOWORK=off`でも緑
- 契約テスト（C-M11に2件追加）：
  - 秘密付きの特権ノードは承認済みのコミットのツリーで動く。次の6点を確かめる：<br>・値が未登録なら開始時に断る<br>・登録と一覧ができる<br>・MCPからの呼び出しは断る<br>・落ちたら差し戻しには決まった文だけが載り、ゲストに結果が無い<br>・ホストのログはplaceholderで、本物の値は無い<br>・承認の後に作業ツリーを書き換えても、承認済みのコミットで通ってpublishされる
  - 差分の承認の前に届くと「no approved commit」でSUSPENDEDになり、記録は残らない
- 判定の分岐（約20か所）を1つずつ壊し、対応するテストが落ちることを確かめた
- 実機（開発版のsandbox、一時のプローブ、外部への送信なし）：
  - `RunJob`の環境はplaceholderで、本物でも空でもない
  - `refs/masuda/approved/`のツリーで動く
  - `commit`ノードの無いワークフローでは断ってSUSPENDEDになる
## 未完と理由
- sandbox `feat/run-job-secrets`とengine `feat/privileged-approved-commit`は10-14にmainへ入れてpushした（ユーザーが`!`で実行）。v0.5.0のリリースでengineにタグを打ったら、masudaの`go.mod`をそのタグに上げる
- #9の残りは#114（v0.6）に分けた。#9・#96・#111は10-14に閉じた
- #112（古いイメージのビルドを消す手段）をv0.6、#113（段階ごとの観点の集合）をv0.5.0に振り、コードと照らした補足をコメントした（10-14、ユーザー判断）
- engineからmasudaを通って秘密付きのノードが動く経路は、実機では通していない（エージェントを使う1周が要るため。ユーザーの判断で案Aに絞った）
- v0.4.0の手順6・7、#85・#83・#74のクローズ、sandboxの`fix/destroy-with-ssh`（sandbox#8）の判断、マイルストーンの無いIssue（#103〜#106・#108・#60）の振り分け、Gondolin #155への返事：前のセッションから持ち越し
## 次の一手
1. v0.5.0の残り（#84、#88、#113、sandbox#11、#65）。#113はengineの`over`の検証・`workflow-schema.md`と、契約`guest-protocol.md`の`/masuda/reviews/*.md`の行に触れる（契約変更はユーザー承認）。終わったら、リリースでsandbox→engine→masudaの順にタグを打つ（タグのpushはユーザー）
2. 持ち越しの項目（上の「未完と理由」の最後）
## 注意点
- **engineの差分の承認が記録するコミットは、その実行の`commit`ノードが作ったものだけ**。`commit`ノードには承認済みの計画が要るので、エージェントを使わないワークフローでは秘密付きの特権ノードを通せない。実機で試すときは、`privileged.Run`を直接呼ぶプローブにする（今回の方法）
- 自動モードの権限判定：masuda-sandboxの編集は`.claude/settings.local.json`の`Edit(/home/ubuntu/work/masuda-sandbox/**)`で通る。秘密関連のcommitは止まるので、ユーザーに`!`で打ってもらう。判定を迂回しない
- 開発版のsandboxの起動：`(cd ../masuda-sandbox && pnpm build && XDG_DATA_HOME="$HOME/.local/share/masuda-sandbox-dev-xdg" GONDOLIN_IMAGE_STORE="$HOME/.cache/gondolin-dev/images" exec node dist/cli.js serve --socket "$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock")`
  - liveの実行：`MASUDA_SANDBOX_SOCKET=$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock MASUDA_LIVE_TEST=1 go test ./live/`
  - 止めるとき：`pgrep`では見つからないことがある（プロセス名が`MainThread`）。`/proc/*/cmdline`でソケットのパスを確かめてから、そのpidを止める
- ハーネス（sandbox 1008368・serve 1008554）は10-13 12:42から動いている。ハーネスの`RunJob`のVMは、ジョブが終われば消える（消えても異常ではない）
- 走っているプローブを止めるときは、プローブのserveのソケットへ`go run ./cmd/masuda stop -socket <dataDir>/masuda.sock <id>`を送る。テストの後始末がVMを壊す。プローブの一時ディレクトリにはClaudeのトークンの写しが入るので、終わったら消す
- チェックアウトの状態：masudaは`develop`、masuda-sandboxとmasuda-engineは`main`。masudaの`go.work`は隣のengineを使う
- フェイクのsandboxは、cwdとHOMEだけをゲストのrootへ写す。コマンドの中の絶対パス（`/masuda/checks/X`等）は写らないので、フェイクでは動かない
- `buf`は入っていない。`go run github.com/bufbuild/buf/cmd/buf@v1.73.0 generate`で作る。BSRは未認証だと短時間に数回でレート制限になる
- mkdocsは入っていない。`scripts/docs-prepare.sh`は`docs/`をその場で書き換えるので、手元で回したら`git checkout -- docs/`で戻す
- `prime.md`のdenyの一覧と`init.go`の`primeDenyRules`は、gate・secret・egress・privileged-commandにサブコマンドを足したら一緒に直す
- ほかのセッションとはSendMessageで話せる（このリポジトリのセッションは`masuda-d3`、ハーネスは`oncall-pf-template-20`）。相手の依頼でIssueを起票・返事するときは、こちらのユーザーの判断を取ってから
- 上流（Gondolin）への投稿は、ユーザーがWebから個人のアカウントで行う。この端末の`gh`は社用で、`scripts/gh.sh`は自分のリポジトリだけ
- この端末の`~/.gitconfig`は会社用。masuda・masuda-sandbox・masuda-engine・gondolinのcloneには、ローカルで個人用の作者を入れてある
- この端末（WSL2）は時計が前後に約10秒跳ぶ
## 契約への提案
無し（#96の`guest-protocol.md`の3か所、sandbox.protoの`RunJobRequest.secrets`、engineの`PrivilegedTask.ApprovedCommit`は、いずれもユーザー承認のうえで変更済み）。
