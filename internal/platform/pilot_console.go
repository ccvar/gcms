package platform

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const PilotProtocolVersion = "1"

var (
	ErrPilotNotFound          = errors.New("pilot resource not found")
	ErrPilotRequestConflict   = errors.New("pilot request_id conflict")
	ErrPilotLeaseLost         = errors.New("pilot task lease lost")
	ErrPilotInvalidTransition = errors.New("invalid pilot task transition")
)

const pilotSchema = `
CREATE TABLE IF NOT EXISTS pilot_devices (
  id TEXT PRIMARY KEY,
  secret_hash TEXT NOT NULL,
  name TEXT NOT NULL,
  pilot_version TEXT NOT NULL DEFAULT '',
  protocol_version TEXT NOT NULL DEFAULT '1',
  platform TEXT NOT NULL DEFAULT '',
  last_seen_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  revoked_at TEXT
);
CREATE TABLE IF NOT EXISTS pilot_bindings (
  id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL REFERENCES pilot_devices(id) ON DELETE CASCADE,
  credential_kind TEXT NOT NULL CHECK(credential_kind IN ('platform','site')),
  credential_id INTEGER NOT NULL,
  credential_site_id INTEGER NOT NULL DEFAULT 0,
  connection_name TEXT NOT NULL DEFAULT '',
  key_prefix TEXT NOT NULL DEFAULT '',
  default_site_id INTEGER,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  revoked_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pilot_bindings_active_connection
ON pilot_bindings(device_id,credential_kind,credential_id,credential_site_id)
WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pilot_bindings_device ON pilot_bindings(device_id,revoked_at);

CREATE TABLE IF NOT EXISTS pilot_tasks (
  id TEXT PRIMARY KEY,
  binding_id TEXT NOT NULL REFERENCES pilot_bindings(id) ON DELETE CASCADE,
  request_id TEXT NOT NULL,
  conversation_id TEXT NOT NULL DEFAULT '',
  operation TEXT NOT NULL DEFAULT 'conversation.create',
  risk TEXT NOT NULL DEFAULT 'write',
  prompt TEXT NOT NULL,
  site_ids_json TEXT NOT NULL DEFAULT '[]',
  site_slugs_json TEXT NOT NULL DEFAULT '[]',
  site_names_json TEXT NOT NULL DEFAULT '[]',
  brain TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  effort TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK(status IN ('queued','claimed','running','waiting_confirmation','completed','failed','canceled','expired')),
  lease_token_hash TEXT NOT NULL DEFAULT '',
  lease_expires_at TEXT,
  attempt INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 3,
  cancel_requested INTEGER NOT NULL DEFAULT 0,
  unlock_id TEXT NOT NULL DEFAULT '',
  progress_text TEXT NOT NULL DEFAULT '',
  final_output TEXT NOT NULL DEFAULT '',
  error_code TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  confirmation_id TEXT NOT NULL DEFAULT '',
  confirmation_json TEXT NOT NULL DEFAULT '',
  confirmation_decision TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  completed_at TEXT,
  UNIQUE(binding_id,request_id)
);
CREATE INDEX IF NOT EXISTS idx_pilot_tasks_claim ON pilot_tasks(binding_id,status,created_at);
CREATE INDEX IF NOT EXISTS idx_pilot_tasks_updated ON pilot_tasks(updated_at DESC);

CREATE TABLE IF NOT EXISTS pilot_task_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT NOT NULL REFERENCES pilot_tasks(id) ON DELETE CASCADE,
  seq INTEGER NOT NULL,
  event_type TEXT NOT NULL,
  payload_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  UNIQUE(task_id,seq)
);
CREATE INDEX IF NOT EXISTS idx_pilot_events_task ON pilot_task_events(task_id,id);

CREATE TABLE IF NOT EXISTS pilot_conversations (
  id TEXT PRIMARY KEY,
  binding_id TEXT NOT NULL REFERENCES pilot_bindings(id) ON DELETE CASCADE,
  pilot_conversation_id TEXT NOT NULL,
  site_ids_json TEXT NOT NULL DEFAULT '[]',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(binding_id,pilot_conversation_id)
);

CREATE TABLE IF NOT EXISTS pilot_unlocks (
  id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  user_name TEXT NOT NULL,
  device_id TEXT NOT NULL,
  binding_id TEXT NOT NULL,
  site_id INTEGER NOT NULL,
  operation TEXT NOT NULL,
  request_id TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  used_at TEXT
);

CREATE TABLE IF NOT EXISTS pilot_audit_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_name TEXT NOT NULL DEFAULT '',
  device_id TEXT NOT NULL DEFAULT '',
  binding_id TEXT NOT NULL DEFAULT '',
  site_id INTEGER NOT NULL DEFAULT 0,
  request_id TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL,
  message TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_pilot_audit_created ON pilot_audit_logs(created_at DESC);

CREATE TABLE IF NOT EXISTS pilot_binding_snapshots (
  binding_id TEXT PRIMARY KEY REFERENCES pilot_bindings(id) ON DELETE CASCADE,
  device_id TEXT NOT NULL,
  scheduled_tasks_json TEXT NOT NULL DEFAULT '[]',
  managed_sites_json TEXT NOT NULL DEFAULT '[]',
  updated_at TEXT NOT NULL
);
`

