package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	masuda "github.com/TadahiroYamamura/masuda"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/microvm"
	"github.com/TadahiroYamamura/masuda/internal/rootfs"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// Host-level, not-yet-configurable constants matching scripts/setup-vm-host.sh
// and docs/INSTALLATION.md (Issue #31 M4/M5-5). A per-repo .masuda/settings.json
// knob for these belongs to a later step once there's a second real value to
// choose between; for now, VMBackend only works if the host was set up
// exactly this way.
const (
	vmBridge          = "br-masuda0"
	vmBridgeGatewayIP = "192.168.200.1"
	vmDHCPLeaseFile   = "/var/lib/misc/masuda-dnsmasq.leases"
)

// vmHost describes masuda's host setup to internal/microvm.
func vmHost() (microvm.Host, error) {
	dataHome, err := workspace.DataHome()
	if err != nil {
		return microvm.Host{}, err
	}
	return microvm.Host{
		DataDir:   dataHome,
		Bridge:    vmBridge,
		LeaseFile: vmDHCPLeaseFile,
		NetHelper: "masuda-net-helper",
		BridgeMissingHint: "  Re-apply it with:  sudo systemctl restart masuda-vm-host\n" +
			"  If that unit does not exist yet, run ./scripts/setup-vm-host.sh from the masuda repository root once; it installs the unit.",
		SSHKeyComment: "masuda-vm",
		GuestUser:     microvm.GuestUser{Name: "ubuntu", UID: 1000, GID: 1000},
	}, nil
}

// GuestStateDir and GuestWorktreeDir are where the guest mounts the two
// virtiofs shares (runtime/fstab.vm's tags). Exported because host-side code
// that writes instructions *for* the guest has to name paths the guest can
// actually open -- the host's own path for the same directory is meaningless
// in there.
const (
	GuestStateDir    = "/masuda-state"
	GuestWorktreeDir = "/workspace"
)

// VMBackend implements Backend using a Cloud Hypervisor microVM instead of a
// Docker container (Issue #31). Booting the VM itself is internal/microvm's
// job; what VMBackend adds is everything masuda puts around it: the image
// entry, the Claude token share, the git identity, the MCP relay for the
// gate-wait connection, and the shared egress proxy.
//
// The only Backend implementation now wired into `masuda sandbox
// start`/`stop` and every other cmd/masuda call site (see
// cmd/masuda/sandbox.go's sandboxBackend) -- DockerBackend was deleted once
// this proved stable in real use, so there's no `.masuda/settings.json`
// backend-selection field either.
type VMBackend struct{}

var _ Backend = VMBackend{}

func (VMBackend) Start(id, worktreeDir, stateDir, repoRoot, image string) (Handle, error) {
	return vmStart(id, worktreeDir, stateDir, repoRoot, image)
}

func (VMBackend) Stop(id string) error {
	return vmStop(id)
}

func (VMBackend) IsRunning(id string) bool {
	return vmIsRunning(id)
}

func (VMBackend) AttachArgs(id string) ([]string, error) {
	return vmAttachArgs(id)
}

func vmRelayPortFile(workDir string) string { return filepath.Join(workDir, "mcp-relay.port") }
func vmRelayPIDFile(workDir string) string  { return filepath.Join(workDir, "mcp-relay.pid") }

// resolveEgressAllowlist returns the hostnames workspace repoRoot's VM is
// allowed to reach over TLS (Issue #11 M4): the intersection of
// config.Config.EgressAllowlist (repoRoot's committed declaration) and
// config.LocalSettings.EgressAllowlist (this user's approval, `masuda
// egress approve`) -- a hostname absent from either side is denied.
// Neither file existing is not an error, matching config.Load/LoadLocal's
// own "missing means empty" treatment -- a repo with no declaration, or a
// user who has approved nothing, both simply get no allowed hostnames.
func resolveEgressAllowlist(repoRoot string) ([]string, error) {
	cfg, err := config.Load(repoRoot)
	if err != nil {
		return nil, err
	}
	local, err := config.LoadLocal(repoRoot)
	if err != nil {
		return nil, err
	}
	approved := make(map[string]bool, len(local.EgressAllowlist))
	for _, h := range local.EgressAllowlist {
		approved[h] = true
	}
	var allowed []string
	for _, h := range cfg.EgressAllowlist {
		if approved[h] {
			allowed = append(allowed, h)
		}
	}
	return allowed, nil
}

