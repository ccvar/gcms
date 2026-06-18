package web

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestNormalizeCloudflareWorkerName(t *testing.T) {
	tests := map[string]string{
		"":                  cloudflareDefaultWorkerName,
		"GCMS Frontend":     "gcms-frontend",
		"gcms_frontend@dev": "gcms-frontend-dev",
	}
	for input, want := range tests {
		if got := normalizeCloudflareWorkerName(input); got != want {
			t.Fatalf("normalizeCloudflareWorkerName(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestNormalizeCloudflareOrigin(t *testing.T) {
	got := normalizeCloudflareOrigin("https://origin.example.com/base/?x=1#top")
	if got != "https://origin.example.com/base" {
		t.Fatalf("origin normalized to %q", got)
	}
}

func TestCloudflareRequestDefaultsUsePublicRequestHost(t *testing.T) {
	s := &Server{baseURL: "http://localhost:8080"}
	r := httptest.NewRequest("GET", "http://127.0.0.1/admin/settings/cloudflare", nil)
	r.Host = "cms.example.com"
	r.Header.Set("X-Forwarded-Proto", "https")
	var cfg CloudflareConfig
	s.applyCloudflareRequestDefaults(r, &cfg)
	if cfg.OriginURL != "https://cms.example.com" {
		t.Fatalf("OriginURL = %q, want https://cms.example.com", cfg.OriginURL)
	}
	if cfg.RoutePattern != "cms.example.com/*" {
		t.Fatalf("RoutePattern = %q, want cms.example.com/*", cfg.RoutePattern)
	}
}

func TestCloudflareWorkerScriptProtectsAdminAndForwardsHost(t *testing.T) {
	script := cloudflareWorkerScript()
	for _, needle := range []string{
		`"/admin"`,
		`"/api/admin"`,
		`"/preview"`,
		`X-Forwarded-Host`,
		`X-Forwarded-Proto`,
		`caches.default`,
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("worker script should contain %s", needle)
		}
	}
}

func TestCloudflareOAuthAuthorizationURL(t *testing.T) {
	got, err := cloudflareOAuthAuthorizationURL("client_123", "https://cms.example.com/admin/settings/cloudflare/callback", "state_123")
	if err != nil {
		t.Fatalf("cloudflareOAuthAuthorizationURL returned %v", err)
	}
	for _, needle := range []string{
		"https://dash.cloudflare.com/oauth2/auth?",
		"response_type=code",
		"client_id=client_123",
		"redirect_uri=https%3A%2F%2Fcms.example.com%2Fadmin%2Fsettings%2Fcloudflare%2Fcallback",
		"state=state_123",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("authorization URL %q should contain %s", got, needle)
		}
	}
}

func TestCloudflareAPITokenTemplateURL(t *testing.T) {
	raw := cloudflareAPITokenTemplateURL()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("token template URL should parse: %v", err)
	}
	if u.Scheme != "https" || u.Host != "dash.cloudflare.com" || u.Path != "/profile/api-tokens" {
		t.Fatalf("unexpected token template URL: %s", raw)
	}
	q := u.Query()
	if q.Get("accountId") != "*" || q.Get("zoneId") != "all" {
		t.Fatalf("unexpected token scope: %s", raw)
	}
	for _, needle := range []string{
		`"key":"workers_scripts"`,
		`"key":"workers_routes"`,
		`"key":"cache"`,
		`"key":"zone"`,
		`"key":"account_settings"`,
	} {
		if !strings.Contains(q.Get("permissionGroupKeys"), needle) {
			t.Fatalf("permissionGroupKeys should contain %s: %s", needle, q.Get("permissionGroupKeys"))
		}
	}
}

func TestCloudflareConfigConfiguredWithAPITokenOnly(t *testing.T) {
	cfg := CloudflareConfig{
		APIToken:     "token",
		WorkerName:   "gcms-frontend",
		OriginURL:    "https://origin.example.com",
		RoutePattern: "example.com/*",
	}
	if !cfg.configured() {
		t.Fatal("API token plus worker/origin should be enough before auto detection")
	}
	if err := cfg.validateDeploy(); err != nil {
		t.Fatalf("validateDeploy returned %v", err)
	}
}

func TestCloudflareConfigConfiguredWithOAuth(t *testing.T) {
	cfg := CloudflareConfig{
		OAuthClientID:     "client_123",
		OAuthClientSecret: "secret",
		OAuthRefreshToken: "refresh",
		AccountID:         "account_123",
		ZoneID:            "zone_123",
		WorkerName:        "gcms-frontend",
		OriginURL:         "https://origin.example.com",
		RoutePattern:      "example.com/*",
	}
	if !cfg.configured() {
		t.Fatal("local OAuth credentials should be enough to configure Cloudflare deploy")
	}
	if err := cfg.validateDeploy(); err != nil {
		t.Fatalf("validateDeploy returned %v", err)
	}
}

func TestCloudflareRouteHostAndZoneMatch(t *testing.T) {
	if got := cloudflareRouteHost("www.example.com/*"); got != "www.example.com" {
		t.Fatalf("cloudflareRouteHost returned %q", got)
	}
	zones := []cloudflareZone{
		{ID: "zone_a", Name: "example.net"},
		{ID: "zone_b", Name: "example.com", Account: cloudflareAccount{ID: "acct", Name: "Main"}},
	}
	got := matchCloudflareZone("www.example.com", zones)
	if got.ID != "zone_b" || got.Account.ID != "acct" {
		t.Fatalf("unexpected matched zone: %+v", got)
	}
}
