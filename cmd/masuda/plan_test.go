package main

import (
	"strings"
	"testing"
)

// A document is a complete input on its own (ADR-0016's --file, now usable
// without a task): the thing being worked on is often something that already
// exists, and restating it as a one-liner adds nothing.
func TestTaskBriefForDefaultsToTheDocumentWhenOnlyFileIsGiven(t *testing.T) {
	brief, err := taskBriefFor("", "/tmp/notes.md", "fix/x")
	if err != nil {
		t.Fatal(err)
	}
	if brief == "" {
		t.Fatal("brief is empty -- _read_task_brief() raises when the key is missing")
	}
	if !strings.Contains(brief, "指示書") {
		t.Fatalf("the brief must point at the instructions document, got %q", brief)
	}
	// The reader is a subagent that never sees the command line, so a flag
	// name is a string it cannot resolve -- it has to be pointed at the
	// section that names the file instead.
	if strings.Contains(brief, "--file") {
		t.Fatalf("the brief must not name a CLI flag the agent cannot see, got %q", brief)
	}
}

// A task alongside --file narrows the document ("just the X part of this"),
// so it must survive untouched.
func TestTaskBriefForKeepsAnExplicitTaskEvenWithAFile(t *testing.T) {
	brief, err := taskBriefFor("Xの部分だけ", "/tmp/notes.md", "fix/x")
	if err != nil {
		t.Fatal(err)
	}
	if brief != "Xの部分だけ" {
		t.Fatalf("brief = %q, want the task verbatim", brief)
	}
}

func TestTaskBriefForRejectsNeitherTaskNorFile(t *testing.T) {
	_, err := taskBriefFor("", "", "fix/x")
	if err == nil {
		t.Fatal("expected an error when there is no input at all")
	}
	if !strings.Contains(err.Error(), "--file") {
		t.Fatalf("the error must mention --file as the other way in: %v", err)
	}
}
