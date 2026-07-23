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
		Items         []controlTheme       `json:"items"`
		Families      []controlThemeFamily `json:"families"`
		SelectedTheme string               `json:"selected_theme"`
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
	seenSkins := make(map[string]bool, len(Themes))
	selectedSkins := 0
	selectedFamilies := 0
	for _, family := range catalog.Families {
		if family.ID == "" || family.Name == "" || family.Description == "" || len(family.Categories) == 0 || len(family.Skins) == 0 {
			t.Fatalf("incomplete theme family: %#v", family)
		}
		if family.Selected {
			selectedFamilies++
		}
		for _, skin := range family.Skins {
			if skin.ID == "" || skin.Name == "" || skin.Description == "" || skin.Category == "" || skin.Layout == "" ||
				skin.Accent == "" || skin.Background == "" || skin.Radius == "" {
				t.Fatalf("incomplete theme skin in family %q: %#v", family.ID, skin)
			}
			if seenSkins[skin.ID] {
				t.Fatalf("theme skin %q appears in more than one family", skin.ID)
			}
			seenSkins[skin.ID] = true
			if skin.Selected {
				selectedSkins++
				if skin.ID != catalog.SelectedTheme || !family.Selected {
					t.Fatalf("selected skin/family mismatch: family=%q skin=%q selected_theme=%q", family.ID, skin.ID, catalog.SelectedTheme)
				}
			}
		}
	}
	if len(seenSkins) != len(Themes) || selectedSkins != 1 || selectedFamilies != 1 {
		t.Fatalf("theme family catalog coverage: skins=%d/%d selected_skins=%d selected_families=%d", len(seenSkins), len(Themes), selectedSkins, selectedFamilies)
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
	var plan struct {
		CurrentTheme    string `json:"current_theme"`
		TargetTheme     string `json:"target_theme"`
		CategoryChanged bool   `json:"category_changed"`
		LayoutChanged   bool   `json:"layout_changed"`
		Impact          struct {
			Content    bool `json:"content"`
			Navigation bool `json:"navigation"`
			Domains    bool `json:"domains"`
		} `json:"impact"`
	}
	if err := json.Unmarshal(dryRun.Body.Bytes(), &plan); err != nil {
		t.Fatalf("decode theme plan: %v", err)
	}
	if plan.CurrentTheme != "editorial" || plan.TargetTheme != "magazine" || plan.CategoryChanged ||
		plan.Impact.Content || plan.Impact.Navigation || plan.Impact.Domains {
		t.Fatalf("unexpected theme plan: %#v", plan)
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

func TestPlatformControlThemeExpectedCurrentGuardsExecution(t *testing.T) {
	srv, h, ps, _, blogSite := setupPlatformAutomation(t)
	token := "gcmsp_themeexpected123"
	createControlConfigurationKey(t, ps, token, platform.KeyMembershipAllowlist,
		strings.Join([]string{apiScopeThemesRead, apiScopeThemesApply}, ","), []int64{blogSite.ID})
	runtime, ok := srv.runtimePool().runtimeByID(blogSite.ID)
	if !ok || runtime == nil || runtime.Store == nil {
		t.Fatal("blog runtime missing")
	}
	if err := runtime.Store.SetSetting("theme", "editorial"); err != nil {
		t.Fatalf("seed current theme: %v", err)
	}

	staleBody := []byte(`{"theme_id":"magazine","expected_current_theme":"terminal"}`)
	stale := controlConfigurationAPIReq(t, h, http.MethodPut,
		"/api/platform/v1/control/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/theme", token, staleBody,
		controlConfigurationRequest{Confirm: "themes.apply", IdempotencyKey: "theme-stale-apply"})
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), `"error":"theme_changed"`) {
		t.Fatalf("stale theme apply = %d %s, want 409 theme_changed", stale.Code, stale.Body.String())
	}
	if got := controlCurrentTheme(runtime.Store); got != "editorial" {
		t.Fatalf("stale apply changed theme to %q", got)
	}

	// dry-run always reports the live current state, even if a caller's previous
	// expectation is stale; execution is the point protected by the compare guard.
	plan := controlConfigurationAPIReq(t, h, http.MethodPut,
		"/api/platform/v1/control/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/theme?dry_run=1", token, staleBody,
		controlConfigurationRequest{})
	if plan.Code != http.StatusOK || !strings.Contains(plan.Body.String(), `"current_theme":"editorial"`) {
		t.Fatalf("stale theme dry-run = %d %s", plan.Code, plan.Body.String())
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

func TestPlatformDiscoveryThemeSummaryHonorsScopeAndDisabledSites(t *testing.T) {
	srv, h, ps, _, blogSite := setupPlatformAutomation(t)
	runtime, ok := srv.runtimePool().runtimeByID(blogSite.ID)
	if !ok || runtime == nil || runtime.Store == nil {
		t.Fatal("blog runtime missing")
	}
	if err := runtime.Store.SetSetting("theme", "tradewind"); err != nil {
		t.Fatalf("seed current theme: %v", err)
	}
	if err := runtime.Store.SetSetting(controlPreviousThemeSettingKey, "editorial"); err != nil {
		t.Fatalf("seed previous theme: %v", err)
	}

	withoutScope := "gcmsp_themehidden12345"
	createControlConfigurationKey(t, ps, withoutScope, platform.KeyMembershipAllowlist,
		"posts:read", []int64{blogSite.ID})
	hidden := platformAPIReq(t, h, http.MethodGet, "/api/platform/v1/sites", withoutScope, nil)
	if hidden.Code != http.StatusOK {
		t.Fatalf("discovery without themes scope = %d %s", hidden.Code, hidden.Body.String())
	}
	var hiddenPayload struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(hidden.Body.Bytes(), &hiddenPayload); err != nil || len(hiddenPayload.Items) != 1 {
		t.Fatalf("decode discovery without themes scope: items=%d err=%v body=%s", len(hiddenPayload.Items), err, hidden.Body.String())
	}
	if _, exposed := hiddenPayload.Items[0]["theme"]; exposed {
		t.Fatalf("discovery exposed theme without themes:read: %s", hidden.Body.String())
	}

	withScope := "gcmsp_themevisible1234"
	createControlConfigurationKey(t, ps, withScope, platform.KeyMembershipAllowlist,
		apiScopeThemesRead, []int64{blogSite.ID})
	visible := platformAPIReq(t, h, http.MethodGet, "/api/platform/v1/sites", withScope, nil)
	if visible.Code != http.StatusOK {
		t.Fatalf("discovery with themes scope = %d %s", visible.Code, visible.Body.String())
	}
	var visiblePayload struct {
		Items []struct {
			Theme *controlSiteThemeSummary `json:"theme"`
		} `json:"items"`
		LifecycleItems []struct {
			ID     int64                    `json:"id"`
			Status string                   `json:"status"`
			Theme  *controlSiteThemeSummary `json:"theme"`
		} `json:"lifecycle_items"`
	}
	if err := json.Unmarshal(visible.Body.Bytes(), &visiblePayload); err != nil || len(visiblePayload.Items) != 1 {
		t.Fatalf("decode discovery with themes scope: items=%d err=%v body=%s", len(visiblePayload.Items), err, visible.Body.String())
	}
	theme := visiblePayload.Items[0].Theme
	if theme == nil || theme.ID != "tradewind" || theme.Family != "factory-catalog" || theme.FamilyName == "" ||
		theme.Category != ThemeCategoryFactory || theme.Layout != "factory-catalog" ||
		theme.PreviousTheme != "editorial" || !theme.CanRollback {
		t.Fatalf("unexpected discovery theme summary: %#v", theme)
	}

	if err := ps.SetSiteStatus(blogSite.ID, "disabled"); err != nil {
		t.Fatalf("disable site: %v", err)
	}
	if err := srv.reloadRuntimePool(); err != nil {
		t.Fatalf("reload runtimes after disabling site: %v", err)
	}
	disabled := platformAPIReq(t, h, http.MethodGet, "/api/platform/v1/sites", withScope, nil)
	if disabled.Code != http.StatusOK {
		t.Fatalf("disabled discovery with themes scope = %d %s", disabled.Code, disabled.Body.String())
	}
	visiblePayload.Items = nil
	visiblePayload.LifecycleItems = nil
	if err := json.Unmarshal(disabled.Body.Bytes(), &visiblePayload); err != nil {
		t.Fatalf("decode disabled discovery: %v body=%s", err, disabled.Body.String())
	}
	if len(visiblePayload.Items) != 0 || len(visiblePayload.LifecycleItems) != 1 {
		t.Fatalf("disabled discovery membership mismatch: items=%d lifecycle=%d", len(visiblePayload.Items), len(visiblePayload.LifecycleItems))
	}
	disabledTheme := visiblePayload.LifecycleItems[0].Theme
	if visiblePayload.LifecycleItems[0].ID != blogSite.ID || visiblePayload.LifecycleItems[0].Status != "disabled" ||
		disabledTheme == nil || disabledTheme.ID != "tradewind" || disabledTheme.PreviousTheme != "editorial" {
		t.Fatalf("disabled site lost theme summary: %#v", visiblePayload.LifecycleItems[0])
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

func TestPlatformControlSecurityIsReadOnlyEvenForLegacyScope(t *testing.T) {
	srv, h, ps, _, _ := setupPlatformAutomation(t)
	token := "gcmsp_securitycontrol12"
	createControlConfigurationKey(t, ps, token, platform.KeyMembershipAll,
		strings.Join([]string{apiScopeControlRead, retiredAPIScopeSecurityWrite}, ","), nil)

	status := controlConfigurationAPIReq(t, h, http.MethodGet, "/api/platform/v1/control/security", token, nil, controlConfigurationRequest{})
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"password_status":"default"`) || !strings.Contains(status.Body.String(), `"initial_password_change_available":true`) || !strings.Contains(status.Body.String(), `"initial_password_change_transport":"gcms_cli"`) || !strings.Contains(status.Body.String(), `"password_write_api_available":false`) {
		t.Fatalf("default password status = %d %s", status.Code, status.Body.String())
	}
	password := "Must-Never-Reach-The-Control-API-2026!"
	body := []byte(`{"new_password":"` + password + `","confirm_password":"` + password + `"}`)
	for _, path := range []string{"/api/platform/v1/control/security", "/api/platform/v1/control/security?dry_run=1"} {
		blocked := controlConfigurationAPIReq(t, h, http.MethodPost, path, token, body,
			controlConfigurationRequest{Confirm: "security.initial-password", IdempotencyKey: "legacy-password-write", PilotUI: true})
		if blocked.Code != http.StatusMethodNotAllowed || !strings.Contains(blocked.Body.String(), "password_write_not_available") || blocked.Header().Get("Allow") != http.MethodGet {
			t.Fatalf("legacy password write was not blocked: %d %s headers=%v", blocked.Code, blocked.Body.String(), blocked.Header())
		}
		if strings.Contains(blocked.Body.String(), password) {
			t.Fatalf("blocked password leaked in response: %s", blocked.Body.String())
		}
	}
	if !srv.adminPasswordIsDefault() {
		t.Fatal("blocked control API request changed the initial password")
	}
}
