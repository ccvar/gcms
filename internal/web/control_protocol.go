package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"cms.ccvar.com/internal/platform"
)

const (
	controlConfirmHeader             = "X-GCMS-Control-Confirm"
	controlIdempotencyHeader         = "Idempotency-Key"
	controlIdempotencyReplayedHeader = "Idempotency-Replayed"
	controlMinIdempotencyKeyLength   = 8
	controlMaxIdempotencyKeyLength   = 128
)

// controlDryRun 报告请求是否只做预检查。dry-run 不创建幂等收据，也不要求
// 确认或短时解锁；业务闭包必须据此保证自身不产生写操作。
func controlDryRun(r *http.Request) bool {
	if r == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("dry_run"))) {
	case "1", "true":
		return true
	default:
		return false
	}
}

func validControlIdempotencyKey(key string) bool {
	if len(key) < controlMinIdempotencyKeyLength || len(key) > controlMaxIdempotencyKeyLength {
		return false
	}
	for _, ch := range key {
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= 'A' && ch <= 'Z':
		case ch >= '0' && ch <= '9':
		case ch == '-', ch == '_', ch == '.', ch == ':':
		default:
			return false
		}
	}
	return true
}

func controlRequestHash(fingerprint string) string {
	sum := sha256.Sum256([]byte(fingerprint))
	return hex.EncodeToString(sum[:])
}

// controlMutationError 是业务处理器可直接返回的、安全且结构化的错误。
// Details 只能放非敏感诊断信息；错误响应不会进入幂等收据。
type controlMutationError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

func (e *controlMutationError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	return e.Code
}

func newControlMutationError(status int, code, message string) *controlMutationError {
	return &controlMutationError{Status: status, Code: code, Message: message}
}

type controlMutationFunc func() (status int, response any, err error)

func writeControlMutationError(w http.ResponseWriter, err error) {
	var mutationErr *controlMutationError
	if candidate, ok := err.(*controlMutationError); ok {
		mutationErr = candidate
	}
	if mutationErr == nil {
		apiError(w, http.StatusInternalServerError, "control_operation_failed", "操作未完成，可以使用相同幂等键重试。")
		return
	}
	status := mutationErr.Status
	if status < 400 || status > 599 {
		status = http.StatusInternalServerError
	}
	code := strings.TrimSpace(mutationErr.Code)
	if code == "" {
		code = "control_operation_failed"
	}
	message := strings.TrimSpace(mutationErr.Message)
	if message == "" {
		message = "操作未完成，可以使用相同幂等键重试。"
	}
	payload := map[string]any{"error": code, "message": message}
	if len(mutationErr.Details) > 0 {
		payload["details"] = mutationErr.Details
	}
	writeJSON(w, status, payload)
}

func writeControlMutationJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
	_, _ = w.Write([]byte("\n"))
}

func runControlMutation(w http.ResponseWriter, fn controlMutationFunc) (body []byte, status int, success, retryable bool) {
	if fn == nil {
		apiError(w, http.StatusInternalServerError, "control_contract_error", "控制操作处理器未配置。")
		return nil, 0, false, true
	}
	status, response, err := fn()
	if err != nil {
		writeControlMutationError(w, err)
		return nil, 0, false, true
	}
	if status == 0 {
		status = http.StatusOK
	}
	if status < 200 || status > 299 {
		apiError(w, http.StatusInternalServerError, "control_contract_error", "控制操作成功响应必须使用 2xx 状态。")
		return nil, 0, false, false
	}
	body, err = json.Marshal(response)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "control_response_error", "无法编码控制操作响应。")
		return nil, 0, false, false
	}
	return body, status, true, false
}

