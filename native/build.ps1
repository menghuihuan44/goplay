#requires -Version 5.1
<#
.SYNOPSIS
  One-click build for the GoPlay Windows native layer (MSYS2 UCRT64).
.DESCRIPTION
  Configures native/ with CMake + Ninja and builds:
    build/goplay_native.dll   capture + input C ABI
    build/goplay-overlay.exe  tray / status overlay
  Use -Test to run the smoke tests and a non-blocking overlay self-test.
.EXAMPLE
  .\build.ps1 -Test
#>
[CmdletBinding()]
param(
    [string]$BuildDir = "build",
    [ValidateSet("Debug", "Release", "RelWithDebInfo", "MinSizeRel")]
    [string]$Configuration = "Release",
    [switch]$Clean,
    [switch]$Test
)

$ErrorActionPreference = "Stop"
$nativeRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$sourceDir = $nativeRoot
$buildPath = Join-Path $nativeRoot $BuildDir

function Require-Tool([string]$Name) {
    $cmd = Get-Command $Name -ErrorAction SilentlyContinue
    if (-not $cmd) {
        throw "$Name was not found on PATH. Open an MSYS2 UCRT64 shell or add C:\msys64\ucrt64\bin and CMake to PATH."
    }
    return $cmd.Source
}

$cmake = Require-Tool "cmake"
$gxx = Require-Tool "g++"
$ninja = Require-Tool "ninja"

Write-Host "== GoPlay native build ==" -ForegroundColor Cyan
Write-Host "cmake : $cmake"
Write-Host "g++   : $gxx"
Write-Host "ninja : $ninja"
& $gxx --version | Select-Object -First 1
& $cmake --version | Select-Object -First 1

if ($Clean -and (Test-Path $buildPath)) {
    Write-Host "Removing $buildPath"
    Remove-Item -Recurse -Force $buildPath
}

$configure = @(
    "-S", $sourceDir,
    "-B", $buildPath,
    "-G", "Ninja",
    "-DCMAKE_BUILD_TYPE=$Configuration",
    "-DCMAKE_CXX_COMPILER=$gxx",
    "-DGOPLAY_BUILD_TESTS=ON"
)
Write-Host "`n> cmake $($configure -join ' ')" -ForegroundColor DarkGray
& $cmake @configure
if ($LASTEXITCODE -ne 0) { throw "cmake configure failed ($LASTEXITCODE)" }

Write-Host "`n> cmake --build $buildPath --parallel" -ForegroundColor DarkGray
& $cmake --build $buildPath --parallel
if ($LASTEXITCODE -ne 0) { throw "cmake build failed ($LASTEXITCODE)" }

$dll = Join-Path $buildPath "goplay_native.dll"
$overlay = Join-Path $buildPath "goplay-overlay.exe"
foreach ($artifact in @($dll, $overlay)) {
    if (-not (Test-Path $artifact)) { throw "missing build artifact: $artifact" }
    $item = Get-Item $artifact
    Write-Host ("built {0} ({1:N0} bytes)" -f $item.Name, $item.Length) -ForegroundColor Green
}

if ($Test) {
    Write-Host "`n== Smoke tests ==" -ForegroundColor Cyan
    & $cmake -E chdir $buildPath ctest --output-on-failure
    if ($LASTEXITCODE -ne 0) { throw "ctest failed ($LASTEXITCODE)" }

    Write-Host "`n== Overlay self-test (auto-exits) ==" -ForegroundColor Cyan
    # Windows PowerShell's & operator does not wait for a GUI-subsystem exe and
    # leaves $LASTEXITCODE unset, so use Start-Process -Wait and inspect ExitCode.
    $overlayProc = Start-Process -FilePath $overlay `
        -ArgumentList "--self-test", "--self-test-ms", "900" `
        -PassThru -Wait
    if ($overlayProc.ExitCode -ne 0) {
        throw "overlay self-test failed ($($overlayProc.ExitCode))"
    }
    Write-Host "overlay exited cleanly" -ForegroundColor Green
}

Write-Host "`nDone. Output: $buildPath" -ForegroundColor Green
