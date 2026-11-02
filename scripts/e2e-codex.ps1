#Requires -Version 7.0
<#
.SYNOPSIS
    Real Codex CLI end-to-end validation of the Crisp Stop hook.
.DESCRIPTION
    Boots an isolated CODEX_HOME plus a scratch (non-git) project under .tmp/,
    starts a deterministic OpenAI Responses-API mock provider for Codex and an
    OpenAI-compatible mock critic for Crisp, installs a project-level
    .codex/hooks.json, and runs the real codex.exe non-interactively.

    Verified scenarios (docs/integrations/codex-cli.md has the measured numbers):
      S0  protocol replay of bin/crisp.exe with realistic hook JSON (12 cases)
      S1  pass: clean answer -> hook fires -> {} -> answer delivered once
      S2  block: critic blocks -> Codex rewrites -> second Stop has
          stop_hook_active=true -> {} -> exactly one rewrite
      S3  hook trust gate: without --dangerously-bypass-hook-trust the hook
          does not run (Codex requires reviewed/trusted hooks)
      S4a timeout=2 with a 4s hook: Codex marks the hook Failed and ignores its
          late block decision (no rewrite); the hook process is not terminated
          on this Windows build, which is recorded as measured behavior
      S4b the same late block within timeout=30 is used and triggers a rewrite
      S5  the checked-in .codex/hooks.json (direct bin/crisp.exe) is invoked
          for real and delivers a valid answer
      S6  project trust gate: project hooks only load for a trusted project

    The harness runs the hook through test/e2e/hooktap so the exact payload and
    Crisp's decision are observable; hooktap relays Crisp's stdout to Codex and
    is only a test wrapper. The installed hook in .codex/hooks.json points at
    bin/crisp.exe directly (verified by S5).
.PARAMETER CodexPath
    Path to codex.exe. Defaults to the CLI version validated on 2026-11-02.
.PARAMETER SkipBuild
    Do not rebuild bin/crisp.exe or the mock/test binaries.
.PARAMETER SkipReplay
    Skip the deterministic protocol replay (S0).
.PARAMETER WorkRoot
    Scratch root; defaults to <repo>/.tmp/e2e (gitignored).
.EXAMPLE
    pwsh -File scripts/e2e-codex.ps1
.EXAMPLE
    pwsh -File scripts/e2e-codex.ps1 -CodexPath C:\path\to\codex.exe -SkipBuild
