// Package platform stores platform-level data for multisite management.
package platform

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"cms.ccvar.com/internal/store"
	"golang.org/x/crypto/bcrypt"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB

	pwMu        sync.Mutex
	pwHash      string
	pwIsDefault bool
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
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
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
	s := &Store{db: db}
	if _, err := s.db.Exec(schema); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("平台数据库迁移失败: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
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
	s.pwIsDefault = bcrypt.CompareHashAndPassword([]byte(hash), []byte(store.DefaultAdminPassword)) == nil
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
	_, err := s.db.Exec(`INSERT INTO platform_sessions(token_hash,admin_id,csrf,current_site_id,expires_at,pw_dismissed,created_at,updated_at)
		VALUES(?,?,?,?,?,0,?,?)`,
		sessionTokenHash(token), adminID, csrf, nil, fmtTime(expiresAt), fmtTime(now), fmtTime(now))
	return err
}

func (s *Store) GetAdminSession(token string) (store.AdminSession, bool, error) {
	var sess store.AdminSession
	var expires string
	var dismissed int
	var currentSite sql.NullInt64
	err := s.db.QueryRow(`SELECT a.username,ps.csrf,ps.current_site_id,ps.expires_at,ps.pw_dismissed
		FROM platform_sessions ps
		JOIN platform_admins a ON a.id=ps.admin_id
		WHERE ps.token_hash=?`, sessionTokenHash(token)).
		Scan(&sess.User, &sess.CSRF, &currentSite, &expires, &dismissed)
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
