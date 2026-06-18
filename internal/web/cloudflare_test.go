package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/store"
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

func TestNormalizeCloudflareRoutePattern(t *testing.T) {
	tests := map[string]string{
		"ccvar.com":                   "ccvar.com/*",
		"www.ccvar.com/*":             "www.ccvar.com/*",
		"https://ccvar.com":           "ccvar.com/*",
		"https://ccvar.com/docs?x=1":  "ccvar.com/docs/*",
		"static.ccvar.com/assets/*":   "static.ccvar.com/assets/*",
		"  www.ccvar.com  extra text": "www.ccvar.com/*",
	}
	for input, want := range tests {
		if got := normalizeCloudflareRoutePattern(input); got != want {
			t.Fatalf("normalizeCloudflareRoutePattern(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestCloudflareStatusStale(t *testing.T) {
	st := &CloudflareStatus{
		Status:    "running",
		UpdatedAt: time.Now().Add(-cloudflareStaleAfter - time.Minute).UTC().Format(time.RFC3339),
	}
	if !cloudflareStatusStale(st) {
		t.Fatal("old running status should be stale")
	}
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if cloudflareStatusStale(st) {
		t.Fatal("fresh running status should not be stale")
	}
}

func TestCloudflareRequestDefaultsOnlyInferOrigin(t *testing.T) {
	s := &Server{baseURL: "http://localhost:8080"}
	r := httptest.NewRequest("GET", "http://127.0.0.1/admin/settings/cloudflare", nil)
	r.Host = "cms.example.com"
	r.Header.Set("X-Forwarded-Proto", "https")
	var cfg CloudflareConfig
	s.applyCloudflareRequestDefaults(r, &cfg)
	if cfg.OriginURL != "https://cms.example.com" {
		t.Fatalf("OriginURL = %q, want https://cms.example.com", cfg.OriginURL)
	}
	if cfg.RoutePattern != "" {
		t.Fatalf("RoutePattern = %q, want empty; public entry domain must be user supplied", cfg.RoutePattern)
	}
}

func TestCloudflareWorkerScriptProtectsAdminAndServesAssets(t *testing.T) {
	script := cloudflareWorkerScript()
	for _, needle := range []string{
		`"/admin"`,
		`"/api/admin"`,
		`"/preview"`,
		`env.ASSETS.fetch`,
		`/cat/`,
		`/page/`,
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("worker script should contain %s", needle)
		}
	}
}

func TestCloudflareAPIErrorCodeDetection(t *testing.T) {
	err := cloudflareAPIError{
		Status: 403,
		Errors: []cloudflareErr{
			{Code: 10000, Message: "Authentication error"},
		},
	}
	if !cloudflareHasErrorCode(err, 10000) {
		t.Fatal("Cloudflare error code 10000 should be detectable")
	}
	if cloudflareHasErrorCode(err, 7003) {
		t.Fatal("unexpected Cloudflare error code match")
	}
	if !strings.Contains(err.Error(), "10000 Authentication error") {
		t.Fatalf("error message should keep original code: %q", err.Error())
	}
}

func TestCloudflareStagePermissionErrorForWorkerUpload(t *testing.T) {
	baseErr := cloudflareAPIError{
		Status: 403,
		Errors: []cloudflareErr{
			{Code: 10000, Message: "Authentication error"},
		},
	}
	err := cloudflareStagePermissionError("worker", baseErr)
	msg := err.Error()
	for _, needle := range []string{"Workers Scripts Edit", "Account Resources", "原始错误", "10000 Authentication error"} {
		if !strings.Contains(msg, needle) {
			t.Fatalf("worker upload permission error %q should contain %q", msg, needle)
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
		`"key":"dns"`,
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
		RoutePattern: "example.com/*",
	}
	if !cfg.configured() {
		t.Fatal("API token plus worker/route should be enough before auto detection")
	}
	if err := cfg.validateDeploy(); err != nil {
		t.Fatalf("validateDeploy returned %v", err)
	}
}

func TestRecommendedCloudflareTokenFormClearsDetectedIDs(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for key, value := range map[string]string{
		cloudflareAccountIDKey:   "old_account",
		cloudflareAccountNameKey: "Old Account",
		cloudflareZoneIDKey:      "old_zone",
		cloudflareZoneNameKey:    "Old Zone",
		cloudflareAPITokenKey:    "old_token",
	} {
		if err := st.SetSetting(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	s := &Server{store: st, baseURL: "https://origin.example.com"}
	form := url.Values{
		"deploy":         {"1"},
		"worker_name":    {"gcms-frontend"},
		"origin_url":     {"https://origin.example.com"},
		"route_pattern":  {"test.ccvar.com/*"},
		"html_cache_ttl": {"300"},
		"auto_sync":      {"1"},
		"api_token":      {"new_token"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/settings/cloudflare", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cfg, err := s.saveCloudflareConfigFromRequest(req)
	if err != nil {
		t.Fatalf("saveCloudflareConfigFromRequest returned %v", err)
	}
	if cfg.AccountID != "" || cfg.ZoneID != "" || cfg.AccountName != "" || cfg.ZoneName != "" {
		t.Fatalf("recommended form should clear stale detected IDs, got %+v", cfg)
	}
	for _, key := range []string{cloudflareAccountIDKey, cloudflareAccountNameKey, cloudflareZoneIDKey, cloudflareZoneNameKey} {
		if got := st.Setting(key); got != "" {
			t.Fatalf("%s should be cleared, got %q", key, got)
		}
	}
	if got := st.Setting(cloudflareAPITokenKey); got != "new_token" {
		t.Fatalf("api token = %q, want new_token", got)
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

func TestCloudflareZoneNameCandidates(t *testing.T) {
	got := cloudflareZoneNameCandidates("test.ccvar.com")
	want := []string{"test.ccvar.com", "ccvar.com"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("cloudflareZoneNameCandidates returned %#v, want %#v", got, want)
	}
	if likely := cloudflareLikelyZoneName("test.ccvar.com"); likely != "ccvar.com" {
		t.Fatalf("cloudflareLikelyZoneName returned %q, want ccvar.com", likely)
	}
}

func TestCloudflareZoneDetectErrorIncludesLikelyZone(t *testing.T) {
	err := cloudflareZoneDetectError("test.ccvar.com/*", nil)
	msg := err.Error()
	for _, needle := range []string{"test.ccvar.com", "ccvar.com", "Zone Read"} {
		if !strings.Contains(msg, needle) {
			t.Fatalf("error %q should contain %q", msg, needle)
		}
	}
}
