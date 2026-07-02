package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
	"golang.org/x/crypto/bcrypt"
)

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
	if strings.Contains(beforeBody, `href="/admin/google/oauth/start?service=analytics"`) || strings.Contains(beforeBody, `href="/admin/google/oauth/start?service=search_console"`) {
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
	if !strings.Contains(body, "OAuth 已配置") || !strings.Contains(body, "test-client-2.apps.googleusercontent.com") || !strings.Contains(body, "/admin/google/oauth/start?service=analytics") || !strings.Contains(body, "/admin/google/oauth/start?service=search_console") {
		t.Fatalf("sites page did not render saved OAuth state")
	}

	if err := ps.UpsertGoogleAccount(&platform.GoogleAccount{
		Service:         platform.GoogleServiceAnalytics,
		GoogleAccountID: "google-account-1",
		Email:           "ga@example.com",
		Name:            "GA Account",
		AccessToken:     "access-token",
	}); err != nil {
		t.Fatalf("upsert google account: %v", err)
	}
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
	startReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/google/oauth/start?service=analytics", nil)
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

	later := time.Now().Add(time.Hour)
	if err := ps.CreateGoogleOAuthState("test-state", platform.GoogleServiceAnalytics, later); err != nil {
		t.Fatalf("create oauth state after config save: %v", err)
	}
}
