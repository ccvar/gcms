package web

import (
	"testing"

	"cms.ccvar.com/internal/store"
)

// TestExportExtTypePagesEnumerates 验证静态导出会为已启用扩展类型产出归档页与详情页，
// 未启用类型完全不产出（保证未用该类型的站点导出无变化）。
func TestExportExtTypePagesEnumerates(t *testing.T) {
	s := newTestPublicServer(t, "")
	lang := s.defaultLang()
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product: %v", err)
	}
	for _, slug := range []string{"p1", "p2"} {
		if _, err := s.store.CreatePost(&store.Post{Type: "product", Lang: lang, Slug: slug, Title: slug, Status: "published"}); err != nil {
			t.Fatalf("create %s: %v", slug, err)
		}
	}

	var outs []string
	render := func(_ /*req*/, out string) error { outs = append(outs, out); return nil }

	if err := s.exportExtTypePages(render, lang, "/"+lang); err != nil {
		t.Fatalf("export: %v", err)
	}
	for _, want := range []string{
		"/" + lang + "/products/index.html",
		"/" + lang + "/products/p1/index.html",
		"/" + lang + "/products/p2/index.html",
	} {
		if !containsStr(outs, want) {
			t.Fatalf("export missing %q; got %v", want, outs)
		}
	}

	// 未启用类型：导出零产出。
	if err := s.store.SetSetting(enabledContentTypesKey, ""); err != nil {
		t.Fatalf("disable: %v", err)
	}
	outs = nil
	if err := s.exportExtTypePages(render, lang, "/"+lang); err != nil {
		t.Fatalf("export disabled: %v", err)
	}
	if len(outs) != 0 {
		t.Fatalf("disabled type should export nothing, got %v", outs)
	}
}

func containsStr(ss []string, x string) bool {
	for _, v := range ss {
		if v == x {
			return true
		}
	}
	return false
}
