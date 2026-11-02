# Codex CLI Stop hook — real end-to-end validation (task-5)

Status: **Stop hooks work end-to-end in the installed Codex CLI.** Crisp's hook
contract (docs/design.md sections 6, 12.4, 12.5) is compatible with this build
apart from one measured Windows timeout nuance explained in section 7.

- Codex CLI: `codex-cli 0.145.0-alpha.18` (Windows x86_64)
- Binary: `C:\Users\71408\AppData\Local\OpenAI\Codex\bin\5dee10576ec7a5b8\codex.exe`
- Go toolchain used for Crisp: `go1.26.4 windows/amd64`
- Validation date: 2026-11-02 (Asia/Shanghai)
- Harness: `test/e2e/`, `scripts/`, project hook in `.codex/hooks.json`

Everything below is either (a) reproduced end-to-end against the real CLI, or
(b) extracted from the binary's embedded protocol definitions. Inferred or
unverified items are marked explicitly; nothing is claimed without evidence.

---

## 1. Verified summary

| # | Claim | Evidence |
|---|-------|----------|
| 1 | The `hooks` feature exists and is enabled by default (stage `stable true`) | `codex features list` output |
| 2 | `$CODEX_HOME/hooks.json` is read | invalid JSON there produced `warning: failed to parse hooks config <CODEX_HOME>\hooks.json: ...` |
| 3 | Project `<repo>/.codex/hooks.json` is read for a **trusted** project | invalid-JSON probe + real hook fired; skipped when the project trust entry was absent |
| 4 | The design 6.5 hooks.json shape is accepted | hook fired (`hook: Stop` → `hook: Stop Completed`) with exactly that shape |
| 5 | Stop fires on the real CLI after an assistant turn | marker file written / capture records / `hook: Stop` in codex stderr |
| 6 | Stop input has exactly the 9 schema fields, all required | embedded JSON schema + captured live payload (section 4) |
| 7 | `stop_hook_active` is false on the first Stop, true on the Stop after a block | captures-loop.jsonl (section 6) |
| 8 | A block decision causes Codex to regenerate the answer | 2 model requests; final answer is the rewrite; 1 critic call only |
| 9 | A second Stop after a rewrite passes immediately | crisp `quality_skip skip_reason=stop_hook_active`, stdout `{}`, no extra model request |
| 10 | hooks.json field name is `timeout`, unit seconds | `timeout`: 2` changed the outcome to `hook: Stop Failed`; `timeout`: 30` let a 3 s hook complete |
| 11 | Untrusted hooks do not run in `codex exec` | marker absent / no capture without `--dangerously-bypass-hook-trust` |
| 12 | A timed-out hook's output is discarded (fail-open) | late synthetic block after timeout: no rewrite, original answer delivered |
| 13 | Codex's default Stop input passthrough does not include Crisp's stderr logs | codex exec output shows only its own `hook: Stop ...` status lines |
| 14 | Full E2E harness is green | `scripts/e2e-codex.ps1` run `run-20261102-215036`: **ALL 8 CHECKS PASSED** (S0 replay 12/12 + S1–S6) |

## 2. Unverified / caveats (honest gaps)

