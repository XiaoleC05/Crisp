// Package critic calls an OpenAI-compatible chat completion endpoint that
// reviews an answer for semantic quality problems. The critic decides whether
// a rewrite is warranted; it never rewrites text itself.
package critic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Decision values accepted from the critic.
const (
	DecisionPass  = "pass"
	DecisionBlock = "block"
)

// Severity values accepted from the critic. Only SeverityMaterial together
// with DecisionBlock may trigger a rewrite.
const (
	SeverityNone     = "none"
	SeverityMinor    = "minor"
	SeverityMaterial = "material"
)

// Bounds applied to untrusted provider output. They keep a hostile or broken
// provider from inflating logs and memory, and keep the verdict usable by the
// decision layer.
const (
	maxIssues                = 10
	maxIssueTypeRunes        = 64
	maxIssueLocationRunes    = 64
	maxIssueInstructionRunes = 400
	maxEnumEchoRunes         = 32
)

// Issue is one quality problem reported by the critic.
type Issue struct {
	Type        string `json:"type"`
	Location    string `json:"location,omitempty"`
	Instruction string `json:"instruction,omitempty"`
}

// Usage mirrors token accounting returned by the provider.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Verdict is the parsed critic response.
type Verdict struct {
	Decision  string  `json:"decision"`
	Severity  string  `json:"severity"`
	Issues    []Issue `json:"issues,omitempty"`
	Usage     Usage   `json:"-"`
	LatencyMS int64   `json:"-"`
}

// MaterialBlock reports whether the verdict requires a rewrite.
func (v Verdict) MaterialBlock() bool {
	return strings.EqualFold(v.Decision, DecisionBlock) &&
		strings.EqualFold(v.Severity, SeverityMaterial)
}

// verdictWire mirrors the strict JSON object the critic is asked to return.
// Pointers and json.RawMessage are used so a missing field can be told apart
// from a present zero value.
type verdictWire struct {
	Decision *string         `json:"decision"`
	Severity *string         `json:"severity"`
	Issues   json.RawMessage `json:"issues"`
}

// parseVerdict validates one critic JSON object. It is deliberately strict:
// invalid JSON, a missing required field, an unknown enum value or a
// non-array issues field all return an error so the caller can fail open.
// Unknown extra fields are ignored because providers add metadata over time.
func parseVerdict(content string) (Verdict, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return Verdict{}, errors.New("empty verdict content")
	}
	var wire verdictWire
	if err := json.Unmarshal([]byte(trimmed), &wire); err != nil {
		return Verdict{}, fmt.Errorf("verdict is not valid JSON: %w", err)
	}
	decision, err := parseEnum("decision", wire.Decision, DecisionPass, DecisionBlock)
	if err != nil {
		return Verdict{}, err
	}
	severity, err := parseEnum("severity", wire.Severity, SeverityNone, SeverityMinor, SeverityMaterial)
	if err != nil {
		return Verdict{}, err
	}
	issues, err := parseIssues(wire.Issues)
	if err != nil {
		return Verdict{}, err
	}
	return Verdict{Decision: decision, Severity: severity, Issues: issues}, nil
}

// parseEnum validates a required enum field. Missing and unknown values are
// both rejected; the echoed value is truncated so an unexpectedly large model
// output cannot bloat the error.
func parseEnum(field string, raw *string, allowed ...string) (string, error) {
	if raw == nil {
		return "", fmt.Errorf("missing required field %q", field)
	}
	value := strings.TrimSpace(*raw)
	for _, candidate := range allowed {
		if value == candidate {
			return value, nil
		}
	}
	return "", fmt.Errorf("field %q has unsupported value %q (want %s)",
		field, truncateRunes(value, maxEnumEchoRunes), strings.Join(allowed, "|"))
}

// parseIssues validates the required issues array. A missing or null array is
// an error; an empty array is valid. Individual issues are normalized and
// capped, and entirely empty issue objects are dropped.
func parseIssues(raw json.RawMessage) ([]Issue, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errors.New(`missing required field "issues"`)
	}
	if trimmed[0] != '[' {
		return nil, errors.New(`field "issues" must be an array`)
	}
	var wire []Issue
	if err := json.Unmarshal(trimmed, &wire); err != nil {
		return nil, fmt.Errorf("field \"issues\" is not a valid array of issue objects: %w", err)
	}
	issues := make([]Issue, 0, min(len(wire), maxIssues))
	for _, issue := range wire {
		normalized := Issue{
			Type:        truncateRunes(strings.TrimSpace(issue.Type), maxIssueTypeRunes),
			Location:    truncateRunes(strings.TrimSpace(issue.Location), maxIssueLocationRunes),
			Instruction: truncateRunes(strings.TrimSpace(issue.Instruction), maxIssueInstructionRunes),
		}
		if normalized.Type == "" && normalized.Location == "" && normalized.Instruction == "" {
			continue
		}
		issues = append(issues, normalized)
		if len(issues) == maxIssues {
			break
		}
	}
	if len(issues) == 0 {
		return nil, nil
	}
	return issues, nil
}

// truncateRunes returns s shortened to at most limit runes. It never splits a
// multi-byte rune in the middle.
func truncateRunes(s string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}
