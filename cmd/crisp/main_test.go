package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XiaoleC05/Crisp/internal/checker"
	"github.com/XiaoleC05/Crisp/internal/policy"
)

// goodAnswer is a fact-dense technical answer used for pass-path tests. It is
// longer than the critic threshold and must not trip any deterministic rule.
const goodAnswer = "这个问题通常和连接池的配置有关。先检查 database/sql 的 SetMaxOpenConns 和 SetMaxIdleConns：如果最大连接数远小于并发请求数，请求就会在获取连接时排队。可以把最大连接数调到与数据库 max_connections 匹配的值，再把连接的最大存活时间设置为小于数据库或负载均衡的空闲断开时间。最后用 pprof 或连接池指标确认排队时间是否下降，并观察数据库端的连接数是否出现尖峰。调整时一次只改一个参数，方便定位回退。"

const shortAnswer = "用 context.WithTimeout 包住请求即可，超时后记得检查 ctx.Err()。"

func materialVerdict() string {
	return `{"decision":"block","severity":"material","issues":[{"type":"semantic_duplicate","location":"paragraph_2","instruction":"与第一段观点重复，应合并"}]}`
}

func passVerdict() string {
	return `{"decision":"pass","severity":"none","issues":[]}`
}

func chatResponse(content string) string {
	payload := map[string]any{
		"id":    "chatcmpl-test",
		"model": "mock-model",
		"choices": []any{
			map[string]any{
				"index":         0,
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": content},
			},
		},
		"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(b)
}

type recordedRequest struct {
	path   string
	auth   string
	body   string
	method string
}

// mockCritic is a hermetic OpenAI-compatible chat completions server.
type mockCritic struct {
	server   *httptest.Server
	calls    atomic.Int32
	mu       sync.Mutex
	requests []recordedRequest
}

func newMockCritic(t *testing.T, respond func(call int, requestBody string) (status int, contentType string, body string)) *mockCritic {
	t.Helper()
	m := &mockCritic{}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		call := int(m.calls.Add(1))
		m.mu.Lock()
		m.requests = append(m.requests, recordedRequest{
			path: r.URL.Path, auth: r.Header.Get("Authorization"), body: string(body), method: r.Method,
		})
		m.mu.Unlock()
		status, contentType, responseBody := respond(call, string(body))
		if contentType == "" {
			contentType = "application/json"
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(m.server.Close)
	return m
}

func (m *mockCritic) configure(t *testing.T) {
	t.Helper()
	t.Setenv("QUALITY_GATE_BASE_URL", m.server.URL)
	t.Setenv("QUALITY_GATE_API_KEY", "test-key")
	t.Setenv("QUALITY_GATE_MODEL", "mock-model")
	t.Setenv("QUALITY_GATE_MODE", policy.ModeBalanced)
	t.Setenv("QUALITY_GATE_CRITIC_TIMEOUT_MS", "5000")
	t.Setenv("QUALITY_GATE_TOTAL_TIMEOUT_MS", "20000")
}

func (m *mockCritic) lastRequest(t *testing.T) recordedRequest {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.requests) == 0 {
		t.Fatal("critic was not called")
	}
	return m.requests[len(m.requests)-1]
}

func runCrisp(t *testing.T, args []string, stdin string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code = run(args, strings.NewReader(stdin), &out, &errBuf)
	return out.String(), errBuf.String(), code
}

func hookInput(t *testing.T, fields map[string]any) string {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal hook input: %v", err)
	}
	return string(b)
}

func baseHookInput(answer string) map[string]any {
	return map[string]any{
		"session_id":             "sess-test",
		"turn_id":                "turn-test",
		"transcript_path":        nil,
		"stop_hook_active":       false,
		"last_assistant_message": answer,
	}
}

func assertPassOutput(t *testing.T, stdout string) {
	t.Helper()
	trimmed := strings.TrimSpace(stdout)
	if trimmed != "{}" {
		t.Fatalf("expected pass output {}, got %q", stdout)
	}
	if !json.Valid([]byte(trimmed)) {
		t.Fatalf("pass output is not valid JSON: %q", stdout)
	}
}

