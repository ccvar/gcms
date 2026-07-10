package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func typeAPIReq(s *Server, token, method, path string, body []byte, pathKey string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if pathKey != "" {
		r.SetPathValue("key", pathKey)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	switch {
	case method == http.MethodPost && strings.HasSuffix(path, "/enable"):
		s.apiTypeEnable(w, r)
	case method == http.MethodPost && strings.HasSuffix(path, "/disable"):
		s.apiTypeDisable(w, r)
	case method == http.MethodPost:
		s.apiTypeCreate(w, r)
	case method == http.MethodPut:
		s.apiTypeUpdate(w, r)
	case method == http.MethodDelete:
		s.apiTypeDelete(w, r)
	default:
		s.apiContentTypes(w, r)
	}
	return w
}

// TestAPITypeEnableDisable 启停扩展类型：types:write 放行、无权拒绝、未知/内置类型 404。
func TestAPITypeEnableDisable(t *testing.T) {
	s, token := newTestAutomationServer(t, "types:write")

	// 启用内置扩展 product
	w := typeAPIReq(s, token, http.MethodPost, "/api/admin/v1/types/product/enable", nil, "product")
	if w.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", w.Code, w.Body.String())
	}
	// 自省应出现 product
	w = typeAPIReq(s, token, http.MethodGet, "/api/admin/v1/types", nil, "")
	if !strings.Contains(w.Body.String(), `"key":"product"`) {
		t.Fatalf("enabled type missing from introspection: %s", w.Body.String())
	}
	// ?all=1 带 enabled 标记
	w = typeAPIReq(s, token, http.MethodGet, "/api/admin/v1/types?all=1", nil, "")
	if !strings.Contains(w.Body.String(), `"key":"doc"`) {
		t.Fatalf("all=1 should list disabled types too: %s", w.Body.String())
	}
	// 停用
	w = typeAPIReq(s, token, http.MethodPost, "/api/admin/v1/types/product/disable", nil, "product")
	if w.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", w.Code, w.Body.String())
	}
	w = typeAPIReq(s, token, http.MethodGet, "/api/admin/v1/types", nil, "")
	if strings.Contains(w.Body.String(), `"key":"product"`) {
		t.Fatalf("disabled type still in default introspection: %s", w.Body.String())
	}
	// 内置 posts 不可启停
	w = typeAPIReq(s, token, http.MethodPost, "/api/admin/v1/types/post/enable", nil, "post")
	if w.Code != http.StatusNotFound {
		t.Fatalf("builtin post enable should 404, got %d", w.Code)
	}

	// 无 types:write 的密钥被拒
	s2, token2 := newTestAutomationServer(t, "posts:read")
	w = typeAPIReq(s2, token2, http.MethodPost, "/api/admin/v1/types/product/enable", nil, "product")
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing scope should 403, got %d %s", w.Code, w.Body.String())
	}
}

// TestAPITypeCreateContentDelete 全链路：AI 创建自定义类型 → 建内容（带 fields）→
// 有内容时删除被拒 → 删内容后可删；内置类型不可改删；坏输入被校验。
func TestAPITypeCreateContentDelete(t *testing.T) {
	s, token := newTestAutomationServer(t, "types:write,cases:read,cases:write,cases:publish")

	// 创建「案例」类型
	def, _ := json.Marshal(map[string]any{
		"key": "cases", "name": "案例", "name_en": "Cases",
		"fields": []map[string]any{
			{"key": "client", "label": "客户", "type": "text", "required": true},
			{"label_en": "Industry", "type": "select", "options": []string{"saas", "retail"}},
			{"key": "shots", "label": "截图", "type": "gallery"},
		},
	})
	w := typeAPIReq(s, token, http.MethodPost, "/api/admin/v1/types", def, "")
	if w.Code != http.StatusCreated {
		t.Fatalf("type create: %d %s", w.Code, w.Body.String())
	}
	// 自省能看到新类型与字段
	w = typeAPIReq(s, token, http.MethodGet, "/api/admin/v1/types", nil, "")
	tb := w.Body.String()
	if !strings.Contains(tb, `"key":"cases"`) || !strings.Contains(tb, `"key":"client"`) || !strings.Contains(tb, `"key":"industry"`) {
		t.Fatalf("introspection missing new type schema: %s", tb)
	}

	// 用新集合建内容（带自定义字段）
	body, _ := json.Marshal(map[string]any{
		"title": "某客户官网改版", "status": "published",
		"fields": map[string]any{"client": "ACME", "industry": "saas"},
	})
	rc := httptest.NewRequest(http.MethodPost, "/api/admin/v1/cases", bytes.NewReader(body))
	rc.SetPathValue("collection", "cases")
	rc.Header.Set("Authorization", "Bearer "+token)
	wc := httptest.NewRecorder()
	s.apiCreateContent(wc, rc)
	if wc.Code != http.StatusCreated {
		t.Fatalf("content create in custom type: %d %s", wc.Code, wc.Body.String())
	}

	// 有内容 → 删类型必须被拒（409）
	w = typeAPIReq(s, token, http.MethodDelete, "/api/admin/v1/types/cases", nil, "cases")
	if w.Code != http.StatusConflict {
		t.Fatalf("delete with content should 409, got %d %s", w.Code, w.Body.String())
	}

	// 清掉内容后可删
	var created struct {
		Item struct {
			ID int64 `json:"id"`
		} `json:"item"`
	}
	_ = json.Unmarshal(wc.Body.Bytes(), &created)
	if created.Item.ID == 0 {
		t.Fatalf("no id in create response: %s", wc.Body.String())
	}
	if err := s.store.DeletePost(created.Item.ID); err != nil {
		t.Fatalf("delete content: %v", err)
	}
	w = typeAPIReq(s, token, http.MethodDelete, "/api/admin/v1/types/cases", nil, "cases")
	if w.Code != http.StatusOK {
		t.Fatalf("delete after cleanup: %d %s", w.Code, w.Body.String())
	}

	// 内置扩展不可修改/删除
	w = typeAPIReq(s, token, http.MethodPut, "/api/admin/v1/types/product", []byte(`{"name":"x"}`), "product")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("builtin update should 400, got %d", w.Code)
	}
	w = typeAPIReq(s, token, http.MethodDelete, "/api/admin/v1/types/product", nil, "product")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("builtin delete should 400, got %d", w.Code)
	}

	// 校验：保留字 key、坏字段类型
	bad, _ := json.Marshal(map[string]any{"key": "admin", "name": "x", "fields": []map[string]any{{"key": "a", "type": "text"}}})
	w = typeAPIReq(s, token, http.MethodPost, "/api/admin/v1/types", bad, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("reserved key should 400, got %d", w.Code)
	}
	bad2, _ := json.Marshal(map[string]any{"key": "widgets", "name": "x", "fields": []map[string]any{{"key": "a", "type": "nope"}}})
	w = typeAPIReq(s, token, http.MethodPost, "/api/admin/v1/types", bad2, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad field type should 400, got %d", w.Code)
	}
}
