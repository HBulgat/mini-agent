package openai

import (
	"github.com/HBulgat/mini-agent/internal/llm"
)

// ModelInfo bundles a model's static capabilities with its public per-
// MTok pricing. The pricing fields are USD per 1,000,000 tokens — see
// §8.8.2 for the cost formula.
type ModelInfo struct {
	Capabilities llm.Capabilities

	InputPerMTok       float64
	OutputPerMTok      float64
	ReasoningPerMTok   float64
	CachedInputPerMTok float64
}

// modelTable is the §8.8.3 P0 minimum set. The ContextWindow /
// MaxOutputTokens numbers come from each provider's docs as of
// 2026-Q1; anything not listed falls through to defaultCapabilities()
// in Capabilities().
//
// Naming convention: lowercased exactly as the API expects in the
// `model` field. We don't list aliases here — bootstrap installs them
// via separate map entries pointing at the same *ModelInfo.
var modelTable = map[string]*ModelInfo{
	"deepseek-chat": {
		Capabilities: llm.Capabilities{
			Model:             "deepseek-chat",
			ContextWindow:     128_000,
			MaxOutputTokens:   8_192,
			SupportsTools:     true,
			SupportsStreaming: true,
			SupportsThinking:  false,
		},
		InputPerMTok:       0.27,
		OutputPerMTok:      1.10,
		ReasoningPerMTok:   1.10,
		CachedInputPerMTok: 0.07,
	},
	"deepseek-reasoner": {
		Capabilities: llm.Capabilities{
			Model:             "deepseek-reasoner",
			ContextWindow:     128_000,
			MaxOutputTokens:   8_192,
			SupportsTools:     true,
			SupportsStreaming: true,
			SupportsThinking:  true,
		},
		InputPerMTok:       0.55,
		OutputPerMTok:      2.19,
		ReasoningPerMTok:   2.19,
		CachedInputPerMTok: 0.14,
	},
	"gpt-4o": {
		Capabilities: llm.Capabilities{
			Model:             "gpt-4o",
			ContextWindow:     128_000,
			MaxOutputTokens:   16_384,
			SupportsTools:     true,
			SupportsStreaming: true,
			SupportsThinking:  false,
		},
		InputPerMTok:       2.50,
		OutputPerMTok:      10.00,
		ReasoningPerMTok:   10.00,
		CachedInputPerMTok: 1.25,
	},
	"gpt-4.1": {
		Capabilities: llm.Capabilities{
			Model:             "gpt-4.1",
			ContextWindow:     1_000_000,
			MaxOutputTokens:   32_768,
			SupportsTools:     true,
			SupportsStreaming: true,
			SupportsThinking:  false,
		},
		InputPerMTok:       2.00,
		OutputPerMTok:      8.00,
		ReasoningPerMTok:   8.00,
		CachedInputPerMTok: 0.50,
	},
	"o3": {
		Capabilities: llm.Capabilities{
			Model:             "o3",
			ContextWindow:     200_000,
			MaxOutputTokens:   100_000,
			SupportsTools:     true,
			SupportsStreaming: true,
			SupportsThinking:  true,
		},
		InputPerMTok:       2.00,
		OutputPerMTok:      8.00,
		ReasoningPerMTok:   8.00,
		CachedInputPerMTok: 0.50,
	},
	"o3-mini": {
		Capabilities: llm.Capabilities{
			Model:             "o3-mini",
			ContextWindow:     200_000,
			MaxOutputTokens:   100_000,
			SupportsTools:     true,
			SupportsStreaming: true,
			SupportsThinking:  true,
		},
		InputPerMTok:       1.10,
		OutputPerMTok:      4.40,
		ReasoningPerMTok:   4.40,
		CachedInputPerMTok: 0.55,
	},
}

// LookupModel returns the ModelInfo for `model` (or nil + false if
// unknown). Exported so tests + bootstrap diagnostics can probe the
// table without going through a Provider instance.
func LookupModel(model string) (*ModelInfo, bool) {
	mi, ok := modelTable[model]
	return mi, ok
}

// defaultCapabilities is the fallback returned by Provider.Capabilities
// when the active model isn't in modelTable. Per D41 we err on the
// side of "tools + streaming yes, thinking no" — a model the system
// has never heard of is unlikely to support thinking, and the user can
// opt-in via Config.ForceThinking.
func defaultCapabilities(model string, forceThinking bool) llm.Capabilities {
	return llm.Capabilities{
		Model:             model,
		ContextWindow:     8_192,
		MaxOutputTokens:   4_096,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsThinking:  forceThinking,
	}
}

// ComputeCost implements the §8.8.2 formula. We export it so trace
// emitters and the `/cost` UI can replay the same math against an
// existing usage_log row without round-tripping through Provider.
func ComputeCost(u *llm.Usage, mi *ModelInfo) float64 {
	if u == nil || mi == nil {
		return 0
	}
	const perMillion = 1_000_000.0

	// Non-cached prompt tokens = total prompt - cached - cache_read.
	// (cache_creation tokens go on top of the prompt as a separate
	// chargeable bucket; OpenAI doesn't currently surface them.)
	nonCachedInput := u.PromptTokens - u.CachedPromptTokens - u.CacheReadTokens
	if nonCachedInput < 0 {
		nonCachedInput = 0
	}

	cost := 0.0
	cost += float64(nonCachedInput) * mi.InputPerMTok / perMillion
	cost += float64(u.CachedPromptTokens) * mi.CachedInputPerMTok / perMillion
	// CacheCreation/CacheRead don't apply to OpenAI; if they're set the
	// caller likely fed an Anthropic Usage by mistake — count as
	// non-cached input to avoid silently zeroing them.
	cost += float64(u.CacheCreationTokens) * mi.InputPerMTok / perMillion
	cost += float64(u.CacheReadTokens) * mi.CachedInputPerMTok / perMillion

	// CompletionTokens already includes ReasoningTokens; pull them out
	// and price separately.
	plainOutput := u.CompletionTokens - u.ReasoningTokens
	if plainOutput < 0 {
		plainOutput = 0
	}
	cost += float64(plainOutput) * mi.OutputPerMTok / perMillion
	cost += float64(u.ReasoningTokens) * mi.ReasoningPerMTok / perMillion
	return cost
}
