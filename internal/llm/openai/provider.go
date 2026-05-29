package openai

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/ssestream"

	"github.com/HBulgat/mini-agent/internal/llm"
	"github.com/HBulgat/mini-agent/internal/llm/network"
	"github.com/HBulgat/mini-agent/internal/llm/tokenest"
)

// Provider implements llm.Provider against any OpenAI-compatible
// chat-completions endpoint (real OpenAI, DeepSeek, Kimi, Qwen,
// self-hosted servers — anything that speaks the same wire protocol).
//
// One Config = one Provider instance = one connection pool. The agent
// can hold several Providers at once (registered under unique names)
// and switch between them at runtime via the Registry.
//
// Concurrency: methods are safe for concurrent calls. The internal
// `model` field is guarded by a mutex because /model can rewrite it
// from the REPL while a turn is in flight.
type Provider struct {
	cfg       *Config
	sdk       openaisdk.Client
	estimator tokenest.Estimator
	logger    *slog.Logger

	mu    sync.RWMutex
	model string
}

// New constructs a Provider from a Config. It validates the input,
// applies defaults, and prepares the SDK client. We do NOT contact
// the network here — Provider.Stream is the first place that does.
//
// `logger` may be nil — we substitute slog.Default(). A nil estimator
// is replaced with the (zh=0.6, en=0.25) charratio default so callers
// can omit it in tests.
func New(cfg *Config, est tokenest.Estimator, logger *slog.Logger) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()

	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	sdk := openaisdk.NewClient(opts...)

	if est == nil {
		est = tokenest.NewCharRatio(0.6, 0.25)
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &Provider{
		cfg:       cfg,
		sdk:       sdk,
		estimator: est,
		logger:    logger,
		model:     cfg.Model,
	}, nil
}

// Name satisfies llm.Provider.Name. The instance label comes from
// Config.Name — every Provider in the Registry must be unique.
func (p *Provider) Name() string {
	return p.cfg.Name
}

// Capabilities reports the static traits of the *currently active*
// model (set at construction time and updated by SetModel).
//
// Lookup order:
//  1. modelTable — fast path for the well-known P0 models.
//  2. defaultCapabilities + Config.ForceThinking — fallback for unknown
//     models. We log a warning so misconfigurations surface instead of
//     hiding behind silent defaults.
func (p *Provider) Capabilities() llm.Capabilities {
	model := p.activeModel()
	if mi, ok := modelTable[model]; ok {
		return mi.Capabilities
	}
	p.logger.Warn("openai: model not in built-in table; using fallback capabilities",
		slog.String("provider", p.Name()),
		slog.String("model", model))
	return defaultCapabilities(model, p.cfg.ForceThinking)
}

// Stream starts a streaming chat completion. The returned channel:
//
//   - Receives StreamEvent values until the stream ends naturally
//     (StreamFinal) or fails (StreamError).
//   - Is always closed by the time the consumer goroutine returns —
//     callers can `for ev := range ch` without leaking.
//   - Is buffered (16 events) so a slow Sink doesn't block the SDK
//     reader on the hot path.
//
// Cancellation: ctx is wrapped in network.WithTimeout (Total budget)
// before being passed to the SDK; an early Stop()/cancel races the
// goroutine to clean up.
func (p *Provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	model := p.activeModel()
	caps := p.Capabilities()

	// Honor R5 §8.7.5: thinking requested but model doesn't support it.
	sendThinking, effort := mapThinkingEffort(req.EnableThinking, req.ThinkingEffort)
	if sendThinking && !caps.SupportsThinking {
		p.logger.Warn("openai: thinking requested but model does not support thinking; ignored",
			slog.String("provider", p.Name()),
			slog.String("model", model))
		sendThinking = false
	}

	params, err := buildAPIRequest(&req, model, sendThinking, effort)
	if err != nil {
		return nil, err
	}

	totalCtx, totalCancel := network.WithTimeout(ctx, p.cfg.Timeout)

	// Open the SSE stream with retry. The retry layer handles 5xx /
	// 429 / network errors during connection setup; once the stream
	// is open, mid-stream errors do NOT retry (D44 / §8.5.5).
	stream, err := network.WithRetry(totalCtx, p.cfg.Retry,
		func(c context.Context) (*ssestream.Stream[openaisdk.ChatCompletionChunk], error) {
			s := p.sdk.Chat.Completions.NewStreaming(c, params)
			// NewStreaming doesn't fail synchronously — errors surface
			// on the first Next(). We hand it back; the consumer's
			// run() will report the SDK error via StreamError.
			return s, nil
		})
	if err != nil {
		totalCancel()
		return nil, fmt.Errorf("openai: open stream: %w", err)
	}

	out := make(chan llm.StreamEvent, 16)
	go func() {
		defer totalCancel()
		defer close(out)
		consumer := newStreamConsumer(stream, out, model)
		consumer.run(totalCtx)
	}()
	return out, nil
}

// EstimateTokens is exposed for the agent.maybeCompact path. It just
// delegates to the estimator we were given at construction.
func (p *Provider) EstimateTokens(messages []*llm.Message) int {
	return p.estimator.EstimateMessages(messages)
}

// SetModel updates the active model at runtime. Used by the `/model`
// slash command after the registry has confirmed the move is legal.
func (p *Provider) SetModel(model string) error {
	if model == "" {
		return errors.New("openai: SetModel: empty model")
	}
	p.mu.Lock()
	p.model = model
	p.mu.Unlock()
	return nil
}

func (p *Provider) activeModel() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.model
}

// Compile-time assertion that *Provider satisfies llm.Provider so
// drift between the interface and the concrete impl shows up at build
// time.
var _ llm.Provider = (*Provider)(nil)
