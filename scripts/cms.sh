#!/usr/bin/env sh
# =============================================================================
# CCVAR 简记 CMS —— 启停脚本（macOS / Linux）
#
#   用法：  ./scripts/cms.sh <命令>
#   命令：  start | stop | restart | status | build | logs
#
#   start 会自动检查 Go 环境：本机已装且 >= 1.23 直接用；否则自动下载官方
#         Go 工具链到项目内 .go/ 目录（不污染系统），构建后台台运行。
#
#   可用环境变量（可在命令前覆盖）：
#     ADDR=:9090            监听地址（默认 :8080）
#     BASE_URL=https://...  站点绝对地址（默认 http://localhost<ADDR>）
#     CMS_DB=/path/cms.db   数据库路径（默认 data/cms.db）
#     GO_VERSION=1.23.4     需要自动安装时下载的 Go 版本
# =============================================================================
set -eu

# ---- 路径 ----
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$SCRIPT_DIR/.." && pwd)
BIN="$ROOT/bin/cms"
RUNDIR="$ROOT/run"
PIDFILE="$RUNDIR/cms.pid"
LOGFILE="$RUNDIR/cms.log"
GOROOT_LOCAL="$ROOT/.go/go"
CONF="$SCRIPT_DIR/cms.conf"

# ---- 读取配置文件（仅已知键；命令行环境变量优先，已设置则不覆盖）----
load_conf() {
  [ -f "$CONF" ] || return 0
  while IFS='=' read -r k v; do
    k=$(printf '%s' "$k" | tr -d '[:space:]')
    case "$k" in ''|\#*) continue ;; esac
    v=$(printf '%s' "$v" | sed 's/[[:space:]]*#.*$//; s/^[[:space:]]*//; s/[[:space:]]*$//')
    case "$k" in
      ADDR)       [ -n "${ADDR:-}" ]       || ADDR="$v" ;;
      BASE_URL)   [ -n "${BASE_URL:-}" ]   || BASE_URL="$v" ;;
      CMS_DB)     [ -n "${CMS_DB:-}" ]     || CMS_DB="$v" ;;
      GO_VERSION) [ -n "${GO_VERSION:-}" ] || GO_VERSION="$v" ;;
    esac
  done < "$CONF"
}
load_conf

# 配置文件与命令行都未提供时的最终兜底默认
ADDR=${ADDR:-:8080}
GO_VERSION=${GO_VERSION:-1.23.4}

# ---- 颜色（终端支持时）----
if [ -t 1 ]; then C_OK='\033[32m'; C_ERR='\033[31m'; C_DIM='\033[2m'; C_0='\033[0m'; else C_OK=; C_ERR=; C_DIM=; C_0=; fi
info()  { printf "%b\n" "${C_DIM}» $*${C_0}"; }
ok()    { printf "%b\n" "${C_OK}✓ $*${C_0}"; }
err()   { printf "%b\n" "${C_ERR}✗ $*${C_0}" >&2; }

# ---- Go 环境：本机达标则用之，否则下载到 .go/ ----
go_ok() {
  command -v go >/dev/null 2>&1 || return 1
  # 取 go1.XX，比较次版本号 >= 23
  v=$(go env GOVERSION 2>/dev/null | sed -e 's/^go//' -e 's/\.[0-9][0-9]*$//' -e 's/[a-z].*$//')
  major=${v%%.*}; minor=${v#*.}
  [ "${major:-0}" -gt 1 ] 2>/dev/null && return 0
  [ "${major:-0}" -eq 1 ] 2>/dev/null && [ "${minor:-0}" -ge 23 ] 2>/dev/null && return 0
  return 1
}

ensure_go() {
  if go_ok; then info "Go: $(go env GOVERSION) （系统）"; return; fi
  if [ -x "$GOROOT_LOCAL/bin/go" ]; then
    export PATH="$GOROOT_LOCAL/bin:$PATH"; export GOROOT="$GOROOT_LOCAL"
    if go_ok; then info "Go: $(go env GOVERSION) （项目内 .go/）"; return; fi
  fi
  info "未检测到合适的 Go（需 >= 1.23），开始自动安装 go${GO_VERSION} 到 .go/ …"
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m)
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    armv6l|armv7l) arch=armv6l ;;
    *) err "不支持的 CPU 架构：$arch，请手动安装 Go：https://go.dev/dl/"; exit 1 ;;
  esac
  case "$os" in linux|darwin) ;; *) err "请手动安装 Go：https://go.dev/dl/"; exit 1 ;; esac
  url="https://go.dev/dl/go${GO_VERSION}.${os}-${arch}.tar.gz"
  mkdir -p "$ROOT/.go"
  info "下载 $url"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" | tar -xz -C "$ROOT/.go"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO- "$url" | tar -xz -C "$ROOT/.go"
  else
    err "需要 curl 或 wget 才能自动安装 Go"; exit 1
  fi
  export PATH="$GOROOT_LOCAL/bin:$PATH"; export GOROOT="$GOROOT_LOCAL"
  go_ok && ok "已安装 $(go env GOVERSION) 到 .go/" || { err "Go 安装失败"; exit 1; }
}

