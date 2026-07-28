package web

import (
	"os"
	"strings"
	"testing"
)

// TestThemePreviewKeepsFixedSidebarLayoutsOutOfFlow guards the preview-only
// cascade: the generic header normalization must not turn full-height fixed
// rails into in-flow blocks and push the preview body below the 760px crop.
func TestThemePreviewKeepsFixedSidebarLayoutsOutOfFlow(t *testing.T) {
	src, err := os.ReadFile("../../templates/theme_preview.html")
	if err != nil {
		t.Fatalf("read theme preview template: %v", err)
	}
	html := string(src)
	styleEnd := strings.Index(html, "</style>")
	if styleEnd < 0 {
		t.Fatal("theme preview template has no inline style block")
	}
	previewCSS := html[:styleEnd]

	for _, layout := range []string{
		"sidebar",
		"factory-sidebar",
		"dtc-catalogue",
		"dtc-atelier",
	} {
		selector := `[data-theme-layout="` + layout + `"]`
		if !strings.Contains(previewCSS, selector) {
			t.Errorf("fixed sidebar layout %q is missing from preview recovery selector", layout)
		}
	}
	if !strings.Contains(previewCSS, `) .site-header:not(.pc-site-header) { position: fixed; inset: 0 auto 0 0; }`) {
		t.Error("fixed sidebar preview recovery rule is missing")
	}
	if !strings.Contains(previewCSS, `.site-header:not(.pc-site-header) { position: relative; top: auto; }`) {
		t.Error("generic preview header normalization unexpectedly changed")
	}
}
