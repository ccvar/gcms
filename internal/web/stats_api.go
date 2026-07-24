package web

// 统计数据开放给 AI（stats:read）：把站点已绑定的 Google Search Console / GA 集成
// 以自动化 API 的形式暴露出来，供 AI 找排名 8~20 的搜索词优化旧文、观察流量变化。
// 结果按「端点|property|参数」在内存缓存 1 小时，防止打爆 Google 配额。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cms.ccvar.com/internal/platform"
)

const (
	statsCacheTTL           = time.Hour
	statsDaysMin            = 1
	statsDaysMax            = 90
	statsSearchDefaultDays  = 28
	statsTrafficDefaultDays = 7
	statsPagesDefaultDays   = 7
	statsAnalyticsDefault   = "sources"
	statsSearchDefaultLimit = 100
	statsPagesDefaultLimit  = 50
	statsSearchLimitMax     = 1000
	statsSearchGroupDefault = "query_page"
)

// statsSearchRow Search Console 搜索词 × 页面的一行表现数据。
type statsSearchRow struct {
	Query       string  `json:"query"`
	Page        string  `json:"page"`
	Date        string  `json:"date,omitempty"`
	Clicks      int     `json:"clicks"`
	Impressions int     `json:"impressions"`
	CTR         float64 `json:"ctr"`
	Position    float64 `json:"position"`
}

// statsSearchCompareRow compare=1 时的行：在当前区间行上追加紧前等长区间的同 key 数据
// （前期无同 key 数据时四个 prev_* 置 null）。
type statsSearchCompareRow struct {
	statsSearchRow
	PrevClicks      *int     `json:"prev_clicks"`
	PrevImpressions *int     `json:"prev_impressions"`
	PrevCTR         *float64 `json:"prev_ctr"`
	PrevPosition    *float64 `json:"prev_position"`
}

// statsPageRow GA4 按 pagePath 维度的一行流量数据。
type statsPageRow struct {
	Path                   string  `json:"path"`
	ActiveUsers            int     `json:"active_users"`
	Sessions               int     `json:"sessions"`
	EngagementRate         float64 `json:"engagement_rate"`
	AverageSessionDuration float64 `json:"average_session_duration"`
}

// statsTrafficSummary GA4 的活跃用户 / 会话 / 互动质量汇总。
type statsTrafficSummary struct {
	ActiveUsers            int     `json:"active_users"`
	Sessions               int     `json:"sessions"`
	EngagementRate         float64 `json:"engagement_rate"`
	AverageSessionDuration float64 `json:"average_session_duration"`
}

// statsAnalyticsRow 是来源、地区、设备与每日趋势共用的 GA4 报告行。
// Values 的顺序与响应中的 dimensions 一致，避免为每种维度复制一套传输结构。
type statsAnalyticsRow struct {
	Values                 []string `json:"values"`
	ActiveUsers            int      `json:"active_users"`
	Sessions               int      `json:"sessions"`
	EngagementRate         float64  `json:"engagement_rate"`
	AverageSessionDuration float64  `json:"average_session_duration"`
}

type statsAnalyticsReport struct {
	Dimensions []string            `json:"dimensions"`
	Rows       []statsAnalyticsRow `json:"rows"`
}

type statsAnalyticsSpec struct {
	Group      string
	Dimensions []string
}

type statsCacheEntry struct {
	payload map[string]any
	expires time.Time
}

// 抓取函数可注入：生产为真实 Google API 调用，测试替换成桩以验证缓存与参数钳制。
var (
	statsSearchFetch           = googleSearchConsoleTopQueries
	statsSearchRangeFetch      = googleSearchConsoleQueriesRange
	statsSearchDimensionsFetch = googleSearchConsoleRowsRange
	statsTrafficFetch          = googleAnalyticsTrafficSummary
	statsPagesFetch            = googleAnalyticsPagesReport
	statsAnalyticsFetch        = googleAnalyticsDimensionReport
)

