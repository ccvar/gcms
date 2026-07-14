// Package store 封装 SQLite 数据访问：模型、迁移与查询。
// 使用纯 Go 驱动 modernc.org/sqlite，无需 CGO，便于交叉编译为单一二进制。
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ---------- 模型 ----------

type Category struct {
	ID          int64
	Slug        string
	Name        string
	Description string
	Position    int
	Lang        string // 语种码，如 zh / en
	TransGroup  string // 互译分组键：同一逻辑分类的各语种版本共享
	Kind        string // post | link：分类归属（文章分类 / 链接分类，相互独立）
	Count       int    // 该分类下已发布条目数（列表时填充）
}

type Post struct {
	ID              int64
	Type            string // post | page | link
	Slug            string
	Title           string
	Excerpt         string
	Content         string // Markdown 源文
	ContentLen      int    // 正文字符数；列表查询不取正文时用于估算阅读时长
	MetaDesc        string
	Keywords        string
	CoverImage      string
	Author          string
	Status          string // draft | published | scheduled
	Featured        bool   // 置顶（首页精选优先）
	EditorMode      string // markdown | rich（记住上次编辑方式）
	CommentsEnabled bool   // 文章页是否显示第三方评论区
	Lang            string // 语种码，如 zh / en
	TransGroup      string // 互译分组键：同一逻辑文章的各语种版本共享
	CategoryID      sql.NullInt64
	Category        *Category
	LinkURL         string // 仅 type=link：指向的目标网址
	Extra           string // 扩展内容类型的自定义字段值（JSON 对象）；内置 post/page/link 一般为空

	PublishedAt time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ReadingTime 估算阅读时长（分钟）。中文按约 350 字/分钟。
func (p *Post) ReadingTime() int {
	n := p.ContentLen
	if n == 0 && p.Content != "" {
		n = len([]rune(p.Content))
	}
	m := n / 350
	if m < 1 {
		m = 1
	}
	return m
}

func (p *Post) IsPublished() bool { return p.Status == "published" }

// KeywordList 把逗号分隔的关键词拆成切片，供模板渲染标签。
func (p *Post) KeywordList() []string {
	var out []string
	for _, k := range strings.Split(p.Keywords, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

type Setting struct {
	Key   string
	Value string
}

// ---------- 连接与迁移 ----------

type Store struct {
	db *sql.DB
	// Seeded 表示本次 Open 触发了空库播种（首次启动），供上层提示默认账号。
	Seeded bool
	// 默认密码校验结果缓存（bcrypt 较慢，仅当 hash 变化时重算）。
	pwMu        sync.Mutex
	pwHash      string
	pwIsDefault bool
	// 设置项读多写少，启动后缓存在内存中；后台保存设置时同步更新。
	settingsMu     sync.RWMutex
	settings       map[string]string
	settingsLoaded bool
}

func Open(path string) (*Store, error) {
	// 通过 DSN 设置 WAL、忙等待与外键约束。
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// WAL 下允许多个读连接并发；写入仍由 SQLite 串行化，连接数保持保守。
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// 新库建表：slug 不再全局唯一，而是 (lang, slug) 复合唯一，
// 以支持各语种使用各自的 slug（如 /zh/about 与 /en/about 并存）。
const schema = `
CREATE TABLE IF NOT EXISTS categories (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  slug        TEXT NOT NULL,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  position    INTEGER NOT NULL DEFAULT 0,
  lang        TEXT NOT NULL DEFAULT 'zh',
  trans_group TEXT NOT NULL DEFAULT '',
  kind        TEXT NOT NULL DEFAULT 'post',
  UNIQUE(lang, slug)
);

CREATE TABLE IF NOT EXISTS posts (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  type         TEXT NOT NULL DEFAULT 'post',
  slug         TEXT NOT NULL,
  title        TEXT NOT NULL,
  excerpt      TEXT NOT NULL DEFAULT '',
  content      TEXT NOT NULL DEFAULT '',
  meta_desc    TEXT NOT NULL DEFAULT '',
  keywords     TEXT NOT NULL DEFAULT '',
  cover_image  TEXT NOT NULL DEFAULT '',
  author       TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL DEFAULT 'draft',
  featured     INTEGER NOT NULL DEFAULT 0,
  editor_mode  TEXT NOT NULL DEFAULT 'markdown',
  comments_enabled INTEGER NOT NULL DEFAULT 0,
  lang         TEXT NOT NULL DEFAULT 'zh',
  trans_group  TEXT NOT NULL DEFAULT '',
  link_url     TEXT NOT NULL DEFAULT '',
  extra        TEXT NOT NULL DEFAULT '',
  category_id  INTEGER REFERENCES categories(id) ON DELETE SET NULL,
  published_at TEXT,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  UNIQUE(lang, slug)
);

CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS admin_sessions (
  token_hash   TEXT PRIMARY KEY,
  user         TEXT NOT NULL,
  csrf         TEXT NOT NULL,
  expires_at   TEXT NOT NULL,
  pw_dismissed INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS automation_keys (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  name         TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,
  token_prefix TEXT NOT NULL,
  scopes       TEXT NOT NULL DEFAULT '',
  last_used_at TEXT,
  created_at   TEXT NOT NULL,
  revoked_at   TEXT
);

CREATE TABLE IF NOT EXISTS automation_logs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  key_id      INTEGER REFERENCES automation_keys(id) ON DELETE SET NULL,
  action      TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_id   INTEGER NOT NULL DEFAULT 0,
  message     TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS content_types (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  key          TEXT NOT NULL UNIQUE,
  name         TEXT NOT NULL DEFAULT '',
  icon         TEXT NOT NULL DEFAULT '',
  url_prefix   TEXT NOT NULL,
  fields       TEXT NOT NULL DEFAULT '[]',
  has_category INTEGER NOT NULL DEFAULT 0,
  searchable   INTEGER NOT NULL DEFAULT 1,
  hierarchical INTEGER NOT NULL DEFAULT 0,
  position     INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("迁移失败: %w", err)
	}
	// 旧库补列（幂等）——先补简单列，再做多语种结构迁移。
	s.addColumnIfMissing("posts", "featured", "INTEGER NOT NULL DEFAULT 0")
	s.addColumnIfMissing("posts", "editor_mode", "TEXT NOT NULL DEFAULT 'markdown'")
	s.addColumnIfMissing("posts", "comments_enabled", "INTEGER NOT NULL DEFAULT 0")
	s.addColumnIfMissing("categories", "position", "INTEGER NOT NULL DEFAULT 0")
	// 旧库（slug 全局唯一、无 lang 列）整体重建为多语种结构。
	if err := s.rebuildForI18n(); err != nil {
		return fmt.Errorf("多语种迁移失败: %w", err)
	}
	// 「链接」内容类型新增列（幂等，须在多语种重建之后补，确保重建后的表也带上）。
	s.addColumnIfMissing("posts", "link_url", "TEXT NOT NULL DEFAULT ''")
	// 「扩展」内容类型的自定义字段值（JSON）。幂等补列，随 store.Open 自动铺到所有站点库（含未来新建站点）。
	s.addColumnIfMissing("posts", "extra", "TEXT NOT NULL DEFAULT ''")
	s.addColumnIfMissing("categories", "kind", "TEXT NOT NULL DEFAULT 'post'")
	// 索引在表结构（含 lang/trans_group）就绪后统一创建，兼容新旧库。
	if err := s.createIndexes(); err != nil {
		return fmt.Errorf("索引创建失败: %w", err)
	}
	if err := s.createSearchIndex(); err != nil {
		return fmt.Errorf("搜索索引创建失败: %w", err)
	}
	if err := s.normalizeBundledAssetPaths(); err != nil {
		return fmt.Errorf("内置资源路径迁移失败: %w", err)
	}
	if err := s.normalizeShowcaseDefaults(); err != nil {
		return fmt.Errorf("产品演示设置迁移失败: %w", err)
	}
	if err := s.seedIfEmpty(); err != nil {
		return err
	}
	if err := s.repairEmptySiteBasePages(); err != nil {
		return fmt.Errorf("空站点基础页面修复失败: %w", err)
	}
	return nil
}

func (s *Store) normalizeBundledAssetPaths() error {
	replacements := map[string]string{
		"/assets/covers/caddy.svg":         "/assets/covers/caddy-brand.webp",
		"/assets/covers/caddy.webp":        "/assets/covers/caddy-brand.webp",
		"/assets/covers/deploy.svg":        "/assets/covers/release-package-real.webp",
		"/assets/covers/architecture.svg":  "/assets/covers/gcms-stack-brand.webp",
		"/assets/covers/i18n.svg":          "/assets/screenshots/language-switch.webp",
		"/assets/covers/editor.svg":        "/assets/screenshots/article-editor.webp",
		"/assets/covers/seo.svg":           "/assets/screenshots/seo-output.webp",
		"/assets/covers/themes.svg":        "/assets/screenshots/theme-settings.webp",
		"/assets/covers/automation.svg":    "/assets/screenshots/automation-api.webp",
		"/assets/covers/updates.svg":       "/assets/screenshots/system-updates.webp",
		"/assets/covers/release-repo.svg":  "/assets/covers/release-repo-real.webp",
		"/assets/covers/release-repo.webp": "/assets/covers/release-repo-real.webp",
		"/assets/covers/release.svg":       "/assets/covers/release-package-real.webp",
		"/assets/covers/release.webp":      "/assets/covers/release-package-real.webp",
		"/assets/covers/go.svg":            "/assets/covers/go-brand.webp",
		"/assets/covers/go-real.webp":      "/assets/covers/go-brand.webp",
		"/assets/covers/sqlite.svg":        "/assets/covers/sqlite-brand.webp",
		"/assets/covers/sqlite-real.webp":  "/assets/covers/sqlite-brand.webp",
	}
	for oldPath, newPath := range replacements {
		if _, err := s.db.Exec(`UPDATE posts SET cover_image=? WHERE cover_image=?`, newPath, oldPath); err != nil {
			return err
		}
	}
	contentReplacements := map[string]string{
		"/assets/figures/deploy-flow.svg":      "/assets/covers/release-package-real.webp",
		"/assets/figures/runtime-stack.svg":    "/assets/covers/gcms-stack-brand.webp",
		"/assets/figures/i18n-routing.svg":     "/assets/screenshots/language-switch.webp",
		"/assets/figures/editor-workflow.svg":  "/assets/screenshots/article-editor.webp",
		"/assets/figures/seo-checklist.svg":    "/assets/screenshots/seo-output.webp",
		"/assets/figures/theme-gallery.svg":    "/assets/screenshots/theme-settings.webp",
		"/assets/figures/automation-scope.svg": "/assets/screenshots/automation-api.webp",
		"/assets/figures/update-pipeline.svg":  "/assets/screenshots/system-updates.webp",
	}
	for oldPath, newPath := range contentReplacements {
		if _, err := s.db.Exec(`UPDATE posts SET content=replace(content, ?, ?) WHERE instr(content, ?) > 0`, oldPath, newPath, oldPath); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) normalizeShowcaseDefaults() error {
	demo := `EXISTS (SELECT 1 FROM settings WHERE key='demo.seed' AND value='showcase')`
	showcaseDescription := "gcms 把文章、页面、资源链接、全语种内容、主题、SEO/GEO、Cloudflare 静态部署、多站管理和 AI 自运营接口收进同一个后台；无需搭数据库服务和前端构建环境，一行命令即可部署，1 vCPU / 512MB 内存的小规格 VPS 也能稳定起步。"
	showcaseHeroEyebrow := "Cloudflare 部署 · 多站管理 · SEO/GEO · AI 自运营"
	showcaseDescriptionEN := "gcms brings posts, pages, resource links, multilingual content, themes, SEO/GEO, Cloudflare static deployment, multisite management and AI-operation APIs into one lightweight admin. No database server or frontend build pipeline required: deploy with one command and start on a 1 vCPU / 512MB VPS."
	showcaseHeroEyebrowEN := "Cloudflare deploy · Multisite · SEO/GEO · AI operations"
	showcaseKeywords := "gcms,CMS,内容管理系统,Cloudflare 静态部署,多站管理,SEO,GEO,AI 自运营"
	showcaseKeywordsEN := "gcms,CMS,content management,Cloudflare static deployment,multisite,SEO,GEO,AI operations"
	updates := []struct {
		key string
		old string
		new string
	}{
		{"site.tagline", "把内容站交付成一个可运行的二进制", "内容发布、搜索增长，一个后台跑通"},
		{"site.tagline", "搭一个能发布、能收录、能长期维护的网站", "内容发布、搜索增长，一个后台跑通"},
		{"site.tagline", "低配置也能跑的完整内容站", "内容发布、搜索增长，一个后台跑通"},
		{"site.tagline", "小机器也能跑的完整内容站", "内容发布、搜索增长，一个后台跑通"},
		{"site.tagline", "一个后台维护官网、文档和资源站", "内容发布、搜索增长，一个后台跑通"},
		{"site.description", "gcms 是面向产品官网、技术文档和轻量内容站的自托管 CMS：单个二进制启动，SQLite 单文件存储，服务端渲染默认做好 SEO，多语种、主题、在线升级和自动化接口开箱可用。", showcaseDescription},
		{"site.description", "gcms 适合产品官网、技术文档、资源导航和轻量内容站。自带后台、主题、多语种、SEO、在线升级和自动化接口，下载即可运行。", showcaseDescription},
		{"site.description", "gcms 适合产品官网、技术文档、资源导航和轻量内容站：单个二进制启动，SQLite 单文件存储，低配 VPS 也能部署；后台、主题、多语种、SEO、在线升级和自动化接口开箱可用。", showcaseDescription},
		{"site.description", "gcms 适合产品官网、技术文档、资源导航和轻量内容站：单个二进制启动，SQLite 单文件存储，低配 VPS 也能部署；后台、主题、多语种、SEO、在线升级都开箱可用，并支持 AI 协助运营。", showcaseDescription},
		{"site.description", "gcms 适合产品官网、技术文档、资源导航和轻量内容站：单个二进制启动，SQLite 单文件存储，小规格 VPS 也能部署；后台、主题、全语种内容、SEO、在线升级和 AI 运营都开箱可用。", showcaseDescription},
		{"site.description", "gcms 适合产品官网、技术文档、资源导航和轻量内容站：单个二进制启动，SQLite 单文件存储，小规格 VPS 也能部署；后台、主题、全语种内容、SEO、在线升级和 AI 自运营接口都开箱可用。", showcaseDescription},
		{"site.description", "gcms 适合产品官网、技术文档、资源导航和轻量内容站：单个二进制启动，SQLite 单文件存储，小规格 VPS 也能部署；后台、主题、全语种内容、SEO/GEO、在线升级和 AI 自运营接口都开箱可用。", showcaseDescription},
		{"site.description", "gcms 把内容发布、全语种管理、主题、SEO/GEO、在线升级和 AI 自运营接口收进同一个后台；单个二进制加 SQLite 文件即可部署，适合小团队把官网、文档和资源站长期跑稳。", showcaseDescription},
		{"site.description", "gcms 把文章、页面、资源链接、全语种内容、主题、SEO/GEO、在线升级和 AI 自运营接口收进同一个后台；单个二进制加 SQLite 文件即可部署，小团队也能长期维护。", showcaseDescription},
		{"site.description", "gcms 把文章、页面、资源链接、全语种内容、主题、SEO/GEO、在线升级和 AI 自运营接口收进同一个后台；无需搭数据库服务和前端构建环境，一行命令即可部署，1 vCPU / 512MB 内存的小规格 VPS 也能稳定起步。", showcaseDescription},
		{"site.hero_eyebrow", "产品官网 · 技术文档 · 自托管内容站", showcaseHeroEyebrow},
		{"site.hero_eyebrow", "产品官网 · 技术文档 · 资源导航 · 轻量内容站", showcaseHeroEyebrow},
		{"site.hero_eyebrow", "低配置部署 · SEO 就绪 · 自托管内容站", showcaseHeroEyebrow},
		{"site.hero_eyebrow", "轻部署 · SEO 就绪 · 全语种覆盖", showcaseHeroEyebrow},
		{"site.hero_eyebrow", "轻部署 · SEO/GEO 就绪 · 全语种覆盖", showcaseHeroEyebrow},
		{"site.hero_eyebrow", "轻部署 · SEO/GEO 就绪 · AI 自运营", showcaseHeroEyebrow},
		{"site.hero_title", "一个二进制，\n上线一个完整\n内容站。", "内容发布、\n搜索增长，\n一个后台跑通"},
		{"site.hero_title", "搭一个能发布、\n能收录、能长期\n维护的网站", "内容发布、\n搜索增长，\n一个后台跑通"},
		{"site.hero_title", "小机器，\n也能跑起完整\n内容站", "内容发布、\n搜索增长，\n一个后台跑通"},
		{"site.hero_title", "官网、文档\n和资源站，\n一个后台维护", "内容发布、\n搜索增长，\n一个后台跑通"},
		{"site.tagline::en", "Ship a complete content site as one binary", "Publish content and grow search from one admin"},
		{"site.tagline::en", "Build a website you can publish, index and maintain", "Publish content and grow search from one admin"},
		{"site.tagline::en", "A complete content site that runs on small servers", "Publish content and grow search from one admin"},
		{"site.tagline::en", "One admin for sites, docs and resources", "Publish content and grow search from one admin"},
		{"site.description::en", "gcms is a self-hosted CMS for product sites, docs and lightweight content hubs: one binary to run, SQLite as a single-file store, server-rendered SEO by default, with multilingual content, themes, in-app updates and automation APIs built in.", showcaseDescriptionEN},
		{"site.description::en", "gcms fits product sites, docs, resource directories and lightweight content hubs. It ships with an admin, themes, multilingual content, SEO, in-app updates and automation APIs, ready to run after download.", showcaseDescriptionEN},
		{"site.description::en", "gcms fits product sites, docs, resource directories and lightweight content hubs: one binary, one SQLite file, deployable on a low-end VPS, with admin, themes, multilingual content, SEO, in-app updates and automation APIs out of the box.", showcaseDescriptionEN},
		{"site.description::en", "gcms fits product sites, docs, resource directories and lightweight content hubs: one binary, one SQLite file, deployable on a low-end VPS, with admin, themes, multilingual content, SEO, in-app updates and AI-assisted operations out of the box.", showcaseDescriptionEN},
		{"site.description::en", "gcms fits product sites, docs, resource directories and lightweight content hubs: one binary, one SQLite file, deployable on small VPS instances, with admin, themes, multilingual content, SEO, in-app updates and AI operations out of the box.", showcaseDescriptionEN},
		{"site.description::en", "gcms fits product sites, docs, resource directories and lightweight content hubs: one binary, one SQLite file, deployable on small VPS instances, with admin, themes, multilingual content, SEO/GEO, in-app updates and AI operations out of the box.", showcaseDescriptionEN},
		{"site.description::en", "gcms brings publishing, multilingual content, themes, SEO/GEO, in-app updates and AI-operation APIs into one lightweight admin. Deploy one binary with one SQLite file, then keep product sites, docs and resource hubs easy to maintain.", showcaseDescriptionEN},
		{"site.description::en", "gcms brings posts, pages, resource links, multilingual content, themes, SEO/GEO, in-app updates and AI-operation APIs into one lightweight admin. Deploy one binary with one SQLite file, then keep content operations easy to maintain.", showcaseDescriptionEN},
		{"site.description::en", "gcms brings posts, pages, resource links, multilingual content, themes, SEO/GEO, in-app updates and AI-operation APIs into one lightweight admin. No database server or frontend build pipeline required: deploy with one command and start on a 1 vCPU / 512MB VPS.", showcaseDescriptionEN},
		{"site.hero_eyebrow::en", "Product sites · Docs · Self-hosted content", showcaseHeroEyebrowEN},
		{"site.hero_eyebrow::en", "Product sites · Docs · Resource directories · Content hubs", showcaseHeroEyebrowEN},
		{"site.hero_eyebrow::en", "Low-resource deploys · SEO-ready · Self-hosted CMS", showcaseHeroEyebrowEN},
		{"site.hero_eyebrow::en", "Easy deploy · SEO-ready · Multilingual", showcaseHeroEyebrowEN},
		{"site.hero_eyebrow::en", "Easy deploy · SEO/GEO-ready · Multilingual", showcaseHeroEyebrowEN},
		{"site.hero_eyebrow::en", "Self-hosted · SEO/GEO-ready · AI operations", showcaseHeroEyebrowEN},
		{"site.hero_title::en", "One binary,\none complete content site.", "Publish content,\ngrow search traffic,\nrun it from one admin"},
		{"site.hero_title::en", "Build a site that\npublishes, ranks\nand stays easy to run", "Publish content,\ngrow search traffic,\nrun it from one admin"},
		{"site.hero_title::en", "A small server\ncan run a complete\ncontent site", "Publish content,\ngrow search traffic,\nrun it from one admin"},
		{"site.hero_title::en", "Run your site,\ndocs and resources\nfrom one admin", "Publish content,\ngrow search traffic,\nrun it from one admin"},
		{"site.share_image", "", "/assets/og-cover.webp"},
		{"site.share_image::en", "", "/assets/og-cover-en.webp"},
		{"hero.image", "", "/assets/hero-product-overview-brand.webp"},
		{"hero.image", "/assets/figures/gcms-showcase-hero.svg", "/assets/hero-product-overview-brand.webp"},
		{"hero.image", "/assets/hero-product-overview.webp", "/assets/hero-product-overview-brand.webp"},
		{"hero.image::en", "", "/assets/hero-product-overview-brand-en.webp"},
	}
	for _, u := range updates {
		if _, err := s.db.Exec(`UPDATE settings SET value=? WHERE key=? AND value=? AND `+demo, u.new, u.key, u.old); err != nil {
			return err
		}
	}
	inserts := map[string]string{
		"site.keywords":             showcaseKeywords,
		"site.keywords::en":         showcaseKeywordsEN,
		"site.hero_description":     showcaseDescription,
		"site.hero_description::en": showcaseDescriptionEN,
		"site.share_image":          "/assets/og-cover.webp",
		"site.share_image::en":      "/assets/og-cover-en.webp",
		"hero.image":                "/assets/hero-product-overview-brand.webp",
		"hero.image::en":            "/assets/hero-product-overview-brand-en.webp",
		"hero.visual":               "image",
		"hero.visual::en":           "image",
	}
	for key, value := range inserts {
		if _, err := s.db.Exec(`INSERT INTO settings(key,value)
			SELECT ?, ? WHERE `+demo+` AND NOT EXISTS (SELECT 1 FROM settings WHERE key=?)`, key, value, key); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`UPDATE settings SET value='image' WHERE key='hero.visual' AND value='' AND ` + demo); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE posts
		SET slug='start', trans_group='s-page-start'
		WHERE type='page' AND slug='contact' AND trans_group='s-page-contact' AND ` + demo + `
		AND NOT EXISTS (SELECT 1 FROM posts p2 WHERE p2.lang=posts.lang AND p2.slug='start')`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE settings
		SET value=replace(value, '"/contact"', '"/start"')
		WHERE key='nav_menu' AND instr(value, '"/contact"') > 0 AND ` + demo); err != nil {
		return err
	}
	zhStartEnv := "\n\n" + md(
		"## 环境要求",
		"gcms 不是重型平台，小规格服务器就能跑起来，适合先用很小的成本把产品官网、技术文档或资源导航上线。",
		"",
		"- **CPU**：1 vCPU 即可启动并支撑日常内容发布；",
		"- **内存**：512MB 级别 VPS 可以部署普通产品官网、文档站或资源导航；",
		"- **磁盘**：程序包很小，主要占用来自 SQLite 数据库、上传图片和日志；",
		"- **系统**：Linux、macOS、Windows 均提供 amd64 / arm64 发布包；",
		"- **公网入口**：生产环境推荐用 Caddy 监听 80/443，gcms 监听 `127.0.0.1:8080`。",
		"",
		"后续内容量或访问量变大，再按实际情况增加内存和磁盘即可。",
	)
	if _, err := s.db.Exec(`UPDATE posts
		SET content=replace(content, ?, ?)
		WHERE type='page' AND lang='zh' AND slug='start' AND instr(content, '## 环境要求') = 0 AND instr(content, '## 部署建议') > 0 AND `+demo,
		"\n\n## 部署建议", zhStartEnv+"\n\n## 部署建议"); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE posts
		SET content=content || ?
		WHERE type='page' AND lang='zh' AND slug='start' AND instr(content, '## 环境要求') = 0 AND `+demo, zhStartEnv); err != nil {
		return err
	}
	enStartEnv := "\n\n" + md(
		"## Environment requirements",
		"gcms is not a heavy platform. A small server is enough to launch a product site, docs hub or resource directory before you scale up.",
		"",
		"- **CPU**: 1 vCPU is enough to start and handle normal publishing work;",
		"- **Memory**: a 512MB VPS can deploy a regular product site, docs site or resource directory;",
		"- **Disk**: the package is small; most growth comes from the SQLite database, uploads and logs;",
		"- **OS**: Linux, macOS and Windows packages are available for amd64 / arm64;",
		"- **Public entry**: in production, put Caddy on 80/443 and bind gcms to `127.0.0.1:8080`.",
		"",
		"When content volume or traffic grows, increase memory and disk based on real usage.",
	)
	if _, err := s.db.Exec(`UPDATE posts
		SET content=replace(content, ?, ?)
		WHERE type='page' AND lang='en' AND slug='start' AND instr(content, '## Environment requirements') = 0 AND instr(content, '## Deployment suggestion') > 0 AND `+demo,
		"\n\n## Deployment suggestion", enStartEnv+"\n\n## Deployment suggestion"); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE posts
		SET content=content || ?
		WHERE type='page' AND lang='en' AND slug='start' AND instr(content, '## Environment requirements') = 0 AND `+demo, enStartEnv); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE posts
		SET content=replace(content, '## 系统要求很低', '## 环境要求')
		WHERE type='post' AND lang='zh' AND slug='deploy-in-5-minutes' AND instr(content, '## 系统要求很低') > 0 AND ` + demo); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE posts
		SET content=replace(content, '## The requirements are small', '## Environment requirements')
		WHERE type='post' AND lang='en' AND slug='deploy-in-5-minutes' AND instr(content, '## The requirements are small') > 0 AND ` + demo); err != nil {
		return err
	}
	enScreenshots := map[string]string{
		"/assets/screenshots/language-switch.webp":    "/assets/screenshots/language-switch-en.webp",
		"/assets/screenshots/article-editor.webp":     "/assets/screenshots/article-editor-en.webp",
		"/assets/screenshots/seo-output.webp":         "/assets/screenshots/seo-output-en.webp",
		"/assets/screenshots/theme-settings.webp":     "/assets/screenshots/theme-settings-en.webp",
		"/assets/screenshots/automation-api.webp":     "/assets/screenshots/automation-api-en.webp",
		"/assets/screenshots/system-updates.webp":     "/assets/screenshots/system-updates-en.webp",
		"/assets/screenshots/visual-editor-home.webp": "/assets/screenshots/visual-editor-home-en.webp",
		"/assets/screenshots/navigation-menu.webp":    "/assets/screenshots/navigation-menu-en.webp",
		"/assets/screenshots/security-demo-data.webp": "/assets/screenshots/security-demo-data-en.webp",
	}
	for oldPath, newPath := range enScreenshots {
		if _, err := s.db.Exec(`UPDATE posts
			SET cover_image=?
			WHERE lang='en' AND cover_image=? AND `+demo, newPath, oldPath); err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE posts
			SET content=replace(content, ?, ?)
			WHERE lang='en' AND instr(content, ?) > 0 AND `+demo, oldPath, newPath, oldPath); err != nil {
			return err
		}
	}
	enCovers := map[string]string{
		"/assets/covers/release-package-real.webp": "/assets/covers/release-package-real-en.webp",
		"/assets/covers/gcms-stack-brand.webp":     "/assets/covers/gcms-stack-brand-en.webp",
		"/assets/covers/release-repo-real.webp":    "/assets/covers/release-repo-real-en.webp",
		"/assets/covers/site.webp":                 "/assets/covers/site-en.webp",
		"/assets/covers/go-brand.webp":             "/assets/covers/go-brand-en.webp",
		"/assets/covers/sqlite-brand.webp":         "/assets/covers/sqlite-brand-en.webp",
		"/assets/covers/caddy-brand.webp":          "/assets/covers/caddy-brand-en.webp",
	}
	for oldPath, newPath := range enCovers {
		if _, err := s.db.Exec(`UPDATE posts
			SET cover_image=?
			WHERE lang='en' AND cover_image=? AND `+demo, newPath, oldPath); err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE posts
			SET content=replace(content, ?, ?)
			WHERE lang='en' AND instr(content, ?) > 0 AND `+demo, oldPath, newPath, oldPath); err != nil {
			return err
		}
	}
	return nil
}

// createIndexes 在 posts 表确定具备 lang/trans_group 列后统一建立索引。
func (s *Store) createIndexes() error {
	stmts := []string{
		`CREATE INDEX IF NOT EXISTS idx_posts_list ON posts(lang, type, status, published_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_category ON posts(category_id)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_category_list ON posts(category_id, type, status, published_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_featured ON posts(lang, type, status, featured, published_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_group ON posts(trans_group)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_due ON posts(status, published_at)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_admin_status_updated ON posts(lang, type, status, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_admin_updated ON posts(lang, type, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_recent_updated ON posts(lang, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_automation_updated ON posts(type, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_posts_type_slug ON posts(type, slug)`,
		`CREATE INDEX IF NOT EXISTS idx_categories_kind_lang_position ON categories(kind, lang, position, id)`,
		`CREATE INDEX IF NOT EXISTS idx_categories_group ON categories(trans_group)`,
		`CREATE INDEX IF NOT EXISTS idx_automation_logs_created ON automation_logs(created_at DESC)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) createSearchIndex() error {
	for _, q := range []string{
		`DROP TRIGGER IF EXISTS posts_search_ai`,
		`DROP TRIGGER IF EXISTS posts_search_au`,
		`DROP TRIGGER IF EXISTS posts_search_ad`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	if !s.hasColumn("post_search", "meta_desc") || !s.hasColumn("post_search", "keywords") {
		if _, err := s.db.Exec(`DROP TABLE IF EXISTS post_search`); err != nil {
			return err
		}
	}
	stmts := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS post_search USING fts5(
			title, excerpt, content, meta_desc, keywords,
			lang UNINDEXED, type UNINDEXED, status UNINDEXED, published_at UNINDEXED,
			tokenize='trigram'
		)`,
		`CREATE TRIGGER posts_search_ai AFTER INSERT ON posts BEGIN
			INSERT INTO post_search(rowid,title,excerpt,content,meta_desc,keywords,lang,type,status,published_at)
			VALUES(new.id,new.title,new.excerpt,new.content,new.meta_desc,new.keywords,new.lang,new.type,new.status,new.published_at);
		END`,
		`CREATE TRIGGER posts_search_au AFTER UPDATE ON posts BEGIN
			DELETE FROM post_search WHERE rowid=old.id;
			INSERT INTO post_search(rowid,title,excerpt,content,meta_desc,keywords,lang,type,status,published_at)
			VALUES(new.id,new.title,new.excerpt,new.content,new.meta_desc,new.keywords,new.lang,new.type,new.status,new.published_at);
		END`,
		`CREATE TRIGGER posts_search_ad AFTER DELETE ON posts BEGIN
			DELETE FROM post_search WHERE rowid=old.id;
		END`,
		`INSERT OR REPLACE INTO post_search(rowid,title,excerpt,content,meta_desc,keywords,lang,type,status,published_at)
			SELECT id,title,excerpt,content,meta_desc,keywords,lang,type,status,published_at FROM posts`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

// hasColumn 判断某表是否已有某列。
func (s *Store) hasColumn(table, col string) bool {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err == nil && name == col {
			return true
		}
	}
	return false
}

// addColumnIfMissing 在列不存在时 ALTER 添加（用于已存在的旧数据库平滑升级）。
func (s *Store) addColumnIfMissing(table, col, def string) {
	if s.hasColumn(table, col) {
		return
	}
	_, _ = s.db.Exec("ALTER TABLE " + table + " ADD COLUMN " + col + " " + def)
}

// rebuildForI18n 把「slug 全局唯一、无 lang」的旧表重建为「(lang,slug) 复合唯一 + lang/trans_group」。
// 仅当 posts 还没有 lang 列时执行一次。已有数据全部归入默认语种 zh，trans_group 取 'zh:'||slug。
func (s *Store) rebuildForI18n() error {
	if s.hasColumn("posts", "lang") {
		return nil
	}
	// 重建期间临时关闭外键（posts 引用 categories）。PRAGMA 不能在事务内生效。
	_, _ = s.db.Exec("PRAGMA foreign_keys=OFF")
	defer s.db.Exec("PRAGMA foreign_keys=ON")

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmts := []string{
		// 分类
		`CREATE TABLE categories_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			slug TEXT NOT NULL, name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '', position INTEGER NOT NULL DEFAULT 0,
			lang TEXT NOT NULL DEFAULT 'zh', trans_group TEXT NOT NULL DEFAULT '',
			UNIQUE(lang, slug))`,
		`INSERT INTO categories_new(id,slug,name,description,position,lang,trans_group)
			SELECT id,slug,name,COALESCE(description,''),COALESCE(position,0),'zh','zh:'||slug FROM categories`,
		`DROP TABLE categories`,
		`ALTER TABLE categories_new RENAME TO categories`,
		// 文章
		`CREATE TABLE posts_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT NOT NULL DEFAULT 'post', slug TEXT NOT NULL, title TEXT NOT NULL,
			excerpt TEXT NOT NULL DEFAULT '', content TEXT NOT NULL DEFAULT '',
			meta_desc TEXT NOT NULL DEFAULT '', keywords TEXT NOT NULL DEFAULT '',
			cover_image TEXT NOT NULL DEFAULT '', author TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'draft', featured INTEGER NOT NULL DEFAULT 0,
			editor_mode TEXT NOT NULL DEFAULT 'markdown',
			comments_enabled INTEGER NOT NULL DEFAULT 0,
			lang TEXT NOT NULL DEFAULT 'zh', trans_group TEXT NOT NULL DEFAULT '',
			category_id INTEGER REFERENCES categories(id) ON DELETE SET NULL,
			published_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			UNIQUE(lang, slug))`,
		`INSERT INTO posts_new(id,type,slug,title,excerpt,content,meta_desc,keywords,cover_image,author,status,featured,editor_mode,comments_enabled,lang,trans_group,category_id,published_at,created_at,updated_at)
			SELECT id,type,slug,title,COALESCE(excerpt,''),COALESCE(content,''),COALESCE(meta_desc,''),COALESCE(keywords,''),
			       COALESCE(cover_image,''),COALESCE(author,''),COALESCE(status,'draft'),COALESCE(featured,0),COALESCE(editor_mode,'markdown'),
			       COALESCE(comments_enabled,0),'zh','zh:'||slug,category_id,published_at,created_at,updated_at FROM posts`,
		`DROP TABLE posts`,
		`ALTER TABLE posts_new RENAME TO posts`,
	}
	for _, q := range stmts {
		if _, err := tx.Exec(q); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// ---------- 时间辅助 ----------

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func parseTime(s sql.NullString) time.Time {
	if !s.Valid || s.String == "" {
		return time.Time{}
	}
	for _, f := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(f, s.String); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ---------- 后台会话 ----------

type AdminSession struct {
	User          string
	CSRF          string
	ExpiresAt     time.Time
	PwDismissed   bool
	CurrentSiteID int64
}

func sessionTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateAdminSession(token, user, csrf string, expiresAt time.Time) error {
	now := time.Now()
	_, _ = s.db.Exec(`DELETE FROM admin_sessions WHERE expires_at<=?`, fmtTime(now))
	_, err := s.db.Exec(`INSERT INTO admin_sessions(token_hash,user,csrf,expires_at,pw_dismissed,created_at,updated_at)
		VALUES(?,?,?,?,0,?,?)`,
		sessionTokenHash(token), user, csrf, fmtTime(expiresAt), fmtTime(now), fmtTime(now))
	return err
}

func (s *Store) GetAdminSession(token string) (AdminSession, bool, error) {
	var sess AdminSession
	var expires string
	var dismissed int
	err := s.db.QueryRow(`SELECT user,csrf,expires_at,pw_dismissed FROM admin_sessions WHERE token_hash=?`, sessionTokenHash(token)).
		Scan(&sess.User, &sess.CSRF, &expires, &dismissed)
	if err == sql.ErrNoRows {
		return AdminSession{}, false, nil
	}
	if err != nil {
		return AdminSession{}, false, err
	}
	t, err := time.Parse(time.RFC3339, expires)
	if err != nil || time.Now().After(t) {
		_ = s.DeleteAdminSession(token)
		return AdminSession{}, false, nil
	}
	sess.ExpiresAt = t
	sess.PwDismissed = dismissed == 1
	return sess, true, nil
}

func (s *Store) DeleteAdminSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM admin_sessions WHERE token_hash=?`, sessionTokenHash(token))
	return err
}

func (s *Store) DismissAdminPasswordWarning(token string) error {
	_, err := s.db.Exec(`UPDATE admin_sessions SET pw_dismissed=1,updated_at=? WHERE token_hash=?`, fmtTime(time.Now()), sessionTokenHash(token))
	return err
}

func (s *Store) SetAdminSessionSite(token string, siteID int64) error {
	return nil
}

// ---------- 自动化 API Key ----------

type AutomationKey struct {
	ID          int64
	Name        string
	TokenPrefix string
	Scopes      string
	LastUsedAt  time.Time
	CreatedAt   time.Time
	RevokedAt   time.Time
}

type AutomationLog struct {
	ID         int64
	KeyID      int64
	KeyName    string
	Action     string
	TargetType string
	TargetID   int64
	Message    string
	CreatedAt  time.Time
}

func (k *AutomationKey) ScopeList() []string {
	var out []string
	for _, s := range strings.Split(k.Scopes, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func automationTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateAutomationKey(name, token, prefix, scopes string) (int64, error) {
	now := fmtTime(time.Now())
	res, err := s.db.Exec(`INSERT INTO automation_keys(name,token_hash,token_prefix,scopes,created_at)
		VALUES(?,?,?,?,?)`, name, automationTokenHash(token), prefix, scopes, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListAutomationKeys() ([]*AutomationKey, error) {
	rows, err := s.db.Query(`SELECT id,name,token_prefix,scopes,last_used_at,created_at,revoked_at
		FROM automation_keys ORDER BY revoked_at IS NOT NULL, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AutomationKey
	for rows.Next() {
		k, err := scanAutomationKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) GetAutomationKeyByID(id int64) (*AutomationKey, bool, error) {
	row := s.db.QueryRow(`SELECT id,name,token_prefix,scopes,last_used_at,created_at,revoked_at
		FROM automation_keys WHERE id=?`, id)
	k, err := scanAutomationKey(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return k, true, nil
}

func (s *Store) GetAutomationKeyByToken(token string) (*AutomationKey, bool, error) {
	row := s.db.QueryRow(`SELECT id,name,token_prefix,scopes,last_used_at,created_at,revoked_at
		FROM automation_keys WHERE token_hash=? AND revoked_at IS NULL`, automationTokenHash(token))
	k, err := scanAutomationKey(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return k, true, nil
}

func (s *Store) RegenerateAutomationKey(id int64, token, prefix string) error {
	res, err := s.db.Exec(`UPDATE automation_keys
		SET token_hash=?, token_prefix=?, last_used_at=NULL
		WHERE id=? AND revoked_at IS NULL`, automationTokenHash(token), prefix, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateAutomationKey(id int64, name, scopes string) error {
	res, err := s.db.Exec(`UPDATE automation_keys
		SET name=?, scopes=?
		WHERE id=? AND revoked_at IS NULL`, name, scopes, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) TouchAutomationKey(id int64) error {
	_, err := s.db.Exec(`UPDATE automation_keys SET last_used_at=? WHERE id=?`, fmtTime(time.Now()), id)
	return err
}

func (s *Store) RevokeAutomationKey(id int64) error {
	_, err := s.db.Exec(`UPDATE automation_keys SET revoked_at=COALESCE(revoked_at,?) WHERE id=?`, fmtTime(time.Now()), id)
	return err
}

func (s *Store) DeleteRevokedAutomationKey(id int64) error {
	res, err := s.db.Exec(`DELETE FROM automation_keys WHERE id=? AND revoked_at IS NOT NULL`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func scanAutomationKey(sc interface{ Scan(...any) error }) (*AutomationKey, error) {
	var k AutomationKey
	var last, created, revoked sql.NullString
	if err := sc.Scan(&k.ID, &k.Name, &k.TokenPrefix, &k.Scopes, &last, &created, &revoked); err != nil {
		return nil, err
	}
	k.LastUsedAt = parseTime(last)
	k.CreatedAt = parseTime(created)
	k.RevokedAt = parseTime(revoked)
	return &k, nil
}

func (s *Store) CreateAutomationLog(keyID int64, action, targetType string, targetID int64, message string) error {
	// 平台密钥（多站）请求不属于本站 automation_keys，keyID<=0 时直接跳过，
	// 避免向 FK 列写入无效引用（平台审计另存 platform_automation_logs）。
	if keyID <= 0 {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO automation_logs(key_id,action,target_type,target_id,message,created_at)
		VALUES(?,?,?,?,?,?)`, keyID, action, targetType, targetID, message, fmtTime(time.Now()))
	return err
}

func (s *Store) ListAutomationLogs(limit int) ([]*AutomationLog, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT l.id,COALESCE(l.key_id,0),COALESCE(k.name,''),l.action,l.target_type,l.target_id,l.message,l.created_at
		FROM automation_logs l LEFT JOIN automation_keys k ON k.id=l.key_id
		ORDER BY l.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AutomationLog
	for rows.Next() {
		var l AutomationLog
		var created sql.NullString
		if err := rows.Scan(&l.ID, &l.KeyID, &l.KeyName, &l.Action, &l.TargetType, &l.TargetID, &l.Message, &created); err != nil {
			return nil, err
		}
		l.CreatedAt = parseTime(created)
		out = append(out, &l)
	}
	return out, rows.Err()
}

// ---------- 查询：公开站点 ----------

const postCols = `p.id,p.type,p.slug,p.title,p.excerpt,p.content,p.meta_desc,p.keywords,
	p.cover_image,p.author,p.status,p.featured,p.editor_mode,p.comments_enabled,p.link_url,p.lang,p.trans_group,p.extra,p.category_id,p.published_at,p.created_at,p.updated_at,
	c.id,c.slug,c.name,c.description`

const postSummaryCols = `p.id,p.type,p.slug,p.title,p.excerpt,'' AS content,p.meta_desc,p.keywords,
	p.cover_image,p.author,p.status,p.featured,p.editor_mode,p.comments_enabled,p.link_url,p.lang,p.trans_group,p.extra,p.category_id,p.published_at,p.created_at,p.updated_at,
	c.id,c.slug,c.name,c.description,length(p.content)`

func scanPost(sc interface{ Scan(...any) error }, hasContentLen bool) (*Post, error) {
	var p Post
	var pub, created, updated sql.NullString
	var cID sql.NullInt64
	var cSlug, cName, cDesc sql.NullString
	var featured int
	var commentsEnabled int
	var contentLen sql.NullInt64
	dest := []any{&p.ID, &p.Type, &p.Slug, &p.Title, &p.Excerpt, &p.Content, &p.MetaDesc,
		&p.Keywords, &p.CoverImage, &p.Author, &p.Status, &featured, &p.EditorMode, &commentsEnabled, &p.LinkURL, &p.Lang, &p.TransGroup, &p.Extra,
		&p.CategoryID, &pub, &created, &updated,
		&cID, &cSlug, &cName, &cDesc}
	if hasContentLen {
		dest = append(dest, &contentLen)
	}
	err := sc.Scan(dest...)
	if err != nil {
		return nil, err
	}
	p.Featured = featured != 0
	p.CommentsEnabled = commentsEnabled != 0
	if contentLen.Valid {
		p.ContentLen = int(contentLen.Int64)
	} else if p.Content != "" {
		p.ContentLen = len([]rune(p.Content))
	}
	p.PublishedAt = parseTime(pub)
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	if cID.Valid {
		p.Category = &Category{ID: cID.Int64, Slug: cSlug.String, Name: cName.String, Description: cDesc.String}
	}
	return &p, nil
}

func (s *Store) queryPosts(where string, args ...any) ([]*Post, error) {
	q := `SELECT ` + postCols + ` FROM posts p LEFT JOIN categories c ON c.id = p.category_id ` + where
	return s.queryPostRows(q, false, args...)
}

func (s *Store) queryPostSummaries(where string, args ...any) ([]*Post, error) {
	q := `SELECT ` + postSummaryCols + ` FROM posts p LEFT JOIN categories c ON c.id = p.category_id ` + where
	return s.queryPostRows(q, true, args...)
}

func (s *Store) queryPostRows(q string, hasContentLen bool, args ...any) ([]*Post, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Post
	for rows.Next() {
		p, err := scanPost(rows, hasContentLen)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListPublished 返回某语种已发布文章（按发布时间倒序，分页）。
func (s *Store) ListPublished(lang string, offset, limit int) ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' AND p.lang=?
		ORDER BY p.published_at DESC LIMIT ? OFFSET ?`, lang, limit, offset)
}

func (s *Store) CountPublished(lang string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM posts WHERE type='post' AND status='published' AND lang=?`, lang).Scan(&n)
	return n, err
}

// GetPostBySlug 取某语种单篇文章。includeDrafts 为 true 时也返回草稿（供后台预览）。
func (s *Store) GetPostBySlug(lang, slug string, includeDrafts bool) (*Post, error) {
	where := `WHERE p.slug=? AND p.lang=? AND p.type='post'`
	if !includeDrafts {
		where += ` AND p.status='published'`
	}
	posts, err := s.queryPosts(where+` LIMIT 1`, slug, lang)
	if err != nil || len(posts) == 0 {
		return nil, err
	}
	return posts[0], nil
}

// GetPage 取某语种单页（type=page），如 about。
func (s *Store) GetPage(lang, slug string) (*Post, error) {
	posts, err := s.queryPosts(`WHERE p.slug=? AND p.lang=? AND p.type='page' AND p.status='published' LIMIT 1`, slug, lang)
	if err != nil || len(posts) == 0 {
		return nil, err
	}
	return posts[0], nil
}

func (s *Store) ListByCategory(catID int64, offset, limit int) ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' AND p.category_id=?
		ORDER BY p.published_at DESC LIMIT ? OFFSET ?`, catID, limit, offset)
}

func (s *Store) CountByCategory(catID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM posts WHERE type='post' AND status='published' AND category_id=?`, catID).Scan(&n)
	return n, err
}

func (s *Store) GetCategoryBySlug(lang, slug string) (*Category, error) {
	var c Category
	err := s.db.QueryRow(`SELECT id,slug,name,description,position,lang,trans_group,kind FROM categories WHERE slug=? AND lang=?`, slug, lang).
		Scan(&c.ID, &c.Slug, &c.Name, &c.Description, &c.Position, &c.Lang, &c.TransGroup, &c.Kind)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &c, err
}

func (s *Store) GetCategoryByID(id int64) (*Category, error) {
	var c Category
	err := s.db.QueryRow(`SELECT id,slug,name,description,position,lang,trans_group,kind FROM categories WHERE id=?`, id).
		Scan(&c.ID, &c.Slug, &c.Name, &c.Description, &c.Position, &c.Lang, &c.TransGroup, &c.Kind)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &c, err
}

func (s *Store) CreateCategory(c *Category) (int64, error) {
	c.Lang = nz(c.Lang, "zh")
	c.Kind = nz(c.Kind, "post")
	if c.TransGroup == "" {
		c.TransGroup = c.Lang + ":" + c.Slug
	}
	var pos int
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(position),-1)+1 FROM categories WHERE lang=? AND kind=?`, c.Lang, c.Kind).Scan(&pos)
	res, err := s.db.Exec(`INSERT INTO categories(slug,name,description,position,lang,trans_group,kind) VALUES(?,?,?,?,?,?,?)`,
		c.Slug, c.Name, c.Description, pos, c.Lang, c.TransGroup, c.Kind)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateCategory(c *Category) error {
	_, err := s.db.Exec(`UPDATE categories SET slug=?,name=?,description=?,trans_group=? WHERE id=?`,
		c.Slug, c.Name, c.Description, c.TransGroup, c.ID)
	return err
}

func (s *Store) DeleteCategory(id int64) error {
	// 外键 ON DELETE SET NULL：文章的 category_id 自动置空。
	_, err := s.db.Exec(`DELETE FROM categories WHERE id=?`, id)
	return err
}

func (s *Store) CategorySlugExists(lang, slug string, exceptID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM categories WHERE slug=? AND lang=? AND id<>?`, slug, lang, exceptID).Scan(&n)
	return n > 0, err
}

// ListCategories 返回某语种、某类型（post|link）的全部分类及各自已发布条目数。
func (s *Store) ListCategories(lang, kind string) ([]*Category, error) {
	return s.scanCategories(`
		SELECT c.id,c.slug,c.name,c.description,c.position,c.lang,c.trans_group,c.kind,
			(SELECT COUNT(*) FROM posts p WHERE p.category_id=c.id AND p.status='published')
		FROM categories c WHERE c.lang=? AND c.kind=? ORDER BY c.position, c.id`, lang, kind)
}

// AllCategories 返回所有语种、某类型的分类（供 sitemap；kind 为空则全部）。
func (s *Store) AllCategories(kind string) ([]*Category, error) {
	where := ""
	var args []any
	if kind != "" {
		where = "WHERE c.kind=?"
		args = append(args, kind)
	}
	return s.scanCategories(`
		SELECT c.id,c.slug,c.name,c.description,c.position,c.lang,c.trans_group,c.kind,
			(SELECT COUNT(*) FROM posts p WHERE p.category_id=c.id AND p.status='published')
		FROM categories c `+where+` ORDER BY c.lang, c.position, c.id`, args...)
}

// CategoryTranslations 返回与某 trans_group 关联的各语种分类（互译版本）。
func (s *Store) CategoryTranslations(group string) ([]*Category, error) {
	if group == "" {
		return nil, nil
	}
	return s.scanCategories(`
		SELECT c.id,c.slug,c.name,c.description,c.position,c.lang,c.trans_group,c.kind,0
		FROM categories c WHERE c.trans_group=? ORDER BY c.lang`, group)
}

func (s *Store) scanCategories(q string, args ...any) ([]*Category, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Slug, &c.Name, &c.Description, &c.Position, &c.Lang, &c.TransGroup, &c.Kind, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// ReorderCategories 按给定 id 顺序写入 position。
func (s *Store) ReorderCategories(ids []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE categories SET position=? WHERE id=?`, i, id); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// PrevPost / NextPost 用于文章详情页的上一篇/下一篇导航（同语种内）。
func (s *Store) PrevPost(p *Post) (*Post, error) {
	posts, err := s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' AND p.lang=? AND p.published_at < ?
		ORDER BY p.published_at DESC LIMIT 1`, p.Lang, fmtTime(p.PublishedAt))
	if err != nil || len(posts) == 0 {
		return nil, err
	}
	return posts[0], nil
}

func (s *Store) NextPost(p *Post) (*Post, error) {
	posts, err := s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' AND p.lang=? AND p.published_at > ?
		ORDER BY p.published_at ASC LIMIT 1`, p.Lang, fmtTime(p.PublishedAt))
	if err != nil || len(posts) == 0 {
		return nil, err
	}
	return posts[0], nil
}

// Related 同语种、同分类的相关文章（排除自身）。
func (s *Store) Related(p *Post, limit int) ([]*Post, error) {
	if !p.CategoryID.Valid {
		return nil, nil
	}
	return s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' AND p.lang=? AND p.category_id=? AND p.id<>?
		ORDER BY p.published_at DESC LIMIT ?`, p.Lang, p.CategoryID.Int64, p.ID, limit)
}

// Search 在某语种的标题、摘要、正文、SEO 描述与关键词标签中检索（默认仅文章）；
// 长词优先走 FTS5，短词保留 LIKE 回退。
func (s *Store) Search(lang, q string, limit int) ([]*Post, error) {
	return s.SearchInTypes(lang, q, []string{"post"}, limit)
}

// SearchInTypes 在给定的内容类型集合内检索，供「扩展」类型一并进入站内搜索。
func (s *Store) SearchInTypes(lang, q string, types []string, limit int) ([]*Post, error) {
	if len(types) == 0 {
		types = []string{"post"}
	}
	if len([]rune(q)) >= 3 {
		if posts, err := s.searchFTS(lang, q, types, limit); err == nil {
			return posts, nil
		}
	}
	return s.searchLike(lang, q, types, limit)
}

func (s *Store) searchFTS(lang, q string, types []string, limit int) ([]*Post, error) {
	match := `"` + strings.ReplaceAll(strings.TrimSpace(q), `"`, `""`) + `"`
	inClause, typeArgs := typeIn("type", types)
	sql := `SELECT ` + postSummaryCols + `
		FROM posts p
		JOIN (
			SELECT rowid, rank FROM post_search
			WHERE post_search MATCH ? AND lang=? AND ` + inClause + ` AND status='published'
			ORDER BY rank LIMIT ?
		) hit ON hit.rowid = p.id
		LEFT JOIN categories c ON c.id = p.category_id
		ORDER BY hit.rank, p.published_at DESC`
	args := append([]any{match, lang}, typeArgs...)
	args = append(args, limit)
	return s.queryPostRows(sql, true, args...)
}

func (s *Store) searchLike(lang, q string, types []string, limit int) ([]*Post, error) {
	like := "%" + q + "%"
	inClause, typeArgs := typeIn("p.type", types)
	args := append([]any{}, typeArgs...)
	args = append(args, lang, like, like, like, like, like, limit)
	return s.queryPostSummaries(`WHERE `+inClause+` AND p.status='published' AND p.lang=?
		AND (p.title LIKE ? OR p.excerpt LIKE ? OR p.content LIKE ? OR p.meta_desc LIKE ? OR p.keywords LIKE ?)
		ORDER BY p.published_at DESC LIMIT ?`, args...)
}

// typeIn 构建 "<col> IN (?,?,..)" 片段及其参数。
func typeIn(col string, types []string) (string, []any) {
	if len(types) == 0 {
		types = []string{"post"}
	}
	ph := make([]string, len(types))
	args := make([]any, len(types))
	for i, t := range types {
		ph[i] = "?"
		args[i] = t
	}
	return col + " IN (" + strings.Join(ph, ",") + ")", args
}

// AllPublished 某语种全部已发布文章，供 rss 使用。
func (s *Store) AllPublished(lang string) ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' AND p.lang=? ORDER BY p.published_at DESC`, lang)
}

// RecentPublished 返回某语种最近的已发布文章，供 rss 使用。
func (s *Store) RecentPublished(lang string, limit int) ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' AND p.lang=?
		ORDER BY p.published_at DESC LIMIT ?`, lang, limit)
}

// AllPublishedAllLangs 所有语种的已发布文章，供 sitemap 使用。
func (s *Store) AllPublishedAllLangs() ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' ORDER BY p.lang, p.published_at DESC`)
}

// AllPagesAllLangs 所有语种的已发布独立页面（type=page），供 sitemap 使用。
func (s *Store) AllPagesAllLangs() ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='page' AND p.status='published' ORDER BY p.lang`)
}

// ---------- 查询：链接（type=link）----------

// ListLinks 返回某语种已发布链接（可按分类过滤，置顶优先、发布时间倒序，分页）。
func (s *Store) ListLinks(lang string, catID int64, offset, limit int) ([]*Post, error) {
	if catID > 0 {
		return s.queryPostSummaries(`WHERE p.type='link' AND p.status='published' AND p.lang=? AND p.category_id=?
			ORDER BY p.featured DESC, p.published_at DESC, p.id DESC LIMIT ? OFFSET ?`, lang, catID, limit, offset)
	}
	return s.queryPostSummaries(`WHERE p.type='link' AND p.status='published' AND p.lang=?
		ORDER BY p.featured DESC, p.published_at DESC, p.id DESC LIMIT ? OFFSET ?`, lang, limit, offset)
}

// CountLinks 统计某语种已发布链接数（可按分类）。
func (s *Store) CountLinks(lang string, catID int64) (int, error) {
	q := `SELECT COUNT(*) FROM posts WHERE type='link' AND status='published' AND lang=?`
	args := []any{lang}
	if catID > 0 {
		q += ` AND category_id=?`
		args = append(args, catID)
	}
	var n int
	err := s.db.QueryRow(q, args...).Scan(&n)
	return n, err
}

// GetLinkBySlug 取某语种单条链接。includeDrafts 为 true 时也返回草稿。
func (s *Store) GetLinkBySlug(lang, slug string, includeDrafts bool) (*Post, error) {
	where := `WHERE p.slug=? AND p.lang=? AND p.type='link'`
	if !includeDrafts {
		where += ` AND p.status='published'`
	}
	posts, err := s.queryPosts(where+` LIMIT 1`, slug, lang)
	if err != nil || len(posts) == 0 {
		return nil, err
	}
	return posts[0], nil
}

// RelatedLinks 同语种、同分类的相关链接（排除自身）。
func (s *Store) RelatedLinks(p *Post, limit int) ([]*Post, error) {
	if !p.CategoryID.Valid {
		return nil, nil
	}
	return s.queryPostSummaries(`WHERE p.type='link' AND p.status='published' AND p.lang=? AND p.category_id=? AND p.id<>?
		ORDER BY p.published_at DESC LIMIT ?`, p.Lang, p.CategoryID.Int64, p.ID, limit)
}

// AllLinksAllLangs 所有语种的已发布链接，供 sitemap 使用。
func (s *Store) AllLinksAllLangs() ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='link' AND p.status='published' ORDER BY p.lang, p.published_at DESC`)
}

