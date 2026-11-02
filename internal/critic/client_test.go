package critic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/XiaoleC05/Crisp/internal/checker"
)

const (
	testAPIKey = "test-api-key-value"
	testModel  = "test-model"
)

const passJSON = "{\"decision\":\"pass\",\"severity\":\"none\",\"issues\":[]}"

type capturedChatBody struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Temperature float64 `json:"temperature"`
	Stream      bool    `json:"stream"`
}

type capturedRequest struct {
	path        string
	method      string
	auth        string
	contentType string
	rawBody     []byte
	body        capturedChatBody
}

// newChatServer starts a hermetic chat-completions mock. A non-nil capture
// channel receives the first request. respond writes the response.
func newChatServer(t *testing.T, capture chan<- capturedRequest, respond func(http.ResponseWriter, *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("mock server: read request body: %v", err)
		}
		var body capturedChatBody
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("mock server: decode request body: %v", err)
		}
		if capture != nil {
			captured := capturedRequest{
				path:        r.URL.Path,
				method:      r.Method,
				auth:        r.Header.Get("Authorization"),
				contentType: r.Header.Get("Content-Type"),
				rawBody:     raw,
				body:        body,
			}
			select {
			case capture <- captured:
			default:
			}
		}
		respond(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeCompletion(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "chatcmpl-test",
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": content},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     100,
			"completion_tokens": 20,
			"total_tokens":      120,
		},
	})
}

func passResponder(content string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeCompletion(w, content)
	}
}

func newTestClient(t *testing.T, cfg Config) Client {
	t.Helper()
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func TestReviewSendsExpectedRequest(t *testing.T) {
	capture := make(chan capturedRequest, 1)
	srv := newChatServer(t, capture, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(10 * time.Millisecond)
		writeCompletion(w, passJSON)
	})
	client := newTestClient(t, Config{BaseURL: srv.URL + "/v1", APIKey: testAPIKey, Model: testModel})
	const answer = "ANSWER-MARKER: the deployment uses 42 replicas."
	in := Input{
		Answer:      answer,
		UserMessage: "QUESTION-MARKER: how many replicas?",
		RuleFindings: []checker.Finding{{
			Type:     checker.FindingDuplicateParagraph,
			Severity: checker.SeverityMaterial,
			Location: "paragraph_3",
			Evidence: "EVIDENCE-MARKER",
		}},
	}

	verdict, err := client.Review(context.Background(), in)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if verdict.Decision != DecisionPass || verdict.Severity != SeverityNone {
		t.Fatalf("verdict = %q/%q, want pass/none", verdict.Decision, verdict.Severity)
	}
	if len(verdict.Issues) != 0 {
		t.Fatalf("issues = %d, want 0", len(verdict.Issues))
	}
	if want := (Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}); verdict.Usage != want {
		t.Errorf("usage = %+v, want %+v", verdict.Usage, want)
	}
	if verdict.LatencyMS < 5 {
		t.Errorf("LatencyMS = %d, want >= 5 (server slept 10ms)", verdict.LatencyMS)
	}

	got := <-capture
	if got.path != "/v1/chat/completions" {
		t.Errorf("request path = %q, want /v1/chat/completions", got.path)
	}
	if got.method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.method)
	}
	if got.auth != "Bearer "+testAPIKey {
		t.Errorf("Authorization = %q, want bearer with the configured key", got.auth)
	}
	if !strings.HasPrefix(got.contentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got.contentType)
	}
	if strings.Contains(string(got.rawBody), testAPIKey) {
		t.Error("request body must not contain the API key")
	}
	if got.body.Model != testModel {
		t.Errorf("model = %q, want %q", got.body.Model, testModel)
	}
	if got.body.Temperature != 0 {
		t.Errorf("temperature = %v, want 0", got.body.Temperature)
	}
	if got.body.Stream {
		t.Error("stream = true, want false")
	}
	if len(got.body.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(got.body.Messages))
	}
	if got.body.Messages[0].Role != "system" || got.body.Messages[1].Role != "user" {
		t.Errorf("roles = %q/%q, want system/user", got.body.Messages[0].Role, got.body.Messages[1].Role)
	}
	systemPrompt := got.body.Messages[0].Content
	if !strings.Contains(systemPrompt, "semantic_duplicate") {
		t.Error("system prompt does not describe the quality problems")
	}
	userPrompt := got.body.Messages[1].Content
	for _, want := range []string{"ANSWER-MARKER", "QUESTION-MARKER", "duplicate_paragraph", "paragraph_3", "material"} {
		if !strings.Contains(userPrompt, want) {
			t.Errorf("user prompt does not contain %q", want)
		}
	}
	if strings.Contains(userPrompt, "EVIDENCE-MARKER") {
		t.Error("user prompt must not contain rule evidence")
	}
}

