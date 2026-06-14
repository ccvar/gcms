package i18n

import "testing"

func TestAdminTrFallbacks(t *testing.T) {
	m := New()

	en := m.AdminTr("en", nil)
	if got := en.T("admin.nav.posts", "文章"); got != "Posts" {
		t.Fatalf("english catalog = %q, want Posts", got)
	}
	if got := en.T("missing.key", "当前中文"); got != "当前中文" {
		t.Fatalf("missing key fallback = %q, want supplied Chinese fallback", got)
	}

	fr := m.AdminTr("fr", nil)
	if got := fr.T("admin.nav.posts", "文章"); got != "文章" {
		t.Fatalf("missing catalog fallback = %q, want Chinese", got)
	}

	custom := m.AdminTr("en", map[string]string{"admin.nav.posts": "Articles"})
	if got := custom.T("admin.nav.posts", "文章"); got != "Articles" {
		t.Fatalf("override = %q, want Articles", got)
	}
}