1. **Timeout does not terminate the hook process on this Windows build.**
   Measured: `timeout`: 2` with a hook sleeping 4 s or 8 s → Codex reports
   `hook: Stop Failed` and discards the output, but the hook process kept
   running to completion (observed lifetimes 5.0 s / 8.0 s) and `codex exec`
   wall time covered that lifetime. Section 7 has the numbers. The design
   section 12.5 expectation "Codex terminates the hook at ~2 s" did **not**
   reproduce; the enforced guarantee is "output after the deadline is ignored",
   not "process is killed".
2. Hook trust was only exercised through `--dangerously-bypass-hook-trust`.
   The interactive TUI trust flow ("Hooks need review" → "Trust all and
   continue") was observed only as binary strings, not clicked through.
3. `config.toml` `[hooks]` configuration was not exercised (the binary
   contains the types and the "loading hooks from both ... and ...; prefer a
   single representation for this layer" warning, but the project/user
   `hooks.json` route is the tested one).
4. Commands whose executable path contains spaces were not tested; the hook
   command in the checked-in config has no spaces.
5. The mock provider reproduces the Responses API subset Codex needs; other
   model providers / real network providers were not involved.
6. The end-to-end script `scripts/e2e-codex.ps1` now runs green from a clean
   state: run `run-20261102-215036` reports **ALL 8 CHECKS PASSED** (S0 replay
   12/12 and S1–S6). An earlier iteration had a Windows file-handle bug
   (reading `Start-Process` redirect files while the mock servers were alive,
   which also leaked mock-server processes); the script now allocates the ports
   up front, probes readiness over TCP, reads JSONL logs with an explicit
   `FileShare.ReadWrite` handle, and waits for mock servers in `finally`.
7. `crisp` is fail-open at every stage, so "hook passes" can also mean
   "critic was not configured". The loop-guard run configured the mock critic
   and its stderr log shows `critic_called=true` on the first Stop.

## 3. Hook discovery and configuration (verified)

- Feature flag: `codex features list` shows `hooks  stable  true`. No
  `--enable hooks` is required.
- **User layer:** `$CODEX_HOME/hooks.json` is always read (a deliberate invalid
  JSON probe produced the parse warning naming that absolute path).
- **Project layer:** `<project-root>/.codex/hooks.json` is read only when the
  project is trusted. Trust is recorded in the user config:
  `[projects.'D:\05_Code\Crisp'] trust_level = "trusted"` (TOML literal
  key). Without it the project hook is silently not loaded.
- Project root detection uses `project_root_markers`; the harness adds a
  `.projectroot` marker in a scratch directory under `.tmp/` so E2E never
  touches git state (the scratch project is not a git repository).
- Codex also has a root `hooks` key in config.toml (types `HookEventsToml`,
  `MatcherGroup`, `HookHandlerConfig`, `HookStateToml` in the binary). A
  warning string exists for using both representations in one layer. Not
  exercised here.
- Hook command execution happens as an OS process from the command string. On
  Windows the tested command form is `<absolute .exe path> <args...>`.

### Trust gate

Hooks do not run until trusted. Two independent gates were observed:

1. **Hook trust**: reviewed/trusted hooks only. `codex exec` has
   `--dangerously-bypass-hook-trust` ("Intended only for automation that
   already vets hook sources"). Without it the Stop command did not execute.
2. **Project trust**: the project-layer `hooks.json` is only discovered for a
   project with `trust_level = "trusted"`. The bypass flag does not replace
   this project-layer gate.

For the harness we pass both: trusted project entry in the isolated
`CODEX_HOME/config.toml` + `--dangerously-bypass-hook-trust`.

## 4. Exact Stop hook input schema

From the JSON schema embedded in the binary (title `stop.command.input`,
`additionalProperties: false`), all nine properties required:

| field | type | notes |
|-------|------|-------|
| `cwd` | string | working directory |
| `hook_event_name` | const `"Stop"` | |
| `last_assistant_message` | string or null | present even if null |
| `model` | string | |
| `permission_mode` | enum `default|acceptEdits|plan|dontAsk|bypassPermissions` | |
| `session_id` | string | |
| `stop_hook_active` | boolean | basic loop guard |
| `transcript_path` | string or null | null with `codex exec --ephemeral`; real rollout path otherwise |
| `turn_id` | string | "Codex extension: expose the active turn id to internal turn-scoped hooks" |

Captured live first-Stop payload (from `.tmp/e2e/captures-pass.jsonl`):

```json
{"hook_event_name":"Stop","cwd":"D:\05_Code\Crisp",
 "session_id":"01a19c71-918d-74a1-9008-8592c151ab21",
 "turn_id":"01a19c71-91fd-7303-88f0-b488a7f8b2e5",
 "model":"mock-model","permission_mode":"bypassPermissions",
 "stop_hook_active":false,"transcript_path":null,
 "last_assistant_message":"<answer text>"}
