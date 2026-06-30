package web

import "testing"

// TestMenuTargetsIncludeEnabledExtTypes 验证已启用的扩展类型归档页出现在后台
// 导航「指向哪里」选项中，未启用时不出现。
func TestMenuTargetsIncludeEnabledExtTypes(t *testing.T) {
	s := newTestPublicServer(t, "")

	hasExtProduct := func() (MenuTargetOption, bool) {
		for _, o := range s.menuTargetOptions() {
			if o.Value == "ext:product" {
				return o, true
			}
		}
		return MenuTargetOption{}, false
	}

	if _, ok := hasExtProduct(); ok {
		t.Fatalf("product should not be a menu target while disabled")
	}

	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product: %v", err)
	}
	opt, ok := hasExtProduct()
	if !ok {
		t.Fatalf("enabled product missing from menu targets")
	}
	if opt.URL != "/products" {
		t.Fatalf("ext product target URL = %q, want /products", opt.URL)
	}
	if opt.Labels["zh"] != "商品" {
		t.Fatalf("ext product zh label = %q, want 商品", opt.Labels["zh"])
	}
}
