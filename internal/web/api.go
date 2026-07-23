package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"cms.ccvar.com/internal/i18n"
	"cms.ccvar.com/internal/store"
)

type automationAuth struct {
	key    *store.AutomationKey
	scopes map[string]bool
	// platform 为 true 时，本次请求由一把「平台密钥」（多站）鉴权，
	// key 为 nil，审计写入 platform_automation_logs（见 recordAutomationLog）。
	platform  bool
	platKeyID int64
}

// platformIdentity 由 servePlatformAPI 在平台层完成鉴权/限流/成员校验后注入 context，
// 供 requireAutomationToken 短路复用，避免二次限流与站库 token 查找。
type platformIdentity struct {
	keyID  int64
	scopes map[string]bool
}

type ctxKeyPlatformIdentity struct{}

func withPlatformIdentity(ctx context.Context, id *platformIdentity) context.Context {
	return context.WithValue(ctx, ctxKeyPlatformIdentity{}, id)
}

func platformIdentityFrom(ctx context.Context) (*platformIdentity, bool) {
	v, ok := ctx.Value(ctxKeyPlatformIdentity{}).(*platformIdentity)
	return v, ok && v != nil
}

// recordAutomationLog 把一次自动化动作写入审计：平台密钥 → 平台库，站点密钥 → 站库。
func (s *Server) recordAutomationLog(auth *automationAuth, action, kind string, targetID int64, message string) {
	if auth == nil {
		return
	}
	if auth.platform {
		if s.platform != nil {
			_ = s.platform.CreatePlatformAutomationLog(auth.platKeyID, s.platformSiteID, action, kind, targetID, message)
		}
		return
	}
	if auth.key != nil {
		_ = s.store.CreateAutomationLog(auth.key.ID, action, kind, targetID, message)
	}
}

const (
	apiIPRateLimit    = 240
	apiTokenRateLimit = 120
	apiRateWindow     = time.Minute
)

const (
	apiScopeLanguagesRead       = "languages:read"
	apiScopeLanguagesWrite      = "languages:write"
	apiScopeLanguagesEnable     = "languages:enable"
	apiScopeLanguagesDefault    = "languages:default"
	apiScopeLanguagesCatalog    = "languages:catalog"
	apiScopeMediaWrite          = "media:write"
	apiScopeSiteRead            = "site:read"
	apiScopeSiteWrite           = "site:write"
	apiScopeContentRead         = "content:read"    // 通配：任意集合（含扩展类型）读
	apiScopeContentWrite        = "content:write"   // 通配：任意集合写 + 类型管理
	apiScopeContentPublish      = "content:publish" // 通配：任意集合发布
	apiScopeBrandAssetsWrite    = "brand:assets:write"
	apiScopeNavigationRead      = "navigation:read"
	apiScopeNavigationWrite     = "navigation:write"
	apiScopeStatsRead           = "stats:read" // 读取 Search Console / GA 统计数据
	apiScopePostCategoriesWrite = "posts:categories:write"
	apiScopeLinkCategoriesWrite = "links:categories:write"

	// 平台控制层权限与既有站点内容权限分离。老密钥不会因升级自动获得
	// 建站、域名、安全设置等能力；只有明确签发这些 scope 的平台密钥才可见。
	apiScopeControlRead      = "control:read"
	apiScopeControlUnlock    = "control:unlock"
	apiScopeSitesCreate      = "sites:create"
	apiScopeSitesUpdate      = "sites:update"
	apiScopeSitesDelete      = "sites:delete"
	apiScopeCategoriesDelete = "categories:delete"
	apiScopeNavigationDelete = "navigation:delete"
	apiScopeThemesRead       = "themes:read"
	apiScopeThemesApply      = "themes:apply"
	apiScopeDomainsRead      = "domains:read"
	apiScopeDomainsWrite     = "domains:write"

	// v1.3.40 曾短暂写入过部分平台密钥。它不再对应任何 HTTP 能力，
	// 新签发时拒绝，旧密钥解析时也必须丢弃。
	retiredAPIScopeSecurityWrite = "security:write"
)

type apiRateLimiter struct {
	mu   sync.Mutex
	hits map[string]apiRateEntry
}

type apiRateEntry struct {
	count int
	reset time.Time
}

func newAPIRateLimiter() *apiRateLimiter {
	return &apiRateLimiter{hits: map[string]apiRateEntry{}}
}

func (l *apiRateLimiter) allow(key string, max int, window time.Duration) (time.Duration, bool) {
	if l == nil || key == "" || max <= 0 {
		return 0, true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	e := l.hits[key]
	if e.reset.IsZero() || now.After(e.reset) {
		l.hits[key] = apiRateEntry{count: 1, reset: now.Add(window)}
		if len(l.hits) > 4096 {
			for k, v := range l.hits {
				if now.After(v.reset) {
					delete(l.hits, k)
				}
			}
		}
		return 0, true
	}
	if e.count >= max {
		return time.Until(e.reset), false
	}
	e.count++
	l.hits[key] = e
	return 0, true
}

func (s *Server) checkAPIRateLimit(w http.ResponseWriter, r *http.Request, token string) bool {
	if s.apiLimiter == nil {
		return true
	}
	if retry, ok := s.apiLimiter.allow("ip:"+clientIP(r), apiIPRateLimit, apiRateWindow); !ok {
		apiRateLimitError(w, retry)
		return false
	}
	if token != "" {
		if retry, ok := s.apiLimiter.allow("token:"+apiTokenRateKey(token), apiTokenRateLimit, apiRateWindow); !ok {
			apiRateLimitError(w, retry)
			return false
		}
	}
	return true
}

func apiTokenRateKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum[:8])
}

func apiRateLimitError(w http.ResponseWriter, retry time.Duration) {
	if retry < time.Second {
		retry = time.Second
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
	apiError(w, http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后再试。")
}

type apiContentInput struct {
	ID          *int64  `json:"id,omitempty"`
	Type        *string `json:"type,omitempty"`
	Lang        *string `json:"lang,omitempty"`
	Slug        *string `json:"slug,omitempty"`
	Title       *string `json:"title,omitempty"`
	Excerpt     *string `json:"excerpt,omitempty"`
	Content     *string `json:"content,omitempty"`
	MetaDesc    *string `json:"meta_desc,omitempty"`
	Keywords    *string `json:"keywords,omitempty"`
	CoverImage  *string `json:"cover_image,omitempty"`
	Author      *string `json:"author,omitempty"`
	Status      *string `json:"status,omitempty"`
	EditorMode  *string `json:"editor_mode,omitempty"`
	LinkURL     *string `json:"link_url,omitempty"`
	TransGroup  *string `json:"trans_group,omitempty"`
	CategoryID  *int64  `json:"category_id,omitempty"`
	PublishedAt *string `json:"published_at,omitempty"`
	// 单篇 SEO 覆盖：robots meta / canonical URL（空串 = 清除覆盖，回到默认）。
	RobotsOverride    *string `json:"robots_override,omitempty"`
	CanonicalOverride *string `json:"canonical_override,omitempty"`
	// Fields 是「扩展」内容类型的自定义字段值（按该类型 schema 的键）。
	Fields map[string]any `json:"fields,omitempty"`
}

type apiFeaturedInput struct {
	Featured *bool `json:"featured"`
}

type apiCategory struct {
	ID          int64  `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Lang        string `json:"lang,omitempty"`
	TransGroup  string `json:"trans_group,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Count       int    `json:"count,omitempty"`
}

type apiCategoryInput struct {
	Lang        *string `json:"lang,omitempty"`
	Slug        *string `json:"slug,omitempty"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	TransGroup  *string `json:"trans_group,omitempty"`
}

type apiCategoryAllEntry struct {
	Kind        string `json:"kind"`
	Lang        string `json:"lang"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Label       string `json:"label"`
	Slug        string `json:"slug"`
	Path        string `json:"path"`
	Purpose     string `json:"purpose"`
	Selectable  bool   `json:"selectable"`
}

type apiCategoryAllEntryInput struct {
	Lang        *string `json:"lang,omitempty"`
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	Label       *string `json:"label,omitempty"`
	Slug        *string `json:"slug,omitempty"`
}

type apiCategoryAllEntryPatch struct {
	apiCategoryAllEntryInput
	Items []apiCategoryAllEntryInput `json:"items,omitempty"`
}

type apiLanguageItem struct {
	Code          string            `json:"code"`
	Name          string            `json:"name"`
	Tag           string            `json:"tag"`
	OG            string            `json:"og"`
	Default       bool              `json:"default"`
	Enabled       bool              `json:"enabled"`
	Custom        bool              `json:"custom"`
	CatalogSource string            `json:"catalog_source"`
	CatalogKeys   int               `json:"catalog_keys"`
	Catalog       map[string]string `json:"catalog,omitempty"`
}

type apiLanguageCreateInput struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Tag     string `json:"tag"`
	OG      string `json:"og"`
	Enable  bool   `json:"enable"`
	Default bool   `json:"default"`
}

type apiLanguageUpdateInput struct {
	Enabled *bool `json:"enabled,omitempty"`
	Default *bool `json:"default,omitempty"`
}

type apiLanguageCatalogInput struct {
	Catalog map[string]string `json:"catalog"`
}

type apiSiteProfileItem struct {
	Lang              string `json:"lang"`
	Name              string `json:"name"`
	Tagline           string `json:"tagline"`
	Description       string `json:"description"`
	Keywords          string `json:"keywords"`
	HeroEyebrow       string `json:"hero_eyebrow"`
	HeroTitle         string `json:"hero_title"`
	HeroDescription   string `json:"hero_description"`
	FooterNote        string `json:"footer_note"`
	HomeFeaturedTitle string `json:"home_featured_title"`
	HomeLinksTitle    string `json:"home_links_title"`
	HomeLatestTitle   string `json:"home_latest_title"`
	DefaultPostAuthor string `json:"default_post_author"`
	DefaultLinkAuthor string `json:"default_link_author"`
	Logo              string `json:"logo,omitempty"`
	Favicon           string `json:"favicon,omitempty"`
	ShareImage        string `json:"share_image,omitempty"`
	HeroVisual        string `json:"hero_visual,omitempty"`
	HeroImage         string `json:"hero_image,omitempty"`

	// 主题 options（工厂主题族数据槽）：返回该语种生效的 settings 覆盖值（含 ::lang 回落裸键，
	// 不含 i18n 内置默认）；没有配置时省略（带 enabled 的槽在被显式关闭时也会返回）。
	FactoryStats      []FactoryStat             `json:"factory_stats,omitempty"`
	FactoryProcess    *apiFactoryProcessItem    `json:"factory_process,omitempty"`
	FactoryCTA        *FactoryTextPair          `json:"factory_cta,omitempty"`
	FactoryCategories *apiFactoryToggleInput    `json:"factory_categories,omitempty"`
	FactoryIndustries *apiFactoryIndustriesItem `json:"factory_industries,omitempty"`
	FactoryGallery    []string                  `json:"factory_gallery,omitempty"`
	FactoryFAQ        *apiFactoryFAQItem        `json:"factory_faq,omitempty"`
	DTCTestimonials   []DTCTestimonial          `json:"dtc_testimonials,omitempty"` // 独立站用户评价（按语种；只录真实评价）
}

type apiSiteProfileInput struct {
	Lang              string  `json:"lang,omitempty"`
	Name              *string `json:"name,omitempty"`
	Tagline           *string `json:"tagline,omitempty"`
	Description       *string `json:"description,omitempty"`
	Keywords          *string `json:"keywords,omitempty"`
	HeroEyebrow       *string `json:"hero_eyebrow,omitempty"`
	HeroTitle         *string `json:"hero_title,omitempty"`
	HeroDescription   *string `json:"hero_description,omitempty"`
	FooterNote        *string `json:"footer_note,omitempty"`
	HomeFeaturedTitle *string `json:"home_featured_title,omitempty"`
	HomeLinksTitle    *string `json:"home_links_title,omitempty"`
	HomeLatestTitle   *string `json:"home_latest_title,omitempty"`
	DefaultPostAuthor *string `json:"default_post_author,omitempty"`
	DefaultLinkAuthor *string `json:"default_link_author,omitempty"`
	Logo              *string `json:"logo,omitempty"`
	Favicon           *string `json:"favicon,omitempty"`
	ShareImage        *string `json:"share_image,omitempty"`
	HeroVisual        *string `json:"hero_visual,omitempty"`
	HeroImage         *string `json:"hero_image,omitempty"`

	// 主题 options（工厂主题族数据槽，见 theme_options.go）：写 settings 语义键，
	// 按 lang 落 键/键::lang（各 enabled 开关与 factory_gallery 是全局例外，不分语种）。
	// 传 []/null（或 items/steps:[]、全空 cta）= 清除该语种覆盖、回落默认；字段缺省 = 不动。
	FactoryStats      json.RawMessage         `json:"factory_stats,omitempty"`
	FactoryProcess    *apiFactoryProcessInput `json:"factory_process,omitempty"`
	FactoryCTA        *FactoryTextPair        `json:"factory_cta,omitempty"`
	FactoryCategories *apiFactoryToggleInput  `json:"factory_categories,omitempty"`
	FactoryIndustries *apiFactoryListInput    `json:"factory_industries,omitempty"`
	FactoryGallery    json.RawMessage         `json:"factory_gallery,omitempty"` // ["url",...]（全局）
	FactoryFAQ        *apiFactoryListInput    `json:"factory_faq,omitempty"`
	DTCTestimonials   json.RawMessage         `json:"dtc_testimonials,omitempty"` // [{name,region,quote}]（按语种；[]/null 清除；只录真实评价）
}

// apiFactoryProcessInput 「合作流程」写入：enabled 缺省不动；steps 缺省不动、[]/null 清除。
type apiFactoryProcessInput struct {
	Enabled *bool           `json:"enabled,omitempty"`
	Steps   json.RawMessage `json:"steps,omitempty"`
}

// apiFactoryToggleInput 纯开关区块（分类入口卡区）写入。
type apiFactoryToggleInput struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// apiFactoryListInput 组类槽（FAQ / 应用行业）写入：enabled 缺省不动；items 缺省不动、[]/null 清除。
type apiFactoryListInput struct {
	Enabled *bool           `json:"enabled,omitempty"`
	Items   json.RawMessage `json:"items,omitempty"`
}

// apiFactoryProcessItem GET /site-profile 的「合作流程」读出（enabled 全局 + 该语种覆盖步骤）。
type apiFactoryProcessItem struct {
	Enabled bool          `json:"enabled"`
	Steps   []FactoryStep `json:"steps,omitempty"`
}

// apiFactoryFAQItem / apiFactoryIndustriesItem 组类槽读出（enabled 全局 + 该语种覆盖条目）。
type apiFactoryFAQItem struct {
	Enabled bool        `json:"enabled"`
	Items   []FactoryQA `json:"items,omitempty"`
}

type apiFactoryIndustriesItem struct {
	Enabled bool              `json:"enabled"`
	Items   []FactoryIndustry `json:"items,omitempty"`
}

type apiSiteProfilePatch struct {
	apiSiteProfileInput
	HomeLinksLimit   *int                  `json:"home_links_limit,omitempty"`
	HomePostsPerPage *int                  `json:"home_posts_per_page,omitempty"`
	Items            []apiSiteProfileInput `json:"items,omitempty"`
}

// apiThemeOptionSlot GET /theme-options 的一个数据槽：spec 注册表（键/类型/文案/语种规则）
// + settings 现值（configured/value 按请求语种；带开关的槽附当前开关态）。
// value 只回覆盖值（不含 i18n 内置默认）——configured=false 即「未配置、前台走默认」。
type apiThemeOptionSlot struct {
	Key        string `json:"key"`
	Type       string `json:"type"`
	Label      string `json:"label"`
	Localized  bool   `json:"localized"`
	EnabledKey string `json:"enabled_key,omitempty"`
	Enabled    *bool  `json:"enabled,omitempty"` // 带整条开关的槽（steps/toggle/pairs/qalist）的当前开关态
	Configured bool   `json:"configured"`
	Value      any    `json:"value,omitempty"` // 按语种现值；gallery 为 URL 数组；hero 为 {visual,image}
}

type apiNavigationItem struct {
	URL    string            `json:"url"`
	Labels map[string]string `json:"labels"`
}

type apiNavigationInput struct {
	Items []apiNavigationItem `json:"items"`
}

type apiContentItem struct {
	ID          int64        `json:"id"`
	Type        string       `json:"type"`
	Lang        string       `json:"lang"`
	Slug        string       `json:"slug"`
	Title       string       `json:"title"`
	Excerpt     string       `json:"excerpt"`
	Content     string       `json:"content,omitempty"`
	MetaDesc    string       `json:"meta_desc"`
	Keywords    string       `json:"keywords"`
	CoverImage  string       `json:"cover_image"`
	Author      string       `json:"author"`
	Status      string       `json:"status"`
	Featured    bool         `json:"featured"`
	EditorMode  string       `json:"editor_mode"`
	LinkURL     string       `json:"link_url,omitempty"`
	TransGroup  string       `json:"trans_group"`
	CategoryID  *int64       `json:"category_id"`
	Category    *apiCategory `json:"category,omitempty"`
	URL         string       `json:"url"`
	PublishedAt string       `json:"published_at,omitempty"`
	CreatedAt   string       `json:"created_at,omitempty"`
	UpdatedAt   string       `json:"updated_at,omitempty"`
	// 单篇 SEO 覆盖（空 = 用默认）。
	RobotsOverride    string         `json:"robots_override,omitempty"`
	CanonicalOverride string         `json:"canonical_override,omitempty"`
	Fields            map[string]any `json:"fields,omitempty"`
	// AI 报废申请（冻结契约：Pilot 侧解析这两个字段，恒输出不省略）。
	Discarded     bool   `json:"discarded"`
	DiscardReason string `json:"discard_reason"`
	DiscardedAt   string `json:"discarded_at,omitempty"`
}

type apiContentPreview struct {
	Item                     apiContentItem `json:"item"`
	PreviewURL               string         `json:"preview_url"`
	FrontendPreviewURL       string         `json:"frontend_preview_url,omitempty"`
	FrontendPreviewExpiresAt string         `json:"frontend_preview_expires_at,omitempty"`
	PublicURL                string         `json:"public_url"`
	ContentHTML              string         `json:"content_html"`
	TOC                      []apiHeading   `json:"toc,omitempty"`
	Robots                   string         `json:"robots"`
}

type apiHeading struct {
	Level int    `json:"level"`
	ID    string `json:"id"`
	Text  string `json:"text"`
}

type apiPreviewURLResponse struct {
	PreviewURL string `json:"preview_url"`
	ExpiresAt  string `json:"expires_at"`
	TTLSeconds int64  `json:"ttl_seconds"`
}

type frontendPreviewClaims struct {
	Collection string `json:"collection"`
	ID         int64  `json:"id"`
	Expires    int64  `json:"exp"`
	Updated    int64  `json:"updated"`
	Revision   string `json:"rev,omitempty"`
}

type sitePreviewClaims struct {
	Kind    string `json:"kind"`
	SiteID  int64  `json:"site_id"`
	Expires int64  `json:"exp"`
	Nonce   string `json:"nonce"`
}

const (
	frontendPreviewTTL           = 2 * time.Hour
	sitePreviewTTL               = 15 * time.Minute
	frontendPreviewSecretSetting = "preview.secret"
)

func (s *Server) apiLanguages(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAutomationScope(w, r, apiScopeLanguagesRead); !ok {
		return
	}
	includeDisabled := parseAPIBool(r.URL.Query().Get("include_disabled")) || strings.EqualFold(r.URL.Query().Get("lang"), "all")
	includeCatalog := parseAPIBool(r.URL.Query().Get("include_catalog")) || parseAPIBool(r.URL.Query().Get("catalog"))
	locales := s.locales()
	if includeDisabled {
		locales = s.i18n.All()
	}
	items := make([]apiLanguageItem, 0, len(locales))
	seen := map[string]bool{}
	for _, l := range locales {
		if seen[l.Code] {
			continue
		}
		items = append(items, s.apiLanguageItem(l.Code, includeCatalog))
		seen[l.Code] = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"default": s.defaultLang(), "items": items})
}