```

Field set was byte-identical across runs:
`cwd, hook_event_name, last_assistant_message, model, permission_mode, session_id, stop_hook_active, transcript_path, turn_id`.
Note the Stop event has **no** `agent_id`/`agent_type` fields; those belong to
`subagent-stop.command.input`.

Implication for Crisp: `internal/hook.Input` parses a compatible subset, so no
contract change is needed.

## 5. Exact Stop hook output schema

From the embedded schema (title `stop.command.output`,
`additionalProperties: false`):

`continue` (bool, default true), `decision` (null or `"block"`),
`reason` (string/null), `stopReason`, `suppressOutput`, `systemMessage`.
There is no `hookSpecificOutput` for Stop. Crisp's `{}` and
`{"decision":"block","reason":"..."}` are both accepted; a block without a
non-empty reason is rejected by Codex ("Stop hook returned decision:block
without a non-empty reason").

Codex status lines observed in `codex exec` stderr:

- `hook: Stop` — hook started
- `hook: Stop Completed` — pass decision accepted
- `hook: Stop Blocked` — block decision accepted, turn continued
- `hook: Stop Failed` — invalid/timeout hook output, decision ignored

Crisp's own structured stderr JSON does **not** appear in `codex exec` output;
the harness wraps Crisp in `test/e2e/hooktap` to capture it (stdout is still
relayed unchanged, so Codex sees exactly Crisp's decision).

## 6. Loop guard: block → rewrite → second Stop (verified end-to-end)

Run: isolated `CODEX_HOME`, project `.codex/hooks.json` → hooktap → crisp,
mock Responses provider with answers `[flawed, improved]`, mock critic blocks
only when the request contains the flawed answer's marker.
Evidence file: `.tmp/e2e/captures-loop.jsonl` plus provider/critic JSONL logs.

| event | stop_hook_active | crisp decision | crisp stdout | stderr event |
|-------|------------------|----------------|--------------|--------------|
| Stop 1 end | `false` | block (critic material) | `{"decision":"block","reason":"请重新输出…"}` | `quality_check decision=block reason_type=semantic_duplicate critic_called=true` |
| Stop 2 end | `true` | pass | `{}` | `quality_skip skip_reason=stop_hook_active` (critic not called) |

Measured in that run:

- Provider model requests: **2** (original + exactly one rewrite, no third turn).
- Critic requests: **1** (second Stop short-circuits before the critic).
- `codex exec` exit code 0; stderr order:
  `hook: Stop` → `hook: Stop Blocked` → hook: Stop → `hook: Stop Completed`.
- Final stdout = the improved answer; the flawed answer was replaced.
- Hook latency inside Crisp: ~9–14 ms per invocation (critic mock local).

This is the section 12.4 requirement "重写后的第二次 Stop (stop_hook_active=true) → {}"
confirmed against the real CLI rather than replayed only.

## 7. Timeout field and measured timeout behavior

Field name: `timeout` in hooks.json, unit **seconds**. The Rust internal field
is `timeout_sec` and the app-server wire name is `timeoutSec` (both appear
in the binary; neither is accepted by hooks.json). Handler config fields seen in
the binary: `type` (command/prompt/agent), `command`, `commandWindows`,
`timeout`, `async`, `statusMessage`. Prompt/agent/async handlers are
skipped with warnings in this build.

Measured on 2026-11-02 (same isolated CODEX_HOME, hook = hooktap wrapper around
Crisp or a sleeping probe; codex exec READ-ONLY, no network):

| experiment | hooks.json `timeout` | hook duration | codex outcome | codex exec wall time | hook process result |
|------------|-----------------|---------------|---------------|----------------------|---------------------|
| baseline | 30 | immediate | `Stop Completed` | 3.8–5.1 s | completed |
| slow hook | 30 | 3.0 s | `Stop Completed` | 7.6 s | completed |
| slow hook | **2** | 5.0 s | `Stop Failed` | 6.5 s | **ran 5.01 s, not killed** |
| slow hook | **2** | 8.0 s | `Stop Failed` | 8.4 s | **ran 8.01 s, not killed** |
| late block output | **2** | 4.0 s (synthetic block) | `Stop Failed`, no rewrite | ≥ 4 s | ran to completion |

Interpretation (honest): the deadline is enforced as a hook **result** decision —
a hook that exceeds `timeout` is marked Failed and its output is discarded, so
a hung/late Crisp can never block delivery (fail-open at the CLI layer). On
this Windows build Codex did **not** terminate the hook process at the deadline
and its `codex exec` wait covered the hook's full lifetime. Since Crisp's own
total budget is 25 s (`QUALITY_GATE_TOTAL_TIMEOUT_MS`, below the 30 s hook
timeout), Crisp normally exits long before this matters. Do not rely on the
hook timeout to kill a wedged Crisp process.

Harness measurement (run-20261102-215036, same setup):

- S4a `timeout`: 2, hook sleeps 4.0 s and then emits a synthetic block:
  `codex exec` 6.0 s, hook process lifetime 4.0 s, outcome `hook: Stop Failed`,
  late block ignored (1 model request, original answer delivered).
- S4b `timeout`: 30, hook sleeps 1.5 s then emits the same block:
  `codex exec` 7.4 s, `hook: Stop Completed`, exactly one rewrite
  (2 model requests).
- `codex exec` includes ~4 s of startup/teardown overhead against the local
  mock provider, so compare scenarios against each other, not raw wall time.

Reproduction (short form):

```powershell
# isolated home + scratch project
$run = 'D:\05_Code\Crisp\.tmp\e2e\repro'
# CODEX_HOME/config.toml: mock provider (wire_api="responses"),
# project_root_markers + [projects.'<scratch>'] trust_level="trusted"
# project .codex/hooks.json: Stop -> command with "timeout": 2
$env:CODEX_HOME = "$run\home"; $env:MOCK_API_KEY = 'mock-key'; $env:NO_COLOR = '1'
& 'C:\Users\71408\AppData\Local\OpenAI\Codex\bin\5dee10576ec7a5b8\codex.exe' 
  exec --skip-git-repo-check -C "$run\project" -s read-only 
  --dangerously-bypass-hook-trust < prompt.txt
