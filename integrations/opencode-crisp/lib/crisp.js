// Bounded, fail-open invocation of the Crisp binary in 'check-text' mode.
// Crisp reads raw answer text on stdin and writes one JSON decision object to
// stdout; any failure on our side MUST become a fail-open pass, mirroring the
// Go implementation's guarantee that a broken gate never blocks an answer.
import { spawn as nodeSpawn } from "node:child_process";

export const MAX_STDOUT_BYTES = 1 << 20; // 1 MiB: decisions are tiny
export const MAX_STDERR_BYTES = 1 << 20;

/** Parse the JSON summary printed by 'crisp check-text'. Throws on bad input. */
export function parseDecision(stdout) {
  const text = typeof stdout === "string" ? stdout.trim() : "";
  if (text === "") throw new Error("crisp produced no stdout");
  const parsed = JSON.parse(text);
  if (!parsed || typeof parsed !== "object") throw new Error("crisp stdout is not an object");
  if (parsed.decision !== "pass" && parsed.decision !== "block") {
    throw new Error("unexpected crisp decision: " + String(parsed.decision));
  }
  return {
    decision: parsed.decision,
    reasonType: typeof parsed.reason_type === "string" ? parsed.reason_type : "",
    ruleTypes: Array.isArray(parsed.rule_types) ? parsed.rule_types : [],
    criticCalled: parsed.critic_called === true,
    failOpen: parsed.fail_open === true,
    answerRunes: typeof parsed.answer_runes === "number" ? parsed.answer_runes : undefined,
    naturalRunes: typeof parsed.natural_runes === "number" ? parsed.natural_runes : undefined,
    criticLatencyMs:
      typeof parsed.critic_latency_ms === "number" ? parsed.critic_latency_ms : undefined,
    criticTokens: typeof parsed.critic_tokens === "number" ? parsed.critic_tokens : undefined,
    elapsedMs: typeof parsed.elapsed_ms === "number" ? parsed.elapsed_ms : undefined,
    raw: parsed,
  };
}

function failOpenResult(reason, extra = {}) {
  return {
    ok: false,
    decision: "pass",
    failOpen: true,
    timedOut: extra.timedOut === true,
    exitCode: extra.exitCode ?? null,
    error: reason,
    stderr: extra.stderr ?? "",
    elapsedMs: extra.elapsedMs,
    summary: undefined,
  };
}

/**
 * Run 'crisp check-text' with the answer text on stdin.
 *
 * Returns a decision object. Never throws: spawn errors, non-zero exits,
 * timeouts, huge/empty output and invalid JSON all become fail-open passes.
 *
 * @param {object} args
 * @param {string} args.binPath      absolute path to the crisp binary
 * @param {string} args.text         raw assistant answer
 * @param {object} [args.env]        child environment
 * @param {number} [args.timeoutMs]  hard bound for the whole child process
 * @param {Function} [args.spawn]    injectable child_process.spawn (tests)
 * @param {Function} [args.now]      injectable clock (tests)
 */
export function runCrispCheck({
  binPath,
  text,
  env = process.env,
  timeoutMs = 30_000,
  spawn = nodeSpawn,
  now = Date.now,
}) {
  const started = now();
  return new Promise((resolve) => {
    let child;
    try {
      child = spawn(binPath, ["check-text"], {
        stdio: ["pipe", "pipe", "pipe"],
        env,
        windowsHide: true,
      });
    } catch (err) {
      resolve(failOpenResult("spawn failed: " + errorMessage(err)));
      return;
    }

    let stdout = "";
    let stderr = "";
    let stdoutBytes = 0;
    let stderrBytes = 0;
    let settled = false;
    let timedOut = false;

    const finish = (result) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve(result);
    };

    const timer = setTimeout(() => {
      timedOut = true;
      try {
        child.kill();
      } catch {
        // ignore: the close/error handler below still settles the promise
      }
    }, Math.max(1, Number(timeoutMs) || 0));

    child.stdout?.on("data", (chunk) => {
      stdoutBytes += chunk.length;
      if (stdoutBytes <= MAX_STDOUT_BYTES) stdout += chunk.toString("utf8");
    });
    child.stderr?.on("data", (chunk) => {
      stderrBytes += chunk.length;
      if (stderrBytes <= MAX_STDERR_BYTES) stderr += chunk.toString("utf8");
    });

    child.on("error", (err) => {
      finish(
        failOpenResult("spawn error: " + errorMessage(err), {
          timedOut,
          stderr,
          elapsedMs: now() - started,
        }),
      );
    });

    child.on("close", (code) => {
      const elapsedMs = now() - started;
      if (timedOut) {
        finish(failOpenResult("timeout", { timedOut: true, exitCode: code, stderr, elapsedMs }));
        return;
      }
      if (code !== 0) {
        finish(
          failOpenResult("crisp exited with code " + String(code), { exitCode: code, stderr, elapsedMs }),
        );
        return;
      }
      try {
        const summary = parseDecision(stdout);
        finish({
          ok: true,
          decision: summary.decision,
          failOpen: summary.failOpen,
          timedOut: false,
          exitCode: 0,
          error: undefined,
          stderr,
          elapsedMs,
          summary,
        });
      } catch (err) {
        finish(
          failOpenResult("invalid crisp output: " + errorMessage(err), {
            exitCode: 0,
            stderr,
            elapsedMs,
          }),
        );
      }
    });

    try {
      child.stdin?.on("error", () => {
        // EPIPE when the child dies early: reported by error/close handlers.
      });
      child.stdin?.end(typeof text === "string" ? text : "", "utf8");
    } catch (err) {
      finish(failOpenResult("write stdin failed: " + errorMessage(err)));
    }
  });
}

function errorMessage(err) {
  if (err instanceof Error) return err.message;
  return String(err);
}
