#Requires -Version 7.0
<#
.SYNOPSIS
    Real DeepSeek Harness headless end-to-end validation of the Crisp guard flow.
.DESCRIPTION
    Proves the committed DeepSeek Harness (Cordis) host plugin
    (integrations/dsh-plugin-crisp) against the real dsh launcher, without
    network access or real credentials:

      1. Creates an isolated DSH_HOME plus a scratch profile from the shipped
         headless template (the real ~/.dsh is never touched).
      2. Registers the plugin bundle with
         'dsh plugin --profile <scratch> add <repo>/integrations/dsh-plugin-crisp'.
      3. Starts a deterministic Messages-compatible mock
         (test/e2e/mockmessages) and the OpenAI-compatible mock critic
         (test/e2e/mockcritic); the mock serves a flawed answer first and the
         improved answer for the rewrite turn.
      4. Runs the real dsh headless agent with guard mode pointed at both mocks
         and asserts the full flow:
           first delivered answer blocked by Crisp
           -> exactly one user-role followup carrying the fixed rewrite
              instruction (internal/policy/decision.go)
           -> the rewrite turn is checked and passes
           -> final stdout is the rewritten (improved) answer.
      5. Writes summary.json and keeps request logs, gate logs and stdout/stderr
         under .tmp/e2e/dsh/run-<stamp>; started processes and environment
         variables are cleaned up in finally.

    If no DeepSeek Harness CLI is found, the script prints SKIPPED with every
    path it searched and exits 0.

    Exit codes: 0 = all checks passed or SKIPPED, 1 = one or more checks failed.
.PARAMETER DshPath
    Explicit path to the dsh launcher (dsh.cmd / dsh.exe). When omitted, the
    script searches PATH and the default DeepSeek Harness installation
    locations.
.PARAMETER SkipBuild
    Do not rebuild bin/crisp.exe or the mock binaries; reuses existing ones.
.PARAMETER ScenarioTimeoutSec
    Timeout for one real dsh headless invocation in seconds. Default 240.
.PARAMETER WorkRoot
    Scratch root; defaults to <repo>/.tmp/e2e/dsh (gitignored).
.EXAMPLE
    pwsh -File scripts/e2e-dsh.ps1
.EXAMPLE
    pwsh -File scripts/e2e-dsh.ps1 -DshPath 'C:\Program Files\DeepSeek Harness\resources\runtime\cli\bin\dsh.cmd' -SkipBuild
