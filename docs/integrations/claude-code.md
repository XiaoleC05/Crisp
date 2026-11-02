# Claude Code Stop hook integration (Phase 4a)

Status: **verified end-to-end against a real Claude Code 2.1.312 CLI Stop hook**,
using an isolated config directory and a local mock Anthropic Messages API +
mock critic (no real model, no credentials, no network). The E2E is committed
and reproducible, and skips gracefully when the CLI is absent:
`pwsh -File scripts/e2e-claude.ps1` (section 8.2).
Crisp's existing hook contract needed no wire-protocol change; two small
compatibility fixes were made in `internal/hook` (section 7).

- Claude Code CLI: `2.1.312 (Claude Code)`, native binary bundled with the
  `anthropic.claude-code-2.1.312-win32-x64` VS Code extension
  (`...\resources\native-binary\claude.exe`). The `claude` command is **not** on
  PATH on this machine and the npm global `@anthropic-ai/claude-code` package is
  not installed; the extension binary is the only runnable CLI found.
- Crisp: `0.1.0`, Go `go1.26.4 windows/amd64`.
- Hook config used for the E2E: an isolated user-level
  `CLAUDE_CONFIG_DIR/settings.json` whose Stop command is exactly the command
  in this repo's `.claude/settings.json` (direct `crisp.exe`, no wrapper).
- Validation date: 2026-11-02 (Asia/Shanghai).

Everything below is either reproduced against the real CLI or explicitly marked
unverified. The mock answers/critic verdicts are synthetic: this validates the
**protocol and the rewrite loop**, not model quality.

---

## 1. Verified summary

| # | Claim | Evidence |
|---|-------|----------|
| 1 | A real Claude Code CLI runs Crisp as a Stop hook | Stop invocation captured, `crisp_exit_code=0`, `crisp_stdout` present; reproduce with `scripts/e2e-claude.ps1` (section 8.2) |
| 2 | Claude Code's Stop input parses with Crisp's existing `hook.Input` | captured payload; no `turn_id`, extra fields ignored; `last_assistant_message` 193/146 runes |
| 3 | A material verdict returns `{"decision":"block",...}` and Claude Code regenerates the answer | first Stop `crisp_stdout={"decision":"block","reason":...}`; second model request served the rewritten answer; final `result` is the rewrite |
| 4 | The second Stop after the rewrite carries `stop_hook_active=true` | captured second Stop payload: `stop_hook_active=true` |
| 5 | Crisp skips the second Stop without calling the critic (one rewrite per turn) | second Stop stderr: `quality_skip skip_reason=stop_hook_active`, stdout `{}`; exactly **1** critic request, exactly **2** model requests, `num_turns=2` |
| 6 | The exact command shape in the checked-in `.claude/settings.json` works | script scenario S2 runs the checked-in config directly (`& 'D:\05_Code\Crisp\bin\crisp.exe'`, `shell=powershell`) and passes |
| 7 | A full Claude payload is not a protocol anomaly in Crisp | unit tests: `Input.Validate()` returns no warnings for the real field set |
| 8 | Crisp fails open on every critic failure (timeout / HTTP 500 / invalid JSON) with Claude-shaped input | protocol matrix 9/9 (section 8) |
| 9 | The real `~/.claude/settings.json` is untouched by the E2E | SHA256 hash identical before/after every run (isolated `CLAUDE_CONFIG_DIR`) |
| 10 | The installer merges safely and never clobbers unrelated settings | installer checks pass (section 9) |

### Observed Stop input (real payload, field set and sizes only)

Captured by `test/e2e/hooktap` (answer text is hashed, never persisted):

```json
{
  "session_id": "f4b95e3e-...",
  "transcript_path": "D:\\...\\home-loop\\projects\\D--05-Code-Crisp--tmp-e2e-claude-project-loop\\f4b95e3e-....jsonl",
  "cwd": "D:\\05_Code\\Crisp\\.tmp\\e2e\\claude\\project-loop",
  "permission_mode": "bypassPermissions",
  "hook_event_name": "Stop",
  "stop_hook_active": false,
  "last_assistant_message": "<answer, 193 runes>",
  "effort": "high",
  "prompt_id": "prompt-...",
  "background_tasks": [],
  "session_crons": []
}
```

