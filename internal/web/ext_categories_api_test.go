package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
)

// extCatAPIKey 在测试服务器上签发一把指定 scopes 的自动化密钥。
func extCatAPIKey(t *testing.T, s *Server, scopes string) string {
	t.Helper()
	token, prefix := newAutomationToken()
	if _, err := s.store.CreateAutomationKey("ext-cat", token, prefix, scopes); err != nil {
		t.Fatalf("create automation key: %v", err)
	}
	return token
}

func extCatAPIReq(t *testing.T, h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// 扩展类型分类 API 全链路（走完整路由，钉住 {collection}/categories 相对
// {collection}/{id} 的优先级）：list / create / update / all-entry 读写。
func TestExtCategoryAPIEndToEnd(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product: %v", err)
	}
	h := s.Handler()
	token := extCatAPIKey(t, s, "products:read,products:write")

	// list：命中分类处理器而不是内容详情（后者会回 400 bad_id）。
	w := extCatAPIReq(t, h, http.MethodGet, "/api/admin/v1/products/categories?lang=zh", token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", w.Code, w.Body.String())
	}
	var list struct {
		Kind  string           `json:"kind"`
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if list.Kind != "product" {
		t.Fatalf("list kind = %q, want product", list.Kind)
	}

	// create
	w = extCatAPIReq(t, h, http.MethodPost, "/api/admin/v1/products/categories", token,
		`{"name":"风扇系列","lang":"zh","description":"工业风扇"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", w.Code, w.Body.String())
	}
	var created struct {
		Item struct {
			ID   int64  `json:"id"`
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"item"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if created.Item.Kind != "product" || created.Item.Name != "风扇系列" {
		t.Fatalf("created item = %+v", created.Item)
	}
	cats, _ := s.store.ListCategories("zh", "product")
	if len(cats) != 1 || cats[0].Kind != "product" {
		t.Fatalf("category not stored with kind=product: %+v", cats)
	}

	// update（{collection}/categories/{id} 相对 {collection}/{id} 的优先级）
	w = extCatAPIReq(t, h, http.MethodPatch,
		"/api/admin/v1/products/categories/"+strconv.FormatInt(created.Item.ID, 10), token,
		`{"name":"风扇与散热","slug":"fans"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", w.Code, w.Body.String())
	}
	cats, _ = s.store.ListCategories("zh", "product")
	if len(cats) != 1 || cats[0].Name != "风扇与散热" || cats[0].Slug != "fans" {
		t.Fatalf("update not applied: %+v", cats)
	}

	// all-entry 读：默认回落类型名，路径固定为 /products。
	w = extCatAPIReq(t, h, http.MethodGet, "/api/admin/v1/products/categories/all-entry?lang=zh", token, "")
	if w.Code != http.StatusOK {
		t.Fatalf("all-entry get status = %d, body = %s", w.Code, w.Body.String())
	}
	var entry struct {
		Items []apiCategoryAllEntry `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &entry)
	if len(entry.Items) != 1 || entry.Items[0].Title != "商品" || entry.Items[0].Path != "/products" {
		t.Fatalf("all-entry items = %+v", entry.Items)
	}

	// all-entry 写：落到 ext_archive_meta（与后台「归档页文案」同一份数据）。
	w = extCatAPIReq(t, h, http.MethodPatch, "/api/admin/v1/products/categories/all-entry", token,
		`{"lang":"zh","title":"产品中心","description":"外贸产品目录"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("all-entry patch status = %d, body = %s", w.Code, w.Body.String())
	}
	if title, intro := s.extArchiveText("product", "zh"); title != "产品中心" || intro != "外贸产品目录" {
		t.Fatalf("ext archive meta = %q/%q", title, intro)
	}

	// slug/label 对扩展类型不适用 → 400 报清楚。
	w = extCatAPIReq(t, h, http.MethodPatch, "/api/admin/v1/products/categories/all-entry", token,
		`{"lang":"zh","slug":"goods"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("all-entry slug patch status = %d, want 400", w.Code)
	}
}

// scope 口径：读走 products:read、写走 products:write；posts 的 categories 专属
// scope 管不到扩展集合；content:write 通配照常放行。
func TestExtCategoryAPIScopes(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product: %v", err)
	}
	h := s.Handler()

	readOnly := extCatAPIKey(t, s, "products:read")
	if w := extCatAPIReq(t, h, http.MethodGet, "/api/admin/v1/products/categories?lang=zh", readOnly, ""); w.Code != http.StatusOK {
		t.Fatalf("read with products:read = %d", w.Code)
	}
	if w := extCatAPIReq(t, h, http.MethodPost, "/api/admin/v1/products/categories", readOnly, `{"name":"X","lang":"zh"}`); w.Code != http.StatusForbidden {
		t.Fatalf("write with products:read = %d, want 403", w.Code)
	}

	postsOnly := extCatAPIKey(t, s, "posts:categories,posts:categories:write")
	if w := extCatAPIReq(t, h, http.MethodGet, "/api/admin/v1/products/categories?lang=zh", postsOnly, ""); w.Code != http.StatusForbidden {
		t.Fatalf("read with posts scopes = %d, want 403", w.Code)
	}

	wildcard := extCatAPIKey(t, s, "content:read,content:write")
	if w := extCatAPIReq(t, h, http.MethodPost, "/api/admin/v1/products/categories", wildcard, `{"name":"通配","lang":"zh"}`); w.Code != http.StatusCreated {
		t.Fatalf("write with content:write wildcard = %d, want 201", w.Code)
	}
}

// 不支持分类的集合与未知集合 → 404；posts/links 字面路由不回归。
func TestExtCategoryAPICollectionGating(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product,doc"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	h := s.Handler()
	token := extCatAPIKey(t, s, "content:read,content:write,posts:categories,links:categories")

	// doc 类型不支持分类 → 404；pages 同理；未知集合 404。
	for _, path := range []string{
		"/api/admin/v1/docs/categories?lang=zh",
		"/api/admin/v1/pages/categories?lang=zh",
		"/api/admin/v1/nonsense/categories?lang=zh",
	} {
		if w := extCatAPIReq(t, h, http.MethodGet, path, token, ""); w.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", path, w.Code)
		}
	}

	// posts/links 字面路由照旧（categories 专属 scope 可读）。
	for _, path := range []string{
		"/api/admin/v1/posts/categories?lang=zh",
		"/api/admin/v1/links/categories?lang=zh",
	} {
		if w := extCatAPIReq(t, h, http.MethodGet, path, token, ""); w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, w.Code)
		}
	}

	// 内容详情路由没被分类路由抢走：GET /products/{id} 仍按内容处理（数字 id 查不到 → 404 not_found 而非 bad_id）。
	if w := extCatAPIReq(t, h, http.MethodGet, "/api/admin/v1/products/999999", token, ""); w.Code != http.StatusNotFound {
		t.Fatalf("content get = %d, want 404", w.Code)
	}
}

// 平台镜像同形可用（含路由优先级）。
func TestExtCategoryAPIPlatformMirror(t *testing.T) {
	srv, h, ps, _, blogSite := setupPlatformAutomation(t)
	_ = srv
	// 给 blog 站启用 product。
	rt, ok := srv.runtimePool().runtimeByID(blogSite.ID)
	if !ok {
		t.Fatalf("blog runtime missing")
	}
	if err := rt.Store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product on blog: %v", err)
	}
	token := "gcmsp_extcat123456789"
	if _, err := ps.CreatePlatformKey("extcat", token, token[:13], platform.KeyMembershipAll,
		"content:read,content:write", nil, time.Time{}); err != nil {
		t.Fatalf("create platform key: %v", err)
	}
	prefix := "/api/platform/v1/sites/" + strconv.FormatInt(blogSite.ID, 10)

	rec := platformAPIReq(t, h, http.MethodPost, prefix+"/products/categories", token, []byte(`{"name":"平台分类","lang":"zh"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("platform create = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec = platformAPIReq(t, h, http.MethodGet, prefix+"/products/categories?lang=zh", token, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "平台分类") {
		t.Fatalf("platform list = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec = platformAPIReq(t, h, http.MethodGet, prefix+"/products/categories/all-entry?lang=zh", token, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/products") {
		t.Fatalf("platform all-entry = %d, body = %s", rec.Code, rec.Body.String())
	}
}
