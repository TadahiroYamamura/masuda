// Package sandbox wraps `docker run` for masuda's per-workspace containers. Each
// sandbox bind-mounts exactly one worktree at /workspace and one workspace
// state directory (masuda's own control files — see internal/workspace) at
// /masuda-state, and gets its own container name and host port, so multiple
// sandboxes can run side by side — the /workspace and /masuda-state paths
// are fixed only from inside a given container.
//
// Everything here is keyed by workspace ID, not branch name: two workspaces
// targeting the same branch (roadmap step 7) must get independent
// containers, so the container name can't be derived from the branch alone.
package sandbox

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// DefaultImage is the masuda sandbox image built from the repo's Dockerfile.
	DefaultImage = "masuda-loop"
	// containerClaudePort is the ttyd port exposed inside every sandbox container.
	containerClaudePort = 7682
	// tmuxSession must match runtime/entrypoint.sh's SESSION.
	tmuxSession = "claude-work"
)

// Handle identifies a running sandbox container.
type Handle struct {
	ID            string
	ContainerName string
	HostPort      int
}

var nameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// ContainerName derives the docker container name for a workspace id. Docker
// container names only allow [a-zA-Z0-9_.-]; workspace IDs are already
// sanitized to that set (internal/workspace.NewID), but this stays
// defensive in case that ever changes.
func ContainerName(id string) string {
	return "masuda-" + nameSanitizer.ReplaceAllString(id, "-")
}

func runDocker(args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}

// freePort asks the OS for an unused TCP port. There's an inherent TOCTOU race
// between closing this listener and `docker run` binding the port, but it's an
// acceptable risk for a per-invocation sandbox port, not a long-lived service.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// hostCredentialMounts returns the `-v host:container` bind-mount arguments
// that let the container's `claude` reuse the host's own subscription login
// (ADR-0001 — no per-request API billing) instead of hitting a fresh
// interactive login wizard, which an unattended container can't get past.
//
// Confirmed empirically: without these, a brand-new container stalls at
// "Select login method" — Claude Code has no session at all in there. Only
// these two specific files are mounted, not the whole ~/.claude directory:
// the container's own ~/.claude/CLAUDE.md must stay isolated from the host's
// real one (ADR-0007) and must keep coming from runtime/CLAUDE.md via cp, not
// a shared mount. Read-write, since an OAuth token may refresh mid-session —
// on the same machine, under the same user account, this isn't a trust
// boundary the way it would be for a genuinely separate party.
func hostCredentialMounts() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	paths := []struct{ host, container string }{
		{filepath.Join(home, ".claude", ".credentials.json"), "/home/ubuntu/.claude/.credentials.json"},
		{filepath.Join(home, ".claude.json"), "/home/ubuntu/.claude.json"},
	}
	var mounts []string
	for _, p := range paths {
		if _, err := os.Stat(p.host); err != nil {
			return nil, fmt.Errorf("%s: %w (log into `claude` on this host first)", p.host, err)
		}
		mounts = append(mounts, "-v", p.host+":"+p.container)
	}
	return mounts, nil
}

