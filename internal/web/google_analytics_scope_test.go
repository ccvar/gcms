package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cms.ccvar.com/internal/platform"
)

func TestGoogleAnalyticsSiteDomainsStayIsolated(t *testing.T) {
	s, _ := newTestStatsServer(t, "stats:read", true)
	s.baseURL = "https://cms.platform.test"
	other, err := s.platform.CreateSite("second", "Second", filepath.Join(t.TempDir(), "site.db"), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.platform.AddSiteDomain(other.ID, "https", "second.test", true, false); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]string{s.platformSiteID: "example.com,www.example.com", other.ID: "second.test,www.second.test"} {
		if got := strings.Join(s.googleAnalyticsHostnamesForSite(id), ","); got != want {
			t.Fatalf("site %d hosts = %q, want %q", id, got, want)
		}
	}
	if err := s.platform.ReplaceSiteDomains(other.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.googleAnalyticsHostnamesForSite(other.ID); len(got) != 0 {
		t.Fatalf("unbound site borrowed another site's hosts: %v", got)
	}
}

func TestGoogleAnalyticsReportsUseBoundStreamAndSeparateCaches(t *testing.T) {
	s, token := newTestStatsServer(t, "stats:read", true)
	in, _, err := s.platform.SiteGoogleIntegration(s.platformSiteID, platform.GoogleServiceAnalytics)
	if err != nil {
		t.Fatal(err)
	}
	in.DataStream = "properties/123456/dataStreams/701"
	if err := s.platform.UpsertSiteGoogleIntegration(in); err != nil {
		t.Fatal(err)
	}
	oldClient := googleHTTPClient
	t.Cleanup(func() { googleHTTPClient = oldClient })
	calls := 0
	googleHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		var body struct {
			DimensionFilter struct {
				Filter struct {
					FieldName    string `json:"fieldName"`
					StringFilter struct {
						MatchType string `json:"matchType"`
						Value     string `json:"value"`
					} `json:"stringFilter"`
				} `json:"filter"`
			} `json:"dimensionFilter"`
		}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		filter := body.DimensionFilter.Filter
		if filter.FieldName != "streamId" || filter.StringFilter.MatchType != "EXACT" || filter.StringFilter.Value != strings.TrimPrefix(in.DataStream, "properties/123456/dataStreams/") {
			t.Fatalf("incorrect query scope: %+v", filter)
		}
		// Simulate records with no hostName, but a valid stream. A hostname filter
		// would exclude all records; the selected stream retains its own metrics.
		users := "70"
		if filter.StringFilter.Value == "702" {
			users = "167"
		}
		return jsonTestResponse(req, http.StatusOK, `{"rows":[{"dimensionValues":[{"value":"/vi/"}],"metricValues":[{"value":"`+users+`"},{"value":"180"},{"value":"0.5"},{"value":"30"}]}]}`), nil
	})}
	summary, err := googleAnalyticsSummaryForScope(context.Background(), "token", in.Property, googleDataRange{Mode: "days", Days: 30}, s.googleAnalyticsScopeForSite(in))
	if err != nil || summary.ActiveUsers7D != 70 {
		t.Fatalf("summary = %+v, err %v", summary, err)
	}
	search, _, _ := s.platform.SiteGoogleIntegration(s.platformSiteID, platform.GoogleServiceSearchConsole)
	search.Enabled = false
	if err := s.platform.UpsertSiteGoogleIntegration(search); err != nil {
		t.Fatal(err)
	}
	refresh := s.refreshDiscoveryGoogleSummaries(context.Background(), httptest.NewRequest(http.MethodGet, "/", nil), map[int64]bool{s.platformSiteID: true})
	stored, ok, err := s.platform.SiteGoogleAnalyticsSummary(s.platformSiteID)
	if err != nil || !ok || refresh.Refreshed != 1 || refresh.Failed != 0 || stored.ActiveUsers != 70 || stored.ScopeKey != s.googleAnalyticsScopeForSite(in).cacheKey() {
		t.Fatalf("refresh=%+v stored=%+v err=%v", refresh, stored, err)
	}
	paths := []string{"/stats/traffic?days=30", "/stats/pages?days=30", "/stats/analytics?group=sources&days=30", "/stats/analytics?group=geography&days=30", "/stats/analytics?group=devices&days=30", "/stats/analytics?group=trend&days=30"}
	for _, path := range paths {
		w, payload := statsGet(t, s, token, path)
		if w.Code != http.StatusOK || payload["scope_stream_id"] != "701" || payload["scope_type"] != "stream" {
			t.Fatalf("%s: %d %v", path, w.Code, payload)
		}
	}
	before := calls
	statsGet(t, s, token, paths[0])
	if calls != before {
		t.Fatal("unchanged binding should use cache")
	}
	in.DataStream = "properties/123456/dataStreams/702"
	if err := s.platform.UpsertSiteGoogleIntegration(in); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		w, payload := statsGet(t, s, token, path)
		if w.Code != http.StatusOK || payload["scope_stream_id"] != "702" {
			t.Fatalf("new stream reused cached data: %s %v", path, payload)
		}
	}
	if calls != before+len(paths) {
		t.Fatalf("new binding should miss every report cache: calls=%d before=%d", calls, before)
	}
}

