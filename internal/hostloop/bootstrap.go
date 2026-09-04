package hostloop

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	masuda "github.com/TadahiroYamamura/masuda"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

const requirementsMarkerName = ".masuda-requirements-sha256"

// The orchestrator scripts extracted into runtimeDir, one per phase pair,
// plus the state-daemon client both import as a sibling module.
const (
	investigatePlanScriptName = "investigate_plan_graph.py"
	implementReviewScriptName = "implement_review_graph.py"
	stateClientScriptName     = "state_client.py"
)

// runtimeDir returns the stable directory masuda extracts its own
// orchestrator script into and builds its own venv under -- a sibling of
// workspace.StateDir's workspaces/ directory, under the same DataHome, so it
// never depends on which target repository the CLI happens to be invoked
// against (see assets.go's package doc for why these assets can't just be
// read from wherever masuda's own checkout lives, if one even exists next to
// the compiled binary at all).
func runtimeDir() (string, error) {
	dh, err := workspace.DataHome()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(dh, "runtime")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// atomicWrite writes data to <dir>/<finalName> via a same-directory temp file
// plus rename, so a concurrent reader never observes a partially written file
// and a crash mid-write never leaves a corrupt final file behind.
func atomicWrite(dir, finalName string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(dir, "."+finalName+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, finalName)); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

func requirementsHash() string {
	sum := sha256.Sum256(masuda.Requirements)
	return hex.EncodeToString(sum[:])
}

// ensureRuntime extracts masuda's embedded orchestrator scripts (always, to
// stay in sync with whichever masuda binary is running) and, if the venv is
// missing or was built against a different requirements.txt, (re)builds it --
// all under runtimeDir(). Returns the venv's python interpreter path and the
// directory the scripts were extracted into.
//
// Both phase pairs' scripts are extracted by the same call rather than one
// each on demand: they share the venv and the state_client.py sibling, so
// splitting them would mean two callers racing to build the same venv for
// no gain.
func ensureRuntime() (pythonPath, dir string, err error) {
	dir, err = runtimeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolving masuda's own runtime directory: %w", err)
	}

	scripts := []struct {
		name    string
		content []byte
	}{
		{investigatePlanScriptName, masuda.InvestigatePlanScript},
		{implementReviewScriptName, masuda.ImplementReviewScript},
		// Both orchestrators import state_client as a sibling module
		// (orchestrator/state_client.py, ADR-0040's state-daemon client) --
		// extracted alongside them into the same runtime dir for that import
		// to resolve. The sandbox image never needed this: it COPYs the whole
		// orchestrator/ directory rather than embedding individual scripts.
		{stateClientScriptName, masuda.StateClientScript},
	}
	for _, s := range scripts {
		if err := atomicWrite(dir, s.name, s.content, 0o644); err != nil {
			return "", "", fmt.Errorf("extracting %s: %w", s.name, err)
		}
	}

	venvDir := filepath.Join(dir, "venv")
	pythonPath = filepath.Join(venvDir, "bin", "python")
	markerPath := filepath.Join(dir, requirementsMarkerName)
	wantHash := requirementsHash()

	upToDate := false
	if haveHash, err := os.ReadFile(markerPath); err == nil && string(haveHash) == wantHash {
		if _, err := os.Stat(pythonPath); err == nil {
			upToDate = true
		}
	}
	if upToDate {
		return pythonPath, dir, nil
	}

	if err := buildVenv(dir, venvDir, wantHash, markerPath); err != nil {
		return "", "", err
	}
	return pythonPath, dir, nil
}

// EnsureImplementReviewOrchestrator prepares the host-side runtime and
// returns what it takes to run one Build/Review orchestrator turn:
// the venv's python and implement_review_graph.py's path.
//
// Exported from this package, whose own subject is the phase 1-2 loop,
// because the runtime it bootstraps (the venv, the extracted scripts) is
// the same one either phase pair needs -- the Build/Review orchestrator
// runs on the host as well, invoked through the state daemon rather than
// by the guest. If a third caller appears, this bootstrap deserves its own
// package instead.
func EnsureImplementReviewOrchestrator() (pythonPath, scriptPath string, err error) {
	pythonPath, dir, err := ensureRuntime()
	if err != nil {
		return "", "", err
	}
	return pythonPath, filepath.Join(dir, implementReviewScriptName), nil
}

// buildVenv (re)creates the venv at venvDir from masuda's embedded
// requirements.txt. It builds into a temporary sibling directory first and
// renames it into place only on full success, so a failed or interrupted
// install is never mistaken for a valid, ready-to-use venv on the next
// invocation (matching the Dockerfile's own `python3 -m venv venv &&
// venv/bin/pip install --no-cache-dir -r requirements.txt` sequence, just
// with the temp+rename safety net a repeatable host-side bootstrap needs and
// a one-shot Docker image build doesn't).
func buildVenv(dir, venvDir, wantHash, markerPath string) error {
	if _, err := exec.LookPath("python3"); err != nil {
		return fmt.Errorf("python3 not found in PATH -- masuda's phase 1-2 host loop needs a host Python 3 interpreter (see docs/INSTALLATION.md): %w", err)
	}

	fmt.Fprintln(os.Stderr, "[masuda] Python依存関係を初期セットアップ中…")

	tmpVenv, err := os.MkdirTemp(dir, "venv.tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp venv directory: %w", err)
	}
	defer os.RemoveAll(tmpVenv)

	if out, err := exec.Command("python3", "-m", "venv", tmpVenv).CombinedOutput(); err != nil {
		return fmt.Errorf("python3 -m venv: %w\n%s", err, out)
	}

	reqPath := filepath.Join(dir, "requirements.txt")
	if err := atomicWrite(dir, "requirements.txt", masuda.Requirements, 0o644); err != nil {
		return fmt.Errorf("writing requirements.txt: %w", err)
	}

	pip := filepath.Join(tmpVenv, "bin", "pip")
	if out, err := exec.Command(pip, "install", "--no-cache-dir", "-r", reqPath).CombinedOutput(); err != nil {
		return fmt.Errorf("%s install -r %s: %w\n%s", pip, reqPath, err, out)
	}

	if err := os.RemoveAll(venvDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing stale venv: %w", err)
	}
	if err := os.Rename(tmpVenv, venvDir); err != nil {
		return fmt.Errorf("moving new venv into place: %w", err)
	}

	if err := os.WriteFile(markerPath, []byte(wantHash), 0o644); err != nil {
		return fmt.Errorf("writing requirements marker: %w", err)
	}
	return nil
}
