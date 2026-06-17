package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"cms.ccvar.com/internal/i18n"
	"cms.ccvar.com/internal/seo"
	"cms.ccvar.com/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// ---------- 会话 ----------

const (
	cookieName      = "ccvar_sess"
	adminLangCookie = "gcms_admin_lang"
)

type session struct {
	user        string
	csrf        string
	exp         time.Time
	pwDismissed bool // 本次会话已关闭「修改默认密码」提示（下次登录重新提示）
}

type sessions struct {
	store *store.Store
}

func newSessions(st *store.Store) *sessions { return &sessions{store: st} }

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
	return session{user: dbSess.User, csrf: dbSess.CSRF, exp: dbSess.ExpiresAt, pwDismissed: dbSess.PwDismissed}, true
}

func (s *sessions) destroy(tok string) {
	_ = s.store.DeleteAdminSession(tok)
}

// dismissPw 标记该会话已关闭默认密码提示。
func (s *sessions) dismissPw(tok string) {
	_ = s.store.DismissAdminPasswordWarning(tok)
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
		if _, ok := s.currentSession(r); !ok {
			if wantsJSON(r) {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login_required", "message": "登录已过期，请重新登录。"})
				return
			}
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
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

func (s *Server) adminI18NOverrides(lang string) map[string]string {
	return i18n.ParseAdminOverrides(s.store.Setting(s.adminI18NKey(lang)))
}

func adminTitleKey(title string) string {
	switch title {
	case "登录":
		return "admin.login.title"
	case "概览":
		return "admin.dashboard.title"
	case "文章":
		return "admin.posts.title"
	case "链接":
		return "admin.links.title"
	case "页面":
		return "admin.pages.title"
	case "设置":
		return "admin.settings.title"
	case "可视化编辑":
		return "admin.nav.visual"
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
		Site:        site,
		SEO:         seo.Meta{Title: titleText + " — " + site.Name + " " + suffix, Robots: "noindex, nofollow"},
		Year:        time.Now().Year(),
		Tr:          s.i18n.Tr(def, def),
		Lang:        def,
		Admin:       admin,
		AdminLang:   adminLang,
		AdminLangs:  s.i18n.AdminLocales(),
		AdminReturn: adminReturn,
		EditLang:    def,
		Locales:     s.locales(),
		AllLocales:  s.i18n.All(),
		AssetVer:    s.assetVer,
	}
}

// authed 填充已登录后台页的公共字段：登录态、CSRF、默认密码提示。
func (s *Server) authed(v *View, sess session) {
	v.Authed = true
	v.CSRF = sess.csrf
	v.ShowPwWarn = !sess.pwDismissed && s.store.IsDefaultPassword()
}

// catKind 取分类管理当前的类型（post|link，来自 ?kind= 或表单）。
func catKind(r *http.Request) string {
	if r.URL.Query().Get("kind") == "link" || r.FormValue("kind") == "link" {
		return "link"
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
	if _, ok := s.currentSession(r); ok {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
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
	storedUser, _ := s.store.GetSetting("admin_user")
	hash, _ := s.store.GetSetting("admin_password_hash")
	if user == storedUser && hash != "" && bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)) == nil {
		s.login.reset(ip)
		tok, err := s.sess.create(user)
		if err != nil {
			s.serverError(w, err)
			return
		}
		s.setSessionCookie(w, r, tok)
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	s.login.fail(ip)
	v := s.adminView(r, "登录")
	v.FormErr = v.Admin.T("admin.login.bad_credentials", "用户名或密码错误")
	s.rnd.Admin(w, "login", http.StatusUnauthorized, v)
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
	case "draft", "published", "scheduled":
		return strings.TrimSpace(r.URL.Query().Get("status"))
	default:
		return ""
	}
}

func (s *Server) adminListRedirect(base string, r *http.Request) string {
	parts := []string{}
	if lang := strings.TrimSpace(r.FormValue("lang")); s.langEnabled(lang) {
		parts = append(parts, "lang="+lang)
	}
	switch status := strings.TrimSpace(r.FormValue("status")); status {
	case "draft", "published", "scheduled":
		parts = append(parts, "status="+status)
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

func (s *Server) adminPosts(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	lang := s.editLang(r)
	status := adminStatusFilter(r)
	page := pageParam(r)
	total, err := s.store.CountAdminContent("post", lang, status)
	if err != nil {
		s.serverError(w, err)
		return
	}
	totalPages := ceilDiv(total, adminListPageSize)
	if totalPages > 0 && page > totalPages {
		page = totalPages
	}
	posts, err := s.store.ListAdminContent("post", lang, status, (page-1)*adminListPageSize, adminListPageSize)
	if err != nil {
		s.serverError(w, err)
		return
	}
	v := s.adminView(r, "文章")
	s.authed(v, sess)
	v.AllPosts = posts
	v.ListTotal = total
	v.StatusFilter = status
	v.AdminListPath = "/admin/posts"
	v.EditLang = lang
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
	if s.store.IsDefaultPassword() {
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
			Href:  "/admin/settings/updates",
			Icon:  "version",
			Tone:  "neutral",
		},
		{
			Label: v.Admin.T("admin.dashboard.status.password", "后台密码"),
			Value: passwordValue,
			Hint:  passwordHint,
			Href:  "/admin/settings/security",
			Icon:  "lock",
			Tone:  passwordTone,
		},
	}
}

func (s *Server) adminVisual(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	lang := s.editLang(r)
	v := s.adminView(r, "可视化编辑")
	s.authed(v, sess)
	v.EditLang = lang
	v.VisualPreviewURL = "/" + lang + "/?visual_edit=1"
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
		text("site", "site.tagline", t("admin.visual.field.tagline", "标语"), st.Tagline, t("admin.visual.hint.tagline", "用于浏览器标题和部分主题的辅助文案。"), false),
		text("site", "site.description", t("admin.visual.field.description", "站点描述"), st.Description, t("admin.visual.hint.description", "首页 Hero 描述，也会作为默认 SEO description。"), true),
		image("site", "site.logo", t("admin.visual.field.logo", "站点 Logo"), s.store.Setting("site.logo"), t("admin.visual.hint.logo", "显示在页眉和页脚，留空时使用内置默认 Logo。")),
		image("site", "site.favicon", t("admin.visual.field.favicon", "浏览器图标"), nonEmpty(s.store.Setting("site.favicon"), defaultFaviconPath), t("admin.visual.hint.favicon", "显示在浏览器标签页，建议使用 SVG、PNG 或 ICO。")),
		image("site", "site.share_image", t("admin.visual.field.share_image", "分享图"), nonEmpty(s.store.Setting("site.share_image"), defaultShareImageURL), t("admin.visual.hint.share_image", "分享到微信、飞书、X 等平台时默认显示，建议 1200×630。")),
		text("home", "site.hero_eyebrow", t("admin.visual.field.hero_eyebrow", "Hero 眉标"), st.HeroEyebrow, t("admin.visual.hint.hero_eyebrow", "首页主标题上方的小字。"), false),
		text("home", "site.hero_title", t("admin.visual.field.hero_title", "Hero 大标题"), st.HeroTitle, t("admin.visual.hint.hero_title", "首页第一屏最醒目的标题，建议短一点。"), true),
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
			path := "/links?cat=" + c.Slug
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
	case "site.name", "site.tagline", "site.description", "site.hero_eyebrow", "site.hero_title",
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
		return "site.share_image"
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
	status := http.StatusOK
	if formErr != "" {
		status = http.StatusBadRequest
	}
	s.rnd.Admin(w, "edit", status, v)
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
	s.fillDefaultAuthor(p)
	p.Slug = s.uniqueSlug(existing.Lang, p.Slug, id)
	if err := s.store.UpdatePost(p); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
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
	page := pageParam(r)
	total, err := s.store.CountAdminContent("link", lang, status)
	if err != nil {
		s.serverError(w, err)
		return
	}
	totalPages := ceilDiv(total, adminListPageSize)
	if totalPages > 0 && page > totalPages {
		page = totalPages
	}
	links, err := s.store.ListAdminContent("link", lang, status, (page-1)*adminListPageSize, adminListPageSize)
	if err != nil {
		s.serverError(w, err)
		return
	}
	v := s.adminView(r, "链接")
	s.authed(v, sess)
	v.AllPosts = links
	v.ListTotal = total
	v.StatusFilter = status
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
	s.fillDefaultAuthor(p)
	p.Slug = s.uniqueSlug(existing.Lang, p.Slug, id)
	if err := s.store.UpdatePost(p); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
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

// ---------- 站点设置（分区独立保存）----------

var settingsSections = map[string]bool{"site": true, "appearance": true, "copy": true, "menu": true, "languages": true, "categories": true, "automation": true, "comments": true, "updates": true, "security": true}

func themeName(id string) string {
	for _, t := range Themes {
		if t.ID == id {
			return t.Name
		}
	}
	return id
}

var themeDescEN = map[string]string{
	"editorial":   "Warm serif, single-column reading list",
	"magazine":    "Sans-serif masthead, centered lead, three-column cards",
	"terminal":    "Monospace, dark, terminal-style content lists",
	"brutalist":   "High contrast, heavy borders, hard shadows, square edges",
	"notebook":    "Paper texture, ruled lines, warm highlights",
	"swiss":       "International grid, red-black palette, large numbering",
	"pastel":      "Soft gradients, rounded cards, friendly tone",
	"newspaper":   "Multi-column serif layout with editorial rhythm",
	"darkpro":     "Modern dark mode, indigo glow, card grid",
	"landing":     "Product landing layout with hero, CTAs and feature cards",
	"product":     "Product site, docs entry and release-note structure",
	"prism":       "Dark poster mood with multi-color signal lines",
	"exchange":    "Web3 growth page with market-dashboard energy",
	"academy":     "AI course and education layout with readable cards",
	"garment":     "B2B garment factory layout with catalog feel",
	"institution": "Professional service, consulting or association website",
	"studio":      "Image-led portfolio for design, photography or studios",
	"lifestyle":   "Warm small-brand site for cafes, stays or boutiques",
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
	return ThemeOption{ID: t.ID, Name: name, Desc: desc}
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
		s.showSettings(w, r, "updates", "", st.Message)
		return
	}
	if st.Running {
		if jsonReq {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "message": "已有升级任务正在运行。", "status": st})
			return
		}
		s.showSettings(w, r, "updates", "", "已有升级任务正在运行。")
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
		s.showSettings(w, r, "updates", "", "启动升级器失败："+err.Error())
		return
	}
	if jsonReq {
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "升级任务已启动。", "status": readUpgradeStatus()})
		return
	}
	s.showSettings(w, r, "updates", "升级任务已启动，页面会显示最新状态。", "")
}

