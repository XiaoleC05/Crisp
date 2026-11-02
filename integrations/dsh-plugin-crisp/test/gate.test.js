import assert from "node:assert/strict";
import { test } from "node:test";

import { applyWithDeps } from "../index.js";
import { DEFAULT_REWRITE_INSTRUCTION } from "../lib/config.js";
import {
  countRunes,
  createAccumulator,
  makeRewriteMessage,
} from "../lib/gate.js";
import {
  createFakeAgent,
  createFakeAgents,
  createFakeCtx,
  createFakeRunner,
  downstreamFrom,
  emitSessionEvent,
  emitTurnEnd,
  emitTurnStart,
  finalAnswerChunks,
  rawBlock,
  rawFailure,
  rawPass,
  runStream,
  textChunks,
} from "./helpers.js";

/** Compose the plugin with a hermetic fake context and capture records. */
function setup(overrides = {}) {
  const logs = [];
  const agents = createFakeAgents();
  const agent = overrides.agent === undefined ? createFakeAgent("s1", overrides.agentOptions) : overrides.agent;
  if (agent !== null) agents.add(agent);
  const ctx = createFakeCtx({ agents: agents.service });
  const runner = overrides.runner ?? createFakeRunner(rawPass());
  const config = {
    mode: overrides.mode ?? "observe",
    minRunes: overrides.minRunes ?? 0,
    maxRunes: overrides.maxRunes ?? 200_000,
    timeoutMs: overrides.timeoutMs ?? 5_000,
    ...overrides.config,
  };
  const gate = applyWithDeps(ctx, config, {
    env: overrides.env ?? {},
    platform: "win32",
    logLine: (record) => logs.push(record),
    runner,
    isAgentLoopRequest: overrides.isAgentLoopRequest ?? (() => true),
  });
  const events = (name) => logs.filter((record) => record.event === name);
  return {
    ctx,
    gate,
    logs,
    runner,
    agents,
    agent,
    checks: () => events("crisp_quality_check"),
    skips: () => events("crisp_quality_skip"),
    guardFollowups: () => events("crisp_guard_followup"),
    guardSkips: () => events("crisp_guard_skipped"),
    guardErrors: () => events("crisp_guard_error"),
    activation: () => events("crisp_quality_gate_active"),
    events,
  };
}

const LONG_ANSWER = "A long enough answer with a material problem. ".repeat(3);

test("observe mode streams every chunk through unchanged and logs one check", async () => {
  const { ctx, logs, runner, checks, activation } = setup();
  const chunks = finalAnswerChunks("This is a normal complete answer.");
  const out = await runStream(ctx, { sessionId: "s1" }, chunks);
  assert.equal(out.length, chunks.length);
  for (let index = 0; index < chunks.length; index += 1) assert.equal(out[index], chunks[index]);
  assert.equal(activation().length, 1);
  assert.equal(runner.calls.length, 1);
  assert.equal(runner.calls[0].input, "This is a normal complete answer.");
  assert.equal(checks().length, 1);
  const check = checks()[0];
  assert.equal(check.mode, "observe");
  assert.equal(check.session_id, "s1");
  assert.equal(check.decision, "pass");
  assert.deepEqual(check.rule_types, []);
  assert.equal(check.critic_called, true);
  assert.equal(check.fail_open, false);
  assert.equal(check.answer_runes, "This is a normal complete answer.".length);
  assert.equal(check.check_latency_ms >= 0, true);
  assert.equal(check.timed_out, false);
  assert.equal(check.exit_code, 0);
  // No answer text, no credentials in any record.
  const serialized = JSON.stringify(logs);
  assert.ok(!serialized.includes("This is a normal complete answer"));
});

test("visible text excludes reasoning and falls back to block-end text", async () => {
  const { ctx, runner } = setup();
  const chunks = [
    { type: "reasoning-delta", index: 1, text: "hidden thoughts" },
    { type: "block-start", index: 0, blockType: "text" },
    { type: "text-delta", index: 0, text: "Vis" },
    { type: "text-delta", index: 0, text: "ible" },
    { type: "block-end", index: 0, block: { type: "text", text: "Visible" } },
    { type: "block-start", index: 2, blockType: "text" },
    { type: "block-end", index: 2, block: { type: "text", text: " fallback" } },
    { type: "finish", reason: { kind: "stop" } },
  ];
  await runStream(ctx, { sessionId: "s1" }, chunks);
  assert.equal(runner.calls.length, 1);
  assert.equal(runner.calls[0].input, "Visible fallback");
});

test("steps that call tools are skipped silently", async () => {
  const { ctx, runner, checks, skips } = setup();
  const chunks = [
    ...textChunks("Let me check the file."),
    { type: "tool-call-delta", index: 1, id: "call-1", argumentsDelta: "{}" },
    { type: "finish", reason: { kind: "stop" } },
  ];
  await runStream(ctx, { sessionId: "s1" }, chunks);
  assert.equal(runner.calls.length, 0);
  assert.equal(checks().length, 0);
  assert.equal(skips().length, 0);
});

