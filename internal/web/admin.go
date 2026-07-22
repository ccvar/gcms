package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"cms.ccvar.com/internal/backup"
	"cms.ccvar.com/internal/i18n"
	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/seo"
	"cms.ccvar.com/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// ---------- 会话 ----------

const (
	cookieName               = "ccvar_sess"
	adminLangCookie          = "gcms_admin_lang"
	localeCatalogsSettingKey = "locale_catalogs"
)

type session struct {
	user               string
	csrf               string
	exp                time.Time
	pwDismissed        bool // 本次会话已关闭「修改默认密码」提示（下次登录重新提示）
	mustChangePassword bool // 本会话使用默认密码登录，修改前不得进入其他后台页面
	currentSiteID      int64
}

type settingsFlash struct {
	Flash             string
	FormErr           string
	NewAPISecret      []string
	NewPlatformSecret []string
	SiteFormErr       string
	SiteFormVals      map[string]string
}

type sessions struct {
	store         adminSessionStore
	mu            sync.Mutex
	settingsFlash map[string]settingsFlash
}

type adminSessionStore interface {
	CreateAdminSession(token, user, csrf string, expiresAt time.Time) error
	GetAdminSession(token string) (store.AdminSession, bool, error)
	DeleteAdminSession(token string) error
	DismissAdminPasswordWarning(token string) error
	RequireAdminPasswordChange(token string) error
	SetAdminSessionSite(token string, siteID int64) error
}

func newSessions(st adminSessionStore) *sessions {
	return &sessions{store: st, settingsFlash: map[string]settingsFlash{}}
}

func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *sessions) create(user string) (string, error) {
	tok := randToken()
	csrf := randToken()
	exp := time.Now().Add(24 * time.Hour)
	if err := s.store.CreateAdminSession(tok, user, csrf, exp); err != nil {
		return "", err
	}
	return tok, nil
}

func (s *sessions) get(tok string) (session, bool) {
	dbSess, ok, err := s.store.GetAdminSession(tok)
	if err != nil || !ok {
		return session{}, false
	}
	return session{
		user: dbSess.User, csrf: dbSess.CSRF, exp: dbSess.ExpiresAt,
		pwDismissed: dbSess.PwDismissed, mustChangePassword: dbSess.MustChangePassword,
		currentSiteID: dbSess.CurrentSiteID,
	}, true
}

func (s *sessions) destroy(tok string) {
	_ = s.store.DeleteAdminSession(tok)
	s.mu.Lock()
	delete(s.settingsFlash, tok)
	s.mu.Unlock()
}

// dismissPw 标记该会话已关闭默认密码提示。
func (s *sessions) dismissPw(tok string) {
	_ = s.store.DismissAdminPasswordWarning(tok)
}

func (s *sessions) requirePasswordChange(tok string) error {
	return s.store.RequireAdminPasswordChange(tok)
}

func (s *sessions) setCurrentSite(tok string, siteID int64) error {
	if tok == "" || siteID <= 0 {
		return nil
	}
	return s.store.SetAdminSessionSite(tok, siteID)
}

func (s *sessions) setSettingsFlash(tok string, f settingsFlash) {
	if tok == "" {
		return
	}
	s.mu.Lock()
	s.settingsFlash[tok] = f
	s.mu.Unlock()
}

func (s *sessions) takeSettingsFlash(tok string) (settingsFlash, bool) {
	if tok == "" {
		return settingsFlash{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.settingsFlash[tok]
	if ok {
		delete(s.settingsFlash, tok)
	}
	return f, ok
}

// ---------- 登录失败限流（防穷举） ----------

// loginLimiter 按客户端 IP 统计登录失败：窗口内累计达 max 次即锁定 lockout 时长。
type loginLimiter struct {
	mu      sync.Mutex
	m       map[string]*loginAttempt
	max     int
	window  time.Duration
	lockout time.Duration
}

type loginAttempt struct {
	fails int
	first time.Time
	until time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{m: map[string]*loginAttempt{}, max: 5, window: 15 * time.Minute, lockout: 10 * time.Minute}
}

// lockedFor 返回该 key 仍需等待的锁定时长（0 表示未锁定）。
func (l *loginLimiter) lockedFor(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if a := l.m[key]; a != nil && time.Now().Before(a.until) {
		return time.Until(a.until)
	}
	return 0
}

// fail 记一次失败：窗口外则重新计数；窗口内累计达阈值则锁定。
func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	a := l.m[key]
	if a == nil || now.Sub(a.first) > l.window {
		a = &loginAttempt{first: now}
		l.m[key] = a
	}
	a.fails++
	if a.fails >= l.max {
		a.until = now.Add(l.lockout)
		a.fails = 0
		a.first = now
	}
	// 顺手清理过期条目，避免 map 无界增长
	for k, v := range l.m {
		if now.After(v.until) && now.Sub(v.first) > l.window {
			delete(l.m, k)
		}
	}
}

// reset 登录成功后清除该 key 的失败记录。
func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.m, key)
	l.mu.Unlock()
}

// clientIP 取请求来源 IP（用作限流 key）。
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if forwarded := forwardedClientIP(r, host); forwarded != "" {
			return forwarded
		}
		return host
	}
	if forwarded := forwardedClientIP(r, host); forwarded != "" {
		return forwarded
	}
	return r.RemoteAddr
}

func forwardedClientIP(r *http.Request, remoteHost string) string {
	ip := net.ParseIP(remoteHost)
	if ip == nil || !ip.IsLoopback() {
		return ""
	}
	forwarded := firstHeaderValue(r.Header.Get("X-Forwarded-For"))
	if forwarded == "" {
		return ""
	}
	parsed := net.ParseIP(forwarded)
	if parsed == nil {
		return ""
	}
	return parsed.String()
}

func (s *Server) currentSession(r *http.Request) (session, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return session{}, false
	}
	return s.sess.get(c.Value)
}

func (s *Server) adminCredentials() (string, string) {
	if s.platform != nil {
		user, hash, err := s.platform.GetAdminCredentials()
		if err == nil && strings.TrimSpace(user) != "" && strings.TrimSpace(hash) != "" {
			return user, hash
		}
	}
	user, _ := s.store.GetSetting("admin_user")
	hash, _ := s.store.GetSetting("admin_password_hash")
	if s.platform != nil && strings.TrimSpace(user) != "" && strings.TrimSpace(hash) != "" {
		_ = s.platform.SetAdminPasswordHash(user, hash)
	}
	return user, hash
}

func (s *Server) setAdminPasswordHash(user, hash string) error {
	if s.platform != nil {
		if strings.TrimSpace(user) == "" {
			storedUser, _, _ := s.platform.GetAdminCredentials()
			user = storedUser
		}
		if strings.TrimSpace(user) == "" {
			user = "admin"
		}
		if err := s.platform.SetAdminPasswordHash(user, hash); err != nil {
			return err
		}
	}
	return s.store.SetSetting("admin_password_hash", hash)
}

func (s *Server) adminPasswordIsDefault() bool {
	if s.platform != nil {
		return s.platform.IsDefaultPassword()
	}
	return s.store.IsDefaultPassword()
}

func sessionToken(r *http.Request) string {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func wantsJSON(r *http.Request) bool {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		return true
	}
	if strings.EqualFold(r.Header.Get("X-Requested-With"), "XMLHttpRequest") {
		return true
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) secureCookie(r *http.Request) bool {
	return strings.HasPrefix(s.baseURL, "https://") || requestScheme(r) == "https"
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	// 生产（BASE_URL 为 https，或经 Caddy 传入 X-Forwarded-Proto=https）时加 Secure。
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: s.secureCookie(r), SameSite: http.SameSiteLaxMode, MaxAge: 86400})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, Secure: s.secureCookie(r), SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.currentSession(r)
		if !ok {
			if wantsJSON(r) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login_required", "message": "登录已过期，请重新登录。"})
				return
			}
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		if sess.mustChangePassword && s.adminPasswordIsDefault() && r.URL.Path != s.adminPasswordURL() {
			if wantsJSON(r) {
				writeJSON(w, http.StatusPreconditionRequired, map[string]string{
					"error":   "password_change_required",
					"message": "首次登录需要先修改默认密码。",
				})
				return
			}
			http.Redirect(w, r, s.adminPasswordURL(), http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// checkCSRF 校验已登录会话与表单中的 CSRF 令牌。
func (s *Server) checkCSRF(w http.ResponseWriter, r *http.Request) (session, bool) {
	sess, ok := s.currentSession(r)
	if !ok {
		if wantsJSON(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login_required", "message": "登录已过期，请重新登录。"})
			return session{}, false
		}
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
		return session{}, false
	}
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		_ = r.ParseMultipartForm(8 << 20)
	} else {
		_ = r.ParseForm()
	}
	if r.FormValue("_csrf") != sess.csrf {
		if wantsJSON(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "bad_csrf", "message": "无效的 CSRF 令牌。"})
			return session{}, false
		}
		http.Error(w, "无效的 CSRF 令牌", http.StatusForbidden)
		return session{}, false
	}
	return sess, true
}

func (s *Server) adminLang(r *http.Request) string {
	if r != nil {
		if lang := strings.TrimSpace(r.URL.Query().Get("admin_lang")); s.i18n.Known(lang) {
			return lang
		}
		if c, err := r.Cookie(adminLangCookie); err == nil && s.i18n.Known(c.Value) {
			return c.Value
		}
		if lang := negotiateAcceptLanguage(r.Header.Get("Accept-Language"), s.i18n.All(), "zh"); s.i18n.Known(lang) {
			return lang
		}
	}
	return "zh"
}

func (s *Server) adminI18NKey(lang string) string {
	if !s.i18n.Known(lang) {
		lang = "zh"
	}
	return "admin_i18n::" + lang
}

func (s *Server) migratePlatformAdminI18N() {
	if s == nil || s.platform == nil || s.store == nil {
		return
	}
	for _, lang := range s.i18n.AdminLocales() {
		key := s.adminI18NKey(lang.Code)
		if _, ok, err := s.platform.LookupSetting(key); err == nil && ok {
			continue
		}
		if legacy := strings.TrimSpace(s.store.Setting(key)); legacy != "" {
			_ = s.platform.SetSetting(key, legacy)
		}
	}
}

func (s *Server) legacyAdminI18NRaw(key string) string {
	if s == nil || s.store == nil {
		return ""
	}
	if v := strings.TrimSpace(s.store.Setting(key)); v != "" {
		return v
	}
	if pool := s.runtimePool(); pool != nil && pool.defaultSite != nil && pool.defaultSite.Store != nil && pool.defaultSite.Store != s.store {
		return strings.TrimSpace(pool.defaultSite.Store.Setting(key))
	}
	return ""
}

func (s *Server) adminI18NRaw(lang string) string {
	key := s.adminI18NKey(lang)
	if s.platform != nil {
		if v, ok, err := s.platform.LookupSetting(key); err == nil && ok {
			return v
		}
		if legacy := s.legacyAdminI18NRaw(key); legacy != "" {
			return legacy
		}
		return ""
	}
	return s.store.Setting(key)
}

func (s *Server) setAdminI18NRaw(lang, raw string) error {
	key := s.adminI18NKey(lang)
	if s.platform != nil {
		return s.platform.SetSetting(key, raw)
	}
	return s.store.SetSetting(key, raw)
}

func (s *Server) adminI18NOverrides(lang string) map[string]string {
	return i18n.ParseAdminOverrides(s.adminI18NRaw(lang))
}

func adminTitleKey(title string) string {
	switch title {
	case "登录":
		return "admin.login.title"
	case "概览":
		return "admin.dashboard.title"
	case "站点":
		return "admin.sites.title"
	case "站点管理":
		return "admin.sites.title"
	case "文章":
		return "admin.posts.title"
	case "链接":
		return "admin.links.title"
	case "页面":
		return "admin.pages.title"
	case "设置":
		return "admin.settings.title"
	case "平台设置":
		return "admin.platform.settings.title"
	case "数据备份":
		return "admin.backups.title"
	case "归档站点":
		return "admin.archived_sites.title"
	case "存储清理":
		return "admin.media_cleanup.title"
	case "可视化编辑":
		return "admin.nav.visual"
	case "安全":
		return "admin.security.title"
	case "系统更新":
		return "admin.settings.menu.updates"
	case "后台翻译", "后台显示文字", "后台文案", "界面文字":
		return "admin.settings.admin_i18n.title"
	default:
		return ""
	}
}

func (s *Server) adminView(r *http.Request, title string) *View {
	def := s.defaultLang()
	site := s.site(def)
	adminLang := s.adminLang(r)
	admin := s.i18n.AdminTr(adminLang, s.adminI18NOverrides(adminLang))
	titleText := admin.T(adminTitleKey(title), title)
	suffix := admin.T("admin.brand.suffix", "后台")
	adminReturn := "/admin"
	if r != nil && r.URL != nil {
		adminReturn = r.URL.RequestURI()
	}
	return &View{
		Site:          site,
		SEO:           seo.Meta{Title: titleText + " — " + site.Name + " " + suffix, Robots: "noindex, nofollow"},
		Year:          time.Now().Year(),
		Tr:            s.i18n.Tr(def, def),
		Lang:          def,
		Admin:         admin,
		AdminLang:     adminLang,
		AdminLangs:    s.i18n.AdminLocales(),
		AdminReturn:   adminReturn,
		EditLang:      def,
		Locales:       s.locales(),
		AllLocales:    s.i18n.All(),
		AssetVer:      s.assetVer,
		PlatformMode:  s.platform != nil,
		AdminSiteURL:  "/" + def + "/",
		PrimaryExtNav: s.primaryExtNav(adminLang), // 已启用且 Primary 的扩展类型（如商品）上浮为一级菜单
	}
}

// authed 填充已登录后台页的公共字段：登录态、CSRF、默认密码提示。
func (s *Server) authed(v *View, sess session) {
	v.Authed = true
	v.CSRF = sess.csrf
	v.ShowPwWarn = s.passwordWarningVisible(sess, false)
	v.ForcePasswordChange = sess.mustChangePassword && s.adminPasswordIsDefault()
	v.PlatformCurrentSiteID = sess.currentSiteID
	v.AdminPreviewPrefix = s.adminSitePreviewPrefix(sess.currentSiteID)
	// 「查看」已发布内容：站点绑了正式域名（或 CF 已发布）就开真实地址——预览通道只是
	// 没有正式入口时的替身；单站部署前缀为空，相对路径本来就是真实地址。
	v.AdminViewPrefix = v.AdminPreviewPrefix
	if base := s.adminSitePublicBaseURL(sess.currentSiteID); base != "" {
		v.AdminViewPrefix = base
	}
	v.AdminSiteURL = s.adminSiteURL(sess.currentSiteID, v.EditLang)
	s.populatePlatformSites(v)
}

// adminSitePublicBaseURL 站点的正式对外入口（不带末尾斜杠）：Cloudflare 已发布取官方域名，
// 否则取已启用域名里的主域名（SiteDomains 排序主域名在前）。没有正式入口返回 ""。
func (s *Server) adminSitePublicBaseURL(siteID int64) string {
	if href, host := s.platformOfficialSiteURL(siteID); href != "" && host != "" {
		return strings.TrimRight(href, "/")
	}
	if s.platform == nil || siteID <= 0 {
		return ""
	}
	doms, err := s.platform.SiteDomains()
	if err != nil {
		return ""
	}
	for _, d := range doms {
		if d == nil || d.SiteID != siteID || !d.Enabled || strings.TrimSpace(d.Host) == "" {
			continue
		}
		scheme := strings.TrimSpace(d.Scheme)
		if scheme == "" {
			scheme = "https"
		}
		return scheme + "://" + strings.TrimSpace(d.Host)
	}
	return ""
}

func (s *Server) platformAuthed(v *View, sess session) {
	s.authed(v, sess)
	v.PlatformAdminView = true
	v.ShowPwWarn = s.passwordWarningVisible(sess, true)
}

func (s *Server) passwordWarningVisible(sess session, platformView bool) bool {
	if sess.pwDismissed || !s.adminPasswordIsDefault() {
		return false
	}
	if s.platform == nil {
		return true
	}
	return platformView
}

func (s *Server) adminSiteURL(siteID int64, lang string) string {
	if !s.langEnabled(lang) {
		lang = s.defaultLang()
	}
	if prefix := s.adminSitePreviewPrefix(siteID); prefix != "" {
		return localizedPath(prefix, lang, "/")
	}
	return "/" + lang + "/"
}

func (s *Server) adminSitePreviewPrefix(siteID int64) string {
	if s.platform == nil || siteID <= 0 {
		return ""
	}
	site, ok, err := s.platform.GetSite(siteID)
	if err != nil || !ok || site == nil {
		return ""
	}
	return "/admin/sites/" + strconv.FormatInt(siteID, 10) + "/preview"
}

func (s *Server) adminSiteVisualPreviewURL(siteID int64, lang string) string {
	return s.adminSiteURL(siteID, lang) + "?visual_edit=1"
}

func (s *Server) automationBaseURL(r *http.Request, currentSiteID int64) string {
	path := "/api/admin/v1"
	if s.platform != nil && currentSiteID > 0 {
		if site, ok, err := s.platform.GetSite(currentSiteID); err == nil && ok && site != nil && !site.IsDefault {
			path = "/api/platform/v1/sites/" + strconv.FormatInt(currentSiteID, 10)
			return s.absForPlatformRequest(r, path)
		}
	}
	return s.absForRequest(r, path)
}

func (s *Server) populatePlatformSites(v *View) {
	if v == nil || s.platform == nil {
		return
	}
	sites, err := s.platform.Sites()
	if err != nil {
		return
	}
	v.PlatformSites = sites
	v.PlatformSiteIcons = map[int64]string{}
	v.PlatformPreviewURLs = map[int64]string{}
	v.PlatformOfficialURLs = map[int64]string{}
	v.PlatformOfficialHosts = map[int64]string{}
	for _, site := range sites {
		if site == nil {
			continue
		}
		v.PlatformPreviewURLs[site.ID] = s.platformSitePreviewURL(site.ID)
		if icon := s.platformSiteIconURL(site.ID); icon != "" {
			v.PlatformSiteIcons[site.ID] = icon
		}
		if href, host := s.platformOfficialSiteURL(site.ID); href != "" && host != "" {
			v.PlatformOfficialURLs[site.ID] = href
			v.PlatformOfficialHosts[site.ID] = host
		}
	}
}

func (s *Server) platformSitePreviewURL(siteID int64) string {
	prefix := s.adminSitePreviewPrefix(siteID)
	if prefix == "" {
		return "/" + s.defaultLang() + "/"
	}
	// 平台卡片只保存稳定的站点预览入口，不把“当前默认语种”写死进 href。
	// 真正打开时由 serveSitePreview 读取该站点的实时语种设置；这样站点从 zh
	// 切到 en 后，旧后台页面也不会继续把用户带到 /en/zh/。
	return strings.TrimRight(prefix, "/") + "/"
}

func (s *Server) platformOfficialSiteURL(siteID int64) (string, string) {
	if siteID <= 0 {
		return "", ""
	}
	var site *platform.Site
	if s.platform != nil {
		if loaded, found, err := s.platform.GetSite(siteID); err == nil && found {
			site = loaded
		}
	}
	if site != nil && site.Status != "enabled" {
		return "", ""
	}
	siteServer := s
	if rt, ok := s.runtimePool().runtimeByID(siteID); ok && rt != nil {
		if site == nil {
			site = rt.Site
		}
		if rt.server != nil {
			siteServer = rt.server
		} else if rt.Store != nil {
			siteServer = s.cloneForRuntime(rt)
		}
	}
	if site != nil && site.Status != "enabled" {
		return "", ""
	}
	if siteServer == nil || siteServer.store == nil {
		return "", ""
	}
	view := siteServer.cloudflareView()
	if view == nil || view.Status == nil || !cloudflareStatusPublished(view.Status) {
		return "", ""
	}
	host := strings.TrimSpace(view.Config.primaryHost())
	if host == "" {
		host = strings.TrimSpace(view.Status.PrimaryDomain)
	}
	if host == "" {
		return "", ""
	}
	return "https://" + host + "/", host
}

func (s *Server) platformSiteIconURL(siteID int64) string {
	raw := ""
	uploadDir := ""
	var site *platform.Site
	if s.platform != nil {
		if loaded, found, err := s.platform.GetSite(siteID); err == nil && found {
			site = loaded
		}
	}
	if rt, ok := s.runtimePool().runtimeByID(siteID); ok && rt != nil && rt.Store != nil {
		raw = strings.TrimSpace(rt.Store.Setting("site.favicon"))
		uploadDir = strings.TrimSpace(rt.UploadDir)
	}
	if site != nil && uploadDir == "" {
		uploadDir = strings.TrimSpace(site.UploadDir)
	}
	if raw == "" && site != nil {
		if strings.TrimSpace(site.DBPath) == "" {
			if icon := s.legacyUploadFaviconURL(siteID, uploadDir); icon != "" {
				return icon
			}
		}
		if strings.TrimSpace(site.DBPath) != "" {
			if _, statErr := os.Stat(site.DBPath); statErr == nil {
				st, openErr := store.Open(site.DBPath)
				if openErr == nil {
					raw = strings.TrimSpace(st.Setting("site.favicon"))
					_ = st.Close()
				}
			}
		}
	}
	if raw == "" {
		if icon := s.legacyUploadFaviconURL(siteID, uploadDir); icon != "" {
			return icon
		}
		if site != nil && site.IsDefault {
			return defaultFaviconPath
		}
		return ""
	}
	if strings.HasPrefix(raw, "/uploads/") {
		name, ok := uploadNameFromPath(strings.Split(raw, "?")[0])
		if !ok {
			return ""
		}
		return s.adminSiteUploadURL(siteID, name)
	}
	if strings.HasPrefix(raw, "/assets/") || raw == "/favicon.ico" || strings.HasPrefix(raw, "data:image/") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return raw
	}
	return ""
}

func (s *Server) legacyUploadFaviconURL(siteID int64, uploadDir string) string {
	name := legacyUploadFaviconName(uploadDir)
	if name == "" {
		return ""
	}
	return s.adminSiteUploadURL(siteID, name)
}

func (s *Server) adminSiteUploadURL(siteID int64, name string) string {
	return "/admin/sites/" + strconv.FormatInt(siteID, 10) + "/uploads/" + url.PathEscape(name)
}

func legacyUploadFaviconName(uploadDir string) string {
	uploadDir = strings.TrimSpace(uploadDir)
	if uploadDir == "" {
		return ""
	}
	for _, name := range []string{"favicon.ico", "favicon.svg", "favicon.png", "favicon.webp", "site-favicon.ico", "site-favicon.svg", "site-favicon.png", "site-favicon.webp"} {
		if info, err := os.Stat(filepath.Join(uploadDir, name)); err == nil && !info.IsDir() {
			return name
		}
	}
	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		return ""
	}
	bestName := ""
	var bestTime time.Time
	for _, entry := range entries {
		if entry == nil || entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !validUploadFilename(name) || !strings.EqualFold(filepath.Ext(name), ".ico") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if bestName == "" || info.ModTime().After(bestTime) {
			bestName = name
			bestTime = info.ModTime()
		}
	}
	return bestName
}

func archivedSiteIconDataURL(site *platform.ArchivedSite) string {
	if site == nil {
		return ""
	}
	archivePath := filepath.Clean(strings.TrimSpace(site.ArchivePath))
	if archivePath == "" || archivePath == "." {
		return ""
	}
	uploadDir := filepath.Join(archivePath, "uploads")
	raw := ""
	dbPath := filepath.Join(archivePath, "cms.db")
	if _, err := os.Stat(dbPath); err == nil {
		st, openErr := store.Open(dbPath)
		if openErr == nil {
			raw = strings.TrimSpace(st.Setting("site.favicon"))
			_ = st.Close()
		}
	}
	if strings.HasPrefix(raw, "data:image/") || strings.HasPrefix(raw, "/assets/") {
		return raw
	}
	if strings.HasPrefix(raw, "/uploads/") {
		if name, ok := uploadNameFromPath(strings.Split(raw, "?")[0]); ok {
			return imageFileDataURL(filepath.Join(uploadDir, name))
		}
	}
	if u, err := url.Parse(raw); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		return raw
	}
	if name := legacyUploadFaviconName(uploadDir); name != "" {
		return imageFileDataURL(filepath.Join(uploadDir, name))
	}
	return ""
}

