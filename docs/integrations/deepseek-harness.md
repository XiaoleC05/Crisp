# Crisp × DeepSeek Harness (Cordis host plugin)

Status: **installable Host-only bundle, verified against the installed DeepSeek
Harness 0.2.1-rc.5 (Windows, Node.js v22.19.0) in an isolated scratch profile.**
The bundle lives at [integrations/dsh-plugin-crisp/](../../integrations/dsh-plugin-crisp/)
and is registered by its own `cordis.patch.yml`.

This page records exactly what was executed end to end, what was verified only
by unit tests, and what remains unverified. The guard flow is reproducible from
the repository: [scripts/e2e-dsh.ps1](../../scripts/e2e-dsh.ps1) runs the real
`dsh` launcher headless against committed local protocol mocks
([test/e2e/mockmessages](../../test/e2e/mockmessages/main.go) and
[test/e2e/mockcritic](../../test/e2e/mockcritic/main.go)). Nothing here was
measured against a real model provider or a real Critic service.

---

## 1. What the gate does

Crisp (see [README.md](../../README.md)) checks a finished assistant answer with a
Go binary: deterministic rules first, an OpenAI-compatible LLM Critic second,
then a PASS / BLOCK / FAIL-OPEN decision. This integration mounts that binary as
a DeepSeek Harness Host plugin.

- **observe** (default): after every delivered assistant answer, run
  `crisp check-text` on the complete visible text, write one structured JSON
  decision line, and never influence the conversation.
- **guard** (opt-in): on a material BLOCK decision, queue exactly one
  user-role followup asking for the fixed full rewrite from
  [internal/policy/decision.go](../../internal/policy/decision.go). The rewrite is
  a new turn; a rewrite turn is never gated again, so a bad rewrite cannot
  loop.

Every failure (spawn error, timeout, non-zero exit, bad JSON) is a fail-open
pass, mirroring the Go core.

---

## 2. Architecture

```text
dsh agent loop
  └─ ctx.llm.stream(options)                     @deepseek-ai/dsh-llm
       └─ ctx.waterfall(this, "llm/stream", options, () => adapterStream())
            ├─ this bundle (listener; mounted last, so it wraps the adapter)
            │    async generator:
            │      for await (chunk of downstream) { accumulate; yield chunk }  <- object identity unchanged
            │      after terminal finish: spawn "crisp check-text" (bounded)
            │      log one JSON line; in guard mode queue agent.followup()
            └─ adapter stream (DeepSeek / pi-ai / retry wrappers)
```

Cordis waterfall semantics (verified from `@deepseek-ai/cordis` 4.0.5): a
listener receives the event payload and a `next` thunk and must return the
downstream value (`return next()`) unless it replaces it. For `llm/stream`
that value is the provider chunk stream, so the listener returns a wrapper
async generator. Chunks are yielded unchanged (same object references), so the
text streaming path is untouched.

Why this shape:

- **Token streaming is preserved.** First-token latency does not change; the
  gate never buffers before the first chunk.
- **The answer is only complete at the terminal `finish` chunk.** Crisp's
  `check-text` contract takes the whole answer on stdin and prints one decision
  object, so the check runs after the last chunk has been yielded to the loop
  but before the wrapper's generator returns `done`. This delays the durable
  `assistant/message` commit (and therefore tool-call start / turn end) by the
  bounded check latency; it does not delay streaming to the user. A hard
  pre-delivery block is impossible without buffering the entire answer and
  retracting it, which would sacrifice first-token latency (and cannot
  un-deliver chunks already rendered).
- **Turn identity comes from the durable session log.** The listener subscribes
  to `session/event` and folds only `turn/start` / `turn/end`, so logs carry
  the durable turn id and the guard has a per-turn dedupe key.
- **Guard uses `agent.followup()`.** The followup is queued at the terminal
  chunk, while the agent is still inside the step; the loop keeps running when
  its inbox has pending input, so the rewrite becomes the next turn. The
  obvious alternative — queueing from a `turn/end` observer — was implemented
  first and measured to lose a race with the one-shot headless runner
  (the followup was logged but the process exited after turn 1). `turn/end`
  is still read, but only to clear turn/suppression state.
