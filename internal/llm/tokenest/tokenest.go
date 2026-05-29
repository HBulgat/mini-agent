// Package tokenest provides Provider-agnostic token-count estimators.
//
// IMPORTANT: estimators are ONLY for "should we trigger compaction?"
// decisions. Real Usage numbers (cost, /cost output, usage_log rows)
// MUST come from the Provider's own usage response. Mixing estimates
// into Usage would corrupt billing.
//
// Available implementations:
//
//	NewCharRatio(zhRatio, enRatio float64)  language-weighted character
//	                                        approximation (used by
//	                                        Anthropic/Gemini and as a
//	                                        zero-dependency fallback for
//	                                        OpenAI when tiktoken-go isn't
//	                                        wired yet).
//
// Reference: docs/system-design/08-llm-providers.md §8.9 (R5, D43)
package tokenest

import "github.com/HBulgat/mini-agent/internal/llm"

// Estimator is the contract every concrete estimator implements.
// Methods MUST be safe for concurrent calls — multiple agent
// goroutines may call EstimateMessages while a stream is in flight.
type Estimator interface {
	// EstimateMessages returns a token-count estimate for the entire
	// conversation prefix that would be sent on the next request.
	// Implementations are free to ignore non-text blocks (image, tool
	// schemas) since the exact accounting depends on the Provider.
	EstimateMessages(messages []*llm.Message) int

	// EstimateText estimates a single string. The compaction layer uses
	// this for individual message bodies before they're embedded in a
	// larger prompt.
	EstimateText(text string) int
}

// Compile-time assertion that CharRatio satisfies the contract; we
// declare it here instead of in charratio.go so the assertion is
// visible next to the interface for readers.
var _ Estimator = (*CharRatio)(nil)
