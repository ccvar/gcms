package web

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const pageDesignContextContractVersion = "1"

// pageDesignContextResponse is the single read contract Pilot uses before it
// designs a page. It intentionally contains live site values and registries,
// while the recommended manifest keeps theme tokens empty so generated pages
// continue to follow the site's current theme instead of copying a stale
// visual snapshot into every revision.
type pageDesignContextResponse struct {
	ContractVersion string                            `json:"contract_version"`
	ContextHash     string                            `json:"context_hash"`
	Lang            string                            `json:"lang"`
	Site            pageDesignSiteContext             `json:"site"`
	Theme           pageDesignThemeContext            `json:"theme"`
	Navigation      []pageDesignNavigationItem        `json:"navigation"`
	Locales         []pageDesignLocale                `json:"locales"`
	Components      []CompositionComponentDefinition  `json:"components"`
	DataSources     []compositionDataSourceDescriptor `json:"data_sources"`
	Recipes         []pageDesignRecipe                `json:"recipes"`
	ManifestDefault pageDesignManifestDefault         `json:"manifest_default"`
	Workflow        pageDesignWorkflowPolicy          `json:"workflow"`
	Quality         pageDesignQualityPolicy           `json:"quality"`
}

type pageDesignSiteContext struct {
	Name        string                `json:"name"`
	Tagline     string                `json:"tagline"`
	Description string                `json:"description"`
	BaseURL     string                `json:"base_url"`
	Prefix      string                `json:"prefix"`
	Author      string                `json:"author"`
	Brand       string                `json:"brand"`
	Logo        string                `json:"logo"`
	ShareImage  string                `json:"share_image,omitempty"`
	Hero        pageDesignHeroContext `json:"hero"`
	FooterNote  string                `json:"footer_note"`
}

type pageDesignHeroContext struct {
	Eyebrow      string `json:"eyebrow"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Visual       string `json:"visual"`
	Image        string `json:"image,omitempty"`
	HasInlineSVG bool   `json:"has_inline_svg"`
}

type pageDesignThemeContext struct {
	ID             string                       `json:"id"`
	Name           string                       `json:"name"`
	Description    string                       `json:"description"`
	Category       string                       `json:"category"`
	Family         string                       `json:"family"`
	Layout         string                       `json:"layout"`
	ContentFamily  string                       `json:"content_family,omitempty"`
	AppearanceHint string                       `json:"appearance_hint"`
	Customized     bool                         `json:"customized"`
	ResolvedHints  pageDesignThemeResolvedHints `json:"resolved_hints"`
	Options        map[string]any               `json:"options"`
	Inheritance    pageDesignThemeInheritance   `json:"inheritance"`
}

type pageDesignThemeResolvedHints struct {
	Background     string `json:"background"`
	Accent         string `json:"accent"`
	RadiusPX       int    `json:"radius_px"`
	ContentWidthPX int    `json:"content_width_px"`
}

type pageDesignThemeInheritance struct {
	Recommended       bool `json:"recommended"`
	CopyTokens        bool `json:"copy_tokens"`
	FollowsSiteTheme  bool `json:"follows_site_theme"`
	FollowsSiteTweaks bool `json:"follows_site_tweaks"`
}

type pageDesignNavigationItem struct {
	Href     string `json:"href"`
	Label    string `json:"label"`
	External bool   `json:"external"`
	Index    int    `json:"index"`
}

type pageDesignLocale struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Tag     string `json:"tag"`
	Default bool   `json:"default"`
}

type pageDesignRecipe struct {
	ID                  string   `json:"id"`
	Title               string   `json:"title"`
	Purpose             string   `json:"purpose"`
	RecommendedSections []string `json:"recommended_sections"`
	DataRule            string   `json:"data_rule"`
}

type pageDesignManifestDefault struct {
	SchemaVersion int               `json:"schema_version"`
	Mode          string            `json:"mode"`
	Shell         CompositionShell  `json:"shell"`
	Theme         CompositionTheme  `json:"theme"`
	Layout        CompositionLayout `json:"layout"`
}

type pageDesignWorkflowPolicy struct {
	PrimarySurface           string   `json:"primary_surface"`
	DefaultStatus            string   `json:"default_status"`
	ImmutableRevisions       bool     `json:"immutable_revisions"`
	RequireLatestETag        bool     `json:"require_latest_etag"`
	PublishNeedsUserApproval bool     `json:"publish_needs_user_approval"`
	Sequence                 []string `json:"sequence"`
}

type pageDesignViewport struct {
	ID       string `json:"id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Required bool   `json:"required"`
}

