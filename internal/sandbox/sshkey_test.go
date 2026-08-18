package sandbox

import (
	"os"
	"testing"

	"golang.org/x/crypto/ssh"
)

// withIsolatedDataHome points workspace.DataHome (and therefore
// SSHKeyPaths) at a throwaway directory for the duration of the test, so
// these tests never touch a real developer's actual VM SSH key.
func withIsolatedDataHome(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
}

func TestEnsureSSHKeypairGeneratesOnce(t *testing.T) {
	withIsolatedDataHome(t)

	privPath, pubPath, err := EnsureSSHKeypair()
	if err != nil {
		t.Fatalf("EnsureSSHKeypair() error = %v", err)
	}
	privContent, err := os.ReadFile(privPath)
	if err != nil {
		t.Fatalf("reading private key: %v", err)
	}
	pubContent, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatalf("reading public key: %v", err)
	}
	if _, err := ssh.ParsePrivateKey(privContent); err != nil {
		t.Errorf("private key doesn't parse: %v", err)
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey(pubContent); err != nil {
		t.Errorf("public key doesn't parse: %v", err)
	}

	// Second call must be a no-op: same key, not a fresh one.
	privPath2, pubPath2, err := EnsureSSHKeypair()
	if err != nil {
		t.Fatalf("second EnsureSSHKeypair() error = %v", err)
	}
	privContent2, err := os.ReadFile(privPath2)
	if err != nil {
		t.Fatalf("reading private key (2nd): %v", err)
	}
	if string(privContent2) != string(privContent) {
		t.Error("EnsureSSHKeypair() regenerated an existing key instead of leaving it alone")
	}
	if pubPath2 != pubPath {
		t.Errorf("public key path changed between calls: %q vs %q", pubPath2, pubPath)
	}
}

func TestGenerateSSHKeypairRotates(t *testing.T) {
	withIsolatedDataHome(t)

	if _, _, err := EnsureSSHKeypair(); err != nil {
		t.Fatalf("initial EnsureSSHKeypair() error = %v", err)
	}
	privPath, pubPath, err := SSHKeyPaths()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := GenerateSSHKeypair(); err != nil {
		t.Fatalf("GenerateSSHKeypair() error = %v", err)
	}
	after, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) == string(before) {
		t.Error("GenerateSSHKeypair() did not produce a different key (rotation should always regenerate)")
	}

	privContent, err := os.ReadFile(privPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ssh.ParsePrivateKey(privContent); err != nil {
		t.Errorf("rotated private key doesn't parse: %v", err)
	}
}
