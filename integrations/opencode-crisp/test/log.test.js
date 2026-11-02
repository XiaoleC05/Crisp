import test from "node:test";
import assert from "node:assert/strict";
import os from "node:os";
import path from "node:path";
import fs from "node:fs/promises";

import { createJsonLineLogger } from "../lib/log.js";

test("writes exactly one JSON line to the log file and to app.log", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "crisp-log-test-"));
  const logFile = path.join(dir, "nested", "quality-gate.jsonl");
  const appLog = [];
  const logger = createJsonLineLogger({
    client: {
      app: {
        log: async (args) => {
          appLog.push(args);
        },
      },
    },
    config: { logFile, appLog: true, service: "crisp" },
    now: () => Date.UTC(2026, 10, 2, 12, 0, 0),
  });
  await logger({ event: "quality_check", decision: "block" });
  const lines = (await fs.readFile(logFile, "utf8")).trim().split("\n");
  assert.equal(lines.length, 1);
  const parsed = JSON.parse(lines[0]);
  assert.equal(parsed.event, "quality_check");
  assert.equal(parsed.decision, "block");
  assert.equal(parsed.ts, "2026-11-02T12:00:00.000Z");
  assert.equal(appLog.length, 1);
  assert.equal(appLog[0].body.service, "crisp");
  assert.equal(appLog[0].body.message, lines[0]);
  assert.equal(appLog[0].body.extra.decision, "block");
  await fs.rm(dir, { recursive: true, force: true });
});

test("logging failures are swallowed", async () => {
  const logger = createJsonLineLogger({
    client: {
      app: {
        log: async () => {
          throw new Error("no server");
        },
      },
    },
    config: { logFile: false, appLog: true, service: "crisp" },
  });
  await logger({ event: "quality_check" }); // must not reject
});
