package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1/sandboxv1connect"
	"github.com/TadahiroYamamura/masuda/internal/secrets"
)

// imageRecord は対象リポジトリのイメージのエントリを最後にビルドした結果。ListImagesの
// 「このエントリはどのbuild_idか」に答えるために`<DataDir>/images/<repo-hash>/<entry>.json`へ置く。
// sandboxのListImagesは名前でしか引けず、名前はただの表示用なのでこちらで持つ。
type imageRecord struct {
	BuildID   string    `json:"build_id"`
	OCIDigest string    `json:"oci_digest,omitempty"`
	BuiltAt   time.Time `json:"built_at"`
}

func imageRecordPath(dataDir, repoRoot, entry string) string {
	return filepath.Join(dataDir, "images", secrets.RepoHash(repoRoot), entry+".json")
}

func (b *backend) loadImageRecord(repoRoot, entry string) (imageRecord, bool) {
	var r imageRecord
	data, err := os.ReadFile(imageRecordPath(b.dataDir, repoRoot, entry))
	if err != nil || json.Unmarshal(data, &r) != nil || r.BuildID == "" {
		return imageRecord{}, false
	}
	return r, true
}

// ImageName はsandboxに記録するイメージの名前（"<repoのディレクトリ名>-<repo-hash>:<entry>"）。
func ImageName(repoRoot, entry string) string {
	return fmt.Sprintf("%s-%s:%s", filepath.Base(repoRoot), secrets.RepoHash(repoRoot), entry)
}

func (b *backend) buildImage(ctx context.Context, repoRoot, entry, contextDir string, log func(string) error) (string, error) {
	return BuildImage(ctx, b.sandbox, b.dataDir, repoRoot, entry, contextDir, log)
}

// BuildImage はcontextDir（Dockerfileを含む）をsandboxのBuildImageでビルドし、build_idを返す。
// 同じDockerイメージからの再ビルドはsandbox側で既存の資産を返すので、masudaは起動のたびに
// 呼んでよい（Dockerfileの変更を取りこぼさない方を選ぶ）。ログの各行はlogへ渡す（nil可）。
// ビルドの記録はdataDir（serveのDataDir）に書く。serveを通さずにビルドしたとき（`masuda
// privileged-command run`）も書くのは、sandboxのイメージの名前が指すものと、ListImagesが答える
// build_idを食い違わせないため。
func BuildImage(ctx context.Context, sb sandboxv1connect.SandboxServiceClient, dataDir, repoRoot, entry, contextDir string, log func(string) error) (string, error) {
	stream, err := sb.BuildImage(ctx, connect.NewRequest(&sandboxv1.BuildImageRequest{
		ContextDir: contextDir,
		Dockerfile: "Dockerfile",
		Name:       ImageName(repoRoot, entry),
	}))
	if err != nil {
		return "", err
	}
	defer stream.Close()
	var built *sandboxv1.Image
	for stream.Receive() {
		switch ev := stream.Msg().Event.(type) {
		case *sandboxv1.BuildImageEvent_LogLine:
			if log != nil {
				if err := log(ev.LogLine); err != nil {
					return "", err
				}
			}
		case *sandboxv1.BuildImageEvent_Built:
			built = ev.Built
		}
	}
	if err := stream.Err(); err != nil {
		return "", err
	}
	if built == nil || built.BuildId == "" {
		return "", errors.New("the sandbox service finished the build without an image")
	}
	rec := imageRecord{BuildID: built.BuildId, OCIDigest: built.OciDigest, BuiltAt: time.Now().UTC()}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", err
	}
	p := imageRecordPath(dataDir, repoRoot, entry)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	if err := writeFileAtomic(p, data, 0o600); err != nil {
		return "", err
	}
	return built.BuildId, nil
}

// writeFileAtomic はpathへ一時ファイルからrenameで書く。
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
