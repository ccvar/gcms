package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cms.ccvar.com/internal/platform"
	"golang.org/x/crypto/bcrypt"
)

const (
	controlUnlockHeader = "X-GCMS-Control-Unlock"
	controlUnlockTTL    = 5 * time.Minute
	controlMaxOps       = 16
)

// platformControlOperation 是 Pilot/技能包的平台控制能力契约。
// Available 只表示当前服务端是否已接入该操作；未接入的能力先公开权限与
// 风险模型，但客户端不得把它当作已可执行的 API。
type platformControlOperation struct {
	ID                   string `json:"id"`
	Label                string `json:"label"`
	RequiredScope        string `json:"required_scope"`
	Risk                 string `json:"risk"`
	Method               string `json:"method"`
	Endpoint             string `json:"endpoint"`
	RequiresConfirmation bool   `json:"requires_confirmation"`
	RequiresUnlock       bool   `json:"requires_unlock"`
	SupportsDryRun       bool   `json:"supports_dry_run"`
	UIOnly               bool   `json:"ui_only"`
	Available            bool   `json:"available"`
	Granted              bool   `json:"granted"`
	UnavailableReason    string `json:"unavailable_reason,omitempty"`
}

func platformControlCatalog() []platformControlOperation {
	return []platformControlOperation{
		{ID: "control.capabilities", Label: "读取控制能力", RequiredScope: apiScopeControlRead, Risk: "read", Method: http.MethodGet, Endpoint: "/control/capabilities", Available: true},
		{ID: "control.openapi", Label: "读取控制接口契约", RequiredScope: apiScopeControlRead, Risk: "read", Method: http.MethodGet, Endpoint: "/control/openapi.json", Available: true},
		{ID: "sites.list", Label: "读取站点列表", RequiredScope: apiScopeControlRead, Risk: "read", Method: http.MethodGet, Endpoint: "/control/sites", Available: true},
		{ID: "sites.get", Label: "读取站点详情", RequiredScope: apiScopeControlRead, Risk: "read", Method: http.MethodGet, Endpoint: "/control/sites/{site_id}", Available: true},
		{ID: "sites.create", Label: "创建站点", RequiredScope: apiScopeSitesCreate, Risk: "write", Method: http.MethodPost, Endpoint: "/control/sites", RequiresConfirmation: true, SupportsDryRun: true, Available: true},
		{ID: "sites.update", Label: "修改站点", RequiredScope: apiScopeSitesUpdate, Risk: "write", Method: http.MethodPatch, Endpoint: "/control/sites/{site_id}", RequiresConfirmation: true, SupportsDryRun: true, Available: true},
		{ID: "sites.delete", Label: "归档删除站点", RequiredScope: apiScopeSitesDelete, Risk: "destructive", Method: http.MethodDelete, Endpoint: "/control/sites/{site_id}", RequiresConfirmation: true, RequiresUnlock: true, SupportsDryRun: true, Available: true},
		{ID: "themes.list", Label: "读取外观主题", RequiredScope: apiScopeThemesRead, Risk: "read", Method: http.MethodGet, Endpoint: "/control/themes", Available: true},
		{ID: "themes.get", Label: "读取主题详情", RequiredScope: apiScopeThemesRead, Risk: "read", Method: http.MethodGet, Endpoint: "/control/themes/{theme_id}", Available: true},
		{ID: "themes.current", Label: "读取站点当前主题", RequiredScope: apiScopeThemesRead, Risk: "read", Method: http.MethodGet, Endpoint: "/control/sites/{site_id}/theme", Available: true},
		{ID: "themes.apply", Label: "应用或恢复外观主题", RequiredScope: apiScopeThemesApply, Risk: "write", Method: http.MethodPut, Endpoint: "/control/sites/{site_id}/theme", RequiresConfirmation: true, SupportsDryRun: true, Available: true},
		{ID: "domains.read", Label: "读取域名配置", RequiredScope: apiScopeDomainsRead, Risk: "read", Method: http.MethodGet, Endpoint: "/control/sites/{site_id}/domains", Available: true},
		{ID: "domains.apply", Label: "修改 GCMS 域名配置", RequiredScope: apiScopeDomainsWrite, Risk: "sensitive", Method: http.MethodPut, Endpoint: "/control/sites/{site_id}/domains", RequiresConfirmation: true, RequiresUnlock: true, SupportsDryRun: true, Available: true},
		{ID: "security.status", Label: "读取后台安全状态", RequiredScope: apiScopeControlRead, Risk: "read", Method: http.MethodGet, Endpoint: "/control/security", Available: true},
	}
}

