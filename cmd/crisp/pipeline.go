package main

import (
	"context"
	"time"

	"github.com/XiaoleC05/Crisp/internal/checker"
	"github.com/XiaoleC05/Crisp/internal/critic"
	"github.com/XiaoleC05/Crisp/internal/policy"
)

// reviewOutcome is the complete result of one quality pipeline run: the
// deterministic report, the optional critic verdict and the final decision.
type reviewOutcome struct {
	Mode         string
	Decision     policy.Decision
	Report       checker.Report
	CriticCalled bool
	Verdict      *critic.Verdict
	CriticErr    error
	Elapsed      time.Duration
}

// reviewAnswer runs the deterministic checker, calls the critic when the
// policy requires it, and maps the result to a PASS/BLOCK decision.
//
// Every critic failure is converted to a fail-open decision by policy.Decide;
// the error itself is still returned so callers can log it.
func reviewAnswer(ctx context.Context, answer, userMessage string) reviewOutcome {
	started := time.Now()
	report := checker.Analyze(answer)
	policyCfg := policy.Config{
		Mode:           envString("QUALITY_GATE_MODE", policy.ModeBalanced),
		MinCriticRunes: envInt("QUALITY_GATE_MIN_CRITIC_RUNES", policy.DefaultMinCriticRunes),
	}.Normalize()

	criticCalled := false
	var verdict *critic.Verdict
	var criticErr error
	if policy.NeedsCritic(policyCfg, report, answer) {
		client, err := critic.New(criticConfigFromEnv())
		if err != nil {
			criticErr = err
		} else {
			criticCalled = true
			v, err := client.Review(ctx, critic.Input{
				Answer:       answer,
				UserMessage:  userMessage,
				RuleFindings: report.Findings,
			})
			if err != nil {
				criticErr = err
			} else {
				verdict = &v
			}
		}
	}

	return reviewOutcome{
		Mode:         policyCfg.Mode,
		Decision:     policy.Decide(report, verdict, criticErr, criticCalled),
		Report:       report,
		CriticCalled: criticCalled,
		Verdict:      verdict,
		CriticErr:    criticErr,
		Elapsed:      time.Since(started),
	}
}

// summaryFields renders the stable JSON summary shared by check-text and
// check-file.
func (o reviewOutcome) summaryFields() map[string]any {
	return map[string]any{
		"decision":          decisionName(o.Decision),
		"reason_type":       o.Decision.ReasonType,
		"rule_types":        o.Report.Types(),
		"critic_called":     o.CriticCalled,
		"fail_open":         o.Decision.FailOpen,
		"answer_runes":      o.Report.Stats.Runes,
		"natural_runes":     o.Report.Stats.NaturalRunes,
		"critic_latency_ms": verdictLatencyMS(o.Verdict),
		"critic_tokens":     verdictTokens(o.Verdict),
		"elapsed_ms":        o.Elapsed.Milliseconds(),
	}
}
