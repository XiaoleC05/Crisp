// Command mockmessages is a deterministic, Anthropic/DeepSeek Messages-API-
// compatible SSE server used only by the Crisp DeepSeek Harness E2E harness
// (scripts/e2e-dsh.ps1).
//
// The harness starts this process, points DEEPSEEK_BASE_URL at it, and runs the
// real dsh headless agent against it. It streams a canned answer sequence (one
// "main" answer per conversation model call) so the guard flow can be verified
// without network access or a real API key:
//
//	answers[0]  the flawed answer delivered before the gate runs
//	answers[1]  the improved answer delivered for the rewrite turn
//
// With -rewrite-marker, a request whose body contains the marker (the fixed
// rewrite instruction queued by the Crisp gate) is served answers[1] directly,
// so the improved answer is tied to the rewrite turn rather than to request
// order. Auxiliary model calls the Harness makes (session titles, compaction
// summaries: thinking disabled, small max_tokens, or the shipped prompt
// markers) are answered neutrally and never consume the answer sequence.
//
// test infrastructure only; stdlib-only on purpose so test/e2e adds no module
// dependencies. It logs request metadata (never prompt or answer bodies).
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
	// neutralAnswer is served to auxiliary model calls so a utility request can
	// never shift the canned conversation sequence.
	neutralAnswer = "Crisp E2E auxiliary answer: ok."
	// chunkRunes is how the canned answer is split into SSE text deltas.
	chunkRunes = 12
	// auxiliaryMaxTokens: a Messages request with a small max_tokens is a side
	// utility call (session title, prompt suggestion, ...), not a conversation.
	auxiliaryMaxTokens = 1024
)

// answersFile is the JSON shape of the -answers file.
type answersFile struct {
	Answers []string `json:"answers"`
}

// requestLog is one JSONL line written to -log. Only request metadata is
// recorded, never prompt/system/answer bodies.
type requestLog struct {
	TS             string `json:"ts"`
	Seq            int    `json:"seq"`
	Method         string `json:"method"`
	Path           string `json:"path"`
	Model          string `json:"model,omitempty"`
	Stream         bool   `json:"stream,omitempty"`
	MaxTokens      int    `json:"max_tokens,omitempty"`
	Messages       int    `json:"messages,omitempty"`
	SystemBytes    int    `json:"system_bytes,omitempty"`
	BodyBytes      int    `json:"body_bytes,omitempty"`
	Continuation   bool   `json:"continuation,omitempty"`
	Auxiliary      bool   `json:"auxiliary,omitempty"`
	AuxReason      string `json:"aux_reason,omitempty"`
	RewriteMarker  bool   `json:"rewrite_marker,omitempty"`
	AnswerIndex    int    `json:"answer_index"`
	AnswerRunes    int    `json:"answer_runes"`
	Completion     string `json:"completion"`
	AnswerSelected string `json:"answer_selected,omitempty"`
}

type server struct {
	mu            sync.Mutex
	answers       []string
	seq           int
	rewriteMarker string
	logFile       *os.File
}

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	answersPath := flag.String("answers", "", "path to the canned answers JSON file (required)")
	logPath := flag.String("log", "", "optional path to a JSONL request log")
	rewriteMarker := flag.String("rewrite-marker", "", "optional body marker that selects the second canned answer (the rewrite turn)")
	flag.Parse()

	if *answersPath == "" {
		log.Fatal("mockmessages: -answers is required")
	}
	raw, err := os.ReadFile(*answersPath)
	if err != nil {
		log.Fatalf("mockmessages: read answers: %v", err)
	}
	var answers answersFile
	if err := json.Unmarshal(raw, &answers); err != nil {
		log.Fatalf("mockmessages: decode answers: %v", err)
	}
	if len(answers.Answers) == 0 {
		log.Fatal("mockmessages: answers file has no answers")
	}

	s := &server{answers: answers.Answers, rewriteMarker: *rewriteMarker}
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatalf("mockmessages: open log: %v", err)
		}
		defer f.Close()
		s.logFile = f
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("mockmessages: listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/v1/messages", s.handleMessages)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/", s.handleUnknown)

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("mockmessages: serve: %v", err)
		}
	}()

	// The harness picks the port itself; this line is only for manual runs.
	fmt.Printf("mockmessages listening on http://%s\n", ln.Addr().String())

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
	_ = srv.Close()
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

// nextSequentialAnswer returns the next canned main answer.
func (s *server) nextSequentialAnswer() (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := s.seq
	if idx >= len(s.answers) {
		idx = len(s.answers) - 1
	}
	s.seq++
	return s.answers[idx], idx
}

