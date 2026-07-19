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

func TestPlatformHostAllowedBehindLocalReverseProxy(t *testing.T) {
	localPool := &SiteRuntimePool{platformHost: "localhost:8080", localPlatform: true}
	check := func(name, target, remote string, pool *SiteRuntimePool, want bool) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.RemoteAddr = remote
			if got := (&Server{}).platformHostAllowed(req, pool); got != want {
				t.Fatalf("platformHostAllowed(%q, %q) = %v, want %v", req.Host, remote, got, want)
			}
		})
	}

	check("configured local host", "http://localhost:8080/admin/login", "203.0.113.10:40000", localPool, true)
	check("public host through loopback proxy", "https://cms.example.test/admin/login", "127.0.0.1:40000", localPool, true)
	check("public host through ipv6 loopback proxy", "https://cms.example.test/admin/login", "[::1]:40000", localPool, true)
	check("public host through direct connection", "https://cms.example.test/admin/login", "203.0.113.10:40000", localPool, false)

	publicPool := &SiteRuntimePool{platformHost: "platform.example.test"}
	check("configured public host", "https://platform.example.test/admin/login", "203.0.113.10:40000", publicPool, true)
	check("wrong public host through loopback proxy", "https://other.example.test/admin/login", "127.0.0.1:40000", publicPool, false)
}

func TestFirstInstallAdminRoutesToLoginBehindLocalReverseProxy(t *testing.T) {
	_, handler, _, _, _ := newPlatformTestServerBase(t, "http://localhost:8080")

	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://cms.example.test"+path, nil)
		req.RemoteAddr = "127.0.0.1:40000"
		req.Header.Set("X-Forwarded-Proto", "https")
		handler.ServeHTTP(rec, req)
		return rec
	}

	admin := request("/admin")
	if admin.Code != http.StatusSeeOther || admin.Header().Get("Location") != "/admin/login" {
		t.Fatalf("anonymous /admin = %d %q, want 303 /admin/login", admin.Code, admin.Header().Get("Location"))
	}

	sites := request("/admin/sites")
	if sites.Code != http.StatusSeeOther || sites.Header().Get("Location") != "/admin/login" {
		t.Fatalf("anonymous /admin/sites = %d %q, want 303 /admin/login", sites.Code, sites.Header().Get("Location"))
	}

	login := request("/admin/login")
	if login.Code != http.StatusOK {
		t.Fatalf("GET /admin/login = %d, want 200; body=%s", login.Code, login.Body.String())
	}

	direct := httptest.NewRecorder()
	directReq := httptest.NewRequest(http.MethodGet, "https://cms.example.test/admin/login", nil)
	directReq.RemoteAddr = "203.0.113.10:40000"
	handler.ServeHTTP(direct, directReq)
	if direct.Code != http.StatusNotFound {
		t.Fatalf("direct request with unknown host = %d, want 404", direct.Code)
	}
}
