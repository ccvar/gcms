package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
	"golang.org/x/crypto/bcrypt"
)

const controlTestPassword = "Control-Test-Password-2026!"

func platformControlUIReq(t *testing.T, h http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, "https://platform.test"+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, "https://platform.test"+path, nil)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(controlUIRequestHeader, controlUIPilotValue)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func setPlatformTestPassword(t *testing.T, ps *platform.Store, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := ps.SetAdminPasswordHash("admin", string(hash)); err != nil {
		t.Fatalf("set admin password: %v", err)
	}
	return string(hash)
}

func controlOperationFromBody(t *testing.T, body []byte, id string) platformControlOperation {
	t.Helper()
	var payload struct {
		Operations []platformControlOperation `json:"operations"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode capabilities: %v (body=%s)", err, body)
	}
	for _, op := range payload.Operations {
		if op.ID == id {
			return op
		}
	}
	t.Fatalf("operation %q not found in %s", id, body)
	return platformControlOperation{}
}

func TestPlatformControlCapabilitiesAreAdditive(t *testing.T) {
	_, h, ps, _, _ := setupPlatformAutomation(t)

	// 旧平台密钥不自动获得控制层，但原有站点发现继续可用。
	legacyToken := "gcmsp_legacycontrol123"
	if _, err := ps.CreatePlatformKey("legacy", legacyToken, legacyToken[:13], platform.KeyMembershipAll,
		"posts:read", nil, time.Time{}); err != nil {
		t.Fatalf("create legacy key: %v", err)
	}
	denied := platformAPIReq(t, h, http.MethodGet, "/api/platform/v1/control/capabilities", legacyToken, nil)
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "missing_scope") {
		t.Fatalf("legacy capabilities = %d %s, want 403 missing_scope", denied.Code, denied.Body.String())
	}
	discovery := platformAPIReq(t, h, http.MethodGet, "/api/platform/v1/sites", legacyToken, nil)
	if discovery.Code != http.StatusOK || !strings.Contains(discovery.Body.String(), `"items"`) {
		t.Fatalf("legacy discovery changed = %d %s", discovery.Code, discovery.Body.String())
	}

	controlToken := "gcmsp_controlcaps12345"
	if _, err := ps.CreatePlatformKey("pilot", controlToken, controlToken[:13], platform.KeyMembershipAll,
		strings.Join([]string{apiScopeControlRead, apiScopeControlUnlock, apiScopeSitesCreate, apiScopeSitesDelete, apiScopeCategoriesDelete, apiScopeNavigationDelete, apiScopeThemesApply, apiScopeDomainsWrite}, ","), nil, time.Time{}); err != nil {
		t.Fatalf("create control key: %v", err)
	}
	rec := platformAPIReq(t, h, http.MethodGet, "/api/platform/v1/control/capabilities", controlToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("capabilities = %d %s", rec.Code, rec.Body.String())
	}
	create := controlOperationFromBody(t, rec.Body.Bytes(), "sites.create")
	if !create.Granted || !create.Available || create.RequiresUnlock || !create.RequiresConfirmation || !create.SupportsDryRun {
		t.Fatalf("sites.create contract = %#v", create)
	}
	remove := controlOperationFromBody(t, rec.Body.Bytes(), "sites.delete")
	if !remove.Granted || !remove.RequiresUnlock || remove.Risk != "destructive" {
		t.Fatalf("sites.delete contract = %#v", remove)
	}
	for _, operation := range []string{"categories.delete", "navigation.delete"} {
		remove := controlOperationFromBody(t, rec.Body.Bytes(), operation)
		if !remove.Granted || !remove.Available || !remove.RequiresUnlock || !remove.RequiresConfirmation || !remove.SupportsDryRun || remove.Risk != "destructive" {
			t.Fatalf("%s contract = %#v", operation, remove)
		}
	}
	update := controlOperationFromBody(t, rec.Body.Bytes(), "sites.update")
	if update.Granted {
		t.Fatalf("sites.update must reflect missing key scope: %#v", update)
	}
	themeApply := controlOperationFromBody(t, rec.Body.Bytes(), "themes.apply")
	if !themeApply.Granted || themeApply.RequiresUnlock || !themeApply.RequiresConfirmation || !themeApply.SupportsDryRun {
		t.Fatalf("themes.apply contract = %#v", themeApply)
	}
	themeApplyLive := controlOperationFromBody(t, rec.Body.Bytes(), "themes.apply_live")
	if !themeApplyLive.Granted || !themeApplyLive.Available || !themeApplyLive.RequiresUnlock || themeApplyLive.Risk != "sensitive" {
		t.Fatalf("themes.apply_live contract = %#v", themeApplyLive)
	}
	clearUnverified := controlOperationFromBody(t, rec.Body.Bytes(), "public_access.clear_unverified")
	if !clearUnverified.Granted || !clearUnverified.Available || !clearUnverified.RequiresUnlock ||
		!clearUnverified.RequiresConfirmation || !clearUnverified.SupportsDryRun || clearUnverified.Risk != "destructive" {
		t.Fatalf("public_access.clear_unverified contract = %#v", clearUnverified)
	}
	if strings.Contains(rec.Body.String(), `"id":"security.initial-password"`) || strings.Contains(rec.Body.String(), `"required_scope":"security:write"`) {
		t.Fatalf("initial-password write must not be advertised to automation keys: %s", rec.Body.String())
	}

	openAPI := platformAPIReq(t, h, http.MethodGet, "/api/platform/v1/control/openapi.json", controlToken, nil)
	if openAPI.Code != http.StatusOK || !strings.Contains(openAPI.Body.String(), `"/control/sites/{siteId}"`) || !strings.Contains(openAPI.Body.String(), controlIdempotencyHeader) {
		t.Fatalf("control openapi = %d %s", openAPI.Code, openAPI.Body.String())
	}
	if strings.Contains(openAPI.Body.String(), "security.initial-password") || strings.Contains(openAPI.Body.String(), "InitialPasswordInput") {
		t.Fatalf("control openapi exposes initial-password write: %s", openAPI.Body.String())
	}
	if !strings.Contains(openAPI.Body.String(), controlUnlockHeader) ||
		!strings.Contains(openAPI.Body.String(), "themes.apply_live") ||
		!strings.Contains(openAPI.Body.String(), "public_access.clear_unverified") ||
		!strings.Contains(openAPI.Body.String(), "PublicAccessClearInput") {
		t.Fatalf("control openapi does not describe protected live operations: %s", openAPI.Body.String())
	}
}

func TestPlatformControlUnlockLifecycle(t *testing.T) {
	srv, h, ps, _, _ := setupPlatformAutomation(t)
	hash := setPlatformTestPassword(t, ps, controlTestPassword)
	token := "gcmsp_unlocklifecycle1"
	keyID, err := ps.CreatePlatformKey("pilot", token, token[:13], platform.KeyMembershipAll,
		strings.Join([]string{apiScopeControlRead, apiScopeControlUnlock, apiScopeSitesDelete}, ","), nil, time.Time{})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	body := []byte(`{"password":"` + controlTestPassword + `","operations":["sites.delete"]}`)

	// 公网明文 HTTP 即使携带正确凭据也不允许提交密码。
	insecureReq := httptest.NewRequest(http.MethodPost, "http://platform.test/api/platform/v1/control/unlock", bytes.NewReader(body))
	insecureReq.RemoteAddr = "203.0.113.7:3456"
	insecureReq.Header.Set("Authorization", "Bearer "+token)
	insecureReq.Header.Set("Content-Type", "application/json")
	insecureReq.Header.Set(controlUIRequestHeader, controlUIPilotValue)
	insecureReq.Header.Set("X-Forwarded-Proto", "https") // 公网客户端伪造也不应放行。
	insecureRec := httptest.NewRecorder()
	h.ServeHTTP(insecureRec, insecureReq)
	if insecureRec.Code != http.StatusUpgradeRequired {
		t.Fatalf("insecure unlock = %d %s", insecureRec.Code, insecureRec.Body.String())
	}

	wrongBody := []byte(`{"password":"do-not-log-this-password","operations":["sites.delete"]}`)
	wrong := platformControlUIReq(t, h, http.MethodPost, "/api/platform/v1/control/unlock", token, wrongBody)
	if wrong.Code != http.StatusUnauthorized || !strings.Contains(wrong.Body.String(), "invalid_admin_password") {
		t.Fatalf("wrong password = %d %s", wrong.Code, wrong.Body.String())
	}
	logs, err := ps.ListPlatformAutomationLogs(20)
	if err != nil {
		t.Fatalf("list audit logs: %v", err)
	}
	for _, log := range logs {
		if strings.Contains(log.Message, "do-not-log-this-password") || strings.Contains(log.Message, controlTestPassword) {
			t.Fatalf("audit leaked password: %#v", log)
		}
	}

	rec := platformControlUIReq(t, h, http.MethodPost, "/api/platform/v1/control/unlock", token, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("unlock = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		UnlockToken string   `json:"unlock_token"`
		ExpiresAt   string   `json:"expires_at"`
		Operations  []string `json:"operations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode unlock: %v", err)
	}
	if !strings.HasPrefix(out.UnlockToken, "gcmsu_") || len(out.Operations) != 1 || out.Operations[0] != "sites.delete" {
		t.Fatalf("unlock response = %#v", out)
	}
	if !srv.controlGrants.valid(keyID, out.UnlockToken, "sites.delete", controlCredentialRevision(hash), time.Now()) {
		t.Fatal("issued unlock token is not bound to key, operation and credential revision")
	}
	if srv.controlGrants.valid(keyID+1, out.UnlockToken, "sites.delete", controlCredentialRevision(hash), time.Now()) {
		t.Fatal("unlock token must not be reusable by another platform key")
	}

	revokeReq := httptest.NewRequest(http.MethodDelete, "https://platform.test/api/platform/v1/control/unlock", nil)
	revokeReq.Header.Set("Authorization", "Bearer "+token)
	revokeReq.Header.Set(controlUnlockHeader, out.UnlockToken)
	revokeRec := httptest.NewRecorder()
	h.ServeHTTP(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusOK || !strings.Contains(revokeRec.Body.String(), `"revoked":true`) {
		t.Fatalf("revoke = %d %s", revokeRec.Code, revokeRec.Body.String())
	}
	if srv.controlGrants.valid(keyID, out.UnlockToken, "sites.delete", controlCredentialRevision(hash), time.Now()) {
		t.Fatal("revoked unlock token remained valid")
	}

	// 密码变更后，之前授权即使还在 TTL 内也必须失效。
	rec = platformControlUIReq(t, h, http.MethodPost, "/api/platform/v1/control/unlock", token, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("second unlock = %d %s", rec.Code, rec.Body.String())
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	newHash := setPlatformTestPassword(t, ps, controlTestPassword+"-changed")
	if srv.controlGrants.valid(keyID, out.UnlockToken, "sites.delete", controlCredentialRevision(newHash), time.Now()) {
		t.Fatal("password change did not invalidate existing unlock")
	}
}

func TestPlatformControlUnlockGuards(t *testing.T) {
	srv, h, ps, _, _ := setupPlatformAutomation(t)
	token := "gcmsp_unlockguards123"
	if _, err := ps.CreatePlatformKey("pilot", token, token[:13], platform.KeyMembershipAll,
		strings.Join([]string{apiScopeControlRead, apiScopeControlUnlock, apiScopeSitesDelete}, ","), nil, time.Time{}); err != nil {
		t.Fatalf("create key: %v", err)
	}
	body := []byte(`{"password":"` + controlTestPassword + `","operations":["sites.delete"]}`)
	defaultPassword := platformControlUIReq(t, h, http.MethodPost, "/api/platform/v1/control/unlock", token, body)
	if defaultPassword.Code != http.StatusPreconditionRequired || !strings.Contains(defaultPassword.Body.String(), "default_password") {
		t.Fatalf("default password guard = %d %s", defaultPassword.Code, defaultPassword.Body.String())
	}

	setPlatformTestPassword(t, ps, controlTestPassword)
	missingUI := platformAPIReq(t, h, http.MethodPost, "/api/platform/v1/control/unlock", token, body)
	if missingUI.Code != http.StatusForbidden || !strings.Contains(missingUI.Body.String(), "pilot_ui_required") {
		t.Fatalf("missing Pilot UI boundary = %d %s", missingUI.Code, missingUI.Body.String())
	}
	missingScopeBody := []byte(`{"password":"` + controlTestPassword + `","operations":["domains.apply"]}`)
	missingScope := platformControlUIReq(t, h, http.MethodPost, "/api/platform/v1/control/unlock", token, missingScopeBody)
	if missingScope.Code != http.StatusForbidden || !strings.Contains(missingScope.Body.String(), "missing_operation_scope") {
		t.Fatalf("missing operation scope = %d %s", missingScope.Code, missingScope.Body.String())
	}

	// 与后台登录共用同等强度的防穷举机制。
	srv.login.max = 1
	wrong := platformControlUIReq(t, h, http.MethodPost, "/api/platform/v1/control/unlock", token,
		[]byte(`{"password":"wrong","operations":["sites.delete"]}`))
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("first wrong password = %d %s", wrong.Code, wrong.Body.String())
	}
	locked := platformControlUIReq(t, h, http.MethodPost, "/api/platform/v1/control/unlock", token, body)
	if locked.Code != http.StatusTooManyRequests || locked.Header().Get("Retry-After") == "" {
		t.Fatalf("locked unlock = %d headers=%v body=%s", locked.Code, locked.Header(), locked.Body.String())
	}
}

