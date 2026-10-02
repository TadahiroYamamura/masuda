package serve

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	apiv1 "github.com/TadahiroYamamura/masuda/gen/masuda/api/v1"
)

// diskCheckEvery はワークスペース置き場の使用量を測る間隔。
const diskCheckEvery = 60 * time.Second

// diskWarningKind はディスク使用量の警告をWatchへ流すときのServeNotice.kind
// （workspace_idは空、全ワークスペース宛て）。
const diskWarningKind = "disk-warning"

// diskUsage は1回の計測結果。exportsはworkspacesの内数（`workspaces/<id>/exports/`の合計）。
type diskUsage struct {
	workspaces int64
	exports    int64
}

// watchDisk はdiskCheckEveryごとにcheckDiskを呼ぶ。
func (b *backend) watchDisk(ctx context.Context) {
	t := time.NewTicker(diskCheckEvery)
	defer t.Stop()
	for {
		b.checkDisk()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// checkDisk は使用量を測り、しきい値を超えたらログとWatchのイベントを出す。消すことはしない
// （何を残すかは利用者が決める。`masuda remove`はexportsを残す）。
//
// 警告は下回っていた状態から超えたときにだけ出す。60秒ごとに同じ警告を流し続けるとWatchの
// 再送バッファを埋めてしまうため。
func (b *backend) checkDisk() {
	limit := b.diskWarn
	u, err := measureDisk(filepath.Join(b.dataDir, "workspaces"))
	if err != nil {
		log.Printf("masuda: measuring disk usage: %v", err)
		return
	}
	b.diskMu.Lock()
	over := u.workspaces > limit
	warn := over && !b.diskOver
	b.diskOver = over
	b.diskMu.Unlock()
	if !warn {
		return
	}
	detail := fmt.Sprintf("%s uses %s (exports %s), over the warning threshold %s (diskWarnBytes in %s); remove finished workspaces with masuda remove",
		filepath.Join(b.dataDir, "workspaces"), humanBytes(u.workspaces), humanBytes(u.exports), humanBytes(limit), configName)
	log.Printf("masuda: %s", detail)
	b.events.publish(&apiv1.WorkspaceEvent{
		Event: &apiv1.WorkspaceEvent_Notice{Notice: &apiv1.ServeNotice{Kind: diskWarningKind, Detail: detail, Value: uint64(u.workspaces)}},
	})
}

// configName はメッセージで設定の置き場所を指すときの名前。
const configName = "$XDG_CONFIG_HOME/masuda/config.json"

// measureDisk はroot以下の通常ファイルの大きさの合計と、そのうち`<id>/exports/`以下の分を返す。
// 途中で消えたファイル（実行中のワークスペースの一時ファイル等）は数えない。
func measureDisk(root string) (diskUsage, error) {
	var u diskUsage
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		u.workspaces += info.Size()
		if rel, err := filepath.Rel(root, p); err == nil && isUnderExports(rel) {
			u.exports += info.Size()
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return u, err
}

// isUnderExports はroot（workspaces/）からの相対パスrelが`<id>/exports/`以下か。
func isUnderExports(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	return len(parts) >= 3 && parts[1] == "exports"
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
