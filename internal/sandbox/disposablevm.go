// disposablevm.go runs one declared privileged command in a VM that exists
// only for that command (ADR-0053). It reuses VMBackend's building blocks
// -- rootfs.Build, the TAP pool, virtiofsd, cloud-hypervisor -- but gives
// the guest far less: a *copy* of the workspace tree, a results directory,
// and nothing else. No /masuda-secrets, no MCP relay, no SSH. The VM is
// destroyed the moment its command finishes, so the exposure is bounded by
// that command's runtime rather than the AI session's.
package sandbox

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	masuda "github.com/TadahiroYamamura/masuda"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/rootfs"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

const (
	// PrivilegedRunsDirName is the subdirectory of a workspace's state
	// directory holding one directory per privileged command, each with one
	// directory per run (ADR-0053). A fixed segment separates every
	// target-repo-chosen command name from masuda's own names directly
	// under the state directory (plan/, triage_concern.json, ...).
	PrivilegedRunsDirName = "privilegedCommands"

	// maxPrivilegedLogBytes is the hard cap the guest-side runner enforces
	// while writing: it protects the disposable VM's own disk and the host
	// process that later reads the file, and has nothing to do with any
	// model's context window. Adjust freely -- this is a constant, not a
	// decision.
	maxPrivilegedLogBytes = 8 << 20

	// privilegedBootSlack is how much longer than the declared timeout the
	// host waits before concluding the VM is never coming back. The guest
	// enforces the declared timeout itself and powers off with an exit code;
	// this covers the boot, the shutdown, and the case where the guest never
	// got far enough to enforce anything.
	privilegedBootSlack = 5 * time.Minute

	// defaultPrivilegedTimeout applies when a declaration sets no timeout
	// of its own (config.PrivilegedCommandDecl.TimeoutSeconds).
	defaultPrivilegedTimeout = 30 * time.Minute

	// outputsDirName holds what a declaration's Outputs asked to keep,
	// inside the run's own directory.
	outputsDirName = "outputs"

	// maxCollectedOutputBytes/Files bound what one run can leave on the
	// host. Runs are never auto-deleted (ADR-0053), so without a bound a
	// loop that runs a command repeatedly would fill the disk one coverage
	// report at a time. Both are ordinary constants, not decisions.
	maxCollectedOutputBytes = 64 << 20
	maxCollectedOutputFiles = 1000
)

// PrivilegedRunRequest is one invocation of one declared command.
type PrivilegedRunRequest struct {
	// Name is the key under Config.PrivilegedCommands, and the directory
	// results are filed under.
	Name string
	Decl config.PrivilegedCommandDecl
	// RepoRoot is the target repository, where the image entry and the
	// egress allowlist are read from.
	RepoRoot string
	// WorktreeDir is the workspace's live worktree -- snapshotted, never
	// shared: the AI session in the main VM keeps working in it while this
	// runs, and a privileged command must not be able to write into it.
	WorktreeDir string
	// StateDir is the workspace's state directory, where the run's results
	// are filed so the main VM can read them over its existing share.
	StateDir string
}

// PrivilegedRunResult is what the host learns about a finished run.
type PrivilegedRunResult struct {
	RunID string
	// Dir is the run's results directory on the host; GuestDir is the same
	// directory as the *main* VM sees it, which is the path worth handing
	// to the AI session.
	Dir      string
	GuestDir string
	ExitCode int
	Log      string
	// TimedOut reports the host's backstop firing, not the guest's own
	// timeout -- the latter comes back as an ordinary non-zero exit code.
	TimedOut bool
	// Outputs lists what was collected, relative to the run directory's
	// outputs/ subdirectory.
	Outputs []string
	// OutputsError explains why collection produced less than the
	// declaration asked for (a cap was hit, a path was unusable). Reported
	// alongside the result rather than replacing it: the command itself may
	// well have succeeded, and its log is still worth having.
	OutputsError string
}

