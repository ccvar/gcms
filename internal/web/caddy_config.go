package web

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cms.ccvar.com/internal/platform"
)

// gcms 自动管理的域名区块标记。标记之外的任何配置一律原样保留。
const (
	caddyDomainsStart = "# >>> gcms domains —— 请勿手动编辑本区块，保存域名时会自动重写 >>>"
	caddyDomainsEnd   = "# <<< gcms domains <<<"
)

// caddyManageEnabled reports whether gcms should write per-domain Caddy blocks and reload
// Caddy when domains are saved. Opt-in via GCMS_CADDY_MANAGE=1 because it writes system
// config and reloads Caddy; off by default (no Caddy files are touched, unchanged behavior).
func caddyManageEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GCMS_CADDY_MANAGE"))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// caddyReverseProxyTarget returns the local address Caddy reverse-proxies to, derived from
// gcms's own ADDR (":8080" -> "127.0.0.1:8080"), mirroring scripts/cms.sh.
func caddyReverseProxyTarget() string {
	addr := strings.TrimSpace(os.Getenv("ADDR"))
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	return addr
}

// caddyConfigFile resolves the file gcms writes its domain blocks into, mirroring
// scripts/cms.sh: /etc/caddy/gcms.caddy when that dir is writable, else <cwd>/gcms.caddy.
func caddyConfigFile() string {
	if caddyDirWritable("/etc/caddy") {
		return "/etc/caddy/gcms.caddy"
	}
	if wd, err := os.Getwd(); err == nil && wd != "" {
		return filepath.Join(wd, "gcms.caddy")
	}
	return "gcms.caddy"
}

