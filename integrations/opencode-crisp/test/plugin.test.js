import test from "node:test";
import assert from "node:assert/strict";
import { existsSync } from "node:fs";

import { createCrispHooks } from "../lib/hooks.js";
import { DEFAULT_REWRITE_INSTRUCTION, defaultCrispBin } from "../lib/config.js";
import {
  BLOCK_DECISION,
  PASS_DECISION,
  assistantMessage,
  makeFakeClient,
  makeFakeSpawn,
  userMessage,
} from "../fixtures/helpers.js";

// A model of one opencode server instance: client + hooks + recorded logs.
function makePlugin({ messages, clientOptions, spawn, options = {}, deps = {} } = {}) {
  const state = { messages: messages ?? [] };
  const fake = makeFakeClient({
    messagesImpl: async () => ({ data: state.messages }),
    ...clientOptions,
  });
  const logs = [];
  const hooks = createCrispHooks(
    { client: fake.client, directory: "D:/work", worktree: "D:/work" },
    options,
    {
      spawn: spawn ?? makeFakeSpawn({ stdout: PASS_DECISION }).spawn,
      env: {},
      logger: async (line) => {
        logs.push(line);
      },
      now: () => Date.UTC(2026, 10, 2, 12, 0, 0),
      ...deps,
    },
  );
  return { hooks, fake, logs, state };
}

const idle = (sessionID = "ses_1") => ({
  event: { type: "session.idle", properties: { sessionID } },
});

test("pass: reviews the last assistant answer, logs once, no toast, no rewrite", async () => {
  const spawn = makeFakeSpawn({ stdout: PASS_DECISION });
  const { hooks, fake, logs } = makePlugin({
    messages: [userMessage(), assistantMessage({ text: "normal answer" })],
    spawn: spawn.spawn,
  });
  await hooks.event(idle());
  assert.equal(spawn.calls.length, 1);
  assert.equal(spawn.calls[0].bin, defaultCrispBin());
  assert.deepEqual(spawn.calls[0].args, ["check-text"]);
  assert.equal(spawn.calls[0].stdinText, "normal answer");
  assert.equal(fake.calls.prompt.length, 0);
  assert.equal(fake.calls.toast.length, 0);
  assert.equal(logs.length, 1);
  assert.equal(logs[0].event, "quality_check");
  assert.equal(logs[0].decision, "pass");
  assert.equal(logs[0].fail_open, false);
  assert.equal(logs[0].message_id, "msg_assistant_1");
  assert.equal(logs[0].elapsed_ms, 0);
  // no answer text leaks into the structured log line
  assert.equal(JSON.stringify(logs[0]).includes("normal answer"), false);
});

test("block: surfaces a toast and requests exactly one rewrite with the fixed instruction", async () => {
  const spawn = makeFakeSpawn({ stdout: BLOCK_DECISION });
  const { hooks, fake, logs } = makePlugin({
    messages: [userMessage(), assistantMessage({ text: "repetitive answer" })],
    spawn: spawn.spawn,
  });
  await hooks.event(idle());
  assert.equal(logs.length, 1);
  assert.equal(logs[0].decision, "block");
  assert.equal(logs[0].reason_type, "semantic_duplicate");
  assert.equal(logs[0].rewrite_requested, true);
  assert.equal(logs[0].rewrite_sent, true);
  assert.equal(logs[0].toast, true);
  assert.equal(fake.calls.toast.length, 1);
  assert.equal(fake.calls.toast[0].body.variant, "warning");
  assert.equal(fake.calls.toast[0].body.title, "Crisp quality gate");
  assert.equal(fake.calls.prompt.length, 1);
  const prompt = fake.calls.prompt[0];
  assert.equal(prompt.path.id, "ses_1");
  assert.equal(prompt.body.parts.length, 1);
  assert.equal(prompt.body.parts[0].type, "text");
  assert.equal(prompt.body.parts[0].text, DEFAULT_REWRITE_INSTRUCTION);
  assert.deepEqual(prompt.body.model, { providerID: "mock", modelID: "mock-model" });
  assert.equal(prompt.body.agent, "build");
});

