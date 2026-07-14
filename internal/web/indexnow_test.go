package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"cms.ccvar.com/internal/store"
)

// TestGenerateIndexNowKey key 必须是 32 位小写 hex，且每次生成都不同。
func TestGenerateIndexNowKey(t *testing.T) {
	hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)
	a, err := generateIndexNowKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !hex32.MatchString(a) {
		t.Fatalf("key %q 不是 32 位小写 hex", a)
	}
	b, err := generateIndexNowKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if a == b {
		t.Fatalf("两次生成的 key 相同：%q", a)
	}
}

// TestIndexNowKeyPersistence 首次读取生成并落 settings；再次读取拿同一把。
func TestIndexNowKeyPersistence(t *testing.T) {
	s, _ := newTestAutomationServer(t, "posts:read")
	first, err := s.indexNowKey()
	if err != nil {
		t.Fatalf("first key: %v", err)
	}
	if got := s.store.Setting(indexNowKeySetting); got != first {
		t.Fatalf("settings 存的 key = %q, want %q", got, first)
	}
	second, err := s.indexNowKey()
	if err != nil {
		t.Fatalf("second key: %v", err)
	}
	if second != first {
		t.Fatalf("key 不稳定：%q != %q", second, first)
	}
}

// TestBuildIndexNowURL URL 组装：url/key 参数齐全且做了转义。
func TestBuildIndexNowURL(t *testing.T) {
	got := buildIndexNowURL("https://api.indexnow.org/indexnow", "https://example.com/zh/posts/a-b/?x=1&y=2", "abc123")
	want := "https://api.indexnow.org/indexnow?key=abc123&url=https%3A%2F%2Fexample.com%2Fzh%2Fposts%2Fa-b%2F%3Fx%3D1%26y%3D2"
	if got != want {
		t.Fatalf("buildIndexNowURL = %q, want %q", got, want)
	}
}

// TestServeIndexNowKeyFile GET /{key}.txt 原样返回 key；其它 .txt 不接。
func TestServeIndexNowKeyFile(t *testing.T) {
	s, _ := newTestAutomationServer(t, "posts:read")
	key, err := s.indexNowKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/"+key+".txt", nil)
	w := httptest.NewRecorder()
	if !s.serveIndexNowKeyFile(w, r) {
		t.Fatalf("key 文件未被接住")
	}
	if w.Body.String() != key {
		t.Fatalf("body = %q, want %q", w.Body.String(), key)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}

	for _, path := range []string{"/robots.txt", "/00000000000000000000000000000000.txt", "/" + key, "/zh/" + key + ".txt"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if s.serveIndexNowKeyFile(httptest.NewRecorder(), r) {
			t.Fatalf("%s 不应命中 key 文件", path)
		}
	}
}

// TestFirePublishHooksInvalidatesSitemap 发布钩子必须立即失效 sitemap 端点缓存；
// 草稿与非 post/page/link 类型不触发。本地 baseURL 下不做 IndexNow 网络提交（测试不打真网）。
func TestFirePublishHooksInvalidatesSitemap(t *testing.T) {
	s, _ := newTestAutomationServer(t, "posts:read")
	s.endpoints = map[string]endpointCacheEntry{}
	seed := func() {
		s.setCachedEndpoint("sitemap:http://localhost:8080", "application/xml", []byte("<x/>"), time.Minute)
		s.setCachedEndpoint("rss:http://localhost:8080", "application/xml", []byte("<x/>"), time.Minute)
	}

	seed()
	s.firePublishHooks(nil, &store.Post{Type: "post", Status: "published", Lang: "zh", Slug: "a"})
	if _, _, ok := s.cachedEndpoint("sitemap:http://localhost:8080"); ok {
		t.Fatalf("发布后 sitemap 缓存仍在")
	}
	if _, _, ok := s.cachedEndpoint("rss:http://localhost:8080"); !ok {
		t.Fatalf("发布钩子不该动 sitemap 之外的端点缓存")
	}

	seed()
	s.firePublishHooks(nil, &store.Post{Type: "post", Status: "draft", Lang: "zh", Slug: "a"})
	if _, _, ok := s.cachedEndpoint("sitemap:http://localhost:8080"); !ok {
		t.Fatalf("草稿不该触发发布钩子")
	}
	s.firePublishHooks(nil, &store.Post{Type: "product", Status: "published", Lang: "zh", Slug: "a"})
	if _, _, ok := s.cachedEndpoint("sitemap:http://localhost:8080"); !ok {
		t.Fatalf("非 post/page/link 不该触发发布钩子")
	}
}
