package web

import (
	"testing"

	"cms.ccvar.com/internal/platform"
)

func TestControlPublicAccessProxyStateSeparatesIntentFromActualDNS(t *testing.T) {
	srv, _, _, _, site := setupPlatformAutomation(t)
	state := newControlPublicAccessProxyState("blog.test", true)
	if err := srv.saveControlPublicAccessProxyState(site.ID, state); err != nil {
		t.Fatalf("save proxy intent: %v", err)
	}

	pending := srv.controlPublicAccessProxyView(site.ID, "blog.test", false, false)
	if !pending.Requested || pending.Actual || pending.Status != publicAccessProxyPending {
		t.Fatalf("pending view = %#v", pending)
	}

	proxiedOnly := srv.controlPublicAccessProxyView(site.ID, "blog.test", true, false)
	if !proxiedOnly.Requested || !proxiedOnly.Actual || proxiedOnly.Status != publicAccessProxyEnabled {
		t.Fatalf("proxied-only view = %#v", proxiedOnly)
	}
	stillPending, ok := srv.loadControlPublicAccessProxyState(site.ID)
	if !ok || stillPending.AccessState != publicAccessProgressPending || stillPending.VerifiedAt != 0 {
		t.Fatalf("Cloudflare-only detection marked access ready: %#v ok=%v", stillPending, ok)
	}

	enabled := srv.controlPublicAccessProxyView(site.ID, "blog.test", true, true)
	if !enabled.Requested || !enabled.Actual || enabled.Status != publicAccessProxyEnabled || enabled.Error != "" {
		t.Fatalf("enabled view = %#v", enabled)
	}
	verified, ok := srv.loadControlPublicAccessProxyState(site.ID)
	if !ok || verified.AccessState != publicAccessProgressReady || verified.VerifiedAt == 0 {
		t.Fatalf("verified origin did not mark access ready: %#v ok=%v", verified, ok)
	}

	state.Status = publicAccessProxyEnabled
	if err := srv.saveControlPublicAccessProxyState(site.ID, state); err != nil {
		t.Fatalf("save enabled state: %v", err)
	}
	missingActual := srv.controlPublicAccessProxyView(site.ID, "blog.test", false, false)
	if !missingActual.Requested || missingActual.Actual || missingActual.Status != publicAccessProxyFailed || missingActual.Error == "" {
		t.Fatalf("missing actual proxy view = %#v", missingActual)
	}

	// A state from an older domain must not leak into a later rebind.
	stale := srv.controlPublicAccessProxyView(site.ID, "new-blog.test", false, false)
	if stale.Requested || stale.Actual || stale.Status != publicAccessProxyDisabled {
		t.Fatalf("stale-domain view = %#v", stale)
	}
}

func TestCloudflareDNSRecordsProxied(t *testing.T) {
	proxied := true
	grey := false
	tests := []struct {
		name    string
		records []cloudflareDNSRecord
		actual  bool
		known   bool
	}{
		{name: "empty"},
		{name: "ignores non route records", records: []cloudflareDNSRecord{{Type: "TXT"}}, actual: false, known: false},
		{name: "orange", records: []cloudflareDNSRecord{{Type: "A", Proxied: &proxied}}, actual: true, known: true},
		{name: "mixed stays incomplete", records: []cloudflareDNSRecord{{Type: "A", Proxied: &proxied}, {Type: "AAAA", Proxied: &grey}}, actual: false, known: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, known := cloudflareDNSRecordsProxied(tt.records)
			if actual != tt.actual || known != tt.known {
				t.Fatalf("actual=%v known=%v", actual, known)
			}
		})
	}
}

