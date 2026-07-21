package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
)

func controlProtocolKey(t *testing.T, ps *platform.Store, scopes string) *platform.PlatformAutomationKey {
	t.Helper()
	token := "gcmsp_protocol_" + strings.ReplaceAll(scopes, ":", "_")
	keyID, err := ps.CreatePlatformKey("protocol", token, token[:13], platform.KeyMembershipAll, scopes, nil, time.Time{})
	if err != nil {
		t.Fatalf("create platform key: %v", err)
	}
	key, ok, err := ps.GetPlatformKey(keyID)
	if err != nil || !ok {
		t.Fatalf("get platform key: ok=%v err=%v", ok, err)
	}
	return key
}

func controlProtocolRequest(operation, idempotencyKey, fingerprint, query string) (*http.Request, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodPost, "https://platform.test/control"+query, nil)
	if operation != "" {
		req.Header.Set(controlConfirmHeader, operation)
	}
	if idempotencyKey != "" {
		req.Header.Set(controlIdempotencyHeader, idempotencyKey)
	}
	req.Header.Set("X-Test-Fingerprint", fingerprint)
	return req, httptest.NewRecorder()
}

func TestControlIdempotencyKeyLengthMatchesContract(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{key: "1234567", want: false},
		{key: "12345678", want: true},
		{key: strings.Repeat("a", 128), want: true},
		{key: strings.Repeat("a", 129), want: false},
		{key: "invalid key", want: false},
	} {
		if got := validControlIdempotencyKey(tc.key); got != tc.want {
			t.Errorf("validControlIdempotencyKey(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}

func TestControlMutationRequiresExactConfirmationAndIdempotencyKey(t *testing.T) {
	srv, _, ps, _, _ := setupPlatformAutomation(t)
	key := controlProtocolKey(t, ps, apiScopeSitesCreate)
	var calls atomic.Int32
	fn := func() (int, any, error) {
		calls.Add(1)
		return http.StatusCreated, map[string]any{"ok": true}, nil
	}

	req, rec := controlProtocolRequest("", "confirm-1", "same", "")
	srv.executeControlMutation(rec, req, key, "sites.create", "same", fn)
	if rec.Code != http.StatusPreconditionRequired || !strings.Contains(rec.Body.String(), "confirmation_required") {
		t.Fatalf("missing confirmation = %d %s", rec.Code, rec.Body.String())
	}

	req, rec = controlProtocolRequest("sites.create ", "confirm-2", "same", "")
	srv.executeControlMutation(rec, req, key, "sites.create", "same", fn)
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("non-exact confirmation = %d %s", rec.Code, rec.Body.String())
	}

	req, rec = controlProtocolRequest("sites.create", "", "same", "")
	srv.executeControlMutation(rec, req, key, "sites.create", "same", fn)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_idempotency_key") {
		t.Fatalf("missing idempotency key = %d %s", rec.Code, rec.Body.String())
	}

	req, rec = controlProtocolRequest("sites.create", "invalid key", "same", "")
	srv.executeControlMutation(rec, req, key, "sites.create", "same", fn)
	if rec.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("invalid idempotency key = %d calls=%d", rec.Code, calls.Load())
	}
}

