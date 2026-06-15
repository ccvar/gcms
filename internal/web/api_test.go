package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"cms.ccvar.com/internal/i18n"
	"cms.ccvar.com/internal/store"
)

func newTestAutomationServer(t *testing.T, scopes string) (*Server, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	token, prefix := newAutomationToken()
	if _, err := st.CreateAutomationKey("test", token, prefix, scopes); err != nil {
		t.Fatalf("create automation key: %v", err)
	}
	return &Server{store: st, i18n: i18n.New(), baseURL: "http://localhost:8080"}, token
}

func TestAPILanguages(t *testing.T) {
	s, token := newTestAutomationServer(t, "languages:read")
	r := httptest.NewRequest(http.MethodGet, "/api/admin/v1/languages", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	s.apiLanguages(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got struct {
		Default string `json:"default"`
		Items   []struct {
			Code    string `json:"code"`
			Default bool   `json:"default"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Default != "zh" {
		t.Fatalf("default = %q, want zh", got.Default)
	}
	if len(got.Items) < 2 {
		t.Fatalf("expected seeded zh/en languages, got %#v", got.Items)
	}
}

func TestAPIListPostCategories(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:categories")
	id, err := s.store.CreateCategory(&store.Category{
		Slug:        "api-test-category",
		Name:        "API Test Category",
		Description: "Category exposed to automation clients.",
		Lang:        "zh",
		Kind:        "post",
	})
	if err != nil {
		t.Fatalf("create category: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/admin/v1/posts/categories?lang=zh", nil)
	r.SetPathValue("collection", "posts")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	s.apiListCategories(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got struct {
		Lang  string `json:"lang"`
		Kind  string `json:"kind"`
		Items []struct {
			ID   int64  `json:"id"`
			Slug string `json:"slug"`
			Name string `json:"name"`
			Lang string `json:"lang"`
			Kind string `json:"kind"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Lang != "zh" || got.Kind != "post" {
		t.Fatalf("lang/kind = %q/%q, want zh/post", got.Lang, got.Kind)
	}
	for _, item := range got.Items {
		if item.ID == id {
			if item.Slug != "api-test-category" || item.Name != "API Test Category" || item.Lang != "zh" || item.Kind != "post" {
				t.Fatalf("category item mismatch: %#v", item)
			}
			return
		}
	}
	t.Fatalf("created category %d not found in response: %#v", id, got.Items)
}

func TestAPIListContentByTransGroupAcrossLanguages(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:read")
	group := "test-group-api-translations"
	for _, lang := range []string{"zh", "en"} {
		if _, err := s.store.CreatePost(&store.Post{
			Type:       "post",
			Lang:       lang,
			TransGroup: group,
			Slug:       "api-trans-" + lang,
			Title:      "API Trans " + lang,
			Status:     "draft",
			EditorMode: "markdown",
		}); err != nil {
			t.Fatalf("create %s post: %v", lang, err)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/admin/v1/posts?trans_group="+group, nil)
	r.SetPathValue("collection", "posts")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	s.apiListContent(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got struct {
		Lang  string `json:"lang"`
		Items []struct {
			Lang       string `json:"lang"`
			TransGroup string `json:"trans_group"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Lang != "all" {
		t.Fatalf("lang = %q, want all", got.Lang)
	}
	seen := map[string]bool{}
	for _, item := range got.Items {
		if item.TransGroup == group {
			seen[item.Lang] = true
		}
	}
	if !seen["zh"] || !seen["en"] {
		t.Fatalf("expected zh/en items for %q, got %#v", group, got.Items)
	}
}
