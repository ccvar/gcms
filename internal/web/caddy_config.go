package web

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cms.ccvar.com/internal/platform"
)

// caddyManifestSentinel is the first line of the manifest, so the sync script can tell a
// real gcms response from an error page or a wrong service before touching any file.
const caddyManifestSentinel = "# gcms-caddy-manifest v1"

// caddyReverseProxyTarget returns the local address Caddy reverse-proxies to, derived from
// gcms's own ADDR (":8080" -> "127.0.0.1:8080"), matching setup-caddy.sh.
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

// caddySiteFilename returns the dedicated Caddy filename for a site keyed by its primary
// host, e.g. "gcms-ubnas.com.caddy". The "gcms-" prefix (with the dash) never collides with
// the installer's own "gcms.caddy", so the sync script's `gcms-*.caddy` orphan sweep can
// never touch the install file. Returns "" for a host with any character unsafe in a
// filename, so a malformed host can never produce a weird path.
func caddySiteFilename(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || strings.HasPrefix(host, ".") || strings.HasPrefix(host, "-") || strings.Contains(host, "..") {
		return ""
	}
	for _, r := range host {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-') {
			return "" // 只允许主机名字符，杜绝路径穿越 / 怪异文件名
		}
	}
	return "gcms-" + host + ".caddy"
}

// renderCaddySiteBlock renders the Caddy config for ONE site: an alias→primary 301 redirect
// block per redirecting alias, plus a serving block for the primary and any non-redirecting
// aliases, with gzip/zstd compression. No tls directive — Caddy automatic HTTPS/ACME applies.
func renderCaddySiteBlock(primary *platform.SiteDomain, aliases []*platform.SiteDomain, target string) string {
	var b strings.Builder
	var shared []string
	for _, d := range aliases {
		if d.RedirectToPrimary {
			// Serving blocks are always auto-HTTPS, so the canonical is https — redirect
			// there regardless of the stored scheme to avoid a downgrade.
			fmt.Fprintf(&b, "%s {\n\tredir https://%s{uri} permanent\n}\n\n", d.Host, primary.Host)
		} else {
			shared = append(shared, d.Host)
		}
	}
	hosts := append([]string{primary.Host}, shared...)
	fmt.Fprintf(&b, "%s {\n\tencode zstd gzip\n\n\treverse_proxy %s\n}\n", strings.Join(hosts, " "), target)
	return b.String()
}

// renderCaddyManifest builds the per-site manifest the sync script consumes: a sentinel line,
// then for every non-default site with a primary domain a "=== <filename> ===" header followed
// by that site's Caddy block. One file per site keeps sites isolated and lets the script
// reconcile safely (write the current set, delete orphaned gcms-*.caddy). Sites keyed by an
// invalid or duplicate primary host are skipped.
func renderCaddyManifest(sites []*platform.Site, domains []*platform.SiteDomain, target string) string {
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

	var body strings.Builder
	seen := map[string]bool{}
	count := 0
	for _, sid := range order {
		var primary *platform.SiteDomain
		var aliases []*platform.SiteDomain
		for _, d := range bySite[sid] {
			if d.IsPrimary {
				if primary == nil {
					primary = d
				}
				continue
			}
			aliases = append(aliases, d)
		}
		if primary == nil {
			continue // 无主域名，跳过
		}
		fname := caddySiteFilename(primary.Host)
		if fname == "" || seen[fname] {
			continue // 非法文件名，或与其它站点主域名撞名
		}
		seen[fname] = true
		fmt.Fprintf(&body, "=== %s ===\n%s", fname, renderCaddySiteBlock(primary, aliases, target))
		count++
	}
	// Header declares the site count so the sync script can tell a legitimate zero-site
	// manifest from a parse failure/truncation (and refuse to wipe on the latter).
	var b strings.Builder
	fmt.Fprintf(&b, "%s sites=%d\n", caddyManifestSentinel, count)
	b.WriteString(body.String())
	return b.String()
}

// caddySiteManifest renders the current per-site Caddy manifest. Always includes the sentinel
// (even with zero sites) so a successful-but-empty response is distinguishable from a failure.
func (s *Server) caddySiteManifest() (string, error) {
	if s.platform == nil {
		return caddyManifestSentinel + " sites=0\n", nil
	}
	sites, err := s.platform.Sites()
	if err != nil {
		return "", err
	}
	domains, err := s.platform.SiteDomains()
	if err != nil {
		return "", err
	}
	return renderCaddyManifest(sites, domains, caddyReverseProxyTarget()), nil
}

// caddyConfDir / caddyMainFile：与同步脚本同一套环境变量与默认值。
func caddyConfDir() string {
	if v := strings.TrimSpace(os.Getenv("CADDY_CONF_DIR")); v != "" {
		return v
	}
	return "/etc/caddy/conf.d"
}

func caddyMainFile() string {
	if v := strings.TrimSpace(os.Getenv("CADDYFILE")); v != "" {
		return v
	}
	return "/etc/caddy/Caddyfile"
}

