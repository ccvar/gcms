package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/seo"
)

const (
	googleOAuthAuthURL      = "https://accounts.google.com/o/oauth2/v2/auth"
	googleOAuthTokenURL     = "https://oauth2.googleapis.com/token"
	googleOAuthUserInfoURL  = "https://openidconnect.googleapis.com/v1/userinfo"
	googleAnalyticsAdminURL = "https://analyticsadmin.googleapis.com/v1beta"
	googleAnalyticsDataURL  = "https://analyticsdata.googleapis.com/v1beta"
	googleSearchConsoleURL  = "https://www.googleapis.com/webmasters/v3"

	googleOAuthClientIDKey     = "google.oauth.client_id"
	googleOAuthClientSecretKey = "google.oauth.client_secret"
	googleOAuthRedirectURLKey  = "google.oauth.redirect_url"

	googleAnalyticsReadonlyScope     = "https://www.googleapis.com/auth/analytics.readonly"
	googleAnalyticsEditScope         = "https://www.googleapis.com/auth/analytics.edit"
	googleSearchConsoleScope         = "https://www.googleapis.com/auth/webmasters"
	googleSearchConsoleReadonlyScope = "https://www.googleapis.com/auth/webmasters.readonly"
)

var googleHTTPClient = &http.Client{Timeout: 10 * time.Second}

const (
	googleAnalyticsPropertiesCacheTTL        = 2 * time.Minute
	googleAnalyticsPropertiesPartialCacheTTL = 20 * time.Second
	googleAnalyticsStreamSummaryTimeout      = 12 * time.Second
	googleAnalyticsStreamSummaryWorkers      = 6
)

type googleAPIError struct {
	StatusCode int
	Message    string
}

func (e *googleAPIError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func newGoogleAPIError(status int, message string) error {
	return &googleAPIError{StatusCode: status, Message: strings.TrimSpace(message)}
}

func isRetriableGoogleAPIError(err error, allowPermissionPropagation bool) bool {
	var apiErr *googleAPIError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == http.StatusTooManyRequests {
			return true
		}
		if allowPermissionPropagation && apiErr.StatusCode == http.StatusForbidden {
			message := strings.ToLower(apiErr.Message)
			return strings.Contains(message, "permission") || strings.Contains(message, "权限")
		}
	}
	return errors.Is(err, context.DeadlineExceeded)
}

func isRetriableGoogleReadError(err error) bool {
	var apiErr *googleAPIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= http.StatusInternalServerError
	}
	return errors.Is(err, context.DeadlineExceeded)
}

func googleRetryDelay(attempt int) time.Duration {
	delays := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second}
	if attempt < 0 {
		attempt = 0
	}
	if attempt >= len(delays) {
		return delays[len(delays)-1]
	}
	return delays[attempt]
}

func waitGoogleRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type googleAnalyticsPropertiesCacheEntry struct {
	accounts   []googleAnalyticsAccountOption
	properties []googleAnalyticsPropertyOption
	warning    string
	expires    time.Time
}

type googleAnalyticsPropertiesCacheCall struct {
	done       chan struct{}
	accounts   []googleAnalyticsAccountOption
	properties []googleAnalyticsPropertyOption
	warning    string
	err        error
}

type googleAnalyticsPropertiesCache struct {
	mu       sync.Mutex
	entries  map[string]googleAnalyticsPropertiesCacheEntry
	inflight map[string]*googleAnalyticsPropertiesCacheCall
}

func newGoogleAnalyticsPropertiesCache() *googleAnalyticsPropertiesCache {
	return &googleAnalyticsPropertiesCache{
		entries:  map[string]googleAnalyticsPropertiesCacheEntry{},
		inflight: map[string]*googleAnalyticsPropertiesCacheCall{},
	}
}

type googleOAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

func cloneGoogleAnalyticsAccounts(in []googleAnalyticsAccountOption) []googleAnalyticsAccountOption {
	return append([]googleAnalyticsAccountOption(nil), in...)
}

func cloneGoogleAnalyticsProperties(in []googleAnalyticsPropertyOption) []googleAnalyticsPropertyOption {
	return append([]googleAnalyticsPropertyOption(nil), in...)
}

func (c *googleAnalyticsPropertiesCache) load(
	ctx context.Context,
	key string,
	loader func() ([]googleAnalyticsAccountOption, []googleAnalyticsPropertyOption, string, error),
) ([]googleAnalyticsAccountOption, []googleAnalyticsPropertyOption, string, error) {
	if c == nil {
		return loader()
	}
	now := time.Now()
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && now.Before(entry.expires) {
		c.mu.Unlock()
		return cloneGoogleAnalyticsAccounts(entry.accounts), cloneGoogleAnalyticsProperties(entry.properties), entry.warning, nil
	}
	if call := c.inflight[key]; call != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil, "", ctx.Err()
		case <-call.done:
			return cloneGoogleAnalyticsAccounts(call.accounts), cloneGoogleAnalyticsProperties(call.properties), call.warning, call.err
		}
	}
	call := &googleAnalyticsPropertiesCacheCall{done: make(chan struct{})}
	c.inflight[key] = call
	c.mu.Unlock()

	call.accounts, call.properties, call.warning, call.err = loader()

	c.mu.Lock()
	if call.err == nil {
		ttl := googleAnalyticsPropertiesCacheTTL
		if call.warning != "" {
			ttl = googleAnalyticsPropertiesPartialCacheTTL
		}
		c.entries[key] = googleAnalyticsPropertiesCacheEntry{
			accounts:   cloneGoogleAnalyticsAccounts(call.accounts),
			properties: cloneGoogleAnalyticsProperties(call.properties),
			warning:    call.warning,
			expires:    time.Now().Add(ttl),
		}
	}
	delete(c.inflight, key)
	close(call.done)
	c.mu.Unlock()

	return cloneGoogleAnalyticsAccounts(call.accounts), cloneGoogleAnalyticsProperties(call.properties), call.warning, call.err
}

func (c *googleAnalyticsPropertiesCache) invalidateAccount(googleAccountID string) {
	if c == nil {
		return
	}
	prefix := strings.TrimSpace(googleAccountID) + "|"
	c.mu.Lock()
	for key := range c.entries {
		if strings.HasPrefix(key, prefix) {
			delete(c.entries, key)
		}
	}
	c.mu.Unlock()
}

func (s *Server) googleAnalyticsPropertyOptions(ctx context.Context, googleAccountID, accessToken, defaultURI string) ([]googleAnalyticsAccountOption, []googleAnalyticsPropertyOption, string, error) {
	defaultURI, _ = normalizeGoogleDefaultURI(defaultURI)
	key := strings.TrimSpace(googleAccountID) + "|" + defaultURI
	return s.googleAnalytics.load(ctx, key, func() ([]googleAnalyticsAccountOption, []googleAnalyticsPropertyOption, string, error) {
		accounts, properties, err := googleAnalyticsAccountsAndProperties(ctx, accessToken)
		if err != nil {
			return nil, nil, "", err
		}
		if defaultURI == "" || len(properties) == 0 {
			return accounts, properties, "", nil
		}
		failed, attempted, detailErr := applyGoogleAnalyticsPropertyStreamSummaries(ctx, accessToken, properties, defaultURI)
		if failed == 0 {
			return accounts, properties, "", nil
		}
		warning := fmt.Sprintf("GA4 属性列表已读取，但 %d/%d 个属性的数据流详情暂时无法读取。可先选择属性，或稍后重试。", failed, attempted)
		if detailErr != nil && attempted == failed {
			warning = "GA4 属性列表已读取，但数据流详情暂时无法读取：" + detailErr.Error()
		}
		return accounts, properties, warning, nil
	})
}

func (s *Server) googleOAuthConfigured(r *http.Request) bool {
	cfg := s.googleOAuthConfig(r)
	return cfg.ClientID != "" && cfg.ClientSecret != ""
}

func (s *Server) googleOAuthConfig(r *http.Request) googleOAuthConfig {
	clientID := ""
	clientSecret := ""
	redirect := ""
	if s.platform != nil {
		clientID = strings.TrimSpace(s.platform.Setting(googleOAuthClientIDKey))
		clientSecret = strings.TrimSpace(s.platform.Setting(googleOAuthClientSecretKey))
		redirect = strings.TrimSpace(s.platform.Setting(googleOAuthRedirectURLKey))
	}
	if clientID == "" {
		clientID = strings.TrimSpace(os.Getenv("GOOGLE_OAUTH_CLIENT_ID"))
	}
	if clientSecret == "" {
		clientSecret = strings.TrimSpace(os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET"))
	}
	if redirect == "" {
		redirect = strings.TrimSpace(os.Getenv("GOOGLE_OAUTH_REDIRECT_URL"))
	}
	if redirect == "" {
		redirect = s.absForPlatformRequest(r, "/admin/google/oauth/callback")
	}
	return googleOAuthConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirect,
	}
}

func googleOAuthScopes(service string) []string {
	base := []string{"openid", "email", "profile"}
	switch platform.NormalizeGoogleService(service) {
	case platform.GoogleServiceAnalytics:
		return append(base, googleAnalyticsReadonlyScope, googleAnalyticsEditScope)
	case platform.GoogleServiceSearchConsole:
		return append(base, googleSearchConsoleScope)
	case platform.GoogleServiceAll:
		return append(base, googleAnalyticsReadonlyScope, googleAnalyticsEditScope, googleSearchConsoleScope)
	default:
		return base
	}
}

func googleServiceLabel(service string) string {
	switch platform.NormalizeGoogleService(service) {
	case platform.GoogleServiceAnalytics:
		return "Google Analytics"
	case platform.GoogleServiceSearchConsole:
		return "Google Search Console"
	case platform.GoogleServiceAll:
		return "Google"
	default:
		return "Google"
	}
}

