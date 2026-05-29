package openai

import (
	"errors"

	"github.com/HBulgat/mini-agent/internal/llm/network"
)

// Config is the per-instance configuration block. R4's `LLMCfg`
// translates each `openai_compat[]` entry into one Config + one
// Provider — a single mini-agent process can run several Providers in
// parallel (e.g. one for DeepSeek, one for openai.com).
type Config struct {
	// Name is the unique instance label visible to the agent and to
	// `/model deepseek:...` references. R4 §7.1.2 makes this user-set;
	// duplicates are rejected by Registry.Register.
	Name string

	// BaseURL points at any OpenAI-compatible HTTP endpoint. Empty =
	// "use the SDK's built-in default" (api.openai.com), which is
	// rarely what we want for self-hosted setups.
	BaseURL string

	// APIKey is opaque to us — passed straight to the SDK.
	APIKey string

	// DefaultModel is the model to call when Request.Model isn't set.
	// Also seeded into Capabilities().Model and used as the initial
	// "active" model for the EstimateTokens estimator.
	DefaultModel string

	// Model is the currently selected model. Mutated by SetActive
	// when the user issues `/model deepseek:other-model`. We seed it
	// from DefaultModel during New().
	Model string

	// ForceThinking, when true, makes Capabilities() report
	// SupportsThinking even when the model is missing from the built-in
	// pricing table. Reserved for self-hosted reasoning models that
	// don't appear publicly. Per D48.
	ForceThinking bool

	// Timeout / Retry can be nil — Provider falls back to the network
	// package defaults so tests don't have to wire them.
	Timeout *network.TimeoutConfig
	Retry   *network.RetryConfig
}

// Validate sanity-checks a Config before construction. We avoid
// touching the network here — that's New()'s job. The errors are
// translated into user-facing config errors by bootstrap.
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("openai: nil Config")
	}
	if c.Name == "" {
		return errors.New("openai: Config.Name is required")
	}
	if c.APIKey == "" {
		return errors.New("openai: Config.APIKey is required")
	}
	if c.DefaultModel == "" {
		return errors.New("openai: Config.DefaultModel is required")
	}
	return nil
}

// withDefaults returns a copy of the Config with empty fields filled
// in from defaults. Call sites work on the returned value, leaving the
// caller's Config untouched.
func (c *Config) withDefaults() *Config {
	out := *c
	if out.Model == "" {
		out.Model = out.DefaultModel
	}
	if out.Timeout == nil {
		out.Timeout = network.DefaultTimeoutConfig()
	}
	if out.Retry == nil {
		out.Retry = network.DefaultRetryConfig()
	}
	return &out
}