- **Auxiliary model calls are ignored.** Agent-loop requests carry a
  `sessionId` but no `purpose`; session-title and compaction calls set
  `purpose` (`session-title`, `compaction`). The bundle pre-filters on that
  field, then (best effort) on `@deepseek-ai/dsh-llm`'s `isAgentLoopRequest`
  marker, which is loaded lazily so the bundle declares no dependency on the
  installation.

### Host plugin shape

`index.js` exports `apply(ctx, config)` (the Host-plugin export form) and a
`name` string; it deliberately exports **no `inject` list and no schemastery
`Config`**:

- No service is required at activation. The plugin only registers listeners;
  guard mode resolves `ctx.get("agents")` lazily when it actually needs
  `agent.followup()`, so the bundle stays active in profiles without an agent
  service and cannot fail startup for a missing dependency.
- The config is a plain defensive object (`lib/config.js`), which keeps the
  bundle independent of the Harness schemastery version. A bad value falls back
  to a safe default and is reported in the activation record's `warnings`.

### Files

| File | Role |
|---|---|
| `integrations/dsh-plugin-crisp/package.json` | private ESM bundle manifest, `dsh.bundle.patch`, exports for index/locale/icon |
| `integrations/dsh-plugin-crisp/cordis.patch.yml` | inserts the single `crisp-quality-gate` row and its example config |
| `integrations/dsh-plugin-crisp/index.js` | Host plugin: `apply(ctx, config)` registers `llm/stream`, `session/event`, `session/disposed` |
| `integrations/dsh-plugin-crisp/lib/gate.js` | chunk accumulator, check orchestration, guard followup, structured records |
| `integrations/dsh-plugin-crisp/lib/crisp.js` | bounded `crisp check-text` child process, decision parsing, fail-open |
| `integrations/dsh-plugin-crisp/lib/config.js` | defensive config, child-env allowlist, default binary resolution |
| `integrations/dsh-plugin-crisp/lib/log.js` | one JSON line per event to `logFile` or stderr |
| `integrations/dsh-plugin-crisp/locale/{en,zh}.json`, `icon.svg` | Plugin Manager display metadata and original icon |
| `integrations/dsh-plugin-crisp/test/` | 57 hermetic `node:test` tests (fake ctx, fake runner, fake child) |

The end-to-end reproduction harness is committed too:
`scripts/e2e-dsh.ps1` (PowerShell 7) drives the real launcher and asserts the
guard flow, with a stdlib-only Messages-API SSE mock at
`test/e2e/mockmessages/main.go` and the shared OpenAI-compatible Critic mock at
`test/e2e/mockcritic/main.go`.

---

## 3. Prerequisites

1. **Crisp binary**

   ```powershell
   go build -o bin/crisp.exe ./cmd/crisp
   ```

   The plugin resolves a binary in this order: `binaryPath` config →
   `config.env.CRISP_BIN` → `CRISP_BIN` environment variable → the checkout's
   `bin/crisp.exe` (when the package still runs from the repo) → `crisp` on
   `PATH`.

2. **Critic configuration** (otherwise the gate only runs deterministic rules
   and fails open on any semantic doubt):

   ```powershell
   $env:QUALITY_GATE_BASE_URL = "https://api.example.com/v1"
   $env:QUALITY_GATE_API_KEY  = "sk-..."      # optional for local services
   $env:QUALITY_GATE_MODEL    = "critic-model"
   $env:QUALITY_GATE_MODE     = "balanced"
   ```

   These variables must exist in the **Harness process environment** or be set
   through the plugin's `env` config (see the env caveat in section 7). The
   spawned child sees only an allowlisted environment, not the whole Harness
   environment, so provider keys are not leaked to the quality gate.

3. **DSH 0.2.1-rc.5** (the version this bundle was verified against). Node.js
   >= 20 is required by the manifest.

---

## 4. Install

### 4.1 Recommended: Plugin Manager `install_bundle`

The shipped Cordis plugin-development docs specify this path: author the bundle
in the workspace, then call `plugin_manager` with
`action: "install_bundle"` and the **absolute package directory** as `target`
(it runs pnpm, saves the profile dependency, and enables the bundle). This path
was **not executed here**: the verification environment had no
`plugin_manager` tool and was not allowed to change the user's real profile.
It is documented from the package READMEs rather than from a run.

Target for this checkout:

