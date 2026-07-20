//! 远程 GCMS 安装与公网 HTTPS 接入。
//!
//! 公网接入按硬闸顺序执行：域名校验 → 服务器公网 IP / DNS 托管识别 → Cloudflare
//! 真实源站核验（若适用）→ Caddy 只读预检 → 备份后配置。只有用户明确点击时才会
//! 幂等创建一条灰云 A 记录；不会覆盖 DNS、自动创建 AAAA 或替用户切换橙云。

use super::{cf, ensure_ssh, keychain, AppState};
use serde::{Deserialize, Serialize};
use std::collections::HashSet;
use std::net::{IpAddr, Ipv4Addr};
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tauri::ipc::Channel;

/// 官方 install.sh 的标准结构：root 默认 /opt/gcms，普通用户默认 $HOME/gcms。
const GCMS_REMOTE_PROBE_CMD: &str = r#"
root=''
probe_root() {
  d=$1
  if [ -x "$d/scripts/cms.sh" ] && [ -x "$d/current/bin/cms" ] && [ -d "$d/releases" ] && [ -d "$d/shared" ]; then
    root=$d
    return 0
  fi
  return 1
}
probe_root /opt/gcms || { [ -n "${HOME:-}" ] && probe_root "$HOME/gcms"; } || true
if [ -z "$root" ]; then
  printf 'PILOT_GCMS_INSTALLED\t0\n'
  exit 0
fi
build="$root/current/BUILD_INFO"
[ -f "$build" ] || build="$root/BUILD_INFO"
version=''
if [ -f "$build" ]; then
  version=$(awk -F= '$1 == "VERSION" { sub(/^[^=]*=/, ""); print; exit }' "$build" 2>/dev/null || true)
fi
running=0
if [ -s "$root/run/cms.pid" ]; then
  pid=$(cat "$root/run/cms.pid" 2>/dev/null || true)
  [ -n "$pid" ] && { kill -0 "$pid" 2>/dev/null || [ -d "/proc/$pid" ]; } && running=1
fi
# 某些安装由 systemd、容器或旧版脚本托管，服务正常时未必保留标准 PID 文件。
# PID 未命中时再探测 GCMS 本机监听地址；任意有效 HTTP 响应都说明服务正在运行。
if [ "$running" = 0 ] && command -v curl >/dev/null 2>&1; then
  addr=$(awk -F= '$1 == "ADDR" { sub(/^[^=]*=/, ""); gsub(/^[[:space:]]+|[[:space:]]+$/, ""); print; exit }' "$root/shared/cms.conf" 2>/dev/null || true)
  port=$(printf '%s' "${addr:-:8080}" | sed -n 's/.*:\([0-9][0-9]*\)$/\1/p')
  [ -n "$port" ] || port=8080
  for local_url in "http://127.0.0.1:$port/admin" "http://[::1]:$port/admin"; do
    code=$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' --connect-timeout 2 --max-time 3 "$local_url" 2>/dev/null || true)
    case "$code" in [1-5][0-9][0-9]) running=1; break ;; esac
  done
fi
base_url=''
if [ -f "$root/shared/cms.conf" ]; then
  base_url=$(awk -F= '$1 == "BASE_URL" { sub(/^[^=]*=/, ""); gsub(/^[[:space:]]+|[[:space:]]+$/, ""); print; exit }' "$root/shared/cms.conf" 2>/dev/null || true)
fi
printf 'PILOT_GCMS_INSTALLED\t1\n'
printf 'PILOT_GCMS_PATH\t%s\n' "$root"
printf 'PILOT_GCMS_VERSION\t%s\n' "$version"
printf 'PILOT_GCMS_RUNNING\t%s\n' "$running"
printf 'PILOT_GCMS_BASE_URL\t%s\n' "$base_url"
"#;

/// 完整下载后执行，避免 `curl | sh` 在下载失败时被空 shell 误判为成功。
const GCMS_REMOTE_INSTALL_CMD: &str = r#"
set -eu
tmp=$(mktemp 2>/dev/null || mktemp -t gcms-install)
trap 'rm -f "$tmp"' EXIT HUP INT TERM
url='https://raw.githubusercontent.com/ccvar/gcms-releases/main/install.sh'
if command -v curl >/dev/null 2>&1; then
  curl -fsSL --retry 3 --connect-timeout 15 "$url" -o "$tmp"
elif command -v wget >/dev/null 2>&1; then
  wget -q -O "$tmp" "$url"
else
  printf '%s\n' '需要 curl 或 wget 才能下载安装脚本' >&2
  exit 127
fi
sh "$tmp"
"#;

/// 从服务器自身向公网探测出口 IP。IPv6 是可选项。
const GCMS_REMOTE_PUBLIC_IP_CMD: &str = r#"
ipv4=''
ipv6=''
if command -v curl >/dev/null 2>&1; then
  for url in https://api.ipify.org https://ipv4.icanhazip.com https://ifconfig.me/ip; do
    ipv4=$(curl -4fsS --connect-timeout 5 --max-time 10 "$url" 2>/dev/null | tr -d '\r\n ' || true)
    [ -n "$ipv4" ] && break
  done
  for url in https://api6.ipify.org https://ipv6.icanhazip.com; do
    ipv6=$(curl -6fsS --connect-timeout 5 --max-time 10 "$url" 2>/dev/null | tr -d '\r\n ' || true)
    [ -n "$ipv6" ] && break
  done