test("short answers are skipped with a structured record", async () => {
  const { ctx, runner, skips, checks } = setup({ minRunes: 40 });
  await runStream(ctx, { sessionId: "s1" }, finalAnswerChunks("短回答"));
  assert.equal(runner.calls.length, 0);
  assert.equal(checks().length, 0);
  assert.equal(skips().length, 1);
  assert.equal(skips()[0].skip_reason, "short_answer");
  assert.equal(skips()[0].answer_runes, 3);
});

test("non-stop finishes are skipped, never checked", async () => {
  const scenarios = [
    ["error", { kind: "error", failure: { code: "AUTH", message: "no key" } }, "finish_error"],
    ["max-tokens", { kind: "max-tokens" }, "finish_max-tokens"],
    ["aborted", { kind: "aborted", failure: { code: "ABORTED", message: "cancelled" } }, "finish_aborted"],
  ];
  for (const [name, reason, expected] of scenarios) {
    const { ctx, runner, skips } = setup();
    await runStream(ctx, { sessionId: "s1" }, [
      ...textChunks("Some visible text that is long enough."),
      { type: "finish", reason },
    ]);
    assert.equal(runner.calls.length, 0, name);
    assert.equal(skips().length, 1, name);
    assert.equal(skips()[0].skip_reason, expected, name);
  }
});

test("a stream without a terminal finish chunk is skipped", async () => {
  const { ctx, runner, skips } = setup();
  await runStream(ctx, { sessionId: "s1" }, textChunks("An answer without a finish chunk."));
  assert.equal(runner.calls.length, 0);
  assert.equal(skips()[0].skip_reason, "missing_finish");
});

test("fail-open runner outcomes still pass the stream and log fail_open", async () => {
  const cases = [
    ["timeout", rawFailure({ timedOut: true, errorMessage: "timeout" })],
    ["nonzero", rawFailure({ exitCode: 2 })],
    ["bad json", rawFailure({ stdout: "not json" })],
    ["spawn error", rawFailure({ errorMessage: "ENOENT" })],
  ];
  for (const [name, raw] of cases) {
    const { ctx, checks } = setup({ runner: createFakeRunner(raw) });
    const chunks = finalAnswerChunks("A complete answer body for fail-open tests.");
    const out = await runStream(ctx, { sessionId: "s1" }, chunks);
    assert.equal(out.length, chunks.length, name);
    assert.equal(checks().length, 1, name);
    const check = checks()[0];
    assert.equal(check.decision, "pass", name);
    assert.equal(check.fail_open, true, name);
    assert.equal(typeof check.error, "string", name);
  }
});

test("requests without a session id are not wrapped at all", () => {
  const { ctx, runner } = setup();
  const handler = ctx.handlersFor("llm/stream")[0];
  const downstream = downstreamFrom(finalAnswerChunks("title"))();
  const result = handler({ messages: [], provider: "deepseek-official" }, () => downstream);
  assert.equal(result, downstream);
  assert.equal(runner.calls.length, 0);
});

test("auxiliary model calls with a purpose are not wrapped", () => {
  const { ctx, runner } = setup();
  const handler = ctx.handlersFor("llm/stream")[0];
  const downstream = downstreamFrom(finalAnswerChunks("title"))();
  for (const purpose of ["session-title", "compaction"]) {
    const result = handler({ sessionId: "s1", purpose }, () => downstream);
    assert.equal(result, downstream, purpose);
  }
  assert.equal(runner.calls.length, 0);
});

test("disabled config is a complete pass-through", () => {
  const { ctx, runner } = setup({ config: { enabled: false } });
  const handler = ctx.handlersFor("llm/stream")[0];
  const downstream = downstreamFrom(finalAnswerChunks("whatever"))();
  const result = handler({ sessionId: "s1" }, () => downstream);
  assert.equal(result, downstream);
  assert.equal(runner.calls.length, 0);
});

test("the agent-loop detector gates checking", async () => {
  const { ctx, runner, checks } = setup({ isAgentLoopRequest: () => false });
  await runStream(ctx, { sessionId: "s1" }, finalAnswerChunks("Answer long enough to check."));
  assert.equal(runner.calls.length, 0);
  assert.equal(checks().length, 0);
});

test("session state is dropped on session/disposed", () => {
  const { ctx, gate } = setup();
  emitTurnStart(ctx, "s1", 1);
  assert.equal(gate._states.size, 1);
  ctx.emit("session/disposed", { id: "s1" });
  assert.equal(gate._states.size, 0);
});

