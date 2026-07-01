package web

import "testing"

func boolp(b bool) *bool { return &b }

func TestPlanCloudflareRecord(t *testing.T) {
	// No existing records -> create a fresh one.
	if p := planCloudflareRecord(nil, "A", "1.2.3.4", false); !p.Create || p.UpdateID != "" || len(p.DeleteIDs) != 0 {
		t.Errorf("empty: got %+v, want create", p)
	}

	// Existing record already correct (same content + same proxy) -> no-op.
	recs := []cloudflareDNSRecord{{ID: "r-a", Type: "A", Content: "1.2.3.4", Proxied: boolp(false)}}
	if p := planCloudflareRecord(recs, "A", "1.2.3.4", false); p.Create || p.UpdateID != "" || len(p.DeleteIDs) != 0 {
		t.Errorf("match: got %+v, want no-op", p)
	}

	// Different content -> update in place.
	if p := planCloudflareRecord(recs, "A", "9.9.9.9", false); p.UpdateID != "r-a" || p.Create {
		t.Errorf("diff content: got %+v, want update r-a", p)
	}

	// Same content but proxy mode differs -> update to normalize the proxy mode.
	if p := planCloudflareRecord(recs, "A", "1.2.3.4", true); p.UpdateID != "r-a" {
		t.Errorf("diff proxy: got %+v, want update r-a", p)
	}

	// Lowercase record type still matches.
	if p := planCloudflareRecord(recs, "a", "1.2.3.4", false); p.Create || p.UpdateID != "" {
		t.Errorf("lowercase type: got %+v, want no-op", p)
	}

	// A conflicting CNAME must be deleted before creating the A (CF error 81054).
	cn := []cloudflareDNSRecord{{ID: "r-cn", Type: "CNAME", Content: "x.pages.dev", Proxied: boolp(true)}}
	if p := planCloudflareRecord(cn, "A", "1.2.3.4", false); !p.Create || len(p.DeleteIDs) != 1 || p.DeleteIDs[0] != "r-cn" {
		t.Errorf("cname conflict: got %+v, want delete r-cn + create", p)
	}

	// The ubnas.com case: the correct grey-cloud A already exists alongside a stale
	// proxied A from a previous deployment -> keep the correct one, delete the stale one.
	dup := []cloudflareDNSRecord{
		{ID: "keep", Type: "A", Content: "47.78.75.160", Proxied: boolp(false)},
		{ID: "stale", Type: "A", Content: "3.33.130.190", Proxied: boolp(true)},
	}
	if p := planCloudflareRecord(dup, "A", "47.78.75.160", false); p.Create || p.UpdateID != "" || len(p.DeleteIDs) != 1 || p.DeleteIDs[0] != "stale" {
		t.Errorf("duplicate A: got %+v, want delete stale only", p)
	}

	// Same duplicate case but the stale proxied record is listed first -> that record is
	// updated to the server IP (grey-cloud) and the extra one is removed. Either way the
	// host converges to a single correct record.
	dup2 := []cloudflareDNSRecord{
		{ID: "stale", Type: "A", Content: "3.33.130.190", Proxied: boolp(true)},
		{ID: "other", Type: "A", Content: "47.78.75.160", Proxied: boolp(false)},
	}
	if p := planCloudflareRecord(dup2, "A", "47.78.75.160", false); p.UpdateID != "stale" || len(p.DeleteIDs) != 1 || p.DeleteIDs[0] != "other" {
		t.Errorf("duplicate A (stale first): got %+v, want update stale + delete other", p)
	}
}
