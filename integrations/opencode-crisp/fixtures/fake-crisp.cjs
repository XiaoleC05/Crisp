// Fake `crisp check-text` used by the real-spawn integration tests.
// Behaviour comes from FAKE_CRISP_MODE: pass (default) | block | crash.
let text = "";
process.stdin.on("data", (chunk) => {
  text += chunk.toString("utf8");
});
process.stdin.on("end", () => {
  const mode = process.env.FAKE_CRISP_MODE || "pass";
  if (mode === "crash") {
    process.stderr.write("fake crisp crashed\n");
    process.exit(2);
  }
  const runes = Array.from(text).length;
  process.stdout.write(
    JSON.stringify({
      decision: mode === "block" ? "block" : "pass",
      reason_type: mode === "block" ? "semantic_duplicate" : "critic_pass",
      rule_types: [],
      critic_called: true,
      fail_open: false,
      answer_runes: runes,
      natural_runes: runes,
      critic_latency_ms: 1,
      critic_tokens: 2,
      elapsed_ms: 2,
    }) + "\n",
  );
  process.exit(0);
});