func imageFileDataURL(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() <= 0 || info.Size() > 256*1024 {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	mimeType := http.DetectContentType(data)
	switch strings.ToLower(filepath.Ext(path)) {
	case ".svg":
		mimeType = "image/svg+xml"
	case ".ico":
		mimeType = "image/x-icon"
	case ".webp":
		mimeType = "image/webp"
	case ".png":
		mimeType = "image/png"
	case ".jpg", ".jpeg":
		mimeType = "image/jpeg"
	}
	if !strings.HasPrefix(mimeType, "image/") {
		return ""
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// catKind 取分类管理当前的类型（来自 ?kind= 或表单）：post|link，
// 或「已启用且支持分类」的扩展类型 key（如 product）——与后台一级菜单的
// Primary 泛化思路一致：不硬编码具体类型，跟着引擎的启用状态走。
// 未知/未启用的 kind 一律回落 post。
func (s *Server) catKind(r *http.Request) string {
	raw := strings.TrimSpace(r.URL.Query().Get("kind"))
	if raw == "" {
		raw = strings.TrimSpace(r.FormValue("kind"))
	}
	switch raw {
	case "post", "link":
		return raw
	}
	for _, ct := range s.activeExtContentTypes() {
		if ct.HasCategory && ct.Key == raw {
			return raw
		}
	}
	return "post"
}

// editLang 取后台当前操作的内容语种（?lang= 或表单 lang），校验后回落默认语种。
func (s *Server) editLang(r *http.Request) string {
	if l := r.URL.Query().Get("lang"); l != "" && s.langEnabled(l) {
		return l
	}
	if l := r.FormValue("lang"); l != "" && s.langEnabled(l) {
		return l
	}
	return s.defaultLang()
}

// ---------- 登录 / 登出 ----------

func (s *Server) adminLoginForm(w http.ResponseWriter, r *http.Request) {
	if sess, ok := s.currentSession(r); ok {
		target := s.adminLandingPath()
		if sess.mustChangePassword && s.adminPasswordIsDefault() {
			target = s.adminPasswordURL()
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	s.rnd.Admin(w, "login", http.StatusOK, s.adminView(r, "登录"))
}

func (s *Server) adminLoginPost(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	ip := clientIP(r)
	// 防穷举：同一 IP 短时间内多次失败则暂时锁定
	if wait := s.login.lockedFor(ip); wait > 0 {
		v := s.adminView(r, "登录")
		v.FormErr = fmt.Sprintf(v.Admin.T("admin.login.too_many", "登录尝试过于频繁，请约 %d 分钟后再试。"), int(wait/time.Minute)+1)
		s.rnd.Admin(w, "login", http.StatusTooManyRequests, v)
		return
	}
	user := strings.TrimSpace(r.FormValue("username"))
	pass := r.FormValue("password")
	storedUser, hash := s.adminCredentials()
	if user == storedUser && hash != "" && bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)) == nil {
		s.login.reset(ip)
		mustChangePassword := s.adminPasswordIsDefault()
		tok, err := s.sess.create(user)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if mustChangePassword {
			if err := s.sess.requirePasswordChange(tok); err != nil {
				s.sess.destroy(tok)
				s.serverError(w, err)
				return
			}
		}
		s.setSessionCookie(w, r, tok)
		target := s.adminLandingPath()
		if mustChangePassword {
			target = s.adminPasswordURL()
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	s.login.fail(ip)
	v := s.adminView(r, "登录")
	v.FormErr = v.Admin.T("admin.login.bad_credentials", "用户名或密码错误")
	s.rnd.Admin(w, "login", http.StatusUnauthorized, v)
}

func (s *Server) adminLandingPath() string {
	if s.platform != nil {
		return "/admin/sites"
	}
	return "/admin"
}

func (s *Server) adminLanguage(w http.ResponseWriter, r *http.Request) {
	lang := strings.TrimSpace(r.URL.Query().Get("lang"))
	if !s.i18n.Known(lang) {
		lang = "zh"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminLangCookie,
		Value:    lang,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   s.secureCookie(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   365 * 24 * 60 * 60,
	})
	http.Redirect(w, r, safeAdminNext(r.URL.Query().Get("next")), http.StatusSeeOther)
}

func safeAdminNext(raw string) string {
	if raw == "" || strings.ContainsAny(raw, "\r\n") || strings.HasPrefix(raw, "//") {
		return "/admin"
	}
	if raw == "/admin" || strings.HasPrefix(raw, "/admin/") || strings.HasPrefix(raw, "/admin?") {
		return raw
	}
	return "/admin"
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		if sess, ok := s.sess.get(c.Value); ok && r.FormValue("_csrf") == sess.csrf {
			s.sess.destroy(c.Value)
		}
	}
	s.clearSessionCookie(w, r)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// adminDismissPw 关闭本会话的「修改默认密码」提示（下次登录会重新出现）。
func (s *Server) adminDismissPw(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if c, err := r.Cookie(cookieName); err == nil {
		s.sess.dismissPw(c.Value)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- 文章管理 ----------

const adminListPageSize = 20

func adminStatusFilter(r *http.Request) string {
	switch strings.TrimSpace(r.URL.Query().Get("status")) {
	case "draft", "published", "scheduled", "discarded": // discarded = AI 报废申请「待清理」档
		return strings.TrimSpace(r.URL.Query().Get("status"))
	default:
		return ""
	}
}

func adminCategoryFilter(r *http.Request) string {
	return strings.Trim(strings.TrimSpace(r.URL.Query().Get("cat")), "/")
}

func (s *Server) adminListRedirect(base string, r *http.Request) string {
	parts := []string{}
	if lang := strings.TrimSpace(r.FormValue("lang")); s.langEnabled(lang) {
		parts = append(parts, "lang="+lang)
	}
	switch status := strings.TrimSpace(r.FormValue("status")); status {
	case "draft", "published", "scheduled", "discarded":
		parts = append(parts, "status="+status)
	}
	if cat := strings.Trim(strings.TrimSpace(r.FormValue("cat")), "/"); cat != "" {
		parts = append(parts, "cat="+cat)
	}
	if q := strings.TrimSpace(r.FormValue("q")); q != "" { // 扩展列表的标题搜索词，操作后保住筛选现场
		parts = append(parts, "q="+url.QueryEscape(q))
	}
	if page, err := strconv.Atoi(strings.TrimSpace(r.FormValue("page"))); err == nil && page > 1 {
		parts = append(parts, "page="+strconv.Itoa(page))
	}
	if len(parts) == 0 {
		return base
	}
	return base + "?" + strings.Join(parts, "&")
}

func (s *Server) adminDashboard(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	lang := s.editLang(r)
	v := s.adminView(r, "概览")
	s.authed(v, sess)
	v.EditLang = lang

	contentCounts, err := s.store.AdminContentStatusCounts(lang)
	if err != nil {
		s.serverError(w, err)
		return
	}
	contentIssues, err := s.store.AdminContentIssueCounts(lang)
	if err != nil {
		s.serverError(w, err)
		return
	}
	stats, err := s.adminOverviewStats(lang, v.Admin, contentCounts)
	if err != nil {
		s.serverError(w, err)
		return
	}
	tasks, err := s.adminOverviewTasks(lang, v.Admin, contentCounts, contentIssues)
	if err != nil {
		s.serverError(w, err)
		return
	}
	recent, err := s.store.ListRecentAdminContent(lang, 8)
	if err != nil {
		s.serverError(w, err)
		return
	}
	keys, err := s.store.ListAutomationKeys()
	if err != nil {
		s.serverError(w, err)
		return
	}
	logs, err := s.store.ListAutomationLogs(5)
	if err != nil {
		s.serverError(w, err)
		return
	}

	v.OverviewStats = stats
	v.OverviewTasks = tasks
	v.OverviewRecent = recent
	v.AutomationKeys = keys
	v.AutomationLogs = logs
	v.Update = currentUpdateInfo()
	v.Upgrade = readUpgradeStatus()
	v.OverviewStatus = s.adminOverviewStatus(v, keys)
	s.rnd.Admin(w, "dashboard", http.StatusOK, v)
}

func (s *Server) adminSites(w http.ResponseWriter, r *http.Request) {
	s.showAdminSites(w, r, http.StatusOK, "", nil)
}

func (s *Server) showAdminSites(w http.ResponseWriter, r *http.Request, status int, formErr string, formVals map[string]string) {
	sess, _ := s.currentSession(r)
	flash := ""
	if status == http.StatusOK && formErr == "" && formVals == nil {
		if f, ok := s.sess.takeSettingsFlash(sessionToken(r)); ok {
			flash = f.Flash
			formErr = f.SiteFormErr
			formVals = f.SiteFormVals
		}
	}
	v := s.adminView(r, "站点管理")
	s.platformAuthed(v, sess)
	v.PlatformCurrentSiteID = sess.currentSiteID
	v.Flash = flash
	v.FormErr = formErr
	if formVals == nil {
		formVals = map[string]string{}
	}
	v.FormVals = formVals
	v.ServerHealth = s.serverHealthSnapshot()
	v.CaddyOnDemand = caddyOnDemandEnabled()
	if s.platform != nil {
		cfTok := strings.TrimSpace(s.platform.Setting(platformCFDNSTokenKey))
		v.CFDNSTokenSet = cfTok != ""
		v.CFDNSFingerprint = cloudflareTokenFingerprint(cfTok)
		v.CFDeployTokenSet = strings.TrimSpace(s.platform.Setting(cloudflareAPITokenKey)) != ""
		v.CFAccountName = strings.TrimSpace(s.platform.Setting(cloudflareAccountNameKey))
		v.CFZoneName = strings.TrimSpace(s.platform.Setting(cloudflareZoneNameKey))
		v.CFServerIPv4 = s.platform.Setting(platformServerIPv4Key)
		v.CFServerIPv6 = s.platform.Setting(platformServerIPv6Key)
		v.CFAuthorizeURL = cloudflareAPITokenTemplateURL("GCMS DNS")
		v.CFProxied = s.platform.Setting(platformCFProxiedKey) != "0" // 默认勾选：未设置或 "1" 都勾上，仅显式 "0" 取消
		googleCfg := s.googleOAuthConfig(r)
		v.GoogleOAuthConfigured = googleCfg.ClientID != "" && googleCfg.ClientSecret != ""
		v.GoogleOAuthClientID = googleCfg.ClientID
		v.GoogleOAuthRedirectURL = googleCfg.RedirectURL
		v.GoogleOAuthSecretSet = googleCfg.ClientSecret != ""
		v.GoogleOAuthProjectID = googleCloudProjectNumberFromClientID(googleCfg.ClientID)
		v.GoogleAnalyticsAdminAPIURL = googleCloudAPIEnableURL("analyticsadmin.googleapis.com", v.GoogleOAuthProjectID)
		v.GoogleAnalyticsDataAPIURL = googleCloudAPIEnableURL("analyticsdata.googleapis.com", v.GoogleOAuthProjectID)
		v.GoogleSearchConsoleAPIURL = googleCloudAPIEnableURL("webmasters.googleapis.com", v.GoogleOAuthProjectID)
		googleRange := s.googleDataRange()
		v.GoogleDataRangeMode = googleRange.Mode
		v.GoogleDataRangeDays = googleRange.Days
		v.GoogleDataRangeFrom = googleRange.From
		v.GoogleDataRangeTo = googleRange.To
		v.GoogleDataRangeLabel = googleRange.Label
		if accounts, err := s.platform.GoogleAccounts(platform.GoogleServiceAnalytics); err == nil {
			v.GoogleAnalyticsAccounts = accounts
		}
		if accounts, err := s.platform.GoogleAccounts(platform.GoogleServiceSearchConsole); err == nil {
			v.GoogleSearchConsoleAccounts = accounts
		}
		v.GoogleAccounts = mergeGoogleAccounts(v.GoogleAnalyticsAccounts, v.GoogleSearchConsoleAccounts)
		if integrations, err := s.platform.SiteGoogleIntegrations(); err == nil {
			v.SiteGoogleIntegrations = integrations
		}
		v.PlatformTelegramTokenSet = s.platformTelegramBotToken() != ""
		if summaries, err := s.platform.SiteGoogleAnalyticsSummaries(); err == nil {
			v.SiteGoogleAnalyticsSummaries = summaries
		}
		if summaries, err := s.platform.SiteGoogleSearchConsoleSummaries(); err == nil {
			v.SiteGoogleSearchSummaries = summaries
		}
	}
	if s.platform == nil {
		siteName := strings.TrimSpace(s.store.Setting("site.name"))
		if siteName == "" {
			siteName = "Default Site"
		}
		v.PlatformSites = []*platform.Site{{
			ID:                          1,
			Slug:                        "main",
			Name:                        siteName,
			Status:                      "enabled",
			IsDefault:                   true,
			ManagementAutomationEnabled: true,
			DBPath:                      "CMS_DB",
			UploadDir:                   s.uploadDir,
		}}
		v.PlatformSiteIcons = map[int64]string{}
		v.PlatformPreviewURLs = map[int64]string{1: "/" + s.defaultLang() + "/"}
		v.PlatformOfficialURLs = map[int64]string{}
		v.PlatformOfficialHosts = map[int64]string{}
		v.PlatformGoogleDefaultURIs = map[int64]string{1: s.defaultGoogleAnalyticsURI(r, v.PlatformSites[0])}
		v.SiteGoogleAnalyticsSummaries = map[int64]*platform.SiteGoogleAnalyticsSummary{}
		v.SiteGoogleSearchSummaries = map[int64]*platform.SiteGoogleSearchConsoleSummary{}
		v.SiteGoogleIntegrations = map[int64]map[string]*platform.SiteGoogleIntegration{
			1: {},
		}
		v.SiteTelegramStatus = map[int64]SiteTelegramStatus{1: s.siteTelegramStatusFor(s.store)}
		if href, host := s.platformOfficialSiteURL(1); href != "" && host != "" {
			v.PlatformOfficialURLs[1] = href
			v.PlatformOfficialHosts[1] = host
		}
		s.setSiteCounts(v, 1, s.store)
		// 左下角芯片：单站模式没有平台 sites 表（合成站点无创建时间、不能绑域名），
		// 口径与平台模式一致，缺来源时优雅降级（详见 buildDeployChip）。
		cfStatus := s.readCloudflareStatus()
		_, isCF := v.PlatformOfficialURLs[1]
		if isCF && cfStatus != nil {
			v.PlatformCFStatus = map[int64]string{1: strings.TrimSpace(cfStatus.Status)}
		}
		contentAt, hasContent := parseChipTime(v.PlatformContentUpdatedAt[1])
		v.PlatformDeployChips = map[int64]*DeployChip{1: buildDeployChip(v.Admin, deployChipInput{
			Site:        v.PlatformSites[0],
			CFPublished: isCF,
			CFStatus:    cfStatus,
			ContentAt:   contentAt,
			HasContent:  hasContent,
		}, time.Now())}
		s.rnd.Admin(w, "sites", status, v)
		return
	}
	sites, err := s.platform.Sites()
	if err != nil {
		s.serverError(w, err)
		return
	}
	domains, err := s.platform.SiteDomains()
	if err != nil {
		s.serverError(w, err)
		return
	}
	v.PlatformDomains = map[int64][]*platform.SiteDomain{}
	for _, d := range domains {
		if d != nil && d.Enabled {
			v.PlatformDomains[d.SiteID] = append(v.PlatformDomains[d.SiteID], d)
		}
	}
	v.PlatformDomainForms = map[int64]SiteDomainForm{}
	for siteID, ds := range v.PlatformDomains {
		form := SiteDomainForm{}
		var aliases []string
		for _, d := range ds {
			if d.IsPrimary {
				form.PrimaryHost = d.Host
			} else {
				aliases = append(aliases, d.Host)
				if d.RedirectToPrimary {
					form.RedirectAliases = true
				}
			}
		}
		form.AliasText = strings.Join(aliases, "\n")
		v.PlatformDomainForms[siteID] = form
	}
	v.PlatformSites = sites
	v.PlatformGoogleDefaultURIs = map[int64]string{}
	if v.SiteGoogleAnalyticsSummaries == nil {
		v.SiteGoogleAnalyticsSummaries = map[int64]*platform.SiteGoogleAnalyticsSummary{}
	}
	if v.SiteGoogleSearchSummaries == nil {
		v.SiteGoogleSearchSummaries = map[int64]*platform.SiteGoogleSearchConsoleSummary{}
	}
	if v.SiteGoogleIntegrations == nil {
		v.SiteGoogleIntegrations = map[int64]map[string]*platform.SiteGoogleIntegration{}
	}
	for _, site := range sites {
		if site != nil && v.SiteGoogleIntegrations[site.ID] == nil {
			v.SiteGoogleIntegrations[site.ID] = map[string]*platform.SiteGoogleIntegration{}
		}
		if site != nil {
			v.PlatformGoogleDefaultURIs[site.ID] = s.defaultGoogleAnalyticsURI(r, site)
		}
	}
	v.PlatformCFStatus = map[int64]string{}
	v.PlatformDeployChips = map[int64]*DeployChip{}
	v.SiteTelegramStatus = map[int64]SiteTelegramStatus{}
	now := time.Now()
	for _, site := range sites {
		if site == nil {
			continue
		}
		var cfStatus *CloudflareStatus
		if rt, ok := s.runtimePool().runtimeByID(site.ID); ok && rt != nil {
			s.setSiteCounts(v, site.ID, rt.Store)
			v.SiteTelegramStatus[site.ID] = s.siteTelegramStatusFor(rt.Store)
			// 每站的 Cloudflare 部署状态文件：CF 形态卡片要「部署中/失败」轮询初值，
			// 「待部署」判定还要看是否成功发布过——所以不分形态都读一次（本地一小文件，量级不变）。
			cfStatus = readCloudflareStatusFile(cloudflareStatusPathForRuntime(rt))
		}
		_, isCF := v.PlatformOfficialURLs[site.ID]
		if isCF && cfStatus != nil {
			v.PlatformCFStatus[site.ID] = strings.TrimSpace(cfStatus.Status)
		}
		contentAt, hasContent := parseChipTime(v.PlatformContentUpdatedAt[site.ID])
		v.PlatformDeployChips[site.ID] = buildDeployChip(v.Admin, deployChipInput{
			Site:        site,
			Domains:     v.PlatformDomains[site.ID],
			CFPublished: isCF,
			CFStatus:    cfStatus,
			ContentAt:   contentAt,
			HasContent:  hasContent,
		}, now)
	}
	s.rnd.Admin(w, "sites", status, v)
}

func mergeGoogleAccounts(lists ...[]*platform.GoogleAccount) []*platform.GoogleAccount {
	seen := map[string]bool{}
	var out []*platform.GoogleAccount
	for _, list := range lists {
		for _, acc := range list {
			if acc == nil {
				continue
			}
			key := strings.TrimSpace(acc.GoogleAccountID)
			if key == "" {
				key = strings.TrimSpace(acc.Email)
			}
			if key == "" {
				key = strings.TrimSpace(acc.Name)
			}
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, acc)
		}
	}
	return out
}

// setSiteCounts fills the platform site card's 语种 / 内容 badges for one site's store:
// enabled-locale count and content rows in the default language (all types, incl. drafts).
func (s *Server) setSiteCounts(v *View, siteID int64, st *store.Store) {
	if v == nil || st == nil {
		return
	}
	if v.PlatformLocaleCounts == nil {
		v.PlatformLocaleCounts = map[int64]int{}
	}
	if v.PlatformContentCounts == nil {
		v.PlatformContentCounts = map[int64]int{}
	}
	if v.PlatformScheduledCounts == nil {
		v.PlatformScheduledCounts = map[int64]int{}
	}
	if v.PlatformContentUpdatedAt == nil {
		v.PlatformContentUpdatedAt = map[int64]string{}
	}
	// 必须带上目标站自己的 custom_locales：平台实例的 i18n 不认别站的自定义语种，
	// 直接 Active 会把它们滤掉（卡片 1 语种、站内概览却有 2 个）。
	locs := s.i18n.ActiveWith(st.Setting("locales"), st.Setting("custom_locales"))
	v.PlatformLocaleCounts[siteID] = len(locs)
	dl := "zh"
	if len(locs) > 0 {
		dl = locs[0].Code
	}
	if n, err := st.CountContent(dl); err == nil {
		v.PlatformContentCounts[siteID] = n
	}
	if n, err := st.CountScheduled(); err == nil {
		v.PlatformScheduledCounts[siteID] = n
	}
	// 对外内容上次更新时间（服务器托管站卡片展示"服务器 · X 前"）。
	if t, ok, err := st.LastPublicUpdate(); err == nil && ok {
		v.PlatformContentUpdatedAt[siteID] = t.UTC().Format(time.RFC3339)
	}
}

func (s *Server) adminCreateSite(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	slug := strings.TrimSpace(r.FormValue("slug"))
	name := strings.TrimSpace(r.FormValue("name"))
	formVals := siteCreateFormVals(r)
	showFormErr := func(msg string) {
		s.sess.setSettingsFlash(sessionToken(r), settingsFlash{SiteFormErr: msg, SiteFormVals: formVals})
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
	}
	dbPath, uploadDir, err := s.newSiteStoragePaths(slug)
	if err != nil {
		showFormErr(err.Error())
		return
	}
	if sites, err := s.platform.Sites(); err != nil {
		s.serverError(w, err)
		return
	} else {
		for _, site := range sites {
			if site != nil && site.Slug == slug {
				showFormErr("站点标记已存在，请换一个。")
				return
			}
		}
	}
	var domainScheme, domainHost string
	if raw := strings.TrimSpace(r.FormValue("domain")); raw != "" {
		domainScheme, domainHost, err = parseSiteDomainInput(raw)
		if err != nil {
			showFormErr(err.Error())
			return
		}
		if s.domainConflictsWithPlatformHost(nil, domainHost) {
			showFormErr("非默认站点不能绑定平台默认 Host")
			return
		}
	}
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		s.serverError(w, err)
		return
	}
	st, err := store.Open(dbPath)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if name != "" {
		if err := st.SetSetting("site.name", name); err != nil {
			_ = st.Close()
			s.serverError(w, err)
			return
		}
	}
	if r.FormValue("seed_mode") == "empty" {
		if err := st.ClearDemoContent(); err != nil {
			_ = st.Close()
			s.serverError(w, err)
			return
		}
		if err := st.EnsureEmptySiteBasePages(); err != nil {
			_ = st.Close()
			s.serverError(w, err)
			return
		}
	}
	// 站型预设：记录 site.kind；工厂站额外启用 product 类型、把「商品」挂进导航，
	// 且在「带演示数据」时写入演示商品（一次性，不做持续约束）。
	if err := applySiteKindPreset(st, s.i18n, r.FormValue("site_kind"), r.FormValue("seed_mode") != "empty"); err != nil {
		_ = st.Close()
		s.serverError(w, err)
		return
	}
	if err := st.Close(); err != nil {
		s.serverError(w, err)
		return
	}
	site, err := s.platform.CreateSite(slug, name, dbPath, uploadDir, r.FormValue("automation") == "1")
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			showFormErr("站点标记已存在，请换一个。")
			return
		}
		s.serverError(w, err)
		return
	}
	if domainHost != "" {
		if err := s.platform.AddSiteDomain(site.ID, domainScheme, domainHost, true, true); err != nil {
			s.serverError(w, err)
			return
		}
	}
	if err := s.reloadRuntimePool(); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
}

func siteCreateFormVals(r *http.Request) map[string]string {
	vals := map[string]string{
		"slug":      strings.TrimSpace(r.FormValue("slug")),
		"name":      strings.TrimSpace(r.FormValue("name")),
		"domain":    strings.TrimSpace(r.FormValue("domain")),
		"seed_mode": strings.TrimSpace(r.FormValue("seed_mode")),
		"site_kind": normalizeSiteKind(r.FormValue("site_kind")),
	}
	if vals["seed_mode"] == "" {
		vals["seed_mode"] = "demo"
	}
	if r.FormValue("automation") == "1" {
		vals["automation"] = "1"
	}
	return vals
}

func (s *Server) adminEnterSite(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	if s.platform != nil {
		site, ok, err := s.platform.GetSite(id)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if !ok || site.Status != "enabled" {
			http.NotFound(w, r)
			return
		}
	} else if id != 1 {
		http.NotFound(w, r)
		return
	}
	if err := s.sess.setCurrentSite(sessionToken(r), id); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) adminDownloadPlatformAutomationSkill(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	id, ok := adminSiteID(w, r)
	if !ok {
		return
	}
	site, found, err := s.platform.GetSite(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	if !site.ManagementAutomationEnabled {
		http.Error(w, "该站点未开启平台自动化入口", http.StatusBadRequest)
		return
	}
	s.writeAutomationSkillZip(w, automationSkillOptions{
		apiBase: s.absForPlatformRequest(r, "/api/platform/v1/sites/"+strconv.FormatInt(id, 10)),
		name:    strings.TrimSpace(site.Name),
	})
}

func (s *Server) adminDownloadPlatformAutomationStarter(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	id, ok := adminSiteID(w, r)
	if !ok {
		return
	}
	site, found, err := s.platform.GetSite(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	if !site.ManagementAutomationEnabled {
		http.Error(w, "该站点未开启平台自动化入口", http.StatusBadRequest)
		return
	}
	s.writeAutomationStarterZip(w, automationSkillOptions{
		apiBase: s.absForPlatformRequest(r, "/api/platform/v1/sites/"+strconv.FormatInt(id, 10)),
		name:    strings.TrimSpace(site.Name),
	})
}

func (s *Server) adminSecurity(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.Redirect(w, r, "/admin/settings/security", http.StatusSeeOther)
		return
	}
	sess, _ := s.currentSession(r)
	v := s.adminView(r, "安全")
	s.platformAuthed(v, sess)
	if f, ok := s.sess.takeSettingsFlash(sessionToken(r)); ok {
		v.Flash = f.Flash
	}
	s.rnd.Admin(w, "security", http.StatusOK, v)
}

func (s *Server) adminPlatformSettings(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
		return
	}
	sess, _ := s.currentSession(r)
	v := s.adminView(r, "平台设置")
	s.platformAuthed(v, sess)
	s.rnd.Admin(w, "platform_settings", http.StatusOK, v)
}

func (s *Server) adminBackups(w http.ResponseWriter, r *http.Request) {
	s.showAdminBackups(w, r, "", "")
}

func (s *Server) showAdminBackups(w http.ResponseWriter, r *http.Request, flash, formErr string) {
	if s.platform == nil {
		http.Redirect(w, r, "/admin/platform/settings", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodGet && flash == "" && formErr == "" {
		if f, ok := s.sess.takeSettingsFlash(sessionToken(r)); ok {
			flash = f.Flash
			formErr = f.FormErr
		}
	}
	sess, _ := s.currentSession(r)
	v := s.adminView(r, "数据备份")
	s.platformAuthed(v, sess)
	v.Flash = flash
	v.FormErr = formErr
	v.BackupDir = s.platformBackupDir()
	v.BackupConfig = backup.ParseConfig(s.platform.Setting(backup.ConfigSettingKey)).Sanitized()
	records, err := backup.ListRecords(v.BackupDir)
	if err != nil {
		s.serverError(w, err)
		return
	}
	v.BackupRecords = records
	status := http.StatusOK
	if formErr != "" {
		status = http.StatusBadRequest
	}
	s.rnd.Admin(w, "backups", status, v)
}

func (s *Server) adminSaveBackupConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	prev := backup.ParseConfig(s.platform.Setting(backup.ConfigSettingKey))
	keep, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("keep_local")))
	cfg := backup.Config{
		AutoSync:        r.FormValue("auto_sync") == "1",
		KeepLocal:       keep,
		Endpoint:        strings.TrimSpace(r.FormValue("endpoint")),
		Region:          strings.TrimSpace(r.FormValue("region")),
		Bucket:          strings.TrimSpace(r.FormValue("bucket")),
		Prefix:          strings.Trim(strings.TrimSpace(r.FormValue("prefix")), "/"),
		AccessKeyID:     strings.TrimSpace(r.FormValue("access_key_id")),
		SecretAccessKey: strings.TrimSpace(r.FormValue("secret_access_key")),
		PathStyle:       r.FormValue("path_style") == "1",
	}
	if cfg.SecretAccessKey == "" {
		cfg.SecretAccessKey = prev.SecretAccessKey
	}
	cfg = backup.NormalizeConfig(cfg)
	data, err := json.Marshal(cfg)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.platform.SetSetting(backup.ConfigSettingKey, string(data)); err != nil {
		s.serverError(w, err)
		return
	}
	if err := backup.ApplyRetention(s.platformBackupDir(), cfg.KeepLocal); err != nil {
		s.serverError(w, err)
		return
	}
	s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: "数据备份设置已保存。"})
	http.Redirect(w, r, "/admin/backups", http.StatusSeeOther)
}

func (s *Server) adminCreateBackup(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	opts, err := s.platformBackupOptions()
	if err != nil {
		s.serverError(w, err)
		return
	}
	rec, err := backup.CreatePlatformBackup(opts)
	if err != nil {
		s.sess.setSettingsFlash(sessionToken(r), settingsFlash{FormErr: "创建备份失败：" + err.Error()})
		http.Redirect(w, r, "/admin/backups", http.StatusSeeOther)
		return
	}
	cfg := backup.ParseConfig(s.platform.Setting(backup.ConfigSettingKey))
	flash := "已创建数据备份：" + rec.Name
	if cfg.AutoSync && cfg.RemoteConfigured() {
		if status, err := s.syncBackupRecord(r.Context(), cfg, rec); err != nil {
			flash += "；远程同步失败：" + err.Error()
			rec.Remote = status
			_ = backup.WriteRecord(s.platformBackupDir(), rec)
		} else {
			flash += "；已同步到 OSS。"
			rec.Remote = status
			_ = backup.WriteRecord(s.platformBackupDir(), rec)
		}
	}
	if err := backup.ApplyRetention(s.platformBackupDir(), cfg.KeepLocal); err != nil {
		s.serverError(w, err)
		return
	}
	s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: flash})
	http.Redirect(w, r, "/admin/backups", http.StatusSeeOther)
}

func (s *Server) adminSyncBackup(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	rec, err := backup.ReadRecord(s.platformBackupDir(), name)
	if err != nil {
		s.sess.setSettingsFlash(sessionToken(r), settingsFlash{FormErr: "读取备份记录失败：" + err.Error()})
		http.Redirect(w, r, "/admin/backups", http.StatusSeeOther)
		return
	}
	cfg := backup.ParseConfig(s.platform.Setting(backup.ConfigSettingKey))
	status, err := s.syncBackupRecord(r.Context(), cfg, rec)
	rec.Remote = status
	_ = backup.WriteRecord(s.platformBackupDir(), rec)
	if err != nil {
		s.sess.setSettingsFlash(sessionToken(r), settingsFlash{FormErr: "远程同步失败：" + err.Error()})
	} else {
		s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: "备份已同步到 OSS：" + status.ObjectKey})
	}
	http.Redirect(w, r, "/admin/backups", http.StatusSeeOther)
}

