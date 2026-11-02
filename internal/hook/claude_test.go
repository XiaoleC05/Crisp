package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// claudeStopPayload is the Stop hook payload shape Claude Code sends. It is
// deliberately not a copy of the Codex payload: there is no turn_id, and
// Claude Code adds fields the gate ignores (cwd, permission_mode, model,
// background_tasks, session_crons).
const claudeStopPayload = `{
  "session_id": "4f7a2b0e-6b3d-4b7f-9f0e-2d4a6c8e1b5a",
  "transcript_path": "C:\\Users\\user\\.claude\\projects\\D--05-Code-Crisp\\4f7a2b0e.jsonl",
  "cwd": "D:\\05_Code\\Crisp",
  "permission_mode": "default",
  "hook_event_name": "Stop",
  "stop_hook_active": false,
  "last_assistant_message": "这是一段足够长的正常回答，用于验证 Claude Code Stop hook 的兼容性。",
  "model": "claude-sonnet-4-5",
  "background_tasks": [],
  "session_crons": []
}`

func parseClaude(t *testing.T, payload string) Input {
	t.Helper()
	in, err := Parse(strings.NewReader(payload))
	if err != nil {
		t.Fatalf("Parse Claude stop payload: %v", err)
	}
	return in
}

func TestParseClaudeStopPayload(t *testing.T) {
	in := parseClaude(t, claudeStopPayload)
	if in.SessionID != "4f7a2b0e-6b3d-4b7f-9f0e-2d4a6c8e1b5a" {
		t.Fatalf("unexpected session_id %q", in.SessionID)
	}
	if in.TurnID != "" {
		t.Fatalf("Claude payload has no turn_id, got %q", in.TurnID)
	}
	if in.HookEventName != "Stop" {
		t.Fatalf("unexpected hook_event_name %q", in.HookEventName)
	}
	if in.TranscriptPath == nil || !strings.HasSuffix(*in.TranscriptPath, ".jsonl") {
		t.Fatalf("unexpected transcript_path: %+v", in.TranscriptPath)
	}
	if in.StopHookActive == nil || *in.StopHookActive {
		t.Fatalf("unexpected stop_hook_active: %+v", in.StopHookActive)
	}
	if in.LastAssistantMessage == nil || strings.TrimSpace(*in.LastAssistantMessage) == "" {
		t.Fatalf("unexpected last_assistant_message: %+v", in.LastAssistantMessage)
	}
	// A full Claude payload is not a protocol anomaly. In particular, the
	// missing Codex-only turn_id must not be reported: it would log a warning
	// on every single Claude Stop.
	if warnings := in.Validate(); len(warnings) != 0 {
		t.Fatalf("full Claude payload should validate cleanly, got %v", warnings)
	}
}

