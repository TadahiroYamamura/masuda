// Package mcpaggregator turns a workspace's curated, Claude-facing MCP
// server (internal/statedaemon/mcpserver.NewCurated) into an aggregator of
// child MCP servers declared by the target repository (Issue #35,
// ADR-0041's forward-pointer). Config.MCPServers (repoRoot's
// .masuda/settings.json, committed and therefore not unconditionally
// trusted -- Issue #19) only *declares* servers; LocalSettings.MCPServers
// (repoRoot's .masuda/settings.local.json, user-owned and gitignored)
// *approves* specific ones and supplies the secret env values they need.
// Only a server that is both declared and approved -- with the approval's
// DeclHash still matching the current declaration -- is ever started.
//
// mcpserver stays the package responsible for "build a *mcp.Server from a
// *statedaemon.Store"; this package's job is the separate concern of
// spawning child processes and registering their tools onto an
// already-constructed server, so it depends on mcpserver's output type
// rather than the other way around.
package mcpaggregator

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// childStartTimeout bounds how long one child MCP server may take to start
// and respond to ListTools before it's given up on. Generous because e.g.
// `npx` may need to download a package on a cold cache; bounded so one
// slow or hung child never leaves a startChild goroutine stuck forever.
const childStartTimeout = 30 * time.Second

// Aggregator owns every child MCP server process this workspace's daemon
// spawned for repoRoot's approved, declared MCP servers, and the tools it
// proxied from each onto the curated server. One broken or misconfigured
// child must never prevent the daemon's own built-in curated tools
// (wait_for_gate_resolution, resolve_gate_from_chat) from working -- every
// failure path in this package logs and skips rather than propagating an
// error out of Start.
type Aggregator struct {
	wg sync.WaitGroup

	mu       sync.Mutex
	children map[string]*child
}

type child struct {
	session *mcp.ClientSession
	logFile *os.File
}

// Start reads repoRoot's .masuda/settings.json and settings.local.json,
// resolves the subset of declared servers that are approved (resolveApproved),
// and asynchronously launches and connects each one, registering its
// allowlisted tools onto curated as it becomes ready. mcp.Server.AddTool is
// safe to call at any time, including after curated has already started
// being served, so Start never blocks its caller on any child.
//
// Start itself never returns an error: a config-read failure or a
// per-child failure is logged to logDir/mcp-<name>.log (or masuda's own
// process log, if that file can't even be opened) and treated as "this
// server just isn't available," never as a reason to fail daemon startup.
func Start(ctx context.Context, repoRoot string, curated *mcp.Server, logDir string) *Aggregator {
	a := &Aggregator{children: make(map[string]*child)}

	cfg, err := config.Load(repoRoot)
	if err != nil {
		log.Printf("mcp aggregator: reading %s: %v", config.SettingsPath(repoRoot), err)
		return a
	}
	local, err := config.LoadLocal(repoRoot)
	if err != nil {
		log.Printf("mcp aggregator: reading %s: %v", config.SettingsLocalPath(repoRoot), err)
		return a
	}

	approved, skipped := resolveApproved(cfg, local)
	for _, s := range skipped {
		log.Printf("mcp aggregator: skipping %q: %s", s.name, s.reason)
	}
	for name, rs := range approved {
		a.wg.Add(1)
		go a.startChild(ctx, name, rs, curated, logDir)
	}
	return a
}

// Close waits for every in-flight startChild to finish (so a child that
// was mid-handshake when ctx was cancelled is accounted for, not leaked),
// then closes every successfully connected child's session.
// mcp.CommandTransport's Close already handles the actual process teardown
// (close stdin, wait, SIGTERM, SIGKILL), so there's nothing else to do
// here.
func (a *Aggregator) Close() {
	a.wg.Wait()
	a.mu.Lock()
	defer a.mu.Unlock()
	for name, c := range a.children {
		if err := c.session.Close(); err != nil {
			log.Printf("mcp aggregator: closing %q: %v", name, err)
		}
		c.logFile.Close()
	}
}

