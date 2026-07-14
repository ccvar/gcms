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
	statsSearchDefaultLimit = 100
	statsSearchLimitMax     = 1000
)

// statsSearchRow Search Console 搜索词 × 页面的一行表现数据。
type statsSearchRow struct {
	Query       string  `json:"query"`
	Page        string  `json:"page"`
	Clicks      int     `json:"clicks"`
	Impressions int     `json:"impressions"`
	Position    float64 `json:"position"`
}

// statsTrafficSummary GA4 的活跃用户 / 会话汇总。
type statsTrafficSummary struct {
	ActiveUsers int `json:"active_users"`
	Sessions    int `json:"sessions"`
}

type statsCacheEntry struct {
	payload map[string]any
	expires time.Time
}

// 抓取函数可注入：生产为真实 Google API 调用，测试替换成桩以验证缓存与参数钳制。
var (
	statsSearchFetch  = googleSearchConsoleTopQueries
	statsTrafficFetch = googleAnalyticsTrafficSummary
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

// statsClampLimit 解析 limit 参数并钳制到 1..1000，缺省 / 非法回落 100。
func statsClampLimit(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return statsSearchDefaultLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return statsSearchDefaultLimit
	}
	if n < 1 {
		return 1
	}
	if n > statsSearchLimitMax {
		return statsSearchLimitMax
	}
	return n
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

// apiStatsSearch GET /stats/search?days=28&limit=100：Search Console 搜索词 × 页面表现。
// 响应 {ok,days,property,rows:[{query,page,clicks,impressions,position}]}，缓存 1 小时。
func (s *Server) apiStatsSearch(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAutomationScope(w, r, apiScopeStatsRead); !ok {
		return
	}
	days := statsClampDays(r.URL.Query().Get("days"), statsSearchDefaultDays)
	limit := statsClampLimit(r.URL.Query().Get("limit"))
	in, acc, errCode, errMsg := s.statsGoogleIntegration(platform.GoogleServiceSearchConsole)
	if errCode != "" {
		apiError(w, http.StatusBadRequest, errCode, errMsg)
		return
	}
	cacheKey := fmt.Sprintf("search|%s|%d|%d", in.Property, days, limit)
	if payload, ok := s.statsCacheGet(cacheKey); ok {
		writeJSON(w, http.StatusOK, payload)
		return
	}
	token, err := s.googleAccessToken(r.Context(), r, acc)
	if err != nil {
		apiError(w, http.StatusBadGateway, "google_auth_failed", err.Error())
		return
	}
	rows, err := statsSearchFetch(r.Context(), token, in.Property, days, limit)
	if err != nil {
		apiError(w, http.StatusBadGateway, "google_api_error", err.Error())
		return
	}
	if rows == nil {
		rows = []statsSearchRow{}
	}
	payload := map[string]any{"ok": true, "days": days, "property": in.Property, "rows": rows}
	s.statsCachePut(cacheKey, payload)
	writeJSON(w, http.StatusOK, payload)
}

// apiStatsTraffic GET /stats/traffic?days=7：GA4 活跃用户 / 会话汇总。
// 响应 {ok,days,property,active_users,sessions}，缓存 1 小时。
func (s *Server) apiStatsTraffic(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAutomationScope(w, r, apiScopeStatsRead); !ok {
		return
	}
	days := statsClampDays(r.URL.Query().Get("days"), statsTrafficDefaultDays)
	in, acc, errCode, errMsg := s.statsGoogleIntegration(platform.GoogleServiceAnalytics)
	if errCode != "" {
		apiError(w, http.StatusBadRequest, errCode, errMsg)
		return
	}
	cacheKey := fmt.Sprintf("traffic|%s|%d", in.Property, days)
	if payload, ok := s.statsCacheGet(cacheKey); ok {
		writeJSON(w, http.StatusOK, payload)
		return
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
	payload := map[string]any{"ok": true, "days": days, "property": in.Property, "active_users": sum.ActiveUsers, "sessions": sum.Sessions}
	s.statsCachePut(cacheKey, payload)
	writeJSON(w, http.StatusOK, payload)
}

// googleSearchConsoleTopQueries 调 Search Console searchanalytics.query（dimensions: query+page），
// 返回近 days 天各搜索词 × 页面的点击、曝光与平均排名（Google 按点击降序返回）。
func googleSearchConsoleTopQueries(ctx context.Context, accessToken, siteURL string, days, limit int) ([]statsSearchRow, error) {
	siteURL, ok := normalizeGoogleSearchConsoleSiteURL(siteURL)
	if !ok {
		return nil, errors.New("Google Search Console 站点属性不正确，无法读取搜索数据")
	}
	end := time.Now().AddDate(0, 0, -1)
	start := end.AddDate(0, 0, -(days - 1))
	body := map[string]any{
		"startDate":  start.Format("2006-01-02"),
		"endDate":    end.Format("2006-01-02"),
		"dimensions": []string{"query", "page"},
		"rowLimit":   limit,
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
			Position:    row.Position,
		}
		if len(row.Keys) > 0 {
			item.Query = row.Keys[0]
		}
		if len(row.Keys) > 1 {
			item.Page = row.Keys[1]
		}
		rows = append(rows, item)
	}
	return rows, nil
}

// googleAnalyticsTrafficSummary GA4 runReport：days 参数化的 activeUsers / sessions 汇总
// （googleAnalyticsSevenDaySummary 的一般化版本）。
func googleAnalyticsTrafficSummary(ctx context.Context, accessToken, property string, days int) (statsTrafficSummary, error) {
	property = normalizeGoogleAnalyticsPropertyName(property)
	if !validGoogleAnalyticsPropertyName(property) {
		return statsTrafficSummary{}, errors.New("GA4 属性无效，无法读取统计数据")
	}
	body := map[string]any{
		"dateRanges": []map[string]string{{"startDate": fmt.Sprintf("%ddaysAgo", days), "endDate": "today"}},
		"metrics":    []map[string]string{{"name": "activeUsers"}, {"name": "sessions"}},
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
		ActiveUsers: googleAnalyticsMetricInt(out.Rows[0].MetricValues, 0),
		Sessions:    googleAnalyticsMetricInt(out.Rows[0].MetricValues, 1),
	}, nil
}