Eleven top-level fields, input payload 1037 bytes (first Stop) / 871 bytes
(second Stop, shorter rewrite). There is **no `turn_id` and no `model`**
(unlike Codex). Only four fields matter to Crisp:
`session_id, transcript_path, stop_hook_active, last_assistant_message`;
the rest are ignored by design.

### Loop observed (block → rewrite → second Stop pass)

| Stop | `stop_hook_active` | answer runes | Crisp stdout | Crisp stderr event |
|------|----------------------|--------------|--------------|--------------------|
| 1 (flawed answer) | `false` | 193 | `{"decision":"block","reason":"请重新输出完整最终回答…"}` | `quality_check decision=block critic_called=true` |
| 2 (rewritten answer) | `true` | 146 | `{}` | `quality_skip skip_reason=stop_hook_active` |

Final CLI result: `is_error=false`, `num_turns=2`, `result` = the rewritten
answer (the flawed answer was replaced). Exactly one critic request; exactly two
model requests (original + one rewrite), i.e. no extra hidden turn.

### Exact E2E 2.1.312 config shape that worked

```json
{
  "hooks": {
    "Stop": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "& 'D:\\05_Code\\Crisp\\bin\\crisp.exe'",
            "shell": "powershell",
            "timeout": 60,
            "statusMessage": "Crisp quality gate E2E"
          }
        ]
      }
    ]
  }
}
```

(The E2E used `timeout: 60`; the shipped config uses `30`, still well above
Crisp's own 25 s total budget. `matcher` is optional for Stop and set to `""`
to mirror the real-world hooks config on this machine.)

---

## 2. Protocol mapping: Codex vs Claude Code

| Aspect | Codex CLI (verified in `docs/codex-e2e.md`) | Claude Code 2.1.312 (verified here) |
|--------|------------------------------------------|-------------------------------------|
| Config file (project) | `<repo>/.codex/hooks.json` | `<repo>/.claude/settings.json` under `hooks` |
| Config file (user) | `$CODEX_HOME/hooks.json` | `$CLAUDE_CONFIG_DIR/settings.json` (default `~/.claude/settings.json`) |
| Structure | `hooks.Stop[]` → `hooks[]` | `hooks.Stop[]` → `matcher` + `hooks[]` |
| Handler fields | `type, command, timeout` (seconds), `statusMessage` | same + `shell` (`bash`/`powershell`), `async`, `args`, `if`, `once` |
| `timeout` unit / meaning | seconds; deadline for the result | seconds; timeout for the command |
| Blocking required | yes (async handlers skipped in that build) | yes for a decision: `async: true` runs in the background and cannot block |
| Input fields | 9 required: `cwd, hook_event_name, last_assistant_message, model, permission_mode, session_id, stop_hook_active, transcript_path, turn_id` | 11 observed: same minus `turn_id`/`model`, plus `effort, prompt_id, background_tasks, session_crons` |
| Pass output | `{}` (or no output) | `{}` |
| Block output | `{"decision":"block","reason":"..."}` (non-empty reason required) | same |
| `hookSpecificOutput` for Stop | not accepted | none; do not emit it |
| Rewrite behavior | block → model regenerates full answer → second Stop | same (verified) |
| One-rewrite guard | `stop_hook_active=true` on the Stop after a block | same (verified) |
| Trust gate | hook trust review + project trust for project layer | user-level hooks run; project/local settings hooks need a trusted workspace / hook review (see section 6) |
| Cross-platform shell | command string is an OS process; PowerShell not pinned in hooks.json | `shell: "powershell"` pins pwsh on Windows (default is bash when Git Bash is present) |

### Input field compatibility (audited, tested in `internal/hook/claude_test.go`)

