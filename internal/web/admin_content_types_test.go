package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

// TestAdminExtHubAndToggle 验证后台「扩展」hub 列出扩展类型，并能启用/停用（写入本站设置）。
func TestAdminExtHubAndToggle(t *testing.T) {
	s := newTestPublicServer(t, "")
	h := s.Handler()

	// hub 页列出扩展类型
	req, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/extensions", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("hub status = %d, body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"商品", "相册", "/products"} {
		if !strings.Contains(body, want) {
			t.Fatalf("hub missing %q", want)
		}
	}

	// 启用 product
	req2, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/extensions/toggle", url.Values{"type": {"product"}, "on": {"1"}})
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, req2)
	if w2.Code != http.StatusSeeOther {
		t.Fatalf("enable status = %d, want 303", w2.Code)
	}
	if !s.contentTypeActive("product") {
		t.Fatalf("product should be active after enabling")
	}

	// 停用 product
	req3, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/extensions/toggle", url.Values{"type": {"product"}, "on": {"0"}})
	w3 := httptest.NewRecorder()
	h.ServeHTTP(w3, req3)
	if w3.Code != http.StatusSeeOther {
		t.Fatalf("disable status = %d, want 303", w3.Code)
	}
	if s.contentTypeActive("product") {
		t.Fatalf("product should be inactive after disabling")
	}

	// 内置类型不可通过该入口切换
	req4, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/extensions/toggle", url.Values{"type": {"post"}, "on": {"1"}})
	w4 := httptest.NewRecorder()
	h.ServeHTTP(w4, req4)
	if w4.Code != http.StatusNotFound {
		t.Fatalf("toggling builtin should 404, got %d", w4.Code)
	}
}

// TestExtListSettingsMenuAndBackLink 验证类型列表页的「类型设置」菜单与返回链分支：
//   - Primary 类型（product）：无「← 扩展」返回链（一级板块，与文章页一致）；
//     设置菜单含归档页文案 + 停用类型；内置类型无「编辑类型」入口；
//   - 非 Primary 类型（gallery）：保留返回 hub 的链接，同样有设置菜单；
//   - 自定义类型（DB）：设置菜单额外含「编辑类型」；
//   - 从设置菜单停用：303 落回 hub，hub 重新出现该卡，列表页 404。
func TestExtListSettingsMenuAndBackLink(t *testing.T) {
	s := newTestPublicServer(t, "")
	h := s.Handler()
	if err := s.store.SaveContentType(&store.ContentTypeRow{Key: "recipe", Name: `{"zh":"菜谱"}`, URLPrefix: "recipe", Fields: `[]`}); err != nil {
		t.Fatalf("save custom type: %v", err)
	}
	if err := s.store.SetSetting(enabledContentTypesKey, "gallery,product,recipe"); err != nil {
		t.Fatalf("enable types: %v", err)
	}
	get := func(path string) string {
		t.Helper()
		req, _ := authedAdminRequest(t, s, http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, w.Code)
		}
		return w.Body.String()
	}

	// Primary：无返回链，设置菜单三要素（归档页文案 / 停用类型），内置类型无编辑入口。
	body := get("/admin/ext/product")
	if strings.Contains(body, `class="ext-back"`) {
		t.Fatalf("primary type list should not show back-to-hub link")
	}
	if !strings.Contains(body, `href="/admin/ext/product/archive"`) {
		t.Fatalf("settings menu missing archive-copy entry")
	}
	if !strings.Contains(body, `name="type" value="product"`) || !strings.Contains(body, "停用类型") {
		t.Fatalf("settings menu missing disable form")
	}
	if strings.Contains(body, "/admin/extensions/types/product/edit") {
		t.Fatalf("builtin type should not offer edit-type entry")
	}

	// 非 Primary：返回链保留，同样有设置菜单。
	body = get("/admin/ext/gallery")
	if !strings.Contains(body, `class="ext-back"`) {
		t.Fatalf("non-primary type list should keep back-to-hub link")
	}
	if !strings.Contains(body, `href="/admin/ext/gallery/archive"`) || !strings.Contains(body, `name="type" value="gallery"`) {
		t.Fatalf("non-primary type list missing settings menu entries")
	}

	// 自定义类型：设置菜单含「编辑类型」。
	body = get("/admin/ext/recipe")
	if !strings.Contains(body, `href="/admin/extensions/types/recipe/edit"`) {
		t.Fatalf("custom type should offer edit-type entry in settings menu")
	}

	// 从设置菜单停用 product：303 回 hub → hub 重新出现该卡 → 列表页 404。
	reqD, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/extensions/toggle", url.Values{"type": {"product"}, "on": {"0"}})
	wD := httptest.NewRecorder()
	h.ServeHTTP(wD, reqD)
	if wD.Code != http.StatusSeeOther || wD.Header().Get("Location") != "/admin/extensions?saved=1" {
		t.Fatalf("disable redirect = %d %q, want 303 /admin/extensions?saved=1", wD.Code, wD.Header().Get("Location"))
	}
	hub := get("/admin/extensions")
	if !strings.Contains(hub, `name="type" value="product"`) {
		t.Fatalf("hub should show product card again after disabling")
	}
	reqL, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/ext/product", nil)
	wL := httptest.NewRecorder()
	h.ServeHTTP(wL, reqL)
	if wL.Code != http.StatusNotFound {
		t.Fatalf("disabled type list should 404, got %d", wL.Code)
	}
}

// TestExtHubEmptyStateWhenAllPromoted 验证 hub 空态：所有类型都已上浮（ExtTypes 为空）时，
// 渲染合理的空态说明而非空白网格。注册表内非 Primary 类型恒存在，端到端造不出该状态，
// 这里直接以空行集渲染模板验证分支。
func TestExtHubEmptyStateWhenAllPromoted(t *testing.T) {
	s := newTestPublicServer(t, "")
	req := httptest.NewRequest(http.MethodGet, "/admin/extensions", nil)
	v := s.adminView(req, "扩展")
	v.ExtTypes = nil
	w := httptest.NewRecorder()
	s.rnd.Admin(w, "extensions", http.StatusOK, v)
	body := w.Body.String()
	if !strings.Contains(body, `class="ext-hub-empty"`) || !strings.Contains(body, "上浮为左侧一级菜单") {
		t.Fatalf("hub empty state missing: %s", body)
	}
	if strings.Contains(body, `class="ext-grid"`) {
		t.Fatalf("hub empty state should not render the grid")
	}
}
