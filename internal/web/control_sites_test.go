package web

import (
	"bytes"
	"encoding/json"
	"fmt"
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

const controlSitesPassword = "Control-Sites-Password-2026!"

type controlSitesFixture struct {
	server      *Server
	platform    *platform.Store
	dataDir     string
	adminHash   string
	defaultSite *platform.Site
	memberSite  *platform.Site
	otherSite   *platform.Site
}

func setupControlSitesFixture(t *testing.T) controlSitesFixture {
	t.Helper()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	defaultDB := filepath.Join(dataDir, "cms.db")
	defaultStore, err := store.Open(defaultDB)
	if err != nil {
		t.Fatalf("open default store: %v", err)
	}
	t.Cleanup(func() { _ = defaultStore.Close() })
	if err := defaultStore.SetSetting("site.name", "Default Site"); err != nil {
		t.Fatalf("set default name: %v", err)
	}
	defaultUploads := filepath.Join(dataDir, "uploads")
	if err := os.MkdirAll(defaultUploads, 0o755); err != nil {
		t.Fatalf("mkdir default uploads: %v", err)
	}

	ps, err := platform.Open(filepath.Join(dataDir, "system.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	hash, err := bcrypt.GenerateFromPassword([]byte(controlSitesPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := ps.BootstrapDefaultSite(platform.DefaultSiteBootstrap{
		Slug:                        "main",
		Name:                        "Default Site",
		DBPath:                      defaultDB,
		UploadDir:                   defaultUploads,
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

	createExistingSite := func(slug, name string) *platform.Site {
		t.Helper()
		root := filepath.Join(dataDir, "sites", slug)
		uploads := filepath.Join(root, "uploads")
		if err := os.MkdirAll(uploads, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", slug, err)
		}
		st, err := store.Open(filepath.Join(root, "cms.db"))
		if err != nil {
			t.Fatalf("open %s store: %v", slug, err)
		}
		if err := st.SetSetting("site.name", name); err != nil {
			_ = st.Close()
			t.Fatalf("set %s name: %v", slug, err)
		}
		if err := st.Close(); err != nil {
			t.Fatalf("close %s store: %v", slug, err)
		}
		site, err := ps.CreateSite(slug, name, filepath.Join(root, "cms.db"), uploads, true)
		if err != nil {
			t.Fatalf("create %s platform site: %v", slug, err)
		}
		return site
	}
	memberSite := createExistingSite("member", "Member Site")
	otherSite := createExistingSite("other", "Other Site")

	srv, err := NewWithPlatform(defaultStore, ps, "https://platform.test", defaultUploads, os.DirFS("../.."), os.DirFS("../.."))
	if err != nil {
		t.Fatalf("new platform server: %v", err)
	}
	return controlSitesFixture{
		server:      srv,
		platform:    ps,
		dataDir:     dataDir,
		adminHash:   string(hash),
		defaultSite: defaultSite,
		memberSite:  memberSite,
		otherSite:   otherSite,
	}
}

func createControlSitesKey(t *testing.T, fixture controlSitesFixture, suffix, membership, scopes string, siteIDs []int64) (string, int64) {
	t.Helper()
	token := "gcmsp_controlsites_" + suffix
	id, err := fixture.platform.CreatePlatformKey("control sites "+suffix, token, token[:13], membership, scopes, siteIDs, time.Time{})
	if err != nil {
		t.Fatalf("create platform key: %v", err)
	}
	return token, id
}

func controlSitesRequest(t *testing.T, srv *Server, method, path, token string, body []byte, operation, idempotencyKey, unlock string, siteID int64) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, "https://platform.test"+path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if operation != "" {
		req.Header.Set(controlConfirmHeader, operation)
	}
	if idempotencyKey != "" {
		req.Header.Set(controlIdempotencyHeader, idempotencyKey)
	}
	if unlock != "" {
		req.Header.Set(controlUnlockHeader, unlock)
	}
	rec := httptest.NewRecorder()
	if siteID > 0 {
		srv.servePlatformControlSite(rec, req, siteID)
	} else {
		srv.servePlatformControlSites(rec, req)
	}
	return rec
}

func findControlSiteBySlug(t *testing.T, ps *platform.Store, slug string) *platform.Site {
	t.Helper()
	sites, err := ps.Sites()
	if err != nil {
		t.Fatalf("list sites: %v", err)
	}
	for _, site := range sites {
		if site != nil && site.Slug == slug {
			return site
		}
	}
	return nil
}

func TestControlSitesDryRunValidationAndSafeList(t *testing.T) {
	fixture := setupControlSitesFixture(t)
	token, _ := createControlSitesKey(t, fixture, "dryrun", platform.KeyMembershipAll,
		strings.Join([]string{apiScopeControlRead, apiScopeSitesCreate}, ","), nil)

	list := controlSitesRequest(t, fixture.server, http.MethodGet, "/api/platform/v1/control/sites", token, nil, "", "", "", 0)
	if list.Code != http.StatusOK {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	if body := list.Body.String(); strings.Contains(body, "db_path") || strings.Contains(body, "upload_dir") || !strings.Contains(body, `"total":3`) {
		t.Fatalf("unsafe or incomplete list response: %s", body)
	}

	body := []byte(`{"slug":"dry-site","name":" Dry Site ","site_kind":"factory","management_automation_enabled":true}`)
	dryRun := controlSitesRequest(t, fixture.server, http.MethodPost, "/api/platform/v1/control/sites?dry_run=1", token, body, "", "", "", 0)
	if dryRun.Code != http.StatusOK {
		t.Fatalf("dry run = %d %s", dryRun.Code, dryRun.Body.String())
	}
	if response := dryRun.Body.String(); !strings.Contains(response, `"normalized_input"`) || !strings.Contains(response, `"impact"`) || !strings.Contains(response, `"warnings"`) || !strings.Contains(response, `"name":"Dry Site"`) || !strings.Contains(response, `"seed_mode":"empty"`) {
		t.Fatalf("dry run contract = %s", response)
	}
	if site := findControlSiteBySlug(t, fixture.platform, "dry-site"); site != nil {
		t.Fatalf("dry run created site: %#v", site)
	}
	if _, err := os.Stat(filepath.Join(fixture.dataDir, "sites", "dry-site")); !os.IsNotExist(err) {
		t.Fatalf("dry run created storage, err=%v", err)
	}

	badSlug := controlSitesRequest(t, fixture.server, http.MethodPost, "/api/platform/v1/control/sites?dry_run=1", token,
		[]byte(`{"slug":"Bad Site","seed_mode":"empty"}`), "", "", "", 0)
	if badSlug.Code != http.StatusBadRequest || !strings.Contains(badSlug.Body.String(), "invalid_slug") {
		t.Fatalf("bad slug = %d %s", badSlug.Code, badSlug.Body.String())
	}
	duplicate := controlSitesRequest(t, fixture.server, http.MethodPost, "/api/platform/v1/control/sites?dry_run=1", token,
		[]byte(`{"slug":"member","seed_mode":"demo"}`), "", "", "", 0)
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), "site_slug_conflict") {
		t.Fatalf("duplicate = %d %s", duplicate.Code, duplicate.Body.String())
	}
}

func TestControlSitesCreateEmptyAndReplay(t *testing.T) {
	fixture := setupControlSitesFixture(t)
	token, _ := createControlSitesKey(t, fixture, "create", platform.KeyMembershipAll,
		strings.Join([]string{apiScopeControlRead, apiScopeSitesCreate}, ","), nil)
	// 未传 seed_mode 必须安全地创建空站；未传自动化开关时默认开启，
	// 这样 Pilot 创建后可以继续建设，无需用户回后台补开权限。
	body := []byte(`{"slug":"empty-site","name":"Empty Site","site_kind":"factory"}`)
	create := controlSitesRequest(t, fixture.server, http.MethodPost, "/api/platform/v1/control/sites", token, body,
		"sites.create", "create-empty-site", "", 0)
	if create.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", create.Code, create.Body.String())
	}
	if response := create.Body.String(); strings.Contains(response, "db_path") || strings.Contains(response, "upload_dir") || !strings.Contains(response, `"site_kind":"factory"`) {
		t.Fatalf("create response unsafe/incomplete: %s", response)
	}

	created := findControlSiteBySlug(t, fixture.platform, "empty-site")
	if created == nil {
		t.Fatal("created site not found")
	}
	if !created.ManagementAutomationEnabled {
		t.Fatal("Pilot-created site did not default management automation to enabled")
	}
	if created.DBPath != filepath.Join(fixture.dataDir, "sites", "empty-site", "cms.db") {
		t.Fatalf("created db path = %q", created.DBPath)
	}
	st, err := store.Open(created.DBPath)
	if err != nil {
		t.Fatalf("open created store: %v", err)
	}
	if kind := siteKindOf(st); kind != siteKindFactory {
		_ = st.Close()
		t.Fatalf("site kind = %q", kind)
	}
	recent, err := st.ListRecentAdminContent("zh", 50)
	if closeErr := st.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("inspect empty store: %v", err)
	}
	if len(recent) != 1 || recent[0].Type != "page" || recent[0].Slug != "about" {
		t.Fatalf("empty seed content = %#v", recent)
	}

	replay := controlSitesRequest(t, fixture.server, http.MethodPost, "/api/platform/v1/control/sites", token, body,
		"sites.create", "create-empty-site", "", 0)
	if replay.Code != http.StatusCreated || replay.Header().Get(controlIdempotencyReplayedHeader) != "true" {
		t.Fatalf("replay = %d headers=%v body=%s", replay.Code, replay.Header(), replay.Body.String())
	}
	sites, err := fixture.platform.Sites()
	if err != nil {
		t.Fatalf("list after replay: %v", err)
	}
	count := 0
	for _, site := range sites {
		if site.Slug == "empty-site" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("idempotent create count = %d", count)
	}
}

