# HANDOFF
## 作業項目
M8（実機で1周）。実際のsandbox service（QEMU VM）・本物のClaude Code（サブスクリプション）で、同梱`develop`を2回通した。どちらもplan gate→review gate→publishまで到達。
- 段階1（scratchpadの使い捨てPythonリポジトリ`repo1`、`feat/m8`）: 本走は19:36:43→19:55:05の約18分。ゲートはplan 1回・review 1回（deviationなし）。着地: `baabc38 add triangle shape module`・`2360202 add triangle tests`（review gateで承認したコミットと一致、テスト通過）。exportsに`execution-log.jsonl`・`report`。VMは破棄済み。ここに至るまでに4回やり直した（下の不具合）
- 段階2（このリポジトリ、base `redesign`→新ブランチ`m8/version-go`）: 20:07:10→20:20:01の約13分。ゲートはplan 1回・review 1回。着地: `1c9685c feat(cli): masuda versionにビルドしたGoのバージョンを併記する`（`dev (go1.26.3)`、`go test ./cmd/masuda/`通過）。`redesign`には混ぜず、ブランチのまま残した
- 直したmasudaの不具合
  - `015f892` tmuxをargvの相対名で起動していた（sandboxはargv[0]の絶対パスを要求）。DockerfileのENVがゲストに引き継がれず`claude`が見つからない→tmuxをshell（`/bin/sh -lc`）で起こす。`~/.claude.json`に`/workspace`の信頼と`bypassPermissionsModeAccepted`を足す
  - `85c6d9d` `masuda run`がワークフローを位置引数で受け付けなかった（設計の例どおりに打てない）
  - `d2f83c5` サブエージェント定義の`tools`にmasudaのMCPツールが無く、`write_output`/`report_result`をメインセッションが代筆していた→`guest.SubagentMCPTools`（next_task以外）を足す
  - `2e0c4a8` 内蔵観点`missing-tests-new-code`・`missing-tests-guard-clauses`がステップ1の途中レビューで選ばれ、「テストが後のステップ」の計画で直せない指摘になって止まった→`trigger`を外し最終レビューだけで使う
  - `e35892d` execノードで`go: not found`（Gondolinの既定PATHに/usr/local/binが無い）と`/tmp/.cache`へ書けない（sandboxの既定`XDG_CACHE_HOME=/tmp/.cache`がroot所有）→`guest.BaseEnv`（PATH・XDG_*_HOME）をguestEnvに入れ、チェックのスクリプトを`#!/bin/sh -el`に
## 完了した契約テスト
C-M1〜C-M7は緑のまま（`go test -count=1 ./contract/`）。`go build ./...`・`go vet ./...`・`go test ./...`も通る。契約ファイル・契約テストのassertionは変えていない
## 未完と理由
- `MASUDA_LIVE_TEST=1 go test ./live/`は作っていない（`live/`は無い）。今回はCLIで手動に回した。自動化するなら、下の「注意点」の環境（GOCACHE等）をテスト側で用意する必要がある
- 特権コマンドは実機で通していない（M7の注意点のまま）
- masuda側で気づいたが直していないもの
  - 活動の判定: メインセッションがターンを終えて人間に問いかけたまま4分以上止まっていても`working(idle)`のままで、`input_wait`にならなかった。バックグラウンドのサブエージェントが動いている間も`working(idle)`と出る。原因は未調査
  - 起動に失敗してBLOCKEDになったワークスペースは`resume`できず、`stop`してから`resume`する必要がある（契約は「stoppedを再開」なので仕様どおりだが使いにくい）
  - `masuda init`は`.gitignore`に`.masuda/`があっても`.masuda/settings.local.json`を足す
  - `docs/design/`への反映（ゲストのPATH・XDG、チェックのログインシェル、サブエージェントへのMCPツール付与）はしていない
