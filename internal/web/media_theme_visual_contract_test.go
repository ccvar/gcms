package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type mediaThemeContract struct {
	id, bg, pageClass, heroClass string
}

// TestMediaThemeVisualContracts locks the small set of properties that make
// these three media-led families distinct from the ordinary content skins.
// This intentionally checks the shipped stylesheet and rendered home HTML:
// screenshot comparison is useful for review, but too brittle for the nine
// colour cards and their no-image fallback.
func TestMediaThemeVisualContracts(t *testing.T) {
	cssPath := filepath.Join("..", "..", "assets", "css", "public.css")
	cssBytes, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("read public CSS: %v", err)
	}
	css := string(cssBytes)

	themes := []mediaThemeContract{
		{"seamless-canvas", "#f2f0eb", "seamless-canvas-page", "sc-hero-media"},
		{"seamless-canvas-white", "#ffffff", "seamless-canvas-page", "sc-hero-media"},
		{"seamless-canvas-dark", "#121416", "seamless-canvas-page", "sc-hero-media"},
		{"night-corridor", "#f4f3f0", "night-corridor-page", "nc-hero-media"},
		{"night-corridor-white", "#ffffff", "night-corridor-page", "nc-hero-media"},
		{"night-corridor-dark", "#080a0d", "night-corridor-page", "nc-hero-media"},
		{"open-ascent", "#f5f5f2", "open-ascent-page", "oa-hero-media"},
		{"open-ascent-white", "#ffffff", "open-ascent-page", "oa-hero-media"},
		{"open-ascent-dark", "#0c1420", "open-ascent-page", "oa-hero-media"},
	}
	for _, theme := range themes {
		t.Run(theme.id, func(t *testing.T) {
			if got := themeBg(theme.id); got != theme.bg {
				t.Errorf("themeBg(%q) = %q, want %q", theme.id, got, theme.bg)
			}
			if !strings.Contains(css, `[data-theme="`+theme.id+`"]`) {
				t.Errorf("public.css has no explicit selector for %q", theme.id)
			}
		})
	}

	// The image wrapper must be transparent: its media is allowed to become the
	// continuous canvas, while the no-image variant can still use the hero's
	// own colour/gradient fallback.
	for _, heroClass := range []string{"sc-hero-media", "nc-hero-media", "oa-hero-media"} {
		cssRuleHas(t, css, "."+heroClass, "background:transparent")
		cssRuleHas(t, css, "."+heroClass, "overflow:hidden")
	}
	// The pure-white Night Corridor card must show the supplied image/SVG
	// without the dark cinematic filter. Its Hero type switches to a dark
	// token instead of relying on a grey wash for contrast.
	cssRuleHas(t, css, `:root[data-theme="night-corridor-white"]`, "--hero-ink:#151617")
	cssRuleHas(t, css, `.nc-hero-media > img`, "filter:none")
	cssRuleHas(t, css, `[data-theme="night-corridor-white"] .nc-hero-embed`, "filter:none")
	cssRuleHas(t, css, `[data-theme="night-corridor-white"] .nc-hero-index`, "background:color-mix(in srgb,var(--bg) 88%,transparent)")

	// The header is fixed while occupying the same transparent Hero canvas;
	// interior pages continue to use the ordinary solid header.
	for _, pageClass := range []string{"seamless-canvas-page", "night-corridor-page", "open-ascent-page"} {
		cssRuleHas(t, css, "html:has(."+pageClass+")", "position:fixed", "background:transparent")
	}

	// Open Ascent keeps the media full bleed without moving its centered
	// content anchors. Its CTA is plain accent text so it cannot collide with
	// the compact article-index rule below.
	cssRuleHas(t, css, ".open-ascent-page", "max-width:none")
	cssRuleHas(t, css, ".oa-action", "border-bottom:0")
	cssRuleHas(t, css, ".oa-hero-list a", "min-height:48px", "padding:6px 0")

	// Every media family exposes the same live curation inputs in its own
	// visual language. Night Corridor must not repeat the category strip that
	// is already present inside its Hero.
	templateContracts := []struct {
		file, curationClass string
	}{
		{"home_seamless_canvas.html", "sc-curation"},
		{"home_night_corridor.html", "nc-curation"},
		{"home_open_ascent.html", "oa-curation"},
	}
	for _, contract := range templateContracts {
		templatePath := filepath.Join("..", "..", "templates", "partials", contract.file)
		templateBytes, err := os.ReadFile(templatePath)
		if err != nil {
			t.Fatalf("read %s: %v", contract.file, err)
		}
		template := string(templateBytes)
		for _, marker := range []string{contract.curationClass, ".FeaturedMore", ".FeatLinks", ".Site.HomeFeatured", ".Site.HomeLinks"} {
			if !strings.Contains(template, marker) {
				t.Errorf("%s misses dynamic curation marker %q", contract.file, marker)
			}
		}
		cssRuleHas(t, css, "."+contract.curationClass, "display:grid")
	}
	nightTemplateBytes, err := os.ReadFile(filepath.Join("..", "..", "templates", "partials", "home_night_corridor.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(nightTemplateBytes), `class="nc-categories"`) {
		t.Error("Night Corridor repeats its Hero category index below the fold")
	}
	for _, forbiddenAccent := range []string{"#d84934", "#ed624b", "#ff765f"} {
		if strings.Contains(css, forbiddenAccent) {
			t.Errorf("Night Corridor still contains the harsh accent %s", forbiddenAccent)
		}
	}

	// One compact mobile block must collapse all three first screens.  The
	// individual page markers avoid accidentally satisfying this with an
	// unrelated global header breakpoint.
	mediaThemeCSS := cssAfter(css, "2026-07 融合媒体骨架")
	mobileCSS := cssBefore(cssAfter(mediaThemeCSS, "@media (max-width:760px)"), "@media (max-width:480px)")
	for _, pageClass := range []string{".seamless-canvas-page", ".night-corridor-page", ".open-ascent-page"} {
		if !strings.Contains(mobileCSS, pageClass) {
			t.Errorf("mobile CSS does not cover %s", pageClass)
		}
	}
	if !strings.Contains(mobileCSS, "min-height") {
		t.Error("mobile media-theme CSS does not constrain Hero height")
	}

	// Render the real home with an existing site-profile image. This proves
	// each family emits the media wrapper that both live pages and theme-card
	// previews now share.
	s := newTestPublicServer(t, "")
	for _, theme := range themesForPreview(themes) {
		if err := s.store.SetSetting("theme", theme.id); err != nil {
			t.Fatalf("set %s theme: %v", theme.id, err)
		}
		if err := s.store.SetSetting("hero.visual", "image"); err != nil {
			t.Fatal(err)
		}
		if err := s.store.SetSetting("hero.image", "/uploads/media-theme-contract.webp"); err != nil {
			t.Fatal(err)
		}
		s.clearGeneratedCaches()

		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/zh/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s home status = %d", theme.id, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `class="`+theme.pageClass+`"`) {
			t.Errorf("%s home misses %s", theme.id, theme.pageClass)
		}
		if !strings.Contains(body, `class="`+theme.heroClass+`"`) {
			t.Errorf("%s home misses Hero media wrapper %s", theme.id, theme.heroClass)
		}
	}
}