// RunPrivilegedCommand builds, boots, waits for, and tears down one
// disposable VM. It blocks for the length of the run.
func RunPrivilegedCommand(req PrivilegedRunRequest) (PrivilegedRunResult, error) {
	if err := config.ValidateImageEntry(req.Decl.Image); err != nil {
		return PrivilegedRunResult{}, err
	}
	imageTag, imageCfg, err := resolveImageEntry(req.RepoRoot, req.Decl.Image)
	if err != nil {
		return PrivilegedRunResult{}, err
	}
	if _, err := exec.LookPath(cloudHypervisorBinary); err != nil {
		return PrivilegedRunResult{}, fmt.Errorf("%s not found on PATH: %w", cloudHypervisorBinary, err)
	}
	kernelPath, err := findKernel()
	if err != nil {
		return PrivilegedRunResult{}, err
	}
	username, err := currentUsername()
	if err != nil {
		return PrivilegedRunResult{}, err
	}

	runID, runDir, err := newPrivilegedRunDir(req.StateDir, req.Name)
	if err != nil {
		return PrivilegedRunResult{}, err
	}
	if err := stageRunRequest(runDir, req.Decl); err != nil {
		return PrivilegedRunResult{}, err
	}

	workDir, err := privilegedWorkDir(runID)
	if err != nil {
		return PrivilegedRunResult{}, err
	}
	// Everything under workDir is scaffolding -- the workspace copy and the
	// rootfs image -- and none of it outlives the run. The results the
	// caller keeps live under runDir instead.
	defer os.RemoveAll(workDir)

	snapshotDir := filepath.Join(workDir, "workspace")
	if err := snapshotWorktree(req.WorktreeDir, snapshotDir); err != nil {
		return PrivilegedRunResult{}, err
	}

	rootfsPath := filepath.Join(workDir, "rootfs.img")
	if err := buildPrivilegedRootfs(imageTag, rootfsPath, kernelPath, imageCfg.RootfsSizeMiB); err != nil {
		return PrivilegedRunResult{}, err
	}

	if err := bootPrivilegedVM(runID, req.RepoRoot, workDir, snapshotDir, runDir, rootfsPath, kernelPath, username, hostTimeout(req.Decl)); err != nil {
		// A timed-out run still has whatever the guest managed to write, so
		// it is reported as a result, not swallowed as an error.
		if _, timedOut := err.(privilegedTimeoutError); !timedOut {
			return PrivilegedRunResult{}, err
		}
		result := readRunResult(runDir, runID, req)
		result.TimedOut = true
		collectInto(&result, snapshotDir, runDir, req.Decl.Outputs)
		return result, nil
	}
	result := readRunResult(runDir, runID, req)
	collectInto(&result, snapshotDir, runDir, req.Decl.Outputs)
	return result, nil
}

// collectInto runs the declared collection and records its outcome on the
// result. Collection failures never turn into an error from
// RunPrivilegedCommand: the run happened, and the exit code and log are
// worth returning either way.
//
// Collected even on the timeout path, where the command was killed
// mid-flight and an artifact may well be half-written: the alternative is
// discarding evidence of what a stuck command had produced so far, and the
// caller already knows the run timed out.
func collectInto(result *PrivilegedRunResult, snapshotDir, runDir string, outputs []string) {
	collected, err := collectOutputs(snapshotDir, runDir, outputs)
	result.Outputs = collected
	if err != nil {
		result.OutputsError = err.Error()
	}
}

