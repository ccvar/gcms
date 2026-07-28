package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func setSingleTestPassword(t *testing.T, s *Server, password string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.setAdminPasswordHash("admin", string(hash)); err != nil {
		t.Fatal(err)
	}
}

func TestNativePageApprovalBindingExpiryAndReplay(t *testing.T) {
	s, token := newTestAutomationServer(t, strings.Join([]string{
		apiScopePageProjectsRead, apiScopePageProjectsWrite,
		apiScopePagesPublish, apiScopeControlUnlock,
	}, ","))
	setSingleTestPassword(t, s, controlTestPassword)
	mux := pagePlatformTestMux(s)
	_, _, project, revision := createPageProjectForAPITest(t, s, mux, token)
	auth := &automationAuth{key: &store.AutomationKey{ID: 41}, scopes: map[string]bool{
		apiScopePagesPublish: true, apiScopeControlUnlock: true,
	}}
	target := pageApprovalConsumeInput{
		SiteID: 0, PageID: project.PostID, ProjectID: project.ID,
		RevisionID: revision.ID, Operation: pageApprovalPublish,
		ETag: project.ETag(), RequestID: "native-publish-1",
	}
	challenge, err := issueNativePageChallenge(s, auth, target)
	if err != nil {
		t.Fatal(err)
	}
	_, passwordHash := s.adminCredentials()
	unlock, _, err := issueNativePageUnlock(
		s, pageNativeActor(auth), challenge, pageApprovalPublish,
		controlCredentialRevision(passwordHash),
	)
	if err != nil {
		t.Fatal(err)
	}
	approval, state := resolveNativePageApproval(s, auth, unlock, target)
	if state != "" || approval == "" {
		t.Fatalf("resolve exact target state=%q approval=%q", state, approval)
	}
	for name, mutate := range map[string]func(*pageApprovalConsumeInput){
		"site":     func(in *pageApprovalConsumeInput) { in.SiteID++ },
		"page":     func(in *pageApprovalConsumeInput) { in.PageID++ },
		"project":  func(in *pageApprovalConsumeInput) { in.ProjectID++ },
		"revision": func(in *pageApprovalConsumeInput) { in.RevisionID++ },
		"build":    func(in *pageApprovalConsumeInput) { in.BuildID++ },
		"etag":     func(in *pageApprovalConsumeInput) { in.ETag = `"other"` },
		"snapshot": func(in *pageApprovalConsumeInput) { in.DataSnapshotHash = "other-snapshot" },
		"operation": func(in *pageApprovalConsumeInput) {
			in.Operation = pageApprovalRollback
		},
		"request": func(in *pageApprovalConsumeInput) { in.RequestID = "native-publish-2" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := target
			mutate(&changed)
			if _, got := resolveNativePageApproval(s, auth, unlock, changed); got != "mismatch" {
				t.Fatalf("state=%q, want mismatch", got)
			}
		})
	}
	grant, state := consumePageApprovalToken(s, approval, target)
	if state != "" || grant == nil {
		t.Fatalf("consume state=%q grant=%#v", state, grant)
	}
	if _, state := consumePageApprovalToken(s, approval, target); state != "" {
		t.Fatalf("same request replay state=%q", state)
	}
	otherRequest := target
	otherRequest.RequestID = "native-publish-other"
	if _, state := consumePageApprovalToken(s, approval, otherRequest); state != "used" {
		t.Fatalf("cross-request replay state=%q, want used", state)
	}

	registry := pageNativeRegistryFor(s)
	digest := sha256.Sum256([]byte(unlock))
	registry.mu.Lock()
	registry.unlocks[digest].expiresAt = time.Now().Add(-time.Second)
	registry.mu.Unlock()
	if _, state := resolveNativePageApproval(s, auth, unlock, target); state != "invalid" {
		t.Fatalf("expired unlock state=%q, want invalid", state)
	}

	passwordTarget := target
	passwordTarget.RequestID = "native-password-change"
	passwordChallenge, err := issueNativePageChallenge(s, auth, passwordTarget)
	if err != nil {
		t.Fatal(err)
	}
	passwordUnlock, _, err := issueNativePageUnlock(
		s, pageNativeActor(auth), passwordChallenge, pageApprovalPublish,
		controlCredentialRevision(passwordHash),
	)
	if err != nil {
		t.Fatal(err)
	}
	setSingleTestPassword(t, s, controlTestPassword+"-changed")
	if _, state := resolveNativePageApproval(s, auth, passwordUnlock, passwordTarget); state != "credential_changed" {
		t.Fatalf("password-changed unlock state=%q, want credential_changed", state)
	}
}

