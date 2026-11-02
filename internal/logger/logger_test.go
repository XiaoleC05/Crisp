package logger

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not valid JSON: %v (%q)", err, line)
		}
		out = append(out, entry)
	}
	return out
}

func TestInfoWritesOneJSONLine(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf)
	log.Info("quality_check", map[string]any{"decision": "pass", "critic_tokens": 12})
	lines := decodeLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d", len(lines))
	}
	line := lines[0]
	if line["event"] != "quality_check" || line["level"] != "info" || line["decision"] != "pass" {
		t.Fatalf("unexpected log line: %v", line)
	}
	if _, ok := line["ts"].(string); !ok {
		t.Fatalf("missing ts field: %v", line)
	}
	if line["critic_tokens"] != float64(12) {
		t.Fatalf("unexpected critic_tokens: %v", line["critic_tokens"])
	}
}

func TestWarnAndError(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf)
	log.Warn("protocol_anomaly", "missing stop_hook_active", nil)
	log.Error("critic_failed", "critic call failed", errString("boom"), map[string]any{"fail_open": true})
	lines := decodeLines(t, &buf)
	if len(lines) != 2 {
		t.Fatalf("expected 2 log lines, got %d", len(lines))
	}
	if lines[0]["level"] != "warn" || lines[0]["msg"] != "missing stop_hook_active" {
		t.Fatalf("unexpected warn line: %v", lines[0])
	}
	if lines[1]["level"] != "error" || lines[1]["error"] != "boom" {
		t.Fatalf("unexpected error line: %v", lines[1])
	}
}

func TestSensitiveFieldsAreDropped(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf)
	log.Info("test", map[string]any{
		"answer":        "full answer text",
		"prompt":        "system prompt",
		"api_key":       "sk-secret",
		"apikey":        "sk-secret",
		"authorization": "Bearer secret",
		"password":      "hunter2",
		"token":         "secret-token",
		"user_message":  "private question",
		"content":       "raw content",
		"decision":      "pass",
	})
	lines := decodeLines(t, &buf)
	for _, key := range []string{"answer", "prompt", "api_key", "apikey", "authorization", "password", "token", "user_message", "content"} {
		if _, ok := lines[0][key]; ok {
			t.Fatalf("sensitive field %q must be dropped: %v", key, lines[0])
		}
	}
	if lines[0]["decision"] != "pass" {
		t.Fatalf("non-sensitive field should remain: %v", lines[0])
	}
}

func TestLongValuesAreTruncated(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf)
	long := strings.Repeat("长", MaxFieldStringRunes+100)
	log.Info("test", map[string]any{"detail": long})
	lines := decodeLines(t, &buf)
	detail, ok := lines[0]["detail"].(string)
	if !ok {
		t.Fatalf("detail missing: %v", lines[0])
	}
	if detail == long {
		t.Fatal("long value was not truncated")
	}
	if utf8.RuneCountInString(detail) <= MaxFieldStringRunes {
		t.Fatalf("truncated value unexpectedly short: %d", utf8.RuneCountInString(detail))
	}
	if !strings.HasSuffix(detail, "(truncated)") {
		t.Fatalf("truncated value should end with marker: %q", detail)
	}
}

func TestMarshalErrorWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf)
	log.Info("test", map[string]any{"bad": make(chan int)})
	if buf.Len() != 0 {
		t.Fatalf("expected no output on marshal error, got %q", buf.String())
	}
}

func TestNilLoggerAndNilWriterDoNotPanic(t *testing.T) {
	var nilLogger *Logger
	nilLogger.Info("test", nil)
	New(nil).Info("test", nil)
}

type errString string

func (e errString) Error() string { return string(e) }
