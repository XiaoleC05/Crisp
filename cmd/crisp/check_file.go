package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/XiaoleC05/Crisp/internal/checker"
	"github.com/XiaoleC05/Crisp/internal/logger"
)

// maxCheckFileBytes bounds one file read by check-file. Larger files are
// reported as errors (fail-open) rather than silently truncated.
const maxCheckFileBytes = 4 << 20

// checkFileResult is the quality result of a single file.
type checkFileResult struct {
	Path            string                `json:"path"`
	Decision        string                `json:"decision"`
	ReasonType      string                `json:"reason_type,omitempty"`
	RuleTypes       []checker.FindingType `json:"rule_types"`
	RuleMaterial    bool                  `json:"rule_material"`
	CriticCalled    bool                  `json:"critic_called"`
	FailOpen        bool                  `json:"fail_open"`
	AnswerRunes     int                   `json:"answer_runes"`
	NaturalRunes    int                   `json:"natural_runes"`
	CriticLatencyMS int64                 `json:"critic_latency_ms"`
	CriticTokens    int                   `json:"critic_tokens"`
	ElapsedMS       int64                 `json:"elapsed_ms"`
	Error           string                `json:"error,omitempty"`
}

type checkFileSummary struct {
	Checked    int `json:"checked"`
	Blocked    int `json:"blocked"`
	FailedOpen int `json:"failed_open"`
	Errors     int `json:"errors"`
}

type checkFileReport struct {
	Files   []checkFileResult `json:"files"`
	Summary checkFileSummary  `json:"summary"`
}

// parseCheckFileArgs parses the check-file arguments. Flags must precede the
// paths; "-" reads one answer from stdin.
func parseCheckFileArgs(args []string) (paths []string, asJSON bool, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			paths = append(paths, args[i+1:]...)
			return paths, asJSON, nil
		case arg == "-":
			paths = append(paths, arg)
		case arg == "--json" || arg == "-json":
			asJSON = true
		case strings.HasPrefix(arg, "-"):
			return nil, false, fmt.Errorf("unknown flag %q", arg)
		default:
			paths = append(paths, arg)
		}
	}
	if len(paths) == 0 {
		return nil, false, fmt.Errorf("no input file given")
	}
	return paths, asJSON, nil
}

func readCheckFile(path string, stdin io.Reader, stdinConsumed *bool) (string, error) {
	if path == "-" {
		if *stdinConsumed {
			return "", fmt.Errorf("stdin (\"-\") can only be used once")
		}
		*stdinConsumed = true
		data, err := io.ReadAll(io.LimitReader(stdin, maxCheckFileBytes+1))
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		if len(data) > maxCheckFileBytes {
			return "", fmt.Errorf("stdin exceeds %d bytes", maxCheckFileBytes)
		}
		return string(data), nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", path)
	}
	if info.Size() > maxCheckFileBytes {
		return "", fmt.Errorf("%s exceeds %d bytes", path, maxCheckFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// runCheckFile implements "crisp check-file": check one or more files (or
// stdin with "-") and report PASS/BLOCK per file.
//
// Exit codes: 0 all checked without block, 1 at least one block, 2 usage error
// or at least one file could not be read/checked.
func runCheckFile(log *logger.Logger, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	paths, asJSON, err := parseCheckFileArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "crisp check-file: %v\n", err)
		printUsage(stderr)
		return 2
	}

	report := checkFileReport{Files: make([]checkFileResult, 0, len(paths))}
	stdinConsumed := false
	for _, path := range paths {
		result := checkFileResult{Path: path, Decision: "pass", RuleTypes: []checker.FindingType{}}
		answer, readErr := readCheckFile(path, stdin, &stdinConsumed)
		if readErr != nil {
			result.FailOpen = true
			result.Error = readErr.Error()
			log.Error("check_file_read_failed", "failed to read file; failing open", readErr,
				map[string]any{"path": path})
			report.Files = append(report.Files, result)
			report.Summary.Errors++
			continue
		}

		started := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(),
			envDuration("QUALITY_GATE_TOTAL_TIMEOUT_MS", defaultTotalTimeout))
		outcome := reviewAnswer(ctx, answer, "")
		cancel()

		result.Decision = decisionName(outcome.Decision)
		result.ReasonType = outcome.Decision.ReasonType
		result.RuleTypes = outcome.Report.Types()
		result.RuleMaterial = outcome.Report.HasMaterial()
		result.CriticCalled = outcome.CriticCalled
		result.FailOpen = outcome.Decision.FailOpen
		result.AnswerRunes = outcome.Report.Stats.Runes
		result.NaturalRunes = outcome.Report.Stats.NaturalRunes
		result.CriticLatencyMS = verdictLatencyMS(outcome.Verdict)
		result.CriticTokens = verdictTokens(outcome.Verdict)
		result.ElapsedMS = time.Since(started).Milliseconds()

		report.Summary.Checked++
		if outcome.Decision.Block {
			report.Summary.Blocked++
		}
		if outcome.Decision.FailOpen {
			report.Summary.FailedOpen++
		}
		report.Files = append(report.Files, result)
	}

	if asJSON {
		writeJSON(stdout, report)
	} else {
		for _, result := range report.Files {
			if result.Error != "" {
				fmt.Fprintf(stdout, "%s: ERROR (%s)\n", result.Path, result.Error)
				continue
			}
			fmt.Fprintf(stdout, "%s: %s (reason_type=%s; rules=%s; critic=%s; runes=%d; %dms)\n",
				result.Path,
				strings.ToUpper(result.Decision),
				reasonTypeOrDash(result.ReasonType),
				ruleTypesText(result.RuleTypes),
				criticText(result),
				result.AnswerRunes,
				result.ElapsedMS)
		}
		fmt.Fprintf(stdout, "checked=%d blocked=%d fail_open=%d errors=%d\n",
			report.Summary.Checked, report.Summary.Blocked, report.Summary.FailedOpen, report.Summary.Errors)
	}

	if report.Summary.Errors > 0 {
		return 2
	}
	if report.Summary.Blocked > 0 {
		return 1
	}
	return 0
}

func reasonTypeOrDash(reasonType string) string {
	if reasonType == "" {
		return "-"
	}
	return reasonType
}

func ruleTypesText(types []checker.FindingType) string {
	if len(types) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(types))
	for _, t := range types {
		parts = append(parts, string(t))
	}
	return strings.Join(parts, ",")
}

func criticText(result checkFileResult) string {
	if result.FailOpen {
		return "fail-open"
	}
	if !result.CriticCalled {
		return "skipped"
	}
	return result.Decision
}
