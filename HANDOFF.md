# HANDOFF
## 作業項目
2026-10-14。v0.5.0の#88（VMの中のメモリ・ディスクの逼迫にホストから気づけない）を実装した。あわせて、実装済みだった#85・#83・#74を閉じた。

**ブランチ**: masudaは`feat/vm-resource-warning`（developの`c992956`から）。developへのマージとpushはまだ。

| コミット | 中身 |
|---|---|
| `a7dad1f` | 公開APIの`Activity`に`resource_warning`を足した（契約変更、ユーザー承認）。`patrol`の生存確認（30秒ごと）の後に、同じVMで`/proc/meminfo`・`/proc/vmstat`・`df -Pk / /tmp`を読み、`memory low`・`<マウント> over N%`・`oom-kill xN`を載せる。しきい値は`config.json`の`vmMemoryWarnPercent`（既定10）・`vmDiskWarnPercent`（既定90）。CLIはACTIVITYに`[...]`を添える |
| `3d5e9ba` | 文書：`overview.md`「VMの中の資源」、`operations.md#vm-resources`、`settings.md`のconfig.json、`troubleshooting.md#vm-memory`、`flows.md` |

ユーザーの判断（10-14）:
- 警告は`ServeNotice`ではなく`Activity.resource_warning`に載せる（案A）
- #85・#83・#74は、修正が`origin/develop`に入っていることを確かめて閉じる（10-14に閉じた。いずれも10-11までに実装済みで、配布はv0.5.0から）
## 完了した契約テスト
- `go build ./...`・`go vet ./...`・`go test ./...`が緑（契約テストを含む）。serveは`-race`も緑。`GOWORK=off`でもビルドが通る
- 契約テストは増やしていない。フェイクのsandboxでは測らないため（Execがホストで流れ、ホストの値になる）。単体テスト：`serve/resources_test.go`（解析・しきい値の境界・測れないときの扱い・状態ごとの表示）と、`cmd/masuda`の`TestListRow`
- 判定の分岐13か所を壊し、すべてテストが落ちることを確かめた
- 実機（開発版のsandbox、2048MiBのVM、一時のプローブ）で確かめたこと：
  - `/tmp`を埋めると`/tmp over 90%`が出て、消すと戻る
  - メモリを使い切っている間は`memory low`が出る
  - OOM killerが動くと`oom-kill x1`が出て、残る
## 未完と理由
- 本物の`masuda serve`の見回りの中で警告が出る様子は、実機で見ていない（エージェントを使う1周が要る）。次にハーネスを上げたときに、`masuda list`に出るかを見る
- 実VMでメモリが逼迫している最中は、1回の測定に上限（10秒）近くかかることがあった。見回りは逐次なので、ワークスペースが多いと1巡が延びる
- v0.5.0の残り：#113、sandbox#11、#84（上流待ちの残り）、#65（緩和済み・根治は上流待ち。閉じるか分けるかの判断）。sandbox#8の保険`8670a19`（`fix/destroy-with-ssh`）はsandboxのmainに未マージ
- 前のセッションから持ち越し：
  - engineのタグを打ったら、masudaの`go.mod`をそのタグに上げる
  - v0.4.0の手順6・7
  - sandboxの`fix/destroy-with-ssh`（sandbox#8）の判断
  - マイルストーンの無いIssue（#103〜#106・#108・#60）の振り分け
  - Gondolin #155への返事
## 次の一手
1. v0.5.0の残りのどれに移るかをユーザーに確かめる（#113、sandbox#11、#84・#65の判断）
2. `feat/vm-resource-warning`をdevelopへマージする（ユーザーの指示を待つ）
3. v0.5.0の残りが終わったら、リリースでsandbox→engine→masudaの順にタグを打つ（タグのpushはユーザー）
## 注意点
- **engineの差分の承認が記録するコミットは、その実行の`commit`ノードが作ったものだけ**。`commit`ノードには承認済みの計画が要るので、エージェントを使わないワークフローでは秘密付きの特権ノードを通せない。実機で試すときは、`privileged.Run`を直接呼ぶプローブにする（今回の方法）
- 自動モードの権限判定：masuda-sandboxの編集は`.claude/settings.local.json`の`Edit(/home/ubuntu/work/masuda-sandbox/**)`で通る。秘密関連のcommitは止まるので、ユーザーに`!`で打ってもらう。判定を迂回しない
- 実VMの中を直接試すときは、`serve`パッケージに一時の`_test.go`を置き、`DialSandbox(<開発版のソケット>)`で`CreateSandbox`（`ListImages`の既存のbuild）→確かめたい関数を呼ぶ→`DestroySandbox`の形が速い（#88はこれで確かめた）。終わったら消す
- 開発版のsandboxの起動：`(cd ../masuda-sandbox && pnpm build && XDG_DATA_HOME="$HOME/.local/share/masuda-sandbox-dev-xdg" GONDOLIN_IMAGE_STORE="$HOME/.cache/gondolin-dev/images" exec node dist/cli.js serve --socket "$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock")`
  - liveの実行：`MASUDA_SANDBOX_SOCKET=$XDG_RUNTIME_DIR/masuda-sandbox-dev.sock MASUDA_LIVE_TEST=1 go test ./live/`
  - 止めるとき：`pgrep`では見つからないことがある（プロセス名が`MainThread`）。`/proc/*/cmdline`でソケットのパスを確かめてから、そのpidを止める
- ハーネス（sandbox 1008368・serve 1008554）は10-13 12:42から動いている。ハーネスの`RunJob`のVMは、ジョブが終われば消える（消えても異常ではない）
- 走っているプローブを止めるときは、プローブのserveのソケットへ`go run ./cmd/masuda stop -socket <dataDir>/masuda.sock <id>`を送る。テストの後始末がVMを壊す。プローブの一時ディレクトリにはClaudeのトークンの写しが入るので、終わったら消す
- チェックアウトの状態：masudaは`feat/vm-resource-warning`（developへは未マージ・未push）、masuda-sandboxとmasuda-engineは`main`。masudaの`go.work`は隣のengineを使う
- フェイクのsandboxは、cwdとHOMEだけをゲストのrootへ写す。コマンドの中の絶対パス（`/masuda/checks/X`等）は写らないので、フェイクでは動かない
- `buf`は入っていない。`go run github.com/bufbuild/buf/cmd/buf@v1.73.0 generate`で作る。BSRは未認証だと短時間に数回でレート制限になる
- mkdocsは入っていない。`scripts/docs-prepare.sh`は`docs/`をその場で書き換えるので、手元で回したら`git checkout -- docs/`で戻す
- `prime.md`のdenyの一覧と`init.go`の`primeDenyRules`は、gate・secret・egress・privileged-commandにサブコマンドを足したら一緒に直す
- ほかのセッションとはSendMessageで話せる（このリポジトリのセッションは`masuda-d3`、ハーネスは`oncall-pf-template-20`）。相手の依頼でIssueを起票・返事するときは、こちらのユーザーの判断を取ってから
- 上流（Gondolin）への投稿は、ユーザーがWebから個人のアカウントで行う。この端末の`gh`は社用で、`scripts/gh.sh`は自分のリポジトリだけ
- この端末の`~/.gitconfig`は会社用。masuda・masuda-sandbox・masuda-engine・gondolinのcloneには、ローカルで個人用の作者を入れてある
- この端末（WSL2）は時計が前後に約10秒跳ぶ
## 契約への提案
無し（#88の`Activity.resource_warning`はユーザー承認のうえで変更済み）。
