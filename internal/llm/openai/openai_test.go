package openai

import (
	"testing"

	"github.com/HBulgat/mini-agent/internal/llm"
)

// ---------- mapThinkingEffort ----------

func TestMapThinkingEffort(t *testing.T) {
	cases := []struct {
		name      string
		enabled   bool
		effort    string
		wantSend  bool
		wantNorm  string
	}{
		{"disabled", false, "high", false, ""},
		{"empty defaults to medium", true, "", true, "medium"},
		{"low", true, "low", true, "low"},
		{"medium", true, "medium", true, "medium"},
		{"high", true, "high", true, "high"},
		{"unknown falls back", true, "extreme", true, "medium"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			send, norm := mapThinkingEffort(c.enabled, c.effort)
			if send != c.wantSend || norm != c.wantNorm {
				t.Errorf("mapThinkingEffort(%v, %q) = (%v, %q), want (%v, %q)",
					c.enabled, c.effort, send, norm, c.wantSend, c.wantNorm)
			}
		})
	}
}

// ---------- Pricing / ComputeCost ----------

func TestLookupModel_Known(t *testing.T) {
	mi, ok := LookupModel("deepseek-reasoner")
	if !ok {
		t.Fatal("deepseek-reasoner missing from modelTable")
	}
	if !mi.Capabilities.SupportsThinking {
		t.Error("deepseek-reasoner should declare SupportsThinking=true")
	}
}

func TestLookupModel_Unknown(t *testing.T) {
	if _, ok := LookupModel("imaginary"); ok {
		t.Error("LookupModel returned ok for unknown model")
	}
}

func TestComputeCost_Basic(t *testing.T) {
	mi := &ModelInfo{
		InputPerMTok:       1.0,
		OutputPerMTok:      2.0,
		ReasoningPerMTok:   4.0,
		CachedInputPerMTok: 0.5,
	}
	u := &llm.Usage{
		PromptTokens:       1_000_000, // → 1.0 USD plain * (1 - cached)
		CachedPromptTokens: 0,
		CompletionTokens:   500_000, // 0.5 of 2 = 1.0
		ReasoningTokens:    0,
	}
	got := ComputeCost(u, mi)
	// 1.0 (input) + 1.0 (output) = 2.0
	if got < 1.99 || got > 2.01 {
		t.Errorf("cost = %v, want 2.0", got)
	}
}

func TestComputeCost_WithCachedAndReasoning(t *testing.T) {
	mi := &ModelInfo{
		InputPerMTok:       1.0,
		OutputPerMTok:      2.0,
		ReasoningPerMTok:   4.0,
		CachedInputPerMTok: 0.5,
	}
	u := &llm.Usage{
		PromptTokens:       1_000_000,
		CachedPromptTokens: 200_000, // 0.2 * 0.5 = 0.10
		CompletionTokens:   500_000,
		ReasoningTokens:    100_000, // 0.1 * 4 = 0.40; rest 0.4 * 2 = 0.80
	}
	got := ComputeCost(u, mi)
	// non-cached input = 800k → 0.8
	// cached input     = 200k → 0.10
	// reasoning        = 100k → 0.40
	// plain output     = 400k → 0.80
	// total = 2.10
	if got < 2.09 || got > 2.11 {
		t.Errorf("cost = %v, want 2.10", got)
	}
}

func TestComputeCost_NilSafe(t *testing.T) {
	if got := ComputeCost(nil, nil); got != 0 {
		t.Errorf("nil cost = %v, want 0", got)
	}
}

// ---------- Codec: canonicalToolChoiceToParam ----------

func TestCanonicalToolChoiceToParam(t *testing.T) {
	if got := canonicalToolChoiceToParam(llm.ToolChoice{Mode: llm.ToolChoiceAuto}); got == nil {
		t.Error("auto: got nil, want OfAuto")
	}
	if got := canonicalToolChoiceToParam(llm.ToolChoice{Mode: llm.ToolChoiceSpecific, Name: "read_file"}); got == nil || got.OfChatCompletionNamedToolChoice == nil {
		t.Error("specific: missing named choice")
	} else if got.OfChatCompletionNamedToolChoice.Function.Name != "read_file" {
		t.Errorf("specific: got name %q", got.OfChatCompletionNamedToolChoice.Function.Name)
	}
}

