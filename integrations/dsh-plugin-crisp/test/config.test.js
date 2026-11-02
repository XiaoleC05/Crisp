import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import {
  DEFAULT_REWRITE_INSTRUCTION,
  DEFAULT_TIMEOUT_MS,
  MAX_TIMEOUT_MS,
  MIN_TIMEOUT_MS,
  MODE_GUARD,
  MODE_OBSERVE,
  PLUGIN_VERSION,
  buildChildEnv,
  defaultCrispBin,
  isTruthyEnv,
  resolveConfig,
} from "../lib/config.js";

const PACKAGE_JSON = JSON.parse(
  readFileSync(fileURLToPath(new URL("../package.json", import.meta.url)), "utf8"),
);
const REPO_ROOT = fileURLToPath(new URL("../../..", import.meta.url));

test("package version and config version agree", () => {
  assert.equal(PLUGIN_VERSION, PACKAGE_JSON.version);
});

test("defaults are defensive and observe-only", () => {
  const config = resolveConfig(undefined, {}, "win32");
  assert.equal(config.enabled, true);
  assert.equal(config.mode, MODE_OBSERVE);
  assert.equal(config.timeoutMs, DEFAULT_TIMEOUT_MS);
  assert.equal(config.minRunes, 40);
  assert.equal(config.maxRunes, 200_000);
  assert.equal(config.logFile, "");
  assert.deepEqual(config.warnings, []);
  assert.ok(config.binaryPath.endsWith("crisp.exe"));
  assert.ok(config.binaryPath.startsWith(REPO_ROOT));
});

test("non-object config is tolerated", () => {
  for (const value of [null, 42, "observe", [], true]) {
    const config = resolveConfig(value, {}, "linux");
    assert.equal(config.mode, MODE_OBSERVE);
    assert.equal(config.timeoutMs, DEFAULT_TIMEOUT_MS);
  }
});

test("explicit config wins over environment", () => {
  const config = resolveConfig(
    {
      mode: "guard",
      binaryPath: "C:/tools/crisp.exe",
      timeoutMs: 5_000,
      minRunes: 10,
      logFile: "C:/logs/gate.jsonl",
    },
    { CRISP_BIN: "C:/env/crisp.exe", CRISP_DSH_MODE: "observe", CRISP_TIMEOUT_MS: "9999" },
    "win32",
  );
  assert.equal(config.mode, MODE_GUARD);
  assert.equal(config.binaryPath, "C:/tools/crisp.exe");
  assert.equal(config.timeoutMs, 5_000);
  assert.equal(config.minRunes, 10);
  assert.equal(config.logFile, "C:/logs/gate.jsonl");
});

test("dedicated environment variables are fallbacks", () => {
  const config = resolveConfig(
    undefined,
    {
      CRISP_BIN: "C:/env/crisp.exe",
      CRISP_TIMEOUT_MS: "4567",
      CRISP_DSH_MODE: "GUARD",
      CRISP_DSH_MIN_RUNES: "77",
      CRISP_DSH_LOG_FILE: "C:/tmp/gate.jsonl",
    },
    "win32",
  );
  assert.equal(config.binaryPath, "C:/env/crisp.exe");
  assert.equal(config.timeoutMs, 4567);
  assert.equal(config.mode, MODE_GUARD);
  assert.equal(config.minRunes, 77);
  assert.equal(config.logFile, "C:/tmp/gate.jsonl");
});

test("CRISP_DSH_DISABLE disables the gate, explicit enabled wins", () => {
  assert.equal(resolveConfig(undefined, { CRISP_DSH_DISABLE: "1" }, "win32").enabled, false);
  assert.equal(resolveConfig(undefined, { CRISP_DSH_DISABLE: "true" }, "win32").enabled, false);
  assert.equal(resolveConfig(undefined, { CRISP_DSH_DISABLE: "0" }, "win32").enabled, true);
  assert.equal(resolveConfig({ enabled: true }, { CRISP_DSH_DISABLE: "1" }, "win32").enabled, true);
});

test("unknown mode falls back to observe with a warning", () => {
  const config = resolveConfig({ mode: "yolo" }, {}, "win32");
  assert.equal(config.mode, MODE_OBSERVE);
  assert.ok(config.warnings.some((warning) => warning.includes("unknown mode")));
});