func (s *Store) migratePilotConsole() error {
	if s == nil {
		return nil
	}
	if _, err := s.db.Exec(pilotSchema); err != nil {
		return err
	}
	for _, item := range []struct{ name, definition string }{
		{"confirmation_id", "TEXT NOT NULL DEFAULT ''"},
		{"confirmation_json", "TEXT NOT NULL DEFAULT ''"},
		{"confirmation_decision", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.ensureColumn("pilot_tasks", item.name, item.definition); err != nil {
			return err
		}
	}
	return nil
}

func pilotHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

type PilotDevice struct {
	ID, Name, PilotVersion, ProtocolVersion, Platform string
	LastSeenAt, CreatedAt, UpdatedAt, RevokedAt       time.Time
}

func (d PilotDevice) Online(now time.Time) bool {
	return d.RevokedAt.IsZero() && !d.LastSeenAt.IsZero() && now.Sub(d.LastSeenAt) <= 45*time.Second
}

type PilotBinding struct {
	ID, DeviceID, CredentialKind, ConnectionName, KeyPrefix string
	CredentialID, CredentialSiteID, DefaultSiteID           int64
	CreatedAt, UpdatedAt, RevokedAt                         time.Time
}

type PilotTask struct {
	ID, BindingID, RequestID, ConversationID, Operation, Risk    string
	Prompt, SiteIDsJSON, SiteSlugsJSON, SiteNamesJSON            string
	Brain, Model, Effort, Status                                 string
	Attempt, MaxAttempts                                         int
	CancelRequested                                              bool
	UnlockID, ProgressText, FinalOutput, ErrorCode, ErrorMessage string
	ConfirmationID, ConfirmationJSON, ConfirmationDecision       string
	LeaseExpiresAt, CreatedAt, UpdatedAt, CompletedAt            time.Time
}

type PilotTaskEvent struct {
	ID          int64     `json:"id"`
	TaskID      string    `json:"task_id"`
	Seq         int       `json:"seq"`
	EventType   string    `json:"type"`
	PayloadJSON string    `json:"payload_json"`
	CreatedAt   time.Time `json:"created_at"`
}

type PilotBindingSnapshot struct {
	BindingID, DeviceID, ScheduledTasksJSON, ManagedSitesJSON string
	UpdatedAt                                                 time.Time
}

func (s *Store) SyncPilotBindingSnapshot(deviceID, bindingID, scheduledJSON, managedJSON string) error {
	var owner string
	var revoked sql.NullString
	if err := s.db.QueryRow(`SELECT device_id,revoked_at FROM pilot_bindings WHERE id=?`, bindingID).Scan(&owner, &revoked); err != nil {
		if err == sql.ErrNoRows {
			return ErrPilotNotFound
		}
		return err
	}
	if owner != deviceID || revoked.Valid {
		return ErrPilotNotFound
	}
	if !json.Valid([]byte(scheduledJSON)) || !json.Valid([]byte(managedJSON)) {
		return fmt.Errorf("invalid pilot binding snapshot")
	}
	_, err := s.db.Exec(`INSERT INTO pilot_binding_snapshots(binding_id,device_id,scheduled_tasks_json,managed_sites_json,updated_at)
VALUES(?,?,?,?,?)
ON CONFLICT(binding_id) DO UPDATE SET device_id=excluded.device_id,scheduled_tasks_json=excluded.scheduled_tasks_json,managed_sites_json=excluded.managed_sites_json,updated_at=excluded.updated_at`,
		bindingID, deviceID, scheduledJSON, managedJSON, fmtTime(time.Now()))
	return err
}

func (s *Store) GetPilotBindingSnapshot(bindingID string) (*PilotBindingSnapshot, error) {
	var snapshot PilotBindingSnapshot
	var updated sql.NullString
	err := s.db.QueryRow(`SELECT binding_id,device_id,scheduled_tasks_json,managed_sites_json,updated_at FROM pilot_binding_snapshots WHERE binding_id=?`, bindingID).
		Scan(&snapshot.BindingID, &snapshot.DeviceID, &snapshot.ScheduledTasksJSON, &snapshot.ManagedSitesJSON, &updated)
	if err == sql.ErrNoRows {
		return nil, ErrPilotNotFound
	}
	snapshot.UpdatedAt = parsePilotTime(updated)
	return &snapshot, err
}

func parsePilotTime(value sql.NullString) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return parseTime(value.String)
}

