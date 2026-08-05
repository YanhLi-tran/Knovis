# 跨平台编译 local-agent
# 用法：
#   .\scripts\build-local-agent.ps1                 # 默认编译全部目标平台
#   .\scripts\build-local-agent.ps1 -Targets linux-amd64  # 仅编译指定目标
#   .\scripts\build-local-agent.ps1 -Version 0.1.1  # 指定版本号（写入文件名）
param(
    [object]$Targets = $null,
    [string]$Version = "",
    [string]$OutputDir = "dist"
)

$ErrorActionPreference = "Stop"
# 强制 UTF-8 输出，避免中文乱码
try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch {}
$OutputEncoding = [System.Text.Encoding]::UTF8

$Root = Resolve-Path (Join-Path $PSScriptRoot "..")
$Pkg = Join-Path $Root "local-agent"

# Targets 兼容三种传入方式：数组 / 逗号分隔字符串 / 单个字符串
if ($null -eq $Targets) {
    $Targets = @("windows-amd64", "linux-amd64", "darwin-amd64", "darwin-arm64")
} elseif ($Targets -is [string]) {
    if ($Targets -match ",") {
        $Targets = $Targets -split "," | ForEach-Object { $_.Trim() }
    } else {
        $Targets = @($Targets)
    }
}

if (-not (Test-Path $OutputDir)) {
    New-Item -ItemType Directory -Path $OutputDir | Out-Null
}
# 输出目录转绝对路径，便于 Push-Location 后引用
$OutputDir = (Resolve-Path $OutputDir).Path

# 版本号写入文件名后缀（可选，便于分发管理）
$verSuffix = ""
if ($Version -ne "") { $verSuffix = "-v$Version" }

$total = $Targets.Count
$idx = 0
$failed = @()
$skipped = @()

foreach ($target in $Targets) {
    $idx++
    $parts = $target -split "-"
    if ($parts.Count -ne 2) {
        Write-Host "[SKIP] invalid target format: $target (expect GOOS-GOARCH)" -ForegroundColor Yellow
        $skipped += $target
        continue
    }
    $goos = $parts[0]
    $goarch = $parts[1]

    # 二进制后缀
    $ext = ""
    if ($goos -eq "windows") { $ext = ".exe" }

    $outName = "local-agent-${target}${verSuffix}${ext}"
    $outPath = Join-Path $OutputDir $outName

    Write-Host ""
    Write-Host "[$idx/$total] build $target -> $outName" -ForegroundColor Cyan

    $env:GOOS = $goos
    $env:GOARCH = $goarch
    $env:CGO_ENABLED = "0"  # 纯 Go，无 CGO 依赖，跨平台编译可靠

    # go build 必须在含 go.mod 的目录下运行（local-agent/）
    Push-Location $Pkg
    try {
        & go build -trimpath -ldflags "-s -w" -o $outPath "."
    } finally {
        Pop-Location
    }
    if ($LASTEXITCODE -ne 0) {
        Write-Host "  [FAIL] build failed" -ForegroundColor Red
        $failed += $target
        continue
    }

    $size = (Get-Item $outPath).Length / 1MB
    Write-Host ("  [OK] {0:N2} MB" -f $size) -ForegroundColor Green
}

# 恢复环境变量
Remove-Item Env:GOOS -ErrorAction SilentlyContinue
Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue

Write-Host ""
if ($failed.Count -gt 0) {
    Write-Host "[SUMMARY] failed: $($failed -join ', ')" -ForegroundColor Red
    exit 1
} elseif ($skipped.Count -gt 0) {
    Write-Host "[SUMMARY] ok=$($total - $skipped.Count - $failed.Count) skipped=$($skipped.Count) total=$total" -ForegroundColor Yellow
} else {
    Write-Host "[SUMMARY] all build ok ($total/$total)" -ForegroundColor Green
    Get-ChildItem $OutputDir -Filter "local-agent-*" | Format-Table Name, @{N='Size(MB)';E={'{0:N2}' -f ($_.Length/1MB)}}
}
