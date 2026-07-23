package web

import (
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// servePlatformControlSitePreviewURL 为 Pilot 创建无需 GCMS 后台登录的短时整站预览。
// 平台密钥只负责签发，实际浏览由 URL 中的站点级 HMAC 票据鉴权；Pilot 不保存后台
// 密码，也不需要 SSH 连接。
func (s *Server) servePlatformControlSitePreviewURL(w http.ResponseWriter, r *http.Request, siteID int64) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST。")
		return
	}
	key, ok := s.requirePlatformControlKey(w, r, apiScopeControlRead)
	if !ok || !controlSiteMembershipAllowed(w, key, siteID) {
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
	token, err := rt.server.signSitePreviewToken(siteID, expiresAt)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "preview_sign_failed", "无法创建私有预览链接。")
		return
	}
	previewPath := "/preview/sites/" + strconv.FormatInt(siteID, 10) + "/site/" + url.PathEscape(token) + "/"
	_ = s.platform.CreatePlatformAutomationLog(key.ID, siteID, "site_preview_create", "site", siteID, "创建短时私有整站预览")
	writeJSON(w, http.StatusCreated, apiPreviewURLResponse{
		PreviewURL: s.absForPlatformRequest(r, previewPath),
		ExpiresAt:  expiresAt.UTC().Format(time.RFC3339),
		TTLSeconds: int64(sitePreviewTTL.Seconds()),
	})
}
