package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ---------- Load ----------

// TestLoad_Defaults: empty path + no config file in HOME → defaults stand.
// We isolate HOME to a temp dir so the developer's real ~/.mini-agent
// can't leak into the test result.
func TestLoad_Defaults(t *testing.T) {
	withIsolatedHome(t)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"agent.max_steps", cfg.Agent.MaxSteps, 50},
		{"agent.tool_retry_max", cfg.Agent.ToolRetryMax, 3},
		{"context.compactor", cfg.Context.Compactor, "summarize"},
		{"context.trigger_ratio", cfg.Context.TriggerRatio, 0.8},
		{"permission.mode", cfg.Permission.Mode, ModeDefault},
		{"web.port", cfg.Web.Port, 7777},
		{"llm.thinking_effort", cfg.LLM.ThinkingEffort, "medium"},
		{"llm.network.retry_max", cfg.LLM.Network.RetryMax, 3},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestLoad_FileNotFound_Explicit: --config pointing at a missing file
// must error (vs. the implicit lookup which silently uses defaults).
func TestLoad_FileNotFound_Explicit(t *testing.T) {
	_, err := Load("/no/such/file.yaml")
	if err == nil {
		t.Fatal("expected error for missing explicit config path")
	}
	if !strings.Contains(err.Error(), "/no/such/file.yaml") {
		t.Errorf("error should mention the offending path: %v", err)
	}
}

// TestLoad_FileOverridesDefaults: a YAML file lays in fewer fields than
// the schema; the unspecified fields stay at default.
func TestLoad_FileOverridesDefaults(t *testing.T) {
	withIsolatedHome(t)

	yamlBody := `
llm:
  active_model: "openai:gpt-4o"
  providers:
    openai_compat:
      - name: "deepseek"
        base_url: "https://api.deepseek.com/v1"
        api_key: "sk-real-secret"
        default_model: "deepseek-chat"
agent:
  max_steps: 99
permission:
  mode: plan
`
	path := writeTemp(t, "config.yaml", yamlBody)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.LLM.ActiveModel != "openai:gpt-4o" {
		t.Errorf("active_model: got %q", cfg.LLM.ActiveModel)
	}
	if cfg.Agent.MaxSteps != 99 {
		t.Errorf("max_steps: got %d, want 99", cfg.Agent.MaxSteps)
	}
	if cfg.Permission.Mode != ModePlan {
		t.Errorf("permission.mode: got %q, want plan", cfg.Permission.Mode)
	}
	// Untouched defaults survive.
	if cfg.Agent.ToolRetryMax != 3 {
		t.Errorf("tool_retry_max default lost: got %d", cfg.Agent.ToolRetryMax)
	}
	if len(cfg.LLM.Providers.OpenAICompat) != 1 {
		t.Fatalf("expected 1 openai_compat provider, got %d", len(cfg.LLM.Providers.OpenAICompat))
	}
	if cfg.LLM.Providers.OpenAICompat[0].APIKey != "sk-real-secret" {
		t.Errorf("api_key not loaded as-is: got %q", cfg.LLM.Providers.OpenAICompat[0].APIKey)
	}
}

// TestLoad_PathExpansion: ~/foo paths resolve to the real home.
func TestLoad_PathExpansion(t *testing.T) {
	home := withIsolatedHome(t)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wantDB := filepath.Join(home, ".mini-agent", "data.db")
	if cfg.Storage.DatabasePath != wantDB {
		t.Errorf("storage.database_path: got %q, want %q", cfg.Storage.DatabasePath, wantDB)
	}
	wantRules := filepath.Join(home, ".mini-agent", "permissions.yaml")
	if cfg.Permission.RulesFile != wantRules {
		t.Errorf("permission.rules_file: got %q, want %q", cfg.Permission.RulesFile, wantRules)
	}
}

// ---------- ResolveMode ----------

