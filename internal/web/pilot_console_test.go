package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
)

func pilotJSONRequest(t *testing.T, h http.Handler, method, path string, body any, headers map[string]string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, "https://platform.test"+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func bindPilotThroughAPI(t *testing.T, h http.Handler, ps *platform.Store, siteID int64) (string, string) {
	t.Helper()
	token := "gcmsp_pilot_console_test_token"
	if _, err := ps.CreatePlatformKey("Pilot", token, "gcmsp_pilot…", platform.KeyMembershipAll, "posts:read,posts:write,sites:create", nil, time.Time{}); err != nil {
		t.Fatal(err)
	}
	rec := pilotJSONRequest(t, h, http.MethodPost, "/api/platform/v1/pilot/bindings", map[string]any{
		"device_id": "device-web-test", "device_secret": "device-secret-test",
		"device_name": "Test Mac", "pilot_version": "0.2.39", "protocol_version": "1",
		"platform": "macos", "connection_name": "Production", "default_site_id": siteID,
	}, map[string]string{"Authorization": "Bearer " + token}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("bind = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Binding struct {
			ID string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Binding.ID, "device-secret-test"
}

func TestPilotBindingHeartbeatQueueClaimAndEventRoundTrip(t *testing.T) {
	_, h, ps, defaultSite, _ := setupPlatformAutomation(t)
	bindingID, secret := bindPilotThroughAPI(t, h, ps, defaultSite.ID)
	deviceHeaders := map[string]string{
		"Authorization":       "Bearer " + secret,
		"X-GCMS-Pilot-Device": "device-web-test",
	}
	heartbeat := pilotJSONRequest(t, h, http.MethodPost, "/api/platform/v1/pilot/heartbeat", map[string]any{
		"Name": "Test Mac", "Version": "0.2.39", "Protocol": "1",
		"bindings": []map[string]any{{
			"binding_id": bindingID,
			"scheduled_tasks": []map[string]any{{"id": "daily-seo", "title": "Daily SEO"}},
			"managed_sites":   []map[string]any{{"id": "managed-main", "site_name": "Main"}},
		}},
	}, deviceHeaders, nil)
	if heartbeat.Code != http.StatusOK || !strings.Contains(heartbeat.Body.String(), `"valid":true`) {
		t.Fatalf("heartbeat = %d: %s", heartbeat.Code, heartbeat.Body.String())
	}

	cookie := platformAdminSession(t, ps)
	overview := pilotJSONRequest(t, h, http.MethodGet, "/admin/pilot/api/overview", map[string]any{}, nil, cookie)
	if overview.Code != http.StatusOK || !strings.Contains(overview.Body.String(), `daily-seo`) || !strings.Contains(overview.Body.String(), `managed-main`) {
		t.Fatalf("overview snapshot = %d: %s", overview.Code, overview.Body.String())
	}
	create := pilotJSONRequest(t, h, http.MethodPost, "/admin/pilot/api/tasks", map[string]any{
		"BindingID": bindingID, "RequestID": "request-web-1", "Prompt": "Audit SEO",
		"Operation": "conversation.create", "Risk": "write", "SiteIDs": []int64{defaultSite.ID},
		"Brain": "codex", "Effort": "high",
	}, map[string]string{"X-CSRF-Token": "csrf"}, cookie)
	if create.Code != http.StatusCreated {
		t.Fatalf("create task = %d: %s", create.Code, create.Body.String())
	}
	var createdOut struct {
		Task struct {
			ConversationID string
		}
	}
	if err := json.Unmarshal(create.Body.Bytes(), &createdOut); err != nil || createdOut.Task.ConversationID == "" {
		t.Fatalf("create conversation id: out=%#v err=%v", createdOut, err)
	}
	continueTask := pilotJSONRequest(t, h, http.MethodPost, "/admin/pilot/api/tasks", map[string]any{
		"BindingID": bindingID, "ConversationID": createdOut.Task.ConversationID,
		"RequestID": "request-web-continue", "Prompt": "Continue the same conversation",
		"Operation": "conversation.create", "Risk": "write", "SiteIDs": []int64{defaultSite.ID},
		"Brain": "codex",
	}, map[string]string{"X-CSRF-Token": "csrf"}, cookie)
	if continueTask.Code != http.StatusCreated || !strings.Contains(continueTask.Body.String(), `"ConversationID":"`+createdOut.Task.ConversationID+`"`) {
		t.Fatalf("continue task = %d: %s", continueTask.Code, continueTask.Body.String())
	}
	missingConversation := pilotJSONRequest(t, h, http.MethodPost, "/admin/pilot/api/tasks", map[string]any{
		"BindingID": bindingID, "ConversationID": "missing-conversation",
		"RequestID": "request-web-missing-conversation", "Prompt": "Do not create",
		"Operation": "conversation.create", "Risk": "write", "SiteIDs": []int64{defaultSite.ID},
	}, map[string]string{"X-CSRF-Token": "csrf"}, cookie)
	if missingConversation.Code != http.StatusNotFound || !strings.Contains(missingConversation.Body.String(), "conversation_not_found") {
		t.Fatalf("missing conversation = %d: %s", missingConversation.Code, missingConversation.Body.String())
	}
	claim := pilotJSONRequest(t, h, http.MethodPost, "/api/platform/v1/pilot/tasks/claim", map[string]any{}, deviceHeaders, nil)
	if claim.Code != http.StatusOK || !strings.Contains(claim.Body.String(), `"lease_token"`) || !strings.Contains(claim.Body.String(), `"RequestID":"request-web-1"`) {
		t.Fatalf("claim = %d: %s", claim.Code, claim.Body.String())
	}
	var claimOut struct {
		Task       struct{ ID string }
		LeaseToken string `json:"lease_token"`
	}
	if err := json.Unmarshal(claim.Body.Bytes(), &claimOut); err != nil {
		t.Fatal(err)
	}
	waiting := pilotJSONRequest(t, h, http.MethodPost, "/api/platform/v1/pilot/tasks/"+claimOut.Task.ID+"/confirmation", map[string]any{
		"LeaseToken": claimOut.LeaseToken, "ConfirmationID": "permit-web-1",
		"Confirmation": map[string]any{"tool": "Bash", "desc": "publish site"},
	}, deviceHeaders, nil)
	if waiting.Code != http.StatusOK || !strings.Contains(waiting.Body.String(), `"Status":"waiting_confirmation"`) {
		t.Fatalf("waiting confirmation = %d: %s", waiting.Code, waiting.Body.String())
	}
	confirm := pilotJSONRequest(t, h, http.MethodPost, "/admin/pilot/api/tasks/"+claimOut.Task.ID+"/confirm", map[string]any{"Allow": true}, map[string]string{"X-CSRF-Token": "csrf"}, cookie)
	if confirm.Code != http.StatusOK || !strings.Contains(confirm.Body.String(), `"ConfirmationDecision":"allow"`) {
		t.Fatalf("confirm = %d: %s", confirm.Code, confirm.Body.String())
	}
	polled := pilotJSONRequest(t, h, http.MethodPost, "/api/platform/v1/pilot/tasks/"+claimOut.Task.ID+"/confirmation", map[string]any{
		"LeaseToken": claimOut.LeaseToken, "ConfirmationID": "permit-web-1",
		"Confirmation": map[string]any{"tool": "Bash", "desc": "publish site"},
	}, deviceHeaders, nil)
	if polled.Code != http.StatusOK || !strings.Contains(polled.Body.String(), `"ConfirmationDecision":"allow"`) {
		t.Fatalf("poll confirmation = %d: %s", polled.Code, polled.Body.String())
	}
}

func TestPilotAdminWritesRequireHeaderCSRFAndTaskRequestIsIdempotent(t *testing.T) {
	_, h, ps, defaultSite, _ := setupPlatformAutomation(t)
	bindingID, _ := bindPilotThroughAPI(t, h, ps, defaultSite.ID)
	cookie := platformAdminSession(t, ps)
	body := map[string]any{
		"BindingID": bindingID, "RequestID": "same-request", "Prompt": "Audit only",
		"Operation": "conversation.create", "Risk": "write", "SiteIDs": []int64{defaultSite.ID},
	}
	missing := pilotJSONRequest(t, h, http.MethodPost, "/admin/pilot/api/tasks", body, nil, cookie)
	if missing.Code != http.StatusForbidden {
		t.Fatalf("missing csrf = %d: %s", missing.Code, missing.Body.String())
	}
	first := pilotJSONRequest(t, h, http.MethodPost, "/admin/pilot/api/tasks", body, map[string]string{"X-CSRF-Token": "csrf"}, cookie)
	second := pilotJSONRequest(t, h, http.MethodPost, "/admin/pilot/api/tasks", body, map[string]string{"X-CSRF-Token": "csrf"}, cookie)
	if first.Code != http.StatusCreated || second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"created":false`) {
		t.Fatalf("idempotency first=%d %s second=%d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	body["Prompt"] = "Different write"
	conflict := pilotJSONRequest(t, h, http.MethodPost, "/admin/pilot/api/tasks", body, map[string]string{"X-CSRF-Token": "csrf"}, cookie)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "idempotency_conflict") {
		t.Fatalf("conflict = %d: %s", conflict.Code, conflict.Body.String())
	}
}
