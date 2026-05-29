package config

import (
	"fmt"
	"strings"
)

// FlagOverrides bundles every CLI flag that mutates Config (per §7.1.5).
// Fields are pointers so "unset on the command line" is distinguishable
// from "explicitly set to zero value" — required because viper has
// already produced a fully-populated Config we must not blindly
// overwrite.
//
// Note: --config is handled at Load() time and never enters this struct.
type FlagOverrides struct {
	Model    *string         // --model
	Mode     *PermissionMode // resolved from --yes / --auto-edit / --plan
	Cwd      *string         // --cwd  (consumed by SessionService, not Config)
	Thinking *string         // --thinking-effort
}

// ResolveMode collapses the three mutually-exclusive permission flags
// into a single PermissionMode pointer. Returns nil when none of them
// is set (defer to config-file value). Returns an error when more than
// one is set — the user must pick exactly one.
func ResolveMode(yes, autoEdit, plan bool) (*PermissionMode, error) {
	count := 0
	var m PermissionMode
	if yes {
		count++
		m = ModeYes
	}
	if autoEdit {
		count++
		m = ModeAutoEdit
	}
	if plan {
		count++
		m = ModePlan
	}
	switch count {
	case 0:
		return nil, nil
	case 1:
		return &m, nil
	default:
		return nil, fmt.Errorf(
			"--yes / --auto-edit / --plan are mutually exclusive; pick at most one")
	}
}

// ApplyFlags layers flag values on top of an already-loaded Config.
// Returns the patched Config (value, per D31 public-interface rule).
//
// Validation is split here from Load() because flags can resurrect a
// Config that would otherwise be invalid (e.g. a config file with no
// `llm.active_model` paired with `--model`).
func ApplyFlags(c Config, f *FlagOverrides) (Config, error) {
	if f == nil {
		return c, validate(&c)
	}

	if f.Model != nil && *f.Model != "" {
		c.LLM.ActiveModel = *f.Model
	}
	if f.Mode != nil {
		c.Permission.Mode = *f.Mode
	}
	if f.Thinking != nil && *f.Thinking != "" {
		c.LLM.ThinkingEffort = *f.Thinking
	}
	// Cwd is intentionally not stored on Config — it flows directly into
	// SessionService at startup. We accept the field here only to keep
	// the override surface consistent with §7.1.5.

	return c, validate(&c)
}

// ParseModelRef splits an "active_model" string into (provider, model).
//
//	"deepseek:deepseek-reasoner"  -> ("deepseek", "deepseek-reasoner")
//	"gpt-4o"                      -> ("",         "gpt-4o")
//
// Empty input returns ("", ""). Strings with multiple ":" keep only the
// first segment as provider — provider names never contain ":" by
// convention (D8 cobra prefix style).
func ParseModelRef(s string) (provider, model string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	idx := strings.Index(s, ":")
	if idx < 0 {
		return "", s
	}
	return s[:idx], s[idx+1:]
}

// validate enforces the few invariants we can check without contacting
// the network. The expensive checks (provider reachable, model exists)
// happen later when llm.Provider is constructed.
func validate(c *Config) error {
	switch c.Permission.Mode {
	case ModeDefault, ModeAutoEdit, ModeYes, ModePlan:
	case "":
		c.Permission.Mode = ModeDefault
	default:
		return fmt.Errorf("permission.mode must be one of default/auto-edit/yes/plan, got %q",
			c.Permission.Mode)
	}

	switch c.LLM.ThinkingEffort {
	case "", "low", "medium", "high":
	default:
		return fmt.Errorf(`llm.thinking_effort must be one of "" / low / medium / high, got %q`,
			c.LLM.ThinkingEffort)
	}

	if c.Agent.MaxSteps <= 0 {
		return fmt.Errorf("agent.max_steps must be > 0, got %d", c.Agent.MaxSteps)
	}
	if c.Agent.ToolRetryMax < 0 {
		return fmt.Errorf("agent.tool_retry_max must be >= 0, got %d", c.Agent.ToolRetryMax)
	}
	if c.Agent.SubAgentDepthMax < 0 {
		return fmt.Errorf("agent.sub_agent_depth_max must be >= 0, got %d", c.Agent.SubAgentDepthMax)
	}

	if c.Context.TriggerRatio <= 0 || c.Context.TriggerRatio > 1 {
		return fmt.Errorf("context.trigger_ratio must be in (0,1], got %v", c.Context.TriggerRatio)
	}
	if c.Context.TargetRatio <= 0 || c.Context.TargetRatio >= c.Context.TriggerRatio {
		return fmt.Errorf("context.target_ratio must be in (0, trigger_ratio), got %v", c.Context.TargetRatio)
	}

	if c.Web.Port <= 0 || c.Web.Port > 65535 {
		return fmt.Errorf("web.port must be in (0,65535], got %d", c.Web.Port)
	}

	return nil
}
