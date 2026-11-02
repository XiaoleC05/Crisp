#Requires -Version 7.0
<#
.SYNOPSIS
    Installs (or refreshes) the project-level Codex Stop hook for Crisp.
.DESCRIPTION
    Writes <repo>/.codex/hooks.json with an absolute path to bin/crisp.exe, the
    "command" handler type, timeout 30 seconds (the only field name Codex
    accepts for the deadline) and a status message shown by the Codex TUI.

    Codex only loads project hooks for a trusted project, and only runs hooks
    that have been reviewed/trusted (interactive TUI) or explicitly allowed for
    automation with --dangerously-bypass-hook-trust. See docs/codex-e2e.md.
.PARAMETER CrispBinary
    Absolute path to the Crisp binary. Defaults to <repo>/bin/crisp.exe.
.PARAMETER TimeoutSeconds
    Hook deadline in seconds, written as "timeout" (not timeoutSec/timeout_sec).
.PARAMETER HooksPath
    Output path. Defaults to <repo>/.codex/hooks.json.
.EXAMPLE
    pwsh -File scripts/install-codex-hook.ps1
.EXAMPLE
    pwsh -File scripts/install-codex-hook.ps1 -TimeoutSeconds 30 -Force
#>
[CmdletBinding()]
param(
    [string]$CrispBinary = '',
    [int]$TimeoutSeconds = 30,
    [string]$HooksPath = '',
    [switch]$Force
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
if (-not $CrispBinary) { $CrispBinary = Join-Path $repoRoot 'bin\crisp.exe' }
if (-not $HooksPath) { $HooksPath = Join-Path $repoRoot '.codex\hooks.json' }
if (-not [System.IO.Path]::IsPathRooted($HooksPath)) { $HooksPath = Join-Path $repoRoot $HooksPath }

if ($TimeoutSeconds -lt 1 -or $TimeoutSeconds -gt 600) {
    throw 'TimeoutSeconds must be between 1 and 600.'
}
$resolvedBinary = [System.IO.Path]::GetFullPath($CrispBinary)
if (-not (Test-Path $resolvedBinary)) {
    throw ('Crisp binary not found: ' + $resolvedBinary + '. Run scripts/build.ps1 first.')
}

# Codex parses "command" as an executable path followed by arguments; keep the
# path unquoted (the default install path contains no spaces).
$commandLine = $resolvedBinary
$config = [ordered]@{
    hooks = [ordered]@{
        Stop = @(
            [ordered]@{
                hooks = @(
                    [ordered]@{
                        type          = 'command'
                        command       = $commandLine
                        timeout       = $TimeoutSeconds
                        statusMessage = 'Checking response quality'
                    }
                )
            }
        )
    }
}
$json = $config | ConvertTo-Json -Depth 10

$hooksDir = Split-Path -Parent $HooksPath
if (-not (Test-Path $hooksDir)) {
    New-Item -ItemType Directory -Force -Path $hooksDir | Out-Null
}
if ((Test-Path $HooksPath) -and -not $Force) {
    throw ('Refusing to overwrite existing ' + $HooksPath + '; pass -Force to replace it.')
}
[System.IO.File]::WriteAllText($HooksPath, $json, (New-Object System.Text.UTF8Encoding($false)))

Write-Host ('wrote ' + $HooksPath)
Write-Host '--- hooks.json ---'
Write-Host $json
Write-Host ''
Write-Host 'Next steps (details in docs/codex-e2e.md):'
Write-Host '  1. Trust this project in Codex (TUI prompt, or [projects.<path>] trust_level = "trusted" in CODEX_HOME/config.toml).'
Write-Host '  2. Review and trust the hook in the Codex TUI on first use.'
Write-Host '  3. For automation-only runs, pass --dangerously-bypass-hook-trust to codex exec.'
Write-Host '  4. Set QUALITY_GATE_BASE_URL / QUALITY_GATE_MODEL (and optionally QUALITY_GATE_API_KEY) for the critic.'
