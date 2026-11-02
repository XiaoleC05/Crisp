// Command mockanthropic is a deterministic, Anthropic Messages-API-compatible
// server used only by the Crisp Claude Code E2E harness.
//
// It exists so the real Claude Code CLI can complete a turn without any network
// access or real API key: the harness starts this process, points
// ANTHROPIC_BASE_URL at it, and runs the CLI non-interactively. The server
// streams a fixed sequence of canned assistant answers (one per main model
// call, repeating the last answer forever) and logs one JSON line per request
// so the harness can count model turns. Requests that carry a small max_tokens
// (title / prompt-suggestion / other utility calls) are answered neutrally and
// do not consume the canned sequence.
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
	"strings"
	"sync"
	"time"
)

const (
	// neutralAnswer is served to auxiliary (non-conversation) model calls so a
	// utility request can never shift the canned conversation sequence.
	neutralAnswer = "（E2E 辅助回答）OK。"
	// chunkRunes is how the canned answer is split into SSE text deltas.
	chunkRunes = 24
	// auxiliaryMaxTokens: a Messages request with max_tokens at or below this
	// is a side utility call (session title, prompt suggestion, ...), not a
	// conversation turn.
	auxiliaryMaxTokens = 1024
)

// answersFile is the JSON shape of the -answers file: a list of canned
// assistant answer texts, served in order for conversation model calls.
type answersFile struct {
	Answers []string `json:"answers"`
}

// requestLog is one JSONL line written to -log. It deliberately records only
// request metadata, never the prompt, system prompt, or answer bodies.
type requestLog struct {
	TS           string `json:"ts"`
	Seq          int    `json:"seq"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	Model        string `json:"model,omitempty"`
	Stream       bool   `json:"stream,omitempty"`
	MaxTokens    int    `json:"max_tokens,omitempty"`
	Messages     int    `json:"messages,omitempty"`
	SystemBytes  int    `json:"system_bytes,omitempty"`
	BodyBytes    int    `json:"body_bytes,omitempty"`
	Continuation bool   `json:"continuation,omitempty"`
	Auxiliary    bool   `json:"auxiliary,omitempty"`
	AnswerIndex  int    `json:"answer_index"`
	AnswerRunes  int    `json:"answer_runes"`
	Completion   string `json:"completion"`
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
		log.Fatal("mockanthropic: -answers is required")
	}
	raw, err := os.ReadFile(*answersPath)
	if err != nil {
		log.Fatalf("mockanthropic: read answers: %v", err)
	}
	var answers answersFile
	if err := json.Unmarshal(raw, &answers); err != nil {
		log.Fatalf("mockanthropic: decode answers: %v", err)
	}
	if len(answers.Answers) == 0 {
		log.Fatal("mockanthropic: answers file has no answers")
	}

	s := &server{answers: answers.Answers}
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatalf("mockanthropic: open log: %v", err)
		}
		defer f.Close()
		s.logFile = f
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("mockanthropic: listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/messages/count_tokens", s.handleCountTokens)
	mux.HandleFunc("/v1/messages", s.handleMessages)
	mux.HandleFunc("/", s.handleUnknown)

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("mockanthropic: serve: %v", err)
		}
	}()

	// The harness picks the port itself; this line is only for manual runs.
	fmt.Printf("mockanthropic listening on http://%s\n", ln.Addr().String())

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
	_ = srv.Close()
}

func (s *server) nextAnswer() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.seq
	if idx >= len(s.answers) {
		idx = len(s.answers) - 1
	}
	s.seq++
	return s.answers[idx], idx
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
	_, _ = io.WriteString(w, `{"data":[{"id":"claude-mock","type":"model","display_name":"Mock Claude","created_at":"2026-01-01T00:00:00Z"}],"has_more":false,"first_id":"claude-mock","last_id":"claude-mock"}`)
}

func (s *server) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"input_tokens":42}`)
}

func (s *server) handleUnknown(w http.ResponseWriter, r *http.Request) {
	s.record(requestLog{
		TS:         time.Now().UTC().Format(time.RFC3339Nano),
		Method:     r.Method,
		Path:       r.URL.Path,
		Completion: "not_found",
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, `{"type":"error","error":{"type":"not_found_error","message":"mockanthropic: not implemented"}}`)
}

// messagesRequest is the subset of the Messages API request body the mock
// needs. Everything else Claude Code sends (tools, betas, metadata, thinking
// config) is accepted and ignored.
type messagesRequest struct {
	Model     string            `json:"model"`
	Stream    bool              `json:"stream"`
	MaxTokens int               `json:"max_tokens"`
	System    json.RawMessage   `json:"system"`
	Messages  []json.RawMessage `json:"messages"`
}

func (s *server) handleMessages(w http.ResponseWriter, r *http.Request) {
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
	var req messagesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}

	auxiliary := req.MaxTokens > 0 && req.MaxTokens <= auxiliaryMaxTokens
	answer := neutralAnswer
	answerIndex := -1
	if !auxiliary {
		answer, answerIndex = s.nextAnswer()
	}
	s.record(requestLog{
		TS:           started.UTC().Format(time.RFC3339Nano),
		Method:       r.Method,
		Path:         r.URL.Path,
		Model:        req.Model,
		Stream:       req.Stream,
		MaxTokens:    req.MaxTokens,
		Messages:     len(req.Messages),
		SystemBytes:  len(req.System),
		BodyBytes:    len(body),
		Continuation: strings.Contains(string(body), `"role":"assistant"`),
		Auxiliary:    auxiliary,
		AnswerIndex:  answerIndex,
		AnswerRunes:  runeLen(answer),
		Completion:   completionKind(req.Stream),
	})

	model := req.Model
	if model == "" {
		model = "claude-mock"
	}
	if !req.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(messageObject(model, answer))
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
	writeSSE(w, flusher, model, answer)
}

func completionKind(stream bool) string {
	if stream {
		return "stream"
	}
	return "json"
}

// messageObject is the non-streaming Messages API response shape.
func messageObject(model, text string) map[string]any {
	return map[string]any{
		"id":            "msg_mock",
		"type":          "message",
		"role":          "assistant",
		"model":         model,
		"content":       []any{map[string]any{"type": "text", "text": text}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": 42, "output_tokens": runeLen(text)/2 + 1},
	}
}

// writeSSE streams one complete assistant message using the exact Messages API
// SSE event sequence Claude Code's client consumes.
func writeSSE(w io.Writer, flusher http.Flusher, model, text string) {
	emit(w, flusher, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_mock", "type": "message", "role": "assistant", "model": model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 42, "output_tokens": 1},
		},
	})
	emit(w, flusher, "content_block_start", map[string]any{
		"type": "content_block_start", "index": 0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
	runes := []rune(text)
	for i := 0; i < len(runes); i += chunkRunes {
		end := i + chunkRunes
		if end > len(runes) {
			end = len(runes)
		}
		emit(w, flusher, "content_block_delta", map[string]any{
			"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": string(runes[i:end])},
		})
	}
	emit(w, flusher, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	emit(w, flusher, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": runeLen(text)/2 + 1},
	})
	emit(w, flusher, "message_stop", map[string]any{"type": "message_stop"})
}

// emit writes one SSE event. The Messages client consumes the "event:" name and
// the JSON data payload.
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