func TestSingleSiteNativeUnlockKeepsPasswordAndApprovalOutOfModel(t *testing.T) {
	s, token := newTestAutomationServer(t, strings.Join([]string{
		apiScopePageProjectsRead, apiScopePageProjectsWrite,
		apiScopePagesPublish, apiScopeControlUnlock,
	}, ","))
	setSingleTestPassword(t, s, controlTestPassword)
	mux := pagePlatformTestMux(s)
	_, _, project, revision := createPageProjectForAPITest(t, s, mux, token)
	publishPath := "/api/admin/v1/page-projects/" + strconv.FormatInt(project.ID, 10) + "/publish"
	body := map[string]any{"revision_id": revision.ID}
	blocked := pagePlatformAPIRequest(
		t, mux, token, http.MethodPost, publishPath, body,
		project.ETag(), "native-endpoint-publish",
	)
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), `"unlock_required":true`) {
		t.Fatalf("blocked publish = %d %s", blocked.Code, blocked.Body.String())
	}
	var challengeResponse struct {
		Challenge string `json:"unlock_challenge"`
	}
	if err := json.Unmarshal(blocked.Body.Bytes(), &challengeResponse); err != nil || challengeResponse.Challenge == "" {
		t.Fatalf("challenge response: %v %s", err, blocked.Body.String())
	}

	unlockMux := http.NewServeMux()
	unlockMux.HandleFunc("POST /api/admin/v1/control/unlock", s.serveSingleControlUnlock)
	unlockBody, _ := json.Marshal(map[string]any{
		"password": controlTestPassword, "operations": []string{pageApprovalPublish},
		"page_challenge": challengeResponse.Challenge,
	})
	unlockRequest := httptest.NewRequest(
		http.MethodPost, "https://site.test/api/admin/v1/control/unlock",
		bytes.NewReader(unlockBody),
	)
	unlockRequest.Header.Set("Authorization", "Bearer "+token)
	unlockRequest.Header.Set("Content-Type", "application/json")
	unlockRequest.Header.Set(controlUIRequestHeader, controlUIPilotValue)
	unlockResponse := httptest.NewRecorder()
	unlockMux.ServeHTTP(unlockResponse, unlockRequest)
	if unlockResponse.Code != http.StatusCreated {
		t.Fatalf("unlock = %d %s", unlockResponse.Code, unlockResponse.Body.String())
	}
	if strings.Contains(unlockResponse.Body.String(), controlTestPassword) ||
		strings.Contains(unlockResponse.Body.String(), "approval_") {
		t.Fatalf("native response leaked password or approval ID: %s", unlockResponse.Body.String())
	}
	var unlocked struct {
		Token string `json:"unlock_token"`
	}
	if err := json.Unmarshal(unlockResponse.Body.Bytes(), &unlocked); err != nil || !strings.HasPrefix(unlocked.Token, "gcmsup_") {
		t.Fatalf("decode unlock: %v %s", err, unlockResponse.Body.String())
	}

	raw, _ := json.Marshal(body)
	publishRequest := httptest.NewRequest(http.MethodPost, publishPath, bytes.NewReader(raw))
	publishRequest.Header.Set("Authorization", "Bearer "+token)
	publishRequest.Header.Set("Content-Type", "application/json")
	publishRequest.Header.Set(pagePlatformConcurrencyHeader, project.ETag())
	publishRequest.Header.Set(pagePlatformIdempotencyHeader, "native-endpoint-publish")
	publishRequest.Header.Set(controlUnlockHeader, unlocked.Token)
	published := httptest.NewRecorder()
	mux.ServeHTTP(published, publishRequest)
	if published.Code != http.StatusCreated {
		t.Fatalf("publish = %d %s", published.Code, published.Body.String())
	}
	if strings.Contains(published.Body.String(), unlocked.Token) ||
		strings.Contains(published.Body.String(), controlTestPassword) {
		t.Fatalf("publish response leaked secret: %s", published.Body.String())
	}

	replayed := httptest.NewRecorder()
	retry := publishRequest.Clone(publishRequest.Context())
	retry.Body = io.NopCloser(bytes.NewReader(raw))
	mux.ServeHTTP(replayed, retry)
	if replayed.Code != http.StatusOK || replayed.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay = %d %s", replayed.Code, replayed.Body.String())
	}
}

