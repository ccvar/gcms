package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAPIExtTypeFieldsAndIntrospection 验证：(1) /types 自省返回已启用扩展类型的字段 schema；
// (2) 通过内容 API 创建扩展类型实例时，fields 写入 extra 且响应回带 fields；(3) 详情 URL type 化。
func TestAPIExtTypeFieldsAndIntrospection(t *testing.T) {
	s, token := newTestAutomationServer(t, "products:read,products:write,products:publish")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product: %v", err)
	}

	// /types 自省
	rt := httptest.NewRequest(http.MethodGet, "/api/admin/v1/types", nil)
	rt.Header.Set("Authorization", "Bearer "+token)
	wt := httptest.NewRecorder()
	s.apiContentTypes(wt, rt)
	if wt.Code != http.StatusOK {
		t.Fatalf("types status = %d, body = %s", wt.Code, wt.Body.String())
	}
	tb := wt.Body.String()
	if !strings.Contains(tb, `"key":"product"`) || !strings.Contains(tb, `"key":"price"`) || !strings.Contains(tb, `"key":"gallery"`) {
		t.Fatalf("types introspection missing product schema: %s", tb)
	}

	// 创建带自定义字段的商品
	body, _ := json.Marshal(map[string]any{
		"title":  "API 商品",
		"status": "published",
		"fields": map[string]any{"price": 299, "gallery": []string{"/u/a.webp", "/u/b.webp"}},
	})
	rc := httptest.NewRequest(http.MethodPost, "/api/admin/v1/products", bytes.NewReader(body))
	rc.SetPathValue("collection", "products")
	rc.Header.Set("Authorization", "Bearer "+token)
	wc := httptest.NewRecorder()
	s.apiCreateContent(wc, rc)
	if wc.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", wc.Code, wc.Body.String())
	}
	var created struct {
		Item apiContentItem `json:"item"`
	}
	if err := json.Unmarshal(wc.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Item.Type != "product" {
		t.Fatalf("created type = %q, want product", created.Item.Type)
	}
	if created.Item.Fields == nil || created.Item.Fields["price"] == nil {
		t.Fatalf("response missing fields.price: %+v", created.Item.Fields)
	}
	if !strings.Contains(created.Item.URL, "/products/") {
		t.Fatalf("response url not type-aware: %q", created.Item.URL)
	}

	// extra 实际落库
	p, _ := s.store.GetPostByID(created.Item.ID)
	if p == nil || !strings.Contains(p.Extra, "299") || !strings.Contains(p.Extra, "/u/a.webp") {
		t.Fatalf("extra not persisted: %v", p)
	}
}
