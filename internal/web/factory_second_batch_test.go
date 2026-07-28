package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var secondBatchFactoryThemes = []string{
	"andon", "certwall", "container", "crate", "draftdesk",
	"exportmap", "floorplan", "furnace", "gantry", "gauge",
	"hazardtape", "inspection", "line", "nameplate", "pipeworks",
	"quotation", "rackwall", "sampleroom", "shutter", "tonnage",
}

var secondBatchFactoryCertificationThemes = []string{
	"certwall", "container", "crate", "draftdesk", "exportmap", "furnace",
	"gantry", "gauge", "hazardtape", "inspection", "line",
	"nameplate", "rackwall", "sampleroom",
}

var legacyFactoryCertificationThemes = []string{"navigator", "showroom", "drafting", "phosphor", "glaze"}

func TestSecondBatchFactoryThemesRegistered(t *testing.T) {
	families := map[string][]string{
		"factory-andon":      {"andon", "shopfloor", "andon-white"},
		"factory-certwall":   {"certwall", "attest", "certwall-white"},
		"factory-container":  {"container", "seafreight", "container-white"},
		"factory-crate":      {"crate", "stencil", "crate-white"},
		"factory-draftdesk":  {"draftdesk", "blueline", "draftdesk-white"},
		"factory-exportmap":  {"exportmap", "nightport", "exportmap-white"},
		"factory-floorplan":  {"floorplan", "zoning", "floorplan-white"},
		"factory-furnace":    {"furnace", "emberdark", "furnace-white"},
		"factory-gantry":     {"gantry", "beamline", "gantry-white"},
		"factory-gauge":      {"gauge", "dialface", "gauge-white"},
		"factory-hazardtape": {"hazardtape", "graveyard", "hazardtape-white"},
		"factory-inspection": {"inspection", "passmark", "inspection-white"},
		"factory-line":       {"line", "conveyor", "line-white"},
		"factory-nameplate":  {"nameplate", "etchplate", "nameplate-white"},
		"factory-pipeworks":  {"pipeworks", "flowline", "pipeworks-white"},
		"factory-quotation":  {"quotation", "proforma", "quotation-white"},
		"factory-rackwall":   {"rackwall", "aisle", "rackwall-white"},
		"factory-sampleroom": {"sampleroom", "swatchbook", "sampleroom-white"},
		"factory-shutter":    {"shutter", "stallfront", "shutter-white"},
		"factory-tonnage":    {"tonnage", "millscale", "tonnage-white"},
	}
	byID := make(map[string]ThemeOption, len(Themes))
	for _, theme := range Themes {
		byID[theme.ID] = theme
	}
	wantIDs := make(map[string]bool, len(families)*3)
	for layout, ids := range families {
		if !isFactoryLayout(layout) {
			t.Errorf("%s is not recognized as a factory layout", layout)
		}
		if _, ok := themeSkeletons[layout]; !ok {
			t.Errorf("themeSkeletons missing %s", layout)
		}
		if themeSkeletonDescEN[layout] == "" {
			t.Errorf("themeSkeletonDescEN missing %s", layout)
		}
		for _, id := range ids {
			wantIDs[id] = true
			theme, ok := byID[id]
			if !ok {
				t.Errorf("factory theme %q not registered", id)
				continue
			}
			if theme.Category != ThemeCategoryFactory {
				t.Errorf("%s category = %q, want factory", id, theme.Category)
			}
			if got := layoutForTheme(id); got != layout {
				t.Errorf("layoutForTheme(%q) = %q, want %q", id, got, layout)
			}
			if themeAccentDefault[id] == "" {
				t.Errorf("%s missing accent default", id)
			}
			if themeRadiusDefault[id] == "" {
				t.Errorf("%s missing radius default", id)
			}
			if themeBgDefault[id] == "" {
				t.Errorf("%s missing explicit background default", id)
			}
			if themeDescEN[id] == "" {
				t.Errorf("%s missing English description", id)
			}
			if strings.HasSuffix(id, "-white") && themeBgDefault[id] != "#ffffff" {
				t.Errorf("%s white card background = %q, want #ffffff", id, themeBgDefault[id])
			}
		}
	}
	if len(wantIDs) != 60 {
		t.Fatalf("second-batch factory theme IDs = %d, want 60", len(wantIDs))
	}
	for _, theme := range Themes {
		if _, isSecondBatchLayout := families[layoutForTheme(theme.ID)]; isSecondBatchLayout && !wantIDs[theme.ID] {
			t.Errorf("unexpected theme %s registered under second-batch layout %s", theme.ID, layoutForTheme(theme.ID))
		}
	}
}

