package sandbox

// Backend abstracts how a sandbox is launched, stopped, and attached to.
// Issue #31 is migrating masuda's sandbox execution substrate from Docker to
// a Cloud Hypervisor microVM; DockerBackend (this file) is the only
// implementation until VMBackend lands, and is expected to be deleted once
// that migration completes -- Docker is not meant to remain as a permanent,
// user-selectable alternative. Introducing this interface now, ahead of
// VMBackend, is deliberately the first, VM-independent step of the roadmap
// (M1): it gives the VM work a seam to implement against without touching
// this package's existing Docker behavior or any of its call sites, which
// keep using the package-level Start/Stop/IsRunning/AttachArgs functions
// unchanged.
type Backend interface {
	// Start launches (or, if already running, resumes) the sandbox for
	// workspace id and returns a Handle describing how to reach it.
	Start(id, worktreeDir, stateDir, repoRoot, image string) (Handle, error)
	// Stop tears down the sandbox for workspace id. Not an error if it's
	// already gone.
	Stop(id string) error
	// IsRunning reports whether the sandbox for workspace id is currently up.
	IsRunning(id string) bool
	// AttachArgs returns the argv for interactively attaching to the
	// sandbox's running session.
	AttachArgs(id string) []string
}

// DockerBackend implements Backend by shelling out to `docker`, wrapping this
// package's existing top-level functions so there's exactly one
// implementation of the container lifecycle, not two.
type DockerBackend struct{}

var _ Backend = DockerBackend{}

func (DockerBackend) Start(id, worktreeDir, stateDir, repoRoot, image string) (Handle, error) {
	return Start(id, worktreeDir, stateDir, repoRoot, image)
}

func (DockerBackend) Stop(id string) error {
	return Stop(id)
}

func (DockerBackend) IsRunning(id string) bool {
	return IsRunning(id)
}

func (DockerBackend) AttachArgs(id string) []string {
	return AttachArgs(id)
}
