package web

import "testing"

func TestGiscusScriptAttr(t *testing.T) {
	raw := `<script src="https://giscus.app/client.js"
		data-repo-id="R_kgDO&amp;Example"
		data-repo='ccvar/site-comments'
		data-category=Announcements
		data-category-id="DIC_kwDOExample">
	</script>`

	tests := map[string]string{
		"data-repo":        "ccvar/site-comments",
		"data-repo-id":     "R_kgDO&Example",
		"data-category":    "Announcements",
		"data-category-id": "DIC_kwDOExample",
		"data-mapping":     "",
	}
	for attr, want := range tests {
		if got := giscusScriptAttr(raw, attr); got != want {
			t.Fatalf("giscusScriptAttr(%q) = %q, want %q", attr, got, want)
		}
	}
}
