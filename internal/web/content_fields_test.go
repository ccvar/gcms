package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

// TestRepeaterRendersOnDetail 验证 repeater 字段（如商品规格）在详情页以键值表渲染。
func TestRepeaterRendersOnDetail(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product: %v", err)
	}
	if _, err := s.store.CreatePost(&store.Post{
		Type: "product", Lang: "zh", Slug: "spec-prod", Title: "规格商品", Status: "published",
		Extra: `{"specs":[{"k":"重量","v":"1.2kg"},{"k":"颜色","v":"黑"}]}`,
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}

	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/zh/products/spec-prod", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"规格参数", "重量", "1.2kg", "颜色", "黑"} {
		if !strings.Contains(body, want) {
			t.Fatalf("detail missing repeater value %q", want)
		}
	}
}
