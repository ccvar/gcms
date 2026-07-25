package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPilotThemeUsesConfiguredProductData(t *testing.T) {
	s := newTestPublicServer(t, "")
	settings := map[string]string{
		"theme":                  "pilot-flight-deck",
		"site.hero_title":        "动态 Pilot 标题",
		"hero.visual":            "image",
		"hero.image":             "/uploads/pilot-real.webp",
		pilotWorkflowSettingKey:  `{"title":"动态流程","items":[{"title":"动态步骤","note":"来自站点设置"}]}`,
		pilotReleaseSettingKey:   `{"version":"9.7.3","date":"2099-12-31","channel":"Canary","url":"/notes"}`,
		pilotDownloadsSettingKey: `{"title":"动态下载","items":[{"label":"获取测试包","platform":"TestOS","arch":"RISC-X","url":"/downloads/test.pkg","meta":"42 MB"}]}`,
		pilotTrustSettingKey:     `{"title":"动态信任","items":[{"title":"动态确认","note":"真实说明"}]}`,
		pilotGallerySettingKey:   `["/uploads/pilot-gallery.webp"]`,
	}
	for key, value := range settings {
		if err := s.store.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	s.clearGeneratedCaches()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/zh/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"动态 Pilot 标题", "/uploads/pilot-real.webp", "动态流程", "动态步骤", "9.7.3", "Canary", "动态下载", "TestOS", "RISC-X", "/downloads/test.pkg", "动态信任", "动态确认", "/uploads/pilot-gallery.webp"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing configured value %q", want)
		}
	}
}

func TestPilotThemeUsesLocalizedDefaultsWithoutConfig(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting("theme", "pilot-flight-deck"); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetSetting("hero.image", "/uploads/pilot-default.webp"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/zh/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"从目标到交付", "连接工作目录", "每一步都在掌控中", "1.0.0", "/uploads/pilot-default.webp"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing default value %q", want)
		}
	}
	if strings.Contains(body, `class="ct-header-cta"`) {
		t.Error("Pilot header must not render the ambiguous view-all CTA")
	}
}

func TestPilotTemplateDoesNotBakeInProductData(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "templates", "partials", "home_pilot_flight_deck.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"macOS", "Windows", "Apple Silicon", "Stable", "v1.", "/downloads/pilot", "2026-"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("template hardcodes product data %q", forbidden)
		}
	}
	for _, binding := range []string{".Site.", ".PilotWorkflow", ".PilotRelease", ".PilotDownloads", ".PilotTrust", ".PilotGallery", ".Featured", ".FeatLinks", ".Categories"} {
		if !strings.Contains(string(body), binding) {
			t.Errorf("template does not consume %q", binding)
		}
	}
}

func TestPilotThemeIsVisibleInContentCategory(t *testing.T) {
	want := map[string]bool{
		"pilot-flight-deck":       false,
		"pilot-flight-deck-white": false,
		"pilot-flight-deck-dark":  false,
	}
	for _, theme := range Themes {
		if _, ok := want[theme.ID]; !ok {
			continue
		}
		if theme.Category != ThemeCategoryContent {
			t.Errorf("%s category=%q, want %q", theme.ID, theme.Category, ThemeCategoryContent)
		}
		want[theme.ID] = true
	}
	for id, found := range want {
		if !found {
			t.Errorf("Pilot theme %q is missing from registry", id)
		}
	}
}

