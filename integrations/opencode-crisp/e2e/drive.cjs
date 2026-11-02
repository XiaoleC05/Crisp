// Drive a running 'opencode serve' over HTTP: create session, prompt it, then
// report the Crisp plugin's structured log lines.
const fs = require("node:fs");
const BASE = process.env.OC_BASE || "http://127.0.0.1:19877";
const PROMPT = process.env.OC_PROMPT || "Say a short sentence.";
const CRISP_LOG = process.env.CRISP_LOG || "";
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function waitReady() {
  for (let i = 0; i < 240; i++) {
    try {
      const res = await fetch(BASE + "/session");
      if (res.ok) return;
    } catch {}
    await sleep(250);
  }
  throw new Error("opencode serve did not become ready");
}

async function main() {
  await waitReady();
  console.log("server ready");

  const created = await fetch(BASE + "/session", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ title: "crisp-e2e" }),
  });
  if (!created.ok) throw new Error("create session failed: " + created.status + " " + (await created.text()));
  const session = await created.json();
  console.log("session:", session.id);

  const prompt = await fetch(BASE + "/session/" + session.id + "/message", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      model: { providerID: "mock", modelID: "mock-model" },
      parts: [{ type: "text", text: PROMPT }],
    }),
  });
  const promptBody = await prompt.text();
  console.log("prompt status:", prompt.status);
  let assistantText = "";
  try {
    const parsed = JSON.parse(promptBody);
    const textParts = (parsed.parts || []).filter((p) => p.type === "text").map((p) => p.text);
    assistantText = textParts.join("");
  } catch {}
  console.log("assistant text:", JSON.stringify(assistantText));

  // Give the (fire-and-forget) plugin event hook time to finish.
  await sleep(3000);

  if (CRISP_LOG && fs.existsSync(CRISP_LOG)) {
    const lines = fs.readFileSync(CRISP_LOG, "utf8").trim().split("\n").filter(Boolean);
    console.log("crisp log lines:", lines.length);
    for (const line of lines) {
      const obj = JSON.parse(line);
      console.log(
        "LOG " +
          JSON.stringify({
            event: obj.event,
            decision: obj.decision,
            reason_type: obj.reason_type,
            fail_open: obj.fail_open,
            answer_runes: obj.answer_runes,
            message_id: obj.message_id,
            skip_reason: obj.skip_reason,
            rewrite_requested: obj.rewrite_requested,
          }),
      );
    }
  } else {
    console.log("NO CRISP LOG at", CRISP_LOG);
  }
}

main().then(
  () => process.exit(0),
  (err) => {
    console.error("E2E FAILED:", err && err.message ? err.message : err);
    process.exit(1);
  },
);
