// The quality-gate core: llm/stream interception, answer accumulation, Crisp
// invocation, structured logging, and the opt-in guard-mode rewrite request.
//
// Interception design (see docs/integrations/deepseek-harness.md):
//   * `llm/stream` is a Cordis waterfall. The handler must return the exact
//     chunk stream downstream produced. We wrap it in an async generator that
//     yields every chunk object unchanged (object identity preserved) so token
//     streaming is untouched, and only after the terminal `finish` chunk has
//     been consumed do we run the complete answer through `crisp check-text`.
//   * The check therefore never delays first-token latency, but it does delay
//     stream completion (and thus the durable assistant/message commit) by the
//     bounded check latency. That trade-off is documented.
//   * Guard mode requests one rewrite with the documented `agent.followup()`
//     API. The trigger is the terminal chunk, not the durable turn/end event:
//     measurement against the real 0.2.1-rc.5 headless runner showed that a
//     followup queued from a turn/end observer loses a race with the one-shot
//     runner's shutdown, while a followup queued while the agent is still
//     inside the step is picked up as the next turn (the loop keeps running
//     when its inbox has pending input). Only turn/start and turn/end are read
//     from the durable log, for turn identity and rewrite-turn suppression.
import { randomUUID } from "node:crypto";
import { existsSync } from "node:fs";
import path from "node:path";

import { DEFAULT_REWRITE_INSTRUCTION, MODE_GUARD, MODE_OBSERVE, resolveConfig } from "./config.js";
import { checkAnswer, spawnCheckTextRunner } from "./crisp.js";
import { createLogger } from "./log.js";

/** How many turns a session's rewrite memory keeps before dropping the oldest. */
const REMEMBERED_TURNS = 64;

/** Source kind on the durable rewrite message the guard appends. */
export const REWRITE_SOURCE = "crisp-quality-gate";

/**
 * Incremental accumulator for the visible assistant text and the protocol
 * facts the gate decides on. Text and reasoning are separate channels:
 * reasoning deltas are never part of the delivered answer.
 *
 * @returns accumulator
 */
export function createAccumulator() {
  /** @type {Map<number, {parts: string[], fallbackText: string|undefined}>} */
  const blocks = new Map();
  let hasToolCall = false;
  let finishKind;

  const slotFor = (index) => {
    const key = Number.isInteger(index) && index >= 0 ? index : 0;
    let slot = blocks.get(key);
    if (slot === undefined) {
      slot = { parts: [], fallbackText: undefined };
      blocks.set(key, slot);
    }
    return slot;
  };

  return {
    push(chunk) {
      if (!chunk || typeof chunk !== "object") return;
      switch (chunk.type) {
        case "text-delta": {
          if (typeof chunk.text === "string") slotFor(chunk.index).parts.push(chunk.text);
          return;
        }
        case "tool-call-delta":
          hasToolCall = true;
          return;
        case "block-start":
          if (chunk.blockType === "tool-call") hasToolCall = true;
          return;
        case "block-end": {
          const block = chunk.block;
          if (!block || typeof block !== "object") return;
          if (block.type === "tool-call") {
            hasToolCall = true;
            return;
          }
          if (block.type === "text" && typeof block.text === "string") {
            const slot = slotFor(chunk.index);
            if (slot.parts.length === 0) slot.fallbackText = block.text;
          }
          return;
        }
        case "finish": {
          const reason = chunk.reason;
          finishKind = reason && typeof reason.kind === "string" ? reason.kind : "missing";
          return;
        }
        default:
          return;
      }
    },
    text() {
      return [...blocks.keys()]
        .sort((left, right) => left - right)
        .map((index) => {
          const slot = blocks.get(index);
          return slot.parts.length > 0 ? slot.parts.join("") : slot.fallbackText ?? "";
        })
        .join("");
    },
    hasToolCall() {
      return hasToolCall;
    },
    finishKind() {
      return finishKind;
    },
  };
}

