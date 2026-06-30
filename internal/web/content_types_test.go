package web

import "testing"

func TestContentTypeRegistryShape(t *testing.T) {
	// 内置三种存在且标记 Builtin。
	for _, k := range []string{"post", "page", "link"} {
		ct := contentTypeByKey(k)
		if ct == nil {
			t.Fatalf("builtin type %q missing from registry", k)
		}
		if !ct.Builtin {
			t.Fatalf("type %q should be Builtin", k)
		}
	}
	// 四种扩展类型存在、非内置、默认关闭。
	for _, k := range []string{"product", "doc", "event", "gallery"} {
		ct := contentTypeByKey(k)
		if ct == nil {
			t.Fatalf("extension type %q missing from registry", k)
		}
		if ct.Builtin {
			t.Fatalf("type %q should not be Builtin", k)
		}
		if ct.DefaultOn {
			t.Fatalf("extension type %q should default OFF", k)
		}
		if ct.URLPrefix == "" {
			t.Fatalf("extension type %q needs a URLPrefix", k)
		}
	}
	// 键唯一。
	seen := map[string]bool{}
	for _, ct := range contentTypes {
		if seen[ct.Key] {
			t.Fatalf("duplicate content type key %q", ct.Key)
		}
		seen[ct.Key] = true
	}
	// extContentTypes 不含内置。
	for _, ct := range extContentTypes() {
		if ct.Builtin {
			t.Fatalf("extContentTypes returned builtin %q", ct.Key)
		}
	}
	// 标签回退。
	if got := contentTypeByKey("product").Name("en"); got != "Products" {
		t.Fatalf("product en name = %q, want Products", got)
	}
	if got := contentTypeByKey("product").Name("ja"); got != "商品" {
		t.Fatalf("product ja name should fall back to zh, got %q", got)
	}
	if f := contentTypeByKey("event").FieldByKey("start_at"); f == nil || f.Type != FieldDatetime {
		t.Fatalf("event.start_at field missing or wrong type: %+v", f)
	}
}

func TestParseEnabledTypes(t *testing.T) {
	got := parseEnabledTypes(" product , gallery ,, ")
	if len(got) != 2 || !got["product"] || !got["gallery"] {
		t.Fatalf("parseEnabledTypes = %v, want {product,gallery}", got)
	}
	if len(parseEnabledTypes("")) != 0 {
		t.Fatalf("empty config should yield no enabled types")
	}
}

func TestPerSiteEnablement(t *testing.T) {
	s := newTestPublicServer(t, "")

	// 默认：扩展类型全部关闭。
	if got := s.activeExtContentTypes(); len(got) != 0 {
		t.Fatalf("default active ext types = %d, want 0", len(got))
	}
	if s.contentTypeActive("product") {
		t.Fatalf("product should be inactive by default")
	}

	// 启用 product + gallery（写入该站点 settings）。
	if err := s.store.SetSetting(enabledContentTypesKey, "product,gallery"); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	active := s.activeExtContentTypes()
	if len(active) != 2 {
		t.Fatalf("active ext types = %d, want 2", len(active))
	}
	if !s.contentTypeActive("product") || !s.contentTypeActive("gallery") {
		t.Fatalf("product/gallery should be active after enabling")
	}
	if s.contentTypeActive("event") {
		t.Fatalf("event should remain inactive (not enabled)")
	}
	// 内置类型恒不经此机制。
	if s.contentTypeActive("post") {
		t.Fatalf("builtin post must not be reported active via ext mechanism")
	}
}