type pageDesignQualityPolicy struct {
	PreviewViewports []pageDesignViewport `json:"preview_viewports"`
	ServerChecks     []string             `json:"server_checks"`
	PilotChecks      []string             `json:"pilot_checks"`
	PublishBlockers  []string             `json:"publish_blockers"`
}

func (s *Server) apiPageDesignContext(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsRead); !ok {
		return
	}
	lang := strings.TrimSpace(r.URL.Query().Get("lang"))
	if lang == "" {
		lang = s.defaultLang()
	}
	if !s.langEnabled(lang) {
		apiError(w, http.StatusUnprocessableEntity, "language_invalid", "请求语种未在当前站点启用。")
		return
	}
	response := s.pageDesignContext(r, lang)
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) pageDesignContext(r *http.Request, lang string) pageDesignContextResponse {
	view := s.viewForLang(r, lang, "")
	theme := view.Theme
	themeOption := ThemeOption{ID: theme, Name: theme, Category: ThemeCategoryContent}
	for _, candidate := range Themes {
		if candidate.ID == theme {
			themeOption = candidate
			break
		}
	}
	customized, accent, radius := s.themeTweak(theme)
	radiusPX, _ := strconv.Atoi(radius)
	contentWidthPX, _ := strconv.Atoi(normalizeLayoutWidth(s.store.Setting(layoutWidthKey)))
	if contentWidthPX == 0 {
		contentWidthPX = 1080
	}

	navigation := make([]pageDesignNavigationItem, 0, len(view.Menu))
	for _, item := range view.Menu {
		navigation = append(navigation, pageDesignNavigationItem{
			Href: item.Href, Label: item.Label, External: item.External, Index: item.Index,
		})
	}
	locales := make([]pageDesignLocale, 0, len(s.locales()))
	for index, locale := range s.locales() {
		locales = append(locales, pageDesignLocale{
			Code: locale.Code, Name: locale.Name, Tag: locale.Tag, Default: index == 0,
		})
	}
	_, dataSources := s.compositionDataSourceCatalog(lang)

	response := pageDesignContextResponse{
		ContractVersion: pageDesignContextContractVersion,
		Lang:            lang,
		Site: pageDesignSiteContext{
			Name: view.Site.Name, Tagline: view.Site.Tagline, Description: view.Site.Description,
			BaseURL: view.Site.BaseURL, Prefix: view.Site.Prefix, Author: view.Site.Author,
			Brand: view.Site.Brand, Logo: view.Site.Logo, ShareImage: view.Site.ShareImage,
			Hero: pageDesignHeroContext{
				Eyebrow: view.Site.HeroEyebrow, Title: view.Site.HeroTitle,
				Description: view.Site.HeroDescription, Visual: view.Site.HeroVisual,
				Image: view.Site.HeroImage, HasInlineSVG: strings.TrimSpace(view.Site.HeroSVG) != "",
			},
			FooterNote: view.Site.FooterNote,
		},
		Theme: pageDesignThemeContext{
			ID: theme, Name: themeOption.Name, Description: themeOption.Desc,
			Category: themeOption.Category, Family: familyForTheme(theme),
			Layout: layoutForTheme(theme), ContentFamily: contentThemeFamily(theme),
			AppearanceHint: pageDesignAppearanceHint(themeBg(theme)), Customized: customized,
			ResolvedHints: pageDesignThemeResolvedHints{
				Background: themeBg(theme), Accent: accent, RadiusPX: radiusPX,
				ContentWidthPX: contentWidthPX,
			},
			Options: s.apiThemeOptionsResponse(lang),
			Inheritance: pageDesignThemeInheritance{
				Recommended: true, CopyTokens: false,
				FollowsSiteTheme: true, FollowsSiteTweaks: true,
			},
		},
		Navigation:  navigation,
		Locales:     locales,
		Components:  CompositionComponentRegistry(),
		DataSources: dataSources,
		Recipes:     pageDesignRecipes(lang),
		ManifestDefault: pageDesignManifestDefault{
			SchemaVersion: CompositionManifestVersion,
			Mode:          "composition",
			Shell:         CompositionShell{Mode: "site", StickyHeader: true},
			Theme:         CompositionTheme{Inherit: true, Tokens: map[string]string{}},
			Layout:        CompositionLayout{ContentMaxWidth: "wide", SectionGap: "comfortable"},
		},
		Workflow: pageDesignWorkflowPolicy{
			PrimarySurface: "pilot", DefaultStatus: "draft", ImmutableRevisions: true,
			RequireLatestETag: true, PublishNeedsUserApproval: true,
			Sequence: []string{
				"read_context", "create_draft", "create_revision", "validate",
				"build", "preview", "iterate", "publish_plan", "explicit_publish",
			},
		},
		Quality: pageDesignQualityPolicy{
			PreviewViewports: []pageDesignViewport{
				{ID: "desktop", Width: 1280, Height: 900, Required: true},
				{ID: "tablet", Width: 768, Height: 1024, Required: true},
				{ID: "mobile", Width: 390, Height: 844, Required: true},
			},
			ServerChecks: []string{
				"manifest_schema", "safe_urls", "asset_integrity",
				"binding_schema", "accessibility_contract",
			},
			PilotChecks: []string{
				"no_horizontal_overflow", "readable_mobile_type", "spacing_rhythm",
				"contrast", "image_crop_and_alt", "theme_consistency",
			},
			PublishBlockers: []string{
				"server_validation_error", "build_error", "missing_required_preview",
				"horizontal_overflow", "unreadable_or_clipped_content",
			},
		},
	}
	response.ContextHash = pageDesignContextHash(response)
	return response
}

