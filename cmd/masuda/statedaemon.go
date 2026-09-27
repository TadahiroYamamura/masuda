package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/sandbox"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpaggregator"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpserver"
	"github.com/TadahiroYamamura/masuda/internal/workflow/host"
	"github.com/TadahiroYamamura/masuda/internal/workflow/hostenv"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
	"github.com/TadahiroYamamura/masuda/internal/worktree"
)

// daemonPIDName/daemonLogName/daemonStoreDirName are the well-known
// filenames a workspace's state directory holds for its statedaemon process
// (Issue #35), besides its socket (statedaemon.SocketPath). store/ is where
// the KV data itself is persisted, kept in its own subdirectory so it
// doesn't mix with workspace.json and the other plain files
// internal/workspace already owns there.
const (
	daemonPIDName      = "daemon.pid"
	daemonLogName      = "daemon.log"
	daemonStoreDirName = "store"
)

// runStatedaemon opens the store under stateDir and serves both its trusted
// (full) and curated (Claude-facing) tool sets, each over its own UDS
// socket, until ctx is cancelled. Extracted from the cobra RunE so it's
// directly testable without spawning a subprocess.
//
// repoRoot lets the curated server aggregate child MCP servers declared in
// repoRoot's .masuda/settings.json and approved in
// .masuda/settings.local.json (internal/statedaemon/mcpaggregator, Issue
// #35). repoRoot == "" disables the aggregator entirely -- the standalone/
// pytest-fixture use of this command (see newInternalStatedaemonCommand's
// --state-dir doc comment) has no associated repository to read either
// file from.
func runStatedaemon(ctx context.Context, stateDir, storeDir, repoRoot, worktreeDir string) error {
	if storeDir == "" {
		storeDir = filepath.Join(stateDir, daemonStoreDirName)
	}
	store, err := statedaemon.Open(storeDir)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Built once, up front, so both ServeCuratedServerUDS and
	// mcpaggregator.Start (which registers proxy tools onto it as child
	// servers become ready) share the same *mcp.Server instance.
	// The privileged-command tool needs a workspace to act on: a repository
	// to read the declaration and its approval from, and the worktree to
	// snapshot into the disposable VM (ADR-0053). Without both, the tool
	// stays unregistered rather than being offered and always failing.
	var runPrivileged mcpserver.PrivilegedRunner
	if repoRoot != "" && worktreeDir != "" {
		runPrivileged = privilegedRunner(repoRoot, worktreeDir, stateDir)
	}
	var curated *mcp.Server
	if repoRoot != "" && worktreeDir != "" {
		env := workflowEnv(stateDir, repoRoot, worktreeDir)
		env.RunCheckFn = checkRunner(env.WorkspaceID, repoRoot, worktreeDir, stateDir, store)
		env.Teardown = func() error {
			go teardownWorkspace(env.WorkspaceID, repoRoot, env.Branch, cancel)
			return nil
		}
		statusFile := ""
		if trusted, err := workspace.TrustedDir(env.WorkspaceID); err == nil {
			statusFile = filepath.Join(trusted, workspace.StatusFileName)
		}
		curated = mcpserver.NewCurated(store, runPrivileged, &host.Lazy{Store: store, Env: env, StatusFile: statusFile})
	} else {
		curated = mcpserver.NewCurated(store, runPrivileged)
	}

	errCh := make(chan error, 2)
	go func() { errCh <- mcpserver.ServeUDS(ctx, store, statedaemon.SocketPath(stateDir)) }()
	go func() {
		errCh <- mcpserver.ServeCuratedServerUDS(ctx, curated, statedaemon.CuratedSocketPath(stateDir))
	}()

	var agg *mcpaggregator.Aggregator
	if repoRoot != "" {
		agg = mcpaggregator.Start(ctx, repoRoot, curated, stateDir)
	}

	// Whichever listener stops first (a real error, or ctx cancellation)
	// triggers the other to stop too, so one socket failing doesn't leave
	// the other running forever as an orphan.
	first := <-errCh
	cancel()
	if agg != nil {
		agg.Close()
	}
	second := <-errCh
	for _, err := range []error{first, second} {
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	return first
}

// newInternalCommand groups plumbing commands masuda spawns for itself
// (e.g. the detached state daemon process) rather than ones a user is meant
// to type.
func newInternalCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "internal",
		Hidden: true,
		Short:  "Internal plumbing commands, not part of the public CLI surface",
	}
	cmd.AddCommand(newInternalStatedaemonCommand())
	cmd.AddCommand(newInternalMCPRelayCommand())
	cmd.AddCommand(newInternalRootfsCommand())
	return cmd
}

