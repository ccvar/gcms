package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
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
	legacyAdminI18N := `{"admin.nav.posts":"Legacy Posts"}`
	if err := defaultStore.SetSetting("admin_i18n::en", legacyAdminI18N); err != nil {
		t.Fatalf("set legacy admin i18n: %v", err)
	}
	defaultToken, defaultPrefix := newAutomationToken()
	if _, err := defaultStore.CreateAutomationKey("default bot", defaultToken, defaultPrefix, "posts:write"); err != nil {
		t.Fatalf("create default automation key: %v", err)
	}
	defaultUploadDir := filepath.Join(dir, "default-uploads")
	if err := os.MkdirAll(defaultUploadDir, 0o755); err != nil {
		t.Fatalf("create default upload dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(defaultUploadDir, "favicon.ico"), []byte("default icon"), 0o644); err != nil {
		t.Fatalf("write default legacy favicon: %v", err)
	}

	otherDB := filepath.Join(dir, "other.db")
	otherStore, err := store.Open(otherDB)
	if err != nil {
		t.Fatalf("open other store: %v", err)
	}
	if err := otherStore.SetSetting("site.name", "Blog Runtime Site"); err != nil {
		t.Fatalf("set other site name: %v", err)
	}
	if err := otherStore.SetSetting("site.favicon", "/uploads/blog-icon.ico"); err != nil {
		t.Fatalf("set other site favicon: %v", err)
	}
	if _, err := otherStore.CreatePost(&store.Post{
		Type:       "post",
		Lang:       "zh",
		Slug:       "preview-internal-link",
		Title:      "Preview Internal Link",
		Status:     "published",
		EditorMode: "markdown",
	}); err != nil {
		t.Fatalf("create other preview post: %v", err)
	}
	otherToken, otherPrefix := newAutomationToken()
	if _, err := otherStore.CreateAutomationKey("blog bot", otherToken, otherPrefix, "languages:read,posts:write"); err != nil {
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
		UploadDir:                   defaultUploadDir,
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
	if err := ps.AddSiteDomain(defaultSite.ID, "https", "default-bound.test", true, true); err != nil {
		t.Fatalf("add default domain: %v", err)
	}
	otherUploadDir := filepath.Join(dir, "blog-uploads")
	if err := os.MkdirAll(otherUploadDir, 0o755); err != nil {
		t.Fatalf("create other upload dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(otherUploadDir, "blog-icon.ico"), []byte("icon"), 0o644); err != nil {
		t.Fatalf("write other favicon: %v", err)
	}
	otherSite, err := ps.CreateSite("blog", "Blog Runtime Site", otherDB, otherUploadDir, true)
	if err != nil {
		t.Fatalf("create other site: %v", err)
	}
	if err := ps.AddSiteDomain(otherSite.ID, "https", "blog.test", true, true); err != nil {
		t.Fatalf("add other domain: %v", err)
	}

	srv, err := NewWithPlatform(defaultStore, ps, "https://platform.test", defaultUploadDir, os.DirFS("../.."), os.DirFS("../.."))
	if err != nil {
		t.Fatalf("new multisite server: %v", err)
	}
	if got := ps.Setting("admin_i18n::en"); got != legacyAdminI18N {
		t.Fatalf("platform admin i18n migration = %q, want %q", got, legacyAdminI18N)
	}
	h := srv.Handler()

	login := httptest.NewRecorder()
	loginForm := url.Values{"username": {"admin"}, "password": {store.DefaultAdminPassword}}
	loginReq := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/login", strings.NewReader(loginForm.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(login, loginReq)
	if login.Code != http.StatusSeeOther || login.Header().Get("Location") != "/admin/sites" {
		t.Fatalf("login status/location = %d %q", login.Code, login.Header().Get("Location"))
	}
	var loginCookie *http.Cookie
	for _, c := range login.Result().Cookies() {
		if c.Name == cookieName {
			loginCookie = c
			break
		}
	}
	if loginCookie == nil {
		t.Fatalf("login did not set session cookie")
	}
	loginSess, ok, err := ps.GetAdminSession(loginCookie.Value)
	if err != nil || !ok {
		t.Fatalf("get login session: ok=%v err=%v", ok, err)
	}
	if loginSess.CurrentSiteID != 0 {
		t.Fatalf("login current site id = %d, want 0", loginSess.CurrentSiteID)
	}
	postPlatformForm := func(path string, form url.Values) *httptest.ResponseRecorder {
		if form == nil {
			form = url.Values{}
		}
		form.Set("_csrf", loginSess.CSRF)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "https://platform.test"+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(loginCookie)
		h.ServeHTTP(rec, req)
		return rec
	}
	getPlatform := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://platform.test"+path, nil)
		req.AddCookie(loginCookie)
		h.ServeHTTP(rec, req)
		return rec
	}
	adminWithoutSite := httptest.NewRecorder()
	adminReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin", nil)
	adminReq.AddCookie(loginCookie)
	h.ServeHTTP(adminWithoutSite, adminReq)
	if adminWithoutSite.Code != http.StatusSeeOther || adminWithoutSite.Header().Get("Location") != "/admin/sites" {
		t.Fatalf("admin without site status/location = %d %q", adminWithoutSite.Code, adminWithoutSite.Header().Get("Location"))
	}
	platformPage := httptest.NewRecorder()
	platformSitesReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites", nil)
	platformSitesReq.AddCookie(loginCookie)
	h.ServeHTTP(platformPage, platformSitesReq)
	if platformPage.Code != http.StatusOK {
		t.Fatalf("platform page status = %d, body = %s", platformPage.Code, platformPage.Body.String())
	}
	if body := platformPage.Body.String(); strings.Contains(body, `href="/admin/posts"`) || strings.Contains(body, `href="/admin/settings"`) {
		t.Fatalf("platform page leaked site admin navigation")
	}
	if body := platformPage.Body.String(); strings.Contains(body, `sites-list-panel`) || strings.Contains(body, `overview-panel-head`) {
		t.Fatalf("platform page rendered an outer site-list panel")
	}
	if body := platformPage.Body.String(); !strings.Contains(body, `id="pw-warn"`) {
		t.Fatalf("platform page did not render password warning")
	}
	if body := platformPage.Body.String(); !strings.Contains(body, `2 个站点`) {
		t.Fatalf("platform page did not render site count")
	}
	if body := platformPage.Body.String(); !strings.Contains(body, `data-site-create-open`) || !strings.Contains(body, `data-site-create-modal`) {
		t.Fatalf("platform page did not render create-site modal")
	}
	if body := platformPage.Body.String(); !strings.Contains(body, `site-card is-default`) || !strings.Contains(body, `href="/admin/platform/settings"`) || strings.Contains(body, `href="/admin/updates"`) || strings.Contains(body, `href="/admin/admin-i18n"`) {
		t.Fatalf("platform page did not render default-site card state or platform nav")
	}
	if body := platformPage.Body.String(); strings.Contains(body, `href="/admin/settings/updates"`) {
		t.Fatalf("platform page rendered old site-level update link")
	}
	defaultIconPath := "/admin/sites/" + strconv.FormatInt(defaultSite.ID, 10) + "/uploads/favicon.ico"
	otherIconPath := "/admin/sites/" + strconv.FormatInt(otherSite.ID, 10) + "/uploads/blog-icon.ico"
	if body := platformPage.Body.String(); !strings.Contains(body, `class="site-card-icon"`) || !strings.Contains(body, defaultIconPath) || !strings.Contains(body, otherIconPath) {
		t.Fatalf("platform page did not render site icons")
	}
	defaultIcon := httptest.NewRecorder()
	defaultIconReq := httptest.NewRequest(http.MethodGet, "https://platform.test"+defaultIconPath, nil)
	defaultIconReq.AddCookie(loginCookie)
	h.ServeHTTP(defaultIcon, defaultIconReq)
	if defaultIcon.Code != http.StatusOK {
		t.Fatalf("default site legacy upload icon status = %d, body = %s", defaultIcon.Code, defaultIcon.Body.String())
	}
	if err := os.Remove(filepath.Join(defaultUploadDir, "favicon.ico")); err != nil {
		t.Fatalf("remove default legacy favicon: %v", err)
	}
	if got := srv.platformSiteIconURL(defaultSite.ID); got != defaultFaviconPath {
		t.Fatalf("default site icon fallback = %q, want %q", got, defaultFaviconPath)
	}
	if err := os.WriteFile(filepath.Join(defaultUploadDir, "favicon.ico"), []byte("default icon"), 0o644); err != nil {
		t.Fatalf("restore default legacy favicon: %v", err)
	}
	otherIcon := httptest.NewRecorder()
	otherIconReq := httptest.NewRequest(http.MethodGet, "https://platform.test"+otherIconPath, nil)
	otherIconReq.AddCookie(loginCookie)
	h.ServeHTTP(otherIcon, otherIconReq)
	if otherIcon.Code != http.StatusOK {
		t.Fatalf("site upload icon status = %d, body = %s", otherIcon.Code, otherIcon.Body.String())
	}
	defaultDomainAction := "/admin/sites/" + strconv.FormatInt(defaultSite.ID, 10) + "/domains"
	otherDomainAction := "/admin/sites/" + strconv.FormatInt(otherSite.ID, 10) + "/domains"
	if body := platformPage.Body.String(); strings.Contains(body, defaultDomainAction) || !strings.Contains(body, otherDomainAction) || !strings.Contains(body, `使用当前默认入口`) {
		t.Fatalf("platform page did not enforce default-site domain UI")
	}
	if body := platformPage.Body.String(); strings.Contains(body, `default-bound.test`) {
		t.Fatalf("platform page rendered a stored default-site domain")
	}
	if body := platformPage.Body.String(); !strings.Contains(body, `name="seed_mode" value="demo" checked`) || !strings.Contains(body, `name="seed_mode" value="empty"`) {
		t.Fatalf("platform page did not render site seed mode options")
	}
	if body := platformPage.Body.String(); strings.Contains(body, `/default"`) {
		t.Fatalf("platform page rendered set-default controls")
	}

	disableDefault := postPlatformForm("/admin/sites/"+strconv.FormatInt(defaultSite.ID, 10)+"/status", url.Values{"status": {"disabled"}})
	if disableDefault.Code != http.StatusBadRequest {
		t.Fatalf("disable default site status = %d, want 400", disableDefault.Code)
	}
	disableDefaultAutomation := postPlatformForm("/admin/sites/"+strconv.FormatInt(defaultSite.ID, 10)+"/automation", url.Values{"enabled": {"0"}})
	if disableDefaultAutomation.Code != http.StatusBadRequest {
		t.Fatalf("disable default platform entry status = %d, want 400", disableDefaultAutomation.Code)
	}
	setOtherDefault := postPlatformForm("/admin/sites/"+strconv.FormatInt(otherSite.ID, 10)+"/default", nil)
	if setOtherDefault.Code != http.StatusBadRequest {
		t.Fatalf("set other site default status = %d, want 400", setOtherDefault.Code)
	}
	bindDefaultDomain := postPlatformForm(defaultDomainAction, url.Values{"host": {"default.test"}})
	if bindDefaultDomain.Code != http.StatusBadRequest {
		t.Fatalf("bind default site domain status = %d, want 400", bindDefaultDomain.Code)
	}

	createBadSlug := postPlatformForm("/admin/sites", url.Values{
		"slug":       {"Bad Site"},
		"name":       {"Bad Runtime Site"},
		"seed_mode":  {"empty"},
		"automation": {"1"},
	})
	if createBadSlug.Code != http.StatusSeeOther || createBadSlug.Header().Get("Location") != "/admin/sites" {
		t.Fatalf("create bad slug status/location = %d %q, want 303 /admin/sites", createBadSlug.Code, createBadSlug.Header().Get("Location"))
	}
	createBadSlugPage := getPlatform("/admin/sites")
	if createBadSlugPage.Code != http.StatusOK {
		t.Fatalf("bad slug redirect page status = %d, body = %s", createBadSlugPage.Code, createBadSlugPage.Body.String())
	}
	if body := createBadSlugPage.Body.String(); !strings.Contains(body, `class="sites-title">站点管理`) || !strings.Contains(body, `2 个站点`) || !strings.Contains(body, "站点标记只能包含小写字母、数字和短横线") || !strings.Contains(body, `data-site-create-modal>`) || !strings.Contains(body, `value="Bad Site"`) {
		t.Fatalf("bad slug did not re-render sites page with modal error, body = %s", body)
	}
	createBadDomain := postPlatformForm("/admin/sites", url.Values{
		"slug":   {"bad-domain"},
		"name":   {"Bad Domain Site"},
		"domain": {"https://platform.test"},
	})
	if createBadDomain.Code != http.StatusSeeOther || createBadDomain.Header().Get("Location") != "/admin/sites" {
		t.Fatalf("create bad domain status/location = %d %q, want 303 /admin/sites", createBadDomain.Code, createBadDomain.Header().Get("Location"))
	}
	createBadDomainPage := getPlatform("/admin/sites")
	if createBadDomainPage.Code != http.StatusOK {
		t.Fatalf("bad domain redirect page status = %d, body = %s", createBadDomainPage.Code, createBadDomainPage.Body.String())
	}
	if body := createBadDomainPage.Body.String(); !strings.Contains(body, "非默认站点不能绑定平台默认 Host") || !strings.Contains(body, `value="bad-domain"`) {
		t.Fatalf("bad domain did not re-render sites page with form state, body = %s", body)
	}

	createEmpty := postPlatformForm("/admin/sites", url.Values{
		"slug":       {"empty"},
		"name":       {"Empty Runtime Site"},
		"seed_mode":  {"empty"},
		"automation": {"1"},
	})
	if createEmpty.Code != http.StatusSeeOther || createEmpty.Header().Get("Location") != "/admin/sites" {
		t.Fatalf("create empty site status/location = %d %q, body = %s", createEmpty.Code, createEmpty.Header().Get("Location"), createEmpty.Body.String())
	}
	var emptySite *platform.Site
	sites, err := ps.Sites()
	if err != nil {
		t.Fatalf("list platform sites: %v", err)
	}
	for _, site := range sites {
		if site.Slug == "empty" {
			emptySite = site
			break
		}
	}
	if emptySite == nil {
		t.Fatalf("created empty site not found")
	}
	emptySitePage := getPlatform("/admin/sites")
	if emptySitePage.Code != http.StatusOK {
		t.Fatalf("empty site page status = %d, body = %s", emptySitePage.Code, emptySitePage.Body.String())
	}
	if body := emptySitePage.Body.String(); !strings.Contains(body, `3 个站点`) {
		t.Fatalf("empty site page did not update site count")
	}
	if body := emptySitePage.Body.String(); !strings.Contains(body, "Empty Runtime Site") || !strings.Contains(body, `class="site-card-icon fallback"`) || !strings.Contains(body, `class="site-empty-icon"`) {
		t.Fatalf("empty site did not render no-icon fallback: %s", body)
	}
	emptyStore, err := store.Open(emptySite.DBPath)
	if err != nil {
		t.Fatalf("open empty site store: %v", err)
	}
	recentEmpty, err := emptyStore.ListRecentAdminContent("zh", 50)
	if closeErr := emptyStore.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("inspect empty site content: %v", err)
	}
	if len(recentEmpty) != 0 {
		t.Fatalf("empty site seeded %d content items, want 0", len(recentEmpty))
	}
	emptyRoot := filepath.Dir(emptySite.DBPath)
	if _, err := os.Stat(emptyRoot); err != nil {
		t.Fatalf("empty site root before archive: %v", err)
	}
	archiveEnabled := postPlatformForm("/admin/sites/"+strconv.FormatInt(emptySite.ID, 10)+"/archive", nil)
	if archiveEnabled.Code != http.StatusBadRequest {
		t.Fatalf("archive enabled site status = %d, want 400", archiveEnabled.Code)
	}
	disableEmpty := postPlatformForm("/admin/sites/"+strconv.FormatInt(emptySite.ID, 10)+"/status", url.Values{"status": {"disabled"}})
	if disableEmpty.Code != http.StatusSeeOther || disableEmpty.Header().Get("Location") != "/admin/sites" {
		t.Fatalf("disable empty site status/location = %d %q", disableEmpty.Code, disableEmpty.Header().Get("Location"))
	}
	disabledEmptyPage := getPlatform("/admin/sites")
	if disabledEmptyPage.Code != http.StatusOK {
		t.Fatalf("disabled empty page status = %d, body = %s", disabledEmptyPage.Code, disabledEmptyPage.Body.String())
	}
	if body := disabledEmptyPage.Body.String(); !strings.Contains(body, `/admin/sites/`+strconv.FormatInt(emptySite.ID, 10)+`/archive`) || !strings.Contains(body, "归档删除") || !strings.Contains(body, `site-card is-disabled`) || !strings.Contains(body, "站点已关闭") {
		t.Fatalf("disabled site did not render archive action and disabled mask: %s", body)
	}
	archiveEmpty := postPlatformForm("/admin/sites/"+strconv.FormatInt(emptySite.ID, 10)+"/archive", nil)
	if archiveEmpty.Code != http.StatusSeeOther || archiveEmpty.Header().Get("Location") != "/admin/sites" {
		t.Fatalf("archive empty site status/location = %d %q, body = %s", archiveEmpty.Code, archiveEmpty.Header().Get("Location"), archiveEmpty.Body.String())
	}
	if _, ok, err := ps.GetSite(emptySite.ID); err != nil || ok {
		t.Fatalf("archived site should not remain active: ok=%v err=%v", ok, err)
	}
	archived, err := ps.ArchivedSites()
	if err != nil {
		t.Fatalf("list archived sites: %v", err)
	}
	if len(archived) != 1 || archived[0].Slug != "empty" || archived[0].OriginalSiteID != emptySite.ID {
		t.Fatalf("archived sites mismatch: %#v", archived)
	}
	archivePath := archived[0].ArchivePath
	if _, err := os.Stat(emptyRoot); !os.IsNotExist(err) {
		t.Fatalf("empty active root after archive err = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(archivePath, "cms.db")); err != nil {
		t.Fatalf("archived cms.db missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(archivePath, "archive.json")); err != nil {
		t.Fatalf("archive manifest missing: %v", err)
	}
	archivedPage := getPlatform("/admin/archived-sites")
	if archivedPage.Code != http.StatusOK {
		t.Fatalf("archived sites page status = %d, body = %s", archivedPage.Code, archivedPage.Body.String())
	}
	if body := archivedPage.Body.String(); !strings.Contains(body, "Empty Runtime Site") || !strings.Contains(body, `class="archived-site-card"`) || !strings.Contains(body, `class="archived-site-icon fallback"`) || !strings.Contains(body, `data-copy-text="empty"`) || !strings.Contains(body, `data-confirm-input-name="confirm_slug"`) || !strings.Contains(body, `data-confirm-input-copy="empty"`) || !strings.Contains(body, `formaction="/admin/archived-sites/`+strconv.FormatInt(archived[0].ID, 10)+`/restore"`) || !strings.Contains(body, `formaction="/admin/archived-sites/`+strconv.FormatInt(archived[0].ID, 10)+`/delete"`) || !strings.Contains(body, "恢复站点") || !strings.Contains(body, "彻底删除") {
		t.Fatalf("archived sites page did not render archive record: %s", body)
	}
	restoreWrongSlug := postPlatformForm("/admin/archived-sites/"+strconv.FormatInt(archived[0].ID, 10)+"/restore", url.Values{"confirm_slug": {"wrong"}})
	if restoreWrongSlug.Code != http.StatusSeeOther || restoreWrongSlug.Header().Get("Location") != "/admin/archived-sites" {
		t.Fatalf("restore wrong slug status/location = %d %q", restoreWrongSlug.Code, restoreWrongSlug.Header().Get("Location"))
	}
	if archivedStillThere, err := ps.ArchivedSites(); err != nil || len(archivedStillThere) != 1 {
		t.Fatalf("wrong slug should keep archive before restore: len=%d err=%v", len(archivedStillThere), err)
	}
	restoreArchived := postPlatformForm("/admin/archived-sites/"+strconv.FormatInt(archived[0].ID, 10)+"/restore", url.Values{"confirm_slug": {"empty"}})
	if restoreArchived.Code != http.StatusSeeOther || restoreArchived.Header().Get("Location") != "/admin/sites" {
		t.Fatalf("restore archived status/location = %d %q", restoreArchived.Code, restoreArchived.Header().Get("Location"))
	}
	restoredSite, ok, err := ps.GetSite(emptySite.ID)
	if err != nil || !ok {
		t.Fatalf("restored site not found: ok=%v err=%v", ok, err)
	}
	if restoredSite.Status != "disabled" {
		t.Fatalf("restored site status = %q, want disabled", restoredSite.Status)
	}
	if _, err := os.Stat(emptyRoot); err != nil {
		t.Fatalf("empty active root after restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(emptyRoot, "archive.json")); !os.IsNotExist(err) {
		t.Fatalf("archive manifest after restore err = %v, want not exist", err)
	}
	if archivedGone, err := ps.ArchivedSites(); err != nil || len(archivedGone) != 0 {
		t.Fatalf("archived site should be restored out of archive list: len=%d err=%v", len(archivedGone), err)
	}

	archiveRestored := postPlatformForm("/admin/sites/"+strconv.FormatInt(restoredSite.ID, 10)+"/archive", nil)
	if archiveRestored.Code != http.StatusSeeOther || archiveRestored.Header().Get("Location") != "/admin/sites" {
		t.Fatalf("archive restored site status/location = %d %q, body = %s", archiveRestored.Code, archiveRestored.Header().Get("Location"), archiveRestored.Body.String())
	}
	archived, err = ps.ArchivedSites()
	if err != nil {
		t.Fatalf("list re-archived sites: %v", err)
	}
	if len(archived) != 1 || archived[0].Slug != "empty" || archived[0].OriginalSiteID != restoredSite.ID {
		t.Fatalf("re-archived sites mismatch: %#v", archived)
	}
	archivePath = archived[0].ArchivePath
	deleteWrongSlug := postPlatformForm("/admin/archived-sites/"+strconv.FormatInt(archived[0].ID, 10)+"/delete", url.Values{"confirm_slug": {"wrong"}})
	if deleteWrongSlug.Code != http.StatusSeeOther || deleteWrongSlug.Header().Get("Location") != "/admin/archived-sites" {
		t.Fatalf("delete wrong slug status/location = %d %q", deleteWrongSlug.Code, deleteWrongSlug.Header().Get("Location"))
	}
	if archivedStillThere, err := ps.ArchivedSites(); err != nil || len(archivedStillThere) != 1 {
		t.Fatalf("wrong slug should keep archive: len=%d err=%v", len(archivedStillThere), err)
	}
	deleteArchived := postPlatformForm("/admin/archived-sites/"+strconv.FormatInt(archived[0].ID, 10)+"/delete", url.Values{"confirm_slug": {"empty"}})
	if deleteArchived.Code != http.StatusSeeOther || deleteArchived.Header().Get("Location") != "/admin/archived-sites" {
		t.Fatalf("delete archived status/location = %d %q", deleteArchived.Code, deleteArchived.Header().Get("Location"))
	}
	if archivedGone, err := ps.ArchivedSites(); err != nil || len(archivedGone) != 0 {
		t.Fatalf("archived site should be deleted: len=%d err=%v", len(archivedGone), err)
	}
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("archive path after permanent delete err = %v, want not exist", err)
	}

	platformSettingsPage := httptest.NewRecorder()
	platformSettingsReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/platform/settings", nil)
	platformSettingsReq.AddCookie(loginCookie)
	h.ServeHTTP(platformSettingsPage, platformSettingsReq)
	if platformSettingsPage.Code != http.StatusOK {
		t.Fatalf("platform settings status = %d, body = %s", platformSettingsPage.Code, platformSettingsPage.Body.String())
	}
	if body := platformSettingsPage.Body.String(); !strings.Contains(body, `href="/admin/security"`) || !strings.Contains(body, `href="/admin/updates"`) || !strings.Contains(body, `href="/admin/admin-i18n"`) || !strings.Contains(body, `href="/admin/backups"`) || !strings.Contains(body, `href="/admin/archived-sites"`) || strings.Contains(body, `href="/admin/posts"`) {
		t.Fatalf("platform settings page did not render platform setting entries")
	}

	backupsPage := httptest.NewRecorder()
	backupsReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/backups", nil)
	backupsReq.AddCookie(loginCookie)
	h.ServeHTTP(backupsPage, backupsReq)
	if backupsPage.Code != http.StatusOK {
		t.Fatalf("backups page status = %d, body = %s", backupsPage.Code, backupsPage.Body.String())
	}
	if body := backupsPage.Body.String(); !strings.Contains(body, `action="/admin/backups"`) || !strings.Contains(body, `action="/admin/backups/config"`) || !strings.Contains(body, "S3-Compatible") || strings.Contains(body, `href="/admin/posts"`) {
		t.Fatalf("backups page did not render platform backup controls")
	}
	createBackup := postPlatformForm("/admin/backups", nil)
	if createBackup.Code != http.StatusSeeOther || createBackup.Header().Get("Location") != "/admin/backups" {
		t.Fatalf("create backup status/location = %d %q", createBackup.Code, createBackup.Header().Get("Location"))
	}
	backupsDir := filepath.Join(dir, "backups")
	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		t.Fatalf("read backups dir: %v", err)
	}
	var hasZip, hasRecord bool
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".zip") {
			hasZip = true
		}
		if strings.HasSuffix(entry.Name(), ".json") {
			hasRecord = true
		}
	}
	if !hasZip || !hasRecord {
		t.Fatalf("backup did not create zip and record in %s: zip=%v record=%v entries=%v", backupsDir, hasZip, hasRecord, entries)
	}

	securityPage := httptest.NewRecorder()
	securityReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/security", nil)
	securityReq.AddCookie(loginCookie)
	h.ServeHTTP(securityPage, securityReq)
	if securityPage.Code != http.StatusOK {
		t.Fatalf("platform security status = %d, body = %s", securityPage.Code, securityPage.Body.String())
	}
	if body := securityPage.Body.String(); !strings.Contains(body, `action="/admin/security"`) || strings.Contains(body, `href="/admin/posts"`) {
		t.Fatalf("platform security page did not render as platform-level page")
	}

	updatePage := httptest.NewRecorder()
	updateReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/updates", nil)
	updateReq.AddCookie(loginCookie)
	h.ServeHTTP(updatePage, updateReq)
	if updatePage.Code != http.StatusOK {
		t.Fatalf("platform updates status = %d, body = %s", updatePage.Code, updatePage.Body.String())
	}
	if body := updatePage.Body.String(); !strings.Contains(body, `data-status-url="/admin/updates/status"`) || !strings.Contains(body, `action="/admin/updates/upgrade"`) || strings.Contains(body, `href="/admin/posts"`) {
		t.Fatalf("platform updates page did not render as platform-level page")
	}
	oldUpdatePage := httptest.NewRecorder()
	oldUpdateReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/settings/updates", nil)
	oldUpdateReq.AddCookie(loginCookie)
	h.ServeHTTP(oldUpdatePage, oldUpdateReq)
	if oldUpdatePage.Code != http.StatusSeeOther || oldUpdatePage.Header().Get("Location") != "/admin/updates" {
		t.Fatalf("old update page status/location = %d %q", oldUpdatePage.Code, oldUpdatePage.Header().Get("Location"))
	}

	adminI18NPage := httptest.NewRecorder()
	adminI18NReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/admin-i18n?admin_lang=en", nil)
	adminI18NReq.AddCookie(loginCookie)
	h.ServeHTTP(adminI18NPage, adminI18NReq)
	if adminI18NPage.Code != http.StatusOK {
		t.Fatalf("platform admin i18n status = %d, body = %s", adminI18NPage.Code, adminI18NPage.Body.String())
	}
	if body := adminI18NPage.Body.String(); !strings.Contains(body, `action="/admin/admin-i18n"`) || !strings.Contains(body, `Legacy Posts`) || strings.Contains(body, `href="/admin/posts"`) {
		t.Fatalf("platform admin i18n page did not render migrated platform overrides")
	}
	saveAdminI18N := postPlatformForm("/admin/settings/admin-i18n", url.Values{
		"admin_lang":      {"en"},
		"admin_i18n_json": {`{"admin.nav.posts":"Platform Posts"}`},
	})
	if saveAdminI18N.Code != http.StatusSeeOther || saveAdminI18N.Header().Get("Location") != "/admin/admin-i18n" {
		t.Fatalf("save admin i18n status/location = %d %q", saveAdminI18N.Code, saveAdminI18N.Header().Get("Location"))
	}
	if got := ps.Setting("admin_i18n::en"); !strings.Contains(got, "Platform Posts") {
		t.Fatalf("platform admin i18n saved to %q", got)
	}

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

	defaultBoundResp := httptest.NewRecorder()
	h.ServeHTTP(defaultBoundResp, httptest.NewRequest(http.MethodGet, "https://default-bound.test/zh/", nil))
	if defaultBoundResp.Code != http.StatusNotFound {
		t.Fatalf("stored default-site domain status = %d, want 404", defaultBoundResp.Code)
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
	previewPrefix := "/admin/sites/" + strconv.FormatInt(otherSite.ID, 10) + "/preview"
	if body := preview.Body.String(); !strings.Contains(body, "Blog Runtime Site") || !strings.Contains(body, `<meta name="robots" content="noindex, nofollow">`) {
		t.Fatalf("preview did not render noindex blog page")
	} else {
		for _, needle := range []string{
			`href="` + previewPrefix + `/zh/posts/preview-internal-link"`,
			`href="` + previewPrefix + `/zh/category"`,
			`href="` + previewPrefix + `/zh/about"`,
			`href="` + previewPrefix + `/sitemap.xml"`,
			`href="` + previewPrefix + `/robots.txt"`,
		} {
			if !strings.Contains(body, needle) {
				t.Fatalf("preview did not keep internal link %q under preview prefix: %s", needle, body)
			}
		}
		for _, needle := range []string{
			`href="/zh/posts/preview-internal-link"`,
			`href="/zh/category"`,
			`href="/zh/about"`,
		} {
			if strings.Contains(body, needle) {
				t.Fatalf("preview rendered root-relative frontend link %q: %s", needle, body)
			}
		}
	}

	previewArticle := httptest.NewRecorder()
	previewArticleReq := httptest.NewRequest(http.MethodGet, "https://platform.test"+previewPrefix+"/zh/posts/preview-internal-link", nil)
	previewArticleReq.AddCookie(&http.Cookie{Name: cookieName, Value: "preview-token"})
	h.ServeHTTP(previewArticle, previewArticleReq)
	if previewArticle.Code != http.StatusOK {
		t.Fatalf("preview article status = %d, body = %s", previewArticle.Code, previewArticle.Body.String())
	}
	if body := previewArticle.Body.String(); !strings.Contains(body, "Preview Internal Link") {
		t.Fatalf("preview article did not render expected post: %s", body)
	} else {
		for _, needle := range []string{
			`href="` + previewPrefix + `/zh/"`,
			`href="` + previewPrefix + `/zh/category"`,
		} {
			if !strings.Contains(body, needle) {
				t.Fatalf("preview article did not keep internal link %q under preview prefix: %s", needle, body)
			}
		}
		if strings.Contains(body, `href="/zh/category"`) {
			t.Fatalf("preview article rendered root-relative category link: %s", body)
		}
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

	saveSiteName := httptest.NewRecorder()
	saveSiteNameForm := url.Values{
		"_csrf":     {prefixSess.CSRF},
		"lang":      {"zh"},
		"site_name": {"Renamed Blog Site"},
	}
	saveSiteNameReq := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/settings/site", strings.NewReader(saveSiteNameForm.Encode()))
	saveSiteNameReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	saveSiteNameReq.AddCookie(&http.Cookie{Name: cookieName, Value: "prefix-token"})
	h.ServeHTTP(saveSiteName, saveSiteNameReq)
	if saveSiteName.Code != http.StatusSeeOther {
		t.Fatalf("save site name status = %d, body = %s", saveSiteName.Code, saveSiteName.Body.String())
	}
	renamedSite, ok, err := ps.GetSite(otherSite.ID)
	if err != nil || !ok {
		t.Fatalf("get renamed platform site: ok=%v err=%v", ok, err)
	}
	if renamedSite.Name != "Renamed Blog Site" {
		t.Fatalf("platform site name = %q, want Renamed Blog Site", renamedSite.Name)
	}
	renamedPlatformPage := httptest.NewRecorder()
	renamedPlatformReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites", nil)
	renamedPlatformReq.AddCookie(&http.Cookie{Name: cookieName, Value: "prefix-token"})
	h.ServeHTTP(renamedPlatformPage, renamedPlatformReq)
	if renamedPlatformPage.Code != http.StatusOK {
		t.Fatalf("renamed platform page status = %d, body = %s", renamedPlatformPage.Code, renamedPlatformPage.Body.String())
	}
	if body := renamedPlatformPage.Body.String(); !strings.Contains(body, "Renamed Blog Site") || !strings.Contains(body, `/admin/sites/`+strconv.FormatInt(otherSite.ID, 10)+`/preview/zh/`) || strings.Contains(body, `/admin/sites/`+strconv.FormatInt(otherSite.ID, 10)+`/preview"`) {
		t.Fatalf("platform page did not render renamed site: %s", body)
	}

	siteAdmin := httptest.NewRecorder()
	siteAdminReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin", nil)
	siteAdminReq.AddCookie(&http.Cookie{Name: cookieName, Value: "prefix-token"})
	h.ServeHTTP(siteAdmin, siteAdminReq)
	if siteAdmin.Code != http.StatusOK {
		t.Fatalf("site admin status = %d, body = %s", siteAdmin.Code, siteAdmin.Body.String())
	}
	siteAdminBody := siteAdmin.Body.String()
	switcherStart := strings.Index(siteAdminBody, `<details class="site-switcher">`)
	switcherEnd := -1
	if switcherStart >= 0 {
		switcherEnd = strings.Index(siteAdminBody[switcherStart:], `</details>`)
	}
	if switcherStart < 0 || switcherEnd < 0 {
		t.Fatalf("site admin did not render site switcher")
	}
	switcherBody := siteAdminBody[switcherStart : switcherStart+switcherEnd]
	if !strings.Contains(switcherBody, `aria-label="切换站点"`) || !strings.Contains(switcherBody, `href="/admin/sites"`) || !strings.Contains(switcherBody, `class="site-switcher-icon"`) || !strings.Contains(switcherBody, defaultIconPath) || !strings.Contains(switcherBody, "Default Runtime Site / main") || !strings.Contains(switcherBody, "/admin/sites/"+strconv.FormatInt(defaultSite.ID, 10)+"/enter") {
		t.Fatalf("site switcher did not render management and other-site entries")
	}
	if strings.Contains(switcherBody, "/admin/sites/"+strconv.FormatInt(otherSite.ID, 10)+"/enter") || strings.Contains(switcherBody, `class="active"`) {
		t.Fatalf("site switcher rendered the current site as a switch target: %s", switcherBody)
	}
	if strings.Contains(siteAdminBody, `id="pw-warn"`) {
		t.Fatalf("site admin should not render platform password warning")
	}
	otherPreviewPath := "/admin/sites/" + strconv.FormatInt(otherSite.ID, 10) + "/preview/zh/"
	if !strings.Contains(siteAdminBody, `href="`+otherPreviewPath+`"`) {
		t.Fatalf("site admin view-site link did not point at current site preview")
	}

	scopedUpload := httptest.NewRecorder()
	scopedUploadReq := httptest.NewRequest(http.MethodGet, "https://platform.test/uploads/blog-icon.ico", nil)
	scopedUploadReq.AddCookie(&http.Cookie{Name: cookieName, Value: "prefix-token"})
	scopedUploadReq.Header.Set("Referer", "https://platform.test/admin/settings/site")
	h.ServeHTTP(scopedUpload, scopedUploadReq)
	if scopedUpload.Code != http.StatusOK {
		t.Fatalf("scoped upload status = %d, body = %s", scopedUpload.Code, scopedUpload.Body.String())
	}

	automationPage := httptest.NewRecorder()
	automationReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/settings/automation", nil)
	automationReq.AddCookie(&http.Cookie{Name: cookieName, Value: "prefix-token"})
	h.ServeHTTP(automationPage, automationReq)
	if automationPage.Code != http.StatusOK {
		t.Fatalf("automation settings status = %d, body = %s", automationPage.Code, automationPage.Body.String())
	}
	otherAPIBase := "https://platform.test/api/platform/v1/sites/" + strconv.FormatInt(otherSite.ID, 10)
	if body := automationPage.Body.String(); !strings.Contains(body, otherAPIBase) || !strings.Contains(body, otherAPIBase+"/openapi.json") || strings.Contains(body, "https://blog.test/api/admin/v1") {
		t.Fatalf("automation settings did not render platform site API base")
	}

	kit := httptest.NewRecorder()
	kitReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/settings/automation/skill.zip", nil)
	kitReq.AddCookie(&http.Cookie{Name: cookieName, Value: "prefix-token"})
	h.ServeHTTP(kit, kitReq)
	if kit.Code != http.StatusOK {
		t.Fatalf("automation skill zip status = %d, body = %s", kit.Code, kit.Body.String())
	}
	zr, err := zip.NewReader(bytes.NewReader(kit.Body.Bytes()), int64(kit.Body.Len()))
	if err != nil {
		t.Fatalf("read automation skill zip: %v", err)
	}
	var envExample string
	for _, f := range zr.File {
		if f.Name != "gcms-content-assistant/.env.example" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open env example: %v", err)
		}
		buf := new(bytes.Buffer)
		if _, err := buf.ReadFrom(rc); err != nil {
			_ = rc.Close()
			t.Fatalf("read env example: %v", err)
		}
		_ = rc.Close()
		envExample = buf.String()
		break
	}
	if !strings.Contains(envExample, "GCMS_API_BASE="+otherAPIBase) {
		t.Fatalf("automation skill env api base = %q, want %q", envExample, otherAPIBase)
	}

	otherAPICreate := httptest.NewRecorder()
	otherAPIBody, err := json.Marshal(map[string]any{
		"title":  "API Other Site Draft",
		"lang":   "zh",
		"status": "draft",
	})
	if err != nil {
		t.Fatalf("marshal other api post: %v", err)
	}
	otherAPIReq := httptest.NewRequest(http.MethodPost, "https://platform.test/api/platform/v1/sites/"+strconv.FormatInt(otherSite.ID, 10)+"/posts", bytes.NewReader(otherAPIBody))
	otherAPIReq.Header.Set("Authorization", "Bearer "+otherToken)
	otherAPIReq.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(otherAPICreate, otherAPIReq)
	if otherAPICreate.Code != http.StatusCreated {
		t.Fatalf("other api create status = %d, body = %s", otherAPICreate.Code, otherAPICreate.Body.String())
	}
	otherPosts := httptest.NewRecorder()
	otherPostsReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/posts?lang=zh&status=draft", nil)
	otherPostsReq.AddCookie(&http.Cookie{Name: cookieName, Value: "prefix-token"})
	h.ServeHTTP(otherPosts, otherPostsReq)
	if otherPosts.Code != http.StatusOK {
		t.Fatalf("other posts status = %d, body = %s", otherPosts.Code, otherPosts.Body.String())
	}
	if body := otherPosts.Body.String(); !strings.Contains(body, "API Other Site Draft") {
		t.Fatalf("platform api-created draft was not visible in other-site admin posts")
	}

	visual := httptest.NewRecorder()
	visualReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/visual", nil)
	visualReq.AddCookie(&http.Cookie{Name: cookieName, Value: "prefix-token"})
	h.ServeHTTP(visual, visualReq)
	if visual.Code != http.StatusOK {
		t.Fatalf("visual status = %d, body = %s", visual.Code, visual.Body.String())
	}
	if body := visual.Body.String(); !strings.Contains(body, `href="`+otherPreviewPath+`"`) || !strings.Contains(body, `src="`+otherPreviewPath+`?visual_edit=1"`) {
		t.Fatalf("visual editor did not point at current site preview")
	}

	if err := ps.CreateAdminSession("default-current-token", "admin", "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create default current session: %v", err)
	}
	if err := ps.SetAdminSessionSite("default-current-token", defaultSite.ID); err != nil {
		t.Fatalf("set default current site: %v", err)
	}
	defaultSiteAdmin := httptest.NewRecorder()
	defaultSiteAdminReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin", nil)
	defaultSiteAdminReq.AddCookie(&http.Cookie{Name: cookieName, Value: "default-current-token"})
	h.ServeHTTP(defaultSiteAdmin, defaultSiteAdminReq)
	if defaultSiteAdmin.Code != http.StatusOK {
		t.Fatalf("default site admin status = %d, body = %s", defaultSiteAdmin.Code, defaultSiteAdmin.Body.String())
	}
	defaultPreviewPath := "/admin/sites/" + strconv.FormatInt(defaultSite.ID, 10) + "/preview/zh/"
	if body := defaultSiteAdmin.Body.String(); !strings.Contains(body, `href="`+defaultPreviewPath+`"`) || strings.Contains(body, `href="/zh/"`) {
		t.Fatalf("default site admin did not point view-site at source preview")
	}
	defaultVisual := httptest.NewRecorder()
	defaultVisualReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/visual", nil)
	defaultVisualReq.AddCookie(&http.Cookie{Name: cookieName, Value: "default-current-token"})
	h.ServeHTTP(defaultVisual, defaultVisualReq)
	if defaultVisual.Code != http.StatusOK {
		t.Fatalf("default visual status = %d, body = %s", defaultVisual.Code, defaultVisual.Body.String())
	}
	if body := defaultVisual.Body.String(); !strings.Contains(body, `href="`+defaultPreviewPath+`"`) || !strings.Contains(body, `src="`+defaultPreviewPath+`?visual_edit=1"`) || strings.Contains(body, `src="/zh/?visual_edit=1"`) {
		t.Fatalf("default visual editor did not point at source preview")
	}

	apiCreate := httptest.NewRecorder()
	apiBody, err := json.Marshal(map[string]any{
		"title":  "API Default Site Draft",
		"lang":   "zh",
		"status": "draft",
	})
	if err != nil {
		t.Fatalf("marshal default api post: %v", err)
	}
	apiReq := httptest.NewRequest(http.MethodPost, "https://platform.test/api/admin/v1/posts", bytes.NewReader(apiBody))
	apiReq.Header.Set("Authorization", "Bearer "+defaultToken)
	apiReq.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(apiCreate, apiReq)
	if apiCreate.Code != http.StatusCreated {
		t.Fatalf("default api create status = %d, body = %s", apiCreate.Code, apiCreate.Body.String())
	}
	defaultPosts := httptest.NewRecorder()
	defaultPostsReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/posts?lang=zh&status=draft", nil)
	defaultPostsReq.AddCookie(&http.Cookie{Name: cookieName, Value: "default-current-token"})
	h.ServeHTTP(defaultPosts, defaultPostsReq)
	if defaultPosts.Code != http.StatusOK {
		t.Fatalf("default posts status = %d, body = %s", defaultPosts.Code, defaultPosts.Body.String())
	}
	if body := defaultPosts.Body.String(); !strings.Contains(body, "API Default Site Draft") {
		t.Fatalf("default api-created draft was not visible in admin posts/logs")
	}
	defaultDashboard := httptest.NewRecorder()
	defaultDashboardReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin", nil)
	defaultDashboardReq.AddCookie(&http.Cookie{Name: cookieName, Value: "default-current-token"})
	h.ServeHTTP(defaultDashboard, defaultDashboardReq)
	if defaultDashboard.Code != http.StatusOK {
		t.Fatalf("default dashboard status = %d, body = %s", defaultDashboard.Code, defaultDashboard.Body.String())
	}
	if body := defaultDashboard.Body.String(); !strings.Contains(body, `class="overview-log-action">创建文章（草稿 · 中文）`) || !strings.Contains(body, `class="overview-log-text">API Default Site Draft`) || !strings.Contains(body, "/admin/posts/") {
		t.Fatalf("default dashboard did not render api log with target link")
	}
}
