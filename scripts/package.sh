#!/usr/bin/env sh
# =============================================================================
# CCVAR 简记 CMS · 打包脚本 —— 生成可直接部署的发布包
#
#   用法：  ./scripts/package.sh [GOOS GOARCH]
#     不带参数        → 为「当前系统」平台打包
#     ./package.sh linux amd64    → 为 Linux 服务器打包（最常见）
#     ./package.sh windows amd64  → 为 Windows 打包
#     ./package.sh darwin arm64   → 为 Apple Silicon Mac 打包
#
#   产物：  dist/cms-<版本>-<os>-<arch>.tar.gz
#   解压后开箱即用（已内置二进制 + 启停脚本 + 默认配置，部署机无需安装 Go）：
#     bin/cms             预编译程序（模板/静态资源已 embed，单文件）
#     scripts/cms.sh      启停脚本（Linux/macOS）
#     scripts/cms.ps1     启停脚本（Windows）
#     scripts/cms.conf    默认配置（端口 / 域名 / 数据库路径）
#     README.txt          部署说明
#
#   可用环境变量：VERSION=v1.0.0 ./scripts/package.sh linux amd64
# =============================================================================
set -eu

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$SCRIPT_DIR/.." && pwd)

if [ -t 1 ]; then C_OK='\033[32m'; C_ERR='\033[31m'; C_DIM='\033[2m'; C_0='\033[0m'; else C_OK=; C_ERR=; C_DIM=; C_0=; fi
info() { printf "%b\n" "${C_DIM}» $*${C_0}"; }
ok()   { printf "%b\n" "${C_OK}✓ $*${C_0}"; }
err()  { printf "%b\n" "${C_ERR}✗ $*${C_0}" >&2; }

# ---- 确保有 Go（优先系统，其次项目内 .go/，否则提示）----
if ! command -v go >/dev/null 2>&1 && [ -x "$ROOT/.go/go/bin/go" ]; then
  export PATH="$ROOT/.go/go/bin:$PATH"
fi
if ! command -v go >/dev/null 2>&1; then
  err "打包需要 Go。可先运行  ./scripts/cms.sh build （会自动安装 Go），或手动安装：https://go.dev/dl/"
  exit 1
fi

GOOS=${1:-$(go env GOOS)}
GOARCH=${2:-$(go env GOARCH)}
VERSION=${VERSION:-$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || date +%Y%m%d)}
NAME="cms-${VERSION}-${GOOS}-${GOARCH}"
OUT="$ROOT/dist"
DIR="$OUT/$NAME"
BINEXT=""; [ "$GOOS" = "windows" ] && BINEXT=".exe"

info "打包 $NAME"
rm -rf "$DIR"
mkdir -p "$DIR/bin" "$DIR/scripts"

# ---- 编译（纯 Go，CGO 关闭，便于交叉编译；裁剪符号表减小体积）----
info "编译 $GOOS/$GOARCH …"
( cd "$ROOT" && CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath -ldflags "-s -w" -o "$DIR/bin/cms$BINEXT" . )
ok "已编译 → bin/cms$BINEXT （$(du -h "$DIR/bin/cms$BINEXT" | cut -f1)）"

# ---- 拷贝启停脚本与默认配置 ----
cp "$SCRIPT_DIR/cms.sh" "$SCRIPT_DIR/cms.ps1" "$SCRIPT_DIR/cms.conf" "$DIR/scripts/"
chmod +x "$DIR/scripts/cms.sh"

# ---- 写入发布包元信息，启动脚本用来提示平台不匹配 ----
cat > "$DIR/BUILD_INFO" <<EOF
VERSION=$VERSION
GOOS=$GOOS
GOARCH=$GOARCH
BUILT_AT=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
EOF

# ---- 部署说明 ----
cat > "$DIR/README.txt" <<EOF
CCVAR 简记 CMS · 部署包（$NAME）

目录结构：
  bin/cms$BINEXT       预编译程序（模板与静态资源已内嵌，单文件运行，部署机无需 Go）
  scripts/cms.sh     启停脚本（Linux / macOS）
  scripts/cms.ps1    启停脚本（Windows PowerShell）
  scripts/cms.conf   配置文件（监听端口 / 站点域名 / 数据库路径）
  BUILD_INFO         发布包平台信息
  data/              运行后自动生成（SQLite 数据库与上传文件）

一、配置（可选但推荐）
  编辑 scripts/cms.conf：
    ADDR=:8080                      监听端口
    BASE_URL=https://your-domain    生产环境务必设为你的 https 域名

二、启动（Linux / macOS）
    ./scripts/cms.sh start          启动（后台运行）
    ./scripts/cms.sh status         查看状态
    ./scripts/cms.sh logs           查看日志
    ./scripts/cms.sh stop           停止
    ./scripts/cms.sh restart        重启

   Windows（PowerShell）：
    ./scripts/cms.ps1 start | status | stop | restart

三、首次启动
  会在 data/cms.db 自动建库并写入演示内容，控制台打印默认后台账号：
    用户名 admin   密码 admin123
  浏览器打开 http://localhost:8080 ，后台 http://localhost:8080/admin
  ⚠ 登录后请尽快在「设置 → 安全」修改默认密码。

四、生产部署建议
  · 用 Nginx / Caddy 终止 HTTPS 并反向代理到本服务端口（如 127.0.0.1:8080）。
  · 在 scripts/cms.conf 设置 BASE_URL 为 https 域名（影响 canonical / 站点地图）。
  · 可用 systemd 托管：ExecStart 指向 bin/cms，工作目录为本包根目录，
    并设置环境变量 ADDR / BASE_URL / CMS_DB。
EOF

# ---- 打 tar.gz ----
( cd "$OUT" && tar -czf "$NAME.tar.gz" "$NAME" )
ok "发布包已生成：dist/$NAME.tar.gz （$(du -h "$OUT/$NAME.tar.gz" | cut -f1)）"
info "解压后：cd $NAME && ./scripts/cms.sh start"
