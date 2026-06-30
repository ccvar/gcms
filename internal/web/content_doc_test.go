package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

// TestDocHierarchy 覆盖文档的层级能力：后台父级下拉选择、子文档父级落库、
// 前台侧边树（当前项高亮）、结构性字段不作为内容展示。
func TestDocHierarchy(t *testing.T) {
	s := newTestPublicServer(t, "")
	lang := s.defaultLang()
	if err := s.store.SetSetting(enabledContentTypesKey, "doc"); err != nil {
		t.Fatalf("enable doc: %v", err)
	}
	h := s.Handler()

	pid, err := s.store.CreatePost(&store.Post{Type: "doc", Lang: lang, Slug: "guide", Title: "使用指南", Status: "published", Extra: `{"order":1}`})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}

	// 后台新建表单：父级下拉含已有文档
	req, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/ext/doc/new", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("new form status = %d", w.Code)
	}
	if nb := w.Body.String(); !strings.Contains(nb, `name="f_parent"`) || !strings.Contains(nb, "使用指南") {
		t.Fatalf("doc form missing parent <select> with existing doc")
	}

	// 通过表单创建子文档，挂到父级下
	form := url.Values{"title": {"安装"}, "slug": {"install"}, "status": {"published"}, "f_parent": {fmt.Sprintf("%d", pid)}, "f_order": {"1"}}
	rc, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/ext/doc", form)
	wc := httptest.NewRecorder()
	h.ServeHTTP(wc, rc)
	if wc.Code != http.StatusSeeOther {
		t.Fatalf("create child status = %d, body = %s", wc.Code, wc.Body.String())
	}
	child, _ := s.store.GetTypedBySlug("doc", lang, "install", true)
	if child == nil || !strings.Contains(child.Extra, fmt.Sprintf("%d", pid)) {
		t.Fatalf("child parent not stored: %v", child)
	}

	// 前台子文档详情：侧边树含两篇、当前项高亮、不显示结构性字段
	wd := httptest.NewRecorder()
	h.ServeHTTP(wd, httptest.NewRequest(http.MethodGet, "/"+lang+"/docs/install", nil))
	if wd.Code != http.StatusOK {
		t.Fatalf("doc detail status = %d, body = %s", wd.Code, wd.Body.String())
	}
	db := wd.Body.String()
	if !strings.Contains(db, "doc-tree") || !strings.Contains(db, "使用指南") || !strings.Contains(db, "/"+lang+"/docs/install") {
		t.Fatalf("doc detail missing sidebar tree")
	}
	if !strings.Contains(db, `class="active"`) {
		t.Fatalf("current doc not marked active in tree")
	}
	if strings.Contains(db, "上级文档") {
		t.Fatalf("doc detail should not show structural parent field as content")
	}

	// 归档页也渲染树
	wa := httptest.NewRecorder()
	h.ServeHTTP(wa, httptest.NewRequest(http.MethodGet, "/"+lang+"/docs", nil))
	if wa.Code != http.StatusOK {
		t.Fatalf("doc archive status = %d", wa.Code)
	}
	if ab := wa.Body.String(); !strings.Contains(ab, "doc-tree") || !strings.Contains(ab, "安装") {
		t.Fatalf("doc archive missing tree")
	}
}
