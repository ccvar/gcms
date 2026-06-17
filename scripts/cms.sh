#!/usr/bin/env sh
# =============================================================================
# CCVAR 简记 CMS —— 启停脚本（macOS / Linux）
#
#   用法：  ./scripts/cms.sh <命令>
#   命令：  start | stop | restart | status | build | logs | upgrade | upgrade-status
#
#   start 会自动检查 Go 环境：本机已装且 >= 1.23 直接用；否则自动下载官方
#         Go 工具链到项目内 .go/ 目录（不污染系统），构建后台台运行。
#
#   可用环境变量（可在命令前覆盖）：
#     ADDR=:9090            监听地址（默认 :8080）
#     BASE_URL=https://...  站点绝对地址（默认 http://localhost<ADDR>）
#     CMS_DB=/path/cms.db   数据库路径（发布包默认 shared/data/cms.db，源码模式默认 data/cms.db）
#     GO_VERSION=1.23.4     需要自动安装时下载的 Go 版本
# =============================================================================
set -eu

# ---- 路径 ----
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$SCRIPT_DIR/.." && pwd)
RUNDIR="$ROOT/run"
LOGDIR="$ROOT/logs"
PIDFILE="$RUNDIR/cms.pid"
LOGFILE="$LOGDIR/cms.log"
UPGRADE_STATUS="$RUNDIR/upgrade.json"
GOROOT_LOCAL="$ROOT/.go/go"
CURRENT="$ROOT/current"

if [ -x "$CURRENT/bin/cms" ]; then
  BIN="$CURRENT/bin/cms"
  BUILD_INFO="$CURRENT/BUILD_INFO"
  DEFAULT_CMS_DB="shared/data/cms.db"
  CONF="$ROOT/shared/cms.conf"
  [ -f "$CONF" ] || CONF="$SCRIPT_DIR/cms.conf"
else
  BIN="$ROOT/bin/cms"
  BUILD_INFO="$ROOT/BUILD_INFO"
  DEFAULT_CMS_DB="data/cms.db"
  CONF="$SCRIPT_DIR/cms.conf"
fi

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
CMS_DB=${CMS_DB:-$DEFAULT_CMS_DB}

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
    *) err "不支持的 CPU 架构：${arch}，请手动安装 Go：https://go.dev/dl/"; exit 1 ;;
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

runtime_platform() {
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m)
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    armv6l|armv7l) arch=armv6l ;;
  esac
  printf '%s/%s' "$os" "$arch"
}

build_info_value() {
  [ -f "$BUILD_INFO" ] || return 0
  awk -F= -v key="$1" '$1 == key { print $2; exit }' "$BUILD_INFO"
}

check_binary_platform() {
  [ -f "$BUILD_INFO" ] || return 0
  target_os=$(build_info_value GOOS)
  target_arch=$(build_info_value GOARCH)
  [ -n "$target_os" ] && [ -n "$target_arch" ] || return 0

  current=$(runtime_platform)
  current_os=${current%/*}
  current_arch=${current#*/}
  if [ "$target_os" != "$current_os" ] || [ "$target_arch" != "$current_arch" ]; then
    err "发布包平台不匹配：当前包是 ${target_os}/${target_arch}，当前系统是 ${current_os}/${current_arch}"
    err "请下载对应平台的发布包，或在源码仓库重新打包：./scripts/package.sh $current_os $current_arch"
    exit 1
  fi
}

build() {
  if [ ! -f "$ROOT/go.mod" ]; then
    err "当前是二进制发布包，不包含源码，无法 build。请下载新版发布包或在源码仓库中构建。"
    exit 1
  fi
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
    info "使用已编译二进制：${BIN}（如已改动代码，请先运行：${0} build）"
    check_binary_platform
  else
    info "未发现已编译二进制，开始首次编译 …"
    build
  fi
  mkdir -p "$RUNDIR" "$LOGDIR"
  info "启动服务 …"
  # 通过导出环境变量传参（子进程继承），以 ROOT 为工作目录保证相对路径正确
  export ADDR
  export BASE_URL="$(base_url)"
  export CMS_DB
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
    err "启动失败，请查看日志：$LOGFILE"; tail -n 20 "$LOGFILE" 2>/dev/null || true
    [ "${START_RETURN_ON_FAIL:-}" = "1" ] && return 1
    exit 1
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
    ok "已停止（PID ${pid}）"
  else
    rm -f "$PIDFILE" 2>/dev/null || true
    info "服务未在运行"
  fi
}

