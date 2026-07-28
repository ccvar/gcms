package web

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"cms.ccvar.com/internal/store"
)

const compositionMaxBindingSortScan = 500

// compositionBaseBindingFields is the single field allow-list shared by
// validation and capability discovery. A field appearing in API discovery is
// therefore guaranteed to be accepted by the resolver (and vice versa).
var compositionBaseBindingFields = map[string]string{
	"id":           "integer",
	"title":        "text",
	"slug":         "text",
	"excerpt":      "text",
	"cover_image":  "image",
	"author":       "text",
	"category":     "text",
	"published_at": "datetime",
	"updated_at":   "datetime",
}

type CompositionBindingAccess string

const (
	CompositionBindingPublishedOnly       CompositionBindingAccess = "published"
	CompositionBindingAuthorizedDraftData CompositionBindingAccess = "authorized_drafts"
)

type CompositionBindingItem struct {
	ID          int64          `json:"id"`
	Type        string         `json:"type"`
	Title       string         `json:"title"`
	Slug        string         `json:"slug"`
	URL         string         `json:"url"`
	Excerpt     string         `json:"excerpt,omitempty"`
	CoverImage  string         `json:"cover_image,omitempty"`
	Author      string         `json:"author,omitempty"`
	Category    string         `json:"category,omitempty"`
	PublishedAt string         `json:"published_at,omitempty"`
	UpdatedAt   string         `json:"updated_at,omitempty"`
	Fields      map[string]any `json:"fields,omitempty"`
}

type CompositionBindingResult struct {
	SectionID     string                   `json:"section_id"`
	Source        string                   `json:"source"`
	UpdateMode    string                   `json:"update_mode"`
	MissingPolicy string                   `json:"missing_policy"`
	Items         []CompositionBindingItem `json:"items"`
	Empty         bool                     `json:"empty"`
}

type CompositionResolvedData struct {
	BySection    map[string]CompositionBindingResult `json:"by_section"`
	SnapshotHash string                              `json:"snapshot_hash"`
	Diagnostics  []CompositionDiagnostic             `json:"diagnostics,omitempty"`
}

type CompositionManifestValidation struct {
	Manifest      *CompositionManifest    `json:"manifest,omitempty"`
	CanonicalJSON string                  `json:"canonical_json,omitempty"`
	ManifestHash  string                  `json:"manifest_hash,omitempty"`
	Diagnostics   []CompositionDiagnostic `json:"diagnostics"`
	Valid         bool                    `json:"valid"`
}

// NormalizeAndValidateCompositionManifest is the shared entry point for admin
// and automation writes. Structural validation is followed by the current
// site's content-type/field policy so a disabled or unknown source can never
// be smuggled into a stored "valid" revision.
func (s *Server) NormalizeAndValidateCompositionManifest(raw []byte, lang string) CompositionManifestValidation {
	manifest, canonical, hash, err := NormalizeCompositionManifest(raw)
	if err != nil {
		return CompositionManifestValidation{
			Diagnostics: compositionDiagnosticsFromError(err),
			Valid:       false,
		}
	}
	diagnostics := s.validateCompositionBindings(manifest, lang)
	return CompositionManifestValidation{
		Manifest:      manifest,
		CanonicalJSON: canonical,
		ManifestHash:  hash,
		Diagnostics:   diagnostics,
		Valid:         !compositionHasErrors(diagnostics),
	}
}

func (s *Server) validateCompositionBindings(manifest *CompositionManifest, lang string) []CompositionDiagnostic {
	if manifest == nil {
		return []CompositionDiagnostic{{
			Level: "error", Code: "manifest_invalid", Path: "$", Message: "Manifest 为空。",
		}}
	}
	var diagnostics []CompositionDiagnostic
	walkCompositionSections(manifest.Sections, func(section *CompositionSection, path string) {
		if section.Binding == nil {
			return
		}
		ct, err := s.compositionBindingType(section.Binding.Source)
		if err != nil {
			diagnostics = append(diagnostics, compositionDiagnosticFromError(err, path+".binding.source"))
			return
		}
		if err := validateCompositionBindingFields(section.Binding, ct); err != nil {
			diagnostics = append(diagnostics, compositionDiagnosticFromError(err, path+".binding.fields"))
		}
		if slug := section.Binding.Filter.CategorySlug; slug != "" {
			if !ct.HasCategory {
				diagnostics = append(diagnostics, CompositionDiagnostic{
					Level: "error", Code: "binding_invalid", Path: path + ".binding.filter.category_slug",
					Message: "此内容类型不支持分类筛选。",
				})
				return
			}
			category, readErr := s.store.GetCategoryBySlug(lang, slug)
			if readErr != nil {
				diagnostics = append(diagnostics, CompositionDiagnostic{
					Level: "error", Code: "binding_read_failed", Path: path + ".binding.filter.category_slug",
					Message: "读取绑定分类失败。",
				})
			} else if category == nil || category.Kind != ct.Key {
				diagnostics = append(diagnostics, CompositionDiagnostic{
					Level: "error", Code: "binding_invalid", Path: path + ".binding.filter.category_slug",
					Message: "绑定分类不存在或不属于所选内容类型。",
				})
			}
		}
	})
	return diagnostics
}