func findLogEvent(t *testing.T, stderr, event string) map[string]any {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(stderr))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var found map[string]any
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("stderr line is not JSON: %v (%q)", err, line)
		}
		if entry["event"] == event {
			found = entry
		}
	}
	if found == nil {
		t.Fatalf("stderr did not contain event %q: %s", event, stderr)
	}
	return found
}

func TestHookGuardPasses(t *testing.T) {
	cases := []struct {
		name        string
		input       func(t *testing.T) string
		wantLog     string
		wantNoCalls bool
	}{
		{
			name: "stop_hook_active true short circuits",
			input: func(t *testing.T) string {
				fields := baseHookInput(goodAnswer)
				fields["stop_hook_active"] = true
				return hookInput(t, fields)
			},
			wantLog: "quality_skip",
		},
		{
			name: "missing stop_hook_active fails open",
			input: func(t *testing.T) string {
				fields := baseHookInput(goodAnswer)
				delete(fields, "stop_hook_active")
				return hookInput(t, fields)
			},
			wantLog: "stop_hook_active_missing",
		},
		{
			name: "missing last_assistant_message passes",
			input: func(t *testing.T) string {
				fields := baseHookInput(goodAnswer)
				delete(fields, "last_assistant_message")
				return hookInput(t, fields)
			},
			wantLog: "quality_skip",
		},
		{
			name: "empty last_assistant_message passes",
			input: func(t *testing.T) string {
				return hookInput(t, baseHookInput("   "))
			},
			wantLog: "quality_skip",
		},
		{
			name:    "invalid hook input passes",
			input:   func(t *testing.T) string { return "this is not json" },
			wantLog: "hook_input_parse_failed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockCritic(t, func(int, string) (int, string, string) {
				return 200, "application/json", chatResponse(materialVerdict())
			})
			m.configure(t)
			stdout, stderr, code := runCrisp(t, nil, tc.input(t))
			if code != 0 {
				t.Fatalf("hook must exit 0, got %d", code)
			}
			assertPassOutput(t, stdout)
			_ = findLogEvent(t, stderr, tc.wantLog)
			if calls := m.calls.Load(); calls != 0 {
				t.Fatalf("critic must not be called in guard path, got %d calls", calls)
			}
		})
	}
}

func TestHookPassesCleanAnswer(t *testing.T) {
	m := newMockCritic(t, func(int, string) (int, string, string) {
		return 200, "application/json", chatResponse(passVerdict())
	})
	m.configure(t)
	stdout, stderr, _ := runCrisp(t, nil, hookInput(t, baseHookInput(goodAnswer)))
	assertPassOutput(t, stdout)
	event := findLogEvent(t, stderr, "quality_check")
	if event["decision"] != "pass" || event["critic_called"] != true {
		t.Fatalf("unexpected quality_check event: %v", event)
	}
	if calls := m.calls.Load(); calls != 1 {
		t.Fatalf("expected exactly one critic call, got %d", calls)
	}
	req := m.lastRequest(t)
	if req.method != http.MethodPost {
		t.Fatalf("unexpected critic method %q", req.method)
	}
	if !strings.HasSuffix(req.path, "/chat/completions") {
		t.Fatalf("unexpected critic path %q", req.path)
	}
	if req.auth != "Bearer test-key" {
		t.Fatalf("missing bearer auth header: %q", req.auth)
	}
	if !strings.Contains(req.body, "mock-model") || !strings.Contains(req.body, goodAnswer) {
		t.Fatalf("critic request body missing model or answer")
	}
}

