package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"cms.ccvar.com/internal/seo"
	"cms.ccvar.com/internal/store"
)

// registerContentTypeRoutes 为每种「扩展」内容类型注册公开路由。
// 全局注册（不区分站点）：命中后由处理器按本站启用情况决定行为，这样后台切换
// 启用状态无需重建路由表。
func (s *Server) registerContentTypeRoutes(mux *http.ServeMux) {
	for _, ct := range extContentTypes() {
		ct := ct
		pref := strings.Trim(ct.URLPrefix, "/")
		if pref == "" {
			continue
		}
		mux.HandleFunc("GET /"+pref, func(w http.ResponseWriter, r *http.Request) { s.extList(w, r, ct) })
		mux.HandleFunc("GET /"+pref+"/{$}", func(w http.ResponseWriter, r *http.Request) { s.extList(w, r, ct) })
		mux.HandleFunc("GET /"+pref+"/page/{pageNum}", func(w http.ResponseWriter, r *http.Request) { s.extList(w, r, ct) })
		mux.HandleFunc("GET /"+pref+"/page/{pageNum}/{$}", func(w http.ResponseWriter, r *http.Request) { s.extList(w, r, ct) })
		if ct.HasCategory {
			// 分类筛选（静态友好，与 /links/cat/{slug} 同构）：/{prefix}/cat/{slug}[/page/N]
			mux.HandleFunc("GET /"+pref+"/cat/{cat}", func(w http.ResponseWriter, r *http.Request) { s.extList(w, r, ct) })
			mux.HandleFunc("GET /"+pref+"/cat/{cat}/{$}", func(w http.ResponseWriter, r *http.Request) { s.extList(w, r, ct) })
			mux.HandleFunc("GET /"+pref+"/cat/{cat}/page/{pageNum}", func(w http.ResponseWriter, r *http.Request) { s.extList(w, r, ct) })
			mux.HandleFunc("GET /"+pref+"/cat/{cat}/page/{pageNum}/{$}", func(w http.ResponseWriter, r *http.Request) { s.extList(w, r, ct) })
		}
		mux.HandleFunc("GET /"+pref+"/{slug}", func(w http.ResponseWriter, r *http.Request) { s.extDetail(w, r, ct) })
		mux.HandleFunc("GET /"+pref+"/{slug}/{$}", func(w http.ResponseWriter, r *http.Request) { s.extDetail(w, r, ct) })
	}
}

