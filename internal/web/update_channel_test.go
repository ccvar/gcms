package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestVersionGreaterUsesSemverPrereleaseOrdering(t *testing.T) {
	tests := []struct {
		latest  string
		current string
		want    bool
	}{
		{"1.3.58", "1.3.57", true},
		{"v1.4.0-preview.2", "1.4.0-preview.1", true},
		{"1.4.0-preview.10", "1.4.0-preview.2", true},
		{"1.4.0", "1.4.0-preview.10", true},
		{"1.4.0-preview.1", "1.4.0", false},
		{"1.4.0-preview.2", "1.4.0-preview.10", false},
		{"1.4.0+build.2", "1.4.0+build.1", false},
		{"1.3.58", "1.3.58", false},
	}
	for _, tt := range tests {
		t.Run(tt.latest+"_from_"+tt.current, func(t *testing.T) {
			if got := versionGreater(tt.latest, tt.current); got != tt.want {
				t.Fatalf("versionGreater(%q, %q) = %v, want %v", tt.latest, tt.current, got, tt.want)
			}
		})
	}
}

func TestUpdateManifestURLUsesIndependentChannels(t *testing.T) {
	t.Setenv("GCMS_UPDATE_URL", "https://stable.example/manifest.json")
	t.Setenv("GCMS_PREVIEW_UPDATE_URL", "https://preview.example/manifest.json")
	current := currentUpdateInfo().Current

	if got := updateManifestURLForChannel(current, updateChannelStable); got != "https://stable.example/manifest.json" {
		t.Fatalf("stable manifest = %q", got)
	}
	if got := updateManifestURLForChannel(current, updateChannelPreview); got != "https://preview.example/manifest.json" {
		t.Fatalf("preview manifest = %q", got)
	}
}

func TestUpgradeCommandReceivesSelectedManifest(t *testing.T) {
	args := upgradeCommandArgs("/srv/gcms", "1.4.0-preview.2", "https://preview.example/manifest.json")
	got := strings.Join(args, "\n")
	for _, want := range []string{
		"/srv/gcms/scripts/cms.sh",
		"upgrade",
		"1.4.0-preview.2",
		"https://preview.example/manifest.json",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("upgrade args %q missing %q", args, want)
		}
	}
}

func TestAdminPreviewActivationRequiresLoginAndCode(t *testing.T) {
	code := "gcms-preview-activation-test-code-32-bytes"
	sum := sha256.Sum256([]byte(code))
	t.Setenv("GCMS_PREVIEW_ACTIVATION_CODE_HASH", hex.EncodeToString(sum[:]))

	s, h, ps, _, _ := setupPlatformAutomation(t)
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("changed-admin-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := ps.SetAdminPasswordHash("admin", string(passwordHash)); err != nil {
		t.Fatalf("set non-default password: %v", err)
	}

	unauthenticated := httptest.NewRequest(
		http.MethodGet,
		"https://platform.test/admin/pre-active?code="+code,
		nil,
	)
	unauthenticatedResponse := httptest.NewRecorder()
	h.ServeHTTP(unauthenticatedResponse, unauthenticated)
	if unauthenticatedResponse.Code != http.StatusSeeOther ||
		unauthenticatedResponse.Header().Get("Location") != "/admin/login" {
		t.Fatalf("unauthenticated activation = %d location=%q", unauthenticatedResponse.Code, unauthenticatedResponse.Header().Get("Location"))
	}
	if got := s.updateChannel(); got != updateChannelStable {
		t.Fatalf("unauthenticated request changed channel to %q", got)
	}

	token, err := s.sess.create("admin")
	if err != nil {
		t.Fatalf("create admin session: %v", err)
	}
	request := func(value, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "https://platform.test"+path+"?code="+value, nil)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	wrong := request("wrong-preview-activation-code", "/admin/pre-active")
	if wrong.Code != http.StatusNotFound || s.updateChannel() != updateChannelStable {
		t.Fatalf("wrong activation = %d channel=%q", wrong.Code, s.updateChannel())
	}

	activated := request(code, "/admin/pre-active")
	if activated.Code != http.StatusSeeOther ||
		activated.Header().Get("Location") != "/admin/updates?preview=active" ||
		s.updateChannel() != updateChannelPreview {
		t.Fatalf("activation = %d location=%q channel=%q", activated.Code, activated.Header().Get("Location"), s.updateChannel())
	}
	if activated.Header().Get("Cache-Control") != "no-store" ||
		activated.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("activation privacy headers = %v", activated.Header())
	}
	if got, ok, err := ps.LookupSetting(updateChannelSetting); err != nil || !ok || got != updateChannelPreview {
		t.Fatalf("platform update channel = %q ok=%v err=%v", got, ok, err)
	}

	left := request(code, "/admin/pre-stable")
	if left.Code != http.StatusSeeOther ||
		left.Header().Get("Location") != "/admin/updates?preview=left" ||
		s.updateChannel() != updateChannelStable {
		t.Fatalf("leave preview = %d location=%q channel=%q", left.Code, left.Header().Get("Location"), s.updateChannel())
	}
}
