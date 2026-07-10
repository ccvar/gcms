package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"cms.ccvar.com/internal/store"
)

// 类型管理 API（给 AI/自动化）：把后台「扩展」的能力释放给技能包——
//   GET    /api/admin/v1/types?all=1        列全部类型（含未启用，带 enabled 标记）
//   POST   /api/admin/v1/types/{key}/enable 启用某扩展类型（本站）
//   POST   /api/admin/v1/types/{key}/disable 停用
//   POST   /api/admin/v1/types              创建自定义类型（DB 驱动，等价后台设计器）
//   PUT    /api/admin/v1/types/{key}        修改自定义类型（仅 DB 类型；内置扩展不可改）
//   DELETE /api/admin/v1/types/{key}        删除自定义类型（有内容一律拒绝，无 force）
// 权限：types:write（content:write 通配同样放行）。这两个 scope 随本批加入密钥表单的
// 「扩展内容类型」权限组——旧密钥需在后台重新勾选后才能管理类型。读取仍是任意有效密钥。

const apiScopeTypesWrite = "types:write"

// apiTypeMaxFields 是 API 创建/修改类型时的字段数上限（设计器表单是 8 槽；
// API 给宽一点但仍有界，防 AI 造出巨型 schema）。
const apiTypeMaxFields = 16

// apiTypeMaxCustom 每站自定义类型数量上限（护栏：防 AI 失控刷类型）。
const apiTypeMaxCustom = 30

func (s *Server) apiTypesWriteGuard(w http.ResponseWriter, r *http.Request) (*automationAuth, bool) {
	auth, ok := s.requireAutomationToken(w, r)
	if !ok {
		return nil, false
	}
	if !automationScopeAllowed(auth.scopes, apiScopeTypesWrite) {
		apiError(w, http.StatusForbidden, "missing_scope", "这条访问权限不能管理内容类型（需要 types:write 或 content:write，在密钥的「扩展内容类型」权限组勾选）。")
		return nil, false
	}
	return auth, true
}

// extTypeExists 返回 key 对应的扩展类型（代码注册表或 DB），内置 post/page/link 返回 nil。
func (s *Server) extTypeExists(key string) *ContentType {
	if ct := contentTypeByKey(key); ct != nil {
		if ct.Builtin {
			return nil
		}
		return ct
	}
	for _, ct := range s.allExtTypes() {
		if ct.Key == key {
			return ct
		}
	}
	return nil
}

func (s *Server) apiTypeEnable(w http.ResponseWriter, r *http.Request) {
	s.apiTypeSetEnabled(w, r, true)
}

func (s *Server) apiTypeDisable(w http.ResponseWriter, r *http.Request) {
	s.apiTypeSetEnabled(w, r, false)
}

func (s *Server) apiTypeSetEnabled(w http.ResponseWriter, r *http.Request, enable bool) {
	auth, ok := s.apiTypesWriteGuard(w, r)
	if !ok {
		return
	}
	key := strings.TrimSpace(r.PathValue("key"))
	if s.extTypeExists(key) == nil {
		apiError(w, http.StatusNotFound, "type_not_found", "没有这个扩展类型："+key+"（内置 posts/pages/links 不可启停）。")
		return
	}
	s.typesMu.Lock()
	defer s.typesMu.Unlock()
	enabled := s.enabledTypeSet()
	if enable {
		enabled[key] = true
	} else {
		delete(enabled, key)
	}
	if err := s.store.SetSetting(enabledContentTypesKey, joinEnabledTypes(enabled)); err != nil {
		s.apiServerError(w, err)
		return
	}
	s.clearGeneratedCaches()
	action := "type_disable"
	if enable {
		action = "type_enable"
	}
	s.recordAutomationLog(auth, action, "content_type", 0, "内容类型 "+key)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": key, "enabled": enable})
}

// ---- 创建 / 修改 / 删除自定义类型 ----

type apiTypeFieldInput struct {
	Key       string   `json:"key"`
	Label     string   `json:"label"`
	LabelEn   string   `json:"label_en"`
	Type      string   `json:"type"`
	Required  bool     `json:"required"`
	Localized bool     `json:"localized"`
	Help      string   `json:"help"`
	Options   []string `json:"options"`
}

type apiTypeInput struct {
	Key          string              `json:"key"`
	Name         string              `json:"name"`
	NameEn       string              `json:"name_en"`
	Icon         string              `json:"icon"`
	Fields       []apiTypeFieldInput `json:"fields"`
	HasCategory  *bool               `json:"has_category"`
	Searchable   *bool               `json:"searchable"`
	Hierarchical *bool               `json:"hierarchical"`
}

