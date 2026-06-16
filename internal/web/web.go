// Package web 提供 HTTP 处理器：公开站点、动态 SEO 端点与后台管理。
package web

import (
	"bytes"
	"context"
	"encoding/xml"
	"hash/fnv"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cms.ccvar.com/internal/i18n"
	"cms.ccvar.com/internal/seo"
	"cms.ccvar.com/internal/store"
)

type Server struct {
	store      *store.Store
	rnd        *Renderer
	baseURL    string
	uploadDir  string
	sess       *sessions
	login      *loginLimiter
	apiLimiter *apiRateLimiter
	i18n       *i18n.Manager
	mux        *http.ServeMux
	assetVer   string // 静态资源内容指纹，用作 ?v= 破缓存（资源变更即失效旧缓存）
	imageSizes map[string]ImageSize
	cacheMu    sync.RWMutex
	content    map[string]contentCacheEntry
	endpoints  map[string]endpointCacheEntry
}

type contentCacheEntry struct {
	updatedAt time.Time
	html      template.HTML
	toc       []Heading
}

type endpointCacheEntry struct {
	body        []byte
	contentType string
	expires     time.Time
}

const (
	generatedEndpointCacheControl = "public, max-age=1800"
	generatedEndpointCacheTTL     = 30 * time.Minute
)

// assetVersion 取关键静态资源内容的短指纹：内容变了指纹就变，配合长缓存做缓存破坏。
func assetVersion(fsys fs.FS) string {
	h := fnv.New64a()
	for _, p := range []string{"assets/css/style.css", "assets/css/public.css", "assets/css/admin.css", "assets/js/admin.js", "assets/js/site.js", "assets/js/toc.js"} {
		if b, err := fs.ReadFile(fsys, p); err == nil {
			_, _ = h.Write(b)
		}
	}
	return strconv.FormatUint(h.Sum64(), 36)
}

// ThemeOption 用于后台设置页的主题下拉。
type ThemeOption struct{ ID, Name, Desc string }

// Themes 是可选的前台主题（布局风格各不相同）。
var Themes = []ThemeOption{
	{"editorial", "编辑 · Editorial", "暖色衬线、单列大字号列表（默认）"},
	{"magazine", "杂志 · Magazine", "无衬线、居中刊头、三列卡片网格"},
	{"terminal", "极客 · Terminal", "等宽字体、深色、终端清单式布局"},
	{"brutalist", "粗野 · Brutalist", "黑白高对比、粗黑边框、硬阴影、无圆角"},
	{"notebook", "手账 · Notebook", "米黄纸张、衬线、横格背景、暖橙强调"},
	{"swiss", "瑞士 · Swiss", "国际主义网格、红黑配色、巨号编号"},
	{"pastel", "柔彩 · Pastel", "浅彩渐变、大圆角卡片、友好柔和"},
	{"newspaper", "报纸 · Newspaper", "多栏排版、衬线、首字下沉、黑白"},
	{"darkpro", "暗夜 · Dark Pro", "现代暗色、靛蓝霓虹、卡片网格"},
	{"landing", "官网 · Landing", "产品/项目官网风：大 hero + CTA 按钮 + 特性卡片"},
	{"product", "产品 · Product", "开源项目/互联网产品官网、文档入口、更新日志"},
	{"prism", "光谱 · Prism", "深色海报感、多色信号线、错层内容卡"},
	{"exchange", "交易所 · Exchange", "Web3 增长页、行情仪表盘、强转化按钮"},
	{"academy", "智课 · Academy", "AI 教材科普、课程目录、阅读友好卡片"},
	{"garment", "制衣 · Garment", "外贸服装工厂、样衣目录、B2B 展示感"},
	{"institution", "机构 · Institution", "专业服务/咨询/律所/协会官网、可信背书"},
	{"studio", "作品 · Studio", "设计/摄影/建筑/品牌工作室、图像主导作品集"},
	{"lifestyle", "生活 · Lifestyle", "咖啡/民宿/餐厅/买手店、小品牌温度感官网"},
}

func validTheme(id string) bool {
	for _, t := range Themes {
		if t.ID == id {
			return true
		}
	}
	return false
}

// 各主题的默认主色 / 圆角（用于微调控件的初值与未自定义时的展示）。
var themeAccentDefault = map[string]string{
	"editorial": "#9a3b2f", "magazine": "#1f5fff", "terminal": "#3fb950",
	"brutalist": "#1f23ff", "notebook": "#c2691f", "swiss": "#e30613",
	"pastel": "#8b5cf6", "newspaper": "#8b0000", "darkpro": "#7c7cf8", "landing": "#4f46e5", "product": "#0f7cff", "prism": "#d7ff4a",
	"exchange": "#00f5a0", "academy": "#2563eb", "garment": "#0f766e",
	"institution": "#8a1f2d", "studio": "#ff4f5e", "lifestyle": "#2f7d57",
}
var themeRadiusDefault = map[string]string{
	"editorial": "10", "magazine": "12", "terminal": "6", "brutalist": "0",
	"notebook": "8", "swiss": "0", "pastel": "18", "newspaper": "0", "darkpro": "14", "landing": "14", "product": "14", "prism": "18",
	"exchange": "16", "academy": "16", "garment": "12",
	"institution": "8", "studio": "4", "lifestyle": "18",
}

const (
	homeLinksLimitKey       = "home.links_limit"
	homePostsPerPageKey     = "home.posts_per_page"
	layoutWidthKey          = "layout.width"
	postDefaultAuthorKey    = "content.post_author"
	linkDefaultAuthorKey    = "content.link_author"
	defaultHomeLinksLimit   = 8
	defaultHomePostsPerPage = 6
	minHomeLinksLimit       = 0
	maxHomeLinksLimit       = 24
	minHomePostsPerPage     = 1
	maxHomePostsPerPage     = 50
)

