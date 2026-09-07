// Package config reads .masuda/settings.json, an optional, user-edited file
// committed at a target repository's root. It lets a repo declare defaults
// for masuda's own CLI flags — which Docker image to run, and which branch
// is the repo's trunk — so a user working in that repo doesn't have to
// repeat the same flags on every invocation.
//
// .masuda/settings.json supersedes the older single-file .masuda.json
// (ADR-0024): masuda init now populates a .masuda/ directory (this file
// plus .masuda/reviews/, internal/perspectives), and reading .masuda.json
// was deliberately dropped rather than kept as a fallback — a breaking
// change, not a migration.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DirName is the config directory's name, expected at a repository's root.
const DirName = ".masuda"

// SettingsFileName is the settings file's name within DirName.
const SettingsFileName = "settings.json"

// SettingsPath returns the absolute path to repoRoot's settings file.
func SettingsPath(repoRoot string) string {
	return filepath.Join(repoRoot, DirName, SettingsFileName)
}

// GitignoreFileName is the .gitignore's name within DirName. masuda init
// writes one covering the machine-local parts of DirName (settings.local.json,
// worktrees/); a repo is free to replace it, e.g. with `*` to keep DirName's
// own contents out of git entirely (ADR-0036).
const GitignoreFileName = ".gitignore"

// GitignorePath returns the absolute path to repoRoot's .masuda/.gitignore.
func GitignorePath(repoRoot string) string {
	return filepath.Join(repoRoot, DirName, GitignoreFileName)
}

// Config is the on-disk shape of .masuda/settings.json. All fields are
// optional — an absent file, or an absent field within one, means "use
// masuda's built-in default."
type Config struct {
	// Image is the name of the .masuda/images/ entry `masuda sandbox
	// start` / `masuda review start` build their VM rootfs from when
	// --image isn't passed explicitly (ADR-0054). Not a Docker tag: the
	// local tag is derived from the entry name (see ImageTag), so nothing
	// here has to be kept in sync with what `masuda update` built.
	Image string `json:"image,omitempty"`
	// Base is the repo's trunk branch, used as the default for both --base
	// (the branch new work starts from) and --into (the branch a merge
	// lands on) when neither is passed explicitly — the two are almost
	// always the same branch in practice.
	Base string `json:"base,omitempty"`
	// ClaudeSettings is passed verbatim to the `claude` CLI's --settings
	// flag for every session masuda launches (phase 1-2 on the host, phase
	// 3-5 in the sandbox). Unlike Image/Base, this has no masuda-side
	// built-in default: masuda itself must not carry implicit Claude Code
	// settings, so an absent field means no --settings flag is added at
	// all, not "fall back to some default." `masuda init` populates it
	// with a starting value the user can freely edit (see cmd/masuda
	// init.go), the same way it materializes .masuda/reviews/ instead of
	// keeping built-in content implicit. masuda never interprets this
	// value — it's an opaque payload for Claude Code, not masuda.
	ClaudeSettings json.RawMessage `json:"claudeSettings,omitempty"`

	// MCPServers declares child MCP servers the per-workspace state daemon
	// should aggregate into its Claude-facing curated tool set (Issue #35,
	// ADR-0041's forward-pointer). The map key is the server's name --
	// used both as the `masuda mcp approve <name>` argument and as the
	// proxied tool name's namespace prefix ("<name>__<tool>").
	//
	// Declaring a server here does nothing on its own: this file is
	// committed to the target repository and so, like every other field
	// here, is not unconditionally trusted (Issue #19). The daemon only
	// starts a declared server once a matching, hash-pinned approval
	// exists in repoRoot/.masuda/settings.local.json (see LoadLocal) --
	// see DeclHash.
	MCPServers map[string]MCPServerDecl `json:"mcpServers,omitempty"`

	// EgressAllowlist declares the TLS hostnames this repo's sandbox VM
	// needs to reach (Issue #11 M4). Same declare/approve split as
	// MCPServers, for the same reason (Issue #19: this file is committed
	// to the target repository, so it can't unilaterally grant network
	// access) -- but no hash-pinning here (contrast MCPServerDecl/
	// DeclHash): a hostname entry carries no separate mutable payload the
	// way a server's command/args/env do, so there is nothing for a
	// malicious edit to change out from under an already-approved name
	// without the name itself changing (which is just a different,
	// unapproved entry). A hostname is allowed only when it appears in
	// both this list and LocalSettings.EgressAllowlist -- see
	// internal/sandbox's resolveEgressAllowlist.
	EgressAllowlist []string `json:"egressAllowlist,omitempty"`

	// PrivilegedCommands declares commands this repo needs to run with
	// capabilities the main sandbox VM deliberately never grants -- root, a
	// Docker daemon (ADR-0053). Each declared command runs in a fresh,
	// single-purpose VM destroyed immediately afterwards; the VM the AI
	// session itself lives in never gains the privilege. The map key is the
	// command's name, used both as the `masuda privileged-command approve
	// <name>` argument and as the only thing the AI-facing
	// run_privileged_command tool may name -- the AI never passes a command
	// string, so what actually runs always comes from this declaration.
	//
	// Same declare/approve split as MCPServers, and hash-pinned for the
	// same reason (see DeclHash): unlike an EgressAllowlist hostname, an
	// entry here carries a mutable payload (the command line, the image)
	// that a project-side edit could swap out from under an approval
	// granted against something else.
	PrivilegedCommands map[string]PrivilegedCommandDecl `json:"privilegedCommands,omitempty"`
}