func scanPilotDevice(row interface{ Scan(...any) error }) (*PilotDevice, error) {
	var d PilotDevice
	var last, created, updated, revoked sql.NullString
	err := row.Scan(&d.ID, &d.Name, &d.PilotVersion, &d.ProtocolVersion, &d.Platform, &last, &created, &updated, &revoked)
	d.LastSeenAt, d.CreatedAt, d.UpdatedAt, d.RevokedAt = parsePilotTime(last), parsePilotTime(created), parsePilotTime(updated), parsePilotTime(revoked)
	return &d, err
}

func scanPilotBinding(row interface{ Scan(...any) error }) (*PilotBinding, error) {
	var b PilotBinding
	var def sql.NullInt64
	var created, updated, revoked sql.NullString
	err := row.Scan(&b.ID, &b.DeviceID, &b.CredentialKind, &b.CredentialID, &b.CredentialSiteID, &b.ConnectionName, &b.KeyPrefix, &def, &created, &updated, &revoked)
	if def.Valid {
		b.DefaultSiteID = def.Int64
	}
	b.CreatedAt, b.UpdatedAt, b.RevokedAt = parsePilotTime(created), parsePilotTime(updated), parsePilotTime(revoked)
	return &b, err
}

const pilotTaskCols = `id,binding_id,request_id,conversation_id,operation,risk,prompt,site_ids_json,site_slugs_json,site_names_json,brain,model,effort,status,attempt,max_attempts,cancel_requested,unlock_id,progress_text,final_output,error_code,error_message,confirmation_id,confirmation_json,confirmation_decision,lease_expires_at,created_at,updated_at,completed_at`

func scanPilotTask(row interface{ Scan(...any) error }) (*PilotTask, error) {
	var t PilotTask
	var cancel int
	var lease, created, updated, completed sql.NullString
	err := row.Scan(&t.ID, &t.BindingID, &t.RequestID, &t.ConversationID, &t.Operation, &t.Risk, &t.Prompt, &t.SiteIDsJSON, &t.SiteSlugsJSON, &t.SiteNamesJSON, &t.Brain, &t.Model, &t.Effort, &t.Status, &t.Attempt, &t.MaxAttempts, &cancel, &t.UnlockID, &t.ProgressText, &t.FinalOutput, &t.ErrorCode, &t.ErrorMessage, &t.ConfirmationID, &t.ConfirmationJSON, &t.ConfirmationDecision, &lease, &created, &updated, &completed)
	t.CancelRequested = cancel != 0
	t.LeaseExpiresAt, t.CreatedAt, t.UpdatedAt, t.CompletedAt = parsePilotTime(lease), parsePilotTime(created), parsePilotTime(updated), parsePilotTime(completed)
	return &t, err
}

