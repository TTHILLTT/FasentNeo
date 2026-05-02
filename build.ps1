# FasentNeo Build Script for Windows/Linux/Android
param(
    [string]$Target = "all",
    [string]$Version = "1.0.0"
)

$ErrorActionPreference = "Stop"
$ProjectDir = $PSScriptRoot
$OutputDir = Join-Path $ProjectDir "dist"
$GoPath = (Get-Command go -ErrorAction SilentlyContinue).Source

if (-not $GoPath) {
    # Try to find Go in common locations
    $GoPath = Join-Path ${env:ProgramFiles} "Go\bin\go.exe"
    if (-not (Test-Path $GoPath)) {
        Write-Error "Go not found. Please install Go from https://go.dev/dl/"
        exit 1
    }
}

$env:PATH = "$(Split-Path $GoPath -Parent);$env:PATH"

New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null

function Build-Platform {
    param($OS, $Arch, $Ext, $Name)
    Write-Host "Building $Name..." -ForegroundColor Cyan
    $env:GOOS = $OS
    $env:GOARCH = $Arch
    $env:CGO_ENABLED = "0"
    $output = Join-Path $OutputDir "$Name$Ext"
    & go build -ldflags "-s -w -X main.version=$Version" -o $output .
    if ($LASTEXITCODE -ne 0) { throw "Build failed for $Name" }
    $size = (Get-Item $output).Length
    Write-Host "  -> $output ($([math]::Round($size/1MB, 1)) MB)" -ForegroundColor Green
}

function Build-Windows {
    Build-Platform -OS "windows" -Arch "amd64" -Ext ".exe" -Name "fasentneo-windows-amd64"
}

function Build-Linux {
    Build-Platform -OS "linux" -Arch "amd64" -Ext "" -Name "fasentneo-linux-amd64"
    Build-Platform -OS "linux" -Arch "arm64" -Ext "" -Name "fasentneo-linux-arm64"
}

function Build-Android {
    Write-Host "Building Android ARM64 binary..." -ForegroundColor Cyan
    $env:GOOS = "android"
    $env:GOARCH = "arm64"
    $env:CGO_ENABLED = "0"
    $output = Join-Path $OutputDir "fasentneo-android-arm64"
    & go build -ldflags "-s -w -X main.version=$Version" -o $output .
    if ($LASTEXITCODE -ne 0) { throw "Build failed for Android" }
    $size = (Get-Item $output).Length
    Write-Host "  -> $output ($([math]::Round($size/1MB, 1)) MB)" -ForegroundColor Green
    Write-Host "  NOTE: Run on Android via Termux or wrap in an APK with gomobile/WebView" -ForegroundColor Yellow
}

function Build-Deb {
    Write-Host "Building .deb package..." -ForegroundColor Cyan
    $debDir = Join-Path $OutputDir "fasentneo_${Version}_amd64"
    $debData = Join-Path $debDir "DEBIAN"
    $debBin = Join-Path $debDir "usr\local\bin"
    $debApp = Join-Path $debDir "usr\share\applications"
    $debIcon = Join-Path $debDir "usr\share\icons\hicolor\256x256\apps"

    New-Item -ItemType Directory -Force -Path $debData, $debBin, $debApp, $debIcon | Out-Null

    # Copy binary
    Copy-Item (Join-Path $OutputDir "fasentneo-linux-amd64") (Join-Path $debBin "fasentneo") -Force

    # Create control file
    @"
Package: fasentneo
Version: $Version
Section: net
Priority: optional
Architecture: amd64
Maintainer: FasentNeo Team
Description: Fast cross-platform file transfer tool
 FasentNeo allows you to quickly transfer files between devices
 on the same local network. Supports Windows, Linux, and Android.
"@ | Out-File -FilePath (Join-Path $debData "control") -Encoding utf8

    # Create desktop entry
    @"
[Desktop Entry]
Name=FasentNeo
Comment=Fast File Transfer
Exec=fasentneo
Icon=fasentneo
Terminal=false
Type=Application
Categories=Utility;Network;
"@ | Out-File -FilePath (Join-Path $debApp "fasentneo.desktop") -Encoding utf8

    Write-Host "  -> $debDir (use 'dpkg-deb --build' on Linux to finalize)" -ForegroundColor Green
}

switch ($Target) {
    "windows" { Build-Windows }
    "linux" { Build-Linux }
    "android" { Build-Android }
    "deb" { Build-Linux; Build-Deb }
    "all" {
        Build-Windows
        Build-Linux
        Build-Android
    }
    default { Write-Host "Unknown target: $Target. Use: windows, linux, android, deb, all" }
}

Write-Host "`nBuild complete!" -ForegroundColor Green