## 次の一手
1. 下の「契約への提案」の判断（sandboxのディスク容量・XDG既定、engineのfixerの終わり方・trigger-matcherの観点の置き場所）
2. `live/`の自動テスト化（段階2の手順をそのままAPIで）
3. 実機で特権コマンドを1回通す
## 注意点
- 実機で動かした手順: `node dist/cli.js serve --socket $XDG_RUNTIME_DIR/masuda-sandbox.sock`（masuda-sandbox）→`masuda serve --sandbox-socket ...`→対象リポジトリで`masuda init`・Dockerfile編集・`egress approve api.anthropic.com`・`image build default`→`masuda run workflows/develop --branch ... --input instructions=@task.md`。ゲストを見るのはAPIの`AttachInfo`（`curl --unix-socket $XDG_RUNTIME_DIR/masuda.sock -H 'Content-Type: application/json' -d '{"id":"<id>"}' http://localhost/masuda.api.v1.WorkspaceService/AttachInfo`）で得たsshに`tmux capture-pane -p -t claude-work`。`masuda attach`というCLIは無い
- masuda自身を対象にするときのイメージ（使ったものは`scratchpad/m8/dotmasuda-m8/`に退避。リポジトリの`.masuda/`は元からある旧設計のもので、実行後に戻した）
  - go.modの`replace ../masuda-engine`のため、engineのソースをビルドコンテキストへ写して`/masuda-engine`にCOPYする（`/workspace/../masuda-engine`）
  - モジュールはイメージで`go mod download -modcacherw all`しておく（実行中のegressはAPIだけ）。`-modcacherw`が無いとGondolinのビルドが失敗する（下の提案）
  - VMのルートFSの空きは約200MBしかない。`checks.test`を`GOCACHE=/tmp/go-cache go test ./...`、`claudeSettings.env.GOCACHE=/tmp/go-cache`にして、ビルドキャッシュをtmpfs（2GB）へ逃がした
- engineの読み取り専用エージェントが作業ツリーを変えると（追跡済みの`__pycache__/*.pyc`をテストで書き換えた等）deviationゲートが開き、拒否すると実行全体がBLOCKEDになる。対象リポジトリ側でバイト列のキャッシュを追跡しないことが前提
- ゲストのclaudeは`NODE_EXTRA_CA_CERTS=/etc/gondolin/mitm/ca.crt`を読めないと警告するが、システムのCAバンドルで通信はできている
- 残したもの: ブランチ`m8/version-go`（このリポジトリ）。`~/.local/share/masuda/workspaces/`に完了2件（`b7bd968ccd00`・`6020d80e8259`）と、removeしたワークスペースのexports。Gondolinイメージ2つ（repo1用`411234b3-...`、masuda用`e9d1cfc3-...`）とそのdockerタグ。scratchpad `m8/`に`repo1`・ログ・やり直し前のrecordsの写し。プロセス・VMは残していない（QEMUは親の12244のみ。`118151`のフェイクserveは以前からあったもので触っていない）
## 契約への提案
- **sandbox: ディスク容量を指定できない**。Gondolinはrootfsを「中身+20%+64MiB」で作り、`CreateSandboxRequest`にも`BuildImageRequest`にも容量の項目が無い。Go入りのイメージで`/`は1.1G中空き200M、`go test ./...`のビルドキャッシュで`No space left on device`になり、スナップショット（git）も失敗してBLOCKEDになった。再現: Goツールチェーン入りのイメージで`checks.test: go test ./...`のexecノードを1つ持つワークフローを回す。`disk_mib`（または`free_disk_mib`）を契約に足す提案
- **sandbox: 読み取り専用ディレクトリを含むイメージのビルドが失敗する**。再現: Dockerfileで非rootの`go mod download`（モジュールキャッシュは0555）→`masuda image build`→`Build failed: EACCES, Permission denied: /tmp/gondolin-build-XXXX`。一時ディレクトリが残り、`chmod -R u+w`しないと消せない
- **sandbox: Execの既定環境**。`XDG_CACHE_HOME=/tmp/.cache`等を渡し、そのディレクトリをroot所有で作るため既定ユーザー（ubuntu）が書けない。PATHは`/usr/sbin:/usr/bin:/sbin:/bin`で/usr/local/binを含まず、DockerfileのENVも引き継がない。masudaは`Exec.env`で上書きして回避した（`guest.BaseEnv`）。sandbox側で直すか、契約に「既定環境はこれ」と書くかの判断を
- **engine: fixerに「直せない」終わり方が無い**。`agents/fixer`のoutcomeは`done`だけで、指摘のファイル以外を変えられない制約と合わさると、正直に報告する手段が無い（`cannot_fix`はundeclared outcomeで拒否）。メインセッションは人間への問いかけでターンを終え、実行が止まる。`cannot_fix`→`end:unresolved`のような出口の提案
- **engine/masudaの境界: 途中レビューの観点の置き場所**。`agents/trigger-matcher`はゲストの`/workspace/.masuda/reviews/*.md`を読むが、最終レビューはホスト側（内蔵観点+対象リポジトリの`.masuda/reviews`）から渡す。`.masuda/reviews`をコミットしていないリポジトリ（段階2）では途中レビューの観点が0件になる。masudaが観点の一覧をゲストの別の場所（例 `/masuda/reviews/`）へ置き、trigger-matcherがそこを読む形にする提案
