package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// ServerHealth is a host-level resource snapshot shown on the platform 站点管理 page.
// Real metrics are only collected on Linux (reads /proc); other platforms report
// Available=false and the UI shows a "仅 Linux 可用" note.
type ServerHealth struct {
	Available  bool    `json:"available"`
	NumCPU     int     `json:"num_cpu"`
	Load1      float64 `json:"load1"`
	Load5      float64 `json:"load5"`
	Load15     float64 `json:"load15"`
	LoadPerCPU float64 `json:"load_per_cpu"`
	MemUsed    uint64  `json:"mem_used"`  // bytes
	MemTotal   uint64  `json:"mem_total"` // bytes
	MemPct     int     `json:"mem_pct"`
	DiskUsed   uint64  `json:"disk_used"`  // bytes
	DiskTotal  uint64  `json:"disk_total"` // bytes
	DiskPct    int     `json:"disk_pct"`
	Status     string  `json:"status"` // ok | warn | crit | na
}

// serverHealthSnapshot returns the current host snapshot (platform-specific reader).
func (s *Server) serverHealthSnapshot() ServerHealth {
	return readServerHealth(s.serverDiskPath())
}

// serverDiskPath picks the filesystem to report disk usage for: the upload dir if
// known (co-located with the data dir on a normal install), else the working dir.
func (s *Server) serverDiskPath() string {
	if s != nil {
		if p := strings.TrimSpace(s.uploadDir); p != "" {
			return p
		}
	}
	return "."
}

// adminServerHealth serves the host snapshot as JSON for the sites-page pill poll.
func (s *Server) adminServerHealth(w http.ResponseWriter, r *http.Request) {
	h := s.serverHealthSnapshot()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(h)
}

// parseLoadavg extracts the 1/5/15-minute load averages from /proc/loadavg content.
func parseLoadavg(s string) (l1, l5, l15 float64, ok bool) {
	f := strings.Fields(s)
	if len(f) < 3 {
		return 0, 0, 0, false
	}
	var err error
	if l1, err = strconv.ParseFloat(f[0], 64); err != nil {
		return 0, 0, 0, false
	}
	if l5, err = strconv.ParseFloat(f[1], 64); err != nil {
		return 0, 0, 0, false
	}
	if l15, err = strconv.ParseFloat(f[2], 64); err != nil {
		return 0, 0, 0, false
	}
	return l1, l5, l15, true
}

// parseMeminfo extracts total and available memory (bytes) from /proc/meminfo content.
func parseMeminfo(s string) (total, available uint64, ok bool) {
	var haveTotal, haveAvail bool
	for _, line := range strings.Split(s, "\n") {
		key, val, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		fields := strings.Fields(val) // e.g. "16384256 kB"
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(key) {
		case "MemTotal":
			total = kb * 1024
			haveTotal = true
		case "MemAvailable":
			available = kb * 1024
			haveAvail = true
		}
		if haveTotal && haveAvail {
			break
		}
	}
	return total, available, haveTotal && haveAvail
}

// pct returns used/total as a rounded integer percentage (0 when total is 0).
func pct(used, total uint64) int {
	if total == 0 {
		return 0
	}
	return int((used*100 + total/2) / total)
}

// healthStatus derives the traffic-light status from load-per-core, then escalates
// on memory or disk pressure.
func healthStatus(h ServerHealth) string {
	if !h.Available {
		return "na"
	}
	status := "ok"
	switch {
	case h.LoadPerCPU >= 1.0:
		status = "crit"
	case h.LoadPerCPU >= 0.7:
		status = "warn"
	}
	rank := map[string]int{"ok": 0, "warn": 1, "crit": 2}
	worse := func(s string) {
		if rank[s] > rank[status] {
			status = s
		}
	}
	switch {
	case h.MemPct >= 95:
		worse("crit")
	case h.MemPct >= 85:
		worse("warn")
	}
	switch {
	case h.DiskPct >= 97:
		worse("crit")
	case h.DiskPct >= 90:
		worse("warn")
	}
	return status
}