/** Count Unicode code points, matching the Go core's rune accounting. */
export function countRunes(text) {
  return typeof text === "string" ? Array.from(text).length : 0;
}

/**
 * Cheap pre-filter for requests an agent loop built.
 *
 * The loop's GenerateOptions never carry a `purpose`; auxiliary model calls
 * (session titles, compaction summaries) set one. That field alone separates
 * the two families without importing installation packages. The precise
 * @deepseek-ai/dsh-llm marker is still consulted when it can be loaded.
 */
function looksLikeAgentRequest(options) {
  return Boolean(
    options &&
      typeof options === "object" &&
      typeof options.sessionId === "string" &&
      options.sessionId.trim() !== "" &&
      options.purpose === undefined,
  );
}

function messageOf(error) {
  return error instanceof Error ? error.message : String(error);
}

function toTurn(value) {
  return Number.isInteger(value) && value >= 0 ? value : null;
}

/**
 * Build the durable user-role message that asks for one complete rewrite.
 * DSH messages are plain JSON values; a user-role message with a plugin-owned
 * source kind is the documented shape for plugin input.
 */
export function makeRewriteMessage(instruction = DEFAULT_REWRITE_INSTRUCTION) {
  return {
    id: `crisp-rewrite-${randomUUID()}`,
    role: "user",
    content: [{ type: "text", text: instruction }],
    source: { kind: REWRITE_SOURCE },
  };
}

/**
 * Create the gate around one plugin context.
 *
 * @param {object} ctx Cordis plugin context (only on/effect/get are used)
 * @param {unknown} rawConfig Loader row config
 * @param {object} [deps] injectable seams (tests / advanced embedding)
 * @param {NodeJS.ProcessEnv} [deps.env]
 * @param {string} [deps.platform]
 * @param {(record:object)=>void} [deps.logLine]
 * @param {Function} [deps.runner] child runner, see lib/crisp.js
 * @param {(options:object)=>Promise<boolean>|boolean} [deps.isAgentLoopRequest]
 * @returns {object} gate with wrapStream/onSessionEvent/announce/dispose
 */