// caddyDirWritable reports whether dir exists and gcms can create files in it.
func caddyDirWritable(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	f, err := os.CreateTemp(dir, ".gcms-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// renderCaddyDomainsSection builds the marked Caddy block for every non-default site's
// bound domains: an alias→primary 301 redirect block for each alias set to redirect, a
// shared serving block for the primary plus any non-redirecting aliases, and gzip/zstd
// compression. No tls directive — Caddy's automatic HTTPS (ACME) applies.
func renderCaddyDomainsSection(sites []*platform.Site, domains []*platform.SiteDomain, target string) string {
	defaultID := int64(0)
	for _, st := range sites {
		if st != nil && st.IsDefault {
			defaultID = st.ID
		}
	}
	bySite := map[int64][]*platform.SiteDomain{}
	var order []int64
	for _, d := range domains {
		if d == nil || !d.Enabled || d.SiteID == defaultID {
			continue
		}
		if _, ok := bySite[d.SiteID]; !ok {
			order = append(order, d.SiteID)
		}
		bySite[d.SiteID] = append(bySite[d.SiteID], d)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	var b strings.Builder
	b.WriteString(caddyDomainsStart)
	b.WriteString("\n# 由 gcms 在「保存域名」时自动生成。标记之外的配置不受影响。\n")
	for _, sid := range order {
		var primary *platform.SiteDomain
		for _, d := range bySite[sid] {
			if d.IsPrimary {
				primary = d
				break
			}
		}
		if primary == nil {
			continue // 无主域名，跳过
		}
		scheme := strings.TrimSpace(primary.Scheme)
		if scheme == "" {
			scheme = "https"
		}
		var shared []string
		for _, d := range bySite[sid] {
			if d.IsPrimary {
				continue
			}
			if d.RedirectToPrimary {
				fmt.Fprintf(&b, "\n%s {\n\tredir %s://%s{uri} permanent\n}\n", d.Host, scheme, primary.Host)
			} else {
				shared = append(shared, d.Host)
			}
		}
		hosts := append([]string{primary.Host}, shared...)
		fmt.Fprintf(&b, "\n%s {\n\tencode zstd gzip\n\n\treverse_proxy %s\n}\n", strings.Join(hosts, " "), target)
	}
	b.WriteString(caddyDomainsEnd)
	return b.String()
}

// spliceCaddyManagedSection returns existing with the gcms-domains marked region replaced
// by section (or section appended when no markers are present). Everything outside the
// markers is preserved verbatim, so hand-written configs are never lost.
func spliceCaddyManagedSection(existing, section string) string {
	if strings.TrimSpace(existing) == "" {
		return section + "\n"
	}
	lines := strings.Split(existing, "\n")
	start, end := -1, -1
	for i, ln := range lines {
		switch strings.TrimSpace(ln) {
		case caddyDomainsStart:
			if start < 0 {
				start = i
			}
		case caddyDomainsEnd:
			if start >= 0 && end < 0 {
				end = i
			}
		}
	}
	if start >= 0 && end >= start {
		out := append([]string{}, lines[:start]...)
		out = append(out, section)
		out = append(out, lines[end+1:]...)
		return strings.Join(out, "\n")
	}
	return strings.TrimRight(existing, "\n") + "\n\n" + section + "\n"
}

// writeCaddyDomainsFile writes section into path's gcms-domains region, preserving all
// other content and leaving a .bak of the previous file.
func writeCaddyDomainsFile(path, section string) error {
	var existing []byte
	if b, err := os.ReadFile(path); err == nil {
		existing = b
	} else if !os.IsNotExist(err) {
		return err
	}
	next := spliceCaddyManagedSection(string(existing), section)
	if len(existing) > 0 {
		_ = os.WriteFile(path+".bak", existing, 0o644)
	}
	return os.WriteFile(path, []byte(next), 0o644)
}

// reloadCaddy reloads Caddy so a rewritten gcms.caddy takes effect. It prefers the main
// Caddyfile (which imports gcms.caddy and holds any other sites) over reloading the gcms
// file alone, mirroring scripts/cms.sh so other sites are never dropped.
func reloadCaddy(cfile string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	run := func(name string, args ...string) bool {
		if _, err := exec.LookPath(name); err != nil {
			return false
		}
		return exec.CommandContext(ctx, name, args...).Run() == nil
	}
	if _, err := os.Stat("/etc/caddy/Caddyfile"); err == nil {
		if run("systemctl", "reload", "caddy") {
			return nil
		}
		if run("caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile") {
			return nil
		}
		return fmt.Errorf("reload via main Caddyfile failed")
	}
	if run("caddy", "reload", "--config", cfile, "--adapter", "caddyfile") {
		return nil
	}
	if run("systemctl", "reload", "caddy") {
		return nil
	}
	return fmt.Errorf("reload failed")
}

// syncCaddyDomains regenerates the gcms-managed Caddy domain blocks from all sites and
// reloads Caddy. No-op unless GCMS_CADDY_MANAGE=1 and the `caddy` binary is present.
// Returns a short status for the admin flash (empty when disabled/not applicable).
func (s *Server) syncCaddyDomains() string {
	if !caddyManageEnabled() || s.platform == nil {
		return "" // 未开启 GCMS_CADDY_MANAGE：静默不动 Caddy
	}
	// 已开启：以下任何跳过 / 失败都返回可见提示，便于排查（不再静默）。
	if _, err := exec.LookPath("caddy"); err != nil {
		return "已开启 Caddy 自动写入，但在 gcms 进程的 PATH 中未找到 caddy 命令。"
	}
	sites, err := s.platform.Sites()
	if err != nil {
		return "读取站点失败，未更新 Caddy 配置。"
	}
	domains, err := s.platform.SiteDomains()
	if err != nil {
		return "读取域名失败，未更新 Caddy 配置。"
	}
	section := renderCaddyDomainsSection(sites, domains, caddyReverseProxyTarget())
	path := caddyConfigFile()
	if err := writeCaddyDomainsFile(path, section); err != nil {
		return "Caddy 配置写入失败（" + err.Error() + "），请检查权限。"
	}
	if err := reloadCaddy(path); err != nil {
		return "Caddy 配置已写入 " + path + "，但自动重载失败，请手动执行 systemctl reload caddy。"
	}
	return "已更新并重载 Caddy 配置。"
}
