package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestUpdateAutomationKey(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	id, err := st.CreateAutomationKey("bot", "gcms_token", "gcms_token", "posts:read,posts:write")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateAutomationKey(id, "edited bot", "links:read"); err != nil {
		t.Fatal(err)
	}
	key, ok, err := st.GetAutomationKeyByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("key not found")
	}
	if key.Name != "edited bot" || key.Scopes != "links:read" {
		t.Fatalf("updated key = (%q, %q), want edited bot and links:read", key.Name, key.Scopes)
	}

	if err := st.RevokeAutomationKey(id); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateAutomationKey(id, "revoked", "pages:read"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("updating revoked key error = %v, want sql.ErrNoRows", err)
	}
}
