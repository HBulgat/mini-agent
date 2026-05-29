package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/ssestream"

	"github.com/HBulgat/mini-agent/internal/llm"
)

// streamConsumer turns the SDK's *ssestream.Stream[ChatCompletionChunk]
// into a `chan llm.StreamEvent`. It is the heart of §8.5 — every quirk
// of the OpenAI / DeepSeek wire shape is handled here so the agent
// loop sees a uniform sequence of events.
//
// Lifecycle (the consumer goroutine drives this):
//  1. Read chunks until Stream.Next() returns false.
//  2. For each chunk:
//     - drain delta.content / delta.reasoning_content into Delta.Content
//       / Delta.Thinking, synthesizing StreamBlockBoundary events on
//       the text↔thinking transition (§8.4.3).
//     - drain delta.tool_calls into the per-index partialToolCall buffer
//       and emit a StreamDelta with ToolCallDelta for each fragment.
//     - capture usage if present (only the trailing chunk carries it).
//  3. After the loop, finalize: parse buffered tool_calls into BlockToolUse,
//     emit a StreamFinal carrying the assembled Message + Usage.
//  4. If the SDK returned an error, surface it as StreamError + close.
//
// The caller is expected to close the SDK stream itself; we read until
// Next() returns false.
type streamConsumer struct {
	sdkStream *ssestream.Stream[openaisdk.ChatCompletionChunk]
	out       chan<- llm.StreamEvent
	model     string

	// Per-stream accumulators.
	textBuf     bytes.Buffer
	thinkBuf    bytes.Buffer
	thinkSig    string                 // populated when SDK exposes encrypted_content (rare)
	toolCalls   map[int64]*partialCall // keyed by index
	finalUsage  *llm.Usage
	stopReason  llm.StopReason
	currentKind llm.ContentBlockType // tracks the active block for boundary synthesis
	blockIndex  int                  // synthetic block index for OpenAI
}

// partialCall accumulates a single function call across chunks. The
// SDK gives us {ID, Name} on the first chunk and Arguments fragments
// thereafter; we keep them in insertion order so the final Message
// preserves the order the model emitted.
type partialCall struct {
	index int64
	id    string
	name  string
	args  bytes.Buffer
}

func newStreamConsumer(s *ssestream.Stream[openaisdk.ChatCompletionChunk], out chan<- llm.StreamEvent, model string) *streamConsumer {
	return &streamConsumer{
		sdkStream: s,
		out:       out,
		model:     model,
		toolCalls: make(map[int64]*partialCall),
	}
}

// run is the goroutine entry point. It does NOT close `out` — the
// Provider's caller controls that. We do close the SDK stream on the
// way out via Close().
func (c *streamConsumer) run(ctx context.Context) {
	defer func() { _ = c.sdkStream.Close() }()

	for c.sdkStream.Next() {
		// Promptly honor cancellation between chunks.
		if err := ctx.Err(); err != nil {
			return
		}
		chunk := c.sdkStream.Current()
		c.handleChunk(&chunk)
	}

	if err := c.sdkStream.Err(); err != nil && !errors.Is(err, context.Canceled) {
		c.send(llm.StreamEvent{Type: llm.StreamError, Err: err})
		return
	}

	c.finalize()
}

