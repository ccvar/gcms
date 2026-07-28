package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

var secondBatchDTCThemes = []string{
	"vitrine", "journalshop", "catalogue", "bazaar", "column",
	"swissgrid", "catwalk", "handcraft", "monthbox", "booth",
	"herbary", "grocer", "cellar", "basecamp", "toybox",
	"paperie", "mono", "flatlay", "flyer", "lightbox",
}

// ---------- 注册全表 ----------

// 独立站主题族注册齐全：13 皮 × Category=dtc、骨架映射、accent/radius/bg/骨架名/EN 描述。
func TestDTCThemesRegistered(t *testing.T) {
	wantLayouts := map[string]string{
		"cream": "dtc-flagship", "amberglow": "dtc-flagship", "inknavy": "dtc-flagship", "oliveleaf": "dtc-flagship",
		"dawnfair":  "dtc-flagship", // Shopify Dawn 气质皮（旗舰骨架第 5 皮）
		"solowhite": "dtc-solo", "charcoal": "dtc-solo", "coralpop": "dtc-solo", "limewash": "dtc-solo",
		"galleria": "dtc-lookbook", "blackbox": "dtc-lookbook", "flaxen": "dtc-lookbook", "fogblue": "dtc-lookbook",
		// 第二批 20 骨架 ×3 皮（原生 / 反差 / 纯白）
		"vitrine": "dtc-vitrine", "vitrine-noir": "dtc-vitrine", "vitrine-white": "dtc-vitrine",
		"journalshop": "dtc-journal", "journalshop-noir": "dtc-journal", "journalshop-white": "dtc-journal",
		"catalogue": "dtc-catalogue", "catalogue-noir": "dtc-catalogue", "catalogue-white": "dtc-catalogue",
		"bazaar": "dtc-bazaar", "bazaar-noir": "dtc-bazaar", "bazaar-white": "dtc-bazaar",
		"column": "dtc-column", "column-day": "dtc-column", "column-white": "dtc-column",
		"swissgrid": "dtc-swissgrid", "swissgrid-noir": "dtc-swissgrid", "swissgrid-white": "dtc-swissgrid",
		"catwalk": "dtc-runway", "catwalk-day": "dtc-runway", "catwalk-white": "dtc-runway",
		"handcraft": "dtc-atelier", "handcraft-noir": "dtc-atelier", "handcraft-white": "dtc-atelier",
		"monthbox": "dtc-monthbox", "monthbox-noir": "dtc-monthbox", "monthbox-white": "dtc-monthbox",
		"booth": "dtc-booth", "booth-noir": "dtc-booth", "booth-white": "dtc-booth",
		"herbary": "dtc-apothecary", "herbary-noir": "dtc-apothecary", "herbary-white": "dtc-apothecary",
		"grocer": "dtc-grocer", "grocer-noir": "dtc-grocer", "grocer-white": "dtc-grocer",
		"cellar": "dtc-cellar", "cellar-day": "dtc-cellar", "cellar-white": "dtc-cellar",
		"basecamp": "dtc-basecamp", "basecamp-noir": "dtc-basecamp", "basecamp-white": "dtc-basecamp",
		"toybox": "dtc-toybox", "toybox-noir": "dtc-toybox", "toybox-white": "dtc-toybox",
		"paperie": "dtc-paperie", "paperie-noir": "dtc-paperie", "paperie-white": "dtc-paperie",
		"mono": "dtc-mono", "mono-noir": "dtc-mono", "mono-white": "dtc-mono",
		"flatlay": "dtc-flatlay", "flatlay-noir": "dtc-flatlay", "flatlay-white": "dtc-flatlay",
		"flyer": "dtc-flyer", "flyer-noir": "dtc-flyer", "flyer-white": "dtc-flyer",
		"lightbox": "dtc-lightbox", "lightbox-day": "dtc-lightbox", "lightbox-white": "dtc-lightbox",
	}
	seen := map[string]bool{}
	for _, th := range Themes {
		if th.Category != ThemeCategoryDTC {
			continue
		}
		seen[th.ID] = true
		layout, ok := wantLayouts[th.ID]
		if !ok {
			t.Fatalf("unexpected dtc theme %q", th.ID)
		}
		if got := layoutForTheme(th.ID); got != layout {
			t.Fatalf("layoutForTheme(%q) = %q, want %q", th.ID, got, layout)
		}
		if themeAccentDefault[th.ID] == "" {
			t.Fatalf("theme %q missing accent default", th.ID)
		}
		if themeRadiusDefault[th.ID] == "" {
			t.Fatalf("theme %q missing radius default", th.ID)
		}
		if themeBgDefault[th.ID] == "" {
			t.Fatalf("theme %q missing bg default", th.ID)
		}
		if themeDescEN[th.ID] == "" {
			t.Fatalf("theme %q missing EN description", th.ID)
		}
	}
	if len(seen) != len(wantLayouts) {
		t.Fatalf("dtc themes = %d, want %d（%v）", len(seen), len(wantLayouts), seen)
	}
	for _, layout := range []string{"dtc-flagship", "dtc-solo", "dtc-lookbook"} {
		if !isDTCLayout(layout) {
			t.Fatalf("isDTCLayout(%q) = false", layout)
		}
		if _, ok := themeSkeletons[layout]; !ok {
			t.Fatalf("themeSkeletons missing %q", layout)
		}
		if themeSkeletonDescEN[layout] == "" {
			t.Fatalf("themeSkeletonDescEN missing %q", layout)
		}
	}
	if isDTCLayout("topbar") || isDTCLayout("factory-catalog") {
		t.Fatal("isDTCLayout 误报非 dtc 骨架")
	}
	// 选择器聚合：dtc 皮肤按骨架归族（族=骨架 id）。
	if got := familyForTheme("cream"); got != "dtc-flagship" {
		t.Fatalf("familyForTheme(cream) = %q", got)
	}
	// 分类 chips：dtc 出现且列在 factory 之后、general 之前。
	present := themeCategoriesPresent()
	joined := strings.Join(present, ",")
	if !strings.Contains(joined, ThemeCategoryFactory+","+ThemeCategoryDTC+","+ThemeCategoryGeneral) {
		t.Fatalf("themeCategoriesPresent() = %v, want …factory,dtc,general", present)
	}
}