// ---------- Codec: extractText / extractToolUses / extractToolResults ----------

func TestFlattenText(t *testing.T) {
	cases := []struct {
		blocks []llm.ContentBlock
		want   string
	}{
		{nil, ""},
		{[]llm.ContentBlock{{Type: llm.BlockText, Text: "a"}}, "a"},
		{[]llm.ContentBlock{
			{Type: llm.BlockText, Text: "a"},
			{Type: llm.BlockText, Text: "b"},
		}, "a\nb"},
		{[]llm.ContentBlock{
			{Type: llm.BlockText, Text: "a"},
			{Type: llm.BlockToolUse, ToolName: "x"},
		}, "a"},
	}
	for _, c := range cases {
		if got := flattenText(c.blocks); got != c.want {
			t.Errorf("flattenText(%v) = %q, want %q", c.blocks, got, c.want)
		}
	}
}

// ---------- Codec: canonicalMessagesToParams round-trip shape ----------

func TestCanonicalMessagesToParams_AssistantWithText(t *testing.T) {
	msgs := []llm.Message{
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{
				{Type: llm.BlockText, Text: "hello"},
			},
		},
	}
	params, err := canonicalMessagesToParams(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 1 {
		t.Fatalf("got %d params, want 1", len(params))
	}
	if params[0].OfAssistant == nil {
		t.Errorf("expected OfAssistant union variant")
	}
}

func TestCanonicalMessagesToParams_UserWithToolResult(t *testing.T) {
	msgs := []llm.Message{
		{
			Role: llm.RoleUser,
			Content: []llm.ContentBlock{
				{Type: llm.BlockText, Text: "please run"},
				{Type: llm.BlockToolResult, ToolUseRefID: "call-1", Output: "ok"},
			},
		},
	}
	params, err := canonicalMessagesToParams(msgs)
	if err != nil {
		t.Fatal(err)
	}
	// Expect: 1 user (text) + 1 tool (result) = 2 params.
	if len(params) != 2 {
		t.Fatalf("got %d params, want 2", len(params))
	}
}

func TestCanonicalMessagesToParams_AssistantWithToolUse(t *testing.T) {
	msgs := []llm.Message{
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{
				{Type: llm.BlockText, Text: "calling"},
				{Type: llm.BlockToolUse, ToolUseID: "call-1", ToolName: "read", ToolInput: map[string]any{"path": "/tmp/x"}},
			},
		},
	}
	params, err := canonicalMessagesToParams(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 1 {
		t.Fatalf("got %d, want 1", len(params))
	}
	a := params[0].OfAssistant
	if a == nil {
		t.Fatal("expected OfAssistant")
	}
	if len(a.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(a.ToolCalls))
	}
	if a.ToolCalls[0].Function.Name != "read" {
		t.Errorf("tool name: got %q", a.ToolCalls[0].Function.Name)
	}
}

// ---------- Codec: thinking blocks are dropped from outbound history ----------

