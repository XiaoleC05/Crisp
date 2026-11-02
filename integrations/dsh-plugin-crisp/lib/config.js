// Configuration resolution for the Crisp quality-gate host plugin.
//
// The plugin is a plain Host-only Cordis bundle: it exports
// apply(ctx, config) and declares NO services, so the Loader can mount it in
// any profile that has the agent loop. Every option is defensive: an unknown
// or malformed value falls back to a safe default instead of throwing, because
// a broken quality gate must never break an agent run.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

/** Keep in sync with package.json "version" (asserted by test/config.test.js). */
export const PLUGIN_VERSION = "0.1.0";

/** Repository root when this package runs from a checkout: integrations/dsh-plugin-crisp/lib/. */
export const REPO_ROOT = fileURLToPath(new URL("../../..", import.meta.url));

export const DEFAULT_TIMEOUT_MS = 30_000;
export const MIN_TIMEOUT_MS = 1_000;
export const MAX_TIMEOUT_MS = 120_000;

/** Answers shorter than this many visible code points are not checked at all. */
export const DEFAULT_MIN_RUNES = 40;
/** Answers longer than this are skipped instead of being piped to the child. */
export const DEFAULT_MAX_RUNES = 200_000;

export const MODE_OBSERVE = "observe";
export const MODE_GUARD = "guard";
export const MODES = Object.freeze([MODE_OBSERVE, MODE_GUARD]);

/**
 * Semantic critic backends.
 * - `http` (default, unchanged): the spawned Crisp binary calls an external
 *   OpenAI-compatible endpoint.
 * - `harness`: the spawned binary runs deterministic rules only, and this
 *   plugin asks the Harness LLM service (ctx.llm) for the semantic verdict.
 */
export const CRITIC_HTTP = "http";
export const CRITIC_HARNESS = "harness";
export const CRITICS = Object.freeze([CRITIC_HTTP, CRITIC_HARNESS]);

/** Harness-critic bounds; the external-http path keeps the Go defaults. */
export const DEFAULT_CRITIC_TIMEOUT_MS = 15_000;
export const MIN_CRITIC_TIMEOUT_MS = 1_000;
export const MAX_CRITIC_TIMEOUT_MS = 30_000;
export const DEFAULT_CRITIC_REASONING_EFFORT = "off";
export const CRITIC_REASONING_EFFORTS = Object.freeze(["off", "low", "high", "max"]);
/** Small output cap: the critic returns one JSON object, never prose. */
export const HARNESS_CRITIC_MAX_TOKENS = 1024;
/** Deterministic judgement: the Go critic also uses temperature 0. */
export const HARNESS_CRITIC_TEMPERATURE = 0;

/**
 * Byte-for-byte the rewrite instruction from internal/policy/decision.go
 * (`policy.RewriteReason`). Keep it identical: the rewrite must ask for a
 * complete non-redundant answer, never for a shorter one.
 */
export const DEFAULT_REWRITE_INSTRUCTION =
  "请重新输出完整最终回答：合并语义重复的段落，删除无效铺垫和结论重复，保留全部必要事实、数字、代码和解释，不要为了缩短篇幅而删除必要信息。";

/**
 * Variables copied from the Harness process environment into the child when
 * they are present. The child does NOT inherit the whole environment: model
 * provider keys and unrelated secrets stay out of the quality-gate process.
 * Entries ending in "*" match by prefix.
 */
export const DEFAULT_ENV_PASSTHROUGH = Object.freeze([
  "QUALITY_GATE_BASE_URL",
  "QUALITY_GATE_API_KEY",
  "QUALITY_GATE_MODEL",
  "QUALITY_GATE_MODE",
  "QUALITY_GATE_CRITIC_TIMEOUT_MS",
  "QUALITY_GATE_MIN_CRITIC_RUNES",
  "QUALITY_GATE_TOTAL_TIMEOUT_MS",
  "QUALITY_GATE_EXTRA_BODY_JSON",
  "CRISP_BIN",
  "CRISP_TIMEOUT_MS",
  "CRISP_DSH_LOG_FILE",
]);

/**
 * Variables a child process needs to start at all on each platform. The
 * allowlist is deliberately small; it is not a security boundary on the same
 * user, but it keeps the quality gate from receiving unrelated secrets.
 */
