# Builds bin/kash.exe with the version stamping `make build` applies, for
# Windows machines without make.
#
# A plain `go build` falls back to dev-<commit>: the release tag cannot be
# recovered from the binary alone. This passes the tag, commit and build time
# through -ldflags, as the release workflow does.
#
# Usage (from anywhere):  powershell -File scripts\build.ps1

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
Push-Location $root
try {
    $module  = 'github.com/akashicode/kash'
    $version = git describe --tags --always --dirty
    if ($LASTEXITCODE -ne 0 -or -not $version) { $version = 'dev' }
    $commit = git rev-parse --short HEAD
    if ($LASTEXITCODE -ne 0 -or -not $commit) { $commit = 'none' }
    $date = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')

    $ldflags = "-s -w -X $module/cmd.version=$version -X $module/cmd.commit=$commit -X $module/cmd.buildDate=$date"
    go build -trimpath -ldflags $ldflags -o bin/kash.exe ./cmd/kash
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

    Write-Host "Built: bin/kash.exe ($version, $commit, $date)"
}
finally {
    Pop-Location
}