func platformControlScopes() []string {
	return []string{
		apiScopeControlRead,
		apiScopeControlUnlock,
		apiScopeSitesCreate,
		apiScopeSitesUpdate,
		apiScopeSitesDelete,
		apiScopeThemesRead,
		apiScopeThemesApply,
		apiScopeDomainsRead,
		apiScopeDomainsWrite,
	}
}

func platformControlOperationByID(id string) (platformControlOperation, bool) {
	id = strings.TrimSpace(id)
	for _, op := range platformControlCatalog() {
		if op.ID == id {
			return op, true
		}
	}
	return platformControlOperation{}, false
}

type controlGrant struct {
	keyID              int64
	operations         map[string]bool
	credentialRevision [32]byte
	expiresAt          time.Time
}

// controlGrantStore 只保存短时授权的哈希，不持久化。进程重启、平台密钥失效或
// 后台密码变更都会使授权不可继续使用。
type controlGrantStore struct {
	mu     sync.Mutex
	grants map[[32]byte]controlGrant
}

func newControlGrantStore() *controlGrantStore {
	return &controlGrantStore{grants: make(map[[32]byte]controlGrant)}
}

func controlCredentialRevision(hash string) [32]byte {
	return sha256.Sum256([]byte(hash))
}

func (s *controlGrantStore) issue(keyID int64, operations []string, credentialRevision [32]byte, now time.Time) (string, time.Time, error) {
	if s == nil || keyID <= 0 || len(operations) == 0 {
		return "", time.Time{}, fmt.Errorf("无法创建短时授权")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := "gcmsu_" + base64.RawURLEncoding.EncodeToString(raw)
	tokenHash := sha256.Sum256([]byte(token))
	opSet := make(map[string]bool, len(operations))
	for _, op := range operations {
		opSet[op] = true
	}
	expiresAt := now.Add(controlUnlockTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	s.grants[tokenHash] = controlGrant{keyID: keyID, operations: opSet, credentialRevision: credentialRevision, expiresAt: expiresAt}
	return token, expiresAt, nil
}

func (s *controlGrantStore) valid(keyID int64, token, operation string, credentialRevision [32]byte, now time.Time) bool {
	if s == nil || keyID <= 0 || strings.TrimSpace(token) == "" {
		return false
	}
	tokenHash := sha256.Sum256([]byte(strings.TrimSpace(token)))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	grant, ok := s.grants[tokenHash]
	if !ok || grant.keyID != keyID || grant.credentialRevision != credentialRevision || !grant.operations[operation] {
		return false
	}
	return now.Before(grant.expiresAt)
}

func (s *controlGrantStore) revoke(keyID int64, token string) bool {
	if s == nil || keyID <= 0 || strings.TrimSpace(token) == "" {
		return false
	}
	tokenHash := sha256.Sum256([]byte(strings.TrimSpace(token)))
	s.mu.Lock()
	defer s.mu.Unlock()
	grant, ok := s.grants[tokenHash]
	if !ok || grant.keyID != keyID {
		return false
	}
	delete(s.grants, tokenHash)
	return true
}

func (s *controlGrantStore) pruneLocked(now time.Time) {
	for tokenHash, grant := range s.grants {
		if !now.Before(grant.expiresAt) {
			delete(s.grants, tokenHash)
		}
	}
}

func (s *Server) servePlatformControl(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch path {
	case "/api/platform/v1/control/capabilities", "/api/platform/v1/control/capabilities/":
		s.servePlatformControlCapabilities(w, r)
	case "/api/platform/v1/control/openapi.json":
		s.servePlatformControlOpenAPI(w, r)
	case "/api/platform/v1/control/unlock", "/api/platform/v1/control/unlock/":
		s.servePlatformControlUnlock(w, r)
	case "/api/platform/v1/control/sites":
		s.servePlatformControlSites(w, r)
	case "/api/platform/v1/control/themes":
		s.servePlatformControlThemes(w, r, "")
	case "/api/platform/v1/control/security":
		s.servePlatformControlSecurity(w, r)
	default:
		const prefix = "/api/platform/v1/control/"
		rest := strings.TrimPrefix(path, prefix)
		parts := strings.Split(rest, "/")
		switch {
		case len(parts) == 2 && parts[0] == "themes" && strings.TrimSpace(parts[1]) != "":
			s.servePlatformControlThemes(w, r, parts[1])
		case len(parts) >= 2 && parts[0] == "sites":
			siteID, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil || siteID <= 0 {
				http.NotFound(w, r)
				return
			}
			switch {
			case len(parts) == 2:
				s.servePlatformControlSite(w, r, siteID)
			case len(parts) == 3 && parts[2] == "theme":
				s.servePlatformControlSiteTheme(w, r, siteID)
			case len(parts) == 3 && parts[2] == "domains":
				s.servePlatformControlSiteDomains(w, r, siteID)
			default:
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	}
}

func (s *Server) requirePlatformControlKey(w http.ResponseWriter, r *http.Request, scope string) (*platform.PlatformAutomationKey, bool) {
	if s.platform == nil {
		apiError(w, http.StatusNotFound, "platform_api_disabled", "未启用平台模式。")
		return nil, false
	}
	token := apiTokenFromRequest(r)
	if !s.checkAPIRateLimit(w, r, token) {
		return nil, false
	}
	if token == "" {
		apiError(w, http.StatusUnauthorized, "missing_token", "缺少访问密钥。")
		return nil, false
	}
	key, ok, err := s.platform.GetPlatformKeyByToken(token)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "auth_error", err.Error())
		return nil, false
	}
	if !ok {
		apiError(w, http.StatusUnauthorized, "invalid_token", "访问密钥无效、已过期或不是平台密钥。")
		return nil, false
	}
	if s.platformAutomationKilled() {
		apiError(w, http.StatusForbidden, "platform_automation_disabled", "平台自动化已被全局关闭。")
		return nil, false
	}
	if !apiScopeMap(key.Scopes)[scope] {
		apiError(w, http.StatusForbidden, "missing_scope", "访问权限不足，需要 "+scope+"。")
		return nil, false
	}
	_ = s.platform.TouchPlatformKey(key.ID)
	return key, true
}

func (s *Server) servePlatformControlCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET。")
		return
	}
	key, ok := s.requirePlatformControlKey(w, r, apiScopeControlRead)
	if !ok {
		return
	}
	scopes := apiScopeMap(key.Scopes)
	operations := platformControlCatalog()
	for i := range operations {
		operations[i].Granted = scopes[operations[i].RequiredScope]
	}
	_, adminHash := s.adminCredentials()
	passwordReady := strings.TrimSpace(adminHash) != "" && !s.adminPasswordIsDefault()
	writeJSON(w, http.StatusOK, map[string]any{
		"api_version": "v1",
		"phase":       "control-v1",
		"key": map[string]any{
			"name":            key.Name,
			"membership_mode": key.MembershipMode,
		},
		"operations": operations,
		"mutation_protocol": map[string]any{
			"confirm_header":     controlConfirmHeader,
			"idempotency_header": controlIdempotencyHeader,
			"dry_run_query":      "dry_run=1",
		},
		"unlock": map[string]any{
			"available":           scopes[apiScopeControlUnlock] && passwordReady,
			"ui_only":             true,
			"required_scope":      apiScopeControlUnlock,
			"ttl_seconds":         int(controlUnlockTTL / time.Second),
			"header":              controlUnlockHeader,
			"password_ready":      passwordReady,
			"password_handled_by": "pilot_ui",
		},
	})
}

