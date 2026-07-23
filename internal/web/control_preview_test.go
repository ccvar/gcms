package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
)

func TestPlatformControlSitePreviewURL(t *testing.T) {
	srv, h, ps, defaultSite, blogSite := setupPlatformAutomation(t)
	token := "gcmsp_sitepreview12345"
	if _, err := ps.CreatePlatformKey(
		"pilot-preview",
		token,
		token[:13],
		platform.KeyMembershipAll,
		apiScopeControlRead,
		nil,
		time.Time{},
	); err != nil {
		t.Fatalf("create platform key: %v", err)
	}

	endpoint := "/api/platform/v1/control/sites/" + strconv.FormatInt(blogSite.ID, 10) + "/preview-url"
	rec := platformAPIReq(t, h, http.MethodPost, endpoint, token, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create preview = %d %s", rec.Code, rec.Body.String())
	}
	var got apiPreviewURLResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode preview response: %v", err)
	}
	if got.TTLSeconds != int64(sitePreviewTTL.Seconds()) {
		t.Fatalf("ttl_seconds = %d, want %d", got.TTLSeconds, int64(sitePreviewTTL.Seconds()))
	}
	expiresAt, err := time.Parse(time.RFC3339, got.ExpiresAt)
	if err != nil || time.Until(expiresAt) < 14*time.Minute || time.Until(expiresAt) > 16*time.Minute {
		t.Fatalf("expires_at = %q (%v)", got.ExpiresAt, err)
	}
	previewURL, err := url.Parse(got.PreviewURL)
	if err != nil {
		t.Fatalf("parse preview URL: %v", err)
	}
	wantPrefix := "/preview/sites/" + strconv.FormatInt(blogSite.ID, 10) + "/site/"
	if previewURL.Scheme != "https" || previewURL.Host != "platform.test" || !strings.HasPrefix(previewURL.Path, wantPrefix) {
		t.Fatalf("preview_url = %q, want platform URL under %q", got.PreviewURL, wantPrefix)
	}

	// 无后台 Cookie、无 GCMS 密码也能打开；页面链接始终留在同一票据路径中。
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, got.PreviewURL, nil))
	if page.Code != http.StatusOK {
		t.Fatalf("open preview = %d %s", page.Code, page.Body.String())
	}
	if page.Header().Get("X-Robots-Tag") != "noindex, nofollow" ||
		page.Header().Get("Cache-Control") != "no-store" ||
		page.Header().Get("Referrer-Policy") != "strict-origin-when-cross-origin" {
		t.Fatalf("preview security headers = %v", page.Header())
	}
	body := page.Body.String()
	if !strings.Contains(body, "Blog Site") || !strings.Contains(body, wantPrefix) {
		t.Fatalf("preview did not render target site inside signed prefix: %s", body)
	}

	// 票据与站点绑定，不能换一个 site ID 复用。
	wrongSitePath := strings.Replace(previewURL.Path, wantPrefix, "/preview/sites/"+strconv.FormatInt(defaultSite.ID, 10)+"/site/", 1)
	wrongSite := httptest.NewRecorder()
	h.ServeHTTP(wrongSite, httptest.NewRequest(http.MethodGet, "https://platform.test"+wrongSitePath, nil))
	if wrongSite.Code != http.StatusNotFound {
		t.Fatalf("cross-site token = %d, want 404", wrongSite.Code)
	}

	// 站点关闭自动化后，已签发的短时票据也立即失效。
	if err := ps.SetSiteAutomation(blogSite.ID, false); err != nil {
		t.Fatalf("disable automation: %v", err)
	}
	revoked := httptest.NewRecorder()
	h.ServeHTTP(revoked, httptest.NewRequest(http.MethodGet, got.PreviewURL, nil))
	if revoked.Code != http.StatusNotFound {
		t.Fatalf("revoked preview = %d, want 404", revoked.Code)
	}
	denied := platformAPIReq(t, h, http.MethodPost, endpoint, token, nil)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("preview for disabled automation = %d %s, want 403", denied.Code, denied.Body.String())
	}

	// 单独钉住过期语义，便于 Pilot 区分“重新生成”与普通 404。
	if err := ps.SetSiteAutomation(blogSite.ID, true); err != nil {
		t.Fatalf("restore automation: %v", err)
	}
	rt, ok := srv.runtimePool().runtimeByID(blogSite.ID)
	if !ok || rt == nil || rt.server == nil {
		t.Fatal("blog runtime missing")
	}
	expiredToken, err := rt.server.signSitePreviewToken(blogSite.ID, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("sign expired preview: %v", err)
	}
	expired := httptest.NewRecorder()
	expiredPath := "https://platform.test/preview/sites/" + strconv.FormatInt(blogSite.ID, 10) + "/site/" + expiredToken + "/"
	h.ServeHTTP(expired, httptest.NewRequest(http.MethodGet, expiredPath, nil))
	if expired.Code != http.StatusGone || !strings.Contains(expired.Body.String(), "已过期") {
		t.Fatalf("expired preview = %d %s, want 410", expired.Code, expired.Body.String())
	}
}

