package web

import (
	"strings"
	"testing"

	"cms.ccvar.com/internal/platform"
)

func TestRenderCaddyDomainsSection(t *testing.T) {
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
	out := renderCaddyDomainsSection(sites, domains, "127.0.0.1:8080")

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
	// Markers present.
	if !strings.Contains(out, caddyDomainsStart) || !strings.Contains(out, caddyDomainsEnd) {
		t.Errorf("section missing managed markers")
	}
}

func TestSpliceCaddyManagedSection(t *testing.T) {
	section := caddyDomainsStart + "\nexample.com {\n\treverse_proxy 127.0.0.1:8080\n}\n" + caddyDomainsEnd

	// Empty file -> just the section.
	if got := spliceCaddyManagedSection("", section); !strings.Contains(got, "example.com") {
		t.Errorf("empty splice dropped the section")
	}

	// No markers -> append, preserving the existing hand-written config.
	existing := "other.com {\n\treverse_proxy 127.0.0.1:9999\n}\n"
	got := spliceCaddyManagedSection(existing, section)
	if !strings.Contains(got, "other.com") || !strings.Contains(got, "example.com") {
		t.Errorf("append lost existing or new content:\n%s", got)
	}

	// Markers present -> replace only between them; preamble and trailing config survive.
	withMarkers := "keep-before.com {\n\treverse_proxy x\n}\n\n" +
		caddyDomainsStart + "\nOLD.com {\n\treverse_proxy y\n}\n" + caddyDomainsEnd +
		"\n\nkeep-after.com {\n\treverse_proxy z\n}\n"
	got = spliceCaddyManagedSection(withMarkers, section)
	if !strings.Contains(got, "keep-before.com") || !strings.Contains(got, "keep-after.com") {
		t.Errorf("replace clobbered surrounding config:\n%s", got)
	}
	if strings.Contains(got, "OLD.com") {
		t.Errorf("replace left stale managed content:\n%s", got)
	}
	if !strings.Contains(got, "example.com") {
		t.Errorf("replace did not insert new section:\n%s", got)
	}
	if strings.Count(got, caddyDomainsStart) != 1 || strings.Count(got, caddyDomainsEnd) != 1 {
		t.Errorf("marker count drifted:\n%s", got)
	}
}
