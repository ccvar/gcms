package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestPagePlatformScopeIssuanceIsExplicitAndAddsOnlyFamilyDependencies(t *testing.T) {
	req := &http.Request{Method: http.MethodPost, Header: make(http.Header), Form: url.Values{
		"scopes": {
			apiScopePageProjectsBuild,
			apiScopePageAssetsWrite,
			apiScopePageAppsWrite,
			apiScopePagePreviewRead,
			apiScopePageCapabilitiesGrant,
		},
	}}
	got := automationScopesFromFormRequired(req)
	joined := "," + strings.Join(got, ",") + ","
	for _, want := range []string{
		apiScopePageProjectsRead,
		apiScopePageProjectsWrite,
		apiScopePageProjectsBuild,
		apiScopePageAssetsWrite,
		apiScopePageAppsWrite,
		apiScopePagePreviewRead,
		apiScopePageCapabilitiesGrant,
		apiScopeControlRead,
		apiScopeControlUnlock,
	} {
		if !strings.Contains(joined, ","+want+",") {
			t.Fatalf("page platform dependency %q missing from %v", want, got)
		}
	}
	for _, denied := range []string{apiScopeContentRead, apiScopeContentWrite, apiScopeContentPublish, apiScopePagesPublish} {
		if strings.Contains(joined, ","+denied+",") {
			t.Fatalf("page platform issuance unexpectedly broadened to %q: %v", denied, got)
		}
	}
}

func TestLegacyContentScopesDoNotIssueOrDefaultPagePlatformScopes(t *testing.T) {
	req := &http.Request{Method: http.MethodPost, Header: make(http.Header), Form: url.Values{
		"scopes": {apiScopeContentRead, apiScopeContentWrite, apiScopeContentPublish},
	}}
	issued := "," + strings.Join(automationScopesFromFormRequired(req), ",") + ","
	defaults := "," + strings.Join(defaultAutomationScopes(), ",") + ","
	for _, scope := range pagePlatformScopes() {
		if strings.Contains(issued, ","+scope+",") {
			t.Fatalf("legacy content scopes unexpectedly issue %q: %s", scope, issued)
		}
		if strings.Contains(defaults, ","+scope+",") {
			t.Fatalf("legacy defaults unexpectedly include %q: %s", scope, defaults)
		}
	}
}

func TestPagePlatformScopeBadgesAreVisible(t *testing.T) {
	got := strings.Join(automationScopeBadges(strings.Join([]string{
		apiScopePageProjectsRead,
		apiScopePageProjectsBuild,
		apiScopePageCapabilitiesGrant,
	}, ",")), " / ")
	for _, want := range []string{"高级页面", "读取工程", "构建", "批准运行能力"} {
		if !strings.Contains(got, want) {
			t.Fatalf("badge %q missing from %q", want, got)
		}
	}
}
