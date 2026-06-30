package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

// TestSearchIncludesEnabledExtTypes 验证启用的扩展类型同时进入动态 SSR 搜索（结果链接 type 化）
// 与静态 search-index.json，且未启用时不进入。
func TestSearchIncludesEnabledExtTypes(t *testing.T) {
	s := newTestPublicServer(t, "")
	lang := s.defaultLang()
	if _, err := s.store.CreatePost(&store.Post{
		Type: "product", Lang: lang, Slug: "srch-prod", Title: "独特商品名片",
		Excerpt: "用于搜索测试", Status: "published", Extra: `{"price":1}`,
	}); err != nil {
		t.Fatalf("create product: %v", err)
	}

	// 未启用：动态搜索与静态索引都不含该商品。
	if hits, _ := s.store.SearchInTypes(lang, "独特商品", s.searchableTypes(), 50); hasPostSlug(hits, "srch-prod") {
		t.Fatalf("disabled product should not appear in search")
	}

	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product: %v", err)
	}

	// 动态 SSR 搜索（完整 HTTP 路径），结果链接应指向 /products/...
	h := s.Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/"+lang+"/search?q=独特商品", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("search status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "独特商品名片") || !strings.Contains(body, "/products/srch-prod") {
		t.Fatalf("dynamic search missing product result or used wrong URL")
	}

	// 静态搜索索引应含 product 条目，URL 正确。
	idx, err := s.staticSearchIndex(lang)
	if err != nil {
		t.Fatalf("static index: %v", err)
	}
	found := false
	for _, e := range idx {
		if e.Type == "product" && strings.Contains(e.URL, "/products/srch-prod") {
			found = true
		}
	}
	if !found {
		t.Fatalf("static search index missing product entry")
	}
}

func hasPostSlug(posts []*store.Post, slug string) bool {
	for _, p := range posts {
		if p.Slug == slug {
			return true
		}
	}
	return false
}