func googleCloudProjectNumberFromClientID(clientID string) string {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return ""
	}
	idx := strings.Index(clientID, "-")
	if idx <= 0 {
		return ""
	}
	project := clientID[:idx]
	for _, r := range project {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return project
}

func googleCloudAPIEnableURL(api, project string) string {
	api = strings.TrimSpace(api)
	if api == "" {
		return ""
	}
	enableURL := "https://console.developers.google.com/apis/api/" + api + "/overview"
	if project = strings.TrimSpace(project); project != "" {
		enableURL += "?project=" + url.QueryEscape(project)
	}
	return enableURL
}

func googleAPIEnableURLFromMessage(msg, api string) string {
	prefix := googleCloudAPIEnableURL(api, "")
	for _, part := range strings.Fields(msg) {
		part = strings.Trim(part, ".,;，。；")
		if strings.HasPrefix(part, prefix) {
			return part
		}
	}
	return ""
}

func googleServiceAPIErrorMessage(msg, api, serviceName, capability string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	lower := strings.ToLower(msg)
	if strings.Contains(lower, strings.ToLower(api)) &&
		(strings.Contains(lower, "has not been used") || strings.Contains(lower, "disabled")) {
		enableURL := googleAPIEnableURLFromMessage(msg, api)
		if enableURL == "" {
			enableURL = googleCloudAPIEnableURL(api, "")
		}
		return serviceName + " 尚未启用：当前 OAuth Client 所属的 Google Cloud 项目还不能调用 " + capability + "。请打开下面链接启用 " + serviceName + "，等待几分钟后回到这里重试：" + enableURL
	}
	return msg
}

func googleAnalyticsAdminAPIErrorMessage(msg string) string {
	return googleServiceAPIErrorMessage(msg, "analyticsadmin.googleapis.com", "Google Analytics Admin API", "GA4 管理接口")
}

func googleAnalyticsDataAPIErrorMessage(msg string) string {
	return googleServiceAPIErrorMessage(msg, "analyticsdata.googleapis.com", "Google Analytics Data API", "GA4 数据读取接口")
}

func googleSearchConsoleAPIErrorMessage(msg string) string {
	return googleServiceAPIErrorMessage(msg, "webmasters.googleapis.com", "Google Search Console API", "Search Console 接口")
}

func googleOAuthAccountServices(service string) []string {
	switch platform.NormalizeGoogleService(service) {
	case platform.GoogleServiceAll:
		return []string{platform.GoogleServiceAnalytics, platform.GoogleServiceSearchConsole}
	case platform.GoogleServiceAnalytics:
		return []string{platform.GoogleServiceAnalytics}
	case platform.GoogleServiceSearchConsole:
		return []string{platform.GoogleServiceSearchConsole}
	default:
		return nil
	}
}

func (s *Server) adminGoogleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	service := platform.NormalizeGoogleService(r.URL.Query().Get("service"))
	if service == "" {
		s.flashGoogleOAuth(r, "未知的 Google 授权类型。")
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
		return
	}
	cfg := s.googleOAuthConfig(r)
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		s.flashGoogleOAuth(r, "请先在站点管理页填写 Google OAuth Client ID 和 Client Secret，再新增授权账号。")
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
		return
	}
	state := randToken()
	if err := s.platform.CreateGoogleOAuthState(state, service, time.Now().Add(10*time.Minute)); err != nil {
		s.serverError(w, err)
		return
	}
	q := url.Values{}
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", cfg.RedirectURL)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(googleOAuthScopes(service), " "))
	q.Set("state", state)
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	http.Redirect(w, r, googleOAuthAuthURL+"?"+q.Encode(), http.StatusSeeOther)
}

func (s *Server) adminGoogleOAuthConfig(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	jsonReq := wantsJSON(r)
	fail := func(status int, msg string) {
		if jsonReq {
			writeJSON(w, status, map[string]any{"ok": false, "message": msg})
			return
		}
		s.flashGoogleOAuth(r, msg)
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	clientID := strings.TrimSpace(r.FormValue("client_id"))
	clientSecret := strings.TrimSpace(r.FormValue("client_secret"))
	redirectURL := strings.TrimSpace(r.FormValue("redirect_url"))
	if clientID == "" {
		fail(http.StatusBadRequest, "请填写 Google OAuth Client ID。")
		return
	}
	storedSecret := strings.TrimSpace(s.platform.Setting(googleOAuthClientSecretKey))
	envSecret := strings.TrimSpace(os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET"))
	if clientSecret == "" && storedSecret == "" && envSecret == "" {
		fail(http.StatusBadRequest, "请填写 Google OAuth Client Secret。")
		return
	}
	if err := s.platform.SetSetting(googleOAuthClientIDKey, clientID); err != nil {
		s.serverError(w, err)
		return
	}
	if clientSecret != "" {
		if err := s.platform.SetSetting(googleOAuthClientSecretKey, clientSecret); err != nil {
			s.serverError(w, err)
			return
		}
	}
	if err := s.platform.SetSetting(googleOAuthRedirectURLKey, redirectURL); err != nil {
		s.serverError(w, err)
		return
	}
	msg := "Google OAuth 配置已保存，现在可以新增授权账号。"
	if jsonReq {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
		return
	}
	s.flashGoogleOAuth(r, msg)
	http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
}

func (s *Server) adminGoogleOAuthClear(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	jsonReq := wantsJSON(r)
	fail := func(status int, msg string) {
		if jsonReq {
			writeJSON(w, status, map[string]any{"ok": false, "message": msg})
			return
		}
		s.flashGoogleOAuth(r, msg)
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if err := s.platform.ClearGoogleOAuthData(googleOAuthClientIDKey, googleOAuthClientSecretKey, googleOAuthRedirectURLKey); err != nil {
		fail(http.StatusInternalServerError, "清除 Google OAuth 配置失败："+err.Error())
		return
	}
	msg := "Google OAuth 配置已清除，本地授权账号和站点 Google 接入记录也已移除。"
	if jsonReq {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
		return
	}
	s.flashGoogleOAuth(r, msg)
	http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
}

func (s *Server) adminGoogleAccountDelete(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	jsonReq := wantsJSON(r)
	fail := func(status int, msg string) {
		if jsonReq {
			writeJSON(w, status, map[string]any{"ok": false, "message": msg})
			return
		}
		s.flashGoogleOAuth(r, msg)
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	service := platform.NormalizeGoogleService(r.FormValue("service"))
	googleAccountID := strings.TrimSpace(r.FormValue("google_account_id"))
	if service == "" || googleAccountID == "" {
		fail(http.StatusBadRequest, "Google 授权账号参数不完整。")
		return
	}
	if service == platform.GoogleServiceAll {
		for _, svc := range googleOAuthAccountServices(service) {
			if err := s.platform.DeleteGoogleAccount(svc, googleAccountID); err != nil {
				s.serverError(w, err)
				return
			}
		}
	} else {
		if err := s.platform.DeleteGoogleAccount(service, googleAccountID); err != nil {
			s.serverError(w, err)
			return
		}
	}
	msg := "Google 授权账号已解除绑定。"
	if jsonReq {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
		return
	}
	s.flashGoogleOAuth(r, msg)
	http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
}

func (s *Server) adminGoogleAnalyticsProperties(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	googleAccountID := strings.TrimSpace(r.URL.Query().Get("account"))
	if googleAccountID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "请选择 Google Analytics 授权账号。"})
		return
	}
	acc, ok, err := s.platform.GoogleAccount(platform.GoogleServiceAnalytics, googleAccountID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "message": "没有找到该 Google Analytics 授权账号。"})
		return
	}
	accessToken, err := s.googleAccessToken(r.Context(), r, acc)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	defaultURI := ""
	if normalized, ok := normalizeGoogleDefaultURI(r.URL.Query().Get("default_uri")); ok {
		defaultURI = normalized
	}
	accounts, properties, warning, err := s.googleAnalyticsPropertyOptions(r.Context(), googleAccountID, accessToken, defaultURI)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accounts": accounts, "properties": properties, "warning": warning})
}

func (s *Server) adminGoogleAnalyticsSummary(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	siteID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || siteID <= 0 {
		http.NotFound(w, r)
		return
	}
	if _, ok, err := s.platform.GetSite(siteID); err != nil {
		s.serverError(w, err)
		return
	} else if !ok {
		http.NotFound(w, r)
		return
	}
	in, ok, err := s.platform.SiteGoogleIntegration(siteID, platform.GoogleServiceAnalytics)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok || !in.Enabled || strings.TrimSpace(in.GoogleAccountID) == "" || strings.TrimSpace(in.Property) == "" || strings.TrimSpace(in.MeasurementID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":            false,
			"status":        platform.GoogleAnalyticsSummaryStatusError,
			"error_message": "Google Analytics 未完整接入，无法读取统计数据。",
		})
		return
	}
	acc, ok, err := s.platform.GoogleAccount(platform.GoogleServiceAnalytics, in.GoogleAccountID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"ok":            false,
			"status":        platform.GoogleAnalyticsSummaryStatusError,
			"error_message": "没有找到 Google Analytics 授权账号。",
		})
		return
	}
	fetchedAt := time.Now()
	accessToken, err := s.googleAccessToken(r.Context(), r, acc)
	if err == nil {
		metrics, runErr := googleAnalyticsSevenDaySummary(r.Context(), accessToken, in.Property)
		if runErr == nil {
			sum := &platform.SiteGoogleAnalyticsSummary{
				SiteID:        siteID,
				Property:      in.Property,
				MeasurementID: in.MeasurementID,
				ActiveUsers7D: metrics.ActiveUsers7D,
				Sessions7D:    metrics.Sessions7D,
				Status:        platform.GoogleAnalyticsSummaryStatusOK,
				FetchedAt:     fetchedAt,
			}
			if err := s.platform.UpsertSiteGoogleAnalyticsSummary(sum); err != nil {
				s.serverError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":              true,
				"status":          sum.Status,
				"active_users_7d": sum.ActiveUsers7D,
				"sessions_7d":     sum.Sessions7D,
				"fetched_at":      sum.FetchedAt.Format(time.RFC3339),
			})
			return
		}
		err = runErr
	}
	msg := "数据暂不可用"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		msg = err.Error()
	}
	sum := &platform.SiteGoogleAnalyticsSummary{
		SiteID:        siteID,
		Property:      in.Property,
		MeasurementID: in.MeasurementID,
		Status:        platform.GoogleAnalyticsSummaryStatusError,
		ErrorMessage:  msg,
		FetchedAt:     fetchedAt,
	}
	if err := s.platform.UpsertSiteGoogleAnalyticsSummary(sum); err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            false,
		"status":        sum.Status,
		"error_message": sum.ErrorMessage,
		"fetched_at":    sum.FetchedAt.Format(time.RFC3339),
	})
}

