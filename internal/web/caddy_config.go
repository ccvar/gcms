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

// caddyManageEnabled reports whether gcms should write the Caddy domain file and reload
// Caddy on domain save. An explicit GCMS_CADDY_MANAGE wins (1/true/on/yes → on,
// 0/false/off/no → off). When it is unset, gcms auto-enables if the setup-caddy.sh layout
// is present — /etc/caddy/conf.d/gcms.caddy already exists — since that file is created by
// setup-caddy.sh and declared gcms-managed. This lets those installs work without any
// extra env plumbing (avoiding the cms.conf/whitelist dependency), while other setups stay
// untouched unless the flag is explicitly turned on.
func caddyManageEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GCMS_CADDY_MANAGE"))) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	}
	if _, err := os.Stat("/etc/caddy/conf.d/gcms.caddy"); err == nil {
		return true
	}
	return false
}

// caddyReverseProxyTarget returns the local address Caddy reverse-proxies to, derived from
// gcms's own ADDR (":8080" -> "127.0.0.1:8080"), matching setup-caddy.sh / scripts/cms.sh.
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

// caddyConfigFile resolves the file gcms owns for its domain blocks. It prefers the
// setup-caddy.sh layout — /etc/caddy/conf.d/gcms.caddy, picked up by the main Caddyfile's
// `import /etc/caddy/conf.d/*.caddy` — then /etc/caddy/gcms.caddy, then <cwd>/gcms.caddy.
func caddyConfigFile() string {
	if caddyDirWritable("/etc/caddy/conf.d") {
		return "/etc/caddy/conf.d/gcms.caddy"
	}
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

// renderCaddyDomainsFile builds the entire gcms-owned Caddy file for every non-default
// site's bound domains: one alias→primary 301 redirect block per redirecting alias, a
// shared serving block for the primary plus any non-redirecting aliases, and gzip/zstd
// compression — matching the setup-caddy.sh template. No tls directive (Caddy's automatic
// HTTPS/ACME applies). gcms owns this file outright, so it is written wholesale.
func renderCaddyDomainsFile(sites []*platform.Site, domains []*platform.SiteDomain, target string) string {
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
	b.WriteString("# 由 gcms 自动生成（保存域名时重写）。本文件由 gcms 独占管理，请勿手动编辑。\n")
	b.WriteString("# 生效方式：主 Caddyfile 的 `import /etc/caddy/conf.d/*.caddy`。\n")
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
	return b.String()
}

// writeCaddyDomainsFile writes content to path wholesale (gcms owns the file), backing up
// the previous version to path+".bak" first.
func writeCaddyDomainsFile(path, content string) error {
	if existing, err := os.ReadFile(path); err == nil && len(existing) > 0 {
		_ = os.WriteFile(path+".bak", existing, 0o644)
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// reloadCaddy reloads Caddy so a rewritten domain file takes effect. It prefers the main
// Caddyfile (which imports the gcms file and holds any other sites) over reloading the gcms
// file alone, mirroring setup-caddy.sh so other sites are never dropped.
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

// syncCaddyDomains rewrites the gcms-owned Caddy domain file from all sites and reloads
// Caddy. No-op unless GCMS_CADDY_MANAGE=1. Once enabled, every outcome returns a visible
// status for the admin flash so failures are never silent.
func (s *Server) syncCaddyDomains() string {
	if !caddyManageEnabled() || s.platform == nil {
		return "" // 未开启 GCMS_CADDY_MANAGE：静默不动 Caddy
	}
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
	content := renderCaddyDomainsFile(sites, domains, caddyReverseProxyTarget())
	path := caddyConfigFile()
	if err := writeCaddyDomainsFile(path, content); err != nil {
		return "Caddy 配置写入失败（" + err.Error() + "），请检查权限。"
	}
	if err := reloadCaddy(path); err != nil {
		return "Caddy 配置已写入 " + path + "，但自动重载失败，请手动执行 systemctl reload caddy。"
	}
	return "已更新并重载 Caddy 配置（" + path + "）。"
}