// caddyConfDirWritable 探测 gcms 能否直接写站点配置目录（root 或目录对进程可写）。
func caddyConfDirWritable() bool {
	dir := caddyConfDir()
	f, err := os.CreateTemp(dir, ".gcms-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// caddyAutoSyncAvailable 报告"保存域名后能否自动写入并重载 Caddy"：
// 直写（conf 目录可写）或脚本（root + 脚本在）任一可行即为真。向导第 3 步据此措辞。
func caddyAutoSyncAvailable() bool {
	if _, err := exec.LookPath("caddy"); err != nil {
		return false
	}
	if caddyConfDirWritable() {
		return true
	}
	return caddySyncScriptPath() != "" && os.Geteuid() == 0
}

// caddyDesiredFiles 计算当前应存在的整组站点文件：文件名 → 配置内容。
// 与 renderCaddyManifest 同一套规则（跳过默认站点/无主域名/非法或撞名文件名）。
func caddyDesiredFiles(sites []*platform.Site, domains []*platform.SiteDomain, target string) map[string]string {
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
	out := map[string]string{}
	for _, sid := range order {
		var primary *platform.SiteDomain
		var aliases []*platform.SiteDomain
		for _, d := range bySite[sid] {
			if d.IsPrimary {
				if primary == nil {
					primary = d
				}
				continue
			}
			aliases = append(aliases, d)
		}
		if primary == nil {
			continue
		}
		fname := caddySiteFilename(primary.Host)
		if fname == "" {
			continue
		}
		if _, dup := out[fname]; dup {
			continue
		}
		out[fname] = renderCaddySiteBlock(primary, aliases, target)
	}
	return out
}

// caddyValidate / caddyReload：与同步脚本同语义（systemctl reload 优先，退回 caddy reload）。
func caddyValidate(ctx context.Context, config string) error {
	return exec.CommandContext(ctx, "caddy", "validate", "--config", config, "--adapter", "caddyfile").Run()
}

func caddyReload(ctx context.Context) bool {
	if _, err := exec.LookPath("systemctl"); err == nil {
		if exec.CommandContext(ctx, "systemctl", "reload", "caddy").Run() == nil {
			return true
		}
	}
	if _, err := os.Stat(caddyMainFile()); err == nil {
		if exec.CommandContext(ctx, "caddy", "reload", "--config", caddyMainFile(), "--adapter", "caddyfile").Run() == nil {
			return true
		}
	}
	return false
}

// applyCaddyDirect 直写模式（应用户要求恢复 v1.0.91 的"保存即写 gcms-<域名>.caddy 并重载"体验）：
// gcms 对 conf.d 有写权限时，按同步脚本的同一套安全语义直接落盘——
// 逐个文件先校验；备份整组 gcms-*.caddy（绝不碰安装文件 gcms.caddy）；原子写 + 清理孤儿；
// 装配后再校验主 Caddyfile（跨站冲突）；重载；任何失败整组回滚。
// 返回 (面向 flash 的结果消息, 是否已处理)；目录不可写等前置不满足时 handled=false，交由脚本/手动路径。
func (s *Server) applyCaddyDirect() (string, bool) {
	if !caddyConfDirWritable() {
		return "", false
	}
	sites, err := s.platform.Sites()
	if err != nil {
		return "Caddy 直写未完成：读取站点失败：" + err.Error(), true
	}
	domains, err := s.platform.SiteDomains()
	if err != nil {
		return "Caddy 直写未完成：读取域名失败：" + err.Error(), true
	}
	desired := caddyDesiredFiles(sites, domains, caddyReverseProxyTarget())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	confDir := caddyConfDir()

	// 1) 落盘前逐个语法校验（写到临时文件校验，不碰 conf.d 正式文件）。
	for name, content := range desired {
		tmp, err := os.CreateTemp("", "gcms-caddy-*.caddy")
		if err != nil {
			return "Caddy 直写未完成：" + err.Error(), true
		}
		_, werr := tmp.WriteString(content)
		_ = tmp.Close()
		if werr == nil {
			werr = caddyValidate(ctx, tmp.Name())
		}
		_ = os.Remove(tmp.Name())
		if werr != nil {
			return "Caddy 直写未完成：站点配置 " + name + " 语法校验未通过，未改动 Caddy。", true
		}
	}

	// 2) 备份当前整组 gcms-*.caddy（内容进内存，供整组回滚）。
	existing := map[string][]byte{}
	entries, _ := os.ReadDir(confDir)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "gcms-") || !strings.HasSuffix(name, ".caddy") {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(confDir, name)); err == nil {
			existing[name] = b
		}
	}
	restore := func() {
		for name := range desired {
			if _, was := existing[name]; !was {
				_ = os.Remove(filepath.Join(confDir, name))
			}
		}
		for name, b := range existing {
			_ = os.WriteFile(filepath.Join(confDir, name), b, 0o644)
		}
	}

	// 3) 原子写目标组 + 删除孤儿（后台已不存在的 gcms-*.caddy）。
	for name, content := range desired {
		tmp := filepath.Join(confDir, name+".tmp")
		if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
			restore()
			return "Caddy 直写未完成：" + err.Error(), true
		}
		if err := os.Rename(tmp, filepath.Join(confDir, name)); err != nil {
			_ = os.Remove(tmp)
			restore()
			return "Caddy 直写未完成：" + err.Error(), true
		}
	}
	removed := 0
	for name := range existing {
		if _, keep := desired[name]; !keep {
			_ = os.Remove(filepath.Join(confDir, name))
			removed++
		}
	}

	// 4) 装配后校验主 Caddyfile（跨站域名冲突等），失败整组回滚。
	if _, err := os.Stat(caddyMainFile()); err == nil {
		if err := caddyValidate(ctx, caddyMainFile()); err != nil {
			restore()
			_ = caddyReload(ctx)
			return "Caddy 直写未完成：写入后主 Caddyfile 校验未通过（域名是否与其它站点冲突？），已整组回滚。", true
		}
	}

	// 5) 重载。
	if !caddyReload(ctx) {
		return fmt.Sprintf("已写入 %d 个站点配置（清理 %d 个），但重载 Caddy 失败，请手动执行 systemctl reload caddy。", len(desired), removed), true
	}
	return fmt.Sprintf("已写入 %d 个 Caddy 站点配置（清理 %d 个）并重载生效。", len(desired), removed), true
}

