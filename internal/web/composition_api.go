package web

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"cms.ccvar.com/internal/store"
)

// registerCompositionAPIRoutes mirrors the same registry and resolver through
// both API namespaces. There is no separate platform implementation: site
// selection happens before these handlers run, so all results come from the
// selected site's real Store and enabled content-type registry.
func (s *Server) registerCompositionAPIRoutes(mux *http.ServeMux) {
	register := func(prefix string) {
		mux.HandleFunc("GET "+prefix+"/page-design-context", s.apiPageDesignContext)
		mux.HandleFunc("GET "+prefix+"/page-components", s.apiCompositionComponents)
		mux.HandleFunc("GET "+prefix+"/page-data-sources", s.apiCompositionDataSources)
		mux.HandleFunc("POST "+prefix+"/page-bindings/preview", s.apiCompositionBindingPreview)
	}
	register("/api/admin/v1")
	register("/api/platform/v1/sites/{siteID}")
}

type compositionDataSourceField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Default  bool   `json:"default,omitempty"`
}

type compositionDataSourceDescriptor struct {
	Key         string                       `json:"key"`
	Label       string                       `json:"label"`
	URLPrefix   string                       `json:"url_prefix"`
	HasCategory bool                         `json:"has_category"`
	Fields      []compositionDataSourceField `json:"fields"`
	Sorts       []string                     `json:"sorts"`
	MaxItems    int                          `json:"max_items"`
}

type compositionBindingPreviewInput struct {
	Lang          string              `json:"lang,omitempty"`
	Manifest      json.RawMessage     `json:"manifest,omitempty"`
	ComponentType string              `json:"component_type,omitempty"`
	SectionID     string              `json:"section_id,omitempty"`
	Binding       *CompositionBinding `json:"binding,omitempty"`
}

func (s *Server) apiCompositionComponents(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsRead); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"manifest_version": CompositionManifestVersion,
		"items":            CompositionComponentRegistry(),
		"limits": map[string]any{
			"max_manifest_bytes": CompositionLimits.MaxManifestBytes,
			"max_sections":       CompositionLimits.MaxSections,
			"max_nesting_depth":  CompositionLimits.MaxDepth,
			"max_children":       CompositionLimits.MaxChildren,
			"max_bindings":       CompositionLimits.MaxBindings,
			"max_binding_items":  CompositionLimits.MaxBindingLimit,
		},
	})
}

func (s *Server) apiCompositionDataSources(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsRead); !ok {
		return
	}
	lang, items := s.compositionDataSourceCatalog(r.URL.Query().Get("lang"))
	writeJSON(w, http.StatusOK, map[string]any{
		"lang": lang, "items": items,
		"data_scope": "published",
	})
}

// compositionDataSourceCatalog is shared by the API and the manual composer,
// so neither surface invents content keys, fields, sorts, or item limits.
func (s *Server) compositionDataSourceCatalog(requestedLang string) (string, []compositionDataSourceDescriptor) {
	lang := strings.TrimSpace(requestedLang)
	if lang == "" || !s.langEnabled(lang) {
		lang = s.defaultLang()
	}
	types := []*ContentType{contentTypeByKey("post")}
	types = append(types, s.activeExtContentTypes()...)
	sort.Slice(types, func(i, j int) bool { return types[i].Key < types[j].Key })

	sorts := make([]string, 0, len(compositionSorts))
	for value := range compositionSorts {
		sorts = append(sorts, value)
	}
	sort.Strings(sorts)

	items := make([]compositionDataSourceDescriptor, 0, len(types))
	for _, ct := range types {
		if ct == nil {
			continue
		}
		fields := make([]compositionDataSourceField, 0, len(compositionBaseBindingFields)+len(ct.Fields))
		baseKeys := make([]string, 0, len(compositionBaseBindingFields))
		for key := range compositionBaseBindingFields {
			baseKeys = append(baseKeys, key)
		}
		sort.Strings(baseKeys)
		for _, key := range baseKeys {
			fields = append(fields, compositionDataSourceField{
				Key: key, Label: key, Type: compositionBaseBindingFields[key],
				Default: key == "title" || key == "slug" ||
					key == "excerpt" || key == "cover_image",
			})
		}
		for _, field := range ct.Fields {
			if field.Structural || field.Type == FieldRelation {
				continue
			}
			fields = append(fields, compositionDataSourceField{
				Key: field.Key, Label: field.Label(lang), Type: string(field.Type),
				Required: field.Required,
			})
		}
		items = append(items, compositionDataSourceDescriptor{
			Key: ct.Key, Label: ct.Name(lang), URLPrefix: ct.URLPrefix,
			HasCategory: ct.HasCategory, Fields: fields,
			Sorts: append([]string(nil), sorts...), MaxItems: CompositionLimits.MaxBindingLimit,
		})
	}
	return lang, items
}

