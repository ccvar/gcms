package backup

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
	_ "modernc.org/sqlite"
)

func TestCreatePlatformBackupIncludesDatabasesUploadsAndManifest(t *testing.T) {
	dir := t.TempDir()
	systemDB := filepath.Join(dir, "system.db")
	siteDB := filepath.Join(dir, "sites", "docs", "cms.db")
	uploads := filepath.Join(dir, "sites", "docs", "uploads")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatal(err)
	}
	createSQLite(t, systemDB, "platform")
	createSQLite(t, siteDB, "site")
	if err := os.WriteFile(filepath.Join(uploads, "cover.webp"), []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec, err := CreatePlatformBackup(Options{
		BackupDir:    filepath.Join(dir, "backups"),
		SystemDBPath: systemDB,
		Sites: []*platform.Site{{
			ID: 1, Slug: "docs", Name: "Docs", Status: "enabled",
			DBPath: siteDB, UploadDir: uploads,
		}},
	})
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	if rec.Name == "" || rec.Size <= 0 || rec.SHA256 == "" || rec.Sites != 1 {
		t.Fatalf("unexpected record: %+v", rec)
	}

	zr, err := zip.OpenReader(ZipPath(filepath.Join(dir, "backups"), rec.Name))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	seen := map[string]bool{}
	var manifest Manifest
	for _, f := range zr.File {
		seen[f.Name] = true
		if f.Name == "manifest.json" {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			if err := json.NewDecoder(rc).Decode(&manifest); err != nil {
				_ = rc.Close()
				t.Fatal(err)
			}
			_ = rc.Close()
		}
	}
	for _, want := range []string{"system/system.db", "sites/docs/cms.db", "sites/docs/uploads/cover.webp", "manifest.json", "README.txt"} {
		if !seen[want] {
			t.Fatalf("backup missing %s; seen=%v", want, seen)
		}
	}
	if manifest.Kind != "platform" || len(manifest.Sites) != 1 || manifest.Sites[0].Slug != "docs" {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}

	records, err := ListRecords(filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Name != rec.Name {
		t.Fatalf("records = %+v, want %s", records, rec.Name)
	}
}

func TestApplyRetentionDeletesOlderBackups(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		name := "gcms-platform-backup-20260621-15000" + string(rune('0'+i)) + ".zip"
		path := ZipPath(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		rec := &BackupRecord{Name: name, Size: 1, SHA256: "x", CreatedAt: now.Add(time.Duration(i) * time.Minute)}
		if err := WriteRecord(dir, rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := ApplyRetention(dir, 2); err != nil {
		t.Fatal(err)
	}
	records, err := ListRecords(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Name != "gcms-platform-backup-20260621-150002.zip" || records[1].Name != "gcms-platform-backup-20260621-150001.zip" {
		t.Fatalf("records after retention = %+v", records)
	}
}

func createSQLite(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t(v TEXT); INSERT INTO t(v) VALUES(?)`, value); err != nil {
		t.Fatal(err)
	}
}
