package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// ---------- schema 声明 ----------

// 工厂主题声明 hero + 工厂族数据槽；内容主题只有 hero 槽。
func TestThemeOptionSpecs(t *testing.T) {
	factory := themeOptionSpecs("showroom") // factory-showcase 骨架
	keys := make([]string, 0, len(factory))
	for _, spec := range factory {
		keys = append(keys, spec.Key)
	}
	want := []string{
		"hero.visual",
		factoryStatsSettingKey, factoryProcessSettingKey,
		factoryCategoriesEnabledKey, factoryIndustriesSettingKey,
		factoryGallerySettingKey, factoryFAQSettingKey, factoryCTASettingKey,
	}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("factory specs = %v, want %v", keys, want)
	}
	for _, id := range []string{"industrial", "gunmetal"} { // 同族（两骨架）共享同一套槽
		if got := themeOptionSpecs(id); len(got) != len(factory) {
			t.Fatalf("theme %q specs = %d, want %d（同族共享）", id, len(got), len(factory))
		}
	}
	content := themeOptionSpecs("editorial")
	if len(content) != 1 || content[0].Type != themeOptHero {
		t.Fatalf("content theme specs = %+v, want 仅 hero 槽", content)
	}
	if themeOptionsLocalized("editorial") || !themeOptionsLocalized("showroom") {
		t.Fatal("themeOptionsLocalized: 工厂族应为 true、内容主题应为 false")
	}
}

// 骨架 → 消费槽子集映射：spec 共用一份，映射只挑子集（盘点自 home_factory_*.html
// 与 header/footer 分支的实际渲染）；未登记的新骨架回落全量。
func TestFactoryLayoutSlotSubsets(t *testing.T) {
	all := []string{
		factoryStatsSettingKey, factoryProcessSettingKey, factoryCategoriesEnabledKey,
		factoryIndustriesSettingKey, factoryGallerySettingKey, factoryFAQSettingKey, factoryCTASettingKey,
	}
	want := map[string][]string{
		"factory-catalog":     all,
		"factory-showcase":    all,
		"factory-onepage":     {factoryStatsSettingKey, factoryProcessSettingKey, factoryFAQSettingKey, factoryCTASettingKey},
		"factory-solutions":   {factoryStatsSettingKey, factoryProcessSettingKey, factoryIndustriesSettingKey, factoryCTASettingKey},
		"factory-engineering": {factoryStatsSettingKey, factoryCategoriesEnabledKey, factoryCTASettingKey},
		"factory-trade":       all,
		"factory-sidebar":     all, // 分类树在侧栏竖栏（header 分支）消费
		"factory-vision":      all, // CTA 在页脚（footer 分支复用 factory_cta）；hero 无图回落 gallery 首图
		"factory-herofold":    all, // hero 无图回落 gallery 首图
	}
	if len(factoryLayoutSlots) != len(want) {
		t.Fatalf("factoryLayoutSlots 登记了 %d 个骨架, want %d", len(factoryLayoutSlots), len(want))
	}
	keysOf := func(specs []ThemeOptionSpec) string {
		keys := make([]string, 0, len(specs))
		for _, spec := range specs {
			keys = append(keys, spec.Key)
		}
		return strings.Join(keys, ",")
	}
	for layout, keys := range want {
		if got := keysOf(factoryLayoutThemeOptions(layout)); got != strings.Join(keys, ",") {
			t.Fatalf("%s 消费槽 = %s, want %s", layout, got, strings.Join(keys, ","))
		}
	}
	// 每个注册进 themeLayouts 的工厂骨架都必须在映射里登记（防新骨架静默回落全量）。
	for theme, layout := range themeLayouts {
		if isFactoryLayout(layout) {
			if _, ok := factoryLayoutSlots[layout]; !ok {
				t.Fatalf("主题 %s 的骨架 %s 未登记消费槽映射", theme, layout)
			}
		}
	}
	// 未登记的新骨架回落全量（宁多勿丢）。
	if got := keysOf(factoryLayoutThemeOptions("factory-future")); got != strings.Join(all, ",") {
		t.Fatalf("未登记骨架应回落全量, got %s", got)
	}
	// 经 themeOptionSpecs 走主题：hero 槽恒在最前 + 骨架子集。
	for theme, keys := range map[string][]string{
		"phosphor": want["factory-engineering"], // 技术骨架代表
		"packline": want["factory-onepage"],     // 单页骨架代表
		"drafting": want["factory-solutions"],   // 方案骨架代表
	} {
		if got := keysOf(themeOptionSpecs(theme)); got != "hero.visual,"+strings.Join(keys, ",") {
			t.Fatalf("%s specs = %s, want hero+%s", theme, got, strings.Join(keys, ","))
		}
	}
}

// ---------- 解析与回落 ----------

func TestFactorySlotParsing(t *testing.T) {
	steps := parseFactorySteps(`[{"title":"询价",  "note":""},{"note":"只有说明"},{"title":1024}]`)
	if len(steps) != 3 || steps[0].Title != "询价" || steps[1].Note != "只有说明" || steps[2].Title != "1024" {
		t.Fatalf("parseFactorySteps = %#v", steps)
	}
	if got := parseFactorySteps(`[{},{},{},{},{"title":"第五步"}]`); len(got) != 4 {
		t.Fatalf("steps 超出 4 步应截断，got %d", len(got))
	}
	qa := parseFactoryQAs(`[{"q":"Q1","a":"A1"},{"q":"","a":"缺问"},{"q":"缺答"},{"q":"Q2","a":"A2"}]`)
	if len(qa) != 2 || qa[1].Q != "Q2" {
		t.Fatalf("parseFactoryQAs = %#v", qa)
	}
	ind := parseFactoryIndustries(`[{"name":"机械","note":"零件"},{"note":"没名字"},{"name":"照明"}]`)
	if len(ind) != 2 || ind[1].Name != "照明" || ind[1].Note != "" {
		t.Fatalf("parseFactoryIndustries = %#v", ind)
	}
	g := parseFactoryGallery(`["/a.webp","  ","/b.webp", 3]`)
	if len(g) != 3 || g[0] != "/a.webp" || g[2] != "3" {
		t.Fatalf("parseFactoryGallery = %#v", g)
	}
	for _, raw := range []string{"", "not json", "null", "[]"} {
		if out := parseFactoryQAs(raw); len(out) != 0 {
			t.Fatalf("parseFactoryQAs(%q) = %#v, want empty", raw, out)
		}
		if out := parseFactoryGallery(raw); len(out) != 0 {
			t.Fatalf("parseFactoryGallery(%q) = %#v, want empty", raw, out)
		}
	}
	pair := parseFactoryTextPair(`{"title":"报价","note":"当天回复"}`)
	if pair.Title != "报价" || pair.Note != "当天回复" {
		t.Fatalf("parseFactoryTextPair = %#v", pair)
	}
}

