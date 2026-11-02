package policy

import (
	"errors"
	"strings"
	"testing"

	"github.com/XiaoleC05/Crisp/internal/checker"
	"github.com/XiaoleC05/Crisp/internal/critic"
)

func reportWith(naturalRunes int, findings ...checker.Finding) checker.Report {
	return checker.Report{
		Findings: findings,
		Stats:    checker.Stats{Runes: naturalRunes, NaturalRunes: naturalRunes},
	}
}

func materialFinding() checker.Finding {
	return checker.Finding{
		Type:     checker.FindingDuplicateParagraph,
		Severity: checker.SeverityMaterial,
		Location: "paragraph_2",
		Evidence: "重复段落",
	}
}

func TestConfigNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   Config
		want Config
	}{
		{"empty", Config{}, Config{Mode: ModeBalanced, MinCriticRunes: DefaultMinCriticRunes}},
		{"economy", Config{Mode: ModeEconomy}, Config{Mode: ModeEconomy, MinCriticRunes: DefaultMinCriticRunes}},
		{"unknown mode", Config{Mode: "turbo"}, Config{Mode: ModeBalanced, MinCriticRunes: DefaultMinCriticRunes}},
		{"negative min", Config{Mode: ModeBalanced, MinCriticRunes: -1}, Config{Mode: ModeBalanced, MinCriticRunes: DefaultMinCriticRunes}},
		{"custom min", Config{Mode: ModeBalanced, MinCriticRunes: 50}, Config{Mode: ModeBalanced, MinCriticRunes: 50}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.Normalize(); got != tc.want {
				t.Fatalf("Normalize() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestNeedsCritic(t *testing.T) {
	cases := []struct {
		name   string
		cfg    Config
		report checker.Report
		want   bool
	}{
		{"balanced long", Config{Mode: ModeBalanced}, reportWith(200), true},
		{"balanced exactly threshold", Config{Mode: ModeBalanced}, reportWith(DefaultMinCriticRunes), true},
		{"balanced short", Config{Mode: ModeBalanced}, reportWith(DefaultMinCriticRunes - 1), false},
		{"economy long clean", Config{Mode: ModeEconomy}, reportWith(500), false},
		{"balanced short suspicious", Config{Mode: ModeBalanced}, reportWith(10, materialFinding()), true},
		{"economy short suspicious", Config{Mode: ModeEconomy}, reportWith(10, materialFinding()), true},
		{"economy long suspicious", Config{Mode: ModeEconomy}, reportWith(500, materialFinding()), true},

		{"custom threshold", Config{Mode: ModeBalanced, MinCriticRunes: 50}, reportWith(60), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NeedsCritic(tc.cfg, tc.report, strings.Repeat("回答", 300)); got != tc.want {
				t.Fatalf("NeedsCritic() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDecideFailOpenOnCriticError(t *testing.T) {
	decision := Decide(reportWith(500), nil, errors.New("critic timeout"), true)
	if decision.Block {
		t.Fatal("critic error must not block")
	}
	if !decision.FailOpen {
		t.Fatal("critic error must be marked fail_open")
	}
	if decision.ReasonType != "critic_error" {
		t.Fatalf("unexpected reason type %q", decision.ReasonType)
	}
}

func TestDecideNotReviewed(t *testing.T) {
	decision := Decide(reportWith(500), nil, nil, false)
	if decision.Block || decision.FailOpen {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	if decision.ReasonType != "not_reviewed" {
		t.Fatalf("unexpected reason type %q", decision.ReasonType)
	}
}

func TestDecideBlocksOnMaterialCriticVerdict(t *testing.T) {
	verdict := critic.Verdict{
		Decision: critic.DecisionBlock,
		Severity: critic.SeverityMaterial,
		Issues:   []critic.Issue{{Type: "semantic_duplicate", Location: "paragraph_3"}},
	}
	decision := Decide(reportWith(500), &verdict, nil, true)
	if !decision.Block {
		t.Fatal("material critic verdict must block")
	}
	if decision.Reason != RewriteReason {
		t.Fatalf("block must use the fixed rewrite reason, got %q", decision.Reason)
	}
	if decision.ReasonType != "semantic_duplicate" {
		t.Fatalf("unexpected reason type %q", decision.ReasonType)
	}
	if decision.FailOpen {
		t.Fatal("a block must not be marked fail_open")
	}
}

func TestDecideMaterialVerdictWithoutIssueType(t *testing.T) {
	verdict := critic.Verdict{Decision: critic.DecisionBlock, Severity: critic.SeverityMaterial}
	decision := Decide(reportWith(500), &verdict, nil, true)
	if !decision.Block || decision.ReasonType != "semantic" {
		t.Fatalf("unexpected decision: %+v", decision)
	}
}

func TestDecideDoesNotBlockOnMinorOrPass(t *testing.T) {
	cases := map[string]critic.Verdict{
		"block but minor": {Decision: critic.DecisionBlock, Severity: critic.SeverityMinor},
		"pass material":   {Decision: critic.DecisionPass, Severity: critic.SeverityMaterial},
		"pass none":       {Decision: critic.DecisionPass, Severity: critic.SeverityNone},
	}
	for name, verdict := range cases {
		t.Run(name, func(t *testing.T) {
			if decision := Decide(reportWith(500), &verdict, nil, true); decision.Block {
				t.Fatalf("verdict %+v must not block", verdict)
			}
		})
	}
}

func TestDecideCaseInsensitiveVerdict(t *testing.T) {
	verdict := critic.Verdict{Decision: "BLOCK", Severity: "MATERIAL"}
	if decision := Decide(reportWith(500), &verdict, nil, true); !decision.Block {
		t.Fatal("verdict comparison should be case insensitive")
	}
}

func TestDecideRulesAloneNeverBlock(t *testing.T) {
	report := reportWith(500, materialFinding())
	decision := Decide(report, nil, nil, false)
	if decision.Block {
		t.Fatal("deterministic findings must not block without critic confirmation")
	}
}

func TestRewriteReasonPreservesContent(t *testing.T) {
	for _, want := range []string{"完整最终回答", "保留全部必要事实、数字、代码", "不要为了缩短篇幅而删除必要信息"} {
		if !strings.Contains(RewriteReason, want) {
			t.Fatalf("rewrite reason must contain %q, got %q", want, RewriteReason)
		}
	}
}
