package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/guest"
	"github.com/TadahiroYamamura/masuda/internal/secrets"
	"github.com/TadahiroYamamura/masuda/serve"
)

// errDoctorFailed は前提が欠けていたことを表す（詳細は出力済みなので、mainはメッセージを出さずに1で終わる）。
var errDoctorFailed = errors.New("doctor: some prerequisites are missing")

type checkStatus int

const (
	checkOK checkStatus = iota
	checkWarn
	checkFail
)

// checkResult は1項目の結果。fixは足りないときの直し方（OKなら空）。
type checkResult struct {
	name   string
	status checkStatus
	detail string
	fix    string
}

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	sf := addSandboxSocketFlags(fs)
	dataDir := fs.String("data-dir", defaultDataDir(), "masuda serveの状態を置くディレクトリ（Claudeトークンの確認に使う）")
	repo := fs.String("repo", "", "このリポジトリの登録も含めてClaudeトークンを確かめる（無ければユーザー単位だけ）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	socket, _, cfgErr := sf.resolve()
	results := []checkResult{
		checkConfig(*sf.config, cfgErr),
		checkGit(ctx),
		checkDocker(ctx),
		checkNode(ctx),
		checkQEMU(ctx),
		checkAccel(ctx),
		checkSandbox(ctx, socket),
		checkToken(*dataDir, *repo),
	}
	if reportChecks(os.Stdout, results) {
		return errDoctorFailed
	}
	return nil
}

// reportChecks は結果を出し、1つでも欠けていればtrueを返す。警告は欠けに数えない。
func reportChecks(w io.Writer, results []checkResult) (failed bool) {
	label := map[checkStatus]string{checkOK: "ok  ", checkWarn: "warn", checkFail: "NG  "}
	for _, r := range results {
		fmt.Fprintf(w, "[%s] %s: %s\n", label[r.status], r.name, r.detail)
		if r.fix != "" && r.status != checkOK {
			for _, l := range strings.Split(r.fix, "\n") {
				fmt.Fprintf(w, "       %s\n", l)
			}
		}
		failed = failed || r.status == checkFail
	}
	if failed {
		fmt.Fprintln(w, "\n足りないものがある（NGの項目）。直し方は各項目の下と docs/user/install.md")
	} else {
		fmt.Fprintln(w, "\n前提はそろっている")
	}
	return failed
}

// commandOutput はnameを引数付きで動かし、標準出力と標準エラーの1行目を返す。
func commandOutput(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line, err
}

func checkConfig(path string, err error) checkResult {
	r := checkResult{name: "config.json", detail: path}
	if err != nil {
		r.status, r.detail = checkFail, err.Error()
		r.fix = "config.jsonを直すか消す（docs/user/settings.md の config.json の節）"
	} else if _, statErr := os.Stat(path); statErr != nil {
		r.detail = "無し（すべて既定）"
	}
	return r
}

func checkGit(ctx context.Context) checkResult {
	r := checkResult{name: "git"}
	out, err := commandOutput(ctx, "git", "--version")
	if err != nil {
		r.status, r.detail = checkFail, "見つからない"
		r.fix = installHint("git", "git")
		return r
	}
	r.detail = out
	return r
}

func checkDocker(ctx context.Context) checkResult {
	r := checkResult{name: "docker"}
	if _, err := exec.LookPath("docker"); err != nil {
		r.status, r.detail = checkFail, "見つからない（VMのイメージのビルドに使う）"
		r.fix = "Dockerを入れる（Linux: Docker Engine、macOS: Docker Desktop等）"
		return r
	}
	out, err := commandOutput(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	if err != nil {
		r.status, r.detail = checkFail, "dockerデーモンに繋がらない: "+out
		r.fix = "dockerデーモンを起動し、sudo無しで`docker`を叩けるようにする（Linux: `sudo usermod -aG docker \"$USER\"`のあとログインし直す）"
		return r
	}
	r.detail = "server " + out
	return r
}

// nodeMin はGondolinが要るNodeの下限。nodeKnownBad以上ではGondolin #134（外への通信が502）が出る。
var (
	nodeMin      = [3]int{22, 19, 0}
	nodeKnownBad = [3]int{24, 17, 0}
)

func checkNode(ctx context.Context) checkResult {
	r := checkResult{name: "node"}
	out, err := commandOutput(ctx, "node", "--version")
	if err != nil {
		r.status, r.detail = checkFail, "見つからない（masuda-sandboxが動く）"
		r.fix = "Node 22.19以上・24.17未満を入れる（Linux: nodesource等、macOS: `brew install node@22`）"
		return r
	}
	r.status, r.detail, r.fix = nodeStatus(out)
	return r
}

// nodeStatus は`node --version`の出力（"v22.19.0"）を判定する。
func nodeStatus(out string) (checkStatus, string, string) {
	v, ok := parseVersion(out)
	switch {
	case !ok:
		return checkWarn, out + "（版を読めない）", "Node 22.19以上・24.17未満であることを確かめる"
	case compareVersion(v, nodeMin) < 0:
		return checkFail, out + "（22.19以上が要る）", "Node 22.19以上・24.17未満を入れる"
	case compareVersion(v, nodeKnownBad) >= 0:
		return checkWarn, out + "（24.17以上はGondolin #134で外への通信が502になる既知の問題がある）", "Node 22.19以上・24.17未満を使う"
	default:
		return checkOK, out, ""
	}
}

func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func compareVersion(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			return a[i] - b[i]
		}
	}
	return 0
}