// ---------- 槽子集映射 ----------

// 独立站骨架 → 消费槽子集：flagship 全量、solo 去系列入口、lookbook 只吃图集+CTA；
// 同族皮肤共享同一套槽；未登记的新 dtc 骨架回落全量。
func TestDTCLayoutSlotSubsets(t *testing.T) {
	want := map[string][]string{
		"cream":     {"hero.visual", factoryStatsSettingKey, factoryCategoriesEnabledKey, factoryGallerySettingKey, dtcTestimonialsSettingKey, factoryFAQSettingKey, factoryCTASettingKey},
		"solowhite": {"hero.visual", factoryStatsSettingKey, factoryGallerySettingKey, dtcTestimonialsSettingKey, factoryFAQSettingKey, factoryCTASettingKey},
		"galleria":  {"hero.visual", factoryGallerySettingKey, factoryCTASettingKey},
	}
	for theme, keys := range want {
		specs := themeOptionSpecs(theme)
		got := make([]string, 0, len(specs))
		for _, spec := range specs {
			got = append(got, spec.Key)
		}
		if strings.Join(got, ",") != strings.Join(keys, ",") {
			t.Fatalf("themeOptionSpecs(%q) = %v, want %v", theme, got, keys)
		}
	}
	for _, pair := range [][2]string{{"cream", "inknavy"}, {"solowhite", "coralpop"}, {"galleria", "blackbox"}} {
		if len(themeOptionSpecs(pair[0])) != len(themeOptionSpecs(pair[1])) {
			t.Fatalf("同族皮肤 %v 槽数不一致", pair)
		}
	}
	// 三骨架都声明按语种的槽（lookbook 的 CTA 也分语种）→ 外观页要语种切换条。
	for _, theme := range []string{"cream", "solowhite", "galleria"} {
		if !themeOptionsLocalized(theme) {
			t.Fatalf("themeOptionsLocalized(%q) = false", theme)
		}
	}
	// 未登记的 dtc 骨架回落全量（宁可多渲染配置项）。
	if got := dtcLayoutThemeOptions("dtc-future"); len(got) != len(dtcThemeOptions) {
		t.Fatalf("未登记骨架应回落全量，got %d", len(got))
	}
	// 评价槽是独立站专属：工厂/内容主题不声明。
	for _, theme := range []string{"showroom", "editorial"} {
		for _, spec := range themeOptionSpecs(theme) {
			if spec.Key == dtcTestimonialsSettingKey {
				t.Fatalf("theme %q 不应声明 dtc.testimonials", theme)
			}
		}
	}
}

// ---------- 解析与派生 ----------

