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

	// 后台列表按层级排序、章节缩进（带 ↳），父级排在子级之前
	wAL := httptest.NewRecorder()
	rAL, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/ext/doc", nil)
	h.ServeHTTP(wAL, rAL)
	alb := wAL.Body.String()
	if !strings.Contains(alb, "ext-doc-child") || !strings.Contains(alb, "↳") {
		t.Fatalf("admin doc list missing hierarchy indent")
	}
	if strings.Index(alb, "/docs/guide") > strings.Index(alb, "/docs/install") {
		t.Fatalf("parent should be listed before its child")
	}

	// GitBook 式：章节各自独立成页；详情页含左侧持久导航树（两篇都在、当前页高亮），不显示结构字段
	wd := httptest.NewRecorder()
	h.ServeHTTP(wd, httptest.NewRequest(http.MethodGet, "/"+lang+"/docs/install", nil))
	if wd.Code != http.StatusOK {
		t.Fatalf("chapter page status = %d, body = %s", wd.Code, wd.Body.String())
	}
	db := wd.Body.String()
	if !strings.Contains(db, "doc-nav") || !strings.Contains(db, "使用指南") || !strings.Contains(db, "/"+lang+"/docs/install") {
		t.Fatalf("chapter page missing sidebar nav tree")
	}
	if !strings.Contains(db, `aria-current="page"`) {
		t.Fatalf("current chapter not highlighted in nav")
	}
	if strings.Contains(db, "上级文档") {
		t.Fatalf("chapter page should not show structural parent field as content")
	}

	// 分类文档页：底部列出本节下级章节
	wg := httptest.NewRecorder()
	h.ServeHTTP(wg, httptest.NewRequest(http.MethodGet, "/"+lang+"/docs/guide", nil))
	if wg.Code != http.StatusOK {
		t.Fatalf("doc page status = %d", wg.Code)
	}
	if gb := wg.Body.String(); !strings.Contains(gb, "doc-children") || !strings.Contains(gb, "安装") {
		t.Fatalf("category doc page missing child chapters")
	}

	// 归档（/docs）：左侧导航 + 章节概览
	wa := httptest.NewRecorder()
	h.ServeHTTP(wa, httptest.NewRequest(http.MethodGet, "/"+lang+"/docs", nil))
	if wa.Code != http.StatusOK {
		t.Fatalf("doc archive status = %d", wa.Code)
	}
	if ab := wa.Body.String(); !strings.Contains(ab, "doc-overview") || !strings.Contains(ab, "使用指南") || !strings.Contains(ab, "安装") {
		t.Fatalf("doc archive missing nav/overview")
	}

	// 拖动排序：再建一个同级章节，调换顺序后 extra.order 落库为新下标
	form2 := url.Values{"title": {"配置"}, "slug": {"config"}, "status": {"published"}, "f_parent": {fmt.Sprintf("%d", pid)}, "f_order": {"2"}}
	rc2, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/ext/doc", form2)
	wc2 := httptest.NewRecorder()
	h.ServeHTTP(wc2, rc2)
	child2, _ := s.store.GetTypedBySlug("doc", lang, "config", true)
	if child2 == nil {
		t.Fatalf("second child not created")
	}
	// 新顺序：config 在前、install 在后
	ro := url.Values{}
	ro.Add("ids", fmt.Sprintf("%d", child2.ID))
	ro.Add("ids", fmt.Sprintf("%d", child.ID))
	rr, _ := authedAdminRequest(t, s, http.MethodPost, "/admin/ext/doc/reorder", ro)
	wr := httptest.NewRecorder()
	h.ServeHTTP(wr, rr)
	if wr.Code != http.StatusOK {
		t.Fatalf("reorder status = %d, body = %s", wr.Code, wr.Body.String())
	}
	c2, _ := s.store.GetPostByID(child2.ID)
	c1, _ := s.store.GetPostByID(child.ID)
	if c2 == nil || !strings.Contains(c2.Extra, `"order":0`) {
		t.Fatalf("reordered first child should have order 0: %v", c2)
	}
	if c1 == nil || !strings.Contains(c1.Extra, `"order":1`) {
		t.Fatalf("reordered second child should have order 1: %v", c1)
	}
	// 排序不应破坏上级关系
	if !strings.Contains(c2.Extra, fmt.Sprintf("%d", pid)) {
		t.Fatalf("reorder dropped parent: %v", c2)
	}
}
