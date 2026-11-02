import test from "node:test";
import assert from "node:assert/strict";
import { spawn as realSpawn } from "node:child_process";
import { EventEmitter } from "node:events";
import { existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { parseDecision, runCrispCheck } from "../lib/crisp.js";
import { BLOCK_DECISION, PASS_DECISION, makeFakeSpawn } from "../fixtures/helpers.js";
import { defaultCrispBin } from "../lib/config.js";

const FIXTURE = fileURLToPath(new URL("../fixtures/fake-crisp.cjs", import.meta.url));

test("parseDecision accepts pass and block summaries", () => {
  assert.equal(parseDecision(PASS_DECISION).decision, "pass");
  const block = parseDecision(BLOCK_DECISION);
  assert.equal(block.decision, "block");
  assert.equal(block.reasonType, "semantic_duplicate");
  assert.deepEqual(block.ruleTypes, []);
  assert.equal(block.criticCalled, true);
  assert.equal(block.failOpen, false);
});

test("parseDecision rejects empty, invalid and unknown decisions", () => {
  assert.throws(() => parseDecision(""));
  assert.throws(() => parseDecision("not json"));
  assert.throws(() => parseDecision("[]"));
  assert.throws(() => parseDecision(JSON.stringify({ decision: "maybe" })));
});

test("pass result: invokes <bin> check-text with stdin text and inherited env", async () => {
  const fake = makeFakeSpawn({ stdout: PASS_DECISION });
  const result = await runCrispCheck({
    binPath: "C:\\tools\\crisp.exe",
    text: "answer body",
    env: { QUALITY_GATE_BASE_URL: "http://127.0.0.1:9/v1", PATH: "p" },
    timeoutMs: 5000,
    spawn: fake.spawn,
    now: (() => { let t = 0; return () => (t += 3); })(),
  });
  assert.equal(result.ok, true);
  assert.equal(result.decision, "pass");
  assert.equal(result.failOpen, false);
  assert.equal(fake.calls.length, 1);
  assert.equal(fake.calls[0].bin, "C:\\tools\\crisp.exe");
  assert.deepEqual(fake.calls[0].args, ["check-text"]);
  assert.equal(fake.calls[0].stdinText, "answer body");
  assert.equal(fake.calls[0].options.env.QUALITY_GATE_BASE_URL, "http://127.0.0.1:9/v1");
  assert.equal(result.summary.answerRunes, 120);
});

test("block result is propagated", async () => {
  const fake = makeFakeSpawn({ stdout: BLOCK_DECISION });
  const result = await runCrispCheck({ binPath: "crisp", text: "x", spawn: fake.spawn, env: {} });
  assert.equal(result.ok, true);
  assert.equal(result.decision, "block");
  assert.equal(result.summary.reasonType, "semantic_duplicate");
});

test("crisp stderr is captured but does not affect a good result", async () => {
  const fake = makeFakeSpawn({ stdout: PASS_DECISION, stderr: "quality_check {}\n" });
  const result = await runCrispCheck({ binPath: "crisp", text: "x", spawn: fake.spawn, env: {} });
  assert.equal(result.ok, true);
  assert.match(result.stderr, /quality_check/);
});

test("timeout kills the child and fails open", async () => {
  const fake = makeFakeSpawn({ stdout: PASS_DECISION, delay: 500 });
  const result = await runCrispCheck({
    binPath: "crisp",
    text: "x",
    spawn: fake.spawn,
    env: {},
    timeoutMs: 20,
  });
  assert.equal(result.ok, false);
  assert.equal(result.decision, "pass");
  assert.equal(result.failOpen, true);
  assert.equal(result.timedOut, true);
  assert.match(result.error, /timeout/);
  assert.equal(fake.calls[0].killed, true);
});

test("non-zero exit fails open", async () => {
  const fake = makeFakeSpawn({ stdout: "", stderr: "boom", code: 2 });
  const result = await runCrispCheck({ binPath: "crisp", text: "x", spawn: fake.spawn, env: {} });
  assert.equal(result.decision, "pass");
  assert.equal(result.failOpen, true);
  assert.equal(result.exitCode, 2);
  assert.match(result.error, /exited with code 2/);
});

test("invalid JSON output fails open", async () => {
  const fake = makeFakeSpawn({ stdout: "<html>oops</html>" });
  const result = await runCrispCheck({ binPath: "crisp", text: "x", spawn: fake.spawn, env: {} });
  assert.equal(result.decision, "pass");
  assert.equal(result.failOpen, true);
  assert.match(result.error, /invalid crisp output/);
});

test("spawn error event fails open", async () => {
  const spawn = (bin, args, opts) => {
    const child = new EventEmitter();
    child.stdout = new EventEmitter();
    child.stderr = new EventEmitter();
    child.stdin = new EventEmitter();
    child.stdin.end = () => {};
    child.kill = () => {};
    setImmediate(() => child.emit("error", new Error("ENOENT")));
    return child;
  };
  const result = await runCrispCheck({ binPath: "missing-crisp", text: "x", spawn, env: {} });
  assert.equal(result.decision, "pass");
  assert.equal(result.failOpen, true);
  assert.match(result.error, /ENOENT/);
});

test("synchronous spawn throw fails open", async () => {
  const spawn = () => {
    throw new Error("EACCES");
  };
  const result = await runCrispCheck({ binPath: "denied", text: "x", spawn, env: {} });
  assert.equal(result.failOpen, true);
  assert.match(result.error, /EACCES/);
});

test("real spawn: fake crisp binary receives stdin and its JSON is parsed", async () => {
  const spawnImpl = (bin, args, opts) => realSpawn(process.execPath, [FIXTURE, ...args], opts);
  const pass = await runCrispCheck({
    binPath: "node",
    text: "real answer body",
    spawn: spawnImpl,
    env: { ...process.env, FAKE_CRISP_MODE: "pass" },
  });
  assert.equal(pass.ok, true);
  assert.equal(pass.decision, "pass");
  assert.equal(pass.summary.answerRunes, "real answer body".length);

  const block = await runCrispCheck({
    binPath: "node",
    text: "bad answer",
    spawn: spawnImpl,
    env: { ...process.env, FAKE_CRISP_MODE: "block" },
  });
  assert.equal(block.decision, "block");
});

test("real spawn: crashing fake crisp fails open", async () => {
  const spawnImpl = (bin, args, opts) => realSpawn(process.execPath, [FIXTURE, ...args], opts);
  const result = await runCrispCheck({
    binPath: "node",
    text: "x",
    spawn: spawnImpl,
    env: { ...process.env, FAKE_CRISP_MODE: "crash" },
  });
  assert.equal(result.failOpen, true);
  assert.equal(result.decision, "pass");
  assert.match(result.stderr, /fake crisp crashed/);
});

// Optional: exercise the real Go binary built at bin/crisp.exe (no critic => no network).
const realBin = defaultCrispBin();
test("real crisp.exe: short pass-through answer is a pass", { skip: !existsSync(realBin) }, async () => {
  const result = await runCrispCheck({
    binPath: realBin,
    text: "This is a normal short answer.",
    env: {},
  });
  assert.equal(result.ok, true);
  assert.equal(result.decision, "pass");
  assert.equal(result.failOpen, false);
  assert.equal(typeof result.summary.answerRunes, "number");
});
