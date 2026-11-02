#Requires -Version 7.0
<#
.SYNOPSIS
    Real Claude Code CLI end-to-end validation of the Crisp Stop hook.
.DESCRIPTION
    Boots an isolated CLAUDE_CONFIG_DIR plus a scratch project under .tmp/,
    starts a deterministic Anthropic Messages-API mock (test/e2e/mockanthropic)
    and an OpenAI-compatible mock critic (test/e2e/mockcritic), writes a
    USER-level settings.json with a blocking Stop hook that wraps bin/crisp.exe
    in test/e2e/hooktap (for payload capture), and runs the real Claude Code CLI
    non-interactively (-p --output-format json).

    Scenarios (both use the real CLI; no network, no real API key):
      S1 block -> rewrite -> second Stop pass
         first Stop: stop_hook_active=false, critic returns material block, Crisp
         emits {"decision":"block",...}; Claude Code regenerates the answer; the
         second Stop has stop_hook_active=true and Crisp emits {} without calling
         the critic. Exactly 2 model requests + 1 critic request, num_turns=2,
         final result is the rewritten answer.
      S2 checked-in .claude/settings.json (direct bin/crisp.exe, no wrapper)
         the shipped project-level hook config is copied into the isolated user
         settings and must drive the same block -> rewrite flow.

    If no Claude Code CLI is found, the script prints SKIPPED with every path it
    searched and exits 0. The real user config (~/.claude) is never touched:
    CLAUDE_CONFIG_DIR points into the run directory and the hash of the real
    user settings.json is compared before/after.

    Artifacts (settings, captures, request logs, claude stdout/stderr,
    summary.json with the CLI version and binary path) are kept under the run
    directory; started processes and environment variables are cleaned up in
    finally.
.PARAMETER ClaudePath
    Explicit path to the Claude Code CLI (claude.exe / claude.cmd). When omitted,
    the script searches PATH, VS Code / VS Code Insiders / Cursor extension
    bundles, and common native/npm install locations.
.PARAMETER SkipBuild
    Do not rebuild bin/crisp.exe or the mock/test binaries; reuses existing ones.
.PARAMETER ScenarioTimeoutSec
    Per-CLI-invocation timeout in seconds. Default 180.
.PARAMETER WorkRoot
    Scratch root; defaults to <repo>/.tmp/e2e/claude (gitignored).
.EXAMPLE
    pwsh -File scripts/e2e-claude.ps1
.EXAMPLE
    pwsh -File scripts/e2e-claude.ps1 -ClaudePath C:\path\to\claude.exe -SkipBuild
