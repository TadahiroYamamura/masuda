package fakesandbox_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
)

type jobRun struct {
	phases   []string
	id       string
	log      string // stdoutとstderrを届いた順に（stderrは"E:"を付ける）
	finished *sandboxv1.RunJobEvent_Finished
}

func runJob(t *testing.T, c sandboxv1connect.SandboxServiceClient, req *sandboxv1.RunJobRequest) (*jobRun, error) {
	t.Helper()
	st, err := c.RunJob(context.Background(), connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	defer st.Close()
	out := &jobRun{}
	for st.Receive() {
		switch ev := st.Msg().Event.(type) {
		case *sandboxv1.RunJobEvent_Phase_:
			out.phases = append(out.phases, ev.Phase.Name)
			out.id = ev.Phase.SandboxId
		case *sandboxv1.RunJobEvent_Stdout:
			out.log += string(ev.Stdout)
		case *sandboxv1.RunJobEvent_Stderr:
			out.log += "E:" + string(ev.Stderr)
		case *sandboxv1.RunJobEvent_Finished_:
			out.finished = ev.Finished
		}
	}
	return out, st.Err()
}

// jobRoot はsb1のゲストrootの場所から、ジョブのゲストrootの場所を出す（どちらも`<dir>/<id>/root`）。
func jobRoot(sb1Root, id string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(sb1Root)), id, "root")
}