func (s *Server) apiCreateLanguage(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requireAutomationScope(w, r, apiScopeLanguagesWrite)
	if !ok {
		return
	}
	var in apiLanguageCreateInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	loc, errCode, errMsg, err := s.addCustomLocale(strings.TrimSpace(in.Code), strings.TrimSpace(in.Name), strings.TrimSpace(in.Tag), strings.TrimSpace(in.OG))
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if errMsg != "" {
		apiError(w, http.StatusBadRequest, errCode, errMsg)
		return
	}
	if in.Enable || in.Default {
		if err := s.enableLocale(loc.Code, in.Default); err != nil {
			apiError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
	}
	def := s.defaultLang()
	s.recordAutomationLog(auth, "create", "language", 0, "新增自定义语种："+loc.Code)
	s.clearGeneratedCaches()
	writeJSON(w, http.StatusCreated, map[string]any{
		"item":    s.apiLanguageItem(loc.Code, false),
		"default": def,
	})
}

func (s *Server) apiUpdateLanguage(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requireAutomationAnyScope(w, r, apiScopeLanguagesEnable, apiScopeLanguagesDefault)
	if !ok {
		return
	}
	code := strings.ToLower(strings.TrimSpace(r.PathValue("code")))
	if code == "" || !s.i18n.Known(code) {
		apiError(w, http.StatusBadRequest, "bad_lang", "语种不存在。")
		return
	}
	var in apiLanguageUpdateInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	if in.Enabled == nil && in.Default == nil {
		apiError(w, http.StatusBadRequest, "empty_patch", "没有收到需要更新的语种设置。")
		return
	}
	if in.Enabled != nil && !automationScopeAllowed(auth.scopes, apiScopeLanguagesEnable) {
		apiError(w, http.StatusForbidden, "missing_scope", "这条访问权限不能启用或禁用语种。")
		return
	}
	if in.Default != nil && !*in.Default {
		apiError(w, http.StatusBadRequest, "bad_request", "设置默认语种时 default 只能传 true。")
		return
	}
	if in.Default != nil && *in.Default && !automationScopeAllowed(auth.scopes, apiScopeLanguagesDefault) {
		apiError(w, http.StatusForbidden, "missing_scope", "这条访问权限不能设置默认语种。")
		return
	}
	if in.Default != nil && *in.Default && in.Enabled != nil && !*in.Enabled {
		apiError(w, http.StatusBadRequest, "bad_request", "不能同时禁用并设为默认语种。")
		return
	}
	if in.Enabled != nil && !*in.Enabled && code == s.defaultLang() {
		apiError(w, http.StatusBadRequest, "bad_request", "默认语种不能禁用，请先设置其它默认语种。")
		return
	}

	action := "update"
	if in.Default != nil && *in.Default {
		if err := s.enableLocale(code, true); err != nil {
			apiError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		action = "set_default"
	} else if in.Enabled != nil {
		if *in.Enabled {
			if err := s.enableLocale(code, false); err != nil {
				apiError(w, http.StatusInternalServerError, "store_error", err.Error())
				return
			}
			action = "enable"
		} else {
			if err := s.disableLocale(code); err != nil {
				apiError(w, http.StatusInternalServerError, "store_error", err.Error())
				return
			}
			action = "disable"
		}
	}
	s.recordAutomationLog(auth, action, "language", 0, "更新语种设置："+code)
	s.clearGeneratedCaches()
	writeJSON(w, http.StatusOK, map[string]any{
		"item":    s.apiLanguageItem(code, false),
		"default": s.defaultLang(),
	})
}

func (s *Server) apiLanguageItem(code string, includeCatalog bool) apiLanguageItem {
	loc := s.i18n.Locale(code)
	def := s.defaultLang()
	item := apiLanguageItem{
		Code:          loc.Code,
		Name:          loc.Name,
		Tag:           loc.Tag,
		OG:            loc.OG,
		Default:       loc.Code == def,
		Enabled:       s.langEnabled(loc.Code),
		Custom:        loc.Custom,
		CatalogSource: s.i18n.CatalogSource(loc.Code),
		CatalogKeys:   s.i18n.CatalogKeyCount(loc.Code, def),
	}
	if includeCatalog {
		item.Catalog = s.i18n.Catalog(loc.Code, def)
	}
	return item
}

func (s *Server) apiGetLanguageCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAutomationScope(w, r, apiScopeLanguagesRead); !ok {
		return
	}
	code := strings.ToLower(strings.TrimSpace(r.PathValue("code")))
	if code == "" || !s.i18n.Known(code) {
		apiError(w, http.StatusBadRequest, "bad_lang", "语种不存在。")
		return
	}
	writeJSON(w, http.StatusOK, s.apiLanguageCatalogResponse(code))
}

func (s *Server) apiUpdateLanguageCatalog(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requireAutomationScope(w, r, apiScopeLanguagesCatalog)
	if !ok {
		return
	}
	code := strings.ToLower(strings.TrimSpace(r.PathValue("code")))
	if code == "" || !s.i18n.Known(code) {
		apiError(w, http.StatusBadRequest, "bad_lang", "语种不存在。")
		return
	}
	var in apiLanguageCatalogInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	if in.Catalog == nil {
		apiError(w, http.StatusBadRequest, "empty_catalog", "请传入 catalog 对象。")
		return
	}
	if err := s.saveLocaleCatalog(code, i18n.SanitizeCatalog(in.Catalog)); err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.recordAutomationLog(auth, "update", "language_catalog", 0, "更新语种字典："+code)
	writeJSON(w, http.StatusOK, s.apiLanguageCatalogResponse(code))
}

func (s *Server) apiLanguageCatalogResponse(code string) map[string]any {
	def := s.defaultLang()
	return map[string]any{
		"code":           code,
		"default":        code == def,
		"catalog_source": s.i18n.CatalogSource(code),
		"catalog_keys":   s.i18n.CatalogKeyCount(code, def),
		"catalog":        s.i18n.Catalog(code, def),
	}
}

func (s *Server) apiGetSiteProfile(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAutomationScope(w, r, apiScopeSiteRead); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.apiSiteProfileResponse())
}

