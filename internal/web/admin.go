package web

import (
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

const cookieName = "ccvar_sess"

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
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
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

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	// 生产（BASE_URL 为 https）时加 Secure，仅经 HTTPS 传输 Cookie；本地 http 开发不加以免无法登录。
	secure := strings.HasPrefix(s.baseURL, "https://")
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 86400})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
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

func (s *Server) adminView(title string) *View {
	def := s.defaultLang()
	site := s.site(def)
	return &View{
		Site:       site,
		SEO:        seo.Meta{Title: title + " — " + site.Name + " 后台", Robots: "noindex, nofollow"},
		Year:       time.Now().Year(),
		Tr:         s.i18n.Tr(def, def),
		Lang:       def,
		EditLang:   def,
		Locales:    s.locales(),
		AllLocales: s.i18n.All(),
		AssetVer:   s.assetVer,
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
	s.rnd.Admin(w, "login", http.StatusOK, s.adminView("登录"))
}

func (s *Server) adminLoginPost(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	ip := clientIP(r)
	// 防穷举：同一 IP 短时间内多次失败则暂时锁定
	if wait := s.login.lockedFor(ip); wait > 0 {
		v := s.adminView("登录")
		v.FormErr = fmt.Sprintf("登录尝试过于频繁，请约 %d 分钟后再试。", int(wait/time.Minute)+1)
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
		s.setSessionCookie(w, tok)
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	s.login.fail(ip)
	v := s.adminView("登录")
	v.FormErr = "用户名或密码错误"
	s.rnd.Admin(w, "login", http.StatusUnauthorized, v)
}

func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		if sess, ok := s.sess.get(c.Value); ok && r.FormValue("_csrf") == sess.csrf {
			s.sess.destroy(c.Value)
		}
	}
	s.clearSessionCookie(w)
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

func (s *Server) adminDashboard(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	lang := s.editLang(r)
	posts, err := s.store.ListAllPosts(lang)
	if err != nil {
		s.serverError(w, err)
		return
	}
	v := s.adminView("文章")
	s.authed(v, sess)
	v.AllPosts = posts
	v.EditLang = lang
	s.rnd.Admin(w, "dashboard", http.StatusOK, v)
}

func (s *Server) adminVisual(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	lang := s.editLang(r)
	v := s.adminView("可视化编辑")
	s.authed(v, sess)
	v.EditLang = lang
	v.VisualPreviewURL = "/" + lang + "/?visual_edit=1"
	v.VisualFields = s.visualFields(lang)
	v.VisualGroups = visualGroups(v.VisualFields)
	v.VisualHistory = s.visualHistory()
	s.rnd.Admin(w, "visual", http.StatusOK, v)
}

func (s *Server) visualFields(lang string) []VisualField {
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
		text("site", "site.name", "站点名称", st.Name, "显示在页眉、页脚和 SEO 站点名中。", false),
		text("site", "site.tagline", "标语", st.Tagline, "用于浏览器标题和部分主题的辅助文案。", false),
		text("site", "site.description", "站点描述", st.Description, "首页 Hero 描述，也会作为默认 SEO description。", true),
		image("site", "site.logo", "站点 Logo", s.store.Setting("site.logo"), "显示在页眉和页脚，留空时使用内置默认 Logo。"),
		image("site", "site.favicon", "浏览器图标", s.store.Setting("site.favicon"), "显示在浏览器标签页，建议使用 SVG、PNG 或 ICO。"),
		text("home", "site.hero_eyebrow", "Hero 眉标", st.HeroEyebrow, "首页主标题上方的小字。", false),
		text("home", "site.hero_title", "Hero 大标题", st.HeroTitle, "首页第一屏最醒目的标题，建议短一点。", true),
		image("home", "hero.image", "Hero 图片", s.store.Setting("hero.image"), "上传后会自动把 Hero 右侧视觉切换为图片模式。"),
		text("home", "home.featured_title", "首页精选标题", st.HomeFeatured, "首页精选文章区块标题。", false),
		text("home", "home.links_title", "首页链接标题", st.HomeLinks, "首页链接区块标题。", false),
		text("home", "home.latest_title", "首页最新标题", st.HomeLatest, "首页最新文章区块标题。", false),
		text("footer", "site.footer_note", "页脚说明", st.FooterNote, "显示在页脚站点名下方。", true),
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
			Hint:      "只修改当前语种的导航名称，导航地址仍在设置里的「导航」维护。",
			Draggable: true,
			Localized: true,
			Inherited: lang != def && explicit == "",
		})
	}
	if about, _ := s.store.GetPage(lang, "about"); about != nil {
		fields = append(fields,
			contextText("about", "page.about.title", "关于标题", about.Title, "", "关于页面的主标题。", false),
			contextText("about", "page.about.excerpt", "关于摘要", about.Excerpt, "", "关于页面标题下方的简短说明。", true),
			contextText("about", "page.about.content", "关于正文", about.Content, "", "支持 Markdown；长内容也可以到「页面」里编辑完整正文。", true),
		)
	}
	categoryAll := s.archiveConfig(lang, "post")
	fields = append(fields,
		contextText("category", "category_all.title", "分类页标题", categoryAll.Title, categoryAll.Path, "文章分类的全部列表页标题。", false),
		contextText("category", "category_all.description", "分类页描述", categoryAll.Description, categoryAll.Path, "显示在文章分类全部页标题下方。", true),
		contextText("categorynav", "category_all.label", "全部按钮", categoryAll.Label, categoryAll.Path, "分类导航里“全部”按钮的显示文字。", false),
	)
	if cats, _ := s.store.ListCategories(lang, "post"); cats != nil {
		for _, c := range cats {
			path := "/category/" + c.Slug
			nameField := contextText("categorynav", "category."+strconv.FormatInt(c.ID, 10)+".name", c.Name, c.Name, path, "分类导航按钮文字；不会改变 URL。", false)
			nameField.Draggable = true
			fields = append(fields,
				nameField,
				contextText("category", "category."+strconv.FormatInt(c.ID, 10)+".description", c.Name+" 描述", c.Description, path, "显示在当前分类页标题下方。", true),
			)
		}
	}
	linksAll := s.archiveConfig(lang, "link")
	fields = append(fields,
		contextText("linkcat", "links_all.title", "链接页标题", linksAll.Title, linksAll.Path, "链接列表页顶部标题。", false),
		contextText("linkcat", "links_all.description", "链接页描述", linksAll.Description, linksAll.Path, "显示在链接列表页标题下方。", true),
		contextText("linkcatnav", "links_all.label", "全部按钮", linksAll.Label, linksAll.Path, "链接分类导航里“全部”按钮的显示文字。", false),
	)
	if cats, _ := s.store.ListCategories(lang, "link"); cats != nil {
		for _, c := range cats {
			path := "/links?cat=" + c.Slug
			nameField := contextText("linkcatnav", "category."+strconv.FormatInt(c.ID, 10)+".name", c.Name, c.Name, path, "链接分类导航按钮文字；不会改变 URL。", false)
			nameField.Draggable = true
			fields = append(fields,
				nameField,
				contextText("linkcat", "category."+strconv.FormatInt(c.ID, 10)+".description", c.Name+" 描述", c.Description, path, "显示在当前链接分类页标题下方。", true),
			)
		}
	}
	return fields
}

