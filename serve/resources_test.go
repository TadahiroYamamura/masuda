package serve

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/internal/fakesandbox"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// probeOutput はresourceProbeの出力の形（実VMの出力に倣う）。tmpMountがfalseなら、/tmpが
// 別のマウントでないときのdfのように同じ`/`を2行出す。
func probeOutput(availKB int, tmpPercent string, oomLine string, tmpMount bool) string {
	var b strings.Builder
	b.WriteString("MemTotal:        4019200 kB\nMemFree:           12000 kB\n")
	if availKB >= 0 {
		b.WriteString("MemAvailable:      " + strconv.Itoa(availKB) + " kB\nBuffers:            1000 kB\n")
	}
	b.WriteString("--vmstat\nnr_free_pages 3000\n")
	b.WriteString(oomLine)
	b.WriteString("pgfault 123\n--df\n")
	b.WriteString("Filesystem     1024-blocks    Used Available Capacity Mounted on\n")
	b.WriteString("/dev/vda          10255636 4521880   5193084      47% /\n")
	if tmpMount {
		b.WriteString("tmpfs              2009600 1909120    100480      " + tmpPercent + " /tmp\n")
	} else {
		b.WriteString("/dev/vda          10255636 4521880   5193084      47% /\n")
	}
	return b.String()
}

func TestVMResourceWarnings(t *testing.T) {
	limits := resourceLimits{memoryPercent: 10, diskPercent: 90}
	// MemTotalは4019200kBなので、10%は401920kB
	cases := []struct {
		name string
		out  string
		want []string
	}{
		{"使えるメモリが合計の10%ちょうどなら警告しない", probeOutput(401920, "50%", "oom_kill 0\n", true), nil},
		{"使えるメモリが合計の10%を1kBでも下回ったら警告する", probeOutput(401919, "50%", "oom_kill 0\n", true), []string{"memory low"}},
		{"tmpの使用率が90%ちょうどなら警告しない", probeOutput(900000, "90%", "oom_kill 0\n", true), nil},
		{"tmpの使用率が90%を超えたらマウント先としきい値で警告する", probeOutput(900000, "91%", "oom_kill 0\n", true), []string{"/tmp over 90%"}},
		{"OOM killerが一度でも動いたら回数を出す", probeOutput(900000, "50%", "oom_kill 2\n", true), []string{"oom-kill x2"}},
		{"oom_killの無いカーネルでは0回とみなす", probeOutput(900000, "50%", "", true), nil},
		{"複数あれば、メモリ・ディスク・OOMの順に並べる", probeOutput(21000, "95%", "oom_kill 1\n", true), []string{"memory low", "/tmp over 90%", "oom-kill x1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := parseVMResources(c.out)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(r.warnings(limits), ","); got != strings.Join(c.want, ",") {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
	t.Run("tmpが別のマウントでなければ同じルートを2回数えない", func(t *testing.T) {
		r, err := parseVMResources(probeOutput(900000, "", "oom_kill 0\n", false))
		if err != nil {
			t.Fatal(err)
		}
		if len(r.disks) != 1 || r.disks[0].mount != "/" || r.disks[0].percent != 47 {
			t.Fatalf("disks: %+v", r.disks)
		}
	})
	t.Run("しきい値は設定の値を使う", func(t *testing.T) {
		r, err := parseVMResources(probeOutput(900000, "85%", "oom_kill 0\n", true))
		if err != nil {
			t.Fatal(err)
		}
		// 900000kBは合計の約22.4%
		if got := strings.Join(r.warnings(resourceLimits{memoryPercent: 25, diskPercent: 80}), ","); got != "memory low,/tmp over 80%" {
			t.Fatalf("got %q", got)
		}
	})
	for name, out := range map[string]string{
		"MemAvailableが無ければ測れなかったとみなす": probeOutput(-1, "50%", "", true),
		"dfの行が無ければ測れなかったとみなす":         "MemTotal: 4019200 kB\nMemAvailable: 900000 kB\n--vmstat\n--df\ndf: /tmp: No such file or directory\n",
		"MemTotalが無ければ測れなかったとみなす":     "MemAvailable: 900000 kB\n--vmstat\n--df\n/dev/vda 10255636 4521880 5193084 47% /\n",
		"出力が空なら測れなかったとみなす":            "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseVMResources(out); err == nil {
				t.Fatal("must fail")
			}
		})
	}
}

func TestCheckResources(t *testing.T) {
	dataDir := t.TempDir()
	fake := fakesandbox.StartInProcess(FakeDir(dataDir))
	b := newBackend(workspace.NewStore(dataDir), fake.Client, fake.Close, Options{DataDir: dataDir, FakeSandbox: true})
	t.Cleanup(b.close)
	ctx := context.Background()
	const id = "w1"
	if _, err := fake.Client.CreateSandbox(ctx, connect.NewRequest(&sandboxv1.CreateSandboxRequest{Id: id, DefaultUser: "ubuntu"})); err != nil {
		t.Fatal(err)
	}
	b.acts.reset(id)
	w := &workspace.Workspace{Meta: workspace.Meta{ID: id, State: workspace.StateRunning}}
	warning := func() string { return b.acts.compute(w, 10*time.Minute, time.Now()).ResourceWarning }

	t.Run("測った値がしきい値を超えていれば活動に警告が載る", func(t *testing.T) {
		// フェイクのExecはホストで流れるので、ホストの値がどうであれ超えるしきい値にする
		b.vmLimits = resourceLimits{memoryPercent: 101, diskPercent: -1}
		b.checkResources(ctx, id)
		if got := warning(); !strings.HasPrefix(got, "memory low, / over -1%") {
			t.Fatalf("warning = %q", got)
		}
	})
	t.Run("人間の判断待ちでも警告を見せる", func(t *testing.T) {
		gate := &workspace.Workspace{Meta: workspace.Meta{ID: id, State: workspace.StateWaitingGate}}
		if got := b.acts.compute(gate, 10*time.Minute, time.Now()).ResourceWarning; got == "" {
			t.Fatal("waiting for a gate must keep the warning")
		}
	})
	t.Run("実行が終わったら警告を見せない", func(t *testing.T) {
		done := &workspace.Workspace{Meta: workspace.Meta{ID: id, State: workspace.StateDone}}
		if got := b.acts.compute(done, 10*time.Minute, time.Now()); got.ResourceWarning != "" || got.Kind != apiv1.ActivityKind_ACTIVITY_KIND_IDLE {
			t.Fatalf("done: %+v", got)
		}
	})
	t.Run("測れなかったときは前の警告を残す", func(t *testing.T) {
		before := warning()
		if _, err := fake.Client.DestroySandbox(ctx, connect.NewRequest(&sandboxv1.DestroySandboxRequest{Id: id})); err != nil {
			t.Fatal(err)
		}
		b.vmLimits = resourceLimits{memoryPercent: 0, diskPercent: 101}
		b.checkResources(ctx, id)
		if got := warning(); got != before {
			t.Fatalf("warning = %q, want %q kept", got, before)
		}
	})
}
