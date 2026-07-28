package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestPagePlatformMigrationIsolatedAcrossSiteDatabases(t *testing.T) {
	root := t.TempDir()
	futurePath := filepath.Join(root, "sites", "future", "cms.db")
	malformedPath := filepath.Join(root, "sites", "malformed", "cms.db")
	healthyPath := filepath.Join(root, "sites", "healthy", "cms.db")

	future := openMigrationIsolationStore(t, futurePath)
	if _, err := future.db.Exec(`DROP INDEX idx_page_revisions_request`); err != nil {
		t.Fatalf("drop future marker index: %v", err)
	}
	if _, err := future.db.Exec(`DROP TABLE page_assets`); err != nil {
		t.Fatalf("drop future marker table: %v", err)
	}
	if _, err := future.db.Exec(`
		UPDATE store_schema_migrations SET version=?
		WHERE name='page_platform'`, pagePlatformSchemaVersion+1); err != nil {
		t.Fatalf("mark future schema: %v", err)
	}
	if err := future.Close(); err != nil {
		t.Fatal(err)
	}

	malformed := openMigrationIsolationStore(t, malformedPath)
	if _, err := malformed.db.Exec(`DROP INDEX idx_page_revisions_request`); err != nil {
		t.Fatalf("drop malformed marker index: %v", err)
	}
	if _, err := malformed.db.Exec(`DROP TABLE page_assets`); err != nil {
		t.Fatalf("drop malformed table: %v", err)
	}
	if _, err := malformed.db.Exec(`CREATE TABLE page_assets (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create malformed table: %v", err)
	}
	if _, err := malformed.db.Exec(`
		UPDATE store_schema_migrations SET version=0
		WHERE name='page_platform'`); err != nil {
		t.Fatalf("reset malformed version: %v", err)
	}
	if err := malformed.Close(); err != nil {
		t.Fatal(err)
	}

	healthy := openMigrationIsolationStore(t, healthyPath)
	marker := createPagePlatformTestPost(t, healthy, "healthy-site-marker", "Healthy site")
	dropPagePlatformSchema(t, healthy)
	if err := healthy.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(futurePath); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("future site must fail closed, got %v", err)
	}
	assertMigrationIsolationMarker(t, futurePath, pagePlatformSchemaVersion+1, false, false)

	if _, err := Open(malformedPath); err == nil || !strings.Contains(err.Error(), "页面平台迁移失败") {
		t.Fatalf("malformed site must fail closed, got %v", err)
	}
	// The failed site's migration transaction must not repair its marker objects.
	assertMigrationIsolationMarker(t, malformedPath, 0, true, false)

	upgraded, err := Open(healthyPath)
	if err != nil {
		t.Fatalf("healthy site should migrate independently: %v", err)
	}
	defer upgraded.Close()
	var version, assets, requestIndex int
	if err := upgraded.db.QueryRow(`
		SELECT version FROM store_schema_migrations WHERE name='page_platform'`,
	).Scan(&version); err != nil {
		t.Fatalf("healthy migration version: %v", err)
	}
	if err := upgraded.db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type='table' AND name='page_assets'`,
	).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type='index' AND name='idx_page_revisions_request'`,
	).Scan(&requestIndex); err != nil {
		t.Fatal(err)
	}
	if version != pagePlatformSchemaVersion || assets != 1 || requestIndex != 1 {
		t.Fatalf("healthy site incomplete: version=%d assets=%d index=%d", version, assets, requestIndex)
	}
	if got, err := upgraded.GetPostByID(marker.ID); err != nil || got == nil || got.Title != marker.Title {
		t.Fatalf("healthy legacy content changed: post=%+v err=%v", got, err)
	}

	// Migrating the healthy database must not mutate either failed database.
	assertMigrationIsolationMarker(t, futurePath, pagePlatformSchemaVersion+1, false, false)
	assertMigrationIsolationMarker(t, malformedPath, 0, true, false)
}

func openMigrationIsolationStore(t *testing.T, path string) *Store {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create site directory %s: %v", path, err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return st
}

func assertMigrationIsolationMarker(
	t *testing.T,
	path string,
	wantVersion int,
	wantAssetsTable bool,
	wantRequestIndex bool,
) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, assets, requestIndex int
	if err := db.QueryRow(`
		SELECT version FROM store_schema_migrations WHERE name='page_platform'`,
	).Scan(&version); err != nil {
		t.Fatalf("read %s version: %v", path, err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type='table' AND name='page_assets'`,
	).Scan(&assets); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type='index' AND name='idx_page_revisions_request'`,
	).Scan(&requestIndex); err != nil {
		t.Fatal(err)
	}
	if version != wantVersion || (assets == 1) != wantAssetsTable || (requestIndex == 1) != wantRequestIndex {
		t.Fatalf(
			"%s marker changed: version=%d assets=%d request_index=%d",
			path,
			version,
			assets,
			requestIndex,
		)
	}
}
