// Exactly-once rewrite bookkeeping, the opencode analogue of Codex's
// stop_hook_active guard (docs/design.md section 6.4).
//
// opencode has no platform-level 'this turn was already rewritten' flag, so
// the plugin tracks it locally:
//   - checked: assistant message ids already reviewed (message.updated and
//     session.idle can both fire for the same answer);
//   - pendingRewrite: after requesting one rewrite for a session, the NEXT
//     assistant message in that session is the rewrite result and is skipped;
//   - rewrittenTurns: assistant message parentID (= the user turn) values that
//     already consumed their single rewrite, so a re-check can never loop.

export const SKIP_REWRITE_RESULT = "rewrite_result";
export const SKIP_ALREADY_CHECKED = "already_checked";
export const SKIP_TURN_REWRITTEN = "turn_already_rewritten";

export class RewriteGuard {
  constructor({ maxChecked = 500 } = {}) {
    this.maxChecked = maxChecked;
    this.checked = new Set();
    this.pendingRewrite = new Map(); // sessionID -> blocked assistant messageID
    this.rewrittenTurns = new Set(); // assistant parentID (user message id)
  }

  markChecked(messageID) {
    if (!messageID) return;
    this.checked.add(messageID);
    while (this.checked.size > this.maxChecked) {
      const oldest = this.checked.values().next().value;
      this.checked.delete(oldest);
    }
  }

  isChecked(messageID) {
    return Boolean(messageID) && this.checked.has(messageID);
  }

  /**
   * Decide what to do with a candidate assistant message.
   * @returns {undefined | string} a SKIP_* reason, or undefined when the
   *   message should be reviewed and was marked checked.
   */
  classify({ sessionID, messageID, parentID }) {
    if (this.isChecked(messageID)) return SKIP_ALREADY_CHECKED;

    const pending = this.pendingRewrite.get(sessionID);
    if (pending !== undefined) {
      if (messageID === pending) return SKIP_ALREADY_CHECKED;
      // The first assistant message after a requested rewrite is the rewrite
      // result: never review or rewrite it again for this turn.
      this.pendingRewrite.delete(sessionID);
      this.markChecked(messageID);
      return SKIP_REWRITE_RESULT;
    }

    if (parentID && this.rewrittenTurns.has(parentID)) return SKIP_TURN_REWRITTEN;

    this.markChecked(messageID);
    return undefined;
  }

  /** Record that one rewrite was requested for this session/turn. */
  markRewriteSent({ sessionID, blockedMessageID, parentID }) {
    this.pendingRewrite.set(sessionID, blockedMessageID);
    if (parentID) {
      this.rewrittenTurns.add(parentID);
      while (this.rewrittenTurns.size > this.maxChecked) {
        const oldest = this.rewrittenTurns.values().next().value;
        this.rewrittenTurns.delete(oldest);
      }
    }
  }

  /** Undo markRewriteSent after the followup request itself failed. */
  cancelRewriteSent({ sessionID, parentID }) {
    this.pendingRewrite.delete(sessionID);
    if (parentID) this.rewrittenTurns.delete(parentID);
  }

  /** Whether this turn already consumed its single rewrite. */
  isTurnRewritten(parentID) {
    return Boolean(parentID) && this.rewrittenTurns.has(parentID);
  }
}