// collectOutputs copies each declared path out of the workspace snapshot
// into the run directory. The snapshot is a host directory the host itself
// made, and by this point the VM is gone -- so this is a plain local copy,
// not a transfer.
//
// Nothing here ever follows a symlink. The snapshot's contents were written
// by a command running as root in a VM that is not trusted, so a link
// pointing at /etc/shadow must not turn into masuda reading /etc/shadow:
// entries that are not regular files or directories are skipped, including
// the declared path itself when it is a link.
func collectOutputs(snapshotDir, runDir string, outputs []string) ([]string, error) {
	if len(outputs) == 0 {
		return nil, nil
	}
	outputsDir := filepath.Join(runDir, outputsDirName)
	// The guest had this directory mounted read-write and may have created
	// something at this path itself; what masuda reports as collected has
	// to be what masuda collected.
	if err := os.RemoveAll(outputsDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(outputsDir, 0o755); err != nil {
		return nil, err
	}

	var collected []string
	var totalBytes int64
	var skipped []string
	for _, declared := range outputs {
		if err := config.ValidateOutputPath(declared); err != nil {
			return collected, err
		}
		rel := filepath.Clean(declared)
		src := filepath.Join(snapshotDir, rel)
		info, err := os.Lstat(src)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (not produced)", declared))
			continue
		}
		switch {
		case info.Mode().IsRegular():
			if err := copyCollected(src, outputsDir, rel, info, &totalBytes, &collected); err != nil {
				return collected, err
			}
		case info.IsDir():
			if err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				entryInfo, err := entry.Info()
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				if !entryInfo.Mode().IsRegular() {
					skipped = append(skipped, path[len(snapshotDir)+1:]+" (not a regular file)")
					return nil
				}
				dstRel, err := filepath.Rel(snapshotDir, path)
				if err != nil {
					return err
				}
				return copyCollected(path, outputsDir, dstRel, entryInfo, &totalBytes, &collected)
			}); err != nil {
				return collected, err
			}
		default:
			skipped = append(skipped, declared+" (not a regular file or directory)")
		}
	}
	if len(skipped) > 0 {
		return collected, fmt.Errorf("not collected: %s", strings.Join(skipped, "; "))
	}
	return collected, nil
}

// copyCollected copies one file, enforcing the per-run caps as it goes.
// Exceeding a cap is an error rather than a silent stop: a coverage report
// cut in half is not a smaller result, it is a broken file, and a caller
// that believes it collected everything would act on it.
func copyCollected(src, outputsDir, rel string, info os.FileInfo, totalBytes *int64, collected *[]string) error {
	if len(*collected)+1 > maxCollectedOutputFiles {
		return fmt.Errorf("more than %d files to collect", maxCollectedOutputFiles)
	}
	if *totalBytes+info.Size() > maxCollectedOutputBytes {
		return fmt.Errorf("more than %d bytes to collect", maxCollectedOutputBytes)
	}
	dst := filepath.Join(outputsDir, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return err
	}
	*totalBytes += info.Size()
	*collected = append(*collected, rel)
	return nil
}

type privilegedTimeoutError struct{}

func (privilegedTimeoutError) Error() string {
	return "privileged command VM did not power off in time"
}

// hostTimeout is the wall-clock backstop for one run: the declared timeout
// (or masuda's default) plus enough room for boot and shutdown.
func hostTimeout(decl config.PrivilegedCommandDecl) time.Duration {
	if decl.TimeoutSeconds > 0 {
		return time.Duration(decl.TimeoutSeconds)*time.Second + privilegedBootSlack
	}
	return defaultPrivilegedTimeout + privilegedBootSlack
}

// newPrivilegedRunDir allocates this run's results directory. Runs are never
// overwritten or auto-deleted (ADR-0053): two runs of the same command must
// both remain readable, so the directory name is a fresh random id rather
// than anything derived from the command or its inputs.
func newPrivilegedRunDir(stateDir, name string) (string, string, error) {
	parent := filepath.Join(stateDir, PrivilegedRunsDirName, name)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", "", err
	}
	for range 100 {
		id, err := workspace.NewRandomID()
		if err != nil {
			return "", "", err
		}
		dir := filepath.Join(parent, id)
		if err := os.Mkdir(dir, 0o755); err == nil {
			return id, dir, nil
		} else if !os.IsExist(err) {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("allocating a run directory under %s: too many collisions", parent)
}

// stageRunRequest writes what the guest-side runner reads (runtime/
// masuda-run.sh). The command reaches the guest as a file in the results
// share rather than on the kernel command line: a command line is a single
// space-separated string with its own quoting rules, and what is staged
// here is an arbitrary shell command from a declaration.
func stageRunRequest(runDir string, decl config.PrivilegedCommandDecl) error {
	files := map[string]string{
		"command":         decl.Command,
		"timeout-seconds": strconv.Itoa(decl.TimeoutSeconds),
		"max-log-bytes":   strconv.Itoa(maxPrivilegedLogBytes),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(runDir, name), []byte(content+"\n"), 0o644); err != nil {
			return fmt.Errorf("staging %s for the privileged VM: %w", name, err)
		}
	}
	return nil
}