func (s *Server) compositionBindingType(source string) (*ContentType, error) {
	source = strings.TrimSpace(source)
	if source == "post" {
		return contentTypeByKey("post"), nil
	}
	// Pages and external links are intentionally not query sources in v1.
	if source == "page" || source == "link" {
		return nil, compositionInvalid("binding_invalid", "", "该内置类型不能作为自由页面卡片数据源。")
	}
	ct := s.lookupType(source)
	if ct == nil || ct.Builtin {
		return nil, compositionInvalid("binding_invalid", "", "数据源不存在。")
	}
	if !s.contentTypeActive(source) {
		return nil, compositionInvalid("binding_invalid", "", "数据源未在当前站点启用。")
	}
	return ct, nil
}

func validateCompositionBindingFields(binding *CompositionBinding, ct *ContentType) error {
	if binding == nil || ct == nil {
		return compositionInvalid("binding_invalid", "", "绑定或内容类型为空。")
	}
	extra := map[string]bool{}
	for _, field := range ct.Fields {
		if !field.Structural && field.Type != FieldRelation {
			extra[field.Key] = true
		}
	}
	for _, field := range binding.Fields {
		if _, base := compositionBaseBindingFields[field]; !base && !extra[field] {
			return compositionInvalid("binding_invalid", "", "字段不在内容类型白名单中："+field)
		}
	}
	return nil
}

// ResolveCompositionBindings runs only declared, bounded queries. It never
// accepts SQL fragments and always produces a deterministic deployment
// snapshot hash.
func (s *Server) ResolveCompositionBindings(
	ctx context.Context,
	manifest *CompositionManifest,
	lang string,
	access CompositionBindingAccess,
) (*CompositionResolvedData, error) {
	if manifest == nil {
		return nil, compositionInvalid("manifest_invalid", "$", "Manifest 为空。")
	}
	if access == CompositionBindingAuthorizedDraftData {
		return nil, compositionInvalid(
			"binding_access_unavailable", "$",
			"草稿数据绑定尚未建立授权协议；当前仅允许读取已发布内容。",
		)
	}
	if access != CompositionBindingPublishedOnly {
		return nil, compositionInvalid("binding_invalid", "$", "绑定读取级别无效。")
	}
	if diagnostics := s.validateCompositionBindings(manifest, lang); compositionHasErrors(diagnostics) {
		return &CompositionResolvedData{Diagnostics: diagnostics}, &CompositionValidationError{Diagnostics: diagnostics}
	}

	resolved := &CompositionResolvedData{BySection: map[string]CompositionBindingResult{}}
	var firstErr error
	walkCompositionSections(manifest.Sections, func(section *CompositionSection, path string) {
		if firstErr != nil || section.Binding == nil {
			return
		}
		select {
		case <-ctx.Done():
			firstErr = ctx.Err()
			return
		default:
		}
		result, err := s.resolveCompositionBinding(section.ID, section.Binding, lang, access)
		if err != nil {
			firstErr = err
			return
		}
		resolved.BySection[section.ID] = result
		if result.Empty {
			level := "warning"
			if result.MissingPolicy == "block" {
				level = "error"
			}
			resolved.Diagnostics = append(resolved.Diagnostics, CompositionDiagnostic{
				Level: level, Code: "binding_empty", Path: path + ".binding",
				Message: "数据绑定当前没有可展示的内容。",
			})
		}
	})
	if firstErr != nil {
		return nil, firstErr
	}
	if compositionHasErrors(resolved.Diagnostics) {
		return resolved, &CompositionValidationError{Diagnostics: resolved.Diagnostics}
	}
	snapshot := make([]CompositionBindingResult, 0, len(resolved.BySection))
	for _, value := range resolved.BySection {
		snapshot = append(snapshot, value)
	}
	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].SectionID < snapshot[j].SectionID })
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	_, resolved.SnapshotHash, err = store.CanonicalJSONHash(string(encoded))
	if err != nil {
		return nil, err
	}
	return resolved, nil
}