func TestParseDTCTestimonials(t *testing.T) {
	got := parseDTCTestimonials(`[
		{"name":"Sarah M.","region":"US","quote":"回购了第二套。"},
		{"name":"","quote":"没名字"},
		{"name":"没评价"},
		{"name":"Ken","quote":"good","region":""}
	]`)
	if len(got) != 2 || got[0].Region != "US" || got[1].Name != "Ken" || got[1].Region != "" {
		t.Fatalf("parseDTCTestimonials = %#v", got)
	}
	for _, raw := range []string{"", "not json", "null", "[]"} {
		if out := parseDTCTestimonials(raw); len(out) != 0 {
			t.Fatalf("parseDTCTestimonials(%q) = %#v, want empty", raw, out)
		}
	}
	// 超出 6 条截断。
	many := `[` + strings.Repeat(`{"name":"a","quote":"b"},`, 7) + `{"name":"a","quote":"b"}]`
	if out := parseDTCTestimonials(many); len(out) != maxDTCTestimonials {
		t.Fatalf("testimonials 应截断到 %d 条，got %d", maxDTCTestimonials, len(out))
	}
}

// 卖点分解取数：图 × 规格逐对配组（都齐才成块，最多 4 块），余图归使用场景。
func TestDTCSoloSelling(t *testing.T) {
	gallery := []string{"/a.webp", "/b.webp", "/c.webp", "/d.webp", "/e.webp", "/f.webp"}
	specs := []FieldPair{{K: "容量", V: "350ml"}, {K: "材质", V: "白瓷"}}
	selling, scenes := dtcSoloSelling(gallery, specs)
	if len(selling) != 2 || selling[0].Img != "/a.webp" || selling[0].Title != "容量" || selling[1].Note != "白瓷" {
		t.Fatalf("selling = %#v", selling)
	}
	if len(scenes) != 4 || scenes[0] != "/c.webp" {
		t.Fatalf("scenes = %#v", scenes)
	}
	// 规格多于图：块数跟图走；无余图则场景为空。
	selling, scenes = dtcSoloSelling(gallery[:1], append(specs, FieldPair{K: "产地", V: "景德镇"}))
	if len(selling) != 1 || len(scenes) != 0 {
		t.Fatalf("selling/scenes = %#v / %#v", selling, scenes)
	}
	// 没规格 → 不硬凑卖点块，整个图集归场景……不：无块时余图=全部图。
	selling, scenes = dtcSoloSelling(gallery[:2], nil)
	if len(selling) != 0 || len(scenes) != 2 {
		t.Fatalf("无规格时 selling=%#v scenes=%#v", selling, scenes)
	}
	// 超过 4 块截断。
	long := []FieldPair{{K: "1", V: "a"}, {K: "2", V: "b"}, {K: "3", V: "c"}, {K: "4", V: "d"}, {K: "5", V: "e"}}
	selling, scenes = dtcSoloSelling(gallery, long)
	if len(selling) != maxDTCSellingBlocks || len(scenes) != 2 {
		t.Fatalf("截断错误：selling=%d scenes=%d", len(selling), len(scenes))
	}
}

// ---------- 外观表单（渲染 + 保存） ----------

// dtc 主题的外观弹窗：渲染 dtc 槽子集（含评价录入行、无假占位），
// 不消费的工厂槽（流程/行业）不渲染；lookbook 只出图集+CTA。
func TestAppearanceThemeOptionsFormDTC(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting("theme", "cream"); err != nil {
		t.Fatal(err)
	}
	cookie, _ := themeOptsAdminSession(t, s)
	body := themeOptsAppearanceHTML(t, s, cookie, "")
	for _, f := range []string{
		`name="hero_visual"`, `name="factory_stat_num_0"`, `name="factory_categories_on"`,
		`name="factory_gallery"`, `name="dtc_testi_name_0"`, `name="dtc_testi_region_0"`, `name="dtc_testi_quote_0"`,
		`name="factory_faq_q_0"`, `name="factory_cta_title"`,
		`name="theme_opts" value="factory"`,
		`name="theme_opt_slot" value="dtc.testimonials"`,
		`name="theme_opt_slot" value="factory.stats"`,
	} {
		if !strings.Contains(body, f) {
			t.Fatalf("cream 弹窗缺少 %s", f)
		}
	}
	for _, f := range []string{`name="factory_process_on"`, `name="factory_industry_name_0"`} {
		if strings.Contains(body, f) {
			t.Fatalf("dtc 不消费的工厂槽不应渲染：%s", f)
		}
	}
	// 评价录入行没有任何默认占位评价（红线：绝不编造）。
	if strings.Contains(body, `name="dtc_testi_quote_0" value=`) {
		t.Fatal("评价 quote 不应有预填值")
	}

	if err := s.store.SetSetting("theme", "galleria"); err != nil { // dtc-lookbook
		t.Fatal(err)
	}
	body = themeOptsAppearanceHTML(t, s, cookie, "")
	for _, f := range []string{`name="factory_gallery"`, `name="factory_cta_title"`} {
		if !strings.Contains(body, f) {
			t.Fatalf("galleria 弹窗缺少 %s", f)
		}
	}
	for _, f := range []string{`name="dtc_testi_name_0"`, `name="factory_stat_num_0"`, `name="factory_categories_on"`, `name="factory_faq_q_0"`} {
		if strings.Contains(body, f) {
			t.Fatalf("lookbook 不消费的槽不应渲染：%s", f)
		}
	}
}

