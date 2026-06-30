package web

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func testWebPBytes() []byte {
	return []byte("RIFF\x18\x00\x00\x00WEBPVP8 \x00\x00\x00\x00gcms-test")
}

func TestAssetCacheControlUsesLongCacheForVersionedAssets(t *testing.T) {
	s := &Server{assetVer: "asset123"}
	versioned := httptest.NewRequest(http.MethodGet, "/assets/css/style.css?v=asset123", nil)
	if got, want := s.assetCacheControl(versioned), "public, max-age=31536000, immutable"; got != want {
		t.Fatalf("versioned cache control = %q, want %q", got, want)
	}
	unversioned := httptest.NewRequest(http.MethodGet, "/assets/js/toc.js", nil)
	if got, want := s.assetCacheControl(unversioned), "public, max-age=86400"; got != want {
		t.Fatalf("unversioned cache control = %q, want %q", got, want)
	}
	staleVersion := httptest.NewRequest(http.MethodGet, "/assets/js/admin.js?v=old", nil)
	if got, want := s.assetCacheControl(staleVersion), "public, max-age=86400"; got != want {
		t.Fatalf("stale version cache control = %q, want %q", got, want)
	}
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

func TestAPICreateCustomLanguage(t *testing.T) {
	s, token := newTestAutomationServer(t, "languages:read,languages:write")
	body := bytes.NewBufferString(`{"code":"pt","name":"Português","tag":"pt-BR","enable":true,"default":true}`)
	r := httptest.NewRequest(http.MethodPost, "/api/admin/v1/languages", body)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	s.apiCreateLanguage(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var created struct {
		Default string `json:"default"`
		Item    struct {
			Code    string `json:"code"`
			Name    string `json:"name"`
			Tag     string `json:"tag"`
			OG      string `json:"og"`
			Default bool   `json:"default"`
			Enabled bool   `json:"enabled"`
			Custom  bool   `json:"custom"`
		} `json:"item"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Default != "pt" || created.Item.Code != "pt" || created.Item.Name != "Português" || created.Item.Tag != "pt-BR" || created.Item.OG != "pt_BR" || !created.Item.Default || !created.Item.Enabled || !created.Item.Custom {
		t.Fatalf("unexpected create response: %#v", created)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/admin/v1/languages", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listResp := httptest.NewRecorder()
	s.apiLanguages(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listResp.Code, listResp.Body.String())
	}
	var listed struct {
		Default string `json:"default"`
		Items   []struct {
			Code    string `json:"code"`
			Default bool   `json:"default"`
			Custom  bool   `json:"custom"`
		} `json:"items"`
	}
	if err := json.Unmarshal(listResp.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if listed.Default != "pt" || len(listed.Items) == 0 || listed.Items[0].Code != "pt" || !listed.Items[0].Default || !listed.Items[0].Custom {
		t.Fatalf("custom language was not enabled as default: %#v", listed)
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
	languages, ok := paths["/languages"].(map[string]any)
	if !ok {
		t.Fatalf("/languages path missing: %#v", paths)
	}
	if _, ok := languages["post"].(map[string]any); !ok {
		t.Fatalf("POST /languages missing: %#v", languages)
	}
	if _, ok := schemas["LanguageCreateInput"]; !ok {
		t.Fatalf("LanguageCreateInput schema missing: %#v", schemas)
	}
	if _, ok := schemas["LanguageItemResponse"]; !ok {
		t.Fatalf("LanguageItemResponse schema missing: %#v", schemas)
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
	for _, path := range []string{"/posts/{id}/preview-url", "/links/{id}/preview-url"} {
		entry, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("%s path missing: %#v", path, paths)
		}
		post, ok := entry["post"].(map[string]any)
		if !ok {
			t.Fatalf("POST %s missing: %#v", path, entry)
		}
		if post["responses"] == nil {
			t.Fatalf("POST %s responses missing: %#v", path, post)
		}
	}
	for _, path := range []string{"/posts/featured/{id}", "/links/featured/{id}"} {
		entry, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("%s path missing: %#v", path, paths)
		}
		patch, ok := entry["patch"].(map[string]any)
		if !ok {
			t.Fatalf("PATCH %s missing: %#v", path, entry)
		}
		if patch["requestBody"] == nil || patch["responses"] == nil {
			t.Fatalf("PATCH %s request/response missing: %#v", path, patch)
		}
	}
	if _, ok := schemas["ContentPreviewResponse"]; !ok {
		t.Fatalf("ContentPreviewResponse schema missing: %#v", schemas)
	}
	if _, ok := schemas["ContentPreview"]; !ok {
		t.Fatalf("ContentPreview schema missing: %#v", schemas)
	}
	if _, ok := schemas["PreviewURLResponse"]; !ok {
		t.Fatalf("PreviewURLResponse schema missing: %#v", schemas)
	}
	if _, ok := schemas["FeaturedInput"]; !ok {
		t.Fatalf("FeaturedInput schema missing: %#v", schemas)
	}
	for _, path := range []string{"/site-profile", "/navigation"} {
		entry, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("%s path missing: %#v", path, paths)
		}
		if _, ok := entry["get"].(map[string]any); !ok {
			t.Fatalf("GET %s missing: %#v", path, entry)
		}
		if _, ok := entry["patch"].(map[string]any); !ok {
			t.Fatalf("PATCH %s missing: %#v", path, entry)
		}
	}
	for _, path := range []string{"/posts/categories", "/links/categories"} {
		entry, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("%s path missing: %#v", path, paths)
		}
		if _, ok := entry["post"].(map[string]any); !ok {
			t.Fatalf("POST %s missing: %#v", path, entry)
		}
	}
	for _, path := range []string{"/posts/categories/all-entry", "/links/categories/all-entry"} {
		entry, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("%s path missing: %#v", path, paths)
		}
		if _, ok := entry["get"].(map[string]any); !ok {
			t.Fatalf("GET %s missing: %#v", path, entry)
		}
		if _, ok := entry["patch"].(map[string]any); !ok {
			t.Fatalf("PATCH %s missing: %#v", path, entry)
		}
	}
	for _, schema := range []string{"SiteProfileResponse", "SiteProfilePatch", "NavigationResponse", "NavigationInput", "CategoryInput", "CategoryItemResponse", "CategoryAllEntryResponse", "CategoryAllEntryPatch"} {
		if _, ok := schemas[schema]; !ok {
			t.Fatalf("%s schema missing: %#v", schema, schemas)
		}
	}
	sitePatch, ok := schemas["SiteProfilePatch"].(map[string]any)
	if !ok {
		t.Fatalf("SiteProfilePatch schema invalid: %#v", schemas["SiteProfilePatch"])
	}
	siteProps, ok := sitePatch["properties"].(map[string]any)
	if !ok {
		t.Fatalf("SiteProfilePatch properties missing: %#v", sitePatch)
	}
	for _, prop := range []string{"logo", "favicon", "share_image", "hero_visual", "hero_image"} {
		if _, ok := siteProps[prop]; !ok {
			t.Fatalf("SiteProfilePatch.%s missing: %#v", prop, siteProps)
		}
	}
	navItem, ok := schemas["NavigationItem"].(map[string]any)
	if !ok {
		t.Fatalf("NavigationItem schema invalid: %#v", schemas["NavigationItem"])
	}
	navProps, ok := navItem["properties"].(map[string]any)
	if !ok {
		t.Fatalf("NavigationItem properties missing: %#v", navItem)
	}
	urlProp, ok := navProps["url"].(map[string]any)
	if !ok {
		t.Fatalf("NavigationItem.url missing: %#v", navProps)
	}
	if desc, _ := urlProp["description"].(string); !strings.Contains(desc, "/links/cat/{slug}") {
		t.Fatalf("NavigationItem.url description missing standard URL rules: %q", desc)
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

func TestAPICategoryAllEntryReadAndWrite(t *testing.T) {
	s, token := newTestAutomationServer(t, strings.Join([]string{
		apiScope("posts", "categories"),
		apiScopePostCategoriesWrite,
		apiScope("links", "categories"),
		apiScopeLinkCategoriesWrite,
	}, ","))

	r := httptest.NewRequest(http.MethodGet, "/api/admin/v1/posts/categories/all-entry?lang=zh", nil)
	r.SetPathValue("collection", "posts")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.apiGetCategoryAllEntry(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("get post all entry status = %d, body = %s", w.Code, w.Body.String())
	}
	var got struct {
		Items []apiCategoryAllEntry `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode post all entry: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].Kind != "post" || got.Items[0].Path != "/category" || got.Items[0].Selectable {
		t.Fatalf("post all entry = %#v", got.Items)
	}

	body, err := json.Marshal(map[string]any{
		"lang":        "zh",
		"title":       "全部教程",
		"description": "覆盖所有教程文章的列表入口。",
		"label":       "全部教程",
		"slug":        "learn",
	})
	if err != nil {
		t.Fatalf("marshal post all entry: %v", err)
	}
	r = httptest.NewRequest(http.MethodPatch, "/api/admin/v1/posts/categories/all-entry", bytes.NewReader(body))
	r.SetPathValue("collection", "posts")
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	s.apiUpdateCategoryAllEntry(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("patch post all entry status = %d, body = %s", w.Code, w.Body.String())
	}
	if cfg := s.archiveConfig("zh", "post"); cfg.Title != "全部教程" || cfg.Path != "/category/learn" || cfg.Label != "全部教程" {
		t.Fatalf("post archive config = %#v", cfg)
	}

	body, err = json.Marshal(map[string]any{
		"items": []map[string]any{
			{
				"lang":        "zh",
				"title":       "资源入口",
				"description": "全部资源链接。",
				"label":       "全部资源",
				"slug":        "resources",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal link all entry: %v", err)
	}
	r = httptest.NewRequest(http.MethodPatch, "/api/admin/v1/links/categories/all-entry", bytes.NewReader(body))
	r.SetPathValue("collection", "links")
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	s.apiUpdateCategoryAllEntry(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("patch link all entry status = %d, body = %s", w.Code, w.Body.String())
	}
	if cfg := s.archiveConfig("zh", "link"); cfg.Title != "资源入口" || cfg.Path != "/links/resources" || cfg.Label != "全部资源" {
		t.Fatalf("link archive config = %#v", cfg)
	}

	r = httptest.NewRequest(http.MethodGet, "/api/admin/v1/links/categories/all-entry?lang=all", nil)
	r.SetPathValue("collection", "links")
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	s.apiGetCategoryAllEntry(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("get all lang link entry status = %d, body = %s", w.Code, w.Body.String())
	}
	got.Items = nil
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode all lang link entry: %v", err)
	}
	if len(got.Items) < 2 {
		t.Fatalf("want multiple language entries, got %#v", got.Items)
	}
}

func TestAPISiteStarterPermissionsAndWrites(t *testing.T) {
	s, token := newTestAutomationServer(t, strings.Join([]string{
		"languages:read",
		apiScopeSiteRead,
		apiScopeSiteWrite,
		apiScopeBrandAssetsWrite,
		apiScopeNavigationRead,
		apiScopeNavigationWrite,
		apiScope("posts", "categories"),
		apiScopePostCategoriesWrite,
		apiScope("links", "categories"),
		apiScopeLinkCategoriesWrite,
	}, ","))

	siteBody, err := json.Marshal(map[string]any{
		"items": []map[string]any{
			{
				"lang":                "zh",
				"hero_title":          "一行命令，跑起完整内容站",
				"home_featured_title": "推荐阅读",
				"default_post_author": "产品团队",
				"logo":                "/uploads/logo-zh.svg",
				"favicon":             "/uploads/favicon.ico",
				"share_image":         "/uploads/share-zh.webp",
				"hero_visual":         "image",
				"hero_image":          "/uploads/hero-zh.webp",
			},
			{
				"lang":                "en",
				"hero_title":          "Launch a complete content site with one command",
				"default_post_author": "Product Team",
				"share_image":         "/uploads/share-en.webp",
				"hero_image":          "/uploads/hero-en.webp",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal site profile: %v", err)
	}
	r := httptest.NewRequest(http.MethodPatch, "/api/admin/v1/site-profile", bytes.NewReader(siteBody))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiUpdateSiteProfile(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("site profile status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := s.store.Setting("site.hero_title"); got != "一行命令，跑起完整内容站" {
		t.Fatalf("site.hero_title = %q", got)
	}
	if got := s.store.Setting("site.hero_title::en"); got != "Launch a complete content site with one command" {
		t.Fatalf("site.hero_title::en = %q", got)
	}
	if got := s.store.Setting("content.post_author"); got != "产品团队" {
		t.Fatalf("content.post_author = %q", got)
	}
	if got := s.store.Setting("site.logo"); got != "/uploads/logo-zh.svg" {
		t.Fatalf("site.logo = %q", got)
	}
	if got := s.store.Setting("site.favicon"); got != "/uploads/favicon.ico" {
		t.Fatalf("site.favicon = %q", got)
	}
	if got := s.store.Setting("site.share_image"); got != "/uploads/share-zh.webp" {
		t.Fatalf("site.share_image = %q", got)
	}
	if got := s.store.Setting("site.share_image::en"); got != "/uploads/share-en.webp" {
		t.Fatalf("site.share_image::en = %q", got)
	}
	if got := s.store.Setting("hero.visual"); got != "image" {
		t.Fatalf("hero.visual = %q", got)
	}
	if got := s.store.Setting("hero.image"); got != "/uploads/hero-zh.webp" {
		t.Fatalf("hero.image = %q", got)
	}
	if got := s.store.Setting("hero.visual::en"); got != "image" {
		t.Fatalf("hero.visual::en = %q", got)
	}
	if got := s.store.Setting("hero.image::en"); got != "/uploads/hero-en.webp" {
		t.Fatalf("hero.image::en = %q", got)
	}

	navBody, err := json.Marshal(map[string]any{
		"items": []map[string]any{
			{"url": "/", "labels": map[string]string{"zh": "首页", "en": "Home"}},
			{"url": "/docs", "labels": map[string]string{"zh": "文档", "en": "Docs"}},
		},
	})
	if err != nil {
		t.Fatalf("marshal navigation: %v", err)
	}
	r = httptest.NewRequest(http.MethodPatch, "/api/admin/v1/navigation", bytes.NewReader(navBody))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	s.apiUpdateNavigation(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("navigation status = %d, body = %s", w.Code, w.Body.String())
	}
	nav := s.apiNavigationResponse()
	if items, ok := nav["items"].([]apiNavigationItem); !ok || len(items) != 2 || items[1].Labels["en"] != "Docs" {
		t.Fatalf("navigation response = %#v", nav)
	}

	catBody, err := json.Marshal(map[string]any{
		"lang":        "zh",
		"name":        "实用教程",
		"slug":        "practical-guides",
		"description": "帮助用户完成上线、配置和运营。",
	})
	if err != nil {
		t.Fatalf("marshal category: %v", err)
	}
	r = httptest.NewRequest(http.MethodPost, "/api/admin/v1/posts/categories", bytes.NewReader(catBody))
	r.SetPathValue("collection", "posts")
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	s.apiCreateCategory(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create category status = %d, body = %s", w.Code, w.Body.String())
	}
	var created struct {
		Item apiCategory `json:"item"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode category: %v", err)
	}
	if created.Item.Kind != "post" || created.Item.Lang != "zh" || created.Item.Slug != "practical-guides" {
		t.Fatalf("created category = %#v", created.Item)
	}
}

func TestAPISiteStarterWriteRequiresScope(t *testing.T) {
	s, token := newTestAutomationServer(t, apiScopeSiteRead)
	body, err := json.Marshal(map[string]any{"hero_title": "blocked"})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	r := httptest.NewRequest(http.MethodPatch, "/api/admin/v1/site-profile", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.apiUpdateSiteProfile(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestAPISiteProfileBrandAssetsRequireScope(t *testing.T) {
	s, siteToken := newTestAutomationServer(t, apiScopeSiteWrite)
	body, err := json.Marshal(map[string]any{"logo": "/uploads/logo.svg"})
	if err != nil {
		t.Fatalf("marshal brand body: %v", err)
	}
	r := httptest.NewRequest(http.MethodPatch, "/api/admin/v1/site-profile", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+siteToken)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiUpdateSiteProfile(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("brand update with site scope status = %d, body = %s", w.Code, w.Body.String())
	}

	s, brandToken := newTestAutomationServer(t, apiScopeBrandAssetsWrite)
	body, err = json.Marshal(map[string]any{"hero_title": "blocked"})
	if err != nil {
		t.Fatalf("marshal site body: %v", err)
	}
	r = httptest.NewRequest(http.MethodPatch, "/api/admin/v1/site-profile", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+brandToken)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	s.apiUpdateSiteProfile(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("site update with brand scope status = %d, body = %s", w.Code, w.Body.String())
	}

	body, err = json.Marshal(map[string]any{
		"logo":        "/uploads/logo.svg",
		"favicon":     "/uploads/favicon.ico",
		"share_image": "/uploads/share.webp",
		"hero_image":  "/uploads/hero.webp",
	})
	if err != nil {
		t.Fatalf("marshal brand body: %v", err)
	}
	r = httptest.NewRequest(http.MethodPatch, "/api/admin/v1/site-profile", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+brandToken)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	s.apiUpdateSiteProfile(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("brand update status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := s.store.Setting("site.logo"); got != "/uploads/logo.svg" {
		t.Fatalf("site.logo = %q", got)
	}
	if got := s.store.Setting("site.favicon"); got != "/uploads/favicon.ico" {
		t.Fatalf("site.favicon = %q", got)
	}
	if got := s.store.Setting("site.share_image"); got != "/uploads/share.webp" {
		t.Fatalf("site.share_image = %q", got)
	}
	if got := s.store.Setting("hero.visual"); got != "image" {
		t.Fatalf("hero.visual = %q", got)
	}
	if got := s.store.Setting("hero.image"); got != "/uploads/hero.webp" {
		t.Fatalf("hero.image = %q", got)
	}
}

func TestAPISiteProfileUpdatesEnglishShareImage(t *testing.T) {
	s, token := newTestAutomationServer(t, apiScopeBrandAssetsWrite)
	if err := s.store.SetSetting("site.share_image", "/uploads/share-zh.webp"); err != nil {
		t.Fatalf("set default share image: %v", err)
	}
	if err := s.store.SetSetting("hero.image", "/uploads/hero-zh.webp"); err != nil {
		t.Fatalf("set default hero image: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"lang":        "en",
		"share_image": "/uploads/share-en.webp",
		"hero_image":  "/uploads/hero-en.webp",
	})
	if err != nil {
		t.Fatalf("marshal share image body: %v", err)
	}
	r := httptest.NewRequest(http.MethodPatch, "/api/admin/v1/site-profile", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiUpdateSiteProfile(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("share image update status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := s.store.Setting("site.share_image"); got != "/uploads/share-zh.webp" {
		t.Fatalf("default site.share_image changed to %q", got)
	}
	if got := s.store.Setting("site.share_image::en"); got != "/uploads/share-en.webp" {
		t.Fatalf("site.share_image::en = %q", got)
	}
	if got := s.store.Setting("hero.image"); got != "/uploads/hero-zh.webp" {
		t.Fatalf("default hero.image changed to %q", got)
	}
	if got := s.store.Setting("hero.visual::en"); got != "image" {
		t.Fatalf("hero.visual::en = %q", got)
	}
	if got := s.store.Setting("hero.image::en"); got != "/uploads/hero-en.webp" {
		t.Fatalf("hero.image::en = %q", got)
	}
	if got := s.site("zh").ShareImage; got != "/uploads/share-zh.webp" {
		t.Fatalf("zh share image = %q", got)
	}
	if got := s.site("en").ShareImage; got != "/uploads/share-en.webp" {
		t.Fatalf("en share image = %q", got)
	}

	var res struct {
		Items []apiSiteProfileItem `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode site profile response: %v", err)
	}
	for _, item := range res.Items {
		if item.Lang == "en" {
			if item.ShareImage != "/uploads/share-en.webp" {
				t.Fatalf("response English share image = %q", item.ShareImage)
			}
			if item.HeroVisual != "image" {
				t.Fatalf("response English hero visual = %q", item.HeroVisual)
			}
			if item.HeroImage != "/uploads/hero-en.webp" {
				t.Fatalf("response English hero image = %q", item.HeroImage)
			}
			return
		}
	}
	t.Fatalf("response missing English site profile item: %#v", res.Items)
}

func TestAutomationStarterZipIncludesBriefAndOpenAPI(t *testing.T) {
	files, err := automationStarterFiles(automationSkillOptions{
		apiBase: "https://example.com/api/admin/v1",
		token:   "gcms_test",
		name:    "starter bot",
		scopes:  strings.Join([]string{apiScopeSiteRead, apiScopeSiteWrite, apiScopeNavigationWrite, apiScopePostCategoriesWrite}, ","),
	})
	if err != nil {
		t.Fatalf("automationStarterFiles: %v", err)
	}
	got := map[string]string{}
	for _, file := range files {
		got[file.name] = file.body
	}
	for _, name := range []string{
		"README.md",
		"gcms-site-starter/给AI的任务说明.md",
		"gcms-site-starter/SKILL.md",
		"gcms-site-starter/新站需求向导.md",
		"gcms-site-starter/站点需求模板.md",
		"gcms-site-starter/第一步-让AI出规划.md",
		"gcms-site-starter/第二步-审核后写入草稿.md",
		"gcms-site-starter/工作流.md",
		"gcms-site-starter/示例提示词.md",
		"gcms-site-starter/connection.json",
		"gcms-site-starter/references/openapi.json",
		"gcms-site-starter/.env",
	} {
		if got[name] == "" {
			t.Fatalf("%s missing from starter files: %#v", name, got)
		}
	}
	if !strings.Contains(got["README.md"], "GCMS 新站 AI 技能包") ||
		!strings.Contains(got["gcms-site-starter/给AI的任务说明.md"], "PATCH /site-profile") ||
		!strings.Contains(got["gcms-site-starter/给AI的任务说明.md"], "/posts/categories/all-entry") {
		t.Fatalf("starter markdown missing expected guidance")
	}
	if !strings.Contains(got["gcms-site-starter/新站需求向导.md"], "第一轮只允许输出规划") ||
		!strings.Contains(got["gcms-site-starter/第一步-让AI出规划.md"], "不允许创建、修改、删除或发布任何内容") ||
		!strings.Contains(got["gcms-site-starter/第二步-审核后写入草稿.md"], "所有页面、文章和链接默认 status=draft") {
		t.Fatalf("starter planning workflow missing expected boundary guidance")
	}
	if !strings.Contains(got["gcms-site-starter/给AI的任务说明.md"], "文章质量与配图标准") ||
		!strings.Contains(got["gcms-site-starter/给AI的任务说明.md"], "Hero 右侧动画标准") ||
		!strings.Contains(got["gcms-site-starter/第一步-让AI出规划.md"], "搜索意图") ||
		!strings.Contains(got["gcms-site-starter/第一步-让AI出规划.md"], "Hero 右侧视觉方案") ||
		!strings.Contains(got["gcms-site-starter/第二步-审核后写入草稿.md"], "POST /media") ||
		!strings.Contains(got["gcms-site-starter/第二步-审核后写入草稿.md"], "hero_image") ||
		!strings.Contains(got["gcms-site-starter/第二步-审核后写入草稿.md"], "cover_image") ||
		!strings.Contains(got["gcms-site-starter/工作流.md"], "文章质量与配图验收") ||
		!strings.Contains(got["gcms-site-starter/SKILL.md"], "需要补图") {
		t.Fatalf("starter article quality and image guidance missing")
	}
	if !strings.Contains(got["gcms-site-starter/SKILL.md"], "gcms-site-starter") || !strings.Contains(got["gcms-site-starter/SKILL.md"], "status: draft") {
		t.Fatalf("starter skill missing expected guidance")
	}
	if !strings.Contains(got["gcms-site-starter/references/openapi.json"], `"/site-profile"`) ||
		!strings.Contains(got["gcms-site-starter/references/openapi.json"], `"CategoryInput"`) ||
		!strings.Contains(got["gcms-site-starter/references/openapi.json"], `"CategoryAllEntryPatch"`) ||
		!strings.Contains(got["gcms-site-starter/references/openapi.json"], `"hero_image"`) {
		t.Fatalf("starter openapi missing site starter paths/schemas")
	}
	if !strings.Contains(got["gcms-site-starter/.env"], "GCMS_API_KEY=gcms_test") {
		t.Fatalf("starter env did not include provided token: %q", got["gcms-site-starter/.env"])
	}
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
	if _, err := part.Write(testWebPBytes()); err != nil {
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

func TestSaveUploadRejectsMismatchedContent(t *testing.T) {
	s := &Server{uploadDir: t.TempDir()}
	if _, err := s.saveUploadFile(strings.NewReader("not an image"), "fake.webp"); err == nil || err.Error() != "bad_type" {
		t.Fatalf("saveUploadFile error = %v, want bad_type", err)
	}
}

func TestServeUploadRejectsDirectoryListing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cover.webp"), testWebPBytes(), 0o644); err != nil {
		t.Fatalf("write upload fixture: %v", err)
	}
	s := &Server{uploadDir: dir}

	for _, path := range []string{"/uploads/", "/uploads/../cover.webp", "/uploads/nested/cover.webp"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		s.serveUpload(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, w.Code)
		}
	}

	r := httptest.NewRequest(http.MethodGet, "/uploads/cover.webp", nil)
	w := httptest.NewRecorder()
	s.serveUpload(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("file status = %d, body = %s", w.Code, w.Body.String())
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
	if _, err := part.Write(testWebPBytes()); err != nil {
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
	logs, err := s.store.ListAutomationLogs(1)
	if err != nil {
		t.Fatalf("list automation logs: %v", err)
	}
	if len(logs) != 1 || !strings.Contains(logs[0].Message, "创建文章（草稿 · English）：Default Author Draft") {
		t.Fatalf("automation log message = %#v", logs)
	}
}

func TestAPIUpdateContentFeaturedRequiresPinScope(t *testing.T) {
	s, token := newTestAutomationServer(t, "posts:read,posts:pin")
	id, err := s.store.CreatePost(&store.Post{
		Type:       "post",
		Lang:       "zh",
		Slug:       "api-pin-post",
		Title:      "API Pin Post",
		Status:     "draft",
		EditorMode: "markdown",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}
	body := bytes.NewReader([]byte(`{"featured":true}`))
	r := httptest.NewRequest(http.MethodPatch, "/api/admin/v1/posts/featured/"+strconv.FormatInt(id, 10), body)
	r.SetPathValue("collection", "posts")
	r.SetPathValue("id", strconv.FormatInt(id, 10))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	s.apiUpdateContentFeatured(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	got, err := s.store.GetPostByID(id)
	if err != nil {
		t.Fatalf("get post: %v", err)
	}
	if got == nil || !got.Featured {
		t.Fatalf("featured = %v, want true", got != nil && got.Featured)
	}

	noPinServer, noPinToken := newTestAutomationServer(t, "posts:read")
	noPinID, err := noPinServer.store.CreatePost(&store.Post{
		Type:       "post",
		Lang:       "zh",
		Slug:       "api-pin-forbidden",
		Title:      "API Pin Forbidden",
		Status:     "draft",
		EditorMode: "markdown",
	})
	if err != nil {
		t.Fatalf("create no pin post: %v", err)
	}
	r = httptest.NewRequest(http.MethodPatch, "/api/admin/v1/posts/featured/"+strconv.FormatInt(noPinID, 10), strings.NewReader(`{"featured":true}`))
	r.SetPathValue("collection", "posts")
	r.SetPathValue("id", strconv.FormatInt(noPinID, 10))
	r.Header.Set("Authorization", "Bearer "+noPinToken)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()

	noPinServer.apiUpdateContentFeatured(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("forbidden status = %d, body = %s", w.Code, w.Body.String())
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
	s := newTestPublicServer(t, "")
	token, prefix := newAutomationToken()
	if _, err := s.store.CreateAutomationKey("preview", token, prefix, "posts:read,links:read"); err != nil {
		t.Fatalf("create automation key: %v", err)
	}
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
		{"posts", postID, "https://example.test/zh/posts/api-preview-post/", "<h2 id=\"section\">Section</h2>"},
		{"links", linkID, "https://example.test/zh/links/api-preview-link/", "<h2 id=\"link-section\">Link Section</h2>"},
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
				FrontendURL string `json:"frontend_preview_url"`
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
		if !strings.Contains(got.Preview.FrontendURL, "/preview/"+tc.collection+"/"+strconv.FormatInt(tc.id, 10)+"?token=") {
			t.Fatalf("%s frontend_preview_url = %q", tc.collection, got.Preview.FrontendURL)
		}
		u, err := url.Parse(got.Preview.FrontendURL)
		if err != nil {
			t.Fatalf("%s parse frontend preview URL: %v", tc.collection, err)
		}
		page := httptest.NewRecorder()
		s.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, u.RequestURI(), nil))
		if page.Code != http.StatusOK {
			t.Fatalf("%s frontend preview status = %d, body = %s", tc.collection, page.Code, page.Body.String())
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

func TestAPICreatePreviewURLRendersFrontendDraft(t *testing.T) {
	s := newTestPublicServer(t, "")
	token, prefix := newAutomationToken()
	if _, err := s.store.CreateAutomationKey("preview", token, prefix, "posts:read"); err != nil {
		t.Fatalf("create automation key: %v", err)
	}
	id, err := s.store.CreatePost(&store.Post{
		Type:       "post",
		Lang:       "zh",
		Slug:       "frontend-preview-draft",
		Title:      "Frontend Preview Draft",
		Content:    "Intro\n\n## Draft Section\n\nBody",
		Status:     "draft",
		EditorMode: "markdown",
	})
	if err != nil {
		t.Fatalf("create post: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/v1/posts/"+strconv.FormatInt(id, 10)+"/preview-url", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create preview URL status = %d, body = %s", w.Code, w.Body.String())
	}
	var got apiPreviewURLResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode preview URL response: %v", err)
	}
	if got.PreviewURL == "" || got.ExpiresAt == "" || got.TTLSeconds != int64(frontendPreviewTTL.Seconds()) {
		t.Fatalf("preview URL response incomplete: %#v", got)
	}
	u, err := url.Parse(got.PreviewURL)
	if err != nil {
		t.Fatalf("parse preview URL: %v", err)
	}
	if u.Path != "/preview/posts/"+strconv.FormatInt(id, 10) || u.Query().Get("token") == "" {
		t.Fatalf("preview URL = %q", got.PreviewURL)
	}

	page := httptest.NewRecorder()
	s.Handler().ServeHTTP(page, httptest.NewRequest(http.MethodGet, u.RequestURI(), nil))
	if page.Code != http.StatusOK {
		t.Fatalf("frontend preview status = %d, body = %s", page.Code, page.Body.String())
	}
	if got := page.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache-control = %q, want no-store", got)
	}
	if got := page.Header().Get("X-Robots-Tag"); got != "noindex, nofollow" {
		t.Fatalf("x-robots-tag = %q", got)
	}
	body := page.Body.String()
	if !strings.Contains(body, "Frontend Preview Draft") || !strings.Contains(body, "Draft Section") {
		t.Fatalf("frontend preview body missing draft content")
	}

	p, err := s.store.GetPostByID(id)
	if err != nil || p == nil {
		t.Fatalf("reload post: %v", err)
	}
	p.Content += "\n\nChanged"
	if err := s.store.UpdatePost(p); err != nil {
		t.Fatalf("update post: %v", err)
	}
	stale := httptest.NewRecorder()
	s.Handler().ServeHTTP(stale, httptest.NewRequest(http.MethodGet, u.RequestURI(), nil))
	if stale.Code != http.StatusGone {
		t.Fatalf("stale preview status = %d, want 410", stale.Code)
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
