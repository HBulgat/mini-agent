// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

// Package ask hosts the `ask_user` tool — the only Meta-category
// tool the agent uses to talk back to its operator. The tool is a
// thin adapter: it converts the LLM's tool_use into a
// uio.QuestionRequest, hands it to the injected Prompter, and wraps
// the operator's reply into a tool.Result.
//
// Why a Prompter pointer rather than a function callback (like cwd)?
//
//   - The Prompter interface is the canonical user-facing shape
//     defined by R3 (`internal/uio`). Adapting it to a `func(ctx,
//     req) (string, error)` would lose nothing functionally but
//     would force every CLI/Web entry point to write the adapter
//     shim. Passing the interface directly keeps the wiring
//     symmetric across tools that use different uio capabilities
//     (a future tool might also want Sink).
//   - The interface is small (4 methods) and stable — there's no
//     risk of accidentally widening the dependency.
package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/invopop/jsonschema"

	"github.com/HBulgat/mini-agent/internal/tool"
	"github.com/HBulgat/mini-agent/internal/uio"
)

// AskUser is the `ask_user` tool. Each Invoke calls
// Prompter.AskUser exactly once and forwards the answer.
type AskUser struct {
	prompter uio.Prompter
}

// NewAskUser constructs the tool. The prompter must be non-nil; with
// a nil prompter, every Invoke would fail-closed with ErrInvalidArgs
// because there's no way to actually ask. We accept it without panic
// at construction so bootstrap can wire the fallback NopPrompter
// without an extra guard.
func NewAskUser(p uio.Prompter) *AskUser {
	return &AskUser{prompter: p}
}

// AskUserArgs is the JSON input. Only `question` is required.
type AskUserArgs struct {
	// Question is the prompt shown to the operator. Required.
	Question string `json:"question" jsonschema:"required,description=The question to ask the operator. Use a single complete sentence; this is rendered verbatim in CLI/Web."`

	// Hint is optional placeholder/explanation text. CLI implementations
	// render it as muted text below the question; Web implementations
	// may render it as a tooltip.
	Hint string `json:"hint,omitempty" jsonschema:"description=Optional placeholder/explanation text shown alongside the question."`
}

// ============================================================
// tool.Tool
// ============================================================

func (a *AskUser) Name() string { return "ask_user" }

func (a *AskUser) Description() string {
	return strings.TrimSpace(`
Ask the operator a question and wait for their reply.

When to use:
- Critical decisions that genuinely need human judgement (e.g. "Which
  of these two API designs do you prefer?")
- Disambiguating an ambiguous request before doing work
- Confirming destructive intent before a tool call the gate cannot
  perfectly judge

When NOT to use:
- Asking permission for a tool call — the permission gate handles
  that automatically; calling ask_user is duplicate work
- Trivial confirmations the agent should infer from context
- Reading the current state of a file/dir — use the read tools

Notes:
- Even in --yes mode this tool blocks until the operator responds
  (yes-mode applies to *risk* approvals, not human-in-the-loop input).
- Returns the operator's verbatim answer in Result.Content. The agent
  loop should treat the answer as opaque text — no parsing.
`)
}

func (a *AskUser) Schema() map[string]any {
	r := &jsonschema.Reflector{
		ExpandedStruct:             true,
		DoNotReference:             true,
		RequiredFromJSONSchemaTags: true,
		Anonymous:                  true,
		AllowAdditionalProperties:  false,
	}
	return schemaToMap(r.Reflect(&AskUserArgs{}))
}

// Category puts ask_user in the Meta bucket. Per R5 §5.5 + D86 the
// gate's matrixDecision returns DecisionAllow for Meta in every mode,
// so this tool runs even under --plan.
func (a *AskUser) Category() tool.Category { return tool.CategoryMeta }

