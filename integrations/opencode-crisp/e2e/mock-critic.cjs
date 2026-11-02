// Hermetic E2E harness for the opencode integration.
//
// These files are the exact harness used to produce the verified E2E evidence in
// docs/integrations/opencode.md. They never touch a real opencode config, a real
// model provider or the user's home config: run them with the isolated
// HOME/XDG_* environment shown in that document.
//
//   mock-provider.cjs  OpenAI-compatible streaming chat-completions server
//   mock-critic.cjs    OpenAI-compatible critic server returning a material block
//   drive.cjs          drives a running 'opencode serve' over HTTP and reads the
//                      plugin's JSONL decision log
//
// Minimal OpenAI-compatible critic mock: always returns a material block verdict.
const http = require("node:http");
const PORT = Number(process.env.MOCK_CRITIC_PORT || 19878);
const VERDICT = JSON.stringify({
  decision: "block",
  severity: "material",
  issues: [
    {
      type: "semantic_duplicate",
      location: "paragraph_2",
      instruction: "与第一段观点重复，应合并",
    },
  ],
});

const server = http.createServer((req, res) => {
  let body = "";
  req.on("data", (c) => (body += c));
  req.on("end", () => {
    console.error("[mock-critic] " + req.method + " " + req.url + " body_bytes=" + body.length + " auth=" + Boolean(req.headers.authorization));
    res.writeHead(200, { "content-type": "application/json" });
    res.end(
      JSON.stringify({
        id: "chatcmpl-mock-critic",
        object: "chat.completion",
        created: Math.floor(Date.now() / 1000),
        model: "mock-critic",
        choices: [
          {
            index: 0,
            message: { role: "assistant", content: VERDICT },
            finish_reason: "stop",
          },
        ],
        usage: { prompt_tokens: 100, completion_tokens: 20, total_tokens: 120 },
      }),
    );
  });
});
server.listen(PORT, "127.0.0.1", () => console.error("[mock-critic] listening on " + PORT));
