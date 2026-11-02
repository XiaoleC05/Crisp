// Package policy turns deterministic findings and critic verdicts into the
// final PASS/BLOCK decision. Every failure path is fail-open.
package policy

import (
	"strings"

	"github.com/XiaoleC05/Crisp/internal/checker"
	"github.com/XiaoleC05/Crisp/internal/critic"
)

// Supported critic modes.
const (
	ModeBalanced = "balanced"
	ModeEconomy  = "economy"

	// DefaultMinCriticRunes is the smallest natural-language answer that is
	// worth a semantic review. Shorter answers are passed through unchanged.
	DefaultMinCriticRunes = 120

	// RewriteReason is the executable instruction sent back to Codex when a
	// full regeneration is required. It must not ask for a length reduction:
	// facts, numbers, code and necessary explanations must survive the rewrite.
	RewriteReason = "请重新输出完整最终回答：合并语义重复的段落，删除无效铺垫和结论重复，保留全部必要事实、数字、代码和解释，不要为了缩短篇幅而删除必要信息。"
)

// Config is the decision policy configuration.
type Config struct {
	Mode           string
	MinCriticRunes int
}

// Normalize fills defaults and rejects unknown modes by falling back to
// balanced, which is the recommended mode.
func (c Config) Normalize() Config {
	out := c
	if out.Mode != ModeEconomy {
		out.Mode = ModeBalanced
	}
	if out.MinCriticRunes <= 0 {
		out.MinCriticRunes = DefaultMinCriticRunes
	}
	return out
}

// NeedsCritic reports whether the LLM critic should review this answer.
//
// A deterministic finding always triggers a semantic review, even for short
// answers: a short answer made only of boilerplate is still low quality, and
// rules are never allowed to block without critic confirmation.
//
// balanced: also review every non-trivial natural-language answer.
// economy: review only answers that already produced deterministic findings.
func NeedsCritic(cfg Config, report checker.Report, answer string) bool {
	cfg = cfg.Normalize()
	if len(report.Findings) > 0 {
		return true
	}
	if cfg.Mode == ModeEconomy {
		return false
	}
	return report.Stats.NaturalRunes >= cfg.MinCriticRunes
}

// Decision is the final quality gate outcome.
type Decision struct {
	Block      bool
	Reason     string
	ReasonType string
	FailOpen   bool
}

// Decide maps the critic result to a decision.
//
// A rule-only finding never blocks: duplicated text can be a legitimate quote,
// so duplicates must be confirmed by the critic. A critic error of any kind
// fails open, because a broken checker must not block a good answer.
func Decide(report checker.Report, verdict *critic.Verdict, criticErr error, criticCalled bool) Decision {
	if criticErr != nil {
		return Decision{FailOpen: true, ReasonType: "critic_error"}
	}
	if verdict == nil {
		return Decision{ReasonType: "not_reviewed"}
	}
	if verdict.MaterialBlock() {
		reasonType := "semantic"
		if len(verdict.Issues) > 0 && strings.TrimSpace(verdict.Issues[0].Type) != "" {
			reasonType = verdict.Issues[0].Type
		}
		return Decision{Block: true, Reason: RewriteReason, ReasonType: reasonType}
	}
	return Decision{ReasonType: "critic_pass"}
}
