package web

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	return &Server{store: st, i18n: i18n.New(), baseURL: "http://localhost:8080", content: map[string]contentCacheEntry{}}, token
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

func TestAutomationOpenAPIIncludesMediaUpload(t *testing.T) {
	spec := automationOpenAPISpec("https://example.com/api/admin/v1")
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths missing or wrong type: %#v", spec["paths"])
	}
	media, ok := paths["/media"].(map[string]any)
	if !ok {
		t.Fatalf("/media path missing: %#v", paths)
	}
	post, ok := media["post"].(map[string]any)
	if !ok {
		t.Fatalf("POST /media missing: %#v", media)
	}
	if post["operationId"] != "uploadMedia" {
		t.Fatalf("operationId = %#v, want uploadMedia", post["operationId"])
	}
	requestBody, ok := post["requestBody"].(map[string]any)
	if !ok {
		t.Fatalf("requestBody missing: %#v", post["requestBody"])
	}
	content, ok := requestBody["content"].(map[string]any)
	if !ok {
		t.Fatalf("requestBody.content missing: %#v", requestBody)
	}
	if _, ok := content["multipart/form-data"]; !ok {
		t.Fatalf("multipart/form-data request body missing: %#v", content)
	}
	components, ok := spec["components"].(map[string]any)
	if !ok {
		t.Fatalf("components missing: %#v", spec["components"])
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatalf("schemas missing: %#v", components["schemas"])
	}
	if _, ok := schemas["MediaUploadResponse"]; !ok {
		t.Fatalf("MediaUploadResponse schema missing: %#v", schemas)
	}
	for _, path := range []string{"/posts/{id}/preview", "/links/{id}/preview"} {
		entry, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("%s path missing: %#v", path, paths)
		}
		get, ok := entry["get"].(map[string]any)
		if !ok {
			t.Fatalf("GET %s missing: %#v", path, entry)
		}
		if get["responses"] == nil {
			t.Fatalf("GET %s responses missing: %#v", path, get)
		}
	}
	if _, ok := schemas["ContentPreviewResponse"]; !ok {
		t.Fatalf("ContentPreviewResponse schema missing: %#v", schemas)
	}
	if _, ok := schemas["ContentPreview"]; !ok {
		t.Fatalf("ContentPreview schema missing: %#v", schemas)
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

func TestAPIUploadMedia(t *testing.T) {
	s, token := newTestAutomationServer(t, "media:write")
	s.uploadDir = t.TempDir()

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	part, err := mw.CreateFormFile("file", "cover.webp")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.WriteString(part, "WEBP test image bytes"); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/admin/v1/media", body)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()

	s.apiUploadMedia(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got struct {
		URL  string `json:"url"`
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.HasPrefix(got.URL, "/uploads/") || !strings.HasSuffix(got.Name, ".webp") {
		t.Fatalf("unexpected upload response: %#v", got)
	}
	if got.Size == 0 {
		t.Fatalf("size = 0, want written bytes")
	}
	if _, err := os.Stat(filepath.Join(s.uploadDir, got.Name)); err != nil {
		t.Fatalf("uploaded file missing: %v", err)
	}
}

func TestAPIUploadMediaRequiresScope(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:write")
	s.uploadDir = t.TempDir()

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	part, err := mw.CreateFormFile("file", "cover.webp")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.WriteString(part, "WEBP test image bytes"); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/admin/v1/media", body)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()

	s.apiUploadMedia(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestAPICreateAndUpdatePageCoverImage(t *testing.T) {
	s, token := newTestAutomationServer(t, "pages:read,pages:write")
	cover := "/uploads/api-page-cover.webp"
	body, err := json.Marshal(map[string]any{
		"title":       "API Page Cover Test",
		"lang":        "zh",
		"status":      "draft",
		"cover_image": cover,
		"content":     "Page body with inline image: ![cover](" + cover + ")",
	})
	if err != nil {
		t.Fatalf("marshal create body: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/admin/v1/pages", bytes.NewReader(body))
	r.SetPathValue("collection", "pages")
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.apiCreateContent(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", w.Code, w.Body.String())
	}
	var created struct {
		Item apiContentItem `json:"item"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Item.CoverImage != cover {
		t.Fatalf("created cover_image = %q, want %q", created.Item.CoverImage, cover)
	}
	if !strings.Contains(created.Item.Content, cover) {
		t.Fatalf("created content does not keep inline image URL: %q", created.Item.Content)
	}

	nextCover := "/uploads/api-page-cover-next.webp"
	patchBody, err := json.Marshal(map[string]any{"cover_image": nextCover})
	if err != nil {
		t.Fatalf("marshal patch body: %v", err)
	}
	r = httptest.NewRequest(http.MethodPatch, "/api/admin/v1/pages/1", bytes.NewReader(patchBody))
	r.SetPathValue("collection", "pages")
	r.SetPathValue("id", strconv.FormatInt(created.Item.ID, 10))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()

	s.apiUpdateContent(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", w.Code, w.Body.String())
	}
	var updated struct {
		Item apiContentItem `json:"item"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update response: %v", err)
	}
	if updated.Item.CoverImage != nextCover {
		t.Fatalf("updated cover_image = %q, want %q", updated.Item.CoverImage, nextCover)
	}
}

func TestAPICreatePostUsesConfiguredDefaultAuthor(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:write")
	if err := s.store.SetSetting("content.post_author::en", "Docs Team"); err != nil {
		t.Fatalf("set default author: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"title":  "Default Author Draft",
		"lang":   "en",
		"status": "draft",
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/admin/v1/posts", bytes.NewReader(body))
	r.SetPathValue("collection", "posts")
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.apiCreateContent(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got struct {
		Item apiContentItem `json:"item"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Item.Author != "Docs Team" {
		t.Fatalf("author = %q, want Docs Team", got.Item.Author)
	}
}

func TestAPIGetPostUsesDefaultAuthorForBlankAuthor(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:read")
	if err := s.store.SetSetting("content.post_author", "Ops Team"); err != nil {
		t.Fatalf("set default author: %v", err)
	}
	id, err := s.store.CreatePost(&store.Post{
		Type:   "post",
		Lang:   "zh",
		Slug:   "blank-author",
		Title:  "Blank Author",
		Status: "draft",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/admin/v1/posts/"+strconv.FormatInt(id, 10), nil)
	r.SetPathValue("collection", "posts")
	r.SetPathValue("id", strconv.FormatInt(id, 10))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	s.apiGetContent(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var got struct {
		Item apiContentItem `json:"item"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Item.Author != "Ops Team" {
		t.Fatalf("author = %q, want Ops Team", got.Item.Author)
	}
}

func TestAPIPreviewPostAndLinkDrafts(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:read,links:read")
	postID, err := s.store.CreatePost(&store.Post{
		Type:       "post",
		Lang:       "zh",
		Slug:       "api-preview-post",
		Title:      "API Preview Post",
		Content:    "Intro\n\n## Section\n\nBody",
		Status:     "draft",
		EditorMode: "markdown",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	linkID, err := s.store.CreatePost(&store.Post{
		Type:       "link",
		Lang:       "zh",
		Slug:       "api-preview-link",
		Title:      "API Preview Link",
		Content:    "Link intro\n\n## Link Section\n\nBody",
		Status:     "draft",
		EditorMode: "markdown",
		LinkURL:    "https://example.com",
	})
	if err != nil {
		t.Fatalf("create link: %v", err)
	}

	for _, tc := range []struct {
		collection string
		id         int64
		wantURL    string
		wantHTML   string
	}{
		{"posts", postID, "http://example.com/zh/posts/api-preview-post", "<h2 id=\"section\">Section</h2>"},
		{"links", linkID, "http://example.com/zh/links/api-preview-link", "<h2 id=\"link-section\">Link Section</h2>"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/admin/v1/"+tc.collection+"/"+strconv.FormatInt(tc.id, 10)+"/preview", nil)
		r.SetPathValue("collection", tc.collection)
		r.SetPathValue("id", strconv.FormatInt(tc.id, 10))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()

		s.apiPreviewContent(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s preview status = %d, body = %s", tc.collection, w.Code, w.Body.String())
		}
		var got struct {
			Preview struct {
				Item struct {
					ID      int64  `json:"id"`
					Status  string `json:"status"`
					Content string `json:"content"`
				} `json:"item"`
				PreviewURL  string `json:"preview_url"`
				PublicURL   string `json:"public_url"`
				ContentHTML string `json:"content_html"`
				TOC         []struct {
					Level int    `json:"level"`
					ID    string `json:"id"`
					Text  string `json:"text"`
				} `json:"toc"`
				Robots string `json:"robots"`
			} `json:"preview"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode %s preview: %v", tc.collection, err)
		}
		if got.Preview.Item.ID != tc.id || got.Preview.Item.Status != "draft" || got.Preview.Item.Content == "" {
			t.Fatalf("%s preview item mismatch: %#v", tc.collection, got.Preview.Item)
		}
		if got.Preview.PublicURL != tc.wantURL {
			t.Fatalf("%s public_url = %q, want %q", tc.collection, got.Preview.PublicURL, tc.wantURL)
		}
		if !strings.HasSuffix(got.Preview.PreviewURL, "/api/admin/v1/"+tc.collection+"/"+strconv.FormatInt(tc.id, 10)+"/preview") {
			t.Fatalf("%s preview_url = %q", tc.collection, got.Preview.PreviewURL)
		}
		if !strings.Contains(got.Preview.ContentHTML, tc.wantHTML) {
			t.Fatalf("%s content_html = %q, want contains %q", tc.collection, got.Preview.ContentHTML, tc.wantHTML)
		}
		if len(got.Preview.TOC) == 0 {
			t.Fatalf("%s preview toc empty", tc.collection)
		}
		if got.Preview.Robots != "noindex, nofollow" {
			t.Fatalf("%s robots = %q", tc.collection, got.Preview.Robots)
		}
	}
}

func TestAPIPreviewRequiresReadScope(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:write")
	id, err := s.store.CreatePost(&store.Post{
		Type:       "post",
		Lang:       "zh",
		Slug:       "api-preview-no-read",
		Title:      "API Preview No Read",
		Content:    "Body",
		Status:     "draft",
		EditorMode: "markdown",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/admin/v1/posts/"+strconv.FormatInt(id, 10)+"/preview", nil)
	r.SetPathValue("collection", "posts")
	r.SetPathValue("id", strconv.FormatInt(id, 10))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	s.apiPreviewContent(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
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