func (s *Server) adminGoogleSearchConsoleSummary(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	siteID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || siteID <= 0 {
		http.NotFound(w, r)
		return
	}
	if _, ok, err := s.platform.GetSite(siteID); err != nil {
		s.serverError(w, err)
		return
	} else if !ok {
		http.NotFound(w, r)
		return
	}
	in, ok, err := s.platform.SiteGoogleIntegration(siteID, platform.GoogleServiceSearchConsole)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok || !in.Enabled || strings.TrimSpace(in.GoogleAccountID) == "" || strings.TrimSpace(in.Property) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":            false,
			"status":        platform.GoogleSearchConsoleSummaryStatusError,
			"error_message": "Google Search Console 未完整接入，无法读取搜索数据。",
		})
		return
	}
	acc, ok, err := s.platform.GoogleAccount(platform.GoogleServiceSearchConsole, in.GoogleAccountID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"ok":            false,
			"status":        platform.GoogleSearchConsoleSummaryStatusError,
			"error_message": "没有找到 Google Search Console 授权账号。",
		})
		return
	}
	fetchedAt := time.Now()
	accessToken, err := s.googleAccessToken(r.Context(), r, acc)
	if err == nil {
		metrics, runErr := googleSearchConsoleSevenDaySummary(r.Context(), accessToken, in.Property)
		if runErr == nil {
			sum := &platform.SiteGoogleSearchConsoleSummary{
				SiteID:        siteID,
				Property:      in.Property,
				Clicks7D:      metrics.Clicks7D,
				Impressions7D: metrics.Impressions7D,
				CTR7D:         metrics.CTR7D,
				Position7D:    metrics.Position7D,
				Status:        platform.GoogleSearchConsoleSummaryStatusOK,
				FetchedAt:     fetchedAt,
			}
			if err := s.platform.UpsertSiteGoogleSearchConsoleSummary(sum); err != nil {
				s.serverError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":             true,
				"status":         sum.Status,
				"clicks_7d":      sum.Clicks7D,
				"impressions_7d": sum.Impressions7D,
				"ctr_7d":         sum.CTR7D,
				"position_7d":    sum.Position7D,
				"fetched_at":     sum.FetchedAt.Format(time.RFC3339),
			})
			return
		}
		err = runErr
	}
	msg := "搜索数据暂不可用"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		msg = err.Error()
	}
	sum := &platform.SiteGoogleSearchConsoleSummary{
		SiteID:       siteID,
		Property:     in.Property,
		Status:       platform.GoogleSearchConsoleSummaryStatusError,
		ErrorMessage: msg,
		FetchedAt:    fetchedAt,
	}
	if err := s.platform.UpsertSiteGoogleSearchConsoleSummary(sum); err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            false,
		"status":        sum.Status,
		"error_message": sum.ErrorMessage,
		"fetched_at":    sum.FetchedAt.Format(time.RFC3339),
	})
}

func (s *Server) adminGoogleSearchConsoleSites(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	googleAccountID := strings.TrimSpace(r.URL.Query().Get("account"))
	if googleAccountID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "请选择 Google Search Console 授权账号。"})
		return
	}
	acc, ok, err := s.platform.GoogleAccount(platform.GoogleServiceSearchConsole, googleAccountID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "message": "没有找到该 Google Search Console 授权账号。"})
		return
	}
	accessToken, err := s.googleAccessToken(r.Context(), r, acc)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	sites, err := googleSearchConsoleSites(r.Context(), accessToken)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sites": sites})
}

func (s *Server) adminCreateGoogleAnalyticsStream(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	siteID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || siteID <= 0 {
		http.NotFound(w, r)
		return
	}
	site, ok, err := s.platform.GetSite(siteID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	fail := func(msg string) {
		if wantsJSON(r) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": msg})
			return
		}
		s.flashGoogleOAuth(r, msg)
		s.redirectSiteGoogle(w, r, siteID)
	}
	failWithProperty := func(status int, msg, property string) {
		if wantsJSON(r) {
			writeJSON(w, status, map[string]any{"ok": false, "message": msg, "property": property, "property_created": true})
			return
		}
		s.flashGoogleOAuth(r, msg)
		s.redirectSiteGoogle(w, r, siteID)
	}
	googleAccountID := strings.TrimSpace(r.FormValue("google_account_id"))
	propertyRaw := strings.TrimSpace(r.FormValue("property"))
	property := normalizeGoogleAnalyticsPropertyName(propertyRaw)
	propertyMode := strings.ToLower(strings.TrimSpace(r.FormValue("property_mode")))
	analyticsAccount := normalizeGoogleAnalyticsAccountName(r.FormValue("analytics_account"))
	if propertyRaw == "__create__" {
		propertyMode = "create"
		property = ""
	}
	if googleAccountID == "" {
		fail("请选择 Google Analytics 授权账号。")
		return
	}
	acc, ok, err := s.platform.GoogleAccount(platform.GoogleServiceAnalytics, googleAccountID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok {
		fail("没有找到该 Google Analytics 授权账号。")
		return
	}
	if strings.TrimSpace(acc.Scopes) != "" && !acc.HasScope(googleAnalyticsEditScope) {
		fail("这个授权账号缺少 Google Analytics 编辑权限，请重新授权 Google Analytics 后再自动创建。")
		return
	}
	defaultURI := strings.TrimSpace(r.FormValue("default_uri"))
	if defaultURI == "" {
		defaultURI = s.defaultGoogleAnalyticsURI(r, site)
	}
	defaultURI, uriOK := normalizeGoogleDefaultURI(defaultURI)
	if !uriOK {
		fail("请填写有效的网站地址，例如 https://example.com。")
		return
	}
	accessToken, err := s.googleAccessToken(r.Context(), r, acc)
	if err != nil {
		fail(err.Error())
		return
	}
	displayName := googleAnalyticsDisplayName(site, defaultURI)
	createdProperty := false
	if propertyMode == "create" {
		accounts, _, err := googleAnalyticsAccountsAndProperties(r.Context(), accessToken)
		if err != nil {
			fail("读取 Google Analytics 账号失败：" + err.Error())
			return
		}
		analyticsAccount, err = selectGoogleAnalyticsAccount(accounts, analyticsAccount)
		if err != nil {
			fail(err.Error())
			return
		}
		newProperty, err := createGoogleAnalyticsProperty(r.Context(), accessToken, analyticsAccount, displayName)
		if err != nil {
			fail("创建 GA4 属性失败：" + err.Error())
			return
		}
		property = newProperty.Name
		createdProperty = true
	} else if !validGoogleAnalyticsPropertyName(property) {
		accounts, properties, err := googleAnalyticsAccountsAndProperties(r.Context(), accessToken)
		if err != nil {
			fail("读取 GA4 属性失败：" + err.Error())
			return
		}
		matchedProperty, matchedStream, reused, err := findGoogleAnalyticsWebDataStreamAcrossProperties(r.Context(), accessToken, properties, defaultURI)
		if err != nil {
			fail("检查已有 GA4 数据流失败：" + err.Error())
			return
		}
		if matchedStream != nil {
			property = matchedProperty
			stream := matchedStream
			measurementID := normalizeGoogleMeasurementID(stream.MeasurementID)
			if !validGoogleMeasurementID(measurementID) {
				fail("Google 数据流没有返回有效的 GA4 Measurement ID。")
				return
			}
			in := &platform.SiteGoogleIntegration{
				SiteID:          siteID,
				Service:         platform.GoogleServiceAnalytics,
				GoogleAccountID: googleAccountID,
				MeasurementID:   measurementID,
				Property:        property,
				DataStream:      strings.TrimSpace(stream.Name),
				Enabled:         true,
			}
			if err := s.platform.UpsertSiteGoogleIntegration(in); err != nil {
				s.serverError(w, err)
				return
			}
			msg := "已复用该域名已有 GA4 数据流并启用统计代码：" + measurementID
			if wantsJSON(r) {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg, "measurement_id": measurementID, "property": property, "data_stream": in.DataStream, "reused": reused})
				return
			}
			s.flashGoogleOAuth(r, msg)
			s.redirectSiteGoogle(w, r, siteID)
			return
		}
		switch len(properties) {
		case 0:
			analyticsAccount, err = selectGoogleAnalyticsAccount(accounts, analyticsAccount)
			if err != nil {
				fail("没有自动匹配到当前域名，也没有可用 GA4 属性。请点击 GA4 属性行的编辑图标，选择“创建新的 GA4 属性和统计代码”。")
				return
			}
			newProperty, err := createGoogleAnalyticsProperty(r.Context(), accessToken, analyticsAccount, displayName)
			if err != nil {
				fail("创建 GA4 属性失败：" + err.Error())
				return
			}
			property = newProperty.Name
			createdProperty = true
		case 1:
			property = properties[0].Name
		default:
			fail("没有自动匹配到当前域名的数据流。请点击 GA4 属性行的编辑图标，搜索选择已有属性，或选择“创建新的 GA4 属性和统计代码”。")
			return
		}
	}
	var stream *googleAnalyticsDataStream
	reused := false
	if createdProperty {
		s.googleAnalytics.invalidateAccount(googleAccountID)
		current, hasCurrent, currentErr := s.platform.SiteGoogleIntegration(siteID, platform.GoogleServiceAnalytics)
		if currentErr != nil {
			s.serverError(w, currentErr)
			return
		}
		if !hasCurrent || !current.Enabled {
			pending := &platform.SiteGoogleIntegration{
				SiteID:          siteID,
				Service:         platform.GoogleServiceAnalytics,
				GoogleAccountID: googleAccountID,
				Property:        property,
				Enabled:         false,
			}
			if err := s.platform.UpsertSiteGoogleIntegration(pending); err != nil {
				s.serverError(w, err)
				return
			}
		}
		stream, err = createGoogleAnalyticsWebDataStreamWithRetry(r.Context(), accessToken, property, displayName, defaultURI, true)
		if err != nil {
			msg := "GA4 属性已创建并已保留，但 Google 尚未允许创建数据流。请稍候后重试；系统不会重复创建属性。详情：" + err.Error()
			failWithProperty(http.StatusBadGateway, msg, property)
			return
		}
	} else {
		stream, reused, err = findGoogleAnalyticsWebDataStream(r.Context(), accessToken, property, defaultURI)
		if err != nil {
			fail("检查已有 GA4 数据流失败：" + err.Error())
			return
		}
		if stream == nil {
			stream, err = createGoogleAnalyticsWebDataStreamWithRetry(r.Context(), accessToken, property, displayName, defaultURI, false)
			if err != nil {
				fail("创建 GA4 数据流失败：" + err.Error())
				return
			}
		}
	}
	if reused && strings.TrimSpace(stream.Name) == "" {
		fail("Google 已找到匹配的数据流，但没有返回有效的数据流名称。")
		return
	}
	measurementID := normalizeGoogleMeasurementID(stream.MeasurementID)
	if !validGoogleMeasurementID(measurementID) {
		fail("Google 数据流没有返回有效的 GA4 Measurement ID。")
		return
	}
	in := &platform.SiteGoogleIntegration{
		SiteID:          siteID,
		Service:         platform.GoogleServiceAnalytics,
		GoogleAccountID: googleAccountID,
		MeasurementID:   measurementID,
		Property:        property,
		DataStream:      strings.TrimSpace(stream.Name),
		Enabled:         true,
	}
	if err := s.platform.UpsertSiteGoogleIntegration(in); err != nil {
		s.serverError(w, err)
		return
	}
	s.googleAnalytics.invalidateAccount(googleAccountID)
	msg := "已自动创建 GA4 数据流并启用统计代码：" + measurementID
	if createdProperty {
		msg = "已自动创建 GA4 属性和 Web 数据流并启用统计代码：" + measurementID
	}
	if reused {
		msg = "已复用该域名已有 GA4 数据流并启用统计代码：" + measurementID
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg, "measurement_id": measurementID, "property": property, "data_stream": in.DataStream, "reused": reused, "property_created": createdProperty})
		return
	}
	s.flashGoogleOAuth(r, msg)
	s.redirectSiteGoogle(w, r, siteID)
}

