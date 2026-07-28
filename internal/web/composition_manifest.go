package web

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"cms.ccvar.com/internal/store"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// CompositionManifestVersion is the only composition protocol version this
// runtime accepts. A future protocol must add an explicit upgrader rather than
// silently interpreting unknown fields.
const CompositionManifestVersion = 1

// CompositionLimits is the single source of truth for the first composition
// runtime. API capability discovery may expose these values, but clients must
// not be trusted to enforce them.
var CompositionLimits = struct {
	MaxManifestBytes int
	MaxSections      int
	MaxDepth         int
	MaxChildren      int
	MaxBindings      int
	MaxBindingLimit  int
	MaxFeatureItems  int
	MaxFAQItems      int
	MaxTextRunes     int
}{
	MaxManifestBytes: 512 << 10,
	MaxSections:      100,
	MaxDepth:         6,
	MaxChildren:      24,
	MaxBindings:      24,
	MaxBindingLimit:  24,
	MaxFeatureItems:  24,
	MaxFAQItems:      32,
	MaxTextRunes:     32_000,
}

type CompositionManifest struct {
	SchemaVersion int                  `json:"schema_version"`
	Mode          string               `json:"mode"`
	Shell         CompositionShell     `json:"shell"`
	Theme         CompositionTheme     `json:"theme"`
	Layout        CompositionLayout    `json:"layout"`
	Sections      []CompositionSection `json:"sections"`
}

type CompositionShell struct {
	Mode         string `json:"mode"`
	StickyHeader bool   `json:"sticky_header"`
}

type CompositionTheme struct {
	Inherit bool              `json:"inherit"`
	Tokens  map[string]string `json:"tokens"`
}

type CompositionLayout struct {
	ContentMaxWidth string `json:"content_max_width"`
	SectionGap      string `json:"section_gap"`
}

type CompositionSection struct {
	ID         string                `json:"id"`
	Type       string                `json:"type"`
	Props      json.RawMessage       `json:"props"`
	Media      *CompositionMedia     `json:"media,omitempty"`
	Binding    *CompositionBinding   `json:"binding,omitempty"`
	Responsive CompositionResponsive `json:"responsive"`
	Children   []CompositionSection  `json:"children,omitempty"`
}

type CompositionMedia struct {
	AssetID int64  `json:"asset_id"`
	SHA256  string `json:"sha256"`
}

type CompositionBinding struct {
	Source        string                   `json:"source"`
	Filter        CompositionBindingFilter `json:"filter"`
	Sort          string                   `json:"sort"`
	Limit         int                      `json:"limit"`
	Fields        []string                 `json:"fields"`
	UpdateMode    string                   `json:"update_mode"`
	MissingPolicy string                   `json:"missing_policy"`
}

type CompositionBindingFilter struct {
	CategorySlug string `json:"category_slug,omitempty"`
	Status       string `json:"status"`
}

type CompositionResponsive struct {
	Desktop CompositionBreakpoint `json:"desktop"`
	Tablet  CompositionBreakpoint `json:"tablet"`
	Mobile  CompositionBreakpoint `json:"mobile"`
}

type CompositionBreakpoint struct {
	Layout        string `json:"layout"`
	Columns       int    `json:"columns"`
	Align         string `json:"align"`
	MediaPosition string `json:"media_position"`
	Hidden        bool   `json:"hidden"`
}

type CompositionAction struct {
	Label string `json:"label"`
	Href  string `json:"href"`
}

type compositionHeroProps struct {
	Eyebrow         string             `json:"eyebrow,omitempty"`
	Title           string             `json:"title"`
	Description     string             `json:"description,omitempty"`
	PrimaryAction   *CompositionAction `json:"primary_action,omitempty"`
	SecondaryAction *CompositionAction `json:"secondary_action,omitempty"`
	Alignment       string             `json:"alignment,omitempty"`
}

type compositionRichTextProps struct {
	Eyebrow string `json:"eyebrow,omitempty"`
	Title   string `json:"title,omitempty"`
	Body    string `json:"body"`
}

type compositionImageProps struct {
	Alt     string `json:"alt"`
	Caption string `json:"caption,omitempty"`
}

type compositionFeatureItem struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Href        string `json:"href,omitempty"`
}

type compositionFeaturesProps struct {
	Eyebrow     string                   `json:"eyebrow,omitempty"`
	Title       string                   `json:"title,omitempty"`
	Description string                   `json:"description,omitempty"`
	Items       []compositionFeatureItem `json:"items"`
}

