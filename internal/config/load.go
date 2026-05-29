package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// DefaultConfigPath returns ~/.mini-agent/config.yaml resolved against
// the current user's home directory. Never panics; on lookup failure it
// returns a relative path so subsequent ReadInConfig produces a clear
// "not found" instead of a runtime crash.
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".mini-agent/config.yaml"
	}
	return filepath.Join(home, ".mini-agent", "config.yaml")
}

// Load reads config from `path` (or the default location when empty),
// merges with built-in defaults, and returns a fully-validated Config.
//
// Behavior matrix (D25 + §7.1.1):
//   - path != ""  → must exist and parse cleanly; otherwise error.
//   - path == ""  → look up ~/.mini-agent/config.yaml; missing file is
//                   NOT an error (defaults stand alone).
//   - parse error → returned verbatim (callers print to stderr).
//
// Returns a Config value to honor the public-interface-takes-values
// part of D31; internal helpers operate on *Config.
func Load(path string) (Config, error) {
	v := viper.New()
	v.SetConfigType("yaml")
	setDefaults(v)

	pathProvided := path != ""
	if pathProvided {
		v.SetConfigFile(path)
	} else {
		// Look up the canonical location only; we don't search PATHs.
		home, _ := os.UserHomeDir()
		if home != "" {
			v.AddConfigPath(filepath.Join(home, ".mini-agent"))
		}
		v.SetConfigName("config")
	}

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return Config{}, fmt.Errorf("config: read %s: %w", v.ConfigFileUsed(), err)
		}
		// Implicit lookup miss → defaults only. An explicitly given
		// --config that doesn't exist still surfaces an error from
		// SetConfigFile + ReadInConfig (os.PathError, not NotFoundError).
		if pathProvided {
			return Config{}, fmt.Errorf("config: file %s not found", path)
		}
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return Config{}, fmt.Errorf("config: decode: %w", err)
	}

	expandPaths(cfg)
	return *cfg, nil
}

// MustLoad is a thin wrapper for tests / call sites that prefer to
// terminate early; not used by main.go (which surfaces the error to
// the user via stderr).
func MustLoad(path string) Config {
	c, err := Load(path)
	if err != nil {
		panic(err)
	}
	return c
}

// setDefaults mirrors the YAML defaults documented in §7.1.2. Every
// field listed there must appear here; missing defaults are a contract
// bug — callers may rely on e.g. Network.RetryMax > 0.
func setDefaults(v *viper.Viper) {
	// LLM
	v.SetDefault("llm.active_model", "deepseek:deepseek-reasoner")
	v.SetDefault("llm.enable_thinking", true)
	v.SetDefault("llm.thinking_effort", "medium")
	v.SetDefault("llm.network.request_timeout", 120*time.Second)
	v.SetDefault("llm.network.total_timeout", 300*time.Second)
	v.SetDefault("llm.network.retry_max", 3)
	v.SetDefault("llm.network.retry_backoff_base", time.Second)
	v.SetDefault("llm.network.retry_jitter", 0.2)

	// Agent
	v.SetDefault("agent.max_steps", 50)
	v.SetDefault("agent.tool_retry_max", 3)
	v.SetDefault("agent.sub_agent_depth_max", 1)

	// Context
	v.SetDefault("context.compactor", "summarize")
	v.SetDefault("context.trigger_ratio", 0.8)
	v.SetDefault("context.target_ratio", 0.5)
	v.SetDefault("context.keep_recent", 5)

	// Permission
	v.SetDefault("permission.mode", string(ModeDefault))
	v.SetDefault("permission.rules_file", "~/.mini-agent/permissions.yaml")

	// Storage
	v.SetDefault("storage.database_path", "~/.mini-agent/data.db")

	// Log
	v.SetDefault("log.path", "~/.mini-agent/logs/mini-agent.log")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	v.SetDefault("log.rotate.max_size_mb", 100)
	v.SetDefault("log.rotate.max_backups", 7)
	v.SetDefault("log.rotate.max_age_days", 7)
	v.SetDefault("log.rotate.compress", true)

	// Web
	v.SetDefault("web.enabled", false)
	v.SetDefault("web.host", "127.0.0.1")
	v.SetDefault("web.port", 7777)

	// AGENTS.md
	v.SetDefault("agentsmd.global_path", "~/.mini-agent/AGENTS.md")
	v.SetDefault("agentsmd.project_lookup", true)

	// Skills
	v.SetDefault("skills.user_dir", "~/.mini-agent/skills")
	v.SetDefault("skills.project_dir", ".mini-agent/skills")

	// UI
	v.SetDefault("ui.show_thinking", false)
	v.SetDefault("ui.show_hidden", false)
	v.SetDefault("ui.show_system", false)
	v.SetDefault("ui.show_archived", false)

	// MCP
	v.SetDefault("mcp.enabled", false)
}

// expandPaths normalizes every user-provided path so downstream code
// can pass them straight to os.* without worrying about ~ or $HOME.
// Path fields are owned by exactly one place each — keep this list in
// sync with `Config` whenever a new path-bearing field is added.
func expandPaths(c *Config) {
	c.Permission.RulesFile = expandHome(c.Permission.RulesFile)
	c.Storage.DatabasePath = expandHome(c.Storage.DatabasePath)
	c.Log.Path = expandHome(c.Log.Path)
	c.AgentsMD.GlobalPath = expandHome(c.AgentsMD.GlobalPath)
	c.Skills.UserDir = expandHome(c.Skills.UserDir)
	// Skills.ProjectDir is intentionally left relative — it's resolved
	// against cwd at runtime, not at config load.
}

// expandHome resolves a leading `~` or `~/` to the current user's home
// directory. We keep this minimal (no $HOME parsing, no ${...}) — full
// variable expansion belongs to the permission rules layer.
func expandHome(p string) string {
	if p == "" {
		return p
	}
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