status() {
  if running; then ok "运行中（PID $(cat "$PIDFILE")） → $(base_url)"; else info "未运行"; fi
}

logs() { mkdir -p "$LOGDIR"; touch "$LOGFILE"; tail -n 80 -f "$LOGFILE"; }

json_escape() {
  sed 's/\\/\\\\/g; s/"/\\"/g'
}

write_upgrade_status() {
  st=$1
  step=$2
  ver=$3
  msg=$4
  mkdir -p "$RUNDIR"
  esc_msg=$(printf '%s' "$msg" | json_escape)
  esc_ver=$(printf '%s' "$ver" | json_escape)
  esc_step=$(printf '%s' "$step" | json_escape)
  ts=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
  cat > "$UPGRADE_STATUS" <<EOF
{"status":"$st","step":"$esc_step","version":"$esc_ver","message":"$esc_msg","updated_at":"$ts"}
EOF
}

upgrade_status() {
  if [ -f "$UPGRADE_STATUS" ]; then
    cat "$UPGRADE_STATUS"
  else
    printf '%s\n' '{"status":"idle","step":"","version":"","message":"暂无升级任务","updated_at":""}'
  fi
}

fail_upgrade() {
  msg=$1
  ver=${2:-}
  step=${3:-failed}
  write_upgrade_status failed "$step" "$ver" "$msg"
  err "$msg"
  exit 1
}

need_release_layout() {
  [ -x "$CURRENT/bin/cms" ] || fail_upgrade "当前目录不是标准二进制发布包，未找到 current/bin/cms"
  [ -L "$CURRENT" ] || fail_upgrade "当前 current 不是软链，cms.sh upgrade 仅支持 Linux/macOS 标准发布包"
  mkdir -p "$ROOT/releases" "$ROOT/tmp" "$ROOT/backups" "$RUNDIR" "$LOGDIR"
}

need_python() {
  command -v python3 >/dev/null 2>&1 || fail_upgrade "升级需要 python3 解析 manifest.json，请先安装 python3"
}

download_file() {
  url=$1
  out=$2
  token=${GCMS_UPDATE_TOKEN:-${GITHUB_TOKEN:-}}
  if command -v curl >/dev/null 2>&1; then
    if [ -n "$token" ]; then
      curl -fsSL --retry 3 --connect-timeout 10 -H "Authorization: Bearer $token" -o "$out" "$url"
    else
      curl -fsSL --retry 3 --connect-timeout 10 -o "$out" "$url"
    fi
  elif command -v wget >/dev/null 2>&1; then
    if [ -n "$token" ]; then
      wget --header="Authorization: Bearer $token" -O "$out" "$url"
    else
      wget -O "$out" "$url"
    fi
  else
    return 1
  fi
}

http_ok() {
  url=$1
  if command -v curl >/dev/null 2>&1; then
    curl -fsS --connect-timeout 2 --max-time 5 "$url" >/dev/null 2>&1
  elif command -v wget >/dev/null 2>&1; then
    wget -q -T 5 -O /dev/null "$url" >/dev/null 2>&1
  else
    return 1
  fi
}

calc_sha256() {
  file=$1
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$file" | awk '{print $NF}'
  else
    return 1
  fi
}

release_public_key_file() {
  if [ -n "${GCMS_UPDATE_PUBLIC_KEY:-}" ]; then
    [ -f "$GCMS_UPDATE_PUBLIC_KEY" ] && { printf '%s' "$GCMS_UPDATE_PUBLIC_KEY"; return 0; }
    return 2
  fi
  for f in "$ROOT/shared/update-public.pem" "$ROOT/scripts/update-public.pem" "$SCRIPT_DIR/update-public.pem"; do
    [ -n "$f" ] && [ -f "$f" ] && { printf '%s' "$f"; return 0; }
  done
  return 1
}

manifest_signature_url() {
  url=$1
  printf '%s.sig' "$url"
}

update_signature_required() {
  case "${GCMS_UPDATE_REQUIRE_SIGNATURE:-1}" in
    0|false|FALSE|no|NO) return 1 ;;
    *) return 0 ;;
  esac
}

