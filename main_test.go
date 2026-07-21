package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
	"cms.ccvar.com/internal/store"
	"cms.ccvar.com/internal/web"
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

func TestIssuePilotAssistantKeyCreatesReusesAndRotatesOneFullAccessKey(t *testing.T) {
	dir := t.TempDir()
	systemPath := filepath.Join(dir, "system.db")
	ps, err := platform.Open(systemPath)
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("close platform store: %v", err)
	}

	var first bytes.Buffer
	if err := issuePilotAssistantKey(systemPath, strings.NewReader(""), &first); err != nil {
		t.Fatalf("issue first key: %v", err)
	}
	firstToken := strings.TrimSpace(strings.TrimPrefix(first.String(), "PILOT_GCMS_ASSISTANT_KEY\t"))
	if !strings.HasPrefix(firstToken, "gcmsp_") {
		t.Fatalf("unexpected first output %q", first.String())
	}

	ps, err = platform.Open(systemPath)
	if err != nil {
		t.Fatalf("reopen platform store: %v", err)
	}
	key, ok, err := ps.GetPlatformKeyByToken(firstToken)
	if err != nil || !ok {
		t.Fatalf("first key missing: ok=%v err=%v", ok, err)
	}
	if key.Name != pilotAssistantName || key.MembershipMode != platform.KeyMembershipAll {
		t.Fatalf("assistant key metadata = %#v", key)
	}
	for _, scope := range web.PilotAssistantAutomationScopes() {
		if !strings.Contains(","+key.Scopes+",", ","+scope+",") {
			t.Fatalf("assistant key missing scope %q", scope)
		}
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("close platform store: %v", err)
	}

	var reused bytes.Buffer
	if err := issuePilotAssistantKey(systemPath, strings.NewReader(firstToken), &reused); err != nil {
		t.Fatalf("reuse current key: %v", err)
	}
	if strings.TrimSpace(reused.String()) != "PILOT_GCMS_ASSISTANT_KEY_REUSED\t1" {
		t.Fatalf("unexpected reuse output %q", reused.String())
	}

	var rotated bytes.Buffer
	if err := issuePilotAssistantKey(systemPath, strings.NewReader("gcmsp_stale"), &rotated); err != nil {
		t.Fatalf("rotate stale key: %v", err)
	}
	rotatedToken := strings.TrimSpace(strings.TrimPrefix(rotated.String(), "PILOT_GCMS_ASSISTANT_KEY\t"))
	if rotatedToken == firstToken || !strings.HasPrefix(rotatedToken, "gcmsp_") {
		t.Fatalf("unexpected rotated output %q", rotated.String())
	}
	ps, err = platform.Open(systemPath)
	if err != nil {
		t.Fatalf("reopen rotated platform store: %v", err)
	}
	defer ps.Close()
	keys, err := ps.ListPlatformKeys()
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("got %d keys, want one idempotent assistant record", len(keys))
	}
	if _, ok, _ := ps.GetPlatformKeyByToken(firstToken); ok {
		t.Fatal("old key should be invalid after rotation")
	}
	if _, ok, err := ps.GetPlatformKeyByToken(rotatedToken); err != nil || !ok {
		t.Fatalf("rotated key missing: ok=%v err=%v", ok, err)
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

func TestSetPilotAdminPasswordRefusesChangedCredentialsWithoutRevokingSessions(t *testing.T) {
	dir := t.TempDir()
	cmsPath := filepath.Join(dir, "cms.db")
	systemPath := filepath.Join(dir, "system.db")
	const currentPassword = "already-changed-password"
	currentHash, err := bcrypt.GenerateFromPassword([]byte(currentPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash current password: %v", err)
	}

	st, err := store.Open(cmsPath)
	if err != nil {
		t.Fatalf("open site store: %v", err)
	}
	user, _ := st.GetSetting("admin_user")
	if err := st.SetSetting("admin_password_hash", string(currentHash)); err != nil {
		t.Fatalf("set site password: %v", err)
	}
	if err := st.CreateAdminSession("site-token", user, "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create site session: %v", err)
	}
	ps, err := platform.Open(systemPath)
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	if err := ps.BootstrapDefaultSite(platform.DefaultSiteBootstrap{
		Slug: "main", Name: "Main", DBPath: cmsPath, AdminUser: user, AdminPasswordHash: string(currentHash),
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

	const attemptedPassword = "Pilot-must-not-overwrite-2026!"
	var output bytes.Buffer
	err = setPilotAdminPassword(cmsPath, systemPath, strings.NewReader(attemptedPassword), &output)
	if err == nil || !strings.Contains(err.Error(), "仅支持设置首次安装的初始密码") {
		t.Fatalf("expected changed-password refusal, got %v", err)
	}
	if output.Len() != 0 || strings.Contains(output.String(), attemptedPassword) {
		t.Fatalf("refusal output must stay empty, got %q", output.String())
	}

	st, err = store.Open(cmsPath)
	if err != nil {
		t.Fatalf("reopen site store: %v", err)
	}
	defer st.Close()
	siteHash, _ := st.GetSetting("admin_password_hash")
	if err := bcrypt.CompareHashAndPassword([]byte(siteHash), []byte(currentPassword)); err != nil {
		t.Fatalf("site password was overwritten: %v", err)
	}
	if _, ok, err := st.GetAdminSession("site-token"); err != nil || !ok {
		t.Fatalf("site session should remain: ok=%v err=%v", ok, err)
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
	if err := bcrypt.CompareHashAndPassword([]byte(platformHash), []byte(currentPassword)); err != nil {
		t.Fatalf("platform password was overwritten: %v", err)
	}
	if _, ok, err := ps.GetAdminSession("platform-token"); err != nil || !ok {
		t.Fatalf("platform session should remain: ok=%v err=%v", ok, err)
	}
}

func TestVerifyPilotAdminPasswordUsesAuthoritativeCredentialsWithoutLeakingSecret(t *testing.T) {
	dir := t.TempDir()
	cmsPath := filepath.Join(dir, "cms.db")
	systemPath := filepath.Join(dir, "system.db")
	st, err := store.Open(cmsPath)
	if err != nil {
		t.Fatalf("open site store: %v", err)
	}
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
	if err := st.Close(); err != nil {
		t.Fatalf("close site store: %v", err)
	}
	if err := ps.Close(); err != nil {
		t.Fatalf("close platform store: %v", err)
	}

	const password = "GCMS-confirmation-2026!"
	if err := setPilotAdminPassword(cmsPath, systemPath, strings.NewReader(password), &bytes.Buffer{}); err != nil {
		t.Fatalf("set password: %v", err)
	}
	var output bytes.Buffer
	if err := verifyPilotAdminPassword(cmsPath, systemPath, strings.NewReader(password), &output); err != nil {
		t.Fatalf("verify password: %v", err)
	}
	if !strings.Contains(output.String(), "PILOT_GCMS_PASSWORD_VERIFIED\t1") {
		t.Fatalf("unexpected output: %q", output.String())
	}
	if strings.Contains(output.String(), password) {
		t.Fatal("verification output must not expose the password")
	}

	output.Reset()
	if err := verifyPilotAdminPassword(cmsPath, systemPath, strings.NewReader("wrong-password"), &output); err == nil || !strings.Contains(err.Error(), "密码不正确") {
		t.Fatalf("expected wrong-password error, got %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("failed verification output must stay empty, got %q", output.String())
	}
}

func TestVerifyPilotAdminPasswordRejectsDefaultPassword(t *testing.T) {
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
	defer ps.Close()
	if err := ps.BootstrapDefaultSite(platform.DefaultSiteBootstrap{
		Slug: "main", Name: "Main", DBPath: cmsPath, AdminUser: user, AdminPasswordHash: hash,
	}); err != nil {
		t.Fatalf("bootstrap platform: %v", err)
	}
	if err := verifyPilotAdminPassword(cmsPath, systemPath, strings.NewReader(store.DefaultAdminPassword), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "默认密码") {
		t.Fatalf("expected default-password refusal, got %v", err)
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

func TestReadPilotConfirmationPasswordValidation(t *testing.T) {
	for _, value := range []string{"", "bad\npassword", "bad\x00password", strings.Repeat("x", 73)} {
		if password, err := readPilotConfirmationPassword(strings.NewReader(value)); err == nil {
			clearBytes(password)
			t.Fatalf("expected validation error for %q", value)
		}
	}
	password, err := readPilotConfirmationPassword(strings.NewReader("legacy\r\n"))
	if err != nil {
		t.Fatalf("read legacy password: %v", err)
	}
	defer clearBytes(password)
	if string(password) != "legacy" {
		t.Fatalf("password = %q", password)
	}
}
