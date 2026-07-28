package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/store"
)

type pageAppE2EFixture struct {
	Server   *Server
	Handler  http.Handler
	Token    string
	Post     *store.Post
	Project  *store.PageProject
	Revision *store.PageProjectRevision
	Build    *store.PageBuild
}

func pageAppTestScopes() string {
	return strings.Join([]string{
		apiScopePageProjectsRead, apiScopePageProjectsWrite, apiScopePageProjectsBuild,
		apiScopePageAppsWrite, apiScopePagePreviewRead, apiScopePagesPublish,
		apiScopePageCapabilitiesRequest, apiScopePageCapabilitiesGrant,
		apiScopeControlUnlock,
	}, ",")
}

func pageAppMultipartRequest(
	t *testing.T,
	handler http.Handler,
	token, requestPath, etag, requestID string,
	raw []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("package", "app.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("summary", "E2E 应用包"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, requestPath, &body)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set(pagePlatformConcurrencyHeader, etag)
	request.Header.Set(pagePlatformIdempotencyHeader, requestID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func createPageAppE2EFixture(
	t *testing.T,
	files map[string]string,
	slug string,
) pageAppE2EFixture {
	t.Helper()
	server := newTestPublicServer(t, "")
	token, prefix := newAutomationToken()
	if _, err := server.store.CreateAutomationKey("page-app-e2e", token, prefix, pageAppTestScopes()); err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	postID, err := server.store.CreatePost(&store.Post{
		Type: "page", Lang: "zh", Slug: slug, Title: "互动应用 E2E",
		Content: "旧标准正文", Status: "draft", TransGroup: "zh:" + slug,
	})
	if err != nil {
		t.Fatal(err)
	}
	post, err := server.store.GetPostByID(postID)
	if err != nil {
		t.Fatal(err)
	}
	plan := pagePlatformAPIRequest(t, handler, token, http.MethodPost,
		"/api/admin/v1/pages/"+strconv.FormatInt(postID, 10)+"/convert-plan",
		map[string]any{}, "", "")
	if plan.Code != http.StatusOK {
		t.Fatalf("convert plan = %d %s", plan.Code, plan.Body.String())
	}
	convert := pagePlatformAPIRequest(t, handler, token, http.MethodPost,
		"/api/admin/v1/pages/"+strconv.FormatInt(postID, 10)+"/convert",
		map[string]any{"mode": store.PageModeApp, "schema_version": 1, "shell_mode": store.PageShellSite},
		plan.Header().Get("ETag"), "app-convert-"+slug)
	if convert.Code != http.StatusCreated {
		t.Fatalf("convert app = %d %s", convert.Code, convert.Body.String())
	}
	var converted pageProjectEnvelope
	if err := json.Unmarshal(convert.Body.Bytes(), &converted); err != nil {
		t.Fatal(err)
	}
	packageResponse := pageAppMultipartRequest(
		t, handler, token,
		"/api/admin/v1/page-projects/"+strconv.FormatInt(converted.Project.ID, 10)+"/app-package",
		converted.Project.ETag(), "app-upload-"+slug, pageAppZipForTest(t, files),
	)
	if packageResponse.Code != http.StatusCreated {
		t.Fatalf("upload app = %d %s", packageResponse.Code, packageResponse.Body.String())
	}
	var uploaded pageProjectEnvelope
	if err := json.Unmarshal(packageResponse.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}
	if uploaded.Project == nil || uploaded.Revision == nil ||
		uploaded.Revision.SourceBundleRef == "" || uploaded.Revision.SourceHash == "" {
		t.Fatalf("upload envelope = %#v", uploaded)
	}
	buildResponse := pagePlatformAPIRequest(t, handler, token, http.MethodPost,
		"/api/admin/v1/page-projects/"+strconv.FormatInt(uploaded.Project.ID, 10)+"/builds",
		map[string]any{"revision_id": uploaded.Revision.ID},
		uploaded.Project.ETag(), "app-build-"+slug)
	if buildResponse.Code != http.StatusCreated {
		t.Fatalf("build app = %d %s", buildResponse.Code, buildResponse.Body.String())
	}
	var built struct {
		Build *store.PageBuild `json:"build"`
	}
	if err := json.Unmarshal(buildResponse.Body.Bytes(), &built); err != nil || built.Build == nil {
		t.Fatalf("decode build: %v body=%s", err, buildResponse.Body.String())
	}
	return pageAppE2EFixture{
		Server: server, Handler: handler, Token: token, Post: post,
		Project: uploaded.Project, Revision: uploaded.Revision, Build: built.Build,
	}
}

func TestPageAppBuildIdempotencyBindsRevisionAndSourceArtifact(t *testing.T) {
	const slug = "app-build-idempotency"
	fixture := createPageAppE2EFixture(t, validPageAppFilesForTest(), slug)
	base := "/api/admin/v1/page-projects/" + strconv.FormatInt(fixture.Project.ID, 10)
	body := map[string]any{"revision_id": fixture.Revision.ID}
	const originalKey = "app-build-" + slug

	replay := pagePlatformAPIRequest(
		t, fixture.Handler, fixture.Token, http.MethodPost, base+"/builds",
		body, fixture.Project.ETag(), originalKey,
	)
	var replayEnvelope struct {
		Build *store.PageBuild `json:"build"`
	}
	if replay.Code != http.StatusOK ||
		replay.Header().Get("Idempotent-Replayed") != "true" ||
		json.Unmarshal(replay.Body.Bytes(), &replayEnvelope) != nil ||
		replayEnvelope.Build == nil || replayEnvelope.Build.ID != fixture.Build.ID {
		t.Fatalf("same-key app replay = %d headers=%v body=%s",
			replay.Code, replay.Header(), replay.Body.String())
	}
	freshKey := pagePlatformAPIRequest(
		t, fixture.Handler, fixture.Token, http.MethodPost, base+"/builds",
		body, fixture.Project.ETag(), "app-build-fresh-key",
	)
	if freshKey.Code != http.StatusOK ||
		freshKey.Header().Get("Idempotent-Replayed") != "" ||
		!strings.Contains(freshKey.Body.String(), `"created":false`) {
		t.Fatalf("fresh-key app artifact reuse mislabeled = %d headers=%v body=%s",
			freshKey.Code, freshKey.Header(), freshKey.Body.String())
	}

	changedFiles := validPageAppFilesForTest()
	changedFiles["app.js"] = `document.body.dataset.buildIdentity = "changed";`
	uploaded := pageAppMultipartRequest(
		t, fixture.Handler, fixture.Token, base+"/app-package",
		fixture.Project.ETag(), "app-build-idempotency-upload-2",
		pageAppZipForTest(t, changedFiles),
	)
	if uploaded.Code != http.StatusCreated {
		t.Fatalf("upload changed app = %d %s", uploaded.Code, uploaded.Body.String())
	}
	var changed pageProjectEnvelope
	if err := json.Unmarshal(uploaded.Body.Bytes(), &changed); err != nil ||
		changed.Project == nil || changed.Revision == nil ||
		changed.Revision.SourceHash == fixture.Revision.SourceHash {
		t.Fatalf("decode changed app: err=%v body=%s", err, uploaded.Body.String())
	}
	conflict := pagePlatformAPIRequest(
		t, fixture.Handler, fixture.Token, http.MethodPost, base+"/builds",
		map[string]any{"revision_id": changed.Revision.ID},
		changed.Project.ETag(), originalKey,
	)
	if conflict.Code != http.StatusConflict ||
		!strings.Contains(conflict.Body.String(), `"error":"idempotency_conflict"`) {
		t.Fatalf("same key with changed app source = %d %s",
			conflict.Code, conflict.Body.String())
	}
	conflictingArtifact := filepath.Join(
		fixture.Server.store.PageProjectStorageDir(), "artifacts",
		strconv.FormatInt(fixture.Project.ID, 10),
		strconv.FormatInt(changed.Revision.ID, 10), changed.Revision.SourceHash,
	)
	if _, err := os.Stat(conflictingArtifact); !os.IsNotExist(err) {
		t.Fatalf("idempotency conflict wrote app artifact: path=%s err=%v",
			conflictingArtifact, err)
	}
	rebuilt := pagePlatformAPIRequest(
		t, fixture.Handler, fixture.Token, http.MethodPost, base+"/builds",
		map[string]any{"revision_id": changed.Revision.ID},
		changed.Project.ETag(), "app-build-idempotency-after-change",
	)
	if rebuilt.Code != http.StatusCreated ||
		!strings.Contains(rebuilt.Body.String(), `"created":true`) {
		t.Fatalf("new key for changed app = %d %s", rebuilt.Code, rebuilt.Body.String())
	}
}

func pageAppFilesWithoutCapabilities() map[string]string {
	files := validPageAppFilesForTest()
	files[pageAppManifestName] = `{"schema_version":1,"entry":"index.html","viewport":"responsive","shell_mode":"site"}`
	return files
}

func TestPageAppUploadBuildPreviewPublishPublicAndStaticExport(t *testing.T) {
	fixture := createPageAppE2EFixture(t, pageAppFilesWithoutCapabilities(), "app-e2e")
	base := "/api/admin/v1/page-projects/" + strconv.FormatInt(fixture.Project.ID, 10)

	previewResponse := pagePlatformAPIRequest(t, fixture.Handler, fixture.Token, http.MethodPost,
		base+"/preview-url", map[string]any{
			"revision_id": fixture.Revision.ID, "build_id": fixture.Build.ID,
		}, fixture.Project.ETag(), "")
	if previewResponse.Code != http.StatusCreated {
		t.Fatalf("preview URL = %d %s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview struct {
		URL string `json:"preview_url"`
	}
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	previewURL, err := url.Parse(preview.URL)
	if err != nil {
		t.Fatal(err)
	}
	previewPage := httptest.NewRecorder()
	fixture.Handler.ServeHTTP(previewPage, httptest.NewRequest(http.MethodGet, previewURL.RequestURI(), nil))
	if previewPage.Code != http.StatusOK ||
		!strings.Contains(previewPage.Body.String(), `sandbox="allow-scripts"`) ||
		strings.Contains(previewPage.Body.String(), "allow-same-origin") ||
		!strings.Contains(previewPage.Body.String(), "草稿预览") ||
		!strings.Contains(previewPage.Header().Get("Content-Security-Policy"), "frame-src 'self'") ||
		!strings.Contains(previewPage.Header().Get("Content-Security-Policy"), "connect-src 'self'") ||
		previewPage.Header().Get("Permissions-Policy") == "" {
		t.Fatalf("preview shell = %d %s", previewPage.Code, previewPage.Body.String())
	}
	frameSource := htmlAttributeForTest(previewPage.Body.String(), `id="gcms-page-app"`, "src")
	if frameSource == "" {
		t.Fatalf("preview frame src missing: %s", previewPage.Body.String())
	}
	previewAsset := httptest.NewRecorder()
	fixture.Handler.ServeHTTP(previewAsset, httptest.NewRequest(http.MethodGet, frameSource, nil))
	if previewAsset.Code != http.StatusOK ||
		!strings.Contains(previewAsset.Header().Get("Content-Security-Policy"), "connect-src 'none'") ||
		previewAsset.Header().Get("X-Content-Type-Options") != "nosniff" ||
		previewAsset.Header().Get("Set-Cookie") != "" {
		t.Fatalf("preview asset = %d headers=%v body=%s", previewAsset.Code, previewAsset.Header(), previewAsset.Body.String())
	}

	approval, err := fixture.Server.issuePageApprovalToken(
		fixture.Project.ID, fixture.Revision.ID, pageApprovalPublish, "admin:e2e", time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	published := pagePlatformAPIRequest(t, fixture.Handler, fixture.Token, http.MethodPost,
		base+"/publish", map[string]any{
			"revision_id": fixture.Revision.ID, "build_id": fixture.Build.ID,
			"approval_token": approval.ApprovalToken,
		}, fixture.Project.ETag(), "app-publish-e2e")
	if published.Code != http.StatusCreated {
		t.Fatalf("publish app = %d %s", published.Code, published.Body.String())
	}
	public := httptest.NewRecorder()
	fixture.Handler.ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/zh/app-e2e", nil))
	if public.Code != http.StatusOK || !strings.Contains(public.Body.String(), "/_gcms/page-apps/") ||
		!strings.Contains(public.Body.String(), `sandbox="allow-scripts"`) ||
		strings.Contains(public.Body.String(), "旧标准正文") ||
		!strings.Contains(public.Header().Get("Content-Security-Policy"), "default-src 'none'") ||
		!strings.Contains(public.Header().Get("Content-Security-Policy"), "frame-src 'self'") ||
		public.Header().Get("Permissions-Policy") == "" ||
		public.Header().Get("Set-Cookie") != "" {
		t.Fatalf("public app = %d %s", public.Code, public.Body.String())
	}
	publicAssetPath := "/_gcms/page-apps/" + strconv.FormatInt(fixture.Project.ID, 10) +
		"/" + strconv.FormatInt(fixture.Revision.ID, 10) + "/app.js"
	publicAsset := httptest.NewRecorder()
	fixture.Handler.ServeHTTP(publicAsset, httptest.NewRequest(http.MethodGet, publicAssetPath, nil))
	if publicAsset.Code != http.StatusOK ||
		!strings.Contains(publicAsset.Body.String(), `textContent = "ready"`) ||
		publicAsset.Header().Get("Permissions-Policy") == "" ||
		publicAsset.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" ||
		publicAsset.Header().Get("Set-Cookie") != "" {
		t.Fatalf("public asset = %d headers=%v body=%s", publicAsset.Code, publicAsset.Header(), publicAsset.Body.String())
	}
	wrongRevision := httptest.NewRecorder()
	fixture.Handler.ServeHTTP(wrongRevision, httptest.NewRequest(
		http.MethodGet,
		"/_gcms/page-apps/"+strconv.FormatInt(fixture.Project.ID, 10)+"/999999/app.js",
		nil,
	))
	if wrongRevision.Code != http.StatusNotFound {
		t.Fatalf("cross revision asset = %d %s", wrongRevision.Code, wrongRevision.Body.String())
	}

	result, err := fixture.Server.exportStaticSite(context.Background(), CloudflareConfig{
		DeployMode:   cloudflareModePages,
		RoutePattern: "example.test/*",
		Domains:      []CloudflareDomain{{Host: "example.test", Primary: true}},
		DefaultLang:  "zh", Locales: []string{"zh", "en"},
	})
	if err != nil {
		t.Fatalf("static export: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(result.Dir) })
	for _, exported := range []string{
		"zh/app-e2e/index.html",
		strings.TrimPrefix(publicAssetPath, "/"),
		"_headers",
	} {
		if _, err := os.Stat(filepath.Join(result.Dir, filepath.FromSlash(exported))); err != nil {
			t.Fatalf("static export missing %s: %v", exported, err)
		}
	}
	headers, err := os.ReadFile(filepath.Join(result.Dir, "_headers"))
	if err != nil ||
		!strings.Contains(string(headers), "/zh/app-e2e/*") ||
		!strings.Contains(string(headers), "Content-Security-Policy: default-src 'none'") ||
		!strings.Contains(string(headers), "Permissions-Policy:") ||
		!strings.Contains(string(headers), "/_gcms/page-apps/*") {
		t.Fatalf("static app headers = %q err=%v", headers, err)
	}
}

func htmlAttributeForTest(body, marker, attribute string) string {
	index := strings.Index(body, marker)
	if index < 0 {
		return ""
	}
	tail := body[index:]
	prefix := attribute + `="`
	index = strings.Index(tail, prefix)
	if index < 0 {
		return ""
	}
	tail = tail[index+len(prefix):]
	end := strings.IndexByte(tail, '"')
	if end < 0 {
		return ""
	}
	return tail[:end]
}

func TestPageAppCapabilityApprovalBridgeAndImmediateRevocation(t *testing.T) {
	fixture := createPageAppE2EFixture(t, validPageAppFilesForTest(), "app-capability")
	base := "/api/admin/v1/page-projects/" + strconv.FormatInt(fixture.Project.ID, 10)
	requested := pagePlatformAPIRequest(t, fixture.Handler, fixture.Token, http.MethodPost,
		base+"/capabilities/request", map[string]any{
			"capability": "client.storage", "config": map[string]any{"max_bytes": 4096},
		}, fixture.Project.ETag(), "cap-request-1")
	if requested.Code != http.StatusCreated {
		t.Fatalf("request capability = %d %s", requested.Code, requested.Body.String())
	}
	requestedGrant, err := fixture.Server.store.GetPageCapabilityGrant(
		fixture.Project.ID, "client.storage",
	)
	if err != nil || requestedGrant == nil {
		t.Fatalf("read requested grant: %#v err=%v", requestedGrant, err)
	}
	approvedGrant, err := fixture.Server.store.UpsertPageCapabilityGrant(
		store.UpsertPageCapabilityGrantInput{
			ProjectID: fixture.Project.ID, Capability: "client.storage",
			ConfigJSON: requestedGrant.ConfigJSON, Status: store.PageCapabilityApproved,
			RequestedBy: requestedGrant.RequestedBy, ApprovedBy: "test-admin",
		},
	)
	if err != nil {
		t.Fatalf("approve test capability: %v", err)
	}
	if approvedGrant == nil || approvedGrant.Status != store.PageCapabilityApproved {
		t.Fatalf("approved test grant = %#v", approvedGrant)
	}
	platformList := pagePlatformAPIRequest(t, fixture.Handler, fixture.Token, http.MethodGet,
		"/api/platform/v1/sites/77/page-projects/"+strconv.FormatInt(fixture.Project.ID, 10)+"/capabilities",
		nil, "", "")
	if platformList.Code != http.StatusOK ||
		!strings.Contains(platformList.Body.String(), `"status":"approved"`) {
		t.Fatalf("platform capability mirror = %d %s", platformList.Code, platformList.Body.String())
	}

	_, bridgeToken, err := fixture.Server.newPageAppRuntimeClaims(
		pageAppPreviewBridgeAudience, fixture.Project, fixture.Revision, fixture.Build,
		time.Now().Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	bridgePath := "/preview/page-app-bridge/" + strconv.FormatInt(fixture.Project.ID, 10) +
		"/" + strconv.FormatInt(fixture.Revision.ID, 10)
	callBridge := func(requestID string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(pageAppBridgeRequest{
			Protocol: pageAppBridgeProtocol, RequestID: requestID,
			ProjectID: fixture.Project.ID, RevisionID: fixture.Revision.ID,
			Capability: "client.storage", Action: "set",
			Payload: json.RawMessage(`{"key":"score","value":42}`),
		})
		request := httptest.NewRequest(http.MethodPost, bridgePath, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(pageAppRuntimeTokenHeader, bridgeToken)
		response := httptest.NewRecorder()
		fixture.Handler.ServeHTTP(response, request)
		return response
	}
	before := callBridge("bridge-before-revoke")
	if before.Code != http.StatusOK || !strings.Contains(before.Body.String(), `"authorized":true`) {
		t.Fatalf("bridge before revoke = %d %s", before.Code, before.Body.String())
	}
	revoked := pagePlatformAPIRequest(t, fixture.Handler, fixture.Token, http.MethodPost,
		base+"/capabilities/revoke", map[string]any{"capability": "client.storage"},
		fixture.Project.ETag(), "cap-revoke-1")
	if revoked.Code != http.StatusOK ||
		!strings.Contains(revoked.Body.String(), `"status":"revoked"`) {
		t.Fatalf("revoke capability = %d %s", revoked.Code, revoked.Body.String())
	}
	after := callBridge("bridge-after-revoke")
	if after.Code != http.StatusForbidden ||
		!strings.Contains(after.Body.String(), "bridge_capability_not_granted") {
		t.Fatalf("bridge after revoke = %d %s", after.Code, after.Body.String())
	}
}

func TestPageAppCapabilityNativeApprovalIsTargetBoundAndReplaySafe(t *testing.T) {
	fixture := createPageAppE2EFixture(t, validPageAppFilesForTest(), "app-capability-native")
	setSingleTestPassword(t, fixture.Server, controlTestPassword)
	base := "/api/admin/v1/page-projects/" + strconv.FormatInt(fixture.Project.ID, 10)
	requested := pagePlatformAPIRequest(t, fixture.Handler, fixture.Token, http.MethodPost,
		base+"/capabilities/request", map[string]any{
			"capability": "client.storage", "config": map[string]any{"max_bytes": 4096},
		}, fixture.Project.ETag(), "cap-native-request")
	if requested.Code != http.StatusCreated {
		t.Fatalf("request capability = %d %s", requested.Code, requested.Body.String())
	}
	injectedToken := pagePlatformAPIRequest(t, fixture.Handler, fixture.Token, http.MethodPost,
		base+"/capabilities/apply", map[string]any{
			"capability": "client.storage", "decision": "approve",
			"approval_token": "AI-visible-tokens-are-not-accepted",
		}, fixture.Project.ETag(), "cap-native-injected-token")
	if injectedToken.Code != http.StatusBadRequest ||
		!strings.Contains(injectedToken.Body.String(), `"error":"bad_json"`) {
		t.Fatalf("public capability DTO accepted approval token = %d %s",
			injectedToken.Code, injectedToken.Body.String())
	}
	applyBody := map[string]any{
		"capability": "client.storage", "decision": "approve",
	}
	blocked := pagePlatformAPIRequest(t, fixture.Handler, fixture.Token, http.MethodPost,
		base+"/capabilities/apply", applyBody, fixture.Project.ETag(), "cap-native-apply")
	if blocked.Code != http.StatusConflict ||
		!strings.Contains(blocked.Body.String(), `"unlock_required":true`) ||
		!strings.Contains(blocked.Body.String(), `"operation":"page_capabilities.grant"`) ||
		strings.Contains(blocked.Body.String(), "approval_token") ||
		strings.Contains(blocked.Body.String(), controlTestPassword) {
		t.Fatalf("blocked capability approval = %d %s", blocked.Code, blocked.Body.String())
	}
	var challenge struct {
		Token      string `json:"unlock_challenge"`
		SiteID     int64  `json:"site_id"`
		PageID     int64  `json:"page_id"`
		ProjectID  int64  `json:"project_id"`
		RevisionID int64  `json:"revision_id"`
		Capability string `json:"capability"`
		ConfigHash string `json:"config_hash"`
		Decision   string `json:"decision"`
		ETag       string `json:"etag"`
		RequestID  string `json:"request_id"`
	}
	if err := json.Unmarshal(blocked.Body.Bytes(), &challenge); err != nil ||
		challenge.Token == "" ||
		challenge.ProjectID != fixture.Project.ID ||
		challenge.RevisionID != fixture.Project.WorkingRevisionID ||
		challenge.Capability != "client.storage" ||
		challenge.Decision != store.PageCapabilityApproved ||
		len(challenge.ConfigHash) != sha256.Size*2 ||
		challenge.ETag != fixture.Project.ETag() ||
		challenge.RequestID != "cap-native-apply" {
		t.Fatalf("decode target-bound challenge: err=%v body=%s", err, blocked.Body.String())
	}

	unlockRaw, _ := json.Marshal(map[string]any{
		"password": controlTestPassword,
		"operations": []string{
			pageCapabilityGrant,
		},
		"page_challenge": challenge.Token,
	})
	unlockRequest := httptest.NewRequest(
		http.MethodPost, "https://site.test/api/admin/v1/control/unlock",
		bytes.NewReader(unlockRaw),
	)
	unlockRequest.Header.Set("Authorization", "Bearer "+fixture.Token)
	unlockRequest.Header.Set("Content-Type", "application/json")
	unlockRequest.Header.Set(controlUIRequestHeader, controlUIPilotValue)
	unlockResponse := httptest.NewRecorder()
	fixture.Handler.ServeHTTP(unlockResponse, unlockRequest)
	if unlockResponse.Code != http.StatusCreated ||
		strings.Contains(unlockResponse.Body.String(), controlTestPassword) ||
		strings.Contains(unlockResponse.Body.String(), "cap_approval_") {
		t.Fatalf("native unlock = %d %s", unlockResponse.Code, unlockResponse.Body.String())
	}
	var unlocked struct {
		Token string `json:"unlock_token"`
	}
	if err := json.Unmarshal(unlockResponse.Body.Bytes(), &unlocked); err != nil ||
		!strings.HasPrefix(unlocked.Token, "gcmsup_") {
		t.Fatalf("decode unlock: err=%v body=%s", err, unlockResponse.Body.String())
	}
	reusedChallengeRequest := unlockRequest.Clone(unlockRequest.Context())
	reusedChallengeRequest.Body = io.NopCloser(bytes.NewReader(unlockRaw))
	reusedChallenge := httptest.NewRecorder()
	fixture.Handler.ServeHTTP(reusedChallenge, reusedChallengeRequest)
	if reusedChallenge.Code != http.StatusConflict ||
		!strings.Contains(reusedChallenge.Body.String(), "unlock_target_changed") {
		t.Fatalf("challenge replay = %d %s",
			reusedChallenge.Code, reusedChallenge.Body.String())
	}

	target := pageApprovalConsumeInput{
		SiteID: challenge.SiteID, PageID: challenge.PageID,
		ProjectID: challenge.ProjectID, RevisionID: challenge.RevisionID,
		Operation: pageCapabilityGrant, ETag: challenge.ETag,
		RequestID: challenge.RequestID, Capability: challenge.Capability,
		ConfigHash: challenge.ConfigHash, Decision: challenge.Decision,
	}
	exactAuth := &automationAuth{key: &store.AutomationKey{ID: 1}}
	hiddenApprovalToken, state := resolveNativePageApproval(
		fixture.Server, exactAuth, unlocked.Token, target,
	)
	if state != "" || hiddenApprovalToken == "" {
		t.Fatalf("resolve exact target state=%q token=%q", state, hiddenApprovalToken)
	}
	for name, mutate := range map[string]func(*pageApprovalConsumeInput){
		"site":       func(v *pageApprovalConsumeInput) { v.SiteID++ },
		"project":    func(v *pageApprovalConsumeInput) { v.ProjectID++ },
		"revision":   func(v *pageApprovalConsumeInput) { v.RevisionID++ },
		"capability": func(v *pageApprovalConsumeInput) { v.Capability = "content.read" },
		"config":     func(v *pageApprovalConsumeInput) { v.ConfigHash = strings.Repeat("a", 64) },
		"decision":   func(v *pageApprovalConsumeInput) { v.Decision = store.PageCapabilityDenied },
		"etag":       func(v *pageApprovalConsumeInput) { v.ETag = `"other"` },
		"request":    func(v *pageApprovalConsumeInput) { v.RequestID = "other-request" },
	} {
		t.Run("cross-target-"+name, func(t *testing.T) {
			changed := target
			mutate(&changed)
			if _, state := resolveNativePageApproval(
				fixture.Server, exactAuth, unlocked.Token, changed,
			); state != "mismatch" {
				t.Fatalf("state=%q, want mismatch", state)
			}
		})
	}
	if _, state := resolveNativePageApproval(
		fixture.Server,
		&automationAuth{key: &store.AutomationKey{ID: 999}},
		unlocked.Token,
		target,
	); state != "mismatch" {
		t.Fatalf("cross-subject state=%q, want mismatch", state)
	}

	applyRaw, _ := json.Marshal(applyBody)
	applyRequest := httptest.NewRequest(http.MethodPost, base+"/capabilities/apply", bytes.NewReader(applyRaw))
	applyRequest.Header.Set("Authorization", "Bearer "+fixture.Token)
	applyRequest.Header.Set("Content-Type", "application/json")
	applyRequest.Header.Set(pagePlatformConcurrencyHeader, fixture.Project.ETag())
	applyRequest.Header.Set(pagePlatformIdempotencyHeader, "cap-native-apply")
	applyRequest.Header.Set(controlUnlockHeader, unlocked.Token)
	applied := httptest.NewRecorder()
	fixture.Handler.ServeHTTP(applied, applyRequest)
	if applied.Code != http.StatusOK ||
		!strings.Contains(applied.Body.String(), `"status":"approved"`) {
		t.Fatalf("approved capability = %d %s", applied.Code, applied.Body.String())
	}
	if _, approved := consumePageAppCapabilityApprovalToken(
		fixture.Server, hiddenApprovalToken, target, pageAutomationActor(exactAuth),
	); approved {
		t.Fatal("one-time capability approval token was reusable after apply")
	}
	replayRequest := applyRequest.Clone(applyRequest.Context())
	replayRequest.Body = io.NopCloser(bytes.NewReader(applyRaw))
	replayed := httptest.NewRecorder()
	fixture.Handler.ServeHTTP(replayed, replayRequest)
	if replayed.Code != http.StatusOK ||
		replayed.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("durable replay = %d headers=%v body=%s",
			replayed.Code, replayed.Header(), replayed.Body.String())
	}
}

func TestPageAppCapabilityNativeApprovalRejectsRequestedConfigTOCTOU(t *testing.T) {
	fixture := createPageAppE2EFixture(t, validPageAppFilesForTest(), "app-capability-toctou")
	setSingleTestPassword(t, fixture.Server, controlTestPassword)
	base := "/api/admin/v1/page-projects/" + strconv.FormatInt(fixture.Project.ID, 10)
	requestCapability := func(maxBytes int, requestID string) *httptest.ResponseRecorder {
		return pagePlatformAPIRequest(t, fixture.Handler, fixture.Token, http.MethodPost,
			base+"/capabilities/request", map[string]any{
				"capability": "client.storage", "config": map[string]any{"max_bytes": maxBytes},
			}, fixture.Project.ETag(), requestID)
	}
	if response := requestCapability(4096, "cap-toctou-request-1"); response.Code != http.StatusCreated {
		t.Fatalf("first request = %d %s", response.Code, response.Body.String())
	}
	blocked := pagePlatformAPIRequest(t, fixture.Handler, fixture.Token, http.MethodPost,
		base+"/capabilities/apply", map[string]any{
			"capability": "client.storage", "decision": "approve",
		}, fixture.Project.ETag(), "cap-toctou-apply")
	var challenge struct {
		Token string `json:"unlock_challenge"`
	}
	if blocked.Code != http.StatusConflict ||
		json.Unmarshal(blocked.Body.Bytes(), &challenge) != nil ||
		challenge.Token == "" {
		t.Fatalf("blocked = %d %s", blocked.Code, blocked.Body.String())
	}
	if response := requestCapability(8192, "cap-toctou-request-2"); response.Code != http.StatusCreated {
		t.Fatalf("changed request = %d %s", response.Code, response.Body.String())
	}
	unlockRaw, _ := json.Marshal(map[string]any{
		"password": controlTestPassword,
		"operations": []string{
			pageCapabilityGrant,
		},
		"page_challenge": challenge.Token,
	})
	unlockRequest := httptest.NewRequest(
		http.MethodPost, "https://site.test/api/admin/v1/control/unlock",
		bytes.NewReader(unlockRaw),
	)
	unlockRequest.Header.Set("Authorization", "Bearer "+fixture.Token)
	unlockRequest.Header.Set("Content-Type", "application/json")
	unlockRequest.Header.Set(controlUIRequestHeader, controlUIPilotValue)
	unlockResponse := httptest.NewRecorder()
	fixture.Handler.ServeHTTP(unlockResponse, unlockRequest)
	if unlockResponse.Code != http.StatusConflict ||
		!strings.Contains(unlockResponse.Body.String(), "unlock_target_changed") {
		t.Fatalf("changed config unlock = %d %s", unlockResponse.Code, unlockResponse.Body.String())
	}
}

func TestPageAppRejectsClientSourceRefAndDetectsTamperedSource(t *testing.T) {
	fixture := createPageAppE2EFixture(t, pageAppFilesWithoutCapabilities(), "app-source-boundary")
	base := "/api/admin/v1/page-projects/" + strconv.FormatInt(fixture.Project.ID, 10)
	arbitrary := pagePlatformAPIRequest(t, fixture.Handler, fixture.Token, http.MethodPost,
		base+"/revisions", map[string]any{
			"base_revision_id":  fixture.Revision.ID,
			"page_meta":         json.RawMessage(fixture.Revision.PageMetaJSON),
			"manifest":          json.RawMessage(fixture.Revision.ManifestJSON),
			"source_bundle_ref": "../../uploads/attacker",
			"source_hash":       strings.Repeat("a", 64),
		}, fixture.Project.ETag(), "app-arbitrary-source")
	if arbitrary.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(arbitrary.Body.String(), "app_package_required") {
		t.Fatalf("arbitrary source ref = %d %s", arbitrary.Code, arbitrary.Body.String())
	}

	sourceFile := filepath.Join(
		fixture.Server.store.PageProjectStorageDir(),
		filepath.FromSlash(fixture.Revision.SourceBundleRef),
		"app.js",
	)
	if err := os.WriteFile(sourceFile, []byte(`document.body.textContent="tampered"`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Remove the already ready build from the test target by creating a fresh
	// uploaded revision, then tamper before its first build.
	files := pageAppFilesWithoutCapabilities()
	files["app.js"] = `document.body.textContent="revision-two"`
	upload := pageAppMultipartRequest(
		t, fixture.Handler, fixture.Token, base+"/app-package", fixture.Project.ETag(),
		"app-upload-tamper-two", pageAppZipForTest(t, files),
	)
	if upload.Code != http.StatusCreated {
		t.Fatalf("upload second app = %d %s", upload.Code, upload.Body.String())
	}
	var second pageProjectEnvelope
	if err := json.Unmarshal(upload.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	secondSource := filepath.Join(
		fixture.Server.store.PageProjectStorageDir(),
		filepath.FromSlash(second.Revision.SourceBundleRef),
		"app.js",
	)
	if err := os.WriteFile(secondSource, []byte(`document.body.textContent="tampered-two"`), 0o644); err != nil {
		t.Fatal(err)
	}
	build := pagePlatformAPIRequest(t, fixture.Handler, fixture.Token, http.MethodPost,
		base+"/builds", map[string]any{"revision_id": second.Revision.ID},
		second.Project.ETag(), "app-build-tampered")
	if build.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(build.Body.String(), "source_hash_mismatch") {
		t.Fatalf("tampered build = %d %s", build.Code, build.Body.String())
	}
	builds, err := fixture.Server.store.ListPageBuilds(fixture.Project.ID, second.Revision.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range builds {
		if candidate.Status == store.PageBuildReady {
			t.Fatalf("tampered source produced ready build: %#v", candidate)
		}
	}
}

func TestPageAppTextSourceEditCreatesImmutableRevisionAndRevalidatesBundle(t *testing.T) {
	fixture := createPageAppE2EFixture(t, pageAppFilesWithoutCapabilities(), "app-text-edit")
	base := "/api/admin/v1/page-projects/" + strconv.FormatInt(fixture.Project.ID, 10)
	filePath := base + "/app-files/app.js"

	read := pagePlatformAPIRequest(
		t, fixture.Handler, fixture.Token, http.MethodGet, filePath, nil, "", "",
	)
	if read.Code != http.StatusOK ||
		!strings.Contains(read.Body.String(), `"content":"document.querySelector`) ||
		read.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("read source = %d headers=%v body=%s", read.Code, read.Header(), read.Body.String())
	}
	platformRead := pagePlatformAPIRequest(
		t, fixture.Handler, fixture.Token, http.MethodGet,
		"/api/platform/v1/sites/77/page-projects/"+
			strconv.FormatInt(fixture.Project.ID, 10)+"/app-files/app.js",
		nil, "", "",
	)
	if platformRead.Code != http.StatusOK ||
		!strings.Contains(platformRead.Body.String(), `"content":"document.querySelector`) {
		t.Fatalf("platform source mirror = %d %s", platformRead.Code, platformRead.Body.String())
	}

	editedSource := `document.querySelector("#app").textContent = "edited safely";`
	editBody := map[string]any{
		"base_revision_id": fixture.Revision.ID,
		"content":          editedSource,
		"summary":          "E2E 编辑脚本",
	}
	edited := pagePlatformAPIRequest(
		t, fixture.Handler, fixture.Token, http.MethodPut, filePath,
		editBody, fixture.Project.ETag(), "app-text-edit-1",
	)
	if edited.Code != http.StatusCreated {
		t.Fatalf("edit source = %d %s", edited.Code, edited.Body.String())
	}
	var envelope pageProjectEnvelope
	if err := json.Unmarshal(edited.Body.Bytes(), &envelope); err != nil ||
		envelope.Project == nil || envelope.Revision == nil {
		t.Fatalf("decode edited source: %v body=%s", err, edited.Body.String())
	}
	if envelope.Revision.ID == fixture.Revision.ID ||
		envelope.Revision.ParentRevisionID != fixture.Revision.ID ||
		envelope.Revision.SourceHash == fixture.Revision.SourceHash {
		t.Fatalf("source edit did not create a new immutable revision: %#v", envelope.Revision)
	}
	oldRaw, _, err := fixture.Server.pageAppAdminSourceFile(
		fixture.Project.ID, fixture.Revision.ID, "app.js",
	)
	if err != nil || string(oldRaw) == editedSource {
		t.Fatalf("old immutable source changed: raw=%q err=%v", oldRaw, err)
	}
	newRaw, _, err := fixture.Server.pageAppAdminSourceFile(
		fixture.Project.ID, envelope.Revision.ID, "app.js",
	)
	if err != nil || string(newRaw) != editedSource {
		t.Fatalf("new source = %q err=%v", newRaw, err)
	}

	replayed := pagePlatformAPIRequest(
		t, fixture.Handler, fixture.Token, http.MethodPut, filePath,
		editBody, fixture.Project.ETag(), "app-text-edit-1",
	)
	if replayed.Code != http.StatusOK ||
		replayed.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("source edit replay = %d headers=%v body=%s",
			replayed.Code, replayed.Header(), replayed.Body.String())
	}
	stale := pagePlatformAPIRequest(
		t, fixture.Handler, fixture.Token, http.MethodPut, filePath,
		editBody, fixture.Project.ETag(), "app-text-edit-stale",
	)
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "revision_conflict") {
		t.Fatalf("stale source edit = %d %s", stale.Code, stale.Body.String())
	}

	currentProject, err := fixture.Server.store.GetPageProject(fixture.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := base + "/app-files/" + pageAppManifestName
	badManifest := pagePlatformAPIRequest(
		t, fixture.Handler, fixture.Token, http.MethodPut, manifestPath,
		map[string]any{
			"base_revision_id": envelope.Revision.ID,
			"content":          `{"schema_version":1,"entry":"index.html","shell_mode":"none"}`,
		},
		currentProject.ETag(), "app-text-edit-bad-manifest",
	)
	if badManifest.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(badManifest.Body.String(), "shell_mode_mismatch") {
		t.Fatalf("manifest shell revalidation = %d %s", badManifest.Code, badManifest.Body.String())
	}
	badCapability := pagePlatformAPIRequest(
		t, fixture.Handler, fixture.Token, http.MethodPut, manifestPath,
		map[string]any{
			"base_revision_id": envelope.Revision.ID,
			"content": `{"schema_version":1,"entry":"index.html","shell_mode":"site",` +
				`"capabilities":[{"name":"database.raw"}]}`,
		},
		currentProject.ETag(), "app-text-edit-bad-capability",
	)
	if badCapability.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(badCapability.Body.String(), "capability_unknown") {
		t.Fatalf("manifest capability revalidation = %d %s",
			badCapability.Code, badCapability.Body.String())
	}
	remoteModule := pagePlatformAPIRequest(
		t, fixture.Handler, fixture.Token, http.MethodPut, filePath,
		map[string]any{
			"base_revision_id": envelope.Revision.ID,
			"content":          `import x from "https://example.test/x.js";`,
		},
		currentProject.ETag(), "app-text-edit-remote-module",
	)
	if remoteModule.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(remoteModule.Body.String(), "remote_module_forbidden") {
		t.Fatalf("source edit bundle revalidation = %d %s",
			remoteModule.Code, remoteModule.Body.String())
	}
	binaryEdit := pagePlatformAPIRequest(
		t, fixture.Handler, fixture.Token, http.MethodPut, base+"/app-files/assets/dot.svg",
		map[string]any{
			"base_revision_id": envelope.Revision.ID,
			"content":          `<svg/>`,
		},
		currentProject.ETag(), "app-text-edit-binary",
	)
	if binaryEdit.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(binaryEdit.Body.String(), "source_file_not_editable") {
		t.Fatalf("binary edit = %d %s", binaryEdit.Code, binaryEdit.Body.String())
	}
	platformUpload := pageAppMultipartRequest(
		t, fixture.Handler, fixture.Token,
		"/api/platform/v1/sites/77/page-projects/"+
			strconv.FormatInt(fixture.Project.ID, 10)+"/app-package",
		currentProject.ETag(), "app-platform-upload",
		pageAppZipForTest(t, pageAppFilesWithoutCapabilities()),
	)
	if platformUpload.Code != http.StatusCreated {
		t.Fatalf("platform app-package mirror = %d %s", platformUpload.Code, platformUpload.Body.String())
	}
}

func TestPageAppUploadRequiresExplicitScopeAndRejectsOversizedBridge(t *testing.T) {
	server := newTestPublicServer(t, "")
	token, prefix := newAutomationToken()
	if _, err := server.store.CreateAutomationKey(
		"legacy-no-app", token, prefix,
		apiScopeContentWrite+","+apiScopePageProjectsRead+","+apiScopePageProjectsWrite,
	); err != nil {
		t.Fatal(err)
	}
	postID, err := server.store.CreatePost(&store.Post{
		Type: "page", Lang: "zh", Slug: "app-scope", Title: "scope", Status: "draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := server.store.CreatePageProject(store.CreatePageProjectInput{
		PostID: postID, Mode: store.PageModeApp, SchemaVersion: 1,
		ShellMode: store.PageShellSite, CreatedBy: store.PageOriginAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := pageAppMultipartRequest(
		t, server.Handler(), token,
		"/api/admin/v1/page-projects/"+strconv.FormatInt(project.ID, 10)+"/app-package",
		project.ETag(), "legacy-app-upload", pageAppZipForTest(t, pageAppFilesWithoutCapabilities()),
	)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "missing_scope") {
		t.Fatalf("legacy scope inherited app upload = %d %s", response.Code, response.Body.String())
	}

	tooLarge := bytes.NewReader(bytes.Repeat([]byte("x"), int(pagePlatformServerLimits().MaxBridgeRequestBytes)+1))
	request := httptest.NewRequest(http.MethodPost, "/preview/page-app-bridge/1/1", tooLarge)
	request.Header.Set(pageAppRuntimeTokenHeader, "invalid")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		// Authorization is deliberately checked before reading an attacker body.
		t.Fatalf("invalid bridge token = %d %s", recorder.Code, recorder.Body.String())
	}
	_, _ = io.Copy(io.Discard, tooLarge)
}

func TestPageAppOpenAPIDocumentsPackageSourceAndCapabilityEndpoints(t *testing.T) {
	spec := automationOpenAPISpec("https://example.test/api/admin/v1")
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("OpenAPI paths = %#v", spec["paths"])
	}
	for _, name := range []string{
		"/page-projects/{project_id}/app-package",
		"/page-projects/{project_id}/app-files/{file_path}",
		"/page-projects/{project_id}/capabilities",
		"/page-projects/{project_id}/capabilities/request",
		"/page-projects/{project_id}/capabilities/apply",
		"/page-projects/{project_id}/capabilities/revoke",
	} {
		if _, exists := paths[name]; !exists {
			t.Fatalf("OpenAPI missing app path %s", name)
		}
	}
}