verify_manifest_signature() {
  manifest_url_value=$1
  manifest=$2
  work=$3
  key_status=0
  pub=$(release_public_key_file) || key_status=$?
  if [ "$key_status" = "2" ]; then
    fail_upgrade "GCMS_UPDATE_PUBLIC_KEY 指向的公钥文件不存在：$GCMS_UPDATE_PUBLIC_KEY" "" verify
  fi
  if [ "$key_status" != "0" ] || [ -z "$pub" ]; then
    if update_signature_required; then
      fail_upgrade "未找到更新验签公钥，已停止升级。标准发布包应包含 scripts/update-public.pem；如需临时跳过验签，请显式设置 GCMS_UPDATE_REQUIRE_SIGNATURE=0。" "" verify
    fi
    info "已显式关闭 manifest 签名要求，跳过签名校验（仍会校验 SHA256）"
    return 0
  fi
  command -v openssl >/dev/null 2>&1 || fail_upgrade "已配置更新公钥，但缺少 openssl，无法校验 manifest 签名" "" verify
  sig="$work/manifest.json.sig"
  sig_url=$(manifest_signature_url "$manifest_url_value")
  write_upgrade_status running verify "" "下载并校验更新清单签名"
  info "下载更新清单签名：$sig_url"
  download_file "$sig_url" "$sig" || fail_upgrade "已配置更新公钥，但下载 manifest 签名失败" "" verify
  if ! openssl dgst -sha256 -verify "$pub" -signature "$sig" "$manifest" >/dev/null 2>&1; then
    fail_upgrade "manifest 签名校验失败，已停止升级" "" verify
  fi
  ok "manifest 签名校验通过"
}

validate_tar_paths() {
  file=$1
  tar -tzf "$file" | while IFS= read -r entry; do
    case "$entry" in
      ""|/*|..|../*|*/../*|*/..|*\\*)
        printf '%s\n' "$entry" >&2
        exit 1
        ;;
    esac
  done
}

abs_path() {
  case "$1" in
    /*) printf '%s' "$1" ;;
    *) printf '%s/%s' "$ROOT" "$1" ;;
  esac
}

manifest_url() {
  if [ -n "${GCMS_UPDATE_URL:-}" ]; then
    printf '%s' "$GCMS_UPDATE_URL"
    return
  fi
  repo=${GCMS_RELEASE_REPO:-$(build_info_value RELEASE_REPO)}
  repo=${repo:-ccvar/gcms-releases}
  printf 'https://github.com/%s/releases/latest/download/manifest.json' "$repo"
}

manifest_asset_info() {
  manifest=$1
  os=$2
  arch=$3
  python3 - "$manifest" "$os" "$arch" <<'PY'
import json
import sys

path, goos, goarch = sys.argv[1:4]
with open(path, encoding="utf-8") as f:
    data = json.load(f)
asset = next((a for a in data.get("assets", []) if a.get("os") == goos and a.get("arch") == goarch), None)
if not asset:
    raise SystemExit("manifest 中没有匹配平台 {}/{} 的发布包".format(goos, goarch))
for value in (
    data.get("version", ""),
    asset.get("url", ""),
    asset.get("sha256", ""),
    asset.get("name", ""),
):
    print(value)
PY
}

check_release_platform_file() {
  bi=$1
  [ -f "$bi" ] || return 1
  target_os=$(awk -F= '$1 == "GOOS" { print $2; exit }' "$bi")
  target_arch=$(awk -F= '$1 == "GOARCH" { print $2; exit }' "$bi")
  current=$(runtime_platform)
  current_os=${current%/*}
  current_arch=${current#*/}
  [ "$target_os" = "$current_os" ] && [ "$target_arch" = "$current_arch" ]
}

set_current_ref() {
  ref=$1
  [ -d "$ROOT/$ref" ] || return 1
  next="$ROOT/current.next.$$"
  rm -f "$next"
  ln -s "$ref" "$next" || return 1
  rm -f "$CURRENT" || return 1
  mv "$next" "$CURRENT"
}

backup_db() {
  backup_dir=$1
  db=$(abs_path "$CMS_DB")
  mkdir -p "$backup_dir/db"
  printf '%s\n' "$CMS_DB" > "$backup_dir/CMS_DB_PATH"
  for suffix in "" "-wal" "-shm"; do
    src="${db}${suffix}"
    [ -f "$src" ] && cp -p "$src" "$backup_dir/db/cms.db${suffix}"
  done
}

