package web

import (
	"context"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// ProxyInfo describes the reverse proxy in front of gcms, for the wizard's ③反向代理 step.
type ProxyInfo struct {
	Kind           string `json:"kind"` // caddy | nginx | other | none
	Running        bool   `json:"running"`
	GcmsIntegrated bool   `json:"gcms_integrated"` // gcms already wired into Caddy
	CanAutoSync    bool   `json:"can_auto_sync"`   // gcms can run the sync script itself (root + script)
	OnDemand       bool   `json:"on_demand"`       // Caddy on-demand TLS (ask endpoint) enabled
}

// detectReverseProxy classifies the reverse proxy using ONLY fixed-string binary lookups
// (no user input ever reaches exec) plus gcms's own Caddy-integration signals.
func detectReverseProxy() ProxyInfo {
	var p ProxyInfo
	switch {
	case hasBinary("caddy"):
		p.Kind = "caddy"
	case hasBinary("nginx"):
		p.Kind = "nginx"
	default:
		p.Kind = "none"
	}
	p.Running = serviceActive(p.Kind)
	p.OnDemand = caddyOnDemandEnabled()
	p.GcmsIntegrated = p.Kind == "caddy" && (p.OnDemand || caddySyncScriptPath() != "")
	p.CanAutoSync = p.Kind == "caddy" && caddySyncScriptPath() != "" && os.Geteuid() == 0
	return p
}

func hasBinary(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// serviceActive best-effort reports whether the named systemd unit is active. Fixed args,
// no user input; returns false where systemctl is absent (e.g. the darwin dev box).
func serviceActive(kind string) bool {
	if kind != "caddy" && kind != "nginx" {
		return false
	}
	if !hasBinary("systemctl") {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", kind).Run() == nil
}

// ServerIPInfo is the server's public IP for the DNS step (target of the A record).
type ServerIPInfo struct {
	IPv4   string `json:"ipv4"`
	IPv6   string `json:"ipv6"`
	Source string `json:"source"` // config | env | detected | ""
}

// serverPublicIP resolves the server's public IP: stored setting → env → a UDP-dial LocalAddr
// probe (no packet sent, no external service). A private/loopback source (NAT box) is dropped
// so the operator fills it in manually rather than getting a wrong LAN IP.
func (s *Server) serverPublicIP() ServerIPInfo {
	info := ServerIPInfo{Source: "config"}
	if s.platform != nil {
		info.IPv4 = strings.TrimSpace(s.platform.Setting(platformServerIPv4Key))
		info.IPv6 = strings.TrimSpace(s.platform.Setting(platformServerIPv6Key))
	}
	if info.IPv4 == "" {
		if v := strings.TrimSpace(os.Getenv("GCMS_SERVER_IPV4")); v != "" {
			info.IPv4, info.Source = v, "env"
		}
	}
	if info.IPv4 == "" {
		if ip := routedSourceIP("udp4", "1.1.1.1:80"); ip != "" {
			info.IPv4, info.Source = ip, "detected"
		}
	}
	if info.IPv4 == "" && info.IPv6 == "" {
		info.Source = ""
	}
	return info
}

// routedSourceIP returns the local source IP the kernel would use to reach addr, via a
// connectionless UDP "dial" (no packet is actually sent). Private/loopback results are
// dropped (a NAT'd host's source is its LAN IP, not its public IP).
func routedSourceIP(network, addr string) string {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.Dial(network, addr)
	if err != nil {
		return ""
	}
	defer conn.Close()
	ua, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || ua.IP == nil {
		return ""
	}
	if ua.IP.IsPrivate() || ua.IP.IsLoopback() || ua.IP.IsUnspecified() {
		return ""
	}
	return ua.IP.String()
}

// DomainDNSInfo is the DNS classification for the wizard's ②DNS step.
type DomainDNSInfo struct {
	Provider       string   `json:"provider"`         // cloudflare | other | unknown
	NameServers    []string `json:"name_servers"`     //
	ARecords       []string `json:"a_records"`        //
	PointsToServer string   `json:"points_to_server"` // yes | no | via_cloudflare | unknown
	Proxied        bool     `json:"proxied"`
}

// detectDomainDNS classifies host's DNS: Cloudflare (any apex NS ends in .ns.cloudflare.com)
// vs other, and whether its A records point at this server. A Cloudflare-proxied host (A =
// Cloudflare edge IP, not the server) reports via_cloudflare rather than a mismatch.
func (s *Server) detectDomainDNS(ctx context.Context, host string) DomainDNSInfo {
	info := DomainDNSInfo{Provider: "unknown", PointsToServer: "unknown"}
	host = normalizeRuntimeHost(host)
	if host == "" {
		return info
	}
	r := net.DefaultResolver
	if ns, err := r.LookupNS(ctx, apexDomain(host)); err == nil && len(ns) > 0 {
		info.Provider = "other"
		for _, n := range ns {
			name := strings.TrimSuffix(strings.ToLower(n.Host), ".")
			info.NameServers = append(info.NameServers, name)
			if strings.HasSuffix(name, ".ns.cloudflare.com") {
				info.Provider = "cloudflare"
			}
		}
	}
	ips, _ := r.LookupIP(ctx, "ip4", host)
	for _, ip := range ips {
		info.ARecords = append(info.ARecords, ip.String())
	}
	serverIP := s.serverPublicIP().IPv4
	switch {
	case len(info.ARecords) == 0:
		info.PointsToServer = "no"
	case serverIP != "" && slices.Contains(info.ARecords, serverIP):
		info.PointsToServer = "yes"
	case info.Provider == "cloudflare":
		info.Proxied, info.PointsToServer = true, "via_cloudflare"
	default:
		info.PointsToServer = "no"
	}
	return info
}

// apexDomain returns the registrable-ish domain (last two labels) for an NS lookup.
func apexDomain(host string) string {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), ".")
	parts := strings.Split(host, ".")
	if len(parts) <= 2 {
		return host
	}
	return strings.Join(parts[len(parts)-2:], ".")
}
