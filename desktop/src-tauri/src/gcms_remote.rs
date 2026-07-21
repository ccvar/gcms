//! 远程 GCMS 安装与公网 HTTPS 接入。
//!
//! 公网接入按硬闸顺序执行：域名校验 → 服务器公网 IP / DNS 托管识别 → Cloudflare
//! 真实源站核验（若适用）→ Web 服务只读预检 → 备份后配置。只有用户明确点击时才会
//! 为主域名及可选跳转域名幂等创建缺失的灰云 A 记录；不会覆盖 DNS 或自动创建 AAAA。
//! 源站 HTTPS 验证通过后，只有记录仍指向当前服务器且 Zone 使用 Full / Full (strict)
//! 时，才会把这次一键配置涉及的 Cloudflare 记录切换为橙云。

use super::{cf, ensure_ssh, keychain, pack, AppState};
use serde::{Deserialize, Serialize};
use std::collections::HashSet;
use std::fs;
use std::net::{IpAddr, Ipv4Addr};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};
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
if [ -n "${PILOT_GCMS_ROOT:-}" ]; then
  probe_root "$PILOT_GCMS_ROOT" || true
else
  probe_root /opt/gcms || { [ -n "${HOME:-}" ] && probe_root "$HOME/gcms"; } || true
fi
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
redirect_domain=''
port=8080
if [ -f "$root/shared/cms.conf" ]; then
  base_url=$(awk -F= '$1 == "BASE_URL" { sub(/^[^=]*=/, ""); gsub(/^[[:space:]]+|[[:space:]]+$/, ""); print; exit }' "$root/shared/cms.conf" 2>/dev/null || true)
  redirect_domain=$(awk -F= '$1 == "PILOT_REDIRECT_DOMAIN" { sub(/^[^=]*=/, ""); gsub(/^[[:space:]]+|[[:space:]]+$/, ""); print; exit }' "$root/shared/cms.conf" 2>/dev/null || true)
  addr=$(awk -F= '$1 == "ADDR" { sub(/^[^=]*=/, ""); gsub(/^[[:space:]]+|[[:space:]]+$/, ""); print; exit }' "$root/shared/cms.conf" 2>/dev/null || true)
  configured_port=$(printf '%s' "${addr:-:8080}" | sed -n 's/.*:\([0-9][0-9]*\)$/\1/p')
  [ -n "$configured_port" ] && port=$configured_port
fi
password_status=unknown
admin_user=''
bin="$root/current/bin/cms"
# 老版本二进制会忽略未知参数并尝试再启动一个服务。只有确认包含本机状态命令时才调用，
# 避免为了检测密码而触发端口冲突或写入数据库。
if [ -x "$bin" ] && grep -a -Fq 'pilot-security-status' "$bin" 2>/dev/null; then
  conf="$root/shared/cms.conf"
  cms_db=$(awk -F= '$1 == "CMS_DB" { sub(/^[^=]*=/, ""); gsub(/^[[:space:]]+|[[:space:]]+$/, ""); print; exit }' "$conf" 2>/dev/null || true)
  system_db=$(awk -F= '$1 == "SYSTEM_DB" { sub(/^[^=]*=/, ""); gsub(/^[[:space:]]+|[[:space:]]+$/, ""); print; exit }' "$conf" 2>/dev/null || true)
  [ -n "$cms_db" ] || cms_db=shared/data/cms.db
  case "$cms_db" in /*) ;; *) cms_db="$root/$cms_db" ;; esac
  [ -n "$system_db" ] || system_db=$(dirname "$cms_db")/system.db
  case "$system_db" in /*) ;; *) system_db="$root/$system_db" ;; esac
  security_output=$(cd "$root" && CMS_DB="$cms_db" SYSTEM_DB="$system_db" "$bin" pilot-security-status 2>/dev/null || true)
  detected=$(printf '%s\n' "$security_output" | awk -F '\t' '$1 == "PILOT_GCMS_PASSWORD_STATUS" { print $2; exit }')
  case "$detected" in default|changed|unknown) password_status=$detected ;; esac
  admin_user=$(printf '%s\n' "$security_output" | awk -F '\t' '$1 == "PILOT_GCMS_ADMIN_USER" { print $2; exit }')
fi
printf 'PILOT_GCMS_INSTALLED\t1\n'
printf 'PILOT_GCMS_PATH\t%s\n' "$root"
printf 'PILOT_GCMS_VERSION\t%s\n' "$version"
printf 'PILOT_GCMS_RUNNING\t%s\n' "$running"
printf 'PILOT_GCMS_PORT\t%s\n' "$port"
printf 'PILOT_GCMS_BASE_URL\t%s\n' "$base_url"
printf 'PILOT_GCMS_REDIRECT_DOMAIN\t%s\n' "$redirect_domain"
printf 'PILOT_GCMS_PASSWORD_STATUS\t%s\n' "$password_status"
printf 'PILOT_GCMS_ADMIN_USER\t%s\n' "$admin_user"
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

/// 主实例服务控制。只调用探测到的标准安装目录里的管理脚本，不按进程名或端口
/// 结束进程，避免误伤同一台服务器上的迁移实例和其它服务。
const GCMS_REMOTE_SERVICE_ACTION_CMD: &str = r#"
set -eu
root=${PILOT_GCMS_HOME:?}
action=${PILOT_GCMS_ACTION:?}
case "$action" in
  start|restart|stop) ;;
  *) printf '%s\n' '不支持的 GCMS 服务操作' >&2; exit 2 ;;
esac
[ -x "$root/scripts/cms.sh" ] && [ -x "$root/current/bin/cms" ] || {
  printf '%s\n' 'GCMS 标准目录不完整，无法控制服务' >&2
  exit 3
}
(
  cd "$root"
  unset ADDR BASE_URL CMS_DB SYSTEM_DB GCMS_CADDY_ONDEMAND
  ./scripts/cms.sh "$action"
)
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

/// Web 服务只读预检。优先识别当前实际占用 80/443 的 Caddy 或标准 Nginx；
/// 容器、自定义启动、混合端口占用、同域名配置和非 Pilot 站点文件都会被标记。
const GCMS_CADDY_PREFLIGHT_CMD: &str = r#"
set +e
domain=${PILOT_DOMAIN:-}
redirect_domain=${PILOT_REDIRECT_DOMAIN:-}
instance_path=${PILOT_GCMS_INSTANCE_PATH:-}
instance_port=${PILOT_GCMS_INSTANCE_PORT:-}
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
for pm in apt-get dnf yum pacman; do
  if command -v "$pm" >/dev/null 2>&1; then package_manager=$pm; break; fi
done

if [ -n "$instance_path" ] && { [ -z "$instance_port" ] || [ "$instance_port" = 0 ]; }; then
  instance_conf="$instance_path/shared/cms.conf"
  instance_addr=$(awk -F= '$1 == "ADDR" { sub(/^[^=]*=/, ""); gsub(/^[[:space:]]+|[[:space:]]+$/, ""); print; exit }' "$instance_conf" 2>/dev/null || true)
  instance_port=$(printf '%s' "$instance_addr" | sed -n 's/.*:\([0-9][0-9]*\)$/\1/p')
fi
site_file=/etc/caddy/conf.d/gcms.caddy
[ -n "$instance_path" ] && [ -n "$instance_port" ] && site_file="/etc/caddy/conf.d/pilot-gcms-${instance_port}.caddy"
site_exists=0
site_managed=0
if [ -f "$site_file" ]; then
  site_exists=1
  grep -Eq '^# Managed by (GCMS setup-caddy\.sh\.|Pilot migration\.)' "$site_file" 2>/dev/null && site_managed=1
fi

nginx_path=$(command -v nginx 2>/dev/null || true)
nginx_version=''
nginx_config=''
nginx_service_exists=0
nginx_service_running=0
nginx_process=0
nginx_container=0
nginx_config_valid=0
nginx_conf_d_included=0
nginx_certbot_available=0
command -v certbot >/dev/null 2>&1 && nginx_certbot_available=1
if [ -n "$nginx_path" ]; then
  nginx_version=$(nginx -v 2>&1 | head -1 | sed 's|^nginx version:[[:space:]]*||' || true)
  nginx_build=$(nginx -V 2>&1 || true)
  nginx_config=$(printf '%s' "$nginx_build" | sed -n 's/.*--conf-path=\([^[:space:]]*\).*/\1/p' | tail -1)
  [ -z "$nginx_config" ] && [ -f /etc/nginx/nginx.conf ] && nginx_config=/etc/nginx/nginx.conf
  nginx_dump=$(nginx -T 2>&1)
  [ "$?" = 0 ] && nginx_config_valid=1
  printf '%s\n' "$nginx_dump" | grep -Eq 'include[[:space:]]+/etc/nginx/conf\.d/\*\.conf;' && nginx_conf_d_included=1
fi
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  if systemctl list-unit-files nginx.service --no-legend 2>/dev/null | grep -q '^nginx\.service'; then
    nginx_service_exists=1
    systemctl is-active --quiet nginx 2>/dev/null && nginx_service_running=1
  fi
fi
if command -v pgrep >/dev/null 2>&1 && pgrep -x nginx >/dev/null 2>&1; then nginx_process=1; fi
if command -v docker >/dev/null 2>&1; then
  docker ps --format '{{.Names}} {{.Image}}' 2>/dev/null | grep -Eqi '(^|[ /])nginx([ :/@]|$)' && nginx_container=1
fi
nginx_suffix=''
[ -n "$instance_path" ] && [ -n "$instance_port" ] && nginx_suffix="-${instance_port}"
nginx_site_file="/etc/nginx/conf.d/pilot-gcms${nginx_suffix}.conf"
nginx_site_exists=0
nginx_site_managed=0
if [ -f "$nginx_site_file" ]; then
  nginx_site_exists=1
  grep -Fq '# Managed by GCMS Pilot.' "$nginx_site_file" 2>/dev/null && nginx_site_managed=1
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
printf 'PILOT_CADDY_SITE_PATH\t%s\n' "$site_file"
printf 'PILOT_CADDY_SITE_EXISTS\t%s\n' "$site_exists"
printf 'PILOT_CADDY_SITE_MANAGED\t%s\n' "$site_managed"
printf 'PILOT_NGINX_PATH\t%s\n' "$nginx_path"
printf 'PILOT_NGINX_VERSION\t%s\n' "$nginx_version"
printf 'PILOT_NGINX_SERVICE_EXISTS\t%s\n' "$nginx_service_exists"
printf 'PILOT_NGINX_SERVICE_RUNNING\t%s\n' "$nginx_service_running"
printf 'PILOT_NGINX_PROCESS\t%s\n' "$nginx_process"
printf 'PILOT_NGINX_CONTAINER\t%s\n' "$nginx_container"
printf 'PILOT_NGINX_CONFIG\t%s\n' "$nginx_config"
printf 'PILOT_NGINX_CONFIG_VALID\t%s\n' "$nginx_config_valid"
printf 'PILOT_NGINX_CONF_D_INCLUDED\t%s\n' "$nginx_conf_d_included"
printf 'PILOT_NGINX_CERTBOT_AVAILABLE\t%s\n' "$nginx_certbot_available"
printf 'PILOT_NGINX_SITE_PATH\t%s\n' "$nginx_site_file"
printf 'PILOT_NGINX_SITE_EXISTS\t%s\n' "$nginx_site_exists"
printf 'PILOT_NGINX_SITE_MANAGED\t%s\n' "$nginx_site_managed"
if [ -n "$domain" ] && [ -d /etc/caddy ]; then
  for check_domain in "$domain" "$redirect_domain"; do
    [ -n "$check_domain" ] || continue
    escaped_domain=$(printf '%s' "$check_domain" | sed 's/[.]/\\./g')
    find /etc/caddy -type f ! -name '*.gcms-backup-*' ! -name '*.bak*' 2>/dev/null | while IFS= read -r file; do
      grep -Eq "(^|[[:space:],])((https?://)?${escaped_domain})(:[0-9]+)?([[:space:],{]|$)" "$file" 2>/dev/null && printf 'PILOT_CADDY_DOMAIN_FILE\t%s\n' "$file"
    done
  done
fi
if [ -n "$domain" ] && [ -d /etc/nginx ]; then
  for check_domain in "$domain" "$redirect_domain"; do
    [ -n "$check_domain" ] || continue
    escaped_domain=$(printf '%s' "$check_domain" | sed 's/[.]/\\./g')
    find /etc/nginx -type f \( -name 'nginx.conf' -o -name '*.conf' \) ! -name '*.pilot-backup-*' ! -name '*.bak*' 2>/dev/null | while IFS= read -r file; do
      grep -Eqs "^[[:space:]]*server_name[[:space:]]+([^;]*[[:space:]])?${escaped_domain}([[:space:]]|;)" "$file" 2>/dev/null && printf 'PILOT_NGINX_DOMAIN_FILE\t%s\n' "$file"
    done
  done
fi
exit 0
"#;

/// 官方脚本外再做一次完整快照，覆盖下载、安装和重载阶段的失败路径。
const GCMS_CADDY_CONFIGURE_CMD: &str = r#"
set -eu
root=${PILOT_GCMS_HOME:?}
domain=${PILOT_DOMAIN:?}
redirect_domain=${PILOT_REDIRECT_DOMAIN:-}
instance_path=${PILOT_GCMS_INSTANCE_PATH:-}
instance_port=${PILOT_GCMS_INSTANCE_PORT:-}
service_name=${PILOT_GCMS_SERVICE_NAME:-}
conf="$root/shared/cms.conf"
caddyfile=/etc/caddy/Caddyfile
sitefile=/etc/caddy/conf.d/gcms.caddy
[ -x "$root/scripts/cms.sh" ] && [ -f "$conf" ] || { printf '%s\n' 'GCMS 标准目录不完整' >&2; exit 2; }

work=$(mktemp -d 2>/dev/null || mktemp -d -t pilot-gcms-caddy)
migration_active=0
migration_finished=0
cleanup_caddy() {
  code=$?
  trap - EXIT HUP INT TERM
  if [ "$migration_active" = 1 ] && [ "$migration_finished" != 1 ]; then
    restore_instance_before
  fi
  rm -rf "$work"
  exit "$code"
}
trap cleanup_caddy EXIT HUP INT TERM
cp "$conf" "$work/cms.conf"
had_caddyfile=0
had_sitefile=0
if [ -f "$caddyfile" ]; then cp "$caddyfile" "$work/Caddyfile"; had_caddyfile=1; fi
if [ -f "$sitefile" ]; then cp "$sitefile" "$work/gcms.caddy"; had_sitefile=1; fi

restart_gcms() {
  if [ -n "$service_name" ]; then
    command -v systemctl >/dev/null 2>&1 && systemctl cat "$service_name" >/dev/null 2>&1 || { printf '%s\n' "迁移实例服务不存在：$service_name" >&2; return 1; }
    systemctl restart "$service_name"
  else
    (cd "$root"; unset ADDR BASE_URL CMS_DB GCMS_CADDY_ONDEMAND; ./scripts/cms.sh restart)
  fi
}

mark_migration_access() {
  [ -n "$instance_path" ] || return 0
  marker="$instance_path/.pilot-instance"
  [ -f "$marker" ] || return 0
  marker_tmp="${marker}.pilot.$$"
  awk -F= '$1 != "ACCESS_DOMAIN" && $1 != "ACCESS_REDIRECT_DOMAIN" { print }' "$marker" > "$marker_tmp"
  printf 'ACCESS_DOMAIN=%s\n' "$domain" >> "$marker_tmp"
  printf 'ACCESS_REDIRECT_DOMAIN=%s\n' "$redirect_domain" >> "$marker_tmp"
  chmod 600 "$marker_tmp"
  mv "$marker_tmp" "$marker"
}

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
  restart_gcms >/dev/null 2>&1 || true
}

# 迁移实例使用独立站点文件和独立反向代理端口，不调用官方 setup-caddy.sh，
# 避免把目标机原有 /opt/gcms 的域名或配置覆盖掉。
if [ -n "$instance_path" ]; then
  [ -n "$instance_port" ] || { printf '%s\n' '迁移实例缺少端口' >&2; exit 2; }
  if ! command -v caddy >/dev/null 2>&1; then
    if command -v apt-get >/dev/null 2>&1; then
      apt-get update -qq >/dev/null 2>&1 || true
      apt-get install -y caddy >/dev/null 2>&1 || true
    elif command -v dnf >/dev/null 2>&1; then
      dnf install -y caddy >/dev/null 2>&1 || true
    elif command -v yum >/dev/null 2>&1; then
      yum install -y caddy >/dev/null 2>&1 || true
    elif command -v pacman >/dev/null 2>&1; then
      pacman -Sy --noconfirm caddy >/dev/null 2>&1 || true
    fi
  fi
  command -v caddy >/dev/null 2>&1 || { printf '%s\n' '目标服务器尚未安装 Caddy，且当前系统包管理器无法自动安装，请先安装 Caddy 后重试' >&2; exit 127; }
  mkdir -p /etc/caddy/conf.d
  sitefile="/etc/caddy/conf.d/pilot-gcms-${instance_port}.caddy"
  had_instance_site=0
  [ -f "$sitefile" ] && { cp "$sitefile" "$work/instance.caddy"; had_instance_site=1; }
  restore_instance_before() {
    cp "$work/cms.conf" "$conf" 2>/dev/null || true
    if [ "$had_caddyfile" = 1 ]; then cp "$work/Caddyfile" "$caddyfile" 2>/dev/null || true; else rm -f "$caddyfile" 2>/dev/null || true; fi
    if [ "$had_instance_site" = 1 ]; then cp "$work/instance.caddy" "$sitefile" 2>/dev/null || true; else rm -f "$sitefile" 2>/dev/null || true; fi
    if command -v caddy >/dev/null 2>&1 && [ -f "$caddyfile" ] && caddy validate --config "$caddyfile" --adapter caddyfile >/dev/null 2>&1; then
      if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet caddy 2>/dev/null; then
        systemctl reload caddy >/dev/null 2>&1 || true
      else
        caddy reload --config "$caddyfile" --adapter caddyfile >/dev/null 2>&1 || true
      fi
    fi
    restart_gcms >/dev/null 2>&1 || true
  }
  migration_active=1
  if [ -f "$caddyfile" ] && ! grep -Eq '^[[:space:]]*import[[:space:]]+/etc/caddy/conf\.d/\*\.caddy[[:space:]]*$' "$caddyfile"; then
    printf '\nimport /etc/caddy/conf.d/*.caddy\n' >> "$caddyfile"
  elif [ ! -f "$caddyfile" ]; then
    printf 'import /etc/caddy/conf.d/*.caddy\n' > "$caddyfile"
  fi
  {
    printf '# Managed by Pilot migration.\n'
    printf '%s {\n' "$domain"
    printf '    reverse_proxy 127.0.0.1:%s\n' "$instance_port"
    printf '}\n'
    if [ -n "$redirect_domain" ]; then
      printf '\n%s {\n    redir https://%s{uri} 301\n}\n' "$redirect_domain" "$domain"
    fi
  } > "$sitefile"
  set_conf_value() {
    key=$1; value=$2; tmp_conf="${conf}.pilot.$$"
    if grep -q "^${key}=" "$conf" 2>/dev/null; then
      awk -v k="$key" -v v="$value" 'BEGIN{done=0} $0 ~ "^" k "=" { print k "=" v; done=1; next } { print } END{ if (!done) print k "=" v }' "$conf" > "$tmp_conf"
    else
      cp "$conf" "$tmp_conf"; printf '%s=%s\n' "$key" "$value" >> "$tmp_conf"
    fi
    mv "$tmp_conf" "$conf"
  }
  set_conf_value BASE_URL "https://$domain"
  if ! caddy validate --config "$caddyfile" --adapter caddyfile >/dev/null 2>&1; then
    printf '%s\n' '迁移实例 Caddy 配置校验失败，正在恢复…' >&2
    restore_instance_before
    migration_finished=1
    exit 45
  fi
  set +e
  if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet caddy 2>/dev/null; then systemctl reload caddy; else caddy reload --config "$caddyfile" --adapter caddyfile; fi
  reload_code=$?
  set -e
  if [ "$reload_code" -ne 0 ]; then
    printf '%s\n' '迁移实例 Caddy 重载失败，正在恢复…' >&2
    restore_instance_before
    migration_finished=1
    exit 46
  fi
  if ! restart_gcms; then
    printf '%s\n' '迁移实例重启失败，正在恢复域名配置…' >&2
    restore_instance_before
    migration_finished=1
    exit 47
  fi
  mark_migration_access
  migration_finished=1
  exit 0
fi

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

# Pilot 明确使用 301，并让跳转域名完整保留路径与查询参数。官方脚本先生成并校验
# 主域名配置；这里仅在其 GCMS 托管站点文件中加入独立跳转块，再做一次完整校验。
if [ -n "$redirect_domain" ]; then
  tmp_site="${sitefile}.pilot.$$"
  {
    IFS= read -r first_line || true
    printf '%s\n' "$first_line"
    printf '%s {\n' "$redirect_domain"
    printf '    redir https://%s{uri} 301\n' "$domain"
    printf '}\n\n'
    cat
  } < "$sitefile" > "$tmp_site"
  mv "$tmp_site" "$sitefile"
  chmod 0644 "$sitefile"
  if ! caddy validate --config "$caddyfile" --adapter caddyfile; then
    printf '%s\n' '跳转域名配置校验失败，正在恢复修改前的配置…' >&2
    restore_before
    exit 45
  fi
  set +e
  if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet caddy 2>/dev/null; then
    systemctl reload caddy
    reload_code=$?
  else
    caddy reload --config "$caddyfile" --adapter caddyfile
    reload_code=$?
  fi
  set -e
  if [ "$reload_code" -ne 0 ]; then
    printf '%s\n' '跳转域名配置重载失败，正在恢复修改前的配置…' >&2
    restore_before
    exit 46
  fi
fi

set_conf_value() {
  key=$1
  value=$2
  tmp_conf="${conf}.pilot.$$"
  if grep -q "^${key}=" "$conf" 2>/dev/null; then
    awk -v k="$key" -v v="$value" 'BEGIN{done=0} $0 ~ "^" k "=" { print k "=" v; done=1; next } { print } END{ if (!done) print k "=" v }' "$conf" > "$tmp_conf"
  else
    cp "$conf" "$tmp_conf"
    printf '%s=%s\n' "$key" "$value" >> "$tmp_conf"
  fi
  mv "$tmp_conf" "$conf"
}
set_conf_value PILOT_REDIRECT_DOMAIN "$redirect_domain"

# setup-caddy.sh 会更新 shared/cms.conf 中的 BASE_URL / ADDR。GCMS 在启动时读取这些值，
# 所以必须重启；否则新域名虽然能返回后台 HTML，/assets/* 仍会因旧 Host 配置而 404。
# 显式清掉 SSH 登录环境里可能遗留的同名变量，否则 cms.sh 会让环境变量覆盖刚写入的配置。
printf '%s\n' '正在重启 GCMS 以应用新的访问域名…'
set +e
restart_gcms
code=$?
set -e
if [ "$code" -ne 0 ]; then
  printf '%s\n' 'GCMS 重启失败，正在恢复修改前的配置…' >&2
  restore_before
  exit "$code"
fi
"#;

/// 标准 systemd Nginx 接入。只写入 /etc/nginx/conf.d 下的 Pilot 独立文件，
/// 使用 Certbot webroot 获取证书；任一步失败都会恢复站点文件与 GCMS 配置。
const GCMS_NGINX_CONFIGURE_CMD: &str = r#"
set -eu
root=${PILOT_GCMS_HOME:?}
domain=${PILOT_DOMAIN:?}
redirect_domain=${PILOT_REDIRECT_DOMAIN:-}
instance_path=${PILOT_GCMS_INSTANCE_PATH:-}
instance_port=${PILOT_GCMS_INSTANCE_PORT:-}
service_name=${PILOT_GCMS_SERVICE_NAME:-}
conf="$root/shared/cms.conf"
nginx_conf=/etc/nginx/nginx.conf
[ -x "$root/scripts/cms.sh" ] && [ -f "$conf" ] || { printf '%s\n' 'GCMS 标准目录不完整' >&2; exit 2; }
command -v nginx >/dev/null 2>&1 || { printf '%s\n' '未找到标准 nginx 命令' >&2; exit 127; }
[ -f "$nginx_conf" ] || { printf '%s\n' '未找到 /etc/nginx/nginx.conf' >&2; exit 3; }
nginx -t >/dev/null 2>&1 || { printf '%s\n' '现有 Nginx 配置未通过校验' >&2; exit 4; }
grep -Eq 'include[[:space:]]+/etc/nginx/conf\.d/\*\.conf;' "$nginx_conf" 2>/dev/null || { printf '%s\n' 'Nginx 未加载 /etc/nginx/conf.d/*.conf' >&2; exit 5; }

