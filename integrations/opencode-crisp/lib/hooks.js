// The opencode plugin hooks: observe completion, review with Crisp, report.
//
// Verified host contract (opencode 1.18.18, 'Plugin.state'/'Plugin.trigger'):
//   - the plugin factory receives { client, directory, worktree, ... };
//   - hooks.event({ event }) receives { id, type, properties } bus events and
//     is invoked WITHOUT being awaited by the event bus (fire-and-forget), so
//     this module keeps all work inside the returned promise and never throws.
//   - client is the @opencode-ai/sdk client: session.get, session.messages,
//     session.promptAsync, tui.showToast and app.log all exist on it.
import { buildCheckEnv, resolveConfig } from "./config.js";
import { eventSessionId, extractFinalAnswer } from "./events.js";
import { runCrispCheck } from "./crisp.js";
import { RewriteGuard } from "./rewrite-guard.js";
import { createJsonLineLogger } from "./log.js";

export const TOAST_TITLE = "Crisp quality gate";

/** Accept both the SDK RequestResult shape { data } and a bare payload. */
function unwrapResult(result) {
  if (result && typeof result === "object" && result.error) {
    const message =
      result.error && typeof result.error === "object" && result.error.message
        ? result.error.message
        : String(result.error);
    throw new Error(message);
  }
  if (result && typeof result === "object" && "data" in result) return result.data;
  return result;
}

export function buildToastMessage({ decision, reasonType, rewriteSent }) {
  const reason = reasonType || "quality_issue";
  if (decision === "block") {
    return rewriteSent
      ? `Crisp: 检测到实质质量问题（${reason}），已请求一次完整重写。`
      : `Crisp: 检测到实质质量问题（${reason}）。建议重新生成完整回答。`;
  }
  return `Crisp: 检查通过（${reason}）。`;
}

function errorMessage(err) {
  if (err instanceof Error) return err.message;
  return String(err);
}

function runeCount(text) {
  return typeof text === "string" ? Array.from(text).length : 0;
}

/**
 * Create the hooks object for one opencode server instance.
 *
 * @param {object} input   opencode PluginInput ({ client, directory, ... })
 * @param {object} options plugin tuple options from opencode.json
 * @param {object} deps    test seams: spawn, env, now, logger, fs
 */