func TestHookBlocksMaterialFinding(t *testing.T) {
	m := newMockCritic(t, func(int, string) (int, string, string) {
		return 200, "application/json", chatResponse(materialVerdict())
	})
	m.configure(t)
	stdout, stderr, code := runCrisp(t, nil, hookInput(t, baseHookInput(goodAnswer)))
	if code != 0 {
		t.Fatalf("hook must exit 0, got %d", code)
	}
	trimmed := strings.TrimSpace(stdout)
	if !json.Valid([]byte(trimmed)) {
		t.Fatalf("block output is not valid JSON: %q", stdout)
	}
	var out struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		t.Fatalf("unmarshal block output: %v", err)
	}
	if out.Decision != "block" {
		t.Fatalf("expected block decision, got %q", out.Decision)
	}
	if out.Reason != policy.RewriteReason {
		t.Fatalf("unexpected rewrite reason: %q", out.Reason)
	}
	event := findLogEvent(t, stderr, "quality_check")
	if event["decision"] != "block" || event["reason_type"] != "semantic_duplicate" {
		t.Fatalf("unexpected quality_check event: %v", event)
	}
	if strings.Contains(stderr, goodAnswer) {
		t.Fatal("stderr must not contain the full answer text")
	}
}

func TestHookFailsOpenOnCriticProblems(t *testing.T) {
	cases := []struct {
		name    string
		respond func(call int, body string) (int, string, string)
		setup   func(t *testing.T)
	}{
		{
			name: "critic returns invalid json",
			respond: func(int, string) (int, string, string) {
				return 200, "application/json", chatResponse("not-a-json-verdict")
			},
		},
		{
			name: "critic returns unknown decision",
			respond: func(int, string) (int, string, string) {
				return 200, "application/json", chatResponse(`{"decision":"maybe","severity":"material"}`)
			},
		},
		{
			name: "critic http 500",
			respond: func(int, string) (int, string, string) {
				return 500, "application/json", `{"error":"internal"}`
			},
		},
		{
			name: "critic timeout",
			respond: func(int, string) (int, string, string) {
				time.Sleep(300 * time.Millisecond)
				return 200, "application/json", chatResponse(passVerdict())
			},
			setup: func(t *testing.T) {
				t.Setenv("QUALITY_GATE_CRITIC_TIMEOUT_MS", "50")
			},
		},
		{
			name: "critic not configured",
			respond: func(int, string) (int, string, string) {
				return 200, "application/json", chatResponse(passVerdict())
			},
			setup: func(t *testing.T) {
				t.Setenv("QUALITY_GATE_BASE_URL", "")
				t.Setenv("QUALITY_GATE_MODEL", "")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockCritic(t, tc.respond)
			m.configure(t)
			if tc.setup != nil {
				tc.setup(t)
			}
			stdout, stderr, code := runCrisp(t, nil, hookInput(t, baseHookInput(goodAnswer)))
			if code != 0 {
				t.Fatalf("hook must exit 0, got %d", code)
			}
			assertPassOutput(t, stdout)
			event := findLogEvent(t, stderr, "quality_check")
			if event["fail_open"] != true {
				t.Fatalf("expected fail_open=true, got %v", event)
			}
		})
	}
}

func TestHookSkipsCriticForShortAnswer(t *testing.T) {
	m := newMockCritic(t, func(int, string) (int, string, string) {
		return 200, "application/json", chatResponse(materialVerdict())
	})
	m.configure(t)
	stdout, stderr, _ := runCrisp(t, nil, hookInput(t, baseHookInput(shortAnswer)))
	assertPassOutput(t, stdout)
	if calls := m.calls.Load(); calls != 0 {
		t.Fatalf("short answer must not call critic, got %d calls", calls)
	}
	event := findLogEvent(t, stderr, "quality_check")
	if event["critic_called"] != false {
		t.Fatalf("expected critic_called=false, got %v", event)
	}
}

