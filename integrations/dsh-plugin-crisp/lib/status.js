// Read-only status service for the Crisp quality gate.
//
// The Host plugin serves one JSON snapshot on GET /crisp/status for the Web
// client's main-area page (opened from the left-sidebar entry). The snapshot is
// built from the same JSONL log the gate writes: counts for today, the latest
// checks, guard activity, and a sanitized configuration summary. It never
// reads or returns provider credentials: only the critic host name, model id
// and capability flags leave this module.
import {
  closeSync,
  fstatSync,
  openSync,
  readSync,
} from "node:fs";

import { buildChildEnv, resolveConfig } from "./config.js";

/** Route path served by the Host plugin. */
export const STATUS_PATH = "/crisp/status";

/** Read at most this many bytes from the end of the log file. */
const MAX_TAIL_BYTES = 1_500_000;
/** How many recent checks the page renders. */
const MAX_RECENT_CHECKS = 20;
/** How many recent checks the average latency is computed over. */
const AVG_LATENCY_SAMPLE = 20;

/** Local calendar-day key, used to count today's checks. */
function localDayKey(date) {
  return date.getFullYear() + "-" + (date.getMonth() + 1) + "-" + date.getDate();
}

/** Parse a timestamp; invalid input becomes null. */
function parseTime(value) {
  if (typeof value !== "string" || value === "") return null;
  const time = Date.parse(value);
  return Number.isFinite(time) ? time : null;
}

