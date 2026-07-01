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
#   解压后即是可升级标准目录：
#     current -> releases/<版本>  当前运行版本
#     releases/<版本>/bin/cms     预编译程序
#     shared/cms.conf             默认配置
#     shared/data/                SQLite 与上传文件
#     scripts/cms.sh              启停脚本（Linux/macOS）
#     scripts/cms.ps1             启停脚本（Windows）
#
#   可用环境变量：VERSION=v1.0.0 ./scripts/package.sh linux amd64
# =============================================================================
set -eu
export LC_ALL=C
export LANG=C

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$SCRIPT_DIR/.." && pwd)

if [ -t 1 ]; then C_OK='\033[32m'; C_ERR='\033[31m'; C_DIM='\033[2m'; C_0='\033[0m'; else C_OK=; C_ERR=; C_DIM=; C_0=; fi
info() { printf "%b\n" "${C_DIM}» $*${C_0}"; }
ok()   { printf "%b\n" "${C_OK}✓ $*${C_0}"; }
err()  { printf "%b\n" "${C_ERR}✗ $*${C_0}" >&2; }

RESTORE_ASSETS_DIR=
restore_assets() {
  if [ -n "${RESTORE_ASSETS_DIR:-}" ] && [ -d "$RESTORE_ASSETS_DIR" ]; then
    info "恢复未压缩前端资源 …"
    ( cd "$RESTORE_ASSETS_DIR" && find . -type f | while IFS= read -r f; do
      src="$RESTORE_ASSETS_DIR/${f#./}"
      dst="$ROOT/${f#./}"
      cp "$src" "$dst"
    done )
    rm -rf "$RESTORE_ASSETS_DIR"
    RESTORE_ASSETS_DIR=
  fi
}
trap restore_assets EXIT HUP INT TERM

minify_release_assets() {
  if [ "${MINIFY_ASSETS:-1}" = "0" ]; then
    info "跳过 CSS/JS 压缩（MINIFY_ASSETS=0）"
    return
  fi
  if [ ! -d "$ROOT/assets/css" ] && [ ! -d "$ROOT/assets/js" ]; then
    return
  fi
  RESTORE_ASSETS_DIR=$(mktemp -d "${TMPDIR:-/tmp}/gcms-assets.XXXXXX")
  ( cd "$ROOT" && find assets/css assets/js -type f \( -name '*.css' -o -name '*.js' \) 2>/dev/null | while IFS= read -r f; do
    mkdir -p "$RESTORE_ASSETS_DIR/$(dirname "$f")"
    cp "$f" "$RESTORE_ASSETS_DIR/$f"
  done )
  info "压缩发布包内 CSS/JS …"
  ( cd "$ROOT" && go run ./tools/minifyassets )
}

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
COMMIT=${COMMIT:-$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)}
BUILT_AT=${BUILT_AT:-$(date -u '+%Y-%m-%dT%H:%M:%SZ')}
RELEASE_REPO=${RELEASE_REPO:-ccvar/gcms-releases}
NAME="cms-${VERSION}-${GOOS}-${GOARCH}"
OUT="$ROOT/dist"
DIR="$OUT/$NAME"
RELEASE_DIR="$DIR/releases/$VERSION"
BINEXT=""; [ "$GOOS" = "windows" ] && BINEXT=".exe"

info "打包 $NAME"
rm -rf "$DIR"
mkdir -p "$RELEASE_DIR/bin" "$RELEASE_DIR/scripts" "$DIR/scripts" "$DIR/shared/data" "$DIR/run" "$DIR/logs" "$DIR/tmp" "$DIR/backups"

# ---- 编译（纯 Go，CGO 关闭，便于交叉编译；裁剪符号表减小体积）----
minify_release_assets
info "编译 $GOOS/$GOARCH …"
( cd "$ROOT" && CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath -ldflags "-s -w -X cms.ccvar.com/internal/version.Version=${VERSION} -X cms.ccvar.com/internal/version.Commit=${COMMIT} -X cms.ccvar.com/internal/version.BuiltAt=${BUILT_AT} -X cms.ccvar.com/internal/version.Repo=${RELEASE_REPO}" -o "$RELEASE_DIR/bin/cms$BINEXT" . )
ok "已编译 → releases/$VERSION/bin/cms$BINEXT （$(du -h "$RELEASE_DIR/bin/cms$BINEXT" | cut -f1)）"
restore_assets

# ---- 拷贝启停脚本与默认配置 ----
cp "$SCRIPT_DIR/cms.sh" "$SCRIPT_DIR/cms.ps1" "$SCRIPT_DIR/gcms-caddy-sync.sh" "$DIR/scripts/"
cp "$SCRIPT_DIR/cms.sh" "$SCRIPT_DIR/cms.ps1" "$SCRIPT_DIR/gcms-caddy-sync.sh" "$RELEASE_DIR/scripts/"
if [ -f "$SCRIPT_DIR/update-public.pem" ]; then
  cp "$SCRIPT_DIR/update-public.pem" "$DIR/scripts/"
  cp "$SCRIPT_DIR/update-public.pem" "$RELEASE_DIR/scripts/"
fi
chmod +x "$DIR/scripts/cms.sh" "$DIR/scripts/gcms-caddy-sync.sh"
chmod +x "$RELEASE_DIR/scripts/cms.sh" "$RELEASE_DIR/scripts/gcms-caddy-sync.sh"
sed 's#^CMS_DB=.*#CMS_DB=shared/data/cms.db#' "$SCRIPT_DIR/cms.conf" > "$DIR/shared/cms.conf"

