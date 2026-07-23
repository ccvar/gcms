package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/store"
)

func patchSiteProfileForTest(t *testing.T, s *Server, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPatch, "/api/admin/v1/site-profile", bytes.NewBufferString(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.apiUpdateSiteProfile(w, r)
	return w
}

func TestAPISiteProfileHomeDisplaySettingsRoundTrip(t *testing.T) {
	s, token := newTestAutomationServer(t, apiScopeSiteRead+","+apiScopeSiteWrite)

	r := httptest.NewRequest(http.MethodGet, "/api/admin/v1/site-profile", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.apiGetSiteProfile(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", w.Code, w.Body.String())
	}
	var initial struct {
		HomeLinksLimit   int `json:"home_links_limit"`
		HomePostsPerPage int `json:"home_posts_per_page"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &initial); err != nil {
		t.Fatalf("decode initial response: %v", err)
	}
	if initial.HomeLinksLimit != defaultHomeLinksLimit || initial.HomePostsPerPage != defaultHomePostsPerPage {
		t.Fatalf("initial display settings = %#v, want links=%d posts=%d", initial, defaultHomeLinksLimit, defaultHomePostsPerPage)
	}

	w = patchSiteProfileForTest(t, s, token, `{"home_links_limit":0,"home_posts_per_page":12}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := s.store.Setting(homeLinksLimitKey); got != "0" {
		t.Fatalf("%s = %q, want 0", homeLinksLimitKey, got)
	}
	if got := s.store.Setting(homePostsPerPageKey); got != "12" {
		t.Fatalf("%s = %q, want 12", homePostsPerPageKey, got)
	}
	var updated struct {
		HomeLinksLimit   int `json:"home_links_limit"`
		HomePostsPerPage int `json:"home_posts_per_page"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated response: %v", err)
	}
	if updated.HomeLinksLimit != 0 || updated.HomePostsPerPage != 12 {
		t.Fatalf("updated display settings = %#v", updated)
	}

	w = patchSiteProfileForTest(t, s, token, `{"home_links_limit":7}`)
	if w.Code != http.StatusOK {
		t.Fatalf("single-field PATCH status = %d, body = %s", w.Code, w.Body.String())
	}
	if got := s.store.Setting(homeLinksLimitKey); got != "7" {
		t.Fatalf("%s = %q, want 7", homeLinksLimitKey, got)
	}
	if got := s.store.Setting(homePostsPerPageKey); got != "12" {
		t.Fatalf("single-field PATCH changed %s to %q", homePostsPerPageKey, got)
	}
}

func TestAPISiteProfileHomeDisplaySettingsValidateBeforeWrite(t *testing.T) {
	s, token := newTestAutomationServer(t, apiScopeSiteWrite)
	if err := s.store.SetSetting(homeLinksLimitKey, "5"); err != nil {
		t.Fatalf("seed links limit: %v", err)
	}
	if err := s.store.SetSetting(homePostsPerPageKey, "9"); err != nil {
		t.Fatalf("seed posts per page: %v", err)
	}

	for _, body := range []string{
		`{"home_links_limit":-1}`,
		`{"home_links_limit":25}`,
		`{"home_posts_per_page":0}`,
		`{"home_posts_per_page":51}`,
		`{"home_links_limit":4,"home_posts_per_page":0}`,
	} {
		w := patchSiteProfileForTest(t, s, token, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("PATCH %s status = %d, body = %s", body, w.Code, w.Body.String())
		}
		if got := s.store.Setting(homeLinksLimitKey); got != "5" {
			t.Fatalf("invalid PATCH %s changed %s to %q", body, homeLinksLimitKey, got)
		}
		if got := s.store.Setting(homePostsPerPageKey); got != "9" {
			t.Fatalf("invalid PATCH %s changed %s to %q", body, homePostsPerPageKey, got)
		}
	}
}

func TestAPISiteProfileHomeDisplaySettingsRequireSiteWriteAndTopLevel(t *testing.T) {
	for _, scope := range []string{apiScopeSiteRead, apiScopeBrandAssetsWrite} {
		s, token := newTestAutomationServer(t, scope)
		w := patchSiteProfileForTest(t, s, token, `{"home_links_limit":3}`)
		if w.Code != http.StatusForbidden {
			t.Fatalf("scope %s status = %d, body = %s", scope, w.Code, w.Body.String())
		}
	}

	s, token := newTestAutomationServer(t, apiScopeSiteWrite)
	w := patchSiteProfileForTest(t, s, token, `{"items":[{"lang":"zh","home_links_limit":3}]}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "bad_json") {
		t.Fatalf("nested global setting status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestSiteProfileHomeDisplaySettingsOpenAPIAndSkillContract(t *testing.T) {
	schemas := automationOpenAPISchemas()
	for _, schemaName := range []string{"SiteProfileResponse", "SiteProfilePatch"} {
		schema, ok := schemas[schemaName].(map[string]any)
		if !ok {
			t.Fatalf("%s schema missing", schemaName)
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s properties missing", schemaName)
		}
		for _, field := range []string{"home_links_limit", "home_posts_per_page"} {
			prop, ok := props[field].(map[string]any)
			if !ok {
				t.Fatalf("%s.%s missing", schemaName, field)
			}
			if prop["type"] != "integer" || !strings.Contains(prop["description"].(string), "全站") {
				t.Fatalf("%s.%s contract = %#v", schemaName, field, prop)
			}
		}
	}

	for name, markdown := range map[string]string{
		"single":   automationSkillMarkdown("https://example.com/api/admin/v1"),
		"platform": platformSkillMarkdown("https://example.com/api/platform/v1"),
	} {
		if !strings.Contains(markdown, `"home_links_limit":8,"home_posts_per_page":6`) ||
			!strings.Contains(markdown, "全站、全语种共用") {
			t.Fatalf("%s skill does not explain homepage display settings", name)
		}
	}
}

func TestKnowledgeHeroLinksHonorConfiguredLimit(t *testing.T) {
	s, _ := newTestAutomationServer(t, apiScopeSiteRead)
	now := time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		if _, err := s.store.CreatePost(&store.Post{
			Type:        "link",
			Lang:        "zh",
			Slug:        "knowledge-link-" + string(rune('a'+i)),
			Title:       "Knowledge Link",
			LinkURL:     "https://example.com",
			Status:      "published",
			Featured:    true,
			PublishedAt: now.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("create link %d: %v", i, err)
		}
	}

	if got := s.knowledgeHeroLinks("zh", 0); len(got) != 0 {
		t.Fatalf("limit 0 returned %d links", len(got))
	}
	if got := s.knowledgeHeroLinks("zh", 2); len(got) != 2 {
		t.Fatalf("limit 2 returned %d links", len(got))
	}
}