elif command -v wget >/dev/null 2>&1; then
  ipv4=$(wget -q -T 10 -O - https://api.ipify.org 2>/dev/null | tr -d '\r\n ' || true)
fi
printf 'PILOT_PUBLIC_IPV4\t%s\n' "$ipv4"
printf 'PILOT_PUBLIC_IPV6\t%s\n' "$ipv6"
"#;

/// Caddy 只读预检。容器、自定义启动、端口占用、同域名配置和非官方站点文件都会被标记。
const GCMS_CADDY_PREFLIGHT_CMD: &str = r#"
set +e
domain=${PILOT_DOMAIN:-}
uid=$(id -u 2>/dev/null || printf 'unknown')
os=$(uname -s 2>/dev/null | tr '[:upper:]' '[:lower:]')
privilege=none
if [ "$uid" = "0" ]; then
  privilege=root
elif command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
  privilege=sudo
fi

caddy_path=$(command -v caddy 2>/dev/null || true)
caddy_version=''
[ -n "$caddy_path" ] && caddy_version=$(caddy version 2>/dev/null | head -1 || true)
service_exists=0
service_running=0
config_path=''
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  if systemctl list-unit-files caddy.service --no-legend 2>/dev/null | grep -q '^caddy\.service'; then
    service_exists=1
    systemctl is-active --quiet caddy 2>/dev/null && service_running=1
    config_path=$(systemctl cat caddy 2>/dev/null | tr '\n' ' ' | sed -n 's/.*--config[= ][[:space:]]*\([^[:space:]]*\).*/\1/p' | tail -1 | tr -d '"' || true)
  fi
fi
[ -z "$config_path" ] && [ -f /etc/caddy/Caddyfile ] && config_path=/etc/caddy/Caddyfile

caddy_process=0
if command -v pgrep >/dev/null 2>&1 && pgrep -x caddy >/dev/null 2>&1; then caddy_process=1; fi
container_caddy=0
if command -v docker >/dev/null 2>&1; then
  docker ps --format '{{.Names}} {{.Image}}' 2>/dev/null | grep -qi caddy && container_caddy=1
fi

listen_dump=''
if command -v ss >/dev/null 2>&1; then
  if [ "$privilege" = sudo ]; then
    listen_dump=$(sudo -n ss -H -ltnp 2>/dev/null || ss -H -ltnp 2>/dev/null || true)
  else
    listen_dump=$(ss -H -ltnp 2>/dev/null || true)
  fi
elif command -v netstat >/dev/null 2>&1; then
  if [ "$privilege" = sudo ]; then
    listen_dump=$(sudo -n netstat -ltnp 2>/dev/null || netstat -ltnp 2>/dev/null || true)
  else
    listen_dump=$(netstat -ltnp 2>/dev/null || true)
  fi
fi
port_owner() {
  p=$1
  lines=$(printf '%s\n' "$listen_dump" | awk -v p="$p" '$4 ~ (":" p "$") { print }')
  if [ -z "$lines" ]; then printf 'free'; return; fi
  if printf '%s' "$lines" | grep -qi caddy; then printf 'caddy'; return; fi
  if printf '%s' "$lines" | grep -qi nginx; then printf 'nginx'; return; fi
  if printf '%s' "$lines" | grep -Eqi 'apache|httpd'; then printf 'apache'; return; fi
  if printf '%s' "$lines" | grep -qi traefik; then printf 'traefik'; return; fi
  printf 'occupied'
}
port80=$(port_owner 80)
port443=$(port_owner 443)

package_manager=''
for pm in apt-get dnf pacman; do
  if command -v "$pm" >/dev/null 2>&1; then package_manager=$pm; break; fi
done

site_file=/etc/caddy/conf.d/gcms.caddy
site_exists=0
site_managed=0
if [ -f "$site_file" ]; then
  site_exists=1
  grep -Fq '# Managed by GCMS setup-caddy.sh.' "$site_file" 2>/dev/null && site_managed=1
fi

printf 'PILOT_CADDY_OS\t%s\n' "$os"
printf 'PILOT_CADDY_PRIVILEGE\t%s\n' "$privilege"
printf 'PILOT_CADDY_PATH\t%s\n' "$caddy_path"
printf 'PILOT_CADDY_VERSION\t%s\n' "$caddy_version"
printf 'PILOT_CADDY_SERVICE_EXISTS\t%s\n' "$service_exists"
printf 'PILOT_CADDY_SERVICE_RUNNING\t%s\n' "$service_running"
printf 'PILOT_CADDY_PROCESS\t%s\n' "$caddy_process"
printf 'PILOT_CADDY_CONTAINER\t%s\n' "$container_caddy"
printf 'PILOT_CADDY_CONFIG\t%s\n' "$config_path"
printf 'PILOT_CADDY_PORT80\t%s\n' "$port80"
printf 'PILOT_CADDY_PORT443\t%s\n' "$port443"
printf 'PILOT_CADDY_PACKAGE_MANAGER\t%s\n' "$package_manager"
printf 'PILOT_CADDY_SITE_EXISTS\t%s\n' "$site_exists"
printf 'PILOT_CADDY_SITE_MANAGED\t%s\n' "$site_managed"
if [ -n "$domain" ] && [ -d /etc/caddy ]; then
  find /etc/caddy -type f ! -name '*.gcms-backup-*' ! -name '*.bak*' 2>/dev/null | while IFS= read -r file; do
    grep -Fq "$domain" "$file" 2>/dev/null && printf 'PILOT_CADDY_DOMAIN_FILE\t%s\n' "$file"
  done
fi
exit 0
"#;

/// 官方脚本外再做一次完整快照，覆盖下载、安装和重载阶段的失败路径。
const GCMS_CADDY_CONFIGURE_CMD: &str = r#"
set -eu
root=${PILOT_GCMS_HOME:?}
domain=${PILOT_DOMAIN:?}
conf="$root/shared/cms.conf"
caddyfile=/etc/caddy/Caddyfile
sitefile=/etc/caddy/conf.d/gcms.caddy
[ -x "$root/scripts/cms.sh" ] && [ -f "$conf" ] || { printf '%s\n' 'GCMS 标准目录不完整' >&2; exit 2; }

work=$(mktemp -d 2>/dev/null || mktemp -d -t pilot-gcms-caddy)
trap 'rm -rf "$work"' EXIT HUP INT TERM
cp "$conf" "$work/cms.conf"
had_caddyfile=0
had_sitefile=0
if [ -f "$caddyfile" ]; then cp "$caddyfile" "$work/Caddyfile"; had_caddyfile=1; fi
if [ -f "$sitefile" ]; then cp "$sitefile" "$work/gcms.caddy"; had_sitefile=1; fi

restore_before() {
  cp "$work/cms.conf" "$conf" 2>/dev/null || true
  if [ "$had_caddyfile" = 1 ]; then
    mkdir -p "$(dirname "$caddyfile")"; cp "$work/Caddyfile" "$caddyfile" 2>/dev/null || true
  else
    rm -f "$caddyfile" 2>/dev/null || true
  fi
  if [ "$had_sitefile" = 1 ]; then
    mkdir -p "$(dirname "$sitefile")"; cp "$work/gcms.caddy" "$sitefile" 2>/dev/null || true
  else
    rm -f "$sitefile" 2>/dev/null || true
  fi
  if command -v caddy >/dev/null 2>&1 && [ -f "$caddyfile" ]; then
    caddy validate --config "$caddyfile" --adapter caddyfile >/dev/null 2>&1 || return 0
    if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet caddy 2>/dev/null; then
      systemctl reload caddy >/dev/null 2>&1 || true
    else
      caddy reload --config "$caddyfile" --adapter caddyfile >/dev/null 2>&1 || true
    fi
  fi
}

setup="$work/setup-caddy.sh"
url='https://raw.githubusercontent.com/ccvar/gcms-releases/main/setup-caddy.sh'
if command -v curl >/dev/null 2>&1; then
  curl -fsSL --retry 3 --connect-timeout 15 "$url" -o "$setup"
elif command -v wget >/dev/null 2>&1; then
  wget -q -O "$setup" "$url"
else
  printf '%s\n' '需要 curl 或 wget 下载 Caddy 配置脚本' >&2
  exit 127
fi

set +e
env DOMAIN="$domain" WWW_REDIRECT=0 GCMS_HOME="$root" sh "$setup"
code=$?
set -e
if [ "$code" -ne 0 ]; then
  printf '%s\n' '配置失败，正在恢复修改前的 GCMS 与 Caddy 配置…' >&2
  restore_before
  exit "$code"
fi
"#;

#[derive(Clone, Serialize, Default, Debug, PartialEq)]
pub(super) struct GcmsRemoteStatus {
    installed: bool,
    version: String,
    path: String,
    running: bool,
    base_url: String,
}

#[derive(Clone, Serialize, Default, Debug, PartialEq)]
pub(super) struct GcmsCaddyPreflight {
    /// missing | standard | custom | conflict | unsupported
    mode: String,
    installed: bool,
    version: String,
    running: bool,
    can_auto_configure: bool,
    /// root | sudo | none
    privilege: String,
    config_path: String,
    port_80: String,
    port_443: String,
    domain_conflicts: Vec<String>,
    detail: String,
}

#[derive(Clone, Serialize, Default, Debug, PartialEq)]
pub(super) struct GcmsDnsHosting {
    /// cloudflare | other | unknown
    provider: String,
    zone: String,
    nameservers: Vec<String>,
    detection_error: String,
}

#[derive(Clone, Serialize, Default, Debug, PartialEq)]
pub(super) struct GcmsCloudflareRecord {
    record_type: String,
    name: String,
    content: String,
    proxied: bool,
}

#[derive(Clone, Serialize, Default, Debug, PartialEq)]
pub(super) struct GcmsCloudflareCheck {
    /// matched | connection_required | zone_not_found | permission_error | api_error |
    /// record_missing | unsupported_record | origin_mismatch | zone_inactive | ssl_unreadable |
    /// ssl_incompatible
    status: String,
    connected_accounts: usize,
    connection_id: String,
    connection_name: String,
    zone_name: String,
    zone_status: String,
    records: Vec<GcmsCloudflareRecord>,
    proxied: bool,
    origin_matched: bool,
    ssl_mode: String,
    ssl_error: String,
    detail: String,
}

#[derive(Clone, Serialize, Debug, PartialEq)]
pub(super) struct GcmsAccessCheck {
    domain: String,
    server_ipv4: Vec<String>,
    server_ipv6: Vec<String>,
    dns_ipv4: Vec<String>,
    dns_ipv6: Vec<String>,
    dns_error: String,
    hosting: GcmsDnsHosting,
    direct_dns_matched: bool,
    cloudflare: Option<GcmsCloudflareCheck>,
    matched: bool,
    caddy: Option<GcmsCaddyPreflight>,
}

#[derive(Clone, Serialize)]
pub(super) struct GcmsAccessApplyResult {
    status: GcmsRemoteStatus,
    url: String,
    https_ok: bool,
    http_status: Option<u16>,
    verification_error: String,
}

#[derive(Clone, Serialize)]
pub(super) struct GcmsCloudflareCreateResult {
    created: bool,
    check: GcmsAccessCheck,
}

#[derive(Clone, Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub(super) enum GcmsInstallEvent {
    Phase { message: String },
    Log { text: String },
}

