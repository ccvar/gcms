package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestSetPilotAdminPasswordUpdatesBothStoresAndRevokesSessions(t *testing.T) {
	dir := t.TempDir()
	cmsPath := filepath.Join(dir, "cms.db")
	systemPath := filepath.Join(dir, "system.db")
	st, err := store.Open(cmsPath)
	if err != nil {
		t.Fatalf("open site store: %v", err)
	}
	user, _ := st.GetSetting("admin_user")
	hash, _ := st.GetSetting("admin_password_hash")
	if err := st.CreateAdminSession("site-token", user, "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create site session: %v", err)
	}
	ps, err := platform.Open(systemPath)
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	if err := ps.BootstrapDefaultSite(platform.DefaultSiteBootstrap{
		Slug: "main", Name: "Main", DBPath: cmsPath, AdminUser: user, AdminPasswordHash: hash,
	}); err != nil {
		t.Fatalf("bootstrap platform: %v", err)
	}
	if err := ps.CreateAdminSession("platform-token", user, "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create platform session: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close site store: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("close platform store: %v", err)
	}

	const password = "A-safe-new-password-2026!"
	var output bytes.Buffer
	if err := setPilotAdminPassword(cmsPath, systemPath, strings.NewReader(password), &output); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if !strings.Contains(output.String(), "PILOT_GCMS_PASSWORD_UPDATED\t1") {
		t.Fatalf("unexpected output: %q", output.String())
	}
	if strings.Contains(output.String(), password) {
		t.Fatal("command output must not expose the password")
	}

	st, err = store.Open(cmsPath)
	if err != nil {
		t.Fatalf("reopen site store: %v", err)
	}
	defer st.Close()
	siteHash, _ := st.GetSetting("admin_password_hash")
	if err := bcrypt.CompareHashAndPassword([]byte(siteHash), []byte(password)); err != nil {
		t.Fatalf("site password not updated: %v", err)
	}
	if _, ok, err := st.GetAdminSession("site-token"); err != nil || ok {
		t.Fatalf("site session was not revoked: ok=%v err=%v", ok, err)
	}

	ps, err = platform.Open(systemPath)
	if err != nil {
		t.Fatalf("reopen platform store: %v", err)
	}
	defer ps.Close()
	_, platformHash, err := ps.GetAdminCredentials()
	if err != nil {
		t.Fatalf("read platform credentials: %v", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(platformHash), []byte(password)); err != nil {
		t.Fatalf("platform password not updated: %v", err)
	}
	if _, ok, err := ps.GetAdminSession("platform-token"); err != nil || ok {
		t.Fatalf("platform session was not revoked: ok=%v err=%v", ok, err)
	}
}

func TestReadPilotAdminPasswordValidation(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "short", value: "short"},
		{name: "default", value: store.DefaultAdminPassword},
		{name: "newline", value: "valid-pass\nword"},
		{name: "nul", value: "valid-pass\x00word"},
		{name: "too many bytes", value: strings.Repeat("密", 25)},
		{name: "too many ascii bytes", value: strings.Repeat("x", 73)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if password, err := readPilotAdminPassword(strings.NewReader(tc.value)); err == nil {
				clearBytes(password)
				t.Fatal("expected validation error")
			}
		})
	}
	password, err := readPilotAdminPassword(strings.NewReader("valid-password\r\n"))
	if err != nil {
		t.Fatalf("read valid password: %v", err)
	}
	defer clearBytes(password)
	if string(password) != "valid-password" {
		t.Fatalf("password = %q", password)
	}
}
