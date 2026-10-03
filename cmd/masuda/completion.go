package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

type argSource string

const (
	argNone      argSource = ""
	argWorkspace argSource = "workspace"
	argWorkflow  argSource = "workflow"
	argImage     argSource = "image"
)

type compNode struct {
	name  string
	flags []string
	arg   argSource
	subs  []compNode
}

// compFlagKinds はフラグの値の補完のしかた。全コマンドで同名のフラグは同じ種類の値を取る。
// "bool"は値を取らない。"none"は値を取るが候補を出さない。
var compFlagKinds = map[string]string{
	"socket":         "file",
	"sandbox-socket": "file",
	"config":         "file",
	"input":          "file",
	"file":           "file",
	"repo":           "dir",
	"data-dir":       "dir",
	"workflow":       "workflow",
	"image":          "image",
	"branch":         "none",
	"base":           "none",
	"comment":        "none",
	"hash":           "none",
	"after":          "none",
	"stall-after":    "none",
	"all":            "bool",
	"force":          "bool",
	"fake-sandbox":   "bool",
}

func leaf(name string, arg argSource, flags ...string) compNode {
	return compNode{name: name, flags: flags, arg: arg}
}

func clientLeaf(name string, arg argSource, flags ...string) compNode {
	return leaf(name, arg, append([]string{"socket"}, flags...)...)
}

func repoSubs(names ...string) []compNode {
	var subs []compNode
	for _, n := range names {
		subs = append(subs, clientLeaf(n, argNone, "repo"))
	}
	return subs
}

var completionTable = []compNode{
	leaf("serve", argNone, "socket", "data-dir", "fake-sandbox", "sandbox-socket", "stall-after", "config"),
	clientLeaf("run", argWorkflow, "repo", "workflow", "branch", "base", "image", "input"),
	clientLeaf("resume", argWorkspace),
	clientLeaf("list", argNone, "repo", "all"),
	clientLeaf("chat", argWorkspace),
	clientLeaf("watch", argWorkspace, "after"),
	{name: "gate", subs: []compNode{
		clientLeaf("list", argWorkspace),
		clientLeaf("show", argWorkspace),
		clientLeaf("approve", argWorkspace, "comment", "hash", "file"),
		clientLeaf("reject", argWorkspace, "comment"),
		clientLeaf("comment", argWorkspace),
		clientLeaf("dismiss", argWorkspace, "comment"),
		clientLeaf("halt", argWorkspace, "comment"),
		clientLeaf("redo", argWorkspace, "comment"),
	}},
	{name: "question", subs: []compNode{
		clientLeaf("list", argWorkspace),
		clientLeaf("answer", argWorkspace),
	}},
	clientLeaf("stop", argWorkspace),
	clientLeaf("remove", argWorkspace, "force"),
	clientLeaf("init", argNone, "repo"),
	{name: "egress", subs: repoSubs("list", "approve", "reject")},
	{name: "secret", subs: repoSubs("list", "set", "approve", "reject")},
	{name: "privileged-command", subs: repoSubs("list", "approve")},
	{name: "image", subs: []compNode{
		clientLeaf("list", argNone, "repo"),
		clientLeaf("build", argImage, "repo"),
	}},
	{name: "workflow", subs: []compNode{
		clientLeaf("list", argNone, "repo"),
		clientLeaf("show", argWorkflow, "repo"),
		clientLeaf("check", argWorkflow, "repo"),
	}},
	leaf("version", argNone, "sandbox-socket", "config"),
	leaf("doctor", argNone, "sandbox-socket", "config", "data-dir", "repo"),
	leaf("help", argNone),
	{name: "completion", subs: []compNode{
		leaf("bash", argNone),
		leaf("zsh", argNone),
	}},
}

func runCompletion(args []string) error {
	if len(args) != 1 || (args[0] != "bash" && args[0] != "zsh") {
		fmt.Fprintln(os.Stderr, "usage: masuda completion bash|zsh")
		return errUsage
	}
	_, err := fmt.Fprint(os.Stdout, completionScript(args[0]))
	return err
}

func completionScript(shell string) string {
	var b strings.Builder
	if shell == "zsh" {
		b.WriteString("autoload -U +X bashcompinit && bashcompinit\n")
	}
	b.WriteString(completionPrologue)

	b.WriteString("_masuda_subs() {\n  case $1 in\n")
	var top []string
	for _, n := range completionTable {
		top = append(top, n.name)
		if len(n.subs) > 0 {
			fmt.Fprintf(&b, "    %s) echo %q ;;\n", n.name, strings.Join(subNames(n), " "))
		}
	}
	fmt.Fprintf(&b, "    \"\") echo %q ;;\n  esac\n}\n\n", strings.Join(top, " "))

	b.WriteString("_masuda_flags() {\n  case $1 in\n")
	forEachLeaf(func(key string, n compNode) {
		var words []string
		for _, f := range n.flags {
			words = append(words, "--"+f, "-"+f)
		}
		fmt.Fprintf(&b, "    %q) echo %q ;;\n", key, strings.Join(words, " "))
	})
	b.WriteString("  esac\n}\n\n")

	b.WriteString("_masuda_arg() {\n  case $1 in\n")
	forEachLeaf(func(key string, n compNode) {
		if n.arg != argNone {
			fmt.Fprintf(&b, "    %q) echo %s ;;\n", key, n.arg)
		}
	})
	b.WriteString("  esac\n}\n\n")

	b.WriteString("_masuda_flagkind() {\n  case $1 in\n")
	var names []string
	for name, kind := range compFlagKinds {
		if kind != "bool" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&b, "    %s) echo %s ;;\n", name, compFlagKinds[name])
	}
	b.WriteString("  esac\n}\n\n")

	b.WriteString(completionBody)
	return b.String()
}