// extList 渲染扩展类型的归档列表（/{prefix}，可带 /cat/{slug} 分类筛选）。
func (s *Server) extList(w http.ResponseWriter, r *http.Request, ct *ContentType) {
	// 未对本站启用：回退为「按 slug 找页面」，与 /{slug} 完全一致——
	// 保证未用该类型的站点上，名为该前缀的页面仍可访问（零回归）。
	if !s.contentTypeActive(ct.Key) {
		s.renderPageBySlug(w, r, ct.URLPrefix)
		return
	}
	lang := langFrom(r)
	const size = 12
	page := pageParam(r)
	// 分类筛选（引擎分类：kind=类型 key 的分类树）。
	var cat *store.Category
	var catID int64
	if cs := strings.TrimSpace(r.PathValue("cat")); cs != "" && ct.HasCategory {
		c, _ := s.store.GetCategoryBySlug(lang, cs)
		if c == nil || c.Kind != ct.Key {
			s.notFound(w, r)
			return
		}
		cat = c
		catID = c.ID
	}
	total, _ := s.store.CountPublishedByType(ct.Key, lang, catID)
	// 越界页码返回 404：分页 canonical 自指后，无限量的空「深页」会变成各自
	// canonical 的软 404，这里直接掐掉（第 1 页即便为空仍渲染空态）。
	totalPages := ceilDiv(total, size)
	if page > 1 && page > totalPages {
		s.notFound(w, r)
		return
	}
	posts, _ := s.store.ListPublishedByType(ct.Key, lang, catID, (page-1)*size, size)
	v := s.view(r, ct.Key)
	v.CT = ct
	v.Posts = posts
	v.Category = cat
	if ct.HasCategory {
		v.Categories, _ = s.store.ListCategories(lang, ct.Key)
	}
	v.ArchiveTitle, v.ArchiveIntro = s.extArchiveText(ct.Key, lang)
	base := "/" + ct.URLPrefix
	label := ct.Name(lang)
	if v.ArchiveTitle != "" {
		label = v.ArchiveTitle
	}
	seoTitle := label
	seoDesc := v.Site.Description
	if v.ArchiveIntro != "" {
		seoDesc = v.ArchiveIntro
	}
	crumbs := []seo.Crumb{{Name: label}}
	if cat != nil {
		base += "/cat/" + cat.Slug
		seoTitle = cat.Name + " — " + seoTitle
		if cat.Description != "" {
			seoDesc = cat.Description
		}
		crumbs = []seo.Crumb{{Name: label, URL: v.Site.Abs("/" + ct.URLPrefix)}, {Name: cat.Name}}
	}
	// 分页页 canonical 自指（/…/page/N/），不再全部归并到第 1 页。
	canonPath := base
	if page > 1 {
		canonPath = paginationPath(base, page)
	}
	canon := v.Site.Abs(canonPath)
	v.SEO = seo.Meta{
		Title:       seoTitle + " — " + v.Site.Name,
		Description: seoDesc,
		Canonical:   canon,
		Robots:      seo.DefaultRobots,
		OGType:      "website",
		JSONLD:      []any{v.Site.CollectionPage(seoTitle, canon), v.Site.BreadcrumbList(crumbs...)},
	}
	setPagination(v, page, totalPages, base)
	if cat != nil {
		// 分类页的语言切换指向各语种互译分类；无互译版本的语种回落类型总页。
		ph := map[string]string{cat.Lang: base}
		if trs, _ := s.store.CategoryTranslations(cat.TransGroup); trs != nil {
			for _, t := range trs {
				ph[t.Lang] = "/" + ct.URLPrefix + "/cat/" + t.Slug
			}
		}
		v.Langs, v.SEO.Alternates = s.i18nLinksForRequest(r, v.Site.BaseURL, lang, ph)
	} else {
		// 无分类分支同样输出 hreflang：归档路径各语种同构（/{lang}/{prefix}），
		// 此前只填语言切换器不填 Alternates，是全站归档页里唯一的缺口。
		ph := map[string]string{}
		for _, l := range s.locales() {
			ph[l.Code] = base
		}
		v.Langs, v.SEO.Alternates = s.i18nLinksForRequest(r, v.Site.BaseURL, lang, ph)
	}
	if ct.Hierarchical {
		v.DocTree = s.buildDocNav(ct, lang, 0)
	}
	s.rnd.Public(w, listTemplate(ct), http.StatusOK, v)
}

// extDetail 渲染扩展类型的单条详情（/{prefix}/{slug}）。
func (s *Server) extDetail(w http.ResponseWriter, r *http.Request, ct *ContentType) {
	if !s.contentTypeActive(ct.Key) {
		s.notFound(w, r)
		return
	}
	lang := langFrom(r)
	p, err := s.store.GetTypedBySlug(ct.Key, lang, r.PathValue("slug"), false)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if p == nil {
		s.notFound(w, r)
		return
	}
	s.renderExtDetail(w, r, ct, p, false)
}