func TestControlSitesUpdateAndDefaultProtection(t *testing.T) {
	fixture := setupControlSitesFixture(t)
	token, _ := createControlSitesKey(t, fixture, "update", platform.KeyMembershipAll,
		strings.Join([]string{apiScopeControlRead, apiScopeSitesUpdate}, ","), nil)

	dryRun := controlSitesRequest(t, fixture.server, http.MethodPatch,
		"/api/platform/v1/control/sites/"+strconv.FormatInt(fixture.memberSite.ID, 10)+"?dry_run=1", token,
		[]byte(`{"name":"Preview Name","status":"disabled","management_automation_enabled":false}`), "", "", "", fixture.memberSite.ID)
	if dryRun.Code != http.StatusOK || !strings.Contains(dryRun.Body.String(), `"changes"`) {
		t.Fatalf("patch dry run = %d %s", dryRun.Code, dryRun.Body.String())
	}
	unchanged, _, _ := fixture.platform.GetSite(fixture.memberSite.ID)
	if unchanged.Name != "Member Site" || unchanged.Status != "enabled" || !unchanged.ManagementAutomationEnabled {
		t.Fatalf("patch dry run changed site: %#v", unchanged)
	}

	patchBody := []byte(`{"name":"Renamed Member","status":"disabled","management_automation_enabled":false}`)
	patch := controlSitesRequest(t, fixture.server, http.MethodPatch,
		"/api/platform/v1/control/sites/"+strconv.FormatInt(fixture.memberSite.ID, 10), token, patchBody,
		"sites.update", "update-member", "", fixture.memberSite.ID)
	if patch.Code != http.StatusOK {
		t.Fatalf("patch = %d %s", patch.Code, patch.Body.String())
	}
	updated, found, err := fixture.platform.GetSite(fixture.memberSite.ID)
	if err != nil || !found || updated.Name != "Renamed Member" || updated.Status != "disabled" || updated.ManagementAutomationEnabled {
		t.Fatalf("updated site = %#v found=%v err=%v", updated, found, err)
	}
	st, err := store.Open(updated.DBPath)
	if err != nil {
		t.Fatalf("open updated site: %v", err)
	}
	if name := st.Setting("site.name"); name != "Renamed Member" {
		_ = st.Close()
		t.Fatalf("site.name = %q", name)
	}
	_ = st.Close()

	protect := controlSitesRequest(t, fixture.server, http.MethodPatch,
		"/api/platform/v1/control/sites/"+strconv.FormatInt(fixture.defaultSite.ID, 10), token,
		[]byte(`{"status":"disabled"}`), "sites.update", "disable-default", "", fixture.defaultSite.ID)
	if protect.Code != http.StatusConflict || !strings.Contains(protect.Body.String(), "default_site_protected") {
		t.Fatalf("default protection = %d %s", protect.Code, protect.Body.String())
	}
}

