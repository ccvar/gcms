<#
=============================================================================
 CCVAR 简记 CMS —— 启停脚本（Windows / PowerShell）

   用法：  .\scripts\cms.ps1 <命令>
   命令：  start | stop | restart | status | build | logs

   start 会自动检查 Go 环境：本机已装且 >= 1.23 直接用；否则自动下载官方
         Go 工具链到项目内 .go\ 目录（不污染系统），构建后后台运行。

   可用环境变量（运行前 $env:XXX 覆盖）：
     $env:ADDR=":9090"               监听地址（默认 :8080）
     $env:BASE_URL="https://..."     站点绝对地址（默认 http://localhost<ADDR>）
     $env:CMS_DB="C:\path\cms.db"    数据库路径（发布包默认 shared\data\cms.db，源码模式默认 data\cms.db）
     $env:GO_VERSION="1.23.4"        需要自动安装时下载的 Go 版本
=============================================================================
#>
param([string]$Command = "")

$ErrorActionPreference = "Stop"

$Root      = Split-Path -Parent $PSScriptRoot
$RunDir    = Join-Path $Root "run"
$LogDir    = Join-Path $Root "logs"
$PidFile   = Join-Path $RunDir "cms.pid"
$LogFile   = Join-Path $LogDir "cms.log"      # Go 日志（stderr）写到这里
$OutLog    = Join-Path $LogDir "cms.out.log"
$LocalGoBin= Join-Path $Root ".go\go\bin"
$Current   = Join-Path $Root "current"

if (Test-Path (Join-Path $Current "bin\cms.exe")) {
  $Bin          = Join-Path $Current "bin\cms.exe"
  $BuildInfo    = Join-Path $Current "BUILD_INFO"
  $DefaultCmsDb = "shared\data\cms.db"
  $Conf         = Join-Path $Root "shared\cms.conf"
  if (-not (Test-Path $Conf)) { $Conf = Join-Path $PSScriptRoot "cms.conf" }
} else {
  $Bin          = Join-Path $Root "bin\cms.exe"
  $BuildInfo    = Join-Path $Root "BUILD_INFO"
  $DefaultCmsDb = "data\cms.db"
  $Conf         = Join-Path $PSScriptRoot "cms.conf"
}

# ---- 读取配置文件（仅已知键；命令行 $env 优先，已设置则不覆盖）----
function Load-Conf {
  $conf = $Conf
  if (-not (Test-Path $conf)) { return }
  foreach ($line in Get-Content $conf) {
    $t = $line.Trim()
    if ($t -eq "" -or $t.StartsWith("#")) { continue }
    $kv = $t -split "=", 2
    if ($kv.Count -ne 2) { continue }
    $k = $kv[0].Trim()
    $v = ($kv[1] -replace '\s*#.*$', '').Trim()
    if ($k -notin @("ADDR", "BASE_URL", "CMS_DB", "GO_VERSION")) { continue }
    if (-not [Environment]::GetEnvironmentVariable($k)) {
      Set-Item -Path "Env:$k" -Value $v
    }
  }
}
Load-Conf

$Addr      = if ($env:ADDR) { $env:ADDR } else { ":8080" }
$GoVersion = if ($env:GO_VERSION) { $env:GO_VERSION } else { "1.23.4" }
if (-not $env:CMS_DB) { $env:CMS_DB = $DefaultCmsDb }

function Info($m) { Write-Host "» $m" -ForegroundColor DarkGray }
function Ok($m)   { Write-Host "✓ $m" -ForegroundColor Green }
function Fail($m) { Write-Host "✗ $m" -ForegroundColor Red }

function BaseUrl {
  if ($env:BASE_URL) { return $env:BASE_URL }
  if ($Addr.StartsWith(":")) { return "http://localhost$Addr" }
  return "http://$Addr"
}