func TestGoogleAnalyticsInvalidScopeDoesNotFetchWholeProperty(t *testing.T) {
	for _, scope := range []googleAnalyticsReportScope{
		{},
		{DataStream: "properties/999/dataStreams/701", Hostnames: []string{"example.com"}},
		{DataStream: "properties/123456/dataStreams/bad"},
		{DataStream: "properties/123456/dataStreams/0"},
	} {
		if err := scope.apply(map[string]any{}, "properties/123456"); err == nil {
			t.Fatalf("invalid scope allowed: %+v", scope)
		}
	}
	body := map[string]any{}
	if err := (googleAnalyticsReportScope{Hostnames: []string{"second.test", "www.second.test"}}).apply(body, "properties/123456"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	if !strings.Contains(string(raw), `"fieldName":"hostName"`) || !strings.Contains(string(raw), `"second.test"`) {
		t.Fatalf("legacy binding must retain site-scoped filter: %s", raw)
	}
}

func TestGoogleAnalyticsSummaryRejectsOldScope(t *testing.T) {
	s, _ := newTestStatsServer(t, "stats:read", true)
	in, _, _ := s.platform.SiteGoogleIntegration(s.platformSiteID, platform.GoogleServiceAnalytics)
	site, _, _ := s.platform.GetSite(s.platformSiteID)
	sum := &platform.SiteGoogleAnalyticsSummary{
		SiteID: site.ID, Property: in.Property, MeasurementID: in.MeasurementID,
		RangeKey: googleDataRangeKeyValue(s.googleDataRange()),
		Status:   platform.GoogleAnalyticsSummaryStatusOK, FetchedAt: time.Now(),
		ActiveUsers: 0, Sessions: 0,
	}
	read := func() map[string]any {
		t.Helper()
		if err := s.platform.UpsertSiteGoogleAnalyticsSummary(sum); err != nil {
			t.Fatal(err)
		}
		return s.discoverySiteIntegrations(nil, site, "https://example.com", s.discoveryIntegrationSnapshot())["analytics"].(map[string]any)
	}
	if payload := read(); payload["status"] != "stale" || payload["active_users"] != nil {
		t.Fatalf("old zero summary must be stale: %v", payload)
	}
	sum.ScopeKey = s.googleAnalyticsScopeForSite(in).cacheKey()
	if payload := read(); payload["status"] != "ok" || payload["active_users"] != 0 {
		t.Fatalf("a fresh real zero must remain valid: %v", payload)
	}
	in.DataStream = "properties/123456/dataStreams/701"
	if err := s.platform.UpsertSiteGoogleIntegration(in); err != nil {
		t.Fatal(err)
	}
	if payload := read(); payload["status"] != "stale" {
		t.Fatalf("changed binding must invalidate summary: %v", payload)
	}
	sum.ScopeKey = s.googleAnalyticsScopeForSite(in).cacheKey()
	sum.ActiveUsers = 70
	if payload := read(); payload["status"] != "ok" || payload["active_users"] != 70 || payload["scope_stream_id"] != "701" {
		t.Fatalf("fresh stream summary: %v", payload)
	}
}