type compositionCardsProps struct {
	Eyebrow     string `json:"eyebrow,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	EmptyState  string `json:"empty_state,omitempty"`
	ShowExcerpt bool   `json:"show_excerpt"`
}

type compositionFAQItem struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type compositionFAQProps struct {
	Eyebrow string               `json:"eyebrow,omitempty"`
	Title   string               `json:"title,omitempty"`
	Items   []compositionFAQItem `json:"items"`
}

type compositionCTAProps struct {
	Eyebrow     string             `json:"eyebrow,omitempty"`
	Title       string             `json:"title"`
	Description string             `json:"description,omitempty"`
	Action      *CompositionAction `json:"action"`
}

type compositionContactFormProps struct {
	Eyebrow      string   `json:"eyebrow,omitempty"`
	Title        string   `json:"title"`
	Description  string   `json:"description,omitempty"`
	Fields       []string `json:"fields"`
	SubmitLabel  string   `json:"submit_label"`
	PrivacyLabel string   `json:"privacy_label,omitempty"`
	PrivacyHref  string   `json:"privacy_href,omitempty"`
}

type compositionLayoutProps struct {
	Label string `json:"label,omitempty"`
}

// CompositionPropertyDefinition drives the admin property panel and is also
// useful to conversational clients. It intentionally describes controlled
// values, not arbitrary CSS.
type CompositionPropertyDefinition struct {
	Key      string   `json:"key"`
	Kind     string   `json:"kind"`
	Required bool     `json:"required,omitempty"`
	Enum     []string `json:"enum,omitempty"`
}

type CompositionComponentDefinition struct {
	Type               string                          `json:"type"`
	Group              string                          `json:"group"`
	Version            int                             `json:"version"`
	Properties         []CompositionPropertyDefinition `json:"properties"`
	BindingSources     []string                        `json:"binding_sources,omitempty"`
	AllowsChildren     bool                            `json:"allows_children"`
	RequiresMedia      bool                            `json:"requires_media"`
	ResponsiveDefaults CompositionResponsive           `json:"responsive_defaults"`
	Accessibility      []string                        `json:"accessibility"`
	Renderer           string                          `json:"renderer"`
}

type CompositionDiagnostic struct {
	Level   string `json:"level"`
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

type CompositionValidationError struct {
	Diagnostics []CompositionDiagnostic
}

func (e *CompositionValidationError) Error() string {
	if e == nil || len(e.Diagnostics) == 0 {
		return "composition manifest invalid"
	}
	return e.Diagnostics[0].Code + ": " + e.Diagnostics[0].Message
}

func (e *CompositionValidationError) Unwrap() error { return store.ErrPageInvalid }

var (
	compositionIDPattern     = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	compositionSourcePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}[a-z0-9]$|^[a-z]$`)
	compositionFieldPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	compositionSlugPattern   = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	compositionTokenValue    = regexp.MustCompile(`^[A-Za-z0-9#(),.% _/-]+$`)
	compositionAllowedTokens = map[string]bool{
		"color.background": true, "color.surface": true, "color.text": true,
		"color.muted": true, "color.accent": true, "color.border": true,
		"font.body": true, "font.display": true, "radius.control": true,
		"radius.card": true, "shadow.card": true, "space.section": true,
		"width.content": true,
	}
	compositionSorts = map[string]bool{
		"featured,-published_at": true,
		"-published_at":          true,
		"published_at":           true,
		"title":                  true,
		"-title":                 true,
		"-updated_at":            true,
	}
)

func compositionResponsiveDefaults(layout string, desktop, tablet, mobile int) CompositionResponsive {
	return CompositionResponsive{
		Desktop: CompositionBreakpoint{Layout: layout, Columns: desktop, Align: "start", MediaPosition: "after-content"},
		Tablet:  CompositionBreakpoint{Layout: layout, Columns: tablet, Align: "start", MediaPosition: "after-content"},
		Mobile:  CompositionBreakpoint{Layout: "stack", Columns: mobile, Align: "start", MediaPosition: "after-content"},
	}
}