function Test-GoOk {
  $g = Get-Command go -ErrorAction SilentlyContinue
  if (-not $g) { return $false }
  try { $v = (& go env GOVERSION) 2>$null } catch { return $false }
  if ($v -match 'go(\d+)\.(\d+)') {
    $maj = [int]$Matches[1]; $min = [int]$Matches[2]
    return ($maj -gt 1) -or ($maj -eq 1 -and $min -ge 23)
  }
  return $false
}

function Ensure-Go {
  if (Test-GoOk) { Info "Go: $(& go env GOVERSION) （系统）"; return }
  if (Test-Path (Join-Path $LocalGoBin "go.exe")) {
    $env:PATH = "$LocalGoBin;$env:PATH"
    if (Test-GoOk) { Info "Go: $(& go env GOVERSION) （项目内 .go\）"; return }
  }
  # 优先尝试 winget / choco
  $winget = Get-Command winget -ErrorAction SilentlyContinue
  $choco  = Get-Command choco  -ErrorAction SilentlyContinue
  if ($winget) {
    Info "用 winget 安装 Go …"
    try { & winget install --silent --accept-source-agreements --accept-package-agreements -e --id GoLang.Go | Out-Null } catch {}
    $env:PATH = "$env:ProgramFiles\Go\bin;$env:PATH"
    if (Test-GoOk) { Ok "已安装 $(& go env GOVERSION)"; return }
  } elseif ($choco) {
    Info "用 Chocolatey 安装 Go …"
    try { & choco install golang -y | Out-Null } catch {}
    $env:PATH = "$env:ProgramFiles\Go\bin;$env:PATH"
    if (Test-GoOk) { Ok "已安装 $(& go env GOVERSION)"; return }
  }
  # 回退：下载官方 zip 到 .go\
  Info "未检测到合适的 Go（需 >= 1.23），下载 go$GoVersion 到 .go\ …"
  $arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
  $url  = "https://go.dev/dl/go$GoVersion.windows-$arch.zip"
  $zip  = Join-Path $env:TEMP "go-$GoVersion.zip"
  Info "下载 $url"
  Invoke-WebRequest -Uri $url -OutFile $zip
  $goParent = Join-Path $Root ".go"
  if (Test-Path $goParent) { Remove-Item $goParent -Recurse -Force }
  New-Item -ItemType Directory -Force -Path $goParent | Out-Null
  Expand-Archive -Path $zip -DestinationPath $goParent -Force
  Remove-Item $zip -Force
  $env:PATH = "$LocalGoBin;$env:PATH"
  if (Test-GoOk) { Ok "已安装 $(& go env GOVERSION) 到 .go\" } else { Fail "Go 安装失败"; exit 1 }
}

function Get-CmsProc {
  if (-not (Test-Path $PidFile)) { return $null }
  $procId = (Get-Content $PidFile -ErrorAction SilentlyContinue | Select-Object -First 1)
  if (-not $procId) { return $null }
  return (Get-Process -Id ([int]$procId) -ErrorAction SilentlyContinue)
}

function Invoke-Build {
  if (-not (Test-Path (Join-Path $Root "go.mod"))) {
    Fail "当前是二进制发布包，不包含源码，无法 build。请下载新版发布包或在源码仓库中构建。"
    exit 1
  }
  Ensure-Go
  Info "构建 → $Bin"
  Push-Location $Root
  try { & go build -o $Bin . ; if ($LASTEXITCODE -ne 0) { throw "go build 失败" } } finally { Pop-Location }
  Ok "构建完成"
}

function Start-Cms {
  if (Get-CmsProc) { Ok "已在运行（PID $((Get-CmsProc).Id)） → $(BaseUrl)"; return }
  # 仅在「尚无已编译二进制」时编译；已编译则直接运行，不重复编译（改代码请先 build）
  if (Test-Path $Bin) {
    Info "使用已编译二进制：$Bin（如已改动代码，请先运行：.\scripts\cms.ps1 build）"
  } else {
    Info "未发现已编译二进制，开始首次编译 …"
    Invoke-Build
  }
  New-Item -ItemType Directory -Force -Path $RunDir | Out-Null
  New-Item -ItemType Directory -Force -Path $LogDir | Out-Null
  $env:ADDR = $Addr
  $env:BASE_URL = (BaseUrl)
  Info "启动服务 …"
  $p = Start-Process -FilePath $Bin -WorkingDirectory $Root -WindowStyle Hidden -PassThru `
        -RedirectStandardOutput $OutLog -RedirectStandardError $LogFile
  $p.Id | Out-File -Encoding ascii -FilePath $PidFile
  Start-Sleep -Seconds 1
  if (Get-CmsProc) {
    Ok "已启动 → $(BaseUrl)   后台 $(BaseUrl)/admin"
    Info "PID $($p.Id)  ·  日志 $LogFile"
  } else {
    Fail "启动失败，请查看日志：$LogFile"
    if (Test-Path $LogFile) { Get-Content $LogFile -Tail 20 }
    exit 1
  }
}

function Stop-Cms {
  $proc = Get-CmsProc
  if ($proc) {
    try { $proc | Stop-Process -Force -ErrorAction SilentlyContinue } catch {}
    Remove-Item $PidFile -ErrorAction SilentlyContinue
    Ok "已停止（PID $($proc.Id)）"
  } else {
    Remove-Item $PidFile -ErrorAction SilentlyContinue
    Info "服务未在运行"
  }
}

function Status-Cms {
  $proc = Get-CmsProc
  if ($proc) { Ok "运行中（PID $($proc.Id)） → $(BaseUrl)" } else { Info "未运行" }
}

function Logs-Cms {
  New-Item -ItemType Directory -Force -Path $RunDir | Out-Null
  if (-not (Test-Path $LogFile)) { New-Item -ItemType File -Path $LogFile | Out-Null }
  Get-Content $LogFile -Tail 80 -Wait
}

function Show-Usage {
  Write-Host @"
CCVAR 简记 CMS · 启停脚本（Windows / PowerShell）

用法：  .\scripts\cms.ps1 <命令>

命令：
  start     启动服务。未编译过则先自动编译（含按需安装 Go），已编译则直接运行、不重复编译。
  stop      停止服务（按 PID 文件结束进程）。
  restart   重启服务（= 先 stop 再 start）。改了代码请先 build 再 restart。
  status    查看运行状态（PID 与访问地址）。
  build     （重新）编译为 bin\cms.exe。仅源码仓库可用，二进制发布包不包含源码。
  logs      实时跟踪「本次运行」日志（Ctrl-C 退出）。
  help      显示本帮助（无参数时同样显示）。

说明：
  · 仅 build、以及「尚无二进制时的 start」会触发编译；其余命令不编译。
  · 发布包默认运行 current\bin\cms.exe，数据保存在 shared\data\，版本保存在 releases\。
  · 每次 start 会重写日志，只保留本次运行日志（logs\cms.log）。

配置：发布包默认读取 shared\cms.conf，源码模式默认读取 scripts\cms.conf。
优先级：`$env` 环境变量 > 配置文件 > 内置默认。
环境变量（运行前 `$env:XXX` 覆盖，优先级最高）：
  `$env:ADDR=":9090"`              监听地址（默认 :8080）
  `$env:BASE_URL="https://..."`    站点绝对地址（默认 http://localhost<ADDR>）
  `$env:CMS_DB="C:\path\cms.db"`   数据库路径（发布包默认 shared\data\cms.db，源码模式默认 data\cms.db）
  `$env:GO_VERSION="1.23.4"`       需自动安装 Go 时下载的版本
"@
}

switch ($Command.ToLower()) {
  "start"   { Start-Cms }
  "stop"    { Stop-Cms }
  "restart" { Stop-Cms; Start-Cms }
  "status"  { Status-Cms }
  "build"   { Invoke-Build }
  "logs"    { Logs-Cms }
  { $_ -in @("", "help", "-h", "--help") } { Show-Usage }
  default   { Write-Host "未知命令：$Command`n"; Show-Usage; exit 2 }
}
