package tokenest

import (
	"unicode"

	"github.com/HBulgat/mini-agent/internal/llm"
)

// CharRatio approximates token counts via a per-language character
// weighting. The defaults (zh=0.6, en=0.25) come from §8.9.2 and are
// tuned for the mini-agent compaction trigger threshold (errors of
// ± 15 % don't matter when the trigger ratio is 0.8 of context window).
//
// "zh" (CJK) characters are counted at ZhRatio tokens/char; everything
// else (ASCII letters, digits, whitespace, punctuation) is counted at
// EnRatio tokens/char. We accept the slight unfairness for languages
// like Korean / Arabic — the numbers are close enough for the
// "do we compact?" decision they feed.
type CharRatio struct {
	ZhRatio float64
	EnRatio float64
}

// NewCharRatio constructs a CharRatio with explicit weights. Pass
// (0.6, 0.25) for the §8.9.2 baseline.
func NewCharRatio(zh, en float64) *CharRatio {
	return &CharRatio{ZhRatio: zh, EnRatio: en}
}

// EstimateText counts CJK and non-CJK runes separately and applies the
// configured weights. We round up so an empty estimate is never
// returned for non-empty input.
func (c *CharRatio) EstimateText(text string) int {
	if text == "" {
		return 0
	}
	zh, en := 0, 0
	for _, r := range text {
		if isCJK(r) {
			zh++
		} else {
			en++
		}
	}
	tokens := float64(zh)*c.ZhRatio + float64(en)*c.EnRatio
	if tokens > 0 && tokens < 1 {
		return 1
	}
	return int(tokens + 0.5)
}

// EstimateMessages walks every ContentBlock in every message. Per-role
// overhead (role tags, JSON envelope) is approximated as a flat 4
// tokens per message — close enough for the compaction trigger.
func (c *CharRatio) EstimateMessages(messages []*llm.Message) int {
	total := 0
	for _, m := range messages {
		if m == nil {
			continue
		}
		// Per-message overhead: role + framing.
		total += 4
		for _, b := range m.Content {
			switch b.Type {
			case llm.BlockText:
				total += c.EstimateText(b.Text)
			case llm.BlockThinking:
				// Thinking blocks count too — the next request feeds
				// them back to Anthropic verbatim with their signature.
				total += c.EstimateText(b.Thinking)
			case llm.BlockToolUse:
				total += c.EstimateText(b.ToolName)
				// JSON-encoding overhead is approximated by counting
				// each input value's stringified form. Keeping it
				// rough on purpose — exact would require a marshal.
				for _, v := range b.ToolInput {
					if s, ok := v.(string); ok {
						total += c.EstimateText(s)
					} else {
						total += 4
					}
				}
			case llm.BlockToolResult:
				total += c.EstimateText(b.Output)
			case llm.BlockRedactedThinking:
				// Encrypted opaque blob — we don't see the plaintext.
				// Use a generous fixed cost so compaction sees them.
				total += 64
			}
		}
	}
	return total
}

// isCJK is a thin wrapper around the standard unicode tables so the
// caller doesn't need to import unicode directly. We accept the full
// Han + Hiragana + Katakana + Hangul ranges (Asian scripts pack
// densely into BPE tokens, hence the higher ZhRatio).
func isCJK(r rune) bool {
	switch {
	case unicode.Is(unicode.Han, r):
		return true
	case unicode.Is(unicode.Hiragana, r):
		return true
	case unicode.Is(unicode.Katakana, r):
		return true
	case unicode.Is(unicode.Hangul, r):
		return true
	}
	return false
}