#>
[CmdletBinding()]
param(
    [string]$ClaudePath = '',
    [switch]$SkipBuild,
    [int]$ScenarioTimeoutSec = 180,
    [string]$WorkRoot = ''
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
if (-not $WorkRoot) { $WorkRoot = Join-Path $repoRoot '.tmp\e2e\claude' }
$runStamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$script:runDir = Join-Path $WorkRoot ('run-' + $runStamp)
$script:binDir = Join-Path $script:runDir 'bin'
$script:logsDir = Join-Path $script:runDir 'logs'
$script:crispExe = Join-Path $repoRoot 'bin\crisp.exe'
$script:mockAnthropicExe = Join-Path $script:binDir 'mockanthropic.exe'
$script:mockCriticExe = Join-Path $script:binDir 'mockcritic.exe'
$script:tapExe = Join-Path $script:binDir 'hooktap.exe'
$script:servers = [System.Collections.Generic.List[object]]::new()
$script:results = [System.Collections.Generic.List[object]]::new()
$script:searchedPaths = [System.Collections.Generic.List[string]]::new()
$script:claudeVersion = ''
$script:goVersion = ''
$script:resolvedClaudePath = ''
$script:claudeProcess = $null
$script:skipped = $false
$script:skipReason = ''
$script:realUserSettings = Join-Path $env:USERPROFILE '.claude\settings.json'
$script:realUserSettingsHashBefore = ''
$script:realUserSettingsHashAfter = ''

# Canned conversation. The flawed answer carries flawMarker so the mock critic
# (auto mode) returns block/material; the improved answer carries none.
$script:flawMarker = 'CRISP_FLAWED_ANSWER'
$script:prompt = '请用中文简单回答：Crisp 的 Stop hook 是做什么的？'
$script:flawed = @'
这是一个模拟的、质量不合格的原始回答。它故意包含一个用于触发质量门禁的标记：CRISP_FLAWED_ANSWER。此外它还有明显的重复与铺垫问题：先说结论，再说一次同样的结论，最后把结论再重复一遍。

总之，核心结论是：这个回答包含标记，这个回答包含标记，这个回答包含标记。为了确保自然语言长度超过 Critic 阈值，这里继续补充一些没有信息量的背景说明，重复解释一遍刚才已经说过的事情，最后再重复一次相同的结论，用来模拟真实世界中信息密度很低的回答。
'@
$script:improved = @'
Crisp 的 Stop hook 在 Agent 生成最终回答之后、交付用户之前运行一次质量检查：先用确定性规则发现重复与无效铺垫，再按需要调用 LLM Critic 做语义审查；只有明确存在实质问题时才要求 Agent 完整重写一次。这是重写后的合格回答，保留必要信息，不包含任何触发门禁的标记。
'@

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
    # with FileShare.ReadWrite so a still-alive helper never causes a sharing
    # violation on Windows.
    $stream = [System.IO.File]::Open($Path, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::ReadWrite)
    try {
        $reader = New-Object System.IO.StreamReader($stream)
        try { return $reader.ReadToEnd() } finally { $reader.Dispose() }
    }
    finally { $stream.Dispose() }
}

function Read-JsonlText([string]$Text) {
    $records = [System.Collections.Generic.List[object]]::new()
    if (-not $Text) { return $records }
    foreach ($line in $Text.Split([char]10)) {
        $trimmed = $line.Trim()
        if (-not $trimmed) { continue }
        $records.Add(($trimmed | ConvertFrom-Json))
    }
    return $records
}

function Read-JsonlFile([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) { return [System.Collections.Generic.List[object]]::new() }
    return (Read-JsonlText (Read-TextShared $Path))
}

function Get-QualityEvent([string]$CrispStderr) {
    $events = @(Read-JsonlText $CrispStderr)
    $quality = @($events | Where-Object { $_.event -eq 'quality_check' })
    if ($quality.Count -eq 0) { return $null }
    return $quality[$quality.Count - 1]
}

function Get-EventByName([string]$CrispStderr, [string]$EventName) {
    $events = @(Read-JsonlText $CrispStderr)
    $matched = @($events | Where-Object { $_.event -eq $EventName })
    if ($matched.Count -eq 0) { return $null }
    return $matched[$matched.Count - 1]
}

function Get-FreeTcpPort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    $listener.Stop()
    return $port
}