export function createCrispHooks(input, options = {}, deps = {}) {
  const config = resolveConfig(options, deps.env ?? process.env);
  const client = input && input.client;
  if (!client || config.enabled === false) return {};

  const guard = new RewriteGuard({ maxChecked: config.maxChecked });
  const now = deps.now ?? Date.now;
  const logger = deps.logger ?? createJsonLineLogger({ client, config, fsImpl: deps.fs, now });
  const inFlight = new Map();

  async function safeLog(line) {
    try {
      await logger(line);
    } catch {
      // Never let logging break the hook.
    }
  }

  async function showToast(message) {
    if (config.toast === false || !client.tui || typeof client.tui.showToast !== "function") return false;
    try {
      await client.tui.showToast({
        body: { title: TOAST_TITLE, message, variant: "warning", duration: 8000 },
      });
      return true;
    } catch {
      // No TUI attached (headless 'opencode run'/'serve') or endpoint missing.
      return false;
    }
  }

  async function requestRewrite({ sessionID, answer }) {
    if (config.rewrite === false) return { requested: false, sent: false, skipped: "disabled" };
    if (answer.parentID && guard.isTurnRewritten(answer.parentID)) {
      return { requested: false, sent: false, skipped: "turn_already_rewritten" };
    }
    if (!client.session || typeof client.session.promptAsync !== "function") {
      return { requested: false, sent: false, skipped: "prompt_async_unavailable" };
    }

    guard.markRewriteSent({
      sessionID,
      blockedMessageID: answer.messageID,
      parentID: answer.parentID,
    });

    const body = { parts: [{ type: "text", text: config.rewriteInstruction }] };
    if (answer.providerID && answer.modelID) {
      body.model = { providerID: answer.providerID, modelID: answer.modelID };
    }
    if (answer.agent) body.agent = answer.agent;

    try {
      await client.session.promptAsync({ path: { id: sessionID }, body });
      return { requested: true, sent: true };
    } catch (err) {
      guard.cancelRewriteSent({ sessionID, parentID: answer.parentID });
      return { requested: true, sent: false, error: errorMessage(err) };
    }
  }

  function baseLogFields(sessionID, answer) {
    return {
      event: "quality_check",
      source: "opencode-crisp",
      session_id: sessionID,
      message_id: answer.messageID,
      parent_message_id: answer.parentID,
      agent: answer.agent,
    };
  }

  async function reviewSession(sessionID) {
    // 1. Skip child (subagent) sessions unless explicitly requested: rewriting
    //    a subagent transcript from here would be surprising.
    if (!config.includeSubagents && client.session && typeof client.session.get === "function") {
      try {
        const session = unwrapResult(await client.session.get({ path: { id: sessionID } }));
        if (session && session.parentID) {
          await safeLog({
            level: "debug",
            event: "quality_skip",
            source: "opencode-crisp",
            session_id: sessionID,
            skip_reason: "subagent_session",
          });
          return;
        }
      } catch (err) {
        // Missing/failed session lookup is not fatal: continue (fail-open).
      }
    }

    // 2. Fetch the transcript and pick the last visible assistant answer.
    let messages;
    try {
      messages = unwrapResult(await client.session.messages({ path: { id: sessionID } }));
    } catch (err) {
      await safeLog({
        level: "warn",
        event: "quality_gate_error",
        source: "opencode-crisp",
        session_id: sessionID,
        error: "session.messages failed: " + errorMessage(err),
      });
      return;
    }

    const answer = extractFinalAnswer(messages);
    if (!answer || !answer.messageID) return;

    // 3. Exactly-once bookkeeping (session.idle and message.updated can both fire).
    const skipReason = guard.classify({
      sessionID,
      messageID: answer.messageID,
      parentID: answer.parentID,
    });
    if (skipReason) {
      await safeLog({
        level: "debug",
        event: "quality_skip",
        source: "opencode-crisp",
        session_id: sessionID,
        message_id: answer.messageID,
        skip_reason: skipReason,
      });
      return;
    }

    // 4. Run the Go checker (bounded, fail-open).
    const result = await runCrispCheck({
      binPath: config.crispBin,
      text: answer.text,
      env: buildCheckEnv(config),
      timeoutMs: config.timeoutMs,
      spawn: deps.spawn,
      now,
    });

    const summary = result.summary;
    const line = {
      ...baseLogFields(sessionID, answer),
      level: result.decision === "block" ? "warn" : "info",
      decision: result.decision,
      reason_type: result.failOpen ? "fail_open" : summary ? summary.reasonType : "",
      rule_types: summary ? summary.ruleTypes : [],
      critic_called: summary ? summary.criticCalled : false,
      fail_open: result.failOpen === true,
      answer_runes: summary && typeof summary.answerRunes === "number" ? summary.answerRunes : runeCount(answer.text),
      natural_runes: summary ? summary.naturalRunes : undefined,
      timeout_ms: config.timeoutMs,
      timed_out: result.timedOut === true,
      exit_code: result.exitCode,
      elapsed_ms: result.elapsedMs,
      error: result.error,
      rewrite_requested: false,
      rewrite_sent: false,
    };

    if (result.decision === "block") {
      const rewritten = await requestRewrite({ sessionID, answer });
      Object.assign(line, {
        rewrite_requested: rewritten.requested === true,
        rewrite_sent: rewritten.sent === true,
      });
      if (rewritten.skipped) line.rewrite_skipped = rewritten.skipped;
      if (rewritten.error) line.rewrite_error = rewritten.error;
      const toast = buildToastMessage({
        decision: "block",
        reasonType: line.reason_type,
        rewriteSent: line.rewrite_sent,
      });
      const shown = await showToast(toast);
      if (shown) line.toast = true;
    }

    await safeLog(line);
  }

  function scheduleReview(sessionID) {
    const existing = inFlight.get(sessionID);
    if (existing) return existing;
    const pending = reviewSession(sessionID)
      .catch(async (err) => {
        await safeLog({
          level: "error",
          event: "quality_gate_error",
          source: "opencode-crisp",
          session_id: sessionID,
          error: errorMessage(err),
        });
      })
      .finally(() => inFlight.delete(sessionID));
    inFlight.set(sessionID, pending);
    return pending;
  }

  return {
    event: async ({ event } = {}) => {
      try {
        const sessionID = eventSessionId(event);
        if (!sessionID) return;
        await scheduleReview(sessionID);
      } catch (err) {
        await safeLog({
          level: "error",
          event: "quality_gate_error",
          source: "opencode-crisp",
          error: "event hook failed: " + errorMessage(err),
        });
      }
    },
  };
}