var compositionComponents = map[string]CompositionComponentDefinition{
	"hero.centered": {
		Type: "hero.centered", Group: "marketing", Version: 1, Renderer: "hero",
		Properties: []CompositionPropertyDefinition{
			{Key: "eyebrow", Kind: "text"}, {Key: "title", Kind: "text", Required: true},
			{Key: "description", Kind: "textarea"}, {Key: "primary_action", Kind: "action"},
			{Key: "secondary_action", Kind: "action"}, {Key: "alignment", Kind: "select", Enum: []string{"start", "center"}},
		},
		ResponsiveDefaults: compositionResponsiveDefaults("stack", 1, 1, 1),
		Accessibility:      []string{"one descriptive heading", "link labels must describe their target", "media needs alt text"},
	},
	"hero.split": {
		Type: "hero.split", Group: "marketing", Version: 1, Renderer: "hero",
		Properties: []CompositionPropertyDefinition{
			{Key: "eyebrow", Kind: "text"}, {Key: "title", Kind: "text", Required: true},
			{Key: "description", Kind: "textarea"}, {Key: "primary_action", Kind: "action"},
			{Key: "secondary_action", Kind: "action"}, {Key: "alignment", Kind: "select", Enum: []string{"start", "center"}},
		},
		ResponsiveDefaults: compositionResponsiveDefaults("split", 2, 2, 1),
		Accessibility:      []string{"one descriptive heading", "link labels must describe their target", "media needs alt text"},
	},
	"text.rich": {
		Type: "text.rich", Group: "content", Version: 1, Renderer: "rich_text",
		Properties: []CompositionPropertyDefinition{
			{Key: "eyebrow", Kind: "text"}, {Key: "title", Kind: "text"}, {Key: "body", Kind: "markdown", Required: true},
		},
		ResponsiveDefaults: compositionResponsiveDefaults("stack", 1, 1, 1),
		Accessibility:      []string{"use semantic heading order", "links must use safe protocols"},
	},
	"media.image": {
		Type: "media.image", Group: "content", Version: 1, Renderer: "image", RequiresMedia: true,
		Properties: []CompositionPropertyDefinition{
			{Key: "alt", Kind: "text", Required: true}, {Key: "caption", Kind: "text"},
		},
		ResponsiveDefaults: compositionResponsiveDefaults("stack", 1, 1, 1),
		Accessibility:      []string{"non-decorative images need meaningful alt text"},
	},
	"features.grid": {
		Type: "features.grid", Group: "marketing", Version: 1, Renderer: "features",
		Properties: []CompositionPropertyDefinition{
			{Key: "eyebrow", Kind: "text"}, {Key: "title", Kind: "text"},
			{Key: "description", Kind: "textarea"}, {Key: "items", Kind: "feature_list", Required: true},
		},
		ResponsiveDefaults: compositionResponsiveDefaults("grid", 3, 2, 1),
		Accessibility:      []string{"feature titles are rendered as headings", "linked features need descriptive labels"},
	},
	"content.cards": {
		Type: "content.cards", Group: "data", Version: 1, Renderer: "cards",
		Properties: []CompositionPropertyDefinition{
			{Key: "eyebrow", Kind: "text"}, {Key: "title", Kind: "text"},
			{Key: "description", Kind: "textarea"}, {Key: "empty_state", Kind: "text"},
			{Key: "show_excerpt", Kind: "boolean"},
		},
		BindingSources:     []string{"post", "*"},
		ResponsiveDefaults: compositionResponsiveDefaults("grid", 3, 2, 1),
		Accessibility:      []string{"cards use article landmarks", "card links have visible names"},
	},
	"posts.grid": {
		Type: "posts.grid", Group: "data", Version: 1, Renderer: "cards",
		Properties: []CompositionPropertyDefinition{
			{Key: "eyebrow", Kind: "text"}, {Key: "title", Kind: "text"},
			{Key: "description", Kind: "textarea"}, {Key: "empty_state", Kind: "text"},
			{Key: "show_excerpt", Kind: "boolean"},
		},
		BindingSources:     []string{"post"},
		ResponsiveDefaults: compositionResponsiveDefaults("grid", 3, 2, 1),
		Accessibility:      []string{"cards use article landmarks", "card links have visible names"},
	},
	"products.grid": {
		Type: "products.grid", Group: "data", Version: 1, Renderer: "cards",
		Properties: []CompositionPropertyDefinition{
			{Key: "eyebrow", Kind: "text"}, {Key: "title", Kind: "text"},
			{Key: "description", Kind: "textarea"}, {Key: "empty_state", Kind: "text"},
			{Key: "show_excerpt", Kind: "boolean"},
		},
		BindingSources:     []string{"product"},
		ResponsiveDefaults: compositionResponsiveDefaults("grid", 4, 2, 1),
		Accessibility:      []string{"cards use article landmarks", "product links have visible names"},
	},
	"custom_content.grid": {
		Type: "custom_content.grid", Group: "data", Version: 1, Renderer: "cards",
		Properties: []CompositionPropertyDefinition{
			{Key: "eyebrow", Kind: "text"}, {Key: "title", Kind: "text"},
			{Key: "description", Kind: "textarea"}, {Key: "empty_state", Kind: "text"},
			{Key: "show_excerpt", Kind: "boolean"},
		},
		BindingSources:     []string{"*"},
		ResponsiveDefaults: compositionResponsiveDefaults("grid", 3, 2, 1),
		Accessibility:      []string{"cards use article landmarks", "card links have visible names"},
	},
	"faq.accordion": {
		Type: "faq.accordion", Group: "marketing", Version: 1, Renderer: "faq",
		Properties: []CompositionPropertyDefinition{
			{Key: "eyebrow", Kind: "text"}, {Key: "title", Kind: "text"}, {Key: "items", Kind: "faq_list", Required: true},
		},
		ResponsiveDefaults: compositionResponsiveDefaults("stack", 1, 1, 1),
		Accessibility:      []string{"native details and summary controls provide keyboard behavior"},
	},
	"cta.banner": {
		Type: "cta.banner", Group: "marketing", Version: 1, Renderer: "cta",
		Properties: []CompositionPropertyDefinition{
			{Key: "eyebrow", Kind: "text"}, {Key: "title", Kind: "text", Required: true},
			{Key: "description", Kind: "textarea"}, {Key: "action", Kind: "action", Required: true},
		},
		ResponsiveDefaults: compositionResponsiveDefaults("row", 2, 2, 1),
		Accessibility:      []string{"call to action has a descriptive label"},
	},
	"form.contact": {
		Type: "form.contact", Group: "interaction", Version: 1, Renderer: "contact_form",
		Properties: []CompositionPropertyDefinition{
			{Key: "eyebrow", Kind: "text"}, {Key: "title", Kind: "text", Required: true},
			{Key: "description", Kind: "textarea"}, {Key: "fields", Kind: "field_list", Required: true},
			{Key: "submit_label", Kind: "text", Required: true}, {Key: "privacy_label", Kind: "text"},
			{Key: "privacy_href", Kind: "url"},
		},
		ResponsiveDefaults: compositionResponsiveDefaults("stack", 1, 1, 1),
		Accessibility:      []string{"every field has a label", "privacy consent is explicit when configured"},
	},
	"layout.section": {
		Type: "layout.section", Group: "layout", Version: 1, Renderer: "container",
		Properties:         []CompositionPropertyDefinition{{Key: "label", Kind: "text"}},
		AllowsChildren:     true,
		ResponsiveDefaults: compositionResponsiveDefaults("stack", 1, 1, 1),
		Accessibility:      []string{"optional label becomes an aria label"},
	},
	"layout.columns": {
		Type: "layout.columns", Group: "layout", Version: 1, Renderer: "container",
		Properties:         []CompositionPropertyDefinition{{Key: "label", Kind: "text"}},
		AllowsChildren:     true,
		ResponsiveDefaults: compositionResponsiveDefaults("grid", 2, 2, 1),
		Accessibility:      []string{"visual column order must match document order"},
	},
}