// ListAllLinks 后台：某语种全部链接（含草稿）。
func (s *Store) ListAllLinks(lang string) ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='link' AND p.lang=? ORDER BY p.updated_at DESC`, lang)
}

// ---------- 查询：「扩展」内容类型（按 type 泛化）----------

// ListPublishedByType 返回某语种、某类型已发布内容（可按分类过滤，置顶优先、发布时间倒序，分页）。
func (s *Store) ListPublishedByType(typ, lang string, catID int64, offset, limit int) ([]*Post, error) {
	if catID > 0 {
		return s.queryPostSummaries(`WHERE p.type=? AND p.status='published' AND p.lang=? AND p.category_id=?
			ORDER BY p.featured DESC, p.published_at DESC, p.id DESC LIMIT ? OFFSET ?`, typ, lang, catID, limit, offset)
	}
	return s.queryPostSummaries(`WHERE p.type=? AND p.status='published' AND p.lang=?
		ORDER BY p.featured DESC, p.published_at DESC, p.id DESC LIMIT ? OFFSET ?`, typ, lang, limit, offset)
}

// CountPublishedByType 统计某语种、某类型已发布条目数（可按分类）。
func (s *Store) CountPublishedByType(typ, lang string, catID int64) (int, error) {
	q := `SELECT COUNT(*) FROM posts WHERE type=? AND status='published' AND lang=?`
	args := []any{typ, lang}
	if catID > 0 {
		q += ` AND category_id=?`
		args = append(args, catID)
	}
	var n int
	err := s.db.QueryRow(q, args...).Scan(&n)
	return n, err
}

// GetTypedBySlug 取某语种、某类型单条内容。includeDrafts 为 true 时也返回草稿（供后台预览）。
func (s *Store) GetTypedBySlug(typ, lang, slug string, includeDrafts bool) (*Post, error) {
	where := `WHERE p.slug=? AND p.lang=? AND p.type=?`
	if !includeDrafts {
		where += ` AND p.status='published'`
	}
	posts, err := s.queryPosts(where+` LIMIT 1`, slug, lang, typ)
	if err != nil || len(posts) == 0 {
		return nil, err
	}
	return posts[0], nil
}

// ListAllByType 后台：某语种、某类型全部内容（含草稿）。
func (s *Store) ListAllByType(typ, lang string) ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type=? AND p.lang=? ORDER BY p.updated_at DESC`, typ, lang)
}