```text
D:\05_Code\Crisp\integrations\dsh-plugin-crisp
```

Do not hand-edit the profile's `package.json`/`cordis.patch.yml` or run pnpm in
a real profile directory; `install_bundle` owns those steps.

### 4.2 CLI / package-manager path (executed in a scratch profile)

The launcher's own plugin management is `dsh plugin --profile <name> <pnpm args>`.
It initializes a profile, runs pnpm in it, saves the dependency, and appends a
new bundle dependency to `dsh.profile.bundles`. This was the path used for the
verification runs, against a throwaway `DSH_HOME` under `.tmp`:

```powershell
$env:DSH_HOME = 'D:\05_Code\Crisp\.tmp\dsh-home'   # scratch state only
$dsh = 'C:\Users\71408\AppData\Local\Programs\DeepSeek Harness\resources\runtime\cli\bin\dsh.cmd'

# 1. create a scratch profile from the shipped headless template
& $dsh --profile crisp-scratch --from-default-profile headless --dump-config

# 2. install this checkout as a linked package into that profile
& $dsh plugin --profile crisp-scratch add 'D:\05_Code\Crisp\integrations\dsh-plugin-crisp'

# 3. confirm the bundle layer and its row compose
& $dsh --profile crisp-scratch --dump-config | Select-String 'crisp-quality-gate' -Context 0,15
```

Observed after step 2/3 (scratch profile):

```json
"dependencies": { "@local/crisp-quality-gate": "link:D:/05_Code/Crisp/integrations/dsh-plugin-crisp" },
"dsh": { "profile": { "bundles": [
  "@deepseek-ai/dsh-base", "@deepseek-ai/dsh-headless", "@local/crisp-quality-gate"
] } }
```

```yaml
# == @local/crisp-quality-gate
- id: crisp-quality-gate
  name: '@local/crisp-quality-gate'
  config:
    mode: observe
    timeoutMs: 30000
    ...
```

Starting the profile mounts the row; a scratch boot (`--patch` overlay pinned a
log file) wrote the activation line:

```json
{"event":"crisp_quality_gate_active","mode":"observe","binary":"D:\\05_Code\\Crisp\\bin\\crisp.exe",
 "binary_exists":true,"timeout_ms":20000,"min_runes":1,...}
```

For a real profile, prefer `install_bundle`; a restart (or HMR where enabled)
is required for the new row to mount in a long-lived process.

---

## 5. Configuration

The row accepts a plain object (no schemastery `Config` export), so every value
is defaulted defensively at activation:

| Field | Default | Meaning |
|---|---|---|
| `enabled` | `true` | Master switch; `CRISP_DSH_DISABLE=1` also disables it. |
| `mode` | `observe` | `observe` = check + log only. `guard` = also queue one rewrite followup on a material block. |
| `binaryPath` | checkout `bin/crisp.exe`, then `CRISP_BIN`, then `crisp` | Crisp executable. |
| `timeoutMs` | `30000` | Hard bound for one check (clamped 1000–120000). Keep it above the binary's own `QUALITY_GATE_TOTAL_TIMEOUT_MS` budget (default 25 s). |
| `minRunes` | `40` | Visible answers shorter than this many code points are not checked (the Go core's own 120-rune Critic threshold still applies inside the binary). |
| `maxRunes` | `200000` | Longer answers are skipped instead of piped to the child. |
| `logFile` | `""` | `""` writes JSON lines to stderr; a path appends JSONL (directories are created). `false` disables file logging. |
| `env` | `{}` | Extra child environment; wins over passthrough. Use this to pin `QUALITY_GATE_*` instead of relying on profile `.env` files. |
| `envPassthrough` | `QUALITY_GATE_*`, `CRISP_*` list | Names copied from the Harness process environment; entries ending in `*` match by prefix. |
| `rewriteInstruction` | fixed Chinese instruction | Must stay byte-identical to `policy.RewriteReason`; override only to track that source. |

Example (override in the profile's own `cordis.patch.yml`, or in a `--patch`
overlay): a matching override replaces the whole config, so restate what you keep.

```yaml
- id: crisp-quality-gate
  config:
    mode: guard
    binaryPath: 'D:\05_Code\Crisp\bin\crisp.exe'
    timeoutMs: 30000
    minRunes: 40
    logFile: 'D:\logs\crisp-dsh.jsonl'
    env:
      QUALITY_GATE_BASE_URL: 'https://api.example.com/v1'
      QUALITY_GATE_MODEL: 'critic-model'
      QUALITY_GATE_MODE: 'balanced'
```

### Log records

One JSON object per line, plugin tag `crisp-quality-gate`:

- `crisp_quality_gate_active` — activation summary (mode, binary, bounds, log target).
- `crisp_quality_check` — one per checked answer:
  `decision`, `reason_type`, `rule_types`, `critic_called`, `fail_open`,
  `answer_runes`, `natural_runes`, `critic_latency_ms`, `critic_tokens`,
  `check_latency_ms`, `exit_code`, `timed_out`, `binary`.
- `crisp_quality_skip` — not checked: `short_answer`, `empty_answer`,
  `answer_too_large`, `finish_error` / `finish_max-tokens` / `finish_aborted`,
  `missing_finish`.
- `crisp_guard_followup` — the one followup was queued (turn, reason, sizes).
- `crisp_guard_skipped` — `no_turn`, `rewrite_turn`, `already_rewritten`,
  `agent_unavailable`, `followup_unavailable`.
- `crisp_guard_error` / `crisp_quality_error` — contained failures; never thrown
  into the agent run.

No record contains answer text, user content, or credentials.

### Data processing boundary

The complete visible assistant answer is written to the **local** `crisp`
child's stdin. If a Critic is configured, the Go binary forwards the answer (and
rule evidence) to the configured OpenAI-compatible service — the same boundary
as the Codex integration in the project README. The child does **not** receive
the Harness process environment wholesale; only the allowlisted / passthrough
variables are forwarded.

