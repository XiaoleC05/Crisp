// Package checker implements deterministic, LLM-free quality rules over a
// finished answer. It never rewrites text: it only reports findings that the
// critic and the decision layer may take into account.
package checker

import "unicode/utf8"

// Severity describes how strongly a single rule finding suggests a real
// quality problem. Rules never decide PASS/BLOCK by themselves: findings are
// inputs to the LLM critic.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityMinor    Severity = "minor"
	SeverityMaterial Severity = "material"
)

// FindingType names a deterministic rule finding.
type FindingType string

const (
	FindingDuplicateParagraph FindingType = "duplicate_paragraph"
	FindingDuplicateSentence  FindingType = "duplicate_sentence"
	FindingFillerOpening      FindingType = "filler_opening"
	FindingFillerClosing      FindingType = "filler_closing"
	FindingStructureAnomaly   FindingType = "structure_anomaly"
	FindingLengthSignal       FindingType = "length_signal"
)

// Finding is a single deterministic rule observation.
type Finding struct {
	Type     FindingType `json:"type"`
	Severity Severity    `json:"severity"`
	Location string      `json:"location"`
	Evidence string      `json:"evidence,omitempty"`
	Detail   string      `json:"detail,omitempty"`
}

// Stats are cheap, descriptive measurements of the answer.
type Stats struct {
	Runes        int `json:"runes"`
	NaturalRunes int `json:"natural_runes"`
	Paragraphs   int `json:"paragraphs"`
	Sentences    int `json:"sentences"`
	CodeBlocks   int `json:"code_blocks"`
}

// Report is the complete output of the deterministic stage.
type Report struct {
	Findings []Finding `json:"findings"`
	Stats    Stats     `json:"stats"`
}

// HasMaterial reports whether at least one finding is marked material.
func (r Report) HasMaterial() bool {
	for _, f := range r.Findings {
		if f.Severity == SeverityMaterial {
			return true
		}
	}
	return false
}

// Types returns distinct finding types in stable first-seen order.
func (r Report) Types() []FindingType {
	seen := make(map[FindingType]bool, len(r.Findings))
	out := make([]FindingType, 0, len(r.Findings))
	for _, f := range r.Findings {
		if seen[f.Type] {
			continue
		}
		seen[f.Type] = true
		out = append(out, f.Type)
	}
	return out
}

// Analyze runs every deterministic rule over text. It is pure and must never
// panic on any input.
func Analyze(text string) Report {
	// TODO(checker): implement duplicate, filler and structure rules.
	runes := utf8.RuneCountInString(text)
	return Report{Stats: Stats{Runes: runes, NaturalRunes: runes}}
}
