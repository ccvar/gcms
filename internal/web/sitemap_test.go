package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/store"
)

func TestArticleTrailingSlashCanonicalAndSitemapLastMod(t *testing.T) {
	s := newTestPublicServer(t, "")
	_, err := s.store.CreatePost(&store.Post{
		Type:        "post",
		Lang:        "zh",
		Slug:        "canonical-trailing-slash",
		Title:       "Canonical Trailing Slash",
		Excerpt:     "测试 canonical 尾斜杠。",
		Content:     "正文",
		Status:      "published",
		PublishedAt: time.Date(2026, 6, 30, 8, 0, 0, 0, time.UTC),
		TransGroup:  "test:canonical-trailing-slash",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}

	h := s.Handler()
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/zh/posts/canonical-trailing-slash/", nil))
	if page.Code != http.StatusOK {
		t.Fatalf("article status = %d, body = %s", page.Code, page.Body.String())
	}
	if !strings.Contains(page.Body.String(), `rel="canonical" href="https://example.test/zh/posts/canonical-trailing-slash/"`) {
		t.Fatalf("article page missing trailing-slash canonical")
	}

	sm := httptest.NewRecorder()
	h.ServeHTTP(sm, httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil))
	if sm.Code != http.StatusOK {
		t.Fatalf("sitemap status = %d, body = %s", sm.Code, sm.Body.String())
	}
	body := sm.Body.String()
	if !strings.Contains(body, "<loc>https://example.test/zh/posts/canonical-trailing-slash/</loc>") {
		t.Fatalf("sitemap missing trailing-slash post URL")
	}
	if !strings.Contains(body, "<lastmod>") {
		t.Fatalf("sitemap missing lastmod entries")
	}
}
