package web

import (
	"net/http"
	"net/http/httptest"
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

func TestMultisiteRuntimeRoutesByHost(t *testing.T) {
	dir := t.TempDir()

	defaultDB := filepath.Join(dir, "default.db")
	defaultStore, err := store.Open(defaultDB)
	if err != nil {
		t.Fatalf("open default store: %v", err)
	}
	t.Cleanup(func() { _ = defaultStore.Close() })
	if err := defaultStore.SetSetting("site.name", "Default Runtime Site"); err != nil {
		t.Fatalf("set default site name: %v", err)
	}

	otherDB := filepath.Join(dir, "other.db")
	otherStore, err := store.Open(otherDB)
	if err != nil {
		t.Fatalf("open other store: %v", err)
	}
	if err := otherStore.SetSetting("site.name", "Blog Runtime Site"); err != nil {
		t.Fatalf("set other site name: %v", err)
	}
	otherToken, otherPrefix := newAutomationToken()
	if _, err := otherStore.CreateAutomationKey("blog bot", otherToken, otherPrefix, "languages:read"); err != nil {
		t.Fatalf("create other automation key: %v", err)
	}
	if err := otherStore.Close(); err != nil {
		t.Fatalf("close other store: %v", err)
	}

	ps, err := platform.Open(filepath.Join(dir, "system.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	hash, err := bcrypt.GenerateFromPassword([]byte(store.DefaultAdminPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := ps.BootstrapDefaultSite(platform.DefaultSiteBootstrap{
		Slug:                        "main",
		Name:                        "Default Runtime Site",
		DBPath:                      defaultDB,
		UploadDir:                   filepath.Join(dir, "default-uploads"),
		AdminUser:                   "admin",
		AdminPasswordHash:           string(hash),
		ManagementAutomationEnabled: true,
	}); err != nil {
		t.Fatalf("bootstrap default site: %v", err)
	}
	defaultSite, err := ps.DefaultSite()
	if err != nil {
		t.Fatalf("default site: %v", err)
	}
	otherSite, err := ps.CreateSite("blog", "Blog Runtime Site", otherDB, filepath.Join(dir, "blog-uploads"), true)
	if err != nil {
		t.Fatalf("create other site: %v", err)
	}
	if err := ps.AddSiteDomain(otherSite.ID, "https", "blog.test", true, true); err != nil {
		t.Fatalf("add other domain: %v", err)
	}

	srv, err := NewWithPlatform(defaultStore, ps, "https://platform.test", filepath.Join(dir, "default-uploads"), os.DirFS("../.."), os.DirFS("../.."))
	if err != nil {
		t.Fatalf("new multisite server: %v", err)
	}
	h := srv.Handler()

	defaultResp := httptest.NewRecorder()
	h.ServeHTTP(defaultResp, httptest.NewRequest(http.MethodGet, "https://platform.test/zh/", nil))
	if defaultResp.Code != http.StatusOK {
		t.Fatalf("default status = %d, body = %s", defaultResp.Code, defaultResp.Body.String())
	}
	if !strings.Contains(defaultResp.Body.String(), "Default Runtime Site") {
		t.Fatalf("default host did not render default store")
	}

	otherResp := httptest.NewRecorder()
	h.ServeHTTP(otherResp, httptest.NewRequest(http.MethodGet, "https://blog.test/zh/", nil))
	if otherResp.Code != http.StatusOK {
		t.Fatalf("other status = %d, body = %s", otherResp.Code, otherResp.Body.String())
	}
	if !strings.Contains(otherResp.Body.String(), "Blog Runtime Site") {
		t.Fatalf("bound host did not render bound store")
	}

	unknownResp := httptest.NewRecorder()
	h.ServeHTTP(unknownResp, httptest.NewRequest(http.MethodGet, "https://unknown.test/zh/", nil))
	if unknownResp.Code != http.StatusNotFound {
		t.Fatalf("unknown host status = %d, want 404", unknownResp.Code)
	}

	platformAPI := httptest.NewRecorder()
	platformReq := httptest.NewRequest(http.MethodGet, "https://platform.test/api/platform/v1/sites/"+strconv.FormatInt(otherSite.ID, 10)+"/languages", nil)
	platformReq.Header.Set("Authorization", "Bearer "+otherToken)
	h.ServeHTTP(platformAPI, platformReq)
	if platformAPI.Code != http.StatusOK {
		t.Fatalf("platform api status = %d, body = %s", platformAPI.Code, platformAPI.Body.String())
	}

	crossSite := httptest.NewRecorder()
	crossReq := httptest.NewRequest(http.MethodGet, "https://platform.test/api/platform/v1/sites/"+strconv.FormatInt(defaultSite.ID, 10)+"/languages", nil)
	crossReq.Header.Set("Authorization", "Bearer "+otherToken)
	h.ServeHTTP(crossSite, crossReq)
	if crossSite.Code != http.StatusUnauthorized {
		t.Fatalf("cross-site token status = %d, want 401", crossSite.Code)
	}

	if err := ps.CreateAdminSession("preview-token", "admin", "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create preview session: %v", err)
	}
	preview := httptest.NewRecorder()
	previewReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites/"+strconv.FormatInt(otherSite.ID, 10)+"/preview/zh/", nil)
	previewReq.AddCookie(&http.Cookie{Name: cookieName, Value: "preview-token"})
	h.ServeHTTP(preview, previewReq)
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status = %d, body = %s", preview.Code, preview.Body.String())
	}
	if got := preview.Header().Get("X-Robots-Tag"); got != "noindex, nofollow" {
		t.Fatalf("preview robots header = %q", got)
	}
	if body := preview.Body.String(); !strings.Contains(body, "Blog Runtime Site") || !strings.Contains(body, `<meta name="robots" content="noindex, nofollow">`) {
		t.Fatalf("preview did not render noindex blog page")
	}

	if err := ps.CreateAdminSession("prefix-token", "admin", "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create prefix session: %v", err)
	}
	prefixed := httptest.NewRecorder()
	prefixedReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites/"+strconv.FormatInt(otherSite.ID, 10)+"/posts", nil)
	prefixedReq.AddCookie(&http.Cookie{Name: cookieName, Value: "prefix-token"})
	h.ServeHTTP(prefixed, prefixedReq)
	if prefixed.Code != http.StatusSeeOther || prefixed.Header().Get("Location") != "/admin/posts" {
		t.Fatalf("prefixed admin status/location = %d %q", prefixed.Code, prefixed.Header().Get("Location"))
	}
	prefixSess, ok, err := ps.GetAdminSession("prefix-token")
	if err != nil || !ok {
		t.Fatalf("get prefix session: ok=%v err=%v", ok, err)
	}
	if prefixSess.CurrentSiteID != otherSite.ID {
		t.Fatalf("prefix current site = %d, want %d", prefixSess.CurrentSiteID, otherSite.ID)
	}
}