func TestParseFactoryCertifications(t *testing.T) {
	raw := `[
		{"name":" IATF 16949 ","note":" 有效期至 2028-06 "},
		{"name":"","note":"空名称必须丢弃"},
		{"name":"Intertek Audit","note":""},
		{"name":"C3"},{"name":"C4"},{"name":"C5"},{"name":"C6"},
		{"name":"C7"},{"name":"C8"},{"name":"超出上限"}
	]`
	got := parseFactoryCertifications(raw)
	if len(got) != maxFactoryCertifications {
		t.Fatalf("certifications len = %d, want %d: %#v", len(got), maxFactoryCertifications, got)
	}
	if got[0].Name != "IATF 16949" || got[0].Note != "有效期至 2028-06" {
		t.Fatalf("certifications[0] = %#v", got[0])
	}
	if got[1].Name != "Intertek Audit" || got[1].Note != "" {
		t.Fatalf("certifications[1] = %#v", got[1])
	}
	if got[len(got)-1].Name == "超出上限" {
		t.Fatal("certification parser did not enforce the item cap")
	}
	for _, invalid := range []string{"", "null", "{}", "not-json", `[{"note":"missing name"}]`} {
		if rows := parseFactoryCertifications(invalid); len(rows) != 0 {
			t.Fatalf("parseFactoryCertifications(%q) = %#v, want empty", invalid, rows)
		}
	}
}

func TestSecondBatchFactoryHeroSlotContract(t *testing.T) {
	s := newTestPublicServer(t, "")
	set := func(key, value string) {
		t.Helper()
		if err := s.store.SetSetting(key, value); err != nil {
			t.Fatalf("SetSetting(%q): %v", key, err)
		}
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
		heroImage   = "/uploads/factory-hero-contract.webp"
		galleryOnly = "/uploads/factory-gallery-only.webp"
		svgMarker   = `data-factory-contract="custom-svg"`
	)
	set(factoryGallerySettingKey, `["`+galleryOnly+`"]`)

	set("hero.visual", "image")
	set("hero.image", heroImage)
	for _, theme := range secondBatchFactoryThemes {
		body := render(theme)
		if !strings.Contains(body, `data-factory-hero-visual="image"`) ||
			!strings.Contains(body, `src="`+heroImage+`"`) {
			t.Errorf("%s image 模式没有渲染站点 Hero 图片", theme)
		}
		if got := strings.Count(body, `src="`+heroImage+`"`); got != 1 {
			t.Errorf("%s Hero 图片出现 %d 次，want 1", theme, got)
		}
	}

	set("hero.visual", "svg")
	set("hero.svg", `<svg viewBox="0 0 10 10" `+svgMarker+`></svg>`)
	for _, theme := range secondBatchFactoryThemes {
		body := render(theme)
		if !strings.Contains(body, `data-factory-hero-visual="svg"`) ||
			!strings.Contains(body, svgMarker) {
			t.Errorf("%s svg 模式没有渲染站点 SVG", theme)
		}
		if strings.Contains(body, `data-factory-hero-visual="image"`) {
			t.Errorf("%s svg 模式错误渲染了 Hero 图片", theme)
		}
	}

	// 历史坑：用户选了 SVG/图片但还没填值时，首屏不能变成空白。
	set("hero.svg", "")
	for _, theme := range secondBatchFactoryThemes {
		if body := render(theme); !strings.Contains(body, `data-factory-hero-visual="animation"`) {
			t.Errorf("%s 空 SVG 槽没有回落主题动画", theme)
		}
	}
	set("hero.visual", "image")
	set("hero.image", "")
	for _, theme := range secondBatchFactoryThemes {
		if body := render(theme); !strings.Contains(body, `data-factory-hero-visual="animation"`) {
			t.Errorf("%s 空图片槽没有回落主题动画", theme)
		}
	}

	set("hero.visual", "")
	for _, theme := range secondBatchFactoryThemes {
		body := render(theme)
		if !strings.Contains(body, `data-factory-hero-visual="animation"`) ||
			!strings.Contains(body, `class="f-heroanim f-heroanim-plain"`) {
			t.Errorf("%s 默认模式没有渲染工厂动画", theme)
		}
		// 图集只属于后续图集内容，不能偷偷替代 Hero。
		if strings.Contains(body, `data-factory-hero-visual="image"`) {
			t.Errorf("%s 默认模式错误地把 gallery 首图当作 Hero", theme)
		}
	}
}

func TestSecondBatchFactoryCertificationsAreRealAndOptional(t *testing.T) {
	s := newTestPublicServer(t, "")
	set := func(key, value string) {
		t.Helper()
		if err := s.store.SetSetting(key, value); err != nil {
			t.Fatalf("SetSetting(%q): %v", key, err)
		}
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
		realName = "客户验厂 A-2026"
		realNote = "有效期至 2028-06"
	)
	set(factoryCertificationsSettingKey, "")
	for _, theme := range append(secondBatchFactoryCertificationThemes, legacyFactoryCertificationThemes...) {
		body := render(theme)
		if strings.Contains(body, realName) || strings.Contains(body, `class="f-cert"`) {
			t.Errorf("%s 空认证槽仍渲染了认证数据", theme)
		}
	}

	set(factoryCertificationsSettingKey, `[{"name":"`+realName+`","note":"`+realNote+`"}]`)
	consumer := make(map[string]bool, len(secondBatchFactoryCertificationThemes))
	for _, theme := range append(secondBatchFactoryCertificationThemes, legacyFactoryCertificationThemes...) {
		consumer[theme] = true
		body := render(theme)
		if !strings.Contains(body, realName) || !strings.Contains(body, realNote) {
			t.Errorf("%s 没有渲染站点真实认证数据", theme)
		}
	}
	for _, theme := range secondBatchFactoryThemes {
		if consumer[theme] {
			continue
		}
		if body := render(theme); strings.Contains(body, realName) {
			t.Errorf("%s 未声明认证槽却渲染了认证数据", theme)
		}
	}
}

