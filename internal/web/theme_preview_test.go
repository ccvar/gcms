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