// 评价槽保存：默认语种裸键、en 走 ::en、name/quote 缺一丢行、留空清键；
// 未登记评价槽的保存（工厂/旧表单）绝不触碰评价键。
func TestAppearanceSaveDTCTestimonials(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting("theme", "cream"); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := themeOptsAdminSession(t, s)
	slots := []string{factoryStatsSettingKey, factoryCategoriesEnabledKey, factoryGallerySettingKey, dtcTestimonialsSettingKey, factoryFAQSettingKey, factoryCTASettingKey}
	post := func(form url.Values) {
		t.Helper()
		form.Set("_csrf", csrf)
		form.Set("theme", "cream")
		req := httptest.NewRequest(http.MethodPost, "https://example.test/admin/settings/appearance", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("save status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}

	post(url.Values{
		"theme_opts":         {"factory"},
		"theme_opt_slot":     slots,
		"dtc_testi_name_0":   {"Sarah M."},
		"dtc_testi_region_0": {"US"},
		"dtc_testi_quote_0":  {"质感远超预期。"},
		"dtc_testi_name_1":   {"只有名字没评价"}, // 应被丢弃
		"dtc_testi_quote_2":  {"只有评价没名字"}, // 应被丢弃
	})
	if got := s.store.Setting(dtcTestimonialsSettingKey); got != `[{"name":"Sarah M.","region":"US","quote":"质感远超预期。"}]` {
		t.Fatalf("dtc.testimonials = %q", got)
	}

	// en 语种：落 ::en，裸键不动。
	post(url.Values{
		"theme_opts":        {"factory"},
		"theme_opt_slot":    slots,
		"lang":              {"en"},
		"dtc_testi_name_0":  {"Ken"},
		"dtc_testi_quote_0": {"Great quality."},
	})
	if got := s.store.Setting(dtcTestimonialsSettingKey + "::en"); got != `[{"name":"Ken","quote":"Great quality."}]` {
		t.Fatalf("dtc.testimonials::en = %q", got)
	}
	if got := s.store.Setting(dtcTestimonialsSettingKey); !strings.Contains(got, "Sarah") {
		t.Fatalf("en 保存不应动默认语种键：%q", got)
	}

	// 留空 → 清键（评价区不渲染）。
	post(url.Values{"theme_opts": {"factory"}, "theme_opt_slot": slots})
	if got := s.store.Setting(dtcTestimonialsSettingKey); got != "" {
		t.Fatalf("dtc.testimonials = %q, want empty", got)
	}

	// 未登记评价槽（工厂主题/旧表单的槽集合）：评价键纹丝不动。
	if err := s.store.SetSetting(dtcTestimonialsSettingKey, `[{"name":"守住","quote":"评价"}]`); err != nil {
		t.Fatal(err)
	}
	post(url.Values{"theme_opts": {"factory"}, "theme_opt_slot": {factoryStatsSettingKey}})
	if got := s.store.Setting(dtcTestimonialsSettingKey); !strings.Contains(got, "守住") {
		t.Fatalf("未登记评价槽的保存动了评价键：%q", got)
	}
}

// ---------- AI 写路径（PATCH /site-profile 的 dtc_testimonials） ----------

func TestAPISiteProfileDTCTestimonials(t *testing.T) {
	s, token := newTestAutomationServer(t, apiScopeSiteWrite+","+apiScopeSiteRead)
	patch := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPatch, "/api/admin/v1/site-profile", bytes.NewReader([]byte(body)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.apiUpdateSiteProfile(w, r)
		return w
	}

	w := patch(`{"items":[
		{"lang":"zh","dtc_testimonials":[{"name":"Sarah M.","region":"US","quote":"回购了第二套。"}]},
		{"lang":"en","dtc_testimonials":[{"name":"Ken","quote":"Great."}]}
	]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := s.store.Setting(dtcTestimonialsSettingKey); got != `[{"name":"Sarah M.","region":"US","quote":"回购了第二套。"}]` {
		t.Fatalf("dtc.testimonials = %q", got)
	}
	if got := s.store.Setting(dtcTestimonialsSettingKey + "::en"); got != `[{"name":"Ken","quote":"Great."}]` {
		t.Fatalf("dtc.testimonials::en = %q", got)
	}

	// GET 读出。
	item := s.apiSiteProfileItem("zh")
	if len(item.DTCTestimonials) != 1 || item.DTCTestimonials[0].Name != "Sarah M." {
		t.Fatalf("item.DTCTestimonials = %#v", item.DTCTestimonials)
	}

	// 传 [] 清除。
	if w := patch(`{"lang":"zh","dtc_testimonials":[]}`); w.Code != http.StatusOK {
		t.Fatalf("clear status = %d", w.Code)
	}
	if got := s.store.Setting(dtcTestimonialsSettingKey); got != "" {
		t.Fatalf("dtc.testimonials = %q, want empty after []", got)
	}

	// 非法格式：400 且不落库。
	for _, bad := range []string{
		`{"lang":"zh","dtc_testimonials":[{"name":"没评价"}]}`,
		`{"lang":"zh","dtc_testimonials":"not-an-array"}`,
	} {
		if w := patch(bad); w.Code != http.StatusBadRequest {
			t.Fatalf("bad payload %s → status %d, want 400（body %s）", bad, w.Code, w.Body.String())
		}
	}
	if got := s.store.Setting(dtcTestimonialsSettingKey); got != "" {
		t.Fatalf("400 后不应落库：%q", got)
	}

	// GET /theme-options：dtc 主题返回 family=dtc + testimonials 槽。
	if err := s.store.SetSetting("theme", "cream"); err != nil {
		t.Fatal(err)
	}
	resp := s.apiThemeOptionsResponse("zh")
	if resp["family"] != ThemeCategoryDTC || resp["layout"] != "dtc-flagship" {
		t.Fatalf("theme-options family/layout = %v/%v", resp["family"], resp["layout"])
	}
	slots, _ := resp["slots"].([]apiThemeOptionSlot)
	found := false
	for _, slot := range slots {
		if slot.Key == dtcTestimonialsSettingKey {
			found = true
			if slot.Type != themeOptTestimonials || !slot.Localized || slot.Configured {
				t.Fatalf("testimonials slot = %#v", slot)
			}
		}
	}
	if !found {
		t.Fatal("theme-options 缺少 dtc.testimonials 槽")
	}
}

// ---------- 首页渲染 ----------

// 三骨架首页：区块跟数据走——评价未配置不渲染（绝不占位），FAQ/CTA 回落零售口径默认；
// solo 吃主打商品 specs 与 gallery 配组；lookbook 无图集回落商品封面墙。
func TestDTCHomeSectionsRendering(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SeedFactoryDemoProducts(); err != nil {
		t.Fatal(err)
	}
	// 演示种子带 hero.visual=image；本测试从「默认动画」模式起步（dtc 轻动画要能出）。
	if err := s.store.SetSetting("hero.visual", ""); err != nil {
		t.Fatal(err)
	}
	fetch := func(path string) string {
		s.clearGeneratedCaches()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	setTheme := func(id string) {
		t.Helper()
		if err := s.store.SetSetting("theme", id); err != nil {
			t.Fatal(err)
		}
	}

	// 旗舰：系列大卡 + 畅销栅格 + 品牌数字（演示种子带 factory.stats/gallery）
	// + FAQ 零售默认 + CTA 默认；评价未配置 → 区块不渲染。
	setTheme("cream")
	body := fetch("/zh/")
	for _, want := range []string{
		`class="df-wrap"`, `class="df-series-grid"`, `class="df-grid"`,
		`class="dt-stats"`, `class="f-gallery"`, // 种子 stats/gallery
		"如何下单？", "联系我们", // dtc FAQ/CTA 零售口径默认
		"/products/cat/mechanical-parts", `id="dtc-contact"`, `class="dt-animsvg"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("flagship 首页缺少 %q", want)
		}
	}
	if strings.Contains(body, "dt-testis") {
		t.Fatal("评价未配置时不应渲染评价区（绝不放假评价）")
	}
	if strings.Contains(body, "起订量（MOQ）怎么算？") {
		t.Fatal("dtc FAQ 默认不应是工厂 MOQ 口径")
	}

	// 品牌数字清空 → 品牌故事回落站点简介块。
	if err := s.store.SetSetting(factoryStatsSettingKey, ""); err != nil {
		t.Fatal(err)
	}
	body = fetch("/zh/")
	if !strings.Contains(body, `class="df-about"`) {
		t.Fatal("stats 未配置时品牌故事应回落站点简介")
	}

	// 配置真实评价 + 品牌数字 → 渲染；stats 顶掉简介回落。
	if err := s.store.SetSetting(dtcTestimonialsSettingKey, `[{"name":"Sarah M.","region":"US","quote":"质感远超预期。"}]`); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting(factoryStatsSettingKey, `[{"num":"2018","label":"品牌创立"}]`); err != nil {
		t.Fatal(err)
	}
	body = fetch("/zh/")
	for _, want := range []string{"dt-testis", "Sarah M.", "质感远超预期。", `class="dt-stats"`, "品牌创立"} {
		if !strings.Contains(body, want) {
			t.Fatalf("flagship 配置后缺少 %q", want)
		}
	}
	if strings.Contains(body, `class="df-about"`) {
		t.Fatal("配了品牌数字后不应再渲染简介回落块")
	}

	// 单品：主打商品 specs 出规格表；种子图集 3 张 × 5 规格 → 3 块卖点、无余图；
	// 5 张图 → 4 块卖点 + 使用场景余图；清空图集 → 卖点分解消失。
	setTheme("solowhite")
	body = fetch("/zh/")
	for _, want := range []string{`class="ds-wrap"`, `class="ds-spec-table"`, "dt-testis", `ds-points"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("solo 首页缺少 %q", want)
		}
	}
	if strings.Contains(body, `ds-scenes"`) {
		t.Fatal("图集 3 张全部进卖点分解时不应有使用场景余图")
	}
	if err := s.store.SetSetting(factoryGallerySettingKey, `["/uploads/l1.webp","/uploads/l2.webp","/uploads/l3.webp","/uploads/l4.webp","/uploads/l5.webp"]`); err != nil {
		t.Fatal(err)
	}
	body = fetch("/zh/")
	if !strings.Contains(body, `ds-points"`) || !strings.Contains(body, `ds-scenes"`) {
		t.Fatal("5 张图应渲染卖点分解 + 使用场景余图")
	}
	if err := s.store.SetSetting(factoryGallerySettingKey, ""); err != nil {
		t.Fatal(err)
	}
	body = fetch("/zh/")
	if strings.Contains(body, `ds-points"`) {
		t.Fatal("未配置 gallery 时不应渲染卖点分解")
	}
	if err := s.store.SetSetting(factoryGallerySettingKey, `["/uploads/l1.webp","/uploads/l2.webp","/uploads/l3.webp","/uploads/l4.webp","/uploads/l5.webp"]`); err != nil {
		t.Fatal(err)
	}

	// 画册：有图集 → 大图墙 + 系列陈列；评价槽不消费（配置了也不渲染——极少文字）。
	setTheme("galleria")
	body = fetch("/zh/")
	for _, want := range []string{`class="dl-wrap"`, `class="dl-wall"`, `dl-series"`, `class="dl-tile-name"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("lookbook 首页缺少 %q", want)
		}
	}
	if strings.Contains(body, "dt-testis") {
		t.Fatal("lookbook 不消费评价槽（极少文字），配置了也不渲染")
	}
	// 无图集 → 回落商品封面墙（首屏），不重复陈列第一组。
	if err := s.store.SetSetting(factoryGallerySettingKey, ""); err != nil {
		t.Fatal(err)
	}
	body = fetch("/zh/")
	if !strings.Contains(body, "dl-wall-products") {
		t.Fatal("无图集时应回落商品封面墙")
	}

	// 商品详情：dtc 骨架同走 product_detail 专属模板（询盘区照用）。
	body = fetch("/zh/products/cnc-machined-parts/")
	if !strings.Contains(body, `class="pd-top"`) {
		t.Fatal("dtc 骨架下商品详情应走 product_detail 模板")
	}
}

// 第二批 20 个 DTC 骨架共享同一条真实数据契约：
//   - 声明分类槽的 18 个骨架只读 product 分类卡（FactoryCats），URL 使用内容类型真实前缀；
//   - column / monthbox 不声明、也不渲染分类；
//   - 分类开关关闭后，所有消费该槽的骨架都必须立即隐藏分类导航；
//   - 每个骨架在真实首页路径上都能渲染真实商品，而非仅在主题卡样例里可见。
func TestDTCNewLayoutsProductCategoryContract(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SeedFactoryDemoProducts(); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting(factoryCategoriesEnabledKey, "1"); err != nil {
		t.Fatal(err)
	}
	type layoutCase struct {
		theme   string
		marker  string
		hasCats bool
	}
	cases := []layoutCase{
		{"vitrine", `class="vtr-wrap"`, true},
		{"journalshop", `class="jnl-wrap"`, true},
		{"catalogue", `class="ctg-wrap"`, true},
		{"bazaar", `class="bzr-wrap"`, true},
		{"column", `class="clm-wrap"`, false},
		{"swissgrid", `class="swg-wrap"`, true},
		{"catwalk", `class="rwy-wrap"`, true},
		{"handcraft", `class="atl-wrap"`, true},
		{"monthbox", `class="mbx-wrap"`, false},
		{"booth", `class="bth-wrap"`, true},
		{"herbary", `class="apo-wrap"`, true},
		{"grocer", `class="gcr-wrap"`, true},
		{"cellar", `class="clr-wrap"`, true},
		{"basecamp", `class="bcp-wrap"`, true},
		{"toybox", `class="tbx-wrap"`, true},
		{"paperie", `class="ppr-wrap"`, true},
		{"mono", `class="mno-wrap"`, true},
		{"flatlay", `class="flt-wrap"`, true},
		{"flyer", `class="flr-wrap"`, true},
		{"lightbox", `class="lbx-wrap"`, true},
	}
	fetchHome := func(theme string) string {
		t.Helper()
		if err := s.store.SetSetting("theme", theme); err != nil {
			t.Fatal(err)
		}
		s.clearGeneratedCaches()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/zh/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("theme %s: GET /zh/ status=%d body=%s", theme, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	hasOption := func(theme, key string) bool {
		for _, spec := range themeOptionSpecs(theme) {
			if spec.Key == key {
				return true
			}
		}
		return false
	}
	optionKeys := func(theme string) []string {
		specs := themeOptionSpecs(theme)
		out := make([]string, 0, len(specs))
		for _, spec := range specs {
			out = append(out, spec.Key)
		}
		return out
	}

	for _, tc := range cases {
		body := fetchHome(tc.theme)
		if !strings.Contains(body, tc.marker) {
			t.Errorf("theme %s: 真实首页缺骨架标记 %s", tc.theme, tc.marker)
		}
		if !strings.Contains(body, "精密 CNC 铝合金加工件") {
			t.Errorf("theme %s: 真实首页没有装载已发布商品", tc.theme)
		}
		if got := hasOption(tc.theme, factoryCategoriesEnabledKey); got != tc.hasCats {
			t.Errorf("theme %s: 分类配置槽=%v, want %v", tc.theme, got, tc.hasCats)
		}
		wantSlots := []string{"hero.visual", factoryStatsSettingKey}
		switch tc.theme {
		case "column", "monthbox":
			// 这两个骨架刻意没有分类和图集区块。
		case "catalogue":
			wantSlots = append(wantSlots, factoryCategoriesEnabledKey)
		default:
			wantSlots = append(wantSlots, factoryCategoriesEnabledKey, factoryGallerySettingKey)
		}
		wantSlots = append(wantSlots, dtcTestimonialsSettingKey, factoryFAQSettingKey, factoryCTASettingKey)
		if got := optionKeys(tc.theme); strings.Join(got, ",") != strings.Join(wantSlots, ",") {
			t.Errorf("theme %s: 配置槽=%v, want %v", tc.theme, got, wantSlots)
		}
		hasProductCat := strings.Contains(body, `/zh/products/cat/mechanical-parts`)
		if hasProductCat != tc.hasCats {
			t.Errorf("theme %s: 商品分类导航=%v, want %v", tc.theme, hasProductCat, tc.hasCats)
		}
		// 新骨架的分类导航不能再拼文章分类路由。
		if strings.Contains(body, `href="/zh/category/mechanical-parts`) {
			t.Errorf("theme %s: 商品分类误用了文章分类路由", tc.theme)
		}
	}

	// 真实商品分类 URL 必须可访问并返回该分类下的真实商品。
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/zh/products/cat/mechanical-parts/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "精密 CNC 铝合金加工件") {
		t.Fatalf("商品分类路由 status=%d, body=%s", rec.Code, rec.Body.String())
	}

	// 同一个共享开关必须控制全部 18 个消费分类槽的骨架。
	if err := s.store.SetSetting(factoryCategoriesEnabledKey, "0"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		if !tc.hasCats {
			continue
		}
		if body := fetchHome(tc.theme); strings.Contains(body, `/products/cat/`) {
			t.Errorf("theme %s: 分类开关关闭后仍渲染商品分类导航", tc.theme)
		}
	}
}