func (s *Server) adminCreateGoogleSearchConsoleProperty(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	siteID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || siteID <= 0 {
		http.NotFound(w, r)
		return
	}
	site, ok, err := s.platform.GetSite(siteID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	fail := func(status int, msg string) {
		if wantsJSON(r) {
			writeJSON(w, status, map[string]any{"ok": false, "message": msg})
			return
		}
		s.flashGoogleOAuth(r, msg)
		s.redirectSiteGoogleSearchModal(w, r, siteID)
	}
	googleAccountID := strings.TrimSpace(r.FormValue("google_account_id"))
	if googleAccountID == "" {
		fail(http.StatusBadRequest, "请选择 Google Search Console 授权账号。")
		return
	}
	property := strings.TrimSpace(r.FormValue("default_uri"))
	if property == "" {
		property = s.defaultGoogleAnalyticsURI(r, site)
	}
	if property == "" {
		property = strings.TrimSpace(r.FormValue("property"))
	}
	property, propertyOK := normalizeGoogleSearchConsoleSiteURL(property)
	if !propertyOK {
		fail(http.StatusBadRequest, "请填写有效的站点属性，例如 https://example.com/ 或 sc-domain:example.com。")
		return
	}
	acc, ok, err := s.platform.GoogleAccount(platform.GoogleServiceSearchConsole, googleAccountID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok {
		fail(http.StatusNotFound, "没有找到该 Google Search Console 授权账号。")
		return
	}
	accessToken, err := s.googleAccessToken(r.Context(), r, acc)
	if err != nil {
		fail(http.StatusBadRequest, err.Error())
		return
	}
	sites, err := googleSearchConsoleSites(r.Context(), accessToken)
	if err != nil {
		fail(http.StatusBadGateway, "读取 Google Search Console 站点属性失败："+err.Error())
		return
	}
	if matched, ok := findGoogleSearchConsoleMatchingSite(sites, property); ok {
		in := &platform.SiteGoogleIntegration{
			SiteID:          siteID,
			Service:         platform.GoogleServiceSearchConsole,
			GoogleAccountID: googleAccountID,
			Property:        strings.TrimSpace(matched.URL),
			Enabled:         true,
		}
		if err := s.platform.UpsertSiteGoogleIntegration(in); err != nil {
			s.serverError(w, err)
			return
		}
		msg := "已匹配并启用 Google Search Console：" + in.Property
		if wantsJSON(r) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg, "property": in.Property, "reused": true})
			return
		}
		s.flashGoogleOAuth(r, msg)
		s.redirectSiteGoogle(w, r, siteID)
		return
	}
	if strings.TrimSpace(acc.Scopes) != "" && !acc.HasScope(googleSearchConsoleScope) {
		fail(http.StatusBadRequest, "当前授权账号只有 Google Search Console 读取权限，且没有匹配到当前站点属性。请重新新增 Google 授权账号并允许 Search Console 管理权限，或先在 Google Search Console 手动添加并验证当前域名。")
		return
	}
	if err := googleSearchConsoleAddSite(r.Context(), accessToken, property); err != nil {
		fail(http.StatusBadGateway, "自动添加 Google Search Console 站点属性失败："+err.Error())
		return
	}
	sites, err = googleSearchConsoleSites(r.Context(), accessToken)
	if err != nil {
		fail(http.StatusBadGateway, "已请求添加站点属性，但重新读取 Google Search Console 失败："+err.Error())
		return
	}
	if matched, ok := findGoogleSearchConsoleMatchingSite(sites, property); ok {
		in := &platform.SiteGoogleIntegration{
			SiteID:          siteID,
			Service:         platform.GoogleServiceSearchConsole,
			GoogleAccountID: googleAccountID,
			Property:        strings.TrimSpace(matched.URL),
			Enabled:         true,
		}
		if err := s.platform.UpsertSiteGoogleIntegration(in); err != nil {
			s.serverError(w, err)
			return
		}
		msg := "已添加并启用 Google Search Console：" + in.Property
		if wantsJSON(r) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg, "property": in.Property, "reused": false})
			return
		}
		s.flashGoogleOAuth(r, msg)
		s.redirectSiteGoogle(w, r, siteID)
		return
	}
	propertyToSave := property
	if added, ok := findGoogleSearchConsoleAnySite(sites, property); ok {
		propertyToSave = strings.TrimSpace(added.URL)
	}
	in := &platform.SiteGoogleIntegration{
		SiteID:          siteID,
		Service:         platform.GoogleServiceSearchConsole,
		GoogleAccountID: googleAccountID,
		Property:        propertyToSave,
		Enabled:         false,
	}
	if err := s.platform.UpsertSiteGoogleIntegration(in); err != nil {
		s.serverError(w, err)
		return
	}
	msg := "已向 Google Search Console 添加站点属性。下一步请在弹窗里打开 Search Console 完成所有权验证，完成后点击“我已验证，重新检测并启用”。"
	if wantsJSON(r) {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"ok":                 true,
			"message":            msg,
			"property":           in.Property,
			"enabled":            false,
			"needs_verification": true,
			"verify_url":         googleSearchConsoleWebURL(in.Property),
		})
		return
	}
	s.flashGoogleOAuth(r, msg)
	s.redirectSiteGoogleSearchModal(w, r, siteID)
}