func (s *Server) resolveCompositionBinding(
	sectionID string,
	binding *CompositionBinding,
	lang string,
	access CompositionBindingAccess,
) (CompositionBindingResult, error) {
	ct, err := s.compositionBindingType(binding.Source)
	if err != nil {
		return CompositionBindingResult{}, err
	}
	if err := validateCompositionBindingFields(binding, ct); err != nil {
		return CompositionBindingResult{}, err
	}
	var categoryID int64
	if binding.Filter.CategorySlug != "" {
		category, err := s.store.GetCategoryBySlug(lang, binding.Filter.CategorySlug)
		if err != nil {
			return CompositionBindingResult{}, err
		}
		if category == nil || category.Kind != ct.Key {
			return CompositionBindingResult{}, compositionInvalid("binding_invalid", "", "绑定分类不存在。")
		}
		categoryID = category.ID
	}

	posts, err := s.compositionBindingPosts(ct.Key, lang, categoryID, binding, access)
	if err != nil {
		return CompositionBindingResult{}, err
	}
	if len(posts) > binding.Limit {
		posts = posts[:binding.Limit]
	}
	result := CompositionBindingResult{
		SectionID: sectionID, Source: binding.Source, UpdateMode: binding.UpdateMode,
		MissingPolicy: binding.MissingPolicy, Items: make([]CompositionBindingItem, 0, len(posts)),
	}
	tr := s.i18n.Tr(lang, s.defaultLang())
	for _, post := range posts {
		item := compositionBindingItem(post, binding.Fields, ct)
		item.URL = tr.U(publicContentPath(post.Type, post.Slug))
		result.Items = append(result.Items, item)
	}
	result.Empty = len(result.Items) == 0
	return result, nil
}

func (s *Server) compositionBindingPosts(
	kind, lang string,
	categoryID int64,
	binding *CompositionBinding,
	access CompositionBindingAccess,
) ([]*store.Post, error) {
	if access == CompositionBindingPublishedOnly && binding.Sort == "featured,-published_at" {
		return s.store.ListPublishedByType(kind, lang, categoryID, 0, binding.Limit)
	}

	var posts []*store.Post
	if access == CompositionBindingPublishedOnly {
		total, err := s.store.CountPublishedByType(kind, lang, categoryID)
		if err != nil {
			return nil, err
		}
		if total > compositionMaxBindingSortScan {
			return nil, compositionInvalid("binding_limit_exceeded", "",
				"该排序需要扫描过多内容，请改用 featured,-published_at 或缩小分类范围。")
		}
		if total > 0 {
			posts, err = s.store.ListPublishedByType(kind, lang, categoryID, 0, total)
			if err != nil {
				return nil, err
			}
		}
	} else {
		var err error
		posts, err = s.store.ListAllByType(kind, lang)
		if err != nil {
			return nil, err
		}
		if len(posts) > compositionMaxBindingSortScan {
			return nil, compositionInvalid("binding_limit_exceeded", "",
				"草稿预览候选内容过多，请先选择分类。")
		}
		filtered := posts[:0]
		for _, post := range posts {
			if post.Discarded() || (categoryID > 0 && (!post.CategoryID.Valid || post.CategoryID.Int64 != categoryID)) {
				continue
			}
			if post.Status != "published" && post.Status != "draft" && post.Status != "scheduled" {
				continue
			}
			filtered = append(filtered, post)
		}
		posts = filtered
	}
	sortCompositionPosts(posts, binding.Sort)
	return posts, nil
}

func sortCompositionPosts(posts []*store.Post, order string) {
	sort.SliceStable(posts, func(i, j int) bool {
		a, b := posts[i], posts[j]
		switch order {
		case "featured,-published_at":
			if a.Featured != b.Featured {
				return a.Featured
			}
			if !a.PublishedAt.Equal(b.PublishedAt) {
				return a.PublishedAt.After(b.PublishedAt)
			}
		case "-published_at":
			if !a.PublishedAt.Equal(b.PublishedAt) {
				return a.PublishedAt.After(b.PublishedAt)
			}
		case "published_at":
			if !a.PublishedAt.Equal(b.PublishedAt) {
				return a.PublishedAt.Before(b.PublishedAt)
			}
		case "title":
			if a.Title != b.Title {
				return a.Title < b.Title
			}
		case "-title":
			if a.Title != b.Title {
				return a.Title > b.Title
			}
		case "-updated_at":
			if !a.UpdatedAt.Equal(b.UpdatedAt) {
				return a.UpdatedAt.After(b.UpdatedAt)
			}
		}
		return a.ID > b.ID
	})
}