// renderExtDetail 构建并渲染某条扩展内容的详情视图。
// preview=true 时用于后台草稿预览（noindex、无 hreflang、不对章节做跳转）。
func (s *Server) renderExtDetail(w http.ResponseWriter, r *http.Request, ct *ContentType, p *store.Post, preview bool) {
	lang := p.Lang
	if lang == "" {
		lang = langFrom(r)
	}
	s.fillDefaultAuthor(p)
	v := s.viewForLang(r, lang, ct.Key)
	v.CT = ct
	v.Post = p
	v.ContentHTML, v.TOC = s.renderedContent(p)
	v.Fields = renderFieldValues(ct, p, lang)
	if ct.Hierarchical {
		// 文档（GitBook 式）：左侧持久导航树——顶层文档为分类、子文档为其下章节，
		// 各自独立成页，当前页高亮；详情页底部列出本节下级章节。
		v.DocTree = s.buildDocNav(ct, lang, p.ID)
		v.DocChildren = activeNodeChildren(v.DocTree)
		// 标题已由页面 H1 呈现；若正文开头重复了同一标题则去掉，避免出现两次。
		v.ContentHTML = stripRedundantHeading(v.ContentHTML, p.Title)
	}
	canon := publicContentPath(ct.Key, p.Slug)
	pageURL := v.Site.Abs(canon) // 页面真实 URL：询盘预填用它，canonical 可被单篇覆盖
	author := strings.TrimSpace(p.Author)
	if author == "" {
		author = v.Site.Author
	}
	// 分享图与页面主图一致：封面缺席时回落图集第一张，再兜底站点默认分享图；
	// 相对路径绝对化（分享抓取器不解析相对 og:image）。
	gallery := extGalleryImages(ct, p)
	mainImg := p.CoverImage
	if mainImg == "" && len(gallery) > 0 {
		mainImg = gallery[0]
	}
	ogType := "article"
	if ct.Key == "product" {
		ogType = "product"
	}
	v.SEO = seo.Meta{
		Title:       p.Title + " — " + v.Site.Name,
		Description: metaDescOf(p),
		Canonical:   seo.OverrideOr(p.CanonicalOverride, pageURL),
		Robots:      seo.OverrideOr(p.RobotsOverride, seo.DefaultRobots),
		OGType:      ogType,
		Image:       v.Site.ContentImage(mainImg),
		Author:      author,
	}
	// 询盘区块（工厂/外贸站 P1）：product 详情页 + 已配置联系方式才渲染。
	// P1 硬编码 product；若未来更多类型需要询盘，可在 ContentType 上加开关替换这里的判断。
	if ct.Key == "product" && v.Contact.Any() {
		prefill := v.Tr.Tf("contact.inquiry_text", p.Title, pageURL)
		v.Inquiry = inquiryView(v.Contact, prefill, p.Title)
	}
	tplName := detailTemplate(ct)
	if ct.Key == "product" {
		// Product 结构化数据（工厂/外贸站 P2）：brand=站点名、sku 从规格嗅探、
		// image=封面+图集多图；不输出 offers/price——口径：price 字段是自由文本且可不填
		// （「US$ 12.5/pc」「面议」…），无法承诺 schema.org offers 要求的结构化币种/数值，
		// 且本 CMS 不做交易，绝不编造报价。价格仅作为普通规格行在页面可见文本中呈现。
		v.SEO.JSONLD = append(v.SEO.JSONLD, v.Site.Product(p, v.SEO.Canonical, v.SEO.Description, productSKU(p), gallery))
		if isFactoryLayout(v.Layout) || isDTCLayout(v.Layout) {
			// 工厂/独立站骨架：商品详情走专属模板（图集 + 规格表 + 首屏询盘 + 相关商品）。
			tplName = "product_detail"
			v.Related = s.relatedProducts(ct, p, lang)
		}
	}
	// BreadcrumbList：与页面可见面包屑同构（首页 → 类型归档 → 分类 → 本条），
	// 内置文章/链接页早已输出，扩展详情此前缺席。
	crumbs := []seo.Crumb{{Name: ct.Name(lang), URL: v.Site.Abs("/" + ct.URLPrefix)}}
	if p.Category != nil {
		crumbs = append(crumbs, seo.Crumb{Name: p.Category.Name, URL: v.Site.Abs("/" + ct.URLPrefix + "/cat/" + p.Category.Slug)})
	}
	crumbs = append(crumbs, seo.Crumb{Name: p.Title})
	v.SEO.JSONLD = append(v.SEO.JSONLD, v.Site.BreadcrumbList(crumbs...))
	if preview {
		v.SEO.Robots = "noindex, nofollow"
		v.SEO.Alternates = nil
	} else {
		ph := map[string]string{p.Lang: canon}
		if trs, _ := s.store.TranslationsPublished(p.TransGroup); trs != nil {
			for _, t := range trs {
				if t.Type == ct.Key {
					ph[t.Lang] = publicContentPath(ct.Key, t.Slug)
				}
			}
		}
		v.Langs, v.SEO.Alternates = s.i18nLinksForRequest(r, v.Site.BaseURL, lang, ph)
	}
	s.rnd.Public(w, tplName, http.StatusOK, v)
}