func TestControlSitesDeleteRequiresUnlockAndArchives(t *testing.T) {
	fixture := setupControlSitesFixture(t)
	token, keyID := createControlSitesKey(t, fixture, "delete", platform.KeyMembershipAll,
		strings.Join([]string{apiScopeControlRead, apiScopeControlUnlock, apiScopeSitesDelete}, ","), nil)
	path := "/api/platform/v1/control/sites/" + strconv.FormatInt(fixture.memberSite.ID, 10)

	withoutUnlock := controlSitesRequest(t, fixture.server, http.MethodDelete, path, token, nil,
		"sites.delete", "delete-member", "", fixture.memberSite.ID)
	if withoutUnlock.Code != http.StatusForbidden || !strings.Contains(withoutUnlock.Body.String(), "unlock_required") {
		t.Fatalf("delete without unlock = %d %s", withoutUnlock.Code, withoutUnlock.Body.String())
	}
	unlock, _, err := fixture.server.controlGrants.issue(keyID, []string{"sites.delete"}, controlCredentialRevision(fixture.adminHash), time.Now())
	if err != nil {
		t.Fatalf("issue unlock: %v", err)
	}
	enabled := controlSitesRequest(t, fixture.server, http.MethodDelete, path, token, nil,
		"sites.delete", "delete-member", unlock, fixture.memberSite.ID)
	if enabled.Code != http.StatusConflict || !strings.Contains(enabled.Body.String(), "site_must_be_disabled") {
		t.Fatalf("delete enabled = %d %s", enabled.Code, enabled.Body.String())
	}
	if err := fixture.platform.SetSiteStatus(fixture.memberSite.ID, "disabled"); err != nil {
		t.Fatalf("disable member: %v", err)
	}

	dryRun := controlSitesRequest(t, fixture.server, http.MethodDelete, path+"?dry_run=1", token, nil,
		"", "", "", fixture.memberSite.ID)
	if dryRun.Code != http.StatusOK || !strings.Contains(dryRun.Body.String(), `"recoverable":true`) {
		t.Fatalf("delete dry run = %d %s", dryRun.Code, dryRun.Body.String())
	}
	if _, found, _ := fixture.platform.GetSite(fixture.memberSite.ID); !found {
		t.Fatal("delete dry run removed site")
	}

	deleted := controlSitesRequest(t, fixture.server, http.MethodDelete, path, token, nil,
		"sites.delete", "delete-member", unlock, fixture.memberSite.ID)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", deleted.Code, deleted.Body.String())
	}
	if response := deleted.Body.String(); strings.Contains(response, "archive_path") || strings.Contains(response, "db_path") || !strings.Contains(response, `"recoverable":true`) {
		t.Fatalf("delete response unsafe/incomplete: %s", response)
	}
	if _, found, err := fixture.platform.GetSite(fixture.memberSite.ID); err != nil || found {
		t.Fatalf("deleted site still active: found=%v err=%v", found, err)
	}
	archived, err := fixture.platform.ArchivedSites()
	if err != nil {
		t.Fatalf("list archives: %v", err)
	}
	if len(archived) != 1 || archived[0].OriginalSiteID != fixture.memberSite.ID {
		t.Fatalf("archive records = %#v", archived)
	}
	if _, err := os.Stat(filepath.Join(archived[0].ArchivePath, "cms.db")); err != nil {
		t.Fatalf("archived cms.db: %v", err)
	}
	if _, err := os.Stat(filepath.Join(archived[0].ArchivePath, "archive.json")); err != nil {
		t.Fatalf("archive manifest: %v", err)
	}

	replay := controlSitesRequest(t, fixture.server, http.MethodDelete, path, token, nil,
		"sites.delete", "delete-member", unlock, fixture.memberSite.ID)
	if replay.Code != http.StatusOK || replay.Header().Get(controlIdempotencyReplayedHeader) != "true" {
		t.Fatalf("delete replay = %d headers=%v body=%s", replay.Code, replay.Header(), replay.Body.String())
	}
}

