package openai

// ThinkingEffort maps the canonical Request.ThinkingEffort string onto
// the OpenAI / DeepSeek wire shape. Returns:
//
//   - send      true if the caller should attach a `reasoning` field
//                to the request body
//   - effort    the value to put into reasoning.effort (low/medium/high)
//
// The §8.4.1 mapping table is the source of truth.
//
// We don't bake the "DeepSeek doesn't accept reasoning at all" rule
// into this helper — the Provider does that at the model-table level
// (the DeepSeek `deepseek-reasoner` entry has SupportsThinking=true
// but the codec ignores effort because the model picks its own).
func mapThinkingEffort(enabled bool, effort string) (send bool, normalized string) {
	if !enabled {
		return false, ""
	}
	switch effort {
	case "low":
		return true, "low"
	case "medium", "":
		return true, "medium"
	case "high":
		return true, "high"
	}
	// Unknown level — fall back to medium and let the Provider log it
	// as a "warn" rather than fail.
	return true, "medium"
}