#[derive(Default, Debug, PartialEq)]
struct RemoteCaddyProbe {
    os: String,
    privilege: String,
    path: String,
    version: String,
    service_exists: bool,
    service_running: bool,
    process_running: bool,
    container: bool,
    config_path: String,
    port_80: String,
    port_443: String,
    package_manager: String,
    site_exists: bool,
    site_managed: bool,
    domain_files: Vec<String>,
}

fn parse_gcms_remote_status(raw: &str) -> Result<GcmsRemoteStatus, String> {
    let mut out = GcmsRemoteStatus::default();
    let mut saw_installed = false;
    for line in raw.lines() {
        let Some((key, value)) = line.split_once('\t') else {
            continue;
        };
        match key.trim() {
            "PILOT_GCMS_INSTALLED" => {
                saw_installed = true;
                out.installed = value.trim() == "1";
            }
            "PILOT_GCMS_PATH" => out.path = value.trim().to_string(),
            "PILOT_GCMS_VERSION" => out.version = value.trim().to_string(),
            "PILOT_GCMS_RUNNING" => out.running = value.trim() == "1",
            "PILOT_GCMS_BASE_URL" => out.base_url = value.trim().to_string(),
            _ => {}
        }
    }
    if !saw_installed {
        return Err("无法识别服务器返回的 GCMS 检测结果".into());
    }
    if out.installed && out.path.is_empty() {
        return Err("检测到 GCMS，但服务器未返回安装目录".into());
    }
    Ok(out)
}

fn shell_quote(value: &str) -> String {
    format!("'{}'", value.replace('\'', "'\"'\"'"))
}

fn normalize_public_domain(raw: &str) -> Result<String, String> {
    let domain = raw.trim().trim_end_matches('.').to_ascii_lowercase();
    if domain.is_empty() {
        return Err("请输入访问域名".into());
    }
    if domain.len() > 253 {
        return Err("域名长度不能超过 253 个字符".into());
    }
    if domain.parse::<IpAddr>().is_ok() {
        return Err("这里需要填写域名，不能直接填写 IP 地址".into());
    }
    if !domain.contains('.') {
        return Err("请填写完整域名，例如 cms.example.com".into());
    }
    for label in domain.split('.') {
        if label.is_empty() || label.len() > 63 {
            return Err("域名格式不正确：每一段必须为 1–63 个字符".into());
        }
        if label.starts_with('-') || label.ends_with('-') {
            return Err("域名格式不正确：每一段不能以连字符开头或结尾".into());
        }
        if !label
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-')
        {
            return Err("域名只支持字母、数字、点和连字符；中文域名请填写 Punycode".into());
        }
    }
    Ok(domain)
}

fn usable_public_ip(ip: IpAddr) -> bool {
    match ip {
        IpAddr::V4(v) => {
            let o = v.octets();
            !v.is_unspecified()
                && !v.is_loopback()
                && !v.is_private()
                && !v.is_link_local()
                && !v.is_multicast()
                && o != [255, 255, 255, 255]
                && !(o[0] == 100 && (64..=127).contains(&o[1]))
        }
        IpAddr::V6(v) => {
            !v.is_unspecified()
                && !v.is_loopback()
                && !v.is_unique_local()
                && !v.is_unicast_link_local()
                && !v.is_multicast()
        }
    }
}

fn parse_remote_public_ips(raw: &str) -> (Vec<IpAddr>, Vec<IpAddr>) {
    let mut v4 = Vec::new();
    let mut v6 = Vec::new();
    for line in raw.lines() {
        let Some((key, value)) = line.split_once('\t') else {
            continue;
        };
        let Ok(ip) = value.trim().parse::<IpAddr>() else {
            continue;
        };
        if !usable_public_ip(ip) {
            continue;
        }
        match (key.trim(), ip) {
            ("PILOT_PUBLIC_IPV4", IpAddr::V4(_)) if !v4.contains(&ip) => v4.push(ip),
            ("PILOT_PUBLIC_IPV6", IpAddr::V6(_)) if !v6.contains(&ip) => v6.push(ip),
            _ => {}
        }
    }
    (v4, v6)
}

fn parse_remote_caddy_probe(raw: &str) -> RemoteCaddyProbe {
    let mut out = RemoteCaddyProbe::default();
    for line in raw.lines() {
        let Some((key, value)) = line.split_once('\t') else {
            continue;
        };
        let value = value.trim();
        match key.trim() {
            "PILOT_CADDY_OS" => out.os = value.to_string(),
            "PILOT_CADDY_PRIVILEGE" => out.privilege = value.to_string(),
            "PILOT_CADDY_PATH" => out.path = value.to_string(),
            "PILOT_CADDY_VERSION" => out.version = value.to_string(),
            "PILOT_CADDY_SERVICE_EXISTS" => out.service_exists = value == "1",
            "PILOT_CADDY_SERVICE_RUNNING" => out.service_running = value == "1",
            "PILOT_CADDY_PROCESS" => out.process_running = value == "1",
            "PILOT_CADDY_CONTAINER" => out.container = value == "1",
            "PILOT_CADDY_CONFIG" => out.config_path = value.to_string(),
            "PILOT_CADDY_PORT80" => out.port_80 = value.to_string(),
            "PILOT_CADDY_PORT443" => out.port_443 = value.to_string(),
            "PILOT_CADDY_PACKAGE_MANAGER" => out.package_manager = value.to_string(),
            "PILOT_CADDY_SITE_EXISTS" => out.site_exists = value == "1",
            "PILOT_CADDY_SITE_MANAGED" => out.site_managed = value == "1",
            "PILOT_CADDY_DOMAIN_FILE" if !value.is_empty() => {
                out.domain_files.push(value.to_string())
            }
            _ => {}
        }
    }
    out.domain_files.sort();
    out.domain_files.dedup();
    out
}

fn classify_caddy_probe(probe: RemoteCaddyProbe) -> GcmsCaddyPreflight {
    let managed_site = "/etc/caddy/conf.d/gcms.caddy";
    let domain_conflicts: Vec<String> = probe
        .domain_files
        .iter()
        .filter(|path| path.as_str() != managed_site || !probe.site_managed)
        .cloned()
        .collect();
    let port_conflict = |owner: &str| !owner.is_empty() && owner != "free" && owner != "caddy";
    let installed = !probe.path.is_empty();
    let mut out = GcmsCaddyPreflight {
        installed,
        version: probe.version.clone(),
        running: probe.service_running || probe.process_running || probe.container,
        privilege: probe.privilege.clone(),
        config_path: probe.config_path.clone(),
        port_80: probe.port_80.clone(),
        port_443: probe.port_443.clone(),
        domain_conflicts: domain_conflicts.clone(),
        ..Default::default()
    };
    let blocked = if probe.os != "linux" {
        Some(("unsupported", "自动配置目前只支持 Linux 服务器".to_string()))
    } else if probe.privilege == "none" {
        Some((
            "unsupported",
            "当前 SSH 用户既不是 root，也没有免密 sudo；无法安全修改 Caddy".to_string(),
        ))
    } else if probe.container {
        Some((
            "custom",
            "检测到容器中的 Caddy；其挂载目录和启动参数未知，请在容器配置中手动接入 GCMS"
                .to_string(),
        ))
    } else if port_conflict(&probe.port_80) || port_conflict(&probe.port_443) {
        Some((
            "conflict",
            format!(
                "80/443 端口已由其他服务占用（80：{}，443：{}），不会自动抢占",
                probe.port_80, probe.port_443
            ),
        ))
    } else if probe.site_exists && !probe.site_managed {
        Some((
            "conflict",
            format!("{managed_site} 已存在但不是 GCMS 官方托管文件，不会覆盖"),
        ))
    } else if !domain_conflicts.is_empty() {
        Some((
            "conflict",
            format!(
                "该域名已出现在其他 Caddy 配置中：{}",
                domain_conflicts.join("、")
            ),
        ))
    } else if !probe.config_path.is_empty() && probe.config_path != "/etc/caddy/Caddyfile" {
        Some((
            "custom",
            format!("Caddy 使用自定义主配置 {}，不会自动修改", probe.config_path),
        ))
    } else if !installed && (probe.service_exists || probe.process_running) {
        Some((
            "custom",
            "检测到已有 Caddy 服务或进程，但其命令不在标准 PATH；不会再安装第二套 Caddy"
                .to_string(),
        ))
    } else if installed && probe.process_running && !probe.service_exists {
        Some((
            "custom",
            "检测到手动启动的 Caddy 进程，但没有标准 caddy.service；不会改变其启动方式".to_string(),
        ))
    } else if installed && !probe.service_exists {
        Some((
            "custom",
            "检测到 Caddy 命令，但不是标准 systemd 安装；请确认启动方式后手动配置".to_string(),
        ))
    } else {
        None
    };
    if let Some((mode, detail)) = blocked {
        out.mode = mode.into();
        out.detail = detail;
        return out;
    }
    if !installed {
        if probe.package_manager.is_empty() {
            out.mode = "unsupported".into();
            out.detail = "未安装 Caddy，且未检测到 apt、dnf 或 pacman，无法自动安装".into();
        } else {
            out.mode = "missing".into();
            out.can_auto_configure = true;
            out.detail = format!(
                "未安装 Caddy，将通过 {} 安装官方软件包并创建独立 GCMS 配置",
                probe.package_manager
            );
        }
        return out;
    }
    out.mode = "standard".into();
    out.can_auto_configure = true;
    out.detail = if probe.site_managed {
        "检测到标准 Caddy 与 GCMS 托管配置；将先备份，再安全更新并校验重载".into()
    } else {
        "检测到标准 Caddy；将保留主配置，只新增独立 GCMS 配置并在校验通过后重载".into()
    };
    out
}

