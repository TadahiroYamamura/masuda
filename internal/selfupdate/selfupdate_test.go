package selfupdate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

func TestFetchLatestRelease(t *testing.T) {
	release := Release{
		TagName: "v0.2.0",
		Assets: []Asset{
			{Name: "masuda_linux_amd64", BrowserDownloadURL: "https://example.invalid/masuda_linux_amd64"},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/releases/latest" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(release)
	}))
	defer server.Close()

	got, err := FetchLatestRelease(server.URL, "owner/repo")
	if err != nil {
		t.Fatalf("FetchLatestRelease() error = %v", err)
	}
	if got.TagName != release.TagName {
		t.Fatalf("TagName = %q, want %q", got.TagName, release.TagName)
	}
	if len(got.Assets) != 1 || got.Assets[0].Name != "masuda_linux_amd64" {
		t.Fatalf("Assets = %+v, want one masuda_linux_amd64 asset", got.Assets)
	}
}

func TestFetchLatestReleaseNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	if _, err := FetchLatestRelease(server.URL, "owner/repo"); err == nil {
		t.Fatal("FetchLatestRelease() error = nil, want error on 404")
	}
}

func TestAssetName(t *testing.T) {
	if got, want := AssetName("linux", "amd64"), "masuda_linux_amd64"; got != want {
		t.Fatalf("AssetName() = %q, want %q", got, want)
	}
}

func TestFindAsset(t *testing.T) {
	release := Release{Assets: []Asset{
		{Name: "masuda_linux_amd64", BrowserDownloadURL: "https://example.invalid/amd64"},
		{Name: "masuda_linux_arm64", BrowserDownloadURL: "https://example.invalid/arm64"},
	}}

	asset, ok := FindAsset(release, "masuda_linux_arm64")
	if !ok || asset.BrowserDownloadURL != "https://example.invalid/arm64" {
		t.Fatalf("FindAsset(linux_arm64) = %+v, %v", asset, ok)
	}

	if _, ok := FindAsset(release, "masuda_darwin_arm64"); ok {
		t.Fatal("FindAsset(darwin_arm64) = ok, want not found")
	}
}

func TestDownloadAndReplace(t *testing.T) {
	newContent := []byte("new masuda binary")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(newContent)
	}))
	defer server.Close()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "masuda")
	if err := os.WriteFile(execPath, []byte("old masuda binary"), 0o755); err != nil {
		t.Fatalf("seeding execPath: %v", err)
	}

	if err := DownloadAndReplace(execPath, server.URL); err != nil {
		t.Fatalf("DownloadAndReplace() error = %v", err)
	}

	got, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatalf("reading execPath: %v", err)
	}
	if string(got) != string(newContent) {
		t.Fatalf("execPath content = %q, want %q", got, newContent)
	}

	info, err := os.Stat(execPath)
	if err != nil {
		t.Fatalf("stat execPath: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("execPath mode = %v, want 0755", info.Mode().Perm())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("dir has %d entries after replace, want 1 (no leftover temp file)", len(entries))
	}
}

func TestDownloadAndReplaceNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	dir := t.TempDir()
	execPath := filepath.Join(dir, "masuda")
	if err := os.WriteFile(execPath, []byte("old"), 0o755); err != nil {
		t.Fatalf("seeding execPath: %v", err)
	}

	if err := DownloadAndReplace(execPath, server.URL); err == nil {
		t.Fatal("DownloadAndReplace() error = nil, want error on 500")
	}

	got, err := os.ReadFile(execPath)
	if err != nil {
		t.Fatalf("reading execPath: %v", err)
	}
	if string(got) != "old" {
		t.Fatalf("execPath was modified despite download failure: %q", got)
	}
}

func TestBlockingWorkspaces(t *testing.T) {
	infos := []workspace.Info{
		{ID: "aaa111"},
		{ID: "bbb222"},
		{ID: "ccc333"},
	}
	running := map[string]bool{"bbb222": true}

	blocking := BlockingWorkspaces(infos, func(id string) bool { return running[id] })
	if len(blocking) != 1 || blocking[0].ID != "bbb222" {
		t.Fatalf("BlockingWorkspaces() = %+v, want only bbb222", blocking)
	}
}

func TestBlockingWorkspacesNoneRunning(t *testing.T) {
	infos := []workspace.Info{{ID: "aaa111"}, {ID: "bbb222"}}

	blocking := BlockingWorkspaces(infos, func(id string) bool { return false })
	if len(blocking) != 0 {
		t.Fatalf("BlockingWorkspaces() = %+v, want empty", blocking)
	}
}

func TestUpdateDockerfileFromTagBumpsPinnedVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Dockerfile")
	original := "# comment\nFROM tadahiroyamamura/masuda:v0.1.0\n\nRUN echo hi\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seeding Dockerfile: %v", err)
	}

	if err := UpdateDockerfileFromTag(path, "v0.2.0"); err != nil {
		t.Fatalf("UpdateDockerfileFromTag() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading Dockerfile: %v", err)
	}
	want := "# comment\nFROM tadahiroyamamura/masuda:v0.2.0\n\nRUN echo hi\n"
	if string(got) != want {
		t.Fatalf("Dockerfile = %q, want %q", got, want)
	}
}

func TestUpdateDockerfileFromTagBumpsLatest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(path, []byte("FROM tadahiroyamamura/masuda:latest\n"), 0o644); err != nil {
		t.Fatalf("seeding Dockerfile: %v", err)
	}

	if err := UpdateDockerfileFromTag(path, "v0.1.0"); err != nil {
		t.Fatalf("UpdateDockerfileFromTag() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading Dockerfile: %v", err)
	}
	if string(got) != "FROM tadahiroyamamura/masuda:v0.1.0\n" {
		t.Fatalf("Dockerfile = %q, want pinned to v0.1.0", got)
	}
}

func TestUpdateDockerfileFromTagNoMatchLeavesFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Dockerfile")
	original := "FROM ubuntu:24.04\nCOPY --from=tadahiroyamamura/masuda:v0.1.0 /opt/masuda /opt/masuda\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seeding Dockerfile: %v", err)
	}

	if err := UpdateDockerfileFromTag(path, "v0.2.0"); err != nil {
		t.Fatalf("UpdateDockerfileFromTag() error = %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading Dockerfile: %v", err)
	}
	if string(got) != original {
		t.Fatalf("Dockerfile = %q, want unchanged %q", got, original)
	}
}