func platformControlTransportAllowed(r *http.Request) bool {
	if r == nil {
		return false
	}
	// 直接 TLS，或本机/本机反代入口。公网 HTTP 即使伪造 X-Forwarded-Proto 也不放行。
	return r.TLS != nil || remoteIsLoopback(r.RemoteAddr)
}

func (s *Server) servePlatformControlUnlock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST 或 DELETE。")
		return
	}
	if !platformControlTransportAllowed(r) {
		w.Header().Set("Upgrade", "TLS/1.2")
		apiError(w, http.StatusUpgradeRequired, "https_required", "高风险操作授权只能通过 HTTPS 或本机连接发起。")
		return
	}
	key, ok := s.requirePlatformControlKey(w, r, apiScopeControlUnlock)
	if !ok {
		return
	}
	if r.Method == http.MethodDelete {
		token := strings.TrimSpace(r.Header.Get(controlUnlockHeader))
		if token == "" {
			apiError(w, http.StatusBadRequest, "missing_unlock_token", "缺少短时授权令牌。")
			return
		}
		revoked := s.controlGrants != nil && s.controlGrants.revoke(key.ID, token)
		if revoked {
			_ = s.platform.CreatePlatformAutomationLog(key.ID, 0, "control_unlock_revoke", "control", 0, "已主动结束短时高风险操作授权")
		}
		writeJSON(w, http.StatusOK, map[string]bool{"revoked": revoked})
		return
	}
	if strings.ToLower(strings.TrimSpace(r.Header.Get(controlUIRequestHeader))) != controlUIPilotValue {
		apiError(w, http.StatusForbidden, "pilot_ui_required", "后台密码只能由 Pilot 原生界面提交。")
		return
	}

	if s.adminPasswordIsDefault() {
		_ = s.platform.CreatePlatformAutomationLog(key.ID, 0, "control_unlock_denied", "control", 0, "短时授权被拒绝：后台仍使用默认密码")
		apiError(w, http.StatusPreconditionRequired, "default_password", "请先修改 GCMS 后台默认密码，再授权高风险操作。")
		return
	}
	var in struct {
		Password   string   `json:"password"`
		Operations []string `json:"operations"`
	}
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	if in.Password == "" {
		apiError(w, http.StatusBadRequest, "password_required", "请输入 GCMS 后台密码。")
		return
	}
	operations, errCode, errMessage := validateControlUnlockOperations(apiScopeMap(key.Scopes), in.Operations)
	if errCode != "" {
		status := http.StatusBadRequest
		if errCode == "missing_operation_scope" {
			status = http.StatusForbidden
		}
		apiError(w, status, errCode, errMessage)
		return
	}

	limitKey := "control-unlock:" + strconv.FormatInt(key.ID, 10) + ":" + clientIP(r)
	if s.login != nil {
		if wait := s.login.lockedFor(limitKey); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			apiError(w, http.StatusTooManyRequests, "unlock_locked", "密码尝试过多，请稍后再试。")
			return
		}
	}
	_, hash := s.adminCredentials()
	if strings.TrimSpace(hash) == "" {
		apiError(w, http.StatusServiceUnavailable, "admin_credentials_unavailable", "后台凭据尚未配置。")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil {
		if s.login != nil {
			s.login.fail(limitKey)
		}
		_ = s.platform.CreatePlatformAutomationLog(key.ID, 0, "control_unlock_denied", "control", 0, "短时授权失败：后台密码校验未通过")
		apiError(w, http.StatusUnauthorized, "invalid_admin_password", "GCMS 后台密码不正确。")
		return
	}
	if s.login != nil {
		s.login.reset(limitKey)
	}
	if s.controlGrants == nil {
		apiError(w, http.StatusServiceUnavailable, "unlock_unavailable", "短时授权服务尚未就绪。")
		return
	}
	token, expiresAt, err := s.controlGrants.issue(key.ID, operations, controlCredentialRevision(hash), time.Now())
	if err != nil {
		apiError(w, http.StatusInternalServerError, "unlock_error", "无法创建短时授权。")
		return
	}
	_ = s.platform.CreatePlatformAutomationLog(key.ID, 0, "control_unlock_issue", "control", 0, "已授权高风险操作："+strings.Join(operations, ", "))
	writeJSON(w, http.StatusCreated, map[string]any{
		"unlock_token": token,
		"expires_at":   expiresAt.UTC().Format(time.RFC3339),
		"ttl_seconds":  int(controlUnlockTTL / time.Second),
		"operations":   operations,
	})
}

