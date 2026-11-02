// Bounded, fail-open invocation of the Crisp binary in "check-text" mode.
//
// Crisp reads raw answer text on stdin and writes exactly one JSON decision
// object on stdout. Any failure here MUST become a fail-open pass: the Go core
// has the same guarantee, and a broken quality gate must never block a good
// answer.
import { spawn as nodeSpawn } from "node:child_process";

import { buildChildEnv } from "./config.js";

export const MAX_STDOUT_BYTES = 256 * 1024;
export const MAX_STDERR_BYTES = 64 * 1024;
export const CHILD_ARGV = Object.freeze(["check-text"]);

/**
 * Parse the JSON summary printed by "crisp check-text".
 *
 * The summary contract is defined by cmd/crisp/pipeline.go summaryFields().
 * Throws on anything that is not one unambiguous decision object, so the
 * caller can fail open.
 *
 * @param {string} stdout raw child stdout
 * @returns normalized decision summary
 */
export function parseDecision(stdout) {
  const text = typeof stdout === "string" ? stdout.trim() : "";
  if (text === "") throw new Error("crisp produced no stdout");
  let parsed;
  try {
    parsed = JSON.parse(text);
  } catch (error) {
    throw new Error(`crisp stdout is not JSON: ${messageOf(error)}`);
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("crisp stdout is not a JSON object");
  }
  if (parsed.decision !== "pass" && parsed.decision !== "block") {
    throw new Error(`unexpected crisp decision: ${JSON.stringify(parsed.decision)}`);
  }
  return {
    decision: parsed.decision,
    reasonType: typeof parsed.reason_type === "string" ? parsed.reason_type : "",
    ruleTypes: Array.isArray(parsed.rule_types)
      ? parsed.rule_types.filter((entry) => typeof entry === "string")
      : [],
    criticCalled: parsed.critic_called === true,
    failOpen: parsed.fail_open === true,
    answerRunes: finiteNumber(parsed.answer_runes),
    naturalRunes: finiteNumber(parsed.natural_runes),
    criticLatencyMs: finiteNumber(parsed.critic_latency_ms),
    criticTokens: finiteNumber(parsed.critic_tokens),
    crispElapsedMs: finiteNumber(parsed.elapsed_ms),
  };
}

