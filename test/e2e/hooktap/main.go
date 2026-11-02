// Command hooktap is a test-only Codex Stop hook wrapper used by the Crisp
// Codex E2E harness.
//
// It records a privacy-preserving summary of the hook payload that Codex sends
// (field presence, answer length and a hash - never the answer itself), runs the
// real crisp binary with the same stdin, relays crisp's stdout/stderr, and
// records crisp's decision so the harness can assert on it. Every diagnostic
// goes to stderr or the capture file; stdout stays a single JSON object as the
// hook protocol requires.
//
// The shipped hook configuration points at bin/crisp.exe directly. hooktap
// exists only to make the protocol observable during E2E runs.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/exec"
	"sort"
	"time"
)

const maxHookInput = 8 << 20

// capture is one JSONL record in the -out file. Two records are written per
// invocation: an immediate "start" record (so a killed hook still leaves
// evidence) and an "end" record with the decision details.
type capture struct {
	Event          string   `json:"event"`
	TS             string   `json:"ts"`
	HookEventName  string   `json:"hook_event_name,omitempty"`
	Cwd            string   `json:"cwd,omitempty"`
	SessionID      string   `json:"session_id,omitempty"`
	TurnID         string   `json:"turn_id,omitempty"`
	Model          string   `json:"model,omitempty"`
	PermissionMode string   `json:"permission_mode,omitempty"`
	StopHookActive *bool    `json:"stop_hook_active"`
	TranscriptPath *string  `json:"transcript_path"`
	InputBytes     int      `json:"input_bytes"`
	TopLevelKeys   []string `json:"top_level_keys"`
	// LastAssistantMessage metadata only: the harness never persists answer
	// text, and neither does this tap.
	LastAssistantMessagePresent bool   `json:"last_assistant_message_present"`
	LastAssistantMessageRunes   int    `json:"last_assistant_message_runes"`
	LastAssistantMessageSHA256  string `json:"last_assistant_message_sha256_16"`
	CrispPath                   string `json:"crisp_path,omitempty"`
	SleepMS                     int64  `json:"sleep_ms,omitempty"`
	// End-record fields.
	CrispExitCode  *int   `json:"crisp_exit_code,omitempty"`
	CrispElapsedMS int64  `json:"crisp_elapsed_ms,omitempty"`
	CrispStdout    string `json:"crisp_stdout,omitempty"`
	CrispStderr    string `json:"crisp_stderr,omitempty"`
	Decision       string `json:"decision,omitempty"`
	OutputOverride bool   `json:"output_override,omitempty"`
}