// copyKey 返回某语种下文案设置的存储键：默认语种用裸键，其它语种用 key::lang。
func (s *Server) copyKey(base, lang string) string {
	if lang == s.defaultLang() {
		return base
	}
	return base + "::" + lang
}

func (s *Server) showSettings(w http.ResponseWriter, r *http.Request, section, flash, formErr string, newAPISecret ...string) {
	sess, _ := s.currentSession(r)
	st := s.site(s.defaultLang())
	adminLang := s.adminLang(r)
	// 当前主题的微调（作为控件初值）
	custom, accent, radius := s.themeTweak(st.Theme)
	cards := make([]ThemeCard, 0, len(Themes))
	for _, t := range Themes {
		display := themeOptionForAdmin(t, adminLang)
		c, a, rd := s.themeTweak(t.ID)
		cards = append(cards, ThemeCard{ID: t.ID, Name: display.Name, Desc: display.Desc, Accent: a, Radius: rd, Custom: c})
	}
	v := s.adminView(r, "设置")
	s.authed(v, sess)
	v.Section = section
	v.CatKind = catKind(r)
	favicon := nonEmpty(st.Favicon, defaultFaviconPath)
	shareImage := nonEmpty(st.ShareImage, defaultShareImageURL)
	v.Settings = &SettingsForm{
		Name: st.Name, Tagline: st.Tagline, Description: st.Description,
		NameDef: st.Name, TaglineDef: st.Tagline, DescriptionDef: st.Description,
		Favicon: favicon, Logo: st.Logo, ShareImage: shareImage, Brand: st.Brand, Theme: st.Theme,
		Custom: custom, Accent: accent, Radius: radius,
		HeroEyebrow: st.HeroEyebrow, HeroTitle: st.HeroTitle, FooterNote: st.FooterNote,
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
	}
	v.Themes = make([]ThemeOption, 0, len(Themes))
	for _, t := range Themes {
		v.Themes = append(v.Themes, themeOptionForAdmin(t, v.AdminLang))
	}
	v.Cards = cards
	v.Flash = flash
	v.FormErr = formErr
	v.AdminI18NJSON = s.store.Setting(s.adminI18NKey(v.AdminLang))
	v.Social = parseSocialLinks(s.store.Setting("social_links"))
	v.APIBaseURL = s.absForRequest(r, "/api/admin/v1")
	v.OpenAPIURL = s.absForRequest(r, "/api/admin/v1/openapi.json")
	v.APIDocsURL = s.absForRequest(r, "/"+s.defaultLang()+"/api-docs")
	v.SkillPackageURL = "/admin/settings/automation/skill.zip"
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
	case "site":
		lang := s.editLang(r)
		v.EditLang = lang
		def := s.site(s.defaultLang())
		v.Settings.NameDef = def.Name
		v.Settings.TaglineDef = def.Tagline
		v.Settings.DescriptionDef = def.Description
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
		kind := catKind(r)
		v.EditLang = lang
		v.CatKind = kind
		all := s.archiveConfig(lang, kind)
		v.Settings.AllTitle = all.Title
		v.Settings.AllLabel = all.Label
		v.Settings.AllSlug = all.Slug
		v.Settings.AllPath = all.Path
		v.Settings.AllDescription = all.Description
		v.Categories, _ = s.store.ListCategories(lang, kind)
		if eid := r.URL.Query().Get("edit"); eid != "" {
			v.EditCat, _ = s.store.GetCategoryByID(atoi64(eid))
		}
	case "languages":
		v.CustomLocales = s.i18n.Custom()
		v.AdminI18NJSON = s.store.Setting(s.adminI18NKey(v.AdminLang))
	case "menu":
		lang := v.AdminLang
		if !s.langEnabled(lang) {
			lang = s.defaultLang()
		}
		v.EditLang = lang
		v.MenuTargets = s.menuTargetOptions(v.Admin)
		v.MenuEdit = s.menuEditRows(v.Admin)
	case "automation":
		v.AutomationKeys, _ = s.store.ListAutomationKeys()
		v.AutomationLogs, _ = s.store.ListAutomationLogs(20)
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
	s.showSettings(w, r, "automation", "访问权限已创建，请在列表中点“查看”复制密钥。", "", token, strings.Join(scopes, ","), name, strconv.FormatInt(id, 10))
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
	s.showSettings(w, r, "automation", "访问权限已更新。", "")
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
		"不要增删改站点设置、分类、导航、安全、系统更新。",
		"",
		"默认只创建或修改草稿。除非我明确要求，并且你有发布权限，否则不要发布内容。",
		"",
		"需要处理多语种内容时，先查看启用语种：",
		"GET /languages",
		"",
		"需要设置分类时，先查看可用分类 ID：",
		"GET /posts/categories?lang=zh",
		"GET /links/categories?lang=zh",
		"",
		"需要设置封面图或正文图片时，先上传媒体文件，拿返回的 url 再写入 cover_image 或 Markdown 图片：",
		"POST /media",
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
	s.showSettings(w, r, "automation", "访问权限已吊销。", "")
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
	s.showSettings(w, r, "automation", "已删除这条访问权限记录。", "")
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
	s.showSettings(w, r, "automation", "访问密钥已重新生成，请在列表中点“查看”复制新密钥。", "", token, key.Scopes, key.Name, strconv.FormatInt(id, 10))
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
		if want[pub] {
			want[write] = true
		}
		if want[write] {
			want[read] = true
		}
	}
	var out []string
	if want["languages:read"] {
		out = append(out, "languages:read")
	}
	if want["media:write"] {
		out = append(out, "media:write")
	}
	for _, col := range automationCollections {
		for _, action := range automationScopeActions(col.path) {
			scope := apiScope(col.path, action)
			if want[scope] {
				out = append(out, scope)
			}
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
	if scope == "languages:read" || scope == "media:write" {
		return true
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
	out := []string{"languages:read", "media:write"}
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
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	lang := s.editLang(r)
	name := strings.TrimSpace(r.FormValue("site_name"))
	if lang == s.defaultLang() && name == "" {
		s.showSettings(w, r, "site", "", "站点名称不能为空。")
		return
	}
	_ = s.store.SetSetting(s.copyKey("site.name", lang), name)
	_ = s.store.SetSetting(s.copyKey("site.tagline", lang), strings.TrimSpace(r.FormValue("site_tagline")))
	_ = s.store.SetSetting(s.copyKey("site.description", lang), strings.TrimSpace(r.FormValue("site_description")))
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
	_ = s.store.SetSetting("site.share_image", shareImage)
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
	_ = s.store.SetSetting("inject.head", strings.TrimSpace(r.FormValue("inject_head")))
	_ = s.store.SetSetting("inject.body", strings.TrimSpace(r.FormValue("inject_body")))
	s.clearGeneratedCaches()
	s.showSettings(w, r, "site", "基础信息已保存。", "")
}

func (s *Server) adminSaveAppearance(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	theme := r.FormValue("theme")
	if !validTheme(theme) {
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

	// 首页 Hero 右侧视觉（默认动画 / 图片或 SVG 文件 / 内联 SVG 代码）——全局
	hv := r.FormValue("hero_visual")
	if hv != "image" && hv != "svg" {
		hv = ""
	}
	_ = s.store.SetSetting("hero.visual", hv)
	_ = s.store.SetSetting("hero.image", strings.TrimSpace(r.FormValue("hero_image")))
	_ = s.store.SetSetting("hero.svg", strings.TrimSpace(r.FormValue("hero_svg")))

	s.clearGeneratedCaches()
	s.showSettings(w, r, "appearance", "外观设置已保存。", "")
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
	s.showSettings(w, r, "comments", "评论设置已保存。", "")
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

func (s *Server) adminSavePassword(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	cur := r.FormValue("current_password")
	neu := r.FormValue("new_password")
	conf := r.FormValue("confirm_password")
	hash, _ := s.store.GetSetting("admin_password_hash")
	switch {
	case bcrypt.CompareHashAndPassword([]byte(hash), []byte(cur)) != nil:
		s.showSettings(w, r, "security", "", "当前密码不正确。")
		return
	case len([]rune(neu)) < 6:
		s.showSettings(w, r, "security", "", "新密码至少 6 位。")
		return
	case neu != conf:
		s.showSettings(w, r, "security", "", "两次输入的新密码不一致。")
		return
	}
	if nh, err := bcrypt.GenerateFromPassword([]byte(neu), bcrypt.DefaultCost); err == nil {
		_ = s.store.SetSetting("admin_password_hash", string(nh))
	}
	s.showSettings(w, r, "security", "密码已更新。", "")
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
			s.showSettings(w, r, "languages", "", v.Admin.T("admin.settings.admin_i18n.invalid", "后台翻译 JSON 格式不正确。"))
			return
		}
	}
	overrides := i18n.ParseAdminOverrides(raw)
	if err := s.store.SetSetting(s.adminI18NKey(lang), i18n.MarshalAdminOverrides(overrides)); err != nil {
		s.serverError(w, err)
		return
	}
	v := s.adminView(r, "设置")
	s.showSettings(w, r, "languages", v.Admin.T("admin.settings.admin_i18n.saved", "后台翻译已保存。"), "")
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
	s.showSettings(w, r, "security", "演示数据已清除。", "")
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
	s.showSettings(w, r, "security", "产品演示站已重新载入。", "")
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
	s.showSettings(w, r, "copy", "文案已保存。", "")
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
	s.showSettings(w, r, "menu", "导航菜单已保存。", "")
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
	s.showSettings(w, r, "languages", "语言设置已保存。", "")
}

// adminAddLocalePreset 新增一个自定义语种预设（存 settings.custom_locales）。
func (s *Server) adminAddLocalePreset(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	code := strings.ToLower(strings.TrimSpace(r.FormValue("code")))
	if !i18n.ValidCode(code) {
		s.showSettings(w, r, "languages", "", "语种代码非法：需 2–12 位小写字母 / 数字 / 连字符（如 pt、zh-tw）。")
		return
	}
	if s.i18n.Known(code) {
		s.showSettings(w, r, "languages", "", "该语种代码已存在（内置或已添加）。")
		return
	}
	cur := s.i18n.Custom()
	cur = append(cur, i18n.Locale{
		Code: code,
		Name: strings.TrimSpace(r.FormValue("name")),
		Tag:  strings.TrimSpace(r.FormValue("tag")),
		OG:   strings.TrimSpace(r.FormValue("og")),
	})
	_ = s.store.SetSetting("custom_locales", i18n.MarshalCustom(cur))
	s.i18n.LoadCustom(s.store.Setting("custom_locales"))
	s.clearGeneratedCaches()
	s.showSettings(w, r, "languages", "已新增语种预设，可在上方勾选启用。", "")
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
	// 同步清理启用列表（Active 会自动丢弃已不可用的语种码）
	act := s.locales()
	codes := make([]string, 0, len(act))
	for _, l := range act {
		codes = append(codes, l.Code)
	}
	_ = s.store.SetSetting("locales", strings.Join(codes, ","))
	_ = s.store.SetSetting("default_lang", codes[0])
	s.clearGeneratedCaches()
	s.showSettings(w, r, "languages", "已删除语种预设。", "")
}

// ---------- 分类管理 ----------

func (s *Server) adminSaveCategoryAll(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	lang := s.editLang(r)
	kind := catKind(r)
	title := strings.TrimSpace(r.FormValue("title"))
	label := strings.TrimSpace(r.FormValue("label"))
	desc := strings.TrimSpace(r.FormValue("description"))
	if title == "" {
		s.showSettings(w, r, "categories", "", "页面标题不能为空。")
		return
	}
	if label == "" {
		s.showSettings(w, r, "categories", "", "“全部”按钮文字不能为空。")
		return
	}
	fallbackSlug := "category"
	if kind == "link" {
		fallbackSlug = "links"
	}
	slug := normalizeArchiveSlug(r.FormValue("slug"), fallbackSlug)
	newPath := archivePath(kind, slug)
	if kind == "post" && newPath != "/category" {
		exists, err := s.store.CategorySlugExists(lang, slug, 0)
		if err != nil {
			s.serverError(w, err)
			return
		}
		if exists {
			s.showSettings(w, r, "categories", "", "Slug 已被真实分类占用，请换一个。")
			return
		}
	} else if kind == "link" && newPath != "/links" {
		if p, err := s.store.GetLinkBySlug(lang, slug, true); err != nil {
			s.serverError(w, err)
			return
		} else if p != nil {
			s.showSettings(w, r, "categories", "", "Slug 已被链接详情页占用，请换一个。")
			return
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
			s.serverError(w, err)
			return
		}
	}
	if kind == "post" {
		s.syncCategoryNavPath(old.Path, newPath)
	}
	s.clearGeneratedCaches()
	s.showSettings(w, r, "categories", "“全部”入口已保存。", "")
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
	c := &store.Category{ID: id, Slug: slug, Name: name, Description: strings.TrimSpace(r.FormValue("description")), Lang: lang, Kind: catKind(r)}
	if id == 0 {
		if _, err := s.store.CreateCategory(c); err != nil {
			s.serverError(w, err)
			return
		}
		s.clearGeneratedCaches()
		s.showSettings(w, r, "categories", "分类已添加。", "")
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
	s.showSettings(w, r, "categories", "分类已更新。", "")
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
	s.showSettings(w, r, "categories", "分类已删除。", "")
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
	p.Content = r.FormValue("content")
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		s.showEdit(w, r, sess, p, "", "标题不能为空。")
		return
	}
	p.Title = title
	p.Excerpt = strings.TrimSpace(r.FormValue("excerpt"))
	p.MetaDesc = strings.TrimSpace(r.FormValue("meta_desc"))
	p.Keywords = strings.TrimSpace(r.FormValue("keywords"))
	p.Author = strings.TrimSpace(r.FormValue("author"))
	p.EditorMode = "markdown"
	if r.FormValue("editor_mode") == "rich" {
		p.EditorMode = "rich"
	}
	if slug := slugify(strings.TrimSpace(r.FormValue("slug"))); slug != "" {
		p.Slug = s.uniqueSlug(p.Lang, slug, id)
	}
	p.Status = "published"
	if err := s.store.UpdatePost(p); err != nil {
		s.serverError(w, err)
		return
	}
	s.clearGeneratedCaches()
	http.Redirect(w, r, fmt.Sprintf("/admin/pages/%d/edit?saved=1", id), http.StatusSeeOther)
}

// postFromForm 从表单构建 Post；返回校验错误信息（空表示通过）。lang 为该文章语种。
func postFromForm(r *http.Request, id int64, lang string) (*store.Post, string) {
	_ = r.ParseForm()
	p := &store.Post{
		ID:         id,
		Type:       "post",
		Lang:       lang,
		Title:      strings.TrimSpace(r.FormValue("title")),
		Excerpt:    strings.TrimSpace(r.FormValue("excerpt")),
		Content:    r.FormValue("content"),
		MetaDesc:   strings.TrimSpace(r.FormValue("meta_desc")),
		Keywords:   strings.TrimSpace(r.FormValue("keywords")),
		CoverImage: strings.TrimSpace(r.FormValue("cover_image")),
		Author:     strings.TrimSpace(r.FormValue("author")),
		TransGroup: strings.TrimSpace(r.FormValue("trans_group")),
		LinkURL:    strings.TrimSpace(r.FormValue("link_url")),
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
	html, _ := RenderContentWithImages(string(body), s.imageSizes)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(html))
}
