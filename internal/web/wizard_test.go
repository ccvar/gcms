package web

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestIPAllowedForVerify(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", // loopback
		"10.0.0.5", "172.16.0.1", "192.168.1.1", "fc00::1", // private / ULA
		"169.254.1.1", "fe80::1", // link-local
		"100.64.0.1",           // CGNAT
		"0.0.0.0",              // unspecified / 0/8
		"198.18.0.1",           // benchmark
		"224.0.0.1", "ff02::1", // multicast
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", // IPv4-mapped internal
	}
	for _, s := range blocked {
		if ipAllowedForVerify(net.ParseIP(s)) {
			t.Errorf("ipAllowedForVerify(%s) = true, want false (must be blocked)", s)
		}
	}
	allowed := []string{"1.1.1.1", "8.8.8.8", "47.78.75.160", "2606:4700:4700::1111"}
	for _, s := range allowed {
		if !ipAllowedForVerify(net.ParseIP(s)) {
			t.Errorf("ipAllowedForVerify(%s) = false, want true (public)", s)
		}
	}
}

// TestSSRFClientRefusesLoopback proves end-to-end that the verify client refuses to connect
// to a loopback address, even to a real server — the core SSRF protection.
func TestSSRFClientRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Gcms", "1")
	}))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil) // http://127.0.0.1:port
	if _, err := ssrfSafeHTTPClient(2 * time.Second).Do(req); err == nil {
		t.Fatal("SSRF client connected to loopback server; the dialer guard must refuse it")
	}
}

func TestWizardHostParam(t *testing.T) {
	parse := func(raw string) (string, error) {
		return wizardHostParam(httptest.NewRequest(http.MethodGet, "/x?host="+url.QueryEscape(raw), nil))
	}
	if h, err := parse("ubnas.com"); err != nil || h != "ubnas.com" {
		t.Errorf("ubnas.com -> %q, %v", h, err)
	}
	if h, err := parse("WWW.UBNAS.COM"); err != nil || h != "www.ubnas.com" {
		t.Errorf("case-fold -> %q, %v", h, err)
	}
	for _, bad := range []string{"", "localhost", "127.0.0.1", "server", "10.0.0.1", "::1", "nodot"} {
		if h, err := parse(bad); err == nil {
			t.Errorf("wizardHostParam(%q) = %q accepted, want rejected", bad, h)
		}
	}
}

func TestApexDomain(t *testing.T) {
	for in, want := range map[string]string{
		"ubnas.com":       "ubnas.com",
		"www.ubnas.com":   "ubnas.com",
		"a.b.example.org": "example.org",
		"EXAMPLE.COM.":    "example.com",
	} {
		if got := apexDomain(in); got != want {
			t.Errorf("apexDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectReverseProxyShape(t *testing.T) {
	// On the dev box this just must not panic and must return a valid kind.
	p := detectReverseProxy()
	switch p.Kind {
	case "caddy", "nginx", "other", "none":
	default:
		t.Errorf("detectReverseProxy kind = %q, want one of caddy/nginx/other/none", p.Kind)
	}
}
