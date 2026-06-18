package web

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	cloudflareAccountIDKey    = "cloudflare.account_id"
	cloudflareAPITokenKey     = "cloudflare.api_token"
	cloudflareWorkerNameKey   = "cloudflare.worker_name"
	cloudflareZoneIDKey       = "cloudflare.zone_id"
	cloudflareRoutePatternKey = "cloudflare.route_pattern"
	cloudflareOriginURLKey    = "cloudflare.origin_url"
	cloudflareHTMLTTLKey      = "cloudflare.html_cache_ttl"
	cloudflareAutoSyncKey     = "cloudflare.auto_sync"
	cloudflareConnectStateKey = "cloudflare.connect_state"
	cloudflareConnectNextKey  = "cloudflare.connect_next"
	cloudflareAccountNameKey  = "cloudflare.account_name"
	cloudflareZoneNameKey     = "cloudflare.zone_name"
	cloudflareConnectedAtKey  = "cloudflare.connected_at"
	cloudflareOAuthIDKey      = "cloudflare.oauth_client_id"
	cloudflareOAuthSecretKey  = "cloudflare.oauth_client_secret"
	cloudflareOAuthAccessKey  = "cloudflare.oauth_access_token"
	cloudflareOAuthRefreshKey = "cloudflare.oauth_refresh_token"
	cloudflareOAuthTypeKey    = "cloudflare.oauth_token_type"
	cloudflareOAuthExpiresKey = "cloudflare.oauth_expires_at"

	cloudflareDefaultWorkerName = "gcms-frontend"
	cloudflareDefaultHTMLTTL    = 300
	cloudflareAPITimeout        = 70 * time.Second
	cloudflareStaleAfter        = 3 * time.Minute
	cloudflareConnectTTL        = 15 * time.Minute
	cloudflareOAuthSkew         = 2 * time.Minute
	cloudflareCallbackPath      = "/admin/settings/cloudflare/callback"
)

var (
	cloudflareWorkerNameRE      = regexp.MustCompile(`[^a-z0-9-]+`)
	cloudflareOAuthAuthorizeURL = "https://dash.cloudflare.com/oauth2/auth"
	cloudflareOAuthTokenURL     = "https://dash.cloudflare.com/oauth2/token"
)

type CloudflareConfig struct {
	AccountID         string
	APIToken          string
	WorkerName        string
	ZoneID            string
	RoutePattern      string
	OriginURL         string
	HTMLCacheTTL      int
	AutoSync          bool
	OAuthClientID     string
	OAuthClientSecret string
	OAuthAccessToken  string
	OAuthRefreshToken string
	OAuthTokenType    string
	OAuthExpiresAt    string
	AccountName       string
	ZoneName          string
	ConnectedAt       string
}

type CloudflareStatus struct {
	Status       string `json:"status"`
	Step         string `json:"step"`
	Message      string `json:"message"`
	WorkerName   string `json:"worker_name"`
	RoutePattern string `json:"route_pattern"`
	UpdatedAt    string `json:"updated_at"`
	LastDeployAt string `json:"last_deploy_at,omitempty"`
	LastPurgeAt  string `json:"last_purge_at,omitempty"`
	Configured   bool   `json:"configured"`
	TokenSet     bool   `json:"token_set"`
	AutoSync     bool   `json:"auto_sync"`
	Running      bool   `json:"running"`
}

type CloudflareView struct {
	Config           CloudflareConfig
	Status           *CloudflareStatus
	TokenSet         bool
	OAuth            bool
	OAuthClientSet   bool
	Configured       bool
	CallbackURL      string
	TokenTemplateURL string
}

type cloudflareOAuthToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

type cloudflareOAuthTarget struct {
	AccountID   string
	AccountName string
	ZoneID      string
	ZoneName    string
}

type cloudflareAccount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cloudflareZone struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Account cloudflareAccount `json:"account"`
}

