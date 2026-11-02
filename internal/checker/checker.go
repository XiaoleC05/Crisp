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

// Tunables for every deterministic rule. Keep them here so the regression
// suite over testdata/cases can be re-tuned without touching rule code.
const (
	// maxEvidenceRunes bounds Finding.Evidence; findings never carry a whole
	// answer, no matter how long the flagged paragraph is.
	maxEvidenceRunes = 120

	// duplicateSimilarity is the character-shingle Jaccard threshold used by
	// both duplicate rules (and the compatible length pre-filter derived from
	// it: two near-duplicates cannot differ in length by more than 1/J).
	duplicateSimilarity = 0.85

	// Duplicate paragraph rules.
	minParagraphRunes           = 20  // ignore shorter paragraphs (list items, one-liners)
	nearDuplicateParagraphRunes = 48  // near-duplicate reports need this much text
	maxAnalyzedParagraphs       = 400 // analysis cap for pathological answers

	// Duplicate sentence rules.
	minSentenceCJKRunes        = 12   // CJK sentences shorter than this are ignored
	minSentenceLatinRunes      = 40   // ASCII/Latin sentences shorter than this are ignored
	sentenceMaterialSimilarity = 0.90 // "strong" similarity required for material
	materialSentenceCJKRunes   = 18
	materialSentenceLatinRunes = 60
	maxAnalyzedSentences       = 600

	// Filler rules.
	fillerParagraphWindow   = 2    // opening/closing paragraphs inspected
	fillerMinParagraphRunes = 6    // ignore tiny "ok." paragraphs
	fillerPureMinRunes      = 12   // a "pure filler" paragraph must be at least this long
	fillerPureMaxRunes      = 100  // ... and short enough to carry no real content
	fillerPureCoverage      = 0.66 // filler phrases must cover this share of the paragraph
	fillerShortClosingRunes = 60   // short closing paragraphs are matched anywhere

	// Structure rules (risk signals only, info/minor).
	tinyListItemRunes      = 30   // a list item shorter than this is "tiny"
	tinyListItemMinCount   = 8    // require this many tiny items ...
	tinyListItemRatio      = 0.70 // ... and this share of all list items
	shortParagraphRunes    = 50   // a "short" paragraph has at most this many runes
	singleSentenceMinCount = 6    // require this many one-sentence prose paragraphs ...
	singleSentenceRatio    = 0.75 // ... and this share of all prose paragraphs
	scaffoldPrefixRunes    = 3    // identical opening runes of list items
	scaffoldMinItems       = 8    // require this many items in one list ...
	scaffoldRatio          = 0.80 // ... and this share repeating the same scaffold

	// Length signal (info only, never a block reason).
	longAnswerNaturalRunes = 2000

	// maxFindingsPerType caps log/critic noise per rule.
	maxFindingsPerType = 5

	// maxInlineSpanRunes bounds scans for inline link destinations so hostile
	// input ("[[[[[...") cannot degrade normalisation into O(n^2).
	maxInlineSpanRunes = 2048
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
// panic on any input: a panic-free quality gate is a hard requirement.
func Analyze(text string) (report Report) {
	defer func() {
		// Fail safe: a rule bug degrades to "no findings", never to a crash.
		if recover() != nil {
			report.Findings = nil
		}
	}()

	doc := parseDocument(text)
	report.Stats = doc.stats
	report.Findings = analyzeRules(doc)
	return report
}

// analyzeRules runs the rules in a fixed order so output is deterministic.
func analyzeRules(doc *document) []Finding {
	findings := make([]Finding, 0, 8)
	findings = append(findings, duplicateParagraphFindings(doc)...)
	findings = append(findings, duplicateSentenceFindings(doc)...)
	findings = append(findings, fillerFindings(doc)...)
	findings = append(findings, structureFindings(doc)...)
	findings = append(findings, lengthFindings(doc)...)
	if len(findings) == 0 {
		return nil
	}
	return findings
}

// runeCount is a tiny helper used across rules.
func runeCount(s string) int { return utf8.RuneCountInString(s) }
