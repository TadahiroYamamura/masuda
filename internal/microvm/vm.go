package microvm

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/TadahiroYamamura/masuda/internal/rootfs"
)

const (
	vmMemorySize = "2048M"
	vmCPUs       = "boot=1"

	vmBootTimeout     = 30 * time.Second
	vmSSHReadyTimeout = 60 * time.Second
	sshReadyInterval  = time.Second
	vmShutdownTimeout = 15 * time.Second
	vmDHCPTimeout     = 20 * time.Second

	baseKernelCmdline = "console=ttyS0 root=/dev/vda rw"
)

// ErrTimeout is returned by Run when the VM did not power itself off within
// the allotted time.
var ErrTimeout = errors.New("VM did not power off in time")

func chPIDPath(workDir string) string  { return filepath.Join(workDir, "cloud-hypervisor.pid") }
func rootfsPath(workDir string) string { return filepath.Join(workDir, "rootfs.img") }
func shareSocket(workDir, tag string) string {
	return filepath.Join(workDir, "virtiofs-"+tag+".sock")
}

// booted is what prepare allocated for one VM, released by release on any
// path that does not leave the VM running.
type booted struct {
	workDir string
	tapName string
	mac     string
	shares  []*VirtiofsProcess
	args    []string
}

func (b *booted) release(h Host, id string) {
	for _, s := range b.shares {
		_ = s.Stop()
	}
	_ = h.releaseTap(id)
}

// prepare does everything short of starting the VMM: builds the rootfs,
// allocates the TAP device, and starts one virtiofsd per share. On error,
// whatever it had allocated is released before it returns.
func (h Host) prepare(spec Spec) (*booted, error) {
	if err := h.Check(); err != nil {
		return nil, err
	}
	kernelPath, err := h.findKernel()
	if err != nil {
		return nil, err
	}
	workDir, err := h.WorkDir(spec.ID)
	if err != nil {
		return nil, err
	}
	username, err := currentUsername()
	if err != nil {
		return nil, err
	}

	// Only the public key ever goes into the guest; one keypair serves every
	// VM on the host (see sshkey.go).
	_, pubKeyPath, err := h.EnsureSSHKeypair()
	if err != nil {
		return nil, fmt.Errorf("ensuring VM SSH keypair: %w", err)
	}
	pubKey, err := os.ReadFile(pubKeyPath)
	if err != nil {
		return nil, fmt.Errorf("reading VM SSH public key: %w", err)
	}

	opts := spec.Rootfs
	opts.ExtraFiles = append([]rootfs.ExtraFile{{
		GuestPath: filepath.Join("home", h.GuestUser.Name, ".ssh/authorized_keys"),
		Content:   pubKey, Mode: 0o600, UID: h.GuestUser.UID, GID: h.GuestUser.GID,
	}}, opts.ExtraFiles...)
	if err := addModules(&opts, spec.Modules, kernelVersionFromPath(kernelPath)); err != nil {
		return nil, err
	}
	img := rootfsPath(workDir)
	if err := rootfs.Build(spec.Image, img, opts); err != nil {
		return nil, fmt.Errorf("building VM rootfs: %w", err)
	}

	b := &booted{workDir: workDir, mac: MACFor(spec.ID)}
	b.tapName, err = h.ensureTap(spec.ID, username)
	if err != nil {
		return nil, fmt.Errorf("allocating VM network interface: %w", err)
	}

	var fsArgs []string
	for _, s := range spec.Shares {
		vf, err := StartVirtiofs(s.HostDir, shareSocket(workDir, s.Tag), filepath.Join(workDir, "virtiofs-"+s.Tag+".log"))
		if err != nil {
			b.release(h, spec.ID)
			return nil, fmt.Errorf("starting virtiofsd for share %s: %w", s.Tag, err)
		}
		b.shares = append(b.shares, vf)
		fsArgs = append(fsArgs, "tag="+s.Tag+",socket="+vf.SocketPath)
	}

	serial := spec.SerialLog
	if serial == "" {
		serial = filepath.Join(workDir, "console.log")
	}
	cmdline := strings.Join(append([]string{baseKernelCmdline}, spec.KernelArgs...), " ")
	b.args = []string{
		"--kernel", kernelPath,
		"--disk", "path=" + img + ",readonly=off,image_type=raw",
	}
	if len(fsArgs) > 0 {
		b.args = append(b.args, "--fs")
		b.args = append(b.args, fsArgs...)
	}
	b.args = append(b.args,
		"--net", "tap="+b.tapName+",mac="+b.mac,
		"--cpus", vmCPUs,
		"--memory", "size="+vmMemorySize+",shared=on",
		"--cmdline", cmdline,
		"--console", "off",
		"--serial", "file="+serial,
	)
	return b, nil
}

