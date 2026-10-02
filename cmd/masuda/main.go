// Command masuda は`masuda serve`（常駐プロセス）と、それを公開API越しに叩くCLI。
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

// version はリリースビルドで`-ldflags "-X main.version=<tag>"`により埋め込む。
var version = "dev"

const usage = `usage: masuda <command> [flags]

commands:
  serve                     公開APIを待ち受ける常駐プロセスを起動する
  run                       ワークフローを新しいワークスペースで始める
  resume <id>               止めたワークスペースを記録から再開する
  list                      ワークスペースの一覧
  watch [<id>]              状態とイベントを流し続ける
  gate list|show|approve|reject
                            ゲートの一覧・内容・判断
  question list|answer      質問の一覧・回答
  stop <id>                 sandboxを止める（記録は残す）
  remove <id>               ワークスペースを消す（exportsは残す）
  init                      対象リポジトリに.masuda/の雛形を置く（serve不要）
  egress list|approve|reject
                            egressの宣言と承認
  secret list|set           秘密の一覧・値の登録（値は標準入力から）
  privileged-command list|approve
                            特権コマンドの一覧・承認
  image list|build          ゲストイメージの一覧・ビルド
  version                   バージョンを表示する

serve・init以外は--socketで指定したmasuda serveを叩く。各コマンドの詳細は -h で出る。
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "run":
		err = runRun(os.Args[2:])
	case "resume":
		err = runResume(os.Args[2:])
	case "list":
		err = runList(os.Args[2:])
	case "watch":
		err = runWatch(os.Args[2:])
	case "gate":
		err = runGate(os.Args[2:])
	case "question":
		err = runQuestion(os.Args[2:])
	case "stop":
		err = runStop(os.Args[2:])
	case "remove":
		err = runRemove(os.Args[2:])
	case "init":
		err = runInit(os.Args[2:])
	case "egress":
		err = runEgress(os.Args[2:])
	case "secret":
		err = runSecret(os.Args[2:])
	case "privileged-command":
		err = runPrivilegedCommand(os.Args[2:])
	case "image":
		err = runImage(os.Args[2:])
	case "version", "--version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "masuda: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if errors.Is(err, errUsage) {
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "masuda: %v\n", err)
		os.Exit(1)
	}
}
