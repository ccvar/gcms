package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cms.ccvar.com/internal/store"
)

func newTestPublicServer(t *testing.T, uploadDir string) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv, err := New(st, "https://example.test", uploadDir, os.DirFS("../.."), os.DirFS("../.."))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv
}

func TestPublicPageCacheAddsETagAndRevalidates(t *testing.T) {
	s := newTestPublicServer(t, "")
	h := s.Handler()

	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/zh/", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
	}
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("public response missing ETag")
	}
	if got, want := first.Header().Get("Cache-Control"), publicPageCacheControl; got != want {
		t.Fatalf("cache-control = %q, want %q", got, want)
	}

	secondReq := httptest.NewRequest(http.MethodGet, "/zh/", nil)
	secondReq.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	h.ServeHTTP(second, secondReq)
	if second.Code != http.StatusNotModified {
		t.Fatalf("second status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 response should be empty, got %q", second.Body.String())
	}

	s.cacheMu.RLock()
	cached := len(s.pages)
	s.cacheMu.RUnlock()
	if cached == 0 {
		t.Fatalf("public page cache should contain the rendered page")
	}
	s.clearGeneratedCaches()
	s.cacheMu.RLock()
	cached = len(s.pages)
	s.cacheMu.RUnlock()
	if cached != 0 {
		t.Fatalf("clearGeneratedCaches should clear public pages, got %d", cached)
	}
}

func TestUploadHeadersUseImmutableCacheAndSandboxCSP(t *testing.T) {
	uploadDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(uploadDir, "avatar.webp"), testWebPBytes(), 0o644); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	s := newTestPublicServer(t, uploadDir)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/uploads/avatar.webp", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if got, want := w.Header().Get("Cache-Control"), uploadCacheControl; got != want {
		t.Fatalf("upload cache-control = %q, want %q", got, want)
	}
	if got := w.Header().Get("Content-Security-Policy"); got == "" {
		t.Fatalf("upload response missing CSP")
	}
	if got := w.Header().Get("Content-Security-Policy-Report-Only"); got != "" {
		t.Fatalf("upload response should not use report-only CSP, got %q", got)
	}
}
