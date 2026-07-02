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
	if sess.CurrentSiteID != 0 {
		t.Fatalf("new session current site id = %d, want 0", sess.CurrentSiteID)
	}
	if err := ps.SetAdminSessionSite("token", site.ID); err != nil {
		t.Fatalf("set current site: %v", err)
	}
	sess, ok, err = ps.GetAdminSession("token")
	if err != nil || !ok {
		t.Fatalf("get selected session: ok=%v err=%v", ok, err)
	}
	if sess.CurrentSiteID != site.ID {
		t.Fatalf("selected current site id = %d, want %d", sess.CurrentSiteID, site.ID)
	}
	if err := ps.DismissAdminPasswordWarning("token"); err != nil {
		t.Fatalf("dismiss warning: %v", err)
	}
	sess, ok, err = ps.GetAdminSession("token")
	if err != nil || !ok || !sess.PwDismissed {
		t.Fatalf("dismissed session: %#v ok=%v err=%v", sess, ok, err)
	}

	other, err := ps.CreateSite("blog", "Blog", filepath.Join(dir, "blog.db"), filepath.Join(dir, "blog-uploads"), true)
	if err != nil {
		t.Fatalf("create site: %v", err)
	}
	if err := ps.SetSiteName(other.ID, "Renamed Blog"); err != nil {
		t.Fatalf("rename site: %v", err)
	}
	renamed, ok, err := ps.GetSite(other.ID)
	if err != nil || !ok {
		t.Fatalf("get renamed site: ok=%v err=%v", ok, err)
	}
	if renamed.Name != "Renamed Blog" {
		t.Fatalf("renamed site name = %q, want Renamed Blog", renamed.Name)
	}
	if err := ps.AddSiteDomain(other.ID, "https", "blog.example.com", true, true); err != nil {
		t.Fatalf("add domain: %v", err)
	}
	domains, err := ps.SiteDomains()
	if err != nil {
		t.Fatalf("site domains: %v", err)
	}
	if len(domains) != 1 || domains[0].SiteID != other.ID || !domains[0].IsPrimary {
		t.Fatalf("domains mismatch: %#v", domains)
	}
	if err := ps.SetSiteAutomation(other.ID, false); err != nil {
		t.Fatalf("set automation: %v", err)
	}
	if err := ps.SetDefaultSite(other.ID); err != nil {
		t.Fatalf("set default site: %v", err)
	}
	def, err := ps.DefaultSite()
	if err != nil {
		t.Fatalf("default site after switch: %v", err)
	}
	if def.ID != other.ID {
		t.Fatalf("default site id = %d, want %d", def.ID, other.ID)
	}
	if err := ps.SetSiteStatus(site.ID, "disabled"); err != nil {
		t.Fatalf("disable old default: %v", err)
	}
}

