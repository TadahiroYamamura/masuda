// Package selfupdate implements `masuda update`'s mechanics (ADR-0032):
// fetching masuda's latest GitHub Release, picking the asset for the running
// OS/architecture, and replacing the current executable in place. masuda's
// own repository is public, so none of this needs authentication.
package selfupdate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/TadahiroYamamura/masuda/internal/workspace"
)

// DefaultRepo is masuda's own GitHub repository, "<owner>/<name>".
const DefaultRepo = "TadahiroYamamura/masuda"

// DefaultDockerImage is the Docker Hub repository masuda's sandbox base
// image is published to (ADR-0032).
const DefaultDockerImage = "tadahiroyamamura/masuda"

// DefaultAPIBase is the GitHub REST API's base URL. Overridable via
// FetchLatestRelease's apiBase parameter so tests can point at an
// httptest.Server instead of the real network.
const DefaultAPIBase = "https://api.github.com"

// Asset is one file attached to a GitHub Release.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Release is the subset of GitHub's Release API response masuda needs.
type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

// FetchLatestRelease fetches repo's latest release from apiBase, unauthenticated
// — masuda's own repository is public, so no token is required.
func FetchLatestRelease(apiBase, repo string) (Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", apiBase, repo)
	resp, err := http.Get(url)
	if err != nil {
		return Release{}, fmt.Errorf("fetching latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("fetching latest release: %s returned %s", url, resp.Status)
	}
	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return Release{}, fmt.Errorf("parsing latest release response: %w", err)
	}
	return release, nil
}

// AssetName returns the release asset name for goos/goarch, matching
// .github/workflows/release.yml's build matrix naming (masuda_<goos>_<goarch>).
func AssetName(goos, goarch string) string {
	return fmt.Sprintf("masuda_%s_%s", goos, goarch)
}

// FindAsset returns the asset named name within release, if present.
func FindAsset(release Release, name string) (Asset, bool) {
	for _, asset := range release.Assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return Asset{}, false
}

// DownloadAndReplace downloads url into execPath's directory and atomically
// replaces execPath with it (download-then-rename, both on the same
// filesystem, so the running process is never left pointing at a partial
// file). The temp file is cleaned up if any step before the rename fails.
func DownloadAndReplace(execPath, url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: returned %s", url, resp.Status)
	}

	dir := filepath.Dir(execPath)
	tmp, err := os.CreateTemp(dir, ".masuda-update-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("making %s executable: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, execPath); err != nil {
		return fmt.Errorf("replacing %s: %w", execPath, err)
	}
	return nil
}

// RebuildDockerfile builds dockerfilePath (typically <repo>/.masuda/Dockerfile)
// against contextDir with --pull, so FROM always resolves to the latest
// published base image, and tags the result as tag.
func RebuildDockerfile(dockerfilePath, contextDir, tag string, stdout, stderr io.Writer) error {
	cmd := exec.Command("docker", "build", "--pull", "-t", tag, "-f", dockerfilePath, contextDir)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build -f %s: %w", dockerfilePath, err)
	}
	return nil
}

var dockerfileFromRe = regexp.MustCompile(`(?m)^FROM\s+` + regexp.QuoteMeta(DefaultDockerImage) + `:\S+`)

// UpdateDockerfileFromTag rewrites dockerfilePath's "FROM
// tadahiroyamamura/masuda:<tag>" line (materialized by masuda init) to
// reference newTag, so masuda update can bump the pinned base image version
// without floating on "latest" (ADR-0032: pinning keeps repeated
// `docker build` runs of an unchanged Dockerfile reproducible between
// updates). No-op if the file has no such FROM line -- e.g. the user
// replaced it with their own base image entirely, which masuda update must
// not overwrite.
func UpdateDockerfileFromTag(dockerfilePath, newTag string) error {
	data, err := os.ReadFile(dockerfilePath)
	if err != nil {
		return err
	}
	updated := dockerfileFromRe.ReplaceAll(data, []byte("FROM "+DefaultDockerImage+":"+newTag))
	if bytes.Equal(updated, data) {
		return nil
	}
	return os.WriteFile(dockerfilePath, updated, 0o644)
}

// BlockingWorkspaces returns the subset of infos isRunning reports as still
// running — `masuda update` refuses to proceed while any exist (ADR-0032):
// the CLI binary is a single executable shared machine-wide, so replacing it
// while another workspace's host loop or sandbox container is mid-flight,
// regardless of which repository that workspace targets, is unsafe.
// isRunning is injected so callers can compose it from
// internal/hostloop.IsRunning / internal/sandbox.IsRunning without this
// package importing either (and so tests can stub it without real
// tmux/docker state).
func BlockingWorkspaces(infos []workspace.Info, isRunning func(id string) bool) []workspace.Info {
	var blocking []workspace.Info
	for _, info := range infos {
		if isRunning(info.ID) {
			blocking = append(blocking, info)
		}
	}
	return blocking
}
