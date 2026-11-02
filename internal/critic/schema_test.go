package critic

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseVerdictAcceptsValidObjects(t *testing.T) {
	cases := []struct {
		name         string
		content      string
		wantDecision string
		wantSeverity string
		wantIssues   int
	}{
		{
			name:         "pass",
			content:      "{\"decision\":\"pass\",\"severity\":\"none\",\"issues\":[]}",
			wantDecision: DecisionPass,
			wantSeverity: SeverityNone,
			wantIssues:   0,
		},
		{
			name:         "surrounding whitespace",
			content:      " \n\t {\"decision\":\"pass\",\"severity\":\"none\",\"issues\":[]}\n ",
			wantDecision: DecisionPass,
			wantSeverity: SeverityNone,
			wantIssues:   0,
		},
		{
			name:         "minor issue",
			content:      "{\"decision\":\"pass\",\"severity\":\"minor\",\"issues\":[{\"type\":\"filler\",\"location\":\"opening\",\"instruction\":\"Drop the greeting.\"}]}",
			wantDecision: DecisionPass,
			wantSeverity: SeverityMinor,
			wantIssues:   1,
		},
		{
			name:         "material block",
			content:      "{\"decision\":\"block\",\"severity\":\"material\",\"issues\":[{\"type\":\"semantic_duplicate\",\"location\":\"paragraph_3\",\"instruction\":\"Merge with paragraph 1.\"}]}",
			wantDecision: DecisionBlock,
			wantSeverity: SeverityMaterial,
			wantIssues:   1,
		},
		{
			name:         "unknown extra fields are ignored",
			content:      "{\"decision\":\"block\",\"severity\":\"material\",\"issues\":[],\"reason\":\"duplicate\",\"provider\":{\"id\":\"x\"}}",
			wantDecision: DecisionBlock,
			wantSeverity: SeverityMaterial,
			wantIssues:   0,
		},
		{
			name:         "unknown issue fields are ignored",
			content:      "{\"decision\":\"pass\",\"severity\":\"minor\",\"issues\":[{\"type\":\"filler\",\"start\":3,\"end\":9}]}",
			wantDecision: DecisionPass,
			wantSeverity: SeverityMinor,
			wantIssues:   1,
		},
		{
			name:         "empty issue objects are dropped",
			content:      "{\"decision\":\"pass\",\"severity\":\"none\",\"issues\":[{},{}]}",
			wantDecision: DecisionPass,
			wantSeverity: SeverityNone,
			wantIssues:   0,
		},
		{
			name:         "enum values are trimmed",
			content:      "{\"decision\":\" pass \",\"severity\":\" none \",\"issues\":[]}",
			wantDecision: DecisionPass,
			wantSeverity: SeverityNone,
			wantIssues:   0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict, err := parseVerdict(tc.content)
			if err != nil {
				t.Fatalf("parseVerdict: %v", err)
			}
			if verdict.Decision != tc.wantDecision {
				t.Errorf("decision = %q, want %q", verdict.Decision, tc.wantDecision)
			}
			if verdict.Severity != tc.wantSeverity {
				t.Errorf("severity = %q, want %q", verdict.Severity, tc.wantSeverity)
			}
			if len(verdict.Issues) != tc.wantIssues {
				t.Errorf("issues = %d, want %d", len(verdict.Issues), tc.wantIssues)
			}
		})
	}
}