func TestMediaThemesHonorHeroVisualSetting(t *testing.T) {
	s := newTestPublicServer(t, "")
	const configuredImage = "/uploads/configured-hero.webp"
	for _, theme := range []string{"seamless-canvas", "night-corridor", "open-ascent"} {
		t.Run(theme, func(t *testing.T) {
			if err := s.store.SetSetting("theme", theme); err != nil {
				t.Fatal(err)
			}
			if err := s.store.SetSetting("hero.image", configuredImage); err != nil {
				t.Fatal(err)
			}

			render := func(visual string) string {
				t.Helper()
				if err := s.store.SetSetting("hero.visual", visual); err != nil {
					t.Fatal(err)
				}
				s.clearGeneratedCaches()
				rec := httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/zh/", nil))
				if rec.Code != http.StatusOK {
					t.Fatalf("visual %q status = %d", visual, rec.Code)
				}
				return rec.Body.String()
			}

			defaultBody := render("")
			if !strings.Contains(defaultBody, `class="media-content-hero-anim"`) ||
				!strings.Contains(defaultBody, `class="hero-svg hero-anim2"`) {
				t.Error("default visual does not render the configured content-theme animation")
			}
			if strings.Contains(defaultBody, configuredImage) {
				t.Error("default visual leaked the stored image instead of following hero.visual")
			}

			anim1Body := render("anim1")
			if !strings.Contains(anim1Body, `class="hero-svg"`) ||
				strings.Contains(anim1Body, `class="hero-svg hero-anim2"`) {
				t.Error("anim1 visual does not render animation 1")
			}
			if strings.Contains(anim1Body, configuredImage) {
				t.Error("anim1 visual leaked the stored image instead of following hero.visual")
			}

			imageBody := render("image")
			if !strings.Contains(imageBody, `src="`+configuredImage+`"`) {
				t.Error("image visual does not render the configured Hero image")
			}
		})
	}
}

func themesForPreview(themes []mediaThemeContract) []mediaThemeContract {
	seen := make(map[string]bool, 3)
	result := make([]mediaThemeContract, 0, 3)
	for _, theme := range themes {
		family := strings.TrimSuffix(strings.TrimSuffix(theme.id, "-white"), "-dark")
		if seen[family] {
			continue
		}
		seen[family] = true
		result = append(result, theme)
	}
	return result
}

func cssRuleHas(t *testing.T, css, selector string, declarations ...string) {
	t.Helper()
	pattern := regexp.QuoteMeta(selector) + `[^\{]*\{([^}]*)\}`
	matches := regexp.MustCompile(pattern).FindAllStringSubmatch(css, -1)
	for _, match := range matches {
		body := strings.ReplaceAll(match[1], " ", "")
		all := true
		for _, declaration := range declarations {
			if !strings.Contains(body, strings.ReplaceAll(declaration, " ", "")) {
				all = false
				break
			}
		}
		if all {
			return
		}
	}
	t.Errorf("CSS selector %q is missing declarations %q", selector, declarations)
}

func cssAfter(css, marker string) string {
	start := strings.Index(css, marker)
	if start < 0 {
		return ""
	}
	return css[start:]
}

func cssBefore(css, marker string) string {
	if end := strings.Index(css, marker); end >= 0 {
		return css[:end]
	}
	return css
}
