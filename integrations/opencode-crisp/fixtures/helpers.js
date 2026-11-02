// Shared hermetic test helpers. No network, no real opencode, no real model.
import { EventEmitter } from "node:events";

/** Build a child_process.spawn-compatible fake and record every call. */
export function makeFakeSpawn({ stdout = "", stderr = "", code = 0, delay = 0 } = {}) {
  const calls = [];
  function spawn(bin, args, options) {
    const call = { bin, args, options, stdinText: undefined, killed: false };
    calls.push(call);
    const child = new EventEmitter();
    child.stdout = new EventEmitter();
    child.stderr = new EventEmitter();
    child.stdin = new EventEmitter();
    child.stdin.end = (text) => {
      call.stdinText = text;
    };
    child.kill = () => {
      call.killed = true;
      setTimeout(() => child.emit("close", null), 5);
    };
    setTimeout(() => {
      if (stdout) child.stdout.emit("data", Buffer.from(stdout));
      if (stderr) child.stderr.emit("data", Buffer.from(stderr));
      child.emit("close", code);
    }, delay);
    return child;
  }
  return { spawn, calls };
}

export const PASS_DECISION = JSON.stringify({
  decision: "pass",
  reason_type: "critic_pass",
  rule_types: ["duplicate_sentence"],
  critic_called: true,
  fail_open: false,
  answer_runes: 120,
  natural_runes: 110,
  critic_latency_ms: 5,
  critic_tokens: 12,
  elapsed_ms: 9,
});

export const BLOCK_DECISION = JSON.stringify({
  decision: "block",
  reason_type: "semantic_duplicate",
  rule_types: [],
  critic_called: true,
  fail_open: false,
  answer_runes: 300,
  natural_runes: 280,
  critic_latency_ms: 7,
  critic_tokens: 20,
  elapsed_ms: 11,
});

/** Assistant message factory matching the verified SDK Message shape. */
export function assistantMessage({
  id = "msg_assistant_1",
  sessionID = "ses_1",
  parentID = "msg_user_1",
  text = "Normal answer text.",
  completed = 1_700_000_000_000,
  ...rest
} = {}) {
  return {
    info: {
      id,
      sessionID,
      role: "assistant",
      parentID,
      mode: "build",
      providerID: "mock",
      modelID: "mock-model",
      time: { created: completed - 10, completed },
      cost: 0,
      tokens: { input: 1, output: 1, reasoning: 0, cache: { read: 0, write: 0 } },
      ...rest,
    },
    parts: [{ id: "part_1", sessionID, messageID: id, type: "text", text }],
  };
}

export function userMessage({ id = "msg_user_1", sessionID = "ses_1", text = "hello" } = {}) {
  return {
    info: { id, sessionID, role: "user", time: { created: 1 } },
    parts: [{ id: "part_u", sessionID, messageID: id, type: "text", text }],
  };
}

/** Minimal fake opencode SDK client. */
export function makeFakeClient({
  messages = [],
  session = { id: "ses_1" },
  toastImpl,
  promptImpl,
  appLogImpl,
  messagesImpl,
} = {}) {
  const calls = { toast: [], prompt: [], appLog: [], get: [], messages: [] };
  return {
    calls,
    client: {
      session: {
        async get(args) {
          calls.get.push(args);
          return { data: session };
        },
        async messages(args) {
          calls.messages.push(args);
          if (messagesImpl) return messagesImpl(args);
          return { data: messages };
        },
        async promptAsync(args) {
          calls.prompt.push(args);
          if (promptImpl) return promptImpl(args);
          return { data: undefined };
        },
      },
      tui: {
        async showToast(args) {
          calls.toast.push(args);
          if (toastImpl) return toastImpl(args);
          return { data: true };
        },
      },
      app: {
        async log(args) {
          calls.appLog.push(args);
          if (appLogImpl) return appLogImpl(args);
          return { data: true };
        },
      },
    },
  };
}