const SYSTEM_ENV_KEYS = Object.freeze([
  "PATH",
  "PATHEXT",
  "SYSTEMROOT",
  "SystemRoot",
  "WINDIR",
  "COMSPEC",
  "TEMP",
  "TMP",
  "TMPDIR",
  "USERPROFILE",
  "HOME",
  "HOMEDRIVE",
  "HOMEPATH",
  "NUMBER_OF_PROCESSORS",
  "PROCESSOR_ARCHITECTURE",
  "OS",
  "LANG",
  "LC_ALL",
  "LC_CTYPE",
]);

function nonEmptyString(value) {
  return typeof value === "string" && value.trim() !== "" ? value.trim() : undefined;
}

function asNumber(value, fallback) {
  const n = typeof value === "number" ? value : Number.parseFloat(String(value ?? ""));
  return Number.isFinite(n) ? n : fallback;
}

function clamp(value, min, max) {
  return Math.min(max, Math.max(min, Math.trunc(value)));
}

function asBoolean(value) {
  return typeof value === "boolean" ? value : undefined;
}

/** Truthiness used only for opt-out environment switches. */
export function isTruthyEnv(value) {
  if (value === undefined || value === null) return false;
  const text = String(value).trim().toLowerCase();
  return text === "1" || text === "true" || text === "yes" || text === "on";
}

/**
 * Default Crisp binary: the checkout's bin/crisp[.exe] when this package runs
 * from the repository, otherwise the PATH-resolved "crisp" command.
 */
export function defaultCrispBin(platform = process.platform, repoRoot = REPO_ROOT, exists = fs.existsSync) {
  const name = platform === "win32" ? "crisp.exe" : "crisp";
  const checkout = path.join(repoRoot, "bin", name);
  if (exists(checkout)) return checkout;
  return platform === "win32" ? "crisp.exe" : "crisp";
}

function normalizePassthrough(value) {
  if (!Array.isArray(value)) return [...DEFAULT_ENV_PASSTHROUGH];
  const names = value
    .filter((entry) => typeof entry === "string")
    .map((entry) => entry.trim())
    .filter((entry) => entry !== "");
  return names.length > 0 ? names : [...DEFAULT_ENV_PASSTHROUGH];
}

function normalizeEnvOverrides(value) {
  if (!value || typeof value !== "object" || Array.isArray(value)) return {};
  const out = {};
  for (const [key, raw] of Object.entries(value)) {
    if (typeof key !== "string" || key === "") continue;
    if (raw === undefined || raw === null) continue;
    if (typeof raw === "string" || typeof raw === "number" || typeof raw === "boolean") {
      out[key] = String(raw);
    }
  }
  return out;
}

/**
 * Resolve the effective host-plugin configuration.
 *
 * Precedence (highest first) for each field: explicit plugin config > the
 * dedicated CRISP_DSH_* environment variable (only where one exists) >
 * default. The generic QUALITY_GATE_* critic settings are NOT interpreted
 * here: they belong to the spawned binary and are passed through untouched.
 *
 * @param {unknown} raw Loader row config (a plain object from cordis.patch.yml).
 * @param {NodeJS.ProcessEnv} [env] Harness process environment.
 * @param {string} [platform] Platform for the default binary name.
 * @returns {object} normalized configuration plus non-fatal warnings.
 */
