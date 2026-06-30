package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestAdminExtCRUD 验证扩展类型的后台动态表单：创建（含自定义字段→extra）、
// 列表展示、编辑回填，以及未启用类型在后台 404。
func TestAdminExtCRUD(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product: %v", err)
	}
	h := s.Handler()

	// 通过表单创建（标准字段 + 自定义字段 f_price / f_gallery / f_specs）
	form := url.Values{
		"title":     {"测试商品"},
		"slug":      {"crud-prod"},
		"content":   {"商品正文"},
		"status":    {"published"},
		"f_price":   {"299"},
		"f_gallery": {"/u/x.webp\n/u/y.webp"},
		"f_specs":   {"重量: 1.2kg\n颜色: 黑"},
	}
	req, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/ext/product", form)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, body = %s", w.Code, w.Body.String())
	}

	p, _ := s.store.GetTypedBySlug("product", s.defaultLang(), "crud-prod", true)
	if p == nil {
		t.Fatalf("created product not found")
	}
	for _, want := range []string{"299", "/u/x.webp", "/u/y.webp", "重量", "1.2kg"} {
		if !strings.Contains(p.Extra, want) {
			t.Fatalf("extra missing %q: %s", want, p.Extra)
		}
	}

	// 列表展示
	reqL, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/ext/product", nil)
	wL := httptest.NewRecorder()
	h.ServeHTTP(wL, reqL)
	if wL.Code != http.StatusOK || !strings.Contains(wL.Body.String(), "测试商品") {
		t.Fatalf("list missing product (status %d)", wL.Code)
	}

	// 编辑表单回填自定义字段
	reqE, _ := authedAdminRequest(t, s, http.MethodGet, fmt.Sprintf("/admin/ext/product/%d/edit", p.ID), nil)
	wE := httptest.NewRecorder()
	h.ServeHTTP(wE, reqE)
	if wE.Code != http.StatusOK {
		t.Fatalf("edit status = %d", wE.Code)
	}
	eb := wE.Body.String()
	for _, want := range []string{"测试商品", "299", "/u/x.webp", "重量: 1.2kg"} {
		if !strings.Contains(eb, want) {
			t.Fatalf("edit form missing %q", want)
		}
	}

	// 更新：改价格
	upd := url.Values{
		"title":   {"测试商品"},
		"slug":    {"crud-prod"},
		"status":  {"published"},
		"f_price": {"359"},
	}
	reqU, _ := authedAdminRequest(t, s, http.MethodPost, fmt.Sprintf("/admin/ext/product/%d", p.ID), upd)
	wU := httptest.NewRecorder()
	h.ServeHTTP(wU, reqU)
	if wU.Code != http.StatusSeeOther {
		t.Fatalf("update status = %d", wU.Code)
	}
	p2, _ := s.store.GetPostByID(p.ID)
	if p2 == nil || !strings.Contains(p2.Extra, "359") {
		t.Fatalf("update did not persist new price: %v", p2)
	}

	// 未启用的类型在后台 404
	if err := s.store.SetSetting(enabledContentTypesKey, ""); err != nil {
		t.Fatalf("disable: %v", err)
	}
	reqD, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/ext/product", nil)
	wD := httptest.NewRecorder()
	h.ServeHTTP(wD, reqD)
	if wD.Code != http.StatusNotFound {
		t.Fatalf("disabled admin list should 404, got %d", wD.Code)
	}
}
