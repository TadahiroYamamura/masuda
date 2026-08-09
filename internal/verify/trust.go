package verify

import (
	"fmt"

	"github.com/sigstore/sigstore-go/pkg/root"
)

// Trust is sigstore-go's root.TrustedMaterial, aliased so callers (e.g.
// cmd/masuda) can name it without importing sigstore-go's root package
// directly.
type Trust = root.TrustedMaterial

// TrustedMaterial fetches Sigstore's public trusted root (Fulcio CA, CT log
// keys, Rekor keys) from Sigstore's own TUF repository. masuda never
// generates, stores, or distributes a trust anchor of its own — Sigstore's
// published root is the entire trust bootstrap (Issue #18's chicken-and-egg
// concern). No fallback to a bundled/stale copy: a TUF fetch failure must
// surface as an error here rather than silently verifying against
// out-of-date material.
func TrustedMaterial() (Trust, error) {
	tr, err := root.FetchTrustedRoot()
	if err != nil {
		return nil, fmt.Errorf("fetching Sigstore trusted root: %w", err)
	}
	return tr, nil
}