type resolvedServer struct {
	decl config.MCPServerDecl
	env  map[string]string // resolved values for every name in decl.Env
}

type skipReason struct{ name, reason string }

// resolveApproved implements the two-guard model's user-side gate: a
// declared server only starts if (1) an approval entry exists and is
// Approved, (2) its DeclHash matches the current declaration (a stale
// approval after a project-side change is treated as no approval at all),
// and (3) every name in decl.Env has a non-empty value in the approval.
// Deliberately a pure function (no I/O), so it's unit-testable without
// spawning anything.
func resolveApproved(cfg config.Config, local config.LocalSettings) (map[string]resolvedServer, []skipReason) {
	approved := make(map[string]resolvedServer)
	var skipped []skipReason
	for name, decl := range cfg.MCPServers {
		approval, ok := local.MCPServers[name]
		if !ok || !approval.Approved {
			skipped = append(skipped, skipReason{name, "not approved -- run `masuda mcp approve " + name + "`"})
			continue
		}
		wantHash, err := config.DeclHash(decl)
		if err != nil || approval.DeclHash != wantHash {
			skipped = append(skipped, skipReason{name, "declaration changed since approval -- re-run `masuda mcp approve " + name + "`"})
			continue
		}
		env := make(map[string]string, len(decl.Env))
		var missing []string
		for _, e := range decl.Env {
			v := approval.Env[e]
			if v == "" {
				missing = append(missing, e)
				continue
			}
			env[e] = v
		}
		if len(missing) > 0 {
			skipped = append(skipped, skipReason{name, "missing env values: " + strings.Join(missing, ", ")})
			continue
		}
		approved[name] = resolvedServer{decl: decl, env: env}
	}
	return approved, skipped
}

// startChild execs decl.Command, connects an MCP client to it over
// stdin/stdout (mcp.CommandTransport), lists its tools, and registers
// every allowlisted one onto curated (registerProxy). Always calls
// a.wg.Done(); registers into a.children only on full success, so
// Aggregator.Close never tries to close a session that was never
// established.
func (a *Aggregator) startChild(ctx context.Context, name string, rs resolvedServer, curated *mcp.Server, logDir string) {
	defer a.wg.Done()
	ctx, cancel := context.WithTimeout(ctx, childStartTimeout)
	defer cancel()

	logFile, err := os.OpenFile(filepath.Join(logDir, "mcp-"+name+".log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("mcp aggregator: opening log for %q: %v", name, err)
		return
	}

	cmd := exec.Command(rs.decl.Command, rs.decl.Args...)
	cmd.Env = os.Environ() // inherit the daemon's own env (PATH, HOME, npm config, ...)
	for k, v := range rs.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = logFile // the child's own diagnostics (npx fetch errors etc.), never silently dropped

	client := mcp.NewClient(&mcp.Implementation{Name: "masuda-mcp-aggregator", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		log.Printf("mcp aggregator: starting %q: %v", name, err)
		logFile.Close()
		return
	}

	tools, err := listAllTools(ctx, session)
	if err != nil {
		log.Printf("mcp aggregator: listing tools for %q: %v", name, err)
		session.Close()
		logFile.Close()
		return
	}

	allow := make(map[string]bool, len(rs.decl.Tools))
	for _, t := range rs.decl.Tools {
		allow[t] = true
	}
	registered := 0
	for _, t := range tools {
		if !allow[t.Name] {
			continue // project-side allowlist: default-deny
		}
		if registerProxy(curated, name, session, t) {
			registered++
		}
	}
	log.Printf("mcp aggregator: %q started, %d/%d allowlisted tools registered", name, registered, len(rs.decl.Tools))

	a.mu.Lock()
	a.children[name] = &child{session: session, logFile: logFile}
	a.mu.Unlock()
}

func listAllTools(ctx context.Context, session *mcp.ClientSession) ([]*mcp.Tool, error) {
	var all []*mcp.Tool
	cursor := ""
	for {
		res, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, err
		}
		all = append(all, res.Tools...)
		if res.NextCursor == "" {
			return all, nil
		}
		cursor = res.NextCursor
	}
}
