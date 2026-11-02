#Requires -Version 7.0
<#
.SYNOPSIS
    Builds the Crisp binary and prints the Go toolchain version.
.DESCRIPTION
    Uses only the Go standard library toolchain. The output path defaults to
    <repo>/bin/crisp.exe. Run from anywhere; the script resolves the repo root
    from its own location.
.EXAMPLE
    pwsh -File scripts/build.ps1
.EXAMPLE
    pwsh -File scripts/build.ps1 -Output bin/crisp.exe
#>
[CmdletBinding()]
param(
    [string]$Output = 'bin/crisp.exe'
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Push-Location $repoRoot
try {
    $goVersion = & go version
    if ($LASTEXITCODE -ne 0) {
        throw 'Go toolchain not found on PATH. Install Go 1.26+ and retry.'
    }
    Write-Host ('go: ' + $goVersion)

    $outPath = $Output
    if (-not [System.IO.Path]::IsPathRooted($outPath)) {
        $outPath = Join-Path $repoRoot $outPath
    }
    $outDir = Split-Path -Parent $outPath
    if (-not (Test-Path $outDir)) {
        New-Item -ItemType Directory -Force -Path $outDir | Out-Null
    }

    & go build -trimpath -o $outPath ./cmd/crisp
    if ($LASTEXITCODE -ne 0) {
        throw ('go build failed with exit code ' + $LASTEXITCODE)
    }
    $built = Get-Item $outPath
    Write-Host ('built ' + $built.FullName + ' (' + $built.Length + ' bytes)')
}
finally {
    Pop-Location
}
