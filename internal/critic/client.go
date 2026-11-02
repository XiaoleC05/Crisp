package critic

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/XiaoleC05/Crisp/internal/checker"
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
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("QUALITY_GATE_BASE_URL is not set")
	}
	if strings.TrimSpace(c.Model) == "" {
		return errors.New("QUALITY_GATE_MODEL is not set")
	}
	return nil
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
	// TODO(critic): implement the OpenAI-compatible HTTP client.
	return nil, ErrNotConfigured
}
