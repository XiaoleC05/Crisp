// Command hookreplay is the deterministic protocol-replay harness for the Crisp
// Codex Stop hook contract.
//
// It feeds realistic Codex Stop hook JSON into the real crisp binary and
// asserts both the exact stdout decision and the structured stderr events. It
// runs fully offline (the critic is an in-process httptest server) and covers
// the docs/design.md section 12.4 scenarios plus fail-open and idempotency
// behavior.
//
// It is a complement to, not a replacement for, the real CLI E2E in
// scripts/e2e-codex.ps1: it proves Crisp's side of the contract deterministically
// on every run, while the CLI E2E proves Codex actually invokes the hook.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

// rewriteReason must match policy.RewriteReason. The block stdout is asserted
// byte-for-byte so a silent prompt change in Crisp is caught here.
const rewriteReason = "请重新输出完整最终回答：合并语义重复的段落，删除无效铺垫和结论重复，保留全部必要事实、数字、代码和解释，不要为了缩短篇幅而删除必要信息。"

const blockStdout = `{"decision":"block","reason":"请重新输出完整最终回答：合并语义重复的段落，删除无效铺垫和结论重复，保留全部必要事实、数字、代码和解释，不要为了缩短篇幅而删除必要信息。"}`

const passStdout = "{}"

// cleanAnswer is a normal, information-dense Chinese answer with no duplicate
// sentences or filler, so the deterministic rules report nothing and only the
// balanced-mode length threshold sends it to the critic.
const cleanAnswer = "输入校验分两步：先检查类型，再检查范围。类型不符直接报错，范围越界给出可读提示；两条错误信息都包含字段名，便于定位。" +
	"错误码稳定用于程序分支，文案稳定用于用户阅读。校验失败时返回第一个错误，并记录完整字段路径；校验通过则原样返回输入。" +
	"对可选字段，缺失与显式空值要区分处理：缺失表示沿用默认值，显式空值表示清空。两条规则都写进接口文档，避免调用方理解不一致。"

// flawedAnswer repeats two paragraphs verbatim, which produces material
// duplicate_paragraph/duplicate_sentence findings and forces a critic call.
const flawedAnswer = `这个问题可以从三个层面来看。首先，需要确认输入数据的类型和取值范围；其次，需要处理空值、越界、格式错误等边界情况；最后，需要把校验结果稳定地输出给调用方，并附上可读的错误信息。

这个问题可以从三个层面来看。首先，需要确认输入数据的类型和取值范围；其次，需要处理空值、越界、格式错误等边界情况；最后，需要把校验结果稳定地输出给调用方，并附上可读的错误信息。`

// markerPhrases must never appear in stderr: the harness asserts the answer
// text is not logged.
var markerPhrases = []string{"输入校验分两步", "需要确认输入数据的类型和取值范围"}

type replayCase struct {
	name        string
	stdin       string
	criticMode  string
	criticDelay time.Duration
	noCritic    bool
	env         map[string]string

	wantStdout      string
	wantEvents      []string
	forbidEvents    []string
	wantQuality     map[string]any
	wantCriticCalls int64
	timeoutMS       int64
}

type outcome struct {
	event string
	line  map[string]any
}

func main() {
	crispPath := flag.String("crisp", "bin/crisp.exe", "path to the crisp binary under test")
	verbose := flag.Bool("v", false, "print each stderr event line")
	flag.Parse()

	started := time.Now()
	cases := buildCases()
	failures := 0
	for _, tc := range cases {
		if err := runCase(*crispPath, tc, *verbose); err != nil {
			failures++
			fmt.Printf("FAIL %-28v %v\n", tc.name, err)
			continue
		}
		fmt.Printf("PASS %-28v\n", tc.name)
	}
	fmt.Println()
	fmt.Printf("%d/%d replay cases passed in %dms\n", len(cases)-failures, len(cases), time.Since(started).Milliseconds())
	if failures > 0 {
		os.Exit(1)
	}
}

// meta renders a realistic Stop hook payload with the exact field set Codex
// 0.145.0-alpha.18 sends (captured in docs/integrations/codex-cli.md).
func meta(answer string, stopHookActive *bool, omitStopHookActive bool) string {
	obj := map[string]any{
		"cwd":                    `D:\05_Code\Crisp`,
		"hook_event_name":        "Stop",
		"model":                  "mock-model",
		"permission_mode":        "bypassPermissions",
		"session_id":             "sess-replay-0001",
		"turn_id":                "turn-replay-0001",
		"transcript_path":        nil,
		"last_assistant_message": answer,
	}
	if !omitStopHookActive {
		obj["stop_hook_active"] = stopHookActive != nil && *stopHookActive
	}
	b, _ := json.Marshal(obj)
	return string(b)
}

func boolPtr(v bool) *bool { return &v }