func (s *Server) adminDownloadBackup(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	if !backup.ValidName(name) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(name)+`"`)
	http.ServeFile(w, r, backup.ZipPath(s.platformBackupDir(), name))
}

func (s *Server) adminDeleteBackup(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	if err := backup.DeleteRecord(s.platformBackupDir(), name); err != nil {
		s.sess.setSettingsFlash(sessionToken(r), settingsFlash{FormErr: "删除备份失败：" + err.Error()})
	} else {
		s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: "备份已删除。"})
	}
	http.Redirect(w, r, "/admin/backups", http.StatusSeeOther)
}

func (s *Server) syncBackupRecord(parent context.Context, cfg backup.Config, rec *backup.BackupRecord) (*backup.RemoteStatus, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	return backup.SyncToS3(ctx, cfg, s.platformBackupDir(), rec)
}

func (s *Server) platformBackupOptions() (backup.Options, error) {
	if s.platform == nil {
		return backup.Options{}, fmt.Errorf("未启用平台模式")
	}
	sites, err := s.platform.Sites()
	if err != nil {
		return backup.Options{}, err
	}
	archived, err := s.platform.ArchivedSites()
	if err != nil {
		return backup.Options{}, err
	}
	return backup.Options{
		BackupDir:    s.platformBackupDir(),
		SystemDBPath: s.platform.Path(),
		Sites:        sites,
		Archived:     archived,
	}, nil
}

func (s *Server) platformBackupDir() string {
	if s.platform != nil {
		if systemPath := strings.TrimSpace(s.platform.Path()); systemPath != "" {
			return filepath.Join(filepath.Dir(systemPath), "backups")
		}
	}
	if s.uploadDir != "" {
		return filepath.Join(filepath.Dir(s.uploadDir), "backups")
	}
	return filepath.Join("data", "backups")
}

func (s *Server) adminUpdates(w http.ResponseWriter, r *http.Request) {
	s.showAdminUpdates(w, r, "", "")
}

func (s *Server) showAdminUpdates(w http.ResponseWriter, r *http.Request, flash, formErr string) {
	if r.Method == http.MethodGet && flash == "" && formErr == "" {
		if f, ok := s.sess.takeSettingsFlash(sessionToken(r)); ok {
			flash = f.Flash
		}
	}
	sess, _ := s.currentSession(r)
	v := s.adminView(r, "系统更新")
	s.platformAuthed(v, sess)
	v.Flash = flash
	v.FormErr = formErr
	v.Update = currentUpdateInfo()
	v.Upgrade = readUpgradeStatus()
	status := http.StatusOK
	if formErr != "" {
		status = http.StatusBadRequest
	}
	s.rnd.Admin(w, "updates", status, v)
}

func (s *Server) adminAdminI18N(w http.ResponseWriter, r *http.Request) {
	s.showAdminI18N(w, r, "", "")
}

func (s *Server) showAdminI18N(w http.ResponseWriter, r *http.Request, flash, formErr string) {
	if r.Method == http.MethodGet && flash == "" && formErr == "" {
		if f, ok := s.sess.takeSettingsFlash(sessionToken(r)); ok {
			flash = f.Flash
		}
	}
	sess, _ := s.currentSession(r)
	v := s.adminView(r, "界面文字")
	s.platformAuthed(v, sess)
	v.Flash = flash
	v.FormErr = formErr
	v.AdminI18NJSON = s.adminI18NRaw(v.AdminLang)
	status := http.StatusOK
	if formErr != "" {
		status = http.StatusBadRequest
	}
	s.rnd.Admin(w, "admin_i18n", status, v)
}

func (s *Server) adminSavePlatformPassword(w http.ResponseWriter, r *http.Request) {
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if msg := s.changeAdminPassword(r); msg != "" {
		s.showPlatformSecurity(w, r, "", msg)
		return
	}
	s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: "密码已更新。"})
	http.Redirect(w, r, "/admin/security", http.StatusSeeOther)
}

func (s *Server) showPlatformSecurity(w http.ResponseWriter, r *http.Request, flash, formErr string) {
	sess, _ := s.currentSession(r)
	v := s.adminView(r, "安全")
	s.platformAuthed(v, sess)
	v.Flash = flash
	v.FormErr = formErr
	s.rnd.Admin(w, "security", http.StatusOK, v)
}

func (s *Server) adminSetDefaultSite(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	id, ok := adminSiteID(w, r)
	if !ok {
		return
	}
	site, found, err := s.platform.GetSite(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	if !site.IsDefault {
		http.Error(w, "其他站点不能设为默认站点", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
}

func (s *Server) adminSetSiteStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	id, ok := adminSiteID(w, r)
	if !ok {
		return
	}
	status := "disabled"
	if r.FormValue("status") == "enabled" {
		status = "enabled"
	}
	if status == "disabled" {
		site, found, err := s.platform.GetSite(id)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if found && site.IsDefault {
			http.Error(w, "默认站点不能关闭", http.StatusBadRequest)
			return
		}
	}
	if err := s.platform.SetSiteStatus(id, status); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.reloadRuntimePool(); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
}

func (s *Server) adminSetSiteAutomation(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	id, ok := adminSiteID(w, r)
	if !ok {
		return
	}
	enabled := r.FormValue("enabled") == "1"
	if !enabled {
		site, found, err := s.platform.GetSite(id)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if found && site.IsDefault {
			http.Error(w, "默认站点不能关闭平台入口", http.StatusBadRequest)
			return
		}
	}
	if err := s.platform.SetSiteAutomation(id, enabled); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.reloadRuntimePool(); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
}

func (s *Server) adminArchiveSite(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	id, ok := adminSiteID(w, r)
	if !ok {
		return
	}
	site, found, err := s.platform.GetSite(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	if site.IsDefault {
		http.Error(w, "默认站点不能归档删除", http.StatusBadRequest)
		return
	}
	if site.Status != "disabled" {
		http.Error(w, "请先关闭站点，再执行归档删除", http.StatusBadRequest)
		return
	}
	sourceRoot, err := standardSiteStorageRoot(site)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if info, err := os.Stat(sourceRoot); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("站点存储目录不是文件夹")
		}
		s.serverError(w, fmt.Errorf("归档源目录不可用: %w", err))
		return
	}
	archivePath, err := s.newArchivedSitePath(site)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		s.serverError(w, err)
		return
	}

	s.detachSiteRuntime(site.ID)
	if err := os.Rename(sourceRoot, archivePath); err != nil {
		_ = s.reloadRuntimePool()
		s.serverError(w, fmt.Errorf("移动站点文件到归档目录失败: %w", err))
		return
	}
	archived, err := s.platform.ArchiveSite(site.ID, archivePath)
	if err != nil {
		_ = os.Rename(archivePath, sourceRoot)
		_ = s.reloadRuntimePool()
		s.serverError(w, err)
		return
	}
	flash := fmt.Sprintf("站点「%s」已归档，数据保留在 %s。", archived.Name, archived.ArchivePath)
	if err := writeArchivedSiteManifest(archived); err != nil {
		flash = fmt.Sprintf("站点「%s」已归档，但写入 archive.json 失败：%s", archived.Name, err)
	}
	if err := s.reloadRuntimePool(); err != nil {
		s.serverError(w, err)
		return
	}
	s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: flash})
	http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
}

func (s *Server) adminArchivedSites(w http.ResponseWriter, r *http.Request) {
	s.showAdminArchivedSites(w, r, "", "")
}

func (s *Server) showAdminArchivedSites(w http.ResponseWriter, r *http.Request, flash, formErr string) {
	if r.Method == http.MethodGet && flash == "" && formErr == "" {
		if f, ok := s.sess.takeSettingsFlash(sessionToken(r)); ok {
			flash = f.Flash
			formErr = f.FormErr
		}
	}
	if s.platform == nil {
		http.Redirect(w, r, "/admin/platform/settings", http.StatusSeeOther)
		return
	}
	sess, _ := s.currentSession(r)
	v := s.adminView(r, "归档站点")
	s.platformAuthed(v, sess)
	v.Flash = flash
	v.FormErr = formErr
	archived, err := s.platform.ArchivedSites()
	if err != nil {
		s.serverError(w, err)
		return
	}
	v.ArchivedSites = archived
	v.ArchivedSiteIcons = map[int64]string{}
	for _, site := range archived {
		if site == nil {
			continue
		}
		if icon := archivedSiteIconDataURL(site); icon != "" {
			v.ArchivedSiteIcons[site.ID] = icon
		}
	}
	status := http.StatusOK
	if formErr != "" {
		status = http.StatusBadRequest
	}
	s.rnd.Admin(w, "archived_sites", status, v)
}

func (s *Server) adminRestoreArchivedSite(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	id, ok := adminSiteID(w, r)
	if !ok {
		return
	}
	archived, found, err := s.platform.GetArchivedSite(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	if strings.TrimSpace(r.FormValue("confirm_slug")) != archived.Slug {
		s.sess.setSettingsFlash(sessionToken(r), settingsFlash{FormErr: "站点标记不匹配，请输入归档站点的标记后再恢复。"})
		http.Redirect(w, r, "/admin/archived-sites", http.StatusSeeOther)
		return
	}
	archivePath, err := safeArchivedSitePath(archived.ArchivePath)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if info, err := os.Stat(archivePath); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("归档目录不是文件夹")
		}
		s.serverError(w, fmt.Errorf("归档目录不可用: %w", err))
		return
	}
	targetRoot, err := standardArchivedSiteStorageRoot(archived)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if _, err := os.Stat(targetRoot); err == nil {
		s.sess.setSettingsFlash(sessionToken(r), settingsFlash{FormErr: fmt.Sprintf("恢复目标目录 %s 已存在，请先检查文件后再恢复。", targetRoot)})
		http.Redirect(w, r, "/admin/archived-sites", http.StatusSeeOther)
		return
	} else if !os.IsNotExist(err) {
		s.serverError(w, err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(targetRoot), 0o755); err != nil {
		s.serverError(w, err)
		return
	}
	if err := os.Rename(archivePath, targetRoot); err != nil {
		s.serverError(w, fmt.Errorf("移动归档目录失败: %w", err))
		return
	}
	restored, err := s.platform.RestoreArchivedSite(archived.ID)
	if err != nil {
		if moveErr := os.Rename(targetRoot, archivePath); moveErr != nil {
			s.serverError(w, fmt.Errorf("恢复站点记录失败: %v；回滚归档目录失败: %w", err, moveErr))
			return
		}
		s.sess.setSettingsFlash(sessionToken(r), settingsFlash{FormErr: fmt.Sprintf("恢复失败：%s", err)})
		http.Redirect(w, r, "/admin/archived-sites", http.StatusSeeOther)
		return
	}
	_ = os.Remove(filepath.Join(targetRoot, "archive.json"))
	if err := s.reloadRuntimePool(); err != nil {
		s.serverError(w, err)
		return
	}
	s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: fmt.Sprintf("站点「%s」已恢复，当前仍为关闭状态。", restored.Name)})
	http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
}

func (s *Server) adminDeleteArchivedSite(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	id, ok := adminSiteID(w, r)
	if !ok {
		return
	}
	archived, found, err := s.platform.GetArchivedSite(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	if strings.TrimSpace(r.FormValue("confirm_slug")) != archived.Slug {
		s.sess.setSettingsFlash(sessionToken(r), settingsFlash{FormErr: "站点标记不匹配，请输入归档站点的标记后再彻底删除。"})
		http.Redirect(w, r, "/admin/archived-sites", http.StatusSeeOther)
		return
	}
	archivePath, err := safeArchivedSitePath(archived.ArchivePath)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := os.RemoveAll(archivePath); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.platform.DeleteArchivedSite(archived.ID); err != nil {
		s.serverError(w, err)
		return
	}
	s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: fmt.Sprintf("归档站点「%s」已彻底删除。", archived.Name)})
	http.Redirect(w, r, "/admin/archived-sites", http.StatusSeeOther)
}

// adminSaveSiteDomains replaces a site's whole domain set from the modal form:
// one 主域名 + an 别名 textarea (one host per line) + a single 301 checkbox.
func (s *Server) adminSaveSiteDomains(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if s.platform == nil {
		http.NotFound(w, r)
		return
	}
	id, ok := adminSiteID(w, r)
	if !ok {
		return
	}
	site, found, err := s.platform.GetSite(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	if site.IsDefault {
		http.Error(w, "默认站点使用当前默认入口，不能绑定域名", http.StatusBadRequest)
		return
	}

	// 校验错误不再回裸文本页（会把用户甩出后台）：
	// 向导走 AJAX（wantsJSON）→ 返回 JSON，就地红字、弹窗不关；
	// 原生提交兜底 → 顶部 Flash 提示 + 留在站点管理页。
	jsonReq := wantsJSON(r)
	failBack := func(msg string) {
		if jsonReq {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": msg})
			return
		}
		s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: "绑定域名未保存：" + msg})
		http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
	}

	primaryRaw := strings.TrimSpace(r.FormValue("primary_domain"))
	aliasRaw := strings.TrimSpace(r.FormValue("alias_domains"))
	redirect := r.FormValue("redirect_aliases") == "1"
	if primaryRaw == "" && aliasRaw != "" {
		failBack("请先填写主域名，再添加别名域名")
		return
	}

	var specs []platform.SiteDomainSpec
	seen := map[string]bool{}
	add := func(raw string, primary bool) error {
		scheme, host, err := parseSiteDomainInput(raw)
		if err != nil {
			return err
		}
		if s.domainConflictsWithPlatformHost(site, host) {
			return fmt.Errorf("非默认站点不能绑定平台默认 Host")
		}
		if seen[host] {
			return nil // 去重：与主域名或已列出的别名重复
		}
		seen[host] = true
		specs = append(specs, platform.SiteDomainSpec{Scheme: scheme, Host: host, Primary: primary, Redirect: !primary && redirect})
		return nil
	}

	if primaryRaw != "" {
		if err := add(primaryRaw, true); err != nil {
			failBack("主域名：" + err.Error())
			return
		}
	}
	for _, line := range strings.Split(aliasRaw, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if err := add(line, false); err != nil {
			failBack("别名域名：" + err.Error())
			return
		}
	}

	if err := s.platform.ReplaceSiteDomains(id, specs); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.reloadRuntimePool(); err != nil {
		s.serverError(w, err)
		return
	}
	unbound := len(specs) == 0
	flashes := []string{} // 保持非 nil：JSON 里恒为数组，前端/测试不用兼容 null
	if unbound {
		flashes = append(flashes, "已解绑全部域名，该站点回到默认入口访问。")
	}
	if msg := s.handleCloudflareDNSFromForm(r, specs); msg != "" {
		flashes = append(flashes, msg)
	}
	if msg := s.applyCaddySites(); msg != "" {
		flashes = append(flashes, msg)
	}
	if jsonReq {
		// 向导「保存并应用」后停留在弹窗里继续做验证：CF/Caddy 的结果消息随 JSON 就地展示，
		// 不塞 session flash（否则会在之后某次导航冒出陈旧横幅）；解绑要跳回站点页，flash 照设。
		if unbound && len(flashes) > 0 {
			s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: strings.Join(flashes, " ")})
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "redirect": "/admin/sites", "messages": flashes, "unbound": unbound})
		return
	}
	if len(flashes) > 0 {
		s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: strings.Join(flashes, " ")})
	}
	http.Redirect(w, r, "/admin/sites", http.StatusSeeOther)
}

// handleCloudflareDNSFromForm saves the platform-level Cloudflare DNS token + server
// IPs from the domain modal, and (when the checkbox is set) upserts grey-cloud A/AAAA
// records pointing the just-saved domains at the server. Returns a flash message.
func (s *Server) handleCloudflareDNSFromForm(r *http.Request, specs []platform.SiteDomainSpec) string {
	if s.platform == nil {
		return ""
	}
	pasted := strings.TrimSpace(r.FormValue("cf_token"))
	ipv4 := strings.TrimSpace(r.FormValue("server_ipv4"))
	ipv6 := strings.TrimSpace(r.FormValue("server_ipv6"))
	wantDNS := r.FormValue("cf_dns") == "1"
	proxied := r.FormValue("cf_proxied") == "1"
	if pasted == "" && ipv4 == "" && ipv6 == "" && !wantDNS {
		return "" // Cloudflare section untouched
	}

	if ipv4 != "" && net.ParseIP(ipv4).To4() == nil {
		return "服务器 IPv4 格式不正确：" + ipv4
	}
	if ipv6 != "" && net.ParseIP(ipv6) == nil {
		return "服务器 IPv6 格式不正确：" + ipv6
	}

	token := strings.TrimSpace(s.platform.Setting(platformCFDNSTokenKey))
	if pasted != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := verifyCloudflareAPITokenLive(ctx, pasted); err != nil {
			return "Cloudflare Token 校验失败：" + err.Error()
		}
		_ = s.platform.SetSetting(platformCFDNSTokenKey, pasted)
		token = pasted
	}
	if ipv4 != "" {
		_ = s.platform.SetSetting(platformServerIPv4Key, ipv4)
	}
	if ipv6 != "" {
		_ = s.platform.SetSetting(platformServerIPv6Key, ipv6)
	}
	cfProxied := "1"
	if !proxied {
		cfProxied = "0"
	}
	_ = s.platform.SetSetting(platformCFProxiedKey, cfProxied)

	if !wantDNS {
		return ""
	}
	if token == "" {
		return "尚未授权 Cloudflare Token，无法自动配置 DNS。"
	}
	if ipv4 == "" {
		ipv4 = strings.TrimSpace(s.platform.Setting(platformServerIPv4Key))
	}
	if ipv6 == "" {
		ipv6 = strings.TrimSpace(s.platform.Setting(platformServerIPv6Key))
	}
	if ipv4 == "" && ipv6 == "" {
		return "请先填写服务器 IPv4（或 IPv6），才能自动配置 DNS。"
	}

	var hosts []string
	for _, sp := range specs {
		hosts = append(hosts, sp.Host)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	results, err := applyCloudflareDNS(ctx, token, ipv4, ipv6, hosts, proxied)
	if err != nil {
		return "读取 Cloudflare 域名失败：" + err.Error()
	}
	var okHosts, failParts []string
	for _, res := range results {
		if res.OK {
			okHosts = append(okHosts, res.Host)
		} else {
			failParts = append(failParts, res.Host+"（"+res.Msg+"）")
		}
	}
	mode := "灰云 · 仅 DNS"
	if proxied {
		mode = "橙云 · 已代理"
	}
	var b strings.Builder
	if len(okHosts) > 0 {
		fmt.Fprintf(&b, "已通过 Cloudflare 把 %s 的 DNS 指向本服务器（%s）。", strings.Join(okHosts, "、"), mode)
	}
	if len(failParts) > 0 {
		fmt.Fprintf(&b, " 未处理：%s。", strings.Join(failParts, "；"))
	}
	return strings.TrimSpace(b.String())
}

