package web

import (
	"encoding/json"
	"net/http"
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
		mux.HandleFunc("GET /"+pref+"/page/{pageNum}", func(w http.ResponseWriter, r *http.Request) { s.extList(w, r, ct) })
		mux.HandleFunc("GET /"+pref+"/page/{pageNum}/{$}", func(w http.ResponseWriter, r *http.Request) { s.extList(w, r, ct) })
		mux.HandleFunc("GET /"+pref+"/{slug}", func(w http.ResponseWriter, r *http.Request) { s.extDetail(w, r, ct) })
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
	canon := "/" + ct.URLPrefix + "/" + p.Slug
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
				ph[t.Lang] = "/" + ct.URLPrefix + "/" + t.Slug
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
		default:
			fv.Text = scalarString(val)
		}
		if fv.Text == "" && len(fv.List) == 0 {
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