| Field | Codex | Claude Code | Crisp handling |
|-------|-------|-------------|----------------|
| `last_assistant_message` | string or null | string (observed always present) | required in practice; null/empty → pass (fail open) |
| `stop_hook_active` | required bool | required bool | `nil` (missing) → pass + anomaly warning; `true` → pass (rewrite guard); `false` → review |
| `session_id` | string | string (UUID) | log correlation only |
| `transcript_path` | string or null | string or null | best-effort recent user message (bounded tail read) |
| `turn_id` | string (Codex extension) | **absent** | optional, log correlation only; **no anomaly warning** for absence (section 7) |
| `hook_event_name` | `"Stop"` | `"Stop"` | stored, not validated |
| Unknown fields | ignored | ignored | ignored on purpose (forward compatible) |

Output compatibility: Crisp writes exactly one JSON object, `{}` or
`{"decision":"block","reason":"<non-empty>"}`, to stdout, exits 0 in
every case (including block and all failures), and writes diagnostics only to
stderr. That matches both agents' Stop hooks.

---

## 3. Install

### 3.1 Build

```powershell
pwsh -File scripts/build.ps1          # or: go build -o bin/crisp.exe ./cmd/crisp
```

### 3.2 Project level (recommended for a repo)

`.claude/settings.example.json` is the checked-in template;
`.claude/settings.json` is this repo's live copy (mirrors the
`.codex/hooks.json` approach). Or generate it:

```powershell
pwsh -File scripts/install-claude-hook.ps1          # writes/merges .claude/settings.json
pwsh -File scripts/install-claude-hook.ps1 -Force   # replace an existing Crisp entry
```

The installer:
- resolves the repo root from its own location and defaults to
  `<repo>/bin/crisp.exe` (fails with a build hint if the binary is missing);
- writes `"timeout": 30` seconds, `type: "command"`,
  `shell: "powershell"` on Windows, and the absolute binary path;
- **merges** into an existing settings file: unrelated settings and other hooks
  are preserved, only Crisp's own Stop handler is added/replaced;
- refuses to replace an existing Crisp handler without `-Force`;
- refuses to touch invalid JSON without `-Force`; with `-Force` it makes a
  timestamped `.bak` copy first;
- re-reads and verifies the written file (exactly one Crisp Stop handler).

### 3.3 User level (all projects)

```powershell
pwsh -File scripts/install-claude-hook.ps1 -SettingsPath "$env:USERPROFILE\.claude\settings.json"
```

This edits a **user-owned** file; back it up first and review the diff. The E2E
used exactly this mechanism with an isolated `CLAUDE_CONFIG_DIR`, never the real
user config.

### 3.4 Critic configuration

Crisp reads its critic settings from the environment inherited by the hook
process. In Claude Code, `settings.json` has an `env` object that is applied to
hook processes:

```json
{
  "env": {
    "QUALITY_GATE_BASE_URL": "https://api.example.com/v1",
    "QUALITY_GATE_MODEL": "your-light-model",
    "QUALITY_GATE_MODE": "balanced"
  }
}
```

Keep `QUALITY_GATE_API_KEY` in the shell environment rather than a committed
settings file. Without a configured critic the gate only runs deterministic
rules and fails open (no blocks), so a production install must configure it.

### 3.5 Smoke test

```powershell
'{"session_id":"s","transcript_path":null,"cwd":"D:\\","permission_mode":"default","hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"answer"}' | .\bin\crisp.exe
# expected: {}
```

---

## 4. One rewrite per turn (`stop_hook_active`)

Claude Code does not enforce a one-rewrite limit itself. When a Stop hook returns
`decision: block`, Claude Code continues the same turn with the hook's reason and
sets `stop_hook_active=true` on the next Stop. Crisp's guard order is:

1. invalid input → `{}` (fail open);
2. `stop_hook_active == true` → `{}`, critic is not called (verified);
3. missing `stop_hook_active` → `{}` + anomaly warning (fail open);
4. empty `last_assistant_message` → `{}`;
5. rules → critic → PASS/FAIL-OPEN `{}` or BLOCK JSON.

Measured on the real CLI: 1 critic call + 2 model requests + `num_turns=2`;
the second Stop passed with `{}` before the critic was reached.

---

## 5. Output details

Pass: `{}`

Block:

```json
{"decision":"block","reason":"请重新输出完整最终回答：合并语义重复的段落，删除无效铺垫和结论重复，保留全部必要事实、数字、代码和解释，不要为了缩短篇幅而删除必要信息。"}
```