func listTemplate(ct *ContentType) string {
	if ct.ListTemplate != "" {
		return ct.ListTemplate
	}
	if ct.Hierarchical {
		return "doc_list" // 层级类型默认走 GitBook 式文档布局
	}
	return "generic_list"
}

func detailTemplate(ct *ContentType) string {
	if ct.DetailTemplate != "" {
		return ct.DetailTemplate
	}
	if ct.Hierarchical {
		return "doc_detail" // 层级类型默认走 GitBook 式文档布局
	}
	return "generic_detail"
}

// contentTypeRouter 在 mux 之前拦截「数据库自定义类型」的公开路径并分发到通用处理器。
// 代码内置扩展类型仍由 mux 的具体路由处理；其余路径透传给 mux。
// 用包装器（而非通配路由）以避免与 /assets/、/uploads/ 等子树模式冲突。
// 此处位于 withLocale 之后，r.URL.Path 已剥离语种前缀。
func (s *Server) contentTypeRouter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := strings.Trim(r.URL.Path, "/"); p != "" && !strings.Contains(p, ".") {
			segs := strings.Split(p, "/")
			// 仅处理「数据库自定义类型」（代码内置类型走 mux 的具体路由）。
			if ct := s.activeExtTypeByPrefix(segs[0]); ct != nil && contentTypeByKey(ct.Key) == nil {
				switch {
				case len(segs) == 1:
					s.extList(w, r, ct)
					return
				case len(segs) == 3 && segs[1] == "cat":
					r.SetPathValue("cat", segs[2])
					s.extList(w, r, ct)
					return
				case len(segs) == 5 && segs[1] == "cat" && segs[3] == "page":
					r.SetPathValue("cat", segs[2])
					r.SetPathValue("pageNum", segs[4])
					s.extList(w, r, ct)
					return
				case len(segs) == 2:
					r.SetPathValue("slug", segs[1])
					s.extDetail(w, r, ct)
					return
				case len(segs) == 3 && segs[1] == "page":
					r.SetPathValue("pageNum", segs[2])
					s.extList(w, r, ct)
					return
				}
			}
			if r.Method == http.MethodGet && len(segs) == 1 && strings.HasSuffix(r.URL.Path, "/") && !isReservedPublicSlug(segs[0]) {
				r.SetPathValue("slug", segs[0])
				s.page(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isReservedPublicSlug(slug string) bool {
	switch slug {
	case "admin", "api", "api-docs", "assets", "category", "favicon.ico", "links", "posts", "preview", "robots.txt", "rss.xml", "search", "sitemap.xml", "uploads":
		return true
	default:
		return false
	}
}

func metaDescOf(p *store.Post) string {
	if s := strings.TrimSpace(p.MetaDesc); s != "" {
		return s
	}
	return strings.TrimSpace(p.Excerpt)
}

// renderFieldValues 把 posts.extra(JSON) 按类型字段 schema 解析为展示值列表。
func renderFieldValues(ct *ContentType, p *store.Post, lang string) []FieldValue {
	if len(ct.Fields) == 0 {
		return nil
	}
	raw := strings.TrimSpace(p.Extra)
	if raw == "" || raw == "{}" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	out := make([]FieldValue, 0, len(ct.Fields))
	for _, f := range ct.Fields {
		if f.Structural {
			continue // 结构性字段（层级父级、排序）不作为内容字段展示
		}
		val, ok := m[f.Key]
		if !ok || val == nil {
			continue
		}
		fv := FieldValue{Key: f.Key, Label: f.Label(lang), Type: string(f.Type)}
		switch f.Type {
		case FieldGallery:
			fv.List = toStringList(val)
		case FieldImage:
			if sv := scalarString(val); sv != "" {
				fv.List = []string{sv}
			}
		case FieldRepeater:
			fv.Pairs = pairsToList(val)
		case FieldDatetime:
			fv.Text = formatDatetimeField(scalarString(val))
		default:
			fv.Text = scalarString(val)
		}
		if fv.Text == "" && len(fv.List) == 0 && len(fv.Pairs) == 0 {
			continue
		}
		out = append(out, fv)
	}
	return out
}

// extGalleryImages 收集一条扩展内容全部 gallery 字段的图片 URL（按字段定义顺序，去空）。
// 供详情页分享图回退、Product JSON-LD 多图与 sitemap 的 image:image 使用。
func extGalleryImages(ct *ContentType, p *store.Post) []string {
	if ct == nil || len(ct.Fields) == 0 {
		return nil
	}
	raw := strings.TrimSpace(p.Extra)
	if raw == "" || raw == "{}" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	var out []string
	for _, f := range ct.Fields {
		if f.Type != FieldGallery {
			continue
		}
		out = append(out, toStringList(m[f.Key])...)
	}
	return out
}

func toStringList(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if sv := scalarString(e); sv != "" {
				out = append(out, sv)
			}
		}
		return out
	case []string:
		return t
	case string:
		if t = strings.TrimSpace(t); t != "" {
			return []string{t}
		}
	}
	return nil
}

// pairsToList 把 repeater 的 JSON 值（[{k,v}...]）转为展示用键值对列表。
func pairsToList(v any) []FieldPair {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]FieldPair, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		k, _ := m["k"].(string)
		if strings.TrimSpace(k) == "" {
			continue
		}
		val, _ := m["v"].(string)
		out = append(out, FieldPair{K: k, V: val})
	}
	return out
}