// 空值回落 i18n 默认、部分覆盖逐项回落、按语种覆盖、开关关闭。
func TestFactorySlotResolution(t *testing.T) {
	s := newTestPublicServer(t, "")

	// 零配置：流程/FAQ/行业全部吃 i18n 默认（zh）。
	steps := s.factoryProcessSteps("zh")
	if len(steps) != 4 || steps[0].Title != "询盘" || steps[3].Title != "出货" || steps[0].Num != "01" {
		t.Fatalf("默认流程 = %#v", steps)
	}
	if qa := s.factoryFAQList("zh"); len(qa) != 4 || !strings.Contains(qa[0].Q, "起订量") {
		t.Fatalf("默认 FAQ = %#v", qa)
	}
	if ind := s.factoryIndustryList("zh"); len(ind) != 4 || ind[0].Name != "机械制造" {
		t.Fatalf("默认行业 = %#v", ind)
	}
	if cta := s.factoryCTAText("zh"); cta.Title != "获取报价" || cta.Note == "" {
		t.Fatalf("默认 CTA = %#v", cta)
	}

	// 流程部分覆盖：只改第 2 步标题，其余字段逐项回落默认。
	if err := s.store.SetSetting(factoryProcessSettingKey, `[{},{"title":"免费打样"}]`); err != nil {
		t.Fatal(err)
	}
	steps = s.factoryProcessSteps("zh")
	if steps[0].Title != "询盘" || steps[1].Title != "免费打样" || steps[1].Note == "" || steps[3].Title != "出货" {
		t.Fatalf("部分覆盖流程 = %#v", steps)
	}

	// 按语种：en 覆盖只影响 en；zh 维持默认。
	if err := s.store.SetSetting(factoryCTASettingKey+"::en", `{"title":"Request a Quote"}`); err != nil {
		t.Fatal(err)
	}
	if cta := s.factoryCTAText("en"); cta.Title != "Request a Quote" || !strings.Contains(cta.Note, "quote") {
		t.Fatalf("en CTA = %#v（title 覆盖、note 回落 en i18n）", cta)
	}
	if cta := s.factoryCTAText("zh"); cta.Title != "获取报价" {
		t.Fatalf("zh CTA 不应被 en 覆盖影响 = %#v", cta)
	}

	// 开关：显式 "0" 关闭；其余值一律开。
	for _, key := range []string{factoryProcessEnabledKey, factoryFAQEnabledKey, factoryIndustriesEnabledKey, factoryCategoriesEnabledKey} {
		if !s.factorySectionEnabled(key) {
			t.Fatalf("%s 缺省应为开", key)
		}
		if err := s.store.SetSetting(key, "0"); err != nil {
			t.Fatal(err)
		}
		if s.factorySectionEnabled(key) {
			t.Fatalf("%s=0 应为关", key)
		}
	}
	if s.factoryFAQList("zh") != nil || s.factoryIndustryList("zh") != nil {
		t.Fatal("开关关闭后 FAQ/行业应返回 nil")
	}
}

// 分类入口卡区（零配置）：吃演示种子的商品分类；开关可整体关闭。
func TestFactoryCategoryCards(t *testing.T) {
	s := newTestPublicServer(t, "")
	if cards := s.factoryCategoryCards("zh"); len(cards) != 0 {
		t.Fatalf("product 未启用时应为空，got %v", cards)
	}
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SeedFactoryDemoProducts(); err != nil {
		t.Fatal(err)
	}
	cards := s.factoryCategoryCards("zh")
	if len(cards) != 3 {
		t.Fatalf("cards = %d, want 3（演示种子三个分类）", len(cards))
	}
	byName := map[string]FactoryCatCard{}
	for _, c := range cards {
		byName[c.Name] = c
	}
	mech := byName["机械配件"]
	if mech.Count != 2 || mech.URL != "/products/cat/mechanical-parts" || mech.Cover == "" {
		t.Fatalf("机械配件卡 = %#v", mech)
	}
	if err := s.store.SetSetting(factoryCategoriesEnabledKey, "0"); err != nil {
		t.Fatal(err)
	}
	if cards := s.factoryCategoryCards("zh"); cards != nil {
		t.Fatalf("开关关闭后应为 nil，got %v", cards)
	}
}

// ---------- 前台渲染 ----------