func TestFactoryGaugeSectionNumbersFollowVisibleOrder(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SeedFactoryDemoProducts(); err != nil {
		t.Fatal(err)
	}
	v := &View{Layout: "factory-gauge"}
	s.fillFactoryHome(v, "zh")
	if got := v.FactorySectionNum["products"]; got != "01" {
		t.Fatalf("gauge products section number = %q, want 01", got)
	}
	if got := v.FactorySectionNum["categories"]; got != "02" {
		t.Fatalf("gauge categories section number = %q, want 02", got)
	}
}

func TestSecondBatchFactoryProductCategoryContract(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SeedFactoryDemoProducts(); err != nil {
		t.Fatal(err)
	}
	for _, theme := range secondBatchFactoryThemes {
		if err := s.store.SetSetting("theme", theme); err != nil {
			t.Fatal(err)
		}
		s.clearGeneratedCaches()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/zh/", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s home status = %d, body = %s", theme, rec.Code, rec.Body.String())
			continue
		}
		body := rec.Body.String()
		if !strings.Contains(body, "精密 CNC 铝合金加工件") {
			t.Errorf("%s 没有渲染已发布的真实商品", theme)
		}
		if !strings.Contains(body, `/zh/products/cat/mechanical-parts`) {
			t.Errorf("%s 没有渲染真实商品分类路由", theme)
		}
		if strings.Contains(body, `href="/zh/category/mechanical-parts`) {
			t.Errorf("%s 把商品分类错误地链接到文章分类路由", theme)
		}
	}
}

func TestSecondBatchFactoryTemplatesContainNoFakeCredentialsOrFixedEnglishLabels(t *testing.T) {
	for _, theme := range secondBatchFactoryThemes {
		path := filepath.Join("..", "..", "templates", "partials", "home_factory_"+theme+".html")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		for _, banned := range []string{
			"ISO 9001", ">CE<", ">RoHS<", ">SGS<",
			">Batches<", ">Item / Description<", ">No.<", ">Pass<", ">Routes<", ">Specs<",
			"index .FactoryGallery 0",
		} {
			if strings.Contains(source, banned) {
				t.Errorf("%s still contains hardcoded visual label %q", theme, banned)
			}
		}
	}
	for _, layout := range []string{"showcase", "solutions", "engineering", "herofold"} {
		path := filepath.Join("..", "..", "templates", "partials", "home_factory_"+layout+".html")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"ISO 9001", ">CE<", ">RoHS<", ">SGS<", ">REACH<", ">UL<"} {
			if strings.Contains(string(data), banned) {
				t.Errorf("%s still contains hardcoded credential %q", layout, banned)
			}
		}
	}
	header, err := os.ReadFile(filepath.Join("..", "..", "templates", "partials", "header.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{">PASS<", ">QC<"} {
		if strings.Contains(string(header), banned) {
			t.Errorf("factory inspection header still contains fake quality marker %q", banned)
		}
	}
	footer, err := os.ReadFile(filepath.Join("..", "..", "templates", "partials", "footer.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"ISO 9001", ">CE<", ">RoHS<", ">SGS<"} {
		if strings.Contains(string(footer), banned) {
			t.Errorf("factory footer still contains hardcoded credential %q", banned)
		}
	}
	css, err := os.ReadFile(filepath.Join("..", "..", "assets", "css", "public.css"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(css), `content: "PASS"`) {
		t.Error("factory inspection CSS must not synthesize an unconditional PASS stamp")
	}
	if strings.Contains(string(css), `content: " ✓"`) {
		t.Error("factory inspection CSS must not synthesize an unconditional success mark")
	}
	for _, selector := range []string{".fcw-card-media::after", ".lc-cover::after"} {
		marker := `[data-theme-layout="factory-certwall"] ` + selector + " {"
		start := strings.Index(string(css), marker)
		if start < 0 {
			t.Errorf("factory certwall CSS missing %s", selector)
			continue
		}
		block := string(css)[start:]
		if end := strings.Index(block, "}"); end >= 0 {
			block = block[:end]
		}
		if strings.Contains(block, `"✓"`) {
			t.Errorf("factory certwall %s must not imply per-product verification", selector)
		}
	}
}
