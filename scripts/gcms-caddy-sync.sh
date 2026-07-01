#!/bin/sh
# gcms-caddy-sync.sh — 把 gcms 后台绑定的站点同步成 Caddy 的“每站一文件”配置并安全重载。
#
# 需以 root / sudo 运行（要写 /etc/caddy 并重载 Caddy）。gcms 本身不写任何 Caddy 文件：
# 它只在本机 loopback 上提供 /internal/caddy/config（一份清单），由本脚本落盘与重载。
#
# 每个站点写一个独立文件 conf.d/gcms-<主域名>.caddy：
#   - 安装产物 conf.d/gcms.caddy（无横杠）绝不碰；本脚本只动 gcms-*.caddy（有横杠）。
#   - 后台已解绑 / 改过主域名的旧文件（孤儿）会被清掉，保持与后台一致（对账）。
# 安全性：落盘前逐个校验、原子写、备份整组 gcms-*.caddy，任何校验/重载失败都整组回滚。
#
# 用法：
#   sudo sh scripts/gcms-caddy-sync.sh          # 绑定/改域名后运行；也可挂 cron / systemd timer
#
# 可选环境变量：
#   ADDR            gcms 监听地址（默认读 <GCMS_HOME>/shared/cms.conf，回退 127.0.0.1:8080）
#   CADDY_CONF_DIR  站点文件目录（默认 /etc/caddy/conf.d）
#   CADDYFILE       主 Caddyfile（默认 /etc/caddy/Caddyfile）
#   GCMS_HOME       gcms 安装目录（默认取脚本所在目录的上级）
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "错误：请以 root / sudo 运行（需要写 /etc/caddy 并重载 Caddy）。" >&2
  exit 1
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
GCMS_HOME="${GCMS_HOME:-$(dirname "$script_dir")}"

# ADDR：优先环境变量，其次 cms.conf（去掉行内 # 注释、引号、首尾空白）。
conf="$GCMS_HOME/shared/cms.conf"
[ -f "$conf" ] || conf="$GCMS_HOME/scripts/cms.conf"
if [ -z "${ADDR:-}" ] && [ -f "$conf" ]; then
  ADDR=$(sed -n 's/^[[:space:]]*ADDR[[:space:]]*=[[:space:]]*//p' "$conf" | head -1 \
    | sed 's/[[:space:]]*#.*$//; s/[[:space:]]*$//' | tr -d '"')
fi
ADDR="${ADDR:-127.0.0.1:8080}"
# 端点仅 loopback 可访问：无论 gcms 绑哪个地址，都只取端口、固定用 127.0.0.1 抓取。
port=$(printf '%s' "$ADDR" | sed 's/.*://')
[ -n "$port" ] || port=8080
url="http://127.0.0.1:$port/internal/caddy/config"

CONF_DIR="${CADDY_CONF_DIR:-/etc/caddy/conf.d}"
CADDYFILE="${CADDYFILE:-/etc/caddy/Caddyfile}"

tmp=""; stage=""; backup=""
cleanup() {
  rm -f "$tmp" 2>/dev/null || true
  rm -rf "$stage" "$backup" 2>/dev/null || true
  rm -f "$CONF_DIR"/gcms-*.caddy.tmp.* 2>/dev/null || true
}
trap cleanup EXIT

tmp=$(mktemp)
stage=$(mktemp -d)
backup=$(mktemp -d)

# 1) 拉取清单
if ! curl -fsS "$url" -o "$tmp"; then
  echo "错误：无法从 gcms 获取配置清单（$url）。gcms 是否在运行、端口是否正确？" >&2
  exit 1
fi
# 2) 必须是 gcms 清单（防止 ADDR 指向别的服务）
if ! head -1 "$tmp" | grep -q '^# gcms-caddy-manifest'; then
  echo "错误：返回内容不是 gcms 配置清单（ADDR 是否指向了别的服务？）。未改动 Caddy。" >&2
  exit 1
fi

# 3) 把清单拆成每站一个文件到暂存目录（文件名由 === gcms-<host>.caddy === 决定，正则限定安全字符）
awk -v dir="$stage" '
  /^=== gcms-[A-Za-z0-9.-]+\.caddy ===$/ { out = dir "/" $2; next }
  out != "" { print >> out }
' "$tmp"