// CompositionComponentRegistry returns defensive copies so callers cannot
// mutate the runtime's validation rules.
func CompositionComponentRegistry() []CompositionComponentDefinition {
	keys := make([]string, 0, len(compositionComponents))
	for key := range compositionComponents {
		keys = append(keys, key)
	}
	sortStrings(keys)
	out := make([]CompositionComponentDefinition, 0, len(keys))
	for _, key := range keys {
		def := compositionComponents[key]
		def.Properties = append([]CompositionPropertyDefinition(nil), def.Properties...)
		def.BindingSources = append([]string(nil), def.BindingSources...)
		def.Accessibility = append([]string(nil), def.Accessibility...)
		out = append(out, def)
	}
	return out
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// NormalizeCompositionManifest performs strict decoding, fills protocol
// defaults, validates every component and returns stable canonical JSON/hash.
func NormalizeCompositionManifest(raw []byte) (*CompositionManifest, string, string, error) {
	if len(raw) == 0 {
		return nil, "", "", compositionInvalid("manifest_invalid", "$", "Manifest 不能为空。")
	}
	if len(raw) > CompositionLimits.MaxManifestBytes {
		return nil, "", "", compositionInvalid("manifest_too_large", "$",
			fmt.Sprintf("Manifest 超过 %d 字节限制。", CompositionLimits.MaxManifestBytes))
	}
	var manifest CompositionManifest
	if err := decodeCompositionStrict(raw, &manifest); err != nil {
		return nil, "", "", compositionInvalid("manifest_invalid", "$", "Manifest JSON 无效或包含未知字段："+err.Error())
	}
	if manifest.SchemaVersion != CompositionManifestVersion {
		return nil, "", "", compositionInvalid("manifest_version_unsupported", "$.schema_version",
			fmt.Sprintf("仅支持 composition Manifest v%d。", CompositionManifestVersion))
	}
	if manifest.Mode != store.PageModeComposition {
		return nil, "", "", compositionInvalid("page_mode_unsupported", "$.mode", "Manifest mode 必须为 composition。")
	}
	if manifest.Shell.Mode == "" {
		manifest.Shell.Mode = store.PageShellSite
	}
	if manifest.Shell.Mode != store.PageShellSite &&
		manifest.Shell.Mode != store.PageShellMinimal &&
		manifest.Shell.Mode != store.PageShellNone {
		return nil, "", "", compositionInvalid("manifest_invalid", "$.shell.mode", "Site Shell 只支持 site、minimal 或 none。")
	}
	if manifest.Theme.Tokens == nil {
		manifest.Theme.Tokens = map[string]string{}
	}
	// Omitted theme objects decode to false; inheritance is the safe/default
	// behavior unless a validated snapshot token set was explicitly supplied.
	if !manifest.Theme.Inherit && len(manifest.Theme.Tokens) == 0 {
		manifest.Theme.Inherit = true
	}
	for key, value := range manifest.Theme.Tokens {
		value = strings.TrimSpace(value)
		if !compositionAllowedTokens[key] {
			return nil, "", "", compositionInvalid("manifest_invalid", "$.theme.tokens."+key, "未知的语义主题 Token。")
		}
		if value == "" || len(value) > 96 || !compositionTokenValue.MatchString(value) ||
			strings.ContainsAny(value, "{};\\") || strings.Contains(strings.ToLower(value), "url(") {
			return nil, "", "", compositionInvalid("manifest_invalid", "$.theme.tokens."+key, "主题 Token 值不安全。")
		}
		manifest.Theme.Tokens[key] = value
	}
	if manifest.Layout.ContentMaxWidth == "" {
		manifest.Layout.ContentMaxWidth = "wide"
	}
	switch manifest.Layout.ContentMaxWidth {
	case "compact", "comfortable", "wide", "full":
	default:
		return nil, "", "", compositionInvalid("manifest_invalid", "$.layout.content_max_width", "内容宽度枚举无效。")
	}
	if manifest.Layout.SectionGap == "" {
		manifest.Layout.SectionGap = "comfortable"
	}
	switch manifest.Layout.SectionGap {
	case "compact", "comfortable", "spacious":
	default:
		return nil, "", "", compositionInvalid("manifest_invalid", "$.layout.section_gap", "区块间距枚举无效。")
	}
	if len(manifest.Sections) == 0 {
		return nil, "", "", compositionInvalid("manifest_invalid", "$.sections", "自由页面至少需要一个区块。")
	}

	state := compositionManifestState{ids: map[string]bool{}}
	for i := range manifest.Sections {
		path := fmt.Sprintf("$.sections[%d]", i)
		if err := normalizeCompositionSection(&manifest.Sections[i], path, 1, &state); err != nil {
			return nil, "", "", err
		}
	}
	if state.sections > CompositionLimits.MaxSections {
		return nil, "", "", compositionInvalid("manifest_limit_exceeded", "$.sections",
			fmt.Sprintf("页面最多允许 %d 个组件实例。", CompositionLimits.MaxSections))
	}
	if state.bindings > CompositionLimits.MaxBindings {
		return nil, "", "", compositionInvalid("manifest_limit_exceeded", "$.sections",
			fmt.Sprintf("页面最多允许 %d 个数据绑定。", CompositionLimits.MaxBindings))
	}

	encoded, err := json.Marshal(&manifest)
	if err != nil {
		return nil, "", "", err
	}
	canonical, hash, err := store.CanonicalJSONHash(string(encoded))
	if err != nil {
		return nil, "", "", err
	}
	return &manifest, canonical, hash, nil
}

type compositionManifestState struct {
	ids      map[string]bool
	sections int
	bindings int
}

func normalizeCompositionSection(section *CompositionSection, path string, depth int, state *compositionManifestState) error {
	state.sections++
	if state.sections > CompositionLimits.MaxSections {
		return compositionInvalid("manifest_limit_exceeded", path, "页面组件数量超过限制。")
	}
	if depth > CompositionLimits.MaxDepth {
		return compositionInvalid("manifest_limit_exceeded", path, "组件嵌套层级超过限制。")
	}
	section.ID = strings.TrimSpace(section.ID)
	if len(section.ID) > 64 || !compositionIDPattern.MatchString(section.ID) {
		return compositionInvalid("manifest_invalid", path+".id", "组件 ID 必须是稳定的小写短横线标识。")
	}
	if state.ids[section.ID] {
		return compositionInvalid("manifest_invalid", path+".id", "组件 ID 在页面内必须唯一。")
	}
	state.ids[section.ID] = true

	def, ok := compositionComponents[section.Type]
	if !ok {
		return compositionInvalid("component_unknown", path+".type", "未知或不受支持的组件："+section.Type)
	}
	if len(section.Children) > 0 && !def.AllowsChildren {
		return compositionInvalid("manifest_invalid", path+".children", "该组件不允许嵌套子组件。")
	}
	if len(section.Children) > CompositionLimits.MaxChildren {
		return compositionInvalid("manifest_limit_exceeded", path+".children", "单个布局容器的子组件过多。")
	}
	if def.AllowsChildren && len(section.Children) == 0 {
		return compositionInvalid("manifest_invalid", path+".children", "布局容器至少需要一个子组件。")
	}

	if err := normalizeCompositionProps(section, path); err != nil {
		return err
	}
	if def.RequiresMedia && section.Media == nil {
		return compositionInvalid("asset_invalid", path+".media", "该组件必须引用不可变素材。")
	}
	if section.Media != nil {
		if section.Media.AssetID <= 0 || !validCompositionSHA256(section.Media.SHA256) {
			return compositionInvalid("asset_invalid", path+".media", "素材必须使用有效的 asset_id 与小写 SHA-256。")
		}
	}
	if section.Binding != nil {
		state.bindings++
		if len(def.BindingSources) == 0 {
			return compositionInvalid("binding_invalid", path+".binding", "该组件不接受数据绑定。")
		}
		if err := normalizeCompositionBinding(section.Binding, def, path+".binding"); err != nil {
			return err
		}
	} else if len(def.BindingSources) > 0 {
		return compositionInvalid("binding_invalid", path+".binding", "数据组件必须声明绑定查询。")
	}
	if err := normalizeCompositionResponsive(&section.Responsive, def.ResponsiveDefaults, path+".responsive"); err != nil {
		return err
	}
	for i := range section.Children {
		if err := normalizeCompositionSection(&section.Children[i],
			fmt.Sprintf("%s.children[%d]", path, i), depth+1, state); err != nil {
			return err
		}
	}
	return nil
}

func normalizeCompositionProps(section *CompositionSection, path string) error {
	raw := section.Props
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte(`{}`)
	}
	var props any
	switch section.Type {
	case "hero.centered", "hero.split":
		p := &compositionHeroProps{}
		if err := decodeCompositionStrict(raw, p); err != nil {
			return componentPropsError(path, err)
		}
		p.Alignment = strings.TrimSpace(p.Alignment)
		if p.Alignment == "" {
			if section.Type == "hero.centered" {
				p.Alignment = "center"
			} else {
				p.Alignment = "start"
			}
		}
		if p.Alignment != "start" && p.Alignment != "center" {
			return compositionInvalid("manifest_invalid", path+".props.alignment", "Hero 对齐方式无效。")
		}
		if err := requireCompositionText(p.Title, path+".props.title", 300); err != nil {
			return err
		}
		if err := optionalCompositionTexts(path+".props", map[string]string{
			"eyebrow": p.Eyebrow, "description": p.Description,
		}); err != nil {
			return err
		}
		if err := validateCompositionAction(p.PrimaryAction, path+".props.primary_action"); err != nil {
			return err
		}
		if err := validateCompositionAction(p.SecondaryAction, path+".props.secondary_action"); err != nil {
			return err
		}
		props = p
	case "text.rich":
		p := &compositionRichTextProps{}
		if err := decodeCompositionStrict(raw, p); err != nil {
			return componentPropsError(path, err)
		}
		if strings.TrimSpace(p.Body) == "" {
			return compositionInvalid("manifest_invalid", path+".props.body", "富文本正文不能为空。")
		}
		if err := compositionTextLimit(p.Body, path+".props.body", CompositionLimits.MaxTextRunes); err != nil {
			return err
		}
		if err := validateCompositionMarkdownURLs(p.Body, path+".props.body"); err != nil {
			return err
		}
		if err := optionalCompositionTexts(path+".props", map[string]string{"eyebrow": p.Eyebrow, "title": p.Title}); err != nil {
			return err
		}
		props = p
	case "media.image":
		p := &compositionImageProps{}
		if err := decodeCompositionStrict(raw, p); err != nil {
			return componentPropsError(path, err)
		}
		if err := requireCompositionText(p.Alt, path+".props.alt", 500); err != nil {
			return err
		}
		if err := compositionTextLimit(p.Caption, path+".props.caption", 1_000); err != nil {
			return err
		}
		props = p
	case "features.grid":
		p := &compositionFeaturesProps{}
		if err := decodeCompositionStrict(raw, p); err != nil {
			return componentPropsError(path, err)
		}
		if len(p.Items) == 0 || len(p.Items) > CompositionLimits.MaxFeatureItems {
			return compositionInvalid("manifest_limit_exceeded", path+".props.items", "特性条目数量无效。")
		}
		if err := optionalCompositionTexts(path+".props", map[string]string{
			"eyebrow": p.Eyebrow, "title": p.Title, "description": p.Description,
		}); err != nil {
			return err
		}
		for i := range p.Items {
			itemPath := fmt.Sprintf("%s.props.items[%d]", path, i)
			if err := requireCompositionText(p.Items[i].Title, itemPath+".title", 300); err != nil {
				return err
			}
			if err := compositionTextLimit(p.Items[i].Description, itemPath+".description", 2_000); err != nil {
				return err
			}
			if p.Items[i].Href != "" && !safeCompositionURL(p.Items[i].Href, true) {
				return compositionInvalid("url_unsafe", itemPath+".href", "链接协议或格式不安全。")
			}
		}
		props = p
	case "content.cards", "posts.grid", "products.grid", "custom_content.grid":
		p := &compositionCardsProps{}
		if err := decodeCompositionStrict(raw, p); err != nil {
			return componentPropsError(path, err)
		}
		if err := optionalCompositionTexts(path+".props", map[string]string{
			"eyebrow": p.Eyebrow, "title": p.Title, "description": p.Description, "empty_state": p.EmptyState,
		}); err != nil {
			return err
		}
		props = p
	case "faq.accordion":
		p := &compositionFAQProps{}
		if err := decodeCompositionStrict(raw, p); err != nil {
			return componentPropsError(path, err)
		}
		if len(p.Items) == 0 || len(p.Items) > CompositionLimits.MaxFAQItems {
			return compositionInvalid("manifest_limit_exceeded", path+".props.items", "FAQ 条目数量无效。")
		}
		if err := optionalCompositionTexts(path+".props", map[string]string{"eyebrow": p.Eyebrow, "title": p.Title}); err != nil {
			return err
		}
		for i := range p.Items {
			itemPath := fmt.Sprintf("%s.props.items[%d]", path, i)
			if err := requireCompositionText(p.Items[i].Question, itemPath+".question", 500); err != nil {
				return err
			}
			if err := requireCompositionText(p.Items[i].Answer, itemPath+".answer", 8_000); err != nil {
				return err
			}
			if err := validateCompositionMarkdownURLs(p.Items[i].Answer, itemPath+".answer"); err != nil {
				return err
			}
		}
		props = p
	case "cta.banner":
		p := &compositionCTAProps{}
		if err := decodeCompositionStrict(raw, p); err != nil {
			return componentPropsError(path, err)
		}
		if err := requireCompositionText(p.Title, path+".props.title", 300); err != nil {
			return err
		}
		if err := optionalCompositionTexts(path+".props", map[string]string{
			"eyebrow": p.Eyebrow, "description": p.Description,
		}); err != nil {
			return err
		}
		if p.Action == nil {
			return compositionInvalid("manifest_invalid", path+".props.action", "CTA 必须配置动作。")
		}
		if err := validateCompositionAction(p.Action, path+".props.action"); err != nil {
			return err
		}
		props = p
	case "form.contact":
		p := &compositionContactFormProps{}
		if err := decodeCompositionStrict(raw, p); err != nil {
			return componentPropsError(path, err)
		}
		if err := requireCompositionText(p.Title, path+".props.title", 300); err != nil {
			return err
		}
		if err := requireCompositionText(p.SubmitLabel, path+".props.submit_label", 120); err != nil {
			return err
		}
		if err := optionalCompositionTexts(path+".props", map[string]string{
			"eyebrow": p.Eyebrow, "description": p.Description, "privacy_label": p.PrivacyLabel,
		}); err != nil {
			return err
		}
		if len(p.Fields) == 0 || len(p.Fields) > 6 {
			return compositionInvalid("manifest_invalid", path+".props.fields", "联系表单字段数量无效。")
		}
		seen := map[string]bool{}
		for i, field := range p.Fields {
			switch field {
			case "name", "email", "phone", "company", "subject", "message":
			default:
				return compositionInvalid("manifest_invalid",
					fmt.Sprintf("%s.props.fields[%d]", path, i), "联系表单字段不受支持。")
			}
			if seen[field] {
				return compositionInvalid("manifest_invalid", path+".props.fields", "联系表单字段不能重复。")
			}
			seen[field] = true
		}
		if !seen["email"] && !seen["phone"] {
			return compositionInvalid("manifest_invalid", path+".props.fields", "联系表单至少需要 email 或 phone。")
		}
		if (p.PrivacyLabel == "") != (p.PrivacyHref == "") {
			return compositionInvalid("manifest_invalid", path+".props.privacy_href", "隐私同意文案与链接必须同时配置。")
		}
		if p.PrivacyHref != "" && !safeCompositionURL(p.PrivacyHref, true) {
			return compositionInvalid("url_unsafe", path+".props.privacy_href", "隐私政策链接不安全。")
		}
		props = p
	case "layout.section", "layout.columns":
		p := &compositionLayoutProps{}
		if err := decodeCompositionStrict(raw, p); err != nil {
			return componentPropsError(path, err)
		}
		if err := compositionTextLimit(p.Label, path+".props.label", 300); err != nil {
			return err
		}
		props = p
	default:
		return compositionInvalid("component_unknown", path+".type", "组件未注册。")
	}
	normalized, err := json.Marshal(props)
	if err != nil {
		return err
	}
	section.Props = normalized
	return nil
}

