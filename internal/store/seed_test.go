package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func openSeededTestStore(t *testing.T) *Store {
	return openSeededTestStoreWithMode(t, "")
}

func openSeededTestStoreWithMode(t *testing.T, seedMode string) *Store {
	t.Helper()
	t.Setenv("CMS_SEED", seedMode)
	st, err := Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestDefaultSeedIsProductShowcase(t *testing.T) {
	st := openSeededTestStore(t)

	if got := st.Setting("site.name"); got != "gcms" {
		t.Fatalf("site.name = %q, want gcms", got)
	}
	if got := st.Setting("site.logo"); got != "" {
		t.Fatalf("site.logo = %q, want empty seed value", got)
	}
	if got := st.Setting("site.brand"); got != "logo" {
		t.Fatalf("site.brand = %q, want logo", got)
	}
	if got := st.Setting("hero.visual"); got != "image" {
		t.Fatalf("hero.visual = %q, want image", got)
	}
	if got := st.Setting("site.share_image"); got != "/assets/og-cover.webp" {
		t.Fatalf("site.share_image = %q, want default share image", got)
	}
	if got := st.Setting("site.share_image::en"); got != "/assets/og-cover-en.webp" {
		t.Fatalf("site.share_image::en = %q, want english share image", got)
	}
	if got := st.Setting("hero.image"); got != "/assets/hero-product-overview-brand.webp" {
		t.Fatalf("hero.image = %q, want product hero image", got)
	}
	if got := st.Setting("hero.image::en"); got != "/assets/hero-product-overview-brand-en.webp" {
		t.Fatalf("hero.image::en = %q, want english product hero image", got)
	}
	if got := st.Setting("site.hero_eyebrow"); got != "Cloudflare 部署 · 多站管理 · SEO/GEO · AI 自运营" {
		t.Fatalf("site.hero_eyebrow = %q", got)
	}
	if got := st.Setting("site.description"); !strings.Contains(got, "Cloudflare 静态部署") || !strings.Contains(got, "多站管理") {
		t.Fatalf("site.description should mention Cloudflare and multisite: %q", got)
	}
	if got := st.Setting("site.keywords"); !strings.Contains(got, "Cloudflare 静态部署") || !strings.Contains(got, "多站管理") {
		t.Fatalf("site.keywords should mention Cloudflare and multisite: %q", got)
	}
	if got := st.Setting("site.hero_description"); !strings.Contains(got, "Cloudflare 静态部署") || !strings.Contains(got, "AI 自运营") {
		t.Fatalf("site.hero_description should mention product capabilities: %q", got)
	}
	nav := st.Setting("nav_menu")
	for _, want := range []string{"/category/features", "/category/guides", "/links", "/start"} {
		if !strings.Contains(nav, want) {
			t.Fatalf("nav_menu does not contain %q: %s", want, nav)
		}
	}
	if !strings.Contains(nav, "开始使用") || !strings.Contains(nav, "Get Started") {
		t.Fatalf("nav_menu should use get-started wording: %s", nav)
	}
	if page, err := st.GetPage("zh", "start"); err != nil {
		t.Fatalf("get start page: %v", err)
	} else if page == nil {
		t.Fatalf("start page missing")
	} else if !strings.Contains(page.Content, "## 环境要求") || !strings.Contains(page.Content, "512MB") {
		t.Fatalf("start page should include environment requirements: %s", page.Content)
	}
	for slug, wantCover := range map[string]string{
		"github":   "/assets/covers/release-repo-real.webp",
		"releases": "/assets/covers/release-package-real.webp",
		"go":       "/assets/covers/go-brand.webp",
		"sqlite":   "/assets/covers/sqlite-brand.webp",
		"caddy":    "/assets/covers/caddy-brand.webp",
	} {
		link, err := st.GetLinkBySlug("zh", slug, true)
		if err != nil {
			t.Fatalf("get link %s: %v", slug, err)
		}
		if link == nil {
			t.Fatalf("link %s missing", slug)
		}
		if link.CoverImage != wantCover {
			t.Fatalf("link %s cover = %q, want %q", slug, link.CoverImage, wantCover)
		}
	}
	for slug, wantCover := range map[string]string{
		"deploy-in-5-minutes":               "/assets/covers/release-package-real.webp",
		"why-go-and-sqlite":                 "/assets/covers/gcms-stack-brand.webp",
		"multilingual-built-in":             "/assets/screenshots/language-switch.webp",
		"dual-mode-editor":                  "/assets/screenshots/article-editor.webp",
		"seo-by-default":                    "/assets/screenshots/seo-output.webp",
		"eighteen-themes":                   "/assets/screenshots/theme-settings.webp",
		"automation-api":                    "/assets/screenshots/automation-api.webp",
		"gcms-content-assistant-skill":      "/assets/screenshots/automation-api.webp",
		"cloudflare-static-deploy":          "/assets/screenshots/cloudflare-deploy.webp",
		"run-multiple-sites-from-one-admin": "/assets/screenshots/site-management.webp",
		"in-app-updates":                    "/assets/screenshots/system-updates.webp",
	} {
		post, err := st.GetPostBySlug("zh", slug, true)
		if err != nil {
			t.Fatalf("get post %s: %v", slug, err)
		}
		if post == nil {
			t.Fatalf("post %s missing", slug)
		}
		if post.CoverImage != wantCover {
			t.Fatalf("post %s cover = %q, want %q", slug, post.CoverImage, wantCover)
		}
		if strings.Contains(post.Content, "/assets/figures/") {
			t.Fatalf("post %s still references old figure asset: %s", slug, post.Content)
		}
	}
	for slug, wantCover := range map[string]string{
		"github":   "/assets/covers/release-repo-real-en.webp",
		"demo":     "/assets/covers/site-en.webp",
		"releases": "/assets/covers/release-package-real-en.webp",
		"go":       "/assets/covers/go-brand-en.webp",
		"sqlite":   "/assets/covers/sqlite-brand-en.webp",
		"caddy":    "/assets/covers/caddy-brand-en.webp",
	} {
		link, err := st.GetLinkBySlug("en", slug, true)
		if err != nil {
			t.Fatalf("get en link %s: %v", slug, err)
		}
		if link == nil {
			t.Fatalf("en link %s missing", slug)
		}
		if link.CoverImage != wantCover {
			t.Fatalf("en link %s cover = %q, want %q", slug, link.CoverImage, wantCover)
		}
	}
	for slug, wantCover := range map[string]string{
		"deploy-in-5-minutes": "/assets/covers/release-package-real-en.webp",
		"why-go-and-sqlite":   "/assets/covers/gcms-stack-brand-en.webp",
	} {
		post, err := st.GetPostBySlug("en", slug, true)
		if err != nil {
			t.Fatalf("get en post %s: %v", slug, err)
		}
		if post == nil {
			t.Fatalf("en post %s missing", slug)
		}
		if post.CoverImage != wantCover {
			t.Fatalf("en post %s cover = %q, want %q", slug, post.CoverImage, wantCover)
		}
		if !strings.Contains(post.Content, wantCover) {
			t.Fatalf("en post %s content should reference %q: %s", slug, wantCover, post.Content)
		}
	}
	for slug, wantCover := range map[string]string{
		"multilingual-built-in":               "/assets/screenshots/language-switch-en.webp",
		"dual-mode-editor":                    "/assets/screenshots/article-editor-en.webp",
		"seo-by-default":                      "/assets/screenshots/seo-output-en.webp",
		"eighteen-themes":                     "/assets/screenshots/theme-settings-en.webp",
		"automation-api":                      "/assets/screenshots/automation-api-en.webp",
		"gcms-content-assistant-skill":        "/assets/screenshots/automation-api-en.webp",
		"cloudflare-static-deploy":            "/assets/screenshots/cloudflare-deploy-en.webp",
		"run-multiple-sites-from-one-admin":   "/assets/screenshots/site-management-en.webp",
		"in-app-updates":                      "/assets/screenshots/system-updates-en.webp",
		"how-to-change-theme":                 "/assets/screenshots/theme-settings-en.webp",
		"how-to-ai-content-ops":               "/assets/screenshots/automation-api-en.webp",
		"visual-edit-homepage-copy":           "/assets/screenshots/visual-editor-home-en.webp",
		"configure-navigation-and-categories": "/assets/screenshots/navigation-menu-en.webp",
		"first-launch-security-and-demo-data": "/assets/screenshots/security-demo-data-en.webp",
	} {
		post, err := st.GetPostBySlug("en", slug, true)
		if err != nil {
			t.Fatalf("get en post %s: %v", slug, err)
		}
		if post == nil {
			t.Fatalf("en post %s missing", slug)
		}
		if post.CoverImage != wantCover {
			t.Fatalf("en post %s cover = %q, want %q", slug, post.CoverImage, wantCover)
		}
		if !strings.Contains(post.Content, wantCover) {
			t.Fatalf("en post %s content should reference %q: %s", slug, wantCover, post.Content)
		}
	}
	if cats, err := st.ListCategories("zh", "post"); err != nil {
		t.Fatalf("list post categories: %v", err)
	} else if len(cats) != 4 {
		t.Fatalf("post categories = %d, want 4", len(cats))
	}
	if post, err := st.GetPostBySlug("zh", "gcms-content-assistant-skill", true); err != nil {
		t.Fatalf("get content assistant skill post: %v", err)
	} else if post == nil {
		t.Fatalf("content assistant skill post missing")
	} else if post.Category == nil || post.Category.Slug != "ops" {
		t.Fatalf("content assistant skill category = %#v, want ops", post.Category)
	} else if !strings.Contains(post.Content, "下载 AI 接入包") || !strings.Contains(post.Content, "node scripts/gcms.js doctor") {
		t.Fatalf("content assistant skill post should explain download and doctor: %s", post.Content)
	}
	if post, err := st.GetPostBySlug("en", "gcms-content-assistant-skill", true); err != nil {
		t.Fatalf("get english content assistant skill post: %v", err)
	} else if post == nil {
		t.Fatalf("english content assistant skill post missing")
	} else if post.TransGroup != "s-content-assistant-skill" {
		t.Fatalf("english content assistant skill trans_group = %q", post.TransGroup)
	} else if !strings.Contains(post.Content, "Download AI Package") || !strings.Contains(post.Content, "node scripts/gcms.js doctor") {
		t.Fatalf("english content assistant skill post should explain download and doctor: %s", post.Content)
	}
}

func TestListAdminContentPaginatesAndFiltersStatus(t *testing.T) {
	st := openSeededTestStore(t)

	for i := 0; i < 25; i++ {
		if _, err := st.CreatePost(&Post{
			Type:       "post",
			Lang:       "zh",
			Slug:       "admin-draft-" + string(rune('a'+i)),
			Title:      "Admin Draft",
			Status:     "draft",
			EditorMode: "markdown",
		}); err != nil {
			t.Fatalf("create draft %d: %v", i, err)
		}
	}
	total, err := st.CountAdminContent("post", "zh", "draft")
	if err != nil {
		t.Fatalf("count drafts: %v", err)
	}
	if total != 25 {
		t.Fatalf("draft total = %d, want 25", total)
	}
	first, err := st.ListAdminContent("post", "zh", "draft", 0, 20)
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(first) != 20 {
		t.Fatalf("first page len = %d, want 20", len(first))
	}
	second, err := st.ListAdminContent("post", "zh", "draft", 20, 20)
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(second) != 5 {
		t.Fatalf("second page len = %d, want 5", len(second))
	}
}

func TestAdminOverviewQueries(t *testing.T) {
	st := openSeededTestStore(t)
	lang := "zz"

	items := []*Post{
		{Type: "post", Lang: lang, Slug: "overview-post", Title: "Overview Post", Status: "draft", EditorMode: "markdown"},
		{Type: "link", Lang: lang, Slug: "overview-link", Title: "Overview Link", Status: "published", CoverImage: "/uploads/link.webp", LinkURL: "https://example.com", EditorMode: "markdown"},
		{Type: "page", Lang: lang, Slug: "overview-page", Title: "Overview Page", Status: "scheduled", EditorMode: "markdown"},
	}
	for i, p := range items {
		if _, err := st.CreatePost(p); err != nil {
			t.Fatalf("create overview item %d: %v", i, err)
		}
	}

	if total, err := st.CountAdminContent("page", lang, "scheduled"); err != nil {
		t.Fatalf("count scheduled pages: %v", err)
	} else if total != 1 {
		t.Fatalf("scheduled page total = %d, want 1", total)
	}
	counts, err := st.AdminContentStatusCounts(lang)
	if err != nil {
		t.Fatalf("admin content status counts: %v", err)
	}
	if got := counts["post"]; got.Total != 1 || got.Draft != 1 {
		t.Fatalf("post counts = %#v, want total=1 draft=1", got)
	}
	if got := counts["link"]; got.Total != 1 || got.Published != 1 {
		t.Fatalf("link counts = %#v, want total=1 published=1", got)
	}
	if got := counts["page"]; got.Total != 1 || got.Scheduled != 1 {
		t.Fatalf("page counts = %#v, want total=1 scheduled=1", got)
	}
	if missing, err := st.CountAdminContentIssue("post", lang, "missing_cover"); err != nil {
		t.Fatalf("count missing post covers: %v", err)
	} else if missing != 1 {
		t.Fatalf("missing post covers = %d, want 1", missing)
	}
	if missing, err := st.CountAdminContentIssue("link", lang, "missing_cover"); err != nil {
		t.Fatalf("count missing link covers: %v", err)
	} else if missing != 0 {
		t.Fatalf("missing link covers = %d, want 0", missing)
	}
	if missing, err := st.CountAdminContentIssue("link", lang, "missing_category"); err != nil {
		t.Fatalf("count missing link categories: %v", err)
	} else if missing != 1 {
		t.Fatalf("missing link categories = %d, want 1", missing)
	}
	issues, err := st.AdminContentIssueCounts(lang)
	if err != nil {
		t.Fatalf("admin content issue counts: %v", err)
	}
	if got := issues["post"]; got.MissingCover != 1 || got.MissingCategory != 1 {
		t.Fatalf("post issues = %#v, want missing cover/category", got)
	}
	if got := issues["link"]; got.MissingCover != 0 || got.MissingCategory != 1 {
		t.Fatalf("link issues = %#v, want category only", got)
	}
	recent, err := st.ListRecentAdminContent(lang, 10)
	if err != nil {
		t.Fatalf("list recent content: %v", err)
	}
	if len(recent) != 3 {
		t.Fatalf("recent len = %d, want 3", len(recent))
	}
	seen := map[string]bool{}
	for _, p := range recent {
		seen[p.Type] = true
	}
	for _, typ := range []string{"post", "link", "page"} {
		if !seen[typ] {
			t.Fatalf("recent content missing type %q: %#v", typ, recent)
		}
	}
}

func TestBundledCoverPathsMigrateToWebP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cms.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	for slug, oldCover := range map[string]string{
		"github":   "/assets/covers/release-repo.webp",
		"releases": "/assets/covers/release.svg",
		"go":       "/assets/covers/go-real.webp",
		"sqlite":   "/assets/covers/sqlite.svg",
		"caddy":    "/assets/covers/caddy.webp",
	} {
		if _, err := st.db.Exec(`UPDATE posts SET cover_image=? WHERE lang=? AND slug=?`, oldCover, "zh", slug); err != nil {
			t.Fatalf("set old cover path for %s: %v", slug, err)
		}
	}
	for slug, oldCover := range map[string]string{
		"deploy-in-5-minutes": "/assets/covers/deploy.svg",
		"why-go-and-sqlite":   "/assets/covers/architecture.svg",
		"dual-mode-editor":    "/assets/covers/editor.svg",
	} {
		if _, err := st.db.Exec(`UPDATE posts SET cover_image=?, content=? WHERE lang=? AND slug=?`, oldCover, "old image ![](/assets/figures/editor-workflow.svg)", "zh", slug); err != nil {
			t.Fatalf("set old post asset paths for %s: %v", slug, err)
		}
	}
	for key, value := range map[string]string{
		"site.tagline":         "把内容站交付成一个可运行的二进制",
		"site.description":     "gcms 是面向产品官网、技术文档和轻量内容站的自托管 CMS：单个二进制启动，SQLite 单文件存储，服务端渲染默认做好 SEO，多语种、主题、在线升级和自动化接口开箱可用。",
		"site.hero_eyebrow":    "产品官网 · 技术文档 · 自托管内容站",
		"site.hero_title":      "一个二进制，\n上线一个完整\n内容站。",
		"site.share_image":     "",
		"site.share_image::en": "",
		"hero.visual":          "",
		"hero.image":           "/assets/figures/gcms-showcase-hero.svg",
		"site.tagline::en":     "Ship a complete content site as one binary",
		"site.hero_title::en":  "One binary,\none complete content site.",
	} {
		if _, err := st.db.Exec(`UPDATE settings SET value=? WHERE key=?`, value, key); err != nil {
			t.Fatalf("set old setting %s: %v", key, err)
		}
	}
	oldStart := md(
		"如果你正在评估 **gcms**，最好的方式是先把它跑起来。",
		"",
		"## 快速试用",
		"- 正式站点：[ccvar.com](https://ccvar.com)",
		"- 下载发布包：[GitHub Releases](https://github.com/ccvar/gcms-releases/releases/latest)",
		"- 一键安装：`curl -fsSL https://raw.githubusercontent.com/ccvar/gcms-releases/main/install.sh | sh`",
		"",
		"## 部署建议",
		"生产环境建议让 gcms 监听 `127.0.0.1:8080`。",
	)
	if _, err := st.db.Exec(`UPDATE posts SET content=? WHERE type='page' AND lang='zh' AND slug='start'`, oldStart); err != nil {
		t.Fatalf("set old start page content: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE posts SET cover_image=?, content=? WHERE lang=? AND slug=?`,
		"/assets/screenshots/article-editor.webp",
		"Old English editor screenshot: ![](/assets/screenshots/article-editor.webp)",
		"en",
		"dual-mode-editor",
	); err != nil {
		t.Fatalf("set old english screenshot path: %v", err)
	}
	for slug, oldCover := range map[string]string{
		"github":              "/assets/covers/release-repo-real.webp",
		"demo":                "/assets/covers/site.webp",
		"releases":            "/assets/covers/release-package-real.webp",
		"go":                  "/assets/covers/go-brand.webp",
		"sqlite":              "/assets/covers/sqlite-brand.webp",
		"caddy":               "/assets/covers/caddy-brand.webp",
		"deploy-in-5-minutes": "/assets/covers/release-package-real.webp",
		"why-go-and-sqlite":   "/assets/covers/gcms-stack-brand.webp",
	} {
		if _, err := st.db.Exec(`UPDATE posts SET cover_image=?, content=? WHERE lang=? AND slug=?`, oldCover, "old English cover ![]("+oldCover+")", "en", slug); err != nil {
			t.Fatalf("set old english cover for %s: %v", slug, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for slug, wantCover := range map[string]string{
		"github":   "/assets/covers/release-repo-real.webp",
		"releases": "/assets/covers/release-package-real.webp",
		"go":       "/assets/covers/go-brand.webp",
		"sqlite":   "/assets/covers/sqlite-brand.webp",
		"caddy":    "/assets/covers/caddy-brand.webp",
	} {
		link, err := st.GetLinkBySlug("zh", slug, true)
		if err != nil {
			t.Fatalf("get %s link: %v", slug, err)
		}
		if link == nil {
			t.Fatalf("%s link missing", slug)
		}
		if link.CoverImage != wantCover {
			t.Fatalf("%s cover = %q, want %q", slug, link.CoverImage, wantCover)
		}
	}
	for slug, wantCover := range map[string]string{
		"deploy-in-5-minutes": "/assets/covers/release-package-real.webp",
		"why-go-and-sqlite":   "/assets/covers/gcms-stack-brand.webp",
		"dual-mode-editor":    "/assets/screenshots/article-editor.webp",
	} {
		post, err := st.GetPostBySlug("zh", slug, true)
		if err != nil {
			t.Fatalf("get %s post: %v", slug, err)
		}
		if post == nil {
			t.Fatalf("%s post missing", slug)
		}
		if post.CoverImage != wantCover {
			t.Fatalf("%s cover = %q, want %q", slug, post.CoverImage, wantCover)
		}
		if strings.Contains(post.Content, "/assets/figures/editor-workflow.svg") {
			t.Fatalf("%s content still references old figure: %s", slug, post.Content)
		}
		if !strings.Contains(post.Content, "/assets/screenshots/article-editor.webp") {
			t.Fatalf("%s content did not migrate to article editor screenshot: %s", slug, post.Content)
		}
	}
	enPost, err := st.GetPostBySlug("en", "dual-mode-editor", true)
	if err != nil {
		t.Fatalf("get migrated en editor post: %v", err)
	}
	if enPost == nil {
		t.Fatalf("migrated en editor post missing")
	}
	if enPost.CoverImage != "/assets/screenshots/article-editor-en.webp" {
		t.Fatalf("en editor cover = %q, want english screenshot", enPost.CoverImage)
	}
	if !strings.Contains(enPost.Content, "/assets/screenshots/article-editor-en.webp") || strings.Contains(enPost.Content, "/assets/screenshots/article-editor.webp") {
		t.Fatalf("en editor content did not migrate to english screenshot: %s", enPost.Content)
	}
	for key, want := range map[string]string{
		"site.tagline":              "内容发布、搜索增长，一个后台跑通",
		"site.description":          "gcms 把文章、页面、资源链接、全语种内容、主题、SEO/GEO、Cloudflare 静态部署、多站管理和 AI 自运营接口收进同一个后台；无需搭数据库服务和前端构建环境，一行命令即可部署，1 vCPU / 512MB 内存的小规格 VPS 也能稳定起步。",
		"site.keywords":             "gcms,CMS,内容管理系统,Cloudflare 静态部署,多站管理,SEO,GEO,AI 自运营",
		"site.hero_eyebrow":         "Cloudflare 部署 · 多站管理 · SEO/GEO · AI 自运营",
		"site.hero_title":           "内容发布、\n搜索增长，\n一个后台跑通",
		"site.hero_description":     "gcms 把文章、页面、资源链接、全语种内容、主题、SEO/GEO、Cloudflare 静态部署、多站管理和 AI 自运营接口收进同一个后台；无需搭数据库服务和前端构建环境，一行命令即可部署，1 vCPU / 512MB 内存的小规格 VPS 也能稳定起步。",
		"site.share_image":          "/assets/og-cover.webp",
		"site.share_image::en":      "/assets/og-cover-en.webp",
		"hero.visual":               "image",
		"hero.image":                "/assets/hero-product-overview-brand.webp",
		"hero.visual::en":           "image",
		"hero.image::en":            "/assets/hero-product-overview-brand-en.webp",
		"site.tagline::en":          "Publish content and grow search from one admin",
		"site.description::en":      "gcms brings posts, pages, resource links, multilingual content, themes, SEO/GEO, Cloudflare static deployment, multisite management and AI-operation APIs into one lightweight admin. No database server or frontend build pipeline required: deploy with one command and start on a 1 vCPU / 512MB VPS.",
		"site.keywords::en":         "gcms,CMS,content management,Cloudflare static deployment,multisite,SEO,GEO,AI operations",
		"site.hero_eyebrow::en":     "Cloudflare deploy · Multisite · SEO/GEO · AI operations",
		"site.hero_title::en":       "Publish content,\ngrow search traffic,\nrun it from one admin",
		"site.hero_description::en": "gcms brings posts, pages, resource links, multilingual content, themes, SEO/GEO, Cloudflare static deployment, multisite management and AI-operation APIs into one lightweight admin. No database server or frontend build pipeline required: deploy with one command and start on a 1 vCPU / 512MB VPS.",
	} {
		if got := st.Setting(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	for slug, wantCover := range map[string]string{
		"github":   "/assets/covers/release-repo-real-en.webp",
		"demo":     "/assets/covers/site-en.webp",
		"releases": "/assets/covers/release-package-real-en.webp",
		"go":       "/assets/covers/go-brand-en.webp",
		"sqlite":   "/assets/covers/sqlite-brand-en.webp",
		"caddy":    "/assets/covers/caddy-brand-en.webp",
	} {
		link, err := st.GetLinkBySlug("en", slug, true)
		if err != nil {
			t.Fatalf("get migrated en link %s: %v", slug, err)
		}
		if link == nil {
			t.Fatalf("migrated en link %s missing", slug)
		}
		if link.CoverImage != wantCover {
			t.Fatalf("migrated en link %s cover = %q, want %q", slug, link.CoverImage, wantCover)
		}
		if !strings.Contains(link.Content, wantCover) {
			t.Fatalf("migrated en link %s content should reference %q: %s", slug, wantCover, link.Content)
		}
	}
	for slug, wantCover := range map[string]string{
		"deploy-in-5-minutes": "/assets/covers/release-package-real-en.webp",
		"why-go-and-sqlite":   "/assets/covers/gcms-stack-brand-en.webp",
	} {
		post, err := st.GetPostBySlug("en", slug, true)
		if err != nil {
			t.Fatalf("get migrated en post %s: %v", slug, err)
		}
		if post == nil {
			t.Fatalf("migrated en post %s missing", slug)
		}
		if post.CoverImage != wantCover {
			t.Fatalf("migrated en post %s cover = %q, want %q", slug, post.CoverImage, wantCover)
		}
		if !strings.Contains(post.Content, wantCover) {
			t.Fatalf("migrated en post %s content should reference %q: %s", slug, wantCover, post.Content)
		}
	}
	page, err := st.GetPage("zh", "start")
	if err != nil {
		t.Fatalf("get migrated start page: %v", err)
	}
	if page == nil {
		t.Fatalf("migrated start page missing")
	}
	if !strings.Contains(page.Content, "## 环境要求") || !strings.Contains(page.Content, "512MB") {
		t.Fatalf("migrated start page did not receive environment requirements: %s", page.Content)
	}
	if strings.Index(page.Content, "## 环境要求") > strings.Index(page.Content, "## 部署建议") {
		t.Fatalf("environment requirements should appear before deployment suggestion: %s", page.Content)
	}
}

func TestClearDemoContentKeepsBaseSettings(t *testing.T) {
	st := openSeededTestStore(t)

	if err := st.ClearDemoContent(); err != nil {
		t.Fatalf("clear demo content: %v", err)
	}
	if n, err := st.CountPublished("zh"); err != nil {
		t.Fatalf("count posts: %v", err)
	} else if n != 0 {
		t.Fatalf("published zh posts = %d, want 0", n)
	}
	if n, err := st.CountLinks("zh", 0); err != nil {
		t.Fatalf("count links: %v", err)
	} else if n != 0 {
		t.Fatalf("published zh links = %d, want 0", n)
	}
	if page, err := st.GetPage("zh", "about"); err != nil {
		t.Fatalf("get about page: %v", err)
	} else if page != nil {
		t.Fatalf("about page still exists after clearing demo content")
	}
	if cats, err := st.ListCategories("zh", "post"); err != nil {
		t.Fatalf("list post categories: %v", err)
	} else if len(cats) != 0 {
		t.Fatalf("post categories = %d, want 0", len(cats))
	}
	if cats, err := st.ListCategories("zh", "link"); err != nil {
		t.Fatalf("list link categories: %v", err)
	} else if len(cats) != 0 {
		t.Fatalf("link categories = %d, want 0", len(cats))
	}
	for _, key := range []string{"nav_menu", "social_links", "home.featured_title", "category.all.title", "links.all.title", "site.share_image", "site.share_image::en", "hero.visual", "hero.visual::en", "hero.image", "hero.image::en", "hero.svg"} {
		if got := st.Setting(key); got != "" {
			t.Fatalf("%s = %q, want empty", key, got)
		}
	}
	if got := st.Setting("demo.seed"); got != "empty" {
		t.Fatalf("demo.seed = %q, want empty", got)
	}
	if got := st.Setting("admin_password_hash"); got == "" {
		t.Fatalf("admin password hash should be kept")
	}
	if got := st.Setting("locales"); got != "zh,en" {
		t.Fatalf("locales = %q, want zh,en", got)
	}
	if got := st.Setting("theme"); got != "product" {
		t.Fatalf("theme = %q, want product", got)
	}
}

func TestEmptySiteBasePagesAreRepairedOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cms.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.ClearDemoContent(); err != nil {
		t.Fatalf("clear demo content: %v", err)
	}
	if page, err := st.GetPage("zh", "about"); err != nil {
		t.Fatalf("get about before reopen: %v", err)
	} else if page != nil {
		t.Fatalf("about page should not be present before repair")
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	for _, lang := range []string{"zh", "en"} {
		page, err := reopened.GetPage(lang, "about")
		if err != nil {
			t.Fatalf("get repaired %s about page: %v", lang, err)
		}
		if page == nil {
			t.Fatalf("repaired %s about page missing", lang)
		}
		if page.Type != "page" || page.Status != "published" || page.TransGroup != "base-page-about" {
			t.Fatalf("repaired %s about page = %#v", lang, page)
		}
	}
	if got := reopened.Setting("empty.base_pages_repaired"); got != "1" {
		t.Fatalf("empty.base_pages_repaired = %q, want 1", got)
	}
	if n, err := reopened.CountPublished("zh"); err != nil {
		t.Fatalf("count repaired posts: %v", err)
	} else if n != 0 {
		t.Fatalf("repaired empty site should not add posts, got %d", n)
	}
}

func TestReloadShowcaseContentReplacesCurrentContent(t *testing.T) {
	st := openSeededTestStoreWithMode(t, "classic")
	if got := st.Setting("site.name"); got == "gcms" {
		t.Fatalf("expected classic seed before reload")
	}
	if err := st.SetSetting("admin_user", "owner"); err != nil {
		t.Fatalf("set admin user: %v", err)
	}
	if err := st.SetSetting("admin_password_hash", "custom-hash"); err != nil {
		t.Fatalf("set admin password hash: %v", err)
	}
	if err := st.SetSetting("site.logo", "/uploads/logo.svg"); err != nil {
		t.Fatalf("set logo: %v", err)
	}
	if err := st.SetSetting("site.favicon", "/uploads/favicon.ico"); err != nil {
		t.Fatalf("set favicon: %v", err)
	}
	if err := st.SetSetting("site.share_image", "/uploads/share.webp"); err != nil {
		t.Fatalf("set share image: %v", err)
	}
	if err := st.SetSetting("site.share_image::en", "/uploads/share-en.webp"); err != nil {
		t.Fatalf("set english share image: %v", err)
	}
	if err := st.SetSetting("theme", "terminal"); err != nil {
		t.Fatalf("set theme: %v", err)
	}
	if _, err := st.CreatePost(&Post{Type: "post", Lang: "zh", Slug: "custom-post", Title: "Custom Post", Status: "published", EditorMode: "markdown"}); err != nil {
		t.Fatalf("create custom post: %v", err)
	}

	if err := st.ReloadShowcaseContent(); err != nil {
		t.Fatalf("reload showcase content: %v", err)
	}

	if got := st.Setting("site.name"); got != "gcms" {
		t.Fatalf("site.name = %q, want gcms", got)
	}
	if got := st.Setting("admin_user"); got != "owner" {
		t.Fatalf("admin_user = %q, want owner", got)
	}
	if got := st.Setting("admin_password_hash"); got != "custom-hash" {
		t.Fatalf("admin_password_hash = %q, want preserved custom hash", got)
	}
	if got := st.Setting("site.logo"); got != "/uploads/logo.svg" {
		t.Fatalf("site.logo = %q, want preserved logo", got)
	}
	if got := st.Setting("site.favicon"); got != "/uploads/favicon.ico" {
		t.Fatalf("site.favicon = %q, want preserved favicon", got)
	}
	if got := st.Setting("site.share_image"); got != "/uploads/share.webp" {
		t.Fatalf("site.share_image = %q, want preserved share image", got)
	}
	if got := st.Setting("site.share_image::en"); got != "/uploads/share-en.webp" {
		t.Fatalf("site.share_image::en = %q, want preserved english share image", got)
	}
	if got := st.Setting("theme"); got != "terminal" {
		t.Fatalf("theme = %q, want preserved theme", got)
	}
	if got := st.Setting("site.brand"); got != "logo" {
		t.Fatalf("site.brand = %q, want logo", got)
	}
	if post, err := st.GetPostBySlug("zh", "custom-post", true); err != nil {
		t.Fatalf("get custom post: %v", err)
	} else if post != nil {
		t.Fatalf("custom post still exists after reload")
	}
	if cat, err := st.GetCategoryBySlug("zh", "engineering"); err != nil {
		t.Fatalf("get old category: %v", err)
	} else if cat != nil {
		t.Fatalf("classic category still exists after reload")
	}
	if n, err := st.CountPublished("zh"); err != nil {
		t.Fatalf("count showcase posts: %v", err)
	} else if n != 16 {
		t.Fatalf("published zh posts = %d, want 16", n)
	}
	if n, err := st.CountLinks("zh", 0); err != nil {
		t.Fatalf("count showcase links: %v", err)
	} else if n != 6 {
		t.Fatalf("published zh links = %d, want 6", n)
	}
	if page, err := st.GetPage("zh", "start"); err != nil {
		t.Fatalf("get start page: %v", err)
	} else if page == nil {
		t.Fatalf("start page missing after reload")
	}
	if nav := st.Setting("nav_menu"); !strings.Contains(nav, "/start") {
		t.Fatalf("nav_menu does not contain start after reload: %s", nav)
	} else if !strings.Contains(nav, "开始使用") || !strings.Contains(nav, "Get Started") {
		t.Fatalf("nav_menu should use get-started wording after reload: %s", nav)
	}
}