func TestCanonicalMessagesToParams_ThinkingDropped(t *testing.T) {
	msgs := []llm.Message{
		{
			Role: llm.RoleAssistant,
			Content: []llm.ContentBlock{
				{Type: llm.BlockThinking, Thinking: "secret"},
			},
		},
	}
	params, err := canonicalMessagesToParams(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 0 {
		t.Errorf("pure-thinking msg should be dropped; got %d params", len(params))
	}
}

// ---------- Stream helpers ----------

func TestExtractReasoning_Present(t *testing.T) {
	raw := `{"role":"assistant","reasoning_content":"step 1","content":"hi"}`
	if got := extractReasoning(raw); got != "step 1" {
		t.Errorf("got %q, want step 1", got)
	}
}

func TestExtractReasoning_Absent(t *testing.T) {
	raw := `{"role":"assistant","content":"hi"}`
	if got := extractReasoning(raw); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := extractReasoning(""); got != "" {
		t.Errorf("empty: got %q", got)
	}
}

func TestExtractReasoningCached_DeepSeekStyle(t *testing.T) {
	raw := `{"prompt_tokens":100,"reasoning_tokens":42}`
	u := &llm.Usage{}
	extractReasoningCached(raw, u)
	if u.ReasoningTokens != 42 {
		t.Errorf("ReasoningTokens = %d, want 42", u.ReasoningTokens)
	}
}

func TestExtractReasoningCached_OpenAIStyle(t *testing.T) {
	raw := `{
        "prompt_tokens_details": {"cached_tokens": 30},
        "completion_tokens_details": {"reasoning_tokens": 25}
    }`
	u := &llm.Usage{}
	extractReasoningCached(raw, u)
	if u.CachedPromptTokens != 30 {
		t.Errorf("Cached: got %d, want 30", u.CachedPromptTokens)
	}
	if u.ReasoningTokens != 25 {
		t.Errorf("Reasoning: got %d, want 25", u.ReasoningTokens)
	}
}

// ---------- Stream: mapStopReason ----------

func TestMapStopReason(t *testing.T) {
	cases := map[string]llm.StopReason{
		"stop":           llm.StopReasonEnd,
		"tool_calls":     llm.StopReasonToolCall,
		"function_call":  llm.StopReasonToolCall,
		"length":         llm.StopReasonMaxTokens,
		"content_filter": llm.StopReasonContentFilter,
		"unexpected":     llm.StopReasonEnd,
	}
	for in, want := range cases {
		if got := mapStopReason(in); got != want {
			t.Errorf("mapStopReason(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---------- Provider: New / Capabilities / SetModel ----------

func TestProvider_New_RejectsBadConfig(t *testing.T) {
	cases := []*Config{
		{Name: "x", APIKey: ""}, // missing api key
		{Name: "", APIKey: "k"}, // missing name
	}
	for i, c := range cases {
		c.DefaultModel = "deepseek-chat"
		if _, err := New(c, nil, nil); err == nil {
			t.Errorf("case %d: expected validation error for %+v", i, c)
		}
	}
}

func TestProvider_New_FillsDefaults(t *testing.T) {
	cfg := &Config{
		Name:         "deepseek",
		APIKey:       "sk-fake",
		DefaultModel: "deepseek-reasoner",
	}
	p, err := New(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "deepseek" {
		t.Errorf("Name: got %q", p.Name())
	}
	caps := p.Capabilities()
	if caps.Model != "deepseek-reasoner" {
		t.Errorf("Capabilities.Model: got %q", caps.Model)
	}
	if !caps.SupportsThinking {
		t.Error("deepseek-reasoner should advertise thinking")
	}
}

func TestProvider_SetModel(t *testing.T) {
	cfg := &Config{Name: "x", APIKey: "k", DefaultModel: "deepseek-chat"}
	p, _ := New(cfg, nil, nil)
	if err := p.SetModel("gpt-4o"); err != nil {
		t.Fatal(err)
	}
	if p.activeModel() != "gpt-4o" {
		t.Errorf("activeModel: got %q, want gpt-4o", p.activeModel())
	}
	if p.Capabilities().Model != "gpt-4o" {
		t.Errorf("Capabilities.Model: got %q, want gpt-4o", p.Capabilities().Model)
	}
}

func TestProvider_UnknownModelFallsBack(t *testing.T) {
	cfg := &Config{Name: "x", APIKey: "k", DefaultModel: "totally-made-up"}
	p, err := New(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	caps := p.Capabilities()
	// Falls back to defaultCapabilities (8192/4096/no thinking).
	if caps.SupportsThinking {
		t.Error("unknown model should default to SupportsThinking=false")
	}
	if caps.ContextWindow != 8192 {
		t.Errorf("ContextWindow: got %d, want 8192", caps.ContextWindow)
	}
}

func TestProvider_ForceThinking(t *testing.T) {
	cfg := &Config{
		Name:          "x",
		APIKey:        "k",
		DefaultModel:  "self-hosted-r1",
		ForceThinking: true,
	}
	p, _ := New(cfg, nil, nil)
	if !p.Capabilities().SupportsThinking {
		t.Error("ForceThinking=true should make Capabilities.SupportsThinking true")
	}
}

func TestProvider_EstimateTokens(t *testing.T) {
	cfg := &Config{Name: "x", APIKey: "k", DefaultModel: "deepseek-chat"}
	p, _ := New(cfg, nil, nil)
	got := p.EstimateTokens([]*llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.BlockText, Text: "hello"}}},
	})
	if got <= 0 {
		t.Errorf("EstimateTokens = %d, want > 0", got)
	}
}