// apiThemeOptions GET /theme-options（admin v1 + 平台 /sites/{id} 镜像；scope 与
// GET /site-profile 的读口径对齐 = site:read）：返回当前主题、骨架、族别与
// 「该主题声明消费的槽子集」+ settings 现值（?lang=xx 按语种，缺省默认语种）。
// 数据全部由 spec 注册表（theme_options.go）+ settings 现值组装——AI 先看这份契约
// 再决定 PATCH /site-profile 写哪些 factory_* 字段；不消费的槽写了也不会在首页渲染。
func (s *Server) apiThemeOptions(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAutomationScope(w, r, apiScopeSiteRead); !ok {
		return
	}
	lang := strings.TrimSpace(r.URL.Query().Get("lang"))
	if lang == "" {
		lang = s.defaultLang()
	}
	if !s.langEnabled(lang) {
		apiError(w, http.StatusBadRequest, "bad_lang", "语种未启用。")
		return
	}
	writeJSON(w, http.StatusOK, s.apiThemeOptionsResponse(lang))
}

// apiThemeOptionsResponse 组装 /theme-options 响应：主题/骨架/族别 + 槽子集与现值。
func (s *Server) apiThemeOptionsResponse(lang string) map[string]any {
	theme := s.store.Setting("theme")
	if !validTheme(theme) {
		theme = "editorial"
	}
	family := ThemeCategoryContent
	for _, t := range Themes {
		if t.ID == theme {
			family = t.Category
			break
		}
	}
	slots := make([]apiThemeOptionSlot, 0, len(factoryThemeOptions)+1)
	for _, spec := range themeOptionSpecs(theme) {
		slot := apiThemeOptionSlot{
			Key:        spec.Key,
			Type:       spec.Type,
			Label:      spec.Label,
			Localized:  spec.Localized,
			EnabledKey: spec.EnabledKey,
		}
		if spec.EnabledKey != "" {
			on := s.factorySectionEnabled(spec.EnabledKey)
			slot.Enabled = &on
		}
		switch spec.Type {
		case themeOptHero:
			// hero 槽（全局）：visual 模式 + 图片 URL；配了任一即视为已配置。
			hv := strings.TrimSpace(s.store.Setting("hero.visual"))
			hi := strings.TrimSpace(s.store.Setting("hero.image"))
			slot.Configured = hv != "" || hi != ""
			slot.Value = map[string]string{"visual": hv, "image": hi}
		case themeOptStats:
			if v := parseFactoryStats(s.localizedSetting(spec.Key, lang, "")); len(v) > 0 {
				slot.Configured, slot.Value = true, v
			}
		case themeOptSteps:
			if v := parseFactorySteps(s.localizedSetting(spec.Key, lang, "")); len(v) > 0 {
				slot.Configured, slot.Value = true, v
			}
		case themeOptTextPair:
			if v := parseFactoryTextPair(s.localizedSetting(spec.Key, lang, "")); v.Title != "" || v.Note != "" {
				slot.Configured, slot.Value = true, v
			}
		case themeOptToggle:
			// 纯开关槽（内容零配置）：状态全在 enabled 上，configured 恒 false。
		case themeOptPairs:
			if v := parseFactoryIndustries(s.localizedSetting(spec.Key, lang, "")); len(v) > 0 {
				slot.Configured, slot.Value = true, v
			}
		case themeOptQAList:
			if v := parseFactoryQAs(s.localizedSetting(spec.Key, lang, "")); len(v) > 0 {
				slot.Configured, slot.Value = true, v
			}
		case themeOptTestimonials:
			if v := parseDTCTestimonials(s.localizedSetting(spec.Key, lang, "")); len(v) > 0 {
				slot.Configured, slot.Value = true, v
			}
		case themeOptGallery:
			// 图集全局（不分语种）：读裸键，value 为 URL 数组。
			if v := parseFactoryGallery(s.store.Setting(spec.Key)); len(v) > 0 {
				slot.Configured, slot.Value = true, v
			}
		}
		slots = append(slots, slot)
	}
	return map[string]any{
		"theme":  theme,
		"layout": layoutForTheme(theme),
		"family": family,
		"lang":   lang,
		"slots":  slots,
	}
}

func (s *Server) apiUpdateSiteProfile(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requireAutomationAnyScope(w, r, apiScopeSiteWrite, apiScopeBrandAssetsWrite)
	if !ok {
		return
	}
	var in apiSiteProfilePatch
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	items := in.Items
	if len(items) == 0 && in.apiSiteProfileInput.hasFields() {
		items = []apiSiteProfileInput{in.apiSiteProfileInput}
	}
	if len(items) == 0 && !in.hasHomeDisplayFields() {
		apiError(w, http.StatusBadRequest, "empty_patch", "没有收到需要更新的站点资料。")
		return
	}
	if in.hasHomeDisplayFields() && !automationScopeAllowed(auth.scopes, apiScopeSiteWrite) {
		apiError(w, http.StatusForbidden, "missing_scope", "这条访问权限不能修改首页显示设置。")
		return
	}
	if errMsg := validateAPIHomeDisplaySettings(in); errMsg != "" {
		apiError(w, http.StatusBadRequest, "bad_request", errMsg)
		return
	}
	for i := range items {
		if items[i].hasTextFields() && !automationScopeAllowed(auth.scopes, apiScopeSiteWrite) {
			apiError(w, http.StatusForbidden, "missing_scope", "这条访问权限不能修改站点文案。")
			return
		}
		if items[i].hasBrandAssetFields() && !automationScopeAllowed(auth.scopes, apiScopeBrandAssetsWrite) {
			apiError(w, http.StatusForbidden, "missing_scope", "这条访问权限不能修改品牌资产。")
			return
		}
		if errMsg := s.applyAPISiteProfileInput(&items[i]); errMsg != "" {
			apiError(w, http.StatusBadRequest, "bad_request", errMsg)
			return
		}
	}
	if errMsg := s.applyAPIHomeDisplaySettings(in); errMsg != "" {
		apiError(w, http.StatusInternalServerError, "store_error", errMsg)
		return
	}
	s.recordAutomationLog(auth, "update", "site", 0, "更新站点资料")
	s.clearGeneratedCaches()
	writeJSON(w, http.StatusOK, s.apiSiteProfileResponse())
}

func (s *Server) apiGetNavigation(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAutomationScope(w, r, apiScopeNavigationRead); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.apiNavigationResponse())
}

func (s *Server) apiUpdateNavigation(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requireAutomationScope(w, r, apiScopeNavigationWrite)
	if !ok {
		return
	}
	var in apiNavigationInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	if len(in.Items) > 50 {
		apiError(w, http.StatusBadRequest, "too_many_items", "导航菜单最多 50 项。")
		return
	}
	rows := make([]MenuRow, 0, len(in.Items))
	for _, item := range in.Items {
		u := strings.TrimSpace(item.URL)
		if u == "" {
			continue
		}
		if !strings.HasPrefix(u, "/") && !isExternalURL(u) {
			apiError(w, http.StatusBadRequest, "bad_url", "导航 URL 需要是站内路径（/ 开头）或完整外部链接。")
			return
		}
		labels := map[string]string{}
		for k, v := range item.Labels {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			labels[k] = strings.TrimSpace(v)
		}
		rows = append(rows, MenuRow{URL: u, Labels: labels})
	}
	currentRows, _, _ := s.effectiveMenuRows()
	if auth.platform && navigationRowsRemoved(currentRows, rows) {
		apiError(w, http.StatusForbidden, "navigation_delete_requires_control", "删除或替换导航项需要使用 Pilot 受控删除，并验证 GCMS 后台密码。")
		return
	}
	b, _ := json.Marshal(rows)
	if err := s.store.SetSetting("nav_menu", string(b)); err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	s.recordAutomationLog(auth, "update", "navigation", 0, "更新前台导航菜单")
	s.clearGeneratedCaches()
	writeJSON(w, http.StatusOK, s.apiNavigationResponse())
}

func (s *Server) apiListCategories(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, scope, ok := s.apiCategoryReadTarget(collection)
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	if _, ok := s.requireAutomationScope(w, r, scope); !ok {
		return
	}
	lang := strings.TrimSpace(r.URL.Query().Get("lang"))
	if lang == "" {
		lang = s.defaultLang()
	}
	if lang != "all" && !s.langEnabled(lang) {
		apiError(w, http.StatusBadRequest, "bad_lang", "语种未启用。")
		return
	}
	var (
		cats []*store.Category
		err  error
	)
	if lang == "all" {
		cats, err = s.store.AllCategories(kind)
	} else {
		cats, err = s.store.ListCategories(lang, kind)
	}
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	items := make([]apiCategory, 0, len(cats))
	for _, cat := range cats {
		items = append(items, apiCategoryItem(cat))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "lang": lang, "kind": kind})
}

func (s *Server) apiGetCategoryAllEntry(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, scope, ok := s.apiCategoryReadTarget(collection)
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	if _, ok := s.requireAutomationScope(w, r, scope); !ok {
		return
	}
	lang := strings.TrimSpace(r.URL.Query().Get("lang"))
	if lang == "" {
		lang = s.defaultLang()
	}
	items, errMsg := s.apiCategoryAllEntryItems(kind, lang)
	if errMsg != "" {
		apiError(w, http.StatusBadRequest, "bad_lang", errMsg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "lang": lang, "kind": kind})
}

func (s *Server) apiUpdateCategoryAllEntry(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, scope, ok := s.apiCategoryWriteTarget(collection)
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	auth, ok := s.requireAutomationScope(w, r, scope)
	if !ok {
		return
	}
	var in apiCategoryAllEntryPatch
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	items := in.Items
	if len(items) == 0 && in.hasFields() {
		items = []apiCategoryAllEntryInput{in.apiCategoryAllEntryInput}
	}
	if len(items) == 0 {
		apiError(w, http.StatusBadRequest, "empty_patch", "没有收到需要更新的分类入口。")
		return
	}
	out := make([]apiCategoryAllEntry, 0, len(items))
	for i := range items {
		lang := s.defaultLang()
		if items[i].Lang != nil && strings.TrimSpace(*items[i].Lang) != "" {
			lang = strings.TrimSpace(*items[i].Lang)
		}
		if lang == "all" || !s.langEnabled(lang) {
			apiError(w, http.StatusBadRequest, "bad_lang", "语种未启用。")
			return
		}
		entry, errMsg, err := s.applyAPICategoryAllEntryInput(kind, lang, &items[i])
		if err != nil {
			apiError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		if errMsg != "" {
			apiError(w, http.StatusBadRequest, "bad_request", errMsg)
			return
		}
		out = append(out, entry)
	}
	s.recordAutomationLog(auth, "update", kind+"-category-all-entry", 0, "更新"+s.apiKindLabel(kind)+"分类总入口")
	responseLang := "all"
	if len(out) == 1 {
		responseLang = out[0].Lang
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "lang": responseLang, "kind": kind})
}

func (s *Server) apiCreateCategory(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, scope, ok := s.apiCategoryWriteTarget(collection)
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	auth, ok := s.requireAutomationScope(w, r, scope)
	if !ok {
		return
	}
	var in apiCategoryInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	lang := s.defaultLang()
	if in.Lang != nil && strings.TrimSpace(*in.Lang) != "" {
		lang = strings.TrimSpace(*in.Lang)
	}
	if !s.langEnabled(lang) {
		apiError(w, http.StatusBadRequest, "bad_lang", "语种未启用。")
		return
	}
	name := ""
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if name == "" {
		apiError(w, http.StatusBadRequest, "bad_name", "分类名称不能为空。")
		return
	}
	slug := ""
	if in.Slug != nil {
		slug = slugify(strings.TrimSpace(*in.Slug))
	}
	if slug == "" {
		slug = slugify(name)
	}
	if slug == "" {
		slug = "cat-" + strconv.FormatInt(time.Now().Unix(), 36)
	}
	slug = s.uniqueAPICategorySlug(lang, slug, 0)
	description := ""
	if in.Description != nil {
		description = strings.TrimSpace(*in.Description)
	}
	transGroup := ""
	if in.TransGroup != nil {
		transGroup = strings.TrimSpace(*in.TransGroup)
	}
	cat := &store.Category{Slug: slug, Name: name, Description: description, Lang: lang, Kind: kind, TransGroup: transGroup}
	id, err := s.store.CreateCategory(cat)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	cat, _ = s.store.GetCategoryByID(id)
	s.recordAutomationLog(auth, "create", kind+"-category", id, "创建"+s.apiKindLabel(kind)+"分类："+name)
	s.clearGeneratedCaches()
	writeJSON(w, http.StatusCreated, map[string]any{"item": apiCategoryItem(cat)})
}

