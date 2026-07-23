package web

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlCloudflareResponseNeverLeaksToken(t *testing.T) {
	fixture := setupControlSitesFixture(t)
	secret := "cf-secret-must-never-reach-pilot"
	authorization := controlCloudflareAuthorization{
		Label:       "Production Cloudflare",
		Token:       secret,
		AccountID:   "account-1",
		AccountName: "Production",
		Zones: []controlCloudflareAuthorizationZone{{
			ID: "zone-1", Name: "example.com", AccountID: "account-1", AccountName: "Production",
		}},
	}
	if err := fixture.server.writeControlCloudflareAuthorizations([]controlCloudflareAuthorization{authorization}); err != nil {
		t.Fatalf("write Cloudflare authorization: %v", err)
	}

	payload := fixture.server.controlCloudflareAuthorizationResponse()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	body := string(raw)
	if strings.Contains(body, secret) || strings.Contains(body, `"token":`) || strings.Contains(body, `"api_token":`) {
		t.Fatalf("Cloudflare secret leaked to Pilot: %s", body)
	}
	if payload["configured"] != true || payload["authorization_count"] != 1 || payload["zone_count"] != 1 {
		t.Fatalf("unexpected safe Cloudflare metadata: %#v", payload)
	}
	if !strings.Contains(body, controlCloudflareAuthorizationID(secret)) || !strings.Contains(body, "example.com") {
		t.Fatalf("safe authorization metadata is incomplete: %s", body)
	}
}

func TestNormalizeControlCloudflareAuthorizationsDeduplicatesSameToken(t *testing.T) {
	items := normalizeControlCloudflareAuthorizations([]controlCloudflareAuthorization{
		{Label: "Pilot 连接", Token: "same-cloudflare-token"},
		{Label: "另一个名称", Token: "  same-cloudflare-token\n"},
	})
	if len(items) != 1 {
		t.Fatalf("same token produced %d authorizations, want 1: %#v", len(items), items)
	}
	if items[0].ID != controlCloudflareAuthorizationID("same-cloudflare-token") {
		t.Fatalf("authorization id did not use the normalized token: %#v", items[0])
	}
	if items[0].Label != "Pilot 连接" {
		t.Fatalf("deduplication should preserve the first authorization metadata: %#v", items[0])
	}
}

func TestControlSiteDeploymentResponseUsesGCMSCloudflareState(t *testing.T) {
	fixture := setupControlSitesFixture(t)
	secret := "cf-site-secret-must-never-reach-pilot"
	cfg := CloudflareConfig{
		APIToken:    secret,
		AccountID:   "account-1",
		AccountName: "Production",
		ZoneID:      "zone-1",
		ZoneName:    "example.com",
		DeployMode:  "workers",
		WorkerName:  "example-site",
		Domains: []CloudflareDomain{
			{Host: "www.example.com", Primary: true},
			{Host: "example.com", RedirectToPrimary: true},
		},
		AutoSync: true,
		SyncMode: cloudflareSyncModeRealtime,
	}
	if err := saveControlCloudflareConfig(fixture.server.store, cfg); err != nil {
		t.Fatalf("save Cloudflare config: %v", err)
	}

	runtime := &SiteRuntime{
		Site: fixture.defaultSite, Store: fixture.server.store,
		BaseURL: "http://127.0.0.1:8080", UploadDir: fixture.defaultSite.UploadDir,
	}
	siteServer := &Server{
		store: fixture.server.store, platform: fixture.platform,
		platformSiteID: fixture.defaultSite.ID, uploadDir: fixture.defaultSite.UploadDir,
		baseURL: runtime.BaseURL, cloudflareStatusFile: filepath.Join(t.TempDir(), "cloudflare-deploy.json"),
		rootServer: fixture.server,
	}
	payload := fixture.server.controlSiteDeploymentResponse(&controlCloudflareSiteHandle{
		site: fixture.defaultSite, runtime: runtime, server: siteServer,
	})

	if payload["provider"] != "cloudflare" || payload["primary_domain"] != "www.example.com" || payload["public_url"] != "https://www.example.com" {
		t.Fatalf("deployment response did not reflect GCMS state: %#v", payload)
	}
	if payload["authorization_id"] != controlCloudflareAuthorizationID(secret) {
		t.Fatalf("deployment response did not expose the safe authorization id: %#v", payload)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal deployment response: %v", err)
	}
	body := string(raw)
	if strings.Contains(body, secret) || strings.Contains(body, `"api_token":`) {
		t.Fatalf("site deployment response leaked Cloudflare secret: %s", body)
	}
	if !strings.Contains(body, `"summary"`) || !strings.Contains(body, `"runtime"`) || !strings.Contains(body, "example.com") {
		t.Fatalf("deployment response is missing GCMS summary/runtime details: %s", body)
	}
}
