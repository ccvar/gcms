package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func TestGoogleAnalyticsAdminAPIErrorMessageExplainsDisabledAPI(t *testing.T) {
	raw := "Google Analytics Admin API has not been used in project 107955144495 before or it is disabled. Enable it by visiting https://console.developers.google.com/apis/api/analyticsadmin.googleapis.com/overview?project=107955144495 then retry."
	got := googleAnalyticsAdminAPIErrorMessage(raw)
	if !strings.Contains(got, "Google Analytics Admin API 尚未启用") ||
		!strings.Contains(got, "当前 OAuth Client 所属的 Google Cloud 项目") ||
		!strings.Contains(got, "project=107955144495") {
		t.Fatalf("unexpected friendly error: %q", got)
	}
}

func TestGoogleSearchConsoleAPIErrorMessageExplainsDisabledAPI(t *testing.T) {
	raw := "Webmasters API has not been used in project 107955144495 before or it is disabled. Enable it by visiting https://console.developers.google.com/apis/api/webmasters.googleapis.com/overview?project=107955144495 then retry."
	got := googleSearchConsoleAPIErrorMessage(raw)
	if !strings.Contains(got, "Google Search Console API 尚未启用") ||
		!strings.Contains(got, "当前 OAuth Client 所属的 Google Cloud 项目") ||
		!strings.Contains(got, "project=107955144495") {
		t.Fatalf("unexpected friendly error: %q", got)
	}
}

func TestGoogleSearchConsoleMatchingPrefersVerifiedProperties(t *testing.T) {
	sites := []googleSearchConsoleSiteOption{
		{URL: "https://example.com/", PermissionLevel: "siteUnverifiedUser"},
		{URL: "sc-domain:example.com", PermissionLevel: "siteOwner"},
	}
	got, ok := findGoogleSearchConsoleMatchingSite(sites, "https://www.example.com/")
	if !ok {
		t.Fatal("expected domain property to match www URL")
	}
	if got.URL != "sc-domain:example.com" {
		t.Fatalf("matched URL = %q", got.URL)
	}
	if _, ok := findGoogleSearchConsoleMatchingSite(sites, "https://example.com/"); !ok {
		t.Fatal("expected verified domain property to match root URL")
	}
	if got, ok := findGoogleSearchConsoleAnySite(sites, "https://example.com/"); !ok || got.URL != "https://example.com/" {
		t.Fatalf("expected any-site lookup to include unverified exact URL, got %#v ok=%v", got, ok)
	}
}