func (s *Server) apiUpdateCategory(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, scope, ok := s.apiCategoryWriteTarget(collection)
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	auth, ok := s.requireAutomationScope(w, r, scope)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		apiError(w, http.StatusBadRequest, "bad_id", "分类 ID 无效。")
		return
	}
	existing, err := s.store.GetCategoryByID(id)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if existing == nil || existing.Kind != kind {
		apiError(w, http.StatusNotFound, "not_found", "分类不存在。")
		return
	}
	var in apiCategoryInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	next := *existing
	if in.Name != nil {
		next.Name = strings.TrimSpace(*in.Name)
	}
	if next.Name == "" {
		apiError(w, http.StatusBadRequest, "bad_name", "分类名称不能为空。")
		return
	}
	if in.Description != nil {
		next.Description = strings.TrimSpace(*in.Description)
	}
	if in.TransGroup != nil {
		next.TransGroup = strings.TrimSpace(*in.TransGroup)
	}
	if in.Slug != nil {
		next.Slug = slugify(strings.TrimSpace(*in.Slug))
		if next.Slug == "" {
			next.Slug = slugify(next.Name)
		}
		if next.Slug == "" {
			next.Slug = "cat-" + strconv.FormatInt(time.Now().Unix(), 36)
		}
		next.Slug = s.uniqueAPICategorySlug(next.Lang, next.Slug, next.ID)
	}
	if next.TransGroup == "" {
		next.TransGroup = next.Lang + ":" + next.Slug
	}
	if err := s.store.UpdateCategory(&next); err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	updated, _ := s.store.GetCategoryByID(next.ID)
	s.recordAutomationLog(auth, "update", kind+"-category", next.ID, "更新"+s.apiKindLabel(kind)+"分类："+next.Name)
	s.clearGeneratedCaches()
	writeJSON(w, http.StatusOK, map[string]any{"item": apiCategoryItem(updated)})
}

func (s *Server) apiUploadMedia(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requireAutomationScope(w, r, apiScopeMediaWrite)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		apiError(w, http.StatusBadRequest, "bad_multipart", "表单解析失败或文件过大。")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		apiError(w, http.StatusBadRequest, "missing_file", "未收到文件。")
		return
	}
	defer file.Close()
	result, err := s.saveUploadFile(file, hdr.Filename)
	if err != nil {
		switch err.Error() {
		case "upload_disabled":
			apiError(w, http.StatusServiceUnavailable, "upload_disabled", "上传未启用。")
		case "bad_type":
			apiError(w, http.StatusBadRequest, "bad_type", "仅支持 jpg、png、gif、webp、svg、ico、avif。")
		case "save_failed":
			apiError(w, http.StatusInternalServerError, "save_failed", "保存失败。")
		default:
			apiError(w, http.StatusBadRequest, "write_failed", "文件过大或写入失败。")
		}
		return
	}
	s.recordAutomationLog(auth, "upload", "media", 0, "上传媒体："+result.URL)
	writeJSON(w, http.StatusCreated, map[string]any{"url": result.URL, "name": result.Name, "size": result.Size})
}

func (s *Server) apiListContent(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, ok := s.apiContentKind(collection)
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	auth, ok := s.requireAutomationScope(w, r, apiScope(collection, "read"))
	if !ok {
		return
	}
	lang := strings.TrimSpace(r.URL.Query().Get("lang"))
	transGroup := strings.TrimSpace(r.URL.Query().Get("trans_group"))
	if lang == "" {
		if transGroup != "" {
			lang = "all"
		} else {
			lang = s.defaultLang()
		}
	}
	if lang != "all" && !s.langEnabled(lang) {
		apiError(w, http.StatusBadRequest, "bad_lang", "语种未启用。")
		return
	}
	limit := apiIntParam(r, "limit", 20)
	offset := apiIntParam(r, "offset", 0)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	items, err := s.store.ListContentForAutomation(kind, lang, status, query, slug, transGroup, offset, limit)
	if err != nil {
		apiError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	out := make([]apiContentItem, 0, len(items))
	for _, p := range items {
		out = append(out, s.apiContentItem(p, false))
	}
	_ = auth
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "limit": limit, "offset": offset, "lang": lang, "q": query, "slug": slug, "trans_group": transGroup})
}

func (s *Server) apiGetContent(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, ok := s.apiContentKind(collection)
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	if _, ok := s.requireAutomationScope(w, r, apiScope(collection, "read")); !ok {
		return
	}
	p, ok := s.apiContentByID(w, r, kind)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": s.apiContentItem(p, true)})
}

func (s *Server) apiPreviewContent(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	if collection != "posts" && collection != "links" {
		apiError(w, http.StatusNotFound, "not_found", "草稿预览仅支持文章和链接。")
		return
	}
	kind, _ := s.apiContentKind(collection)
	if _, ok := s.requireAutomationScope(w, r, apiScope(collection, "read")); !ok {
		return
	}
	p, ok := s.apiContentByID(w, r, kind)
	if !ok {
		return
	}
	preview := *p
	if preview.PublishedAt.IsZero() {
		preview.PublishedAt = preview.UpdatedAt
		if preview.PublishedAt.IsZero() {
			preview.PublishedAt = preview.CreatedAt
		}
	}
	html, toc := s.renderedContent(&preview)
	frontendURL, frontendExp, _ := s.frontendPreviewURL(r, collection, p, time.Now().Add(frontendPreviewTTL))
	writeJSON(w, http.StatusOK, map[string]any{
		"preview": apiContentPreview{
			Item:                     s.apiContentItem(&preview, true),
			PreviewURL:               s.absForRequest(r, fmt.Sprintf("/api/admin/v1/%s/%d/preview", collection, preview.ID)),
			FrontendPreviewURL:       frontendURL,
			FrontendPreviewExpiresAt: apiTime(frontendExp),
			PublicURL:                s.absForRequest(r, s.apiContentURL(&preview)),
			ContentHTML:              string(html),
			TOC:                      apiHeadings(toc),
			Robots:                   "noindex, nofollow",
		},
	})
}

func (s *Server) apiCreatePreviewURL(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	if collection != "posts" && collection != "links" {
		apiError(w, http.StatusNotFound, "not_found", "草稿预览仅支持文章和链接。")
		return
	}
	kind, _ := s.apiContentKind(collection)
	if _, ok := s.requireAutomationScope(w, r, apiScope(collection, "read")); !ok {
		return
	}
	p, ok := s.apiContentByID(w, r, kind)
	if !ok {
		return
	}
	expires := time.Now().Add(frontendPreviewTTL)
	previewURL, expires, err := s.frontendPreviewURL(r, collection, p, expires)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "sign_failed", "生成预览链接失败。")
		return
	}
	writeJSON(w, http.StatusCreated, apiPreviewURLResponse{
		PreviewURL: previewURL,
		ExpiresAt:  apiTime(expires),
		TTLSeconds: int64(frontendPreviewTTL.Seconds()),
	})
}

func (s *Server) apiCreateContent(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, ok := s.apiContentKind(collection)
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	auth, ok := s.requireAutomationScope(w, r, apiScope(collection, "write"))
	if !ok {
		return
	}
	var in apiContentInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	p := &store.Post{Type: kind, Status: "draft", Lang: s.defaultLang(), EditorMode: "markdown"}
	if in.Lang != nil && strings.TrimSpace(*in.Lang) != "" {
		p.Lang = strings.TrimSpace(*in.Lang)
	}
	if !s.langEnabled(p.Lang) {
		apiError(w, http.StatusBadRequest, "bad_lang", "语种未启用。")
		return
	}
	publishNeeded, errMsg := s.applyAPIContentInput(p, &in, true)
	if errMsg != "" {
		apiError(w, http.StatusBadRequest, "bad_request", errMsg)
		return
	}
	if errMsg := validateCanonicalOverride(in.CanonicalOverride); errMsg != "" {
		apiError(w, http.StatusUnprocessableEntity, "invalid_canonical", errMsg)
		return
	}
	s.applyExtraFields(p, kind, in.Fields)
	if publishNeeded && !automationScopeAllowed(auth.scopes, apiScope(collection, "publish")) {
		apiError(w, http.StatusForbidden, "missing_scope", "这条访问权限不能发布该类内容。")
		return
	}
	// 发布质量门：仅自动化 API 创建即发布、且类型有规则集（posts/product；admin 后台人工发布不拦）。
	if qualityGateApplies(kind, &in, p) {
		if failures := qualityGateFailures(kind, p); len(failures) > 0 {
			apiQualityGateError(w, failures)
			return
		}
	}
	if errMsg := s.validateAPICategory(p); errMsg != "" {
		apiError(w, http.StatusBadRequest, "bad_category", errMsg)
		return
	}
	s.fillDefaultAuthor(p)
	p.Slug = s.uniqueSlug(p.Lang, p.Slug, 0)
	id, err := s.store.CreatePost(p)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	p.ID = id
	created, _ := s.store.GetPostByID(id)
	s.recordAutomationLog(auth, "create", kind, id, s.automationContentLogMessage("create", kind, created))
	s.clearGeneratedCaches()
	s.firePublishHooks(r, created)
	writeJSON(w, http.StatusCreated, map[string]any{"item": s.apiContentItem(created, true)})
}

func (s *Server) apiUpdateContent(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, ok := s.apiContentKind(collection)
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	auth, ok := s.requireAutomationScope(w, r, apiScope(collection, "write"))
	if !ok {
		return
	}
	existing, ok := s.apiContentByID(w, r, kind)
	if !ok {
		return
	}
	var in apiContentInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	if existing.Status != "draft" && !automationScopeAllowed(auth.scopes, apiScope(collection, "publish")) {
		apiError(w, http.StatusForbidden, "missing_scope", "修改已发布或定时内容需要该类内容的发布权限。")
		return
	}
	next := *existing
	publishNeeded, errMsg := s.applyAPIContentInput(&next, &in, false)
	if errMsg != "" {
		apiError(w, http.StatusBadRequest, "bad_request", errMsg)
		return
	}
	if errMsg := validateCanonicalOverride(in.CanonicalOverride); errMsg != "" {
		apiError(w, http.StatusUnprocessableEntity, "invalid_canonical", errMsg)
		return
	}
	s.applyExtraFields(&next, kind, in.Fields)
	if publishNeeded && !automationScopeAllowed(auth.scopes, apiScope(collection, "publish")) {
		apiError(w, http.StatusForbidden, "missing_scope", "这条访问权限不能发布该类内容。")
		return
	}
	// 发布质量门：仅自动化 API 把 status 置为 published 的更新、且类型有规则集（admin 后台人工发布不拦）。
	if qualityGateApplies(kind, &in, &next) {
		if failures := qualityGateFailures(kind, &next); len(failures) > 0 {
			apiQualityGateError(w, failures)
			return
		}
	}
	if errMsg := s.validateAPICategory(&next); errMsg != "" {
		apiError(w, http.StatusBadRequest, "bad_category", errMsg)
		return
	}
	s.fillDefaultAuthor(&next)
	next.Slug = s.uniqueSlug(next.Lang, next.Slug, next.ID)
	if err := s.store.UpdatePostFrom(&next, store.PostRevisionSourceAPI); err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	updated, _ := s.store.GetPostByID(next.ID)
	s.recordAutomationLog(auth, "update", kind, next.ID, s.automationContentLogMessage("update", kind, &next))
	s.clearGeneratedCaches()
	s.firePublishHooks(r, updated)
	writeJSON(w, http.StatusOK, map[string]any{"item": s.apiContentItem(updated, true)})
}

type apiRelinkInput struct {
	TransGroup *string `json:"trans_group,omitempty"`
	LinkToID   *int64  `json:"link_to_id,omitempty"`
}

// relinkPost 是"重连互译组"的唯一受控入口：校验后把 p.TransGroup 改成 group 并保存。
// 返回错误信息（空＝成功）。API 与后台共用，规则一致。
//   - 目标组非空、且已有成员（join 真实的组，防打错字造孤儿组）
//   - 组内成员必须同 type（不混 post/page/link）
//   - 组内不能已有同 lang 的另一篇（一个互译组每种语言仅一篇）
func (s *Server) relinkPost(p *store.Post, group, source string) string {
	group = strings.TrimSpace(group)
	if group == "" {
		return "目标互译组不能为空。"
	}
	if group == p.TransGroup {
		return "" // 已在该组，无需变更
	}
	members, err := s.store.TranslationsAll(group, p.ID)
	if err != nil {
		return "读取目标组失败：" + err.Error()
	}
	if len(members) == 0 {
		return "目标互译组不存在（组内没有其它内容）。请用 link_to_id 指向一篇已存在的兄弟内容，或确认 trans_group 是否填对。"
	}
	for _, m := range members {
		if m.Type != p.Type {
			return fmt.Sprintf("目标组里有不同类型的内容（%s #%d），不能和当前 %s 混到一组。", m.Type, m.ID, p.Type)
		}
		if m.Lang == p.Lang {
			return fmt.Sprintf("目标组里已有一篇 %s 语种的内容（#%d），一个互译组每种语言只能有一篇。", m.Lang, m.ID)
		}
	}
	p.TransGroup = group
	if err := s.store.UpdatePostFrom(p, source); err != nil {
		return "保存失败：" + err.Error()
	}
	return ""
}