func (s *Server) adminSiteUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := adminSiteID(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if !validUploadFilename(name) {
		http.NotFound(w, r)
		return
	}
	uploadDir := strings.TrimSpace(s.uploadDir)
	if s.platform != nil {
		rt, found := s.runtimePool().runtimeByID(id)
		if !found || rt == nil {
			http.NotFound(w, r)
			return
		}
		uploadDir = strings.TrimSpace(rt.UploadDir)
	} else if id != 1 {
		http.NotFound(w, r)
		return
	}
	if uploadDir == "" {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(uploadDir, name)
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", uploadCacheControl)
	http.ServeFile(w, r, full)
}

func adminSiteID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

func (s *Server) newSiteStoragePaths(slug string) (dbPath, uploadDir string, err error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return "", "", fmt.Errorf("站点标记不能为空")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`).MatchString(slug) {
		return "", "", fmt.Errorf("站点标记只能包含小写字母、数字和短横线")
	}
	base := "data"
	if s.platform != nil {
		if site, err := s.platform.DefaultSite(); err == nil && strings.TrimSpace(site.DBPath) != "" {
			base = filepath.Dir(site.DBPath)
		}
	}
	root := filepath.Join(base, "sites", slug)
	return filepath.Join(root, "cms.db"), filepath.Join(root, "uploads"), nil
}

func (s *Server) platformDataDir() string {
	base := "data"
	if s.platform != nil {
		if site, err := s.platform.DefaultSite(); err == nil && strings.TrimSpace(site.DBPath) != "" {
			base = filepath.Dir(site.DBPath)
		}
	}
	return base
}

func standardSiteStorageRoot(site *platform.Site) (string, error) {
	if site == nil {
		return "", fmt.Errorf("站点不存在")
	}
	dbPath := filepath.Clean(strings.TrimSpace(site.DBPath))
	if dbPath == "." || filepath.Base(dbPath) != "cms.db" {
		return "", fmt.Errorf("只能归档标准站点存储目录")
	}
	root := filepath.Dir(dbPath)
	if filepath.Base(root) != site.Slug || filepath.Base(filepath.Dir(root)) != "sites" {
		return "", fmt.Errorf("只能归档 data/sites/{slug} 下的站点")
	}
	if uploadDir := strings.TrimSpace(site.UploadDir); uploadDir != "" {
		rel, err := filepath.Rel(root, filepath.Clean(uploadDir))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			return "", fmt.Errorf("上传目录不在站点存储目录内，不能自动归档")
		}
	}
	return root, nil
}

func standardArchivedSiteStorageRoot(site *platform.ArchivedSite) (string, error) {
	if site == nil {
		return "", fmt.Errorf("归档站点不存在")
	}
	dbPath := filepath.Clean(strings.TrimSpace(site.DBPath))
	if dbPath == "." || filepath.Base(dbPath) != "cms.db" {
		return "", fmt.Errorf("只能恢复标准站点存储目录")
	}
	root := filepath.Dir(dbPath)
	if filepath.Base(root) != site.Slug || filepath.Base(filepath.Dir(root)) != "sites" {
		return "", fmt.Errorf("只能恢复到 data/sites/{slug} 下的站点")
	}
	if uploadDir := strings.TrimSpace(site.UploadDir); uploadDir != "" {
		rel, err := filepath.Rel(root, filepath.Clean(uploadDir))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			return "", fmt.Errorf("上传目录不在站点存储目录内，不能自动恢复")
		}
	}
	return root, nil
}

func (s *Server) newArchivedSitePath(site *platform.Site) (string, error) {
	if site == nil {
		return "", fmt.Errorf("站点不存在")
	}
	base := filepath.Join(s.platformDataDir(), "deleted-sites")
	stamp := time.Now().UTC().Format("20060102150405")
	name := fmt.Sprintf("%s-%d-%s", site.Slug, site.ID, stamp)
	path := filepath.Join(base, name)
	if _, err := os.Stat(path); err == nil {
		path = filepath.Join(base, name+"-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return path, nil
}

func safeArchivedSitePath(path string) (string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." || path == string(os.PathSeparator) {
		return "", fmt.Errorf("归档路径无效")
	}
	if filepath.Base(filepath.Dir(path)) != "deleted-sites" {
		return "", fmt.Errorf("只能删除 deleted-sites 下的归档目录")
	}
	return path, nil
}

func writeArchivedSiteManifest(site *platform.ArchivedSite) error {
	if site == nil || strings.TrimSpace(site.ArchivePath) == "" {
		return nil
	}
	data, err := json.MarshalIndent(site, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(site.ArchivePath, "archive.json"), data, 0o644)
}

func parseSiteDomainInput(raw string) (scheme, host string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("域名不能为空")
	}
	// 错误消息必须带上原始值：别名 textarea 一行出错会整单失败，不指明是哪一行用户没法自查。
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return "", "", fmt.Errorf("域名「%s」格式不正确（示例：www.example.com，每行一个）", raw)
		}
		scheme = u.Scheme
		host = u.Host
	} else {
		scheme = "https"
		host = raw
	}
	host = normalizeRuntimeHost(host)
	if host == "" || strings.ContainsAny(host, "/ \t") {
		return "", "", fmt.Errorf("域名「%s」格式不正确（不能包含空格或路径；多个域名请分行填写）", raw)
	}
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	return scheme, host, nil
}

func (s *Server) domainConflictsWithPlatformHost(site *platform.Site, host string) bool {
	if site != nil && site.IsDefault {
		return false
	}
	return normalizeRuntimeHost(host) != "" && normalizeRuntimeHost(host) == normalizeRuntimeHost(baseURLHost(s.baseURL))
}

func (s *Server) adminPosts(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	lang := s.editLang(r)
	status := adminStatusFilter(r)
	category := adminCategoryFilter(r)
	page := pageParam(r)
	categories, err := s.store.ListCategories(lang, "post")
	if err != nil {
		s.serverError(w, err)
		return
	}
	categoryName := ""
	if category != "" {
		for _, c := range categories {
			if c.Slug == category {
				categoryName = c.Name
				break
			}
		}
	}
	total, err := s.store.CountAdminContentFiltered("post", lang, status, category)
	if err != nil {
		s.serverError(w, err)
		return
	}
	totalPages := ceilDiv(total, adminListPageSize)
	if totalPages > 0 && page > totalPages {
		page = totalPages
	}
	posts, err := s.store.ListAdminContentFiltered("post", lang, status, category, (page-1)*adminListPageSize, adminListPageSize)
	if err != nil {
		s.serverError(w, err)
		return
	}
	v := s.adminView(r, "文章")
	s.authed(v, sess)
	v.AllPosts = posts
	v.Categories = categories
	v.ListTotal = total
	v.StatusFilter = status
	v.CategoryFilter = category
	v.CategoryFilterName = categoryName
	v.AdminListPath = "/admin/posts"
	v.EditLang = lang
	// AI 报废申请「待清理」：条数驱动清空按钮与筛选档；purged 回跳展示清理结果。
	v.DiscardTotal, _ = s.store.CountDiscardedDrafts("post", lang)
	if n := strings.TrimSpace(r.URL.Query().Get("purged")); n != "" {
		if cnt, err := strconv.Atoi(n); err == nil && cnt >= 0 {
			v.Flash = fmt.Sprintf(v.Admin.T("admin.discard.purged", "已清理 %d 篇 AI 弃用草稿。"), cnt)
		}
	}
	setPagination(v, page, totalPages, "/admin/posts")
	s.rnd.Admin(w, "posts", http.StatusOK, v)
}

func overviewStatusCount(counts map[string]store.AdminContentCounts, kind, status string) int {
	c := counts[kind]
	switch status {
	case "":
		return c.Total
	case "published":
		return c.Published
	case "draft":
		return c.Draft
	case "scheduled":
		return c.Scheduled
	default:
		return 0
	}
}

func overviewIssueCount(issues map[string]store.AdminContentIssues, kind, issue string) int {
	item := issues[kind]
	switch issue {
	case "missing_cover":
		return item.MissingCover
	case "missing_category":
		return item.MissingCategory
	case "missing_excerpt":
		return item.MissingExcerpt
	case "missing_meta_desc":
		return item.MissingMetaDesc
	default:
		return 0
	}
}

func (s *Server) adminOverviewStats(lang string, admin *i18n.AdminTr, counts map[string]store.AdminContentCounts) ([]OverviewStat, error) {
	specs := []struct {
		kind  string
		label string
		href  string
		icon  string
	}{
		{"post", admin.T("admin.nav.posts", "文章"), "/admin/posts?lang=" + lang, "posts"},
		{"link", admin.T("admin.nav.links", "链接"), "/admin/links?lang=" + lang, "links"},
		{"page", admin.T("admin.nav.pages", "页面"), "/admin/pages?lang=" + lang, "pages"},
	}
	out := make([]OverviewStat, 0, len(specs))
	for _, spec := range specs {
		c := counts[spec.kind]
		out = append(out, OverviewStat{
			Label: spec.label, Href: spec.href, Icon: spec.icon, Total: c.Total,
			Published: c.Published, Draft: c.Draft, Scheduled: c.Scheduled,
		})
	}
	out = append(out, OverviewStat{
		Label: admin.T("admin.dashboard.languages", "语种"),
		Href:  "/admin/settings/languages",
		Icon:  "languages",
		Total: len(s.locales()),
	})
	return out, nil
}

func (s *Server) adminOverviewTasks(lang string, admin *i18n.AdminTr, counts map[string]store.AdminContentCounts, issues map[string]store.AdminContentIssues) ([]OverviewTask, error) {
	var out []OverviewTask
	addCount := func(kind, status, labelKey, labelFallback, hintKey, hintFallback, href, icon, tone string) error {
		n := overviewStatusCount(counts, kind, status)
		if n > 0 {
			out = append(out, OverviewTask{
				Label: admin.T(labelKey, labelFallback),
				Hint:  admin.T(hintKey, hintFallback),
				Href:  href,
				Icon:  icon,
				Count: n,
				Tone:  tone,
			})
		}
		return nil
	}
	addIssue := func(kind, issue, labelKey, labelFallback, hintKey, hintFallback, href, icon string) error {
		n := overviewIssueCount(issues, kind, issue)
		if n > 0 {
			out = append(out, OverviewTask{
				Label: admin.T(labelKey, labelFallback),
				Hint:  admin.T(hintKey, hintFallback),
				Href:  href,
				Icon:  icon,
				Count: n,
				Tone:  "warn",
			})
		}
		return nil
	}
	postBase := "/admin/posts?lang=" + lang
	linkBase := "/admin/links?lang=" + lang
	if err := addCount("post", "draft", "admin.dashboard.task.post_drafts", "文章草稿", "admin.dashboard.task.post_drafts_hint", "等待审核、补充或发布", postBase+"&status=draft", "posts", "accent"); err != nil {
		return nil, err
	}
	if err := addCount("post", "scheduled", "admin.dashboard.task.post_scheduled", "定时文章", "admin.dashboard.task.post_scheduled_hint", "确认发布时间和内容状态", postBase+"&status=scheduled", "clock", "ok"); err != nil {
		return nil, err
	}
	if err := addCount("link", "draft", "admin.dashboard.task.link_drafts", "链接草稿", "admin.dashboard.task.link_drafts_hint", "等待确认目标网址和详情页", linkBase+"&status=draft", "links", "accent"); err != nil {
		return nil, err
	}
	if err := addCount("link", "scheduled", "admin.dashboard.task.link_scheduled", "定时链接", "admin.dashboard.task.link_scheduled_hint", "确认发布时间和目标网址", linkBase+"&status=scheduled", "clock", "ok"); err != nil {
		return nil, err
	}
	if err := addIssue("post", "missing_cover", "admin.dashboard.task.posts_missing_cover", "文章缺封面", "admin.dashboard.task.missing_cover_hint", "会影响列表、分享和 AI 审稿质量", postBase, "image"); err != nil {
		return nil, err
	}
	if err := addIssue("post", "missing_category", "admin.dashboard.task.posts_missing_category", "文章缺分类", "admin.dashboard.task.missing_category_hint", "会影响分类页和内容筛选", postBase, "folder"); err != nil {
		return nil, err
	}
	if err := addIssue("link", "missing_cover", "admin.dashboard.task.links_missing_cover", "链接缺封面", "admin.dashboard.task.missing_cover_hint", "会影响列表、分享和 AI 审稿质量", linkBase, "image"); err != nil {
		return nil, err
	}
	if err := addIssue("link", "missing_category", "admin.dashboard.task.links_missing_category", "链接缺分类", "admin.dashboard.task.missing_category_hint", "会影响分类页和内容筛选", linkBase, "folder"); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Server) adminOverviewStatus(v *View, keys []*store.AutomationKey) []OverviewStatus {
	active, revoked := 0, 0
	for _, key := range keys {
		if key.RevokedAt.IsZero() {
			active++
		} else {
			revoked++
		}
	}
	automationTone := "ok"
	automationValue := fmt.Sprintf(v.Admin.T("admin.dashboard.status.api_active", "%d 个有效密钥"), active)
	automationHint := v.Admin.T("admin.dashboard.status.api_hint", "最近调用记录见下方")
	if active == 0 {
		automationTone = "warn"
		automationValue = v.Admin.T("admin.dashboard.status.api_empty", "未配置")
		automationHint = v.Admin.T("admin.dashboard.status.api_empty_hint", "创建访问权限后，AI 才能安全接入")
	}
	if revoked > 0 {
		automationHint = fmt.Sprintf(v.Admin.T("admin.dashboard.status.api_revoked_hint", "%d 条已吊销记录保留在设置中"), revoked)
	}
	passwordTone := "ok"
	passwordValue := v.Admin.T("admin.dashboard.status.password_ok", "已处理")
	passwordHint := v.Admin.T("admin.dashboard.status.password_ok_hint", "后台登录密码不是默认值")
	if s.adminPasswordIsDefault() {
		passwordTone = "warn"
		passwordValue = v.Admin.T("admin.dashboard.status.password_warn", "需要处理")
		passwordHint = v.Admin.T("admin.warning.default_password", "当前仍在使用默认密码，建议尽快修改以保证安全。")
	}
	updateValue := v.Admin.T("admin.settings.updates.current_version", "当前版本")
	if v.Update != nil {
		updateValue = v.Update.Current.Version
		if updateValue == "" {
			updateValue = v.Admin.T("admin.settings.updates.dev_build", "开发构建")
		}
	}
	updateHref := "/admin/settings/updates"
	if s.platform != nil {
		updateHref = "/admin/updates"
	}
	return []OverviewStatus{
		{
			Label: v.Admin.T("admin.dashboard.status.api", "自动化接口"),
			Value: automationValue,
			Hint:  automationHint,
			Href:  "/admin/settings/automation",
			Icon:  "bot",
			Tone:  automationTone,
		},
		{
			Label: v.Admin.T("admin.dashboard.status.version", "系统版本"),
			Value: updateValue,
			Hint:  v.Admin.T("admin.dashboard.status.version_hint", "可在系统更新里检查新版本"),
			Href:  updateHref,
			Icon:  "version",
			Tone:  "neutral",
		},
		{
			Label: v.Admin.T("admin.dashboard.status.password", "后台密码"),
			Value: passwordValue,
			Hint:  passwordHint,
			Href:  s.adminPasswordURL(),
			Icon:  "lock",
			Tone:  passwordTone,
		},
	}
}

func (s *Server) adminPasswordURL() string {
	if s.platform != nil {
		return "/admin/security"
	}
	return "/admin/settings/security"
}

func (s *Server) adminVisual(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	lang := s.editLang(r)
	v := s.adminView(r, "可视化编辑")
	s.authed(v, sess)
	v.EditLang = lang
	v.AdminSiteURL = s.adminSiteURL(sess.currentSiteID, lang)
	v.VisualPreviewURL = s.adminSiteVisualPreviewURL(sess.currentSiteID, lang)
	v.VisualFields = s.visualFields(lang, v.Admin)
	v.VisualGroups = visualGroups(v.VisualFields, v.Admin)
	v.VisualHistory = s.visualHistory()
	v.LayoutWidth = normalizeLayoutWidth(s.store.Setting(layoutWidthKey))
	s.rnd.Admin(w, "visual", http.StatusOK, v)
}

func adminUI(admin *i18n.AdminTr, key, fallback string) string {
	if admin != nil {
		return admin.T(key, fallback)
	}
	return fallback
}

func firstAdmin(admins []*i18n.AdminTr) *i18n.AdminTr {
	if len(admins) == 0 {
		return nil
	}
	return admins[0]
}

func (s *Server) visualFields(lang string, admins ...*i18n.AdminTr) []VisualField {
	admin := firstAdmin(admins)
	t := func(key, fallback string) string { return adminUI(admin, key, fallback) }
	st := s.site(lang)
	def := s.defaultLang()
	text := func(group, key, label, value, hint string, multiline bool) VisualField {
		return VisualField{
			Key:       key,
			Label:     label,
			Value:     value,
			Group:     group,
			Kind:      "text",
			Hint:      hint,
			Multiline: multiline,
			Localized: true,
			Inherited: lang != def && strings.TrimSpace(s.store.Setting(key+"::"+lang)) == "",
		}
	}
	image := func(group, key, label, value, hint string) VisualField {
		return VisualField{Key: key, Label: label, Value: value, Group: group, Kind: "image", Hint: hint}
	}
	contextText := func(group, key, label, value, meta, hint string, multiline bool) VisualField {
		return VisualField{
			Key:        key,
			Label:      label,
			Value:      value,
			Meta:       meta,
			Group:      group,
			Kind:       "text",
			Hint:       hint,
			Multiline:  multiline,
			Contextual: true,
			Localized:  true,
		}
	}
	fields := []VisualField{
		text("site", "site.name", t("admin.visual.field.site_name", "站点名称"), st.Name, t("admin.visual.hint.site_name", "显示在页眉、页脚和 SEO 站点名中。"), false),
		text("site", "site.tagline", t("admin.visual.field.tagline", "首页标题标语"), st.Tagline, t("admin.visual.hint.tagline", "会和站点名称组成首页浏览器标题。"), false),
		text("site", "site.description", t("admin.visual.field.description", "首页 SEO 描述"), st.Description, t("admin.visual.hint.description", "用于首页 meta description；Hero 描述在首页文案里单独设置。"), true),
		text("site", "site.keywords", t("admin.visual.field.keywords", "首页 SEO 关键词"), st.Keywords, t("admin.visual.hint.keywords", "用于首页 meta keywords，多个关键词用逗号分隔。"), true),
		image("site", "site.logo", t("admin.visual.field.logo", "站点 Logo"), s.store.Setting("site.logo"), t("admin.visual.hint.logo", "显示在页眉和页脚，留空时使用内置默认 Logo。")),
		image("site", "site.favicon", t("admin.visual.field.favicon", "浏览器图标"), nonEmpty(s.store.Setting("site.favicon"), defaultFaviconPath), t("admin.visual.hint.favicon", "显示在浏览器标签页，建议使用 SVG、PNG 或 ICO。")),
		image("site", "site.share_image", t("admin.visual.field.share_image", "分享图"), nonEmpty(st.ShareImage, defaultShareImageURL), t("admin.visual.hint.share_image", "分享到微信、飞书、X 等平台时默认显示，建议 1200×630。")),
		text("home", "site.hero_eyebrow", t("admin.visual.field.hero_eyebrow", "Hero 眉标"), st.HeroEyebrow, t("admin.visual.hint.hero_eyebrow", "首页主标题上方的小字。"), false),
		text("home", "site.hero_title", t("admin.visual.field.hero_title", "Hero 大标题"), st.HeroTitle, t("admin.visual.hint.hero_title", "首页第一屏最醒目的标题，建议短一点。"), true),
		text("home", "site.hero_description", t("admin.visual.field.hero_description", "Hero 描述"), st.HeroDescription, t("admin.visual.hint.hero_description", "首页大标题下方的说明文案，留空时回退首页 SEO 描述。"), true),
		image("home", "hero.image", t("admin.visual.field.hero_image", "Hero 图片"), st.HeroImage, t("admin.visual.hint.hero_image", "上传后会自动把 Hero 右侧视觉切换为图片模式。")),
		text("home", "home.featured_title", t("admin.visual.field.home_featured", "首页精选标题"), st.HomeFeatured, t("admin.visual.hint.home_featured", "首页精选文章区块标题。"), false),
		text("home", "home.links_title", t("admin.visual.field.home_links", "首页链接标题"), st.HomeLinks, t("admin.visual.hint.home_links", "首页链接区块标题。"), false),
		text("home", "home.latest_title", t("admin.visual.field.home_latest", "首页最新标题"), st.HomeLatest, t("admin.visual.hint.home_latest", "首页最新文章区块标题。"), false),
		text("footer", "site.footer_note", t("admin.visual.field.footer_note", "页脚说明"), st.FooterNote, t("admin.visual.hint.footer_note", "显示在页脚站点名下方。"), true),
	}
	for i, row := range s.menuEditRows() {
		explicit := strings.TrimSpace(row.Labels[lang])
		label := strings.TrimSpace(row.Labels[lang])
		if label == "" {
			label = strings.TrimSpace(row.Labels[def])
		}
		if label == "" {
			label = row.URL
		}
		fields = append(fields, VisualField{
			Key:       "nav." + strconv.Itoa(i),
			Label:     label,
			Value:     label,
			Meta:      row.URL,
			Group:     "nav",
			Kind:      "text",
			Hint:      t("admin.visual.hint.nav", "只修改当前语种的导航名称，导航地址仍在设置里的「导航」维护。"),
			Draggable: true,
			Localized: true,
			Inherited: lang != def && explicit == "",
		})
	}
	if about, _ := s.store.GetPage(lang, "about"); about != nil {
		fields = append(fields,
			contextText("about", "page.about.title", t("admin.visual.field.about_title", "关于标题"), about.Title, "", t("admin.visual.hint.about_title", "关于页面的主标题。"), false),
			contextText("about", "page.about.excerpt", t("admin.visual.field.about_excerpt", "关于摘要"), about.Excerpt, "", t("admin.visual.hint.about_excerpt", "关于页面标题下方的简短说明。"), true),
			contextText("about", "page.about.content", t("admin.visual.field.about_content", "关于正文"), about.Content, "", t("admin.visual.hint.about_content", "支持 Markdown；长内容也可以到「页面」里编辑完整正文。"), true),
		)
	}
	categoryAll := s.archiveConfig(lang, "post")
	fields = append(fields,
		contextText("category", "category_all.title", t("admin.visual.field.category_title", "分类页标题"), categoryAll.Title, categoryAll.Path, t("admin.visual.hint.category_title", "文章分类的全部列表页标题。"), false),
		contextText("category", "category_all.description", t("admin.visual.field.category_description", "分类页描述"), categoryAll.Description, categoryAll.Path, t("admin.visual.hint.category_description", "显示在文章分类全部页标题下方。"), true),
		contextText("categorynav", "category_all.label", t("admin.visual.field.all_button", "全部按钮"), categoryAll.Label, categoryAll.Path, t("admin.visual.hint.category_all_button", "分类导航里“全部”按钮的显示文字。"), false),
	)
	if cats, _ := s.store.ListCategories(lang, "post"); cats != nil {
		for _, c := range cats {
			path := "/category/" + c.Slug
			nameField := contextText("categorynav", "category."+strconv.FormatInt(c.ID, 10)+".name", c.Name, c.Name, path, t("admin.visual.hint.category_nav_name", "分类导航按钮文字；不会改变 URL。"), false)
			nameField.Draggable = true
			fields = append(fields,
				nameField,
				contextText("category", "category."+strconv.FormatInt(c.ID, 10)+".description", fmt.Sprintf(t("admin.visual.field.named_description", "%s 描述"), c.Name), c.Description, path, t("admin.visual.hint.category_item_description", "显示在当前分类页标题下方。"), true),
			)
		}
	}
	linksAll := s.archiveConfig(lang, "link")
	fields = append(fields,
		contextText("linkcat", "links_all.title", t("admin.visual.field.links_title", "链接页标题"), linksAll.Title, linksAll.Path, t("admin.visual.hint.links_title", "链接列表页顶部标题。"), false),
		contextText("linkcat", "links_all.description", t("admin.visual.field.links_description", "链接页描述"), linksAll.Description, linksAll.Path, t("admin.visual.hint.links_description", "显示在链接列表页标题下方。"), true),
		contextText("linkcatnav", "links_all.label", t("admin.visual.field.all_button", "全部按钮"), linksAll.Label, linksAll.Path, t("admin.visual.hint.links_all_button", "链接分类导航里“全部”按钮的显示文字。"), false),
	)
	if cats, _ := s.store.ListCategories(lang, "link"); cats != nil {
		for _, c := range cats {
			path := "/links/cat/" + c.Slug
			nameField := contextText("linkcatnav", "category."+strconv.FormatInt(c.ID, 10)+".name", c.Name, c.Name, path, t("admin.visual.hint.link_category_nav_name", "链接分类导航按钮文字；不会改变 URL。"), false)
			nameField.Draggable = true
			fields = append(fields,
				nameField,
				contextText("linkcat", "category."+strconv.FormatInt(c.ID, 10)+".description", fmt.Sprintf(t("admin.visual.field.named_description", "%s 描述"), c.Name), c.Description, path, t("admin.visual.hint.link_category_item_description", "显示在当前链接分类页标题下方。"), true),
			)
		}
	}
	return fields
}

func visualGroups(fields []VisualField, admins ...*i18n.AdminTr) []VisualGroup {
	var admin *i18n.AdminTr
	if len(admins) > 0 {
		admin = admins[0]
	}
	t := func(key, fallback string) string { return adminUI(admin, key, fallback) }
	titles := []VisualGroup{
		{ID: "site", Title: t("admin.visual.group.site", "站点信息")},
		{ID: "home", Title: t("admin.visual.group.home", "首页内容")},
		{ID: "about", Title: t("admin.visual.group.about", "关于页面")},
		{ID: "nav", Title: t("admin.visual.group.nav", "导航")},
		{ID: "category", Title: t("admin.visual.group.category", "文章分类页")},
		{ID: "categorynav", Title: t("admin.visual.group.categorynav", "文章分类导航")},
		{ID: "linkcat", Title: t("admin.visual.group.linkcat", "链接页")},
		{ID: "linkcatnav", Title: t("admin.visual.group.linkcatnav", "链接分类导航")},
		{ID: "footer", Title: t("admin.visual.group.footer", "页脚")},
	}
	byID := map[string]int{}
	for i := range titles {
		byID[titles[i].ID] = i
	}
	for _, f := range fields {
		i, ok := byID[f.Group]
		if !ok {
			i = len(titles)
			byID[f.Group] = i
			titles = append(titles, VisualGroup{ID: f.Group, Title: f.Group})
		}
		titles[i].Fields = append(titles[i].Fields, f)
	}
	out := titles[:0]
	for _, g := range titles {
		if len(g.Fields) > 0 {
			out = append(out, g)
		}
	}
	return out
}

func (s *Server) adminVisualSave(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	lang := s.editLang(r)
	key := strings.TrimSpace(r.FormValue("key"))
	value := strings.ReplaceAll(strings.TrimSpace(r.FormValue("value")), "\r\n", "\n")

	if strings.HasPrefix(key, "page.about.") {
		page, err := s.store.GetPage(lang, "about")
		if err != nil {
			s.serverError(w, err)
			return
		}
		if page == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "message": "关于页面不存在。"})
			return
		}
		field := strings.TrimPrefix(key, "page.about.")
		old := ""
		switch field {
		case "title":
			if value == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "关于标题不能为空。"})
				return
			}
			old, page.Title = page.Title, value
		case "excerpt":
			old, page.Excerpt = page.Excerpt, value
		case "content":
			old, page.Content = page.Content, value
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "这个关于页字段暂不支持可视化编辑。"})
			return
		}
		if err := s.store.UpdatePost(page); err != nil {
			s.serverError(w, err)
			return
		}
		h := s.pushVisualHistory(VisualLog{
			Key:   key,
			Label: visualFieldLabel(s.visualFields(lang), key),
			Lang:  lang,
			Kind:  "text",
			Old:   old,
			New:   value,
		})
		s.clearGeneratedCaches()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "已保存。", "history": h})
		return
	}

	if kind, field, ok := archiveVisualField(key); ok {
		if (field == "title" || field == "label") && value == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "这个字段不能为空。"})
			return
		}
		all := s.archiveConfig(lang, kind)
		old := ""
		switch field {
		case "title":
			old = all.Title
		case "label":
			old = all.Label
		case "description":
			old = all.Description
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "这个字段暂不支持可视化编辑。"})
			return
		}
		if err := s.store.SetSetting(s.archiveStoreKey(kind, field, lang), value); err != nil {
			s.serverError(w, err)
			return
		}
		h := s.pushVisualHistory(VisualLog{
			Key:   key,
			Label: visualFieldLabel(s.visualFields(lang), key),
			Lang:  lang,
			Kind:  "text",
			Old:   old,
			New:   value,
		})
		s.clearGeneratedCaches()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "已保存。", "history": h})
		return
	}

	if strings.HasPrefix(key, "category.") {
		parts := strings.Split(key, ".")
		if len(parts) != 3 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "分类字段无效。"})
			return
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || id <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "分类项无效。"})
			return
		}
		c, err := s.store.GetCategoryByID(id)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if c == nil || c.Lang != lang {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "message": "分类不存在。"})
			return
		}
		old := ""
		switch parts[2] {
		case "name":
			if value == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "分类名称不能为空。"})
				return
			}
			old, c.Name = c.Name, value
		case "description":
			old, c.Description = c.Description, value
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "这个分类字段暂不支持可视化编辑。"})
			return
		}
		if err := s.store.UpdateCategory(c); err != nil {
			s.serverError(w, err)
			return
		}
		h := s.pushVisualHistory(VisualLog{
			Key:   key,
			Label: visualFieldLabel(s.visualFields(lang), key),
			Lang:  lang,
			Kind:  "text",
			Old:   old,
			New:   value,
		})
		s.clearGeneratedCaches()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "已保存。", "history": h})
		return
	}

	if strings.HasPrefix(key, "nav.") {
		idx, err := strconv.Atoi(strings.TrimPrefix(key, "nav."))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "导航项无效。"})
			return
		}
		rows := s.menuEditRows()
		if idx < 0 || idx >= len(rows) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "导航项不存在。"})
			return
		}
		if rows[idx].Labels == nil {
			rows[idx].Labels = map[string]string{}
		}
		old := rows[idx].Labels[lang]
		rows[idx].Labels[lang] = value
		b, _ := json.Marshal(rows)
		_ = s.store.SetSetting("nav_menu", string(b))
		h := s.pushVisualHistory(VisualLog{
			Key:   key,
			Label: visualFieldLabel(s.visualFields(lang), key),
			Lang:  lang,
			Kind:  "text",
			Old:   old,
			New:   value,
		})
		s.clearGeneratedCaches()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "已保存。", "history": h})
		return
	}

	if !visualSettingAllowed(key) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "这个字段暂不支持可视化编辑。"})
		return
	}
	if key == "site.name" && value == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "站点名称不能为空。"})
		return
	}
	if key == layoutWidthKey {
		value = normalizeLayoutWidth(value)
	}
	storeKey := s.visualStoreKey(key, lang)
	old := s.store.Setting(storeKey)
	_ = s.store.SetSetting(storeKey, value)
	if key == "hero.image" {
		heroVisualKey := s.copyKey("hero.visual", lang)
		if value != "" {
			_ = s.store.SetSetting(heroVisualKey, "image")
		} else if s.store.Setting(heroVisualKey) == "image" {
			_ = s.store.SetSetting(heroVisualKey, "")
		}
	}
	h := s.pushVisualHistory(VisualLog{
		Key:   key,
		Label: visualFieldLabel(s.visualFields(lang), key),
		Lang:  lang,
		Kind:  visualFieldKind(s.visualFields(lang), key),
		Old:   old,
		New:   value,
	})
	s.clearGeneratedCaches()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "已保存。", "history": h})
}

func (s *Server) adminVisualUndo(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	history := s.visualHistory()
	if id == "" && len(history) > 0 {
		id = history[0].ID
	}
	var item VisualLog
	var kept []VisualLog
	found := false
	for _, h := range history {
		if h.ID == id {
			item = h
			found = true
			continue
		}
		kept = append(kept, h)
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "message": "没有找到可撤回的修改。"})
		return
	}
	if err := s.restoreVisualValue(item); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	s.saveVisualHistory(kept)
	s.clearGeneratedCaches()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "已撤回。", "key": item.Key, "value": item.Old})
}

func (s *Server) adminVisualNavReorder(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	rows := s.menuEditRows()
	if len(rows) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "没有可排序的导航。"})
		return
	}
	keys := r.Form["keys"]
	if len(keys) == 0 {
		keys = strings.Split(r.FormValue("order"), ",")
	}
	if len(keys) != len(rows) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "导航顺序不完整。"})
		return
	}
	used := map[int]bool{}
	next := make([]MenuRow, 0, len(rows))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if !strings.HasPrefix(key, "nav.") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "导航项无效。"})
			return
		}
		idx, err := strconv.Atoi(strings.TrimPrefix(key, "nav."))
		if err != nil || idx < 0 || idx >= len(rows) || used[idx] {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "导航项无效。"})
			return
		}
		used[idx] = true
		next = append(next, rows[idx])
	}
	b, _ := json.Marshal(next)
	_ = s.store.SetSetting("nav_menu", string(b))
	s.clearGeneratedCaches()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "导航顺序已保存。"})
}

func (s *Server) adminVisualCategoryReorder(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	lang := s.editLang(r)
	group := strings.TrimSpace(r.FormValue("group"))
	kind := ""
	switch group {
	case "categorynav":
		kind = "post"
	case "linkcatnav":
		kind = "link"
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "分类分组无效。"})
		return
	}
	cats, err := s.store.ListCategories(lang, kind)
	if err != nil {
		s.serverError(w, err)
		return
	}
	keys := r.Form["keys"]
	if len(keys) == 0 {
		keys = strings.Split(r.FormValue("order"), ",")
	}
	if len(keys) != len(cats) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "分类顺序不完整。"})
		return
	}
	allowed := map[int64]bool{}
	for _, c := range cats {
		allowed[c.ID] = true
	}
	used := map[int64]bool{}
	ids := make([]int64, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if !strings.HasPrefix(key, "category.") || !strings.HasSuffix(key, ".name") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "分类项无效。"})
			return
		}
		parts := strings.Split(key, ".")
		if len(parts) != 3 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "分类项无效。"})
			return
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || id <= 0 || !allowed[id] || used[id] {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "分类项无效。"})
			return
		}
		used[id] = true
		ids = append(ids, id)
	}
	if err := s.store.ReorderCategories(ids); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "分类顺序已保存。"})
}

func visualSettingAllowed(key string) bool {
	switch key {
	case "site.name", "site.tagline", "site.description", "site.keywords", "site.hero_eyebrow", "site.hero_title", "site.hero_description",
		"home.featured_title", "home.links_title", "home.latest_title", "site.footer_note",
		"site.logo", "site.favicon", "site.share_image", "hero.image", layoutWidthKey:
		return true
	default:
		return false
	}
}

func (s *Server) visualStoreKey(key, lang string) string {
	switch key {
	case "site.logo":
		return "site.logo"
	case "site.favicon":
		return "site.favicon"
	case "site.share_image":
		return s.copyKey("site.share_image", lang)
	case "hero.image":
		return s.copyKey("hero.image", lang)
	case layoutWidthKey:
		return layoutWidthKey
	default:
		return s.copyKey(key, lang)
	}
}

func archiveVisualField(key string) (kind, field string, ok bool) {
	switch {
	case strings.HasPrefix(key, "category_all."):
		return "post", strings.TrimPrefix(key, "category_all."), true
	case strings.HasPrefix(key, "links_all."):
		return "link", strings.TrimPrefix(key, "links_all."), true
	default:
		return "", "", false
	}
}

func visualFieldLabel(fields []VisualField, key string) string {
	if key == layoutWidthKey {
		return "页面宽度"
	}
	for _, f := range fields {
		if f.Key == key {
			return f.Label
		}
	}
	return key
}

func visualFieldKind(fields []VisualField, key string) string {
	for _, f := range fields {
		if f.Key == key && f.Kind != "" {
			return f.Kind
		}
	}
	return "text"
}

func (s *Server) visualHistory() []VisualLog {
	var out []VisualLog
	_ = json.Unmarshal([]byte(s.store.Setting("visual.history")), &out)
	return out
}

func (s *Server) saveVisualHistory(items []VisualLog) {
	if len(items) > 20 {
		items = items[:20]
	}
	b, _ := json.Marshal(items)
	_ = s.store.SetSetting("visual.history", string(b))
}

func (s *Server) pushVisualHistory(h VisualLog) VisualLog {
	if h.Old == h.New {
		return h
	}
	h.ID = strconv.FormatInt(time.Now().UnixNano(), 36)
	h.At = time.Now().Format("2006-01-02 15:04")
	items := append([]VisualLog{h}, s.visualHistory()...)
	s.saveVisualHistory(items)
	return h
}

func (s *Server) restoreVisualValue(h VisualLog) error {
	if strings.HasPrefix(h.Key, "page.about.") {
		page, err := s.store.GetPage(h.Lang, "about")
		if err != nil {
			return err
		}
		if page == nil {
			return fmt.Errorf("关于页面不存在")
		}
		switch strings.TrimPrefix(h.Key, "page.about.") {
		case "title":
			page.Title = h.Old
		case "excerpt":
			page.Excerpt = h.Old
		case "content":
			page.Content = h.Old
		default:
			return fmt.Errorf("这个关于页字段暂不支持撤回")
		}
		return s.store.UpdatePost(page)
	}
	if kind, field, ok := archiveVisualField(h.Key); ok {
		switch field {
		case "title", "label", "description":
			return s.store.SetSetting(s.archiveStoreKey(kind, field, h.Lang), h.Old)
		default:
			return fmt.Errorf("这个字段暂不支持撤回")
		}
	}
	if strings.HasPrefix(h.Key, "category.") {
		parts := strings.Split(h.Key, ".")
		if len(parts) != 3 {
			return fmt.Errorf("分类字段无效")
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || id <= 0 {
			return fmt.Errorf("分类项无效")
		}
		c, err := s.store.GetCategoryByID(id)
		if err != nil {
			return err
		}
		if c == nil || c.Lang != h.Lang {
			return fmt.Errorf("分类不存在")
		}
		switch parts[2] {
		case "name":
			c.Name = h.Old
		case "description":
			c.Description = h.Old
		default:
			return fmt.Errorf("这个分类字段暂不支持撤回")
		}
		return s.store.UpdateCategory(c)
	}
	if strings.HasPrefix(h.Key, "nav.") {
		idx, err := strconv.Atoi(strings.TrimPrefix(h.Key, "nav."))
		if err != nil {
			return fmt.Errorf("导航项无效")
		}
		rows := s.menuEditRows()
		if idx < 0 || idx >= len(rows) {
			return fmt.Errorf("导航项不存在")
		}
		if rows[idx].Labels == nil {
			rows[idx].Labels = map[string]string{}
		}
		rows[idx].Labels[h.Lang] = h.Old
		b, _ := json.Marshal(rows)
		return s.store.SetSetting("nav_menu", string(b))
	}
	if !visualSettingAllowed(h.Key) {
		return fmt.Errorf("这个字段暂不支持撤回")
	}
	key := s.visualStoreKey(h.Key, h.Lang)
	if err := s.store.SetSetting(key, h.Old); err != nil {
		return err
	}
	if h.Key == "hero.image" {
		heroVisualKey := s.copyKey("hero.visual", h.Lang)
		if h.Old != "" {
			_ = s.store.SetSetting(heroVisualKey, "image")
		} else if s.store.Setting(heroVisualKey) == "image" {
			_ = s.store.SetSetting(heroVisualKey, "")
		}
	}
	return nil
}

func (s *Server) adminNew(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	s.showEdit(w, r, sess, &store.Post{Status: "draft", Lang: s.editLang(r)}, "", "")
}

func (s *Server) adminPostPreview(w http.ResponseWriter, r *http.Request) {
	s.adminContentPreview(w, r, "post")
}

func (s *Server) adminLinkPreview(w http.ResponseWriter, r *http.Request) {
	s.adminContentPreview(w, r, "link")
}

func (s *Server) adminContentPreview(w http.ResponseWriter, r *http.Request, typ string) {
	p, _ := s.store.GetPostByID(atoi64(r.PathValue("id")))
	if p == nil || p.Type != typ {
		s.notFound(w, r)
		return
	}
	if sess, ok := s.currentSession(r); ok {
		if prefix := s.adminSitePreviewPrefix(sess.currentSiteID); prefix != "" {
			ctx := withPreviewRoutePrefix(withPreviewNoindex(r.Context()), prefix)
			r = r.Clone(ctx)
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
			w.Header().Set("Cache-Control", "no-store")
		}
	}
	s.renderContentPreviewPage(w, r, p, typ)
}

func (s *Server) frontendPreviewContent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")

	collection := r.PathValue("collection")
	kind, ok := apiContentKind(collection)
	if !ok || (collection != "posts" && collection != "links") {
		s.notFound(w, r)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.notFound(w, r)
		return
	}
	claims, state := s.verifyFrontendPreviewToken(strings.TrimSpace(r.URL.Query().Get("token")))
	if state == "expired" {
		http.Error(w, "预览链接已过期，请重新生成。", http.StatusGone)
		return
	}
	if state != "" || claims.Collection != collection || claims.ID != id {
		s.notFound(w, r)
		return
	}
	p, err := s.store.GetPostByID(id)
	if err != nil {
		http.Error(w, "读取预览内容失败。", http.StatusInternalServerError)
		return
	}
	if p == nil || p.Type != kind {
		s.notFound(w, r)
		return
	}
	if claims.Revision != "" {
		if claims.Revision != previewRevision(p) {
			http.Error(w, "内容已更新，请重新生成预览链接。", http.StatusGone)
			return
		}
	} else if claims.Updated != previewUpdatedUnix(p) {
		http.Error(w, "内容已更新，请重新生成预览链接。", http.StatusGone)
		return
	}
	s.renderContentPreviewPage(w, r, p, kind)
}

func (s *Server) renderContentPreviewPage(w http.ResponseWriter, r *http.Request, p *store.Post, typ string) {
	preview := *p
	if preview.PublishedAt.IsZero() {
		preview.PublishedAt = preview.UpdatedAt
		if preview.PublishedAt.IsZero() {
			preview.PublishedAt = preview.CreatedAt
		}
	}
	s.fillDefaultAuthor(&preview)
	p = &preview

	nav, tpl := "", "article"
	if typ == "link" {
		nav, tpl = "links", "link"
	}
	v := s.viewForLang(r, p.Lang, nav)
	switch typ {
	case "link":
		v.SEO = v.Site.Link(p)
	default:
		v.SEO = v.Site.Article(p)
	}
	v.SEO.Robots = "noindex, nofollow"
	v.SEO.Alternates = nil
	v.Site.InjectHead = ""
	v.Site.InjectBody = ""
	v.Post = p
	v.ContentHTML, v.TOC = s.renderedContent(p)
	s.rnd.Public(w, tpl, http.StatusOK, v)
}

func (s *Server) adminEdit(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	p, _ := s.store.GetPostByID(atoi64(r.PathValue("id")))
	if p == nil {
		s.notFound(w, r)
		return
	}
	flash := ""
	if r.URL.Query().Get("saved") == "1" {
		flash = "文章已保存。"
	}
	if r.URL.Query().Get("restored") == "1" {
		flash = "已恢复到所选历史版本。"
	}
	s.showEdit(w, r, sess, p, flash, "")
}

func (s *Server) showEdit(w http.ResponseWriter, r *http.Request, sess session, e *store.Post, flash, formErr string) {
	v := s.adminView(r, "编辑")
	s.authed(v, sess)
	v.Edit = e
	v.IsPage = e.Type == "page"
	v.IsLink = e.Type == "link"
	catKind := "post"
	switch e.Type {
	case "page":
		v.EditBase, v.EditListURL, v.EditTypeLabel = "pages", "/admin/pages", v.Admin.T("admin.edit.type_page", "页面")
	case "link":
		v.EditBase, v.EditListURL, v.EditTypeLabel, catKind = "links", "/admin/links", v.Admin.T("admin.edit.type_link", "链接"), "link"
	default:
		v.EditBase, v.EditListURL, v.EditTypeLabel = "posts", "/admin/posts", v.Admin.T("admin.edit.type_post", "文章")
	}
	title := fmt.Sprintf(v.Admin.T("admin.edit.title_new", "新建%s"), v.EditTypeLabel)
	if e.ID != 0 {
		title = fmt.Sprintf(v.Admin.T("admin.edit.title_edit", "编辑%s"), v.EditTypeLabel)
	}
	v.SEO.Title = title + " — " + v.Site.Name + " " + v.Admin.T("admin.brand.suffix", "后台")
	switch flash {
	case "文章已保存。":
		flash = v.Admin.T("admin.edit.saved_post", "文章已保存。")
	case "页面已保存。":
		flash = v.Admin.T("admin.edit.saved_page", "页面已保存。")
	case "链接已保存。":
		flash = v.Admin.T("admin.edit.saved_link", "链接已保存。")
	case "已恢复到所选历史版本。":
		flash = v.Admin.T("admin.edit.revision_restored", "已恢复到所选历史版本。")
	}
	if formErr == "标题不能为空。" {
		formErr = v.Admin.T("admin.edit.title_required", "标题不能为空。")
	}
	v.Flash = flash
	v.FormErr = formErr
	lang := e.Lang
	if lang == "" || !s.langEnabled(lang) {
		lang = s.defaultLang()
	}
	v.EditLang = lang
	if !v.IsPage {
		kind := "post"
		if v.IsLink {
			kind = "link"
		}
		v.DefaultAuthor = s.defaultContentAuthor(kind, lang)
	}
	v.Categories, _ = s.store.ListCategories(lang, catKind)
	if e.TransGroup != "" {
		v.Trans, _ = s.store.TranslationsAll(e.TransGroup, e.ID)
	}
	if e.ID != 0 {
		v.Revisions = s.revisionViews(e.ID)
	}
	status := http.StatusOK
	if formErr != "" {
		status = http.StatusBadRequest
	}
	s.rnd.Admin(w, "edit", status, v)
}

// adminRelink 后台"重连互译组"：表单填目标文章 ID 或 trans_group，校验后改本文 trans_group，
// 回到编辑页显示结果。复用 relinkPost 的同一套校验。
func (s *Server) adminRelink(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	id := atoi64(r.PathValue("id"))
	p, _ := s.store.GetPostByID(id)
	if p == nil {
		s.notFound(w, r)
		return
	}
	target := strings.TrimSpace(r.FormValue("relink_target"))
	group := target
	if tid := atoi64(target); tid > 0 { // 填的是文章 ID → 取它所在的组
		if tp, _ := s.store.GetPostByID(tid); tp != nil {
			group = tp.TransGroup
		}
	}
	flash := "已重连互译组。"
	if target == "" {
		flash = "重连失败：请填目标文章 ID 或 trans_group。"
	} else if msg := s.relinkPost(p, group, store.PostRevisionSourceAdmin); msg != "" {
		flash = "重连失败：" + msg
	} else {
		s.clearGeneratedCaches()
	}
	fresh, _ := s.store.GetPostByID(id)
	if fresh == nil {
		fresh = p
	}
	s.showEdit(w, r, sess, fresh, flash, "")
}

func (s *Server) adminCreate(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	lang := s.editLang(r)
	p, formErr := postFromForm(r, 0, lang)
	if formErr != "" {
		s.showEdit(w, r, sess, p, "", formErr)
		return
	}
	s.fillDefaultAuthor(p)
	p.Slug = s.uniqueSlug(lang, p.Slug, 0)
	id, err := s.store.CreatePost(p)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	p.ID = id
	s.firePublishHooks(r, p)
	http.Redirect(w, r, fmt.Sprintf("/admin/posts/%d/edit?saved=1", id), http.StatusSeeOther)
}

func (s *Server) adminUpdate(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	id := atoi64(r.PathValue("id"))
	existing, _ := s.store.GetPostByID(id)
	if existing == nil {
		s.notFound(w, r)
		return
	}
	p, formErr := postFromForm(r, id, existing.Lang)
	if formErr != "" {
		p.TransGroup = existing.TransGroup
		s.showEdit(w, r, sess, p, "", formErr)
		return
	}
	p.CreatedAt = existing.CreatedAt
	p.Featured = existing.Featured     // 置顶通过单独入口切换，编辑保存时保留
	p.TransGroup = existing.TransGroup // 互译关联固定，编辑保存时保留
	if p.PublishedAt.IsZero() {        // 表单未指定发布时间则沿用原值
		p.PublishedAt = existing.PublishedAt
	}
	preserveSEOOverrides(r, p, existing)
	s.fillDefaultAuthor(p)
	p.Slug = s.uniqueSlug(existing.Lang, p.Slug, id)
	if err := s.store.UpdatePost(p); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	s.firePublishHooks(r, p)
	http.Redirect(w, r, fmt.Sprintf("/admin/posts/%d/edit?saved=1", id), http.StatusSeeOther)
}

func (s *Server) adminDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if err := s.store.DeletePost(atoi64(r.PathValue("id"))); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	http.Redirect(w, r, s.adminListRedirect("/admin/posts", r), http.StatusSeeOther)
}

func (s *Server) adminPin(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	_ = s.store.SetFeatured(atoi64(r.PathValue("id")), r.FormValue("on") == "1")
	s.clearGeneratedCaches()
	http.Redirect(w, r, s.adminListRedirect("/admin/posts", r), http.StatusSeeOther)
}

func (s *Server) adminPostStatus(w http.ResponseWriter, r *http.Request) {
	s.adminContentStatus(w, r, "post", "/admin/posts")
}

// adminTranslate 为某篇内容创建/跳转到指定语种的互译版本（共享 trans_group）。
func (s *Server) adminTranslate(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	src, _ := s.store.GetPostByID(atoi64(r.PathValue("id")))
	if src == nil {
		s.notFound(w, r)
		return
	}
	editPath := func(id int64) string {
		switch src.Type {
		case "page":
			return fmt.Sprintf("/admin/pages/%d/edit", id)
		case "link":
			return fmt.Sprintf("/admin/links/%d/edit", id)
		}
		return fmt.Sprintf("/admin/posts/%d/edit", id)
	}
	target := strings.TrimSpace(r.FormValue("lang"))
	if !s.langEnabled(target) || target == src.Lang {
		http.Redirect(w, r, editPath(src.ID), http.StatusSeeOther)
		return
	}
	// 已存在该语种版本 → 直接跳过去
	if trs, _ := s.store.TranslationsAll(src.TransGroup, 0); trs != nil {
		for _, t := range trs {
			if t.Lang == target {
				http.Redirect(w, r, editPath(t.ID), http.StatusSeeOther)
				return
			}
		}
	}
	np := &store.Post{
		Type: src.Type, Title: src.Title, Excerpt: src.Excerpt, Content: src.Content,
		MetaDesc: src.MetaDesc, Keywords: src.Keywords, CoverImage: src.CoverImage, Author: src.Author,
		Status: "draft", EditorMode: src.EditorMode, Lang: target, TransGroup: src.TransGroup, LinkURL: src.LinkURL,
	}
	np.Slug = s.uniqueSlug(target, src.Slug, 0)
	// 分类映射到目标语种的对应分类（若存在互译关联）
	if src.CategoryID.Valid {
		if sc, _ := s.store.GetCategoryByID(src.CategoryID.Int64); sc != nil {
			if ct, _ := s.store.CategoryTranslations(sc.TransGroup); ct != nil {
				for _, c := range ct {
					if c.Lang == target {
						np.CategoryID = sql.NullInt64{Int64: c.ID, Valid: true}
					}
				}
			}
		}
	}
	id, err := s.store.CreatePost(np)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	http.Redirect(w, r, editPath(id), http.StatusSeeOther)
}

// ---------- 链接管理（type=link）----------

func (s *Server) adminLinks(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	lang := s.editLang(r)
	status := adminStatusFilter(r)
	category := adminCategoryFilter(r)
	page := pageParam(r)
	categories, err := s.store.ListCategories(lang, "link")
	if err != nil {
		s.serverError(w, err)
		return
	}
	categoryName := ""
	if category != "" {
		for _, c := range categories {
			if c.Slug == category {
				categoryName = c.Name
				break
			}
		}
	}
	total, err := s.store.CountAdminContentFiltered("link", lang, status, category)
	if err != nil {
		s.serverError(w, err)
		return
	}
	totalPages := ceilDiv(total, adminListPageSize)
	if totalPages > 0 && page > totalPages {
		page = totalPages
	}
	links, err := s.store.ListAdminContentFiltered("link", lang, status, category, (page-1)*adminListPageSize, adminListPageSize)
	if err != nil {
		s.serverError(w, err)
		return
	}
	v := s.adminView(r, "链接")
	s.authed(v, sess)
	v.AllPosts = links
	v.Categories = categories
	v.ListTotal = total
	v.StatusFilter = status
	v.CategoryFilter = category
	v.CategoryFilterName = categoryName
	v.AdminListPath = "/admin/links"
	v.EditLang = lang
	setPagination(v, page, totalPages, "/admin/links")
	s.rnd.Admin(w, "links", http.StatusOK, v)
}

func (s *Server) adminLinkNew(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	s.showEdit(w, r, sess, &store.Post{Type: "link", Status: "draft", Lang: s.editLang(r)}, "", "")
}

func (s *Server) adminLinkEdit(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	p, _ := s.store.GetPostByID(atoi64(r.PathValue("id")))
	if p == nil || p.Type != "link" {
		s.notFound(w, r)
		return
	}
	flash := ""
	if r.URL.Query().Get("saved") == "1" {
		flash = "链接已保存。"
	}
	s.showEdit(w, r, sess, p, flash, "")
}

func (s *Server) adminLinkCreate(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	lang := s.editLang(r)
	p, formErr := postFromForm(r, 0, lang)
	p.Type = "link"
	if formErr != "" {
		s.showEdit(w, r, sess, p, "", formErr)
		return
	}
	s.fillDefaultAuthor(p)
	p.Slug = s.uniqueSlug(lang, p.Slug, 0)
	id, err := s.store.CreatePost(p)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	p.ID = id
	s.firePublishHooks(r, p)
	http.Redirect(w, r, fmt.Sprintf("/admin/links/%d/edit?saved=1", id), http.StatusSeeOther)
}

func (s *Server) adminLinkUpdate(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	id := atoi64(r.PathValue("id"))
	existing, _ := s.store.GetPostByID(id)
	if existing == nil || existing.Type != "link" {
		s.notFound(w, r)
		return
	}
	p, formErr := postFromForm(r, id, existing.Lang)
	p.Type = "link"
	if formErr != "" {
		p.TransGroup = existing.TransGroup
		s.showEdit(w, r, sess, p, "", formErr)
		return
	}
	p.CreatedAt = existing.CreatedAt
	p.Featured = existing.Featured
	p.TransGroup = existing.TransGroup
	if p.PublishedAt.IsZero() {
		p.PublishedAt = existing.PublishedAt
	}
	preserveSEOOverrides(r, p, existing)
	s.fillDefaultAuthor(p)
	p.Slug = s.uniqueSlug(existing.Lang, p.Slug, id)
	if err := s.store.UpdatePost(p); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	s.firePublishHooks(r, p)
	http.Redirect(w, r, fmt.Sprintf("/admin/links/%d/edit?saved=1", id), http.StatusSeeOther)
}

func (s *Server) adminLinkDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	_ = s.store.DeletePost(atoi64(r.PathValue("id")))
	s.clearGeneratedCaches()
	http.Redirect(w, r, s.adminListRedirect("/admin/links", r), http.StatusSeeOther)
}

func (s *Server) adminLinkPin(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	_ = s.store.SetFeatured(atoi64(r.PathValue("id")), r.FormValue("on") == "1")
	s.clearGeneratedCaches()
	http.Redirect(w, r, s.adminListRedirect("/admin/links", r), http.StatusSeeOther)
}

func (s *Server) adminLinkStatus(w http.ResponseWriter, r *http.Request) {
	s.adminContentStatus(w, r, "link", "/admin/links")
}

func (s *Server) adminContentStatus(w http.ResponseWriter, r *http.Request, kind, listPath string) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	id := atoi64(r.PathValue("id"))
	p, err := s.store.GetPostByID(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if p == nil || p.Type != kind {
		s.notFound(w, r)
		return
	}
	target := strings.TrimSpace(r.FormValue("target_status"))
	now := time.Now()
	switch target {
	case "published":
		p.Status = "published"
		if p.PublishedAt.IsZero() || p.PublishedAt.After(now) {
			p.PublishedAt = now
		}
	case "draft":
		p.Status = "draft"
	case "scheduled":
		if p.PublishedAt.IsZero() || !p.PublishedAt.After(now) {
			// 没有未来的发布时间：跳编辑页让用户先设 publish_at（按类型回跳，扩展类型走 /admin/ext/…）。
			http.Redirect(w, r, adminEditURLForPost(p), http.StatusSeeOther)
			return
		}
		p.Status = "scheduled"
	default:
		http.Redirect(w, r, s.adminListRedirect(listPath, r), http.StatusSeeOther)
		return
	}
	if err := s.store.UpdatePost(p); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	s.firePublishHooks(r, p)
	http.Redirect(w, r, s.adminListRedirect(listPath, r), http.StatusSeeOther)
}

// ---------- 站点设置（分区独立保存）----------

var settingsSections = map[string]bool{"site": true, "appearance": true, "copy": true, "menu": true, "languages": true, "categories": true, "automation": true, "cloudflare": true, "comments": true, "telegram": true, "contact": true, "updates": true, "security": true}

func themeName(id string) string {
	for _, t := range Themes {
		if t.ID == id {
			return t.Name
		}
	}
	return id
}

var themeDescEN = map[string]string{
	"editorial":    "Warm serif, single-column reading list",
	"magazine":     "Sans-serif masthead, centered lead, three-column cards",
	"terminal":     "Monospace, dark, terminal-style content lists",
	"brutalist":    "High contrast, heavy borders, hard shadows, square edges",
	"notebook":     "Paper texture, ruled lines, warm highlights",
	"swiss":        "International grid, red-black palette, large numbering",
	"pastel":       "Soft gradients, rounded cards, friendly tone",
	"newspaper":    "Multi-column serif layout with editorial rhythm",
	"darkpro":      "Modern dark mode, indigo glow, card grid",
	"landing":      "Product landing layout with hero, CTAs and feature cards",
	"product":      "Product site, docs entry and release-note structure",
	"prism":        "Dark poster mood with multi-color signal lines",
	"exchange":     "Web3 growth page with market-dashboard energy",
	"academy":      "AI course and education layout with readable cards",
	"garment":      "B2B garment factory layout with catalog feel",
	"institution":  "Professional service, consulting or association website",
	"studio":       "Image-led portfolio for design, photography or studios",
	"lifestyle":    "Warm small-brand site for cafes, stays or boutiques",
	"knowledge":    "Search-first docs hub with navigation, recommended reads and updates",
	"gilded":       "Espresso-black with gilded gold accents and light-weight serif display type — a dark luxury skin for boutique studios, whisky and jewelry sites",
	"grove":        "Cream paper with a deep-pine sidebar and honey highlights, serif headings — a botanical skin for tea gardens, florists and slow-living blogs",
	"obsidian":     "Graphite-black bento tiles with electric-lime accents and mono numerals — a nighttime skin for developer portfolios and tech homepages",
	"codex":        "Ivory paper with oxblood accents, serif entries under a double-ruled table head — a museum-catalog skin for collections and humanities archives",
	"gilt":         "The split hero in ebony-and-gilt dark: serif display type with brass accents — night-gallery mood for galleries, luxury brands and collection sites",
	"zenith":       "The centered manifesto under a deep night-blue sky: serif headline with a starlit periwinkle accent — manifestos, poetry and concept projects",
	"fir":          "Full-width bands re-dyed in cream paper and pine green with serif display type and pill buttons — organic food, craft and outdoor brand landing pages",
	"ember":        "The ticker goes after-hours: graphite dark base with amber signal color and mono timestamps — terminal mood for crypto dashboards and night-time data sites",
	"ignition":     "Dark launch console: amber-to-flame gradient supply bar and CTA on near-black, telemetry mood for night mints and token drops",
	"cork":         "Kraft-paper pinboard: wax-seal red pins and serif headlines on warm cork lanes, for editorial planning boards and community notice walls",
	"orbit":        "Deep-space night log: glowing star nodes and ice-blue mono dates along a midnight spine, for dark changelogs and build journals",
	"runway":       "Midnight runway reel: crimson stage light and tight sans display type over full-black horizontal panels, for fashion lookbooks and dark photo portfolios",
	"velvet":       "Midnight couture cover: velvet dark with champagne-gold serif display — fashion houses, fragrance, gallery launches",
	"pulse":        "Dark NOC console: ice-cyan readouts and monospace headings for node / infra / API status pages",
	"onyx":         "Onyx-dark bio card with neon-lime accents and a monospace name — developer / hacker link-in-bio",
	"lotus":        "Cool waterside botanics: aqua-mist paper, lotus-pink accents and rounded sans — spa, floral, wellness brands",
	"vapor":        "The Y2K desktop after midnight: violet night wallpaper, neon-pink window chrome and volt-green glints — a vaporwave playground for retro communities and personal fun sites",
	"matinee":      "The widescreen reel printed as a daylight program: warm paper white, burgundy caption cards, serif titles and silver-gelatin imagery — for photo books, exhibition catalogues and brand film sites",
	"rave":         "The anti-grid collage gone nocturnal: black paper with white-ink hard shadows, xerox grayscale plus one volt-green hit, typewriter headlines — for club nights, electronic labels and after-dark zines",
	"astrolabe":    "The filterable directory under a real night sky: ink-blue ground, starlight-amber accents, serif titles and a faint star-speckle backdrop — a planetarium take on ecosystem indexes and project walls",
	"masonry":      "Pinterest-style waterfall: centered hero, category chips and a wide featured card above variable-height cards cascading down airy white columns, for image libraries, inspiration walls and design collections",
	"darkroom":     "The masonry waterfall after dark: near-black gallery walls with a safelight-red accent and grayscale covers that bloom to color on hover, for photography portfolios, night galleries and visual archives",
	"feed":         "Social microblog stream: sticky profile rail plus a centered single-column feed of post cards with a pinned featured entry; clean sky-blue social feel — personal updates / build-in-public / short-form content",
	"noir":         "The feed stream goes lights-out: pure-black AMOLED cards with neon-violet accents and mono timestamps — late-night developer streams / dark personal microblogs",
	"gazette":      "Broadsheet front page: giant serif nameplate over thick-and-thin double rules, a ruled multi-column body and a double-boxed bulletin sidebar on ivory paper with oxblood kickers — for news, opinion and editorial desks",
	"tabloid":      "The gazette skeleton in street-tabloid clothes: black paper, shouting uppercase sans headlines, scarlet block kickers and high-contrast white rules — for pop-culture news, music zines and hot-take commentary",
	"manual":       "Three-pane handbook home: a chapter rail, numbered sections and a quick-reference sidebar in calm blue-gray — for docs sites, product manuals and API references",
	"kernel":       "The manual skeleton in graphite engineer trim: mono headings and steel-blue accents with a man-page mood — for developer docs, ops handbooks and API references",
	"almanac":      "Almanac home: a double-ruled masthead, a poetic seven-column month lattice pinning posts as day cells, and a dotted-leader agenda — warm planner paper with vermilion pins, for event calendars, changelogs and periodicals",
	"nightshift":   "The almanac lattice on night shift: ink-blue dark with neon-violet glowing pins and mono day numbers over the month grid and agenda — for nightlife line-ups, esports schedules and on-chain event calendars",
	"inbox":        "Email-client three-pane: folder rail, message list with unread dots, and a reading pane opening the featured post — classic light mail; newsletter archives & announcements",
	"midnight":     "The three-pane mail shell after dark: ink-blue night, icy-blue accent, glowing unread dots, a gilded star — dev newsletters & changelogs",
	"catalog":      "Resource-catalog home with a brand intro, featured showcase, department rail and compact product cards — tool directories, product catalogs and course libraries",
	"nightmarket":  "The catalog skeleton after dark: black display counters, neon-mint labels and luminous covers — Web3 tools, digital products and nighttime resource directories",
	"broadcast":    "Broadcast home with a large featured player, channel markers and a numbered episode queue — podcasts, interviews, video series and serialized content",
	"airwave":      "The broadcast skeleton on a midnight frequency: deep-blue console, violet signal lights and mono timecodes — tech podcasts, electronic music and late-night interviews",
	"exhibit":      "Curated exhibition home with label-style typography, an asymmetric art wall and room navigation that keeps full covers visible — photography, architecture and case studies",
	"afterhours":   "The exhibition after closing: charcoal gallery walls, safelight-red labels and restrained spotlights — night galleries, visual archives and premium creative studios",
	"paperwhite":   "Cool white paper with ultramarine and vermilion signals — a crisp, restrained editorial site",
	"citrus":       "Pale lemon with leaf green and coral — a bright, approachable brand-content site",
	"bookshop":     "White pages, a cobalt sidebar and red bookmarks — publishing and knowledge blogs",
	"canal":        "Aqua paper with a deep-teal sidebar and orange details — calm documentation and reading",
	"confetti":     "White bento tiles with playful primary-color accents — lively, orderly personal homepages",
	"icebox":       "Icy blue bento tiles with cobalt and cyan — a transparent, technical portfolio skin",
	"ledger":       "White paper and accounting green with precise rules — data indexes and professional archives",
	"signal":       "Pale gray with black ink and signal orange — a sharp product and release index",
	"gallery":      "White split gallery with navy and label red — restrained work and brand showcases",
	"coast":        "Light-aqua split with deep teal and coral — travel and relaxed lifestyle brands",
	"monument":     "Stone-white centered axis with cobalt marks — manifestos and institutional homepages",
	"petal":        "Blush paper with burgundy and leaf green — soft, symmetrical culture and lifestyle sites",
	"market":       "Sky blue, warm yellow and vermilion bands — energetic event and commerce landing pages",
	"seaside":      "Pale-cyan bands with navy and coral — easygoing holiday and outdoor brands",
	"daytrade":     "White and graphite ticker with green signals — daylight data and news dashboards",
	"mintwire":     "Mint ticker with deep teal and violet signals — fresh live news and project updates",
	"sunrise":      "Morning-orange launch page with coral and cobalt CTAs — bright product drops",
	"horizon":      "Sky-blue launch page with cobalt and sunlight yellow — technology and project launches",
	"workshop":     "White blueprint board with blue structure and red pins — practical team planning",
	"playbook":     "Cool-white board with forest green and navy — rigorous roadmaps and project lanes",
	"chronicle":    "Bright paper with an indigo spine and vermilion dates — a modern publishing timeline",
	"gardenpath":   "Pale green with forest and pollen-pink nodes — gentle brand histories and journals",
	"portfolio":    "Pure-white horizontal reel with black ink and label red — minimal case-study portfolios",
	"postcard":     "Sky-blue reel with orange and cobalt — travel, event and lifestyle collections",
	"atelier":      "White modernist poster with red-blue signals — design and cultural launches",
	"festival":     "Pale yellow with pink and teal poster blocks — events, music and creative brands",
	"daywatch":     "Cool-white monitoring panel with blue-green status colors — trustworthy service status",
	"clinic":       "Pale aqua with medical teal and coral warnings — friendly health and system status",
	"peach":        "Peach-pink with coral and teal controls — an approachable creator link page",
	"skyline":      "Bright white and sky blue with sunlight yellow — clean personal and project profiles",
	"herbarium":    "Sage paper with forest green and specimen-red labels — organic and handmade brands",
	"coralreef":    "Aqua organic curves with coral and cobalt — playful wellness and lifestyle brands",
	"cloudos":      "Cloud-blue desktop with cobalt windows and clear white panels — a light digital workspace",
	"candyglass":   "Pink glass desktop with teal and orange controls — playful, bright Y2K software",
	"paperfilm":    "White-paper cinema with black ink and caption red — editorial photography and film books",
	"azurefilm":    "Pale-blue cinema with deep navy and sunlight yellow — modern brand film sites",
	"cutpaper":     "White collage with cobalt, vermilion and warm yellow — crisp cultural and event pages",
	"primary":      "Pale gray with primary blocks and hard black rules — bold fashion and music collage",
	"atlas":        "Bright atlas with navy and blue nodes — professional ecosystem directories",
	"mintmap":      "Mint map with deep teal and coral nodes — friendly tools and resource directories",
	"pinboard":     "White masonry wall with coral and blue labels — image collections and inspiration boards",
	"spectrum":     "Pale-gray gallery with violet, green and orange categories — rich but controlled masonry",
	"daybook":      "Bright social feed with cobalt and pale green — personal updates and short-form content",
	"civic":        "Light-gray feed with navy and civic red — community notices and organization updates",
	"broadsheet":   "Pure-white broadsheet with navy copy and vermilion kickers — a modern news front page",
	"salmonpress":  "Pale salmon paper with charcoal and deep green — a warm cultural newspaper",
	"fieldguide":   "White handbook with forest green and orange annotations — outdoor and product guides",
	"bluebook":     "Pale-blue handbook with navy and red numbering — precise technical references",
	"sunclock":     "Pale-yellow calendar with navy and red dates — bright event and editorial schedules",
	"seedcalendar": "Sage calendar with forest green and coral pins — seasonal and garden planning",
	"postbox":      "Bright mail client with postal red and navy — classic newsletter archives",
	"airmail":      "Sky-blue mail client with navy and coral markers — light briefings and announcements",
	"apothecary":   "Mint catalog shelves with forest and coral labels — approachable tool directories",
	"toolroom":     "Cool-gray shelves with cobalt and safety orange — efficient product catalogs",
	"publicradio":  "White broadcast desk with navy and signal red — professional podcasts and interviews",
	"morningfm":    "Sky-blue broadcast desk with deep teal and sunlight yellow — warm morning programs",
	"whitecube":    "Pure-white gallery walls with graphite type and label red — minimal art and case studies",
	"botanical":    "Sage gallery walls with forest green and cobalt labels — nature-led exhibitions",
	// 外贸独立站主题族（dtc）
	"cream":     "Cream-white paper with warm near-black and hairlines — flagship brand-store homepage for general DTC brands",
	"amberglow": "Warm amber paper with honey-orange accents and serif headings — cozy lifestyle skin for home fragrance and craft brands",
	"inknavy":   "Deep ink-navy with champagne-gold accents and serif display type — premium night-mode skin for accessible luxury brands",
	"oliveleaf": "Rice-paper base with olive-green accents and soft rounded cards — natural skin for personal care and outdoor-living brands",
	"dawnfair":  "Pure white with system sans-serif light-weight display type, hairline dividers, outlined buttons that invert on hover and frameless product cards — Shopify-Dawn default-store feel for general DTC brands",
	"solowhite": "Pure white with an indigo conversion button and oversized type — single-product long-scroll funnel for one-hero-product brands",
	"charcoal":  "Charcoal dark base with amber-gold CTAs and type over imagery — launch-night skin for electronics and fitness gear",
	"coralpop":  "Milk-white base with lively coral accents and large radii — energetic skin for beauty and trend gadgets",
	"limewash":  "Pale green-gray base with moss-green accents and bold serif titles — fresh natural skin for wellness and plant-based products",
	"galleria":  "Gallery white with near-black fine type and zero radius — visual-first lookbook wall for fashion and designer brands",
	"blackbox":  "Near-black exhibition hall with warm gold wall labels — after-dark lookbook for high fashion and limited collections",
	"flaxen":    "Flaxen linen paper with flax-brown accents and small serif labels — textile skin for home linen and slow-living brands",
	"fogblue":   "Fog gray-blue base with harbor-blue accents — cool still-life skin for ceramics, stationery and minimal lifestyle brands",
}

func themeOptionForAdmin(t ThemeOption, lang string) ThemeOption {
	if !strings.HasPrefix(strings.ToLower(lang), "en") {
		return t
	}
	name := t.Name
	if i := strings.LastIndex(name, " · "); i >= 0 {
		name = strings.TrimSpace(name[i+len(" · "):])
	}
	desc := themeDescEN[t.ID]
	if desc == "" {
		desc = t.Desc
	}
	return ThemeOption{ID: t.ID, Name: name, Desc: desc, Category: t.Category}
}

func (s *Server) adminSettings(w http.ResponseWriter, r *http.Request) {
	s.showSettings(w, r, "site", "", "")
}

func (s *Server) adminSettingsSection(w http.ResponseWriter, r *http.Request) {
	sec := r.PathValue("section")
	if !settingsSections[sec] {
		s.notFound(w, r)
		return
	}
	if s.platform != nil && sec == "updates" {
		http.Redirect(w, r, "/admin/updates", http.StatusSeeOther)
		return
	}
	s.showSettings(w, r, sec, "", "")
}

type UpgradeStatus struct {
	Status    string `json:"status"`
	Step      string `json:"step"`
	Version   string `json:"version"`
	Message   string `json:"message"`
	UpdatedAt string `json:"updated_at"`
	Available bool   `json:"available"`
	Running   bool   `json:"running"`
	RunnerLog string `json:"runner_log"`
}

func upgradeRoot() string {
	if wd, err := os.Getwd(); err == nil && wd != "" {
		return wd
	}
	return "."
}

func upgradeStatusPath() string {
	return filepath.Join(upgradeRoot(), "run", "upgrade.json")
}

func upgradeRunnerLogPath() string {
	return filepath.Join(upgradeRoot(), "logs", "upgrade-runner.log")
}

func upgradeScriptAvailable() bool {
	root := upgradeRoot()
	if info, err := os.Stat(filepath.Join(root, "scripts", "cms.sh")); err != nil || info.IsDir() {
		return false
	}
	if info, err := os.Lstat(filepath.Join(root, "current")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	if info, err := os.Stat(filepath.Join(root, "current", "bin", "cms")); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return false
	}
	return true
}

func readUpgradeStatus() *UpgradeStatus {
	st := &UpgradeStatus{
		Status:    "idle",
		Message:   "暂无升级任务",
		Available: upgradeScriptAvailable(),
		RunnerLog: "logs/upgrade-runner.log",
	}
	if data, err := os.ReadFile(upgradeStatusPath()); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, st)
		if st.Status == "" {
			st.Status = "idle"
		}
	}
	st.Available = upgradeScriptAvailable()
	st.Running = st.Status == "running"
	st.RunnerLog = "logs/upgrade-runner.log"
	if !st.Available && st.Status == "idle" {
		st.Message = "当前运行目录不是标准 Linux/macOS 发布包，无法由后台升级。"
	}
	return st
}

func writeUpgradeStatus(st UpgradeStatus) {
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	_ = os.MkdirAll(filepath.Dir(upgradeStatusPath()), 0o755)
	if data, err := json.Marshal(st); err == nil {
		_ = os.WriteFile(upgradeStatusPath(), append(data, '\n'), 0o644)
	}
}

func writeQueuedUpgradeStatus(version string) {
	st := UpgradeStatus{
		Status:  "running",
		Step:    "queued",
		Version: version,
		Message: "升级任务已启动，等待升级器接管。",
	}
	writeUpgradeStatus(st)
}

func launchUpgrade(version string) error {
	root := upgradeRoot()
	logPath := upgradeRunnerLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}

	args := []string{filepath.Join(root, "scripts", "cms.sh"), "upgrade"}
	if version = strings.TrimSpace(version); version != "" {
		args = append(args, version)
	}
	cmd := exec.Command("sh", args...)
	cmd.Dir = root
	cmd.Env = os.Environ()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	_ = logFile.Close()
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}

func (s *Server) adminUpgradeStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, readUpgradeStatus())
}

func (s *Server) adminUpdateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, checkLatestRelease(ctx))
}

func (s *Server) adminStartUpgrade(w http.ResponseWriter, r *http.Request) {
	jsonReq := wantsJSON(r)
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	version := strings.TrimSpace(r.FormValue("version"))
	st := readUpgradeStatus()
	if !st.Available {
		if jsonReq {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": st.Message, "status": st})
			return
		}
		s.showUpgradeFormError(w, r, st.Message)
		return
	}
	if st.Running {
		if jsonReq {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "message": "已有升级任务正在运行。", "status": st})
			return
		}
		s.showUpgradeFormError(w, r, "已有升级任务正在运行。")
		return
	}
	writeQueuedUpgradeStatus(version)
	if err := launchUpgrade(version); err != nil {
		failed := UpgradeStatus{
			Status:  "failed",
			Step:    "launch",
			Version: version,
			Message: "启动升级器失败：" + err.Error(),
		}
		writeUpgradeStatus(failed)
		if jsonReq {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "message": failed.Message, "status": readUpgradeStatus()})
			return
		}
		s.showUpgradeFormError(w, r, "启动升级器失败："+err.Error())
		return
	}
	if jsonReq {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "升级任务已启动。", "status": readUpgradeStatus()})
		return
	}
	s.redirectUpgradePage(w, r, "升级任务已启动，页面会显示最新状态。")
}

func (s *Server) showUpgradeFormError(w http.ResponseWriter, r *http.Request, msg string) {
	if s.platform != nil {
		s.showAdminUpdates(w, r, "", msg)
		return
	}
	s.showSettings(w, r, "updates", "", msg)
}

func (s *Server) redirectUpgradePage(w http.ResponseWriter, r *http.Request, flash string) {
	if s.platform != nil {
		s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: flash})
		http.Redirect(w, r, "/admin/updates", http.StatusSeeOther)
		return
	}
	s.redirectSettings(w, r, "updates", flash)
}

// copyKey 返回某语种下文案设置的存储键：默认语种用裸键，其它语种用 key::lang。
func (s *Server) copyKey(base, lang string) string {
	if lang == s.defaultLang() {
		return base
	}
	return base + "::" + lang
}

func (s *Server) settingsRedirectURL(r *http.Request, section string) string {
	if !settingsSections[section] {
		section = "site"
	}
	q := url.Values{}
	switch section {
	case "site", "copy":
		q.Set("lang", s.editLang(r))
	case "appearance":
		// 主题 options 槽按语种编辑：非默认语种保存后停留在该语种；默认语种维持无参 URL（现状）。
		if lang := s.editLang(r); lang != s.defaultLang() {
			q.Set("lang", lang)
		}
	case "categories":
		q.Set("kind", s.catKind(r))
		q.Set("lang", s.editLang(r))
	}
	target := "/admin/settings/" + section
	if encoded := q.Encode(); encoded != "" {
		target += "?" + encoded
	}
	return target
}

func (s *Server) redirectSettings(w http.ResponseWriter, r *http.Request, section, flash string, newAPISecret ...string) {
	s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: flash, NewAPISecret: newAPISecret})
	http.Redirect(w, r, s.settingsRedirectURL(r, section), http.StatusSeeOther)
}

func (s *Server) showSettings(w http.ResponseWriter, r *http.Request, section, flash, formErr string, newAPISecret ...string) {
	if r.Method == http.MethodGet && flash == "" && formErr == "" {
		if f, ok := s.sess.takeSettingsFlash(sessionToken(r)); ok {
			flash = f.Flash
			newAPISecret = f.NewAPISecret
		}
	}
	sess, _ := s.currentSession(r)
	st := s.site(s.defaultLang())
	adminLang := s.adminLang(r)
	// 当前主题的微调（作为控件初值）
	custom, accent, radius := s.themeTweak(st.Theme)
	cards := make([]ThemeCard, 0, len(Themes))
	for _, t := range Themes {
		display := themeOptionForAdmin(t, adminLang)
		c, a, rd := s.themeTweak(t.ID)
		cards = append(cards, ThemeCard{ID: t.ID, Name: display.Name, Desc: display.Desc, Category: t.Category, Accent: a, Radius: rd, Bg: themeBg(t.ID), Custom: c})
	}
	v := s.adminView(r, "设置")
	s.authed(v, sess)
	v.Section = section
	v.CatKind = s.catKind(r)
	favicon := nonEmpty(st.Favicon, defaultFaviconPath)
	shareImage := nonEmpty(st.ShareImage, defaultShareImageURL)
	v.Settings = &SettingsForm{
		Name: st.Name, Tagline: st.Tagline, Description: st.Description, Keywords: st.Keywords,
		NameDef: st.Name, TaglineDef: st.Tagline, DescriptionDef: st.Description, KeywordsDef: st.Keywords,
		Favicon: favicon, Logo: st.Logo, LogoScale: nonEmpty(st.LogoScale, "1"), ShareImage: shareImage, Brand: st.Brand, Theme: st.Theme,
		Custom: custom, Accent: accent, Radius: radius,
		HeroEyebrow: st.HeroEyebrow, HeroTitle: st.HeroTitle, HeroDescription: st.HeroDescription, FooterNote: st.FooterNote,
		HeroVisual: st.HeroVisual, HeroImage: st.HeroImage, HeroSVG: st.HeroSVG,
		HomeLinksLimit:   strconv.Itoa(s.intSetting(homeLinksLimitKey, defaultHomeLinksLimit, minHomeLinksLimit, maxHomeLinksLimit)),
		HomePostsPerPage: strconv.Itoa(s.intSetting(homePostsPerPageKey, defaultHomePostsPerPage, minHomePostsPerPage, maxHomePostsPerPage)),
		InjectHead:       st.InjectHead, InjectBody: st.InjectBody,
		CommentsProvider:    commentProvider(s.store.Setting("comments.provider")),
		GiscusRepo:          s.store.Setting("comments.giscus.repo"),
		GiscusRepoID:        s.store.Setting("comments.giscus.repo_id"),
		GiscusCategory:      s.store.Setting("comments.giscus.category"),
		GiscusCategoryID:    s.store.Setting("comments.giscus.category_id"),
		GiscusMapping:       commentMapping(s.store.Setting("comments.giscus.mapping")),
		GiscusStrict:        s.store.Setting("comments.giscus.strict") != "0",
		GiscusReactions:     s.store.Setting("comments.giscus.reactions") != "0",
		GiscusInputPosition: commentInputPosition(s.store.Setting("comments.giscus.input_position")),
		GiscusTheme:         commentTheme(s.store.Setting("comments.giscus.theme")),
		ExternalLinks:       s.externalLinkPolicy().Form(),
	}
	v.Themes = make([]ThemeOption, 0, len(Themes))
	for _, t := range Themes {
		v.Themes = append(v.Themes, themeOptionForAdmin(t, v.AdminLang))
	}
	v.Cards = cards
	v.FamilyCards = themeFamilyCards(cards, st.Theme, adminLang)
	v.ThemeCategories = themeCategoriesPresent()
	v.Settings.HomeSections, v.Settings.HomeHero = s.homeSectionConfig()
	v.Flash = flash
	v.FormErr = formErr
	v.AdminI18NJSON = s.adminI18NRaw(v.AdminLang)
	v.LocaleCatalogs = s.localeCatalogViews(v.Admin)
	v.Social = parseSocialLinks(s.store.Setting("social_links"))
	v.APIBaseURL = s.automationBaseURL(r, sess.currentSiteID)
	v.OpenAPIURL = strings.TrimRight(v.APIBaseURL, "/") + "/openapi.json"
	v.APIDocsURL = s.absForRequest(r, "/"+s.defaultLang()+"/api-docs")
	v.SkillPackageURL = "/admin/settings/automation/skill.zip"
	v.StarterPackageURL = "/admin/settings/automation/starter.zip"
	if len(newAPISecret) > 0 {
		v.NewAPISecret = newAPISecret[0]
		if len(newAPISecret) > 1 {
			v.NewAPIScopes = newAPISecret[1]
			v.NewAIBrief = automationAIBrief(v.APIBaseURL, newAPISecret[0], strings.Split(newAPISecret[1], ","))
		} else {
			v.NewAIBrief = automationAIBrief(v.APIBaseURL, newAPISecret[0], nil)
		}
		if len(newAPISecret) > 2 {
			v.NewAPIName = newAPISecret[2]
		}
		if len(newAPISecret) > 3 {
			v.NewAPIKeyID = atoi64(newAPISecret[3])
		}
	}

	switch section {
	case "appearance":
		// 主题 options schema：按当前主题声明的槽渲染动态表单（工厂族槽 + 全主题 hero 槽），
		// 承载在「主题配置」弹窗里；首页版块设置只对标准/默认布局生效，
		// 策划/工厂/dtc 骨架（固定结构）下该节整体不渲染——控件不提交，
		// 后端靠 home_sections_present 登记字段区分「没渲染」与「清空」，数据不丢。
		lang := s.editLang(r)
		v.EditLang = lang
		v.ThemeOptions = s.themeOptionViews(st.Theme, lang)
		v.ThemeOptsLocalized = themeOptionsLocalized(st.Theme)
		v.HomeSectionsApply = layoutForTheme(st.Theme) == "topbar"
	case "site":
		lang := s.editLang(r)
		v.EditLang = lang
		v.Settings.SiteKind = siteKindOf(s.store)
		def := s.site(s.defaultLang())
		v.Settings.NameDef = def.Name
		v.Settings.TaglineDef = def.Tagline
		v.Settings.DescriptionDef = def.Description
		v.Settings.KeywordsDef = def.Keywords
		localized := func(base, fallback string) string {
			v := s.store.Setting(s.copyKey(base, lang))
			if lang == s.defaultLang() && v == "" {
				return fallback
			}
			return v
		}
		v.Settings.Name = localized("site.name", def.Name)
		v.Settings.Tagline = localized("site.tagline", def.Tagline)
		v.Settings.Description = localized("site.description", def.Description)
		v.Settings.Keywords = localized("site.keywords", def.Keywords)
		v.Settings.ShareImage = localized("site.share_image", defaultShareImageURL)
		authorValue := func(kind string) string {
			v := strings.TrimSpace(s.store.Setting(s.copyKey(defaultAuthorKey(kind), lang)))
			if lang == s.defaultLang() && v == "" {
				return defaultAuthorFallback(kind, lang)
			}
			return v
		}
		v.Settings.PostAuthor = authorValue("post")
		v.Settings.PostAuthorDef = defaultAuthorFallback("post", lang)
		v.Settings.LinkAuthor = authorValue("link")
		v.Settings.LinkAuthorDef = defaultAuthorFallback("link", lang)
	case "copy":
		lang := s.editLang(r)
		v.EditLang = lang
		// 显示该语种实际存储值（未设置即为空，便于看出回落）
		v.Settings.Tagline = s.store.Setting(s.copyKey("site.tagline", lang))
		v.Settings.Description = s.store.Setting(s.copyKey("site.description", lang))
		v.Settings.HeroEyebrow = s.store.Setting(s.copyKey("site.hero_eyebrow", lang))
		v.Settings.HeroTitle = s.store.Setting(s.copyKey("site.hero_title", lang))
		v.Settings.HeroDescriptionDef = s.site(s.defaultLang()).HeroDescription
		v.Settings.HeroDescription = s.store.Setting(s.copyKey("site.hero_description", lang))
		v.Settings.FooterNote = s.store.Setting(s.copyKey("site.footer_note", lang))
		v.Settings.HomeFeatured = s.store.Setting(s.copyKey("home.featured_title", lang))
		v.Settings.HomeLinks = s.store.Setting(s.copyKey("home.links_title", lang))
		v.Settings.HomeLatest = s.store.Setting(s.copyKey("home.latest_title", lang))
		v.Settings.HeroImageDef = s.store.Setting("hero.image")
		v.Settings.HeroImage = s.store.Setting(s.copyKey("hero.image", lang))
		v.Settings.HeroImageMode = "inherit"
		if lang == s.defaultLang() {
			v.Settings.HeroImageMode = "global"
			if v.Settings.HeroImage == "" {
				v.Settings.HeroImage = v.Settings.HeroImageDef
			}
		} else if v.Settings.HeroImage != "" {
			v.Settings.HeroImageMode = "custom"
		}
		// 语种默认值（输入框 placeholder，提示「留空则用此默认」）
		tr := s.i18n.Tr(lang, s.defaultLang())
		v.Settings.HomeFeaturedDef = tr.T("home.featured")
		v.Settings.HomeLinksDef = tr.T("home.links")
		v.Settings.HomeLatestDef = tr.T("home.latest")
	case "categories":
		lang := s.editLang(r)
		kind := s.catKind(r)
		v.EditLang = lang
		v.CatKind = kind
		v.CatKindTabs = s.extCategoryKinds(v.AdminLang) // 已启用且支持分类的扩展类型页签（如商品）
		switch kind {
		case "post":
			v.CatSlugBase = "/category/"
		case "link":
			v.CatSlugBase = "/links/cat/"
		default:
			// 扩展类型分类：无「全部入口」行（归档页标题/简介在扩展 hub 的「归档页文案」维护，
			// 同一份 ext_archive_meta 数据），这里只做分类 CRUD。
			v.CatKindExt = true
			if ct := s.lookupType(kind); ct != nil {
				v.CatSlugBase = "/" + strings.Trim(ct.URLPrefix, "/") + "/cat/"
			}
		}
		if !v.CatKindExt {
			all := s.archiveConfig(lang, kind)
			v.Settings.AllTitle = all.Title
			v.Settings.AllLabel = all.Label
			v.Settings.AllSlug = all.Slug
			v.Settings.AllPath = all.Path
			v.Settings.AllDescription = all.Description
		}
		v.Categories, _ = s.store.ListCategories(lang, kind)
		if eid := r.URL.Query().Get("edit"); eid != "" {
			v.EditCat, _ = s.store.GetCategoryByID(atoi64(eid))
		}
	case "languages":
		v.CustomLocales = s.i18n.Custom()
		v.AdminI18NJSON = s.adminI18NRaw(v.AdminLang)
	case "menu":
		lang := v.AdminLang
		if !s.langEnabled(lang) {
			lang = s.defaultLang()
		}
		v.EditLang = lang
		v.MenuTargets = s.menuTargetOptions(v.Admin)
		v.MenuEdit = s.menuEditRows(v.Admin)
	case "telegram":
		// token 本身绝不回填页面（密文性质），只给「已配置」状态。
		v.Settings.TelegramTokenSet = strings.TrimSpace(s.store.Setting(telegramBotTokenSetting)) != ""
		v.Settings.TelegramChannel = s.store.Setting(telegramChannelSetting)
		v.Settings.TelegramChannelURL = s.store.Setting(telegramChannelURLSetting)
		v.Settings.TelegramAutoPush = s.store.Setting(telegramAutoPushSetting) == "1"
		v.Settings.TelegramLastError = s.store.Setting(telegramLastErrorSetting)
	case "contact":
		v.Settings.ContactWhatsApp = s.store.Setting(contactWhatsAppSetting)
		v.Settings.ContactEmail = s.store.Setting(contactEmailSetting)
		v.Settings.ContactPhone = s.store.Setting(contactPhoneSetting)
		v.Settings.ContactWeChatQR = s.store.Setting(contactWeChatQRSetting)
		v.Settings.ContactFloat = s.store.Setting(contactFloatSetting) != "0" // 默认开
	case "automation":
		v.AutomationKeys, _ = s.store.ListAutomationKeys()
		v.AutomationLogs, _ = s.store.ListAutomationLogs(20)
	case "cloudflare":
		v.Cloudflare = s.cloudflareViewForRequest(r)
	case "updates":
		v.Update = currentUpdateInfo()
		v.Upgrade = readUpgradeStatus()
	}

	status := http.StatusOK
	if formErr != "" {
		status = http.StatusBadRequest
	}
	s.rnd.Admin(w, "settings", status, v)
}

func (s *Server) adminCreateAutomationKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.showSettings(w, r, "automation", "", "名称不能为空。")
		return
	}
	scopes := automationScopesFromForm(r)
	token, prefix := newAutomationToken()
	id, err := s.store.CreateAutomationKey(name, token, prefix, strings.Join(scopes, ","))
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.redirectSettings(w, r, "automation", "访问权限已创建，请在列表中点“查看”复制密钥。", token, strings.Join(scopes, ","), name, strconv.FormatInt(id, 10))
}

func (s *Server) adminUpdateAutomationKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	id := atoi64(r.FormValue("id"))
	name := strings.TrimSpace(r.FormValue("name"))
	if id <= 0 {
		s.showSettings(w, r, "automation", "", "访问权限不存在。")
		return
	}
	if name == "" {
		s.showSettings(w, r, "automation", "", "用途名称不能为空。")
		return
	}
	scopes := automationScopesFromFormRequired(r)
	if len(scopes) == 0 {
		s.showSettings(w, r, "automation", "", "至少选择一项权限。")
		return
	}
	if err := s.store.UpdateAutomationKey(id, name, strings.Join(scopes, ",")); err != nil {
		if err == sql.ErrNoRows {
			s.showSettings(w, r, "automation", "", "这条访问权限已失效，不能修改。")
			return
		}
		s.serverError(w, err)
		return
	}
	s.redirectSettings(w, r, "automation", "访问权限已更新。")
}

func newAutomationToken() (token, prefix string) {
	token = "gcms_" + randToken()
	prefix = token
	if len(prefix) > 13 {
		prefix = prefix[:13]
	}
	return token, prefix
}

func automationAIBrief(apiBase, token string, scopes []string) string {
	scopeText := automationScopeLabels(scopes)
	return strings.Join([]string{
		"你是我的网站内容助手。",
		"",
		"连接地址：" + apiBase,
		"OpenAPI 描述文件：" + strings.TrimRight(apiBase, "/") + "/openapi.json",
		"认证方式：Authorization: Bearer " + token,
		"当前权限：" + scopeText,
		"",
		"如果你能读取文件或运行脚本，可以使用 GCMS AI 助手使用包；包里有 SKILL.md、openapi.json 和 scripts/gcms.js。",
		"脚本环境变量：GCMS_API_BASE=" + apiBase,
		"脚本环境变量：GCMS_API_KEY=" + token,
		"",
		"你可以帮我查看语种和分类、上传媒体，并查看、预览、创建、修改文章、链接、页面。",
		"用户明确要求且权限允许时，也可以生成并上传首页 Hero 右侧动画，然后用 /site-profile 写入 hero_image 和 hero_visual:image。",
		"不要增删改安全、系统更新；没有明确要求或没有对应权限时，不要修改站点文案、分类、导航或品牌视觉。",
		"",
		"默认只创建或修改草稿。除非我明确要求，并且你有发布权限，否则不要发布内容。",
		"",
		"需要处理多语种内容时，先查看启用语种：",
		"GET /languages",
		"需要查看未启用的内置语种时：GET /languages?include_disabled=true。越南语 vi、印尼语 id、泰语 th 已是内置预设，不要重复创建。",
		"需要新增自定义语种时：如果是不在内置列表里的自定义语种，并且你有 languages:write 权限，可用 POST /languages 新增。",
		"需要启用或禁用语种时：如果你有 languages:enable 权限，可用 PATCH /languages/{code}，请求体为 {\"enabled\":true} 或 {\"enabled\":false}；当前默认语种不能禁用。",
		"需要设置默认语种时：如果你有 languages:default 权限，可用 PATCH /languages/{code}，请求体为 {\"default\":true}；设为默认会自动启用该语种。",
		"需要维护前台按钮、页脚、搜索空状态等系统文案时：先 GET /languages/{code}/catalog；如果你有 languages:catalog 权限，可用 PATCH /languages/{code}/catalog 写入 {\"catalog\":{...}}。自定义语种或新启用语种出现 home.xxx、footer.xxx 这类 key 时，优先补这里，不要改文章正文替代。",
		"",
		"需要设置分类时，先查看可用分类 ID：",
		"GET /posts/categories?lang=zh",
		"GET /links/categories?lang=zh",
		"",
		"需要设置封面图、正文图片或 Hero 右侧视觉时，先上传媒体文件，拿返回的 url 再写入 cover_image、Markdown 图片或 hero_image：",
		"POST /media",
		"图片上传规则：所有通过媒体接口上传的图片资源，上传前必须先转成 WebP（.webp）格式；Hero 右侧动画必须使用 animated WebP。不要直接上传 jpg、png、gif 原图。",
		"Hero 动画写入规则：先读取 GET /site-profile，提出方案并等我确认；确认后上传动画，再 PATCH /site-profile 写入对应语种的 hero_image，并把 hero_visual 设为 image。",
		"",
		"如果我要你修改某篇内容，请先找到它的 id，不要只凭标题猜。",
		"可以这样查找：",
		"GET /posts?lang=zh&q=关键词",
		"GET /posts?lang=zh&slug=slug",
		"GET /pages?lang=zh&q=关键词",
		"GET /links?lang=zh&q=关键词",
		"",
		"如果我要你更新某篇内容的全部语种，先 GET /posts/{id} 读取 trans_group，再查同组版本：",
		"GET /posts?lang=all&trans_group=分组值",
		"然后逐条 PATCH 对应语种版本的 id，不要用一个语种的正文覆盖其它语种。",
		"",
		"找到目标后，再用对应 id 更新：",
		"PATCH /posts/{id}",
		"PATCH /pages/{id}",
		"PATCH /links/{id}",
		"",
		"发布前复核文章或链接草稿时，可以读取预览结果，检查渲染后的正文 HTML、目录和正式 URL：",
		"GET /posts/{id}/preview",
		"GET /links/{id}/preview",
		"",
		"如果我要你置顶或取消置顶文章/链接，先查到准确 id，再调用：",
		"PATCH /posts/featured/{id}  请求体 {\"featured\":true/false}",
		"PATCH /links/featured/{id}  请求体 {\"featured\":true/false}",
		"置顶只影响首页精选文章或精选链接，不适用于页面，也不等同发布。",
		"",
		"如果找到多个相似结果，先让我确认。",
		"",
		"完成后告诉我创建或修改了哪些内容、对应 id、状态，以及建议我在后台审核什么。",
	}, "\n")
}

func (s *Server) adminRevokeAutomationKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if err := s.store.RevokeAutomationKey(atoi64(r.FormValue("id"))); err != nil {
		s.serverError(w, err)
		return
	}
	s.redirectSettings(w, r, "automation", "访问权限已吊销。")
}

func (s *Server) adminDeleteAutomationKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if err := s.store.DeleteRevokedAutomationKey(atoi64(r.FormValue("id"))); err != nil {
		if err == sql.ErrNoRows {
			s.showSettings(w, r, "automation", "", "只能删除已吊销的访问权限。")
			return
		}
		s.serverError(w, err)
		return
	}
	s.redirectSettings(w, r, "automation", "已删除这条访问权限记录。")
}

func (s *Server) adminRegenerateAutomationKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	id := atoi64(r.FormValue("id"))
	key, ok, err := s.store.GetAutomationKeyByID(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok {
		s.showSettings(w, r, "automation", "", "访问权限不存在。")
		return
	}
	if !key.RevokedAt.IsZero() {
		s.showSettings(w, r, "automation", "", "这条访问权限已吊销，不能重新生成密钥。")
		return
	}
	token, prefix := newAutomationToken()
	if err := s.store.RegenerateAutomationKey(id, token, prefix); err != nil {
		if err == sql.ErrNoRows {
			s.showSettings(w, r, "automation", "", "这条访问权限已失效，不能重新生成密钥。")
			return
		}
		s.serverError(w, err)
		return
	}
	s.redirectSettings(w, r, "automation", "访问密钥已重新生成，请在列表中点“查看”复制新密钥。", token, key.Scopes, key.Name, strconv.FormatInt(id, 10))
}

func automationScopesFromForm(r *http.Request) []string {
	return automationScopesFromFormWithDefault(r, true)
}

func automationScopesFromFormRequired(r *http.Request) []string {
	return automationScopesFromFormWithDefault(r, false)
}

func automationScopesFromFormWithDefault(r *http.Request, useDefault bool) []string {
	_ = r.ParseForm()
	want := map[string]bool{}
	for _, scope := range r.Form["scopes"] {
		if automationScopeValid(scope) {
			want[scope] = true
		}
	}
	for _, col := range automationCollections {
		pub := apiScope(col.path, "publish")
		write := apiScope(col.path, "write")
		read := apiScope(col.path, "read")
		pin := apiScope(col.path, "pin")
		if want[pub] {
			want[write] = true
		}
		if want[write] {
			want[read] = true
		}
		if want[pin] {
			want[read] = true
		}
	}
	if want[apiScopeSiteWrite] {
		want[apiScopeSiteRead] = true
	}
	if want[apiScopeBrandAssetsWrite] {
		want[apiScopeSiteRead] = true
	}
	if want[apiScopeNavigationWrite] {
		want[apiScopeNavigationRead] = true
	}
	if want[apiScopePostCategoriesWrite] {
		want[apiScope("posts", "categories")] = true
	}
	if want[apiScopeLinkCategoriesWrite] {
		want[apiScope("links", "categories")] = true
	}
	if want[apiScopeLanguagesWrite] || want[apiScopeLanguagesEnable] || want[apiScopeLanguagesDefault] || want[apiScopeLanguagesCatalog] {
		want[apiScopeLanguagesRead] = true
	}
	// 平台控制权限是独立的能力族：任一控制权限都自动补齐能力自省；
	// 删站和改域名还必须允许 Pilot 在密码验证后签发短时授权。初始密码
	// 不属于 HTTP 自动化权限，只能通过服务器上的 GCMS 专用 CLI 设置。
	for _, scope := range platformControlScopes() {
		if want[scope] {
			want[apiScopeControlRead] = true
			break
		}
	}
	if want[apiScopeSitesDelete] || want[apiScopeDomainsWrite] {
		want[apiScopeControlUnlock] = true
	}
	var out []string
	if want[apiScopeLanguagesRead] {
		out = append(out, apiScopeLanguagesRead)
	}
	if want[apiScopeLanguagesWrite] {
		out = append(out, apiScopeLanguagesWrite)
	}
	if want[apiScopeLanguagesEnable] {
		out = append(out, apiScopeLanguagesEnable)
	}
	if want[apiScopeLanguagesDefault] {
		out = append(out, apiScopeLanguagesDefault)
	}
	if want[apiScopeLanguagesCatalog] {
		out = append(out, apiScopeLanguagesCatalog)
	}
	if want[apiScopeMediaWrite] {
		out = append(out, apiScopeMediaWrite)
	}
	// 注意（v1.3.16 教训）：这里是白名单式输出组装，仅通过 automationScopeValid 不够，
	// 新 scope 必须同时加进这份列表，否则表单勾选后会被静默丢弃。
	for _, scope := range []string{apiScopeSiteRead, apiScopeSiteWrite, apiScopeBrandAssetsWrite, apiScopeNavigationRead, apiScopeNavigationWrite, apiScopeStatsRead} {
		if want[scope] {
			out = append(out, scope)
		}
	}
	for _, scope := range platformControlScopes() {
		if want[scope] {
			out = append(out, scope)
		}
	}
	for _, col := range automationCollections {
		for _, action := range automationScopeActions(col.path) {
			scope := apiScope(col.path, action)
			if want[scope] {
				out = append(out, scope)
			}
		}
	}
	// content:*/types:write 与扩展集合 scope（如 products:write）通过了 automationScopeValid，
	// 但不属于上面枚举的内置 scope，不透传就会在签发时被静默丢弃（v1.3.16 同款教训）。
	// 按表单提交顺序追加，emitted 兼做去重。
	emitted := map[string]bool{}
	for _, scope := range out {
		emitted[scope] = true
	}
	for _, scope := range r.Form["scopes"] {
		if want[scope] && !emitted[scope] {
			emitted[scope] = true
			out = append(out, scope)
		}
	}
	if len(out) == 0 {
		if useDefault {
			out = append(out, defaultAutomationScopes()...)
		}
	}
	return out
}

func automationScopeValid(scope string) bool {
	// v1.3.40 曾短暂暴露过 HTTP 初始密码写权限。该 scope 已永久退役，
	// 必须在动态集合规则前硬拒绝，避免它被误当成 security 集合的 write。
	if scope == retiredAPIScopeSecurityWrite {
		return false
	}
	switch scope {
	case apiScopeLanguagesRead, apiScopeLanguagesWrite, apiScopeLanguagesEnable, apiScopeLanguagesDefault, apiScopeLanguagesCatalog, apiScopeMediaWrite, apiScopeSiteRead, apiScopeSiteWrite, apiScopeBrandAssetsWrite, apiScopeNavigationRead, apiScopeNavigationWrite,
		apiScopeStatsRead, apiScopeTypesWrite, apiScopeContentRead, apiScopeContentWrite, apiScopeContentPublish,
		apiScopeControlRead, apiScopeControlUnlock, apiScopeSitesCreate, apiScopeSitesUpdate, apiScopeSitesDelete,
		apiScopeThemesRead, apiScopeThemesApply, apiScopeDomainsRead, apiScopeDomainsWrite:
		return true
	}
	// 扩展集合 scope（如 products:write / cases:read）：集合名为合法 slug 即放行——
	// 集合是否真实存在由运行时 apiContentKind 校验；这里只保证词法合法，
	// 否则动态类型的集合权限永远无法通过表单签发。
	if col, action, ok := strings.Cut(scope, ":"); ok && col != "" && slugify(col) == col {
		switch action {
		case "read", "write", "publish":
			return true
		}
	}
	for _, col := range automationCollections {
		for _, action := range automationScopeActions(col.path) {
			if scope == apiScope(col.path, action) {
				return true
			}
		}
	}
	return false
}

func defaultAutomationScopes() []string {
	out := []string{apiScopeLanguagesRead, apiScopeMediaWrite}
	for _, col := range automationCollections {
		out = append(out, apiScope(col.path, "read"))
		if col.path == "posts" || col.path == "links" {
			out = append(out, apiScope(col.path, "categories"))
		}
		out = append(out, apiScope(col.path, "write"))
	}
	return out
}

func (s *Server) adminSaveSite(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	lang := s.editLang(r)
	name := strings.TrimSpace(r.FormValue("site_name"))
	if lang == s.defaultLang() && name == "" {
		s.showSettings(w, r, "site", "", "站点名称不能为空。")
		return
	}
	_ = s.store.SetSetting(s.copyKey("site.name", lang), name)
	if s.platform != nil && sess.currentSiteID > 0 && lang == s.defaultLang() {
		if err := s.platform.SetSiteName(sess.currentSiteID, name); err != nil {
			s.serverError(w, err)
			return
		}
	}
	_ = s.store.SetSetting(s.copyKey("site.tagline", lang), strings.TrimSpace(r.FormValue("site_tagline")))
	_ = s.store.SetSetting(s.copyKey("site.description", lang), strings.TrimSpace(r.FormValue("site_description")))
	_ = s.store.SetSetting(s.copyKey("site.keywords", lang), strings.TrimSpace(r.FormValue("site_keywords")))
	_ = s.store.SetSetting(s.copyKey(postDefaultAuthorKey, lang), strings.TrimSpace(r.FormValue("default_post_author")))
	_ = s.store.SetSetting(s.copyKey(linkDefaultAuthorKey, lang), strings.TrimSpace(r.FormValue("default_link_author")))
	favicon := strings.TrimSpace(r.FormValue("site_favicon"))
	if favicon == defaultFaviconPath {
		favicon = ""
	}
	shareImage := strings.TrimSpace(r.FormValue("site_share_image"))
	if shareImage == defaultShareImageURL {
		shareImage = ""
	}
	_ = s.store.SetSetting("site.favicon", favicon)
	_ = s.store.SetSetting("site.logo", strings.TrimSpace(r.FormValue("site_logo")))
	_ = s.store.SetSetting("site.logo_scale", normalizeLogoScale(r.FormValue("site_logo_scale")))
	_ = s.store.SetSetting(s.copyKey("site.share_image", lang), shareImage)
	brand := r.FormValue("site_brand")
	if brand != "both" && brand != "text" {
		brand = "logo"
	}
	linksLimit := boundedInt(r.FormValue("home_links_limit"), defaultHomeLinksLimit, minHomeLinksLimit, maxHomeLinksLimit)
	postsPerPage := boundedInt(r.FormValue("home_posts_per_page"), defaultHomePostsPerPage, minHomePostsPerPage, maxHomePostsPerPage)
	_ = s.store.SetSetting("site.brand", brand)
	_ = s.store.SetSetting(homeLinksLimitKey, strconv.Itoa(linksLimit))
	_ = s.store.SetSetting(homePostsPerPageKey, strconv.Itoa(postsPerPage))
	_ = s.store.SetSetting("social_links", buildSocialJSON(r.Form["social_url"], r.Form["social_label"]))
	_ = s.store.SetSetting(externalLinkPolicyKey, externalLinkPolicyFromForm(r.Form).JSON())
	_ = s.store.SetSetting("inject.head", strings.TrimSpace(r.FormValue("inject_head")))
	_ = s.store.SetSetting("inject.body", strings.TrimSpace(r.FormValue("inject_body")))
	s.clearGeneratedCaches()
	s.redirectSettings(w, r, "site", "基础信息已保存。")
}

func (s *Server) adminSaveAppearance(w http.ResponseWriter, r *http.Request) {
	// 外观保存支持 AJAX（admin.js fetch 提交，成功就地 flash + 弹窗关，不整页刷新）；
	// 无 JS 时照旧走 PRG。两条路径共用同一套解析与落库逻辑。
	jsonReq := wantsJSON(r)
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	theme := r.FormValue("theme")
	if !validTheme(theme) {
		if jsonReq {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "未知的主题。"})
			return
		}
		s.showSettings(w, r, "appearance", "", "未知的主题。")
		return
	}
	_ = s.store.SetSetting("theme", theme)

	custom := ""
	if v := r.FormValue("theme_custom"); v == "on" || v == "1" {
		custom = "1"
	}
	_ = s.store.SetSetting("theme."+theme+".custom", custom)
	if accent := strings.TrimSpace(r.FormValue("theme_accent")); hexColor(accent) {
		_ = s.store.SetSetting("theme."+theme+".accent", accent)
	}
	if radius := strings.TrimSpace(r.FormValue("theme_radius")); radius != "" {
		if n, err := strconv.Atoi(radius); err == nil && n >= 0 && n <= 40 {
			_ = s.store.SetSetting("theme."+theme+".radius", strconv.Itoa(n))
		}
	}

	// 首页 Hero 视觉（默认动画 / 动画1 / 图片或 SVG 文件 / 内联 SVG 代码）——全局
	// （主题 options schema 的 hero 槽：控件由 schema 循环渲染，存储键维持不变。）
	// anim1 必须显式放行：下拉里有「动画1」选项，早前一律归一成 "" 导致该选择保存后回落默认动画。
	hv := r.FormValue("hero_visual")
	if hv != "image" && hv != "svg" && hv != "anim1" {
		hv = ""
	}
	_ = s.store.SetSetting("hero.visual", hv)
	_ = s.store.SetSetting("hero.image", strings.TrimSpace(r.FormValue("hero_image")))
	_ = s.store.SetSetting("hero.svg", strings.TrimSpace(r.FormValue("hero_svg")))

	// 主题 options（工厂族三槽）：只有表单实际渲染了这些控件（带槽标记）才写，
	// 防止在内容主题下保存外观时误清工厂数据；按编辑语种落 键 / 键::lang。
	if r.FormValue(themeOptsFormMarker) == "factory" {
		s.saveThemeOptionsFromForm(r, s.editLang(r))
	}

	// 首页版块：Hero 开关 + 版块顺序/显示（仅默认布局）——与「选主题」同处一屏保存。
	// 固定结构骨架（策划/工厂/dtc）下该节整体不渲染 → 控件不提交；只有表单带
	// 登记字段（home_sections_present，随该节一起渲染）才写这两个键——
	// 缺字段 ≠ 清空，换回标准主题时已有配置原样保留（同 theme_opt_slot 的思路）。
	if r.FormValue("home_sections_present") == "1" {
		homeHero := "1"
		if r.FormValue("home_hero") != "1" {
			homeHero = "0"
		}
		_ = s.store.SetSetting(homeHeroKey, homeHero)
		_ = s.store.SetSetting(homeSectionsKey, sanitizeHomeSectionsJSON(r.FormValue("home_sections")))
	}

	s.clearGeneratedCaches()
	if jsonReq {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "外观设置已保存。", "theme": theme})
		return
	}
	s.redirectSettings(w, r, "appearance", "外观设置已保存。")
}

func (s *Server) adminSaveComments(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	provider := commentProvider(r.FormValue("comments_provider"))
	repo := strings.TrimSpace(r.FormValue("giscus_repo"))
	repoID := strings.TrimSpace(r.FormValue("giscus_repo_id"))
	category := strings.TrimSpace(r.FormValue("giscus_category"))
	categoryID := strings.TrimSpace(r.FormValue("giscus_category_id"))
	mapping := commentMapping(r.FormValue("giscus_mapping"))
	strict := boolAttr(r.FormValue("giscus_strict") == "1")
	reactions := boolAttr(r.FormValue("giscus_reactions") == "1")
	inputPosition := commentInputPosition(r.FormValue("giscus_input_position"))
	theme := commentTheme(r.FormValue("giscus_theme"))
	if r.FormValue("enable_giscus") == "1" {
		provider = "giscus"
	}

	if script := r.FormValue("giscus_config_script"); strings.TrimSpace(script) != "" {
		if v := giscusScriptAttr(script, "data-repo"); repo == "" && v != "" {
			repo = v
		}
		if v := giscusScriptAttr(script, "data-repo-id"); repoID == "" && v != "" {
			repoID = v
		}
		if v := giscusScriptAttr(script, "data-category"); category == "" && v != "" {
			category = v
		}
		if v := giscusScriptAttr(script, "data-category-id"); categoryID == "" && v != "" {
			categoryID = v
		}
		if v := giscusScriptAttr(script, "data-mapping"); v != "" {
			mapping = commentMapping(v)
		}
		if v := giscusScriptAttr(script, "data-strict"); v != "" {
			strict = boolAttr(v == "1")
		}
		if v := giscusScriptAttr(script, "data-reactions-enabled"); v != "" {
			reactions = boolAttr(v == "1")
		}
		if v := giscusScriptAttr(script, "data-input-position"); v != "" {
			inputPosition = commentInputPosition(v)
		}
		if v := giscusScriptAttr(script, "data-theme"); v != "" {
			theme = commentTheme(v)
		}
	}

	if provider == "giscus" {
		switch {
		case !validGiscusRepo(repo):
			s.showSettings(w, r, "comments", "", "仓库地址请填写 owner/repo，例如 ccvar/site-comments。")
			return
		case repoID == "":
			s.showSettings(w, r, "comments", "", "请填写 giscus 生成的 Repo ID。")
			return
		case category == "":
			s.showSettings(w, r, "comments", "", "请填写讨论分类名称。")
			return
		case categoryID == "":
			s.showSettings(w, r, "comments", "", "请填写 giscus 生成的 Category ID。")
			return
		}
	}

	_ = s.store.SetSetting("comments.provider", provider)
	_ = s.store.SetSetting("comments.giscus.repo", repo)
	_ = s.store.SetSetting("comments.giscus.repo_id", repoID)
	_ = s.store.SetSetting("comments.giscus.category", category)
	_ = s.store.SetSetting("comments.giscus.category_id", categoryID)
	_ = s.store.SetSetting("comments.giscus.mapping", mapping)
	_ = s.store.SetSetting("comments.giscus.strict", strict)
	_ = s.store.SetSetting("comments.giscus.reactions", reactions)
	_ = s.store.SetSetting("comments.giscus.input_position", inputPosition)
	_ = s.store.SetSetting("comments.giscus.theme", theme)
	s.clearGeneratedCaches()
	s.redirectSettings(w, r, "comments", "评论设置已保存。")
}

func commentProvider(v string) string {
	if strings.TrimSpace(v) == "giscus" {
		return "giscus"
	}
	return "none"
}

func validGiscusRepo(v string) bool {
	if strings.ContainsAny(v, " \t\r\n") || strings.Contains(v, "://") {
		return false
	}
	parts := strings.Split(v, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, part := range parts {
		for _, r := range part {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
	}
	return true
}

func giscusScriptAttr(raw, attr string) string {
	attr = strings.TrimSpace(attr)
	if attr == "" {
		return ""
	}
	re := regexp.MustCompile(`(?is)\b` + regexp.QuoteMeta(attr) + `\s*=\s*("([^"]*)"|'([^']*)'|([^\s>]+))`)
	m := re.FindStringSubmatch(raw)
	if len(m) == 0 {
		return ""
	}
	for _, v := range m[2:] {
		if v != "" {
			return strings.TrimSpace(html.UnescapeString(v))
		}
	}
	return ""
}