func TestParseClaudeMissingOptionalFields(t *testing.T) {
	// Minimal payload: no transcript_path, no turn_id, no last_assistant_message.
	in, err := Parse(strings.NewReader(`{"session_id":"s-1","hook_event_name":"Stop","stop_hook_active":false}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if in.TranscriptPath != nil || in.LastAssistantMessage != nil || in.TurnID != "" {
		t.Fatalf("expected nil/empty optional fields: %+v", in)
	}
	warnings := in.Validate()
	if len(warnings) != 1 || warnings[0] != "missing last_assistant_message" {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
}

func TestParseClaudeNullTranscriptPath(t *testing.T) {
	in := parseClaude(t, `{
  "session_id": "s-2",
  "transcript_path": null,
  "cwd": "D:\\05_Code\\Crisp",
  "hook_event_name": "Stop",
  "stop_hook_active": false,
  "last_assistant_message": "answer"
}`)
	if in.TranscriptPath != nil {
		t.Fatalf("explicit null transcript_path should decode to nil, got %q", *in.TranscriptPath)
	}
	if warnings := in.Validate(); len(warnings) != 0 {
		t.Fatalf("null transcript_path is valid (documented for ephemeral sessions), got warnings %v", warnings)
	}
	if got := ExtractLastUserMessage(""); got != "" {
		t.Fatalf("empty transcript path must extract nothing, got %q", got)
	}
}

func TestParseClaudeUnknownExtraFieldsIgnored(t *testing.T) {
	payload := `{
  "session_id": "s-3",
  "transcript_path": null,
  "cwd": "D:\\05_Code\\Crisp",
  "hook_event_name": "Stop",
  "stop_hook_active": false,
  "last_assistant_message": "answer",
  "permission_mode": "acceptEdits",
  "turn_id": null,
  "future_field": {"nested": [1, 2, {"deep": true}]},
  "background_tasks": [{"type": "subagent", "id": "x"}],
  "session_crons": [],
  "agent_id": "agent-1",
  "agent_type": "general-purpose",
  "stop_reason": "end_turn"
}`
	in := parseClaude(t, payload)
	if in.SessionID != "s-3" || in.StopHookActive == nil || *in.StopHookActive {
		t.Fatalf("unexpected parse result: %+v", in)
	}
	if warnings := in.Validate(); len(warnings) != 0 {
		t.Fatalf("unknown extra fields must not produce warnings, got %v", warnings)
	}
}

func TestParseClaudeStopHookActiveTrue(t *testing.T) {
	payload := strings.Replace(claudeStopPayload, `"stop_hook_active": false`, `"stop_hook_active": true`, 1)
	in := parseClaude(t, payload)
	if in.StopHookActive == nil || !*in.StopHookActive {
		t.Fatalf("stop_hook_active should be true: %+v", in.StopHookActive)
	}
	// The rewrite guard is applied by the command layer; the parser must only
	// preserve the tri-state (nil vs false vs true).
	if warnings := in.Validate(); len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
}

func TestParseClaudeMalformedInputs(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"whitespace":    "\r\n\t ",
		"truncated":     `{"session_id":"s","hook_event_name":"Stop"`,
		"invalid json":  `{"session_id":}`,
		"array":         `[{"session_id":"s"}]`,
		"string scalar": `"Stop"`,
		"number scalar": `42`,
		"trailing data": `{"session_id":"s"} {}`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(input)); err == nil {
				t.Fatalf("expected a parse error for %s, got nil", name)
			}
		})
	}
}

func TestParseClaudeJSONNull(t *testing.T) {
	// encoding/json decodes a literal null into the struct without an error and
	// leaves the zero value. That is not a crash: the command layer fails open
	// because stop_hook_active is absent (see cmd/crisp). Document the edge
	// case here so a future strict top-level-object check does not silently
	// change behavior unnoticed.
	in, err := Parse(strings.NewReader("null"))
	if err != nil {
		t.Fatalf("current parser accepts JSON null as a zero payload: %v", err)
	}
	warnings := in.Validate()
	if len(warnings) != 3 {
		t.Fatalf("expected the 3 missing-field warnings, got %v", warnings)
	}
}

func TestWriteBlockMatchesClaudeStopContract(t *testing.T) {
	var buf bytes.Buffer
	const reason = "请重新输出完整最终回答：合并语义重复的段落。"
	if err := WriteBlock(&buf, reason); err != nil {
		t.Fatalf("WriteBlock: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(buf.Bytes(), &raw); err != nil {
		t.Fatalf("block output is not a JSON object: %v", err)
	}
	if len(raw) != 2 || raw["decision"] != "block" || raw["reason"] != reason {
		t.Fatalf("Claude Stop expects exactly decision+reason for a block, got %v", raw)
	}
	// Claude Code has no hookSpecificOutput for Stop; emitting one would be a
	// protocol mistake.
	if _, ok := raw["hookSpecificOutput"]; ok {
		t.Fatalf("Stop output must not contain hookSpecificOutput: %v", raw)
	}
}

// claudeTranscript builds a synthetic Claude Code transcript. Real transcripts
// interleave real prompts, assistant turns, tool results (top-level type
// "user" + toolUseResult), sidechain entries and CLI meta records.
func claudeTranscript(t *testing.T, lastRealPrompt string) string {
	t.Helper()
	lines := []string{
		`{"type":"summary","summary":"earlier context"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"最初的问题"}]},"sessionId":"s","isSidechain":false}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"先读一下文件"}]},"sessionId":"s"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"SECRET TOOL OUTPUT"}]},"toolUseResult":{"stdout":"SECRET TOOL OUTPUT"},"sessionId":"s","isSidechain":false}`,
		`{"type":"attachment","attachment":{"type":"file"},"sessionId":"s"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"sidechain prompt"}]},"sessionId":"s","isSidechain":true}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"<local-command-stdout>meta</local-command-stdout>"}]},"sessionId":"s","isMeta":true}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"` + lastRealPrompt + `"}]},"sessionId":"s","isSidechain":false,"promptSource":"user"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"最终回答"}]},"sessionId":"s"}`,
	}
	return strings.Join(lines, "\n")
}

func TestExtractLastUserMessageClaudeTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-transcript.jsonl")
	if err := os.WriteFile(path, []byte(claudeTranscript(t, "最新问题：帮我修一下这个 bug")), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	got := ExtractLastUserMessage(path)
	if got != "最新问题：帮我修一下这个 bug" {
		t.Fatalf("expected the latest real prompt, got %q", got)
	}
	if strings.Contains(got, "SECRET TOOL OUTPUT") {
		t.Fatalf("tool output must not be forwarded as the user message: %q", got)
	}
}

func TestExtractLastUserMessageClaudeToolResultOnly(t *testing.T) {
	// A turn that ends right after a tool result has no user prompt in the read
	// window: extract nothing rather than tool output (data boundary).
	path := filepath.Join(t.TempDir(), "claude-tool-only.jsonl")
	content := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"PRIVATE FILE CONTENT"}]},"toolUseResult":{"stdout":"PRIVATE FILE CONTENT"},"sessionId":"s"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"看到了"}]},"sessionId":"s"}`,
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if got := ExtractLastUserMessage(path); got != "" {
		t.Fatalf("expected empty user context for a tool-result-only tail, got %q", got)
	}
}

func TestExtractLastUserMessageClaudeStringContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-string.jsonl")
	content := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"字符串形式的问题"},"sessionId":"s"}`,
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if got := ExtractLastUserMessage(path); got != "字符串形式的问题" {
		t.Fatalf("unexpected user message %q", got)
	}
}
