package web

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cms.ccvar.com/internal/store"
)

func pagePlatformTestPNG(t *testing.T, value uint8) []byte {
	t.Helper()
	var raw bytes.Buffer
	pixel := image.NewRGBA(image.Rect(0, 0, 1, 1))
	pixel.SetRGBA(0, 0, color.RGBA{R: value, G: 20, B: 40, A: 255})
	if err := png.Encode(&raw, pixel); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func pagePlatformAssetHTTPRequest(
	t *testing.T,
	token, requestPath, etag, requestID string,
	raw []byte,
	extraFields map[string]string,
) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range extraFields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("file", "hero.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, requestPath, bytes.NewReader(body.Bytes()))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set(pagePlatformConcurrencyHeader, etag)
	request.Header.Set(pagePlatformIdempotencyHeader, requestID)
	return request
}

func pagePlatformAssetRequest(
	t *testing.T,
	mux http.Handler,
	token, requestPath, etag, requestID string,
	raw []byte,
	extraFields map[string]string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := pagePlatformAssetHTTPRequest(
		t, token, requestPath, etag, requestID, raw, extraFields,
	)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func pagePlatformTestMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	s.registerPagePlatformAPIRoutes(mux)
	return mux
}

func compositionPublicTestMux(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	s.registerCompositionPublicRoutes(mux)
	return mux
}

func pagePlatformAPIRequest(
	t *testing.T,
	mux http.Handler,
	token, method, requestPath string,
	body any,
	etag, requestID string,
) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
	}
	request := httptest.NewRequest(method, requestPath, bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if etag != "" {
		request.Header.Set(pagePlatformConcurrencyHeader, etag)
	}
	if requestID != "" {
		request.Header.Set(pagePlatformIdempotencyHeader, requestID)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func createPageProjectForAPITest(t *testing.T, s *Server, mux http.Handler, token string) (int64, *store.Post, *store.PageProject, *store.PageProjectRevision) {
	t.Helper()
	page := &store.Post{
		Type: "page", Lang: "zh", Slug: "page-platform-api",
		Title: "页面工程 API", Content: "转换前正文", Status: "draft",
		TransGroup: "zh:page-platform-api",
	}
	id, err := s.store.CreatePost(page)
	if err != nil {
		t.Fatalf("create page: %v", err)
	}
	page, err = s.store.GetPostByID(id)
	if err != nil {
		t.Fatalf("read page: %v", err)
	}
	plan := pagePlatformAPIRequest(t, mux, token, http.MethodPost,
		"/api/admin/v1/pages/"+strconv.FormatInt(id, 10)+"/convert-plan", map[string]any{}, "", "")
	if plan.Code != http.StatusOK {
		t.Fatalf("convert plan = %d %s", plan.Code, plan.Body.String())
	}
	etag := plan.Header().Get("ETag")
	if !strings.HasPrefix(etag, `"content-`) {
		t.Fatalf("convert ETag = %q", etag)
	}
	convert := pagePlatformAPIRequest(t, mux, token, http.MethodPost,
		"/api/admin/v1/pages/"+strconv.FormatInt(id, 10)+"/convert",
		map[string]any{"mode": "composition", "schema_version": 1, "shell_mode": "site"},
		etag, "convert-1")
	if convert.Code != http.StatusCreated {
		t.Fatalf("convert = %d %s", convert.Code, convert.Body.String())
	}
	var envelope pageProjectEnvelope
	if err := json.Unmarshal(convert.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode convert: %v", err)
	}
	if envelope.Project == nil || envelope.Revision == nil {
		t.Fatalf("convert envelope = %#v", envelope)
	}
	return id, page, envelope.Project, envelope.Revision
}

func TestPagePlatformAPIScopeIsolationAndMutationHeaders(t *testing.T) {
	legacy, legacyToken := newTestAutomationServer(t, apiScopeContentWrite)
	legacyMux := pagePlatformTestMux(legacy)
	pageID, err := legacy.store.CreatePost(&store.Post{
		Type: "page", Lang: "zh", Slug: "legacy-no-inherit", Title: "legacy", Status: "draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	denied := pagePlatformAPIRequest(t, legacyMux, legacyToken, http.MethodPost,
		"/api/admin/v1/pages/"+strconv.FormatInt(pageID, 10)+"/convert-plan", map[string]any{}, "", "")
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "missing_scope") {
		t.Fatalf("legacy scope = %d %s", denied.Code, denied.Body.String())
	}

	s, token := newTestAutomationServer(t, strings.Join([]string{
		apiScopePageProjectsRead, apiScopePageProjectsWrite,
	}, ","))
	mux := pagePlatformTestMux(s)
	id, err := s.store.CreatePost(&store.Post{
		Type: "page", Lang: "zh", Slug: "headers", Title: "headers", Status: "draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	uri := "/api/admin/v1/pages/" + strconv.FormatInt(id, 10) + "/convert"
	missingIdempotency := pagePlatformAPIRequest(t, mux, token, http.MethodPost, uri,
		map[string]any{"mode": "composition"}, `"content-x"`, "")
	if missingIdempotency.Code != http.StatusBadRequest ||
		!strings.Contains(missingIdempotency.Body.String(), "idempotency_key_required") {
		t.Fatalf("missing idempotency = %d %s", missingIdempotency.Code, missingIdempotency.Body.String())
	}
	missingETag := pagePlatformAPIRequest(t, mux, token, http.MethodPost, uri,
		map[string]any{"mode": "composition"}, "", "headers-1")
	if missingETag.Code != http.StatusPreconditionRequired ||
		!strings.Contains(missingETag.Body.String(), "if_match_required") {
		t.Fatalf("missing If-Match = %d %s", missingETag.Code, missingETag.Body.String())
	}
}

func TestPagePlatformAPIConvertIsSidecarAndIdempotent(t *testing.T) {
	s, token := newTestAutomationServer(t, strings.Join([]string{
		apiScopePageProjectsRead, apiScopePageProjectsWrite,
	}, ","))
	mux := pagePlatformTestMux(s)
	pageID, before, project, baseline := createPageProjectForAPITest(t, s, mux, token)
	if project.PostID != pageID || project.WorkingRevisionID != baseline.ID ||
		baseline.RevisionKind != store.PageRevisionStandardBaseline {
		t.Fatalf("project=%#v baseline=%#v", project, baseline)
	}
	after, err := s.store.GetPostByID(pageID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Content != before.Content || after.Status != before.Status ||
		after.Slug != before.Slug || after.Title != before.Title {
		t.Fatalf("conversion mutated old page: before=%#v after=%#v", before, after)
	}
	for _, prefix := range []string{"/api/admin/v1", "/api/platform/v1/sites/12"} {
		listed := pagePlatformAPIRequest(t, mux, token, http.MethodGet,
			prefix+"/page-projects?lang=zh&slug=page-platform-api&mode=composition",
			nil, "", "")
		if listed.Code != http.StatusOK {
			t.Fatalf("%s project discovery = %d %s", prefix, listed.Code, listed.Body.String())
		}
		var result struct {
			Items []pageProjectListItem `json:"items"`
			Total int                   `json:"total"`
		}
		if err := json.Unmarshal(listed.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Total != 1 || len(result.Items) != 1 ||
			result.Items[0].Project.ID != project.ID ||
			result.Items[0].Page.ID != pageID ||
			result.Items[0].Page.Slug != "page-platform-api" ||
			result.Items[0].ETag != project.ETag() ||
			!strings.Contains(result.Items[0].Links.AdminPath, strconv.FormatInt(pageID, 10)) {
			t.Fatalf("%s project discovery = %#v", prefix, result)
		}
	}

	retry := pagePlatformAPIRequest(t, mux, token, http.MethodPost,
		"/api/admin/v1/page-projects",
		map[string]any{
			"page_id": pageID, "mode": "composition",
			"schema_version": 1, "shell_mode": "site",
		},
		standardPageConversionETag(after), "convert-1")
	if retry.Code != http.StatusOK || retry.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("convert retry = %d headers=%v body=%s", retry.Code, retry.Header(), retry.Body.String())
	}

	get := pagePlatformAPIRequest(t, mux, token, http.MethodGet,
		"/api/platform/v1/sites/12/page-projects/"+strconv.FormatInt(project.ID, 10),
		nil, "", "")
	if get.Code != http.StatusOK || get.Header().Get("ETag") != project.ETag() {
		t.Fatalf("platform mirror get = %d ETag=%q body=%s", get.Code, get.Header().Get("ETag"), get.Body.String())
	}
}

func TestPagePlatformAPIRevisionConflictReplayAndRestore(t *testing.T) {
	s, token := newTestAutomationServer(t, strings.Join([]string{
		apiScopePageProjectsRead, apiScopePageProjectsWrite, apiScopePageProjectsBuild,
		apiScopePagePreviewRead,
	}, ","))
	mux := pagePlatformTestMux(s)
	_, _, project, baseline := createPageProjectForAPITest(t, s, mux, token)
	meta := map[string]any{
		"slug": "page-platform-api", "title": "新标题", "lang": "zh",
	}
	body := map[string]any{
		"base_revision_id": baseline.ID,
		"page_meta":        meta,
		"summary":          "第一次编排",
	}
	var manifest any
	if err := json.Unmarshal(
		compositionManifestJSON(t, "site", []map[string]any{compositionHeroSection("Hero")}),
		&manifest,
	); err != nil {
		t.Fatal(err)
	}
	body["manifest"] = manifest
	uri := "/api/admin/v1/page-projects/" + strconv.FormatInt(project.ID, 10) + "/revisions"
	create := pagePlatformAPIRequest(t, mux, token, http.MethodPost, uri, body, project.ETag(), "revision-1")
	if create.Code != http.StatusCreated {
		t.Fatalf("create revision = %d %s", create.Code, create.Body.String())
	}
	var created pageProjectEnvelope
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Revision == nil || created.Revision.ParentRevisionID != baseline.ID ||
		created.Project.WorkingRevisionID != created.Revision.ID {
		t.Fatalf("created = %#v", created)
	}

	replay := pagePlatformAPIRequest(t, mux, token, http.MethodPost, uri, body, project.ETag(), "revision-1")
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("revision replay = %d headers=%v body=%s", replay.Code, replay.Header(), replay.Body.String())
	}
	changed := map[string]any{}
	for key, value := range body {
		changed[key] = value
	}
	changed["summary"] = "同 key 的不同请求"
	idemConflict := pagePlatformAPIRequest(t, mux, token, http.MethodPost, uri, changed, project.ETag(), "revision-1")
	if idemConflict.Code != http.StatusConflict ||
		!strings.Contains(idemConflict.Body.String(), "idempotency_conflict") {
		t.Fatalf("idempotency conflict = %d %s", idemConflict.Code, idemConflict.Body.String())
	}
	stale := pagePlatformAPIRequest(t, mux, token, http.MethodPost, uri,
		map[string]any{
			"base_revision_id": baseline.ID, "page_meta": meta,
			"manifest": map[string]any{"schema_version": 1}, "summary": "stale",
		},
		project.ETag(), "revision-stale")
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "revision_conflict") {
		t.Fatalf("stale revision = %d %s", stale.Code, stale.Body.String())
	}

	current := created.Project
	validate := pagePlatformAPIRequest(t, mux, token, http.MethodPost,
		"/api/admin/v1/page-projects/"+strconv.FormatInt(project.ID, 10)+"/validate",
		map[string]any{"revision_id": created.Revision.ID}, current.ETag(), "")
	if validate.Code != http.StatusOK || !strings.Contains(validate.Body.String(), `"valid":true`) {
		t.Fatalf("validate = %d %s", validate.Code, validate.Body.String())
	}
	built := pagePlatformAPIRequest(t, mux, token, http.MethodPost,
		"/api/admin/v1/page-projects/"+strconv.FormatInt(project.ID, 10)+"/builds",
		map[string]any{"revision_id": created.Revision.ID}, current.ETag(), "composition-build-1")
	if built.Code != http.StatusCreated ||
		!strings.Contains(built.Body.String(), `"RuntimeVersion":"composition-v1"`) {
		t.Fatalf("build = %d %s", built.Code, built.Body.String())
	}
	var buildResult struct {
		Build struct {
			ID int64 `json:"ID"`
		} `json:"build"`
	}
	if err := json.Unmarshal(built.Body.Bytes(), &buildResult); err != nil ||
		buildResult.Build.ID <= 0 {
		t.Fatalf("decode build = %+v err=%v", buildResult, err)
	}
	preview := pagePlatformAPIRequest(t, mux, token, http.MethodPost,
		"/api/admin/v1/page-projects/"+strconv.FormatInt(project.ID, 10)+"/preview-url",
		map[string]any{
			"revision_id": created.Revision.ID,
			"build_id":    buildResult.Build.ID,
		}, current.ETag(), "")
	if preview.Code != http.StatusCreated {
		t.Fatalf("preview URL = %d %s", preview.Code, preview.Body.String())
	}
	var previewResult struct {
		URL     string `json:"preview_url"`
		BuildID int64  `json:"build_id"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &previewResult); err != nil ||
		previewResult.URL == "" || previewResult.BuildID != buildResult.Build.ID {
		t.Fatalf("decode preview URL: %+v err=%v", previewResult, err)
	}
	previewPage := httptest.NewRecorder()
	compositionPublicTestMux(s).ServeHTTP(
		previewPage, httptest.NewRequest(http.MethodGet, previewResult.URL, nil),
	)
	if previewPage.Code != http.StatusOK ||
		!strings.Contains(previewPage.Body.String(), "Hero") ||
		previewPage.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("signed composition preview = %d headers=%v body=%s",
			previewPage.Code, previewPage.Header(), previewPage.Body.String())
	}

	restore := pagePlatformAPIRequest(t, mux, token, http.MethodPost,
		"/api/admin/v1/page-projects/"+strconv.FormatInt(project.ID, 10)+"/restore",
		map[string]any{"revision_id": created.Revision.ID, "summary": "复制修订恢复"},
		current.ETag(), "restore-1")
	if restore.Code != http.StatusCreated {
		t.Fatalf("restore = %d %s", restore.Code, restore.Body.String())
	}
	var restored pageProjectEnvelope
	if err := json.Unmarshal(restore.Body.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Revision == nil || restored.Revision.ID == created.Revision.ID ||
		restored.Revision.ParentRevisionID != created.Revision.ID ||
		restored.Revision.Origin != store.PageOriginRestore {
		t.Fatalf("restored revision = %#v", restored.Revision)
	}
	restoreReplay := pagePlatformAPIRequest(t, mux, token, http.MethodPost,
		"/api/admin/v1/page-projects/"+strconv.FormatInt(project.ID, 10)+"/restore",
		map[string]any{"revision_id": created.Revision.ID, "summary": "复制修订恢复"},
		current.ETag(), "restore-1")
	if restoreReplay.Code != http.StatusOK ||
		restoreReplay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("restore replay = %d headers=%v body=%s",
			restoreReplay.Code, restoreReplay.Header(), restoreReplay.Body.String())
	}
	var replayed pageProjectEnvelope
	if err := json.Unmarshal(restoreReplay.Body.Bytes(), &replayed); err != nil {
		t.Fatal(err)
	}
	if replayed.Revision == nil || replayed.Revision.ID != restored.Revision.ID {
		t.Fatalf("restore replay created another revision: %#v", replayed.Revision)
	}
}

func TestCompositionCatalogAndBindingPreviewAPIRoutes(t *testing.T) {
	s, token := newTestAutomationServer(t, apiScopePageProjectsRead)
	mux := pagePlatformTestMux(s)
	if _, err := s.store.CreatePost(&store.Post{
		Type: "post", Lang: "zh", Slug: "catalog-preview",
		Title: "Catalog preview content", Status: "published",
	}); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{
		"/api/admin/v1",
		"/api/platform/v1/sites/12",
	} {
		components := pagePlatformAPIRequest(
			t, mux, token, http.MethodGet, prefix+"/page-components", nil, "", "",
		)
		if components.Code != http.StatusOK ||
			!strings.Contains(components.Body.String(), `"hero.split"`) ||
			!strings.Contains(components.Body.String(), `"max_bindings"`) {
			t.Fatalf("%s components = %d %s", prefix, components.Code, components.Body.String())
		}
		sources := pagePlatformAPIRequest(
			t, mux, token, http.MethodGet, prefix+"/page-data-sources?lang=zh", nil, "", "",
		)
		if sources.Code != http.StatusOK ||
			!strings.Contains(sources.Body.String(), `"key":"post"`) ||
			!strings.Contains(sources.Body.String(), `"sorts"`) ||
			!strings.Contains(sources.Body.String(), `"fields"`) {
			t.Fatalf("%s sources = %d %s", prefix, sources.Code, sources.Body.String())
		}
		preview := pagePlatformAPIRequest(
			t, mux, token, http.MethodPost, prefix+"/page-bindings/preview",
			map[string]any{
				"lang":           "zh",
				"component_type": "posts.grid",
				"section_id":     "api-preview",
				"binding": map[string]any{
					"source": "post", "filter": map[string]any{"status": "published"},
					"sort": "-published_at", "limit": 3, "fields": []string{"title", "slug"},
					"update_mode": "live", "missing_policy": "placeholder",
				},
			},
			"", "",
		)
		if preview.Code != http.StatusOK ||
			!strings.Contains(preview.Body.String(), "Catalog preview content") {
			t.Fatalf("%s binding preview = %d %s", prefix, preview.Code, preview.Body.String())
		}
	}
}

func TestPageDesignContextAPIRoutesUseLiveThemeAndData(t *testing.T) {
	s, token := newTestAutomationServer(t, apiScopePageProjectsRead)
	settings := map[string]string{
		"theme":                         "answer-desk-dark",
		"site.name":                     "Context Test",
		"site.tagline":                  "Live site tagline",
		"site.hero_title":               "Live Hero",
		"site.hero_description":         "Read from the selected site",
		"hero.visual":                   "image",
		"hero.image":                    "/uploads/live-hero.webp",
		layoutWidthKey:                  "1240",
		"theme.answer-desk-dark.custom": "1",
		"theme.answer-desk-dark.accent": "#66aaff",
		"theme.answer-desk-dark.radius": "7",
	}
	for key, value := range settings {
		if err := s.store.SetSetting(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	if _, err := s.store.CreatePost(&store.Post{
		Type: "post", Lang: "zh", Slug: "context-live-post",
		Title: "Real context content", Status: "published",
	}); err != nil {
		t.Fatal(err)
	}
	mux := pagePlatformTestMux(s)
	var expectedHash string
	for _, prefix := range []string{
		"/api/admin/v1",
		"/api/platform/v1/sites/12",
	} {
		response := pagePlatformAPIRequest(
			t, mux, token, http.MethodGet, prefix+"/page-design-context?lang=zh", nil, "", "",
		)
		if response.Code != http.StatusOK {
			t.Fatalf("%s design context = %d %s", prefix, response.Code, response.Body.String())
		}
		var context pageDesignContextResponse
		if err := json.Unmarshal(response.Body.Bytes(), &context); err != nil {
			t.Fatalf("decode %s: %v", prefix, err)
		}
		if context.ContractVersion != pageDesignContextContractVersion ||
			context.Lang != "zh" || len(context.ContextHash) != 64 {
			t.Fatalf("%s contract = %#v", prefix, context)
		}
		if expectedHash == "" {
			expectedHash = context.ContextHash
		} else if context.ContextHash != expectedHash {
			t.Fatalf("mirrored routes returned different context hashes: %q != %q",
				context.ContextHash, expectedHash)
		}
		if context.Site.Name != "Context Test" ||
			context.Site.Hero.Title != "Live Hero" ||
			context.Site.Hero.Image != "/uploads/live-hero.webp" {
			t.Fatalf("%s did not use live site values: %#v", prefix, context.Site)
		}
		if context.Theme.ID != "answer-desk-dark" ||
			context.Theme.Family != "answer-desk" ||
			context.Theme.Layout != "answer-desk" ||
			context.Theme.AppearanceHint != "dark" ||
			!context.Theme.Customized ||
			context.Theme.ResolvedHints.Accent != "#66aaff" ||
			context.Theme.ResolvedHints.RadiusPX != 7 ||
			context.Theme.ResolvedHints.ContentWidthPX != 1240 {
			t.Fatalf("%s theme context = %#v", prefix, context.Theme)
		}
		if !context.Theme.Inheritance.Recommended ||
			context.Theme.Inheritance.CopyTokens ||
			!context.ManifestDefault.Theme.Inherit ||
			len(context.ManifestDefault.Theme.Tokens) != 0 ||
			context.ManifestDefault.Shell.Mode != "site" ||
			!context.ManifestDefault.Shell.StickyHeader {
			t.Fatalf("%s inheritance defaults = theme %#v manifest %#v",
				prefix, context.Theme.Inheritance, context.ManifestDefault)
		}
		if len(context.Components) == 0 || len(context.DataSources) == 0 ||
			context.DataSources[0].Key != "post" || len(context.Recipes) < 3 {
			t.Fatalf("%s registries = components=%d sources=%#v recipes=%#v",
				prefix, len(context.Components), context.DataSources, context.Recipes)
		}
		if len(context.Quality.PreviewViewports) != 3 ||
			context.Quality.PreviewViewports[2].Width != 390 ||
			!context.Workflow.PublishNeedsUserApproval ||
			context.Workflow.PrimarySurface != "pilot" {
			t.Fatalf("%s Pilot policy = workflow %#v quality %#v",
				prefix, context.Workflow, context.Quality)
		}
	}
	// newTestAutomationServer constructs a minimal server without the normal
	// New()/Handler bootstrap. Attach the real page-platform submux before
	// exercising the production dispatcher.
	s.pagePlatformMux = mux
	productionRoute := pagePlatformAPIRequest(
		t, s.Handler(), token, http.MethodGet,
		"/api/admin/v1/page-design-context?lang=zh", nil, "", "",
	)
	if productionRoute.Code != http.StatusOK ||
		!strings.Contains(productionRoute.Body.String(), `"context_hash"`) {
		t.Fatalf("production admin design context = %d %s",
			productionRoute.Code, productionRoute.Body.String())
	}

	badLang := pagePlatformAPIRequest(
		t, mux, token, http.MethodGet, "/api/admin/v1/page-design-context?lang=xx", nil, "", "",
	)
	if badLang.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(badLang.Body.String(), "language_invalid") {
		t.Fatalf("bad language = %d %s", badLang.Code, badLang.Body.String())
	}

	legacy, legacyToken := newTestAutomationServer(t, apiScopeContentRead)
	denied := pagePlatformAPIRequest(
		t, pagePlatformTestMux(legacy), legacyToken, http.MethodGet,
		"/api/admin/v1/page-design-context", nil, "", "",
	)
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "missing_scope") {
		t.Fatalf("legacy scope = %d %s", denied.Code, denied.Body.String())
	}
}

func TestPagePlatformAPIAssetMetadataBoundary(t *testing.T) {
	s, token := newTestAutomationServer(t, strings.Join([]string{
		apiScopePageProjectsRead, apiScopePageProjectsWrite, apiScopePageAssetsWrite,
	}, ","))
	mux := pagePlatformTestMux(s)
	_, page, project, baseline := createPageProjectForAPITest(t, s, mux, token)
	uri := "/api/admin/v1/page-projects/" + strconv.FormatInt(project.ID, 10) + "/assets"
	pngA := pagePlatformTestPNG(t, 100)
	invalid := pagePlatformAssetRequest(t, mux, token, uri, project.ETag(), "asset-1",
		pngA, map[string]string{"storage_ref": "outside/project.webp"})
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "asset_invalid") {
		t.Fatalf("client storage ref must be rejected = %d %s", invalid.Code, invalid.Body.String())
	}
	fields := map[string]string{
		"logical_key": "hero/main.png",
		"origin":      "upload",
		"provenance":  `{"source":"test"}`,
	}
	created := pagePlatformAssetRequest(t, mux, token, uri, project.ETag(), "asset-2", pngA, fields)
	if created.Code != http.StatusCreated {
		t.Fatalf("asset create = %d %s", created.Code, created.Body.String())
	}
	replay := pagePlatformAssetRequest(t, mux, token, uri, project.ETag(), "asset-2", pngA, fields)
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("asset replay = %d headers=%v %s", replay.Code, replay.Header(), replay.Body.String())
	}
	var result struct {
		Asset *store.PageAsset `json:"asset"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil || result.Asset == nil {
		t.Fatalf("decode asset: %v body=%s", err, created.Body.String())
	}
	var replayResult struct {
		Asset *store.PageAsset `json:"asset"`
	}
	if err := json.Unmarshal(replay.Body.Bytes(), &replayResult); err != nil ||
		replayResult.Asset == nil || replayResult.Asset.ID != result.Asset.ID {
		t.Fatalf("asset replay changed result: err=%v body=%s", err, replay.Body.String())
	}
	conflict := pagePlatformAssetRequest(
		t, mux, token, uri, project.ETag(), "asset-2", pagePlatformTestPNG(t, 200), fields,
	)
	if conflict.Code != http.StatusConflict ||
		!strings.Contains(conflict.Body.String(), "idempotency_conflict") {
		t.Fatalf("same key with different multipart input = %d %s", conflict.Code, conflict.Body.String())
	}
	deduplicated := pagePlatformAssetRequest(
		t, mux, token, uri, project.ETag(), "asset-3", pngA, fields,
	)
	if deduplicated.Code != http.StatusOK ||
		deduplicated.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("content-addressed dedupe = %d %s", deduplicated.Code, deduplicated.Body.String())
	}
	if result.Asset.StorageRef != "page-projects/"+strconv.FormatInt(project.ID, 10)+
		"/assets/"+result.Asset.SHA256 {
		t.Fatalf("server storage ref = %q", result.Asset.StorageRef)
	}
	stored, err := s.readCompositionAsset(result.Asset)
	if err != nil || !bytes.Equal(stored, pngA) {
		t.Fatalf("stored asset integrity: bytes=%d err=%v", len(stored), err)
	}

	// Two requests can link the same content-addressed final path before either
	// DB transaction commits. The loser must never clean up the winner's file.
	pngB := pagePlatformTestPNG(t, 150)
	concurrentRequests := []*http.Request{
		pagePlatformAssetHTTPRequest(t, token, uri, project.ETag(), "asset-concurrent-1", pngB, fields),
		pagePlatformAssetHTTPRequest(t, token, uri, project.ETag(), "asset-concurrent-2", pngB, fields),
	}
	concurrentResponses := []*httptest.ResponseRecorder{
		httptest.NewRecorder(), httptest.NewRecorder(),
	}
	start := make(chan struct{})
	var wait sync.WaitGroup
	for i := range concurrentRequests {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			mux.ServeHTTP(concurrentResponses[index], concurrentRequests[index])
		}(i)
	}
	close(start)
	wait.Wait()
	for i, response := range concurrentResponses {
		if response.Code != http.StatusCreated && response.Code != http.StatusOK {
			t.Fatalf("concurrent upload %d = %d %s", i, response.Code, response.Body.String())
		}
	}
	assets, err := s.store.ListPageAssets(project.ID)
	if err != nil || len(assets) != 2 {
		t.Fatalf("concurrent content dedupe assets=%d err=%v", len(assets), err)
	}
	var concurrentAsset *store.PageAsset
	for _, asset := range assets {
		if asset.SHA256 != result.Asset.SHA256 {
			concurrentAsset = asset
		}
	}
	concurrentStored, err := s.readCompositionAsset(concurrentAsset)
	if err != nil || !bytes.Equal(concurrentStored, pngB) {
		t.Fatalf("concurrent winner file was removed: bytes=%d err=%v",
			len(concurrentStored), err)
	}

	manifestRaw := compositionManifestJSON(t, "none", []map[string]any{{
		"id": "hero-image", "type": "media.image",
		"props": map[string]any{"alt": "Uploaded hero"},
		"media": map[string]any{
			"asset_id": result.Asset.ID, "sha256": result.Asset.SHA256,
		},
	}})
	validation := s.NormalizeAndValidateCompositionManifest(manifestRaw, page.Lang)
	if !validation.Valid {
		t.Fatalf("uploaded asset manifest = %+v", validation.Diagnostics)
	}
	meta, err := store.PageRevisionMetaFromPost(page).CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := s.store.CreatePageProjectRevision(store.CreatePageRevisionInput{
		ProjectID: project.ID, BaseRevisionID: baseline.ID,
		RevisionKind: store.PageRevisionComposition, PageMetaJSON: meta,
		ManifestJSON: validation.CanonicalJSON, Origin: store.PageOriginAPI,
		RequestID: "asset-public-revision", ValidationJSON: `{"valid":true}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err = s.store.GetPageProject(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := s.ValidateCompositionBuild(
		context.Background(), project, revision, CompositionBindingPublishedOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	publishCompositionProject(t, s, project, revision, preflight.DataSnapshotHash)
	publicPath := "/page-assets/" + strconv.FormatInt(result.Asset.ID, 10) + "/" + result.Asset.SHA256
	publicAsset := httptest.NewRecorder()
	compositionPublicTestMux(s).ServeHTTP(
		publicAsset, httptest.NewRequest(http.MethodGet, publicPath, nil),
	)
	if publicAsset.Code != http.StatusOK || !bytes.Equal(publicAsset.Body.Bytes(), pngA) ||
		publicAsset.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("public immutable asset = %d headers=%v bytes=%d",
			publicAsset.Code, publicAsset.Header(), publicAsset.Body.Len())
	}
	staticResult := &staticExportResult{
		Dir: t.TempDir(), Files: map[string]staticExportFile{}, ByHash: map[string]string{},
	}
	if err := s.exportPublishedCompositionAssets(staticResult); err != nil {
		t.Fatalf("export composition assets: %v", err)
	}
	exportedPath := filepath.Join(staticResult.Dir, strings.TrimPrefix(publicPath, "/"))
	exported, err := os.ReadFile(exportedPath)
	if err != nil || !bytes.Equal(exported, pngA) {
		t.Fatalf("static immutable asset bytes=%d err=%v", len(exported), err)
	}

	project, err = s.store.GetPageProject(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	deleteResponse := pagePlatformAPIRequest(t, mux, token, http.MethodDelete,
		uri+"/"+strconv.FormatInt(result.Asset.ID, 10), nil, project.ETag(), "asset-delete-1")
	if deleteResponse.Code != http.StatusNotImplemented ||
		!strings.Contains(deleteResponse.Body.String(), "asset_delete_not_supported") {
		t.Fatalf("safe delete boundary = %d %s", deleteResponse.Code, deleteResponse.Body.String())
	}
}

func TestPageApprovalTokenIsBoundOneTimeAndPublishIsIdempotent(t *testing.T) {
	s, token := newTestAutomationServer(t, strings.Join([]string{
		apiScopePageProjectsRead, apiScopePageProjectsWrite, apiScopePagesPublish,
	}, ","))
	mux := pagePlatformTestMux(s)
	_, _, project, baseline := createPageProjectForAPITest(t, s, mux, token)

	plan := pagePlatformAPIRequest(t, mux, token, http.MethodPost,
		"/api/admin/v1/page-projects/"+strconv.FormatInt(project.ID, 10)+"/publish-plan",
		map[string]any{"revision_id": baseline.ID}, project.ETag(), "")
	if plan.Code != http.StatusOK || !strings.Contains(plan.Body.String(), `"can_execute":true`) ||
		!strings.Contains(plan.Body.String(), `"approval_token_issued":false`) {
		t.Fatalf("publish plan = %d %s", plan.Code, plan.Body.String())
	}
	approval, err := s.issuePageApprovalToken(project.ID, baseline.ID, pageApprovalPublish, "admin:test", time.Minute)
	if err != nil {
		t.Fatalf("issue approval: %v", err)
	}
	if approval.ApprovalToken == "" || approval.ApprovalID == "" ||
		approval.ETag != project.ETag() {
		t.Fatalf("approval = %#v", approval)
	}
	publishBody := map[string]any{
		"revision_id":    baseline.ID,
		"approval_token": approval.ApprovalToken,
	}
	uri := "/api/admin/v1/page-projects/" + strconv.FormatInt(project.ID, 10) + "/publish"
	published := pagePlatformAPIRequest(t, mux, token, http.MethodPost, uri,
		publishBody, project.ETag(), "publish-1")
	if published.Code != http.StatusCreated {
		t.Fatalf("publish = %d %s", published.Code, published.Body.String())
	}
	if strings.Contains(published.Body.String(), approval.ApprovalToken) {
		t.Fatal("publication response leaked approval secret")
	}
	retry := pagePlatformAPIRequest(t, mux, token, http.MethodPost, uri,
		publishBody, project.ETag(), "publish-1")
	if retry.Code != http.StatusOK || retry.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("publish retry = %d headers=%v %s", retry.Code, retry.Header(), retry.Body.String())
	}
	reuse := pagePlatformAPIRequest(t, mux, token, http.MethodPost, uri,
		publishBody, project.ETag(), "publish-2")
	if reuse.Code != http.StatusConflict ||
		!strings.Contains(reuse.Body.String(), "publish_confirmation_required") {
		t.Fatalf("approval reuse = %d %s", reuse.Code, reuse.Body.String())
	}

	tampered := approval.ApprovalToken[:len(approval.ApprovalToken)-1] + "x"
	tamperedBody := map[string]any{"revision_id": baseline.ID, "approval_token": tampered}
	tamperResponse := pagePlatformAPIRequest(t, mux, token, http.MethodPost, uri,
		tamperedBody, project.ETag(), "publish-tamper")
	if tamperResponse.Code != http.StatusConflict ||
		!strings.Contains(tamperResponse.Body.String(), "publish_confirmation_required") {
		t.Fatalf("tampered approval = %d %s", tamperResponse.Code, tamperResponse.Body.String())
	}

	publications := pagePlatformAPIRequest(t, mux, token, http.MethodGet,
		"/api/admin/v1/page-projects/"+strconv.FormatInt(project.ID, 10)+"/publications",
		nil, "", "")
	if publications.Code != http.StatusOK || !strings.Contains(publications.Body.String(), approval.ApprovalID) {
		t.Fatalf("publications = %d %s", publications.Code, publications.Body.String())
	}
}

func TestCompositionPublishUsesServerValidatedDataSnapshot(t *testing.T) {
	s, token := newTestAutomationServer(t, strings.Join([]string{
		apiScopePageProjectsRead, apiScopePageProjectsWrite, apiScopePagesPublish,
		apiScopeControlUnlock,
	}, ","))
	setSingleTestPassword(t, s, controlTestPassword)
	raw := compositionManifestJSON(
		t, "site", []map[string]any{compositionHeroSection("Server snapshot")},
	)
	_, project, revision := createCompositionProject(t, s, raw, "draft")
	preflight, err := s.ValidateCompositionBuild(
		context.Background(), project, revision, CompositionBindingPublishedOnly,
	)
	if err != nil || !validCompositionSHA256(preflight.DataSnapshotHash) {
		t.Fatalf("composition preflight hash=%q err=%v", preflight.DataSnapshotHash, err)
	}
	build, err := s.store.CreatePageBuild(store.CreatePageBuildInput{
		ProjectID: project.ID, RevisionID: revision.ID, Status: store.PageBuildReady,
		ArtifactRef:  "composition:ssr/" + preflight.RenderHash,
		ArtifactHash: preflight.RenderHash, DiagnosticsJSON: `[]`,
		RuntimeVersion: compositionRuntimeVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := pagePlatformTestMux(s)
	uri := "/api/admin/v1/page-projects/" + strconv.FormatInt(project.ID, 10) + "/publish"

	mismatch := pagePlatformAPIRequest(t, mux, token, http.MethodPost, uri,
		map[string]any{
			"revision_id": revision.ID, "build_id": build.ID,
			"data_snapshot_hash": strings.Repeat("f", 64),
		},
		project.ETag(), "composition-snapshot-mismatch")
	if mismatch.Code != http.StatusConflict ||
		!strings.Contains(mismatch.Body.String(), `"error":"data_snapshot_conflict"`) ||
		!strings.Contains(mismatch.Body.String(), preflight.DataSnapshotHash) {
		t.Fatalf("snapshot mismatch = %d %s", mismatch.Code, mismatch.Body.String())
	}

	blocked := pagePlatformAPIRequest(t, mux, token, http.MethodPost, uri,
		map[string]any{"revision_id": revision.ID, "build_id": build.ID},
		project.ETag(), "composition-snapshot-publish")
	if blocked.Code != http.StatusConflict ||
		!strings.Contains(blocked.Body.String(), `"unlock_required":true`) ||
		!strings.Contains(
			blocked.Body.String(),
			`"data_snapshot_hash":"`+preflight.DataSnapshotHash+`"`,
		) {
		t.Fatalf("server-bound challenge = %d %s", blocked.Code, blocked.Body.String())
	}
	var challenge struct {
		Token string `json:"unlock_challenge"`
	}
	if err := json.Unmarshal(blocked.Body.Bytes(), &challenge); err != nil ||
		challenge.Token == "" {
		t.Fatalf("decode server-bound challenge: err=%v body=%s", err, blocked.Body.String())
	}
	unlockRaw, _ := json.Marshal(map[string]any{
		"password":       controlTestPassword,
		"operations":     []string{pageApprovalPublish},
		"page_challenge": challenge.Token,
	})
	unlockRequest := httptest.NewRequest(
		http.MethodPost, "https://site.test/api/admin/v1/control/unlock",
		bytes.NewReader(unlockRaw),
	)
	unlockRequest.Header.Set("Authorization", "Bearer "+token)
	unlockRequest.Header.Set("Content-Type", "application/json")
	unlockRequest.Header.Set(controlUIRequestHeader, controlUIPilotValue)
	unlockResponse := httptest.NewRecorder()
	s.serveSingleControlUnlock(unlockResponse, unlockRequest)
	var unlocked struct {
		Token string `json:"unlock_token"`
	}
	if unlockResponse.Code != http.StatusCreated ||
		json.Unmarshal(unlockResponse.Body.Bytes(), &unlocked) != nil ||
		unlocked.Token == "" {
		t.Fatalf("native unlock = %d %s", unlockResponse.Code, unlockResponse.Body.String())
	}
	publishRaw, _ := json.Marshal(map[string]any{
		"revision_id": revision.ID, "build_id": build.ID,
	})
	publishRequest := httptest.NewRequest(http.MethodPost, uri, bytes.NewReader(publishRaw))
	publishRequest.Header.Set("Authorization", "Bearer "+token)
	publishRequest.Header.Set("Content-Type", "application/json")
	publishRequest.Header.Set(pagePlatformConcurrencyHeader, project.ETag())
	publishRequest.Header.Set(pagePlatformIdempotencyHeader, "composition-snapshot-publish")
	publishRequest.Header.Set(controlUnlockHeader, unlocked.Token)
	published := httptest.NewRecorder()
	mux.ServeHTTP(published, publishRequest)
	if published.Code != http.StatusCreated ||
		!strings.Contains(
			published.Body.String(),
			`"DataSnapshotHash":"`+preflight.DataSnapshotHash+`"`,
		) {
		t.Fatalf("server-bound publication = %d %s", published.Code, published.Body.String())
	}
}

func TestCompositionPublishRejectsBuildAfterBoundDataChanges(t *testing.T) {
	s, token := newTestAutomationServer(t, strings.Join([]string{
		apiScopePageProjectsRead, apiScopePageProjectsWrite,
		apiScopePageProjectsBuild, apiScopePagesPublish,
	}, ","))
	contentID, err := s.store.CreatePost(&store.Post{
		Type: "post", Lang: "zh", Slug: "bound-story", Title: "Before build",
		Excerpt: "Original snapshot", Status: "published", EditorMode: "markdown",
	})
	if err != nil {
		t.Fatalf("create bound content: %v", err)
	}
	raw := compositionManifestJSON(t, "site", []map[string]any{
		compositionHeroSection("Snapshot guard"),
		compositionPostCardsSection(),
	})
	_, project, revision := createCompositionProject(t, s, raw, "draft")
	first, err := s.ValidateCompositionBuild(
		context.Background(), project, revision, CompositionBindingPublishedOnly,
	)
	if err != nil {
		t.Fatalf("initial build validation: %v", err)
	}
	oldBuild, err := s.store.CreatePageBuild(store.CreatePageBuildInput{
		ProjectID: project.ID, RevisionID: revision.ID, Status: store.PageBuildReady,
		ArtifactRef: "composition:ssr/" + first.RenderHash, ArtifactHash: first.RenderHash,
		DiagnosticsJSON: `[]`, RuntimeVersion: compositionRuntimeVersion,
	})
	if err != nil {
		t.Fatalf("create initial build: %v", err)
	}

	content, err := s.store.GetPostByID(contentID)
	if err != nil || content == nil {
		t.Fatalf("read bound content: post=%+v err=%v", content, err)
	}
	content.Title = "After build"
	content.Excerpt = "A different live snapshot"
	if err := s.store.UpdatePost(content); err != nil {
		t.Fatalf("mutate bound content: %v", err)
	}

	mux := pagePlatformTestMux(s)
	base := "/api/admin/v1/page-projects/" + strconv.FormatInt(project.ID, 10)
	target := map[string]any{"revision_id": revision.ID, "build_id": oldBuild.ID}
	plan := pagePlatformAPIRequest(
		t, mux, token, http.MethodPost, base+"/publish-plan",
		target, project.ETag(), "",
	)
	if plan.Code != http.StatusOK ||
		!strings.Contains(plan.Body.String(), `"can_execute":false`) ||
		!strings.Contains(plan.Body.String(), `"code":"build_stale"`) {
		t.Fatalf("stale build plan = %d %s", plan.Code, plan.Body.String())
	}
	publish := pagePlatformAPIRequest(
		t, mux, token, http.MethodPost, base+"/publish",
		target, project.ETag(), "stale-build-publish",
	)
	if publish.Code != http.StatusConflict ||
		!strings.Contains(publish.Body.String(), `"error":"build_stale"`) ||
		strings.Contains(publish.Body.String(), `"unlock_required":true`) {
		t.Fatalf("stale build publish = %d %s", publish.Code, publish.Body.String())
	}

	rebuilt := pagePlatformAPIRequest(
		t, mux, token, http.MethodPost, base+"/builds",
		map[string]any{"revision_id": revision.ID}, project.ETag(), "fresh-build",
	)
	if rebuilt.Code != http.StatusCreated {
		t.Fatalf("fresh build = %d %s", rebuilt.Code, rebuilt.Body.String())
	}
	var rebuiltBody struct {
		Build store.PageBuild `json:"build"`
	}
	if err := json.Unmarshal(rebuilt.Body.Bytes(), &rebuiltBody); err != nil ||
		rebuiltBody.Build.ID <= 0 || rebuiltBody.Build.ID == oldBuild.ID {
		t.Fatalf("decode fresh build: body=%s err=%v", rebuilt.Body.String(), err)
	}
	freshPlan := pagePlatformAPIRequest(
		t, mux, token, http.MethodPost, base+"/publish-plan",
		map[string]any{"revision_id": revision.ID, "build_id": rebuiltBody.Build.ID},
		project.ETag(), "",
	)
	if freshPlan.Code != http.StatusOK ||
		!strings.Contains(freshPlan.Body.String(), `"can_execute":true`) ||
		strings.Contains(freshPlan.Body.String(), `"code":"build_stale"`) {
		t.Fatalf("fresh build plan = %d %s", freshPlan.Code, freshPlan.Body.String())
	}
}

func TestCompositionBuildIdempotencyBindsCurrentValidatedArtifact(t *testing.T) {
	s, token := newTestAutomationServer(t, strings.Join([]string{
		apiScopePageProjectsRead, apiScopePageProjectsBuild,
	}, ","))
	contentID, err := s.store.CreatePost(&store.Post{
		Type: "post", Lang: "zh", Slug: "build-idempotency-source",
		Title: "Build identity one", Excerpt: "Snapshot one",
		Status: "published", EditorMode: "markdown",
	})
	if err != nil {
		t.Fatalf("create bound content: %v", err)
	}
	raw := compositionManifestJSON(t, "site", []map[string]any{
		compositionHeroSection("Build idempotency"),
		compositionPostCardsSection(),
	})
	_, project, revision := createCompositionProject(t, s, raw, "draft")
	mux := pagePlatformTestMux(s)
	uri := "/api/admin/v1/page-projects/" + strconv.FormatInt(project.ID, 10) + "/builds"
	body := map[string]any{"revision_id": revision.ID}
	const requestID = "composition-durable-build"

	first := pagePlatformAPIRequest(
		t, mux, token, http.MethodPost, uri, body, project.ETag(), requestID,
	)
	if first.Code != http.StatusCreated {
		t.Fatalf("first build = %d %s", first.Code, first.Body.String())
	}
	var firstEnvelope struct {
		Build *store.PageBuild `json:"build"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstEnvelope); err != nil ||
		firstEnvelope.Build == nil {
		t.Fatalf("decode first build: err=%v body=%s", err, first.Body.String())
	}
	replay := pagePlatformAPIRequest(
		t, mux, token, http.MethodPost, uri, body, project.ETag(), requestID,
	)
	var replayEnvelope struct {
		Build *store.PageBuild `json:"build"`
	}
	if replay.Code != http.StatusOK ||
		replay.Header().Get("Idempotent-Replayed") != "true" ||
		json.Unmarshal(replay.Body.Bytes(), &replayEnvelope) != nil ||
		replayEnvelope.Build == nil || replayEnvelope.Build.ID != firstEnvelope.Build.ID {
		t.Fatalf("same-key replay = %d headers=%v body=%s",
			replay.Code, replay.Header(), replay.Body.String())
	}
	freshKey := pagePlatformAPIRequest(
		t, mux, token, http.MethodPost, uri, body, project.ETag(), "composition-fresh-key",
	)
	if freshKey.Code != http.StatusOK ||
		freshKey.Header().Get("Idempotent-Replayed") != "" ||
		!strings.Contains(freshKey.Body.String(), `"created":false`) {
		t.Fatalf("fresh-key artifact reuse mislabeled = %d headers=%v body=%s",
			freshKey.Code, freshKey.Header(), freshKey.Body.String())
	}

	content, err := s.store.GetPostByID(contentID)
	if err != nil || content == nil {
		t.Fatalf("read bound content: post=%+v err=%v", content, err)
	}
	content.Title = "Build identity two"
	content.Excerpt = "Snapshot two"
	if err := s.store.UpdatePost(content); err != nil {
		t.Fatalf("mutate bound content: %v", err)
	}
	conflict := pagePlatformAPIRequest(
		t, mux, token, http.MethodPost, uri, body, project.ETag(), requestID,
	)
	if conflict.Code != http.StatusConflict ||
		!strings.Contains(conflict.Body.String(), `"error":"idempotency_conflict"`) {
		t.Fatalf("changed live identity with same key = %d %s",
			conflict.Code, conflict.Body.String())
	}
	rebuilt := pagePlatformAPIRequest(
		t, mux, token, http.MethodPost, uri, body, project.ETag(), "composition-after-change",
	)
	if rebuilt.Code != http.StatusCreated ||
		!strings.Contains(rebuilt.Body.String(), `"created":true`) {
		t.Fatalf("new key after data change = %d %s", rebuilt.Code, rebuilt.Body.String())
	}
}

func TestPagePlatformAPIFailClosedUnsupportedMutationAndBaselineRuntime(t *testing.T) {
	s, token := newTestAutomationServer(t, strings.Join([]string{
		apiScopePageProjectsRead, apiScopePageProjectsWrite,
		apiScopePageProjectsBuild, apiScopePagePreviewRead,
	}, ","))
	mux := pagePlatformTestMux(s)
	_, _, project, _ := createPageProjectForAPITest(t, s, mux, token)
	base := "/api/admin/v1/page-projects/" + strconv.FormatInt(project.ID, 10)
	cases := []struct {
		method, path string
		body         any
		requestID    string
		status       int
		code         string
	}{
		{http.MethodPatch, base, map[string]any{}, "patch-1", http.StatusNotImplemented, "project_update_not_supported"},
		{http.MethodPost, base + "/builds", map[string]any{}, "build-1", http.StatusUnprocessableEntity, "composition_invalid"},
		{http.MethodPost, base + "/preview-url", map[string]any{}, "", http.StatusUnprocessableEntity, "composition_invalid"},
	}
	for _, tc := range cases {
		response := pagePlatformAPIRequest(t, mux, token, tc.method, tc.path,
			tc.body, project.ETag(), tc.requestID)
		if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.code) {
			t.Fatalf("%s %s = %d %s", tc.method, tc.path, response.Code, response.Body.String())
		}
	}
}