func (s *Server) adminSavePassword(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if msg := s.changeAdminPassword(r); msg != "" {
		s.showSettings(w, r, "security", "", msg)
		return
	}
	s.redirectSettings(w, r, "security", "密码已更新。")
}

func (s *Server) changeAdminPassword(r *http.Request) string {
	cur := r.FormValue("current_password")
	neu := r.FormValue("new_password")
	conf := r.FormValue("confirm_password")
	user, hash := s.adminCredentials()
	switch {
	case bcrypt.CompareHashAndPassword([]byte(hash), []byte(cur)) != nil:
		return "当前密码不正确。"
	case len([]rune(neu)) < 6:
		return "新密码至少 6 位。"
	case neu == store.DefaultAdminPassword:
		return "新密码不能继续使用默认密码。"
	case neu != conf:
		return "两次输入的新密码不一致。"
	}
	if nh, err := bcrypt.GenerateFromPassword([]byte(neu), bcrypt.DefaultCost); err == nil {
		_ = s.setAdminPasswordHash(user, string(nh))
	}
	return ""
}

func (s *Server) adminSaveAdminI18N(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	lang := strings.TrimSpace(r.FormValue("admin_lang"))
	if !s.i18n.Known(lang) {
		lang = s.adminLang(r)
	}
	raw := strings.TrimSpace(r.FormValue("admin_i18n_json"))
	if raw != "" {
		var kv map[string]string
		if err := json.Unmarshal([]byte(raw), &kv); err != nil {
			v := s.adminView(r, "设置")
			msg := v.Admin.T("admin.settings.admin_i18n.invalid", "界面文字 JSON 格式不正确。")
			if s.platform != nil {
				s.showAdminI18N(w, r, "", msg)
			} else {
				s.showSettings(w, r, "languages", "", msg)
			}
			return
		}
	}
	overrides := i18n.ParseAdminOverrides(raw)
	if err := s.setAdminI18NRaw(lang, i18n.MarshalAdminOverrides(overrides)); err != nil {
		s.serverError(w, err)
		return
	}
	v := s.adminView(r, "设置")
	flash := v.Admin.T("admin.settings.admin_i18n.saved", "界面文字已保存。")
	if s.platform != nil {
		s.sess.setSettingsFlash(sessionToken(r), settingsFlash{Flash: flash})
		http.Redirect(w, r, "/admin/admin-i18n", http.StatusSeeOther)
		return
	}
	s.redirectSettings(w, r, "languages", flash)
}

