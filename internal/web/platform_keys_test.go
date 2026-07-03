package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
)

func platformAdminSession(t *testing.T, ps *platform.Store) *http.Cookie {
	t.Helper()
	if err := ps.CreateAdminSession("pkeys-token", "admin", "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create admin session: %v", err)
	}
	return &http.Cookie{Name: cookieName, Value: "pkeys-token"}
}

func getAutomation(t *testing.T, h http.Handler, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/automation", nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	return rec
}

func postKeyForm(t *testing.T, h http.Handler, cookie *http.Cookie, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	form.Set("_csrf", "csrf")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://platform.test"+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	return rec
}

func TestPlatformKeysUIRendersSection(t *testing.T) {
	_, h, ps, _, _ := setupPlatformAutomation(t)
	cookie := platformAdminSession(t, ps)

	rec := getAutomation(t, h, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/automation = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, needle := range []string{
		`id="platform-keys"`,
		`平台 AI 访问密钥`,
		`href="#platform-key-modal"`,
		`action="/admin/sites/automation/keys"`,
		`name="membership" value="allowlist"`,
		`name="membership" value="all"`,
		`name="site_ids"`,
		`name="scopes" value="posts:write"`,
		`href="/admin/automation"`, // the nav entry links back to this page
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("automation page missing %q", needle)
		}
	}

	// The section must have moved OFF the sites-management page.
	sites := httptest.NewRecorder()
	sreq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites", nil)
	sreq.AddCookie(cookie)
	h.ServeHTTP(sites, sreq)
	if sites.Code != http.StatusOK {
		t.Fatalf("GET /admin/sites = %d", sites.Code)
	}
	if strings.Contains(sites.Body.String(), `id="platform-keys"`) {
		t.Fatalf("platform-keys section should no longer render on /admin/sites")
	}
	// But the sites page still links to the new automation page via the nav.
	if !strings.Contains(sites.Body.String(), `href="/admin/automation"`) {
		t.Fatalf("sites page nav missing the AI 接入 link")
	}
}

func TestPlatformKeysCreateRevealAndManage(t *testing.T) {
	_, h, ps, defaultSite, blogSite := setupPlatformAutomation(t)
	cookie := platformAdminSession(t, ps)

	// Create an allowlist key scoped to blog only.
	create := postKeyForm(t, h, cookie, "/admin/sites/automation/keys", url.Values{
		"name":       {"多站助手"},
		"membership": {"allowlist"},
		"site_ids":   {strconv.FormatInt(blogSite.ID, 10)},
		"scopes":     {"posts:read", "posts:write"},
	})
	if create.Code != http.StatusSeeOther {
		t.Fatalf("create key status = %d, body=%s", create.Code, create.Body.String())
	}
	if loc := create.Header().Get("Location"); loc != "/admin/automation" {
		t.Fatalf("create redirect = %q, want /admin/automation", loc)
	}

	// Store now has the key with the right membership + allowlist.
	keys, err := ps.ListPlatformKeys()
	if err != nil || len(keys) != 1 {
		t.Fatalf("list keys: n=%d err=%v", len(keys), err)
	}
	key := keys[0]
	if key.Name != "多站助手" || key.MembershipMode != platform.KeyMembershipAllowlist {
		t.Fatalf("key mismatch: %#v", key)
	}
	if len(key.AllowedSiteIDs) != 1 || key.AllowedSiteIDs[0] != blogSite.ID {
		t.Fatalf("allowlist = %v, want [blog]", key.AllowedSiteIDs)
	}
	if !strings.Contains(key.Scopes, "posts:write") || !strings.Contains(key.Scopes, "posts:read") {
		t.Fatalf("scopes = %q", key.Scopes)
	}
	if key.TokenPrefix == "" || !strings.HasPrefix(key.TokenPrefix, "gcmsp_") {
		t.Fatalf("token prefix = %q, want gcmsp_...", key.TokenPrefix)
	}

	// The follow-up GET shows the reveal-once secret modal with a gcmsp_ token and the platform pack download.
	reveal := getAutomation(t, h, cookie)
	rbody := reveal.Body.String()
	if !strings.Contains(rbody, `id="platform-secret-modal"`) {
		t.Fatalf("reveal modal not rendered")
	}
	if !strings.Contains(rbody, `action="/admin/automation/platform-skill.zip"`) {
		t.Fatalf("reveal modal missing platform pack download form")
	}
	if !strings.Contains(rbody, "gcmsp_") {
		t.Fatalf("reveal modal did not show a gcmsp_ token")
	}
	// The key row shows the blog site as manageable (membership) and its scopes.
	if !strings.Contains(rbody, "多站助手") {
		t.Fatalf("key row not listed")
	}
	// Reveal is one-shot: a second GET no longer shows the secret modal.
	if second := getAutomation(t, h, cookie); strings.Contains(second.Body.String(), `id="platform-secret-modal"`) {
		t.Fatalf("secret modal should not persist across reloads")
	}

	// Update: switch to all-sites membership + add a scope.
	upd := postKeyForm(t, h, cookie, "/admin/sites/automation/keys/update", url.Values{
		"id":         {strconv.FormatInt(key.ID, 10)},
		"name":       {"多站助手改"},
		"membership": {"all"},
		"scopes":     {"posts:read", "posts:write", "links:read"},
	})
	if upd.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d, body=%s", upd.Code, upd.Body.String())
	}
	updated, _, _ := ps.GetPlatformKey(key.ID)
	if updated.Name != "多站助手改" || updated.MembershipMode != platform.KeyMembershipAll {
		t.Fatalf("update failed: %#v", updated)
	}
	if len(updated.AllowedSiteIDs) != 0 {
		t.Fatalf("all-mode should clear allowlist, got %v", updated.AllowedSiteIDs)
	}

	// Regenerate rotates the token (row survives, still active).
	regen := postKeyForm(t, h, cookie, "/admin/sites/automation/keys/regenerate", url.Values{"id": {strconv.FormatInt(key.ID, 10)}})
	if regen.Code != http.StatusSeeOther {
		t.Fatalf("regenerate status = %d", regen.Code)
	}
	if k, ok, _ := ps.GetPlatformKey(key.ID); !ok || !k.Active() {
		t.Fatalf("key should remain active after regenerate")
	}

	// Revoke → key inactive.
	rev := postKeyForm(t, h, cookie, "/admin/sites/automation/keys/revoke", url.Values{"id": {strconv.FormatInt(key.ID, 10)}})
	if rev.Code != http.StatusSeeOther {
		t.Fatalf("revoke status = %d", rev.Code)
	}
	if k, _, _ := ps.GetPlatformKey(key.ID); k.Active() {
		t.Fatalf("key should be inactive after revoke")
	}

	// Delete a non-revoked key is refused; deleting the revoked one succeeds.
	del := postKeyForm(t, h, cookie, "/admin/sites/automation/keys/delete", url.Values{"id": {strconv.FormatInt(key.ID, 10)}})
	if del.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d", del.Code)
	}
	if _, ok, _ := ps.GetPlatformKey(key.ID); ok {
		t.Fatalf("key should be gone after delete")
	}

	_ = defaultSite
}

