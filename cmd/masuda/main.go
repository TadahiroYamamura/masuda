// Command masuda は`masuda serve`（常駐プロセス）と、それを公開API越しに叩くCLI。
package main

import (
	"fmt"
	"os"
)

// version はリリースビルドで`-ldflags "-X main.version=<tag>"`により埋め込む。
var version = "dev"

const usage = `usage: masuda <command> [flags]

commands:
  serve     公開APIを待ち受ける常駐プロセスを起動する
  version   バージョンを表示する
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
	case "version", "--version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "masuda: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "masuda: %v\n", err)
		os.Exit(1)
	}
}