test("timeout and rune bounds are clamped", () => {
  assert.equal(resolveConfig({ timeoutMs: 1 }, {}, "win32").timeoutMs, MIN_TIMEOUT_MS);
  assert.equal(resolveConfig({ timeoutMs: 10_000_000 }, {}, "win32").timeoutMs, MAX_TIMEOUT_MS);
  assert.equal(resolveConfig({ timeoutMs: "nope" }, {}, "win32").timeoutMs, DEFAULT_TIMEOUT_MS);
  assert.equal(resolveConfig({ minRunes: -5 }, {}, "win32").minRunes, 0);
  assert.equal(resolveConfig({ minRunes: "abc" }, {}, "win32").minRunes, 40);
  const bounded = resolveConfig({ minRunes: 500, maxRunes: 100 }, {}, "win32");
  assert.equal(bounded.maxRunes, 500);
});

test("logFile false disables file logging without disabling the gate", () => {
  const config = resolveConfig({ logFile: false }, {}, "win32");
  assert.equal(config.logFile, false);
  assert.equal(config.enabled, true);
});

test("rewrite instruction defaults to the Go policy string", () => {
  assert.equal(
    DEFAULT_REWRITE_INSTRUCTION,
    "请重新输出完整最终回答：合并语义重复的段落，删除无效铺垫和结论重复，保留全部必要事实、数字、代码和解释，不要为了缩短篇幅而删除必要信息。",
  );
  assert.equal(resolveConfig({}, {}, "win32").rewriteInstruction, DEFAULT_REWRITE_INSTRUCTION);
  assert.equal(
    resolveConfig({ rewriteInstruction: "custom" }, {}, "win32").rewriteInstruction,
    "custom",
  );
});

test("defaultCrispBin supports checkout and PATH fallbacks", () => {
  assert.equal(
    defaultCrispBin(process.platform, REPO_ROOT, () => true),
    path.join(REPO_ROOT, "bin", process.platform === "win32" ? "crisp.exe" : "crisp"),
  );
  assert.equal(defaultCrispBin("win32", "/missing", () => false), "crisp.exe");
  assert.equal(defaultCrispBin("linux", "/missing", () => false), "crisp");
});

test("isTruthyEnv is narrow", () => {
  for (const value of ["1", "true", "YES", "on", " On "]) assert.equal(isTruthyEnv(value), true);
  for (const value of [undefined, null, "0", "false", "", "no", "off"]) {
    assert.equal(isTruthyEnv(value), false);
  }
});

test("buildChildEnv passes only allowlisted and requested variables", () => {
  const baseEnv = {
    PATH: "C:/Windows/system32",
    SystemRoot: "C:/Windows",
    DEEPSEEK_API_KEY: "super-secret",
    QUALITY_GATE_BASE_URL: "https://critic.example/v1",
    QUALITY_GATE_MODEL: "critic-model",
    HOME: "C:/Users/example",
    UNRELATED_SECRET: "nope",
  };
  const config = resolveConfig({}, baseEnv, "win32");
  const childEnv = buildChildEnv(config, baseEnv);
  assert.equal(childEnv.QUALITY_GATE_BASE_URL, "https://critic.example/v1");
  assert.equal(childEnv.QUALITY_GATE_MODEL, "critic-model");
  assert.equal(childEnv.PATH, "C:/Windows/system32");
  assert.equal(childEnv.SystemRoot, "C:/Windows");
  assert.equal(childEnv.HOME, "C:/Users/example");
  assert.equal(childEnv.DEEPSEEK_API_KEY, undefined);
  assert.equal(childEnv.UNRELATED_SECRET, undefined);
});

test("buildChildEnv supports prefix passthrough and config overrides", () => {
  const baseEnv = { QUALITY_GATE_BASE_URL: "https://a", QUALITY_GATE_MODEL: "m", KEEP_ME: "x" };
  const config = resolveConfig(
    {
      envPassthrough: ["QUALITY_GATE_*"],
      env: { QUALITY_GATE_MODE: "economy" },
    },
    baseEnv,
    "win32",
  );
  const childEnv = buildChildEnv(config, baseEnv);
  assert.equal(childEnv.QUALITY_GATE_BASE_URL, "https://a");
  assert.equal(childEnv.QUALITY_GATE_MODEL, "m");
  assert.equal(childEnv.QUALITY_GATE_MODE, "economy");
  assert.equal(childEnv.KEEP_ME, undefined);
});