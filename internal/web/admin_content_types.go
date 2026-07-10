package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"cms.ccvar.com/internal/store"
)

// 后台「扩展」分区——内容类型引擎的管理入口。
// 全部代码集中在本文件，与既有 admin.go 解耦，互不干扰。

// ---------- hub：启用/停用 ----------

// adminExtHub 渲染「扩展」hub：列出全部扩展类型及其启用状态。
func (s *Server) adminExtHub(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	v := s.adminView(r, "扩展")
	s.authed(v, sess)
	v.ExtTypes = s.extTypeRows(s.editLang(r))
	if r.URL.Query().Get("saved") == "1" {
		v.Flash = "已更新。"
	}
	s.rnd.Admin(w, "extensions", http.StatusOK, v)
}

// adminExtToggle 启用/停用某扩展类型（写入本站 enabled_content_types）。
func (s *Server) adminExtToggle(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	key := strings.TrimSpace(r.FormValue("type"))
	ct := s.lookupType(key)
	if ct == nil || ct.Builtin {
		s.notFound(w, r)
		return
	}
	enabled := s.enabledTypeSet()
	if r.FormValue("on") == "1" {
		enabled[key] = true
	} else {
		delete(enabled, key)
	}
	if err := s.store.SetSetting(enabledContentTypesKey, joinEnabledTypes(enabled)); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	http.Redirect(w, r, "/admin/extensions?saved=1", http.StatusSeeOther)
}

// ---------- 单个类型的内容 CRUD ----------

// adminExtType 解析并校验路由里的 {type}：必须是已注册、非内置、且对本站启用的扩展类型。
func (s *Server) adminExtType(r *http.Request) *ContentType {
	ct := s.lookupType(strings.TrimSpace(r.PathValue("type")))
	if ct == nil || ct.Builtin || !s.contentTypeActive(ct.Key) {
		return nil
	}
	return ct
}

func (s *Server) adminExtList(w http.ResponseWriter, r *http.Request) {
	ct := s.adminExtType(r)
	if ct == nil {
		s.notFound(w, r)
		return
	}
	sess, _ := s.currentSession(r)
	lang := s.editLang(r)
	posts, err := s.store.ListAllByType(ct.Key, lang)
	if err != nil {
		s.serverError(w, err)
		return
	}
	v := s.adminView(r, ct.Name(s.adminLang(r)))
	s.authed(v, sess)
	v.ExtType = ct
	if ct.Hierarchical {
		// 层级类型：按「分类 → 章节」排好序并标注缩进层级，让后台一眼看清结构、支持同级拖动排序。
		posts, v.ExtDepth, v.ExtParent = adminDocOrder(posts)
	}
	v.ExtPosts = posts
	v.EditLang = lang
	if r.URL.Query().Get("deleted") == "1" {
		v.Flash = "已删除。"
	}
	s.rnd.Admin(w, "ext_list", http.StatusOK, v)
}

func (s *Server) adminExtNew(w http.ResponseWriter, r *http.Request) {
	ct := s.adminExtType(r)
	if ct == nil {
		s.notFound(w, r)
		return
	}
	sess, _ := s.currentSession(r)
	p := &store.Post{Type: ct.Key, Status: "draft", Lang: s.editLang(r)}
	s.rnd.Admin(w, "ext_edit", http.StatusOK, s.adminExtEditView(r, sess, ct, p, "", ""))
}

func (s *Server) adminExtEdit(w http.ResponseWriter, r *http.Request) {
	ct := s.adminExtType(r)
	if ct == nil {
		s.notFound(w, r)
		return
	}
	sess, _ := s.currentSession(r)
	p, _ := s.store.GetPostByID(atoi64(r.PathValue("id")))
	if p == nil || p.Type != ct.Key {
		s.notFound(w, r)
		return
	}
	flash := ""
	if r.URL.Query().Get("saved") == "1" {
		flash = "已保存。"
	}
	s.rnd.Admin(w, "ext_edit", http.StatusOK, s.adminExtEditView(r, sess, ct, p, flash, ""))
}

