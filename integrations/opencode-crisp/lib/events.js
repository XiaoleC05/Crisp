// Pure event/message extraction. No I/O so every branch is unit-testable.

function asString(value) {
  return typeof value === "string" && value !== "" ? value : undefined;
}

/**
 * Map an opencode bus event to the session id whose final answer should be
 * reviewed, or undefined when the event is irrelevant.
 *
 * Verified event shapes (opencode 1.18.18 loader + SDK 1.17.18 types):
 *   { id, type: "session.idle",    properties: { sessionID } }
 *   { id, type: "message.updated", properties: { info: Message } }
 * An assistant message is complete when info.time.completed is set.
 */
export function eventSessionId(event) {
  if (!event || typeof event !== "object") return undefined;
  const { type, properties } = event;
  if (!properties || typeof properties !== "object") return undefined;

  if (type === "session.idle") {
    return asString(properties.sessionID);
  }

  if (type === "message.updated") {
    const info = properties.info;
    if (!info || typeof info !== "object") return undefined;
    if (info.role !== "assistant") return undefined;
    if (!asString(info.sessionID)) return undefined;
    if (!info.time || typeof info.time.completed !== "number") return undefined;
    return info.sessionID;
  }

  return undefined;
}

function isVisibleTextPart(part) {
  return (
    part &&
    part.type === "text" &&
    part.synthetic !== true &&
    part.ignored !== true &&
    typeof part.text === "string"
  );
}

/**
 * Return the final visible assistant answer from session.messages() output.
 * messages is the SDK shape: Array<{ info: Message, parts: Part[] }>.
 * Returns undefined when there is no reviewable assistant answer.
 */
export function extractFinalAnswer(messages) {
  if (!Array.isArray(messages) || messages.length === 0) return undefined;

  for (let i = messages.length - 1; i >= 0; i -= 1) {
    const entry = messages[i];
    const info = entry?.info;
    if (!info || info.role !== "assistant") continue;
    if (info.summary === true) continue; // compaction/summary assistant turns
    if (info.error) continue; // provider/auth/abort errors are not answers
    const parts = Array.isArray(entry.parts) ? entry.parts : [];
    const text = parts
      .filter(isVisibleTextPart)
      .map((part) => part.text)
      .join("\n\n")
      .trim();
    if (text === "") continue;
    return {
      sessionID: asString(info.sessionID),
      messageID: asString(info.id),
      parentID: asString(info.parentID),
      agent: asString(info.mode) ?? asString(info.agent),
      providerID: asString(info.providerID),
      modelID: asString(info.modelID),
      text,
      created: typeof info.time?.created === "number" ? info.time.created : undefined,
      completed: typeof info.time?.completed === "number" ? info.time.completed : undefined,
    };
  }

  return undefined;
}
