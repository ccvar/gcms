package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCaddyOnDemandEnabled(t *testing.T) {
	cases := map[string]bool{"": false, "0": false, "no": false, "1": true, "on": true, "TRUE": true, "yes": true}
	for v, want := range cases {
		t.Setenv("GCMS_CADDY_ONDEMAND", v)
		if got := caddyOnDemandEnabled(); got != want {
			t.Errorf("caddyOnDemandEnabled(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestPoolRedirectTarget(t *testing.T) {
	p := &SiteRuntimePool{redirects: map[string]string{"alias.example.com": "https://main.example.com"}}
	if got := p.redirectTarget("alias.example.com"); got != "https://main.example.com" {
		t.Fatalf("alias redirectTarget = %q", got)
	}
	if got := p.redirectTarget("ALIAS.example.com"); got != "https://main.example.com" {
		t.Fatalf("case-insensitive redirectTarget = %q", got)
	}
	if got := p.redirectTarget("main.example.com"); got != "" {
		t.Fatalf("primary host should not redirect, got %q", got)
	}
	var empty *SiteRuntimePool
	if got := empty.redirectTarget("x"); got != "" {
		t.Fatalf("nil pool redirectTarget = %q", got)
	}
}

func TestCaddyAsk(t *testing.T) {
	s := &Server{baseURL: "https://platform.example.com"}
	s.runtimes = &SiteRuntimePool{
		byHost:       map[string]*SiteRuntime{"alias.example.com": {}, "main.example.com": {}},
		platformHost: "platform.example.com",
	}
	check := func(query string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/internal/caddy/ask?domain="+query, nil)
		rec := httptest.NewRecorder()
		s.caddyAsk(rec, req)
		if rec.Code != want {
			t.Errorf("ask(domain=%q) = %d, want %d", query, rec.Code, want)
		}
	}
	check("main.example.com", http.StatusOK)        // bound primary
	check("alias.example.com", http.StatusOK)       // bound alias
	check("platform.example.com", http.StatusOK)    // platform host
	check("PLATFORM.example.com", http.StatusOK)    // case-insensitive
	check("evil.example.com", http.StatusForbidden) // unknown
	check("", http.StatusBadRequest)                // missing
}
