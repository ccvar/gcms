package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"cms.ccvar.com/internal/platform"
)

// 这组端点供 Pilot 的原生“站点”页使用。它们和后台表单读写同一份平台库；
// 响应只给配置状态与非秘密标识，OAuth secret、refresh token、Bot token 永不回传。

type controlGoogleRangeInput struct {
	Mode string `json:"mode"`
	Days int    `json:"days"`
	From string `json:"from"`
	To   string `json:"to"`
}

type controlGlobalGoogleInput struct {
	ClientID     *string                  `json:"client_id"`
	ClientSecret *string                  `json:"client_secret"`
	RedirectURL  *string                  `json:"redirect_url"`
	DataRange    *controlGoogleRangeInput `json:"data_range"`
}

type controlGlobalTelegramInput struct {
	BotToken *string `json:"bot_token"`
	Clear    bool    `json:"clear"`
}

type controlGlobalIntegrationsInput struct {
	Google     *controlGlobalGoogleInput   `json:"google"`
	Telegram   *controlGlobalTelegramInput `json:"telegram"`
	Cloudflare *controlCloudflareInput     `json:"cloudflare"`
}

type controlSiteGoogleInput struct {
	Clear         bool   `json:"clear"`
	Enabled       bool   `json:"enabled"`
	AccountID     string `json:"account_id"`
	MeasurementID string `json:"measurement_id"`
	Property      string `json:"property"`
	DataStream    string `json:"data_stream"`
}

type controlSiteIntegrationsInput struct {
	Analytics     *controlSiteGoogleInput `json:"analytics"`
	SearchConsole *controlSiteGoogleInput `json:"search_console"`
}

type controlSiteGoogleProvisionInput struct {
	AccountID        string `json:"account_id"`
	DefaultURI       string `json:"default_uri"`
	Property         string `json:"property"`
	PropertyMode     string `json:"property_mode"`
	AnalyticsAccount string `json:"analytics_account"`
}

func (s *Server) servePlatformControlIntegrations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if _, ok := s.requirePlatformControlKey(w, r, apiScopeSiteRead); !ok {
			return
		}
		writeJSON(w, http.StatusOK, s.controlGlobalIntegrationsResponse(r))
	case http.MethodPut:
		key, ok := s.requirePlatformControlKey(w, r, apiScopeSiteWrite)
		if !ok {
			return
		}
		var in controlGlobalIntegrationsInput
		if !decodeAPIJSON(w, r, &in) {
			return
		}
		if errCode, errMessage := s.applyControlGlobalIntegrations(r, in); errCode != "" {
			apiError(w, http.StatusBadRequest, errCode, errMessage)
			return
		}
		_ = s.platform.CreatePlatformAutomationLog(key.ID, 0, "integrations_update", "platform", 0, "已通过 Pilot 更新平台接入配置")
		writeJSON(w, http.StatusOK, s.controlGlobalIntegrationsResponse(r))
	default:
		w.Header().Set("Allow", "GET, PUT")
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET 或 PUT。")
	}
}

func (s *Server) applyControlGlobalIntegrations(r *http.Request, in controlGlobalIntegrationsInput) (string, string) {
	if in.Google != nil {
		google := in.Google
		current := s.googleOAuthConfig(r)
		clientID := current.ClientID
		clientSecret := current.ClientSecret
		if google.ClientID != nil {
			clientID = strings.TrimSpace(*google.ClientID)
			if clientID == "" {
				return "google_client_id_required", "Google OAuth Client ID 不能为空。"
			}
		}
		if google.ClientSecret != nil && strings.TrimSpace(*google.ClientSecret) != "" {
			clientSecret = strings.TrimSpace(*google.ClientSecret)
		}
		if (google.ClientID != nil || google.ClientSecret != nil) && (clientID == "" || clientSecret == "") {
			return "google_oauth_incomplete", "请同时配置 Google OAuth Client ID 和 Client Secret。"
		}
		var normalizedRange *googleDataRange
		if google.DataRange != nil {
			rangeValue := googleDataRange{Mode: strings.TrimSpace(google.DataRange.Mode), Days: google.DataRange.Days, From: strings.TrimSpace(google.DataRange.From), To: strings.TrimSpace(google.DataRange.To)}
			normalized := s.googleDataRangeFromValue(rangeValue)
			if normalized.Mode == "" {
				return "google_data_range_invalid", "数据范围无效；固定范围请选择 7、15 或 30 天，自定义范围最多 90 天。"
			}
			normalizedRange = &normalized
		}

		if google.ClientID != nil {
			if err := s.platform.SetSetting(googleOAuthClientIDKey, clientID); err != nil {
				return "store_error", "保存 Google OAuth Client ID 失败。"
			}
		}
		if google.ClientSecret != nil && strings.TrimSpace(*google.ClientSecret) != "" {
			if err := s.platform.SetSetting(googleOAuthClientSecretKey, clientSecret); err != nil {
				return "store_error", "保存 Google OAuth Client Secret 失败。"
			}
		}
		if google.RedirectURL != nil {
			if err := s.platform.SetSetting(googleOAuthRedirectURLKey, strings.TrimSpace(*google.RedirectURL)); err != nil {
				return "store_error", "保存 Google OAuth 回调地址失败。"
			}
		}
		if normalizedRange != nil {
			raw, _ := json.Marshal(normalizedRange)
			if err := s.platform.SetSetting(googleDataRangeKey, string(raw)); err != nil {
				return "store_error", "保存 Google 数据范围失败。"
			}
		}
	}
	if in.Telegram != nil {
		token := s.platformTelegramBotToken()
		if in.Telegram.Clear {
			token = ""
		} else if in.Telegram.BotToken != nil && strings.TrimSpace(*in.Telegram.BotToken) != "" {
			token = strings.TrimSpace(*in.Telegram.BotToken)
		}
		if err := s.platform.SetSetting(platformTelegramBotTokenKey, token); err != nil {
			return "store_error", "保存 Telegram Bot Token 失败。"
		}
	}
	if in.Cloudflare != nil {
		if code, message := s.applyControlCloudflare(r.Context(), *in.Cloudflare); code != "" {
			return code, message
		}
	}
	return "", ""
}