export function createGate(ctx, rawConfig, deps = {}) {
  const baseEnv = deps.env ?? process.env;
  const config = resolveConfig(rawConfig, baseEnv, deps.platform ?? process.platform);
  const log = createLogger(config, deps);
  const runner = typeof deps.runner === "function" ? deps.runner : spawnCheckTextRunner;
  const explicitDetector =
    typeof deps.isAgentLoopRequest === "function" ? deps.isAgentLoopRequest : undefined;

  /** @type {Map<string, object>} per-session turn/guard state */
  const states = new Map();
  let disposed = false;
  let loopMarker; // undefined = not loaded, null = unavailable
  let loopMarkerLoading;

  function stateFor(sessionId) {
    let state = states.get(sessionId);
    if (state === undefined) {
      state = {
        sessionId,
        currentTurn: null,
        rewrittenTurns: new Set(),
        suppressNextTurn: false,
        suppressTurn: null,
      };
      states.set(sessionId, state);
    }
    return state;
  }

  /**
   * Best-effort detection of agent-loop built requests. @deepseek-ai/dsh-llm
   * is carried by the installation (the bundle declares no dependency), so the
   * marker function is loaded lazily and its absence falls back to the
   * purpose/sessionId shape check instead of disabling the gate.
   */
  async function isAgentLoopRequest(options) {
    if (!looksLikeAgentRequest(options)) return false;
    if (explicitDetector !== undefined) {
      try {
        return (await explicitDetector(options)) === true;
      } catch {
        return false;
      }
    }
    if (loopMarker === undefined) {
      loopMarkerLoading ??= import("@deepseek-ai/dsh-llm").then(
        (module) => {
          loopMarker = typeof module.isAgentLoopRequest === "function" ? module.isAgentLoopRequest : null;
        },
        () => {
          loopMarker = null;
        },
      );
      await loopMarkerLoading;
    }
    if (loopMarker !== null) {
      try {
        return loopMarker(options) === true;
      } catch {
        return false;
      }
    }
    return true;
  }

  function agentFor(sessionId) {
    try {
      const agents = typeof ctx?.get === "function" ? ctx.get("agents") : undefined;
      const agent = typeof agents?.get === "function" ? agents.get(sessionId) : undefined;
      return agent ?? undefined;
    } catch {
      return undefined;
    }
  }

  function logSkip(state, skipReason, extra = {}) {
    log({
      event: "crisp_quality_skip",
      mode: config.mode,
      session_id: state.sessionId,
      turn: state.currentTurn,
      skip_reason: skipReason,
      ...extra,
    });
  }

  function guardLog(state, event, extra = {}) {
    log({ event, mode: config.mode, session_id: state.sessionId, turn: state.currentTurn, ...extra });
  }

  /**
   * Ask for one rewrite, exactly once per turn, with the documented
   * agent.followup() API. A followup queued while the agent is still inside the
   * step becomes the next turn; the loop keeps running while its inbox has
   * pending input. The rewrite turn itself is suppressed so a bad rewrite
   * cannot loop.
   */
  function requestRewrite(state, pending) {
    const turn = state.currentTurn;
    if (turn === null) {
      guardLog(state, "crisp_guard_skipped", { skip_reason: "no_turn" });
      return;
    }
    if (state.suppressTurn === turn) {
      guardLog(state, "crisp_guard_skipped", { skip_reason: "rewrite_turn" });
      return;
    }
    if (state.rewrittenTurns.has(turn)) {
      guardLog(state, "crisp_guard_skipped", { skip_reason: "already_rewritten" });
      return;
    }
    const agent = agentFor(state.sessionId);
    if (agent === undefined) {
      guardLog(state, "crisp_guard_skipped", { skip_reason: "agent_unavailable" });
      return;
    }
    if (typeof agent.followup !== "function") {
      guardLog(state, "crisp_guard_skipped", { skip_reason: "followup_unavailable" });
      return;
    }

    state.rewrittenTurns.add(turn);
    while (state.rewrittenTurns.size > REMEMBERED_TURNS) {
      const oldest = state.rewrittenTurns.values().next().value;
      state.rewrittenTurns.delete(oldest);
    }
    state.suppressNextTurn = true;
    try {
      agent.followup(makeRewriteMessage(config.rewriteInstruction));
      guardLog(state, "crisp_guard_followup", {
        action: "followup",
        reason_type: pending.reasonType,
        answer_runes: pending.answerRunes,
        instruction_chars: countRunes(config.rewriteInstruction),
      });
    } catch (error) {
      state.suppressNextTurn = false;
      guardLog(state, "crisp_guard_error", { error: messageOf(error) });
    }
  }

  async function evaluate(state, acc) {
    // A step that called a tool is not a delivered answer; silently skip it.
    if (acc.hasToolCall()) return;
    const finishKind = acc.finishKind();
    if (finishKind !== "stop") {
      logSkip(state, finishKind === undefined || finishKind === null ? "missing_finish" : `finish_${finishKind}`, {
        finish: finishKind ?? null,
      });
      return;
    }
    const text = acc.text();
    const trimmed = text.trim();
    const runes = countRunes(trimmed);
    if (trimmed === "") {
      logSkip(state, "empty_answer");
      return;
    }
    if (runes < config.minRunes) {
      logSkip(state, "short_answer", { answer_runes: runes, min_runes: config.minRunes });
      return;
    }
    if (runes > config.maxRunes) {
      logSkip(state, "answer_too_large", { answer_runes: runes, max_runes: config.maxRunes });
      return;
    }

    const started = Date.now();
    const result = await checkAnswer({ text, config, runner, env: baseEnv });
    const checkLatencyMs = Date.now() - started;
    log({
      event: "crisp_quality_check",
      mode: config.mode,
      session_id: state.sessionId,
      turn: state.currentTurn,
      decision: result.decision,
      reason_type: result.reasonType || null,
      rule_types: result.ruleTypes,
      critic_called: result.criticCalled,
      fail_open: result.failOpen,
      answer_runes: runes,
      natural_runes: result.naturalRunes ?? null,
      critic_latency_ms: result.criticLatencyMs ?? null,
      critic_tokens: result.criticTokens ?? null,
      check_latency_ms: checkLatencyMs,
      crisp_elapsed_ms: result.crispElapsedMs ?? null,
      exit_code: result.exitCode,
      timed_out: result.timedOut,
      binary: config.binaryPath,
      ...(result.error === undefined ? {} : { error: result.error }),
    });

    if (config.mode !== MODE_GUARD || result.failOpen || result.decision !== "block") return;
    requestRewrite(state, { answerRunes: runes, reasonType: result.reasonType || null });
  }

  /** Handle one durable session event; only turn boundaries matter here. */
  function onSessionEvent(session, event) {
    if (disposed) return;
    const sessionId = session?.id;
    if (typeof sessionId !== "string" || sessionId === "") return;
    if (!event || typeof event !== "object") return;
    if (event.type === "turn/start") {
      const state = stateFor(sessionId);
      state.currentTurn = toTurn(event.data?.turn);
      if (state.suppressNextTurn) {
        state.suppressNextTurn = false;
        state.suppressTurn = state.currentTurn;
      }
      return;
    }
    if (event.type === "turn/end") {
      const state = stateFor(sessionId);
      const turn = toTurn(event.data?.turn);
      if (state.currentTurn === turn) state.currentTurn = null;
      if (state.suppressTurn === turn) state.suppressTurn = null;
    }
  }

  /** Forget a disposed session's guard state. */
  function forgetSession(session) {
    if (typeof session?.id === "string") states.delete(session.id);
  }

  /**
   * Wrap one downstream llm/stream chunk stream.
   *
   * @param {object} options GenerateOptions seen at the waterfall
   * @param {() => AsyncIterable<object>} next downstream chain
   * @returns {AsyncIterable<object>} the downstream stream, unchanged chunks
   */
  function wrapStream(options, next) {
    if (disposed || !config.enabled) return next();
    if (typeof next !== "function") return next;
    if (!looksLikeAgentRequest(options)) return next();
    const sessionId = String(options.sessionId);
    const state = stateFor(sessionId);

    return (async function* wrapped() {
      const acc = createAccumulator();
      let completed = false;
      try {
        for await (const chunk of next()) {
          acc.push(chunk);
          yield chunk;
        }
        completed = true;
      } finally {
        if (completed && !disposed) {
          try {
            if (await isAgentLoopRequest(options)) await evaluate(state, acc);
          } catch (error) {
            log({
              event: "crisp_quality_error",
              mode: config.mode,
              session_id: sessionId,
              phase: "after_stream",
              error: messageOf(error),
            });
          }
        }
      }
    })();
  }

  /** Log one activation line so deployments can confirm the gate is mounted. */
  function announce() {
    let binaryExists = null;
    try {
      if (path.isAbsolute(config.binaryPath)) binaryExists = existsSync(config.binaryPath);
    } catch {
      binaryExists = null;
    }
    log({
      event: "crisp_quality_gate_active",
      plugin_version: config.pluginVersion,
      mode: config.mode,
      enabled: config.enabled,
      binary: config.binaryPath,
      binary_exists: binaryExists,
      timeout_ms: config.timeoutMs,
      min_runes: config.minRunes,
      max_runes: config.maxRunes,
      log_file: typeof config.logFile === "string" && config.logFile !== "" ? config.logFile : "stderr",
      warnings: config.warnings,
    });
  }

  /** Stop accepting checks and drop per-session state. */
  function dispose() {
    disposed = true;
    states.clear();
  }

  return {
    config,
    wrapStream,
    onSessionEvent,
    forgetSession,
    announce,
    dispose,
    /** @internal exposed for diagnostics/tests */
    _states: states,
  };
}

export { MODE_GUARD, MODE_OBSERVE };
