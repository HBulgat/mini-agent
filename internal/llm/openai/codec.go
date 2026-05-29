package openai

import (
	"encoding/json"
	"fmt"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"

	"github.com/HBulgat/mini-agent/internal/llm"
)

// buildAPIRequest converts a canonical llm.Request into the openai-go
// ChatCompletionNewParams shape. We don't expose the result outside
// the package — the Provider hands it straight to the SDK.
//
// Key translations:
//   - Each canonical Message → one or more SDK message params (a tool
//     result becomes its own `role: tool` entry).
//   - llm.Request.ThinkingEffort → params.ReasoningEffort (skipped for
//     non-o-series models; the model-table check is inside the
//     Provider, not here).
//   - llm.Request.Tools → params.Tools (FunctionDefinition).
//   - StreamOptions.IncludeUsage is always true so we get the final
//     token-count chunk per §8.3 fallback.
func buildAPIRequest(req *llm.Request, model string, sendThinking bool, effort string) (openaisdk.ChatCompletionNewParams, error) {
	params := openaisdk.ChatCompletionNewParams{
		Model:         shared.ChatModel(model),
		StreamOptions: openaisdk.ChatCompletionStreamOptionsParam{IncludeUsage: param.NewOpt(true)},
	}

	// ----- messages -----
	msgs, err := canonicalMessagesToParams(req.Messages)
	if err != nil {
		return openaisdk.ChatCompletionNewParams{}, err
	}
	params.Messages = msgs

	// ----- temperature / max tokens / stop -----
	if req.Temperature != nil {
		params.Temperature = param.NewOpt(float64(*req.Temperature))
	}
	if req.MaxTokens != nil {
		// MaxCompletionTokens is the new field; MaxTokens is legacy and
		// rejected by o-series. Use the new one universally.
		params.MaxCompletionTokens = param.NewOpt(int64(*req.MaxTokens))
	}
	if len(req.Stop) > 0 {
		params.Stop = openaisdk.ChatCompletionNewParamsStopUnion{
			OfStringArray: req.Stop,
		}
	}

	// ----- thinking / reasoning effort -----
	if sendThinking {
		params.ReasoningEffort = shared.ReasoningEffort(effort)
	}

	// ----- tools -----
	if len(req.Tools) > 0 {
		tools := make([]openaisdk.ChatCompletionToolParam, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, openaisdk.ChatCompletionToolParam{
				Function: shared.FunctionDefinitionParam{
					Name:        t.Name,
					Description: param.NewOpt(t.Description),
					Parameters:  shared.FunctionParameters(t.Schema),
				},
			})
		}
		params.Tools = tools
	}

	// ----- tool choice -----
	if tc := canonicalToolChoiceToParam(req.ToolChoice); tc != nil {
		params.ToolChoice = *tc
	}

	return params, nil
}

// canonicalToolChoiceToParam translates llm.ToolChoice. Returns nil
// (omits the field) for ToolChoiceAuto when no tools are configured —
// the SDK omits the JSON anyway, but explicit nil keeps the wire small.
func canonicalToolChoiceToParam(tc llm.ToolChoice) *openaisdk.ChatCompletionToolChoiceOptionUnionParam {
	switch tc.Mode {
	case llm.ToolChoiceAuto:
		out := openaisdk.ChatCompletionToolChoiceOptionUnionParam{
			OfAuto: param.NewOpt("auto"),
		}
		return &out
	case llm.ToolChoiceNone:
		out := openaisdk.ChatCompletionToolChoiceOptionUnionParam{
			OfAuto: param.NewOpt("none"),
		}
		return &out
	case llm.ToolChoiceRequired:
		out := openaisdk.ChatCompletionToolChoiceOptionUnionParam{
			OfAuto: param.NewOpt("required"),
		}
		return &out
	case llm.ToolChoiceSpecific:
		named := openaisdk.ChatCompletionNamedToolChoiceParam{
			Function: openaisdk.ChatCompletionNamedToolChoiceFunctionParam{
				Name: tc.Name,
			},
		}
		out := openaisdk.ChatCompletionToolChoiceOptionUnionParam{
			OfChatCompletionNamedToolChoice: &named,
		}
		return &out
	}
	return nil
}

