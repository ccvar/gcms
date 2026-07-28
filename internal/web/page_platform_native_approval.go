package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"cms.ccvar.com/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const (
	pageNativeChallengeTTL = 3 * time.Minute
	pageNativeUnlockTTL    = 5 * time.Minute
)

// pageNativeChallenge is created only after a concrete publish/rollback
// request reaches the server. The opaque challenge lets Pilot's native UI
// confirm that exact target without exposing an admin password or approval
// secret to the model/tool process.
type pageNativeChallenge struct {
	server    *Server
	subject   string
	target    pageApprovalConsumeInput
	expiresAt time.Time
	used      bool
}

type pageNativeUnlock struct {
	subject            string
	target             pageApprovalConsumeInput
	approvalToken      string
	credentialRevision [32]byte
	expiresAt          time.Time
}

type pageNativeApprovalRegistry struct {
	mu         sync.Mutex
	challenges map[[32]byte]*pageNativeChallenge
	unlocks    map[[32]byte]*pageNativeUnlock
}

var pageNativeApprovalRegistries sync.Map // map[*Server]*pageNativeApprovalRegistry

func pageNativeRoot(s *Server) *Server {
	if s == nil {
		return nil
	}
	for s.rootServer != nil {
		s = s.rootServer
	}
	return s
}

func pageNativeRegistryFor(s *Server) *pageNativeApprovalRegistry {
	root := pageNativeRoot(s)
	value, _ := pageNativeApprovalRegistries.LoadOrStore(root, &pageNativeApprovalRegistry{
		challenges: map[[32]byte]*pageNativeChallenge{},
		unlocks:    map[[32]byte]*pageNativeUnlock{},
	})
	return value.(*pageNativeApprovalRegistry)
}

func pageNativeRandom(prefix string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func pageNativeActor(auth *automationAuth) string {
	if auth == nil {
		return ""
	}
	return pageAutomationActor(auth)
}

func issueNativePageChallenge(s *Server, auth *automationAuth, target pageApprovalConsumeInput) (string, error) {
	if s == nil || auth == nil || pageNativeActor(auth) == "" ||
		target.ProjectID <= 0 || target.PageID <= 0 || target.RevisionID <= 0 ||
		target.ETag == "" || target.RequestID == "" {
		return "", errors.New("页面确认目标不完整")
	}
	switch target.Operation {
	case pageApprovalPublish, pageApprovalRollback:
		if target.Capability != "" || target.ConfigHash != "" || target.Decision != "" {
			return "", errors.New("页面确认目标包含无关能力字段")
		}
	case pageCapabilityGrant:
		if strings.TrimSpace(target.Capability) == "" ||
			!validCompositionSHA256(target.ConfigHash) ||
			target.Decision != store.PageCapabilityApproved {
			return "", errors.New("能力批准目标不完整")
		}
	default:
		return "", errors.New("不支持的原生确认操作")
	}
	token, err := pageNativeRandom("gcmspc_")
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(token))
	registry := pageNativeRegistryFor(s)
	now := time.Now()
	registry.mu.Lock()
	registry.pruneLocked(now)
	registry.challenges[digest] = &pageNativeChallenge{
		server: s, subject: pageNativeActor(auth), target: target,
		expiresAt: now.Add(pageNativeChallengeTTL),
	}
	registry.mu.Unlock()
	return token, nil
}

func (r *pageNativeApprovalRegistry) pruneLocked(now time.Time) {
	for digest, challenge := range r.challenges {
		if challenge == nil || !now.Before(challenge.expiresAt) {
			delete(r.challenges, digest)
		}
	}
	for digest, unlock := range r.unlocks {
		if unlock == nil || !now.Before(unlock.expiresAt) {
			delete(r.unlocks, digest)
		}
	}
}