func newInternalStatedaemonCommand() *cobra.Command {
	var stateDir, storeDir, repoRoot, worktreeDir string
	cmd := &cobra.Command{
		Use:    "statedaemon",
		Hidden: true,
		Short:  "Run a state daemon in the foreground (normally spawned detached by workspace create)",
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if stateDir == "" {
				return fmt.Errorf("--state-dir is required")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			err := runStatedaemon(ctx, stateDir, storeDir, repoRoot, worktreeDir)
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		},
	}
	// A directory, not a workspace ID: internal/workspace's registry plays
	// no part here, so this same command doubles as a standalone daemon for
	// orchestrator/tests' pytest fixtures (an arbitrary tmp_path, no
	// workspace.Create involved) as well as the real per-workspace process
	// startDaemon spawns.
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "directory to persist state under and serve (required)")
	cmd.Flags().StringVar(&storeDir, "store-dir", "",
		"where the key/value store lives; a real workspace passes its host-only trusted directory (default: <state-dir>/store)")
	// Optional: omitting it (the pytest-fixture case above) disables the
	// child-MCP-server aggregator rather than erroring, since those
	// fixtures have no real target repository to read
	// settings(.local).json from.
	cmd.Flags().StringVar(&repoRoot, "repo-root", "",
		"target repository root to read .masuda/settings.json + settings.local.json's child MCP server declarations from (optional; omit to disable the aggregator)")
	// Optional for the same reason as --repo-root: without a worktree to
	// snapshot there is nothing for a privileged command to run against, so
	// that tool stays unoffered rather than failing on every call.
	cmd.Flags().StringVar(&worktreeDir, "worktree-dir", "",
		"this workspace's clone, snapshotted into the disposable VM a privileged command runs in (optional; omit to disable run_privileged_command)")
	return cmd
}

// privilegedRunner adapts internal/sandbox's disposable-VM run to the
// callback the curated MCP server takes. The wiring lives here because this
// is the one place that already depends on both packages -- mcpserver
// cannot import internal/sandbox, whose own tests import mcpserver.
func privilegedRunner(repoRoot, worktreeDir, stateDir string) mcpserver.PrivilegedRunner {
	return func(_ context.Context, name string) (mcpserver.PrivilegedRunResult, error) {
		decl, err := sandbox.ResolveApprovedPrivilegedCommand(repoRoot, name)
		if err != nil {
			return mcpserver.PrivilegedRunResult{}, err
		}
		result, err := sandbox.RunPrivilegedCommand(sandbox.PrivilegedRunRequest{
			Name:        name,
			Decl:        decl,
			RepoRoot:    repoRoot,
			WorktreeDir: worktreeDir,
			StateDir:    stateDir,
		})
		if err != nil {
			return mcpserver.PrivilegedRunResult{}, err
		}
		return mcpserver.PrivilegedRunResult{
			ExitCode:     result.ExitCode,
			Log:          result.Log,
			ResultsDir:   result.GuestDir,
			Outputs:      result.Outputs,
			OutputsError: result.OutputsError,
			TimedOut:     result.TimedOut,
		}, nil
	}
}

// startDaemon spawns workspace id's state daemon as a detached background
// process (Setsid, stdout/stderr to daemon.log inside the workspace's state
// directory) and records its PID so stopDaemon can find it later. The
// process must outlive this CLI invocation -- it has to keep running across
// the many short-lived `masuda plan/review approve` etc. invocations that
// follow, the same "fire and forget" shape internal/sandbox.Start uses for
// the sandbox container itself.
//
// Idempotent: a no-op if a daemon for id is already alive (daemonAlive), so
// every entrypoint that needs the daemon running (new workspace creation,
// but also a `masuda plan start <workspace-id>` resume where the daemon may
// have died since -- host reboot, manual kill, a crash) can call this
// unconditionally instead of tracking "did I already start this" itself.
// ensureDaemon is startDaemon behind a variable so tests that exercise a
// command's own behaviour don't have to spawn a real daemon subprocess:
// under `go test`, os.Executable() is the test binary, so startDaemon would
// re-exec the test suite with flags it can't parse and only fail once
// daemonStartupTimeout elapsed. Same reason that timeout is a var rather
// than a const.
var ensureDaemon = startDaemon

func startDaemon(id string) error {
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		return err
	}
	if daemonServing(stateDir) {
		return nil
	}
	if err := reclaimStaleDaemon(stateDir); err != nil {
		return err
	}
	info, err := workspace.Load(id)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(stateDir, daemonLogName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()

	trusted, err := workspace.TrustedDir(id)
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "internal", "statedaemon",
		"--state-dir", stateDir,
		"--store-dir", filepath.Join(trusted, daemonStoreDirName),
		"--repo-root", info.RepoRoot,
		"--worktree-dir", worktree.Dir(info.RepoRoot, id),
	)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stateDir, daemonPIDName), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		return err
	}
	return waitForDaemon(stateDir)
}