// apiRelinkContent 把一篇已存在的内容重连到某互译组（唯一能改 trans_group 的 API）。
// body 二选一：{ "link_to_id": <兄弟内容 id> } 或 { "trans_group": "<组键>" }。
func (s *Server) apiRelinkContent(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, ok := s.apiContentKind(collection)
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	auth, ok := s.requireAutomationScope(w, r, apiScope(collection, "write"))
	if !ok {
		return
	}
	existing, ok := s.apiContentByID(w, r, kind)
	if !ok {
		return
	}
	var in apiRelinkInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	// 重连已发布/定时内容会改线上 hreflang 与语言切换 → 和编辑已发布同款权限。
	if existing.Status != "draft" && !automationScopeAllowed(auth.scopes, apiScope(collection, "publish")) {
		apiError(w, http.StatusForbidden, "missing_scope", "重连已发布或定时内容需要该类内容的发布权限。")
		return
	}
	group := ""
	switch {
	case in.LinkToID != nil:
		target, err := s.store.GetPostByID(*in.LinkToID)
		if err != nil || target == nil {
			apiError(w, http.StatusBadRequest, "bad_request", "link_to_id 指向的内容不存在。")
			return
		}
		if target.ID == existing.ID {
			apiError(w, http.StatusBadRequest, "bad_request", "不能关联到自己。")
			return
		}
		group = target.TransGroup
	case in.TransGroup != nil:
		group = strings.TrimSpace(*in.TransGroup)
	default:
		apiError(w, http.StatusBadRequest, "bad_request", "需要提供 link_to_id 或 trans_group。")
		return
	}
	if msg := s.relinkPost(existing, group, store.PostRevisionSourceAPI); msg != "" {
		apiError(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	updated, _ := s.store.GetPostByID(existing.ID)
	members, _ := s.store.TranslationsAll(existing.TransGroup, 0) // exceptID=0 → 含自己，即整组
	list := make([]map[string]any, 0, len(members))
	for _, m := range members {
		list = append(list, map[string]any{"id": m.ID, "lang": m.Lang, "title": m.Title, "status": m.Status, "type": m.Type})
	}
	s.recordAutomationLog(auth, "relink", kind, existing.ID, fmt.Sprintf("relink %s#%d → trans_group=%s", kind, existing.ID, existing.TransGroup))
	s.clearGeneratedCaches()
	writeJSON(w, http.StatusOK, map[string]any{"item": s.apiContentItem(updated, true), "trans_group": existing.TransGroup, "members": list})
}

func (s *Server) apiUpdatePostFeatured(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("collection", "posts")
	s.apiUpdateContentFeatured(w, r)
}

func (s *Server) apiUpdateLinkFeatured(w http.ResponseWriter, r *http.Request) {
	r.SetPathValue("collection", "links")
	s.apiUpdateContentFeatured(w, r)
}

func (s *Server) apiUpdateContentFeatured(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("collection")
	kind, ok := s.apiContentKind(collection)
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	}
	// posts/links 用专属 pin scope；扩展集合（商品等，无 pin scope 枚举）与内容写同权。
	// pages 没有置顶语义，维持 404。
	scope := apiScope(collection, "pin")
	switch kind {
	case "post", "link":
	case "page":
		apiError(w, http.StatusNotFound, "not_found", "接口不存在。")
		return
	default:
		scope = apiScope(collection, "write")
	}
	auth, ok := s.requireAutomationScope(w, r, scope)
	if !ok {
		return
	}
	existing, ok := s.apiContentByID(w, r, kind)
	if !ok {
		return
	}
	var in apiFeaturedInput
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	if in.Featured == nil {
		apiError(w, http.StatusBadRequest, "bad_request", "featured 不能为空。")
		return
	}
	if err := s.store.SetFeatured(existing.ID, *in.Featured); err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	updated, _ := s.store.GetPostByID(existing.ID)
	action := "pin"
	label := "置顶"
	if !*in.Featured {
		action = "unpin"
		label = "取消置顶"
	}
	s.recordAutomationLog(auth, action, kind, existing.ID, fmt.Sprintf("%s%s：%s", label, apiKindName(kind), existing.Title))
	s.clearGeneratedCaches()
	writeJSON(w, http.StatusOK, map[string]any{"item": s.apiContentItem(updated, true)})
}

func (s *Server) automationContentLogMessage(action, kind string, p *store.Post) string {
	actionLabel := "更新"
	if action == "create" {
		actionLabel = "创建"
	}
	title := ""
	status := ""
	lang := ""
	if p != nil {
		title = p.Title
		status = p.Status
		lang = p.Lang
	}
	return fmt.Sprintf("%s%s（%s · %s）：%s", actionLabel, apiKindName(kind), apiStatusLabel(status), s.apiLangLabel(lang), title)
}

func apiStatusLabel(status string) string {
	switch strings.TrimSpace(status) {
	case "published":
		return "已发布"
	case "scheduled":
		return "定时"
	case "draft", "":
		return "草稿"
	default:
		return strings.TrimSpace(status)
	}
}

func (s *Server) apiLangLabel(code string) string {
	code = strings.TrimSpace(code)
	for _, loc := range s.locales() {
		if loc.Code == code {
			return loc.Name
		}
	}
	if code == "" {
		return s.defaultLang()
	}
	return code
}

func (s *Server) requireAutomationToken(w http.ResponseWriter, r *http.Request) (*automationAuth, bool) {
	// 平台密钥路径：鉴权与限流已在 servePlatformAPI 平台层完成。
	// 此分支必须是本函数第一条语句——放在 checkAPIRateLimit 之前，否则平台请求会被二次计数、令牌桶减半。
	if pid, ok := platformIdentityFrom(r.Context()); ok {
		return &automationAuth{platform: true, platKeyID: pid.keyID, scopes: pid.scopes}, true
	}
	token := apiTokenFromRequest(r)
	if !s.checkAPIRateLimit(w, r, token) {
		return nil, false
	}
	if token == "" {
		apiError(w, http.StatusUnauthorized, "missing_token", "缺少访问密钥。")
		return nil, false
	}
	key, exists, err := s.store.GetAutomationKeyByToken(token)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "auth_error", err.Error())
		return nil, false
	}
	if !exists {
		apiError(w, http.StatusUnauthorized, "invalid_token", "访问密钥无效或已吊销。")
		return nil, false
	}
	auth := &automationAuth{key: key, scopes: apiScopeMap(key.Scopes)}
	_ = s.store.TouchAutomationKey(key.ID)
	return auth, true
}

func (s *Server) requireAutomationScope(w http.ResponseWriter, r *http.Request, scope string) (*automationAuth, bool) {
	auth, ok := s.requireAutomationToken(w, r)
	if !ok {
		return nil, false
	}
	if !automationScopeAllowed(auth.scopes, scope) {
		apiError(w, http.StatusForbidden, "missing_scope", "访问权限不足。")
		return nil, false
	}
	return auth, true
}

func (s *Server) requireAutomationAnyScope(w http.ResponseWriter, r *http.Request, scopes ...string) (*automationAuth, bool) {
	auth, ok := s.requireAutomationToken(w, r)
	if !ok {
		return nil, false
	}
	for _, scope := range scopes {
		if automationScopeAllowed(auth.scopes, scope) {
			return auth, true
		}
	}
	apiError(w, http.StatusForbidden, "missing_scope", "访问权限不足。")
	return nil, false
}

func (s *Server) apiContentByID(w http.ResponseWriter, r *http.Request, kind string) (*store.Post, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		apiError(w, http.StatusBadRequest, "bad_id", "内容 ID 无效。")
		return nil, false
	}
	p, err := s.store.GetPostByID(id)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "store_error", err.Error())
		return nil, false
	}
	if p == nil || p.Type != kind {
		apiError(w, http.StatusNotFound, "not_found", "内容不存在。")
		return nil, false
	}
	return p, true
}

func (in apiSiteProfileInput) hasTextFields() bool {
	return in.Name != nil || in.Tagline != nil || in.Description != nil || in.Keywords != nil ||
		in.HeroEyebrow != nil || in.HeroTitle != nil || in.HeroDescription != nil || in.FooterNote != nil ||
		in.HomeFeaturedTitle != nil || in.HomeLinksTitle != nil || in.HomeLatestTitle != nil ||
		in.DefaultPostAuthor != nil || in.DefaultLinkAuthor != nil ||
		in.FactoryStats != nil || in.FactoryProcess != nil || in.FactoryCTA != nil || // 主题 options 槽按站点文案权限（site:write）
		in.FactoryCategories != nil || in.FactoryIndustries != nil || in.FactoryGallery != nil || in.FactoryFAQ != nil ||
		in.DTCTestimonials != nil
}

func (in apiSiteProfileInput) hasBrandAssetFields() bool {
	return in.Logo != nil || in.Favicon != nil || in.ShareImage != nil ||
		in.HeroVisual != nil || in.HeroImage != nil
}

func (in apiSiteProfileInput) hasFields() bool {
	return in.hasTextFields() || in.hasBrandAssetFields()
}

func (in apiSiteProfilePatch) hasFields() bool {
	return in.apiSiteProfileInput.hasFields() || in.hasHomeDisplayFields()
}

func (in apiSiteProfilePatch) hasHomeDisplayFields() bool {
	return in.HomeLinksLimit != nil || in.HomePostsPerPage != nil
}

func validateAPIHomeDisplaySettings(in apiSiteProfilePatch) string {
	if in.HomeLinksLimit != nil && (*in.HomeLinksLimit < minHomeLinksLimit || *in.HomeLinksLimit > maxHomeLinksLimit) {
		return "home_links_limit 必须在 " + strconv.Itoa(minHomeLinksLimit) + " 到 " + strconv.Itoa(maxHomeLinksLimit) + " 之间。"
	}
	if in.HomePostsPerPage != nil && (*in.HomePostsPerPage < minHomePostsPerPage || *in.HomePostsPerPage > maxHomePostsPerPage) {
		return "home_posts_per_page 必须在 " + strconv.Itoa(minHomePostsPerPage) + " 到 " + strconv.Itoa(maxHomePostsPerPage) + " 之间。"
	}
	return ""
}

func (s *Server) applyAPIHomeDisplaySettings(in apiSiteProfilePatch) string {
	if in.HomeLinksLimit != nil {
		if err := s.store.SetSetting(homeLinksLimitKey, strconv.Itoa(*in.HomeLinksLimit)); err != nil {
			return err.Error()
		}
	}
	if in.HomePostsPerPage != nil {
		if err := s.store.SetSetting(homePostsPerPageKey, strconv.Itoa(*in.HomePostsPerPage)); err != nil {
			return err.Error()
		}
	}
	return ""
}

func (s *Server) apiSiteProfileResponse() map[string]any {
	locales := s.locales()
	items := make([]apiSiteProfileItem, 0, len(locales))
	for _, loc := range locales {
		items = append(items, s.apiSiteProfileItem(loc.Code))
	}
	return map[string]any{
		"default":             s.defaultLang(),
		"home_links_limit":    s.intSetting(homeLinksLimitKey, defaultHomeLinksLimit, minHomeLinksLimit, maxHomeLinksLimit),
		"home_posts_per_page": s.intSetting(homePostsPerPageKey, defaultHomePostsPerPage, minHomePostsPerPage, maxHomePostsPerPage),
		"items":               items,
	}
}

func (s *Server) apiSiteProfileItem(lang string) apiSiteProfileItem {
	st := s.site(lang)
	item := apiSiteProfileItem{
		Lang:              lang,
		Name:              st.Name,
		Tagline:           st.Tagline,
		Description:       st.Description,
		Keywords:          st.Keywords,
		HeroEyebrow:       st.HeroEyebrow,
		HeroTitle:         st.HeroTitle,
		HeroDescription:   st.HeroDescription,
		FooterNote:        st.FooterNote,
		HomeFeaturedTitle: st.HomeFeatured,
		HomeLinksTitle:    st.HomeLinks,
		HomeLatestTitle:   st.HomeLatest,
		DefaultPostAuthor: s.defaultContentAuthor("post", lang),
		DefaultLinkAuthor: s.defaultContentAuthor("link", lang),
		Logo:              st.Logo,
		Favicon:           st.Favicon,
		ShareImage:        st.ShareImage,
		HeroVisual:        st.HeroVisual,
		HeroImage:         st.HeroImage,
	}
	// 主题 options（工厂族数据槽）：返回该语种生效的 settings 覆盖（不含 i18n 内置默认）。
	item.FactoryStats = parseFactoryStats(s.localizedSetting(factoryStatsSettingKey, lang, ""))
	if steps := parseFactorySteps(s.localizedSetting(factoryProcessSettingKey, lang, "")); len(steps) > 0 || !s.factoryProcessEnabled() {
		item.FactoryProcess = &apiFactoryProcessItem{Enabled: s.factoryProcessEnabled(), Steps: steps}
	}
	if cta := parseFactoryTextPair(s.localizedSetting(factoryCTASettingKey, lang, "")); cta.Title != "" || cta.Note != "" {
		item.FactoryCTA = &cta
	}
	if !s.factorySectionEnabled(factoryCategoriesEnabledKey) {
		off := false
		item.FactoryCategories = &apiFactoryToggleInput{Enabled: &off}
	}
	if items := parseFactoryIndustries(s.localizedSetting(factoryIndustriesSettingKey, lang, "")); len(items) > 0 || !s.factorySectionEnabled(factoryIndustriesEnabledKey) {
		item.FactoryIndustries = &apiFactoryIndustriesItem{Enabled: s.factorySectionEnabled(factoryIndustriesEnabledKey), Items: items}
	}
	item.FactoryGallery = parseFactoryGallery(s.store.Setting(factoryGallerySettingKey))
	if items := parseFactoryQAs(s.localizedSetting(factoryFAQSettingKey, lang, "")); len(items) > 0 || !s.factorySectionEnabled(factoryFAQEnabledKey) {
		item.FactoryFAQ = &apiFactoryFAQItem{Enabled: s.factorySectionEnabled(factoryFAQEnabledKey), Items: items}
	}
	item.DTCTestimonials = parseDTCTestimonials(s.localizedSetting(dtcTestimonialsSettingKey, lang, ""))
	return item
}

