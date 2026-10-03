package main

import (
	"errors"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bashが無い")
	}
}

func writeScript(t *testing.T, dir, shell string) string {
	t.Helper()
	p := filepath.Join(dir, "comp."+shell)
	if err := os.WriteFile(p, []byte(completionScript(shell)), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

type fakeMasuda struct {
	bin string
	log string
}

func newFakeMasuda(t *testing.T, dir, body string) fakeMasuda {
	t.Helper()
	f := fakeMasuda{bin: filepath.Join(dir, "masuda"), log: filepath.Join(dir, "calls.log")}
	script := "#!/bin/bash\necho \"$*\" >> " + f.log + "\n" + body
	if err := os.WriteFile(f.bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

const okFake = `case "$1 $2" in
  "list --all") printf 'ID\tBRANCH\nws-1\tmain\nws-2\tdev\n' ;;
  "workflow list") printf 'WORKFLOW\tORIGIN\nworkflows/develop\tbundled\n' ;;
  "image list") printf 'ENTRY\tBUILT\nbase\tyes\n' ;;
esac
`

func complete(t *testing.T, bin, dir string, words ...string) (reply []string, stderr string) {
	t.Helper()
	script := writeScript(t, dir, "bash")
	driver := `source "$1"; bin=$2; cword=$3; shift 3
COMP_WORDS=("$bin" "$@"); COMP_CWORD=$cword
_masuda
printf '%s\n' "${COMPREPLY[@]}"`
	args := append([]string{"-c", driver, "bash", script, bin, strconv.Itoa(len(words))}, words...)
	cmd := exec.Command("bash", args...)
	cmd.Dir = dir
	var errb strings.Builder
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bashの実行に失敗: %v\n%s", err, errb.String())
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			reply = append(reply, l)
		}
	}
	return reply, errb.String()
}

func assertContainsAll(t *testing.T, got, want []string) {
	t.Helper()
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("候補に%qが無い: %v", w, got)
		}
	}
}

func mainSwitchCases(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var cases []string
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "main" {
			return true
		}
		ast.Inspect(fd, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				lit, ok := e.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(s, "-") {
					cases = append(cases, s)
				}
			}
			return true
		})
		return false
	})
	sort.Strings(cases)
	return cases
}

func tableCommands() []string {
	var names []string
	for _, n := range completionTable {
		names = append(names, n.name)
	}
	sort.Strings(names)
	return names
}