restore_db() {
  backup_dir=$1
  [ -d "$backup_dir/db" ] || return 0
  db=$(abs_path "$CMS_DB")
  mkdir -p "$(dirname "$db")"
  for suffix in "" "-wal" "-shm"; do
    bak="$backup_dir/db/cms.db${suffix}"
    dest="${db}${suffix}"
    if [ -f "$bak" ]; then
      cp -p "$bak" "$dest"
    else
      rm -f "$dest"
    fi
  done
}

backup_root_files() {
  backup_dir=$1
  mkdir -p "$backup_dir/root"
  [ -f "$ROOT/BUILD_INFO" ] && cp -p "$ROOT/BUILD_INFO" "$backup_dir/root/BUILD_INFO"
  [ -f "$ROOT/README.txt" ] && cp -p "$ROOT/README.txt" "$backup_dir/root/README.txt"
  if [ -d "$ROOT/scripts" ]; then
    mkdir -p "$backup_dir/root/scripts"
    cp -R "$ROOT/scripts/." "$backup_dir/root/scripts/"
  fi
}

restore_root_files() {
  backup_dir=$1
  [ -d "$backup_dir/root" ] || return 0
  [ -f "$backup_dir/root/BUILD_INFO" ] && cp -p "$backup_dir/root/BUILD_INFO" "$ROOT/BUILD_INFO"
  [ -f "$backup_dir/root/README.txt" ] && cp -p "$backup_dir/root/README.txt" "$ROOT/README.txt"
  if [ -d "$backup_dir/root/scripts" ]; then
    mkdir -p "$ROOT/scripts"
    cp -R "$backup_dir/root/scripts/." "$ROOT/scripts/"
    [ -f "$ROOT/scripts/cms.sh" ] && chmod +x "$ROOT/scripts/cms.sh"
  fi
}

install_root_files() {
  extracted_root=$1
  release_dir=$2
  if [ -d "$extracted_root/scripts" ]; then
    mkdir -p "$ROOT/scripts"
    cp -R "$extracted_root/scripts/." "$ROOT/scripts/"
    [ -f "$ROOT/scripts/cms.sh" ] && chmod +x "$ROOT/scripts/cms.sh"
  fi
  [ -f "$extracted_root/README.txt" ] && cp -p "$extracted_root/README.txt" "$ROOT/README.txt"
  [ -f "$release_dir/BUILD_INFO" ] && cp -p "$release_dir/BUILD_INFO" "$ROOT/BUILD_INFO"
}

wait_http_ready() {
  url=$(base_url)
  i=0
  while [ "$i" -lt 30 ]; do
    http_ok "$url" && return 0
    sleep 1
    i=$((i+1))
  done
  return 1
}

rollback_upgrade() {
  prev_ref=$1
  backup_dir=$2
  was_running=$3
  new_ver=$4
  write_upgrade_status running rollback "$new_ver" "升级失败，正在回滚到 $prev_ref"
  stop || true
  restore_db "$backup_dir"
  restore_root_files "$backup_dir"
  set_current_ref "$prev_ref" || fail_upgrade "回滚失败：无法切回 $prev_ref，请手动检查 current 软链" "$new_ver" rollback
  if [ "$was_running" = "1" ]; then
    START_RETURN_ON_FAIL=1
    if start && wait_http_ready; then
      START_RETURN_ON_FAIL=
      write_upgrade_status failed rollback "$new_ver" "升级失败，已回滚到 $prev_ref"
      fail_upgrade "升级失败，已回滚到 $prev_ref" "$new_ver" rollback
    fi
    START_RETURN_ON_FAIL=
    fail_upgrade "升级失败，且旧版本启动异常，请查看 $LOGFILE" "$new_ver" rollback
  fi
  write_upgrade_status failed rollback "$new_ver" "升级失败，已回滚到 $prev_ref（服务原本未运行）"
  fail_upgrade "升级失败，已回滚到 $prev_ref" "$new_ver" rollback
}