// canonicalMessagesToParams flattens canonical Messages into the
// SDK's `[]ChatCompletionMessageParamUnion`. The complication is
// that one canonical assistant Message may contain text + thinking +
// multiple tool_use blocks together, while OpenAI's wire shape wants
// the tool_calls collected onto a single assistant message and any
// tool_result blocks emitted as separate `role: tool` messages.
//
// We deliberately drop pure thinking blocks here — OpenAI's chat-
// completions API has no slot for them on inbound history (the
// reasoning_content is per-stream, not per-message in the request).
// This matches D36: thinking blocks are kept in our DB for visibility
// but not echoed back to OpenAI.
func canonicalMessagesToParams(in []llm.Message) ([]openaisdk.ChatCompletionMessageParamUnion, error) {
	out := make([]openaisdk.ChatCompletionMessageParamUnion, 0, len(in))
	for i := range in {
		m := &in[i]
		switch m.Role {
		case llm.RoleSystem:
			text := flattenText(m.Content)
			if text == "" {
				continue
			}
			out = append(out, openaisdk.SystemMessage(text))

		case llm.RoleUser:
			// Tool results live on user messages canonically; OpenAI
			// wants them as their own role:tool entries.
			extracted := extractToolResults(m.Content)
			text := flattenText(m.Content)
			if text != "" {
				out = append(out, openaisdk.UserMessage(text))
			}
			for _, tr := range extracted {
				out = append(out, openaisdk.ToolMessage(tr.output, tr.refID))
			}

		case llm.RoleAssistant:
			// Build a single assistant message holding text + tool_calls.
			text := flattenText(m.Content)
			calls := extractToolUses(m.Content)

			if len(calls) == 0 && text == "" {
				// Pure thinking block — skip per the docstring.
				continue
			}

			assistant := openaisdk.ChatCompletionAssistantMessageParam{}
			if text != "" {
				assistant.Content.OfString = param.NewOpt(text)
			}
			if len(calls) > 0 {
				converted := make([]openaisdk.ChatCompletionMessageToolCallParam, 0, len(calls))
				for _, c := range calls {
					argsJSON, err := json.Marshal(c.input)
					if err != nil {
						return nil, fmt.Errorf("openai: marshal tool args for %s: %w", c.name, err)
					}
					converted = append(converted, openaisdk.ChatCompletionMessageToolCallParam{
						ID: c.id,
						Function: openaisdk.ChatCompletionMessageToolCallFunctionParam{
							Name:      c.name,
							Arguments: string(argsJSON),
						},
					})
				}
				assistant.ToolCalls = converted
			}
			out = append(out, openaisdk.ChatCompletionMessageParamUnion{OfAssistant: &assistant})

		case llm.RoleTool:
			// Canonical layer prefers user+ToolResultBlock, but we
			// tolerate the legacy shape for round-tripping older
			// sessions.
			for _, b := range m.Content {
				if b.Type == llm.BlockToolResult {
					out = append(out, openaisdk.ToolMessage(b.Output, b.ToolUseRefID))
				}
			}
		}
	}
	return out, nil
}

// flattenText concatenates every BlockText body in order. Empty input
// returns "". This is a lossy view — it deliberately ignores tool_use,
// tool_result, and thinking blocks. The caller is expected to extract
// those separately.
func flattenText(blocks []llm.ContentBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	if len(blocks) == 1 && blocks[0].Type == llm.BlockText {
		return blocks[0].Text
	}
	var s string
	for _, b := range blocks {
		if b.Type == llm.BlockText {
			if s != "" {
				s += "\n"
			}
			s += b.Text
		}
	}
	return s
}

// toolResult is the local-only shape extractToolResults emits.
type toolResult struct {
	refID  string
	output string
}

func extractToolResults(blocks []llm.ContentBlock) []toolResult {
	var out []toolResult
	for _, b := range blocks {
		if b.Type == llm.BlockToolResult {
			out = append(out, toolResult{refID: b.ToolUseRefID, output: b.Output})
		}
	}
	return out
}

// toolUse mirrors extractToolUses' return value.
type toolUse struct {
	id    string
	name  string
	input map[string]any
}

func extractToolUses(blocks []llm.ContentBlock) []toolUse {
	var out []toolUse
	for _, b := range blocks {
		if b.Type == llm.BlockToolUse {
			out = append(out, toolUse{id: b.ToolUseID, name: b.ToolName, input: b.ToolInput})
		}
	}
	return out
}
