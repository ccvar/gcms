package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func compositionContactRequest(
	t *testing.T,
	s *Server,
	values url.Values,
	origin string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		"http://contact.example/api/forms/contact",
		strings.NewReader(values.Encode()),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	return response
}

func cloneCompositionContactValues(values url.Values) url.Values {
	out := make(url.Values, len(values))
	for key, items := range values {
		out[key] = append([]string(nil), items...)
	}
	return out
}

func TestCompositionContactSubmissionIsPublishedBoundValidatedAndPrivate(t *testing.T) {
	s := newTestPublicServer(t, "")
	raw := compositionManifestJSON(t, "none", []map[string]any{{
		"id": "contact", "type": "form.contact",
		"props": map[string]any{
			"title": "Contact", "fields": []string{"name", "email", "message"},
			"submit_label": "Send", "privacy_label": "Privacy",
			"privacy_href": "/privacy",
		},
	}})
	_, project, revision := createCompositionProject(t, s, raw, "published")
	preflight, err := s.ValidateCompositionBuild(
		context.Background(), project, revision, CompositionBindingPublishedOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	publishCompositionProject(t, s, project, revision, preflight.DataSnapshotHash)

	valid := url.Values{
		"_project_id":     {strconv.FormatInt(project.ID, 10)},
		"_revision_id":    {strconv.FormatInt(revision.ID, 10)},
		"_section_id":     {"contact"},
		"name":            {"Ada"},
		"email":           {"ada@example.test"},
		"message":         {"Please call me."},
		"privacy_consent": {"1"},
		"website":         {""},
	}
	crossSite := compositionContactRequest(t, s, valid, "https://evil.example")
	if crossSite.Code != http.StatusForbidden ||
		!strings.Contains(crossSite.Body.String(), "origin_forbidden") {
		t.Fatalf("cross-site submission = %d %s", crossSite.Code, crossSite.Body.String())
	}

	unknown := cloneCompositionContactValues(valid)
	unknown.Set("admin", "true")
	unknownResponse := compositionContactRequest(t, s, unknown, "http://contact.example")
	if unknownResponse.Code != http.StatusBadRequest ||
		!strings.Contains(unknownResponse.Body.String(), "form_field_invalid") {
		t.Fatalf("unknown field = %d %s", unknownResponse.Code, unknownResponse.Body.String())
	}

	noConsent := cloneCompositionContactValues(valid)
	noConsent.Del("privacy_consent")
	consentResponse := compositionContactRequest(t, s, noConsent, "http://contact.example")
	if consentResponse.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(consentResponse.Body.String(), "privacy_consent_required") {
		t.Fatalf("privacy consent = %d %s", consentResponse.Code, consentResponse.Body.String())
	}

	success := compositionContactRequest(t, s, valid, "http://contact.example")
	if success.Code != http.StatusCreated || !strings.Contains(success.Body.String(), `"ok":true`) ||
		success.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("contact success = %d headers=%v body=%s",
			success.Code, success.Header(), success.Body.String())
	}

	dir := filepath.Join(s.store.PageProjectStorageDir(), "forms", "contact")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("private inbox entries=%v err=%v", entries, err)
	}
	file, err := os.Open(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatalf("contact inbox is empty: %v", scanner.Err())
	}
	var record compositionContactRecord
	if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record.ProjectID != project.ID || record.RevisionID != revision.ID ||
		record.SectionID != "contact" || record.Fields["email"] != "ada@example.test" ||
		record.SourceHash == "" {
		t.Fatalf("contact record = %+v", record)
	}
	if _, exists := record.Fields["privacy_consent"]; exists {
		t.Fatalf("consent control leaked into submitted fields: %+v", record.Fields)
	}

	// The invalid-consent attempt also reaches the abuse limiter, so three
	// further accepted requests fill the five-request source/project window.
	for i := 0; i < 3; i++ {
		response := compositionContactRequest(t, s, valid, "http://contact.example")
		if response.Code != http.StatusCreated {
			t.Fatalf("rate-limit warmup %d = %d %s", i, response.Code, response.Body.String())
		}
	}
	limited := compositionContactRequest(t, s, valid, "http://contact.example")
	if limited.Code != http.StatusTooManyRequests ||
		limited.Header().Get("Retry-After") == "" ||
		!strings.Contains(limited.Body.String(), "rate_limited") {
		t.Fatalf("rate limit = %d headers=%v body=%s",
			limited.Code, limited.Header(), limited.Body.String())
	}

	stale := cloneCompositionContactValues(valid)
	stale.Set("_revision_id", strconv.FormatInt(revision.ID+999, 10))
	staleResponse := compositionContactRequest(t, s, stale, "http://contact.example")
	if staleResponse.Code != http.StatusNotFound {
		t.Fatalf("stale/unpublished revision = %d %s", staleResponse.Code, staleResponse.Body.String())
	}
}

