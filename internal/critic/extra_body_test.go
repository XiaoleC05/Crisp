package critic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newCapturingCriticServer returns a server that records the request body and
// replies with a valid pass verdict.
func newCapturingCriticServer(t *testing.T, captured *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if captured != nil {
			*captured = string(body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"decision\":\"pass\",\"severity\":\"none\",\"issues\":[]}"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReviewMergesProviderExtraBody(t *testing.T) {
	var captured string
	srv := newCapturingCriticServer(t, &captured)
	client, err := New(Config{
		BaseURL:       srv.URL,
		Model:         "deepseek-flash",
		Timeout:       5_000_000_000,
		ExtraBodyJSON: `{"thinking":{"type":"disabled"},"top_p":0.9}`,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	verdict, err := client.Review(context.Background(), Input{Answer: strings.Repeat("正常技术回答内容。", 30)})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if verdict.Decision != DecisionPass {
		t.Fatalf("unexpected verdict: %+v", verdict)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(captured), &body); err != nil {
		t.Fatalf("captured body is not JSON: %v", err)
	}
	if body["model"] != "deepseek-flash" {
		t.Fatalf("model missing or wrong: %v", body["model"])
	}
	thinking, ok := body["thinking"].(map[string]any)
	if !ok || thinking["type"] != "disabled" {
		t.Fatalf("thinking extra body not merged: %v", body["thinking"])
	}
	if body["top_p"] != 0.9 {
		t.Fatalf("top_p extra body not merged: %v", body["top_p"])
	}
	if body["stream"] != false {
		t.Fatalf("stream must stay false: %v", body["stream"])
	}
	if _, ok := body["messages"].([]any); !ok {
		t.Fatalf("messages missing: %v", body["messages"])
	}
}

func TestNewRejectsInvalidExtraBody(t *testing.T) {
	cases := map[string]string{
		"invalid json": `not-json`,
		"array":        `[1,2,3]`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := New(Config{BaseURL: "http://127.0.0.1:1", Model: "m", ExtraBodyJSON: raw})
			if err == nil {
				t.Fatal("expected error for invalid extra body")
			}
			if !errors.Is(err, ErrNotConfigured) {
				t.Fatalf("error should wrap ErrNotConfigured: %v", err)
			}
		})
	}
}
