package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func pagePlatformOperationForTest(t *testing.T, operations []pagePlatformOperation, id string) pagePlatformOperation {
	t.Helper()
	for _, operation := range operations {
		if operation.ID == id {
			return operation
		}
	}
	t.Fatalf("operation %q not found", id)
	return pagePlatformOperation{}
}

func pagePlatformCapabilitiesRequestForTest(t *testing.T, server *Server, token string) (*httptest.ResponseRecorder, pagePlatformCapabilitiesResponse) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/page-platform/capabilities", nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	server.servePagePlatformCapabilities(response, request)

	var body pagePlatformCapabilitiesResponse
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode capabilities response: %v (body=%s)", err, response.Body.String())
		}
	}
	return response, body
}

func TestPagePlatformCapabilitiesLegacyTokenDiscoversWithoutNewGrants(t *testing.T) {
	server, token := newTestAutomationServer(t, strings.Join([]string{
		apiScopeContentRead,
		apiScopeContentWrite,
		apiScopeContentPublish,
	}, ","))

	response, body := pagePlatformCapabilitiesRequestForTest(t, server, token)
	if response.Code != http.StatusOK {
		t.Fatalf("capabilities = %d %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if body.APIVersion != pagePlatformAPIVersion || body.PagePlatform.Version != pagePlatformContractVersion {
		t.Fatalf("versions = api %q contract %q", body.APIVersion, body.PagePlatform.Version)
	}
	if body.Phase != pagePlatformPhase {
		t.Fatalf("phase = %q", body.Phase)
	}

	if len(body.PagePlatform.Modes) != 3 {
		t.Fatalf("modes = %#v", body.PagePlatform.Modes)
	}
	modes := map[string]pagePlatformModeCapability{}
	for _, mode := range body.PagePlatform.Modes {
		modes[mode.ID] = mode
	}
	if !modes["standard"].Available {
		t.Fatalf("standard mode must remain available: %#v", modes["standard"])
	}
	if !modes["composition"].Available || modes["composition"].UnavailableReason != "" {
		t.Fatalf("composition mode must be available: %#v", modes["composition"])
	}
	if !modes["app"].Available || modes["app"].UnavailableReason != "" {
		t.Fatalf("app mode must be available after runtime E2E coverage: %#v", modes["app"])
	}
	for _, id := range []string{"composition", "app"} {
		if len(modes[id].ManifestVersions) != 1 || modes[id].ManifestVersions[0] != 1 {
			t.Fatalf("%s manifest versions = %v", id, modes[id].ManifestVersions)
		}
	}

	discovery := pagePlatformOperationForTest(t, body.Operations, "page_platform.capabilities")
	if !discovery.Available || !discovery.Granted || discovery.RequiredScope != "" {
		t.Fatalf("discovery operation = %#v", discovery)
	}
	for _, id := range []string{"page_projects.list", "page_projects.get", "page_assets.upload"} {
		operation := pagePlatformOperationForTest(t, body.Operations, id)
		if operation.Granted {
			t.Fatalf("legacy content wildcard unexpectedly granted %s: %#v", id, operation)
		}
		if !operation.Available {
			t.Fatalf("implemented composition operation unavailable %s: %#v", id, operation)
		}
	}
	designContext := pagePlatformOperationForTest(t, body.Operations, "page_design_context.get")
	if !designContext.Available || designContext.Granted ||
		designContext.RequiredScope != apiScopePageProjectsRead {
		t.Fatalf("legacy token design context operation = %#v", designContext)
	}
	for _, id := range []string{"page_apps.upload", "page_capabilities.grant"} {
		operation := pagePlatformOperationForTest(t, body.Operations, id)
		if operation.Granted {
			t.Fatalf("legacy content wildcard unexpectedly granted %s: %#v", id, operation)
		}
		if !operation.Available {
			t.Fatalf("tested app operation unavailable %s: %#v", id, operation)
		}
	}
	standardPreview := pagePlatformOperationForTest(t, body.Operations, "standard_pages.preview")
	if !standardPreview.Available || !standardPreview.Granted ||
		standardPreview.RequiredScope != apiScopePagesRead {
		t.Fatalf("legacy content:read should retain standard page preview access: %#v", standardPreview)
	}
	if !body.PagePlatform.Features.PrivatePreview ||
		!body.PagePlatform.Features.StandardPagePrivatePreview ||
		!body.PagePlatform.Features.ProjectRevisionPrivatePreview ||
		!body.PagePlatform.Features.PilotDesignContext ||
		!body.PagePlatform.Features.RevisionConflict ||
		!body.PagePlatform.Features.StaticExport ||
		!body.PagePlatform.Features.CapabilityBridge ||
		!body.PagePlatform.Features.PublishApprovalToken {
		t.Fatalf("private preview feature granularity = %#v", body.PagePlatform.Features)
	}
	if len(body.PagePlatform.BindingUpdateModes) != 1 ||
		body.PagePlatform.BindingUpdateModes[0] != "live" {
		t.Fatalf("binding update modes advertise unsupported semantics: %v",
			body.PagePlatform.BindingUpdateModes)
	}

	limits := body.PagePlatform.Limits
	if limits.MaxManifestBytes <= 0 || limits.MaxSections <= 0 || limits.MaxAssets <= 0 ||
		limits.MaxAppUnpackedBytes <= 0 || limits.MaxAppTextFileBytes <= 0 ||
		limits.PrivatePreviewTTLSeconds <= 0 {
		t.Fatalf("server limits must be concrete positive values: %#v", limits)
	}
	if limits.PrivatePreviewTTLSeconds != int(frontendPreviewTTL.Seconds()) {
		t.Fatalf("private preview TTL = %d, actual server TTL = %d", limits.PrivatePreviewTTLSeconds, int(frontendPreviewTTL.Seconds()))
	}
	if body.MutationProtocol.IdempotencyHeader != "Idempotency-Key" ||
		body.MutationProtocol.ConcurrencyHeader != "If-Match" ||
		!body.MutationProtocol.ETagRequiredOnWrites ||
		!body.MutationProtocol.ApprovalRevisionBound {
		t.Fatalf("mutation protocol = %#v", body.MutationProtocol)
	}
}

func TestPagePlatformScopeIsolationIsExact(t *testing.T) {
	legacy := apiScopeMap(strings.Join([]string{
		apiScopeContentRead,
		apiScopeContentWrite,
		apiScopeContentPublish,
	}, ","))

	// 这个断言记录了不能复用旧 helper 的原因：旧 helper 会把两段式的
	// page_apps:write 当成普通内容集合权限。
	if !automationScopeAllowed(legacy, apiScopePageAppsWrite) {
		t.Fatal("test precondition changed: automationScopeAllowed no longer demonstrates legacy wildcard inheritance")
	}
	for _, scope := range pagePlatformScopes() {
		if pagePlatformScopeAllowed(legacy, scope) {
			t.Fatalf("legacy content wildcard unexpectedly grants exact page scope %q", scope)
		}
	}
	for _, existingScope := range []string{apiScopePagesRead, apiScopePagesPublish} {
		if !pagePlatformScopeAllowed(legacy, existingScope) {
			t.Fatalf("existing content compatibility scope %q must retain its wildcard semantics", existingScope)
		}
	}

	explicit := apiScopeMap(strings.Join([]string{
		apiScopePageProjectsRead,
		apiScopePageAssetsWrite,
		apiScopePageAppsWrite,
		apiScopePageCapabilitiesGrant,
	}, ","))
	for _, scope := range []string{
		apiScopePageProjectsRead,
		apiScopePageAssetsWrite,
		apiScopePageAppsWrite,
		apiScopePageCapabilitiesGrant,
	} {
		if !pagePlatformScopeAllowed(explicit, scope) {
			t.Fatalf("explicit page scope %q was not granted", scope)
		}
	}
	if pagePlatformScopeAllowed(explicit, apiScopePageCapabilitiesRequest) {
		t.Fatal("grant permission must not imply request permission")
	}

	body := pagePlatformCapabilities(explicit)
	for _, id := range []string{"page_projects.get", "page_assets.upload", "page_apps.upload", "page_capabilities.grant"} {
		operation := pagePlatformOperationForTest(t, body.Operations, id)
		if !operation.Granted {
			t.Fatalf("explicit scope did not grant %s: %#v", id, operation)
		}
		if !operation.Available {
			t.Fatalf("implemented operation unavailable %s: %#v", id, operation)
		}
	}
	if pagePlatformOperationForTest(t, body.Operations, "page_capabilities.request").Granted {
		t.Fatal("page_capabilities:grant must not imply page_capabilities:request")
	}
}

func TestPagePlatformCapabilitiesRequiresOnlyValidAutomationToken(t *testing.T) {
	server, token := newTestAutomationServer(t, apiScopeContentRead)

	missing, _ := pagePlatformCapabilitiesRequestForTest(t, server, "")
	if missing.Code != http.StatusUnauthorized || !strings.Contains(missing.Body.String(), "missing_token") {
		t.Fatalf("missing token = %d %s", missing.Code, missing.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/api/admin/v1/page-platform/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.servePagePlatformCapabilities(response, request)
	if response.Code != http.StatusMethodNotAllowed || !strings.Contains(response.Body.String(), "method_not_allowed") {
		t.Fatalf("POST = %d %s", response.Code, response.Body.String())
	}
}

func TestPagePlatformCapabilitiesAcceptsPlatformIdentity(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/page-platform/capabilities", nil)
	request = request.WithContext(withPlatformIdentity(request.Context(), &platformIdentity{
		keyID:  42,
		scopes: apiScopeMap(apiScopeContentRead),
	}))
	response := httptest.NewRecorder()
	server.servePagePlatformCapabilities(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("platform identity capabilities = %d %s", response.Code, response.Body.String())
	}

	var body pagePlatformCapabilitiesResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode platform identity response: %v", err)
	}
	if !pagePlatformOperationForTest(t, body.Operations, "page_platform.capabilities").Granted {
		t.Fatal("valid platform identity must be able to inspect capability discovery")
	}
	if pagePlatformOperationForTest(t, body.Operations, "page_projects.get").Granted {
		t.Fatal("platform content:read must not grant page_projects:read")
	}
}

func TestPagePlatformCapabilitiesAdminAndPlatformRoutesAreRegistered(t *testing.T) {
	server := newTestPublicServer(t, "")
	token, prefix := newAutomationToken()
	if _, err := server.store.CreateAutomationKey("page-platform-route-test", token, prefix, apiScopeContentRead); err != nil {
		t.Fatalf("create automation key: %v", err)
	}

	for _, path := range []string{
		"/api/admin/v1/page-platform/capabilities",
		"/api/platform/v1/sites/1/page-platform/capabilities",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, response.Code, response.Body.String())
		}
		var body pagePlatformCapabilitiesResponse
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode GET %s: %v", path, err)
		}
		if !pagePlatformOperationForTest(t, body.Operations, "page_platform.capabilities").Available {
			t.Fatalf("GET %s did not reach page platform capability handler", path)
		}
	}
}

