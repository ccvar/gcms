package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestThemeBrowse 主题试穿预览：真实站点数据 + 候选主题渲染 + 站内可翻页 + 不污染公共缓存。
func TestThemeBrowse(t *testing.T) {
	_, h, ps, _, blogSite := setupPlatformAutomation(t)
	cookie := platformAdminSession(t, ps)

	// 会话绑定站点（站点级 /admin 路由需要 current site）。
	enter := httptest.NewRecorder()
	enterReq := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites/"+strconv.FormatInt(blogSite.ID, 10)+"/posts", nil)
	enterReq.AddCookie(cookie)
	h.ServeHTTP(enter, enterReq)

	get := func(path string, withAuth bool) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://platform.test"+path, nil)
		if withAuth {
			req.AddCookie(cookie)
		}
		h.ServeHTTP(rec, req)
		return rec
	}

	// 1) 未登录 → 跳登录，不泄内容。
	if rec := get("/admin/theme-browse/gazette/", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("unauthed browse status = %d, want 303", rec.Code)
	}

	// 2) 试穿 gazette：候选主题渲染 + noindex + 站内链接改写进前缀。
	rec := get("/admin/theme-browse/gazette/", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("browse status = %d, body=%s", rec.Code, rec.Body.String()[:200])
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-theme="gazette"`) || !strings.Contains(body, `data-theme-layout="gazette"`) {
		t.Fatalf("browse did not render candidate theme")
	}
	if got := rec.Header().Get("X-Robots-Tag"); got != "noindex, nofollow" {
		t.Fatalf("browse robots header = %q", got)
	}
	if !strings.Contains(body, `href="/admin/theme-browse/gazette/`) {
		t.Fatalf("internal links not rewritten under browse prefix")
	}

	// 3) 可翻页：跟随页面里第一条改写后的内容页链接（排除 rss/sitemap 等资源），仍是候选主题。
	m := regexp.MustCompile(`href="(/admin/theme-browse/gazette/[a-z]{2}/[^".]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no navigable rewritten link found in browse page")
	}
	next := get(m[1], true)
	if next.Code != http.StatusOK {
		t.Fatalf("navigate %s status = %d", m[1], next.Code)
	}
	if !strings.Contains(next.Body.String(), `data-theme="gazette"`) {
		t.Fatalf("navigated page lost the candidate theme")
	}

	// 4) 公共前台不受影响（也证明试穿页没进公共缓存）：站点自身主题照旧。
	pub := httptest.NewRecorder()
	pubReq := httptest.NewRequest(http.MethodGet, "https://blog.test/zh/", nil)
	h.ServeHTTP(pub, pubReq)
	if pub.Code != http.StatusOK {
		t.Fatalf("public home status = %d", pub.Code)
	}
	if strings.Contains(pub.Body.String(), `data-theme="gazette"`) {
		t.Fatalf("public page leaked the preview theme (cache pollution?)")
	}

	// 5) 外观设置页渲染出每张卡的「真实预览」入口 + 弹窗里的链接位。
	appearance := get("/admin/settings/appearance", true)
	if appearance.Code != http.StatusOK {
		t.Fatalf("appearance page status = %d", appearance.Code)
	}
	ab := appearance.Body.String()
	if !strings.Contains(ab, `href="/admin/theme-browse/gazette/"`) || !strings.Contains(ab, `data-tp-browse`) {
		t.Fatalf("appearance page missing theme-browse entries")
	}

	// 6) 非法主题 / 越界路径 → 404。
	if rec := get("/admin/theme-browse/not-a-theme/", true); rec.Code != http.StatusNotFound {
		t.Fatalf("invalid theme status = %d, want 404", rec.Code)
	}
	if rec := get("/admin/theme-browse/gazette/admin/settings", true); rec.Code != http.StatusNotFound {
		t.Fatalf("recursion path status = %d, want 404", rec.Code)
	}
}
