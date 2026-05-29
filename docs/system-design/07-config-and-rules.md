# 7. 配置、AGENTS.md 与权限规则（R4 + R5 修订）

> Status: ✅ R4 已锁定（2026-05-24）+ R5 修订 llm 段（2026-05-24）
> 范围：配置文件字段表（viper）、AGENTS.md 查找与合并、白黑名单规则文件、硬黑名单结构
> 关联：实现 [`05-core-abstractions.md`](05-core-abstractions.md) §5.5 (`permission`) 与 §5.8 (`session`) 中的配置入口；为 R6 (Agent prompt 拼接) 提供 AGENTS.md 加载契约
>
> **R5 修订点**（2026-05-24）：原 `provider:` 段重构为 `llm:` 段以支持多 Provider 实例 + 多 base_url + pricing_overrides + network 子段。详见 §7.1.2 与 [`08-llm-providers.md`](08-llm-providers.md) §8.11。

---

## 7.1 配置加载

### 7.1.1 加载优先级

由低到高（高的覆盖低的）：

```
1. 代码内置默认值（viper.SetDefault）
2. 配置文件（默认 ~/.mini-agent/config.yaml；--config 可指定其它路径）
3. CLI 启动参数（--model / --cwd / --yes 等）
```

**不读环境变量**作为正式配置源——避免与配置文件的歧义来源。

### 7.1.2 完整字段表

```yaml
# ~/.mini-agent/config.yaml
# 所有字段均有默认值，未列出的字段使用默认。

# ==================== LLM（R5 重构） ====================
llm:
  active_model: "deepseek:deepseek-reasoner"   # provider:model 格式（推荐显式前缀避免歧义）
  providers:
    # OpenAI 兼容子包：可注册多个实例（每个 base_url 一个）
    openai_compat:
      - name: "deepseek"
        base_url: "https://api.deepseek.com/v1"
        api_key: "sk-..."                       # 敏感字段：屏蔽
        default_model: "deepseek-reasoner"
        force_thinking: false                   # self-hosted 思考模型时手动开
      - name: "openai"
        base_url: "https://api.openai.com/v1"
        api_key: "sk-..."
        default_model: "gpt-4o"
        force_thinking: false
    anthropic:
      api_key: "sk-ant-..."                     # 敏感字段
      default_model: "claude-sonnet-4-5"
      force_thinking: false
    gemini:                                     # P1，P0 阶段可省略
      api_key: "..."
      default_model: "gemini-2.5-flash"
      force_thinking: false

  # 单价表覆盖：每 model 的字段全部以 USD per million tokens 为单位
  # 缺省走 internal/llm/<provider>/pricing.go 内置表
  pricing_overrides:
    "deepseek-reasoner":
      input_per_mtok: 0.55
      output_per_mtok: 2.19
      reasoning_per_mtok: 2.19
      cached_input_per_mtok: 0.14
    # 其它 model 不写时走内置默认

  # 全局思考开关与 effort（每次 LLM 请求继承；未来 R9 的 /thinking 命令可覆盖）
  enable_thinking: true
  thinking_effort: "medium"                     # "" | "low" | "medium" | "high"

  # 网络层（共享，所有 Provider 子包遵守）
  network:
    request_timeout: 120s                       # 单次请求超时（含等待 + 读流总时长）
    total_timeout: 300s                         # 含重试的总超时
    retry_max: 3
    retry_backoff_base: 1s                      # 指数退避基数：1s/2s/4s
    retry_jitter: 0.2                           # ±20%

# ==================== Agent ====================
agent:
  max_steps: 50                    # 单次任务最大循环步数
  tool_retry_max: 3                # 单个工具失败重试上限
  sub_agent_depth_max: 1           # 子 agent 嵌套深度上限

# ==================== Context（压缩） ====================
context:
  compactor: summarize             # summarize | sliding-window | hierarchical
  trigger_ratio: 0.8               # currentTokens / capacity 超过此值触发
  target_ratio: 0.5                # 压缩目标占容量比例
  keep_recent: 5                   # 必保留最近 N 轮 user/assistant 原文

# ==================== Permission ====================
permission:
  mode: default                    # default | auto-edit | yes | plan
  rules_file: ~/.mini-agent/permissions.yaml

# ==================== Storage ====================
storage:
  database_path: ~/.mini-agent/data.db

# ==================== Log ====================
log:
  path: ~/.mini-agent/logs/mini-agent.log
  level: info                      # debug | info | warn | error
  format: json                     # json | text；默认 JSON Lines
  rotate:
    max_size_mb: 100
    max_backups: 7
    max_age_days: 7
    compress: true

# ==================== Web ====================
web:
  enabled: false                   # mini-agent serve 启动时被覆盖
  host: 127.0.0.1
  port: 7777

# ==================== AGENTS.md ====================
agentsmd:
  global_path: ~/.mini-agent/AGENTS.md
  project_lookup: true             # 是否在 cwd 寻找 ./AGENTS.md（不向上递归）

# ==================== Skills ====================
skills:
  user_dir: ~/.mini-agent/skills
  project_dir: .mini-agent/skills  # 相对 cwd 的项目级 skill 目录

# ==================== UI（CLI 启动时的默认显示开关） ====================
ui:
  show_thinking: false             # 默认是否显示模型思考
  show_hidden: false               # 默认是否显示 UserVisibility=hidden
  show_system: false               # 默认是否显示 UserVisibility=system
  show_archived: false             # 默认是否显示 Visibility=archived

# ==================== MCP（P3） ====================
mcp:
  enabled: false
  servers: []
```

