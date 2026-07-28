package web

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cms.ccvar.com/internal/platform"
	"golang.org/x/crypto/bcrypt"
)

const (
	pilotLeaseTTL  = 40 * time.Second
	pilotUnlockTTL = 3 * time.Minute
)

func pilotRandom(prefix string) string {
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	return prefix + base64.RawURLEncoding.EncodeToString(raw)
}

func pilotAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": code, "message": message})
}

func (s *Server) pilotPlatformRequired(w http.ResponseWriter) bool {
	if s.platform == nil {
		pilotAPIError(w, http.StatusNotFound, "pilot_console_unavailable", "Pilot 控制台需要 GCMS 平台模式。")
		return false
	}
	return true
}

func pilotBearer(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) > 7 && strings.EqualFold(value[:7], "Bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

func (s *Server) authenticatePilotDevice(w http.ResponseWriter, r *http.Request) (*platform.PilotDevice, bool) {
	if !s.pilotPlatformRequired(w) {
		return nil, false
	}
	deviceID := strings.TrimSpace(r.Header.Get("X-GCMS-Pilot-Device"))
	secret := pilotBearer(r)
	if deviceID == "" || secret == "" {
		pilotAPIError(w, http.StatusUnauthorized, "device_auth_required", "缺少 Pilot 设备凭据。")
		return nil, false
	}
	device, ok, err := s.platform.AuthenticatePilot(deviceID, secret)
	if err != nil {
		pilotAPIError(w, http.StatusInternalServerError, "device_auth_error", err.Error())
		return nil, false
	}
	if !ok {
		pilotAPIError(w, http.StatusUnauthorized, "device_credential_revoked", "设备凭据无效或已撤销。")
		return nil, false
	}
	return device, true
}

type pilotBindInput struct {
	DeviceID       string `json:"device_id"`
	DeviceSecret   string `json:"device_secret"`
	DeviceName     string `json:"device_name"`
	PilotVersion   string `json:"pilot_version"`
	Protocol       string `json:"protocol_version"`
	Platform       string `json:"platform"`
	ConnectionName string `json:"connection_name"`
	DefaultSiteID  int64  `json:"default_site_id"`
}

func (s *Server) servePilotDeviceAPI(w http.ResponseWriter, r *http.Request, pool *SiteRuntimePool) {
	if !s.pilotPlatformRequired(w) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/platform/v1/pilot")
	switch {
	case path == "/bindings" && r.Method == http.MethodPost:
		s.pilotBind(w, r, pool)
	case path == "/heartbeat" && r.Method == http.MethodPost:
		s.pilotHeartbeat(w, r)
	case path == "/tasks/claim" && r.Method == http.MethodPost:
		s.pilotClaim(w, r)
	case strings.HasPrefix(path, "/tasks/") && strings.HasSuffix(path, "/events") && r.Method == http.MethodPost:
		s.pilotDeviceEvent(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/tasks/"), "/events"))
	case strings.HasPrefix(path, "/tasks/") && strings.HasSuffix(path, "/confirmation") && r.Method == http.MethodPost:
		s.pilotDeviceConfirmation(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/tasks/"), "/confirmation"))
	case strings.HasPrefix(path, "/tasks/") && r.Method == http.MethodPatch:
		s.pilotDeviceTaskUpdate(w, r, strings.TrimPrefix(path, "/tasks/"))
	case strings.HasPrefix(path, "/conversations/") && r.Method == http.MethodPatch:
		s.pilotDeviceConversationSync(w, r, strings.TrimPrefix(path, "/conversations/"))
	case strings.HasPrefix(path, "/bindings/") && r.Method == http.MethodDelete:
		s.pilotDeviceUnbind(w, r, strings.TrimPrefix(path, "/bindings/"))
	case strings.HasPrefix(path, "/bindings/") && strings.HasSuffix(path, "/default-site") && r.Method == http.MethodPatch:
		s.pilotDeviceDefault(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/bindings/"), "/default-site"))
	default:
		pilotAPIError(w, http.StatusNotFound, "not_found", "Pilot 接口不存在。")
	}
}

func (s *Server) pilotDeviceConversationSync(w http.ResponseWriter, r *http.Request, conversationID string) {
	device, ok := s.authenticatePilotDevice(w, r)
	if !ok {
		return
	}
	var in struct {
		BindingID string          `json:"binding_id"`
		Snapshot  json.RawMessage `json:"snapshot"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&in) != nil || in.BindingID == "" || len(in.Snapshot) == 0 {
		pilotAPIError(w, http.StatusBadRequest, "invalid_conversation_sync", "对话同步格式无效。")
		return
	}
	changed, err := s.platform.SyncPilotConversation(device.ID, in.BindingID, conversationID, string(in.Snapshot))
	if err != nil {
		pilotAPIError(w, http.StatusForbidden, "conversation_sync_forbidden", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": changed})
}

func (s *Server) pilotBind(w http.ResponseWriter, r *http.Request, pool *SiteRuntimePool) {
	token := apiTokenFromRequest(r)
	if token == "" {
		pilotAPIError(w, http.StatusUnauthorized, "skill_credential_required", "缺少技能包访问密钥。")
		return
	}
	var in pilotBindInput
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in) != nil {
		pilotAPIError(w, http.StatusBadRequest, "invalid_json", "绑定请求格式无效。")
		return
	}
	if in.Protocol == "" {
		in.Protocol = platform.PilotProtocolVersion
	}
	if in.Protocol != platform.PilotProtocolVersion {
		pilotAPIError(w, http.StatusUpgradeRequired, "protocol_incompatible", "Pilot 与 GCMS 协议版本不兼容，请升级。")
		return
	}
	kind := ""
	var keyID, siteID int64
	keyPrefix := ""
	if key, ok, err := s.platform.GetPlatformKeyByToken(token); err == nil && ok {
		kind, keyID, keyPrefix = "platform", key.ID, key.TokenPrefix
	} else if err != nil {
		pilotAPIError(w, http.StatusInternalServerError, "auth_error", err.Error())
		return
	} else {
		for id, rt := range pool.byID {
			if rt == nil || rt.Store == nil || rt.Site == nil {
				continue
			}
			if key, ok, err := rt.Store.GetAutomationKeyByToken(token); err == nil && ok {
				kind, keyID, siteID, keyPrefix = "site", key.ID, id, key.TokenPrefix
				break
			}
		}
	}
	if kind == "" {
		pilotAPIError(w, http.StatusUnauthorized, "skill_credential_invalid", "技能包密钥无效、已过期或已撤销。")
		return
	}
	if in.DefaultSiteID > 0 && !s.pilotCredentialAllowsSite(kind, keyID, siteID, in.DefaultSiteID) {
		pilotAPIError(w, http.StatusForbidden, "default_site_forbidden", "默认站点不在技能包授权范围内。")
		return
	}
	bindingID := pilotRandom("pb_")
	binding, err := s.platform.BindPilot(in.DeviceID, in.DeviceSecret, in.DeviceName, in.PilotVersion, in.Protocol, in.Platform, bindingID, kind, keyID, siteID, in.ConnectionName, keyPrefix, in.DefaultSiteID)
	if err != nil {
		code := "bind_failed"
		if strings.Contains(err.Error(), "device_secret_mismatch") {
			code = "device_secret_mismatch"
		}
		pilotAPIError(w, http.StatusConflict, code, err.Error())
		return
	}
	_ = s.platform.PilotAudit("", in.DeviceID, binding.ID, in.DefaultSiteID, "", "binding.created", "Pilot 技能包连接已绑定")
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "binding": binding, "protocol_version": platform.PilotProtocolVersion, "heartbeat_seconds": 20})
}

func (s *Server) pilotCredentialAllowsSite(kind string, keyID, credentialSiteID, targetSiteID int64) bool {
	if targetSiteID <= 0 {
		return false
	}
	if kind == "platform" {
		key, ok, err := s.platform.GetPlatformKey(keyID)
		if err != nil || !ok || key == nil || !key.Active() {
			return false
		}
		allowed, err := s.platform.PlatformKeyCanAccessSite(key, targetSiteID)
		return err == nil && allowed
	}
	if kind != "site" || credentialSiteID != targetSiteID {
		return false
	}
	rt, ok := s.runtimePool().runtimeByID(credentialSiteID)
	if !ok || rt == nil || rt.Store == nil {
		return false
	}
	key, ok, err := rt.Store.GetAutomationKeyByID(keyID)
	return err == nil && ok && key != nil && key.RevokedAt.IsZero()
}

func (s *Server) pilotBindingSites(binding *platform.PilotBinding) ([]map[string]any, error) {
	var out []map[string]any
	domainBySite := map[int64]string{}
	if domains, err := s.platform.SiteDomains(); err == nil {
		for _, domain := range domains {
			if domain == nil || !domain.Enabled {
				continue
			}
			if domain.IsPrimary || domainBySite[domain.SiteID] == "" {
				domainBySite[domain.SiteID] = domain.Host
			}
		}
	}
	if binding.CredentialKind == "platform" {
		key, ok, err := s.platform.GetPlatformKey(binding.CredentialID)
		if err != nil || !ok || key == nil || !key.Active() {
			return nil, errors.New("技能包密钥已失效")
		}
		sites, err := s.platform.ManageableSites(key)
		if err != nil {
			return nil, err
		}
		for _, site := range sites {
			out = append(out, map[string]any{"id": site.ID, "slug": site.Slug, "name": site.Name, "domain": domainBySite[site.ID], "favicon": s.platformSiteIconURL(site.ID), "is_default": site.ID == binding.DefaultSiteID})
		}
		return out, nil
	}
	if !s.pilotCredentialAllowsSite("site", binding.CredentialID, binding.CredentialSiteID, binding.CredentialSiteID) {
		return nil, errors.New("单站技能包密钥已失效")
	}
	site, ok, err := s.platform.GetSite(binding.CredentialSiteID)
	if err != nil || !ok {
		return nil, errors.New("授权站点已不存在")
	}
	return []map[string]any{{"id": site.ID, "slug": site.Slug, "name": site.Name, "domain": domainBySite[site.ID], "favicon": s.platformSiteIconURL(site.ID), "is_default": true}}, nil
}

func (s *Server) pilotHeartbeat(w http.ResponseWriter, r *http.Request) {
	device, ok := s.authenticatePilotDevice(w, r)
	if !ok {
		return
	}
	var in struct {
		Name, Version, Protocol string
		Bindings                []struct {
			BindingID      string          `json:"binding_id"`
			ScheduledTasks json.RawMessage `json:"scheduled_tasks"`
			ManagedSites   json.RawMessage `json:"managed_sites"`
		} `json:"bindings"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&in)
	if in.Protocol != "" && in.Protocol != platform.PilotProtocolVersion {
		pilotAPIError(w, http.StatusUpgradeRequired, "protocol_incompatible", "协议版本不兼容。")
		return
	}
	if in.Name == "" {
		in.Name = device.Name
	}
	if in.Version == "" {
		in.Version = device.PilotVersion
	}
	if in.Protocol == "" {
		in.Protocol = platform.PilotProtocolVersion
	}
	if err := s.platform.HeartbeatPilot(device.ID, in.Name, in.Version, in.Protocol); err != nil {
		pilotAPIError(w, http.StatusUnauthorized, "device_credential_revoked", err.Error())
		return
	}
	for _, snapshot := range in.Bindings {
		scheduled := snapshot.ScheduledTasks
		managed := snapshot.ManagedSites
		if len(scheduled) == 0 {
			scheduled = json.RawMessage(`[]`)
		}
		if len(managed) == 0 {
			managed = json.RawMessage(`[]`)
		}
		if snapshot.BindingID != "" {
			if err := s.platform.SyncPilotBindingSnapshot(device.ID, snapshot.BindingID, string(scheduled), string(managed)); err != nil {
				pilotAPIError(w, http.StatusForbidden, "binding_snapshot_forbidden", "Pilot 本地状态不属于当前设备或绑定。")
				return
			}
		}
	}
	bindings, _ := s.platform.ActivePilotBindings(device.ID)
	var payload []map[string]any
	for _, binding := range bindings {
		sites, err := s.pilotBindingSites(binding)
		defaultValid := binding.DefaultSiteID == 0
		for _, site := range sites {
			if site["id"] == binding.DefaultSiteID {
				defaultValid = true
			}
		}
		item := map[string]any{"binding": binding, "sites": sites, "valid": err == nil, "default_site_valid": defaultValid}
		if err != nil {
			item["error"] = err.Error()
		}
		payload = append(payload, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "server_time": time.Now(), "bindings": payload})
}

func (s *Server) pilotClaim(w http.ResponseWriter, r *http.Request) {
	device, ok := s.authenticatePilotDevice(w, r)
	if !ok {
		return
	}
	deadline := time.Now().Add(25 * time.Second)
	for {
		leaseToken := pilotRandom("pl_")
		task, err := s.platform.ClaimPilotTask(device.ID, leaseToken, pilotLeaseTTL)
		if err != nil {
			pilotAPIError(w, http.StatusConflict, "claim_failed", err.Error())
			return
		}
		if task != nil {
			binding, err := s.platform.GetPilotBinding(task.BindingID)
			if err != nil {
				pilotAPIError(w, http.StatusConflict, "binding_invalid", err.Error())
				return
			}
			var siteIDs []int64
			_ = json.Unmarshal([]byte(task.SiteIDsJSON), &siteIDs)
			for _, siteID := range siteIDs {
				if !s.pilotCredentialAllowsSite(binding.CredentialKind, binding.CredentialID, binding.CredentialSiteID, siteID) {
					_ = s.platform.UpdatePilotTask(device.ID, task.ID, leaseToken, "failed", "", "", "site_forbidden", "站点授权已移除", pilotLeaseTTL)
					pilotAPIError(w, http.StatusForbidden, "site_forbidden", "站点授权已移除。")
					return
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": task, "lease_token": leaseToken, "lease_seconds": int(pilotLeaseTTL.Seconds())})
			return
		}
		if time.Now().After(deadline) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": nil})
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (s *Server) pilotDeviceEvent(w http.ResponseWriter, r *http.Request, taskID string) {
	device, ok := s.authenticatePilotDevice(w, r)
	if !ok {
		return
	}
	var in struct {
		LeaseToken, Type string
		Payload          json.RawMessage
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&in) != nil || in.LeaseToken == "" || in.Type == "" {
		pilotAPIError(w, http.StatusBadRequest, "invalid_event", "事件格式无效。")
		return
	}
	if len(in.Payload) == 0 {
		in.Payload = []byte(`{}`)
	}
	event, err := s.platform.AppendPilotTaskEvent(device.ID, taskID, in.LeaseToken, in.Type, string(in.Payload))
	if err != nil {
		pilotAPIError(w, http.StatusConflict, "lease_lost", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "event": event})
}

func (s *Server) pilotDeviceConfirmation(w http.ResponseWriter, r *http.Request, taskID string) {
	device, ok := s.authenticatePilotDevice(w, r)
	if !ok {
		return
	}
	var in struct {
		LeaseToken, ConfirmationID string
		Confirmation               json.RawMessage
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&in) != nil || in.LeaseToken == "" || in.ConfirmationID == "" || len(in.Confirmation) == 0 {
		pilotAPIError(w, http.StatusBadRequest, "invalid_confirmation", "确认请求格式无效。")
		return
	}
	task, err := s.platform.SetPilotTaskConfirmation(device.ID, taskID, in.LeaseToken, in.ConfirmationID, string(in.Confirmation), pilotLeaseTTL)
	if err != nil {
		pilotAPIError(w, http.StatusConflict, "confirmation_rejected", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": task})
}

func (s *Server) pilotDeviceTaskUpdate(w http.ResponseWriter, r *http.Request, taskID string) {
	device, ok := s.authenticatePilotDevice(w, r)
	if !ok {
		return
	}
	var in struct{ LeaseToken, Status, Progress, Output, ErrorCode, ErrorMessage string }
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&in) != nil {
		pilotAPIError(w, 400, "invalid_json", "任务状态格式无效。")
		return
	}
	if err := s.platform.UpdatePilotTask(device.ID, taskID, in.LeaseToken, in.Status, in.Progress, in.Output, in.ErrorCode, in.ErrorMessage, pilotLeaseTTL); err != nil {
		pilotAPIError(w, 409, "lease_lost", err.Error())
		return
	}
	task, _ := s.platform.GetPilotTask(taskID)
	if task != nil && (in.Status == "completed" || in.Status == "failed" || in.Status == "canceled") {
		_ = s.platform.PilotAudit("", device.ID, task.BindingID, 0, task.RequestID, "task."+in.Status, in.ErrorMessage)
	}
	writeJSON(w, 200, map[string]any{"ok": true, "task": task})
}

func (s *Server) pilotDeviceUnbind(w http.ResponseWriter, r *http.Request, bindingID string) {
	device, ok := s.authenticatePilotDevice(w, r)
	if !ok {
		return
	}
	binding, err := s.platform.GetPilotBinding(bindingID)
	if err != nil || binding.DeviceID != device.ID {
		pilotAPIError(w, 404, "binding_not_found", "绑定不存在。")
		return
	}
	if err = s.platform.RevokePilotBinding(bindingID); err != nil {
		pilotAPIError(w, 409, "unbind_failed", err.Error())
		return
	}
	_ = s.platform.PilotAudit("", device.ID, bindingID, 0, "", "binding.revoked_by_device", "Pilot 端解除绑定")
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) pilotDeviceDefault(w http.ResponseWriter, r *http.Request, bindingID string) {
	device, ok := s.authenticatePilotDevice(w, r)
	if !ok {
		return
	}
	var in struct{ SiteID int64 }
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in) != nil {
		pilotAPIError(w, http.StatusBadRequest, "invalid_json", "站点格式无效。")
		return
	}
	binding, err := s.platform.GetPilotBinding(bindingID)
	if err != nil || binding.DeviceID != device.ID || !binding.RevokedAt.IsZero() {
		pilotAPIError(w, http.StatusNotFound, "binding_not_found", "绑定不存在。")
		return
	}
	if in.SiteID > 0 && !s.pilotCredentialAllowsSite(binding.CredentialKind, binding.CredentialID, binding.CredentialSiteID, in.SiteID) {
		pilotAPIError(w, http.StatusForbidden, "site_forbidden", "默认站点不在技能包授权范围内。")
		return
	}
	if err := s.platform.UpdatePilotDefaultSite(bindingID, in.SiteID); err != nil {
		pilotAPIError(w, http.StatusConflict, "default_update_failed", err.Error())
		return
	}
	_ = s.platform.PilotAudit("", device.ID, bindingID, in.SiteID, "", "binding.default_site_updated_by_device", "Pilot 端更新新对话默认站点")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) checkPilotCSRF(w http.ResponseWriter, r *http.Request) (session, bool) {
	sess, ok := s.currentSession(r)
	if !ok {
		pilotAPIError(w, 401, "login_required", "登录已过期。")
		return session{}, false
	}
	token := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
	if token == "" || token != sess.csrf {
		pilotAPIError(w, 403, "csrf_invalid", "安全令牌无效，请刷新页面。")
		return session{}, false
	}
	return sess, true
}

func (s *Server) adminPilotConsole(w http.ResponseWriter, r *http.Request) {
	if !s.pilotPlatformRequired(w) {
		return
	}
	sess, _ := s.currentSession(r)
	v := s.adminView(r, "Pilot 控制台")
	s.platformAuthed(v, sess)
	s.rnd.Admin(w, "pilot_console", 200, v)
}

func (s *Server) adminPilotAPI(w http.ResponseWriter, r *http.Request) {
	if !s.pilotPlatformRequired(w) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/admin/pilot/api")
	if r.Method != http.MethodGet {
		if _, ok := s.checkPilotCSRF(w, r); !ok {
			return
		}
	}
	switch {
	case path == "/overview" && r.Method == http.MethodGet:
		s.adminPilotOverview(w, r)
	case path == "/tasks" && r.Method == http.MethodPost:
		s.adminPilotCreateTask(w, r)
	case strings.HasPrefix(path, "/tasks/") && strings.HasSuffix(path, "/events") && r.Method == http.MethodGet:
		s.adminPilotEvents(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/tasks/"), "/events"))
	case strings.HasPrefix(path, "/tasks/") && strings.HasSuffix(path, "/cancel") && r.Method == http.MethodPost:
		s.adminPilotCancel(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/tasks/"), "/cancel"))
	case strings.HasPrefix(path, "/tasks/") && strings.HasSuffix(path, "/retry") && r.Method == http.MethodPost:
		s.adminPilotRetry(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/tasks/"), "/retry"))
	case strings.HasPrefix(path, "/tasks/") && strings.HasSuffix(path, "/confirm") && r.Method == http.MethodPost:
		s.adminPilotConfirm(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/tasks/"), "/confirm"))
	case path == "/unlock" && r.Method == http.MethodPost:
		s.adminPilotUnlock(w, r)
	case strings.HasPrefix(path, "/bindings/") && strings.HasSuffix(path, "/default-site") && r.Method == http.MethodPatch:
		s.adminPilotDefault(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/bindings/"), "/default-site"))
	case strings.HasPrefix(path, "/bindings/") && r.Method == http.MethodDelete:
		s.adminPilotUnbind(w, r, strings.TrimPrefix(path, "/bindings/"))
	default:
		pilotAPIError(w, 404, "not_found", "接口不存在。")
	}
}

func (s *Server) adminPilotOverview(w http.ResponseWriter, r *http.Request) {
	devices, err := s.platform.ListPilotDevices()
	if err != nil {
		pilotAPIError(w, 500, "store_error", err.Error())
		return
	}
	tasks, _ := s.platform.ListPilotTasks(100)
	ds := make([]map[string]any, 0)
	if tasks == nil {
		tasks = make([]*platform.PilotTask, 0)
	}
	for _, d := range devices {
		bindings, _ := s.platform.ActivePilotBindings(d.ID)
		var bs []map[string]any
		for _, b := range bindings {
			sites, e := s.pilotBindingSites(b)
			snapshot, _ := s.platform.GetPilotBindingSnapshot(b.ID)
			defaultValid := b.DefaultSiteID == 0
			for _, site := range sites {
				if site["id"] == b.DefaultSiteID {
					defaultValid = true
				}
			}
			bs = append(bs, map[string]any{"binding": b, "sites": sites, "snapshot": snapshot, "valid": e == nil, "default_site_valid": defaultValid, "error": func() string {
				if e != nil {
					return e.Error()
				}
				return ""
			}()})
		}
		ds = append(ds, map[string]any{"device": d, "online": d.Online(time.Now()), "bindings": bs})
	}
	writeJSON(w, 200, map[string]any{"ok": true, "devices": ds, "tasks": tasks, "server_time": time.Now()})
}

type adminPilotTaskInput struct {
	BindingID, RequestID, ConversationID, Prompt, Operation, Risk, Brain, Model, Effort, UnlockToken string
	SiteIDs                                                                                          []int64
}

func (s *Server) adminPilotCreateTask(w http.ResponseWriter, r *http.Request) {
	var in adminPilotTaskInput
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&in) != nil {
		pilotAPIError(w, 400, "invalid_json", "任务格式无效。")
		return
	}
	if in.RequestID == "" || in.Prompt == "" || in.BindingID == "" {
		pilotAPIError(w, 400, "missing_field", "缺少 binding_id、request_id 或任务内容。")
		return
	}
	b, err := s.platform.GetPilotBinding(in.BindingID)
	if err != nil || !b.RevokedAt.IsZero() {
		pilotAPIError(w, 404, "binding_not_found", "绑定不存在或已撤销。")
		return
	}
	sites, err := s.pilotBindingSites(b)
	if err != nil {
		pilotAPIError(w, 403, "skill_credential_invalid", err.Error())
		return
	}
	allowed := map[int64]map[string]any{}
	for _, site := range sites {
		allowed[site["id"].(int64)] = site
	}
	isSiteCreate := in.Operation == "sites.create"
	isPilotControl := strings.HasPrefix(in.Operation, "schedule.") || strings.HasPrefix(in.Operation, "managed.")
	isSiteLessControl := isPilotControl && in.Operation != "schedule.create"
	if isSiteCreate {
		if b.CredentialKind != "platform" {
			pilotAPIError(w, http.StatusForbidden, "platform_key_required", "新建站点需要平台技能包。")
			return
		}
		key, ok, keyErr := s.platform.GetPlatformKey(b.CredentialID)
		hasScope := false
		if keyErr == nil && ok {
			for _, scope := range key.ScopeList() {
				if scope == "sites:create" {
					hasScope = true
				}
			}
		}
		if !hasScope {
			pilotAPIError(w, http.StatusForbidden, "scope_required", "技能包没有 sites:create 权限。")
			return
		}
		in.SiteIDs = nil
	}
	if !isSiteCreate && !isSiteLessControl && len(in.SiteIDs) == 0 && b.DefaultSiteID > 0 {
		if _, ok := allowed[b.DefaultSiteID]; ok {
			in.SiteIDs = []int64{b.DefaultSiteID}
		}
	}
	if !isSiteCreate && !isSiteLessControl && len(in.SiteIDs) == 0 && len(sites) > 0 {
		in.SiteIDs = []int64{sites[0]["id"].(int64)}
	}
	if !isSiteCreate && !isSiteLessControl && len(in.SiteIDs) == 0 {
		pilotAPIError(w, http.StatusConflict, "no_authorized_sites", "技能包当前没有可用授权站点。")
		return
	}
	var slugs, names []string
	for _, id := range in.SiteIDs {
		site, ok := allowed[id]
		if !ok {
			pilotAPIError(w, 403, "site_forbidden", "站点不在技能包实时授权范围内。")
			return
		}
		slugs = append(slugs, site["slug"].(string))
		names = append(names, site["name"].(string))
	}
	if in.Operation == "" {
		in.Operation = "conversation.create"
	}
	if in.Risk == "" {
		in.Risk = "write"
	}
	unlockID := ""
	if in.Risk == "sensitive" || in.Risk == "destructive" {
		if len(in.SiteIDs) != 1 {
			pilotAPIError(w, 400, "unlock_site_required", "敏感操作必须绑定一个目标站点。")
			return
		}
		unlockID, err = s.platform.ConsumePilotUnlock(in.UnlockToken, b.DeviceID, b.ID, in.SiteIDs[0], in.Operation, in.RequestID)
		if err != nil {
			pilotAPIError(w, 403, "unlock_invalid_or_expired", "短时解锁无效、已过期或已使用。")
			return
		}
	}
	taskID := pilotRandom("pt_")
	convID := strings.TrimSpace(in.ConversationID)
	if convID != "" {
		belongs, lookupErr := s.platform.PilotConversationBelongs(b.ID, convID)
		if lookupErr != nil {
			pilotAPIError(w, 500, "conversation_lookup_failed", lookupErr.Error())
			return
		}
		if !belongs {
			pilotAPIError(w, 404, "conversation_not_found", "对话不存在，或不属于当前 Pilot 技能包绑定。")
			return
		}
	} else {
		convID = pilotRandom("pc_")
	}
	task, created, err := s.platform.CreatePilotTask(taskID, b.ID, in.RequestID, convID, in.Operation, in.Risk, in.Prompt, in.SiteIDs, slugs, names, in.Brain, in.Model, in.Effort, unlockID)
	if err == platform.ErrPilotRequestConflict {
		pilotAPIError(w, 409, "idempotency_conflict", "同一 request_id 已用于不同任务。")
		return
	}
	if err != nil {
		pilotAPIError(w, 500, "task_create_failed", err.Error())
		return
	}
	sess, _ := s.currentSession(r)
	auditSiteID := int64(0)
	if len(in.SiteIDs) > 0 {
		auditSiteID = in.SiteIDs[0]
	}
	_ = s.platform.PilotAudit(sess.user, b.DeviceID, b.ID, auditSiteID, in.RequestID, "task.created", "远程对话任务已排队")
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"ok": true, "created": created, "task": task})
}

func (s *Server) adminPilotEvents(w http.ResponseWriter, r *http.Request, taskID string) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	events, err := s.platform.ListPilotTaskEvents(taskID, after)
	if err != nil {
		pilotAPIError(w, 500, "events_failed", err.Error())
		return
	}
	task, err := s.platform.GetPilotTask(taskID)
	if err != nil {
		pilotAPIError(w, 404, "task_not_found", "任务不存在。")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "task": task, "events": events})
}
func (s *Server) adminPilotCancel(w http.ResponseWriter, r *http.Request, id string) {
	task, _ := s.platform.GetPilotTask(id)
	if err := s.platform.RequestPilotTaskCancel(id); err != nil {
		pilotAPIError(w, 409, "cancel_invalid", err.Error())
		return
	}
	if task != nil {
		sess, _ := s.currentSession(r)
		_ = s.platform.PilotAudit(sess.user, "", task.BindingID, 0, task.RequestID, "task.cancel_requested", "用户请求取消远程任务")
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) adminPilotRetry(w http.ResponseWriter, r *http.Request, id string) {
	old, err := s.platform.GetPilotTask(id)
	if err != nil {
		pilotAPIError(w, 404, "task_not_found", "任务不存在。")
		return
	}
	var in struct{ RequestID, UnlockToken string }
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in)
	if in.RequestID == "" {
		in.RequestID = pilotRandom("req_")
	}
	unlockID := ""
	if old.Risk == "sensitive" || old.Risk == "destructive" {
		var siteIDs []int64
		_ = json.Unmarshal([]byte(old.SiteIDsJSON), &siteIDs)
		if len(siteIDs) != 1 {
			pilotAPIError(w, http.StatusConflict, "unlock_site_required", "敏感重试必须绑定一个目标站点。")
			return
		}
		binding, bindErr := s.platform.GetPilotBinding(old.BindingID)
		if bindErr != nil {
			pilotAPIError(w, http.StatusConflict, "binding_not_found", "绑定已失效。")
			return
		}
		unlockID, err = s.platform.ConsumePilotUnlock(in.UnlockToken, binding.DeviceID, binding.ID, siteIDs[0], old.Operation, in.RequestID)
		if err != nil {
			pilotAPIError(w, http.StatusForbidden, "unlock_invalid_or_expired", "敏感任务重试需要重新输入后台密码。")
			return
		}
	}
	task, err := s.platform.RetryPilotTask(id, pilotRandom("pt_"), in.RequestID, pilotRandom("pc_"), unlockID)
	if err != nil {
		pilotAPIError(w, 409, "retry_invalid", err.Error())
		return
	}
	sess, _ := s.currentSession(r)
	_ = s.platform.PilotAudit(sess.user, "", task.BindingID, 0, task.RequestID, "task.retried", "用户显式重试远程任务")
	writeJSON(w, 201, map[string]any{"ok": true, "task": task})
}

func (s *Server) adminPilotConfirm(w http.ResponseWriter, r *http.Request, id string) {
	var in struct{ Allow bool }
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in) != nil {
		pilotAPIError(w, http.StatusBadRequest, "invalid_json", "确认决定格式无效。")
		return
	}
	task, err := s.platform.DecidePilotTaskConfirmation(id, in.Allow)
	if err != nil {
		pilotAPIError(w, http.StatusConflict, "confirmation_not_pending", "任务当前没有可处理的确认请求。")
		return
	}
	sess, _ := s.currentSession(r)
	action := "task.confirmation_denied"
	message := "网页拒绝 Pilot 工具操作"
	if in.Allow {
		action = "task.confirmation_allowed"
		message = "网页允许 Pilot 工具操作"
	}
	_ = s.platform.PilotAudit(sess.user, "", task.BindingID, 0, task.RequestID, action, message)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": task})
}

func (s *Server) adminPilotUnlock(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password, BindingID, Operation, RequestID string
		SiteID                                    int64
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in) != nil {
		pilotAPIError(w, 400, "invalid_json", "解锁格式无效。")
		return
	}
	sess, _ := s.currentSession(r)
	_, hash := s.adminCredentials()
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil {
		_ = s.platform.PilotAudit(sess.user, "", in.BindingID, in.SiteID, in.RequestID, "unlock.denied", "后台密码校验失败")
		pilotAPIError(w, 403, "password_invalid", "后台密码不正确。")
		return
	}
	b, err := s.platform.GetPilotBinding(in.BindingID)
	if err != nil || !s.pilotCredentialAllowsSite(b.CredentialKind, b.CredentialID, b.CredentialSiteID, in.SiteID) {
		pilotAPIError(w, 403, "site_forbidden", "绑定或站点授权无效。")
		return
	}
	id, token := pilotRandom("pu_"), pilotRandom("gcmsu_")
	expires := time.Now().Add(pilotUnlockTTL)
	if err = s.platform.CreatePilotUnlock(id, token, sess.user, b.DeviceID, b.ID, in.SiteID, in.Operation, in.RequestID, expires); err != nil {
		pilotAPIError(w, 500, "unlock_failed", err.Error())
		return
	}
	_ = s.platform.PilotAudit(sess.user, b.DeviceID, b.ID, in.SiteID, in.RequestID, "unlock.issued", "已签发一次性短时解锁")
	writeJSON(w, 201, map[string]any{"ok": true, "unlock_token": token, "expires_at": expires})
}

func (s *Server) adminPilotDefault(w http.ResponseWriter, r *http.Request, id string) {
	var in struct{ SiteID int64 }
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		pilotAPIError(w, 400, "invalid_json", "站点格式无效。")
		return
	}
	b, err := s.platform.GetPilotBinding(id)
	if err != nil {
		pilotAPIError(w, 404, "binding_not_found", "绑定不存在。")
		return
	}
	if in.SiteID > 0 && !s.pilotCredentialAllowsSite(b.CredentialKind, b.CredentialID, b.CredentialSiteID, in.SiteID) {
		pilotAPIError(w, 403, "site_forbidden", "默认站点不在授权范围。")
		return
	}
	if err = s.platform.UpdatePilotDefaultSite(id, in.SiteID); err != nil {
		pilotAPIError(w, 409, "default_update_failed", err.Error())
		return
	}
	sess, _ := s.currentSession(r)
	_ = s.platform.PilotAudit(sess.user, b.DeviceID, id, in.SiteID, "", "binding.default_site_updated", "GCMS 网页更新新对话默认站点")
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) adminPilotUnbind(w http.ResponseWriter, r *http.Request, id string) {
	b, err := s.platform.GetPilotBinding(id)
	if err != nil {
		pilotAPIError(w, 404, "binding_not_found", "绑定不存在。")
		return
	}
	if err = s.platform.RevokePilotBinding(id); err != nil {
		pilotAPIError(w, 409, "unbind_failed", err.Error())
		return
	}
	sess, _ := s.currentSession(r)
	_ = s.platform.PilotAudit(sess.user, b.DeviceID, id, 0, "", "binding.revoked_by_admin", "GCMS 网页解除绑定")
	writeJSON(w, 200, map[string]any{"ok": true})
}
