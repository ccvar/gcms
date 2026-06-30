package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

// TestTypeDesignerEndToEnd 走完整链路：可视化设计器建类型 → hub 列出 → 通用后台建实例
// → 公开归档/详情（动态路由 + 通用模板 + 自定义字段）→ 搜索索引 → 删除类型即停用(公开 404)。
func TestTypeDesignerEndToEnd(t *testing.T) {
	s := newTestPublicServer(t, "")
	lang := s.defaultLang()
	h := s.Handler()

	// 1. 设计器创建自定义类型 recipe（servings 数字 + ingredients repeater）
	typeForm := url.Values{
		"key": {"recipe"}, "name_zh": {"菜谱"}, "name_en": {"Recipes"}, "searchable": {"1"},
		"field_0_key": {"servings"}, "field_0_label_zh": {"份量"}, "field_0_type": {"number"},
		"field_1_key": {"ingredients"}, "field_1_label_zh": {"配料"}, "field_1_type": {"repeater"},
	}
	rt, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/extensions/types", typeForm)
	wt := httptest.NewRecorder()
	h.ServeHTTP(wt, rt)
	if wt.Code != http.StatusSeeOther {
		t.Fatalf("create type status = %d, body = %s", wt.Code, wt.Body.String())
	}
	if row, _ := s.store.GetContentType("recipe"); row == nil {
		t.Fatalf("recipe type not stored")
	}
	if !s.contentTypeActive("recipe") || s.lookupType("recipe") == nil {
		t.Fatalf("recipe not auto-enabled / not in merged registry")
	}

	// 2. hub 列出它（带编辑入口）
	rh, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/extensions", nil)
	wh := httptest.NewRecorder()
	h.ServeHTTP(wh, rh)
	if hb := wh.Body.String(); !strings.Contains(hb, "菜谱") || !strings.Contains(hb, "/admin/extensions/types/recipe/edit") {
		t.Fatalf("hub missing custom type with edit link")
	}

	// 3. 通用后台 CRUD 建一条实例
	inst := url.Values{
		"title": {"番茄炒蛋"}, "slug": {"tomato-egg"}, "status": {"published"},
		"f_servings": {"2"}, "f_ingredients": {"番茄: 2个\n鸡蛋: 3个"},
	}
	ri, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/ext/recipe", inst)
	wi := httptest.NewRecorder()
	h.ServeHTTP(wi, ri)
	if wi.Code != http.StatusSeeOther {
		t.Fatalf("create recipe instance status = %d, body = %s", wi.Code, wi.Body.String())
	}
	if rec, _ := s.store.GetTypedBySlug("recipe", lang, "tomato-egg", true); rec == nil || !strings.Contains(rec.Extra, "番茄") {
		t.Fatalf("recipe instance extra not stored")
	}

	// 4. 公开归档 + 详情（动态路由 + 通用模板 + 自定义字段）
	wa := httptest.NewRecorder()
	h.ServeHTTP(wa, httptest.NewRequest(http.MethodGet, "/"+lang+"/recipe", nil))
	if wa.Code != http.StatusOK || !strings.Contains(wa.Body.String(), "番茄炒蛋") || !strings.Contains(wa.Body.String(), "/recipe/tomato-egg") {
		t.Fatalf("recipe archive failed: %d", wa.Code)
	}
	wd := httptest.NewRecorder()
	h.ServeHTTP(wd, httptest.NewRequest(http.MethodGet, "/"+lang+"/recipe/tomato-egg", nil))
	if wd.Code != http.StatusOK {
		t.Fatalf("recipe detail status = %d, body = %s", wd.Code, wd.Body.String())
	}
	for _, want := range []string{"番茄炒蛋", "份量", "配料", "番茄", "鸡蛋"} {
		if !strings.Contains(wd.Body.String(), want) {
			t.Fatalf("recipe detail missing %q", want)
		}
	}

	// 5. 搜索索引含 recipe
	idx, _ := s.staticSearchIndex(lang)
	foundIdx := false
	for _, e := range idx {
		if e.Type == "recipe" {
			foundIdx = true
		}
	}
	if !foundIdx {
		t.Fatalf("static search index missing recipe")
	}

	// 6. 删除类型 → 停用 → 公开 404（内容本身不删，仅不可达）
	rdel, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/extensions/types/recipe/delete", nil)
	wdel := httptest.NewRecorder()
	h.ServeHTTP(wdel, rdel)
	if wdel.Code != http.StatusSeeOther {
		t.Fatalf("delete type status = %d", wdel.Code)
	}
	if s.contentTypeActive("recipe") {
		t.Fatalf("recipe still active after type delete")
	}
	w404 := httptest.NewRecorder()
	h.ServeHTTP(w404, httptest.NewRequest(http.MethodGet, "/"+lang+"/recipe/tomato-egg", nil))
	if w404.Code != http.StatusNotFound {
		t.Fatalf("recipe detail should 404 after type delete, got %d", w404.Code)
	}
}

// TestAPICustomType 验证自动化 API 也认数据库自定义类型：/types 自省 + 带 fields 创建。
func TestAPICustomType(t *testing.T) {
	s, token := newTestAutomationServer(t, "recipe:read,recipe:write,recipe:publish")
	if err := s.store.SaveContentType(&store.ContentTypeRow{
		Key: "recipe", Name: `{"zh":"菜谱"}`, URLPrefix: "recipe",
		Fields: `[{"key":"servings","type":"number"}]`, Searchable: true,
	}); err != nil {
		t.Fatalf("save type: %v", err)
	}
	if err := s.store.SetSetting(enabledContentTypesKey, "recipe"); err != nil {
		t.Fatalf("enable: %v", err)
	}

	rt := httptest.NewRequest(http.MethodGet, "/api/admin/v1/types", nil)
	rt.Header.Set("Authorization", "Bearer "+token)
	wt := httptest.NewRecorder()
	s.apiContentTypes(wt, rt)
	if wt.Code != http.StatusOK || !strings.Contains(wt.Body.String(), `"key":"recipe"`) || !strings.Contains(wt.Body.String(), `"key":"servings"`) {
		t.Fatalf("/types missing custom type: %s", wt.Body.String())
	}

	body, _ := json.Marshal(map[string]any{"title": "番茄汤", "status": "published", "fields": map[string]any{"servings": 4}})
	rc := httptest.NewRequest(http.MethodPost, "/api/admin/v1/recipe", bytes.NewReader(body))
	rc.SetPathValue("collection", "recipe")
	rc.Header.Set("Authorization", "Bearer "+token)
	wc := httptest.NewRecorder()
	s.apiCreateContent(wc, rc)
	if wc.Code != http.StatusCreated {
		t.Fatalf("api create custom status = %d, body = %s", wc.Code, wc.Body.String())
	}
	var created struct {
		Item apiContentItem `json:"item"`
	}
	if err := json.Unmarshal(wc.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Item.Type != "recipe" || created.Item.Fields["servings"] == nil {
		t.Fatalf("api custom create missing fields: %+v", created.Item)
	}
	if !strings.Contains(created.Item.URL, "/recipe/") {
		t.Fatalf("api url not type-aware: %q", created.Item.URL)
	}
}