func compositionBindingItem(post *store.Post, fields []string, ct *ContentType) CompositionBindingItem {
	item := CompositionBindingItem{
		ID: post.ID, Type: post.Type, Title: post.Title, Slug: post.Slug,
		Fields: map[string]any{},
	}
	extra := parseExtraMap(post.Extra)
	for _, field := range fields {
		switch field {
		case "id":
			item.Fields[field] = post.ID
		case "title":
			item.Title = post.Title
		case "slug":
			item.Slug = post.Slug
		case "excerpt":
			item.Excerpt = post.Excerpt
		case "cover_image":
			if safeCompositionMediaURL(post.CoverImage) {
				item.CoverImage = post.CoverImage
			}
		case "author":
			item.Author = post.Author
		case "category":
			if post.Category != nil {
				item.Category = post.Category.Name
			}
		case "published_at":
			item.PublishedAt = compositionAPITime(post.PublishedAt)
		case "updated_at":
			item.UpdatedAt = compositionAPITime(post.UpdatedAt)
		default:
			if definition := ct.FieldByKey(field); definition != nil {
				if value, ok := compositionExtraValue(extra[field], definition.Type); ok {
					item.Fields[field] = value
				}
			}
		}
	}
	if len(item.Fields) == 0 {
		item.Fields = nil
	}
	return item
}

func compositionExtraValue(value any, kind FieldType) (any, bool) {
	if value == nil {
		return nil, false
	}
	switch kind {
	case FieldBool:
		b, ok := value.(bool)
		return b, ok
	case FieldNumber:
		switch v := value.(type) {
		case float64:
			return v, true
		case json.Number:
			return v.String(), true
		default:
			text := scalarString(value)
			if _, err := strconv.ParseFloat(text, 64); err == nil {
				return text, true
			}
		}
	case FieldImage, FieldGallery:
		var safe []string
		for _, candidate := range toStringList(value) {
			if safeCompositionMediaURL(candidate) {
				safe = append(safe, candidate)
			}
		}
		if len(safe) > 0 {
			return safe, true
		}
	case FieldRepeater:
		var rows []map[string]string
		if raw, ok := value.([]any); ok {
			for _, candidate := range raw {
				row, ok := candidate.(map[string]any)
				if !ok {
					continue
				}
				key, val := scalarString(row["k"]), scalarString(row["v"])
				if key != "" {
					rows = append(rows, map[string]string{"k": key, "v": val})
				}
			}
		}
		if len(rows) > 0 {
			return rows, true
		}
	case FieldURL:
		text := scalarString(value)
		if safeCompositionURL(text, true) {
			return text, true
		}
	default:
		text := scalarString(value)
		if text != "" && utf8RuneCount(text) <= 4_000 {
			return text, true
		}
	}
	return nil, false
}

func safeCompositionMediaURL(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "//") || strings.Contains(value, "\\") {
		return false
	}
	if strings.HasPrefix(value, "/") {
		return !strings.HasPrefix(value, "/admin/") && !strings.HasPrefix(value, "/api/admin/")
	}
	return strings.HasPrefix(strings.ToLower(value), "https://")
}

func compositionAPITime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func utf8RuneCount(value string) int {
	return len([]rune(value))
}

func walkCompositionSections(sections []CompositionSection, visit func(*CompositionSection, string)) {
	var walk func([]CompositionSection, string)
	walk = func(items []CompositionSection, prefix string) {
		for i := range items {
			path := fmt.Sprintf("%s[%d]", prefix, i)
			visit(&items[i], path)
			walk(items[i].Children, path+".children")
		}
	}
	walk(sections, "$.sections")
}

func compositionDiagnosticsFromError(err error) []CompositionDiagnostic {
	var validation *CompositionValidationError
	if errorsAsComposition(err, &validation) && len(validation.Diagnostics) > 0 {
		return append([]CompositionDiagnostic(nil), validation.Diagnostics...)
	}
	return []CompositionDiagnostic{{
		Level: "error", Code: "manifest_invalid", Message: err.Error(),
	}}
}

func errorsAsComposition(err error, target **CompositionValidationError) bool {
	for err != nil {
		if typed, ok := err.(*CompositionValidationError); ok {
			*target = typed
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = unwrapper.Unwrap()
	}
	return false
}

func compositionDiagnosticFromError(err error, fallbackPath string) CompositionDiagnostic {
	diagnostics := compositionDiagnosticsFromError(err)
	diagnostic := diagnostics[0]
	if diagnostic.Path == "" {
		diagnostic.Path = fallbackPath
	}
	return diagnostic
}

func compositionHasErrors(diagnostics []CompositionDiagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == "error" {
			return true
		}
	}
	return false
}