func TestCompositionContactCloudflareStaticOriginAndSafeRedirect(t *testing.T) {
	s := newTestPublicServer(t, "")
	for key, value := range map[string]string{
		cloudflareDomainsKey: encodeCloudflareDomains([]CloudflareDomain{{
			Host: "landing.example", Primary: true,
		}}),
		cloudflareOriginURLKey: "https://contact.example",
	} {
		if err := s.store.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	raw := compositionManifestJSON(t, "none", []map[string]any{{
		"id": "contact", "type": "form.contact",
		"props": map[string]any{
			"title": "Contact", "fields": []string{"name", "email", "message"},
			"submit_label": "Send",
		},
	}})
	post, project, revision := createCompositionProject(t, s, raw, "published")
	preflight, err := s.ValidateCompositionBuild(
		context.Background(), project, revision, CompositionBindingPublishedOnly,
	)
	if err != nil {
		t.Fatal(err)
	}
	publishCompositionProject(t, s, project, revision, preflight.DataSnapshotHash)

	publicURL := "https://landing.example/" + post.Lang + publicContentPath(post.Type, post.Slug)
	publicResponse := httptest.NewRecorder()
	publicRequest := httptest.NewRequest(http.MethodGet, publicURL, nil)
	s.Handler().ServeHTTP(publicResponse, publicRequest)
	if publicResponse.Code != http.StatusOK ||
		!strings.Contains(
			publicResponse.Body.String(),
			`action="https://contact.example/api/forms/contact"`,
		) {
		t.Fatalf("public contact action = %d %s", publicResponse.Code, publicResponse.Body.String())
	}

	exported, err := s.exportStaticSite(context.Background(), CloudflareConfig{
		DeployMode: cloudflareModePages,
		Domains: []CloudflareDomain{{
			Host: "landing.example", Primary: true,
		}},
		RoutePattern: "landing.example/*",
		OriginURL:    "https://contact.example",
	})
	if err != nil {
		t.Fatalf("static export with contact form: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(exported.Dir) })
	staticHTML, err := os.ReadFile(filepath.Join(
		exported.Dir, post.Lang, post.Slug, "index.html",
	))
	if err != nil ||
		!strings.Contains(
			string(staticHTML),
			`action="https://contact.example/api/forms/contact"`,
		) {
		t.Fatalf("static contact action = %q err=%v", staticHTML, err)
	}

	values := url.Values{
		"_project_id":  {strconv.FormatInt(project.ID, 10)},
		"_revision_id": {strconv.FormatInt(revision.ID, 10)},
		"_section_id":  {"contact"},
		"name":         {"Ada"},
		"email":        {"ada@example.test"},
		"message":      {"Please call me."},
		"website":      {""},
	}
	submit := httptest.NewRequest(
		http.MethodPost,
		"https://contact.example/api/forms/contact",
		strings.NewReader(values.Encode()),
	)
	submit.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	submit.Header.Set("Accept", "text/html")
	submit.Header.Set("Origin", "https://landing.example")
	submit.Header.Set("Sec-Fetch-Site", "cross-site")
	submitResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(submitResponse, submit)
	wantLocation := publicURL + "?contact=sent#contact"
	if submitResponse.Code != http.StatusSeeOther ||
		submitResponse.Header().Get("Location") != wantLocation {
		t.Fatalf(
			"public-origin submit = %d location=%q body=%s",
			submitResponse.Code,
			submitResponse.Header().Get("Location"),
			submitResponse.Body.String(),
		)
	}

	untrusted := httptest.NewRequest(
		http.MethodPost,
		"https://contact.example/api/forms/contact",
		strings.NewReader(values.Encode()),
	)
	untrusted.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	untrusted.Header.Set("Origin", "https://evil.example")
	untrusted.Header.Set("Sec-Fetch-Site", "cross-site")
	untrustedResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(untrustedResponse, untrusted)
	if untrustedResponse.Code != http.StatusForbidden ||
		!strings.Contains(untrustedResponse.Body.String(), "origin_forbidden") {
		t.Fatalf(
			"untrusted public origin = %d %s",
			untrustedResponse.Code,
			untrustedResponse.Body.String(),
		)
	}
}

func TestCompositionContactStaticExportRequiresDistinctConfiguredOrigin(t *testing.T) {
	for _, tc := range []struct {
		name      string
		originURL string
		want      string
	}{
		{
			name: "missing origin",
			want: "必须配置合法的 GCMS OriginURL",
		},
		{
			name:      "http origin",
			originURL: "http://contact.example",
			want:      "必须使用 HTTPS",
		},
		{
			name:      "origin equals public domain",
			originURL: "https://landing.example",
			want:      "必须与 Cloudflare 公共域名不同",
		},
		{
			name:      "origin equals pages deployment domain",
			originURL: "https://gcms-landing-example.pages.dev",
			want:      "必须与 Cloudflare 公共域名不同",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestPublicServer(t, "")
			raw := compositionManifestJSON(t, "none", []map[string]any{{
				"id": "contact", "type": "form.contact",
				"props": map[string]any{
					"title": "Contact", "fields": []string{"name", "email", "message"},
					"submit_label": "Send",
				},
			}})
			_, project, revision := createCompositionProject(t, s, raw, "published")
			preflight, err := s.ValidateCompositionBuild(
				context.Background(), project, revision, CompositionBindingPublishedOnly,
			)
			if err != nil {
				t.Fatal(err)
			}
			publishCompositionProject(t, s, project, revision, preflight.DataSnapshotHash)

			exported, err := s.exportStaticSite(context.Background(), CloudflareConfig{
				DeployMode: cloudflareModePages,
				Domains: []CloudflareDomain{{
					Host: "landing.example", Primary: true,
				}},
				RoutePattern: "landing.example/*",
				OriginURL:    tc.originURL,
			})
			if exported != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("exported=%#v err=%v, want %q", exported, err, tc.want)
			}
		})
	}
}
