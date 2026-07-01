package web

import "testing"

func TestChooseDNSAction(t *testing.T) {
	recs := []cloudflareDNSRecord{
		{ID: "r-a", Type: "A", Name: "a.com", Content: "1.2.3.4"},
		{ID: "r-aaaa", Type: "AAAA", Name: "a.com", Content: "2001:db8::1"},
	}
	cases := []struct {
		name       string
		recordType string
		content    string
		wantOp     string
		wantID     string
	}{
		{"A same content -> skip", "A", "1.2.3.4", "skip", "r-a"},
		{"A different content -> update", "A", "9.9.9.9", "update", "r-a"},
		{"AAAA different -> update", "AAAA", "2001:db8::2", "update", "r-aaaa"},
		{"CNAME missing -> create", "CNAME", "x.pages.dev", "create", ""},
		{"lowercase type still matches", "a", "1.2.3.4", "skip", "r-a"},
	}
	for _, c := range cases {
		got := chooseDNSAction(recs, c.recordType, c.content)
		if got.Op != c.wantOp || got.RecordID != c.wantID {
			t.Errorf("%s: got {%s %s}, want {%s %s}", c.name, got.Op, got.RecordID, c.wantOp, c.wantID)
		}
	}

	// No existing records at all -> always create.
	if got := chooseDNSAction(nil, "A", "1.2.3.4"); got.Op != "create" {
		t.Errorf("empty existing: got %s, want create", got.Op)
	}
}