func (s *Server) controlGlobalIntegrationsResponse(r *http.Request) map[string]any {
	cfg := s.googleOAuthConfig(r)
	dataRange := s.googleDataRange()
	return map[string]any{
		"google": map[string]any{
			"oauth_configured":         cfg.ClientID != "" && cfg.ClientSecret != "",
			"client_id":                cfg.ClientID,
			"client_secret_configured": cfg.ClientSecret != "",
			"redirect_url":             cfg.RedirectURL,
			"data_range":               map[string]any{"mode": dataRange.Mode, "days": dataRange.Days, "from": dataRange.From, "to": dataRange.To, "label": dataRange.Label},
			"analytics_accounts":       s.controlGoogleAccounts(platform.GoogleServiceAnalytics),
			"search_console_accounts":  s.controlGoogleAccounts(platform.GoogleServiceSearchConsole),
			"authorize_url":            s.absForPlatformRequest(r, "/admin/sites"),
		},
		"telegram": map[string]any{
			"shared_bot_configured": s.platformTelegramBotToken() != "",
		},
		"cloudflare": s.controlCloudflareAuthorizationResponse(),
	}
}

// controlCloudflareAuthorizationResponse only exposes safe authorization metadata.
// The actual Cloudflare tokens stay in GCMS settings and are never returned to Pilot.
func (s *Server) controlCloudflareAuthorizationResponse() map[string]any {
	if s == nil || s.platform == nil {
		return map[string]any{"authorizations": []map[string]any{}, "zones": []map[string]any{}}
	}
	authorizations := s.readControlCloudflareAuthorizations()
	items := make([]map[string]any, 0, len(authorizations))
	zones := []map[string]any{}
	zoneSeen := map[string]bool{}
	accountName := ""
	zoneName := ""
	for _, authorization := range authorizations {
		item := controlCloudflareAuthorizationSafeItem(authorization)
		item["purpose"] = "DNS、域名绑定与 Cloudflare 部署"
		items = append(items, item)
		if accountName == "" {
			accountName = authorization.AccountName
		}
		for _, zone := range authorization.Zones {
			if zoneSeen[zone.ID] {
				continue
			}
			zoneSeen[zone.ID] = true
			zones = append(zones, map[string]any{
				"id": zone.ID, "name": zone.Name,
				"account_id": zone.AccountID, "account": zone.AccountName,
				"authorization_id": authorization.ID,
			})
		}
	}
	if len(zones) == 1 {
		zoneName, _ = zones[0]["name"].(string)
	}
	configured := len(items) > 0
	return map[string]any{
		"authorizations":      items,
		"authorization_count": len(items),
		"configured":          configured,
		"dns_configured":      configured,
		"deploy_configured":   configured,
		"zones":               zones,
		"zone_count":          len(zones),
		"zone":                zoneName,
		"account":             accountName,
	}
}

func (s *Server) controlGoogleAccounts(service string) []map[string]any {
	items := []map[string]any{}
	if s == nil || s.platform == nil {
		return items
	}
	accounts, err := s.platform.GoogleAccounts(service)
	if err != nil {
		return items
	}
	for _, account := range accounts {
		if account == nil || strings.TrimSpace(account.GoogleAccountID) == "" {
			continue
		}
		label := strings.TrimSpace(account.Name)
		if label == "" {
			label = controlMaskEmail(account.Email)
		}
		items = append(items, map[string]any{
			"account_id": account.GoogleAccountID,
			"label":      label,
			"email":      controlMaskEmail(account.Email),
		})
	}
	return items
}