Claude Code requires a non-empty `reason` for a Stop block; Crisp always sends
the fixed rewrite instruction, so this holds. Crisp never emits
`hookSpecificOutput` for Stop. It also never patches the answer itself: it only
asks for a complete regeneration.

---

## 6. Trust, permissions, and enablement notes

- **User-level hooks run without an extra project trust step** (verified in
  `-p` print mode). **Project/local `.claude/settings.json` hooks belong to a
  trusted workspace and may require hook review/approval**; the interactive TUI
  flow was *not* clicked through here (see unverified list).
- **`disableAllHooks: true`** in any settings layer disables all hooks,
  including Crisp. Check it if the hook never fires.
- `async: true` runs a hook in the background; a Stop decision cannot be
  delivered before the turn ends, so do **not** add it to Crisp's Stop handler.
- Managed/enterprise policy settings and `--safe-mode`/`--bare` can disable
  hooks; `--bare` explicitly skips settings hooks.
- Hook commands execute arbitrary local code. Review `.claude/settings.json`
  (and any user settings) before trusting a workspace, and keep
  `--dangerously-skip-permissions` for isolated automation only (used in the
  E2E scratch project).
- The hook process must stay short-lived and self-bounded: Crisp's own total
  budget is 25 s (`QUALITY_GATE_TOTAL_TIMEOUT_MS`), below the 30 s hook
  `timeout` in the shipped config.

---

## 7. Compatibility fixes made in `internal/hook` (Codex behavior unchanged)

The wire protocol needed no change. The audit found two integration issues that
would have hurt Claude users, both fixed with Codex behavior preserved:

1. **`turn_id` no longer reported as a protocol anomaly.** It is a Codex
   extension; Claude Code never sends it. Previously every single Claude Stop
   logged `protocol_anomaly: missing turn_id`. Codex always sends `turn_id`, so
   its behavior and logs are unchanged. (`Input.TurnID` is still parsed and
   logged when present.)
2. **Claude transcript extraction skips non-prompt "user" records.**
   Claude Code transcripts wrap tool results, sidechain activity, and CLI meta
   messages in top-level `"type":"user"` records. Before the fix, the
   backwards scan could return **tool output** instead of the user's question as
   the "recent user message" forwarded to the critic (wrong context + a local
   tool-output data leak). Such records are now skipped: tool-result-only,
   `isMeta`, `isSidechain`, or `toolUseResult` records contribute no user text.
   Codex transcripts use a different shape (`type: response_item`) and are
   unaffected.

Audited and found **compatible as-is**: full Claude payload, missing optional
fields, `transcript_path: null`, unknown/extra top-level fields, extra nested
objects/arrays, `stop_hook_active` true/false/missing, malformed inputs (empty,
whitespace, truncated, invalid JSON, array, scalar, trailing data), and JSON
`null` (decodes to a zero payload; the command layer fails open on the missing
`stop_hook_active`).

---

## 8. Verification (committed, reproducible)

### 8.1 Unit tests (Crisp side)

```powershell
go test ./internal/hook -count=1 -v
gofmt -l internal/hook
go vet ./internal/hook
```

### 8.2 Real Claude Code CLI E2E - `scripts/e2e-claude.ps1`

One command:

```powershell
pwsh -File scripts/e2e-claude.ps1
```

The script (PowerShell 7, committed) does the following:

- **Discovers the Claude Code CLI**: `-ClaudePath` when given, otherwise
  PATH, then the VS Code / VS Code Insiders / Cursor extension bundles
  (`anthropic.claude-code-*` → `resources/native-binary/claude.exe`, newest
  version first), then common native/npm install locations. If nothing is
  found it prints `SKIPPED` with every path it searched and exits 0.
- **Builds** `bin/crisp.exe` (`-SkipBuild` to reuse) plus the test helpers
  (`test/e2e/mockanthropic`, `test/e2e/mockcritic`, `test/e2e/hooktap`).
- **Isolates everything** in `.tmp/e2e/claude/run-<stamp>/`: an isolated
  `CLAUDE_CONFIG_DIR` plus a scratch project. The real `~/.claude/` is never
  written; its settings SHA256 is compared before and after.