test("the answer produced by the requested rewrite is not reviewed again", async () => {
  const spawn = makeFakeSpawn({ stdout: BLOCK_DECISION });
  const { hooks, fake, logs, state } = makePlugin({
    messages: [userMessage(), assistantMessage({ text: "bad answer" })],
    spawn: spawn.spawn,
  });
  await hooks.event(idle()); // block -> exactly one rewrite requested
  assert.equal(fake.calls.prompt.length, 1);
  assert.equal(spawn.calls.length, 1);

  // The rewrite turn produces a new user message + assistant message.
  state.messages = [
    ...state.messages,
    userMessage({ id: "msg_user_2" }),
    assistantMessage({ id: "msg_assistant_2", parentID: "msg_user_2", text: "rewritten answer" }),
  ];
  await hooks.event(idle());
  assert.equal(spawn.calls.length, 1); // no second review
  assert.equal(fake.calls.prompt.length, 1); // no second rewrite
  const skip = logs.find((line) => line.skip_reason === "rewrite_result");
  assert.ok(skip, "expected rewrite_result skip");
});

test("duplicate completion events review the answer only once", async () => {
  const spawn = makeFakeSpawn({ stdout: PASS_DECISION });
  const { hooks, logs } = makePlugin({
    messages: [userMessage(), assistantMessage()],
    spawn: spawn.spawn,
  });
  await hooks.event(idle());
  await hooks.event(idle());
  await hooks.event({
    event: {
      type: "message.updated",
      properties: {
        info: { role: "assistant", sessionID: "ses_1", id: "msg_assistant_1", time: { created: 1, completed: 2 } },
      },
    },
  });
  assert.equal(spawn.calls.length, 1);
  assert.equal(logs.filter((line) => line.event === "quality_check").length, 1);
  assert.equal(logs.filter((line) => line.skip_reason === "already_checked").length, 2);
});

test("rewrite:false only surfaces the toast", async () => {
  const spawn = makeFakeSpawn({ stdout: BLOCK_DECISION });
  const { hooks, fake, logs } = makePlugin({
    messages: [userMessage(), assistantMessage()],
    spawn: spawn.spawn,
    options: { rewrite: false },
  });
  await hooks.event(idle());
  assert.equal(fake.calls.prompt.length, 0);
  assert.equal(fake.calls.toast.length, 1);
  assert.equal(logs[0].rewrite_requested, false);
  assert.equal(logs[0].rewrite_skipped, "disabled");
});

test("toast:false suppresses the toast", async () => {
  const spawn = makeFakeSpawn({ stdout: BLOCK_DECISION });
  const { hooks, fake } = makePlugin({
    messages: [userMessage(), assistantMessage()],
    spawn: spawn.spawn,
    options: { toast: false },
  });
  await hooks.event(idle());
  assert.equal(fake.calls.toast.length, 0);
  assert.equal(fake.calls.prompt.length, 1);
});

test("crisp failure fails open: pass, no toast, no rewrite", async () => {
  const spawn = makeFakeSpawn({ stdout: "", stderr: "boom", code: 2 });
  const { hooks, fake, logs } = makePlugin({
    messages: [userMessage(), assistantMessage()],
    spawn: spawn.spawn,
  });
  await hooks.event(idle());
  assert.equal(logs[0].decision, "pass");
  assert.equal(logs[0].fail_open, true);
  assert.equal(logs[0].reason_type, "fail_open");
  assert.match(logs[0].error, /exited with code 2/);
  assert.equal(fake.calls.toast.length, 0);
  assert.equal(fake.calls.prompt.length, 0);
});

test("transcript fetch failure logs an error and never throws", async () => {
  const { hooks, logs } = makePlugin({
    clientOptions: {
      messagesImpl: async () => {
        throw new Error("db locked");
      },
    },
  });
  await hooks.event(idle());
  assert.equal(logs.length, 1);
  assert.equal(logs[0].event, "quality_gate_error");
  assert.match(logs[0].error, /db locked/);
});