test("guard mode requests exactly one followup rewrite for a blocked turn", async () => {
  const { ctx, agent, guardFollowups, guardSkips } = setup({
    mode: "guard",
    runner: createFakeRunner(rawBlock()),
  });
  emitTurnStart(ctx, "s1", 1);
  await runStream(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  // The followup is queued while the step is still running, so the one-shot
  // headless runner also picks the rewrite turn up instead of exiting.
  assert.equal(agent.followups.length, 1);
  assert.equal(guardFollowups().length, 1);
  assert.equal(guardFollowups()[0].turn, 1);
  const message = agent.followups[0];
  assert.equal(message.role, "user");
  assert.equal(message.content.length, 1);
  assert.equal(message.content[0].type, "text");
  assert.equal(message.content[0].text, DEFAULT_REWRITE_INSTRUCTION);
  assert.equal(message.source.kind, "crisp-quality-gate");
  assert.equal(typeof message.id, "string");
  emitTurnEnd(ctx, "s1", 1);

  // The rewrite's own turn is never rewritten again.
  emitTurnStart(ctx, "s1", 2);
  await runStream(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  assert.equal(agent.followups.length, 1);
  assert.equal(guardSkips().length, 1);
  assert.equal(guardSkips()[0].skip_reason, "rewrite_turn");
  emitTurnEnd(ctx, "s1", 2);

  // A later, separate turn can be gated again.
  emitTurnStart(ctx, "s1", 3);
  await runStream(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  assert.equal(agent.followups.length, 2);
  assert.notEqual(agent.followups[0].id, agent.followups[1].id);
});

test("guard mode rewrites at most once per turn even with several attempts", async () => {
  const { ctx, agent, guardSkips } = setup({ mode: "guard", runner: createFakeRunner(rawBlock()) });
  emitTurnStart(ctx, "s1", 1);
  await runStream(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  await runStream(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  assert.equal(agent.followups.length, 1);
  assert.equal(guardSkips().length, 1);
  assert.equal(guardSkips()[0].skip_reason, "already_rewritten");
});

test("guard mode requires a known turn", async () => {
  const { ctx, agent, guardSkips, checks } = setup({ mode: "guard", runner: createFakeRunner(rawBlock()) });
  await runStream(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  assert.equal(checks()[0].decision, "block");
  assert.equal(agent.followups.length, 0);
  assert.equal(guardSkips().some((record) => record.skip_reason === "no_turn"), true);
});

test("guard mode queues the rewrite before the turn ends and keeps it once", async () => {
  const { ctx, agent } = setup({ mode: "guard", runner: createFakeRunner(rawBlock()) });
  emitTurnStart(ctx, "s1", 1);
  await runStream(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  assert.equal(agent.followups.length, 1);
  emitTurnEnd(ctx, "s1", 1, { kind: "error" });
  assert.equal(agent.followups.length, 1);
});

test("guard mode does not rewrite after a fail-open decision", async () => {
  const { ctx, agent } = setup({
    mode: "guard",
    runner: createFakeRunner(rawFailure({ timedOut: true, errorMessage: "timeout" })),
  });
  emitTurnStart(ctx, "s1", 1);
  await runStream(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  assert.equal(agent.followups.length, 0);
});

test("observe mode never requests a rewrite", async () => {
  const { ctx, agent } = setup({ mode: "observe", runner: createFakeRunner(rawBlock()) });
  emitTurnStart(ctx, "s1", 1);
  await runStream(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  assert.equal(agent.followups.length, 0);
});

test("guard skips cleanly when the agent is unavailable", async () => {
  const { ctx, guardSkips } = setup({
    mode: "guard",
    agent: null,
    runner: createFakeRunner(rawBlock()),
  });
  emitTurnStart(ctx, "s1", 1);
  await runStream(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  assert.equal(guardSkips().some((record) => record.skip_reason === "agent_unavailable"), true);
});

test("a refusing followup is logged, not thrown", async () => {
  const agent = createFakeAgent("s1", { throwOnFollowup: true });
  const { ctx, guardErrors } = setup({ mode: "guard", agent, runner: createFakeRunner(rawBlock()) });
  emitTurnStart(ctx, "s1", 1);
  await assert.doesNotReject(async () => {
    await runStream(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  });
  assert.equal(guardErrors().length, 1);
  assert.equal(guardErrors()[0].error, "followup refused");
});

test("activation record reports config warnings and log destination", () => {
  const { activation } = setup({ config: { mode: "bogus-mode" } });
  const record = activation()[0];
  assert.equal(record.mode, "observe");
  assert.ok(record.warnings.length >= 1);
  assert.equal(record.log_file, "stderr");
});

test("accumulator concatenates text blocks in index order", () => {
  const acc = createAccumulator();
  acc.push({ type: "text-delta", index: 2, text: "B" });
  acc.push({ type: "text-delta", index: 0, text: "A" });
  acc.push({ type: "finish", reason: { kind: "stop" } });
  assert.equal(acc.text(), "AB");
  assert.equal(acc.hasToolCall(), false);
  assert.equal(acc.finishKind(), "stop");
});

test("countRunes counts code points, not UTF-16 units", () => {
  assert.equal(countRunes("abc"), 3);
  assert.equal(countRunes("你好"), 2);
  assert.equal(countRunes("😀"), 1);
  assert.equal(countRunes(""), 0);
  assert.equal(countRunes(undefined), 0);
});

test("makeRewriteMessage produces unique durable user messages", () => {
  const first = makeRewriteMessage();
  const second = makeRewriteMessage("custom instruction");
  assert.notEqual(first.id, second.id);
  assert.equal(first.content[0].text, DEFAULT_REWRITE_INSTRUCTION);
  assert.equal(second.content[0].text, "custom instruction");
  assert.equal(first.source.kind, "crisp-quality-gate");
});
