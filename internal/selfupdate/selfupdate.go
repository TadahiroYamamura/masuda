// Package selfupdate implements `masuda update`'s mechanics (ADR-0032):
// fetching masuda's latest GitHub Release, picking the asset for the running
// OS/architecture, and replacing the current executable in place. masuda's
// own repository is public, so none of this needs authentication.
package selfupdate

import (
	"archive/zip"
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

func fetchRelease(url string) (Release, error) {
	resp, err := http.Get(url)
	if err != nil {
		return Release{}, fmt.Errorf("fetching release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("fetching release: %s returned %s", url, resp.Status)
	}
	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return Release{}, fmt.Errorf("parsing release response: %w", err)
	}
	return release, nil
}

// FetchLatestRelease fetches repo's latest release from apiBase, unauthenticated
// — masuda's own repository is public, so no token is required.
func FetchLatestRelease(apiBase, repo string) (Release, error) {
	return fetchRelease(fmt.Sprintf("%s/repos/%s/releases/latest", apiBase, repo))
}

// FetchReleaseByTag fetches repo's release tagged tag from apiBase,
// unauthenticated. Used by `masuda init` (ADR-0033) to pin what it
// materializes to the release matching its own embedded version, rather
// than always tracking whatever is newest.
func FetchReleaseByTag(apiBase, repo, tag string) (Release, error) {
	return fetchRelease(fmt.Sprintf("%s/repos/%s/releases/tags/%s", apiBase, repo, tag))
}

// AssetName returns the release asset name for goos/goarch, matching
// .github/workflows/release.yml's build matrix naming (masuda_<goos>_<goarch>).
func AssetName(goos, goarch string) string {
	return fmt.Sprintf("masuda_%s_%s", goos, goarch)
}

// BundleAssetName returns the release asset name of assetName's detached
// Sigstore signature bundle (ADR-0038) — the cosign keyless "sign-blob
// --bundle" output CI attaches alongside every binary/reviews-zip asset.
func BundleAssetName(assetName string) string {
	return assetName + ".bundle"
}

// ReviewsAssetName is the release asset containing masuda's built-in review
// perspectives (internal/perspectives/builtin/*.md, zipped by CI's `reviews`
// job — ADR-0033).
const ReviewsAssetName = "masuda_reviews.zip"

// FindAsset returns the asset named name within release, if present.
func FindAsset(release Release, name string) (Asset, bool) {
	for _, asset := range release.Assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return Asset{}, false
}

// DownloadBytes downloads url and returns its body in full. Used for small
// assets (signature bundles, release API responses) that are verified or
// parsed in memory rather than streamed to disk.
func DownloadBytes(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: returned %s", url, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	return data, nil
}

// DownloadAndReplace downloads url into execPath's directory and atomically
// replaces execPath with it (download-then-rename, both on the same
// filesystem, so the running process is never left pointing at a partial
// file). The temp file is cleaned up if any step before the rename fails.
//
// verify (ADR-0038) is called with the fully downloaded content before the
// rename, so a signature verification failure leaves execPath untouched —
// there is no window where an unverified binary is in place. verify may be
// nil where verification is out of scope (e.g. tests exercising the
// download/replace mechanics in isolation); every real call site must pass
// a real verifier.
func DownloadAndReplace(execPath, url string, verify func(data []byte) error) error {
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

	var buf bytes.Buffer
	if _, err := io.Copy(io.MultiWriter(tmp, &buf), resp.Body); err != nil {
		tmp.Close()
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	if verify != nil {
		if err := verify(buf.Bytes()); err != nil {
			return fmt.Errorf("verifying %s: %w", url, err)
		}
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

// SyncReviews downloads url (a zip of built-in perspective .md files,
// ReviewsAssetName within a Release), creates reviewsDir if needed, and
// writes every entry that doesn't already exist there. Existing files
// (customized, or intentionally disabled via "enable: false" — ADR-0033)
// are never touched, so this is safe to run repeatedly: it only ever adds
// perspectives newly introduced since reviewsDir was last synced. Returns
// the filenames actually written.
//
// verify (ADR-0038) is called with the fully downloaded zip bytes before
// any file is extracted; on error, nothing under reviewsDir is touched.
// verify may be nil where verification is out of scope (tests exercising
// the sync mechanics in isolation) — every real call site must pass a real
// verifier.
func SyncReviews(url, reviewsDir string, verify func(data []byte) error) ([]string, error) {
	data, err := DownloadBytes(url)
	if err != nil {
		return nil, err
	}
	if verify != nil {
		if err := verify(data); err != nil {
			return nil, fmt.Errorf("verifying %s: %w", url, err)
		}
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("reading %s as zip: %w", url, err)
	}
	if err := os.MkdirAll(reviewsDir, 0o755); err != nil {
		return nil, err
	}

	var added []string
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := filepath.Base(f.Name)
		dest := filepath.Join(reviewsDir, name)
		if _, err := os.Stat(dest); err == nil {
			continue // already present -- never overwrite
		} else if !os.IsNotExist(err) {
			return nil, err
		}

		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("reading %s from %s: %w", f.Name, url, err)
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("reading %s from %s: %w", f.Name, url, err)
		}
		if err := os.WriteFile(dest, content, 0o644); err != nil {
			return nil, err
		}
		added = append(added, name)
	}
	return added, nil
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
