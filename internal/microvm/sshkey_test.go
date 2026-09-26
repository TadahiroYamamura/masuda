package microvm

import (
	"os"
	"testing"

	"golang.org/x/crypto/ssh"
)

// testHost points DataDir (and therefore SSHKeyPaths) at a throwaway
// directory, so these tests never touch a real developer's VM SSH key.
func testHost(t *testing.T) Host {
	t.Helper()
	return Host{DataDir: t.TempDir(), SSHKeyComment: "test"}
}

func TestEnsureSSHKeypairGeneratesOnce(t *testing.T) {
	h := testHost(t)

	privPath, pubPath, err := h.EnsureSSHKeypair()
	if err != nil {
		t.Fatalf("h.EnsureSSHKeypair() error = %v", err)
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
	privPath2, pubPath2, err := h.EnsureSSHKeypair()
	if err != nil {
		t.Fatalf("second h.EnsureSSHKeypair() error = %v", err)
	}
	privContent2, err := os.ReadFile(privPath2)
	if err != nil {
		t.Fatalf("reading private key (2nd): %v", err)
	}
	if string(privContent2) != string(privContent) {
		t.Error("h.EnsureSSHKeypair() regenerated an existing key instead of leaving it alone")
	}
	if pubPath2 != pubPath {
		t.Errorf("public key path changed between calls: %q vs %q", pubPath2, pubPath)
	}
}

func TestGenerateSSHKeypairRotates(t *testing.T) {
	h := testHost(t)

	if _, _, err := h.EnsureSSHKeypair(); err != nil {
		t.Fatalf("initial h.EnsureSSHKeypair() error = %v", err)
	}
	privPath, pubPath, err := h.SSHKeyPaths()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := h.GenerateSSHKeypair(); err != nil {
		t.Fatalf("h.GenerateSSHKeypair() error = %v", err)
	}
	after, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) == string(before) {
		t.Error("h.GenerateSSHKeypair() did not produce a different key (rotation should always regenerate)")
	}

	privContent, err := os.ReadFile(privPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ssh.ParsePrivateKey(privContent); err != nil {
		t.Errorf("rotated private key doesn't parse: %v", err)
	}
}
