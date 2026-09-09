package platform

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestGoogleAnalyticsScopeMigrationPreservesOldSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE site_google_analytics_summaries (
		site_id INTEGER PRIMARY KEY, property TEXT NOT NULL DEFAULT '', measurement_id TEXT NOT NULL DEFAULT '',
		active_users_7d INTEGER NOT NULL DEFAULT 0, sessions_7d INTEGER NOT NULL DEFAULT 0,
		active_users INTEGER NOT NULL DEFAULT 0, sessions INTEGER NOT NULL DEFAULT 0,
		range_key TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT '', error_message TEXT NOT NULL DEFAULT '',
		fetched_at TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL);
		INSERT INTO site_google_analytics_summaries(site_id, property, active_users, sessions, range_key, status, updated_at)
		VALUES(1, 'properties/123', 70, 80, '30', 'ok', '2026-09-09T00:00:00Z')`)
	closeErr := db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	summary, ok, err := s.SiteGoogleAnalyticsSummary(1)
	if err != nil || !ok || summary.ScopeKey != "" || summary.ActiveUsers != 70 || summary.Sessions != 80 {
		t.Fatalf("migrated summary=%+v ok=%v err=%v", summary, ok, err)
	}
	summary.ScopeKey = "v2:stream:properties/123/dataStreams/456"
	if err := s.UpsertSiteGoogleAnalyticsSummary(summary); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil {
		t.Fatal(err)
	}
	all, err := s.SiteGoogleAnalyticsSummaries()
	if err != nil || all[1] == nil || all[1].ScopeKey != summary.ScopeKey || all[1].ActiveUsers != 70 {
		t.Fatalf("scope round trip: %+v err=%v", all[1], err)
	}
}