func privilegedWorkDir(runID string) (string, error) {
	dataHome, err := workspace.DataHome()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(dataHome, "vm", "privileged-"+runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// snapshotWorktree copies the workspace tree the command will see. A copy,
// not a share: the main VM's AI session goes on editing the real worktree
// while this runs, and a privileged command's writes -- deliberate or not --
// have to stay inside something disposable.
func snapshotWorktree(worktreeDir, snapshotDir string) error {
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return err
	}
	out, err := exec.Command("cp", "-a", worktreeDir+"/.", snapshotDir+"/").CombinedOutput()
	if err != nil {
		return fmt.Errorf("snapshotting %s: %w\n%s", worktreeDir, err, out)
	}
	return nil
}

// buildPrivilegedRootfs converts the declared image entry into a bootable
// rootfs, injecting what masuda -- not the project's Dockerfile -- is
// responsible for: the runner, and the guest kernel's module tree.
//
// The whole module tree, not just virtiofs.ko as VMBackend injects for the
// main VM: a distribution kernel keeps overlay, bridge, veth and the
// netfilter chains as modules, and a Docker daemon needs all of them (see
// ADR-0053 -- confirmed live, dockerd otherwise fails to start at all).
// Which modules a given command's workload will reach for is not knowable
// in advance, so the tree goes in whole.
func buildPrivilegedRootfs(imageTag, rootfsPath, kernelPath string, minSizeMiB int) error {
	kernelVersion := kernelVersionFromPath(kernelPath)
	modulesDir := filepath.Join("/lib/modules", kernelVersion)
	if _, err := os.Stat(modulesDir); err != nil {
		return fmt.Errorf("no kernel module tree at %s -- is linux-modules-%s installed?: %w", modulesDir, kernelVersion, err)
	}
	return rootfs.Build(imageTag, rootfsPath, rootfs.Options{
		ExtraFiles: []rootfs.ExtraFile{
			{GuestPath: "usr/local/bin/masuda-run", Content: masuda.PrivilegedRunner, Mode: 0o755, UID: 0, GID: 0},
			{GuestPath: "etc/systemd/system/masuda-run.service", Content: masuda.PrivilegedRunnerUnit, Mode: 0o644, UID: 0, GID: 0},
		},
		// usr/lib/..., not lib/...: an Ubuntu image is usrmerged, so /lib is
		// a symlink and copying a real directory onto it fails (see
		// virtiofsModuleExtraFile's own comment).
		ExtraDirs: []rootfs.ExtraDir{{HostPath: modulesDir, GuestPath: filepath.Join("usr/lib/modules", kernelVersion)}},
		// What `systemctl enable` would have written, had there been a
		// running systemd at image build time to run it. Not systemd.wants=
		// on the kernel command line: systemd in this guest reports
		// "Detected virtualization docker" (docker export carries
		// /.dockerenv along) and a systemd that believes it is containerized
		// ignores systemd.* options entirely -- see rootfs.ExtraSymlink.
		ExtraSymlinks: []rootfs.ExtraSymlink{{
			GuestPath: "etc/systemd/system/multi-user.target.wants/masuda-run.service",
			Target:    "/etc/systemd/system/masuda-run.service",
		}},
		MinSizeMiB: minSizeMiB,
	})
}

// bootPrivilegedVM starts the VM and blocks until it powers itself off (the
// runner's last act) or the backstop fires. Everything it allocates is
// released before it returns, including on the timeout path.
func bootPrivilegedVM(runID, repoRoot, workDir, snapshotDir, runDir, rootfsPath, kernelPath, username string, timeout time.Duration) error {
	netID := privilegedNetID(runID)
	tapName, err := EnsureTap(netID, vmBridge, username)
	if err != nil {
		return fmt.Errorf("allocating the disposable VM's network interface: %w", err)
	}
	defer func() { _ = ReleaseTap(netID) }()

	mac := MACFor(netID)
	// Without this the egress proxy has no way to tell which repository's
	// allowlist applies: it resolves a client IP through the DHCP lease to a
	// MAC, and then to a *workspace* -- and this VM is not one
	// (NewEgressAllowlistFunc denies anything it cannot place).
	if err := registerPrivilegedVM(mac, repoRoot); err != nil {
		return err
	}
	defer func() { _ = unregisterPrivilegedVM(mac) }()

	if err := EnsureEgressProxy(); err != nil {
		return fmt.Errorf("ensuring egress-proxy is running: %w", err)
	}

	wsVF, err := StartVirtiofs(snapshotDir, filepath.Join(workDir, "virtiofs-workspace.sock"), filepath.Join(workDir, "virtiofs-workspace.log"))
	if err != nil {
		return fmt.Errorf("starting virtiofsd for /workspace: %w", err)
	}
	defer func() { _ = wsVF.Stop() }()

	resultsVF, err := StartVirtiofs(runDir, filepath.Join(workDir, "virtiofs-results.sock"), filepath.Join(workDir, "virtiofs-results.log"))
	if err != nil {
		return fmt.Errorf("starting virtiofsd for /masuda-results: %w", err)
	}
	defer func() { _ = resultsVF.Stop() }()

	cmd := exec.Command(cloudHypervisorBinary,
		"--kernel", kernelPath,
		"--disk", "path="+rootfsPath+",readonly=off,image_type=raw",
		"--fs",
		"tag=workspace,socket="+wsVF.SocketPath,
		"tag=masuda-results,socket="+resultsVF.SocketPath,
		"--net", "tap="+tapName+",mac="+mac,
		"--cpus", vmCPUs,
		"--memory", "size="+vmMemorySize+",shared=on",
		"--cmdline", "console=ttyS0 root=/dev/vda rw",
		"--console", "off",
		"--serial", "file="+filepath.Join(runDir, "console.log"),
	)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting cloud-hypervisor: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("cloud-hypervisor exited abnormally: %w", err)
		}
		return nil
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return privilegedTimeoutError{}
	}
}

// privilegedNetID keys this run's TAP device and MAC. Deliberately derived
// from the run id alone rather than the workspace: the workspace's own VM is
// still running (that is where the request came from), so reusing its
// identity would put two interfaces with the same name and MAC on the same
// bridge. Linux caps interface names at 15 bytes, which "tap-p" plus a
// 6-character id fits.
func privilegedNetID(runID string) string { return "p" + runID }

// readRunResult reads back whatever the guest wrote. A missing or unreadable
// exit code is reported as one rather than as an error: the run happened,
// and the log is usually the only thing that can explain what went wrong.
func readRunResult(runDir, runID string, req PrivilegedRunRequest) PrivilegedRunResult {
	result := PrivilegedRunResult{
		RunID:    runID,
		Dir:      runDir,
		GuestDir: filepath.Join("/masuda-state", PrivilegedRunsDirName, req.Name, runID),
		ExitCode: -1,
	}
	if raw, err := os.ReadFile(filepath.Join(runDir, "exit-code")); err == nil {
		if code, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
			result.ExitCode = code
		}
	}
	if log, err := os.ReadFile(filepath.Join(runDir, "log")); err == nil {
		result.Log = string(log)
	}
	return result
}