func validTypeFieldType(t string) bool {
	for _, v := range typeFieldTypeOptions() {
		if t == v {
			return true
		}
	}
	return false
}

// apiTypeFieldsJSON 把 API 字段输入编码为存库 JSON（与设计器 typeFieldsFromForm 同构：
// key 缺省从 label_en 推导、去重、type 白名单校验）。
func apiTypeFieldsJSON(in []apiTypeFieldInput) (string, error) {
	if len(in) == 0 {
		return "", fmt.Errorf("fields 不能为空——一个类型至少要有一个字段")
	}
	if len(in) > apiTypeMaxFields {
		return "", fmt.Errorf("字段太多（%d 个），上限 %d", len(in), apiTypeMaxFields)
	}
	var fields []fieldJSON
	seen := map[string]bool{}
	for i, f := range in {
		if len(f.Label) > 80 || len(f.LabelEn) > 80 || len(f.Help) > 300 {
			return "", fmt.Errorf("字段 %d：label ≤80 字符、help ≤300 字符", i+1)
		}
		if len(f.Options) > 50 {
			return "", fmt.Errorf("字段 %d：options 最多 50 项", i+1)
		}
		for _, o := range f.Options {
			if len(o) > 80 {
				return "", fmt.Errorf("字段 %d：单个 option ≤80 字符", i+1)
			}
		}
		key := slugify(f.Key)
		if key == "" {
			key = slugify(f.LabelEn)
		}
		if key == "" {
			key = fmt.Sprintf("field%d", i+1)
		}
		for base, n := key, 2; seen[key]; n++ {
			key = fmt.Sprintf("%s-%d", base, n)
		}
		seen[key] = true
		ft := strings.TrimSpace(f.Type)
		if ft == "" {
			ft = "text"
		}
		if !validTypeFieldType(ft) {
			return "", fmt.Errorf("字段 %q 的 type %q 不支持；可用：%s", key, ft, strings.Join(typeFieldTypeOptions(), "/"))
		}
		if len(f.Options) > 0 && ft != "select" {
			return "", fmt.Errorf("字段 %q：options 只对 select 类型有效", key)
		}
		fj := fieldJSON{
			Key: key, Label: map[string]string{}, Type: ft,
			Required: f.Required, Localized: f.Localized, Options: f.Options,
		}
		if v := strings.TrimSpace(f.Label); v != "" {
			fj.Label["zh"] = v
		}
		if v := strings.TrimSpace(f.LabelEn); v != "" {
			fj.Label["en"] = v
		}
		if len(fj.Label) == 0 {
			fj.Label["zh"] = key
		}
		if v := strings.TrimSpace(f.Help); v != "" {
			fj.Help = map[string]string{"zh": v}
		}
		fields = append(fields, fj)
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *Server) apiTypeCreate(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.apiTypesWriteGuard(w, r)
	if !ok {
		return
	}
	var in apiTypeInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	s.typesMu.Lock()
	defer s.typesMu.Unlock()
	key := slugify(in.Key)
	if key == "" {
		key = slugify(in.NameEn)
	}
	if !s.adminTypeKeyValid(key) {
		apiError(w, http.StatusBadRequest, "bad_key", "类型标识无效或已被占用（小写字母/数字/连字符，≤32 字符，不可用保留字或与既有类型/路由冲突）："+key)
		return
	}
	rows, err := s.store.ListContentTypes()
	if err != nil {
		s.apiServerError(w, err) // fail-closed：查不了数量就不建
		return
	}
	if len(rows) >= apiTypeMaxCustom {
		apiError(w, http.StatusBadRequest, "too_many_types", fmt.Sprintf("本站自定义类型已达上限 %d 个，请先清理不用的类型。", apiTypeMaxCustom))
		return
	}
	if strings.TrimSpace(in.Name) == "" && strings.TrimSpace(in.NameEn) == "" {
		apiError(w, http.StatusBadRequest, "bad_name", "name / name_en 至少填一个。")
		return
	}
	if len(in.Name) > 80 || len(in.NameEn) > 80 || len(in.Icon) > 64 {
		apiError(w, http.StatusBadRequest, "bad_name", "name/name_en ≤80 字符，icon ≤64 字符。")
		return
	}
	fieldsJSON, err := apiTypeFieldsJSON(in.Fields)
	if err != nil {
		apiError(w, http.StatusBadRequest, "bad_fields", err.Error())
		return
	}
	row := &store.ContentTypeRow{
		Key:          key,
		Name:         labelJSON(in.Name, in.NameEn),
		Icon:         strings.TrimSpace(in.Icon),
		URLPrefix:    key,
		Fields:       fieldsJSON,
		HasCategory:  in.HasCategory != nil && *in.HasCategory,
		Searchable:   in.Searchable == nil || *in.Searchable,
		Hierarchical: in.Hierarchical != nil && *in.Hierarchical,
	}
	if err := s.store.SaveContentType(row); err != nil {
		s.apiServerError(w, err)
		return
	}
	enabled := s.enabledTypeSet()
	enabled[key] = true
	_ = s.store.SetSetting(enabledContentTypesKey, joinEnabledTypes(enabled))
	s.clearGeneratedCaches()
	s.recordAutomationLog(auth, "type_create", "content_type", 0, "新建内容类型 "+key)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "key": key, "collection": key, "enabled": true})
}

