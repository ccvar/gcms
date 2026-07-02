package platform

import (
	"database/sql"
	"strings"
	"time"
)

const (
	GoogleServiceAnalytics     = "analytics"
	GoogleServiceSearchConsole = "search_console"
)

type GoogleAccount struct {
	ID              int64
	Service         string
	GoogleAccountID string
	Email           string
	Name            string
	Picture         string
	Scopes          string
	AccessToken     string
	RefreshToken    string
	TokenExpiry     time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func NormalizeGoogleService(service string) string {
	switch strings.ToLower(strings.TrimSpace(service)) {
	case GoogleServiceAnalytics, "ga", "google_analytics":
		return GoogleServiceAnalytics
	case GoogleServiceSearchConsole, "gsc", "search-console", "searchconsole", "google_search_console":
		return GoogleServiceSearchConsole
	default:
		return ""
	}
}

func (s *Store) CreateGoogleOAuthState(state, service string, expiresAt time.Time) error {
	if s == nil {
		return nil
	}
	service = NormalizeGoogleService(service)
	if strings.TrimSpace(state) == "" || service == "" {
		return sql.ErrNoRows
	}
	now := time.Now()
	_, _ = s.db.Exec(`DELETE FROM platform_google_oauth_states WHERE expires_at<=?`, fmtTime(now))
	_, err := s.db.Exec(`INSERT INTO platform_google_oauth_states(state,service,created_at,expires_at)
		VALUES(?,?,?,?)`, strings.TrimSpace(state), service, fmtTime(now), fmtTime(expiresAt))
	return err
}

func (s *Store) ConsumeGoogleOAuthState(state string) (string, bool, error) {
	if s == nil {
		return "", false, nil
	}
	state = strings.TrimSpace(state)
	if state == "" {
		return "", false, nil
	}
	now := time.Now()
	_, _ = s.db.Exec(`DELETE FROM platform_google_oauth_states WHERE expires_at<=?`, fmtTime(now))
	var service, expires string
	err := s.db.QueryRow(`SELECT service,expires_at FROM platform_google_oauth_states WHERE state=?`, state).Scan(&service, &expires)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	_, _ = s.db.Exec(`DELETE FROM platform_google_oauth_states WHERE state=?`, state)
	if exp := parseTime(expires); exp.IsZero() || now.After(exp) {
		return "", false, nil
	}
	service = NormalizeGoogleService(service)
	return service, service != "", nil
}

func (s *Store) UpsertGoogleAccount(acc *GoogleAccount) error {
	if s == nil || acc == nil {
		return nil
	}
	service := NormalizeGoogleService(acc.Service)
	googleID := strings.TrimSpace(acc.GoogleAccountID)
	if service == "" || googleID == "" {
		return sql.ErrNoRows
	}
	now := fmtTime(time.Now())
	expiry := ""
	if !acc.TokenExpiry.IsZero() {
		expiry = fmtTime(acc.TokenExpiry)
	}
	_, err := s.db.Exec(`INSERT INTO platform_google_accounts(service,google_account_id,email,name,picture,scopes,access_token,refresh_token,token_expiry,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(service,google_account_id) DO UPDATE SET
			email=excluded.email,
			name=excluded.name,
			picture=excluded.picture,
			scopes=excluded.scopes,
			access_token=excluded.access_token,
			refresh_token=CASE WHEN excluded.refresh_token<>'' THEN excluded.refresh_token ELSE platform_google_accounts.refresh_token END,
			token_expiry=excluded.token_expiry,
			updated_at=excluded.updated_at`,
		service, googleID, strings.TrimSpace(acc.Email), strings.TrimSpace(acc.Name), strings.TrimSpace(acc.Picture),
		strings.TrimSpace(acc.Scopes), strings.TrimSpace(acc.AccessToken), strings.TrimSpace(acc.RefreshToken), expiry, now, now)
	return err
}

func (s *Store) GoogleAccounts(service string) ([]*GoogleAccount, error) {
	if s == nil {
		return nil, nil
	}
	service = NormalizeGoogleService(service)
	if service == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT id,service,google_account_id,email,name,picture,scopes,access_token,refresh_token,token_expiry,created_at,updated_at
		FROM platform_google_accounts WHERE service=? ORDER BY updated_at DESC, id DESC`, service)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*GoogleAccount
	for rows.Next() {
		acc, err := scanGoogleAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, acc)
	}
	return out, rows.Err()
}

func (s *Store) DeleteGoogleAccount(service, googleAccountID string) error {
	if s == nil {
		return nil
	}
	service = NormalizeGoogleService(service)
	googleAccountID = strings.TrimSpace(googleAccountID)
	if service == "" || googleAccountID == "" {
		return sql.ErrNoRows
	}
	_, err := s.db.Exec(`DELETE FROM platform_google_accounts WHERE service=? AND google_account_id=?`, service, googleAccountID)
	return err
}

type googleAccountScanner interface {
	Scan(dest ...any) error
}

func scanGoogleAccount(row googleAccountScanner) (*GoogleAccount, error) {
	var acc GoogleAccount
	var expiry, created, updated string
	if err := row.Scan(&acc.ID, &acc.Service, &acc.GoogleAccountID, &acc.Email, &acc.Name, &acc.Picture, &acc.Scopes, &acc.AccessToken, &acc.RefreshToken, &expiry, &created, &updated); err != nil {
		return nil, err
	}
	acc.TokenExpiry = parseTime(expiry)
	acc.CreatedAt = parseTime(created)
	acc.UpdatedAt = parseTime(updated)
	return &acc, nil
}
