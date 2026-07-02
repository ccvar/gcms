package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"cms.ccvar.com/internal/platform"
)

const (
	googleOAuthAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleOAuthTokenURL    = "https://oauth2.googleapis.com/token"
	googleOAuthUserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"

	googleOAuthClientIDKey     = "google.oauth.client_id"
	googleOAuthClientSecretKey = "google.oauth.client_secret"
	googleOAuthRedirectURLKey  = "google.oauth.redirect_url"
)

type googleOAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
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
		return append(base, "https://www.googleapis.com/auth/analytics.readonly")
	case platform.GoogleServiceSearchConsole:
		return append(base, "https://www.googleapis.com/auth/webmasters.readonly")
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
	default:
		return "Google"
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
	if err := s.platform.DeleteGoogleAccount(service, googleAccountID); err != nil {
		s.serverError(w, err)
		return
	}
	msg := "Google 授权账号已解除绑定。"
	if jsonReq {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
		return
	}
	s.flashGoogleOAuth(r, msg)
	http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
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
	err = s.platform.UpsertGoogleAccount(&platform.GoogleAccount{
		Service:         service,
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
	label := user.Email
	if label == "" {
		label = user.Name
	}
	if label == "" {
		label = user.Sub
	}
	s.flashGoogleOAuth(r, fmt.Sprintf("%s 已授权：%s", googleServiceLabel(service), label))
	http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
}

func (s *Server) flashGoogleOAuth(r *http.Request, msg string) {
	s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: strings.TrimSpace(msg)})
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
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
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
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
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
