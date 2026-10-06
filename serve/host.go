package serve

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// hostResources はホストの物理メモリの総量（MiB）とCPU数を返す。メモリが分からなければ0。
// テストで差し替える。
var hostResources = func() (memoryMiB uint64, cpus int) {
	return hostMemoryMiB(), runtime.NumCPU()
}

// exceedsHost は、イメージのエントリimageのVMがホストの物理メモリの総量かCPU数を超えるなら、
// その理由を返す。空きではなく総量と比べる（空きは時々刻々と変わるので、起動の可否の判定に使えない）。
func exceedsHost(image string, memoryMiB, cpus uint32) []string {
	hostMem, hostCPUs := hostResources()
	var problems []string
	if hostMem > 0 && uint64(memoryMiB) > hostMem {
		problems = append(problems, fmt.Sprintf("image %s: memoryMiB %d exceeds the host's total memory (%d MiB)", image, memoryMiB, hostMem))
	}
	if hostCPUs > 0 && int(cpus) > hostCPUs {
		problems = append(problems, fmt.Sprintf("image %s: cpus %d exceeds the host's CPU count (%d)", image, cpus, hostCPUs))
	}
	return problems
}

// hostMemoryMiB は/proc/meminfoのMemTotalを返す。Linux以外には/proc/meminfoが無いので0を返し、
// メモリの比較をせずQEMUに任せる。Linuxで読めないのは想定外なので、ログに出す。
func hostMemoryMiB() uint64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		if runtime.GOOS == "linux" {
			log.Printf("masuda: reading the host's total memory: %v", err)
		}
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kib, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				log.Printf("masuda: parsing MemTotal in /proc/meminfo: %v", err)
				return 0
			}
			return kib / 1024
		}
	}
	if err := sc.Err(); err != nil {
		log.Printf("masuda: reading /proc/meminfo: %v", err)
		return 0
	}
	log.Printf("masuda: MemTotal not found in /proc/meminfo")
	return 0
}