func subNames(n compNode) []string {
	var names []string
	for _, s := range n.subs {
		names = append(names, s.name)
	}
	return names
}

func forEachLeaf(f func(key string, n compNode)) {
	for _, n := range completionTable {
		if len(n.subs) == 0 {
			f(n.name, n)
			continue
		}
		for _, s := range n.subs {
			f(n.name+" "+s.name, s)
		}
	}
}

// completionPrologue の _masuda_dyn は、候補の取得先を決める --socket を「--socket <path>」の分離形でしか拾わない。
// bash は COMP_WORDBREAKS の `=` で「--socket=/x」を3語に割るため、`=` 形まで追うと語の組み立てが要り、
// 取りこぼしても既定のソケットで候補が出るだけなので割り切っている。
const completionPrologue = `# masuda completion
_masuda_dyn() {
  local bin=${COMP_WORDS[0]} i
  local -a sock=()
  for ((i = 1; i < COMP_CWORD; i++)); do
    case ${COMP_WORDS[i]} in
      --socket|-socket) sock=(--socket "${COMP_WORDS[i+1]}") ;;
    esac
  done
  {
    case $1 in
      workspace) "$bin" list --all "${sock[@]}" ;;
      workflow) "$bin" workflow list "${sock[@]}" ;;
      image) "$bin" image list "${sock[@]}" ;;
    esac
  } 2>/dev/null | awk 'NR > 1 { print $1 }'
}

_masuda_dynreply() {
  local line
  while IFS= read -r line; do
    [[ $line == "$cur"* ]] && COMPREPLY+=("$line")
  done <<< "$(_masuda_dyn "$1")"
}

_masuda_path() {
  local IFS=$'\n'
  COMPREPLY=($(compgen -"$1" -- "$cur"))
  compopt -o filenames 2>/dev/null
}

`

const completionBody = `_masuda() {
  local cur=${COMP_WORDS[COMP_CWORD]} flag w i
  local -a pos=()
  COMPREPLY=()
  if [[ $cur == = ]]; then
    cur=
    flag=${COMP_WORDS[COMP_CWORD-1]}
  elif [[ ${COMP_WORDS[COMP_CWORD-1]} == = ]]; then
    flag=${COMP_WORDS[COMP_CWORD-2]}
  else
    flag=${COMP_WORDS[COMP_CWORD-1]}
  fi

  if [[ $flag == -* ]]; then
    flag=${flag#-}
    flag=${flag#-}
    case $(_masuda_flagkind "$flag") in
      file) _masuda_path f; return 0 ;;
      dir) _masuda_path d; return 0 ;;
      workflow|image) _masuda_dynreply "$(_masuda_flagkind "$flag")"; return 0 ;;
      none) return 0 ;;
    esac
  fi

  for ((i = 1; i < COMP_CWORD; i++)); do
    w=${COMP_WORDS[i]}
    if [[ $w == -* ]]; then
      w=${w#-}
      w=${w#-}
      if [[ -n $(_masuda_flagkind "$w") ]]; then
        [[ ${COMP_WORDS[i+1]} == = ]] && ((i++))
        ((i++))
      fi
    elif [[ $w != = ]]; then
      pos+=("$w")
    fi
  done

  local cmd=${pos[0]} sub= n subs
  if (( ${#pos[@]} == 0 )); then
    [[ $cur == -* ]] || COMPREPLY=($(compgen -W "$(_masuda_subs "")" -- "$cur"))
    return 0
  fi
  subs=$(_masuda_subs "$cmd")
  if [[ -n $subs ]]; then
    if (( ${#pos[@]} == 1 )); then
      [[ $cur == -* ]] || COMPREPLY=($(compgen -W "$subs" -- "$cur"))
      return 0
    fi
    sub=${pos[1]}
    n=$(( ${#pos[@]} - 2 ))
  else
    n=$(( ${#pos[@]} - 1 ))
  fi
  local key=$cmd${sub:+ $sub}

  if [[ $cur == -* ]]; then
    COMPREPLY=($(compgen -W "$(_masuda_flags "$key")" -- "$cur"))
    return 0
  fi
  (( n == 0 )) || return 0
  local src=$(_masuda_arg "$key")
  [[ -n $src ]] && _masuda_dynreply "$src"
  return 0
}

complete -F _masuda masuda
`
