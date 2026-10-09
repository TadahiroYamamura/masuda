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
  serve                     start the long-running process that serves the public API
  run                       start a workflow in a new workspace
  resume <id>               resume a stopped or suspended workspace from its records
  list [--all]              list workspaces (--all: also done and stopped ones)
  chat <id>                 attach to the guest's main session (tmux) over ssh
  watch [<id>]              stream status and events
  wait <id>                 wait until the workspace needs a human or ends, print one line and exit
  gate list|show|approve|reject|comment|dismiss|halt|redo
                            list, show and decide gates (dismiss/halt/redo are for triage)
  question list|answer      list and answer questions
  stop <id>                 stop the sandbox (records are kept)
  remove <id>               remove a workspace (exports are kept)
  init                      put the .masuda/ templates and guidance for the host agent into a repository (no serve needed)
  prime [--hook-json]       print how the host agent uses masuda (no serve needed)
  egress list|approve|reject
                            declared and approved egress hosts
  secret list|set|approve|reject
                            list secrets, set a value (read from stdin), approve plaintext secrets
  env import <file>         import a .env file: declared secrets into the secret store, public envFiles values into vars of settings.local.json
  privileged-command list|approve|run
                            list, approve and run privileged commands (run needs no serve; it talks to masuda-sandbox directly)
  image list|build          list and build guest images
  workflow list|show|check  list workflows, draw one (Mermaid), check them
  doc [<page>[#<id>]]       print the documents of this version (--serve: browse them; no serve needed)
  version                   print the versions of masuda and the masuda-sandbox it connects to
  doctor                    check the prerequisites (QEMU, KVM/HVF, Node, Docker, git, sandbox, token)
  completion bash|zsh       print a shell completion script (no serve needed)

Commands other than serve, init, prime, doc, workflow, version, doctor, completion and privileged-command run
talk to the masuda serve given by --socket. Run 'masuda <command> -h' for details.
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
	case "chat":
		err = runChat(os.Args[2:])
	case "workflow":
		err = runWorkflow(os.Args[2:])
	case "watch":
		err = runWatch(os.Args[2:])
	case "wait":
		err = runWait(os.Args[2:])
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
	case "prime":
		err = runPrime(os.Args[2:])
	case "egress":
		err = runEgress(os.Args[2:])
	case "secret":
		err = runSecret(os.Args[2:])
	case "env":
		err = runEnv(os.Args[2:])
	case "privileged-command":
		err = runPrivilegedCommand(os.Args[2:])
	case "image":
		err = runImage(os.Args[2:])
	case "version", "--version", "-v":
		err = runVersion(os.Args[2:])
	case "doctor":
		err = runDoctor(os.Args[2:])
	case "doc":
		err = runDoc(os.Args[2:])
	case "completion":
		err = runCompletion(os.Args[2:])
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "masuda: unknown command %q; run 'masuda -h' for usage\n", os.Args[1])
		os.Exit(2)
	}
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if errors.Is(err, errDoctorFailed) {
		os.Exit(1)
	}
	if errors.Is(err, errUsage) {
		os.Exit(2)
	}
	var code exitCodeError
	if errors.As(err, &code) {
		os.Exit(code.code)
	}
	var unreachable *serveUnreachableError
	if errors.As(err, &unreachable) {
		err = unreachable
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "masuda: %v\n", err)
		os.Exit(1)
	}
}
