[CmdletBinding()]
param(
  [ValidateSet('all','build','desktop','test','race','run','eval','audit','native','release','release-windows','clean')]
  [string]$Task = 'all'
)

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

function Invoke-Build {
  Write-Host '== go build ./... =='
  & go build ./...
  if ($LASTEXITCODE -ne 0) { throw "go build failed ($LASTEXITCODE)" }

  New-Item -ItemType Directory -Force bin | Out-Null
  $targets = @(
    @{ Name = 'goplayd';     Pkg = './cmd/goplayd' },
    @{ Name = 'goplay';      Pkg = './cmd/goplay' },
    @{ Name = 'goplay-model';Pkg = './cmd/goplay-model' },
    @{ Name = 'goplay-eval'; Pkg = './cmd/goplay-eval' },
    @{ Name = 'goplay-audit';Pkg = './cmd/goplay-audit' }
  )
  foreach ($t in $targets) {
    $out = Join-Path 'bin' ($t.Name + '.exe')
    Write-Host ("== build {0} ==" -f $out)
    & go build -o $out $t.Pkg
    if ($LASTEXITCODE -ne 0) { throw "build $($t.Name) failed ($LASTEXITCODE)" }
  }
}

function Invoke-Desktop {
  New-Item -ItemType Directory -Force bin | Out-Null
  Write-Host '== build desktop shell =='
  & go build -o bin\goplay-desktop.exe ./cmd/goplay-desktop
  if ($LASTEXITCODE -ne 0) { throw "desktop build failed ($LASTEXITCODE)" }
}

function Invoke-Test {
  & go test ./... -count=1
  if ($LASTEXITCODE -ne 0) { throw "go test failed ($LASTEXITCODE)" }
}

function Invoke-Race {
  & go test -race ./internal/engine ./internal/api ./internal/memory ./internal/scheduler -count=1
  if ($LASTEXITCODE -ne 0) { throw "race tests failed ($LASTEXITCODE)" }
}

function Invoke-Release([string]$Target) {
  & go run ./cmd/goplay-release -target $Target -out dist
  if ($LASTEXITCODE -ne 0) { throw "release failed ($LASTEXITCODE)" }
}

switch ($Task) {
  'build' { Invoke-Build }
  'desktop' { Invoke-Desktop }
  'test'  { Invoke-Test }
  'race'  { Invoke-Race }
  'eval'  {
    & go run ./cmd/goplay-eval -quick -json -report-dir reports
    if ($LASTEXITCODE -ne 0) { throw "eval failed ($LASTEXITCODE)" }
  }
  'audit' {
    & go run ./cmd/goplay-audit -json -out reports/audit-report.json
    if ($LASTEXITCODE -ne 0) { throw "audit failed ($LASTEXITCODE)" }
  }
  'native' {
    & powershell -NoProfile -ExecutionPolicy Bypass -File native/build.ps1
    if ($LASTEXITCODE -ne 0) { throw "native build failed ($LASTEXITCODE)" }
  }
  'release' { Invoke-Release 'all' }
  'release-windows' { Invoke-Release 'windows' }
  'clean' {
    Remove-Item -Recurse -Force bin, .tmpdata, .tmp-smoke, dist -ErrorAction SilentlyContinue
    Write-Host 'cleaned build outputs and temporary data'
  }
  'all' { Invoke-Build; Invoke-Desktop; Invoke-Test; Invoke-Race }
  'run' {
    & go run ./cmd/goplayd -addr 127.0.0.1:8420 -data .tmpdata -llm offline
  }
}