#>
[CmdletBinding()]
param(
    [string]$DshPath = '',
    [switch]$SkipBuild,
    [int]$ScenarioTimeoutSec = 240,
    [string]$WorkRoot = ''
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
if (-not $WorkRoot) { $WorkRoot = Join-Path $repoRoot '.tmp\e2e\dsh' }
$runStamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$script:runDir = Join-Path $WorkRoot ('run-' + $runStamp)
$script:binDir = Join-Path $WorkRoot 'bin'
$script:logsDir = Join-Path $script:runDir 'logs'
$script:dshHome = Join-Path $script:runDir 'dsh-home'
$script:profileName = 'crisp-e2e-' + $runStamp.ToLowerInvariant()
$script:pluginPath = Join-Path $repoRoot 'integrations\dsh-plugin-crisp'
$script:crispExe = Join-Path $repoRoot 'bin\crisp.exe'
$script:mockMessagesExe = Join-Path $script:binDir 'mockmessages.exe'
$script:mockCriticExe = Join-Path $script:binDir 'mockcritic.exe'
$script:servers = [System.Collections.Generic.List[object]]::new()
$script:results = [System.Collections.Generic.List[object]]::new()
$script:searchedPaths = [System.Collections.Generic.List[string]]::new()
$script:dshVersion = ''
$script:goVersion = ''
$script:resolvedDshPath = ''
$script:skipped = $false
$script:skipReason = ''
$script:startedAt = Get-Date
$script:dshProcess = $null
$script:realDshHome = Join-Path $env:USERPROFILE '.dsh'
$script:realProfilePath = Join-Path $script:realDshHome ('profiles\' + $script:profileName)
$script:managedEnvNames = @(
    'DSH_HOME',
    'DEEPSEEK_API_KEY',
    'DEEPSEEK_BASE_URL',
    'QUALITY_GATE_BASE_URL',
    'QUALITY_GATE_API_KEY',
    'QUALITY_GATE_MODEL',
    'QUALITY_GATE_MODE',
    'QUALITY_GATE_CRITIC_TIMEOUT_MS',
    'QUALITY_GATE_TOTAL_TIMEOUT_MS',
    'QUALITY_GATE_MIN_CRITIC_RUNES'
)
$script:envBackup = @{}

# Canned conversation. The flawed answer is the exact measured sample from the
# original manual E2E: short enough to need rule signals before the Critic is
# consulted, containing filler + duplicate sentences. The improved answer is a
# valid rewrite with no rule signal and fewer than the 120-rune Critic
# threshold, so the rewrite turn is reviewed without another Critic call.
$script:prompt = 'How many deployment replicas does this service use? Answer in Chinese.'
$script:flawed = @'
好的，这是一个很好的问题。首先，我来说明一下。

部署使用 42 个副本。部署使用 42 个副本。

总结：部署使用 42 个副本。
'@
$script:improved = '部署使用 42 个副本，配置文件位于 deploy/app.yaml，扩缩容阈值为 CPU 70%。'
# Present in the flawed answer only; mockcritic uses it to return block/material.
$script:criticMarker = '好的，这是一个很好的问题'
# Filled from internal/policy/decision.go so the harness checks the shipped
# instruction rather than a stale copy.
$script:rewriteInstruction = ''

function Write-Section([string]$Text) {
    Write-Host ''
    Write-Host ('=== ' + $Text + ' ===') -ForegroundColor Cyan
}

function Add-Result([string]$Name, [bool]$Passed, [string]$Detail) {
    $script:results.Add([pscustomobject]@{ Scenario = $Name; Passed = $Passed; Detail = $Detail })
    if ($Passed) {
        Write-Host ('PASS  ' + $Name + '  ' + $Detail) -ForegroundColor Green
    }
    else {
        Write-Host ('FAIL  ' + $Name + '  ' + $Detail) -ForegroundColor Red
    }
}

function Test-Check([string]$Name, [scriptblock]$Body) {
    try {
        & $Body
        Add-Result $Name $true 'ok'
    }
    catch {
        Add-Result $Name $false ('EXCEPTION: ' + $_.Exception.Message)
    }
}

function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}

function Assert-Equal($Actual, $Expected, [string]$Message) {
    $a = [string]$Actual
    $e = [string]$Expected
    if ($a -ne $e) {
        throw ($Message + ': expected [' + $e + '], got [' + $a + ']')
    }
}

function Assert-Contains([string]$Haystack, [string]$Needle, [string]$Message) {
    if (-not $Haystack.Contains($Needle)) { throw ($Message + ': missing [' + $Needle + ']') }
}

function Assert-NotContains([string]$Haystack, [string]$Needle, [string]$Message) {
    if ($Haystack.Contains($Needle)) { throw ($Message + ': unexpectedly found [' + $Needle + ']') }
}

function Write-Utf8NoBom([string]$Path, [string]$Text) {
    [System.IO.File]::WriteAllText($Path, $Text, (New-Object System.Text.UTF8Encoding($false)))
}

function Read-TextShared([string]$Path) {
    # Start-Process keeps redirected file handles open until the child exits; read
    # with FileShare.ReadWrite so a still-alive mock server never causes a
    # sharing violation on Windows.
    $stream = [System.IO.File]::Open($Path, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::ReadWrite)
    try {
        $reader = New-Object System.IO.StreamReader($stream)
        try { return $reader.ReadToEnd() } finally { $reader.Dispose() }
    }
    finally { $stream.Dispose() }
}

function Read-JsonlFile([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return @() }
    $records = [System.Collections.Generic.List[object]]::new()
    foreach ($line in (Read-TextShared $Path).Split([char]10)) {
        $trim = $line.Trim()
        if ($trim -eq '') { continue }
        try { $records.Add(($trim | ConvertFrom-Json)) } catch { }
    }
    return @($records)
}

function Read-NdjsonFile([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return @() }
    $records = [System.Collections.Generic.List[object]]::new()
    foreach ($line in (Read-TextShared $Path).Split([char]10)) {
        $trim = $line.Trim()
        if (-not $trim.StartsWith('{')) { continue }
        try { $records.Add(($trim | ConvertFrom-Json)) } catch { }
    }
    return @($records)
}

function Get-FreeTcpPort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try { return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port }
    finally { $listener.Stop() }
}

