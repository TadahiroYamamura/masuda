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

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/hostloop"
	"github.com/TadahiroYamamura/masuda/internal/sandbox"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpaggregator"
	"github.com/TadahiroYamamura/masuda/internal/statedaemon/mcpserver"
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

// orchestratorTaskFileName is the file implement_review_graph.py writes its
// rendered task into, relative to the state directory (TASK_MD there).
const orchestratorTaskFileName = "TASK.md"

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
func runStatedaemon(ctx context.Context, stateDir, repoRoot, worktreeDir string) error {
	store, err := statedaemon.Open(filepath.Join(stateDir, daemonStoreDirName))
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
	// The Build/Review orchestrator runs here, on the host, not in the
	// guest: it is masuda's own control code (the state machine, the
	// budget, the step commits), and the only reason it ever lived inside
	// the sandbox was that the phase 4-5 session invoked it directly.
	// Needs the worktree to run git against; the state directory it reads
	// and writes through is this daemon's own.
	var runOrchestrator mcpserver.OrchestratorRunner
	if worktreeDir != "" {
		runOrchestrator = orchestratorRunner(worktreeDir, stateDir)
	}
	curated := mcpserver.NewCurated(store, runPrivileged, runOrchestrator)

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
	cmd.AddCommand(newInternalStateCommand())
	cmd.AddCommand(newInternalMCPRelayCommand())
	cmd.AddCommand(newInternalRootfsCommand())
	return cmd
}

func newInternalStatedaemonCommand() *cobra.Command {
	var stateDir, repoRoot, worktreeDir string
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
			err := runStatedaemon(ctx, stateDir, repoRoot, worktreeDir)
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

// orchestratorRunner builds the OrchestratorRunner the curated next_task
// tool calls: one turn of implement_review_graph.py against worktreeDir,
// then the TASK.md it wrote.
func orchestratorRunner(worktreeDir, stateDir string) mcpserver.OrchestratorRunner {
	return func(ctx context.Context) (string, error) {
		python, script, err := hostloop.EnsureImplementReviewOrchestrator()
		if err != nil {
			return "", fmt.Errorf("preparing masuda's own host-side python runtime: %w", err)
		}
		return runOrchestratorTurn(ctx, python, script, worktreeDir, stateDir)
	}
}

// runOrchestratorTurn runs one detect_phase -> write_task_md turn and
// returns the task text it produced.
//
// cwd is worktreeDir because every git command in implement_review_graph.py
// is relative to it, and MASUDA_STATE_DIR is where that script resolves all
// of masuda's own control files from -- the same two inputs it got inside
// the sandbox, just pointing at the host's side of the same virtiofs share.
//
// Split out from orchestratorRunner so it can be tested against a stub
// interpreter, without a venv.
func runOrchestratorTurn(ctx context.Context, python, script, worktreeDir, stateDir string) (string, error) {
	cmd := exec.CommandContext(ctx, python, script)
	cmd.Dir = worktreeDir
	cmd.Env = append(os.Environ(),
		"MASUDA_STATE_DIR="+stateDir,
		// The orchestrator opens files at the host path above, but the
		// paths it writes into prompts have to be the ones the guest
		// session it instructs can open -- the same directory, reached
		// through that VM's virtiofs mount.
		"MASUDA_GUEST_STATE_DIR="+sandbox.GuestStateDir,
	)
	// Combined, and only used to explain a failure: the orchestrator's own
	// chatter is not something the calling session needs on success.
	var output strings.Builder
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("running %s: %w\n%s", script, err, output.String())
	}

	task, err := os.ReadFile(filepath.Join(stateDir, orchestratorTaskFileName))
	if err != nil {
		return "", fmt.Errorf("reading the %s the orchestrator should have written: %w", orchestratorTaskFileName, err)
	}
	return string(task), nil
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
func startDaemon(id string) error {
	stateDir, err := workspace.StateDir(id)
	if err != nil {
		return err
	}
	if daemonAlive(stateDir) {
		return nil
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

	cmd := exec.Command(exe, "internal", "statedaemon",
		"--state-dir", stateDir,
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

// daemonAlive reports whether the PID recorded in stateDir/daemon.pid
// belongs to a live process, using signal 0 (POSIX's standard existence
// probe: no signal is actually delivered, the call just fails with ESRCH if
// the process is gone). A missing or unparsable PID file counts as not
// alive rather than an error, since both are exactly the "nothing to
// recover" case startDaemon's caller wants to treat the same way.
func daemonAlive(stateDir string) bool {
	data, err := os.ReadFile(filepath.Join(stateDir, daemonPIDName))
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
	data, err := os.ReadFile(filepath.Join(stateDir, daemonPIDName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return err
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
