package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	dataDir := fs.String("data-dir", defaultDataDir(), "masuda serve's data directory (used to check the Claude token)")
	repo := fs.String("repo", "", "also check the Claude token registered for this repository (otherwise only the per-user token)")
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
		checkLog("masuda serve log", filepath.Join(*dataDir, "logs", serveLogName)),
		checkLog("masuda-sandbox serve log", sandboxLogPath()),
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
		fmt.Fprintln(w, "\nsome prerequisites are missing (the NG items); see the fix under each item and "+docRef("user/install", ""))
	} else {
		fmt.Fprintln(w, "\nall prerequisites are in place")
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
		r.fix = "fix or remove config.json (see " + docRef("user/settings", "serve-config") + ")"
	} else if _, statErr := os.Stat(path); statErr != nil {
		r.detail = "none (all defaults)"
	}
	return r
}

func checkGit(ctx context.Context) checkResult {
	r := checkResult{name: "git"}
	out, err := commandOutput(ctx, "git", "--version")
	if err != nil {
		r.status, r.detail = checkFail, "not found"
		r.fix = installHint("git", "git")
		return r
	}
	r.detail = out
	return r
}

func checkDocker(ctx context.Context) checkResult {
	r := checkResult{name: "docker"}
	if _, err := exec.LookPath("docker"); err != nil {
		r.status, r.detail = checkFail, "not found (needed to build VM images)"
		r.fix = "install Docker (Linux: Docker Engine; macOS: Docker Desktop or similar)"
		return r
	}
	out, err := commandOutput(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	if err != nil {
		r.status, r.detail = checkFail, "cannot reach the docker daemon: "+out
		r.fix = "start the docker daemon and make `docker` usable without sudo (Linux: `sudo usermod -aG docker \"$USER\"`, then log in again)"
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
		r.status, r.detail = checkFail, "not found (masuda-sandbox runs on it)"
		r.fix = "install Node >= 22.19 and < 24.17 (Linux: nodesource or similar; macOS: `brew install node@22`)"
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
		return checkWarn, out + " (cannot read the version)", "make sure Node is >= 22.19 and < 24.17"
	case compareVersion(v, nodeMin) < 0:
		return checkFail, out + " (22.19 or later is required)", "install Node >= 22.19 and < 24.17"
	case compareVersion(v, nodeKnownBad) >= 0:
		return checkWarn, out + " (24.17 and later have a known issue, Gondolin #134, where outbound requests fail with 502)", "use Node >= 22.19 and < 24.17"
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
		r.status, r.detail = checkFail, bin+" not found"
		r.fix = qemuHint()
		return r
	}
	r.detail = out
	// Gondolinは起動のたびにqemu-imgでqcow2のオーバーレイを作る（lz4は自前でinitramfsを組むときだけで、
	// 配布済みのアセットを使うmasudaの経路では要らない）。
	if _, err := exec.LookPath("qemu-img"); err != nil {
		r.status = checkFail
		r.detail += " (qemu-img not found)"
		r.fix = qemuHint()
	}
	return r
}

func qemuHint() string {
	if runtime.GOOS == "darwin" {
		return "`brew install qemu`"
	}
	return "`sudo apt install qemu-system-x86 qemu-utils` (Debian/Ubuntu)"
}

func installHint(what, pkg string) string {
	if runtime.GOOS == "darwin" {
		return fmt.Sprintf("install %s (`brew install %s`)", what, pkg)
	}
	return fmt.Sprintf("install %s (Debian/Ubuntu: `sudo apt install %s`)", what, pkg)
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
			r.detail = "readable and writable"
		case errors.Is(err, os.ErrNotExist):
			r.status, r.detail = checkFail, "missing"
			r.fix = "enable hardware virtualization in the BIOS (Intel VT-x / AMD-V), or nested virtualization on WSL2"
		default:
			r.status, r.detail = checkFail, err.Error()
			r.fix = "run `sudo usermod -aG kvm \"$USER\"`, then log in again"
		}
		return r
	case "darwin":
		r := checkResult{name: "HVF"}
		out, err := commandOutput(ctx, "sysctl", "-n", "kern.hv_support")
		if err != nil || out != "1" {
			r.status, r.detail = checkFail, "Hypervisor.framework is not available (kern.hv_support="+out+")"
			r.fix = "run on macOS on Apple silicon (not inside a virtual machine)"
			return r
		}
		r.detail = "available (macOS support is experimental)"
		return r
	default:
		return checkResult{name: "virtualization", status: checkFail, detail: runtime.GOOS + " is not supported", fix: "run on Linux x86_64 or macOS arm64"}
	}
}

func checkSandbox(ctx context.Context, socket string) checkResult {
	r := checkResult{name: "masuda-sandbox"}
	info, err := serve.SandboxInfo(ctx, serve.DialSandbox(socket))
	if err != nil {
		r.status, r.detail = checkFail, socket+": "+unwrapConnect(err).Error()
		r.fix = fmt.Sprintf("start `masuda-sandbox serve --socket %s`; if it is not installed, `npm install -g` the masuda-sandbox tgz from the release (%s)", socket, docRef("user/install", ""))
		if _, lerr := exec.LookPath("masuda-sandbox"); lerr != nil {
			r.fix += "\nmasuda-sandbox is not on PATH"
		}
		return r
	}
	r.detail = fmt.Sprintf("%s (%s, gondolin %s)", info.Version, info.Platform, info.GondolinVersion)
	if err := serve.CheckContract(info, version); err != nil {
		r.status = checkFail
		r.detail += ": " + unwrapConnect(err).Error()
		r.fix = "install the masuda-sandbox release with the same version as masuda"
	}
	return r
}

func checkToken(dataDir, repo string) checkResult {
	r := checkResult{name: "Claude token"}
	repoRoot := ""
	if repo != "" {
		top, err := commandOutput(context.Background(), "git", "-C", repo, "rev-parse", "--show-toplevel")
		if err != nil {
			r.status, r.detail = checkFail, repo+" is not a git repository"
			return r
		}
		repoRoot = top
	}
	_, ok, err := secrets.New(dataDir).ClaudeToken(repoRoot, guest.TokenEnv)
	switch {
	case err != nil:
		r.status, r.detail = checkFail, err.Error()
	case !ok:
		r.status, r.detail = checkFail, "not registered"
		r.fix = "create a token with `claude setup-token`, start masuda serve, then register it with `masuda secret set " + guest.TokenEnv + "`"
	default:
		r.detail = "registered"
	}
	return r
}

// checkLog はログの既定の置き場所と最後に書かれた時刻を出す。前提の確認ではないので、いつもok。
// serveを--log-fileで別の場所に向けたときは、ここに出る場所ではない。
func checkLog(name, path string) checkResult {
	r := checkResult{name: name, status: checkOK, detail: path}
	if st, err := os.Stat(path); err == nil {
		r.detail += " (last written " + st.ModTime().Format("2006-01-02 15:04") + ")"
	} else {
		r.detail += " (not created yet)"
	}
	return r
}

// sandboxLogPath はmasuda-sandbox serveのログの既定の置き場所。sandboxのデータディレクトリ
// （src/datafile.ts）の決め方と揃える。
func sandboxLogPath() string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "(home directory unknown)"
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "masuda-sandbox", "logs", "masuda-sandbox-serve.log")
}
