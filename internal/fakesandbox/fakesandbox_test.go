package fakesandbox_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
	"github.com/TadahiroYamamura/masuda/internal/fakesandbox"
	"github.com/TadahiroYamamura/masuda/internal/guest"
	"github.com/TadahiroYamamura/masuda/internal/sandboxcontract"
)

func newFake(t *testing.T) (sandboxv1connect.SandboxServiceClient, string) {
	t.Helper()
	dir := t.TempDir()
	p := fakesandbox.StartInProcess(dir)
	t.Cleanup(p.Close)
	ctx := context.Background()
	_, err := p.Client.CreateSandbox(ctx, connect.NewRequest(&sandboxv1.CreateSandboxRequest{
		Id: "sb1", DefaultUser: "ubuntu", Env: map[string]string{"FROM_CREATE": "1"},
		Secrets: []*sandboxv1.SecretDecl{{Name: "TOKEN", Value: "real-secret", PlaceholderPrefix: "sk-", PlaceholderLength: 20}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return p.Client, filepath.Join(dir, "sb1", "root")
}

func readAll(t *testing.T, c sandboxv1connect.SandboxServiceClient, path string) (string, error) {
	t.Helper()
	st, err := c.ReadFile(context.Background(), connect.NewRequest(&sandboxv1.ReadFileRequest{Id: "sb1", Path: path}))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for st.Receive() {
		b.Write(st.Msg().Data)
	}
	return b.String(), st.Err()
}

func TestLifecycleAndPlaceholders(t *testing.T) {
	c, _ := newFake(t)
	ctx := context.Background()
	got, err := c.GetSandbox(ctx, connect.NewRequest(&sandboxv1.GetSandboxRequest{Id: "sb1"}))
	if err != nil || got.Msg.State != sandboxv1.SandboxState_SANDBOX_STATE_RUNNING {
		t.Fatalf("GetSandbox: %v %+v", err, got)
	}
	ph := got.Msg.Placeholders["TOKEN"]
	if !strings.HasPrefix(ph, "sk-") || len(ph) != 20 || strings.Contains(ph, "real-secret") {
		t.Fatalf("placeholder %q", ph)
	}
	if _, err := c.CreateSandbox(ctx, connect.NewRequest(&sandboxv1.CreateSandboxRequest{Id: "sb1", DefaultUser: "ubuntu"})); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("duplicate id: %v", err)
	}
	if _, err := c.SetPolicy(ctx, connect.NewRequest(&sandboxv1.SetPolicyRequest{Id: "sb1", Policy: &sandboxv1.Policy{EnabledSecrets: []string{"NOPE"}}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("undeclared secret enabled: %v", err)
	}
	if _, err := c.SetPolicy(ctx, connect.NewRequest(&sandboxv1.SetPolicyRequest{Id: "sb1", Policy: &sandboxv1.Policy{AllowedHosts: []string{"example.com"}, EnabledSecrets: []string{"TOKEN"}}})); err != nil {
		t.Fatal(err)
	}
	got, _ = c.GetSandbox(ctx, connect.NewRequest(&sandboxv1.GetSandboxRequest{Id: "sb1"}))
	if strings.Join(got.Msg.Policy.AllowedHosts, ",") != "example.com" {
		t.Fatalf("policy not recorded: %+v", got.Msg.Policy)
	}

	// 再送(after_seq=1)の後、Destroyで STOPPED が届いてストリームが終わる。
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, err := c.WatchEvents(wctx, connect.NewRequest(&sandboxv1.WatchEventsRequest{Id: "sb1", AfterSeq: 1}))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		_, _ = c.DestroySandbox(ctx, connect.NewRequest(&sandboxv1.DestroySandboxRequest{Id: "sb1"}))
	}()
	var states []string
	for st.Receive() {
		states = append(states, st.Msg().GetStateChanged().GetState().String())
	}
	if err := st.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(states, ",") != "SANDBOX_STATE_RUNNING,SANDBOX_STATE_STOPPED" {
		t.Fatalf("events: %v", states)
	}
	if _, err := c.DestroySandbox(ctx, connect.NewRequest(&sandboxv1.DestroySandboxRequest{Id: "sb1"})); err != nil {
		t.Fatalf("Destroy must be idempotent: %v", err)
	}
	if _, err := c.GetSandbox(ctx, connect.NewRequest(&sandboxv1.GetSandboxRequest{Id: "sb1"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("Get after Destroy: %v", err)
	}
}

func TestExecMapsCwdAndHome(t *testing.T) {
	c, root := newFake(t)
	ctx := context.Background()
	if err := guest.WriteBytes(ctx, c, "sb1", "/workspace/x.txt", []byte("hello"), 0); err != nil {
		t.Fatal(err)
	}
	res, err := guest.Shell(ctx, c, "sb1", "/workspace", `cat x.txt; echo " $HOME $FROM_CREATE $FROM_EXEC"; echo err >&2`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "hello " + filepath.Join(root, "home/ubuntu") + " 1 \n"; string(res.Stdout) != want {
		t.Fatalf("stdout %q, want %q", res.Stdout, want)
	}
	if string(res.Stderr) != "err\n" {
		t.Fatalf("stderr %q", res.Stderr)
	}
	res, err = guest.Exec(ctx, c, &sandboxv1.ExecRequest{Id: "sb1", Shell: "exit 3"})
	if err != nil || res.ExitCode != 3 {
		t.Fatalf("exit code: %v %+v", err, res)
	}
	res, err = guest.Exec(ctx, c, &sandboxv1.ExecRequest{Id: "sb1", Shell: "sleep 10", TimeoutMs: 100})
	if err != nil || !res.TimedOut {
		t.Fatalf("timeout: %v %+v", err, res)
	}
	if _, err := guest.Exec(ctx, c, &sandboxv1.ExecRequest{Id: "sb1", Argv: []string{"sh", "-c", "true"}}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("relative argv[0]: %v", err)
	}
}

func TestFilesDoNotFollowSymlinks(t *testing.T) {
	c, root := newFake(t)
	ctx := context.Background()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("host"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(root, "masuda/out"), 0o755)
	if err := os.Symlink(outside, filepath.Join(root, "masuda/out/link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(root, "masuda/dirlink")); err != nil {
		t.Fatal(err)
	}
	if _, err := readAll(t, c, "/masuda/out/link"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("read through symlink: %v", err)
	}
	if _, err := readAll(t, c, "/masuda/dirlink/secret"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("read through symlinked dir: %v", err)
	}
	if _, err := readAll(t, c, "/../../secret"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("read with ..: %v", err)
	}
	// 既存のsymlinkは辿らずに置き換える。
	if err := guest.WriteBytes(ctx, c, "sb1", "/masuda/out/link", []byte("guest"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "host" {
		t.Fatalf("write followed the symlink: %q", b)
	}
	if got, err := readAll(t, c, "/masuda/out/link"); err != nil || got != "guest" {
		t.Fatalf("read back: %v %q", err, got)
	}
	if err := guest.WriteBytes(ctx, c, "sb1", "/masuda/dirlink/secret", []byte("x"), 0); err == nil {
		t.Fatal("write through a symlinked directory succeeded")
	}
	big := strings.Repeat("y", 200<<10)
	if err := guest.WriteBytes(ctx, c, "sb1", "/home/ubuntu/big", []byte(big), 0); err != nil {
		t.Fatal(err)
	}
	if got, err := readAll(t, c, "/home/ubuntu/big"); err != nil || got != big {
		t.Fatalf("big file round trip: %v len=%d", err, len(got))
	}
}

// rootでのExecは、ゲストrootをchrootした中でuid 0として動き、絶対パスがゲストのものになる。
func TestExecAsRootSeesGuestPaths(t *testing.T) {
	c, root := newFake(t)
	if err := os.MkdirAll(filepath.Join(root, "workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := guest.Exec(context.Background(), c, &sandboxv1.ExecRequest{
		Id: "sb1", User: "root", Cwd: "/workspace",
		Shell: `id -u > /workspace/uid.txt; pwd; echo "$HOME"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Skipf("unprivileged user namespaces unavailable here: %s", res.Stderr)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "/workspace\n/root" {
		t.Fatalf("stdout %q", got)
	}
	uid, _ := os.ReadFile(filepath.Join(root, "workspace", "uid.txt"))
	if strings.TrimSpace(string(uid)) != "0" {
		t.Fatalf("uid %q", uid)
	}
}

// フェイクの契約はmasudaの生成元と同じなので、serveの互換性の確認を常に通る。
func TestGetServerInfoReportsMasudaContract(t *testing.T) {
	c, _ := newFake(t)
	res, err := c.GetServerInfo(context.Background(), connect.NewRequest(&sandboxv1.GetServerInfoRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.ContractSha256 != sandboxcontract.SHA256 || res.Msg.Contract != "masuda.sandbox.v1" {
		t.Fatalf("info = %v", res.Msg)
	}
}