// applyAPIFactoryOptions 主题 options（工厂族三槽）的 API 写入：
// 校验格式后按语种落 settings 语义键（存规范化 JSON；[]/null/全空 = 清除覆盖回落默认）。
func (s *Server) applyAPIFactoryOptions(in *apiSiteProfileInput, lang string) string {
	if in.FactoryStats != nil {
		var rows []map[string]any
		if err := json.Unmarshal(in.FactoryStats, &rows); err != nil && strings.TrimSpace(string(in.FactoryStats)) != "null" {
			return "factory_stats 需要 [{num,label}] 数组。"
		}
		stats := parseFactoryStats(string(in.FactoryStats))
		if len(rows) > 0 && len(stats) == 0 {
			return "factory_stats 每组需要非空的 num 与 label（最多 " + strconv.Itoa(maxFactoryStats) + " 组）。"
		}
		if err := s.store.SetSetting(s.copyKey(factoryStatsSettingKey, lang), marshalThemeOptionJSON(stats, len(stats) == 0)); err != nil {
			return err.Error()
		}
	}
	if in.FactoryProcess != nil {
		if in.FactoryProcess.Enabled != nil {
			flag := "" // 开（缺省语义）
			if !*in.FactoryProcess.Enabled {
				flag = factoryProcessDisabledFlag
			}
			if err := s.store.SetSetting(factoryProcessEnabledKey, flag); err != nil { // 全局开关，不分语种
				return err.Error()
			}
		}
		if in.FactoryProcess.Steps != nil {
			var rows []map[string]any
			if err := json.Unmarshal(in.FactoryProcess.Steps, &rows); err != nil && strings.TrimSpace(string(in.FactoryProcess.Steps)) != "null" {
				return "factory_process.steps 需要 [{title,note}] 数组（按位对应四步，空字段回落默认）。"
			}
			steps := parseFactorySteps(string(in.FactoryProcess.Steps))
			empty := true
			for _, st := range steps {
				if strings.TrimSpace(st.Title) != "" || strings.TrimSpace(st.Note) != "" {
					empty = false
					break
				}
			}
			if err := s.store.SetSetting(s.copyKey(factoryProcessSettingKey, lang), marshalThemeOptionJSON(steps, empty)); err != nil {
				return err.Error()
			}
		}
	}
	if in.FactoryCTA != nil {
		cta := FactoryTextPair{Title: strings.TrimSpace(in.FactoryCTA.Title), Note: strings.TrimSpace(in.FactoryCTA.Note)}
		if err := s.store.SetSetting(s.copyKey(factoryCTASettingKey, lang), marshalThemeOptionJSON(cta, cta.Title == "" && cta.Note == "")); err != nil {
			return err.Error()
		}
	}
	if in.FactoryCategories != nil && in.FactoryCategories.Enabled != nil {
		if err := s.store.SetSetting(factoryCategoriesEnabledKey, apiToggleFlag(*in.FactoryCategories.Enabled)); err != nil {
			return err.Error()
		}
	}
	if in.FactoryIndustries != nil {
		if in.FactoryIndustries.Enabled != nil {
			if err := s.store.SetSetting(factoryIndustriesEnabledKey, apiToggleFlag(*in.FactoryIndustries.Enabled)); err != nil {
				return err.Error()
			}
		}
		if in.FactoryIndustries.Items != nil {
			var rows []map[string]any
			if err := json.Unmarshal(in.FactoryIndustries.Items, &rows); err != nil && strings.TrimSpace(string(in.FactoryIndustries.Items)) != "null" {
				return "factory_industries.items 需要 [{name,note}] 数组。"
			}
			items := parseFactoryIndustries(string(in.FactoryIndustries.Items))
			if len(rows) > 0 && len(items) == 0 {
				return "factory_industries.items 每项需要非空的 name（note 可空，最多 " + strconv.Itoa(maxFactoryIndustries) + " 项）。"
			}
			if err := s.store.SetSetting(s.copyKey(factoryIndustriesSettingKey, lang), marshalThemeOptionJSON(items, len(items) == 0)); err != nil {
				return err.Error()
			}
		}
	}
	if in.FactoryGallery != nil {
		var rows []any
		if err := json.Unmarshal(in.FactoryGallery, &rows); err != nil && strings.TrimSpace(string(in.FactoryGallery)) != "null" {
			return "factory_gallery 需要图片 URL 字符串数组。"
		}
		items := parseFactoryGallery(string(in.FactoryGallery))
		if len(rows) > 0 && len(items) == 0 {
			return "factory_gallery 每项需要非空的图片 URL（最多 " + strconv.Itoa(maxFactoryGallery) + " 张）。"
		}
		// 图集全局（不分语种）：写裸键。
		if err := s.store.SetSetting(factoryGallerySettingKey, marshalThemeOptionJSON(items, len(items) == 0)); err != nil {
			return err.Error()
		}
	}
	if in.FactoryFAQ != nil {
		if in.FactoryFAQ.Enabled != nil {
			if err := s.store.SetSetting(factoryFAQEnabledKey, apiToggleFlag(*in.FactoryFAQ.Enabled)); err != nil {
				return err.Error()
			}
		}
		if in.FactoryFAQ.Items != nil {
			var rows []map[string]any
			if err := json.Unmarshal(in.FactoryFAQ.Items, &rows); err != nil && strings.TrimSpace(string(in.FactoryFAQ.Items)) != "null" {
				return "factory_faq.items 需要 [{q,a}] 数组。"
			}
			items := parseFactoryQAs(string(in.FactoryFAQ.Items))
			if len(rows) > 0 && len(items) == 0 {
				return "factory_faq.items 每条需要非空的 q 与 a（最多 " + strconv.Itoa(maxFactoryQA) + " 条）。"
			}
			if err := s.store.SetSetting(s.copyKey(factoryFAQSettingKey, lang), marshalThemeOptionJSON(items, len(items) == 0)); err != nil {
				return err.Error()
			}
		}
	}
	if in.DTCTestimonials != nil {
		// 独立站用户评价（红线：只能录入真实用户评价，绝不编造；格式校验管不了真实性，
		// 真实性约束写死在 SKILL / 自动化文档里）。
		var rows []map[string]any
		if err := json.Unmarshal(in.DTCTestimonials, &rows); err != nil && strings.TrimSpace(string(in.DTCTestimonials)) != "null" {
			return "dtc_testimonials 需要 [{name,region,quote}] 数组。"
		}
		items := parseDTCTestimonials(string(in.DTCTestimonials))
		if len(rows) > 0 && len(items) == 0 {
			return "dtc_testimonials 每条需要非空的 name 与 quote（region 可空，最多 " + strconv.Itoa(maxDTCTestimonials) + " 条），且只能录入真实用户评价。"
		}
		if err := s.store.SetSetting(s.copyKey(dtcTestimonialsSettingKey, lang), marshalThemeOptionJSON(items, len(items) == 0)); err != nil {
			return err.Error()
		}
	}
	return ""
}

// apiToggleFlag 区块开关 API 值 → settings 值（开=存空保持缺省语义，关=存 "0"）。
func apiToggleFlag(enabled bool) string {
	if enabled {
		return ""
	}
	return factoryProcessDisabledFlag
}

func (s *Server) applyAPISiteProfileInput(in *apiSiteProfileInput) string {
	if in == nil {
		return ""
	}
	lang := strings.TrimSpace(in.Lang)
	if lang == "" {
		lang = s.defaultLang()
	}
	if !s.langEnabled(lang) {
		return "语种未启用。"
	}
	if in.Name != nil && lang == s.defaultLang() && strings.TrimSpace(*in.Name) == "" {
		return "默认语种的站点名称不能为空。"
	}
	set := func(key string, value *string) error {
		if value == nil {
			return nil
		}
		return s.store.SetSetting(s.copyKey(key, lang), strings.TrimSpace(*value))
	}
	for _, item := range []struct {
		key   string
		value *string
	}{
		{"site.name", in.Name},
		{"site.tagline", in.Tagline},
		{"site.description", in.Description},
		{"site.keywords", in.Keywords},
		{"site.hero_eyebrow", in.HeroEyebrow},
		{"site.hero_title", in.HeroTitle},
		{"site.hero_description", in.HeroDescription},
		{"site.footer_note", in.FooterNote},
		{"home.featured_title", in.HomeFeaturedTitle},
		{"home.links_title", in.HomeLinksTitle},
		{"home.latest_title", in.HomeLatestTitle},
		{postDefaultAuthorKey, in.DefaultPostAuthor},
		{linkDefaultAuthorKey, in.DefaultLinkAuthor},
	} {
		if err := set(item.key, item.value); err != nil {
			return err.Error()
		}
	}
	for _, item := range []struct {
		key   string
		value *string
	}{
		{"site.logo", in.Logo},
		{"site.share_image", in.ShareImage},
		{"hero.image", in.HeroImage},
	} {
		if err := set(item.key, item.value); err != nil {
			return err.Error()
		}
	}
	if in.HeroVisual != nil {
		hv := strings.TrimSpace(*in.HeroVisual)
		if hv != "" && hv != "image" && hv != "svg" && hv != "anim1" && hv != "anim2" {
			return "Hero 右侧视觉类型无效。"
		}
		if err := s.store.SetSetting(s.copyKey("hero.visual", lang), hv); err != nil {
			return err.Error()
		}
	} else if in.HeroImage != nil {
		heroImage := strings.TrimSpace(*in.HeroImage)
		heroVisualKey := s.copyKey("hero.visual", lang)
		if heroImage != "" {
			if err := s.store.SetSetting(heroVisualKey, "image"); err != nil {
				return err.Error()
			}
		} else if s.store.Setting(heroVisualKey) == "image" {
			if err := s.store.SetSetting(heroVisualKey, ""); err != nil {
				return err.Error()
			}
		}
	}
	if in.Favicon != nil {
		if err := s.store.SetSetting("site.favicon", strings.TrimSpace(*in.Favicon)); err != nil {
			return err.Error()
		}
	}
	// 主题 options（工厂族三槽）：与其它站点文案同一写路径、同一语种规则。
	if errMsg := s.applyAPIFactoryOptions(in, lang); errMsg != "" {
		return errMsg
	}
	return ""
}

func (s *Server) apiNavigationResponse() map[string]any {
	rows, _, configured := s.effectiveMenuRows()
	items := make([]apiNavigationItem, 0, len(rows))
	for _, row := range rows {
		labels := map[string]string{}
		for k, v := range row.Labels {
			labels[k] = v
		}
		items = append(items, apiNavigationItem{URL: row.URL, Labels: labels})
	}
	langs := make([]string, 0, len(s.locales()))
	for _, loc := range s.locales() {
		langs = append(langs, loc.Code)
	}
	source := "configured"
	if !configured {
		source = "defaults"
	}
	return map[string]any{
		"default": s.defaultLang(), "languages": langs, "items": items,
		"configured": configured, "source": source,
	}
}

func (in apiCategoryAllEntryPatch) hasFields() bool {
	return in.Lang != nil || in.Title != nil || in.Description != nil || in.Label != nil || in.Slug != nil
}

// apiExtCategoryRouter 在 mux 之前拦截「扩展类型分类」与「扩展集合置顶」的 API 路径并
// 分发到对应处理器（admin v1 与平台镜像两个命名空间；platform 路径经 servePlatformAPI
// 鉴权后同样流经 siteHandler，所以一个包装器两边都覆盖）。posts/links 不在此拦截，仍走
// mux 字面路由。用包装器而非 {collection} 通配路由：PATCH /{collection}/categories/{id}、
// PATCH /{collection}/featured/{id} 与 /languages/{code}/catalog 这类「前段字面+后段通配」
// 模式在 ServeMux 里互不更具体，注册即 panic——与公开路由的 contentTypeRouter 同一处理思路
// （本仓库路由老坑）。
func (s *Server) apiExtCategoryRouter(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 扩展集合置顶：PATCH /{collection}/featured/{id}（商品等；posts/links 走 mux 字面路由）。
		if collection, id, ok := apiExtFeaturedPath(r.URL.Path); ok &&
			collection != "posts" && collection != "links" && r.Method == http.MethodPatch {
			r.SetPathValue("collection", collection)
			r.SetPathValue("id", id)
			s.apiUpdateContentFeatured(w, r)
			return
		}
		collection, rest, ok := apiExtCategoryPath(r.URL.Path)
		if !ok || collection == "posts" || collection == "links" {
			next.ServeHTTP(w, r)
			return
		}
		r.SetPathValue("collection", collection)
		switch {
		case rest == "" && r.Method == http.MethodGet:
			s.apiListCategories(w, r)
		case rest == "" && r.Method == http.MethodPost:
			s.apiCreateCategory(w, r)
		case rest == "all-entry" && r.Method == http.MethodGet:
			s.apiGetCategoryAllEntry(w, r)
		case rest == "all-entry" && r.Method == http.MethodPatch:
			s.apiUpdateCategoryAllEntry(w, r)
		case rest != "" && rest != "all-entry" && r.Method == http.MethodPatch:
			r.SetPathValue("id", rest)
			s.apiUpdateCategory(w, r)
		default:
			next.ServeHTTP(w, r) // 方法不符合分类端点 → 交回 mux（404/405 语义不变）
		}
	})
}

