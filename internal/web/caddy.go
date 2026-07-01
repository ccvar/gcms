package web

import (
	"io"
	"net/http"
	"os"
	"strings"
)

// caddyOnDemandEnabled reports whether the host is running gcms behind Caddy with
// on-demand TLS wired to this app's ask endpoint. Set by scripts/cms.sh caddy-setup
// via GCMS_CADDY_ONDEMAND=1 (read from cms.conf / environment). Drives whether the
// admin UI shows "auto" or manual domain-binding instructions.
func caddyOnDemandEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GCMS_CADDY_ONDEMAND"))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// knownHost reports whether host is a domain this server serves: the platform host,
// the single-site base URL host, or any bound site domain (primary or alias).
func (s *Server) knownHost(host string) bool {
	host = normalizeRuntimeHost(host)
	if host == "" {
		return false
	}
	if h := normalizeRuntimeHost(baseURLHost(s.baseURL)); h != "" && host == h {
		return true
	}
	pool := s.runtimePool()
	if pool == nil {
		return false
	}
	if pool.platformHost != "" && host == pool.platformHost {
		return true
	}
	_, ok := pool.byHost[host]
	return ok
}

// caddyAsk answers Caddy's on-demand TLS ask: 200 for a domain gcms serves, 403
// otherwise, so Caddy only provisions certificates for hosts we actually route.
// Unauthenticated by design — Caddy calls it locally and it only reveals yes/no.
func (s *Server) caddyAsk(w http.ResponseWriter, r *http.Request) {
	domain := r.URL.Query().Get("domain")
	if strings.TrimSpace(domain) == "" {
		http.Error(w, "missing domain", http.StatusBadRequest)
		return
	}
	if s.knownHost(domain) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
		return
	}
	http.Error(w, "unknown domain", http.StatusForbidden)
}