func qemuBinary() string {
	if runtime.GOARCH == "arm64" {
		return "qemu-system-aarch64"
	}
	return "qemu-system-x86_64"
}

func checkQEMU(ctx context.Context) checkResult {
	r := checkResult{name: "qemu"}
	bin := qemuBinary()
	out, err := commandOutput(ctx, bin, "--version")
	if err != nil {
		r.status, r.detail = checkFail, bin+"が見つからない"
		r.fix = qemuHint()
		return r
	}
	r.detail = out
	if runtime.GOOS == "linux" {
		var missing []string
		for _, b := range []string{"qemu-img", "lz4"} {
			if _, err := exec.LookPath(b); err != nil {
				missing = append(missing, b)
			}
		}
		if len(missing) > 0 {
			r.status = checkWarn
			r.detail += "（" + strings.Join(missing, "・") + "が見つからない）"
			r.fix = qemuHint()
		}
	}
	return r
}

func qemuHint() string {
	if runtime.GOOS == "darwin" {
		return "`brew install qemu`"
	}
	return "`sudo apt install qemu-system-x86 qemu-utils lz4`（Debian/Ubuntu）"
}

func installHint(what, pkg string) string {
	if runtime.GOOS == "darwin" {
		return fmt.Sprintf("%sを入れる（`brew install %s`）", what, pkg)
	}
	return fmt.Sprintf("%sを入れる（Debian/Ubuntu: `sudo apt install %s`）", what, pkg)
}

// checkAccel はハードウェア仮想化（LinuxはKVM、macOSはHVF）を使えるかを確かめる。
func checkAccel(ctx context.Context) checkResult {
	switch runtime.GOOS {
	case "linux":
		r := checkResult{name: "/dev/kvm"}
		f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
		switch {
		case err == nil:
			f.Close()
			r.detail = "読み書きできる"
		case errors.Is(err, os.ErrNotExist):
			r.status, r.detail = checkFail, "無い"
			r.fix = "BIOSの仮想化支援（Intel VT-x・AMD-V）か、WSL2の入れ子の仮想化を有効にする"
		default:
			r.status, r.detail = checkFail, err.Error()
			r.fix = "`sudo usermod -aG kvm \"$USER\"`のあとログインし直す"
		}
		return r
	case "darwin":
		r := checkResult{name: "HVF"}
		out, err := commandOutput(ctx, "sysctl", "-n", "kern.hv_support")
		if err != nil || out != "1" {
			r.status, r.detail = checkFail, "Hypervisor.frameworkを使えない（kern.hv_support="+out+"）"
			r.fix = "Apple siliconのmacOSで動かす（仮想マシンの中では使えない）"
			return r
		}
		r.detail = "使える（macOSは実験的な対応）"
		return r
	default:
		return checkResult{name: "仮想化", status: checkFail, detail: runtime.GOOS + "には対応していない", fix: "Linux x86_64かmacOS arm64で動かす"}
	}
}

func checkSandbox(ctx context.Context, socket string) checkResult {
	r := checkResult{name: "masuda-sandbox"}
	info, err := serve.SandboxInfo(ctx, serve.DialSandbox(socket))
	if err != nil {
		r.status, r.detail = checkFail, socket+": "+unwrapConnect(err).Error()
		r.fix = fmt.Sprintf("`masuda-sandbox serve --socket %s`を起動する。入っていなければReleaseのmasuda-sandboxのtgzを`npm install -g`する（docs/user/install.md）", socket)
		if _, lerr := exec.LookPath("masuda-sandbox"); lerr != nil {
			r.fix += "\nmasuda-sandboxがPATHに無い"
		}
		return r
	}
	r.detail = fmt.Sprintf("%s（%s、gondolin %s）", info.Version, info.Platform, info.GondolinVersion)
	if err := serve.CheckContract(info, version); err != nil {
		r.status = checkFail
		r.detail += ": " + unwrapConnect(err).Error()
		r.fix = "masudaと同じバージョンのmasuda-sandboxを入れる"
	}
	return r
}

func checkToken(dataDir, repo string) checkResult {
	r := checkResult{name: "Claudeトークン"}
	repoRoot := ""
	if repo != "" {
		top, err := commandOutput(context.Background(), "git", "-C", repo, "rev-parse", "--show-toplevel")
		if err != nil {
			r.status, r.detail = checkFail, repo+"はgitリポジトリではない"
			return r
		}
		repoRoot = top
	}
	_, ok, err := secrets.New(dataDir).ClaudeToken(repoRoot, guest.TokenEnv)
	switch {
	case err != nil:
		r.status, r.detail = checkFail, err.Error()
	case !ok:
		r.status, r.detail = checkFail, "登録されていない"
		r.fix = "`claude setup-token`で作ったトークンを、masuda serveを起動してから`masuda secret set " + guest.TokenEnv + "`で登録する"
	default:
		r.detail = "登録済み"
	}
	return r
}
