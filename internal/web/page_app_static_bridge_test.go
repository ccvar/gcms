package web

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cms.ccvar.com/internal/store"
)

func TestPageAppStaticExportRequiresDistinctOriginForApprovedBridge(t *testing.T) {
	fixture := createPageAppE2EFixture(
		t, validPageAppFilesForTest(), "app-static-bridge",
	)
	if _, err := fixture.Server.store.UpsertPageCapabilityGrant(
		store.UpsertPageCapabilityGrantInput{
			ProjectID: fixture.Project.ID, Capability: "client.storage",
			ConfigJSON: `{"max_bytes":4096}`, Status: store.PageCapabilityApproved,
			RequestedBy: "admin:test", ApprovedBy: "admin:test",
		},
	); err != nil {
		t.Fatalf("approve bridge capability: %v", err)
	}
	if _, _, err := fixture.Server.store.PublishPageProject(
		store.PublishPageProjectInput{
			ProjectID: fixture.Project.ID, RevisionID: fixture.Revision.ID,
			BuildID:                   fixture.Build.ID,
			ExpectedWorkingRevisionID: fixture.Revision.ID,
			Action:                    store.PagePublicationPublish, ApprovalID: "admin-session",
			ActorID: "admin:test", Origin: store.PageOriginAdmin,
			RequestID:      "app-static-bridge-publish",
			DeliveryStatus: store.PageDeliveryQueued,
		},
	); err != nil {
		t.Fatalf("publish bridge app: %v", err)
	}

	baseConfig := CloudflareConfig{
		DeployMode: cloudflareModePages, PagesProjectName: "app-static-bridge",
		RoutePattern: "public.example/*",
		Domains:      []CloudflareDomain{{Host: "public.example", Primary: true}},
		DefaultLang:  "zh", Locales: []string{"zh", "en"},
	}
	for _, test := range []struct {
		name   string
		origin string
		want   string
	}{
		{name: "missing", want: "OriginURL"},
		{name: "http_origin", origin: "http://origin.example", want: "HTTPS"},
		{name: "public_domain", origin: "https://public.example", want: "OriginURL"},
		{name: "pages_dev", origin: "https://app-static-bridge.pages.dev", want: "OriginURL"},
		{name: "other_pages_dev", origin: "https://other-project.pages.dev", want: "OriginURL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := baseConfig
			cfg.OriginURL = test.origin
			result, err := fixture.Server.exportStaticSite(context.Background(), cfg)
			if result != nil {
				_ = os.RemoveAll(result.Dir)
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("static bridge origin %q should fail closed, result=%+v err=%v",
					test.origin, result, err)
			}
		})
	}

	baseConfig.OriginURL = "https://origin.example"
	result, err := fixture.Server.exportStaticSite(context.Background(), baseConfig)
	if err != nil {
		t.Fatalf("static export with distinct bridge origin: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(result.Dir) })
	html, err := os.ReadFile(filepath.Join(
		result.Dir, "zh", "app-static-bridge", "index.html",
	))
	if err != nil {
		t.Fatal(err)
	}
	bridgeURL := "https://origin.example/_gcms/page-app-bridge/" +
		strconv.FormatInt(fixture.Project.ID, 10) + "/" +
		strconv.FormatInt(fixture.Revision.ID, 10)
	if !strings.Contains(string(html), `"bridge_url":"`+bridgeURL+`"`) {
		t.Fatalf("static shell does not use distinct bridge origin: %s", html)
	}
	headers, err := os.ReadFile(filepath.Join(result.Dir, "_headers"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(
		string(headers), "connect-src 'self' https://origin.example",
	) {
		t.Fatalf("static shell CSP does not allow bridge origin: %s", headers)
	}
}

func TestPageAppStaticExportIgnoresStaleGrantNotDeclaredByPublishedRevision(t *testing.T) {
	fixture := createPageAppE2EFixture(
		t, pageAppFilesWithoutCapabilities(), "app-static-no-bridge",
	)
	// Capability grants intentionally outlive individual revisions. A grant
	// left by an older revision must not make a later pure-client manifest
	// depend on the server Bridge.
	if _, err := fixture.Server.store.UpsertPageCapabilityGrant(
		store.UpsertPageCapabilityGrantInput{
			ProjectID: fixture.Project.ID, Capability: "client.storage",
			ConfigJSON: `{"max_bytes":4096}`, Status: store.PageCapabilityApproved,
			RequestedBy: "admin:test", ApprovedBy: "admin:test",
		},
	); err != nil {
		t.Fatalf("create stale bridge grant: %v", err)
	}
	if _, _, err := fixture.Server.store.PublishPageProject(
		store.PublishPageProjectInput{
			ProjectID: fixture.Project.ID, RevisionID: fixture.Revision.ID,
			BuildID:                   fixture.Build.ID,
			ExpectedWorkingRevisionID: fixture.Revision.ID,
			Action:                    store.PagePublicationPublish, ApprovalID: "admin-session",
			ActorID: "admin:test", Origin: store.PageOriginAdmin,
			RequestID:      "app-static-no-bridge-publish",
			DeliveryStatus: store.PageDeliveryQueued,
		},
	); err != nil {
		t.Fatalf("publish pure-client app: %v", err)
	}

	result, err := fixture.Server.exportStaticSite(context.Background(), CloudflareConfig{
		DeployMode: cloudflareModePages, PagesProjectName: "app-static-no-bridge",
		RoutePattern: "public.example/*",
		Domains:      []CloudflareDomain{{Host: "public.example", Primary: true}},
		DefaultLang:  "zh", Locales: []string{"zh", "en"},
	})
	if err != nil {
		t.Fatalf("pure-client published manifest should export without OriginURL: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(result.Dir) })
	if _, err := os.Stat(filepath.Join(
		result.Dir, "zh", "app-static-no-bridge", "index.html",
	)); err != nil {
		t.Fatalf("pure-client app was not exported: %v", err)
	}
}