func TestPagePlatformOperationCatalogContract(t *testing.T) {
	operations := pagePlatformOperationCatalog()
	if len(operations) == 0 {
		t.Fatal("empty page platform operation catalog")
	}
	seen := map[string]bool{}
	allowedRisk := map[string]bool{
		pagePlatformRiskRead:        true,
		pagePlatformRiskWrite:       true,
		pagePlatformRiskSensitive:   true,
		pagePlatformRiskDestructive: true,
	}
	allowedConfirmation := map[string]bool{
		pagePlatformConfirmationNone:              true,
		pagePlatformConfirmationExplicit:          true,
		pagePlatformConfirmationApprovalToken:     true,
		pagePlatformConfirmationImpactAndExplicit: true,
	}
	allowedConcurrency := map[string]bool{
		pagePlatformConcurrencyNone:                    true,
		pagePlatformConcurrencyIfMatch:                 true,
		pagePlatformConcurrencyContentRevisionBound:    true,
		pagePlatformConcurrencyBaseRevisionAndIfMatch:  true,
		pagePlatformConcurrencyApprovalRevisionAndETag: true,
	}
	allowedScopes := map[string]bool{
		apiScopePagesRead:    true,
		apiScopePagesPublish: true,
	}
	for _, scope := range pagePlatformScopes() {
		if allowedScopes[scope] {
			t.Fatalf("duplicate page platform scope %q", scope)
		}
		allowedScopes[scope] = true
	}

	available := 0
	for _, operation := range operations {
		if operation.ID == "" || seen[operation.ID] {
			t.Fatalf("empty or duplicate operation id %q", operation.ID)
		}
		seen[operation.ID] = true
		if operation.Method == "" || operation.Endpoint == "" {
			t.Fatalf("operation lacks method/endpoint: %#v", operation)
		}
		if operation.RequiredScope != "" && !allowedScopes[operation.RequiredScope] {
			t.Fatalf("operation uses an undeclared scope: %#v", operation)
		}
		if !allowedRisk[operation.Risk] || !allowedConfirmation[operation.Confirmation] ||
			!allowedConcurrency[operation.Concurrency] {
			t.Fatalf("operation has unsupported policy value: %#v", operation)
		}
		if operation.Risk == pagePlatformRiskSensitive || operation.Risk == pagePlatformRiskDestructive {
			if !operation.RequiresConfirmation {
				t.Fatalf("high-risk operation lacks confirmation: %#v", operation)
			}
		}
		if operation.Risk == pagePlatformRiskDestructive {
			if !operation.SupportsDryRun || !operation.RequiresUnlock || !operation.RequiresIdempotencyKey {
				t.Fatalf("destructive operation lacks impact/idempotency/unlock guard: %#v", operation)
			}
		}
		if operation.Risk == pagePlatformRiskWrite && !operation.RequiresIdempotencyKey {
			t.Fatalf("write operation lacks idempotency: %#v", operation)
		}
		if operation.Available {
			available++
			if operation.UnavailableReason != "" {
				t.Fatalf("available operation has unavailable reason: %#v", operation)
			}
		} else if operation.UnavailableReason == "" {
			t.Fatalf("unavailable operation lacks reason: %#v", operation)
		}
	}
	if available < 20 ||
		!pagePlatformOperationForTest(t, operations, "page_platform.capabilities").Available ||
		!pagePlatformOperationForTest(t, operations, "standard_pages.preview").Available ||
		!pagePlatformOperationForTest(t, operations, "pages.preview").Available ||
		!pagePlatformOperationForTest(t, operations, "page_builds.create").Available {
		t.Fatalf("composition operation catalog is incomplete; available count = %d", available)
	}

	standardPreview := pagePlatformOperationForTest(t, operations, "standard_pages.preview")
	if standardPreview.RequiredScope != apiScopePagesRead ||
		standardPreview.Endpoint != "/pages/{page_id}/preview-url" ||
		standardPreview.Concurrency != pagePlatformConcurrencyContentRevisionBound {
		t.Fatalf("standard_pages.preview contract = %#v", standardPreview)
	}
	preview := pagePlatformOperationForTest(t, operations, "pages.preview")
	if !preview.Available || preview.RequiredScope != apiScopePagePreviewRead ||
		preview.Concurrency != pagePlatformConcurrencyIfMatch {
		t.Fatalf("reserved pages.preview contract = %#v", preview)
	}
	publish := pagePlatformOperationForTest(t, operations, "pages.publish")
	if publish.Confirmation != pagePlatformConfirmationApprovalToken ||
		publish.Concurrency != pagePlatformConcurrencyApprovalRevisionAndETag ||
		!publish.RequiresIdempotencyKey {
		t.Fatalf("publish contract = %#v", publish)
	}
}
