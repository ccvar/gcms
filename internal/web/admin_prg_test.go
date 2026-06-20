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
