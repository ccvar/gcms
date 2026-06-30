package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

// TestExtTypeEnabledServesArchiveAndDetail 验证启用某扩展类型后，归档页与详情页
// 经完整 HTTP 路径（含语种前缀）正确渲染，且详情页显示自定义字段。
func TestExtTypeEnabledServesArchiveAndDetail(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product: %v", err)
	}
	if _, err := s.store.CreatePost(&store.Post{
		Type: "product", Lang: "zh", Slug: "ext-prod-1", Title: "演示商品",
		Excerpt: "一句话简介", Status: "published",
		Extra:   `{"price":199,"gallery":["/u/a.webp","/u/b.webp"]}`,
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}

	h := s.Handler()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/zh/products", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("archive status = %d, body = %s", w.Code, w.Body.String())
	}
	if b := w.Body.String(); !strings.Contains(b, "演示商品") || !strings.Contains(b, "/products/ext-prod-1") {
		t.Fatalf("archive missing product card")
	}

	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/zh/products/ext-prod-1", nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body = %s", w2.Code, w2.Body.String())
	}
	body := w2.Body.String()
	for _, want := range []string{"演示商品", "价格", "199", "/u/a.webp", "/u/b.webp"} {
		if !strings.Contains(body, want) {
			t.Fatalf("detail missing %q", want)
		}
	}
}

// TestExtTypeDisabledFallsBackToPage 验证多站点安全保证：未启用某类型时，
// 同名前缀的「页面」仍照常渲染（列表路由回退），详情路由 404（与历史一致）。
func TestExtTypeDisabledFallsBackToPage(t *testing.T) {
	s := newTestPublicServer(t, "")
	// gallery 类型默认关闭；建一个 slug=gallery 的页面，访问 /gallery 应渲染该页面。
	if _, err := s.store.CreatePost(&store.Post{
		Type: "page", Lang: "zh", Slug: "gallery", Title: "画廊介绍页", Content: "页面正文", Status: "published",
	}); err != nil {
		t.Fatalf("create page: %v", err)
	}
	h := s.Handler()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/zh/gallery", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("page fallback status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "画廊介绍页") {
		t.Fatalf("disabled-type prefix did not fall back to the page")
	}

	// 未启用类型的详情路由 404。
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/zh/products/whatever", nil))
	if w2.Code != http.StatusNotFound {
		t.Fatalf("disabled detail status = %d, want 404", w2.Code)
	}
}
