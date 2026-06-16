package web

import (
	"os"
	"strings"
	"testing"
)

func TestRendererParsesTemplates(t *testing.T) {
	if _, err := NewRenderer(os.DirFS("../.."), scanAssetImageSizes(os.DirFS("../.."))); err != nil {
		t.Fatalf("parse templates: %v", err)
	}
}

func TestRenderContentAddsImageLoadingHints(t *testing.T) {
	html, _ := RenderContent("![cover](/assets/cover.webp)")
	got := string(html)
	for _, want := range []string{`loading="lazy"`, `decoding="async"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered image missing %s: %s", want, got)
		}
	}
}

func TestAddImageLoadingHintsHandlesRenderedHTML(t *testing.T) {
	got := addImageLoadingHints(`<p><img src="/a.webp" alt="A"></p><p><img src="/b.webp" loading="eager" decoding="sync"></p>`, map[string]ImageSize{
		"/a.webp": {Width: 1200, Height: 630},
	})
	for _, want := range []string{`src="/a.webp"`, `loading="lazy"`, `decoding="async"`, `width="1200"`, `height="630"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("image hint output missing %s: %s", want, got)
		}
	}
	if strings.Count(got, `loading=`) != 2 || strings.Count(got, `decoding=`) != 2 {
		t.Fatalf("should not duplicate existing attrs: %s", got)
	}
}