upgrade() {
  target=${1:-}
  need_release_layout
  need_python

  lock="$RUNDIR/upgrade.lock"
  if ! mkdir "$lock" 2>/dev/null; then
    fail_upgrade "已有升级任务正在运行"
  fi
  trap 'rm -rf "$lock"' EXIT

  stamp=$(date -u '+%Y%m%dT%H%M%SZ')
  work="$ROOT/tmp/upgrade-$stamp"
  mkdir -p "$work"

  current_version=$(build_info_value VERSION)
  current_version=${current_version:-unknown}
  current=$(runtime_platform)
  current_os=${current%/*}
  current_arch=${current#*/}

  murl=$(manifest_url)
  manifest="$work/manifest.json"
  write_upgrade_status running manifest "$target" "下载更新清单"
  info "下载更新清单：$murl"
  download_file "$murl" "$manifest" || fail_upgrade "下载更新清单失败" "$target" manifest
  verify_manifest_signature "$murl" "$manifest" "$work"

  parsed=$(manifest_asset_info "$manifest" "$current_os" "$current_arch" 2>"$work/manifest.err") || {
    fail_upgrade "$(cat "$work/manifest.err")" "$target" manifest
  }
  new_version=$(printf '%s\n' "$parsed" | sed -n '1p')
  asset_url=$(printf '%s\n' "$parsed" | sed -n '2p')
  asset_sha=$(printf '%s\n' "$parsed" | sed -n '3p')
  asset_name=$(printf '%s\n' "$parsed" | sed -n '4p')

  [ -n "$new_version" ] || fail_upgrade "manifest 缺少 version" "$target" manifest
  [ -n "$asset_url" ] || fail_upgrade "manifest 缺少当前平台发布包下载 URL" "$new_version" manifest
  [ -n "$asset_sha" ] || fail_upgrade "manifest 缺少当前平台发布包 SHA256" "$new_version" manifest
  [ -n "$asset_name" ] || asset_name="cms-${new_version}-${current_os}-${current_arch}.tar.gz"

  if [ -n "$target" ] && [ "$target" != "$new_version" ]; then
    fail_upgrade "当前更新源最新版本是 $new_version，不是指定的 $target" "$target" manifest
  fi
  if [ "$new_version" = "$current_version" ]; then
    write_upgrade_status success done "$new_version" "当前已经是最新版本"
    ok "当前已经是最新版本：$new_version"
    return
  fi

  case "$asset_name" in
    *.tar.gz) ;;
    *) fail_upgrade "cms.sh upgrade 仅支持 .tar.gz 发布包：$asset_name" "$new_version" download ;;
  esac

  pkg="$work/$asset_name"
  write_upgrade_status running download "$new_version" "下载 $asset_name"
  info "下载发布包：$asset_name"
  download_file "$asset_url" "$pkg" || fail_upgrade "下载发布包失败：$asset_name" "$new_version" download

  write_upgrade_status running verify "$new_version" "校验 SHA256"
  actual_sha=$(calc_sha256 "$pkg") || fail_upgrade "缺少 SHA256 工具，请安装 sha256sum、shasum 或 openssl" "$new_version" verify
  if [ "$actual_sha" != "$asset_sha" ]; then
    fail_upgrade "SHA256 不匹配，已停止升级" "$new_version" verify
  fi
  ok "SHA256 校验通过"

  extract_dir="$work/extract"
  mkdir -p "$extract_dir"
  write_upgrade_status running extract "$new_version" "解压发布包"
  validate_tar_paths "$pkg" || fail_upgrade "发布包包含不安全路径，已停止解压" "$new_version" extract
  tar -xzf "$pkg" -C "$extract_dir" || fail_upgrade "解压发布包失败" "$new_version" extract
  extracted_root=$(find "$extract_dir" -mindepth 1 -maxdepth 1 -type d | head -n 1)
  [ -n "$extracted_root" ] || fail_upgrade "发布包结构异常：缺少根目录" "$new_version" extract

  src_release="$extracted_root/releases/$new_version"
  [ -d "$src_release" ] || src_release="$extracted_root/current"
  [ -x "$src_release/bin/cms" ] || fail_upgrade "发布包结构异常：缺少 bin/cms" "$new_version" extract
  check_release_platform_file "$src_release/BUILD_INFO" || fail_upgrade "发布包平台与当前系统不匹配" "$new_version" extract

  dest_release="$ROOT/releases/$new_version"
  if [ -e "$dest_release" ]; then
    fail_upgrade "目标版本目录已存在：$dest_release。请确认后手动清理再升级。" "$new_version" extract
  fi
  tmp_release="$ROOT/releases/.${new_version}.tmp.$$"
  rm -rf "$tmp_release"
  cp -R "$src_release" "$tmp_release" || fail_upgrade "复制新版本目录失败" "$new_version" extract
  mv "$tmp_release" "$dest_release" || fail_upgrade "安装新版本目录失败" "$new_version" extract

  prev_ref=$(readlink "$CURRENT")
  [ -n "$prev_ref" ] || fail_upgrade "无法读取当前 current 指向" "$new_version" switch
  backup_dir="$ROOT/backups/${new_version}-$stamp"

  was_running=0
  running && was_running=1
  if [ "$was_running" = "1" ]; then
    write_upgrade_status running stop "$new_version" "停止当前服务"
    stop
  fi

  write_upgrade_status running backup "$new_version" "备份数据库与运行脚本"
  backup_db "$backup_dir"
  backup_root_files "$backup_dir"

  write_upgrade_status running switch "$new_version" "切换 current 到 $new_version"
  set_current_ref "releases/$new_version" || rollback_upgrade "$prev_ref" "$backup_dir" "$was_running" "$new_version"
  install_root_files "$extracted_root" "$dest_release"

  if [ "$was_running" = "1" ]; then
    write_upgrade_status running restart "$new_version" "启动新版本并健康检查"
    START_RETURN_ON_FAIL=1
    if start && wait_http_ready; then
      START_RETURN_ON_FAIL=
      write_upgrade_status success done "$new_version" "升级完成"
      ok "升级完成：$current_version → $new_version"
      return
    fi
    START_RETURN_ON_FAIL=
    rollback_upgrade "$prev_ref" "$backup_dir" "$was_running" "$new_version"
  fi

  write_upgrade_status success done "$new_version" "升级完成，服务原本未运行，未自动启动"
  ok "升级完成：$current_version → $new_version（服务原本未运行，未自动启动）"
}

