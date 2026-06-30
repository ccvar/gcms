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

// TestExtArchiveTranslatePreview 覆盖扩展内容的三项后台能力：
// #1 归档页文案（分语种自定义标题/简介，前台生效）、#4 草稿预览（公开 404、后台 noindex）、
// #2 跨语种互译（生成目标语种草稿并共享 trans_group）。
func TestExtArchiveTranslatePreview(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting("locales", "zh,en"); err != nil {
		t.Fatalf("set locales: %v", err)
	}
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product: %v", err)
	}
	h := s.Handler()
	lang := s.defaultLang()

	form := url.Values{"title": {"手冲壶"}, "slug": {"pourover"}, "content": {"正文"}, "status": {"published"}, "lang": {lang}, "f_price": {"268"}}
	req, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/ext/product", form)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d, body=%s", w.Code, w.Body.String())
	}
	p, _ := s.store.GetTypedBySlug("product", lang, "pourover", true)
	if p == nil {
		t.Fatalf("product not created")
	}

	// #1 归档页文案：保存自定义标题/简介，前台归档页生效
	am := url.Values{"title_" + lang: {"精选好物"}, "intro_" + lang: {"严选每一件。"}}
	ra, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/ext/product/archive", am)
	wa := httptest.NewRecorder()
	h.ServeHTTP(wa, ra)
	if wa.Code != http.StatusSeeOther {
		t.Fatalf("archive save status = %d", wa.Code)
	}
	wf := httptest.NewRecorder()
	h.ServeHTTP(wf, httptest.NewRequest(http.MethodGet, "/"+lang+"/products", nil))
	if fb := wf.Body.String(); !strings.Contains(fb, "精选好物") || !strings.Contains(fb, "严选每一件。") {
		t.Fatalf("archive page missing custom title/intro")
	}

	// #4 草稿预览：转为草稿后，公开 404、后台预览 200 且 noindex
	updDraft := url.Values{"title": {"手冲壶"}, "slug": {"pourover"}, "status": {"draft"}, "lang": {lang}, "f_price": {"268"}}
	ru, _ := authedAdminRequest(t, s, http.MethodPost, fmt.Sprintf("/admin/ext/product/%d", p.ID), updDraft)
	wu := httptest.NewRecorder()
	h.ServeHTTP(wu, ru)
	if wu.Code != http.StatusSeeOther {
		t.Fatalf("to-draft status = %d", wu.Code)
	}
	wPub := httptest.NewRecorder()
	h.ServeHTTP(wPub, httptest.NewRequest(http.MethodGet, "/"+lang+"/products/pourover", nil))
	if wPub.Code != http.StatusNotFound {
		t.Fatalf("draft public should 404, got %d", wPub.Code)
	}
	rp, _ := authedAdminRequest(t, s, http.MethodGet, fmt.Sprintf("/admin/ext/product/%d/preview", p.ID), nil)
	wp := httptest.NewRecorder()
	h.ServeHTTP(wp, rp)
	if wp.Code != http.StatusOK {
		t.Fatalf("preview status = %d", wp.Code)
	}
	if pb := wp.Body.String(); !strings.Contains(pb, "手冲壶") || !strings.Contains(pb, "noindex") {
		t.Fatalf("preview missing content or noindex meta")
	}

	// #2 互译：翻译为 en → 生成 en 草稿并共享 trans_group
	rt, _ := authedAdminRequest(t, s, http.MethodPost, fmt.Sprintf("/admin/ext/product/%d/translate", p.ID), url.Values{"lang": {"en"}})
	wt := httptest.NewRecorder()
	h.ServeHTTP(wt, rt)
	if wt.Code != http.StatusSeeOther {
		t.Fatalf("translate status = %d", wt.Code)
	}
	en, _ := s.store.GetTypedBySlug("product", "en", "pourover", true)
	if en == nil {
		t.Fatalf("en translation not created")
	}
	if en.Lang != "en" || en.TransGroup != p.TransGroup {
		t.Fatalf("en translation not linked: lang=%q group=%q (want en / %q)", en.Lang, en.TransGroup, p.TransGroup)
	}
}