func writeFile(t *testing.T, p, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func skipWithoutRoot(t *testing.T, r *jobRun) {
	t.Helper()
	if r.finished != nil && r.finished.Setup != nil && r.finished.Setup.ExitCode != 0 && strings.Contains(r.log, "unshare") {
		t.Skipf("unprivileged user namespaces unavailable here: %s", r.log)
	}
}

func TestRunJob(t *testing.T) {
	c, sb1 := newFake(t)
	ws := filepath.Join(sb1, "workspace")
	writeFile(t, filepath.Join(ws, "build/app.bin"), "bin", 0o755)
	writeFile(t, filepath.Join(ws, "build/sub/data.txt"), "data", 0o640)
	// umaskで削られる許可ビットも保たれること。
	writeFile(t, filepath.Join(ws, "build/shared.txt"), "shared", 0o666)
	writeFile(t, filepath.Join(ws, "build/.git/HEAD"), "ref", 0o644)
	writeFile(t, filepath.Join(ws, "src/main.go"), "package main", 0o644)
	if err := os.Symlink("/etc/passwd", filepath.Join(ws, "build/link")); err != nil {
		t.Fatal(err)
	}
	host := filepath.Join(t.TempDir(), "seed.txt")
	writeFile(t, host, "seed", 0o600)
	outDir := filepath.Join(t.TempDir(), "outputs")

	r, err := runJob(t, c, &sandboxv1.RunJobRequest{
		Inputs: []*sandboxv1.RunJobRequest_Input{
			{Source: &sandboxv1.RunJobRequest_Input_HostFile{HostFile: &sandboxv1.RunJobRequest_HostFile{HostPath: host, GuestPath: "/masuda/seed.txt", Mode: 0o600}}},
			{Source: &sandboxv1.RunJobRequest_Input_FromSandbox{FromSandbox: &sandboxv1.RunJobRequest_FromSandbox{Id: "sb1", Root: "/workspace", Patterns: []string{"build/**"}, DestRoot: "/workspace"}}},
		},
		SetupShell:     `cat /masuda/seed.txt > /workspace/setup.txt; echo setup-out`,
		Shell:          `id -u > uid.txt; cat setup.txt; echo err >&2; mkdir -p report; echo r > report/a.xml; exit 3`,
		Outputs:        []string{"uid.txt", "report/**", "missing/*.xml"},
		OutputsHostDir: outDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	skipWithoutRoot(t, r)
	root := jobRoot(sb1, r.id)

	t.Run("段階はcreating・inputs・setup・running・outputs・destroyingの順に届く", func(t *testing.T) {
		want := []string{"creating", "inputs", "setup", "running", "outputs", "destroying"}
		if !slices.Equal(r.phases, want) || !strings.HasPrefix(r.id, "job-") {
			t.Fatalf("phases %v id %q", r.phases, r.id)
		}
	})
	t.Run("HostFileはゲストのパスへ指定の許可ビットで置かれる", func(t *testing.T) {
		fi, err := os.Stat(filepath.Join(root, "masuda/seed.txt"))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("seed: %v %v", fi, err)
		}
	})
	t.Run("FromSandboxは当たる通常ファイルだけを許可ビットを保って写し、.gitとシンボリックリンクは写さない", func(t *testing.T) {
		for rel, mode := range map[string]os.FileMode{"build/app.bin": 0o755, "build/sub/data.txt": 0o640, "build/shared.txt": 0o666} {
			fi, err := os.Stat(filepath.Join(root, "workspace", rel))
			if err != nil || fi.Mode().Perm() != mode {
				t.Errorf("%s: %v %v", rel, fi, err)
			}
		}
		for _, rel := range []string{"build/.git/HEAD", "build/link", "src/main.go"} {
			if _, err := os.Lstat(filepath.Join(root, "workspace", rel)); err == nil {
				t.Errorf("%s must not be copied", rel)
			}
		}
	})
	t.Run("setup_shellの後にshellが動き、両方の出力が届いた順にstdoutとstderrで届く", func(t *testing.T) {
		if r.log != "setup-out\nseedE:err\n" {
			t.Fatalf("log %q", r.log)
		}
		f := r.finished
		if f.Setup == nil || f.Setup.ExitCode != 0 || f.Exited == nil || f.Exited.ExitCode != 3 || f.JobTimedOut {
			t.Fatalf("finished %v", f)
		}
	})
	t.Run("outputsは回収してoutputs_host_dirへ書き、当たらなかったパターンはoutputs_errorに入る", func(t *testing.T) {
		f := r.finished
		if !slices.Equal(f.Outputs, []string{"report/a.xml", "uid.txt"}) || !strings.Contains(f.OutputsError, `no file matched "missing/*.xml"`) {
			t.Fatalf("outputs %v error %q", f.Outputs, f.OutputsError)
		}
		uid, _ := os.ReadFile(filepath.Join(outDir, "uid.txt"))
		if strings.TrimSpace(string(uid)) != "0" {
			t.Fatalf("uid %q", uid)
		}
		if fi, err := os.Stat(filepath.Join(outDir, "report/a.xml")); err != nil || fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("collected file must be readable only by the owner: %v %v", fi, err)
		}
	})
	t.Run("ジョブのsandboxは終わったら一覧から消え、ゲストrootは残る", func(t *testing.T) {
		list, err := c.ListSandboxes(context.Background(), connect.NewRequest(&sandboxv1.ListSandboxesRequest{}))
		if err != nil || len(list.Msg.Sandboxes) != 1 || list.Msg.Sandboxes[0].Id != "sb1" {
			t.Fatalf("sandboxes %v %v", list, err)
		}
		if _, err := os.Stat(filepath.Join(root, "workspace/uid.txt")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRunJobSetupFailure(t *testing.T) {
	c, sb1 := newFake(t)
	outDir := filepath.Join(t.TempDir(), "outputs")
	r, err := runJob(t, c, &sandboxv1.RunJobRequest{
		SetupShell:     `echo partial > /workspace/partial.txt; exit 7`,
		Shell:          `touch /workspace/ran.txt`,
		Outputs:        []string{"partial.txt"},
		OutputsHostDir: outDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	skipWithoutRoot(t, r)
	t.Run("setup_shellが失敗したらshellは動かず、Finishedのexitedは空になる", func(t *testing.T) {
		if slices.Contains(r.phases, "running") || r.finished.Exited != nil || r.finished.Setup.GetExitCode() != 7 {
			t.Fatalf("phases %v finished %v", r.phases, r.finished)
		}
		if _, err := os.Stat(filepath.Join(jobRoot(sb1, r.id), "workspace/ran.txt")); err == nil {
			t.Fatal("shell ran after setup_shell failed")
		}
	})
	t.Run("setup_shellが失敗してもoutputsは回収する", func(t *testing.T) {
		if !slices.Equal(r.finished.Outputs, []string{"partial.txt"}) {
			t.Fatalf("outputs %v", r.finished.Outputs)
		}
	})
}

func TestRunJobTimeouts(t *testing.T) {
	c, _ := newFake(t)
	t.Run("shellがtimeout_msを過ぎるとexitedのtimed_outが立つ", func(t *testing.T) {
		r, err := runJob(t, c, &sandboxv1.RunJobRequest{Shell: `sleep 5`, TimeoutMs: 200})
		if err != nil {
			t.Fatal(err)
		}
		if r.finished.Exited == nil || !r.finished.Exited.TimedOut || r.finished.JobTimedOut {
			t.Fatalf("finished %v", r.finished)
		}
	})
	t.Run("job_timeout_msを過ぎるとjob_timed_outが立ち、exitedは空になる", func(t *testing.T) {
		r, err := runJob(t, c, &sandboxv1.RunJobRequest{Shell: `sleep 5`, JobTimeoutMs: 200, Outputs: []string{"x"}, OutputsHostDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		if !r.finished.JobTimedOut || r.finished.Exited != nil || slices.Contains(r.phases, "outputs") {
			t.Fatalf("phases %v finished %v", r.phases, r.finished)
		}
	})
}

func TestRunJobRejectsMalformedRequests(t *testing.T) {
	c, _ := newFake(t)
	cases := map[string]*sandboxv1.RunJobRequest{
		"shellが空":                     {},
		"outputsにoutputs_host_dirが無い": {Shell: "true", Outputs: []string{"a"}},
		"outputsのパターンが..を含む":          {Shell: "true", Outputs: []string{"../a"}, OutputsHostDir: t.TempDir()},
		"FromSandboxのパターンが絶対パス": {Shell: "true", Inputs: []*sandboxv1.RunJobRequest_Input{
			{Source: &sandboxv1.RunJobRequest_Input_FromSandbox{FromSandbox: &sandboxv1.RunJobRequest_FromSandbox{Id: "sb1", Root: "/workspace", Patterns: []string{"/etc/**"}}}},
		}},
	}
	for name, req := range cases {
		t.Run(name+"ならInvalidArgumentで断る", func(t *testing.T) {
			if _, err := runJob(t, c, req); connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("err %v", err)
			}
		})
	}
	t.Run("FromSandboxの写し元が無ければNotFoundで終わり、ジョブのsandboxは残らない", func(t *testing.T) {
		_, err := runJob(t, c, &sandboxv1.RunJobRequest{Shell: "true", Inputs: []*sandboxv1.RunJobRequest_Input{
			{Source: &sandboxv1.RunJobRequest_Input_FromSandbox{FromSandbox: &sandboxv1.RunJobRequest_FromSandbox{Id: "nope", Root: "/workspace", Patterns: []string{"**"}}}},
		}})
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("err %v", err)
		}
		list, _ := c.ListSandboxes(context.Background(), connect.NewRequest(&sandboxv1.ListSandboxesRequest{}))
		if len(list.Msg.Sandboxes) != 1 {
			t.Fatalf("sandboxes %v", list.Msg.Sandboxes)
		}
	})
}

func TestDeleteImageRemovesOnlyThatImage(t *testing.T) {
	c, _ := newFake(t)
	ctx := context.Background()
	build := func(name string) string {
		st, err := c.BuildImage(ctx, connect.NewRequest(&sandboxv1.BuildImageRequest{ContextDir: "/x", Name: name}))
		if err != nil {
			t.Fatal(err)
		}
		var id string
		for st.Receive() {
			if b := st.Msg().GetBuilt(); b != nil {
				id = b.BuildId
			}
		}
		return id
	}
	a, b := build("a"), build("b")
	t.Run("DeleteImageは指したbuild_idのイメージだけを一覧から消し、2回目は何もせず成功する", func(t *testing.T) {
		for range 2 {
			if _, err := c.DeleteImage(ctx, connect.NewRequest(&sandboxv1.DeleteImageRequest{BuildId: a})); err != nil {
				t.Fatal(err)
			}
		}
		list, _ := c.ListImages(ctx, connect.NewRequest(&sandboxv1.ListImagesRequest{}))
		if len(list.Msg.Images) != 1 || list.Msg.Images[0].BuildId != b {
			t.Fatalf("images %v", list.Msg.Images)
		}
	})
}