// Start launches a new sandbox container for workspace id, bind-mounting
// worktreeDir at /workspace and stateDir (masuda's own control files) at
// /masuda-state, places claudeMdPath at ~/.claude/CLAUDE.md inside the
// container before the container's own entrypoint (and therefore Claude)
// starts, then starts it.
//
// A create → cp → start sequence is used instead of a single `docker run` so the
// CLAUDE.md copy always lands before runtime/entrypoint.sh launches Claude —
// `docker run` would start the entrypoint immediately, racing the copy.
func Start(id, worktreeDir, stateDir, claudeMdPath, image string) (Handle, error) {
	if image == "" {
		image = DefaultImage
	}
	name := ContainerName(id)

	if IsRunning(id) {
		port, err := runningHostPort(name)
		if err != nil {
			return Handle{}, err
		}
		return Handle{ID: id, ContainerName: name, HostPort: port}, nil
	}

	// A previous run's container may still exist in the "Exited" state (its
	// tmux session ended on its own, but `docker create`/`start` don't clean
	// up after themselves) — docker create fails on a name conflict
	// otherwise. Confirmed empirically when resuming after a review
	// rejection. Ignore the error: there may be nothing to remove.
	_, _ = runDocker("rm", "-f", name)

	port, err := freePort()
	if err != nil {
		return Handle{}, fmt.Errorf("allocating host port: %w", err)
	}

	credentialMounts, err := hostCredentialMounts()
	if err != nil {
		return Handle{}, fmt.Errorf("locating host Claude Code credentials: %w", err)
	}

	// The loop protocol only re-invokes the orchestrator when TASK.md is
	// *absent*. A workspace arriving here from the phase 1-2 host loop still
	// has that loop's terminal "DONE (G1 approved)" TASK.md sitting in its
	// state dir — without clearing it, this container's fresh session would
	// read that stale file, see "DONE", and exit immediately without ever
	// invoking the phase 4 orchestrator. Same bug and same fix as
	// hostloop.Start's resume case.
	if err := os.Remove(filepath.Join(stateDir, "TASK.md")); err != nil && !os.IsNotExist(err) {
		return Handle{}, fmt.Errorf("clearing stale TASK.md before sandbox start: %w", err)
	}

	createArgs := []string{
		"create",
		"--name", name,
		"-p", fmt.Sprintf("%d:%d", port, containerClaudePort),
		"-v", worktreeDir + ":/workspace",
		"-v", stateDir + ":/masuda-state",
	}
	createArgs = append(createArgs, credentialMounts...)
	createArgs = append(createArgs, image)

	if _, err := runDocker(createArgs...); err != nil {
		return Handle{}, fmt.Errorf("docker create: %w", err)
	}

	if _, err := runDocker("cp", claudeMdPath, name+":/home/ubuntu/.claude/CLAUDE.md"); err != nil {
		_, _ = runDocker("rm", "-f", name)
		return Handle{}, fmt.Errorf("copying CLAUDE.md into container: %w", err)
	}

	if _, err := runDocker("start", name); err != nil {
		_, _ = runDocker("rm", "-f", name)
		return Handle{}, fmt.Errorf("docker start: %w", err)
	}

	return Handle{ID: id, ContainerName: name, HostPort: port}, nil
}

// Stop stops and removes the sandbox container for workspace id. It's not an
// error for the container to already be gone.
func Stop(id string) error {
	name := ContainerName(id)
	_, _ = runDocker("stop", name)
	_, err := runDocker("rm", "-f", name)
	return err
}

// runningHostPort returns the host port currently mapped to a running
// container's ttyd port, so Start can report it on a no-op resume.
func runningHostPort(name string) (int, error) {
	out, err := runDocker("port", name, fmt.Sprintf("%d/tcp", containerClaudePort))
	if err != nil {
		return 0, fmt.Errorf("inspecting running container's port: %w", err)
	}
	// e.g. "0.0.0.0:45355\n[::]:45355\n" -- take the first mapping's port.
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	idx := strings.LastIndex(line, ":")
	if idx < 0 {
		return 0, fmt.Errorf("unexpected `docker port` output: %q", out)
	}
	var port int
	if _, err := fmt.Sscanf(line[idx+1:], "%d", &port); err != nil {
		return 0, fmt.Errorf("parsing port from %q: %w", line, err)
	}
	return port, nil
}

// IsRunning reports whether the sandbox container for workspace id is
// currently running.
func IsRunning(id string) bool {
	out, err := runDocker("inspect", "--format", "{{.State.Running}}", ContainerName(id))
	return err == nil && strings.TrimSpace(out) == "true"
}

// AttachArgs returns the argv for interactively attaching to the sandbox's tmux
// session (`masuda plan/review chat`). Callers exec this directly (not via
// exec.Command's Output/Run) so the user's terminal is wired straight through.
func AttachArgs(id string) []string {
	return []string{"docker", "exec", "-it", ContainerName(id), "tmux", "attach", "-t", tmuxSession}
}
