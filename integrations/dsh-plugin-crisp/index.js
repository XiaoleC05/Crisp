// Crisp quality gate — Host-only DeepSeek Harness (Cordis) plugin bundle.
//
// The gate listens on the `llm/stream` waterfall of the mounted
// @deepseek-ai/dsh-llm service, passes every model chunk through unchanged
// while accumulating the visible assistant text, and after the terminal
// `finish` chunk runs the complete answer through `crisp check-text`. Each
// decision is logged as one structured JSON line. In `guard` mode a material
// block detected for a completed turn queues exactly one user-role followup
// asking for the fixed full rewrite; the default `observe` mode never
// influences the conversation.
//
// The plugin declares no required services: it only listens to events, and
// resolves ctx.get("agents") lazily when guard mode needs agent.followup().
import { createGate } from "./lib/gate.js";

/** Cordis plugin display name used by Loader diagnostics. */
export const name = "crisp-quality-gate";

/**
 * Host plugin entry.
 *
 * @param {object} ctx Cordis plugin context
 * @param {object} config Loader row config (cordis.patch.yml)
 * @returns {object} the gate handle (its disposer is owned by ctx.effect)
 */
export function apply(ctx, config) {
  return applyWithDeps(ctx, config, {});
}

/**
 * @internal Test/embedding seam: same as apply with injectable IO.
 * Not part of the supported host-plugin surface; `apply` is the entry point
 * the Loader calls.
 *
 * @param {object} ctx Cordis plugin context
 * @param {object} config Loader row config
 * @param {object} deps {env, platform, logLine, runner, schedule, isAgentLoopRequest, getLlm, background}
 * @returns {object} the gate handle
 */
export function applyWithDeps(ctx, config, deps) {
  // Resolve the Harness LLM service lazily: the plugin still mounts in
  // profiles without one, and the external-http critic never touches it.
  const getLlm =
    typeof deps.getLlm === "function"
      ? deps.getLlm
      : () => {
          try {
            return ctx.get?.("llm") ?? ctx.llm;
          } catch {
            return undefined;
          }
        };
  const gate = createGate(ctx, config, { ...deps, getLlm });
  if (typeof ctx?.effect === "function") {
    ctx.effect(() => () => gate.dispose(), "crisp-quality-gate.lifecycle()");
  }
  ctx.on("llm/stream", (options, next) => gate.wrapStream(options, next));
  ctx.on("session/event", (session, event) => gate.onSessionEvent(session, event));
  ctx.on("session/disposed", (session) => gate.forgetSession(session));
  gate.announce();
  return gate;
}