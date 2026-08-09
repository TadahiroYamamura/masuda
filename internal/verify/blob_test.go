package verify

import (
	"testing"

	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	sigverify "github.com/sigstore/sigstore-go/pkg/verify"
)

const testWorkflowIdentity = "https://github.com/TadahiroYamamura/masuda/.github/workflows/release.yml@refs/tags/v0.4.0"

func mustExpectedIdentity(t *testing.T, repo string) sigverify.CertificateIdentity {
	t.Helper()
	id, err := ExpectedIdentity(repo)
	if err != nil {
		t.Fatalf("ExpectedIdentity() error = %v", err)
	}
	return id
}

func TestVerifyEntityHappyPath(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatalf("NewVirtualSigstore() error = %v", err)
	}
	artifact := []byte("masuda_linux_amd64 contents")
	entity, err := vs.Sign(testWorkflowIdentity, GitHubActionsOIDCIssuer, artifact)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	identity := mustExpectedIdentity(t, "TadahiroYamamura/masuda")
	if err := verifyEntity(entity, artifact, identity, vs); err != nil {
		t.Fatalf("verifyEntity() error = %v, want nil", err)
	}
}

func TestVerifyEntityTamperedArtifact(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatalf("NewVirtualSigstore() error = %v", err)
	}
	entity, err := vs.Sign(testWorkflowIdentity, GitHubActionsOIDCIssuer, []byte("real contents"))
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	identity := mustExpectedIdentity(t, "TadahiroYamamura/masuda")
	if err := verifyEntity(entity, []byte("tampered contents"), identity, vs); err == nil {
		t.Fatal("verifyEntity() with tampered artifact succeeded, want error")
	}
}

func TestVerifyEntityWrongRepoIdentity(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatalf("NewVirtualSigstore() error = %v", err)
	}
	artifact := []byte("contents")
	// Signed under a different repository's workflow identity.
	otherIdentity := "https://github.com/someone-else/other-repo/.github/workflows/release.yml@refs/tags/v1.0.0"
	entity, err := vs.Sign(otherIdentity, GitHubActionsOIDCIssuer, artifact)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	identity := mustExpectedIdentity(t, "TadahiroYamamura/masuda")
	if err := verifyEntity(entity, artifact, identity, vs); err == nil {
		t.Fatal("verifyEntity() with mismatched repo identity succeeded, want error")
	}
}

func TestVerifyEntityWrongIssuer(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatalf("NewVirtualSigstore() error = %v", err)
	}
	artifact := []byte("contents")
	entity, err := vs.Sign(testWorkflowIdentity, "https://not-github-actions.example", artifact)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	identity := mustExpectedIdentity(t, "TadahiroYamamura/masuda")
	if err := verifyEntity(entity, artifact, identity, vs); err == nil {
		t.Fatal("verifyEntity() with mismatched OIDC issuer succeeded, want error")
	}
}

func TestVerifyEntityNotFromReleaseWorkflow(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatalf("NewVirtualSigstore() error = %v", err)
	}
	artifact := []byte("contents")
	// Right repo, but a different workflow file -- must not satisfy the
	// SAN regex, which is pinned to release.yml specifically.
	wrongWorkflow := "https://github.com/TadahiroYamamura/masuda/.github/workflows/ci.yml@refs/tags/v0.4.0"
	entity, err := vs.Sign(wrongWorkflow, GitHubActionsOIDCIssuer, artifact)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	identity := mustExpectedIdentity(t, "TadahiroYamamura/masuda")
	if err := verifyEntity(entity, artifact, identity, vs); err == nil {
		t.Fatal("verifyEntity() signed by a non-release workflow succeeded, want error")
	}
}

func TestVerifyBlobRejectsInvalidBundleJSON(t *testing.T) {
	vs, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatalf("NewVirtualSigstore() error = %v", err)
	}
	identity := mustExpectedIdentity(t, "TadahiroYamamura/masuda")

	err = VerifyBlob([]byte("contents"), []byte("not a bundle"), identity, vs)
	if err == nil {
		t.Fatal("VerifyBlob() with invalid bundle JSON succeeded, want error")
	}
}
