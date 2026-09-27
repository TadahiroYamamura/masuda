package microvm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// kernelFixture copies vmlinuz-<v> into a fresh DataDir for every version in
// copied, and creates a modules directory for every version in installed.
func kernelFixture(t *testing.T, copied, installed []string) Host {
	t.Helper()
	dataDir := t.TempDir()
	for _, v := range copied {
		if err := os.WriteFile(filepath.Join(dataDir, "vmlinuz-"+v), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	modules := t.TempDir()
	for _, v := range installed {
		if err := os.MkdirAll(filepath.Join(modules, v), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	original := hostModulesRoot
	hostModulesRoot = modules
	t.Cleanup(func() { hostModulesRoot = original })
	return Host{DataDir: dataDir}
}

func TestFindKernelOrdersByVersionNotName(t *testing.T) {
	h := kernelFixture(t, []string{"6.8.0-99-generic", "6.8.0-142-generic"}, []string{"6.8.0-99-generic", "6.8.0-142-generic"})
	got, err := h.findKernel()
	if err != nil {
		t.Fatal(err)
	}
	if v := kernelVersionFromPath(got); v != "6.8.0-142-generic" {
		t.Errorf("findKernel() = %s, want 6.8.0-142-generic", v)
	}
}

// #57: the newest copy's modules are gone, so it cannot boot; an older copy
// whose modules are still installed can.
func TestFindKernelSkipsCopiesWhoseModulesAreGone(t *testing.T) {
	h := kernelFixture(t, []string{"6.8.0-138-generic", "6.8.0-142-generic"}, []string{"6.8.0-138-generic"})
	got, err := h.findKernel()
	if err != nil {
		t.Fatal(err)
	}
	if v := kernelVersionFromPath(got); v != "6.8.0-138-generic" {
		t.Errorf("findKernel() = %s, want the one whose modules are installed", v)
	}
}

func TestFindKernelNamesTheFixWhenNoCopyIsBootable(t *testing.T) {
	h := kernelFixture(t, []string{"6.8.0-138-generic"}, []string{"6.8.0-142-generic"})
	_, err := h.findKernel()
	if err == nil {
		t.Fatal("findKernel() = nil error with no bootable copy, want an error")
	}
	for _, want := range []string{"6.8.0-138-generic", "setup-vm-host.sh"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("findKernel() error = %q, want it to mention %q", err, want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"6.8.0-99-generic", "6.8.0-142-generic", -1},
		{"6.8.0-142-generic", "6.8.0-142-generic", 0},
		{"6.10.0-1-generic", "6.8.0-142-generic", 1},
		{"6.8.0-142-generic", "6.8.0-142-lowlatency", -1},
	} {
		got := compareVersions(c.a, c.b)
		if (got < 0) != (c.want < 0) || (got > 0) != (c.want > 0) {
			t.Errorf("compareVersions(%q, %q) = %d, want sign %d", c.a, c.b, got, c.want)
		}
	}
}