function finiteNumber(value) {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

function messageOf(error) {
  return error instanceof Error ? error.message : String(error);
}

/**
 * Default child runner: spawn the binary, feed the answer on stdin, and
 * resolve one bounded observation of the child's lifetime. Never rejects.
 *
 * @param {object} args
 * @param {string} args.binaryPath
 * @param {string[]} [args.args]
 * @param {string} args.input
 * @param {NodeJS.ProcessEnv} [args.env]
 * @param {number} [args.timeoutMs]
 * @param {Function} [args.spawn] injectable child_process.spawn (tests)
 * @param {Function} [args.now] injectable clock (tests)
 * @returns {Promise<{stdout:string,stderr:string,exitCode:number|null,timedOut:boolean,errorMessage?:string,elapsedMs:number}>}
 */
export function spawnCheckTextRunner({
  binaryPath,
  args = CHILD_ARGV,
  input,
  env = buildChildEnv({}),
  timeoutMs = 30_000,
  spawn = nodeSpawn,
  now = Date.now,
}) {
  const started = now();
  return new Promise((resolve) => {
    let child;
    try {
      child = spawn(binaryPath, [...args], {
        stdio: ["pipe", "pipe", "pipe"],
        env,
        windowsHide: true,
      });
    } catch (error) {
      resolve({
        stdout: "",
        stderr: "",
        exitCode: null,
        timedOut: false,
        errorMessage: `spawn failed: ${messageOf(error)}`,
        elapsedMs: now() - started,
      });
      return;
    }

    let stdout = "";
    let stderr = "";
    let stdoutBytes = 0;
    let stderrBytes = 0;
    let settled = false;
    let timedOut = false;
    let timer;

    const finish = (result) => {
      if (settled) return;
      settled = true;
      if (timer !== undefined) clearTimeout(timer);
      resolve({ ...result, elapsedMs: now() - started });
    };

    timer = setTimeout(() => {
      timedOut = true;
      try {
        child.kill();
      } catch {
        // The close/error handlers below still settle the promise.
      }
    }, Math.max(1, Number(timeoutMs) || 1));

    child.stdout?.on("data", (chunk) => {
      stdoutBytes += chunk.length;
      if (stdoutBytes <= MAX_STDOUT_BYTES) stdout += chunk.toString("utf8");
    });
    child.stderr?.on("data", (chunk) => {
      stderrBytes += chunk.length;
      if (stderrBytes <= MAX_STDERR_BYTES) stderr += chunk.toString("utf8");
    });

    child.on("error", (error) => {
      finish({ stdout, stderr, exitCode: null, timedOut, errorMessage: messageOf(error) });
    });
    child.on("close", (code) => {
      if (timedOut) {
        finish({ stdout, stderr, exitCode: code ?? null, timedOut: true, errorMessage: "timeout" });
        return;
      }
      finish({ stdout, stderr, exitCode: typeof code === "number" ? code : null, timedOut: false });
    });

    try {
      child.stdin?.on("error", () => {
        // EPIPE when the child exits early; the close handler reports it.
      });
      child.stdin?.end(typeof input === "string" ? input : "", "utf8");
    } catch (error) {
      finish({ stdout, stderr, exitCode: null, timedOut, errorMessage: `write stdin failed: ${messageOf(error)}` });
    }
  });
}

function failOpen(error, extra = {}) {
  return {
    ok: false,
    decision: "pass",
    failOpen: true,
    reasonType: "",
    ruleTypes: [],
    criticCalled: false,
    answerRunes: undefined,
    naturalRunes: undefined,
    criticLatencyMs: undefined,
    criticTokens: undefined,
    crispElapsedMs: undefined,
    timedOut: extra.timedOut === true,
    exitCode: extra.exitCode ?? null,
    error,
  };
}

/**
 * Run one check-text check. Never throws.
 *
 * @param {object} args
 * @param {string} args.text complete visible assistant answer
 * @param {object} args.config normalized plugin config
 * @param {Function} [args.runner] injectable child runner
 * @param {NodeJS.ProcessEnv} [args.env] Harness environment to filter into the child
 * @returns {Promise<object>} checked decision or a fail-open pass
 */
export async function checkAnswer({ text, config, runner = spawnCheckTextRunner, env = process.env }) {
  let raw;
  try {
    raw = await runner({
      binaryPath: config.binaryPath,
      args: [...CHILD_ARGV],
      input: typeof text === "string" ? text : "",
      env: buildChildEnv(config, env),
      timeoutMs: config.timeoutMs,
    });
  } catch (error) {
    return failOpen(`runner threw: ${messageOf(error)}`);
  }
  if (!raw || typeof raw !== "object") return failOpen("runner returned no result");
  if (raw.errorMessage) {
    return failOpen(messageOf(raw.errorMessage), {
      timedOut: raw.timedOut === true,
      exitCode: raw.exitCode ?? null,
    });
  }
  if (raw.timedOut) return failOpen("timeout", { timedOut: true, exitCode: raw.exitCode ?? null });
  if (raw.exitCode !== 0) {
    return failOpen(`crisp exited with code ${String(raw.exitCode)}`, { exitCode: raw.exitCode ?? null });
  }
  let summary;
  try {
    summary = parseDecision(raw.stdout);
  } catch (error) {
    return failOpen(`invalid crisp output: ${messageOf(error)}`, { exitCode: 0 });
  }
  return {
    ok: true,
    decision: summary.decision,
    failOpen: summary.failOpen,
    reasonType: summary.reasonType,
    ruleTypes: summary.ruleTypes,
    criticCalled: summary.criticCalled,
    answerRunes: summary.answerRunes,
    naturalRunes: summary.naturalRunes,
    criticLatencyMs: summary.criticLatencyMs,
    criticTokens: summary.criticTokens,
    crispElapsedMs: summary.crispElapsedMs,
    timedOut: false,
    exitCode: 0,
    error: undefined,
  };
}