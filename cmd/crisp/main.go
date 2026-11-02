// Command crisp is the Crisp agent-output quality gate.
//
// In its default mode it is a Codex Stop hook: it reads one JSON object from
// stdin and writes exactly one JSON object to stdout. Every internal failure
// fails open ({}) because a broken quality gate must never block a good
// answer.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/XiaoleC05/Crisp/internal/critic"
	"github.com/XiaoleC05/Crisp/internal/hook"
	"github.com/XiaoleC05/Crisp/internal/logger"
	"github.com/XiaoleC05/Crisp/internal/policy"
)

const (
	version = "0.1.0"

	defaultTotalTimeout  = 25 * time.Second
	defaultCriticTimeout = 8 * time.Second
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is the whole program behind small seams (args, stdin, stdout, stderr) so
// the hook contract is testable without spawning a process.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	log := logger.New(stderr)
	cmd := "hook"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "hook":
		return runHook(log, stdin, stdout)
	case "check-text":
		return runCheckText(log, stdin, stdout)
	case "check-file":
		return runCheckFile(log, args[1:], stdin, stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "crisp %s\n", version)
		return 0
	case "help", "--help", "-h":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "crisp: unknown command %q\n", cmd)
		printUsage(stderr)
		return 2
	}
}

// runHook implements the Codex Stop hook contract.
func runHook(log *logger.Logger, stdin io.Reader, stdout io.Writer) (code int) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(),
		envDuration("QUALITY_GATE_TOTAL_TIMEOUT_MS", defaultTotalTimeout))
	defer cancel()

	// A panic anywhere must still produce a valid pass response. outputWritten
	// avoids corrupting stdout when the panic happens after a block response.
	outputWritten := false
	defer func() {
		if r := recover(); r != nil {
			log.Error("panic", "quality gate panicked; failing open", fmt.Errorf("%v", r), nil)
			if !outputWritten {
				_ = hook.WritePass(stdout)
			}
		}
		code = 0
	}()

	in, err := hook.Parse(stdin)
	if err != nil {
		log.Error("hook_input_parse_failed", "invalid hook input; failing open", err, nil)
		outputWritten = true
		return writePass(stdout, log)
	}

	base := map[string]any{
		"session_id": truncateString(in.SessionID, 64),
		"turn_id":    truncateString(in.TurnID, 64),
	}
	for _, warning := range in.Validate() {
		log.Warn("protocol_anomaly", warning, base)
	}

	// Guard 1: a second Stop after a rewrite must pass immediately. Codex does
	// not enforce a one-rewrite limit itself, so this is what stops loops.
	if in.StopHookActive == nil {
		log.Warn("stop_hook_active_missing", "stop_hook_active is absent; failing open", base)
		outputWritten = true
		return writePass(stdout, log)
	}
	if *in.StopHookActive {
		log.Info("quality_skip", mergeFields(base, map[string]any{"skip_reason": "stop_hook_active"}))
		outputWritten = true
		return writePass(stdout, log)
	}

	// Guard 2: nothing to review.
	if in.LastAssistantMessage == nil || strings.TrimSpace(*in.LastAssistantMessage) == "" {
		log.Info("quality_skip", mergeFields(base, map[string]any{"skip_reason": "empty_last_assistant_message"}))
		outputWritten = true
		return writePass(stdout, log)
	}

	answer := *in.LastAssistantMessage
	userMessage := ""
	if in.TranscriptPath != nil {
		userMessage = hook.ExtractLastUserMessage(*in.TranscriptPath)
	}

	outcome := reviewAnswer(ctx, answer, userMessage)
	if outcome.CriticErr != nil {
		if errors.Is(outcome.CriticErr, critic.ErrNotConfigured) {
			log.Warn("critic_unavailable", "critic is not usable; failing open",
				mergeFields(base, map[string]any{"error": outcome.CriticErr.Error()}))
		} else {
			log.Warn("critic_failed", "critic call failed; failing open",
				mergeFields(base, map[string]any{"error": outcome.CriticErr.Error()}))
		}
	}

	log.Info("quality_check", mergeFields(base, map[string]any{
		"mode":              outcome.Mode,
		"decision":          decisionName(outcome.Decision),
		"reason_type":       outcome.Decision.ReasonType,
		"rule_types":        outcome.Report.Types(),
		"rule_material":     outcome.Report.HasMaterial(),
		"answer_runes":      outcome.Report.Stats.Runes,
		"natural_runes":     outcome.Report.Stats.NaturalRunes,
		"critic_called":     outcome.CriticCalled,
		"critic_latency_ms": verdictLatencyMS(outcome.Verdict),
		"critic_tokens":     verdictTokens(outcome.Verdict),
		"fail_open":         outcome.Decision.FailOpen,
		"elapsed_ms":        time.Since(started).Milliseconds(),
	}))

	outputWritten = true
	if outcome.Decision.Block {
		if err := hook.WriteBlock(stdout, outcome.Decision.Reason); err != nil {
			log.Error("hook_output_failed", "failed to write block decision", err, nil)
		}
		return 0
	}
	return writePass(stdout, log)
}