func controlMaskEmail(value string) string {
	value = strings.TrimSpace(value)
	local, domain, ok := strings.Cut(value, "@")
	if !ok || local == "" || domain == "" {
		return value
	}
	localRunes := []rune(local)
	visible := 1
	if len(localRunes) > 3 {
		visible = 2
	}
	if visible > len(localRunes) {
		visible = len(localRunes)
	}
	return string(localRunes[:visible]) + "***@" + domain
}

func (s *Server) servePlatformControlSiteIntegrations(w http.ResponseWriter, r *http.Request, siteID int64) {
	if siteID <= 0 {
		apiError(w, http.StatusBadRequest, "invalid_site_id", "站点 ID 无效。")
		return
	}
	switch r.Method {
	case http.MethodGet:
		key, ok := s.requirePlatformControlKey(w, r, apiScopeSiteRead)
		if !ok || !controlSiteMembershipAllowed(w, key, siteID) {
			return
		}
		if !s.controlIntegrationSiteExists(w, siteID) {
			return
		}
		writeJSON(w, http.StatusOK, s.controlSiteIntegrationsResponse(siteID))
	case http.MethodPut:
		key, ok := s.requirePlatformControlKey(w, r, apiScopeSiteWrite)
		if !ok || !controlSiteMembershipAllowed(w, key, siteID) {
			return
		}
		if !s.controlIntegrationSiteExists(w, siteID) {
			return
		}
		var in controlSiteIntegrationsInput
		if !decodeAPIJSON(w, r, &in) {
			return
		}
		if code, message := s.applyControlSiteIntegrations(siteID, in); code != "" {
			apiError(w, http.StatusBadRequest, code, message)
			return
		}
		_ = s.platform.CreatePlatformAutomationLog(key.ID, siteID, "site_integrations_update", "site", siteID, "已通过 Pilot 更新站点 Google 接入")
		writeJSON(w, http.StatusOK, s.controlSiteIntegrationsResponse(siteID))
	default:
		w.Header().Set("Allow", "GET, PUT")
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET 或 PUT。")
	}
}

// servePlatformControlSiteGoogleProvision exposes the same domain-aware setup
// workflow used by the GCMS admin. It is intentionally separate from the generic
// integration PUT endpoint: "enabled" is an outcome of successful provisioning,
// not a value Pilot may optimistically persist.
func (s *Server) servePlatformControlSiteGoogleProvision(w http.ResponseWriter, r *http.Request, siteID int64, service string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST。")
		return
	}
	key, ok := s.requirePlatformControlKey(w, r, apiScopeSiteWrite)
	if !ok || !controlSiteMembershipAllowed(w, key, siteID) {
		return
	}
	if !s.controlIntegrationSiteExists(w, siteID) {
		return
	}
	var in controlSiteGoogleProvisionInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	form := url.Values{}
	form.Set("google_account_id", strings.TrimSpace(in.AccountID))
	form.Set("default_uri", strings.TrimSpace(in.DefaultURI))
	form.Set("property", strings.TrimSpace(in.Property))
	form.Set("property_mode", strings.TrimSpace(in.PropertyMode))
	form.Set("analytics_account", strings.TrimSpace(in.AnalyticsAccount))
	r.Form = form
	r.PostForm = form
	r.SetPathValue("id", strconv.FormatInt(siteID, 10))
	r.Header.Set("Accept", "application/json")

	_ = s.platform.CreatePlatformAutomationLog(key.ID, siteID, "site_google_provision", "site", siteID, "已通过 Pilot 发起站点 Google 接入")
	switch service {
	case platform.GoogleServiceAnalytics:
		s.createGoogleAnalyticsStream(w, r, false)
	case platform.GoogleServiceSearchConsole:
		s.createGoogleSearchConsoleProperty(w, r, false)
	default:
		apiError(w, http.StatusNotFound, "google_service_not_found", "未知的 Google 接入类型。")
	}
}

func (s *Server) controlIntegrationSiteExists(w http.ResponseWriter, siteID int64) bool {
	_, ok, err := s.platform.GetSite(siteID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "site_read_failed", "无法读取站点。")
		return false
	}
	if !ok {
		apiError(w, http.StatusNotFound, "site_not_found", "站点不存在。")
		return false
	}
	return true
}