// 工厂首页：默认区块齐 + 编号连续；关闭流程条后不渲染且编号顺延；图集未配置不渲染、配置后出现。
func TestFactoryHomeSectionsRendering(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting("theme", "showroom"); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SeedFactoryDemoProducts(); err != nil {
		t.Fatal(err)
	}
	fetch := func() string {
		s.clearGeneratedCaches()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/zh/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("home status = %d", rec.Code)
		}
		return rec.Body.String()
	}

	body := fetch()
	for _, want := range []string{
		`class="f-cats"`, `class="f-industries"`, `data-factory-faq`, `class="f-gallery"`, // 新区块（图集吃种子）
		"起订量（MOQ）怎么算？", "机械制造", // FAQ/行业 i18n 默认
		"/products/cat/mechanical-parts",      // 分类卡链接（zh 前缀由 Tr.U 添加，包含即可）
		"/assets/covers/factory-workshop.svg", // 种子图集
		`class="f-process"`, "获取报价",           // 流程默认 + CTA i18n 默认
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("首页缺少 %q", want)
		}
	}

	// 目录骨架：编号连续（无 News 的空站会按实际渲染的区块编号）。
	if err := s.store.SetSetting("theme", "industrial"); err != nil {
		t.Fatal(err)
	}
	body = fetch()
	if !strings.Contains(body, `class="fc-wrap"`) {
		t.Fatal("industrial 应走 factory-catalog 骨架")
	}

	// 关闭流程条：区块消失。
	if err := s.store.SetSetting(factoryProcessEnabledKey, "0"); err != nil {
		t.Fatal(err)
	}
	body = fetch()
	if strings.Contains(body, `class="f-process"`) {
		t.Fatal("factory.process.enabled=0 时不应渲染流程条")
	}

	// 图集清空：区块消失（未配置不渲染，绝不占位）。
	if err := s.store.SetSetting(factoryGallerySettingKey, ""); err != nil {
		t.Fatal(err)
	}
	body = fetch()
	if strings.Contains(body, `class="f-gallery"`) {
		t.Fatal("factory.gallery 未配置时不应渲染图集")
	}

	// 文本覆盖立即生效（zh）。
	if err := s.store.SetSetting(factoryCTASettingKey, `{"title":"立即询盘","note":"24 小时内回复"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting(factoryFAQSettingKey, `[{"q":"支持定制吗？","a":"支持来图来样定制。"}]`); err != nil {
		t.Fatal(err)
	}
	body = fetch()
	if !strings.Contains(body, "立即询盘") || !strings.Contains(body, "24 小时内回复") {
		t.Fatal("CTA 自定义未生效")
	}
	if !strings.Contains(body, "支持定制吗？") || strings.Contains(body, "起订量（MOQ）怎么算？") {
		t.Fatal("FAQ 自定义应整组替换默认")
	}
}

// ---------- hero 默认动画（工厂骨架接入） ----------

// hero.visual=default 时有 hero 视觉位的工厂骨架渲染图纸机械动画（f-heroanim），
// 全屏骨架（vision/herofold）在无图可回落时也在文案右侧渲染动画；
// image/svg 模式与 gallery 回落维持既有行为，不渲染动画组件。
func TestFactoryHeroAnimRendering(t *testing.T) {
	s := newTestPublicServer(t, "")
	// 演示种子带 hero.visual=image + 演示图；本测试从「默认动画」模式起步
	// （存储里残留的 hero.image 不影响动画模式判定——按 visual 分流）。
	if err := s.store.SetSetting("hero.visual", ""); err != nil {
		t.Fatal(err)
	}
	fetch := func() string {
		s.clearGeneratedCaches()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/zh/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("home status = %d", rec.Code)
		}
		return rec.Body.String()
	}
	setTheme := func(id string) {
		t.Helper()
		if err := s.store.SetSetting("theme", id); err != nil {
			t.Fatal(err)
		}
	}

	// 骨架代表：展台（裸贴纹理）/ 单页（边卡）/ 侧栏（横幅）/ 沉浸与门楣（全屏无图回落时文案右侧）。
	for theme, marker := range map[string]string{
		"showroom":  `class="f-heroanim f-heroanim-plain fs-hero-anim"`,
		"packline":  `class="fo-hero-media f-heroanim"`,
		"steelrack": `class="fb-hero-banner f-heroanim"`,
		"eclipse":   `class="f-heroanim f-heroanim-plain fv-hero-anim"`,
		"glaze":     `class="f-heroanim f-heroanim-plain fh-hero-anim"`,
	} {
		setTheme(theme)
		body := fetch()
		if !strings.Contains(body, marker) || !strings.Contains(body, "fa-gear-big") {
			t.Fatalf("%s 默认视觉应渲染工厂动画（缺 %q）", theme, marker)
		}
	}

	// image 模式（配了图）：出图不出动画。
	setTheme("showroom")
	if err := s.store.SetSetting("hero.visual", "image"); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting("hero.image", "/uploads/hero.webp"); err != nil {
		t.Fatal(err)
	}
	body := fetch()
	if strings.Contains(body, "f-heroanim") || !strings.Contains(body, `class="fs-hero-img"`) {
		t.Fatal("image 模式不应渲染动画、应渲染底图")
	}

	// svg 模式：工厂骨架维持纹理现状（不渲染动画也不内嵌自定义 SVG）。
	if err := s.store.SetSetting("hero.visual", "svg"); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting("hero.svg", "<svg></svg>"); err != nil {
		t.Fatal(err)
	}
	if body = fetch(); strings.Contains(body, "f-heroanim") {
		t.Fatal("svg 模式不应渲染动画（维持纹理回落）")
	}

	// 全屏骨架（沉浸/门楣）：gallery 回落优先于动画——配了图集仍全幅铺底图（既有行为不变）。
	if err := s.store.SetSetting("hero.visual", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting(factoryGallerySettingKey, `["/uploads/w1.webp"]`); err != nil {
		t.Fatal(err)
	}
	for theme, img := range map[string]string{"eclipse": `class="fv-hero-img"`, "glaze": `class="fh-hero-img"`} {
		setTheme(theme)
		body = fetch()
		if strings.Contains(body, "f-heroanim") || !strings.Contains(body, img) {
			t.Fatalf("%s 配了图集应回落铺底图而非动画", theme)
		}
	}
}

// ---------- 外观页动态表单 ----------

func themeOptsAdminSession(t *testing.T, s *Server) (*http.Cookie, string) {
	t.Helper()
	if err := s.setAdminPasswordHash("admin", nonDefaultTestPasswordHash(t)); err != nil {
		t.Fatalf("set non-default admin password: %v", err)
	}
	h := s.Handler()
	form := url.Values{"username": {"admin"}, "password": {nonDefaultTestPassword}}
	req := httptest.NewRequest(http.MethodPost, "https://example.test/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("login did not set session cookie")
	}
	page := httptest.NewRequest(http.MethodGet, "https://example.test/admin/settings/appearance", nil)
	page.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, page)
	if rec.Code != http.StatusOK {
		t.Fatalf("appearance status = %d", rec.Code)
	}
	m := regexp.MustCompile(`name="_csrf" value="([^"]+)"`).FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatal("appearance page missing csrf")
	}
	return cookie, m[1]
}