port=$instance_port
if [ -z "$port" ] || [ "$port" = 0 ]; then
  addr=$(awk -F= '$1 == "ADDR" { sub(/^[^=]*=/, ""); gsub(/^[[:space:]]+|[[:space:]]+$/, ""); print; exit }' "$conf" 2>/dev/null || true)
  port=$(printf '%s' "${addr:-:8080}" | sed -n 's/.*:\([0-9][0-9]*\)$/\1/p')
fi
[ -n "$port" ] || port=8080
case "$port" in *[!0-9]*|'') printf '%s\n' 'GCMS 监听端口无效' >&2; exit 6 ;; esac

suffix=''
[ -n "$instance_path" ] && suffix="-${port}"
sitefile="/etc/nginx/conf.d/pilot-gcms${suffix}.conf"
safe_domain=$(printf '%s' "$domain" | tr -c 'A-Za-z0-9.-' '-')
cert_name="pilot-gcms-${safe_domain}"
cert_dir="/etc/letsencrypt/live/${cert_name}"
acme_root=/var/lib/pilot-gcms-acme

work=$(mktemp -d 2>/dev/null || mktemp -d -t pilot-gcms-nginx)
cp "$conf" "$work/cms.conf"
had_site=0
[ -f "$sitefile" ] && { cp "$sitefile" "$work/site.conf"; had_site=1; }
finished=0
gcms_changed=0

reload_nginx() {
  nginx -t || return 1
  if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet nginx 2>/dev/null; then
    systemctl reload nginx
  else
    nginx -s reload
  fi
}

restart_gcms() {
  if [ -n "$service_name" ]; then
    command -v systemctl >/dev/null 2>&1 && systemctl cat "$service_name" >/dev/null 2>&1 || { printf '%s\n' "迁移实例服务不存在：$service_name" >&2; return 1; }
    systemctl restart "$service_name"
  else
    (cd "$root"; unset ADDR BASE_URL CMS_DB GCMS_CADDY_ONDEMAND; ./scripts/cms.sh restart)
  fi
}

mark_migration_access() {
  [ -n "$instance_path" ] || return 0
  marker="$instance_path/.pilot-instance"
  [ -f "$marker" ] || return 0
  marker_tmp="${marker}.pilot.$$"
  awk -F= '$1 != "ACCESS_DOMAIN" && $1 != "ACCESS_REDIRECT_DOMAIN" { print }' "$marker" > "$marker_tmp"
  printf 'ACCESS_DOMAIN=%s\n' "$domain" >> "$marker_tmp"
  printf 'ACCESS_REDIRECT_DOMAIN=%s\n' "$redirect_domain" >> "$marker_tmp"
  chmod 600 "$marker_tmp"
  mv "$marker_tmp" "$marker"
}

restore_before() {
  set +e
  cp "$work/cms.conf" "$conf" 2>/dev/null || true
  if [ "$had_site" = 1 ]; then cp "$work/site.conf" "$sitefile" 2>/dev/null || true; else rm -f "$sitefile" 2>/dev/null || true; fi
  reload_nginx >/dev/null 2>&1 || true
  if [ "$gcms_changed" = 1 ]; then
    restart_gcms >/dev/null 2>&1 || true
  fi
  set -e
}

cleanup() {
  code=$?
  trap - EXIT HUP INT TERM
  [ "$finished" = 1 ] || restore_before
  rm -rf "$work"
  exit "$code"
}
trap cleanup EXIT HUP INT TERM

mkdir -p /etc/nginx/conf.d "$acme_root/.well-known/acme-challenge"
ipv6_listen=0
[ -s /proc/net/if_inet6 ] && ipv6_listen=1
{
  printf '# Managed by GCMS Pilot.\n'
  printf '# Temporary HTTP site used for certificate issuance.\n'
  printf 'server {\n'
  printf '    listen 80;\n'
  [ "$ipv6_listen" = 1 ] && printf '    listen [::]:80;\n'
  printf '    server_name %s' "$domain"
  [ -n "$redirect_domain" ] && printf ' %s' "$redirect_domain"
  printf ';\n'
  printf '    location ^~ /.well-known/acme-challenge/ {\n'
  printf '        root %s;\n' "$acme_root"
  printf '        default_type text/plain;\n'
  printf '        try_files $uri =404;\n'
  printf '    }\n'
  printf '    location / {\n'
  printf '        proxy_pass http://127.0.0.1:%s;\n' "$port"
  printf '        proxy_http_version 1.1;\n'
  printf '        proxy_set_header Host $host;\n'
  printf '        proxy_set_header X-Real-IP $remote_addr;\n'
  printf '        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n'
  printf '        proxy_set_header X-Forwarded-Proto http;\n'
  printf '    }\n'
  printf '}\n'
} > "$sitefile"
chmod 0644 "$sitefile"
reload_nginx

if ! command -v certbot >/dev/null 2>&1; then
  printf '%s\n' '正在安装 Certbot…'
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y certbot
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y certbot
  elif command -v yum >/dev/null 2>&1; then
    yum install -y certbot
  elif command -v pacman >/dev/null 2>&1; then
    pacman -Sy --noconfirm certbot
  else
    printf '%s\n' '未检测到可安装 Certbot 的包管理器' >&2
    exit 127
  fi
fi
command -v certbot >/dev/null 2>&1 || { printf '%s\n' 'Certbot 安装失败' >&2; exit 127; }

printf '%s\n' '正在申请 HTTPS 证书…'
set -- certbot certonly --webroot --webroot-path "$acme_root" --cert-name "$cert_name" --non-interactive --agree-tos --register-unsafely-without-email --keep-until-expiring --expand --preferred-challenges http --deploy-hook 'nginx -t && { systemctl reload nginx 2>/dev/null || nginx -s reload; }'
set -- "$@" -d "$domain"
[ -n "$redirect_domain" ] && set -- "$@" -d "$redirect_domain"
"$@"
[ -s "$cert_dir/fullchain.pem" ] && [ -s "$cert_dir/privkey.pem" ] || { printf '%s\n' '证书申请完成但未找到证书文件' >&2; exit 7; }

{
  printf '# Managed by GCMS Pilot.\n'
  printf 'server {\n'
  printf '    listen 80;\n'
  [ "$ipv6_listen" = 1 ] && printf '    listen [::]:80;\n'
  printf '    server_name %s' "$domain"
  [ -n "$redirect_domain" ] && printf ' %s' "$redirect_domain"
  printf ';\n'
  printf '    location ^~ /.well-known/acme-challenge/ {\n'
  printf '        root %s;\n' "$acme_root"
  printf '        default_type text/plain;\n'
  printf '        try_files $uri =404;\n'
  printf '    }\n'
  printf '    location / { return 301 https://%s$request_uri; }\n' "$domain"
  printf '}\n\n'
  printf 'server {\n'
  printf '    listen 443 ssl;\n'
  [ "$ipv6_listen" = 1 ] && printf '    listen [::]:443 ssl;\n'
  printf '    server_name %s;\n' "$domain"
  printf '    ssl_certificate %s/fullchain.pem;\n' "$cert_dir"
  printf '    ssl_certificate_key %s/privkey.pem;\n' "$cert_dir"
  printf '    ssl_protocols TLSv1.2 TLSv1.3;\n'
  printf '    client_max_body_size 100m;\n'
  printf '    location / {\n'
  printf '        proxy_pass http://127.0.0.1:%s;\n' "$port"
  printf '        proxy_http_version 1.1;\n'
  printf '        proxy_set_header Host $host;\n'
  printf '        proxy_set_header X-Real-IP $remote_addr;\n'
  printf '        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n'
  printf '        proxy_set_header X-Forwarded-Proto https;\n'
  printf '        proxy_set_header Upgrade $http_upgrade;\n'
  printf '        proxy_set_header Connection "upgrade";\n'
  printf '        proxy_read_timeout 300s;\n'
  printf '        proxy_buffering off;\n'
  printf '    }\n'
  printf '}\n'
  if [ -n "$redirect_domain" ]; then
    printf '\nserver {\n'
    printf '    listen 443 ssl;\n'
    [ "$ipv6_listen" = 1 ] && printf '    listen [::]:443 ssl;\n'
    printf '    server_name %s;\n' "$redirect_domain"
    printf '    ssl_certificate %s/fullchain.pem;\n' "$cert_dir"
    printf '    ssl_certificate_key %s/privkey.pem;\n' "$cert_dir"
    printf '    return 301 https://%s$request_uri;\n' "$domain"
    printf '}\n'
  fi
} > "$sitefile"
chmod 0644 "$sitefile"
reload_nginx

set_conf_value() {
  key=$1
  value=$2
  tmp_conf="${conf}.pilot.$$"
  if grep -q "^${key}=" "$conf" 2>/dev/null; then
    awk -v k="$key" -v v="$value" 'BEGIN{done=0} $0 ~ "^" k "=" { print k "=" v; done=1; next } { print } END{ if (!done) print k "=" v }' "$conf" > "$tmp_conf"
  else
    cp "$conf" "$tmp_conf"
    printf '%s=%s\n' "$key" "$value" >> "$tmp_conf"
  fi
  mv "$tmp_conf" "$conf"
}
set_conf_value ADDR "127.0.0.1:$port"
set_conf_value BASE_URL "https://$domain"
set_conf_value PILOT_REDIRECT_DOMAIN "$redirect_domain"
gcms_changed=1

printf '%s\n' '正在重启 GCMS 以应用新的访问域名…'
restart_gcms
mark_migration_access
finished=1
"#;

/// 修复已写入新域名、但实际仍由旧 GCMS 进程提供服务的安装。
///
/// 老安装可能遗留失效 PID 文件，或 SSH 登录环境中还导出了旧 BASE_URL：此时
/// `cms.sh restart` 看似执行过，真正占用端口的进程却没有加载 shared/cms.conf。
/// 脚本只会接管安装根目录下的 GCMS 二进制，不会按端口盲杀其它服务。
const GCMS_REMOTE_RELOAD_DOMAIN_CMD: &str = r#"
set -eu
root=${PILOT_GCMS_HOME:?}
domain=${PILOT_DOMAIN:?}
service_name=${PILOT_GCMS_SERVICE_NAME:-}
conf="$root/shared/cms.conf"
[ -x "$root/scripts/cms.sh" ] && [ -x "$root/current/bin/cms" ] && [ -f "$conf" ] || {
  printf '%s\n' 'GCMS 标准目录不完整，无法重新加载域名配置' >&2
  exit 2
}

set_conf_value() {
  key=$1
  value=$2
  tmp="${conf}.pilot.$$"
  if grep -q "^${key}=" "$conf" 2>/dev/null; then
    awk -v k="$key" -v v="$value" 'BEGIN{done=0} $0 ~ "^" k "=" { print k "=" v; done=1; next } { print } END{ if (!done) print k "=" v }' "$conf" > "$tmp"
  else
    cp "$conf" "$tmp"
    printf '%s=%s\n' "$key" "$value" >> "$tmp"
  fi
  mv "$tmp" "$conf"
}

# “重新检测”针对的就是已经确认过的访问域名。再次写入可修复旧版 Pilot 曾被
# 登录环境 BASE_URL 覆盖、导致配置文件仍是 localhost 的情况。
set_conf_value BASE_URL "https://$domain"

restart_from_conf() {
  if [ -n "$service_name" ]; then
    command -v systemctl >/dev/null 2>&1 && systemctl cat "$service_name" >/dev/null 2>&1 || { printf '%s\n' "迁移实例服务不存在：$service_name" >&2; return 1; }
    systemctl restart "$service_name"
  else
    (
      cd "$root"
      unset ADDR BASE_URL CMS_DB GCMS_CADDY_ONDEMAND
      ./scripts/cms.sh restart
    )
  fi
}

asset_code() {
  command -v curl >/dev/null 2>&1 || { printf '000'; return; }
  addr=$(awk -F= '$1 == "ADDR" { sub(/^[^=]*=/, ""); gsub(/^[[:space:]]+|[[:space:]]+$/, ""); print; exit }' "$conf" 2>/dev/null || true)
  [ -n "$addr" ] || addr=127.0.0.1:8080
  case "$addr" in
    :*) target="127.0.0.1$addr" ;;
    0.0.0.0:*) target="127.0.0.1:${addr##*:}" ;;
    '[::]':*) target="127.0.0.1:${addr##*:}" ;;
    '[::1]':*) target="$addr" ;;
    *) target="$addr" ;;
  esac
  curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' -H "Host: $domain" --connect-timeout 3 --max-time 6 "http://$target/assets/css/admin.css" 2>/dev/null || printf '000'
}

asset_ready() {
  i=0
  while [ "$i" -lt 5 ]; do
    code=$(asset_code)
    case "$code" in 2[0-9][0-9]|3[0-9][0-9]) return 0 ;; esac
    i=$((i + 1))
    sleep 1
  done
  return 1
}

set +e
restart_from_conf
restart_code=$?
set -e
if [ "$restart_code" -eq 0 ] && asset_ready; then
  printf 'PILOT_GCMS_RELOADED\t1\n'
  exit 0
fi

# PID 文件失效时，cms.sh 无法结束旧进程，新进程又会因端口占用而退出。
# 仅枚举 exe 位于当前标准安装目录内的 cms 进程，绝不按名称或端口广泛终止。
root_real=$(readlink -f "$root" 2>/dev/null || printf '%s' "$root")
current_real=$(readlink -f "$root/current/bin/cms" 2>/dev/null || printf '')
pids=''
managed_unit=''
if [ -d /proc ]; then
  for proc in /proc/[0-9]*; do
    [ -r "$proc/exe" ] || continue
    pid=${proc##*/}
    exe=$(readlink "$proc/exe" 2>/dev/null || true)
    exe=${exe% (deleted)}
    case "$exe" in
      "$current_real"|"$root_real"/releases/*/bin/cms|"$root_real"/bin/cms)
        unit=$(sed -n 's#^.*/\([^/]*\.service\)$#\1#p' "$proc/cgroup" 2>/dev/null | head -1 || true)
        if [ -n "$unit" ] && command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet "$unit" 2>/dev/null; then
          managed_unit=$unit
        else
          pids="$pids $pid"
        fi
        ;;
    esac
  done
fi

if [ -n "$managed_unit" ]; then
  printf 'GCMS 由 systemd 服务 %s 托管；该服务仍在使用旧 BASE_URL，请更新服务环境后重启。\n' "$managed_unit" >&2
  exit 46
fi

if [ -n "$pids" ]; then
  for pid in $pids; do kill "$pid" 2>/dev/null || true; done
  i=0
  while [ "$i" -lt 12 ]; do
    alive=''
    for pid in $pids; do kill -0 "$pid" 2>/dev/null && alive="$alive $pid"; done
    [ -z "$alive" ] && break
    sleep 1
    i=$((i + 1))
  done
  for pid in $pids; do kill -0 "$pid" 2>/dev/null && kill -9 "$pid" 2>/dev/null || true; done
  rm -f "$root/run/cms.pid"
  (
    cd "$root"
    unset ADDR BASE_URL CMS_DB GCMS_CADDY_ONDEMAND
    ./scripts/cms.sh start
  )
fi

if asset_ready; then
  printf 'PILOT_GCMS_RELOADED\t1\n'
  exit 0
fi

printf 'GCMS 已重启，但仍未按 %s 提供页面资源；请查看 %s/logs/cms.log。\n' "$domain" "$root" >&2
exit 47
"#;

#[derive(Clone, Serialize, Default, Debug, PartialEq)]
pub(super) struct GcmsRemoteStatus {
    installed: bool,
    version: String,
    path: String,
    running: bool,
    port: u16,
    base_url: String,
    redirect_domain: String,
    /// default | changed | unknown
    password_status: String,
    admin_user: String,
}

#[derive(Clone, Serialize, Default, Debug, PartialEq)]
pub(super) struct GcmsMigrationServer {
    id: String,
    connection_id: String,
    name: String,
    server_name: String,
    instance_kind: String,
    role: String,
    installed: bool,
    version: String,
    path: String,
    running: bool,
    port: u16,
    base_url: String,
    redirect_domain: String,
    service_ready: bool,
    service_detail: String,
}

#[derive(Clone, Serialize, Default, Debug, PartialEq)]
pub(super) struct GcmsMigrationPreflight {
    target: GcmsMigrationServer,
    sources: Vec<GcmsMigrationServer>,
    issues: Vec<String>,
    domain_conflicts: Vec<String>,
    can_start: bool,
}

#[derive(Default)]
struct GcmsMigrationTargetEnv {
    privilege: String,
    systemd: bool,
    root: String,
}

#[derive(Clone, Serialize, Deserialize, Default, Debug, PartialEq)]
#[serde(default)]
pub(super) struct GcmsMigrationSnapshot {
    id: String,
    target_id: String,
    source_id: String,
    source_name: String,
    version: String,
    bytes: u64,
    instance_path: String,
    port: u16,
    source_base_url: String,
    source_redirect_domain: String,
    base_url: String,
    redirect_domain: String,
    access_configured: bool,
    https_ok: bool,
    cloudflare_proxy_applicable: bool,
    cloudflare_proxied: bool,
    cloudflare_proxy_error: String,
    service_name: String,
    service_installed: bool,
    running: bool,
    created_at: u64,
    updated_at: u64,
    last_error: String,
}

#[derive(Clone, Serialize, Default, Debug, PartialEq)]
pub(super) struct GcmsMigrationStageResult {
    target_id: String,
    snapshots: Vec<GcmsMigrationSnapshot>,
    failures: Vec<String>,
    backup_path: String,
}

#[derive(Clone, Debug)]
struct GcmsMigrationSourceSpec {
    selection_id: String,
    /// 用于生成目标实例 ID；主实例沿用旧版 connection id，避免升级后重复迁移。
    identity: String,
    connection_id: String,
    name: String,
    server_name: String,
    instance_kind: String,
    status: GcmsRemoteStatus,
}

fn migration_now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|duration| duration.as_secs())
        .unwrap_or(0)
}

fn migration_registry_path(data_dir: &Path) -> PathBuf {
    data_dir.join("gcms-migration-instances.json")
}

struct MigrationCacheGuard(PathBuf);

impl Drop for MigrationCacheGuard {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn clear_migration_cache(data_dir: &Path) {
    let _ = fs::remove_dir_all(data_dir.join("migration-cache"));
}

fn read_migration_registry(data_dir: &Path) -> Vec<GcmsMigrationSnapshot> {
    let mut instances: Vec<GcmsMigrationSnapshot> = fs::read(migration_registry_path(data_dir))
        .ok()
        .and_then(|raw| serde_json::from_slice(&raw).ok())
        .unwrap_or_default();
    // 早期记录只有 base_url 字段，而该值实际来自源实例复制后的 cms.conf，不能据此
    // 判断目标服务器已经接管域名。升级时把它迁到“源域名”槽位，避免误显示可访问。
    for instance in &mut instances {
        if !instance.access_configured
            && instance.source_base_url.is_empty()
            && !instance.base_url.is_empty()
        {
            instance.source_base_url = std::mem::take(&mut instance.base_url);
            instance.source_redirect_domain = std::mem::take(&mut instance.redirect_domain);
        }
    }
    instances
}

fn save_migration_registry(
    data_dir: &Path,
    instances: &[GcmsMigrationSnapshot],
) -> Result<(), String> {
    let path = migration_registry_path(data_dir);
    let raw = serde_json::to_vec_pretty(instances)
        .map_err(|error| format!("序列化迁移实例注册表失败：{error}"))?;
    let tmp = path.with_extension("json.tmp");
    fs::write(&tmp, raw).map_err(|error| format!("写入迁移实例注册表失败：{error}"))?;
    #[cfg(target_os = "windows")]
    if path.exists() {
        fs::remove_file(&path).map_err(|error| format!("更新迁移实例注册表失败：{error}"))?;
    }
    fs::rename(&tmp, &path).map_err(|error| format!("保存迁移实例注册表失败：{error}"))
}

fn upsert_migration_instance(
    data_dir: &Path,
    instance: GcmsMigrationSnapshot,
) -> Result<GcmsMigrationSnapshot, String> {
    let mut instances = read_migration_registry(data_dir);
    if let Some(current) = instances.iter_mut().find(|item| item.id == instance.id) {
        *current = instance.clone();
    } else {
        instances.push(instance.clone());
    }
    save_migration_registry(data_dir, &instances)?;
    Ok(instance)
}

fn update_migration_instance_status(
    data_dir: &Path,
    instance_id: &str,
    status: &GcmsRemoteStatus,
    https_ok: bool,
    cloudflare_proxy_applicable: bool,
    cloudflare_proxied: bool,
    cloudflare_proxy_error: &str,
    last_error: Option<&str>,
) -> Result<(), String> {
    if instance_id.is_empty() {
        return Ok(());
    }
    let mut instances = read_migration_registry(data_dir);
    let Some(instance) = instances.iter_mut().find(|item| item.id == instance_id) else {
        return Err("迁移实例记录不存在，未更新域名状态".into());
    };
    instance.running = status.running;
    if !status.version.is_empty() {
        instance.version = status.version.clone();
    }
    instance.base_url = status.base_url.clone();
    instance.redirect_domain = status.redirect_domain.clone();
    instance.access_configured = true;
    instance.https_ok = https_ok;
    instance.cloudflare_proxy_applicable = cloudflare_proxy_applicable;
    instance.cloudflare_proxied = cloudflare_proxied;
    instance.cloudflare_proxy_error = cloudflare_proxy_error.to_string();
    instance.last_error = last_error.unwrap_or_default().to_string();
    instance.updated_at = migration_now();
    save_migration_registry(data_dir, &instances)
}

fn clear_migration_instance_access(
    data_dir: &Path,
    instance_id: &str,
    last_error: &str,
) -> Result<(), String> {
    let mut instances = read_migration_registry(data_dir);
    let Some(instance) = instances.iter_mut().find(|item| item.id == instance_id) else {
        return Err("迁移实例记录不存在，无法清除域名状态".into());
    };
    instance.base_url.clear();
    instance.redirect_domain.clear();
    instance.access_configured = false;
    instance.https_ok = false;
    instance.cloudflare_proxy_applicable = false;
    instance.cloudflare_proxied = false;
    instance.cloudflare_proxy_error.clear();
    instance.last_error = last_error.to_string();
    instance.updated_at = migration_now();
    save_migration_registry(data_dir, &instances)
}

fn migration_instance_for_request(
    data_dir: &Path,
    instance_id: Option<&str>,
    conn_id: &str,
    instance_path: Option<&str>,
) -> Result<Option<GcmsMigrationSnapshot>, String> {
    let Some(instance_id) = instance_id.filter(|value| !value.is_empty()) else {
        return Ok(None);
    };
    let instance = read_migration_registry(data_dir)
        .into_iter()
        .find(|item| item.id == instance_id)
        .ok_or("迁移实例记录不存在")?;
    if instance.target_id != conn_id {
        return Err("迁移实例与当前目标服务器不一致".into());
    }
    if let Some(path) = instance_path.filter(|value| !value.is_empty()) {
        if path != instance.instance_path {
            return Err("迁移实例目录与本地登记不一致".into());
        }
    }
    Ok(Some(instance))
}

fn migration_instance_id(target_id: &str, source_id: &str) -> String {
    // FNV-1a 在不同 Rust / Pilot 版本间保持稳定；不能使用 DefaultHasher，后者不承诺
    // 算法稳定，升级后可能把同一源→目标误认成新实例。
    let mut hash = 0xcbf29ce484222325u64;
    for byte in target_id
        .as_bytes()
        .iter()
        .copied()
        .chain(std::iter::once(0xff))
        .chain(source_id.as_bytes().iter().copied())
    {
        hash ^= u64::from(byte);
        hash = hash.wrapping_mul(0x100000001b3);
    }
    format!("gcms-{hash:016x}")
}

async fn resolve_migration_source(
    state: &AppState,
    selection_id: &str,
) -> Result<GcmsMigrationSourceSpec, String> {
    let selection_id = selection_id.trim();
    if selection_id.is_empty() {
        return Err("源实例标识为空".into());
    }
    if let Some(instance_id) = selection_id.strip_prefix("instance:") {
        if instance_id.is_empty() {
            return Err("迁移实例标识为空".into());
        }
        let snapshot = read_migration_registry(&state.data_dir)
            .into_iter()
            .find(|item| item.id == instance_id)
            .ok_or_else(|| format!("迁移实例记录不存在：{instance_id}"))?;
        if snapshot.target_id.is_empty() || snapshot.instance_path.is_empty() {
            return Err(format!(
                "迁移实例「{}」的来源信息不完整",
                snapshot.source_name
            ));
        }
        let connection = state.conns.get(&snapshot.target_id)?;
        if connection.kind != "ssh" {
            return Err(format!(
                "迁移实例「{}」不在 SSH 服务器上",
                snapshot.source_name
            ));
        }
        let mut status =
            gcms_remote_status_at(state, &snapshot.target_id, Some(&snapshot.instance_path))
                .await?;
        if !status.installed || status.path.is_empty() {
            return Err(format!(
                "迁移实例「{}」的目录已不存在",
                snapshot.source_name
            ));
        }
        // 未接管域名的迁移实例在磁盘配置里仍保留原域名；继续迁移时将其作为候选
        // 原域名传递，但不能误判为当前服务器已经接管。
        if snapshot.access_configured {
            if status.base_url.is_empty() {
                status.base_url = snapshot.base_url.clone();
            }
            if status.redirect_domain.is_empty() {
                status.redirect_domain = snapshot.redirect_domain.clone();
            }
        } else {
            status.base_url = snapshot.source_base_url.clone();
            status.redirect_domain = snapshot.source_redirect_domain.clone();
        }
        let domain = domain_from_base_url(&status.base_url).ok();
        let name = domain
            .filter(|value| !value.is_empty())
            .unwrap_or_else(|| snapshot.source_name.clone());
        return Ok(GcmsMigrationSourceSpec {
            selection_id: selection_id.to_string(),
            identity: format!("instance:{}", snapshot.id),
            connection_id: snapshot.target_id,
            name,
            server_name: connection.name,
            instance_kind: "migration".into(),
            status,
        });
    }

    let connection_id = selection_id.strip_prefix("main:").unwrap_or(selection_id);
    if connection_id.is_empty() {
        return Err("主实例连接标识为空".into());
    }
    let connection = state.conns.get(connection_id)?;
    if connection.kind != "ssh" {
        return Err(format!("「{}」不是 SSH 服务器", connection.name));
    }
    let status = gcms_remote_status_inner(state, connection_id).await?;
    Ok(GcmsMigrationSourceSpec {
        selection_id: format!("main:{connection_id}"),
        // 兼容旧记录：旧版直接用 connection id 生成稳定实例 ID。
        identity: connection_id.to_string(),
        connection_id: connection_id.to_string(),
        name: connection.name.clone(),
        server_name: connection.name,
        instance_kind: "main".into(),
        status,
    })
}

async fn resolve_migration_sources(
    state: &AppState,
    source_ids: &[String],
) -> Result<Vec<GcmsMigrationSourceSpec>, String> {
    let mut sources = Vec::with_capacity(source_ids.len());
    for source_id in source_ids {
        sources.push(resolve_migration_source(state, source_id).await?);
    }
    Ok(sources)
}

const GCMS_MIGRATION_TARGET_ENV_CMD: &str = r#"
set +e
uid=$(id -u 2>/dev/null || printf 'unknown')
privilege=none
if [ "$uid" = 0 ]; then
  privilege=root
elif command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
  privilege=sudo
fi
systemd=0
command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ] && systemd=1
if [ "$uid" = 0 ]; then root=/opt/gcms-instances; else root="${HOME:?}/gcms-instances"; fi
printf 'PILOT_MIGRATION_PRIVILEGE\t%s\n' "$privilege"
printf 'PILOT_MIGRATION_SYSTEMD\t%s\n' "$systemd"
printf 'PILOT_MIGRATION_ROOT\t%s\n' "$root"
exit 0
"#;