function Test-TcpPort([int]$Port) {
    $client = [System.Net.Sockets.TcpClient]::new()
    try {
        $task = $client.ConnectAsync('127.0.0.1', $Port)
        if (-not $task.Wait(250)) { return $false }
        return $client.Connected
    }
    catch { return $false }
    finally { $client.Dispose() }
}

function Start-MockServer {
    param(
        [string]$Exe,
        [string[]]$Arguments,
        [string]$Label
    )
    # A free port is chosen here so the harness never has to read the child's
    # redirected stdout while it runs (Windows keeps that handle open).
    $port = Get-FreeTcpPort
    $outFile = Join-Path $script:logsDir ($Label + '.out')
    $errFile = Join-Path $script:logsDir ($Label + '.err')
    Remove-Item $outFile, $errFile -Force -ErrorAction SilentlyContinue
    $fullArgs = @('-addr', ('127.0.0.1:' + $port)) + $Arguments
    $process = Start-Process -FilePath $Exe -ArgumentList $fullArgs -PassThru -RedirectStandardOutput $outFile -RedirectStandardError $errFile -NoNewWindow
    $script:servers.Add($process)
    $ready = $false
    for ($i = 0; $i -lt 100; $i++) {
        Start-Sleep -Milliseconds 100
        if ($process.HasExited) { break }
        if (Test-TcpPort $port) { $ready = $true; break }
    }
    if (-not $ready) {
        if (-not $process.HasExited) { Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue }
        throw ('mock server ' + $Label + ' did not become ready on port ' + $port)
    }
    return [pscustomobject]@{ Process = $process; Port = $port; OutFile = $outFile; ErrFile = $errFile }
}

function Find-DshCli {
    if ($DshPath) {
        $script:searchedPaths.Add('explicit -DshPath: ' + $DshPath)
        if (Test-Path -LiteralPath $DshPath) { return (Resolve-Path -LiteralPath $DshPath).Path }
        return ''
    }
    $onPath = Get-Command dsh -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($onPath -and $onPath.Source) {
        $script:searchedPaths.Add('PATH: ' + $onPath.Source)
        if (Test-Path -LiteralPath $onPath.Source) { return $onPath.Source }
    }
    else {
        $script:searchedPaths.Add('PATH: dsh (not found)')
    }
    $installRoots = @()
    if ($env:LOCALAPPDATA) { $installRoots += (Join-Path $env:LOCALAPPDATA 'Programs\DeepSeek Harness') }
    if ($env:ProgramFiles) { $installRoots += (Join-Path $env:ProgramFiles 'DeepSeek Harness') }
    $programFilesX86 = [Environment]::GetEnvironmentVariable('ProgramFiles(x86)', 'Process')
    if ($programFilesX86) { $installRoots += (Join-Path $programFilesX86 'DeepSeek Harness') }
    foreach ($root in $installRoots) {
        $candidate = Join-Path $root 'resources\runtime\cli\bin\dsh.cmd'
        $script:searchedPaths.Add($candidate)
        if (Test-Path -LiteralPath $candidate) { return $candidate }
    }
    foreach ($root in $installRoots) {
        $candidate = Join-Path $root 'resources\runtime\cli\bin\dsh.exe'
        $script:searchedPaths.Add($candidate)
        if (Test-Path -LiteralPath $candidate) { return $candidate }
    }
    return ''
}

