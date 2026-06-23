package web

import (
	"path/filepath"
	"strings"
	"testing"

	"cms.ccvar.com/internal/i18n"
	"cms.ccvar.com/internal/store"
)

func TestMenuURLMatchesCurrent(t *testing.T) {
	tests := []struct {
		name        string
		menuURL     string
		currentPath string
		currentFull string
		want        bool
	}{
		{"category exact", "/category/features", "/category/features", "/category/features", true},
		{"category sibling", "/category/start", "/category/features", "/category/features", false},
		{"root", "/", "/", "/", true},
		{"query exact", "/links?cat=tools", "/links", "/links?cat=tools", true},
		{"query differs", "/links?cat=tools", "/links", "/links?cat=design", false},
		{"external ignored", "https://example.com", "/category/features", "/category/features", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := menuURLMatchesCurrent(tt.menuURL, tt.currentPath, tt.currentFull); got != tt.want {
				t.Fatalf("menuURLMatchesCurrent(%q, %q, %q) = %v, want %v", tt.menuURL, tt.currentPath, tt.currentFull, got, tt.want)
			}
		})
	}
}

func TestBuildMenuJSONIgnoresPlaceholderURLs(t *testing.T) {
	got := buildMenuJSON(
		[]string{
			"%e8%87%aa%e5%ae%9a%e4%b9%89%e5%9c%b0%e5%9d%80",
			"/docs%20%e6%88%96%20https://example.com",
			"docs",
			"/valid",
		},
		map[string][]string{"zh": {"乱码", "占位", "无斜杠", "有效"}},
	)
	if strings.Contains(got, "乱码") || strings.Contains(got, "占位") || strings.Contains(got, "无斜杠") {
		t.Fatalf("buildMenuJSON kept invalid placeholder rows: %s", got)
	}
	if !strings.Contains(got, `"/valid"`) || !strings.Contains(got, "有效") {
		t.Fatalf("buildMenuJSON dropped valid row: %s", got)
	}
}

func TestMenuTargetOptionsIncludeContentEntrances(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "cms.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SetSetting("locales", "zh,en"); err != nil {
		t.Fatalf("set locales: %v", err)
	}
	if _, err := st.CreateCategory(&store.Category{Slug: "engineering", Name: "工程", Lang: "zh", Kind: "post", TransGroup: "cat:engineering"}); err != nil {
		t.Fatalf("create post category: %v", err)
	}
	if _, err := st.CreateCategory(&store.Category{Slug: "engineering", Name: "Engineering", Lang: "en", Kind: "post", TransGroup: "cat:engineering"}); err != nil {
		t.Fatalf("create translated post category: %v", err)
	}
	if _, err := st.CreateCategory(&store.Category{Slug: "tools", Name: "工具", Lang: "zh", Kind: "link", TransGroup: "linkcat:tools"}); err != nil {
		t.Fatalf("create link category: %v", err)
	}
	if _, err := st.CreatePost(&store.Post{Type: "page", Lang: "zh", Slug: "features", Title: "功能", Status: "published", TransGroup: "page:features"}); err != nil {
		t.Fatalf("create page: %v", err)
	}
	if _, err := st.CreatePost(&store.Post{Type: "page", Lang: "en", Slug: "features", Title: "Features", Status: "published", TransGroup: "page:features"}); err != nil {
		t.Fatalf("create translated page: %v", err)
	}
	s := &Server{store: st, i18n: i18n.New()}
	options := s.menuTargetOptions()
	byURL := map[string]MenuTargetOption{}
	for _, opt := range options {
		byURL[opt.URL] = opt
	}
	if opt := byURL["/category/engineering"]; opt.Value == "" || opt.Labels["en"] != "Engineering" {
		t.Fatalf("missing post category option: %#v", opt)
	}
	if opt := byURL["/links/cat/tools"]; opt.Value == "" || opt.Labels["zh"] != "工具" {
		t.Fatalf("missing link category option: %#v", opt)
	}
	if opt := byURL["/features"]; opt.Value == "" || opt.Labels["en"] != "Features" {
		t.Fatalf("missing page option: %#v", opt)
	}
	rows := decorateMenuRows([]MenuRow{{URL: "/links?cat=tools"}}, options)
	if len(rows) != 1 || rows[0].URL != "/links/cat/tools" || rows[0].TargetValue == "__custom__" {
		t.Fatalf("legacy link query was not recognized as link category: %#v", rows)
	}
}
