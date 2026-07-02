package web

import (
	"strings"
	"testing"
)

func TestExternalLinkPolicyAttrs(t *testing.T) {
	policy := defaultExternalLinkPolicy().WithInternalHosts("https://example.com")
	if got := policy.HTMLAttr("https://example.com/about"); got != "" {
		t.Fatalf("internal link attrs = %q, want empty", got)
	}

	got := string(policy.HTMLAttr("https://other.example/path"))
	if got != ` target="_blank" rel="noopener noreferrer"` {
		t.Fatalf("default external attrs = %q", got)
	}

	policy.Rules = []ExternalLinkRule{{
		Domain:            "partner.example",
		IncludeSubdomains: true,
		TargetBlank:       true,
		Rel:               []string{"nofollow", "sponsored", "noopener", "noreferrer"},
	}}
	got = string(policy.HTMLAttr("https://go.partner.example/path"))
	if got != ` target="_blank" rel="sponsored nofollow noopener noreferrer"` {
		t.Fatalf("domain rule attrs = %q", got)
	}
}

func TestRenderContentWithLinkPolicyDecoratesMarkdownLinks(t *testing.T) {
	policy := ExternalLinkPolicy{
		TargetBlank: true,
		Rel:         []string{"noopener", "noreferrer"},
		Rules: []ExternalLinkRule{{
			Domain:            "ads.example",
			IncludeSubdomains: true,
			TargetBlank:       true,
			Rel:               []string{"sponsored", "nofollow", "noopener", "noreferrer"},
		}},
	}.WithInternalHosts("https://site.example")

	html, _ := RenderContentWithLinkPolicy("[ad](https://go.ads.example/x) [inside](/about)", nil, &policy)
	got := string(html)
	for _, want := range []string{`href="https://go.ads.example/x"`, `target="_blank"`, `rel="sponsored nofollow noopener noreferrer"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered markdown missing %s: %s", want, got)
		}
	}
	if strings.Contains(got, `href="/about" target="_blank"`) {
		t.Fatalf("relative link should not be decorated: %s", got)
	}
}
