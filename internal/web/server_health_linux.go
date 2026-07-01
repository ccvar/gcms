//go:build linux

package web

import (
	"os"
	"runtime"
	"syscall"
)

// readServerHealth reads host load / memory / disk from /proc and statfs on Linux.
func readServerHealth(diskPath string) ServerHealth {
	h := ServerHealth{Available: true, NumCPU: runtime.NumCPU()}
	if h.NumCPU < 1 {
		h.NumCPU = 1
	}

	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if l1, l5, l15, ok := parseLoadavg(string(b)); ok {
			h.Load1, h.Load5, h.Load15 = l1, l5, l15
		}
	}
	h.LoadPerCPU = h.Load1 / float64(h.NumCPU)

	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		if total, avail, ok := parseMeminfo(string(b)); ok && total > 0 {
			if avail > total {
				avail = total
			}
			h.MemTotal = total
			h.MemUsed = total - avail
			h.MemPct = pct(h.MemUsed, h.MemTotal)
		}
	}

	if diskPath == "" {
		diskPath = "."
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(diskPath, &st); err == nil && st.Blocks > 0 {
		bsize := uint64(st.Bsize)
		h.DiskTotal = st.Blocks * bsize
		free := st.Bavail * bsize
		if free > h.DiskTotal {
			free = h.DiskTotal
		}
		h.DiskUsed = h.DiskTotal - free
		h.DiskPct = pct(h.DiskUsed, h.DiskTotal)
	}

	h.Status = healthStatus(h)
	return h
}
