package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	masuda "github.com/TadahiroYamamura/masuda"
	"github.com/TadahiroYamamura/masuda/internal/rootfs"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// Host-level, not-yet-configurable constants matching scripts/setup-vm-host.sh
// and docs/CONTRIBUTING.md (Issue #31 M4/M5-5). A per-repo .masuda/settings.json
// knob for these belongs to a later step once there's a second real value to
// choose between; for now, VMBackend only works if the host was set up
// exactly this way.
const (
	vmBridge          = "br-masuda0"
	vmBridgeGatewayIP = "192.168.200.1"
	vmDHCPLeaseFile   = "/var/lib/misc/masuda-dnsmasq.leases"
)

const (
	cloudHypervisorBinary = "cloud-hypervisor"
	vmMemorySize          = "2048M"
	vmCPUs                = "boot=1"

	vmBootTimeout     = 30 * time.Second
	vmShutdownTimeout = 15 * time.Second
	vmDHCPTimeout     = 20 * time.Second
)

// VMBackend implements Backend using a Cloud Hypervisor microVM instead of a
// Docker container (Issue #31). It's the integration point for every piece
// built across M2-M5-5: internal/rootfs.Build for the disk image,
// EnsureTap/ReleaseTap for networking, StartVirtiofs for /workspace and
// /masuda-state, StartMCPRelay for the gate-wait MCP connection, and
// EnsureSSHKeypair/SSHAttachArgs for `masuda chat`.
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

