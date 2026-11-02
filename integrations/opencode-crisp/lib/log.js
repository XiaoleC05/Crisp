// One structured JSON line per quality-gate event.
//
// The line never contains the answer text, the user message or the API key:
// only ids, decision metadata, counters and error strings (same data boundary
// as the Go implementation's stderr logger). It is written to
//   1. the configured JSONL file (default: <os tmpdir>/opencode-crisp/quality-gate.jsonl),
//   2. opencode's own server log via client.app.log, so it shows up in
//      opencode's log files with the 'crisp' service tag.
import fs from "node:fs";
import path from "node:path";

export function createJsonLineLogger({ client, config, fsImpl = fs, now = Date.now } = {}) {
  /** @param {object} line structured fields; ts/level are added if absent */
  return async function logLine(line) {
    const record = {
      ts: new Date(now()).toISOString(),
      level: "info",
      ...line,
    };
    const text = JSON.stringify(record);

    if (config && config.logFile) {
      try {
        await fsImpl.promises.mkdir(path.dirname(config.logFile), { recursive: true });
        await fsImpl.promises.appendFile(config.logFile, text + "\n", "utf8");
      } catch {
        // Logging must never break the hook.
      }
    }

    if (config && config.appLog !== false && client && client.app && typeof client.app.log === "function") {
      try {
        await client.app.log({
          body: {
            service: config.service || "crisp",
            level: record.level,
            message: text,
            extra: record,
          },
        });
      } catch {
        // A missing server logger must never break the hook.
      }
    }
  };
}
