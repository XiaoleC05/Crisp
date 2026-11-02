# Generic Stop-hook protocol (adapter contract for other terminal agents)

This is the minimal contract an agent runtime must satisfy to use Crisp as a
quality gate at the end of a turn. Codex CLI and Claude Code already speak it
(see `docs/codex-e2e.md` and `docs/integrations/claude-code.md`); any other
terminal agent can either match it directly or use a small adapter wrapper.

Crisp is intentionally a **process + JSON on stdin/stdout** contract: no SDK, no
network service, no third-party Go dependencies. The host runs the binary when a
final answer is ready, then reacts to the one JSON object it prints.

---

## 1. Contract at a glance

```text
host (at end of turn, blocking) --stdin: one JSON object--> crisp
                                                              | rules + optional LLM critic
host <--stdout: one JSON object ({} or decision:block)--------+
```

- **Blocking.** The host must run Crisp synchronously and wait for its result.
  An async / fire-and-forget hook cannot block delivery, so a block decision
  would arrive too late to trigger a rewrite.
- **stdin** is one UTF-8 JSON object (no BOM, no trailing data), capped at
  4 MiB.
- **stdout** is exactly one JSON object and nothing else. Crisp always exits 0.
- The host must interpret the JSON decision, not the exit code. A host that
  cannot parse the output, or whose timeout expires, should **treat it as
  pass** (fail open) - that is the same invariant Crisp applies internally.

---

## 2. Input contract

### 2.1 Required field

| Field | Type | Meaning |
|-------|------|---------|
| `last_assistant_message` | string | The final answer text to review. Empty/whitespace -> pass immediately. |

### 2.2 Optional fields

| Field | Type | Meaning |
|-------|------|---------|
| `stop_hook_active` | bool | Per-turn rewrite guard. `true` means this Stop follows a rewrite; Crisp passes immediately. Missing -> pass + protocol-anomaly warning (fail open). |
| `transcript_path` | string or null | Path to the host's transcript JSONL. Read locally, best-effort, to supply the user's recent message to the critic. Missing/null/unreadable is fine. |
| `session_id` | string | Optional log correlation. |
| `turn_id` | string | Optional log correlation (Codex sends it; Claude Code does not). Never required. |
| `hook_event_name` | string | `"Stop"`. Stored but not validated. |

- **Unknown top-level fields are ignored on purpose.** Adapters may forward the
  host's native payload (Codex and Claude do exactly that) as long as the fields
  above decode; nested extra objects/arrays are fine.
- Extra fields whose names collide with the contract (`last_assistant_message`
  etc.) must have the documented type; a wrong type makes the whole input fail
  to parse -> Crisp fails open.
- Rejected inputs (all -> print `{}`, exit 0, log stderr): empty/whitespace
  stdin, invalid JSON, JSON array/scalar, trailing data after the object,
  oversized input, read error.

### 2.3 Transcript handling (optional optimization)

If `transcript_path` is given, Crisp reads at most the last 512 KiB of the
file, walks JSONL backwards for the most recent real user message, and caps it
to 2000 runes before sending it to the critic.

- The format is "JSONL where any user/assistant message may appear nested"
  (both Codex and Claude shapes are auto-detected best-effort).
- Claude Code "user" records that are actually tool results, sidechain events, or
  CLI meta messages are skipped (they are not user prompts). Adapters for other
  agents should avoid pointing `transcript_path` at a file where tool results
  are indistinguishable from user messages; when in doubt, omit the field.
- A wrong/absent transcript never blocks: it only reduces critic context.

### 2.4 Minimal and maximal examples

```json
{"last_assistant_message":"用 context.WithTimeout 包住请求即可，超时后记得检查 ctx.Err()。"}
```

```json
{
  "session_id": "4f7a2b0e-...",
  "turn_id": "turn-42",
  "transcript_path": "C:\\path\\to\\transcript.jsonl",
  "stop_hook_active": false,
  "last_assistant_message": "<the complete final answer>",
  "hook_event_name": "Stop"
}
```

---

## 3. Output contract

### 3.1 Pass - plain `{}`

```json
{}
```

Deliver the original answer unchanged.

### 3.2 Block - request a full rewrite

```json
{"decision":"block","reason":"请重新输出完整最终回答：合并语义重复的段落，删除无效铺垫和结论重复，保留全部必要事实、数字、代码和解释，不要为了缩短篇幅而删除必要信息。"}
```

