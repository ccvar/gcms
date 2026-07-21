// Package platform stores platform-level data for multisite management.
package platform

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"cms.ccvar.com/internal/store"

	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	path string

	pwMu        sync.Mutex
	pwHash      string
	pwIsDefault bool

	settingsMu     sync.RWMutex
	settings       map[string]string
	settingsLoaded bool
}

type Site struct {
	ID                          int64
	Slug                        string
	Name                        string
	Status                      string
	IsDefault                   bool
	ManagementAutomationEnabled bool
	AdminNote                   string
	DBPath                      string
	UploadDir                   string
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
}

type SiteDomain struct {
	ID                int64
	SiteID            int64
	Scheme            string
	Host              string
	IsPrimary         bool
	RedirectToPrimary bool
	Enabled           bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type ArchivedSite struct {
	ID                          int64
	OriginalSiteID              int64
	Slug                        string
	Name                        string
	Status                      string
	ManagementAutomationEnabled bool
	AdminNote                   string
	DBPath                      string
	UploadDir                   string
	ArchivePath                 string
	DomainsJSON                 string
	ArchivedAt                  time.Time
	CreatedAt                   time.Time
	UpdatedAt                   time.Time
}

type DefaultSiteBootstrap struct {
	Slug                        string
	Name                        string
	DBPath                      string
	UploadDir                   string
	AdminUser                   string
	AdminPasswordHash           string
	ManagementAutomationEnabled bool
}

const schema = `
CREATE TABLE IF NOT EXISTS sites (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'enabled',
  is_default INTEGER NOT NULL DEFAULT 0,
  management_automation_enabled INTEGER NOT NULL DEFAULT 0,
  admin_note TEXT NOT NULL DEFAULT '',
  db_path TEXT NOT NULL,
  upload_dir TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_sites_one_default
ON sites(is_default)
WHERE is_default = 1;

CREATE TABLE IF NOT EXISTS site_domains (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
  scheme TEXT NOT NULL DEFAULT 'https',
  host TEXT NOT NULL,
  is_primary INTEGER NOT NULL DEFAULT 0,
  redirect_to_primary INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_site_domains_enabled_host
ON site_domains(host)
WHERE enabled = 1;

CREATE UNIQUE INDEX IF NOT EXISTS idx_site_domains_one_primary
ON site_domains(site_id)
WHERE is_primary = 1;

CREATE TABLE IF NOT EXISTS archived_sites (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  original_site_id INTEGER NOT NULL,
  slug TEXT NOT NULL,
  name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'disabled',
  management_automation_enabled INTEGER NOT NULL DEFAULT 0,
  admin_note TEXT NOT NULL DEFAULT '',
  db_path TEXT NOT NULL,
  upload_dir TEXT NOT NULL DEFAULT '',
  archive_path TEXT NOT NULL,
  domains_json TEXT NOT NULL DEFAULT '[]',
  archived_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_archived_sites_archived_at
ON archived_sites(archived_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS platform_admins (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS platform_sessions (
  token_hash TEXT PRIMARY KEY,
  admin_id INTEGER NOT NULL REFERENCES platform_admins(id) ON DELETE CASCADE,
  csrf TEXT NOT NULL,
  current_site_id INTEGER REFERENCES sites(id) ON DELETE SET NULL,
  expires_at TEXT NOT NULL,
  pw_dismissed INTEGER NOT NULL DEFAULT 0,
  must_change_password INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS platform_google_accounts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  service TEXT NOT NULL,
  google_account_id TEXT NOT NULL,
  email TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  picture TEXT NOT NULL DEFAULT '',
  scopes TEXT NOT NULL DEFAULT '',
  access_token TEXT NOT NULL DEFAULT '',
  refresh_token TEXT NOT NULL DEFAULT '',
  token_expiry TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(service, google_account_id)
);

CREATE INDEX IF NOT EXISTS idx_platform_google_accounts_service_updated
ON platform_google_accounts(service, updated_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS platform_google_oauth_states (
  state TEXT PRIMARY KEY,
  service TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_platform_google_oauth_states_expires
ON platform_google_oauth_states(expires_at);

CREATE TABLE IF NOT EXISTS site_google_integrations (
  site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
  service TEXT NOT NULL,
  google_account_id TEXT NOT NULL DEFAULT '',
  measurement_id TEXT NOT NULL DEFAULT '',
  property TEXT NOT NULL DEFAULT '',
  data_stream TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(site_id, service)
);

CREATE INDEX IF NOT EXISTS idx_site_google_integrations_service_enabled
ON site_google_integrations(service, enabled);

CREATE TABLE IF NOT EXISTS site_google_analytics_summaries (
  site_id INTEGER PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
  property TEXT NOT NULL DEFAULT '',
  measurement_id TEXT NOT NULL DEFAULT '',
	active_users_7d INTEGER NOT NULL DEFAULT 0,
	sessions_7d INTEGER NOT NULL DEFAULT 0,
	active_users INTEGER NOT NULL DEFAULT 0,
	sessions INTEGER NOT NULL DEFAULT 0,
	range_key TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  fetched_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS site_google_search_console_summaries (
  site_id INTEGER PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE,
  property TEXT NOT NULL DEFAULT '',
  clicks_7d INTEGER NOT NULL DEFAULT 0,
  impressions_7d INTEGER NOT NULL DEFAULT 0,
  ctr_7d REAL NOT NULL DEFAULT 0,
	position_7d REAL NOT NULL DEFAULT 0,
	clicks INTEGER NOT NULL DEFAULT 0,
	impressions INTEGER NOT NULL DEFAULT 0,
	ctr REAL NOT NULL DEFAULT 0,
	position REAL NOT NULL DEFAULT 0,
	range_key TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  fetched_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS platform_automation_keys (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  name            TEXT NOT NULL,
  token_hash      TEXT NOT NULL UNIQUE,
  token_prefix    TEXT NOT NULL,
  membership_mode TEXT NOT NULL DEFAULT 'allowlist',
  scopes          TEXT NOT NULL DEFAULT '',
  expires_at      TEXT,
  last_used_at    TEXT,
  created_at      TEXT NOT NULL,
  revoked_at      TEXT
);

CREATE TABLE IF NOT EXISTS platform_automation_key_sites (
  key_id     INTEGER NOT NULL REFERENCES platform_automation_keys(id) ON DELETE CASCADE,
  site_id    INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  PRIMARY KEY(key_id, site_id)
);

CREATE INDEX IF NOT EXISTS idx_platform_automation_key_sites_site
ON platform_automation_key_sites(site_id);

CREATE TABLE IF NOT EXISTS platform_automation_logs (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  key_id    INTEGER REFERENCES platform_automation_keys(id) ON DELETE SET NULL,
  site_id   INTEGER NOT NULL DEFAULT 0,
  action    TEXT NOT NULL,
  kind      TEXT NOT NULL DEFAULT '',
  target_id INTEGER NOT NULL DEFAULT 0,
  message   TEXT NOT NULL DEFAULT '',
  at        TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_platform_automation_logs_key
ON platform_automation_logs(key_id, id DESC);

CREATE TABLE IF NOT EXISTS platform_control_operation_receipts (
  key_id          INTEGER NOT NULL REFERENCES platform_automation_keys(id) ON DELETE CASCADE,
  operation       TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  request_hash    TEXT NOT NULL,
  state           TEXT NOT NULL DEFAULT 'running' CHECK(state IN ('running', 'completed')),
  http_status     INTEGER NOT NULL DEFAULT 0,
  response_json   TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  completed_at    TEXT,
  PRIMARY KEY(key_id, operation, idempotency_key)
);

CREATE INDEX IF NOT EXISTS idx_platform_control_operation_receipts_updated
ON platform_control_operation_receipts(updated_at DESC);
`

func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	s := &Store{db: db, path: strings.TrimSpace(path)}
	if _, err := s.db.Exec(schema); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("平台数据库迁移失败: %w", err)
	}
	if err := s.migrate(); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("平台数据库迁移失败: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	if s == nil {
		return nil
	}
	for _, col := range []struct{ table, name, definition string }{
		{"site_google_integrations", "data_stream", "TEXT NOT NULL DEFAULT ''"},
		{"site_google_analytics_summaries", "active_users", "INTEGER NOT NULL DEFAULT 0"},
		{"site_google_analytics_summaries", "sessions", "INTEGER NOT NULL DEFAULT 0"},
		{"site_google_analytics_summaries", "range_key", "TEXT NOT NULL DEFAULT ''"},
		{"site_google_search_console_summaries", "clicks", "INTEGER NOT NULL DEFAULT 0"},
		{"site_google_search_console_summaries", "impressions", "INTEGER NOT NULL DEFAULT 0"},
		{"site_google_search_console_summaries", "ctr", "REAL NOT NULL DEFAULT 0"},
		{"site_google_search_console_summaries", "position", "REAL NOT NULL DEFAULT 0"},
		{"site_google_search_console_summaries", "range_key", "TEXT NOT NULL DEFAULT ''"},
		{"platform_sessions", "must_change_password", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := s.ensureColumn(col.table, col.name, col.definition); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureColumn(table, column, definition string) error {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition)
	return err
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) loadSettings() error {
	if s == nil {
		return nil
	}
	rows, err := s.db.Query(`SELECT key,value FROM settings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	next := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		next[key] = value
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.settingsMu.Lock()
	s.settings = next
	s.settingsLoaded = true
	s.settingsMu.Unlock()
	return nil
}

func (s *Store) LookupSetting(key string) (string, bool, error) {
	if s == nil {
		return "", false, nil
	}
	s.settingsMu.RLock()
	if s.settingsLoaded {
		v, ok := s.settings[key]
		s.settingsMu.RUnlock()
		return v, ok, nil
	}
	s.settingsMu.RUnlock()
	if err := s.loadSettings(); err != nil {
		return "", false, err
	}
	s.settingsMu.RLock()
	v, ok := s.settings[key]
	s.settingsMu.RUnlock()
	return v, ok, nil
}

func (s *Store) GetSetting(key string) (string, error) {
	v, _, err := s.LookupSetting(key)
	return v, err
}

func (s *Store) Setting(key string) string {
	v, _ := s.GetSetting(key)
	return v
}

func (s *Store) SetSetting(key, value string) error {
	if s == nil {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	if err != nil {
		return err
	}
	s.settingsMu.Lock()
	if s.settingsLoaded {
		if s.settings == nil {
			s.settings = map[string]string{}
		}
		s.settings[key] = value
	}
	s.settingsMu.Unlock()
	return nil
}

func (s *Store) BootstrapDefaultSite(in DefaultSiteBootstrap) error {
	if s == nil {
		return nil
	}
	slug := normalizeSlug(in.Slug, "main")
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "Default Site"
	}
	adminUser := strings.TrimSpace(in.AdminUser)
	if adminUser == "" {
		adminUser = "admin"
	}
	now := fmtTime(time.Now())
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var siteID int64
	err = tx.QueryRow(`SELECT id FROM sites WHERE is_default=1 LIMIT 1`).Scan(&siteID)
	switch {
	case err == sql.ErrNoRows:
		res, err := tx.Exec(`INSERT INTO sites(slug,name,status,is_default,management_automation_enabled,db_path,upload_dir,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?)`,
			slug, name, "enabled", 1, boolInt(in.ManagementAutomationEnabled), strings.TrimSpace(in.DBPath), strings.TrimSpace(in.UploadDir), now, now)
		if err != nil {
			return err
		}
		siteID, _ = res.LastInsertId()
	case err != nil:
		return err
	default:
		_, err = tx.Exec(`UPDATE sites SET db_path=CASE WHEN db_path='' THEN ? ELSE db_path END,
			upload_dir=CASE WHEN upload_dir='' THEN ? ELSE upload_dir END,
			management_automation_enabled=CASE WHEN management_automation_enabled=0 THEN ? ELSE management_automation_enabled END,
			updated_at=?
			WHERE id=?`,
			strings.TrimSpace(in.DBPath), strings.TrimSpace(in.UploadDir), boolInt(in.ManagementAutomationEnabled), now, siteID)
		if err != nil {
			return err
		}
	}

	var adminID int64
	err = tx.QueryRow(`SELECT id FROM platform_admins WHERE username=?`, adminUser).Scan(&adminID)
	switch {
	case err == sql.ErrNoRows:
		if strings.TrimSpace(in.AdminPasswordHash) != "" {
			if _, err := tx.Exec(`INSERT INTO platform_admins(username,password_hash,created_at,updated_at)
				VALUES(?,?,?,?)`, adminUser, strings.TrimSpace(in.AdminPasswordHash), now, now); err != nil {
				return err
			}
		}
	case err != nil:
		return err
	}
	return tx.Commit()
}

func (s *Store) DefaultSite() (*Site, error) {
	if s == nil {
		return nil, sql.ErrNoRows
	}
	row := s.db.QueryRow(`SELECT id,slug,name,status,is_default,management_automation_enabled,admin_note,db_path,upload_dir,created_at,updated_at
		FROM sites WHERE is_default=1 LIMIT 1`)
	return scanSite(row)
}

func (s *Store) GetSite(id int64) (*Site, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	row := s.db.QueryRow(`SELECT id,slug,name,status,is_default,management_automation_enabled,admin_note,db_path,upload_dir,created_at,updated_at
		FROM sites WHERE id=?`, id)
	site, err := scanSite(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return site, true, nil
}

func (s *Store) Sites() ([]*Site, error) {
	rows, err := s.db.Query(`SELECT id,slug,name,status,is_default,management_automation_enabled,admin_note,db_path,upload_dir,created_at,updated_at
		FROM sites ORDER BY is_default DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Site
	for rows.Next() {
		site, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

func (s *Store) CreateSite(slug, name, dbPath, uploadDir string, managementAutomation bool) (*Site, error) {
	if s == nil {
		return nil, sql.ErrConnDone
	}
	slug = normalizeSlug(slug, "")
	if slug == "" {
		return nil, fmt.Errorf("站点标记不能为空")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = slug
	}
	now := fmtTime(time.Now())
	res, err := s.db.Exec(`INSERT INTO sites(slug,name,status,is_default,management_automation_enabled,db_path,upload_dir,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)`,
		slug, name, "enabled", 0, boolInt(managementAutomation), strings.TrimSpace(dbPath), strings.TrimSpace(uploadDir), now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	site, ok, err := s.GetSite(id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, sql.ErrNoRows
	}
	return site, nil
}

func (s *Store) SetSiteName(id int64, name string) error {
	if s == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("站点名称不能为空")
	}
	res, err := s.db.Exec(`UPDATE sites SET name=?,updated_at=? WHERE id=?`, name, fmtTime(time.Now()), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SetSiteStatus(id int64, status string) error {
	if s == nil {
		return nil
	}
	status = strings.TrimSpace(strings.ToLower(status))
	if status != "enabled" && status != "disabled" {
		return fmt.Errorf("无效站点状态")
	}
	res, err := s.db.Exec(`UPDATE sites SET status=?,updated_at=? WHERE id=?`, status, fmtTime(time.Now()), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SetSiteAutomation(id int64, enabled bool) error {
	if s == nil {
		return nil
	}
	res, err := s.db.Exec(`UPDATE sites SET management_automation_enabled=?,updated_at=? WHERE id=?`, boolInt(enabled), fmtTime(time.Now()), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SetDefaultSite(id int64) error {
	if s == nil {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRow(`SELECT status FROM sites WHERE id=?`, id).Scan(&status); err != nil {
		return err
	}
	if status != "enabled" {
		return fmt.Errorf("只能把启用站点设为默认站点")
	}
	now := fmtTime(time.Now())
	if _, err := tx.Exec(`UPDATE sites SET is_default=0,updated_at=? WHERE is_default=1`, now); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE sites SET is_default=1,updated_at=? WHERE id=?`, now, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *Store) SiteDomains() ([]*SiteDomain, error) {
	if s == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT id,site_id,scheme,host,is_primary,redirect_to_primary,enabled,created_at,updated_at
		FROM site_domains ORDER BY site_id ASC, is_primary DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SiteDomain
	for rows.Next() {
		d, err := scanSiteDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) AddSiteDomain(siteID int64, scheme, host string, primary, redirect bool) error {
	if s == nil {
		return nil
	}
	scheme = strings.TrimSpace(strings.ToLower(scheme))
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return fmt.Errorf("域名不能为空")
	}
	now := fmtTime(time.Now())
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if primary {
		if _, err := tx.Exec(`UPDATE site_domains SET is_primary=0,updated_at=? WHERE site_id=?`, now, siteID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO site_domains(site_id,scheme,host,is_primary,redirect_to_primary,enabled,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?)`, siteID, scheme, host, boolInt(primary), boolInt(redirect), 1, now, now); err != nil {
		return err
	}
	return tx.Commit()
}

// SiteDomainSpec describes one domain to bind when replacing a site's domain set.
type SiteDomainSpec struct {
	Scheme   string
	Host     string
	Primary  bool
	Redirect bool
}

// ReplaceSiteDomains atomically replaces every domain bound to a site with specs
// (primary first, then aliases). An empty slice clears all domains (unbinds the site).
func (s *Store) ReplaceSiteDomains(siteID int64, specs []SiteDomainSpec) error {
	if s == nil {
		return nil
	}
	now := fmtTime(time.Now())
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM site_domains WHERE site_id=?`, siteID); err != nil {
		return err
	}
	for _, d := range specs {
		scheme := strings.TrimSpace(strings.ToLower(d.Scheme))
		if scheme != "http" && scheme != "https" {
			scheme = "https"
		}
		host := strings.TrimSpace(strings.ToLower(d.Host))
		if host == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO site_domains(site_id,scheme,host,is_primary,redirect_to_primary,enabled,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?)`, siteID, scheme, host, boolInt(d.Primary), boolInt(d.Redirect), 1, now, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ArchiveSite(id int64, archivePath string) (*ArchivedSite, error) {
	if s == nil {
		return nil, sql.ErrConnDone
	}
	archivePath = strings.TrimSpace(archivePath)
	if archivePath == "" {
		return nil, fmt.Errorf("归档路径不能为空")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	site, err := scanSite(tx.QueryRow(`SELECT id,slug,name,status,is_default,management_automation_enabled,admin_note,db_path,upload_dir,created_at,updated_at
		FROM sites WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	if site.IsDefault {
		return nil, fmt.Errorf("默认站点不能归档删除")
	}
	if site.Status != "disabled" {
		return nil, fmt.Errorf("只能归档已关闭的站点")
	}
	domains, err := siteDomainsTx(tx, id)
	if err != nil {
		return nil, err
	}
	domainsJSON := "[]"
	if b, err := json.Marshal(domains); err == nil {
		domainsJSON = string(b)
	}
	now := fmtTime(time.Now())
	created := fmtTime(site.CreatedAt)
	if site.CreatedAt.IsZero() {
		created = now
	}
	updated := fmtTime(site.UpdatedAt)
	if site.UpdatedAt.IsZero() {
		updated = now
	}
	res, err := tx.Exec(`INSERT INTO archived_sites(original_site_id,slug,name,status,management_automation_enabled,admin_note,db_path,upload_dir,archive_path,domains_json,archived_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		site.ID, site.Slug, site.Name, site.Status, boolInt(site.ManagementAutomationEnabled), site.AdminNote, site.DBPath, site.UploadDir, archivePath, domainsJSON, now, created, updated)
	if err != nil {
		return nil, err
	}
	archivedID, _ := res.LastInsertId()
	if _, err := tx.Exec(`UPDATE platform_sessions SET current_site_id=NULL,updated_at=? WHERE current_site_id=?`, now, id); err != nil {
		return nil, err
	}
	del, err := tx.Exec(`DELETE FROM sites WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	if n, err := del.RowsAffected(); err != nil {
		return nil, err
	} else if n == 0 {
		return nil, sql.ErrNoRows
	}
	archived, err := scanArchivedSite(tx.QueryRow(`SELECT id,original_site_id,slug,name,status,management_automation_enabled,admin_note,db_path,upload_dir,archive_path,domains_json,archived_at,created_at,updated_at
		FROM archived_sites WHERE id=?`, archivedID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return archived, nil
}

func (s *Store) ArchivedSites() ([]*ArchivedSite, error) {
	if s == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT id,original_site_id,slug,name,status,management_automation_enabled,admin_note,db_path,upload_dir,archive_path,domains_json,archived_at,created_at,updated_at
		FROM archived_sites ORDER BY archived_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ArchivedSite
	for rows.Next() {
		site, err := scanArchivedSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

func (s *Store) GetArchivedSite(id int64) (*ArchivedSite, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	site, err := scanArchivedSite(s.db.QueryRow(`SELECT id,original_site_id,slug,name,status,management_automation_enabled,admin_note,db_path,upload_dir,archive_path,domains_json,archived_at,created_at,updated_at
		FROM archived_sites WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return site, true, nil
}

func (s *Store) DeleteArchivedSite(id int64) error {
	if s == nil {
		return nil
	}
	res, err := s.db.Exec(`DELETE FROM archived_sites WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) RestoreArchivedSite(id int64) (*Site, error) {
	if s == nil {
		return nil, sql.ErrConnDone
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	archived, err := scanArchivedSite(tx.QueryRow(`SELECT id,original_site_id,slug,name,status,management_automation_enabled,admin_note,db_path,upload_dir,archive_path,domains_json,archived_at,created_at,updated_at
		FROM archived_sites WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	var existing int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sites WHERE slug=?`, archived.Slug).Scan(&existing); err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, fmt.Errorf("站点标记 %q 已存在，无法恢复", archived.Slug)
	}

	status := "disabled"
	now := fmtTime(time.Now())
	created := fmtTime(archived.CreatedAt)
	if archived.CreatedAt.IsZero() {
		created = now
	}
	var idInUse int
	if archived.OriginalSiteID > 0 {
		if err := tx.QueryRow(`SELECT COUNT(*) FROM sites WHERE id=?`, archived.OriginalSiteID).Scan(&idInUse); err != nil {
			return nil, err
		}
	}
	var siteID int64
	if archived.OriginalSiteID > 0 && idInUse == 0 {
		_, err = tx.Exec(`INSERT INTO sites(id,slug,name,status,is_default,management_automation_enabled,admin_note,db_path,upload_dir,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			archived.OriginalSiteID, archived.Slug, archived.Name, status, 0, boolInt(archived.ManagementAutomationEnabled), archived.AdminNote, archived.DBPath, archived.UploadDir, created, now)
		siteID = archived.OriginalSiteID
	} else {
		var res sql.Result
		res, err = tx.Exec(`INSERT INTO sites(slug,name,status,is_default,management_automation_enabled,admin_note,db_path,upload_dir,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`,
			archived.Slug, archived.Name, status, 0, boolInt(archived.ManagementAutomationEnabled), archived.AdminNote, archived.DBPath, archived.UploadDir, created, now)
		if err == nil {
			siteID, _ = res.LastInsertId()
		}
	}
	if err != nil {
		return nil, err
	}

	var domains []*SiteDomain
	if raw := strings.TrimSpace(archived.DomainsJSON); raw != "" {
		if err := json.Unmarshal([]byte(raw), &domains); err != nil {
			return nil, fmt.Errorf("读取归档域名失败: %w", err)
		}
	}
	for _, domain := range domains {
		if domain == nil {
			continue
		}
		host := strings.TrimSpace(strings.ToLower(domain.Host))
		if host == "" {
			continue
		}
		scheme := strings.TrimSpace(strings.ToLower(domain.Scheme))
		if scheme != "http" && scheme != "https" {
			scheme = "https"
		}
		domainCreated := fmtTime(domain.CreatedAt)
		if domain.CreatedAt.IsZero() {
			domainCreated = now
		}
		if _, err := tx.Exec(`INSERT INTO site_domains(site_id,scheme,host,is_primary,redirect_to_primary,enabled,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?)`,
			siteID, scheme, host, boolInt(domain.IsPrimary), boolInt(domain.RedirectToPrimary), boolInt(domain.Enabled), domainCreated, now); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`DELETE FROM archived_sites WHERE id=?`, id); err != nil {
		return nil, err
	}
	site, err := scanSite(tx.QueryRow(`SELECT id,slug,name,status,is_default,management_automation_enabled,admin_note,db_path,upload_dir,created_at,updated_at
		FROM sites WHERE id=?`, siteID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return site, nil
}

func (s *Store) GetAdminCredentials() (string, string, error) {
	if s == nil {
		return "", "", sql.ErrNoRows
	}
	var user, hash string
	err := s.db.QueryRow(`SELECT username,password_hash FROM platform_admins ORDER BY id ASC LIMIT 1`).Scan(&user, &hash)
	return user, hash, err
}

func (s *Store) SetAdminPasswordHash(username, hash string) error {
	if s == nil {
		return nil
	}
	now := fmtTime(time.Now())
	res, err := s.db.Exec(`UPDATE platform_admins SET password_hash=?,updated_at=? WHERE username=?`, hash, now, username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = s.db.Exec(`INSERT INTO platform_admins(username,password_hash,created_at,updated_at) VALUES(?,?,?,?)`, username, hash, now, now)
	return err
}

// RevokeAdminSessions 注销平台后台的全部会话。
// 密码由服务器本机运维命令轮换时，不保留任何旧登录态。
func (s *Store) RevokeAdminSessions() error {
	if s == nil {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM platform_sessions`)
	return err
}

func (s *Store) IsDefaultPassword() bool {
	_, hash, err := s.GetAdminCredentials()
	if err != nil || hash == "" {
		return false
	}
	s.pwMu.Lock()
	defer s.pwMu.Unlock()
	if hash == s.pwHash {
		return s.pwIsDefault
	}
	s.pwHash = hash
	s.pwIsDefault = store.IsDefaultAdminPasswordHash(hash)
	return s.pwIsDefault
}

func (s *Store) CreateAdminSession(token, user, csrf string, expiresAt time.Time) error {
	if s == nil {
		return nil
	}
	now := time.Now()
	_, _ = s.db.Exec(`DELETE FROM platform_sessions WHERE expires_at<=?`, fmtTime(now))
	var adminID int64
	if err := s.db.QueryRow(`SELECT id FROM platform_admins WHERE username=?`, user).Scan(&adminID); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO platform_sessions(token_hash,admin_id,csrf,current_site_id,expires_at,pw_dismissed,must_change_password,created_at,updated_at)
		VALUES(?,?,?,?,?,0,0,?,?)`,
		sessionTokenHash(token), adminID, csrf, nil, fmtTime(expiresAt), fmtTime(now), fmtTime(now))
	return err
}

func (s *Store) GetAdminSession(token string) (store.AdminSession, bool, error) {
	var sess store.AdminSession
	var expires string
	var dismissed, mustChange int
	var currentSite sql.NullInt64
	err := s.db.QueryRow(`SELECT a.username,ps.csrf,ps.current_site_id,ps.expires_at,ps.pw_dismissed,ps.must_change_password
		FROM platform_sessions ps
		JOIN platform_admins a ON a.id=ps.admin_id
		WHERE ps.token_hash=?`, sessionTokenHash(token)).
		Scan(&sess.User, &sess.CSRF, &currentSite, &expires, &dismissed, &mustChange)
	if err == sql.ErrNoRows {
		return store.AdminSession{}, false, nil
	}
	if err != nil {
		return store.AdminSession{}, false, err
	}
	t, err := time.Parse(time.RFC3339, expires)
	if err != nil || time.Now().After(t) {
		_ = s.DeleteAdminSession(token)
		return store.AdminSession{}, false, nil
	}
	sess.ExpiresAt = t
	sess.PwDismissed = dismissed == 1
	sess.MustChangePassword = mustChange == 1
	if currentSite.Valid {
		sess.CurrentSiteID = currentSite.Int64
	}
	return sess, true, nil
}

func (s *Store) DeleteAdminSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM platform_sessions WHERE token_hash=?`, sessionTokenHash(token))
	return err
}

func (s *Store) DismissAdminPasswordWarning(token string) error {
	_, err := s.db.Exec(`UPDATE platform_sessions SET pw_dismissed=1,updated_at=? WHERE token_hash=?`, fmtTime(time.Now()), sessionTokenHash(token))
	return err
}

func (s *Store) RequireAdminPasswordChange(token string) error {
	_, err := s.db.Exec(`UPDATE platform_sessions SET must_change_password=1,updated_at=? WHERE token_hash=?`, fmtTime(time.Now()), sessionTokenHash(token))
	return err
}

func (s *Store) SetAdminSessionSite(token string, siteID int64) error {
	res, err := s.db.Exec(`UPDATE platform_sessions SET current_site_id=?,updated_at=? WHERE token_hash=?`, siteID, fmtTime(time.Now()), sessionTokenHash(token))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

type siteScanner interface {
	Scan(dest ...any) error
}

type siteDomainScanner interface {
	Scan(dest ...any) error
}

type archivedSiteScanner interface {
	Scan(dest ...any) error
}

func scanSite(row siteScanner) (*Site, error) {
	var s Site
	var isDefault, management int
	var created, updated string
	if err := row.Scan(&s.ID, &s.Slug, &s.Name, &s.Status, &isDefault, &management, &s.AdminNote, &s.DBPath, &s.UploadDir, &created, &updated); err != nil {
		return nil, err
	}
	s.IsDefault = isDefault == 1
	s.ManagementAutomationEnabled = management == 1
	s.CreatedAt = parseTime(created)
	s.UpdatedAt = parseTime(updated)
	return &s, nil
}

func siteDomainsTx(tx *sql.Tx, siteID int64) ([]*SiteDomain, error) {
	rows, err := tx.Query(`SELECT id,site_id,scheme,host,is_primary,redirect_to_primary,enabled,created_at,updated_at
		FROM site_domains WHERE site_id=? ORDER BY is_primary DESC, id ASC`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SiteDomain
	for rows.Next() {
		d, err := scanSiteDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func scanSiteDomain(row siteDomainScanner) (*SiteDomain, error) {
	var d SiteDomain
	var isPrimary, redirectToPrimary, enabled int
	var created, updated string
	if err := row.Scan(&d.ID, &d.SiteID, &d.Scheme, &d.Host, &isPrimary, &redirectToPrimary, &enabled, &created, &updated); err != nil {
		return nil, err
	}
	d.IsPrimary = isPrimary == 1
	d.RedirectToPrimary = redirectToPrimary == 1
	d.Enabled = enabled == 1
	d.CreatedAt = parseTime(created)
	d.UpdatedAt = parseTime(updated)
	return &d, nil
}

func scanArchivedSite(row archivedSiteScanner) (*ArchivedSite, error) {
	var s ArchivedSite
	var management int
	var archived, created, updated string
	if err := row.Scan(&s.ID, &s.OriginalSiteID, &s.Slug, &s.Name, &s.Status, &management, &s.AdminNote, &s.DBPath, &s.UploadDir, &s.ArchivePath, &s.DomainsJSON, &archived, &created, &updated); err != nil {
		return nil, err
	}
	s.ManagementAutomationEnabled = management == 1
	s.ArchivedAt = parseTime(archived)
	s.CreatedAt = parseTime(created)
	s.UpdatedAt = parseTime(updated)
	return &s, nil
}

func normalizeSlug(v, fallback string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	var b strings.Builder
	lastDash := false
	for _, r := range v {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return fallback
	}
	return out
}

func sessionTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func fmtTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func parseTime(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, v)
	return t
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// ===================== 平台级自动化密钥（多站 AI 管理，v2） =====================

const (
	KeyMembershipAll       = "all"
	KeyMembershipAllowlist = "allowlist"
)

const platformKeyCols = `id,name,token_prefix,membership_mode,scopes,expires_at,last_used_at,created_at,revoked_at`

// PlatformAutomationKey 是一把可管理多个站点的平台密钥（存平台库，非站库）。
type PlatformAutomationKey struct {
	ID             int64
	Name           string
	TokenPrefix    string
	MembershipMode string // all | allowlist
	Scopes         string // 逗号分隔，复用 apiScope 词表
	AllowedSiteIDs []int64
	ExpiresAt      time.Time
	LastUsedAt     time.Time
	CreatedAt      time.Time
	RevokedAt      time.Time
}

// PlatformAutomationLogEntry 是一条跨站审计记录。
type PlatformAutomationLogEntry struct {
	ID       int64
	KeyID    int64
	SiteID   int64
	Action   string
	Kind     string
	TargetID int64
	Message  string
	At       time.Time
}

// ScopeList 拆出能力列表。
func (k *PlatformAutomationKey) ScopeList() []string {
	var out []string
	for _, s := range strings.Split(k.Scopes, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Active 报告密钥是否仍有效（未吊销、未过期）。
func (k *PlatformAutomationKey) Active() bool {
	if k == nil || !k.RevokedAt.IsZero() {
		return false
	}
	if !k.ExpiresAt.IsZero() && time.Now().After(k.ExpiresAt) {
		return false
	}
	return true
}

// CanManageSite 只判成员范围；站点自身 enabled / automation-on 由 PlatformKeyCanAccessSite 实时查库判定。
func (k *PlatformAutomationKey) CanManageSite(siteID int64) bool {
	if k == nil || !k.Active() {
		return false
	}
	if k.MembershipMode == KeyMembershipAll {
		return true
	}
	for _, id := range k.AllowedSiteIDs {
		if id == siteID {
			return true
		}
	}
	return false
}

func normalizePlatformMembership(mode string) string {
	if mode == KeyMembershipAll {
		return KeyMembershipAll
	}
	return KeyMembershipAllowlist
}

func dedupInt64(in []int64) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, v := range in {
		if v > 0 && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func scanPlatformKey(row interface{ Scan(...any) error }) (*PlatformAutomationKey, error) {
	var k PlatformAutomationKey
	var expires, lastUsed, revoked sql.NullString
	var created string
	if err := row.Scan(&k.ID, &k.Name, &k.TokenPrefix, &k.MembershipMode, &k.Scopes, &expires, &lastUsed, &created, &revoked); err != nil {
		return nil, err
	}
	k.ExpiresAt = parseTime(expires.String)
	k.LastUsedAt = parseTime(lastUsed.String)
	k.CreatedAt = parseTime(created)
	k.RevokedAt = parseTime(revoked.String)
	return &k, nil
}

func (s *Store) platformKeyAllowedSiteIDs(keyID int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT site_id FROM platform_automation_key_sites WHERE key_id=? ORDER BY site_id`, keyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CreatePlatformKey 插入一把密钥；token 明文由调用方生成（只显示一次），此处只存 SHA-256。mode!="all" 按 allowlist。
func (s *Store) CreatePlatformKey(name, token, prefix, mode, scopes string, allowedSiteIDs []int64, expiresAt time.Time) (int64, error) {
	if s == nil {
		return 0, sql.ErrConnDone
	}
	mode = normalizePlatformMembership(mode)
	now := fmtTime(time.Now())
	var exp any
	if !expiresAt.IsZero() {
		exp = fmtTime(expiresAt)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO platform_automation_keys(name,token_hash,token_prefix,membership_mode,scopes,expires_at,created_at)
		VALUES(?,?,?,?,?,?,?)`,
		strings.TrimSpace(name), sessionTokenHash(token), prefix, mode, strings.TrimSpace(scopes), exp, now)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if mode == KeyMembershipAllowlist {
		for _, sid := range dedupInt64(allowedSiteIDs) {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO platform_automation_key_sites(key_id,site_id,created_at) VALUES(?,?,?)`, id, sid, now); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

// GetPlatformKeyByToken 按明文 token 查有效密钥并水化白名单；未命中/吊销/过期一律 (nil,false,nil)。
func (s *Store) GetPlatformKeyByToken(token string) (*PlatformAutomationKey, bool, error) {
	if s == nil || strings.TrimSpace(token) == "" {
		return nil, false, nil
	}
	row := s.db.QueryRow(`SELECT `+platformKeyCols+` FROM platform_automation_keys WHERE token_hash=?`, sessionTokenHash(token))
	k, err := scanPlatformKey(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !k.Active() {
		return nil, false, nil
	}
	if k.MembershipMode == KeyMembershipAllowlist {
		if k.AllowedSiteIDs, err = s.platformKeyAllowedSiteIDs(k.ID); err != nil {
			return nil, false, err
		}
	}
	return k, true, nil
}

// GetPlatformKey 按 id 取（含已吊销/过期，供后台展示），水化白名单。
func (s *Store) GetPlatformKey(id int64) (*PlatformAutomationKey, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	row := s.db.QueryRow(`SELECT `+platformKeyCols+` FROM platform_automation_keys WHERE id=?`, id)
	k, err := scanPlatformKey(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if k.AllowedSiteIDs, err = s.platformKeyAllowedSiteIDs(k.ID); err != nil {
		return nil, false, err
	}
	return k, true, nil
}

// ListPlatformKeys 列出所有密钥（含白名单），供后台管理。
func (s *Store) ListPlatformKeys() ([]*PlatformAutomationKey, error) {
	if s == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT ` + platformKeyCols + ` FROM platform_automation_keys ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	var out []*PlatformAutomationKey
	for rows.Next() {
		k, err := scanPlatformKey(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, k := range out {
		if k.AllowedSiteIDs, err = s.platformKeyAllowedSiteIDs(k.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UpdatePlatformKey 更新名称/成员模式/能力/白名单/过期（不改 token）。
func (s *Store) UpdatePlatformKey(id int64, name, mode, scopes string, allowedSiteIDs []int64, expiresAt time.Time) error {
	if s == nil {
		return sql.ErrConnDone
	}
	mode = normalizePlatformMembership(mode)
	var exp any
	if !expiresAt.IsZero() {
		exp = fmtTime(expiresAt)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE platform_automation_keys SET name=?,membership_mode=?,scopes=?,expires_at=? WHERE id=?`,
		strings.TrimSpace(name), mode, strings.TrimSpace(scopes), exp, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM platform_automation_key_sites WHERE key_id=?`, id); err != nil {
		return err
	}
	if mode == KeyMembershipAllowlist {
		now := fmtTime(time.Now())
		for _, sid := range dedupInt64(allowedSiteIDs) {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO platform_automation_key_sites(key_id,site_id,created_at) VALUES(?,?,?)`, id, sid, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// RotatePlatformKeyToken 只换 token（保留名称/成员/能力/白名单），重置最近使用时间。仅未吊销的密钥可换。
func (s *Store) RotatePlatformKeyToken(id int64, token, prefix string) error {
	if s == nil {
		return sql.ErrConnDone
	}
	res, err := s.db.Exec(`UPDATE platform_automation_keys
		SET token_hash=?, token_prefix=?, last_used_at=NULL
		WHERE id=? AND revoked_at IS NULL`, sessionTokenHash(token), prefix, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// TouchPlatformKey 记录最近使用时间。
func (s *Store) TouchPlatformKey(id int64) error {
	if s == nil {
		return nil
	}
	_, err := s.db.Exec(`UPDATE platform_automation_keys SET last_used_at=? WHERE id=?`, fmtTime(time.Now()), id)
	return err
}

// RevokePlatformKey 吊销（软删，保留审计）。
func (s *Store) RevokePlatformKey(id int64) error {
	if s == nil {
		return sql.ErrConnDone
	}
	_, err := s.db.Exec(`UPDATE platform_automation_keys SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, fmtTime(time.Now()), id)
	return err
}

// DeletePlatformKey 物理删除（连带白名单级联；审计 key_id 置空保留）。
func (s *Store) DeletePlatformKey(id int64) error {
	if s == nil {
		return sql.ErrConnDone
	}
	_, err := s.db.Exec(`DELETE FROM platform_automation_keys WHERE id=?`, id)
	return err
}

// PlatformKeyCanAccessSite 实时判定：成员范围 + 站点 enabled + automation-on（分发层用，避免读缓存的 rt.Site）。
func (s *Store) PlatformKeyCanAccessSite(k *PlatformAutomationKey, siteID int64) (bool, error) {
	if k == nil || !k.CanManageSite(siteID) {
		return false, nil
	}
	site, ok, err := s.GetSite(siteID)
	if err != nil || !ok || site == nil {
		return false, err
	}
	return site.Status == "enabled" && site.ManagementAutomationEnabled, nil
}

// ManageableSites 返回该密钥当前**实际可管**的站点（成员 ∩ enabled ∩ automation-on）。
// 发现接口与分发共用它，保证"能发现即能调用"。
func (s *Store) ManageableSites(k *PlatformAutomationKey) ([]*Site, error) {
	if s == nil || k == nil {
		return nil, nil
	}
	sites, err := s.Sites()
	if err != nil {
		return nil, err
	}
	var out []*Site
	for _, st := range sites {
		if st == nil || st.Status != "enabled" || !st.ManagementAutomationEnabled {
			continue
		}
		if k.CanManageSite(st.ID) {
			out = append(out, st)
		}
	}
	return out, nil
}

// CreatePlatformAutomationLog 记录一条跨站审计（keyID<=0 时以 NULL 存，保留吊销后的历史）。
func (s *Store) CreatePlatformAutomationLog(keyID, siteID int64, action, kind string, targetID int64, message string) error {
	if s == nil {
		return nil
	}
	var kid any
	if keyID > 0 {
		kid = keyID
	}
	_, err := s.db.Exec(`INSERT INTO platform_automation_logs(key_id,site_id,action,kind,target_id,message,at) VALUES(?,?,?,?,?,?,?)`,
		kid, siteID, action, kind, targetID, strings.TrimSpace(message), fmtTime(time.Now()))
	return err
}

// ListPlatformAutomationLogs 取最近的跨站审计（供后台时间线）。
func (s *Store) ListPlatformAutomationLogs(limit int) ([]*PlatformAutomationLogEntry, error) {
	if s == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id,COALESCE(key_id,0),site_id,action,kind,target_id,message,at FROM platform_automation_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PlatformAutomationLogEntry
	for rows.Next() {
		var e PlatformAutomationLogEntry
		var at string
		if err := rows.Scan(&e.ID, &e.KeyID, &e.SiteID, &e.Action, &e.Kind, &e.TargetID, &e.Message, &at); err != nil {
			return nil, err
		}
		e.At = parseTime(at)
		out = append(out, &e)
	}
	return out, rows.Err()
}
