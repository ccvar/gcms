package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

func TestNormalizeSiteKind(t *testing.T) {
	cases := map[string]string{
		"":          siteKindContent,
		"content":   siteKindContent,
		"factory":   siteKindFactory,
		" factory ": siteKindFactory,
		"weird":     siteKindContent,
	}
	for in, want := range cases {
		if got := normalizeSiteKind(in); got != want {
			t.Fatalf("normalizeSiteKind(%q) = %q, want %q", in, got, want)
		}
	}
}

// 工厂站预设（带演示数据的站）：product 已启用 + 「商品」插入既有导航首页之后
// （其余入口保留）+ site.kind 记录为 factory。
func TestApplySiteKindPresetFactory(t *testing.T) {
	s := newTestPublicServer(t, "")
	before := parseMenuRows(s.store.Setting("nav_menu")) // 演示种子自带菜单

	if err := applySiteKindPreset(s.store, s.i18n, siteKindFactory, false); err != nil {
		t.Fatalf("applySiteKindPreset: %v", err)
	}

	if got := s.store.Setting(siteKindSettingKey); got != siteKindFactory {
		t.Fatalf("site.kind = %q, want factory", got)
	}
	if !s.contentTypeActive("product") {
		t.Fatalf("product should be active after factory preset (enabled_content_types=%q)",
			s.store.Setting(enabledContentTypesKey))
	}

	rows := parseMenuRows(s.store.Setting("nav_menu"))
	if len(rows) != len(before)+1 {
		t.Fatalf("nav rows = %d, want %d (existing + product)", len(rows), len(before)+1)
	}
	var product *MenuRow
	urls := map[string]bool{}
	for i := range rows {
		urls[rows[i].URL] = true
		if rows[i].URL == "/products" {
			product = &rows[i]
		}
	}
	if product == nil {
		t.Fatalf("nav_menu should contain /products, got %v", rows)
	}
	if product.Labels["zh"] != "商品" || product.Labels["en"] != "Products" {
		t.Fatalf("product nav labels = %v", product.Labels)
	}
	// 既有入口全部保留；商品插在首页之后。
	for _, row := range before {
		if !urls[row.URL] {
			t.Fatalf("factory preset dropped existing nav entry %q", row.URL)
		}
	}
	if len(before) > 0 && before[0].URL == "/" && rows[1].URL != "/products" {
		t.Fatalf("product entry should sit right after home, got %q", rows[1].URL)
	}

	// 品牌最终生效文字站名：演示种子先写了 site.brand=logo（深色工厂皮下演示 logo
	// 会隐形），建站预设必须盖过它——工厂站落 text。
	if got := s.store.Setting("site.brand"); got != "text" {
		t.Fatalf("site.brand = %q, want text（工厂预设需覆盖演示种子的 logo）", got)
	}

	// 幂等：重复应用不再追加。
	if err := applySiteKindPreset(s.store, s.i18n, siteKindFactory, false); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if again := parseMenuRows(s.store.Setting("nav_menu")); len(again) != len(rows) {
		t.Fatalf("re-apply should be idempotent: %d != %d", len(again), len(rows))
	}

	// 「商品」入口与导航挂载目标枚举（ext:product）指向一致。
	target := ""
	for _, opt := range s.menuTargetOptions() {
		if opt.Value == "ext:product" {
			target = opt.URL
		}
	}
	if target != "/products" {
		t.Fatalf("ext:product target URL = %q, want /products", target)
	}

	// 前台导航真的渲染「商品」。
	req := httptest.NewRequest(http.MethodGet, "/zh/", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("home status = %d", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "/zh/products") {
		t.Fatalf("home nav should link to /zh/products")
	}
}

// 内容站（默认）：只记录 site.kind，不动启用集合与导航。
func TestApplySiteKindPresetContent(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := newTestPublicServer(t, "")

	navBefore := st.Setting("nav_menu")
	typesBefore := st.Setting(enabledContentTypesKey)
	if err := applySiteKindPreset(st, s.i18n, "", true); err != nil {
		t.Fatalf("applySiteKindPreset: %v", err)
	}
	if got := st.Setting(siteKindSettingKey); got != siteKindContent {
		t.Fatalf("site.kind = %q, want content", got)
	}
	if got := st.Setting(enabledContentTypesKey); got != typesBefore {
		t.Fatalf("content site should not touch enabled types: %q != %q", got, typesBefore)
	}
	if got := st.Setting("nav_menu"); got != navBefore {
		t.Fatalf("content site should keep nav untouched")
	}
}

// 空数据站（无 nav_menu）：工厂站预设写出完整默认菜单（首页/商品/分类/关于），
// 且每行都带 zh/en 标签、是合法 JSON。
func TestFactoryNavMenuFromEmpty(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting("nav_menu", ""); err != nil { // 模拟空数据站
		t.Fatalf("clear nav: %v", err)
	}
	if err := applySiteKindPreset(s.store, s.i18n, siteKindFactory, false); err != nil {
		t.Fatalf("applySiteKindPreset: %v", err)
	}
	var rows []MenuRow
	if err := json.Unmarshal([]byte(s.store.Setting("nav_menu")), &rows); err != nil {
		t.Fatalf("nav_menu not valid JSON: %v", err)
	}
	wantURLs := []string{"/", "/products", "/category", "/about"}
	if len(rows) != len(wantURLs) {
		t.Fatalf("nav rows = %v", rows)
	}
	for i, row := range rows {
		if row.URL != wantURLs[i] {
			t.Fatalf("nav row %d = %q, want %q", i, row.URL, wantURLs[i])
		}
		for _, code := range []string{"zh", "en"} {
			if strings.TrimSpace(row.Labels[code]) == "" {
				t.Fatalf("nav row %q missing %s label: %v", row.URL, code, row.Labels)
			}
		}
	}
}

// 工厂站 + 带演示数据：写入 6 组中英配对演示商品（每语种 6 条，已发布），
// 每条带价格 + 规格 repeater（≥3 行）、分类与封面；幂等；前台货架直接可见。
func TestFactoryDemoProducts(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := applySiteKindPreset(s.store, s.i18n, siteKindFactory, true); err != nil {
		t.Fatalf("applySiteKindPreset: %v", err)
	}

	for _, lang := range []string{"zh", "en"} {
		posts, err := s.store.ListAllByType("product", lang)
		if err != nil {
			t.Fatalf("list products (%s): %v", lang, err)
		}
		if len(posts) != 6 {
			t.Fatalf("demo products (%s) = %d, want 6", lang, len(posts))
		}
		ct := contentTypeByKey("product")
		for _, p := range posts {
			if p.Status != "published" {
				t.Fatalf("demo product %q should be published, got %q", p.Slug, p.Status)
			}
			if strings.TrimSpace(p.CoverImage) == "" {
				t.Fatalf("demo product %q missing cover", p.Slug)
			}
			if !p.CategoryID.Valid {
				t.Fatalf("demo product %q not attached to a category", p.Slug)
			}
			fields := renderFieldValues(ct, p, lang)
			var price string
			var specs []FieldPair
			for _, f := range fields {
				switch f.Key {
				case "price":
					price = f.Text
				case "specs":
					specs = f.Pairs
				}
			}
			// 演示商品刻意不填价格：price 是自由文本（可「面议」），留空 = 前台不显示价格行，
			// 更贴外贸习惯（见 seed_factory.go 头注）。
			if price != "" {
				t.Fatalf("demo product %q should not carry a price (extra=%s)", p.Slug, p.Extra)
			}
			if len(specs) < 3 {
				t.Fatalf("demo product %q specs rows = %d, want >= 3 (extra=%s)", p.Slug, len(specs), p.Extra)
			}
		}
	}
	// 中英按 trans_group 配对。
	zh, _ := s.store.ListAllByType("product", "zh")
	for _, p := range zh {
		trs, _ := s.store.TranslationsAll(p.TransGroup, p.ID)
		found := false
		for _, tr := range trs {
			if tr.Lang == "en" {
				found = true
			}
		}
		if !found {
			t.Fatalf("demo product %q missing en translation", p.Slug)
		}
	}

	// 幂等：重复应用不再翻倍。
	if err := applySiteKindPreset(s.store, s.i18n, siteKindFactory, true); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if again, _ := s.store.ListAllByType("product", "zh"); len(again) != 6 {
		t.Fatalf("demo products should be idempotent, got %d", len(again))
	}

	// 前台货架：/zh/products 列出演示商品。
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/zh/products", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("products archive status = %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"精密 CNC 铝合金加工件", "150W LED 工矿灯", "纯棉华夫格酒店毛巾"} {
		if !strings.Contains(body, want) {
			t.Fatalf("products archive missing %q", want)
		}
	}
}

