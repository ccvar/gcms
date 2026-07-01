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

func TestPublicTrFallbacksAndNewLocaleCatalogs(t *testing.T) {
	m := New()

	ja := m.Tr("ja", "ja")
	if got := ja.T("home.cta_start"); got != "开始阅读" {
		t.Fatalf("missing public catalog fallback = %q, want Chinese fallback", got)
	}

	tests := []struct {
		code string
		key  string
		want string
	}{
		{"vi", "home.cta_start", "Bắt đầu đọc"},
		{"vi", "footer.content", "Nội dung"},
		{"id", "home.cta_start", "Mulai membaca"},
		{"id", "footer.content", "Konten"},
		{"th", "home.cta_start", "เริ่มอ่าน"},
		{"th", "footer.content", "เนื้อหา"},
	}
	for _, tt := range tests {
		tr := m.Tr(tt.code, tt.code)
		if got := tr.T(tt.key); got != tt.want {
			t.Fatalf("%s %s = %q, want %q", tt.code, tt.key, got, tt.want)
		}
	}
}

func TestNewPublicLocaleCatalogsCoverEnglishKeys(t *testing.T) {
	m := New()
	en := m.cats["en"]
	for _, code := range []string{"vi", "id", "th"} {
		cat := m.cats[code]
		if len(cat) == 0 {
			t.Fatalf("%s catalog missing", code)
		}
		for key := range en {
			if cat[key] == "" {
				t.Fatalf("%s catalog missing key %q", code, key)
			}
		}
	}
}

func TestPublicCatalogOverrides(t *testing.T) {
	m := New()
	m.LoadCatalogOverrides(`{"id":{"home.cta_start":"Baca sekarang","footer.about":"Tentang kami"}}`)

	tr := m.Tr("id", "zh")
	if got := tr.T("home.cta_start"); got != "Baca sekarang" {
		t.Fatalf("override home.cta_start = %q", got)
	}
	if got := tr.T("footer.about"); got != "Tentang kami" {
		t.Fatalf("override footer.about = %q", got)
	}
	if got := m.CatalogSource("id"); got != "custom" {
		t.Fatalf("catalog source = %q, want custom", got)
	}

	m.LoadCatalogOverrides(`{}`)
	if got := m.Tr("id", "zh").T("home.cta_start"); got != "Mulai membaca" {
		t.Fatalf("cleared override fallback = %q, want built-in Indonesian", got)
	}
	if got := m.CatalogSource("id"); got != "builtin" {
		t.Fatalf("catalog source after clear = %q, want builtin", got)
	}
}