const GCMS_MIGRATION_STAGE_CMD: &str = r#"
set -eu
umask 077
root=${PILOT_GCMS_ROOT:?}
archive=${PILOT_GCMS_ARCHIVE:?}
expect_running=${PILOT_GCMS_EXPECT_RUNNING:-0}
mkdir -p "$(dirname "$archive")"
chmod 700 "$(dirname "$archive")" 2>/dev/null || true
was_running=0
pid=''
if [ -s "$root/run/cms.pid" ]; then
  pid=$(cat "$root/run/cms.pid" 2>/dev/null || true)
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null && was_running=1 || true
fi
if [ "$was_running" = 0 ] && [ -d /proc ]; then
  root_real=$(readlink -f "$root" 2>/dev/null || printf '%s' "$root")
  current_real=$(readlink -f "$root/current/bin/cms" 2>/dev/null || printf '')
  for proc in /proc/[0-9]*; do
    [ -r "$proc/exe" ] || continue
    candidate=$(readlink "$proc/exe" 2>/dev/null || true)
    candidate=${candidate% (deleted)}
    case "$candidate" in
      "$current_real"|"$root_real"/releases/*/bin/cms|"$root_real"/bin/cms)
        pid=${proc##*/}; was_running=1; break ;;
    esac
  done
fi
[ "$expect_running" != 1 ] || [ "$was_running" = 1 ] || { printf '%s\n' '源 GCMS 显示运行中，但无法安全定位其进程，未创建在线快照' >&2; exit 3; }
managed_unit=''
if [ "$was_running" = 1 ] && [ -r "/proc/$pid/cgroup" ]; then
  managed_unit=$(sed -n 's#^.*/\([^/]*\.service\)$#\1#p' "/proc/$pid/cgroup" 2>/dev/null | head -1 || true)
  command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet "$managed_unit" 2>/dev/null || managed_unit=''
fi
source_service() {
  action=$1
  if [ -n "$managed_unit" ]; then
    if [ "$(id -u)" = 0 ]; then systemctl "$action" "$managed_unit"
    elif command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then sudo -n systemctl "$action" "$managed_unit"
    else return 77
    fi
  else
    (cd "$root"; unset ADDR BASE_URL CMS_DB GCMS_CADDY_ONDEMAND; ./scripts/cms.sh "$action")
  fi
}
[ "$was_running" != 1 ] || [ -x "$root/scripts/cms.sh" ] || { printf '%s\n' '源 GCMS 缺少可执行的管理脚本，无法安全停机快照' >&2; exit 3; }
restart=0
cleanup() {
  if [ "$restart" = 1 ] && [ -x "$root/scripts/cms.sh" ]; then
    source_service start >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT HUP INT TERM
if [ "$was_running" = 1 ] && [ -x "$root/scripts/cms.sh" ]; then
  restart=1
  if ! source_service stop >/dev/null 2>&1; then
    printf '%s\n' '源 GCMS 停止失败，未创建可能不一致的快照' >&2
    exit 3
  fi
  i=0
  while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 15 ]; do sleep 1; i=$((i + 1)); done
  kill -0 "$pid" 2>/dev/null && { printf '%s\n' '源 GCMS 未在安全窗口内停止，迁移已取消' >&2; exit 3; }
fi
tar -C "$root" -czf "$archive" .
chmod 600 "$archive"
snapshot_bytes=$(wc -c < "$archive" | tr -d ' ')
if [ "$restart" = 1 ]; then
  if ! source_service start >/dev/null 2>&1; then
    printf '%s\n' '快照已创建，但源 GCMS 恢复运行失败' >&2
    exit 4
  fi
  new_pid=$(cat "$root/run/cms.pid" 2>/dev/null || true)
  [ -n "$new_pid" ] && kill -0 "$new_pid" 2>/dev/null || { printf '%s\n' '快照已创建，但源 GCMS 未恢复运行' >&2; exit 4; }
  restart=0
fi
printf 'PILOT_GCMS_SNAPSHOT_BYTES\t%s\n' "$snapshot_bytes"
"#;

const GCMS_MIGRATION_RESTORE_CMD: &str = r#"
set -eu
umask 077
archive=${PILOT_GCMS_ARCHIVE:?}
instance=${PILOT_GCMS_INSTANCE:?}
requested_port=${PILOT_GCMS_PORT:?}
instance_id=${PILOT_GCMS_INSTANCE_ID:?}
source_id=${PILOT_GCMS_SOURCE_ID:?}
service_name=${PILOT_GCMS_SERVICE_NAME:?}
marker="$instance/.pilot-instance"
created_instance=0
restore_finished=0
cleanup_restore() {
  code=$?
  trap - EXIT HUP INT TERM
  if [ "$restore_finished" != 1 ] && [ "$created_instance" = 1 ]; then
    [ -x "$instance/scripts/cms.sh" ] && (cd "$instance"; unset ADDR BASE_URL CMS_DB GCMS_CADDY_ONDEMAND; ./scripts/cms.sh stop) >/dev/null 2>&1 || true
    rm -rf "$instance"
    [ "$code" -ne 0 ] || code=1
  fi
  exit "$code"
}
trap cleanup_restore EXIT HUP INT TERM
if [ -d "$instance" ]; then
  existing_source=$(awk -F= '$1 == "SOURCE_ID" { print $2; exit }' "$marker" 2>/dev/null || true)
  existing_port=$(awk -F= '$1 == "PORT" { print $2; exit }' "$marker" 2>/dev/null || true)
  if [ "$existing_source" = "$source_id" ] && [ -n "$existing_port" ] && [ -x "$instance/scripts/cms.sh" ]; then
    if [ -s "$instance/run/cms.pid" ]; then
      pid=$(cat "$instance/run/cms.pid" 2>/dev/null || true)
    else
      pid=''
    fi
    [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null || (cd "$instance"; unset ADDR BASE_URL CMS_DB GCMS_CADDY_ONDEMAND; ./scripts/cms.sh start) >/dev/null 2>&1 || true
    printf 'PILOT_GCMS_INSTANCE_PORT\t%s\n' "$existing_port"
    printf 'PILOT_GCMS_INSTANCE_PATH\t%s\n' "$instance"
    printf 'PILOT_GCMS_INSTANCE_REUSED\t1\n'
    restore_finished=1
    exit 0
  fi
  printf '%s\n' "目标目录已存在且不属于本次迁移：$instance" >&2
  exit 5
fi
mkdir -p "$(dirname "$instance")" "$instance"
created_instance=1
chmod 700 "$(dirname "$instance")" "$instance"
tar -xzf "$archive" -C "$instance"
port=$requested_port
case "$port" in *[!0-9]*|'') printf '%s\n' '迁移实例端口无效' >&2; exit 2 ;; esac
[ "$port" -ge 1024 ] && [ "$port" -le 65535 ] || { printf '%s\n' '迁移实例端口超出范围' >&2; exit 2; }
port_busy() {
  if command -v ss >/dev/null 2>&1; then
    ss -H -ltn 2>/dev/null | awk -v p=":$1" '$4 ~ p"$" { found=1 } END { exit found ? 0 : 1 }'
  elif command -v netstat >/dev/null 2>&1; then
    netstat -ltn 2>/dev/null | awk -v p=":$1" '$4 ~ p"$" { found=1 } END { exit found ? 0 : 1 }'
  else
    return 1
  fi
}
while port_busy "$port"; do
  [ "$port" -lt 65535 ] || { printf '%s\n' '目标服务器没有可用的迁移端口' >&2; exit 2; }
  port=$((port + 1))
done
conf="$instance/shared/cms.conf"
[ -f "$conf" ] || { printf '%s\n' '迁移实例缺少 shared/cms.conf' >&2; exit 2; }
if grep -Eq '^[[:space:]]*ADDR=' "$conf"; then
  sed -i.bak -E "s|^[[:space:]]*ADDR=.*$|ADDR=127.0.0.1:$port|" "$conf"
else
  printf '\nADDR=127.0.0.1:%s\n' "$port" >> "$conf"
fi
rm -f "$conf.bak"
chmod 600 "$conf" 2>/dev/null || true
find "$instance/shared" -type f \( -name '*.db' -o -name '*.db-*' -o -name '*.sqlite' -o -name '*.sqlite-*' \) -exec chmod 600 {} \; 2>/dev/null || true
[ -x "$instance/scripts/cms.sh" ] || { printf '%s\n' '迁移实例缺少 scripts/cms.sh' >&2; exit 3; }
rm -f "$instance/run/cms.pid"
{
  printf 'INSTANCE_ID=%s\n' "$instance_id"
  printf 'SOURCE_ID=%s\n' "$source_id"
  printf 'PORT=%s\n' "$port"
  printf 'SERVICE_NAME=%s\n' "$service_name"
} > "$marker"
chmod 600 "$marker"
(cd "$instance"; unset ADDR BASE_URL CMS_DB GCMS_CADDY_ONDEMAND; ./scripts/cms.sh start) >/dev/null 2>&1
running=0
if [ -s "$instance/run/cms.pid" ]; then
  pid=$(cat "$instance/run/cms.pid" 2>/dev/null || true)
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null && running=1 || true
fi
[ "$running" = 1 ] || { printf '%s\n' '迁移实例启动后未检测到有效 PID' >&2; exit 4; }
printf 'PILOT_GCMS_INSTANCE_PORT\t%s\n' "$port"
printf 'PILOT_GCMS_INSTANCE_PATH\t%s\n' "$instance"
printf 'PILOT_GCMS_INSTANCE_REUSED\t0\n'
restore_finished=1
"#;

const GCMS_MIGRATION_SERVICE_CMD: &str = r#"
set -eu
instance=${PILOT_GCMS_INSTANCE:?}
service_name=${PILOT_GCMS_SERVICE_NAME:?}
run_user=${PILOT_GCMS_RUN_USER:?}
unit="/etc/systemd/system/${service_name}.service"
command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ] || { printf '%s\n' '目标服务器没有可用的 systemd' >&2; exit 2; }
case "$service_name" in *[!A-Za-z0-9_.@-]*|'') printf '%s\n' '迁移服务名称不合法' >&2; exit 2 ;; esac
case "$run_user" in *[!A-Za-z0-9_.@-]*|'') printf '%s\n' '迁移服务用户不合法' >&2; exit 2 ;; esac
id "$run_user" >/dev/null 2>&1 || { printf '%s\n' "迁移服务用户不存在：$run_user" >&2; exit 2; }
[ -x "$instance/scripts/cms.sh" ] && [ -f "$instance/.pilot-instance" ] || { printf '%s\n' '迁移实例目录不完整或缺少 Pilot 标记' >&2; exit 2; }
if [ -f "$unit" ] && ! grep -Fq '# Managed by GCMS Pilot migration.' "$unit"; then
  printf '%s\n' "已存在非 Pilot 管理的服务：$unit" >&2
  exit 3
fi
tmp="${unit}.pilot.$$"
{
  printf '# Managed by GCMS Pilot migration.\n'
  printf '[Unit]\nDescription=GCMS migrated instance %s\nAfter=network-online.target\nWants=network-online.target\n\n' "$service_name"
  printf '[Service]\nType=forking\nUser=%s\nWorkingDirectory=%s\nPIDFile=%s/run/cms.pid\n' "$run_user" "$instance" "$instance"
  printf 'ExecStart=%s/scripts/cms.sh start\nExecStop=%s/scripts/cms.sh stop\nExecReload=%s/scripts/cms.sh restart\n' "$instance" "$instance" "$instance"
  printf 'Restart=on-failure\nRestartSec=5\nTimeoutStartSec=120\nTimeoutStopSec=60\n\n[Install]\nWantedBy=multi-user.target\n'
} > "$tmp"
chmod 0644 "$tmp"
mv "$tmp" "$unit"
(cd "$instance"; unset ADDR BASE_URL CMS_DB GCMS_CADDY_ONDEMAND; ./scripts/cms.sh stop) >/dev/null 2>&1 || true
systemctl daemon-reload
systemctl enable --now "$service_name"
systemctl is-active --quiet "$service_name"
printf 'PILOT_GCMS_SERVICE_INSTALLED\t1\n'
"#;

#[derive(Clone, Serialize, Default, Debug, PartialEq)]
pub(super) struct GcmsCaddyPreflight {
    /// missing | standard | custom | conflict | unsupported
    mode: String,
    /// caddy | nginx
    provider: String,
    installed: bool,
    version: String,
    running: bool,
    can_auto_configure: bool,
    /// root | sudo | none
    privilege: String,
    config_path: String,
    site_path: String,
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
pub(super) struct GcmsCloudflareCandidate {
    connection_id: String,
    connection_name: String,
    connection_remark: String,
    key_prefix: String,
    account_id: String,
    zone_name: String,
    status: String,
    detail: String,
    /// 已能读取 Zone、DNS 和 Zone Settings。DNS Edit 最终仍由写入 API 再校验。
    permission_complete: bool,
    preferred: bool,
}

#[derive(Clone, Serialize, Default, Debug, PartialEq)]
pub(super) struct GcmsCloudflareCheck {
    /// matched | connection_required | connection_selection_required | zone_not_found |
    /// permission_error | api_error | record_missing | unsupported_record | origin_mismatch |
    /// zone_inactive | ssl_unreadable | ssl_incompatible
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
    candidates: Vec<GcmsCloudflareCandidate>,
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
    primary_matched: bool,
    redirect: Option<GcmsRedirectCheck>,
    matched: bool,
    caddy: Option<GcmsCaddyPreflight>,
}

#[derive(Clone, Serialize, Default, Debug, PartialEq)]
pub(super) struct GcmsRedirectCheck {
    domain: String,
    dns_ipv4: Vec<String>,
    dns_ipv6: Vec<String>,
    dns_error: String,
    hosting: GcmsDnsHosting,
    direct_dns_matched: bool,
    cloudflare: Option<GcmsCloudflareCheck>,
    matched: bool,
}

#[derive(Clone, Serialize)]
pub(super) struct GcmsAccessApplyResult {
    status: GcmsRemoteStatus,
    url: String,
    https_ok: bool,
    http_status: Option<u16>,
    verification_error: String,
    redirect_url: String,
    redirect_ok: bool,
    redirect_http_status: Option<u16>,
    redirect_verification_error: String,
    cloudflare_proxy_applicable: bool,
    cloudflare_proxied: bool,
    cloudflare_proxy_error: String,
}

#[derive(Clone, Serialize)]
pub(super) struct GcmsCloudflareCreateResult {
    created: bool,
    created_domains: Vec<String>,
    check: GcmsAccessCheck,
}

#[derive(Clone, Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub(super) enum GcmsInstallEvent {
    Phase {
        message: String,
    },
    Log {
        text: String,
    },
    Progress {
        current: u32,
        total: u32,
        source_index: u32,
        source_total: u32,
        message: String,
    },
}

fn send_gcms_migration_progress(
    channel: &Channel<GcmsInstallEvent>,
    current: u32,
    total: u32,
    source_index: u32,
    source_total: u32,
    message: impl Into<String>,
) {
    let message = message.into();
    let _ = channel.send(GcmsInstallEvent::Phase {
        message: message.clone(),
    });
    let _ = channel.send(GcmsInstallEvent::Progress {
        current,
        total,
        source_index,
        source_total,
        message,
    });
}

fn send_gcms_migration_log(channel: &Channel<GcmsInstallEvent>, text: impl Into<String>) {
    let _ = channel.send(GcmsInstallEvent::Log { text: text.into() });
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
    site_path: String,
    site_exists: bool,
    site_managed: bool,
    domain_files: Vec<String>,
    nginx_path: String,
    nginx_version: String,
    nginx_service_exists: bool,
    nginx_service_running: bool,
    nginx_process_running: bool,
    nginx_container: bool,
    nginx_config_path: String,
    nginx_config_valid: bool,
    nginx_conf_d_included: bool,
    nginx_certbot_available: bool,
    nginx_site_path: String,
    nginx_site_exists: bool,
    nginx_site_managed: bool,
    nginx_domain_files: Vec<String>,
}

fn parse_gcms_remote_status(raw: &str) -> Result<GcmsRemoteStatus, String> {
    let mut out = GcmsRemoteStatus {
        password_status: "unknown".into(),
        ..GcmsRemoteStatus::default()
    };
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
            "PILOT_GCMS_PORT" => out.port = value.trim().parse::<u16>().unwrap_or(0),
            "PILOT_GCMS_BASE_URL" => out.base_url = value.trim().to_string(),
            "PILOT_GCMS_REDIRECT_DOMAIN" => out.redirect_domain = value.trim().to_string(),
            "PILOT_GCMS_PASSWORD_STATUS" => {
                out.password_status = match value.trim() {
                    "default" | "changed" => value.trim().to_string(),
                    _ => "unknown".to_string(),
                }
            }
            "PILOT_GCMS_ADMIN_USER" => out.admin_user = value.trim().to_string(),
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

fn parse_migration_target_env(raw: &str) -> GcmsMigrationTargetEnv {
    let mut environment = GcmsMigrationTargetEnv::default();
    for line in raw.lines() {
        let Some((key, value)) = line.split_once('\t') else {
            continue;
        };
        match key.trim() {
            "PILOT_MIGRATION_PRIVILEGE" => environment.privilege = value.trim().to_string(),
            "PILOT_MIGRATION_SYSTEMD" => environment.systemd = value.trim() == "1",
            "PILOT_MIGRATION_ROOT" => environment.root = value.trim().to_string(),
            _ => {}
        }
    }
    environment
}

async fn migration_target_env(
    state: &AppState,
    conn_id: &str,
) -> Result<GcmsMigrationTargetEnv, String> {
    ensure_ssh(state, conn_id).await?;
    let out = state
        .ssh
        .exec(conn_id, GCMS_MIGRATION_TARGET_ENV_CMD, 20, false)
        .await?;
    if out.code != 0 {
        return Err(format!("检测目标服务器服务环境失败：{}", out.stderr.trim()));
    }
    let environment = parse_migration_target_env(&out.stdout);
    if environment.root.is_empty() {
        return Err("目标服务器未返回正式实例目录".into());
    }
    Ok(environment)
}

async fn migration_service_enabled(state: &AppState, conn_id: &str, service_name: &str) -> bool {
    let command = format!(
        "systemctl is-enabled --quiet {} 2>/dev/null && systemctl is-active --quiet {} 2>/dev/null",
        shell_quote(service_name),
        shell_quote(service_name)
    );
    state
        .ssh
        .exec(conn_id, &command, 20, false)
        .await
        .is_ok_and(|output| output.code == 0)
}

async fn install_migration_service(
    state: &AppState,
    target_id: &str,
    environment: &GcmsMigrationTargetEnv,
    instance_path: &str,
    service_name: &str,
    run_user: &str,
) -> Result<(), String> {
    let service_env = format!(
        "PILOT_GCMS_INSTANCE={} PILOT_GCMS_SERVICE_NAME={} PILOT_GCMS_RUN_USER={}",
        shell_quote(instance_path),
        shell_quote(service_name),
        shell_quote(run_user)
    );
    let service_body = shell_quote(GCMS_MIGRATION_SERVICE_CMD);
    let service_command = if environment.privilege == "root" {
        format!("env {service_env} sh -c {service_body}")
    } else if environment.privilege == "sudo" {
        format!("sudo -n env {service_env} sh -c {service_body}")
    } else {
        return Err("创建迁移实例服务需要 root 或免密 sudo".into());
    };
    let output = state
        .ssh
        .exec(target_id, &service_command, 180, false)
        .await
        .map_err(|error| format!("创建 systemd 服务失败：{error}"))?;
    if output.code != 0 {
        let detail = gcms_install_log(&output.stdout, &output.stderr);
        return Err(format!(
            "创建 systemd 服务失败：{}",
            detail
                .lines()
                .rev()
                .map(str::trim)
                .find(|line| !line.is_empty())
                .unwrap_or("未知错误")
        ));
    }
    Ok(())
}

async fn clear_remote_migration_access_marker(
    state: &AppState,
    target_id: &str,
    instance_path: &str,
) -> Result<(), String> {
    let marker = format!("{instance_path}/.pilot-instance");
    let command = format!(
        "marker={marker}; if [ -f \"$marker\" ]; then tmp=\"${{marker}}.pilot.$$\"; awk -F= '$1 != \"ACCESS_DOMAIN\" && $1 != \"ACCESS_REDIRECT_DOMAIN\" {{ print }}' \"$marker\" > \"$tmp\" && chmod 600 \"$tmp\" && mv \"$tmp\" \"$marker\"; fi",
        marker = shell_quote(&marker)
    );
    let output = state
        .ssh
        .exec(target_id, &command, 30, false)
        .await
        .map_err(|error| format!("清理迁移实例域名标记失败：{error}"))?;
    if output.code != 0 {
        return Err(format!(
            "清理迁移实例域名标记失败：{}",
            output.stderr.trim()
        ));
    }
    Ok(())
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

fn domain_from_base_url(raw: &str) -> Result<String, String> {
    let url = reqwest::Url::parse(raw.trim()).map_err(|_| "迁移实例的访问地址格式不正确")?;
    let domain = url
        .host_str()
        .ok_or_else(|| "迁移实例的访问地址缺少域名".to_string())?;
    normalize_public_domain(domain)
}

fn normalize_redirect_domain(
    raw: Option<&str>,
    primary_domain: &str,
) -> Result<Option<String>, String> {
    let Some(raw) = raw.map(str::trim).filter(|value| !value.is_empty()) else {
        return Ok(None);
    };
    let domain =
        normalize_public_domain(raw).map_err(|error| format!("跳转域名不正确：{error}"))?;
    if domain == primary_domain {
        return Err("跳转域名不能与主访问域名相同".into());
    }
    Ok(Some(domain))
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
            "PILOT_CADDY_SITE_PATH" => out.site_path = value.to_string(),
            "PILOT_CADDY_SITE_EXISTS" => out.site_exists = value == "1",
            "PILOT_CADDY_SITE_MANAGED" => out.site_managed = value == "1",
            "PILOT_CADDY_DOMAIN_FILE" if !value.is_empty() => {
                out.domain_files.push(value.to_string())
            }
            "PILOT_NGINX_PATH" => out.nginx_path = value.to_string(),
            "PILOT_NGINX_VERSION" => out.nginx_version = value.to_string(),
            "PILOT_NGINX_SERVICE_EXISTS" => out.nginx_service_exists = value == "1",
            "PILOT_NGINX_SERVICE_RUNNING" => out.nginx_service_running = value == "1",
            "PILOT_NGINX_PROCESS" => out.nginx_process_running = value == "1",
            "PILOT_NGINX_CONTAINER" => out.nginx_container = value == "1",
            "PILOT_NGINX_CONFIG" => out.nginx_config_path = value.to_string(),
            "PILOT_NGINX_CONFIG_VALID" => out.nginx_config_valid = value == "1",
            "PILOT_NGINX_CONF_D_INCLUDED" => out.nginx_conf_d_included = value == "1",
            "PILOT_NGINX_CERTBOT_AVAILABLE" => out.nginx_certbot_available = value == "1",
            "PILOT_NGINX_SITE_PATH" => out.nginx_site_path = value.to_string(),
            "PILOT_NGINX_SITE_EXISTS" => out.nginx_site_exists = value == "1",
            "PILOT_NGINX_SITE_MANAGED" => out.nginx_site_managed = value == "1",
            "PILOT_NGINX_DOMAIN_FILE" if !value.is_empty() => {
                out.nginx_domain_files.push(value.to_string())
            }
            _ => {}
        }
    }
    out.domain_files.sort();
    out.domain_files.dedup();
    out.nginx_domain_files.sort();
    out.nginx_domain_files.dedup();
    out
}