export function resolveConfig(raw, env = process.env, platform = process.platform) {
  const input = raw && typeof raw === "object" && !Array.isArray(raw) ? raw : {};
  const optionEnv = normalizeEnvOverrides(input.env);
  const warnings = [];

  let mode = nonEmptyString(input.mode) ?? nonEmptyString(env.CRISP_DSH_MODE) ?? MODE_OBSERVE;
  mode = mode.toLowerCase();
  if (!MODES.includes(mode)) {
    warnings.push(`unknown mode ${JSON.stringify(mode)}; falling back to ${MODE_OBSERVE}`);
    mode = MODE_OBSERVE;
  }

  const binaryPath =
    nonEmptyString(input.binaryPath) ??
    nonEmptyString(optionEnv.CRISP_BIN) ??
    nonEmptyString(env.CRISP_BIN) ??
    defaultCrispBin(platform);

  const rawTimeout =
    input.timeoutMs ?? optionEnv.CRISP_TIMEOUT_MS ?? env.CRISP_TIMEOUT_MS ?? DEFAULT_TIMEOUT_MS;
  const timeoutMs = clamp(asNumber(rawTimeout, DEFAULT_TIMEOUT_MS), MIN_TIMEOUT_MS, MAX_TIMEOUT_MS);
  if (asNumber(rawTimeout, DEFAULT_TIMEOUT_MS) !== timeoutMs) {
    warnings.push(`timeoutMs clamped to ${timeoutMs}`);
  }

  const rawMinRunes = input.minRunes ?? env.CRISP_DSH_MIN_RUNES;
  const minRunes = clamp(asNumber(rawMinRunes, DEFAULT_MIN_RUNES), 0, 100_000);
  const maxRunes = clamp(asNumber(input.maxRunes, DEFAULT_MAX_RUNES), minRunes, 4_000_000);
  if (maxRunes < DEFAULT_MAX_RUNES && asNumber(input.maxRunes, DEFAULT_MAX_RUNES) > 4_000_000) {
    warnings.push(`maxRunes clamped to ${maxRunes}`);
  }

  const disabled = isTruthyEnv(env.CRISP_DSH_DISABLE);
  const enabled = asBoolean(input.enabled) ?? !disabled;

  let logFile = "";
  if (input.logFile === false) logFile = false;
  else {
    logFile =
      nonEmptyString(input.logFile) ??
      nonEmptyString(env.CRISP_DSH_LOG_FILE) ??
      "";
  }

  const rewriteInstruction =
    nonEmptyString(input.rewriteInstruction) ?? DEFAULT_REWRITE_INSTRUCTION;

  // Semantic critic backend. `http` is the historical default and its whole
  // code path stays byte-identical; `harness` is opt-in.
  let critic = nonEmptyString(input.critic) ?? CRITIC_HTTP;
  critic = critic.toLowerCase();
  if (!CRITICS.includes(critic)) {
    warnings.push(`unknown critic ${JSON.stringify(critic)}; falling back to ${CRITIC_HTTP}`);
    critic = CRITIC_HTTP;
  }
  const criticProvider = nonEmptyString(input.criticProvider) ?? "";
  const criticModel = nonEmptyString(input.criticModel) ?? "";
  if (critic === CRITIC_HARNESS && (criticProvider === "" || criticModel === "")) {
    warnings.push(
      `critic "${CRITIC_HARNESS}" needs criticProvider and criticModel; reviews will fail open until both are set`,
    );
  }
  let criticReasoningEffort =
    nonEmptyString(input.criticReasoningEffort) ?? DEFAULT_CRITIC_REASONING_EFFORT;
  criticReasoningEffort = criticReasoningEffort.toLowerCase();
  if (!CRITIC_REASONING_EFFORTS.includes(criticReasoningEffort)) {
    warnings.push(
      `unknown criticReasoningEffort ${JSON.stringify(criticReasoningEffort)}; falling back to ${DEFAULT_CRITIC_REASONING_EFFORT}`,
    );
    criticReasoningEffort = DEFAULT_CRITIC_REASONING_EFFORT;
  }
  const criticTimeoutMs = clamp(
    asNumber(input.criticTimeoutMs, DEFAULT_CRITIC_TIMEOUT_MS),
    MIN_CRITIC_TIMEOUT_MS,
    MAX_CRITIC_TIMEOUT_MS,
  );
  if (asNumber(input.criticTimeoutMs, DEFAULT_CRITIC_TIMEOUT_MS) !== criticTimeoutMs) {
    warnings.push(`criticTimeoutMs clamped to ${criticTimeoutMs}`);
  }

  return {
    pluginVersion: PLUGIN_VERSION,
    enabled,
    mode,
    binaryPath,
    timeoutMs,
    minRunes,
    maxRunes,
    logFile,
    rewriteInstruction,
    critic,
    criticProvider,
    criticModel,
    criticReasoningEffort,
    criticTimeoutMs,
    envPassthrough: normalizePassthrough(input.envPassthrough),
    env: optionEnv,
    warnings,
  };
}

/** Whether a passthrough entry matches an environment variable name. */
function matchesPassthrough(pattern, name) {
  if (pattern.endsWith("*")) return name.startsWith(pattern.slice(0, -1));
  return name === pattern;
}

/**
 * Build the environment for the spawned Crisp process: platform essentials,
 * explicitly passed-through variables, then config overrides. Never the whole
 * Harness environment.
 */
export function buildChildEnv(config, baseEnv = process.env) {
  const out = {};
  for (const key of SYSTEM_ENV_KEYS) {
    if (typeof baseEnv[key] === "string") out[key] = baseEnv[key];
  }
  for (const pattern of config?.envPassthrough ?? []) {
    for (const [key, value] of Object.entries(baseEnv)) {
      if (value !== undefined && matchesPassthrough(pattern, key)) out[key] = value;
    }
  }
  for (const [key, value] of Object.entries(config?.env ?? {})) out[key] = value;
  return out;
}