func TestCompletionScriptの構文(t *testing.T) {
	requireBash(t)
	for _, shell := range []string{"bash", "zsh"} {
		p := writeScript(t, t.TempDir(), shell)
		if shell == "zsh" {
			b, _ := os.ReadFile(p)
			_, rest, _ := strings.Cut(string(b), "\n")
			if err := os.WriteFile(p, []byte(rest), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if out, err := exec.Command("bash", "-n", p).CombinedOutput(); err != nil {
			t.Errorf("%s向けスクリプトがbash -nを通らない: %v\n%s", shell, err, out)
		}
	}
}

func TestCompletionの候補(t *testing.T) {
	requireBash(t)
	dir := t.TempDir()
	fake := newFakeMasuda(t, dir, okFake)

	t.Run("1語目の候補にmain.goのswitchのすべてのコマンドが入る", func(t *testing.T) {
		got, _ := complete(t, fake.bin, dir, "")
		assertContainsAll(t, got, mainSwitchCases(t))
	})

	t.Run("2語目の候補に各コマンドのサブコマンドが入る", func(t *testing.T) {
		want := map[string][]string{
			"gate":               {"list", "show", "approve", "reject", "comment", "dismiss", "halt", "redo"},
			"question":           {"list", "answer"},
			"egress":             {"list", "approve", "reject"},
			"secret":             {"list", "set", "approve", "reject"},
			"privileged-command": {"list", "approve"},
			"image":              {"list", "build"},
			"workflow":           {"list", "show", "check"},
			"completion":         {"bash", "zsh"},
		}
		for cmd, subs := range want {
			got, _ := complete(t, fake.bin, dir, cmd, "")
			if !slices.Equal(sortedCopy(got), sortedCopy(subs)) {
				t.Errorf("%sの2語目の候補 = %v, want %v", cmd, got, subs)
			}
		}
	})

	t.Run("runのフラグ候補に--branchが入り--と-の両方の形で補完できる", func(t *testing.T) {
		got, _ := complete(t, fake.bin, dir, "run", "--b")
		assertContainsAll(t, got, []string{"--branch", "--base"})
		got, _ = complete(t, fake.bin, dir, "run", "-b")
		assertContainsAll(t, got, []string{"-branch", "-base"})
		got, _ = complete(t, fake.bin, dir, "run", "-")
		assertContainsAll(t, got, []string{"--branch", "-branch"})
	})

	t.Run("gateのサブコマンドのフラグが補完される", func(t *testing.T) {
		got, _ := complete(t, fake.bin, dir, "gate", "approve", "--")
		assertContainsAll(t, got, []string{"--comment", "--hash", "--file", "--socket"})
	})

	t.Run("値がパスのフラグの直後はコマンド候補を出さずディレクトリだけを補完する", func(t *testing.T) {
		if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "afile.txt"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		got, _ := complete(t, fake.bin, dir, "run", "--repo", "")
		if !slices.Equal(got, []string{"subdir"}) {
			t.Errorf("--repoの直後の候補 = %v, want [subdir]", got)
		}
	})

	t.Run("値がファイルのフラグの直後はファイルも補完する", func(t *testing.T) {
		got, _ := complete(t, fake.bin, dir, "run", "--input", "af")
		if !slices.Equal(got, []string{"afile.txt"}) {
			t.Errorf("--inputの直後の候補 = %v, want [afile.txt]", got)
		}
	})

	t.Run("=でつないだパスのフラグの値も補完する", func(t *testing.T) {
		got, _ := complete(t, fake.bin, dir, "run", "--repo", "=", "su")
		if !slices.Equal(got, []string{"subdir"}) {
			t.Errorf("--repo=の候補 = %v, want [subdir]", got)
		}
	})

	t.Run("値を取るが候補の無いフラグの直後は何も出さない", func(t *testing.T) {
		got, _ := complete(t, fake.bin, dir, "run", "--branch", "")
		if len(got) != 0 {
			t.Errorf("--branchの直後の候補 = %v, want 空", got)
		}
	})

	t.Run("位置引数にワークスペースID・ワークフロー名・イメージ名が出る", func(t *testing.T) {
		cases := []struct {
			words []string
			want  []string
		}{
			{[]string{"resume", ""}, []string{"ws-1", "ws-2"}},
			{[]string{"gate", "show", ""}, []string{"ws-1", "ws-2"}},
			{[]string{"remove", "--force", ""}, []string{"ws-1", "ws-2"}},
			{[]string{"question", "answer", "ws"}, []string{"ws-1", "ws-2"}},
			{[]string{"run", ""}, []string{"workflows/develop"}},
			{[]string{"run", "--branch", "x", "--workflow", ""}, []string{"workflows/develop"}},
			{[]string{"workflow", "show", ""}, []string{"workflows/develop"}},
			{[]string{"image", "build", ""}, []string{"base"}},
			{[]string{"run", "--image", ""}, []string{"base"}},
		}
		for _, c := range cases {
			got, _ := complete(t, fake.bin, dir, c.words...)
			if !slices.Equal(sortedCopy(got), c.want) {
				t.Errorf("%v の候補 = %v, want %v", c.words, got, c.want)
			}
		}
	})

	t.Run("2つ目以降の位置引数には動的候補を出さない", func(t *testing.T) {
		got, _ := complete(t, fake.bin, dir, "gate", "show", "ws-1", "")
		if len(got) != 0 {
			t.Errorf("候補 = %v, want 空", got)
		}
	})

	t.Run("--socketが入力済みのとき動的候補の取得に渡される", func(t *testing.T) {
		if err := os.Remove(fake.log); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		complete(t, fake.bin, dir, "resume", "--socket", "/tmp/x.sock", "")
		b, err := os.ReadFile(fake.log)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(b)); got != "list --all --socket /tmp/x.sock" {
			t.Errorf("masudaへの引数 = %q", got)
		}
	})

	t.Run("serveに繋がらず候補の取得が失敗したときは候補が空でstderrに何も出ない", func(t *testing.T) {
		bad := newFakeMasuda(t, t.TempDir(), "echo 'connection refused' >&2\nexit 1\n")
		got, stderr := complete(t, bad.bin, dir, "resume", "")
		if len(got) != 0 || stderr != "" {
			t.Errorf("候補 = %v, stderr = %q, want 両方空", got, stderr)
		}
	})
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	sort.Strings(c)
	return c
}

func TestCompletionのzsh向け出力(t *testing.T) {
	const head = "autoload -U +X bashcompinit && bashcompinit\n"
	zsh, bash := completionScript("zsh"), completionScript("bash")
	if !strings.HasPrefix(zsh, head) {
		t.Errorf("zsh向け出力がbashcompinitの読み込み行で始まらない: %q", zsh[:60])
	}
	if strings.TrimPrefix(zsh, head) != bash {
		t.Error("zsh向けの補完関数がbash向けと違う")
	}
	if strings.HasPrefix(bash, "autoload") {
		t.Error("bash向け出力にbashcompinitの読み込み行がある")
	}
}

func TestRunCompletionの引数(t *testing.T) {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	origErr, origOut := os.Stderr, os.Stdout
	os.Stderr, os.Stdout = devnull, devnull
	defer func() { os.Stderr, os.Stdout = origErr, origOut }()

	for _, args := range [][]string{nil, {"fish"}, {"bash", "zsh"}, {""}} {
		if err := runCompletion(args); !errors.Is(err, errUsage) {
			t.Errorf("runCompletion(%q) = %v, want errUsage", args, err)
		}
	}
	for _, shell := range []string{"bash", "zsh"} {
		if err := runCompletion([]string{shell}); err != nil {
			t.Errorf("runCompletion(%q) = %v", shell, err)
		}
	}
}

func TestCompletionの表がmainのswitchと一致する(t *testing.T) {
	got, want := tableCommands(), mainSwitchCases(t)
	if !slices.Equal(got, want) {
		t.Errorf("表のコマンド = %v, main.goのswitch = %v", got, want)
	}
}

var completionRunners = map[string]func([]string) error{
	"serve":                      runServe,
	"run":                        runRun,
	"resume":                     runResume,
	"list":                       runList,
	"chat":                       runChat,
	"watch":                      runWatch,
	"stop":                       runStop,
	"remove":                     runRemove,
	"init":                       runInit,
	"version":                    runVersion,
	"doctor":                     runDoctor,
	"gate list":                  gateList,
	"gate show":                  gateShow,
	"gate approve":               func(a []string) error { return gateDecide(a, true) },
	"gate reject":                func(a []string) error { return gateDecide(a, false) },
	"gate comment":               gateComment,
	"gate dismiss":               func(a []string) error { return gateTriage("dismiss", a) },
	"gate halt":                  func(a []string) error { return gateTriage("halt", a) },
	"gate redo":                  func(a []string) error { return gateTriage("redo", a) },
	"question list":              questionList,
	"question answer":            questionAnswer,
	"egress list":                egressList,
	"egress approve":             func(a []string) error { return egressDecide(a, true) },
	"egress reject":              func(a []string) error { return egressDecide(a, false) },
	"secret list":                secretList,
	"secret set":                 secretSet,
	"secret approve":             func(a []string) error { return secretDecide(a, true) },
	"secret reject":              func(a []string) error { return secretDecide(a, false) },
	"privileged-command list":    privilegedList,
	"privileged-command approve": privilegedApprove,
	"image list":                 imageList,
	"image build":                imageBuild,
	"workflow list":              workflowList,
	"workflow show":              workflowShow,
	"workflow check":             workflowCheck,
}

var helpFlagLine = regexp.MustCompile(`^  -(\S+)`)

func helpFlags(t *testing.T, run func([]string) error) []string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	err = run([]string{"-h"})
	os.Stderr = orig
	w.Close()
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("-hの結果 = %v", err)
	}
	out, _ := io.ReadAll(r)
	var flags []string
	for _, l := range strings.Split(string(out), "\n") {
		if m := helpFlagLine.FindStringSubmatch(l); m != nil {
			flags = append(flags, m[1])
		}
	}
	sort.Strings(flags)
	return flags
}

func TestCompletionの表のフラグが実物のhelp出力と一致する(t *testing.T) {
	seen := map[string]bool{}
	forEachLeaf(func(key string, n compNode) {
		seen[key] = true
		for _, f := range n.flags {
			if _, ok := compFlagKinds[f]; !ok {
				t.Errorf("%s: フラグ%qの種類がcompFlagKindsに無い", key, f)
			}
		}
		run, ok := completionRunners[key]
		if !ok {
			if len(n.flags) != 0 {
				t.Errorf("%s: -hを呼ぶ対応が無いのにフラグが%vある", key, n.flags)
			}
			return
		}
		if got, want := helpFlags(t, run), sortedCopy(n.flags); !slices.Equal(got, want) {
			t.Errorf("%s: 実物のフラグ = %v, 表 = %v", key, got, want)
		}
	})
	for key := range completionRunners {
		if !seen[key] {
			t.Errorf("completionRunnersの%qが表に無い", key)
		}
	}
}
