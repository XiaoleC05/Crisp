// Command mockcritic is a deterministic OpenAI-compatible chat completions
// server used only by the Crisp Codex E2E harness.
//
// Crisp's critic client POSTs /chat/completions and expects a strict JSON
// verdict. This mock returns pass, block, invalid JSON or HTTP errors on
// demand, and can decide from a marker in the request body so a rewritten
// answer is reviewed differently from the original one.
//
// Test infrastructure only; stdlib-only on purpose.
package main

import (
	"bytes"
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
	passVerdict  = `{"decision":"pass","severity":"none","issues":[]}`
	blockVerdict = `{"decision":"block","severity":"material","issues":[{"type":"semantic_duplicate","location":"paragraph_2","instruction":"Merge with paragraph 1."}]}`
	invalidBody  = `this is not a valid verdict`
)

type server struct {
	mu      sync.Mutex
	seq     int
	verdict string
	marker  string
	delay   time.Duration
	logFile *os.File
}

type requestLog struct {
	TS          string `json:"ts"`
	Seq         int    `json:"seq"`
	Path        string `json:"path"`
	BodyBytes   int    `json:"body_bytes"`
	MarkerFound bool   `json:"marker_found"`
	Verdict     string `json:"verdict"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	verdict := flag.String("verdict", "auto", "auto|pass|block|invalid|http500")
	marker := flag.String("marker", "", "with -verdict auto: block only when the request body contains this marker")
	delay := flag.Duration("delay", 0, "delay before responding (critic-timeout tests)")
	logPath := flag.String("log", "", "optional path to a JSONL request log")
	flag.Parse()

	switch *verdict {
	case "auto", "pass", "block", "invalid", "http500":
	default:
		log.Fatalf("mockcritic: unsupported -verdict %q", *verdict)
	}

	s := &server{verdict: *verdict, marker: *marker, delay: *delay}
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatalf("mockcritic: open log: %v", err)
		}
		defer f.Close()
		s.logFile = f
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("mockcritic: listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/v1/chat/completions", s.handleChat)

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("mockcritic: serve: %v", err)
		}
	}()
	fmt.Printf("mockcritic listening on http://%s\n", ln.Addr().String())

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	<-stop
	_ = srv.Close()
}

func (s *server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if s.delay > 0 {
		time.Sleep(s.delay)
	}

	s.mu.Lock()
	s.seq++
	seq := s.seq
	s.mu.Unlock()

	markerFound := s.marker != "" && bytes.Contains(body, []byte(s.marker))
	decision := s.verdict
	if decision == "auto" {
		if markerFound {
			decision = "block"
		} else {
			decision = "pass"
		}
	}
	s.record(requestLog{
		TS:          time.Now().UTC().Format(time.RFC3339Nano),
		Seq:         seq,
		Path:        r.URL.Path,
		BodyBytes:   len(body),
		MarkerFound: markerFound,
		Verdict:     decision,
	})

	switch decision {
	case "http500":
		http.Error(w, `{"error":{"message":"mock critic failure","type":"server_error"}}`, http.StatusInternalServerError)
		return
	case "invalid", "timeout":
		s.writeCompletion(w, invalidBody)
		return
	case "block":
		s.writeCompletion(w, blockVerdict)
	default:
		s.writeCompletion(w, passVerdict)
	}
}

func (s *server) writeCompletion(w http.ResponseWriter, content string) {
	resp := map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "mock-critic",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     100,
			"completion_tokens": 20,
			"total_tokens":      120,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
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

var _ = strings.TrimSpace