func TestControlSitesAllowlistCannotEscapeMembership(t *testing.T) {
	fixture := setupControlSitesFixture(t)
	token, _ := createControlSitesKey(t, fixture, "allowlist", platform.KeyMembershipAllowlist,
		strings.Join([]string{apiScopeControlRead, apiScopeSitesCreate, apiScopeSitesUpdate, apiScopeSitesDelete}, ","),
		[]int64{fixture.memberSite.ID})

	if err := fixture.platform.SetSiteStatus(fixture.memberSite.ID, "disabled"); err != nil {
		t.Fatalf("disable member: %v", err)
	}
	if err := fixture.platform.SetSiteAutomation(fixture.memberSite.ID, false); err != nil {
		t.Fatalf("disable member automation: %v", err)
	}
	list := controlSitesRequest(t, fixture.server, http.MethodGet, "/api/platform/v1/control/sites", token, nil, "", "", "", 0)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"slug":"member"`) || strings.Contains(list.Body.String(), `"slug":"main"`) || strings.Contains(list.Body.String(), `"slug":"other"`) {
		t.Fatalf("allowlist list = %d %s", list.Code, list.Body.String())
	}

	for _, target := range []*platform.Site{fixture.defaultSite, fixture.otherSite} {
		get := controlSitesRequest(t, fixture.server, http.MethodGet,
			"/api/platform/v1/control/sites/"+strconv.FormatInt(target.ID, 10), token, nil, "", "", "", target.ID)
		if get.Code != http.StatusForbidden || !strings.Contains(get.Body.String(), "membership_scope") {
			t.Fatalf("allowlist get site %d = %d %s", target.ID, get.Code, get.Body.String())
		}
		patch := controlSitesRequest(t, fixture.server, http.MethodPatch,
			"/api/platform/v1/control/sites/"+strconv.FormatInt(target.ID, 10), token, []byte(`{"status":"disabled"}`),
			"sites.update", fmt.Sprintf("patch-forbidden-%d", target.ID), "", target.ID)
		if patch.Code != http.StatusForbidden || !strings.Contains(patch.Body.String(), "membership_scope") {
			t.Fatalf("allowlist patch site %d = %d %s", target.ID, patch.Code, patch.Body.String())
		}
	}

	create := controlSitesRequest(t, fixture.server, http.MethodPost, "/api/platform/v1/control/sites?dry_run=1", token,
		[]byte(`{"slug":"forbidden-create","seed_mode":"empty"}`), "", "", "", 0)
	if create.Code != http.StatusForbidden || !strings.Contains(create.Body.String(), "membership_scope") {
		t.Fatalf("allowlist create = %d %s", create.Code, create.Body.String())
	}

	// 成员站点即使当前 disabled 且 automation-off，仍能通过 CanManageSite 重新开启。
	reenable := controlSitesRequest(t, fixture.server, http.MethodPatch,
		"/api/platform/v1/control/sites/"+strconv.FormatInt(fixture.memberSite.ID, 10), token,
		[]byte(`{"status":"enabled","management_automation_enabled":true}`),
		"sites.update", "reenable-member", "", fixture.memberSite.ID)
	if reenable.Code != http.StatusOK {
		t.Fatalf("reenable member = %d %s", reenable.Code, reenable.Body.String())
	}
	member, found, err := fixture.platform.GetSite(fixture.memberSite.ID)
	if err != nil || !found || member.Status != "enabled" || !member.ManagementAutomationEnabled {
		t.Fatalf("reenabled member = %#v found=%v err=%v", member, found, err)
	}
}

func TestControlSitesResponsesNeverExposeStoragePaths(t *testing.T) {
	fixture := setupControlSitesFixture(t)
	token, _ := createControlSitesKey(t, fixture, "safe", platform.KeyMembershipAll, apiScopeControlRead, nil)
	for _, site := range []*platform.Site{fixture.defaultSite, fixture.memberSite, fixture.otherSite} {
		rec := controlSitesRequest(t, fixture.server, http.MethodGet,
			"/api/platform/v1/control/sites/"+strconv.FormatInt(site.ID, 10), token, nil, "", "", "", site.ID)
		if rec.Code != http.StatusOK {
			t.Fatalf("get site %d = %d %s", site.ID, rec.Code, rec.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode site %d: %v", site.ID, err)
		}
		if body := rec.Body.String(); strings.Contains(body, "db_path") || strings.Contains(body, "upload_dir") || strings.Contains(body, site.DBPath) || strings.Contains(body, site.UploadDir) {
			t.Fatalf("site %d leaked storage path: %s", site.ID, body)
		}
	}
}