func pageDesignContextHash(response pageDesignContextResponse) string {
	response.ContextHash = ""
	raw, err := json.Marshal(response)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:])
}

func pageDesignAppearanceHint(background string) string {
	value := strings.TrimPrefix(strings.TrimSpace(background), "#")
	if len(value) == 3 {
		value = strings.Repeat(value[0:1], 2) +
			strings.Repeat(value[1:2], 2) +
			strings.Repeat(value[2:3], 2)
	}
	if len(value) != 6 {
		return "mixed"
	}
	raw, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return "mixed"
	}
	red := float64((raw >> 16) & 0xff)
	green := float64((raw >> 8) & 0xff)
	blue := float64(raw & 0xff)
	if (0.2126*red + 0.7152*green + 0.0722*blue) < 128 {
		return "dark"
	}
	return "light"
}

func pageDesignRecipes(lang string) []pageDesignRecipe {
	if lang == "zh" {
		return []pageDesignRecipe{
			{
				ID: "marketing_landing", Title: "产品或服务宣传页",
				Purpose: "建立清晰价值主张、信任信息与行动入口。",
				RecommendedSections: []string{
					"hero.split", "features.grid", "content.cards", "faq.accordion", "cta.banner",
				},
				DataRule: "产品、案例或文章必须绑定当前站点真实数据源；无数据时使用明确空状态。",
			},
			{
				ID: "campaign_story", Title: "活动或专题页",
				Purpose: "围绕单一主题组织叙事、视觉素材、相关内容与行动入口。",
				RecommendedSections: []string{
					"hero.centered", "text.rich", "media.image", "posts.grid", "cta.banner",
				},
				DataRule: "相关内容使用实时绑定，活动专属文案保存在区块属性中。",
			},
			{
				ID: "knowledge_hub", Title: "知识与内容聚合页",
				Purpose: "帮助访问者快速理解主题并进入真实内容。",
				RecommendedSections: []string{
					"hero.centered", "posts.grid", "faq.accordion", "cta.banner",
				},
				DataRule: "文章列表必须从已发布内容读取，并设置真实的缺省提示。",
			},
			{
				ID: "lead_generation", Title: "咨询转化页",
				Purpose: "解释能力、降低疑虑并收集合规咨询。",
				RecommendedSections: []string{
					"hero.split", "features.grid", "faq.accordion", "form.contact",
				},
				DataRule: "不得伪造客户、数字或评价；表单只使用受控字段。",
			},
		}
	}
	return []pageDesignRecipe{
		{
			ID: "marketing_landing", Title: "Product or service landing page",
			Purpose: "Present a clear value proposition, evidence and next action.",
			RecommendedSections: []string{
				"hero.split", "features.grid", "content.cards", "faq.accordion", "cta.banner",
			},
			DataRule: "Bind products, proof and articles to real site sources; use an explicit empty state when no data exists.",
		},
		{
			ID: "campaign_story", Title: "Campaign or editorial feature",
			Purpose: "Build a focused narrative with media, related content and a next action.",
			RecommendedSections: []string{
				"hero.centered", "text.rich", "media.image", "posts.grid", "cta.banner",
			},
			DataRule: "Use live bindings for related content and keep campaign-specific copy in component properties.",
		},
		{
			ID: "knowledge_hub", Title: "Knowledge and content hub",
			Purpose: "Help visitors understand a topic and reach real published content.",
			RecommendedSections: []string{
				"hero.centered", "posts.grid", "faq.accordion", "cta.banner",
			},
			DataRule: "Read article lists from published content and provide a truthful empty state.",
		},
		{
			ID: "lead_generation", Title: "Lead generation page",
			Purpose: "Explain the offer, answer objections and collect a consented enquiry.",
			RecommendedSections: []string{
				"hero.split", "features.grid", "faq.accordion", "form.contact",
			},
			DataRule: "Never invent customers, metrics or testimonials; use only controlled form fields.",
		},
	}
}
