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
// plan/, gate markers, review results, ...) into a directory the target
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
	"sort"
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
	Name      string    `json:"name,omitempty"`
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

// CommitMessageFileName is the plain-text commit message orchestrator/*.py's
// synthesize phase writes (ADR-0023's follow-up fix): masuda's phase 4/5
// never runs `git commit` itself (review diffs are computed from staged,
// uncommitted changes throughout), so without this the only record of a
// workspace's work is its clone's uncommitted working tree — which
// `review approve` then deletes. The synthesize subagent, which already has
// full context on what changed, writes the message here; `finalizeReviewApproval`
// reads it and commits in the clone right before pulling it into repoRoot.
const CommitMessageFileName = ".masuda-commit-message"

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

// maxNewIDAttempts bounds NewID's collision-retry loop. A collision on the
// first attempt is already a 1-in-16.7M event (3 random bytes); this is a
// defensive cap against a pathological RNG/filesystem failure, not something
// expected to ever actually bind in practice.
const maxNewIDAttempts = 100

// NewID generates a workspace ID not already in use: 6 random hex chars, no
// branch name (ADR-0030 dropped the `<sanitized-branch>-` prefix ADR-0014
// originally used — Info.Branch/Info.Name already carry that information
// for every masuda-native surface, so embedding it in the identifier itself
// only paid off for reading raw `docker ps`/`tmux ls` output, which isn't
// worth lengthening the identifier every masuda command takes as an
// argument). Retries on collision against Exists -- dropping the branch
// prefix pools every workspace into one shared 3-byte ID space instead of
// one per branch, so this is no longer rare enough over a tool's lifetime to
// leave unchecked (an unchecked collision would silently overwrite an
// existing workspace's metadata, since Create's os.MkdirAll/os.WriteFile are
// both unconditional).
func NewID() (string, error) {
	suffix := make([]byte, 3)
	for range maxNewIDAttempts {
		if _, err := rand.Read(suffix); err != nil {
			return "", fmt.Errorf("generating workspace ID: %w", err)
		}
		id := hex.EncodeToString(suffix)
		if !Exists(id) {
			return id, nil
		}
	}
	return "", fmt.Errorf("generating workspace ID: %d consecutive collisions, giving up", maxNewIDAttempts)
}

// Create persists a new workspace's metadata and returns it. Call once per
// masuda invocation that starts a genuinely new piece of work — `masuda
// worktree create`, `masuda plan start <branch> <task>`, `masuda review
// start <branch-or-ref>`. name is an optional human-readable label (display
// only — it plays no part in resolving a workspace, unlike id) and may be
// empty.
func Create(repoRoot, id, branch, base, name string) (Info, error) {
	info := Info{ID: id, Name: name, Branch: branch, Base: base, RepoRoot: repoRoot, CreatedAt: time.Now()}
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

// Rename overwrites workspace id's display name (Info.Name) — the one field
// on Info a user can change after creation, since unlike id/branch/base it's
// purely a label carrying no resolution or git meaning.
func Rename(id, name string) error {
	info, err := Load(id)
	if err != nil {
		return err
	}
	info.Name = name
	dir, err := StateDir(id)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, metadataFileName), data, 0o644)
}

// Exists reports whether id refers to an already-created workspace — used
// to disambiguate `masuda plan/review start <arg>`: an existing workspace ID
// means resume, anything else means arg is a branch name to start fresh
// against.
func Exists(id string) bool {
	_, err := Load(id)
	return err == nil
}

// ListAll returns every workspace known to this machine, regardless of which
// repository it targets — the state directory is global (not per-repo),
// since it deliberately lives outside any single git checkout. Sorted by
// CreatedAt, most recent first: os.ReadDir's underlying filename order used
// to be a reasonable stand-in (IDs were branch-prefixed, ADR-0014), but
// ADR-0030's pure-random IDs sort in an order that means nothing to a human,
// so this needs to be explicit now.
//
// This exists alongside the repo-scoped List for `masuda update` (ADR-0032):
// replacing the CLI binary affects every repo's workspaces at once, so its
// "is anything in progress" check must not filter by the repo it happens to
// be invoked from.
func ListAll() ([]Info, error) {
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
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].CreatedAt.After(infos[j].CreatedAt) })
	return infos, nil
}

// List returns every workspace whose metadata records repoRoot as its
// repository. See ListAll for the underlying enumeration and sort order.
func List(repoRoot string) ([]Info, error) {
	all, err := ListAll()
	if err != nil {
		return nil, err
	}
	var infos []Info
	for _, info := range all {
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
	fmt.Fprintln(w, "WORKSPACE ID\tNAME\tBRANCH\tBASE\tSTATUS\tRUNNING\tCREATED")
	for _, e := range entries {
		running := "no"
		if e.Running {
			running = "yes"
		}
		name := e.Name
		if name == "" {
			name = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			e.ID, name, e.Branch, e.Base, e.TaskStatus, running, e.CreatedAt.Format(time.RFC3339))
	}
	w.Flush()
	return buf.String()
}
