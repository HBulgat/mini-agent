// Package openai implements llm.Provider against any OpenAI-compatible
// chat-completions endpoint — official OpenAI, DeepSeek, Kimi, Qwen,
// or any self-hosted server speaking the same wire shape. R5 calls
// this the "OpenAI 兼容" Provider.
//
// Files:
//
//	config.go    Config struct + sensible defaults
//	pricing.go   built-in modelTable (capabilities + per-MTok pricing)
//	thinking.go  ThinkingEffort → reasoning.effort + reasoning_content adapter
//	codec.go     canonical Request ↔ openai-go params (and the inverse for streaming chunks)
//	stream.go    line-by-line stream consumer; emits StreamEvent + StreamBlockBoundary
//	provider.go  the public Provider that ties everything together
//
// Reference: docs/system-design/08-llm-providers.md (R5 §8.4–§8.12)
package openai
