# CodePilot Windows 构建脚本(PowerShell 备用)
# ----------------------------------------------------------------------
# 等价于 `make build`,供没有 make 工具的 Windows 用户使用。
#
# 用法:  powershell -ExecutionPolicy Bypass -File build\build.ps1
# ----------------------------------------------------------------------

$ErrorActionPreference = "Stop"
$ProjectRoot = Split-Path -Parent $PSScriptRoot
Set-Location $ProjectRoot

$App        = "CodePilot"
$AssetsDir  = Join-Path $ProjectRoot "build\assets"
$DistDir    = Join-Path $ProjectRoot "build\dist"
$SysoPath   = Join-Path $ProjectRoot "src\resource_windows_amd64.syso"
$IcoPath    = Join-Path $AssetsDir "icon.ico"
$Entry      = ".\src"
$Python     = "python"

# 1. 检测/安装 rsrc
# 优先 GOBIN,否则用 GOPATH\bin;GOPATH 可能是分号分隔多路径,取第一段并去掉末尾分号
$GoBin = (& go env GOBIN).Trim()
if ([string]::IsNullOrEmpty($GoBin)) {
    $GoPathRaw = (& go env GOPATH).Trim()
    $GoPath    = ($GoPathRaw -split ';')[0].TrimEnd('\', '/', ';')
    $GoBin     = Join-Path $GoPath "bin"
}
$Rsrc = Join-Path $GoBin "rsrc.exe"
if (-not (Test-Path $Rsrc)) {
    Write-Host ">> 未找到 rsrc (expected at $Rsrc),正在安装 akavel/rsrc ..."
    & go install github.com/akavel/rsrc@latest | Out-Null
    if (-not (Test-Path $Rsrc)) { throw "rsrc 安装失败,请检查 go env GOBIN=$GoBin" }
}
Write-Host ">> 使用 rsrc: $Rsrc"

# 2. 生成图标资源
Write-Host ">> 生成图标资源 ..."
& $Python (Join-Path $AssetsDir "generate_icon.py")

# 3. 编译 .syso
Write-Host ">> 编译 .syso ..."
& $Rsrc -ico $IcoPath -o $SysoPath

# 4. 编译 exe
if (-not (Test-Path $DistDir)) { New-Item -ItemType Directory -Path $DistDir | Out-Null }
$OutExe = Join-Path $DistDir "$App.exe"
Write-Host ">> 编译 $App.exe ..."
$env:GOOS = "windows"; $env:GOARCH = "amd64"
go build -ldflags="-s -w" -o $OutExe $Entry

# 5. Copy builtin Skill resources (Step 10.1 Task 4)
#    Source: src/internal/skill/builtin/<name>/SKILL.md
#    Target: <output>/internal/skill/builtin/<name>/SKILL.md
#    This matches the only path scanned by skill.LoadAll (scanner.go builtinRelPath = "internal/skill/builtin").
if (-not $ProjectRoot) {
    $ProjectRoot = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
}
$SkillSrc = Join-Path $ProjectRoot "src\internal\skill\builtin"
if (Test-Path $SkillSrc) {
    $SkillDstBuiltin = Join-Path $DistDir "internal\skill\builtin"
    if (-not (Test-Path $SkillDstBuiltin)) { New-Item -ItemType Directory -Path $SkillDstBuiltin | Out-Null }
    Copy-Item -Path (Join-Path $SkillSrc "*") -Destination $SkillDstBuiltin -Recurse -Force

    $CopiedCount = (Get-ChildItem -Recurse -Path $SkillSrc -Filter "SKILL.md" | Measure-Object).Count
    Write-Host ">> Copied $CopiedCount builtin SKILL.md -> $DistDir\internal\skill\builtin\"
}

Write-Host ">> 完成: $OutExe"
Get-Item $OutExe | Select-Object Name, @{Name="Size(MB)";Expression={[math]::Round($_.Length/1MB,2)}}