// Start boots spec as a long-lived VM and returns once the guest accepts SSH.
// A DHCP lease alone is not enough: sshd starts only after the guest has
// generated its host keys, several seconds later, and until then Shutdown's
// graceful poweroff cannot run (it falls back to killing the VMM, which can
// corrupt the disk image) and AttachArgs's session is refused. It is not
// idempotent on its own -- callers check IsRunning first. The VMM
// keeps running after this process exits; Shutdown stops it.
func (h Host) Start(spec Spec) error {
	b, err := h.prepare(spec)
	if err != nil {
		return err
	}
	cmd := exec.Command(cloudHypervisorBinary, b.args...)
	if err := StartBackgroundProcess(cmd, chPIDPath(b.workDir), rootfsPath(b.workDir)); err != nil {
		b.release(h, spec.ID)
		return fmt.Errorf("starting cloud-hypervisor: %w", err)
	}
	guestIP, err := LookupGuestIP(b.mac, h.LeaseFile, vmBootTimeout)
	if err != nil {
		_ = h.Shutdown(spec.ID)
		_ = h.Remove(spec.ID)
		return fmt.Errorf("VM did not obtain a DHCP lease in time (boot failure?): %w", err)
	}
	if err := waitUntil(func() error { return h.sshProbe(guestIP) }, vmSSHReadyTimeout, sshReadyInterval); err != nil {
		_ = h.Shutdown(spec.ID)
		_ = h.Remove(spec.ID)
		return fmt.Errorf("VM's sshd did not become reachable in time: %w", err)
	}
	return nil
}

// sshProbe runs `true` in the guest. An actual SSH session rather than a
// check that port 22 is open: the guest's sshd is socket-activated, so the
// port can accept before sshd itself is able to serve.
func (h Host) sshProbe(guestIP string) error {
	privKeyPath, _, err := h.SSHKeyPaths()
	if err != nil {
		return err
	}
	base := sshBaseArgs(h.GuestUser.Name, guestIP, privKeyPath)
	// ConnectTimeout: a dropped (rather than refused) connection would
	// otherwise hold one attempt for the TCP timeout, far past the deadline.
	args := append([]string{base[0], "-o", "ConnectTimeout=5", "-o", "BatchMode=yes"}, base[1:]...)
	out, err := exec.Command(args[0], append(args[1:], "true")...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// waitUntil calls try every interval until it succeeds or timeout passes,
// returning try's last error in the latter case.
func waitUntil(try func() error, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := try()
		if err == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("gave up after %s: %w", timeout, err)
		}
		time.Sleep(interval)
	}
}

// Run boots spec, blocks until the guest powers itself off, and releases
// its virtiofsd processes and TAP device before returning. A VM still
// running after timeout is killed and ErrTimeout returned. The working
// directory is left for the caller's Remove, so files the caller kept there
// (a Share's source, say) can still be read after the VM is gone.
//
// Unlike Start, no pidfile is written: the VMM is this process's own child
// for its whole life, so there is nothing for a later invocation to find.
func (h Host) Run(spec Spec, timeout time.Duration) error {
	b, err := h.prepare(spec)
	if err != nil {
		return err
	}
	defer b.release(h, spec.ID)

	// Deliberately left in the caller's session, unlike Start's VMM (see
	// StartBackgroundProcess): nothing but this call supervises the VM, so a
	// Ctrl-C that kills the caller has to take the VM down with it rather
	// than leave it running unwatched.
	cmd := exec.Command(cloudHypervisorBinary, b.args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting cloud-hypervisor: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("cloud-hypervisor exited abnormally: %w", err)
		}
		return nil
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return ErrTimeout
	}
}

