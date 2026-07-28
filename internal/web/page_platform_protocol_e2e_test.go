package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

// pageProtocolNativeMutation exercises the same native-confirmation boundary
// used by Pilot: the model-visible request receives only a target-bound
// challenge, the password is submitted to the native control endpoint, and
// the short-lived unlock is carried only in a request header.
func pageProtocolNativeMutation(
	t *testing.T,
	handler http.Handler,
	token, endpoint, etag, requestID, operation string,
	body map[string]any,
) *httptest.ResponseRecorder {
	t.Helper()
	blocked := pagePlatformAPIRequest(
		t, handler, token, http.MethodPost, endpoint, body, etag, requestID,
	)
	if blocked.Code != http.StatusConflict ||
		!strings.Contains(blocked.Body.String(), `"unlock_required":true`) {
		t.Fatalf("native mutation did not stop for confirmation: %d %s",
			blocked.Code, blocked.Body.String())
	}
	var challenge struct {
		Token string `json:"unlock_challenge"`
	}
	if err := json.Unmarshal(blocked.Body.Bytes(), &challenge); err != nil ||
		challenge.Token == "" {
		t.Fatalf("decode native challenge: err=%v body=%s",
			err, blocked.Body.String())
	}

	unlockRaw, _ := json.Marshal(map[string]any{
		"password":       controlTestPassword,
		"operations":     []string{operation},
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
	handler.ServeHTTP(unlockResponse, unlockRequest)
	var unlock struct {
		Token string `json:"unlock_token"`
	}
	if unlockResponse.Code != http.StatusCreated ||
		json.Unmarshal(unlockResponse.Body.Bytes(), &unlock) != nil ||
		!strings.HasPrefix(unlock.Token, "gcmsup_") {
		t.Fatalf("native unlock = %d %s",
			unlockResponse.Code, unlockResponse.Body.String())
	}

	raw, _ := json.Marshal(body)
	request := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(pagePlatformConcurrencyHeader, etag)
	request.Header.Set(pagePlatformIdempotencyHeader, requestID)
	request.Header.Set(controlUnlockHeader, unlock.Token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if strings.Contains(response.Body.String(), unlock.Token) ||
		strings.Contains(response.Body.String(), controlTestPassword) {
		t.Fatalf("native mutation leaked a UI-only secret: %s", response.Body.String())
	}
	return response
}

func pageProtocolBuild(
	t *testing.T,
	handler http.Handler,
	token, prefix string,
	project *store.PageProject,
	revisionID int64,
	requestID string,
) *store.PageBuild {
	t.Helper()
	response := pagePlatformAPIRequest(
		t, handler, token, http.MethodPost, prefix+"/builds",
		map[string]any{"revision_id": revisionID},
		project.ETag(), requestID,
	)
	if response.Code != http.StatusCreated && response.Code != http.StatusOK {
		t.Fatalf("build = %d %s", response.Code, response.Body.String())
	}
	var envelope struct {
		Build *store.PageBuild `json:"build"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil ||
		envelope.Build == nil || envelope.Build.Status != store.PageBuildReady {
		t.Fatalf("decode ready build: err=%v body=%s",
			err, response.Body.String())
	}
	return envelope.Build
}

func TestStandardPageProtocolKeepsLegacyWorkflowAndOptionalETag(t *testing.T) {
	s := newTestPublicServer(t, "")
	token, prefix := newAutomationToken()
	if _, err := s.store.CreateAutomationKey(
		"standard-protocol", token, prefix,
		"pages:read,pages:write,pages:publish",
	); err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()
	created := pagePlatformAPIRequest(
		t, handler, token, http.MethodPost, "/api/admin/v1/pages",
		map[string]any{
			"lang": "zh", "slug": "standard-protocol",
			"title": "标准页面第一版", "content": "标准正文第一版",
			"status": "draft", "editor_mode": "markdown",
		},
		"", "",
	)
	var createEnvelope struct {
		Item apiContentItem `json:"item"`
	}
	firstETag := created.Header().Get("ETag")
	if created.Code != http.StatusCreated ||
		json.Unmarshal(created.Body.Bytes(), &createEnvelope) != nil ||
		createEnvelope.Item.ID <= 0 ||
		!strings.HasPrefix(firstETag, `"content-`) {
		t.Fatalf("standard create = %d etag=%q body=%s",
			created.Code, firstETag, created.Body.String())
	}
	pageID := createEnvelope.Item.ID

	// The platform/Pilot mirror reads and writes the same legacy row and
	// optional strong ETag; it does not create a hidden advanced project.
	platformPath := "/api/platform/v1/sites/42/pages/" +
		strconv.FormatInt(pageID, 10)
	read := pagePlatformAPIRequest(
		t, handler, token, http.MethodGet, platformPath, nil, "", "",
	)
	if read.Code != http.StatusOK ||
		read.Header().Get("ETag") != firstETag {
		t.Fatalf("platform standard read = %d etag=%q body=%s",
			read.Code, read.Header().Get("ETag"), read.Body.String())
	}
	updated := pagePlatformAPIRequest(
		t, handler, token, http.MethodPatch, platformPath,
		map[string]any{
			"title": "标准页面第二版", "content": "标准正文第二版",
		},
		firstETag, "",
	)
	secondETag := updated.Header().Get("ETag")
	if updated.Code != http.StatusOK || secondETag == "" ||
		secondETag == firstETag {
		t.Fatalf("platform standard update = %d etag=%q body=%s",
			updated.Code, secondETag, updated.Body.String())
	}
	if project, err := s.store.GetPageProjectByPostID(pageID); err != nil ||
		project != nil {
		t.Fatalf("standard workflow created sidecar: project=%#v err=%v",
			project, err)
	}

	previewResponse := pagePlatformAPIRequest(
		t, handler, token, http.MethodPost,
		"/api/admin/v1/pages/"+strconv.FormatInt(pageID, 10)+"/preview-url",
		nil, "", "",
	)
	var preview struct {
		URL string `json:"preview_url"`
	}
	if previewResponse.Code != http.StatusCreated ||
		json.Unmarshal(previewResponse.Body.Bytes(), &preview) != nil ||
		preview.URL == "" {
		t.Fatalf("standard preview URL = %d %s",
			previewResponse.Code, previewResponse.Body.String())
	}
	previewURL, err := url.Parse(preview.URL)
	if err != nil {
		t.Fatal(err)
	}
	previewPage := httptest.NewRecorder()
	handler.ServeHTTP(
		previewPage,
		httptest.NewRequest(http.MethodGet, previewURL.RequestURI(), nil),
	)
	if previewPage.Code != http.StatusOK ||
		!strings.Contains(previewPage.Body.String(), "标准正文第二版") ||
		previewPage.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("standard preview = %d %s",
			previewPage.Code, previewPage.Body.String())
	}

	published := pagePlatformAPIRequest(
		t, handler, token, http.MethodPatch,
		"/api/admin/v1/pages/"+strconv.FormatInt(pageID, 10),
		map[string]any{"status": "published"},
		secondETag, "",
	)
	if published.Code != http.StatusOK {
		t.Fatalf("legacy standard publish = %d %s",
			published.Code, published.Body.String())
	}
	public := httptest.NewRecorder()
	handler.ServeHTTP(
		public,
		httptest.NewRequest(http.MethodGet, "/zh/standard-protocol", nil),
	)
	if public.Code != http.StatusOK ||
		!strings.Contains(public.Body.String(), "标准正文第二版") {
		t.Fatalf("published standard route = %d %s",
			public.Code, public.Body.String())
	}

	revisions := pagePlatformAPIRequest(
		t, handler, token, http.MethodGet,
		"/api/admin/v1/pages/"+strconv.FormatInt(pageID, 10)+"/revisions",
		nil, "", "",
	)
	var revisionList struct {
		Items []struct {
			ID     int64  `json:"id"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if revisions.Code != http.StatusOK ||
		json.Unmarshal(revisions.Body.Bytes(), &revisionList) != nil ||
		len(revisionList.Items) < 2 {
		t.Fatalf("standard revision list = %d %s",
			revisions.Code, revisions.Body.String())
	}
	var initialRevisionID int64
	for _, revision := range revisionList.Items {
		if revision.Title == "标准页面第一版" &&
			revision.Status == "draft" {
			initialRevisionID = revision.ID
			break
		}
	}
	if initialRevisionID <= 0 {
		t.Fatalf("initial legacy revision missing: %#v", revisionList.Items)
	}
	restored := pagePlatformAPIRequest(
		t, handler, token, http.MethodPost,
		"/api/admin/v1/pages/"+strconv.FormatInt(pageID, 10)+
			"/revisions/"+strconv.FormatInt(initialRevisionID, 10)+"/restore",
		map[string]any{}, "", "",
	)
	if restored.Code != http.StatusOK ||
		!strings.Contains(restored.Body.String(), `"title":"标准页面第一版"`) ||
		!strings.Contains(restored.Body.String(), `"status":"draft"`) {
		t.Fatalf("legacy standard restore = %d %s",
			restored.Code, restored.Body.String())
	}
}

func TestPageAppProtocolNativePublishAndRollback(t *testing.T) {
	fixture := createPageAppE2EFixture(
		t, pageAppFilesWithoutCapabilities(), "app-protocol-rollback",
	)
	setSingleTestPassword(t, fixture.Server, controlTestPassword)
	base := "/api/admin/v1/page-projects/" +
		strconv.FormatInt(fixture.Project.ID, 10)

	firstPlan := pagePlatformAPIRequest(
		t, fixture.Handler, fixture.Token, http.MethodPost,
		base+"/publish-plan",
		map[string]any{
			"revision_id": fixture.Revision.ID,
			"build_id":    fixture.Build.ID,
		},
		fixture.Project.ETag(), "",
	)
	if firstPlan.Code != http.StatusOK ||
		!strings.Contains(firstPlan.Body.String(), `"can_execute":true`) {
		t.Fatalf("first app publish plan = %d %s",
			firstPlan.Code, firstPlan.Body.String())
	}
	firstPublish := pageProtocolNativeMutation(
		t, fixture.Handler, fixture.Token, base+"/publish",
		fixture.Project.ETag(), "app-protocol-publish-1",
		pageApprovalPublish,
		map[string]any{
			"revision_id": fixture.Revision.ID,
			"build_id":    fixture.Build.ID,
		},
	)
	if firstPublish.Code != http.StatusCreated {
		t.Fatalf("first app publish = %d %s",
			firstPublish.Code, firstPublish.Body.String())
	}

	secondFiles := pageAppFilesWithoutCapabilities()
	secondFiles["app.js"] = `document.getElementById("status").textContent = "second";`
	upload := pageAppMultipartRequest(
		t, fixture.Handler, fixture.Token, base+"/app-package",
		fixture.Project.ETag(), "app-protocol-upload-2",
		pageAppZipForTest(t, secondFiles),
	)
	if upload.Code != http.StatusCreated {
		t.Fatalf("second app upload = %d %s",
			upload.Code, upload.Body.String())
	}
	var second pageProjectEnvelope
	if err := json.Unmarshal(upload.Body.Bytes(), &second); err != nil ||
		second.Project == nil || second.Revision == nil ||
		second.Revision.ID == fixture.Revision.ID {
		t.Fatalf("decode second app revision: err=%v body=%s",
			err, upload.Body.String())
	}
	secondBuild := pageProtocolBuild(
		t, fixture.Handler, fixture.Token, base, second.Project,
		second.Revision.ID, "app-protocol-build-2",
	)
	preview := pagePlatformAPIRequest(
		t, fixture.Handler, fixture.Token, http.MethodPost,
		base+"/preview-url",
		map[string]any{
			"revision_id": second.Revision.ID,
			"build_id":    secondBuild.ID,
		},
		second.Project.ETag(), "",
	)
	if preview.Code != http.StatusCreated ||
		!strings.Contains(preview.Body.String(), `"preview_url"`) {
		t.Fatalf("second app preview = %d %s",
			preview.Code, preview.Body.String())
	}
	secondPlan := pagePlatformAPIRequest(
		t, fixture.Handler, fixture.Token, http.MethodPost,
		base+"/publish-plan",
		map[string]any{
			"revision_id": second.Revision.ID,
			"build_id":    secondBuild.ID,
		},
		second.Project.ETag(), "",
	)
	if secondPlan.Code != http.StatusOK ||
		!strings.Contains(secondPlan.Body.String(), `"can_execute":true`) {
		t.Fatalf("second app publish plan = %d %s",
			secondPlan.Code, secondPlan.Body.String())
	}
	secondPublish := pageProtocolNativeMutation(
		t, fixture.Handler, fixture.Token, base+"/publish",
		second.Project.ETag(), "app-protocol-publish-2",
		pageApprovalPublish,
		map[string]any{
			"revision_id": second.Revision.ID,
			"build_id":    secondBuild.ID,
		},
	)
	if secondPublish.Code != http.StatusCreated {
		t.Fatalf("second app publish = %d %s",
			secondPublish.Code, secondPublish.Body.String())
	}

	rollbackPlan := pagePlatformAPIRequest(
		t, fixture.Handler, fixture.Token, http.MethodPost,
		base+"/rollback-plan",
		map[string]any{
			"revision_id": fixture.Revision.ID,
			"build_id":    fixture.Build.ID,
		},
		second.Project.ETag(), "",
	)
	if rollbackPlan.Code != http.StatusOK ||
		!strings.Contains(rollbackPlan.Body.String(), `"can_execute":true`) {
		t.Fatalf("app rollback plan = %d %s",
			rollbackPlan.Code, rollbackPlan.Body.String())
	}
	rollback := pageProtocolNativeMutation(
		t, fixture.Handler, fixture.Token, base+"/rollback",
		second.Project.ETag(), "app-protocol-rollback-1",
		pageApprovalRollback,
		map[string]any{
			"revision_id": fixture.Revision.ID,
			"build_id":    fixture.Build.ID,
		},
	)
	if rollback.Code != http.StatusCreated {
		t.Fatalf("app rollback = %d %s",
			rollback.Code, rollback.Body.String())
	}
	current, err := fixture.Server.store.GetPageProject(fixture.Project.ID)
	if err != nil || current == nil ||
		current.WorkingRevisionID != second.Revision.ID ||
		current.PublishedRevisionID != fixture.Revision.ID {
		t.Fatalf("app rollback pointers: project=%#v err=%v", current, err)
	}
	public := httptest.NewRecorder()
	fixture.Handler.ServeHTTP(
		public,
		httptest.NewRequest(
			http.MethodGet, "/zh/app-protocol-rollback", nil,
		),
	)
	firstEntry := "/" + strconv.FormatInt(fixture.Revision.ID, 10) + "/index.html"
	secondEntry := "/" + strconv.FormatInt(second.Revision.ID, 10) + "/index.html"
	if public.Code != http.StatusOK ||
		!strings.Contains(public.Body.String(), firstEntry) ||
		strings.Contains(public.Body.String(), secondEntry) {
		t.Fatalf("public app did not use rolled-back artifact: %d %s",
			public.Code, public.Body.String())
	}
}

func TestPagePlatformProtocolAdminAndPilotShareCompositionHistory(t *testing.T) {
	s := newTestPublicServer(t, "")
	token, prefix := newAutomationToken()
	scopes := strings.Join([]string{
		apiScopePageProjectsRead, apiScopePageProjectsWrite,
		apiScopePageProjectsBuild, apiScopePagePreviewRead,
		apiScopePagesPublish, apiScopeControlUnlock,
	}, ",")
	if _, err := s.store.CreateAutomationKey(
		"pilot-protocol-e2e", token, prefix, scopes,
	); err != nil {
		t.Fatal(err)
	}
	setSingleTestPassword(t, s, controlTestPassword)
	handler := s.Handler()

	// The page and its first immutable composition revision are created by the
	// manual GCMS workbench.
	page, project := createAdminCompositionPage(
		t, s, "协议端到端", "protocol-composition",
	)
	adminRevision, err := s.store.GetPageProjectRevision(project.WorkingRevisionID)
	if err != nil || adminRevision == nil ||
		adminRevision.Origin != store.PageOriginAdmin {
		t.Fatalf("admin revision=%#v err=%v", adminRevision, err)
	}

	// Pilot reads the exact same project and ETag, not an AI-side shadow copy.
	apiBase := "/api/admin/v1/page-projects/" + strconv.FormatInt(project.ID, 10)
	read := pagePlatformAPIRequest(
		t, handler, token, http.MethodGet, apiBase, nil, "", "",
	)
	var readEnvelope pageProjectEnvelope
	if read.Code != http.StatusOK ||
		read.Header().Get("ETag") != project.ETag() ||
		json.Unmarshal(read.Body.Bytes(), &readEnvelope) != nil ||
		readEnvelope.Project == nil ||
		readEnvelope.Project.WorkingRevisionID != adminRevision.ID {
		t.Fatalf("Pilot did not observe admin project: %d headers=%v body=%s",
			read.Code, read.Header(), read.Body.String())
	}

	firstBuild := pageProtocolBuild(
		t, handler, token, apiBase, project, adminRevision.ID,
		"protocol-composition-build-1",
	)
	preview := pagePlatformAPIRequest(
		t, handler, token, http.MethodPost, apiBase+"/preview-url",
		map[string]any{"revision_id": adminRevision.ID},
		project.ETag(), "",
	)
	if preview.Code != http.StatusCreated ||
		!strings.Contains(preview.Body.String(), `"preview_url"`) {
		t.Fatalf("composition preview = %d %s",
			preview.Code, preview.Body.String())
	}
	firstPlan := pagePlatformAPIRequest(
		t, handler, token, http.MethodPost, apiBase+"/publish-plan",
		map[string]any{
			"revision_id": adminRevision.ID,
			"build_id":    firstBuild.ID,
		},
		project.ETag(), "",
	)
	if firstPlan.Code != http.StatusOK ||
		!strings.Contains(firstPlan.Body.String(), `"can_execute":true`) ||
		!strings.Contains(firstPlan.Body.String(), `"approval_token_issued":false`) {
		t.Fatalf("first publish plan = %d %s",
			firstPlan.Code, firstPlan.Body.String())
	}
	firstPublish := pageProtocolNativeMutation(
		t, handler, token, apiBase+"/publish", project.ETag(),
		"protocol-composition-publish-1", pageApprovalPublish,
		map[string]any{
			"revision_id": adminRevision.ID,
			"build_id":    firstBuild.ID,
		},
	)
	if firstPublish.Code != http.StatusCreated {
		t.Fatalf("first native publish = %d %s",
			firstPublish.Code, firstPublish.Body.String())
	}

	// Pilot advances the same immutable history. An admin form that retained
	// the old ETag must now fail instead of overwriting the Pilot revision.
	var manifest any
	if err := json.Unmarshal(
		compositionManifestJSON(
			t, "site",
			[]map[string]any{compositionHeroSection("Pilot 第二版")},
		),
		&manifest,
	); err != nil {
		t.Fatal(err)
	}
	revisionResponse := pagePlatformAPIRequest(
		t, handler, token, http.MethodPost, apiBase+"/revisions",
		map[string]any{
			"base_revision_id": adminRevision.ID,
			"page_meta": map[string]any{
				"lang": "zh", "slug": page.Slug, "title": "协议端到端",
			},
			"manifest": manifest,
			"summary":  "Pilot 对话创建第二版",
		},
		project.ETag(), "protocol-composition-revision-2",
	)
	if revisionResponse.Code != http.StatusCreated {
		t.Fatalf("Pilot revision = %d %s",
			revisionResponse.Code, revisionResponse.Body.String())
	}
	var revised pageProjectEnvelope
	if err := json.Unmarshal(revisionResponse.Body.Bytes(), &revised); err != nil ||
		revised.Project == nil || revised.Revision == nil ||
		revised.Revision.Origin != store.PageOriginAPI {
		t.Fatalf("decode Pilot revision: err=%v body=%s",
			err, revisionResponse.Body.String())
	}
	staleAdmin := url.Values{
		"_etag":         {project.ETag()},
		"title":         {page.Title},
		"slug":          {page.Slug},
		"manifest_json": {adminRevision.ManifestJSON},
		"summary":       {"过期后台保存"},
	}
	staleRequest, _ := authedAdminRequest(
		t, s, http.MethodPost,
		"/admin/pages/"+strconv.FormatInt(page.ID, 10)+"/project",
		staleAdmin,
	)
	staleResponse := httptest.NewRecorder()
	handler.ServeHTTP(staleResponse, staleRequest)
	if staleResponse.Code != http.StatusConflict {
		t.Fatalf("stale admin overwrite = %d %s",
			staleResponse.Code, staleResponse.Body.String())
	}

	secondBuild := pageProtocolBuild(
		t, handler, token, apiBase, revised.Project, revised.Revision.ID,
		"protocol-composition-build-2",
	)
	secondPlan := pagePlatformAPIRequest(
		t, handler, token, http.MethodPost, apiBase+"/publish-plan",
		map[string]any{
			"revision_id": revised.Revision.ID,
			"build_id":    secondBuild.ID,
		},
		revised.Project.ETag(), "",
	)
	if secondPlan.Code != http.StatusOK ||
		!strings.Contains(secondPlan.Body.String(), `"can_execute":true`) {
		t.Fatalf("second publish plan = %d %s",
			secondPlan.Code, secondPlan.Body.String())
	}
	secondPublish := pageProtocolNativeMutation(
		t, handler, token, apiBase+"/publish", revised.Project.ETag(),
		"protocol-composition-publish-2", pageApprovalPublish,
		map[string]any{
			"revision_id": revised.Revision.ID,
			"build_id":    secondBuild.ID,
		},
	)
	if secondPublish.Code != http.StatusCreated {
		t.Fatalf("second native publish = %d %s",
			secondPublish.Code, secondPublish.Body.String())
	}

	rollbackPlan := pagePlatformAPIRequest(
		t, handler, token, http.MethodPost, apiBase+"/rollback-plan",
		map[string]any{
			"revision_id": adminRevision.ID,
			"build_id":    firstBuild.ID,
		},
		revised.Project.ETag(), "",
	)
	if rollbackPlan.Code != http.StatusOK ||
		!strings.Contains(rollbackPlan.Body.String(), `"can_execute":true`) {
		t.Fatalf("rollback plan = %d %s",
			rollbackPlan.Code, rollbackPlan.Body.String())
	}
	rollback := pageProtocolNativeMutation(
		t, handler, token, apiBase+"/rollback", revised.Project.ETag(),
		"protocol-composition-rollback-1", pageApprovalRollback,
		map[string]any{
			"revision_id": adminRevision.ID,
			"build_id":    firstBuild.ID,
		},
	)
	if rollback.Code != http.StatusCreated {
		t.Fatalf("native rollback = %d %s",
			rollback.Code, rollback.Body.String())
	}
	current, err := s.store.GetPageProject(project.ID)
	if err != nil || current == nil ||
		current.WorkingRevisionID != revised.Revision.ID ||
		current.PublishedRevisionID != adminRevision.ID {
		t.Fatalf("rollback moved wrong pointers: project=%#v err=%v",
			current, err)
	}

	public := httptest.NewRecorder()
	handler.ServeHTTP(
		public,
		httptest.NewRequest(http.MethodGet, "/zh/"+page.Slug, nil),
	)
	if public.Code != http.StatusOK ||
		!strings.Contains(public.Body.String(), page.Title) ||
		strings.Contains(public.Body.String(), "Pilot 第二版") {
		t.Fatalf("public route did not serve rolled-back revision: %d %s",
			public.Code, public.Body.String())
	}

	var thirdManifest any
	if err := json.Unmarshal(
		compositionManifestJSON(
			t, "site",
			[]map[string]any{compositionHeroSection("Pilot 第三版")},
		),
		&thirdManifest,
	); err != nil {
		t.Fatal(err)
	}
	thirdRevision := pagePlatformAPIRequest(
		t, handler, token, http.MethodPost, apiBase+"/revisions",
		map[string]any{
			"base_revision_id": revised.Revision.ID,
			"page_meta": map[string]any{
				"lang": "zh", "slug": page.Slug, "title": "协议端到端",
			},
			"manifest": thirdManifest,
			"summary":  "发布响应丢失后的并发新修订",
		},
		revised.Project.ETag(), "protocol-composition-revision-3",
	)
	if thirdRevision.Code != http.StatusCreated {
		t.Fatalf("third revision = %d %s",
			thirdRevision.Code, thirdRevision.Body.String())
	}
	restarted, err := New(
		s.store, "https://example.test", "",
		os.DirFS("../.."), os.DirFS("../.."),
	)
	if err != nil {
		t.Fatalf("restart protocol server: %v", err)
	}
	handler = restarted.Handler()

	// A disconnect can lose the success response after the native unlock has
	// already been consumed. Even though another edit has made the original
	// If-Match stale, the durable receipt must make an exact retry safe without
	// prompting for the administrator password again.
	replayRaw, _ := json.Marshal(map[string]any{
		"revision_id": adminRevision.ID,
		"build_id":    firstBuild.ID,
	})
	replayRequest := httptest.NewRequest(
		http.MethodPost, apiBase+"/rollback", bytes.NewReader(replayRaw),
	)
	replayRequest.Header.Set("Authorization", "Bearer "+token)
	replayRequest.Header.Set("Content-Type", "application/json")
	replayRequest.Header.Set(pagePlatformConcurrencyHeader, revised.Project.ETag())
	replayRequest.Header.Set(
		pagePlatformIdempotencyHeader, "protocol-composition-rollback-1",
	)
	replayResponse := httptest.NewRecorder()
	handler.ServeHTTP(replayResponse, replayRequest)
	if replayResponse.Code != http.StatusOK ||
		replayResponse.Header().Get("Idempotent-Replayed") != "true" ||
		!strings.Contains(replayResponse.Body.String(), `"created":false`) {
		t.Fatalf("approval-free durable replay = %d headers=%v body=%s",
			replayResponse.Code, replayResponse.Header(), replayResponse.Body.String())
	}

	// The same key cannot be reused to publish a different revision.
	conflict := pagePlatformAPIRequest(
		t, handler, token, http.MethodPost, apiBase+"/rollback",
		map[string]any{
			"revision_id": revised.Revision.ID,
			"build_id":    secondBuild.ID,
		},
		revised.Project.ETag(), "protocol-composition-rollback-1",
	)
	if conflict.Code != http.StatusConflict ||
		!strings.Contains(conflict.Body.String(), `"error":"idempotency_conflict"`) {
		t.Fatalf("changed durable replay = %d %s",
			conflict.Code, conflict.Body.String())
	}
}