function Test-TcpPort([int]$Port) {
    $client = [System.Net.Sockets.TcpClient]::new()
    try {
        $task = $client.ConnectAsync('127.0.0.1', $Port)
        if ($task.Wait(250)) { return $client.Connected }
        return $false
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

function Get-ExtensionVersion([string]$Name) {
    $m = [regex]::Match($Name, 'anthropic\.claude-code-([0-9]+\.[0-9]+\.[0-9]+)')
    if ($m.Success) { return [version]$m.Groups[1].Value }
    return [version]'0.0.0'
}

function Find-ClaudeCli {
    if ($ClaudePath) {
        $script:searchedPaths.Add('explicit -ClaudePath: ' + $ClaudePath)
        if (Test-Path -LiteralPath $ClaudePath) { return $ClaudePath }
        return ''
    }
    $onPath = Get-Command claude -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($onPath -and $onPath.Source) {
        $script:searchedPaths.Add('PATH: ' + $onPath.Source)
        if (Test-Path -LiteralPath $onPath.Source) { return $onPath.Source }
    }
    else {
        $script:searchedPaths.Add('PATH: claude (not found)')
    }

    # Claude Code is bundled as a native binary inside its VS Code extension.
    $extensionRoots = @(
        (Join-Path $env:USERPROFILE '.vscode\extensions'),
        (Join-Path $env:USERPROFILE '.vscode-insiders\extensions'),
        (Join-Path $env:USERPROFILE '.cursor\extensions')
    )
    $extensionBinaries = [System.Collections.Generic.List[object]]::new()
    foreach ($root in $extensionRoots) {
        $script:searchedPaths.Add((Join-Path $root 'anthropic.claude-code-*\resources\native-binary\claude.exe'))
        if (-not (Test-Path -LiteralPath $root)) { continue }
        foreach ($dir in @(Get-ChildItem -LiteralPath $root -Directory -Filter 'anthropic.claude-code-*' -ErrorAction SilentlyContinue)) {
            $candidate = Join-Path $dir.FullName 'resources\native-binary\claude.exe'
            if (Test-Path -LiteralPath $candidate) {
                $extensionBinaries.Add([pscustomobject]@{ Path = $candidate; Version = (Get-ExtensionVersion $dir.Name) })
            }
        }
    }
    if ($extensionBinaries.Count -gt 0) {
        $best = @($extensionBinaries | Sort-Object -Property Version -Descending)[0]
        return $best.Path
    }

    $known = @(
        (Join-Path $env:APPDATA 'npm\claude.cmd'),
        (Join-Path $env:APPDATA 'npm\claude.exe'),
        (Join-Path $env:LOCALAPPDATA 'Programs\claude\claude.exe'),
        (Join-Path $env:USERPROFILE '.local\bin\claude.exe'),
        (Join-Path $env:USERPROFILE '.local\bin\claude'),
        (Join-Path $env:USERPROFILE '.claude\local\claude.exe')
    )
    foreach ($candidate in $known) {
        $script:searchedPaths.Add($candidate)
        if (Test-Path -LiteralPath $candidate) { return $candidate }
    }
    return ''
}

function Write-Summary {
    New-Item -ItemType Directory -Force -Path $script:runDir | Out-Null
    $artifactFiles = @()
    if (Test-Path -LiteralPath $script:logsDir) {
        $artifactFiles = @(Get-ChildItem -LiteralPath $script:logsDir -File -ErrorAction SilentlyContinue | ForEach-Object { $_.FullName })
    }
    $summary = [ordered]@{
        skipped                    = $script:skipped
        skipReason                 = $script:skipReason
        searchedPaths              = @($script:searchedPaths)
        claudePath                 = $script:resolvedClaudePath
        claudeVersion              = $script:claudeVersion
        goVersion                  = $script:goVersion
        repoRoot                   = $repoRoot
        workDir                    = $script:runDir
        command                    = 'pwsh -File scripts/e2e-claude.ps1'
        prompt                     = $script:prompt
        flawedMarker               = $script:flawMarker
        realUserSettings           = $script:realUserSettings
        realUserSettingsHashBefore = $script:realUserSettingsHashBefore
        realUserSettingsHashAfter  = $script:realUserSettingsHashAfter
        results                    = @($script:results)
        artifacts                  = [ordered]@{
            logs       = $script:logsDir
            logFiles   = $artifactFiles
            summaryPath = (Join-Path $script:runDir 'summary.json')
        }
    }
    Write-Utf8NoBom (Join-Path $script:runDir 'summary.json') ($summary | ConvertTo-Json -Depth 8)
}

function Stop-Scenario($Scenario) {
    foreach ($process in @($Scenario.Anthropic.Process, $Scenario.Critic.Process)) {
        if ($process -and -not $process.HasExited) {
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
            try { Wait-Process -Id $process.Id -Timeout 5 -ErrorAction SilentlyContinue } catch { }
        }
    }
}

function New-TapHookCommand([string]$CapturePath) {
    $tap = $script:tapExe.Replace("'", "''")
    $crisp = $script:crispExe.Replace("'", "''")
    $capture = $CapturePath.Replace("'", "''")
    return ("& '" + $tap + "' -crisp '" + $crisp + "' -out '" + $capture + "'")
}

function New-Scenario {
    param(
        [string]$Tag,
        [string[]]$Answers,
        [string]$HookCommand = '',
        [switch]$UseInstalledSettings
    )
    $homeDir = Join-Path $script:runDir ('home-' + $Tag)
    $projectDir = Join-Path $script:runDir ('project-' + $Tag)
    New-Item -ItemType Directory -Force -Path $homeDir, $projectDir | Out-Null

    $answersPath = Join-Path $script:logsDir ($Tag + '-answers.json')
    Write-Utf8NoBom $answersPath (@{ answers = $Answers } | ConvertTo-Json -Depth 5)
    $anthropicLog = Join-Path $script:logsDir ($Tag + '-anthropic.jsonl')
    $criticLog = Join-Path $script:logsDir ($Tag + '-critic.jsonl')
    Remove-Item $anthropicLog, $criticLog -Force -ErrorAction SilentlyContinue

    $anthropic = Start-MockServer -Exe $script:mockAnthropicExe -Arguments @('-answers', $answersPath, '-log', $anthropicLog) -Label ($Tag + '-anthropic')
    try {
        $critic = Start-MockServer -Exe $script:mockCriticExe -Arguments @('-verdict', 'auto', '-marker', $script:flawMarker, '-log', $criticLog) -Label ($Tag + '-critic')
    }
    catch {
        if ($anthropic -and -not $anthropic.Process.HasExited) { Stop-Process -Id $anthropic.Process.Id -Force -ErrorAction SilentlyContinue }
        throw
    }

    # Environment inherited by the CLI and therefore by the Stop hook process.
    $env:CLAUDE_CONFIG_DIR = $homeDir
    $env:ANTHROPIC_BASE_URL = 'http://127.0.0.1:' + $anthropic.Port
    $env:ANTHROPIC_API_KEY = 'crisp-e2e-key'
    $env:ANTHROPIC_AUTH_TOKEN = 'crisp-e2e-key'
    $env:DISABLE_TELEMETRY = '1'
    $env:DISABLE_ERROR_REPORTING = '1'
    $env:DISABLE_AUTOUPDATER = '1'
    $env:CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC = '1'
    $env:NO_COLOR = '1'
    $env:QUALITY_GATE_BASE_URL = 'http://127.0.0.1:' + $critic.Port + '/v1'
    $env:QUALITY_GATE_MODEL = 'mock-critic'
    $env:QUALITY_GATE_API_KEY = 'crisp-e2e-key'
    $env:QUALITY_GATE_MODE = 'balanced'
    $env:QUALITY_GATE_CRITIC_TIMEOUT_MS = '5000'
    $env:QUALITY_GATE_TOTAL_TIMEOUT_MS = '20000'

    $settingsPath = Join-Path $homeDir 'settings.json'
    if ($UseInstalledSettings) {
        $installedPath = Join-Path $repoRoot '.claude\settings.json'
        if (-not (Test-Path -LiteralPath $installedPath)) { throw ('missing checked-in ' + $installedPath) }
        Write-Utf8NoBom $settingsPath ([System.IO.File]::ReadAllText($installedPath))
    }
    else {
        $settings = [ordered]@{
            hooks = [ordered]@{
                Stop = @(
                    [ordered]@{
                        matcher = ''
                        hooks   = @(
                            [ordered]@{
                                type          = 'command'
                                command       = $HookCommand
                                shell         = 'powershell'
                                timeout       = 60
                                statusMessage = 'Crisp quality gate E2E'
                            }
                        )
                    }
                )
            }
        }
        Write-Utf8NoBom $settingsPath ($settings | ConvertTo-Json -Depth 10)
    }

    return [pscustomobject]@{
        Tag         = $Tag
        HomeDir     = $homeDir
        ProjectDir  = $projectDir
        Settings    = $settingsPath
        Anthropic   = $anthropic
        Critic      = $critic
        AnthropicLog = $anthropicLog
        CriticLog   = $criticLog
    }
}

function Invoke-ClaudeCli {
    param(
        [string]$Label,
        $Scenario
    )
    $promptPath = Join-Path $script:logsDir ($Label + '-prompt.txt')
    Write-Utf8NoBom $promptPath $script:prompt
    $stdoutPath = Join-Path $script:logsDir ($Label + '-claude.out')
    $stderrPath = Join-Path $script:logsDir ($Label + '-claude.err')
    Remove-Item $stdoutPath, $stderrPath -Force -ErrorAction SilentlyContinue

    $claudeArgs = @('-p', '--model', 'sonnet', '--dangerously-skip-permissions', '--output-format', 'json')
    $file = $script:resolvedClaudePath
    if ($file -match '\.(cmd|bat)$') {
        # Start-Process cannot execute a .cmd/.bat directly; go through cmd.exe.
        $claudeArgs = @('/c', $file) + $claudeArgs
        $file = $env:ComSpec
    }
    $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
    $process = Start-Process -FilePath $file -ArgumentList $claudeArgs -WorkingDirectory $Scenario.ProjectDir -RedirectStandardInput $promptPath -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath -PassThru -NoNewWindow
    $script:claudeProcess = $process
    $finished = $process.WaitForExit($ScenarioTimeoutSec * 1000)
    $stopwatch.Stop()
    if (-not $finished) {
        & taskkill /PID $process.Id /T /F 2>&1 | Out-Null
        try { $process.WaitForExit(5000) | Out-Null } catch { }
    }
    $exitCode = -1
    if ($process.HasExited) { $exitCode = $process.ExitCode }
    $stdout = ''
    $stderr = ''
    if (Test-Path -LiteralPath $stdoutPath) { $stdout = Read-TextShared $stdoutPath }
    if (Test-Path -LiteralPath $stderrPath) { $stderr = Read-TextShared $stderrPath }
    return [pscustomobject]@{
        Finished   = $finished
        ExitCode   = $exitCode
        ElapsedMs  = $stopwatch.ElapsedMilliseconds
        Stdout     = $stdout
        Stderr     = $stderr
        StdoutPath = $stdoutPath
        StderrPath = $stderrPath
    }
}

function Invoke-Scenario {
    param(
        [string]$Name,
        [scriptblock]$Body
    )
    Write-Section $Name
    try {
        & $Body
    }
    catch {
        Add-Result $Name $false ('EXCEPTION: ' + $_.Exception.Message)
    }
}

function Get-FileHashIfPresent([string]$Path) {
    if (Test-Path -LiteralPath $Path) { return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash }
    return '(absent)'
}

# ---------------------------------------------------------------------------
# Discovery
# ---------------------------------------------------------------------------
Write-Section 'Claude Code CLI discovery'
$script:resolvedClaudePath = Find-ClaudeCli
if (-not $script:resolvedClaudePath) {
    $script:skipped = $true
    $script:skipReason = 'Claude Code CLI not found'
    Write-Host 'SKIPPED: Claude Code CLI not found. Searched:' -ForegroundColor Yellow
    foreach ($searched in $script:searchedPaths) { Write-Host ('  - ' + $searched) }
    Write-Summary
    Write-Host ('summary: ' + (Join-Path $script:runDir 'summary.json'))
    exit 0
}
$script:claudeVersion = (& $script:resolvedClaudePath --version 2>&1 | Out-String).Trim()
if ($LASTEXITCODE -ne 0) {
    throw ('claude --version failed for ' + $script:resolvedClaudePath + ': ' + $script:claudeVersion)
}
$script:goVersion = (& go version 2>&1 | Out-String).Trim()
Write-Host ('claude: ' + $script:claudeVersion)
Write-Host ('path:   ' + $script:resolvedClaudePath)
Write-Host ('go:     ' + $script:goVersion)
Write-Host ('run:    ' + $script:runDir)

# ---------------------------------------------------------------------------
# Setup
# ---------------------------------------------------------------------------
New-Item -ItemType Directory -Force -Path $script:binDir, $script:logsDir | Out-Null
$script:realUserSettingsHashBefore = Get-FileHashIfPresent $script:realUserSettings

if (-not $SkipBuild) {
    Write-Section 'Build'
    & pwsh -NoProfile -File (Join-Path $repoRoot 'scripts\build.ps1') | Out-Host
    if ($LASTEXITCODE -ne 0) { throw 'scripts/build.ps1 failed' }
    Push-Location $repoRoot
    try {
        & go build -o $script:tapExe ./test/e2e/hooktap
        if ($LASTEXITCODE -ne 0) { throw 'failed to build hooktap' }
        & go build -o $script:mockAnthropicExe ./test/e2e/mockanthropic
        if ($LASTEXITCODE -ne 0) { throw 'failed to build mockanthropic' }
        & go build -o $script:mockCriticExe ./test/e2e/mockcritic
        if ($LASTEXITCODE -ne 0) { throw 'failed to build mockcritic' }
    }
    finally { Pop-Location }
}
foreach ($required in @($script:crispExe, $script:tapExe, $script:mockAnthropicExe, $script:mockCriticExe)) {
    if (-not (Test-Path -LiteralPath $required)) {
        throw ('missing ' + $required + '; run without -SkipBuild or run scripts/build.ps1')
    }
}

# ---------------------------------------------------------------------------
# Scenarios
# ---------------------------------------------------------------------------
try {
    Invoke-Scenario 'S1 block -> rewrite -> second Stop pass' {
        $captures = Join-Path $script:logsDir 's1-captures.jsonl'
        Remove-Item $captures -Force -ErrorAction SilentlyContinue
        $scenario = New-Scenario -Tag 's1' -Answers @($script:flawed, $script:improved) -HookCommand (New-TapHookCommand $captures)
        try {
            $run = Invoke-ClaudeCli -Label 's1' -Scenario $scenario
            Assert-True $run.Finished ('claude -p did not finish within ' + $ScenarioTimeoutSec + 's')
            Assert-Equal $run.ExitCode 0 'claude exit code'
            $final = $run.Stdout.Trim() | ConvertFrom-Json
            Assert-Equal $final.is_error $false 'is_error'
            Assert-Equal $final.subtype 'success' 'subtype'
            Assert-Equal $final.num_turns 2 'num_turns (exactly one rewrite)'
            Assert-Equal $final.result $script:improved 'final result is the rewritten answer'
            Assert-NotContains ([string]$final.result) $script:flawMarker 'flawed answer text must be gone'

            $records = @(Read-JsonlFile $captures)
            $events = @($records | ForEach-Object { $_.event })
            Assert-Equal ($events -join ',') 'start,end,start,end' 'Stop hook invocation sequence'
            $ends = @($records | Where-Object { $_.event -eq 'end' })
            Assert-Equal $ends.Count 2 'Stop hook completions'
            $first = $ends[0]
            $second = $ends[1]

            # Real Claude Code payload shape (no Codex-only turn_id).
            Assert-Equal $first.hook_event_name 'Stop' 'first hook_event_name'
            Assert-Equal $first.stop_hook_active $false 'first stop_hook_active'
            Assert-Equal $second.stop_hook_active $true 'second stop_hook_active'
            Assert-True (-not [string]::IsNullOrEmpty($first.session_id)) 'session_id present'
            Assert-True (-not [string]::IsNullOrEmpty($first.transcript_path)) 'transcript_path present'
            Assert-True ($first.last_assistant_message_present -eq $true) 'last_assistant_message present'
            Assert-True ($first.last_assistant_message_runes -gt 60) 'last_assistant_message runes'
            $requiredKeys = @('cwd', 'hook_event_name', 'last_assistant_message', 'permission_mode', 'session_id', 'stop_hook_active', 'transcript_path')
            $keys = @($first.top_level_keys)
            foreach ($key in $requiredKeys) {
                Assert-True ($keys -contains $key) ('payload missing required key ' + $key)
            }
            Assert-True (-not ($keys -contains 'turn_id')) 'Claude payload must not contain the Codex-only turn_id field'

            # Crisp decisions (user-level settings hook is blocking).
            Assert-Equal $first.decision 'block' 'first Stop decision'
            Assert-Equal $second.decision 'pass' 'second Stop decision'
            Assert-Contains $first.crisp_stdout '"decision":"block"' 'first Stop stdout is a block decision'
            Assert-Contains $first.crisp_stdout '请重新输出完整最终回答' 'first Stop reason is the fixed rewrite instruction'
            Assert-Equal ($second.crisp_stdout.Trim()) '{}' 'second Stop stdout'
            $firstQuality = Get-QualityEvent $first.crisp_stderr
            Assert-True ($null -ne $firstQuality) 'first Stop quality_check event'
            Assert-Equal $firstQuality.decision 'block' 'first quality_check decision'
            Assert-Equal $firstQuality.critic_called $true 'first quality_check critic_called'
            Assert-Equal $firstQuality.fail_open $false 'first quality_check fail_open'
            $skip = Get-EventByName $second.crisp_stderr 'quality_skip'
            Assert-True ($null -ne $skip) 'second Stop must log quality_skip'
            Assert-Equal $skip.skip_reason 'stop_hook_active' 'second Stop skip reason'

            # Exactly 2 conversation model requests and exactly 1 critic request.
            $modelCalls = @(Read-JsonlFile $scenario.AnthropicLog | Where-Object { $_.path -eq '/v1/messages' -and -not $_.auxiliary })
            Assert-Equal $modelCalls.Count 2 'model requests (initial + exactly one rewrite)'
            Assert-Equal (Read-JsonlFile $scenario.CriticLog).Count 1 'critic requests (second Stop must short-circuit)'

            Add-Result 'S1 block-rewrite-loopguard' $true ('real Claude payload, hook fired 2x via user-level settings; stop_hook_active false->true; model_requests=2; critic_requests=1; claude_elapsed_ms=' + $run.ElapsedMs)
        }
        finally { Stop-Scenario $scenario }
    }

    Invoke-Scenario 'S2 checked-in .claude/settings.json (direct crisp.exe)' {
        $installedPath = Join-Path $repoRoot '.claude\settings.json'
        if (-not (Test-Path -LiteralPath $installedPath)) {
            throw ('missing checked-in ' + $installedPath)
        }
        $installedCommand = ''
        try {
            $installedJson = [System.IO.File]::ReadAllText($installedPath) | ConvertFrom-Json
            $installedCommand = [string]$installedJson.hooks.Stop[0].hooks[0].command
        }
        catch {
            throw ('checked-in .claude/settings.json is not a valid hook config: ' + $_.Exception.Message)
        }
        $pathMatch = [regex]::Match($installedCommand, "'([^']+crisp\.exe)'")
        if (-not $pathMatch.Success -or -not (Test-Path -LiteralPath $pathMatch.Groups[1].Value)) {
            Write-Host ('NOTE: skipping S2; checked-in .claude/settings.json command points at a missing binary: ' + $installedCommand) -ForegroundColor Yellow
        }
        else {
            $scenario = New-Scenario -Tag 's2' -Answers @($script:flawed, $script:improved) -UseInstalledSettings
            try {
                $run = Invoke-ClaudeCli -Label 's2' -Scenario $scenario
                Assert-True $run.Finished ('claude -p did not finish within ' + $ScenarioTimeoutSec + 's')
                Assert-Equal $run.ExitCode 0 'claude exit code'
                $final = $run.Stdout.Trim() | ConvertFrom-Json
                Assert-Equal $final.is_error $false 'is_error'
                Assert-Equal $final.num_turns 2 'num_turns (exactly one rewrite)'
                Assert-Equal $final.result $script:improved 'final result is the rewritten answer'
                Assert-NotContains ([string]$final.result) $script:flawMarker 'flawed answer text must be gone'
                $modelCalls = @(Read-JsonlFile $scenario.AnthropicLog | Where-Object { $_.path -eq '/v1/messages' -and -not $_.auxiliary })
                Assert-Equal $modelCalls.Count 2 'model requests (initial + exactly one rewrite)'
                Assert-Equal (Read-JsonlFile $scenario.CriticLog).Count 1 'critic requests (second Stop must short-circuit)'
                Add-Result 'S2 installed-config' $true ('checked-in .claude/settings.json (' + $installedCommand + ') invoked by real Claude Code; 1 rewrite; model_requests=2; critic_requests=1; claude_elapsed_ms=' + $run.ElapsedMs)
            }
            finally { Stop-Scenario $scenario }
        }
    }
}
catch {
    Add-Result 'harness' $false ('EXCEPTION: ' + $_.Exception.Message)
}
finally {
    if ($script:claudeProcess -and -not $script:claudeProcess.HasExited) {
        & taskkill /PID $script:claudeProcess.Id /T /F 2>&1 | Out-Null
    }
    foreach ($process in $script:servers) {
        if ($process -and -not $process.HasExited) {
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
            try { Wait-Process -Id $process.Id -Timeout 5 -ErrorAction SilentlyContinue } catch { }
        }
    }
    foreach ($name in @(
            'CLAUDE_CONFIG_DIR',
            'ANTHROPIC_BASE_URL',
            'ANTHROPIC_API_KEY',
            'ANTHROPIC_AUTH_TOKEN',
            'DISABLE_TELEMETRY',
            'DISABLE_ERROR_REPORTING',
            'DISABLE_AUTOUPDATER',
            'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC',
            'NO_COLOR',
            'QUALITY_GATE_BASE_URL',
            'QUALITY_GATE_MODEL',
            'QUALITY_GATE_API_KEY',
            'QUALITY_GATE_MODE',
            'QUALITY_GATE_CRITIC_TIMEOUT_MS',
            'QUALITY_GATE_TOTAL_TIMEOUT_MS'
        )) {
        Remove-Item ('Env:' + $name) -ErrorAction SilentlyContinue
    }
}

# ---------------------------------------------------------------------------
# Isolation check + summary
# ---------------------------------------------------------------------------
$script:realUserSettingsHashAfter = Get-FileHashIfPresent $script:realUserSettings
if ($script:realUserSettingsHashBefore -eq $script:realUserSettingsHashAfter) {
    Add-Result 'isolation real ~/.claude untouched' $true ('SHA256 ' + $script:realUserSettingsHashAfter)
}
else {
    Add-Result 'isolation real ~/.claude untouched' $false ('real user settings changed! before=' + $script:realUserSettingsHashBefore + ' after=' + $script:realUserSettingsHashAfter)
}

Write-Summary
Write-Section 'Summary'
$failed = @($script:results | Where-Object { -not $_.Passed })
Write-Host ('claude: ' + $script:claudeVersion + ' (' + $script:resolvedClaudePath + ')')
Write-Host ('go:     ' + $script:goVersion)
Write-Host ('run:    ' + $script:runDir)
Write-Host ('summary:' + (Join-Path $script:runDir 'summary.json'))
if ($failed.Count -gt 0) {
    Write-Host ('FAILED: ' + $failed.Count + ' of ' + $script:results.Count + ' checks') -ForegroundColor Red
    exit 1
}
Write-Host ('ALL ' + $script:results.Count + ' CHECKS PASSED') -ForegroundColor Green
exit 0