function Get-DshVersion {
    param([string]$Dsh)
    $raw = (& $Dsh --version 2>&1 | Out-String).Trim()
    $versionMatches = [regex]::Matches($raw, '\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?')
    if ($versionMatches.Count -gt 0) { return $versionMatches[$versionMatches.Count - 1].Value }
    return $raw
}

function Get-RewriteInstruction {
    $goFile = Join-Path $repoRoot 'internal\policy\decision.go'
    if (-not (Test-Path -LiteralPath $goFile)) {
        throw ('cannot find ' + $goFile + '; the rewrite instruction assertion needs the Go source')
    }
    $source = [System.IO.File]::ReadAllText($goFile)
    $match = [regex]::Match($source, 'RewriteReason\s*=\s*"([^"]+)"')
    if (-not $match.Success) { throw 'policy.RewriteReason not found in internal/policy/decision.go' }
    return $match.Groups[1].Value
}

function YAML-Token([string]$Value) {
    # Single-quoted YAML scalar: single quotes are doubled, everything else is literal.
    return ("'" + $Value.Replace("'", "''") + "'")
}

function Invoke-DshHeadless {
    param([string]$OverlayPath, [string]$StdoutPath, [string]$StderrPath)
    $wrapperPath = Join-Path $script:logsDir 'run-dsh.cmd'
    $wrapper = @(
        '@echo off',
        'setlocal DisableDelayedExpansion',
        ('"' + $script:resolvedDshPath + '" --profile ' + $script:profileName + ' --patch "' + $OverlayPath + '" --json "' + $script:prompt + '"'),
        'exit /b %errorlevel%'
    ) -join [Environment]::NewLine
    Write-Utf8NoBom $wrapperPath ($wrapper + [Environment]::NewLine)
    Remove-Item $StdoutPath, $StderrPath -Force -ErrorAction SilentlyContinue
    $process = Start-Process -FilePath $env:ComSpec -ArgumentList @('/d', '/c', ('"' + $wrapperPath + '"')) -PassThru -RedirectStandardOutput $StdoutPath -RedirectStandardError $StderrPath -NoNewWindow
    $script:dshProcess = $process
    $finished = $process.WaitForExit($ScenarioTimeoutSec * 1000)
    if (-not $finished) {
        & taskkill /PID $process.Id /T /F 2>&1 | Out-Null
        throw ('dsh headless did not finish within ' + $ScenarioTimeoutSec + 's')
    }
    return $process.ExitCode
}

function Write-Summary {
    New-Item -ItemType Directory -Force -Path $script:runDir | Out-Null
    $artifactFiles = @()
    if (Test-Path -LiteralPath $script:logsDir) {
        $artifactFiles = @(Get-ChildItem -LiteralPath $script:logsDir -File -ErrorAction SilentlyContinue | ForEach-Object { $_.FullName })
    }
    $elapsedMs = [int]((Get-Date) - $script:startedAt).TotalMilliseconds
    $summary = [ordered]@{
        skipped            = $script:skipped
        skipReason         = $script:skipReason
        searchedPaths      = @($script:searchedPaths)
        dshPath            = $script:resolvedDshPath
        dshVersion         = $script:dshVersion
        goVersion          = $script:goVersion
        pluginPath         = $script:pluginPath
        crispPath          = $script:crispExe
        profile            = $script:profileName
        dshHome            = $script:dshHome
        runDir             = $script:runDir
        command            = 'pwsh -File scripts/e2e-dsh.ps1'
        prompt             = $script:prompt
        criticMarker       = $script:criticMarker
        rewriteInstruction = $script:rewriteInstruction
        elapsedMs          = $elapsedMs
        results            = @($script:results)
        artifacts          = [ordered]@{
            logs        = $script:logsDir
            logFiles    = $artifactFiles
            summaryPath = (Join-Path $script:runDir 'summary.json')
        }
    }
    Write-Utf8NoBom (Join-Path $script:runDir 'summary.json') ($summary | ConvertTo-Json -Depth 8)
}

