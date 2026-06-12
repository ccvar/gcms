// Package seo 负责为每个页面动态构建 SEO 元数据与 JSON-LD 结构化数据。
package seo

import (
	"strings"
	"time"

	"cms.ccvar.com/internal/store"
)

// Site 描述全站级别的 SEO 信息（含当前语种）。
type Site struct {
	Name         string
	Tagline      string
	Description  string
	BaseURL      string   // 如 https://cms.ccvar.com（用于绝对 URL）
	Locale       string   // Open Graph locale，如 zh_CN
	LangTag      string   // BCP47，如 zh-CN（<html lang> / hreflang / inLanguage）
	Prefix       string   // 语种路径前缀，如 /zh（内容 URL 用）
	Author       string   // 组织名，如 CCVAR
	Theme        string   // 前台主题
	Favicon      string   // 站点图标 URL（为空用默认）
	Logo         string   // 站点 logo 图片 URL（为空用文字刊名）
	Brand        string   // 页眉品牌显示：logo | both | text
	HeroEyebrow  string   // 首页 hero 眉标
	HeroTitle    string   // 首页 hero 大标题（换行渲染为 <br>）
	HeroVisual   string   // 首页右侧视觉类型：""(默认动画) | image | svg
	HeroImage    string   // 视觉为 image 时的图片/SVG 文件 URL
	HeroSVG      string   // 视觉为 svg 时的内联 SVG 代码
	FooterNote   string   // 页脚 logo 下方说明
	HomeFeatured string   // 首页「精选」栏目标题（可自定义，空则用语种默认）
	HomeLinks    string   // 首页「精选链接」栏目标题
	HomeLatest   string   // 首页「最新」栏目标题
	HomeLabel    string   // 面包屑「首页」文案（随语种）
	LinksLabel   string   // 「链接」栏目名（随语种）
	InjectHead   string   // 自定义注入：<head> 末尾（统计/校验等）
	InjectBody   string   // 自定义注入：</body> 前（统计/广告等）
	OGAltLocale  []string // 其它启用语种的 OG locale（og:locale:alternate）
}

func (s Site) base() string { return strings.TrimRight(s.BaseURL, "/") }

// Root 把站内路径拼成「不带语种前缀」的绝对 URL（用于静态资源、sitemap、robots）。
func (s Site) Root(path string) string {
	if path == "" {
		path = "/"
	}
	return s.base() + path
}