#>
[CmdletBinding()]
param(
    [string]$CodexPath = '',
    [switch]$SkipBuild,
    [switch]$SkipReplay,
    [int]$ScenarioTimeoutSec = 150,
    [string]$WorkRoot = ''
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
if (-not $WorkRoot) { $WorkRoot = Join-Path $repoRoot '.tmp\e2e' }
$runStamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$script:runDir = Join-Path $WorkRoot ('run-' + $runStamp)
$script:binDir = Join-Path $script:runDir 'bin'
$script:logsDir = Join-Path $script:runDir 'logs'
$script:scratch = Join-Path $script:runDir 'project'
$script:codexHome = Join-Path $script:runDir 'home'
$script:codexHomeConfig = Join-Path $script:codexHome 'config.toml'
$script:mockProviderExe = Join-Path $script:binDir 'mockprovider.exe'
$script:mockCriticExe = Join-Path $script:binDir 'mockcritic.exe'
$script:tapExe = Join-Path $script:binDir 'hooktap.exe'
$script:crispExe = Join-Path $repoRoot 'bin\crisp.exe'
$script:servers = New-Object System.Collections.Generic.List[object]
$script:results = New-Object System.Collections.Generic.List[object]
$script:codexVersion = ''
$script:goVersion = ''
$script:flawMarker = '总之，核心结论是'
$script:blockJson = '{"decision":"block","reason":"e2e synthetic block decision"}'
$script:clean = '输入校验分两步：先检查类型，再检查范围。类型不符直接报错，范围越界给出可读提示；两条错误信息都包含字段名，便于定位。错误码稳定用于程序分支，文案稳定用于用户阅读。对可选字段，缺失与显式空值要区分处理：缺失表示沿用默认值，显式空值表示清空。校验通过后返回规范化结果。错误信息首行给出结论，次行给出字段名。'
$script:improved = '输入校验分两步：先检查类型，再检查范围。类型不符直接报错，范围越界给出可读提示；两条错误信息都包含字段名，便于定位。错误码稳定用于程序分支，文案稳定用于用户阅读。'
$script:flawed = @'
这个问题可以从三个层面来看。首先，需要确认输入数据的类型和取值范围；其次，需要处理空值、越界、格式错误等边界情况；最后，需要把校验结果稳定地输出给调用方，并附上可读的错误信息。简单来说，关键就是先确认输入，再处理边界，最后输出结果。

总之，核心结论是：先确认输入数据，再处理边界情况，最后输出结果。只要按这个顺序执行，就能保证校验逻辑正确、输出稳定、调用方容易定位问题。
'@
$script:expectedHookKeys = 'cwd,hook_event_name,last_assistant_message,model,permission_mode,session_id,stop_hook_active,transcript_path,turn_id'

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

function Read-JsonlText([string]$Text) {
    $records = New-Object System.Collections.Generic.List[object]
    if (-not $Text) { return $records }
    foreach ($line in $Text.Split([char]10)) {
        $trimmed = $line.Trim()
        if (-not $trimmed) { continue }
        $records.Add(($trimmed | ConvertFrom-Json))
    }
    return $records
}

function Read-JsonlFile([string]$Path) {
    if (-not (Test-Path $Path)) { return (New-Object System.Collections.Generic.List[object]) }
    # Open explicitly with FileShare.ReadWrite: the mock servers keep their log
    # file open while a scenario runs, and File.ReadAllText can fail with a
    # sharing violation against a Go writer on Windows.
    $stream = [System.IO.File]::Open($Path, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::ReadWrite)
    try {
        $reader = New-Object System.IO.StreamReader($stream)
        try { $text = $reader.ReadToEnd() } finally { $reader.Dispose() }
    }
    finally { $stream.Dispose() }
    return (Read-JsonlText $text)
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
    # A free port is chosen here (rather than letting the server choose) so the
    # harness never has to read the child's redirected stdout while it runs: on
    # Windows Start-Process keeps that file handle open, so a read while the
    # server is running fails with "file is being used by another process".
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

function Stop-Scenario($ScenarioEnv) {
    foreach ($process in @($ScenarioEnv.Provider.Process, $ScenarioEnv.Critic.Process)) {
        if ($process -and -not $process.HasExited) {
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
            try { Wait-Process -Id $process.Id -Timeout 5 -ErrorAction SilentlyContinue } catch { }
        }
    }
}

function Write-CodexHomeConfig {
    param(
        [int]$ProviderPort,
        [switch]$TrustProject
    )
    $lines = @(
        'model = "mock-model"',
        'model_provider = "mock"',
        'request_max_retries = 0',
        'stream_max_retries = 0',
        'check_for_update_on_startup = false',
        'project_root_markers = [".projectroot", ".git"]',
        '',
        '[model_providers.mock]',
        'name = "Mock Responses Provider"',
        ('base_url = "http://127.0.0.1:' + $ProviderPort + '/v1"'),
        'wire_api = "responses"',
        'env_key = "MOCK_API_KEY"',
        'supports_websockets = false',
        '',
        '[features]',
        'plugins = false'
    )
    if ($TrustProject) {
        $lines += @('', ("[projects.'" + $script:scratch + "']"), 'trust_level = "trusted"')
    }
    Write-Utf8NoBom $script:codexHomeConfig ($lines -join [Environment]::NewLine)
}

function Write-HooksConfig {
    param(
        [string]$CommandLine,
        [int]$TimeoutSeconds
    )
    $hooks = [ordered]@{
        hooks = [ordered]@{
            Stop = @(
                [ordered]@{
                    hooks = @(
                        [ordered]@{
                            type          = 'command'
                            command       = $CommandLine
                            timeout       = $TimeoutSeconds
                            statusMessage = 'Checking response quality'
                        }
                    )
                }
            )
        }
    }
    $json = $hooks | ConvertTo-Json -Depth 10
    $path = Join-Path $script:scratch '.codex\hooks.json'
    Write-Utf8NoBom $path $json
    return $path
}

function New-TapHookCommand {
    param(
        [string]$CapturePath,
        [int]$SleepMs = 0,
        [string]$StdoutFile = ''
    )
    $parts = @($script:tapExe, '-crisp', $script:crispExe, '-out', $CapturePath)
    if ($SleepMs -gt 0) { $parts += @('-sleep-ms', [string]$SleepMs) }
    if ($StdoutFile) { $parts += @('-stdout-file', $StdoutFile) }
    return ($parts -join ' ')
}

function Initialize-Scenario {
    param(
        [string]$Tag,
        [string[]]$Answers,
        [string]$CriticVerdict = 'auto',
        [string]$CriticMarker = 'MATERIAL_MARKER_ABSENT',
        [switch]$NoTrust
    )
    $answersPath = Join-Path $script:logsDir ($Tag + '-answers.json')
    $answersJson = @{ answers = $Answers } | ConvertTo-Json -Depth 5
    Write-Utf8NoBom $answersPath $answersJson
    $providerLog = Join-Path $script:logsDir ($Tag + '-provider.jsonl')
    $criticLog = Join-Path $script:logsDir ($Tag + '-critic.jsonl')
    Remove-Item $providerLog, $criticLog -Force -ErrorAction SilentlyContinue
    $provider = Start-MockServer -Exe $script:mockProviderExe -Arguments @('-answers', $answersPath, '-log', $providerLog) -Label ($Tag + '-provider')
    try {
        $critic = Start-MockServer -Exe $script:mockCriticExe -Arguments @('-verdict', $CriticVerdict, '-marker', $CriticMarker, '-log', $criticLog) -Label ($Tag + '-critic')
    }
    catch {
        if ($provider -and -not $provider.Process.HasExited) {
            Stop-Process -Id $provider.Process.Id -Force -ErrorAction SilentlyContinue
        }
        throw
    }
    Write-CodexHomeConfig -ProviderPort $provider.Port -TrustProject:(-not $NoTrust)
    $env:CODEX_HOME = $script:codexHome
    $env:MOCK_API_KEY = 'crisp-e2e-key'
    $env:NO_COLOR = '1'
    $env:QUALITY_GATE_BASE_URL = 'http://127.0.0.1:' + $critic.Port + '/v1'
    $env:QUALITY_GATE_MODEL = 'mock-critic'
    $env:QUALITY_GATE_API_KEY = 'crisp-e2e-key'
    return [pscustomobject]@{
        Tag         = $Tag
        Provider    = $provider
        Critic      = $critic
        ProviderLog = $providerLog
        CriticLog   = $criticLog
    }
}

function Invoke-Codex {
    param(
        [string]$Label,
        [switch]$BypassHookTrust = $true
    )
    $promptPath = Join-Path $script:logsDir 'prompt.txt'
    Write-Utf8NoBom $promptPath 'Reply with the canned answer only.'
    $stdoutPath = Join-Path $script:logsDir ($Label + '.out')
    $stderrPath = Join-Path $script:logsDir ($Label + '.err')
    Remove-Item $stdoutPath, $stderrPath -Force -ErrorAction SilentlyContinue
    $codexArgs = @('exec', '--skip-git-repo-check', '-C', $script:scratch, '-s', 'read-only')
    if ($BypassHookTrust) { $codexArgs += '--dangerously-bypass-hook-trust' }
    $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
    $process = Start-Process -FilePath $CodexPath -ArgumentList $codexArgs -PassThru -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath -RedirectStandardInput $promptPath -NoNewWindow
    $finished = $process.WaitForExit($ScenarioTimeoutSec * 1000)
    $stopwatch.Stop()
    if (-not $finished) {
        & taskkill /PID $process.Id /T /F 2>&1 | Out-Null
    }
    $stdout = ''
    $stderr = ''
    if (Test-Path $stdoutPath) { $stdout = [System.IO.File]::ReadAllText($stdoutPath) }
    if (Test-Path $stderrPath) { $stderr = [System.IO.File]::ReadAllText($stderrPath) }
    return [pscustomobject]@{
        Finished  = $finished
        ExitCode  = $process.ExitCode
        ElapsedMs = $stopwatch.ElapsedMilliseconds
        Stdout    = $stdout
        Stderr    = $stderr
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

function Find-CodexCli {
    $onPath = Get-Command codex -ErrorAction SilentlyContinue
    if ($onPath -and $onPath.Source -and (Test-Path $onPath.Source)) { return $onPath.Source }
    $roots = @(
        (Join-Path $env:LOCALAPPDATA 'OpenAI\Codex\bin'),
        (Join-Path $env:USERPROFILE 'AppData\Local\OpenAI\Codex\bin')
    ) | Select-Object -Unique
    foreach ($root in $roots) {
        if (-not (Test-Path $root)) { continue }
        $candidate = Get-ChildItem -Path $root -Recurse -Depth 2 -Filter 'codex.exe' -File -ErrorAction SilentlyContinue |
            Sort-Object LastWriteTime -Descending |
            Select-Object -First 1
        if ($candidate) { return $candidate.FullName }
    }
    return ''
}

function Get-RecordsOrEmpty([string]$Path) {
    return @(Read-JsonlFile $Path)
}

# ---------------------------------------------------------------------------
# Setup
# ---------------------------------------------------------------------------
Write-Section 'Environment'
if (-not $CodexPath) { $CodexPath = Find-CodexCli }
if (-not $CodexPath -or -not (Test-Path $CodexPath)) {
    $searched = @(
        'PATH (codex)',
        (Join-Path $env:LOCALAPPDATA 'OpenAI\Codex\bin\*\codex.exe')
    )
    Write-Host 'SKIPPED: Codex CLI not found.'
    Write-Host ('searched: ' + ($searched -join '; '))
    Write-Host 'Pass -CodexPath <path-to-codex.exe> to run the real CLI harness.'
    exit 0
}
$codexVersionOutput = & $CodexPath --version 2>&1
if ($LASTEXITCODE -ne 0) { throw ('codex --version failed: ' + ($codexVersionOutput | Out-String)) }
$script:codexVersion = ($codexVersionOutput | Out-String).Trim()
$script:goVersion = (& go version | Out-String).Trim()
Write-Host ('codex: ' + $script:codexVersion)
Write-Host ('go:    ' + $script:goVersion)
Write-Host ('run:   ' + $script:runDir)

New-Item -ItemType Directory -Force -Path $script:binDir, $script:logsDir, $script:codexHome, (Join-Path $script:scratch '.codex') | Out-Null
Write-Utf8NoBom (Join-Path $script:scratch '.projectroot') 'crisp e2e scratch project root marker'

if (-not $SkipBuild) {
    Write-Section 'Build'
    & pwsh -NoProfile -File (Join-Path $repoRoot 'scripts\build.ps1') | Out-Host
    if ($LASTEXITCODE -ne 0) { throw 'scripts/build.ps1 failed' }
    & go build -o $script:tapExe ./test/e2e/hooktap
    if ($LASTEXITCODE -ne 0) { throw 'failed to build hooktap' }
    & go build -o $script:mockProviderExe ./test/e2e/mockprovider
    if ($LASTEXITCODE -ne 0) { throw 'failed to build mockprovider' }
    & go build -o $script:mockCriticExe ./test/e2e/mockcritic
    if ($LASTEXITCODE -ne 0) { throw 'failed to build mockcritic' }
}
if (-not (Test-Path $script:crispExe)) { throw ('missing ' + $script:crispExe + '; run without -SkipBuild or run scripts/build.ps1') }
if (-not (Test-Path $script:tapExe)) { throw 'missing hooktap.exe; run without -SkipBuild' }

try {
    # -----------------------------------------------------------------
    # S0: deterministic protocol replay (crisp side of the contract)
    # -----------------------------------------------------------------
    if (-not $SkipReplay) {
        Write-Section 'S0 protocol replay (hook JSON -> crisp decisions)'
        Push-Location $repoRoot
        try {
            $replayOutput = & go run ./test/e2e/hookreplay -crisp bin\crisp.exe 2>&1
            $replayExit = $LASTEXITCODE
        }
        finally { Pop-Location }
        $replayText = ($replayOutput | Out-String)
        Write-Host $replayText
        if ($replayExit -eq 0) {
            Add-Result 'S0 protocol replay' $true ($replayText.Trim().Split([char]10)[-1])
        }
        else {
            Add-Result 'S0 protocol replay' $false 'replay harness reported failures'
        }
    }

    # -----------------------------------------------------------------
    # S1: pass case
    # -----------------------------------------------------------------
    Invoke-Scenario 'S1 pass-clean' {
        $scenario = Initialize-Scenario -Tag 's1' -Answers @($script:clean)
        try {
            $capture = Join-Path $script:logsDir 's1-captures.jsonl'
            Remove-Item $capture -Force -ErrorAction SilentlyContinue
            Write-HooksConfig (New-TapHookCommand -CapturePath $capture) 30 | Out-Null
            $run = Invoke-Codex -Label 's1'
            Assert-True $run.Finished 'codex exec did not finish'
            Assert-Equal $run.ExitCode 0 'codex exit code'
            Assert-Contains $run.Stderr 'hook: Stop Completed' 'codex hook outcome'
            $records = Get-RecordsOrEmpty $capture
            $ends = @($records | Where-Object { $_.event -eq 'end' })
            Assert-Equal $ends.Count 1 'Stop hook invocations'
            $end = $ends[0]
            Assert-Equal $end.stop_hook_active $false 'first stop_hook_active'
            Assert-Equal $end.hook_event_name 'Stop' 'hook_event_name'
            Assert-True (-not [string]::IsNullOrEmpty($end.transcript_path)) 'transcript_path should be present without --ephemeral'
            Assert-Equal $end.decision 'pass' 'crisp decision'
            Assert-Equal ($end.crisp_stdout.Trim()) '{}' 'crisp stdout'
            Assert-Equal ((@($end.top_level_keys)) -join ',') $script:expectedHookKeys 'hook input field set'
            Assert-True ($end.last_assistant_message_runes -gt 60) 'last_assistant_message length'
            $quality = Get-QualityEvent $end.crisp_stderr
            Assert-True ($null -ne $quality) 'quality_check stderr event'
            Assert-Equal $quality.decision 'pass' 'quality_check decision'
            Assert-Equal $quality.critic_called $true 'critic_called'
            Assert-Equal $quality.fail_open $false 'fail_open'
            Assert-Equal (Get-RecordsOrEmpty $scenario.ProviderLog).Count 1 'model requests'
            Assert-Equal (Get-RecordsOrEmpty $scenario.CriticLog).Count 1 'critic requests'
            Assert-Contains $run.Stdout '输入校验分两步' 'delivered answer'
            Add-Result 'S1 pass-clean' $true ('hook fired once; decision=pass; critic_called=true; codex_elapsed_ms=' + $run.ElapsedMs + '; hook_elapsed_ms=' + $end.crisp_elapsed_ms)
        }
        finally { Stop-Scenario $scenario }
    }

    # -----------------------------------------------------------------
    # S2: block -> rewrite -> second Stop (loop guard)
    # -----------------------------------------------------------------
    Invoke-Scenario 'S2 block-rewrite-loopguard' {
        $scenario = Initialize-Scenario -Tag 's2' -Answers @($script:flawed, $script:improved) -CriticMarker $script:flawMarker
        try {
            $capture = Join-Path $script:logsDir 's2-captures.jsonl'
            Remove-Item $capture -Force -ErrorAction SilentlyContinue
            Write-HooksConfig (New-TapHookCommand -CapturePath $capture) 30 | Out-Null
            $run = Invoke-Codex -Label 's2'
            Assert-True $run.Finished 'codex exec did not finish'
            Assert-Equal $run.ExitCode 0 'codex exit code'
            Assert-Contains $run.Stderr 'hook: Stop Blocked' 'codex block outcome'
            Assert-Contains $run.Stderr 'hook: Stop Completed' 'codex pass outcome after rewrite'
            $records = Get-RecordsOrEmpty $capture
            $events = @($records | ForEach-Object { $_.event })
            Assert-Equal ($events -join ',') 'start,end,start,end' 'hook invocation sequence'
            $ends = @($records | Where-Object { $_.event -eq 'end' })
            $first = $ends[0]
            $second = $ends[1]
            Assert-Equal $first.stop_hook_active $false 'first stop_hook_active'
            Assert-Equal $second.stop_hook_active $true 'second stop_hook_active'
            Assert-Equal $first.decision 'block' 'first crisp decision'
            Assert-Equal $second.decision 'pass' 'second crisp decision'
            Assert-Equal ($second.crisp_stdout.Trim()) '{}' 'second crisp stdout'
            Assert-True ($first.crisp_stdout.Contains('"decision":"block"')) 'first stdout is a block decision'
            Assert-True ($first.crisp_stdout.Contains('请重新输出完整最终回答')) 'block reason is the fixed rewrite instruction'
            $firstQuality = Get-QualityEvent $first.crisp_stderr
            Assert-True ($null -ne $firstQuality) 'first quality_check event'
            Assert-Equal $firstQuality.decision 'block' 'first quality_check decision'
            Assert-Equal $firstQuality.reason_type 'semantic_duplicate' 'first quality_check reason_type'
            $skip = Get-EventByName $second.crisp_stderr 'quality_skip'
            Assert-True ($null -ne $skip) 'second Stop must log quality_skip'
            Assert-Equal $skip.skip_reason 'stop_hook_active' 'second Stop skip reason'
            Assert-Equal (Get-RecordsOrEmpty $scenario.ProviderLog).Count 2 'model requests (initial + exactly one rewrite)'
            Assert-Equal (Get-RecordsOrEmpty $scenario.CriticLog).Count 1 'critic calls (second Stop must short-circuit)'
            Assert-Contains $run.Stdout '输入校验分两步' 'rewritten answer delivered'
            Assert-NotContains $run.Stdout $script:flawMarker 'flawed answer must be replaced'
            Add-Result 'S2 block-rewrite-loopguard' $true ('1 rewrite; stop_hook_active false->true; model_requests=2; critic_requests=1; codex_elapsed_ms=' + $run.ElapsedMs)
        }
        finally { Stop-Scenario $scenario }
    }

    # -----------------------------------------------------------------
    # S3: hook trust gate
    # -----------------------------------------------------------------
    Invoke-Scenario 'S3 hook-trust-gate' {
        $scenario = Initialize-Scenario -Tag 's3' -Answers @($script:clean)
        try {
            $capture = Join-Path $script:logsDir 's3-captures.jsonl'
            Remove-Item $capture -Force -ErrorAction SilentlyContinue
            Write-HooksConfig (New-TapHookCommand -CapturePath $capture) 30 | Out-Null
            $run = Invoke-Codex -Label 's3' -BypassHookTrust:$false
            Assert-True $run.Finished 'codex exec did not finish'
            Assert-Equal $run.ExitCode 0 'codex exit code'
            Assert-NotContains $run.Stderr 'hook: Stop' 'untrusted hooks must not run'
            $ends = @(Get-RecordsOrEmpty $capture | Where-Object { $_.event -eq 'end' })
            Assert-Equal $ends.Count 0 'hook invocation count without trust'
            Assert-Equal (Get-RecordsOrEmpty $scenario.ProviderLog).Count 1 'model requests'
            Assert-Contains $run.Stdout '输入校验分两步' 'delivered answer'
            Add-Result 'S3 hook-trust-gate' $true ('hook skipped without --dangerously-bypass-hook-trust; codex_elapsed_ms=' + $run.ElapsedMs)
        }
        finally { Stop-Scenario $scenario }
    }

    # -----------------------------------------------------------------
    # S4a: timeout=2 vs 4s hook; late block decision must be ignored
    # -----------------------------------------------------------------
    Invoke-Scenario 'S4a timeout-ignores-late-block' {
        $scenario = Initialize-Scenario -Tag 's4a' -Answers @($script:clean)
        try {
            $capture = Join-Path $script:logsDir 's4a-captures.jsonl'
            $blockFile = Join-Path $script:logsDir 's4a-block-once.json'
            Remove-Item $capture, $blockFile -Force -ErrorAction SilentlyContinue
            Write-Utf8NoBom $blockFile $script:blockJson
            Write-HooksConfig (New-TapHookCommand -CapturePath $capture -SleepMs 4000 -StdoutFile $blockFile) 2 | Out-Null
            $run = Invoke-Codex -Label 's4a'
            Assert-True $run.Finished 'codex exec did not finish'
            Assert-Equal $run.ExitCode 0 'codex exit code'
            Assert-Contains $run.Stderr 'hook: Stop Failed' 'codex timeout outcome'
            Assert-Equal (Get-RecordsOrEmpty $scenario.ProviderLog).Count 1 'late block must not trigger a rewrite'
            Assert-Contains $run.Stdout '输入校验分两步' 'original answer delivered'
            $records = Get-RecordsOrEmpty $capture
            $ends = @($records | Where-Object { $_.event -eq 'end' })
            Assert-Equal $ends.Count 1 'timed-out hook still completed as a process'
            $end = $ends[0]
            $starts = @($records | Where-Object { $_.event -eq 'start' })
            Assert-True ($starts.Count -eq 1) 'start record present'
            $startTs = [DateTimeOffset]::Parse($starts[0].ts)
            $endTs = [DateTimeOffset]::Parse($end.ts)
            $hookLifetimeMs = [int]($endTs - $startTs).TotalMilliseconds
            Assert-True ($hookLifetimeMs -ge 3500) 'hook process should have run its full 4s sleep (not terminated at timeout)'
            Add-Result 'S4a timeout-ignores-late-block' $true ('timeout_sec=2 sleep_ms=4000 codex_elapsed_ms=' + $run.ElapsedMs + ' hook_process_lifetime_ms=' + $hookLifetimeMs + ' codex_outcome=Stop Failed late_block_ignored=true')
        }
        finally { Stop-Scenario $scenario }
    }

    # -----------------------------------------------------------------
    # S4b: the same late block within timeout=30 IS used (control)
    # -----------------------------------------------------------------
    Invoke-Scenario 'S4b within-timeout-block-used' {
        $scenario = Initialize-Scenario -Tag 's4b' -Answers @($script:clean, $script:improved)
        try {
            $capture = Join-Path $script:logsDir 's4b-captures.jsonl'
            $blockFile = Join-Path $script:logsDir 's4b-block-once.json'
            Remove-Item $capture, $blockFile -Force -ErrorAction SilentlyContinue
            Write-Utf8NoBom $blockFile $script:blockJson
            Write-HooksConfig (New-TapHookCommand -CapturePath $capture -SleepMs 1500 -StdoutFile $blockFile) 30 | Out-Null
            $run = Invoke-Codex -Label 's4b'
            Assert-True $run.Finished 'codex exec did not finish'
            Assert-Equal $run.ExitCode 0 'codex exit code'
            Assert-Contains $run.Stderr 'hook: Stop Completed' 'codex outcome within timeout'
            Assert-Equal (Get-RecordsOrEmpty $scenario.ProviderLog).Count 2 'block within timeout must trigger one rewrite'
            Assert-Contains $run.Stdout '输入校验分两步' 'rewritten answer delivered'
            $ends = @(Get-RecordsOrEmpty $capture | Where-Object { $_.event -eq 'end' })
            Assert-Equal $ends.Count 2 'two Stop invocations (block then pass)'
            Add-Result 'S4b within-timeout-block-used' $true ('timeout_sec=30 sleep_ms=1500 codex_elapsed_ms=' + $run.ElapsedMs + ' model_requests=2 codex_outcome=Stop Completed')
        }
        finally { Stop-Scenario $scenario }
    }

    # -----------------------------------------------------------------
    # S5: the checked-in project hook configuration works for real
    # -----------------------------------------------------------------
    Invoke-Scenario 'S5 installed-hooks-config' {
        $scenario = Initialize-Scenario -Tag 's5' -Answers @($script:clean)
        try {
            $installedPath = Join-Path $repoRoot '.codex\hooks.json'
            if (-not (Test-Path $installedPath)) { throw ('missing checked-in ' + $installedPath) }
            $installed = [System.IO.File]::ReadAllText($installedPath)
            Write-Utf8NoBom (Join-Path $script:scratch '.codex\hooks.json') $installed
            $run = Invoke-Codex -Label 's5'
            Assert-True $run.Finished 'codex exec did not finish'
            Assert-Equal $run.ExitCode 0 'codex exit code'
            Assert-Contains $run.Stderr 'hook: Stop Completed' 'installed config hook outcome'
            Assert-Equal (Get-RecordsOrEmpty $scenario.ProviderLog).Count 1 'model requests'
            Assert-Contains $run.Stdout '输入校验分两步' 'delivered answer'
            Add-Result 'S5 installed-hooks-config' $true ('checked-in .codex/hooks.json (bin/crisp.exe, timeout=30) invoked by real codex; codex_elapsed_ms=' + $run.ElapsedMs)
        }
        finally { Stop-Scenario $scenario }
    }

    # -----------------------------------------------------------------
    # S6: project trust gate (project hooks require a trusted project)
    # -----------------------------------------------------------------
    Invoke-Scenario 'S6 project-trust-gate' {
        $scenario = Initialize-Scenario -Tag 's6' -Answers @($script:clean) -NoTrust
        try {
            $capture = Join-Path $script:logsDir 's6-captures.jsonl'
            Remove-Item $capture -Force -ErrorAction SilentlyContinue
            Write-HooksConfig (New-TapHookCommand -CapturePath $capture) 30 | Out-Null
            $run = Invoke-Codex -Label 's6'
            Assert-True $run.Finished 'codex exec did not finish'
            Assert-Equal $run.ExitCode 0 'codex exit code'
            Assert-NotContains $run.Stderr 'hook: Stop' 'untrusted project hooks must not load'
            $ends = @(Get-RecordsOrEmpty $capture | Where-Object { $_.event -eq 'end' })
            Assert-Equal $ends.Count 0 'hook invocation count for untrusted project'
            Assert-Equal (Get-RecordsOrEmpty $scenario.ProviderLog).Count 1 'model requests'
            Add-Result 'S6 project-trust-gate' $true 'project hooks skipped without [projects.<path>] trust_level = "trusted"'
        }
        finally { Stop-Scenario $scenario }
    }
}
finally {
    # Best-effort cleanup: stop every server we started and any orphaned tap.
    foreach ($process in $script:servers) {
        if ($process -and -not $process.HasExited) {
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
            try { Wait-Process -Id $process.Id -Timeout 5 -ErrorAction SilentlyContinue } catch { }
        }
    }
    Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.ProcessName -eq 'hooktap' } | Stop-Process -Force -ErrorAction SilentlyContinue
    foreach ($name in @('CODEX_HOME', 'MOCK_API_KEY', 'QUALITY_GATE_BASE_URL', 'QUALITY_GATE_MODEL', 'QUALITY_GATE_API_KEY')) {
        Remove-Item ('Env:' + $name) -ErrorAction SilentlyContinue
    }
}

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
Write-Section 'Summary'
$failed = @($script:results | Where-Object { -not $_.Passed })
Write-Host ('codex: ' + $script:codexVersion)
Write-Host ('go:    ' + $script:goVersion)
$summary = [pscustomobject]@{
    codexVersion = $script:codexVersion
    goVersion    = $script:goVersion
    workDir      = $script:runDir
    results      = $script:results
}
$summaryJson = $summary | ConvertTo-Json -Depth 6
Write-Utf8NoBom (Join-Path $script:runDir 'summary.json') $summaryJson
Write-Host ('artifacts: ' + $script:runDir)
if ($failed.Count -gt 0) {
    Write-Host ('FAILED: ' + $failed.Count + ' of ' + $script:results.Count + ' checks') -ForegroundColor Red
    exit 1
}
Write-Host ('ALL ' + $script:results.Count + ' CHECKS PASSED') -ForegroundColor Green
exit 0