// issueNativePageUnlock consumes a server-signed challenge after Pilot's
// native password UI succeeds. The returned token is operation/target/ETag/
// request bound. It contains no page approval secret.
func issueNativePageUnlock(
	s *Server,
	subject, challengeToken, operation string,
	credentialRevision [32]byte,
) (string, time.Time, error) {
	subject = strings.TrimSpace(subject)
	challengeToken = strings.TrimSpace(challengeToken)
	if subject == "" || challengeToken == "" {
		return "", time.Time{}, errors.New("页面确认挑战缺失")
	}
	registry := pageNativeRegistryFor(s)
	digest := sha256.Sum256([]byte(challengeToken))
	now := time.Now()
	registry.mu.Lock()
	registry.pruneLocked(now)
	challenge := registry.challenges[digest]
	if challenge == nil || challenge.used {
		registry.mu.Unlock()
		return "", time.Time{}, errors.New("页面确认挑战无效、过期或已使用")
	}
	if challenge.subject != subject || challenge.target.Operation != operation {
		registry.mu.Unlock()
		return "", time.Time{}, errors.New("页面确认挑战与当前密钥或操作不匹配")
	}
	challenge.used = true
	registry.mu.Unlock()

	var approvalToken string
	var approvalExpiresAt time.Time
	switch challenge.target.Operation {
	case pageApprovalPublish, pageApprovalRollback:
		approval, err := challenge.server.issuePageApprovalToken(
			challenge.target.ProjectID,
			challenge.target.RevisionID,
			challenge.target.Operation,
			"pilot-native:"+subject,
			pageNativeUnlockTTL,
		)
		if err != nil {
			return "", time.Time{}, errors.New("页面目标已变化，请重新执行发布预检")
		}
		// issuePageApprovalToken validates the current page/revision. Verify all
		// remaining binding fields before creating the native grant.
		if approval.SiteID != challenge.target.SiteID ||
			approval.PageID != challenge.target.PageID ||
			approval.ProjectID != challenge.target.ProjectID ||
			approval.RevisionID != challenge.target.RevisionID ||
			approval.Operation != challenge.target.Operation ||
			approval.ETag != challenge.target.ETag ||
			approval.DataSnapshotHash != challenge.target.DataSnapshotHash {
			return "", time.Time{}, errors.New("页面目标已变化，请重新执行发布预检")
		}
		approvalToken = approval.ApprovalToken
		approvalExpiresAt = approval.ExpiresAt
	case pageCapabilityGrant:
		issued, err := challenge.server.issuePageAppCapabilityApprovalToken(
			challenge.target, subject, pageNativeUnlockTTL,
		)
		if err != nil {
			return "", time.Time{}, errors.New("能力申请或页面目标已变化，请重新发起批准")
		}
		approvalToken = issued.Token
		approvalExpiresAt = issued.ExpiresAt
	default:
		return "", time.Time{}, errors.New("不支持的原生确认操作")
	}
	unlockToken, err := pageNativeRandom("gcmsup_")
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := now.Add(pageNativeUnlockTTL)
	if approvalExpiresAt.Before(expiresAt) {
		expiresAt = approvalExpiresAt
	}
	unlockDigest := sha256.Sum256([]byte(unlockToken))
	registry.mu.Lock()
	registry.pruneLocked(now)
	registry.unlocks[unlockDigest] = &pageNativeUnlock{
		subject: subject, target: challenge.target,
		approvalToken: approvalToken, credentialRevision: credentialRevision,
		expiresAt: expiresAt,
	}
	registry.mu.Unlock()
	return unlockToken, expiresAt, nil
}

func resolveNativePageApproval(
	s *Server,
	auth *automationAuth,
	unlockToken string,
	target pageApprovalConsumeInput,
) (string, string) {
	unlockToken = strings.TrimSpace(unlockToken)
	if unlockToken == "" {
		return "", "missing"
	}
	registry := pageNativeRegistryFor(s)
	digest := sha256.Sum256([]byte(unlockToken))
	now := time.Now()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.pruneLocked(now)
	unlock := registry.unlocks[digest]
	if unlock == nil {
		return "", "invalid"
	}
	_, currentHash := s.adminCredentials()
	if strings.TrimSpace(currentHash) == "" ||
		unlock.credentialRevision != controlCredentialRevision(currentHash) {
		delete(registry.unlocks, digest)
		return "", "credential_changed"
	}
	if unlock.subject != pageNativeActor(auth) ||
		!nativePageApprovalTargetsEqual(unlock.target, target) {
		return "", "mismatch"
	}
	return unlock.approvalToken, ""
}

func nativePageApprovalTargetsEqual(a, b pageApprovalConsumeInput) bool {
	return a.SiteID == b.SiteID &&
		a.PageID == b.PageID &&
		a.ProjectID == b.ProjectID &&
		a.RevisionID == b.RevisionID &&
		a.BuildID == b.BuildID &&
		a.Operation == b.Operation &&
		a.ETag == b.ETag &&
		a.RequestID == b.RequestID &&
		a.DataSnapshotHash == b.DataSnapshotHash &&
		a.Capability == b.Capability &&
		a.ConfigHash == b.ConfigHash &&
		a.Decision == b.Decision
}