// scalarString 把 JSON 标量转为展示字符串（整数不带小数尾）。
func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case bool:
		if t {
			return "✓"
		}
		return ""
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	}
	return ""
}

// formatDatetimeField 把 datetime-local 形态的时间串格式化为可读形式。
func formatDatetimeField(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05", "2006-01-02 15:04"} {
		if t, err := time.Parse(layout, s); err == nil {
			if t.Hour() == 0 && t.Minute() == 0 {
				return t.Format("2006 年 1 月 2 日")
			}
			return t.Format("2006 年 1 月 2 日 15:04")
		}
	}
	return s
}

// publicContentPath 返回某条内容的公开路径（按类型路由）。供搜索结果等处生成正确链接。
func publicContentPath(typ, slug string) string {
	switch typ {
	case "post":
		return "/posts/" + slug + "/"
	case "link":
		return "/links/" + slug + "/"
	case "page":
		return "/" + slug + "/"
	default:
		if ct := contentTypeByKey(typ); ct != nil && ct.URLPrefix != "" {
			return "/" + ct.URLPrefix + "/" + slug + "/"
		}
		// 数据库自定义类型：URL 前缀恒等于 key，直接拼接。
		return "/" + typ + "/" + slug + "/"
	}
}

// searchableTypes 返回本站参与站内搜索的类型：文章 + 已启用且可搜索的扩展类型。
func (s *Server) searchableTypes() []string {
	types := []string{"post"}
	for _, ct := range s.activeExtContentTypes() {
		if ct.Searchable {
			types = append(types, ct.Key)
		}
	}
	return types
}

// DocNode 是层级类型（如文档）侧边树的一个节点。
type DocNode struct {
	Title    string
	URL      string // 已带语种前缀的公开路径
	Active   bool
	Children []DocNode
}

// docParentID 读取某文档的上级 ID（extra.parent）。
func docParentID(p *store.Post) int64 {
	if ex := strings.TrimSpace(p.Extra); ex != "" {
		var m map[string]any
		if json.Unmarshal([]byte(ex), &m) == nil {
			return anyToInt64(m["parent"])
		}
	}
	return 0
}

// docOrder 读取某文档的排序权重（extra.order）。
func docOrder(p *store.Post) float64 {
	if ex := strings.TrimSpace(p.Extra); ex != "" {
		var m map[string]any
		if json.Unmarshal([]byte(ex), &m) == nil {
			return anyToFloat(m["order"])
		}
	}
	return 0
}

