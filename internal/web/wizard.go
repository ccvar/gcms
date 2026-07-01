package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// adminWizardProxyDetect (GET, read-only, no egress to a user host → no CSRF, like
// adminServerHealth) reports the reverse proxy + this server's public IP + gcms's local port,
// so the wizard's ③反向代理 step can render the right guidance on open.
func (s *Server) adminWizardProxyDetect(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"proxy":     detectReverseProxy(),
		"server_ip": s.serverPublicIP(),
		"target":    caddyReverseProxyTarget(),
	})
}

// adminWizardDNSDetect (POST + CSRF: it triggers an outbound DNS lookup) classifies the typed
// domain's DNS for the ②DNS step.
func (s *Server) adminWizardDNSDetect(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	host, err := wizardHostParam(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_host"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.detectDomainDNS(ctx, host))
}

// adminWizardVerify (POST + CSRF: it triggers an outbound HTTPS fetch) runs the ④验证 check.
func (s *Server) adminWizardVerify(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkCSRF(w, r); !ok {
		return
	}
	host, err := wizardHostParam(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_host"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 9*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, verifyDomainReachable(ctx, host))
}

// DomainStage is the card's binding-progress stage for a site's primary bound domain.
type DomainStage struct {
	Stage  string `json:"stage"` // none | dns | pending | ok
	Host   string `json:"host"`
	Reason string `json:"reason"`
}

// adminWizardStatus reports how far the site's primary bound domain has progressed toward
// being live, for the card's status chip. GET (like adminServerHealth); the domain is already
// bound in the DB (not attacker-chosen) and the reachability probe is SSRF-guarded.
func (s *Server) adminWizardStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := adminSiteID(w, r)
	if !ok {
		return
	}
	host := s.sitePrimaryHost(id)
	if host == "" {
		writeJSON(w, http.StatusOK, DomainStage{Stage: "none"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	st := DomainStage{Host: host}
	if s.detectDomainDNS(ctx, host).PointsToServer == "no" {
		st.Stage = "dns" // A 记录还没指向本机
		writeJSON(w, http.StatusOK, st)
		return
	}
	if v := verifyDomainReachable(ctx, host); v.OK {
		st.Stage = "ok"
	} else {
		st.Stage, st.Reason = "pending", v.Reason
	}
	writeJSON(w, http.StatusOK, st)
}

// sitePrimaryHost returns the enabled primary domain host bound to the site, or "".
func (s *Server) sitePrimaryHost(id int64) string {
	if s.platform == nil {
		return ""
	}
	domains, err := s.platform.SiteDomains()
	if err != nil {
		return ""
	}
	for _, d := range domains {
		if d != nil && d.SiteID == id && d.IsPrimary && d.Enabled {
			return d.Host
		}
	}
	return ""
}

// wizardHostParam extracts and validates the "host" parameter: it must be a well-formed public
// FQDN. IP literals, localhost, and single-label names are rejected so the DNS/verify probes
// can't be pointed at internal names (the verify path also has the dialer-level SSRF guard).
func wizardHostParam(r *http.Request) (string, error) {
	raw := strings.TrimSpace(r.FormValue("host"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("host"))
	}
	if raw == "" {
		return "", fmt.Errorf("empty host")
	}
	_, host, err := parseSiteDomainInput(raw)
	if err != nil {
		return "", err
	}
	host = normalizeRuntimeHost(host)
	if host == "" || host == "localhost" || net.ParseIP(host) != nil || !strings.Contains(host, ".") {
		return "", fmt.Errorf("not a public domain")
	}
	return host, nil
}