async fn gcms_remote_status_inner(
    state: &AppState,
    conn_id: &str,
) -> Result<GcmsRemoteStatus, String> {
    ensure_ssh(state, conn_id).await?;
    let out = state
        .ssh
        .exec(conn_id, GCMS_REMOTE_PROBE_CMD, 25, false)
        .await?;
    if out.code != 0 {
        let detail = out.stderr.trim();
        return Err(if detail.is_empty() {
            format!("检测 GCMS 失败（退出码 {}）", out.code)
        } else {
            format!(
                "检测 GCMS 失败：{}",
                detail.chars().take(300).collect::<String>()
            )
        });
    }
    parse_gcms_remote_status(&out.stdout)
}

#[tauri::command]
pub(super) async fn gcms_remote_status(
    state: tauri::State<'_, AppState>,
    conn_id: String,
) -> Result<GcmsRemoteStatus, String> {
    gcms_remote_status_inner(&state, &conn_id).await
}

async fn gcms_caddy_preflight_inner(
    state: &AppState,
    conn_id: &str,
    domain: &str,
) -> Result<GcmsCaddyPreflight, String> {
    let env = format!("PILOT_DOMAIN={}", shell_quote(domain));
    let body = shell_quote(GCMS_CADDY_PREFLIGHT_CMD);
    let command = format!("env {env} sh -c {body}");
    let out = state.ssh.exec(conn_id, &command, 35, false).await?;
    if out.code != 0 {
        let detail = out.stderr.trim();
        return Err(if detail.is_empty() {
            format!("Caddy 预检失败（退出码 {}）", out.code)
        } else {
            format!(
                "Caddy 预检失败：{}",
                detail.chars().take(300).collect::<String>()
            )
        });
    }
    let initial = parse_remote_caddy_probe(&out.stdout);
    // 有免密 sudo 时必须以 root 重新做一遍只读预检。否则普通用户可能读不到
    // /etc/caddy 下的自定义配置，进而把“已有同域名站点”误判为可自动修改。
    let probe = if initial.privilege == "sudo" {
        let elevated = format!("sudo -n env {env} sh -c {body}");
        let out = state.ssh.exec(conn_id, &elevated, 35, false).await?;
        if out.code != 0 {
            let detail = out.stderr.trim();
            return Err(if detail.is_empty() {
                format!("Caddy 提权预检失败（退出码 {}）", out.code)
            } else {
                format!(
                    "Caddy 提权预检失败：{}",
                    detail.chars().take(300).collect::<String>()
                )
            });
        }
        let mut probe = parse_remote_caddy_probe(&out.stdout);
        // 对 UI 保留真实的授权方式，而不是提升后脚本看到的 root。
        probe.privilege = "sudo".into();
        probe
    } else {
        initial
    };
    Ok(classify_caddy_probe(probe))
}

async fn resolve_domain_ips(domain: &str) -> (Vec<IpAddr>, Vec<IpAddr>, String) {
    let resolved = tokio::time::timeout(
        Duration::from_secs(12),
        tokio::net::lookup_host((domain, 0)),
    )
    .await;
    let addrs = match resolved {
        Ok(Ok(addrs)) => addrs,
        Ok(Err(e)) => return (Vec::new(), Vec::new(), format!("域名暂未解析：{e}")),
        Err(_) => return (Vec::new(), Vec::new(), "域名解析超时，请稍后重试".into()),
    };
    let mut v4 = Vec::new();
    let mut v6 = Vec::new();
    for addr in addrs {
        let ip = addr.ip();
        match ip {
            IpAddr::V4(_) if !v4.contains(&ip) => v4.push(ip),
            IpAddr::V6(_) if !v6.contains(&ip) => v6.push(ip),
            _ => {}
        }
    }
    v4.sort();
    v6.sort();
    (v4, v6, String::new())
}

fn dns_addresses_match_server(
    dns_v4: &[IpAddr],
    dns_v6: &[IpAddr],
    server_v4: &[IpAddr],
    server_v6: &[IpAddr],
) -> bool {
    (!dns_v4.is_empty() || !dns_v6.is_empty())
        && dns_v4.iter().all(|ip| server_v4.contains(ip))
        && dns_v6.iter().all(|ip| server_v6.contains(ip))
}

#[derive(Deserialize, Default)]
struct DohAnswer {
    #[serde(default)]
    name: String,
    #[serde(rename = "type", default)]
    record_type: u16,
    #[serde(default)]
    data: String,
}

#[derive(Deserialize, Default)]
struct DohResponse {
    #[serde(rename = "Status", default)]
    status: u32,
    #[serde(rename = "Answer", default)]
    answer: Vec<DohAnswer>,
}

async fn doh_nameservers(client: &reqwest::Client, domain: &str) -> Result<Vec<String>, String> {
    let mut last_error = String::new();
    for endpoint in [
        "https://cloudflare-dns.com/dns-query",
        "https://dns.google/resolve",
    ] {
        let mut url = match reqwest::Url::parse(endpoint) {
            Ok(url) => url,
            Err(error) => {
                last_error = error.to_string();
                continue;
            }
        };
        url.query_pairs_mut()
            .append_pair("name", domain)
            .append_pair("type", "NS");
        let response = client
            .get(url)
            .header("Accept", "application/dns-json")
            .send()
            .await;
        let response = match response {
            Ok(response) => response,
            Err(error) => {
                last_error = error.to_string();
                continue;
            }
        };
        let status = response.status();
        if !status.is_success() {
            last_error = format!("DNS 查询返回 HTTP {status}");
            continue;
        }
        let body = match response.json::<DohResponse>().await {
            Ok(body) => body,
            Err(error) => {
                last_error = format!("DNS 查询响应无法解析：{error}");
                continue;
            }
        };
        if body.status != 0 && body.status != 3 {
            last_error = format!("DNS 查询返回状态 {}", body.status);
            continue;
        }
        let mut nameservers = body
            .answer
            .into_iter()
            .filter(|answer| {
                answer.record_type == 2
                    && answer
                        .name
                        .trim_end_matches('.')
                        .eq_ignore_ascii_case(domain)
            })
            .map(|answer| {
                answer
                    .data
                    .trim()
                    .trim_end_matches('.')
                    .to_ascii_lowercase()
            })
            .filter(|name| !name.is_empty())
            .collect::<Vec<_>>();
        nameservers.sort();
        nameservers.dedup();
        return Ok(nameservers);
    }
    Err(if last_error.is_empty() {
        "权威 DNS 查询失败".into()
    } else {
        last_error
    })
}