fn classify_caddy_probe(probe: RemoteCaddyProbe) -> GcmsCaddyPreflight {
    let nginx_owns_port = probe.port_80 == "nginx" || probe.port_443 == "nginx";
    if nginx_owns_port {
        let managed_site = probe.nginx_site_path.as_str();
        let domain_conflicts: Vec<String> = probe
            .nginx_domain_files
            .iter()
            .filter(|path| path.as_str() != managed_site || !probe.nginx_site_managed)
            .cloned()
            .collect();
        let port_conflict = |owner: &str| !owner.is_empty() && owner != "free" && owner != "nginx";
        let installed = !probe.nginx_path.is_empty();
        let running = probe.nginx_service_running || probe.nginx_process_running;
        let mut out = GcmsCaddyPreflight {
            provider: "nginx".into(),
            installed,
            version: probe.nginx_version.clone(),
            running,
            privilege: probe.privilege.clone(),
            config_path: probe.nginx_config_path.clone(),
            site_path: probe.nginx_site_path.clone(),
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
                "当前 SSH 用户既不是 root，也没有免密 sudo；无法安全修改 Nginx".to_string(),
            ))
        } else if port_conflict(&probe.port_80) || port_conflict(&probe.port_443) {
            Some((
                "conflict",
                format!(
                    "80/443 由不同服务混合占用（80：{}，443：{}），不会自动改动",
                    probe.port_80, probe.port_443
                ),
            ))
        } else if probe.nginx_container && !probe.nginx_service_exists {
            Some((
                "custom",
                "检测到容器中的 Nginx；挂载目录和启动参数未知，请在容器配置中手动接入 GCMS"
                    .to_string(),
            ))
        } else if !installed {
            Some((
                "custom",
                "端口由 Nginx 占用，但未在标准 PATH 找到 nginx 命令；不会猜测其安装位置"
                    .to_string(),
            ))
        } else if !probe.nginx_service_exists || !probe.nginx_service_running {
            Some((
                "custom",
                "Nginx 当前不是由正在运行的标准 systemd 服务管理；为避免影响现有站点，不会自动改变其启动方式".to_string(),
            ))
        } else if probe.nginx_config_path != "/etc/nginx/nginx.conf" {
            Some((
                "custom",
                format!(
                    "Nginx 使用自定义主配置 {}，不会自动修改",
                    if probe.nginx_config_path.is_empty() {
                        "（未识别）"
                    } else {
                        probe.nginx_config_path.as_str()
                    }
                ),
            ))
        } else if !probe.nginx_config_valid {
            Some((
                "conflict",
                "现有 Nginx 配置未通过 nginx -t；请先修复原配置".to_string(),
            ))
        } else if !probe.nginx_conf_d_included {
            Some((
                "custom",
                "现有 Nginx 未加载 /etc/nginx/conf.d/*.conf；Pilot 不会改写主配置".to_string(),
            ))
        } else if !probe.nginx_certbot_available && probe.package_manager.is_empty() {
            Some((
                "unsupported",
                "服务器尚未安装 Certbot，且未检测到 apt、dnf、yum 或 pacman，无法自动申请 HTTPS 证书".to_string(),
            ))
        } else if probe.nginx_site_exists && !probe.nginx_site_managed {
            Some((
                "conflict",
                format!(
                    "{} 已存在但不是 Pilot 托管文件，不会覆盖",
                    probe.nginx_site_path
                ),
            ))
        } else if !domain_conflicts.is_empty() {
            Some((
                "conflict",
                format!(
                    "该域名已出现在其他 Nginx 配置中：{}",
                    domain_conflicts.join("、")
                ),
            ))
        } else {
            None
        };
        if let Some((mode, detail)) = blocked {
            out.mode = mode.into();
            out.detail = detail;
            return out;
        }
        out.mode = "standard".into();
        out.can_auto_configure = true;
        out.detail = if probe.nginx_site_managed {
            "检测到标准 Nginx 与 Pilot 托管配置；将先备份，再安全更新并校验重载".into()
        } else {
            "检测到标准 Nginx；将保留现有站点，只新增一份独立 GCMS 配置".into()
        };
        return out;
    }

    let managed_site = if probe.site_path.is_empty() {
        "/etc/caddy/conf.d/gcms.caddy"
    } else {
        probe.site_path.as_str()
    };
    let domain_conflicts: Vec<String> = probe
        .domain_files
        .iter()
        .filter(|path| path.as_str() != managed_site || !probe.site_managed)
        .cloned()
        .collect();
    let port_conflict = |owner: &str| !owner.is_empty() && owner != "free" && owner != "caddy";
    let installed = !probe.path.is_empty();
    let mut out = GcmsCaddyPreflight {
        provider: "caddy".into(),
        installed,
        version: probe.version.clone(),
        running: probe.service_running || probe.process_running || probe.container,
        privilege: probe.privilege.clone(),
        config_path: probe.config_path.clone(),
        site_path: managed_site.to_string(),
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
            out.detail = "未安装 Caddy，且未检测到 apt、dnf、yum 或 pacman，无法自动安装".into();
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
    gcms_remote_status_at(state, conn_id, None).await
}

async fn gcms_remote_status_at(
    state: &AppState,
    conn_id: &str,
    instance_path: Option<&str>,
) -> Result<GcmsRemoteStatus, String> {
    ensure_ssh(state, conn_id).await?;
    let command = instance_path
        .filter(|path| !path.trim().is_empty())
        .map(|path| {
            format!(
                "env PILOT_GCMS_ROOT={} sh -c {}",
                shell_quote(path),
                shell_quote(GCMS_REMOTE_PROBE_CMD)
            )
        });
    let out = state
        .ssh
        .exec(
            conn_id,
            command.as_deref().unwrap_or(GCMS_REMOTE_PROBE_CMD),
            25,
            false,
        )
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

async fn gcms_remote_service_action(
    state: &AppState,
    conn_id: &str,
    action: &str,
) -> Result<GcmsRemoteStatus, String> {
    let conn = state.conns.get(conn_id)?;
    if conn.kind != "ssh" {
        return Err("这不是远程服务器连接".into());
    }
    let _guard = begin_gcms_operation(state, conn_id)?;
    let before = gcms_remote_status_at(state, conn_id, None).await?;
    if !before.installed {
        return Err("这台服务器尚未安装标准 GCMS".into());
    }
    if action == "stop" && !before.running {
        return Ok(before);
    }
    let script_action = if action == "restart" && !before.running {
        "start"
    } else {
        action
    };
    let action_label = match (action, before.running) {
        ("stop", _) => "关闭 GCMS 服务",
        ("restart", false) => "启动 GCMS 服务",
        _ => "重启 GCMS 服务",
    };

    let command = format!(
        "env PILOT_GCMS_HOME={} PILOT_GCMS_ACTION={} sh -c {}",
        shell_quote(&before.path),
        shell_quote(script_action),
        shell_quote(GCMS_REMOTE_SERVICE_ACTION_CMD),
    );
    let out = state.ssh.exec(conn_id, &command, 120, false).await?;

    // cms.sh 返回后进程和监听端口可能还在切换，短暂轮询后再判断结果。
    let expect_running = action == "restart";
    for attempt in 0..6 {
        if attempt > 0 {
            tokio::time::sleep(Duration::from_secs(1)).await;
        }
        let after = gcms_remote_status_at(state, conn_id, None).await?;
        // restart 返回非零时，服务即使仍在运行也不能证明重启成功；stop 则允许
        // 管理脚本返回非零但进程确实已经退出的幂等结果。
        if after.running == expect_running && (out.code == 0 || action == "stop") {
            return Ok(after);
        }
        if out.code != 0 && action == "restart" {
            break;
        }
    }

    let log = gcms_install_log(&out.stdout, &out.stderr);
    let detail = log
        .lines()
        .rev()
        .map(str::trim)
        .find(|line| !line.is_empty())
        .unwrap_or(if out.code == 0 {
            "服务状态未在等待时间内更新"
        } else {
            "远程管理脚本执行失败"
        });
    Err(format!(
        "{}失败{}：{}",
        action_label,
        if out.code == 0 {
            String::new()
        } else {
            format!("（退出码 {}）", out.code)
        },
        detail.chars().take(300).collect::<String>()
    ))
}

#[tauri::command]
pub(super) async fn gcms_remote_restart(
    state: tauri::State<'_, AppState>,
    conn_id: String,
) -> Result<GcmsRemoteStatus, String> {
    gcms_remote_service_action(&state, &conn_id, "restart").await
}

#[tauri::command]
pub(super) async fn gcms_remote_stop(
    state: tauri::State<'_, AppState>,
    conn_id: String,
) -> Result<GcmsRemoteStatus, String> {
    gcms_remote_service_action(&state, &conn_id, "stop").await
}

/// 多源独立实例迁移的安全预检。
///
/// 这里故意只读：每个源实例会在目标机保留为独立目录、端口和 systemd 服务，
/// 不覆盖目标现有 GCMS，也不把不同源实例的数据库混在一起。
#[tauri::command]
pub(super) async fn gcms_remote_migration_preflight(
    state: tauri::State<'_, AppState>,
    target_id: String,
    source_ids: Vec<String>,
) -> Result<GcmsMigrationPreflight, String> {
    if source_ids.is_empty() {
        return Err("至少选择一个源实例".into());
    }
    if source_ids.len() > 50 {
        return Err("一次最多预检 50 个源实例".into());
    }
    if source_ids.iter().collect::<HashSet<_>>().len() != source_ids.len() {
        return Err("源实例列表中存在重复项".into());
    }

    let target_conn = state.conns.get(&target_id)?;
    if target_conn.kind != "ssh" {
        return Err("目标必须是远程 SSH 服务器".into());
    }
    let target_status = gcms_remote_status_inner(&state, &target_id).await?;
    let target_environment = migration_target_env(&state, &target_id).await?;
    let service_ready = target_environment.systemd
        && matches!(target_environment.privilege.as_str(), "root" | "sudo");
    let target = GcmsMigrationServer {
        id: target_id.clone(),
        connection_id: target_id.clone(),
        name: target_conn.name.clone(),
        server_name: target_conn.name.clone(),
        instance_kind: "target".into(),
        role: "target".into(),
        installed: target_status.installed,
        version: target_status.version.clone(),
        path: target_status.path.clone(),
        running: target_status.running,
        port: target_status.port,
        base_url: target_status.base_url.clone(),
        redirect_domain: target_status.redirect_domain.clone(),
        service_ready,
        service_detail: if !target_environment.systemd {
            "目标服务器没有可用的 systemd，无法保证实例开机自启".into()
        } else if !matches!(target_environment.privilege.as_str(), "root" | "sudo") {
            "当前 SSH 用户没有 root 或免密 sudo，无法创建实例服务".into()
        } else {
            format!("正式目录：{}", target_environment.root)
        },
    };

    let resolved_sources = resolve_migration_sources(&state, &source_ids).await?;
    let mut sources = Vec::with_capacity(resolved_sources.len());
    let mut issues = Vec::new();
    let mut domains: std::collections::HashMap<String, String> = std::collections::HashMap::new();
    let mut domain_conflicts = Vec::new();
    if !target.service_ready {
        issues.push(target.service_detail.clone());
    }
    for source in resolved_sources {
        if source.connection_id == target_id {
            issues.push(format!(
                "源实例「{}」已位于目标服务器，不能迁移到自身",
                source.name
            ));
        }
        let status = source.status;
        if !status.installed {
            issues.push(format!("源实例「{}」未检测到完整 GCMS 目录", source.name));
        }
        if status.version.is_empty() {
            issues.push(format!(
                "源实例「{}」未返回 GCMS 版本，无法确认迁移兼容性",
                source.name
            ));
        }
        if !status.base_url.trim().is_empty() {
            let domain = status.base_url.trim().to_ascii_lowercase();
            if let Some(previous) = domains.insert(domain.clone(), source.name.clone()) {
                domain_conflicts.push(format!("{} · {}、{}", domain, previous, source.name));
                issues.push(format!(
                    "源实例存在重复访问域名：{}（{}、{}）",
                    domain, previous, source.name
                ));
            }
        }
        sources.push(GcmsMigrationServer {
            id: source.selection_id,
            connection_id: source.connection_id,
            name: source.name,
            server_name: source.server_name,
            instance_kind: source.instance_kind,
            role: "source".into(),
            installed: status.installed,
            version: status.version,
            path: status.path,
            running: status.running,
            port: status.port,
            base_url: status.base_url,
            redirect_domain: status.redirect_domain,
            service_ready: false,
            service_detail: String::new(),
        });
    }

    for source in &sources {
        if !source.base_url.trim().is_empty() && source.base_url == target.base_url {
            issues.push(format!(
                "目标服务器已使用源实例的访问地址：{}",
                source.base_url
            ));
        }
    }
    if sources.iter().any(|source| !source.installed) {
        issues.push("存在目录不完整的源实例，不能开始迁移".into());
    }

    Ok(GcmsMigrationPreflight {
        target,
        sources,
        domain_conflicts,
        can_start: issues.is_empty(),
        issues,
    })
}

/// 把每个源实例的完整 GCMS 安装目录恢复为目标服务器上的正式独立实例。
///
/// 每个源→目标组合使用稳定实例 id、独立目录/端口/systemd 服务；成功一个就立即
/// 持久化一个。重试会复用已登记或带远程标记的实例，不覆盖目标原有 GCMS。
#[tauri::command]
pub(super) async fn gcms_remote_migration_stage(
    state: tauri::State<'_, AppState>,
    target_id: String,
    source_ids: Vec<String>,
    on_event: Channel<GcmsInstallEvent>,
) -> Result<GcmsMigrationStageResult, String> {
    if source_ids.is_empty() {
        return Err("至少选择一个源实例".into());
    }
    if source_ids.len() > 50 {
        return Err("一次最多迁移 50 个源实例".into());
    }
    if source_ids.iter().collect::<HashSet<_>>().len() != source_ids.len() {
        return Err("源实例列表中存在重复项".into());
    }
    let target = state.conns.get(&target_id)?;
    if target.kind != "ssh" {
        return Err("目标必须是远程 SSH 服务器".into());
    }
    let source_specs = resolve_migration_sources(&state, &source_ids).await?;
    if let Some(source) = source_specs
        .iter()
        .find(|source| source.connection_id == target_id)
    {
        return Err(format!(
            "源实例「{}」已经位于目标服务器，不能迁移到自身",
            source.name
        ));
    }
    let _target_status = gcms_remote_status_inner(&state, &target_id).await?;
    let target_environment = migration_target_env(&state, &target_id).await?;
    if !target_environment.systemd {
        return Err("目标服务器没有可用的 systemd，无法创建可开机自启的迁移实例".into());
    }
    if !matches!(target_environment.privilege.as_str(), "root" | "sudo") {
        return Err("当前 SSH 用户没有 root 或免密 sudo，无法创建迁移实例服务".into());
    }
    ensure_ssh(&state, &target_id).await?;
    let _target_guard = begin_gcms_operation(&state, &target_id)?;
    let run_id = uuid::Uuid::new_v4().to_string();
    let staging_root = format!("{}/.staging/{run_id}", target_environment.root);
    let local_cache_root = state.data_dir.join("migration-cache").join(&run_id);
    fs::create_dir_all(&local_cache_root)
        .map_err(|error| format!("创建本地迁移缓存失败：{error}"))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = fs::set_permissions(&local_cache_root, fs::Permissions::from_mode(0o700));
    }
    let _cache_guard = MigrationCacheGuard(local_cache_root.clone());
    let init = format!(
        "umask 077; mkdir -p {root}; chmod 700 {root}; find {parent} -mindepth 1 -maxdepth 1 -type d -mtime +1 -exec rm -rf -- {{}} + 2>/dev/null || true",
        root = shell_quote(&staging_root),
        parent = shell_quote(&format!("{}/.staging", target_environment.root)),
    );
    let init_out = state.ssh.exec(&target_id, &init, 30, false).await?;
    if init_out.code != 0 {
        return Err(format!("创建目标迁移目录失败：{}", init_out.stderr.trim()));
    }

    const STEPS_PER_SOURCE: u32 = 6;
    let source_total = source_specs.len() as u32;
    let total_steps = source_total.saturating_mul(STEPS_PER_SOURCE);
    send_gcms_migration_progress(
        &on_event,
        0,
        total_steps,
        0,
        source_total,
        "正在准备迁移任务…",
    );
    send_gcms_migration_log(
        &on_event,
        format!(
            "目标服务器：{}；实例根目录：{}；源实例：{} 个",
            target.name, target_environment.root, source_total
        ),
    );
    let mut snapshots = Vec::with_capacity(source_specs.len());
    let mut failures = Vec::new();
    for (index, source_spec) in source_specs.iter().enumerate() {
        let source_id = &source_spec.connection_id;
        let source_identity = &source_spec.identity;
        let source_index = index as u32 + 1;
        let source_start = index as u32 * STEPS_PER_SOURCE;
        let source_done = source_start + STEPS_PER_SOURCE;
        send_gcms_migration_progress(
            &on_event,
            source_start,
            total_steps,
            source_index,
            source_total,
            format!("正在读取第 {source_index}/{source_total} 个源实例…"),
        );
        let source = match state.conns.get(source_id) {
            Ok(source) => source,
            Err(error) => {
                let failure = format!("源实例所在服务器连接不存在：{error}");
                send_gcms_migration_log(&on_event, format!("[失败] {failure}"));
                send_gcms_migration_progress(
                    &on_event,
                    source_done,
                    total_steps,
                    source_index,
                    source_total,
                    "这个源实例迁移失败，继续处理下一个",
                );
                failures.push(failure);
                continue;
            }
        };
        send_gcms_migration_log(
            &on_event,
            format!(
                "[{source_index}/{source_total}] 开始处理「{}」({}@{}，{} :{})",
                source_spec.name,
                source.ssh_user,
                source.ssh_host,
                if source_spec.instance_kind == "main" {
                    "主实例"
                } else {
                    "迁移实例"
                },
                source_spec.status.port
            ),
        );
        if source.kind != "ssh" {
            let failure = format!("「{}」不在远程 SSH 服务器上", source_spec.name);
            send_gcms_migration_log(&on_event, format!("[失败] {failure}"));
            send_gcms_migration_progress(
                &on_event,
                source_done,
                total_steps,
                source_index,
                source_total,
                format!("「{}」迁移失败，继续处理下一个", source_spec.name),
            );
            failures.push(failure);
            continue;
        }
        let status = source_spec.status.clone();
        if !status.installed || status.path.is_empty() {
            let failure = format!("「{}」未检测到可迁移的完整 GCMS 目录", source_spec.name);
            send_gcms_migration_log(&on_event, format!("[失败] {failure}"));
            send_gcms_migration_progress(
                &on_event,
                source_done,
                total_steps,
                source_index,
                source_total,
                format!("「{}」迁移失败，继续处理下一个", source_spec.name),
            );
            failures.push(failure);
            continue;
        }
        let instance_id = migration_instance_id(&target_id, source_identity);
        let instance_path = format!("{}/{}", target_environment.root, instance_id);
        let service_name = format!("pilot-{instance_id}");

        if let Some(mut existing) = read_migration_registry(&state.data_dir)
            .into_iter()
            .find(|instance| instance.id == instance_id)
        {
            if existing.service_name.is_empty() {
                existing.service_name = service_name.clone();
            }
            if existing.source_base_url.is_empty() {
                existing.source_base_url = status.base_url.clone();
                existing.source_redirect_domain = status.redirect_domain.clone();
            }
            if let Ok(remote) =
                gcms_remote_status_at(&state, &target_id, Some(&existing.instance_path)).await
            {
                if remote.installed {
                    if !migration_service_enabled(&state, &target_id, &existing.service_name).await
                    {
                        send_gcms_migration_progress(
                            &on_event,
                            source_start + 4,
                            total_steps,
                            source_index,
                            source_total,
                            format!("正在修复「{}」的开机自启服务…", source_spec.name),
                        );
                        if let Err(error) = install_migration_service(
                            &state,
                            &target_id,
                            &target_environment,
                            &existing.instance_path,
                            &existing.service_name,
                            &target.ssh_user,
                        )
                        .await
                        {
                            existing.running = remote.running;
                            existing.service_installed = false;
                            existing.last_error = error.clone();
                            existing.updated_at = migration_now();
                            if let Ok(stored) = upsert_migration_instance(&state.data_dir, existing)
                            {
                                snapshots.push(stored);
                            }
                            let failure = format!("「{}」{error}", source_spec.name);
                            send_gcms_migration_log(&on_event, format!("[失败] {failure}"));
                            send_gcms_migration_progress(
                                &on_event,
                                source_done,
                                total_steps,
                                source_index,
                                source_total,
                                format!("「{}」服务修复失败", source_spec.name),
                            );
                            failures.push(failure);
                            continue;
                        }
                    }
                    existing.running = remote.running;
                    existing.service_installed = true;
                    existing.version = if remote.version.is_empty() {
                        existing.version
                    } else {
                        remote.version
                    };
                    if existing.access_configured {
                        existing.base_url = remote.base_url;
                        existing.redirect_domain = remote.redirect_domain;
                    }
                    existing.updated_at = migration_now();
                    existing.last_error.clear();
                    let existing = upsert_migration_instance(&state.data_dir, existing)?;
                    send_gcms_migration_log(
                        &on_event,
                        format!("[完成] 「{}」已存在，跳过重复复制", source_spec.name),
                    );
                    send_gcms_migration_progress(
                        &on_event,
                        source_done,
                        total_steps,
                        source_index,
                        source_total,
                        format!("「{}」已存在，已完成状态复核", source_spec.name),
                    );
                    snapshots.push(existing);
                    continue;
                }
            }
        }
        if let Err(error) = ensure_ssh(&state, source_id).await {
            let failure = format!("连接源实例「{}」所在服务器失败：{error}", source_spec.name);
            send_gcms_migration_log(&on_event, format!("[失败] {failure}"));
            send_gcms_migration_progress(
                &on_event,
                source_done,
                total_steps,
                source_index,
                source_total,
                format!("「{}」迁移失败，继续处理下一个", source_spec.name),
            );
            failures.push(failure);
            continue;
        }
        let _source_guard = match begin_gcms_operation(&state, source_id) {
            Ok(guard) => guard,
            Err(error) => {
                let failure = format!("源实例「{}」所在服务器正忙：{error}", source_spec.name);
                send_gcms_migration_log(&on_event, format!("[失败] {failure}"));
                send_gcms_migration_progress(
                    &on_event,
                    source_done,
                    total_steps,
                    source_index,
                    source_total,
                    format!("「{}」迁移失败，继续处理下一个", source_spec.name),
                );
                failures.push(failure);
                continue;
            }
        };
        send_gcms_migration_progress(
            &on_event,
            source_start,
            total_steps,
            source_index,
            source_total,
            format!("正在创建「{}」实例快照…", source_spec.name),
        );

        let remote_archive = format!("/tmp/pilot-gcms-{run_id}-{index}.tar.gz");
        let env = format!(
            "PILOT_GCMS_ROOT={} PILOT_GCMS_ARCHIVE={} PILOT_GCMS_EXPECT_RUNNING={}",
            shell_quote(&status.path),
            shell_quote(&remote_archive),
            u8::from(status.running)
        );
        let command = format!("env {env} sh -c {}", shell_quote(GCMS_MIGRATION_STAGE_CMD));
        let out = match state.ssh.exec(source_id, &command, 1800, false).await {
            Ok(output) => output,
            Err(error) => {
                let _ = state
                    .ssh
                    .exec(
                        source_id,
                        &format!("rm -f {}", shell_quote(&remote_archive)),
                        30,
                        false,
                    )
                    .await;
                let failure = format!("源实例「{}」快照失败：{error}", source_spec.name);
                send_gcms_migration_log(&on_event, format!("[失败] {failure}"));
                send_gcms_migration_progress(
                    &on_event,
                    source_done,
                    total_steps,
                    source_index,
                    source_total,
                    format!("「{}」迁移失败，继续处理下一个", source_spec.name),
                );
                failures.push(failure);
                continue;
            }
        };
        if out.code != 0 {
            let _ = state
                .ssh
                .exec(
                    source_id,
                    &format!("rm -f {}", shell_quote(&remote_archive)),
                    30,
                    false,
                )
                .await;
            let failure = format!(
                "源实例「{}」快照失败：{}",
                source_spec.name,
                out.stderr.trim().chars().take(500).collect::<String>()
            );
            send_gcms_migration_log(&on_event, format!("[失败] {failure}"));
            send_gcms_migration_progress(
                &on_event,
                source_done,
                total_steps,
                source_index,
                source_total,
                format!("「{}」迁移失败，继续处理下一个", source_spec.name),
            );
            failures.push(failure);
            continue;
        }
        let bytes = out
            .stdout
            .lines()
            .find_map(|line| line.strip_prefix("PILOT_GCMS_SNAPSHOT_BYTES\t"))
            .and_then(|value| value.trim().parse::<u64>().ok())
            .unwrap_or(0);
        send_gcms_migration_log(
            &on_event,
            format!(
                "[快照] 「{}」已创建（{:.1} MB）",
                source_spec.name,
                bytes as f64 / 1_048_576.0
            ),
        );
        send_gcms_migration_progress(
            &on_event,
            source_start + 1,
            total_steps,
            source_index,
            source_total,
            format!("快照已创建，正在下载「{}」…", source_spec.name),
        );
        let local_archive = local_cache_root.join(format!("{index}.tar.gz"));
        let download = state
            .ssh
            .download(source_id, &remote_archive, &local_archive.to_string_lossy())
            .await;
        let _ = state
            .ssh
            .exec(
                source_id,
                &format!("rm -f {}", shell_quote(&remote_archive)),
                30,
                false,
            )
            .await;
        if let Err(error) = download {
            let _ = fs::remove_file(&local_archive);
            let failure = format!("下载「{}」快照失败：{error}", source_spec.name);
            send_gcms_migration_log(&on_event, format!("[失败] {failure}"));
            send_gcms_migration_progress(
                &on_event,
                source_done,
                total_steps,
                source_index,
                source_total,
                format!("「{}」迁移失败，继续处理下一个", source_spec.name),
            );
            failures.push(failure);
            continue;
        }
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = fs::set_permissions(&local_archive, fs::Permissions::from_mode(0o600));
        }
        let target_archive = format!("{staging_root}/{instance_id}.tar.gz");
        send_gcms_migration_log(
            &on_event,
            format!("[下载] 「{}」快照已落到本机临时缓存", source_spec.name),
        );
        send_gcms_migration_progress(
            &on_event,
            source_start + 2,
            total_steps,
            source_index,
            source_total,
            format!("正在上传「{}」到目标服务器…", source_spec.name),
        );
        let upload = state
            .ssh
            .upload(
                &target_id,
                &local_archive.to_string_lossy(),
                &target_archive,
            )
            .await;
        let _ = fs::remove_file(&local_archive);
        if let Err(error) = upload {
            let _ = state
                .ssh
                .exec(
                    &target_id,
                    &format!("rm -f {}", shell_quote(&target_archive)),
                    30,
                    false,
                )
                .await;
            let failure = format!("上传「{}」到目标服务器失败：{error}", source_spec.name);
            send_gcms_migration_log(&on_event, format!("[失败] {failure}"));
            send_gcms_migration_progress(
                &on_event,
                source_done,
                total_steps,
                source_index,
                source_total,
                format!("「{}」迁移失败，继续处理下一个", source_spec.name),
            );
            failures.push(failure);
            continue;
        }
        let _ = state
            .ssh
            .exec(
                &target_id,
                &format!("chmod 600 {}", shell_quote(&target_archive)),
                30,
                false,
            )
            .await;
        send_gcms_migration_log(
            &on_event,
            format!("[上传] 「{}」快照已送达目标服务器", source_spec.name),
        );
        send_gcms_migration_progress(
            &on_event,
            source_start + 3,
            total_steps,
            source_index,
            source_total,
            format!("正在恢复「{}」为独立实例…", source_spec.name),
        );
        let requested_port = 18080u16.saturating_add(index as u16);
        let restore_env = format!(
            "PILOT_GCMS_ARCHIVE={} PILOT_GCMS_INSTANCE={} PILOT_GCMS_PORT={} PILOT_GCMS_INSTANCE_ID={} PILOT_GCMS_SOURCE_ID={} PILOT_GCMS_SERVICE_NAME={}",
            shell_quote(&target_archive),
            shell_quote(&instance_path),
            requested_port,
            shell_quote(&instance_id),
            shell_quote(source_identity),
            shell_quote(&service_name)
        );
        let restore = format!(
            "env {restore_env} sh -c {}",
            shell_quote(GCMS_MIGRATION_RESTORE_CMD)
        );
        let extract_out = match state.ssh.exec(&target_id, &restore, 1800, false).await {
            Ok(output) => output,
            Err(error) => {
                let _ = state
                    .ssh
                    .exec(
                        &target_id,
                        &format!("rm -f {}", shell_quote(&target_archive)),
                        30,
                        false,
                    )
                    .await;
                let failure = format!(
                    "目标服务器创建「{}」独立实例失败：{error}",
                    source_spec.name
                );
                send_gcms_migration_log(&on_event, format!("[失败] {failure}"));
                send_gcms_migration_progress(
                    &on_event,
                    source_done,
                    total_steps,
                    source_index,
                    source_total,
                    format!("「{}」迁移失败，继续处理下一个", source_spec.name),
                );
                failures.push(failure);
                continue;
            }
        };
        let _ = state
            .ssh
            .exec(
                &target_id,
                &format!("rm -f {}", shell_quote(&target_archive)),
                30,
                false,
            )
            .await;
        if extract_out.code != 0 {
            let failure = format!(
                "目标服务器创建「{}」独立实例失败：{}",
                source_spec.name,
                extract_out.stderr.trim()
            );
            send_gcms_migration_log(&on_event, format!("[失败] {failure}"));
            send_gcms_migration_progress(
                &on_event,
                source_done,
                total_steps,
                source_index,
                source_total,
                format!("「{}」迁移失败，继续处理下一个", source_spec.name),
            );
            failures.push(failure);
            continue;
        }
        let instance_port = extract_out
            .stdout
            .lines()
            .find_map(|line| line.strip_prefix("PILOT_GCMS_INSTANCE_PORT\t"))
            .and_then(|value| value.trim().parse::<u16>().ok())
            .unwrap_or(requested_port);
        send_gcms_migration_log(
            &on_event,
            format!(
                "[恢复] 「{}」已写入 {}，监听端口 {}",
                source_spec.name, instance_path, instance_port
            ),
        );
        send_gcms_migration_progress(
            &on_event,
            source_start + 4,
            total_steps,
            source_index,
            source_total,
            format!("正在为「{}」创建开机自启服务…", source_spec.name),
        );
        let service_result = install_migration_service(
            &state,
            &target_id,
            &target_environment,
            &instance_path,
            &service_name,
            &target.ssh_user,
        )
        .await;
        let now = migration_now();
        let mut instance = GcmsMigrationSnapshot {
            id: instance_id,
            target_id: target_id.clone(),
            source_id: source_identity.clone(),
            source_name: source_spec.name.clone(),
            version: status.version,
            bytes,
            instance_path: instance_path.clone(),
            port: instance_port,
            source_base_url: status.base_url,
            source_redirect_domain: status.redirect_domain,
            base_url: String::new(),
            redirect_domain: String::new(),
            access_configured: false,
            https_ok: false,
            cloudflare_proxy_applicable: false,
            cloudflare_proxied: false,
            cloudflare_proxy_error: String::new(),
            service_name,
            service_installed: service_result.is_ok(),
            running: false,
            created_at: now,
            updated_at: now,
            last_error: String::new(),
        };
        if let Err(error) = service_result {
            instance.last_error = format!("实例已恢复，但{error}");
            if let Ok(stored) = upsert_migration_instance(&state.data_dir, instance.clone()) {
                snapshots.push(stored);
            }
            let failure = format!("「{}」{}", source_spec.name, instance.last_error);
            send_gcms_migration_log(&on_event, format!("[失败] {failure}"));
            send_gcms_migration_progress(
                &on_event,
                source_done,
                total_steps,
                source_index,
                source_total,
                format!("「{}」实例已恢复，但服务创建失败", source_spec.name),
            );
            failures.push(failure);
            continue;
        }
        send_gcms_migration_log(
            &on_event,
            format!(
                "[服务] 「{}」已创建并启用 {}",
                source_spec.name, instance.service_name
            ),
        );
        send_gcms_migration_progress(
            &on_event,
            source_start + 5,
            total_steps,
            source_index,
            source_total,
            format!("正在检查「{}」实例运行状态…", source_spec.name),
        );
        match gcms_remote_status_at(&state, &target_id, Some(&instance_path)).await {
            Ok(remote) => {
                instance.running = remote.running;
                if !remote.version.is_empty() {
                    instance.version = remote.version;
                }
            }
            Err(error) => instance.last_error = error,
        }
        if !instance.running && instance.last_error.is_empty() {
            instance.last_error = "systemd 服务已创建，但实例尚未进入运行状态".into();
        }
        if !instance.last_error.is_empty() {
            let failure = format!("「{}」{}", source_spec.name, instance.last_error);
            send_gcms_migration_log(&on_event, format!("[警告] {failure}"));
            failures.push(failure);
        }
        let instance = upsert_migration_instance(&state.data_dir, instance)?;
        send_gcms_migration_log(
            &on_event,
            format!(
                "[完成] 「{}」已迁移到端口 {}{}",
                source_spec.name,
                instance.port,
                if instance.running {
                    "，服务运行中"
                } else {
                    "，等待服务就绪"
                }
            ),
        );
        send_gcms_migration_progress(
            &on_event,
            source_done,
            total_steps,
            source_index,
            source_total,
            format!("「{}」迁移完成", source_spec.name),
        );
        snapshots.push(instance);
    }
    let _ = state
        .ssh
        .exec(
            &target_id,
            &format!("rmdir {} 2>/dev/null || true", shell_quote(&staging_root)),
            30,
            false,
        )
        .await;
    let summary = if failures.is_empty() {
        format!("迁移完成：{} 个独立实例已创建", snapshots.len())
    } else {
        format!(
            "迁移结束：{} 个实例已保存，{} 项需要处理",
            snapshots.len(),
            failures.len()
        )
    };
    send_gcms_migration_progress(
        &on_event,
        total_steps,
        total_steps,
        source_total,
        source_total,
        summary.clone(),
    );
    send_gcms_migration_log(&on_event, summary);
    Ok(GcmsMigrationStageResult {
        target_id,
        snapshots,
        failures,
        backup_path: String::new(),
    })
}