func normalizeLayoutWidth(v string) string {
	switch strings.TrimSpace(v) {
	case "1080", "1200", "1240", "1360", "1440":
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

// ThemeCard 是设置页每个主题卡片的状态（含该主题自己的微调值）。
type ThemeCard struct {
	ID, Name, Desc string
	Accent, Radius string
	Custom         bool
}

// themeTweak 读取某主题的微调（按主题独立存储），未设置时回落到该主题默认。
func (s *Server) themeTweak(id string) (custom bool, accent, radius string) {
	if v, _ := s.store.GetSetting("theme." + id + ".custom"); v == "1" {
		custom = true
	}
	accent, _ = s.store.GetSetting("theme." + id + ".accent")
	if !hexColor(accent) {
		if d := themeAccentDefault[id]; d != "" {
			accent = d
		} else {
			accent = "#9a3b2f"
		}
	}
	radius, _ = s.store.GetSetting("theme." + id + ".radius")
	if radius == "" {
		if d := themeRadiusDefault[id]; d != "" {
			radius = d
		} else {
			radius = "10"
		}
	}
	return
}

// LangLink 是页眉语言切换器里的一项。
type LangLink struct {
	Code, Name, URL string
	Active          bool
}

// ArchiveConfig 描述文章分类/链接列表的「全部」入口配置。
// 它不是 categories 表里的真实分类，只用于列表页标题、筛选按钮、SEO 和默认导航。
type ArchiveConfig struct {
	Title       string
	Label       string
	Description string
	Slug        string
	Path        string
}

// View 是传给模板的统一数据载体。
type View struct {
	Site       seo.Site
	SEO        seo.Meta
	Nav        string
	Year       int
	Theme      string
	ThemeStyle template.CSS
	AssetVer   string

	// 多语种（前台）
	Tr          *i18n.Tr
	Lang        string
	Langs       []LangLink
	Admin       *i18n.AdminTr
	AdminLang   string
	AdminLangs  []i18n.Locale
	AdminReturn string

	Posts        []*store.Post
	Featured     *store.Post
	FeaturedMore []*store.Post
	FeatLinks    []*store.Post
	Post         *store.Post
	Page         *store.Post
	Categories   []*store.Category
	Category     *store.Category
	CategoryAll  ArchiveConfig
	LinksAll     ArchiveConfig
	Prev         *store.Post
	Next         *store.Post
	Related      []*store.Post
	Giscus       *GiscusView

	ContentHTML template.HTML
	TOC         []Heading

	PageNum    int
	TotalPages int
	HasPrev    bool
	HasNext    bool
	PrevPage   int
	NextPage   int
	BasePath   string

	Query   string
	Results int

	// 后台
	AllPosts         []*store.Post
	ListTotal        int
	StatusFilter     string
	AdminListPath    string
	DefaultAuthor    string
	Edit             *store.Post
	IsPage           bool
	IsLink           bool
	EditBase         string // 编辑表单的后台路径基：posts | pages | links
	EditListURL      string // 返回列表的后台 URL
	EditTypeLabel    string // 文章 | 页面 | 链接
	Authed           bool
	ShowPwWarn       bool // 仍为默认密码且本会话未关闭提示
	CSRF             string
	Flash            string
	FormErr          string
	Settings         *SettingsForm
	Themes           []ThemeOption
	Cards            []ThemeCard
	Section          string
	CatKind          string // 分类管理当前类型：post | link
	EditCat          *store.Category
	FormVals         map[string]string // 表单回填（分类新增/编辑出错时）
	Update           *UpdateInfo       // 系统更新检查
	Upgrade          *UpgradeStatus    // 系统升级任务状态
	AutomationKeys   []*store.AutomationKey
	AutomationLogs   []*store.AutomationLog
	NewAPISecret     string
	NewAPIName       string
	NewAPIScopes     string
	NewAIBrief       string
	NewAPIKeyID      int64
	APIBaseURL       string
	OpenAPIURL       string
	APIDocsURL       string
	SkillPackageURL  string
	EditLang         string             // 后台当前操作的内容语种
	Locales          []i18n.Locale      // 已启用语种
	AllLocales       []i18n.Locale      // 全部可选语种（内置 + 自定义，语言设置勾选）
	CustomLocales    []i18n.Locale      // 自定义预设（可删除）
	AdminI18NJSON    string             // 当前后台语种的用户覆盖翻译 JSON
	Trans            []*store.Post      // 当前编辑文章的互译版本
	Social           []SocialLink       // 页脚社交链接（前台渲染 / 后台回填）
	Menu             []MenuItem         // 前台页眉导航（按当前语种解析）
	MenuEdit         []MenuRow          // 后台导航菜单编辑（URL + 各语种标签）
	MenuTargets      []MenuTargetOption // 后台导航菜单可选入口
	VisualEdit       bool               // 前台 iframe 可视化编辑模式
	VisualPreviewURL string             // 后台可视化编辑 iframe 地址
	VisualFields     []VisualField      // 可视化编辑侧栏字段
	VisualGroups     []VisualGroup      // 可视化编辑侧栏字段分组
	VisualHistory    []VisualLog        // 可视化编辑最近修改
	LayoutWidth      string             // 前台内容最大宽度预设（空=跟随主题）
	OverviewStats    []OverviewStat     // 后台概览：内容状态
	OverviewTasks    []OverviewTask     // 后台概览：待处理事项
	OverviewRecent   []*store.Post      // 后台概览：最近更新
	OverviewStatus   []OverviewStatus   // 后台概览：系统状态
}

type OverviewStat struct {
	Label     string
	Href      string
	Icon      string
	Total     int
	Published int
	Draft     int
	Scheduled int
}

type OverviewTask struct {
	Label string
	Hint  string
	Href  string
	Icon  string
	Count int
	Tone  string
}

type OverviewStatus struct {
	Label string
	Value string
	Hint  string
	Href  string
	Icon  string
	Tone  string
}

type VisualField struct {
	Key        string
	Label      string
	Value      string
	Meta       string // 侧栏卡片的辅助展示值，例如导航 URL
	Group      string
	Kind       string // text | image
	Hint       string
	Multiline  bool
	Draggable  bool // 是否允许在可视化编辑侧栏拖动排序
	Contextual bool // 是否只在当前预览页出现对应元素时显示
	Localized  bool // 是否按语种保存
	Inherited  bool // 当前语种是否沿用默认语种
}

type VisualGroup struct {
	ID     string
	Title  string
	Fields []VisualField
}

type VisualLog struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Label string `json:"label"`
	Lang  string `json:"lang"`
	Kind  string `json:"kind"`
	Old   string `json:"old"`
	New   string `json:"new"`
	At    string `json:"at"`
}

// GiscusView 是前台文章页渲染 giscus 所需的受控配置。
type GiscusView struct {
	Repo          string
	RepoID        string
	Category      string
	CategoryID    string
	Mapping       string
	Strict        string
	Reactions     string
	InputPosition string
	Theme         string
	Lang          string
}

// SettingsForm 承载后台设置页的可编辑字段。
type SettingsForm struct {
	Name           string
	NameDef        string
	Tagline        string
	TaglineDef     string
	Description    string
	DescriptionDef string
	PostAuthor     string
	PostAuthorDef  string
	LinkAuthor     string
	LinkAuthorDef  string
	Favicon        string
	Logo           string
	ShareImage     string
	Brand          string
	Theme          string
	Custom         bool   // 是否启用主题微调
	Accent         string // 自定义主色 #rrggbb
	Radius         string // 自定义圆角 px
	HeroEyebrow    string
	HeroTitle      string
	HeroVisual     string // ""(默认动画) | image | svg
	HeroImage      string
	HeroImageDef   string
	HeroImageMode  string
	HeroSVG        string
	FooterNote     string
	// 首页栏目标题（可自定义，空则前台回落语种默认）
	HomeFeatured string
	HomeLinks    string
	HomeLatest   string
	// 首页显示数量（站点信息）
	HomeLinksLimit   string
	HomePostsPerPage string
	// 各栏目标题的语种默认值（作为输入框 placeholder 提示）
	HomeFeaturedDef string
	HomeLinksDef    string
	HomeLatestDef   string
	// 代码注入（统计/广告等；头部进 <head> 末尾，尾部进 </body> 前）
	InjectHead string
	InjectBody string
	// 第三方评论（giscus）
	CommentsProvider    string
	GiscusRepo          string
	GiscusRepoID        string
	GiscusCategory      string
	GiscusCategoryID    string
	GiscusMapping       string
	GiscusStrict        bool
	GiscusReactions     bool
	GiscusInputPosition string
	GiscusTheme         string
	// 分类/链接列表的「全部」入口（设置 - 分类）。
	AllTitle       string
	AllLabel       string
	AllSlug        string
	AllPath        string
	AllDescription string
}

const (
	defaultFaviconPath   = "/assets/favicon.svg"
	defaultLogoPath      = "/assets/logo.svg"
	defaultLogoENPath    = "/assets/logo-en.svg"
	defaultShareImageURL = "/assets/og-cover.webp"
)

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func defaultAuthorFallback(kind, lang string) string {
	if lang == "en" {
		if kind == "link" {
			return "GCMS Picks"
		}
		return "GCMS Team"
	}
	if kind == "link" {
		return "gcms 推荐"
	}
	return "gcms 团队"
}

func defaultAuthorKey(kind string) string {
	if kind == "link" {
		return linkDefaultAuthorKey
	}
	return postDefaultAuthorKey
}

func (s *Server) defaultContentAuthor(kind, lang string) string {
	if lang == "" || !s.langEnabled(lang) {
		lang = s.defaultLang()
	}
	if v := strings.TrimSpace(s.store.Setting(s.copyKey(defaultAuthorKey(kind), lang))); v != "" {
		return v
	}
	if lang != "en" && lang != s.defaultLang() {
		if v := strings.TrimSpace(s.store.Setting(defaultAuthorKey(kind))); v != "" {
			return v
		}
	}
	return defaultAuthorFallback(kind, lang)
}

func (s *Server) fillDefaultAuthor(p *store.Post) {
	if p == nil || strings.TrimSpace(p.Author) != "" {
		return
	}
	switch p.Type {
	case "post", "link":
		p.Author = s.defaultContentAuthor(p.Type, p.Lang)
	}
}

func New(st *store.Store, baseURL, uploadDir string, tplFS, assetsFS fs.FS) (*Server, error) {
	imageSizes := scanAssetImageSizes(assetsFS)
	rnd, err := NewRenderer(tplFS, imageSizes)
	if err != nil {
		return nil, err
	}
	if uploadDir != "" {
		_ = os.MkdirAll(uploadDir, 0o755)
	}
	s := &Server{
		store: st, rnd: rnd, baseURL: baseURL, uploadDir: uploadDir,
		sess: newSessions(st), login: newLoginLimiter(), apiLimiter: newAPIRateLimiter(), i18n: i18n.New(), assetVer: assetVersion(assetsFS), imageSizes: imageSizes,
		content: map[string]contentCacheEntry{}, endpoints: map[string]endpointCacheEntry{},
	}
	s.i18n.LoadCustom(st.Setting("custom_locales")) // 合并后台新增的自定义语种预设
	s.routes(assetsFS)
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.securityHeaders(s.withLocale(s.mux)) }

func (s *Server) serveUpload(w http.ResponseWriter, r *http.Request) {
	name, ok := uploadNameFromPath(r.URL.EscapedPath())
	if !ok {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(s.uploadDir, name)
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, full)
}

func uploadNameFromPath(escapedPath string) (string, bool) {
	raw := strings.TrimPrefix(escapedPath, "/uploads/")
	if raw == "" {
		return "", false
	}
	name, err := url.PathUnescape(raw)
	if err != nil || !validUploadFilename(name) {
		return "", false
	}
	return name, true
}

func validUploadFilename(name string) bool {
	if name == "" || name == "." || strings.HasPrefix(name, ".") || strings.Contains(name, "..") || strings.ContainsAny(name, `/\`) {
		return false
	}
	if !allowedUploadExt[strings.ToLower(filepath.Ext(name))] {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}

func (s *Server) assetCacheControl(r *http.Request) string {
	if s.assetVer != "" && r.URL.Query().Get("v") == s.assetVer {
		return "public, max-age=31536000, immutable"
	}
	return "public, max-age=86400"
}

// securityHeaders 给所有响应加上基础安全头；并为静态资源/上传文件加缓存，
// 特别地对 /uploads/（尤其用户上传的 SVG）施加 CSP，杜绝直链访问触发脚本执行（XSS）。
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		p := r.URL.Path
		// 后台禁止被任意站点内嵌（防点击劫持）；公开页不限制，便于被正常嵌入/预览
		if strings.HasPrefix(p, "/admin") {
			h.Set("X-Frame-Options", "SAMEORIGIN")
		}
		switch {
		case strings.HasPrefix(p, "/uploads/"):
			// 用户上传内容：禁脚本与插件，并禁止被嵌入为顶层文档执行（SVG XSS 防护）
			h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; script-src 'none'; object-src 'none'; base-uri 'none'; sandbox")
			h.Set("Cache-Control", "public, max-age=2592000")
		case strings.HasPrefix(p, "/assets/"):
			h.Set("Cache-Control", s.assetCacheControl(r))
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- 多语种基础设施 ----------

type ctxKey int

const langKey ctxKey = 0

func withLang(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, langKey, lang)
}

func langFrom(r *http.Request) string {
	if v, ok := r.Context().Value(langKey).(string); ok && v != "" {
		return v
	}
	return "zh"
}

// locales 返回已启用语种（首个为默认）。
func (s *Server) locales() []i18n.Locale { return s.i18n.Active(s.store.Setting("locales")) }

func (s *Server) defaultLang() string { return s.locales()[0].Code }

func (s *Server) langEnabled(code string) bool {
	for _, l := range s.locales() {
		if l.Code == code {
			return true
		}
	}
	return false
}

type langPreference struct {
	value string
	q     float64
	order int
}

func normalizeLangToken(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.ReplaceAll(v, "_", "-")
	return v
}

func parseAcceptLanguage(header string) []langPreference {
	if strings.TrimSpace(header) == "" {
		return nil
	}
	parts := strings.Split(header, ",")
	prefs := make([]langPreference, 0, len(parts))
	for i, raw := range parts {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		segments := strings.Split(raw, ";")
		value := normalizeLangToken(segments[0])
		if value == "" {
			continue
		}
		q := 1.0
		for _, seg := range segments[1:] {
			seg = strings.TrimSpace(seg)
			if strings.HasPrefix(seg, "q=") {
				v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(seg, "q=")), 64)
				if err != nil {
					q = 0
					break
				}
				q = v
			}
		}
		if q <= 0 {
			continue
		}
		if q > 1 {
			q = 1
		}
		prefs = append(prefs, langPreference{value: value, q: q, order: i})
	}
	sort.SliceStable(prefs, func(i, j int) bool {
		if prefs[i].q == prefs[j].q {
			return prefs[i].order < prefs[j].order
		}
		return prefs[i].q > prefs[j].q
	})
	return prefs
}

func negotiateAcceptLanguage(header string, locales []i18n.Locale, fallback string) string {
	if len(locales) == 0 {
		return fallback
	}
	code := map[string]string{}
	tag := map[string]string{}
	for _, l := range locales {
		code[normalizeLangToken(l.Code)] = l.Code
		tag[normalizeLangToken(l.Tag)] = l.Code
	}
	if fallback == "" {
		fallback = locales[0].Code
	}
	for _, pref := range parseAcceptLanguage(header) {
		if pref.value == "*" {
			return fallback
		}
		if v, ok := code[pref.value]; ok {
			return v
		}
		if v, ok := tag[pref.value]; ok {
			return v
		}
		primary := pref.value
		if i := strings.IndexByte(primary, '-'); i >= 0 {
			primary = primary[:i]
		}
		if v, ok := code[primary]; ok {
			return v
		}
		if v, ok := tag[primary]; ok {
			return v
		}
		for _, l := range locales {
			c := normalizeLangToken(l.Code)
			t := normalizeLangToken(l.Tag)
			if strings.HasPrefix(c, primary+"-") || strings.HasPrefix(t, primary+"-") {
				return l.Code
			}
		}
	}
	return fallback
}

func (s *Server) preferredLang(r *http.Request, fallback string) string {
	return negotiateAcceptLanguage(r.Header.Get("Accept-Language"), s.locales(), fallback)
}

func (s *Server) abs(path string) string { return absWithBase(s.baseURL, path) }

func absWithBase(baseURL, path string) string { return strings.TrimRight(baseURL, "/") + path }

func (s *Server) absForRequest(r *http.Request, path string) string {
	return absWithBase(s.publicBaseURL(r), path)
}

func (s *Server) publicBaseURL(r *http.Request) string {
	configured := strings.TrimRight(strings.TrimSpace(s.baseURL), "/")
	if configured != "" && !isLocalBaseURL(configured) {
		return configured
	}
	if host := requestHost(r); host != "" {
		return requestScheme(r) + "://" + host
	}
	if configured != "" {
		return configured
	}
	return "http://localhost:8080"
}

func requestScheme(r *http.Request) string {
	if r == nil {
		return "http"
	}
	if proto := firstHeaderValue(r.Header.Get("X-Forwarded-Proto")); proto == "http" || proto == "https" {
		return proto
	}
	if strings.EqualFold(r.Header.Get("X-Forwarded-Ssl"), "on") || r.TLS != nil {
		return "https"
	}
	return "http"
}

func requestHost(r *http.Request) string {
	if r == nil {
		return ""
	}
	for _, raw := range []string{r.Header.Get("X-Forwarded-Host"), r.Header.Get("X-Original-Host"), r.Host} {
		host := firstHeaderValue(raw)
		if host != "" && !strings.ContainsAny(host, " \t\r\n") {
			return host
		}
	}
	return ""
}

func firstHeaderValue(raw string) string {
	if raw == "" {
		return ""
	}
	if i := strings.IndexByte(raw, ','); i >= 0 {
		raw = raw[:i]
	}
	return strings.ToLower(strings.TrimSpace(raw))
}

func isLocalBaseURL(raw string) bool {
	host := ""
	if u, err := url.Parse(raw); err == nil {
		host = u.Hostname()
	}
	if host == "" {
		host = strings.TrimSpace(raw)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
	}
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	default:
		return false
	}
}

// 这些路径不参与语种前缀：后台、静态资源、上传、全局 SEO 端点。
func isReservedPath(p string) bool {
	switch {
	case strings.HasPrefix(p, "/admin"), strings.HasPrefix(p, "/api/"), strings.HasPrefix(p, "/assets/"), strings.HasPrefix(p, "/uploads/"):
		return true
	case p == "/robots.txt", p == "/sitemap.xml", p == "/favicon.ico":
		return true
	}
	return false
}

func shiftPath(p string) (head, tail string) {
	p = strings.TrimPrefix(p, "/")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i], p[i:]
	}
	return p, "/"
}

// withLocale 是放在 mux 前的中间件：识别并剥掉路径里的语种前缀写入 context；
// 无前缀的公开路径 302 跳到默认语种；保留路径原样透传。
func (s *Server) withLocale(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		def := s.defaultLang()
		if isReservedPath(r.URL.Path) {
			next.ServeHTTP(w, r.WithContext(withLang(r.Context(), def)))
			return
		}
		head, tail := shiftPath(r.URL.Path)
		if s.langEnabled(head) {
			r.URL.Path = tail
			next.ServeHTTP(w, r.WithContext(withLang(r.Context(), head)))
			return
		}
		// 无语种前缀 → 根路径按 Accept-Language 协商，其它路径仍跳兜底语种，避免旧链接被跳到不存在的译文。
		targetLang := def
		if r.URL.Path == "/" {
			targetLang = s.preferredLang(r, def)
			w.Header().Add("Vary", "Accept-Language")
			w.Header().Set("Cache-Control", "private, no-store")
		}
		target := "/" + targetLang
		if r.URL.Path == "/" {
			target += "/"
		} else {
			target += r.URL.Path
		}
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusFound)
	})
}

// langSwitch 构建「仅切换器」语言链接（不输出 hreflang）：每个语种走 fallback 路径。
func (s *Server) langSwitch(cur string, pathByLang map[string]string, fallback string) []LangLink {
	var out []LangLink
	for _, l := range s.locales() {
		p := fallback
		if pathByLang != nil {
			if v, ok := pathByLang[l.Code]; ok {
				p = v
			}
		}
		out = append(out, LangLink{Code: l.Code, Name: l.Name, URL: "/" + l.Code + p, Active: l.Code == cur})
	}
	return out
}

// i18nLinks 给定「该页在各语种的相对路径」，同时构建语言切换器与 hreflang 备份链接。
// pathByLang 仅包含真实存在译文的语种；缺失语种的切换器回退到该语种首页，且不输出其 hreflang。
func (s *Server) i18nLinks(baseURL, cur string, pathByLang map[string]string) (langs []LangLink, alts []seo.Alternate) {
	def := s.defaultLang()
	for _, l := range s.locales() {
		if p, ok := pathByLang[l.Code]; ok {
			url := "/" + l.Code + p
			langs = append(langs, LangLink{Code: l.Code, Name: l.Name, URL: url, Active: l.Code == cur})
			alts = append(alts, seo.Alternate{Hreflang: l.Tag, Href: absWithBase(baseURL, url)})
		} else {
			langs = append(langs, LangLink{Code: l.Code, Name: l.Name, URL: "/" + l.Code + "/", Active: l.Code == cur})
		}
	}
	if p, ok := pathByLang[def]; ok {
		alts = append(alts, seo.Alternate{Hreflang: "x-default", Href: absWithBase(baseURL, "/"+def+p)})
	} else {
		alts = append(alts, seo.Alternate{Hreflang: "x-default", Href: absWithBase(baseURL, "/"+def+"/")})
	}
	return
}

func (s *Server) localizedSetting(base, lang, dflt string) string {
	def := s.defaultLang()
	if lang != def {
		if v := strings.TrimSpace(s.store.Setting(base + "::" + lang)); v != "" {
			return v
		}
	}
	if v := strings.TrimSpace(s.store.Setting(base)); v != "" {
		return v
	}
	return dflt
}

func archivePrefix(kind string) string {
	if kind == "link" {
		return "links.all."
	}
	return "category.all."
}

func (s *Server) archiveStoreKey(kind, field, lang string) string {
	return s.copyKey(archivePrefix(kind)+field, lang)
}

func normalizeArchiveSlug(slug, fallback string) string {
	slug = slugify(slug)
	if slug == "" {
		return fallback
	}
	return slug
}

func archivePath(kind, slug string) string {
	switch kind {
	case "link":
		if slug == "" || slug == "links" {
			return "/links"
		}
		return "/links/" + slug
	default:
		if slug == "" || slug == "category" {
			return "/category"
		}
		return "/category/" + slug
	}
}

func (s *Server) archiveConfig(lang, kind string) ArchiveConfig {
	def := s.defaultLang()
	tr := s.i18n.Tr(lang, def)
	prefix := archivePrefix(kind)
	siteDesc := s.localizedSetting("site.description", lang, "用 Go 与 SQLite 构建的轻量内容站，关注后端工程、极简设计与搜索引擎优化。")
	titleDef, labelDef, slugDef := tr.T("nav.category"), tr.T("links.all"), "category"
	if kind == "link" {
		titleDef = tr.T("nav.links")
		slugDef = "links"
	}
	title := s.localizedSetting(prefix+"title", lang, titleDef)
	label := s.localizedSetting(prefix+"label", lang, labelDef)
	desc := s.localizedSetting(prefix+"description", lang, siteDesc)
	slug := normalizeArchiveSlug(s.localizedSetting(prefix+"slug", lang, slugDef), slugDef)
	return ArchiveConfig{Title: title, Label: label, Description: desc, Slug: slug, Path: archivePath(kind, slug)}
}

// site 每请求构建站点配置（含当前语种的文案、前缀、OG/hreflang 元信息）。
func (s *Server) site(lang string) seo.Site {
	loc := s.i18n.Locale(lang)
	def := s.defaultLang()
	tr := s.i18n.Tr(lang, def)
	// 文案：优先 key::lang，回落默认语种 bare key，再回落硬编码默认。
	get := func(base, dflt string) string {
		if lang != def {
			if v := s.store.Setting(base + "::" + lang); v != "" {
				return v
			}
		}
		if v := s.store.Setting(base); v != "" {
			return v
		}
		return dflt
	}
	getAsset := func(base string) string {
		if lang != def {
			if v := s.store.Setting(base + "::" + lang); v != "" {
				return v
			}
		}
		return s.store.Setting(base)
	}
	theme := s.store.Setting("theme")
	if !validTheme(theme) {
		theme = "editorial"
	}
	var ogAlt []string
	for _, l := range s.locales() {
		if l.Code != lang {
			ogAlt = append(ogAlt, l.OG)
		}
	}
	brand := s.store.Setting("site.brand")
	if brand == "" {
		brand = "logo"
	}
	logo := getAsset("site.logo")
	if logo == "" {
		logo = defaultLogoPath
	}
	if lang == "en" && logo == defaultLogoPath {
		logo = defaultLogoENPath
	}
	linkAll := s.archiveConfig(lang, "link")
	return seo.Site{
		Name:             get("site.name", "CCVAR 简记"),
		Tagline:          get("site.tagline", "记录技术、工具与思考"),
		Description:      get("site.description", "用 Go 与 SQLite 构建的轻量内容站，关注后端工程、极简设计与搜索引擎优化。"),
		BaseURL:          s.baseURL,
		Locale:           loc.OG,
		LangTag:          loc.Tag,
		Prefix:           "/" + loc.Code,
		Author:           s.defaultContentAuthor("post", lang),
		Theme:            theme,
		Favicon:          s.store.Setting("site.favicon"),
		Logo:             logo,
		ShareImage:       getAsset("site.share_image"),
		Brand:            brand,
		HeroEyebrow:      get("site.hero_eyebrow", "Go · SQLite · SEO"),
		HeroTitle:        get("site.hero_title", "把复杂留给后端，\n把简单留给读者。"),
		HeroVisual:       getAsset("hero.visual"),
		HeroImage:        getAsset("hero.image"),
		HeroSVG:          s.store.Setting("hero.svg"),
		FooterNote:       get("site.footer_note", "用 Go 与 SQLite 构建。"),
		HomeFeatured:     get("home.featured_title", tr.T("home.featured")),
		HomeLinks:        get("home.links_title", tr.T("home.links")),
		HomeLatest:       get("home.latest_title", tr.T("home.latest")),
		HomeLabel:        tr.T("nav.home"),
		LinksLabel:       linkAll.Title,
		LinksDescription: linkAll.Description,
		InjectHead:       s.store.Setting("inject.head"),
		InjectBody:       s.store.Setting("inject.body"),
		OGAltLocale:      ogAlt,
	}
}

// themeOverride 取「当前主题」的微调，生成注入 <html> 的内联 CSS 变量。
func (s *Server) themeOverride() template.CSS {
	return s.themeOverrideFor(s.store.Setting("theme"))
}

func (s *Server) themeOverrideFor(theme string) template.CSS {
	if !validTheme(theme) {
		theme = "editorial"
	}
	custom, accent, radius := s.themeTweak(theme)
	var b strings.Builder
	if width := normalizeLayoutWidth(s.store.Setting(layoutWidthKey)); width != "" {
		b.WriteString("--w-wide:" + width + "px;")
	}
	if !custom {
		return template.CSS(b.String())
	}
	if hexColor(accent) {
		b.WriteString("--accent:" + accent + ";")
		b.WriteString("--accent-soft:color-mix(in srgb," + accent + " 80%,#fff);")
		b.WriteString("--accent-wash:color-mix(in srgb," + accent + " 14%,transparent);")
	}
	if n, err := strconv.Atoi(radius); err == nil && n >= 0 && n <= 40 {
		b.WriteString("--radius:" + strconv.Itoa(n) + "px;")
	}
	return template.CSS(b.String())
}

func (s *Server) renderedContent(p *store.Post) (template.HTML, []Heading) {
	if p == nil {
		return "", nil
	}
	key := p.Type + ":" + strconv.FormatInt(p.ID, 10)
	s.cacheMu.RLock()
	if e, ok := s.content[key]; ok && e.updatedAt.Equal(p.UpdatedAt) {
		toc := append([]Heading(nil), e.toc...)
		s.cacheMu.RUnlock()
		return e.html, toc
	}
	s.cacheMu.RUnlock()

	html, toc := RenderContentWithImages(p.Content, s.imageSizes)
	s.cacheMu.Lock()
	if len(s.content) > 512 {
		s.content = map[string]contentCacheEntry{}
	}
	s.content[key] = contentCacheEntry{updatedAt: p.UpdatedAt, html: html, toc: append([]Heading(nil), toc...)}
	s.cacheMu.Unlock()
	return html, toc
}

func (s *Server) cachedEndpoint(key string) ([]byte, string, bool) {
	now := time.Now()
	s.cacheMu.RLock()
	e, ok := s.endpoints[key]
	if ok && now.Before(e.expires) {
		body := append([]byte(nil), e.body...)
		s.cacheMu.RUnlock()
		return body, e.contentType, true
	}
	s.cacheMu.RUnlock()
	if ok {
		s.cacheMu.Lock()
		if cur, still := s.endpoints[key]; still && now.After(cur.expires) {
			delete(s.endpoints, key)
		}
		s.cacheMu.Unlock()
	}
	return nil, "", false
}

func (s *Server) setCachedEndpoint(key, contentType string, body []byte, ttl time.Duration) {
	s.cacheMu.Lock()
	s.endpoints[key] = endpointCacheEntry{body: append([]byte(nil), body...), contentType: contentType, expires: time.Now().Add(ttl)}
	s.cacheMu.Unlock()
}

func (s *Server) clearGeneratedCaches() {
	s.cacheMu.Lock()
	s.content = map[string]contentCacheEntry{}
	s.endpoints = map[string]endpointCacheEntry{}
	s.cacheMu.Unlock()
}

func hexColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func (s *Server) routes(assetsFS fs.FS) {
	mux := http.NewServeMux()

	// 公开站点（语种前缀由 withLocale 中间件剥离后命中这些原始路由）
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /posts/{slug}", s.article)
	mux.HandleFunc("GET /category", s.categoryRoot)
	mux.HandleFunc("GET /category/{slug}", s.category)
	mux.HandleFunc("GET /links", s.links)
	mux.HandleFunc("GET /links/{slug}", s.link)
	mux.HandleFunc("GET /api-docs", s.apiDocs)
	mux.HandleFunc("GET /about", s.about)
	mux.HandleFunc("GET /search", s.search)
	mux.HandleFunc("GET /{slug}", s.page)

	// SEO 端点（动态生成）
	mux.HandleFunc("GET /sitemap.xml", s.sitemap)
	mux.HandleFunc("GET /rss.xml", s.rss)
	mux.HandleFunc("GET /robots.txt", s.robots)

	// 静态资源（embed）
	mux.Handle("GET /assets/", http.FileServer(http.FS(assetsFS)))
	// 浏览器会自动请求站点根的 /favicon.ico
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, assetsFS, "assets/favicon.ico")
	})

	// 用户上传（运行时文件，存于磁盘）
	if s.uploadDir != "" {
		mux.HandleFunc("GET /uploads/", s.serveUpload)
	}
	mux.HandleFunc("POST /admin/upload", s.requireAuth(s.adminUpload))
	mux.HandleFunc("POST /admin/render", s.requireAuth(s.adminRender))

	// 自动化 API（开放语种、分类读取、媒体上传，以及文章 / 页面 / 链接内容操作）。
	mux.HandleFunc("GET /api/admin/v1/openapi.json", s.apiOpenAPI)
	mux.HandleFunc("GET /api/admin/v1/languages", s.apiLanguages)
	mux.HandleFunc("POST /api/admin/v1/media", s.apiUploadMedia)
	mux.HandleFunc("GET /api/admin/v1/{collection}/categories", s.apiListCategories)
	mux.HandleFunc("GET /api/admin/v1/{collection}", s.apiListContent)
	mux.HandleFunc("POST /api/admin/v1/{collection}", s.apiCreateContent)
	mux.HandleFunc("GET /api/admin/v1/{collection}/{id}/preview", s.apiPreviewContent)
	mux.HandleFunc("GET /api/admin/v1/{collection}/{id}", s.apiGetContent)
	mux.HandleFunc("PATCH /api/admin/v1/{collection}/{id}", s.apiUpdateContent)

	// 后台
	mux.HandleFunc("GET /admin/login", s.adminLoginForm)
	mux.HandleFunc("POST /admin/login", s.adminLoginPost)
	mux.HandleFunc("GET /admin/language", s.adminLanguage)
	mux.HandleFunc("POST /admin/logout", s.adminLogout)
	mux.HandleFunc("POST /admin/dismiss-pw", s.requireAuth(s.adminDismissPw))
	mux.HandleFunc("GET /admin", s.requireAuth(s.adminDashboard))
	mux.HandleFunc("GET /admin/posts", s.requireAuth(s.adminPosts))
	mux.HandleFunc("GET /admin/visual", s.requireAuth(s.adminVisual))
	mux.HandleFunc("POST /admin/visual/save", s.requireAuth(s.adminVisualSave))
	mux.HandleFunc("POST /admin/visual/undo", s.requireAuth(s.adminVisualUndo))
	mux.HandleFunc("POST /admin/visual/nav/reorder", s.requireAuth(s.adminVisualNavReorder))
	mux.HandleFunc("POST /admin/visual/categories/reorder", s.requireAuth(s.adminVisualCategoryReorder))
	mux.HandleFunc("GET /admin/settings", s.requireAuth(s.adminSettings))
	mux.HandleFunc("GET /admin/settings/{section}", s.requireAuth(s.adminSettingsSection))
	mux.HandleFunc("GET /admin/theme-preview/{theme}", s.requireAuth(s.adminThemePreview))
	mux.HandleFunc("GET /admin/settings/updates/status", s.requireAuth(s.adminUpgradeStatus))
	mux.HandleFunc("GET /admin/settings/updates/check", s.requireAuth(s.adminUpdateCheck))
	mux.HandleFunc("POST /admin/settings/site", s.requireAuth(s.adminSaveSite))
	mux.HandleFunc("POST /admin/settings/appearance", s.requireAuth(s.adminSaveAppearance))
	mux.HandleFunc("POST /admin/settings/comments", s.requireAuth(s.adminSaveComments))
	mux.HandleFunc("POST /admin/settings/updates/upgrade", s.requireAuth(s.adminStartUpgrade))
	mux.HandleFunc("POST /admin/settings/security", s.requireAuth(s.adminSavePassword))
	mux.HandleFunc("POST /admin/settings/admin-i18n", s.requireAuth(s.adminSaveAdminI18N))
	mux.HandleFunc("POST /admin/settings/demo/reload", s.requireAuth(s.adminReloadProductDemo))
	mux.HandleFunc("POST /admin/settings/demo/clear", s.requireAuth(s.adminClearDemoContent))
	mux.HandleFunc("POST /admin/settings/copy", s.requireAuth(s.adminSaveCopy))
	mux.HandleFunc("POST /admin/settings/menu", s.requireAuth(s.adminSaveMenu))
	mux.HandleFunc("POST /admin/settings/languages", s.requireAuth(s.adminSaveLanguages))
	mux.HandleFunc("POST /admin/settings/languages/preset", s.requireAuth(s.adminAddLocalePreset))
	mux.HandleFunc("POST /admin/settings/languages/preset/delete", s.requireAuth(s.adminDeleteLocalePreset))
	mux.HandleFunc("POST /admin/settings/categories/all", s.requireAuth(s.adminSaveCategoryAll))
	mux.HandleFunc("POST /admin/settings/categories", s.requireAuth(s.adminSaveCategory))
	mux.HandleFunc("POST /admin/settings/categories/delete", s.requireAuth(s.adminDeleteCategory))
	mux.HandleFunc("POST /admin/settings/categories/reorder", s.requireAuth(s.adminReorderCategories))
	mux.HandleFunc("POST /admin/settings/automation/keys", s.requireAuth(s.adminCreateAutomationKey))
	mux.HandleFunc("POST /admin/settings/automation/keys/update", s.requireAuth(s.adminUpdateAutomationKey))
	mux.HandleFunc("POST /admin/settings/automation/keys/regenerate", s.requireAuth(s.adminRegenerateAutomationKey))
	mux.HandleFunc("POST /admin/settings/automation/keys/revoke", s.requireAuth(s.adminRevokeAutomationKey))
	mux.HandleFunc("POST /admin/settings/automation/keys/delete", s.requireAuth(s.adminDeleteAutomationKey))
	mux.HandleFunc("GET /admin/settings/automation/skill.zip", s.requireAuth(s.adminDownloadAutomationSkill))
	mux.HandleFunc("POST /admin/settings/automation/skill.zip", s.requireAuth(s.adminDownloadAutomationSkill))

	// 页面（如关于）
	mux.HandleFunc("GET /admin/pages", s.requireAuth(s.adminPages))
	mux.HandleFunc("GET /admin/pages/{id}/edit", s.requireAuth(s.adminPageEdit))
	mux.HandleFunc("POST /admin/pages/{id}", s.requireAuth(s.adminPageSave))
	mux.HandleFunc("GET /admin/posts/new", s.requireAuth(s.adminNew))
	mux.HandleFunc("GET /admin/posts/{id}/preview", s.requireAuth(s.adminPostPreview))
	mux.HandleFunc("GET /admin/posts/{id}/edit", s.requireAuth(s.adminEdit))
	mux.HandleFunc("POST /admin/posts", s.requireAuth(s.adminCreate))
	mux.HandleFunc("POST /admin/posts/{id}", s.requireAuth(s.adminUpdate))
	mux.HandleFunc("POST /admin/posts/{id}/delete", s.requireAuth(s.adminDelete))
	mux.HandleFunc("POST /admin/posts/{id}/pin", s.requireAuth(s.adminPin))
	mux.HandleFunc("POST /admin/posts/{id}/translate", s.requireAuth(s.adminTranslate))

	// 链接（type=link）
	mux.HandleFunc("GET /admin/links", s.requireAuth(s.adminLinks))
	mux.HandleFunc("GET /admin/links/new", s.requireAuth(s.adminLinkNew))
	mux.HandleFunc("GET /admin/links/{id}/preview", s.requireAuth(s.adminLinkPreview))
	mux.HandleFunc("GET /admin/links/{id}/edit", s.requireAuth(s.adminLinkEdit))
	mux.HandleFunc("POST /admin/links", s.requireAuth(s.adminLinkCreate))
	mux.HandleFunc("POST /admin/links/{id}", s.requireAuth(s.adminLinkUpdate))
	mux.HandleFunc("POST /admin/links/{id}/delete", s.requireAuth(s.adminLinkDelete))
	mux.HandleFunc("POST /admin/links/{id}/pin", s.requireAuth(s.adminLinkPin))

	// 兜底 404
	mux.HandleFunc("GET /", s.notFound)

	s.mux = mux
}

// ---------- 公开处理器 ----------

func (s *Server) view(r *http.Request, nav string) *View {
	return s.viewForLang(r, langFrom(r), nav)
}

func (s *Server) viewForLang(r *http.Request, lang, nav string) *View {
	if !s.langEnabled(lang) {
		lang = s.defaultLang()
	}
	st := s.site(lang)
	st.BaseURL = s.publicBaseURL(r)
	tr := s.i18n.Tr(lang, s.defaultLang())
	v := &View{
		Site: st, Nav: nav, Year: time.Now().Year(), Theme: st.Theme, ThemeStyle: s.themeOverride(),
		Tr: tr, Lang: lang, AssetVer: s.assetVer,
		CategoryAll: s.archiveConfig(lang, "post"),
		LinksAll:    s.archiveConfig(lang, "link"),
	}
	if r.URL.Query().Get("visual_edit") == "1" {
		if _, ok := s.currentSession(r); ok {
			v.VisualEdit = true
		}
	}
	v.Langs = s.langSwitch(lang, nil, "/")
	v.Social = parseSocialLinks(s.store.Setting("social_links"))
	v.Menu = s.menuItems(r, lang, tr, nav)
	return v
}

func (s *Server) giscusForPost(lang string, p *store.Post) *GiscusView {
	if p == nil || p.Type != "post" || !p.CommentsEnabled || s.store.Setting("comments.provider") != "giscus" {
		return nil
	}
	g := &GiscusView{
		Repo:          strings.TrimSpace(s.store.Setting("comments.giscus.repo")),
		RepoID:        strings.TrimSpace(s.store.Setting("comments.giscus.repo_id")),
		Category:      strings.TrimSpace(s.store.Setting("comments.giscus.category")),
		CategoryID:    strings.TrimSpace(s.store.Setting("comments.giscus.category_id")),
		Mapping:       commentMapping(s.store.Setting("comments.giscus.mapping")),
		Strict:        boolAttr(s.store.Setting("comments.giscus.strict") != "0"),
		Reactions:     boolAttr(s.store.Setting("comments.giscus.reactions") != "0"),
		InputPosition: commentInputPosition(s.store.Setting("comments.giscus.input_position")),
		Theme:         commentTheme(s.store.Setting("comments.giscus.theme")),
		Lang:          giscusLang(s.i18n.Locale(lang)),
	}
	if g.Repo == "" || g.RepoID == "" || g.Category == "" || g.CategoryID == "" {
		return nil
	}
	return g
}

func boolAttr(on bool) string {
	if on {
		return "1"
	}
	return "0"
}

func commentMapping(v string) string {
	switch strings.TrimSpace(v) {
	case "url", "title", "og:title":
		return strings.TrimSpace(v)
	default:
		return "pathname"
	}
}

func commentInputPosition(v string) string {
	if strings.TrimSpace(v) == "top" {
		return "top"
	}
	return "bottom"
}

func commentTheme(v string) string {
	switch strings.TrimSpace(v) {
	case "light", "dark", "dark_high_contrast", "dark_dimmed", "transparent_dark", "noborder_light", "noborder_dark", "cobalt", "purple_dark":
		return strings.TrimSpace(v)
	default:
		return "preferred_color_scheme"
	}
}

func giscusLang(loc i18n.Locale) string {
	supported := map[string]string{
		"en": "en", "zh-cn": "zh-CN", "zh-tw": "zh-TW", "ja": "ja", "ko": "ko",
		"fr": "fr", "de": "de", "es": "es", "it": "it", "pt": "pt", "ru": "ru",
	}
	code := normalizeLangToken(loc.Code)
	if v, ok := supported[code]; ok {
		return v
	}
	tag := normalizeLangToken(loc.Tag)
	if v, ok := supported[tag]; ok {
		return v
	}
	if strings.HasPrefix(tag, "zh-hant") {
		return "zh-TW"
	}
	if strings.HasPrefix(tag, "zh") {
		return "zh-CN"
	}
	if i := strings.IndexByte(tag, '-'); i > 0 {
		if v, ok := supported[tag[:i]]; ok {
			return v
		}
	}
	return "en"
}

func (s *Server) applyTheme(v *View, theme string) {
	if !validTheme(theme) {
		return
	}
	v.Theme = theme
	v.Site.Theme = theme
	v.ThemeStyle = s.themeOverrideFor(theme)
}

// menuItems 构建前台页眉导航：未配置时回落默认菜单（首页/分类/关于，用 i18n 文案）。
func (s *Server) menuItems(r *http.Request, lang string, tr *i18n.Tr, nav string) []MenuItem {
	rows := parseMenuRows(s.store.Setting("nav_menu"))
	if len(rows) == 0 {
		categoryPath := s.archiveConfig(lang, "post").Path
		linksPath := s.archiveConfig(lang, "link").Path
		return []MenuItem{
			{Href: tr.U("/"), Label: tr.T("nav.home"), Active: nav == "home", Index: 0},
			{Href: tr.U(categoryPath), Label: tr.T("nav.category"), Active: nav == "category", Index: 1},
			{Href: tr.U(linksPath), Label: tr.T("nav.links"), Active: nav == "links", Index: 2},
			{Href: tr.U("/about"), Label: tr.T("nav.about"), Active: nav == "about", Index: 3},
		}
	}
	def := s.defaultLang()
	out := make([]MenuItem, 0, len(rows))
	currentPath := r.URL.Path
	if currentPath == "" {
		currentPath = "/"
	}
	currentFull := currentPath
	if r.URL.RawQuery != "" {
		currentFull += "?" + r.URL.RawQuery
	}
	exactAny := false
	groupCount := 0
	for _, m := range rows {
		if menuURLMatchesCurrent(m.URL, currentPath, currentFull) {
			exactAny = true
		}
		if k := navKeyOf(m.URL); k != "" && k == nav {
			groupCount++
		}
	}
	for i, m := range rows {
		label := strings.TrimSpace(m.Labels[lang])
		if label == "" {
			label = strings.TrimSpace(m.Labels[def])
		}
		if label == "" {
			label = m.URL
		}
		ext := isExternalURL(m.URL)
		href := m.URL
		if !ext {
			href = tr.U(m.URL)
		}
		k := navKeyOf(m.URL)
		active := false
		if exactAny {
			active = menuURLMatchesCurrent(m.URL, currentPath, currentFull)
		} else if groupCount == 1 {
			active = k != "" && k == nav
		}
		out = append(out, MenuItem{Href: href, Label: label, Active: active, External: ext, Index: i})
	}
	return out
}

func (s *Server) menuTargetOptions(admins ...*i18n.AdminTr) []MenuTargetOption {
	def := s.defaultLang()
	locales := s.locales()
	admin := firstAdmin(admins)
	t := func(key, fallback string) string { return adminUI(admin, key, fallback) }
	labelsFromKey := func(key string) map[string]string {
		labels := map[string]string{}
		for _, l := range locales {
			labels[l.Code] = s.i18n.Tr(l.Code, def).T(key)
		}
		return labels
	}
	archiveLabels := func(kind, fallback string) map[string]string {
		labels := map[string]string{}
		for _, l := range locales {
			if title := strings.TrimSpace(s.archiveConfig(l.Code, kind).Title); title != "" {
				labels[l.Code] = title
			} else {
				labels[l.Code] = s.i18n.Tr(l.Code, def).T(fallback)
			}
		}
		return labels
	}
	staticLabels := func(zh, en string) map[string]string {
		labels := map[string]string{}
		for _, l := range locales {
			if l.Code == "en" {
				labels[l.Code] = en
			} else {
				labels[l.Code] = zh
			}
		}
		return labels
	}
	categoryPath := s.archiveConfig(def, "post").Path
	linksPath := s.archiveConfig(def, "link").Path
	return []MenuTargetOption{
		{Value: "home", Label: t("admin.settings.menu.target.home", "首页"), URL: "/", Kind: "preset", Labels: labelsFromKey("nav.home")},
		{Value: "category", Label: t("admin.settings.menu.target.category", "文章分类页"), URL: categoryPath, Kind: "preset", Labels: archiveLabels("post", "nav.category")},
		{Value: "links", Label: t("admin.settings.menu.target.links", "链接页"), URL: linksPath, Kind: "preset", Labels: archiveLabels("link", "nav.links")},
		{Value: "about", Label: t("admin.settings.menu.target.about", "关于页"), URL: "/about", Kind: "preset", Labels: labelsFromKey("nav.about")},
		{Value: "start", Label: t("admin.settings.menu.target.start", "开始使用页"), URL: "/start", Kind: "preset", Labels: staticLabels("开始使用", "Get Started")},
		{Value: "search", Label: t("admin.settings.menu.target.search", "搜索页"), URL: "/search", Kind: "preset", Labels: labelsFromKey("nav.search")},
		{Value: "__custom__", Label: t("admin.settings.menu.target.custom", "自定义站内路径"), Kind: "custom", Labels: map[string]string{}},
		{Value: "__external__", Label: t("admin.settings.menu.target.external", "外部链接"), Kind: "external", Labels: map[string]string{}},
	}
}

// menuEditRows 为后台导航编辑提供回填行：未配置时给出默认菜单可编辑副本（各语种填 i18n 文案）。
func (s *Server) menuEditRows(admins ...*i18n.AdminTr) []MenuRow {
	targets := s.menuTargetOptions(admins...)
	if rows := parseMenuRows(s.store.Setting("nav_menu")); len(rows) > 0 {
		return decorateMenuRows(rows, targets)
	}
	byValue := map[string]MenuTargetOption{}
	for _, opt := range targets {
		byValue[opt.Value] = opt
	}
	mk := func(value string) MenuRow {
		opt := byValue[value]
		return MenuRow{URL: opt.URL, Labels: opt.Labels}
	}
	return decorateMenuRows([]MenuRow{mk("home"), mk("category"), mk("links"), mk("about")}, targets)
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	s.renderHome(w, r)
}

func (s *Server) adminThemePreview(w http.ResponseWriter, r *http.Request) {
	theme := r.PathValue("theme")
	if !validTheme(theme) {
		s.notFound(w, r)
		return
	}
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Cache-Control", "private, max-age=60")
	lang := langFrom(r)
	v := s.view(r, "home")
	s.applyTheme(v, theme)
	v.SEO = v.Site.Home()
	v.Site.HeroVisual = ""
	v.Site.HeroImage = ""
	v.Site.HeroSVG = ""
	v.Site.InjectHead = ""
	v.Site.InjectBody = ""

	posts, _ := s.store.ListPublished(lang, 0, 4)
	if len(posts) == 0 {
		posts = []*store.Post{
			{Title: v.Site.Name + " 更新日志", Excerpt: v.Site.Description, PublishedAt: time.Now()},
			{Title: "快速开始", Excerpt: "安装、配置与内容发布流程。", PublishedAt: time.Now()},
			{Title: "设计与主题", Excerpt: "为不同类型的网站选择合适的前台结构。", PublishedAt: time.Now()},
		}
	}
	v.Featured = posts[0]
	if len(posts) > 1 {
		v.Posts = posts[1:]
	}
	v.FeatLinks, _ = s.store.FeaturedLinks(lang, 3)
	if len(v.FeatLinks) == 0 {
		v.FeatLinks, _ = s.store.ListLinks(lang, 0, 0, 3)
	}
	if len(v.FeatLinks) == 0 {
		v.FeatLinks = []*store.Post{
			{Title: "文档", Excerpt: "查看部署、配置与 API 用法。"},
			{Title: "发布", Excerpt: "版本更新与一键升级流程。"},
			{Title: "生态", Excerpt: "自动化接口与内容助手接入。"},
		}
	}
	s.rnd.ThemePreview(w, http.StatusOK, v)
}

func (s *Server) renderHome(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	page := pageParam(r)
	postsPerPage := s.intSetting(homePostsPerPageKey, defaultHomePostsPerPage, minHomePostsPerPage, maxHomePostsPerPage)
	linksLimit := s.intSetting(homeLinksLimitKey, defaultHomeLinksLimit, minHomeLinksLimit, maxHomeLinksLimit)
	total, _ := s.store.CountPublished(lang)
	totalPages := ceilDiv(total, postsPerPage)
	posts, err := s.store.ListPublished(lang, (page-1)*postsPerPage, postsPerPage)
	if err != nil {
		s.serverError(w, err)
		return
	}
	v := s.view(r, "home")
	v.SEO = v.Site.Home()
	if page == 1 {
		// 精选优先取置顶文章（可多篇），否则取最新一篇
		if fps, _ := s.store.FeaturedPosts(lang, postsPerPage); len(fps) > 0 {
			v.Featured = fps[0]
			v.FeaturedMore = fps[1:]
			fset := map[int64]bool{}
			for _, f := range fps {
				fset[f.ID] = true
			}
			for _, p := range posts {
				if !fset[p.ID] {
					v.Posts = append(v.Posts, p)
				}
			}
		} else if len(posts) > 0 {
			v.Featured = posts[0]
			v.Posts = posts[1:]
		}
		// 链接模块：仅当存在「置顶」链接时才在首页展示
		if linksLimit > 0 {
			v.FeatLinks, _ = s.store.FeaturedLinks(lang, linksLimit)
		}
	} else {
		v.Posts = posts
	}
	// 首页在每个语种都存在 → 全语种 hreflang
	ph := map[string]string{}
	for _, l := range s.locales() {
		ph[l.Code] = "/"
	}
	v.Langs, v.SEO.Alternates = s.i18nLinks(v.Site.BaseURL, lang, ph)
	setPagination(v, page, totalPages, "/")
	s.rnd.Public(w, "home", http.StatusOK, v)
}

func (s *Server) article(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	p, err := s.store.GetPostBySlug(lang, r.PathValue("slug"), false)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if p == nil {
		s.notFound(w, r)
		return
	}
	s.fillDefaultAuthor(p)
	v := s.view(r, "")
	v.SEO = v.Site.Article(p)
	v.Post = p
	v.ContentHTML, v.TOC = s.renderedContent(p)
	v.Prev, _ = s.store.PrevPost(p)
	v.Next, _ = s.store.NextPost(p)
	v.Related, _ = s.store.Related(p, 3)
	v.Giscus = s.giscusForPost(lang, p)
	ph := map[string]string{p.Lang: "/posts/" + p.Slug}
	if trs, _ := s.store.TranslationsPublished(p.TransGroup); trs != nil {
		for _, t := range trs {
			if t.Type == "post" {
				ph[t.Lang] = "/posts/" + t.Slug
			}
		}
	}
	v.Langs, v.SEO.Alternates = s.i18nLinks(v.Site.BaseURL, lang, ph)
	s.rnd.Public(w, "article", http.StatusOK, v)
}

func (s *Server) category(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	all := s.archiveConfig(lang, "post")
	if all.Path != "/category" && r.PathValue("slug") == all.Slug {
		s.categoryAll(w, r, all)
		return
	}
	c, err := s.store.GetCategoryBySlug(lang, r.PathValue("slug"))
	if err != nil {
		s.serverError(w, err)
		return
	}
	if c == nil {
		s.notFound(w, r)
		return
	}
	const size = 8
	page := pageParam(r)
	total, _ := s.store.CountByCategory(c.ID)
	posts, _ := s.store.ListByCategory(c.ID, (page-1)*size, size)
	cats, _ := s.store.ListCategories(lang, "post")
	v := s.view(r, "category")
	v.SEO = v.Site.Category(c)
	v.Category = c
	v.Categories = cats
	v.Posts = posts
	ph := map[string]string{c.Lang: "/category/" + c.Slug}
	if trs, _ := s.store.CategoryTranslations(c.TransGroup); trs != nil {
		for _, t := range trs {
			ph[t.Lang] = "/category/" + t.Slug
		}
	}
	v.Langs, v.SEO.Alternates = s.i18nLinks(v.Site.BaseURL, lang, ph)
	setPagination(v, page, ceilDiv(total, size), "/category/"+c.Slug)
	s.rnd.Public(w, "category", http.StatusOK, v)
}

func (s *Server) categoryRoot(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	s.categoryAll(w, r, s.archiveConfig(lang, "post"))
}

func (s *Server) categoryAll(w http.ResponseWriter, r *http.Request, all ArchiveConfig) {
	const size = 8
	lang := langFrom(r)
	page := pageParam(r)
	total, _ := s.store.CountPublished(lang)
	posts, err := s.store.ListPublished(lang, (page-1)*size, size)
	if err != nil {
		s.serverError(w, err)
		return
	}
	cats, _ := s.store.ListCategories(lang, "post")
	c := &store.Category{Slug: all.Slug, Name: all.Title, Description: all.Description, Lang: lang, Kind: "post"}
	v := s.view(r, "category")
	v.SEO = v.Site.CategoryArchive(c, all.Path)
	v.Category = c
	v.Categories = cats
	v.Posts = posts
	ph := map[string]string{}
	for _, l := range s.locales() {
		ph[l.Code] = s.archiveConfig(l.Code, "post").Path
	}
	v.Langs, v.SEO.Alternates = s.i18nLinks(v.Site.BaseURL, lang, ph)
	setPagination(v, page, ceilDiv(total, size), all.Path)
	s.rnd.Public(w, "category", http.StatusOK, v)
}

func (s *Server) links(w http.ResponseWriter, r *http.Request) {
	const size = 12
	lang := langFrom(r)
	page := pageParam(r)
	// 分类筛选 ?cat=slug（仅链接分类）
	var cat *store.Category
	var catID int64
	if cs := trim(r.URL.Query().Get("cat")); cs != "" {
		if c, _ := s.store.GetCategoryBySlug(lang, cs); c != nil && c.Kind == "link" {
			cat, catID = c, c.ID
		}
	}
	total, _ := s.store.CountLinks(lang, catID)
	items, err := s.store.ListLinks(lang, catID, (page-1)*size, size)
	if err != nil {
		s.serverError(w, err)
		return
	}
	cats, _ := s.store.ListCategories(lang, "link")
	v := s.view(r, "links")
	v.SEO = v.Site.Links(cat)
	v.Posts = items
	v.Categories = cats
	v.Category = cat
	basePath := v.LinksAll.Path
	if cat != nil {
		basePath = "/links"
	}
	ph := map[string]string{}
	for _, l := range s.locales() {
		ph[l.Code] = "/links"
	}
	v.Langs, v.SEO.Alternates = s.i18nLinks(v.Site.BaseURL, lang, ph)
	setPagination(v, page, ceilDiv(total, size), basePath)
	s.rnd.Public(w, "links", http.StatusOK, v)
}

func (s *Server) link(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	all := s.archiveConfig(lang, "link")
	if all.Path != "/links" && r.PathValue("slug") == all.Slug {
		s.links(w, r)
		return
	}
	p, err := s.store.GetLinkBySlug(lang, r.PathValue("slug"), false)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if p == nil {
		s.notFound(w, r)
		return
	}
	s.fillDefaultAuthor(p)
	v := s.view(r, "links")
	v.SEO = v.Site.Link(p)
	v.Post = p
	v.ContentHTML, v.TOC = s.renderedContent(p)
	v.Related, _ = s.store.RelatedLinks(p, 6)
	ph := map[string]string{p.Lang: "/links/" + p.Slug}
	if trs, _ := s.store.TranslationsPublished(p.TransGroup); trs != nil {
		for _, t := range trs {
			if t.Type == "link" {
				ph[t.Lang] = "/links/" + t.Slug
			}
		}
	}
	v.Langs, v.SEO.Alternates = s.i18nLinks(v.Site.BaseURL, lang, ph)
	s.rnd.Public(w, "link", http.StatusOK, v)
}

func (s *Server) apiDocs(w http.ResponseWriter, r *http.Request) {
	v := s.view(r, "api_docs")
	v.SEO = seo.Meta{
		Title:       "自动化接口开放文档 — " + v.Site.Name,
		Description: "GCMS 自动化接口的开放文档，包含语种、分类、媒体上传、文章、链接、页面的接口地址、权限说明、参数说明与请求示例。",
		Canonical:   v.Site.Abs("/api-docs"),
		Robots:      "noindex, follow",
		OGType:      "article",
		Author:      v.Site.Author,
	}
	s.rnd.Public(w, "api_docs", http.StatusOK, v)
}

func (s *Server) about(w http.ResponseWriter, r *http.Request) {
	s.renderPageBySlug(w, r, "about")
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	slug := trim(r.PathValue("slug"))
	if slug == "" {
		s.notFound(w, r)
		return
	}
	s.renderPageBySlug(w, r, slug)
}

func (s *Server) renderPageBySlug(w http.ResponseWriter, r *http.Request, slug string) {
	lang := langFrom(r)
	p, err := s.store.GetPage(lang, slug)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if p == nil {
		s.notFound(w, r)
		return
	}
	v := s.view(r, slug)
	v.SEO = v.Site.Page(p)
	v.Page = p
	v.ContentHTML, _ = s.renderedContent(p)
	ph := map[string]string{p.Lang: "/" + p.Slug}
	if trs, _ := s.store.TranslationsPublished(p.TransGroup); trs != nil {
		for _, t := range trs {
			if t.Type == "page" {
				ph[t.Lang] = "/" + t.Slug
			}
		}
	}
	v.Langs, v.SEO.Alternates = s.i18nLinks(v.Site.BaseURL, lang, ph)
	s.rnd.Public(w, "page", http.StatusOK, v)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	q := trim(r.URL.Query().Get("q"))
	v := s.view(r, "search")
	v.SEO = v.Site.Search(q)
	v.SEO.Title = v.Tr.T("search.title") + " — " + v.Site.Name
	v.Query = q
	if q != "" {
		posts, _ := s.store.Search(lang, q, 50)
		v.Posts = posts
		v.Results = len(posts)
	}
	// 切换器保留查询词
	sp := "/search"
	if q != "" {
		sp += "?q=" + url.QueryEscape(q)
	}
	v.Langs = s.langSwitch(lang, nil, sp)
	s.rnd.Public(w, "search", http.StatusOK, v)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	v := s.view(r, "")
	v.SEO = v.Site.NotFound()
	v.SEO.Title = v.Tr.T("nf.title") + " — " + v.Site.Name
	s.rnd.Public(w, "404", http.StatusNotFound, v)
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	http.Error(w, "内部错误", http.StatusInternalServerError)
}

// ---------- 动态 SEO 端点 ----------

func xmlEsc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// sitemap 生成多语种站点地图：同一逻辑页面的各语种 URL 互相用 xhtml:link 标注 hreflang。
func (s *Server) sitemap(w http.ResponseWriter, r *http.Request) {
	const contentType = "application/xml; charset=utf-8"
	baseURL := s.publicBaseURL(r)
	cacheKey := "sitemap:" + baseURL
	if body, ct, ok := s.cachedEndpoint(cacheKey); ok {
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", generatedEndpointCacheControl)
		_, _ = w.Write(body)
		return
	}
	abs := func(path string) string { return absWithBase(baseURL, path) }

	locales := s.locales()
	def := s.defaultLang()
	var b strings.Builder
	b.WriteString(xml.Header)
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">` + "\n")

	writeGroup := func(byLang map[string]string, freq, prio string) {
		for _, l := range locales {
			p, ok := byLang[l.Code]
			if !ok {
				continue
			}
			b.WriteString("  <url>\n")
			b.WriteString("    <loc>" + xmlEsc(abs("/"+l.Code+p)) + "</loc>\n")
			if freq != "" {
				b.WriteString("    <changefreq>" + freq + "</changefreq>\n")
			}
			if prio != "" {
				b.WriteString("    <priority>" + prio + "</priority>\n")
			}
			for _, a := range locales {
				if ap, ok := byLang[a.Code]; ok {
					b.WriteString(`    <xhtml:link rel="alternate" hreflang="` + a.Tag + `" href="` + xmlEsc(abs("/"+a.Code+ap)) + `"/>` + "\n")
				}
			}
			if dp, ok := byLang[def]; ok {
				b.WriteString(`    <xhtml:link rel="alternate" hreflang="x-default" href="` + xmlEsc(abs("/"+def+dp)) + `"/>` + "\n")
			}
			b.WriteString("  </url>\n")
		}
	}

	// 首页（全语种）
	home := map[string]string{}
	for _, l := range locales {
		home[l.Code] = "/"
	}
	writeGroup(home, "daily", "1.0")

	// 链接列表页（全语种）
	linksList := map[string]string{}
	for _, l := range locales {
		linksList[l.Code] = "/links"
	}
	writeGroup(linksList, "weekly", "0.6")

	categoryAll := map[string]string{}
	for _, l := range locales {
		categoryAll[l.Code] = s.archiveConfig(l.Code, "post").Path
	}
	writeGroup(categoryAll, "weekly", "0.7")

	groupBy := func(items func(add func(group, lang, path string))) []map[string]string {
		gm := map[string]map[string]string{}
		var order []string
		items(func(group, lang, path string) {
			if gm[group] == nil {
				gm[group] = map[string]string{}
				order = append(order, group)
			}
			gm[group][lang] = path
		})
		out := make([]map[string]string, 0, len(order))
		for _, g := range order {
			out = append(out, gm[g])
		}
		return out
	}

	if cats, err := s.store.AllCategories("post"); err == nil {
		for _, g := range groupBy(func(add func(group, lang, path string)) {
			for _, c := range cats {
				add(c.TransGroup, c.Lang, "/category/"+c.Slug)
			}
		}) {
			writeGroup(g, "weekly", "0.7")
		}
	}
	if posts, err := s.store.AllPublishedAllLangs(); err == nil {
		for _, g := range groupBy(func(add func(group, lang, path string)) {
			for _, p := range posts {
				add(p.TransGroup, p.Lang, "/posts/"+p.Slug)
			}
		}) {
			writeGroup(g, "monthly", "0.8")
		}
	}
	if pages, err := s.store.AllPagesAllLangs(); err == nil {
		for _, g := range groupBy(func(add func(group, lang, path string)) {
			for _, p := range pages {
				add(p.TransGroup, p.Lang, "/"+p.Slug)
			}
		}) {
			writeGroup(g, "monthly", "0.6")
		}
	}
	if links, err := s.store.AllLinksAllLangs(); err == nil {
		for _, g := range groupBy(func(add func(group, lang, path string)) {
			for _, p := range links {
				add(p.TransGroup, p.Lang, "/links/"+p.Slug)
			}
		}) {
			writeGroup(g, "monthly", "0.7")
		}
	}

	b.WriteString("</urlset>\n")
	body := []byte(b.String())
	s.setCachedEndpoint(cacheKey, contentType, body, generatedEndpointCacheTTL)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", generatedEndpointCacheControl)
	_, _ = w.Write(body)
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	Description string `xml:"description"`
	Category    string `xml:"category,omitempty"`
	PubDate     string `xml:"pubDate"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Language    string    `xml:"language"`
	Items       []rssItem `xml:"item"`
}

type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}

func (s *Server) rss(w http.ResponseWriter, r *http.Request) {
	lang := langFrom(r)
	cacheKey := "rss:" + lang
	const contentType = "application/rss+xml; charset=utf-8"
	if body, ct, ok := s.cachedEndpoint(cacheKey); ok {
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", generatedEndpointCacheControl)
		_, _ = w.Write(body)
		return
	}

	site := s.site(lang)
	feed := rssFeed{Version: "2.0", Channel: rssChannel{
		Title:       site.Name,
		Link:        site.Abs("/"),
		Description: site.Description,
		Language:    site.LangTag,
	}}
	if posts, err := s.store.RecentPublished(lang, 20); err == nil {
		for _, p := range posts {
			cat := ""
			if p.Category != nil {
				cat = p.Category.Name
			}
			feed.Channel.Items = append(feed.Channel.Items, rssItem{
				Title:       p.Title,
				Link:        site.Abs("/posts/" + p.Slug),
				GUID:        site.Abs("/posts/" + p.Slug),
				Description: p.Excerpt,
				Category:    cat,
				PubDate:     p.PublishedAt.Format(time.RFC1123Z),
			})
		}
	}
	var b bytes.Buffer
	_, _ = b.WriteString(xml.Header)
	enc := xml.NewEncoder(&b)
	enc.Indent("", "  ")
	_ = enc.Encode(feed)
	body := b.Bytes()
	s.setCachedEndpoint(cacheKey, contentType, body, generatedEndpointCacheTTL)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", generatedEndpointCacheControl)
	_, _ = w.Write(body)
}

func (s *Server) robots(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("User-agent: *\nAllow: /\nDisallow: /admin/\nDisallow: /admin\n")
	for _, l := range s.locales() {
		b.WriteString("Disallow: /" + l.Code + "/search\n")
	}
	b.WriteString("\nSitemap: " + s.absForRequest(r, "/sitemap.xml") + "\n")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// ---------- 小工具 ----------

func pageParam(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if n < 1 {
		n = 1
	}
	return n
}

func boundedInt(raw string, def, min, max int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		n = def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func (s *Server) intSetting(key string, def, min, max int) int {
	return boundedInt(s.store.Setting(key), def, min, max)
}

func ceilDiv(a, b int) int {
	if b == 0 {
		return 0
	}
	return (a + b - 1) / b
}

func setPagination(v *View, page, totalPages int, base string) {
	v.PageNum = page
	v.TotalPages = totalPages
	v.BasePath = base
	v.HasPrev = page > 1
	v.HasNext = page < totalPages
	v.PrevPage = page - 1
	v.NextPage = page + 1
}

func trim(s string) string { return strings.TrimSpace(s) }
