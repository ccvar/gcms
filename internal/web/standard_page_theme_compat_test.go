package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

// TestStandardPageContractAcrossRegisteredThemes is a structural compatibility
// test rather than a pixel snapshot. It exercises the real public route for
// every registered theme and protects the stable standard-page data, SEO,
// shell, and responsive viewport contracts while allowing intentional visual
// design changes inside each theme.
func TestStandardPageContractAcrossRegisteredThemes(t *testing.T) {
	s := newTestPublicServer(t, "")
	const (
		slug          = "standard-page-platform-compat"
		title         = "Standard page compatibility marker"
		contentMarker = "STANDARD-PAGE-BODY-MUST-STAY-DYNAMIC"
	)
	postID, err := s.store.CreatePost(&store.Post{
		Type: "page", Slug: slug, Title: title, Excerpt: "Compatibility excerpt",
		Content: contentMarker, Status: "published", EditorMode: "markdown",
		Lang: "zh", Author: "compat-test",
	})
	if err != nil {
		t.Fatalf("create standard page: %v", err)
	}
	if project, err := s.store.GetPageProjectByPostID(postID); err != nil || project != nil {
		t.Fatalf("standard page unexpectedly owns a project: project=%+v err=%v", project, err)
	}

	for _, theme := range Themes {
		theme := theme
		t.Run(theme.ID, func(t *testing.T) {
			if err := s.store.SetSetting("theme", theme.ID); err != nil {
				t.Fatalf("set theme: %v", err)
			}
			s.clearGeneratedCaches()
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(
				rec,
				httptest.NewRequest(http.MethodGet, "https://example.test/zh/"+slug, nil),
			)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			required := []string{
				`data-theme="` + theme.ID + `"`,
				`data-theme-layout="` + layoutForTheme(theme.ID) + `"`,
				`<meta name="viewport" content="width=device-width, initial-scale=1.0">`,
				`class="content-page content-page-` + slug + `"`,
				`class="prose read"`,
				title,
				contentMarker,
				`<link rel="canonical" href="`,
				`/zh/` + slug,
				`href="/assets/css/public.css?v=`,
				`src="/assets/js/site.js?v=`,
			}
			for _, want := range required {
				if !strings.Contains(body, want) {
					t.Errorf("theme %q standard page missing contract %q", theme.ID, want)
				}
			}
			for _, forbidden := range []string{
				`data-page-mode="composition"`,
				`data-page-mode="app"`,
				`class="page-project-runtime"`,
			} {
				if strings.Contains(body, forbidden) {
					t.Errorf("theme %q standard page leaked project runtime marker %q", theme.ID, forbidden)
				}
			}
		})
	}

	if project, err := s.store.GetPageProjectByPostID(postID); err != nil || project != nil {
		t.Fatalf("theme rendering backfilled a standard page project: project=%+v err=%v", project, err)
	}
}

func TestPublicCSSRetainsSharedResponsiveStandardPageContract(t *testing.T) {
	s := newTestPublicServer(t, "")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, "https://example.test/assets/css/public.css", nil),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("public css status = %d", rec.Code)
	}
	css := rec.Body.String()
	for _, contract := range []string{
		`.wrap { width: 100%; max-width: var(--w-wide);`,
		`.read { max-width: var(--w-read);`,
		`img, svg, video { max-width: 100%; height: auto;`,
		`@media (max-width: 860px)`,
		`@media (max-width: 620px)`,
	} {
		if !strings.Contains(css, contract) {
			t.Errorf("shared responsive CSS contract missing %q", contract)
		}
	}
	if len(Themes) == 0 {
		t.Fatal("theme registry is empty")
	}
	t.Logf("responsive standard-page contract covers %s registered themes", fmt.Sprint(len(Themes)))
}
