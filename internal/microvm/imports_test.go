package microvm

import (
	"os/exec"
	"strings"
	"testing"
)

// allowedMasudaDeps are the masuda packages this one may depend on. Each
// has to be as free of masuda-specific knowledge as this package itself,
// because it would have to move out with it.
var allowedMasudaDeps = map[string]bool{
	"github.com/TadahiroYamamura/masuda/internal/microvm": true,
	"github.com/TadahiroYamamura/masuda/internal/rootfs":  true,
}

// TestNoMasudaImports keeps the boundary the package doc describes from
// eroding one convenient import at a time: reaching for internal/config or
// internal/workspace from here is exactly the coupling this package was
// split out to remove.
func TestNoMasudaImports(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasPrefix(dep, "github.com/TadahiroYamamura/masuda") && !allowedMasudaDeps[dep] {
			t.Errorf("internal/microvm depends on %s", dep)
		}
	}
}
