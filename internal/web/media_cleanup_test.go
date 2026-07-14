package web

import (
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

func writeUploadFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// mediaCleanupFixture 三个站点的平台服务器：默认站与 blog 站各有被引用文件和孤儿文件；
// shop 站不自绑域名、走 Cloudflare 部署域名回退。
type mediaCleanupFixture struct {
	srv           *Server
	h             http.Handler
	cookie        *http.Cookie
	csrf          string
	defaultDir    string
	blogDir       string
	blogSiteID    int64
	defaultSiteID int64
	shopSiteID    int64
}

func newMediaCleanupFixture(t *testing.T) *mediaCleanupFixture {
	t.Helper()
	dir := t.TempDir()

	defaultStore, err := store.Open(filepath.Join(dir, "default.db"))
	if err != nil {
		t.Fatalf("open default store: %v", err)
	}
	t.Cleanup(func() { _ = defaultStore.Close() })
	defaultUploadDir := filepath.Join(dir, "default-uploads")
	if err := os.MkdirAll(defaultUploadDir, 0o755); err != nil {
		t.Fatalf("create default upload dir: %v", err)
	}
	writeUploadFile(t, defaultUploadDir, "ref-content.webp", "content-ref")
	writeUploadFile(t, defaultUploadDir, "ref-cover.webp", "cover-ref")
	writeUploadFile(t, defaultUploadDir, "ref-extra.webp", "extra-ref")
	writeUploadFile(t, defaultUploadDir, "ref-setting.webp", "setting-ref")
	writeUploadFile(t, defaultUploadDir, "orphan-a.webp", "orphan-a-data")
	// 草稿正文引用 + 封面引用 + extra JSON 引用都必须算作被引用。
	if _, err := defaultStore.CreatePost(&store.Post{
		Type:       "post",
		Lang:       "zh",
		Slug:       "media-cleanup-ref",
		Title:      "引用扫描测试",
		Content:    "正文里有一张图 ![img](/uploads/ref-content.webp) 。",
		CoverImage: "/uploads/ref-cover.webp",
		Extra:      `{"gallery":["/uploads/ref-extra.webp"]}`,
		Status:     "draft",
	}); err != nil {
		t.Fatalf("create default post: %v", err)
	}
	// settings 表 value 引用（Logo/favicon/分享图等都存在这里）。
	if err := defaultStore.SetSetting("site.logo", "/uploads/ref-setting.webp"); err != nil {
		t.Fatalf("set default logo: %v", err)
	}

	blogDB := filepath.Join(dir, "blog.db")
	blogStore, err := store.Open(blogDB)
	if err != nil {
		t.Fatalf("open blog store: %v", err)
	}
	blogUploadDir := filepath.Join(dir, "blog-uploads")
	if err := os.MkdirAll(blogUploadDir, 0o755); err != nil {
		t.Fatalf("create blog upload dir: %v", err)
	}
	writeUploadFile(t, blogUploadDir, "blog-keep.webp", "blog-keep")
	writeUploadFile(t, blogUploadDir, "blog-orphan.webp", "blog-orphan-data")
	if _, err := blogStore.CreatePost(&store.Post{
		Type:    "post",
		Lang:    "zh",
		Slug:    "blog-media-ref",
		Title:   "Blog 引用",
		Content: `<img src="/uploads/blog-keep.webp">`,
		Status:  "published",
	}); err != nil {
		t.Fatalf("create blog post: %v", err)
	}
	if err := blogStore.Close(); err != nil {
		t.Fatalf("close blog store: %v", err)
	}

	// shop 站：不自绑 SiteDomains 域名，但配置了已发布的 Cloudflare 部署域名，
	// 存储清理页应回退显示该域名（与站点管理卡片同源）。
	shopRoot := filepath.Join(dir, "shop")
	shopDB := filepath.Join(shopRoot, "cms.db")
	shopUploadDir := filepath.Join(shopRoot, "uploads")
	if err := os.MkdirAll(shopUploadDir, 0o755); err != nil {
		t.Fatalf("create shop upload dir: %v", err)
	}
	shopStore, err := store.Open(shopDB)
	if err != nil {
		t.Fatalf("open shop store: %v", err)
	}
	if err := shopStore.SetSetting(cloudflareDomainsKey, encodeCloudflareDomains([]CloudflareDomain{{Host: "shop.blockvar.com", Primary: true}})); err != nil {
		t.Fatalf("set shop cloudflare domains: %v", err)
	}
	if err := shopStore.Close(); err != nil {
		t.Fatalf("close shop store: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(shopRoot, "run"), 0o755); err != nil {
		t.Fatalf("create shop run dir: %v", err)
	}
	// status=success 且带 last_deploy_at 即视为已发布（cloudflareStatusPublished）。
	if err := os.WriteFile(filepath.Join(shopRoot, "run", "cloudflare-deploy.json"), []byte(`{"status":"success","message":"部署完成","last_deploy_at":"2026-07-10T00:00:00Z"}`), 0o644); err != nil {
		t.Fatalf("write shop cloudflare status: %v", err)
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
		Name:                        "Main Cleanup Site",
		DBPath:                      filepath.Join(dir, "default.db"),
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
	blogSite, err := ps.CreateSite("blog", "Blog Cleanup Site", blogDB, blogUploadDir, true)
	if err != nil {
		t.Fatalf("create blog site: %v", err)
	}
	// blog 站绑定主域名，默认站不绑定（页面上应显示「未绑定域名」）。
	if err := ps.AddSiteDomain(blogSite.ID, "https", "blog-primary.test", true, true); err != nil {
		t.Fatalf("add blog domain: %v", err)
	}
	shopSite, err := ps.CreateSite("shop", "Shop Cleanup Site", shopDB, shopUploadDir, true)
	if err != nil {
		t.Fatalf("create shop site: %v", err)
	}

	srv, err := NewWithPlatform(defaultStore, ps, "https://platform.test", defaultUploadDir, os.DirFS("../.."), os.DirFS("../.."))
	if err != nil {
		t.Fatalf("new platform server: %v", err)
	}
	h := srv.Handler()

	login := httptest.NewRecorder()
	loginForm := url.Values{"username": {"admin"}, "password": {store.DefaultAdminPassword}}
	loginReq := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/login", strings.NewReader(loginForm.Encode()))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(login, loginReq)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, body = %s", login.Code, login.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range login.Result().Cookies() {
		if c.Name == cookieName {
			cookie = c
			break
		}
	}
	if cookie == nil {
		t.Fatalf("login did not set session cookie")
	}
	sess, ok, err := ps.GetAdminSession(cookie.Value)
	if err != nil || !ok {
		t.Fatalf("get login session: ok=%v err=%v", ok, err)
	}

	return &mediaCleanupFixture{
		srv:           srv,
		h:             h,
		cookie:        cookie,
		csrf:          sess.CSRF,
		defaultDir:    defaultUploadDir,
		blogDir:       blogUploadDir,
		blogSiteID:    blogSite.ID,
		defaultSiteID: defaultSite.ID,
		shopSiteID:    shopSite.ID,
	}
}

func (f *mediaCleanupFixture) postJSON(t *testing.T, path string, form url.Values) (int, map[string]any) {
	t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("_csrf", f.csrf)
	req := httptest.NewRequest(http.MethodPost, "https://platform.test"+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.AddCookie(f.cookie)
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s response: %v; body = %s", path, err, w.Body.String())
	}
	return w.Code, out
}

// siteReport 从响应 sites 数组里取指定站点的报告。
func siteReport(t *testing.T, out map[string]any, siteID int64) map[string]any {
	t.Helper()
	sites, _ := out["sites"].([]any)
	for _, raw := range sites {
		if rep, ok := raw.(map[string]any); ok && rep["id"] == float64(siteID) {
			return rep
		}
	}
	t.Fatalf("site %d missing in response: %v", siteID, out)
	return nil
}

func TestPlatformMediaCleanupScanAndClean(t *testing.T) {
	f := newMediaCleanupFixture(t)

	// 页面入口：平台设置 → 存储清理，表格列出全部站点。
	pageReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/media-cleanup", nil)
	pageReq.AddCookie(f.cookie)
	page := httptest.NewRecorder()
	f.h.ServeHTTP(page, pageReq)
	if page.Code != http.StatusOK {
		t.Fatalf("page status = %d, body = %s", page.Code, page.Body.String())
	}
	body := page.Body.String()
	for _, want := range []string{
		"data-media-cleanup", "data-media-scan-all",
		"Main Cleanup Site", "Blog Cleanup Site",
		`data-media-site="` + strconv.FormatInt(f.blogSiteID, 10) + `"`,
		// 站点列：图标（有图标或字母占位）+ 主域名 / 未绑定提示。
		"media-cleanup-site-icon",
		"blog-primary.test",
		"未绑定域名",
		// 未自绑域名但已发布 Cloudflare 部署的站点：回退显示 CF 域名。
		"Shop Cleanup Site",
		"shop.blockvar.com",
		// 行内清理换成图标按钮：icon-trash + title 保留完整文案。
		`title="移入回收站（7 天后自动删除）"`,
		`class="del act-ico"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("cleanup page missing %q", want)
		}
	}

	// 扫描：只报告不动文件，两站各 1 个孤儿。
	code, scan := f.postJSON(t, "/admin/media-cleanup/scan", nil)
	if code != http.StatusOK || scan["ok"] != true {
		t.Fatalf("scan = %d %v", code, scan)
	}
	if got := scan["count"].(float64); got != 2 {
		t.Fatalf("scan count = %v, want 2; %v", got, scan)
	}
	wantBytes := float64(len("orphan-a-data") + len("blog-orphan-data"))
	if got := scan["bytes"].(float64); got != wantBytes {
		t.Fatalf("scan bytes = %v, want %v", got, wantBytes)
	}
	if rep := siteReport(t, scan, f.defaultSiteID); rep["count"].(float64) != 1 {
		t.Fatalf("default site scan report = %v", rep)
	}
	if rep := siteReport(t, scan, f.blogSiteID); rep["count"].(float64) != 1 {
		t.Fatalf("blog site scan report = %v", rep)
	}
	for _, name := range []string{"orphan-a.webp", "ref-content.webp"} {
		if _, err := os.Stat(filepath.Join(f.defaultDir, name)); err != nil {
			t.Fatalf("scan should not move %s: %v", name, err)
		}
	}

	// 单站行内清理：只动 blog 站。
	code, cleanBlog := f.postJSON(t, "/admin/media-cleanup/clean", url.Values{"site": {strconv.FormatInt(f.blogSiteID, 10)}})
	if code != http.StatusOK || cleanBlog["ok"] != true {
		t.Fatalf("clean blog = %d %v", code, cleanBlog)
	}
	if got := cleanBlog["moved"].(float64); got != 1 {
		t.Fatalf("clean blog moved = %v, want 1", got)
	}
	if _, err := os.Stat(filepath.Join(f.blogDir, "blog-orphan.webp")); !os.IsNotExist(err) {
		t.Fatalf("blog orphan should be trashed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.blogDir, "blog-keep.webp")); err != nil {
		t.Fatalf("blog referenced file should stay: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.defaultDir, "orphan-a.webp")); err != nil {
		t.Fatalf("single-site clean should not touch default site: %v", err)
	}
	trashEntries, err := os.ReadDir(filepath.Join(f.blogDir, mediaTrashDirName))
	if err != nil {
		t.Fatalf("read blog trash: %v", err)
	}
	found := false
	for _, e := range trashEntries {
		if strings.HasSuffix(e.Name(), "-blog-orphan.webp") {
			found = true
		}
	}
	if !found {
		t.Fatalf("blog trash should contain blog-orphan.webp with timestamp prefix, got %v", trashEntries)
	}

	// 全站清理：默认站孤儿进回收站，被引用文件原地不动。
	code, cleanAll := f.postJSON(t, "/admin/media-cleanup/clean", nil)
	if code != http.StatusOK || cleanAll["ok"] != true {
		t.Fatalf("clean all = %d %v", code, cleanAll)
	}
	if got := cleanAll["moved"].(float64); got != 1 {
		t.Fatalf("clean all moved = %v, want 1; %v", got, cleanAll)
	}
	if _, err := os.Stat(filepath.Join(f.defaultDir, "orphan-a.webp")); !os.IsNotExist(err) {
		t.Fatalf("default orphan should be trashed, stat err = %v", err)
	}
	for _, name := range []string{"ref-content.webp", "ref-cover.webp", "ref-extra.webp", "ref-setting.webp"} {
		if _, err := os.Stat(filepath.Join(f.defaultDir, name)); err != nil {
			t.Fatalf("referenced file %s should stay in uploads: %v", name, err)
		}
	}
}

func TestPlatformMediaCleanupPurgesExpiredTrash(t *testing.T) {
	f := newMediaCleanupFixture(t)
	trashDir := filepath.Join(f.defaultDir, mediaTrashDirName)
	if err := os.MkdirAll(trashDir, 0o755); err != nil {
		t.Fatalf("mkdir trash: %v", err)
	}
	writeUploadFile(t, trashDir, "expired.webp", "old")
	writeUploadFile(t, trashDir, "fresh.webp", "new")
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(trashDir, "expired.webp"), old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// 扫描（不执行清理）也会顺带清空超期回收站文件。
	if code, out := f.postJSON(t, "/admin/media-cleanup/scan", nil); code != http.StatusOK || out["ok"] != true {
		t.Fatalf("scan = %d %v", code, out)
	}

	if _, err := os.Stat(filepath.Join(trashDir, "expired.webp")); !os.IsNotExist(err) {
		t.Fatalf("expired trash file should be purged, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(trashDir, "fresh.webp")); err != nil {
		t.Fatalf("fresh trash file should survive purge: %v", err)
	}
}

func TestPlatformMediaCleanupAuth(t *testing.T) {
	f := newMediaCleanupFixture(t)

	// 未登录的 AJAX 请求：401。
	anon := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/media-cleanup/scan", strings.NewReader(""))
	anon.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	anon.Header.Set("Accept", "application/json")
	anonRec := httptest.NewRecorder()
	f.h.ServeHTTP(anonRec, anon)
	if anonRec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous scan status = %d, want 401; body = %s", anonRec.Code, anonRec.Body.String())
	}

	// 未登录访问页面：跳转登录。
	anonPage := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/media-cleanup", nil)
	pageRec := httptest.NewRecorder()
	f.h.ServeHTTP(pageRec, anonPage)
	if pageRec.Code != http.StatusSeeOther || pageRec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("anonymous page status/location = %d %q", pageRec.Code, pageRec.Header().Get("Location"))
	}

	// 已登录但 CSRF 错误：403，且不动任何文件。
	bad := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/media-cleanup/clean", strings.NewReader("_csrf=wrong"))
	bad.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	bad.Header.Set("Accept", "application/json")
	bad.AddCookie(f.cookie)
	badRec := httptest.NewRecorder()
	f.h.ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusForbidden {
		t.Fatalf("bad csrf status = %d, want 403; body = %s", badRec.Code, badRec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(f.defaultDir, "orphan-a.webp")); err != nil {
		t.Fatalf("orphan must stay untouched after rejected requests: %v", err)
	}
}

func TestScanUploadRefsExtractsBasenames(t *testing.T) {
	refs := map[string]bool{}
	scanUploadRefs(`![a](/uploads/a-1.webp) <img src="https://cdn.example.com/uploads/b_2.png"> 句尾 uploads/c.svg。`, refs)
	for _, want := range []string{"a-1.webp", "b_2.png", "c.svg"} {
		if !refs[want] {
			t.Fatalf("refs missing %q, got %v", want, refs)
		}
	}
}
