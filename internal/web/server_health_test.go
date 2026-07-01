package web

import "testing"

func TestParseLoadavg(t *testing.T) {
	l1, l5, l15, ok := parseLoadavg("0.52 0.58 0.59 1/234 5678\n")
	if !ok {
		t.Fatalf("parseLoadavg ok=false")
	}
	if l1 != 0.52 || l5 != 0.58 || l15 != 0.59 {
		t.Fatalf("parseLoadavg = %v %v %v", l1, l5, l15)
	}
	if _, _, _, ok := parseLoadavg("garbage"); ok {
		t.Fatalf("parseLoadavg(garbage) ok=true")
	}
}

func TestParseMeminfo(t *testing.T) {
	sample := "MemTotal:        1000000 kB\n" +
		"MemFree:          100000 kB\n" +
		"MemAvailable:     400000 kB\n" +
		"Buffers:           50000 kB\n"
	total, avail, ok := parseMeminfo(sample)
	if !ok {
		t.Fatalf("parseMeminfo ok=false")
	}
	if total != 1000000*1024 {
		t.Fatalf("total = %d", total)
	}
	if avail != 400000*1024 {
		t.Fatalf("available = %d", avail)
	}
	if _, _, ok := parseMeminfo("MemTotal: 100 kB\n"); ok {
		t.Fatalf("parseMeminfo without MemAvailable should be ok=false")
	}
}

func TestPct(t *testing.T) {
	if got := pct(0, 0); got != 0 {
		t.Fatalf("pct(0,0) = %d", got)
	}
	if got := pct(1, 2); got != 50 {
		t.Fatalf("pct(1,2) = %d", got)
	}
	if got := pct(999, 1000); got != 100 {
		t.Fatalf("pct(999,1000) = %d", got) // rounds up
	}
}

func TestHealthStatus(t *testing.T) {
	cases := []struct {
		name string
		h    ServerHealth
		want string
	}{
		{"unavailable", ServerHealth{Available: false}, "na"},
		{"idle", ServerHealth{Available: true, LoadPerCPU: 0.2, MemPct: 30, DiskPct: 40}, "ok"},
		{"busy load", ServerHealth{Available: true, LoadPerCPU: 0.8}, "warn"},
		{"overloaded", ServerHealth{Available: true, LoadPerCPU: 1.4}, "crit"},
		{"disk warn beats idle load", ServerHealth{Available: true, LoadPerCPU: 0.1, DiskPct: 92}, "warn"},
		{"mem crit beats warn load", ServerHealth{Available: true, LoadPerCPU: 0.8, MemPct: 96}, "crit"},
	}
	for _, c := range cases {
		if got := healthStatus(c.h); got != c.want {
			t.Errorf("%s: healthStatus = %q, want %q", c.name, got, c.want)
		}
	}
}
