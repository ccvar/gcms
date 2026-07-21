package web

import (
	"bytes"
	"encoding/json"
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

// setupPlatformAutomation 起一个双站点平台服务器（default + blog，均开启自动化），返回句柄与站点。
func setupPlatformAutomation(t *testing.T) (*Server, http.Handler, *platform.Store, *platform.Site, *platform.Site) {
	return newPlatformTestServerBase(t, "https://platform.test")
}

// newPlatformTestServerBase 同上，但可指定 baseURL（本地 base 便于让真实 node CLI 打到 httptest.Server）。
func newPlatformTestServerBase(t *testing.T, baseURL string) (*Server, http.Handler, *platform.Store, *platform.Site, *platform.Site) {
	t.Helper()
	dir := t.TempDir()

	defaultDB := filepath.Join(dir, "default.db")
	defaultStore, err := store.Open(defaultDB)
	if err != nil {
		t.Fatalf("open default store: %v", err)
	}
	t.Cleanup(func() { _ = defaultStore.Close() })
	if err := defaultStore.SetSetting("site.name", "Default Site"); err != nil {
		t.Fatalf("set default name: %v", err)
	}
	defaultUploadDir := filepath.Join(dir, "default-uploads")
	if err := os.MkdirAll(defaultUploadDir, 0o755); err != nil {
		t.Fatalf("mk default uploads: %v", err)
	}

	// 第二个站点：先建好 DB 文件（运行时会打开它）。
	blogDB := filepath.Join(dir, "blog.db")
	blogStore, err := store.Open(blogDB)
	if err != nil {
		t.Fatalf("open blog store: %v", err)
	}
	_ = blogStore.SetSetting("site.name", "Blog Site")
	if err := blogStore.Close(); err != nil {
		t.Fatalf("close blog store: %v", err)
	}
	blogUploadDir := filepath.Join(dir, "blog-uploads")
	if err := os.MkdirAll(blogUploadDir, 0o755); err != nil {
		t.Fatalf("mk blog uploads: %v", err)
	}

	ps, err := platform.Open(filepath.Join(dir, "system.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	hash, err := bcrypt.GenerateFromPassword([]byte(store.DefaultAdminPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := ps.BootstrapDefaultSite(platform.DefaultSiteBootstrap{
		Slug: "main", Name: "Default Site", DBPath: defaultDB, UploadDir: defaultUploadDir,
		AdminUser: "admin", AdminPasswordHash: string(hash), ManagementAutomationEnabled: true,
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defaultSite, err := ps.DefaultSite()
	if err != nil {
		t.Fatalf("default site: %v", err)
	}
	blogSite, err := ps.CreateSite("blog", "Blog Site", blogDB, blogUploadDir, true)
	if err != nil {
		t.Fatalf("create blog site: %v", err)
	}
	// 给 blog 站绑一个前台域名，便于用例访问其公共页面（如主题试穿的缓存不污染断言）。
	if err := ps.AddSiteDomain(blogSite.ID, "https", "blog.test", true, true); err != nil {
		t.Fatalf("add blog domain: %v", err)
	}

	srv, err := NewWithPlatform(defaultStore, ps, baseURL, defaultUploadDir, os.DirFS("../.."), os.DirFS("../.."))
	if err != nil {
		t.Fatalf("new platform server: %v", err)
	}
	return srv, srv.Handler(), ps, defaultSite, blogSite
}

func platformAPIReq(t *testing.T, h http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, "https://platform.test"+path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, "https://platform.test"+path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// TestPlatformMirrorSimilarAndStatsPages 平台镜像路由：/{collection}/similar 与 /stats/pages
// 必须命中各自处理器，不被 {collection}/{id} 通配吞掉（本仓库踩过的坑，钉住）。
func TestPlatformMirrorSimilarAndStatsPages(t *testing.T) {
	_, h, ps, _, blogSite := setupPlatformAutomation(t)
	token := "gcmsp_mirror1234567890"
	if _, err := ps.CreatePlatformKey("mirror", token, token[:13], platform.KeyMembershipAll,
		"posts:read,stats:read", nil, time.Time{}); err != nil {
		t.Fatalf("create key: %v", err)
	}
	prefix := "/api/platform/v1/sites/" + strconv.FormatInt(blogSite.ID, 10)

	rec := platformAPIReq(t, h, http.MethodGet, prefix+"/posts/similar?title=%E5%86%85%E5%AE%B9%E7%AE%A1%E7%90%86", token, nil)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	// 命中查重处理器 → 200 {ok:true}；被 {id} 通配吞掉会是 400 bad_id。
	if rec.Code != http.StatusOK || out["ok"] != true {
		t.Fatalf("platform similar = %d %v, want 200 ok", rec.Code, out)
	}

	rec2 := platformAPIReq(t, h, http.MethodGet, prefix+"/stats/pages", token, nil)
	var out2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &out2)
	// 站点未接 GA → 命中统计处理器返回 400 analytics_not_connected；
	// 被 {collection} 通配接走会是 404 未知集合。
	if rec2.Code != http.StatusBadRequest || out2["error"] != "analytics_not_connected" {
		t.Fatalf("platform stats/pages = %d %v, want 400 analytics_not_connected", rec2.Code, out2)
	}
}

// TestPlatformMirrorThemeOptions 平台镜像：/sites/{id}/theme-options 命中主题配置槽处理器
// （scope 与 site-profile 读口径一致 = site:read），不被 {collection} 通配吞掉。
func TestPlatformMirrorThemeOptions(t *testing.T) {
	_, h, ps, _, blogSite := setupPlatformAutomation(t)
	token := "gcmsp_themeopts123456"
	if _, err := ps.CreatePlatformKey("opts", token, token[:13], platform.KeyMembershipAll,
		"site:read", nil, time.Time{}); err != nil {
		t.Fatalf("create key: %v", err)
	}
	rec := platformAPIReq(t, h, http.MethodGet, "/api/platform/v1/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/theme-options", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("platform theme-options = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Theme  string `json:"theme"`
		Layout string `json:"layout"`
		Slots  []struct {
			Key string `json:"key"`
		} `json:"slots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Theme == "" || out.Layout == "" || len(out.Slots) == 0 || out.Slots[0].Key != "hero.visual" {
		t.Fatalf("platform theme-options body 契约不符：%s", rec.Body.String())
	}
}

func TestPlatformKeyDispatchMembership(t *testing.T) {
	_, h, ps, defaultSite, blogSite := setupPlatformAutomation(t)

	// allowlist 密钥，仅授权 blog。
	token := "gcmsp_dispatch1234567"
	if _, err := ps.CreatePlatformKey("bot", token, token[:13], platform.KeyMembershipAllowlist,
		"posts:read,posts:write,languages:read", []int64{blogSite.ID}, time.Time{}); err != nil {
		t.Fatalf("create platform key: %v", err)
	}

	// 授权站点 → 200。
	ok := platformAPIReq(t, h, http.MethodGet, "/api/platform/v1/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/languages", token, nil)
	if ok.Code != http.StatusOK {
		t.Fatalf("allowed site status = %d, body = %s", ok.Code, ok.Body.String())
	}

	// 非授权站点 → 403 site_forbidden。
	forbidden := platformAPIReq(t, h, http.MethodGet, "/api/platform/v1/sites/"+strconv.FormatInt(defaultSite.ID, 10)+"/languages", token, nil)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("forbidden site status = %d, body = %s", forbidden.Code, forbidden.Body.String())
	}

	// 无效 token（非平台密钥且非站点密钥）→ 回退站点路径后 401。
	bad := platformAPIReq(t, h, http.MethodGet, "/api/platform/v1/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/languages", "gcmsp_not_a_real_key", nil)
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d, want 401, body = %s", bad.Code, bad.Body.String())
	}
}

func TestPlatformKeyDiscoveryContract(t *testing.T) {
	srv, h, ps, _, blogSite := setupPlatformAutomation(t)
	token := "gcmsp_discovery123456"
	if _, err := ps.CreatePlatformKey("bot", token, token[:13], platform.KeyMembershipAllowlist,
		"posts:read", []int64{blogSite.ID}, time.Time{}); err != nil {
		t.Fatalf("create key: %v", err)
	}

	// 缺 token → 401。
	if noTok := platformAPIReq(t, h, http.MethodGet, "/api/platform/v1/sites", "", nil); noTok.Code != http.StatusUnauthorized {
		t.Fatalf("discovery without token status = %d, want 401", noTok.Code)
	}

	rec := platformAPIReq(t, h, http.MethodGet, "/api/platform/v1/sites", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("discovery status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []struct {
			ID           int64    `json:"id"`
			Slug         string   `json:"slug"`
			Name         string   `json:"name"`
			Capabilities []string `json:"capabilities"`
			APIBase      string   `json:"api_base"`
			URL          string   `json:"url"`
			Logo         string   `json:"logo"`
			Favicon      string   `json:"favicon"`
			Readiness    *struct {
				PublicURL        bool `json:"public_url"`
				HTTPS            bool `json:"https"`
				Logo             bool `json:"logo"`
				Favicon          bool `json:"favicon"`
				ShareImage       bool `json:"share_image"`
				PublishedContent bool `json:"published_content"`
			} `json:"readiness"`
		} `json:"items"`
		AllSites bool `json:"all_sites"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal discovery: %v (body=%s)", err, rec.Body.String())
	}
	if payload.AllSites {
		t.Fatalf("all_sites should be false for allowlist key")
	}
	if len(payload.Items) != 1 {
		t.Fatalf("discovery items = %d, want 1 (blog only): %s", len(payload.Items), rec.Body.String())
	}
	it := payload.Items[0]
	if it.ID != blogSite.ID || it.Slug != "blog" {
		t.Fatalf("discovery item mismatch: %#v", it)
	}
	if len(it.Capabilities) == 0 || it.Capabilities[0] != "posts:read" {
		t.Fatalf("discovery capabilities = %v, want posts:read", it.Capabilities)
	}
	wantBase := "https://platform.test/api/platform/v1/sites/" + strconv.FormatInt(blogSite.ID, 10)
	if it.APIBase != wantBase {
		t.Fatalf("discovery api_base = %q, want %q", it.APIBase, wantBase)
	}
	// 附加展示字段：blog 站绑定了 blog.test 域名 → url 是它的公开地址；未设置 Logo → 空。
	if it.URL != "https://blog.test" {
		t.Fatalf("discovery url = %q, want https://blog.test", it.URL)
	}
	if it.Logo != "" {
		t.Fatalf("discovery logo should be empty when site.logo unset, got %q", it.Logo)
	}
	if it.Readiness == nil {
		t.Fatal("discovery readiness should be present")
	}
	if !it.Readiness.PublicURL || !it.Readiness.HTTPS {
		t.Fatalf("discovery readiness should reflect the bound HTTPS domain: %#v", it.Readiness)
	}
	if it.Readiness.Logo || it.Readiness.Favicon || it.Readiness.ShareImage {
		t.Fatalf("built-in or empty brand assets must remain pending: %#v", it.Readiness)
	}
	if !it.Readiness.PublishedContent {
		t.Fatalf("published fixture content should be reflected in readiness: %#v", it.Readiness)
	}

	// 未部署的新站没有公开域名，但只要真实 Logo / favicon 已写入并能由站点使用，
	// readiness 就必须判定完成，Pilot 也必须拿到可直接展示的受控内联图片。
	rt, ok := srv.runtimePool().runtimeByID(blogSite.ID)
	if !ok || rt == nil || rt.Store == nil {
		t.Fatal("blog runtime should be available")
	}
	brandSVG := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32" fill="#1677ff"/></svg>`)
	if err := os.WriteFile(filepath.Join(rt.UploadDir, "brand.svg"), brandSVG, 0o644); err != nil {
		t.Fatalf("write test brand: %v", err)
	}
	// ICO 是当前新站向导实际上传的格式；发现接口只负责受控传输，不解析或改写图片内容。
	if err := os.WriteFile(filepath.Join(rt.UploadDir, "favicon.ico"), []byte{0x00, 0x00, 0x01, 0x00, 0x00, 0x00}, 0o644); err != nil {
		t.Fatalf("write test favicon: %v", err)
	}
	if err := rt.Store.SetSetting("site.logo", "/uploads/brand.svg"); err != nil {
		t.Fatalf("set test logo: %v", err)
	}
	if err := rt.Store.SetSetting("site.favicon", "/uploads/favicon.ico"); err != nil {
		t.Fatalf("set test favicon: %v", err)
	}
	if err := ps.ReplaceSiteDomains(blogSite.ID, nil); err != nil {
		t.Fatalf("clear blog domains: %v", err)
	}

	rec = platformAPIReq(t, h, http.MethodGet, "/api/platform/v1/sites", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("discovery without public domain status = %d, body = %s", rec.Code, rec.Body.String())
	}
	payload = struct {
		Items []struct {
			ID           int64    `json:"id"`
			Slug         string   `json:"slug"`
			Name         string   `json:"name"`
			Capabilities []string `json:"capabilities"`
			APIBase      string   `json:"api_base"`
			URL          string   `json:"url"`
			Logo         string   `json:"logo"`
			Favicon      string   `json:"favicon"`
			Readiness    *struct {
				PublicURL        bool `json:"public_url"`
				HTTPS            bool `json:"https"`
				Logo             bool `json:"logo"`
				Favicon          bool `json:"favicon"`
				ShareImage       bool `json:"share_image"`
				PublishedContent bool `json:"published_content"`
			} `json:"readiness"`
		} `json:"items"`
		AllSites bool `json:"all_sites"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal undeployed discovery: %v (body=%s)", err, rec.Body.String())
	}
	if len(payload.Items) != 1 {
		t.Fatalf("undeployed discovery items = %d, want 1", len(payload.Items))
	}
	it = payload.Items[0]
	if it.URL != "" {
		t.Fatalf("undeployed discovery url = %q, want empty", it.URL)
	}
	if !strings.HasPrefix(it.Logo, "data:image/svg+xml;base64,") {
		t.Fatalf("undeployed logo should be an inline image, got %q", it.Logo)
	}
	if !strings.HasPrefix(it.Favicon, "data:image/x-icon;base64,") {
		t.Fatalf("undeployed favicon should be an inline image, got %q", it.Favicon)
	}
	if it.Readiness == nil || !it.Readiness.Logo || !it.Readiness.Favicon {
		t.Fatalf("uploaded brand assets must be ready without a public domain: %#v", it.Readiness)
	}
	if it.Readiness.PublicURL || it.Readiness.HTTPS {
		t.Fatalf("domain readiness must remain pending for undeployed site: %#v", it.Readiness)
	}
}

func TestPlatformKeyAuditRouting(t *testing.T) {
	_, h, ps, _, blogSite := setupPlatformAutomation(t)
	token := "gcmsp_audit1234567890"
	if _, err := ps.CreatePlatformKey("bot", token, token[:13], platform.KeyMembershipAll,
		"posts:read,posts:write", nil, time.Time{}); err != nil {
		t.Fatalf("create key: %v", err)
	}
	body, _ := json.Marshal(map[string]any{"title": "Platform Draft", "lang": "zh", "status": "draft"})
	rec := platformAPIReq(t, h, http.MethodPost, "/api/platform/v1/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/posts", token, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create post status = %d, body = %s", rec.Code, rec.Body.String())
	}
	// 审计必须写入平台库（红队 FK 修复：绝不向站库 FK 列写 sentinel）。
	logs, err := ps.ListPlatformAutomationLogs(50)
	if err != nil {
		t.Fatalf("list platform logs: %v", err)
	}
	if len(logs) == 0 {
		t.Fatalf("platform action produced no audit row")
	}
	found := false
	for _, l := range logs {
		if l.SiteID == blogSite.ID && l.Action == "create" && l.KeyID > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no platform_automation_logs row for the create on blog site: %#v", logs)
	}
}

func TestPlatformKeyGlobalKillSwitch(t *testing.T) {
	srv, h, ps, _, blogSite := setupPlatformAutomation(t)
	token := "gcmsp_kill12345678901"
	if _, err := ps.CreatePlatformKey("bot", token, token[:13], platform.KeyMembershipAll,
		"languages:read", nil, time.Time{}); err != nil {
		t.Fatalf("create key: %v", err)
	}
	path := "/api/platform/v1/sites/" + strconv.FormatInt(blogSite.ID, 10) + "/languages"
	if ok := platformAPIReq(t, h, http.MethodGet, path, token, nil); ok.Code != http.StatusOK {
		t.Fatalf("before kill switch status = %d", ok.Code)
	}
	// 全局急停。
	if err := ps.SetSetting(platformAutomationKillSwitchKey, "1"); err != nil {
		t.Fatalf("set kill switch: %v", err)
	}
	if !srv.platformAutomationKilled() {
		t.Fatalf("kill switch not detected")
	}
	if killed := platformAPIReq(t, h, http.MethodGet, path, token, nil); killed.Code != http.StatusForbidden {
		t.Fatalf("after kill switch status = %d, want 403, body = %s", killed.Code, killed.Body.String())
	}
	// 发现端点也应急停。
	if disc := platformAPIReq(t, h, http.MethodGet, "/api/platform/v1/sites", token, nil); disc.Code != http.StatusForbidden {
		t.Fatalf("discovery after kill switch status = %d, want 403", disc.Code)
	}
}
