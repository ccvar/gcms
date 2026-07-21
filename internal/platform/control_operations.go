package platform

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ControlOperationRunning   = "running"
	ControlOperationCompleted = "completed"
)

// ControlOperationLeaseTTL 是 running 收据在没有完成或释放时的保守租约。
// 进程崩溃留下的收据超过该时间后，下一次请求可以原子接管。
const ControlOperationLeaseTTL = 10 * time.Minute

// ControlOperationReservation 描述一次幂等预占的结果。只有 Reserved 的调用方
// 可以执行真实写操作；其余结果必须等待、重放或报告冲突。
type ControlOperationReservation string

const (
	ControlOperationReserved   ControlOperationReservation = "reserved"
	ControlOperationReplay     ControlOperationReservation = "replay"
	ControlOperationInProgress ControlOperationReservation = "in_progress"
	ControlOperationConflict   ControlOperationReservation = "conflict"
)

var ErrControlOperationNotReserved = errors.New("control operation is not reserved")

// ControlOperationReceipt 是平台控制写操作的持久幂等收据。RequestHash 只能是
// 调用方对稳定请求指纹计算出的 SHA-256；这里不保存原始请求或后台密码。
type ControlOperationReceipt struct {
	KeyID          int64
	Operation      string
	IdempotencyKey string
	RequestHash    string
	State          string
	HTTPStatus     int
	ResponseJSON   []byte
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CompletedAt    time.Time
}

func validControlRequestHash(hash string) bool {
	hash = strings.TrimSpace(hash)
	if len(hash) != 64 {
		return false
	}
	raw, err := hex.DecodeString(hash)
	return err == nil && len(raw) == 32
}

func validateControlReceiptIdentity(keyID int64, operation, idempotencyKey, requestHash string) error {
	if keyID <= 0 {
		return fmt.Errorf("invalid platform key id")
	}
	if strings.TrimSpace(operation) == "" {
		return fmt.Errorf("control operation is required")
	}
	if idempotencyKey == "" {
		return fmt.Errorf("idempotency key is required")
	}
	if !validControlRequestHash(requestHash) {
		return fmt.Errorf("request hash must be a SHA-256 hex digest")
	}
	return nil
}

func scanControlOperationReceipt(row interface{ Scan(...any) error }) (*ControlOperationReceipt, error) {
	var receipt ControlOperationReceipt
	var response string
	var createdAt, updatedAt string
	var completedAt sql.NullString
	if err := row.Scan(
		&receipt.KeyID,
		&receipt.Operation,
		&receipt.IdempotencyKey,
		&receipt.RequestHash,
		&receipt.State,
		&receipt.HTTPStatus,
		&response,
		&createdAt,
		&updatedAt,
		&completedAt,
	); err != nil {
		return nil, err
	}
	receipt.ResponseJSON = []byte(response)
	receipt.CreatedAt = parseTime(createdAt)
	receipt.UpdatedAt = parseTime(updatedAt)
	receipt.CompletedAt = parseTime(completedAt.String)
	return &receipt, nil
}

const controlOperationReceiptCols = `key_id,operation,idempotency_key,request_hash,state,http_status,response_json,created_at,updated_at,completed_at`