Rules:

- Exactly two keys; `decision` is the literal `"block"`; `reason` must be
  non-empty (hosts may reject an empty reason).
- The host must **regenerate the complete final answer for the same turn**, with
  the reason supplied to the model. Crisp never edits/patches text.
- `hookSpecificOutput` and any other keys are never emitted for this contract;
  adapters must not invent them.
- stdout must contain no markdown fences, banners, warnings or debug text. All
  diagnostics go to stderr.

### 3.3 Exit code

Crisp exits 0 in every case, including a block and every internal failure. Hosts
must decide from the stdout JSON; a non-zero exit from a wrapper adapter should
be treated as pass.

---

## 4. Environment configuration

The hook process inherits the host's environment (so configure these where the
host launches hooks, or in a settings `env` block where supported):

| Variable | Default | Meaning |
|----------|---------|---------|
| `QUALITY_GATE_BASE_URL` | empty | OpenAI-compatible critic base URL. Empty -> critic disabled. |
| `QUALITY_GATE_API_KEY` | empty | Bearer key; never commit it, never log it. |
| `QUALITY_GATE_MODEL` | empty | Critic model name. |
| `QUALITY_GATE_MODE` | `balanced` | `balanced` or `economy`. |
| `QUALITY_GATE_CRITIC_TIMEOUT_MS` | 8000 | Per-critic-request HTTP timeout. |
| `QUALITY_GATE_TOTAL_TIMEOUT_MS` | 25000 | Whole-process budget. **Must stay below the host hook timeout.** |
| `QUALITY_GATE_MIN_CRITIC_RUNES` | 120 | Shorter natural-language answers skip the critic unless rules fired. |

Without a configured critic the gate only runs the deterministic rules and
**fails open** (it never blocks), so a production adapter install must configure
a critic. Recommended host-side timeout: at least 30 s (so 25 s of Crisp budget
plus process startup fits comfortably).

---

## 5. Fail-open invariants (hard requirements)

1. Any internal anomaly - parse failure, timeout, network error, critic HTTP
   error, invalid critic JSON, unknown enum, panic - results in `{}`.
2. stdout is always exactly one valid JSON object; never empty, never two
   objects, never human text.
3. Crisp never blocks when `stop_hook_active` is missing; adapters/hosts
   should not treat that pass as a quality verdict.
4. Crisp only reads stdin, the optional transcript tail, and the configured
   critic URL. It writes no files and does not modify the answer/transcript.
5. Logs go to stderr as one JSON object per line (decision, rule types, timings,
   token counts); the full answer, API key, and transcript content are not
   logged.
6. The host must fail open too: hook timeout, missing binary, unparsable stdout,
   or crash => deliver the original answer.

---

## 6. Per-turn rewrite guard (`stop_hook_active`)

Crisp's own loop guard is:

| `stop_hook_active` | Crisp behavior |
|---------------------|----------------|
| missing | print `{}`, log `stop_hook_active_missing` (fail open) |
| `true` | print `{}`, log `quality_skip stop_hook_active`, **critic not called** |
| `false` | review the answer normally (pass or block) |

So the **host/adapter must set `stop_hook_active=true` on the Stop that follows a
block/rewrite**, otherwise the same turn could be rewritten repeatedly (Crisp
does not persist cross-process state). The host can satisfy this in two ways:

1. it already has a per-turn "already rewrote" flag (Codex and Claude Code set
   this field themselves - verified end-to-end), or
2. the adapter tracks the turn itself (e.g. a small per-session state file keyed
   by `session_id`/`turn_id`) and injects the field before piping to Crisp.

A host that cannot provide a turn-scoped flag must enforce "at most one rewrite
per turn" itself instead.

---

## 7. Adapting an agent: decision table