// 第二批 20 个 DTC 骨架必须严格服从同一套 Hero 三态：
// image 只使用站点 HeroImage，svg 只使用站点 HeroSVG，默认/动画态始终使用
// dtc_hero_anim。商品封面与图集可以出现在后续陈列区，但不能顶替 Hero。
func TestSecondBatchDTCHeroVisualContract(t *testing.T) {
	s := newTestPublicServer(t, "")
	set := func(key, value string) {
		t.Helper()
		if err := s.store.SetSetting(key, value); err != nil {
			t.Fatalf("SetSetting(%q): %v", key, err)
		}
	}
	set(enabledContentTypesKey, "product")
	if err := s.store.SeedFactoryDemoProducts(); err != nil {
		t.Fatal(err)
	}

	render := func(theme string) string {
		t.Helper()
		set("theme", theme)
		s.clearGeneratedCaches()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/zh/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s home status = %d, body = %s", theme, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	const (
		heroImage  = "/uploads/dtc-hero-contract.webp"
		svgMarker  = `data-dtc-contract="custom-svg"`
		animMarker = `data-dtc-hero-visual="animation"`
	)
	set("hero.image", heroImage)
	set("hero.svg", `<svg viewBox="0 0 10 10" `+svgMarker+`></svg>`)

	set("hero.visual", "image")
	for _, theme := range secondBatchDTCThemes {
		body := render(theme)
		if got := strings.Count(body, `src="`+heroImage+`"`); got != 1 {
			t.Errorf("%s image 模式 HeroImage 出现 %d 次，want 1", theme, got)
		}
		if strings.Contains(body, svgMarker) || strings.Contains(body, animMarker) {
			t.Errorf("%s image 模式混入了 SVG 或默认动画", theme)
		}
	}

	set("hero.visual", "svg")
	for _, theme := range secondBatchDTCThemes {
		body := render(theme)
		if got := strings.Count(body, svgMarker); got != 1 {
			t.Errorf("%s svg 模式 HeroSVG 出现 %d 次，want 1", theme, got)
		}
		if strings.Contains(body, `src="`+heroImage+`"`) || strings.Contains(body, animMarker) {
			t.Errorf("%s svg 模式混入了 HeroImage 或默认动画", theme)
		}
	}

	for _, visual := range []string{"", "anim1", "anim2"} {
		set("hero.visual", visual)
		for _, theme := range secondBatchDTCThemes {
			body := render(theme)
			if got := strings.Count(body, animMarker); got != 1 {
				t.Errorf("%s %q 模式默认动画出现 %d 次，want 1", theme, visual, got)
			}
			if strings.Contains(body, `src="`+heroImage+`"`) || strings.Contains(body, svgMarker) {
				t.Errorf("%s %q 模式泄漏了未选中的 HeroImage/HeroSVG", theme, visual)
			}
		}
	}
}

