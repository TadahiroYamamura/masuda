// Package sandbox wraps `docker run` for masuda's per-worktree containers. Each
// sandbox bind-mounts exactly one worktree at /workspace and gets its own
// container name and host port, so multiple sandboxes can run side by side —
// the /workspace path is fixed only from inside a given container.
package sandbox

import (
	"bytes"
	"fmt"
	"net"
	"os/exec"
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
	Branch        string
	ContainerName string
	HostPort      int
}

var nameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// ContainerName derives the docker container name for a branch. Docker container
// names only allow [a-zA-Z0-9_.-], so anything else in the branch name (e.g. the
// "/" in "feat/foo") is collapsed to "-".
func ContainerName(branch string) string {
	return "masuda-" + nameSanitizer.ReplaceAllString(branch, "-")
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

// Start launches a new sandbox container bind-mounting worktreeDir at /workspace,
// places claudeMdPath at ~/.claude/CLAUDE.md inside the container before the
// container's own entrypoint (and therefore Claude) starts, then starts it.
//
// A create → cp → start sequence is used instead of a single `docker run` so the
// CLAUDE.md copy always lands before runtime/entrypoint.sh launches Claude —
// `docker run` would start the entrypoint immediately, racing the copy.
func Start(branch, worktreeDir, claudeMdPath, image string) (Handle, error) {
	if image == "" {
		image = DefaultImage
	}
	name := ContainerName(branch)
	port, err := freePort()
	if err != nil {
		return Handle{}, fmt.Errorf("allocating host port: %w", err)
	}

	_, err = runDocker(
		"create",
		"--name", name,
		"-p", fmt.Sprintf("%d:%d", port, containerClaudePort),
		"-v", worktreeDir+":/workspace",
		image,
	)
	if err != nil {
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

	return Handle{Branch: branch, ContainerName: name, HostPort: port}, nil
}

// Stop stops and removes the sandbox container for branch. It's not an error for
// the container to already be gone.
func Stop(branch string) error {
	name := ContainerName(branch)
	_, _ = runDocker("stop", name)
	_, err := runDocker("rm", "-f", name)
	return err
}

// IsRunning reports whether the sandbox container for branch is currently running.
func IsRunning(branch string) bool {
	out, err := runDocker("inspect", "--format", "{{.State.Running}}", ContainerName(branch))
	return err == nil && strings.TrimSpace(out) == "true"
}

// AttachArgs returns the argv for interactively attaching to the sandbox's tmux
// session (`masuda plan/review chat`). Callers exec this directly (not via
// exec.Command's Output/Run) so the user's terminal is wired straight through.
func AttachArgs(branch string) []string {
	return []string{"docker", "exec", "-it", ContainerName(branch), "tmux", "attach", "-t", tmuxSession}
}