---

## 6. Verification (executed)

### 6.1 Committed guard E2E harness — PASS

The original manual guard run has been promoted into the repository:

- [scripts/e2e-dsh.ps1](../../scripts/e2e-dsh.ps1) — PowerShell 7 E2E harness.
- [test/e2e/mockmessages/main.go](../../test/e2e/mockmessages/main.go) —
  stdlib-only Anthropic/DeepSeek Messages-API mock with SSE streaming.
- [test/e2e/mockcritic/main.go](../../test/e2e/mockcritic/main.go) — the
  existing OpenAI-compatible Critic mock.

One-line reproduction (from the repository root):

```powershell
pwsh -File scripts/e2e-dsh.ps1
```

What it does:

1. Finds the installed `dsh` launcher (explicit `-DshPath`, then `PATH`, then
   the default DeepSeek Harness install locations). If none is found it prints
   `SKIPPED` with every searched path, writes `summary.json`, and exits 0.
2. Builds `bin/crisp.exe` plus the two mocks, then creates an isolated
   `DSH_HOME` and a scratch profile from the shipped `headless` template under
   `.tmp/e2e/dsh/run-<stamp>/` (the real user `~/.dsh` is never opened; the
   script asserts no scratch profile appears there).
3. Registers the bundle with
   `dsh plugin --profile <scratch> add <repo>/integrations/dsh-plugin-crisp`.
4. Starts the Messages mock (flawed answer first, improved answer for the
   rewrite request) and the Critic mock (block for the flawed answer), writes a
   guard-mode `--patch` overlay, and runs the real `dsh` headless agent with
   `DEEPSEEK_BASE_URL` and `QUALITY_GATE_*` pointed at the local mocks.
5. Asserts the complete guard flow as 11 named checks: scratch profile created;
   bundle registered in the profile manifest; headless run exit 0; exactly two
   turns and final stdout equals the improved answer; first check blocked with
   rule signals and a Critic call; rewrite turn checked and passed; exactly one
   user-role followup carrying the fixed rewrite instruction; auxiliary model
   calls neutralized (no answer-sequence shift); the Critic saw exactly one
   request and returned `block`; no contained gate errors or fail-open
   records; scratch-`DSH_HOME`-only isolation.
6. Writes `summary.json` (dsh/go versions, plugin path, per-check results,
   artifact paths) and keeps gate/mock/stdout logs; processes and environment
   variables are cleaned up in `finally`.

