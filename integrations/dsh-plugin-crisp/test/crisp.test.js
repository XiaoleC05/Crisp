import assert from "node:assert/strict";
import { test } from "node:test";

import { resolveConfig } from "../lib/config.js";
import {
  checkAnswer,
  parseDecision,
  spawnCheckTextRunner,
} from "../lib/crisp.js";
import { createChildFactory, createFakeRunner, rawBlock, rawFailure, rawPass, tick } from "./helpers.js";

test("parseDecision accepts the check-text summary", () => {
  const summary = parseDecision(
    JSON.stringify({
      decision: "block",
      reason_type: "semantic_duplicate",
      rule_types: ["duplicate_paragraph", 42, "filler_opening"],
      critic_called: true,
      fail_open: false,
      answer_runes: 812,
      natural_runes: 764,
      critic_latency_ms: 1250,
      critic_tokens: 320,
      elapsed_ms: 1400,
    }),
  );
  assert.equal(summary.decision, "block");
  assert.equal(summary.reasonType, "semantic_duplicate");
  assert.deepEqual(summary.ruleTypes, ["duplicate_paragraph", "filler_opening"]);
  assert.equal(summary.criticCalled, true);
  assert.equal(summary.failOpen, false);
  assert.equal(summary.answerRunes, 812);
  assert.equal(summary.naturalRunes, 764);
  assert.equal(summary.criticLatencyMs, 1250);
  assert.equal(summary.criticTokens, 320);
  assert.equal(summary.crispElapsedMs, 1400);
});

test("parseDecision tolerates optional fields but rejects ambiguity", () => {
  const minimal = parseDecision(JSON.stringify({ decision: "pass" }));
  assert.equal(minimal.decision, "pass");
  assert.deepEqual(minimal.ruleTypes, []);
  assert.equal(minimal.answerRunes, undefined);

  const rejected = [
    "",
    "   ",
    "not json",
    "[]",
    "null",
    '{"decision":"maybe"}',
    '{"decision":42}',
    '{"reason_type":"x"}',
  ];
  for (const stdout of rejected) {
    assert.throws(() => parseDecision(stdout), undefined, "should reject: " + stdout);
  }
});

test("checkAnswer returns the parsed decision and forwards the exact answer text", async () => {
  const runner = createFakeRunner(rawBlock());
  const config = resolveConfig({ timeoutMs: 1234 }, {}, "win32");
  const result = await checkAnswer({ text: "answer body", config, runner });
  assert.equal(result.ok, true);
  assert.equal(result.decision, "block");
  assert.equal(result.failOpen, false);
  assert.equal(result.reasonType, "semantic_duplicate");
  assert.deepEqual(result.ruleTypes, ["duplicate_paragraph"]);
  assert.equal(runner.calls.length, 1);
  assert.equal(runner.calls[0].input, "answer body");
  assert.equal(runner.calls[0].binaryPath, config.binaryPath);
  assert.deepEqual(runner.calls[0].args, ["check-text"]);
  assert.equal(runner.calls[0].timeoutMs, 1234);
  assert.equal(typeof runner.calls[0].env, "object");
});

test("checkAnswer passes only the filtered child environment", async () => {
  const runner = createFakeRunner(rawPass());
  const env = {
    PATH: "C:/Windows/system32",
    DEEPSEEK_API_KEY: "secret",
    QUALITY_GATE_BASE_URL: "https://critic.example/v1",
  };
  const config = resolveConfig({}, env, "win32");
  await checkAnswer({ text: "text", config, runner, env });
  const childEnv = runner.calls[0].env;
  assert.equal(childEnv.QUALITY_GATE_BASE_URL, "https://critic.example/v1");
  assert.equal(childEnv.PATH, "C:/Windows/system32");
  assert.equal(childEnv.DEEPSEEK_API_KEY, undefined);
});