| Host capability | Recommended integration |
|-----------------|-------------------------|
| Blocking pre-delivery command hook with stdin JSON + stdout JSON (Codex, Claude Code) | Point the hook at Crisp directly; optionally forward the host's native payload. |
| Blocking hook but a different output schema | Adapter wrapper: build the minimal Crisp input (section 2), run Crisp, translate the block reason to the host's decision format. |
| Blocking hook with a non-JSON stdin format | Adapter extracts the final answer, builds the minimal JSON, relays Crisp's stdout in the host's expected format. |
| Only async / post-hoc hooks (observe after delivery) | Cannot prevent delivery. Either (a) use the verdict as an audit signal and schedule a corrective turn that explicitly asks for a rewrite (UX-visible, does not replace the already-delivered answer), or (b) run Crisp behind a local API proxy (design Phase 4 fallback) that holds the stream until the check completes. |
| No extension points at all | Offline/CI use of `crisp check-text` only, or the proxy fallback. |
| HTTP/prompt/agent-type hooks | Wrap Crisp in a command handler; do not implement the check in the host's prompt/LLM hook. |

A wrapper adapter is deliberately small: read stdin, build the Crisp payload,
run `crisp.exe`, write its stdout verbatim. Do not re-serialize with a custom
encoder that may reorder/escape the block JSON incorrectly; validate and relay.

### 7.1 Minimal wrapper recipes

Windows / PowerShell host that already provides a compatible payload
(identical to how the repo installs it):

```powershell
& '<path-to-crisp.exe>'
```

POSIX shell host:

```sh
'<path-to-crisp>'
```

Adapter that must synthesize the payload from a host-specific event:

```powershell
# $answer = host's final answer text; $turnAlreadyRewritten = host state
$payload = [ordered]@{
    session_id             = $sessionId
    transcript_path        = $transcriptPath   # or $null
    stop_hook_active       = [bool]$turnAlreadyRewritten
    last_assistant_message = $answer
    hook_event_name        = 'Stop'
} | ConvertTo-Json -Depth 5 -Compress
$decision = $payload | & '<path-to-crisp.exe>'
# relay $decision to the host unchanged ({} = pass)
```

Important: pass the answer through **verbatim** (no markdown stripping, no
truncation) and make sure the wrapper's own stdout contains nothing but Crisp's
JSON (write wrapper diagnostics to stderr).

---

## 8. Test checklist for a new adapter

Run these before claiming an integration works:

1. `echo '{"last_assistant_message":"<normal answer>"}' | crisp` -> `{}`.
2. Mock critic returns `block/material` -> stdout is
   `{"decision":"block","reason":"<non-empty>"}`.
3. `stop_hook_active=true` -> `{}` and the critic is not called.
4. Critic timeout / HTTP 500 / invalid JSON -> `{}` with `fail_open:true` in
   stderr.
5. Malformed stdin, empty stdin, trailing data -> `{}`, exit 0.
6. stdout is always one valid JSON object (parse it in the host).
7. Host actually consumes a block and regenerates the answer; exactly one
   rewrite per turn; final answer is the rewrite.
8. Hook timeout > `QUALITY_GATE_TOTAL_TIMEOUT_MS` + startup overhead.
9. Answer text / API key do not appear in logs; decision metrics do.
10. Host-side fail-open: kill Crisp / move the binary away => original answer is
    delivered.

The repo's own evidence for steps 1-9:
`go test ./internal/hook`, `go test ./cmd/crisp`, the Codex E2E in
`docs/codex-e2e.md`, and the Claude Code protocol matrix + real-CLI E2E in
`docs/integrations/claude-code.md`.

---

## 9. Compatibility and versioning notes

- The contract is **additive**: Crisp ignores unknown input fields, and emits
  only `{}` or `{"decision":"block","reason":...}`.
- Field names are stable snake_case; `turn_id` is always optional.
- Adapters should include `session_id`/`turn_id` when available: they are only
  used for log correlation and never affect decisions.
- The critic decision rule is fixed: only a critic verdict with
  `decision=block` and `severity=material` blocks; rules alone never block.
- Data boundary: the answer text and a best-effort recent user message are sent
  to the configured critic service. See the data-boundary section in
  `README.md` and `docs/integrations/claude-code.md` before deploying to a
  privacy-sensitive environment.

## 10. References

- Design spec: `docs/design.md` sections 6 (protocol), 9 (decision), 10 (env),
  12.4 (hook integration tests).
- Codex real-cli validation: `docs/codex-e2e.md`.
- Claude Code integration: `docs/integrations/claude-code.md`.
- Config templates: `.codex/hooks.example.json`, `.claude/settings.example.json`.
- Installers: `scripts/install-codex-hook.ps1`, `scripts/install-claude-hook.ps1`.
