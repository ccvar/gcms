package web

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

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
		mux.HandleFunc("GET /"+pref+"/{slug}", func(w http.ResponseWriter, r *http.Request) { s.extDetail(w, r, ct) })
		mux.HandleFunc("GET /"+pref+"/{slug}/{$}", func(w http.ResponseWriter, r *http.Request) { s.extDetail(w, r, ct) })
	}
}

// extList 渲染扩展类型的归档列表（/{prefix}）。
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
	total, _ := s.store.CountPublishedByType(ct.Key, lang, 0)
	posts, _ := s.store.ListPublishedByType(ct.Key, lang, 0, (page-1)*size, size)
	v := s.view(r, ct.Key)
	v.CT = ct
	v.Posts = posts
	base := "/" + ct.URLPrefix
	v.SEO = seo.Meta{
		Title:       ct.Name(lang) + " — " + v.Site.Name,
		Description: v.Site.Description,
		Canonical:   v.Site.Abs(base),
		OGType:      "website",
	}
	setPagination(v, page, ceilDiv(total, size), base)
	v.Langs = s.langSwitchForRequest(r, lang, nil, base)
	if ct.Hierarchical {
		v.DocTree = s.buildDocTree(ct, lang, 0)
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
	s.fillDefaultAuthor(p)
	v := s.view(r, ct.Key)
	v.CT = ct
	v.Post = p
	v.ContentHTML, v.TOC = s.renderedContent(p)
	v.Fields = renderFieldValues(ct, p, lang)
	if ct.Hierarchical {
		v.DocTree = s.buildDocTree(ct, lang, p.ID)
	}
	canon := publicContentPath(ct.Key, p.Slug)
	author := strings.TrimSpace(p.Author)
	if author == "" {
		author = v.Site.Author
	}
	v.SEO = seo.Meta{
		Title:       p.Title + " — " + v.Site.Name,
		Description: metaDescOf(p),
		Canonical:   v.Site.Abs(canon),
		OGType:      "article",
		Image:       p.CoverImage,
		Author:      author,
	}
	ph := map[string]string{p.Lang: canon}
	if trs, _ := s.store.TranslationsPublished(p.TransGroup); trs != nil {
		for _, t := range trs {
			if t.Type == ct.Key {
				ph[t.Lang] = publicContentPath(ct.Key, t.Slug)
			}
		}
	}
	v.Langs, v.SEO.Alternates = s.i18nLinksForRequest(r, v.Site.BaseURL, lang, ph)
	s.rnd.Public(w, detailTemplate(ct), http.StatusOK, v)
}

func listTemplate(ct *ContentType) string {
	if ct.ListTemplate != "" {
		return ct.ListTemplate
	}
	return "generic_list"
}

func detailTemplate(ct *ContentType) string {
	if ct.DetailTemplate != "" {
		return ct.DetailTemplate
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

// buildDocTree 由某语种全部已发布的层级内容构建父子树（按 extra.order 再按标题排序）。
// 父级不存在者视为根；带环路保护。currentID 用于标记当前页高亮。
func (s *Server) buildDocTree(ct *ContentType, lang string, currentID int64) []DocNode {
	posts, _ := s.store.AllPublishedByType(ct.Key, lang)
	if len(posts) == 0 {
		return nil
	}
	type docMeta struct {
		post   *store.Post
		parent int64
		order  float64
	}
	ids := map[int64]bool{}
	for _, p := range posts {
		ids[p.ID] = true
	}
	childrenOf := map[int64][]docMeta{}
	for _, p := range posts {
		dm := docMeta{post: p}
		if ex := strings.TrimSpace(p.Extra); ex != "" {
			var m map[string]any
			if json.Unmarshal([]byte(ex), &m) == nil {
				dm.parent = anyToInt64(m["parent"])
				dm.order = anyToFloat(m["order"])
			}
		}
		if dm.parent != 0 && !ids[dm.parent] {
			dm.parent = 0 // 父级缺失 → 当作根
		}
		childrenOf[dm.parent] = append(childrenOf[dm.parent], dm)
	}
	for k := range childrenOf {
		sort.SliceStable(childrenOf[k], func(i, j int) bool {
			a, b := childrenOf[k][i], childrenOf[k][j]
			if a.order != b.order {
				return a.order < b.order
			}
			return a.post.Title < b.post.Title
		})
	}
	seen := map[int64]bool{}
	var build func(parent int64) []DocNode
	build = func(parent int64) []DocNode {
		var out []DocNode
		for _, dm := range childrenOf[parent] {
			if seen[dm.post.ID] {
				continue // 环路保护
			}
			seen[dm.post.ID] = true
			out = append(out, DocNode{
				Title:    dm.post.Title,
				URL:      "/" + lang + publicContentPath(ct.Key, dm.post.Slug),
				Active:   dm.post.ID == currentID,
				Children: build(dm.post.ID),
			})
		}
		return out
	}
	return build(0)
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
