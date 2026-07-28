package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func assertNoUnresolvedLocalOpenAPIRefs(t *testing.T, document map[string]any) {
	t.Helper()
	var walk func(any)
	walk = func(value any) {
		switch current := value.(type) {
		case map[string]any:
			if ref, ok := current["$ref"].(string); ok && strings.HasPrefix(ref, "#/") {
				target := any(document)
				for _, rawPart := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
					part := strings.ReplaceAll(strings.ReplaceAll(rawPart, "~1", "/"), "~0", "~")
					object, ok := target.(map[string]any)
					if !ok {
						t.Errorf("OpenAPI ref %q traverses a non-object at %q", ref, part)
						break
					}
					target, ok = object[part]
					if !ok {
						t.Errorf("OpenAPI ref %q is unresolved at %q", ref, part)
						break
					}
				}
			}
			for _, child := range current {
				walk(child)
			}
		case []any:
			for _, child := range current {
				walk(child)
			}
		}
	}
	walk(document)
}

func TestAutomationOpenAPIHasNoUnresolvedLocalReferences(t *testing.T) {
	t.Run("dynamic", func(t *testing.T) {
		assertNoUnresolvedLocalOpenAPIRefs(t, automationOpenAPISpec("/api/admin/v1"))
	})
	t.Run("bundled", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(
			"..", "..", "skills", "gcms-content-assistant", "references", "openapi.json",
		))
		if err != nil {
			t.Fatalf("read bundled OpenAPI: %v", err)
		}
		var document map[string]any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatalf("parse bundled OpenAPI: %v", err)
		}
		assertNoUnresolvedLocalOpenAPIRefs(t, document)
	})
}
