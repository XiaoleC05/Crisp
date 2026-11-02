package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseValid(t *testing.T) {
	input := `{
  "session_id": "sess-1",
  "turn_id": "turn-1",
  "transcript_path": "/tmp/transcript.jsonl",
  "stop_hook_active": false,
  "last_assistant_message": "final answer",
  "hook_event_name": "Stop"
}`
	in, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if in.SessionID != "sess-1" || in.TurnID != "turn-1" {
		t.Fatalf("unexpected ids: %+v", in)
	}
	if in.TranscriptPath == nil || *in.TranscriptPath != "/tmp/transcript.jsonl" {
		t.Fatalf("unexpected transcript path: %+v", in.TranscriptPath)
	}
	if in.StopHookActive == nil || *in.StopHookActive {
		t.Fatalf("unexpected stop_hook_active: %+v", in.StopHookActive)
	}
	if in.LastAssistantMessage == nil || *in.LastAssistantMessage != "final answer" {
		t.Fatalf("unexpected last assistant message: %+v", in.LastAssistantMessage)
	}
	if in.HookEventName != "Stop" {
		t.Fatalf("unexpected hook event name: %q", in.HookEventName)
	}
}

func TestParseMissingOptionalFields(t *testing.T) {
	in, err := Parse(strings.NewReader(`{"stop_hook_active": true}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if in.StopHookActive == nil || !*in.StopHookActive {
		t.Fatalf("stop_hook_active should be true: %+v", in.StopHookActive)
	}
	if in.TranscriptPath != nil || in.LastAssistantMessage != nil {
		t.Fatalf("expected nil optional pointers: %+v", in)
	}
}

func TestParseExplicitNulls(t *testing.T) {
	in, err := Parse(strings.NewReader(`{"transcript_path": null, "stop_hook_active": null, "last_assistant_message": null}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if in.StopHookActive != nil || in.TranscriptPath != nil || in.LastAssistantMessage != nil {
		t.Fatalf("explicit null should decode to nil pointers: %+v", in)
	}
}

func TestParseRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"empty":          "",
		"whitespace":     " \n\t ",
		"invalid json":   "{not json",
		"array":          "[1,2,3]",
		"trailing data":  `{"session_id":"s"} {"session_id":"t"}`,
		"oversized data": strings.Repeat("x", MaxInputBytes+1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(input)); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

func TestValidateWarnings(t *testing.T) {
	var zero Input
	warnings := zero.Validate()
	if len(warnings) != 4 {
		t.Fatalf("expected 4 warnings for zero input, got %v", warnings)
	}
	full := Input{
		SessionID:            "s",
		TurnID:               "t",
		TranscriptPath:       ptr("/tmp/x"),
		StopHookActive:       ptr(false),
		LastAssistantMessage: ptr("answer"),
	}
	if warnings := full.Validate(); len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
}

func TestWritePassIsValidJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePass(&buf); err != nil {
		t.Fatalf("WritePass: %v", err)
	}
	if got := buf.String(); got != "{}\n" {
		t.Fatalf("unexpected pass output %q", got)
	}
	if !json.Valid(buf.Bytes()) {
		t.Fatal("pass output is not valid JSON")
	}
}

func TestWriteBlockIsValidJSON(t *testing.T) {
	var buf bytes.Buffer
	reason := "请重新输出完整最终回答"
	if err := WriteBlock(&buf, reason); err != nil {
		t.Fatalf("WriteBlock: %v", err)
	}
	if !json.Valid(buf.Bytes()) {
		t.Fatalf("block output is not valid JSON: %q", buf.String())
	}
	var out BlockOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal block: %v", err)
	}
	if out.Decision != "block" || out.Reason != reason {
		t.Fatalf("unexpected block output: %+v", out)
	}
}

func TestExtractLastUserMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := strings.Join([]string{
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"最初的问题"}]}}`,
		`{"type":"response_item","payload":{"role":"assistant","content":[{"type":"output_text","text":"回答一"}]}}`,
		"this line is not json and must be skipped",
		`{"type":"response_item","payload":{"role":"user","content":[{"type":"input_text","text":"最新问题"}]}}`,
		`{"type":"response_item","payload":{"role":"assistant","content":[{"type":"output_text","text":"回答二"}]}}`,
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if got := ExtractLastUserMessage(path); got != "最新问题" {
		t.Fatalf("unexpected user message %q", got)
	}
}

func TestExtractLastUserMessageTopLevelRole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	content := strings.Join([]string{
		`{"role":"user","content":"问题 A"}`,
		`{"role":"assistant","content":[{"type":"output_text","text":"回答"}]}`,
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if got := ExtractLastUserMessage(path); got != "问题 A" {
		t.Fatalf("unexpected user message %q", got)
	}
}

func TestExtractLastUserMessageMissingOrMalformedFile(t *testing.T) {
	if got := ExtractLastUserMessage(filepath.Join(t.TempDir(), "missing.jsonl")); got != "" {
		t.Fatalf("expected empty for missing file, got %q", got)
	}
	path := filepath.Join(t.TempDir(), "malformed.jsonl")
	if err := os.WriteFile(path, []byte("not json\n{broken\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if got := ExtractLastUserMessage(path); got != "" {
		t.Fatalf("expected empty for malformed file, got %q", got)
	}
	if got := ExtractLastUserMessage(""); got != "" {
		t.Fatalf("expected empty for empty path, got %q", got)
	}
}

func TestExtractLastUserMessageTruncates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	long := strings.Repeat("长", MaxUserMessageRunes+500)
	line, err := json.Marshal(map[string]any{"role": "user", "content": long})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, line, 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	got := ExtractLastUserMessage(path)
	if utf8.RuneCountInString(got) != MaxUserMessageRunes {
		t.Fatalf("expected %d runes, got %d", MaxUserMessageRunes, utf8.RuneCountInString(got))
	}
}

func TestExtractLastUserMessageReadsBoundedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	var builder strings.Builder
	userLine, err := json.Marshal(map[string]any{"role": "user", "content": "早先的问题"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	builder.Write(userLine)
	builder.WriteByte('\n')
	filler, err := json.Marshal(map[string]any{"role": "assistant", "content": strings.Repeat("填充", 2000)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for builder.Len() < MaxTranscriptTailBytes+4096 {
		builder.Write(filler)
		builder.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if got := ExtractLastUserMessage(path); got != "" {
		t.Fatalf("user message outside the read window should not be found, got %q", got)
	}
}

func ptr[T any](v T) *T { return &v }
