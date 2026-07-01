package web

import (
	"strings"
	"testing"

	"cms.ccvar.com/internal/platform"
)

func TestCaddySiteFilename(t *testing.T) {
	ok := map[string]string{
		"ubnas.com":        "gcms-ubnas.com.caddy",
		"WWW.Example.COM":  "gcms-www.example.com.caddy", // lowercased
		"a-b.example.io":   "gcms-a-b.example.io.caddy",
		"  ubnas.com  ":    "gcms-ubnas.com.caddy", // trimmed
		"xn--fiq228c.test": "gcms-xn--fiq228c.test.caddy",
	}
	for in, want := range ok {
		if got := caddySiteFilename(in); got != want {
			t.Errorf("caddySiteFilename(%q) = %q, want %q", in, got, want)
		}
	}
	// Anything that could produce a weird/unsafe filename must be rejected.
	for _, bad := range []string{"", "a/b.com", "../etc/passwd", ".foo.com", "-foo.com", "a..b", "a b.com", "a;b", "a$b", "café.com"} {
		if got := caddySiteFilename(bad); got != "" {
			t.Errorf("caddySiteFilename(%q) = %q, want \"\" (rejected)", bad, got)
		}
	}
}

func TestRenderCaddyManifest(t *testing.T) {
	sites := []*platform.Site{
		{ID: 1, IsDefault: true},  // default site: never emitted
		{ID: 2, IsDefault: false}, // alias + 301
		{ID: 3, IsDefault: false}, // alias + no redirect
		{ID: 4, IsDefault: false}, // no alias
	}
	domains := []*platform.SiteDomain{
		{SiteID: 1, Scheme: "https", Host: "default-bound.test", IsPrimary: true, Enabled: true},
		{SiteID: 2, Scheme: "https", Host: "a.com", IsPrimary: true, Enabled: true},
		{SiteID: 2, Scheme: "https", Host: "www.a.com", RedirectToPrimary: true, Enabled: true},
		{SiteID: 3, Scheme: "https", Host: "b.com", IsPrimary: true, Enabled: true},
		{SiteID: 3, Scheme: "https", Host: "www.b.com", RedirectToPrimary: false, Enabled: true},
		{SiteID: 4, Scheme: "https", Host: "c.com", IsPrimary: true, Enabled: true},
	}
	out := renderCaddyManifest(sites, domains, "127.0.0.1:8080")

	if !strings.HasPrefix(out, caddyManifestSentinel+" sites=3\n") {
		t.Fatalf("manifest must start with the sentinel + site count:\n%s", out)
	}
	if strings.Contains(out, "default-bound.test") || strings.Contains(out, "gcms-default-bound.test.caddy") {
		t.Fatalf("default site must never appear:\n%s", out)
	}
	// One section header per non-default site, keyed by primary host.
	for _, hdr := range []string{"=== gcms-a.com.caddy ===", "=== gcms-b.com.caddy ===", "=== gcms-c.com.caddy ==="} {
		if !strings.Contains(out, hdr) {
			t.Errorf("missing section header %q:\n%s", hdr, out)
		}
	}
	// Case 1 — alias + 301.
	if !strings.Contains(out, "www.a.com {\n\tredir https://a.com{uri} permanent\n}") {
		t.Errorf("missing 301 redirect for www.a.com:\n%s", out)
	}
	if !strings.Contains(out, "a.com {\n\tencode zstd gzip\n\n\treverse_proxy 127.0.0.1:8080\n}") {
		t.Errorf("missing serving block for a.com:\n%s", out)
	}
	// Case 2 — alias + no redirect: shared block.
	if !strings.Contains(out, "b.com www.b.com {\n\tencode zstd gzip\n\n\treverse_proxy 127.0.0.1:8080\n}") {
		t.Errorf("missing shared block for b.com www.b.com:\n%s", out)
	}
	if strings.Contains(out, "redir https://b.com") {
		t.Errorf("no-redirect alias should not produce a redir block:\n%s", out)
	}
	// Case 3 — no alias.
	if !strings.Contains(out, "c.com {\n\tencode zstd gzip\n\n\treverse_proxy 127.0.0.1:8080\n}") {
		t.Errorf("missing block for c.com:\n%s", out)
	}
	// No tls/on_demand — template is ACME-default.
	if strings.Contains(out, "tls ") || strings.Contains(out, "on_demand") {
		t.Errorf("unexpected tls/on_demand directive:\n%s", out)
	}
}

func TestRenderCaddyManifestEmpty(t *testing.T) {
	// Zero non-default sites → sentinel only (so the sync script clears all orphans, and a
	// successful-but-empty response is distinguishable from a failure).
	out := renderCaddyManifest([]*platform.Site{{ID: 1, IsDefault: true}}, nil, "127.0.0.1:8080")
	if strings.TrimSpace(out) != caddyManifestSentinel+" sites=0" {
		t.Errorf("empty manifest = %q, want sentinel with sites=0", out)
	}
}

func TestCaddyConfigEndpointGuards(t *testing.T) {
	for _, h := range []string{"127.0.0.1:8080", "127.0.0.1", "localhost:8080", "[::1]:8080", "::1"} {
		if !hostIsLoopback(h) {
			t.Errorf("hostIsLoopback(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"ubnas.com", "ubnas.com:443", "www.ubnas.com", "8.8.8.8:80", ""} {
		if hostIsLoopback(h) {
			t.Errorf("hostIsLoopback(%q) = true, want false", h)
		}
	}
	if !remoteIsLoopback("127.0.0.1:5555") || !remoteIsLoopback("[::1]:5555") {
		t.Error("remoteIsLoopback should accept loopback RemoteAddr")
	}
	if remoteIsLoopback("203.0.113.7:5555") {
		t.Error("remoteIsLoopback should reject a public RemoteAddr")
	}
}

func TestCaddyReverseProxyTarget(t *testing.T) {
	t.Setenv("ADDR", ":8080")
	if got := caddyReverseProxyTarget(); got != "127.0.0.1:8080" {
		t.Errorf("ADDR=:8080 -> %q, want 127.0.0.1:8080", got)
	}
	t.Setenv("ADDR", "127.0.0.1:9000")
	if got := caddyReverseProxyTarget(); got != "127.0.0.1:9000" {
		t.Errorf("ADDR=127.0.0.1:9000 -> %q, want unchanged", got)
	}
}
