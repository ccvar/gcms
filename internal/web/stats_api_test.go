package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
)

// newTestStatsServer 站点密钥 + 平台 Google 集成的统计端点测试夹具。
// bind 为 true 时给站点绑上 GSC / GA 集成与带有效 token 的授权账号。
func newTestStatsServer(t *testing.T, scopes string, bind bool) (*Server, string) {
	t.Helper()
	s, token := newTestAutomationServer(t, scopes)
	ps, err := platform.Open(filepath.Join(t.TempDir(), "system.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	s.platform = ps
	// site_google_integrations 有指向 sites 的外键，先建一行真实站点。
	site, err := ps.CreateSite("stats-site", "Stats Site", filepath.Join(t.TempDir(), "stats.db"), filepath.Join(t.TempDir(), "uploads"), true)
	if err != nil {
		t.Fatalf("create platform site: %v", err)
	}
	s.platformSiteID = site.ID
	if !bind {
		return s, token
	}
	for _, cfg := range []struct {
		service  string
		property string
	}{
		{platform.GoogleServiceSearchConsole, "sc-domain:example.com"},
		{platform.GoogleServiceAnalytics, "properties/123456"},
	} {
		if err := ps.UpsertGoogleAccount(&platform.GoogleAccount{
			Service:         cfg.service,
			GoogleAccountID: "acct-1",
			Email:           "stats@example.com",
			AccessToken:     "valid-token",
			TokenExpiry:     time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatalf("upsert google account: %v", err)
		}
		if err := ps.UpsertSiteGoogleIntegration(&platform.SiteGoogleIntegration{
			SiteID:          site.ID,
			Service:         cfg.service,
			GoogleAccountID: "acct-1",
			Property:        cfg.property,
			MeasurementID:   "G-TEST",
			Enabled:         true,
		}); err != nil {
			t.Fatalf("upsert integration: %v", err)
		}
	}
	return s, token
}

func statsGet(t *testing.T, s *Server, token, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	switch {
	case strings.Contains(path, "/stats/traffic"):
		s.apiStatsTraffic(w, req)
	case strings.Contains(path, "/stats/pages"):
		s.apiStatsPages(w, req)
	default:
		s.apiStatsSearch(w, req)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

// TestStatsScopeIssuable 钉死 v1.3.16 教训：stats:read 必须同时通过
// automationScopeValid 白名单和表单输出组装，缺一个都会让勾选被静默丢弃。
func TestStatsScopeIssuable(t *testing.T) {
	if !automationScopeValid(apiScopeStatsRead) {
		t.Fatalf("stats:read 应通过 automationScopeValid")
	}
	form := url.Values{"scopes": {"stats:read", "posts:read"}}
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	out := automationScopesFromFormRequired(req)
	found := false
	for _, sc := range out {
		if sc == apiScopeStatsRead {
			found = true
		}
	}
	if !found {
		t.Fatalf("表单签发结果缺少 stats:read：%v（白名单组装漏加）", out)
	}
}

func TestStatsEndpointsRequireScope(t *testing.T) {
	s, token := newTestStatsServer(t, "posts:read,posts:write", true)
	for _, path := range []string{"/api/admin/v1/stats/search", "/api/admin/v1/stats/traffic", "/api/admin/v1/stats/pages"} {
		w, out := statsGet(t, s, token, path)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s without stats:read = %d, want 403; body = %s", path, w.Code, w.Body.String())
		}
		if out["error"] != "missing_scope" {
			t.Fatalf("%s error = %v, want missing_scope", path, out["error"])
		}
	}
	// 无密钥 → 401。
	w, _ := statsGet(t, s, "", "/api/admin/v1/stats/search")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous stats = %d, want 401", w.Code)
	}
}

func TestStatsSearchCacheAndClamp(t *testing.T) {
	s, token := newTestStatsServer(t, "stats:read", true)
	calls := 0
	var gotDays, gotLimit int
	orig := statsSearchFetch
	statsSearchFetch = func(ctx context.Context, accessToken, property string, days, limit int) ([]statsSearchRow, error) {
		calls++
		gotDays, gotLimit = days, limit
		if accessToken != "valid-token" || property != "sc-domain:example.com" {
			t.Fatalf("fetch args = %q %q", accessToken, property)
		}
		return []statsSearchRow{{Query: "gcms 教程", Page: "https://example.com/posts/guide", Clicks: 12, Impressions: 340, Position: 9.4}}, nil
	}
	t.Cleanup(func() { statsSearchFetch = orig })

	// 越界参数钳制：days 500→90，limit 9999→1000。
	w, out := statsGet(t, s, token, "/api/admin/v1/stats/search?days=500&limit=9999")
	if w.Code != http.StatusOK || out["ok"] != true {
		t.Fatalf("search = %d %v", w.Code, out)
	}
	if gotDays != 90 || gotLimit != 1000 {
		t.Fatalf("clamped days/limit = %d/%d, want 90/1000", gotDays, gotLimit)
	}
	if out["days"].(float64) != 90 || out["property"] != "sc-domain:example.com" {
		t.Fatalf("response meta = %v", out)
	}
	rows := out["rows"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["query"] != "gcms 教程" {
		t.Fatalf("rows = %v", rows)
	}

	// 同参数再来一次：命中 1 小时缓存，不再调 Google。
	w2, out2 := statsGet(t, s, token, "/api/admin/v1/stats/search?days=500&limit=9999")
	if w2.Code != http.StatusOK || out2["ok"] != true {
		t.Fatalf("cached search = %d %v", w2.Code, out2)
	}
	if calls != 1 {
		t.Fatalf("fetch calls = %d, want 1（应命中缓存）", calls)
	}

	// 下界钳制：days=0 → 1（参数不同，是另一份缓存键）。
	if w3, _ := statsGet(t, s, token, "/api/admin/v1/stats/search?days=0"); w3.Code != http.StatusOK {
		t.Fatalf("days=0 status = %d", w3.Code)
	}
	if calls != 2 || gotDays != 1 {
		t.Fatalf("after days=0: calls=%d days=%d, want 2/1", calls, gotDays)
	}
}

func TestStatsTrafficCacheAndDefaults(t *testing.T) {
	s, token := newTestStatsServer(t, "stats:read", true)
	calls := 0
	var gotDays int
	orig := statsTrafficFetch
	statsTrafficFetch = func(ctx context.Context, accessToken, property string, days int) (statsTrafficSummary, error) {
		calls++
		gotDays = days
		return statsTrafficSummary{ActiveUsers: 88, Sessions: 120}, nil
	}
	t.Cleanup(func() { statsTrafficFetch = orig })

	w, out := statsGet(t, s, token, "/api/admin/v1/stats/traffic")
	if w.Code != http.StatusOK || out["ok"] != true {
		t.Fatalf("traffic = %d %v", w.Code, out)
	}
	if gotDays != 7 {
		t.Fatalf("default days = %d, want 7", gotDays)
	}
	if out["active_users"].(float64) != 88 || out["sessions"].(float64) != 120 || out["property"] != "properties/123456" {
		t.Fatalf("traffic payload = %v", out)
	}
	if w2, _ := statsGet(t, s, token, "/api/admin/v1/stats/traffic"); w2.Code != http.StatusOK {
		t.Fatalf("cached traffic failed")
	}
	if calls != 1 {
		t.Fatalf("fetch calls = %d, want 1（应命中缓存）", calls)
	}
}

// TestMergeStatsCompareRows 合并纯函数：按 query+page 附加前期数据，前期没有的行 prev_* 为 null；
// 只存在于前期的行不进结果（基准是当前区间）。
func TestMergeStatsCompareRows(t *testing.T) {
	cur := []statsSearchRow{
		{Query: "gcms 教程", Page: "https://e.com/a", Clicks: 12, Impressions: 340, Position: 9.4},
		{Query: "新词", Page: "https://e.com/b", Clicks: 3, Impressions: 50, Position: 18.2},
	}
	prev := []statsSearchRow{
		{Query: "gcms 教程", Page: "https://e.com/a", Clicks: 5, Impressions: 210, Position: 14.2},
		{Query: "gcms 教程", Page: "https://e.com/other", Clicks: 9, Impressions: 100, Position: 6.0}, // 同 query 不同 page，不该串
		{Query: "掉出的词", Page: "https://e.com/c", Clicks: 7, Impressions: 90, Position: 4.1},         // 仅前期，有意丢弃
	}
	rows := mergeStatsCompareRows(cur, prev)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	first := rows[0]
	if first.PrevClicks == nil || *first.PrevClicks != 5 || first.PrevImpressions == nil || *first.PrevImpressions != 210 || first.PrevPosition == nil || *first.PrevPosition != 14.2 {
		t.Fatalf("first prev = %+v", first)
	}
	if first.Clicks != 12 || first.Impressions != 340 || first.Position != 9.4 {
		t.Fatalf("first current 被合并改写：%+v", first)
	}
	second := rows[1]
	if second.PrevClicks != nil || second.PrevImpressions != nil || second.PrevPosition != nil {
		t.Fatalf("前期无数据应置 null：%+v", second)
	}
}

// TestStatsCompareWindows 紧前区间必须与当前区间等长且首尾相接。
func TestStatsCompareWindows(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	curStart, curEnd, prevStart, prevEnd := statsCompareWindows(now, 7)
	if curEnd.Format("2006-01-02") != "2026-07-13" || curStart.Format("2006-01-02") != "2026-07-07" {
		t.Fatalf("current window = %s..%s", curStart, curEnd)
	}
	if prevEnd.Format("2006-01-02") != "2026-07-06" || prevStart.Format("2006-01-02") != "2026-06-30" {
		t.Fatalf("prev window = %s..%s", prevStart, prevEnd)
	}
}

// TestStatsSearchCompare compare=1：服务端拉当前 + 紧前两份数据并合并；缓存键与非 compare 区分。
func TestStatsSearchCompare(t *testing.T) {
	s, token := newTestStatsServer(t, "stats:read", true)
	var windows [][2]string
	orig := statsSearchRangeFetch
	statsSearchRangeFetch = func(ctx context.Context, accessToken, property string, start, end time.Time, limit int) ([]statsSearchRow, error) {
		windows = append(windows, [2]string{start.Format("2006-01-02"), end.Format("2006-01-02")})
		if len(windows) == 1 { // 当前区间
			return []statsSearchRow{{Query: "gcms", Page: "https://e.com/a", Clicks: 10, Impressions: 200, Position: 9.0}}, nil
		}
		return []statsSearchRow{{Query: "gcms", Page: "https://e.com/a", Clicks: 4, Impressions: 90, Position: 15.5}}, nil
	}
	t.Cleanup(func() { statsSearchRangeFetch = orig })

	w, out := statsGet(t, s, token, "/api/admin/v1/stats/search?days=7&compare=1")
	if w.Code != http.StatusOK || out["ok"] != true || out["compare"] != true {
		t.Fatalf("compare = %d %v", w.Code, out)
	}
	if len(windows) != 2 {
		t.Fatalf("range fetch 次数 = %d, want 2（当前 + 紧前）", len(windows))
	}
	// 两个区间等长且首尾相接
	curStart, _ := time.Parse("2006-01-02", windows[0][0])
	prevEnd, _ := time.Parse("2006-01-02", windows[1][1])
	if !prevEnd.AddDate(0, 0, 1).Equal(curStart) {
		t.Fatalf("区间不相接：cur=%v prev=%v", windows[0], windows[1])
	}
	rows := out["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	row := rows[0].(map[string]any)
	if row["clicks"].(float64) != 10 || row["prev_clicks"].(float64) != 4 || row["prev_position"].(float64) != 15.5 {
		t.Fatalf("merged row = %v", row)
	}

	// 命中缓存：不再调 Google
	if w2, _ := statsGet(t, s, token, "/api/admin/v1/stats/search?days=7&compare=1"); w2.Code != http.StatusOK {
		t.Fatalf("cached compare failed")
	}
	if len(windows) != 2 {
		t.Fatalf("compare 缓存未命中：fetch 次数 = %d", len(windows))
	}
}

// TestStatsPages page-stats：默认 days=7/limit=50，钳制与 1 小时缓存与其它 stats 端点一致。
func TestStatsPages(t *testing.T) {
	s, token := newTestStatsServer(t, "stats:read", true)
	calls := 0
	var gotDays, gotLimit int
	orig := statsPagesFetch
	statsPagesFetch = func(ctx context.Context, accessToken, property string, days, limit int) ([]statsPageRow, error) {
		calls++
		gotDays, gotLimit = days, limit
		return []statsPageRow{{Path: "/zh/posts/guide/", ActiveUsers: 66, Sessions: 80}}, nil
	}
	t.Cleanup(func() { statsPagesFetch = orig })

	w, out := statsGet(t, s, token, "/api/admin/v1/stats/pages")
	if w.Code != http.StatusOK || out["ok"] != true {
		t.Fatalf("pages = %d %v", w.Code, out)
	}
	if gotDays != 7 || gotLimit != 50 {
		t.Fatalf("defaults days/limit = %d/%d, want 7/50", gotDays, gotLimit)
	}
	rows := out["rows"].([]any)
	row := rows[0].(map[string]any)
	if row["path"] != "/zh/posts/guide/" || row["active_users"].(float64) != 66 || row["sessions"].(float64) != 80 {
		t.Fatalf("rows = %v", rows)
	}
	// 缓存命中
	if w2, _ := statsGet(t, s, token, "/api/admin/v1/stats/pages"); w2.Code != http.StatusOK {
		t.Fatalf("cached pages failed")
	}
	if calls != 1 {
		t.Fatalf("fetch calls = %d, want 1（应命中缓存）", calls)
	}
	// 越界钳制
	if w3, _ := statsGet(t, s, token, "/api/admin/v1/stats/pages?days=500&limit=9999"); w3.Code != http.StatusOK {
		t.Fatalf("clamped pages failed")
	}
	if gotDays != 90 || gotLimit != 1000 {
		t.Fatalf("clamped days/limit = %d/%d, want 90/1000", gotDays, gotLimit)
	}
}

// TestStatsRoutePrecedence 走真实 mux：字面 /stats/search 必须命中统计处理器，
// 而不是被 GET /{collection}/{id} 通配吞掉（平台命名空间的老坑，站点侧同样钉住）。
func TestStatsRoutePrecedence(t *testing.T) {
	s := newTestPublicServer(t, "")
	token, prefix := newAutomationToken()
	if _, err := s.store.CreateAutomationKey("stats", token, prefix, "stats:read"); err != nil {
		t.Fatalf("create key: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/v1/stats/search", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	// 命中统计处理器：单站无平台库 → 400 search_console_not_connected；
	// 若被 {collection}/{id} 通配接走，会是 404 未知集合或 403 缺内容权限。
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != http.StatusBadRequest || out["error"] != "search_console_not_connected" {
		t.Fatalf("stats route = %d %v, want 400 search_console_not_connected", w.Code, out)
	}
}

func TestStatsNotConnectedErrors(t *testing.T) {
	// 平台在但未绑定集成。
	s, token := newTestStatsServer(t, "stats:read", false)
	w, out := statsGet(t, s, token, "/api/admin/v1/stats/search")
	if w.Code != http.StatusBadRequest || out["error"] != "search_console_not_connected" {
		t.Fatalf("unbound search = %d %v, want 400 search_console_not_connected", w.Code, out)
	}
	w2, out2 := statsGet(t, s, token, "/api/admin/v1/stats/traffic")
	if w2.Code != http.StatusBadRequest || out2["error"] != "analytics_not_connected" {
		t.Fatalf("unbound traffic = %d %v, want 400 analytics_not_connected", w2.Code, out2)
	}

	// 单站模式（无平台库）同样返回明确错误码。
	single, singleToken := newTestAutomationServer(t, "stats:read")
	w3, out3 := statsGet(t, single, singleToken, "/api/admin/v1/stats/search")
	if w3.Code != http.StatusBadRequest || out3["error"] != "search_console_not_connected" {
		t.Fatalf("single-site search = %d %v", w3.Code, out3)
	}
}
