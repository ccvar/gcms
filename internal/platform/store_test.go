package platform

import (
	"path/filepath"
	"testing"
	"time"

	"cms.ccvar.com/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func TestBootstrapDefaultSiteAndPlatformSession(t *testing.T) {
	dir := t.TempDir()
	hashBytes, err := bcrypt.GenerateFromPassword([]byte(store.DefaultAdminPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	ps, err := Open(filepath.Join(dir, "system.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	if err := ps.BootstrapDefaultSite(DefaultSiteBootstrap{
		Slug:                        "main",
		Name:                        "Main Site",
		DBPath:                      filepath.Join(dir, "cms.db"),
		UploadDir:                   filepath.Join(dir, "uploads"),
		AdminUser:                   "admin",
		AdminPasswordHash:           string(hashBytes),
		ManagementAutomationEnabled: true,
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	site, err := ps.DefaultSite()
	if err != nil {
		t.Fatalf("default site: %v", err)
	}
	if site.Slug != "main" || !site.IsDefault || !site.ManagementAutomationEnabled {
		t.Fatalf("default site mismatch: %#v", site)
	}
	user, hash, err := ps.GetAdminCredentials()
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	if user != "admin" || hash == "" {
		t.Fatalf("credentials = %q/%q", user, hash)
	}
	if !ps.IsDefaultPassword() {
		t.Fatalf("default password should be detected")
	}

	expires := time.Now().Add(time.Hour)
	if err := ps.CreateAdminSession("token", "admin", "csrf", expires); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sess, ok, err := ps.GetAdminSession("token")
	if err != nil || !ok {
		t.Fatalf("get session: ok=%v err=%v", ok, err)
	}
	if sess.User != "admin" || sess.CSRF != "csrf" {
		t.Fatalf("session mismatch: %#v", sess)
	}
	if sess.CurrentSiteID != site.ID {
		t.Fatalf("current site id = %d, want %d", sess.CurrentSiteID, site.ID)
	}
	if err := ps.SetAdminSessionSite("token", site.ID); err != nil {
		t.Fatalf("set current site: %v", err)
	}
	if err := ps.DismissAdminPasswordWarning("token"); err != nil {
		t.Fatalf("dismiss warning: %v", err)
	}
	sess, ok, err = ps.GetAdminSession("token")
	if err != nil || !ok || !sess.PwDismissed {
		t.Fatalf("dismissed session: %#v ok=%v err=%v", sess, ok, err)
	}
}