// vmWorkDir is where VMBackend keeps everything it manages for workspace id
// (rootfs image, virtiofsd/mcp-relay sockets+logs, the cloud-hypervisor
// process's own pidfile) -- deliberately *not* inside stateDir, which gets
// shared into the guest as /masuda-state: none of this is meant to be
// guest-visible, unlike the daemon sockets statedaemon already keeps
// directly in stateDir today.
func vmWorkDir(id string) (string, error) {
	dataHome, err := workspace.DataHome()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(dataHome, "vm", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// findKernel returns the newest vmlinuz-* under masuda's data home
// (scripts/setup-vm-host.sh puts it there, copied to a location this
// process can read -- see docs/CONTRIBUTING.md for why the one under
// /boot itself isn't usable directly).
func findKernel() (string, error) {
	dataHome, err := workspace.DataHome()
	if err != nil {
		return "", err
	}
	matches, err := filepath.Glob(filepath.Join(dataHome, "vmlinuz-*"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no vmlinuz-* found under %s -- run scripts/setup-vm-host.sh first", dataHome)
	}
	sort.Strings(matches)
	return matches[len(matches)-1], nil
}

func currentUsername() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("determining current user: %w", err)
	}
	return u.Username, nil
}

// kernelVersionFromPath derives the `uname -r`-style version string from a
// vmlinuz-<version> path (see findKernel), e.g.
// "/…/vmlinuz-6.8.0-138-generic" -> "6.8.0-138-generic". This is also the
// exact host /lib/modules/<version> directory name -- scripts/setup-vm-host.sh
// installs the kernel via linux-image-generic, which always installs a
// matching linux-modules-<version>-generic package alongside it.
func kernelVersionFromPath(kernelPath string) string {
	return strings.TrimPrefix(filepath.Base(kernelPath), "vmlinuz-")
}

// virtiofsModuleExtraFile locates this host's copy of the guest kernel's
// virtiofs.ko (installed as part of the same linux-image-generic package
// findKernel's vmlinuz came from) and returns it as an ExtraFile to inject
// into the guest's kernel module tree -- see internal/rootfs.Build's depmod
// step for why this alone is enough for the guest to auto-load it (Issue
// #31 M5-6). A plain Docker/Ubuntu image has no kernel module tree at all
// otherwise: /workspace and /masuda-state wouldn't mount without this,
// since virtio-fs support isn't built into a generic distro kernel.
//
// GuestPath is usr/lib/modules/..., not lib/modules/...: the masuda-loop
// image (Ubuntu 24.04, usrmerge) has /lib as a symlink to /usr/lib, and
// staging a real directory tree at "lib/..." makes extractAndFormat's
// `cp -a` fail trying to overwrite that symlink with a directory
// (confirmed live -- alpine, used in internal/rootfs's own unit tests,
// has no such symlink, so this only surfaces against a real usrmerge
// image). depmod/modprobe resolve straight through the symlink either way,
// so this has no effect on module loading itself.
func virtiofsModuleExtraFile(kernelVersion string) (rootfs.ExtraFile, error) {
	pattern := filepath.Join("/lib/modules", kernelVersion, "kernel/fs/fuse/virtiofs.ko*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return rootfs.ExtraFile{}, err
	}
	if len(matches) == 0 {
		return rootfs.ExtraFile{}, fmt.Errorf("no virtiofs kernel module found at %s -- is linux-modules-%s-generic installed?", pattern, kernelVersion)
	}
	content, err := os.ReadFile(matches[0])
	if err != nil {
		return rootfs.ExtraFile{}, fmt.Errorf("reading %s: %w", matches[0], err)
	}
	rel, err := filepath.Rel("/lib/modules/"+kernelVersion, matches[0])
	if err != nil {
		return rootfs.ExtraFile{}, err
	}
	return rootfs.ExtraFile{
		GuestPath: filepath.Join("usr/lib/modules", kernelVersion, rel),
		Content:   content,
		Mode:      0o644,
		UID:       0,
		GID:       0,
	}, nil
}

func chPIDPath(workDir string) string { return filepath.Join(workDir, "cloud-hypervisor.pid") }
func vmWorkspaceSocketPath(workDir string) string {
	return filepath.Join(workDir, "virtiofs-workspace.sock")
}
func vmStateSocketPath(workDir string) string { return filepath.Join(workDir, "virtiofs-state.sock") }
func vmRelayPortFile(workDir string) string   { return filepath.Join(workDir, "mcp-relay.port") }

// vmClaudeSecretsDir/vmClaudeSecretsSocketPath stage the `claude
// setup-token` OAuth token (see internal/sandbox/claudetoken.go) for
// sharing into the guest at /masuda-secrets (runtime/fstab.vm's
// claude-secrets tag) -- a separate share from /masuda-state so this
// credential never ends up inside a target repository's workspace state,
// and separate from the rootfs image so a token registered or rotated
// after a workspace's rootfs was built still takes effect on the next
// Start without a rebuild.
func vmClaudeSecretsDir(workDir string) string { return filepath.Join(workDir, "claude-secrets") }
func vmClaudeSecretsSocketPath(workDir string) string {
	return filepath.Join(workDir, "virtiofs-claude-secrets.sock")
}

// vmStart builds (if not already running) everything a VM needs and boots
// it. Idempotent like DockerBackend's Start: if id's VM is already running,
// it returns immediately without rebuilding anything.
func vmStart(id, worktreeDir, stateDir, repoRoot, image string) (Handle, error) {
	if vmIsRunning(id) {
		return Handle{ID: id, ContainerName: TapName(id)}, nil
	}

	if _, err := exec.LookPath(cloudHypervisorBinary); err != nil {
		return Handle{}, fmt.Errorf("%s not found on PATH: %w", cloudHypervisorBinary, err)
	}
	kernelPath, err := findKernel()
	if err != nil {
		return Handle{}, err
	}
	workDir, err := vmWorkDir(id)
	if err != nil {
		return Handle{}, err
	}
	username, err := currentUsername()
	if err != nil {
		return Handle{}, err
	}

	// Shared into the guest for free via the existing /masuda-state
	// virtiofs mount (started below) -- no separate share needed, unlike
	// the claude-secrets one, since this isn't sensitive and stateDir is
	// already workspace-scoped.
	if err := WriteGitIdentity(stateDir, repoRoot); err != nil {
		return Handle{}, fmt.Errorf("writing git identity for guest: %w", err)
	}

	// SSH: only the public key ever goes into the guest (ExtraFile below);
	// EnsureSSHKeypair generates the pair on first use across all
	// workspaces, not one per workspace (see internal/sandbox/sshkey.go).
	_, pubKeyPath, err := EnsureSSHKeypair()
	if err != nil {
		return Handle{}, fmt.Errorf("ensuring VM SSH keypair: %w", err)
	}
	pubKey, err := os.ReadFile(pubKeyPath)
	if err != nil {
		return Handle{}, fmt.Errorf("reading VM SSH public key: %w", err)
	}

	virtiofsModule, err := virtiofsModuleExtraFile(kernelVersionFromPath(kernelPath))
	if err != nil {
		return Handle{}, fmt.Errorf("locating virtiofs kernel module: %w", err)
	}

	rootfsPath := filepath.Join(workDir, "rootfs.img")
	extra := []rootfs.ExtraFile{
		{GuestPath: "home/ubuntu/.ssh/authorized_keys", Content: pubKey, Mode: 0o600, UID: 1000, GID: 1000},
		// The Docker path injects this via `docker cp` after `docker create`,
		// deliberately not baked into the Docker image itself (ADR-0007), so
		// the loop protocol can be iterated on without an image rebuild. A
		// VM's rootfs has no equivalent "image vs. container" split -- it's
		// rebuilt from scratch on every Start regardless (see rootfs.Build's
		// own doc comment) -- so injecting it as an ExtraFile here costs
		// nothing extra and needs no separate delivery mechanism.
		{GuestPath: "home/ubuntu/.claude/CLAUDE.md", Content: masuda.ClaudeMD, Mode: 0o644, UID: 1000, GID: 1000},
		virtiofsModule,
	}
	if err := rootfs.Build(image, rootfsPath, extra); err != nil {
		return Handle{}, fmt.Errorf("building VM rootfs: %w", err)
	}

	tapName, err := EnsureTap(id, vmBridge, username)
	if err != nil {
		return Handle{}, fmt.Errorf("allocating VM network interface: %w", err)
	}

	wsVF, err := StartVirtiofs(worktreeDir, vmWorkspaceSocketPath(workDir), filepath.Join(workDir, "virtiofs-workspace.log"))
	if err != nil {
		_ = ReleaseTap(id)
		return Handle{}, fmt.Errorf("starting virtiofsd for /workspace: %w", err)
	}
	stateVF, err := StartVirtiofs(stateDir, vmStateSocketPath(workDir), filepath.Join(workDir, "virtiofs-state.log"))
	if err != nil {
		_ = wsVF.Stop()
		_ = ReleaseTap(id)
		return Handle{}, fmt.Errorf("starting virtiofsd for /masuda-state: %w", err)
	}

	// claude-secrets: only shared when a token has actually been registered
	// (`masuda internal claude-token set`) -- see claudetoken.go's doc
	// comment for why the Docker path's ~/.claude file bind mounts don't
	// translate to a VM guest. Not finding one is not an error here: the
	// guest just boots without it (and Claude Code inside prints its own
	// "not logged in" message), the same as a fresh masuda install that
	// hasn't been set up for the VM path at all yet.
	var secretsVF *VirtiofsProcess
	if tokenPath, err := ClaudeOAuthTokenPath(); err == nil {
		if token, err := os.ReadFile(tokenPath); err == nil {
			secretsDir := vmClaudeSecretsDir(workDir)
			if err := os.MkdirAll(secretsDir, 0o700); err != nil {
				_ = stateVF.Stop()
				_ = wsVF.Stop()
				_ = ReleaseTap(id)
				return Handle{}, fmt.Errorf("creating claude secrets staging directory: %w", err)
			}
			if err := os.WriteFile(filepath.Join(secretsDir, "token"), token, 0o600); err != nil {
				_ = stateVF.Stop()
				_ = wsVF.Stop()
				_ = ReleaseTap(id)
				return Handle{}, fmt.Errorf("staging claude oauth token: %w", err)
			}
			secretsVF, err = StartVirtiofs(secretsDir, vmClaudeSecretsSocketPath(workDir), filepath.Join(workDir, "virtiofs-claude-secrets.log"))
			if err != nil {
				_ = stateVF.Stop()
				_ = wsVF.Stop()
				_ = ReleaseTap(id)
				return Handle{}, fmt.Errorf("starting virtiofsd for /masuda-secrets: %w", err)
			}
		}
	}

	relayPort, err := freePort()
	if err != nil {
		if secretsVF != nil {
			_ = secretsVF.Stop()
		}
		_ = stateVF.Stop()
		_ = wsVF.Stop()
		_ = ReleaseTap(id)
		return Handle{}, fmt.Errorf("allocating mcp-relay port: %w", err)
	}
	relay, err := StartMCPRelay(
		statedaemon.CuratedSocketPath(stateDir),
		vmBridgeGatewayIP, relayPort,
		filepath.Join(workDir, "mcp-relay.log"))
	if err != nil {
		if secretsVF != nil {
			_ = secretsVF.Stop()
		}
		_ = stateVF.Stop()
		_ = wsVF.Stop()
		_ = ReleaseTap(id)
		return Handle{}, fmt.Errorf("starting mcp-relay: %w", err)
	}
	// vmStop runs as a separate invocation (a later masuda command run),
	// with no access to the *MCPRelayProcess this call returned -- unlike
	// the virtiofsd sockets, whose paths are deterministic and can just be
	// recomputed, relayPort was randomly chosen by freePort(), so it has to
	// be persisted for Stop to find and kill the right process.
	if err := os.WriteFile(vmRelayPortFile(workDir), []byte(strconv.Itoa(relayPort)), 0o644); err != nil {
		if secretsVF != nil {
			_ = secretsVF.Stop()
		}
		_ = relay.Stop()
		_ = stateVF.Stop()
		_ = wsVF.Stop()
		_ = ReleaseTap(id)
		return Handle{}, fmt.Errorf("recording mcp-relay port: %w", err)
	}

	mac := MACFor(id)
	cmdline := fmt.Sprintf("console=ttyS0 root=/dev/vda rw masuda.mcp_relay=%s", relay.Addr)
	fsArgs := []string{
		"tag=workspace,socket=" + wsVF.SocketPath,
		"tag=masuda-state,socket=" + stateVF.SocketPath,
	}
	if secretsVF != nil {
		fsArgs = append(fsArgs, "tag=claude-secrets,socket="+secretsVF.SocketPath)
	}
	cmdArgs := []string{
		"--kernel", kernelPath,
		"--disk", "path=" + rootfsPath + ",readonly=off,image_type=raw",
		"--fs",
	}
	cmdArgs = append(cmdArgs, fsArgs...)
	cmdArgs = append(cmdArgs,
		"--net", "tap="+tapName+",mac="+mac,
		"--cpus", vmCPUs,
		"--memory", "size="+vmMemorySize+",shared=on",
		"--cmdline", cmdline,
		"--console", "off",
		"--serial", "file="+filepath.Join(workDir, "console.log"),
	)
	cmd := exec.Command(cloudHypervisorBinary, cmdArgs...)
	if err := startBackgroundProcess(cmd, chPIDPath(workDir)); err != nil {
		if secretsVF != nil {
			_ = secretsVF.Stop()
		}
		_ = relay.Stop()
		_ = stateVF.Stop()
		_ = wsVF.Stop()
		_ = ReleaseTap(id)
		return Handle{}, fmt.Errorf("starting cloud-hypervisor: %w", err)
	}

	if _, err := LookupGuestIP(mac, vmDHCPLeaseFile, vmBootTimeout); err != nil {
		_ = vmStop(id)
		return Handle{}, fmt.Errorf("VM did not obtain a DHCP lease in time (boot failure?): %w", err)
	}

	return Handle{ID: id, ContainerName: tapName}, nil
}

// vmStop shuts the VM for workspace id down and releases everything Start
// allocated. Not an error if it's already gone. Runs as an independent
// invocation from Start (a separate `masuda sandbox stop`), so unlike
// Start's own rollback paths it has no in-memory handles to the processes
// it's stopping -- everything here is found again via deterministic paths
// (vmWorkspaceSocketPath etc.) or the port file Start wrote.
//
// Shutdown is graceful where possible: SSH in and run `sudo systemctl
// poweroff` (see the sudoers.d rule the Dockerfile installs, scoped to
// exactly that command), then wait for cloud-hypervisor's own process to
// exit on its own. Confirmed live that skipping this and just killing the
// process corrupts the disk image (the guest never gets to unmount/sync) --
// this isn't optional polish.
func vmStop(id string) error {
	workDir, err := vmWorkDir(id)
	if err != nil {
		return err
	}

	attemptGracefulShutdown(id, workDir)
	waitForProcessExit(chPIDPath(workDir), vmShutdownTimeout)

	// Fallback if the graceful path above didn't finish in time (or never
	// got to run at all, e.g. no DHCP lease found): killStalePID is a
	// no-op against an already-exited process, so this is safe to always
	// call.
	killStalePID(chPIDPath(workDir))
	_ = os.Remove(chPIDPath(workDir))

	stopKnownProcess(vmWorkspaceSocketPath(workDir))
	stopKnownProcess(vmStateSocketPath(workDir))
	stopKnownProcess(vmClaudeSecretsSocketPath(workDir)) // no-op if claude-secrets was never started (no token registered)

	if port, err := os.ReadFile(vmRelayPortFile(workDir)); err == nil {
		addr := vmBridgeGatewayIP + ":" + strings.TrimSpace(string(port))
		stopKnownProcess(addr)
	}

	if err := ReleaseTap(id); err != nil {
		return fmt.Errorf("releasing VM network interface: %w", err)
	}

	// Ephemeral by design, same as Docker's `rm -f`: a fresh Start rebuilds
	// the rootfs image and sockets from scratch, nothing here is meant to
	// survive a Stop.
	return os.RemoveAll(workDir)
}

// stopKnownProcess kills whatever startBackgroundProcess-managed process is
// recorded for identity (a socket path or a listen address -- see pidPath)
// and removes its pid file. Best effort, mirroring killStalePID: there's
// nothing a caller can usefully do about a leftover orphan process beyond
// what this already tries.
func stopKnownProcess(identity string) {
	killStalePID(pidPath(identity))
	_ = os.Remove(pidPath(identity))
}

// attemptGracefulShutdown is best-effort: any failure (no DHCP lease found,
// SSH unreachable, guest too slow) just falls through to vmStop's
// SIGTERM-based fallback, which vmStop always runs regardless.
func attemptGracefulShutdown(id, workDir string) {
	if _, err := os.Stat(chPIDPath(workDir)); err != nil {
		return // nothing recorded as running for this id
	}
	guestIP, err := LookupGuestIP(MACFor(id), vmDHCPLeaseFile, 2*time.Second)
	if err != nil {
		return
	}
	privKeyPath, _, err := SSHKeyPaths()
	if err != nil {
		return
	}
	sshArgs := append(sshBaseArgs(guestIP, privKeyPath), "sudo", "-n", "systemctl", "poweroff")
	cmd := exec.Command(sshArgs[0], sshArgs[1:]...)
	_ = cmd.Run() // the SSH connection is expected to drop mid-command as the guest shuts down
}

func waitForProcessExit(pidFilePath string, timeout time.Duration) {
	data, err := os.ReadFile(pidFilePath)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if sigErr := proc.Signal(syscall.Signal(0)); sigErr != nil {
			return // process is gone
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// vmIsRunning reports whether cloud-hypervisor is alive for workspace id.
func vmIsRunning(id string) bool {
	workDir, err := vmWorkDir(id)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(chPIDPath(workDir))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// vmAttachArgs looks up workspace id's guest IP (from the DHCP lease its
// deterministic MAC address should have, see MACFor) and returns the SSH
// argv to attach to it.
func vmAttachArgs(id string) ([]string, error) {
	guestIP, err := LookupGuestIP(MACFor(id), vmDHCPLeaseFile, vmDHCPTimeout)
	if err != nil {
		return nil, fmt.Errorf("looking up VM guest IP for %s: %w", id, err)
	}
	privKeyPath, _, err := SSHKeyPaths()
	if err != nil {
		return nil, err
	}
	return SSHAttachArgs(guestIP, privKeyPath), nil
}