// Abs 把站内路径拼成「带当前语种前缀」的绝对 URL（用于内容 canonical / JSON-LD）。
func (s Site) Abs(path string) string {
	if path == "" || path == "/" {
		return s.base() + s.Prefix + "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return s.base() + s.Prefix + path
}

func (s Site) langTag() string {
	if s.LangTag != "" {
		return s.LangTag
	}
	return "zh-CN"
}

func (s Site) homeLabel() string {
	if s.HomeLabel != "" {
		return s.HomeLabel
	}
	return "首页"
}

// Alternate 是一条 hreflang 备份链接。
type Alternate struct {
	Hreflang string // 如 en / zh-CN / x-default
	Href     string
}

// Meta 是单个页面的全部 SEO 数据，交给模板的 head 局部渲染。
type Meta struct {
	Title       string
	Description string
	Keywords    string
	Canonical   string
	Robots      string
	OGType      string
	Image       string
	Published   string // RFC3339，仅文章
	Modified    string
	Section     string
	Author      string
	Alternates  []Alternate // hreflang 备份链接（多语种）
	// JSONLD 保存若干「原始」结构化数据对象（map）。模板在
	// <script type="application/ld+json"> 上下文内输出时，由 html/template
	// 自动序列化为合法 JSON 并做安全转义——切勿在此预先 Marshal 成字符串，
	// 否则会被脚本上下文当作 JS 字符串再次转义。
	JSONLD []any
}

const defaultRobots = "index, follow, max-image-preview:large"

type crumb struct{ Name, URL string }

func (s Site) breadcrumb(items []crumb) map[string]any {
	list := make([]any, 0, len(items))
	for i, c := range items {
		item := map[string]any{"@type": "ListItem", "position": i + 1, "name": c.Name}
		if c.URL != "" {
			item["item"] = c.URL
		}
		list = append(list, item)
	}
	return map[string]any{
		"@context":        "https://schema.org",
		"@type":           "BreadcrumbList",
		"itemListElement": list,
	}
}

func (s Site) defaultImage() string { return s.Root("/assets/og-cover.png") }

// Home 首页：WebSite + Organization。
func (s Site) Home() Meta {
	web := map[string]any{
		"@type":       "WebSite",
		"@id":         s.Abs("/") + "#website",
		"url":         s.Abs("/"),
		"name":        s.Name,
		"description": s.Description,
		"inLanguage":  s.langTag(),
		"potentialAction": map[string]any{
			"@type":       "SearchAction",
			"target":      map[string]any{"@type": "EntryPoint", "urlTemplate": s.Abs("/search?q={search_term_string}")},
			"query-input": "required name=search_term_string",
		},
	}
	org := map[string]any{
		"@type": "Organization", "@id": s.Root("/") + "#org",
		"name": s.Author, "url": s.Root("/"), "logo": s.Root("/assets/logo.svg"),
	}
	graph := map[string]any{"@context": "https://schema.org", "@graph": []any{web, org}}
	return Meta{
		Title:       s.Name + " — " + s.Tagline,
		Description: s.Description,
		Keywords:    "Go,SQLite,CMS,内容管理系统,服务端渲染,SEO,极简设计,后端工程",
		Canonical:   s.Abs("/"),
		Robots:      defaultRobots,
		OGType:      "website",
		Image:       s.defaultImage(),
		Author:      s.Author,
		JSONLD:      []any{graph},
	}
}

// Article 文章详情：BlogPosting + BreadcrumbList。
func (s Site) Article(p *store.Post) Meta {
	canon := s.Abs("/posts/" + p.Slug)
	desc := p.MetaDesc
	if desc == "" {
		desc = p.Excerpt
	}
	img := p.CoverImage
	switch {
	case img == "":
		img = s.defaultImage()
	case !strings.HasPrefix(img, "http"):
		img = s.Root(img)
	}
	section, catURL := "", s.Abs("/")
	if p.Category != nil {
		section = p.Category.Name
		catURL = s.Abs("/category/" + p.Category.Slug)
	}
	post := map[string]any{
		"@context":         "https://schema.org",
		"@type":            "BlogPosting",
		"mainEntityOfPage": map[string]any{"@type": "WebPage", "@id": canon},
		"headline":         p.Title,
		"description":      desc,
		"image":            img,
		"datePublished":    p.PublishedAt.Format(time.RFC3339),
		"dateModified":     p.UpdatedAt.Format(time.RFC3339),
		"author":           map[string]any{"@type": "Person", "name": p.Author},
		"publisher": map[string]any{
			"@type": "Organization", "name": s.Author,
			"logo": map[string]any{"@type": "ImageObject", "url": s.Root("/assets/logo.svg")},
		},
		"inLanguage": s.langTag(),
	}
	if p.Keywords != "" {
		post["keywords"] = p.Keywords
	}
	crumbs := s.breadcrumb([]crumb{
		{s.homeLabel(), s.Abs("/")},
		{section, catURL},
		{p.Title, ""},
	})
	return Meta{
		Title:       p.Title + " — " + s.Name,
		Description: desc,
		Keywords:    p.Keywords,
		Canonical:   canon,
		Robots:      defaultRobots,
		OGType:      "article",
		Image:       img,
		Published:   p.PublishedAt.Format(time.RFC3339),
		Modified:    p.UpdatedAt.Format(time.RFC3339),
		Section:     section,
		Author:      p.Author,
		JSONLD:      []any{post, crumbs},
	}
}

// Category 分类页：CollectionPage + BreadcrumbList。
func (s Site) Category(c *store.Category) Meta {
	canon := s.Abs("/category/" + c.Slug)
	coll := map[string]any{
		"@context":   "https://schema.org",
		"@type":      "CollectionPage",
		"name":       c.Name,
		"url":        canon,
		"inLanguage": s.langTag(),
		"isPartOf":   map[string]any{"@type": "WebSite", "name": s.Name, "url": s.Abs("/")},
	}
	crumbs := s.breadcrumb([]crumb{{s.homeLabel(), s.Abs("/")}, {c.Name, ""}})
	desc := c.Description
	if desc == "" {
		desc = c.Name
	}
	return Meta{
		Title:       c.Name + " — " + s.Name,
		Description: desc,
		Keywords:    c.Name,
		Canonical:   canon,
		Robots:      defaultRobots,
		OGType:      "website",
		Image:       s.defaultImage(),
		JSONLD:      []any{coll, crumbs},
	}
}

// Page 静态页（如关于）：AboutPage/WebPage + BreadcrumbList。
func (s Site) Page(p *store.Post) Meta {
	canon := s.Abs("/" + p.Slug)
	desc := p.MetaDesc
	if desc == "" {
		desc = p.Excerpt
	}
	typ := "WebPage"
	if p.Slug == "about" {
		typ = "AboutPage"
	}
	page := map[string]any{
		"@context": "https://schema.org", "@type": typ,
		"name": p.Title + " — " + s.Name, "url": canon, "inLanguage": s.langTag(),
	}
	crumbs := s.breadcrumb([]crumb{{s.homeLabel(), s.Abs("/")}, {p.Title, ""}})
	return Meta{
		Title:       p.Title + " — " + s.Name,
		Description: desc,
		Keywords:    p.Keywords,
		Canonical:   canon,
		Robots:      defaultRobots,
		OGType:      "website",
		Image:       s.defaultImage(),
		JSONLD:      []any{page, crumbs},
	}
}

func (s Site) linksLabel() string {
	if s.LinksLabel != "" {
		return s.LinksLabel
	}
	return "链接"
}

// Links 链接列表页：CollectionPage + BreadcrumbList。cat 非空表示按分类筛选。
func (s Site) Links(cat *store.Category) Meta {
	label := s.linksLabel()
	canon := s.Abs("/links")
	title := label + " — " + s.Name
	name := label
	desc := s.Description
	if cat != nil {
		canon = s.Abs("/links?cat=" + cat.Slug)
		title = cat.Name + " — " + label + " — " + s.Name
		name = cat.Name + " · " + label
		if cat.Description != "" {
			desc = cat.Description
		}
	}
	coll := map[string]any{
		"@context": "https://schema.org", "@type": "CollectionPage",
		"name": name, "url": canon, "inLanguage": s.langTag(),
		"isPartOf": map[string]any{"@type": "WebSite", "name": s.Name, "url": s.Abs("/")},
	}
	crumbs := s.breadcrumb([]crumb{{s.homeLabel(), s.Abs("/")}, {label, s.Abs("/links")}})
	return Meta{
		Title: title, Description: desc, Keywords: label, Canonical: canon,
		Robots: defaultRobots, OGType: "website", Image: s.defaultImage(), JSONLD: []any{coll, crumbs},
	}
}

// Link 链接详情页：WebPage(指向外部资源) + BreadcrumbList。
func (s Site) Link(p *store.Post) Meta {
	label := s.linksLabel()
	canon := s.Abs("/links/" + p.Slug)
	desc := p.MetaDesc
	if desc == "" {
		desc = p.Excerpt
	}
	img := p.CoverImage
	switch {
	case img == "":
		img = s.defaultImage()
	case !strings.HasPrefix(img, "http"):
		img = s.Root(img)
	}
	page := map[string]any{
		"@context": "https://schema.org", "@type": "WebPage",
		"name": p.Title, "description": desc, "url": canon,
		"inLanguage": s.langTag(), "primaryImageOfPage": img,
	}
	if p.LinkURL != "" {
		page["significantLink"] = p.LinkURL
	}
	if p.Keywords != "" {
		page["keywords"] = p.Keywords
	}
	crumbs := s.breadcrumb([]crumb{{s.homeLabel(), s.Abs("/")}, {label, s.Abs("/links")}, {p.Title, ""}})
	return Meta{
		Title: p.Title + " — " + s.Name, Description: desc, Keywords: p.Keywords, Canonical: canon,
		Robots: defaultRobots, OGType: "website", Image: img, JSONLD: []any{page, crumbs},
	}
}

// Search 搜索结果页：不应被索引。
func (s Site) Search(q string) Meta {
	return Meta{
		Title:       "搜索 — " + s.Name,
		Description: "在 " + s.Name + " 中搜索文章。",
		Canonical:   s.Abs("/search"),
		Robots:      "noindex, follow",
		OGType:      "website",
	}
}

// NotFound 404 页：不应被索引。
func (s Site) NotFound() Meta {
	return Meta{
		Title:       "页面未找到 — " + s.Name,
		Description: "抱歉，你访问的页面不存在或已被移动。",
		Canonical:   s.Abs("/404"),
		Robots:      "noindex, follow",
		OGType:      "website",
	}
}