# 3b) 用清单头声明的站点数对账：识破"格式漂移/截断导致解析出 0 个站点"的情况——
#     否则下面的孤儿清理会把所有站点文件误当孤儿删光。数量一致（含 0=0 合法清空）才继续。
want=$(head -1 "$tmp" | sed -n 's/^# gcms-caddy-manifest v1 sites=\([0-9][0-9]*\).*/\1/p')
got=$(find "$stage" -maxdepth 1 -name 'gcms-*.caddy' 2>/dev/null | wc -l | tr -d ' ')
if [ -z "$want" ] || [ "$want" != "$got" ]; then
  echo "错误：清单解析异常（头部声明 ${want:-未知} 个站点，实际解析出 $got 个）。未改动 Caddy。" >&2
  exit 1
fi

# 4) 落盘前逐个校验语法——任何一个不合法就绝不碰 /etc/caddy
if command -v caddy >/dev/null 2>&1; then
  for f in "$stage"/gcms-*.caddy; do
    [ -e "$f" ] || continue
    if ! caddy validate --config "$f" --adapter caddyfile >/dev/null 2>&1; then
      echo "错误：站点配置 $(basename "$f") 语法校验未通过。未改动 Caddy。" >&2
      exit 1
    fi
  done
fi

mkdir -p "$CONF_DIR"

# 提醒：主 Caddyfile 需 import conf.d，否则同步的文件不生效。
if [ -f "$CADDYFILE" ] && ! grep -qE 'import[[:space:]]+/etc/caddy/conf\.d/\*' "$CADDYFILE"; then
  echo "警告：$CADDYFILE 未发现 \`import /etc/caddy/conf.d/*.caddy\`，同步的站点文件可能不生效。" >&2
fi

# 5) 备份当前整组 gcms-*.caddy（有横杠，永不含安装文件 gcms.caddy），供整组回滚
for f in "$CONF_DIR"/gcms-*.caddy; do
  [ -e "$f" ] || continue
  cp "$f" "$backup/"
done

restore() {
  # 整组回滚：清掉当前 gcms-*.caddy，放回备份的那组。永不触碰 gcms.caddy。
  for f in "$CONF_DIR"/gcms-*.caddy; do [ -e "$f" ] && rm -f "$f"; done
  for f in "$backup"/gcms-*.caddy; do [ -e "$f" ] && cp "$f" "$CONF_DIR/"; done
}

reload_caddy() {
  if command -v systemctl >/dev/null 2>&1 && systemctl reload caddy >/dev/null 2>&1; then
    return 0
  fi
  if command -v caddy >/dev/null 2>&1 && [ -f "$CADDYFILE" ] \
    && caddy reload --config "$CADDYFILE" --adapter caddyfile >/dev/null 2>&1; then
    return 0
  fi
  return 1
}

# 6) 应用：把暂存的每站文件原子写入 conf.d，并删掉后台已不存在的孤儿 gcms-*.caddy
wrote=0
for f in "$stage"/gcms-*.caddy; do
  [ -e "$f" ] || continue
  name=$(basename "$f")
  cp "$f" "$CONF_DIR/$name.tmp.$$"
  mv "$CONF_DIR/$name.tmp.$$" "$CONF_DIR/$name"
  wrote=$((wrote + 1))
done
removed=0
for f in "$CONF_DIR"/gcms-*.caddy; do
  [ -e "$f" ] || continue
  name=$(basename "$f")
  if [ ! -e "$stage/$name" ]; then
    rm -f "$f"
    removed=$((removed + 1))
  fi
done

# 7) 装配后再校验主 Caddyfile（捕捉跨站冲突，如域名与其它站点重复），失败整组回滚
if command -v caddy >/dev/null 2>&1 && [ -f "$CADDYFILE" ]; then
  if ! caddy validate --config "$CADDYFILE" --adapter caddyfile >/dev/null 2>&1; then
    echo "错误：写入后主 Caddyfile 校验未通过（域名是否与其它站点冲突？）。" >&2
    restore
    reload_caddy || true
    echo "  已整组回滚到原配置。" >&2
    exit 1
  fi
fi

# 8) 重载，失败也整组回滚
if ! reload_caddy; then
  echo "错误：重载 Caddy 失败。" >&2
  restore
  reload_caddy || true
  echo "  已整组回滚到原配置。" >&2
  exit 1
fi

echo "完成：已同步 $wrote 个站点文件到 $CONF_DIR，清理 $removed 个孤儿，并重载 Caddy。"
