package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/sandbox"
	"github.com/TadahiroYamamura/masuda/internal/workflow/engine"
	"github.com/TadahiroYamamura/masuda/internal/workflow/hostenv"
	"github.com/TadahiroYamamura/masuda/internal/workflow/snapshot"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// checkFeedbackBytes bounds how much of a failing check's log is handed to
// the next node as feedback. The whole log stays on disk; the tail is
// usually where a test runner reports what failed.
const checkFeedbackBytes = 4000

// defaultCheckTimeout applies when a check declares none.
const defaultCheckTimeout = 30 * time.Minute

// checkRunner runs the checks fixed for this run (ADR-0071). A `command`
// check runs in the workspace's VM over SSH, never on the host: it runs
// the repository's code. A `privilegedCommand` check goes through the
// approved disposable-VM path, so its approval and hash pinning apply
// unchanged.
func checkRunner(id, repoRoot, worktreeDir, stateDir string, store engine.Store) func(string) (bool, string, error) {
	return func(name string) (bool, string, error) {
		var checks map[string]config.CheckDecl
		if err := snapshot.LoadChecks(store, &checks); err != nil {
			return false, "", err
		}
		decl, ok := checks[name]
		if !ok {
			return false, "", fmt.Errorf("check %q is not declared in the settings this run started with", name)
		}
		timeout := defaultCheckTimeout
		if decl.TimeoutSeconds > 0 {
			timeout = time.Duration(decl.TimeoutSeconds) * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		trusted, err := workspace.TrustedDir(id)
		if err != nil {
			return false, "", err
		}
		env := &hostenv.Env{StateDir: stateDir, TrustedDir: trusted}
		var exitCode int
		var log []byte
		if decl.PrivilegedCommand != "" {
			res, err := privilegedRunner(repoRoot, worktreeDir, stateDir)(ctx, decl.PrivilegedCommand)
			if err != nil {
				return false, "", err
			}
			exitCode, log = res.ExitCode, []byte(res.Log)
		} else {
			args, err := sandbox.VMExecArgs(id, "cd /workspace && bash -lc "+shellQuote(decl.Command))
			if err != nil {
				return false, "", err
			}
			cmd := exec.CommandContext(ctx, args[0], args[1:]...)
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			err = cmd.Run()
			log = out.Bytes()
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else if err != nil {
				return false, "", fmt.Errorf("check %s: %w", name, err)
			}
			if ctx.Err() == context.DeadlineExceeded {
				exitCode = -1
				log = append(log, []byte(fmt.Sprintf("\n(timed out after %s)\n", timeout))...)
			}
		}
		logPath, err := env.Put(filepath.Join("checks", fmt.Sprintf("%s-%d.log", name, time.Now().UnixNano())), log)
		if err != nil {
			return false, "", err
		}
		if exitCode == 0 {
			return true, "", nil
		}
		tail := log
		if len(tail) > checkFeedbackBytes {
			tail = tail[len(tail)-checkFeedbackBytes:]
		}
		return false, checkFeedback(name, exitCode, guestStatePath(stateDir, logPath), tail), nil
	}
}

func checkFeedback(name string, exitCode int, guestLog string, tail []byte) string {
	return fmt.Sprintf("check %s が失敗した（終了コード %d）。全文のログ: %s\n\nログの末尾:\n```\n%s\n```\n", name, exitCode, guestLog, tail)
}

// guestStatePath is where the guest sees p, a path under the state
// directory.
func guestStatePath(stateDir, p string) string {
	return path.Join(sandbox.GuestStateDir, filepath.ToSlash(strings.TrimPrefix(p, stateDir+"/")))
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