func TestReplaceSiteDomains(t *testing.T) {
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
		Slug: "main", Name: "Main", DBPath: filepath.Join(dir, "cms.db"),
		UploadDir: filepath.Join(dir, "uploads"), AdminUser: "admin",
		AdminPasswordHash: string(hashBytes),
	}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	site, err := ps.DefaultSite()
	if err != nil {
		t.Fatalf("default site: %v", err)
	}

	byHost := func() map[string]*SiteDomain {
		all, err := ps.SiteDomains()
		if err != nil {
			t.Fatalf("list domains: %v", err)
		}
		m := map[string]*SiteDomain{}
		for _, d := range all {
			if d.SiteID == site.ID {
				m[d.Host] = d
			}
		}
		return m
	}

	// Primary + one redirecting alias + one independent alias.
	if err := ps.ReplaceSiteDomains(site.ID, []SiteDomainSpec{
		{Scheme: "https", Host: "a.com", Primary: true},
		{Scheme: "https", Host: "www.a.com", Redirect: true},
		{Scheme: "https", Host: "b.com", Redirect: false},
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	m := byHost()
	if len(m) != 3 {
		t.Fatalf("want 3 domains, got %d", len(m))
	}
	if !m["a.com"].IsPrimary || m["a.com"].RedirectToPrimary {
		t.Fatalf("a.com should be primary non-redirect: %#v", m["a.com"])
	}
	if m["www.a.com"].IsPrimary || !m["www.a.com"].RedirectToPrimary {
		t.Fatalf("www.a.com should be redirecting alias: %#v", m["www.a.com"])
	}
	if m["b.com"].IsPrimary || m["b.com"].RedirectToPrimary {
		t.Fatalf("b.com should be independent alias: %#v", m["b.com"])
	}

	// Replace with a different set — old aliases must be gone.
	if err := ps.ReplaceSiteDomains(site.ID, []SiteDomainSpec{
		{Scheme: "https", Host: "a.com", Primary: true},
		{Scheme: "https", Host: "c.com", Redirect: true},
	}); err != nil {
		t.Fatalf("replace 2: %v", err)
	}
	m = byHost()
	if len(m) != 2 || m["www.a.com"] != nil || m["b.com"] != nil || m["c.com"] == nil {
		t.Fatalf("replace did not swap domain set: %#v", m)
	}

	// Empty spec clears everything.
	if err := ps.ReplaceSiteDomains(site.ID, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if m = byHost(); len(m) != 0 {
		t.Fatalf("want 0 domains after clear, got %d", len(m))
	}
}

func TestGoogleOAuthStateAndAccounts(t *testing.T) {
	dir := t.TempDir()
	ps, err := Open(filepath.Join(dir, "system.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	if err := ps.CreateGoogleOAuthState("state-1", "ga", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("create oauth state: %v", err)
	}
	service, ok, err := ps.ConsumeGoogleOAuthState("state-1")
	if err != nil || !ok {
		t.Fatalf("consume oauth state: service=%q ok=%v err=%v", service, ok, err)
	}
	if service != GoogleServiceAnalytics {
		t.Fatalf("oauth service = %q, want %q", service, GoogleServiceAnalytics)
	}
	if service, ok, err = ps.ConsumeGoogleOAuthState("state-1"); err != nil || ok || service != "" {
		t.Fatalf("state should be single-use: service=%q ok=%v err=%v", service, ok, err)
	}
	if err := ps.CreateGoogleOAuthState("expired", GoogleServiceSearchConsole, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("create expired oauth state: %v", err)
	}
	if service, ok, err = ps.ConsumeGoogleOAuthState("expired"); err != nil || ok || service != "" {
		t.Fatalf("expired state should be rejected: service=%q ok=%v err=%v", service, ok, err)
	}

	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := ps.UpsertGoogleAccount(&GoogleAccount{
		Service:         GoogleServiceAnalytics,
		GoogleAccountID: "google-1",
		Email:           "old@example.com",
		Name:            "Old Name",
		Picture:         "https://example.com/old.png",
		Scopes:          "openid profile",
		AccessToken:     "access-1",
		RefreshToken:    "refresh-1",
		TokenExpiry:     expiry,
	}); err != nil {
		t.Fatalf("upsert google account: %v", err)
	}
	if err := ps.UpsertGoogleAccount(&GoogleAccount{
		Service:         "google_analytics",
		GoogleAccountID: "google-1",
		Email:           "new@example.com",
		Name:            "New Name",
		Picture:         "https://example.com/new.png",
		Scopes:          "openid profile analytics",
		AccessToken:     "access-2",
		TokenExpiry:     expiry.Add(time.Hour),
	}); err != nil {
		t.Fatalf("upsert google account without refresh token: %v", err)
	}
	accounts, err := ps.GoogleAccounts(GoogleServiceAnalytics)
	if err != nil {
		t.Fatalf("list google accounts: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("got %d google accounts, want 1", len(accounts))
	}
	acc := accounts[0]
	if acc.Email != "new@example.com" || acc.Name != "New Name" || acc.AccessToken != "access-2" {
		t.Fatalf("account fields were not updated: %#v", acc)
	}
	if acc.RefreshToken != "refresh-1" {
		t.Fatalf("refresh token = %q, want preserved refresh-1", acc.RefreshToken)
	}
	if err := ps.DeleteGoogleAccount(GoogleServiceAnalytics, "google-1"); err != nil {
		t.Fatalf("delete google account: %v", err)
	}
	accounts, err = ps.GoogleAccounts(GoogleServiceAnalytics)
	if err != nil {
		t.Fatalf("list google accounts after delete: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("got %d google accounts after delete, want 0", len(accounts))
	}
}
