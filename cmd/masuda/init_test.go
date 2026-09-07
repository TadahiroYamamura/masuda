package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
)

// The .gitignore init writes is the answer to warnIfNotGitignored: without
// it, every `masuda mcp/egress/privileged-command approve` warns that
// settings.local.json -- which routinely holds real secret values -- is not
// covered by anything. Asserted against real `git check-ignore` rather than
// the template's text, since what matters is git's own verdict.
func TestInitGitignoreCoversTheMachineLocalPartsOfMasudaDir(t *testing.T) {
	root := newTestRepo(t)
	if err := os.MkdirAll(filepath.Join(root, config.DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.GitignorePath(root), []byte(gitignoreTemplate), 0o644); err != nil {
		t.Fatal(err)
	}

	ignored := func(rel string) bool {
		t.Helper()
		err := exec.Command("git", "-C", root, "check-ignore", "-q", filepath.Join(root, rel)).Run()
		return err == nil
	}

	for _, rel := range []string{
		".masuda/settings.local.json",
		".masuda/worktrees/abc123/main.go",
	} {
		if !ignored(rel) {
			t.Errorf("%s is not ignored -- approve commands will keep warning about it", rel)
		}
	}

	// The other half of .masuda/ is the project's own declaration and has to
	// stay committable, or a team never sees the settings/perspectives.
	for _, rel := range []string{
		".masuda/settings.json",
		".masuda/images/default/Dockerfile",
		".masuda/reviews/dead-code.md",
		".masuda/.gitignore",
	} {
		if ignored(rel) {
			t.Errorf("%s is ignored, but it is meant to be committed", rel)
		}
	}
}

// The hosts the sandbox's own Claude Code cannot start without. Empty would
// mean init produces a settings.json that leaves every fresh workspace dead
// on arrival (Issue #50).
func TestRequiredEgressHostsIsNotEmpty(t *testing.T) {
	if len(requiredEgressHosts) == 0 {
		t.Fatal("requiredEgressHosts is empty")
	}
}