async fn detect_dns_hosting(domain: &str) -> GcmsDnsHosting {
    let client = match reqwest::Client::builder()
        .timeout(Duration::from_secs(7))
        .build()
    {
        Ok(client) => client,
        Err(error) => {
            return GcmsDnsHosting {
                provider: "unknown".into(),
                detection_error: format!("无法初始化 DNS 检测：{error}"),
                ..Default::default()
            };
        }
    };
    let labels = domain.split('.').collect::<Vec<_>>();
    let mut last_error = String::new();
    // 逐级向父域查 NS，可同时覆盖普通 Zone 和被单独委派的子域。极端深层域名只查
    // 完整主机名与最靠近注册域的 7 级，避免恶意输入制造上百次外部请求。
    let mut starts = vec![0];
    let parent_end = labels.len().saturating_sub(1);
    let parent_start = parent_end.saturating_sub(7);
    starts.extend(parent_start..parent_end);
    starts.sort_unstable();
    starts.dedup();
    for start in starts {
        let candidate = labels[start..].join(".");
        match doh_nameservers(&client, &candidate).await {
            Ok(nameservers) if !nameservers.is_empty() => {
                let cloudflare = nameservers
                    .iter()
                    .all(|name| name.ends_with(".ns.cloudflare.com"));
                return GcmsDnsHosting {
                    provider: if cloudflare { "cloudflare" } else { "other" }.into(),
                    zone: candidate,
                    nameservers,
                    detection_error: String::new(),
                };
            }
            Ok(_) => {}
            Err(error) => last_error = error,
        }
    }
    GcmsDnsHosting {
        provider: "unknown".into(),
        detection_error: if last_error.is_empty() {
            "未找到该域名的权威 NS 记录".into()
        } else {
            format!("暂时无法识别 DNS 托管商：{last_error}")
        },
        ..Default::default()
    }
}

fn classify_cloudflare_inspection(
    connection_id: &str,
    connection_name: &str,
    connected_accounts: usize,
    domain: &str,
    inspect: cf::CfHostnameInspect,
    server_v4: &[IpAddr],
    server_v6: &[IpAddr],
) -> GcmsCloudflareCheck {
    let relevant = inspect
        .records
        .iter()
        .filter(|record| {
            record
                .name
                .trim_end_matches('.')
                .eq_ignore_ascii_case(domain)
                && matches!(record.record_type.as_str(), "A" | "AAAA" | "CNAME")
        })
        .collect::<Vec<_>>();
    let address_records = relevant
        .iter()
        .copied()
        .filter(|record| matches!(record.record_type.as_str(), "A" | "AAAA"))
        .collect::<Vec<_>>();
    let has_cname = relevant.iter().any(|record| record.record_type == "CNAME");
    let origin_matched = !address_records.is_empty()
        && address_records.iter().all(|record| {
            record.content.parse::<IpAddr>().is_ok_and(|ip| match ip {
                IpAddr::V4(_) => server_v4.contains(&ip),
                IpAddr::V6(_) => server_v6.contains(&ip),
            })
        });
    let proxied = relevant.iter().any(|record| record.proxied);
    let records = relevant
        .into_iter()
        .map(|record| GcmsCloudflareRecord {
            record_type: record.record_type.clone(),
            name: record.name.clone(),
            content: record.content.clone(),
            proxied: record.proxied,
        })
        .collect::<Vec<_>>();

    let (status, detail) = if records.is_empty() {
        (
            "record_missing",
            format!("Cloudflare Zone 中没有 {domain} 的 A、AAAA 或 CNAME 记录。"),
        )
    } else if address_records.is_empty() && has_cname {
        (
            "unsupported_record",
            "检测到 CNAME。为避免把代理链误判成源站，当前自动配置只核验直接 A / AAAA 记录。".into(),
        )
    } else if !origin_matched {
        (
            "origin_mismatch",
            "Cloudflare 中的真实源站记录与这台 SSH 服务器公网 IP 不一致。".into(),
        )
    } else if inspect.zone_status != "active" {
        (
            "zone_inactive",
            format!(
                "Cloudflare Zone 当前状态为 {}，需变为 active 后才能继续。",
                if inspect.zone_status.is_empty() {
                    "未知"
                } else {
                    &inspect.zone_status
                }
            ),
        )
    } else if proxied && !inspect.ssl_error.is_empty() {
        (
            "ssl_unreadable",
            "橙云已开启，但无法读取 SSL/TLS 模式。请给 Token 增加 Zone Settings: Read 权限后重新检测。".into(),
        )
    } else if proxied && !matches!(inspect.ssl_mode.as_str(), "full" | "strict") {
        (
            "ssl_incompatible",
            format!(
                "橙云已开启，但 SSL/TLS 模式为 {}。请先在 Cloudflare 改为 Full 或 Full (strict)，避免重定向循环。",
                if inspect.ssl_mode.is_empty() {
                    "未知"
                } else {
                    &inspect.ssl_mode
                }
            ),
        )
    } else {
        (
            "matched",
            if proxied {
                "橙云已开启；真实源站记录与服务器一致，SSL/TLS 模式可安全继续。".into()
            } else {
                "Cloudflare DNS 记录与服务器一致；当前为仅 DNS。".into()
            },
        )
    };

    GcmsCloudflareCheck {
        status: status.into(),
        connected_accounts,
        connection_id: connection_id.into(),
        connection_name: connection_name.into(),
        zone_name: inspect.zone_name,
        zone_status: inspect.zone_status,
        records,
        proxied,
        origin_matched,
        ssl_mode: inspect.ssl_mode,
        ssl_error: inspect.ssl_error,
        detail,
    }
}

async fn inspect_cloudflare_hosting(
    state: &AppState,
    domain: &str,
    zone: &str,
    server_v4: &[IpAddr],
    server_v6: &[IpAddr],
) -> GcmsCloudflareCheck {
    let connections = state
        .conns
        .list()
        .into_iter()
        .filter(|connection| connection.kind == "cloudflare")
        .collect::<Vec<_>>();
    let connected_accounts = connections
        .iter()
        .map(|connection| {
            if connection.account_id.is_empty() {
                connection.id.as_str()
            } else {
                connection.account_id.as_str()
            }
        })
        .collect::<HashSet<_>>()
        .len();
    if connections.is_empty() {
        return GcmsCloudflareCheck {
            status: "connection_required".into(),
            detail: "已识别为 Cloudflare 托管。请先连接持有该域名的 Cloudflare 账号，Pilot 才能在橙云下只读核验真实源站。".into(),
            ..Default::default()
        };
    }

    let mut permission_errors = Vec::new();
    let mut api_errors = Vec::new();
    let mut readable_fallback = None;
    for connection in connections {
        let connection_id = connection.id.clone();
        let key_id = connection_id.clone();
        let token =
            match tauri::async_runtime::spawn_blocking(move || keychain::get_key(&key_id)).await {
                Ok(Ok(token)) => token,
                Ok(Err(error)) => {
                    api_errors.push(format!("{}：{error}", connection.name));
                    continue;
                }
                Err(error) => {
                    api_errors.push(format!("{}：读取凭据失败（{error}）", connection.name));
                    continue;
                }
            };
        match cf::inspect_hostname(&token, &connection.account_id, zone, domain).await {
            Ok(Some(inspect)) => {
                let classified = classify_cloudflare_inspection(
                    &connection_id,
                    &connection.name,
                    connected_accounts,
                    domain,
                    inspect,
                    server_v4,
                    server_v6,
                );
                // 同一账号可能被重新连接过：旧 Token 读不到 SSL 设置时，继续寻找权限更完整的连接。
                if classified.status == "ssl_unreadable" {
                    readable_fallback.get_or_insert(classified);
                    continue;
                }
                return classified;
            }
            Ok(None) => {}
            Err(error) => {
                let error = format!("{}：{error}", connection.name);
                if error.contains("Cloudflare 401")
                    || error.contains("Cloudflare 403")
                    || error.contains("权限")
                {
                    permission_errors.push(error);
                } else {
                    api_errors.push(error);
                }
            }
        }
    }

    if let Some(classified) = readable_fallback {
        classified
    } else if !permission_errors.is_empty() {
        GcmsCloudflareCheck {
            status: "permission_error".into(),
            connected_accounts,
            zone_name: zone.into(),
            detail: format!(
                "无法完整读取 Cloudflare Zone/DNS。请确认 Token 具有 Zone: Read、DNS: Read（或 Edit）；橙云还需 Zone Settings: Read。 {}",
                permission_errors[0]
            ),
            ..Default::default()
        }
    } else if !api_errors.is_empty() {
        GcmsCloudflareCheck {
            status: "api_error".into(),
            connected_accounts,
            zone_name: zone.into(),
            detail: format!(
                "Cloudflare 只读检测暂时失败，请检查网络或系统钥匙串后重试。 {}",
                api_errors[0]
            ),
            ..Default::default()
        }
    } else {
        GcmsCloudflareCheck {
            status: "zone_not_found".into(),
            connected_accounts,
            zone_name: zone.into(),
            detail: format!(
                "已连接的 {connected_accounts} 个 Cloudflare 账号中没有找到 {zone}，请连接持有该域名的账号。"
            ),
            ..Default::default()
        }
    }
}

