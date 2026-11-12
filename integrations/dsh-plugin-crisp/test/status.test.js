import assert from "node:assert/strict";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { test } from "node:test";

import {
  STATUS_PATH,
  createStatusHandler,
  createStatusService,
  readLogTail,
} from "../lib/status.js";

/** Build a temporary log file from JSONL records. */
function withLog(records, run) {
  const dir = mkdtempSync(path.join(tmpdir(), "crisp-status-"));
  const file = path.join(dir, "quality-gate.jsonl");
  writeFileSync(file, records.map((record) => JSON.stringify(record)).join("\n") + "\n", "utf8");
  try {
    return run(file, dir);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

/** Build one quality-check record. */
function check(overrides = {}) {
  return {
    ts: new Date().toISOString(),
    level: "info",
    plugin: "crisp-quality-gate",
    event: "crisp_quality_check",
    mode: "guard",
    decision: "pass",
    reason_type: "critic_pass",
    rule_types: [],
    critic_called: true,
    fail_open: false,
    critic_latency_ms: 800,
    critic_tokens: 1000,
    answer_runes: 900,
    turn: 1,
    ...overrides,
  };
}

/** A noon-local clock so fixed offsets stay inside one local calendar day. */
const NOON = new Date(2026, 10, 12, 12, 0, 0);

/** Fake Node response collecting status, headers and body. */
function fakeResponse() {
  return {
    statusCode: 200,
    headers: {},
    body: "",
    setHeader(name, value) {
      this.headers[String(name).toLowerCase()] = value;
    },
    end(chunk) {
      this.body += chunk === undefined || chunk === null ? "" : String(chunk);
      this.ended = true;
    },
  };
}

test("snapshot aggregates recent checks, today counts and guard activity", () => {
  const now = () => NOON;
  const minutesAgo = (minutes) => new Date(NOON.getTime() - minutes * 60_000).toISOString();
  const records = [
    { ts: minutesAgo(60), event: "crisp_quality_gate_active", mode: "guard" },
    check({ ts: minutesAgo(10), decision: "pass", critic_latency_ms: 600, turn: 1 }),
    check({
      ts: minutesAgo(5),
      decision: "block",
      reason_type: "semantic_duplicate",
      rule_types: ["duplicate_sentence", "conclusion_repetition"],
      critic_latency_ms: 900,
      critic_tokens: 1200,
      turn: 2,
    }),
    check({
      ts: minutesAgo(2),
      decision: "pass",
      reason_type: "critic_error",
      critic_called: false,
      fail_open: true,
      critic_latency_ms: 0,
      critic_tokens: 0,
      turn: 3,
    }),
    check({
      ts: new Date(NOON.getTime() - 26 * 60 * 60_000).toISOString(),
      decision: "block",
      critic_called: false,
      critic_latency_ms: 0,
      turn: 0,
    }),
    { ts: minutesAgo(1), event: "crisp_guard_followup", turn: 2 },
    { ts: minutesAgo(1), event: "crisp_quality_error", phase: "rules" },
  ];
  withLog(records, (file) => {
    const service = createStatusService(
      {},
      {
        mode: "guard",
        logFile: file,
        env: {
          QUALITY_GATE_BASE_URL: "https://api.deepseek.com/v1",
          QUALITY_GATE_MODEL: "deepseek-flash",
          QUALITY_GATE_API_KEY: "sk-secret-value",
          QUALITY_GATE_EXTRA_BODY_JSON: '{"thinking":{"type":"disabled"}}',
        },
      },
      { now },
    );
    const snapshot = service.snapshot();
    assert.equal(snapshot.ok, true);
    assert.equal(snapshot.config.mode, "guard");
    assert.equal(snapshot.config.delivery, "stream");
    assert.equal(snapshot.config.critic.mode, "http");
    assert.equal(snapshot.config.critic.model, "deepseek-flash");
    assert.equal(snapshot.config.critic.baseURLHost, "api.deepseek.com");
    assert.equal(snapshot.config.critic.thinkingOff, true);
    assert.equal(snapshot.config.critic.configured, true);
    assert.equal(snapshot.summary.total, 4);
    assert.equal(snapshot.summary.today.total, 3);
    assert.equal(snapshot.summary.today.pass, 1);
    assert.equal(snapshot.summary.today.block, 1);
    assert.equal(snapshot.summary.today.failOpen, 1);
    assert.equal(snapshot.summary.criticCalls, 2);
    assert.equal(snapshot.summary.avgCriticLatencyMs, 750);
    assert.equal(snapshot.summary.guard.followups, 1);
    assert.equal(snapshot.summary.errors, 1);
    assert.equal(snapshot.summary.lastDecision, "fail_open");
    assert.equal(snapshot.recent.length, 4);
    assert.equal(snapshot.recent[0].failOpen, true);
    assert.equal(snapshot.recent[1].decision, "block");
    assert.deepEqual(snapshot.recent[1].ruleTypes, ["duplicate_sentence", "conclusion_repetition"]);
    assert.equal(typeof snapshot.recent[0].ts, "string");
    assert.equal(snapshot.generatedAt, NOON.toISOString());
    const serialized = JSON.stringify(snapshot);
    assert.equal(serialized.includes("sk-secret-value"), false, "API key must never leave the host");
    assert.equal(serialized.includes("https://api.deepseek.com"), false, "raw base URL is not needed");
  });
});

test("snapshot exposes the optimize delivery configuration", () => {
  const service = createStatusService(
    {},
    {
      mode: "guard",
      delivery: "optimize",
      optimizerProvider: "deepseek-account",
      optimizerModel: "deepseek-flash",
      optimizerReasoningEffort: "off",
      optimizerTimeoutMs: 30000,
      logFile: "",
    },
    { now: () => NOON },
  );
  const snapshot = service.snapshot();
  assert.equal(snapshot.config.delivery, "optimize");
  assert.equal(snapshot.config.optimizer.provider, "deepseek-account");
  assert.equal(snapshot.config.optimizer.model, "deepseek-flash");
  assert.equal(snapshot.config.optimizer.configured, true);
});

test("missing or malformed logs degrade to an empty snapshot", () => {
  const service = createStatusService({}, { logFile: "" }, { now: () => NOON });
  const empty = service.snapshot();
  assert.equal(empty.log.exists, false);
  assert.equal(empty.summary.total, 0);
  assert.equal(empty.recent.length, 0);
  assert.equal(empty.summary.today.total, 0);

  withLog(["{not json", check({ ts: NOON.toISOString() })], (file) => {
    const snapshot = createStatusService({}, { logFile: file }, { now: () => NOON }).snapshot();
    assert.equal(snapshot.log.exists, true);
    assert.equal(snapshot.summary.total, 1);
  });
});

test("readLogTail reads a bounded tail and skips its partial first line", () => {
  const dir = mkdtempSync(path.join(tmpdir(), "crisp-tail-"));
  const file = path.join(dir, "log.jsonl");
  // Larger than the 1.5 MB tail window so the read starts inside this line.
  const filler = "x".repeat(1_600_000);
  writeFileSync(file, filler + "\n" + JSON.stringify({ event: "a" }) + "\n" + JSON.stringify({ event: "b" }) + "\n", "utf8");
  try {
    const tail = readLogTail(file);
    assert.equal(tail.exists, true);
    assert.equal(tail.lines.length, 2);
    assert.equal(JSON.parse(tail.lines[0]).event, "a");
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("the route serves GET/HEAD JSON and rejects other methods", () => {
  const service = { snapshot: () => ({ ok: true, value: 1 }) };
  const handler = createStatusHandler(service, {});

  const get = fakeResponse();
  handler({ method: "GET" }, get);
  assert.equal(get.statusCode, 200);
  assert.match(get.headers["content-type"], /application\/json/);
  assert.equal(get.headers["cache-control"], "no-store");
  assert.deepEqual(JSON.parse(get.body), { ok: true, value: 1 });

  const head = fakeResponse();
  handler({ method: "HEAD" }, head);
  assert.equal(head.statusCode, 200);
  assert.equal(head.body, "");

  const post = fakeResponse();
  handler({ method: "POST" }, post);
  assert.equal(post.statusCode, 405);
  assert.equal(post.headers.allow, "GET, HEAD");
});

test("the route defers to the connection fence when it rejects a request", () => {
  let seenMethod = "";
  const ctx = {
    get(name) {
      if (name !== "connection") return undefined;
      return {
        requestRejection(req) {
          seenMethod = req.method;
          return 403;
        },
      };
    },
  };
  const handler = createStatusHandler({ snapshot: () => ({ ok: true }) }, ctx);
  const response = fakeResponse();
  handler({ method: "GET" }, response);
  assert.equal(seenMethod, "GET");
  assert.equal(response.statusCode, 403);
  assert.equal(response.body, "");
});

test("route path stays stable for the web client", () => {
  assert.equal(STATUS_PATH, "/crisp/status");
});