func (s *Server) adminSaveSiteGoogleIntegration(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	siteID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || siteID <= 0 {
		http.NotFound(w, r)
		return
	}
	if _, ok, err := s.platform.GetSite(siteID); err != nil {
		s.serverError(w, err)
		return
	} else if !ok {
		http.NotFound(w, r)
		return
	}
	service := platform.NormalizeGoogleService(r.FormValue("service"))
	if service == "" {
		s.flashGoogleOAuth(r, "未知的 Google 接入类型。")
		s.redirectSiteGoogle(w, r, siteID)
		return
	}
	if strings.TrimSpace(r.FormValue("action")) == "delete" {
		if err := s.platform.DeleteSiteGoogleIntegration(siteID, service); err != nil {
			s.serverError(w, err)
			return
		}
		s.flashGoogleOAuth(r, googleServiceLabel(service)+" 接入已解除。")
		s.redirectSiteGoogle(w, r, siteID)
		return
	}

	enabled := r.FormValue("enabled") == "1" || r.FormValue("enabled") == "on"
	in := &platform.SiteGoogleIntegration{
		SiteID:          siteID,
		Service:         service,
		GoogleAccountID: strings.TrimSpace(r.FormValue("google_account_id")),
		Enabled:         enabled,
	}
	switch service {
	case platform.GoogleServiceAnalytics:
		measurementID := normalizeGoogleMeasurementID(r.FormValue("measurement_id"))
		if measurementID != "" && !validGoogleMeasurementID(measurementID) {
			s.flashGoogleOAuth(r, "GA4 Measurement ID 格式不正确，请填写类似 G-XXXXXXXXXX 的值。")
			s.redirectSiteGoogle(w, r, siteID)
			return
		}
		if enabled && measurementID == "" {
			s.flashGoogleOAuth(r, "启用 Google Analytics 前，请先填写 GA4 Measurement ID。")
			s.redirectSiteGoogle(w, r, siteID)
			return
		}
		in.MeasurementID = measurementID
		in.Enabled = enabled && measurementID != ""
	case platform.GoogleServiceSearchConsole:
		property, propertyOK := normalizeGoogleSearchConsoleSiteURL(r.FormValue("property"))
		if enabled && in.GoogleAccountID == "" {
			s.flashGoogleOAuth(r, "启用 Google Search Console 前，请先选择授权账号。")
			s.redirectSiteGoogle(w, r, siteID)
			return
		}
		if enabled && !propertyOK {
			s.flashGoogleOAuth(r, "启用 Google Search Console 前，请先填写有效的站点属性。")
			s.redirectSiteGoogle(w, r, siteID)
			return
		}
		in.Property = property
		in.Enabled = enabled && in.GoogleAccountID != "" && propertyOK
	}
	if err := s.platform.UpsertSiteGoogleIntegration(in); err != nil {
		s.serverError(w, err)
		return
	}
	s.flashGoogleOAuth(r, googleServiceLabel(service)+" 站点接入已保存。")
	s.redirectSiteGoogle(w, r, siteID)
}

func (s *Server) redirectSiteGoogle(w http.ResponseWriter, r *http.Request, siteID int64) {
	target := "/admin/sites#site-card-" + strconv.FormatInt(siteID, 10)
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) redirectSiteGoogleSearchModal(w http.ResponseWriter, r *http.Request, siteID int64) {
	target := "/admin/sites#site-google-search-modal-" + strconv.FormatInt(siteID, 10)
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) adminGoogleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	if msg := strings.TrimSpace(r.URL.Query().Get("error")); msg != "" {
		s.flashGoogleOAuth(r, "Google 授权已取消或失败："+msg)
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
		return
	}
	service, ok, err := s.platform.ConsumeGoogleOAuthState(r.URL.Query().Get("state"))
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok {
		s.flashGoogleOAuth(r, "Google 授权状态已失效，请重新发起授权。")
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		s.flashGoogleOAuth(r, "Google 授权没有返回 code，请重新授权。")
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
		return
	}
	cfg := s.googleOAuthConfig(r)
	token, err := exchangeGoogleOAuthToken(r.Context(), cfg, code)
	if err != nil {
		s.flashGoogleOAuth(r, "Google 授权换取令牌失败："+err.Error())
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
		return
	}
	user, err := fetchGoogleUserInfo(r.Context(), token.AccessToken)
	if err != nil {
		s.flashGoogleOAuth(r, "Google 授权成功，但读取账号信息失败："+err.Error())
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
		return
	}
	if user.Sub == "" {
		s.flashGoogleOAuth(r, "Google 授权成功，但账号 ID 为空，请重新授权。")
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
		return
	}
	expiry := time.Time{}
	if token.ExpiresIn > 0 {
		expiry = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	scopes := token.Scope
	if strings.TrimSpace(scopes) == "" {
		scopes = strings.Join(googleOAuthScopes(service), " ")
	}
	services := googleOAuthAccountServices(service)
	if len(services) == 0 {
		s.flashGoogleOAuth(r, "未知的 Google 授权类型。")
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
		return
	}
	for _, svc := range services {
		err = s.platform.UpsertGoogleAccount(&platform.GoogleAccount{
			Service:         svc,
			GoogleAccountID: user.Sub,
			Email:           user.Email,
			Name:            user.Name,
			Picture:         user.Picture,
			Scopes:          scopes,
			AccessToken:     token.AccessToken,
			RefreshToken:    token.RefreshToken,
			TokenExpiry:     expiry,
		})
		if err != nil {
			s.serverError(w, err)
			return
		}
	}
	label := user.Email
	if label == "" {
		label = user.Name
	}
	if label == "" {
		label = user.Sub
	}
	if service == platform.GoogleServiceAll {
		s.flashGoogleOAuth(r, fmt.Sprintf("Google 已授权：%s，可用于 Google Analytics 和 Google Search Console。", label))
	} else {
		s.flashGoogleOAuth(r, fmt.Sprintf("%s 已授权：%s", googleServiceLabel(service), label))
	}
	http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
}

func (s *Server) flashGoogleOAuth(r *http.Request, msg string) {
	s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: strings.TrimSpace(msg)})
}

func (s *Server) applySiteGoogleIntegrations(r *http.Request, st *seo.Site) {
	if s == nil || st == nil || s.platform == nil {
		return
	}
	if r != nil {
		if previewNoindexFrom(r.Context()) || r.URL.Query().Get("visual_edit") == "1" {
			return
		}
		path := strings.TrimSpace(r.URL.Path)
		if path == "/admin" || strings.HasPrefix(path, "/admin/") || path == "/preview" || strings.HasPrefix(path, "/preview/") {
			return
		}
	}
	measurementID := s.siteAnalyticsMeasurementID()
	if measurementID == "" {
		return
	}
	snippet := googleAnalyticsHeadSnippet(measurementID)
	if strings.TrimSpace(st.InjectHead) == "" {
		st.InjectHead = snippet
		return
	}
	st.InjectHead = strings.TrimRight(st.InjectHead, "\n") + "\n" + snippet
}

func (s *Server) siteAnalyticsMeasurementID() string {
	if s == nil || s.platform == nil || s.platformSiteID <= 0 {
		return ""
	}
	in, ok, err := s.platform.SiteGoogleIntegration(s.platformSiteID, platform.GoogleServiceAnalytics)
	if err != nil || !ok || in == nil || !in.Enabled {
		return ""
	}
	measurementID := normalizeGoogleMeasurementID(in.MeasurementID)
	if !validGoogleMeasurementID(measurementID) {
		return ""
	}
	return measurementID
}

func googleAnalyticsHeadSnippet(measurementID string) string {
	measurementID = normalizeGoogleMeasurementID(measurementID)
	if !validGoogleMeasurementID(measurementID) {
		return ""
	}
	return fmt.Sprintf(`<script async src="https://www.googletagmanager.com/gtag/js?id=%s"></script>
<script>
  window.dataLayer = window.dataLayer || [];
  function gtag(){dataLayer.push(arguments);}
  gtag('js', new Date());
  gtag('config', '%s');
</script>`, measurementID, measurementID)
}

func normalizeGoogleMeasurementID(id string) string {
	return strings.ToUpper(strings.TrimSpace(id))
}

func validGoogleMeasurementID(id string) bool {
	id = normalizeGoogleMeasurementID(id)
	if len(id) < 4 || len(id) > 32 || !strings.HasPrefix(id, "G-") {
		return false
	}
	for i := 0; i < len(id); i++ {
		ch := id[i]
		if ch == '-' || (ch >= '0' && ch <= '9') || (ch >= 'A' && ch <= 'Z') {
			continue
		}
		return false
	}
	return true
}

type googleAnalyticsPropertyOption struct {
	Name                         string `json:"name"`
	DisplayName                  string `json:"display_name"`
	Account                      string `json:"account"`
	AccountDisplayName           string `json:"account_display_name"`
	DataStream                   string `json:"data_stream,omitempty"`
	DataStreamID                 string `json:"data_stream_id,omitempty"`
	DataStreamDisplayName        string `json:"data_stream_display_name,omitempty"`
	MeasurementID                string `json:"measurement_id,omitempty"`
	DefaultURI                   string `json:"default_uri,omitempty"`
	Matched                      bool   `json:"matched,omitempty"`
	MatchedDataStream            string `json:"matched_data_stream,omitempty"`
	MatchedDataStreamID          string `json:"matched_data_stream_id,omitempty"`
	MatchedDataStreamDisplayName string `json:"matched_data_stream_display_name,omitempty"`
	MatchedMeasurementID         string `json:"matched_measurement_id,omitempty"`
	MatchedDefaultURI            string `json:"matched_default_uri,omitempty"`
}

type googleAnalyticsAccountOption struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

type googleAnalyticsDataStream struct {
	Name          string
	MeasurementID string
	DefaultURI    string
	DisplayName   string
}

type googleAnalyticsMetricValue struct {
	Value string `json:"value"`
}

type googleAnalyticsSummaryMetrics struct {
	ActiveUsers7D int
	Sessions7D    int
}

type googleSearchConsoleSummaryMetrics struct {
	Clicks7D      int
	Impressions7D int
	CTR7D         float64
	Position7D    float64
}