async fn gcms_remote_access_check_inner(
    state: &AppState,
    conn_id: &str,
    raw_domain: &str,
) -> Result<GcmsAccessCheck, String> {
    let domain = normalize_public_domain(raw_domain)?;
    let conn = state.conns.get(conn_id)?;
    if conn.kind != "ssh" {
        return Err("这不是远程服务器连接".into());
    }
    let status = gcms_remote_status_inner(state, conn_id).await?;
    if !status.installed {
        return Err("请先在这台服务器安装 GCMS，再配置公网访问".into());
    }

    let public_out = state
        .ssh
        .exec(conn_id, GCMS_REMOTE_PUBLIC_IP_CMD, 45, false)
        .await?;
    let (mut server_v4, mut server_v6) = parse_remote_public_ips(&public_out.stdout);
    // 出站探测偶尔会被防火墙拦截；SSH 目标本身就是公网 IP 时可作为可信降级。
    if server_v4.is_empty() && server_v6.is_empty() {
        if let Ok(ip) = conn.ssh_host.parse::<IpAddr>() {
            if usable_public_ip(ip) {
                match ip {
                    IpAddr::V4(_) => server_v4.push(ip),
                    IpAddr::V6(_) => server_v6.push(ip),
                }
            }
        }
    }
    if server_v4.is_empty() && server_v6.is_empty() {
        return Err("无法从服务器探测公网 IP。请确认服务器能访问 api.ipify.org，或使用公网 IP 建立 SSH 连接".into());
    }

    let ((dns_v4, dns_v6, dns_error), hosting) =
        tokio::join!(resolve_domain_ips(&domain), detect_dns_hosting(&domain));
    // 不能只凭“其中一个地址命中”放行：遗留的 A/AAAA 会把一部分访客带到错误源站。
    let direct_dns_matched = dns_addresses_match_server(&dns_v4, &dns_v6, &server_v4, &server_v6);
    let cloudflare = if hosting.provider == "cloudflare" {
        Some(
            inspect_cloudflare_hosting(state, &domain, &hosting.zone, &server_v4, &server_v6).await,
        )
    } else {
        None
    };
    // Cloudflare（尤其橙云）必须以 API 中的真实源站记录为准；其他托管商保持公网 DNS 对照。
    let matched = cloudflare
        .as_ref()
        .map(|check| check.status == "matched")
        .unwrap_or(direct_dns_matched);
    let caddy = if matched {
        Some(gcms_caddy_preflight_inner(state, conn_id, &domain).await?)
    } else {
        None
    };
    Ok(GcmsAccessCheck {
        domain,
        server_ipv4: server_v4.into_iter().map(|ip| ip.to_string()).collect(),
        server_ipv6: server_v6.into_iter().map(|ip| ip.to_string()).collect(),
        dns_ipv4: dns_v4.into_iter().map(|ip| ip.to_string()).collect(),
        dns_ipv6: dns_v6.into_iter().map(|ip| ip.to_string()).collect(),
        dns_error,
        hosting,
        direct_dns_matched,
        cloudflare,
        matched,
        caddy,
    })
}

#[tauri::command]
pub(super) async fn gcms_remote_access_check(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    domain: String,
) -> Result<GcmsAccessCheck, String> {
    gcms_remote_access_check_inner(&state, &conn_id, &domain).await
}

#[tauri::command]
pub(super) async fn gcms_cloudflare_create_a_record(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    domain: String,
) -> Result<GcmsCloudflareCreateResult, String> {
    let conn = state.conns.get(&conn_id)?;
    if conn.kind != "ssh" {
        return Err("这不是远程服务器连接".into());
    }
    let _guard = begin_gcms_operation(&state, &conn_id)?;
    let check = gcms_remote_access_check_inner(&state, &conn_id, &domain).await?;
    let cloudflare = check
        .cloudflare
        .as_ref()
        .ok_or("该域名当前未识别为 Cloudflare 托管")?;
    if cloudflare.status == "matched" {
        return Ok(GcmsCloudflareCreateResult {
            created: false,
            check,
        });
    }
    if cloudflare.status != "record_missing" {
        return Err(format!("当前状态不允许自动创建记录：{}", cloudflare.detail));
    }
    if cloudflare.zone_status != "active" {
        return Err(format!(
            "Cloudflare Zone 当前状态为 {}，请先等待 Zone 激活",
            if cloudflare.zone_status.is_empty() {
                "未知"
            } else {
                &cloudflare.zone_status
            }
        ));
    }
    let address = match check.server_ipv4.as_slice() {
        [address] => address
            .parse::<Ipv4Addr>()
            .map_err(|_| "检测到的服务器 IPv4 地址无效")?,
        [] => return Err(
            "未检测到服务器公网 IPv4。Pilot 不会仅凭 IPv6 自动创建 AAAA，请先确认服务器 IPv4 网络"
                .into(),
        ),
        _ => return Err("检测到多个服务器公网 IPv4，为避免选错源站，请手动配置 A 记录".into()),
    };
    let cf_connection_id = cloudflare.connection_id.clone();
    let zone_name = cloudflare.zone_name.clone();
    if cf_connection_id.is_empty() || zone_name.is_empty() {
        return Err("没有可用于创建记录的 Cloudflare 连接或 Zone".into());
    }
    let cf_connection = state.conns.get(&cf_connection_id)?;
    if cf_connection.kind != "cloudflare" {
        return Err("核验连接不是 Cloudflare 连接".into());
    }
    let key_id = cf_connection_id.clone();
    let token = tauri::async_runtime::spawn_blocking(move || keychain::get_key(&key_id))
        .await
        .map_err(|error| format!("读取 Cloudflare 凭据失败：{error}"))??;
    let created = cf::create_dns_only_a_record(
        &token,
        &cf_connection.account_id,
        &zone_name,
        &check.domain,
        address,
    )
    .await
    .map_err(|error| format!("创建 Cloudflare A 记录失败：{error}"))?;

    let refreshed = gcms_remote_access_check_inner(&state, &conn_id, &check.domain)
        .await
        .map_err(|error| {
            if created {
                format!("A 记录已创建，但重新核验失败：{error}。可直接点击重新检测")
            } else {
                format!("A 记录已存在，但重新核验失败：{error}。可直接点击重新检测")
            }
        })?;
    Ok(GcmsCloudflareCreateResult {
        created,
        check: refreshed,
    })
}

struct GcmsOperationGuard {
    active: Arc<Mutex<HashSet<String>>>,
    conn_id: String,
}

impl Drop for GcmsOperationGuard {
    fn drop(&mut self) {
        if let Ok(mut active) = self.active.lock() {
            active.remove(&self.conn_id);
        }
    }
}

fn begin_gcms_operation(state: &AppState, conn_id: &str) -> Result<GcmsOperationGuard, String> {
    let mut active = state
        .gcms_installing
        .lock()
        .map_err(|_| "GCMS 操作状态锁异常")?;
    if !active.insert(conn_id.to_string()) {
        return Err("这台服务器正在执行 GCMS 安装、DNS 或公网配置，请等待当前操作完成".into());
    }
    drop(active);
    Ok(GcmsOperationGuard {
        active: state.gcms_installing.clone(),
        conn_id: conn_id.to_string(),
    })
}

fn gcms_install_log(stdout: &str, stderr: &str) -> String {
    let combined = match (stdout.trim(), stderr.trim()) {
        ("", "") => String::new(),
        (out, "") => out.to_string(),
        ("", err) => err.to_string(),
        (out, err) => format!("{out}\n{err}"),
    };
    let chars: Vec<char> = combined.chars().collect();
    if chars.len() <= 12_000 {
        combined
    } else {
        format!(
            "…（前段输出已省略）\n{}",
            chars[chars.len() - 12_000..].iter().collect::<String>()
        )
    }
}