Options: `-DshPath <dsh.cmd>` pins the launcher (it still SKIPs when the path
is missing), `-SkipBuild` reuses the binaries built in
`.tmp/e2e/dsh/bin`, `-ScenarioTimeoutSec <n>` bounds one headless invocation
(default 240), `-WorkRoot <dir>` moves the scratch root.

Exit codes: **0** = all checks passed or SKIPPED, **1** = one or more checks
failed. Artifacts: `.tmp/e2e/dsh/run-<stamp>/summary.json` plus
`logs/gate.jsonl`, `logs/mockmessages.jsonl`, `logs/mockcritic.jsonl`,
`logs/dsh-headless.out` and `logs/dsh-headless.err`.

Measured on Windows with Go 1.26.4 and dsh 0.2.1-rc.5 (2026-11-02):
**ALL 11 CHECKS PASSED** (`elapsedMs: 2839`), reproduced twice — once with a
full build and once with `-SkipBuild` — and the missing-`-DshPath` run exits 0
with `SKIPPED`.

### 6.2 Unit tests — PASS

```powershell
cd integrations/dsh-plugin-crisp
node --test "test/*.test.js"
```

Result: **57/57 pass, 0 fail**. Coverage: config defaults/precedence/clamping;
child-env filtering; decision parsing; spawn success/error/exit/kill; all
fail-open paths; stream pass-through identity; visible-text accumulation
(reasoning excluded, block-end fallback); tool-call and non-stop skips;
minRunes; agent-request detection; guard followup once per turn, rewrite-turn
suppression, no-turn/agent-unavailable/refusing-followup paths;
manifest/patch/locale/icon contract; rewrite instruction equality with
`internal/policy/decision.go`.

### 6.3 Additional one-off measurements during integration development

These were measured manually on the same Windows machine before the committed
harness existed; the automated harness above now covers the successful guard
path (including the activation record), while the failure paths below are still
one-off measurements:

- **Mount/activation inside the real Loader.** A scratch `--help` boot of the
  headless profile mounted the bundle and wrote
  `{"event":"crisp_quality_gate_active","mode":"observe",...,"binary_exists":true}`.
- **`llm/stream` interception on a failing provider.** A headless run pointed at
  a closed local port (`http://127.0.0.1:9`, no network) reached the real
  adapter dispatch path; the gate observed the terminal `finish:error` chunk and
  logged `crisp_quality_skip ... "skip_reason":"finish_error"` with the correct
  session id and turn 1.

### 6.4 Guard flow measured numbers

The committed harness reproduces the flow below; these are the numbers from the
original manual run that motivated it and from the harness run:

1. Turn 1 delivers the flawed answer. Gate record: `decision:"block"`,
   `rule_types:["duplicate_sentence","filler_opening"]`, `critic_called:true`,
   `fail_open:false`, real `crisp.exe` exit 0.
2. Exactly one `crisp_guard_followup` is queued for turn 1 (68 characters of
   rewrite instruction).
3. Turn 2 starts; the Messages mock receives a second conversation model call
   whose body carries the fixed rewrite instruction and the assistant history
   (2 main model calls, 1 Critic call total; the auxiliary session-title call is
   answered neutrally and does not shift the answer sequence).
4. Turn 2 is checked: `decision:"pass"`, `fail_open:false`.
5. Headless final stdout is the improved (rewritten) answer; exit 0.
6. The durable session log contains 2 `turn/start` + 2 `turn/end`, 2
   `assistant/message`, and exactly one `user/message` carrying the rewrite
   instruction with source kind `crisp-quality-gate` (measured by decoding the
   zstd session log during the original manual run; the committed harness
   asserts the same facts through the gate and mock logs rather than decoding
   the session container).

### 6.5 What remains unverified

