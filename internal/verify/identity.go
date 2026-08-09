// Package verify checks that release assets masuda downloads (CLI binaries,
// the built-in review perspectives zip) were built and signed by masuda's
// own GitHub Actions release workflow, using cosign keyless signing
// (Sigstore). Trust anchors itself in Sigstore's public Fulcio root CA —
// never a key masuda generates, stores, or rotates — which is how this
// sidesteps the chicken-and-egg problem of "who verifies the verifier"
// (Issue #18).
package verify

import (
	"fmt"
	"regexp"

	sigverify "github.com/sigstore/sigstore-go/pkg/verify"
)

// GitHubActionsOIDCIssuer is the OIDC issuer Fulcio records on certificates
// it mints for GitHub Actions workflow identities.
const GitHubActionsOIDCIssuer = "https://token.actions.githubusercontent.com"

// Identity is sigstore-go's verify.CertificateIdentity, aliased so callers
// (e.g. cmd/masuda) can name it without importing sigstore-go's verify
// package directly.
type Identity = sigverify.CertificateIdentity

// ExpectedIdentity is the certificate-identity policy every masuda release
// asset must have been signed under: a Fulcio certificate issued to repo's
// own release.yml workflow, for any tag it was triggered by (the SAN's tag
// suffix varies per release; the issuer and workflow path do not).
func ExpectedIdentity(repo string) (Identity, error) {
	sanRegex := `^https://github\.com/` + regexp.QuoteMeta(repo) + `/\.github/workflows/release\.yml@refs/tags/.+$`
	identity, err := sigverify.NewShortCertificateIdentity(GitHubActionsOIDCIssuer, "", "", sanRegex)
	if err != nil {
		return Identity{}, fmt.Errorf("building expected certificate identity for %s: %w", repo, err)
	}
	return identity, nil
}
