//go:build !linux

package web

import "runtime"

// readServerHealth reports "unavailable" off Linux (no /proc); the UI degrades to a
// "仅 Linux 可用" note. Dev machines (macOS) hit this path.
func readServerHealth(diskPath string) ServerHealth {
	_ = diskPath
	n := runtime.NumCPU()
	if n < 1 {
		n = 1
	}
	return ServerHealth{Available: false, NumCPU: n, Status: "na"}
}