#[tauri::command]
pub(super) async fn gcms_remote_install(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    on_event: Channel<GcmsInstallEvent>,
) -> Result<GcmsRemoteStatus, String> {
    let conn = state.conns.get(&conn_id)?;
    if conn.kind != "ssh" {
        return Err("这不是远程服务器连接".into());
    }
    let _guard = begin_gcms_operation(&state, &conn_id)?;
    let phase = |message: &str| {
        let _ = on_event.send(GcmsInstallEvent::Phase {
            message: message.to_string(),
        });
    };

    phase("正在连接服务器…");
    let before = gcms_remote_status_inner(&state, &conn_id).await?;
    if before.installed {
        phase("服务器已安装 GCMS");
        return Ok(before);
    }

    phase("正在下载安装并启动 GCMS…");
    let result = state
        .ssh
        .exec(&conn_id, GCMS_REMOTE_INSTALL_CMD, 900, false)
        .await?;
    let log = gcms_install_log(&result.stdout, &result.stderr);
    if !log.is_empty() {
        let _ = on_event.send(GcmsInstallEvent::Log { text: log.clone() });
    }
    if result.code != 0 {
        let brief = log
            .lines()
            .rev()
            .map(str::trim)
            .find(|line| !line.is_empty())
            .unwrap_or("未知错误");
        return Err(format!(
            "安装失败（退出码 {}）：{}",
            result.code,
            brief.chars().take(300).collect::<String>()
        ));
    }

    phase("安装完成，正在验证服务…");
    let after = gcms_remote_status_inner(&state, &conn_id).await?;
    if !after.installed {
        return Err("安装命令已结束，但未检测到标准 GCMS 安装目录".into());
    }
    phase("GCMS 已安装");
    Ok(after)
}

async fn verify_gcms_https(domain: &str) -> (bool, Option<u16>, String) {
    let client = match reqwest::Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(Duration::from_secs(18))
        .build()
    {
        Ok(client) => client,
        Err(e) => return (false, None, format!("创建 HTTPS 检测请求失败：{e}")),
    };
    let url = format!("https://{domain}/admin");
    let mut last_error = String::new();
    for attempt in 0..3 {
        match client.get(&url).send().await {
            Ok(response) => {
                let status = response.status();
                let ok = status.is_success() || status.is_redirection();
                return (
                    ok,
                    Some(status.as_u16()),
                    if ok {
                        String::new()
                    } else {
                        format!("HTTPS 已连通，但 /admin 返回 HTTP {}", status.as_u16())
                    },
                );
            }
            Err(e) => last_error = e.to_string(),
        }
        if attempt < 2 {
            tokio::time::sleep(Duration::from_secs(2)).await;
        }
    }
    (false, None, format!("HTTPS 暂未连通：{last_error}"))
}

