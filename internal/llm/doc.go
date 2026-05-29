// Package llm — see llm.go for the canonical type contract. The
// per-provider sub-packages (openai, anthropic, gemini) implement the
// Provider interface and are landed by T1.4 / T4.9; until then this
// package only exposes the types so other modules (session, agent,
// tool) can compile.
//
// Design references:
//   - docs/system-design/05-core-abstractions.md §5.3 (R3 + R5)
//   - docs/system-design/08-llm-providers.md (R5, D32–D48)
package llm