| Claim | Status |
|---|---|
| `plugin_manager install_bundle` path | **Not executed** (tool unavailable in the verification environment; user profile untouched). Documented from shipped docs. |
| Full rewrite on the user's real profile / long-lived Web/TUI surface | **Not executed.** Guard followup was verified through the headless runner by the committed harness; the session-event and followup APIs used are the same, but no GUI session was driven. |
| Rendering of the plugin's title/description/icon in Plugin Manager | **Not executed** (no browser control / no plugin-manager tool). Locale JSON, exports, and SVG validity are covered by contract tests only. |
| Real DeepSeek provider and real Critic service | **Not used.** The E2E used local protocol mocks; no credentials or network calls were involved. |
| `@deepseek-ai/dsh-llm-replay` headless replay | **Blocked:** the package is not shipped in this installation's `app.asar` (checked by listing; only the source-checkout catalog mentions it). The committed Messages mock is used instead. |
| Other DSH versions, non-Windows platforms, paths with spaces | **Not verified.** The harness derives the plugin path from the repository and needs an installed `dsh`; it was measured only against 0.2.1-rc.5 on Windows. |
| Which detector branch ran in the E2E (lazy marker import vs `purpose`/`sessionId` fallback) | **Not observed.** Both produce the same decision; titles/compaction are filtered by `purpose` regardless. |

---

## 7. Limitations and design notes

1. **No hard pre-delivery block.** Chunks are passed through to preserve token
   streaming, so the answer becomes visible to the user before the check can
   finish. `observe` only logs; `guard` asks for a repair as the next turn.
   A hard block would require buffering the whole answer (losing first-token
   latency), and even then already-rendered chunks cannot be un-delivered.
2. **End-of-turn latency.** The check runs after the terminal chunk and before
   the wrapper reports completion; the durable assistant-message commit waits
   for it (bounded by `timeoutMs`). Rule-only checks are sub-millisecond in the
   Go evaluation; a Critic HTTP call dominates when configured.
3. **Guard semantics.** Exactly one rewrite per turn (deduped by durable turn
   id), never for the rewrite's own turn, with the fixed Chinese instruction.
   If the rewrite still fails, the gate does not loop. If the followup is
   queued and the user sends new input first, the rewrite request shares the
   next turn with that input (the same behavior as any followup).
4. **One-shot headless runs.** A followup queued from a `turn/end` observer lost
   a race with the headless runner's shutdown, which is why the trigger is the
   terminal chunk. The headless E2E showed the rewrite turn runs inside the same
   invocation; long-lived surfaces were not exercised.
5. **Auxiliary calls.** Model calls with a `purpose` (session titles,
   compaction) are never checked. If a future Harness version adds a
   session-scoped auxiliary call without `purpose`, the `minRunes` and
   visible-text checks still filter most of them, but the boundary is
   best-effort rather than a protocol guarantee.
6. **Child environment.** Only platform essentials plus `envPassthrough` names
   and `env` overrides are passed to `crisp`. Values loaded from profile
   `.env` files are **not** necessarily in `process.env`; set
   `QUALITY_GATE_*` in the Harness process environment or in the plugin's
   `env` config.
7. **Fail-open everywhere.** Spawn errors, timeouts, non-zero exits, bad JSON,
   and unknown decisions are all logged as `fail_open: true` passes.
8. **Windows.** The child is spawned with `windowsHide: true`; a timeout calls
   `child.kill()` on the direct child (Crisp itself spawns no children).
9. **Restart.** A profile that is already running does not pick up a newly
   installed bundle until restart/HMR, same as any Cordis plugin change.

---

## 8. Quick checks

```powershell
# full guard E2E against the real dsh launcher (local mocks, no network)
pwsh -File scripts/e2e-dsh.ps1

# unit tests (no binary, no network)
cd D:\05_Code\Crisp\integrations\dsh-plugin-crisp; node --test "test/*.test.js"

# binary contract used by the plugin
'This is a normal complete answer.' | D:\05_Code\Crisp\bin\crisp.exe check-text

# inspect the most recent harness run (summary + gate/mock logs)
$run = Get-ChildItem .tmp\e2e\dsh -Directory -Filter 'run-*' | Sort-Object LastWriteTime -Descending | Select-Object -First 1
Get-Content (Join-Path $run.FullName 'summary.json')
```

---

## 9. Pointers

- Crisp binary build, env variables, decisions: [README.md](../../README.md),
  [docs/design.md](../design.md).
- Codex Stop-hook integration and its real E2E: [docs/integrations/codex-cli.md](codex-cli.md).
- Rule-layer evaluation: [docs/eval-report.md](../eval-report.md).
- Guard E2E harness: [scripts/e2e-dsh.ps1](../../scripts/e2e-dsh.ps1); mock
  Messages API: [test/e2e/mockmessages/main.go](../../test/e2e/mockmessages/main.go).