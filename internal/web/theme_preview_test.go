package web

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestThemePreviewRendersAllThemes 渲染每一个注册主题的后台预览页：
// 任何皮肤/骨架登记不一致（Themes 有但 CSS/模板缺）或模板执行错误都会在这里翻车。
func TestThemePreviewRendersAllThemes(t *testing.T) {
	_, h, ps, _, blogSite := setupPlatformAutomation(t)
	cookie := platformAdminSession(t, ps)
	layoutSkeletons := map[string]string{
		"almanac": "al-wrap", "axis": "axis-wrap", "bands": "bands-band bands-hero",
		"bento": "bento-wrap", "bloom": "bloom-wrap", "board": "board-wrap",
		"broadcast": "bc-wrap", "catalog": "ct-wrap", "cinema": "cin-reel",
		"collage": "col-wrap", "constellation": "cst", "deck": "deck-shell",
		"desktop": "dsk-wrap", "exhibit": "ex-wrap", "feed": "fd-wrap",
		"factory-catalog": "fc-wrap", "factory-showcase": "fs-wrap",
		"factory-onepage": "fo-wrap", "factory-solutions": "fx-wrap",
		"factory-engineering": "fe-wrap", "factory-trade": "ft-wrap",
		"factory-sidebar": "fb-wrap", "factory-vision": "fv-wrap",
		"factory-herofold": "fh-wrap",
		"gazette":          "gz-wrap", "inbox": "ib-wrap", "index": "index-wrap",
		"liftoff": "lo-hero", "manual": "mn-wrap", "masonry": "ms-wrap",
		"poster": "poster-scroll", "profile": "prof-wrap", "split": "split-hero",
		"ticker": "tick-marquee", "timeline": "tl-wrap", "uptime": "up-wrap",
	}

	// 进入站点后台，让会话绑定一个站点（站点级路由需要 current site）。
	enter := httptest.NewRecorder()
	enterReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/posts", nil)
	enterReq.AddCookie(cookie)
	h.ServeHTTP(enter, enterReq)

	if len(Themes) == 0 {
		t.Fatal("Themes registry is empty")
	}
	for _, th := range Themes {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/theme-preview/"+th.ID, nil)
		req.AddCookie(cookie)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("theme %q preview status = %d", th.ID, rec.Code)
			continue
		}
		body := rec.Body.String()
		if !strings.Contains(body, `data-theme="`+th.ID+`"`) {
			t.Errorf("theme %q preview missing data-theme attr", th.ID)
		}
		wantLayout := layoutForTheme(th.ID)
		if !strings.Contains(body, `data-theme-layout="`+wantLayout+`"`) {
			t.Errorf("theme %q preview missing data-theme-layout=%q", th.ID, wantLayout)
		}
		if skeleton, ok := layoutSkeletons[wantLayout]; ok && !strings.Contains(body, `class="`+skeleton+`"`) {
			t.Errorf("theme %q preview missing %s skeleton", th.ID, skeleton)
		}
	}

	// 皮肤 CSS 必须真的存在于被服务的 public.css：防止只登记不写皮。
	// 例外：默认主题 editorial 走 :root 基础变量；与骨架同名的"原生主题"（如 sidebar）
	// 可以骑默认调色板、只靠 data-theme-layout 布局 CSS。
	for _, th := range Themes {
		if th.ID == "editorial" || th.ID == layoutForTheme(th.ID) {
			continue
		}
		if !publicCSSHasTheme(t, h, cookie, th.ID) {
			t.Errorf("public.css missing [data-theme=%q] block", th.ID)
		}
	}
}

func TestLightThemePairsCoverEveryLayout(t *testing.T) {
	pairs := map[string][2]string{
		"topbar": {"paperwhite", "citrus"}, "sidebar": {"bookshop", "canal"},
		"bento": {"confetti", "icebox"}, "index": {"ledger", "signal"},
		"split": {"gallery", "coast"}, "axis": {"monument", "petal"},
		"bands": {"market", "seaside"}, "ticker": {"daytrade", "mintwire"},
		"liftoff": {"sunrise", "horizon"}, "board": {"workshop", "playbook"},
		"timeline": {"chronicle", "gardenpath"}, "deck": {"portfolio", "postcard"},
		"poster": {"atelier", "festival"}, "uptime": {"daywatch", "clinic"},
		"profile": {"peach", "skyline"}, "bloom": {"herbarium", "coralreef"},
		"desktop": {"cloudos", "candyglass"}, "cinema": {"paperfilm", "azurefilm"},
		"collage": {"cutpaper", "primary"}, "constellation": {"atlas", "mintmap"},
		"masonry": {"pinboard", "spectrum"}, "feed": {"daybook", "civic"},
		"gazette": {"broadsheet", "salmonpress"}, "manual": {"fieldguide", "bluebook"},
		"almanac": {"sunclock", "seedcalendar"}, "inbox": {"postbox", "airmail"},
		"catalog": {"apothecary", "toolroom"}, "broadcast": {"publicradio", "morningfm"},
		"exhibit": {"whitecube", "botanical"},
	}
	if len(pairs) != 29 {
		t.Fatalf("light theme layout count = %d, want 29", len(pairs))
	}

	registered := make(map[string]bool, len(Themes))
	for _, theme := range Themes {
		registered[theme.ID] = true
	}
	for layout, ids := range pairs {
		for _, id := range ids {
			if !registered[id] {
				t.Errorf("light theme %q is not registered", id)
			}
			if got := layoutForTheme(id); got != layout {
				t.Errorf("light theme %q layout = %q, want %q", id, got, layout)
			}
		}
	}
}

var publicCSSCache string

func publicCSSHasTheme(t *testing.T, h http.Handler, cookie *http.Cookie, id string) bool {
	t.Helper()
	if publicCSSCache == "" {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://platform.test/assets/css/public.css", nil)
		req.AddCookie(cookie)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("fetch public.css status = %d", rec.Code)
		}
		publicCSSCache = rec.Body.String()
	}
	return strings.Contains(publicCSSCache, `[data-theme="`+id+`"]`)
}