func TestPlatformControlSitePreviewRequiresMembershipAndPOST(t *testing.T) {
	_, h, ps, defaultSite, blogSite := setupPlatformAutomation(t)
	token := "gcmsp_previewallowlist"
	if _, err := ps.CreatePlatformKey(
		"limited-preview",
		token,
		token[:13],
		platform.KeyMembershipAllowlist,
		apiScopeControlRead,
		[]int64{defaultSite.ID},
		time.Time{},
	); err != nil {
		t.Fatalf("create allowlist key: %v", err)
	}
	endpoint := "/api/platform/v1/control/sites/" + strconv.FormatInt(blogSite.ID, 10) + "/preview-url"
	denied := platformAPIReq(t, h, http.MethodPost, endpoint, token, nil)
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "membership_scope") {
		t.Fatalf("allowlist preview = %d %s, want 403 membership_scope", denied.Code, denied.Body.String())
	}
	method := platformAPIReq(t, h, http.MethodGet, endpoint, token, nil)
	if method.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET preview-url = %d %s, want 405", method.Code, method.Body.String())
	}
}

func TestPlatformControlCandidateThemePreviewIsSignedReadOnlyAndScoped(t *testing.T) {
	srv, h, ps, defaultSite, blogSite := setupPlatformAutomation(t)
	themeToken := "gcmsp_themepreview1234"
	if _, err := ps.CreatePlatformKey(
		"pilot-theme-preview",
		themeToken,
		themeToken[:13],
		platform.KeyMembershipAll,
		apiScopeThemesRead,
		nil,
		time.Time{},
	); err != nil {
		t.Fatalf("create theme preview key: %v", err)
	}
	controlToken := "gcmsp_currentpreview123"
	if _, err := ps.CreatePlatformKey(
		"pilot-current-preview",
		controlToken,
		controlToken[:13],
		platform.KeyMembershipAll,
		apiScopeControlRead,
		nil,
		time.Time{},
	); err != nil {
		t.Fatalf("create current preview key: %v", err)
	}
	runtime, ok := srv.runtimePool().runtimeByID(blogSite.ID)
	if !ok || runtime == nil || runtime.server == nil || runtime.Store == nil {
		t.Fatal("blog runtime missing")
	}
	if err := runtime.Store.SetSetting("theme", "editorial"); err != nil {
		t.Fatalf("seed current theme: %v", err)
	}

	endpoint := "/api/platform/v1/control/sites/" + strconv.FormatInt(blogSite.ID, 10) + "/preview-url"
	body := []byte(`{"theme_id":"magazine"}`)
	rec := platformAPIReq(t, h, http.MethodPost, endpoint, themeToken, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create candidate theme preview = %d %s", rec.Code, rec.Body.String())
	}
	var got apiPreviewURLResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode candidate preview: %v", err)
	}
	if got.ThemeID != "magazine" || got.CurrentTheme != "editorial" {
		t.Fatalf("candidate preview response = %#v", got)
	}
	if current := controlCurrentTheme(runtime.Store); current != "editorial" {
		t.Fatalf("signing candidate preview changed current theme to %q", current)
	}
	previewURL, err := url.Parse(got.PreviewURL)
	if err != nil {
		t.Fatalf("parse candidate preview URL: %v", err)
	}
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, got.PreviewURL, nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `data-theme="magazine"`) {
		t.Fatalf("candidate preview render = %d %s", page.Code, page.Body.String())
	}
	if current := controlCurrentTheme(runtime.Store); current != "editorial" {
		t.Fatalf("rendering candidate preview changed current theme to %q", current)
	}

	// A candidate request requires themes:read, while the legacy empty-body
	// current-site preview keeps its existing control:read requirement.
	missingThemeScope := platformAPIReq(t, h, http.MethodPost, endpoint, controlToken, body)
	if missingThemeScope.Code != http.StatusForbidden || !strings.Contains(missingThemeScope.Body.String(), apiScopeThemesRead) {
		t.Fatalf("candidate preview with control:read only = %d %s", missingThemeScope.Code, missingThemeScope.Body.String())
	}
	missingControlScope := platformAPIReq(t, h, http.MethodPost, endpoint, themeToken, nil)
	if missingControlScope.Code != http.StatusForbidden || !strings.Contains(missingControlScope.Body.String(), apiScopeControlRead) {
		t.Fatalf("current preview with themes:read only = %d %s", missingControlScope.Code, missingControlScope.Body.String())
	}
	invalid := platformAPIReq(t, h, http.MethodPost, endpoint, themeToken, []byte(`{"theme_id":"not-a-theme"}`))
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), `"error":"invalid_theme"`) {
		t.Fatalf("invalid candidate preview = %d %s", invalid.Code, invalid.Body.String())
	}

	sitePrefix := "/preview/sites/" + strconv.FormatInt(blogSite.ID, 10)
	rest := strings.TrimPrefix(previewURL.Path, sitePrefix)
	signedToken, _, ok := signedWholeSitePreviewTarget(rest)
	if !ok {
		t.Fatalf("candidate preview path did not contain signed site token: %q", previewURL.Path)
	}

	// The same signed claims cannot be moved to another site.
	wrongSitePath := strings.Replace(previewURL.Path, sitePrefix, "/preview/sites/"+strconv.FormatInt(defaultSite.ID, 10), 1)
	wrongSite := httptest.NewRecorder()
	h.ServeHTTP(wrongSite, httptest.NewRequest(http.MethodGet, "https://platform.test"+wrongSitePath, nil))
	if wrongSite.Code != http.StatusNotFound {
		t.Fatalf("candidate cross-site token = %d, want 404", wrongSite.Code)
	}

	// Theme id is inside the signed payload. Rewriting it while retaining the
	// original signature must invalidate the URL instead of changing the preview.
	tokenParts := strings.Split(signedToken, ".")
	if len(tokenParts) != 2 {
		t.Fatalf("candidate token parts = %d", len(tokenParts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(tokenParts[0])
	if err != nil {
		t.Fatalf("decode candidate claims: %v", err)
	}
	var claims sitePreviewClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal candidate claims: %v", err)
	}
	claims.ThemeID = "terminal"
	payload, err = json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal tampered claims: %v", err)
	}
	tamperedToken := base64.RawURLEncoding.EncodeToString(payload) + "." + tokenParts[1]
	tamperedPath := strings.Replace(previewURL.Path, signedToken, tamperedToken, 1)
	tampered := httptest.NewRecorder()
	h.ServeHTTP(tampered, httptest.NewRequest(http.MethodGet, "https://platform.test"+tamperedPath, nil))
	if tampered.Code != http.StatusNotFound {
		t.Fatalf("tampered candidate theme = %d, want 404", tampered.Code)
	}

	expiredToken, err := runtime.server.signSiteThemePreviewToken(blogSite.ID, "magazine", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("sign expired candidate preview: %v", err)
	}
	expiredPath := "https://platform.test/preview/sites/" + strconv.FormatInt(blogSite.ID, 10) + "/site/" + expiredToken + "/"
	expired := httptest.NewRecorder()
	h.ServeHTTP(expired, httptest.NewRequest(http.MethodGet, expiredPath, nil))
	if expired.Code != http.StatusGone {
		t.Fatalf("expired candidate preview = %d %s, want 410", expired.Code, expired.Body.String())
	}
}