// CountByType 某类型全部内容条数（不分语种与状态）。删除类型前的护栏用。
func (s *Store) CountByType(typ string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM posts WHERE type=?`, typ).Scan(&n)
	return n, err
}

// AllPublishedByType 某语种、某类型全部已发布内容（供搜索索引与静态导出枚举）。
func (s *Store) AllPublishedByType(typ, lang string) ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type=? AND p.status='published' AND p.lang=? ORDER BY p.published_at DESC`, typ, lang)
}

// TranslationsPublished 返回与某 trans_group 关联、已发布的各语种内容（含 post 与 page）。
// 供前台构建语言切换与 hreflang 备份链接。
func (s *Store) TranslationsPublished(group string) ([]*Post, error) {
	if group == "" {
		return nil, nil
	}
	return s.queryPostSummaries(`WHERE p.trans_group=? AND p.status='published' ORDER BY p.lang`, group)
}

// ---------- 查询：后台 ----------

func (s *Store) ListAllPosts(lang string) ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='post' AND p.lang=? ORDER BY p.updated_at DESC`, lang)
}

type AdminContentCounts struct {
	Total     int
	Published int
	Draft     int
	Scheduled int
}

type AdminContentIssues struct {
	MissingCover    int
	MissingCategory int
	MissingExcerpt  int
	MissingMetaDesc int
}

func adminContentWhere(kind, lang, status, categorySlug string) (string, []any, error) {
	switch kind {
	case "post", "link", "page":
	default:
		return "", nil, fmt.Errorf("unsupported content type: %s", kind)
	}
	where := `WHERE p.type=? AND p.lang=?`
	args := []any{kind, lang}
	switch strings.TrimSpace(status) {
	case "", "all":
	case "draft", "published", "scheduled":
		where += ` AND p.status=?`
		args = append(args, strings.TrimSpace(status))
	default:
		return "", nil, fmt.Errorf("unsupported status: %s", status)
	}
	if categorySlug = strings.TrimSpace(categorySlug); categorySlug != "" {
		if kind == "page" {
			return "", nil, fmt.Errorf("category filter is not supported for pages")
		}
		where += ` AND p.category_id=(SELECT cx.id FROM categories cx WHERE cx.lang=? AND cx.kind=? AND cx.slug=? LIMIT 1)`
		args = append(args, lang, kind, categorySlug)
	}
	return where, args, nil
}

// ListAdminContent 后台：某语种文章、链接或页面列表（含草稿，可按状态过滤，分页）。
func (s *Store) ListAdminContent(kind, lang, status string, offset, limit int) ([]*Post, error) {
	return s.ListAdminContentFiltered(kind, lang, status, "", offset, limit)
}

// ListAdminContentFiltered 后台：某语种文章、链接或页面列表（含草稿，可按状态和分类过滤，分页）。
func (s *Store) ListAdminContentFiltered(kind, lang, status, categorySlug string, offset, limit int) ([]*Post, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	where, args, err := adminContentWhere(kind, lang, status, categorySlug)
	if err != nil {
		return nil, err
	}
	args = append(args, limit, offset)
	return s.queryPostSummaries(where+` ORDER BY p.updated_at DESC LIMIT ? OFFSET ?`, args...)
}

// CountAdminContent 后台：统计某语种文章、链接或页面数量（含草稿，可按状态过滤）。
func (s *Store) CountAdminContent(kind, lang, status string) (int, error) {
	return s.CountAdminContentFiltered(kind, lang, status, "")
}

// CountAdminContentFiltered 后台：统计某语种文章、链接或页面数量（含草稿，可按状态和分类过滤）。
func (s *Store) CountAdminContentFiltered(kind, lang, status, categorySlug string) (int, error) {
	where, args, err := adminContentWhere(kind, lang, status, categorySlug)
	if err != nil {
		return 0, err
	}
	var n int
	err = s.db.QueryRow(`SELECT COUNT(*) FROM posts p `+where, args...).Scan(&n)
	return n, err
}

// CountContent 统计某语种下的全部内容行数（所有类型、含草稿），用于站点概览徽标。
func (s *Store) CountContent(lang string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM posts WHERE lang=?`, lang).Scan(&n)
	return n, err
}

