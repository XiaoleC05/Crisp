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
  createFakeLlm,
  createFakeRunner,
  criticChunks,
  downstreamFrom,
  emitSessionEvent,
  emitTurnEnd,
  emitTurnStart,
  finalAnswerChunks,
  rawBlock,
  rawFailure,
  rawPass,
  rawRules,
  rewriteChunks,
  runStream,
  textChunks,
} from "./helpers.js";

/** Run one stream and settle any detached observe-mode evaluation. */
async function runStreamSettled(ctx, options, chunks) {
  const out = await runStream(ctx, options, chunks);
  if (typeof ctx.flushBackground === "function") await ctx.flushBackground();
  return out;
}

/** Compose the plugin with a hermetic fake context and capture records. */
function setup(overrides = {}) {
  const logs = [];
  const agents = createFakeAgents();
  const agent = overrides.agent === undefined ? createFakeAgent("s1", overrides.agentOptions) : overrides.agent;
  if (agent !== null) agents.add(agent);
  const ctx = createFakeCtx({ agents: agents.service, llm: overrides.llm });
  const runner = overrides.runner ?? createFakeRunner(rawPass());
  const config = {
    mode: overrides.mode ?? "observe",
    minRunes: overrides.minRunes ?? 0,
    maxRunes: overrides.maxRunes ?? 200_000,
    timeoutMs: overrides.timeoutMs ?? 5_000,
    ...overrides.criticConfig,
    ...overrides.config,
  };
  const backgroundTasks = [];
  const background = overrides.background ?? ((fn) => { backgroundTasks.push(fn); });
  async function flushBackground() {
    while (backgroundTasks.length > 0) {
      const tasks = backgroundTasks.splice(0);
      await Promise.all(tasks.map((task) => Promise.resolve().then(task)));
    }
  }
  ctx.flushBackground = flushBackground;
  const gate = applyWithDeps(ctx, config, {
    env: overrides.env ?? {},
    platform: "win32",
    logLine: (record) => logs.push(record),
    runner,
    isAgentLoopRequest: overrides.isAgentLoopRequest ?? (() => true),
    getLlm: overrides.getLlm ?? (overrides.llm === undefined ? undefined : () => overrides.llm),
    background,
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
  const out = await runStreamSettled(ctx, { sessionId: "s1" }, chunks);
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
  await runStreamSettled(ctx, { sessionId: "s1" }, chunks);
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
  await runStreamSettled(ctx, { sessionId: "s1" }, chunks);
  assert.equal(runner.calls.length, 0);
  assert.equal(checks().length, 0);
  assert.equal(skips().length, 0);
});

test("short answers are skipped with a structured record", async () => {
  const { ctx, runner, skips, checks } = setup({ minRunes: 40 });
  await runStreamSettled(ctx, { sessionId: "s1" }, finalAnswerChunks("短回答"));
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
    await runStreamSettled(ctx, { sessionId: "s1" }, [
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
  await runStreamSettled(ctx, { sessionId: "s1" }, textChunks("An answer without a finish chunk."));
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
    const out = await runStreamSettled(ctx, { sessionId: "s1" }, chunks);
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
  await runStreamSettled(ctx, { sessionId: "s1" }, finalAnswerChunks("Answer long enough to check."));
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
  await runStreamSettled(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
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
  await runStreamSettled(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  assert.equal(agent.followups.length, 1);
  assert.equal(guardSkips().length, 1);
  assert.equal(guardSkips()[0].skip_reason, "rewrite_turn");
  emitTurnEnd(ctx, "s1", 2);

  // A later, separate turn can be gated again.
  emitTurnStart(ctx, "s1", 3);
  await runStreamSettled(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  assert.equal(agent.followups.length, 2);
  assert.notEqual(agent.followups[0].id, agent.followups[1].id);
});

test("guard mode rewrites at most once per turn even with several attempts", async () => {
  const { ctx, agent, guardSkips } = setup({ mode: "guard", runner: createFakeRunner(rawBlock()) });
  emitTurnStart(ctx, "s1", 1);
  await runStreamSettled(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  await runStreamSettled(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  assert.equal(agent.followups.length, 1);
  assert.equal(guardSkips().length, 1);
  assert.equal(guardSkips()[0].skip_reason, "already_rewritten");
});

test("guard mode requires a known turn", async () => {
  const { ctx, agent, guardSkips, checks } = setup({ mode: "guard", runner: createFakeRunner(rawBlock()) });
  await runStreamSettled(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  assert.equal(checks()[0].decision, "block");
  assert.equal(agent.followups.length, 0);
  assert.equal(guardSkips().some((record) => record.skip_reason === "no_turn"), true);
});

test("guard mode queues the rewrite before the turn ends and keeps it once", async () => {
  const { ctx, agent } = setup({ mode: "guard", runner: createFakeRunner(rawBlock()) });
  emitTurnStart(ctx, "s1", 1);
  await runStreamSettled(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
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
  await runStreamSettled(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  assert.equal(agent.followups.length, 0);
});

test("observe mode never requests a rewrite", async () => {
  const { ctx, agent } = setup({ mode: "observe", runner: createFakeRunner(rawBlock()) });
  emitTurnStart(ctx, "s1", 1);
  await runStreamSettled(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  assert.equal(agent.followups.length, 0);
});

test("guard skips cleanly when the agent is unavailable", async () => {
  const { ctx, guardSkips } = setup({
    mode: "guard",
    agent: null,
    runner: createFakeRunner(rawBlock()),
  });
  emitTurnStart(ctx, "s1", 1);
  await runStreamSettled(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  assert.equal(guardSkips().some((record) => record.skip_reason === "agent_unavailable"), true);
});

test("a refusing followup is logged, not thrown", async () => {
  const agent = createFakeAgent("s1", { throwOnFollowup: true });
  const { ctx, guardErrors } = setup({ mode: "guard", agent, runner: createFakeRunner(rawBlock()) });
  emitTurnStart(ctx, "s1", 1);
  await assert.doesNotReject(async () => {
    await runStreamSettled(ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
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

// ---------------------------------------------------------------------------
// Harness-native critic mode (ctx.llm) and detached observe mode
// ---------------------------------------------------------------------------

const HARNESS_CRITIC_CONFIG = {
  critic: "harness",
  criticProvider: "deepseek-account",
  criticModel: "deepseek-flash",
};
const MATERIAL_VERDICT = {
  decision: "block",
  severity: "material",
  issues: [{ type: "semantic_duplicate", location: "paragraph_3", instruction: "Merge with paragraph 1." }],
};
const PASS_VERDICT = { decision: "pass", severity: "none", issues: [] };

test("observe mode runs the evaluation detached so it cannot delay the stream", async () => {
  const { ctx, checks, runner } = setup();
  const chunks = finalAnswerChunks("A complete answer body that is checked.");
  const out = await runStream(ctx, { sessionId: "s1" }, chunks);
  assert.equal(out.length, chunks.length);
  // Nothing has been checked yet: the queued background task has not run.
  assert.equal(checks().length, 0);
  assert.equal(runner.calls.length, 0);
  await ctx.flushBackground();
  assert.equal(checks().length, 1);
  assert.equal(runner.calls.length, 1);
});

test("harness critic: rules from crisp, verdict from ctx.llm, pass decision", async () => {
  const llm = createFakeLlm(
    criticChunks(PASS_VERDICT, { usage: { inputTokens: 10, outputTokens: 5, totalTokens: 15 } }),
  );
  const harness = setup({
    runner: createFakeRunner(rawRules()),
    llm,
    criticConfig: HARNESS_CRITIC_CONFIG,
  });
  await runStreamSettled(
    harness.ctx,
    { sessionId: "s1" },
    finalAnswerChunks("A long enough answer body for the harness critic to review."),
  );

  // Rules: the crisp child is told there is no HTTP critic, and it returns the
  // rule types while its own fail_open decision is ignored.
  assert.equal(harness.runner.calls.length, 1);
  assert.equal(harness.runner.calls[0].env.QUALITY_GATE_BASE_URL, "");
  assert.equal(harness.runner.calls[0].env.QUALITY_GATE_MODEL, "");

  // Semantic review: one harness LLM call with deterministic controls.
  assert.equal(llm.calls.length, 1);
  const options = llm.calls[0];
  assert.equal(options.provider, "deepseek-account");
  assert.equal(options.model, "deepseek-flash");
  assert.equal(options.reasoningEffort, "off");
  assert.equal(options.temperature, 0);
  assert.equal(options.maxTokens, 1024);
  assert.equal("sessionId" in options, false);
  assert.equal("purpose" in options, false);

  assert.equal(harness.checks().length, 1);
  const check = harness.checks()[0];
  assert.equal(check.critic, "harness");
  assert.equal(check.decision, "pass");
  assert.equal(check.reason_type, "critic_pass");
  assert.equal(check.critic_called, true);
  assert.equal(check.fail_open, false);
  assert.deepEqual(check.rule_types, ["duplicate_sentence", "filler_opening"]);
  assert.equal(check.natural_runes, 64);
  assert.equal(check.critic_tokens, 15);
  assert.equal(check.critic_latency_ms >= 0, true);
  assert.equal(check.exit_code, 0);
});

test("harness critic: material block becomes a block decision and one guard rewrite", async () => {
  const llm = createFakeLlm(
    criticChunks(MATERIAL_VERDICT, { usage: { inputTokens: 100, outputTokens: 20, totalTokens: 120 } }),
  );
  const harness = setup({
    mode: "guard",
    runner: createFakeRunner(rawRules()),
    llm,
    criticConfig: HARNESS_CRITIC_CONFIG,
  });
  emitTurnStart(harness.ctx, "s1", 1);
  await runStreamSettled(harness.ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));

  const check = harness.checks()[0];
  assert.equal(check.decision, "block");
  assert.equal(check.reason_type, "semantic_duplicate");
  assert.equal(check.critic_called, true);
  assert.equal(check.fail_open, false);
  assert.equal(check.critic_tokens, 120);
  assert.equal(harness.agent.followups.length, 1);
  assert.equal(harness.guardFollowups()[0].turn, 1);
});

test("harness critic: block with minor severity still passes (never a rewrite)", async () => {
  const llm = createFakeLlm(
    criticChunks({ decision: "block", severity: "minor", issues: [{ type: "filler" }] }),
  );
  const harness = setup({
    mode: "guard",
    runner: createFakeRunner(rawRules()),
    llm,
    criticConfig: HARNESS_CRITIC_CONFIG,
  });
  emitTurnStart(harness.ctx, "s1", 1);
  await runStreamSettled(harness.ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  assert.equal(harness.checks()[0].decision, "pass");
  assert.equal(harness.checks()[0].reason_type, "critic_pass");
  assert.equal(harness.agent.followups.length, 0);
});

test("harness critic failures are fail-open and never rewrite", async () => {
  const cases = [
    ["invalid json", createFakeLlm(criticChunks("I think this answer is fine.")), /invalid harness critic verdict/],
    ["missing llm", undefined, /ctx\.llm\) is not available/],
  ];
  for (const [name, llm, pattern] of cases) {
    const harness = setup({
      mode: "guard",
      runner: createFakeRunner(rawRules()),
      llm,
      criticConfig: HARNESS_CRITIC_CONFIG,
    });
    emitTurnStart(harness.ctx, "s1", 1);
    await runStreamSettled(harness.ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
    const check = harness.checks()[0];
    assert.equal(check.decision, "pass", name);
    assert.equal(check.fail_open, true, name);
    assert.equal(check.critic_called, true, name);
    assert.match(check.error, pattern, name);
    assert.equal(harness.agent.followups.length, 0, name);
  }
});

test("harness mode keeps reviewing when the crisp rules child fails", async () => {
  const llm = createFakeLlm(criticChunks(MATERIAL_VERDICT));
  const harness = setup({
    runner: createFakeRunner(rawFailure({ errorMessage: "spawn failed: ENOENT" })),
    llm,
    criticConfig: HARNESS_CRITIC_CONFIG,
  });
  await runStreamSettled(harness.ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  const check = harness.checks()[0];
  assert.equal(check.decision, "block");
  assert.deepEqual(check.rule_types, []);
  assert.equal(check.fail_open, false);
  const ruleError = harness.events("crisp_quality_error").find((record) => record.phase === "rules");
  assert.ok(ruleError);
  assert.match(ruleError.error, /spawn failed/);
});

test("http critic mode remains the default and never touches the harness LLM", async () => {
  const llm = createFakeLlm(criticChunks(MATERIAL_VERDICT));
  const harness = setup({ llm, runner: createFakeRunner(rawPass()) });
  await runStreamSettled(harness.ctx, { sessionId: "s1" }, finalAnswerChunks(LONG_ANSWER));
  assert.equal(llm.calls.length, 0);
  const check = harness.checks()[0];
  assert.equal(check.decision, "pass");
  assert.equal("critic" in check, false);
});

// ---------------------------------------------------------------------------
// delivery:"optimize" (pre-delivery streamed rewrite, no extra visible turn)
// ---------------------------------------------------------------------------

const OPTIMIZE_CONFIG = {
  delivery: "optimize",
  optimizerProvider: "deepseek-account",
  optimizerModel: "deepseek-flash",
};

function optimizeSetup(overrides = {}) {
  return setup({
    mode: "guard",
    ...overrides,
    config: { ...OPTIMIZE_CONFIG, ...(overrides.config ?? {}) },
  });
}

test("optimize delivery: pass replays the original chunks unchanged", async () => {
  const llm = createFakeLlm(rewriteChunks("should not be used"));
  const harness = optimizeSetup({ runner: createFakeRunner(rawPass()), llm });
  const chunks = finalAnswerChunks("A normal answer that passes review.");
  const out = await runStreamSettled(harness.ctx, { sessionId: "s1" }, chunks);
  assert.equal(out.length, chunks.length);
  for (let index = 0; index < chunks.length; index += 1) assert.equal(out[index], chunks[index]);
  assert.equal(llm.calls.length, 0, "pass path must not call the optimizer");
  assert.equal(harness.agent.followups.length, 0);
  assert.equal(harness.events("crisp_optimize_started").length, 0);
  assert.equal(harness.checks().length, 1);
  assert.equal(harness.checks()[0].decision, "pass");
});

test("optimize delivery: block yields only the streamed rewrite, no followup", async () => {
  const rewrite = rewriteChunks("Rewritten answer that keeps the facts.", {
    usage: { inputTokens: 30, outputTokens: 12, totalTokens: 42 },
  });
  const llm = createFakeLlm(rewrite);
  const harness = optimizeSetup({ runner: createFakeRunner(rawBlock()), llm });
  const original = finalAnswerChunks("A flawed original answer text.");
  const out = await runStreamSettled(harness.ctx, { sessionId: "s1" }, original);

  assert.equal(out.length, rewrite.length);
  for (let index = 0; index < rewrite.length; index += 1) assert.equal(out[index], rewrite[index]);
  for (const chunk of original) assert.ok(!out.includes(chunk), "original chunk must not reach the consumer");
  const visible = out.filter((chunk) => chunk.type === "text-delta").map((chunk) => chunk.text).join("");
  assert.equal(visible, "Rewritten answer that keeps the facts.");
  assert.ok(!visible.includes("flawed original"));

  assert.equal(llm.calls.length, 1, "exactly one rewrite");
  const options = llm.calls[0];
  assert.equal(options.provider, "deepseek-account");
  assert.equal(options.model, "deepseek-flash");
  assert.equal(options.purpose, "crisp-optimize");
  assert.equal("sessionId" in options, false);
  assert.equal(harness.agent.followups.length, 0, "no extra turn in optimize mode");

  const started = harness.events("crisp_optimize_started");
  const applied = harness.events("crisp_optimize_applied");
  assert.equal(started.length, 1);
  assert.equal(applied.length, 1);
  assert.equal(started[0].delivery, "optimize");
  assert.equal(applied[0].delivery, "optimize");
  assert.equal(applied[0].rewrite_runes, 38);
  assert.equal(applied[0].tokens, 42);
  assert.equal(harness.checks()[0].decision, "block");
});

test("optimize delivery: rewrite chunks reach the consumer before the rewrite completes", async () => {
  let release;
  const finished = new Promise((resolve) => {
    release = resolve;
  });
  let rewriteFinished = false;
  const llm = {
    calls: [],
    stream(options) {
      this.calls.push(options);
      return (async function* slowRewrite() {
        yield { type: "block-start", index: 0, blockType: "text" };
        yield { type: "text-delta", index: 0, text: "First streamed part" };
        await finished;
        yield { type: "text-delta", index: 0, text: " and the rest." };
        yield { type: "finish", reason: { kind: "stop" } };
        rewriteFinished = true;
      })();
    },
  };
  const harness = optimizeSetup({ runner: createFakeRunner(rawBlock()), llm });
  const handler = harness.ctx.handlersFor("llm/stream")[0];
  const original = finalAnswerChunks("A flawed original answer text.");
  const stream = handler({ sessionId: "s1" }, downstreamFrom(original));
  const iterator = stream[Symbol.asyncIterator]();

  // The gate holds chunks until the first visible rewrite delta, then streams.
  const first = await iterator.next();
  assert.equal(first.value.type, "block-start");
  const second = await iterator.next();
  assert.equal(second.value.type, "text-delta");
  assert.equal(second.value.text, "First streamed part");
  assert.equal(rewriteFinished, false, "consumer must receive chunks while the rewrite is still running");

  release();
  const rest = [];
  for (let next = await iterator.next(); !next.done; next = await iterator.next()) rest.push(next.value);
  assert.equal(rewriteFinished, true);
  const visible = ["First streamed part", ...rest.filter((c) => c.type === "text-delta").map((c) => c.text)].join("");
  assert.equal(visible, "First streamed part and the rest.");
});

test("optimize delivery: a rewrite that cannot start replays the original", async () => {
  const llm = {
    calls: [],
    stream() {
      throw new Error("NO_ADAPTER");
    },
  };
  const harness = optimizeSetup({ runner: createFakeRunner(rawBlock()), llm });
  const original = finalAnswerChunks("A flawed original answer text.");
  const out = await runStreamSettled(harness.ctx, { sessionId: "s1" }, original);
  assert.equal(out.length, original.length);
  for (let index = 0; index < original.length; index += 1) assert.equal(out[index], original[index]);
  const errors = harness.events("crisp_optimize_error");
  assert.equal(errors.length, 1);
  assert.equal(errors[0].phase, "start");
  assert.match(errors[0].error, /NO_ADAPTER/);
  assert.equal(harness.agent.followups.length, 0);
});

test("optimize delivery: a rewrite with no visible text replays the original", async () => {
  const llm = createFakeLlm([
    { type: "block-start", index: 0, blockType: "text" },
    { type: "finish", reason: { kind: "stop" } },
  ]);
  const harness = optimizeSetup({ runner: createFakeRunner(rawBlock()), llm });
  const original = finalAnswerChunks("A flawed original answer text.");
  const out = await runStreamSettled(harness.ctx, { sessionId: "s1" }, original);
  assert.equal(out.length, original.length);
  for (let index = 0; index < original.length; index += 1) assert.equal(out[index], original[index]);
  assert.equal(harness.events("crisp_optimize_error")[0].phase, "empty_rewrite");
});

test("optimize delivery: mid-rewrite failure terminates with an error finish, never the original", async () => {
  const llm = {
    calls: [],
    stream(options) {
      this.calls.push(options);
      return (async function* brokenRewrite() {
        yield { type: "block-start", index: 0, blockType: "text" };
        yield { type: "text-delta", index: 0, text: "Partial rewrite" };
        throw new Error("stream broke");
      })();
    },
  };
  const harness = optimizeSetup({ runner: createFakeRunner(rawBlock()), llm });
  const original = finalAnswerChunks("A flawed original answer text.");
  const out = await runStreamSettled(harness.ctx, { sessionId: "s1" }, original);
  const visible = out.filter((chunk) => chunk.type === "text-delta").map((chunk) => chunk.text).join("");
  assert.equal(visible, "Partial rewrite");
  const finish = out.filter((chunk) => chunk.type === "finish");
  assert.equal(finish.length, 1);
  assert.equal(finish[0].reason.kind, "error");
  assert.equal(finish[0].reason.failure.code, "CRISP_OPTIMIZE_ERROR");
  for (const chunk of original) assert.ok(!out.includes(chunk), "original must not be appended after a partial rewrite");
  assert.equal(harness.events("crisp_optimize_error")[0].phase, "mid_stream");
});

test("optimize delivery: tool-call steps replay without review or rewrite", async () => {
  const llm = createFakeLlm(rewriteChunks("should not be used"));
  const harness = optimizeSetup({ runner: createFakeRunner(rawBlock()), llm });
  const chunks = [
    ...textChunks("Let me check the file."),
    { type: "tool-call-delta", index: 1, id: "call-1", argumentsDelta: "{}" },
    { type: "finish", reason: { kind: "stop" } },
  ];
  const out = await runStreamSettled(harness.ctx, { sessionId: "s1" }, chunks);
  assert.equal(out.length, chunks.length);
  for (let index = 0; index < chunks.length; index += 1) assert.equal(out[index], chunks[index]);
  assert.equal(llm.calls.length, 0);
  assert.equal(harness.checks().length, 0);
  assert.equal(harness.events("crisp_optimize_started").length, 0);
});

test("optimize delivery: missing optimizer route replays the original and warns", async () => {
  const llm = createFakeLlm(rewriteChunks("should not be used"));
  const harness = setup({
    mode: "guard",
    runner: createFakeRunner(rawBlock()),
    llm,
    config: { delivery: "optimize" },
  });
  const original = finalAnswerChunks("A flawed original answer text.");
  const out = await runStreamSettled(harness.ctx, { sessionId: "s1" }, original);
  assert.equal(out.length, original.length);
  for (let index = 0; index < original.length; index += 1) assert.equal(out[index], original[index]);
  assert.equal(llm.calls.length, 0);
  assert.equal(harness.agent.followups.length, 0);
  const activation = harness.activation()[0];
  assert.equal(activation.delivery, "optimize");
  assert.equal(activation.optimize_available, false);
  assert.ok(activation.warnings.some((warning) => warning.includes("needs optimizerProvider and optimizerModel")));
});

test("optimize delivery is ignored outside guard mode", async () => {
  const llm = createFakeLlm(rewriteChunks("should not be used"));
  const harness = setup({
    runner: createFakeRunner(rawBlock()),
    llm,
    config: { delivery: "optimize" },
  });
  const chunks = finalAnswerChunks("An observe-mode answer.");
  const out = await runStreamSettled(harness.ctx, { sessionId: "s1" }, chunks);
  assert.equal(out.length, chunks.length);
  for (let index = 0; index < chunks.length; index += 1) assert.equal(out[index], chunks[index]);
  assert.equal(llm.calls.length, 0);
  assert.equal(harness.events("crisp_optimize_started").length, 0);
  assert.ok(
    harness.activation()[0].warnings.some((warning) => warning.includes('only applies with mode "guard"')),
  );
});