func themeOptsAppearanceHTML(t *testing.T, s *Server, cookie *http.Cookie, query string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "https://example.test/admin/settings/appearance"+query, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("appearance status = %d", rec.Code)
	}
	return rec.Body.String()
}

// 工厂主题出全部槽表单；内容主题只出 hero 槽（不出工厂槽、不带槽标记）。
func TestAppearanceThemeOptionsForm(t *testing.T) {
	s := newTestPublicServer(t, "")
	cookie, _ := themeOptsAdminSession(t, s)

	factoryFields := []string{
		`name="factory_stat_num_0"`, `name="factory_process_on"`, `name="factory_step_title_0"`,
		`name="factory_categories_on"`, `name="factory_industry_name_0"`, `name="factory_gallery"`,
		`name="factory_faq_q_0"`, `name="factory_cta_title"`, `name="theme_opts" value="factory"`,
	}
	body := themeOptsAppearanceHTML(t, s, cookie, "")
	if !strings.Contains(body, `name="hero_visual"`) {
		t.Fatal("内容主题应保留 hero 槽（既有 Hero 配置归并进 schema）")
	}
	for _, f := range factoryFields {
		if strings.Contains(body, f) {
			t.Fatalf("内容主题不应渲染工厂槽 %s", f)
		}
	}
	// options 承载在「主题配置」弹窗里：入口按钮 + modal 容器 + 首页版块节
	// （标准布局下渲染，且带 home_sections_present 登记字段——保存才写这两个键）。
	for _, f := range []string{`href="#theme-options-modal"`, `id="theme-options-modal"`, `data-home-sections`, `name="home_hero"`, `name="home_sections"`, `name="home_sections_present" value="1"`} {
		if !strings.Contains(body, f) {
			t.Fatalf("外观页缺少主题配置弹窗要素 %s", f)
		}
	}

	if err := s.store.SetSetting("theme", "industrial"); err != nil {
		t.Fatal(err)
	}
	body = themeOptsAppearanceHTML(t, s, cookie, "")
	if !strings.Contains(body, `name="hero_visual"`) {
		t.Fatal("工厂主题也应有 hero 槽")
	}
	for _, f := range factoryFields {
		if !strings.Contains(body, f) {
			t.Fatalf("工厂主题缺少表单控件 %s", f)
		}
	}
	// 工厂骨架=固定结构：首页版块节整体不渲染（控件不提交；后端靠登记字段区分
	// 「没渲染」与「清空」，换回标准主题时已有配置保留）。
	for _, f := range []string{`name="home_hero"`, `name="home_sections"`, `name="home_sections_present"`, "data-home-sections"} {
		if strings.Contains(body, f) {
			t.Fatalf("工厂主题下首页版块节不应渲染：%s", f)
		}
	}
	// 语种切换条 + en 编辑视图展示 en 存储值（默认语种值不串场）。
	if err := s.store.SetSetting(factoryCTASettingKey, `{"title":"中文标题","note":""}`); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting(factoryCTASettingKey+"::en", `{"title":"EN Title","note":""}`); err != nil {
		t.Fatal(err)
	}
	body = themeOptsAppearanceHTML(t, s, cookie, "?lang=en")
	if !strings.Contains(body, `value="EN Title"`) || strings.Contains(body, `value="中文标题"`) {
		t.Fatal("en 编辑视图应显示 en 存储值而非默认语种值")
	}
}

// 骨架子集渲染：engineering（phosphor）弹窗只出 stats/categories/cta 三槽 + hero，
// 不消费的槽（流程/行业/图集/FAQ）不渲染；槽登记字段只登记渲染的子集。
func TestAppearanceThemeOptionsFormSubset(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting("theme", "phosphor"); err != nil { // factory-engineering
		t.Fatal(err)
	}
	cookie, _ := themeOptsAdminSession(t, s)
	body := themeOptsAppearanceHTML(t, s, cookie, "")
	for _, f := range []string{
		`name="hero_visual"`, `name="factory_stat_num_0"`, `name="factory_categories_on"`, `name="factory_cta_title"`,
		`name="theme_opts" value="factory"`,
		`name="theme_opt_slot" value="factory.stats"`,
		`name="theme_opt_slot" value="factory.categories.enabled"`,
		`name="theme_opt_slot" value="factory.cta"`,
	} {
		if !strings.Contains(body, f) {
			t.Fatalf("engineering 弹窗缺少 %s", f)
		}
	}
	for _, f := range []string{
		`name="factory_process_on"`, `name="factory_step_title_0"`,
		`name="factory_industry_name_0"`, `name="factory_gallery"`, `name="factory_faq_q_0"`,
		`name="theme_opt_slot" value="factory.process"`,
		`name="theme_opt_slot" value="factory.industries"`,
		`name="theme_opt_slot" value="factory.gallery"`,
		`name="theme_opt_slot" value="factory.faq"`,
	} {
		if strings.Contains(body, f) {
			t.Fatalf("engineering 不消费的槽不应渲染：%s", f)
		}
	}
}

