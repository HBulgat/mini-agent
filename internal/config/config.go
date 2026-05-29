package config

import "time"

// Config is the in-memory representation of ~/.mini-agent/config.yaml,
// keyed exactly as in docs/system-design/07-config-and-rules.md §7.1.2.
//
// Conventions (D26 + D31):
//   - Sub-config structs use value types (small, copied freely).
//   - Provider list / per-provider blocks use pointer slices/pointers
//     because they're optional and may be nil (gemini in P0) — pointer
//     also keeps mutation site explicit when masking secrets.
//   - api_key fields MUST be filtered by Config.String() before printing.
type Config struct {
	LLM        LLMCfg        `mapstructure:"llm"`
	Agent      AgentCfg      `mapstructure:"agent"`
	Context    ContextCfg    `mapstructure:"context"`
	Permission PermissionCfg `mapstructure:"permission"`
	Storage    StorageCfg    `mapstructure:"storage"`
	Log        LogCfg        `mapstructure:"log"`
	Web        WebCfg        `mapstructure:"web"`
	AgentsMD   AgentsMDCfg   `mapstructure:"agentsmd"`
	Skills     SkillsCfg     `mapstructure:"skills"`
	UI         UICfg         `mapstructure:"ui"`
	MCP        MCPCfg        `mapstructure:"mcp"`
}

// ---------- LLM (R5) ----------

type LLMCfg struct {
	ActiveModel      string                   `mapstructure:"active_model"`
	Providers        ProvidersCfg             `mapstructure:"providers"`
	PricingOverrides map[string]*ModelPricing `mapstructure:"pricing_overrides"`
	EnableThinking   bool                     `mapstructure:"enable_thinking"`
	ThinkingEffort   string                   `mapstructure:"thinking_effort"`
	Network          NetworkCfg               `mapstructure:"network"`
}

type ProvidersCfg struct {
	OpenAICompat []*OpenAICompatCfg `mapstructure:"openai_compat"`
	Anthropic    *AnthropicCfg      `mapstructure:"anthropic"`
	Gemini       *GeminiCfg         `mapstructure:"gemini"`
}

type OpenAICompatCfg struct {
	Name          string `mapstructure:"name"`
	BaseURL       string `mapstructure:"base_url"`
	APIKey        string `mapstructure:"api_key"`
	DefaultModel  string `mapstructure:"default_model"`
	ForceThinking bool   `mapstructure:"force_thinking"`
}

type AnthropicCfg struct {
	APIKey        string `mapstructure:"api_key"`
	DefaultModel  string `mapstructure:"default_model"`
	ForceThinking bool   `mapstructure:"force_thinking"`
}

type GeminiCfg struct {
	APIKey        string `mapstructure:"api_key"`
	DefaultModel  string `mapstructure:"default_model"`
	ForceThinking bool   `mapstructure:"force_thinking"`
}

type ModelPricing struct {
	InputPerMTok         float64 `mapstructure:"input_per_mtok"`
	OutputPerMTok        float64 `mapstructure:"output_per_mtok"`
	ReasoningPerMTok     float64 `mapstructure:"reasoning_per_mtok"`
	CachedInputPerMTok   float64 `mapstructure:"cached_input_per_mtok"`
	CacheCreationPerMTok float64 `mapstructure:"cache_creation_per_mtok"`
	CacheReadPerMTok     float64 `mapstructure:"cache_read_per_mtok"`
}

type NetworkCfg struct {
	RequestTimeout   time.Duration `mapstructure:"request_timeout"`
	TotalTimeout     time.Duration `mapstructure:"total_timeout"`
	RetryMax         int           `mapstructure:"retry_max"`
	RetryBackoffBase time.Duration `mapstructure:"retry_backoff_base"`
	RetryJitter      float64       `mapstructure:"retry_jitter"`
}

// ---------- Agent / Context / Permission ----------

type AgentCfg struct {
	MaxSteps         int `mapstructure:"max_steps"`
	ToolRetryMax     int `mapstructure:"tool_retry_max"`
	SubAgentDepthMax int `mapstructure:"sub_agent_depth_max"`
}

type ContextCfg struct {
	Compactor    string  `mapstructure:"compactor"`
	TriggerRatio float64 `mapstructure:"trigger_ratio"`
	TargetRatio  float64 `mapstructure:"target_ratio"`
	KeepRecent   int     `mapstructure:"keep_recent"`
}

// PermissionMode is the typed enum for permission.mode.
// We deliberately keep it a string so YAML serialization round-trips
// without custom (Un)Marshalers.
type PermissionMode string

const (
	ModeDefault  PermissionMode = "default"
	ModeAutoEdit PermissionMode = "auto-edit"
	ModeYes      PermissionMode = "yes"
	ModePlan     PermissionMode = "plan"
)

type PermissionCfg struct {
	Mode      PermissionMode `mapstructure:"mode"`
	RulesFile string         `mapstructure:"rules_file"`
}

// ---------- Storage / Log / Web ----------

type StorageCfg struct {
	DatabasePath string `mapstructure:"database_path"`
}

type LogCfg struct {
	Path   string       `mapstructure:"path"`
	Level  string       `mapstructure:"level"`
	Format string       `mapstructure:"format"`
	Rotate LogRotateCfg `mapstructure:"rotate"`
}

type LogRotateCfg struct {
	MaxSizeMB  int  `mapstructure:"max_size_mb"`
	MaxBackups int  `mapstructure:"max_backups"`
	MaxAgeDays int  `mapstructure:"max_age_days"`
	Compress   bool `mapstructure:"compress"`
}

type WebCfg struct {
	Enabled bool   `mapstructure:"enabled"`
	Host    string `mapstructure:"host"`
	Port    int    `mapstructure:"port"`
}

// ---------- AGENTS.md / Skills / UI / MCP ----------

type AgentsMDCfg struct {
	GlobalPath    string `mapstructure:"global_path"`
	ProjectLookup bool   `mapstructure:"project_lookup"`
}

type SkillsCfg struct {
	UserDir    string `mapstructure:"user_dir"`
	ProjectDir string `mapstructure:"project_dir"`
}

type UICfg struct {
	ShowThinking bool `mapstructure:"show_thinking"`
	ShowHidden   bool `mapstructure:"show_hidden"`
	ShowSystem   bool `mapstructure:"show_system"`
	ShowArchived bool `mapstructure:"show_archived"`
}

type MCPCfg struct {
	Enabled bool          `mapstructure:"enabled"`
	Servers []*MCPServer  `mapstructure:"servers"`
}

// MCPServer is a placeholder shape; full schema lands in R13.
type MCPServer struct {
	Name string `mapstructure:"name"`
	URL  string `mapstructure:"url"`
}