### 7.1.3 字段总览

| 顶层 key | 字段数 | 关键约束 |
|---|---|---|
| `llm` | 复合（多 Provider 实例）| 每个 provider 的 `api_key` 敏感（屏蔽）；`active_model` 用 `provider:model` 格式 |
| `agent` | 3 | 与 `agent.Config` 一一对应 |
| `context` | 4 | 与 `agent.CompactionConfig` 一一对应 |
| `permission` | 2 | `mode` 可被 CLI flag 覆盖 |
| `storage` | 1 | 路径 `~` 自动展开 |
| `log` | 6 | rotate 子段对接 lumberjack |
| `web` | 3 | `enabled` 由 `serve` 子命令运行时设为 true |
| `agentsmd` | 2 | 见 §7.2 |
| `skills` | 2 | 与 `skill.Config` 对应 |
| `ui` | 4 | 启动时影响 CLI 默认显示开关 |
| `mcp` | 2 | P3 |

### 7.1.4 viper 加载实现

> 遵循 D31 指针约定：受 receiver 用指针；接口签名（`Load`）返回值仍为 `Config` 值类型作为公开契约，但内部 helper 在 `*Config` 上操作避免拷贝。

```go
// internal/config/config.go
package config

type Config struct {
    LLM        LLMCfg         `mapstructure:"llm"`         // R5 重构
    Agent      AgentCfg
    Context    ContextCfg     `mapstructure:"context"`
    Permission PermissionCfg
    Storage    StorageCfg
    Log        LogCfg
    Web        WebCfg
    AgentsMD   AgentsMDCfg    `mapstructure:"agentsmd"`
    Skills     SkillsCfg
    UI         UICfg          `mapstructure:"ui"`
    MCP        MCPCfg
}

// LLMCfg R5 新增
type LLMCfg struct {
    ActiveModel       string                          `mapstructure:"active_model"`
    Providers         ProvidersCfg
    PricingOverrides  map[string]*ModelPricing        `mapstructure:"pricing_overrides"`
    EnableThinking    bool                            `mapstructure:"enable_thinking"`
    ThinkingEffort    string                          `mapstructure:"thinking_effort"`
    Network           NetworkCfg
}

type ProvidersCfg struct {
    OpenAICompat []*OpenAICompatCfg `mapstructure:"openai_compat"`
    Anthropic    *AnthropicCfg
    Gemini       *GeminiCfg
}

type OpenAICompatCfg struct {
    Name          string
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
    InputPerMTok          float64 `mapstructure:"input_per_mtok"`
    OutputPerMTok         float64 `mapstructure:"output_per_mtok"`
    ReasoningPerMTok      float64 `mapstructure:"reasoning_per_mtok"`
    CachedInputPerMTok    float64 `mapstructure:"cached_input_per_mtok"`
    CacheCreationPerMTok  float64 `mapstructure:"cache_creation_per_mtok"`
    CacheReadPerMTok      float64 `mapstructure:"cache_read_per_mtok"`
}

type NetworkCfg struct {
    RequestTimeout    time.Duration `mapstructure:"request_timeout"`
    TotalTimeout      time.Duration `mapstructure:"total_timeout"`
    RetryMax          int           `mapstructure:"retry_max"`
    RetryBackoffBase  time.Duration `mapstructure:"retry_backoff_base"`
    RetryJitter       float64       `mapstructure:"retry_jitter"`
}

// Load 公开契约：接受文件路径，返回 Config 值
// 内部全部用 *Config 操作避免大结构体拷贝
func Load(path string) (Config, error) {
    v := viper.New()
    v.SetConfigType("yaml")
    setDefaults(v)

    if path != "" {
        v.SetConfigFile(path)
    } else {
        v.AddConfigPath(homeDir() + "/.mini-agent")
        v.SetConfigName("config")
    }

    if err := v.ReadInConfig(); err != nil {
        var notFound viper.ConfigFileNotFoundError
        if !errors.As(err, &notFound) {
            return Config{}, err
        }
    }

    cfg := &Config{}
    if err := v.Unmarshal(cfg); err != nil { return Config{}, err }
    expandPaths(cfg)
    return *cfg, nil
}

// String 返回脱敏后的 yaml 文本（供 Trace / 错误信息使用）
func (c *Config) String() string {
    masked := *c
    // 屏蔽所有 provider 的 api_key（R5 多 provider 场景）
    for i := range masked.LLM.Providers.OpenAICompat {
        if masked.LLM.Providers.OpenAICompat[i].APIKey != "" {
            cp := *masked.LLM.Providers.OpenAICompat[i]
            cp.APIKey = "****"
            masked.LLM.Providers.OpenAICompat[i] = &cp
        }
    }
    if masked.LLM.Providers.Anthropic != nil && masked.LLM.Providers.Anthropic.APIKey != "" {
        cp := *masked.LLM.Providers.Anthropic
        cp.APIKey = "****"
        masked.LLM.Providers.Anthropic = &cp
    }
    if masked.LLM.Providers.Gemini != nil && masked.LLM.Providers.Gemini.APIKey != "" {
        cp := *masked.LLM.Providers.Gemini
        cp.APIKey = "****"
        masked.LLM.Providers.Gemini = &cp
    }
    out, _ := yaml.Marshal(&masked)
    return string(out)
}

// expandPaths ~ 展开为绝对路径
func expandPaths(c *Config) { /* ... */ }
```

