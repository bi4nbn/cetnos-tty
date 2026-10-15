# build.ps1 — 自动递增 patch 版本号，编译 Linux amd64 静态二进制
$ErrorActionPreference = "Stop"
Set-Location "$PSScriptRoot\src"

# 读取当前版本
$verFile = "$PSScriptRoot\VERSION"
if (-not (Test-Path $verFile)) { "v1.0.0" | Out-File -Encoding ascii -NoNewline $verFile }
$cur = (Get-Content $verFile).Trim()

# vX.Y.Z -> X.Y.Z
$parts = $cur.TrimStart("v") -split "\."
$patch = [int]$parts[2] + 1
$newVer = "v$($parts[0]).$($parts[1]).$patch"
$newVer | Out-File -Encoding ascii -NoNewline $verFile
Write-Host "Building $newVer ..."

# 编译
$env:GOOS = "linux"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
go build -ldflags "-s -w -X main.version=$newVer" -o linux-tty .
if ($LASTEXITCODE -ne 0) { Write-Host "BUILD FAILED"; exit 1 }

$size = [math]::Round((Get-Item linux-tty).Length / 1KB)
Write-Host "OK: linux-tty ($size KB) $newVer"