func TestBaseURLJoining(t *testing.T) {
	cases := []struct {
		name     string
		suffix   string
		wantPath string
	}{
		{"no suffix", "", "/chat/completions"},
		{"trailing slash", "/", "/chat/completions"},
		{"v1 prefix", "/v1", "/v1/chat/completions"},
		{"v1 prefix trailing slash", "/v1/", "/v1/chat/completions"},
		{"custom version prefix", "/openai/v1", "/openai/v1/chat/completions"},
		{"already complete path", "/v1/chat/completions", "/v1/chat/completions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := make(chan capturedRequest, 1)
			srv := newChatServer(t, capture, passResponder(passJSON))
			client := newTestClient(t, Config{BaseURL: srv.URL + tc.suffix, Model: testModel})
			if _, err := client.Review(context.Background(), Input{Answer: "answer"}); err != nil {
				t.Fatalf("Review: %v", err)
			}
			if got := (<-capture).path; got != tc.wantPath {
				t.Errorf("request path = %q, want %q", got, tc.wantPath)
			}
		})
	}
}

func TestAuthorizationHeaderIsOptional(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want string
	}{
		{"with key", "local-secret", "Bearer local-secret"},
		{"without key", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture := make(chan capturedRequest, 1)
			srv := newChatServer(t, capture, passResponder(passJSON))
			client := newTestClient(t, Config{BaseURL: srv.URL, APIKey: tc.key, Model: testModel})
			if _, err := client.Review(context.Background(), Input{Answer: "answer"}); err != nil {
				t.Fatalf("Review: %v", err)
			}
			if got := (<-capture).auth; got != tc.want {
				t.Errorf("Authorization = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewConfigValidation(t *testing.T) {
	cases := []struct {
		name       string
		cfg        Config
		wantUsable bool
	}{
		{"missing base URL", Config{Model: testModel}, false},
		{"missing model", Config{BaseURL: "http://127.0.0.1:1/v1"}, false},
		{"empty config", Config{}, false},
		{"unsupported scheme", Config{BaseURL: "ftp://example.com/v1", Model: testModel}, false},
		{"missing host", Config{BaseURL: "/v1", Model: testModel}, false},
		{"valid", Config{BaseURL: "http://127.0.0.1:1/v1", Model: testModel}, true},
		{"valid with key", Config{BaseURL: "http://127.0.0.1:1", APIKey: testAPIKey, Model: testModel}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New(tc.cfg)
			if !tc.wantUsable {
				if err == nil {
					t.Fatal("New succeeded, want error")
				}
				if !errors.Is(err, ErrNotConfigured) {
					t.Errorf("error %v does not wrap ErrNotConfigured", err)
				}
				if client != nil {
					t.Error("client is non-nil on error")
				}
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if client == nil {
				t.Fatal("client is nil")
			}
		})
	}
}

func TestNewTimeoutDefaults(t *testing.T) {
	cases := []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{"zero uses default", 0, defaultTimeout},
		{"negative uses default", -time.Second, defaultTimeout},
		{"explicit timeout is kept", 3 * time.Second, 3 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New(Config{BaseURL: "http://127.0.0.1:1/v1", Model: testModel, Timeout: tc.timeout})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			hc, ok := client.(*httpClient)
			if !ok {
				t.Fatalf("client type = %T, want *httpClient", client)
			}
			if hc.timeout != tc.want {
				t.Errorf("timeout = %s, want %s", hc.timeout, tc.want)
			}
			if hc.http.Timeout != tc.want {
				t.Errorf("http.Client.Timeout = %s, want %s", hc.http.Timeout, tc.want)
			}
			if hc.http.CheckRedirect == nil {
				t.Error("redirects must be disabled")
			}
		})
	}
}

func TestReviewVerdictParsing(t *testing.T) {
	cases := []struct {
		name         string
		content      string
		wantDecision string
		wantSeverity string
		wantIssues   int
		wantMaterial bool
	}{
		{
			name:         "pass",
			content:      "{\"decision\":\"pass\",\"severity\":\"none\",\"issues\":[]}",
			wantDecision: DecisionPass,
			wantSeverity: SeverityNone,
			wantIssues:   0,
			wantMaterial: false,
		},
		{
			name:         "minor",
			content:      "{\"decision\":\"pass\",\"severity\":\"minor\",\"issues\":[{\"type\":\"filler\",\"location\":\"opening\",\"instruction\":\"Drop the greeting.\"}]}",
			wantDecision: DecisionPass,
			wantSeverity: SeverityMinor,
			wantIssues:   1,
			wantMaterial: false,
		},
		{
			name:         "material block",
			content:      "{\"decision\":\"block\",\"severity\":\"material\",\"issues\":[{\"type\":\"semantic_duplicate\",\"location\":\"paragraph_3\",\"instruction\":\"Merge with paragraph 1.\"}]}",
			wantDecision: DecisionBlock,
			wantSeverity: SeverityMaterial,
			wantIssues:   1,
			wantMaterial: true,
		},
		{
			name:         "block with minor is not material",
			content:      "{\"decision\":\"block\",\"severity\":\"minor\",\"issues\":[]}",
			wantDecision: DecisionBlock,
			wantSeverity: SeverityMinor,
			wantIssues:   0,
			wantMaterial: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newChatServer(t, nil, passResponder(tc.content))
			client := newTestClient(t, Config{BaseURL: srv.URL, Model: testModel})
			verdict, err := client.Review(context.Background(), Input{Answer: "answer"})
			if err != nil {
				t.Fatalf("Review: %v", err)
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
			if got := verdict.MaterialBlock(); got != tc.wantMaterial {
				t.Errorf("MaterialBlock = %v, want %v", got, tc.wantMaterial)
			}
		})
	}
}

func TestReviewRejectsInvalidVerdicts(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"empty content", ""},
		{"not JSON", "not-json"},
		{"missing decision", "{\"severity\":\"none\",\"issues\":[]}"},
		{"unknown decision", "{\"decision\":\"maybe\",\"severity\":\"none\",\"issues\":[]}"},
		{"unknown severity", "{\"decision\":\"pass\",\"severity\":\"severe\",\"issues\":[]}"},
		{"uppercase decision", "{\"decision\":\"PASS\",\"severity\":\"none\",\"issues\":[]}"},
		{"missing severity", "{\"decision\":\"pass\",\"issues\":[]}"},
		{"missing issues", "{\"decision\":\"pass\",\"severity\":\"none\"}"},
		{"null issues", "{\"decision\":\"pass\",\"severity\":\"none\",\"issues\":null}"},
		{"object issues", "{\"decision\":\"pass\",\"severity\":\"none\",\"issues\":{}}"},
		{"string issues", "{\"decision\":\"pass\",\"severity\":\"none\",\"issues\":\"none\"}"},
		{"null decision", "{\"decision\":null,\"severity\":\"none\",\"issues\":[]}"},
		{"trailing text", "{\"decision\":\"pass\",\"severity\":\"none\",\"issues\":[]} extra"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newChatServer(t, nil, passResponder(tc.content))
			client := newTestClient(t, Config{BaseURL: srv.URL, APIKey: testAPIKey, Model: testModel})
			verdict, err := client.Review(context.Background(), Input{Answer: "ANSWER-MARKER"})
			if err == nil {
				t.Fatalf("Review succeeded with verdict %+v, want error", verdict)
			}
			if strings.Contains(err.Error(), "ANSWER-MARKER") {
				t.Errorf("error leaks the answer text: %v", err)
			}
			if strings.Contains(err.Error(), testAPIKey) {
				t.Errorf("error leaks the API key: %v", err)
			}
		})
	}
}

