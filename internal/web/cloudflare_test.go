package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
)

func stubCloudflareTokenVerify(t *testing.T, fn func(context.Context, string) error) {
	t.Helper()
	prev := verifyCloudflareAPIToken
	verifyCloudflareAPIToken = fn
	t.Cleanup(func() { verifyCloudflareAPIToken = prev })
}

func TestNormalizeCloudflareWorkerName(t *testing.T) {
	tests := map[string]string{
		"":                  cloudflareDefaultWorkerName,
		"GCMS Frontend":     "gcms-frontend",
		"gcms_frontend@dev": "gcms-frontend-dev",
	}
	for input, want := range tests {
		if got := normalizeCloudflareWorkerName(input); got != want {
			t.Fatalf("normalizeCloudflareWorkerName(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestCloudflareDefaultProjectNameForHost(t *testing.T) {
	tests := map[string]string{
		"":                         cloudflareDefaultWorkerName,
		"https://Test.CCVAR.com/":  "gcms-test-ccvar-com",
		"www.example.com:443/path": "gcms-www-example-com",
	}
	for input, want := range tests {
		if got := cloudflareDefaultProjectNameForHost(input); got != want {
			t.Fatalf("cloudflareDefaultProjectNameForHost(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestNormalizeCloudflareOrigin(t *testing.T) {
	got := normalizeCloudflareOrigin("https://origin.example.com/base/?x=1#top")
	if got != "https://origin.example.com/base" {
		t.Fatalf("origin normalized to %q", got)
	}
}

func TestNormalizeCloudflareRoutePattern(t *testing.T) {
	tests := map[string]string{
		"ccvar.com":                   "ccvar.com/*",
		"www.ccvar.com/*":             "www.ccvar.com/*",
		"https://ccvar.com":           "ccvar.com/*",
		"https://ccvar.com/docs?x=1":  "ccvar.com/docs/*",
		"static.ccvar.com/assets/*":   "static.ccvar.com/assets/*",
		"  www.ccvar.com  extra text": "www.ccvar.com/*",
	}
	for input, want := range tests {
		if got := normalizeCloudflareRoutePattern(input); got != want {
			t.Fatalf("normalizeCloudflareRoutePattern(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestNormalizeCloudflareDomainsFromForm(t *testing.T) {
	domains := cloudflareDomainsFromForm("https://ccvar.com/", []string{"www.ccvar.com\nblog.ccvar.com/*", "ccvar.com"}, true)
	if len(domains) != 3 {
		t.Fatalf("domains len = %d, want 3: %#v", len(domains), domains)
	}
	if domains[0].Host != "ccvar.com" || !domains[0].Primary || domains[0].RedirectToPrimary {
		t.Fatalf("primary domain mismatch: %#v", domains[0])
	}
	for _, d := range domains[1:] {
		if !d.RedirectToPrimary {
			t.Fatalf("alias should redirect to primary: %#v", d)
		}
	}
	cfg := CloudflareConfig{Domains: domains}
	if got := strings.Join(cfg.routePatterns(), ","); got != "ccvar.com/*,www.ccvar.com/*,blog.ccvar.com/*" {
		t.Fatalf("routePatterns = %q", got)
	}
	if got := strings.Join(cfg.redirectHosts(), ","); got != "blog.ccvar.com,www.ccvar.com" {
		t.Fatalf("redirectHosts = %q", got)
	}
}

func TestCloudflareWorkerDNSTargetsTakeOverFrontendDomains(t *testing.T) {
	cfg := CloudflareConfig{Domains: []CloudflareDomain{
		{Host: "ccvar.com", Primary: true},
		{Host: "www.ccvar.com", RedirectToPrimary: true},
		{Host: "docs.ccvar.com"},
	}}

	primary := cloudflareWorkerDNSTarget(cfg, cfg.Domains[0])
	if primary.Type != "AAAA" || primary.Host != "ccvar.com" || primary.Content != "100::" {
		t.Fatalf("primary target = %#v, want AAAA ccvar.com 100::", primary)
	}

	alias := cloudflareWorkerDNSTarget(cfg, cfg.Domains[1])
	if alias.Type != "CNAME" || alias.Host != "www.ccvar.com" || alias.Content != "ccvar.com" {
		t.Fatalf("redirect alias target = %#v, want CNAME www.ccvar.com ccvar.com", alias)
	}

	standalone := cloudflareWorkerDNSTarget(cfg, cfg.Domains[2])
	if standalone.Type != "AAAA" || standalone.Host != "docs.ccvar.com" || standalone.Content != "100::" {
		t.Fatalf("standalone target = %#v, want AAAA docs.ccvar.com 100::", standalone)
	}
}

func TestCloudflareDNSRecordsMatchWorkerTarget(t *testing.T) {
	proxied := true
	unproxied := false
	target := cloudflareDNSTarget{Type: "AAAA", Host: "test3.ccvar.com", Content: "100::"}

	if !cloudflareDNSRecordsMatchTarget([]cloudflareDNSRecord{{Type: "AAAA", Name: "test3.ccvar.com", Content: "100::", Proxied: &proxied}}, target) {
		t.Fatal("proxied Worker Assets AAAA target should be treated as managed")
	}
	if cloudflareDNSRecordsMatchTarget([]cloudflareDNSRecord{{Type: "AAAA", Name: "test3.ccvar.com", Content: "100::", Proxied: &unproxied}}, target) {
		t.Fatal("unproxied Worker Assets DNS record should not be treated as managed")
	}
	if cloudflareDNSRecordsMatchTarget([]cloudflareDNSRecord{{Type: "A", Name: "test3.ccvar.com", Content: "38.207.175.236", Proxied: &proxied}}, target) {
		t.Fatal("old origin A record should not be treated as managed")
	}
	if cloudflareDNSRecordsMatchTarget([]cloudflareDNSRecord{
		{Type: "AAAA", Name: "test3.ccvar.com", Content: "100::", Proxied: &proxied},
		{Type: "A", Name: "test3.ccvar.com", Content: "38.207.175.236", Proxied: &proxied},
	}, target) {
		t.Fatal("extra route DNS record should keep takeover status pending")
	}
}

func TestCloudflareDNSRecordsMatchRedirectAliasTarget(t *testing.T) {
	proxied := true
	target := cloudflareDNSTarget{Type: "CNAME", Host: "www.ccvar.com", Content: "ccvar.com"}
	if !cloudflareDNSRecordsMatchTarget([]cloudflareDNSRecord{{Type: "CNAME", Name: "www.ccvar.com", Content: "ccvar.com.", Proxied: &proxied}}, target) {
		t.Fatal("proxied redirect CNAME target should be treated as managed")
	}
}

func TestCloudflarePagesDNSTarget(t *testing.T) {
	cfg := CloudflareConfig{Domains: []CloudflareDomain{{Host: "ccvar.com", Primary: true}}}
	target := cloudflarePagesDNSTarget(cfg, cfg.Domains[0], "gcms-ccvar-com.pages.dev.")
	if target.Type != "CNAME" || target.Host != "ccvar.com" || target.Content != "gcms-ccvar-com.pages.dev" {
		t.Fatalf("Pages target = %#v, want CNAME ccvar.com gcms-ccvar-com.pages.dev", target)
	}
}

func TestCloudflareViewInfersLegacyPublishedDeployment(t *testing.T) {
	t.Chdir(t.TempDir())
	writeCloudflareStatus(CloudflareStatus{
		Status:  "idle",
		Message: "暂无 Cloudflare 部署任务",
	})
	st, err := store.Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for key, value := range map[string]string{
		cloudflareAPITokenKey:     "token",
		cloudflareDeployModeKey:   cloudflareModeWorkerAssets,
		cloudflareWorkerNameKey:   "gcms-test3-ccvar-com",
		cloudflareDomainsKey:      `[{"host":"test3.ccvar.com","primary":true}]`,
		cloudflareAccountIDKey:    "account_id",
		cloudflareZoneIDKey:       "zone_id",
		cloudflareSourceModeKey:   cloudflareSourceModeRedirect,
		cloudflareAutoSyncKey:     "1",
		cloudflareHTMLTTLKey:      "300",
		cloudflareOriginURLKey:    "https://cms.ccvar.com",
		cloudflareSyncModeKey:     cloudflareSyncModeRealtime,
		cloudflarePagesProjectKey: "gcms-test3-ccvar-com",
	} {
		if err := st.SetSetting(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	s := &Server{store: st}

	view := s.cloudflareView()
	if !view.Status.Published {
		t.Fatal("legacy configured deployment should be treated as published")
	}
	if view.Status.Status != "success" {
		t.Fatalf("legacy status = %q, want success", view.Status.Status)
	}
	if view.Status.DNSStatus != cloudflareDNSStatusManual {
		t.Fatalf("legacy dns status = %q, want manual", view.Status.DNSStatus)
	}
	if !view.Status.CanUnpublish {
		t.Fatal("legacy published deployment should allow unpublish")
	}
	if !strings.Contains(view.Status.Message, "旧版本") {
		t.Fatalf("legacy message = %q, want compatibility hint", view.Status.Message)
	}
}

func TestCloudflareViewDoesNotInferFreshConfigAsPublished(t *testing.T) {
	t.Chdir(t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for key, value := range map[string]string{
		cloudflareAPITokenKey:   "token",
		cloudflareDeployModeKey: cloudflareModeWorkerAssets,
		cloudflareWorkerNameKey: "gcms-new",
		cloudflareDomainsKey:    `[{"host":"new.example.com","primary":true}]`,
	} {
		if err := st.SetSetting(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	s := &Server{store: st}

	view := s.cloudflareView()
	if view.Status.Published {
		t.Fatal("fresh config without detected account and zone should not be treated as published")
	}
	if view.Status.DNSStatus != "" {
		t.Fatalf("fresh config dns status = %q, want empty", view.Status.DNSStatus)
	}
}

func TestCloudflareViewDoesNotInferUnpublishedStatusAsPublished(t *testing.T) {
	t.Chdir(t.TempDir())
	writeCloudflareStatus(CloudflareStatus{
		Status:  "success",
		Message: "Cloudflare 公开入口已取消；项目和静态资源仍保留在 Cloudflare，DNS 未被删除。",
	})
	st, err := store.Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for key, value := range map[string]string{
		cloudflareAPITokenKey:   "token",
		cloudflareDeployModeKey: cloudflareModeWorkerAssets,
		cloudflareWorkerNameKey: "gcms-test3-ccvar-com",
		cloudflareDomainsKey:    `[{"host":"test3.ccvar.com","primary":true}]`,
		cloudflareAccountIDKey:  "account_id",
		cloudflareZoneIDKey:     "zone_id",
	} {
		if err := st.SetSetting(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	s := &Server{store: st}

	view := s.cloudflareView()
	if view.Status.Published {
		t.Fatal("unpublished status should not be treated as published")
	}
}

func TestCloudflarePagesRedirectsFile(t *testing.T) {
	cfg := CloudflareConfig{Domains: []CloudflareDomain{
		{Host: "ccvar.com", Primary: true},
		{Host: "www.ccvar.com", RedirectToPrimary: true},
		{Host: "docs.ccvar.com"},
	}}
	got := cloudflarePagesRedirectsFile(cfg)
	want := "https://www.ccvar.com/* https://ccvar.com/:splat 301\n"
	if got != want {
		t.Fatalf("redirects file = %q, want %q", got, want)
	}
}

func TestCloudflarePagesRedirectsDefaultPagesDomain(t *testing.T) {
	cfg := CloudflareConfig{
		DeployMode:       cloudflareModePages,
		PagesProjectName: "gcms-ccvar-com",
		Domains: []CloudflareDomain{
			{Host: "ccvar.com", Primary: true},
			{Host: "www.ccvar.com", RedirectToPrimary: true},
		},
	}
	got := cloudflarePagesRedirectsFile(cfg)
	want := "https://gcms-ccvar-com.pages.dev/* https://ccvar.com/:splat 301\nhttps://www.ccvar.com/* https://ccvar.com/:splat 301\n"
	if got != want {
		t.Fatalf("redirects file = %q, want %q", got, want)
	}
}

func TestCloudflarePagesRedirectsInferDefaultProjectDomain(t *testing.T) {
	cfg := CloudflareConfig{
		DeployMode: cloudflareModePages,
		Domains:    []CloudflareDomain{{Host: "docs.example.com", Primary: true}},
	}
	got := cloudflarePagesRedirectsFile(cfg)
	want := "https://gcms-docs-example-com.pages.dev/* https://docs.example.com/:splat 301\n"
	if got != want {
		t.Fatalf("redirects file = %q, want %q", got, want)
	}
}

func TestExportStaticSiteWritesRootRSSFromDefaultLocale(t *testing.T) {
	s := newTestPublicServer(t, "")
	result, err := s.exportStaticSite(context.Background(), CloudflareConfig{
		DeployMode:       cloudflareModeWorkerAssets,
		RoutePattern:     "static.example.com/*",
		WorkerName:       "gcms-static-example-com",
		HTMLCacheTTL:     300,
		SourceMode:       cloudflareSourceModeRedirect,
		AutoSync:         true,
		SyncMode:         cloudflareSyncModeRealtime,
		SyncTime:         cloudflareDefaultSyncTime,
		OriginURL:        "https://origin.example.com",
		PagesProjectName: "gcms-static-example-com",
		Domains:          []CloudflareDomain{{Host: "static.example.com", Primary: true}},
	})
	if err != nil {
		t.Fatalf("export static site: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(result.Dir) })
	if f, ok := result.Files["/rss.xml"]; !ok {
		t.Fatalf("root rss was not exported")
	} else if f.Size == 0 {
		t.Fatalf("root rss should not be empty")
	}
}

func TestExportStaticSiteUsesCurrentSiteWhenPlatformHostDiffers(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(cwd, "../.."))
	runDir := filepath.Join(t.TempDir(), "runtime")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("create runtime dir: %v", err)
	}
	t.Chdir(runDir)

	dir := t.TempDir()
	defaultDB := filepath.Join(dir, "cms.db")
	st, err := store.Open(defaultDB)
	if err != nil {
		t.Fatalf("open default store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	uploadDir := filepath.Join(dir, "uploads")
	ps, err := platform.Open(filepath.Join(dir, "system.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	if err := ps.BootstrapDefaultSite(platform.DefaultSiteBootstrap{
		Slug:                        "main",
		Name:                        "Default Site",
		DBPath:                      defaultDB,
		UploadDir:                   uploadDir,
		ManagementAutomationEnabled: true,
	}); err != nil {
		t.Fatalf("bootstrap default site: %v", err)
	}
	srv, err := NewWithPlatform(st, ps, "https://cms.example.test", uploadDir, os.DirFS(repoRoot), os.DirFS(repoRoot))
	if err != nil {
		t.Fatalf("new platform server: %v", err)
	}

	unknownHost := httptest.NewRecorder()
	srv.Handler().ServeHTTP(unknownHost, httptest.NewRequest(http.MethodGet, "https://www.example.test/zh/", nil))
	if unknownHost.Code != http.StatusNotFound {
		t.Fatalf("platform dispatcher status = %d, want 404 for unbound Cloudflare host", unknownHost.Code)
	}

	result, err := srv.exportStaticSite(context.Background(), CloudflareConfig{
		DeployMode:       cloudflareModeWorkerAssets,
		RoutePattern:     "www.example.test/*",
		WorkerName:       "gcms-www-example-test",
		HTMLCacheTTL:     300,
		SourceMode:       cloudflareSourceModeRedirect,
		AutoSync:         true,
		SyncMode:         cloudflareSyncModeRealtime,
		SyncTime:         cloudflareDefaultSyncTime,
		OriginURL:        "https://cms.example.test",
		PagesProjectName: "gcms-www-example-test",
		Domains:          []CloudflareDomain{{Host: "www.example.test", Primary: true}},
	})
	if err != nil {
		t.Fatalf("export static site: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(result.Dir) })
	if f, ok := result.Files["/index.html"]; !ok {
		t.Fatalf("root index was not exported")
	} else if f.Size == 0 {
		t.Fatalf("root index should not be empty")
	}
	if f, ok := result.Files["/zh/index.html"]; !ok {
		t.Fatalf("localized index was not exported")
	} else if f.Size == 0 {
		t.Fatalf("localized index should not be empty")
	}
}

func TestStaticPaginationUsesPrettyPaths(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(homePostsPerPageKey, "2"); err != nil {
		t.Fatalf("set home posts per page: %v", err)
	}
	now := time.Now().UTC()
	postCatID, err := s.store.CreateCategory(&store.Category{
		Slug: "export-post-cat",
		Name: "Export Post Category",
		Lang: "zh",
		Kind: "post",
	})
	if err != nil {
		t.Fatalf("create post category: %v", err)
	}
	for i := 0; i < 9; i++ {
		if _, err := s.store.CreatePost(&store.Post{
			Type:        "post",
			Lang:        "zh",
			Slug:        fmt.Sprintf("export-post-%02d", i),
			Title:       fmt.Sprintf("Export Post %02d", i),
			Content:     "content",
			Status:      "published",
			EditorMode:  "markdown",
			CategoryID:  sql.NullInt64{Int64: postCatID, Valid: true},
			PublishedAt: now.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("create post %d: %v", i, err)
		}
	}
	linkCatID, err := s.store.CreateCategory(&store.Category{
		Slug: "export-link-cat",
		Name: "Export Link Category",
		Lang: "zh",
		Kind: "link",
	})
	if err != nil {
		t.Fatalf("create link category: %v", err)
	}
	for i := 0; i < 13; i++ {
		if _, err := s.store.CreatePost(&store.Post{
			Type:        "link",
			Lang:        "zh",
			Slug:        fmt.Sprintf("export-link-%02d", i),
			Title:       fmt.Sprintf("Export Link %02d", i),
			Excerpt:     "link excerpt",
			Status:      "published",
			EditorMode:  "markdown",
			LinkURL:     fmt.Sprintf("https://example.com/%02d", i),
			CategoryID:  sql.NullInt64{Int64: linkCatID, Valid: true},
			PublishedAt: now.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("create link %d: %v", i, err)
		}
	}

	for _, target := range []string{
		"/zh/page/2/",
		"/zh/category/export-post-cat/page/2/",
		"/zh/links/cat/export-link-cat/",
		"/zh/links/cat/export-link-cat/page/2/",
	} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", target, w.Code, w.Body.String())
		}
	}

	result, err := s.exportStaticSite(context.Background(), CloudflareConfig{
		DeployMode:       cloudflareModeWorkerAssets,
		RoutePattern:     "static.example.com/*",
		WorkerName:       "gcms-static-example-com",
		HTMLCacheTTL:     300,
		SourceMode:       cloudflareSourceModeRedirect,
		AutoSync:         true,
		SyncMode:         cloudflareSyncModeRealtime,
		SyncTime:         cloudflareDefaultSyncTime,
		OriginURL:        "https://origin.example.com",
		PagesProjectName: "gcms-static-example-com",
		Domains:          []CloudflareDomain{{Host: "static.example.com", Primary: true}},
	})
	if err != nil {
		t.Fatalf("export static site: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(result.Dir) })
	for _, want := range []string{
		"/zh/page/2/index.html",
		"/zh/category/export-post-cat/page/2/index.html",
		"/zh/links/cat/export-link-cat/index.html",
		"/zh/links/cat/export-link-cat/page/2/index.html",
	} {
		if _, ok := result.Files[want]; !ok {
			t.Fatalf("static export missing %s", want)
		}
	}

	indexBody, err := os.ReadFile(result.Files["/zh/index.html"].DiskPath)
	if err != nil {
		t.Fatalf("read zh index: %v", err)
	}
	if body := string(indexBody); !strings.Contains(body, `href="/zh/page/2/"`) || strings.Contains(body, `?page=2`) {
		t.Fatalf("home pagination should use pretty static path, body contains query = %v", strings.Contains(body, `?page=2`))
	}
	categoryBody, err := os.ReadFile(result.Files["/zh/category/export-post-cat/index.html"].DiskPath)
	if err != nil {
		t.Fatalf("read category index: %v", err)
	}
	if body := string(categoryBody); !strings.Contains(body, `href="/zh/category/export-post-cat/page/2/"`) || strings.Contains(body, `?page=2`) {
		t.Fatalf("category pagination should use pretty static path, body contains query = %v", strings.Contains(body, `?page=2`))
	}
	linksBody, err := os.ReadFile(result.Files["/zh/links/cat/export-link-cat/index.html"].DiskPath)
	if err != nil {
		t.Fatalf("read links category index: %v", err)
	}
	if body := string(linksBody); !strings.Contains(body, `href="/zh/links/cat/export-link-cat/page/2/"`) {
		t.Fatalf("link category pagination missing pretty path")
	} else if strings.Contains(body, `?cat=export-link-cat`) {
		t.Fatalf("link category navigation should use pretty path")
	} else if strings.Contains(body, `?page=2`) {
		t.Fatalf("link category pagination should not use query page")
	}
}

func TestCloudflareCanonicalFrontendRedirectsOrigin(t *testing.T) {
	t.Chdir(t.TempDir())
	writeCloudflareStatus(CloudflareStatus{
		Status:        "success",
		LastDeployAt:  time.Now().UTC().Format(time.RFC3339),
		PrimaryDomain: "www.example.com",
		Published:     true,
	})
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "http://origin.example.net/zh/posts/demo?utm=1", nil)
	r.Host = "origin.example.net"
	if got, want := s.cloudflareCanonicalFrontendRedirect(r), "https://www.example.com/zh/posts/demo?utm=1"; got != want {
		t.Fatalf("redirect = %q, want %q", got, want)
	}
	r.Host = "www.example.com"
	if got := s.cloudflareCanonicalFrontendRedirect(r); got != "" {
		t.Fatalf("primary host should not redirect, got %q", got)
	}
	r.Host = "origin.example.net"
	r.URL.Path = "/admin"
	if got := s.cloudflareCanonicalFrontendRedirect(r); got != "" {
		t.Fatalf("admin path should not redirect, got %q", got)
	}
	previewReq := httptest.NewRequest(http.MethodGet, "http://origin.example.net/zh/?visual_edit=1", nil)
	previewReq.Host = "origin.example.net"
	previewReq = previewReq.WithContext(withPreviewNoindex(previewReq.Context()))
	if got := s.cloudflareCanonicalFrontendRedirect(previewReq); got != "" {
		t.Fatalf("site preview should not redirect, got %q", got)
	}
}

func TestCloudflareCanonicalFrontendSourceMode(t *testing.T) {
	t.Chdir(t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	writeCloudflareStatus(CloudflareStatus{
		Status:        "success",
		LastDeployAt:  time.Now().UTC().Format(time.RFC3339),
		PrimaryDomain: "www.example.com",
		Published:     true,
	})
	s := &Server{store: st}
	r := httptest.NewRequest(http.MethodGet, "http://origin.example.net/zh/posts/demo", nil)
	r.Host = "origin.example.net"
	if err := st.SetSetting(cloudflareSourceModeKey, cloudflareSourceModeNoindex); err != nil {
		t.Fatalf("set source mode: %v", err)
	}
	if got := s.cloudflareCanonicalFrontendRedirect(r); got != "" {
		t.Fatalf("noindex mode should not redirect, got %q", got)
	}
	if !s.cloudflareCanonicalFrontendNoindex(r) {
		t.Fatal("noindex mode should set noindex for origin frontend")
	}
	if err := st.SetSetting(cloudflareSourceModeKey, cloudflareSourceModeNone); err != nil {
		t.Fatalf("set source mode: %v", err)
	}
	if got := s.cloudflareCanonicalFrontendRedirect(r); got != "" {
		t.Fatalf("none mode should not redirect, got %q", got)
	}
	if s.cloudflareCanonicalFrontendNoindex(r) {
		t.Fatal("none mode should not set noindex")
	}
}

func TestCloudflareStatusStale(t *testing.T) {
	st := &CloudflareStatus{
		Status:    "running",
		UpdatedAt: time.Now().Add(-cloudflareStaleAfter - time.Minute).UTC().Format(time.RFC3339),
	}
	if !cloudflareStatusStale(st) {
		t.Fatal("old running status should be stale")
	}
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if cloudflareStatusStale(st) {
		t.Fatal("fresh running status should not be stale")
	}
}

func TestCloudflareStatusFailedKeepsPreviousStep(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg := CloudflareConfig{
		APIToken:   "token",
		DeployMode: cloudflareModeWorkerAssets,
		WorkerName: "gcms-frontend",
		Domains:    []CloudflareDomain{{Host: "example.com", Primary: true}},
	}
	writeCloudflareStatus(CloudflareStatus{
		Status:    "running",
		Step:      "assets",
		Message:   "uploading assets",
		TokenSet:  true,
		Published: true,
	})
	st := cloudflareStatusFailed(cfg, "failed", "boom")
	if st.Step != "assets" {
		t.Fatalf("failed step = %q, want previous assets step", st.Step)
	}
	if !st.Published {
		t.Fatal("failed status should preserve previous published state")
	}
}

func TestCloudflareRequestDefaultsOnlyInferOrigin(t *testing.T) {
	s := &Server{baseURL: "http://localhost:8080"}
	r := httptest.NewRequest("GET", "http://127.0.0.1/admin/settings/cloudflare", nil)
	r.Host = "cms.example.com"
	r.Header.Set("X-Forwarded-Proto", "https")
	var cfg CloudflareConfig
	s.applyCloudflareRequestDefaults(r, &cfg)
	if cfg.OriginURL != "https://cms.example.com" {
		t.Fatalf("OriginURL = %q, want https://cms.example.com", cfg.OriginURL)
	}
	if cfg.RoutePattern != "" {
		t.Fatalf("RoutePattern = %q, want empty; public entry domain must be user supplied", cfg.RoutePattern)
	}
}

func TestCloudflareWorkerScriptProtectsAdminAndServesAssets(t *testing.T) {
	script := cloudflareWorkerScript()
	for _, needle := range []string{
		`"/admin"`,
		`"/api/admin"`,
		`"/preview"`,
		`const DEFAULT_LANG = "zh";`,
		`Accept-Language`,
		`localeRedirect`,
		`headers.Vary = "Accept-Language";`,
		`typeof env.ASSETS.fetch`,
		`status: 503`,
		`env.ASSETS.fetch`,
		`/cat/`,
		`/page/`,
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("worker script should contain %s", needle)
		}
	}
}

func TestCloudflareWorkerScriptUsesLocaleConfig(t *testing.T) {
	cfg := CloudflareConfig{
		DefaultLang: "en",
		Locales:     []string{"en", "zh"},
	}
	script := cloudflareWorkerScriptForConfig(cfg)
	for _, needle := range []string{
		`const DEFAULT_LANG = "en";`,
		`const LOCALES = ["en","zh"];`,
		`next.pathname = "/" + preferredLanguage(request.headers.get("Accept-Language")) + "/";`,
		`next.pathname = "/" + DEFAULT_LANG + (pathname.startsWith("/") ? pathname : "/" + pathname);`,
		`return makeRedirect(langRedirect.url, 302, langRedirect.varyLanguage);`,
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("worker script should contain %s", needle)
		}
	}
}

func TestCloudflareWorkerScriptRedirectsAliasHosts(t *testing.T) {
	cfg := CloudflareConfig{Domains: []CloudflareDomain{
		{Host: "ccvar.com", Primary: true},
		{Host: "www.ccvar.com", RedirectToPrimary: true},
	}}
	script := cloudflareWorkerScriptForConfig(cfg)
	for _, needle := range []string{
		`const PRIMARY_HOST = "ccvar.com";`,
		`const REDIRECT_HOSTS = new Set(["www.ccvar.com"]);`,
		`const PUBLIC_HOSTS = new Set(["ccvar.com","www.ccvar.com"]);`,
		`!REDIRECT_HOSTS.has(host) && PUBLIC_HOSTS.has(host)`,
		`return makeRedirect(redirectURL, 301, false);`,
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("worker script should contain %s", needle)
		}
	}
}

func TestCloudflareWorkerUploadMetadataIncludesAssetsBinding(t *testing.T) {
	var metadata map[string]any
	if err := json.Unmarshal(cloudflareWorkerUploadMetadata("jwt_123"), &metadata); err != nil {
		t.Fatalf("metadata should be valid json: %v", err)
	}
	if metadata["main_module"] != "worker.js" {
		t.Fatalf("main_module = %v, want worker.js", metadata["main_module"])
	}
	assets, ok := metadata["assets"].(map[string]any)
	if !ok {
		t.Fatalf("assets metadata missing: %#v", metadata["assets"])
	}
	if assets["jwt"] != "jwt_123" {
		t.Fatalf("assets jwt = %v", assets["jwt"])
	}
	bindings, ok := metadata["bindings"].([]any)
	if !ok || len(bindings) != 1 {
		t.Fatalf("bindings = %#v, want one assets binding", metadata["bindings"])
	}
	binding, ok := bindings[0].(map[string]any)
	if !ok || binding["name"] != "ASSETS" || binding["type"] != "assets" {
		t.Fatalf("binding = %#v, want ASSETS assets binding", bindings[0])
	}
}

func TestCloudflareAPIErrorCodeDetection(t *testing.T) {
	err := cloudflareAPIError{
		Status: 403,
		Errors: []cloudflareErr{
			{Code: 10000, Message: "Authentication error"},
		},
	}
	if !cloudflareHasErrorCode(err, 10000) {
		t.Fatal("Cloudflare error code 10000 should be detectable")
	}
	if cloudflareHasErrorCode(err, 7003) {
		t.Fatal("unexpected Cloudflare error code match")
	}
	if !strings.Contains(err.Error(), "10000 Authentication error") {
		t.Fatalf("error message should keep original code: %q", err.Error())
	}
}

func TestCloudflareStagePermissionErrorForWorkerUpload(t *testing.T) {
	baseErr := cloudflareAPIError{
		Status: 403,
		Errors: []cloudflareErr{
			{Code: 10000, Message: "Authentication error"},
		},
	}
	err := cloudflareStagePermissionError("worker", baseErr)
	msg := err.Error()
	for _, needle := range []string{"Workers Scripts Edit", "Account Resources", "原始错误", "10000 Authentication error"} {
		if !strings.Contains(msg, needle) {
			t.Fatalf("worker upload permission error %q should contain %q", msg, needle)
		}
	}
}

func TestCloudflareAPITokenTemplateURL(t *testing.T) {
	raw := cloudflareAPITokenTemplateURL("")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("token template URL should parse: %v", err)
	}
	if u.Scheme != "https" || u.Host != "dash.cloudflare.com" || u.Path != "/profile/api-tokens" {
		t.Fatalf("unexpected token template URL: %s", raw)
	}
	q := u.Query()
	if q.Get("accountId") != "*" || q.Get("zoneId") != "all" {
		t.Fatalf("unexpected token scope: %s", raw)
	}
	if q.Get("name") != cloudflareDefaultAPITokenName {
		t.Fatalf("default token name = %q, want %q", q.Get("name"), cloudflareDefaultAPITokenName)
	}
	for _, needle := range []string{
		`"key":"workers_scripts"`,
		`"key":"workers_routes"`,
		`"key":"page"`,
		`"key":"dns"`,
		`"key":"cache"`,
		`"key":"zone"`,
		`"key":"account_settings"`,
	} {
		if !strings.Contains(q.Get("permissionGroupKeys"), needle) {
			t.Fatalf("permissionGroupKeys should contain %s: %s", needle, q.Get("permissionGroupKeys"))
		}
	}
	projectURL := cloudflareAPITokenTemplateURL("gcms-test-ccvar-com")
	projectParsed, err := url.Parse(projectURL)
	if err != nil {
		t.Fatalf("project token template URL should parse: %v", err)
	}
	if got := projectParsed.Query().Get("name"); got != "gcms-test-ccvar-com" {
		t.Fatalf("project token name = %q, want gcms-test-ccvar-com", got)
	}
}

func TestCloudflareConfigConfiguredWithAPITokenOnly(t *testing.T) {
	cfg := CloudflareConfig{
		APIToken:   "token",
		DeployMode: cloudflareModeWorkerAssets,
		WorkerName: "gcms-frontend",
		Domains:    []CloudflareDomain{{Host: "example.com", Primary: true}},
	}
	if !cfg.configured() {
		t.Fatal("API token plus worker/route should be enough before auto detection")
	}
	if err := cfg.validateDeploy(); err != nil {
		t.Fatalf("validateDeploy returned %v", err)
	}
}

func TestCloudflareConfigConfiguredWithPagesMode(t *testing.T) {
	cfg := CloudflareConfig{
		APIToken:         "token",
		DeployMode:       cloudflareModePages,
		PagesProjectName: "gcms-frontend",
		Domains:          []CloudflareDomain{{Host: "example.com", Primary: true}},
	}
	if !cfg.configured() {
		t.Fatal("API token plus Pages project and public domain should configure Pages deploy")
	}
	if err := cfg.validateDeploy(); err != nil {
		t.Fatalf("validateDeploy returned %v", err)
	}
	cfg.PagesProjectName = ""
	if cfg.configured() {
		t.Fatal("Pages mode should require a Pages project name")
	}
	if err := cfg.validateDeploy(); err == nil || !strings.Contains(err.Error(), "Pages") {
		t.Fatalf("validateDeploy should explain missing Pages project, got %v", err)
	}
}

func TestNormalizeCloudflareDeployModeDefaultsToWorkerAssets(t *testing.T) {
	if got := normalizeCloudflareDeployMode(""); got != cloudflareModeWorkerAssets {
		t.Fatalf("default deploy mode = %q, want %q", got, cloudflareModeWorkerAssets)
	}
	if got := normalizeCloudflareDeployMode("pages"); got != cloudflareModePages {
		t.Fatalf("pages deploy mode = %q, want %q", got, cloudflareModePages)
	}
}

func TestRecommendedCloudflareTokenFormClearsDetectedIDs(t *testing.T) {
	stubCloudflareTokenVerify(t, func(context.Context, string) error { return nil })
	st, err := store.Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for key, value := range map[string]string{
		cloudflareAccountIDKey:   "old_account",
		cloudflareAccountNameKey: "Old Account",
		cloudflareZoneIDKey:      "old_zone",
		cloudflareZoneNameKey:    "Old Zone",
		cloudflareAPITokenKey:    "old_token",
	} {
		if err := st.SetSetting(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	s := &Server{store: st, baseURL: "https://origin.example.com"}
	form := url.Values{
		"deploy":         {"1"},
		"worker_name":    {"gcms-frontend"},
		"origin_url":     {"https://origin.example.com"},
		"route_pattern":  {"test.ccvar.com/*"},
		"html_cache_ttl": {"300"},
		"auto_sync":      {"1"},
		"api_token":      {"new_token"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/settings/cloudflare", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cfg, err := s.saveCloudflareConfigFromRequest(req)
	if err != nil {
		t.Fatalf("saveCloudflareConfigFromRequest returned %v", err)
	}
	if cfg.AccountID != "" || cfg.ZoneID != "" || cfg.AccountName != "" || cfg.ZoneName != "" {
		t.Fatalf("recommended form should clear stale detected IDs, got %+v", cfg)
	}
	for _, key := range []string{cloudflareAccountIDKey, cloudflareAccountNameKey, cloudflareZoneIDKey, cloudflareZoneNameKey} {
		if got := st.Setting(key); got != "" {
			t.Fatalf("%s should be cleared, got %q", key, got)
		}
	}
	if got := st.Setting(cloudflareAPITokenKey); got != "new_token" {
		t.Fatalf("api token = %q, want new_token", got)
	}
}

func TestRecommendedCloudflareFormDefaultsProjectFromPrimaryDomain(t *testing.T) {
	stubCloudflareTokenVerify(t, func(context.Context, string) error { return nil })
	st, err := store.Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Server{store: st, baseURL: "https://origin.example.com"}
	form := url.Values{
		"deploy":         {"1"},
		"project_custom": {"0"},
		"deploy_mode":    {cloudflareModeWorkerAssets},
		"primary_domain": {"test.ccvar.com"},
		"html_cache_ttl": {"300"},
		"auto_sync":      {"1"},
		"api_token":      {"new_token"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/settings/cloudflare", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cfg, err := s.saveCloudflareConfigFromRequest(req)
	if err != nil {
		t.Fatalf("saveCloudflareConfigFromRequest returned %v", err)
	}
	if cfg.WorkerName != "gcms-test-ccvar-com" || cfg.PagesProjectName != "gcms-test-ccvar-com" {
		t.Fatalf("project names = %q/%q, want gcms-test-ccvar-com", cfg.WorkerName, cfg.PagesProjectName)
	}
}

func TestSaveCloudflareConfigRejectsInvalidToken(t *testing.T) {
	stubCloudflareTokenVerify(t, func(context.Context, string) error { return errors.New("token inactive") })
	st, err := store.Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SetSetting(cloudflareAPITokenKey, "old_token"); err != nil {
		t.Fatalf("set old token: %v", err)
	}
	s := &Server{store: st, baseURL: "https://origin.example.com"}
	form := url.Values{
		"deploy_mode":    {cloudflareModeWorkerAssets},
		"primary_domain": {"test.ccvar.com"},
		"html_cache_ttl": {"300"},
		"auto_sync":      {"1"},
		"api_token":      {"bad_token"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/settings/cloudflare", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := s.saveCloudflareConfigFromRequest(req); err == nil {
		t.Fatal("saveCloudflareConfigFromRequest should reject invalid token")
	}
	if got := st.Setting(cloudflareAPITokenKey); got != "old_token" {
		t.Fatalf("api token = %q, want old_token", got)
	}
}

func TestSaveCloudflareConfigVerifiesExistingTokenWhenRequested(t *testing.T) {
	var verifiedToken string
	stubCloudflareTokenVerify(t, func(_ context.Context, token string) error {
		verifiedToken = token
		return nil
	})
	st, err := store.Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SetSetting(cloudflareAPITokenKey, "old_token"); err != nil {
		t.Fatalf("set old token: %v", err)
	}
	s := &Server{store: st, baseURL: "https://origin.example.com"}
	form := url.Values{
		"deploy_mode":    {cloudflareModeWorkerAssets},
		"primary_domain": {"test.ccvar.com"},
		"verify_token":   {"1"},
		"html_cache_ttl": {"300"},
		"auto_sync":      {"1"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/settings/cloudflare", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := s.saveCloudflareConfigFromRequest(req); err != nil {
		t.Fatalf("saveCloudflareConfigFromRequest returned %v", err)
	}
	if verifiedToken != "old_token" {
		t.Fatalf("verified token = %q, want old_token", verifiedToken)
	}
}

func TestCloudflareResetClearsProjectNames(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := &Server{store: st}
	for key, value := range map[string]string{
		cloudflareAPITokenKey:     "token",
		cloudflareDeployModeKey:   cloudflareModePages,
		cloudflareWorkerNameKey:   "gcms-old-worker",
		cloudflarePagesProjectKey: "gcms-old-pages",
		cloudflareDomainsKey:      `[{"host":"example.com","primary":true}]`,
		cloudflareSourceModeKey:   cloudflareSourceModeNoindex,
	} {
		if err := st.SetSetting(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	if err := s.clearCloudflareBinding(); err != nil {
		t.Fatalf("clearCloudflareBinding returned %v", err)
	}
	for _, key := range []string{cloudflareAPITokenKey, cloudflareDeployModeKey, cloudflareWorkerNameKey, cloudflarePagesProjectKey, cloudflareDomainsKey, cloudflareSourceModeKey} {
		if got := st.Setting(key); got != "" {
			t.Fatalf("%s after reset = %q, want empty", key, got)
		}
	}
	view := s.cloudflareView()
	if view.TokenSet {
		t.Fatal("view.TokenSet = true after reset, want false")
	}
	if view.ProjectName != "" || view.ProjectDefault != "" {
		t.Fatalf("project after reset = %q/%q, want empty", view.ProjectName, view.ProjectDefault)
	}
	clientView := cloudflareClientViewFromView(view)
	if clientView.ProjectName != "" || clientView.ProjectDefault != "" {
		t.Fatalf("client project after reset = %q/%q, want empty", clientView.ProjectName, clientView.ProjectDefault)
	}
}

func TestCloudflareRouteHostAndZoneMatch(t *testing.T) {
	if got := cloudflareRouteHost("www.example.com/*"); got != "www.example.com" {
		t.Fatalf("cloudflareRouteHost returned %q", got)
	}
	zones := []cloudflareZone{
		{ID: "zone_a", Name: "example.net"},
		{ID: "zone_b", Name: "example.com", Account: cloudflareAccount{ID: "acct", Name: "Main"}},
	}
	got := matchCloudflareZone("www.example.com", zones)
	if got.ID != "zone_b" || got.Account.ID != "acct" {
		t.Fatalf("unexpected matched zone: %+v", got)
	}
}

func TestCloudflareZoneNameCandidates(t *testing.T) {
	got := cloudflareZoneNameCandidates("test.ccvar.com")
	want := []string{"test.ccvar.com", "ccvar.com"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("cloudflareZoneNameCandidates returned %#v, want %#v", got, want)
	}
	if likely := cloudflareLikelyZoneName("test.ccvar.com"); likely != "ccvar.com" {
		t.Fatalf("cloudflareLikelyZoneName returned %q, want ccvar.com", likely)
	}
}

func TestCloudflareZoneDetectErrorIncludesLikelyZone(t *testing.T) {
	err := cloudflareZoneDetectError("test.ccvar.com/*", nil)
	msg := err.Error()
	for _, needle := range []string{"test.ccvar.com", "ccvar.com", "Zone Read"} {
		if !strings.Contains(msg, needle) {
			t.Fatalf("error %q should contain %q", msg, needle)
		}
	}
}

func TestCloudflareClientViewIsRedactedAndOnlyLinksPublishedSite(t *testing.T) {
	cfg := CloudflareConfig{
		APIToken:   "cfut_secret_token_value",
		DeployMode: cloudflareModeWorkerAssets,
		WorkerName: "gcms-frontend",
		Domains:    []CloudflareDomain{{Host: "example.com", Primary: true}},
		ZoneID:     "zone_123",
	}
	view := &CloudflareView{
		Config:     cfg,
		Status:     &CloudflareStatus{Status: "idle", TokenSet: true},
		TokenSet:   true,
		Configured: true,
	}
	view.decorate()
	clientView := cloudflareClientViewFromView(view)
	if clientView.PublicURL != "" {
		t.Fatalf("unpublished client view should not expose public URL, got %q", clientView.PublicURL)
	}
	if clientView.TokenFingerprint == "" || strings.Contains(clientView.TokenFingerprint, cfg.APIToken) {
		t.Fatalf("token fingerprint should be redacted, got %q", clientView.TokenFingerprint)
	}
	if clientView.CanUnpublish || clientView.CanPurge {
		t.Fatalf("unpublished site should not expose destructive actions: %+v", clientView)
	}

	view.Status.Status = "success"
	view.Status.LastDeployAt = time.Now().UTC().Format(time.RFC3339)
	view.Status.Published = true
	clientView = cloudflareClientViewFromView(view)
	if clientView.PublicURL != "https://example.com" {
		t.Fatalf("published client view PublicURL = %q", clientView.PublicURL)
	}
	if !clientView.CanUnpublish || !clientView.CanPurge {
		t.Fatalf("published site should expose deployment actions: %+v", clientView)
	}
}
