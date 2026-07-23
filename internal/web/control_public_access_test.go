package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"cms.ccvar.com/internal/platform"
)

func publicAccessClearTestPayload(t *testing.T, srv *Server, siteID int64, host string, state controlPublicAccessProxyState) []byte {
	t.Helper()
	domains, err := srv.controlDomainsForSite(siteID)
	if err != nil {
		t.Fatalf("read site domains: %v", err)
	}
	body, err := json.Marshal(controlPublicAccessClearInput{
		ExpectedPrimaryDomain:     host,
		ExpectedGeneration:        state.Generation,
		ExpectedDomainFingerprint: controlPublicAccessDomainFingerprint(siteID, domains),
	})
	if err != nil {
		t.Fatalf("marshal clear input: %v", err)
	}
	return body
}

func TestPlatformControlClearUnverifiedPublicAccess(t *testing.T) {
	srv, h, ps, _, site := setupPlatformAutomation(t)
	setPlatformTestPassword(t, ps, controlTestPassword)
	token := "gcmsp_publicclear12345"
	createControlConfigurationKey(t, ps, token, platform.KeyMembershipAllowlist,
		strings.Join([]string{apiScopeControlRead, apiScopeControlUnlock, apiScopeDomainsRead, apiScopeDomainsWrite}, ","), []int64{site.ID})

	const host = "unverified.invalid"
	if err := ps.ReplaceSiteDomains(site.ID, []platform.SiteDomainSpec{{Scheme: "https", Host: host, Primary: true}}); err != nil {
		t.Fatalf("seed unverified domain: %v", err)
	}
	state := newControlPublicAccessProxyState(host, false)
	state.AccessState = publicAccessProgressAttention
	state.AccessStage = "dns"
	state.AccessMessage = "DNS 尚未生效。"
	if err := srv.saveControlPublicAccessProxyState(site.ID, state); err != nil {
		t.Fatalf("seed public-access state: %v", err)
	}
	body := publicAccessClearTestPayload(t, srv, site.ID, host, state)
	path := "/api/platform/v1/control/sites/" + strconv.FormatInt(site.ID, 10) + "/public-access"

	plan := controlConfigurationAPIReq(t, h, http.MethodDelete, path+"?dry_run=1", token, body, controlConfigurationRequest{})
	if plan.Code != http.StatusOK || !strings.Contains(plan.Body.String(), `"dry_run":true`) ||
		!strings.Contains(plan.Body.String(), `"dns_preserved":true`) {
		t.Fatalf("clear dry-run = %d %s", plan.Code, plan.Body.String())
	}
	if domains, _ := srv.controlDomainsForSite(site.ID); len(domains) != 1 {
		t.Fatalf("dry-run changed domains: %#v", domains)
	}

	locked := controlConfigurationAPIReq(t, h, http.MethodDelete, path, token, body,
		controlConfigurationRequest{Confirm: "public_access.clear_unverified", IdempotencyKey: "clear-unverified-1"})
	if locked.Code != http.StatusForbidden || !strings.Contains(locked.Body.String(), "unlock_required") {
		t.Fatalf("clear without unlock = %d %s", locked.Code, locked.Body.String())
	}
	unlock := controlConfigurationUnlock(t, h, token, controlTestPassword, "public_access.clear_unverified")
	cleared := controlConfigurationAPIReq(t, h, http.MethodDelete, path, token, body,
		controlConfigurationRequest{
			Confirm:        "public_access.clear_unverified",
			IdempotencyKey: "clear-unverified-1",
			UnlockToken:    unlock,
		})
	if cleared.Code != http.StatusOK || !strings.Contains(cleared.Body.String(), `"cleared_domain":"`+host+`"`) {
		t.Fatalf("clear unverified domain = %d %s", cleared.Code, cleared.Body.String())
	}
	if domains, _ := srv.controlDomainsForSite(site.ID); len(domains) != 0 {
		t.Fatalf("domain remains after clear: %#v", domains)
	}
	if _, ok := srv.loadControlPublicAccessProxyState(site.ID); ok {
		t.Fatal("public-access progress remains after clear")
	}
}

