package web

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"
)

// VerifyResult is the wizard's ④验证 outcome. OK means the domain returned 200 AND was
// served by a gcms instance (X-Gcms header).
type VerifyResult struct {
	OK           bool   `json:"ok"`
	Status       int    `json:"status"`
	ServedByGcms bool   `json:"served_by_gcms"`
	Reason       string `json:"reason"` // ok | not_gcms | bad_status | unreachable | bad_host
}

// verifyDomainReachable does an SSRF-hardened HTTPS GET of https://host/ and reports whether
// it is reachable, returns 200, and is served by gcms. The SSRF guard runs on the ACTUAL
// dialed IP (post-DNS-resolution), so a DNS-rebind to an internal address is still refused at
// connect time; redirects are capped and re-guarded; proxy env is ignored.
func verifyDomainReachable(ctx context.Context, host string) VerifyResult {
	res := VerifyResult{Reason: "bad_host"}
	if host == "" {
		return res
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/", nil)
	if err != nil {
		return res
	}
	req.Header.Set("User-Agent", "gcms-verify/1")
	resp, err := ssrfSafeHTTPClient(6 * time.Second).Do(req)
	if err != nil {
		res.Reason = "unreachable"
		return res
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) // drain a bounded amount
	res.Status = resp.StatusCode
	res.ServedByGcms = resp.Header.Get("X-Gcms") != ""
	switch {
	case resp.StatusCode == http.StatusOK && res.ServedByGcms:
		res.OK, res.Reason = true, "ok"
	case resp.StatusCode == http.StatusOK:
		res.Reason = "not_gcms"
	default:
		res.Reason = "bad_status"
	}
	return res
}

// ssrfSafeHTTPClient builds a client that only ever connects to public unicast addresses.
// The dialer Control hook fires per connection (including followed redirects), checking the
// resolved IP, so it is immune to DNS rebinding. It is NOT http.DefaultClient.
func ssrfSafeHTTPClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout: timeout,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil || !ipAllowedForVerify(ip) {
				return fmt.Errorf("refused non-public address: %s", address)
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 nil, // never honor HTTP(S)_PROXY env
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			DisableKeepAlives:     true,
		},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 2 {
				return fmt.Errorf("too many redirects")
			}
			return nil // each hop re-dials → the Control hook re-guards its IP
		},
	}
}

// ipAllowedForVerify reports whether ip is a public unicast address safe to fetch. It rejects
// loopback, RFC1918/ULA private, link-local, CGNAT, benchmark, and unspecified/multicast — on
// both IPv4 and IPv4-mapped IPv6.
func ipAllowedForVerify(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 0: // 0.0.0.0/8
			return false
		case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127: // 100.64.0.0/10 CGNAT
			return false
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 0: // 192.0.0.0/24
			return false
		case v4[0] == 198 && (v4[1] == 18 || v4[1] == 19): // 198.18.0.0/15 benchmark
			return false
		}
	}
	return true
}