func (s *Server) applyControlSiteIntegrations(siteID int64, in controlSiteIntegrationsInput) (string, string) {
	var analyticsMutation *controlSiteGoogleMutation
	var searchConsoleMutation *controlSiteGoogleMutation
	if in.Analytics != nil {
		mutation, code, message := s.prepareControlSiteGoogleIntegration(siteID, platform.GoogleServiceAnalytics, *in.Analytics)
		if code != "" {
			return code, message
		}
		analyticsMutation = mutation
	}
	if in.SearchConsole != nil {
		mutation, code, message := s.prepareControlSiteGoogleIntegration(siteID, platform.GoogleServiceSearchConsole, *in.SearchConsole)
		if code != "" {
			return code, message
		}
		searchConsoleMutation = mutation
	}
	for _, mutation := range []*controlSiteGoogleMutation{analyticsMutation, searchConsoleMutation} {
		if mutation == nil {
			continue
		}
		if mutation.clear {
			if err := s.platform.DeleteSiteGoogleIntegration(siteID, mutation.service); err != nil {
				return "store_error", "解除 Google 接入失败。"
			}
			continue
		}
		if err := s.platform.UpsertSiteGoogleIntegration(mutation.integration); err != nil {
			return "store_error", "保存站点 Google 接入失败。"
		}
	}
	return "", ""
}

type controlSiteGoogleMutation struct {
	service     string
	clear       bool
	integration *platform.SiteGoogleIntegration
}

func (s *Server) prepareControlSiteGoogleIntegration(siteID int64, service string, input controlSiteGoogleInput) (*controlSiteGoogleMutation, string, string) {
	if input.Clear {
		return &controlSiteGoogleMutation{service: service, clear: true}, "", ""
	}
	accountID := strings.TrimSpace(input.AccountID)
	if accountID != "" {
		if _, ok, err := s.platform.GoogleAccount(service, accountID); err != nil {
			return nil, "store_error", "读取 Google 授权账号失败。"
		} else if !ok {
			return nil, "google_account_not_found", "选择的 Google 授权账号不存在，请重新授权。"
		}
	}
	in := &platform.SiteGoogleIntegration{SiteID: siteID, Service: service, GoogleAccountID: accountID, Enabled: input.Enabled}
	switch service {
	case platform.GoogleServiceAnalytics:
		in.MeasurementID = normalizeGoogleMeasurementID(input.MeasurementID)
		in.Property = normalizeGoogleAnalyticsPropertyName(input.Property)
		in.DataStream = strings.TrimSpace(input.DataStream)
		if in.MeasurementID != "" && !validGoogleMeasurementID(in.MeasurementID) {
			return nil, "analytics_measurement_invalid", "GA4 Measurement ID 格式不正确，请填写类似 G-XXXXXXXXXX 的值。"
		}
		if in.Property != "" && !validGoogleAnalyticsPropertyName(in.Property) {
			return nil, "analytics_property_invalid", "GA4 Property 格式不正确，请填写 properties/数字。"
		}
		if in.Enabled && (in.GoogleAccountID == "" || in.MeasurementID == "" || in.Property == "") {
			return nil, "analytics_incomplete", "启用 GA 前需要授权账号、Measurement ID 和 Property。"
		}
	case platform.GoogleServiceSearchConsole:
		if strings.TrimSpace(input.Property) != "" {
			var ok bool
			in.Property, ok = normalizeGoogleSearchConsoleSiteURL(input.Property)
			if !ok {
				return nil, "search_console_property_invalid", "Search Console 属性无效，请填写 https://example.com/ 或 sc-domain:example.com。"
			}
		}
		if in.Enabled && (in.GoogleAccountID == "" || in.Property == "") {
			return nil, "search_console_incomplete", "启用 Search Console 前需要授权账号和站点属性。"
		}
	}
	return &controlSiteGoogleMutation{service: service, integration: in}, "", ""
}

func (s *Server) controlSiteIntegrationsResponse(siteID int64) map[string]any {
	response := map[string]any{
		"analytics":      map[string]any{"configured": false, "enabled": false},
		"search_console": map[string]any{"configured": false, "enabled": false},
	}
	for _, service := range []string{platform.GoogleServiceAnalytics, platform.GoogleServiceSearchConsole} {
		integration, ok, err := s.platform.SiteGoogleIntegration(siteID, service)
		if err != nil || !ok || integration == nil {
			continue
		}
		item := map[string]any{
			"configured":     true,
			"enabled":        integration.Enabled,
			"account_id":     integration.GoogleAccountID,
			"measurement_id": integration.MeasurementID,
			"property":       integration.Property,
			"data_stream":    integration.DataStream,
		}
		if service == platform.GoogleServiceAnalytics {
			response["analytics"] = item
		} else {
			if integration.Property != "" && !integration.Enabled {
				item["needs_verification"] = true
				item["verify_url"] = googleSearchConsoleWebURL(integration.Property)
			}
			response["search_console"] = item
		}
	}
	return response
}