// LastPublicUpdate 返回「对外可见内容」上次变化的时间：已发布且已到发布时间的内容里 updated_at 的最大值。
// 服务器动态托管——任何已发布内容一保存即对外生效，因此这个时间就是公开站点上次真正变化的时刻。
// 草稿和未到时间的定时内容都不计入（尚未对外）。没有任何已发布内容时返回 ok=false。
func (s *Store) LastPublicUpdate() (time.Time, bool, error) {
	var v sql.NullString
	err := s.db.QueryRow(`SELECT MAX(updated_at) FROM posts
		WHERE status='published' AND (published_at IS NULL OR published_at<=?)`, fmtTime(time.Now())).Scan(&v)
	if err != nil {
		return time.Time{}, false, err
	}
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		return time.Time{}, false, nil
	}
	t, perr := time.Parse(time.RFC3339, v.String)
	if perr != nil {
		return time.Time{}, false, nil
	}
	return t, true, nil
}

func (s *Store) AdminContentStatusCounts(lang string) (map[string]AdminContentCounts, error) {
	counts := map[string]AdminContentCounts{
		"post": {},
		"link": {},
		"page": {},
	}
	rows, err := s.db.Query(`SELECT type,status,COUNT(*) FROM posts
		WHERE lang=? AND type IN ('post','link','page')
		GROUP BY type,status`, lang)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, status string
		var n int
		if err := rows.Scan(&kind, &status, &n); err != nil {
			return nil, err
		}
		c := counts[kind]
		c.Total += n
		switch status {
		case "published":
			c.Published += n
		case "draft":
			c.Draft += n
		case "scheduled":
			c.Scheduled += n
		}
		counts[kind] = c
	}
	return counts, rows.Err()
}

