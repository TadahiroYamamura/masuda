package serve

import (
	"context"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
)

// stubHost はテストの間だけ、ホストの資源をmemoryMiB・cpusに見せる。
func stubHost(t *testing.T, memoryMiB uint64, cpus int) {
	t.Helper()
	orig := hostResources
	hostResources = func() (uint64, int) { return memoryMiB, cpus }
	t.Cleanup(func() { hostResources = orig })
}

func TestExceedsHost(t *testing.T) {
	cases := []struct {
		name      string
		hostMem   uint64
		hostCPUs  int
		memoryMiB uint32
		cpus      uint32
		want      []string
	}{
		{"ホストの総量とCPU数ちょうどなら理由を返さない", 8192, 4, 8192, 4, nil},
		{"メモリがホストの総量を1MiB超えたら理由を返す", 8192, 4, 8193, 4, []string{"memoryMiB 8193 exceeds the host's total memory (8192 MiB)"}},
		{"CPU数がホストより1つ多ければ理由を返す", 8192, 4, 8192, 5, []string{"cpus 5 exceeds the host's CPU count (4)"}},
		{"メモリとCPU数の両方が超えたら理由を2つ返す", 8192, 4, 9000, 8, []string{"memoryMiB 9000", "cpus 8"}},
		{"ホストのメモリが分からなければメモリは比べない", 0, 4, 1 << 20, 4, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stubHost(t, c.hostMem, c.hostCPUs)
			got := exceedsHost("go", c.memoryMiB, c.cpus)
			if len(got) != len(c.want) {
				t.Fatalf("exceedsHost = %q, want %d problems", got, len(c.want))
			}
			for i, w := range c.want {
				if !strings.Contains(got[i], w) || !strings.HasPrefix(got[i], "image go: ") {
					t.Fatalf("problem %d = %q, want it to contain %q", i, got[i], w)
				}
			}
		})
	}
}

// recordingCreate はCreateSandboxの要求を覚えておくsandboxクライアント。
type recordingCreate struct {
	sandboxv1connect.SandboxServiceClient
	mu   *sync.Mutex
	reqs *[]*sandboxv1.CreateSandboxRequest
}

func (r recordingCreate) CreateSandbox(ctx context.Context, req *connect.Request[sandboxv1.CreateSandboxRequest]) (*connect.Response[sandboxv1.Sandbox], error) {
	r.mu.Lock()
	*r.reqs = append(*r.reqs, req.Msg)
	r.mu.Unlock()
	return r.SandboxServiceClient.CreateSandbox(ctx, req)
}

func TestRunPassesImageMemoryAndCPUsToSandbox(t *testing.T) {
	t.Run("settings.jsonのmemoryMiBとcpusがVMの作成に渡る", func(t *testing.T) {
		stubHost(t, 16384, 8)
		var mu sync.Mutex
		var reqs []*sandboxv1.CreateSandboxRequest
		ws, _, repo := newTestAPIWith(t, func(c sandboxv1connect.SandboxServiceClient) sandboxv1connect.SandboxServiceClient {
			return recordingCreate{c, &mu, &reqs}
		})
		writeRepoFile(t, repo, ".masuda/settings.json", `{"images": {"default": {"memoryMiB": 6144, "cpus": 2}}}`)
		res, err := ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/x", Inputs: smokeInputs}))
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, ws, res.Msg.Id, apiv1.WorkspaceState_WORKSPACE_STATE_RUNNING)
		mu.Lock()
		defer mu.Unlock()
		if len(reqs) == 0 || reqs[0].MemoryMib != 6144 || reqs[0].Cpus != 2 {
			t.Fatalf("CreateSandbox requests = %+v", reqs)
		}
	})
	t.Run("ホストの総量を超えるmemoryMiBはワークスペースを作る前に断る", func(t *testing.T) {
		stubHost(t, 4096, 8)
		ws, _, repo := newTestAPI(t)
		writeRepoFile(t, repo, ".masuda/settings.json", `{"images": {"default": {"memoryMiB": 8192}}}`)
		_, err := ws.Run(context.Background(), connect.NewRequest(&apiv1.RunRequest{RepoRoot: repo, Workflow: "workflows/smoke", Branch: "feat/x", Inputs: smokeInputs}))
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "memoryMiB 8192 exceeds the host's total memory (4096 MiB)") {
			t.Fatalf("Run = %v", err)
		}
		list, err := ws.List(context.Background(), connect.NewRequest(&apiv1.ListWorkspacesRequest{}))
		if err != nil || len(list.Msg.Workspaces) != 0 {
			t.Fatalf("workspaces after the rejected Run: %v %+v", err, list)
		}
	})
}