func (s *Server) statsCacheGet(key string) (map[string]any, bool) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	e, ok := s.statsCache[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.payload, true
}

func (s *Server) statsCachePut(key string, payload map[string]any) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	if s.statsCache == nil {
		s.statsCache = map[string]statsCacheEntry{}
	}
	s.statsCache[key] = statsCacheEntry{payload: payload, expires: time.Now().Add(statsCacheTTL)}
}

// statsClampDays 解析 days 参数并钳制到 1..90，缺省 / 非法回落默认值。
func statsClampDays(raw string, def int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < statsDaysMin {
		return statsDaysMin
	}
	if n > statsDaysMax {
		return statsDaysMax
	}
	return n
}

// statsClampLimit 解析 limit 参数并钳制到 1..1000，缺省 / 非法回落 def。
func statsClampLimit(raw string, def int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < 1 {
		return 1
	}
	if n > statsSearchLimitMax {
		return statsSearchLimitMax
	}
	return n
}

// statsAnalyticsGroupSpec 约束客户端可读取的 GA4 维度组合。
// 只开放产品实际展示的组合，避免把任意维度/指标透传给 Google。
func statsAnalyticsGroupSpec(raw string) (statsAnalyticsSpec, bool) {
	group := strings.ToLower(strings.TrimSpace(raw))
	if group == "" {
		group = statsAnalyticsDefault
	}
	switch group {
	case "sources":
		return statsAnalyticsSpec{
			Group:      group,
			Dimensions: []string{"sessionDefaultChannelGroup", "sessionSourceMedium"},
		}, true
	case "geography":
		return statsAnalyticsSpec{
			Group:      group,
			Dimensions: []string{"country", "region"},
		}, true
	case "devices":
		return statsAnalyticsSpec{
			Group:      group,
			Dimensions: []string{"deviceCategory", "operatingSystem", "browser"},
		}, true
	case "trend":
		return statsAnalyticsSpec{
			Group:      group,
			Dimensions: []string{"date"},
		}, true
	default:
		return statsAnalyticsSpec{Group: group}, false
	}
}

// statsSearchGroupDimensions 把前端需要的视图映射为 GSC Search Analytics 维度。
// query_page 保留既有接口语义；total 不传 dimensions，得到该时间段的精确汇总。
func statsSearchGroupDimensions(raw string) ([]string, string, bool) {
	group := strings.ToLower(strings.TrimSpace(raw))
	if group == "" {
		group = statsSearchGroupDefault
	}
	switch group {
	case statsSearchGroupDefault:
		return []string{"query", "page"}, group, true
	case "query":
		return []string{"query"}, group, true
	case "page":
		return []string{"page"}, group, true
	case "date":
		return []string{"date"}, group, true
	case "total":
		return nil, group, true
	default:
		return nil, group, false
	}
}

// statsGoogleIntegration 取当前站点已绑定且启用的 Google 集成与授权账号。
// errCode 非空表示不可用（未绑定 / 账号缺失），错误码给 AI 一个明确的失败原因。
func (s *Server) statsGoogleIntegration(service string) (in *platform.SiteGoogleIntegration, acc *platform.GoogleAccount, errCode, errMsg string) {
	notConnectedCode := "search_console_not_connected"
	notConnectedMsg := "该站点尚未接入 Google Search Console，无法读取搜索数据。"
	if service == platform.GoogleServiceAnalytics {
		notConnectedCode = "analytics_not_connected"
		notConnectedMsg = "该站点尚未接入 Google Analytics，无法读取流量数据。"
	}
	if s.platform == nil || s.platformSiteID <= 0 {
		return nil, nil, notConnectedCode, notConnectedMsg
	}
	integration, ok, err := s.platform.SiteGoogleIntegration(s.platformSiteID, service)
	if err != nil {
		return nil, nil, "store_error", err.Error()
	}
	if !ok || !integration.Enabled || strings.TrimSpace(integration.GoogleAccountID) == "" || strings.TrimSpace(integration.Property) == "" {
		return nil, nil, notConnectedCode, notConnectedMsg
	}
	account, ok, err := s.platform.GoogleAccount(service, integration.GoogleAccountID)
	if err != nil {
		return nil, nil, "store_error", err.Error()
	}
	if !ok {
		return nil, nil, "google_account_missing", "没有找到对应的 Google 授权账号，请在站点管理里重新授权。"
	}
	return integration, account, "", ""
}

