package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"cms.ccvar.com/internal/seo"
	"cms.ccvar.com/internal/store"
)

const compositionRuntimeVersion = "composition-v1"

type CompositionAssetView struct {
	ID         int64  `json:"id"`
	SHA256     string `json:"sha256"`
	URL        string `json:"url"`
	MediaType  string `json:"media_type"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	StorageRef string `json:"-"`
}

type CompositionBuildResult struct {
	Valid             bool                           `json:"valid"`
	RuntimeVersion    string                         `json:"runtime_version"`
	CanonicalManifest string                         `json:"canonical_manifest,omitempty"`
	ManifestHash      string                         `json:"manifest_hash,omitempty"`
	DataSnapshotHash  string                         `json:"data_snapshot_hash,omitempty"`
	RenderHash        string                         `json:"render_hash,omitempty"`
	Diagnostics       []CompositionDiagnostic        `json:"diagnostics"`
	Manifest          *CompositionManifest           `json:"-"`
	ResolvedData      *CompositionResolvedData       `json:"-"`
	Assets            map[int64]CompositionAssetView `json:"-"`
	BodyHTML          template.HTML                  `json:"-"`
}

type compositionRenderIdentity struct {
	SchemaVersion    int               `json:"schema_version"`
	RuntimeVersion   string            `json:"runtime_version"`
	SiteID           int64             `json:"site_id,omitempty"`
	PageID           int64             `json:"page_id"`
	ProjectID        int64             `json:"project_id"`
	RevisionID       int64             `json:"revision_id"`
	PageMetaHash     string            `json:"page_meta_hash"`
	ManifestHash     string            `json:"manifest_hash"`
	DataSnapshotHash string            `json:"data_snapshot_hash"`
	BodyHash         string            `json:"body_hash"`
	Site             seo.Site          `json:"site"`
	Menu             []MenuItem        `json:"menu"`
	Theme            string            `json:"theme"`
	ThemeStyle       string            `json:"theme_style"`
	AssetVersion     string            `json:"asset_version"`
	Year             int               `json:"year"`
	Shell            CompositionShell  `json:"shell"`
	Layout           CompositionLayout `json:"layout"`
}

type CompositionPageRender struct {
	Project      *store.PageProject
	Revision     *store.PageProjectRevision
	Page         *store.Post
	Base         *View
	Build        *CompositionBuildResult
	ShellMode    string
	StickyHeader bool
	Preview      bool
	BodyHTML     template.HTML
	DocumentHTML []byte
}

type compositionSectionView struct {
	ID           string
	Type         string
	ProjectID    int64
	RevisionID   int64
	Class        string
	Desktop      CompositionBreakpoint
	Tablet       CompositionBreakpoint
	Mobile       CompositionBreakpoint
	Hidden       bool
	Label        string
	Eyebrow      string
	Title        string
	Description  string
	RichHTML     template.HTML
	Actions      []compositionActionView
	Asset        *CompositionAssetView
	Alt          string
	Caption      string
	Features     []compositionFeatureView
	Cards        []compositionCardView
	Empty        bool
	EmptyState   string
	FAQs         []compositionFAQView
	FormFields   []compositionFormFieldView
	SubmitLabel  string
	PrivacyLabel string
	PrivacyHref  string
	Children     []compositionSectionView
}

type compositionActionView struct {
	Label    string
	Href     string
	External bool
}

type compositionFeatureView struct {
	Title       string
	Description string
	Href        string
	External    bool
}

type compositionCardFieldView struct {
	Label string
	Value string
}

type compositionCardView struct {
	ID         int64
	Title      string
	URL        string
	Excerpt    string
	CoverImage string
	Category   string
	Fields     []compositionCardFieldView
}

type compositionFAQView struct {
	Question   string
	AnswerHTML template.HTML
}

type compositionFormFieldView struct {
	Name     string
	Type     string
	Label    string
	Required bool
	Textarea bool
}

type compositionDocumentView struct {
	Site         seo.Site
	SEO          seo.Meta
	Menu         []MenuItem
	Theme        string
	ThemeStyle   template.CSS
	AssetVer     string
	Year         int
	ShellMode    string
	StickyHeader bool
	WidthClass   string
	GapClass     string
	Preview      bool
	PreviewLabel string
	RevisionNo   int
	PageID       int64
	ProjectID    int64
	Body         template.HTML
	JSONLD       []template.JS
	InjectHead   template.HTML
	InjectBody   template.HTML
}

// CompositionAssetPublicPath is opaque and content addressed. It never leaks
// the private storage_ref. The central route must verify this ID/hash pair
// against the current project before serving bytes.
func CompositionAssetPublicPath(assetID int64, sha256 string) string {
	if assetID <= 0 || !validCompositionSHA256(sha256) {
		return ""
	}
	return fmt.Sprintf("/page-assets/%d/%s", assetID, sha256)
}

// ValidateCompositionBuild performs all checks needed for a ready composition
// build: schema, site binding policy, immutable assets, dynamic data snapshot
// and safe server rendering. It does not mutate Store state.
func (s *Server) ValidateCompositionBuild(
	ctx context.Context,
	project *store.PageProject,
	revision *store.PageProjectRevision,
	access CompositionBindingAccess,
) (*CompositionBuildResult, error) {
	result := &CompositionBuildResult{
		RuntimeVersion: compositionRuntimeVersion,
		Diagnostics:    []CompositionDiagnostic{},
		Assets:         map[int64]CompositionAssetView{},
	}
	if project == nil || project.ID <= 0 || project.Mode != store.PageModeComposition {
		err := compositionInvalid("page_mode_unsupported", "$", "目标不是自由编排页面工程。")
		result.Diagnostics = compositionDiagnosticsFromError(err)
		return result, err
	}
	if revision == nil || revision.ID <= 0 || revision.ProjectID != project.ID ||
		revision.RevisionKind != store.PageRevisionComposition {
		err := compositionInvalid("revision_invalid", "$", "目标修订不属于该自由页面工程。")
		result.Diagnostics = compositionDiagnosticsFromError(err)
		return result, err
	}
	var meta store.PageRevisionMeta
	if err := decodeCompositionStrict([]byte(revision.PageMetaJSON), &meta); err != nil ||
		strings.TrimSpace(meta.Lang) == "" || strings.TrimSpace(meta.Slug) == "" {
		invalid := compositionInvalid("revision_invalid", "$.page_meta", "页面修订元数据无效。")
		result.Diagnostics = compositionDiagnosticsFromError(invalid)
		return result, invalid
	}

	validation := s.NormalizeAndValidateCompositionManifest([]byte(revision.ManifestJSON), meta.Lang)
	result.Manifest = validation.Manifest
	result.CanonicalManifest = validation.CanonicalJSON
	result.ManifestHash = validation.ManifestHash
	result.Diagnostics = append(result.Diagnostics, validation.Diagnostics...)
	if !validation.Valid {
		err := &CompositionValidationError{Diagnostics: result.Diagnostics}
		return result, err
	}
	if project.SchemaVersion != CompositionManifestVersion {
		diagnostic := CompositionDiagnostic{
			Level: "error", Code: "manifest_version_unsupported", Path: "$.schema_version",
			Message: "工程 Schema 版本与运行时不一致。",
		}
		result.Diagnostics = append(result.Diagnostics, diagnostic)
		return result, &CompositionValidationError{Diagnostics: result.Diagnostics}
	}
	if project.ShellMode != "" && project.ShellMode != validation.Manifest.Shell.Mode {
		diagnostic := CompositionDiagnostic{
			Level: "error", Code: "shell_mode_conflict", Path: "$.shell.mode",
			Message: "工程 Site Shell 与修订快照不一致，请先保存新的统一修订。",
		}
		result.Diagnostics = append(result.Diagnostics, diagnostic)
		return result, &CompositionValidationError{Diagnostics: result.Diagnostics}
	}

	var assetErr bool
	seenAssets := map[int64]bool{}
	walkCompositionSections(validation.Manifest.Sections, func(section *CompositionSection, path string) {
		if section.Media == nil || seenAssets[section.Media.AssetID] {
			return
		}
		seenAssets[section.Media.AssetID] = true
		asset, err := s.store.GetPageAsset(section.Media.AssetID)
		if err != nil {
			assetErr = true
			result.Diagnostics = append(result.Diagnostics, CompositionDiagnostic{
				Level: "error", Code: "asset_read_failed", Path: path + ".media",
				Message: "读取页面素材失败。",
			})
			return
		}
		if asset == nil || asset.ProjectID != project.ID || asset.SHA256 != section.Media.SHA256 {
			assetErr = true
			result.Diagnostics = append(result.Diagnostics, CompositionDiagnostic{
				Level: "error", Code: "asset_invalid", Path: path + ".media",
				Message: "素材不存在、不属于当前工程或哈希不一致。",
			})
			return
		}
		if !strings.HasPrefix(asset.MediaType, "image/") || asset.MediaType == "image/svg+xml" {
			assetErr = true
			result.Diagnostics = append(result.Diagnostics, CompositionDiagnostic{
				Level: "error", Code: "asset_invalid", Path: path + ".media",
				Message: "第一版自由页面只接受不可执行的位图素材。",
			})
			return
		}
		// Metadata alone is not an asset. Re-open the private immutable blob and
		// verify its byte size and SHA before a build can become publishable.
		if _, err := s.readCompositionAsset(asset); err != nil {
			assetErr = true
			result.Diagnostics = append(result.Diagnostics, CompositionDiagnostic{
				Level: "error", Code: "asset_storage_invalid", Path: path + ".media",
				Message: "素材文件缺失、越界或与登记哈希不一致。",
			})
			return
		}
		result.Assets[asset.ID] = CompositionAssetView{
			ID: asset.ID, SHA256: asset.SHA256, URL: CompositionAssetPublicPath(asset.ID, asset.SHA256),
			MediaType: asset.MediaType, Width: asset.Width, Height: asset.Height, StorageRef: asset.StorageRef,
		}
	})
	if assetErr {
		return result, &CompositionValidationError{Diagnostics: result.Diagnostics}
	}

	resolved, err := s.ResolveCompositionBindings(ctx, validation.Manifest, meta.Lang, access)
	if resolved != nil {
		result.ResolvedData = resolved
		result.DataSnapshotHash = resolved.SnapshotHash
		result.Diagnostics = append(result.Diagnostics, resolved.Diagnostics...)
	}
	if err != nil {
		return result, err
	}
	sections, err := s.compositionSectionViews(
		validation.Manifest.Sections, resolved, result.Assets, meta.Lang,
		project.ID, revision.ID,
	)
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, compositionDiagnosticsFromError(err)...)
		return result, err
	}
	body, err := renderCompositionSections(sections)
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, CompositionDiagnostic{
			Level: "error", Code: "render_failed", Message: "服务端渲染失败。",
		})
		return result, err
	}
	result.BodyHTML = body
	viewRequest, _ := http.NewRequest(http.MethodGet, "/"+meta.Lang+"/"+meta.Slug, nil)
	base := s.viewForLang(viewRequest, meta.Lang, meta.Slug)
	identity := compositionRenderIdentity{
		SchemaVersion: 1, RuntimeVersion: compositionRuntimeVersion,
		SiteID: s.platformSiteID, PageID: project.PostID, ProjectID: project.ID,
		RevisionID: revision.ID, PageMetaHash: revision.PageMetaHash,
		ManifestHash: result.ManifestHash, DataSnapshotHash: result.DataSnapshotHash,
		BodyHash: store.SHA256Hex([]byte(body)), Site: base.Site, Menu: base.Menu,
		Theme: base.Theme,
		ThemeStyle: string(compositionResolvedThemeCSS(
			base.ThemeStyle, validation.Manifest.Theme,
		)),
		AssetVersion: base.AssetVer, Year: base.Year,
		Shell: validation.Manifest.Shell, Layout: validation.Manifest.Layout,
	}
	rawIdentity, err := json.Marshal(identity)
	if err != nil {
		result.Diagnostics = append(result.Diagnostics, CompositionDiagnostic{
			Level: "error", Code: "render_identity_failed",
			Message: "无法生成页面主题与外壳快照。",
		})
		return result, err
	}
	result.RenderHash = store.SHA256Hex(rawIdentity)
	result.Valid = !compositionHasErrors(result.Diagnostics)
	return result, nil
}

// RenderCompositionRevision builds the view model and complete safe document
// for one explicit immutable revision. Signed preview handlers should pass
// preview=true; this function itself neither signs nor authorizes a request.
func (s *Server) RenderCompositionRevision(
	r *http.Request,
	project *store.PageProject,
	revision *store.PageProjectRevision,
	preview bool,
	access CompositionBindingAccess,
) (*CompositionPageRender, error) {
	if r == nil {
		r, _ = http.NewRequest(http.MethodGet, "/", nil)
	}
	build, err := s.ValidateCompositionBuild(r.Context(), project, revision, access)
	if err != nil {
		return nil, err
	}
	post, err := s.store.GetPostByID(project.PostID)
	if err != nil {
		return nil, err
	}
	if post == nil || post.Type != "page" {
		return nil, store.ErrPagePostRequired
	}
	page, err := compositionRevisionPage(post, revision)
	if err != nil {
		return nil, err
	}
	s.fillDefaultAuthor(page)

	request := r
	if preview {
		request = r.Clone(withPreviewNoindex(r.Context()))
	}
	base := s.viewForLang(request, page.Lang, page.Slug)
	base.Page = page
	base.SEO = base.Site.Page(page)
	if preview {
		base.ForceNoindex = true
		base.SEO.Robots = "noindex, nofollow"
		base.SEO.Alternates = nil
		base.Langs = nil
		base.Site.InjectHead = ""
		base.Site.InjectBody = ""
	}
	jsonld := compositionJSONLD(base.SEO.JSONLD)
	documentView := compositionDocumentView{
		Site: base.Site, SEO: base.SEO, Menu: base.Menu, Theme: base.Theme,
		ThemeStyle: compositionResolvedThemeCSS(base.ThemeStyle, build.Manifest.Theme), AssetVer: base.AssetVer,
		Year: base.Year, ShellMode: build.Manifest.Shell.Mode,
		StickyHeader: build.Manifest.Shell.StickyHeader,
		WidthClass:   "cmp-width-" + build.Manifest.Layout.ContentMaxWidth,
		GapClass:     "cmp-gap-" + build.Manifest.Layout.SectionGap,
		Preview:      preview, PreviewLabel: compositionPreviewLabel(page.Lang),
		RevisionNo: revision.RevisionNo, PageID: page.ID, ProjectID: project.ID,
		Body: build.BodyHTML, JSONLD: jsonld,
	}
	if !preview {
		documentView.InjectHead = template.HTML(base.Site.InjectHead)
		documentView.InjectBody = template.HTML(base.Site.InjectBody)
	}
	document, err := renderCompositionDocument(documentView)
	if err != nil {
		return nil, err
	}
	if action := s.compositionContactAction(r); action != compositionContactActionPath {
		from := []byte(`action="` + compositionContactActionPath + `"`)
		to := []byte(`action="` + template.HTMLEscapeString(action) + `"`)
		document = bytes.ReplaceAll(document, from, to)
	}
	return &CompositionPageRender{
		Project: project, Revision: revision, Page: page, Base: base, Build: build,
		ShellMode: build.Manifest.Shell.Mode, StickyHeader: build.Manifest.Shell.StickyHeader,
		Preview: preview, BodyHTML: build.BodyHTML, DocumentHTML: document,
	}, nil
}

// RenderPublishedComposition is the fail-closed public/static dispatch helper.
// handled=false means the caller must continue the legacy standard/app branch.
// handled=true with an error means a published composition exists but could
// not be safely rendered; callers must not fall back to stale page.content.
func (s *Server) RenderPublishedComposition(
	r *http.Request,
	post *store.Post,
) (*CompositionPageRender, bool, error) {
	if post == nil || post.Type != "page" {
		return nil, false, nil
	}
	project, err := s.store.GetPageProjectByPostID(post.ID)
	if err != nil {
		return nil, true, err
	}
	if project == nil || project.Mode != store.PageModeComposition || project.PublishedRevisionID <= 0 {
		return nil, false, nil
	}
	revision, err := s.store.GetPageProjectRevision(project.PublishedRevisionID)
	if err != nil {
		return nil, true, err
	}
	if revision == nil {
		return nil, true, store.ErrPageRevisionNotFound
	}
	if revision.RevisionKind == store.PageRevisionStandardBaseline {
		return nil, false, nil
	}
	if revision.RevisionKind != store.PageRevisionComposition {
		return nil, false, nil
	}
	rendered, err := s.RenderCompositionRevision(r, project, revision, false, CompositionBindingPublishedOnly)
	return rendered, true, err
}

// RenderCompositionRevisionPreview is deliberately authorization-agnostic.
// A route must verify a short-lived token bound to project/revision before
// calling it.
func (s *Server) RenderCompositionRevisionPreview(
	r *http.Request,
	projectID, revisionID int64,
	access CompositionBindingAccess,
) (*CompositionPageRender, error) {
	project, err := s.store.GetPageProject(projectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, store.ErrPageProjectNotFound
	}
	revision, err := s.store.GetPageProjectRevision(revisionID)
	if err != nil {
		return nil, err
	}
	if revision == nil || revision.ProjectID != project.ID {
		return nil, store.ErrPageRevisionNotFound
	}
	return s.RenderCompositionRevision(r, project, revision, true, access)
}

// WriteCompositionPage is shared by public, private preview and static-export
// adapters. Preview responses are always non-cacheable and non-indexable.
func (s *Server) WriteCompositionPage(w http.ResponseWriter, rendered *CompositionPageRender, status int) {
	if rendered == nil {
		http.Error(w, "自由页面渲染结果为空。", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if rendered.Preview {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	} else {
		w.Header().Set("Cache-Control", publicPageCacheControl)
		w.Header().Set("ETag", `"`+rendered.Build.ManifestHash+"-"+rendered.Build.DataSnapshotHash+`"`)
	}
	w.WriteHeader(status)
	_, _ = w.Write(rendered.DocumentHTML)
}

func compositionRevisionPage(post *store.Post, revision *store.PageProjectRevision) (*store.Post, error) {
	if post == nil || revision == nil {
		return nil, store.ErrPageInvalid
	}
	var meta store.PageRevisionMeta
	if err := decodeCompositionStrict([]byte(revision.PageMetaJSON), &meta); err != nil {
		return nil, err
	}
	page := *post
	page.Slug = meta.Slug
	page.Title = meta.Title
	page.Excerpt = meta.Excerpt
	page.MetaDesc = meta.MetaDesc
	page.Keywords = meta.Keywords
	page.CoverImage = meta.CoverImage
	page.Author = meta.Author
	page.Lang = meta.Lang
	page.TransGroup = meta.TransGroup
	page.RobotsOverride = meta.RobotsOverride
	page.CanonicalOverride = meta.CanonicalOverride
	page.Content = ""
	return &page, nil
}

func (s *Server) compositionSectionViews(
	sections []CompositionSection,
	data *CompositionResolvedData,
	assets map[int64]CompositionAssetView,
	lang string,
	projectID, revisionID int64,
) ([]compositionSectionView, error) {
	out := make([]compositionSectionView, 0, len(sections))
	for i := range sections {
		view, err := s.compositionSectionView(
			&sections[i], data, assets, lang, projectID, revisionID,
		)
		if err != nil {
			return nil, err
		}
		if !view.Hidden {
			out = append(out, view)
		}
	}
	return out, nil
}

func (s *Server) compositionSectionView(
	section *CompositionSection,
	data *CompositionResolvedData,
	assets map[int64]CompositionAssetView,
	lang string,
	projectID, revisionID int64,
) (compositionSectionView, error) {
	view := compositionSectionView{
		ID: section.ID, Type: section.Type, ProjectID: projectID, RevisionID: revisionID,
		Desktop: section.Responsive.Desktop, Tablet: section.Responsive.Tablet, Mobile: section.Responsive.Mobile,
		Class: "cmp-" + strings.ReplaceAll(section.Type, ".", "-") + " " + compositionResponsiveClass(section.Responsive),
	}
	if section.Responsive.Desktop.Hidden && section.Responsive.Tablet.Hidden && section.Responsive.Mobile.Hidden {
		view.Hidden = true
		return view, nil
	}
	if section.Media != nil {
		if asset, ok := assets[section.Media.AssetID]; ok {
			copy := asset
			view.Asset = &copy
		}
	}
	switch section.Type {
	case "hero.centered", "hero.split":
		var props compositionHeroProps
		_ = json.Unmarshal(section.Props, &props)
		view.Eyebrow, view.Title, view.Description = props.Eyebrow, props.Title, props.Description
		view.Alt = props.Title
		view.Actions = compositionActionViews(props.PrimaryAction, props.SecondaryAction)
	case "text.rich":
		var props compositionRichTextProps
		_ = json.Unmarshal(section.Props, &props)
		view.Eyebrow, view.Title = props.Eyebrow, props.Title
		view.RichHTML, _ = RenderContentWithLinkPolicy(props.Body, s.imageSizes, nil)
	case "media.image":
		var props compositionImageProps
		_ = json.Unmarshal(section.Props, &props)
		view.Alt, view.Caption = props.Alt, props.Caption
	case "features.grid":
		var props compositionFeaturesProps
		_ = json.Unmarshal(section.Props, &props)
		view.Eyebrow, view.Title, view.Description = props.Eyebrow, props.Title, props.Description
		for _, feature := range props.Items {
			view.Features = append(view.Features, compositionFeatureView{
				Title: feature.Title, Description: feature.Description, Href: feature.Href,
				External: strings.HasPrefix(strings.ToLower(feature.Href), "https://"),
			})
		}
	case "content.cards", "posts.grid", "products.grid", "custom_content.grid":
		var props compositionCardsProps
		_ = json.Unmarshal(section.Props, &props)
		view.Eyebrow, view.Title, view.Description = props.Eyebrow, props.Title, props.Description
		view.EmptyState = props.EmptyState
		binding := CompositionBindingResult{Empty: true}
		if data != nil {
			binding = data.BySection[section.ID]
		}
		if binding.Empty && binding.MissingPolicy == "hide" {
			view.Hidden = true
			return view, nil
		}
		view.Empty = binding.Empty
		ct, _ := s.compositionBindingType(binding.Source)
		for _, item := range binding.Items {
			card := compositionCardView{
				ID: item.ID, Title: item.Title, URL: item.URL, CoverImage: item.CoverImage,
				Category: item.Category,
			}
			if props.ShowExcerpt {
				card.Excerpt = item.Excerpt
			}
			keys := make([]string, 0, len(item.Fields))
			for key := range item.Fields {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				value := compositionCardFieldText(item.Fields[key])
				if value == "" {
					continue
				}
				label := key
				if ct != nil {
					if field := ct.FieldByKey(key); field != nil {
						label = field.Label(lang)
					}
				}
				card.Fields = append(card.Fields, compositionCardFieldView{Label: label, Value: value})
			}
			view.Cards = append(view.Cards, card)
		}
	case "faq.accordion":
		var props compositionFAQProps
		_ = json.Unmarshal(section.Props, &props)
		view.Eyebrow, view.Title = props.Eyebrow, props.Title
		for _, item := range props.Items {
			answer, _ := RenderContentWithLinkPolicy(item.Answer, s.imageSizes, nil)
			view.FAQs = append(view.FAQs, compositionFAQView{Question: item.Question, AnswerHTML: answer})
		}
	case "cta.banner":
		var props compositionCTAProps
		_ = json.Unmarshal(section.Props, &props)
		view.Eyebrow, view.Title, view.Description = props.Eyebrow, props.Title, props.Description
		view.Actions = compositionActionViews(props.Action)
	case "form.contact":
		var props compositionContactFormProps
		_ = json.Unmarshal(section.Props, &props)
		view.Eyebrow, view.Title, view.Description = props.Eyebrow, props.Title, props.Description
		view.SubmitLabel, view.PrivacyLabel, view.PrivacyHref = props.SubmitLabel, props.PrivacyLabel, props.PrivacyHref
		for _, field := range props.Fields {
			view.FormFields = append(view.FormFields, compositionFormField(field, lang))
		}
	case "layout.section", "layout.columns":
		var props compositionLayoutProps
		_ = json.Unmarshal(section.Props, &props)
		view.Label = props.Label
		children, err := s.compositionSectionViews(
			section.Children, data, assets, lang, projectID, revisionID,
		)
		if err != nil {
			return compositionSectionView{}, err
		}
		view.Children = children
	default:
		return compositionSectionView{}, compositionInvalid("component_unknown", "", "组件没有渲染器。")
	}
	return view, nil
}

func compositionActionViews(actions ...*CompositionAction) []compositionActionView {
	var out []compositionActionView
	for _, action := range actions {
		if action == nil {
			continue
		}
		out = append(out, compositionActionView{
			Label: action.Label, Href: action.Href,
			External: strings.HasPrefix(strings.ToLower(action.Href), "https://"),
		})
	}
	return out
}

func compositionResponsiveClass(responsive CompositionResponsive) string {
	values := []string{
		"cmp-d-" + responsive.Desktop.Layout, "cmp-d-cols-" + strconv.Itoa(responsive.Desktop.Columns),
		"cmp-d-align-" + responsive.Desktop.Align, "cmp-d-media-" + responsive.Desktop.MediaPosition,
		"cmp-t-" + responsive.Tablet.Layout, "cmp-t-cols-" + strconv.Itoa(responsive.Tablet.Columns),
		"cmp-t-align-" + responsive.Tablet.Align, "cmp-t-media-" + responsive.Tablet.MediaPosition,
		"cmp-m-" + responsive.Mobile.Layout, "cmp-m-cols-" + strconv.Itoa(responsive.Mobile.Columns),
		"cmp-m-align-" + responsive.Mobile.Align, "cmp-m-media-" + responsive.Mobile.MediaPosition,
	}
	if responsive.Desktop.Hidden {
		values = append(values, "cmp-hide-d")
	}
	if responsive.Tablet.Hidden {
		values = append(values, "cmp-hide-t")
	}
	if responsive.Mobile.Hidden {
		values = append(values, "cmp-hide-m")
	}
	return strings.Join(values, " ")
}

func compositionFormField(field, lang string) compositionFormFieldView {
	zh := strings.HasPrefix(strings.ToLower(lang), "zh")
	labelsZH := map[string]string{
		"name": "姓名", "email": "邮箱", "phone": "电话", "company": "公司",
		"subject": "主题", "message": "留言",
	}
	labelsEN := map[string]string{
		"name": "Name", "email": "Email", "phone": "Phone", "company": "Company",
		"subject": "Subject", "message": "Message",
	}
	label := labelsEN[field]
	if zh {
		label = labelsZH[field]
	}
	inputType := "text"
	if field == "email" {
		inputType = "email"
	} else if field == "phone" {
		inputType = "tel"
	}
	return compositionFormFieldView{
		Name: field, Type: inputType, Label: label,
		Required: field == "name" || field == "email" || field == "message",
		Textarea: field == "message",
	}
}

func compositionCardFieldText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case []string:
		return strings.Join(typed, " · ")
	default:
		encoded, _ := json.Marshal(typed)
		if len(encoded) <= 300 {
			return string(encoded)
		}
	}
	return ""
}

func compositionThemeCSS(theme CompositionTheme) template.CSS {
	if len(theme.Tokens) == 0 {
		return ""
	}
	names := map[string]string{
		"color.background": "--cmp-bg", "color.surface": "--cmp-surface",
		"color.text": "--cmp-text", "color.muted": "--cmp-muted",
		"color.accent": "--cmp-accent", "color.border": "--cmp-border",
		"font.body": "--cmp-font-body", "font.display": "--cmp-font-display",
		"radius.control": "--cmp-radius-control", "radius.card": "--cmp-radius-card",
		"shadow.card": "--cmp-shadow-card", "space.section": "--cmp-space-section",
		"width.content": "--cmp-width-content",
	}
	keys := make([]string, 0, len(theme.Tokens))
	for key := range theme.Tokens {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		if cssName := names[key]; cssName != "" {
			out.WriteString(cssName)
			out.WriteByte(':')
			out.WriteString(theme.Tokens[key])
			out.WriteByte(';')
		}
	}
	return template.CSS(out.String())
}

func compositionResolvedThemeCSS(siteTheme template.CSS, theme CompositionTheme) template.CSS {
	return template.CSS(string(siteTheme) + string(compositionThemeCSS(theme)))
}

func compositionJSONLD(values []any) []template.JS {
	out := make([]template.JS, 0, len(values))
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err == nil {
			out = append(out, template.JS(encoded))
		}
	}
	return out
}

func compositionPreviewLabel(lang string) string {
	if strings.HasPrefix(strings.ToLower(lang), "zh") {
		return "草稿预览"
	}
	return "Draft preview"
}

func renderCompositionSections(sections []compositionSectionView) (template.HTML, error) {
	var buffer bytes.Buffer
	if err := compositionSectionTemplate.ExecuteTemplate(&buffer, "sections", sections); err != nil {
		return "", err
	}
	return template.HTML(buffer.String()), nil
}

func renderCompositionDocument(view compositionDocumentView) ([]byte, error) {
	var buffer bytes.Buffer
	if err := compositionDocumentTemplate.Execute(&buffer, view); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

var compositionSectionTemplate = template.Must(template.New("composition_sections").Parse(`
{{define "sections"}}{{range .}}{{template "section" .}}{{end}}{{end}}
{{define "heading"}}{{if .Eyebrow}}<p class="cmp-eyebrow">{{.Eyebrow}}</p>{{end}}{{if .Title}}<h2>{{.Title}}</h2>{{end}}{{if .Description}}<p class="cmp-description">{{.Description}}</p>{{end}}{{end}}
{{define "actions"}}{{if .}}<div class="cmp-actions">{{range .}}<a class="cmp-button" href="{{.Href}}"{{if .External}} target="_blank" rel="noopener noreferrer"{{end}}>{{.Label}}</a>{{end}}</div>{{end}}{{end}}
{{define "section"}}
<section id="{{.ID}}" class="cmp-section {{.Class}}" data-component="{{.Type}}" data-layout-desktop="{{.Desktop.Layout}}" data-columns-desktop="{{.Desktop.Columns}}" data-layout-tablet="{{.Tablet.Layout}}" data-columns-tablet="{{.Tablet.Columns}}" data-layout-mobile="{{.Mobile.Layout}}" data-columns-mobile="{{.Mobile.Columns}}"{{if .Label}} aria-label="{{.Label}}"{{end}}>
{{if or (eq .Type "hero.centered") (eq .Type "hero.split")}}
  <div class="cmp-copy">{{template "heading" .}}{{template "actions" .Actions}}</div>
  {{if .Asset}}<figure class="cmp-media"><img src="{{.Asset.URL}}" alt="{{.Alt}}" loading="eager" decoding="async"{{if .Asset.Width}} width="{{.Asset.Width}}"{{end}}{{if .Asset.Height}} height="{{.Asset.Height}}"{{end}}></figure>{{end}}
{{else if eq .Type "text.rich"}}
  <div class="cmp-copy">{{template "heading" .}}<div class="cmp-rich">{{.RichHTML}}</div></div>
{{else if eq .Type "media.image"}}
  {{if .Asset}}<figure class="cmp-media"><img src="{{.Asset.URL}}" alt="{{.Alt}}" loading="lazy" decoding="async"{{if .Asset.Width}} width="{{.Asset.Width}}"{{end}}{{if .Asset.Height}} height="{{.Asset.Height}}"{{end}}>{{if .Caption}}<figcaption>{{.Caption}}</figcaption>{{end}}</figure>{{end}}
{{else if eq .Type "features.grid"}}
  {{template "heading" .}}<div class="cmp-grid">{{range .Features}}<article class="cmp-card">{{if .Href}}<a href="{{.Href}}"{{if .External}} target="_blank" rel="noopener noreferrer"{{end}}><h3>{{.Title}}</h3></a>{{else}}<h3>{{.Title}}</h3>{{end}}{{if .Description}}<p>{{.Description}}</p>{{end}}</article>{{end}}</div>
{{else if or (eq .Type "content.cards") (eq .Type "posts.grid") (eq .Type "products.grid") (eq .Type "custom_content.grid")}}
  {{template "heading" .}}{{if .Cards}}<div class="cmp-grid">{{range .Cards}}<article class="cmp-card">{{if .CoverImage}}<a class="cmp-card-media" href="{{.URL}}"><img src="{{.CoverImage}}" alt="" loading="lazy" decoding="async"></a>{{end}}{{if .Category}}<p class="cmp-eyebrow">{{.Category}}</p>{{end}}<h3><a href="{{.URL}}">{{.Title}}</a></h3>{{if .Excerpt}}<p>{{.Excerpt}}</p>{{end}}{{if .Fields}}<dl>{{range .Fields}}<div><dt>{{.Label}}</dt><dd>{{.Value}}</dd></div>{{end}}</dl>{{end}}</article>{{end}}</div>{{else if and .Empty .EmptyState}}<p class="cmp-empty" role="status">{{.EmptyState}}</p>{{end}}
{{else if eq .Type "faq.accordion"}}
  {{template "heading" .}}<div class="cmp-faq">{{range .FAQs}}<details><summary>{{.Question}}</summary><div class="cmp-rich">{{.AnswerHTML}}</div></details>{{end}}</div>
{{else if eq .Type "cta.banner"}}
  <div class="cmp-copy">{{template "heading" .}}</div>{{template "actions" .Actions}}
{{else if eq .Type "form.contact"}}
  <div class="cmp-copy">{{template "heading" .}}</div><form class="cmp-form" method="post" action="/api/forms/contact"><input type="hidden" name="_project_id" value="{{.ProjectID}}"><input type="hidden" name="_revision_id" value="{{.RevisionID}}"><input type="hidden" name="_section_id" value="{{.ID}}"><input type="text" name="website" tabindex="-1" autocomplete="off" aria-hidden="true" class="cmp-honeypot">{{range .FormFields}}<label><span>{{.Label}}</span>{{if .Textarea}}<textarea name="{{.Name}}"{{if .Required}} required{{end}} maxlength="4000"></textarea>{{else}}<input type="{{.Type}}" name="{{.Name}}"{{if .Required}} required{{end}} maxlength="500">{{end}}</label>{{end}}{{if .PrivacyHref}}<label class="cmp-consent"><input type="checkbox" name="privacy_consent" value="1" required><span><a href="{{.PrivacyHref}}">{{.PrivacyLabel}}</a></span></label>{{end}}<button class="cmp-button" type="submit">{{.SubmitLabel}}</button></form>
{{else if or (eq .Type "layout.section") (eq .Type "layout.columns")}}
  <div class="cmp-container">{{range .Children}}{{template "section" .}}{{end}}</div>
{{end}}
</section>
{{end}}
`))

var compositionDocumentTemplate = template.Must(template.New("composition_document").Parse(`<!doctype html>
<html lang="{{if .Site.LangTag}}{{.Site.LangTag}}{{else}}zh-CN{{end}}" data-theme="{{.Theme}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.SEO.Title}}</title>
{{if .SEO.Description}}<meta name="description" content="{{.SEO.Description}}">{{end}}
{{if .SEO.Keywords}}<meta name="keywords" content="{{.SEO.Keywords}}">{{end}}
<meta name="robots" content="{{.SEO.Robots}}">
{{if .SEO.Canonical}}<link rel="canonical" href="{{.SEO.Canonical}}">{{end}}
<meta property="og:title" content="{{.SEO.Title}}">
{{if .SEO.Description}}<meta property="og:description" content="{{.SEO.Description}}">{{end}}
{{if .SEO.Canonical}}<meta property="og:url" content="{{.SEO.Canonical}}">{{end}}
{{if .SEO.Image}}<meta property="og:image" content="{{.SEO.Image}}">{{end}}
<meta property="og:type" content="{{if .SEO.OGType}}{{.SEO.OGType}}{{else}}website{{end}}">
{{range .JSONLD}}<script type="application/ld+json">{{.}}</script>{{end}}
<link rel="stylesheet" href="/assets/css/public.css?v={{.AssetVer}}">
<style>
:root{--cmp-bg:var(--bg,#fff);--cmp-surface:var(--surface,#fff);--cmp-text:var(--ink,#171717);--cmp-muted:var(--muted,#666);--cmp-accent:var(--accent,#3156c8);--cmp-border:var(--line,#ddd);--cmp-font-body:var(--sans,system-ui,sans-serif);--cmp-font-display:var(--serif,var(--sans,system-ui,sans-serif));--cmp-radius-control:var(--radius,8px);--cmp-radius-card:var(--radius,12px);--cmp-shadow-card:var(--shadow,none);--cmp-space-section:clamp(3rem,7vw,7rem);--cmp-width-content:var(--w-wide,1240px);{{.ThemeStyle}}}
body.cmp-page{margin:0;background:var(--cmp-bg);color:var(--cmp-text);font-family:var(--cmp-font-body);line-height:1.6}.cmp-shell-header{position:relative;z-index:20;border-bottom:1px solid var(--cmp-border);background:color-mix(in srgb,var(--cmp-bg) 94%,transparent)}.cmp-shell-header.is-sticky{position:sticky;top:0;backdrop-filter:blur(16px)}.cmp-shell-inner{width:min(calc(100% - 2rem),var(--cmp-width-content));margin:auto;min-height:72px;display:flex;align-items:center;justify-content:space-between;gap:2rem}.cmp-brand{display:inline-flex;align-items:center;gap:.65rem;color:inherit;text-decoration:none;font-weight:750}.cmp-brand img{display:block;max-width:150px;max-height:42px}.cmp-shell-nav{display:flex;flex-wrap:wrap;gap:clamp(.8rem,2vw,2rem)}.cmp-shell-nav a{color:inherit;text-decoration:none}.cmp-preview{position:sticky;top:0;z-index:50;padding:.45rem 1rem;background:#fff0a8;color:#302400;text-align:center;font:600 13px/1.4 system-ui}.cmp-main{margin:auto}.cmp-width-compact{max-width:760px}.cmp-width-comfortable{max-width:1080px}.cmp-width-wide{max-width:var(--cmp-width-content)}.cmp-width-full{max-width:none}.cmp-gap-compact{--cmp-gap:2rem}.cmp-gap-comfortable{--cmp-gap:var(--cmp-space-section)}.cmp-gap-spacious{--cmp-gap:clamp(5rem,10vw,10rem)}.cmp-main>.cmp-section{margin-block:var(--cmp-gap)}.cmp-section{padding-inline:clamp(1rem,4vw,3rem);box-sizing:border-box}.cmp-section h1,.cmp-section h2,.cmp-section h3{font-family:var(--cmp-font-display);line-height:1.08;text-wrap:balance}.cmp-section h2{font-size:clamp(2rem,5vw,4.8rem);margin:.15em 0}.cmp-description{max-width:65ch;color:var(--cmp-muted)}.cmp-eyebrow{text-transform:uppercase;letter-spacing:.09em;font-size:.78rem;font-weight:750;color:var(--cmp-accent)}.cmp-actions{display:flex;gap:.75rem;flex-wrap:wrap;margin-top:1.5rem}.cmp-button{display:inline-flex;min-height:44px;align-items:center;justify-content:center;padding:.55rem 1rem;border:1px solid var(--cmp-accent);border-radius:var(--cmp-radius-control);background:var(--cmp-accent);color:#fff;text-decoration:none;font:inherit;font-weight:700}.cmp-cta-banner{column-gap:clamp(1.5rem,5vw,4rem)}.cmp-cta-banner>.cmp-actions{align-self:start;align-items:center;justify-content:flex-start;margin-top:1rem}.cmp-media img{display:block;width:100%;height:auto;border-radius:var(--cmp-radius-card)}.cmp-grid,.cmp-container{display:grid;grid-template-columns:repeat(var(--cmp-cols,1),minmax(0,1fr));gap:clamp(1rem,2.5vw,2rem)}.cmp-card{min-width:0;padding:clamp(1rem,2vw,1.5rem);border:1px solid var(--cmp-border);border-radius:var(--cmp-radius-card);background:var(--cmp-surface);box-shadow:var(--cmp-shadow-card)}.cmp-card h3{font-size:clamp(1.1rem,2vw,1.5rem);margin:.4rem 0}.cmp-card a{color:inherit}.cmp-card-media{display:block;margin:-1rem -1rem 1rem}.cmp-card-media img{display:block;width:100%;aspect-ratio:16/10;object-fit:cover;border-radius:var(--cmp-radius-card) var(--cmp-radius-card) 0 0}.cmp-card dl div{display:flex;justify-content:space-between;gap:1rem;border-top:1px solid var(--cmp-border);padding:.35rem 0}.cmp-card dt{color:var(--cmp-muted)}.cmp-card dd{margin:0}.cmp-rich{max-width:72ch}.cmp-rich img{max-width:100%;height:auto}.cmp-faq details{border-top:1px solid var(--cmp-border);padding:1rem 0}.cmp-faq summary{cursor:pointer;font-weight:700}.cmp-form{display:grid;gap:1rem;max-width:680px}.cmp-form label{display:grid;gap:.35rem}.cmp-form input,.cmp-form textarea{box-sizing:border-box;width:100%;padding:.7rem .8rem;border:1px solid var(--cmp-border);border-radius:var(--cmp-radius-control);background:var(--cmp-surface);color:var(--cmp-text);font:inherit}.cmp-form textarea{min-height:140px;resize:vertical}.cmp-honeypot{position:absolute!important;left:-9999px!important}.cmp-consent{grid-template-columns:auto 1fr!important;align-items:start}.cmp-consent input{width:auto!important}.cmp-shell-footer{border-top:1px solid var(--cmp-border);padding:2rem 1rem;color:var(--cmp-muted)}.cmp-shell-footer>div{width:min(100%,var(--cmp-width-content));margin:auto;display:flex;justify-content:space-between;gap:2rem;flex-wrap:wrap}
@media(min-width:1025px){.cmp-d-grid,.cmp-d-split,.cmp-d-row{display:grid;grid-template-columns:repeat(var(--cmp-cols-d,1),minmax(0,1fr));align-items:var(--cmp-align-d,start)}.cmp-d-cols-1{--cmp-cols-d:1}.cmp-d-cols-2{--cmp-cols-d:2}.cmp-d-cols-3{--cmp-cols-d:3}.cmp-d-cols-4{--cmp-cols-d:4}.cmp-d-cols-5{--cmp-cols-d:5}.cmp-d-cols-6{--cmp-cols-d:6}.cmp-d-grid .cmp-grid,.cmp-d-split .cmp-grid,.cmp-d-row .cmp-grid,.cmp-d-grid>.cmp-container,.cmp-d-split>.cmp-container,.cmp-d-row>.cmp-container{--cmp-cols:var(--cmp-cols-d)}.cmp-hide-d{display:none!important}.cmp-d-media-before-content .cmp-media{order:-1}.cmp-d-align-center{text-align:center}.cmp-d-align-center .cmp-description{margin-inline:auto}.cmp-d-align-end{text-align:right}.cmp-cta-banner:is(.cmp-d-grid,.cmp-d-split,.cmp-d-row):not(.cmp-d-cols-1)>.cmp-actions{align-self:center;margin-top:0}}
@media(min-width:641px) and (max-width:1024px){.cmp-t-grid,.cmp-t-split,.cmp-t-row{display:grid;grid-template-columns:repeat(var(--cmp-cols-t,1),minmax(0,1fr))}.cmp-t-cols-1{--cmp-cols-t:1}.cmp-t-cols-2{--cmp-cols-t:2}.cmp-t-cols-3{--cmp-cols-t:3}.cmp-t-cols-4{--cmp-cols-t:4}.cmp-t-cols-5{--cmp-cols-t:5}.cmp-t-cols-6{--cmp-cols-t:6}.cmp-t-grid .cmp-grid,.cmp-t-split .cmp-grid,.cmp-t-row .cmp-grid,.cmp-t-grid>.cmp-container,.cmp-t-split>.cmp-container,.cmp-t-row>.cmp-container{--cmp-cols:var(--cmp-cols-t)}.cmp-hide-t{display:none!important}.cmp-t-media-before-content .cmp-media{order:-1}.cmp-t-align-center{text-align:center}.cmp-t-align-end{text-align:right}.cmp-cta-banner:is(.cmp-t-grid,.cmp-t-split,.cmp-t-row):not(.cmp-t-cols-1)>.cmp-actions{align-self:center;margin-top:0}}
@media(max-width:640px){.cmp-shell-inner{min-height:60px}.cmp-shell-nav{display:none}.cmp-main>.cmp-section{margin-block:clamp(2.5rem,12vw,5rem)}.cmp-m-grid,.cmp-m-split,.cmp-m-row{display:grid;grid-template-columns:repeat(var(--cmp-cols-m,1),minmax(0,1fr))}.cmp-m-cols-1{--cmp-cols-m:1}.cmp-m-cols-2{--cmp-cols-m:2}.cmp-m-cols-3{--cmp-cols-m:3}.cmp-m-cols-4{--cmp-cols-m:4}.cmp-m-cols-5{--cmp-cols-m:5}.cmp-m-cols-6{--cmp-cols-m:6}.cmp-m-grid .cmp-grid,.cmp-m-split .cmp-grid,.cmp-m-row .cmp-grid,.cmp-m-grid>.cmp-container,.cmp-m-split>.cmp-container,.cmp-m-row>.cmp-container{--cmp-cols:var(--cmp-cols-m)}.cmp-hide-m{display:none!important}.cmp-m-media-before-content .cmp-media{order:-1}.cmp-m-align-center{text-align:center}.cmp-m-align-end{text-align:right}.cmp-cta-banner:is(.cmp-m-grid,.cmp-m-split,.cmp-m-row):not(.cmp-m-cols-1)>.cmp-actions{align-self:center;margin-top:0}}
.cmp-features-grid,.cmp-content-cards,.cmp-posts-grid,.cmp-products-grid,.cmp-custom-content-grid,.cmp-layout-columns{display:block}
</style>
{{.InjectHead}}
</head>
<body class="cmp-page">
{{if .Preview}}<div class="cmp-preview" role="status">{{.PreviewLabel}} · Revision {{.RevisionNo}}</div>{{end}}
{{if eq .ShellMode "site"}}<header class="cmp-shell-header{{if .StickyHeader}} is-sticky{{end}}"><div class="cmp-shell-inner"><a class="cmp-brand" href="{{.Site.Prefix}}/">{{if and .Site.Logo (ne .Site.Brand "text")}}<img src="{{.Site.Logo}}" alt="{{.Site.Name}}">{{end}}{{if or (not .Site.Logo) (ne .Site.Brand "logo")}}<span>{{.Site.Name}}</span>{{end}}</a><nav class="cmp-shell-nav" aria-label="Main navigation">{{range .Menu}}<a href="{{.Href}}"{{if .Active}} aria-current="page"{{end}}{{if .External}} target="_blank" rel="noopener noreferrer"{{end}}>{{.Label}}</a>{{end}}</nav></div></header>{{else if eq .ShellMode "minimal"}}<header class="cmp-shell-header{{if .StickyHeader}} is-sticky{{end}}"><div class="cmp-shell-inner"><a class="cmp-brand" href="{{.Site.Prefix}}/">{{if and .Site.Logo (ne .Site.Brand "text")}}<img src="{{.Site.Logo}}" alt="{{.Site.Name}}">{{end}}{{if or (not .Site.Logo) (ne .Site.Brand "logo")}}<span>{{.Site.Name}}</span>{{end}}</a></div></header>{{end}}
<main class="cmp-main {{.WidthClass}} {{.GapClass}}" data-page-mode="composition" data-project-id="{{.ProjectID}}" data-page-id="{{.PageID}}">{{.Body}}</main>
{{if eq .ShellMode "site"}}<footer class="cmp-shell-footer"><div><span>{{.Site.Name}}</span><span>© {{.Year}}{{if .Site.FooterNote}} · {{.Site.FooterNote}}{{end}}</span></div></footer>{{end}}
{{.InjectBody}}
</body></html>`))
