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
import { createStatusHandler, createStatusService } from "./lib/status.js";

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
  registerStatusRoute(ctx, config, deps);
  gate.announce();
  return gate;
}

/**
 * Resolve the composition's web server lazily. Profiles without the Web UI do
 * not mount one; the gate itself must keep working there.
 *
 * @param {object} ctx Cordis plugin context
 * @param {object} deps injectable seams
 * @returns {object|undefined} the webServer service
 */
function getWebServer(ctx, deps) {
  if (typeof deps.getWebServer === "function") {
    try {
      return deps.getWebServer();
    } catch {
      return undefined;
    }
  }
  try {
    return ctx.get?.("webServer") ?? ctx.webServer;
  } catch {
    return undefined;
  }
}

/**
 * Serve the read-only status snapshot used by the Web client's left-sidebar
 * page. Registration is best-effort: a missing web server (non-Web profiles)
 * or a route collision must never break the quality gate.
 *
 * @param {object} ctx Cordis plugin context
 * @param {object} config Loader row config
 * @param {object} deps injectable seams
 */
function registerStatusRoute(ctx, config, deps) {
  /**
   * Register the route on one concrete webServer service. Returns a disposer,
   * or undefined when the service is absent and the plugin should stay quiet.
   *
   * @param {object|undefined} webServer the composition's web server service
   * @returns {(() => void)|undefined} route disposer
   */
  const registerOn = (webServer) => {
    if (webServer === undefined || webServer === null || typeof webServer.register !== "function") {
      return undefined;
    }
    try {
      const service = createStatusService(ctx, config, deps);
      const handler = createStatusHandler(service, ctx);
      const dispose = webServer.register({ kind: "exact", path: service.route, handler });
      return () => {
        try {
          dispose?.();
        } catch {
          // The web server may already be tearing down.
        }
      };
    } catch (error) {
      try {
        ctx.logger?.warn?.(error);
      } catch {
        // Diagnostics are best-effort; the gate still mounts.
      }
      return undefined;
    }
  };

  // A test/embedding seam can pin the service. Otherwise use Cordis optional
  // service injection: the Web server may not exist yet when this row applies
  // (or may be replaced later), so a one-shot ctx.get() at activation can
  // silently leave the route unregistered and make the sidebar page show 404.
  if (typeof deps.getWebServer === "function") {
    const webServer = getWebServer(ctx, deps);
    const dispose = registerOn(webServer);
    if (typeof dispose === "function" && typeof ctx.effect === "function") {
      ctx.effect(() => dispose, "crisp-quality-gate.status-route()");
    } else if (typeof dispose === "function") {
      // No effect scope (test doubles): leave the route registered.
    }
    return;
  }

  if (typeof ctx.inject === "function") {
    try {
      ctx.inject(["webServer"], (childCtx) => {
        const webServer = childCtx?.webServer;
        const dispose = registerOn(webServer);
        if (typeof dispose === "function" && typeof childCtx?.effect === "function") {
          childCtx.effect(() => dispose, "crisp-quality-gate.status-route()");
        }
      });
      return;
    } catch (error) {
      try {
        ctx.logger?.warn?.(error);
      } catch {
        // Fall through to the one-shot path below.
      }
    }
  }

  // Older hosts without ctx.inject: keep the historical one-shot registration.
  const dispose = registerOn(getWebServer(ctx, deps));
  if (typeof dispose === "function" && typeof ctx.effect === "function") {
    ctx.effect(() => dispose, "crisp-quality-gate.status-route()");
  }
}