package web

import (
	"testing"

	"cms.ccvar.com/internal/store"
)

// TestFactoryThemesRegistered 工厂主题族登记一致性：36 款皮肤全部 Category=factory，
// 且映射到九个工厂骨架（每骨架 4 皮）；factory chip 因此在后台筛选里出现。
func TestFactoryThemesRegistered(t *testing.T) {
	wantLayout := map[string]string{
		"industrial": "factory-catalog", "machinist": "factory-catalog",
		"tradewind": "factory-catalog", "foundry": "factory-catalog",
		"showroom": "factory-showcase", "assembly": "factory-showcase",
		"harbor": "factory-showcase", "gunmetal": "factory-showcase",
		"packline": "factory-onepage", "carbon": "factory-onepage",
		"linen": "factory-onepage", "redline": "factory-onepage",
		"drafting": "factory-solutions", "flagship": "factory-solutions",
		"concrete": "factory-solutions", "amberpress": "factory-solutions",
		"phosphor": "factory-engineering", "schematic": "factory-engineering",
		"titanium": "factory-engineering", "hazard": "factory-engineering",
		"navigator": "factory-trade", "cargo": "factory-trade",
		"mistblue": "factory-trade", "malachite": "factory-trade",
		"steelrack": "factory-sidebar", "depot": "factory-sidebar",
		"nightbay": "factory-sidebar", "plateblue": "factory-sidebar",
		"eclipse": "factory-vision", "haze": "factory-vision",
		"copperglow": "factory-vision", "indigo": "factory-vision",
		"glaze": "factory-herofold", "carbonblue": "factory-herofold",
		"warmsand": "factory-herofold", "nightfall": "factory-herofold",
	}
	byID := map[string]ThemeOption{}
	for _, th := range Themes {
		byID[th.ID] = th
	}
	for id, layout := range wantLayout {
		th, ok := byID[id]
		if !ok {
			t.Errorf("factory theme %q not registered", id)
			continue
		}
		if th.Category != ThemeCategoryFactory {
			t.Errorf("theme %q category = %q, want factory", id, th.Category)
		}
		if got := layoutForTheme(id); got != layout {
			t.Errorf("theme %q layout = %q, want %q", id, got, layout)
		}
		if !isFactoryLayout(layoutForTheme(id)) {
			t.Errorf("layout of %q not recognized as factory", id)
		}
	}
	// factory 分类必须出现在筛选 chips 里
	found := false
	for _, c := range themeCategoriesPresent() {
		if c == ThemeCategoryFactory {
			found = true
		}
	}
	if !found {
		t.Error("themeCategoriesPresent() missing factory")
	}
}

// TestParseFactoryStats factory.stats 解析容错：数字/字符串 num、丢弃空项、最多 4 组、非法 JSON 返回空。
func TestParseFactoryStats(t *testing.T) {
	got := parseFactoryStats(`[{"num":2008,"label":"工厂成立"},{"num":"12,000㎡","label":"自有厂房"},{"num":"","label":"空的丢弃"},{"label":"没有数字"},{"num":"45+","label":"出口国家"},{"num":"200+","label":"员工"},{"num":"999","label":"超出上限"}]`)
	if len(got) != 4 {
		t.Fatalf("stats len = %d, want 4 (最多 4 组): %#v", len(got), got)
	}
	if got[0].Num != "2008" || got[0].Label != "工厂成立" {
		t.Fatalf("stats[0] = %#v", got[0])
	}
	if got[1].Num != "12,000㎡" {
		t.Fatalf("stats[1].Num = %q", got[1].Num)
	}
	if got[3].Num != "200+" {
		t.Fatalf("stats[3] = %#v（空项应被丢弃）", got[3])
	}
	for _, raw := range []string{"", "not json", `{"num":1}`, "[]", "null"} {
		if out := parseFactoryStats(raw); len(out) != 0 {
			t.Fatalf("parseFactoryStats(%q) = %#v, want empty", raw, out)
		}
	}
}