func (s *Store) BindPilot(deviceID, secret, deviceName, pilotVersion, protocolVersion, osName, bindingID, kind string, credentialID, credentialSiteID int64, connectionName, keyPrefix string, defaultSiteID int64) (*PilotBinding, error) {
	if s == nil || strings.TrimSpace(deviceID) == "" || strings.TrimSpace(secret) == "" || strings.TrimSpace(bindingID) == "" {
		return nil, fmt.Errorf("invalid pilot binding")
	}
	now := fmtTime(time.Now())
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var existingHash string
	var revoked sql.NullString
	err = tx.QueryRow(`SELECT secret_hash,revoked_at FROM pilot_devices WHERE id=?`, deviceID).Scan(&existingHash, &revoked)
	switch {
	case err == sql.ErrNoRows:
		_, err = tx.Exec(`INSERT INTO pilot_devices(id,secret_hash,name,pilot_version,protocol_version,platform,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, deviceID, pilotHash(secret), deviceName, pilotVersion, protocolVersion, osName, now, now)
	case err == nil && revoked.Valid:
		_, err = tx.Exec(`UPDATE pilot_devices SET secret_hash=?,name=?,pilot_version=?,protocol_version=?,platform=?,updated_at=?,revoked_at=NULL WHERE id=?`, pilotHash(secret), deviceName, pilotVersion, protocolVersion, osName, now, deviceID)
	case err == nil && existingHash != pilotHash(secret):
		return nil, fmt.Errorf("device_secret_mismatch")
	case err == nil:
		_, err = tx.Exec(`UPDATE pilot_devices SET name=?,pilot_version=?,protocol_version=?,platform=?,updated_at=?,revoked_at=NULL WHERE id=?`, deviceName, pilotVersion, protocolVersion, osName, now, deviceID)
	}
	if err != nil {
		return nil, err
	}
	var def any
	if defaultSiteID > 0 {
		def = defaultSiteID
	}
	_, err = tx.Exec(`INSERT INTO pilot_bindings(id,device_id,credential_kind,credential_id,credential_site_id,connection_name,key_prefix,default_site_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		bindingID, deviceID, kind, credentialID, credentialSiteID, connectionName, keyPrefix, def, now, now)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetPilotBinding(bindingID)
}

func (s *Store) AuthenticatePilot(deviceID, secret string) (*PilotDevice, bool, error) {
	d, err := scanPilotDevice(s.db.QueryRow(`SELECT id,name,pilot_version,protocol_version,platform,last_seen_at,created_at,updated_at,revoked_at FROM pilot_devices WHERE id=? AND secret_hash=? AND revoked_at IS NULL`, deviceID, pilotHash(secret)))
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	return d, err == nil, err
}

func (s *Store) GetPilotBinding(id string) (*PilotBinding, error) {
	b, err := scanPilotBinding(s.db.QueryRow(`SELECT id,device_id,credential_kind,credential_id,credential_site_id,connection_name,key_prefix,default_site_id,created_at,updated_at,revoked_at FROM pilot_bindings WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, ErrPilotNotFound
	}
	return b, err
}

func (s *Store) ActivePilotBindings(deviceID string) ([]*PilotBinding, error) {
	rows, err := s.db.Query(`SELECT id,device_id,credential_kind,credential_id,credential_site_id,connection_name,key_prefix,default_site_id,created_at,updated_at,revoked_at FROM pilot_bindings WHERE device_id=? AND revoked_at IS NULL ORDER BY created_at`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PilotBinding
	for rows.Next() {
		b, e := scanPilotBinding(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) ListPilotDevices() ([]*PilotDevice, error) {
	rows, err := s.db.Query(`SELECT id,name,pilot_version,protocol_version,platform,last_seen_at,created_at,updated_at,revoked_at FROM pilot_devices ORDER BY revoked_at IS NOT NULL,last_seen_at DESC,created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PilotDevice
	for rows.Next() {
		d, e := scanPilotDevice(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) HeartbeatPilot(deviceID, name, version, protocol string) error {
	res, err := s.db.Exec(`UPDATE pilot_devices SET name=?,pilot_version=?,protocol_version=?,last_seen_at=?,updated_at=? WHERE id=? AND revoked_at IS NULL`, name, version, protocol, fmtTime(time.Now()), fmtTime(time.Now()), deviceID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrPilotNotFound
	}
	return nil
}

func (s *Store) RevokePilotBinding(id string) error {
	now := fmtTime(time.Now())
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE pilot_bindings SET revoked_at=COALESCE(revoked_at,?),updated_at=? WHERE id=?`, now, now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrPilotNotFound
	}
	_, err = tx.Exec(`UPDATE pilot_tasks SET status='canceled',error_code='binding_revoked',error_message='绑定已解除',updated_at=?,completed_at=? WHERE binding_id=? AND status IN ('queued','claimed','running','waiting_confirmation')`, now, now, id)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM pilot_unlocks WHERE binding_id=?`, id)
	if err != nil {
		return err
	}
	var deviceID string
	if err = tx.QueryRow(`SELECT device_id FROM pilot_bindings WHERE id=?`, id).Scan(&deviceID); err != nil {
		return err
	}
	var active int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM pilot_bindings WHERE device_id=? AND revoked_at IS NULL`, deviceID).Scan(&active); err != nil {
		return err
	}
	if active == 0 {
		_, err = tx.Exec(`UPDATE pilot_devices SET revoked_at=COALESCE(revoked_at,?),secret_hash=?,updated_at=? WHERE id=?`, now, pilotHash("revoked:"+deviceID+":"+now), now, deviceID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RevokePilotDevice(id string) error {
	now := fmtTime(time.Now())
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE pilot_devices SET revoked_at=COALESCE(revoked_at,?),secret_hash=?,updated_at=? WHERE id=?`, now, pilotHash("revoked:"+id+":"+now), now, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrPilotNotFound
	}
	_, err = tx.Exec(`UPDATE pilot_bindings SET revoked_at=COALESCE(revoked_at,?),updated_at=? WHERE device_id=?`, now, now, id)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM pilot_unlocks WHERE device_id=?`, id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdatePilotDefaultSite(bindingID string, siteID int64) error {
	var value any
	if siteID > 0 {
		value = siteID
	}
	res, err := s.db.Exec(`UPDATE pilot_bindings SET default_site_id=?,updated_at=? WHERE id=? AND revoked_at IS NULL`, value, fmtTime(time.Now()), bindingID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrPilotNotFound
	}
	return nil
}

func canonicalTaskInput(prompt string, siteIDs []int64, brain, model, effort, operation, risk string) string {
	raw, _ := json.Marshal(struct {
		Prompt                                string
		SiteIDs                               []int64
		Brain, Model, Effort, Operation, Risk string
	}{prompt, siteIDs, brain, model, effort, operation, risk})
	return pilotHash(string(raw))
}

func (s *Store) CreatePilotTask(id, bindingID, requestID, conversationID, operation, risk, prompt string, siteIDs []int64, siteSlugs, siteNames []string, brain, model, effort, unlockID string) (*PilotTask, bool, error) {
	ids, _ := json.Marshal(siteIDs)
	slugs, _ := json.Marshal(siteSlugs)
	names, _ := json.Marshal(siteNames)
	now := fmtTime(time.Now())
	_, err := s.db.Exec(`INSERT INTO pilot_tasks(id,binding_id,request_id,conversation_id,operation,risk,prompt,site_ids_json,site_slugs_json,site_names_json,brain,model,effort,status,unlock_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'queued',?,?,?)`,
		id, bindingID, requestID, conversationID, operation, risk, prompt, string(ids), string(slugs), string(names), brain, model, effort, unlockID, now, now)
	if err == nil {
		t, e := s.GetPilotTask(id)
		return t, true, e
	}
	var existing *PilotTask
	if strings.Contains(strings.ToLower(err.Error()), "unique") {
		existing, _ = s.GetPilotTaskByRequest(bindingID, requestID)
		if existing != nil {
			var oldIDs []int64
			_ = json.Unmarshal([]byte(existing.SiteIDsJSON), &oldIDs)
			if canonicalTaskInput(existing.Prompt, oldIDs, existing.Brain, existing.Model, existing.Effort, existing.Operation, existing.Risk) != canonicalTaskInput(prompt, siteIDs, brain, model, effort, operation, risk) {
				return nil, false, ErrPilotRequestConflict
			}
			return existing, false, nil
		}
	}
	return nil, false, err
}

func (s *Store) GetPilotTask(id string) (*PilotTask, error) {
	t, e := scanPilotTask(s.db.QueryRow(`SELECT `+pilotTaskCols+` FROM pilot_tasks WHERE id=?`, id))
	if e == sql.ErrNoRows {
		return nil, ErrPilotNotFound
	}
	return t, e
}
func (s *Store) GetPilotTaskByRequest(bindingID, requestID string) (*PilotTask, error) {
	t, e := scanPilotTask(s.db.QueryRow(`SELECT `+pilotTaskCols+` FROM pilot_tasks WHERE binding_id=? AND request_id=?`, bindingID, requestID))
	if e == sql.ErrNoRows {
		return nil, ErrPilotNotFound
	}
	return t, e
}

func (s *Store) PilotConversationBelongs(bindingID, conversationID string) (bool, error) {
	var found int
	err := s.db.QueryRow(`SELECT EXISTS(
		SELECT 1 FROM pilot_tasks WHERE binding_id=? AND conversation_id=?
		UNION ALL
		SELECT 1 FROM pilot_conversations WHERE binding_id=? AND pilot_conversation_id=?
	)`, bindingID, conversationID, bindingID, conversationID).Scan(&found)
	return found == 1, err
}
func (s *Store) ListPilotTasks(limit int) ([]*PilotTask, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, e := s.db.Query(`SELECT `+pilotTaskCols+` FROM pilot_tasks ORDER BY created_at DESC LIMIT ?`, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []*PilotTask
	for rows.Next() {
		t, e := scanPilotTask(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) ClaimPilotTask(deviceID, leaseToken string, lease time.Duration) (*PilotTask, error) {
	now := time.Now()
	tx, e := s.db.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	// 已经进入 running 的 AI 写任务绝不能因租约超时在另一进程盲目重跑；
	// 原 Pilot 没有凭相同租约恢复时明确终止，由用户显式 retry 生成新 request_id。
	if _, e = tx.Exec(`UPDATE pilot_tasks SET status='failed',error_code='execution_interrupted',error_message='Pilot 执行中断，未自动重复写操作',updated_at=?,completed_at=? WHERE status='running' AND lease_expires_at<?`, fmtTime(now), fmtTime(now), fmtTime(now)); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(`UPDATE pilot_tasks SET status='expired',error_code='confirmation_expired',error_message='等待确认已过期',updated_at=?,completed_at=? WHERE status='waiting_confirmation' AND lease_expires_at<?`, fmtTime(now), fmtTime(now), fmtTime(now)); e != nil {
		return nil, e
	}
	row := tx.QueryRow(`SELECT t.id FROM pilot_tasks t JOIN pilot_bindings b ON b.id=t.binding_id WHERE b.device_id=? AND b.revoked_at IS NULL AND (t.status='queued' OR (t.status='claimed' AND t.lease_expires_at<? AND t.attempt<t.max_attempts)) ORDER BY t.created_at LIMIT 1`, deviceID, fmtTime(now))
	var id string
	if e = row.Scan(&id); e == sql.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	res, e := tx.Exec(`UPDATE pilot_tasks SET status='claimed',lease_token_hash=?,lease_expires_at=?,attempt=attempt+1,updated_at=? WHERE id=?`, pilotHash(leaseToken), fmtTime(now.Add(lease)), fmtTime(now), id)
	if e != nil {
		return nil, e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return nil, ErrPilotLeaseLost
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return s.GetPilotTask(id)
}

func (s *Store) UpdatePilotTask(deviceID, taskID, leaseToken, status, progress, output, code, message string, lease time.Duration) error {
	allowed := map[string]bool{"running": true, "waiting_confirmation": true, "completed": true, "failed": true, "canceled": true}
	if !allowed[status] {
		return ErrPilotInvalidTransition
	}
	now := time.Now()
	var completed any
	if status == "completed" || status == "failed" || status == "canceled" {
		completed = fmtTime(now)
	}
	res, e := s.db.Exec(`UPDATE pilot_tasks SET status=?,progress_text=?,final_output=?,error_code=?,error_message=?,lease_expires_at=?,updated_at=?,completed_at=? WHERE id=? AND binding_id IN (SELECT id FROM pilot_bindings WHERE device_id=? AND revoked_at IS NULL) AND lease_token_hash=? AND status IN ('claimed','running','waiting_confirmation')`,
		status, progress, output, code, message, fmtTime(now.Add(lease)), fmtTime(now), completed, taskID, deviceID, pilotHash(leaseToken))
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrPilotLeaseLost
	}
	return nil
}

func (s *Store) SetPilotTaskConfirmation(deviceID, taskID, leaseToken, confirmationID, confirmationJSON string, lease time.Duration) (*PilotTask, error) {
	if strings.TrimSpace(confirmationID) == "" || len(confirmationJSON) > 256<<10 {
		return nil, ErrPilotInvalidTransition
	}
	now := time.Now()
	res, err := s.db.Exec(`UPDATE pilot_tasks SET status='waiting_confirmation',progress_text='等待 GCMS 网页确认',confirmation_id=?,confirmation_json=?,confirmation_decision=CASE WHEN confirmation_id=? THEN confirmation_decision ELSE '' END,lease_expires_at=?,updated_at=? WHERE id=? AND binding_id IN (SELECT id FROM pilot_bindings WHERE device_id=? AND revoked_at IS NULL) AND lease_token_hash=? AND status IN ('claimed','running','waiting_confirmation')`,
		confirmationID, confirmationJSON, confirmationID, fmtTime(now.Add(lease)), fmtTime(now), taskID, deviceID, pilotHash(leaseToken))
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrPilotLeaseLost
	}
	return s.GetPilotTask(taskID)
}

func (s *Store) DecidePilotTaskConfirmation(taskID string, allow bool) (*PilotTask, error) {
	decision := "deny"
	if allow {
		decision = "allow"
	}
	res, err := s.db.Exec(`UPDATE pilot_tasks SET confirmation_decision=?,updated_at=? WHERE id=? AND status='waiting_confirmation' AND confirmation_id<>'' AND confirmation_decision=''`, decision, fmtTime(time.Now()), taskID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrPilotInvalidTransition
	}
	return s.GetPilotTask(taskID)
}

func (s *Store) AppendPilotTaskEvent(deviceID, taskID, leaseToken, eventType, payload string) (*PilotTaskEvent, error) {
	tx, e := s.db.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var ok int
	e = tx.QueryRow(`SELECT 1 FROM pilot_tasks t JOIN pilot_bindings b ON b.id=t.binding_id WHERE t.id=? AND b.device_id=? AND b.revoked_at IS NULL AND t.lease_token_hash=?`, taskID, deviceID, pilotHash(leaseToken)).Scan(&ok)
	if e != nil {
		return nil, ErrPilotLeaseLost
	}
	var seq int
	_ = tx.QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM pilot_task_events WHERE task_id=?`, taskID).Scan(&seq)
	res, e := tx.Exec(`INSERT INTO pilot_task_events(task_id,seq,event_type,payload_json,created_at) VALUES(?,?,?,?,?)`, taskID, seq, eventType, payload, fmtTime(time.Now()))
	if e != nil {
		return nil, e
	}
	id, _ := res.LastInsertId()
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return &PilotTaskEvent{ID: id, TaskID: taskID, Seq: seq, EventType: eventType, PayloadJSON: payload, CreatedAt: time.Now()}, nil
}

func (s *Store) ListPilotTaskEvents(taskID string, after int64) ([]*PilotTaskEvent, error) {
	rows, e := s.db.Query(`SELECT id,task_id,seq,event_type,payload_json,created_at FROM pilot_task_events WHERE task_id=? AND id>? ORDER BY id LIMIT 500`, taskID, after)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []*PilotTaskEvent
	for rows.Next() {
		var v PilotTaskEvent
		var at sql.NullString
		if e := rows.Scan(&v.ID, &v.TaskID, &v.Seq, &v.EventType, &v.PayloadJSON, &at); e != nil {
			return nil, e
		}
		v.CreatedAt = parsePilotTime(at)
		out = append(out, &v)
	}
	return out, rows.Err()
}

// SyncPilotConversation records a later local Pilot continuation against the same
// real Conversation. Identical snapshots are ignored so periodic heartbeats do
// not create an unbounded event stream.
func (s *Store) SyncPilotConversation(deviceID, bindingID, conversationID, payload string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var valid int
	if err = tx.QueryRow(`SELECT 1 FROM pilot_bindings WHERE id=? AND device_id=? AND revoked_at IS NULL`, bindingID, deviceID).Scan(&valid); err != nil {
		return false, ErrPilotNotFound
	}
	now := fmtTime(time.Now())
	_, err = tx.Exec(`INSERT INTO pilot_conversations(id,binding_id,pilot_conversation_id,site_ids_json,created_at,updated_at)
		VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET updated_at=excluded.updated_at`,
		conversationID, bindingID, conversationID, "[]", now, now)
	if err != nil {
		return false, err
	}
	var taskID string
	if err = tx.QueryRow(`SELECT id FROM pilot_tasks WHERE binding_id=? AND conversation_id=? ORDER BY created_at DESC LIMIT 1`, bindingID, conversationID).Scan(&taskID); err == sql.ErrNoRows {
		return false, tx.Commit()
	} else if err != nil {
		return false, err
	}
	var previous string
	_ = tx.QueryRow(`SELECT payload_json FROM pilot_task_events WHERE task_id=? AND event_type='local_sync' ORDER BY seq DESC LIMIT 1`, taskID).Scan(&previous)
	if previous == payload {
		return false, tx.Commit()
	}
	var seq int
	_ = tx.QueryRow(`SELECT COALESCE(MAX(seq),0)+1 FROM pilot_task_events WHERE task_id=?`, taskID).Scan(&seq)
	if _, err = tx.Exec(`INSERT INTO pilot_task_events(task_id,seq,event_type,payload_json,created_at) VALUES(?,?,?,?,?)`, taskID, seq, "local_sync", payload, now); err != nil {
		return false, err
	}
	var body struct {
		Status string `json:"status"`
		Output string `json:"output"`
	}
	_ = json.Unmarshal([]byte(payload), &body)
	if body.Output != "" {
		_, _ = tx.Exec(`UPDATE pilot_tasks SET final_output=?,progress_text='Pilot 本地对话已同步',updated_at=? WHERE id=?`, body.Output, now, taskID)
	}
	return true, tx.Commit()
}

func (s *Store) RequestPilotTaskCancel(taskID string) error {
	res, e := s.db.Exec(`UPDATE pilot_tasks SET cancel_requested=1,status=CASE WHEN status='queued' THEN 'canceled' ELSE status END,completed_at=CASE WHEN status='queued' THEN ? ELSE completed_at END,updated_at=? WHERE id=? AND status IN ('queued','claimed','running','waiting_confirmation')`, fmtTime(time.Now()), fmtTime(time.Now()), taskID)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrPilotInvalidTransition
	}
	return nil
}
func (s *Store) RetryPilotTask(oldID, newID, newRequestID, newConversationID, unlockID string) (*PilotTask, error) {
	old, e := s.GetPilotTask(oldID)
	if e != nil {
		return nil, e
	}
	if old.Status != "failed" && old.Status != "expired" && old.Status != "canceled" {
		return nil, ErrPilotInvalidTransition
	}
	var ids []int64
	var slugs, names []string
	_ = json.Unmarshal([]byte(old.SiteIDsJSON), &ids)
	_ = json.Unmarshal([]byte(old.SiteSlugsJSON), &slugs)
	_ = json.Unmarshal([]byte(old.SiteNamesJSON), &names)
	t, _, e := s.CreatePilotTask(newID, old.BindingID, newRequestID, newConversationID, old.Operation, old.Risk, old.Prompt, ids, slugs, names, old.Brain, old.Model, old.Effort, unlockID)
	return t, e
}

func (s *Store) CreatePilotUnlock(id, token, user, deviceID, bindingID string, siteID int64, operation, requestID string, expires time.Time) error {
	_, e := s.db.Exec(`INSERT INTO pilot_unlocks(id,token_hash,user_name,device_id,binding_id,site_id,operation,request_id,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, pilotHash(token), user, deviceID, bindingID, siteID, operation, requestID, fmtTime(expires), fmtTime(time.Now()))
	return e
}
func (s *Store) ConsumePilotUnlock(token, deviceID, bindingID string, siteID int64, operation, requestID string) (string, error) {
	var id string
	e := s.db.QueryRow(`SELECT id FROM pilot_unlocks WHERE token_hash=?`, pilotHash(token)).Scan(&id)
	if e != nil {
		return "", fmt.Errorf("unlock_invalid_or_expired")
	}
	res, e := s.db.Exec(`UPDATE pilot_unlocks SET used_at=? WHERE id=? AND device_id=? AND binding_id=? AND site_id=? AND operation=? AND request_id=? AND used_at IS NULL AND expires_at>?`, fmtTime(time.Now()), id, deviceID, bindingID, siteID, operation, requestID, fmtTime(time.Now()))
	if e != nil {
		return "", e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return "", fmt.Errorf("unlock_invalid_or_expired")
	}
	return id, nil
}
func (s *Store) PilotAudit(user, device, binding string, siteID int64, requestID, action, message string) error {
	_, e := s.db.Exec(`INSERT INTO pilot_audit_logs(user_name,device_id,binding_id,site_id,request_id,action,message,created_at) VALUES(?,?,?,?,?,?,?,?)`, user, device, binding, siteID, requestID, action, message, fmtTime(time.Now()))
	return e
}