#[tauri::command]
pub(super) async fn gcms_remote_migration_instances(
    state: tauri::State<'_, AppState>,
) -> Result<Vec<GcmsMigrationSnapshot>, String> {
    if state
        .gcms_installing
        .lock()
        .map(|active| active.is_empty())
        .unwrap_or(false)
    {
        clear_migration_cache(&state.data_dir);
    }
    let mut instances = read_migration_registry(&state.data_dir);
    for instance in &mut instances {
        match state.conns.get(&instance.target_id) {
            Ok(connection) if connection.kind == "ssh" => {
                match gcms_remote_status_at(
                    &state,
                    &instance.target_id,
                    Some(&instance.instance_path),
                )
                .await
                {
                    Ok(status) => {
                        instance.running = status.running;
                        if !status.version.is_empty() {
                            instance.version = status.version;
                        }
                        if instance.source_base_url.is_empty() && !instance.access_configured {
                            instance.source_base_url = status.base_url.clone();
                            instance.source_redirect_domain = status.redirect_domain.clone();
                        }
                        if instance.access_configured {
                            instance.base_url = status.base_url;
                            instance.redirect_domain = status.redirect_domain;
                        } else {
                            instance.base_url.clear();
                            instance.redirect_domain.clear();
                        }
                    }
                    Err(error) => {
                        instance.running = false;
                        instance.last_error = error;
                    }
                }
                let service_probe = format!(
                    "systemctl is-enabled --quiet {service} 2>/dev/null && printf 'PILOT_SERVICE_ENABLED\\t1\\n' || true; awk -F= '$1 == \"ACCESS_DOMAIN\" {{ printf \"PILOT_ACCESS_DOMAIN\\t%s\\n\", $2 }} $1 == \"ACCESS_REDIRECT_DOMAIN\" {{ printf \"PILOT_ACCESS_REDIRECT\\t%s\\n\", $2 }}' {marker} 2>/dev/null || true",
                    service = shell_quote(&instance.service_name),
                    marker = shell_quote(&format!("{}/.pilot-instance", instance.instance_path))
                );
                if let Ok(out) = state
                    .ssh
                    .exec(&instance.target_id, &service_probe, 20, false)
                    .await
                {
                    instance.service_installed = out
                        .stdout
                        .lines()
                        .any(|line| line == "PILOT_SERVICE_ENABLED\t1");
                    let marker_domain = out.stdout.lines().find_map(|line| {
                        line.strip_prefix("PILOT_ACCESS_DOMAIN\t")
                            .map(str::trim)
                            .filter(|value| !value.is_empty())
                    });
                    if let Some(domain) = marker_domain {
                        instance.access_configured = true;
                        instance.base_url = format!("https://{domain}");
                        instance.redirect_domain = out
                            .stdout
                            .lines()
                            .find_map(|line| {
                                line.strip_prefix("PILOT_ACCESS_REDIRECT\t").map(str::trim)
                            })
                            .unwrap_or_default()
                            .to_string();
                    }
                }
            }
            _ => {
                instance.running = false;
                instance.last_error = "对应的目标 SSH 连接已不存在".into();
            }
        }
        instance.updated_at = migration_now();
    }
    instances.sort_by(|left, right| right.created_at.cmp(&left.created_at));
    save_migration_registry(&state.data_dir, &instances)?;
    Ok(instances)
}

#[tauri::command]
pub(super) async fn gcms_remote_migration_refresh_access(
    state: tauri::State<'_, AppState>,
    instance_id: String,
) -> Result<GcmsMigrationSnapshot, String> {
    let instance = read_migration_registry(&state.data_dir)
        .into_iter()
        .find(|item| item.id == instance_id)
        .ok_or("迁移实例记录不存在")?;
    if !instance.access_configured {
        return Ok(instance);
    }
    let target = state.conns.get(&instance.target_id)?;
    if target.kind != "ssh" {
        return Err("目标连接不是 SSH 服务器".into());
    }
    let domain = domain_from_base_url(&instance.base_url)?;
    let redirect_domain =
        normalize_redirect_domain(Some(instance.redirect_domain.as_str()), &domain)?;
    let _guard = begin_gcms_operation(&state, &instance.target_id)?;
    let redirect_verify = async {
        if let Some(redirect_domain) = redirect_domain.as_deref() {
            verify_gcms_redirect(&domain, redirect_domain).await
        } else {
            (true, None, String::new())
        }
    };
    let (check, (primary_ok, _, primary_error), (redirect_ok, _, redirect_error)) = tokio::join!(
        gcms_remote_access_check_inner(
            &state,
            &instance.target_id,
            &domain,
            redirect_domain.as_deref(),
            Some(&instance.instance_path),
            Some(instance.port),
        ),
        verify_gcms_https(&domain),
        redirect_verify,
    );
    let check = check?;
    let https_ok = primary_ok && redirect_ok;
    let verification_error = if !primary_ok {
        primary_error
    } else if !redirect_ok {
        redirect_error
    } else {
        String::new()
    };
    let (cloudflare_proxy_applicable, cloudflare_proxied, cloudflare_proxy_error) =
        gcms_cloudflare_proxy_health(&check);
    let status =
        gcms_remote_status_at(&state, &instance.target_id, Some(&instance.instance_path)).await?;
    update_migration_instance_status(
        &state.data_dir,
        &instance.id,
        &status,
        https_ok,
        cloudflare_proxy_applicable,
        cloudflare_proxied,
        &cloudflare_proxy_error,
        (!https_ok).then_some(verification_error.as_str()),
    )?;
    read_migration_registry(&state.data_dir)
        .into_iter()
        .find(|item| item.id == instance.id)
        .ok_or_else(|| "迁移实例状态保存后无法重新读取".into())
}

#[tauri::command]
pub(super) async fn gcms_remote_migration_restart(
    state: tauri::State<'_, AppState>,
    instance_id: String,
) -> Result<GcmsMigrationSnapshot, String> {
    let mut instance = read_migration_registry(&state.data_dir)
        .into_iter()
        .find(|item| item.id == instance_id)
        .ok_or("迁移实例记录不存在")?;
    let target = state.conns.get(&instance.target_id)?;
    if target.kind != "ssh" {
        return Err("目标连接不是 SSH 服务器".into());
    }
    let _guard = begin_gcms_operation(&state, &instance.target_id)?;
    let environment = migration_target_env(&state, &instance.target_id).await?;
    if !matches!(environment.privilege.as_str(), "root" | "sudo") {
        return Err("重启迁移实例需要 root 或免密 sudo".into());
    }
    let restart = format!(
        "systemctl restart {} && systemctl is-active --quiet {}",
        shell_quote(&instance.service_name),
        shell_quote(&instance.service_name)
    );
    let command = if environment.privilege == "root" {
        restart
    } else {
        format!("sudo -n sh -c {}", shell_quote(&restart))
    };
    let out = state
        .ssh
        .exec(&instance.target_id, &command, 120, false)
        .await?;
    if out.code != 0 {
        instance.running = false;
        instance.last_error = format!("重启失败：{}", out.stderr.trim());
        instance.updated_at = migration_now();
        let _ = upsert_migration_instance(&state.data_dir, instance.clone());
        return Err(instance.last_error);
    }
    let status =
        gcms_remote_status_at(&state, &instance.target_id, Some(&instance.instance_path)).await?;
    instance.running = status.running;
    instance.service_installed = true;
    if instance.access_configured {
        instance.base_url = status.base_url;
        instance.redirect_domain = status.redirect_domain;
    }
    instance.last_error.clear();
    instance.updated_at = migration_now();
    upsert_migration_instance(&state.data_dir, instance)
}

#[tauri::command]
pub(super) async fn gcms_remote_migration_stop(
    state: tauri::State<'_, AppState>,
    instance_id: String,
) -> Result<GcmsMigrationSnapshot, String> {
    let mut instance = read_migration_registry(&state.data_dir)
        .into_iter()
        .find(|item| item.id == instance_id)
        .ok_or("迁移实例记录不存在")?;
    let target = state.conns.get(&instance.target_id)?;
    if target.kind != "ssh" {
        return Err("目标连接不是 SSH 服务器".into());
    }
    let _guard = begin_gcms_operation(&state, &instance.target_id)?;
    let environment = migration_target_env(&state, &instance.target_id).await?;
    if !matches!(environment.privilege.as_str(), "root" | "sudo") {
        return Err("停止迁移实例需要 root 或免密 sudo".into());
    }
    let stop = format!(
        "systemctl stop {} && ! systemctl is-active --quiet {}",
        shell_quote(&instance.service_name),
        shell_quote(&instance.service_name)
    );
    let command = if environment.privilege == "root" {
        stop
    } else {
        format!("sudo -n sh -c {}", shell_quote(&stop))
    };
    let out = state
        .ssh
        .exec(&instance.target_id, &command, 120, false)
        .await?;
    if out.code != 0 {
        instance.last_error = format!("停止失败：{}", out.stderr.trim());
        instance.updated_at = migration_now();
        let _ = upsert_migration_instance(&state.data_dir, instance.clone());
        return Err(instance.last_error);
    }
    instance.running = false;
    instance.service_installed = true;
    instance.last_error.clear();
    instance.updated_at = migration_now();
    upsert_migration_instance(&state.data_dir, instance)
}

