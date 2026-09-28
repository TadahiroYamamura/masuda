package main

import (
	"os/exec"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/workflow/engine"
	"github.com/TadahiroYamamura/masuda/internal/workflow/snapshot"
)

func TestShellQuoteSurvivesTheShell(t *testing.T) {
	for _, s := range []string{`go test ./... && echo 'ok'`, `a "b" $HOME \n`, ``} {
		out, err := exec.Command("bash", "-c", "printf %s "+shellQuote(s)).Output()
		if err != nil || string(out) != s {
			t.Errorf("shellQuote(%q) came back as %q (%v)", s, out, err)
		}
	}
}

func TestCheckRunnerRefusesUndeclaredCheck(t *testing.T) {
	store := engine.NewMemStore()
	if err := snapshot.SaveChecks(store, map[string]config.CheckDecl{"test": {Command: "true"}}); err != nil {
		t.Fatal(err)
	}
	run := checkRunner("ws", t.TempDir(), t.TempDir(), t.TempDir(), store)
	if _, _, err := run("lint"); err == nil {
		t.Fatal("an undeclared check ran")
	}
}
