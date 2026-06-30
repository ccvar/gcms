package web

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

// TestGenericTemplatesRegistered 确认通用兜底模板已在 NewRenderer 注册。
func TestGenericTemplatesRegistered(t *testing.T) {
	s := newTestPublicServer(t, "")
	for _, name := range []string{"generic_list", "generic_detail"} {
		if _, ok := s.rnd.sets[name]; !ok {
			t.Fatalf("template %q not registered", name)
		}
	}
}

// TestGenericDetailRenders 用真实 View 执行 generic_detail，验证标题、自定义字段
// （标量 / 画廊 / URL）与面包屑里的类型名都正确渲染。
func TestGenericDetailRenders(t *testing.T) {
	s := newTestPublicServer(t, "")
	req := httptest.NewRequest(http.MethodGet, "/zh/products/demo", nil)

	v := s.viewForLang(req, "zh", "")
	p := &store.Post{Type: "product", Slug: "demo", Title: "演示商品", Excerpt: "一句话简介", Lang: "zh", Status: "published"}
	v.Post = p
	v.SEO = v.Site.Article(p)
	v.ContentHTML = template.HTML("<p>正文内容</p>")
	v.CT = contentTypeByKey("product")
	v.Fields = []FieldValue{
		{Key: "price", Label: "价格", Type: "number", Text: "199"},
		{Key: "gallery", Label: "图集", Type: "gallery", List: []string{"/u/a.webp", "/u/b.webp"}},
		{Key: "signup_url", Label: "链接", Type: "url", Text: "https://example.com"},
	}

	w := httptest.NewRecorder()
	s.rnd.Public(w, "generic_detail", http.StatusOK, v)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"演示商品", "商品", "价格", "199", "/u/a.webp", "/u/b.webp", "https://example.com", "正文内容"} {
		if !strings.Contains(body, want) {
			t.Fatalf("generic_detail body missing %q", want)
		}
	}
}

// TestGenericListRenders 用真实 View 执行 generic_list，验证类型名标题与卡片链接。
func TestGenericListRenders(t *testing.T) {
	s := newTestPublicServer(t, "")
	req := httptest.NewRequest(http.MethodGet, "/zh/products", nil)

	v := s.viewForLang(req, "zh", "")
	v.CT = contentTypeByKey("product")
	v.Posts = []*store.Post{
		{Type: "product", Slug: "demo", Title: "演示商品", Excerpt: "简介", Lang: "zh", Status: "published"},
	}

	w := httptest.NewRecorder()
	s.rnd.Public(w, "generic_list", http.StatusOK, v)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"商品", "演示商品", "/products/demo"} {
		if !strings.Contains(body, want) {
			t.Fatalf("generic_list body missing %q", want)
		}
	}
}