base_url() {
  if [ -n "${BASE_URL:-}" ]; then printf '%s' "$BASE_URL"; return; fi
  case "$ADDR" in
    :*) printf 'http://localhost%s' "$ADDR" ;;
    *)  printf 'http://%s' "$ADDR" ;;
  esac
}

build() {
  ensure_go
  info "构建 → $BIN"
  ( cd "$ROOT" && go build -o "$BIN" . )
  ok "构建完成"
}

running() { [ -f "$PIDFILE" ] && kill -0 "$(cat "$PIDFILE" 2>/dev/null)" 2>/dev/null; }

start() {
  if running; then ok "已在运行（PID $(cat "$PIDFILE")） → $(base_url)"; return; fi
  # 仅在「尚无已编译二进制」时编译；已编译则直接运行，不重复编译（改代码请用 build）
  if [ -x "$BIN" ]; then
    info "使用已编译二进制：$BIN（如已改动代码，请先运行：$0 build）"
  else
    info "未发现已编译二进制，开始首次编译 …"
    build
  fi
  mkdir -p "$RUNDIR"
  info "启动服务 …"
  # 通过导出环境变量传参（子进程继承），以 ROOT 为工作目录保证 data/ 相对路径正确
  export ADDR
  export BASE_URL="$(base_url)"
  [ -n "${CMS_DB:-}" ] && export CMS_DB
  cd "$ROOT"
  # 单 > 截断日志：本次启动只保留本次运行日志，不混入历史
  # 直接后台运行二进制并记录其真实 PID（nohup 会 exec 二进制，PID 不变）；脱离终端
  nohup "$BIN" >"$LOGFILE" 2>&1 </dev/null &
  echo $! >"$PIDFILE"
  sleep 1
  if running; then
    ok "已启动 → $(base_url)   后台 $(base_url)/admin"
    info "PID $(cat "$PIDFILE")  ·  日志 $LOGFILE"
  else
    err "启动失败，请查看日志：$LOGFILE"; tail -n 20 "$LOGFILE" 2>/dev/null || true; exit 1
  fi
}

stop() {
  if running; then
    pid=$(cat "$PIDFILE")
    kill "$pid" 2>/dev/null || true
    # 等待退出，必要时强杀
    i=0; while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 10 ]; do sleep 0.3; i=$((i+1)); done
    kill -0 "$pid" 2>/dev/null && kill -9 "$pid" 2>/dev/null || true
    rm -f "$PIDFILE"
    ok "已停止（PID $pid）"
  else
    rm -f "$PIDFILE" 2>/dev/null || true
    info "服务未在运行"
  fi
}

status() {
  if running; then ok "运行中（PID $(cat "$PIDFILE")） → $(base_url)"; else info "未运行"; fi
}

logs() { mkdir -p "$RUNDIR"; touch "$LOGFILE"; tail -n 80 -f "$LOGFILE"; }

usage() {
  cat <<EOF
CCVAR 简记 CMS · 启停脚本（macOS / Linux）

用法：  $0 <命令>

命令：
  start     启动服务。未编译过则先自动编译（含按需安装 Go），已编译则直接运行、不重复编译。
  stop      停止服务（按 PID 文件结束进程并释放端口）。
  restart   重启服务（= 先 stop 再 start）。改了代码请先 build 再 restart。
  status    查看运行状态（PID 与访问地址）。
  build     （重新）编译为 bin/cms。唯一会强制重新编译的命令。
  logs      实时跟踪「本次运行」日志（Ctrl-C 退出）。
  help      显示本帮助（无参数时同样显示）。

说明：
  · 仅 build、以及「尚无二进制时的 start」会触发编译；其余命令不编译。
  · 每次 start 会清空旧日志，只保留本次运行日志（run/cms.log）。

配置：默认读取 scripts/cms.conf（KEY=VALUE）。优先级：命令行环境变量 > 配置文件 > 内置默认。
环境变量（在命令前覆盖，优先级最高）：
  ADDR=:9090                监听地址（默认 :8080）
  BASE_URL=https://example  站点绝对地址（默认 http://localhost<ADDR>）
  CMS_DB=/path/cms.db       数据库路径（默认 data/cms.db）
  GO_VERSION=1.23.4         需自动安装 Go 时下载的版本
EOF
}

case "${1:-}" in
  start)   start ;;
  stop)    stop ;;
  restart) stop; start ;;
  status)  status ;;
  build)   build ;;
  logs)    logs ;;
  ""|help|-h|--help) usage ;;
  *) printf "未知命令：%s\n\n" "$1"; usage; exit 2 ;;
esac
