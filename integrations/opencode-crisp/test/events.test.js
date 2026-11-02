import test from "node:test";
import assert from "node:assert/strict";

import { eventSessionId, extractFinalAnswer } from "../lib/events.js";

test("session.idle maps to its session", () => {
  assert.equal(eventSessionId({ type: "session.idle", properties: { sessionID: "ses_1" } }), "ses_1");
});

test("completed assistant message.updated maps to its session", () => {
  const event = {
    type: "message.updated",
    properties: {
      info: { role: "assistant", sessionID: "ses_2", time: { created: 1, completed: 2 } },
    },
  };
  assert.equal(eventSessionId(event), "ses_2");
});

test("irrelevant events are ignored", () => {
  assert.equal(eventSessionId(undefined), undefined);
  assert.equal(eventSessionId({}), undefined);
  assert.equal(eventSessionId({ type: "session.status", properties: { sessionID: "s" } }), undefined);
  // incomplete assistant message
  assert.equal(
    eventSessionId({
      type: "message.updated",
      properties: { info: { role: "assistant", sessionID: "s", time: { created: 1 } } },
    }),
    undefined,
  );
  // user messages are not answers
  assert.equal(
    eventSessionId({
      type: "message.updated",
      properties: { info: { role: "user", sessionID: "s", time: { created: 1 } } },
    }),
    undefined,
  );
  assert.equal(eventSessionId({ type: "message.updated", properties: {} }), undefined);
});

test("extractFinalAnswer returns the last visible assistant answer", () => {
  const messages = [
    { info: { role: "user", id: "u1", sessionID: "s", time: { created: 1 } }, parts: [] },
    {
      info: { role: "assistant", id: "m1", sessionID: "s", parentID: "u1", mode: "build",
        providerID: "p", modelID: "m", time: { created: 2, completed: 3 } },
      parts: [{ type: "text", text: "first answer" }],
    },
    {
      info: { role: "assistant", id: "m2", sessionID: "s", parentID: "u2", mode: "plan",
        providerID: "p2", modelID: "m2", time: { created: 4, completed: 5 } },
      parts: [{ type: "text", text: "second answer" }],
    },
  ];
  const answer = extractFinalAnswer(messages);
  assert.equal(answer.messageID, "m2");
  assert.equal(answer.parentID, "u2");
  assert.equal(answer.agent, "plan");
  assert.equal(answer.providerID, "p2");
  assert.equal(answer.text, "second answer");
});

test("extractFinalAnswer joins visible text parts and skips synthetic/ignored ones", () => {
  const messages = [
    {
      info: { role: "assistant", id: "m1", sessionID: "s", parentID: "u1", time: { created: 1, completed: 2 } },
      parts: [
        { type: "text", text: "part one" },
        { type: "text", text: "synthetic tool narration", synthetic: true },
        { type: "text", text: "ignored text", ignored: true },
        { type: "reasoning", text: "thinking" },
        { type: "text", text: "part two" },
      ],
    },
  ];
  assert.equal(extractFinalAnswer(messages).text, "part one\n\npart two");
});

test("extractFinalAnswer skips error, summary and empty answers", () => {
  const err = { info: { role: "assistant", id: "m1", sessionID: "s", error: { name: "x" },
    time: { created: 1, completed: 2 } }, parts: [{ type: "text", text: "boom" }] };
  const summary = { info: { role: "assistant", id: "m2", sessionID: "s", summary: true,
    time: { created: 1, completed: 2 } }, parts: [{ type: "text", text: "summary" }] };
  const empty = { info: { role: "assistant", id: "m3", sessionID: "s", time: { created: 1, completed: 2 } }, parts: [] };
  assert.equal(extractFinalAnswer([err, summary, empty]), undefined);
  assert.equal(extractFinalAnswer([]), undefined);
  assert.equal(extractFinalAnswer(undefined), undefined);
});