func normalizeCompositionBinding(binding *CompositionBinding, def CompositionComponentDefinition, path string) error {
	binding.Source = strings.TrimSpace(binding.Source)
	if !compositionSourcePattern.MatchString(binding.Source) {
		return compositionInvalid("binding_invalid", path+".source", "数据源标识无效。")
	}
	allowed := false
	for _, source := range def.BindingSources {
		if source == "*" || source == binding.Source {
			allowed = true
		}
	}
	if !allowed {
		return compositionInvalid("binding_invalid", path+".source", "该组件不能绑定此内容类型。")
	}
	if def.Type == "custom_content.grid" && (binding.Source == "post" || binding.Source == "product") {
		return compositionInvalid("binding_invalid", path+".source", "自定义内容组件必须绑定已启用的自定义类型。")
	}
	binding.Filter.Status = strings.TrimSpace(binding.Filter.Status)
	if binding.Filter.Status == "" {
		binding.Filter.Status = "published"
	}
	if binding.Filter.Status != "published" {
		return compositionInvalid("binding_invalid", path+".filter.status", "公开绑定只能声明 published。")
	}
	binding.Filter.CategorySlug = strings.TrimSpace(binding.Filter.CategorySlug)
	if binding.Filter.CategorySlug != "" && !compositionSlugPattern.MatchString(binding.Filter.CategorySlug) {
		return compositionInvalid("binding_invalid", path+".filter.category_slug", "分类 Slug 格式无效。")
	}
	binding.Sort = strings.TrimSpace(binding.Sort)
	if binding.Sort == "" {
		binding.Sort = "featured,-published_at"
	}
	if !compositionSorts[binding.Sort] {
		return compositionInvalid("binding_invalid", path+".sort", "排序字段不在白名单中。")
	}
	if binding.Limit == 0 {
		binding.Limit = 6
	}
	if binding.Limit < 1 || binding.Limit > CompositionLimits.MaxBindingLimit {
		return compositionInvalid("binding_invalid", path+".limit",
			fmt.Sprintf("单个绑定 limit 必须在 1–%d。", CompositionLimits.MaxBindingLimit))
	}
	if len(binding.Fields) == 0 {
		binding.Fields = []string{"title", "slug", "excerpt", "cover_image"}
	}
	if len(binding.Fields) > 24 {
		return compositionInvalid("binding_invalid", path+".fields", "绑定字段数量过多。")
	}
	seen := map[string]bool{}
	for i, field := range binding.Fields {
		field = strings.TrimSpace(field)
		if !compositionFieldPattern.MatchString(field) || seen[field] {
			return compositionInvalid("binding_invalid", fmt.Sprintf("%s.fields[%d]", path, i), "绑定字段无效或重复。")
		}
		seen[field] = true
		binding.Fields[i] = field
	}
	binding.UpdateMode = strings.TrimSpace(binding.UpdateMode)
	if binding.UpdateMode == "" {
		binding.UpdateMode = "live"
	}
	if binding.UpdateMode != "live" {
		return compositionInvalid(
			"binding_update_mode_unavailable",
			path+".update_mode",
			"当前版本只支持 live；release_snapshot 尚未实现可追溯快照，因此拒绝保存。",
		)
	}
	binding.MissingPolicy = strings.TrimSpace(binding.MissingPolicy)
	if binding.MissingPolicy == "" {
		binding.MissingPolicy = "placeholder"
	}
	switch binding.MissingPolicy {
	case "hide", "placeholder", "block":
	default:
		return compositionInvalid("binding_invalid", path+".missing_policy", "数据缺失策略无效。")
	}
	return nil
}