func (c *streamConsumer) handleChunk(chunk *openaisdk.ChatCompletionChunk) {
	// Usage chunk: SDK sometimes returns choices=[] with usage set.
	if chunk.Usage.TotalTokens > 0 || chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
		u := llm.Usage{
			PromptTokens:     int(chunk.Usage.PromptTokens),
			CompletionTokens: int(chunk.Usage.CompletionTokens),
			TotalTokens:      int(chunk.Usage.TotalTokens),
		}
		// Reasoning + cached prompt are optional sub-objects; pull them
		// out of the raw JSON since the SDK exposes them via nested types.
		extractReasoningCached(chunk.Usage.RawJSON(), &u)
		c.finalUsage = &u
	}

	if len(chunk.Choices) == 0 {
		return
	}
	choice := &chunk.Choices[0]

	// Some chunks carry a finish_reason (the last content chunk).
	if choice.FinishReason != "" {
		c.stopReason = mapStopReason(string(choice.FinishReason))
	}

	delta := &choice.Delta

	// Text content.
	if delta.Content != "" {
		c.transitionTo(llm.BlockText)
		c.textBuf.WriteString(delta.Content)
		c.send(llm.StreamEvent{
			Type:  llm.StreamDelta,
			Delta: llm.Delta{Content: delta.Content},
		})
	}

	// Reasoning content — only present on DeepSeek / o-series. The SDK
	// type doesn't expose it as a typed field, so we pull it from
	// JSON.raw via extractReasoning.
	if reasoning := extractReasoning(delta.RawJSON()); reasoning != "" {
		c.transitionTo(llm.BlockThinking)
		c.thinkBuf.WriteString(reasoning)
		c.send(llm.StreamEvent{
			Type:  llm.StreamDelta,
			Delta: llm.Delta{Thinking: reasoning},
		})
	}

	// Tool calls — partial, indexed.
	for _, tc := range delta.ToolCalls {
		c.handleToolCallDelta(&tc)
	}
}

// transitionTo emits BlockBoundary events when the active content kind
// changes. OpenAI doesn't send block_start / block_stop natively, so
// we synthesize them based on which delta field arrived (§8.4.3).
func (c *streamConsumer) transitionTo(next llm.ContentBlockType) {
	if c.currentKind == next {
		return
	}
	if c.currentKind != "" {
		c.send(llm.StreamEvent{
			Type: llm.StreamBlockBoundary,
			Boundary: &llm.BlockBoundary{
				BlockType: c.currentKind,
				IsStart:   false,
				Index:     c.blockIndex,
			},
		})
		c.blockIndex++
	}
	c.send(llm.StreamEvent{
		Type: llm.StreamBlockBoundary,
		Boundary: &llm.BlockBoundary{
			BlockType: next,
			IsStart:   true,
			Index:     c.blockIndex,
		},
	})
	c.currentKind = next
}

func (c *streamConsumer) handleToolCallDelta(tc *openaisdk.ChatCompletionChunkChoiceDeltaToolCall) {
	pc := c.toolCalls[tc.Index]
	if pc == nil {
		pc = &partialCall{index: tc.Index}
		c.toolCalls[tc.Index] = pc
	}
	if tc.ID != "" {
		pc.id = tc.ID
	}
	if tc.Function.Name != "" {
		pc.name = tc.Function.Name
	}
	if tc.Function.Arguments != "" {
		pc.args.WriteString(tc.Function.Arguments)
	}

	c.send(llm.StreamEvent{
		Type: llm.StreamDelta,
		Delta: llm.Delta{
			ToolCallDelta: &llm.ToolCallDelta{
				Index:    int(tc.Index),
				ID:       pc.id,
				Name:     pc.name,
				ArgsDiff: tc.Function.Arguments,
			},
		},
	})
}

// finalize closes any open block, parses the accumulated tool_call
// arguments, and emits the terminal StreamFinal event carrying the
// fully-assembled Message + Usage.
func (c *streamConsumer) finalize() {
	if c.currentKind != "" {
		c.send(llm.StreamEvent{
			Type: llm.StreamBlockBoundary,
			Boundary: &llm.BlockBoundary{
				BlockType: c.currentKind,
				IsStart:   false,
				Index:     c.blockIndex,
			},
		})
	}

	final := llm.FinalResponse{
		StopReason: c.stopReason,
	}
	if c.finalUsage != nil {
		final.Usage = *c.finalUsage
	}

	// Build canonical Message. Order:
	//   1. Thinking block (if any)
	//   2. Text block (if any)
	//   3. ToolUse blocks (in index order)
	final.Message = llm.Message{Role: llm.RoleAssistant}
	if c.thinkBuf.Len() > 0 {
		final.Message.Content = append(final.Message.Content, llm.ContentBlock{
			Type:              llm.BlockThinking,
			Thinking:          c.thinkBuf.String(),
			ThinkingSignature: c.thinkSig,
		})
	}
	if c.textBuf.Len() > 0 {
		final.Message.Content = append(final.Message.Content, llm.ContentBlock{
			Type: llm.BlockText,
			Text: c.textBuf.String(),
		})
	}
	for _, pc := range orderedCalls(c.toolCalls) {
		input := map[string]any{}
		if pc.args.Len() > 0 {
			if err := json.Unmarshal(pc.args.Bytes(), &input); err != nil {
				// Tolerate malformed JSON: surface the raw text via a
				// magic field so the tool layer can decide what to do.
				input = map[string]any{"_raw": pc.args.String()}
			}
		}
		final.Message.Content = append(final.Message.Content, llm.ContentBlock{
			Type:      llm.BlockToolUse,
			ToolUseID: pc.id,
			ToolName:  pc.name,
			ToolInput: input,
		})
	}

	c.send(llm.StreamEvent{
		Type:  llm.StreamFinal,
		Final: &final,
	})
}

