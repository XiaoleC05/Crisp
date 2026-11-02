import test from "node:test";
import assert from "node:assert/strict";
import path from "node:path";

import {
  DEFAULT_REWRITE_INSTRUCTION,
  DEFAULT_TIMEOUT_MS,
  MAX_TIMEOUT_MS,
  MIN_TIMEOUT_MS,
  REPO_ROOT,
  buildCheckEnv,
  defaultCrispBin,
  resolveConfig,
} from "../lib/config.js";

test("defaults: repo bin path, 30s timeout, rewrite+toast+appLog on", () => {
  const config = resolveConfig({}, {}, "win32");
  assert.equal(config.crispBin, path.join(REPO_ROOT, "bin", "crisp.exe"));
  assert.equal(config.timeoutMs, DEFAULT_TIMEOUT_MS);
  assert.equal(config.rewrite, true);
  assert.equal(config.toast, true);
  assert.equal(config.appLog, true);
  assert.equal(config.includeSubagents, false);
  assert.equal(config.enabled, true);
  assert.match(config.logFile, /opencode-crisp/);
  assert.equal(config.mode, undefined);
  assert.deepEqual(config.env, {});
  assert.equal(DEFAULT_REWRITE_INSTRUCTION.startsWith("请重新输出完整最终回答"), true);
});

test("defaultCrispBin honours the platform", () => {
  assert.equal(defaultCrispBin("win32", "C:\\r"), "C:\\r\\bin\\crisp.exe");
  assert.equal(defaultCrispBin("linux", "/repo"), "/repo/bin/crisp");
});

test("bin precedence: options.env > options.crispBin > env.CRISP_BIN > repo default", () => {
  const base = resolveConfig({ crispBin: "C:\\x\\crisp.exe" }, { CRISP_BIN: "C:\\env\\crisp.exe" });
  assert.equal(base.crispBin, "C:\\x\\crisp.exe");

  const envOnly = resolveConfig({}, { CRISP_BIN: "C:\\env\\crisp.exe" });
  assert.equal(envOnly.crispBin, "C:\\env\\crisp.exe");

  const envTuple = resolveConfig({ crispBin: "C:\\x\\crisp.exe", env: { CRISP_BIN: "C:\\tuple\\crisp.exe" } }, {});
  assert.equal(envTuple.crispBin, "C:\\tuple\\crisp.exe");
});

test("timeout is clamped into [1s, 120s] and read from env", () => {
  assert.equal(resolveConfig({ timeoutMs: 5 }, {}).timeoutMs, MIN_TIMEOUT_MS);
  assert.equal(resolveConfig({ timeoutMs: 999_999 }, {}).timeoutMs, MAX_TIMEOUT_MS);
  assert.equal(resolveConfig({ timeoutMs: 12_345 }, {}).timeoutMs, 12_345);
  assert.equal(resolveConfig({}, { CRISP_TIMEOUT_MS: "4000" }).timeoutMs, 4_000);
  assert.equal(resolveConfig({}, { CRISP_TIMEOUT_MS: "not-a-number" }).timeoutMs, DEFAULT_TIMEOUT_MS);
});

test("mode maps to QUALITY_GATE_MODE and tuple env overrides win", () => {
  const config = resolveConfig({ mode: "economy" }, {});
  assert.equal(config.env.QUALITY_GATE_MODE, "economy");

  const tuple = resolveConfig({ env: { QUALITY_GATE_MODE: "balanced", QUALITY_GATE_BASE_URL: "http://127.0.0.1:9/v1" } }, {});
  assert.equal(tuple.mode, "balanced");
  assert.equal(tuple.env.QUALITY_GATE_BASE_URL, "http://127.0.0.1:9/v1");
});

test("buildCheckEnv inherits the parent env and applies tuple overrides", () => {
  const config = resolveConfig({ mode: "economy", env: { EXTRA: "1", REMOVED: undefined } }, {});
  const env = buildCheckEnv(config, { QUALITY_GATE_BASE_URL: "http://mock/v1", PATH: "p", REMOVED: "gone" });
  assert.equal(env.QUALITY_GATE_BASE_URL, "http://mock/v1");
  assert.equal(env.QUALITY_GATE_MODE, "economy");
  assert.equal(env.EXTRA, "1");
  assert.equal(env.PATH, "p");
  assert.equal("REMOVED" in env, false);
});

test("switches can disable rewrite, toast, appLog, logFile, subagents", () => {
  const config = resolveConfig(
    { rewrite: false, toast: false, appLog: false, logFile: false, includeSubagents: true },
    {},
  );
  assert.equal(config.rewrite, false);
  assert.equal(config.toast, false);
  assert.equal(config.appLog, false);
  assert.equal(config.logFile, false);
  assert.equal(config.includeSubagents, true);
});

test("CRISP_OPENCODE_LOG_FILE overrides the default log path", () => {
  const config = resolveConfig({}, { CRISP_OPENCODE_LOG_FILE: "C:\\tmp\\gate.jsonl" });
  assert.equal(config.logFile, "C:\\tmp\\gate.jsonl");
});

test("CRISP_OPENCODE_DISABLE disables the plugin", () => {
  assert.equal(resolveConfig({}, { CRISP_OPENCODE_DISABLE: "1" }).enabled, false);
  assert.equal(resolveConfig({}, { CRISP_OPENCODE_DISABLE: "true" }).enabled, false);
  assert.equal(resolveConfig({}, { CRISP_OPENCODE_DISABLE: "0" }).enabled, true);
});