func TestPlatformKeysValidationErrorsUseFlash(t *testing.T) {
	_, h, ps, _, _ := setupPlatformAutomation(t)
	cookie := platformAdminSession(t, ps)

	// Empty name → error surfaced via top Flash banner on the automation page.
	create := postKeyForm(t, h, cookie, "/admin/sites/automation/keys", url.Values{"name": {""}, "membership": {"all"}})
	if create.Code != http.StatusSeeOther {
		t.Fatalf("create-empty status = %d", create.Code)
	}
	if loc := create.Header().Get("Location"); loc != "/admin/automation" {
		t.Fatalf("error redirect = %q, want /admin/automation", loc)
	}
	page := getAutomation(t, h, cookie)
	body := page.Body.String()
	if !strings.Contains(body, `class="flash"`) || !strings.Contains(body, "用途名称不能为空") {
		t.Fatalf("empty-name error not shown as flash: %s", body)
	}
	if keys, _ := ps.ListPlatformKeys(); len(keys) != 0 {
		t.Fatalf("empty-name create should not persist a key, got %d", len(keys))
	}

	// Revoking a bogus id reports an error (not a false success).
	rev := postKeyForm(t, h, cookie, "/admin/sites/automation/keys/revoke", url.Values{"id": {"999999"}})
	if rev.Code != http.StatusSeeOther {
		t.Fatalf("revoke-bogus status = %d", rev.Code)
	}
	after := getAutomation(t, h, cookie).Body.String()
	if !strings.Contains(after, "访问权限不存在") {
		t.Fatalf("revoke of nonexistent id should report 访问权限不存在, body: %s", after)
	}
}

func TestPlatformKeysDeleteActiveRefused(t *testing.T) {
	_, h, ps, _, _ := setupPlatformAutomation(t)
	cookie := platformAdminSession(t, ps)
	id, err := ps.CreatePlatformKey("bot", "gcmsp_active12345678", "gcmsp_active1", platform.KeyMembershipAll, "posts:read", nil, time.Time{})
	if err != nil {
		t.Fatalf("seed key: %v", err)
	}
	// Deleting an active (non-revoked) key must be refused; the key survives.
	del := postKeyForm(t, h, cookie, "/admin/sites/automation/keys/delete", url.Values{"id": {strconv.FormatInt(id, 10)}})
	if del.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d", del.Code)
	}
	if _, ok, _ := ps.GetPlatformKey(id); !ok {
		t.Fatalf("active key must not be deleted")
	}
}
