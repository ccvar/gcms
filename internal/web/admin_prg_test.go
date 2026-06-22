package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func authedAdminRequest(t *testing.T, s *Server, method, target string, form url.Values) (*http.Request, string) {
	t.Helper()
	token, err := s.sess.create("admin")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	dbSess, ok, err := s.store.GetAdminSession(token)
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	if form == nil {
		form = url.Values{}
	}
	form.Set("_csrf", dbSess.CSRF)
	req := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	return req, token
}

func TestAdminSettingsSaveUsesRedirectAfterPost(t *testing.T) {
	s := newTestPublicServer(t, "")
	form := url.Values{
		"theme":        {"editorial"},
		"theme_accent": {"#9a3b2f"},
		"theme_radius": {"10"},
	}
	req, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/settings/appearance", form)
	w := httptest.NewRecorder()

	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	if got, want := w.Header().Get("Location"), "/admin/settings/appearance"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestAdminCanCreatePageFromPagesScreen(t *testing.T) {
	s := newTestPublicServer(t, "")
	h := s.Handler()

	listReq, token := authedAdminRequest(t, s, http.MethodGet, "/admin/pages?lang=zh", nil)
	list := httptest.NewRecorder()
	h.ServeHTTP(list, listReq)
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", list.Code, list.Body.String())
	}
	if !strings.Contains(list.Body.String(), `href="/admin/pages/new?lang=zh"`) {
		t.Fatalf("pages list should render new page entry")
	}

	newReq := httptest.NewRequest(http.MethodGet, "/admin/pages/new?lang=zh", nil)
	newReq.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	newPage := httptest.NewRecorder()
	h.ServeHTTP(newPage, newReq)
	if newPage.Code != http.StatusOK {
		t.Fatalf("new page status = %d, body = %s", newPage.Code, newPage.Body.String())
	}
	if !strings.Contains(newPage.Body.String(), "新建页面") || strings.Contains(newPage.Body.String(), `class="btn act-view" href="/zh/"`) {
		t.Fatalf("new page form should render page create state without premature view link")
	}

	form := url.Values{
		"title":       {"团队介绍"},
		"slug":        {"team"},
		"content":     {"这里介绍团队。"},
		"excerpt":     {"团队与服务介绍。"},
		"meta_desc":   {"团队介绍页面"},
		"keywords":    {"团队,介绍"},
		"author":      {"gcms 团队"},
		"editor_mode": {"markdown"},
	}
	createReq, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/pages?lang=zh", form)
	created := httptest.NewRecorder()
	h.ServeHTTP(created, createReq)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	location := created.Header().Get("Location")
	if !strings.HasPrefix(location, "/admin/pages/") || !strings.HasSuffix(location, "/edit?saved=1") {
		t.Fatalf("create Location = %q", location)
	}
	page, err := s.store.GetPage("zh", "team")
	if err != nil {
		t.Fatalf("get created page: %v", err)
	}
	if page == nil {
		t.Fatalf("created page missing")
	}
	if page.Type != "page" || page.Status != "published" || page.Title != "团队介绍" || page.Content != "这里介绍团队。" {
		t.Fatalf("created page = %#v", page)
	}
}

func TestAdminAutomationSecretSurvivesSettingsRedirectOnce(t *testing.T) {
	s := newTestPublicServer(t, "")
	form := url.Values{"name": {"content helper"}}
	req, token := authedAdminRequest(t, s, http.MethodPost, "/admin/settings/automation/keys", form)
	w := httptest.NewRecorder()

	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	location := w.Header().Get("Location")
	if location != "/admin/settings/automation" {
		t.Fatalf("Location = %q, want /admin/settings/automation", location)
	}

	get := httptest.NewRequest(http.MethodGet, location, nil)
	get.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	page := httptest.NewRecorder()
	s.Handler().ServeHTTP(page, get)
	if page.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", page.Code, page.Body.String())
	}
	if !strings.Contains(page.Body.String(), `id="new-api-secret"`) {
		t.Fatalf("redirected page should show the one-time API secret")
	}

	again := httptest.NewRequest(http.MethodGet, location, nil)
	again.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	second := httptest.NewRecorder()
	s.Handler().ServeHTTP(second, again)
	if strings.Contains(second.Body.String(), `id="new-api-secret"`) {
		t.Fatalf("one-time API secret should be consumed after first GET")
	}
}

func TestCloudflareManualSyncDisablesAutomaticDeploy(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(cloudflareAPITokenKey, "token"); err != nil {
		t.Fatalf("set token: %v", err)
	}
	if err := s.store.SetSetting(cloudflareDeployModeKey, cloudflareModeWorkerAssets); err != nil {
		t.Fatalf("set deploy mode: %v", err)
	}
	if err := s.store.SetSetting(cloudflareWorkerNameKey, "gcms-test"); err != nil {
		t.Fatalf("set worker: %v", err)
	}
	if err := s.store.SetSetting(cloudflareDomainsKey, encodeCloudflareDomains([]CloudflareDomain{{Host: "www.example.com", Primary: true}})); err != nil {
		t.Fatalf("set domains: %v", err)
	}

	form := url.Values{
		"sync_mode": {"manual"},
		"sync_time": {"03:00"},
	}
	req, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/settings/cloudflare/sync", form)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}
	if got := s.store.Setting(cloudflareSyncModeKey); got != cloudflareSyncModeManual {
		t.Fatalf("sync mode = %q, want manual", got)
	}
	if got := s.store.Setting(cloudflareAutoSyncKey); got != "0" {
		t.Fatalf("auto sync = %q, want 0", got)
	}

	s.clearGeneratedCaches()
	if got := s.store.Setting(cloudflareSyncPendingKey); got != "1" {
		t.Fatalf("sync pending = %q, want 1", got)
	}
	if got := s.store.Setting(cloudflareSyncNextAtKey); got != "" {
		t.Fatalf("sync next at = %q, want empty for manual sync", got)
	}
}