type cloudflareAPIResponse struct {
	Success bool            `json:"success"`
	Errors  []cloudflareErr `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cloudflareErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cloudflareRoute struct {
	ID      string `json:"id"`
	Pattern string `json:"pattern"`
	Script  string `json:"script"`
}

func cloudflareStatusPath() string {
	return filepath.Join(upgradeRoot(), "run", "cloudflare-deploy.json")
}

func (cfg CloudflareConfig) tokenSet() bool {
	return strings.TrimSpace(cfg.APIToken) != "" || cfg.oauthSet()
}

func (cfg CloudflareConfig) directSet() bool {
	return strings.TrimSpace(cfg.AccountID) != "" && strings.TrimSpace(cfg.APIToken) != ""
}

func (cfg CloudflareConfig) oauthClientSet() bool {
	return strings.TrimSpace(cfg.OAuthClientID) != "" && strings.TrimSpace(cfg.OAuthClientSecret) != ""
}

func (cfg CloudflareConfig) oauthSet() bool {
	return cfg.oauthClientSet() && strings.TrimSpace(cfg.OAuthRefreshToken) != ""
}

func (cfg CloudflareConfig) configured() bool {
	return cfg.tokenSet() &&
		strings.TrimSpace(cfg.WorkerName) != "" &&
		strings.TrimSpace(cfg.OriginURL) != ""
}

func (cfg CloudflareConfig) validateDeploy() error {
	if !cfg.tokenSet() {
		return errors.New("请先粘贴 Cloudflare API Token，或在高级设置里完成 OAuth 授权。")
	}
	if strings.TrimSpace(cfg.WorkerName) == "" {
		return errors.New("请填写 Worker 名称。")
	}
	if _, err := url.ParseRequestURI(cfg.OriginURL); err != nil || !(strings.HasPrefix(cfg.OriginURL, "http://") || strings.HasPrefix(cfg.OriginURL, "https://")) {
		return errors.New("源站地址必须是完整的 http:// 或 https:// 地址。")
	}
	if cfg.RoutePattern != "" && strings.Contains(cfg.RoutePattern, "://") {
		return errors.New("Worker 路由请填写 example.com/* 这种格式，不要带 http:// 或 https://。")
	}
	return nil
}

func cloudflareStatusFailed(cfg CloudflareConfig, step, msg string) CloudflareStatus {
	if strings.TrimSpace(msg) == "" {
		msg = "Cloudflare 部署失败。"
	}
	return CloudflareStatus{
		Status:       "failed",
		Step:         step,
		Message:      msg,
		WorkerName:   cfg.WorkerName,
		RoutePattern: cfg.RoutePattern,
		Configured:   cfg.configured(),
		TokenSet:     cfg.tokenSet(),
		AutoSync:     cfg.AutoSync,
	}
}

func normalizeCloudflareWorkerName(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, "_", "-")
	v = cloudflareWorkerNameRE.ReplaceAllString(v, "-")
	v = strings.Trim(v, "-")
	if v == "" {
		return cloudflareDefaultWorkerName
	}
	if len(v) > 63 {
		v = strings.Trim(v[:63], "-")
	}
	if v == "" {
		return cloudflareDefaultWorkerName
	}
	return v
}

func normalizeCloudflareOrigin(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return v
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

func normalizeCloudflareRoutePattern(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.Contains(v, "://") {
		if u, err := url.Parse(v); err == nil && u.Host != "" {
			path := strings.TrimSpace(u.EscapedPath())
			if path == "" || path == "/" {
				return u.Host + "/*"
			}
			return u.Host + strings.TrimRight(path, "/") + "/*"
		}
	}
	if strings.Contains(v, " ") {
		v = strings.Fields(v)[0]
	}
	if strings.HasSuffix(v, "/*") || strings.Contains(v, "*") {
		return v
	}
	if strings.Contains(v, "/") {
		return strings.TrimRight(v, "/") + "/*"
	}
	return v + "/*"
}

func (s *Server) defaultCloudflareRoutePattern() string {
	return cloudflareRoutePatternFromBaseURL(s.baseURL)
}

func cloudflareRoutePatternFromBaseURL(base string) string {
	base = strings.TrimSpace(base)
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if cloudflareLocalHost(host) {
		return ""
	}
	return u.Host + "/*"
}

func (s *Server) defaultCloudflareOriginURL() string {
	return cloudflareOriginFromBaseURL(s.baseURL)
}

func cloudflareOriginFromBaseURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if cloudflareLocalHost(host) {
		return ""
	}
	return base
}

func cloudflareLocalHost(host string) bool {
	host = strings.ToLower(strings.Trim(host, "[]"))
	return host == "localhost" ||
		host == "127.0.0.1" ||
		host == "::1" ||
		strings.HasSuffix(host, ".local")
}

func (s *Server) cloudflareConfig() CloudflareConfig {
	ttl, err := strconv.Atoi(strings.TrimSpace(s.store.Setting(cloudflareHTMLTTLKey)))
	if err != nil || ttl < 0 {
		ttl = cloudflareDefaultHTMLTTL
	}
	if ttl > 86400 {
		ttl = 86400
	}
	worker := normalizeCloudflareWorkerName(s.store.Setting(cloudflareWorkerNameKey))
	origin := normalizeCloudflareOrigin(s.store.Setting(cloudflareOriginURLKey))
	if origin == "" {
		origin = s.defaultCloudflareOriginURL()
	}
	route := normalizeCloudflareRoutePattern(s.store.Setting(cloudflareRoutePatternKey))
	if route == "" {
		route = s.defaultCloudflareRoutePattern()
	}
	return CloudflareConfig{
		AccountID:         strings.TrimSpace(s.store.Setting(cloudflareAccountIDKey)),
		APIToken:          strings.TrimSpace(s.store.Setting(cloudflareAPITokenKey)),
		WorkerName:        worker,
		ZoneID:            strings.TrimSpace(s.store.Setting(cloudflareZoneIDKey)),
		RoutePattern:      route,
		OriginURL:         origin,
		HTMLCacheTTL:      ttl,
		AutoSync:          s.store.Setting(cloudflareAutoSyncKey) == "1",
		OAuthClientID:     strings.TrimSpace(s.store.Setting(cloudflareOAuthIDKey)),
		OAuthClientSecret: strings.TrimSpace(s.store.Setting(cloudflareOAuthSecretKey)),
		OAuthAccessToken:  strings.TrimSpace(s.store.Setting(cloudflareOAuthAccessKey)),
		OAuthRefreshToken: strings.TrimSpace(s.store.Setting(cloudflareOAuthRefreshKey)),
		OAuthTokenType:    strings.TrimSpace(s.store.Setting(cloudflareOAuthTypeKey)),
		OAuthExpiresAt:    strings.TrimSpace(s.store.Setting(cloudflareOAuthExpiresKey)),
		AccountName:       strings.TrimSpace(s.store.Setting(cloudflareAccountNameKey)),
		ZoneName:          strings.TrimSpace(s.store.Setting(cloudflareZoneNameKey)),
		ConnectedAt:       strings.TrimSpace(s.store.Setting(cloudflareConnectedAtKey)),
	}
}

func (s *Server) cloudflareConfigForRequest(r *http.Request) CloudflareConfig {
	cfg := s.cloudflareConfig()
	s.applyCloudflareRequestDefaults(r, &cfg)
	return cfg
}

func (s *Server) cloudflareViewForRequest(r *http.Request) *CloudflareView {
	view := s.cloudflareView()
	s.applyCloudflareRequestDefaults(r, &view.Config)
	view.Status.WorkerName = view.Config.WorkerName
	view.Status.RoutePattern = view.Config.RoutePattern
	view.Status.Configured = view.Config.configured()
	view.Configured = view.Status.Configured
	return view
}

func (s *Server) applyCloudflareRequestDefaults(r *http.Request, cfg *CloudflareConfig) {
	if cfg == nil || r == nil {
		return
	}
	base := s.publicBaseURL(r)
	if strings.TrimSpace(cfg.OriginURL) == "" {
		cfg.OriginURL = cloudflareOriginFromBaseURL(base)
	}
	if strings.TrimSpace(cfg.RoutePattern) == "" {
		cfg.RoutePattern = cloudflareRoutePatternFromBaseURL(base)
	}
}

func readCloudflareStatus() *CloudflareStatus {
	st := &CloudflareStatus{
		Status:  "idle",
		Message: "暂无 Cloudflare 部署任务",
	}
	if data, err := os.ReadFile(cloudflareStatusPath()); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, st)
		if st.Status == "" {
			st.Status = "idle"
		}
		if st.Message == "" {
			st.Message = "暂无 Cloudflare 部署任务"
		}
	}
	if st.Status == "running" && cloudflareStatusStale(st) {
		st.Status = "failed"
		st.Step = "timeout"
		st.Message = "上一次部署任务长时间没有更新，可能已被中断。请检查 Token、域名和源站地址后重新部署。"
		writeCloudflareStatus(*st)
	}
	st.Running = st.Status == "running"
	return st
}

func cloudflareStatusStale(st *CloudflareStatus) bool {
	if st == nil || st.Status != "running" || strings.TrimSpace(st.UpdatedAt) == "" {
		return false
	}
	updatedAt, err := time.Parse(time.RFC3339, st.UpdatedAt)
	if err != nil {
		return false
	}
	return time.Since(updatedAt) > cloudflareStaleAfter
}

func writeCloudflareStatus(st CloudflareStatus) {
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	st.Running = st.Status == "running"
	_ = os.MkdirAll(filepath.Dir(cloudflareStatusPath()), 0o755)
	if data, err := json.MarshalIndent(st, "", "  "); err == nil {
		_ = os.WriteFile(cloudflareStatusPath(), append(data, '\n'), 0o644)
	}
}

func (s *Server) cloudflareView() *CloudflareView {
	cfg := s.cloudflareConfig()
	st := readCloudflareStatus()
	st.WorkerName = cfg.WorkerName
	st.RoutePattern = cfg.RoutePattern
	st.Configured = cfg.configured()
	st.TokenSet = cfg.tokenSet()
	st.AutoSync = cfg.AutoSync
	return &CloudflareView{
		Config:           cfg,
		Status:           st,
		TokenSet:         cfg.tokenSet(),
		OAuth:            cfg.oauthSet(),
		OAuthClientSet:   cfg.oauthClientSet(),
		Configured:       cfg.configured(),
		TokenTemplateURL: cloudflareAPITokenTemplateURL(),
	}
}

func cloudflareAPITokenTemplateURL() string {
	permissions := []map[string]string{
		{"key": "workers_scripts", "type": "edit"},
		{"key": "workers_routes", "type": "edit"},
		{"key": "cache", "type": "purge"},
		{"key": "zone", "type": "read"},
		{"key": "account_settings", "type": "read"},
	}
	raw, _ := json.Marshal(permissions)
	u := url.URL{Scheme: "https", Host: "dash.cloudflare.com", Path: "/profile/api-tokens"}
	q := u.Query()
	q.Set("name", "GCMS Cloudflare Deploy")
	q.Set("accountId", "*")
	q.Set("zoneId", "all")
	q.Set("permissionGroupKeys", string(raw))
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Server) saveCloudflareConfigFromRequest(r *http.Request) (CloudflareConfig, error) {
	cfg := s.cloudflareConfigForRequest(r)
	cfg.AccountID = strings.TrimSpace(r.FormValue("account_id"))
	if token := strings.TrimSpace(r.FormValue("api_token")); token != "" {
		cfg.APIToken = token
	}
	clearOAuthSecret := false
	if _, ok := r.Form["oauth_client_id"]; ok {
		nextID := strings.TrimSpace(r.FormValue("oauth_client_id"))
		clearOAuthSecret = nextID != cfg.OAuthClientID && strings.TrimSpace(r.FormValue("oauth_client_secret")) == ""
		cfg.OAuthClientID = nextID
	}
	if secret := strings.TrimSpace(r.FormValue("oauth_client_secret")); secret != "" {
		cfg.OAuthClientSecret = secret
	} else if clearOAuthSecret {
		cfg.OAuthClientSecret = ""
	}
	cfg.WorkerName = normalizeCloudflareWorkerName(r.FormValue("worker_name"))
	cfg.ZoneID = strings.TrimSpace(r.FormValue("zone_id"))
	if raw := strings.TrimSpace(r.FormValue("route_pattern")); raw != "" || r.FormValue("deploy") != "1" {
		cfg.RoutePattern = normalizeCloudflareRoutePattern(raw)
	}
	if raw := strings.TrimSpace(r.FormValue("origin_url")); raw != "" || r.FormValue("deploy") != "1" {
		cfg.OriginURL = normalizeCloudflareOrigin(raw)
	}
	cfg.AutoSync = r.FormValue("auto_sync") == "1"
	ttl, err := strconv.Atoi(strings.TrimSpace(r.FormValue("html_cache_ttl")))
	if err != nil {
		return cfg, errors.New("HTML 缓存时间必须是数字。")
	}
	if ttl < 0 || ttl > 86400 {
		return cfg, errors.New("HTML 缓存时间需要在 0 到 86400 秒之间。")
	}
	cfg.HTMLCacheTTL = ttl

	settings := map[string]string{
		cloudflareAccountIDKey:    cfg.AccountID,
		cloudflareWorkerNameKey:   cfg.WorkerName,
		cloudflareZoneIDKey:       cfg.ZoneID,
		cloudflareRoutePatternKey: cfg.RoutePattern,
		cloudflareOriginURLKey:    cfg.OriginURL,
		cloudflareHTMLTTLKey:      strconv.Itoa(cfg.HTMLCacheTTL),
		cloudflareAutoSyncKey:     boolSetting(cfg.AutoSync),
		cloudflareOAuthIDKey:      cfg.OAuthClientID,
	}
	if strings.TrimSpace(r.FormValue("api_token")) != "" {
		settings[cloudflareAPITokenKey] = cfg.APIToken
	}
	if strings.TrimSpace(r.FormValue("oauth_client_secret")) != "" {
		settings[cloudflareOAuthSecretKey] = cfg.OAuthClientSecret
	} else if clearOAuthSecret {
		settings[cloudflareOAuthSecretKey] = ""
	}
	for k, v := range settings {
		if err := s.store.SetSetting(k, v); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func boolSetting(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

func (s *Server) adminSaveCloudflare(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	cfg, err := s.saveCloudflareConfigFromRequest(r)
	if err != nil {
		s.showSettings(w, r, "cloudflare", "", err.Error())
		return
	}
	if r.FormValue("deploy") == "1" {
		if err := cfg.validateDeploy(); err != nil {
			s.showSettings(w, r, "cloudflare", "", err.Error())
			return
		}
		if err := s.queueCloudflareDeploy(cfg); err != nil {
			s.showSettings(w, r, "cloudflare", "", err.Error())
			return
		}
		s.showSettings(w, r, "cloudflare", "Cloudflare Token 已保存，部署任务已启动。", "")
		return
	}
	s.showSettings(w, r, "cloudflare", "Cloudflare 部署配置已保存。", "")
}

func (s *Server) queueCloudflareDeploy(cfg CloudflareConfig) error {
	st := readCloudflareStatus()
	if st.Running {
		return errors.New("已有 Cloudflare 部署任务正在运行。")
	}
	writeCloudflareStatus(CloudflareStatus{
		Status:       "running",
		Step:         "queued",
		Message:      "部署任务已启动，正在连接 Cloudflare。",
		WorkerName:   cfg.WorkerName,
		RoutePattern: cfg.RoutePattern,
		Configured:   cfg.configured(),
		TokenSet:     cfg.tokenSet(),
		AutoSync:     cfg.AutoSync,
	})
	go func() {
		defer func() {
			if v := recover(); v != nil {
				writeCloudflareStatus(cloudflareStatusFailed(cfg, "failed", fmt.Sprintf("Cloudflare 部署任务异常中断：%v", v)))
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), cloudflareAPITimeout)
		defer cancel()
		if err := s.deployCloudflare(ctx, cfg); err != nil {
			writeCloudflareStatus(cloudflareStatusFailed(cfg, "failed", err.Error()))
		}
	}()
	return nil
}

func (s *Server) saveCloudflareOAuthClientFromRequest(r *http.Request) (CloudflareConfig, error) {
	cfg := s.cloudflareConfig()
	if err := r.ParseForm(); err != nil {
		return cfg, err
	}
	clearOAuthSecret := false
	if _, ok := r.Form["oauth_client_id"]; ok {
		nextID := strings.TrimSpace(r.FormValue("oauth_client_id"))
		clearOAuthSecret = nextID != cfg.OAuthClientID && strings.TrimSpace(r.FormValue("oauth_client_secret")) == ""
		cfg.OAuthClientID = nextID
	}
	if secret := strings.TrimSpace(r.FormValue("oauth_client_secret")); secret != "" {
		cfg.OAuthClientSecret = secret
	} else if clearOAuthSecret {
		cfg.OAuthClientSecret = ""
	}
	settings := map[string]string{
		cloudflareOAuthIDKey: cfg.OAuthClientID,
	}
	if strings.TrimSpace(r.FormValue("oauth_client_secret")) != "" {
		settings[cloudflareOAuthSecretKey] = cfg.OAuthClientSecret
	} else if clearOAuthSecret {
		settings[cloudflareOAuthSecretKey] = ""
	}
	for k, v := range settings {
		if err := s.store.SetSetting(k, v); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

func (s *Server) adminStartCloudflareConnect(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	cfg, err := s.saveCloudflareOAuthClientFromRequest(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !cfg.oauthClientSet() {
		s.showSettings(w, r, "cloudflare", "", "请先填写并保存 Cloudflare OAuth Client ID 和 Client Secret。")
		return
	}
	state := randToken()
	_ = s.store.SetSetting(cloudflareConnectStateKey, fmt.Sprintf("%s:%d", state, time.Now().Unix()))
	if r.FormValue("auto_deploy") == "1" {
		_ = s.store.SetSetting(cloudflareConnectNextKey, "deploy")
	} else {
		_ = s.store.SetSetting(cloudflareConnectNextKey, "")
	}
	authURL, err := cloudflareOAuthAuthorizationURL(cfg.OAuthClientID, s.absForRequest(r, cloudflareCallbackPath), state)
	if err != nil {
		s.showSettings(w, r, "cloudflare", "", "生成 Cloudflare 授权地址失败："+err.Error())
		return
	}
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

func (s *Server) adminCloudflareCallback(w http.ResponseWriter, r *http.Request) {
	if errCode := strings.TrimSpace(r.URL.Query().Get("error")); errCode != "" {
		msg := strings.TrimSpace(r.URL.Query().Get("error_description"))
		if msg == "" {
			msg = errCode
		}
		s.showSettings(w, r, "cloudflare", "", "Cloudflare 授权失败："+msg)
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if !s.validCloudflareConnectState(state) {
		s.showSettings(w, r, "cloudflare", "", "Cloudflare 授权状态已过期或不匹配，请重新连接。")
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		s.showSettings(w, r, "cloudflare", "", "Cloudflare 没有返回授权 code。")
		return
	}
	cfg := s.cloudflareConfig()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	token, target, err := s.exchangeCloudflareOAuthCode(ctx, cfg, code, s.absForRequest(r, cloudflareCallbackPath))
	if err != nil {
		s.showSettings(w, r, "cloudflare", "", "Cloudflare 授权交换失败："+err.Error())
		return
	}
	if err := s.saveCloudflareOAuthResult(cfg, token, target); err != nil {
		s.serverError(w, err)
		return
	}
	_ = s.store.SetSetting(cloudflareConnectStateKey, "")
	flash := "Cloudflare 已连接，部署配置已自动填写。"
	if s.store.Setting(cloudflareConnectNextKey) == "deploy" {
		nextCfg := s.cloudflareConfig()
		if err := nextCfg.validateDeploy(); err != nil {
			s.showSettings(w, r, "cloudflare", flash, err.Error())
			return
		}
		writeCloudflareStatus(CloudflareStatus{
			Status:       "running",
			Step:         "queued",
			Message:      "Cloudflare 已连接，正在自动部署 Worker。",
			WorkerName:   nextCfg.WorkerName,
			RoutePattern: nextCfg.RoutePattern,
			Configured:   nextCfg.configured(),
			TokenSet:     nextCfg.tokenSet(),
			AutoSync:     nextCfg.AutoSync,
		})
		go func() {
			defer func() {
				if v := recover(); v != nil {
					writeCloudflareStatus(cloudflareStatusFailed(nextCfg, "failed", fmt.Sprintf("Cloudflare 部署任务异常中断：%v", v)))
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), cloudflareAPITimeout)
			defer cancel()
			if err := s.deployCloudflare(ctx, nextCfg); err != nil {
				writeCloudflareStatus(cloudflareStatusFailed(nextCfg, "failed", err.Error()))
			}
		}()
		flash = "Cloudflare 已连接，部署任务已启动。"
	}
	_ = s.store.SetSetting(cloudflareConnectNextKey, "")
	s.showSettings(w, r, "cloudflare", flash, "")
}

func (s *Server) validCloudflareConnectState(state string) bool {
	if state == "" {
		return false
	}
	raw := strings.TrimSpace(s.store.Setting(cloudflareConnectStateKey))
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(state), []byte(parts[0])) != 1 {
		return false
	}
	created, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(created, 0)) <= cloudflareConnectTTL
}

func cloudflareOAuthAuthorizationURL(clientID, callbackURL, state string) (string, error) {
	u, err := url.Parse(cloudflareOAuthAuthorizeURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", strings.TrimSpace(clientID))
	q.Set("redirect_uri", callbackURL)
	q.Set("state", state)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *Server) exchangeCloudflareOAuthCode(ctx context.Context, cfg CloudflareConfig, code, callbackURL string) (cloudflareOAuthToken, cloudflareOAuthTarget, error) {
	token, err := cloudflareOAuthTokenRequest(ctx, cfg, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {callbackURL},
	})
	if err != nil {
		return token, cloudflareOAuthTarget{}, err
	}
	if strings.TrimSpace(token.AccessToken) == "" || strings.TrimSpace(token.RefreshToken) == "" {
		return token, cloudflareOAuthTarget{}, errors.New("Cloudflare 没有返回完整的 OAuth token。")
	}
	target, err := discoverCloudflareTarget(ctx, token.AccessToken, cfg.RoutePattern)
	if err != nil {
		return token, target, err
	}
	return token, target, nil
}

func (s *Server) saveCloudflareOAuthResult(cfg CloudflareConfig, token cloudflareOAuthToken, target cloudflareOAuthTarget) error {
	if target.AccountID != "" {
		cfg.AccountID = strings.TrimSpace(target.AccountID)
	}
	if target.AccountName != "" {
		cfg.AccountName = strings.TrimSpace(target.AccountName)
	}
	if target.ZoneID != "" {
		cfg.ZoneID = strings.TrimSpace(target.ZoneID)
	}
	if target.ZoneName != "" {
		cfg.ZoneName = strings.TrimSpace(target.ZoneName)
	}
	if cfg.HTMLCacheTTL <= 0 {
		cfg.HTMLCacheTTL = cloudflareDefaultHTMLTTL
	}
	cfg.AutoSync = true
	cfg.OAuthAccessToken = strings.TrimSpace(token.AccessToken)
	cfg.OAuthRefreshToken = strings.TrimSpace(token.RefreshToken)
	cfg.OAuthTokenType = strings.TrimSpace(token.TokenType)
	if cfg.OAuthTokenType == "" {
		cfg.OAuthTokenType = "Bearer"
	}
	cfg.OAuthExpiresAt = cloudflareTokenExpiry(token.ExpiresIn)
	settings := map[string]string{
		cloudflareAccountIDKey:    cfg.AccountID,
		cloudflareAccountNameKey:  cfg.AccountName,
		cloudflareZoneIDKey:       cfg.ZoneID,
		cloudflareZoneNameKey:     cfg.ZoneName,
		cloudflareWorkerNameKey:   cfg.WorkerName,
		cloudflareRoutePatternKey: cfg.RoutePattern,
		cloudflareOriginURLKey:    cfg.OriginURL,
		cloudflareHTMLTTLKey:      strconv.Itoa(cfg.HTMLCacheTTL),
		cloudflareAutoSyncKey:     boolSetting(cfg.AutoSync),
		cloudflareOAuthAccessKey:  cfg.OAuthAccessToken,
		cloudflareOAuthRefreshKey: cfg.OAuthRefreshToken,
		cloudflareOAuthTypeKey:    cfg.OAuthTokenType,
		cloudflareOAuthExpiresKey: cfg.OAuthExpiresAt,
		cloudflareConnectedAtKey:  time.Now().UTC().Format(time.RFC3339),
	}
	for k, v := range settings {
		if err := s.store.SetSetting(k, v); err != nil {
			return err
		}
	}
	return nil
}

func cloudflareTokenExpiry(expiresIn int) string {
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return time.Now().UTC().Add(time.Duration(expiresIn) * time.Second).Format(time.RFC3339)
}

func cloudflareOAuthTokenRequest(ctx context.Context, cfg CloudflareConfig, form url.Values) (cloudflareOAuthToken, error) {
	var token cloudflareOAuthToken
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cloudflareOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return token, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	secret := strings.TrimSpace(cfg.OAuthClientSecret)
	client := strings.TrimSpace(cfg.OAuthClientID)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(client+":"+secret)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return token, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return token, err
	}
	if resp.StatusCode >= 400 {
		return token, fmt.Errorf("Cloudflare OAuth 返回 HTTP %d：%s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if err := json.Unmarshal(data, &token); err != nil {
		return token, err
	}
	return token, nil
}

func (s *Server) cloudflareAuthorizedConfig(ctx context.Context, cfg CloudflareConfig) (CloudflareConfig, error) {
	if strings.TrimSpace(cfg.APIToken) != "" {
		return cfg, nil
	}
	if !cfg.oauthSet() {
		return cfg, errors.New("请先连接 Cloudflare，或在高级设置里填写 API Token。")
	}
	if strings.TrimSpace(cfg.OAuthAccessToken) != "" && !cloudflareOAuthExpired(cfg.OAuthExpiresAt) {
		cfg.APIToken = cfg.OAuthAccessToken
		return cfg, nil
	}
	token, err := cloudflareOAuthTokenRequest(ctx, cfg, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {cfg.OAuthRefreshToken},
	})
	if err != nil {
		return cfg, err
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return cfg, errors.New("Cloudflare 没有返回新的 Access Token。")
	}
	if strings.TrimSpace(token.RefreshToken) == "" {
		token.RefreshToken = cfg.OAuthRefreshToken
	}
	if strings.TrimSpace(token.TokenType) == "" {
		token.TokenType = "Bearer"
	}
	cfg.OAuthAccessToken = strings.TrimSpace(token.AccessToken)
	cfg.OAuthRefreshToken = strings.TrimSpace(token.RefreshToken)
	cfg.OAuthTokenType = strings.TrimSpace(token.TokenType)
	cfg.OAuthExpiresAt = cloudflareTokenExpiry(token.ExpiresIn)
	cfg.APIToken = cfg.OAuthAccessToken
	if err := s.saveCloudflareOAuthToken(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (s *Server) prepareCloudflareAPIConfig(ctx context.Context, cfg CloudflareConfig) (CloudflareConfig, error) {
	cfg, err := s.cloudflareAuthorizedConfig(ctx, cfg)
	if err != nil {
		return cfg, err
	}
	if strings.TrimSpace(cfg.AccountID) == "" || (strings.TrimSpace(cfg.ZoneID) == "" && strings.TrimSpace(cfg.RoutePattern) != "") {
		target, _ := discoverCloudflareTarget(ctx, cfg.APIToken, cfg.RoutePattern)
		if cfg.AccountID == "" && target.AccountID != "" {
			cfg.AccountID = target.AccountID
			cfg.AccountName = target.AccountName
		}
		if cfg.ZoneID == "" && target.ZoneID != "" {
			cfg.ZoneID = target.ZoneID
			cfg.ZoneName = target.ZoneName
			if cfg.AccountID == "" {
				cfg.AccountID = target.AccountID
				cfg.AccountName = target.AccountName
			}
		}
		_ = s.saveCloudflareDetectedTarget(cfg)
	}
	if strings.TrimSpace(cfg.AccountID) == "" {
		return cfg, errors.New("无法自动识别 Cloudflare Account ID。请确认 Token 权限包含 Account Settings Read，或在高级设置里手动填写 Account ID。")
	}
	if strings.TrimSpace(cfg.RoutePattern) != "" && strings.TrimSpace(cfg.ZoneID) == "" {
		return cfg, errors.New("无法自动识别 Worker 路由对应的 Zone ID。请确认 Token 权限包含 Zone Read，或在高级设置里手动填写 Zone ID。")
	}
	return cfg, nil
}

func (s *Server) saveCloudflareDetectedTarget(cfg CloudflareConfig) error {
	settings := map[string]string{
		cloudflareAccountIDKey:   strings.TrimSpace(cfg.AccountID),
		cloudflareAccountNameKey: strings.TrimSpace(cfg.AccountName),
		cloudflareZoneIDKey:      strings.TrimSpace(cfg.ZoneID),
		cloudflareZoneNameKey:    strings.TrimSpace(cfg.ZoneName),
	}
	for k, v := range settings {
		if strings.TrimSpace(v) == "" {
			continue
		}
		if err := s.store.SetSetting(k, v); err != nil {
			return err
		}
	}
	return nil
}

func cloudflareOAuthExpired(expiresAt string) bool {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(expiresAt))
	if err != nil {
		return true
	}
	return time.Until(t) <= cloudflareOAuthSkew
}

func (s *Server) saveCloudflareOAuthToken(cfg CloudflareConfig) error {
	settings := map[string]string{
		cloudflareOAuthAccessKey:  cfg.OAuthAccessToken,
		cloudflareOAuthRefreshKey: cfg.OAuthRefreshToken,
		cloudflareOAuthTypeKey:    cfg.OAuthTokenType,
		cloudflareOAuthExpiresKey: cfg.OAuthExpiresAt,
	}
	for k, v := range settings {
		if err := s.store.SetSetting(k, v); err != nil {
			return err
		}
	}
	return nil
}

func discoverCloudflareTarget(ctx context.Context, token, routePattern string) (cloudflareOAuthTarget, error) {
	var target cloudflareOAuthTarget
	zones, err := listCloudflareZones(ctx, token)
	if err == nil {
		host := cloudflareRouteHost(routePattern)
		if zone := matchCloudflareZone(host, zones); zone.ID != "" {
			target.ZoneID = zone.ID
			target.ZoneName = zone.Name
			target.AccountID = zone.Account.ID
			target.AccountName = zone.Account.Name
			return target, nil
		}
		if host == "" && len(zones) == 1 {
			zone := zones[0]
			target.ZoneID = zone.ID
			target.ZoneName = zone.Name
			target.AccountID = zone.Account.ID
			target.AccountName = zone.Account.Name
			return target, nil
		}
	}
	accounts, err := listCloudflareAccounts(ctx, token)
	if err != nil {
		return target, nil
	}
	if len(accounts) == 1 {
		target.AccountID = accounts[0].ID
		target.AccountName = accounts[0].Name
	}
	return target, nil
}

func listCloudflareZones(ctx context.Context, token string) ([]cloudflareZone, error) {
	result, err := cloudflareAPIRequest(ctx, token, http.MethodGet, "/zones?per_page=50", nil, "")
	if err != nil {
		return nil, err
	}
	var zones []cloudflareZone
	if len(result) > 0 {
		if err := json.Unmarshal(result, &zones); err != nil {
			return nil, err
		}
	}
	return zones, nil
}

func listCloudflareAccounts(ctx context.Context, token string) ([]cloudflareAccount, error) {
	result, err := cloudflareAPIRequest(ctx, token, http.MethodGet, "/accounts?per_page=50", nil, "")
	if err != nil {
		return nil, err
	}
	var accounts []cloudflareAccount
	if len(result) > 0 {
		if err := json.Unmarshal(result, &accounts); err != nil {
			return nil, err
		}
	}
	return accounts, nil
}

func cloudflareRouteHost(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return ""
	}
	if strings.Contains(pattern, "://") {
		if u, err := url.Parse(pattern); err == nil {
			pattern = u.Host
		}
	}
	if i := strings.Index(pattern, "/"); i >= 0 {
		pattern = pattern[:i]
	}
	pattern = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(pattern)), "*.")
	if i := strings.LastIndex(pattern, ":"); i > -1 {
		pattern = pattern[:i]
	}
	return pattern
}

func matchCloudflareZone(host string, zones []cloudflareZone) cloudflareZone {
	host = strings.TrimSpace(strings.ToLower(host))
	var best cloudflareZone
	for _, zone := range zones {
		name := strings.ToLower(strings.TrimSpace(zone.Name))
		if name == "" {
			continue
		}
		if host == name || strings.HasSuffix(host, "."+name) || (host == "" && best.ID == "") {
			if len(name) > len(best.Name) {
				best = zone
			}
		}
	}
	return best
}

func (s *Server) adminCloudflareStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.cloudflareView().Status)
}

func (s *Server) adminStartCloudflareDeploy(w http.ResponseWriter, r *http.Request) {
	jsonReq := wantsJSON(r)
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	cfg := s.cloudflareConfigForRequest(r)
	if err := cfg.validateDeploy(); err != nil {
		if jsonReq {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": err.Error(), "status": readCloudflareStatus()})
			return
		}
		s.showSettings(w, r, "cloudflare", "", err.Error())
		return
	}
	if err := s.queueCloudflareDeploy(cfg); err != nil {
		if jsonReq {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "message": err.Error(), "status": readCloudflareStatus()})
			return
		}
		s.showSettings(w, r, "cloudflare", "", err.Error())
		return
	}
	if jsonReq {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "Cloudflare 部署任务已启动。", "status": readCloudflareStatus()})
		return
	}
	s.showSettings(w, r, "cloudflare", "Cloudflare 部署任务已启动，请稍后刷新状态。", "")
}

func (s *Server) adminCloudflarePurge(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	cfg := s.cloudflareConfig()
	if !cfg.tokenSet() {
		s.showSettings(w, r, "cloudflare", "", "清除缓存需要先粘贴 Cloudflare API Token，或完成 OAuth 授权。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.purgeCloudflareCache(ctx, cfg, "手动清除 Cloudflare 缓存完成。"); err != nil {
		s.showSettings(w, r, "cloudflare", "", err.Error())
		return
	}
	s.showSettings(w, r, "cloudflare", "Cloudflare 缓存已清除。", "")
}

func (s *Server) deployCloudflare(ctx context.Context, cfg CloudflareConfig) error {
	setStep := func(step, msg string) {
		writeCloudflareStatus(CloudflareStatus{
			Status:       "running",
			Step:         step,
			Message:      msg,
			WorkerName:   cfg.WorkerName,
			RoutePattern: cfg.RoutePattern,
			Configured:   cfg.configured(),
			TokenSet:     cfg.tokenSet(),
			AutoSync:     cfg.AutoSync,
		})
	}
	lastPurgeAt := ""
	setStep("detect", "正在识别 Cloudflare 账号和域名。")
	var err error
	cfg, err = s.prepareCloudflareAPIConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("获取 Cloudflare 授权失败：%w", err)
	}
	setStep("worker", "正在上传 Worker 脚本。")
	if err := uploadCloudflareWorker(ctx, cfg); err != nil {
		return fmt.Errorf("上传 Worker 失败：%w", err)
	}
	if cfg.ZoneID != "" && cfg.RoutePattern != "" {
		setStep("route", "正在绑定 Worker 路由。")
		if err := ensureCloudflareRoute(ctx, cfg); err != nil {
			return fmt.Errorf("绑定 Worker 路由失败：%w", err)
		}
		setStep("purge", "正在清理 Cloudflare 缓存。")
		if err := purgeCloudflareEverything(ctx, cfg); err != nil {
			return fmt.Errorf("清理 Cloudflare 缓存失败：%w", err)
		}
		lastPurgeAt = time.Now().UTC().Format(time.RFC3339)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	writeCloudflareStatus(CloudflareStatus{
		Status:       "success",
		Step:         "done",
		Message:      "Cloudflare Worker 已部署；内容仍由当前 gcms 源站渲染，Cloudflare 负责入口与缓存。",
		WorkerName:   cfg.WorkerName,
		RoutePattern: cfg.RoutePattern,
		UpdatedAt:    now,
		LastDeployAt: now,
		LastPurgeAt:  lastPurgeAt,
		Configured:   cfg.configured(),
		TokenSet:     cfg.tokenSet(),
		AutoSync:     cfg.AutoSync,
	})
	return nil
}

func (s *Server) scheduleCloudflareSync(reason string) {
	cfg := s.cloudflareConfig()
	if !cfg.AutoSync || !cfg.tokenSet() {
		return
	}
	if strings.TrimSpace(reason) == "" {
		reason = "内容已更新，Cloudflare 缓存已自动清除。"
	}
	s.cloudflareMu.Lock()
	if s.cloudflareTimer != nil {
		s.cloudflareTimer.Stop()
	}
	msg := reason
	s.cloudflareTimer = time.AfterFunc(25*time.Second, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.purgeCloudflareCache(ctx, cfg, msg)
	})
	s.cloudflareMu.Unlock()
}

func (s *Server) purgeCloudflareCache(ctx context.Context, cfg CloudflareConfig, message string) error {
	var err error
	cfg, err = s.prepareCloudflareAPIConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("获取 Cloudflare 授权失败：%w", err)
	}
	if strings.TrimSpace(cfg.ZoneID) == "" {
		return errors.New("清理 Cloudflare 缓存需要先识别或填写 Zone ID。")
	}
	if err := purgeCloudflareEverything(ctx, cfg); err != nil {
		return fmt.Errorf("清理 Cloudflare 缓存失败：%w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	writeCloudflareStatus(CloudflareStatus{
		Status:       "success",
		Step:         "purge",
		Message:      message,
		WorkerName:   cfg.WorkerName,
		RoutePattern: cfg.RoutePattern,
		LastPurgeAt:  now,
		Configured:   cfg.configured(),
		TokenSet:     cfg.tokenSet(),
		AutoSync:     cfg.AutoSync,
	})
	return nil
}

func uploadCloudflareWorker(ctx context.Context, cfg CloudflareConfig) error {
	metadata := map[string]any{
		"main_module":        "worker.js",
		"compatibility_date": "2025-01-01",
		"bindings": []map[string]string{
			{"type": "plain_text", "name": "GCMS_ORIGIN", "text": cfg.OriginURL},
			{"type": "plain_text", "name": "HTML_CACHE_TTL", "text": strconv.Itoa(cfg.HTMLCacheTTL)},
		},
	}
	metadataJSON, _ := json.Marshal(metadata)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := writeMultipartField(mw, "metadata", "", "application/json", metadataJSON); err != nil {
		return err
	}
	if err := writeMultipartField(mw, "worker.js", "worker.js", "application/javascript+module", []byte(cloudflareWorkerScript())); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	path := fmt.Sprintf("/accounts/%s/workers/scripts/%s", url.PathEscape(cfg.AccountID), url.PathEscape(cfg.WorkerName))
	_, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodPut, path, &body, mw.FormDataContentType())
	return err
}

func writeMultipartField(mw *multipart.Writer, name, filename, contentType string, data []byte) error {
	h := make(textproto.MIMEHeader)
	if filename != "" {
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, name, filename))
	} else {
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, name))
	}
	h.Set("Content-Type", contentType)
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}

func ensureCloudflareRoute(ctx context.Context, cfg CloudflareConfig) error {
	routes, err := listCloudflareRoutes(ctx, cfg)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"pattern": cfg.RoutePattern, "script": cfg.WorkerName})
	for _, rt := range routes {
		if rt.Pattern == cfg.RoutePattern {
			path := fmt.Sprintf("/zones/%s/workers/routes/%s", url.PathEscape(cfg.ZoneID), url.PathEscape(rt.ID))
			_, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodPut, path, bytes.NewReader(body), "application/json")
			return err
		}
	}
	path := fmt.Sprintf("/zones/%s/workers/routes", url.PathEscape(cfg.ZoneID))
	_, err = cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodPost, path, bytes.NewReader(body), "application/json")
	return err
}

func listCloudflareRoutes(ctx context.Context, cfg CloudflareConfig) ([]cloudflareRoute, error) {
	path := fmt.Sprintf("/zones/%s/workers/routes", url.PathEscape(cfg.ZoneID))
	result, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, err
	}
	var routes []cloudflareRoute
	if len(result) > 0 {
		_ = json.Unmarshal(result, &routes)
	}
	return routes, nil
}

func purgeCloudflareEverything(ctx context.Context, cfg CloudflareConfig) error {
	if strings.TrimSpace(cfg.ZoneID) == "" {
		return nil
	}
	body := strings.NewReader(`{"purge_everything":true}`)
	path := fmt.Sprintf("/zones/%s/purge_cache", url.PathEscape(cfg.ZoneID))
	_, err := cloudflareAPIRequest(ctx, cfg.APIToken, http.MethodPost, path, body, "application/json")
	return err
}

func cloudflareAPIRequest(ctx context.Context, token, method, path string, body io.Reader, contentType string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, method, "https://api.cloudflare.com/client/v4"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	var envelope cloudflareAPIResponse
	if err := json.Unmarshal(data, &envelope); err != nil {
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("Cloudflare 返回 HTTP %d：%s", resp.StatusCode, strings.TrimSpace(string(data)))
		}
		return data, nil
	}
	if resp.StatusCode >= 400 || !envelope.Success {
		return nil, fmt.Errorf("Cloudflare 返回错误：%s", cloudflareErrorMessage(envelope.Errors, resp.StatusCode))
	}
	return envelope.Result, nil
}

func cloudflareErrorMessage(errs []cloudflareErr, status int) string {
	if len(errs) == 0 {
		return fmt.Sprintf("HTTP %d", status)
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		if e.Message == "" {
			continue
		}
		if e.Code != 0 {
			parts = append(parts, fmt.Sprintf("%d %s", e.Code, e.Message))
		} else {
			parts = append(parts, e.Message)
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("HTTP %d", status)
	}
	return strings.Join(parts, "；")
}

func cloudflareWorkerScript() string {
	return `const BLOCKED_PREFIXES = ["/admin", "/api/admin", "/preview"];

function blocked(pathname) {
  return BLOCKED_PREFIXES.some((prefix) => pathname === prefix || pathname.startsWith(prefix + "/"));
}

function cacheable(request, response, ttl) {
  if (request.method !== "GET" || ttl <= 0 || response.status !== 200) return false;
  if (response.headers.has("set-cookie")) return false;
  const type = response.headers.get("content-type") || "";
  return type.includes("text/html") || type.includes("application/xml") || type.includes("application/rss+xml") || type.includes("text/plain");
}

export default {
  async fetch(request, env, ctx) {
    const incoming = new URL(request.url);
    if (blocked(incoming.pathname)) {
      return new Response("Not found", { status: 404 });
    }

    const originBase = new URL(env.GCMS_ORIGIN);
    const originURL = new URL(request.url);
    originURL.protocol = originBase.protocol;
    originURL.hostname = originBase.hostname;
    originURL.port = originBase.port;

    const ttl = Number.parseInt(env.HTML_CACHE_TTL || "0", 10) || 0;
    if (request.method === "GET" && ttl > 0) {
      const cacheKey = new Request(request.url, request);
      const hit = await caches.default.match(cacheKey);
      if (hit) return hit;
    }

    const headers = new Headers(request.headers);
    headers.set("X-Forwarded-Host", incoming.host);
    headers.set("X-Forwarded-Proto", "https");
    headers.set("X-GCMS-Cloudflare", "1");

    const init = {
      method: request.method,
      headers,
      redirect: "manual",
    };
    if (request.method !== "GET" && request.method !== "HEAD") {
      init.body = request.body;
    }

    const response = await fetch(originURL.toString(), init);
    const out = new Response(response.body, response);
    out.headers.set("X-GCMS-Edge", "cloudflare-worker");

    if (cacheable(request, out, ttl)) {
      const cacheKey = new Request(request.url, request);
      const cached = new Response(out.clone().body, out);
      cached.headers.set("Cache-Control", "public, max-age=" + ttl);
      ctx.waitUntil(caches.default.put(cacheKey, cached));
    }
    return out;
  },
};
`
}