func (s *Server) localeCatalogViews(admin *i18n.AdminTr) map[string]LocaleCatalogView {
	out := map[string]LocaleCatalogView{}
	def := s.defaultLang()
	for _, loc := range s.i18n.All() {
		code := loc.Code
		source := s.i18n.CatalogSource(code)
		override := s.i18n.CatalogOverride(code)
		catalog := s.i18n.Catalog(code, def)
		out[code] = LocaleCatalogView{
			Code:          code,
			JSON:          i18n.MarshalCatalog(catalog),
			Source:        source,
			SourceLabel:   localeCatalogSourceLabel(admin, source),
			OverrideCount: len(override),
			KeyCount:      len(catalog),
		}
	}
	return out
}

func localeCatalogSourceLabel(admin *i18n.AdminTr, source string) string {
	switch source {
	case "custom":
		return admin.T("admin.settings.languages.catalog_source_custom", "已自定义")
	case "builtin":
		return admin.T("admin.settings.languages.catalog_source_builtin", "内置")
	default:
		return admin.T("admin.settings.languages.catalog_source_fallback", "回落")
	}
}

func (s *Server) saveLocaleCatalog(code string, catalog map[string]string) error {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" || !s.i18n.Known(code) {
		return fmt.Errorf("unknown locale: %s", code)
	}
	overrides := i18n.ParseCatalogOverrides(s.store.Setting(localeCatalogsSettingKey))
	if overrides == nil {
		overrides = map[string]map[string]string{}
	}
	if len(catalog) == 0 {
		delete(overrides, code)
	} else {
		overrides[code] = i18n.SanitizeCatalog(catalog)
	}
	raw := i18n.MarshalCatalogOverrides(overrides)
	if err := s.store.SetSetting(localeCatalogsSettingKey, raw); err != nil {
		return err
	}
	s.i18n.LoadCatalogOverrides(raw)
	s.clearGeneratedCaches()
	return nil
}