func normalizeCompositionResponsive(value *CompositionResponsive, defaults CompositionResponsive, path string) error {
	if value.Desktop.Layout == "" {
		value.Desktop = defaults.Desktop
	}
	if value.Tablet.Layout == "" {
		value.Tablet = defaults.Tablet
	}
	if value.Mobile.Layout == "" {
		value.Mobile = defaults.Mobile
	}
	for name, bp := range map[string]*CompositionBreakpoint{
		"desktop": &value.Desktop, "tablet": &value.Tablet, "mobile": &value.Mobile,
	} {
		switch bp.Layout {
		case "stack", "split", "grid", "row":
		default:
			return compositionInvalid("manifest_invalid", path+"."+name+".layout", "响应式布局枚举无效。")
		}
		if bp.Columns == 0 {
			switch name {
			case "desktop":
				bp.Columns = defaults.Desktop.Columns
			case "tablet":
				bp.Columns = defaults.Tablet.Columns
			default:
				bp.Columns = defaults.Mobile.Columns
			}
		}
		if bp.Columns < 1 || bp.Columns > 6 {
			return compositionInvalid("manifest_invalid", path+"."+name+".columns", "响应式列数必须在 1–6。")
		}
		if bp.Align == "" {
			bp.Align = "start"
		}
		switch bp.Align {
		case "start", "center", "end", "stretch":
		default:
			return compositionInvalid("manifest_invalid", path+"."+name+".align", "响应式对齐枚举无效。")
		}
		if bp.MediaPosition == "" {
			bp.MediaPosition = "after-content"
		}
		if bp.MediaPosition != "before-content" && bp.MediaPosition != "after-content" {
			return compositionInvalid("manifest_invalid", path+"."+name+".media_position", "媒体位置枚举无效。")
		}
	}
	return nil
}