// apiExtCategoryPath 解析 /api/admin/v1/{collection}/categories[/{rest}] 与
// /api/platform/v1/sites/{siteID}/{collection}/categories[/{rest}]，返回集合名与尾段。
func apiExtCategoryPath(p string) (collection, rest string, ok bool) {
	tail := ""
	switch {
	case strings.HasPrefix(p, "/api/admin/v1/"):
		tail = strings.TrimPrefix(p, "/api/admin/v1/")
	case strings.HasPrefix(p, "/api/platform/v1/sites/"):
		tail = strings.TrimPrefix(p, "/api/platform/v1/sites/")
		i := strings.IndexByte(tail, '/')
		if i < 0 {
			return "", "", false
		}
		tail = tail[i+1:] // 去掉 {siteID} 段
	default:
		return "", "", false
	}
	segs := strings.Split(strings.Trim(tail, "/"), "/")
	if len(segs) < 2 || len(segs) > 3 || segs[0] == "" || segs[1] != "categories" {
		return "", "", false
	}
	if len(segs) == 3 {
		if segs[2] == "" {
			return "", "", false
		}
		return segs[0], segs[2], true
	}
	return segs[0], "", true
}

// apiExtFeaturedPath 解析 /api/admin/v1/{collection}/featured/{id} 与
// /api/platform/v1/sites/{siteID}/{collection}/featured/{id}，返回集合名与内容 ID 段。
func apiExtFeaturedPath(p string) (collection, id string, ok bool) {
	tail := ""
	switch {
	case strings.HasPrefix(p, "/api/admin/v1/"):
		tail = strings.TrimPrefix(p, "/api/admin/v1/")
	case strings.HasPrefix(p, "/api/platform/v1/sites/"):
		tail = strings.TrimPrefix(p, "/api/platform/v1/sites/")
		i := strings.IndexByte(tail, '/')
		if i < 0 {
			return "", "", false
		}
		tail = tail[i+1:] // 去掉 {siteID} 段
	default:
		return "", "", false
	}
	segs := strings.Split(strings.Trim(tail, "/"), "/")
	if len(segs) != 3 || segs[0] == "" || segs[1] != "featured" || segs[2] == "" {
		return "", "", false
	}
	return segs[0], segs[2], true
}

// apiCategoryReadTarget 解析分类端点的集合：posts/links 沿用专属 categories scope；
// 「支持分类」的扩展类型（含数据库自定义类型）复用该集合的 read scope。
// scope 口径（评审定）：扩展类型不新增 categories scope 种类（签发面不变）——
// 读走 {collection}:read、写走 {collection}:write，与该类型内容本身同权。
func (s *Server) apiCategoryReadTarget(collection string) (kind, scope string, ok bool) {
	switch collection {
	case "posts":
		return "post", apiScope(collection, "categories"), true
	case "links":
		return "link", apiScope(collection, "categories"), true
	}
	if ct := s.extTypeByPrefix(collection); ct != nil && ct.HasCategory {
		return ct.Key, apiScope(collection, "read"), true
	}
	return "", "", false
}

func (s *Server) apiCategoryWriteTarget(collection string) (kind, scope string, ok bool) {
	switch collection {
	case "posts":
		return "post", apiScopePostCategoriesWrite, true
	case "links":
		return "link", apiScopeLinkCategoriesWrite, true
	}
	if ct := s.extTypeByPrefix(collection); ct != nil && ct.HasCategory {
		return ct.Key, apiScope(collection, "write"), true
	}
	return "", "", false
}

func (s *Server) apiCategoryAllEntryItems(kind, lang string) ([]apiCategoryAllEntry, string) {
	if lang == "all" {
		locales := s.locales()
		items := make([]apiCategoryAllEntry, 0, len(locales))
		for _, loc := range locales {
			items = append(items, s.apiCategoryAllEntryItem(kind, loc.Code))
		}
		return items, ""
	}
	if !s.langEnabled(lang) {
		return nil, "语种未启用。"
	}
	return []apiCategoryAllEntry{s.apiCategoryAllEntryItem(kind, lang)}, ""
}

func (s *Server) apiCategoryAllEntryItem(kind, lang string) apiCategoryAllEntry {
	if kind != "post" && kind != "link" {
		return s.apiExtCategoryAllEntryItem(kind, lang)
	}
	cfg := s.archiveConfig(lang, kind)
	return apiCategoryAllEntry{
		Kind:        kind,
		Lang:        lang,
		Title:       cfg.Title,
		Description: cfg.Description,
		Label:       cfg.Label,
		Slug:        cfg.Slug,
		Path:        cfg.Path,
		Purpose:     categoryAllEntryPurpose(kind),
		Selectable:  false,
	}
}

// apiExtCategoryAllEntryItem 扩展类型的归档入口：映射到 ext_archive_meta（后台
// 「扩展 → 归档页文案」同一份数据）；路径由类型定义固定，无自定义 slug / label。
func (s *Server) apiExtCategoryAllEntryItem(kind, lang string) apiCategoryAllEntry {
	name, prefix := kind, kind
	if ct := s.lookupType(kind); ct != nil {
		name = ct.Name(lang)
		prefix = strings.Trim(ct.URLPrefix, "/")
	}
	title, intro := s.extArchiveText(kind, lang)
	if title == "" {
		title = name
	}
	return apiCategoryAllEntry{
		Kind:        kind,
		Lang:        lang,
		Title:       title,
		Description: intro,
		Slug:        prefix,
		Path:        "/" + prefix,
		Purpose:     "扩展类型归档入口，控制该类型列表页的标题与简介（对应后台「扩展 → 归档页文案」）；路径由类型定义固定，不支持自定义 slug 或“全部”按钮文字，也不是可写入 category_id 的真实分类。",
		Selectable:  false,
	}
}

func categoryAllEntryPurpose(kind string) string {
	if kind == "link" {
		return "链接总入口，控制全部链接列表页的标题、描述、路径和“全部”筛选按钮；它不是可写入 category_id 的真实链接分类。"
	}
	return "文章总入口，控制全部文章列表页的标题、描述、路径和“全部”筛选按钮；它不是可写入 category_id 的真实文章分类。"
}

func (s *Server) applyAPICategoryAllEntryInput(kind, lang string, in *apiCategoryAllEntryInput) (apiCategoryAllEntry, string, error) {
	if kind != "post" && kind != "link" {
		return s.applyExtCategoryAllEntryInput(kind, lang, in)
	}
	current := s.archiveConfig(lang, kind)
	title := current.Title
	if in.Title != nil {
		title = strings.TrimSpace(*in.Title)
	}
	desc := current.Description
	if in.Description != nil {
		desc = strings.TrimSpace(*in.Description)
	}
	label := current.Label
	if in.Label != nil {
		label = strings.TrimSpace(*in.Label)
	}
	slug := current.Slug
	if in.Slug != nil {
		slug = strings.TrimSpace(*in.Slug)
	}
	if _, errMsg, err := s.saveCategoryAllEntry(kind, lang, title, label, desc, slug); err != nil || errMsg != "" {
		return apiCategoryAllEntry{}, errMsg, err
	}
	return s.apiCategoryAllEntryItem(kind, lang), "", nil
}

// applyExtCategoryAllEntryInput 扩展类型归档入口的写路径：title/description 写进
// ext_archive_meta（与后台弹窗同一存储，两处操作同一数据）；slug/label 不适用——
// 显式给了就 400 报清楚，而不是默默丢弃。
func (s *Server) applyExtCategoryAllEntryInput(kind, lang string, in *apiCategoryAllEntryInput) (apiCategoryAllEntry, string, error) {
	if in.Slug != nil || in.Label != nil {
		return apiCategoryAllEntry{}, "扩展类型归档入口不支持自定义 slug 或 label（路径由类型定义固定）。", nil
	}
	all := s.extArchiveMetaAll()
	entry, ok := all[kind]
	if !ok || entry.Title == nil {
		entry.Title = map[string]string{}
	}
	if entry.Intro == nil {
		entry.Intro = map[string]string{}
	}
	if in.Title != nil {
		if t := strings.TrimSpace(*in.Title); t != "" {
			entry.Title[lang] = t
		} else {
			delete(entry.Title, lang)
		}
	}
	if in.Description != nil {
		if d := strings.TrimSpace(*in.Description); d != "" {
			entry.Intro[lang] = d
		} else {
			delete(entry.Intro, lang)
		}
	}
	if len(entry.Title) == 0 && len(entry.Intro) == 0 {
		delete(all, kind)
	} else {
		all[kind] = entry
	}
	b, err := json.Marshal(all)
	if err != nil {
		return apiCategoryAllEntry{}, "", err
	}
	if err := s.store.SetSetting(extArchiveMetaKey, string(b)); err != nil {
		return apiCategoryAllEntry{}, "", err
	}
	s.clearGeneratedCaches()
	return s.apiCategoryAllEntryItem(kind, lang), "", nil
}

func (s *Server) uniqueAPICategorySlug(lang, slug string, exceptID int64) string {
	base := slug
	for n := 2; ; n++ {
		exists, err := s.store.CategorySlugExists(lang, slug, exceptID)
		if err != nil || !exists {
			return slug
		}
		slug = base + "-" + strconv.Itoa(n)
	}
}

func (s *Server) frontendPreviewURL(r *http.Request, collection string, p *store.Post, expires time.Time) (string, time.Time, error) {
	token, err := s.signFrontendPreviewToken(frontendPreviewClaims{
		Collection: collection,
		ID:         p.ID,
		Expires:    expires.Unix(),
		Updated:    previewUpdatedUnix(p),
		Revision:   previewRevision(p),
	})
	if err != nil {
		return "", time.Time{}, err
	}
	base, sitePrefixed := s.frontendPreviewBase(r)
	path := fmt.Sprintf("/preview/%s/%d?token=%s", collection, p.ID, url.QueryEscape(token))
	if sitePrefixed {
		path = fmt.Sprintf("/preview/sites/%d/%s/%d?token=%s", s.platformSiteID, collection, p.ID, url.QueryEscape(token))
	}
	return absWithBase(base, path), expires, nil
}

// frontendPreviewBase 决定前台预览链接落在哪个主机上。预览页必须由 Go 服务端动态渲染
// （未发布内容在任何静态导出里都不存在），所以主机必须「能到达本服务、且能按 Host 路由到当前站点」：
//   - 站点公开域名本来就由 Go 直接服务（单站部署，或平台域名表指向本站点）→ 维持现状；
//   - 公开域名指向 Cloudflare 静态导出（那里没有 /preview 路由）、或压根路由不到本站点 →
//     回退到本次 API 请求所到达的主机（请求能进来即证明可达）；
//   - 回退主机也路由不到本站点时（平台子站经平台主机调用是常态），第二返回值为 true，
//     调用方改用 /preview/sites/{siteID}/… 前缀，由平台入口按站点 ID 分发（serveSignedSitePreview）。
//
// 预览链接是短期签名链接，可达性优先于域名好看。
func (s *Server) frontendPreviewBase(r *http.Request) (string, bool) {
	base := s.publicBaseURL(r)
	if s.frontendPreviewHostServesSite(baseURLHost(base)) {
		return base, false
	}
	reqHost := requestHost(r)
	if reqHost == "" {
		// 拿不到请求主机（理论上不会发生）：保底用站点前缀形式，至少平台入口可达。
		return base, s.platformRuntimePool() != nil
	}
	reqBase := requestScheme(r) + "://" + reqHost
	if s.frontendPreviewHostServesSite(reqHost) {
		return reqBase, false
	}
	if s.platformRuntimePool() != nil {
		return reqBase, true
	}
	// 单站部署且请求主机也被 CF 占用：没有更好的候选，保持请求主机（尽力而为）。
	return reqBase, false
}

// frontendPreviewHostServesSite 判断 host 是否由本 Go 服务直接服务且路由到当前站点：
// Cloudflare 静态导出已发布时，其公开域名的 DNS 指向 CF 静态文件，视为不可达。
func (s *Server) frontendPreviewHostServesSite(host string) bool {
	host = normalizeRuntimeHost(host)
	if host == "" {
		return false
	}
	if s.cloudflareStaticServesHost(host) {
		return false
	}
	pool := s.platformRuntimePool()
	if pool == nil {
		// 单站部署：所有到达 Go 的主机都由本站点应答。
		return true
	}
	rt, ok := pool.runtimeByHost(host)
	return ok && rt != nil && rt.Site != nil && rt.Site.ID == s.platformSiteID
}

func previewUpdatedUnix(p *store.Post) int64 {
	if p == nil || p.UpdatedAt.IsZero() {
		return 0
	}
	return p.UpdatedAt.UTC().Unix()
}

func previewRevision(p *store.Post) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	write := func(s string) {
		b.WriteString(s)
		b.WriteByte('\x00')
	}
	write(p.Type)
	write(strconv.FormatInt(p.ID, 10))
	write(p.Lang)
	write(p.Slug)
	write(p.Title)
	write(p.Excerpt)
	write(p.Content)
	write(p.MetaDesc)
	write(p.Keywords)
	write(p.CoverImage)
	write(p.Author)
	write(p.Status)
	write(p.EditorMode)
	write(p.LinkURL)
	write(p.TransGroup)
	write(p.RobotsOverride)
	write(p.CanonicalOverride)
	if p.CategoryID.Valid {
		write(strconv.FormatInt(p.CategoryID.Int64, 10))
	}
	write(p.PublishedAt.UTC().Format(time.RFC3339Nano))
	write(p.UpdatedAt.UTC().Format(time.RFC3339Nano))
	sum := sha256.Sum256([]byte(b.String()))
	return fmt.Sprintf("%x", sum[:8])
}