func TestGoogleOAuthConfigCanBeSavedFromPlatformSites(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cms.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SetSetting("site.name", "OAuth Test Site"); err != nil {
		t.Fatalf("set site name: %v", err)
	}
	uploadDir := filepath.Join(dir, "uploads")
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		t.Fatalf("create uploads: %v", err)
	}
	ps, err := platform.Open(filepath.Join(dir, "system.db"))
	if err != nil {
		t.Fatalf("open platform: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	hash, err := bcrypt.GenerateFromPassword([]byte(store.DefaultAdminPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := ps.BootstrapDefaultSite(platform.DefaultSiteBootstrap{
		Slug:                        "main",
		Name:                        "OAuth Test Site",
		DBPath:                      dbPath,
		UploadDir:                   uploadDir,
		AdminUser:                   "admin",
		AdminPasswordHash:           string(hash),
		ManagementAutomationEnabled: true,
	}); err != nil {
		t.Fatalf("bootstrap default site: %v", err)
	}
	srv, err := NewWithPlatform(st, ps, "https://platform.test", uploadDir, os.DirFS("../.."), os.DirFS("../.."))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	h := srv.Handler()

	login := httptest.NewRecorder()
	loginForm := url.Values{"username": {"admin"}, "password": {store.DefaultAdminPassword}}
	loginReq := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/login", strings.NewReader(loginForm.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(login, loginReq)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d", login.Code)
	}
	var loginCookie *http.Cookie
	for _, c := range login.Result().Cookies() {
		if c.Name == cookieName {
			loginCookie = c
			break
		}
	}
	if loginCookie == nil {
		t.Fatal("login did not set session cookie")
	}
	loginSess, ok, err := ps.GetAdminSession(loginCookie.Value)
	if err != nil || !ok {
		t.Fatalf("get login session: ok=%v err=%v", ok, err)
	}

	before := httptest.NewRecorder()
	beforeReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites", nil)
	beforeReq.AddCookie(loginCookie)
	h.ServeHTTP(before, beforeReq)
	if before.Code != http.StatusOK {
		t.Fatalf("before sites status = %d", before.Code)
	}
	beforeBody := before.Body.String()
	if !strings.Contains(beforeBody, `data-google-oauth-root`) || !strings.Contains(beforeBody, `class="google-oauth-form"`) || !strings.Contains(beforeBody, "Client ID 和 Client Secret 从哪里获取") || !strings.Contains(beforeBody, "先配置 OAuth") {
		t.Fatalf("sites page did not render OAuth config form")
	}
	if strings.Contains(beforeBody, `href="/admin/google/oauth/start?service=all"`) {
		t.Fatalf("sites page rendered Google authorize links before OAuth config was saved")
	}

	form := url.Values{}
	form.Set("_csrf", loginSess.CSRF)
	form.Set("client_id", "test-client.apps.googleusercontent.com")
	form.Set("client_secret", "test-secret")
	form.Set("redirect_url", "https://platform.test/admin/google/oauth/callback")
	save := httptest.NewRecorder()
	saveReq := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/google/oauth/config", strings.NewReader(form.Encode()))
	saveReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	saveReq.AddCookie(loginCookie)
	h.ServeHTTP(save, saveReq)
	if save.Code != http.StatusSeeOther || save.Header().Get("Location") != "/admin/sites" {
		t.Fatalf("save status/location = %d %q", save.Code, save.Header().Get("Location"))
	}
	if got := ps.Setting(googleOAuthClientIDKey); got != "test-client.apps.googleusercontent.com" {
		t.Fatalf("client id = %q", got)
	}
	if got := ps.Setting(googleOAuthClientSecretKey); got != "test-secret" {
		t.Fatalf("client secret = %q", got)
	}
	if got := ps.Setting(googleOAuthRedirectURLKey); got != "https://platform.test/admin/google/oauth/callback" {
		t.Fatalf("redirect url = %q", got)
	}

	jsonForm := url.Values{}
	jsonForm.Set("_csrf", loginSess.CSRF)
	jsonForm.Set("client_id", "test-client-2.apps.googleusercontent.com")
	jsonForm.Set("redirect_url", "https://platform.test/admin/google/oauth/callback")
	jsonSave := httptest.NewRecorder()
	jsonSaveReq := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/google/oauth/config", strings.NewReader(jsonForm.Encode()))
	jsonSaveReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	jsonSaveReq.Header.Set("Accept", "application/json")
	jsonSaveReq.AddCookie(loginCookie)
	h.ServeHTTP(jsonSave, jsonSaveReq)
	if jsonSave.Code != http.StatusOK || !strings.Contains(jsonSave.Body.String(), `"ok":true`) {
		t.Fatalf("json save status/body = %d %q", jsonSave.Code, jsonSave.Body.String())
	}
	if got := ps.Setting(googleOAuthClientIDKey); got != "test-client-2.apps.googleusercontent.com" {
		t.Fatalf("json client id = %q", got)
	}
	if got := ps.Setting(googleOAuthClientSecretKey); got != "test-secret" {
		t.Fatalf("json save should preserve client secret, got %q", got)
	}

	after := httptest.NewRecorder()
	afterReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites", nil)
	afterReq.AddCookie(loginCookie)
	h.ServeHTTP(after, afterReq)
	if after.Code != http.StatusOK {
		t.Fatalf("after sites status = %d", after.Code)
	}
	body := after.Body.String()
	if !strings.Contains(body, "OAuth 已配置") || !strings.Contains(body, "test-client-2.apps.googleusercontent.com") || !strings.Contains(body, "/admin/google/oauth/start?service=all") {
		t.Fatalf("sites page did not render saved OAuth state")
	}

	if err := ps.UpsertGoogleAccount(&platform.GoogleAccount{
		Service:         platform.GoogleServiceAnalytics,
		GoogleAccountID: "google-account-1",
		Email:           "ga@example.com",
		Name:            "GA Account",
		Scopes:          strings.Join([]string{"openid", "email", googleAnalyticsReadonlyScope, googleAnalyticsEditScope}, " "),
		AccessToken:     "access-token",
		TokenExpiry:     time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("upsert google account: %v", err)
	}
	if err := ps.UpsertGoogleAccount(&platform.GoogleAccount{
		Service:         platform.GoogleServiceSearchConsole,
		GoogleAccountID: "search-account-1",
		Email:           "gsc@example.com",
		Name:            "Search Account",
		Scopes:          strings.Join([]string{"openid", "email", googleSearchConsoleReadonlyScope}, " "),
		AccessToken:     "search-access-token",
		TokenExpiry:     time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("upsert search console account: %v", err)
	}
	oldGoogleHTTPClient := googleHTTPClient
	googleHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/v1beta/accountSummaries":
			return jsonTestResponse(req, http.StatusOK, `{"accountSummaries":[{"name":"accounts/1","displayName":"Demo Account","propertySummaries":[{"property":"properties/123","displayName":"Demo Property"}]}]}`), nil
		case req.Method == http.MethodGet && req.URL.Path == "/v1beta/properties/123/dataStreams":
			return jsonTestResponse(req, http.StatusOK, `{"dataStreams":[]}`), nil
		case req.Method == http.MethodPost && req.URL.Path == "/v1beta/properties/123/dataStreams":
			return jsonTestResponse(req, http.StatusOK, `{"name":"properties/123/dataStreams/456","webStreamData":{"measurementId":"G-AUTO123"}}`), nil
		case req.Method == http.MethodPost && req.URL.Path == "/v1beta/properties":
			return jsonTestResponse(req, http.StatusOK, `{"name":"properties/999","parent":"accounts/1","displayName":"gcms - Test Site"}`), nil
		case req.Method == http.MethodGet && req.URL.Path == "/v1beta/properties/999/dataStreams":
			return jsonTestResponse(req, http.StatusOK, `{"dataStreams":[]}`), nil
		case req.Method == http.MethodPost && req.URL.Path == "/v1beta/properties/999/dataStreams":
			return jsonTestResponse(req, http.StatusOK, `{"name":"properties/999/dataStreams/777","webStreamData":{"measurementId":"G-CREATE999"}}`), nil
		case req.Method == http.MethodGet && req.URL.Path == "/webmasters/v3/sites":
			return jsonTestResponse(req, http.StatusOK, `{"siteEntry":[{"siteUrl":"https://platform.test/","permissionLevel":"siteOwner"}]}`), nil
		default:
			t.Fatalf("unexpected google api request: %s %s", req.Method, req.URL.String())
			return jsonTestResponse(req, http.StatusNotFound, `{}`), nil
		}
	})}
	t.Cleanup(func() { googleHTTPClient = oldGoogleHTTPClient })
	withAccount := httptest.NewRecorder()
	withAccountReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites", nil)
	withAccountReq.AddCookie(loginCookie)
	h.ServeHTTP(withAccount, withAccountReq)
	if withAccount.Code != http.StatusOK {
		t.Fatalf("sites with account status = %d", withAccount.Code)
	}
	accountBody := withAccount.Body.String()
	if !strings.Contains(accountBody, "ga@example.com") || !strings.Contains(accountBody, "/admin/google/accounts/delete") || !strings.Contains(accountBody, "解除绑定") {
		t.Fatalf("sites page did not render google account unlink controls")
	}
	site, err := ps.DefaultSite()
	if err != nil {
		t.Fatalf("default site: %v", err)
	}
	props := httptest.NewRecorder()
	propsReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/google/analytics/properties?account=google-account-1", nil)
	propsReq.AddCookie(loginCookie)
	h.ServeHTTP(props, propsReq)
	if props.Code != http.StatusOK || !strings.Contains(props.Body.String(), "properties/123") || !strings.Contains(props.Body.String(), "accounts/1") {
		t.Fatalf("analytics properties status/body = %d %q", props.Code, props.Body.String())
	}
	gscSites := httptest.NewRecorder()
	gscSitesReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/google/search-console/sites?account=search-account-1", nil)
	gscSitesReq.AddCookie(loginCookie)
	h.ServeHTTP(gscSites, gscSitesReq)
	if gscSites.Code != http.StatusOK || !strings.Contains(gscSites.Body.String(), "https://platform.test/") {
		t.Fatalf("search console sites status/body = %d %q", gscSites.Code, gscSites.Body.String())
	}
	autoForm := url.Values{}
	autoForm.Set("_csrf", loginSess.CSRF)
	autoForm.Set("google_account_id", "google-account-1")
	autoForm.Set("property", "properties/123")
	autoForm.Set("default_uri", "https://platform.test")
	autoSave := httptest.NewRecorder()
	autoSaveReq := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/sites/"+strconv.FormatInt(site.ID, 10)+"/google/analytics/stream", strings.NewReader(autoForm.Encode()))
	autoSaveReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	autoSaveReq.AddCookie(loginCookie)
	h.ServeHTTP(autoSave, autoSaveReq)
	if autoSave.Code != http.StatusSeeOther || autoSave.Header().Get("Location") != "/admin/sites#site-card-"+strconv.FormatInt(site.ID, 10) {
		t.Fatalf("auto stream save status/location = %d %q", autoSave.Code, autoSave.Header().Get("Location"))
	}
	autoIntegration, ok, err := ps.SiteGoogleIntegration(site.ID, platform.GoogleServiceAnalytics)
	if err != nil || !ok {
		t.Fatalf("get auto google integration: ok=%v err=%v", ok, err)
	}
	if autoIntegration.MeasurementID != "G-AUTO123" || autoIntegration.Property != "properties/123" || autoIntegration.DataStream != "properties/123/dataStreams/456" || !autoIntegration.Enabled {
		t.Fatalf("auto site google integration mismatch: %#v", autoIntegration)
	}
	createForm := url.Values{}
	createForm.Set("_csrf", loginSess.CSRF)
	createForm.Set("google_account_id", "google-account-1")
	createForm.Set("property", "__create__")
	createForm.Set("property_mode", "create")
	createForm.Set("analytics_account", "accounts/1")
	createForm.Set("default_uri", "https://created.platform.test")
	createSave := httptest.NewRecorder()
	createSaveReq := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/sites/"+strconv.FormatInt(site.ID, 10)+"/google/analytics/stream", strings.NewReader(createForm.Encode()))
	createSaveReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createSaveReq.AddCookie(loginCookie)
	h.ServeHTTP(createSave, createSaveReq)
	if createSave.Code != http.StatusSeeOther || createSave.Header().Get("Location") != "/admin/sites#site-card-"+strconv.FormatInt(site.ID, 10) {
		t.Fatalf("create stream save status/location = %d %q", createSave.Code, createSave.Header().Get("Location"))
	}
	createdIntegration, ok, err := ps.SiteGoogleIntegration(site.ID, platform.GoogleServiceAnalytics)
	if err != nil || !ok {
		t.Fatalf("get created google integration: ok=%v err=%v", ok, err)
	}
	if createdIntegration.MeasurementID != "G-CREATE999" || createdIntegration.Property != "properties/999" || createdIntegration.DataStream != "properties/999/dataStreams/777" || !createdIntegration.Enabled {
		t.Fatalf("created site google integration mismatch: %#v", createdIntegration)
	}
	siteForm := url.Values{}
	siteForm.Set("_csrf", loginSess.CSRF)
	siteForm.Set("service", platform.GoogleServiceAnalytics)
	siteForm.Set("google_account_id", "google-account-1")
	siteForm.Set("measurement_id", "g-test123")
	siteForm.Set("enabled", "1")
	siteSave := httptest.NewRecorder()
	siteSaveReq := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/sites/"+strconv.FormatInt(site.ID, 10)+"/google", strings.NewReader(siteForm.Encode()))
	siteSaveReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	siteSaveReq.AddCookie(loginCookie)
	h.ServeHTTP(siteSave, siteSaveReq)
	if siteSave.Code != http.StatusSeeOther || siteSave.Header().Get("Location") != "/admin/sites#site-card-"+strconv.FormatInt(site.ID, 10) {
		t.Fatalf("site google save status/location = %d %q", siteSave.Code, siteSave.Header().Get("Location"))
	}
	siteIntegration, ok, err := ps.SiteGoogleIntegration(site.ID, platform.GoogleServiceAnalytics)
	if err != nil || !ok {
		t.Fatalf("get site google integration: ok=%v err=%v", ok, err)
	}
	if siteIntegration.MeasurementID != "G-TEST123" || siteIntegration.GoogleAccountID != "google-account-1" || !siteIntegration.Enabled {
		t.Fatalf("site google integration mismatch: %#v", siteIntegration)
	}
	configuredPage := httptest.NewRecorder()
	configuredPageReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites", nil)
	configuredPageReq.AddCookie(loginCookie)
	h.ServeHTTP(configuredPage, configuredPageReq)
	if configuredPage.Code != http.StatusOK {
		t.Fatalf("configured sites status = %d", configuredPage.Code)
	}
	configuredBody := configuredPage.Body.String()
	if !strings.Contains(configuredBody, "Google Analytics") || !strings.Contains(configuredBody, "G-TEST123") || !strings.Contains(configuredBody, "site-google-badge is-on") {
		t.Fatalf("sites page did not render saved site google integration")
	}
	publicView := srv.viewForLang(httptest.NewRequest(http.MethodGet, "https://platform.test/zh/", nil), "zh", "home")
	if !strings.Contains(publicView.Site.InjectHead, "https://www.googletagmanager.com/gtag/js?id=G-TEST123") || !strings.Contains(publicView.Site.InjectHead, "gtag('config', 'G-TEST123')") {
		t.Fatalf("public view did not inject analytics snippet: %q", publicView.Site.InjectHead)
	}
	adminView := srv.viewForLang(httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites", nil), "zh", "home")
	if strings.Contains(adminView.Site.InjectHead, "G-TEST123") {
		t.Fatalf("admin view should not inject analytics snippet: %q", adminView.Site.InjectHead)
	}
	previewReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites/1/preview/zh/", nil)
	previewReq = previewReq.WithContext(withPreviewNoindex(previewReq.Context()))
	previewView := srv.viewForLang(previewReq, "zh", "home")
	if strings.Contains(previewView.Site.InjectHead, "G-TEST123") {
		t.Fatalf("preview view should not inject analytics snippet: %q", previewView.Site.InjectHead)
	}
	visualView := srv.viewForLang(httptest.NewRequest(http.MethodGet, "https://platform.test/zh/?visual_edit=1", nil), "zh", "home")
	if strings.Contains(visualView.Site.InjectHead, "G-TEST123") {
		t.Fatalf("visual edit view should not inject analytics snippet: %q", visualView.Site.InjectHead)
	}
	deleteForm := url.Values{}
	deleteForm.Set("_csrf", loginSess.CSRF)
	deleteForm.Set("service", platform.GoogleServiceAnalytics)
	deleteForm.Set("google_account_id", "google-account-1")
	deleteResp := httptest.NewRecorder()
	deleteReq := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/google/accounts/delete", strings.NewReader(deleteForm.Encode()))
	deleteReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	deleteReq.Header.Set("Accept", "application/json")
	deleteReq.AddCookie(loginCookie)
	h.ServeHTTP(deleteResp, deleteReq)
	if deleteResp.Code != http.StatusOK || !strings.Contains(deleteResp.Body.String(), `"ok":true`) {
		t.Fatalf("delete google account status/location/body = %d %q %q", deleteResp.Code, deleteResp.Header().Get("Location"), deleteResp.Body.String())
	}
	accounts, err := ps.GoogleAccounts(platform.GoogleServiceAnalytics)
	if err != nil {
		t.Fatalf("list google accounts after delete: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("got %d google accounts after delete, want 0", len(accounts))
	}

	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "env-client.apps.googleusercontent.com")
	start := httptest.NewRecorder()
	startReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/google/oauth/start?service=all", nil)
	startReq.AddCookie(loginCookie)
	h.ServeHTTP(start, startReq)
	if start.Code != http.StatusSeeOther {
		t.Fatalf("oauth start status = %d", start.Code)
	}
	loc := start.Header().Get("Location")
	if !strings.Contains(loc, "client_id=test-client-2.apps.googleusercontent.com") {
		t.Fatalf("oauth start should use saved client id, location = %q", loc)
	}
	if strings.Contains(loc, "env-client.apps.googleusercontent.com") {
		t.Fatalf("oauth start used env client id instead of saved platform client id, location = %q", loc)
	}
	parsedLoc, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse oauth start location: %v", err)
	}
	if scope := parsedLoc.Query().Get("scope"); !strings.Contains(scope, googleAnalyticsEditScope) {
		t.Fatalf("oauth start scope should include analytics edit, scope = %q", scope)
	}
	if scope := parsedLoc.Query().Get("scope"); !strings.Contains(scope, googleSearchConsoleScope) {
		t.Fatalf("oauth start scope should include search console management scope, scope = %q", scope)
	}

	later := time.Now().Add(time.Hour)
	if err := ps.CreateGoogleOAuthState("test-state", platform.GoogleServiceAll, later); err != nil {
		t.Fatalf("create oauth state after config save: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonTestResponse(req *http.Request, status int, body string) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}
