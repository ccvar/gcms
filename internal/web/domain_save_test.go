package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// TestSaveSiteDomains 绑定域名：坏输入不再甩纯文本 400 页，而是 Flash 报出具体哪一行坏；
// 合法输入正常保存。
func TestSaveSiteDomains(t *testing.T) {
	_, h, ps, _, blogSite := setupPlatformAutomation(t)
	cookie := platformAdminSession(t, ps)
	sid := strconv.FormatInt(blogSite.ID, 10)

	post := func(form url.Values) *httptest.ResponseRecorder {
		form.Set("_csrf", "csrf")
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/sites/"+sid+"/domains", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		h.ServeHTTP(rec, req)
		return rec
	}
	sitesPage := func() string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "https://platform.test/admin/sites", nil)
		req.AddCookie(cookie)
		h.ServeHTTP(rec, req)
		return rec.Body.String()
	}

	// 1) 别名一行塞了两个域名（含空格）→ 303 回站点页 + Flash 指明具体哪一行坏。
	bad := post(url.Values{"primary_domain": {"bgvar.com"}, "alias_domains": {"www.bgvar.com blog.bgvar.com"}})
	if bad.Code != http.StatusSeeOther || bad.Header().Get("Location") != "/admin/sites" {
		t.Fatalf("bad alias: status=%d loc=%q (要求留在后台而不是裸 400 页)", bad.Code, bad.Header().Get("Location"))
	}
	body := sitesPage()
	if !strings.Contains(body, "绑定域名未保存") || !strings.Contains(body, "www.bgvar.com blog.bgvar.com") {
		t.Fatalf("flash 未指明出错的别名行")
	}

	// 2) 向导 AJAX 路径（Accept: JSON）：坏输入 → 400 JSON 带具体值（弹窗内就地报错、不跳页）。
	postJSON := func(form url.Values) *httptest.ResponseRecorder {
		form.Set("_csrf", "csrf")
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "https://platform.test/admin/sites/"+sid+"/domains", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.AddCookie(cookie)
		h.ServeHTTP(rec, req)
		return rec
	}
	jbad := postJSON(url.Values{"primary_domain": {"bgvar.com"}, "alias_domains": {"www.bgvar.com blog.bgvar.com"}})
	if jbad.Code != http.StatusBadRequest || !strings.Contains(strings.ReplaceAll(jbad.Body.String(), " ", ""), `"ok":false`) {
		t.Fatalf("json bad alias: status=%d body=%s", jbad.Code, jbad.Body.String())
	}
	if !strings.Contains(jbad.Body.String(), "www.bgvar.com blog.bgvar.com") {
		t.Fatalf("json error 未带出错的具体值: %s", jbad.Body.String())
	}
	// 向导 AJAX 成功 → 200 {ok:true, redirect}，前端据此跳回站点页（成功横幅已备好）。
	jgood := postJSON(url.Values{"primary_domain": {"jgood.bgvar.com"}, "redirect_aliases": {"1"}})
	if jgood.Code != http.StatusOK || !strings.Contains(jgood.Body.String(), "/admin/sites") {
		t.Fatalf("json good save: status=%d body=%s", jgood.Code, jgood.Body.String())
	}

	// 3) 原生提交兜底：合法输入 → 保存成功，域名落库。
	good := post(url.Values{"primary_domain": {"bgvar.com"}, "alias_domains": {"www.bgvar.com\r\nblog.bgvar.com"}, "redirect_aliases": {"1"}})
	if good.Code != http.StatusSeeOther {
		t.Fatalf("good save status=%d body=%q", good.Code, good.Body.String())
	}
	ds, err := ps.SiteDomains()
	if err != nil {
		t.Fatal(err)
	}
	hosts := map[string]bool{}
	for _, d := range ds {
		if d.SiteID == blogSite.ID {
			hosts[d.Host] = true
		}
	}
	for _, want := range []string{"bgvar.com", "www.bgvar.com", "blog.bgvar.com"} {
		if !hosts[want] {
			t.Fatalf("domain %s not saved; got %v", want, hosts)
		}
	}
}
