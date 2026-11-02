#Requires -Version 7.0
<#
.SYNOPSIS
    Installs (or refreshes) the Claude Code Stop hook for Crisp.
.DESCRIPTION
    Merges a Crisp Stop hook into a Claude Code settings file. The default
    target is the project-level <repo>/.claude/settings.json; existing settings
    and other hooks are preserved, only Crisp's own Stop handler is added or
    replaced.

    The handler is a blocking command hook (async is deliberately NOT set: a
    Stop decision is only honored while the hook blocks the turn), with
    "timeout": 30 seconds and the absolute path to bin/crisp.exe. On Windows the
    handler is pinned to the PowerShell shell so the Windows path is never
    parsed by Git Bash (Claude Code defaults to bash when Git Bash is present).

    Claude Code only loads project/local settings hooks for a trusted
    workspace and may ask for explicit hook review. See
    docs/integrations/claude-code.md.
.PARAMETER CrispBinary
    Absolute path to the Crisp binary. Defaults to <repo>/bin/crisp.exe.
.PARAMETER TimeoutSeconds
    Hook deadline in seconds, written as "timeout" (the Claude Code field name).
.PARAMETER SettingsPath
    Settings file to write/merge. Defaults to <repo>/.claude/settings.json.
    Use $env:USERPROFILE\.claude\settings.json to install for every project
    (that file is user-owned: back it up first).
.PARAMETER Force
    Replace an existing Crisp Stop hook entry, or rewrite a settings file that
    is not valid JSON (a timestamped .bak copy is made first). Never deletes
    unrelated settings or hooks.
.EXAMPLE
    pwsh -File scripts/install-claude-hook.ps1
.EXAMPLE
    pwsh -File scripts/install-claude-hook.ps1 -Force -TimeoutSeconds 30
.EXAMPLE
    pwsh -File scripts/install-claude-hook.ps1 -SettingsPath "$env:USERPROFILE\.claude\settings.json"
