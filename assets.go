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

//go:embed orchestrator/state_client.py
var StateClientScript []byte

//go:embed requirements.txt
var Requirements []byte

//go:embed runtime/CLAUDE.md
var ClaudeMD []byte

// PrivilegedRunner and PrivilegedRunnerUnit are injected into a disposable
// VM's rootfs (ADR-0053) rather than being part of the image the target
// repository declares: they are masuda's protocol for handing a command in
// and getting an exit code and log back, and must not depend on -- or be
// breakable by -- a Dockerfile the project owns.
//
//go:embed runtime/masuda-run.sh
var PrivilegedRunner []byte

//go:embed runtime/masuda-run.service
var PrivilegedRunnerUnit []byte

// DockerTemplate is the starting content of an image entry whose VM runs a
// Docker daemon (.masuda/images/<entry>/Dockerfile, ADR-0053/0054). It
// lives under templates/ rather than docker/ to keep the distinction
// visible: docker/{go,python,typescript,full}/Dockerfile are masuda's own
// build inputs, while this one is never built here -- it is copied into a
// target repository, where it becomes that project's file to edit.
//
//go:embed templates/docker.Dockerfile
var DockerTemplate []byte