async fn gcms_caddy_preflight_inner(
    state: &AppState,
    conn_id: &str,
    domain: &str,
    redirect_domain: Option<&str>,
    instance_path: Option<&str>,
    instance_port: Option<u16>,
) -> Result<GcmsCaddyPreflight, String> {
    let env = format!(
        "PILOT_DOMAIN={} PILOT_REDIRECT_DOMAIN={} PILOT_GCMS_INSTANCE_PATH={} PILOT_GCMS_INSTANCE_PORT={}",
        shell_quote(domain),
        shell_quote(redirect_domain.unwrap_or_default()),
        shell_quote(instance_path.unwrap_or_default()),
        instance_port.unwrap_or(0)
    );
    let body = shell_quote(GCMS_CADDY_PREFLIGHT_CMD);
    let command = format!("env {env} sh -c {body}");
    let out = state.ssh.exec(conn_id, &command, 35, false).await?;
    if out.code != 0 {
        let detail = out.stderr.trim();
        return Err(if detail.is_empty() {
            format!("Web 服务预检失败（退出码 {}）", out.code)
        } else {
            format!(
                "Web 服务预检失败：{}",
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
                format!("Web 服务提权预检失败（退出码 {}）", out.code)
            } else {
                format!(
                    "Web 服务提权预检失败：{}",
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
    allowed_cname_target: Option<&str>,
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
    let cname_matched = address_records.is_empty()
        && has_cname
        && allowed_cname_target.is_some_and(|target| {
            relevant
                .iter()
                .filter(|record| record.record_type == "CNAME")
                .all(|record| {
                    record
                        .content
                        .trim_end_matches('.')
                        .eq_ignore_ascii_case(target)
                })
        });
    let origin_matched = cname_matched
        || (!address_records.is_empty()
            && address_records.iter().all(|record| {
                record.content.parse::<IpAddr>().is_ok_and(|ip| match ip {
                    IpAddr::V4(_) => server_v4.contains(&ip),
                    IpAddr::V6(_) => server_v6.contains(&ip),
                })
            }));
    let any_proxied = relevant.iter().any(|record| record.proxied);
    // 同一主机名可能同时有 A / AAAA。只有全部相关记录都已代理，前端才可显示
    // “橙云已完成”；部分代理仍需进入最后一步，把剩余记录安全补齐。
    let proxied = !relevant.is_empty() && relevant.iter().all(|record| record.proxied);
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
    } else if address_records.is_empty() && has_cname && !cname_matched {
        (
            "unsupported_record",
            "检测到指向其他目标的 CNAME。为避免把代理链误判成当前服务器，Pilot 不会自动修改。"
                .into(),
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
    } else if any_proxied && !inspect.ssl_error.is_empty() {
        (
            "ssl_unreadable",
            "橙云已开启，但无法读取 SSL/TLS 模式。请给 Token 增加 Zone Settings: Read 权限后重新检测。".into(),
        )
    } else if any_proxied && !matches!(inspect.ssl_mode.as_str(), "full" | "strict") {
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
            if cname_matched {
                if proxied {
                    "跳转域名的 CNAME 已指向主域名，橙云与 SSL/TLS 模式可安全继续。".into()
                } else if any_proxied {
                    "跳转域名只有部分记录开启橙云，Pilot 会在源站验证后补齐。".into()
                } else {
                    "跳转域名的 CNAME 已指向主域名。".into()
                }
            } else if proxied {
                "橙云已开启；真实源站记录与服务器一致，SSL/TLS 模式可安全继续。".into()
            } else if any_proxied {
                "部分 DNS 记录已开启橙云；真实源站一致，Pilot 会在源站验证后补齐。".into()
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
        candidates: Vec::new(),
    }
}

fn cloudflare_error_is_permission_related(error: &str) -> bool {
    error.contains("Cloudflare 401")
        || error.contains("Cloudflare 403")
        || error.contains("权限")
        || error.to_ascii_lowercase().contains("unauthorized")
}

fn cloudflare_candidate(
    connection: &pack::Connection,
    zone: &str,
    status: &str,
    detail: &str,
    permission_complete: bool,
    preferred: bool,
) -> GcmsCloudflareCandidate {
    GcmsCloudflareCandidate {
        connection_id: connection.id.clone(),
        connection_name: connection.name.clone(),
        connection_remark: connection.remark.clone(),
        key_prefix: connection.key_prefix.clone(),
        account_id: connection.account_id.clone(),
        zone_name: zone.into(),
        status: status.into(),
        detail: detail.into(),
        permission_complete,
        preferred,
    }
}

fn cloudflare_check_with_candidate(
    status: &str,
    connected_accounts: usize,
    zone: &str,
    detail: String,
    selected: Option<&GcmsCloudflareCandidate>,
    candidates: Vec<GcmsCloudflareCandidate>,
) -> GcmsCloudflareCheck {
    GcmsCloudflareCheck {
        status: status.into(),
        connected_accounts,
        connection_id: selected
            .map(|candidate| candidate.connection_id.clone())
            .unwrap_or_default(),
        connection_name: selected
            .map(|candidate| candidate.connection_name.clone())
            .unwrap_or_default(),
        zone_name: zone.into(),
        detail,
        candidates,
        ..Default::default()
    }
}

fn auto_cloudflare_candidate_index(candidates: &[GcmsCloudflareCandidate]) -> Option<usize> {
    let complete = candidates
        .iter()
        .enumerate()
        .filter_map(|(index, candidate)| candidate.permission_complete.then_some(index))
        .collect::<Vec<_>>();
    if complete.len() == 1 {
        complete.first().copied()
    } else if candidates.len() == 1 {
        Some(0)
    } else {
        None
    }
}

async fn inspect_cloudflare_hosting(
    state: &AppState,
    domain: &str,
    zone: &str,
    server_v4: &[IpAddr],
    server_v6: &[IpAddr],
    allowed_cname_target: Option<&str>,
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

    let preferred_id = connections
        .iter()
        .find(|connection| {
            connection
                .preferred_zones
                .iter()
                .any(|saved| saved.eq_ignore_ascii_case(zone))
        })
        .map(|connection| connection.id.clone());
    let mut ordered_connections = connections.iter().collect::<Vec<_>>();
    ordered_connections
        .sort_by_key(|connection| preferred_id.as_deref() != Some(connection.id.as_str()));
    let mut found = Vec::<(GcmsCloudflareCheck, GcmsCloudflareCandidate)>::new();
    let mut permission_failures = Vec::<GcmsCloudflareCandidate>::new();
    let mut api_failures = Vec::<GcmsCloudflareCandidate>::new();
    for connection in ordered_connections {
        let preferred = preferred_id.as_deref() == Some(connection.id.as_str());
        let connection_id = connection.id.clone();
        let key_id = connection_id.clone();
        let token =
            match tauri::async_runtime::spawn_blocking(move || keychain::get_key(&key_id)).await {
                Ok(Ok(token)) => token,
                Ok(Err(error)) => {
                    let detail = format!("{}：{error}", connection.name);
                    api_failures.push(cloudflare_candidate(
                        connection,
                        zone,
                        "api_error",
                        &detail,
                        false,
                        preferred,
                    ));
                    continue;
                }
                Err(error) => {
                    let detail = format!("{}：读取凭据失败（{error}）", connection.name);
                    api_failures.push(cloudflare_candidate(
                        connection,
                        zone,
                        "api_error",
                        &detail,
                        false,
                        preferred,
                    ));
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
                    allowed_cname_target,
                );
                let candidate = cloudflare_candidate(
                    connection,
                    &classified.zone_name,
                    &classified.status,
                    &classified.detail,
                    classified.ssl_error.is_empty(),
                    preferred,
                );
                // 已有明确绑定时，正常检测只访问这一条连接，不再把用户的所有 Token
                // 逐个试一遍。仅当它失效/无权/已不含该 Zone 时，才继续寻找可切换候选。
                if preferred {
                    let mut selected = classified;
                    selected.candidates = vec![candidate];
                    return selected;
                }
                found.push((classified, candidate));
            }
            Ok(None) => {}
            Err(error) => {
                let detail = format!("{}：{error}", connection.name);
                let candidate = cloudflare_candidate(
                    connection,
                    zone,
                    if cloudflare_error_is_permission_related(&detail) {
                        "permission_error"
                    } else {
                        "api_error"
                    },
                    &detail,
                    false,
                    preferred,
                );
                if cloudflare_error_is_permission_related(&detail) {
                    permission_failures.push(candidate);
                } else {
                    api_failures.push(candidate);
                }
            }
        }
    }

    let successful_candidates = found
        .iter()
        .map(|(_, candidate)| candidate.clone())
        .collect::<Vec<_>>();

    // 用户已经明确选择过时，绝不因列表顺序或另一个 Token 权限更高而悄悄换连接。
    if let Some(preferred_id) = preferred_id.as_deref() {
        if let Some((mut selected, _)) = found
            .iter()
            .find(|(_, candidate)| candidate.connection_id == preferred_id)
            .cloned()
        {
            selected.candidates = successful_candidates;
            return selected;
        }
        if let Some(selected) = permission_failures
            .iter()
            .find(|candidate| candidate.connection_id == preferred_id)
        {
            let mut candidates = successful_candidates;
            candidates.push(selected.clone());
            return cloudflare_check_with_candidate(
                "permission_error",
                connected_accounts,
                zone,
                format!(
                    "已固定使用 Cloudflare 连接「{}」，但它无法完整读取 {zone}。请更新这条连接的 Token，至少授予 Zone: Read、DNS: Read（或 Edit）和 Zone Settings: Read。 {}",
                    selected.connection_name, selected.detail
                ),
                Some(selected),
                candidates,
            );
        }
        if let Some(selected) = api_failures
            .iter()
            .find(|candidate| candidate.connection_id == preferred_id)
        {
            let mut candidates = successful_candidates;
            candidates.push(selected.clone());
            return cloudflare_check_with_candidate(
                "api_error",
                connected_accounts,
                zone,
                format!(
                    "已固定使用 Cloudflare 连接「{}」，但当前无法读取它。请检查网络、系统钥匙串或 Token 后重试。 {}",
                    selected.connection_name, selected.detail
                ),
                Some(selected),
                candidates,
            );
        }
        if !found.is_empty() {
            return cloudflare_check_with_candidate(
                "connection_selection_required",
                connected_accounts,
                zone,
                format!(
                    "之前选中的 Cloudflare 连接已无法访问 {zone}。为避免误用其他 Token，Pilot 没有自动切换，请重新选择连接。"
                ),
                None,
                successful_candidates,
            );
        }
    }

    // 没有历史选择：只有“唯一权限完整候选”或“唯一候选”才自动采用；其余交给用户。
    let selected_index = auto_cloudflare_candidate_index(&successful_candidates);
    if let Some(index) = selected_index {
        let mut selected = found[index].0.clone();
        selected.candidates = successful_candidates;
        return selected;
    }
    if found.len() > 1 {
        return cloudflare_check_with_candidate(
            "connection_selection_required",
            connected_accounts,
            zone,
            format!(
                "有 {} 条 Cloudflare 连接都能管理 {zone}。请选择本次要使用的连接，Pilot 会记住选择并确保 DNS、HTTPS 与橙云始终使用同一枚 Token。",
                found.len()
            ),
            None,
            successful_candidates,
        );
    }

    if permission_failures.len() == 1 {
        let selected = &permission_failures[0];
        return cloudflare_check_with_candidate(
            "permission_error",
            connected_accounts,
            zone,
            format!(
                "Cloudflare 连接「{}」无法完整读取 {zone}。请更新这条连接的 Token，至少授予 Zone: Read、DNS: Read（或 Edit）和 Zone Settings: Read。 {}",
                selected.connection_name, selected.detail
            ),
            Some(selected),
            permission_failures.clone(),
        );
    }
    if permission_failures.len() > 1 {
        return cloudflare_check_with_candidate(
            "connection_selection_required",
            connected_accounts,
            zone,
            format!(
                "有 {} 条 Cloudflare 连接因权限不足而无法确认是否管理 {zone}。请选择你为这个域名创建的连接，再更新它的 Token。",
                permission_failures.len()
            ),
            None,
            permission_failures,
        );
    }
    if let Some(selected) = api_failures.first().cloned() {
        return cloudflare_check_with_candidate(
            "api_error",
            connected_accounts,
            zone,
            format!(
                "Cloudflare 只读检测暂时失败，请检查网络、系统钥匙串或 Token 后重试。 {}",
                selected.detail
            ),
            (api_failures.len() == 1).then_some(&selected),
            api_failures,
        );
    }
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

async fn inspect_access_domain(
    state: &AppState,
    domain: &str,
    server_v4: &[IpAddr],
    server_v6: &[IpAddr],
    allowed_cname_target: Option<&str>,
) -> GcmsRedirectCheck {
    let ((dns_v4, dns_v6, dns_error), hosting) =
        tokio::join!(resolve_domain_ips(domain), detect_dns_hosting(domain));
    // 不能只凭“其中一个地址命中”放行：遗留的 A/AAAA 会把一部分访客带到错误源站。
    let direct_dns_matched = dns_addresses_match_server(&dns_v4, &dns_v6, server_v4, server_v6);
    let cloudflare = if hosting.provider == "cloudflare" {
        Some(
            inspect_cloudflare_hosting(
                state,
                domain,
                &hosting.zone,
                server_v4,
                server_v6,
                allowed_cname_target,
            )
            .await,
        )
    } else {
        None
    };
    // Cloudflare（尤其橙云）必须以 API 中的真实源站记录为准；其他托管商保持公网 DNS 对照。
    let matched = cloudflare
        .as_ref()
        .map(|check| check.status == "matched")
        .unwrap_or(direct_dns_matched);
    GcmsRedirectCheck {
        domain: domain.into(),
        dns_ipv4: dns_v4.into_iter().map(|ip| ip.to_string()).collect(),
        dns_ipv6: dns_v6.into_iter().map(|ip| ip.to_string()).collect(),
        dns_error,
        hosting,
        direct_dns_matched,
        cloudflare,
        matched,
    }
}

async fn gcms_remote_access_check_inner(
    state: &AppState,
    conn_id: &str,
    raw_domain: &str,
    raw_redirect_domain: Option<&str>,
    instance_path: Option<&str>,
    instance_port: Option<u16>,
) -> Result<GcmsAccessCheck, String> {
    let domain = normalize_public_domain(raw_domain)?;
    let redirect_domain = normalize_redirect_domain(raw_redirect_domain, &domain)?;
    let conn = state.conns.get(conn_id)?;
    if conn.kind != "ssh" {
        return Err("这不是远程服务器连接".into());
    }
    let status = gcms_remote_status_at(state, conn_id, instance_path).await?;
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

    let (primary, redirect) = if let Some(redirect_domain) = redirect_domain.as_deref() {
        let (primary, redirect) = tokio::join!(
            inspect_access_domain(state, &domain, &server_v4, &server_v6, None),
            inspect_access_domain(
                state,
                redirect_domain,
                &server_v4,
                &server_v6,
                Some(&domain)
            )
        );
        (primary, Some(redirect))
    } else {
        (
            inspect_access_domain(state, &domain, &server_v4, &server_v6, None).await,
            None,
        )
    };
    let primary_matched = primary.matched;
    let matched = primary_matched && redirect.as_ref().map(|check| check.matched).unwrap_or(true);
    let caddy = if matched {
        Some(
            gcms_caddy_preflight_inner(
                state,
                conn_id,
                &domain,
                redirect_domain.as_deref(),
                instance_path,
                instance_port,
            )
            .await?,
        )
    } else {
        None
    };
    Ok(GcmsAccessCheck {
        domain,
        server_ipv4: server_v4.into_iter().map(|ip| ip.to_string()).collect(),
        server_ipv6: server_v6.into_iter().map(|ip| ip.to_string()).collect(),
        dns_ipv4: primary.dns_ipv4,
        dns_ipv6: primary.dns_ipv6,
        dns_error: primary.dns_error,
        hosting: primary.hosting,
        direct_dns_matched: primary.direct_dns_matched,
        cloudflare: primary.cloudflare,
        primary_matched,
        redirect,
        matched,
        caddy,
    })
}

#[tauri::command]
pub(super) async fn gcms_remote_access_check(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    domain: String,
    redirect_domain: Option<String>,
    instance_path: Option<String>,
    instance_port: Option<u16>,
) -> Result<GcmsAccessCheck, String> {
    gcms_remote_access_check_inner(
        &state,
        &conn_id,
        &domain,
        redirect_domain.as_deref(),
        instance_path.as_deref(),
        instance_port,
    )
    .await
}

/// 用户在多个候选中明确选择管理某个 Zone 的 Cloudflare 连接。选择前重新只读确认
/// 该 Token 确实能看到 Zone，避免仅凭前端传来的连接 id 写入错误绑定。
#[tauri::command]
pub(super) async fn gcms_cloudflare_select_connection(
    state: tauri::State<'_, AppState>,
    zone: String,
    connection_id: String,
) -> Result<pack::Connection, String> {
    let zone = normalize_public_domain(&zone)?;
    let connection = state.conns.get(&connection_id)?;
    if connection.kind != "cloudflare" {
        return Err("这不是 Cloudflare 连接".into());
    }
    let key_id = connection_id.clone();
    let token = tauri::async_runtime::spawn_blocking(move || keychain::get_key(&key_id))
        .await
        .map_err(|error| format!("读取 Cloudflare 凭据失败：{error}"))??;
    let inspect = cf::inspect_hostname(&token, &connection.account_id, &zone, &zone).await?;
    if inspect.is_none() {
        return Err(format!(
            "Cloudflare 连接「{}」中没有找到 Zone {zone}，未保存选择",
            connection.name
        ));
    }
    state
        .conns
        .set_cloudflare_zone_preference(&connection_id, &zone)
}

/// 用户主动要求改用其他连接时，只清除本地 Zone 选择；不会删除 Token 或修改 Cloudflare。
#[tauri::command]
pub(super) async fn gcms_cloudflare_clear_connection(
    state: tauri::State<'_, AppState>,
    zone: String,
) -> Result<(), String> {
    let zone = normalize_public_domain(&zone)?;
    state.conns.clear_cloudflare_zone_preference(&zone)
}

fn remember_cloudflare_selections(state: &AppState, check: &GcmsAccessCheck) -> Result<(), String> {
    let mut selected = Vec::<(String, String)>::new();
    if let Some(cloudflare) = check.cloudflare.as_ref() {
        if !cloudflare.connection_id.is_empty() && !cloudflare.zone_name.is_empty() {
            selected.push((
                cloudflare.zone_name.clone(),
                cloudflare.connection_id.clone(),
            ));
        }
    }
    if let Some(cloudflare) = check
        .redirect
        .as_ref()
        .and_then(|redirect| redirect.cloudflare.as_ref())
    {
        if !cloudflare.connection_id.is_empty() && !cloudflare.zone_name.is_empty() {
            selected.push((
                cloudflare.zone_name.clone(),
                cloudflare.connection_id.clone(),
            ));
        }
    }

    let mut seen = std::collections::HashMap::<String, String>::new();
    for (zone_name, connection_id) in &selected {
        if let Some(existing) = seen.insert(zone_name.to_ascii_lowercase(), connection_id.clone()) {
            if existing != *connection_id {
                return Err(format!(
                    "同一个 Cloudflare Zone {} 被核验为两条不同连接，已停止操作，请重新选择",
                    zone_name
                ));
            }
        }
    }
    for (zone_name, connection_id) in selected {
        state
            .conns
            .set_cloudflare_zone_preference(&connection_id, &zone_name)?;
    }
    Ok(())
}

#[tauri::command]
pub(super) async fn gcms_cloudflare_create_a_record(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    domain: String,
    redirect_domain: Option<String>,
) -> Result<GcmsCloudflareCreateResult, String> {
    let conn = state.conns.get(&conn_id)?;
    if conn.kind != "ssh" {
        return Err("这不是远程服务器连接".into());
    }
    let _guard = begin_gcms_operation(&state, &conn_id)?;
    let check = gcms_remote_access_check_inner(
        &state,
        &conn_id,
        &domain,
        redirect_domain.as_deref(),
        None,
        None,
    )
    .await?;
    if check.matched {
        return Ok(GcmsCloudflareCreateResult {
            created: false,
            created_domains: Vec::new(),
            check,
        });
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
    let mut targets = Vec::new();
    for (label, route_domain, route_matched, cloudflare) in [
        (
            "主访问域名",
            check.domain.as_str(),
            check.primary_matched,
            check.cloudflare.as_ref(),
        ),
        (
            "跳转域名",
            check
                .redirect
                .as_ref()
                .map(|route| route.domain.as_str())
                .unwrap_or_default(),
            check
                .redirect
                .as_ref()
                .map(|route| route.matched)
                .unwrap_or(true),
            check
                .redirect
                .as_ref()
                .and_then(|route| route.cloudflare.as_ref()),
        ),
    ] {
        if route_domain.is_empty() || route_matched {
            continue;
        }
        let cloudflare = cloudflare.ok_or_else(|| {
            format!("{label} {route_domain} 未由 Cloudflare 托管，请先手动完成 DNS 解析")
        })?;
        if cloudflare.status != "record_missing" {
            return Err(format!(
                "{label} {route_domain} 当前不允许自动创建记录：{}",
                cloudflare.detail
            ));
        }
        if cloudflare.zone_status != "active" {
            return Err(format!(
                "{label}所在的 Cloudflare Zone 当前状态为 {}，请先等待 Zone 激活",
                if cloudflare.zone_status.is_empty() {
                    "未知"
                } else {
                    &cloudflare.zone_status
                }
            ));
        }
        if cloudflare.connection_id.is_empty() || cloudflare.zone_name.is_empty() {
            return Err(format!(
                "{label}没有可用于创建记录的 Cloudflare 连接或 Zone"
            ));
        }
        targets.push((
            label,
            route_domain.to_string(),
            cloudflare.connection_id.clone(),
            cloudflare.zone_name.clone(),
        ));
    }
    if targets.is_empty() {
        return Err("当前域名状态不允许自动创建 Cloudflare 记录".into());
    }

    // 从此处起会发生 Cloudflare 写入：先固定 Zone→连接，保证主域名、跳转域名和后续
    // 橙云操作不会因为连接列表顺序变化而换用另一枚 Token。
    remember_cloudflare_selections(&state, &check)?;

    // 先把所有连接和凭据读完，再开始任何 DNS 写入，避免第二个账号凭据无效时留下
    // “只创建了一半”的可预见中间状态。网络/API 在写入期间失败仍会返回明确域名。
    let mut prepared = Vec::new();
    for (label, route_domain, cf_connection_id, zone_name) in targets {
        let cf_connection = state.conns.get(&cf_connection_id)?;
        if cf_connection.kind != "cloudflare" {
            return Err(format!("{label}的核验连接不是 Cloudflare 连接"));
        }
        let key_id = cf_connection_id.clone();
        let token = tauri::async_runtime::spawn_blocking(move || keychain::get_key(&key_id))
            .await
            .map_err(|error| format!("读取 Cloudflare 凭据失败：{error}"))??;
        prepared.push((
            label,
            route_domain,
            cf_connection.account_id,
            zone_name,
            token,
        ));
    }

    let mut created_domains = Vec::new();
    for (label, route_domain, account_id, zone_name, token) in prepared {
        let created =
            cf::create_dns_only_a_record(&token, &account_id, &zone_name, &route_domain, address)
                .await
                .map_err(|error| {
                    format!("创建 {label} {route_domain} 的 Cloudflare A 记录失败：{error}")
                })?;
        if created {
            created_domains.push(route_domain);
        }
    }

    let redirect_domain = check.redirect.as_ref().map(|route| route.domain.as_str());
    let refreshed = gcms_remote_access_check_inner(
        &state,
        &conn_id,
        &check.domain,
        redirect_domain,
        None,
        None,
    )
    .await
    .map_err(|error| {
        if !created_domains.is_empty() {
            format!("DNS 记录已创建，但重新核验失败：{error}。可直接点击重新检测")
        } else {
            format!("DNS 记录已存在，但重新核验失败：{error}。可直接点击重新检测")
        }
    })?;
    Ok(GcmsCloudflareCreateResult {
        created: !created_domains.is_empty(),
        created_domains,
        check: refreshed,
    })
}

fn gcms_check_has_cloudflare(check: &GcmsAccessCheck) -> bool {
    check.cloudflare.is_some()
        || check
            .redirect
            .as_ref()
            .is_some_and(|route| route.cloudflare.is_some())
}

/// 只读汇总 Cloudflare 代理状态。这里绝不修改 DNS；用于恢复旧迁移记录里缺失的
/// HTTPS / 橙云状态，避免把“以前已开启”误显示为“尚未检测”。
fn gcms_cloudflare_proxy_health(check: &GcmsAccessCheck) -> (bool, bool, String) {
    let routes = [
        ("主访问域名", check.cloudflare.as_ref()),
        (
            "跳转域名",
            check
                .redirect
                .as_ref()
                .and_then(|route| route.cloudflare.as_ref()),
        ),
    ];
    let relevant = routes
        .into_iter()
        .filter_map(|(label, cloudflare)| cloudflare.map(|cloudflare| (label, cloudflare)))
        .collect::<Vec<_>>();
    if relevant.is_empty() {
        return (false, false, String::new());
    }
    let proxied = relevant.iter().all(|(_, cloudflare)| cloudflare.proxied);
    let errors = relevant
        .iter()
        .filter(|(_, cloudflare)| cloudflare.status != "matched")
        .map(|(label, cloudflare)| format!("{label}：{}", cloudflare.detail))
        .collect::<Vec<_>>();
    (true, proxied, errors.join("；"))
}

/// 一键配置的最后一道安全闸：源站 HTTPS 已验证后，才尝试把本次涉及的 Cloudflare
/// 主机名切换为橙云。失败不会回滚已可用的源站 HTTPS，但会作为明确结果返回前端。
async fn gcms_enable_cloudflare_proxy(
    state: &AppState,
    check: &GcmsAccessCheck,
) -> (bool, bool, String) {
    let applicable = gcms_check_has_cloudflare(check);
    if !applicable {
        return (false, false, String::new());
    }
    if let Err(error) = remember_cloudflare_selections(state, check) {
        return (true, false, error);
    }
    let expected_addresses = check
        .server_ipv4
        .iter()
        .chain(check.server_ipv6.iter())
        .filter_map(|address| address.parse::<IpAddr>().ok())
        .collect::<Vec<_>>();
    if expected_addresses.is_empty() {
        return (
            true,
            false,
            "未检测到可核验的服务器公网 IP，未开启 Cloudflare 橙云".into(),
        );
    }

    let mut targets = Vec::new();
    for (label, hostname, cloudflare, allowed_cname_target) in [
        (
            "主访问域名",
            check.domain.as_str(),
            check.cloudflare.as_ref(),
            None,
        ),
        (
            "跳转域名",
            check
                .redirect
                .as_ref()
                .map(|route| route.domain.as_str())
                .unwrap_or_default(),
            check
                .redirect
                .as_ref()
                .and_then(|route| route.cloudflare.as_ref()),
            Some(check.domain.as_str()),
        ),
    ] {
        let Some(cloudflare) = cloudflare else {
            continue;
        };
        if hostname.is_empty() {
            continue;
        }
        if cloudflare.status != "matched" || !cloudflare.origin_matched {
            return (
                true,
                false,
                format!(
                    "{label} {hostname} 尚未通过 Cloudflare 源站核验，未开启橙云：{}",
                    cloudflare.detail
                ),
            );
        }
        if cloudflare.proxied {
            continue;
        }
        if cloudflare.connection_id.is_empty() || cloudflare.zone_name.is_empty() {
            return (
                true,
                false,
                format!("{label} {hostname} 缺少可写入的 Cloudflare 连接或 Zone"),
            );
        }
        targets.push((
            label,
            hostname.to_string(),
            cloudflare.connection_id.clone(),
            cloudflare.zone_name.clone(),
            allowed_cname_target.map(str::to_string),
        ));
    }
    if targets.is_empty() {
        return (true, true, String::new());
    }

    // 在任何写入前先读取全部凭据，避免主域名已切换、跳转域名却因凭据缺失而中断。
    let mut prepared = Vec::new();
    for (label, hostname, connection_id, zone_name, allowed_cname_target) in targets {
        let connection = match state.conns.get(&connection_id) {
            Ok(connection) if connection.kind == "cloudflare" => connection,
            Ok(_) => {
                return (
                    true,
                    false,
                    format!("{label} {hostname} 的核验连接不是 Cloudflare 连接"),
                )
            }
            Err(error) => return (true, false, error),
        };
        let key_id = connection_id.clone();
        let token =
            match tauri::async_runtime::spawn_blocking(move || keychain::get_key(&key_id)).await {
                Ok(Ok(token)) => token,
                Ok(Err(error)) => return (true, false, error),
                Err(error) => return (true, false, format!("读取 Cloudflare 凭据失败：{error}")),
            };
        prepared.push((
            label,
            hostname,
            connection.account_id,
            zone_name,
            allowed_cname_target,
            token,
        ));
    }

    for (label, hostname, account_id, zone_name, allowed_cname_target, token) in prepared {
        if let Err(error) = cf::enable_proxy_for_hostname(
            &token,
            &account_id,
            &zone_name,
            &hostname,
            &expected_addresses,
            allowed_cname_target.as_deref(),
        )
        .await
        {
            return (
                true,
                false,
                format!("开启 {label} {hostname} 的 Cloudflare 橙云失败：{error}"),
            );
        }
    }
    (true, true, String::new())
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
    // Caddy/浏览器曾可能把首次 404 按 immutable 缓存；每次验证使用独立查询参数，
    // 确保读到修复后的源站响应，而不是旧的负缓存。
    let cache_bust = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis();
    let asset_url = format!("https://{domain}/assets/css/admin.css?pilot_verify={cache_bust}");
    let mut last_error = String::new();
    for attempt in 0..3 {
        match client.get(&url).send().await {
            Ok(response) => {
                let status = response.status();
                let ok = status.is_success() || status.is_redirection();
                if !ok {
                    return (
                        false,
                        Some(status.as_u16()),
                        format!("HTTPS 已连通，但 /admin 返回 HTTP {}", status.as_u16()),
                    );
                }
                return match client.get(&asset_url).send().await {
                    Ok(asset) if asset.status().is_success() => {
                        (true, Some(status.as_u16()), String::new())
                    }
                    Ok(asset) => (
                        false,
                        Some(status.as_u16()),
                        format!(
                            "HTTPS 已连通，但 GCMS 页面资源返回 HTTP {}",
                            asset.status().as_u16()
                        ),
                    ),
                    Err(e) => (
                        false,
                        Some(status.as_u16()),
                        format!("HTTPS 已连通，但暂时无法读取 GCMS 页面资源：{e}"),
                    ),
                };
            }
            Err(e) => last_error = e.to_string(),
        }
        if attempt < 2 {
            tokio::time::sleep(Duration::from_secs(2)).await;
        }
    }
    (false, None, format!("HTTPS 暂未连通：{last_error}"))
}

fn gcms_verification_needs_domain_reload(error: &str) -> bool {
    error.contains("GCMS 页面资源") || error.contains("/admin 返回 HTTP 404")
}

async fn verify_gcms_redirect(
    primary_domain: &str,
    redirect_domain: &str,
) -> (bool, Option<u16>, String) {
    let client = match reqwest::Client::builder()
        .redirect(reqwest::redirect::Policy::none())
        .timeout(Duration::from_secs(18))
        .build()
    {
        Ok(client) => client,
        Err(error) => return (false, None, format!("创建跳转检测请求失败：{error}")),
    };
    let path = "/admin?pilot_redirect_verify=1";
    let url = format!("https://{redirect_domain}{path}");
    let expected = format!("https://{primary_domain}{path}");
    let mut last_error = String::new();
    for attempt in 0..3 {
        match client.get(&url).send().await {
            Ok(response) => {
                let status = response.status();
                let location = response
                    .headers()
                    .get(reqwest::header::LOCATION)
                    .and_then(|value| value.to_str().ok())
                    .unwrap_or_default();
                if status.as_u16() == 301 && location == expected {
                    return (true, Some(301), String::new());
                }
                last_error = if status.as_u16() != 301 {
                    format!(
                        "跳转域名已连通，但应返回 HTTP 301，实际返回 HTTP {}",
                        status.as_u16()
                    )
                } else {
                    format!(
                        "跳转目标不正确：应为 {expected}，实际为 {}",
                        if location.is_empty() {
                            "未返回 Location"
                        } else {
                            location
                        }
                    )
                };
            }
            Err(error) => last_error = format!("跳转域名 HTTPS 暂未连通：{error}"),
        }
        if attempt < 2 {
            tokio::time::sleep(Duration::from_secs(2)).await;
        }
    }
    (false, None, last_error)
}

#[tauri::command]
pub(super) async fn gcms_remote_access_verify(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    domain: String,
    redirect_domain: Option<String>,
    enable_cloudflare_proxy: Option<bool>,
    instance_path: Option<String>,
    _instance_port: Option<u16>,
    migration_instance_id: Option<String>,
) -> Result<GcmsAccessApplyResult, String> {
    let conn = state.conns.get(&conn_id)?;
    if conn.kind != "ssh" {
        return Err("这不是远程服务器连接".into());
    }
    let domain = normalize_public_domain(&domain)?;
    let redirect_domain = normalize_redirect_domain(redirect_domain.as_deref(), &domain)?;
    let migration_instance = migration_instance_for_request(
        &state.data_dir,
        migration_instance_id.as_deref(),
        &conn_id,
        instance_path.as_deref(),
    )?;
    let effective_instance_path = migration_instance
        .as_ref()
        .map(|instance| instance.instance_path.as_str())
        .or(instance_path.as_deref());
    let effective_instance_port = migration_instance
        .as_ref()
        .map(|instance| instance.port)
        .or(_instance_port);
    let _guard = begin_gcms_operation(&state, &conn_id)?;
    let status = gcms_remote_status_at(&state, &conn_id, effective_instance_path).await?;
    let (mut https_ok, mut http_status, mut verification_error) = verify_gcms_https(&domain).await;

    // 兼容旧版 Pilot 已写入 BASE_URL、但实际进程没有重新加载的安装：只有后台路由或
    // 内置页面资源明确失败时才执行定向修复，然后重新验证；普通 DNS/证书等待不重启。
    if !https_ok
        && gcms_verification_needs_domain_reload(&verification_error)
        && status.installed
        && !status.path.is_empty()
    {
        let env = format!(
            "PILOT_DOMAIN={} PILOT_GCMS_HOME={} PILOT_GCMS_SERVICE_NAME={}",
            shell_quote(&domain),
            shell_quote(&status.path),
            shell_quote(
                migration_instance
                    .as_ref()
                    .map(|instance| instance.service_name.as_str())
                    .unwrap_or_default()
            )
        );
        let body = shell_quote(GCMS_REMOTE_RELOAD_DOMAIN_CMD);
        let command = if migration_instance.is_some() {
            let environment = migration_target_env(&state, &conn_id).await?;
            if environment.privilege == "root" {
                format!("env {env} sh -c {body}")
            } else if environment.privilege == "sudo" {
                format!("sudo -n env {env} sh -c {body}")
            } else {
                return Err("重新加载迁移实例需要 root 或免密 sudo".into());
            }
        } else {
            format!("env {env} sh -c {body}")
        };
        let restarted = state.ssh.exec(&conn_id, &command, 120, false).await?;
        if restarted.code == 0 {
            tokio::time::sleep(Duration::from_secs(2)).await;
            (https_ok, http_status, verification_error) = verify_gcms_https(&domain).await;
        } else {
            let detail = gcms_install_log(&restarted.stdout, &restarted.stderr);
            verification_error = format!(
                "HTTPS 已连通，但页面资源自动修复失败：{}",
                detail
                    .lines()
                    .rev()
                    .map(str::trim)
                    .find(|line| !line.is_empty())
                    .unwrap_or("GCMS 重启失败")
            );
        }
    }

    let (mut redirect_ok, mut redirect_http_status, mut redirect_verification_error) =
        if let Some(redirect_domain) = redirect_domain.as_deref() {
            verify_gcms_redirect(&domain, redirect_domain).await
        } else {
            (true, None, String::new())
        };
    let mut all_https_ok = https_ok && redirect_ok;
    if https_ok && !redirect_ok {
        verification_error = redirect_verification_error.clone();
    }
    let (mut cloudflare_proxy_applicable, mut cloudflare_proxied, mut cloudflare_proxy_error) =
        (false, false, String::new());
    if all_https_ok && enable_cloudflare_proxy.unwrap_or(false) {
        match gcms_remote_access_check_inner(
            &state,
            &conn_id,
            &domain,
            redirect_domain.as_deref(),
            effective_instance_path,
            effective_instance_port,
        )
        .await
        {
            Ok(check) => {
                (
                    cloudflare_proxy_applicable,
                    cloudflare_proxied,
                    cloudflare_proxy_error,
                ) = gcms_enable_cloudflare_proxy(&state, &check).await;
            }
            Err(error) => {
                cloudflare_proxy_applicable = true;
                cloudflare_proxy_error = format!("重新核验 Cloudflare 记录失败：{error}");
            }
        }
        if cloudflare_proxied {
            tokio::time::sleep(Duration::from_secs(2)).await;
            (https_ok, http_status, verification_error) = verify_gcms_https(&domain).await;
            if let Some(redirect_domain) = redirect_domain.as_deref() {
                (
                    redirect_ok,
                    redirect_http_status,
                    redirect_verification_error,
                ) = verify_gcms_redirect(&domain, redirect_domain).await;
            }
            all_https_ok = https_ok && redirect_ok;
            if https_ok && !redirect_ok {
                verification_error = redirect_verification_error.clone();
            }
        }
    }
    let refreshed = gcms_remote_status_at(&state, &conn_id, effective_instance_path).await?;
    if let Some(instance_id) = migration_instance_id.as_deref() {
        update_migration_instance_status(
            &state.data_dir,
            instance_id,
            &refreshed,
            all_https_ok,
            cloudflare_proxy_applicable,
            cloudflare_proxied,
            &cloudflare_proxy_error,
            (!all_https_ok).then_some(verification_error.as_str()),
        )?;
    }
    Ok(GcmsAccessApplyResult {
        status: refreshed,
        url: format!("https://{domain}"),
        https_ok: all_https_ok,
        http_status,
        verification_error,
        redirect_url: redirect_domain
            .as_ref()
            .map(|domain| format!("https://{domain}"))
            .unwrap_or_default(),
        redirect_ok,
        redirect_http_status,
        redirect_verification_error,
        cloudflare_proxy_applicable,
        cloudflare_proxied,
        cloudflare_proxy_error,
    })
}

async fn gcms_remote_access_configure_inner(
    state: &AppState,
    conn_id: String,
    domain: String,
    redirect_domain: Option<String>,
    enable_cloudflare_proxy: Option<bool>,
    instance_path: Option<String>,
    instance_port: Option<u16>,
    migration_instance_id: Option<String>,
    on_event: &Channel<GcmsInstallEvent>,
) -> Result<GcmsAccessApplyResult, String> {
    let conn = state.conns.get(&conn_id)?;
    if conn.kind != "ssh" {
        return Err("这不是远程服务器连接".into());
    }
    let migration_instance = migration_instance_for_request(
        &state.data_dir,
        migration_instance_id.as_deref(),
        &conn_id,
        instance_path.as_deref(),
    )?;
    let effective_instance_path = migration_instance
        .as_ref()
        .map(|instance| instance.instance_path.as_str())
        .or(instance_path.as_deref());
    let effective_instance_port = migration_instance
        .as_ref()
        .map(|instance| instance.port)
        .or(instance_port);
    let phase = |message: &str| {
        let _ = on_event.send(GcmsInstallEvent::Phase {
            message: message.to_string(),
        });
    };

    phase("正在复核域名解析与服务器环境…");
    let check = gcms_remote_access_check_inner(
        &state,
        &conn_id,
        &domain,
        redirect_domain.as_deref(),
        effective_instance_path,
        effective_instance_port,
    )
    .await?;
    if !check.matched {
        let (label, route_cloudflare, route_dns_error) = if !check.primary_matched {
            (
                "主访问域名",
                check.cloudflare.as_ref(),
                check.dns_error.as_str(),
            )
        } else if let Some(redirect) = check.redirect.as_ref().filter(|route| !route.matched) {
            (
                "跳转域名",
                redirect.cloudflare.as_ref(),
                redirect.dns_error.as_str(),
            )
        } else {
            ("域名", None, "")
        };
        let reason = route_cloudflare
            .map(|cloudflare| cloudflare.detail.as_str())
            .filter(|detail| !detail.is_empty())
            .or_else(|| (!route_dns_error.is_empty()).then_some(route_dns_error))
            .unwrap_or("DNS 解析尚未指向这台服务器");
        return Err(format!("{label}尚未通过安全校验：{reason}（未修改服务器）"));
    }
    // 一旦用户确认进入配置流程，就把本次核验采用的 Cloudflare 连接固定下来。
    // 即使记录已是橙云、后面无需 API 写入，今后的重新检测也会继续使用同一枚 Token。
    remember_cloudflare_selections(&state, &check)?;
    let preflight = check.caddy.clone().ok_or("未完成 Web 服务预检")?;
    if !preflight.can_auto_configure {
        return Err(format!("当前环境不允许自动配置：{}", preflight.detail));
    }
    let before = gcms_remote_status_at(&state, &conn_id, effective_instance_path).await?;
    if !before.installed || before.path.is_empty() {
        return Err("未检测到标准 GCMS 安装目录".into());
    }

    phase(if preflight.provider == "nginx" {
        "正在备份现有 Nginx 并配置 HTTPS…"
    } else if preflight.mode == "missing" {
        "正在安装并配置 Caddy…"
    } else {
        "正在备份并配置 Caddy…"
    });
    let env = format!(
        "PILOT_DOMAIN={} PILOT_REDIRECT_DOMAIN={} PILOT_GCMS_HOME={} PILOT_GCMS_INSTANCE_PATH={} PILOT_GCMS_INSTANCE_PORT={} PILOT_GCMS_SERVICE_NAME={}",
        shell_quote(&check.domain),
        shell_quote(
            check
                .redirect
                .as_ref()
                .map(|route| route.domain.as_str())
                .unwrap_or_default()
        ),
        shell_quote(&before.path),
        shell_quote(effective_instance_path.unwrap_or_default()),
        effective_instance_port.unwrap_or(0),
        shell_quote(
            migration_instance
                .as_ref()
                .map(|instance| instance.service_name.as_str())
                .unwrap_or_default()
        )
    );
    let configure_script = if preflight.provider == "nginx" {
        GCMS_NGINX_CONFIGURE_CMD
    } else {
        GCMS_CADDY_CONFIGURE_CMD
    };
    let body = shell_quote(configure_script);
    let command = if preflight.privilege == "root" {
        format!("env {env} sh -c {body}")
    } else if preflight.privilege == "sudo" {
        format!("sudo -n env {env} sh -c {body}")
    } else {
        return Err(format!(
            "配置 {} 需要 root 或免密 sudo 权限",
            if preflight.provider == "nginx" {
                "Nginx"
            } else {
                "Caddy"
            }
        ));
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

    phase(if preflight.provider == "nginx" {
        "Nginx 已配置，正在验证 HTTPS…"
    } else {
        "Caddy 已配置，正在验证 HTTPS…"
    });
    let status = gcms_remote_status_at(&state, &conn_id, effective_instance_path).await?;
    let (mut primary_https_ok, mut http_status, mut verification_error) =
        verify_gcms_https(&check.domain).await;
    let (redirect_url, mut redirect_ok, mut redirect_http_status, mut redirect_verification_error) =
        if let Some(redirect) = check.redirect.as_ref() {
            let (ok, status, error) = verify_gcms_redirect(&check.domain, &redirect.domain).await;
            (format!("https://{}", redirect.domain), ok, status, error)
        } else {
            (String::new(), true, None, String::new())
        };
    let mut https_ok = primary_https_ok && redirect_ok;
    if primary_https_ok && !redirect_ok {
        verification_error = redirect_verification_error.clone();
    }
    let (mut cloudflare_proxy_applicable, mut cloudflare_proxied, mut cloudflare_proxy_error) =
        (false, false, String::new());
    if https_ok && enable_cloudflare_proxy.unwrap_or(false) {
        phase("源站 HTTPS 已就绪，正在开启 Cloudflare 橙云代理…");
        (
            cloudflare_proxy_applicable,
            cloudflare_proxied,
            cloudflare_proxy_error,
        ) = gcms_enable_cloudflare_proxy(&state, &check).await;
        if cloudflare_proxied {
            phase("Cloudflare 橙云已开启，正在复检公网访问…");
            tokio::time::sleep(Duration::from_secs(2)).await;
            (primary_https_ok, http_status, verification_error) =
                verify_gcms_https(&check.domain).await;
            if let Some(redirect) = check.redirect.as_ref() {
                (
                    redirect_ok,
                    redirect_http_status,
                    redirect_verification_error,
                ) = verify_gcms_redirect(&check.domain, &redirect.domain).await;
            }
            https_ok = primary_https_ok && redirect_ok;
            if primary_https_ok && !redirect_ok {
                verification_error = redirect_verification_error.clone();
            }
        }
    }
    phase(if !https_ok {
        "配置已保存，等待 HTTPS 生效"
    } else if cloudflare_proxy_applicable && !cloudflare_proxied {
        "网站已可访问，Cloudflare 橙云需要确认"
    } else if cloudflare_proxy_applicable {
        "公网访问与 Cloudflare 代理已就绪"
    } else {
        "公网访问已就绪"
    });
    if let Some(instance_id) = migration_instance_id.as_deref() {
        update_migration_instance_status(
            &state.data_dir,
            instance_id,
            &status,
            https_ok,
            cloudflare_proxy_applicable,
            cloudflare_proxied,
            &cloudflare_proxy_error,
            (!https_ok).then_some(verification_error.as_str()),
        )?;
    }
    Ok(GcmsAccessApplyResult {
        status,
        url: format!("https://{}", check.domain),
        https_ok,
        http_status,
        verification_error,
        redirect_url,
        redirect_ok,
        redirect_http_status,
        redirect_verification_error,
        cloudflare_proxy_applicable,
        cloudflare_proxied,
        cloudflare_proxy_error,
    })
}

#[tauri::command]
pub(super) async fn gcms_remote_access_configure(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    domain: String,
    redirect_domain: Option<String>,
    enable_cloudflare_proxy: Option<bool>,
    instance_path: Option<String>,
    instance_port: Option<u16>,
    migration_instance_id: Option<String>,
    on_event: Channel<GcmsInstallEvent>,
) -> Result<GcmsAccessApplyResult, String> {
    let _guard = begin_gcms_operation(&state, &conn_id)?;
    gcms_remote_access_configure_inner(
        &state,
        conn_id,
        domain,
        redirect_domain,
        enable_cloudflare_proxy,
        instance_path,
        instance_port,
        migration_instance_id,
        &on_event,
    )
    .await
}

struct PreparedGcmsCutover {
    label: String,
    token: String,
    account_id: String,
    plan: cf::CfAddressCutoverPlan,
}

async fn rollback_gcms_cutovers(cutovers: &[PreparedGcmsCutover]) -> String {
    let mut errors = Vec::new();
    for cutover in cutovers.iter().rev() {
        if let Err(error) =
            cf::restore_address_cutover(&cutover.token, &cutover.account_id, &cutover.plan).await
        {
            errors.push(format!("{}：{error}", cutover.label));
        }
    }
    errors.join("；")
}

async fn clear_failed_migration_cutover(
    state: &AppState,
    instance: &GcmsMigrationSnapshot,
    detail: &str,
) -> String {
    let mut errors = Vec::new();
    if let Err(error) =
        clear_remote_migration_access_marker(state, &instance.target_id, &instance.instance_path)
            .await
    {
        errors.push(error);
    }
    if let Err(error) = clear_migration_instance_access(&state.data_dir, &instance.id, detail) {
        errors.push(error);
    }
    errors.join("；")
}

fn gcms_access_mismatch_reason(check: &GcmsAccessCheck) -> String {
    if !check.primary_matched {
        return check
            .cloudflare
            .as_ref()
            .map(|cloudflare| cloudflare.detail.clone())
            .filter(|detail| !detail.is_empty())
            .unwrap_or_else(|| {
                if check.dns_error.is_empty() {
                    "主访问域名仍未指向目标服务器".into()
                } else {
                    check.dns_error.clone()
                }
            });
    }
    check
        .redirect
        .as_ref()
        .filter(|redirect| !redirect.matched)
        .map(|redirect| {
            redirect
                .cloudflare
                .as_ref()
                .map(|cloudflare| cloudflare.detail.clone())
                .filter(|detail| !detail.is_empty())
                .unwrap_or_else(|| {
                    if redirect.dns_error.is_empty() {
                        "跳转域名仍未指向目标服务器".into()
                    } else {
                        redirect.dns_error.clone()
                    }
                })
        })
        .unwrap_or_else(|| "域名复核状态不完整".into())
}

/// 把迁移实例继续使用的 Cloudflare 老域名安全切到目标服务器。
///
/// 只接受结构明确的单 A（可带单 AAAA）记录。所有记录先建内存回滚点，再改 DNS、
/// 配置目标 HTTPS 并验证后台与静态资源；任一步失败都会尽力恢复旧记录。
#[tauri::command]
pub(super) async fn gcms_remote_access_cutover(
    state: tauri::State<'_, AppState>,
    conn_id: String,
    domain: String,
    redirect_domain: Option<String>,
    instance_path: Option<String>,
    instance_port: Option<u16>,
    migration_instance_id: String,
    on_event: Channel<GcmsInstallEvent>,
) -> Result<GcmsAccessApplyResult, String> {
    if migration_instance_id.trim().is_empty() {
        return Err("只有已登记的迁移实例才能自动切换老域名".into());
    }
    let conn = state.conns.get(&conn_id)?;
    if conn.kind != "ssh" {
        return Err("这不是远程服务器连接".into());
    }
    let migration_instance = migration_instance_for_request(
        &state.data_dir,
        Some(&migration_instance_id),
        &conn_id,
        instance_path.as_deref(),
    )?
    .ok_or("迁移实例记录不存在")?;
    if instance_port.is_some_and(|port| port != 0 && port != migration_instance.port) {
        return Err("迁移实例端口与本地登记不一致".into());
    }
    let effective_path = migration_instance.instance_path.clone();
    let effective_port = migration_instance.port;
    let _guard = begin_gcms_operation(&state, &conn_id)?;
    let phase = |message: &str| {
        let _ = on_event.send(GcmsInstallEvent::Phase {
            message: message.to_string(),
        });
    };

    phase("正在建立 Cloudflare DNS 回滚点…");
    let check = gcms_remote_access_check_inner(
        &state,
        &conn_id,
        &domain,
        redirect_domain.as_deref(),
        Some(&effective_path),
        Some(effective_port),
    )
    .await?;
    if check.matched {
        return gcms_remote_access_configure_inner(
            &state,
            conn_id,
            check.domain,
            check.redirect.map(|route| route.domain),
            Some(true),
            Some(effective_path),
            Some(effective_port),
            Some(migration_instance_id),
            &on_event,
        )
        .await;
    }
    let target_ipv4 = match check.server_ipv4.as_slice() {
        [address] => address
            .parse::<Ipv4Addr>()
            .map_err(|_| "目标服务器公网 IPv4 无效")?,
        [] => return Err("未检测到目标服务器公网 IPv4，不能自动切换老域名".into()),
        _ => return Err("检测到多个目标服务器公网 IPv4，请先明确迁移使用的源站地址".into()),
    };
    let target_ipv6 = match check.server_ipv6.as_slice() {
        [address] => Some(
            address
                .parse::<std::net::Ipv6Addr>()
                .map_err(|_| "目标服务器公网 IPv6 无效")?,
        ),
        _ => None,
    };

    let mut routes = Vec::new();
    if !check.primary_matched {
        routes.push((
            "主访问域名".to_string(),
            check.domain.clone(),
            check.cloudflare.clone(),
        ));
    }
    if let Some(redirect) = check.redirect.as_ref().filter(|route| !route.matched) {
        routes.push((
            "跳转域名".to_string(),
            redirect.domain.clone(),
            redirect.cloudflare.clone(),
        ));
    }
    if routes.is_empty() {
        return Err("没有需要切换的域名记录".into());
    }
    remember_cloudflare_selections(&state, &check)?;

    // 先读取所有连接、凭据和旧记录，确保没有任何 DNS 写入后才发现第二个域名不可操作。
    let mut prepared = Vec::new();
    for (label, hostname, cloudflare) in routes {
        let cloudflare = cloudflare.ok_or_else(|| {
            format!("{label} {hostname} 不在 Cloudflare，需在原 DNS 服务商手动切换")
        })?;
        if cloudflare.status != "origin_mismatch" {
            return Err(format!(
                "{label} {hostname} 当前不是可安全切换的旧源站记录：{}",
                cloudflare.detail
            ));
        }
        if cloudflare.connection_id.is_empty() || cloudflare.zone_name.is_empty() {
            return Err(format!("{label}缺少已确认的 Cloudflare 连接或 Zone"));
        }
        let connection = state.conns.get(&cloudflare.connection_id)?;
        if connection.kind != "cloudflare" {
            return Err(format!("{label}的核验连接不是 Cloudflare 连接"));
        }
        let key_id = connection.id.clone();
        let token = tauri::async_runtime::spawn_blocking(move || keychain::get_key(&key_id))
            .await
            .map_err(|error| format!("读取 Cloudflare 凭据失败：{error}"))??;
        let plan = cf::prepare_address_cutover(
            &token,
            &connection.account_id,
            &cloudflare.zone_name,
            &hostname,
            target_ipv4,
            target_ipv6,
        )
        .await
        .map_err(|error| format!("{label} {hostname} 无法建立安全回滚点：{error}"))?;
        prepared.push(PreparedGcmsCutover {
            label: format!("{label} {hostname}"),
            token,
            account_id: connection.account_id,
            plan,
        });
    }

    let mut applied = Vec::new();
    for cutover in prepared {
        phase(&format!("正在切换 {}…", cutover.label));
        if let Err(error) =
            cf::apply_address_cutover(&cutover.token, &cutover.account_id, &cutover.plan).await
        {
            let rollback_error = rollback_gcms_cutovers(&applied).await;
            return Err(if rollback_error.is_empty() {
                error
            } else {
                format!("{error}；其他域名回滚失败：{rollback_error}")
            });
        }
        applied.push(cutover);
    }

    phase("DNS 已切到目标服务器，正在复核并配置 HTTPS…");
    let switched = gcms_remote_access_check_inner(
        &state,
        &conn_id,
        &check.domain,
        check.redirect.as_ref().map(|route| route.domain.as_str()),
        Some(&effective_path),
        Some(effective_port),
    )
    .await;
    let switched = match switched {
        Ok(switched) if switched.matched => switched,
        Ok(switched) => {
            let rollback_error = rollback_gcms_cutovers(&applied).await;
            let reason = gcms_access_mismatch_reason(&switched);
            return Err(if rollback_error.is_empty() {
                format!("DNS 切换后复核未通过，已恢复旧记录：{reason}")
            } else {
                format!("DNS 切换后复核未通过：{reason}；自动回滚失败：{rollback_error}")
            });
        }
        Err(error) => {
            let rollback_error = rollback_gcms_cutovers(&applied).await;
            return Err(if rollback_error.is_empty() {
                format!("DNS 切换后复核失败，已恢复旧记录：{error}")
            } else {
                format!("DNS 切换后复核失败：{error}；自动回滚失败：{rollback_error}")
            });
        }
    };

    let configured = gcms_remote_access_configure_inner(
        &state,
        conn_id.clone(),
        switched.domain.clone(),
        switched.redirect.as_ref().map(|route| route.domain.clone()),
        Some(true),
        Some(effective_path.clone()),
        Some(effective_port),
        Some(migration_instance_id.clone()),
        &on_event,
    )
    .await;
    let mut configured = match configured {
        Ok(result) => result,
        Err(error) => {
            let rollback_error = rollback_gcms_cutovers(&applied).await;
            let detail = format!("目标服务器配置失败：{error}");
            let cleanup_error = if rollback_error.is_empty() {
                clear_failed_migration_cutover(&state, &migration_instance, &detail).await
            } else {
                String::new()
            };
            return Err(if !rollback_error.is_empty() {
                format!("{detail}；Cloudflare 自动回滚失败：{rollback_error}")
            } else if cleanup_error.is_empty() {
                format!("{detail}；Cloudflare 已恢复旧源站")
            } else {
                format!(
                    "{detail}；Cloudflare 已恢复旧源站，但目标实例状态清理失败：{cleanup_error}"
                )
            });
        }
    };

    // ACME 签发和 Cloudflare 边缘刷新偶尔超过普通验证窗口；切换流程多等待一轮，
    // 仍不能确认后台及资源正常时再恢复旧源站。
    for _ in 0..5 {
        if configured.https_ok {
            break;
        }
        tokio::time::sleep(Duration::from_secs(4)).await;
        let (primary_ok, status, error) = verify_gcms_https(&switched.domain).await;
        configured.http_status = status;
        configured.verification_error = error;
        let (redirect_ok, redirect_status, redirect_error) =
            if let Some(redirect) = switched.redirect.as_ref() {
                verify_gcms_redirect(&switched.domain, &redirect.domain).await
            } else {
                (true, None, String::new())
            };
        configured.redirect_ok = redirect_ok;
        configured.redirect_http_status = redirect_status;
        configured.redirect_verification_error = redirect_error;
        configured.https_ok = primary_ok && redirect_ok;
    }
    if !configured.https_ok {
        let rollback_error = rollback_gcms_cutovers(&applied).await;
        let detail = if configured.verification_error.is_empty() {
            "HTTPS 或页面资源未在安全窗口内就绪".to_string()
        } else {
            configured.verification_error.clone()
        };
        if rollback_error.is_empty() {
            let cleanup_error =
                clear_failed_migration_cutover(&state, &migration_instance, &detail).await;
            return Err(if cleanup_error.is_empty() {
                format!("{detail}；Cloudflare 已自动恢复旧源站")
            } else {
                format!(
                    "{detail}；Cloudflare 已恢复旧源站，但目标实例状态清理失败：{cleanup_error}"
                )
            });
        }
        update_migration_instance_status(
            &state.data_dir,
            &migration_instance_id,
            &configured.status,
            configured.https_ok,
            configured.cloudflare_proxy_applicable,
            configured.cloudflare_proxied,
            &configured.cloudflare_proxy_error,
            Some(&detail),
        )?;
        return Err(format!(
            "{detail}；Cloudflare 自动回滚失败：{rollback_error}"
        ));
    }
    phase("老域名已安全切换到迁移实例");
    Ok(configured)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_remote_gcms_probe() {
        let found = parse_gcms_remote_status(
            "login banner\nPILOT_GCMS_INSTALLED\t1\nPILOT_GCMS_PATH\t/opt/gcms\nPILOT_GCMS_VERSION\tv1.3.36\nPILOT_GCMS_RUNNING\t1\nPILOT_GCMS_PORT\t8080\nPILOT_GCMS_BASE_URL\thttps://cms.example.com\nPILOT_GCMS_REDIRECT_DOMAIN\twww.example.com\n",
        ).unwrap();
        assert!(found.installed && found.running);
        assert_eq!(found.path, "/opt/gcms");
        assert_eq!(found.version, "v1.3.36");
        assert_eq!(found.port, 8080);
        assert_eq!(found.base_url, "https://cms.example.com");
        assert_eq!(found.redirect_domain, "www.example.com");
        assert_eq!(found.password_status, "unknown");
        assert!(
            !parse_gcms_remote_status("PILOT_GCMS_INSTALLED\t0\n")
                .unwrap()
                .installed
        );
        assert!(parse_gcms_remote_status("unrelated output").is_err());
    }

    #[test]
    fn parses_remote_gcms_password_status_without_exposing_credentials() {
        let found = parse_gcms_remote_status(
            "PILOT_GCMS_INSTALLED\t1\nPILOT_GCMS_PATH\t/opt/gcms\nPILOT_GCMS_PASSWORD_STATUS\tdefault\nPILOT_GCMS_ADMIN_USER\tadmin\n",
        )
        .unwrap();
        assert_eq!(found.password_status, "default");
        assert_eq!(found.admin_user, "admin");

        let invalid = parse_gcms_remote_status(
            "PILOT_GCMS_INSTALLED\t1\nPILOT_GCMS_PATH\t/opt/gcms\nPILOT_GCMS_PASSWORD_STATUS\tnot-a-status\n",
        )
        .unwrap();
        assert_eq!(invalid.password_status, "unknown");
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
    fn domain_reload_only_repairs_confirmed_gcms_route_failures() {
        assert!(gcms_verification_needs_domain_reload(
            "HTTPS 已连通，但 GCMS 页面资源返回 HTTP 404"
        ));
        assert!(gcms_verification_needs_domain_reload(
            "HTTPS 已连通，但 /admin 返回 HTTP 404"
        ));
        assert!(!gcms_verification_needs_domain_reload(
            "HTTPS 暂未连通：dns error"
        ));
        assert!(!gcms_verification_needs_domain_reload(
            "HTTPS 已连通，但 /admin 返回 HTTP 503"
        ));
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
        assert_eq!(
            domain_from_base_url("https://CMS.Example.COM/admin").unwrap(),
            "cms.example.com"
        );
        assert!(domain_from_base_url("not-a-url").is_err());
        assert_eq!(
            normalize_redirect_domain(Some(" WWW.Example.COM. "), "example.com").unwrap(),
            Some("www.example.com".into())
        );
        assert_eq!(
            normalize_redirect_domain(Some("  "), "example.com").unwrap(),
            None
        );
        assert!(normalize_redirect_domain(Some("example.com"), "example.com").is_err());
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
                id: "record-1".into(),
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
            None,
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
            None,
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
            None,
        );
        assert_eq!(unreadable.status, "ssl_unreadable");
    }

    #[test]
    fn partial_cloudflare_proxy_is_not_reported_as_complete() {
        let server_v4 = vec!["203.0.113.8".parse::<IpAddr>().unwrap()];
        let server_v6 = vec!["2001:4860:4860::8888".parse::<IpAddr>().unwrap()];
        let mut inspect = cf_inspection("A", "203.0.113.8", true, "strict");
        inspect.records.push(cf::CfDnsRecord {
            id: "record-2".into(),
            record_type: "AAAA".into(),
            name: "cms.example.com".into(),
            content: "2001:4860:4860::8888".into(),
            proxied: false,
            proxiable: true,
        });
        let result = classify_cloudflare_inspection(
            "cf-1",
            "Cloudflare",
            1,
            "cms.example.com",
            inspect,
            &server_v4,
            &server_v6,
            None,
        );
        assert_eq!(result.status, "matched");
        assert!(result.origin_matched);
        assert!(!result.proxied);
        assert!(result.detail.contains("部分 DNS 记录"));
    }

    #[test]
    fn read_only_proxy_health_keeps_https_and_cloudflare_separate() {
        let server = vec!["203.0.113.8".parse::<IpAddr>().unwrap()];
        let cloudflare = classify_cloudflare_inspection(
            "cf-1",
            "Cloudflare",
            1,
            "cms.example.com",
            cf_inspection("A", "203.0.113.8", true, "strict"),
            &server,
            &[],
            None,
        );
        let mut check = GcmsAccessCheck {
            domain: "cms.example.com".into(),
            server_ipv4: vec!["203.0.113.8".into()],
            server_ipv6: Vec::new(),
            dns_ipv4: vec!["203.0.113.8".into()],
            dns_ipv6: Vec::new(),
            dns_error: String::new(),
            hosting: GcmsDnsHosting::default(),
            direct_dns_matched: true,
            cloudflare: Some(cloudflare),
            primary_matched: true,
            redirect: None,
            matched: true,
            caddy: None,
        };
        assert_eq!(
            gcms_cloudflare_proxy_health(&check),
            (true, true, String::new())
        );
        check.cloudflare.as_mut().unwrap().proxied = false;
        assert_eq!(
            gcms_cloudflare_proxy_health(&check),
            (true, false, String::new())
        );
        check.cloudflare = None;
        assert_eq!(
            gcms_cloudflare_proxy_health(&check),
            (false, false, String::new())
        );
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
            None,
        );
        assert_eq!(result.status, "matched");
        assert!(!result.proxied);
    }

    #[test]
    fn cloudflare_auto_selection_only_chooses_an_unambiguous_connection() {
        let candidate = |id: &str, permission_complete: bool| GcmsCloudflareCandidate {
            connection_id: id.into(),
            permission_complete,
            ..Default::default()
        };
        assert_eq!(
            auto_cloudflare_candidate_index(&[candidate("one", false)]),
            Some(0)
        );
        assert_eq!(
            auto_cloudflare_candidate_index(&[
                candidate("old", false),
                candidate("complete", true),
            ]),
            Some(1)
        );
        assert_eq!(
            auto_cloudflare_candidate_index(&[candidate("one", true), candidate("two", true),]),
            None
        );
        assert_eq!(
            auto_cloudflare_candidate_index(&[candidate("one", false), candidate("two", false),]),
            None
        );
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
            None,
        );
        assert_eq!(result.status, "unsupported_record");
        assert!(!result.origin_matched);
    }

    #[test]
    fn cloudflare_redirect_cname_to_primary_is_safe() {
        let mut inspect = cf_inspection("CNAME", "cms.example.com", true, "strict");
        inspect.records[0].name = "www.example.com".into();
        let result = classify_cloudflare_inspection(
            "cf-1",
            "Cloudflare",
            1,
            "www.example.com",
            inspect,
            &["203.0.113.8".parse::<IpAddr>().unwrap()],
            &[],
            Some("cms.example.com"),
        );
        assert_eq!(result.status, "matched");
        assert!(result.origin_matched);
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
    fn parses_standard_nginx_probe_fields() {
        let probe = parse_remote_caddy_probe(
            "PILOT_CADDY_OS\tlinux\n\
             PILOT_CADDY_PRIVILEGE\troot\n\
             PILOT_CADDY_PORT80\tnginx\n\
             PILOT_CADDY_PORT443\tnginx\n\
             PILOT_NGINX_PATH\t/usr/sbin/nginx\n\
             PILOT_NGINX_VERSION\tnginx/1.24.0\n\
             PILOT_NGINX_SERVICE_EXISTS\t1\n\
             PILOT_NGINX_SERVICE_RUNNING\t1\n\
             PILOT_NGINX_CONFIG\t/etc/nginx/nginx.conf\n\
             PILOT_NGINX_CONFIG_VALID\t1\n\
             PILOT_NGINX_CONF_D_INCLUDED\t1\n\
             PILOT_NGINX_CERTBOT_AVAILABLE\t1\n\
             PILOT_NGINX_SITE_PATH\t/etc/nginx/conf.d/pilot-gcms.conf\n",
        );
        assert_eq!(probe.nginx_path, "/usr/sbin/nginx");
        assert_eq!(probe.nginx_version, "nginx/1.24.0");
        assert!(probe.nginx_service_exists);
        assert!(probe.nginx_service_running);
        assert!(probe.nginx_config_valid);
        assert!(probe.nginx_conf_d_included);
        assert!(probe.nginx_certbot_available);
        assert_eq!(probe.nginx_site_path, "/etc/nginx/conf.d/pilot-gcms.conf");
    }

    #[test]
    fn refuses_to_overwrite_custom_web_servers() {
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
        let occupied = classify_caddy_probe(occupied);
        assert_eq!(occupied.provider, "nginx");
        assert_eq!(occupied.mode, "custom");
        assert!(!occupied.can_auto_configure);

        let mut nginx = base_probe();
        nginx.port_80 = "nginx".into();
        nginx.port_443 = "nginx".into();
        nginx.nginx_path = "/usr/sbin/nginx".into();
        nginx.nginx_version = "nginx/1.24.0".into();
        nginx.nginx_service_exists = true;
        nginx.nginx_service_running = true;
        nginx.nginx_process_running = true;
        nginx.nginx_config_path = "/etc/nginx/nginx.conf".into();
        nginx.nginx_config_valid = true;
        nginx.nginx_conf_d_included = true;
        nginx.nginx_certbot_available = true;
        nginx.nginx_site_path = "/etc/nginx/conf.d/pilot-gcms.conf".into();
        let nginx = classify_caddy_probe(nginx);
        assert_eq!(nginx.provider, "nginx");
        assert_eq!(nginx.mode, "standard");
        assert!(nginx.can_auto_configure);

        let mut nginx_without_certbot = base_probe();
        nginx_without_certbot.port_80 = "nginx".into();
        nginx_without_certbot.nginx_path = "/usr/sbin/nginx".into();
        nginx_without_certbot.nginx_service_exists = true;
        nginx_without_certbot.nginx_service_running = true;
        nginx_without_certbot.nginx_config_path = "/etc/nginx/nginx.conf".into();
        nginx_without_certbot.nginx_config_valid = true;
        nginx_without_certbot.nginx_conf_d_included = true;
        nginx_without_certbot.nginx_site_path = "/etc/nginx/conf.d/pilot-gcms.conf".into();
        let nginx_without_certbot = classify_caddy_probe(nginx_without_certbot);
        assert_eq!(nginx_without_certbot.mode, "unsupported");
        assert!(!nginx_without_certbot.can_auto_configure);

        let mut inactive_nginx = base_probe();
        inactive_nginx.port_80 = "nginx".into();
        inactive_nginx.nginx_path = "/usr/sbin/nginx".into();
        inactive_nginx.nginx_service_exists = true;
        inactive_nginx.nginx_process_running = true;
        inactive_nginx.nginx_config_path = "/etc/nginx/nginx.conf".into();
        inactive_nginx.nginx_config_valid = true;
        inactive_nginx.nginx_conf_d_included = true;
        inactive_nginx.nginx_certbot_available = true;
        inactive_nginx.nginx_site_path = "/etc/nginx/conf.d/pilot-gcms.conf".into();
        let inactive_nginx = classify_caddy_probe(inactive_nginx);
        assert_eq!(inactive_nginx.mode, "custom");
        assert!(!inactive_nginx.can_auto_configure);

        let mut mixed_nginx = base_probe();
        mixed_nginx.port_80 = "nginx".into();
        mixed_nginx.port_443 = "apache2".into();
        mixed_nginx.nginx_path = "/usr/sbin/nginx".into();
        mixed_nginx.nginx_service_exists = true;
        mixed_nginx.nginx_service_running = true;
        mixed_nginx.nginx_config_path = "/etc/nginx/nginx.conf".into();
        mixed_nginx.nginx_config_valid = true;
        mixed_nginx.nginx_conf_d_included = true;
        mixed_nginx.nginx_certbot_available = true;
        mixed_nginx.nginx_site_path = "/etc/nginx/conf.d/pilot-gcms.conf".into();
        assert_eq!(classify_caddy_probe(mixed_nginx).mode, "conflict");

        let mut nginx_domain_conflict = base_probe();
        nginx_domain_conflict.port_80 = "nginx".into();
        nginx_domain_conflict.nginx_path = "/usr/sbin/nginx".into();
        nginx_domain_conflict.nginx_service_exists = true;
        nginx_domain_conflict.nginx_service_running = true;
        nginx_domain_conflict.nginx_config_path = "/etc/nginx/nginx.conf".into();
        nginx_domain_conflict.nginx_config_valid = true;
        nginx_domain_conflict.nginx_conf_d_included = true;
        nginx_domain_conflict.nginx_certbot_available = true;
        nginx_domain_conflict.nginx_site_path = "/etc/nginx/conf.d/pilot-gcms.conf".into();
        nginx_domain_conflict.nginx_domain_files =
            vec!["/etc/nginx/sites-enabled/existing.conf".into()];
        let nginx_domain_conflict = classify_caddy_probe(nginx_domain_conflict);
        assert_eq!(nginx_domain_conflict.mode, "conflict");
        assert_eq!(
            nginx_domain_conflict.domain_conflicts,
            vec!["/etc/nginx/sites-enabled/existing.conf"]
        );

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

    #[test]
    fn migration_registry_round_trips_and_validates_instance_scope() {
        let stamp = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_nanos();
        let data_dir = std::env::temp_dir().join(format!(
            "gcms-pilot-migration-registry-{}-{stamp}",
            std::process::id()
        ));
        fs::create_dir_all(&data_dir).unwrap();
        let mut instance = GcmsMigrationSnapshot {
            id: "gcms-test".into(),
            target_id: "target-one".into(),
            source_id: "source-one".into(),
            source_name: "Source".into(),
            instance_path: "/opt/gcms-instances/gcms-test".into(),
            port: 18080,
            base_url: "https://cms.example.com".into(),
            access_configured: true,
            https_ok: true,
            cloudflare_proxy_applicable: true,
            cloudflare_proxied: true,
            service_name: "pilot-gcms-test".into(),
            created_at: 1,
            updated_at: 1,
            ..Default::default()
        };
        upsert_migration_instance(&data_dir, instance.clone()).unwrap();
        instance.running = true;
        instance.updated_at = 2;
        upsert_migration_instance(&data_dir, instance.clone()).unwrap();

        assert_eq!(read_migration_registry(&data_dir), vec![instance.clone()]);
        assert_eq!(
            migration_instance_for_request(
                &data_dir,
                Some("gcms-test"),
                "target-one",
                Some("/opt/gcms-instances/gcms-test")
            )
            .unwrap(),
            Some(instance)
        );
        assert!(
            migration_instance_for_request(&data_dir, Some("gcms-test"), "target-two", None)
                .is_err()
        );
        let _ = fs::remove_dir_all(data_dir);
    }

    #[test]
    fn migration_registry_keeps_legacy_entries_without_access_fields() {
        let stamp = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_nanos();
        let data_dir = std::env::temp_dir().join(format!(
            "gcms-pilot-legacy-migration-registry-{}-{stamp}",
            std::process::id()
        ));
        fs::create_dir_all(&data_dir).unwrap();
        fs::write(
            migration_registry_path(&data_dir),
            br#"[{"id":"legacy","target_id":"target","source_id":"source","source_name":"Legacy","version":"v1","bytes":1,"instance_path":"/opt/gcms-instances/legacy","port":18080,"base_url":"https://old.example.com","redirect_domain":"","service_name":"pilot-gcms-legacy","service_installed":true,"running":true,"created_at":1,"updated_at":1,"last_error":""}]"#,
        )
        .unwrap();

        let instances = read_migration_registry(&data_dir);
        assert_eq!(instances.len(), 1);
        assert_eq!(instances[0].id, "legacy");
        assert_eq!(instances[0].source_base_url, "https://old.example.com");
        assert!(instances[0].base_url.is_empty());
        assert!(!instances[0].access_configured);
        let _ = fs::remove_dir_all(data_dir);
    }

    #[cfg(not(target_os = "windows"))]
    #[test]
    fn remote_shell_scripts_pass_syntax_check() {
        for script in [
            GCMS_REMOTE_PROBE_CMD,
            GCMS_REMOTE_PUBLIC_IP_CMD,
            GCMS_REMOTE_SERVICE_ACTION_CMD,
            GCMS_CADDY_PREFLIGHT_CMD,
            GCMS_CADDY_CONFIGURE_CMD,
            GCMS_NGINX_CONFIGURE_CMD,
            GCMS_REMOTE_RELOAD_DOMAIN_CMD,
            GCMS_MIGRATION_TARGET_ENV_CMD,
            GCMS_MIGRATION_STAGE_CMD,
            GCMS_MIGRATION_RESTORE_CMD,
            GCMS_MIGRATION_SERVICE_CMD,
        ] {
            let status = std::process::Command::new("sh")
                .args(["-n", "-c", script])
                .status()
                .unwrap();
            assert!(status.success());
        }
        assert!(GCMS_CADDY_CONFIGURE_CMD.contains("redir https://%s{uri} 301"));
        assert!(GCMS_CADDY_CONFIGURE_CMD.contains("PILOT_REDIRECT_DOMAIN"));
        assert!(GCMS_NGINX_CONFIGURE_CMD.contains("nginx -t"));
        assert!(GCMS_NGINX_CONFIGURE_CMD.contains("Managed by GCMS Pilot"));
        assert!(GCMS_NGINX_CONFIGURE_CMD.contains("certbot certonly --webroot"));
        assert!(GCMS_NGINX_CONFIGURE_CMD.contains("restore_before"));
    }

    #[cfg(not(target_os = "windows"))]
    #[test]
    fn remote_domain_reload_ignores_inherited_base_url() {
        use std::os::unix::fs::PermissionsExt;

        let stamp = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let base = std::env::temp_dir().join(format!(
            "gcms-pilot-domain-reload-{}-{stamp}",
            std::process::id()
        ));
        let root = base.join("gcms");
        let fake_bin = base.join("bin");
        let cms_bin = root.join("current/bin/cms");
        for path in [
            root.join("scripts"),
            root.join("current/bin"),
            root.join("shared"),
            root.join("run"),
            fake_bin.clone(),
        ] {
            std::fs::create_dir_all(path).unwrap();
        }
        let cms_script = root.join("scripts/cms.sh");
        std::fs::write(
            &cms_script,
            r#"#!/bin/sh
root=$(cd "$(dirname "$0")/.." && pwd)
base=${BASE_URL:-$(sed -n 's/^BASE_URL=//p' "$root/shared/cms.conf")}
printf '%s' "$base" > "$root/run/seen-base-url"
exit 0
"#,
        )
        .unwrap();
        std::fs::write(&cms_bin, "#!/bin/sh\nexit 0\n").unwrap();
        std::fs::write(
            root.join("shared/cms.conf"),
            "ADDR=127.0.0.1:18080\nBASE_URL=http://localhost:8080\n",
        )
        .unwrap();
        let fake_curl = fake_bin.join("curl");
        std::fs::write(&fake_curl, "#!/bin/sh\nprintf '200'\n").unwrap();
        for path in [&cms_script, &cms_bin, &fake_curl] {
            std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o755)).unwrap();
        }

        let output = std::process::Command::new("sh")
            .args(["-c", GCMS_REMOTE_RELOAD_DOMAIN_CMD])
            .env("PILOT_GCMS_HOME", &root)
            .env("PILOT_DOMAIN", "new.example.test")
            .env("BASE_URL", "http://stale.example.test")
            .env(
                "PATH",
                format!(
                    "{}:{}",
                    fake_bin.display(),
                    std::env::var("PATH").unwrap_or_default()
                ),
            )
            .output()
            .unwrap();
        assert!(
            output.status.success(),
            "stdout={} stderr={}",
            String::from_utf8_lossy(&output.stdout),
            String::from_utf8_lossy(&output.stderr)
        );
        assert_eq!(
            std::fs::read_to_string(root.join("run/seen-base-url")).unwrap(),
            "https://new.example.test"
        );
        assert!(std::fs::read_to_string(root.join("shared/cms.conf"))
            .unwrap()
            .contains("BASE_URL=https://new.example.test"));
        let _ = std::fs::remove_dir_all(base);
    }
}