### 7.1.5 CLI flag 与配置的合并

| CLI flag | 覆盖字段 |
|---|---|
| `--config <path>` | 不进入 Config（决定从哪读） |
| `--model <name>` | `llm.active_model`（接受 `provider:model` 或裸 model） |
| `--cwd <path>` | （传给 SessionService.Cwd） |
| `--yes` / `--auto-edit` / `--plan` | `permission.mode` |
| `--no-trace` | （传给 ReplUIO） |
| `-p` / `--print` | （改变 cli/repl 模式） |
| `--session <id>` | （传给 SessionService.Resume） |
| `--thinking-effort <level>` | `llm.thinking_effort`（R5 新增）|

### 7.1.6 不支持的能力（明确）

- **配置热重载**：运行时修改 `config.yaml` 不会自动生效，必须重启
- **环境变量来源**：不读 `MINI_AGENT_*` 等 env 作为配置项
- **多文件配置合并**：不支持 conf.d 风格分片

---

## 7.2 AGENTS.md 查找与合并

### 7.2.1 查找路径

按**由低到高**优先级查找：

```
1. 全局：config.agentsmd.global_path  （默认 ~/.mini-agent/AGENTS.md）
2. 项目：<cwd>/AGENTS.md              （仅当 config.agentsmd.project_lookup=true 时）
```

**关键决定：不向上递归**——避免在 monorepo 中误命中祖先目录的 AGENTS.md。希望项目级生效则把 AGENTS.md 直接放在启动 cwd 下。

### 7.2.2 合并策略

两个文件都存在时，**按"全局在前 + 项目级在后"拼接**：

```
[全局 AGENTS.md 内容]

---

[项目级 AGENTS.md 内容]
```

约束：
- 全局在前，项目级在后（项目级"覆盖"靠模型语义理解，不做文本级 diff）
- 中间用 markdown `---` 分隔
- 整段文本被包装在一条 system 消息里注入；`UserVisibility=system`

### 7.2.3 加载时机

| 时机 | 行为 |
|---|---|
| 进程启动 | 读取一次，缓存在内存 |
| `/cd <path>` 切换工作目录 | 重新读取**项目级**（全局不变） |
| 文件被外部修改 | 不监听；重启或 `/cd` 触发重新加载 |

### 7.2.4 注入位置

`agent.prepareInitialHistory` 中（来自 `05-core-abstractions.md` §5.9.8）：