func validateControlUnlockOperations(scopes map[string]bool, requested []string) ([]string, string, string) {
	if len(requested) == 0 {
		return nil, "operations_required", "请指定需要授权的高风险操作。"
	}
	if len(requested) > controlMaxOps {
		return nil, "too_many_operations", "一次授权的操作过多。"
	}
	seen := make(map[string]bool, len(requested))
	operations := make([]string, 0, len(requested))
	for _, id := range requested {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		op, ok := platformControlOperationByID(id)
		if !ok {
			return nil, "unknown_operation", "未知的高风险操作：" + id
		}
		if !op.RequiresUnlock {
			return nil, "unlock_not_required", "该操作不需要密码解锁：" + id
		}
		if !scopes[op.RequiredScope] {
			return nil, "missing_operation_scope", "当前平台密钥无权执行：" + id
		}
		seen[id] = true
		operations = append(operations, id)
	}
	if len(operations) == 0 {
		return nil, "operations_required", "请指定需要授权的高风险操作。"
	}
	sort.Strings(operations)
	return operations, "", ""
}

// requireControlUnlock 供后续高风险操作端点复用：令牌必须与平台密钥、具体操作和
// 当前后台密码版本同时匹配。
func (s *Server) requireControlUnlock(w http.ResponseWriter, r *http.Request, key *platform.PlatformAutomationKey, operation string) bool {
	if key == nil {
		apiError(w, http.StatusUnauthorized, "invalid_token", "平台密钥无效。")
		return false
	}
	op, ok := platformControlOperationByID(operation)
	if !ok || !op.RequiresUnlock {
		apiError(w, http.StatusInternalServerError, "control_contract_error", "高风险操作契约无效。")
		return false
	}
	if !apiScopeMap(key.Scopes)[op.RequiredScope] {
		apiError(w, http.StatusForbidden, "missing_scope", "访问权限不足，需要 "+op.RequiredScope+"。")
		return false
	}
	_, hash := s.adminCredentials()
	token := strings.TrimSpace(r.Header.Get(controlUnlockHeader))
	if token == "" || hash == "" || s.controlGrants == nil || !s.controlGrants.valid(key.ID, token, operation, controlCredentialRevision(hash), time.Now()) {
		apiError(w, http.StatusForbidden, "unlock_required", "该操作需要在 Pilot 中输入后台密码重新授权。")
		return false
	}
	return true
}