func TestReviewRejectsBadCompletionEnvelope(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantSubstr string
	}{
		{"not JSON", "not-json", "not valid JSON"},
		{"empty choices", "{\"choices\":[]}", "no choices"},
		{"empty content", "{\"choices\":[{\"message\":{\"content\":\"\"}}]}", "empty content"},
		{"provider error envelope", "{\"error\":{\"message\":\"quota exceeded\"}}", "quota exceeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newChatServer(t, nil, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			})
			client := newTestClient(t, Config{BaseURL: srv.URL, Model: testModel})
			_, err := client.Review(context.Background(), Input{Answer: "answer"})
			if err == nil {
				t.Fatal("Review succeeded, want error")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error = %v, want it to contain %q", err, tc.wantSubstr)
			}
		})
	}
}

func TestReviewHTTPErrors(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantSubstr []string
	}{
		{"server error", http.StatusInternalServerError, "internal boom", []string{"HTTP 500", "internal boom"}},
		{"rate limited", http.StatusTooManyRequests, "{\"error\":\"rate limited\"}", []string{"HTTP 429", "rate limited"}},
		{"redirect is not followed", http.StatusFound, "", []string{"HTTP 302"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newChatServer(t, nil, func(w http.ResponseWriter, _ *http.Request) {
				if tc.status == http.StatusFound {
					w.Header().Set("Location", "http://example.invalid/elsewhere")
					w.WriteHeader(tc.status)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			client := newTestClient(t, Config{BaseURL: srv.URL, APIKey: testAPIKey, Model: testModel})
			_, err := client.Review(context.Background(), Input{Answer: "ANSWER-MARKER"})
			if err == nil {
				t.Fatal("Review succeeded, want error")
			}
			for _, want := range tc.wantSubstr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %v, want it to contain %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "ANSWER-MARKER") || strings.Contains(err.Error(), testAPIKey) {
				t.Errorf("error leaks sensitive data: %v", err)
			}
		})
	}
}

func TestReviewHTTPErrorBodyIsTruncated(t *testing.T) {
	srv := newChatServer(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, strings.Repeat("x", 5000))
	})
	client := newTestClient(t, Config{BaseURL: srv.URL, Model: testModel})
	_, err := client.Review(context.Background(), Input{Answer: "answer"})
	if err == nil {
		t.Fatal("Review succeeded, want error")
	}
	msg := err.Error()
	if len(msg) > 400 {
		t.Errorf("error message is %d bytes, want a truncated body", len(msg))
	}
	if !strings.Contains(msg, "...") {
		t.Errorf("truncated body marker missing from error: %v", err)
	}
}

