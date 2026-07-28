package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

func compositionManifestJSON(t *testing.T, shell string, sections []map[string]any) []byte {
	t.Helper()
	value := map[string]any{
		"schema_version": 1,
		"mode":           "composition",
		"shell": map[string]any{
			"mode":          shell,
			"sticky_header": true,
		},
		"theme": map[string]any{"inherit": true, "tokens": map[string]any{}},
		"layout": map[string]any{
			"content_max_width": "wide",
			"section_gap":       "comfortable",
		},
		"sections": sections,
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return raw
}

func TestCompositionGridComponentsUseInnerResponsiveGrid(t *testing.T) {
	rendered, err := renderCompositionDocument(compositionDocumentView{})
	if err != nil {
		t.Fatal(err)
	}
	html := string(rendered)
	const innerGridOverride = ".cmp-features-grid,.cmp-content-cards,.cmp-posts-grid,.cmp-products-grid,.cmp-custom-content-grid,.cmp-layout-columns{display:block}"
	if !strings.Contains(html, innerGridOverride) {
		t.Fatalf("composition document missing inner-grid override")
	}
	if strings.Count(html, "<style>") != 1 {
		t.Fatalf("composition document style blocks = %d, want 1", strings.Count(html, "<style>"))
	}
}

func TestCompositionDocumentInheritsSiteThemeVariablesAndTweaks(t *testing.T) {
	rendered, err := renderCompositionDocument(compositionDocumentView{
		Theme: "answer-desk-dark",
		ThemeStyle: compositionResolvedThemeCSS(
			"--w-wide:1240px;--accent:#66aaff;--radius:7px;",
			CompositionTheme{Inherit: true, Tokens: map[string]string{
				"color.accent": "#ff5500",
			}},
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	html := string(rendered)
	for _, required := range []string{
		"--cmp-surface:var(--surface,#fff)",
		"--cmp-font-body:var(--sans,system-ui,sans-serif)",
		"--cmp-font-display:var(--serif,var(--sans,system-ui,sans-serif))",
		"--cmp-radius-card:var(--radius,12px)",
		"--cmp-shadow-card:var(--shadow,none)",
		"--cmp-width-content:var(--w-wide,1240px)",
		"--w-wide:1240px;--accent:#66aaff;--radius:7px;--cmp-accent:#ff5500;",
	} {
		if !strings.Contains(html, required) {
			t.Errorf("composition theme output missing %q", required)
		}
	}
}

func compositionHeroSection(title string) map[string]any {
	return map[string]any{
		"id": "hero-main", "type": "hero.split",
		"props": map[string]any{
			"eyebrow": "Launch", "title": title, "description": "Page description",
			"primary_action": map[string]any{"label": "Read", "href": "#posts"},
		},
		"responsive": map[string]any{
			"mobile": map[string]any{"layout": "stack", "columns": 1, "media_position": "after-content"},
		},
	}
}

func compositionPostCardsSection(fields ...string) map[string]any {
	if len(fields) == 0 {
		fields = []string{"title", "slug", "excerpt", "cover_image", "updated_at"}
	}
	return map[string]any{
		"id": "posts", "type": "posts.grid",
		"props": map[string]any{
			"title": "Latest", "show_excerpt": true, "empty_state": "Nothing published.",
		},
		"binding": map[string]any{
			"source": "post",
			"filter": map[string]any{"status": "published"},
			"sort":   "-published_at", "limit": 6, "fields": fields,
			"update_mode": "live", "missing_policy": "placeholder",
		},
	}
}

func createCompositionProject(
	t *testing.T,
	s *Server,
	raw []byte,
	status string,
) (*store.Post, *store.PageProject, *store.PageProjectRevision) {
	t.Helper()
	pageID, err := s.store.CreatePost(&store.Post{
		Type: "page", Lang: "zh", Slug: "campaign", Title: "Campaign",
		Excerpt: "Campaign summary", Status: status, EditorMode: "markdown",
	})
	if err != nil {
		t.Fatalf("create page: %v", err)
	}
	page, _ := s.store.GetPostByID(pageID)
	validation := s.NormalizeAndValidateCompositionManifest(raw, "zh")
	if !validation.Valid {
		t.Fatalf("manifest invalid: %+v", validation.Diagnostics)
	}
	project, err := s.store.CreatePageProject(store.CreatePageProjectInput{
		PostID: pageID, Mode: store.PageModeComposition, SchemaVersion: 1,
		ShellMode: validation.Manifest.Shell.Mode, CreatedBy: store.PageOriginAdmin,
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	meta, err := store.PageRevisionMetaFromPost(page).CanonicalJSON()
	if err != nil {
		t.Fatalf("page meta: %v", err)
	}
	revision, _, err := s.store.CreatePageProjectRevision(store.CreatePageRevisionInput{
		ProjectID: project.ID, BaseRevisionID: 0, RevisionKind: store.PageRevisionComposition,
		PageMetaJSON: meta, ManifestJSON: validation.CanonicalJSON,
		Origin: store.PageOriginAdmin, ActorID: "test-admin", Summary: "test composition",
		ValidationJSON: `{"valid":true}`,
	})
	if err != nil {
		t.Fatalf("create revision: %v", err)
	}
	project, _ = s.store.GetPageProject(project.ID)
	return page, project, revision
}

func publishCompositionProject(
	t *testing.T,
	s *Server,
	project *store.PageProject,
	revision *store.PageProjectRevision,
	dataHash string,
) {
	t.Helper()
	build, err := s.store.CreatePageBuild(store.CreatePageBuildInput{
		ProjectID: project.ID, RevisionID: revision.ID, Status: store.PageBuildReady,
		DiagnosticsJSON: `[]`, RuntimeVersion: compositionRuntimeVersion,
	})
	if err != nil {
		t.Fatalf("create ready build: %v", err)
	}
	_, _, err = s.store.PublishPageProject(store.PublishPageProjectInput{
		ProjectID: project.ID, RevisionID: revision.ID, BuildID: build.ID,
		ExpectedWorkingRevisionID: revision.ID, Action: store.PagePublicationPublish,
		ActorID: "test-admin", Origin: store.PageOriginAdmin, RequestID: "publish-composition",
		DataSnapshotHash: dataHash,
	})
	if err != nil {
		t.Fatalf("publish project: %v", err)
	}
}

func TestCompositionManifestNormalizesRegistryAndResponsiveDefaults(t *testing.T) {
	raw := compositionManifestJSON(t, "site", []map[string]any{compositionHeroSection("A real launch")})
	first, canonical, hash, err := NormalizeCompositionManifest(raw)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if first.Sections[0].Responsive.Desktop.Layout != "split" ||
		first.Sections[0].Responsive.Tablet.Columns != 2 ||
		first.Sections[0].Responsive.Mobile.Columns != 1 {
		t.Fatalf("responsive defaults not normalized: %+v", first.Sections[0].Responsive)
	}
	_, secondCanonical, secondHash, err := NormalizeCompositionManifest([]byte(canonical))
	if err != nil {
		t.Fatalf("normalize canonical: %v", err)
	}
	if canonical != secondCanonical || hash != secondHash || len(hash) != 64 {
		t.Fatalf("normalization/hash is unstable:\n%s\n%s\n%s\n%s", canonical, secondCanonical, hash, secondHash)
	}

	registry := CompositionComponentRegistry()
	seen := map[string]bool{}
	for _, definition := range registry {
		seen[definition.Type] = true
		if definition.Version != 1 || definition.Renderer == "" ||
			len(definition.Properties) == 0 || len(definition.Accessibility) == 0 {
			t.Fatalf("incomplete registry entry: %+v", definition)
		}
	}
	for _, required := range []string{
		"hero.centered", "hero.split", "text.rich", "media.image", "features.grid",
		"content.cards", "posts.grid", "products.grid", "custom_content.grid",
		"faq.accordion", "cta.banner", "form.contact",
	} {
		if !seen[required] {
			t.Fatalf("registry missing %s", required)
		}
	}
}

func TestCompositionDraftBindingAccessFailsClosed(t *testing.T) {
	s := newTestPublicServer(t, "")
	raw := compositionManifestJSON(
		t, "none", []map[string]any{compositionPostCardsSection("title", "slug")},
	)
	manifest, _, _, err := NormalizeCompositionManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ResolveCompositionBindings(
		context.Background(), manifest, "zh", CompositionBindingAuthorizedDraftData,
	)
	if err == nil || !strings.Contains(err.Error(), "草稿数据绑定尚未建立授权协议") {
		t.Fatalf("authorized draft access should fail closed: %v", err)
	}
}

func TestCompositionManifestRejectsUnknownCodeURLsAndLimits(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		code string
	}{
		{
			name: "unknown top-level field",
			raw:  []byte(`{"schema_version":1,"mode":"composition","shell":{"mode":"site","sticky_header":false},"theme":{"inherit":true,"tokens":{}},"layout":{"content_max_width":"wide","section_gap":"comfortable"},"sections":[],"script":"alert(1)"}`),
			code: "manifest_invalid",
		},
		{
			name: "unknown component",
			raw: compositionManifestJSON(t, "site", []map[string]any{{
				"id": "bad", "type": "html.raw", "props": map[string]any{"html": "<script>alert(1)</script>"},
			}}),
			code: "component_unknown",
		},
		{
			name: "javascript action",
			raw: compositionManifestJSON(t, "site", []map[string]any{{
				"id": "hero", "type": "hero.centered",
				"props": map[string]any{
					"title": "Unsafe", "primary_action": map[string]any{"label": "Run", "href": "javaScript:alert(1)"},
				},
			}}),
			code: "url_unsafe",
		},
		{
			name: "markdown javascript",
			raw: compositionManifestJSON(t, "site", []map[string]any{{
				"id": "copy", "type": "text.rich",
				"props": map[string]any{"body": `[run](javascript:alert(1))`},
			}}),
			code: "url_unsafe",
		},
		{
			name: "markdown unmanaged image",
			raw: compositionManifestJSON(t, "site", []map[string]any{{
				"id": "copy", "type": "text.rich",
				"props": map[string]any{"body": `![tracking](https://evil.test/pixel)`},
			}}),
			code: "url_unsafe",
		},
		{
			name: "theme css injection",
			raw:  []byte(`{"schema_version":1,"mode":"composition","shell":{"mode":"site","sticky_header":false},"theme":{"inherit":false,"tokens":{"color.accent":"red;}</style><script>alert(1)</script>"}},"layout":{"content_max_width":"wide","section_gap":"comfortable"},"sections":[{"id":"hero","type":"hero.centered","props":{"title":"x"}}]}`),
			code: "manifest_invalid",
		},
		{
			name: "duplicate id",
			raw: compositionManifestJSON(t, "site", []map[string]any{
				compositionHeroSection("One"),
				{"id": "hero-main", "type": "text.rich", "props": map[string]any{"body": "Two"}},
			}),
			code: "manifest_invalid",
		},
		{
			name: "unimplemented release snapshot",
			raw: compositionManifestJSON(t, "site", []map[string]any{{
				"id": "posts", "type": "posts.grid",
				"props": map[string]any{"title": "Posts"},
				"binding": map[string]any{
					"source": "post", "filter": map[string]any{"status": "published"},
					"sort": "-published_at", "limit": 4, "fields": []string{"title"},
					"update_mode": "release_snapshot", "missing_policy": "placeholder",
				},
			}}),
			code: "binding_update_mode_unavailable",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := NormalizeCompositionManifest(tc.raw)
			if err == nil {
				t.Fatal("manifest unexpectedly accepted")
			}
			diagnostics := compositionDiagnosticsFromError(err)
			if len(diagnostics) == 0 || diagnostics[0].Code != tc.code {
				t.Fatalf("diagnostics = %+v, want %s", diagnostics, tc.code)
			}
		})
	}
}

func TestCompositionRichTextAndPlainTextRenderWithoutXSS(t *testing.T) {
	s := newTestPublicServer(t, "")
	raw := compositionManifestJSON(t, "none", []map[string]any{
		{
			"id": "hero", "type": "hero.centered",
			"props": map[string]any{"title": `<img src=x onerror="alert(1)">`},
		},
		{
			"id": "copy", "type": "text.rich",
			"props": map[string]any{"body": `<script>alert("raw")</script>` + "\n\n**safe body**"},
		},
	})
	_, project, revision := createCompositionProject(t, s, raw, "draft")
	rendered, err := s.RenderCompositionRevision(
		httptest.NewRequest(http.MethodGet, "/preview", nil),
		project, revision, true, CompositionBindingPublishedOnly,
	)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	body := string(rendered.DocumentHTML)
	if strings.Contains(body, `<script>alert("raw")</script>`) ||
		strings.Contains(body, `<img src=x onerror=`) {
		t.Fatalf("rendered executable manifest content: %s", body)
	}
	for _, want := range []string{`&lt;img src=x onerror=`, `<!-- raw HTML omitted -->`, `<strong>safe body</strong>`, `noindex, nofollow`} {
		if !strings.Contains(body, want) {
			t.Fatalf("render missing %q: %s", want, body)
		}
	}
}

func TestCompositionBindingPolicyRejectsInvalidSourcesFieldsAndQueries(t *testing.T) {
	s := newTestPublicServer(t, "")
	cases := []struct {
		name    string
		section map[string]any
	}{
		{
			name: "disabled product",
			section: map[string]any{
				"id": "products", "type": "products.grid", "props": map[string]any{"title": "Products"},
				"binding": map[string]any{
					"source": "product", "filter": map[string]any{"status": "published"},
					"sort": "-published_at", "limit": 4, "fields": []string{"title"},
				},
			},
		},
		{
			name: "private field",
			section: map[string]any{
				"id": "posts", "type": "posts.grid", "props": map[string]any{"title": "Posts"},
				"binding": map[string]any{
					"source": "post", "filter": map[string]any{"status": "published"},
					"sort": "-published_at", "limit": 4, "fields": []string{"title", "content"},
				},
			},
		},
		{
			name: "sql sort",
			section: map[string]any{
				"id": "posts", "type": "posts.grid", "props": map[string]any{"title": "Posts"},
				"binding": map[string]any{
					"source": "post", "filter": map[string]any{"status": "published"},
					"sort": "published_at; DROP TABLE posts", "limit": 4, "fields": []string{"title"},
				},
			},
		},
		{
			name: "draft public query",
			section: map[string]any{
				"id": "posts", "type": "posts.grid", "props": map[string]any{"title": "Posts"},
				"binding": map[string]any{
					"source": "post", "filter": map[string]any{"status": "draft"},
					"sort": "-published_at", "limit": 4, "fields": []string{"title"},
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			validation := s.NormalizeAndValidateCompositionManifest(
				compositionManifestJSON(t, "site", []map[string]any{tc.section}), "zh",
			)
			if validation.Valid || !compositionHasErrors(validation.Diagnostics) {
				t.Fatalf("invalid binding accepted: %+v", validation)
			}
		})
	}
}

func TestCompositionBindingUsesLiveDataAndChangesSnapshot(t *testing.T) {
	s := newTestPublicServer(t, "")
	postID, err := s.store.CreatePost(&store.Post{
		Type: "post", Lang: "zh", Slug: "dynamic-news", Title: "Dynamic title one",
		Excerpt: "Live excerpt", CoverImage: "/uploads/live.webp", Status: "published",
	})
	if err != nil {
		t.Fatalf("create dynamic content: %v", err)
	}
	raw := compositionManifestJSON(t, "site", []map[string]any{
		compositionHeroSection("Campaign"),
		compositionPostCardsSection(),
	})
	_, project, revision := createCompositionProject(t, s, raw, "draft")

	first, err := s.RenderCompositionRevision(
		httptest.NewRequest(http.MethodGet, "/preview", nil),
		project, revision, true, CompositionBindingPublishedOnly,
	)
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	if !strings.Contains(string(first.BodyHTML), "Dynamic title one") {
		t.Fatalf("first render did not use real Store content: %s", first.BodyHTML)
	}
	firstHash := first.Build.DataSnapshotHash

	post, _ := s.store.GetPostByID(postID)
	post.Title = "Dynamic title two"
	post.Excerpt = "Changed in the CMS"
	if err := s.store.UpdatePost(post); err != nil {
		t.Fatalf("update bound content: %v", err)
	}
	second, err := s.RenderCompositionRevision(
		httptest.NewRequest(http.MethodGet, "/preview", nil),
		project, revision, true, CompositionBindingPublishedOnly,
	)
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if !strings.Contains(string(second.BodyHTML), "Dynamic title two") ||
		strings.Contains(string(second.BodyHTML), "Dynamic title one") {
		t.Fatalf("second render did not reflect current Store content: %s", second.BodyHTML)
	}
	if second.Build.DataSnapshotHash == firstHash {
		t.Fatalf("data snapshot hash did not change: %s", firstHash)
	}
	if second.Build.ManifestHash != first.Build.ManifestHash {
		t.Fatalf("data update unexpectedly changed manifest hash")
	}
}

func TestCompositionEmptyBindingPoliciesAreExplicit(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable empty product type: %v", err)
	}
	emptyProducts := func(policy string) map[string]any {
		return map[string]any{
			"id": "products", "type": "products.grid",
			"props": map[string]any{"title": "Products", "empty_state": "Nothing published."},
			"binding": map[string]any{
				"source": "product", "filter": map[string]any{"status": "published"},
				"sort": "-published_at", "limit": 6, "fields": []string{"title"},
				"update_mode": "live", "missing_policy": policy,
			},
		}
	}
	placeholder := emptyProducts("placeholder")
	raw := compositionManifestJSON(t, "site", []map[string]any{placeholder})
	_, project, revision := createCompositionProject(t, s, raw, "draft")
	rendered, err := s.RenderCompositionRevision(
		httptest.NewRequest(http.MethodGet, "/preview", nil),
		project, revision, true, CompositionBindingPublishedOnly,
	)
	if err != nil {
		t.Fatalf("placeholder render: %v", err)
	}
	if !strings.Contains(string(rendered.BodyHTML), "Nothing published.") {
		t.Fatalf("placeholder empty state missing: %s", rendered.BodyHTML)
	}

	s2 := newTestPublicServer(t, "")
	if err := s2.store.SetSetting(enabledContentTypesKey, "product"); err != nil {
		t.Fatalf("enable product type for blocking case: %v", err)
	}
	block := emptyProducts("block")
	blockRaw := compositionManifestJSON(t, "site", []map[string]any{block})
	_, blockProject, blockRevision := createCompositionProject(t, s2, blockRaw, "draft")
	build, err := s2.ValidateCompositionBuild(
		context.Background(), blockProject, blockRevision, CompositionBindingPublishedOnly,
	)
	if err == nil || build.Valid {
		t.Fatalf("blocking empty binding should fail build: %+v, %v", build, err)
	}
}

func TestCompositionAssetsAreImmutableAndProjectBound(t *testing.T) {
	s := newTestPublicServer(t, "")
	hash := strings.Repeat("a", 64)
	base := compositionManifestJSON(t, "none", []map[string]any{{
		"id": "hero", "type": "hero.split",
		"props": map[string]any{"title": "Asset hero"},
		"media": map[string]any{"asset_id": 1, "sha256": hash},
	}})
	_, project, _ := createCompositionProject(t, s, compositionManifestJSON(t, "none", []map[string]any{
		compositionHeroSection("temporary"),
	}), "draft")
	asset, _, err := s.store.CreatePageAsset(store.CreatePageAssetInput{
		ProjectID: project.ID, LogicalKey: "hero", StorageRef: "page-projects/hero.webp",
		MediaType: "image/webp", ByteSize: 10, SHA256: hash, Origin: "upload",
		ProvenanceJSON: `{}`, Width: 1200, Height: 800,
	})
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	var value map[string]any
	_ = json.Unmarshal(base, &value)
	value["sections"].([]any)[0].(map[string]any)["media"].(map[string]any)["asset_id"] = asset.ID
	base, _ = json.Marshal(value)

	validation := s.NormalizeAndValidateCompositionManifest(base, "zh")
	if !validation.Valid {
		t.Fatalf("asset manifest invalid: %+v", validation.Diagnostics)
	}
	pageID, _ := s.store.CreatePost(&store.Post{
		Type: "page", Lang: "zh", Slug: "asset-page", Title: "Asset Page", Status: "draft",
	})
	page, _ := s.store.GetPostByID(pageID)
	secondProject, _ := s.store.CreatePageProject(store.CreatePageProjectInput{
		PostID: pageID, Mode: store.PageModeComposition, SchemaVersion: 1,
		ShellMode: store.PageShellNone, CreatedBy: store.PageOriginAdmin,
	})
	meta, _ := store.PageRevisionMetaFromPost(page).CanonicalJSON()
	revision, _, _ := s.store.CreatePageProjectRevision(store.CreatePageRevisionInput{
		ProjectID: secondProject.ID, RevisionKind: store.PageRevisionComposition,
		PageMetaJSON: meta, ManifestJSON: validation.CanonicalJSON, Origin: store.PageOriginAdmin,
	})
	secondProject, _ = s.store.GetPageProject(secondProject.ID)
	build, err := s.ValidateCompositionBuild(
		context.Background(), secondProject, revision, CompositionBindingPublishedOnly,
	)
	if err == nil || build.Valid {
		t.Fatalf("cross-project asset was accepted: %+v", build)
	}
}

func TestCompositionSiteShellPreviewSEOAndResponsiveOutput(t *testing.T) {
	s := newTestPublicServer(t, "")
	if err := s.store.SetSetting("inject.head", `<script id="production-only">x</script>`); err != nil {
		t.Fatalf("set production injection: %v", err)
	}
	raw := compositionManifestJSON(t, "site", []map[string]any{
		compositionHeroSection("Responsive campaign"),
		{
			"id": "cta", "type": "cta.banner",
			"props": map[string]any{
				"title": "Talk to us", "action": map[string]any{"label": "Email", "href": "mailto:hello@example.test"},
			},
			"responsive": map[string]any{
				"desktop": map[string]any{"layout": "row", "columns": 2, "align": "center", "media_position": "after-content"},
				"tablet":  map[string]any{"layout": "row", "columns": 2, "align": "start", "media_position": "after-content"},
				"mobile":  map[string]any{"layout": "stack", "columns": 1, "align": "start", "media_position": "after-content"},
			},
		},
	})
	_, project, revision := createCompositionProject(t, s, raw, "draft")
	rendered, err := s.RenderCompositionRevision(
		httptest.NewRequest(http.MethodGet, "/preview", nil),
		project, revision, true, CompositionBindingPublishedOnly,
	)
	if err != nil {
		t.Fatalf("preview render: %v", err)
	}
	body := string(rendered.DocumentHTML)
	for _, want := range []string{
		`class="cmp-shell-header is-sticky"`,
		`data-layout-desktop="row"`, `data-columns-mobile="1"`,
		`<meta name="robots" content="noindex, nofollow">`,
		`data-page-mode="composition"`, `草稿预览`,
		`.cmp-cta-banner>.cmp-actions{align-self:start;align-items:center;justify-content:flex-start;margin-top:1rem}`,
		`.cmp-cta-banner:is(.cmp-d-grid,.cmp-d-split,.cmp-d-row):not(.cmp-d-cols-1)>.cmp-actions{align-self:center;margin-top:0}`,
		`.cmp-cta-banner:is(.cmp-t-grid,.cmp-t-split,.cmp-t-row):not(.cmp-t-cols-1)>.cmp-actions{align-self:center;margin-top:0}`,
		`.cmp-cta-banner:is(.cmp-m-grid,.cmp-m-split,.cmp-m-row):not(.cmp-m-cols-1)>.cmp-actions{align-self:center;margin-top:0}`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("preview missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "production-only") {
		t.Fatalf("preview must not inject production custom code")
	}
}

func TestCompositionPublishedDispatchAndWriteAreFailClosed(t *testing.T) {
	s := newTestPublicServer(t, "")
	postID, err := s.store.CreatePost(&store.Post{
		Type: "post", Lang: "zh", Slug: "published-card", Title: "Published card",
		Status: "published",
	})
	if err != nil || postID == 0 {
		t.Fatalf("seed card: %v", err)
	}
	raw := compositionManifestJSON(t, "minimal", []map[string]any{
		compositionHeroSection("Published composition"),
		compositionPostCardsSection("title", "slug"),
	})
	page, project, revision := createCompositionProject(t, s, raw, "draft")
	preflight, err := s.ValidateCompositionBuild(context.Background(), project, revision, CompositionBindingPublishedOnly)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	publishCompositionProject(t, s, project, revision, preflight.DataSnapshotHash)
	page, _ = s.store.GetPostByID(page.ID)

	rendered, handled, err := s.RenderPublishedComposition(
		httptest.NewRequest(http.MethodGet, "/zh/campaign/", nil), page,
	)
	if err != nil || !handled {
		t.Fatalf("published dispatch = handled %v, err %v", handled, err)
	}
	body := string(rendered.DocumentHTML)
	if !strings.Contains(body, "Published composition") ||
		!strings.Contains(body, "Published card") ||
		!strings.Contains(body, `<meta name="robots" content="index, follow`) {
		t.Fatalf("published composition output incomplete: %s", body)
	}
	if strings.Contains(body, `<nav class="cmp-shell-nav"`) || strings.Contains(body, `<footer class="cmp-shell-footer"`) {
		t.Fatalf("minimal shell rendered full site chrome: %s", body)
	}

	rec := httptest.NewRecorder()
	s.WriteCompositionPage(rec, rendered, http.StatusOK)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") == "" ||
		rec.Header().Get("Cache-Control") != publicPageCacheControl {
		t.Fatalf("write response = %d %#v", rec.Code, rec.Header())
	}

	standardID, _ := s.store.CreatePost(&store.Post{
		Type: "page", Lang: "zh", Slug: "standard-remains", Title: "Standard", Status: "published",
	})
	standard, _ := s.store.GetPostByID(standardID)
	if _, handled, err := s.RenderPublishedComposition(nil, standard); handled || err != nil {
		t.Fatalf("standard page should continue legacy branch: handled=%v err=%v", handled, err)
	}
}

func TestCompositionContactFormUsesOnlyControlledEndpoint(t *testing.T) {
	s := newTestPublicServer(t, "")
	raw := compositionManifestJSON(t, "none", []map[string]any{{
		"id": "contact", "type": "form.contact",
		"props": map[string]any{
			"title": "Contact", "fields": []string{"name", "email", "message"},
			"submit_label": "Send", "privacy_label": "Privacy",
			"privacy_href": "/privacy",
		},
	}})
	_, project, revision := createCompositionProject(t, s, raw, "draft")
	rendered, err := s.RenderCompositionRevision(
		httptest.NewRequest(http.MethodGet, "/preview", nil),
		project, revision, true, CompositionBindingPublishedOnly,
	)
	if err != nil {
		t.Fatalf("render contact: %v", err)
	}
	body := string(rendered.BodyHTML)
	if !strings.Contains(body, `action="/api/forms/contact"`) ||
		!strings.Contains(body, `name="_project_id" value="`+strconv.FormatInt(project.ID, 10)+`"`) ||
		!strings.Contains(body, `name="_revision_id" value="`+strconv.FormatInt(revision.ID, 10)+`"`) ||
		!strings.Contains(body, `name="_section_id" value="contact"`) ||
		strings.Contains(body, "formaction=") || strings.Contains(body, "http://") {
		t.Fatalf("contact form target is not controlled: %s", body)
	}
}

func TestCompositionPreviewTicketInvalidatesWhenLiveDataChanges(t *testing.T) {
	s := newTestPublicServer(t, "")
	postID, err := s.store.CreatePost(&store.Post{
		Type: "post", Lang: "zh", Slug: "preview-bound-post",
		Title: "Preview snapshot one", Status: "published",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, project, revision := createCompositionProject(
		t,
		s,
		compositionManifestJSON(t, "none", []map[string]any{compositionPostCardsSection("title", "slug")}),
		"draft",
	)
	request := httptest.NewRequest(
		http.MethodPost,
		"http://preview.example/api/admin/v1/page-projects/1/preview-url",
		strings.NewReader(`{"revision_id":`+strconv.FormatInt(revision.ID, 10)+`}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	s.createCompositionPreviewURL(response, request, project)
	if response.Code != http.StatusCreated {
		t.Fatalf("create preview = %d %s", response.Code, response.Body.String())
	}
	var created struct {
		PreviewURL       string `json:"preview_url"`
		DataSnapshotHash string `json:"data_snapshot_hash"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil ||
		created.PreviewURL == "" || !validCompositionSHA256(created.DataSnapshotHash) {
		t.Fatalf("preview response = %+v err=%v", created, err)
	}

	first := httptest.NewRecorder()
	s.Handler().ServeHTTP(first, httptest.NewRequest(http.MethodGet, created.PreviewURL, nil))
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "Preview snapshot one") {
		t.Fatalf("first preview = %d %s", first.Code, first.Body.String())
	}
	post, err := s.store.GetPostByID(postID)
	if err != nil {
		t.Fatal(err)
	}
	post.Title = "Preview snapshot two"
	if err := s.store.UpdatePost(post); err != nil {
		t.Fatal(err)
	}
	stale := httptest.NewRecorder()
	s.Handler().ServeHTTP(stale, httptest.NewRequest(http.MethodGet, created.PreviewURL, nil))
	if stale.Code != http.StatusGone {
		t.Fatalf("data-stale preview = %d %s", stale.Code, stale.Body.String())
	}
}

func TestCompositionPreviewBindsExactReadyBuild(t *testing.T) {
	s := newTestPublicServer(t, "")
	_, err := s.store.CreatePost(&store.Post{
		Type: "post", Lang: "zh", Slug: "preview-build-bound-post",
		Title: "Build snapshot one", Status: "published",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, project, revision := createCompositionProject(
		t,
		s,
		compositionManifestJSON(t, "none", []map[string]any{
			compositionPostCardsSection("title", "slug"),
		}),
		"draft",
	)
	validated, err := s.ValidateCompositionBuild(
		context.Background(), project, revision, CompositionBindingPublishedOnly,
	)
	if err != nil {
		t.Fatalf("validate build: %v", err)
	}
	ready, err := s.store.CreatePageBuild(store.CreatePageBuildInput{
		ProjectID: project.ID, RevisionID: revision.ID, Status: store.PageBuildReady,
		ArtifactRef:  "composition:ssr/" + validated.RenderHash,
		ArtifactHash: validated.RenderHash, DiagnosticsJSON: `[]`,
		RuntimeVersion: compositionRuntimeVersion,
	})
	if err != nil {
		t.Fatalf("create ready build: %v", err)
	}
	create := func(buildID int64) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(
			http.MethodPost,
			"http://preview.example/api/admin/v1/page-projects/1/preview-url",
			strings.NewReader(
				`{"revision_id":`+strconv.FormatInt(revision.ID, 10)+
					`,"build_id":`+strconv.FormatInt(buildID, 10)+`}`,
			),
		)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		s.createCompositionPreviewURL(response, request, project)
		return response
	}
	queued, err := s.store.CreatePageBuild(store.CreatePageBuildInput{
		ProjectID: project.ID, RevisionID: revision.ID, Status: store.PageBuildQueued,
		DiagnosticsJSON: `[]`, RuntimeVersion: compositionRuntimeVersion,
	})
	if err != nil {
		t.Fatalf("create queued build: %v", err)
	}
	notReady := create(queued.ID)
	if notReady.Code != http.StatusConflict ||
		!strings.Contains(notReady.Body.String(), `"build_not_ready"`) {
		t.Fatalf("not-ready preview = %d %s", notReady.Code, notReady.Body.String())
	}

	foreignPageID, err := s.store.CreatePost(&store.Post{
		Type: "page", Lang: "zh", Slug: "foreign-build", Title: "Foreign build",
		Status: "draft", EditorMode: "markdown",
	})
	if err != nil {
		t.Fatalf("create foreign page: %v", err)
	}
	foreignPage, err := s.store.GetPostByID(foreignPageID)
	if err != nil {
		t.Fatalf("read foreign page: %v", err)
	}
	foreignManifest := s.NormalizeAndValidateCompositionManifest(
		compositionManifestJSON(t, "none", []map[string]any{
			compositionHeroSection("Foreign build"),
		}),
		"zh",
	)
	if !foreignManifest.Valid {
		t.Fatalf("foreign manifest: %+v", foreignManifest.Diagnostics)
	}
	foreignProject, err := s.store.CreatePageProject(store.CreatePageProjectInput{
		PostID: foreignPageID, Mode: store.PageModeComposition, SchemaVersion: 1,
		ShellMode: "none", CreatedBy: store.PageOriginAdmin,
	})
	if err != nil {
		t.Fatalf("create foreign project: %v", err)
	}
	foreignMeta, err := store.PageRevisionMetaFromPost(foreignPage).CanonicalJSON()
	if err != nil {
		t.Fatalf("foreign page meta: %v", err)
	}
	foreignRevision, _, err := s.store.CreatePageProjectRevision(store.CreatePageRevisionInput{
		ProjectID: foreignProject.ID, RevisionKind: store.PageRevisionComposition,
		PageMetaJSON: foreignMeta, ManifestJSON: foreignManifest.CanonicalJSON,
		Origin: store.PageOriginAdmin, ActorID: "test-admin",
	})
	if err != nil {
		t.Fatalf("create foreign revision: %v", err)
	}
	foreignProject, err = s.store.GetPageProject(foreignProject.ID)
	if err != nil {
		t.Fatalf("refresh foreign project: %v", err)
	}
	foreignValidation, err := s.ValidateCompositionBuild(
		context.Background(), foreignProject, foreignRevision, CompositionBindingPublishedOnly,
	)
	if err != nil {
		t.Fatalf("validate foreign build: %v", err)
	}
	foreignBuild, err := s.store.CreatePageBuild(store.CreatePageBuildInput{
		ProjectID: foreignProject.ID, RevisionID: foreignRevision.ID,
		Status: store.PageBuildReady, ArtifactRef: "composition:ssr/" + foreignValidation.RenderHash,
		ArtifactHash: foreignValidation.RenderHash, DiagnosticsJSON: `[]`,
		RuntimeVersion: compositionRuntimeVersion,
	})
	if err != nil {
		t.Fatalf("create foreign build: %v", err)
	}
	wrongProject := create(foreignBuild.ID)
	if wrongProject.Code != http.StatusNotFound ||
		!strings.Contains(wrongProject.Body.String(), `"build_not_found"`) {
		t.Fatalf("foreign build preview = %d %s", wrongProject.Code, wrongProject.Body.String())
	}

	first := create(ready.ID)
	var created struct {
		PreviewURL string `json:"preview_url"`
		BuildID    int64  `json:"build_id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil ||
		first.Code != http.StatusCreated || created.PreviewURL == "" ||
		created.BuildID != ready.ID {
		t.Fatalf("build-bound preview = %d %s", first.Code, first.Body.String())
	}
	rendered := httptest.NewRecorder()
	s.Handler().ServeHTTP(
		rendered, httptest.NewRequest(http.MethodGet, created.PreviewURL, nil),
	)
	if rendered.Code != http.StatusOK {
		t.Fatalf("initial build-bound preview = %d %s", rendered.Code, rendered.Body.String())
	}
	if err := s.store.SetSetting("theme", "answer-desk-dark"); err != nil {
		t.Fatal(err)
	}
	expired := httptest.NewRecorder()
	s.Handler().ServeHTTP(
		expired, httptest.NewRequest(http.MethodGet, created.PreviewURL, nil),
	)
	if expired.Code != http.StatusGone {
		t.Fatalf("theme-stale preview URL = %d %s", expired.Code, expired.Body.String())
	}
	stale := create(ready.ID)
	if stale.Code != http.StatusConflict ||
		!strings.Contains(stale.Body.String(), `"build_stale"`) {
		t.Fatalf("stale build preview = %d %s", stale.Code, stale.Body.String())
	}
	invalidRequest := httptest.NewRequest(
		http.MethodPost,
		"http://preview.example/api/admin/v1/page-projects/1/preview-url",
		strings.NewReader(`{"revision_id":-1,"build_id":-1}`),
	)
	invalidRequest.Header.Set("Content-Type", "application/json")
	invalid := httptest.NewRecorder()
	s.createCompositionPreviewURL(invalid, invalidRequest, project)
	if invalid.Code != http.StatusBadRequest ||
		!strings.Contains(invalid.Body.String(), `"invalid_preview_target"`) {
		t.Fatalf("negative preview target = %d %s", invalid.Code, invalid.Body.String())
	}
}

func TestCompositionAssetPathBypassesLocaleRedirect(t *testing.T) {
	s := newTestPublicServer(t, "")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodGet,
			"/page-assets/999/"+strings.Repeat("a", 64),
			nil,
		),
	)
	if response.Code != http.StatusNotFound || response.Header().Get("Location") != "" {
		t.Fatalf("immutable asset path entered locale routing: %d headers=%v",
			response.Code, response.Header())
	}
}