// resolveImageEntry turns the image *entry name* every caller now passes
// (ADR-0054) into the local Docker reference to export a rootfs from, plus
// that entry's build parameters. The entry's directory must exist: the
// derived tag is meaningless on its own, and letting `docker create` fail
// on a tag nobody ever built produces an error that says nothing about
// which masuda command the user skipped.
func resolveImageEntry(repoRoot, entry string) (string, config.ImageConfig, error) {
	if _, err := os.Stat(config.ImageDockerfilePath(repoRoot, entry)); err != nil {
		return "", config.ImageConfig{}, fmt.Errorf("image entry %q has no Dockerfile at %s -- run `masuda init` (new project) or `masuda sandbox build` (built elsewhere): %w",
			entry, config.ImageDockerfilePath(repoRoot, entry), err)
	}
	tag, err := config.ImageTag(repoRoot, entry)
	if err != nil {
		return "", config.ImageConfig{}, err
	}
	cfg, err := config.LoadImage(repoRoot, entry)
	if err != nil {
		return "", config.ImageConfig{}, err
	}
	return tag, cfg, nil
}

// vmClaudeSecretsDir/vmClaudeSecretsSocketPath stage the `claude
// setup-token` OAuth token (see internal/sandbox/claudetoken.go) for
// sharing into the guest at /masuda-secrets (runtime/fstab.vm's
// claude-secrets tag) -- a separate share from /masuda-state so this
// credential never ends up inside a target repository's workspace state,
// and separate from the rootfs image so a token registered or rotated
// after a workspace's rootfs was built still takes effect on the next
// Start without a rebuild.
func vmClaudeSecretsDir(workDir string) string { return filepath.Join(workDir, "claude-secrets") }

// vmStart boots workspace id's VM. Idempotent: if it is already running,
// it returns immediately without rebuilding anything.
//
// The MCP relay and the Claude token staging happen before the VM is
// booted, not after: the relay's address has to be on the guest's kernel
// command line, and the token has to be in place for its share.
func vmStart(id, worktreeDir, stateDir, repoRoot, image string) (Handle, error) {
	h, err := vmHost()
	if err != nil {
		return Handle{}, err
	}
	if h.IsRunning(id) {
		return Handle{ID: id, ContainerName: microvm.TapName(id)}, nil
	}
	if err := h.Check(); err != nil {
		return Handle{}, err
	}
	workDir, err := h.WorkDir(id)
	if err != nil {
		return Handle{}, err
	}

	// Shared into the guest for free via the existing /masuda-state
	// virtiofs mount -- no separate share needed, unlike the claude-secrets
	// one, since this isn't sensitive and stateDir is already
	// workspace-scoped.
	if err := WriteGitIdentity(stateDir, repoRoot); err != nil {
		return Handle{}, fmt.Errorf("writing git identity for guest: %w", err)
	}

	imageTag, imageCfg, err := resolveImageEntry(repoRoot, image)
	if err != nil {
		return Handle{}, err
	}

	shares := []microvm.Share{
		{Tag: "workspace", HostDir: worktreeDir},
		{Tag: "masuda-state", HostDir: stateDir},
	}
	// claude-secrets: only shared when a token has actually been registered
	// (`masuda internal claude-token set`) -- see claudetoken.go's doc
	// comment for why the Docker path's ~/.claude file bind mounts don't
	// translate to a VM guest. Not finding one is not an error here: the
	// guest just boots without it (and Claude Code inside prints its own
	// "not logged in" message), the same as a fresh masuda install that
	// hasn't been set up for the VM path at all yet.
	if tokenPath, err := ClaudeOAuthTokenPath(); err == nil {
		if token, err := os.ReadFile(tokenPath); err == nil {
			secretsDir := vmClaudeSecretsDir(workDir)
			if err := os.MkdirAll(secretsDir, 0o700); err != nil {
				return Handle{}, fmt.Errorf("creating claude secrets staging directory: %w", err)
			}
			if err := os.WriteFile(filepath.Join(secretsDir, "token"), token, 0o600); err != nil {
				return Handle{}, fmt.Errorf("staging claude oauth token: %w", err)
			}
			shares = append(shares, microvm.Share{Tag: "claude-secrets", HostDir: secretsDir})
		}
	}

	relayPort, err := freePort()
	if err != nil {
		return Handle{}, fmt.Errorf("allocating mcp-relay port: %w", err)
	}
	relay, err := StartMCPRelay(
		statedaemon.CuratedSocketPath(stateDir),
		vmBridgeGatewayIP, relayPort,
		filepath.Join(workDir, "mcp-relay.log"), vmRelayPIDFile(workDir))
	if err != nil {
		return Handle{}, fmt.Errorf("starting mcp-relay: %w", err)
	}
	// vmStop runs as a separate invocation (a later masuda command run),
	// with no access to the *MCPRelayProcess this call returned -- relayPort
	// was randomly chosen by freePort(), so it has to be persisted for Stop
	// to find and kill the right process.
	if err := os.WriteFile(vmRelayPortFile(workDir), []byte(strconv.Itoa(relayPort)), 0o644); err != nil {
		_ = relay.Stop()
		return Handle{}, fmt.Errorf("recording mcp-relay port: %w", err)
	}

	// Egress proxy (Issue #11): one shared process for the whole host, not
	// one per workspace -- see internal/sandbox/egressproxy.go's doc
	// comment for why. The guest never learns its address either: once
	// scripts/setup-vm-host.sh's REDIRECT rule is in place, the guest's
	// own outbound 443 traffic gets redirected to it, so there's nothing
	// to pass via the kernel command line the way mcp-relay's address is.
	// It is deliberately never stopped on a failure below: other
	// workspaces' VMs may depend on it staying up.
	if err := EnsureEgressProxy(); err != nil {
		_ = relay.Stop()
		return Handle{}, fmt.Errorf("ensuring egress-proxy is running: %w", err)
	}

	err = h.Start(microvm.Spec{
		ID:    id,
		Image: imageTag,
		Rootfs: rootfs.Options{
			// Injected rather than baked into the image (ADR-0007), so the
			// loop protocol can be iterated on without an image rebuild.
			// The rootfs is rebuilt from scratch on every Start regardless,
			// so this costs nothing extra.
			ExtraFiles: []rootfs.ExtraFile{
				{GuestPath: "home/ubuntu/.claude/CLAUDE.md", Content: masuda.ClaudeMD, Mode: 0o644, UID: 1000, GID: 1000},
			},
			MinSizeMiB: imageCfg.RootfsSizeMiB,
		},
		Modules:    microvm.VirtiofsModuleOnly,
		Shares:     shares,
		KernelArgs: []string{"masuda.mcp_relay=" + relay.Addr},
	})
	if err != nil {
		_ = relay.Stop()
		return Handle{}, err
	}
	return Handle{ID: id, ContainerName: microvm.TapName(id)}, nil
}

