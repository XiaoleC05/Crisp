// Package logger writes structured JSON-line events to stderr.
//
// stdout is reserved for the single JSON decision consumed by the Codex Stop
// hook, so every diagnostic line must go to stderr. Logging is best-effort and
// must never affect the quality decision.
package logger

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"
)

// MaxFieldStringRunes caps string values so a single log line can never grow
// large enough to contain an entire answer.
const MaxFieldStringRunes = 512

// Logger is a minimal structured JSON logger. It is safe for concurrent use.
type Logger struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

// New returns a logger writing JSON lines to w (normally stderr).
func New(w io.Writer) *Logger {
	return &Logger{w: w, now: time.Now}
}

// Info logs a normal event.
func (l *Logger) Info(event string, fields map[string]any) {
	l.write("info", event, fields)
}

// Warn logs a recoverable anomaly. msg is a short human-readable message.
func (l *Logger) Warn(event, msg string, fields map[string]any) {
	l.write("warn", event, withMessage(msg, fields))
}

// Error logs an error without failing anything: logging must never change the
// decision.
func (l *Logger) Error(event, msg string, err error, fields map[string]any) {
	f := withMessage(msg, fields)
	if err != nil {
		f["error"] = err.Error()
	}
	l.write("error", event, f)
}

func (l *Logger) write(level, event string, fields map[string]any) {
	if l == nil || l.w == nil {
		return
	}
	entry := make(map[string]any, len(fields)+3)
	entry["ts"] = l.now().UTC().Format(time.RFC3339Nano)
	entry["level"] = level
	entry["event"] = event
	for k, v := range fields {
		if isSensitiveKey(k) {
			continue
		}
		entry[k] = truncateValue(v)
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(b)
}

func withMessage(msg string, fields map[string]any) map[string]any {
	out := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		out[k] = v
	}
	if msg != "" {
		out["msg"] = msg
	}
	return out
}

// isSensitiveKey prevents answer text, user content and credentials from
// being logged by accident.
func isSensitiveKey(key string) bool {
	switch strings.ToLower(key) {
	case "answer", "last_assistant_message", "user_message", "prompt", "content",
		"api_key", "apikey", "authorization", "password", "secret", "token":
		return true
	}
	return false
}

func truncateValue(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	r := []rune(s)
	if len(r) <= MaxFieldStringRunes {
		return s
	}
	return string(r[:MaxFieldStringRunes]) + "...(truncated)"
}