// apiStatsSearch GET /stats/search?days=28&limit=100[&compare=1][&group=query_page]。
// group 支持 query_page（默认）、query、page、date、total；行统一带 clicks、impressions、
// ctr 与 position，date 组额外带 date。结果缓存 1 小时，fresh=1 可显式绕过缓存。
// compare=1 时服务端另拉「紧前等长区间」一份数据，按当前 group 的稳定键合并进当前行；
// date 不支持 compare（两个区间的日期不会一一对应）。
func (s *Server) apiStatsSearch(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAutomationScope(w, r, apiScopeStatsRead); !ok {
		return
	}
	days := statsClampDays(r.URL.Query().Get("days"), statsSearchDefaultDays)
	limit := statsClampLimit(r.URL.Query().Get("limit"), statsSearchDefaultLimit)
	compare := parseAPIBool(r.URL.Query().Get("compare"))
	dimensions, group, validGroup := statsSearchGroupDimensions(r.URL.Query().Get("group"))
	if !validGroup {
		apiError(w, http.StatusBadRequest, "invalid_group", "group 仅支持 query_page、query、page、date 或 total。")
		return
	}
	if compare && group == "date" {
		apiError(w, http.StatusBadRequest, "compare_not_supported", "date 维度不支持 compare；请分别查询日期趋势。")
		return
	}
	if group == "total" {
		limit = 1
	}
	fresh := parseAPIBool(r.URL.Query().Get("fresh"))
	in, acc, errCode, errMsg := s.statsGoogleIntegration(platform.GoogleServiceSearchConsole)
	if errCode != "" {
		apiError(w, http.StatusBadRequest, errCode, errMsg)
		return
	}
	cacheKey := fmt.Sprintf("search|%s|%s|%d|%d|%t", in.Property, group, days, limit, compare)
	if !fresh {
		if payload, ok := s.statsCacheGet(cacheKey); ok {
			writeJSON(w, http.StatusOK, payload)
			return
		}
	}
	token, err := s.googleAccessToken(r.Context(), r, acc)
	if err != nil {
		apiError(w, http.StatusBadGateway, "google_auth_failed", err.Error())
		return
	}
	var payload map[string]any
	if compare {
		curStart, curEnd, prevStart, prevEnd := statsCompareWindows(time.Now(), days)
		var cur, prev []statsSearchRow
		if group == statsSearchGroupDefault {
			cur, err = statsSearchRangeFetch(r.Context(), token, in.Property, curStart, curEnd, limit)
		} else {
			cur, err = statsSearchDimensionsFetch(r.Context(), token, in.Property, curStart, curEnd, limit, dimensions)
		}
		if err != nil {
			apiError(w, http.StatusBadGateway, "google_api_error", err.Error())
			return
		}
		if group == statsSearchGroupDefault {
			prev, err = statsSearchRangeFetch(r.Context(), token, in.Property, prevStart, prevEnd, limit)
		} else {
			prev, err = statsSearchDimensionsFetch(r.Context(), token, in.Property, prevStart, prevEnd, limit, dimensions)
		}
		if err != nil {
			apiError(w, http.StatusBadGateway, "google_api_error", err.Error())
			return
		}
		payload = map[string]any{"ok": true, "days": days, "property": in.Property, "group": group, "compare": true, "rows": mergeStatsCompareRowsByGroup(cur, prev, group)}
	} else {
		var rows []statsSearchRow
		if group == statsSearchGroupDefault {
			rows, err = statsSearchFetch(r.Context(), token, in.Property, days, limit)
		} else {
			start, end, _, _ := statsCompareWindows(time.Now(), days)
			rows, err = statsSearchDimensionsFetch(r.Context(), token, in.Property, start, end, limit, dimensions)
		}
		if err != nil {
			apiError(w, http.StatusBadGateway, "google_api_error", err.Error())
			return
		}
		if rows == nil {
			rows = []statsSearchRow{}
		}
		payload = map[string]any{"ok": true, "days": days, "property": in.Property, "group": group, "rows": rows}
	}
	s.statsCachePut(cacheKey, payload)
	writeJSON(w, http.StatusOK, payload)
}