// 空数据工厂站（seedDemo=false）：不写演示商品。
func TestFactoryDemoProductsSkippedForEmptySeed(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := applySiteKindPreset(s.store, s.i18n, siteKindFactory, false); err != nil {
		t.Fatalf("applySiteKindPreset: %v", err)
	}
	if posts, _ := s.store.ListAllByType("product", "zh"); len(posts) != 0 {
		t.Fatalf("empty-seed factory site should have no demo products, got %d", len(posts))
	}
}

// 扩展 hub 迁出：已启用且 Primary 的类型整卡不再出现在 hub（内容入口在左侧一级菜单，
// 类型管理在其列表页的设置菜单）；非 Primary 类型（gallery）照旧显示完整卡片；
// 停用后 Primary 类型的卡片重新出现（带启用按钮）。
func TestExtHubDedupsPrimaryTypes(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "gallery,product"); err != nil {
		t.Fatalf("enable types: %v", err)
	}

	rows := s.extTypeRows("zh")
	byKey := map[string]ExtTypeRow{}
	for _, row := range rows {
		byKey[row.Key] = row
	}
	if !byKey["product"].Primary || !byKey["product"].Enabled {
		t.Fatalf("product row should be enabled+primary: %+v", byKey["product"])
	}
	if byKey["gallery"].Primary {
		t.Fatalf("gallery should not be primary")
	}
	// hub 行集合：已上浮的 product 被过滤；非 Primary（gallery）与未启用类型（doc）保留。
	hubKeys := map[string]bool{}
	for _, row := range s.hubExtTypeRows("zh") {
		hubKeys[row.Key] = true
	}
	if hubKeys["product"] {
		t.Fatalf("enabled primary type should be filtered from hub rows")
	}
	if !hubKeys["gallery"] || !hubKeys["doc"] {
		t.Fatalf("hub rows should keep non-primary/disabled types: %v", hubKeys)
	}

	req, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/extensions", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ext hub status = %d", w.Code)
	}
	body := w.Body.String()
	// product 整卡迁出：内容入口、停用表单、归档文案弹窗全都不在 hub
	//（顶部一级菜单的 /admin/ext/product 链接仍在，那正是上浮本身）。
	if strings.Contains(body, `class="ext-go" href="/admin/ext/product"`) {
		t.Fatalf("hub should not show content entry for promoted primary type")
	}
	if strings.Contains(body, `name="type" value="product"`) {
		t.Fatalf("hub should not show disable form for promoted primary type")
	}
	if strings.Contains(body, `data-archive-open="product"`) {
		t.Fatalf("hub should not show archive-copy entry for promoted primary type")
	}
	// 非 Primary 的 gallery 照旧：内容入口 + 类型管理（停用表单/归档文案）都在。
	if !strings.Contains(body, `class="ext-go" href="/admin/ext/gallery"`) {
		t.Fatalf("hub should keep content entry for non-primary enabled type")
	}
	if !strings.Contains(body, `name="type" value="gallery"`) || !strings.Contains(body, `data-archive-open="gallery"`) {
		t.Fatalf("hub should keep type management for non-primary enabled type")
	}

	// 停用后：product 卡片重新出现（带启用表单）。
	if err := s.store.SetSetting(enabledContentTypesKey, "gallery"); err != nil {
		t.Fatalf("disable product: %v", err)
	}
	req2, _ := authedAdminRequest(t, s, http.MethodGet, "/admin/extensions", nil)
	w2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(w2, req2)
	if !strings.Contains(w2.Body.String(), `name="type" value="product"`) {
		t.Fatalf("disabled primary type should reappear in hub")
	}
}