// vmStop shuts workspace id's VM down and releases everything vmStart
// allocated. Not an error if it's already gone.
//
// The relay is stopped between microvm's Shutdown and Remove because its
// pid file lives in the VM's working directory, which Remove deletes. Its
// identity is the curated socket it was pointed at, the same value vmStart
// passed to StartMCPRelay; an unresolvable state directory leaves the
// marker empty, which makes KillStalePID do nothing rather than signal an
// unidentified PID. vmRelayPortFile stays only because a VM started before
// the pid file existed recorded its address there.
func vmStop(id string) error {
	h, err := vmHost()
	if err != nil {
		return err
	}
	workDir, err := h.WorkDir(id)
	if err != nil {
		return err
	}
	shutdownErr := h.Shutdown(id)

	relayMarker := ""
	if stateDir, err := workspace.StateDir(id); err == nil {
		relayMarker = statedaemon.CuratedSocketPath(stateDir)
	}
	microvm.KillStalePID(vmRelayPIDFile(workDir), relayMarker)
	_ = os.Remove(vmRelayPIDFile(workDir))
	// egress-proxy is deliberately NOT stopped here -- see
	// EnsureEgressProxy's doc comment: it's a shared, host-wide process,
	// not scoped to this workspace.

	if shutdownErr != nil {
		return shutdownErr
	}
	return h.Remove(id)
}

func vmIsRunning(id string) bool {
	h, err := vmHost()
	if err != nil {
		return false
	}
	return h.IsRunning(id)
}

// vmAttachArgs returns the ssh argv attaching to the guest's tmux session,
// where runtime/entrypoint.sh runs Claude Code.
func vmAttachArgs(id string) ([]string, error) {
	h, err := vmHost()
	if err != nil {
		return nil, err
	}
	return h.AttachArgs(id, "tmux", "attach", "-t", tmuxSession)
}

// SSHKeyPaths and GenerateSSHKeypair expose the host's VM SSH keypair to
// `masuda internal vm-ssh-key`.
func SSHKeyPaths() (privatePath, publicPath string, err error) {
	h, err := vmHost()
	if err != nil {
		return "", "", err
	}
	return h.SSHKeyPaths()
}

func GenerateSSHKeypair() error {
	h, err := vmHost()
	if err != nil {
		return err
	}
	return h.GenerateSSHKeypair()
}
