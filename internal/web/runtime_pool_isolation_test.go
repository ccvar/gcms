package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestReloadRuntimePoolIsolatesBrokenSiteAndKeepsHealthySitesServing(t *testing.T) {
	fixture := setupControlSitesFixture(t)
	badRoot := filepath.Join(fixture.dataDir, "sites", "broken")
	if err := os.MkdirAll(filepath.Join(badRoot, "uploads"), 0o755); err != nil {
		t.Fatal(err)
	}
	badDB := filepath.Join(badRoot, "cms.db")
	if err := os.WriteFile(badDB, []byte("this is not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	badSite, err := fixture.platform.CreateSite(
		"broken",
		"Broken Site",
		badDB,
		filepath.Join(badRoot, "uploads"),
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.platform.AddSiteDomain(badSite.ID, "https", "broken.example.test", true, false); err != nil {
		t.Fatal(err)
	}

	if err := fixture.server.reloadRuntimePool(); err != nil {
		t.Fatalf("one bad site must not take down the runtime pool: %v", err)
	}
	pool := fixture.server.runtimePool()
	if pool == nil {
		t.Fatal("runtime pool missing")
	}
	if _, ok := pool.runtimeByID(fixture.defaultSite.ID); !ok {
		t.Fatal("healthy default site disappeared")
	}
	if _, ok := pool.runtimeByID(fixture.memberSite.ID); !ok {
		t.Fatal("healthy member site disappeared")
	}
	if _, ok := pool.runtimeByID(badSite.ID); ok {
		t.Fatal("broken site must be fail-closed")
	}
	failure, ok := pool.runtimeFailure(badSite.ID)
	if !ok || failure.Code != "site_runtime_unavailable" || failure.Detail == "" {
		t.Fatalf("runtime failure = %#v, %v", failure, ok)
	}

	healthy := httptest.NewRecorder()
	healthyReq := httptest.NewRequest(http.MethodGet, "https://platform.test/zh/", nil)
	fixture.server.Handler().ServeHTTP(healthy, healthyReq)
	if healthy.Code != http.StatusOK {
		t.Fatalf("healthy site status = %d, body=%s", healthy.Code, healthy.Body.String())
	}

	broken := httptest.NewRecorder()
	brokenReq := httptest.NewRequest(http.MethodGet, "https://broken.example.test/zh/", nil)
	fixture.server.Handler().ServeHTTP(broken, brokenReq)
	if broken.Code != http.StatusNotFound {
		t.Fatalf("broken public host must fail closed: %d %s", broken.Code, broken.Body.String())
	}

	token, _ := createControlSitesKey(
		t,
		fixture,
		"runtime-isolation",
		"all",
		apiScopePageProjectsRead,
		nil,
	)
	platformRequest := httptest.NewRequest(
		http.MethodGet,
		"https://platform.test/api/platform/v1/sites/"+strconv.FormatInt(badSite.ID, 10)+"/page-platform/capabilities",
		nil,
	)
	platformRequest.Header.Set("Authorization", "Bearer "+token)
	platformResponse := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(platformResponse, platformRequest)
	if platformResponse.Code != http.StatusServiceUnavailable ||
		!strings.Contains(platformResponse.Body.String(), "site_runtime_unavailable") {
		t.Fatalf(
			"broken platform API must report isolated runtime: %d %s",
			platformResponse.Code,
			platformResponse.Body.String(),
		)
	}

	view := &View{}
	fixture.server.populatePlatformSites(view)
	if strings.TrimSpace(view.PlatformRuntimeErrors[badSite.ID]) == "" {
		t.Fatalf("admin runtime error missing: %#v", view.PlatformRuntimeErrors)
	}
}

func TestChildRuntimePlatformMetadataReusesRootRuntimePool(t *testing.T) {
	fixture := setupControlSitesFixture(t)
	pool := fixture.server.runtimePool()
	memberRuntime, memberOK := pool.runtimeByID(fixture.memberSite.ID)
	otherRuntime, otherOK := pool.runtimeByID(fixture.otherSite.ID)
	if !memberOK || memberRuntime == nil || memberRuntime.server == nil ||
		!otherOK || otherRuntime == nil || otherRuntime.server == nil || otherRuntime.Store == nil {
		t.Fatal("site runtimes missing")
	}
	child := memberRuntime.server
	if child.runtimePool() != nil {
		t.Fatal("child runtime unexpectedly owns a runtime pool")
	}
	if child.platformRuntimePool() != pool {
		t.Fatal("child runtime did not resolve the platform root pool")
	}

	if err := memberRuntime.Store.SetSetting(cloudflareDomainsKey, encodeCloudflareDomains([]CloudflareDomain{{Host: "member.example.test", Primary: true}})); err != nil {
		t.Fatalf("set member Cloudflare domain: %v", err)
	}
	memberRuntime.server.writeCloudflareStatus(CloudflareStatus{Status: "success", Published: true})
	if err := otherRuntime.Store.SetSetting(cloudflareDomainsKey, encodeCloudflareDomains([]CloudflareDomain{{Host: "other.example.test", Primary: true}})); err != nil {
		t.Fatalf("set other Cloudflare domain: %v", err)
	}
	otherRuntime.server.writeCloudflareStatus(CloudflareStatus{Status: "success", Published: true})
	if err := otherRuntime.Store.SetSetting("site.favicon", "/uploads/other-icon.svg"); err != nil {
		t.Fatalf("set other favicon: %v", err)
	}

	// 隐藏磁盘路径后，只有复用根池中已打开的 Store 才能读取图标；旧逻辑会尝试
	// 对每个站点重新 os.Stat/store.Open，并在这里得到空结果。
	movedDB := fixture.otherSite.DBPath + ".moved"
	if err := os.Rename(fixture.otherSite.DBPath, movedDB); err != nil {
		t.Fatalf("temporarily hide other site database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := os.Stat(movedDB); err == nil {
			_ = os.Rename(movedDB, fixture.otherSite.DBPath)
		}
	})

	view := &View{}
	child.populatePlatformSites(view)
	if got := view.PlatformSiteIcons[fixture.otherSite.ID]; got != "/admin/sites/"+strconv.FormatInt(fixture.otherSite.ID, 10)+"/uploads/other-icon.svg" {
		t.Fatalf("other site icon from child runtime = %q", got)
	}
	if got := view.PlatformOfficialURLs[fixture.otherSite.ID]; got != "https://other.example.test/" {
		t.Fatalf("other site official URL from child runtime = %q", got)
	}
	if got := view.PlatformOfficialURLs[fixture.memberSite.ID]; got != "https://member.example.test/" {
		t.Fatalf("member site official URL from child runtime = %q", got)
	}
}