# ---- 写入发布包元信息，启动脚本用来提示平台不匹配 ----
cat > "$RELEASE_DIR/BUILD_INFO" <<EOF
VERSION=$VERSION
COMMIT=$COMMIT
GOOS=$GOOS
GOARCH=$GOARCH
BUILT_AT=$BUILT_AT
RELEASE_REPO=$RELEASE_REPO
EOF
cp "$RELEASE_DIR/BUILD_INFO" "$DIR/BUILD_INFO"

if [ "$GOOS" = "windows" ]; then
  mkdir -p "$DIR/current"
  cp -R "$RELEASE_DIR/." "$DIR/current/"
else
  ( cd "$DIR" && ln -s "releases/$VERSION" current )
fi

# ---- 部署说明 ----
cat > "$DIR/README.txt" <<EOF
CCVAR 简记 CMS · 部署包（${NAME}）

目录结构：
  current            当前运行版本（Linux/macOS 为软链，Windows 为目录）
  releases/$VERSION  当前版本程序目录
  shared/cms.conf    配置文件（监听端口 / 站点域名 / 数据库路径）
  shared/data/       SQLite 数据库与上传文件
  scripts/           启停脚本
  run/               PID 文件
  logs/              运行日志
  tmp/               升级临时目录
  backups/           升级前备份目录
  BUILD_INFO         发布包平台与版本信息快照

更新源：
  公开发布仓库：https://github.com/${RELEASE_REPO}
  后台「设置 → 系统更新」会从该公开仓库读取 manifest.json 检查新版本，
  检测到可用更新时可直接点击「一键升级」。

一、配置（可选但推荐）
  编辑 shared/cms.conf：
    ADDR=:8080                      监听端口
    BASE_URL=https://your-domain    生产环境务必设为你的 https 域名
    CMS_DB=shared/data/cms.db       共享数据库路径，升级时不会被覆盖

二、启动（Linux / macOS）
    ./scripts/cms.sh start          启动（后台运行）
    ./scripts/cms.sh status         查看状态
    ./scripts/cms.sh logs           查看日志
    ./scripts/cms.sh stop           停止
    ./scripts/cms.sh restart        重启
    ./scripts/cms.sh upgrade        升级到公开发布仓库的最新版本
    ./scripts/cms.sh upgrade-status 查看最近一次升级状态

   Windows（PowerShell）：
    ./scripts/cms.ps1 start | status | stop | restart

三、首次启动
  会在 shared/data/cms.db 自动建库并写入演示内容，控制台打印默认后台账号：
    用户名 admin   密码 admin123
  浏览器打开 http://localhost:8080 ，后台 http://localhost:8080/admin
  ⚠ 登录后请尽快在「设置 → 安全」修改默认密码。

四、生产部署建议
  · 用 Nginx / Caddy 终止 HTTPS 并反向代理到本服务端口（如 127.0.0.1:8080）。
  · 在 shared/cms.conf 设置 BASE_URL 为 https 域名（影响 canonical / 站点地图）。
  · 可用 systemd 托管：ExecStart 建议直接指向 current/bin/cms，
    WorkingDirectory 设为本包根目录，并设置 ADDR / BASE_URL / CMS_DB=shared/data/cms.db。
    scripts/cms.sh 更适合手动启停与简单部署。

五、升级
  Linux / macOS 可直接执行：

    ./scripts/cms.sh upgrade

  它会读取公开发布仓库的 manifest.json，先校验 manifest.json.sig，
  再下载当前平台包、校验 SHA256、检查压缩包路径安全性，解压到 releases/<新版本>，
  备份 shared/data/cms.db，切换 current，然后重启并做健康检查。
  失败时会切回旧版本并恢复数据库备份。

  后台「设置 → 系统更新」的一键升级按钮也会调用同一个升级器。
  升级状态写入 run/upgrade.json，可用 ./scripts/cms.sh upgrade-status 查看。
  后台异步启动日志写入 logs/upgrade-runner.log。
  依赖：python3、curl 或 wget、tar、sha256sum / shasum / openssl 之一。

六、升级目录规划
  本发布包默认已经是后台一键升级所需的标准目录：

    $NAME/
      current -> releases/$VERSION
      releases/$VERSION/
      shared/data/cms.db
      shared/data/uploads/
      shared/cms.conf
      backups/
      tmp/

  current 指向当前运行版本；shared 保存数据库、上传文件和配置，升级时不覆盖。
EOF
cp "$DIR/README.txt" "$RELEASE_DIR/README.txt"

# ---- 打 tar.gz ----
( cd "$OUT" && tar -czf "$NAME.tar.gz" "$NAME" )
if command -v sha256sum >/dev/null 2>&1; then
  ( cd "$OUT" && sha256sum "$NAME.tar.gz" > "$NAME.sha256" )
elif command -v shasum >/dev/null 2>&1; then
  ( cd "$OUT" && shasum -a 256 "$NAME.tar.gz" > "$NAME.sha256" )
fi
ok "发布包已生成：dist/$NAME.tar.gz （$(du -h "$OUT/$NAME.tar.gz" | cut -f1)）"
info "解压后：cd $NAME && ./scripts/cms.sh start"
