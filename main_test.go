package main

import (
	"path/filepath"
	"testing"

	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func TestReadPilotAdminCredentialsPrefersPlatformAndFallsBackToSite(t *testing.T) {
	dir := t.TempDir()
	cmsPath := filepath.Join(dir, "cms.db")
	systemPath := filepath.Join(dir, "system.db")
	st, err := store.Open(cmsPath)
	if err != nil {
		t.Fatalf("open site store: %v", err)
	}
	defer st.Close()
	user, _ := st.GetSetting("admin_user")
	hash, _ := st.GetSetting("admin_password_hash")

	ps, err := platform.Open(systemPath)
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	if err := ps.BootstrapDefaultSite(platform.DefaultSiteBootstrap{
		Slug: "main", Name: "Main", DBPath: cmsPath, AdminUser: user, AdminPasswordHash: hash,
	}); err != nil {
		t.Fatalf("bootstrap platform: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("close platform store: %v", err)
	}

	gotUser, gotHash := readPilotAdminCredentials(systemPath, cmsPath)
	if gotUser != "admin" || !store.IsDefaultAdminPasswordHash(gotHash) {
		t.Fatalf("default credentials = %q/default:%v", gotUser, store.IsDefaultAdminPasswordHash(gotHash))
	}

	customHash, err := bcrypt.GenerateFromPassword([]byte("changed-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash custom password: %v", err)
	}
	ps, err = platform.Open(systemPath)
	if err != nil {
		t.Fatalf("reopen platform store: %v", err)
	}
	if err := ps.SetAdminPasswordHash("admin", string(customHash)); err != nil {
		t.Fatalf("set platform password: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("close updated platform store: %v", err)
	}
	_, gotHash = readPilotAdminCredentials(systemPath, cmsPath)
	if store.IsDefaultAdminPasswordHash(gotHash) {
		t.Fatal("platform password should take precedence over the legacy site hash")
	}

	gotUser, gotHash = readPilotAdminCredentials(filepath.Join(dir, "missing.db"), cmsPath)
	if gotUser != "admin" || !store.IsDefaultAdminPasswordHash(gotHash) {
		t.Fatal("missing platform database should fall back to site credentials")
	}
}

func TestPilotStatusFieldIsSingleLine(t *testing.T) {
	if got := pilotStatusField("ad\tmin\r\nuser"); got != "ad min  user" {
		t.Fatalf("pilotStatusField = %q", got)
	}
}
