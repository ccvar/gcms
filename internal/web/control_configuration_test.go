package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
	"golang.org/x/crypto/bcrypt"
)

type controlConfigurationRequest struct {
	Confirm        string
	IdempotencyKey string
	UnlockToken    string
	PilotUI        bool
}

func controlConfigurationAPIReq(t *testing.T, h http.Handler, method, path, token string, body []byte, options controlConfigurationRequest) *httptest.ResponseRecorder {
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
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if options.Confirm != "" {
		req.Header.Set(controlConfirmHeader, options.Confirm)
	}
	if options.IdempotencyKey != "" {
		req.Header.Set(controlIdempotencyHeader, options.IdempotencyKey)
	}
	if options.UnlockToken != "" {
		req.Header.Set(controlUnlockHeader, options.UnlockToken)
	}
	if options.PilotUI {
		req.Header.Set(controlUIRequestHeader, controlUIPilotValue)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func createControlConfigurationKey(t *testing.T, ps *platform.Store, token, membership, scopes string, allowed []int64) {
	t.Helper()
	if _, err := ps.CreatePlatformKey("configuration-test", token, token[:13], membership, scopes, allowed, time.Time{}); err != nil {
		t.Fatalf("create platform control key: %v", err)
	}
}

func controlConfigurationUnlock(t *testing.T, h http.Handler, token, password, operation string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"password": password, "operations": []string{operation}})
	rec := controlConfigurationAPIReq(t, h, http.MethodPost, "/api/platform/v1/control/unlock", token, body, controlConfigurationRequest{PilotUI: true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("unlock %s = %d %s", operation, rec.Code, rec.Body.String())
	}
	var out struct {
		Token string `json:"unlock_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Token == "" {
		t.Fatalf("decode unlock: token=%q err=%v body=%s", out.Token, err, rec.Body.String())
	}
	return out.Token
}

func TestPlatformControlThemesListApplyAndRollback(t *testing.T) {
	srv, h, ps, _, blogSite := setupPlatformAutomation(t)
	token := "gcmsp_themecontrol12345"
	createControlConfigurationKey(t, ps, token, platform.KeyMembershipAllowlist,
		strings.Join([]string{apiScopeControlRead, apiScopeThemesRead, apiScopeThemesApply}, ","), []int64{blogSite.ID})

	list := controlConfigurationAPIReq(t, h, http.MethodGet, "/api/platform/v1/control/themes", token, nil, controlConfigurationRequest{})
	if list.Code != http.StatusOK {
		t.Fatalf("theme list = %d %s", list.Code, list.Body.String())
	}
	var catalog struct {
		Items         []controlTheme `json:"items"`
		SelectedTheme string         `json:"selected_theme"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("decode theme list: %v", err)
	}
	if len(catalog.Items) != len(Themes) {
		t.Fatalf("theme items = %d, want %d", len(catalog.Items), len(Themes))
	}
	foundStructured := false
	foundSelected := false
	for _, theme := range catalog.Items {
		if theme.ID == "editorial" {
			foundStructured = theme.Name != "" && theme.Description != "" && theme.Category != "" && theme.Layout != "" && len(theme.Options) > 0
		}
		if theme.ID == catalog.SelectedTheme {
			foundSelected = theme.Selected
		}
	}
	if !foundStructured || !foundSelected || !validTheme(catalog.SelectedTheme) {
		t.Fatalf("theme catalog structure/selection invalid: structured=%v selected=%q selected_flag=%v", foundStructured, catalog.SelectedTheme, foundSelected)
	}
	detail := controlConfigurationAPIReq(t, h, http.MethodGet, "/api/platform/v1/control/themes/magazine", token, nil, controlConfigurationRequest{})
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"id":"magazine"`) {
		t.Fatalf("theme detail = %d %s", detail.Code, detail.Body.String())
	}

	runtime, ok := srv.runtimePool().runtimeByID(blogSite.ID)
	if !ok {
		t.Fatal("blog runtime missing")
	}
	if err := runtime.Store.SetSetting("theme", "editorial"); err != nil {
		t.Fatalf("seed current theme: %v", err)
	}
	body := []byte(`{"theme_id":"magazine"}`)
	dryRun := controlConfigurationAPIReq(t, h, http.MethodPut,
		"/api/platform/v1/control/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/theme?dry_run=1", token, body, controlConfigurationRequest{})
	if dryRun.Code != http.StatusOK || !strings.Contains(dryRun.Body.String(), `"dry_run":true`) {
		t.Fatalf("theme dry run = %d %s", dryRun.Code, dryRun.Body.String())
	}
	if got := controlCurrentTheme(runtime.Store); got != "editorial" {
		t.Fatalf("dry run changed theme to %q", got)
	}

	runtime.server.setCachedEndpoint("control-theme-test", "application/json", []byte(`{}`), time.Hour)
	apply := controlConfigurationAPIReq(t, h, http.MethodPut,
		"/api/platform/v1/control/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/theme", token, body,
		controlConfigurationRequest{Confirm: "themes.apply", IdempotencyKey: "theme-apply-1"})
	if apply.Code != http.StatusOK {
		t.Fatalf("theme apply = %d %s", apply.Code, apply.Body.String())
	}
	if got := controlCurrentTheme(runtime.Store); got != "magazine" {
		t.Fatalf("applied theme = %q, want magazine", got)
	}
	if previous := runtime.Store.Setting(controlPreviousThemeSettingKey); previous != "editorial" {
		t.Fatalf("previous theme = %q, want editorial", previous)
	}
	if _, _, cached := runtime.server.cachedEndpoint("control-theme-test"); cached {
		t.Fatal("theme apply did not clear generated caches")
	}
	applyReplay := controlConfigurationAPIReq(t, h, http.MethodPut,
		"/api/platform/v1/control/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/theme", token, body,
		controlConfigurationRequest{Confirm: "themes.apply", IdempotencyKey: "theme-apply-1"})
	if applyReplay.Code != http.StatusOK || applyReplay.Header().Get(controlIdempotencyReplayedHeader) != "true" {
		t.Fatalf("theme apply replay = %d headers=%v body=%s", applyReplay.Code, applyReplay.Header(), applyReplay.Body.String())
	}

	rollbackBody := []byte(`{"action":"rollback"}`)
	rollback := controlConfigurationAPIReq(t, h, http.MethodPut,
		"/api/platform/v1/control/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/theme", token, rollbackBody,
		controlConfigurationRequest{Confirm: "themes.apply", IdempotencyKey: "theme-rollback-1"})
	if rollback.Code != http.StatusOK || !strings.Contains(rollback.Body.String(), `"rolled_back":true`) {
		t.Fatalf("theme rollback = %d %s", rollback.Code, rollback.Body.String())
	}
	if got := controlCurrentTheme(runtime.Store); got != "editorial" {
		t.Fatalf("rolled-back theme = %q, want editorial", got)
	}
	if previous := runtime.Store.Setting(controlPreviousThemeSettingKey); previous != "" {
		t.Fatalf("rollback marker was not cleared: %q", previous)
	}
	rollbackReplay := controlConfigurationAPIReq(t, h, http.MethodPut,
		"/api/platform/v1/control/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/theme", token, rollbackBody,
		controlConfigurationRequest{Confirm: "themes.apply", IdempotencyKey: "theme-rollback-1"})
	if rollbackReplay.Code != http.StatusOK || rollbackReplay.Header().Get(controlIdempotencyReplayedHeader) != "true" {
		t.Fatalf("theme rollback replay = %d headers=%v body=%s", rollbackReplay.Code, rollbackReplay.Header(), rollbackReplay.Body.String())
	}
}

func TestPlatformControlConfigurationHonorsMembershipForDisabledSites(t *testing.T) {
	_, h, ps, defaultSite, blogSite := setupPlatformAutomation(t)
	token := "gcmsp_configallowlist1"
	createControlConfigurationKey(t, ps, token, platform.KeyMembershipAllowlist,
		strings.Join([]string{apiScopeThemesRead, apiScopeDomainsRead}, ","), []int64{blogSite.ID})

	for _, path := range []string{
		"/api/platform/v1/control/sites/" + strconv.FormatInt(defaultSite.ID, 10) + "/theme",
		"/api/platform/v1/control/sites/" + strconv.FormatInt(defaultSite.ID, 10) + "/domains",
	} {
		rec := controlConfigurationAPIReq(t, h, http.MethodGet, path, token, nil, controlConfigurationRequest{})
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "site_forbidden") {
			t.Fatalf("allowlist escape %s = %d %s", path, rec.Code, rec.Body.String())
		}
	}

	if err := ps.SetSiteStatus(blogSite.ID, "disabled"); err != nil {
		t.Fatalf("disable blog: %v", err)
	}
	allowed := controlConfigurationAPIReq(t, h, http.MethodGet,
		"/api/platform/v1/control/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/theme", token, nil, controlConfigurationRequest{})
	if allowed.Code != http.StatusOK || !strings.Contains(allowed.Body.String(), `"site_status":"disabled"`) {
		t.Fatalf("disabled allowlisted site = %d %s", allowed.Code, allowed.Body.String())
	}
}

func TestPlatformControlDomainsDryRunConflictAndPersist(t *testing.T) {
	_, h, ps, defaultSite, blogSite := setupPlatformAutomation(t)
	setPlatformTestPassword(t, ps, controlTestPassword)
	token := "gcmsp_domaincontrol1234"
	createControlConfigurationKey(t, ps, token, platform.KeyMembershipAll,
		strings.Join([]string{apiScopeControlRead, apiScopeControlUnlock, apiScopeDomainsRead, apiScopeDomainsWrite}, ","), nil)

	body := []byte(`{"primary_domain":"new.example.test","redirect_domains":["www.new.example.test"]}`)
	dryRun := controlConfigurationAPIReq(t, h, http.MethodPut,
		"/api/platform/v1/control/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/domains?dry_run=1", token, body, controlConfigurationRequest{})
	if dryRun.Code != http.StatusOK {
		t.Fatalf("domains dry run = %d %s", dryRun.Code, dryRun.Body.String())
	}
	for _, needle := range []string{`"normalized_input"`, `"external_requirements"`, `"owner":"pilot"`, `"performed_by_gcms":false`} {
		if !strings.Contains(dryRun.Body.String(), needle) {
			t.Fatalf("domains dry run missing %s: %s", needle, dryRun.Body.String())
		}
	}
	domains, err := ps.SiteDomains()
	if err != nil {
		t.Fatalf("list domains after dry run: %v", err)
	}
	for _, domain := range domains {
		if domain.SiteID == blogSite.ID && domain.Host == "new.example.test" {
			t.Fatal("dry run persisted domain")
		}
	}

	unlock := controlConfigurationUnlock(t, h, token, controlTestPassword, "domains.apply")
	apply := controlConfigurationAPIReq(t, h, http.MethodPut,
		"/api/platform/v1/control/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/domains", token, body,
		controlConfigurationRequest{Confirm: "domains.apply", IdempotencyKey: "domain-apply-1", UnlockToken: unlock})
	if apply.Code != http.StatusOK {
		t.Fatalf("domains apply = %d %s", apply.Code, apply.Body.String())
	}
	domains, _ = ps.SiteDomains()
	var primary, redirect bool
	for _, domain := range domains {
		if domain.SiteID != blogSite.ID {
			continue
		}
		primary = primary || (domain.Host == "new.example.test" && domain.IsPrimary)
		redirect = redirect || (domain.Host == "www.new.example.test" && domain.RedirectToPrimary)
	}
	if !primary || !redirect {
		t.Fatalf("persisted domains missing primary/redirect: %#v", domains)
	}

	conflictBody := []byte(`{"primary_domain":"new.example.test","redirect_domains":[]}`)
	conflict := controlConfigurationAPIReq(t, h, http.MethodPut,
		"/api/platform/v1/control/sites/"+strconv.FormatInt(defaultSite.ID, 10)+"/domains?dry_run=1", token, conflictBody, controlConfigurationRequest{})
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "domain_conflict") {
		t.Fatalf("global domain conflict = %d %s", conflict.Code, conflict.Body.String())
	}

	duplicateBody := []byte(`{"primary_domain":"dup.example.test","redirect_domains":["dup.example.test"]}`)
	duplicate := controlConfigurationAPIReq(t, h, http.MethodPut,
		"/api/platform/v1/control/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/domains?dry_run=1", token, duplicateBody, controlConfigurationRequest{})
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), "duplicate_domain") {
		t.Fatalf("duplicate domain = %d %s", duplicate.Code, duplicate.Body.String())
	}
}

func TestPlatformControlInitialPasswordChangeIsUIOnlyAndOneTime(t *testing.T) {
	srv, h, ps, _, _ := setupPlatformAutomation(t)
	token := "gcmsp_securitycontrol12"
	createControlConfigurationKey(t, ps, token, platform.KeyMembershipAll,
		strings.Join([]string{apiScopeControlRead, apiScopeSecurityWrite}, ","), nil)

	status := controlConfigurationAPIReq(t, h, http.MethodGet, "/api/platform/v1/control/security", token, nil, controlConfigurationRequest{})
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"password_status":"default"`) || !strings.Contains(status.Body.String(), `"initial_password_change_available":true`) {
		t.Fatalf("default password status = %d %s", status.Code, status.Body.String())
	}
	dryRun := controlConfigurationAPIReq(t, h, http.MethodPost, "/api/platform/v1/control/security?dry_run=1", token, []byte(`{}`), controlConfigurationRequest{})
	if dryRun.Code != http.StatusOK || !strings.Contains(dryRun.Body.String(), `"maximum_bytes":72`) {
		t.Fatalf("password dry run = %d %s", dryRun.Code, dryRun.Body.String())
	}
	if !srv.adminPasswordIsDefault() {
		t.Fatal("password dry run changed credentials")
	}

	password := "New-Initial-Password-2026!"
	body, _ := json.Marshal(map[string]string{"new_password": password, "confirm_password": password})
	withoutUI := controlConfigurationAPIReq(t, h, http.MethodPost, "/api/platform/v1/control/security", token, body,
		controlConfigurationRequest{Confirm: "security.initial-password", IdempotencyKey: "password-no-ui"})
	if withoutUI.Code != http.StatusForbidden || !strings.Contains(withoutUI.Body.String(), "pilot_ui_required") {
		t.Fatalf("non-UI password change = %d %s", withoutUI.Code, withoutUI.Body.String())
	}

	if err := ps.CreateAdminSession("platform-session-before-password", "admin", "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create platform session: %v", err)
	}
	if err := srv.store.CreateAdminSession("site-session-before-password", "admin", "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create site session: %v", err)
	}
	change := controlConfigurationAPIReq(t, h, http.MethodPost, "/api/platform/v1/control/security", token, body,
		controlConfigurationRequest{Confirm: "security.initial-password", IdempotencyKey: "password-change-1", PilotUI: true})
	if change.Code != http.StatusOK {
		t.Fatalf("initial password change = %d %s", change.Code, change.Body.String())
	}
	if strings.Contains(change.Body.String(), password) || strings.Contains(change.Body.String(), store.DefaultAdminPassword) {
		t.Fatalf("password leaked in response: %s", change.Body.String())
	}
	_, platformHash, err := ps.GetAdminCredentials()
	if err != nil || bcrypt.CompareHashAndPassword([]byte(platformHash), []byte(password)) != nil {
		t.Fatalf("platform password not updated: err=%v", err)
	}
	defaultHash := srv.store.Setting("admin_password_hash")
	if bcrypt.CompareHashAndPassword([]byte(defaultHash), []byte(password)) != nil {
		t.Fatal("default site password was not updated")
	}
	if _, ok, _ := ps.GetAdminSession("platform-session-before-password"); ok {
		t.Fatal("platform admin sessions were not revoked")
	}
	if _, ok, _ := srv.store.GetAdminSession("site-session-before-password"); ok {
		t.Fatal("default-site admin sessions were not revoked")
	}

	secondPassword := "Another-Initial-Password-2026!"
	secondBody, _ := json.Marshal(map[string]string{"new_password": secondPassword, "confirm_password": secondPassword})
	second := controlConfigurationAPIReq(t, h, http.MethodPost, "/api/platform/v1/control/security", token, secondBody,
		controlConfigurationRequest{Confirm: "security.initial-password", IdempotencyKey: "password-change-2", PilotUI: true})
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "initial_password_already_changed") {
		t.Fatalf("second initial password change = %d %s", second.Code, second.Body.String())
	}
	if strings.Contains(second.Body.String(), secondPassword) {
		t.Fatalf("rejected password leaked: %s", second.Body.String())
	}

	dryRunWithPassword := controlConfigurationAPIReq(t, h, http.MethodPost, "/api/platform/v1/control/security?dry_run=1", token, body, controlConfigurationRequest{})
	if dryRunWithPassword.Code != http.StatusBadRequest || strings.Contains(dryRunWithPassword.Body.String(), password) {
		t.Fatalf("password dry-run body handling = %d %s", dryRunWithPassword.Code, dryRunWithPassword.Body.String())
	}
}