func TestResolveMode_None(t *testing.T) {
	m, err := ResolveMode(false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if m != nil {
		t.Errorf("expected nil, got %q", *m)
	}
}

func TestResolveMode_Single(t *testing.T) {
	cases := []struct {
		yes, ae, plan bool
		want          PermissionMode
	}{
		{true, false, false, ModeYes},
		{false, true, false, ModeAutoEdit},
		{false, false, true, ModePlan},
	}
	for _, c := range cases {
		m, err := ResolveMode(c.yes, c.ae, c.plan)
		if err != nil {
			t.Fatalf("yes=%v ae=%v plan=%v: %v", c.yes, c.ae, c.plan, err)
		}
		if m == nil || *m != c.want {
			t.Errorf("yes=%v ae=%v plan=%v: got %v, want %v", c.yes, c.ae, c.plan, m, c.want)
		}
	}
}

func TestResolveMode_Conflict(t *testing.T) {
	cases := [][3]bool{
		{true, true, false},
		{true, false, true},
		{false, true, true},
		{true, true, true},
	}
	for _, c := range cases {
		_, err := ResolveMode(c[0], c[1], c[2])
		if err == nil {
			t.Errorf("yes=%v ae=%v plan=%v: expected conflict error", c[0], c[1], c[2])
		}
	}
}

// ---------- ParseModelRef ----------

func TestParseModelRef(t *testing.T) {
	cases := []struct {
		in           string
		wantProvider string
		wantModel    string
	}{
		{"deepseek:deepseek-reasoner", "deepseek", "deepseek-reasoner"},
		{"openai:gpt-4o", "openai", "gpt-4o"},
		{"gpt-4o", "", "gpt-4o"},
		{"", "", ""},
		{"  deepseek:r1  ", "deepseek", "r1"},
	}
	for _, c := range cases {
		p, m := ParseModelRef(c.in)
		if p != c.wantProvider || m != c.wantModel {
			t.Errorf("ParseModelRef(%q) = (%q,%q), want (%q,%q)",
				c.in, p, m, c.wantProvider, c.wantModel)
		}
	}
}

// ---------- ApplyFlags ----------

func TestApplyFlags_OverrideModel(t *testing.T) {
	withIsolatedHome(t)
	base, _ := Load("")

	model := "openai:gpt-4o"
	cfg, err := ApplyFlags(base, &FlagOverrides{Model: &model})
	if err != nil {
		t.Fatalf("ApplyFlags: %v", err)
	}
	if cfg.LLM.ActiveModel != "openai:gpt-4o" {
		t.Errorf("active_model not overridden: got %q", cfg.LLM.ActiveModel)
	}
}

func TestApplyFlags_OverrideMode(t *testing.T) {
	withIsolatedHome(t)
	base, _ := Load("")

	mode := ModePlan
	cfg, err := ApplyFlags(base, &FlagOverrides{Mode: &mode})
	if err != nil {
		t.Fatalf("ApplyFlags: %v", err)
	}
	if cfg.Permission.Mode != ModePlan {
		t.Errorf("mode not overridden: got %q", cfg.Permission.Mode)
	}
}

func TestApplyFlags_NilOverrides(t *testing.T) {
	withIsolatedHome(t)
	base, _ := Load("")

	cfg, err := ApplyFlags(base, nil)
	if err != nil {
		t.Fatalf("ApplyFlags(nil): %v", err)
	}
	if cfg.LLM.ActiveModel == "" {
		t.Errorf("default active_model should survive nil overrides")
	}
}

func TestApplyFlags_ValidateInvalidMode(t *testing.T) {
	withIsolatedHome(t)
	base, _ := Load("")
	base.Permission.Mode = "bogus"
	_, err := ApplyFlags(base, nil)
	if err == nil {
		t.Fatal("expected validation error for unknown permission.mode")
	}
}

func TestApplyFlags_ValidateRatios(t *testing.T) {
	withIsolatedHome(t)
	base, _ := Load("")
	base.Context.TargetRatio = 0.9
	base.Context.TriggerRatio = 0.8 // target >= trigger
	_, err := ApplyFlags(base, nil)
	if err == nil {
		t.Fatal("expected validation error when target_ratio >= trigger_ratio")
	}
}

// ---------- String / Mask ----------

func TestString_MasksAPIKeys(t *testing.T) {
	withIsolatedHome(t)
	yamlBody := `
llm:
  providers:
    openai_compat:
      - name: "deepseek"
        api_key: "sk-real-secret"
    anthropic:
      api_key: "sk-ant-real"
    gemini:
      api_key: "gem-real"
`
	cfg, err := Load(writeTemp(t, "config.yaml", yamlBody))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	dump := cfg.String()
	for _, leak := range []string{"sk-real-secret", "sk-ant-real", "gem-real"} {
		if strings.Contains(dump, leak) {
			t.Errorf("String() leaked %q in:\n%s", leak, dump)
		}
	}
	if !strings.Contains(dump, maskedSecret) {
		t.Errorf("String() should contain mask placeholder %q; got:\n%s", maskedSecret, dump)
	}

	// Original cfg untouched (we shouldn't have mutated provider entries).
	if cfg.LLM.Providers.OpenAICompat[0].APIKey != "sk-real-secret" {
		t.Errorf("String() mutated source config: got APIKey=%q",
			cfg.LLM.Providers.OpenAICompat[0].APIKey)
	}
}

func TestString_NilReceiver(t *testing.T) {
	var c *Config
	if got := c.String(); got != "(nil config)" {
		t.Errorf("nil receiver: got %q", got)
	}
}

// ---------- helpers ----------

// withIsolatedHome points os.UserHomeDir() at a temp dir for the duration
// of the test. Returns the temp dir path so the test can build expected
// paths off it.
func withIsolatedHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	switch runtime.GOOS {
	case "windows":
		t.Setenv("USERPROFILE", tmp)
	default:
		t.Setenv("HOME", tmp)
	}
	return tmp
}

// writeTemp drops `body` into <t.TempDir()>/<name> and returns the
// absolute path. Used to feed Load() a controlled YAML file.
func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("writeTemp: %v", err)
	}
	return p
}
