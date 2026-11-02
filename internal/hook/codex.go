// Package hook parses the Codex Stop hook protocol and writes the JSON
// response that Codex consumes.
package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	// MaxInputBytes bounds the hook JSON read from stdin.
	MaxInputBytes = 4 << 20
	// MaxTranscriptTailBytes bounds how much of transcript_path is read.
	MaxTranscriptTailBytes = 512 << 10
	// MaxUserMessageRunes bounds the user context forwarded to the critic.
	MaxUserMessageRunes = 2000
)

// Input is the subset of the Codex Stop hook payload the gate relies on.
// Unknown fields are ignored on purpose: Codex adds fields over time.
type Input struct {
	SessionID            string  `json:"session_id"`
	TurnID               string  `json:"turn_id"`
	TranscriptPath       *string `json:"transcript_path"`
	StopHookActive       *bool   `json:"stop_hook_active"`
	LastAssistantMessage *string `json:"last_assistant_message"`
	HookEventName        string  `json:"hook_event_name"`
}

// Parse decodes one Stop hook payload. Empty, oversized, malformed or
// trailing-data inputs are rejected; the caller must fail open on any error.
func Parse(r io.Reader) (Input, error) {
	if r == nil {
		return Input{}, errors.New("nil hook input")
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxInputBytes+1))
	if err != nil {
		return Input{}, fmt.Errorf("read hook input: %w", err)
	}
	if len(data) > MaxInputBytes {
		return Input{}, fmt.Errorf("hook input exceeds %d bytes", MaxInputBytes)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return Input{}, errors.New("empty hook input")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var in Input
	if err := dec.Decode(&in); err != nil {
		return Input{}, fmt.Errorf("decode hook input: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Input{}, errors.New("hook input contains trailing data")
	}
	return in, nil
}

// Validate lists protocol anomalies. They are logged but never block.
func (in Input) Validate() []string {
	var warnings []string
	if in.SessionID == "" {
		warnings = append(warnings, "missing session_id")
	}
	if in.TurnID == "" {
		warnings = append(warnings, "missing turn_id")
	}
	if in.StopHookActive == nil {
		warnings = append(warnings, "missing stop_hook_active")
	}
	if in.LastAssistantMessage == nil {
		warnings = append(warnings, "missing last_assistant_message")
	}
	return warnings
}

// BlockOutput is the only non-pass response Codex accepts: it asks for a full
// regeneration of the answer.
type BlockOutput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// WritePass writes the pass response ({}) followed by a newline.
func WritePass(w io.Writer) error {
	_, err := io.WriteString(w, "{}\n")
	return err
}

// WriteBlock writes the block response followed by a newline.
func WriteBlock(w io.Writer, reason string) error {
	b, err := json.Marshal(BlockOutput{Decision: "block", Reason: reason})
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// ExtractLastUserMessage returns the most recent user message found in the
// transcript at path. It is strictly best-effort: unreadable or unparsable
// transcripts return "". Only a bounded tail of the file is read, and the
// result is capped so it cannot inflate the critic prompt.
func ExtractLastUserMessage(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.Size() <= 0 {
		return ""
	}
	tail := info.Size()
	if tail > MaxTranscriptTailBytes {
		tail = MaxTranscriptTailBytes
	}
	if _, err := f.Seek(info.Size()-tail, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxTranscriptTailBytes))
	if err != nil {
		return ""
	}

	lines := bytes.Split(data, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(line, &v); err != nil {
			continue
		}
		if msg := findUserText(v, 0); msg != "" {
			return truncateRunes(strings.TrimSpace(msg), MaxUserMessageRunes)
		}
	}
	return ""
}

const maxExtractDepth = 6

func findUserText(v any, depth int) string {
	if depth > maxExtractDepth {
		return ""
	}
	switch t := v.(type) {
	case map[string]any:
		role, _ := t["role"].(string)
		typ, _ := t["type"].(string)
		if strings.EqualFold(role, "user") || typ == "user_message" || typ == "user" {
			for _, key := range []string{"text", "content", "message", "input"} {
				if s := extractText(t[key], depth+1); s != "" {
					return s
				}
			}
		}
		for _, key := range []string{"payload", "item", "message", "data"} {
			if s := findUserText(t[key], depth+1); s != "" {
				return s
			}
		}
	case []any:
		for i := len(t) - 1; i >= 0; i-- {
			if s := findUserText(t[i], depth+1); s != "" {
				return s
			}
		}
	}
	return ""
}

func extractText(v any, depth int) string {
	if depth > maxExtractDepth {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			if s := extractText(item, depth+1); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		for _, key := range []string{"text", "content", "message"} {
			if s := extractText(t[key], depth+1); s != "" {
				return s
			}
		}
	}
	return ""
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return string(r[:limit])
}