// statsCompareWindows 纯函数：当前区间（截至昨天、长 days 天）与紧前等长区间的起止日期。
func statsCompareWindows(now time.Time, days int) (curStart, curEnd, prevStart, prevEnd time.Time) {
	curEnd = now.AddDate(0, 0, -1)
	curStart = curEnd.AddDate(0, 0, -(days - 1))
	prevEnd = curStart.AddDate(0, 0, -1)
	prevStart = prevEnd.AddDate(0, 0, -(days - 1))
	return curStart, curEnd, prevStart, prevEnd
}

// mergeStatsCompareRows 纯函数：以当前区间行为基准，按 query+page 附加前期数据；
// 前期没有该 key 时 prev_* 保持 null。只存在于前期的行不进结果（基准是当前表现）。
func mergeStatsCompareRows(cur, prev []statsSearchRow) []statsSearchCompareRow {
	return mergeStatsCompareRowsByGroup(cur, prev, statsSearchGroupDefault)
}

func statsSearchRowKey(row statsSearchRow, group string) string {
	switch group {
	case "query":
		return row.Query
	case "page":
		return row.Page
	case "date":
		return row.Date
	case "total":
		return "total"
	default:
		return row.Query + "\x00" + row.Page
	}
}

// mergeStatsCompareRowsByGroup 是各聚合视图共用的环比合并；只保留当前期出现的行。
func mergeStatsCompareRowsByGroup(cur, prev []statsSearchRow, group string) []statsSearchCompareRow {
	idx := make(map[string]statsSearchRow, len(prev))
	for _, row := range prev {
		idx[statsSearchRowKey(row, group)] = row
	}
	out := make([]statsSearchCompareRow, 0, len(cur))
	for _, row := range cur {
		item := statsSearchCompareRow{statsSearchRow: row}
		if p, ok := idx[statsSearchRowKey(row, group)]; ok {
			clicks, impressions, ctr, position := p.Clicks, p.Impressions, p.CTR, p.Position
			item.PrevClicks, item.PrevImpressions, item.PrevCTR, item.PrevPosition = &clicks, &impressions, &ctr, &position
		}
		out = append(out, item)
	}
	return out
}

// apiStatsTraffic GET /stats/traffic?days=7：GA4 活跃用户 / 会话汇总。
// 响应 {ok,days,property,active_users,sessions}，缓存 1 小时。
func (s *Server) apiStatsTraffic(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAutomationScope(w, r, apiScopeStatsRead); !ok {
		return
	}
	days := statsClampDays(r.URL.Query().Get("days"), statsTrafficDefaultDays)
	fresh := parseAPIBool(r.URL.Query().Get("fresh"))
	in, acc, errCode, errMsg := s.statsGoogleIntegration(platform.GoogleServiceAnalytics)
	if errCode != "" {
		apiError(w, http.StatusBadRequest, errCode, errMsg)
		return
	}
	cacheKey := fmt.Sprintf("traffic|%s|%d", in.Property, days)
	if !fresh {
		if payload, ok := s.statsCacheGet(cacheKey); ok {
			writeJSON(w, http.StatusOK, payload)
			return
		}
	}
	token, err := s.googleAccessToken(r.Context(), r, acc)
	if err != nil {
		apiError(w, http.StatusBadGateway, "google_auth_failed", err.Error())
		return
	}
	sum, err := statsTrafficFetch(r.Context(), token, in.Property, days)
	if err != nil {
		apiError(w, http.StatusBadGateway, "google_api_error", err.Error())
		return
	}
	payload := map[string]any{
		"ok":                       true,
		"days":                     days,
		"property":                 in.Property,
		"active_users":             sum.ActiveUsers,
		"sessions":                 sum.Sessions,
		"engagement_rate":          sum.EngagementRate,
		"average_session_duration": sum.AverageSessionDuration,
	}
	s.statsCachePut(cacheKey, payload)
	writeJSON(w, http.StatusOK, payload)
}