// applyCaddySites pushes the current sites into Caddy after a domain save. When gcms runs as
// root it executes the sync script directly, so binding a domain takes effect immediately;
// otherwise it returns a reminder to run the sudo script by hand. The script does all the
// privileged, validated, rollback-safe work — gcms never writes Caddy files itself. Returns a
// status line for the admin flash ("" when there is no Caddy on the box).
func (s *Server) applyCaddySites() string {
	if s.platform == nil {
		return ""
	}
	if _, err := exec.LookPath("caddy"); err != nil {
		return "" // 没装 Caddy：不处理、不提示
	}
	// 直写模式优先：conf.d 可写就直接落盘 gcms-<域名>.caddy 并重载（同脚本的校验/回滚语义）。
	if msg, handled := s.applyCaddyDirect(); handled {
		return msg
	}
	script := caddySyncScriptPath()
	if script == "" || os.Geteuid() != 0 {
		// 目录不可写、脚本不在或非 root → 提示手动运行。
		return "域名已保存。让 Caddy 生效请运行：sudo sh scripts/gcms-caddy-sync.sh（或配置定时同步）。"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", script).CombinedOutput()
	msg := lastNonEmptyLine(string(out))
	if err != nil {
		if msg == "" {
			msg = strings.TrimSpace(err.Error())
		}
		return "Caddy 同步未完成：" + msg
	}
	if msg == "" {
		msg = "已同步并重载 Caddy。"
	}
	return msg
}

// caddySyncScriptPath returns the sync script's path (relative to gcms's working directory,
// which cms.sh sets to the install root) when it exists, else "".
func caddySyncScriptPath() string {
	// 不依赖进程 CWD（systemd 的 WorkingDirectory 常不是安装目录）：
	// 依次尝试 CWD 相对路径、可执行文件旁、发布包布局（包根/scripts，二进制在 current/bin/）。
	candidates := []string{"scripts/gcms-caddy-sync.sh"}
	if exe, err := os.Executable(); err == nil {
		d := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(d, "scripts", "gcms-caddy-sync.sh"),
			filepath.Join(d, "..", "scripts", "gcms-caddy-sync.sh"),
			filepath.Join(d, "..", "..", "scripts", "gcms-caddy-sync.sh"),
		)
	}
	for _, p := range candidates {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// lastNonEmptyLine returns the last non-blank line of s — the sync script's summary/error line.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

// caddyConfigHandler serves the per-site Caddy manifest as text/plain for the sync script
// (scripts/gcms-caddy-sync.sh) to apply. Only requests made directly to gcms on loopback are
// served — proxied/public requests are rejected — so it is never reachable through the reverse
// proxy. Read-only; it reveals only already-public domains.
func (s *Server) caddyConfigHandler(w http.ResponseWriter, r *http.Request) {
	if !remoteIsLoopback(r.RemoteAddr) || !hostIsLoopback(r.Host) || r.Header.Get("X-Forwarded-For") != "" {
		http.NotFound(w, r)
		return
	}
	content, err := s.caddySiteManifest()
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, content)
}

// hostIsLoopback reports whether a Host header names a loopback address (127.0.0.1/::1/
// localhost, optional port). Distinguishes a direct local curl from a proxied public request.
func hostIsLoopback(hostport string) bool {
	h := strings.TrimSpace(hostport)
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.ToLower(strings.Trim(h, "[]"))
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// remoteIsLoopback reports whether a RemoteAddr is a loopback IP.
func remoteIsLoopback(remoteAddr string) bool {
	h := remoteAddr
	if hh, _, err := net.SplitHostPort(remoteAddr); err == nil {
		h = hh
	}
	ip := net.ParseIP(strings.TrimSpace(h))
	return ip != nil && ip.IsLoopback()
}