func componentPropsError(path string, err error) error {
	return compositionInvalid("manifest_invalid", path+".props", "组件属性无效或包含未知字段："+err.Error())
}

func validateCompositionAction(action *CompositionAction, path string) error {
	if action == nil {
		return nil
	}
	if err := requireCompositionText(action.Label, path+".label", 160); err != nil {
		return err
	}
	if !safeCompositionURL(action.Href, true) {
		return compositionInvalid("url_unsafe", path+".href", "链接协议或格式不安全。")
	}
	return nil
}

func optionalCompositionTexts(path string, values map[string]string) error {
	for key, value := range values {
		if err := compositionTextLimit(value, path+"."+key, 4_000); err != nil {
			return err
		}
	}
	return nil
}

func requireCompositionText(value, path string, max int) error {
	if strings.TrimSpace(value) == "" {
		return compositionInvalid("manifest_invalid", path, "字段不能为空。")
	}
	return compositionTextLimit(value, path, max)
}

func compositionTextLimit(value, path string, max int) error {
	if !utf8.ValidString(value) {
		return compositionInvalid("manifest_invalid", path, "文本不是有效 UTF-8。")
	}
	if utf8.RuneCountInString(value) > max {
		return compositionInvalid("manifest_limit_exceeded", path, fmt.Sprintf("文本超过 %d 字符限制。", max))
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return compositionInvalid("manifest_invalid", path, "文本包含控制字符。")
		}
	}
	return nil
}

