package critic

import (
	"strings"
	"testing"
)

func TestSystemPromptEncodesPreferredAnswerStyle(t *testing.T) {
	system, _ := BuildPrompt(Input{Answer: "answer"})
	for _, want := range []string{
		"conclusion-first",
		"clearly structured",
		"not over-fragmentation",
		"short or simple answers should stay short",
	} {
		if !strings.Contains(system, want) {
			t.Fatalf("system prompt must mention %q", want)
		}
	}
}
