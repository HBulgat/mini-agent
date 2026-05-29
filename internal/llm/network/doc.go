// Package network is the shared network layer for every Provider
// sub-package (openai/anthropic/gemini). It provides:
//
//   - HTTPError       a typed error carrying StatusCode + RetryAfter
//   - RetryConfig     exponential-backoff + jitter retry policy
//   - TimeoutConfig   request vs total timeout split
//   - WithRetry       loop helper that respects ctx cancellation
//   - WithTimeout     ctx wrapper with the total budget
//
// Reference: docs/system-design/08-llm-providers.md §8.10 (R5, D44)
package network