func TestPlatformControlClearUnverifiedRejectsSuccessfulOrStaleAttempt(t *testing.T) {
	srv, h, ps, _, site := setupPlatformAutomation(t)
	setPlatformTestPassword(t, ps, controlTestPassword)
	token := "gcmsp_publicguard12345"
	createControlConfigurationKey(t, ps, token, platform.KeyMembershipAllowlist,
		strings.Join([]string{apiScopeControlUnlock, apiScopeDomainsWrite}, ","), []int64{site.ID})
	unlock := controlConfigurationUnlock(t, h, token, controlTestPassword, "public_access.clear_unverified")
	path := "/api/platform/v1/control/sites/" + strconv.FormatInt(site.ID, 10) + "/public-access"

	const host = "successful.invalid"
	if err := ps.ReplaceSiteDomains(site.ID, []platform.SiteDomainSpec{{Scheme: "https", Host: host, Primary: true}}); err != nil {
		t.Fatalf("seed domain: %v", err)
	}
	state := newControlPublicAccessProxyState(host, false)
	state.VerifiedAt = 123
	state.AccessState = publicAccessProgressReady
	state.AccessStage = "ready"
	if err := srv.saveControlPublicAccessProxyState(site.ID, state); err != nil {
		t.Fatalf("seed verified state: %v", err)
	}
	body := publicAccessClearTestPayload(t, srv, site.ID, host, state)
	blocked := controlConfigurationAPIReq(t, h, http.MethodDelete, path, token, body,
		controlConfigurationRequest{
			Confirm:        "public_access.clear_unverified",
			IdempotencyKey: "clear-verified-1",
			UnlockToken:    unlock,
		})
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "public_access_already_live") {
		t.Fatalf("verified clear = %d %s", blocked.Code, blocked.Body.String())
	}
	if domains, _ := srv.controlDomainsForSite(site.ID); len(domains) != 1 {
		t.Fatalf("verified domain was removed: %#v", domains)
	}

	state.VerifiedAt = 0
	state.AccessState = publicAccessProgressAttention
	state.AccessStage = "dns"
	if err := srv.saveControlPublicAccessProxyState(site.ID, state); err != nil {
		t.Fatalf("seed attention state: %v", err)
	}
	staleBody := publicAccessClearTestPayload(t, srv, site.ID, host, state)
	state.Generation += "-changed"
	if err := srv.saveControlPublicAccessProxyState(site.ID, state); err != nil {
		t.Fatalf("advance attempt: %v", err)
	}
	stale := controlConfigurationAPIReq(t, h, http.MethodDelete, path, token, staleBody,
		controlConfigurationRequest{
			Confirm:        "public_access.clear_unverified",
			IdempotencyKey: "clear-stale-attempt-1",
			UnlockToken:    unlock,
		})
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "public_access_attempt_changed") {
		t.Fatalf("stale attempt clear = %d %s", stale.Code, stale.Body.String())
	}
}

func TestPlatformControlPublicAccessChecksCloudflareZoneBeforeSavingDomain(t *testing.T) {
	_, h, ps, _, site := setupPlatformAutomation(t)
	setPlatformTestPassword(t, ps, controlTestPassword)
	token := "gcmsp_publicpreflight12"
	createControlConfigurationKey(t, ps, token, platform.KeyMembershipAllowlist,
		strings.Join([]string{apiScopeControlUnlock, apiScopeDomainsWrite}, ","), []int64{site.ID})
	unlock := controlConfigurationUnlock(t, h, token, controlTestPassword, "public_access.apply")
	path := "/api/platform/v1/control/sites/" + strconv.FormatInt(site.ID, 10) + "/public-access"
	body := []byte(`{"primary_domain":"www.not-authorized.invalid","redirect_domains":[],"auto_dns":true}`)
	apply := controlConfigurationAPIReq(t, h, http.MethodPost, path, token, body,
		controlConfigurationRequest{
			Confirm:        "public_access.apply",
			IdempotencyKey: "public-preflight-1",
			UnlockToken:    unlock,
		})
	if apply.Code != http.StatusUnprocessableEntity || !strings.Contains(apply.Body.String(), "cloudflare_not_authorized") {
		t.Fatalf("Cloudflare preflight = %d %s", apply.Code, apply.Body.String())
	}
	domains, err := ps.SiteDomains()
	if err != nil {
		t.Fatalf("list domains after failed preflight: %v", err)
	}
	for _, domain := range domains {
		if domain != nil && domain.SiteID == site.ID && domain.Host == "www.not-authorized.invalid" {
			t.Fatalf("failed Cloudflare preflight persisted domain: %#v", domain)
		}
	}
}