func (s *Server) adminSaveLocaleCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	admin := s.adminView(r, "设置").Admin
	code := strings.ToLower(strings.TrimSpace(r.FormValue("code")))
	if code == "" || !s.i18n.Known(code) {
		s.showSettings(w, r, "languages", "", admin.T("admin.settings.languages.catalog_bad_lang", "语种不存在。"))
		return
	}
	catalog, err := i18n.ParseCatalog(r.FormValue("catalog_json"))
	if err != nil {
		s.showSettings(w, r, "languages", "", admin.T("admin.settings.languages.catalog_invalid", "字典 JSON 格式不正确。"))
		return
	}
	if err := s.saveLocaleCatalog(code, catalog); err != nil {
		s.serverError(w, err)
		return
	}
	s.redirectSettings(w, r, "languages", admin.T("admin.settings.languages.catalog_saved", "语种字典已保存。"))
}

func (s *Server) adminClearDemoContent(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if err := s.store.ClearDemoContent(); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	s.redirectSettings(w, r, "security", "演示数据已清除。")
}

func (s *Server) adminReloadProductDemo(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if err := s.store.ReloadShowcaseContent(); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	s.redirectSettings(w, r, "security", "产品演示站已重新载入。")
}

func (s *Server) adminSaveCopy(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	lang := s.editLang(r)
	set := func(base, field string) {
		_ = s.store.SetSetting(s.copyKey(base, lang), strings.TrimSpace(r.FormValue(field)))
	}
	set("site.hero_eyebrow", "hero_eyebrow")
	set("site.hero_title", "hero_title")
	set("site.hero_description", "hero_description")
	set("site.footer_note", "footer_note")
	set("home.featured_title", "home_featured")
	set("home.links_title", "home_links")
	set("home.latest_title", "home_latest")
	heroImage := strings.TrimSpace(r.FormValue("hero_image_lang"))
	if lang == s.defaultLang() {
		_ = s.store.SetSetting("hero.image", heroImage)
		if heroImage != "" {
			_ = s.store.SetSetting("hero.visual", "image")
		} else if s.store.Setting("hero.visual") == "image" {
			_ = s.store.SetSetting("hero.visual", "")
		}
	} else if r.FormValue("hero_image_mode") == "custom" && heroImage != "" {
		_ = s.store.SetSetting(s.copyKey("hero.image", lang), heroImage)
		_ = s.store.SetSetting(s.copyKey("hero.visual", lang), "image")
	} else {
		_ = s.store.SetSetting(s.copyKey("hero.image", lang), "")
		_ = s.store.SetSetting(s.copyKey("hero.visual", lang), "")
	}
	s.clearGeneratedCaches()
	s.redirectSettings(w, r, "copy", "文案已保存。")
}