func buildCases() []replayCase {
	activeFalse := boolPtr(false)
	activeTrue := boolPtr(true)
	return []replayCase{
		{
			name:            "pass_clean_balanced",
			stdin:           meta(cleanAnswer, activeFalse, false),
			criticMode:      "pass",
			wantStdout:      passStdout,
			wantEvents:      []string{"quality_check"},
			wantQuality:     map[string]any{"decision": "pass", "fail_open": false, "critic_called": true},
			wantCriticCalls: 1,
		},
		{
			name:            "pass_short_answer",
			stdin:           meta("好的。", activeFalse, false),
			criticMode:      "pass",
			wantStdout:      passStdout,
			wantEvents:      []string{"quality_check"},
			wantQuality:     map[string]any{"decision": "pass", "fail_open": false, "critic_called": false, "reason_type": "not_reviewed"},
			wantCriticCalls: 0,
		},
		{
			name:            "block_material",
			stdin:           meta(flawedAnswer, activeFalse, false),
			criticMode:      "block_material",
			wantStdout:      blockStdout,
			wantEvents:      []string{"quality_check"},
			wantQuality:     map[string]any{"decision": "block", "fail_open": false, "critic_called": true, "reason_type": "semantic_duplicate"},
			wantCriticCalls: 1,
		},
		{
			name:            "block_minor_passes",
			stdin:           meta(flawedAnswer, activeFalse, false),
			criticMode:      "block_minor",
			wantStdout:      passStdout,
			wantEvents:      []string{"quality_check"},
			wantQuality:     map[string]any{"decision": "pass", "fail_open": false, "critic_called": true},
			wantCriticCalls: 1,
		},
		{
			name:            "loop_guard_active",
			stdin:           meta(flawedAnswer, activeTrue, false),
			criticMode:      "block_material",
			wantStdout:      passStdout,
			wantEvents:      []string{"quality_skip"},
			wantQuality:     map[string]any{"skip_reason": "stop_hook_active"},
			wantCriticCalls: 0,
		},
		{
			name:         "stop_hook_active_missing",
			stdin:        meta(cleanAnswer, nil, true),
			criticMode:   "pass",
			wantStdout:   passStdout,
			wantEvents:   []string{"protocol_anomaly", "stop_hook_active_missing"},
			forbidEvents: []string{"quality_check"},
		},
		{
			name:       "empty_last_assistant_message",
			stdin:      meta("", activeFalse, false),
			criticMode: "pass",
			wantStdout: passStdout,
			wantEvents: []string{"quality_skip"},
			wantQuality: map[string]any{
				"skip_reason": "empty_last_assistant_message",
			},
		},
		{
			name:         "invalid_hook_json",
			stdin:        "{not json at all",
			wantStdout:   passStdout,
			wantEvents:   []string{"hook_input_parse_failed"},
			forbidEvents: []string{"quality_check"},
		},
		{
			name:        "critic_unconfigured",
			stdin:       meta(cleanAnswer, activeFalse, false),
			noCritic:    true,
			wantStdout:  passStdout,
			wantEvents:  []string{"critic_unavailable", "quality_check"},
			wantQuality: map[string]any{"decision": "pass", "fail_open": true, "critic_called": false},
			timeoutMS:   4000,
		},
		{
			name:            "critic_timeout",
			stdin:           meta(cleanAnswer, activeFalse, false),
			criticMode:      "timeout",
			criticDelay:     500 * time.Millisecond,
			env:             map[string]string{"QUALITY_GATE_CRITIC_TIMEOUT_MS": "100"},
			wantStdout:      passStdout,
			wantEvents:      []string{"critic_failed", "quality_check"},
			wantQuality:     map[string]any{"decision": "pass", "fail_open": true, "critic_called": true},
			timeoutMS:       3000,
			wantCriticCalls: 1,
		},
		{
			name:            "critic_invalid_json",
			stdin:           meta(cleanAnswer, activeFalse, false),
			criticMode:      "invalid",
			wantStdout:      passStdout,
			wantEvents:      []string{"critic_failed", "quality_check"},
			wantQuality:     map[string]any{"decision": "pass", "fail_open": true, "critic_called": true},
			timeoutMS:       4000,
			wantCriticCalls: 1,
		},
		{
			name:            "critic_http_500",
			stdin:           meta(cleanAnswer, activeFalse, false),
			criticMode:      "http500",
			wantStdout:      passStdout,
			wantEvents:      []string{"critic_failed", "quality_check"},
			wantQuality:     map[string]any{"decision": "pass", "fail_open": true, "critic_called": true},
			timeoutMS:       4000,
			wantCriticCalls: 1,
		},
	}
}