func visualGroups(fields []VisualField) []VisualGroup {
	titles := []VisualGroup{
		{ID: "site", Title: "站点信息"},
		{ID: "home", Title: "首页内容"},
		{ID: "about", Title: "关于页面"},
		{ID: "nav", Title: "导航"},
		{ID: "category", Title: "文章分类页"},
		{ID: "categorynav", Title: "文章分类导航"},
		{ID: "linkcat", Title: "链接页"},
		{ID: "linkcatnav", Title: "链接分类导航"},
		{ID: "footer", Title: "页脚"},
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
	storeKey := s.visualStoreKey(key, lang)
	old := s.store.Setting(storeKey)
	_ = s.store.SetSetting(storeKey, value)
	if key == "hero.image" {
		if value != "" {
			_ = s.store.SetSetting("hero.visual", "image")
		} else if s.store.Setting("hero.visual") == "image" {
			_ = s.store.SetSetting("hero.visual", "")
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
		"site.logo", "site.favicon", "hero.image":
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
	case "hero.image":
		return "hero.image"
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
		if h.Old != "" {
			_ = s.store.SetSetting("hero.visual", "image")
		} else if s.store.Setting("hero.visual") == "image" {
			_ = s.store.SetSetting("hero.visual", "")
		}
	}
	return nil
}

func (s *Server) adminNew(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	s.showEdit(w, sess, &store.Post{Status: "draft", Lang: s.editLang(r)}, "", "")
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
	s.showEdit(w, sess, p, flash, "")
}

func (s *Server) showEdit(w http.ResponseWriter, sess session, e *store.Post, flash, formErr string) {
	v := s.adminView("编辑")
	s.authed(v, sess)
	v.Edit = e
	v.IsPage = e.Type == "page"
	v.IsLink = e.Type == "link"
	catKind := "post"
	switch e.Type {
	case "page":
		v.EditBase, v.EditListURL, v.EditTypeLabel = "pages", "/admin/pages", "页面"
	case "link":
		v.EditBase, v.EditListURL, v.EditTypeLabel, catKind = "links", "/admin/links", "链接", "link"
	default:
		v.EditBase, v.EditListURL, v.EditTypeLabel = "posts", "/admin", "文章"
	}
	title := "新建" + v.EditTypeLabel
	if e.ID != 0 {
		title = "编辑" + v.EditTypeLabel
	}
	v.SEO.Title = title + " — " + v.Site.Name + " 后台"
	v.Flash = flash
	v.FormErr = formErr
	lang := e.Lang
	if lang == "" || !s.langEnabled(lang) {
		lang = s.defaultLang()
	}
	v.EditLang = lang
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
		s.showEdit(w, sess, p, "", formErr)
		return
	}
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
		s.showEdit(w, sess, p, "", formErr)
		return
	}
	p.CreatedAt = existing.CreatedAt
	p.Featured = existing.Featured     // 置顶通过单独入口切换，编辑保存时保留
	p.TransGroup = existing.TransGroup // 互译关联固定，编辑保存时保留
	if p.PublishedAt.IsZero() {        // 表单未指定发布时间则沿用原值
		p.PublishedAt = existing.PublishedAt
	}
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
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) adminPin(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	_ = s.store.SetFeatured(atoi64(r.PathValue("id")), r.FormValue("on") == "1")
	s.clearGeneratedCaches()
	lang := strings.TrimSpace(r.FormValue("lang"))
	dest := "/admin"
	if s.langEnabled(lang) {
		dest += "?lang=" + lang
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
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
	links, err := s.store.ListAllLinks(lang)
	if err != nil {
		s.serverError(w, err)
		return
	}
	v := s.adminView("链接")
	s.authed(v, sess)
	v.AllPosts = links
	v.EditLang = lang
	s.rnd.Admin(w, "links", http.StatusOK, v)
}

func (s *Server) adminLinkNew(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.currentSession(r)
	s.showEdit(w, sess, &store.Post{Type: "link", Status: "draft", Lang: s.editLang(r)}, "", "")
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
	s.showEdit(w, sess, p, flash, "")
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
		s.showEdit(w, sess, p, "", formErr)
		return
	}
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
		s.showEdit(w, sess, p, "", formErr)
		return
	}
	p.CreatedAt = existing.CreatedAt
	p.Featured = existing.Featured
	p.TransGroup = existing.TransGroup
	if p.PublishedAt.IsZero() {
		p.PublishedAt = existing.PublishedAt
	}
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
	dest := "/admin/links"
	if lang := strings.TrimSpace(r.FormValue("lang")); s.langEnabled(lang) {
		dest += "?lang=" + lang
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (s *Server) adminLinkPin(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	_ = s.store.SetFeatured(atoi64(r.PathValue("id")), r.FormValue("on") == "1")
	s.clearGeneratedCaches()
	dest := "/admin/links"
	if lang := strings.TrimSpace(r.FormValue("lang")); s.langEnabled(lang) {
		dest += "?lang=" + lang
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
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
	// 当前主题的微调（作为控件初值）
	custom, accent, radius := s.themeTweak(st.Theme)
	cards := make([]ThemeCard, 0, len(Themes))
	for _, t := range Themes {
		c, a, rd := s.themeTweak(t.ID)
		cards = append(cards, ThemeCard{ID: t.ID, Name: t.Name, Desc: t.Desc, Accent: a, Radius: rd, Custom: c})
	}
	v := s.adminView("设置")
	s.authed(v, sess)
	v.Section = section
	v.CatKind = catKind(r)
	v.Settings = &SettingsForm{
		Name: st.Name, Tagline: st.Tagline, Description: st.Description,
		NameDef: st.Name, TaglineDef: st.Tagline, DescriptionDef: st.Description,
		Favicon: st.Favicon, Logo: st.Logo, Brand: st.Brand, Theme: st.Theme,
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
	v.Themes = Themes
	v.Cards = cards
	v.Flash = flash
	v.FormErr = formErr
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
	case "menu":
		v.MenuEdit = s.menuEditRows()
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
		"你可以帮我查看、创建和修改文章、链接、页面。",
		"不要操作站点设置、分类、导航、安全、系统更新。",
		"",
		"默认只创建或修改草稿。除非我明确要求，并且你有发布权限，否则不要发布内容。",
		"",
		"如果我要你修改某篇内容，请先找到它的 id，不要只凭标题猜。",
		"可以这样查找：",
		"GET /posts?lang=zh&q=关键词",
		"GET /posts?lang=zh&slug=slug",
		"GET /pages?lang=zh&q=关键词",
		"GET /links?lang=zh&q=关键词",
		"",
		"找到目标后，再用对应 id 更新：",
		"PATCH /posts/{id}",
		"PATCH /pages/{id}",
		"PATCH /links/{id}",
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
	for _, col := range automationCollections {
		for _, action := range []string{"read", "write", "publish"} {
			scope := apiScope(col.path, action)
			if want[scope] {
				out = append(out, scope)
			}
		}
	}
	if len(out) == 0 {
		for _, col := range automationCollections {
			out = append(out, apiScope(col.path, "read"), apiScope(col.path, "write"))
		}
	}
	return out
}

func automationScopeValid(scope string) bool {
	for _, col := range automationCollections {
		for _, action := range []string{"read", "write", "publish"} {
			if scope == apiScope(col.path, action) {
				return true
			}
		}
	}
	return false
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
	_ = s.store.SetSetting("site.favicon", strings.TrimSpace(r.FormValue("site_favicon")))
	_ = s.store.SetSetting("site.logo", strings.TrimSpace(r.FormValue("site_logo")))
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
	v := s.adminView("页面")
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
	s.showEdit(w, sess, p, flash, "")
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
		s.showEdit(w, sess, p, "", "标题不能为空。")
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

// adminUpload 接收 multipart 图片，存到 uploadDir，返回 {"url":"/uploads/<name>"}。
func (s *Server) adminUpload(w http.ResponseWriter, r *http.Request) {
	if s.uploadDir == "" {
		uploadJSON(w, http.StatusServiceUnavailable, `{"error":"上传未启用"}`)
		return
	}
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
	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	if !allowedUploadExt[ext] {
		uploadJSON(w, http.StatusBadRequest, `{"error":"仅支持 jpg/png/gif/webp/svg"}`)
		return
	}
	name := randToken()[:20] + ext
	out, err := os.Create(filepath.Join(s.uploadDir, name))
	if err != nil {
		uploadJSON(w, http.StatusInternalServerError, `{"error":"保存失败"}`)
		return
	}
	defer out.Close()
	if _, err := io.Copy(out, file); err != nil {
		uploadJSON(w, http.StatusBadRequest, `{"error":"文件过大或写入失败"}`)
		return
	}
	uploadJSON(w, http.StatusOK, `{"url":"/uploads/`+name+`"}`)
}

// adminRender 把请求体里的 Markdown 渲染成 HTML，供富文本编辑器进入时初始化。
func (s *Server) adminRender(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	html, _ := RenderContent(string(body))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(html))
}