// orderedCalls returns partialCalls sorted by their original Index,
// so the assembled Message preserves the model's emission order.
func orderedCalls(m map[int64]*partialCall) []*partialCall {
	if len(m) == 0 {
		return nil
	}
	out := make([]*partialCall, 0, len(m))
	for _, pc := range m {
		out = append(out, pc)
	}
	// Stable sort by Index using insertion sort — the slices are tiny
	// (rarely more than 4-5 calls) so the simpler algorithm is fine.
	for i := 1; i < len(out); i++ {
		j := i
		for j > 0 && out[j-1].index > out[j].index {
			out[j-1], out[j] = out[j], out[j-1]
			j--
		}
	}
	return out
}

func (c *streamConsumer) send(e llm.StreamEvent) {
	// Non-blocking send: if the receiver is gone we drop. The agent
	// loop guarantees a buffer of 16 in newStreamConsumer's caller, so
	// dropping should be exceedingly rare.
	select {
	case c.out <- e:
	default:
		// Buffer full — fall back to a blocking send so we don't lose
		// events under load. The blocking path also respects ctx
		// because the surrounding goroutine returns when ctx fires.
		c.out <- e
	}
}

// mapStopReason translates an OpenAI finish_reason string into our
// canonical StopReason enum.
func mapStopReason(reason string) llm.StopReason {
	switch reason {
	case "stop":
		return llm.StopReasonEnd
	case "tool_calls", "function_call":
		return llm.StopReasonToolCall
	case "length":
		return llm.StopReasonMaxTokens
	case "content_filter":
		return llm.StopReasonContentFilter
	default:
		return llm.StopReasonEnd
	}
}

// extractReasoning pulls `delta.reasoning_content` out of the raw JSON
// payload of a delta. The OpenAI SDK doesn't model this field (it's a
// DeepSeek / o-series extension), so we parse it ad-hoc.
//
// Returns "" when the field is absent or empty — the caller treats
// that as "no reasoning in this chunk".
func extractReasoning(rawJSON string) string {
	if rawJSON == "" {
		return ""
	}
	// Cheap pre-filter: skip the JSON parse when the substring isn't
	// even present. Saves a parse on the dominant "no reasoning" path.
	if !bytes.Contains([]byte(rawJSON), []byte(`"reasoning_content"`)) {
		return ""
	}
	var holder struct {
		ReasoningContent string `json:"reasoning_content"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &holder); err != nil {
		return ""
	}
	return holder.ReasoningContent
}

// extractReasoningCached pulls reasoning_tokens + cached_tokens out of
// usage.RawJSON. The SDK doesn't expose `prompt_tokens_details` /
// `completion_tokens_details` as typed fields in v1.12 either. We
// merge whatever we find into `u`.
func extractReasoningCached(rawJSON string, u *llm.Usage) {
	if rawJSON == "" || u == nil {
		return
	}
	var holder struct {
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
		// DeepSeek non-standard top-level field.
		ReasoningTokens int `json:"reasoning_tokens"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &holder); err != nil {
		return
	}
	u.CachedPromptTokens = holder.PromptTokensDetails.CachedTokens
	if holder.CompletionTokensDetails.ReasoningTokens > 0 {
		u.ReasoningTokens = holder.CompletionTokensDetails.ReasoningTokens
	} else if holder.ReasoningTokens > 0 {
		u.ReasoningTokens = holder.ReasoningTokens
	}
}
