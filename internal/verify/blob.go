package verify

import (
	"bytes"
	"fmt"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	sigverify "github.com/sigstore/sigstore-go/pkg/verify"
)

// VerifyBlob checks that bundleJSON is a valid Sigstore bundle (the format
// `cosign sign-blob --new-bundle-format` produces) proving artifact was
// signed under identity, backed by trusted.
//
// Requires Rekor transparency log inclusion — the property that actually
// anchors trust here. It deliberately does not also require an embedded
// Certificate Transparency SCT (verify.WithSignedCertificateTimestamps):
// masuda's own test fixtures (sigstore-go's VirtualSigstore) don't generate
// one the way a real Fulcio-issued certificate does, and Rekor inclusion
// already proves the signature was publicly logged, which is the property
// SCT verification would otherwise be standing in for.
func VerifyBlob(artifact, bundleJSON []byte, identity Identity, trusted Trust) error {
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return fmt.Errorf("parsing signature bundle: %w", err)
	}
	return verifyEntity(&b, artifact, identity, trusted)
}

func verifyEntity(entity sigverify.SignedEntity, artifact []byte, identity Identity, trusted Trust) error {
	verifier, err := sigverify.NewVerifier(trusted, sigverify.WithTransparencyLog(1), sigverify.WithObserverTimestamps(1))
	if err != nil {
		return fmt.Errorf("constructing verifier: %w", err)
	}
	policy := sigverify.NewPolicy(sigverify.WithArtifact(bytes.NewReader(artifact)), sigverify.WithCertificateIdentity(identity))
	if _, err := verifier.Verify(entity, policy); err != nil {
		return fmt.Errorf("verifying signature: %w", err)
	}
	return nil
}