```

Prompt is passed on stdin to avoid Start-Process quoting issues. The hook's
start/end timestamps are recorded by `test/e2e/hooktap` in a JSONL capture file.

## 8. Reproducing the whole validation

```powershell
pwsh -File scripts/build.ps1                 # build bin/crisp.exe, prints go version
pwsh -File scripts/install-codex-hook.ps1 -Force
pwsh -File scripts/e2e-codex.ps1             # real codex.exe scenarios (see script help)
go run ./test/e2e/hookreplay -crisp bin/crisp.exe   # deterministic 12-case replay
```

See `scripts/e2e-codex.ps1 -Help` for scenario list (S0 replay, S1 pass,
S2 block/rewrite/loop guard, S3 hook-trust gate, S4a/S4b timeout pair, S5
checked-in config, S6 project-trust gate). The harness creates only `.tmp/`
scratch data; it never writes outside its run directory.

### Mock infrastructure (stdlib only, no new module dependencies)

- `test/e2e/mockprovider`: OpenAI **Responses API** (`wire_api = "responses"`
  is mandatory in this CLI; `wire_api="chat"` is rejected) with SSE events
  `response.created/in_progress/output_item.added/content_part.added/output_text.delta/done/content_part.done/output_item.done/completed`.
  Serves a canned answer sequence and logs request metadata (no prompt bodies).
- `test/e2e/mockcritic`: OpenAI chat-completions mock returning strict Crisp
  verdicts (`pass|block|invalid|http500`; `auto` blocks on a body marker).
- `test/e2e/hooktap`: hook wrapper capturing field presence/lengths/hashes of
  the payload (never the answer text) and Crisp's stdout/stderr/decision.
- `test/e2e/hookreplay`: offline protocol replay of the section 12.4 table
  against the real binary (12 cases: pass, block, minor-block pass, loop guard,
  missing `stop_hook_active`, empty message, invalid JSON, critic unconfigured,
  critic timeout/invalid JSON/HTTP 500, short answer). Latest run: **12/12 pass**.
- `scripts/build.ps1`: builds `bin/crisp.exe` and prints `go version`.
- `scripts/install-codex-hook.ps1`: writes `.codex/hooks.json` with an
  absolute binary path, `type`, `timeout` and `statusMessage`.
- `.codex/hooks.json` / `.codex/hooks.example.json`: project-level hook
  pointing directly at `D:\05_Code\Crisp\bin\crisp.exe`, `"timeout": 30`.

## 9. Known limitations / risks

1. Windows timeout does not kill the hook process (section 7). Keep Crisp's own
   total-timeout budget well below the hook `timeout`.
2. Hook trust must be granted interactively (TUI review) or bypassed for
   automation; unauthenticated/untrusted hooks are silently skipped in
   `codex exec`. Any CI using this hook needs
   `--dangerously-bypass-hook-trust` or a persisted trust state.
3. Project `.codex/hooks.json` requires the project trust entry; a fresh
   machine needs that config step before the hook works.
4. `codex exec` does not surface Crisp's stderr JSON; production debugging
   needs the Crisp log routed elsewhere (the hook wrapper in this repo is a
   test-only tap).
5. On Windows the CLI does not kill a hook that exceeds its timeout; if a
   parent `codex.exe` is force-killed it can leave an orphaned hook wrapper
   process behind. The E2E script cleans up its own test wrappers; production
   hook commands should stay short-lived and self-bounded (Crisp already is).
6. Hook commands with spaces in the executable path were not tested.
7. `config.toml` `[hooks]` route is unverified (see section 2).

## 10. Exact version strings

```text
codex-cli 0.145.0-alpha.18
go version go1.26.4 windows/amd64
Crisp: 0.1.0 (crisp version)
```