- **Starts the deterministic mock Anthropic Messages API**
  (`test/e2e/mockanthropic`: SSE streaming, canned flawed-then-improved answers,
  metadata-only request log) and the mock critic (`test/e2e/mockcritic`, which
  blocks when the flawed-answer marker is in the request), points
  `ANTHROPIC_BASE_URL` / `QUALITY_GATE_*` at them, and runs the real CLI
  non-interactively (`-p --model sonnet --dangerously-skip-permissions
  --output-format json`) with a **USER-level blocking Stop hook**.
- **Asserts** (both scenarios): the hook fired with a real Claude payload
  (`Stop`, `stop_hook_active` false then true, `session_id`,
  `transcript_path`, `last_assistant_message`, and no Codex-only `turn_id`);
  first Stop block → Claude Code regenerates → second Stop `{}`; exactly
  **2 conversation model requests** and **1 critic request**; the final result
  equals the rewritten answer. S1's wrapper capture additionally proves the hook
  sequence `start,end,start,end` and the block/pass decisions.
- **Writes** `summary.json` (CLI `--version` output, binary path, searched
  paths, per-check results, artifact paths) and stops every started process /
  clears the environment variables in `finally`.

Scenarios:

| # | Scenario | What it proves |
|---|----------|----------------|
| S1 | user-level hook via the `test/e2e/hooktap` wrapper | real Stop payload shape, block → rewrite → loop guard, exact request counts |
| S2 | checked-in `.claude/settings.json` (direct `bin/crisp.exe`, `shell: powershell`, `timeout: 30`) | the shipped project-level config drives the same flow when the repo binary path in it is valid |

Recorded run (2026-11-02): `run-20261102-221324`, Claude Code
`2.1.312 (Claude Code)`, `go1.26.4 windows/amd64`, **3/3 checks passed**
(S1, S2, `isolation real ~/.claude untouched`; S1 and S2 each
`model_requests=2`, `critic_requests=1`). Artifacts (gitignored):
`.tmp/e2e/claude/run-20261102-221324/` including `summary.json`.

Options: `-ClaudePath <path>`, `-SkipBuild`,
`-ScenarioTimeoutSec <n>`, `-WorkRoot <dir>`. Exit codes: 0 when all
checks pass or when the CLI is absent (SKIPPED), 1 on a failed check.

### 8.3 Deterministic protocol matrix (no CLI required)

The Crisp side of the contract is also verified without Claude Code, with
Claude-shaped payloads against `bin/crisp.exe` + `test/e2e/mockcritic`:
9/9 PASS.

| Case | Payload / critic | Expected | Observed |
|------|------------------|----------|----------|
| c1 pass clean answer | `stop_hook_active=false`, critic pass | `{}` | `{}` |
| c2 block material | flawed answer, critic block/material | `{"decision":"block","reason":...}` | block + non-empty reason |
| c3 critic timeout | critic delay 2 s, crisp timeout 300 ms | `{}` + `fail_open=true` | `{}` + `fail_open=true` |
| c4 critic HTTP 500 | 500 | `{}` + fail open | `{}` + fail open |
| c5 critic invalid JSON | non-JSON verdict | `{}` + fail open | `{}` + fail open |
| c6 `stop_hook_active=true` | block critic would block | `{}`, `skip_reason=stop_hook_active` | as expected |
| c7 missing `stop_hook_active` | block critic would block | `{}` + anomaly warning | `{}` |
| c8 malformed input | `not json` | `{}` | `{}` |
| c9 `transcript_path=null` | critic pass | `{}` | `{}` |

Quick single-case checks (no Claude Code needed):

```powershell
$payload = '{"session_id":"s","transcript_path":null,"cwd":"D:\\","permission_mode":"default","hook_event_name":"Stop","stop_hook_active":false,"last_assistant_message":"<your answer>"}'
$payload | .\bin\crisp.exe                         # {} when no critic is configured (fail open)

# block path: run a mock critic in a separate shell, then point Crisp at it
go run ./test/e2e/mockcritic -addr 127.0.0.1:19001 -verdict block
$env:QUALITY_GATE_BASE_URL = 'http://127.0.0.1:19001/v1'
$env:QUALITY_GATE_MODEL = 'mock-model'
$payload | .\bin\crisp.exe                         # {"decision":"block","reason":"..."}

# rewrite guard: stop_hook_active=true short-circuits before the critic
$payload = $payload.Replace('"stop_hook_active":false','"stop_hook_active":true')
$payload | .\bin\crisp.exe                         # {}
```