test("checkAnswer fails open on every failure mode", async () => {
  const config = resolveConfig({}, {}, "win32");
  const cases = [
    ["runner throws", createFakeRunner(() => { throw new Error("boom"); }), "runner threw"],
    ["spawn error", createFakeRunner(rawFailure({ errorMessage: "ENOENT" })), "ENOENT"],
    ["timeout", createFakeRunner(rawFailure({ timedOut: true, errorMessage: "timeout" })), "timeout"],
    ["nonzero exit", createFakeRunner(rawFailure({ exitCode: 3 })), "exited with code 3"],
    ["invalid json", createFakeRunner(rawFailure({ stdout: "oops" })), "invalid crisp output"],
    ["empty stdout", createFakeRunner(rawFailure({ stdout: "" })), "invalid crisp output"],
    ["unknown decision", createFakeRunner(rawFailure({ stdout: '{"decision":"wut"}' })), "invalid crisp output"],
  ];
  for (const [name, runner, expected] of cases) {
    const result = await checkAnswer({ text: "answer", config, runner });
    assert.equal(result.ok, false, name);
    assert.equal(result.decision, "pass", name);
    assert.equal(result.failOpen, true, name);
    assert.match(result.error, new RegExp(expected.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")), name);
  }
});

test("spawnCheckTextRunner spawns crisp check-text and returns bounded output", async () => {
  const factory = createChildFactory((child) => {
    child.stdout.emit("data", Buffer.from(JSON.stringify({ decision: "pass" })));
    child.stderr.emit("data", Buffer.from("some diagnostic"));
    child.emit("close", 0);
  });
  const result = await spawnCheckTextRunner({
    binaryPath: "C:/tools/crisp.exe",
    input: "hello answer",
    env: { PATH: "x" },
    timeoutMs: 5_000,
    spawn: factory.spawn,
  });
  assert.equal(result.exitCode, 0);
  assert.equal(result.timedOut, false);
  assert.equal(result.errorMessage, undefined);
  assert.deepEqual(JSON.parse(result.stdout), { decision: "pass" });
  assert.equal(result.stderr, "some diagnostic");
  assert.equal(factory.children.length, 1);
  assert.equal(factory.children[0].binaryPath, "C:/tools/crisp.exe");
  assert.deepEqual(factory.children[0].args, ["check-text"]);
  assert.equal(factory.children[0].stdinData, "hello answer");
  assert.deepEqual(factory.children[0].options.env, { PATH: "x" });
  assert.equal(factory.children[0].options.windowsHide, true);
});

test("spawnCheckTextRunner reports spawn errors without throwing", async () => {
  const result = await spawnCheckTextRunner({
    binaryPath: "missing.exe",
    input: "x",
    timeoutMs: 1_000,
    spawn: () => {
      throw new Error("spawn ENOENT");
    },
  });
  assert.equal(result.errorMessage, "spawn failed: spawn ENOENT");
  assert.equal(result.exitCode, null);
});

test("spawnCheckTextRunner reports nonzero exits and stderr", async () => {
  const factory = createChildFactory((child) => {
    child.stderr.emit("data", Buffer.from("bad things"));
    child.emit("close", 7);
  });
  const result = await spawnCheckTextRunner({
    binaryPath: "crisp.exe",
    input: "x",
    timeoutMs: 1_000,
    spawn: factory.spawn,
  });
  assert.equal(result.exitCode, 7);
  assert.equal(result.stderr, "bad things");
  assert.equal(result.errorMessage, undefined);
});

test("spawnCheckTextRunner enforces the timeout by killing the child", async () => {
  const factory = createChildFactory(() => {
    // Never closes on its own; only the timeout kills it.
  });
  const result = await spawnCheckTextRunner({
    binaryPath: "crisp.exe",
    input: "x",
    timeoutMs: 10,
    spawn: factory.spawn,
  });
  await tick();
  assert.equal(result.timedOut, true);
  assert.equal(result.errorMessage, "timeout");
  assert.equal(factory.children[0].killed, true);
});

test("spawnCheckTextRunner reports child process errors", async () => {
  const factory = createChildFactory((child) => {
    child.emit("error", new Error("EACCES"));
  });
  const result = await spawnCheckTextRunner({
    binaryPath: "crisp.exe",
    input: "x",
    timeoutMs: 1_000,
    spawn: factory.spawn,
  });
  assert.equal(result.errorMessage, "EACCES");
});