# ---------------------------------------------------------------------------
# Isolated environment and discovery
# ---------------------------------------------------------------------------
New-Item -ItemType Directory -Force -Path $script:runDir, $script:binDir, $script:logsDir, $script:dshHome | Out-Null
foreach ($name in $script:managedEnvNames) {
    $script:envBackup[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$env:DSH_HOME = $script:dshHome

Write-Section 'DeepSeek Harness CLI discovery'
$script:resolvedDshPath = Find-DshCli
if (-not $script:resolvedDshPath) {
    $script:skipped = $true
    $script:skipReason = 'DeepSeek Harness CLI not found'
    try { $script:goVersion = (& go version 2>&1 | Out-String).Trim() } catch { $script:goVersion = '' }
    Write-Host 'SKIPPED: DeepSeek Harness CLI not found. Searched:' -ForegroundColor Yellow
    foreach ($searched in $script:searchedPaths) { Write-Host ('  - ' + $searched) }
    Write-Summary
    Write-Host ('summary: ' + (Join-Path $script:runDir 'summary.json'))
    exit 0
}
$script:dshVersion = Get-DshVersion -Dsh $script:resolvedDshPath
$script:goVersion = (& go version 2>&1 | Out-String).Trim()
$script:rewriteInstruction = Get-RewriteInstruction
Write-Host ('dsh:    ' + $script:dshVersion + ' (' + $script:resolvedDshPath + ')')
Write-Host ('go:     ' + $script:goVersion)
Write-Host ('plugin: ' + $script:pluginPath)
Write-Host ('run:    ' + $script:runDir)
Write-Host ('home:   ' + $script:dshHome + ' (isolated; real ' + $script:realDshHome + ' untouched)')

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------
if (-not $SkipBuild) {
    Write-Section 'Build'
    & pwsh -NoProfile -File (Join-Path $repoRoot 'scripts\build.ps1') | Out-Host
    if ($LASTEXITCODE -ne 0) { throw 'scripts/build.ps1 failed' }
    Push-Location $repoRoot
    try {
        & go build -o $script:mockMessagesExe ./test/e2e/mockmessages
        if ($LASTEXITCODE -ne 0) { throw 'failed to build mockmessages' }
        & go build -o $script:mockCriticExe ./test/e2e/mockcritic
        if ($LASTEXITCODE -ne 0) { throw 'failed to build mockcritic' }
    }
    finally { Pop-Location }
}
foreach ($required in @($script:crispExe, $script:mockMessagesExe, $script:mockCriticExe)) {
    if (-not (Test-Path -LiteralPath $required)) {
        throw ('missing ' + $required + '; run without -SkipBuild or run scripts/build.ps1')
    }
}

# ---------------------------------------------------------------------------
# Scenario: guard block -> one followup -> rewrite turn pass
# ---------------------------------------------------------------------------
try {
    Write-Section 'Scratch profile + bundle registration'

    $answersPath = Join-Path $script:logsDir 'answers.json'
    Write-Utf8NoBom $answersPath (@{ answers = @($script:flawed, $script:improved) } | ConvertTo-Json -Depth 5)

    $profileJson = Join-Path $script:dshHome ('profiles\' + $script:profileName + '\package.json')
    $createOut = Join-Path $script:logsDir 'profile-create.out'
    $createErr = Join-Path $script:logsDir 'profile-create.err'
    & $script:resolvedDshPath --profile $script:profileName --from-default-profile headless --dump-config 1> $createOut 2> $createErr
    $createExit = $LASTEXITCODE
    Test-Check 'scratch profile created from headless template' {
        Assert-Equal $createExit 0 'dsh --from-default-profile exit code'
        Assert-True (Test-Path -LiteralPath $profileJson) 'scratch profile package.json exists'
        $manifest = [System.IO.File]::ReadAllText($profileJson) | ConvertFrom-Json
        Assert-Contains ($manifest.dsh.profile.bundles -join ',') '@deepseek-ai/dsh-headless' 'headless bundle selected'
        Assert-True ($null -ne $manifest.dependencies) 'fresh profile has a dependencies object'
    }

    $addOut = Join-Path $script:logsDir 'plugin-add.out'
    $addErr = Join-Path $script:logsDir 'plugin-add.err'
    & $script:resolvedDshPath plugin --profile $script:profileName add $script:pluginPath 1> $addOut 2> $addErr
    $addExit = $LASTEXITCODE
    Test-Check 'bundle registered in scratch profile' {
        Assert-Equal $addExit 0 'dsh plugin add exit code'
        $manifest = [System.IO.File]::ReadAllText($profileJson) | ConvertFrom-Json
        Assert-Contains ($manifest.dsh.profile.bundles -join ',') '@local/crisp-quality-gate' 'bundle selected in dsh.profile.bundles'
        $spec = $manifest.dependencies.'@local/crisp-quality-gate'
        Assert-True ($null -ne $spec) 'profile dependency saw the plugin package'
        Assert-Contains ([string]$spec) 'link:' 'local bundle installed as a link'
    }

    $messageLog = Join-Path $script:logsDir 'mockmessages.jsonl'
    $criticLog = Join-Path $script:logsDir 'mockcritic.jsonl'
    Remove-Item $messageLog, $criticLog -Force -ErrorAction SilentlyContinue
    $messages = Start-MockServer -Exe $script:mockMessagesExe -Arguments @('-answers', $answersPath, '-log', $messageLog, '-rewrite-marker', $script:rewriteInstruction) -Label 'mockmessages'
    $critic = Start-MockServer -Exe $script:mockCriticExe -Arguments @('-verdict', 'auto', '-marker', $script:criticMarker, '-log', $criticLog) -Label 'mockcritic'

    $gateLog = Join-Path $script:logsDir 'gate.jsonl'
    $stdoutPath = Join-Path $script:logsDir 'dsh-headless.out'
    $stderrPath = Join-Path $script:logsDir 'dsh-headless.err'
    $overlayPath = Join-Path $script:runDir 'guard-overlay.yml'
    $criticURL = 'http://127.0.0.1:' + $critic.Port + '/v1'
    $messagesURL = 'http://127.0.0.1:' + $messages.Port
    $overlay = @(
        '# Generated by scripts/e2e-dsh.ps1: force guard mode onto the scratch profile.',
        '- id: crisp-quality-gate',
        '  config:',
        '    mode: guard',
        ('    binaryPath: ' + (YAML-Token $script:crispExe)),
        '    timeoutMs: 30000',
        '    minRunes: 1',
        ('    logFile: ' + (YAML-Token $gateLog)),
        '    env:',
        ('      QUALITY_GATE_BASE_URL: ' + (YAML-Token $criticURL)),
        ('      QUALITY_GATE_API_KEY: ' + (YAML-Token 'crisp-e2e-key')),
        ('      QUALITY_GATE_MODEL: ' + (YAML-Token 'mock-critic')),
        ('      QUALITY_GATE_MODE: ' + (YAML-Token 'balanced'))
    ) -join [Environment]::NewLine
    Write-Utf8NoBom $overlayPath ($overlay + [Environment]::NewLine)

    # The Critic child receives these through the plugin's allowlisted
    # passthrough; the overlay env config above is the authoritative source.
    $env:DEEPSEEK_API_KEY = 'crisp-e2e-key'
    $env:DEEPSEEK_BASE_URL = $messagesURL
    $env:QUALITY_GATE_BASE_URL = $criticURL
    $env:QUALITY_GATE_API_KEY = 'crisp-e2e-key'
    $env:QUALITY_GATE_MODEL = 'mock-critic'
    $env:QUALITY_GATE_MODE = 'balanced'

    Write-Section 'Real dsh headless guard run'
    $runStart = Get-Date
    $exitCode = Invoke-DshHeadless -OverlayPath $overlayPath -StdoutPath $stdoutPath -StderrPath $stderrPath
    $runElapsedMs = [int]((Get-Date) - $runStart).TotalMilliseconds
    Write-Host ('exit=' + $exitCode + ' elapsed_ms=' + $runElapsedMs)

    $stdoutRecords = @(Read-NdjsonFile $stdoutPath)
    $gateRecords = @(Read-JsonlFile $gateLog)
    $messageRecords = @(Read-JsonlFile $messageLog)
    $criticRecords = @(Read-JsonlFile $criticLog)

    Test-Check 'dsh headless guard run exited 0' {
        Assert-Equal $exitCode 0 'dsh exit code'
        Assert-True ($stdoutRecords.Count -gt 0) 'dsh emitted JSON events on stdout'
    }

    Test-Check 'two turns delivered and final stdout is the rewritten answer' {
        $final = @($stdoutRecords | Where-Object { $_.type -eq 'final' })
        Assert-Equal $final.Count 1 'exactly one final event'
        $turnStarts = @($stdoutRecords | Where-Object { $_.type -eq 'status' -and $_.phase -eq 'turn_start' })
        Assert-Equal $turnStarts.Count 2 'exactly two turns (one rewrite)'
        Assert-Equal $final[0].text $script:improved 'final text is the improved answer'
        Assert-NotContains ([string]$final[0].text) $script:criticMarker 'flawed answer text is gone from the final answer'
    }

    Test-Check 'first answer blocked by Crisp with rule signals + Critic' {
        $checks = @($gateRecords | Where-Object { $_.event -eq 'crisp_quality_check' })
        Assert-Equal $checks.Count 2 'two gate checks (initial + rewrite)'
        $first = $checks[0]
        Assert-Equal $first.decision 'block' 'initial decision'
        Assert-Equal $first.fail_open $false 'initial fail_open'
        Assert-Equal $first.critic_called $true 'initial critic_called'
        Assert-Equal $first.turn 1 'initial turn id'
        Assert-True (@($first.rule_types).Count -ge 1) 'initial rule_types non-empty'
    }

    Test-Check 'rewrite turn checked and passed' {
        $checks = @($gateRecords | Where-Object { $_.event -eq 'crisp_quality_check' })
        $second = $checks[1]
        Assert-Equal $second.decision 'pass' 'rewrite decision'
        Assert-Equal $second.fail_open $false 'rewrite fail_open'
        Assert-Equal $second.turn 2 'rewrite turn id'
    }

    Test-Check 'exactly one user-role followup with the fixed rewrite instruction' {
        $followups = @($gateRecords | Where-Object { $_.event -eq 'crisp_guard_followup' })
        Assert-Equal $followups.Count 1 'one guard followup'
        Assert-Equal $followups[0].turn 1 'followup belongs to turn 1'
        Assert-Equal $followups[0].action 'followup' 'followup action'
        Assert-Equal $followups[0].instruction_chars $script:rewriteInstruction.Length 'followup instruction length'
        $mainCalls = @($messageRecords | Where-Object { -not $_.auxiliary })
        Assert-Equal $mainCalls.Count 2 'two conversation model calls'
        Assert-Equal $mainCalls[0].answer_selected 'initial' 'first model call served the flawed answer'
        Assert-Equal ([bool]$mainCalls[0].rewrite_marker) $false 'first model call has no rewrite marker'
        Assert-Equal $mainCalls[1].answer_selected 'rewrite' 'second model call served the improved answer'
        Assert-Equal ([bool]$mainCalls[1].rewrite_marker) $true 'rewrite request carries the fixed instruction'
        Assert-Equal $mainCalls[1].continuation $true 'rewrite request carries assistant history'
    }

    Test-Check 'auxiliary model calls neutralized (no answer-sequence shift)' {
        $auxiliary = @($messageRecords | Where-Object { $_.auxiliary })
        Assert-True ($auxiliary.Count -ge 1) 'session-title auxiliary call seen and neutralized'
        foreach ($record in $auxiliary) { Assert-Equal $record.answer_selected 'auxiliary' 'auxiliary request served the neutral answer' }
        $mainCalls = @($messageRecords | Where-Object { -not $_.auxiliary })
        $indexes = @($mainCalls | ForEach-Object { [int]$_.answer_index } | Sort-Object)
        Assert-Equal ($indexes -join ',') '0,1' 'main answer sequence consumed exactly once per turn'
    }

    Test-Check 'mock Critic saw exactly one check and returned block' {
        Assert-Equal $criticRecords.Count 1 'one Critic request'
        Assert-Equal $criticRecords[0].verdict 'block' 'Critic verdict'
        Assert-Equal ([bool]$criticRecords[0].marker_found) $true 'Critic saw the flawed-answer marker'
    }

    Test-Check 'no contained gate errors or fail-open records' {
        $errors = @($gateRecords | Where-Object { $_.event -eq 'crisp_guard_error' -or $_.event -eq 'crisp_quality_error' })
        Assert-Equal $errors.Count 0 'no gate error records'
        $failOpen = @($gateRecords | Where-Object { $_.event -eq 'crisp_quality_check' -and $_.fail_open -eq $true })
        Assert-Equal $failOpen.Count 0 'no fail-open checks'
        $activation = @($gateRecords | Where-Object { $_.event -eq 'crisp_quality_gate_active' })
        Assert-Equal $activation.Count 1 'one activation record'
        Assert-Equal $activation[0].mode 'guard' 'gate mounted in guard mode'
        Assert-Equal $activation[0].binary_exists $true 'configured crisp.exe exists'
    }

    Write-Host ('gate log: ' + $gateLog)
    Write-Host ('mock messages log: ' + $messageLog)
    Write-Host ('mock critic log: ' + $criticLog)
}
catch {
    Add-Result 'harness' $false ('EXCEPTION: ' + $_.Exception.Message)
}
finally {
    foreach ($process in $script:servers) {
        if ($process -and -not $process.HasExited) {
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
            try { Wait-Process -Id $process.Id -Timeout 5 -ErrorAction SilentlyContinue } catch { }
        }
    }
    if ($script:dshProcess -and -not $script:dshProcess.HasExited) {
        & taskkill /PID $script:dshProcess.Id /T /F 2>&1 | Out-Null
    }
    foreach ($name in $script:managedEnvNames) {
        $previous = $script:envBackup[$name]
        if ($null -eq $previous) {
            [Environment]::SetEnvironmentVariable($name, $null, 'Process')
        }
        else {
            [Environment]::SetEnvironmentVariable($name, $previous, 'Process')
        }
    }
}

# ---------------------------------------------------------------------------
# Isolation check + summary
# ---------------------------------------------------------------------------
Test-Check 'isolation: scratch DSH_HOME only, real ~/.dsh untouched' {
    Assert-True ($script:dshHome.StartsWith($script:runDir)) 'DSH_HOME is inside the run directory'
    Assert-True (Test-Path -LiteralPath $script:dshHome) 'scratch DSH_HOME exists'
    Assert-True (-not (Test-Path -LiteralPath $script:realProfilePath)) 'scratch profile was not created under the real user DSH home'
}

Write-Summary
Write-Section 'Summary'
$failed = @($script:results | Where-Object { -not $_.Passed })
Write-Host ('dsh:    ' + $script:dshVersion + ' (' + $script:resolvedDshPath + ')')
Write-Host ('go:     ' + $script:goVersion)
Write-Host ('plugin: ' + $script:pluginPath)
Write-Host ('run:    ' + $script:runDir)
Write-Host ('summary:' + (Join-Path $script:runDir 'summary.json'))
if ($failed.Count -gt 0) {
    Write-Host ('FAILED: ' + $failed.Count + ' of ' + $script:results.Count + ' checks') -ForegroundColor Red
    exit 1
}
Write-Host ('ALL ' + $script:results.Count + ' CHECKS PASSED') -ForegroundColor Green
exit 0