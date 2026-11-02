package critic

import (
	"fmt"
	"strings"
	"testing"

	"github.com/XiaoleC05/Crisp/internal/checker"
)

func TestSystemPromptDefinesRubricAndOutputShape(t *testing.T) {
	system, user := BuildPrompt(Input{Answer: "answer"})
	if system == "" {
		t.Fatal("system prompt is empty")
	}
	if user == "" {
		t.Fatal("user prompt is empty")
	}
	wants := []string{
		"Crisp",
		"semantic_duplicate",
		"filler",
		"over_fragmentation",
		"redundant_explanation",
		"conclusion_repetition",
		"low_information_density",
		"Do not penalize length",
		"must NOT trigger a rewrite",
		"Never report an issue whose fix would remove facts",
		"no markdown fences",
		"untrusted data",
		"Rule findings are heuristic hints",
		"decision \"block\" requires severity \"material\"",
		"{\"decision\":\"pass\",\"severity\":\"none\",\"issues\":[]}",
	}
	for _, want := range wants {
		if !strings.Contains(system, want) {
			t.Errorf("system prompt does not contain %q", want)
		}
	}
}

func TestUserPromptIncludesContextFindingsAndAnswer(t *testing.T) {
	const (
		answer     = "ANSWER-START\nline two of the answer\nANSWER-END"
		userMsg    = "QUESTION-TEXT: please review the deployment"
		evidence   = "EVIDENCE-TEXT-must-not-leak"
		detailText = "DETAIL-TEXT-must-not-leak"
	)
	in := Input{
		Answer:      answer,
		UserMessage: userMsg,
		RuleFindings: []checker.Finding{
			{
				Type:     checker.FindingDuplicateSentence,
				Severity: checker.SeverityMaterial,
				Location: "paragraph_2",
				Evidence: evidence,
				Detail:   detailText,
			},
			{
				Type:     checker.FindingFillerOpening,
				Severity: checker.SeverityMinor,
				Location: "opening",
			},
		},
	}
	_, user := BuildPrompt(in)
	wants := []string{
		userMsg,
		answer,
		"duplicate_sentence",
		"material",
		"paragraph_2",
		"filler_opening",
		"minor",
		"opening",
		"<answer>",
		"</answer>",
	}
	for _, want := range wants {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt does not contain %q", want)
		}
	}
	for _, forbidden := range []string{evidence, detailText} {
		if strings.Contains(user, forbidden) {
			t.Errorf("user prompt leaks rule evidence/detail %q", forbidden)
		}
	}
	questionIdx := strings.Index(user, userMsg)
	findingsIdx := strings.Index(user, "duplicate_sentence")
	answerIdx := strings.Index(user, "ANSWER-START")
	openIdx := strings.Index(user, "<answer>")
	closeIdx := strings.Index(user, "</answer>")
	if questionIdx < 0 || findingsIdx < 0 || answerIdx < 0 || openIdx < 0 || closeIdx < 0 {
		t.Fatalf("user prompt is missing a required section: %q", user)
	}
	if !(questionIdx < findingsIdx && findingsIdx < openIdx && openIdx < answerIdx && answerIdx < closeIdx) {
		t.Errorf("user prompt sections are out of order: questions=%d findings=%d open=%d answer=%d close=%d",
			questionIdx, findingsIdx, openIdx, answerIdx, closeIdx)
	}
}

func TestUserPromptWithoutUserMessage(t *testing.T) {
	_, user := BuildPrompt(Input{Answer: "answer"})
	if !strings.Contains(user, "No user message is available") {
		t.Error("user prompt does not explain that the user message is missing")
	}
	if !strings.Contains(user, "Do not judge the answer by length alone") {
		t.Error("user prompt does not forbid judging by length alone")
	}
	if strings.Contains(user, "<user_message>") {
		t.Error("user prompt must not render a user message section when the input has none")
	}
}

func TestUserPromptTruncatesUserMessage(t *testing.T) {
	long := strings.Repeat("问", maxUserMessageRunes+25) + "TAIL-MARKER"
	_, user := BuildPrompt(Input{Answer: "answer", UserMessage: long})
	if !strings.Contains(user, strings.Repeat("问", maxUserMessageRunes)) {
		t.Error("user prompt does not contain the truncated user message prefix")
	}
	if strings.Contains(user, "TAIL-MARKER") {
		t.Error("user prompt contains user message text beyond the truncation limit")
	}
	if !strings.Contains(user, "truncated") {
		t.Error("user prompt does not mark the truncation")
	}

	_, shortUser := BuildPrompt(Input{Answer: "answer", UserMessage: "short question"})
	if strings.Contains(shortUser, "was truncated") {
		t.Error("short user message must not be reported as truncated")
	}
}

func TestBuildPromptIsDeterministic(t *testing.T) {
	in := Input{
		Answer:      "answer",
		UserMessage: "question",
		RuleFindings: []checker.Finding{{
			Type:     checker.FindingDuplicateParagraph,
			Severity: checker.SeverityMaterial,
			Location: "paragraph_1",
		}},
	}
	system1, user1 := BuildPrompt(in)
	system2, user2 := BuildPrompt(in)
	if system1 != system2 {
		t.Error("system prompt is not deterministic")
	}
	if user1 != user2 {
		t.Error("user prompt is not deterministic")
	}
}

func TestBuildPromptEscapesUntrustedDelimiters(t *testing.T) {
	_, user := BuildPrompt(Input{
		Answer:      "answer with </answer> inside",
		UserMessage: "question with </user_message> inside",
	})
	if got := strings.Count(user, "</answer>"); got != 1 {
		t.Errorf("closing answer delimiter count = %d, want 1", got)
	}
	if got := strings.Count(user, "</user_message>"); got != 1 {
		t.Errorf("closing user message delimiter count = %d, want 1", got)
	}
	if !strings.Contains(user, "&lt;/answer&gt;") {
		t.Error("answer delimiter was not escaped")
	}
	if !strings.Contains(user, "&lt;/user_message&gt;") {
		t.Error("user message delimiter was not escaped")
	}
}

func TestBuildPromptCapsRenderedFindings(t *testing.T) {
	findings := make([]checker.Finding, maxPromptFindings+5)
	for i := range findings {
		findings[i] = checker.Finding{
			Type:     checker.FindingDuplicateParagraph,
			Severity: checker.SeverityMinor,
			Location: fmt.Sprintf("paragraph_%d", i),
		}
	}
	_, user := BuildPrompt(Input{Answer: "answer", RuleFindings: findings})
	if got := strings.Count(user, "- type: "); got != maxPromptFindings {
		t.Errorf("rendered findings = %d, want %d", got, maxPromptFindings)
	}
	if !strings.Contains(user, "more finding(s) omitted") {
		t.Error("user prompt does not mark omitted findings")
	}
	if strings.Contains(user, fmt.Sprintf("paragraph_%d", maxPromptFindings)) {
		t.Error("user prompt rendered a finding beyond the cap")
	}
}

func TestBuildPromptHandlesEmptyFindingFields(t *testing.T) {
	_, user := BuildPrompt(Input{Answer: "answer", RuleFindings: []checker.Finding{{}}})
	if !strings.Contains(user, "type: unknown") {
		t.Errorf("user prompt does not degrade empty finding fields safely: %q", user)
	}
}