// ReserveControlOperation 原子预占一个幂等键。相同请求已完成时返回 Replay，
// 执行中返回 InProgress；同一键对应不同请求指纹时返回 Conflict。
// running 收据超过 ControlOperationLeaseTTL 后视为进程遗留，但只有相同请求
// 可以原子续租；幂等键一旦绑定请求指纹，就不能被不同请求重新解释。
func (s *Store) ReserveControlOperation(keyID int64, operation, idempotencyKey, requestHash string) (*ControlOperationReceipt, ControlOperationReservation, error) {
	if s == nil {
		return nil, "", sql.ErrConnDone
	}
	operation = strings.TrimSpace(operation)
	requestHash = strings.ToLower(strings.TrimSpace(requestHash))
	if err := validateControlReceiptIdentity(keyID, operation, idempotencyKey, requestHash); err != nil {
		return nil, "", err
	}

	nowTime := time.Now().UTC()
	now := fmtTime(nowTime)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO platform_control_operation_receipts(
		key_id,operation,idempotency_key,request_hash,state,http_status,response_json,created_at,updated_at
	) VALUES(?,?,?,?,?,0,'',?,?) ON CONFLICT(key_id,operation,idempotency_key) DO NOTHING`,
		keyID, operation, idempotencyKey, requestHash, ControlOperationRunning, now, now)
	if err != nil {
		return nil, "", err
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return nil, "", err
	}
	receipt, err := scanControlOperationReceipt(tx.QueryRow(`SELECT `+controlOperationReceiptCols+`
		FROM platform_control_operation_receipts WHERE key_id=? AND operation=? AND idempotency_key=?`,
		keyID, operation, idempotencyKey))
	if err != nil {
		return nil, "", err
	}
	if inserted == 1 {
		if err := tx.Commit(); err != nil {
			return nil, "", err
		}
		return receipt, ControlOperationReserved, nil
	}
	// 已完成收据永久保持原请求归属；只有 running 收据可以过期接管。
	staleBefore := nowTime.Add(-ControlOperationLeaseTTL)
	if receipt.State == ControlOperationRunning && receipt.RequestHash == requestHash && (receipt.UpdatedAt.IsZero() || !receipt.UpdatedAt.After(staleBefore)) {
		res, err := tx.Exec(`UPDATE platform_control_operation_receipts
			SET state=?,http_status=0,response_json='',updated_at=?,completed_at=NULL
			WHERE key_id=? AND operation=? AND idempotency_key=? AND request_hash=? AND state=? AND updated_at<=?`,
			ControlOperationRunning, now,
			keyID, operation, idempotencyKey, requestHash, ControlOperationRunning, fmtTime(staleBefore))
		if err != nil {
			return nil, "", err
		}
		updated, err := res.RowsAffected()
		if err != nil {
			return nil, "", err
		}
		if updated == 1 {
			receipt, err = scanControlOperationReceipt(tx.QueryRow(`SELECT `+controlOperationReceiptCols+`
				FROM platform_control_operation_receipts WHERE key_id=? AND operation=? AND idempotency_key=?`,
				keyID, operation, idempotencyKey))
			if err != nil {
				return nil, "", err
			}
			if err := tx.Commit(); err != nil {
				return nil, "", err
			}
			return receipt, ControlOperationReserved, nil
		}
		// 极少数并发数据库实现可能在条件更新前丢失竞争；重新读取获胜者。
		receipt, err = scanControlOperationReceipt(tx.QueryRow(`SELECT `+controlOperationReceiptCols+`
			FROM platform_control_operation_receipts WHERE key_id=? AND operation=? AND idempotency_key=?`,
			keyID, operation, idempotencyKey))
		if err != nil {
			return nil, "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	if receipt.RequestHash != requestHash {
		return receipt, ControlOperationConflict, nil
	}
	if receipt.State == ControlOperationCompleted {
		return receipt, ControlOperationReplay, nil
	}
	return receipt, ControlOperationInProgress, nil
}

// CompleteControlOperation 固化成功写操作的 HTTP 状态和 JSON 响应，供后续请求
// 原样重放。只有仍处于 running 且请求哈希一致的预占可以完成。
func (s *Store) CompleteControlOperation(keyID int64, operation, idempotencyKey, requestHash string, httpStatus int, responseJSON []byte) error {
	if s == nil {
		return sql.ErrConnDone
	}
	operation = strings.TrimSpace(operation)
	requestHash = strings.ToLower(strings.TrimSpace(requestHash))
	if err := validateControlReceiptIdentity(keyID, operation, idempotencyKey, requestHash); err != nil {
		return err
	}
	if httpStatus < 100 || httpStatus > 599 {
		return fmt.Errorf("invalid HTTP status %d", httpStatus)
	}
	if len(responseJSON) == 0 || !json.Valid(responseJSON) {
		return fmt.Errorf("control operation response must be valid JSON")
	}
	now := fmtTime(time.Now())
	res, err := s.db.Exec(`UPDATE platform_control_operation_receipts
		SET state=?,http_status=?,response_json=?,updated_at=?,completed_at=?
		WHERE key_id=? AND operation=? AND idempotency_key=? AND request_hash=? AND state=?`,
		ControlOperationCompleted, httpStatus, string(responseJSON), now, now,
		keyID, operation, idempotencyKey, requestHash, ControlOperationRunning)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrControlOperationNotReserved
	}
	return nil
}

// ReleaseControlOperationReservation 撤销尚未完成的预占，使业务失败后的同一请求
// 可以安全重试。已完成的收据不会被删除。
func (s *Store) ReleaseControlOperationReservation(keyID int64, operation, idempotencyKey, requestHash string) error {
	if s == nil {
		return sql.ErrConnDone
	}
	operation = strings.TrimSpace(operation)
	requestHash = strings.ToLower(strings.TrimSpace(requestHash))
	if err := validateControlReceiptIdentity(keyID, operation, idempotencyKey, requestHash); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM platform_control_operation_receipts
		WHERE key_id=? AND operation=? AND idempotency_key=? AND request_hash=? AND state=?`,
		keyID, operation, idempotencyKey, requestHash, ControlOperationRunning)
	return err
}

// GetControlOperationReceipt 读取一条收据，主要供控制处理器诊断和测试使用。
func (s *Store) GetControlOperationReceipt(keyID int64, operation, idempotencyKey string) (*ControlOperationReceipt, bool, error) {
	if s == nil || keyID <= 0 {
		return nil, false, nil
	}
	receipt, err := scanControlOperationReceipt(s.db.QueryRow(`SELECT `+controlOperationReceiptCols+`
		FROM platform_control_operation_receipts WHERE key_id=? AND operation=? AND idempotency_key=?`,
		keyID, strings.TrimSpace(operation), idempotencyKey))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return receipt, true, nil
}