// Shutdown stops VM id and releases its virtiofsd processes and TAP device,
// leaving its working directory in place for Remove. Not an error if it is
// already gone. It runs as an independent invocation from Start, so
// everything is found again through deterministic paths rather than
// in-memory handles.
//
// Graceful where possible: SSH in and run `sudo systemctl poweroff`, then
// wait for the VMM to exit on its own. Confirmed live that skipping this and
// just killing the process corrupts the disk image (the guest never gets to
// unmount/sync) -- this isn't optional polish.
func (h Host) Shutdown(id string) error {
	workDir, err := h.WorkDir(id)
	if err != nil {
		return err
	}

	h.attemptGracefulShutdown(id, workDir)
	waitForProcessExit(chPIDPath(workDir), vmShutdownTimeout)

	// Fallback if the graceful path didn't finish in time (or never got to
	// run at all, e.g. no DHCP lease found): KillStalePID is a no-op against
	// an already-exited process, so this is safe to always call.
	KillStalePID(chPIDPath(workDir), rootfsPath(workDir))
	_ = os.Remove(chPIDPath(workDir))

	// Globbed rather than derived from a Spec, which Shutdown does not have:
	// whichever shares the VM was started with left a pidfile each.
	pidFiles, _ := filepath.Glob(filepath.Join(workDir, "virtiofs-*.sock.pid"))
	for _, p := range pidFiles {
		socket := strings.TrimSuffix(p, ".pid")
		KillStalePID(p, socket)
		_ = os.Remove(p)
	}

	if err := h.releaseTap(id); err != nil {
		return fmt.Errorf("releasing VM network interface: %w", err)
	}
	return nil
}

// Remove deletes VM id's working directory. A VM is ephemeral by design: a
// fresh Start rebuilds the rootfs and sockets from scratch.
func (h Host) Remove(id string) error {
	return os.RemoveAll(filepath.Join(h.DataDir, "vm", id))
}

// attemptGracefulShutdown is best-effort: any failure (no DHCP lease found,
// SSH unreachable, guest too slow) just falls through to Shutdown's
// SIGTERM-based fallback, which always runs regardless.
func (h Host) attemptGracefulShutdown(id, workDir string) {
	if _, err := os.Stat(chPIDPath(workDir)); err != nil {
		return // nothing recorded as running for this id
	}
	guestIP, err := LookupGuestIP(MACFor(id), h.LeaseFile, 2*time.Second)
	if err != nil {
		return
	}
	privKeyPath, _, err := h.SSHKeyPaths()
	if err != nil {
		return
	}
	sshArgs := append(sshBaseArgs(h.GuestUser.Name, guestIP, privKeyPath), "sudo", "-n", "systemctl", "poweroff")
	cmd := exec.Command(sshArgs[0], sshArgs[1:]...)
	_ = cmd.Run() // the SSH connection is expected to drop mid-command as the guest shuts down
}

func waitForProcessExit(pidFilePath string, timeout time.Duration) {
	data, err := os.ReadFile(pidFilePath)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if sigErr := proc.Signal(syscall.Signal(0)); sigErr != nil || isZombie(pid) {
			return // process is gone
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// isZombie reports whether pid has exited but not been reaped. When the
// process that started the VMM is still alive -- Start and Shutdown called
// from one process, as a long-lived supervisor or a test does -- the exited
// VMM stays a zombie, and a signal-0 probe keeps answering that it is alive.
func isZombie(pid int) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	// The state is the first field after the parenthesised command name,
	// which may itself contain spaces or parentheses.
	rest := string(data[strings.LastIndexByte(string(data), ')')+1:])
	fields := strings.Fields(rest)
	return len(fields) > 0 && fields[0] == "Z"
}

// IsRunning reports whether VM id is running, by checking that the PID
// recorded in cloud-hypervisor.pid still belongs to a cloud-hypervisor
// started for *this* VM -- its argv carries this VM's rootfs image
// (`--disk path=<workDir>/rootfs.img,...`), which no other VM can claim.
//
// The identity check is the whole point (ADR-0061). A bare signal-0 probe
// says only that some process holds that number, and a host reboot both
// kills the VM and resets the PID space, so a recycled PID reads as a
// running VM -- and a caller that skips Start when this says yes then
// reports success while starting nothing (the same shape as Issue #52).
func (h Host) IsRunning(id string) bool {
	workDir, err := h.WorkDir(id)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(chPIDPath(workDir))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false
	}
	return processCmdlineContains(pid, rootfsPath(workDir))
}

// AttachArgs returns the ssh argv that runs remoteCmd in VM id interactively,
// after looking up the guest's current DHCP-assigned IP.
func (h Host) AttachArgs(id string, remoteCmd ...string) ([]string, error) {
	guestIP, err := LookupGuestIP(MACFor(id), h.LeaseFile, vmDHCPTimeout)
	if err != nil {
		return nil, fmt.Errorf("looking up VM guest IP for %s: %w", id, err)
	}
	privKeyPath, _, err := h.SSHKeyPaths()
	if err != nil {
		return nil, err
	}
	return append(sshBaseArgs(h.GuestUser.Name, guestIP, privKeyPath), remoteCmd...), nil
}
