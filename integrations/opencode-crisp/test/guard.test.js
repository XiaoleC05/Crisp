import test from "node:test";
import assert from "node:assert/strict";

import {
  RewriteGuard,
  SKIP_ALREADY_CHECKED,
  SKIP_REWRITE_RESULT,
  SKIP_TURN_REWRITTEN,
} from "../lib/rewrite-guard.js";

test("classify marks a new message checked and allows review", () => {
  const guard = new RewriteGuard();
  assert.equal(guard.classify({ sessionID: "s", messageID: "m1", parentID: "u1" }), undefined);
  assert.equal(guard.isChecked("m1"), true);
});

test("classify rejects a second review of the same message", () => {
  const guard = new RewriteGuard();
  guard.classify({ sessionID: "s", messageID: "m1", parentID: "u1" });
  assert.equal(guard.classify({ sessionID: "s", messageID: "m1", parentID: "u1" }), SKIP_ALREADY_CHECKED);
});

test("first assistant message after a requested rewrite is skipped as rewrite result", () => {
  const guard = new RewriteGuard();
  guard.classify({ sessionID: "s", messageID: "m1", parentID: "u1" });
  guard.markRewriteSent({ sessionID: "s", blockedMessageID: "m1", parentID: "u1" });
  assert.equal(guard.classify({ sessionID: "s", messageID: "m2", parentID: "u2" }), SKIP_REWRITE_RESULT);
  // and it is marked checked so a later event cannot review it either
  assert.equal(guard.classify({ sessionID: "s", messageID: "m2", parentID: "u2" }), SKIP_ALREADY_CHECKED);
});

test("a turn that already consumed its rewrite is never rewritten again", () => {
  const guard = new RewriteGuard();
  guard.classify({ sessionID: "s", messageID: "m1", parentID: "u1" });
  guard.markRewriteSent({ sessionID: "s", blockedMessageID: "m1", parentID: "u1" });
  // simulate an unrelated new message with the same parent turn (defensive)
  assert.equal(guard.isTurnRewritten("u1"), true);
  guard.pendingRewrite.delete("s");
  assert.equal(guard.classify({ sessionID: "s", messageID: "m9", parentID: "u1" }), SKIP_TURN_REWRITTEN);
});

test("cancelRewriteSent rolls back a failed followup request", () => {
  const guard = new RewriteGuard();
  guard.classify({ sessionID: "s", messageID: "m1", parentID: "u1" });
  guard.markRewriteSent({ sessionID: "s", blockedMessageID: "m1", parentID: "u1" });
  guard.cancelRewriteSent({ sessionID: "s", parentID: "u1" });
  assert.equal(guard.isTurnRewritten("u1"), false);
  assert.equal(guard.pendingRewrite.has("s"), false);
});

test("checked set is bounded", () => {
  const guard = new RewriteGuard({ maxChecked: 3 });
  for (const id of ["a", "b", "c", "d"]) guard.markChecked(id);
  assert.equal(guard.checked.has("a"), false);
  assert.equal(guard.checked.has("d"), true);
});