func TestAppearanceThemeOptionsFormPilot(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting("theme", "pilot-flight-deck"); err != nil {
		t.Fatal(err)
	}
	cookie, _ := themeOptsAdminSession(t, s)
	body := themeOptsAppearanceHTML(t, s, cookie, "")
	for _, field := range []string{
		`name="hero_visual"`,
		`name="theme_opts" value="factory"`,
		`name="theme_opt_slot" value="pilot.workflow"`,
		`name="theme_opt_slot" value="pilot.release"`,
		`name="theme_opt_slot" value="pilot.downloads"`,
		`name="theme_opt_slot" value="pilot.trust"`,
		`name="theme_opt_slot" value="pilot.gallery"`,
		`name="pilot_workflow_title"`,
		`name="pilot_workflow_title_0"`,
		`name="pilot_release_version"`,
		`name="pilot_downloads_title"`,
		`name="pilot_download_label_0"`,
		`name="pilot_trust_title"`,
		`name="pilot_gallery_0"`,
		`value="从目标到交付"`,
		`value="下载 macOS 版"`,
	} {
		if !strings.Contains(body, field) {
			t.Errorf("Pilot 主题配置弹窗缺少 %s", field)
		}
	}
}

func TestAppearanceSavePilotVisualFields(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting("theme", "pilot-flight-deck"); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := themeOptsAdminSession(t, s)
	form := url.Values{
		"_csrf":                     {csrf},
		"theme":                     {"pilot-flight-deck"},
		"theme_opts":                {"factory"},
		"theme_opt_slot":            {pilotWorkflowSettingKey, pilotReleaseSettingKey, pilotDownloadsSettingKey, pilotTrustSettingKey, pilotGallerySettingKey},
		"pilot_workflow_title":      {"真实流程"},
		"pilot_workflow_note":       {"三步完成"},
		"pilot_workflow_title_0":    {"连接项目"},
		"pilot_workflow_note_0":     {"读取上下文"},
		"pilot_release_version":     {"2.4.1"},
		"pilot_release_date":        {"2026-07-25"},
		"pilot_release_channel":     {"Stable"},
		"pilot_release_url":         {"/release-notes"},
		"pilot_downloads_title":     {"获取 Pilot"},
		"pilot_download_label_0":    {"macOS"},
		"pilot_download_platform_0": {"macOS"},
		"pilot_download_arch_0":     {"Apple Silicon"},
		"pilot_download_url_0":      {"/downloads/pilot.dmg"},
		"pilot_download_meta_0":     {"124 MB"},
		"pilot_download_label_1":    {"无效地址会丢弃"},
		"pilot_download_url_1":      {"javascript:alert(1)"},
		"pilot_trust_title":         {"安全边界"},
		"pilot_trust_title_0":       {"本地执行"},
		"pilot_trust_note_0":        {"数据留在设备上"},
		"pilot_gallery_0":           {"/uploads/pilot-1.webp"},
		"pilot_gallery_1":           {"javascript:alert(1)"},
	}
	req := httptest.NewRequest(http.MethodPost, "https://example.test/admin/settings/appearance", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status = %d, body = %s", rec.Code, rec.Body.String())
	}

	workflow := parsePilotWorkflow(s.store.Setting(pilotWorkflowSettingKey))
	if workflow.Title != "真实流程" || len(workflow.Items) != 1 || workflow.Items[0].Note != "读取上下文" {
		t.Fatalf("workflow = %#v", workflow)
	}
	release := parsePilotRelease(s.store.Setting(pilotReleaseSettingKey))
	if release.Version != "2.4.1" || release.URL != "/release-notes" {
		t.Fatalf("release = %#v", release)
	}
	downloads := parsePilotDownloads(s.store.Setting(pilotDownloadsSettingKey))
	if len(downloads.Items) != 1 || downloads.Items[0].Arch != "Apple Silicon" {
		t.Fatalf("downloads = %#v", downloads)
	}
	trust := parsePilotTrust(s.store.Setting(pilotTrustSettingKey))
	if len(trust.Items) != 1 || trust.Items[0].Title != "本地执行" {
		t.Fatalf("trust = %#v", trust)
	}
	gallery := parsePilotGallery(s.store.Setting(pilotGallerySettingKey))
	if len(gallery) != 1 || gallery[0] != "/uploads/pilot-1.webp" {
		t.Fatalf("gallery = %#v", gallery)
	}
}