// MCPServerDecl is one entry in Config.MCPServers: how to launch a child
// MCP server and which of its tools may ever reach Claude. It never
// carries secret values -- only the *names* of environment variables the
// child needs (Env); actual values live in the user-owned, gitignored
// settings.local.json (see LocalSettings.MCPServers[name].Env).
type MCPServerDecl struct {
	// Command is the executable to exec (e.g. "npx").
	Command string `json:"command"`
	// Args are passed to Command verbatim.
	Args []string `json:"args,omitempty"`
	// Env lists the names (never values) of environment variables the
	// child process needs. A name here with no corresponding value in the
	// user's approval blocks the daemon from starting this server at all.
	Env []string `json:"env,omitempty"`
	// Tools is the allowlist of this child server's own tool names that
	// may be proxied onto the curated set. Default-deny: any tool the
	// child reports that isn't listed here is never registered, no matter
	// what the user approved -- the declaration-side half of a two-guard
	// model (the user-side half is MCPServerApproval.Approved).
	Tools []string `json:"tools,omitempty"`
}

// PrivilegedCommandDecl is one entry in Config.PrivilegedCommands: what
// to run, and which disposable VM image to run it in (ADR-0053). Like
// MCPServerDecl it never carries secret values -- but where a child MCP
// server at least needs the *names* of the credentials it wants, a
// privileged command needs none at all: its VM is deliberately given none
// of the session's long-lived assets (no /masuda-secrets, no MCP relay,
// no SSH), so there is nothing for a credential name to refer to.
type PrivilegedCommandDecl struct {
	// Command is the command line to run inside the disposable VM, against
	// a snapshot copy of the workspace tree.
	Command string `json:"command"`
	// Image names the .masuda/images/ entry this command runs in
	// (ADR-0054). Required: masuda carries no built-in fallback image,
	// following the same principle as ClaudeSettings (ADR-0031) -- masuda
	// itself must not make an implicit configuration decision on the
	// user's behalf. `masuda init` materializes the entry as a directory
	// the user sees, edits, and commits, so a declaration here always
	// points at something real in the repository.
	Image string `json:"image"`
	// TimeoutSeconds bounds a single run. Zero means the runner's own
	// default applies -- unlike a missing Image, a missing timeout cannot
	// cause something other than what the user reviewed to execute, so
	// there is nothing here for an implicit default to undermine.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
	// Outputs lists paths, relative to /workspace, to collect from the
	// disposable VM's workspace snapshot once the command finishes
	// (ADR-0053). Collected files land in a per-run directory under the
	// workspace state directory -- never back onto the live worktree,
	// which the main VM's session may have moved on from since the
	// snapshot was taken.
	Outputs []string `json:"outputs,omitempty"`
}

// PinnedDecl is the set of declaration types whose approvals are pinned to
// the exact declaration they were granted against. A closed union rather
// than `any` keeps DeclHash from silently accepting some unrelated value
// that merely happens to marshal.
type PinnedDecl interface {
	MCPServerDecl | PrivilegedCommandDecl
}

// DeclHash returns a stable fingerprint of decl (sha256 of its canonical
// JSON encoding). `masuda mcp approve` and `masuda privileged-command
// approve` record this alongside a user's approval (MCPServerApproval /
// PrivilegedCommandApproval.DeclHash) so masuda can tell whether
// repoRoot's settings.json changed that declaration since it was last
// approved -- e.g. a malicious commit swapping an already-approved
// server's command/args. A hash mismatch is treated the same as "never
// approved" (see internal/statedaemon/mcpaggregator), without which
// approval-by-name alone would let a project-side change silently
// escalate a previously-reviewed declaration, reintroducing the
// "settings.json blindly trusted" problem this project/user split exists
// to avoid (Issue #19).
func DeclHash[T PinnedDecl](decl T) (string, error) {
	data, err := json.Marshal(decl)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ValidateOutputPath rejects a PrivilegedCommandDecl.Outputs entry that
// could name something outside the disposable VM's workspace snapshot.
//
// Lives here, rather than next to either caller, because two very different
// moments have to agree on it: `masuda privileged-command approve` refuses
// to record an approval for a declaration it would later refuse to honour,
// and the host-side collector re-checks every entry when it walks the
// snapshot -- the approval could have been recorded by an older masuda, and
// the collector is the side that actually touches the filesystem.
func ValidateOutputPath(out string) error {
	if strings.TrimSpace(out) == "" {
		return errors.New(`"outputs" contains an empty path`)
	}
	if filepath.IsAbs(out) {
		return fmt.Errorf(`"outputs" entry %q must be relative to /workspace`, out)
	}
	clean := filepath.Clean(out)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf(`"outputs" entry %q escapes the workspace`, out)
	}
	return nil
}

// PrivilegedCommandHash returns the value `masuda privileged-command
// approve` records, and that a run request must match before any disposable
// VM is started: DeclHash(decl) combined with the digest of the image entry
// decl names (ADR-0053).
//
// Pinning the declaration alone would leave a hole. What actually runs with
// privilege is determined by the command line *and* by the contents of the
// image it runs in, and the image is declared in the same untrusted,
// project-committed .masuda/ tree (Issue #19) -- so an approval pinned only
// to {command, image name, ...} would survive that image's Dockerfile being
// replaced with something the user never reviewed.
func PrivilegedCommandHash(repoRoot string, decl PrivilegedCommandDecl) (string, error) {
	declHash, err := DeclHash(decl)
	if err != nil {
		return "", err
	}
	imageDigest, err := ImageDigest(repoRoot, decl.Image)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(declHash + "\x00" + imageDigest))
	return hex.EncodeToString(sum[:]), nil
}

// Load reads .masuda/settings.json from repoRoot. A missing file is not an
// error — it returns a zero-value Config, so callers can treat every field
// as "unset, fall back to the built-in default."
func Load(repoRoot string) (Config, error) {
	path := SettingsPath(repoRoot)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	return cfg, nil
}
