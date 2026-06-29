package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArchiveAllLabelRendersSeparatelyFromTitle(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting("category.all.title", "分类"); err != nil {
		t.Fatalf("set category title: %v", err)
	}
	if err := s.store.SetSetting("category.all.label", "全部"); err != nil {
		t.Fatalf("set category label: %v", err)
	}
	if err := s.store.SetSetting("links.all.title", "链接"); err != nil {
		t.Fatalf("set links title: %v", err)
	}
	if err := s.store.SetSetting("links.all.label", "全部"); err != nil {
		t.Fatalf("set links label: %v", err)
	}

	tests := []struct {
		path       string
		activeWant string
		activeBad  string
		heading    string
	}{
		{path: "/zh/category", activeWant: `<a href="/zh/category" class="active">全部</a>`, activeBad: `<a href="/zh/category" class="active">分类</a>`, heading: "<h1>分类</h1>"},
		{path: "/zh/links", activeWant: `<a href="/zh/links" class="active">全部</a>`, activeBad: `<a href="/zh/links" class="active">链接</a>`, heading: "<h1>链接</h1>"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			if !strings.Contains(body, tc.heading) {
				t.Fatalf("body missing heading %q", tc.heading)
			}
			if !strings.Contains(body, tc.activeWant) {
				t.Fatalf("body missing active all label %q", tc.activeWant)
			}
			if strings.Contains(body, tc.activeBad) {
				t.Fatalf("active all label used title instead: %q", tc.activeBad)
			}
		})
	}
}
