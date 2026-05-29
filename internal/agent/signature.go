// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// signature produces a stable identifier for a (tool_name, args) pair
// per D56. The identifier feeds the failure counter so the loop can
// detect when the model is calling the same thing the same way over
// and over.
//
// Format: "<tool_name>:<sha256(canonicalJSON(args))[:16hex]>".
//
// Why hash:
//   - Args may be large maps with file contents; including the raw
//     bytes in the counter map blows up memory.
//   - 8 bytes (16 hex chars) is collision-resistant for the small N
//     of distinct args we see per session.
//
// Why canonical JSON (sorted keys):
//   - Go's encoding/json doesn't guarantee key order; without sorting
//     two semantically-identical args would hash differently and
//     defeat the counter.
//   - Nested maps and arrays are walked recursively so order
//     consistency holds at every level.
//
// Why we deliberately do NOT path-canonicalise (D56 + C2):
//   - "./go.mod" and "go.mod" produce different signatures. When the
//     model retries by switching path style, the counter resets —
//     that's the desired "they tried a different way" behaviour.
func signature(toolName string, args map[string]any) string {
	cj, err := canonicalJSON(args)
	if err != nil {
		// Unhashable input — return a sentinel signature so the
		// counter can still track repeats by tool name only.
		return toolName + ":<unhashable>"
	}
	sum := sha256.Sum256(cj)
	// 8 bytes = 16 hex chars (D56).
	return toolName + ":" + hex.EncodeToString(sum[:8])
}

// canonicalJSON marshals v with deterministic key order. Maps have
// their keys sorted; arrays preserve insertion order; everything else
// goes through encoding/json verbatim.
//
// We don't try to canonicalise floats / number representations —
// JSON inputs come from the LLM as already-decoded map[string]any
// where numeric tokens are float64 either way.
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case map[string]any:
		// Sort keys for deterministic output.
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return err
			}
			buf.Write(kb)
			buf.WriteByte(':')
			if err := writeCanonical(buf, x[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil

	case []any:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil

	default:
		// Primitives + unknown types delegate to encoding/json.
		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("canonicalJSON: %w", err)
		}
		buf.Write(raw)
		return nil
	}
}
