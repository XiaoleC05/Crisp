// Shared hermetic test helpers: fake Cordis context, fake agents, fake child
// runner, fake child process, and small stream/event utilities. No real Crisp
// binary, no network, no Harness installation is required.
import assert from "node:assert/strict";
import { EventEmitter } from "node:events";

/** Create a minimal Cordis-like plugin context. */
export function createFakeCtx({ agents, llm } = {}) {
  const handlers = new Map();
  const effects = [];
  return {
    handlers,
    effects,
    on(name, listener) {
      if (!handlers.has(name)) handlers.set(name, []);
      handlers.get(name).push(listener);
      return () => {};
    },
    emit(name, ...args) {
      for (const listener of handlers.get(name) ?? []) listener(...args);
    },
    effect(fn) {
      const disposer = fn();
      effects.push(disposer);
      return disposer;
    },
    get(name) {
      if (name === "agents") return agents;
      if (name === "llm") return llm;
      return undefined;
    },
    handlersFor(name) {
      return handlers.get(name) ?? [];
    },
  };
}

/** A fake ctx.agents service plus the agent map behind it. */
export function createFakeAgents() {
  const byId = new Map();
  return {
    byId,
    service: {
      get(id) {
        return byId.get(id);
      },
    },
    add(agent) {
      byId.set(agent.id, agent);
      return agent;
    },
  };
}

/** A fake Agent exposing followup() with recorded calls. */
export function createFakeAgent(id, { throwOnFollowup = false } = {}) {
  return {
    id,
    followups: [],
    followup(message) {
      if (throwOnFollowup) throw new Error("followup refused");
      this.followups.push(message);
    },
  };
}

/**
 * A scripted child runner. `script` is either a raw result, a function
 * returning a raw result, or an array of either, consumed per call.
 */
export function createFakeRunner(script = {}) {
  const calls = [];
  let index = 0;
  async function runner(request) {
    calls.push(request);
    const entry =
      typeof script === "function"
        ? script(request, calls.length - 1)
        : Array.isArray(script)
          ? script[Math.min(index++, script.length - 1)]
          : script;
    return typeof entry === "function" ? entry(request) : entry;
  }
  runner.calls = calls;
  return runner;
}

/** Raw runner result for a passing check. */
export function rawPass(overrides = {}) {
  return {
    stdout: JSON.stringify({
      decision: "pass",
      reason_type: "critic_pass",
      rule_types: [],
      critic_called: true,
      fail_open: false,
      answer_runes: 120,
      natural_runes: 110,
      critic_latency_ms: 12,
      critic_tokens: 34,
      elapsed_ms: 60,
      ...overrides,
    }),
    stderr: "",
    exitCode: 0,
    timedOut: false,
  };
}

/** Raw runner result for a material blocking decision. */
export function rawBlock(overrides = {}) {
  return {
    stdout: JSON.stringify({
      decision: "block",
      reason_type: "semantic_duplicate",
      rule_types: ["duplicate_paragraph"],
      critic_called: true,
      fail_open: false,
      answer_runes: 400,
      natural_runes: 380,
      critic_latency_ms: 30,
      critic_tokens: 90,
      elapsed_ms: 120,
      ...overrides,
    }),
    stderr: "",
    exitCode: 0,
    timedOut: false,
  };
}

/** Raw runner result that must be treated as fail-open. */
export function rawFailure(overrides = {}) {
  return { stdout: "", stderr: "", exitCode: 0, timedOut: false, ...overrides };
}

/** Raw rule-only crisp result: fail_open (no HTTP critic) but rule_types present. */
export function rawRules(overrides = {}) {
  return {
    stdout: JSON.stringify({
      decision: "pass",
      reason_type: "not_reviewed",
      rule_types: ["duplicate_sentence", "filler_opening"],
      critic_called: false,
      fail_open: true,
      answer_runes: 66,
      natural_runes: 64,
      critic_latency_ms: 0,
      critic_tokens: 0,
      elapsed_ms: 2,
      ...overrides,
    }),
    stderr: "",
    exitCode: 0,
    timedOut: false,
  };
}

