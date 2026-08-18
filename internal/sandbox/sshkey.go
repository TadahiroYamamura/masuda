package sandbox

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"

	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// sshKeyFileName/sshPubKeyFileName are the fixed, host-level (not
// per-workspace) location of the keypair masuda uses to SSH into VM guests
// for `masuda chat` (Issue #31 M5-5, the VM path's equivalent of Docker's
// `docker exec -it ... tmux attach`). One keypair per masuda installation,
// not per workspace: workspaces are ephemeral and short-lived, so a
// per-workspace key would mean generating and injecting a fresh one into
// every rootfs build for no real isolation benefit -- the security boundary
// that actually matters is "can reach the private bridge network at all",
// not "which workspace".
const (
	sshKeyFileName    = "vm-ssh-key"
	sshPubKeyFileName = "vm-ssh-key.pub"
)

// SSHKeyPaths returns the private and public key file paths, under
// masuda's own XDG data directory (workspace.DataHome) -- the same base
// directory vmlinuz and workspace state already live under.
func SSHKeyPaths() (privatePath, publicPath string, err error) {
	dir, err := workspace.DataHome()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, sshKeyFileName), filepath.Join(dir, sshPubKeyFileName), nil
}

// EnsureSSHKeypair returns the existing keypair's paths, generating one
// first via GenerateSSHKeypair if neither file exists yet. Safe to call on
// every VM start -- after the first call anywhere on this host, every
// later call just finds the files already there.
func EnsureSSHKeypair() (privatePath, publicPath string, err error) {
	privatePath, publicPath, err = SSHKeyPaths()
	if err != nil {
		return "", "", err
	}
	if _, statErr := os.Stat(privatePath); statErr == nil {
		return privatePath, publicPath, nil
	}
	if err := GenerateSSHKeypair(); err != nil {
		return "", "", err
	}
	return privatePath, publicPath, nil
}

// GenerateSSHKeypair creates a fresh ed25519 keypair at SSHKeyPaths,
// overwriting whatever was there before. This is the actual "rotate"
// operation -- `masuda internal vm-ssh-key rotate` calls it directly and
// unconditionally; EnsureSSHKeypair calls it only as a first-use bootstrap,
// when no key exists yet.
//
// Only the *public* half is ever meant to leave the host: it gets injected
// into a VM's rootfs at build time (internal/rootfs.ExtraFile), the private
// half never does. The guest -- and anything running inside it, including
// the agent itself -- must never be able to read what would let it
// impersonate the host's SSH client.
//
// Rotating does not retroactively revoke access from already-built rootfs
// images or already-running VMs: they keep trusting the old public key
// until rebuilt/restarted. Accepted limitation (masuda workspaces are
// ephemeral, so stale trust cycles out on its own as workspaces are
// replaced) -- not a gap this function tries to close.
func GenerateSSHKeypair() error {
	privatePath, publicPath, err := SSHKeyPaths()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(privatePath), 0o755); err != nil {
		return fmt.Errorf("creating key directory: %w", err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generating keypair: %w", err)
	}

	block, err := ssh.MarshalPrivateKey(priv, "masuda-vm")
	if err != nil {
		return fmt.Errorf("marshaling private key: %w", err)
	}
	if err := os.WriteFile(privatePath, pem.EncodeToMemory(block), 0o600); err != nil {
		return fmt.Errorf("writing private key %s: %w", privatePath, err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return fmt.Errorf("converting public key: %w", err)
	}
	if err := os.WriteFile(publicPath, ssh.MarshalAuthorizedKey(sshPub), 0o644); err != nil {
		return fmt.Errorf("writing public key %s: %w", publicPath, err)
	}
	return nil
}