func TestHookForwardsTranscriptUserMessage(t *testing.T) {
	const userQuestion = "为什么我的 Go 服务内存一直不释放？"
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	transcript := strings.Join([]string{
		`{"role":"user","content":"` + userQuestion + `"}`,
		`{"role":"assistant","content":"先看 pprof"}`,
	}, "\n")
	if err := os.WriteFile(path, []byte(transcript), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	m := newMockCritic(t, func(int, string) (int, string, string) {
		return 200, "application/json", chatResponse(passVerdict())
	})
	m.configure(t)
	fields := baseHookInput(goodAnswer)
	fields["transcript_path"] = path
	stdout, _, _ := runCrisp(t, nil, hookInput(t, fields))
	assertPassOutput(t, stdout)
	req := m.lastRequest(t)
	if !strings.Contains(req.body, userQuestion) {
		t.Fatalf("critic request should include the transcript user message")
	}
}

func TestCheckTextDecisionOutput(t *testing.T) {
	m := newMockCritic(t, func(int, string) (int, string, string) {
		return 200, "application/json", chatResponse(materialVerdict())
	})
	m.configure(t)
	stdout, _, code := runCrisp(t, []string{"check-text"}, goodAnswer)
	if code != 0 {
		t.Fatalf("check-text must exit 0, got %d", code)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &out); err != nil {
		t.Fatalf("check-text output is not JSON: %v", err)
	}
	if out["decision"] != "block" {
		t.Fatalf("expected block decision, got %v", out)
	}
}

func TestVersionCommand(t *testing.T) {
	stdout, _, code := runCrisp(t, []string{"version"}, "")
	if code != 0 {
		t.Fatalf("version exit code = %d", code)
	}
	if !strings.Contains(stdout, "crisp") {
		t.Fatalf("unexpected version output %q", stdout)
	}
}

type evalCase struct {
	ID                  string   `json:"id"`
	Category            string   `json:"category"`
	UserMessage         string   `json:"user_message"`
	Answer              string   `json:"answer"`
	Expected            string   `json:"expected"`
	ExpectedSeverity    string   `json:"expected_severity"`
	ExpectedRuleSignals []string `json:"expected_rule_signals"`
	Notes               string   `json:"notes"`
}

func loadEvalCases(t *testing.T) []evalCase {
	t.Helper()
	dir := filepath.Join("..", "..", "testdata", "cases")
	var cases []evalCase
	for _, name := range []string{"positive.jsonl", "negative.jsonl"} {
		path := filepath.Join(dir, name)
		f, err := os.Open(path)
		if err != nil {
			t.Skipf("samples not available yet: %v", err)
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var c evalCase
			if err := json.Unmarshal([]byte(line), &c); err != nil {
				f.Close()
				t.Fatalf("%s:%d invalid JSON: %v", name, lineNo, err)
			}
			cases = append(cases, c)
		}
		if err := scanner.Err(); err != nil {
			f.Close()
			t.Fatalf("read %s: %v", name, err)
		}
		f.Close()
	}
	return cases
}

// TestRuleEvaluation is the first-layer evaluation from the spec: it measures
// deterministic coverage and false positives over the 30 regression samples.
func TestRuleEvaluation(t *testing.T) {
	cases := loadEvalCases(t)
	if len(cases) != 30 {
		t.Fatalf("expected 30 regression samples, got %d", len(cases))
	}

	type bucket struct {
		total    int
		detected int
		material int
	}
	byCategory := map[string]*bucket{}
	var goodTotal, goodMaterial int

	for _, c := range cases {
		report := checker.Analyze(c.Answer)
		b := byCategory[c.Category]
		if b == nil {
			b = &bucket{}
			byCategory[c.Category] = b
		}
		b.total++
		if len(report.Findings) > 0 {
			b.detected++
		}
		if report.HasMaterial() {
			b.material++
		}
		if c.Expected == "pass" {
			goodTotal++
			if report.HasMaterial() {
				goodMaterial++
			}
		}
	}

	for category, b := range byCategory {
		t.Logf("category=%-22s total=%d rule_signals=%d material=%d", category, b.total, b.detected, b.material)
	}
	t.Logf("false_positive_material=%d/%d", goodMaterial, goodTotal)

	if goodMaterial != 0 {
		t.Fatalf("normal answers must never produce material rule findings, got %d/%d", goodMaterial, goodTotal)
	}

	for _, category := range []string{"duplicate_conclusion", "filler_opening"} {
		b := byCategory[category]
		if b == nil || b.detected < 4 {
			got := 0
			if b != nil {
				got = b.detected
			}
			t.Fatalf("expected deterministic coverage >= 4/5 for %s, got %d", category, got)
		}
	}
}
