package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

// 分类设置页的扩展类型页签：只上「已启用且支持分类」的扩展类型（泛化，不硬编码 product）。
func TestExtCategoryKinds(t *testing.T) {
	s := newTestPublicServer(t, "")
	if tabs := s.extCategoryKinds("zh"); len(tabs) != 0 {
		t.Fatalf("no ext type enabled, tabs = %v", tabs)
	}
	// product/event 支持分类，doc 不支持——启用三者只应出前两个。
	if err := s.store.SetSetting(enabledContentTypesKey, "doc,event,product"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	tabs := s.extCategoryKinds("zh")
	got := map[string]string{}
	for _, tab := range tabs {
		got[tab.Key] = tab.Name
	}
	if len(tabs) != 2 || got["product"] != "商品" || got["event"] != "活动" {
		t.Fatalf("tabs = %v, want product+event", tabs)
	}
}

// catKind 泛化：post/link 照旧；已启用且支持分类的扩展类型 key 放行；
// 未启用/不支持分类/未知 kind 回落 post。
func TestCatKindValidation(t *testing.T) {
	s := newTestPublicServer(t, "")
	mk := func(kind string) *http.Request {
		return httptest.NewRequest(http.MethodGet, "/admin/settings/categories?kind="+kind, nil)
	}
	if got := s.catKind(mk("link")); got != "link" {
		t.Fatalf("link -> %q", got)
	}
	if got := s.catKind(mk("product")); got != "post" {
		t.Fatalf("disabled product should fall back to post, got %q", got)
	}
	if err := s.store.SetSetting(enabledContentTypesKey, "product,doc"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if got := s.catKind(mk("product")); got != "product" {
		t.Fatalf("enabled product -> %q, want product", got)
	}
	if got := s.catKind(mk("doc")); got != "post" {
		t.Fatalf("doc has no categories, should fall back to post, got %q", got)
	}
	if got := s.catKind(mk("nonsense")); got != "post" {
		t.Fatalf("unknown kind -> %q, want post", got)
	}
}

// 分类设置页渲染：未启用时无商品页签；启用后出现「商品分类」页签，
// kind=product 视图无「全部入口」行与其弹窗、带扩展说明文案。
func TestCategoriesPageExtSectionRendering(t *testing.T) {
	s := newTestPublicServer(t, "")
	h := s.Handler()

	get := func(path string) string {
		req, _ := authedAdminRequest(t, s, http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, w.Code)
		}
		return w.Body.String()
	}

	// 未启用：无商品页签；?kind=product 回落文章分类视图（仍有全部入口行）。
	body := get("/admin/settings/categories?kind=post&lang=zh")
	if strings.Contains(body, "商品分类") {
		t.Fatalf("product tab should not render before enabling")
	}
	body = get("/admin/settings/categories?kind=product&lang=zh")
	if !strings.Contains(body, "全部文章入口") {
		t.Fatalf("disabled product kind should fall back to post view")
	}

	// 启用 product：页签出现；product 视图无全部入口、有扩展说明。
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	body = get("/admin/settings/categories?kind=post&lang=zh")
	if !strings.Contains(body, "商品分类") || !strings.Contains(body, "kind=product") {
		t.Fatalf("categories page missing product tab after enabling")
	}
	body = get("/admin/settings/categories?kind=product&lang=zh")
	if strings.Contains(body, "全部文章入口") || strings.Contains(body, "全部链接入口") || strings.Contains(body, `id="cat-all-modal"`) {
		t.Fatalf("ext kind view should not render the all-entry row/modal")
	}
	if !strings.Contains(body, "归档页文案") {
		t.Fatalf("ext kind view should point archive copy to the extensions hub")
	}
	// 文章/链接视图不回归。
	body = get("/admin/settings/categories?kind=link&lang=zh")
	if !strings.Contains(body, "全部链接入口") || !strings.Contains(body, `id="cat-all-modal"`) {
		t.Fatalf("link kind view regressed")
	}
}

// 商品分类 CRUD 走既有表单端点，kind=product 正确落库；删除后挂着的商品变未分类；
// 文章分类不受影响。
func TestExtCategoryCRUD(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	h := s.Handler()
	post := func(path string, form url.Values) *httptest.ResponseRecorder {
		req, _ := authedAdminRequest(t, s, http.MethodPost, path, form)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	// 新增
	w := post("/admin/settings/categories", url.Values{
		"lang": {"zh"}, "kind": {"product"}, "name": {"风扇系列"}, "description": {"工业风扇产品线"},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, body = %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "kind=product") {
		t.Fatalf("create redirect should keep kind=product, got %q", loc)
	}
	cats, _ := s.store.ListCategories("zh", "product")
	if len(cats) != 1 || cats[0].Name != "风扇系列" || cats[0].Kind != "product" {
		t.Fatalf("product category not stored correctly: %+v", cats)
	}
	catID := cats[0].ID
	// 文章分类没被波及（演示种子的 post 分类仍是 post kind）。
	if postCats, _ := s.store.ListCategories("zh", "post"); len(postCats) > 0 {
		for _, c := range postCats {
			if c.Kind != "post" {
				t.Fatalf("post category kind corrupted: %+v", c)
			}
		}
	}

	// 改名 + 自定义 slug
	w = post("/admin/settings/categories", url.Values{
		"lang": {"zh"}, "kind": {"product"}, "id": {strconv.FormatInt(catID, 10)},
		"name": {"风扇与散热"}, "slug": {"fans"},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d", w.Code)
	}
	cats, _ = s.store.ListCategories("zh", "product")
	if len(cats) != 1 || cats[0].Name != "风扇与散热" || cats[0].Slug != "fans" || cats[0].Kind != "product" {
		t.Fatalf("product category update wrong: %+v", cats)
	}

	// 挂一条商品再删分类：内容变未分类（与文章/链接分类同语义，FK SET NULL）。
	pid, err := s.store.CreatePost(&store.Post{
		Type: "product", Lang: "zh", Slug: "cat-crud-prod", Title: "分类测试商品", Status: "published",
	})
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	p, _ := s.store.GetPostByID(pid)
	p.CategoryID.Int64, p.CategoryID.Valid = catID, true
	if err := s.store.UpdatePost(p); err != nil {
		t.Fatalf("attach category: %v", err)
	}
	w = post("/admin/settings/categories/delete", url.Values{
		"lang": {"zh"}, "kind": {"product"}, "id": {strconv.FormatInt(catID, 10)},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("delete status = %d", w.Code)
	}
	if cats, _ = s.store.ListCategories("zh", "product"); len(cats) != 0 {
		t.Fatalf("product category not deleted: %+v", cats)
	}
	if p2, _ := s.store.GetPostByID(pid); p2 == nil || p2.CategoryID.Valid {
		t.Fatalf("product should become uncategorized after category delete, got %+v", p2)
	}

	// 「全部入口」端点对扩展 kind 关门（归档文案走扩展 hub）。
	w = post("/admin/settings/categories/all", url.Values{
		"lang": {"zh"}, "kind": {"product"}, "title": {"X"}, "label": {"Y"},
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("categories/all with ext kind = %d, want 404", w.Code)
	}
}

// 后台分类页建的商品分类，商品编辑表单（引擎动态表单）立刻可选——两处操作同一数据。
func TestExtCategorySharedWithEngineForm(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	h := s.Handler()
	req, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/settings/categories", url.Values{
		"lang": {"zh"}, "kind": {"product"}, "name": {"户外系列"},
	})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d", w.Code)
	}
	reqN, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/ext/product/new", nil)
	wN := httptest.NewRecorder()
	h.ServeHTTP(wN, reqN)
	if wN.Code != http.StatusOK {
		t.Fatalf("ext new form status = %d", wN.Code)
	}
	if !strings.Contains(wN.Body.String(), "户外系列") {
		t.Fatalf("engine edit form should offer the category created from settings page")
	}
}
