package web

import (
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

// typeFieldsFromForm 把设计器的字段行编码为存库用的 JSON（跳过 key 为空的行）。
func typeFieldsFromForm(r *http.Request) string {
	var fields []fieldJSON
	for i := 0; i < typeFieldSlots; i++ {
		key := slugify(r.FormValue(fmt.Sprintf("field_%d_key", i)))
		if key == "" {
			continue
		}
		fj := fieldJSON{
			Key:       key,
			Label:     map[string]string{},
			Type:      strings.TrimSpace(r.FormValue(fmt.Sprintf("field_%d_type", i))),
			Required:  r.FormValue(fmt.Sprintf("field_%d_required", i)) == "1",
			Localized: r.FormValue(fmt.Sprintf("field_%d_localized", i)) == "1",
		}
		if zh := strings.TrimSpace(r.FormValue(fmt.Sprintf("field_%d_label_zh", i))); zh != "" {
			fj.Label["zh"] = zh
		}
		if en := strings.TrimSpace(r.FormValue(fmt.Sprintf("field_%d_label_en", i))); en != "" {
			fj.Label["en"] = en
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