func TestPlatformControlScopeIssuanceAndDefaults(t *testing.T) {
	req := &http.Request{Method: http.MethodPost, Header: make(http.Header), Form: url.Values{
		"scopes": {apiScopeSitesDelete, apiScopeCategoriesDelete, apiScopeNavigationDelete},
	}}
	got := automationScopesFromFormRequired(req)
	joined := "," + strings.Join(got, ",") + ","
	for _, want := range []string{apiScopeControlRead, apiScopeControlUnlock, apiScopeSitesDelete, apiScopeCategoriesDelete, apiScopeNavigationDelete} {
		if !strings.Contains(joined, ","+want+",") {
			t.Fatalf("control scope dependency %q missing from %v", want, got)
		}
	}
	themeReq := &http.Request{Method: http.MethodPost, Header: make(http.Header), Form: url.Values{
		"scopes": {apiScopeThemesApply},
	}}
	themeScopes := "," + strings.Join(automationScopesFromFormRequired(themeReq), ",") + ","
	for _, want := range []string{apiScopeControlRead, apiScopeControlUnlock, apiScopeThemesApply} {
		if !strings.Contains(themeScopes, ","+want+",") {
			t.Fatalf("theme scope dependency %q missing from %s", want, themeScopes)
		}
	}
	legacySecurityScope := retiredAPIScopeSecurityWrite
	securityReq := &http.Request{Method: http.MethodPost, Header: make(http.Header), Form: url.Values{
		"scopes": {legacySecurityScope},
	}}
	securityScopes := automationScopesFromFormRequired(securityReq)
	securityJoined := "," + strings.Join(securityScopes, ",") + ","
	if strings.Contains(securityJoined, ","+legacySecurityScope+",") || automationScopeValid(legacySecurityScope) {
		t.Fatalf("legacy security:write must not be grantable from automation forms: %v", securityScopes)
	}
	if apiScopeMap(apiScopeControlRead + "," + legacySecurityScope)[legacySecurityScope] {
		t.Fatal("legacy security:write must be discarded when old keys are parsed")
	}
	defaults := "," + strings.Join(defaultAutomationScopes(), ",") + ","
	for _, denied := range platformControlScopes() {
		if strings.Contains(defaults, ","+denied+",") {
			t.Fatalf("legacy default scopes unexpectedly include %q", denied)
		}
	}
}
