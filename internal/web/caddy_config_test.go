package web

import (
	"strings"
	"testing"

	"cms.ccvar.com/internal/platform"
)

func TestRenderCaddyDomainsFile(t *testing.T) {
	sites := []*platform.Site{
		{ID: 1, IsDefault: true},  // default site: never written
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
	out := renderCaddyDomainsFile(sites, domains, "127.0.0.1:8080")

	// Default site's domain must never appear.
	if strings.Contains(out, "default-bound.test") {
		t.Fatalf("default site domain leaked into Caddy config:\n%s", out)
	}
	// Case 1 — alias + 301: a redirect block for the alias, a serving block for the primary.
	if !strings.Contains(out, "www.a.com {\n\tredir https://a.com{uri} permanent\n}") {
		t.Errorf("missing 301 redirect block for www.a.com:\n%s", out)
	}
	if !strings.Contains(out, "a.com {\n\tencode zstd gzip\n\n\treverse_proxy 127.0.0.1:8080\n}") {
		t.Errorf("missing serving block for a.com:\n%s", out)
	}
	// Case 2 — alias + no redirect: both hosts share one serving block.
	if !strings.Contains(out, "b.com www.b.com {\n\tencode zstd gzip\n\n\treverse_proxy 127.0.0.1:8080\n}") {
		t.Errorf("missing shared block for b.com www.b.com:\n%s", out)
	}
	if strings.Contains(out, "redir https://b.com") {
		t.Errorf("no-redirect alias should not produce a redir block:\n%s", out)
	}
	// Case 3 — no alias: just the primary.
	if !strings.Contains(out, "c.com {\n\tencode zstd gzip\n\n\treverse_proxy 127.0.0.1:8080\n}") {
		t.Errorf("missing block for c.com:\n%s", out)
	}
	// Header comment marking gcms ownership; no on-demand/tls directive.
	if !strings.Contains(out, "由 gcms 自动生成") {
		t.Errorf("missing gcms ownership header:\n%s", out)
	}
	if strings.Contains(out, "tls ") || strings.Contains(out, "on_demand") {
		t.Errorf("unexpected tls/on_demand directive (template is ACME-default):\n%s", out)
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
