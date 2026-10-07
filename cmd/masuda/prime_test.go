package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestPrimeの出力をSessionStartフックのadditionalContextとして読める(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	orig := os.Stdout
	os.Stdout = w
	err = runPrime([]string{"--hook-json"})
	os.Stdout = orig
	w.Close()
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		HookSpecificOutput struct{ HookEventName, AdditionalContext string }
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got.HookSpecificOutput.HookEventName != "SessionStart" || !strings.HasPrefix(got.HookSpecificOutput.AdditionalContext, "# masuda") {
		t.Errorf("hook output = %s", out)
	}
}