func TestPlatformNativePageUnlockUsesSiteBoundChallenge(t *testing.T) {
	root, handler, platformStore, _, blogSite := setupPlatformAutomation(t)
	setPlatformTestPassword(t, platformStore, controlTestPassword)
	platformToken := "gcmsp_page_native_unlock_2026"
	if _, err := platformStore.CreatePlatformKey(
		"pilot-pages", platformToken, platformToken[:13], platform.KeyMembershipAll,
		strings.Join([]string{
			apiScopeControlRead, apiScopeControlUnlock,
			apiScopePageProjectsRead, apiScopePagesPublish,
		}, ","),
		nil, time.Time{},
	); err != nil {
		t.Fatal(err)
	}
	runtime, ok := root.runtimePool().runtimeByID(blogSite.ID)
	if !ok || runtime.Store == nil {
		t.Fatal("blog runtime unavailable")
	}
	pageID, err := runtime.Store.CreatePost(&store.Post{
		Type: "page", Lang: "zh", Slug: "native-platform-page",
		Title: "平台原生确认页面", Content: "baseline", Status: "draft",
		TransGroup: "zh:native-platform-page",
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := runtime.Store.CreatePageProject(store.CreatePageProjectInput{
		PostID: pageID, Mode: store.PageModeComposition, SchemaVersion: 1,
		ShellMode: store.PageShellSite, CreatedBy: store.PageOriginAPI,
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := runtime.Store.CreatePageProjectRevision(store.CreatePageRevisionInput{
		ProjectID: project.ID, RevisionKind: store.PageRevisionStandardBaseline,
		PageMetaJSON: `{"lang":"zh","slug":"native-platform-page","title":"平台原生确认页面"}`,
		ManifestJSON: "{}", StandardContent: "baseline",
		Origin: store.PageOriginAPI, ActorID: "test", RequestID: "platform-baseline",
		Summary: "baseline", ValidationJSON: `{"valid":true}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	project, _ = runtime.Store.GetPageProject(project.ID)
	prefix := "/api/platform/v1/sites/" + strconv.FormatInt(blogSite.ID, 10)
	publishPath := prefix + "/page-projects/" + strconv.FormatInt(project.ID, 10) + "/publish"
	publishRaw, _ := json.Marshal(map[string]any{"revision_id": revision.ID})
	makePublish := func(unlock string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "https://platform.test"+publishPath, bytes.NewReader(publishRaw))
		request.Header.Set("Authorization", "Bearer "+platformToken)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(pagePlatformConcurrencyHeader, project.ETag())
		request.Header.Set(pagePlatformIdempotencyHeader, "platform-native-publish")
		if unlock != "" {
			request.Header.Set(controlUnlockHeader, unlock)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	blocked := makePublish("")
	if blocked.Code != http.StatusConflict {
		t.Fatalf("blocked = %d %s", blocked.Code, blocked.Body.String())
	}
	var blockedBody struct {
		Challenge string `json:"unlock_challenge"`
		SiteID    int64  `json:"site_id"`
	}
	if err := json.Unmarshal(blocked.Body.Bytes(), &blockedBody); err != nil ||
		blockedBody.Challenge == "" || blockedBody.SiteID != blogSite.ID {
		t.Fatalf("bad platform challenge: %v %s", err, blocked.Body.String())
	}
	unlockRaw, _ := json.Marshal(map[string]any{
		"password":       controlTestPassword,
		"operations":     []string{pageApprovalPublish},
		"page_challenge": blockedBody.Challenge,
	})
	unlockResponse := platformControlUIReq(
		t, handler, http.MethodPost, "/api/platform/v1/control/unlock",
		platformToken, unlockRaw,
	)
	if unlockResponse.Code != http.StatusCreated {
		t.Fatalf("platform unlock = %d %s", unlockResponse.Code, unlockResponse.Body.String())
	}
	var unlocked struct {
		Token string `json:"unlock_token"`
	}
	if err := json.Unmarshal(unlockResponse.Body.Bytes(), &unlocked); err != nil ||
		!strings.HasPrefix(unlocked.Token, "gcmsup_") {
		t.Fatalf("decode platform unlock: %v %s", err, unlockResponse.Body.String())
	}
	published := makePublish(unlocked.Token)
	if published.Code != http.StatusCreated {
		t.Fatalf("platform publish = %d %s", published.Code, published.Body.String())
	}
}

func TestPlatformUnboundSitePagePublishDoesNotRequirePasswordUnlock(t *testing.T) {
	root, handler, platformStore, _, blogSite := setupPlatformAutomation(t)
	if err := platformStore.ReplaceSiteDomains(blogSite.ID, nil); err != nil {
		t.Fatalf("clear blog domains: %v", err)
	}
	platformToken := "gcmsp_page_unbound_publish_2026"
	if _, err := platformStore.CreatePlatformKey(
		"pilot-unbound-pages", platformToken, platformToken[:13], platform.KeyMembershipAll,
		strings.Join([]string{apiScopePageProjectsRead, apiScopePagesPublish}, ","),
		nil, time.Time{},
	); err != nil {
		t.Fatal(err)
	}
	runtime, ok := root.runtimePool().runtimeByID(blogSite.ID)
	if !ok || runtime.Store == nil {
		t.Fatal("blog runtime unavailable")
	}
	pageID, err := runtime.Store.CreatePost(&store.Post{
		Type: "page", Lang: "zh", Slug: "unbound-publish-page",
		Title: "未绑定域名页面", Content: "baseline", Status: "draft",
		TransGroup: "zh:unbound-publish-page",
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err := runtime.Store.CreatePageProject(store.CreatePageProjectInput{
		PostID: pageID, Mode: store.PageModeComposition, SchemaVersion: 1,
		ShellMode: store.PageShellSite, CreatedBy: store.PageOriginAPI,
	})
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := runtime.Store.CreatePageProjectRevision(store.CreatePageRevisionInput{
		ProjectID: project.ID, RevisionKind: store.PageRevisionStandardBaseline,
		PageMetaJSON: `{"lang":"zh","slug":"unbound-publish-page","title":"未绑定域名页面"}`,
		ManifestJSON: "{}", StandardContent: "baseline",
		Origin: store.PageOriginAPI, ActorID: "test", RequestID: "unbound-baseline",
		Summary: "baseline", ValidationJSON: `{"valid":true}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	project, err = runtime.Store.GetPageProject(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/api/platform/v1/sites/" + strconv.FormatInt(blogSite.ID, 10)
	base := prefix + "/page-projects/" + strconv.FormatInt(project.ID, 10)
	body := map[string]any{"revision_id": revision.ID}
	capabilityRequest := httptest.NewRequest(
		http.MethodGet, "https://platform.test"+prefix+"/page-platform/capabilities", nil,
	)
	capabilityRequest.Header.Set("Authorization", "Bearer "+platformToken)
	capabilityResponse := httptest.NewRecorder()
	handler.ServeHTTP(capabilityResponse, capabilityRequest)
	var capabilities pagePlatformCapabilitiesResponse
	if capabilityResponse.Code != http.StatusOK {
		t.Fatalf("unbound capabilities = %d %s", capabilityResponse.Code, capabilityResponse.Body.String())
	}
	if err := json.Unmarshal(capabilityResponse.Body.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	publishCapability := pagePlatformOperationForTest(t, capabilities.Operations, pageApprovalPublish)
	if publishCapability.RequiresUnlock ||
		publishCapability.Confirmation != pagePlatformConfirmationExplicit {
		t.Fatalf("unbound publish capability = %#v", publishCapability)
	}
	request := func(path, requestID string) *httptest.ResponseRecorder {
		raw, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		req := httptest.NewRequest(http.MethodPost, "https://platform.test"+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+platformToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(pagePlatformConcurrencyHeader, project.ETag())
		if requestID != "" {
			req.Header.Set(pagePlatformIdempotencyHeader, requestID)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	plan := request(base+"/publish-plan", "")
	if plan.Code != http.StatusOK ||
		!strings.Contains(plan.Body.String(), `"requires_approval_token":false`) {
		t.Fatalf("unbound publish plan = %d %s", plan.Code, plan.Body.String())
	}
	published := request(base+"/publish", "unbound-page-publish")
	if published.Code != http.StatusCreated ||
		strings.Contains(published.Body.String(), `"unlock_required":true`) {
		t.Fatalf("unbound publish = %d %s", published.Code, published.Body.String())
	}
	publication, err := runtime.Store.GetPageProject(project.ID)
	if err != nil || publication == nil || publication.PublishedRevisionID != revision.ID {
		t.Fatalf("published project = %#v err=%v", publication, err)
	}
}