// googleSearchConsoleTopQueries 调 Search Console searchanalytics.query（dimensions: query+page），
// 返回近 days 天各搜索词 × 页面的点击、曝光与平均排名（Google 按点击降序返回）。
func googleSearchConsoleTopQueries(ctx context.Context, accessToken, siteURL string, days, limit int) ([]statsSearchRow, error) {
	end := time.Now().AddDate(0, 0, -1)
	start := end.AddDate(0, 0, -(days - 1))
	return googleSearchConsoleQueriesRange(ctx, accessToken, siteURL, start, end, limit)
}

// googleSearchConsoleQueriesRange 起止日期参数化版本（compare 模式需要拉两个区间）。
func googleSearchConsoleQueriesRange(ctx context.Context, accessToken, siteURL string, start, end time.Time, limit int) ([]statsSearchRow, error) {
	return googleSearchConsoleRowsRange(ctx, accessToken, siteURL, start, end, limit, []string{"query", "page"})
}

// googleSearchConsoleRowsRange 是 Search Analytics 的通用维度读取器。dimensions 为空时
// Google 返回整个时间段的汇总；非空时 keys 的顺序与 dimensions 完全一致。
func googleSearchConsoleRowsRange(ctx context.Context, accessToken, siteURL string, start, end time.Time, limit int, dimensions []string) ([]statsSearchRow, error) {
	siteURL, ok := normalizeGoogleSearchConsoleSiteURL(siteURL)
	if !ok {
		return nil, errors.New("Google Search Console 站点属性不正确，无法读取搜索数据")
	}
	body := map[string]any{
		"startDate": start.Format("2006-01-02"),
		"endDate":   end.Format("2006-01-02"),
		"rowLimit":  limit,
	}
	if len(dimensions) > 0 {
		body["dimensions"] = dimensions
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	reqURL := googleSearchConsoleURL + "/sites/" + url.PathEscape(siteURL) + "/searchAnalytics/query"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := googleHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Rows []struct {
			Keys        []string `json:"keys"`
			Clicks      float64  `json:"clicks"`
			Impressions float64  `json:"impressions"`
			CTR         float64  `json:"ctr"`
			Position    float64  `json:"position"`
		} `json:"rows"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(out.Error.Message)
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return nil, errors.New(googleSearchConsoleAPIErrorMessage(msg))
	}
	rows := make([]statsSearchRow, 0, len(out.Rows))
	for _, row := range out.Rows {
		item := statsSearchRow{
			Clicks:      googleRoundedMetric(row.Clicks),
			Impressions: googleRoundedMetric(row.Impressions),
			CTR:         row.CTR,
			Position:    row.Position,
		}
		for index, dimension := range dimensions {
			if index >= len(row.Keys) {
				break
			}
			switch dimension {
			case "query":
				item.Query = row.Keys[index]
			case "page":
				item.Page = row.Keys[index]
			case "date":
				item.Date = row.Keys[index]
			}
		}
		rows = append(rows, item)
	}
	return rows, nil
}

// googleAnalyticsTrafficSummary GA4 runReport：days 参数化的流量与互动质量汇总
// （googleAnalyticsSevenDaySummary 的一般化版本）。
func googleAnalyticsTrafficSummary(ctx context.Context, accessToken, property string, days int) (statsTrafficSummary, error) {
	property = normalizeGoogleAnalyticsPropertyName(property)
	if !validGoogleAnalyticsPropertyName(property) {
		return statsTrafficSummary{}, errors.New("GA4 属性无效，无法读取统计数据")
	}
	body := map[string]any{
		"dateRanges": []map[string]string{{"startDate": fmt.Sprintf("%ddaysAgo", days), "endDate": "today"}},
		"metrics": []map[string]string{
			{"name": "activeUsers"},
			{"name": "sessions"},
			{"name": "engagementRate"},
			{"name": "averageSessionDuration"},
		},
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return statsTrafficSummary{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleAnalyticsDataURL+"/"+property+":runReport", &buf)
	if err != nil {
		return statsTrafficSummary{}, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := googleHTTPClient.Do(req)
	if err != nil {
		return statsTrafficSummary{}, err
	}
	defer resp.Body.Close()
	var out struct {
		Rows []struct {
			MetricValues []googleAnalyticsMetricValue `json:"metricValues"`
		} `json:"rows"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return statsTrafficSummary{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := out.Error.Message
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return statsTrafficSummary{}, errors.New(googleAnalyticsDataAPIErrorMessage(msg))
	}
	if len(out.Rows) == 0 {
		return statsTrafficSummary{}, nil
	}
	return statsTrafficSummary{
		ActiveUsers:            googleAnalyticsMetricInt(out.Rows[0].MetricValues, 0),
		Sessions:               googleAnalyticsMetricInt(out.Rows[0].MetricValues, 1),
		EngagementRate:         googleAnalyticsMetricFloat(out.Rows[0].MetricValues, 2),
		AverageSessionDuration: googleAnalyticsMetricFloat(out.Rows[0].MetricValues, 3),
	}, nil
}

// apiStatsPages GET /stats/pages?days=7&limit=50：GA4 按 pagePath 维度的活跃用户 / 会话。
// 响应 {ok,days,property,rows:[{path,active_users,sessions}]}，缓存 1 小时。
func (s *Server) apiStatsPages(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAutomationScope(w, r, apiScopeStatsRead); !ok {
		return
	}
	days := statsClampDays(r.URL.Query().Get("days"), statsPagesDefaultDays)
	limit := statsClampLimit(r.URL.Query().Get("limit"), statsPagesDefaultLimit)
	fresh := parseAPIBool(r.URL.Query().Get("fresh"))
	in, acc, errCode, errMsg := s.statsGoogleIntegration(platform.GoogleServiceAnalytics)
	if errCode != "" {
		apiError(w, http.StatusBadRequest, errCode, errMsg)
		return
	}
	cacheKey := fmt.Sprintf("pages|%s|%d|%d", in.Property, days, limit)
	if !fresh {
		if payload, ok := s.statsCacheGet(cacheKey); ok {
			writeJSON(w, http.StatusOK, payload)
			return
		}
	}
	token, err := s.googleAccessToken(r.Context(), r, acc)
	if err != nil {
		apiError(w, http.StatusBadGateway, "google_auth_failed", err.Error())
		return
	}
	rows, err := statsPagesFetch(r.Context(), token, in.Property, days, limit)
	if err != nil {
		apiError(w, http.StatusBadGateway, "google_api_error", err.Error())
		return
	}
	if rows == nil {
		rows = []statsPageRow{}
	}
	payload := map[string]any{"ok": true, "days": days, "property": in.Property, "rows": rows}
	s.statsCachePut(cacheKey, payload)
	writeJSON(w, http.StatusOK, payload)
}

// googleAnalyticsPagesReport GA4 runReport：pagePath 维度 × 流量与互动质量，
// 按活跃用户降序取前 limit 行。
func googleAnalyticsPagesReport(ctx context.Context, accessToken, property string, days, limit int) ([]statsPageRow, error) {
	property = normalizeGoogleAnalyticsPropertyName(property)
	if !validGoogleAnalyticsPropertyName(property) {
		return nil, errors.New("GA4 属性无效，无法读取统计数据")
	}
	body := map[string]any{
		"dateRanges": []map[string]string{{"startDate": fmt.Sprintf("%ddaysAgo", days), "endDate": "today"}},
		"dimensions": []map[string]string{{"name": "pagePath"}},
		"metrics": []map[string]string{
			{"name": "activeUsers"},
			{"name": "sessions"},
			{"name": "engagementRate"},
			{"name": "averageSessionDuration"},
		},
		"orderBys": []map[string]any{{"desc": true, "metric": map[string]string{"metricName": "activeUsers"}}},
		"limit":    limit,
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleAnalyticsDataURL+"/"+property+":runReport", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := googleHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Rows []struct {
			DimensionValues []struct {
				Value string `json:"value"`
			} `json:"dimensionValues"`
			MetricValues []googleAnalyticsMetricValue `json:"metricValues"`
		} `json:"rows"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := out.Error.Message
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return nil, errors.New(googleAnalyticsDataAPIErrorMessage(msg))
	}
	rows := make([]statsPageRow, 0, len(out.Rows))
	for _, row := range out.Rows {
		item := statsPageRow{
			ActiveUsers:            googleAnalyticsMetricInt(row.MetricValues, 0),
			Sessions:               googleAnalyticsMetricInt(row.MetricValues, 1),
			EngagementRate:         googleAnalyticsMetricFloat(row.MetricValues, 2),
			AverageSessionDuration: googleAnalyticsMetricFloat(row.MetricValues, 3),
		}
		if len(row.DimensionValues) > 0 {
			item.Path = row.DimensionValues[0].Value
		}
		rows = append(rows, item)
	}
	return rows, nil
}

// apiStatsAnalytics GET /stats/analytics?group=sources|geography|devices|trend：
// GA4 来源、地区、设备或每日趋势。每个标签按需读取并单独缓存。
func (s *Server) apiStatsAnalytics(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAutomationScope(w, r, apiScopeStatsRead); !ok {
		return
	}
	spec, ok := statsAnalyticsGroupSpec(r.URL.Query().Get("group"))
	if !ok {
		apiError(w, http.StatusBadRequest, "invalid_group", "group 仅支持 sources、geography、devices 或 trend")
		return
	}
	days := statsClampDays(r.URL.Query().Get("days"), statsTrafficDefaultDays)
	limit := statsClampLimit(r.URL.Query().Get("limit"), statsPagesDefaultLimit)
	if spec.Group == "trend" && limit > days+1 {
		limit = days + 1
	}
	fresh := parseAPIBool(r.URL.Query().Get("fresh"))
	in, acc, errCode, errMsg := s.statsGoogleIntegration(platform.GoogleServiceAnalytics)
	if errCode != "" {
		apiError(w, http.StatusBadRequest, errCode, errMsg)
		return
	}
	cacheKey := fmt.Sprintf("analytics|%s|%s|%d|%d", spec.Group, in.Property, days, limit)
	if !fresh {
		if payload, ok := s.statsCacheGet(cacheKey); ok {
			writeJSON(w, http.StatusOK, payload)
			return
		}
	}
	token, err := s.googleAccessToken(r.Context(), r, acc)
	if err != nil {
		apiError(w, http.StatusBadGateway, "google_auth_failed", err.Error())
		return
	}
	report, err := statsAnalyticsFetch(r.Context(), token, in.Property, spec, days, limit)
	if err != nil {
		apiError(w, http.StatusBadGateway, "google_api_error", err.Error())
		return
	}
	if report.Dimensions == nil {
		report.Dimensions = append([]string(nil), spec.Dimensions...)
	}
	if report.Rows == nil {
		report.Rows = []statsAnalyticsRow{}
	}
	payload := map[string]any{
		"ok":         true,
		"days":       days,
		"property":   in.Property,
		"group":      spec.Group,
		"dimensions": report.Dimensions,
		"rows":       report.Rows,
	}
	s.statsCachePut(cacheKey, payload)
	writeJSON(w, http.StatusOK, payload)
}

// googleAnalyticsDimensionReport 执行产品允许的 GA4 维度报告。
// 所有分组使用同一组质量指标，前端可在不同视图间做一致比较。
func googleAnalyticsDimensionReport(
	ctx context.Context,
	accessToken, property string,
	spec statsAnalyticsSpec,
	days, limit int,
) (statsAnalyticsReport, error) {
	property = normalizeGoogleAnalyticsPropertyName(property)
	if !validGoogleAnalyticsPropertyName(property) {
		return statsAnalyticsReport{}, errors.New("GA4 属性无效，无法读取统计数据")
	}
	dimensions := make([]map[string]string, 0, len(spec.Dimensions))
	for _, name := range spec.Dimensions {
		dimensions = append(dimensions, map[string]string{"name": name})
	}
	body := map[string]any{
		"dateRanges": []map[string]string{{"startDate": fmt.Sprintf("%ddaysAgo", days), "endDate": "today"}},
		"dimensions": dimensions,
		"metrics": []map[string]string{
			{"name": "activeUsers"},
			{"name": "sessions"},
			{"name": "engagementRate"},
			{"name": "averageSessionDuration"},
		},
		"limit": limit,
	}
	if spec.Group == "trend" {
		body["orderBys"] = []map[string]any{{
			"dimension": map[string]string{"dimensionName": "date", "orderType": "ALPHANUMERIC"},
		}}
	} else {
		body["orderBys"] = []map[string]any{{
			"desc":   true,
			"metric": map[string]string{"metricName": "sessions"},
		}}
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return statsAnalyticsReport{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleAnalyticsDataURL+"/"+property+":runReport", &buf)
	if err != nil {
		return statsAnalyticsReport{}, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := googleHTTPClient.Do(req)
	if err != nil {
		return statsAnalyticsReport{}, err
	}
	defer resp.Body.Close()
	var out struct {
		Rows []struct {
			DimensionValues []struct {
				Value string `json:"value"`
			} `json:"dimensionValues"`
			MetricValues []googleAnalyticsMetricValue `json:"metricValues"`
		} `json:"rows"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return statsAnalyticsReport{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(out.Error.Message)
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return statsAnalyticsReport{}, errors.New(googleAnalyticsDataAPIErrorMessage(msg))
	}
	report := statsAnalyticsReport{
		Dimensions: append([]string(nil), spec.Dimensions...),
		Rows:       make([]statsAnalyticsRow, 0, len(out.Rows)),
	}
	for _, row := range out.Rows {
		values := make([]string, len(spec.Dimensions))
		for i := range values {
			if i < len(row.DimensionValues) {
				values[i] = strings.TrimSpace(row.DimensionValues[i].Value)
			}
		}
		report.Rows = append(report.Rows, statsAnalyticsRow{
			Values:                 values,
			ActiveUsers:            googleAnalyticsMetricInt(row.MetricValues, 0),
			Sessions:               googleAnalyticsMetricInt(row.MetricValues, 1),
			EngagementRate:         googleAnalyticsMetricFloat(row.MetricValues, 2),
			AverageSessionDuration: googleAnalyticsMetricFloat(row.MetricValues, 3),
		})
	}
	return report, nil
}

func googleAnalyticsMetricFloat(values []googleAnalyticsMetricValue, index int) float64 {
	if index < 0 || index >= len(values) {
		return 0
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(values[index].Value), 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