func TestControlPublicAccessProxyOlderWorkerCannotOverwriteNewIntent(t *testing.T) {
	srv, _, _, _, site := setupPlatformAutomation(t)
	first := newControlPublicAccessProxyState("blog.test", true)
	if err := srv.saveControlPublicAccessProxyState(site.ID, first); err != nil {
		t.Fatalf("save first state: %v", err)
	}
	second := newControlPublicAccessProxyState("blog.test", false)
	if first.Generation == second.Generation {
		second.Generation += "-new"
	}
	if err := srv.saveControlPublicAccessProxyState(site.ID, second); err != nil {
		t.Fatalf("save second state: %v", err)
	}

	first.Status = publicAccessProxyEnabled
	if srv.updateControlPublicAccessProxyState(site.ID, first) {
		t.Fatal("stale worker unexpectedly overwrote the newer intent")
	}
	got, ok := srv.loadControlPublicAccessProxyState(site.ID)
	if !ok || got.Requested || got.Generation != second.Generation || got.Status != publicAccessProxyDisabled {
		t.Fatalf("current state = %#v, ok=%v", got, ok)
	}
}

func TestPrimaryHostFromDomainSpecs(t *testing.T) {
	specs := []platform.SiteDomainSpec{
		{Scheme: "https", Host: "www.example.com", Redirect: true},
		{Scheme: "https", Host: "CMS.Example.com", Primary: true},
	}
	if got := primaryHostFromDomainSpecs(specs); got != "cms.example.com" {
		t.Fatalf("primary host = %q", got)
	}
}

func TestDiscoverySitePublicAccessSummary(t *testing.T) {
	srv, _, _, _, site := setupPlatformAutomation(t)
	domains := []*platform.SiteDomain{{
		SiteID: site.ID, Scheme: "https", Host: "blog.test", IsPrimary: true, Enabled: true,
	}}
	// Waiting for DNS / HTTPS is public-access state, not an orange-cloud
	// preference. A grey-cloud/manual flow must expose the same card summary.
	state := newControlPublicAccessProxyState("blog.test", false)
	if err := srv.saveControlPublicAccessProxyState(site.ID, state); err != nil {
		t.Fatalf("save pending state: %v", err)
	}

	summary := srv.discoverySitePublicAccess(site.ID, domains)
	if summary == nil || summary.State != "pending" || summary.Stage != "dns" || summary.Host != "blog.test" {
		t.Fatalf("pending DNS summary = %#v", summary)
	}

	state.AccessState = publicAccessProgressPending
	state.AccessStage = "https"
	state.AccessMessage = "DNS 已生效，正在等待源站 HTTPS 验证通过。"
	if err := srv.saveControlPublicAccessProxyState(site.ID, state); err != nil {
		t.Fatalf("save HTTPS state: %v", err)
	}
	summary = srv.discoverySitePublicAccess(site.ID, domains)
	if summary == nil || summary.Stage != "https" {
		t.Fatalf("pending HTTPS summary = %#v", summary)
	}

	state.Status = publicAccessProxyFailed
	state.AccessState = publicAccessProgressAttention
	state.AccessStage = "dns"
	state.AccessMessage = "正在等待 DNS 生效。 等待超时，可稍后重试。"
	state.Error = "正在等待 DNS 生效。 等待超时，可稍后重试。"
	if err := srv.saveControlPublicAccessProxyState(site.ID, state); err != nil {
		t.Fatalf("save failed state: %v", err)
	}
	summary = srv.discoverySitePublicAccess(site.ID, domains)
	if summary == nil || summary.State != "attention" || summary.Stage != "dns" || !summary.CanClear ||
		summary.Generation == "" || summary.DomainFingerprint == "" {
		t.Fatalf("attention summary = %#v", summary)
	}

	state.Status = publicAccessProxyEnabled
	state.AccessState = publicAccessProgressReady
	state.AccessStage = "ready"
	state.AccessMessage = ""
	state.Error = ""
	state.VerifiedAt = 123
	if err := srv.saveControlPublicAccessProxyState(site.ID, state); err != nil {
		t.Fatalf("save ready state: %v", err)
	}
	summary = srv.discoverySitePublicAccess(site.ID, domains)
	if summary == nil || summary.State != "ready" || summary.Stage != "ready" || summary.CanClear {
		t.Fatalf("ready summary = %#v", summary)
	}
}
