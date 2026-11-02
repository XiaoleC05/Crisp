// Command mockprovider is a deterministic, OpenAI-compatible Responses API
// server used only by the Crisp Codex E2E harness.
//
// It exists so the real Codex CLI can complete a turn without any network
// access or API key: the harness configures Codex with
// wire_api = "responses" and base_url pointing at this process. The server
// serves a fixed sequence of canned assistant answers (one per model call,
// repeating the last answer forever) and logs one JSON line per request so the
// harness can count model turns.
//
// This is test infrastructure, not part of the Crisp binary. It is stdlib-only
// on purpose: test/e2e must not add module dependencies.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"
)

// answersFile is the JSON shape of the -answers file: a list of canned
// assistant answer texts, served in order.
type answersFile struct {
	Answers []string `json:"answers"`
}

// requestLog is one JSONL line written to -log. It deliberately records only
// request metadata, never the prompt or answer bodies.
type requestLog struct {
	TS        string `json:"ts"`
	Seq       int    `json:"seq"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Model     string `json:"model,omitempty"`
	Stream    bool   `json:"stream,omitempty"`
	InputLen  int    `json:"input_len,omitempty"`
	BodyBytes int    `json:"body_bytes,omitempty"`
}

type server struct {
	mu      sync.Mutex
	answers []string
	seq     int
	logFile *os.File
}

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	answersPath := flag.String("answers", "", "path to the canned answers JSON file")
	logPath := flag.String("log", "", "optional path to a JSONL request log")
	flag.Parse()

	if *answersPath == "" {
		log.Fatal("mockprovider: -answers is required")
	}
	raw, err := os.ReadFile(*answersPath)
	if err != nil {
		log.Fatalf("mockprovider: read answers: %v", err)
	}
	var answers answersFile
	if err := json.Unmarshal(raw, &answers); err != nil {
		log.Fatalf("mockprovider: decode answers: %v", err)
	}
	if len(answers.Answers) == 0 {
		log.Fatal("mockprovider: answers file has no answers")
	}

	s := &server{answers: answers.Answers}
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatalf("mockprovider: open log: %v", err)
		}
		defer f.Close()
		s.logFile = f
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("mockprovider: listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/responses", s.handleResponses)

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("mockprovider: serve: %v", err)
		}
	}()

	// The harness reads this exact line to learn which port was chosen.
	fmt.Printf("mockprovider listening on http://%s\n", ln.Addr().String())

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
	_ = srv.Close()
}

func (s *server) nextAnswer() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.seq
	if idx >= len(s.answers) {
		idx = len(s.answers) - 1
	}
	s.seq++
	return s.answers[idx]
}

func (s *server) record(entry requestLog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logFile == nil {
		return
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	_, _ = s.logFile.Write(append(b, '\n'))
}

func (s *server) handleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"mock-model","object":"model","created":0,"owned_by":"crisp-e2e"}]}`)
}

// responsesRequest is the subset of the Responses API request body the mock
// needs. Everything else Codex sends (tools, instructions, reasoning config,
// include lists) is accepted and ignored.
type responsesRequest struct {
	Model  string          `json:"model"`
	Stream bool            `json:"stream"`
	Input  json.RawMessage `json:"input"`
}

func (s *server) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	started := time.Now()
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var req responsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	answer := s.nextAnswer()
	s.record(requestLog{
		TS:        started.UTC().Format(time.RFC3339Nano),
		Method:    r.Method,
		Path:      r.URL.Path,
		Model:     req.Model,
		Stream:    req.Stream,
		InputLen:  len(req.Input),
		BodyBytes: len(body),
	})

	responseID := fmt.Sprintf("resp_mock_%d", time.Now().UnixNano())
	messageID := fmt.Sprintf("msg_mock_%d", time.Now().UnixNano())
	createdAt := time.Now().Unix()
	model := req.Model
	if model == "" {
		model = "mock-model"
	}
	item := outputMessage{
		ID:     messageID,
		Type:   "message",
		Status: "completed",
		Role:   "assistant",
		Content: []outputContent{{
			Type:        "output_text",
			Text:        answer,
			Annotations: []any{},
		}},
	}
	resp := responseObject{
		ID:                responseID,
		Object:            "response",
		CreatedAt:         createdAt,
		Status:            "completed",
		Model:             model,
		Output:            []outputMessage{item},
		ParallelToolCalls: false,
		ToolChoice:        "auto",
		Tools:             []any{},
		Usage: &usage{
			InputTokens:  128,
			OutputTokens: runeLen(answer)/2 + 1,
			TotalTokens:  128 + runeLen(answer)/2 + 1,
		},
	}

	if !req.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	inProgress := resp
	inProgress.Status = "in_progress"
	inProgress.Output = []outputMessage{}
	emit(w, flusher, "response.created", map[string]any{"type": "response.created", "response": inProgress})
	emit(w, flusher, "response.in_progress", map[string]any{"type": "response.in_progress", "response": inProgress})

	addedItem := item
	addedItem.Status = "in_progress"
	addedItem.Content = []outputContent{}
	emit(w, flusher, "response.output_item.added", map[string]any{
		"type": "response.output_item.added", "output_index": 0, "item": addedItem,
	})
	emit(w, flusher, "response.content_part.added", map[string]any{
		"type":          "response.content_part.added",
		"item_id":       messageID,
		"output_index":  0,
		"content_index": 0,
		"part":          outputContent{Type: "output_text", Text: "", Annotations: []any{}},
	})

	// Chunk the answer on rune boundaries so multi-byte CJK stays valid UTF-8.
	runes := []rune(answer)
	const chunk = 24
	for i := 0; i < len(runes); i += chunk {
		end := i + chunk
		if end > len(runes) {
			end = len(runes)
		}
		emit(w, flusher, "response.output_text.delta", map[string]any{
			"type":          "response.output_text.delta",
			"item_id":       messageID,
			"output_index":  0,
			"content_index": 0,
			"delta":         string(runes[i:end]),
			"logprobs":      []any{},
		})
	}
	emit(w, flusher, "response.output_text.done", map[string]any{
		"type":          "response.output_text.done",
		"item_id":       messageID,
		"output_index":  0,
		"content_index": 0,
		"text":          answer,
		"logprobs":      []any{},
	})
	emit(w, flusher, "response.content_part.done", map[string]any{
		"type":          "response.content_part.done",
		"item_id":       messageID,
		"output_index":  0,
		"content_index": 0,
		"part":          outputContent{Type: "output_text", Text: answer, Annotations: []any{}},
	})
	emit(w, flusher, "response.output_item.done", map[string]any{
		"type": "response.output_item.done", "output_index": 0, "item": item,
	})
	emit(w, flusher, "response.completed", map[string]any{"type": "response.completed", "response": resp})
}

type outputContent struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Annotations []any  `json:"annotations"`
}

type outputMessage struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Status  string          `json:"status"`
	Role    string          `json:"role"`
	Content []outputContent `json:"content"`
}

type usage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

type responseObject struct {
	ID                string          `json:"id"`
	Object            string          `json:"object"`
	CreatedAt         int64           `json:"created_at"`
	Status            string          `json:"status"`
	Model             string          `json:"model"`
	Output            []outputMessage `json:"output"`
	ParallelToolCalls bool            `json:"parallel_tool_calls"`
	ToolChoice        string          `json:"tool_choice"`
	Tools             []any           `json:"tools"`
	Usage             *usage          `json:"usage,omitempty"`
}

// emit writes one SSE event. Codex's Responses client consumes the "event:"
// name and the JSON data payload.
func emit(w io.Writer, flusher http.Flusher, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	flusher.Flush()
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