// adminSaveMenu 保存前台导航菜单（URL + 各语种标签，顺序即 DOM 行序）。
func (s *Server) adminSaveMenu(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	labelsByLang := map[string][]string{}
	for _, l := range s.locales() {
		labelsByLang[l.Code] = r.Form["nav_label_"+l.Code]
	}
	_ = s.store.SetSetting("nav_menu", buildMenuJSON(r.Form["nav_url"], labelsByLang))
	s.clearGeneratedCaches()
	s.redirectSettings(w, r, "menu", "导航菜单已保存。")
}

func (s *Server) adminSaveLanguages(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	// 勾选启用的语种
	seen := map[string]bool{}
	var enabled []string
	for _, c := range r.Form["enabled"] {
		c = strings.TrimSpace(c)
		if s.i18n.Known(c) && !seen[c] {
			enabled = append(enabled, c)
			seen[c] = true
		}
	}
	if len(enabled) == 0 {
		enabled = []string{"zh"}
	}
	enabled = i18n.SortLocales(enabled)
	// 默认语种置首
	def := strings.TrimSpace(r.FormValue("default_lang"))
	if seen[def] {
		ordered := []string{def}
		for _, c := range enabled {
			if c != def {
				ordered = append(ordered, c)
			}
		}
		enabled = ordered
	}
	_ = s.store.SetSetting("locales", strings.Join(enabled, ","))
	_ = s.store.SetSetting("default_lang", enabled[0])
	s.clearGeneratedCaches()
	s.redirectSettings(w, r, "languages", "语言设置已保存。")
}

// adminAddLocalePreset 新增一个自定义语种预设（存 settings.custom_locales）。
func (s *Server) adminAddLocalePreset(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	_, _, errMsg, err := s.addCustomLocale(
		r.FormValue("code"),
		r.FormValue("name"),
		r.FormValue("tag"),
		r.FormValue("og"),
	)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if errMsg != "" {
		s.showSettings(w, r, "languages", "", errMsg)
		return
	}
	s.clearGeneratedCaches()
	s.redirectSettings(w, r, "languages", "已新增语种预设，可在上方勾选启用。")
}

func (s *Server) addCustomLocale(code, name, tag, og string) (i18n.Locale, string, string, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	if !i18n.ValidCode(code) {
		return i18n.Locale{}, "bad_code", "语种代码非法：需 2-12 位小写字母 / 数字 / 连字符（如 pt、zh-tw）。", nil
	}
	if s.i18n.Known(code) {
		return i18n.Locale{}, "language_exists", "该语种代码已存在（内置或已添加）。", nil
	}
	cur := s.i18n.Custom()
	cur = append(cur, i18n.Locale{
		Code: code,
		Name: strings.TrimSpace(name),
		Tag:  strings.TrimSpace(tag),
		OG:   strings.TrimSpace(og),
	})
	raw := i18n.MarshalCustom(cur)
	if err := s.store.SetSetting("custom_locales", raw); err != nil {
		return i18n.Locale{}, "", "", err
	}
	s.i18n.LoadCustom(raw)
	return s.i18n.Locale(code), "", "", nil
}

func (s *Server) enableLocale(code string, makeDefault bool) error {
	code = strings.ToLower(strings.TrimSpace(code))
	if !s.i18n.Known(code) {
		return fmt.Errorf("unknown locale: %s", code)
	}
	seen := map[string]bool{}
	codes := []string{}
	if makeDefault {
		codes = append(codes, code)
		seen[code] = true
	}
	for _, loc := range s.locales() {
		if seen[loc.Code] {
			continue
		}
		codes = append(codes, loc.Code)
		seen[loc.Code] = true
	}
	if !seen[code] {
		codes = append(codes, code)
	}
	if len(codes) == 0 {
		codes = []string{"zh"}
	}
	if err := s.store.SetSetting("locales", strings.Join(codes, ",")); err != nil {
		return err
	}
	return s.store.SetSetting("default_lang", codes[0])
}

func (s *Server) disableLocale(code string) error {
	code = strings.ToLower(strings.TrimSpace(code))
	if !s.i18n.Known(code) {
		return fmt.Errorf("unknown locale: %s", code)
	}
	if code == s.defaultLang() {
		return fmt.Errorf("default locale cannot be disabled: %s", code)
	}
	seen := map[string]bool{}
	codes := []string{}
	for _, loc := range s.locales() {
		if loc.Code == code || seen[loc.Code] {
			continue
		}
		codes = append(codes, loc.Code)
		seen[loc.Code] = true
	}
	if len(codes) == 0 {
		codes = []string{"zh"}
	}
	return s.store.SetSetting("locales", strings.Join(codes, ","))
}

// adminDeleteLocalePreset 删除一个自定义语种预设；若它在启用列表里也一并移除。
func (s *Server) adminDeleteLocalePreset(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	code := strings.TrimSpace(r.FormValue("code"))
	var kept []i18n.Locale
	for _, l := range s.i18n.Custom() {
		if l.Code != code {
			kept = append(kept, l)
		}
	}
	_ = s.store.SetSetting("custom_locales", i18n.MarshalCustom(kept))
	s.i18n.LoadCustom(s.store.Setting("custom_locales"))
	overrides := i18n.ParseCatalogOverrides(s.store.Setting(localeCatalogsSettingKey))
	delete(overrides, strings.ToLower(strings.TrimSpace(code)))
	rawCatalogs := i18n.MarshalCatalogOverrides(overrides)
	_ = s.store.SetSetting(localeCatalogsSettingKey, rawCatalogs)
	s.i18n.LoadCatalogOverrides(rawCatalogs)
	// 同步清理启用列表（Active 会自动丢弃已不可用的语种码）
	act := s.locales()
	codes := make([]string, 0, len(act))
	for _, l := range act {
		codes = append(codes, l.Code)
	}
	_ = s.store.SetSetting("locales", strings.Join(codes, ","))
	_ = s.store.SetSetting("default_lang", codes[0])
	s.clearGeneratedCaches()
	s.redirectSettings(w, r, "languages", "已删除语种预设。")
}

// ---------- 分类管理 ----------

func (s *Server) adminSaveCategoryAll(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	lang := s.editLang(r)
	kind := s.catKind(r)
	// 「全部入口」只属于文章/链接归档；扩展类型的归档文案走扩展 hub（ext_archive_meta）。
	if kind != "post" && kind != "link" {
		s.notFound(w, r)
		return
	}
	title := r.FormValue("title")
	label := r.FormValue("label")
	desc := r.FormValue("description")
	_, errMsg, err := s.saveCategoryAllEntry(kind, lang, title, label, desc, r.FormValue("slug"))
	if err != nil {
		s.serverError(w, err)
		return
	}
	if errMsg != "" {
		s.showSettings(w, r, "categories", "", errMsg)
		return
	}
	s.redirectSettings(w, r, "categories", "“全部”入口已保存。")
}

func (s *Server) saveCategoryAllEntry(kind, lang, title, label, desc, slugRaw string) (ArchiveConfig, string, error) {
	title = strings.TrimSpace(title)
	label = strings.TrimSpace(label)
	desc = strings.TrimSpace(desc)
	if title == "" {
		return ArchiveConfig{}, "页面标题不能为空。", nil
	}
	if label == "" {
		return ArchiveConfig{}, "“全部”按钮文字不能为空。", nil
	}
	fallbackSlug := "category"
	if kind == "link" {
		fallbackSlug = "links"
	}
	slug := normalizeArchiveSlug(slugRaw, fallbackSlug)
	newPath := archivePath(kind, slug)
	if kind == "post" && newPath != "/category" {
		exists, err := s.store.CategorySlugExists(lang, slug, 0)
		if err != nil {
			return ArchiveConfig{}, "", err
		}
		if exists {
			return ArchiveConfig{}, "Slug 已被真实分类占用，请换一个。", nil
		}
	} else if kind == "link" && newPath != "/links" {
		if p, err := s.store.GetLinkBySlug(lang, slug, true); err != nil {
			return ArchiveConfig{}, "", err
		} else if p != nil {
			return ArchiveConfig{}, "Slug 已被链接详情页占用，请换一个。", nil
		}
	}
	old := s.archiveConfig(lang, kind)
	values := map[string]string{
		"title":       title,
		"label":       label,
		"slug":        slug,
		"description": desc,
	}
	for field, value := range values {
		if err := s.store.SetSetting(s.archiveStoreKey(kind, field, lang), value); err != nil {
			return ArchiveConfig{}, "", err
		}
	}
	s.syncCategoryNavPath(old.Path, newPath)
	s.clearGeneratedCaches()
	return s.archiveConfig(lang, kind), "", nil
}

func (s *Server) syncCategoryNavPath(oldPath, newPath string) {
	if newPath == "" || oldPath == newPath {
		return
	}
	rows := parseMenuRows(s.store.Setting("nav_menu"))
	if len(rows) == 0 {
		return
	}
	changed := false
	for i := range rows {
		if rows[i].URL == oldPath || rows[i].URL == "/category/engineering" || rows[i].URL == "/category/all" {
			rows[i].URL = newPath
			changed = true
		}
	}
	if changed {
		b, _ := json.Marshal(rows)
		_ = s.store.SetSetting("nav_menu", string(b))
	}
}

func (s *Server) adminSaveCategory(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	lang := s.editLang(r)
	id := atoi64(r.FormValue("id"))
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.showSettings(w, r, "categories", "", "分类名称不能为空。")
		return
	}
	slug := slugify(strings.TrimSpace(r.FormValue("slug")))
	if slug == "" {
		slug = slugify(name)
	}
	if slug == "" {
		slug = "cat-" + strconv.FormatInt(time.Now().Unix(), 36)
	}
	base, n := slug, 2
	for {
		exists, _ := s.store.CategorySlugExists(lang, slug, id)
		if !exists {
			break
		}
		slug = base + "-" + strconv.Itoa(n)
		n++
	}
	c := &store.Category{ID: id, Slug: slug, Name: name, Description: strings.TrimSpace(r.FormValue("description")), Lang: lang, Kind: s.catKind(r)}
	if id == 0 {
		if _, err := s.store.CreateCategory(c); err != nil {
			s.serverError(w, err)
			return
		}
		s.clearGeneratedCaches()
		s.redirectSettings(w, r, "categories", "分类已添加。")
		return
	}
	if ex, _ := s.store.GetCategoryByID(id); ex != nil {
		c.Lang = ex.Lang
		c.TransGroup = ex.TransGroup
	}
	if err := s.store.UpdateCategory(c); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	s.redirectSettings(w, r, "categories", "分类已更新。")
}

func (s *Server) adminDeleteCategory(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	if err := s.store.DeleteCategory(atoi64(r.FormValue("id"))); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	s.redirectSettings(w, r, "categories", "分类已删除。")
}

func (s *Server) adminReorderCategories(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	var ids []int64
	for _, sv := range strings.Split(r.FormValue("order"), ",") {
		if n := atoi64(strings.TrimSpace(sv)); n > 0 {
			ids = append(ids, n)
		}
	}
	if err := s.store.ReorderCategories(ids); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	w.WriteHeader(http.StatusNoContent)
}

// ---------- 页面（type=page，如关于）----------

func (s *Server) adminPages(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	lang := s.editLang(r)
	pages, err := s.store.ListPages(lang)
	if err != nil {
		s.serverError(w, err)
		return
	}
	v := s.adminView(r, "页面")
	s.authed(v, sess)
	v.AllPosts = pages
	v.EditLang = lang
	s.rnd.Admin(w, "pages", http.StatusOK, v)
}

func (s *Server) adminPageNew(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	s.showEdit(w, r, sess, &store.Post{Type: "page", Status: "published", Lang: s.editLang(r)}, "", "")
}

func (s *Server) adminPageEdit(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	p, _ := s.store.GetPostByID(atoi64(r.PathValue("id")))
	if p == nil || p.Type != "page" {
		s.notFound(w, r)
		return
	}
	flash := ""
	if r.URL.Query().Get("saved") == "1" {
		flash = "页面已保存。"
	}
	s.showEdit(w, r, sess, p, flash, "")
}

func (s *Server) adminPageCreate(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	lang := s.editLang(r)
	p, formErr := pageFromForm(r, 0, lang)
	if formErr != "" {
		s.showEdit(w, r, sess, p, "", formErr)
		return
	}
	p.Slug = s.uniqueSlug(lang, p.Slug, 0)
	id, err := s.store.CreatePost(p)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	p.ID = id
	s.firePublishHooks(r, p)
	http.Redirect(w, r, fmt.Sprintf("/admin/pages/%d/edit?saved=1", id), http.StatusSeeOther)
}

func (s *Server) adminPageSave(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.checkCSRF(w, r)
	if !ok {
		return
	}
	id := atoi64(r.PathValue("id"))
	p, _ := s.store.GetPostByID(id)
	if p == nil || p.Type != "page" {
		s.notFound(w, r)
		return
	}
	updated, formErr := pageFromForm(r, id, p.Lang)
	if formErr != "" {
		updated.TransGroup = p.TransGroup
		s.showEdit(w, r, sess, updated, "", formErr)
		return
	}
	updated.CreatedAt = p.CreatedAt
	updated.TransGroup = p.TransGroup
	updated.PublishedAt = p.PublishedAt
	preserveSEOOverrides(r, updated, p)
	updated.Slug = s.uniqueSlug(p.Lang, updated.Slug, id)
	if err := s.store.UpdatePost(updated); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	s.firePublishHooks(r, updated)
	http.Redirect(w, r, fmt.Sprintf("/admin/pages/%d/edit?saved=1", id), http.StatusSeeOther)
}

func (s *Server) adminPageDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	id := atoi64(r.PathValue("id"))
	p, err := s.store.GetPostByID(id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if p == nil || p.Type != "page" {
		s.notFound(w, r)
		return
	}
	if err := s.store.DeletePost(id); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	http.Redirect(w, r, s.adminListRedirect("/admin/pages", r), http.StatusSeeOther)
}

func pageFromForm(r *http.Request, id int64, lang string) (*store.Post, string) {
	_ = r.ParseForm()
	p := &store.Post{
		ID:                id,
		Type:              "page",
		Lang:              lang,
		Title:             strings.TrimSpace(r.FormValue("title")),
		Excerpt:           strings.TrimSpace(r.FormValue("excerpt")),
		Content:           r.FormValue("content"),
		MetaDesc:          strings.TrimSpace(r.FormValue("meta_desc")),
		Keywords:          strings.TrimSpace(r.FormValue("keywords")),
		Author:            strings.TrimSpace(r.FormValue("author")),
		Status:            "published",
		EditorMode:        "markdown",
		TransGroup:        strings.TrimSpace(r.FormValue("trans_group")),
		RobotsOverride:    strings.TrimSpace(r.FormValue("robots_override")),
		CanonicalOverride: strings.TrimSpace(r.FormValue("canonical_override")),
	}
	if r.FormValue("editor_mode") == "rich" {
		p.EditorMode = "rich"
	}
	slug := slugify(strings.TrimSpace(r.FormValue("slug")))
	if slug == "" {
		slug = slugify(p.Title)
	}
	if slug == "" {
		slug = "page-" + strconv.FormatInt(time.Now().Unix(), 36)
	}
	p.Slug = slug
	if p.Title == "" {
		return p, "标题不能为空。"
	}
	return p, ""
}

// postFromForm 从表单构建 Post；返回校验错误信息（空表示通过）。lang 为该文章语种。
func postFromForm(r *http.Request, id int64, lang string) (*store.Post, string) {
	_ = r.ParseForm()
	p := &store.Post{
		ID:                id,
		Type:              "post",
		Lang:              lang,
		Title:             strings.TrimSpace(r.FormValue("title")),
		Excerpt:           strings.TrimSpace(r.FormValue("excerpt")),
		Content:           r.FormValue("content"),
		MetaDesc:          strings.TrimSpace(r.FormValue("meta_desc")),
		Keywords:          strings.TrimSpace(r.FormValue("keywords")),
		CoverImage:        strings.TrimSpace(r.FormValue("cover_image")),
		Author:            strings.TrimSpace(r.FormValue("author")),
		TransGroup:        strings.TrimSpace(r.FormValue("trans_group")),
		LinkURL:           strings.TrimSpace(r.FormValue("link_url")),
		RobotsOverride:    strings.TrimSpace(r.FormValue("robots_override")),
		CanonicalOverride: strings.TrimSpace(r.FormValue("canonical_override")),
	}
	if r.FormValue("comments_enabled") == "1" {
		p.CommentsEnabled = true
	}
	switch r.FormValue("status") {
	case "published":
		p.Status = "published"
	case "scheduled":
		p.Status = "scheduled"
		if t, err := time.ParseInLocation("2006-01-02T15:04", strings.TrimSpace(r.FormValue("publish_at")), time.Local); err == nil {
			p.PublishedAt = t
		}
	default:
		p.Status = "draft"
	}
	if cid := strings.TrimSpace(r.FormValue("category_id")); cid != "" {
		if n, err := strconv.ParseInt(cid, 10, 64); err == nil {
			p.CategoryID = sql.NullInt64{Int64: n, Valid: true}
		}
	}
	slug := slugify(strings.TrimSpace(r.FormValue("slug")))
	if slug == "" {
		slug = slugify(p.Title)
	}
	if slug == "" {
		slug = "post-" + strconv.FormatInt(time.Now().Unix(), 36)
	}
	p.Slug = slug
	p.EditorMode = "markdown"
	if r.FormValue("editor_mode") == "rich" {
		p.EditorMode = "rich"
	}

	if p.Title == "" {
		return p, "标题不能为空"
	}
	return p, ""
}

// preserveSEOOverrides 表单里没有 SEO 覆盖字段时（如扩展类型编辑器等旧表单），保留库内原值，
// 防止后台保存把自动化 API 写入的 robots/canonical 覆盖悄悄清空。
func preserveSEOOverrides(r *http.Request, p, existing *store.Post) {
	if _, ok := r.PostForm["robots_override"]; !ok {
		p.RobotsOverride = existing.RobotsOverride
	}
	if _, ok := r.PostForm["canonical_override"]; !ok {
		p.CanonicalOverride = existing.CanonicalOverride
	}
}

// uniqueSlug 在某语种内 slug 冲突时追加数字后缀。
func (s *Server) uniqueSlug(lang, slug string, exceptID int64) string {
	base, n := slug, 2
	for {
		exists, err := s.store.SlugExists(lang, slug, exceptID)
		if err != nil || !exists {
			return slug
		}
		slug = base + "-" + strconv.Itoa(n)
		n++
	}
}

// slugify 把字符串转为 URL 友好的 slug（保留 ASCII 字母数字，其余转连字符）。
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == ' ' || r == '_' || r == '.':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// ---------- 图片上传 ----------

var allowedUploadExt = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".webp": true, ".svg": true, ".ico": true, ".avif": true,
}

func uploadJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

type uploadResult struct {
	URL  string `json:"url"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func (s *Server) saveUploadFile(file io.Reader, filename string) (uploadResult, error) {
	if s.uploadDir == "" {
		return uploadResult{}, fmt.Errorf("upload_disabled")
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if !allowedUploadExt[ext] {
		return uploadResult{}, fmt.Errorf("bad_type")
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return uploadResult{}, fmt.Errorf("write_failed")
	}
	head = head[:n]
	if !validUploadContent(ext, head) {
		return uploadResult{}, fmt.Errorf("bad_type")
	}
	name := randToken()[:20] + ext
	out, err := os.Create(filepath.Join(s.uploadDir, name))
	if err != nil {
		return uploadResult{}, fmt.Errorf("save_failed")
	}
	defer out.Close()
	if len(head) > 0 {
		if _, err := out.Write(head); err != nil {
			return uploadResult{}, fmt.Errorf("write_failed")
		}
	}
	m, err := io.Copy(out, file)
	if err != nil {
		return uploadResult{}, fmt.Errorf("write_failed")
	}
	return uploadResult{URL: "/uploads/" + name, Name: name, Size: int64(len(head)) + m}, nil
}

func validUploadContent(ext string, head []byte) bool {
	if len(head) == 0 {
		return false
	}
	sniff := http.DetectContentType(head)
	switch ext {
	case ".jpg", ".jpeg":
		return sniff == "image/jpeg"
	case ".png":
		return sniff == "image/png"
	case ".gif":
		return sniff == "image/gif"
	case ".webp":
		return sniff == "image/webp" || isWebP(head)
	case ".avif":
		return sniff == "image/avif" || isAVIF(head)
	case ".ico":
		return sniff == "image/x-icon" || isICO(head)
	case ".svg":
		return isSVG(head)
	default:
		return false
	}
}

func isWebP(b []byte) bool {
	return len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
}

func isAVIF(b []byte) bool {
	if len(b) < 12 || string(b[4:8]) != "ftyp" {
		return false
	}
	head := b
	if len(head) > 64 {
		head = head[:64]
	}
	return bytes.Contains(head, []byte("avif")) || bytes.Contains(head, []byte("avis"))
}

func isICO(b []byte) bool {
	return len(b) >= 4 && b[0] == 0 && b[1] == 0 && (b[2] == 1 || b[2] == 2) && b[3] == 0
}

func isSVG(b []byte) bool {
	trimmed := bytes.ToLower(bytes.TrimSpace(b))
	return bytes.HasPrefix(trimmed, []byte("<svg")) || bytes.Contains(trimmed, []byte("<svg"))
}

// adminUpload 接收 multipart 图片，存到 uploadDir，返回 {"url":"/uploads/<name>"}。
func (s *Server) adminUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20) // 限制 8MB
	// 必须先解析 multipart，_csrf 字段才进入 r.Form，否则 checkCSRF 取不到。
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		uploadJSON(w, http.StatusBadRequest, `{"error":"表单解析失败或文件过大"}`)
		return
	}
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		uploadJSON(w, http.StatusBadRequest, `{"error":"未收到文件"}`)
		return
	}
	defer file.Close()
	result, err := s.saveUploadFile(file, hdr.Filename)
	if err != nil {
		switch err.Error() {
		case "upload_disabled":
			uploadJSON(w, http.StatusServiceUnavailable, `{"error":"上传未启用"}`)
		case "bad_type":
			uploadJSON(w, http.StatusBadRequest, `{"error":"仅支持 jpg/png/gif/webp/svg/ico/avif"}`)
		case "save_failed":
			uploadJSON(w, http.StatusInternalServerError, `{"error":"保存失败"}`)
		default:
			uploadJSON(w, http.StatusBadRequest, `{"error":"文件过大或写入失败"}`)
		}
		return
	}
	body, err := json.Marshal(map[string]string{"url": result.URL})
	if err != nil {
		uploadJSON(w, http.StatusBadRequest, `{"error":"文件过大或写入失败"}`)
		return
	}
	uploadJSON(w, http.StatusOK, string(body))
}

// adminRender 把请求体里的 Markdown 渲染成 HTML，供富文本编辑器进入时初始化。
func (s *Server) adminRender(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	policy := s.externalLinkPolicy()
	html, _ := RenderContentWithLinkPolicy(string(body), s.imageSizes, &policy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(html))
}
