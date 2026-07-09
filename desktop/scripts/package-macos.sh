#!/usr/bin/env bash
# 本地打包 GCMS Pilot（macOS，测试用）。
# 产物：src-tauri/target/release/bundle/{macos/GCMS Pilot.app, dmg/*.dmg}
# 与正式发布（CI）的差别：本地没有 ed25519 更新私钥，这里用 --config 覆盖关掉
# createUpdaterArtifacts —— 只出安装包、不出 .app.tar.gz 更新包，装出来的 App
# 一切功能正常（包括「检查更新」，它走线上 latest.json）。
set -euo pipefail

cd "$(dirname "$0")/.."   # → desktop/

command -v node  >/dev/null || { echo "缺 Node.js（≥20）：https://nodejs.org"; exit 1; }
command -v cargo >/dev/null || { echo "缺 Rust：https://rustup.rs"; exit 1; }

[ -d node_modules ] || npm ci

# 覆盖片段：关掉更新包签名（本地无 TAURI_SIGNING_PRIVATE_KEY）
cfg="$(mktemp -t pilot-local-XXXX).json"
trap 'rm -f "$cfg"' EXIT
printf '{"bundle":{"createUpdaterArtifacts":false}}' > "$cfg"

t0=$(date +%s)
npm run tauri build -- --bundles app,dmg --config "$cfg"
echo
echo "✅ 打包完成（$(( $(date +%s) - t0 ))s）："
ls -lh src-tauri/target/release/bundle/dmg/*.dmg 2>/dev/null || true
echo "   App：src-tauri/target/release/bundle/macos/GCMS Pilot.app"