func (s *Server) apiCompositionBindingPreview(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requirePagePlatformScope(w, r, apiScopePageProjectsRead); !ok {
		return
	}
	var input compositionBindingPreviewInput
	if !decodeAPIJSON(w, r, &input) {
		return
	}
	lang := strings.TrimSpace(input.Lang)
	if lang == "" {
		lang = s.defaultLang()
	}
	if !s.langEnabled(lang) {
		apiError(w, http.StatusUnprocessableEntity, "language_invalid", "请求语种未在当前站点启用。")
		return
	}

	var validation CompositionManifestValidation
	if len(input.Manifest) > 0 {
		if input.Binding != nil || input.ComponentType != "" || input.SectionID != "" {
			apiError(w, http.StatusBadRequest, "binding_preview_ambiguous",
				"manifest 与单个 binding 预览参数不能同时提交。")
			return
		}
		validation = s.NormalizeAndValidateCompositionManifest(input.Manifest, lang)
	} else {
		if input.Binding == nil {
			apiError(w, http.StatusBadRequest, "binding_required", "必须提交 manifest 或 binding。")
			return
		}
		componentType := strings.TrimSpace(input.ComponentType)
		if componentType == "" {
			switch input.Binding.Source {
			case "post":
				componentType = "posts.grid"
			case "product":
				componentType = "products.grid"
			default:
				componentType = "custom_content.grid"
			}
		}
		sectionID := strings.TrimSpace(input.SectionID)
		if sectionID == "" {
			sectionID = "binding-preview"
		}
		manifest := CompositionManifest{
			SchemaVersion: CompositionManifestVersion,
			Mode:          store.PageModeComposition,
			Shell:         CompositionShell{Mode: store.PageShellNone},
			Theme:         CompositionTheme{Inherit: true, Tokens: map[string]string{}},
			Layout: CompositionLayout{
				ContentMaxWidth: "wide", SectionGap: "comfortable",
			},
			Sections: []CompositionSection{{
				ID: sectionID, Type: componentType,
				Props:   json.RawMessage(`{"show_excerpt":true}`),
				Binding: input.Binding,
			}},
		}
		raw, err := json.Marshal(manifest)
		if err != nil {
			apiError(w, http.StatusInternalServerError, "binding_preview_failed", "创建绑定预览失败。")
			return
		}
		validation = s.NormalizeAndValidateCompositionManifest(raw, lang)
	}
	if !validation.Valid {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"valid": false, "diagnostics": validation.Diagnostics,
		})
		return
	}

	resolved, err := s.ResolveCompositionBindings(
		r.Context(), validation.Manifest, lang, CompositionBindingPublishedOnly,
	)
	if err != nil {
		diagnostics := compositionDiagnosticsFromError(err)
		if resolved != nil && len(resolved.Diagnostics) > 0 {
			diagnostics = resolved.Diagnostics
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"valid": false, "diagnostics": diagnostics,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"valid":              true,
		"lang":               lang,
		"manifest_hash":      validation.ManifestHash,
		"data_snapshot_hash": resolved.SnapshotHash,
		"bindings":           resolved.BySection,
		"diagnostics":        resolved.Diagnostics,
	})
}