func (s *Server) previewSigningSecret() ([]byte, error) {
	secret := strings.TrimSpace(s.store.Setting(frontendPreviewSecretSetting))
	if secret == "" {
		secret = randToken()
		if err := s.store.SetSetting(frontendPreviewSecretSetting, secret); err != nil {
			return nil, err
		}
	}
	return []byte(secret), nil
}

func (s *Server) signFrontendPreviewToken(claims frontendPreviewClaims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	sig, err := s.frontendPreviewSignature(encodedPayload)
	if err != nil {
		return "", err
	}
	return encodedPayload + "." + sig, nil
}

func (s *Server) frontendPreviewSignature(encodedPayload string) (string, error) {
	secret, err := s.previewSigningSecret()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(encodedPayload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Server) verifyFrontendPreviewToken(token string) (frontendPreviewClaims, string) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return frontendPreviewClaims{}, "invalid"
	}
	want, err := s.frontendPreviewSignature(parts[0])
	if err != nil || !hmac.Equal([]byte(want), []byte(parts[1])) {
		return frontendPreviewClaims{}, "invalid"
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return frontendPreviewClaims{}, "invalid"
	}
	var claims frontendPreviewClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return frontendPreviewClaims{}, "invalid"
	}
	if claims.Expires <= 0 || time.Now().After(time.Unix(claims.Expires, 0)) {
		return frontendPreviewClaims{}, "expired"
	}
	if claims.ID <= 0 || (claims.Collection != "posts" && claims.Collection != "links") {
		return frontendPreviewClaims{}, "invalid"
	}
	return claims, ""
}

func (s *Server) signSitePreviewToken(siteID int64, expires time.Time) (string, error) {
	if siteID <= 0 || expires.IsZero() {
		return "", fmt.Errorf("站点预览参数无效")
	}
	payload, err := json.Marshal(sitePreviewClaims{
		Kind:    "site",
		SiteID:  siteID,
		Expires: expires.Unix(),
		Nonce:   randToken()[:24],
	})
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	sig, err := s.sitePreviewSignature(encodedPayload)
	if err != nil {
		return "", err
	}
	return encodedPayload + "." + sig, nil
}

func (s *Server) sitePreviewSignature(encodedPayload string) (string, error) {
	secret, err := s.previewSigningSecret()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	// 与单篇内容预览使用同一份站点私钥，但采用独立的签名域，避免两类
	// token 即使载荷碰巧相似也能互相复用。
	_, _ = mac.Write([]byte("site-preview\x00"))
	_, _ = mac.Write([]byte(encodedPayload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Server) verifySitePreviewToken(token string, expectedSiteID int64) (sitePreviewClaims, string) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return sitePreviewClaims{}, "invalid"
	}
	want, err := s.sitePreviewSignature(parts[0])
	if err != nil || !hmac.Equal([]byte(want), []byte(parts[1])) {
		return sitePreviewClaims{}, "invalid"
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return sitePreviewClaims{}, "invalid"
	}
	var claims sitePreviewClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return sitePreviewClaims{}, "invalid"
	}
	if claims.Expires <= 0 || time.Now().After(time.Unix(claims.Expires, 0)) {
		return sitePreviewClaims{}, "expired"
	}
	if claims.Kind != "site" || claims.SiteID <= 0 || claims.SiteID != expectedSiteID || strings.TrimSpace(claims.Nonce) == "" {
		return sitePreviewClaims{}, "invalid"
	}
	return claims, ""
}

func (s *Server) applyAPIContentInput(p *store.Post, in *apiContentInput, creating bool) (bool, string) {
	if in.Type != nil && strings.TrimSpace(*in.Type) != "" && strings.TrimSpace(*in.Type) != p.Type {
		return false, "不能通过该接口修改内容类型。"
	}
	if in.Title != nil {
		p.Title = strings.TrimSpace(*in.Title)
	}
	if in.Excerpt != nil {
		p.Excerpt = strings.TrimSpace(*in.Excerpt)
	}
	if in.Content != nil {
		p.Content = *in.Content
	}
	if in.MetaDesc != nil {
		p.MetaDesc = strings.TrimSpace(*in.MetaDesc)
	}
	if in.Keywords != nil {
		p.Keywords = strings.TrimSpace(*in.Keywords)
	}
	if in.CoverImage != nil {
		p.CoverImage = strings.TrimSpace(*in.CoverImage)
	}
	if in.Author != nil {
		p.Author = strings.TrimSpace(*in.Author)
	}
	if in.EditorMode != nil {
		switch strings.TrimSpace(*in.EditorMode) {
		case "", "markdown":
			p.EditorMode = "markdown"
		case "rich":
			p.EditorMode = "rich"
		default:
			return false, "editor_mode 只能是 markdown 或 rich。"
		}
	}
	if in.LinkURL != nil {
		p.LinkURL = strings.TrimSpace(*in.LinkURL)
	}
	if in.TransGroup != nil && creating {
		p.TransGroup = strings.TrimSpace(*in.TransGroup)
	}
	if in.RobotsOverride != nil {
		p.RobotsOverride = strings.TrimSpace(*in.RobotsOverride)
	}
	if in.CanonicalOverride != nil {
		p.CanonicalOverride = strings.TrimSpace(*in.CanonicalOverride)
	}
	if in.Slug != nil {
		p.Slug = slugify(strings.TrimSpace(*in.Slug))
	}
	if p.Slug == "" && creating {
		p.Slug = slugify(p.Title)
	}
	if p.Slug == "" && creating {
		p.Slug = p.Type + "-" + strconv.FormatInt(time.Now().Unix(), 36)
	}
	if in.CategoryID != nil {
		if p.Type == "page" {
			return false, "页面不支持 category_id。"
		}
		if *in.CategoryID > 0 {
			p.CategoryID = sql.NullInt64{Int64: *in.CategoryID, Valid: true}
		} else {
			p.CategoryID = sql.NullInt64{}
		}
	}
	publishNeeded := false
	if in.Status != nil {
		status := strings.TrimSpace(*in.Status)
		switch status {
		case "", "draft":
			p.Status = "draft"
		case "published", "scheduled":
			p.Status = status
			publishNeeded = true
		default:
			return false, "status 只能是 draft、published 或 scheduled。"
		}
	}
	if in.PublishedAt != nil {
		t, err := parseAPITime(strings.TrimSpace(*in.PublishedAt))
		if err != nil {
			return false, "published_at 需要使用 RFC3339 或 2006-01-02T15:04 格式。"
		}
		p.PublishedAt = t
	}
	if p.Status == "scheduled" {
		publishNeeded = true
		if p.PublishedAt.IsZero() {
			return false, "定时发布需要 published_at。"
		}
	}
	if p.Status == "published" {
		publishNeeded = true
	}
	if p.Type == "link" && p.LinkURL == "" && p.Status != "draft" {
		return false, "发布链接时 link_url 不能为空。"
	}
	if p.Title == "" {
		return false, "标题不能为空。"
	}
	return publishNeeded, ""
}

// validateCanonicalOverride 校验 canonical_override：允许空串（清除覆盖），
// 否则必须是 http(s) 绝对 URL（带 host），不合法由调用方回 422。
func validateCanonicalOverride(v *string) string {
	if v == nil {
		return ""
	}
	raw := strings.TrimSpace(*v)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "canonical_override 必须是以 http:// 或 https:// 开头的合法绝对 URL。"
	}
	return ""
}

func (s *Server) validateAPICategory(p *store.Post) string {
	if !p.CategoryID.Valid {
		return ""
	}
	cat, err := s.store.GetCategoryByID(p.CategoryID.Int64)
	if err != nil {
		return err.Error()
	}
	if cat == nil {
		return "分类不存在。"
	}
	want := strings.TrimSpace(p.Type)
	if want == "page" || want == "" {
		return "该内容类型不能设置分类。"
	}
	if cat.Kind != want || cat.Lang != p.Lang {
		return "分类语种或类型与内容不匹配。"
	}
	return ""
}

func (s *Server) apiContentItem(p *store.Post, includeContent bool) apiContentItem {
	author := strings.TrimSpace(p.Author)
	if author == "" && (p.Type == "post" || p.Type == "link") {
		author = s.defaultContentAuthor(p.Type, p.Lang)
	}
	var categoryID *int64
	if p.CategoryID.Valid {
		v := p.CategoryID.Int64
		categoryID = &v
	}
	var cat *apiCategory
	if p.Category != nil {
		v := apiCategoryItem(p.Category)
		cat = &v
	}
	item := apiContentItem{
		ID: p.ID, Type: p.Type, Lang: p.Lang, Slug: p.Slug, Title: p.Title, Excerpt: p.Excerpt,
		MetaDesc: p.MetaDesc, Keywords: p.Keywords, CoverImage: p.CoverImage, Author: author,
		Status: p.Status, Featured: p.Featured, EditorMode: p.EditorMode, LinkURL: p.LinkURL,
		TransGroup: p.TransGroup, CategoryID: categoryID, Category: cat, URL: s.apiContentURL(p),
		PublishedAt: apiTime(p.PublishedAt), CreatedAt: apiTime(p.CreatedAt), UpdatedAt: apiTime(p.UpdatedAt),
		RobotsOverride: p.RobotsOverride, CanonicalOverride: p.CanonicalOverride,
		Discarded: p.Discarded(), DiscardReason: p.DiscardReason, DiscardedAt: apiTime(p.DiscardedAt),
	}
	if includeContent {
		item.Content = p.Content
	}
	if f := s.extraToAPIMap(p.Type, p.Extra); len(f) > 0 {
		item.Fields = f
	}
	return item
}

func apiCategoryItem(c *store.Category) apiCategory {
	if c == nil {
		return apiCategory{}
	}
	return apiCategory{
		ID: c.ID, Slug: c.Slug, Name: c.Name, Description: c.Description,
		Lang: c.Lang, TransGroup: c.TransGroup, Kind: c.Kind, Count: c.Count,
	}
}

func apiHeadings(toc []Heading) []apiHeading {
	if len(toc) == 0 {
		return nil
	}
	out := make([]apiHeading, 0, len(toc))
	for _, h := range toc {
		out = append(out, apiHeading{Level: h.Level, ID: h.ID, Text: h.Text})
	}
	return out
}

func (s *Server) apiContentURL(p *store.Post) string {
	return "/" + p.Lang + publicContentPath(p.Type, p.Slug)
}

// apiContentKind 把集合名映射到内置内容类型 kind（代码层，无站点上下文）。
func apiContentKind(collection string) (string, bool) {
	switch collection {
	case "posts":
		return "post", true
	case "pages":
		return "page", true
	case "links":
		return "link", true
	}
	return "", false
}

// apiContentKind 方法在内置类型之外，还识别本站数据库里的自定义类型（API 内容端点用）。
// 集合名即其 URL 前缀（自定义类型前缀恒等于 key）。
func (s *Server) apiContentKind(collection string) (string, bool) {
	if kind, ok := apiContentKind(collection); ok {
		return kind, true
	}
	if ct := s.extTypeByPrefix(collection); ct != nil {
		return ct.Key, true
	}
	return "", false
}

func apiKindName(kind string) string {
	switch kind {
	case "page":
		return "页面"
	case "link":
		return "链接"
	default:
		return "文章"
	}
}

// apiKindLabel 审计日志里的类型名：内置三种沿用 apiKindName，扩展类型用注册表名称。
func (s *Server) apiKindLabel(kind string) string {
	switch kind {
	case "post", "page", "link":
		return apiKindName(kind)
	}
	if ct := s.lookupType(kind); ct != nil {
		return ct.Name("zh")
	}
	return kind
}

func apiScopeMap(scopes string) map[string]bool {
	out := map[string]bool{}
	for _, s := range strings.Split(scopes, ",") {
		if s = strings.TrimSpace(s); s != "" {
			if s == retiredAPIScopeSecurityWrite {
				continue
			}
			out[s] = true
		}
	}
	return out
}

func apiScope(collection, action string) string {
	return collection + ":" + action
}

func automationScopeActions(resource string) []string {
	switch resource {
	case "posts", "links":
		return []string{"read", "categories", "categories:write", "write", "publish", "pin"}
	default:
		return []string{"read", "write", "publish"}
	}
}

func automationScopeAllowed(scopes map[string]bool, scope string) bool {
	if scopes[scope] {
		return true
	}
	parts := strings.Split(scope, ":")
	return len(parts) == 2 && scopes["content:"+parts[1]]
}

func apiTokenFromRequest(r *http.Request) string {
	if token := strings.TrimSpace(r.Header.Get("X-GCMS-API-Key")); token != "" {
		return token
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) >= 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

func decodeAPIJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		apiError(w, http.StatusBadRequest, "bad_json", "JSON 请求体无效："+err.Error())
		return false
	}
	return true
}

func apiError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": code, "message": message})
}

func apiIntParam(r *http.Request, key string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(key)))
	if err != nil {
		return def
	}
	if n < 0 {
		return def
	}
	return n
}

func parseAPIBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parseAPITime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02T15:04", s, time.Local)
}

func apiTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
