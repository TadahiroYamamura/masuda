// Package workspace assigns each masuda run its own unique identity — a
// workspace ID — decoupled from the git branch it targets, and owns the
// per-workspace state directory (outside any git worktree) that all of
// masuda's own control files live in (roadmap step 7).
//
// This exists because the worktree path and sandbox container name used to
// be keyed by branch name alone: two workspaces targeting the same branch
// (a full pipeline run and a `masuda review start` of the same branch, or
// two parallel attempts at the same task) would fight over the same
// worktree directory and container. Workspace IDs make every masuda
// invocation independent, and moving masuda's control files (TASK.md,
// PLAN.md, gate markers, review results, ...) into a directory the target
// repository's git never sees also fixes a real bug found along the way:
// `git add -A` inside the worktree was picking up masuda's own scratch
// files and showing them to review subagents as if they were part of the
// change under review.
package workspace

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"
)

// Info is the metadata masuda persists for a workspace — enough to resolve
// a workspace ID back to the real git branch/base it targets, and which
// repo it belongs to (so `masuda worktree list` can filter to the current
// repo even though the state directory is global, not per-repo).
type Info struct {
	ID        string    `json:"id"`
	Branch    string    `json:"branch"`
	Base      string    `json:"base"`
	RepoRoot  string    `json:"repo_root"`
	CreatedAt time.Time `json:"created_at"`
}

const metadataFileName = "workspace.json"

// BaseRefFileName is the plain-text file (just the base ref name, no JSON)
// written alongside workspace.json — orchestrator/*.py reads this one
// directly rather than parsing the richer Go-side metadata, keeping the
// Python side's dependency on this package minimal.
const BaseRefFileName = ".masuda-base-ref"

func xdgBase() (string, error) {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}

// DataHome returns masuda's own XDG-based data directory
// (<XDG_DATA_HOME or ~/.local/share>/masuda) — the single place that knows
// masuda's data-dir name, shared by this package's workspaces/ subdirectory
// and internal/hostloop's runtime/ subdirectory (host-side venv + extracted
// orchestrator script), so neither depends on the target repository's root.
func DataHome() (string, error) {
	base, err := xdgBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "masuda"), nil
}

func rootDir() (string, error) {
	dh, err := DataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dh, "workspaces"), nil
}

// StateDir returns the on-disk directory masuda's own control files for
// workspace id live in — never inside the git worktree itself.
func StateDir(id string) (string, error) {
	root, err := rootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, id), nil
}

var idSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// NewID generates a unique workspace ID for branch: <sanitized-branch>-<6 hex
// chars>. The branch prefix keeps IDs recognizable in listings; the random
// suffix is what actually guarantees uniqueness across repeated invocations
// for the same branch.
func NewID(branch string) (string, error) {
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generating workspace ID: %w", err)
	}
	return fmt.Sprintf("%s-%s", idSanitizer.ReplaceAllString(branch, "-"), hex.EncodeToString(suffix)), nil
}

// Create persists a new workspace's metadata and returns it. Call once per
// masuda invocation that starts a genuinely new piece of work — `masuda
// worktree create`, `masuda plan start <branch> <task>`, `masuda review
// start <branch-or-ref>`.
func Create(repoRoot, id, branch, base string) (Info, error) {
	info := Info{ID: id, Branch: branch, Base: base, RepoRoot: repoRoot, CreatedAt: time.Now()}
	dir, err := StateDir(id)
	if err != nil {
		return Info{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Info{}, err
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return Info{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, metadataFileName), data, 0o644); err != nil {
		return Info{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, BaseRefFileName), []byte(base), 0o644); err != nil {
		return Info{}, err
	}
	return info, nil
}

// Load reads back a previously created workspace's metadata.
func Load(id string) (Info, error) {
	dir, err := StateDir(id)
	if err != nil {
		return Info{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, metadataFileName))
	if err != nil {
		return Info{}, fmt.Errorf("workspace %q not found: %w", id, err)
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return Info{}, fmt.Errorf("parsing metadata for workspace %q: %w", id, err)
	}
	return info, nil
}

// Exists reports whether id refers to an already-created workspace — used
// to disambiguate `masuda plan/review start <arg>`: an existing workspace ID
// means resume, anything else means arg is a branch name to start fresh
// against.
func Exists(id string) bool {
	_, err := Load(id)
	return err == nil
}

// List returns every workspace whose metadata records repoRoot as its
// repository — the state directory itself is global (not per-repo), since
// it deliberately lives outside any single git checkout.
func List(repoRoot string) ([]Info, error) {
	root, err := rootDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var infos []Info
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := Load(e.Name())
		if err != nil {
			continue // skip anything that doesn't look like a valid workspace
		}
		if info.RepoRoot == repoRoot {
			infos = append(infos, info)
		}
	}
	return infos, nil
}

// Remove deletes a workspace's state directory. It's the caller's
// responsibility to also remove the corresponding git worktree
// (internal/worktree.Remove) and stop any running sandbox/host-loop session
// first.
func Remove(id string) error {
	dir, err := StateDir(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// Status derives a short, human-readable progress summary for workspace id
// straight from its TASK.md -- the same file masuda's orchestrators
// (re)write on every loop iteration to instruct the self-looping Claude
// session what to do next. This is already the single authoritative
// "what's happening right now" statement (investigate/plan/gate-wait/
// implement/review perspective N of TOTAL/done/blocked), so reading it here
// avoids re-implementing orchestrator/*.py's phase-detection logic a second
// time in Go, which could drift out of sync as those orchestrators evolve.
func Status(id string) string {
	dir, err := StateDir(id)
	if err != nil {
		return "(unknown)"
	}
	data, err := os.ReadFile(filepath.Join(dir, "TASK.md"))
	if err != nil {
		return "(not started)"
	}
	line, _, _ := strings.Cut(string(data), "\n")
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#"))
}

// EntryStatus adds live progress info to Info for `masuda workspace list`.
// Running is computed by the caller (cmd/masuda), not this package: it
// requires internal/hostloop and internal/sandbox, and internal/hostloop
// already imports this package (DataHome), so importing either back here
// would cycle.
type EntryStatus struct {
	Info
	TaskStatus string
	Running    bool
}

// FormatEntries renders workspaces as an aligned, headered table for `masuda
// workspace list` (docker ps-style), via text/tabwriter -- the standard
// library's own tool for exactly this kind of column alignment.
func FormatEntries(entries []EntryStatus) string {
	if len(entries) == 0 {
		return "(no workspaces for this repo)\n"
	}
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "WORKSPACE ID\tBRANCH\tBASE\tSTATUS\tRUNNING\tCREATED")
	for _, e := range entries {
		running := "no"
		if e.Running {
			running = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			e.ID, e.Branch, e.Base, e.TaskStatus, running, e.CreatedAt.Format(time.RFC3339))
	}
	w.Flush()
	return buf.String()
}