```
1. system: 内置系统 prompt（含 ReAct 引导）
2. system: skill 列表注入
3. system: AGENTS.md 合并文本   ← 本节
4. user: 当前 user 输入
```

### 7.2.5 文件容错

| 场景 | 行为 |
|---|---|
| 全局 / 项目级文件都不存在 | 跳过，无错误 |
| 文件存在但读取失败 | 记录 trace 警告，跳过 |
| 文件为空 | 跳过 |
| 文件大于 1 MB | 截断并记录 trace 警告 |

### 7.2.6 接口签名

```go
// internal/agentsmd/loader.go
package agentsmd

type Loader interface {
    // Load 按当前 cwd 与配置加载并合并 AGENTS.md 内容；返回合并后的文本（可能为空）
    Load(ctx context.Context, cwd string) (string, error)
}

type Config struct {
    GlobalPath    string  // 默认 ~/.mini-agent/AGENTS.md
    ProjectLookup bool
    MaxBytes      int64   // 单文件最大字节（默认 1 MB）
}

func New(cfg *Config) Loader   // D31：构造时接受指针避免大 struct 拷贝
```

---

## 7.3 白黑名单规则文件

### 7.3.1 文件位置

由 `config.permission.rules_file` 指定（默认 `~/.mini-agent/permissions.yaml`）。文件不存在时仅硬黑名单生效。

### 7.3.2 格式

```yaml
# ~/.mini-agent/permissions.yaml
rules:
  # ===== 命令级（仅对 bash 工具生效） =====
  - type: deny
    granularity: command
    pattern: "git push --force*"
    reason: "force push is forbidden"

  - type: allow
    granularity: command
    pattern: "ls *"
    reason: "ls always allowed"

  # ===== 路径级（对 read_file / write_file / edit_file / delete_file 生效） =====
  - type: deny
    granularity: path
    pattern: "/etc/**"
    reason: "system files are read-only"

  - type: allow
    granularity: path
    pattern: "${cwd}/**"
    reason: "project files always allowed"

  # ===== 工具级 =====
  - type: deny
    granularity: tool
    pattern: "web_search"
    reason: "no internet"
```

### 7.3.3 字段语义

| 字段 | 取值 | 说明 |
|---|---|---|
| `type` | `allow` / `deny` | 规则类型 |
| `granularity` | `command` / `path` / `tool` | 规则粒度 |
| `pattern` | 字符串 | 匹配模板（见 §7.3.4） |
| `reason` | 字符串（可选） | 用于 Trace 与拒绝消息 |

### 7.3.4 模板匹配语义

| 粒度 | 语义 | 示例 |
|---|---|---|
| `command` | 对 `bash` 工具的命令字符串做 **glob 匹配**（`*` 任意非空白；`**` 任意） | `git push --force*` 匹配 `git push --force origin main` |
| `path` | 对路径做 **doublestar glob 匹配**（`**` 跨多层目录） | `/etc/**` 匹配 `/etc/passwd`；`${cwd}/**` 限定项目内 |
| `tool` | 对工具名做**完全匹配** | `bash` 匹配 `bash`；不支持通配符 |

变量展开：

| 变量 | 展开为 |
|---|---|
| `${cwd}` | 当前工作目录绝对路径 |
| `${home}` | 用户家目录 |
| `~` | 同 `${home}` |

### 7.3.5 路径匹配判定流程

1. 把 `pattern` 中的变量展开（`${cwd}` / `~` 等）
2. 把待匹配的 `target_path` 转为绝对路径（`filepath.Abs`）
3. 用 doublestar glob 库匹配

不引入"项目内 / 项目外"隐式概念——通过 `pattern: "${cwd}/**"` 显式表达。

### 7.3.6 规则评估顺序

```
1. 硬黑名单（代码内置，不可被用户规则覆盖）
2. 用户黑名单（rules.deny 命中即拒）
3. 用户白名单（rules.allow 命中即允许，跳过模式判定）
4. 模式判定（plan/auto-edit/yes/默认）
5. 审批询问
```

关键：
- `allow` 优先级 **高于模式判定**——`--plan` 模式下，配置 `allow path "${cwd}/**"` 仍可让项目内写文件通过
- `allow` **不能覆盖硬黑名单**——硬黑名单是兜底防线

### 7.3.7 加载与解析