// docChildren 由某语种全部已发布层级内容构建「上级 → 子级（已按 order、标题排序）」映射。
// 父级缺失者归到顶层（0）。
func docChildren(posts []*store.Post) map[int64][]*store.Post {
	ids := map[int64]bool{}
	for _, p := range posts {
		ids[p.ID] = true
	}
	childrenOf := map[int64][]*store.Post{}
	for _, p := range posts {
		parent := docParentID(p)
		if parent != 0 && !ids[parent] {
			parent = 0
		}
		childrenOf[parent] = append(childrenOf[parent], p)
	}
	for k := range childrenOf {
		sort.SliceStable(childrenOf[k], func(i, j int) bool {
			if oa, ob := docOrder(childrenOf[k][i]), docOrder(childrenOf[k][j]); oa != ob {
				return oa < ob
			}
			return childrenOf[k][i].Title < childrenOf[k][j].Title
		})
	}
	return childrenOf
}

// buildDocNav 构建 GitBook 式文档左侧导航树：顶层文档为分类、子文档为其下章节，
// 各自独立成页（URL 为真实页面路径）。currentID 用于高亮当前页；带环路保护。
func (s *Server) buildDocNav(ct *ContentType, lang string, currentID int64) []DocNode {
	posts, _ := s.store.AllPublishedByType(ct.Key, lang)
	if len(posts) == 0 {
		return nil
	}
	childrenOf := docChildren(posts)
	seen := map[int64]bool{}
	var build func(parent int64) []DocNode
	build = func(parent int64) []DocNode {
		var out []DocNode
		for _, c := range childrenOf[parent] {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			out = append(out, DocNode{
				Title:    c.Title,
				URL:      "/" + lang + publicContentPath(ct.Key, c.Slug),
				Active:   c.ID == currentID,
				Children: build(c.ID),
			})
		}
		return out
	}
	return build(0)
}

var docLeadHeadingRe = regexp.MustCompile(`(?is)^\s*<h[1-3][^>]*>(.*?)</h[1-3]>\s*`)
var docHTMLTagRe = regexp.MustCompile(`<[^>]+>`)

// stripRedundantHeading 去掉正文开头与页面标题完全重复的标题（文档标题已由 H1 呈现）。
func stripRedundantHeading(html template.HTML, title string) template.HTML {
	s := string(html)
	m := docLeadHeadingRe.FindStringSubmatch(s)
	if m == nil {
		return html
	}
	inner := strings.TrimSpace(docHTMLTagRe.ReplaceAllString(m[1], ""))
	if inner != "" && inner == strings.TrimSpace(title) {
		return template.HTML(strings.TrimPrefix(s, m[0]))
	}
	return html
}

// adminDocOrder 把后台层级类型列表（含草稿）按「分类 → 章节」深度优先排序，
// 并返回每条的缩进层级与上级 ID，让后台列表能展示结构、支持同级拖动排序。
func adminDocOrder(posts []*store.Post) ([]*store.Post, map[int64]int, map[int64]int64) {
	childrenOf := docChildren(posts)
	out := make([]*store.Post, 0, len(posts))
	depth := map[int64]int{}
	parent := map[int64]int64{}
	seen := map[int64]bool{}
	var walk func(par int64, d int)
	walk = func(par int64, d int) {
		for _, c := range childrenOf[par] {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			depth[c.ID] = d
			parent[c.ID] = par
			out = append(out, c)
			walk(c.ID, d+1)
		}
	}
	walk(0, 0)
	// 兜底：任何没被树覆盖到的（理论上不会有）追加在后面
	for _, p := range posts {
		if !seen[p.ID] {
			out = append(out, p)
		}
	}
	return out, depth, parent
}

// activeNodeChildren 返回导航树里「当前页」节点的下级章节（供详情页底部列出）。
func activeNodeChildren(tree []DocNode) []DocNode {
	for _, n := range tree {
		if n.Active {
			return n.Children
		}
		if c := activeNodeChildren(n.Children); c != nil {
			return c
		}
	}
	return nil
}

func anyToInt64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	case json.Number:
		n, _ := t.Int64()
		return n
	}
	return 0
}

func anyToFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	}
	return 0
}
