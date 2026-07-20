package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const nonDefaultTestPassword = "changed-password"

func nonDefaultTestPasswordHash(t *testing.T) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(nonDefaultTestPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash non-default test password: %v", err)
	}
	return string(hash)
}

func TestDefaultPasswordMustBeChangedBeforeUsingAdmin(t *testing.T) {
	s := newTestPublicServer(t, "")
	h := s.Handler()

	loginForm := url.Values{"username": {"admin"}, "password": {store.DefaultAdminPassword}}
	loginReq := httptest.NewRequest(http.MethodPost, "https://example.test/admin/login", strings.NewReader(loginForm.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login := httptest.NewRecorder()
	h.ServeHTTP(login, loginReq)
	if login.Code != http.StatusSeeOther || login.Header().Get("Location") != "/admin/settings/security" {
		t.Fatalf("default login status/location = %d %q", login.Code, login.Header().Get("Location"))
	}
	var cookie *http.Cookie
	for _, candidate := range login.Result().Cookies() {
		if candidate.Name == cookieName {
			cookie = candidate
			break
		}
	}
	if cookie == nil {
		t.Fatal("login did not set session cookie")
	}

	blockedReq := httptest.NewRequest(http.MethodGet, "https://example.test/admin/posts", nil)
	blockedReq.AddCookie(cookie)
	blocked := httptest.NewRecorder()
	h.ServeHTTP(blocked, blockedReq)
	if blocked.Code != http.StatusSeeOther || blocked.Header().Get("Location") != "/admin/settings/security" {
		t.Fatalf("blocked admin status/location = %d %q", blocked.Code, blocked.Header().Get("Location"))
	}

	securityReq := httptest.NewRequest(http.MethodGet, "https://example.test/admin/settings/security", nil)
	securityReq.AddCookie(cookie)
	security := httptest.NewRecorder()
	h.ServeHTTP(security, securityReq)
	if security.Code != http.StatusOK {
		t.Fatalf("security status = %d, body = %s", security.Code, security.Body.String())
	}
	body := security.Body.String()
	if !strings.Contains(body, "首次登录需要先修改默认密码") || strings.Contains(body, "data-pw-dismiss") {
		t.Fatalf("security page did not render the mandatory warning correctly")
	}
	match := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("security page missing csrf")
	}

	postPassword := func(next string) *httptest.ResponseRecorder {
		form := url.Values{
			"_csrf":            {match[1]},
			"current_password": {store.DefaultAdminPassword},
			"new_password":     {next},
			"confirm_password": {next},
		}
		req := httptest.NewRequest(http.MethodPost, "https://example.test/admin/settings/security", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	stillDefault := postPassword(store.DefaultAdminPassword)
	if stillDefault.Code != http.StatusBadRequest || !strings.Contains(stillDefault.Body.String(), "不能继续使用默认密码") {
		t.Fatalf("default password reuse status/body = %d %s", stillDefault.Code, stillDefault.Body.String())
	}
	changed := postPassword(nonDefaultTestPassword)
	if changed.Code != http.StatusSeeOther || changed.Header().Get("Location") != "/admin/settings/security" {
		t.Fatalf("change status/location = %d %q", changed.Code, changed.Header().Get("Location"))
	}

	allowedReq := httptest.NewRequest(http.MethodGet, "https://example.test/admin/posts", nil)
	allowedReq.AddCookie(cookie)
	allowed := httptest.NewRecorder()
	h.ServeHTTP(allowed, allowedReq)
	if allowed.Code != http.StatusOK {
		t.Fatalf("admin should be available after password change: %d", allowed.Code)
	}
}

func TestPlatformDefaultPasswordLoginRequiresChange(t *testing.T) {
	_, h, ps, _, _ := setupPlatformAutomation(t)
	loginForm := url.Values{"username": {"admin"}, "password": {store.DefaultAdminPassword}}
	loginReq := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/login", strings.NewReader(loginForm.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	login := httptest.NewRecorder()
	h.ServeHTTP(login, loginReq)
	if login.Code != http.StatusSeeOther || login.Header().Get("Location") != "/admin/security" {
		t.Fatalf("platform default login status/location = %d %q", login.Code, login.Header().Get("Location"))
	}
	var cookie *http.Cookie
	for _, candidate := range login.Result().Cookies() {
		if candidate.Name == cookieName {
			cookie = candidate
			break
		}
	}
	if cookie == nil {
		t.Fatal("platform login did not set session cookie")
	}
	sess, ok, err := ps.GetAdminSession(cookie.Value)
	if err != nil || !ok || !sess.MustChangePassword {
		t.Fatalf("platform session password-change state: %#v ok=%v err=%v", sess, ok, err)
	}

	blockedReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites", nil)
	blockedReq.AddCookie(cookie)
	blocked := httptest.NewRecorder()
	h.ServeHTTP(blocked, blockedReq)
	if blocked.Code != http.StatusSeeOther || blocked.Header().Get("Location") != "/admin/security" {
		t.Fatalf("platform blocked status/location = %d %q", blocked.Code, blocked.Header().Get("Location"))
	}
}