func TestReviewDoesNotRetry(t *testing.T) {
	var attempts int32
	srv := newChatServer(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "unavailable")
	})
	client := newTestClient(t, Config{BaseURL: srv.URL, Model: testModel})
	if _, err := client.Review(context.Background(), Input{Answer: "answer"}); err == nil {
		t.Fatal("Review succeeded, want error")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("attempts = %d, want exactly 1 (no retries)", got)
	}
}

func TestReviewRequestTimeout(t *testing.T) {
	srv := newChatServer(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		writeCompletion(w, passJSON)
	})
	client := newTestClient(t, Config{BaseURL: srv.URL, Model: testModel, Timeout: 40 * time.Millisecond})
	started := time.Now()
	_, err := client.Review(context.Background(), Input{Answer: "answer"})
	if err == nil {
		t.Fatal("Review succeeded, want timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v does not wrap context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("timeout took %s, want a bounded wait", elapsed)
	}
}

func TestReviewHonorsCallerContext(t *testing.T) {
	srv := newChatServer(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		writeCompletion(w, passJSON)
	})
	client := newTestClient(t, Config{BaseURL: srv.URL, Model: testModel})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := client.Review(ctx, Input{Answer: "answer"}); err == nil {
		t.Fatal("Review succeeded, want caller deadline error")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v does not wrap context.DeadlineExceeded", err)
	}
}

func TestReviewRejectsNilContextAndEmptyAnswer(t *testing.T) {
	client := newTestClient(t, Config{BaseURL: "http://127.0.0.1:1/v1", Model: testModel})
	if _, err := client.Review(nil, Input{Answer: "answer"}); err == nil {
		t.Error("Review with nil context succeeded, want error")
	}
	if _, err := client.Review(context.Background(), Input{Answer: "  \n\t "}); err == nil {
		t.Error("Review with empty answer succeeded, want error")
	}
}

func TestReviewMissingUsageIsZero(t *testing.T) {
	srv := newChatServer(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": passJSON}}}})
	})
	client := newTestClient(t, Config{BaseURL: srv.URL, Model: testModel})
	verdict, err := client.Review(context.Background(), Input{Answer: "answer"})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if verdict.Usage != (Usage{}) {
		t.Errorf("usage = %+v, want zero", verdict.Usage)
	}
	if verdict.LatencyMS < 0 {
		t.Errorf("LatencyMS = %d, want non-negative", verdict.LatencyMS)
	}
}

func TestReviewRejectsOversizedResponse(t *testing.T) {
	srv := newChatServer(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, strings.Repeat("a", maxResponseBytes+10))
	})
	client := newTestClient(t, Config{BaseURL: srv.URL, Model: testModel})
	if _, err := client.Review(context.Background(), Input{Answer: "answer"}); err == nil {
		t.Fatal("Review succeeded, want size error")
	} else if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error = %v, want an oversized response error", err)
	}
}