func TestDTCSectionLabelsAreLocalized(t *testing.T) {
	s := newTestPublicServer(t, "")
	keys := []string{
		"dtc.label.products",
		"dtc.label.story",
		"dtc.label.scenes",
		"dtc.label.reviews",
		"dtc.label.guide",
		"dtc.label.look",
	}
	for _, lang := range []string{"zh", "en", "vi", "id", "th"} {
		tr := s.i18n.Tr(lang, s.defaultLang())
		for _, key := range keys {
			if got := tr.T(key); got == "" || got == key {
				t.Errorf("%s: %s 未本地化，got %q", lang, key, got)
			}
		}
	}
}

// ---------- 站型预设 ----------

// dtc 站型：normalize 合法、预设与工厂站一致（启用商品 + 导航挂商品 + brand=text + 记录 site.kind）。
func TestApplySiteKindPresetDTC(t *testing.T) {
	if got := normalizeSiteKind(" dtc "); got != siteKindDTC {
		t.Fatalf("normalizeSiteKind(dtc) = %q", got)
	}
	s := newTestPublicServer(t, "")
	if err := applySiteKindPreset(s.store, s.i18n, siteKindDTC, true); err != nil {
		t.Fatalf("applySiteKindPreset: %v", err)
	}
	if got := siteKindOf(s.store); got != siteKindDTC {
		t.Fatalf("site.kind = %q", got)
	}
	if !parseEnabledTypes(s.store.Setting(enabledContentTypesKey))["product"] {
		t.Fatal("dtc 预设应启用 product 类型")
	}
	if got := s.store.Setting("site.brand"); got != "text" {
		t.Fatalf("site.brand = %q, want text", got)
	}
	rows := parseMenuRows(s.store.Setting("nav_menu"))
	found := false
	for _, row := range rows {
		if row.URL == "/products" {
			found = true
		}
	}
	if !found {
		t.Fatalf("导航缺少商品入口：%v", rows)
	}
	// 演示商品已种（seedDemo=true）。
	posts, _ := s.store.ListPublishedByType("product", "zh", 0, 0, 5)
	if len(posts) == 0 {
		t.Fatal("dtc 预设（带演示数据）应种演示商品")
	}
}

// ---------- 自动化文档 ----------

// SKILL 文档：独立站主题清单 + dtc_testimonials 字段 + 「绝不编造」红线成文。
func TestAutomationDocsDTC(t *testing.T) {
	doc := automationThemeOptionsDocString()
	for _, want := range []string{
		"cream", "solowhite", "galleria",
		"`dtc_testimonials`",
		"绝不编造",
		"未配置时评价区不渲染",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("SKILL 主题配置小节缺少 %q", want)
		}
	}
	schema := automationDTCTestimonialsSchema()
	desc, _ := schema["description"].(string)
	if !strings.Contains(desc, "绝不编造") || !strings.Contains(desc, "site:write") {
		t.Fatalf("dtc_testimonials schema 描述缺红线：%q", desc)
	}
}