// CountAdminContentIssue 统计后台概览中的内容缺项。
func (s *Store) CountAdminContentIssue(kind, lang, issue string) (int, error) {
	switch kind {
	case "post", "link", "page":
	default:
		return 0, fmt.Errorf("unsupported content type: %s", kind)
	}
	where := `WHERE type=? AND lang=?`
	args := []any{kind, lang}
	switch strings.TrimSpace(issue) {
	case "missing_cover":
		where += ` AND TRIM(COALESCE(cover_image,''))=''`
	case "missing_category":
		where += ` AND category_id IS NULL`
	case "missing_excerpt":
		where += ` AND TRIM(COALESCE(excerpt,''))=''`
	case "missing_meta_desc":
		where += ` AND TRIM(COALESCE(meta_desc,''))=''`
	default:
		return 0, fmt.Errorf("unsupported content issue: %s", issue)
	}
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM posts `+where, args...).Scan(&n)
	return n, err
}

func (s *Store) AdminContentIssueCounts(lang string) (map[string]AdminContentIssues, error) {
	issues := map[string]AdminContentIssues{
		"post": {},
		"link": {},
		"page": {},
	}
	rows, err := s.db.Query(`SELECT type,
			COALESCE(SUM(CASE WHEN TRIM(COALESCE(cover_image,''))='' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN category_id IS NULL THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN TRIM(COALESCE(excerpt,''))='' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN TRIM(COALESCE(meta_desc,''))='' THEN 1 ELSE 0 END),0)
		FROM posts
		WHERE lang=? AND type IN ('post','link','page')
		GROUP BY type`, lang)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var item AdminContentIssues
		if err := rows.Scan(&kind, &item.MissingCover, &item.MissingCategory, &item.MissingExcerpt, &item.MissingMetaDesc); err != nil {
			return nil, err
		}
		issues[kind] = item
	}
	return issues, rows.Err()
}

// ListRecentAdminContent 返回某语种最近更新的后台内容，含文章、链接和页面。
func (s *Store) ListRecentAdminContent(lang string, limit int) ([]*Post, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	return s.queryPostSummaries(`WHERE p.lang=? AND p.type IN ('post','link','page')
		ORDER BY p.updated_at DESC LIMIT ?`, lang, limit)
}

func (s *Store) ListContentForAutomation(kind, lang, status, query, slug, transGroup string, offset, limit int) ([]*Post, error) {
	// kind 由调用方经 apiContentKind（注册表/DB 类型感知）校验后传入；扩展类型（product/
	// 自定义等）与内置同构走同一查询。只拦空值防误用。
	if strings.TrimSpace(kind) == "" {
		return nil, fmt.Errorf("unsupported content type: (empty)")
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	where := `WHERE p.type=?`
	args := []any{kind}
	if lang = strings.TrimSpace(lang); lang != "" && lang != "all" {
		where += ` AND p.lang=?`
		args = append(args, lang)
	}
	if transGroup = strings.TrimSpace(transGroup); transGroup != "" {
		where += ` AND p.trans_group=?`
		args = append(args, transGroup)
	}
	if slug = strings.TrimSpace(slug); slug != "" {
		where += ` AND p.slug=?`
		args = append(args, slug)
	}
	if query = strings.TrimSpace(query); query != "" {
		like := "%" + query + "%"
		where += ` AND (p.title LIKE ? OR p.slug LIKE ? OR p.excerpt LIKE ? OR p.content LIKE ?)`
		args = append(args, like, like, like, like)
	}
	switch status {
	case "", "all":
	case "draft", "published", "scheduled":
		where += ` AND p.status=?`
		args = append(args, status)
	default:
		return nil, fmt.Errorf("unsupported status: %s", status)
	}
	args = append(args, limit, offset)
	return s.queryPostSummaries(where+` ORDER BY p.updated_at DESC LIMIT ? OFFSET ?`, args...)
}

func (s *Store) ListPages(lang string) ([]*Post, error) {
	return s.queryPostSummaries(`WHERE p.type='page' AND p.lang=? ORDER BY p.updated_at DESC`, lang)
}

func (s *Store) GetPostByID(id int64) (*Post, error) {
	posts, err := s.queryPosts(`WHERE p.id=? LIMIT 1`, id)
	if err != nil || len(posts) == 0 {
		return nil, err
	}
	return posts[0], nil
}

// TranslationsAll 返回与某 trans_group 关联的各语种内容（含草稿，排除自身），供后台展示互译版本。
func (s *Store) TranslationsAll(group string, exceptID int64) ([]*Post, error) {
	if group == "" {
		return nil, nil
	}
	return s.queryPostSummaries(`WHERE p.trans_group=? AND p.id<>? ORDER BY p.lang`, group, exceptID)
}

func (s *Store) CreatePost(p *Post) (int64, error) {
	now := time.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	if p.Status == "published" && p.PublishedAt.IsZero() {
		p.PublishedAt = now
	}
	p.Lang = nz(p.Lang, "zh")
	if p.TransGroup == "" {
		p.TransGroup = p.Lang + ":" + p.Slug
	}
	res, err := s.db.Exec(`INSERT INTO posts
		(type,slug,title,excerpt,content,meta_desc,keywords,cover_image,author,status,featured,editor_mode,comments_enabled,link_url,lang,trans_group,extra,category_id,published_at,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		nz(p.Type, "post"), p.Slug, p.Title, p.Excerpt, p.Content, p.MetaDesc, p.Keywords, p.CoverImage,
		p.Author, p.Status, boolInt(p.Featured), nz(p.EditorMode, "markdown"), boolInt(p.CommentsEnabled), p.LinkURL, p.Lang, p.TransGroup,
		p.Extra, p.CategoryID, nullTime(p.PublishedAt), fmtTime(p.CreatedAt), fmtTime(p.UpdatedAt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdatePost(p *Post) error {
	p.UpdatedAt = time.Now()
	if p.Status == "published" && p.PublishedAt.IsZero() {
		p.PublishedAt = p.UpdatedAt
	}
	_, err := s.db.Exec(`UPDATE posts SET
		slug=?,title=?,excerpt=?,content=?,meta_desc=?,keywords=?,cover_image=?,author=?,status=?,featured=?,editor_mode=?,comments_enabled=?,link_url=?,trans_group=?,extra=?,category_id=?,published_at=?,updated_at=?
		WHERE id=?`,
		p.Slug, p.Title, p.Excerpt, p.Content, p.MetaDesc, p.Keywords, p.CoverImage, p.Author, p.Status,
		boolInt(p.Featured), nz(p.EditorMode, "markdown"), boolInt(p.CommentsEnabled), p.LinkURL, p.TransGroup, p.Extra, p.CategoryID, nullTime(p.PublishedAt), fmtTime(p.UpdatedAt), p.ID)
	return err
}

// SetFeatured 单独切换置顶（不动其它字段）。
func (s *Store) SetFeatured(id int64, on bool) error {
	_, err := s.db.Exec(`UPDATE posts SET featured=? WHERE id=?`, boolInt(on), id)
	return err
}

// FeaturedPosts 返回某语种置顶的已发布文章（按发布时间倒序），供首页精选列表使用。
func (s *Store) FeaturedPosts(lang string, limit int) ([]*Post, error) {
	now := fmtTime(time.Now())
	return s.queryPostSummaries(`WHERE p.type='post' AND p.status='published' AND p.lang=? AND p.featured=1 AND p.published_at<=?
		ORDER BY p.published_at DESC LIMIT ?`, lang, now, limit)
}

// FeaturedLinks 取该语种下「置顶」的链接，供首页链接模块展示；无置顶则返回空（首页隐藏该模块）。
func (s *Store) FeaturedLinks(lang string, limit int) ([]*Post, error) {
	now := fmtTime(time.Now())
	return s.queryPostSummaries(`WHERE p.type='link' AND p.status='published' AND p.lang=? AND p.featured=1 AND p.published_at<=?
		ORDER BY p.published_at DESC, p.id DESC LIMIT ?`, lang, now, limit)
}

func (s *Store) DeletePost(id int64) error {
	_, err := s.db.Exec(`DELETE FROM posts WHERE id=?`, id)
	return err
}

// PublishDue 把到点的「定时发布」文章翻为「已发布」，返回处理条数。由后台定时器调用。
func (s *Store) PublishDue() (int64, error) {
	res, err := s.db.Exec(`UPDATE posts SET status='published' WHERE status='scheduled' AND published_at<=?`, fmtTime(time.Now()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SlugExists 校验某语种内 slug 是否被其它文章占用（exceptID 为 0 表示新建）。
func (s *Store) SlugExists(lang, slug string, exceptID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM posts WHERE slug=? AND lang=? AND id<>?`, slug, lang, exceptID).Scan(&n)
	return n > 0, err
}

// ---------- 设置 ----------

func (s *Store) loadSettings() error {
	rows, err := s.db.Query(`SELECT key,value FROM settings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	next := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		next[key] = value
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.settingsMu.Lock()
	s.settings = next
	s.settingsLoaded = true
	s.settingsMu.Unlock()
	return nil
}

func (s *Store) GetSetting(key string) (string, error) {
	s.settingsMu.RLock()
	if s.settingsLoaded {
		v := s.settings[key]
		s.settingsMu.RUnlock()
		return v, nil
	}
	s.settingsMu.RUnlock()

	if err := s.loadSettings(); err != nil {
		return "", err
	}
	s.settingsMu.RLock()
	v := s.settings[key]
	s.settingsMu.RUnlock()
	return v, nil
}

// Setting 便捷读取（忽略错误，缺失返回空串）。
func (s *Store) Setting(key string) string {
	v, _ := s.GetSetting(key)
	return v
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	if err != nil {
		return err
	}
	s.settingsMu.Lock()
	if s.settingsLoaded {
		if s.settings == nil {
			s.settings = map[string]string{}
		}
		s.settings[key] = value
	}
	s.settingsMu.Unlock()
	return err
}

// AllPostReferenceTexts 返回全部 posts 行的文本字段拼接（所有语种、所有类型、含草稿），
// 供上传文件引用扫描使用：正文、摘要、封面、关键词、extra JSON 等都在其中。
func (s *Store) AllPostReferenceTexts() ([]string, error) {
	rows, err := s.db.Query(`SELECT title, excerpt, content, meta_desc, keywords, cover_image, link_url, extra FROM posts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var title, excerpt, content, metaDesc, keywords, coverImage, linkURL, extra string
		if err := rows.Scan(&title, &excerpt, &content, &metaDesc, &keywords, &coverImage, &linkURL, &extra); err != nil {
			return nil, err
		}
		out = append(out, strings.Join([]string{title, excerpt, content, metaDesc, keywords, coverImage, linkURL, extra}, "\n"))
	}
	return out, rows.Err()
}

// AllSettingValues 返回 settings 表全部 value（Logo、favicon、分享图、hero、导航等都存在其中），
// 供上传文件引用扫描使用。
func (s *Store) AllSettingValues() ([]string, error) {
	rows, err := s.db.Query(`SELECT value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

// ClearDemoContent 清除首装产品官网演示数据。
// 保留账号、密码、主题、语言、Logo/ico、上传文件与其他基础站点配置。
func (s *Store) ClearDemoContent() error {
	settings := demoSettingKeys()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM posts WHERE trans_group LIKE 's-%' OR trans_group LIKE 'g-%'`); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM categories WHERE trans_group LIKE 's-%'`); err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, key := range settings {
		if _, err := tx.Exec(`DELETE FROM settings WHERE key=?`, key); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO settings(key,value) VALUES('demo.seed','empty')
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.settingsMu.Lock()
	if s.settingsLoaded {
		if s.settings == nil {
			s.settings = map[string]string{}
		}
		for _, key := range settings {
			delete(s.settings, key)
		}
		s.settings["demo.seed"] = "empty"
	}
	s.settingsMu.Unlock()
	return nil
}

// ReloadShowcaseContent 用最新的产品官网样板替换当前内容区。
// 这是给新站初始化/测试用的显式操作：保留登录凭据、Logo/ico、当前主题与上传文件。
func (s *Store) ReloadShowcaseContent() error {
	preserve := map[string]string{}
	for _, key := range []string{"admin_user", "admin_password_hash", "site.logo", "site.favicon", "site.share_image", "site.share_image::en", "theme"} {
		preserve[key] = s.Setting(key)
	}
	if _, err := s.db.Exec(`
DELETE FROM posts;
DELETE FROM categories;
DELETE FROM sqlite_sequence WHERE name IN ('posts','categories');
`); err != nil {
		return err
	}
	if err := s.seedShowcase(); err != nil {
		return err
	}
	for key, value := range preserve {
		if value == "" {
			continue
		}
		if err := s.SetSetting(key, value); err != nil {
			return err
		}
	}
	s.Seeded = false
	return nil
}

func demoSettingKeys() []string {
	keys := []string{"nav_menu", "social_links", "demo.seed", "site.share_image", "site.share_image::en", "hero.visual", "hero.visual::en", "hero.image", "hero.image::en", "hero.svg"}
	for _, base := range []string{"home.featured_title", "home.links_title", "home.latest_title"} {
		keys = append(keys, base, base+"::en")
	}
	for _, prefix := range []string{"category.all.", "links.all."} {
		for _, field := range []string{"title", "label", "slug", "description"} {
			base := prefix + field
			keys = append(keys, base, base+"::en")
		}
	}
	return keys
}

// ---------- 小工具 ----------

func nz(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return fmtTime(t)
}