// runCheckText runs the same pipeline over raw answer text from stdin. It is a
// testing/evaluation entry point, not part of the Stop hook contract.
func runCheckText(log *logger.Logger, stdin io.Reader, stdout io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(),
		envDuration("QUALITY_GATE_TOTAL_TIMEOUT_MS", defaultTotalTimeout))
	defer cancel()

	data, err := io.ReadAll(io.LimitReader(stdin, 4<<20))
	if err != nil {
		writeJSON(stdout, map[string]any{"decision": "pass", "fail_open": true, "error": "read stdin failed"})
		return 0
	}
	outcome := reviewAnswer(ctx, string(data), "")
	writeJSON(stdout, outcome.summaryFields())
	return 0
}

func criticConfigFromEnv() critic.Config {
	return critic.Config{
		BaseURL:     strings.TrimSpace(os.Getenv("QUALITY_GATE_BASE_URL")),
		APIKey:      strings.TrimSpace(os.Getenv("QUALITY_GATE_API_KEY")),
		Model:       strings.TrimSpace(os.Getenv("QUALITY_GATE_MODEL")),
		Timeout:     envDuration("QUALITY_GATE_CRITIC_TIMEOUT_MS", defaultCriticTimeout),
		Temperature: 0,
	}
}

func writePass(stdout io.Writer, log *logger.Logger) int {
	if err := hook.WritePass(stdout); err != nil {
		log.Error("hook_output_failed", "failed to write pass decision", err, nil)
	}
	return 0
}

func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		_, _ = io.WriteString(w, "{}")
	}
}

func decisionName(d policy.Decision) string {
	if d.Block {
		return "block"
	}
	return "pass"
}

func verdictLatencyMS(v *critic.Verdict) int64 {
	if v == nil {
		return 0
	}
	return v.LatencyMS
}

func verdictTokens(v *critic.Verdict) int {
	if v == nil {
		return 0
	}
	return v.Usage.TotalTokens
}

func mergeFields(base map[string]any, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func truncateString(s string, limit int) string {
	r := []rune(s)
	if limit <= 0 || len(r) <= limit {
		return s
	}
	return string(r[:limit])
}

func envString(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func envDuration(key string, def time.Duration) time.Duration {
	return time.Duration(envInt(key, int(def/time.Millisecond))) * time.Millisecond
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, "crisp - agent output quality gate\n\nUsage:\n  crisp                 Run as a Stop hook (Codex CLI / Claude Code; JSON on stdin, JSON on stdout)\n  crisp hook            Same as above\n  crisp check-text      Run the quality pipeline over raw text on stdin (testing)\n  crisp check-file      Check one or more text/Markdown files: crisp check-file [--json] <file> [...] (\"-\" reads stdin)\n  crisp version         Print the version\n\nEnvironment:\n  QUALITY_GATE_BASE_URL, QUALITY_GATE_API_KEY, QUALITY_GATE_MODEL  (critic endpoint; unset = fail-open)\n  QUALITY_GATE_MODE=balanced|economy\n  QUALITY_GATE_CRITIC_TIMEOUT_MS=8000\n  QUALITY_GATE_MIN_CRITIC_RUNES=120\n  QUALITY_GATE_TOTAL_TIMEOUT_MS=25000\n")
}