func revokeNativePageUnlock(s *Server, subject, token string) bool {
	registry := pageNativeRegistryFor(s)
	digest := sha256.Sum256([]byte(strings.TrimSpace(token)))
	registry.mu.Lock()
	defer registry.mu.Unlock()
	unlock := registry.unlocks[digest]
	if unlock == nil || unlock.subject != strings.TrimSpace(subject) {
		return false
	}
	delete(registry.unlocks, digest)
	return true
}

func pageUnlockOperationsOnly(operations []string) bool {
	return len(operations) == 1 &&
		(operations[0] == pageApprovalPublish ||
			operations[0] == pageApprovalRollback ||
			operations[0] == pageCapabilityGrant)
}

// serveSingleControlUnlock supplies the same native confirmation boundary to
// a single-site automation key. It accepts only target-bound page publication
// and capability approval operations; broad platform operations remain
// platform-key-only.
func (s *Server) serveSingleControlUnlock(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST 或 DELETE。")
		return
	}
	if !platformControlTransportAllowed(r) {
		w.Header().Set("Upgrade", "TLS/1.2")
		apiError(w, http.StatusUpgradeRequired, "https_required", "高风险操作授权只能通过 HTTPS 或本机连接发起。")
		return
	}
	auth, ok := s.requireAutomationScope(w, r, apiScopeControlUnlock)
	if !ok {
		return
	}
	subject := pageNativeActor(auth)
	if r.Method == http.MethodDelete {
		token := strings.TrimSpace(r.Header.Get(controlUnlockHeader))
		if token == "" {
			apiError(w, http.StatusBadRequest, "missing_unlock_token", "缺少短时授权令牌。")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{
			"revoked": revokeNativePageUnlock(s, subject, token),
		})
		return
	}
	if strings.ToLower(strings.TrimSpace(r.Header.Get(controlUIRequestHeader))) != controlUIPilotValue {
		apiError(w, http.StatusForbidden, "pilot_ui_required", "后台密码只能由 Pilot 原生界面提交。")
		return
	}
	if s.adminPasswordIsDefault() {
		apiError(w, http.StatusPreconditionRequired, "default_password", "请先修改 GCMS 后台默认密码，再授权高风险操作。")
		return
	}
	var in struct {
		Password      string   `json:"password"`
		Operations    []string `json:"operations"`
		PageChallenge string   `json:"page_challenge"`
	}
	if !decodeAPIJSON(w, r, &in) {
		return
	}
	operations, errCode, errMessage := validateControlUnlockOperations(auth.scopes, in.Operations)
	if errCode != "" {
		status := http.StatusBadRequest
		if errCode == "missing_operation_scope" {
			status = http.StatusForbidden
		}
		apiError(w, status, errCode, errMessage)
		return
	}
	if !pageUnlockOperationsOnly(operations) || strings.TrimSpace(in.PageChallenge) == "" {
		apiError(w, http.StatusBadRequest, "page_challenge_required", "单站短时授权仅支持带 page_challenge 的页面发布、回滚或能力批准。")
		return
	}
	if in.Password == "" {
		apiError(w, http.StatusBadRequest, "password_required", "请输入 GCMS 后台密码。")
		return
	}
	limitKey := "page-control-unlock:" + strconv.FormatInt(auth.key.ID, 10) + ":" + clientIP(r)
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
		apiError(w, http.StatusUnauthorized, "invalid_admin_password", "GCMS 后台密码不正确。")
		return
	}
	if s.login != nil {
		s.login.reset(limitKey)
	}
	token, expiresAt, err := issueNativePageUnlock(
		s, subject, strings.TrimSpace(in.PageChallenge), operations[0],
		controlCredentialRevision(hash),
	)
	if err != nil {
		apiError(w, http.StatusConflict, "unlock_target_changed", err.Error())
		return
	}
	s.recordAutomationLog(auth, "approve", "page_control", 0, "Pilot 原生界面已批准 "+operations[0])
	writeJSON(w, http.StatusCreated, map[string]any{
		"unlock_token": token,
		"expires_at":   expiresAt.UTC().Format(time.RFC3339),
		"ttl_seconds":  int(time.Until(expiresAt).Seconds()),
		"operations":   operations,
	})
}