func safeCompositionURL(value string, allowContact bool) bool {
	value = strings.TrimSpace(html.UnescapeString(value))
	if value == "" || strings.HasPrefix(value, "//") || strings.Contains(value, "\\") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) && r != ' ' {
			return false
		}
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "" {
		if scheme == "https" {
			return parsed.Host != ""
		}
		if allowContact && (scheme == "mailto" || scheme == "tel") {
			return parsed.Opaque != "" || parsed.Path != ""
		}
		return false
	}
	if strings.HasPrefix(value, "#") {
		return len(value) > 1 && compositionIDPattern.MatchString(strings.TrimPrefix(value, "#"))
	}
	if strings.HasPrefix(value, "/") {
		return !strings.HasPrefix(value, "/api/admin/") && !strings.HasPrefix(value, "/admin/")
	}
	return parsed.Path != "" && !strings.Contains(strings.Split(parsed.Path, "/")[0], ":")
}

func validateCompositionMarkdownURLs(markdown, path string) error {
	source := []byte(markdown)
	doc := gmark.Parser().Parse(text.NewReader(source))
	var invalid string
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || invalid != "" {
			return ast.WalkContinue, nil
		}
		switch n := node.(type) {
		case *ast.Link:
			if !safeCompositionURL(string(n.Destination), true) {
				invalid = string(n.Destination)
			}
		case *ast.Image:
			// Rich text cannot bypass immutable page assets with an inline
			// remote image. Images belong in media.image or component media.
			invalid = string(n.Destination)
		}
		return ast.WalkContinue, nil
	})
	if invalid != "" {
		return compositionInvalid("url_unsafe", path, "富文本包含不安全链接或未受管图片。")
	}
	return nil
}

func validCompositionSHA256(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func decodeCompositionStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON 包含多个值")
		}
		return err
	}
	return nil
}

func compositionInvalid(code, path, message string) error {
	return &CompositionValidationError{Diagnostics: []CompositionDiagnostic{{
		Level: "error", Code: code, Path: path, Message: message,
	}}}
}