/**
 * A fake Harness LLM service: ctx.llm.stream(options) yields the scripted
 * chunks for each call and records the options it received.
 *
 * @param {object[] | Function} script chunk array, a function returning one,
 *   or an async iterable for hanging/streaming simulations
 */
export function createFakeLlm(script = []) {
  const calls = [];
  return {
    calls,
    stream(options) {
      calls.push(options);
      const entry = typeof script === "function" ? script(options, calls.length - 1) : script;
      if (entry && typeof entry[Symbol.asyncIterator] === "function") return entry;
      return (async function* streamChunks() {
        for (const chunk of entry ?? []) yield chunk;
      })();
    },
  };
}

/** Build one critic-model chunk stream that returns a JSON verdict. */
export function criticChunks(verdict, options = {}) {
  const payload = typeof verdict === "string" ? verdict : JSON.stringify(verdict);
  const chunks = [];
  if (options.usage) chunks.push({ type: "usage", usage: options.usage });
  chunks.push(...textChunks(payload));
  chunks.push({ type: "finish", reason: options.reason ?? { kind: "stop" } });
  return chunks;
}

/** Build text-delta chunks for one block. */
export function textChunks(text, { index = 0, size = 17 } = {}) {
  const chunks = [];
  for (let offset = 0; offset < text.length; offset += size) {
    chunks.push({ type: "text-delta", index, text: text.slice(offset, offset + size) });
  }
  return chunks;
}

/** Build a complete text-only assistant chunk stream ending in finish stop. */
export function finalAnswerChunks(text, { index = 0 } = {}) {
  return [...textChunks(text, { index }), { type: "finish", reason: { kind: "stop" } }];
}

/** A downstream stream factory over fixed chunks. */
export function downstreamFrom(chunks) {
  return function downstream() {
    return (async function* stream() {
      for (const chunk of chunks) yield chunk;
    })();
  };
}

/** Consume an async iterable to an array. */
export async function collect(stream) {
  const out = [];
  for await (const chunk of stream) out.push(chunk);
  return out;
}

/** Run one llm/stream request through the plugin's registered handler. */
export async function runStream(ctx, options, chunks) {
  const handler = ctx.handlersFor("llm/stream")[0];
  assert.equal(typeof handler, "function", "llm/stream handler must be registered");
  const stream = handler(options, downstreamFrom(chunks));
  return collect(stream);
}

/** Emit one durable session event to the plugin's session/event listeners. */
export function emitSessionEvent(ctx, sessionId, event) {
  ctx.emit("session/event", { id: sessionId }, event);
}

/** Emit a durable turn/start event. */
export function emitTurnStart(ctx, sessionId, turn) {
  emitSessionEvent(ctx, sessionId, { type: "turn/start", data: { turn } });
}

/** Emit a durable turn/end event. */
export function emitTurnEnd(ctx, sessionId, turn, reason = { kind: "completed" }) {
  emitSessionEvent(ctx, sessionId, { type: "turn/end", data: { turn, reason } });
}

/** Deterministic deferred-work scheduler for guard tests. */
export function createScheduler() {
  const queue = [];
  return {
    schedule(fn) {
      queue.push(fn);
    },
    flush() {
      const queued = queue.splice(0);
      for (const fn of queued) fn();
    },
    get pending() {
      return queue.length;
    },
  };
}

/**
 * Build a fake child_process.spawn factory. `behavior(child, args)` runs on a
 * setImmediate so the runner has attached its listeners first.
 */
export function createChildFactory(behavior) {
  const children = [];
  function spawn(binaryPath, args, options) {
    const child = new EventEmitter();
    child.binaryPath = binaryPath;
    child.args = args;
    child.options = options;
    child.stdinData = undefined;
    child.killed = false;
    child.stdin = new EventEmitter();
    child.stdin.end = (data) => {
      child.stdinData = data;
    };
    child.stdout = new EventEmitter();
    child.stderr = new EventEmitter();
    child.kill = () => {
      child.killed = true;
      setImmediate(() => child.emit("close", null));
    };
    children.push(child);
    setImmediate(() => behavior(child, args, options));
    return child;
  }
  return { spawn, children };
}

/** Wait one macrotask so queued setImmediate work (fake child) can run. */
export function tick() {
  return new Promise((resolve) => setTimeout(resolve, 5));
}