func TestParseVerdictRejectsInvalidObjects(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		wantSubstr string
	}{
		{"empty", "", "empty verdict"},
		{"invalid JSON", "{not json", "not valid JSON"},
		{"top-level array", "[1,2,3]", "not valid JSON"},
		{"missing decision", "{\"severity\":\"none\",\"issues\":[]}", "missing required field"},
		{"missing severity", "{\"decision\":\"pass\",\"issues\":[]}", "missing required field"},
		{"missing issues", "{\"decision\":\"pass\",\"severity\":\"none\"}", "missing required field"},
		{"unknown decision", "{\"decision\":\"approve\",\"severity\":\"none\",\"issues\":[]}", "unsupported value"},
		{"unknown severity", "{\"decision\":\"pass\",\"severity\":\"fatal\",\"issues\":[]}", "unsupported value"},
		{"null issues", "{\"decision\":\"pass\",\"severity\":\"none\",\"issues\":null}", "must be an array"},
		{"object issues", "{\"decision\":\"pass\",\"severity\":\"none\",\"issues\":{}}", "must be an array"},
		{"invalid issue shape", "{\"decision\":\"pass\",\"severity\":\"none\",\"issues\":[42]}", "not a valid array"},
		{"null decision", "{\"decision\":null,\"severity\":\"none\",\"issues\":[]}", "missing required field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseVerdict(tc.content); err == nil {
				t.Fatalf("parseVerdict(%q) succeeded, want error", tc.content)
			} else if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error = %v, want it to contain %q", err, tc.wantSubstr)
			}
		})
	}
}

func TestParseVerdictRejectsMarkdownFences(t *testing.T) {
	fence := strings.Repeat(string(rune(96)), 3)
	content := fence + "json {not json}" + fence
	if _, err := parseVerdict(content); err == nil {
		t.Fatal("parseVerdict accepted a markdown fence, want error")
	}
}

func TestParseVerdictBoundsUntrustedValues(t *testing.T) {
	issues := make([]map[string]string, 0, maxIssues+15)
	for i := 0; i < maxIssues+15; i++ {
		issues = append(issues, map[string]string{
			"type":        strings.Repeat("t", 200),
			"location":    strings.Repeat("l", 200),
			"instruction": strings.Repeat("i", 2000),
		})
	}
	payload, err := json.Marshal(map[string]any{
		"decision": "block",
		"severity": "material",
		"issues":   issues,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	verdict, err := parseVerdict(string(payload))
	if err != nil {
		t.Fatalf("parseVerdict: %v", err)
	}
	if len(verdict.Issues) != maxIssues {
		t.Errorf("issues = %d, want capped at %d", len(verdict.Issues), maxIssues)
	}
	for i, issue := range verdict.Issues {
		if n := utf8.RuneCountInString(issue.Type); n > maxIssueTypeRunes {
			t.Errorf("issue %d type runes = %d, want <= %d", i, n, maxIssueTypeRunes)
		}
		if n := utf8.RuneCountInString(issue.Location); n > maxIssueLocationRunes {
			t.Errorf("issue %d location runes = %d, want <= %d", i, n, maxIssueLocationRunes)
		}
		if n := utf8.RuneCountInString(issue.Instruction); n > maxIssueInstructionRunes {
			t.Errorf("issue %d instruction runes = %d, want <= %d", i, n, maxIssueInstructionRunes)
		}
	}
}

func TestParseVerdictTruncatesEchoedEnumValues(t *testing.T) {
	long := strings.Repeat("z", 5000)
	content := "{\"decision\":\"" + long + "\",\"severity\":\"none\",\"issues\":[]}"
	_, err := parseVerdict(content)
	if err == nil {
		t.Fatal("parseVerdict succeeded, want error")
	}
	if len(err.Error()) > 500 {
		t.Errorf("error message is %d bytes, want a bounded echo", len(err.Error()))
	}
}

func TestVerdictMaterialBlock(t *testing.T) {
	cases := []struct {
		name    string
		verdict Verdict
		want    bool
	}{
		{"block material", Verdict{Decision: DecisionBlock, Severity: SeverityMaterial}, true},
		{"pass material", Verdict{Decision: DecisionPass, Severity: SeverityMaterial}, false},
		{"block minor", Verdict{Decision: DecisionBlock, Severity: SeverityMinor}, false},
		{"block none", Verdict{Decision: DecisionBlock, Severity: SeverityNone}, false},
		{"pass none", Verdict{Decision: DecisionPass, Severity: SeverityNone}, false},
		{"empty", Verdict{}, false},
		{"case insensitive", Verdict{Decision: "BLOCK", Severity: "MATERIAL"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.verdict.MaterialBlock(); got != tc.want {
				t.Errorf("MaterialBlock = %v, want %v", got, tc.want)
			}
		})
	}
}
