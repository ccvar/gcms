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
	if got := st.Setting("hero.image"); got != "/assets/hero-product-overview-brand.webp" {
		t.Fatalf("hero.image = %q, want product hero image", got)
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
		"deploy-in-5-minutes":   "/assets/covers/release-package-real.webp",
		"why-go-and-sqlite":     "/assets/covers/gcms-stack-brand.webp",
		"multilingual-built-in": "/assets/screenshots/language-switch.webp",
		"dual-mode-editor":      "/assets/screenshots/article-editor.webp",
		"seo-by-default":        "/assets/screenshots/seo-output.webp",
		"eighteen-themes":       "/assets/screenshots/theme-settings.webp",
		"automation-api":        "/assets/screenshots/automation-api.webp",
		"in-app-updates":        "/assets/screenshots/system-updates.webp",
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
	if cats, err := st.ListCategories("zh", "post"); err != nil {
		t.Fatalf("list post categories: %v", err)
	} else if len(cats) != 4 {
		t.Fatalf("post categories = %d, want 4", len(cats))
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
		"site.tagline":        "把内容站交付成一个可运行的二进制",
		"site.description":    "gcms 是面向产品官网、技术文档和轻量内容站的自托管 CMS：单个二进制启动，SQLite 单文件存储，服务端渲染默认做好 SEO，多语种、主题、在线升级和自动化接口开箱可用。",
		"site.hero_eyebrow":   "产品官网 · 技术文档 · 自托管内容站",
		"site.hero_title":     "一个二进制，\n上线一个完整\n内容站。",
		"site.share_image":    "",
		"hero.visual":         "",
		"hero.image":          "/assets/figures/gcms-showcase-hero.svg",
		"site.tagline::en":    "Ship a complete content site as one binary",
		"site.hero_title::en": "One binary,\none complete content site.",
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
	for key, want := range map[string]string{
		"site.tagline":        "低配置也能跑的完整内容站",
		"site.description":    "gcms 适合产品官网、技术文档、资源导航和轻量内容站：单个二进制启动，SQLite 单文件存储，低配 VPS 也能部署；后台、主题、多语种、SEO、在线升级都开箱可用，并支持 AI 协助运营。",
		"site.hero_eyebrow":   "低配置部署 · SEO 就绪 · 自托管内容站",
		"site.hero_title":     "小机器，\n也能跑起完整\n内容站",
		"site.share_image":    "/assets/og-cover.webp",
		"hero.visual":         "image",
		"hero.image":          "/assets/hero-product-overview-brand.webp",
		"site.tagline::en":    "A complete content site that runs on small servers",
		"site.hero_title::en": "A small server\ncan run a complete\ncontent site",
	} {
		if got := st.Setting(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
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
	for _, key := range []string{"nav_menu", "social_links", "home.featured_title", "category.all.title", "links.all.title", "site.share_image", "hero.visual", "hero.image", "hero.svg"} {
		if got := st.Setting(key); got != "" {
			t.Fatalf("%s = %q, want empty", key, got)
		}
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
	} else if n != 13 {
		t.Fatalf("published zh posts = %d, want 13", n)
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