func (s *Server) apiTypeUpdate(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.apiTypesWriteGuard(w, r)
	if !ok {
		return
	}
	s.typesMu.Lock()
	defer s.typesMu.Unlock()
	key := strings.TrimSpace(r.PathValue("key"))
	existing, _ := s.store.GetContentType(key)
	if existing == nil {
		if contentTypeByKey(key) != nil {
			apiError(w, http.StatusBadRequest, "builtin_type", "内置类型不可修改（product/doc/event/gallery 等由系统维护）。")
			return
		}
		apiError(w, http.StatusNotFound, "type_not_found", "没有这个自定义类型："+key)
		return
	}
	var in apiTypeInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	row := &store.ContentTypeRow{
		Key:          key,
		Name:         existing.Name,
		Icon:         existing.Icon,
		URLPrefix:    key,
		Fields:       existing.Fields,
		HasCategory:  existing.HasCategory,
		Searchable:   existing.Searchable,
		Hierarchical: existing.Hierarchical,
	}
	if strings.TrimSpace(in.Name) != "" || strings.TrimSpace(in.NameEn) != "" {
		row.Name = labelJSON(in.Name, in.NameEn)
	}
	if strings.TrimSpace(in.Icon) != "" {
		row.Icon = strings.TrimSpace(in.Icon)
	}
	if in.Fields != nil {
		fieldsJSON, err := apiTypeFieldsJSON(in.Fields)
		if err != nil {
			apiError(w, http.StatusBadRequest, "bad_fields", err.Error())
			return
		}
		row.Fields = fieldsJSON
	}
	if in.HasCategory != nil {
		row.HasCategory = *in.HasCategory
	}
	if in.Searchable != nil {
		row.Searchable = *in.Searchable
	}
	if in.Hierarchical != nil {
		row.Hierarchical = *in.Hierarchical
	}
	if err := s.store.SaveContentType(row); err != nil {
		s.apiServerError(w, err)
		return
	}
	s.clearGeneratedCaches()
	s.recordAutomationLog(auth, "type_update", "content_type", 0, "修改内容类型 "+key)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": key})
}

func (s *Server) apiTypeDelete(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.apiTypesWriteGuard(w, r)
	if !ok {
		return
	}
	s.typesMu.Lock()
	defer s.typesMu.Unlock()
	key := strings.TrimSpace(r.PathValue("key"))
	existing, _ := s.store.GetContentType(key)
	if existing == nil {
		if contentTypeByKey(key) != nil {
			apiError(w, http.StatusBadRequest, "builtin_type", "内置类型不可删除，可用 disable 停用。")
			return
		}
		apiError(w, http.StatusNotFound, "type_not_found", "没有这个自定义类型："+key)
		return
	}
	// 护栏：有内容（任何语种、任何状态）一律拒绝——API 无 force；确要删除请先清空内容或走后台。
	// fail-closed：计数查询出错同样拒绝（否则 DB 瞬时故障会绕过护栏直接删）。
	n, err := s.store.CountByType(key)
	if err != nil {
		s.apiServerError(w, err)
		return
	}
	if n > 0 {
		apiError(w, http.StatusConflict, "type_has_content",
			fmt.Sprintf("该类型下还有 %d 条内容，先删除内容再删类型（或到后台操作）。", n))
		return
	}
	if err := s.store.DeleteContentType(key); err != nil {
		s.apiServerError(w, err)
		return
	}
	enabled := s.enabledTypeSet()
	delete(enabled, key)
	_ = s.store.SetSetting(enabledContentTypesKey, joinEnabledTypes(enabled))
	s.clearGeneratedCaches()
	s.recordAutomationLog(auth, "type_delete", "content_type", 0, "删除内容类型 "+key)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": key, "deleted": true})
}

func (s *Server) apiServerError(w http.ResponseWriter, err error) {
	apiError(w, http.StatusInternalServerError, "server_error", err.Error())
}