usage() {
  cat <<EOF
CCVAR 简记 CMS · 启停脚本（macOS / Linux）

用法：  $0 <命令>

命令：
  start     启动服务。未编译过则先自动编译（含按需安装 Go），已编译则直接运行、不重复编译。
  stop      停止服务（按 PID 文件结束进程并释放端口）。
  restart   重启服务（= 先 stop 再 start）。改了代码请先 build 再 restart。
  status    查看运行状态（PID 与访问地址）。
  build     （重新）编译为 bin/cms。仅源码仓库可用，二进制发布包不包含源码。
  logs      实时跟踪「本次运行」日志（Ctrl-C 退出）。
  upgrade   从公开发布仓库升级到最新版本，可选指定版本：upgrade v1.0.5。
  upgrade-status
            输出最近一次升级状态（run/upgrade.json）。
  help      显示本帮助（无参数时同样显示）。

说明：
  · 仅 build、以及「尚无二进制时的 start」会触发编译；其余命令不编译。
  · 发布包默认运行 current/bin/cms，数据保存在 shared/data/，版本保存在 releases/。
  · upgrade 会校验 manifest 签名、校验 SHA256、备份数据库、切换 current，并在失败时回滚。
  · 每次 start 会清空旧日志，只保留本次运行日志（logs/cms.log）。

配置：发布包默认读取 shared/cms.conf，源码模式默认读取 scripts/cms.conf。
优先级：命令行环境变量 > 配置文件 > 内置默认。
环境变量（在命令前覆盖，优先级最高）：
  ADDR=:9090                监听地址（默认 :8080）
  BASE_URL=https://example  站点绝对地址（默认 http://localhost<ADDR>）
  CMS_DB=/path/cms.db       数据库路径（发布包默认 shared/data/cms.db，源码模式默认 data/cms.db）
  GO_VERSION=1.23.4         需自动安装 Go 时下载的版本
  GCMS_UPDATE_URL=https://.../manifest.json  自定义更新清单地址
  GCMS_UPDATE_PUBLIC_KEY=/path/update-public.pem  自定义更新清单签名公钥
  GCMS_UPDATE_REQUIRE_SIGNATURE=0            临时关闭 manifest 签名强校验（排障时使用）
  GCMS_RELEASE_REPO=ccvar/gcms-releases      默认公开发布仓库
EOF
}

case "${1:-}" in
  start)   start ;;
  stop)    stop ;;
  restart) stop; start ;;
  status)  status ;;
  build)   build ;;
  logs)    logs ;;
  upgrade) shift; upgrade "${1:-}" ;;
  upgrade-status) upgrade_status ;;
  ""|help|-h|--help) usage ;;
  *) printf "未知命令：%s\n\n" "$1"; usage; exit 2 ;;
esac