// executeControlMutation 是所有平台控制写端点的统一入口。fingerprint 必须是
// 业务处理器生成的稳定、无密码请求指纹；这里只持久化它的 SHA-256。
func (s *Server) executeControlMutation(w http.ResponseWriter, r *http.Request, key *platform.PlatformAutomationKey, operation, fingerprint string, fn controlMutationFunc) {
	op, ok := platformControlOperationByID(operation)
	if !ok {
		apiError(w, http.StatusBadRequest, "unknown_operation", "未知的控制操作。")
		return
	}
	if key == nil {
		apiError(w, http.StatusUnauthorized, "invalid_token", "平台密钥无效。")
		return
	}
	if !apiScopeMap(key.Scopes)[op.RequiredScope] {
		apiError(w, http.StatusForbidden, "missing_scope", "访问权限不足，需要 "+op.RequiredScope+"。")
		return
	}

	// 预检查由业务闭包根据 controlDryRun(r) 走只读分支，不持久化操作收据。
	if controlDryRun(r) {
		body, status, success, _ := runControlMutation(w, fn)
		if success {
			writeControlMutationJSON(w, status, body)
		}
		return
	}
	if r == nil || r.Header.Get(controlConfirmHeader) != operation {
		apiError(w, http.StatusPreconditionRequired, "confirmation_required", "请明确确认操作："+operation+"。")
		return
	}
	idempotencyKey := r.Header.Get(controlIdempotencyHeader)
	if !validControlIdempotencyKey(idempotencyKey) {
		apiError(w, http.StatusBadRequest, "invalid_idempotency_key", "请提供 8–128 个字符的有效 Idempotency-Key。")
		return
	}
	if strings.TrimSpace(fingerprint) == "" {
		apiError(w, http.StatusInternalServerError, "control_contract_error", "控制操作缺少稳定请求指纹。")
		return
	}
	if op.RequiresUnlock && !s.requireControlUnlock(w, r, key, operation) {
		return
	}
	if s.platform == nil {
		apiError(w, http.StatusServiceUnavailable, "platform_api_disabled", "未启用平台模式。")
		return
	}

	requestHash := controlRequestHash(fingerprint)
	receipt, reservation, err := s.platform.ReserveControlOperation(key.ID, operation, idempotencyKey, requestHash)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "idempotency_error", "无法预占幂等操作。")
		return
	}
	switch reservation {
	case platform.ControlOperationReplay:
		if receipt == nil || receipt.HTTPStatus == 0 || !json.Valid(receipt.ResponseJSON) {
			apiError(w, http.StatusInternalServerError, "idempotency_receipt_invalid", "已完成操作的幂等收据无效。")
			return
		}
		w.Header().Set(controlIdempotencyReplayedHeader, "true")
		writeControlMutationJSON(w, receipt.HTTPStatus, receipt.ResponseJSON)
		return
	case platform.ControlOperationConflict:
		apiError(w, http.StatusConflict, "idempotency_conflict", "这个 Idempotency-Key 已用于不同请求，请更换后重试。")
		return
	case platform.ControlOperationInProgress:
		w.Header().Set("Retry-After", "1")
		apiError(w, http.StatusConflict, "idempotency_in_progress", "相同操作正在处理中，请稍后使用同一幂等键重试。")
		return
	case platform.ControlOperationReserved:
		// 继续执行。
	default:
		apiError(w, http.StatusInternalServerError, "idempotency_error", "无法识别幂等预占状态。")
		return
	}

	// 不同幂等键仍可能同时修改同一个站点。GCMS 是单进程平台服务，统一串行
	// 执行控制层写操作可避免主题快照、域名替换和运行时重载彼此穿插；业务
	// 闭包仍会在拿到锁后重新读取并校验最新状态。
	if s.controlMutation != nil {
		s.controlMutation.Lock()
		defer s.controlMutation.Unlock()
	}

	completed := false
	releaseReservation := false
	defer func() {
		if !completed && releaseReservation {
			_ = s.platform.ReleaseControlOperationReservation(key.ID, operation, idempotencyKey, requestHash)
		}
		if recovered := recover(); recovered != nil {
			panic(recovered)
		}
	}()
	body, status, success, retryable := runControlMutation(w, fn)
	if !success {
		releaseReservation = retryable
		return
	}
	if err := s.platform.CompleteControlOperation(key.ID, operation, idempotencyKey, requestHash, status, body); err != nil {
		apiError(w, http.StatusInternalServerError, "idempotency_commit_failed", "操作结果未能安全保存，请重新检查状态。")
		return
	}
	completed = true
	writeControlMutationJSON(w, status, body)
}