test("subagent sessions are skipped unless includeSubagents is set", async () => {
  const spawn = makeFakeSpawn({ stdout: PASS_DECISION });
  const parent = makePlugin({
    messages: [userMessage(), assistantMessage()],
    clientOptions: { session: { id: "ses_child", parentID: "ses_parent" } },
    spawn: spawn.spawn,
  });
  await parent.hooks.event(idle());
  assert.equal(parent.fake.calls.messages.length, 0);
  assert.equal(spawn.calls.length, 0);
  assert.equal(parent.logs[0].skip_reason, "subagent_session");

  const included = makePlugin({
    messages: [userMessage(), assistantMessage()],
    clientOptions: { session: { id: "ses_child", parentID: "ses_parent" } },
    spawn: spawn.spawn,
    options: { includeSubagents: true },
  });
  await included.hooks.event(idle());
  assert.equal(included.fake.calls.messages.length, 1);
  assert.equal(spawn.calls.length, 1);
});

test("a rejected rewrite request is reported and does not break the gate", async () => {
  const spawn = makeFakeSpawn({ stdout: BLOCK_DECISION });
  const { hooks, fake, logs } = makePlugin({
    messages: [userMessage(), assistantMessage()],
    spawn: spawn.spawn,
    clientOptions: {
      promptImpl: async () => {
        throw new Error("400 invalid agent");
      },
    },
  });
  await hooks.event(idle());
  assert.equal(logs[0].rewrite_requested, true);
  assert.equal(logs[0].rewrite_sent, false);
  assert.match(logs[0].rewrite_error, /invalid agent/);
  assert.equal(fake.calls.toast.length, 1);
});

test("a failing toast does not prevent the rewrite or the log line", async () => {
  const spawn = makeFakeSpawn({ stdout: BLOCK_DECISION });
  const { hooks, fake, logs } = makePlugin({
    messages: [userMessage(), assistantMessage()],
    spawn: spawn.spawn,
    clientOptions: {
      toastImpl: async () => {
        throw new Error("no TUI");
      },
    },
  });
  await hooks.event(idle());
  assert.equal(fake.calls.prompt.length, 1);
  assert.equal(logs.length, 1);
  assert.equal(logs[0].rewrite_sent, true);
});

test("unrelated events do nothing", async () => {
  const spawn = makeFakeSpawn({ stdout: PASS_DECISION });
  const { hooks } = makePlugin({ messages: [], spawn: spawn.spawn });
  await hooks.event({ event: { type: "session.updated", properties: { info: {} } } });
  await hooks.event({});
  await hooks.event();
  assert.equal(spawn.calls.length, 0);
});

test("CRISP_OPENCODE_DISABLE returns no hooks", () => {
  const hooks = createCrispHooks(
    { client: makeFakeClient().client },
    {},
    { env: { CRISP_OPENCODE_DISABLE: "1" }, logger: async () => {} },
  );
  assert.deepEqual(hooks, {});
});

test("a throwing logger is contained", async () => {
  const spawn = makeFakeSpawn({ stdout: BLOCK_DECISION });
  const hooks = createCrispHooks(
    { client: makeFakeClient({ messages: [userMessage(), assistantMessage()] }).client },
    {},
    {
      spawn: spawn.spawn,
      env: {},
      logger: async () => {
        throw new Error("disk full");
      },
    },
  );
  await hooks.event(idle()); // must resolve, not reject
});

// Integration: the real Go binary, still hermetic (short answer => no critic).
const realBin = defaultCrispBin();
test("real crisp.exe through the plugin: pass decision is logged", { skip: !existsSync(realBin) }, async () => {
  const logs = [];
  const hooks = createCrispHooks(
    {
      client: makeFakeClient({
        messages: [userMessage(), assistantMessage({ text: "This is a normal short answer." })],
      }).client,
    },
    {},
    { env: {}, logger: async (line) => { logs.push(line); } },
  );
  await hooks.event(idle());
  assert.equal(logs.length, 1);
  assert.equal(logs[0].event, "quality_check");
  assert.equal(logs[0].decision, "pass");
  assert.equal(logs[0].fail_open, false);
  assert.equal(logs[0].answer_runes, 30);
});