// Invoke routes the question through the Prompter. ctx cancellation
// propagates via the underlying Prompter implementation (CLI's stdin
// reader honours ctx.Done; Web's SSE/REST round-trip drops the wait
// when the page closes).
func (a *AskUser) Invoke(ctx context.Context, input map[string]any) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, tool.MapCtxError(err)
	}
	// Per-request Prompter (set by agent.Loop from RunInput) takes
	// precedence over the construction-time fallback. This lets a
	// single registered AskUser tool serve concurrent CLI / Web
	// runs that each carry their own Prompter — without the bridge
	// the bootstrap-time NopPrompter would freeze every Invoke.
	prompter := a.prompter
	if p, ok := uio.PrompterFromContext(ctx); ok {
		prompter = p
	}
	if prompter == nil {
		// Fail-closed: no prompter available anywhere. The agent
		// loop will surface this as a tool failure and most
		// likely fall back to producing a textual answer instead.
		return tool.Result{}, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: "ask_user has no prompter wired (running headless?)",
		}
	}

	args, err := a.decodeArgs(input)
	if err != nil {
		return tool.Result{}, err
	}
	if err := a.validateArgs(args); err != nil {
		return tool.Result{}, err
	}

	answer, err := prompter.AskUser(ctx, uio.QuestionRequest{
		Question: args.Question,
		Hint:     args.Hint,
	})
	if err != nil {
		// Prompter errors map onto the standard ctx error contract
		// when they're cancellation-shaped; everything else is
		// treated as a generic IO failure.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return tool.Result{}, tool.MapCtxError(ctxErr)
		}
		return tool.Result{}, tool.MapIOError(err)
	}

	return tool.Result{
		Content: buildAskContent(args.Question, answer),
		Display: buildAskDisplay(args.Question, answer),
	}, nil
}

// ============================================================
// argument decoding & validation
// ============================================================

func (a *AskUser) decodeArgs(input map[string]any) (*AskUserArgs, error) {
	if input == nil {
		return nil, &tool.Error{Code: tool.ErrInvalidArgs, Message: "input is nil"}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, &tool.Error{Code: tool.ErrInvalidArgs, Message: fmt.Sprintf("re-marshal input: %v", err)}
	}
	var out AskUserArgs
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, &tool.Error{Code: tool.ErrInvalidArgs, Message: err.Error()}
	}
	return &out, nil
}

func (a *AskUser) validateArgs(args *AskUserArgs) error {
	if strings.TrimSpace(args.Question) == "" {
		return &tool.Error{Code: tool.ErrInvalidArgs, Message: "question is required"}
	}
	return nil
}

// ============================================================
// content / display
// ============================================================

// buildAskContent wraps the Q+A in a structured tag per D73. The
// agent loop will hand this back to the LLM, which should treat the
// <answer> contents as the operator's verbatim reply.
func buildAskContent(question, answer string) string {
	var b strings.Builder
	b.WriteString("<ask_user>\n")
	fmt.Fprintf(&b, "<question>%s</question>\n", escapeForTag(question))
	fmt.Fprintf(&b, "<answer>%s</answer>\n", escapeForTag(answer))
	b.WriteString("</ask_user>")
	return b.String()
}

// buildAskDisplay produces the user-facing one-liner per D74:
//
//	ask_user "<question snippet>" → "<answer snippet>"
//
// Both fields are snipped to 60 chars so the trace line stays tidy.
func buildAskDisplay(question, answer string) string {
	q := snippet(question, 60)
	a := snippet(answer, 60)
	return fmt.Sprintf("ask_user %q → %q", q, a)
}

// snippet truncates s to at most n bytes, appending "..." when cut.
// Chosen as bytes (not runes) because trace lines are byte-oriented;
// a Chinese character could land one byte short and look ugly, but
// the alternative (rune-aware) is overkill for a trace line.
func snippet(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// escapeForTag is a minimal XML-ish escape for the bracketing chars
// that would corrupt our <ask_user> envelope. We don't aim for full
// XML compliance — the LLM doesn't need it.
func escapeForTag(s string) string {
	if !strings.ContainsAny(s, "<>&") {
		return s
	}
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// schemaToMap converts an *jsonschema.Schema to map[string]any via
// JSON round-trip — same shape used by every other tool package.
func schemaToMap(s any) map[string]any {
	if s == nil {
		return map[string]any{}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}