// TestFactorySpecCompare 技术骨架规格对比表取数：specs 共有键求交集、键序跟随第一个商品、
// 无规格的商品跳过、带规格商品 <2 或无共有键回落 nil、行数与列数受上限约束。
func TestFactorySpecCompare(t *testing.T) {
	p := func(title, extra string) *store.Post { return &store.Post{Title: title, Extra: extra} }

	got := factorySpecCompare([]*store.Post{
		p("A", `{"specs":[{"k":"型号","v":"A-1"},{"k":"材质","v":"铝"},{"k":"尺寸","v":"10mm"},{"k":"认证","v":"CE"}]}`),
		p("无规格（跳过）", ``),
		p("B", `{"specs":[{"k":"材质","v":"钢"},{"k":"型号","v":"B-2"},{"k":"重量","v":"1kg"}]}`),
		p("C", `{"specs":[{"k":"型号","v":"C-3"},{"k":"材质","v":"铜"},{"k":"尺寸","v":"12mm"}]}`),
	})
	if got == nil {
		t.Fatal("compare = nil, want table")
	}
	if len(got.Items) != 3 || got.Items[0].Title != "A" || got.Items[1].Title != "B" || got.Items[2].Title != "C" {
		t.Fatalf("items = %d（无规格的商品应被跳过）", len(got.Items))
	}
	// 共有键 = 型号/材质（尺寸缺 B、重量缺 A/C），键序跟随第一个商品的 specs 顺序。
	if len(got.Rows) != 2 || got.Rows[0].Key != "型号" || got.Rows[1].Key != "材质" {
		t.Fatalf("rows = %#v, want [型号 材质]", got.Rows)
	}
	if got.Rows[0].Vals[1] != "B-2" || got.Rows[1].Vals[2] != "铜" {
		t.Fatalf("vals 错位：%#v", got.Rows)
	}

	// 带规格的商品不足 2 个 → nil（回落普通栅格）。
	if out := factorySpecCompare([]*store.Post{
		p("A", `{"specs":[{"k":"型号","v":"A-1"}]}`),
		p("B", `not json`),
		p("C", ``),
	}); out != nil {
		t.Fatalf("单商品应回落 nil, got %#v", out)
	}
	// 没有任何共有键 → nil。
	if out := factorySpecCompare([]*store.Post{
		p("A", `{"specs":[{"k":"型号","v":"A-1"}]}`),
		p("B", `{"specs":[{"k":"材质","v":"钢"}]}`),
	}); out != nil {
		t.Fatalf("无共有键应回落 nil, got %#v", out)
	}
	if out := factorySpecCompare(nil); out != nil {
		t.Fatalf("空列表应回落 nil, got %#v", out)
	}

	// 上限：最多 4 列；行数最多 maxFactoryCompareRows。
	many := make([]*store.Post, 0, 6)
	extra := `{"specs":[{"k":"k1","v":"v"},{"k":"k2","v":"v"},{"k":"k3","v":"v"},{"k":"k4","v":"v"},{"k":"k5","v":"v"},{"k":"k6","v":"v"},{"k":"k7","v":"v"}]}`
	for i := 0; i < 6; i++ {
		many = append(many, p("P", extra))
	}
	got = factorySpecCompare(many)
	if got == nil || len(got.Items) != maxFactoryCompareItems || len(got.Rows) != maxFactoryCompareRows {
		t.Fatalf("上限失守：items=%d rows=%d", len(got.Items), len(got.Rows))
	}
}

// TestProductChips 商品卡「卖点规格」chip：嗅探材质/起订量，最多 2 个，顺序固定材质在前。
func TestProductChips(t *testing.T) {
	p := &store.Post{Extra: `{"specs":[{"k":"型号","v":"GC-HB-150"},{"k":"起订量","v":"100 台"},{"k":"材质","v":"压铸铝"},{"k":"认证","v":"CE"}]}`}
	got := productChips(p)
	if len(got) != 2 || got[0].K != "材质" || got[0].V != "压铸铝" || got[1].K != "起订量" || got[1].V != "100 台" {
		t.Fatalf("productChips = %#v, want [材质 压铸铝, 起订量 100 台]", got)
	}
	en := &store.Post{Extra: `{"specs":[{"k":"Material","v":"6061-T6 aluminum"},{"k":"MOQ","v":"500 pcs"}]}`}
	got = productChips(en)
	if len(got) != 2 || got[0].V != "6061-T6 aluminum" || got[1].V != "500 pcs" {
		t.Fatalf("productChips(en) = %#v", got)
	}
	for _, extra := range []string{"", "not json", `{}`, `{"specs":[{"k":"颜色","v":"黑"}]}`, `{"specs":[{"k":"材质","v":""}]}`} {
		if out := productChips(&store.Post{Extra: extra}); len(out) != 0 {
			t.Fatalf("productChips(%q) = %#v, want empty", extra, out)
		}
	}
}

// TestProductSKU 从规格 repeater 嗅探型号/SKU。
func TestProductSKU(t *testing.T) {
	cases := []struct {
		extra string
		want  string
	}{
		{`{"specs":[{"k":"型号","v":"6204-2RS"},{"k":"材质","v":"轴承钢"}]}`, "6204-2RS"},
		{`{"specs":[{"k":"Model No.","v":"FLG-DN50"}]}`, "FLG-DN50"},
		{`{"specs":[{"k":"SKU","v":"A-100"}]}`, "A-100"},
		{`{"specs":[{"k":"材质","v":"304 不锈钢"}]}`, ""},
		{`{}`, ""},
		{``, ""},
		{`not json`, ""},
	}
	for _, tc := range cases {
		p := &store.Post{Extra: tc.extra}
		if got := productSKU(p); got != tc.want {
			t.Errorf("productSKU(%q) = %q, want %q", tc.extra, got, tc.want)
		}
	}
}
