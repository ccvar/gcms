package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type controlSitePreviewInput struct {
	ThemeID string `json:"theme_id,omitempty"`
}

func decodeOptionalControlSitePreviewInput(w http.ResponseWriter, r *http.Request, in *controlSitePreviewInput) bool {
	if r.Body == nil || r.ContentLength == 0 {
		return true
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(in); err != nil {
		if err == io.EOF {
			return true
		}
		apiError(w, http.StatusBadRequest, "bad_json", "JSON 请求体无效："+err.Error())
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		apiError(w, http.StatusBadRequest, "bad_json", "JSON 请求体只能包含一个对象。")
		return false
	}
	return true
}

// servePlatformControlSitePreviewURL 为 Pilot 创建无需 GCMS 后台登录的短时整站预览。
// 平台密钥只负责签发，实际浏览由 URL 中的站点级 HMAC 票据鉴权；Pilot 不保存后台
// 密码，也不需要 SSH 连接。
func (s *Server) servePlatformControlSitePreviewURL(w http.ResponseWriter, r *http.Request, siteID int64) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST。")
		return
	}
	var in controlSitePreviewInput
	if !decodeOptionalControlSitePreviewInput(w, r, &in) {
		return
	}
	in.ThemeID = strings.TrimSpace(in.ThemeID)
	requiredScope := apiScopeControlRead
	if in.ThemeID != "" {
		requiredScope = apiScopeThemesRead
	}
	key, ok := s.requirePlatformControlKey(w, r, requiredScope)
	if !ok || !controlSiteMembershipAllowed(w, key, siteID) {
		return
	}
	if in.ThemeID != "" && !validTheme(in.ThemeID) {
		apiError(w, http.StatusBadRequest, "invalid_theme", "请选择有效的外观主题。")
		return
	}
	allowed, err := s.platform.PlatformKeyCanAccessSite(key, siteID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "site_access_check_failed", "无法检查站点预览权限。")
		return
	}
	if !allowed {
		apiError(w, http.StatusForbidden, "site_forbidden", "站点已关闭，或未开启平台自动化，暂时不能预览。")
		return
	}
	pool := s.runtimePool()
	rt, found := pool.runtimeByID(siteID)
	if !found || rt == nil || rt.server == nil || rt.Site == nil {
		apiError(w, http.StatusNotFound, "site_not_found", "站点运行时不存在，请刷新后重试。")
		return
	}
	expiresAt := time.Now().Add(sitePreviewTTL)
	token, err := rt.server.signSiteThemePreviewToken(siteID, in.ThemeID, expiresAt)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "preview_sign_failed", "无法创建私有预览链接。")
		return
	}
	previewPath := "/preview/sites/" + strconv.FormatInt(siteID, 10) + "/site/" + url.PathEscape(token) + "/"
	logMessage := "创建短时私有整站预览"
	if in.ThemeID != "" {
		logMessage = "创建候选主题 " + in.ThemeID + " 的短时私有整站预览"
	}
	_ = s.platform.CreatePlatformAutomationLog(key.ID, siteID, "site_preview_create", "site", siteID, logMessage)
	writeJSON(w, http.StatusCreated, apiPreviewURLResponse{
		PreviewURL:   s.absForPlatformRequest(r, previewPath),
		ExpiresAt:    expiresAt.UTC().Format(time.RFC3339),
		TTLSeconds:   int64(sitePreviewTTL.Seconds()),
		ThemeID:      in.ThemeID,
		CurrentTheme: controlCurrentTheme(rt.Store),
	})
}