// 子集渲染下的保存：只写表单登记（theme_opt_slot）的槽——没渲染的槽不出现 ≠ 数据被清；
// 登记的槽照常写入与清空。
func TestAppearanceSaveThemeOptionsSubset(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting("theme", "phosphor"); err != nil { // factory-engineering
		t.Fatal(err)
	}
	// 预置 engineering 不消费的槽数据（模拟从全量骨架换过来的站点）。
	preserved := map[string]string{
		factoryProcessSettingKey:    `[{"title":"守住流程"}]`,
		factoryProcessEnabledKey:    "0",
		factoryIndustriesSettingKey: `[{"name":"守住行业","note":"x"}]`,
		factoryIndustriesEnabledKey: "0",
		factoryGallerySettingKey:    `["/uploads/keep.webp"]`,
		factoryFAQSettingKey:        `[{"q":"守住","a":"FAQ"}]`,
		factoryFAQEnabledKey:        "0",
	}
	for k, v := range preserved {
		if err := s.store.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	cookie, csrf := themeOptsAdminSession(t, s)
	post := func(form url.Values) {
		t.Helper()
		form.Set("_csrf", csrf)
		form.Set("theme", "phosphor")
		req := httptest.NewRequest(http.MethodPost, "https://example.test/admin/settings/appearance", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("save status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}

	// 子集表单提交（engineering 只渲染 stats/categories/cta 三槽）：其余字段全部缺席。
	subset := []string{factoryStatsSettingKey, factoryCategoriesEnabledKey, factoryCTASettingKey}
	post(url.Values{
		"theme_opts":            {"factory"},
		"theme_opt_slot":        subset,
		"factory_stat_num_0":    {"1998"},
		"factory_stat_label_0":  {"建厂"},
		"factory_categories_on": {"1"},
		"factory_cta_title":     {"要图纸报价"},
	})
	// 登记槽照常写入。
	if got := s.store.Setting(factoryStatsSettingKey); got != `[{"num":"1998","label":"建厂"}]` {
		t.Fatalf("factory.stats = %q", got)
	}
	if got := s.store.Setting(factoryCTASettingKey); got != `{"title":"要图纸报价","note":""}` {
		t.Fatalf("factory.cta = %q", got)
	}
	if got := s.store.Setting(factoryCategoriesEnabledKey); got != "" {
		t.Fatalf("factory.categories.enabled = %q, want \"\"（勾选=开）", got)
	}
	// 没登记的槽（含其开关）纹丝不动——表单里字段缺席绝不等于清空。
	for k, v := range preserved {
		if got := s.store.Setting(k); got != v {
			t.Fatalf("未渲染槽 %s 被动了：%q, want %q", k, got, v)
		}
	}

	// 登记槽留空 → 清键回落默认；未登记槽依旧保留。
	post(url.Values{"theme_opts": {"factory"}, "theme_opt_slot": subset})
	if got := s.store.Setting(factoryStatsSettingKey); got != "" {
		t.Fatalf("factory.stats = %q, want empty（留空回落默认）", got)
	}
	for _, k := range []string{factoryProcessSettingKey, factoryIndustriesSettingKey, factoryGallerySettingKey, factoryFAQSettingKey} {
		if got := s.store.Setting(k); got != preserved[k] {
			t.Fatalf("未渲染槽 %s 被清了：%q", k, got)
		}
	}
}

// 首页版块的保存闸门：只有表单带 home_sections_present 登记字段（该节实际渲染了）
// 才写 home.hero / home.sections——固定结构主题下该节整体不渲染，字段缺席 ≠ 清空。
func TestAppearanceSaveHomeSectionsGuard(t *testing.T) {
	s := newTestPublicServer(t, "")
	custom := `[{"key":"latest","on":true},{"key":"featured","on":false},{"key":"links","on":true},{"key":"categories","on":false}]`
	if err := s.store.SetSetting(homeSectionsKey, custom); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting(homeHeroKey, "1"); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := themeOptsAdminSession(t, s)
	post := func(theme string, form url.Values) {
		t.Helper()
		form.Set("_csrf", csrf)
		form.Set("theme", theme)
		req := httptest.NewRequest(http.MethodPost, "https://example.test/admin/settings/appearance", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("save status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}

	// 固定结构主题（工厂）保存：无登记字段 → 两键纹丝不动（老逻辑会把 hero 清成 "0"、
	// 版块表重置回默认——这正是「缺字段=清空」的误清）。
	post("industrial", url.Values{"theme_opts": {"factory"}, "theme_opt_slot": {factoryStatsSettingKey}})
	if got := s.store.Setting(homeHeroKey); got != "1" {
		t.Fatalf("home.hero = %q, want 1（未渲染的节不该被写）", got)
	}
	if got := s.store.Setting(homeSectionsKey); got != custom {
		t.Fatalf("home.sections = %q, want 原值保留", got)
	}

	// 标准布局主题保存：带登记字段 → 照常写入（未勾选 hero = 关，版块 JSON 过 sanitize）。
	post("editorial", url.Values{
		"home_sections_present": {"1"},
		"home_sections":         {`[{"key":"featured","on":false},{"key":"latest","on":true}]`},
	})
	if got := s.store.Setting(homeHeroKey); got != "0" {
		t.Fatalf("home.hero = %q, want 0（登记后未勾选=关）", got)
	}
	if got := s.store.Setting(homeSectionsKey); !strings.HasPrefix(got, `[{"key":"featured","on":false},{"key":"latest","on":true}`) {
		t.Fatalf("home.sections = %q, want 提交值落库", got)
	}
}

// 外观保存的 AJAX 路径：Accept JSON → 成功 200 {ok:true} 不重定向、失败（未知主题）400 {ok:false}；
// 落库行为与 PRG 路径一致。
func TestAppearanceSaveAJAX(t *testing.T) {
	s := newTestPublicServer(t, "")
	cookie, csrf := themeOptsAdminSession(t, s)
	post := func(form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		form.Set("_csrf", csrf)
		req := httptest.NewRequest(http.MethodPost, "https://example.test/admin/settings/appearance", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}

	rec := post(url.Values{"theme": {"cream"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("ajax save status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"ok":true`) || !strings.Contains(body, "外观设置已保存。") {
		t.Fatalf("ajax save body = %s", body)
	}
	if got := s.store.Setting("theme"); got != "cream" {
		t.Fatalf("theme = %q, want cream", got)
	}

	rec = post(url.Values{"theme": {"no-such-theme"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ajax invalid theme status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Fatalf("ajax invalid theme body = %s", rec.Body.String())
	}
	if got := s.store.Setting("theme"); got != "cream" {
		t.Fatalf("theme = %q, 失败不应改主题", got)
	}
}

// 保存：写语义键（默认语种裸键 / en 走 ::en）、开关落 "0"、留空清键回落默认；
// 无槽标记的保存（内容主题表单）绝不触碰工厂键。
func TestAppearanceSaveThemeOptions(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting("theme", "showroom"); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := themeOptsAdminSession(t, s)
	post := func(form url.Values) {
		t.Helper()
		form.Set("_csrf", csrf)
		form.Set("theme", "showroom")
		req := httptest.NewRequest(http.MethodPost, "https://example.test/admin/settings/appearance", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("save status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}

	// 默认语种保存三组 + 新区块；hero 槽选「动画1」必须落键（历史坑：一律归一成 "" 丢选择）。
	post(url.Values{
		"theme_opts":              {"factory"},
		"hero_visual":             {"anim1"},
		"factory_stat_num_0":      {"2010"},
		"factory_stat_label_0":    {"建厂"},
		"factory_stat_num_1":      {"只有数字没标签"}, // 应被丢弃
		"factory_process_on":      {"1"},
		"factory_step_title_1":    {"免费打样"},
		"factory_industries_on":   {"1"},
		"factory_industry_name_0": {"汽车配件"},
		"factory_industry_note_0": {"主机厂供货"},
		"factory_gallery":         {"/uploads/a.webp\n\n /uploads/b.webp "},
		"factory_faq_on":          {"1"},
		"factory_faq_q_0":         {"能贴牌吗？"},
		"factory_faq_a_0":         {"支持 OEM。"},
		"factory_categories_on":   {"1"},
		"factory_cta_title":       {"谈一单"},
	})
	if got := s.store.Setting(factoryStatsSettingKey); got != `[{"num":"2010","label":"建厂"}]` {
		t.Fatalf("factory.stats = %q", got)
	}
	if got := s.store.Setting(factoryProcessSettingKey); !strings.Contains(got, `"免费打样"`) {
		t.Fatalf("factory.process = %q", got)
	}
	if got := s.store.Setting(factoryIndustriesSettingKey); got != `[{"name":"汽车配件","note":"主机厂供货"}]` {
		t.Fatalf("factory.industries = %q", got)
	}
	if got := s.store.Setting(factoryGallerySettingKey); got != `["/uploads/a.webp","/uploads/b.webp"]` {
		t.Fatalf("factory.gallery = %q", got)
	}
	if got := s.store.Setting(factoryFAQSettingKey); got != `[{"q":"能贴牌吗？","a":"支持 OEM。"}]` {
		t.Fatalf("factory.faq = %q", got)
	}
	if got := s.store.Setting(factoryCTASettingKey); got != `{"title":"谈一单","note":""}` {
		t.Fatalf("factory.cta = %q", got)
	}
	if got := s.store.Setting("hero.visual"); got != "anim1" {
		t.Fatalf("hero.visual = %q, want anim1（动画1 应落键）", got)
	}

	// 非法 hero_visual 值归一成 ""（默认动画）；不带槽标记，不影响后续工厂键断言。
	post(url.Values{"hero_visual": {"bogus"}})
	if got := s.store.Setting("hero.visual"); got != "" {
		t.Fatalf("hero.visual = %q, want \"\"（非法值归一默认）", got)
	}

	// en 语种保存：落 ::en，裸键不动。
	post(url.Values{
		"theme_opts":        {"factory"},
		"lang":              {"en"},
		"factory_cta_title": {"Talk Business"},
		// 开关不勾选 → 全局键写 "0"
	})
	if got := s.store.Setting(factoryCTASettingKey + "::en"); got != `{"title":"Talk Business","note":""}` {
		t.Fatalf("factory.cta::en = %q", got)
	}
	if got := s.store.Setting(factoryCTASettingKey); got != `{"title":"谈一单","note":""}` {
		t.Fatalf("en 保存不应动默认语种键，got %q", got)
	}
	for _, key := range []string{factoryProcessEnabledKey, factoryFAQEnabledKey, factoryIndustriesEnabledKey, factoryCategoriesEnabledKey} {
		if got := s.store.Setting(key); got != "0" {
			t.Fatalf("%s = %q, want 0（未勾选）", key, got)
		}
	}
	// en 表单其它文本留空 → en 覆盖被清空（回落默认语种/内置默认）。
	if got := s.store.Setting(factoryStatsSettingKey + "::en"); got != "" {
		t.Fatalf("factory.stats::en = %q, want empty", got)
	}

	// 默认语种全部留空 → 清键（回落 i18n）。
	post(url.Values{"theme_opts": {"factory"}})
	for _, key := range []string{factoryStatsSettingKey, factoryProcessSettingKey, factoryIndustriesSettingKey, factoryGallerySettingKey, factoryFAQSettingKey, factoryCTASettingKey} {
		if got := s.store.Setting(key); got != "" {
			t.Fatalf("%s = %q, want empty（留空回落默认）", key, got)
		}
	}

	// 无槽标记（内容主题的表单形态）：绝不触碰工厂键。
	if err := s.store.SetSetting(factoryStatsSettingKey, `[{"num":"1","label":"守住"}]`); err != nil {
		t.Fatal(err)
	}
	post(url.Values{}) // 没有 theme_opts
	if got := s.store.Setting(factoryStatsSettingKey); !strings.Contains(got, "守住") {
		t.Fatalf("无槽标记的保存动了工厂键：%q", got)
	}
}

// ---------- AI 写路径（PATCH /site-profile） ----------

func TestAPISiteProfileFactoryOptions(t *testing.T) {
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

	// 批量：zh + en 全套槽。
	w := patch(`{"items":[
		{"lang":"zh",
		 "factory_stats":[{"num":2008,"label":"建厂"}],
		 "factory_process":{"enabled":true,"steps":[{"title":"询盘","note":"当天回复"}]},
		 "factory_cta":{"title":"要报价","note":"附上数量"},
		 "factory_categories":{"enabled":false},
		 "factory_industries":{"items":[{"name":"军工","note":"高可靠"}]},
		 "factory_gallery":["/uploads/w1.webp"],
		 "factory_faq":{"items":[{"q":"验厂吗","a":"欢迎"}]}},
		{"lang":"en","factory_cta":{"title":"Get Pricing","note":""}}
	]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := s.store.Setting(factoryStatsSettingKey); got != `[{"num":"2008","label":"建厂"}]` {
		t.Fatalf("factory.stats = %q（数字 num 应规范化为字符串）", got)
	}
	if got := s.store.Setting(factoryProcessSettingKey); !strings.Contains(got, `"询盘"`) {
		t.Fatalf("factory.process = %q", got)
	}
	if got := s.store.Setting(factoryCTASettingKey + "::en"); got != `{"title":"Get Pricing","note":""}` {
		t.Fatalf("factory.cta::en = %q", got)
	}
	if got := s.store.Setting(factoryCategoriesEnabledKey); got != "0" {
		t.Fatalf("factory.categories.enabled = %q, want 0", got)
	}
	if got := s.store.Setting(factoryIndustriesSettingKey); got != `[{"name":"军工","note":"高可靠"}]` {
		t.Fatalf("factory.industries = %q", got)
	}
	if got := s.store.Setting(factoryGallerySettingKey); got != `["/uploads/w1.webp"]` {
		t.Fatalf("factory.gallery = %q", got)
	}
	if got := s.store.Setting(factoryFAQSettingKey); got != `[{"q":"验厂吗","a":"欢迎"}]` {
		t.Fatalf("factory.faq = %q", got)
	}

	// GET 读出：zh item 带回覆盖值与被关的开关。
	item := s.apiSiteProfileItem("zh")
	if len(item.FactoryStats) != 1 || item.FactoryStats[0].Num != "2008" {
		t.Fatalf("item.FactoryStats = %#v", item.FactoryStats)
	}
	if item.FactoryProcess == nil || !item.FactoryProcess.Enabled || len(item.FactoryProcess.Steps) != 1 {
		t.Fatalf("item.FactoryProcess = %#v", item.FactoryProcess)
	}
	if item.FactoryCategories == nil || item.FactoryCategories.Enabled == nil || *item.FactoryCategories.Enabled {
		t.Fatalf("item.FactoryCategories = %#v（被关闭应显式返回 enabled:false）", item.FactoryCategories)
	}
	if item.FactoryCTA == nil || item.FactoryCTA.Title != "要报价" {
		t.Fatalf("item.FactoryCTA = %#v", item.FactoryCTA)
	}

	// 传 [] 清除覆盖。
	if w := patch(`{"lang":"zh","factory_stats":[],"factory_gallery":[],"factory_faq":{"items":[]}}`); w.Code != http.StatusOK {
		t.Fatalf("clear status = %d, body = %s", w.Code, w.Body.String())
	}
	for _, key := range []string{factoryStatsSettingKey, factoryGallerySettingKey, factoryFAQSettingKey} {
		if got := s.store.Setting(key); got != "" {
			t.Fatalf("%s = %q, want empty after []", key, got)
		}
	}

	// 非法格式：400 且不落库。
	for _, bad := range []string{
		`{"lang":"zh","factory_stats":[{"num":"only-num"}]}`,
		`{"lang":"zh","factory_stats":"not-an-array"}`,
		`{"lang":"zh","factory_faq":{"items":[{"q":"没答案"}]}}`,
		`{"lang":"zh","factory_gallery":[""]}`,
	} {
		if w := patch(bad); w.Code != http.StatusBadRequest {
			t.Fatalf("bad payload %s → status %d, want 400（body %s）", bad, w.Code, w.Body.String())
		}
	}

	// 权限：只有品牌资产权限的密钥写主题槽 → 403（这些槽按站点文案 site:write 计）。
	sBrand, brandToken := newTestAutomationServer(t, apiScopeBrandAssetsWrite)
	r := httptest.NewRequest(http.MethodPatch, "/api/admin/v1/site-profile", bytes.NewReader([]byte(`{"lang":"zh","factory_stats":[{"num":"1","label":"x"}]}`)))
	r.Header.Set("Authorization", "Bearer "+brandToken)
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	sBrand.apiUpdateSiteProfile(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("brand-only scope should be 403, got %d body %s", w.Code, w.Body.String())
	}
	if got := sBrand.store.Setting(factoryStatsSettingKey); got != "" {
		t.Fatalf("403 后不应落库，got %q", got)
	}
}

// ---------- GET /theme-options（自动化端点） ----------

type themeOptionsResp struct {
	Theme  string `json:"theme"`
	Layout string `json:"layout"`
	Family string `json:"family"`
	Lang   string `json:"lang"`
	Slots  []struct {
		Key        string          `json:"key"`
		Type       string          `json:"type"`
		Label      string          `json:"label"`
		Localized  bool            `json:"localized"`
		EnabledKey string          `json:"enabled_key"`
		Enabled    *bool           `json:"enabled"`
		Configured bool            `json:"configured"`
		Value      json.RawMessage `json:"value"`
	} `json:"slots"`
}

// 端点契约：字段齐全、scope=site:read、槽子集跟随当前主题、未启用槽带 enabled:false、
// 文本槽按语种取现值、gallery 回 URL 数组。走完整路由（admin v1 字面路径不被通配吞掉）。
func TestAPIThemeOptionsEndpoint(t *testing.T) {
	s := newTestPublicServer(t, "")
	token, prefix := newAutomationToken()
	if _, err := s.store.CreateAutomationKey("opt", token, prefix, apiScopeSiteRead); err != nil {
		t.Fatal(err)
	}
	get := func(path, tok string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "https://example.test"+path, nil)
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, r)
		return rec
	}
	decode := func(rec *httptest.ResponseRecorder) themeOptionsResp {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var out themeOptionsResp
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	// 内容主题：只有 hero 槽。
	if err := s.store.SetSetting("theme", "editorial"); err != nil {
		t.Fatal(err)
	}
	out := decode(get("/api/admin/v1/theme-options", token))
	if out.Theme != "editorial" || out.Layout != "topbar" || out.Family != "content" || out.Lang != "zh" {
		t.Fatalf("content theme head = %+v", out)
	}
	if len(out.Slots) != 1 || out.Slots[0].Key != "hero.visual" || out.Slots[0].Type != "hero" {
		t.Fatalf("content theme slots = %+v, want 仅 hero", out.Slots)
	}

	// 工厂技术骨架（phosphor）：槽子集 + 现值 + 开关态。
	if err := s.store.SetSetting("theme", "phosphor"); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting(factoryStatsSettingKey, `[{"num":"2008","label":"建厂"}]`); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting(factoryCTASettingKey+"::en", `{"title":"Get Pricing","note":""}`); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting(factoryCategoriesEnabledKey, "0"); err != nil {
		t.Fatal(err)
	}
	out = decode(get("/api/admin/v1/theme-options", token))
	if out.Theme != "phosphor" || out.Layout != "factory-engineering" || out.Family != "factory" {
		t.Fatalf("factory theme head = %+v", out)
	}
	keys := make([]string, 0, len(out.Slots))
	byKey := map[string]int{}
	for i, slot := range out.Slots {
		keys = append(keys, slot.Key)
		byKey[slot.Key] = i
	}
	if strings.Join(keys, ",") != "hero.visual,factory.stats,factory.categories.enabled,factory.cta" {
		t.Fatalf("engineering slots = %v（应只回消费子集）", keys)
	}
	stats := out.Slots[byKey[factoryStatsSettingKey]]
	if !stats.Configured || !stats.Localized || stats.Type != "stats" || !strings.Contains(string(stats.Value), `"2008"`) {
		t.Fatalf("stats slot = %+v", stats)
	}
	cats := out.Slots[byKey[factoryCategoriesEnabledKey]]
	if cats.EnabledKey != factoryCategoriesEnabledKey || cats.Enabled == nil || *cats.Enabled || cats.Configured {
		t.Fatalf("categories slot = %+v（关闭时应 enabled:false、configured:false）", cats)
	}
	if cta := out.Slots[byKey[factoryCTASettingKey]]; cta.Configured || cta.Value != nil {
		t.Fatalf("zh cta 未配置应 configured:false 且省略 value，got %+v", cta)
	}

	// 按语种现值：?lang=en 取 ::en 覆盖。
	out = decode(get("/api/admin/v1/theme-options?lang=en", token))
	if out.Lang != "en" {
		t.Fatalf("lang = %q", out.Lang)
	}
	for _, slot := range out.Slots {
		if slot.Key == factoryCTASettingKey {
			if !slot.Configured || !strings.Contains(string(slot.Value), "Get Pricing") {
				t.Fatalf("en cta slot = %+v", slot)
			}
		}
	}

	// gallery：全量骨架下回 URL 数组。
	if err := s.store.SetSetting("theme", "showroom"); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting(factoryGallerySettingKey, `["/uploads/w1.webp","/uploads/w2.webp"]`); err != nil {
		t.Fatal(err)
	}
	out = decode(get("/api/admin/v1/theme-options", token))
	if len(out.Slots) != 8 {
		t.Fatalf("showroom slots = %d, want 8（hero + 7 工厂槽）", len(out.Slots))
	}
	found := false
	for _, slot := range out.Slots {
		if slot.Key == factoryGallerySettingKey {
			found = true
			var urls []string
			if err := json.Unmarshal(slot.Value, &urls); err != nil || len(urls) != 2 || urls[0] != "/uploads/w1.webp" {
				t.Fatalf("gallery value = %s", slot.Value)
			}
			if slot.Localized || !slot.Configured {
				t.Fatalf("gallery slot = %+v", slot)
			}
		}
	}
	if !found {
		t.Fatal("showroom 应包含 gallery 槽")
	}

	// 语种未启用 → 400；缺权限 → 403；无认证 → 401。
	if rec := get("/api/admin/v1/theme-options?lang=xx", token); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad lang status = %d", rec.Code)
	}
	wrongToken, wrongPrefix := newAutomationToken()
	if _, err := s.store.CreateAutomationKey("noscope", wrongToken, wrongPrefix, apiScopeStatsRead); err != nil {
		t.Fatal(err)
	}
	if rec := get("/api/admin/v1/theme-options", wrongToken); rec.Code != http.StatusForbidden {
		t.Fatalf("missing scope status = %d, want 403（scope 口径 = site:read）", rec.Code)
	}
	if rec := get("/api/admin/v1/theme-options", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no auth status = %d, want 401", rec.Code)
	}
}

// ---------- 文档同步 ----------

func TestAutomationDocsThemeOptions(t *testing.T) {
	spec := automationOpenAPISpec("http://x/api/admin/v1")
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, want := range []string{"factory_stats", "factory_process", "factory_cta", "factory_categories", "factory_industries", "factory_gallery", "factory_faq"} {
		if !strings.Contains(doc, `"`+want+`"`) {
			t.Fatalf("openapi 缺少 %s", want)
		}
	}
	// theme-options 端点：路径 + 响应 schema 进 OpenAPI。
	for _, want := range []string{`"/theme-options"`, `"ThemeOptionsResponse"`, `"ThemeOptionSlot"`, `"getThemeOptions"`} {
		if !strings.Contains(doc, want) {
			t.Fatalf("openapi 缺少 %s", want)
		}
	}
	md := automationSkillMarkdown("http://x/api/admin/v1")
	for _, want := range []string{
		"## 主题配置", "factory_faq", "factory_gallery", "industrial", "gunmetal", "`theme-options`",
		// 与「主题配置」节互链：先 theme-options 看槽再 PATCH site-profile；绝不编造工厂数字。
		"node scripts/gcms.js theme-options", "绝不编造工厂数字", "GET /theme-options",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("SKILL.md 缺少 %q", want)
		}
	}
	// 平台包 SKILL.md 与两份 CLI 脚本（含镜像，另有 TestSkillScriptMirrorsInSync 钉住）同步带上命令。
	if pmd := platformSkillMarkdown("http://x/api/platform/v1"); !strings.Contains(pmd, "theme-options --site") {
		t.Fatal("平台 SKILL.md 缺少 theme-options --site 命令")
	}
	for name, script := range map[string]string{"single": skillScriptSingle, "platform": skillScriptPlatform} {
		if !strings.Contains(script, `cmd === "theme-options"`) || !strings.Contains(script, "/theme-options") {
			t.Fatalf("%s CLI 脚本缺少 theme-options 命令", name)
		}
		if !strings.Contains(script, "服务端较旧（没有 theme-options 端点）") {
			t.Fatalf("%s CLI 脚本缺少 404 降级指引", name)
		}
	}
}
