package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAdminSessionPasswordChangeRequirement(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.CreateAdminSession("token", "admin", "csrf", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sess, ok, err := s.GetAdminSession("token")
	if err != nil || !ok {
		t.Fatalf("get session: %#v ok=%v err=%v", sess, ok, err)
	}
	if sess.MustChangePassword {
		t.Fatal("new session unexpectedly requires a password change")
	}

	if err := s.RequireAdminPasswordChange("token"); err != nil {
		t.Fatalf("require password change: %v", err)
	}
	sess, ok, err = s.GetAdminSession("token")
	if err != nil || !ok || !sess.MustChangePassword {
		t.Fatalf("required password-change session: %#v ok=%v err=%v", sess, ok, err)
	}
}