func main() {
	crispPath := flag.String("crisp", "", "path to the crisp binary; empty means tap only")
	outPath := flag.String("out", "", "required path to the JSONL capture file")
	sleepMS := flag.Int("sleep-ms", 0, "test-only delay before invoking crisp (timeout tests)")
	stdoutFile := flag.String("stdout-file", "", "test-only: print this file's contents instead of crisp's stdout (timeout fail-open tests)")
	flag.Parse()

	if *outPath == "" {
		fail("hooktap: -out is required")
	}

	started := time.Now()
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, maxHookInput+1))
	if err != nil {
		fail("hooktap: read stdin: " + err.Error())
	}
	if len(raw) > maxHookInput {
		raw = raw[:maxHookInput]
	}

	base := parseCapture(raw)
	base.Event = "start"
	base.TS = started.UTC().Format(time.RFC3339Nano)
	base.InputBytes = len(raw)
	base.CrispPath = *crispPath
	appendRecord(*outPath, base)

	var sleepDuration time.Duration
	if *sleepMS > 0 {
		sleepDuration = time.Duration(*sleepMS) * time.Millisecond
		time.Sleep(sleepDuration)
	}

	stdout, stderr, exitCode, elapsedMS := runCrisp(*crispPath, raw)

	end := base
	end.Event = "end"
	end.TS = time.Now().UTC().Format(time.RFC3339Nano)
	end.SleepMS = sleepDuration.Milliseconds()
	end.CrispExitCode = &exitCode
	end.CrispElapsedMS = elapsedMS
	end.CrispStdout = string(stdout)
	end.CrispStderr = string(stderr)
	if decision := parseDecision(stdout); decision != "" {
		end.Decision = decision
	}

	output := stdout
	// -stdout-file provides a synthetic hook decision exactly once (the file is
	// consumed). A consumed file lets a timeout test emit a late block that
	// Codex ignores, and lets the subsequent Stop after a rewrite fall back to
	// crisp's real output, so the harness cannot create an infinite rewrite
	// loop.
	if *stdoutFile != "" {
		if override, err := os.ReadFile(*stdoutFile); err == nil {
			_ = os.Remove(*stdoutFile)
			output = override
			if len(bytes.TrimSpace(output)) == 0 {
				output = []byte("{}\n")
			}
			if output[len(output)-1] != '\n' {
				output = append(output, '\n')
			}
			end.OutputOverride = true
		}
	}
	if len(bytes.TrimSpace(output)) == 0 {
		// Fail open if the tapped binary produced nothing: Codex always needs
		// one valid hook output object.
		output = []byte("{}\n")
	}
	appendRecord(*outPath, end)

	_, _ = os.Stdout.Write(output)
	if len(stderr) > 0 {
		_, _ = os.Stderr.Write(stderr)
	}
}

func parseCapture(raw []byte) capture {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return capture{}
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	c := capture{TopLevelKeys: keys, InputBytes: len(raw)}
	decodeString := func(key string) string {
		var v string
		_ = json.Unmarshal(fields[key], &v)
		return v
	}
	c.HookEventName = decodeString("hook_event_name")
	c.Cwd = decodeString("cwd")
	c.SessionID = decodeString("session_id")
	c.TurnID = decodeString("turn_id")
	c.Model = decodeString("model")
	c.PermissionMode = decodeString("permission_mode")
	if raw, ok := fields["stop_hook_active"]; ok && string(raw) != "null" {
		var b bool
		if err := json.Unmarshal(raw, &b); err == nil {
			c.StopHookActive = &b
		}
	}
	if raw, ok := fields["transcript_path"]; ok && string(raw) != "null" {
		var p string
		if err := json.Unmarshal(raw, &p); err == nil {
			c.TranscriptPath = &p
		}
	}
	if raw, ok := fields["last_assistant_message"]; ok && string(raw) != "null" {
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			c.LastAssistantMessagePresent = true
			c.LastAssistantMessageRunes = runeLen(text)
			sum := sha256.Sum256([]byte(text))
			c.LastAssistantMessageSHA256 = hex.EncodeToString(sum[:])[:16]
		}
	}
	return c
}

func runCrisp(crispPath string, input []byte) (stdout, stderr []byte, exitCode int, elapsedMS int64) {
	if crispPath == "" {
		return nil, nil, 0, 0
	}
	cmd := exec.Command(crispPath)
	cmd.Stdin = bytes.NewReader(input)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	start := time.Now()
	err := cmd.Run()
	elapsedMS = time.Since(start).Milliseconds()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
			errBuf.WriteString("hooktap: run crisp: " + err.Error())
		}
	}
	return outBuf.Bytes(), errBuf.Bytes(), exitCode, elapsedMS
}

func parseDecision(stdout []byte) string {
	var out struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &out); err != nil {
		return ""
	}
	if out.Decision == "" {
		return "pass"
	}
	return out.Decision
}

func appendRecord(path string, rec capture) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// fail always exits 0 with a valid pass response: a broken tap must never
// turn into a blocking hook.
func fail(msg string) {
	_, _ = io.WriteString(os.Stderr, msg+"\n")
	_, _ = io.WriteString(os.Stdout, "{}\n")
	os.Exit(0)
}
