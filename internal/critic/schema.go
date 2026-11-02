// Package critic calls an OpenAI-compatible chat completion endpoint that
// reviews an answer for semantic quality problems. The critic decides whether
// a rewrite is warranted; it never rewrites text itself.
package critic

import "strings"

const (
	DecisionPass  = "pass"
	DecisionBlock = "block"

	SeverityNone     = "none"
	SeverityMinor    = "minor"
	SeverityMaterial = "material"
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