// 端到端：POST /admin/sites 选工厂站 → 新站 product 已启用、导航含商品、
// site.kind=factory；进入该站后台后左侧出现一级「商品」菜单。
func TestCreateFactorySiteEndToEnd(t *testing.T) {
	srv, h, ps, _, _ := setupPlatformAutomation(t)
	cookie := platformAdminSession(t, ps)

	form := url.Values{
		"_csrf":     {"csrf"},
		"slug":      {"factory1"},
		"name":      {"外贸工厂站"},
		"seed_mode": {"demo"},
		"site_kind": {"factory"},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/sites", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/sites" {
		t.Fatalf("create site: status=%d loc=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}

	sites, err := ps.Sites()
	if err != nil {
		t.Fatalf("sites: %v", err)
	}
	var siteID int64
	for _, st := range sites {
		if st != nil && st.Slug == "factory1" {
			siteID = st.ID
		}
	}
	if siteID == 0 {
		t.Fatalf("factory1 site not created")
	}
	rt, ok := srv.runtimePool().runtimeByID(siteID)
	if !ok || rt == nil {
		t.Fatalf("runtime for new site missing")
	}
	if got := rt.Store.Setting(siteKindSettingKey); got != siteKindFactory {
		t.Fatalf("new site kind = %q, want factory", got)
	}
	if enabled := parseEnabledTypes(rt.Store.Setting(enabledContentTypesKey)); !enabled["product"] {
		t.Fatalf("new factory site should enable product, got %q", rt.Store.Setting(enabledContentTypesKey))
	}
	nav := rt.Store.Setting("nav_menu")
	if !strings.Contains(nav, "/products") {
		t.Fatalf("new factory site nav missing products entry: %s", nav)
	}
	// 带演示数据 → 演示商品开箱即有（每语种 6 条）。
	if products, _ := rt.Store.ListAllByType("product", "zh"); len(products) != 6 {
		t.Fatalf("new factory site demo products = %d, want 6", len(products))
	}

	// 进入该站后台 → 左侧一级菜单出现「商品」（/admin/ext/product 直达）。
	enter := httptest.NewRecorder()
	enterReq := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/sites/"+strconv.FormatInt(siteID, 10)+"/enter",
		strings.NewReader(url.Values{"_csrf": {"csrf"}}.Encode()))
	enterReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	enterReq.AddCookie(cookie)
	h.ServeHTTP(enter, enterReq)
	if enter.Code != http.StatusSeeOther {
		t.Fatalf("enter site status = %d", enter.Code)
	}
	admin := httptest.NewRecorder()
	adminReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin", nil)
	adminReq.AddCookie(cookie)
	h.ServeHTTP(admin, adminReq)
	if admin.Code != http.StatusOK {
		t.Fatalf("admin overview status = %d", admin.Code)
	}
	if body := admin.Body.String(); !strings.Contains(body, `href="/admin/ext/product"`) {
		t.Fatalf("admin nav missing primary 商品 entry")
	}
}

// 已启用且标记 Primary 的扩展类型上浮为后台一级菜单。
func TestPrimaryExtNav(t *testing.T) {
	s := newTestPublicServer(t, "")

	if got := s.primaryExtNav("zh"); len(got) != 0 {
		t.Fatalf("primary nav should be empty before enabling, got %v", got)
	}
	if err := s.store.SetSetting(enabledContentTypesKey, "product,gallery"); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	got := s.primaryExtNav("zh")
	if len(got) != 1 || got[0].Key != "product" || got[0].Name != "商品" {
		t.Fatalf("primary nav = %v, want [{product 商品}]", got)
	}
	if got := s.primaryExtNav("en"); len(got) != 1 || got[0].Name != "Products" {
		t.Fatalf("primary nav (en) = %v, want Products", got)
	}
}

// 主题轻分类：每个条目都有合法分类；分类枚举只含实际存在的类别。
func TestThemeCategories(t *testing.T) {
	valid := map[string]bool{ThemeCategoryContent: true, ThemeCategoryFactory: true, ThemeCategoryDTC: true, ThemeCategoryGeneral: true}
	counts := map[string]int{}
	for _, th := range Themes {
		if !valid[th.Category] {
			t.Fatalf("theme %q has invalid category %q", th.ID, th.Category)
		}
		counts[th.Category]++
	}
	if counts[ThemeCategoryContent] == 0 || counts[ThemeCategoryGeneral] == 0 {
		t.Fatalf("expected both content and general themes, got %v", counts)
	}

	// themeCategoriesPresent 只报注册表里实际存在的分类，且按 content → factory → dtc → general 定序
	//（主题族到位前该分类不出现，chips 随注册表自动跟进）。
	var want []string
	for _, c := range []string{ThemeCategoryContent, ThemeCategoryFactory, ThemeCategoryDTC, ThemeCategoryGeneral} {
		if counts[c] > 0 {
			want = append(want, c)
		}
	}
	present := themeCategoriesPresent()
	if len(present) != len(want) {
		t.Fatalf("themeCategoriesPresent() = %v, want %v", present, want)
	}
	for i := range want {
		if present[i] != want[i] {
			t.Fatalf("themeCategoriesPresent() = %v, want %v", present, want)
		}
	}

	// 后台展示层（含英文名转换）保留分类。
	for _, th := range Themes {
		if got := themeOptionForAdmin(th, "en"); got.Category != th.Category {
			t.Fatalf("themeOptionForAdmin dropped category for %q", th.ID)
		}
	}
}
