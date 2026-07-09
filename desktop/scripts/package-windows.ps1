# 本地打包 GCMS Pilot（Windows，测试用）—— 产 NSIS 安装器 *-setup.exe。
#
# 运行（在 desktop\ 或仓库任意位置）：
#   powershell -ExecutionPolicy Bypass -File desktop\scripts\package-windows.ps1
#   （或双击同目录的 package-windows.cmd）
#
# 前置（一次性）：
#   1. Node.js ≥20：  winget install OpenJS.NodeJS.LTS
#   2. Rust(MSVC)：   winget install Rustlang.Rustup   然后 rustup default stable-msvc
#   3. VS C++ 生成工具（MSVC 链接器）：
#      winget install Microsoft.VisualStudio.2022.BuildTools --override "--wait --add Microsoft.VisualStudio.Workload.VCTools"
#   NSIS 不用手装（tauri CLI 自动下载）；WebView2 Win10/11 一般自带。
#
# 与正式发布（CI）的差别：本地没有 ed25519 更新私钥，这里用 --config 覆盖关掉
# createUpdaterArtifacts —— 只出安装器、不出更新签名，装出来的 App 功能不受影响。

$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")   # → desktop\

foreach ($t in @(
    @{ cmd = "node";  hint = "缺 Node.js（>=20）：winget install OpenJS.NodeJS.LTS" },
    @{ cmd = "npm";   hint = "npm 不在 PATH（随 Node.js 安装）" },
    @{ cmd = "cargo"; hint = "缺 Rust：winget install Rustlang.Rustup ，再 rustup default stable-msvc" }
)) {
    if (-not (Get-Command $t.cmd -ErrorAction SilentlyContinue)) {
        Write-Host "❌ $($t.hint)" -ForegroundColor Red; exit 1
    }
}

if (-not (Test-Path node_modules)) { npm ci; if ($LASTEXITCODE -ne 0) { exit 1 } }

# 覆盖片段：关掉更新包签名（本地无 TAURI_SIGNING_PRIVATE_KEY）
$cfg = Join-Path ([System.IO.Path]::GetTempPath()) "pilot-local-config.json"
'{"bundle":{"createUpdaterArtifacts":false}}' | Set-Content -Path $cfg -Encoding ascii

$t0 = Get-Date
npm run tauri build -- --bundles nsis --config $cfg
if ($LASTEXITCODE -ne 0) { Write-Host "❌ 构建失败（见上方输出）" -ForegroundColor Red; exit 1 }

$exe = Get-ChildItem "src-tauri\target\release\bundle\nsis\*-setup.exe" | Select-Object -First 1
Write-Host ""
Write-Host ("✅ 打包完成（{0:n0}s）：" -f ((Get-Date) - $t0).TotalSeconds) -ForegroundColor Green
Write-Host ("   {0}  ({1:n1} MB)" -f $exe.FullName, ($exe.Length / 1MB))
