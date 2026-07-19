package platform

import (
	"database/sql"
	"strings"
	"time"
)

const (
	GoogleServiceAnalytics     = "analytics"
	GoogleServiceSearchConsole = "search_console"
	GoogleServiceAll           = "all"

	GoogleAnalyticsSummaryStatusOK    = "ok"
	GoogleAnalyticsSummaryStatusError = "error"

	GoogleSearchConsoleSummaryStatusOK    = GoogleAnalyticsSummaryStatusOK
	GoogleSearchConsoleSummaryStatusError = GoogleAnalyticsSummaryStatusError
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

type SiteGoogleIntegration struct {
	SiteID          int64
	Service         string
	GoogleAccountID string
	MeasurementID   string
	Property        string
	DataStream      string
	Enabled         bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type SiteGoogleAnalyticsSummary struct {
	SiteID        int64
	Property      string
	MeasurementID string
	ActiveUsers7D int
	Sessions7D    int
	ActiveUsers   int
	Sessions      int
	RangeKey      string
	Status        string
	ErrorMessage  string
	FetchedAt     time.Time
	UpdatedAt     time.Time
}

type SiteGoogleSearchConsoleSummary struct {
	SiteID        int64
	Property      string
	Clicks7D      int
	Impressions7D int
	CTR7D         float64
	Position7D    float64
	Clicks        int
	Impressions   int
	CTR           float64
	Position      float64
	RangeKey      string
	Status        string
	ErrorMessage  string
	FetchedAt     time.Time
	UpdatedAt     time.Time
}

func NormalizeGoogleService(service string) string {
	switch strings.ToLower(strings.TrimSpace(service)) {
	case GoogleServiceAnalytics, "ga", "google_analytics":
		return GoogleServiceAnalytics
	case GoogleServiceSearchConsole, "gsc", "search-console", "searchconsole", "google_search_console":
		return GoogleServiceSearchConsole
	case GoogleServiceAll, "google", "combined":
		return GoogleServiceAll
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
	if service == "" || service == GoogleServiceAll || googleID == "" {
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
	if service == "" || service == GoogleServiceAll {
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

func (s *Store) GoogleAccount(service, googleAccountID string) (*GoogleAccount, bool, error) {
	if s == nil {
		return nil, false, nil
	}
	service = NormalizeGoogleService(service)
	googleAccountID = strings.TrimSpace(googleAccountID)
	if service == "" || service == GoogleServiceAll || googleAccountID == "" {
		return nil, false, nil
	}
	row := s.db.QueryRow(`SELECT id,service,google_account_id,email,name,picture,scopes,access_token,refresh_token,token_expiry,created_at,updated_at
		FROM platform_google_accounts WHERE service=? AND google_account_id=?`, service, googleAccountID)
	acc, err := scanGoogleAccount(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return acc, true, nil
}

func (a *GoogleAccount) HasScope(scope string) bool {
	if a == nil || strings.TrimSpace(scope) == "" {
		return false
	}
	for _, part := range strings.Fields(a.Scopes) {
		if part == scope {
			return true
		}
	}
	return false
}

func (s *Store) DeleteGoogleAccount(service, googleAccountID string) error {
	if s == nil {
		return nil
	}
	service = NormalizeGoogleService(service)
	googleAccountID = strings.TrimSpace(googleAccountID)
	if service == "" || service == GoogleServiceAll || googleAccountID == "" {
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

func (s *Store) UpsertSiteGoogleIntegration(in *SiteGoogleIntegration) error {
	if s == nil || in == nil {
		return nil
	}
	service := NormalizeGoogleService(in.Service)
	if in.SiteID <= 0 || service == "" || service == GoogleServiceAll {
		return sql.ErrNoRows
	}
	now := fmtTime(time.Now())
	_, err := s.db.Exec(`INSERT INTO site_google_integrations(site_id,service,google_account_id,measurement_id,property,data_stream,enabled,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(site_id,service) DO UPDATE SET
			google_account_id=excluded.google_account_id,
			measurement_id=excluded.measurement_id,
			property=excluded.property,
			data_stream=excluded.data_stream,
			enabled=excluded.enabled,
			updated_at=excluded.updated_at`,
		in.SiteID, service, strings.TrimSpace(in.GoogleAccountID), strings.TrimSpace(in.MeasurementID), strings.TrimSpace(in.Property), strings.TrimSpace(in.DataStream), boolInt(in.Enabled), now, now)
	if err == nil && service == GoogleServiceAnalytics {
		_, err = s.db.Exec(`DELETE FROM site_google_analytics_summaries WHERE site_id=?`, in.SiteID)
	} else if err == nil && service == GoogleServiceSearchConsole {
		_, err = s.db.Exec(`DELETE FROM site_google_search_console_summaries WHERE site_id=?`, in.SiteID)
	}
	return err
}

func (s *Store) SiteGoogleIntegration(siteID int64, service string) (*SiteGoogleIntegration, bool, error) {
	if s == nil || siteID <= 0 {
		return nil, false, nil
	}
	service = NormalizeGoogleService(service)
	if service == "" || service == GoogleServiceAll {
		return nil, false, nil
	}
	row := s.db.QueryRow(`SELECT site_id,service,google_account_id,measurement_id,property,data_stream,enabled,created_at,updated_at
		FROM site_google_integrations WHERE site_id=? AND service=?`, siteID, service)
	in, err := scanSiteGoogleIntegration(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return in, true, nil
}

func (s *Store) SiteGoogleIntegrations() (map[int64]map[string]*SiteGoogleIntegration, error) {
	out := map[int64]map[string]*SiteGoogleIntegration{}
	if s == nil {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT site_id,service,google_account_id,measurement_id,property,data_stream,enabled,created_at,updated_at
		FROM site_google_integrations ORDER BY site_id ASC, service ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		in, err := scanSiteGoogleIntegration(rows)
		if err != nil {
			return nil, err
		}
		if out[in.SiteID] == nil {
			out[in.SiteID] = map[string]*SiteGoogleIntegration{}
		}
		out[in.SiteID][in.Service] = in
	}
	return out, rows.Err()
}

func (s *Store) UpsertSiteGoogleAnalyticsSummary(sum *SiteGoogleAnalyticsSummary) error {
	if s == nil || sum == nil {
		return nil
	}
	if sum.SiteID <= 0 {
		return sql.ErrNoRows
	}
	status := strings.TrimSpace(sum.Status)
	if status == "" {
		status = GoogleAnalyticsSummaryStatusOK
	}
	fetched := ""
	if !sum.FetchedAt.IsZero() {
		fetched = fmtTime(sum.FetchedAt)
	}
	now := fmtTime(time.Now())
	_, err := s.db.Exec(`INSERT INTO site_google_analytics_summaries(site_id,property,measurement_id,active_users_7d,sessions_7d,active_users,sessions,range_key,status,error_message,fetched_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(site_id) DO UPDATE SET
			property=excluded.property,
			measurement_id=excluded.measurement_id,
			active_users_7d=excluded.active_users_7d,
			sessions_7d=excluded.sessions_7d,
			active_users=excluded.active_users,
			sessions=excluded.sessions,
			range_key=excluded.range_key,
			status=excluded.status,
			error_message=excluded.error_message,
			fetched_at=excluded.fetched_at,
			updated_at=excluded.updated_at`,
		sum.SiteID, strings.TrimSpace(sum.Property), strings.TrimSpace(sum.MeasurementID), sum.ActiveUsers7D, sum.Sessions7D, sum.ActiveUsers, sum.Sessions, strings.TrimSpace(sum.RangeKey),
		status, strings.TrimSpace(sum.ErrorMessage), fetched, now)
	return err
}

func (s *Store) SiteGoogleAnalyticsSummary(siteID int64) (*SiteGoogleAnalyticsSummary, bool, error) {
	if s == nil || siteID <= 0 {
		return nil, false, nil
	}
	row := s.db.QueryRow(`SELECT site_id,property,measurement_id,active_users_7d,sessions_7d,active_users,sessions,range_key,status,error_message,fetched_at,updated_at
		FROM site_google_analytics_summaries WHERE site_id=?`, siteID)
	sum, err := scanSiteGoogleAnalyticsSummary(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return sum, true, nil
}

func (s *Store) SiteGoogleAnalyticsSummaries() (map[int64]*SiteGoogleAnalyticsSummary, error) {
	out := map[int64]*SiteGoogleAnalyticsSummary{}
	if s == nil {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT site_id,property,measurement_id,active_users_7d,sessions_7d,active_users,sessions,range_key,status,error_message,fetched_at,updated_at
		FROM site_google_analytics_summaries ORDER BY site_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		sum, err := scanSiteGoogleAnalyticsSummary(rows)
		if err != nil {
			return nil, err
		}
		out[sum.SiteID] = sum
	}
	return out, rows.Err()
}

func (s *Store) DeleteSiteGoogleAnalyticsSummary(siteID int64) error {
	if s == nil || siteID <= 0 {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM site_google_analytics_summaries WHERE site_id=?`, siteID)
	return err
}

func (s *Store) UpsertSiteGoogleSearchConsoleSummary(sum *SiteGoogleSearchConsoleSummary) error {
	if s == nil || sum == nil {
		return nil
	}
	if sum.SiteID <= 0 {
		return sql.ErrNoRows
	}
	status := strings.TrimSpace(sum.Status)
	if status == "" {
		status = GoogleSearchConsoleSummaryStatusOK
	}
	fetched := ""
	if !sum.FetchedAt.IsZero() {
		fetched = fmtTime(sum.FetchedAt)
	}
	now := fmtTime(time.Now())
	_, err := s.db.Exec(`INSERT INTO site_google_search_console_summaries(site_id,property,clicks_7d,impressions_7d,ctr_7d,position_7d,clicks,impressions,ctr,position,range_key,status,error_message,fetched_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(site_id) DO UPDATE SET
			property=excluded.property,
			clicks_7d=excluded.clicks_7d,
			impressions_7d=excluded.impressions_7d,
			ctr_7d=excluded.ctr_7d,
			position_7d=excluded.position_7d,
			clicks=excluded.clicks,
			impressions=excluded.impressions,
			ctr=excluded.ctr,
			position=excluded.position,
			range_key=excluded.range_key,
			status=excluded.status,
			error_message=excluded.error_message,
			fetched_at=excluded.fetched_at,
			updated_at=excluded.updated_at`,
		sum.SiteID, strings.TrimSpace(sum.Property), sum.Clicks7D, sum.Impressions7D, sum.CTR7D, sum.Position7D, sum.Clicks, sum.Impressions, sum.CTR, sum.Position, strings.TrimSpace(sum.RangeKey),
		status, strings.TrimSpace(sum.ErrorMessage), fetched, now)
	return err
}

func (s *Store) SiteGoogleSearchConsoleSummary(siteID int64) (*SiteGoogleSearchConsoleSummary, bool, error) {
	if s == nil || siteID <= 0 {
		return nil, false, nil
	}
	row := s.db.QueryRow(`SELECT site_id,property,clicks_7d,impressions_7d,ctr_7d,position_7d,clicks,impressions,ctr,position,range_key,status,error_message,fetched_at,updated_at
		FROM site_google_search_console_summaries WHERE site_id=?`, siteID)
	sum, err := scanSiteGoogleSearchConsoleSummary(row)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return sum, true, nil
}

func (s *Store) SiteGoogleSearchConsoleSummaries() (map[int64]*SiteGoogleSearchConsoleSummary, error) {
	out := map[int64]*SiteGoogleSearchConsoleSummary{}
	if s == nil {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT site_id,property,clicks_7d,impressions_7d,ctr_7d,position_7d,clicks,impressions,ctr,position,range_key,status,error_message,fetched_at,updated_at
		FROM site_google_search_console_summaries ORDER BY site_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		sum, err := scanSiteGoogleSearchConsoleSummary(rows)
		if err != nil {
			return nil, err
		}
		out[sum.SiteID] = sum
	}
	return out, rows.Err()
}

func (s *Store) DeleteSiteGoogleSearchConsoleSummary(siteID int64) error {
	if s == nil || siteID <= 0 {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM site_google_search_console_summaries WHERE site_id=?`, siteID)
	return err
}

func (s *Store) DeleteSiteGoogleIntegration(siteID int64, service string) error {
	if s == nil || siteID <= 0 {
		return nil
	}
	service = NormalizeGoogleService(service)
	if service == "" || service == GoogleServiceAll {
		return sql.ErrNoRows
	}
	_, err := s.db.Exec(`DELETE FROM site_google_integrations WHERE site_id=? AND service=?`, siteID, service)
	if err == nil && service == GoogleServiceAnalytics {
		_, err = s.db.Exec(`DELETE FROM site_google_analytics_summaries WHERE site_id=?`, siteID)
	} else if err == nil && service == GoogleServiceSearchConsole {
		_, err = s.db.Exec(`DELETE FROM site_google_search_console_summaries WHERE site_id=?`, siteID)
	}
	return err
}

func (s *Store) ClearGoogleOAuthData(settingKeys ...string) error {
	if s == nil {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, key := range settingKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM settings WHERE key=?`, key); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM platform_google_oauth_states`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM platform_google_accounts WHERE service IN (?,?)`, GoogleServiceAnalytics, GoogleServiceSearchConsole); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM site_google_integrations WHERE service IN (?,?)`, GoogleServiceAnalytics, GoogleServiceSearchConsole); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM site_google_analytics_summaries`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM site_google_search_console_summaries`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.settingsMu.Lock()
	if s.settingsLoaded {
		for _, key := range settingKeys {
			delete(s.settings, strings.TrimSpace(key))
		}
	}
	s.settingsMu.Unlock()
	return nil
}

type siteGoogleIntegrationScanner interface {
	Scan(dest ...any) error
}

func scanSiteGoogleIntegration(row siteGoogleIntegrationScanner) (*SiteGoogleIntegration, error) {
	var in SiteGoogleIntegration
	var enabled int
	var created, updated string
	if err := row.Scan(&in.SiteID, &in.Service, &in.GoogleAccountID, &in.MeasurementID, &in.Property, &in.DataStream, &enabled, &created, &updated); err != nil {
		return nil, err
	}
	in.Service = NormalizeGoogleService(in.Service)
	in.Enabled = enabled != 0
	in.CreatedAt = parseTime(created)
	in.UpdatedAt = parseTime(updated)
	return &in, nil
}

type siteGoogleAnalyticsSummaryScanner interface {
	Scan(dest ...any) error
}

func scanSiteGoogleAnalyticsSummary(row siteGoogleAnalyticsSummaryScanner) (*SiteGoogleAnalyticsSummary, error) {
	var sum SiteGoogleAnalyticsSummary
	var fetched, updated string
	if err := row.Scan(&sum.SiteID, &sum.Property, &sum.MeasurementID, &sum.ActiveUsers7D, &sum.Sessions7D, &sum.ActiveUsers, &sum.Sessions, &sum.RangeKey, &sum.Status, &sum.ErrorMessage, &fetched, &updated); err != nil {
		return nil, err
	}
	if sum.ActiveUsers == 0 && sum.Sessions == 0 {
		sum.ActiveUsers, sum.Sessions = sum.ActiveUsers7D, sum.Sessions7D
	}
	sum.FetchedAt = parseTime(fetched)
	sum.UpdatedAt = parseTime(updated)
	return &sum, nil
}

type siteGoogleSearchConsoleSummaryScanner interface {
	Scan(dest ...any) error
}

func scanSiteGoogleSearchConsoleSummary(row siteGoogleSearchConsoleSummaryScanner) (*SiteGoogleSearchConsoleSummary, error) {
	var sum SiteGoogleSearchConsoleSummary
	var fetched, updated string
	if err := row.Scan(&sum.SiteID, &sum.Property, &sum.Clicks7D, &sum.Impressions7D, &sum.CTR7D, &sum.Position7D, &sum.Clicks, &sum.Impressions, &sum.CTR, &sum.Position, &sum.RangeKey, &sum.Status, &sum.ErrorMessage, &fetched, &updated); err != nil {
		return nil, err
	}
	if sum.Clicks == 0 && sum.Impressions == 0 {
		sum.Clicks, sum.Impressions, sum.CTR, sum.Position = sum.Clicks7D, sum.Impressions7D, sum.CTR7D, sum.Position7D
	}
	sum.FetchedAt = parseTime(fetched)
	sum.UpdatedAt = parseTime(updated)
	return &sum, nil
}
