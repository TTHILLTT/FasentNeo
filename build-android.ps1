# Build Android APK
param(
    [string]$Version = "1.0.0"
)

$ErrorActionPreference = "Stop"
$ProjectDir = $PSScriptRoot

# Find Go
$GoPath = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $GoPath) {
    $GoPath = Join-Path ${env:ProgramFiles} "Go\bin\go.exe"
    if (-not (Test-Path $GoPath)) {
        Write-Error "Go not found"
        exit 1
    }
}
$env:PATH = "$(Split-Path $GoPath -Parent);$env:PATH"

# 1. Build Go binary for Android ARM64
Write-Host "Building Go binary for Android ARM64..." -ForegroundColor Cyan
$env:GOOS = "android"
$env:GOARCH = "arm64"
$env:CGO_ENABLED = "0"
$jniLibs = Join-Path $ProjectDir "android\app\src\main\jniLibs\arm64-v8a"
New-Item -ItemType Directory -Force -Path $jniLibs | Out-Null
$output = Join-Path $jniLibs "libfasentneo.so"
& go build -ldflags "-s -w -X main.version=$Version" -o $output $ProjectDir
if ($LASTEXITCODE -ne 0) { throw "Go build failed" }
$size = (Get-Item $output).Length
Write-Host "  -> $output ($([math]::Round($size/1MB, 1)) MB)" -ForegroundColor Green

# 2. Build APK
Write-Host "Building APK..." -ForegroundColor Cyan
$gradlew = Join-Path $ProjectDir "android\gradlew.bat"
if (Test-Path $gradlew) {
    Push-Location (Join-Path $ProjectDir "android")
    try {
        & .\gradlew.bat assembleRelease
        if ($LASTEXITCODE -eq 0) {
            $apk = Join-Path $ProjectDir "android\app\build\outputs\apk\release\app-release-unsigned.apk"
            if (Test-Path $apk) {
                Write-Host "  -> $apk" -ForegroundColor Green
                Write-Host ""
                Write-Host "APK built! Install via:" -ForegroundColor Yellow
                Write-Host "  adb install $apk" -ForegroundColor White
            }
        } else {
            Write-Warning "Gradle build failed. Try opening android/ in Android Studio instead."
        }
    } finally {
        Pop-Location
    }
} else {
    Write-Host ""
    Write-Host "Gradle wrapper not found. To build the APK:" -ForegroundColor Yellow
    Write-Host "  1. Open the 'android/' folder in Android Studio" -ForegroundColor White
    Write-Host "  2. Build -> Build Bundle(s) / APK(s) -> Build APK(s)" -ForegroundColor White
    Write-Host ""
    Write-Host "Or install Gradle and run:" -ForegroundColor Yellow
    Write-Host "  cd android && gradle assembleRelease" -ForegroundColor White
}