type googleSearchConsoleSiteOption struct {
	URL             string `json:"url"`
	PermissionLevel string `json:"permission_level"`
}

func normalizeGoogleAnalyticsPropertyName(name string) string {
	return strings.TrimSpace(name)
}

func normalizeGoogleAnalyticsAccountName(name string) string {
	return strings.TrimSpace(name)
}

func googleAnalyticsDataStreamID(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	parts := strings.Split(name, "/")
	if len(parts) == 0 {
		return name
	}
	return strings.TrimSpace(parts[len(parts)-1])
}

func validGoogleAnalyticsPropertyName(name string) bool {
	name = normalizeGoogleAnalyticsPropertyName(name)
	if !strings.HasPrefix(name, "properties/") {
		return false
	}
	id := strings.TrimPrefix(name, "properties/")
	if id == "" {
		return false
	}
	for _, ch := range id {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func validGoogleAnalyticsAccountName(name string) bool {
	name = normalizeGoogleAnalyticsAccountName(name)
	if !strings.HasPrefix(name, "accounts/") {
		return false
	}
	id := strings.TrimPrefix(name, "accounts/")
	if id == "" {
		return false
	}
	for _, ch := range id {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func normalizeGoogleDefaultURI(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), true
}

func (s *Server) defaultGoogleAnalyticsURI(r *http.Request, site *platform.Site) string {
	if s == nil {
		return ""
	}
	// GA/GSC 追踪的是站点**对外公开**的地址（真实访客访问的域名），不是平台后台自身的 host。
	// 因此默认站点也要优先用它的正式站/绑定域名（如 ccvar.com），只有在完全没有对外域名时，
	// 才回退到平台 host（见函数末尾）——早期版本对默认站直接返回平台 host（cms.ccvar.com）是错的。
	if site != nil {
		if href, _ := s.platformOfficialSiteURL(site.ID); href != "" {
			return href
		}
	}
	// 默认站点不使用其存储的绑定域名（平台强制默认站走平台入口），只有非默认站点才回退到绑定域名；
	// 默认站点没有正式站入口时，最后回退到平台 host。
	if s.platform != nil && site != nil && !site.IsDefault {
		domains, err := s.platform.SiteDomains()
		if err == nil {
			var first *platform.SiteDomain
			for _, d := range domains {
				if d == nil || d.SiteID != site.ID || !d.Enabled || strings.TrimSpace(d.Host) == "" {
					continue
				}
				if first == nil {
					first = d
				}
				if d.IsPrimary {
					return d.Scheme + "://" + d.Host
				}
			}
			if first != nil {
				return first.Scheme + "://" + first.Host
			}
		}
	}
	return s.platformPublicBaseURL(r)
}

func (s *Server) googleAccessToken(ctx context.Context, r *http.Request, acc *platform.GoogleAccount) (string, error) {
	if acc == nil {
		return "", errors.New("Google 授权账号不存在")
	}
	if strings.TrimSpace(acc.AccessToken) != "" && (acc.TokenExpiry.IsZero() || time.Now().Before(acc.TokenExpiry.Add(-1*time.Minute))) {
		return strings.TrimSpace(acc.AccessToken), nil
	}
	if strings.TrimSpace(acc.RefreshToken) == "" {
		return "", errors.New("授权已过期且没有 refresh token，请重新授权 Google 账号")
	}
	cfg := s.googleOAuthConfig(r)
	token, err := refreshGoogleOAuthToken(ctx, cfg, acc.RefreshToken)
	if err != nil {
		return "", err
	}
	expiry := time.Time{}
	if token.ExpiresIn > 0 {
		expiry = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	scopes := strings.TrimSpace(token.Scope)
	if scopes == "" {
		scopes = acc.Scopes
	}
	if err := s.platform.UpsertGoogleAccount(&platform.GoogleAccount{
		Service:         acc.Service,
		GoogleAccountID: acc.GoogleAccountID,
		Email:           acc.Email,
		Name:            acc.Name,
		Picture:         acc.Picture,
		Scopes:          scopes,
		AccessToken:     token.AccessToken,
		RefreshToken:    token.RefreshToken,
		TokenExpiry:     expiry,
	}); err != nil {
		return "", err
	}
	return strings.TrimSpace(token.AccessToken), nil
}

func googleAnalyticsProperties(ctx context.Context, accessToken string) ([]googleAnalyticsPropertyOption, error) {
	_, properties, err := googleAnalyticsAccountsAndProperties(ctx, accessToken)
	return properties, err
}

func googleAnalyticsSevenDaySummary(ctx context.Context, accessToken, property string) (googleAnalyticsSummaryMetrics, error) {
	property = normalizeGoogleAnalyticsPropertyName(property)
	if !validGoogleAnalyticsPropertyName(property) {
		return googleAnalyticsSummaryMetrics{}, errors.New("GA4 属性无效，无法读取统计数据")
	}
	body := map[string]any{
		"dateRanges": []map[string]string{{"startDate": "7daysAgo", "endDate": "today"}},
		"metrics":    []map[string]string{{"name": "activeUsers"}, {"name": "sessions"}},
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return googleAnalyticsSummaryMetrics{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleAnalyticsDataURL+"/"+property+":runReport", &buf)
	if err != nil {
		return googleAnalyticsSummaryMetrics{}, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := googleHTTPClient.Do(req)
	if err != nil {
		return googleAnalyticsSummaryMetrics{}, err
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
		return googleAnalyticsSummaryMetrics{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := out.Error.Message
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return googleAnalyticsSummaryMetrics{}, errors.New(googleAnalyticsDataAPIErrorMessage(msg))
	}
	if len(out.Rows) == 0 {
		return googleAnalyticsSummaryMetrics{}, nil
	}
	return googleAnalyticsSummaryMetrics{
		ActiveUsers7D: googleAnalyticsMetricInt(out.Rows[0].MetricValues, 0),
		Sessions7D:    googleAnalyticsMetricInt(out.Rows[0].MetricValues, 1),
	}, nil
}

func googleAnalyticsMetricInt(values []googleAnalyticsMetricValue, index int) int {
	if index < 0 || index >= len(values) {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(values[index].Value))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func googleAnalyticsAccountsAndProperties(ctx context.Context, accessToken string) ([]googleAnalyticsAccountOption, []googleAnalyticsPropertyOption, error) {
	const attempts = 3
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		accounts, properties, err := googleAnalyticsAccountsAndPropertiesOnce(ctx, accessToken)
		if err == nil {
			return accounts, properties, nil
		}
		lastErr = err
		if attempt == attempts-1 || !isRetriableGoogleReadError(err) {
			break
		}
		if err := waitGoogleRetry(ctx, googleRetryDelay(attempt)); err != nil {
			return nil, nil, err
		}
	}
	return nil, nil, lastErr
}

func googleAnalyticsAccountsAndPropertiesOnce(ctx context.Context, accessToken string) ([]googleAnalyticsAccountOption, []googleAnalyticsPropertyOption, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleAnalyticsAdminURL+"/accountSummaries?pageSize=200", nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")
	resp, err := googleHTTPClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	var out struct {
		AccountSummaries []struct {
			Name              string `json:"name"`
			Account           string `json:"account"`
			DisplayName       string `json:"displayName"`
			PropertySummaries []struct {
				Property    string `json:"property"`
				DisplayName string `json:"displayName"`
			} `json:"propertySummaries"`
		} `json:"accountSummaries"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := out.Error.Message
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return nil, nil, newGoogleAPIError(resp.StatusCode, googleAnalyticsAdminAPIErrorMessage(msg))
	}
	if decodeErr != nil {
		return nil, nil, decodeErr
	}
	var accounts []googleAnalyticsAccountOption
	var properties []googleAnalyticsPropertyOption
	seenAccounts := map[string]bool{}
	for _, account := range out.AccountSummaries {
		accountName := normalizeGoogleAnalyticsAccountName(account.Account)
		if !validGoogleAnalyticsAccountName(accountName) {
			accountName = normalizeGoogleAnalyticsAccountName(account.Name)
		}
		if !validGoogleAnalyticsAccountName(accountName) && strings.HasPrefix(strings.TrimSpace(account.Name), "accountSummaries/") {
			accountName = "accounts/" + strings.TrimPrefix(strings.TrimSpace(account.Name), "accountSummaries/")
		}
		if !validGoogleAnalyticsAccountName(accountName) {
			continue
		}
		accountLabel := strings.TrimSpace(account.DisplayName)
		if accountLabel == "" {
			accountLabel = accountName
		}
		if !seenAccounts[accountName] {
			accounts = append(accounts, googleAnalyticsAccountOption{
				Name:        accountName,
				DisplayName: accountLabel,
			})
			seenAccounts[accountName] = true
		}
		for _, property := range account.PropertySummaries {
			name := normalizeGoogleAnalyticsPropertyName(property.Property)
			if !validGoogleAnalyticsPropertyName(name) {
				continue
			}
			label := strings.TrimSpace(property.DisplayName)
			if label == "" {
				label = name
			}
			properties = append(properties, googleAnalyticsPropertyOption{
				Name:               name,
				DisplayName:        label,
				Account:            accountName,
				AccountDisplayName: accountLabel,
			})
		}
	}
	return accounts, properties, nil
}

func selectGoogleAnalyticsAccount(accounts []googleAnalyticsAccountOption, requested string) (string, error) {
	requested = normalizeGoogleAnalyticsAccountName(requested)
	if requested != "" {
		if !validGoogleAnalyticsAccountName(requested) {
			return "", errors.New("Analytics 账号参数不正确。")
		}
		for _, account := range accounts {
			if account.Name == requested {
				return requested, nil
			}
		}
		return "", errors.New("当前授权账号无法访问所选 Analytics 账号。")
	}
	switch len(accounts) {
	case 0:
		return "", errors.New("当前授权账号没有可用的 Analytics 账号，请先在 Google Analytics 中开通。")
	case 1:
		return accounts[0].Name, nil
	default:
		return "", errors.New("当前授权账号可访问多个 Analytics 账号，请点击 GA4 属性行的编辑图标，选择用于新建 GA4 属性的账号。")
	}
}

func googleAnalyticsDisplayName(site *platform.Site, defaultURI string) string {
	if site != nil {
		if name := strings.TrimSpace(site.Name); name != "" {
			return "gcms - " + name
		}
		if slug := strings.TrimSpace(site.Slug); slug != "" {
			return "gcms - " + slug
		}
	}
	if u, err := url.Parse(defaultURI); err == nil && strings.TrimSpace(u.Host) != "" {
		return "gcms - " + u.Host
	}
	return "gcms site"
}

func createGoogleAnalyticsProperty(ctx context.Context, accessToken, account, displayName string) (*googleAnalyticsPropertyOption, error) {
	account = normalizeGoogleAnalyticsAccountName(account)
	if !validGoogleAnalyticsAccountName(account) {
		return nil, errors.New("Analytics 账号参数不正确")
	}
	displayName = strings.TrimSpace(displayName)
	if displayName == "" {
		displayName = "gcms site"
	}
	body := map[string]any{
		"parent":       account,
		"displayName":  displayName,
		"timeZone":     "Etc/UTC",
		"currencyCode": "USD",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleAnalyticsAdminURL+"/properties", bytes.NewReader(raw))
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
		Name        string `json:"name"`
		Parent      string `json:"parent"`
		DisplayName string `json:"displayName"`
		Error       struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := out.Error.Message
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return nil, newGoogleAPIError(resp.StatusCode, googleAnalyticsAdminAPIErrorMessage(msg))
	}
	if decodeErr != nil {
		return nil, decodeErr
	}
	name := normalizeGoogleAnalyticsPropertyName(out.Name)
	if !validGoogleAnalyticsPropertyName(name) {
		return nil, errors.New("Google 没有返回有效的 GA4 属性。")
	}
	label := strings.TrimSpace(out.DisplayName)
	if label == "" {
		label = displayName
	}
	parent := normalizeGoogleAnalyticsAccountName(out.Parent)
	if !validGoogleAnalyticsAccountName(parent) {
		parent = account
	}
	return &googleAnalyticsPropertyOption{
		Name:        name,
		DisplayName: label,
		Account:     parent,
	}, nil
}

func findGoogleAnalyticsWebDataStreamAcrossProperties(ctx context.Context, accessToken string, properties []googleAnalyticsPropertyOption, defaultURI string) (string, *googleAnalyticsDataStream, bool, error) {
	for _, property := range properties {
		name := normalizeGoogleAnalyticsPropertyName(property.Name)
		if !validGoogleAnalyticsPropertyName(name) {
			continue
		}
		stream, reused, err := findGoogleAnalyticsWebDataStream(ctx, accessToken, name, defaultURI)
		if err != nil {
			return "", nil, false, err
		}
		if stream != nil {
			return name, stream, reused, nil
		}
	}
	return "", nil, false, nil
}

func applyGoogleAnalyticsPropertyStreamSummaries(ctx context.Context, accessToken string, properties []googleAnalyticsPropertyOption, defaultURI string) (failed, attempted int, firstErr error) {
	defaultURI, hasDefaultURI := normalizeGoogleDefaultURI(defaultURI)
	type job struct {
		index int
		name  string
	}
	type result struct {
		index   int
		streams []*googleAnalyticsDataStream
		err     error
	}
	jobs := make(chan job, len(properties))
	results := make(chan result, len(properties))
	for i := range properties {
		name := normalizeGoogleAnalyticsPropertyName(properties[i].Name)
		if !validGoogleAnalyticsPropertyName(name) {
			continue
		}
		jobs <- job{index: i, name: name}
		attempted++
	}
	close(jobs)
	if attempted == 0 {
		return 0, 0, nil
	}

	workCtx, cancel := context.WithTimeout(ctx, googleAnalyticsStreamSummaryTimeout)
	defer cancel()
	workerCount := googleAnalyticsStreamSummaryWorkers
	if attempted < workerCount {
		workerCount = attempted
	}
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer wg.Done()
			for item := range jobs {
				streams, err := googleAnalyticsWebDataStreams(workCtx, accessToken, item.name)
				results <- result{index: item.index, streams: streams, err: err}
			}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	for item := range results {
		if item.err != nil {
			failed++
			if firstErr == nil {
				firstErr = item.err
			}
			continue
		}
		var fallback *googleAnalyticsDataStream
		var matched *googleAnalyticsDataStream
		for _, stream := range item.streams {
			if stream == nil {
				continue
			}
			if fallback == nil {
				fallback = stream
			}
			if !hasDefaultURI {
				continue
			}
			streamURI, ok := normalizeGoogleDefaultURI(stream.DefaultURI)
			if ok && streamURI == defaultURI {
				matched = stream
				break
			}
		}
		if matched == nil {
			setGoogleAnalyticsPropertyStreamSummary(&properties[item.index], fallback)
			continue
		}
		setGoogleAnalyticsPropertyStreamSummary(&properties[item.index], matched)
		properties[item.index].Matched = true
		properties[item.index].MatchedDataStream = strings.TrimSpace(matched.Name)
		properties[item.index].MatchedDataStreamID = googleAnalyticsDataStreamID(matched.Name)
		properties[item.index].MatchedDataStreamDisplayName = strings.TrimSpace(matched.DisplayName)
		properties[item.index].MatchedMeasurementID = normalizeGoogleMeasurementID(matched.MeasurementID)
		properties[item.index].MatchedDefaultURI = strings.TrimSpace(matched.DefaultURI)
	}
	return failed, attempted, firstErr
}

func setGoogleAnalyticsPropertyStreamSummary(property *googleAnalyticsPropertyOption, stream *googleAnalyticsDataStream) {
	if property == nil || stream == nil {
		return
	}
	property.DataStream = strings.TrimSpace(stream.Name)
	property.DataStreamID = googleAnalyticsDataStreamID(stream.Name)
	property.DataStreamDisplayName = strings.TrimSpace(stream.DisplayName)
	property.MeasurementID = normalizeGoogleMeasurementID(stream.MeasurementID)
	property.DefaultURI = strings.TrimSpace(stream.DefaultURI)
}

func googleAnalyticsWebDataStreams(ctx context.Context, accessToken, property string) ([]*googleAnalyticsDataStream, error) {
	const attempts = 3
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		streams, err := googleAnalyticsWebDataStreamsOnce(ctx, accessToken, property)
		if err == nil {
			return streams, nil
		}
		lastErr = err
		if attempt == attempts-1 || !isRetriableGoogleReadError(err) {
			break
		}
		if err := waitGoogleRetry(ctx, googleRetryDelay(attempt)); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func googleAnalyticsWebDataStreamsOnce(ctx context.Context, accessToken, property string) ([]*googleAnalyticsDataStream, error) {
	property = normalizeGoogleAnalyticsPropertyName(property)
	if !validGoogleAnalyticsPropertyName(property) {
		return nil, errors.New("GA4 属性参数不正确")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleAnalyticsAdminURL+"/"+property+"/dataStreams?pageSize=200", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")
	resp, err := googleHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		DataStreams []struct {
			Name          string `json:"name"`
			Type          string `json:"type"`
			DisplayName   string `json:"displayName"`
			WebStreamData struct {
				DefaultURI    string `json:"defaultUri"`
				MeasurementID string `json:"measurementId"`
			} `json:"webStreamData"`
		} `json:"dataStreams"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := out.Error.Message
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return nil, newGoogleAPIError(resp.StatusCode, googleAnalyticsAdminAPIErrorMessage(msg))
	}
	if decodeErr != nil {
		return nil, decodeErr
	}
	var streams []*googleAnalyticsDataStream
	for _, item := range out.DataStreams {
		if strings.TrimSpace(item.Type) != "" && item.Type != "WEB_DATA_STREAM" {
			continue
		}
		if strings.TrimSpace(item.WebStreamData.MeasurementID) == "" {
			continue
		}
		streams = append(streams, &googleAnalyticsDataStream{
			Name:          strings.TrimSpace(item.Name),
			DisplayName:   strings.TrimSpace(item.DisplayName),
			DefaultURI:    strings.TrimSpace(item.WebStreamData.DefaultURI),
			MeasurementID: item.WebStreamData.MeasurementID,
		})
	}
	return streams, nil
}

func findGoogleAnalyticsWebDataStream(ctx context.Context, accessToken, property, defaultURI string) (*googleAnalyticsDataStream, bool, error) {
	defaultURI, ok := normalizeGoogleDefaultURI(defaultURI)
	if !ok {
		return nil, false, errors.New("网站地址不正确")
	}
	streams, err := googleAnalyticsWebDataStreams(ctx, accessToken, property)
	if err != nil {
		return nil, false, err
	}
	for _, stream := range streams {
		if stream == nil {
			continue
		}
		streamURI, ok := normalizeGoogleDefaultURI(stream.DefaultURI)
		if ok && streamURI == defaultURI {
			return stream, true, nil
		}
	}
	return nil, false, nil
}

func createGoogleAnalyticsWebDataStream(ctx context.Context, accessToken, property, displayName, defaultURI string) (*googleAnalyticsDataStream, error) {
	property = normalizeGoogleAnalyticsPropertyName(property)
	if !validGoogleAnalyticsPropertyName(property) {
		return nil, errors.New("GA4 属性参数不正确")
	}
	defaultURI, ok := normalizeGoogleDefaultURI(defaultURI)
	if !ok {
		return nil, errors.New("网站地址不正确")
	}
	body := map[string]any{
		"type":        "WEB_DATA_STREAM",
		"displayName": strings.TrimSpace(displayName),
		"webStreamData": map[string]any{
			"defaultUri": defaultURI,
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleAnalyticsAdminURL+"/"+property+"/dataStreams", bytes.NewReader(raw))
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
		Name          string `json:"name"`
		WebStreamData struct {
			MeasurementID string `json:"measurementId"`
		} `json:"webStreamData"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := out.Error.Message
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return nil, newGoogleAPIError(resp.StatusCode, googleAnalyticsAdminAPIErrorMessage(msg))
	}
	if decodeErr != nil {
		return nil, decodeErr
	}
	return &googleAnalyticsDataStream{
		Name:          out.Name,
		MeasurementID: out.WebStreamData.MeasurementID,
		DefaultURI:    defaultURI,
		DisplayName:   strings.TrimSpace(displayName),
	}, nil
}

func createGoogleAnalyticsWebDataStreamWithRetry(ctx context.Context, accessToken, property, displayName, defaultURI string, allowPermissionPropagation bool) (*googleAnalyticsDataStream, error) {
	const attempts = 5
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		stream, err := createGoogleAnalyticsWebDataStream(ctx, accessToken, property, displayName, defaultURI)
		if err == nil {
			return stream, nil
		}
		lastErr = err
		if attempt == attempts-1 || !isRetriableGoogleAPIError(err, allowPermissionPropagation) {
			break
		}
		if err := waitGoogleRetry(ctx, googleRetryDelay(attempt)); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func googleSearchConsoleSites(ctx context.Context, accessToken string) ([]googleSearchConsoleSiteOption, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleSearchConsoleURL+"/sites", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")
	resp, err := googleHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		SiteEntry []struct {
			SiteURL         string `json:"siteUrl"`
			PermissionLevel string `json:"permissionLevel"`
		} `json:"siteEntry"`
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
		return nil, errors.New(googleSearchConsoleAPIErrorMessage(msg))
	}
	var sites []googleSearchConsoleSiteOption
	for _, item := range out.SiteEntry {
		u := strings.TrimSpace(item.SiteURL)
		if u == "" {
			continue
		}
		sites = append(sites, googleSearchConsoleSiteOption{
			URL:             u,
			PermissionLevel: strings.TrimSpace(item.PermissionLevel),
		})
	}
	return sites, nil
}

func googleSearchConsoleAddSite(ctx context.Context, accessToken, siteURL string) error {
	siteURL, ok := normalizeGoogleSearchConsoleSiteURL(siteURL)
	if !ok {
		return errors.New("站点属性不正确")
	}
	reqURL := googleSearchConsoleURL + "/sites/" + url.PathEscape(siteURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")
	resp, err := googleHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(out.Error.Message)
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return errors.New(googleSearchConsoleAPIErrorMessage(msg))
	}
	return nil
}

func googleSearchConsoleSevenDaySummary(ctx context.Context, accessToken, siteURL string) (googleSearchConsoleSummaryMetrics, error) {
	siteURL, ok := normalizeGoogleSearchConsoleSiteURL(siteURL)
	if !ok {
		return googleSearchConsoleSummaryMetrics{}, errors.New("Google Search Console 站点属性不正确，无法读取搜索数据")
	}
	end := time.Now().AddDate(0, 0, -1)
	start := end.AddDate(0, 0, -6)
	body := map[string]any{
		"startDate": start.Format("2006-01-02"),
		"endDate":   end.Format("2006-01-02"),
		"rowLimit":  1,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return googleSearchConsoleSummaryMetrics{}, err
	}
	reqURL := googleSearchConsoleURL + "/sites/" + url.PathEscape(siteURL) + "/searchAnalytics/query"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(raw))
	if err != nil {
		return googleSearchConsoleSummaryMetrics{}, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := googleHTTPClient.Do(req)
	if err != nil {
		return googleSearchConsoleSummaryMetrics{}, err
	}
	defer resp.Body.Close()
	var out struct {
		Rows []struct {
			Clicks      float64 `json:"clicks"`
			Impressions float64 `json:"impressions"`
			CTR         float64 `json:"ctr"`
			Position    float64 `json:"position"`
		} `json:"rows"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return googleSearchConsoleSummaryMetrics{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(out.Error.Message)
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return googleSearchConsoleSummaryMetrics{}, errors.New(googleSearchConsoleAPIErrorMessage(msg))
	}
	if len(out.Rows) == 0 {
		return googleSearchConsoleSummaryMetrics{}, nil
	}
	row := out.Rows[0]
	return googleSearchConsoleSummaryMetrics{
		Clicks7D:      googleRoundedMetric(row.Clicks),
		Impressions7D: googleRoundedMetric(row.Impressions),
		CTR7D:         row.CTR,
		Position7D:    row.Position,
	}, nil
}

func googleRoundedMetric(v float64) int {
	if v <= 0 {
		return 0
	}
	return int(v + 0.5)
}

func normalizeGoogleSearchConsoleSiteURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "sc-domain:") {
		domain := strings.TrimSpace(raw[len("sc-domain:"):])
		domain = strings.Trim(domain, "/")
		if domain == "" || strings.ContainsAny(domain, "/?#") {
			return "", false
		}
		return "sc-domain:" + strings.ToLower(domain), true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), true
}

func googleSearchConsoleCompareKey(raw string) string {
	normalized, ok := normalizeGoogleSearchConsoleSiteURL(raw)
	if ok {
		return strings.TrimRight(normalized, "/")
	}
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(raw)), "/")
}

func googleSearchConsoleCandidateKeys(raw string) []string {
	normalized, ok := normalizeGoogleSearchConsoleSiteURL(raw)
	if !ok {
		return nil
	}
	keys := []string{googleSearchConsoleCompareKey(normalized)}
	if strings.HasPrefix(normalized, "http://") || strings.HasPrefix(normalized, "https://") {
		if u, err := url.Parse(normalized); err == nil {
			host := strings.ToLower(u.Hostname())
			if host != "" {
				keys = append(keys, googleSearchConsoleCompareKey("sc-domain:"+host))
				if strings.HasPrefix(host, "www.") {
					keys = append(keys, googleSearchConsoleCompareKey("sc-domain:"+strings.TrimPrefix(host, "www.")))
				}
			}
		}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

func googleSearchConsoleSiteVerified(permissionLevel string) bool {
	switch strings.ToLower(strings.TrimSpace(permissionLevel)) {
	case "siteowner", "sitefulluser", "siterestricteduser":
		return true
	default:
		return false
	}
}

func googleSearchConsoleWebURL(property string) string {
	property = strings.TrimSpace(property)
	if property == "" {
		return "https://search.google.com/search-console"
	}
	return "https://search.google.com/search-console?resource_id=" + url.QueryEscape(property)
}

func findGoogleSearchConsoleMatchingSite(sites []googleSearchConsoleSiteOption, target string) (googleSearchConsoleSiteOption, bool) {
	keys := googleSearchConsoleCandidateKeys(target)
	if len(keys) == 0 {
		return googleSearchConsoleSiteOption{}, false
	}
	for _, key := range keys {
		for _, site := range sites {
			if !googleSearchConsoleSiteVerified(site.PermissionLevel) {
				continue
			}
			if googleSearchConsoleCompareKey(site.URL) == key {
				return site, true
			}
		}
	}
	return googleSearchConsoleSiteOption{}, false
}

func findGoogleSearchConsoleAnySite(sites []googleSearchConsoleSiteOption, target string) (googleSearchConsoleSiteOption, bool) {
	keys := googleSearchConsoleCandidateKeys(target)
	if len(keys) == 0 {
		return googleSearchConsoleSiteOption{}, false
	}
	for _, key := range keys {
		for _, site := range sites {
			if googleSearchConsoleCompareKey(site.URL) == key {
				return site, true
			}
		}
	}
	return googleSearchConsoleSiteOption{}, false
}

type googleTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func exchangeGoogleOAuthToken(ctx context.Context, cfg googleOAuthConfig, code string) (*googleTokenResponse, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("Google OAuth 客户端未配置")
	}
	form := url.Values{}
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", cfg.RedirectURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := googleHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out googleTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := out.ErrorDesc
		if msg == "" {
			msg = out.Error
		}
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return nil, errors.New(msg)
	}
	if out.AccessToken == "" {
		return nil, errors.New("Google 未返回 access_token")
	}
	return &out, nil
}

func refreshGoogleOAuthToken(ctx context.Context, cfg googleOAuthConfig, refreshToken string) (*googleTokenResponse, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("Google OAuth 客户端未配置")
	}
	form := url.Values{}
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	form.Set("refresh_token", strings.TrimSpace(refreshToken))
	form.Set("grant_type", "refresh_token")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := googleHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out googleTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := out.ErrorDesc
		if msg == "" {
			msg = out.Error
		}
		if msg == "" {
			msg = "HTTP " + strconv.Itoa(resp.StatusCode)
		}
		return nil, errors.New(msg)
	}
	if out.AccessToken == "" {
		return nil, errors.New("Google 未返回 access_token")
	}
	return &out, nil
}

type googleUserInfo struct {
	Sub     string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

func fetchGoogleUserInfo(ctx context.Context, accessToken string) (*googleUserInfo, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, errors.New("access_token 为空")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleOAuthUserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := googleHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out googleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.New("HTTP " + strconv.Itoa(resp.StatusCode))
	}
	return &out, nil
}
