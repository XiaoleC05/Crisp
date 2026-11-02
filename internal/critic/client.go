package critic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/XiaoleC05/Crisp/internal/checker"
)

const (
	// defaultTimeout is the critic HTTP timeout used when Config.Timeout is
	// unset. The outer hook budget (25s) is larger, so the critic itself is
	// bounded here.
	defaultTimeout = 8 * time.Second
	// chatCompletionsPath is appended to the configured base URL.
	chatCompletionsPath = "chat/completions"
	// maxResponseBytes bounds how much of a provider response is read.
	maxResponseBytes = 1 << 20
	// maxErrorBodyBytes bounds the provider response body echoed in errors.
	maxErrorBodyBytes = 200
	// maxProviderErrorRunes bounds a provider error message echoed in errors.
	maxProviderErrorRunes = 200
)

// Config is the critic transport and model configuration. It comes from
// environment variables and never contains anything that may be logged.
type Config struct {
	BaseURL     string
	APIKey      string
	Model       string
	Timeout     time.Duration
	Temperature float64
}

// Validate fails when required settings are missing. APIKey is optional for
// locally hosted OpenAI-compatible servers.
func (c Config) Validate() error {
	base := strings.TrimSpace(c.BaseURL)
	if base == "" {
		return errors.New("QUALITY_GATE_BASE_URL is not set")
	}
	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("invalid QUALITY_GATE_BASE_URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("QUALITY_GATE_BASE_URL must use http or https")
	}
	if u.Host == "" {
		return errors.New("QUALITY_GATE_BASE_URL must include a host")
	}
	if strings.TrimSpace(c.Model) == "" {
		return errors.New("QUALITY_GATE_MODEL is not set")
	}
	return nil
}

// endpoint joins the configured base URL with the OpenAI-compatible chat
// completions path. Base URLs are accepted with or without a trailing slash
// and with or without a version prefix such as /v1: the suffix is always
// "chat/completions", never a hard-coded "/v1/chat/completions".
func (c Config) endpoint() string {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if strings.HasSuffix(base, "/"+chatCompletionsPath) {
		return base
	}
	return base + "/" + chatCompletionsPath
}

// Input is everything the critic is allowed to see.
type Input struct {
	Answer       string
	UserMessage  string
	RuleFindings []checker.Finding
}

// Client reviews a finished answer and returns a parsed verdict.
type Client interface {
	Review(ctx context.Context, in Input) (Verdict, error)
}

// ErrNotConfigured is returned when required critic settings are missing.
var ErrNotConfigured = errors.New("critic is not configured")

// New builds a Client. Callers must fail open when this returns an error.
func New(cfg Config) (Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	client := &httpClient{
		endpoint:    cfg.endpoint(),
		model:       strings.TrimSpace(cfg.Model),
		apiKey:      strings.TrimSpace(cfg.APIKey),
		temperature: cfg.Temperature,
		timeout:     timeout,
		http: &http.Client{
			Timeout: timeout,
			// A quality gate never retries and does not follow redirects: a
			// redirect could send the bearer token to another host.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	return client, nil
}

// chatMessage is one OpenAI-compatible chat message.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest is the OpenAI-compatible chat completion payload. Streaming is
// explicitly disabled and no retries are performed.
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	Stream      bool          `json:"stream"`
}

// chatResponse is the subset of the OpenAI-compatible completion response the
// critic relies on. Unknown fields are ignored on purpose: providers add them.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
	Error *chatError `json:"error"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// httpClient is the default Client, speaking the OpenAI chat completions
// protocol over net/http with no retries and no streaming.
type httpClient struct {
	endpoint    string
	model       string
	apiKey      string
	temperature float64
	timeout     time.Duration
	http        *http.Client
}

// Review sends the answer to the configured critic service and parses a strict
// verdict. Any transport, HTTP, JSON or schema failure returns an error; the
// caller must fail open. Errors never contain the answer text or the API key.
func (c *httpClient) Review(ctx context.Context, in Input) (Verdict, error) {
	if ctx == nil {
		return Verdict{}, errors.New("critic: nil context")
	}
	if strings.TrimSpace(in.Answer) == "" {
		return Verdict{}, errors.New("critic: empty answer")
	}
	system, user := BuildPrompt(in)
	payload := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: c.temperature,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		// Marshalling plain strings cannot fail in practice, but the critic
		// must stay panic-free on every path.
		return Verdict{}, fmt.Errorf("critic: encode request: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Verdict{}, fmt.Errorf("critic: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	started := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded), isTimeoutError(err):
			return Verdict{}, fmt.Errorf("critic: request timed out after %s: %w", c.timeout, context.DeadlineExceeded)
		case errors.Is(err, context.Canceled):
			return Verdict{}, fmt.Errorf("critic: request canceled: %w", err)
		default:
			return Verdict{}, fmt.Errorf("critic: request failed: %w", err)
		}
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Verdict{}, fmt.Errorf("critic: read response: %w", err)
	}
	latencyMS := time.Since(started).Milliseconds()
	if len(data) > maxResponseBytes {
		return Verdict{}, fmt.Errorf("critic: response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Verdict{}, fmt.Errorf("critic: provider returned HTTP %d: %s", resp.StatusCode, errorSnippet(data))
	}
	content, usage, err := parseCompletion(data)
	if err != nil {
		return Verdict{}, fmt.Errorf("critic: %w", err)
	}
	verdict, err := parseVerdict(content)
	if err != nil {
		return Verdict{}, fmt.Errorf("critic: invalid verdict: %w", err)
	}
	verdict.Usage = usage
	verdict.LatencyMS = latencyMS
	return verdict, nil
}

// parseCompletion extracts the first choice's content and the provider token
// usage from a chat completion response body.
func parseCompletion(data []byte) (string, Usage, error) {
	var resp chatResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return "", Usage{}, fmt.Errorf("completion response is not valid JSON: %w", err)
	}
	if len(resp.Choices) == 0 {
		if resp.Error != nil && strings.TrimSpace(resp.Error.Message) != "" {
			return "", Usage{}, fmt.Errorf("provider error: %s",
				truncateRunes(strings.TrimSpace(resp.Error.Message), maxProviderErrorRunes))
		}
		return "", Usage{}, errors.New("completion response has no choices")
	}
	content := resp.Choices[0].Message.Content
	if strings.TrimSpace(content) == "" {
		return "", Usage{}, errors.New("completion response has empty content")
	}
	var usage Usage
	if resp.Usage != nil {
		usage = Usage{
			PromptTokens:     max(resp.Usage.PromptTokens, 0),
			CompletionTokens: max(resp.Usage.CompletionTokens, 0),
			TotalTokens:      max(resp.Usage.TotalTokens, 0),
		}
	}
	return content, usage, nil
}

// errorSnippet renders at most maxErrorBodyBytes bytes of a provider response
// body for an error message. Whitespace is flattened and multi-byte runes are
// never split.
func errorSnippet(data []byte) string {
	flat := strings.Join(strings.Fields(string(data)), " ")
	if flat == "" {
		return "(empty body)"
	}
	var b strings.Builder
	b.Grow(min(len(flat), maxErrorBodyBytes))
	written := 0
	for _, r := range flat {
		size := utf8.RuneLen(r)
		if size < 0 {
			size = 1
		}
		if written+size > maxErrorBodyBytes {
			b.WriteString("...")
			break
		}
		b.WriteRune(r)
		written += size
	}
	return b.String()
}

// isTimeoutError reports whether err is a network timeout that is not already
// classified as context.DeadlineExceeded.
func isTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
