package serve

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	sandboxv1 "github.com/TadahiroYamamura/masuda/gen/masuda/sandbox/v1"
	"github.com/TadahiroYamamura/masuda/internal/guest"
)

// resourceLimits はVMの中の資源の警告のしきい値（#88）。
type resourceLimits struct {
	// memoryPercent は、使えるメモリが合計のこの割合（%）を下回ったら警告する値。
	memoryPercent int
	// diskPercent は、ファイルシステムの使用率（%）がこれを超えたら警告する値。
	diskPercent int
}

// resourceProbe はゲストで流す測り方。ゲストのClaude Codeやワークフローの協力を要らないよう、
// /procとdfだけを読む。区切りの行で3つの出力を分ける。dfの`-P`は1つのファイルシステムを
// 必ず1行に出させるため（長いデバイス名で折り返さない）。
const resourceProbe = `cat /proc/meminfo; echo '--vmstat'; cat /proc/vmstat; echo '--df'; df -Pk / /tmp`

// vmResources はゲストで測った値。
type vmResources struct {
	memTotalKB int64
	memAvailKB int64
	// oomKills はVMの起動からのOOM killerの発動回数（/proc/vmstatのoom_kill）。
	oomKills int64
	disks    []diskCapacity
}

// diskCapacity はdfの1行（マウント先と使用率）。
type diskCapacity struct {
	mount   string
	percent int
}

// parseVMResources はresourceProbeの出力を読む。メモリの合計・使えるメモリ・dfの行のどれかが
// 読めなければエラー（測れていないものを「余裕がある」とみなさないため）。oom_killは
// カーネルによっては無いので、無ければ0。
func parseVMResources(out string) (vmResources, error) {
	var r vmResources
	section := "meminfo"
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch line {
		case "--vmstat":
			section = "vmstat"
			continue
		case "--df":
			section = "df"
			continue
		}
		f := strings.Fields(line)
		switch section {
		case "meminfo":
			if len(f) < 2 {
				continue
			}
			switch f[0] {
			case "MemTotal:":
				r.memTotalKB, _ = strconv.ParseInt(f[1], 10, 64)
			case "MemAvailable:":
				r.memAvailKB, _ = strconv.ParseInt(f[1], 10, 64)
			}
		case "vmstat":
			if len(f) == 2 && f[0] == "oom_kill" {
				r.oomKills, _ = strconv.ParseInt(f[1], 10, 64)
			}
		case "df":
			// Filesystem 1024-blocks Used Available Capacity Mounted-on
			if len(f) < 6 || !strings.HasSuffix(f[4], "%") {
				continue
			}
			p, err := strconv.Atoi(strings.TrimSuffix(f[4], "%"))
			if err != nil {
				continue
			}
			mount := strings.Join(f[5:], " ")
			dup := false
			for _, d := range r.disks {
				dup = dup || d.mount == mount
			}
			// /tmpが別のマウントでなければ、dfは同じ`/`を2回出す
			if !dup {
				r.disks = append(r.disks, diskCapacity{mount: mount, percent: p})
			}
		}
	}
	if r.memTotalKB <= 0 {
		return r, errors.New("no MemTotal in /proc/meminfo")
	}
	if r.memAvailKB <= 0 {
		return r, errors.New("no MemAvailable in /proc/meminfo")
	}
	if len(r.disks) == 0 {
		return r, errors.New("no filesystem in the df output")
	}
	return r, nil
}

// warnings はしきい値を超えているものを、公開APIのActivity.resource_warningに載せる形で返す。
// 数値を含めないのは、超えている間は同じ文字列のままにして、statusイベントを測るたびに
// 出さないため（数値はログに出す）。
func (r vmResources) warnings(l resourceLimits) []string {
	var out []string
	if r.memAvailKB*100 < r.memTotalKB*int64(l.memoryPercent) {
		out = append(out, "memory low")
	}
	for _, d := range r.disks {
		if d.percent > l.diskPercent {
			out = append(out, fmt.Sprintf("%s over %d%%", d.mount, l.diskPercent))
		}
	}
	if r.oomKills > 0 {
		out = append(out, fmt.Sprintf("oom-kill x%d", r.oomKills))
	}
	return out
}

// summary はログ用の測った値の要約。
func (r vmResources) summary() string {
	parts := []string{fmt.Sprintf("memory %s of %s available", humanBytes(r.memAvailKB<<10), humanBytes(r.memTotalKB<<10))}
	for _, d := range r.disks {
		parts = append(parts, fmt.Sprintf("%s %d%% used", d.mount, d.percent))
	}
	parts = append(parts, fmt.Sprintf("oom_kill %d", r.oomKills))
	return strings.Join(parts, ", ")
}

// checkResources はidのVMの中の資源を測り、警告が変わったときだけ活動に反映してログに出す。
// 測れなかったときは前の表示を残し、ログだけ出す（VMの一時的な詰まりで警告が消えないように）。
func (b *backend) checkResources(parent context.Context, id string) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	res, err := guest.Exec(ctx, b.sandbox, &sandboxv1.ExecRequest{
		Id:   id,
		Argv: []string{"/bin/sh", "-c", resourceProbe},
		Cwd:  "/",
	})
	if err == nil && res.ExitCode != 0 {
		err = fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	var r vmResources
	if err == nil {
		r, err = parseVMResources(string(res.Stdout))
	}
	if err != nil {
		if parent.Err() == nil {
			log.Printf("masuda: %s: measuring resources in the VM: %v", id, err)
		}
		return
	}
	warning := strings.Join(r.warnings(b.vmLimits), ", ")
	changed := false
	b.acts.update(id, func(act *activity) {
		changed = act.resourceWarning != warning
		act.resourceWarning = warning
	})
	if !changed {
		return
	}
	if warning == "" {
		log.Printf("masuda: %s: resources in the VM recovered (%s)", id, r.summary())
		return
	}
	log.Printf("masuda: %s: resources in the VM are strained: %s (%s; vmMemoryWarnPercent/vmDiskWarnPercent in %s)", id, warning, r.summary(), configName)
}