```go
// internal/permission/rules.go
type RuleSet struct {
    UserRules    []*UserRule       // D31：用指针切片便于规则数大时减少拷贝
    HardDenylist []*HardDenyRule
}

func LoadRules(rulesFile string) (*RuleSet, error) {
    rs := &RuleSet{HardDenylist: hardDenylist()}
    if rulesFile == "" { return rs, nil }
    data, err := os.ReadFile(rulesFile)
    if errors.Is(err, fs.ErrNotExist) { return rs, nil }
    if err != nil { return nil, err }

    var doc struct { Rules []*UserRule `yaml:"rules"` }
    if err := yaml.Unmarshal(data, &doc); err != nil { return nil, err }
    rs.UserRules = doc.Rules
    return rs, nil
}
```

### 7.3.8 Gate.CheckRulesOnly 实现

```go
func (g *gate) CheckRulesOnly(op Operation) Result {
    // 1. 硬黑名单
    for _, r := range g.rules.HardDenylist {
        if matchHardDeny(r, &op) {
            return Result{Decision: DecisionDenyHard, Reason: r.Reason}
        }
    }
    // 2. 用户规则（按文件顺序，首个命中决策）
    for _, r := range g.rules.UserRules {
        if matchUserRule(r, &op) {
            switch r.Type {
            case RuleDeny:
                return Result{Decision: DecisionDeny, Reason: r.Reason}
            case RuleAllow:
                return Result{Decision: DecisionAllow, Reason: r.Reason}
            }
        }
    }
    return Result{Decision: DecisionAllow, Reason: "no rule matched"}
}
```

---

## 7.4 硬黑名单结构（位置已定，规则集 R7 详定）

```go
// internal/permission/hard_denylist.go
package permission

// hardDenylist 代码内置的硬黑名单
// 不可被用户规则覆盖；即使 --yes 也拒绝
// 完整规则集 R7 详定，本轮只锁定**位置 + 结构**
func hardDenylist() []*HardDenyRule {
    return []*HardDenyRule{
        // bash 命令级（R7 完整清单）
        {ToolName: "bash", Pattern: "rm -rf /*",       Reason: "destroys root filesystem"},
        {ToolName: "bash", Pattern: "rm -rf ~*",       Reason: "destroys home directory"},
        {ToolName: "bash", Pattern: "rm -rf $HOME*",   Reason: "destroys home directory"},
        {ToolName: "bash", Pattern: ":(){ :|:& };:",   Reason: "fork bomb"},
        {ToolName: "bash", Pattern: "* > /etc/passwd", Reason: "modifies system credentials"},
        // ... R7 详定
    }
}
```

---

## 7.5 关键决定（D25–D31）

| 编号 | 决定 |
|---|---|
| **D25** | 配置加载三层优先级：默认值 < 配置文件 < CLI flag。**不读环境变量**作为正式配置源 |
| **D26** | 配置文件结构按子模块分组（11 个顶层 key 共约 37 字段）；敏感字段（api_key）在 `String()` / 日志输出时屏蔽 |
| **D27** | AGENTS.md 查找：全局 `~/.mini-agent/AGENTS.md` + 项目级 `<cwd>/AGENTS.md`（**不向上递归**）；`/cd` 后重新加载项目级；用 `---` 拼接为合并文本注入；UserVisibility=system |
| **D28** | 白黑名单规则文件位置可配置（默认 `~/.mini-agent/permissions.yaml`）；文件不存在视为无用户规则 |
| **D29** | 规则三种粒度（command / path / tool）+ 两种类型（allow / deny）；`allow` 优先级高于模式判定，但**不能覆盖硬黑名单** |
| **D30** | 规则评估顺序：硬黑名单 → 用户规则（按文件顺序首个命中）→ 模式判定 → 审批；`path` 粒度用 doublestar glob 匹配，支持 `${cwd}` / `${home}` / `~` 变量展开 |
| **D31** | Go 代码"指针优先，例外明确"风格：① 所有结构体方法 receiver 用 `*T`（除非真值类型）；② 大结构体在私有 helper、批处理切片场景用指针；③ 接口（公开契约）方法参数保留值类型；④ 高频调用 + 小事件结构（如 uio Sink 方法）保留值类型；⑤ 不对 slice/map/channel/func/interface/string/error 加 `*T` |

---

## 7.6 留待后续轮次

| 议题 | 归属 |
|---|---|
| 硬黑名单完整规则集 | R7 |
| `bash` 命令字符串解析（处理引号、转义、复合命令） | R7 |
| `RememberApproval` 等价性判定（参数完全一致 / prefix 匹配） | R7 |
| AGENTS.md 内容如何与系统 prompt 文本拼接（具体模板） | R6 |
| viper 启动失败时的错误提示文案 | 实现期 |
