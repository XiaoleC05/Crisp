// Plugin configuration: defaults, option/env precedence, and the fixed rewrite
// instruction shared with the Go decision core (internal/policy/decision.go).
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

export const PLUGIN_DIR = fileURLToPath(new URL("..", import.meta.url));
export const REPO_ROOT = fileURLToPath(new URL("../../..", import.meta.url));

export const DEFAULT_TIMEOUT_MS = 30_000;
export const MIN_TIMEOUT_MS = 1_000;
export const MAX_TIMEOUT_MS = 120_000;
export const DEFAULT_MAX_CHECKED = 500;

// Keep byte-for-byte identical to policy.RewriteReason in
// internal/policy/decision.go: the rewrite request must never ask for a
// shorter answer, only for a non-redundant complete one.
export const DEFAULT_REWRITE_INSTRUCTION =
  "请重新输出完整最终回答：合并语义重复的段落，删除无效铺垫和结论重复，保留全部必要事实、数字、代码和解释，不要为了缩短篇幅而删除必要信息。";

const PLUGIN_SERVICE = "crisp";

function asNonEmptyString(value) {
  return typeof value === "string" && value.trim() !== "" ? value.trim() : undefined;
}

function asBoolean(value, fallback) {
  return typeof value === "boolean" ? value : fallback;
}

function isTruthyEnv(value) {
  if (value === undefined || value === null) return false;
  const text = String(value).trim().toLowerCase();
  return text === "1" || text === "true" || text === "yes";
}

function clampTimeout(value) {
  if (!Number.isFinite(value)) return DEFAULT_TIMEOUT_MS;
  const n = Math.trunc(value);
  if (n < MIN_TIMEOUT_MS) return MIN_TIMEOUT_MS;
  if (n > MAX_TIMEOUT_MS) return MAX_TIMEOUT_MS;
  return n;
}

function parsePositiveInt(value, fallback) {
  const n = typeof value === "number" ? value : Number.parseInt(String(value ?? ""), 10);
  if (!Number.isFinite(n) || n <= 0) return fallback;
  return Math.trunc(n);
}

/** Absolute default of the Crisp binary inside this repository checkout. */
export function defaultCrispBin(platform = process.platform, repoRoot = REPO_ROOT) {
  const win = platform === "win32";
  const sep = win ? "\\" : "/";
  const root = String(repoRoot).replace(/[\\/]+$/, "");
  return root + sep + "bin" + sep + (win ? "crisp.exe" : "crisp");
}

/**
 * Resolve the effective plugin configuration.
 *
 * Precedence (highest first):
 *   tuple options in opencode.json  >  dedicated env vars  >  defaults
 * Generic QUALITY_GATE_* critic variables are NOT read here: the spawned
 * Crisp process inherits the opencode server environment, so they pass
 * through untouched.
 */
export function resolveConfig(options = {}, env = process.env, platform = process.platform) {
  const opts = options && typeof options === "object" ? options : {};
  const optionEnv = opts.env && typeof opts.env === "object" ? opts.env : {};
  const mergedEnv = { ...env, ...optionEnv };

  const crispBin =
    asNonEmptyString(optionEnv.CRISP_BIN) ??
    asNonEmptyString(opts.crispBin) ??
    asNonEmptyString(env.CRISP_BIN) ??
    defaultCrispBin(platform);

  const rawTimeout =
    opts.timeoutMs ?? mergedEnv.CRISP_TIMEOUT_MS ?? env.CRISP_TIMEOUT_MS ?? DEFAULT_TIMEOUT_MS;

  const mode = asNonEmptyString(optionEnv.QUALITY_GATE_MODE) ?? asNonEmptyString(opts.mode);
  const rewriteInstruction =
    asNonEmptyString(opts.rewriteInstruction) ?? DEFAULT_REWRITE_INSTRUCTION;

  const logFile =
    opts.logFile === false
      ? false
      : asNonEmptyString(opts.logFile) ??
        asNonEmptyString(mergedEnv.CRISP_OPENCODE_LOG_FILE) ??
        path.join(os.tmpdir(), "opencode-crisp", "quality-gate.jsonl");

  return {
    service: PLUGIN_SERVICE,
    crispBin,
    timeoutMs: clampTimeout(Number(rawTimeout)),
    mode,
    // Env for the child process: inherited environment + tuple 'env' overrides,
    // plus convenience mapping of "mode" to the variable Crisp already knows.
    env: mode ? { ...optionEnv, QUALITY_GATE_MODE: mode } : { ...optionEnv },
    rewrite: asBoolean(opts.rewrite, true),
    rewriteInstruction,
    toast: asBoolean(opts.toast, true),
    appLog: asBoolean(opts.appLog, true),
    logFile,
    includeSubagents: asBoolean(opts.includeSubagents, false),
    maxChecked: parsePositiveInt(opts.maxChecked, DEFAULT_MAX_CHECKED),
    enabled: !isTruthyEnv(mergedEnv.CRISP_OPENCODE_DISABLE),
  };
}

/** Environment passed to the Crisp child process (includes QUALITY_GATE_*). */
export function buildCheckEnv(config, baseEnv = process.env) {
  const out = { ...baseEnv, ...(config.env ?? {}) };
  for (const key of Object.keys(out)) {
    if (out[key] === undefined) delete out[key];
  }
  return out;
}
