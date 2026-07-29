package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/masuda/internal/hostloop"
	"github.com/TadahiroYamamura/masuda/internal/sandbox"
	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// newChatCommand builds `masuda chat`, the single attach point for whichever
// session (phase 1-2 host loop or phase 3-5 sandbox) is currently running for
// a workspace. Unlike show/approve/reject (internal/gate.go), attaching
// doesn't need to know which gate the session is waiting at — a workspace
// only ever has one live tmux session at a time, and G1 in particular can be
// waiting in either place (first pass: host loop; reopened by a plan
// deviation, ADR-0010: sandbox) — so this always tries host loop first, then
// sandbox, rather than branching on a gate name the caller would otherwise
// have to supply.
func newChatCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "chat <workspace-id>",
		Short: "Attach interactively to whichever session is running for a workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if !workspace.Exists(id) {
				return fmt.Errorf("no workspace %q", id)
			}
			if hostloop.IsRunning(id) {
				return attach(hostloop.AttachArgs(id))
			}
			if sandbox.IsRunning(id) {
				return attach(sandbox.AttachArgs(id))
			}
			return fmt.Errorf("no session running for %q — run `masuda plan start %s` or `masuda sandbox start %s` first", id, id, id)
		},
	}
}