func runCase(crispPath string, tc replayCase, verbose bool) error {
	var calls int64
	var criticURL string
	if !tc.noCritic {
		server := newCriticServer(tc.criticMode, tc.criticDelay, &calls)
		defer server.Close()
		criticURL = server.URL + "/v1"
	}

	env := baseEnv()
	if criticURL != "" {
		env = append(env, "QUALITY_GATE_BASE_URL="+criticURL, "QUALITY_GATE_MODEL=mock-critic")
	}
	for k, v := range tc.env {
		env = append(env, k+"="+v)
	}

	started := time.Now()
	cmd := exec.Command(crispPath)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(tc.stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	exitErr := cmd.Run()
	elapsed := time.Since(started)
	if exitErr != nil {
		return fmt.Errorf("crisp exited with error: %w", exitErr)
	}
	if tc.timeoutMS > 0 && elapsed.Milliseconds() > tc.timeoutMS {
		return fmt.Errorf("crisp took %dms, expected under %dms", elapsed.Milliseconds(), tc.timeoutMS)
	}

	gotStdout := strings.TrimSpace(stdout.String())
	if gotStdout != tc.wantStdout {
		return fmt.Errorf("stdout = %q, want %q", truncate(gotStdout, 400), truncate(tc.wantStdout, 400))
	}
	var decisionObj map[string]any
	if err := json.Unmarshal([]byte(gotStdout), &decisionObj); err != nil {
		return fmt.Errorf("stdout is not one JSON object: %w", err)
	}

	outcomes, rawLines, err := parseStderr(stderr.String())
	if err != nil {
		return err
	}
	if verbose {
		for _, line := range rawLines {
			fmt.Println("   stderr:", line)
		}
	}
	have := map[string]bool{}
	byEvent := map[string][]map[string]any{}
	for _, o := range outcomes {
		have[o.event] = true
		byEvent[o.event] = append(byEvent[o.event], o.line)
	}
	for _, want := range tc.wantEvents {
		if !have[want] {
			return fmt.Errorf("missing stderr event %q (got %v)", want, eventNames(outcomes))
		}
	}
	for _, forbid := range tc.forbidEvents {
		if have[forbid] {
			return fmt.Errorf("unexpected stderr event %q", forbid)
		}
	}
	if len(tc.wantQuality) > 0 {
		event := "quality_check"
		if _, ok := tc.wantQuality["skip_reason"]; ok {
			event = "quality_skip"
		}
		lines := byEvent[event]
		if len(lines) == 0 {
			return fmt.Errorf("missing %q event required by quality assertions", event)
		}
		line := lines[len(lines)-1]
		for key, want := range tc.wantQuality {
			if got := line[key]; !jsonEqual(got, want) {
				return fmt.Errorf("%s event field %q = %v, want %v", event, key, got, want)
			}
		}
	}
	if tc.wantCriticCalls >= 0 && !tc.noCritic {
		if got := atomic.LoadInt64(&calls); got != tc.wantCriticCalls {
			return fmt.Errorf("critic calls = %d, want %d", got, tc.wantCriticCalls)
		}
	}
	for _, marker := range markerPhrases {
		if strings.Contains(stderr.String(), marker) {
			return fmt.Errorf("stderr leaked answer text (%q)", truncate(marker, 20))
		}
	}
	return nil
}

func baseEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "QUALITY_GATE_") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// parseStderr consumes one JSON object per stderr line. Crisp promises exactly
// that shape, so a non-JSON line is a contract violation.
func parseStderr(raw string) ([]outcome, []string, error) {
	var outcomes []outcome
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return nil, lines, fmt.Errorf("stderr line is not JSON: %q", truncate(line, 200))
		}
		event, _ := m["event"].(string)
		if event == "" {
			return nil, lines, fmt.Errorf("stderr JSON line has no event: %q", truncate(line, 200))
		}
		outcomes = append(outcomes, outcome{event: event, line: m})
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, lines, fmt.Errorf("read stderr: %w", err)
	}
	return outcomes, lines, nil
}

func eventNames(outcomes []outcome) []string {
	names := make([]string, 0, len(outcomes))
	for _, o := range outcomes {
		names = append(names, o.event)
	}
	return names
}

func jsonEqual(got any, want any) bool {
	g, err := json.Marshal(got)
	if err != nil {
		return false
	}
	w, err := json.Marshal(want)
	if err != nil {
		return false
	}
	return bytes.Equal(g, w)
}

func truncate(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "..."
}

// newCriticServer serves an OpenAI-compatible chat completion whose content is
// the requested critic verdict. It counts calls so the harness can prove the
// hook short-circuits before calling the critic.
func newCriticServer(mode string, delay time.Duration, calls *int64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(calls, 1)
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		if mode == "http500" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"mock critic failure","type":"server_error"}}`))
			return
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		writeCompletion(w, verdictContent(mode))
	}))
}

func verdictContent(mode string) string {
	switch mode {
	case "block_material":
		return `{"decision":"block","severity":"material","issues":[{"type":"semantic_duplicate","location":"paragraph_2","instruction":"Merge with paragraph 1."}]}`
	case "block_minor":
		return `{"decision":"block","severity":"minor","issues":[{"type":"semantic_duplicate","location":"paragraph_2","instruction":"Merge with paragraph 1."}]}`
	case "invalid":
		return "this is not a valid verdict"
	default:
		return `{"decision":"pass","severity":"none","issues":[]}`
	}
}

func writeCompletion(w http.ResponseWriter, content string) {
	resp := map[string]any{
		"id":      "chatcmpl-replay",
		"object":  "chat.completion",
		"created": 0,
		"model":   "mock-critic",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