---

## 9. Installer verification matrix

Run against scratch copies under `.tmp/` (never the real user config); all checks passed:

| Scenario | Result |
|----------|--------|
| fresh settings file | valid JSON, one Crisp Stop handler, Stop array written |
| merge into existing settings | `model`/`env`/`permissions` and a foreign `PreToolUse` + `Stop` handler preserved |
| re-run without `-Force` | refuses with an actionable error (exit non-zero) |
| re-run with `-Force` | exactly one Crisp handler, foreign handler kept |
| invalid JSON without `-Force` | refuses, file untouched |
| invalid JSON with `-Force` | timestamped `.bak` created, new valid settings written |
| missing binary / bad `-TimeoutSeconds` | fails fast with a clear error |

---

## 10. Unverified / open items (honest gaps)

1. **No real Anthropic model or network was involved.** The Claude CLI ran
   against a local mock Messages API; answers and critic verdicts were canned.
   This validates hook protocol, decision handling, and the rewrite loop - not
   output quality on a real model. The committed harness
   (`scripts/e2e-claude.ps1` + `test/e2e/mockanthropic`) reproduces the same
   setup with the same limitation.
2. **CLI source:** the tested binary is the one bundled with the VS Code
   extension (`anthropic.claude-code-2.1.312-win32-x64/resources/native-binary/claude.exe`).
   A PATH-installed official `claude` CLI was not available on this machine.
   Behavior may differ across CLI versions; the input field set is versioned by
   Claude Code, so re-verify after upgrading. The harness auto-discovers the
   CLI and can be pointed at another build with `-ClaudePath`.
3. **Project/local settings hook trust/review was not exercised.** The E2E used
   isolated *user* settings. Project-level `.claude/settings.json` hooks are
   documented to need workspace trust and/or hook review; that interactive flow
   was not clicked through. docs/README wording should keep that caveat.
4. **Interactive TUI path not exercised.** Only `-p` (print) mode was run.
5. **Hook timeout behavior in Claude Code not measured.** The E2E used a 60 s
   timeout and quick hooks. Crisp's own fail-open timeout path is verified
   (`c3`), but whether Claude Code kills a timed-out hook process or just
   discards its result was not measured (Codex's Windows behavior is documented
   in `docs/codex-e2e.md` and does not kill).
6. **`async: true` and `onFailure`/`once`/`args` handler variants were not
   exercised**; the shipped config deliberately stays a plain blocking command
   hook.
7. **`disableAllHooks` was not exercised in the E2E** (the isolated settings
   file did not contain it); it is documented behavior of Claude Code settings.
8. **Non-Windows:** only Windows was tested (`shell: powershell`). The
   installer emits a POSIX variant (`'<path>'` with default shell) untested here.
9. The last-assistant-message/user-context forwarding was validated to *include
   the real prompt* on a real Claude transcript, but the critic request in the
   E2E was matched only by marker; the exact 2000-rune truncation behavior is
   covered by unit tests, not by the E2E.
10. **Without a Claude Code CLI the harness exits 0 with `SKIPPED`** (after
    listing every searched path), so a CI machine lacking the CLI does not
    perform a real E2E; it is a deliberate no-op, not a pass.

---

## 11. Data boundary

- The **full answer text** (`last_assistant_message`) and a **best-effort recent
  user message** (bounded to 2000 runes, read from the transcript tail, max
  512 KiB) are sent to the configured Critic service. Whether that is allowed
  depends on the critic endpoint and your compliance rules.
- Transcript tool output is **not** forwarded as user context after the fix in
  section 7; prose rules and answer text still flow to the critic.
- Crisp's stderr logs contain decision, rule types, timings and token counts -
  never the full answer, API keys, or transcript content.
- Claude Code's own session cost/usage must be read from Claude Code; total token
  cost = original answer + critic + rewrite.