#[tauri::command]
pub(super) async fn gcms_remote_access_configure(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    domain: String,
    on_event: Channel<GcmsInstallEvent>,
) -> Result<GcmsAccessApplyResult, String> {
    let conn = state.conns.get(&conn_id)?;
    if conn.kind != "ssh" {
        return Err("这不是远程服务器连接".into());
    }
    let _guard = begin_gcms_operation(&state, &conn_id)?;
    let phase = |message: &str| {
        let _ = on_event.send(GcmsInstallEvent::Phase {
            message: message.to_string(),
        });
    };

    phase("正在复核域名解析与服务器环境…");
    let check = gcms_remote_access_check_inner(&state, &conn_id, &domain).await?;
    if !check.matched {
        let reason = check
            .cloudflare
            .as_ref()
            .map(|cloudflare| cloudflare.detail.as_str())
            .filter(|detail| !detail.is_empty())
            .unwrap_or("域名解析尚未指向这台服务器");
        return Err(format!("域名尚未通过安全校验：{reason}（未修改 Caddy）"));
    }
    let preflight = check.caddy.ok_or("未完成 Caddy 预检")?;
    if !preflight.can_auto_configure {
        return Err(format!("当前环境不允许自动配置：{}", preflight.detail));
    }
    let before = gcms_remote_status_inner(&state, &conn_id).await?;
    if !before.installed || before.path.is_empty() {
        return Err("未检测到标准 GCMS 安装目录".into());
    }

    phase(if preflight.mode == "missing" {
        "正在安装并配置 Caddy…"
    } else {
        "正在备份并配置 Caddy…"
    });
    let env = format!(
        "PILOT_DOMAIN={} PILOT_GCMS_HOME={}",
        shell_quote(&check.domain),
        shell_quote(&before.path)
    );
    let body = shell_quote(GCMS_CADDY_CONFIGURE_CMD);
    let command = if preflight.privilege == "root" {
        format!("env {env} sh -c {body}")
    } else if preflight.privilege == "sudo" {
        format!("sudo -n env {env} sh -c {body}")
    } else {
        return Err("配置 Caddy 需要 root 或免密 sudo 权限".into());
    };
    let result = state.ssh.exec(&conn_id, &command, 900, false).await?;
    let log = gcms_install_log(&result.stdout, &result.stderr);
    if !log.is_empty() {
        let _ = on_event.send(GcmsInstallEvent::Log { text: log.clone() });
    }
    if result.code != 0 {
        let brief = log
            .lines()
            .rev()
            .map(str::trim)
            .find(|line| !line.is_empty())
            .unwrap_or("未知错误");
        return Err(format!(
            "公网访问配置失败（退出码 {}）：{}",
            result.code,
            brief.chars().take(300).collect::<String>()
        ));
    }

    phase("Caddy 已配置，正在验证 HTTPS…");
    let status = gcms_remote_status_inner(&state, &conn_id).await?;
    let (https_ok, http_status, verification_error) = verify_gcms_https(&check.domain).await;
    phase(if https_ok {
        "公网访问已就绪"
    } else {
        "配置已保存，等待 HTTPS 生效"
    });
    Ok(GcmsAccessApplyResult {
        status,
        url: format!("https://{}", check.domain),
        https_ok,
        http_status,
        verification_error,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_remote_gcms_probe() {
        let found = parse_gcms_remote_status(
            "login banner\nPILOT_GCMS_INSTALLED\t1\nPILOT_GCMS_PATH\t/opt/gcms\nPILOT_GCMS_VERSION\tv1.3.36\nPILOT_GCMS_RUNNING\t1\nPILOT_GCMS_BASE_URL\thttps://cms.example.com\n",
        ).unwrap();
        assert!(found.installed && found.running);
        assert_eq!(found.path, "/opt/gcms");
        assert_eq!(found.version, "v1.3.36");
        assert_eq!(found.base_url, "https://cms.example.com");
        assert!(
            !parse_gcms_remote_status("PILOT_GCMS_INSTALLED\t0\n")
                .unwrap()
                .installed
        );
        assert!(parse_gcms_remote_status("unrelated output").is_err());
    }

    #[cfg(not(target_os = "windows"))]
    #[test]
    fn remote_probe_uses_local_http_when_pid_file_is_missing() {
        use std::os::unix::fs::PermissionsExt;

        // /opt/gcms 优先级更高；开发机真的安装过时不改动它，也不让夹具误测另一套安装。
        if std::path::Path::new("/opt/gcms/scripts/cms.sh").is_file()
            && std::path::Path::new("/opt/gcms/current/bin/cms").is_file()
        {
            return;
        }
        let stamp = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let base =
            std::env::temp_dir().join(format!("gcms-pilot-probe-{}-{stamp}", std::process::id()));
        let root = base.join("gcms");
        let fake_bin = base.join("bin");
        std::fs::create_dir_all(root.join("scripts")).unwrap();
        std::fs::create_dir_all(root.join("current/bin")).unwrap();
        std::fs::create_dir_all(root.join("releases")).unwrap();
        std::fs::create_dir_all(root.join("shared")).unwrap();
        std::fs::create_dir_all(&fake_bin).unwrap();
        for path in [root.join("scripts/cms.sh"), root.join("current/bin/cms")] {
            std::fs::write(&path, "#!/bin/sh\nexit 0\n").unwrap();
            std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755)).unwrap();
        }
        std::fs::write(root.join("shared/cms.conf"), "ADDR=127.0.0.1:18080\n").unwrap();
        let fake_curl = fake_bin.join("curl");
        std::fs::write(&fake_curl, "#!/bin/sh\nprintf '204'\n").unwrap();
        std::fs::set_permissions(&fake_curl, std::fs::Permissions::from_mode(0o755)).unwrap();
        let path = format!(
            "{}:{}",
            fake_bin.display(),
            std::env::var("PATH").unwrap_or_default()
        );
        let output = std::process::Command::new("sh")
            .args(["-c", GCMS_REMOTE_PROBE_CMD])
            .env("HOME", &base)
            .env("PATH", path)
            .output()
            .unwrap();
        std::fs::remove_dir_all(&base).ok();
        assert!(output.status.success());
        assert!(String::from_utf8_lossy(&output.stdout).contains("PILOT_GCMS_RUNNING\t1"));
    }

    #[test]
    fn remote_log_is_bounded_and_keeps_tail() {
        let got = gcms_install_log(&format!("{}THE-END", "x".repeat(13_000)), "stderr tail");
        assert!(got.starts_with("…（前段输出已省略）"));
        assert!(got.contains("THE-END") && got.ends_with("stderr tail"));
        assert!(got.chars().count() < 12_100);
    }

    #[test]
    fn validates_domain_before_shell_use() {
        assert_eq!(
            normalize_public_domain(" CMS.Example.COM. ").unwrap(),
            "cms.example.com"
        );
        for bad in [
            "",
            "localhost",
            "https://cms.example.com",
            "cms.example.com/admin",
            "127.0.0.1",
            "*.example.com",
            "cms.example.com;reboot",
            "-cms.example.com",
            "cms..example.com",
            "中文.example.com",
        ] {
            assert!(
                normalize_public_domain(bad).is_err(),
                "{bad} should be rejected"
            );
        }
        assert_eq!(shell_quote("a'b"), "'a'\"'\"'b'");
    }

    #[test]
    fn parses_only_usable_public_ips() {
        let (v4, v6) = parse_remote_public_ips(
            "PILOT_PUBLIC_IPV4\t203.0.113.8\nPILOT_PUBLIC_IPV6\t2001:4860:4860::8888\nPILOT_PUBLIC_IPV4\t127.0.0.1\n");
        assert_eq!(v4, vec!["203.0.113.8".parse::<IpAddr>().unwrap()]);
        assert_eq!(v6, vec!["2001:4860:4860::8888".parse::<IpAddr>().unwrap()]);
    }

    #[test]
    fn direct_dns_rejects_any_stale_address() {
        let server = vec!["203.0.113.8".parse::<IpAddr>().unwrap()];
        let matching = vec!["203.0.113.8".parse::<IpAddr>().unwrap()];
        let mixed = vec![
            "203.0.113.8".parse::<IpAddr>().unwrap(),
            "203.0.113.9".parse::<IpAddr>().unwrap(),
        ];
        assert!(dns_addresses_match_server(&matching, &[], &server, &[]));
        assert!(!dns_addresses_match_server(&mixed, &[], &server, &[]));
        assert!(!dns_addresses_match_server(&[], &[], &server, &[]));
    }

    fn cf_inspection(
        record_type: &str,
        content: &str,
        proxied: bool,
        ssl_mode: &str,
    ) -> cf::CfHostnameInspect {
        cf::CfHostnameInspect {
            zone_id: "zone-id".into(),
            zone_name: "example.com".into(),
            zone_status: "active".into(),
            records: vec![cf::CfDnsRecord {
                record_type: record_type.into(),
                name: "cms.example.com".into(),
                content: content.into(),
                proxied,
                proxiable: true,
            }],
            ssl_mode: ssl_mode.into(),
            ssl_error: String::new(),
        }
    }

    #[test]
    fn accepts_cloudflare_orange_cloud_only_with_matching_origin_and_safe_ssl() {
        let server = vec!["203.0.113.8".parse::<IpAddr>().unwrap()];
        let ready = classify_cloudflare_inspection(
            "cf-1",
            "Cloudflare",
            1,
            "cms.example.com",
            cf_inspection("A", "203.0.113.8", true, "strict"),
            &server,
            &[],
        );
        assert_eq!(ready.status, "matched");
        assert!(ready.proxied && ready.origin_matched);

        let flexible = classify_cloudflare_inspection(
            "cf-1",
            "Cloudflare",
            1,
            "cms.example.com",
            cf_inspection("A", "203.0.113.8", true, "flexible"),
            &server,
            &[],
        );
        assert_eq!(flexible.status, "ssl_incompatible");
        assert!(flexible.origin_matched);

        let mut unreadable = cf_inspection("A", "203.0.113.8", true, "");
        unreadable.ssl_error = "permission denied".into();
        let unreadable = classify_cloudflare_inspection(
            "cf-1",
            "Cloudflare",
            1,
            "cms.example.com",
            unreadable,
            &server,
            &[],
        );
        assert_eq!(unreadable.status, "ssl_unreadable");
    }

    #[test]
    fn cloudflare_dns_only_does_not_require_ssl_setting_permission() {
        let server = vec!["203.0.113.8".parse::<IpAddr>().unwrap()];
        let mut inspect = cf_inspection("A", "203.0.113.8", false, "");
        inspect.ssl_error = "permission denied".into();
        let result = classify_cloudflare_inspection(
            "cf-1",
            "Cloudflare",
            1,
            "cms.example.com",
            inspect,
            &server,
            &[],
        );
        assert_eq!(result.status, "matched");
        assert!(!result.proxied);
    }

    #[test]
    fn cloudflare_cname_is_not_guessed_as_the_origin() {
        let result = classify_cloudflare_inspection(
            "cf-1",
            "Cloudflare",
            1,
            "cms.example.com",
            cf_inspection("CNAME", "origin.example.net", true, "strict"),
            &["203.0.113.8".parse::<IpAddr>().unwrap()],
            &[],
        );
        assert_eq!(result.status, "unsupported_record");
        assert!(!result.origin_matched);
    }

    fn base_probe() -> RemoteCaddyProbe {
        RemoteCaddyProbe {
            os: "linux".into(),
            privilege: "root".into(),
            port_80: "free".into(),
            port_443: "free".into(),
            ..Default::default()
        }
    }

    #[test]
    fn refuses_to_overwrite_custom_caddy() {
        let mut missing = base_probe();
        missing.package_manager = "apt-get".into();
        let missing = classify_caddy_probe(missing);
        assert_eq!(missing.mode, "missing");
        assert!(missing.can_auto_configure);

        let mut standard = base_probe();
        standard.path = "/usr/bin/caddy".into();
        standard.service_exists = true;
        standard.service_running = true;
        standard.process_running = true;
        standard.config_path = "/etc/caddy/Caddyfile".into();
        standard.port_80 = "caddy".into();
        standard.port_443 = "caddy".into();
        let standard = classify_caddy_probe(standard);
        assert_eq!(standard.mode, "standard");
        assert!(standard.can_auto_configure);

        let mut occupied = base_probe();
        occupied.port_80 = "nginx".into();
        assert_eq!(classify_caddy_probe(occupied).mode, "conflict");

        let mut custom = base_probe();
        custom.path = "/usr/local/bin/caddy".into();
        custom.process_running = true;
        custom.config_path = "/srv/caddy/custom.caddy".into();
        assert_eq!(classify_caddy_probe(custom).mode, "custom");

        let mut hidden_binary = base_probe();
        hidden_binary.service_exists = true;
        hidden_binary.process_running = true;
        hidden_binary.config_path = "/etc/caddy/Caddyfile".into();
        hidden_binary.package_manager = "apt-get".into();
        let hidden_binary = classify_caddy_probe(hidden_binary);
        assert_eq!(hidden_binary.mode, "custom");
        assert!(!hidden_binary.can_auto_configure);

        let mut conflict = base_probe();
        conflict.path = "/usr/bin/caddy".into();
        conflict.service_exists = true;
        conflict.config_path = "/etc/caddy/Caddyfile".into();
        conflict.domain_files = vec!["/etc/caddy/conf.d/other.caddy".into()];
        let conflict = classify_caddy_probe(conflict);
        assert_eq!(conflict.mode, "conflict");
        assert_eq!(
            conflict.domain_conflicts,
            vec!["/etc/caddy/conf.d/other.caddy"]
        );
    }

    #[cfg(not(target_os = "windows"))]
    #[test]
    fn remote_shell_scripts_pass_syntax_check() {
        for script in [
            GCMS_REMOTE_PROBE_CMD,
            GCMS_REMOTE_PUBLIC_IP_CMD,
            GCMS_CADDY_PREFLIGHT_CMD,
            GCMS_CADDY_CONFIGURE_CMD,
        ] {
            let status = std::process::Command::new("sh")
                .args(["-n", "-c", script])
                .status()
                .unwrap();
            assert!(status.success());
        }
    }
}
