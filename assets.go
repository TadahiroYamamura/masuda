// Package masuda embeds masuda's own host-side runtime assets that must
// never be resolved relative to whatever target repository the CLI happens
// to be invoked against. go:embed can only reach files at-or-below the
// declaring file's own directory (no ".."), and these three assets are
// siblings of internal/ and cmd/ at the repo root — so this file has to
// live at the repo root too, even though it isn't meant as a public API.
package masuda

import _ "embed"

//go:embed orchestrator/investigate_plan_graph.py
var OrchestratorScript []byte

//go:embed requirements.txt
var Requirements []byte

//go:embed runtime/CLAUDE.md
var ClaudeMD []byte
