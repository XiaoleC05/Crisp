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
// Minimal OpenAI-compatible chat-completions mock for opencode E2E.
// Always answers with a short, normal answer (Crisp should pass it).
const http = require("node:http");
const PORT = Number(process.env.MOCK_PORT || 19876);
const ANSWER = process.env.MOCK_ANSWER || "This is a normal short answer.";

function chunk(res, delta, finishReason) {
  const payload = {
    id: "chatcmpl-mock",
    object: "chat.completion.chunk",
    created: Math.floor(Date.now() / 1000),
    model: "mock-model",
    choices: [{ index: 0, delta, finish_reason: finishReason ?? null }],
  };
  res.write("data: " + JSON.stringify(payload) + "\n\n");
}

const server = http.createServer((req, res) => {
  let body = "";
  req.on("data", (c) => (body += c));
  req.on("end", () => {
    console.error("[mock-provider] " + req.method + " " + req.url + " body_bytes=" + body.length);
    if (req.method === "GET" && (req.url === "/v1/models" || req.url === "/models")) {
      res.writeHead(200, { "content-type": "application/json" });
      res.end(JSON.stringify({ object: "list", data: [{ id: "mock-model", object: "model" }] }));
      return;
    }
    if (req.method === "POST" && (req.url === "/v1/chat/completions" || req.url === "/chat/completions")) {
      res.writeHead(200, {
        "content-type": "text/event-stream",
        "cache-control": "no-cache",
        connection: "keep-alive",
      });
      chunk(res, { role: "assistant", content: "" });
      chunk(res, { content: ANSWER });
      chunk(res, {}, "stop");
      res.write("data: [DONE]\n\n");
      res.end();
      return;
    }
    if (req.method === "POST" && req.url.includes("/responses")) {
      // Not used by the openai-compatible provider, but answer harmlessly.
      res.writeHead(200, { "content-type": "application/json" });
      res.end(JSON.stringify({ id: "resp-mock", output: [] }));
      return;
    }
    res.writeHead(404, { "content-type": "application/json" });
    res.end(JSON.stringify({ error: { message: "not found" } }));
  });
});
server.listen(PORT, "127.0.0.1", () => console.error("[mock-provider] listening on " + PORT));