func TestControlMutationDryRunSkipsWriteGuardsAndReceipt(t *testing.T) {
	srv, _, ps, _, _ := setupPlatformAutomation(t)
	key := controlProtocolKey(t, ps, apiScopeSitesCreate)
	for _, query := range []string{"?dry_run=1", "?dry_run=true", "?dry_run=TRUE"} {
		req, rec := controlProtocolRequest("", "", "preview", query)
		srv.executeControlMutation(rec, req, key, "sites.create", "preview", func() (int, any, error) {
			if !controlDryRun(req) {
				t.Fatal("business handler did not observe dry-run")
			}
			return http.StatusOK, map[string]any{"dry_run": true}, nil
		})
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"dry_run":true`) {
			t.Fatalf("dry-run %q = %d %s", query, rec.Code, rec.Body.String())
		}
	}
	if _, ok, err := ps.GetControlOperationReceipt(key.ID, "sites.create", ""); err != nil || ok {
		t.Fatalf("dry-run created receipt: ok=%v err=%v", ok, err)
	}
}

func TestControlMutationReplayConflictAndRetry(t *testing.T) {
	srv, _, ps, _, _ := setupPlatformAutomation(t)
	key := controlProtocolKey(t, ps, apiScopeSitesCreate)
	var calls atomic.Int32
	fn := func() (int, any, error) {
		calls.Add(1)
		return http.StatusCreated, map[string]any{"site_id": 42}, nil
	}
	req, first := controlProtocolRequest("sites.create", "create-site-42", "site-42", "")
	srv.executeControlMutation(first, req, key, "sites.create", "site-42", fn)
	if first.Code != http.StatusCreated {
		t.Fatalf("first mutation = %d %s", first.Code, first.Body.String())
	}

	req, replay := controlProtocolRequest("sites.create", "create-site-42", "site-42", "")
	srv.executeControlMutation(replay, req, key, "sites.create", "site-42", fn)
	if replay.Code != http.StatusCreated || replay.Header().Get(controlIdempotencyReplayedHeader) != "true" || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay = %d headers=%v body=%q, first=%q", replay.Code, replay.Header(), replay.Body.String(), first.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("business handler calls = %d, want 1", calls.Load())
	}
	receipt, ok, err := ps.GetControlOperationReceipt(key.ID, "sites.create", "create-site-42")
	if err != nil || !ok || receipt.RequestHash != controlRequestHash("site-42") || receipt.RequestHash == "site-42" {
		t.Fatalf("stored fingerprint = %#v ok=%v err=%v", receipt, ok, err)
	}

	req, conflict := controlProtocolRequest("sites.create", "create-site-42", "different-site", "")
	srv.executeControlMutation(conflict, req, key, "sites.create", "different-site", fn)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "idempotency_conflict") {
		t.Fatalf("conflict = %d %s", conflict.Code, conflict.Body.String())
	}

	retryKey := "retry-after-business-error"
	req, failed := controlProtocolRequest("sites.create", retryKey, "retry-site", "")
	srv.executeControlMutation(failed, req, key, "sites.create", "retry-site", func() (int, any, error) {
		return 0, nil, newControlMutationError(http.StatusUnprocessableEntity, "site_invalid", "站点参数无效。")
	})
	if failed.Code != http.StatusUnprocessableEntity || !strings.Contains(failed.Body.String(), "site_invalid") {
		t.Fatalf("business error = %d %s", failed.Code, failed.Body.String())
	}
	req, retried := controlProtocolRequest("sites.create", retryKey, "retry-site", "")
	srv.executeControlMutation(retried, req, key, "sites.create", "retry-site", fn)
	if retried.Code != http.StatusCreated {
		t.Fatalf("retry after business error = %d %s", retried.Code, retried.Body.String())
	}
}

func TestControlMutationConcurrentRequestReportsInProgress(t *testing.T) {
	srv, _, ps, _, _ := setupPlatformAutomation(t)
	key := controlProtocolKey(t, ps, apiScopeSitesCreate)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req, rec := controlProtocolRequest("sites.create", "concurrent-site", "concurrent", "")
		srv.executeControlMutation(rec, req, key, "sites.create", "concurrent", func() (int, any, error) {
			close(started)
			<-release
			return http.StatusCreated, map[string]any{"ok": true}, nil
		})
		done <- rec
	}()
	<-started
	req, inProgress := controlProtocolRequest("sites.create", "concurrent-site", "concurrent", "")
	srv.executeControlMutation(inProgress, req, key, "sites.create", "concurrent", func() (int, any, error) {
		t.Fatal("concurrent duplicate executed business handler")
		return 0, nil, nil
	})
	if inProgress.Code != http.StatusConflict || inProgress.Header().Get("Retry-After") == "" || !strings.Contains(inProgress.Body.String(), "idempotency_in_progress") {
		t.Fatalf("in-progress = %d headers=%v body=%s", inProgress.Code, inProgress.Header(), inProgress.Body.String())
	}
	close(release)
	if first := <-done; first.Code != http.StatusCreated {
		t.Fatalf("first concurrent request = %d %s", first.Code, first.Body.String())
	}
}

func TestControlMutationChecksScopeAndUnlockFromCatalog(t *testing.T) {
	srv, _, ps, _, _ := setupPlatformAutomation(t)
	withoutScope := controlProtocolKey(t, ps, apiScopeControlRead)
	req, denied := controlProtocolRequest("sites.create", "scope-denied", "scope", "")
	srv.executeControlMutation(denied, req, withoutScope, "sites.create", "scope", func() (int, any, error) {
		t.Fatal("missing-scope request executed")
		return 0, nil, nil
	})
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "missing_scope") {
		t.Fatalf("scope denial = %d %s", denied.Code, denied.Body.String())
	}

	hash := setPlatformTestPassword(t, ps, controlTestPassword)
	unlockedKey := controlProtocolKey(t, ps, strings.Join([]string{apiScopeControlUnlock, apiScopeDomainsWrite}, ","))
	req, locked := controlProtocolRequest("domains.apply", "domain-apply-1", "domain", "")
	srv.executeControlMutation(locked, req, unlockedKey, "domains.apply", "domain", func() (int, any, error) {
		t.Fatal("locked request executed")
		return 0, nil, nil
	})
	if locked.Code != http.StatusForbidden || !strings.Contains(locked.Body.String(), "unlock_required") {
		t.Fatalf("unlock denial = %d %s", locked.Code, locked.Body.String())
	}

	unlockToken, _, err := srv.controlGrants.issue(unlockedKey.ID, []string{"domains.apply"}, controlCredentialRevision(hash), time.Now())
	if err != nil {
		t.Fatalf("issue unlock: %v", err)
	}
	req, allowed := controlProtocolRequest("domains.apply", "domain-apply-1", "domain", "")
	req.Header.Set(controlUnlockHeader, unlockToken)
	srv.executeControlMutation(allowed, req, unlockedKey, "domains.apply", "domain", func() (int, any, error) {
		return http.StatusOK, map[string]any{"applied": true}, nil
	})
	if allowed.Code != http.StatusOK {
		t.Fatalf("unlocked mutation = %d %s", allowed.Code, allowed.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(allowed.Body.Bytes(), &payload); err != nil || payload["applied"] != true {
		t.Fatalf("unlocked response = %v err=%v", payload, err)
	}
}