#>
[CmdletBinding()]
param(
    [string]$CrispBinary = '',
    [int]$TimeoutSeconds = 30,
    [string]$SettingsPath = '',
    [switch]$Force
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
if (-not $CrispBinary) { $CrispBinary = Join-Path $repoRoot 'bin\crisp.exe' }
if (-not $SettingsPath) { $SettingsPath = Join-Path $repoRoot '.claude\settings.json' }
if (-not [System.IO.Path]::IsPathRooted($SettingsPath)) { $SettingsPath = Join-Path $repoRoot $SettingsPath }

if ($TimeoutSeconds -lt 1 -or $TimeoutSeconds -gt 600) {
    throw 'TimeoutSeconds must be between 1 and 600.'
}
$resolvedBinary = [System.IO.Path]::GetFullPath($CrispBinary)
if (-not (Test-Path -LiteralPath $resolvedBinary)) {
    throw ('Crisp binary not found: ' + $resolvedBinary + '. Run scripts/build.ps1 first.')
}
$settingsPathFull = [System.IO.Path]::GetFullPath($SettingsPath)

# Match a command that executes the Crisp binary, ignoring quoting style.
$isCrispCommand = {
    param($CommandText)
    if ($CommandText -isnot [string]) { return $false }
    return $CommandText -match "(?i)(?:^|[\\/'\s&])crisp(?:\.exe)?(?:$|[\s'])"
}

$escapedBinary = $resolvedBinary.Replace("'", "''")
$handler = [ordered]@{
    type          = 'command'
    command       = ('& ' + "'" + $escapedBinary + "'")
    timeout       = $TimeoutSeconds
    statusMessage = 'Checking response quality'
}
if ($IsWindows) {
    # Force PowerShell: Claude Code defaults to bash when Git Bash is installed,
    # and a raw Windows path would be mangled there.
    $handler['shell'] = 'powershell'
} else {
    # POSIX: a single quoted word is executed as an absolute command path.
    $handler['command'] = ("'" + $escapedBinary + "'")
}

# ---- load the existing settings file (if any) --------------------------------
$settings = [ordered]@{}
if (Test-Path -LiteralPath $settingsPathFull) {
    $raw = [System.IO.File]::ReadAllText($settingsPathFull)
    if ([string]::IsNullOrWhiteSpace($raw)) {
        $settings = [ordered]@{}
    }
    else {
        try {
            $parsed = $raw | ConvertFrom-Json -AsHashtable
        }
        catch {
            $parsed = $null
        }
        if ($parsed -isnot [System.Collections.IDictionary]) {
            if (-not $Force) {
                throw ('Refusing to modify ' + $settingsPathFull + ': it is not a valid JSON object. Back it up and pass -Force to replace it.')
            }
            $backupPath = $settingsPathFull + '.bak-' + (Get-Date -Format 'yyyyMMdd-HHmmss')
            Copy-Item -LiteralPath $settingsPathFull -Destination $backupPath
            Write-Host ('backed up unparsable settings to ' + $backupPath)
            $settings = [ordered]@{}
        }
        else {
            $settings = $parsed
        }
    }
}

# ---- merge the Crisp Stop hook, preserving everything else ------------------
$hooks = $settings['hooks']
if ($null -eq $hooks) {
    $hooks = [ordered]@{}
    $settings['hooks'] = $hooks
}
if ($hooks -isnot [System.Collections.IDictionary]) {
    throw ('Refusing to modify ' + $settingsPathFull + ': its "hooks" value is not a JSON object.')
}

$stopGroups = @()
if ($null -ne $hooks['Stop']) { $stopGroups = @($hooks['Stop']) }

$existingCrisp = 0
$keptGroups = [System.Collections.Generic.List[object]]::new()
foreach ($group in $stopGroups) {
    if ($null -eq $group) { continue }
    if ($group -isnot [System.Collections.IDictionary]) {
        throw ('Refusing to modify ' + $settingsPathFull + ': a hooks.Stop entry is not a JSON object.')
    }
    $handlers = $group['hooks']
    if ($null -eq $handlers) {
        $keptGroups.Add($group)
        continue
    }
    $keptHandlers = [System.Collections.Generic.List[object]]::new()
    foreach ($entry in @($handlers)) {
        $commandText = $null
        if ($entry -is [System.Collections.IDictionary]) { $commandText = $entry['command'] }
        if (& $isCrispCommand $commandText) {
            $existingCrisp++
        }
        else {
            $keptHandlers.Add($entry)
        }
    }
    if ($keptHandlers.Count -gt 0) {
        $group['hooks'] = $keptHandlers.ToArray()
        $keptGroups.Add($group)
    }
}

if ($existingCrisp -gt 0 -and -not $Force) {
    throw ('A Crisp Stop hook already exists in ' + $settingsPathFull + '; pass -Force to replace it.')
}

$keptGroups.Add([ordered]@{
    matcher = ''
    hooks   = @($handler)
})
$hooks['Stop'] = $keptGroups.ToArray()

$json = $settings | ConvertTo-Json -Depth 100
if ([string]::IsNullOrWhiteSpace($json) -or $json.TrimStart()[0] -ne '{') {
    throw 'internal error: generated settings JSON is not an object'
}
try {
    $null = $json | ConvertFrom-Json
}
catch {
    throw ('internal error: generated settings JSON is invalid: ' + $_.Exception.Message)
}

$settingsDir = Split-Path -Parent $settingsPathFull
if ($settingsDir -and -not (Test-Path -LiteralPath $settingsDir)) {
    New-Item -ItemType Directory -Force -Path $settingsDir | Out-Null
}
[System.IO.File]::WriteAllText($settingsPathFull, $json + [Environment]::NewLine, (New-Object System.Text.UTF8Encoding($false)))

# ---- verify what landed on disk ----------------------------------------------
$written = [System.IO.File]::ReadAllText($settingsPathFull) | ConvertFrom-Json -AsHashtable
$writtenStop = @($written['hooks']['Stop'])
$crispCount = 0
foreach ($group in $writtenStop) {
    foreach ($entry in @($group['hooks'])) {
        if ($entry -is [System.Collections.IDictionary] -and (& $isCrispCommand $entry['command'])) { $crispCount++ }
    }
}
if ($crispCount -ne 1) {
    throw ('verification failed: expected 1 Crisp Stop hook in ' + $settingsPathFull + ', found ' + $crispCount)
}
if ($existingCrisp -gt 0) {
    Write-Host ('replaced ' + $existingCrisp + ' existing Crisp Stop hook entry (other hooks preserved)')
}

Write-Host ('wrote ' + $settingsPathFull)
Write-Host '--- Stop hook ---'
Write-Host ($handler | ConvertTo-Json -Depth 10)
Write-Host ''
Write-Host 'Next steps (details in docs/integrations/claude-code.md):'
Write-Host '  1. Build the binary: pwsh -File scripts/build.ps1 (skip if bin/crisp.exe is current).'
Write-Host '  2. Trust the workspace and review/approve the hook when Claude Code asks (project/local settings only).'
Write-Host '  3. Keep the hook blocking: do NOT add "async": true, otherwise stop_hook_active/block has no effect.'
Write-Host '  4. Configure the critic (QUALITY_GATE_BASE_URL / QUALITY_GATE_MODEL, optional QUALITY_GATE_API_KEY);'
Write-Host '     without it Crisp only runs deterministic rules and fails open.'
Write-Host '  5. If settings.json has "disableAllHooks": true, no hook runs at all.'
Write-Host '  6. Smoke test: ''{"session_id":"s","hook_event_name":"Stop","stop_hook_active":false,"transcript_path":null,"last_assistant_message":"answer"}'' | bin\crisp.exe'
Write-Host '  7. Data boundary: the answer text (and a best-effort recent user message from the transcript) is sent to the critic.'
