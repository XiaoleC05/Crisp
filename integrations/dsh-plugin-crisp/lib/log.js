// One structured JSON line per quality-gate event, never containing the
// answer text, user content, or credentials.
//
// Destination precedence: config.logFile (append) > stderr. A failing log file
// falls back to stderr; logging failures are swallowed because they must never
// disturb an agent run. Tests inject deps.logLine to capture records.
import { appendFileSync, mkdirSync } from "node:fs";
import { dirname } from "node:path";

export const PLUGIN_ID = "crisp-quality-gate";

/**
 * Build the plugin's logger.
 *
 * @param {object} config normalized plugin config
 * @param {object} [deps]
 * @param {(record: object) => void} [deps.logLine] capture sink (tests)
 * @param {() => Date} [deps.now] clock (tests)
 * @returns {(record: object) => void} logger
 */
export function createLogger(config, deps = {}) {
  const sink = typeof deps.logLine === "function" ? deps.logLine : undefined;
  const now = typeof deps.now === "function" ? deps.now : () => new Date();

  return function log(record) {
    let line;
    try {
      line = { ts: now().toISOString(), level: "info", plugin: PLUGIN_ID, ...record };
    } catch {
      return;
    }
    if (sink !== undefined) {
      try {
        sink(line);
      } catch {
        // A capture sink must not break the gate either.
      }
      return;
    }
    let text;
    try {
      text = JSON.stringify(line);
    } catch {
      return;
    }
    if (typeof config?.logFile === "string" && config.logFile !== "") {
      try {
        mkdirSync(dirname(config.logFile), { recursive: true });
        appendFileSync(config.logFile, text + "\n", "utf8");
        return;
      } catch {
        // Fall through to stderr below.
      }
    }
    try {
      process.stderr.write(text + "\n");
    } catch {
      // stderr can be closed; logging is best effort.
    }
  };
}