// daemonStartupTimeout bounds how long startDaemon waits for the daemon it
// just spawned to start listening. A var, not a const, so the failure path's
// test doesn't have to sit through the whole timeout to reach the assertion
// it cares about (the same reason internal/sandbox keeps resolveMasudaExe a
// var).
var daemonStartupTimeout = 10 * time.Second

// waitForDaemon blocks until the daemon spawned for stateDir accepts on its
// trusted socket.
//
// Without this, startDaemon returns as soon as the process is forked, and
// the very next thing every caller does -- WriteTaskBrief, WriteInstructions'
// neighbours, a gate read -- dials that socket. Losing that race is not
// theoretical: `masuda plan start <branch> --file ...` failed with
// "connect: no such file or directory" against a daemon that was up
// milliseconds later.
//
// A plain dial is enough of a readiness signal: net.Listen on a Unix socket
// binds and listens in one step, so once a connection is accepted by the
// kernel it is queued for the server whether or not Accept has been reached
// yet.
//
// A daemon that died instead of listening would otherwise surface only as a
// timeout, so its log is read back into the error -- that is where the real
// reason lands (a state directory whose path exceeds AF_UNIX's ~108 byte
// sun_path limit, say, which fails at bind with a bare "invalid argument").
func waitForDaemon(stateDir string) error {
	path := statedaemon.SocketPath(stateDir)
	deadline := time.Now().Add(daemonStartupTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", path)
		if err == nil {
			conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	msg := fmt.Sprintf("state daemon did not start listening on %s within %s: %v", path, daemonStartupTimeout, lastErr)
	if log, err := os.ReadFile(filepath.Join(stateDir, daemonLogName)); err == nil && len(bytes.TrimSpace(log)) > 0 {
		msg += "\n" + strings.TrimSpace(string(log))
	}
	return errors.New(msg)
}

// daemonDialTimeout bounds the readiness probe below. A var so tests can
// shorten it, the same reason daemonStartupTimeout is one.
var daemonDialTimeout = time.Second

// daemonServing reports whether a daemon is actually accepting connections
// on stateDir's trusted socket.
//
// This deliberately does not ask "is the recorded PID alive". A PID is the
// wrong question in both directions after a host reboot (Issue #52): the
// socket file survives in the state directory while nothing listens on it,
// and the PID recorded next to it may since have been handed to an unrelated
// process, which a bare signal-0 probe reports as a healthy daemon. The
// caller then skips starting one and the command fails with "connect:
// connection refused" -- exactly the failure this was supposed to prevent.
// What every caller actually needs to know is whether the socket answers,
// so that is what gets asked. internal/sandbox.EnsureEgressProxy settled on
// the same shape for the same reason.
func daemonServing(stateDir string) bool {
	conn, err := net.DialTimeout("unix", statedaemon.SocketPath(stateDir), daemonDialTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// daemonPID reads the PID recorded in stateDir/daemon.pid. A missing or
// unparsable file counts as "no PID" rather than an error: both are the
// "nothing to recover" case every caller here treats the same way.
func daemonPID(stateDir string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(stateDir, daemonPIDName))
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// isDaemonProcess reports whether pid is a live process that is identifiably
// *this* state directory's daemon, by reading its argv out of /proc.
//
// Without this check both of the places that act on daemon.pid are unsafe
// once a PID has been recycled -- and a host reboot recycles every PID at
// once. reclaimStaleDaemon would SIGTERM a stranger's process; stopDaemon
// would do the same on every `review approve` and `workspace remove`. Signal
// 0 cannot tell the difference, since it only answers "does some process
// hold this number".
//
// Linux-only, like the rest of masuda's process handling (the sandbox is a
// Cloud Hypervisor microVM). A /proc that cannot be read is treated as "not
// ours", the conservative direction: the cost is an orphaned daemon, where
// guessing the other way costs an unrelated process a SIGTERM.
func isDaemonProcess(pid int, stateDir string) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	// /proc/<pid>/cmdline is NUL-separated argv, so both markers are matched
	// as whole arguments -- `--state-dir <dir>` passes the directory as its
	// own argument, and a substring match would also accept a different
	// workspace whose path merely starts with this one.
	var sawSubcommand, sawStateDir bool
	for _, arg := range strings.Split(string(data), "\x00") {
		switch arg {
		case "statedaemon":
			sawSubcommand = true
		case stateDir:
			sawStateDir = true
		}
	}
	return sawSubcommand && sawStateDir
}

// daemonStopTimeout bounds how long reclaimStaleDaemon waits for a daemon it
// signalled to actually exit before giving up and starting a replacement.
var daemonStopTimeout = 5 * time.Second

// reclaimStaleDaemon deals with the PID recorded for a state directory whose
// socket is not answering, so that startDaemon can spawn a replacement
// without ending up with two daemons on one store.
//
// That matters because the store has no write arbitration -- "one writer per
// key" is an assumption nothing enforces (Issue #44) -- so a second daemon
// against the same directory is worse than the dead one it was meant to
// replace. Three cases, all ending in "safe to start a new one":
//
//   - no PID recorded, or the process is gone: nothing to do (the reboot case)
//   - the process is alive and is this directory's daemon, but its socket
//     stopped answering: SIGTERM it and wait for it to go
//   - the process is alive but is something else entirely (a recycled PID):
//     leave it alone; the PID file is about to be overwritten anyway
func reclaimStaleDaemon(stateDir string) error {
	pid, ok := daemonPID(stateDir)
	if !ok || !isDaemonProcess(pid, stateDir) {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	deadline := time.Now().Add(daemonStopTimeout)
	for time.Now().Before(deadline) {
		if !isDaemonProcess(pid, stateDir) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("state daemon %d for %s did not exit within %s after SIGTERM", pid, stateDir, daemonStopTimeout)
}

// stopDaemon signals workspace id's state daemon (if one is running) to shut
// down. A missing PID file means the daemon was never started -- true for
// every workspace created before this feature existed -- so that's treated
// as success, not an error, the same way internal/sandbox.Start ignores
// "nothing to remove" from its own cleanup step.
func stopDaemon(id string) error {
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		return err
	}
	pid, ok := daemonPID(stateDir)
	if !ok {
		return nil
	}
	// Same PID-reuse hazard reclaimStaleDaemon guards against, and now on a
	// path that runs every time a workspace ends: `review approve` tears the
	// workspace down (ADR-0005) and stops its daemon on the way, so a
	// recorded PID that has been recycled would take an unrelated process
	// with it.
	if !isDaemonProcess(pid, stateDir) {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

// workflowEnv builds the engine's environment for the workspace whose
// state directory is stateDir.
func workflowEnv(stateDir, repoRoot, worktreeDir string) *hostenv.Env {
	id := filepath.Base(stateDir)
	info, _ := workspace.Load(id)
	env := &hostenv.Env{
		WorkspaceID:  id,
		RepoRoot:     repoRoot,
		Worktree:     worktreeDir,
		StateDir:     stateDir,
		BaseRef:      info.Base,
		Branch:       info.Branch,
		Perspectives: repoPerspectives(repoRoot),
		// runtime/entrypoint.sh points the guest's ~/.claude/projects here.
		TranscriptsDir: filepath.Join(stateDir, "transcripts"),
	}
	if home, err := os.UserHomeDir(); err == nil {
		env.ExportDir = filepath.Join(home, ".masuda", "exports", id)
	}
	return env
}

// repoPerspectives reads review perspectives from the host repository's
// .masuda/reviews/. How perspectives are held is undecided (ADR-0082) and
// the old mechanism is not carried over; this stopgap only lets the
// bundled review workflows run until that is designed.
func repoPerspectives(repoRoot string) func() ([]hostenv.Perspective, error) {
	return func() ([]hostenv.Perspective, error) {
		files, err := filepath.Glob(filepath.Join(repoRoot, config.DirName, "reviews", "*.md"))
		if err != nil {
			return nil, err
		}
		var out []hostenv.Perspective
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			out = append(out, hostenv.Perspective{Name: strings.TrimSuffix(filepath.Base(f), ".md"), Content: string(b)})
		}
		return out, nil
	}
}

// teardownWorkspace removes a workspace after its workflow published or
// discarded it, then stops this daemon. It waits a moment first so the
// engine can record the node's result before the store's directory goes
// away; removing it mid-write would leave a half-recreated directory.
func teardownWorkspace(id, repoRoot, branch string, stop context.CancelFunc) {
	time.Sleep(2 * time.Second)
	if sandboxBackend.IsRunning(id) {
		if err := sandboxBackend.Stop(id); err != nil {
			fmt.Fprintf(os.Stderr, "teardown: stopping the sandbox: %v\n", err)
		}
	}
	if err := worktree.Remove(repoRoot, id, branch, false); err != nil {
		fmt.Fprintf(os.Stderr, "teardown: removing the clone: %v\n", err)
	}
	if err := workspace.Remove(id); err != nil {
		fmt.Fprintf(os.Stderr, "teardown: removing the workspace: %v\n", err)
	}
	stop()
}
