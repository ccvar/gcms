package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDashboardThemeFamiliesAreRegisteredWithPureWhiteCards(t *testing.T) {
	cards := themeFamilyCards(pickerCardsFromRegistry(t), "toolbench", "zh")
	for _, family := range []string{"toolbench", "decision-grid", "release-radar"} {
		t.Run(family, func(t *testing.T) {
			if !validTheme(family) || !validTheme(family+"-white") {
				t.Fatalf("%s family is not fully registered", family)
			}
			if got := layoutForTheme(family + "-white"); got != family {
				t.Errorf("layoutForTheme(%q) = %q, want %q", family+"-white", got, family)
			}
			if got := contentThemeFamily(family + "-white"); got != family {
				t.Errorf("contentThemeFamily(%q) = %q, want %q", family+"-white", got, family)
			}
			if got := themeBg(family + "-white"); got != "#ffffff" {
				t.Errorf("themeBg(%q) = %q, want #ffffff", family+"-white", got)
			}

			var found *ThemeFamilyCard
			for i := range cards {
				if cards[i].Family == family {
					found = &cards[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("missing %s family card", family)
			}
			if len(found.Skins) != 2 || found.Skins[0].ID != family || found.Skins[1].ID != family+"-white" {
				t.Errorf("%s skins = %#v, want native then pure-white", family, found.Skins)
			}
		})
	}
}

func TestDashboardThemesUseLiveSlotsAndRestyleInnerPages(t *testing.T) {
	for _, family := range []string{"toolbench", "decision-grid", "release-radar"} {
		t.Run(family, func(t *testing.T) {
			templatePath := filepath.Join("..", "..", "templates", "partials", "home_"+strings.ReplaceAll(family, "-", "_")+".html")
			templateBytes, err := os.ReadFile(templatePath)
			if err != nil {
				t.Fatalf("read %s: %v", templatePath, err)
			}
			template := string(templateBytes)
			for _, marker := range []string{
				".Site.HeroVisual", ".Site.HomeLinks", ".Site.HomeFeatured", ".Site.HomeLatest",
				".FeatLinks", ".FeaturedMore", ".Posts", ".Categories",
			} {
				if !strings.Contains(template, marker) {
					t.Errorf("%s template misses live slot %q", family, marker)
				}
			}
			for _, synthetic := range []string{
				"内容发布工具", "搜索增强方案", "两种页面构建方式",
				"Headless CMS 对比", "2026-07-30", "120+",
			} {
				if strings.Contains(template, synthetic) {
					t.Errorf("%s template hard-codes design sample %q", family, synthetic)
				}
			}

			cssPath := filepath.Join("..", "..", "assets", "css", family+".css")
			cssBytes, err := os.ReadFile(cssPath)
			if err != nil {
				t.Fatalf("read %s: %v", cssPath, err)
			}
			css := string(cssBytes)
			for _, marker := range []string{
				`[data-theme="` + family + `"]`,
				`[data-theme="` + family + `-white"]`,
				".dd-site-header", ".page-head", ".post-list", ".link-grid",
				".article-grid", ".searchbox", ".pagination",
				"@media (max-width:",
			} {
				if !strings.Contains(css, marker) {
					t.Errorf("%s CSS misses contract %q", family, marker)
				}
			}
			if !strings.Contains(css, ".ct-footer") && !strings.Contains(css, ".site-footer") {
				t.Errorf("%s CSS does not restyle the shared footer", family)
			}
		})
	}
}

func TestDashboardHeaderSearchAffordanceMatchesItsVisualTreatment(t *testing.T) {
	headerPath := filepath.Join("..", "..", "templates", "partials", "header.html")
	headerBytes, err := os.ReadFile(headerPath)
	if err != nil {
		t.Fatalf("read %s: %v", headerPath, err)
	}
	header := string(headerBytes)
	for _, marker := range []string{
		`{{define "header_search_form"}}`,
		`action="{{.Tr.U "/search"}}"`,
		`name="q" value="{{.Query}}"`,
		`{{if eq .ContentThemeFamily "release-radar"}}`,
		`{{template "header_search_form" .}}`,
	} {
		if !strings.Contains(header, marker) {
			t.Errorf("dashboard header misses functional search contract %q", marker)
		}
	}
}