// messagesRequest is the subset of the Messages API request body this mock
// needs; unknown fields (tools, output_config, system snapshots, ...) are
// accepted and ignored.
type messagesRequest struct {
	Model     string            `json:"model"`
	Stream    bool              `json:"stream"`
	MaxTokens int               `json:"max_tokens"`
	System    json.RawMessage   `json:"system"`
	Messages  []json.RawMessage `json:"messages"`
}

// message is the subset of one Messages API message this mock needs.
type message struct {
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func decodeMessage(raw json.RawMessage) (message, bool) {
	var msg message
	if err := json.Unmarshal(raw, &msg); err != nil {
		return message{}, false
	}
	return msg, true
}

// lastUserText returns the text blocks of the request's last user message. It
// looks only at the request's own messages: the request body also carries
// Harness extension fields (session-log snapshots, plugin inventory) that may
// embed earlier prompts, so scanning the whole body would misclassify a real
// conversation turn.
func lastUserText(req messagesRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg, ok := decodeMessage(req.Messages[i])
		if !ok || msg.Role != "user" {
			continue
		}
		var b strings.Builder
		for _, block := range msg.Content {
			if block.Type == "text" {
				b.WriteString(block.Text)
			}
		}
		return b.String()
	}
	return ""
}

// requestHasRole reports whether the request's own messages contain a role.
func requestHasRole(req messagesRequest, role string) bool {
	for _, raw := range req.Messages {
		if msg, ok := decodeMessage(raw); ok && msg.Role == role {
			return true
		}
	}
	return false
}

// auxiliary reports whether a request is a side utility call rather than a
// conversation turn. The shipped headless profile issues a session-title model
// call whose last user message is the title instruction, compaction summaries
// carry their own preamble, and small-max_tokens calls are utility requests.
// Those are answered neutrally so they never consume the canned answer
// sequence.
func auxiliary(req messagesRequest, userText string) (bool, string) {
	if req.MaxTokens > 0 && req.MaxTokens <= auxiliaryMaxTokens {
		return true, "max_tokens"
	}
	for _, marker := range []string{
		"Create a concise title",
		"Generate the session title",
		"automatically generated checkpoint",
		"condensing an earlier span of the conversation",
	} {
		if strings.Contains(userText, marker) {
			return true, marker
		}
	}
	return false, ""
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
	userText := lastUserText(req)
	aux, auxReason := auxiliary(req, userText)
	rewriteMarker := s.rewriteMarker != "" && strings.Contains(userText, s.rewriteMarker)
	continuation := requestHasRole(req, "assistant")

	var answer, selected string
	answerIndex := -1
	switch {
	case aux:
		answer = neutralAnswer
		selected = "auxiliary"
	case rewriteMarker && len(s.answers) > 1:
		answer = s.answers[1]
		answerIndex = 1
		selected = "rewrite"
	default:
		var idx int
		answer, idx = s.nextSequentialAnswer()
		answerIndex = idx
		if idx == 0 {
			selected = "initial"
		} else {
			selected = "sequence"
		}
	}

	s.record(requestLog{
		TS:             started.UTC().Format(time.RFC3339Nano),
		Method:         r.Method,
		Path:           r.URL.Path,
		Model:          req.Model,
		Stream:         req.Stream,
		MaxTokens:      req.MaxTokens,
		Messages:       len(req.Messages),
		SystemBytes:    len(req.System),
		BodyBytes:      len(body),
		Continuation:   continuation,
		Auxiliary:      aux,
		AuxReason:      auxReason,
		RewriteMarker:  rewriteMarker,
		AnswerIndex:    answerIndex,
		AnswerRunes:    runeLen(answer),
		Completion:     completionKind(req.Stream),
		AnswerSelected: selected,
	})

	model := req.Model
	if model == "" {
		model = "deepseek-mock"
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

// writeSSE streams one complete assistant message using the Messages SSE event
// sequence the DSH DeepSeek adapter consumes.
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

// emit writes one SSE event and flushes it so the client sees streaming chunks.
func emit(w io.Writer, flusher http.Flusher, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	flusher.Flush()
}

func (s *server) handleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"data":[{"id":"deepseek-mock","type":"model","display_name":"Mock DeepSeek"}],"has_more":false,"first_id":"deepseek-mock","last_id":"deepseek-mock"}`)
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
	_, _ = io.WriteString(w, `{"type":"error","error":{"type":"not_found_error","message":"mockmessages: not implemented"}}`)
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
