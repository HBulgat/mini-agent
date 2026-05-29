// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

package agent

import (
	"github.com/HBulgat/mini-agent/internal/llm"
)

// synthesizeInterruptedResult builds a placeholder tool_result for a
// tool_use that never got executed (typically because ctx was
// cancelled mid-batch). Without this, the next-turn LLM request
// would be rejected — both Anthropic and OpenAI demand every
// tool_use have a matching tool_result in the conversation history
// (D64 §9.7.1).
//
// We mark IsError=true so the model knows the call did not actually
// run; the message body explains why. Subsequent attempts will see a
// clean history and can retry the operation.
func synthesizeInterruptedResult(call llm.ContentBlock) llm.Message {
	return llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{{
			Type:         llm.BlockToolResult,
			ToolUseRefID: call.ToolUseID,
			Output:       "[interrupted before tool was invoked]",
			IsError:      true,
		}},
	}
}

// toolResultMsg wraps a (possibly large) text payload into the
// canonical user+tool_result message shape. isError signals to the
// LLM whether to treat the body as an error context or as legitimate
// tool output.
func toolResultMsg(call llm.ContentBlock, output string, isError bool) llm.Message {
	return llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{{
			Type:         llm.BlockToolResult,
			ToolUseRefID: call.ToolUseID,
			Output:       output,
			IsError:      isError,
		}},
	}
}

// extractToolUseBlocks pulls every Type=ToolUse block out of an
// assistant message in original order. Used by the loop to decide
// whether the turn ended (no tool_uses) or needs another iteration
// (one or more tool_uses).
func extractToolUseBlocks(msg llm.Message) []llm.ContentBlock {
	var out []llm.ContentBlock
	for _, b := range msg.Content {
		if b.Type == llm.BlockToolUse {
			out = append(out, b)
		}
	}
	return out
}