/** Safe JSON parse: malformed input becomes undefined. */
function safeJson(text) {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

/** Only the URL host of the configured critic endpoint, never credentials. */
function safeHost(baseURL) {
  if (typeof baseURL !== "string" || baseURL.trim() === "") return "";
  try {
    return new URL(baseURL).host;
  } catch {
    return "";
  }
}

/** Whether the merged child environment disables model thinking. */
function thinkingDisabled(extraBodyJSON) {
  const extra = safeJson(extraBodyJSON ?? "");
  if (extra === undefined || extra === null || typeof extra !== "object" || Array.isArray(extra)) {
    return false;
  }
  const thinking = extra.thinking;
  if (thinking !== null && typeof thinking === "object" && !Array.isArray(thinking)) {
    if (thinking.type === "disabled") return true;
    if (thinking.enabled === false) return true;
  }
  if (extra.enable_thinking === false) return true;
  const effort = extra.reasoning_effort ?? extra.reasoningEffort;
  return effort === "none" || effort === "off" || effort === "minimal";
}

/** Normalize a rule-type list for the page. */
function ruleTypesOf(record) {
  const types = Array.isArray(record.rule_types) ? record.rule_types : [];
  return types.filter((item) => typeof item === "string").slice(0, 8);
}

/** One recent-check row, with defensive defaults for older log lines. */
function recentCheckOf(record) {
  return {
    ts: typeof record.ts === "string" ? record.ts : "",
    turn: Number.isInteger(record.turn) ? record.turn : null,
    decision: typeof record.decision === "string" ? record.decision : "pass",
    reasonType: typeof record.reason_type === "string" ? record.reason_type : "",
    ruleTypes: ruleTypesOf(record),
    criticCalled: record.critic_called === true,
    failOpen: record.fail_open === true,
    criticLatencyMs: Number.isFinite(record.critic_latency_ms) ? record.critic_latency_ms : 0,
    criticTokens: Number.isFinite(record.critic_tokens) ? record.critic_tokens : 0,
    answerRunes: Number.isFinite(record.answer_runes) ? record.answer_runes : 0,
  };
}

/** Read the tail of the log file without loading an unbounded file. */
export function readLogTail(file, fs = { openSync, fstatSync, readSync, closeSync }) {
  if (typeof file !== "string" || file === "") {
    return { exists: false, sizeBytes: 0, lines: [] };
  }
  let fd;
  try {
    fd = fs.openSync(file, "r");
  } catch (error) {
    if (error?.code === "ENOENT") return { exists: false, sizeBytes: 0, lines: [] };
    throw error;
  }
  try {
    const stat = fs.fstatSync(fd);
    const size = stat.size;
    const start = Math.max(0, size - MAX_TAIL_BYTES);
    const length = size - start;
    const buffer = Buffer.allocUnsafe(length);
    let read = 0;
    while (read < length) {
      const count = fs.readSync(fd, buffer, read, length - read, start + read);
      if (count <= 0) break;
      read += count;
    }
    let lines = buffer.subarray(0, read).toString("utf8").split(/\r?\n/);
    // A tail read usually starts inside a line: drop that partial first line.
    if (start > 0) lines = lines.slice(1);
    return { exists: true, sizeBytes: size, lines: lines.filter((line) => line.trim() !== "") };
  } finally {
    fs.closeSync(fd);
  }
}

/** Parse JSONL records, silently skipping malformed lines. */
function parseRecords(lines) {
  const records = [];
  for (const line of lines) {
    const record = safeJson(line);
    if (record !== null && typeof record === "object" && !Array.isArray(record)) records.push(record);
  }
  return records;
}

/**
 * Build the status service for one mounted Host plugin.
 *
 * @param {object} ctx Cordis plugin context (used only for the connection fence)
 * @param {unknown} rawConfig Loader row config
 * @param {object} [deps] Injectable seams: env, platform, now, fs
 * @returns {{route: string, snapshot: () => object}}
 */
export function createStatusService(ctx, rawConfig, deps = {}) {
  const env = deps.env ?? process.env;
  const platform = deps.platform ?? process.platform;
  const config = resolveConfig(rawConfig, env, platform);
  const now = typeof deps.now === "function" ? deps.now : () => new Date();
  const fs = deps.fs ?? { openSync, fstatSync, readSync, closeSync };
  const gateEnv = buildChildEnv(config, env);

  const critic = (() => {
    if (config.critic === "harness") {
      return {
        mode: "harness",
        provider: config.criticProvider,
        model: config.criticModel,
        reasoningEffort: config.criticReasoningEffort,
        baseURLHost: "",
        thinkingOff: config.criticReasoningEffort === "off",
        configured: config.criticProvider !== "" && config.criticModel !== "",
      };
    }
    const model = typeof gateEnv.QUALITY_GATE_MODEL === "string" ? gateEnv.QUALITY_GATE_MODEL : "";
    const baseURLHost = safeHost(gateEnv.QUALITY_GATE_BASE_URL);
    return {
      mode: "http",
      provider: "",
      model,
      reasoningEffort: "",
      baseURLHost,
      thinkingOff: thinkingDisabled(gateEnv.QUALITY_GATE_EXTRA_BODY_JSON),
      configured: baseURLHost !== "" && model !== "",
    };
  })();

  return {
    route: STATUS_PATH,
    snapshot() {
      const current = now();
      const todayKey = localDayKey(current);
      const log = readLogTail(config.logFile === false ? "" : config.logFile, fs);
      const records = parseRecords(log.lines);

      const checks = records
        .filter((record) => record.event === "crisp_quality_check")
        .sort((a, b) => (parseTime(a.ts) ?? 0) - (parseTime(b.ts) ?? 0));
      const todayChecks = checks.filter((record) => {
        const time = parseTime(record.ts);
        return time !== null && localDayKey(new Date(time)) === todayKey;
      });
      const criticCalls = checks.filter(
        (record) =>
          record.critic_called === true &&
          record.fail_open !== true &&
          Number.isFinite(record.critic_latency_ms) &&
          record.critic_latency_ms > 0,
      );
      const latencySample = criticCalls.slice(-AVG_LATENCY_SAMPLE);
      const avgCriticLatencyMs =
        latencySample.length === 0
          ? 0
          : Math.round(
              latencySample.reduce((sum, record) => sum + record.critic_latency_ms, 0) /
                latencySample.length,
            );

      const countBy = (list, predicate) => list.filter(predicate).length;
      const pass = (record) => record.fail_open !== true && record.decision === "pass";
      const block = (record) => record.fail_open !== true && record.decision === "block";
      const failOpen = (record) => record.fail_open === true;

      const last = checks.length === 0 ? null : checks[checks.length - 1];
      const recent = checks.slice(-MAX_RECENT_CHECKS).reverse().map(recentCheckOf);

      const guardFollowups = records.filter((record) => record.event === "crisp_guard_followup");
      const guardSkipped = records.filter((record) => record.event === "crisp_guard_skipped");
      const errors = records.filter((record) => record.event === "crisp_quality_error");
      const lastFollowup = guardFollowups.length === 0 ? null : guardFollowups[guardFollowups.length - 1];

      return {
        ok: true,
        pluginVersion: config.pluginVersion,
        generatedAt: current.toISOString(),
        config: {
          enabled: config.enabled,
          mode: config.mode,
          maxRunes: config.maxRunes,
          minRunes: config.minRunes,
          logFile: config.logFile === false ? null : config.logFile,
          binaryPath: config.binaryPath,
          warnings: config.warnings,
          critic,
        },
        log: {
          exists: log.exists,
          sizeBytes: log.sizeBytes,
        },
        summary: {
          total: checks.length,
          pass: countBy(checks, pass),
          block: countBy(checks, block),
          failOpen: countBy(checks, failOpen),
          criticCalls: criticCalls.length,
          avgCriticLatencyMs,
          lastDecision: last === null ? null : last.fail_open === true ? "fail_open" : last.decision,
          lastCheckAt: last !== null && typeof last.ts === "string" ? last.ts : null,
          today: {
            total: todayChecks.length,
            pass: countBy(todayChecks, pass),
            block: countBy(todayChecks, block),
            failOpen: countBy(todayChecks, failOpen),
          },
          guard: {
            followups: guardFollowups.length,
            skipped: guardSkipped.length,
            lastFollowupAt:
              lastFollowup !== null && typeof lastFollowup.ts === "string" ? lastFollowup.ts : null,
          },
          errors: errors.length,
        },
        recent,
      };
    },
  };
}

/** Send one JSON body with the local-page cache policy. */
function sendJson(res, status, payload) {
  const body = JSON.stringify(payload);
  res.statusCode = status;
  res.setHeader("content-type", "application/json; charset=utf-8");
  res.setHeader("cache-control", "no-store");
  res.setHeader("content-length", String(Buffer.byteLength(body)));
  res.end(body);
}

/** Whether the composition's connection service rejects this request first. */
function rejected(ctx, req, res) {
  let connection;
  try {
    connection = typeof ctx?.get === "function" ? ctx.get("connection") : undefined;
  } catch {
    connection = undefined;
  }
  if (connection === undefined || connection === null) return false;
  if (typeof connection.requestRejection !== "function") return false;
  const rejection = connection.requestRejection(req);
  if (rejection === undefined) return false;
  res.statusCode = rejection;
  res.end();
  return true;
}

/**
 * Create the Node request handler for GET/HEAD /crisp/status.
 *
 * @param {{snapshot: () => object}} service status service
 * @param {object} ctx Cordis plugin context (for the connection fence)
 * @returns {(req: object, res: object) => void}
 */
export function createStatusHandler(service, ctx) {
  return function handleStatus(req, res) {
    if (rejected(ctx, req, res)) return;
    if (req.method !== "GET" && req.method !== "HEAD") {
      res.statusCode = 405;
      res.setHeader("allow", "GET, HEAD");
      res.end();
      return;
    }
    let snapshot;
    try {
      snapshot = service.snapshot();
    } catch {
      sendJson(res, 500, { ok: false, error: "status-unavailable" });
      return;
    }
    if (req.method === "HEAD") {
      res.statusCode = 200;
      res.setHeader("content-type", "application/json; charset=utf-8");
      res.setHeader("cache-control", "no-store");
      res.end();
      return;
    }
    sendJson(res, 200, snapshot);
  };
}
