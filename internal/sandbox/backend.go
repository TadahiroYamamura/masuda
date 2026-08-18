package sandbox

// Backend abstracts how a sandbox is launched, stopped, and attached to.
// Issue #31 migrated masuda's sandbox execution substrate from Docker to a
// Cloud Hypervisor microVM; VMBackend (internal/sandbox/vmbackend.go) is now
// the only implementation. DockerBackend (which wrapped `docker
// run`/`exec`/`create`/`stop`) was deleted once VMBackend proved stable in
// real use -- Docker was never meant to remain as a permanent,
// user-selectable alternative. This interface itself predates that
// deletion (M1 of the roadmap): it gave the VM work a seam to implement
// against without touching the Docker path's behavior while both existed
// side by side.
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
	// sandbox's running session. Returns an error because VMBackend's
	// implementation has to look up the guest's current DHCP-assigned IP
	// (Issue #31 M5-5/M5-6), which is real I/O that can fail -- e.g. no
	// lease yet, or the VM isn't actually running.
	AttachArgs(id string) ([]string, error)
}
