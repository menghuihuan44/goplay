[CmdletBinding()]
param(
  [int]$Port = 18430,
  [int]$Seed = 7,
  [int]$TimeoutSec = 90
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$bin = Join-Path $root 'bin/goplayd.exe'
if (-not (Test-Path $bin)) {
  Write-Host 'building goplayd...'
  New-Item -ItemType Directory -Force (Join-Path $root 'bin') | Out-Null
  & go build -o $bin ./cmd/goplayd
  if ($LASTEXITCODE -ne 0) { throw 'build failed' }
}

$data = Join-Path $root '.tmp-smoke'
if (Test-Path $data) { Remove-Item -Recurse -Force $data }
New-Item -ItemType Directory -Force $data | Out-Null
$stdout = Join-Path $data 'daemon.stdout.log'
$stderr = Join-Path $data 'daemon.stderr.log'
$base = "http://127.0.0.1:$Port"
$proc = Start-Process -FilePath $bin -ArgumentList @(
  '-addr', "127.0.0.1:$Port",
  '-data', $data,
  '-llm', 'offline'
) -PassThru -WindowStyle Hidden -RedirectStandardOutput $stdout -RedirectStandardError $stderr

try {
  $ready = $false
  for ($i = 0; $i -lt 100; $i++) {
    Start-Sleep -Milliseconds 100
    try {
      $h = Invoke-RestMethod "$base/api/health" -TimeoutSec 1
      if ($h.ok) { $ready = $true; break }
    } catch { }
  }
  if (-not $ready) { throw "daemon did not become healthy; see $stderr" }

  $body = @{
    id = 'smoke-demo'
    name = 'Smoke Demo'
    profile_id = 'mock_dungeon'
    seed = $Seed
    max_episodes = 1
    tick_ms = 20
    human_resume_ms = 200
    auto_start = $true
  } | ConvertTo-Json
  $null = Invoke-RestMethod -Method Post "$base/api/instances" -ContentType 'application/json' -Body $body

  $snap = $null
  $deadline = (Get-Date).AddSeconds($TimeoutSec)
  while ((Get-Date) -lt $deadline) {
    Start-Sleep -Milliseconds 100
    $snap = Invoke-RestMethod "$base/api/instances/smoke-demo"
    if ($snap.status -in @('stopped', 'error')) { break }
  }
  if ($null -eq $snap) { throw 'no snapshot' }
  if ($snap.status -eq 'error') { throw "engine error: $($snap.stats.last_error)" }
  if ($snap.stats.actions -lt 5) { throw "too few actions: $($snap.stats.actions)" }
  if ($snap.stats.wins -lt 1) { throw "demo did not win: wins=$($snap.stats.wins) progress=$($snap.stats.progress)" }

  [pscustomobject]@{
    status = $snap.status
    actions = $snap.stats.actions
    wins = $snap.stats.wins
    losses = $snap.stats.losses
    progress = $snap.stats.progress
    best_progress = $snap.stats.best_progress
    reward = $snap.stats.reward
    decisions = $snap.stats.decisions
    errors = $snap.stats.errors
  } | ConvertTo-Json

  Write-Host 'SMOKE PASS'
} catch {
  Write-Host "SMOKE FAIL: $_"
  if (Test-Path $stderr) { Get-Content $stderr -Tail 40 | Write-Host }
  exit 1
} finally {
  if ($proc -and -not $proc.HasExited) {
    Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
    $proc.WaitForExit(5000) | Out-Null
  }
  Remove-Item -Recurse -Force $data -ErrorAction SilentlyContinue
}
