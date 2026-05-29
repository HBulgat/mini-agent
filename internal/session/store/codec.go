package store

import (
	"encoding/json"
	"fmt"

	"github.com/HBulgat/mini-agent/internal/llm"
)

// blocks_json and original_ids_json are stored as plain TEXT in SQLite.
// We encode/decode them with the standard JSON marshaler — the
// llm.ContentBlock struct's field tags + zero-value omitempty (which we
// add via a tagged shadow type) keep the rows small and self-describing.

// jsonBlock is the on-disk shape of llm.ContentBlock. It mirrors the
// canonical struct field-for-field and adds `omitempty` JSON tags so a
// plain text block doesn't waste bytes on tool_use fields.
//
// Why a shadow type instead of editing llm.ContentBlock's tags directly:
// the canonical type lives in internal/llm and gets serialized differently
// when sent to a Provider's HTTP body (each Codec applies provider-specific
// renames). The on-disk shape is *our* concern alone.
type jsonBlock struct {
	Type              llm.ContentBlockType `json:"type"`
	Text              string               `json:"text,omitempty"`
	Thinking          string               `json:"thinking,omitempty"`
	ThinkingSignature string               `json:"thinking_signature,omitempty"`
	ToolUseID         string               `json:"tool_use_id,omitempty"`
	ToolName          string               `json:"tool_name,omitempty"`
	ToolInput         map[string]any       `json:"tool_input,omitempty"`
	ToolUseRefID      string               `json:"tool_use_ref_id,omitempty"`
	Output            string               `json:"output,omitempty"`
	IsError           bool                 `json:"is_error,omitempty"`
}

func toJSONBlock(b llm.ContentBlock) jsonBlock {
	return jsonBlock(b) // identical layout — Go permits the conversion
}

func fromJSONBlock(b jsonBlock) llm.ContentBlock {
	return llm.ContentBlock(b)
}

// encodeBlocks serializes a slice of canonical content blocks for the
// `blocks_json` column. Always returns valid JSON — even an empty input
// produces "[]" so the column never holds NULL.
func encodeBlocks(blocks []llm.ContentBlock) (string, error) {
	if len(blocks) == 0 {
		return "[]", nil
	}
	out := make([]jsonBlock, len(blocks))
	for i, b := range blocks {
		out[i] = toJSONBlock(b)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("store: encode blocks: %w", err)
	}
	return string(raw), nil
}

// decodeBlocks reverses encodeBlocks. An empty/whitespace string and
// "[]" both decode to a nil slice (cheaper for callers that range over
// it without a length check).
func decodeBlocks(raw string) ([]llm.ContentBlock, error) {
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	var jb []jsonBlock
	if err := json.Unmarshal([]byte(raw), &jb); err != nil {
		return nil, fmt.Errorf("store: decode blocks: %w", err)
	}
	if len(jb) == 0 {
		return nil, nil
	}
	out := make([]llm.ContentBlock, len(jb))
	for i, b := range jb {
		out[i] = fromJSONBlock(b)
	}
	return out, nil
}

// encodeIDs serializes the OriginalIDs slice. Same NULL-avoidance rule
// as encodeBlocks: we always write a valid JSON array.
func encodeIDs(ids []string) (string, error) {
	if len(ids) == 0 {
		return "[]", nil
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("store: encode ids: %w", err)
	}
	return string(raw), nil
}

func decodeIDs(raw string) ([]string, error) {
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("store: decode ids: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// asInt64 / asFloat64 unwrap the `interface{}` columns sqlc emits for
// COALESCE(SUM(...)) aggregates. modernc/sqlite scans numeric SUM
// results as int64 (and 0.0-defaulted floats as float64), so we accept
// both shapes and silently coerce.
//
// Returning 0 on a nil/unknown shape is the right default for usage
// aggregation: an empty session has zero usage.
func asInt64(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case []byte:
		// modernc/sqlite occasionally returns numeric literals as
		// []byte when the column has affinity NUMERIC; ParseInt would
		// be overkill — we just return 0 because zero-row aggregates
		// already map to 0.
		_ = x
		return 0
	default:
		return 0
	}
}

func asFloat64(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case float64:
		return x
	case int64:
		return float64(x)
	case int:
		return float64(x)
	default:
		return 0
	}
}