// adminExtPreview 后台草稿预览：以公开详情模板渲染扩展内容（含草稿），noindex、不跳转章节。
func (s *Server) adminExtPreview(w http.ResponseWriter, r *http.Request) {
	ct := s.adminExtType(r)
	if ct == nil {
		s.notFound(w, r)
		return
	}
	p, _ := s.store.GetPostByID(atoi64(r.PathValue("id")))
	if p == nil || p.Type != ct.Key {
		s.notFound(w, r)
		return
	}
	if sess, ok := s.currentSession(r); ok {
		if prefix := s.adminSitePreviewPrefix(sess.currentSiteID); prefix != "" {
			ctx := withPreviewRoutePrefix(withPreviewNoindex(r.Context()), prefix)
			r = r.Clone(ctx)
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
			w.Header().Set("Cache-Control", "no-store")
		}
	}
	s.renderExtDetail(w, r, ct, p, true)
}

// adminExtTranslate 为某条扩展内容创建/跳转到目标语种版本（共享 trans_group，复制自定义字段）。
// 与 adminTranslate 同构，但落点为 /admin/ext/{type}/{id}/edit。
func (s *Server) adminExtTranslate(w http.ResponseWriter, r *http.Request) {
	ct := s.adminExtType(r)
	if ct == nil {
		s.notFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	src, _ := s.store.GetPostByID(atoi64(r.PathValue("id")))
	if src == nil || src.Type != ct.Key {
		s.notFound(w, r)
		return
	}
	editPath := func(id int64) string { return fmt.Sprintf("/admin/ext/%s/%d/edit", ct.Key, id) }
	target := strings.TrimSpace(r.FormValue("lang"))
	if !s.langEnabled(target) || target == src.Lang {
		http.Redirect(w, r, editPath(src.ID), http.StatusSeeOther)
		return
	}
	// 已存在该语种版本 → 直接跳过去
	if trs, _ := s.store.TranslationsAll(src.TransGroup, 0); trs != nil {
		for _, t := range trs {
			if t.Lang == target {
				http.Redirect(w, r, editPath(t.ID), http.StatusSeeOther)
				return
			}
		}
	}
	np := &store.Post{
		Type: src.Type, Title: src.Title, Excerpt: src.Excerpt, Content: src.Content,
		MetaDesc: src.MetaDesc, Keywords: src.Keywords, CoverImage: src.CoverImage, Author: src.Author,
		Status: "draft", EditorMode: src.EditorMode, Lang: target, TransGroup: src.TransGroup,
		Extra: src.Extra, // 自定义字段一并带过去（含文档上级/排序）
	}
	np.Slug = s.uniqueSlug(target, src.Slug, 0)
	if src.CategoryID.Valid {
		if sc, _ := s.store.GetCategoryByID(src.CategoryID.Int64); sc != nil {
			if cts, _ := s.store.CategoryTranslations(sc.TransGroup); cts != nil {
				for _, c := range cts {
					if c.Lang == target {
						np.CategoryID = sql.NullInt64{Int64: c.ID, Valid: true}
					}
				}
			}
		}
	}
	id, err := s.store.CreatePost(np)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	http.Redirect(w, r, editPath(id), http.StatusSeeOther)
}

func (s *Server) adminExtEditView(r *http.Request, sess session, ct *ContentType, p *store.Post, flash, formErr string) *View {
	v := s.adminView(r, ct.Name(s.adminLang(r)))
	s.authed(v, sess)
	v.ExtType = ct
	v.ExtEdit = p
	v.ExtValues = extraToFormValues(ct, p.Extra)
	v.EditLang = p.Lang
	if v.EditLang == "" {
		v.EditLang = s.editLang(r)
	}
	v.Flash = flash
	v.FormErr = formErr
	if ct.HasCategory {
		v.Categories, _ = s.store.ListCategories(v.EditLang, ct.Key)
	}
	if ctHasRelation(ct) {
		opts, _ := s.store.ListAllByType(ct.Key, v.EditLang)
		for _, o := range opts {
			if o.ID != p.ID { // 不能把自己设为上级
				v.ExtRelOptions = append(v.ExtRelOptions, o)
			}
		}
	}
	if p.ID != 0 && p.TransGroup != "" {
		v.Trans, _ = s.store.TranslationsAll(p.TransGroup, p.ID)
	}
	return v
}

// ctHasRelation 判断类型是否含关联字段（如文档上级）。
func ctHasRelation(ct *ContentType) bool {
	for _, f := range ct.Fields {
		if f.Type == FieldRelation {
			return true
		}
	}
	return false
}

func (s *Server) adminExtCreate(w http.ResponseWriter, r *http.Request) {
	ct := s.adminExtType(r)
	if ct == nil {
		s.notFound(w, r)
		return
	}
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	lang := s.editLang(r)
	p, formErr := postFromForm(r, 0, lang)
	p.Type = ct.Key
	p.Extra = extraFromForm(ct, r)
	if formErr != "" {
		s.rnd.Admin(w, "ext_edit", http.StatusOK, s.adminExtEditView(r, sess, ct, p, "", formErr))
		return
	}
	s.fillDefaultAuthor(p)
	p.Slug = s.uniqueSlug(lang, p.Slug, 0)
	id, err := s.store.CreatePost(p)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	http.Redirect(w, r, fmt.Sprintf("/admin/ext/%s/%d/edit?saved=1", ct.Key, id), http.StatusSeeOther)
}

func (s *Server) adminExtUpdate(w http.ResponseWriter, r *http.Request) {
	ct := s.adminExtType(r)
	if ct == nil {
		s.notFound(w, r)
		return
	}
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	id := atoi64(r.PathValue("id"))
	existing, _ := s.store.GetPostByID(id)
	if existing == nil || existing.Type != ct.Key {
		s.notFound(w, r)
		return
	}
	p, formErr := postFromForm(r, id, existing.Lang)
	p.Type = ct.Key
	p.Extra = extraFromForm(ct, r)
	// 保留创建时间、置顶、互译分组（与 adminUpdate 一致）。
	p.CreatedAt = existing.CreatedAt
	p.Featured = existing.Featured
	p.TransGroup = existing.TransGroup
	if formErr != "" {
		s.rnd.Admin(w, "ext_edit", http.StatusOK, s.adminExtEditView(r, sess, ct, p, "", formErr))
		return
	}
	s.fillDefaultAuthor(p)
	p.Slug = s.uniqueSlug(existing.Lang, p.Slug, id)
	if err := s.store.UpdatePost(p); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	http.Redirect(w, r, fmt.Sprintf("/admin/ext/%s/%d/edit?saved=1", ct.Key, id), http.StatusSeeOther)
}

func (s *Server) adminExtDelete(w http.ResponseWriter, r *http.Request) {
	ct := s.adminExtType(r)
	if ct == nil {
		s.notFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	id := atoi64(r.PathValue("id"))
	existing, _ := s.store.GetPostByID(id)
	if existing == nil || existing.Type != ct.Key {
		s.notFound(w, r)
		return
	}
	if err := s.store.DeletePost(id); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	http.Redirect(w, r, "/admin/ext/"+ct.Key+"?deleted=1", http.StatusSeeOther)
}

// adminExtReorder 保存层级类型同级条目的新顺序（把 extra.order 写为各自在列表中的下标）。
func (s *Server) adminExtReorder(w http.ResponseWriter, r *http.Request) {
	ct := s.adminExtType(r)
	if ct == nil || !ct.Hierarchical {
		s.notFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	for i, idStr := range r.Form["ids"] {
		p, _ := s.store.GetPostByID(atoi64(idStr))
		if p == nil || p.Type != ct.Key {
			continue
		}
		next := setExtraOrder(p.Extra, i)
		if next == p.Extra {
			continue
		}
		p.Extra = next
		if err := s.store.UpdatePost(p); err != nil {
			s.serverError(w, err)
			return
		}
	}
	s.clearGeneratedCaches()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// setExtraOrder 只更新 extra JSON 里的 order 字段，保留其它自定义字段。
func setExtraOrder(extra string, order int) string {
	m := map[string]any{}
	if t := strings.TrimSpace(extra); t != "" {
		_ = json.Unmarshal([]byte(t), &m)
	}
	if cur, ok := m["order"].(float64); ok && int(cur) == order {
		return extra
	}
	m["order"] = order
	b, err := json.Marshal(m)
	if err != nil {
		return extra
	}
	return string(b)
}

// ---------- 归档页文案（每站点、分语种自定义标题/简介）----------

const extArchiveMetaKey = "ext_archive_meta"

// extArchiveEntry 是某类型归档页的自定义文案（按语种）。
type extArchiveEntry struct {
	Title map[string]string `json:"title,omitempty"`
	Intro map[string]string `json:"intro,omitempty"`
}

func (s *Server) extArchiveMetaAll() map[string]extArchiveEntry {
	out := map[string]extArchiveEntry{}
	if raw := strings.TrimSpace(s.store.Setting(extArchiveMetaKey)); raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out
}

// extArchiveText 返回某类型归档页在某语种下的自定义标题与简介（未设置则为空）。
func (s *Server) extArchiveText(typeKey, lang string) (title, intro string) {
	if e, ok := s.extArchiveMetaAll()[typeKey]; ok {
		return strings.TrimSpace(e.Title[lang]), strings.TrimSpace(e.Intro[lang])
	}
	return "", ""
}

func (s *Server) adminExtArchiveForm(w http.ResponseWriter, r *http.Request) {
	ct := s.adminExtType(r)
	if ct == nil {
		s.notFound(w, r)
		return
	}
	sess, _ := s.currentSession(r)
	v := s.adminView(r, ct.Name(s.adminLang(r)))
	s.authed(v, sess)
	v.ExtType = ct
	v.EditLang = s.adminLang(r)
	vals := map[string]string{}
	if e, ok := s.extArchiveMetaAll()[ct.Key]; ok {
		for code, t := range e.Title {
			vals["title_"+code] = t
		}
		for code, in := range e.Intro {
			vals["intro_"+code] = in
		}
	}
	v.ExtValues = vals
	if r.URL.Query().Get("saved") == "1" {
		v.Flash = v.Admin.T("admin.ext.saved", "已保存。")
	}
	s.rnd.Admin(w, "ext_archive", http.StatusOK, v)
}

func (s *Server) adminExtArchiveSave(w http.ResponseWriter, r *http.Request) {
	ct := s.adminExtType(r)
	if ct == nil {
		s.notFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	all := s.extArchiveMetaAll()
	entry := extArchiveEntry{Title: map[string]string{}, Intro: map[string]string{}}
	for _, loc := range s.locales() {
		if t := strings.TrimSpace(r.FormValue("title_" + loc.Code)); t != "" {
			entry.Title[loc.Code] = t
		}
		if in := strings.TrimSpace(r.FormValue("intro_" + loc.Code)); in != "" {
			entry.Intro[loc.Code] = in
		}
	}
	if len(entry.Title) == 0 && len(entry.Intro) == 0 {
		delete(all, ct.Key)
	} else {
		all[ct.Key] = entry
	}
	b, _ := json.Marshal(all)
	if err := s.store.SetSetting(extArchiveMetaKey, string(b)); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}
	http.Redirect(w, r, "/admin/ext/"+ct.Key+"/archive?saved=1", http.StatusSeeOther)
}

// ---------- 自定义字段 <-> 表单 ----------

// extraFromForm 从表单读取自定义字段（输入名前缀 f_），按 schema 编码为 extra JSON。
func extraFromForm(ct *ContentType, r *http.Request) string {
	if len(ct.Fields) == 0 {
		return ""
	}
	m := map[string]any{}
	for _, f := range ct.Fields {
		raw := strings.TrimSpace(r.FormValue("f_" + f.Key))
		switch f.Type {
		case FieldGallery, FieldImage:
			if urls := splitLines(raw); len(urls) > 0 {
				m[f.Key] = urls
			}
		case FieldRepeater:
			if pairs := parsePairs(raw); len(pairs) > 0 {
				m[f.Key] = pairs
			}
		case FieldBool:
			if raw == "1" || raw == "on" {
				m[f.Key] = true
			}
		case FieldNumber:
			if raw != "" {
				if n, err := strconv.ParseFloat(raw, 64); err == nil {
					m[f.Key] = n
				} else {
					m[f.Key] = raw
				}
			}
		default:
			if raw != "" {
				m[f.Key] = raw
			}
		}
	}
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// extraToFormValues 把 extra JSON 反解为各字段的表单回填字符串。
func extraToFormValues(ct *ContentType, extra string) map[string]string {
	out := map[string]string{}
	extra = strings.TrimSpace(extra)
	if extra == "" || extra == "{}" {
		return out
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(extra), &m); err != nil {
		return out
	}
	for _, f := range ct.Fields {
		v, ok := m[f.Key]
		if !ok || v == nil {
			continue
		}
		switch f.Type {
		case FieldGallery, FieldImage:
			out[f.Key] = strings.Join(toStringList(v), "\n")
		case FieldRepeater:
			out[f.Key] = pairsToText(v)
		case FieldBool:
			if b, ok := v.(bool); ok && b {
				out[f.Key] = "1"
			}
		default:
			out[f.Key] = scalarString(v)
		}
	}
	return out
}

func splitLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

func parsePairs(s string) []map[string]string {
	var out []map[string]string
	for _, ln := range splitLines(s) {
		k, v, ok := strings.Cut(ln, ":")
		if !ok {
			k, v, _ = strings.Cut(ln, "：")
		}
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out = append(out, map[string]string{"k": k, "v": strings.TrimSpace(v)})
	}
	return out
}

func pairsToText(v any) string {
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	var lines []string
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
		lines = append(lines, k+": "+val)
	}
	return strings.Join(lines, "\n")
}

// ---------- Phase 3：可视化类型设计器 ----------

const typeFieldSlots = 8

// reservedTypePrefixes 是不能用作自定义类型 key/前缀的保留字（避免与既有路由冲突）。
var reservedTypePrefixes = []string{
	"post", "page", "link", "posts", "pages", "links", "category", "about", "start",
	"search", "admin", "api", "assets", "uploads", "sitemap", "robots", "rss", "favicon",
	// API 字面路由段：类型 key＝集合名＝URL 段，撞上会被字面路由遮蔽或劫持（评审确认）。
	"types", "media", "languages", "navigation", "site-profile", "sites", "skill-pack",
	"openapi", "api-docs", "preview", "preview-url", "featured", "relink",
}

// TypeFormView 驱动类型设计器表单。
type TypeFormView struct {
	IsNew        bool
	Key          string
	NameZh       string
	NameEn       string
	Icon         string
	HasCategory  bool
	Searchable   bool
	Hierarchical bool
	Fields       []TypeFieldForm
	FieldTypes   []string
}

// TypeFieldForm 是设计器里一行字段的表单值。
type TypeFieldForm struct {
	Key       string
	LabelZh   string
	LabelEn   string
	Type      string
	Required  bool
	Localized bool
}

func typeFieldTypeOptions() []string {
	return []string{"text", "textarea", "markdown", "number", "datetime", "url", "select", "bool", "image", "gallery", "repeater", "relation"}
}

// adminTypeKeyValid 校验自定义类型 key：小写字母/数字/连字符，非保留字，且不与已有类型冲突。
func (s *Server) adminTypeKeyValid(key string) bool {
	if key == "" || len(key) > 32 {
		return false
	}
	for _, r := range key {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	for _, rk := range reservedTypePrefixes {
		if key == rk {
			return false
		}
	}
	if contentTypeByKey(key) != nil {
		return false
	}
	// 与任何既有类型的 URL 前缀撞车也不行（如 products/docs/events 是内置类型的集合名，
	// 注册后会被前缀解析抢占，内容永远进不到自定义类型——评审实测确认）。
	if s.extTypeByPrefix(key) != nil {
		return false
	}
	// 已启用语种码（zh/en/自定义码）当类型 key 会与语种路由互相吞并。
	if s.langEnabled(key) {
		return false
	}
	if row, _ := s.store.GetContentType(key); row != nil {
		return false
	}
	return true
}

func (s *Server) adminTypeNew(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	v := s.adminView(r, "新建类型")
	s.authed(v, sess)
	v.TypeForm = newTypeFormView(nil)
	s.rnd.Admin(w, "ext_type_edit", http.StatusOK, v)
}

func (s *Server) adminTypeEdit(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	row, _ := s.store.GetContentType(strings.TrimSpace(r.PathValue("key")))
	if row == nil {
		s.notFound(w, r)
		return
	}
	v := s.adminView(r, "编辑类型")
	s.authed(v, sess)
	v.TypeForm = newTypeFormView(row)
	s.rnd.Admin(w, "ext_type_edit", http.StatusOK, v)
}

func (s *Server) adminTypeSave(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	editKey := strings.TrimSpace(r.PathValue("key"))
	isNew := editKey == ""
	key := editKey
	if isNew {
		key = slugify(r.FormValue("key"))
	}
	if isNew && !s.adminTypeKeyValid(key) {
		v := s.adminView(r, "新建类型")
		s.authed(v, sess)
		v.TypeForm = typeFormFromRequest(r, key, true)
		v.FormErr = "类型标识无效或已被占用（只能用小写字母、数字、连字符，且不能与已有类型冲突）。"
		s.rnd.Admin(w, "ext_type_edit", http.StatusOK, v)
		return
	}
	if !isNew {
		if existing, _ := s.store.GetContentType(key); existing == nil {
			s.notFound(w, r)
			return
		}
	}
	row := &store.ContentTypeRow{
		Key:          key,
		Name:         labelJSON(r.FormValue("name_zh"), r.FormValue("name_en")),
		Icon:         strings.TrimSpace(r.FormValue("icon")),
		URLPrefix:    key,
		Fields:       typeFieldsFromForm(r),
		HasCategory:  r.FormValue("has_category") == "1",
		Searchable:   r.FormValue("searchable") == "1",
		Hierarchical: r.FormValue("hierarchical") == "1",
	}
	if err := s.store.SaveContentType(row); err != nil {
		s.serverError(w, err)
		return
	}
	// 新建/保存即对本站启用。
	enabled := s.enabledTypeSet()
	enabled[key] = true
	_ = s.store.SetSetting(enabledContentTypesKey, joinEnabledTypes(enabled))
	s.clearGeneratedCaches()
	http.Redirect(w, r, "/admin/ext/"+key, http.StatusSeeOther)
}

func (s *Server) adminTypeDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	key := strings.TrimSpace(r.PathValue("key"))
	if err := s.store.DeleteContentType(key); err != nil {
		s.serverError(w, err)
		return
	}
	enabled := s.enabledTypeSet()
	delete(enabled, key)
	_ = s.store.SetSetting(enabledContentTypesKey, joinEnabledTypes(enabled))
	s.clearGeneratedCaches()
	http.Redirect(w, r, "/admin/extensions", http.StatusSeeOther)
}

func newTypeFormView(row *store.ContentTypeRow) *TypeFormView {
	f := &TypeFormView{Searchable: true, FieldTypes: typeFieldTypeOptions()}
	if row == nil {
		f.IsNew = true
	} else {
		names := parseLabelJSON(row.Name, row.Key)
		f.Key = row.Key
		f.NameZh = names["zh"]
		f.NameEn = names["en"]
		f.Icon = row.Icon
		f.HasCategory = row.HasCategory
		f.Searchable = row.Searchable
		f.Hierarchical = row.Hierarchical
		for _, fl := range parseFieldsJSON(row.Fields) {
			f.Fields = append(f.Fields, TypeFieldForm{
				Key: fl.Key, LabelZh: fl.Labels["zh"], LabelEn: fl.Labels["en"],
				Type: string(fl.Type), Required: fl.Required, Localized: fl.Localized,
			})
		}
	}
	show := len(f.Fields) + 2
	if show < 3 {
		show = 3
	}
	if show > typeFieldSlots {
		show = typeFieldSlots
	}
	for len(f.Fields) < show {
		f.Fields = append(f.Fields, TypeFieldForm{})
	}
	return f
}

func typeFormFromRequest(r *http.Request, key string, isNew bool) *TypeFormView {
	f := &TypeFormView{
		IsNew:        isNew,
		Key:          key,
		NameZh:       strings.TrimSpace(r.FormValue("name_zh")),
		NameEn:       strings.TrimSpace(r.FormValue("name_en")),
		Icon:         strings.TrimSpace(r.FormValue("icon")),
		HasCategory:  r.FormValue("has_category") == "1",
		Searchable:   r.FormValue("searchable") == "1",
		Hierarchical: r.FormValue("hierarchical") == "1",
		FieldTypes:   typeFieldTypeOptions(),
	}
	for i := 0; i < typeFieldSlots; i++ {
		f.Fields = append(f.Fields, TypeFieldForm{
			Key:       strings.TrimSpace(r.FormValue(fmt.Sprintf("field_%d_key", i))),
			LabelZh:   strings.TrimSpace(r.FormValue(fmt.Sprintf("field_%d_label_zh", i))),
			LabelEn:   strings.TrimSpace(r.FormValue(fmt.Sprintf("field_%d_label_en", i))),
			Type:      strings.TrimSpace(r.FormValue(fmt.Sprintf("field_%d_type", i))),
			Required:  r.FormValue(fmt.Sprintf("field_%d_required", i)) == "1",
			Localized: r.FormValue(fmt.Sprintf("field_%d_localized", i)) == "1",
		})
	}
	return f
}

// typeFieldsFromForm 把设计器的字段行编码为存库用的 JSON。
// 标识 key 优先用隐藏的 field_N_key（编辑时回填、保证稳定），为空则按英文名自动生成，
// 仅有中文名时给个稳定兜底标识；整行为空才跳过。
func typeFieldsFromForm(r *http.Request) string {
	var fields []fieldJSON
	seen := map[string]bool{}
	for i := 0; i < typeFieldSlots; i++ {
		labelZh := strings.TrimSpace(r.FormValue(fmt.Sprintf("field_%d_label_zh", i)))
		labelEn := strings.TrimSpace(r.FormValue(fmt.Sprintf("field_%d_label_en", i)))
		key := slugify(r.FormValue(fmt.Sprintf("field_%d_key", i)))
		if key == "" {
			key = slugify(labelEn)
		}
		if key == "" && (labelZh != "" || labelEn != "") {
			key = fmt.Sprintf("field%d", len(fields)+1)
		}
		if key == "" {
			continue
		}
		for base, n := key, 2; seen[key]; n++ {
			key = fmt.Sprintf("%s-%d", base, n)
		}
		seen[key] = true
		fj := fieldJSON{
			Key:       key,
			Label:     map[string]string{},
			Type:      strings.TrimSpace(r.FormValue(fmt.Sprintf("field_%d_type", i))),
			Required:  r.FormValue(fmt.Sprintf("field_%d_required", i)) == "1",
			Localized: r.FormValue(fmt.Sprintf("field_%d_localized", i)) == "1",
		}
		if labelZh != "" {
			fj.Label["zh"] = labelZh
		}
		if labelEn != "" {
			fj.Label["en"] = labelEn
		}
		if fj.Type == "" {
			fj.Type = "text"
		}
		fields = append(fields, fj)
	}
	if len(fields) == 0 {
		return "[]"
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func labelJSON(zh, en string) string {
	m := map[string]string{}
	if zh = strings.TrimSpace(zh); zh != "" {
		m["zh"] = zh
	}
	if en = strings.TrimSpace(en); en != "" {
		m["en"] = en
	}
	b, _ := json.Marshal(m)
	return string(b)
}
