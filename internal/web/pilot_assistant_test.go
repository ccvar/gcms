package web

import "testing"

func TestPilotAssistantAutomationScopesCoverPlatformAutomation(t *testing.T) {
	scopes := PilotAssistantAutomationScopes()
	got := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		if got[scope] {
			t.Fatalf("duplicate scope %q", scope)
		}
		got[scope] = true
	}
	for _, want := range []string{
		"languages:read", "languages:write", "languages:enable", "languages:default", "languages:catalog",
		"media:write", "site:read", "site:write", "brand:assets:write",
		"navigation:read", "navigation:write", "stats:read",
		"content:read", "content:write", "content:publish",
		"posts:categories", "posts:categories:write", "posts:pin",
		"links:categories", "links:categories:write", "links:pin",
	} {
		if !got[want] {
			t.Errorf("missing scope %q", want)
		}
	}
}
