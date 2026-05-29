# 8. LLM Provider 适配（R5）

> Status: ✅ R5 已锁定（2026-05-24）
> 范围：OpenAI 兼容 / Anthropic / Gemini 三家 Provider 的协议适配、流式契约、function calling 适配、思考模式协议映射、usage / cost 计算、token 估算、网络层重试与超时
> 关联：实现 [`05-core-abstractions.md`](05-core-abstractions.md) §5.3 (`llm`) 中定义的 `Provider` 接口；与 [`06-session-storage.md`](06-session-storage.md) §6.9 Codec 章节配合
> 修订动作：本轮对 `05-core-abstractions.md` §5.3 的 `Request` / `Usage` / `StreamEvent` 做了字段扩充（已就地更新）；对 `06-session-storage.md` §6.9 Codec 接口做了实化；对 `07-config-and-rules.md` §7.1 `llm` 配置段做了重构

---

## 8.1 范围与优先级

| Provider | 阶段 | SDK | 主战场模型 |
|---|---|---|---|
| **OpenAI 兼容**（含 DeepSeek / Kimi / Qwen / 真 OpenAI / self-hosted） | **P0** | `github.com/openai/openai-go` | DeepSeek-V4-Pro（主力）/ deepseek-chat / deepseek-reasoner / gpt-4o / gpt-4.1 / o3 / o3-mini |
| **Anthropic** | **P0** | `github.com/anthropics/anthropic-sdk-go` | claude-sonnet-4-5 / claude-haiku-4-5 |
| **Gemini** | P1（Iter-4） | `google.golang.org/genai` | gemini-2.5-flash / gemini-2.5-pro |
| MCP（远程工具） | P3 | — | 不在 R5 范围（R13） |

**P0 双 Provider 的设计动机**：思考模式 + signature 校验 + Codec 双向转换是 R3 设计的"试金石"。Anthropic 的 thinking + signature 协议是最严格的，必须在 P0 跑通才能验证 canonical 抽象的正确性。

---

## 8.2 目录结构

```
internal/llm/
├── llm.go                    # 接口与类型（见 05-core-abstractions §5.3）
├── registry.go               # Provider 注册表（按 name 查询）
├── network/                  # 共享：重试 + 超时 + 退避
│   ├── retry.go              # 重试策略（5xx + 429 + 网络错误；指数退避 + jitter；Retry-After 优先）
│   └── timeout.go            # 超时上下文构造
├── tokenest/                 # 共享：token 估算工具
│   ├── tokenest.go           # 接口 + 默认实现
│   ├── tiktoken.go           # OpenAI 系（pkoukk/tiktoken-go 包装）
│   └── charratio.go          # 中英文字符比例近似（Anthropic / Gemini fallback）
├── openai/
│   ├── provider.go           # 实现 llm.Provider
│   ├── codec.go              # Canonical ↔ OpenAI 协议
│   ├── stream.go             # 流式响应解析
│   ├── pricing.go            # 内置：model → ModelInfo（Capabilities + 单价）
│   └── thinking.go           # o-series reasoning + DeepSeek reasoning_content 适配
├── anthropic/
│   ├── provider.go
│   ├── codec.go              # 含 thinking + signature + redacted_thinking 处理
│   ├── stream.go             # 含 content_block_start / content_block_delta / content_block_stop
│   ├── pricing.go            # 含 cache_creation / cache_read 单价
│   └── thinking.go
└── gemini/                   # P1
    ├── provider.go
    ├── codec.go
    ├── stream.go
    ├── pricing.go
    └── thinking.go
```

设计原则：
- 三家 provider 子包结构对称，便于横向对照
- `network/` 与 `tokenest/` 两个共享子包供三家复用
- 每个 provider 子包**自包含**：包括 codec、流式、pricing、thinking——新增 provider 时不动其它子包

---

## 8.3 Provider 接口（已定义，见 §5.3）—— R5 实化要点

```go
type Provider interface {
    Name() string
    Capabilities() Capabilities
    Stream(ctx context.Context, req Request) (<-chan StreamEvent, error)
}
```

R5 实化的隐性契约（写入文档便于实现期对照）：

| 契约 | 要求 |
|---|---|
| `Name()` | 启动时唯一；OpenAI 兼容多实例时取 config 中的 `name` 字段（如 `deepseek` / `openai-real` / `kimi`） |
| `Capabilities()` | 必须返回**当前激活模型**的能力（来自 `pricing.go` 内置表 + config 覆盖）；模型不在表中时退化默认值 `{ContextWindow:8192, MaxOutputTokens:4096, SupportsTools:true, SupportsStreaming:true, SupportsThinking:false}` + warn 日志 |
| `Stream()` | 必须使用 `network.WithRetry` + `network.WithTimeout` 包装；返回的 channel 在 ctx 取消时立即关闭；Provider 内部不吞 ctx 错误 |
| usage 缺失 | 由 Provider 子包 fallback（OpenAI 流式默认不带 usage——必须设置 `stream_options.include_usage=true`；Anthropic 流式带 message_delta.usage）；fallback 失败时返回 `Usage{}` 零值 + 写日志告警 + 不阻断主流程 |

---

## 8.4 R5 对 §5.3 接口的字段扩充

### 8.4.1 `Request` 新增 `ThinkingEffort`

```go
type Request struct {
    Messages       []Message
    Tools          []ToolSpec
    ToolChoice     ToolChoice
    Temperature    *float32
    MaxTokens      *int
    Stop           []string
    EnableThinking bool
    ThinkingEffort string  // R5 新增："" | "low" | "medium" | "high"
}
```

各 Provider 的映射：

| Provider | EnableThinking=false | EnableThinking=true + Effort="" | Effort="low" | Effort="medium" | Effort="high" |
|---|---|---|---|---|---|
| OpenAI o-series | 不发 `reasoning` 字段 | `reasoning.effort="medium"` | `reasoning.effort="low"` | `reasoning.effort="medium"` | `reasoning.effort="high"` |
| OpenAI 非 o-series | 不发 `reasoning` 字段 | 同左 | 同左 | 同左 | 同左 |
| DeepSeek `deepseek-reasoner` | 报 warn（模型本身决定）+ 正常请求 | 模型自带 | — | — | — |
| Anthropic | 不发 `thinking` 字段 | `thinking.budget_tokens=4096` | `budget_tokens=1024` | `budget_tokens=4096` | `budget_tokens=16000` |
| Gemini | `thinkingConfig.thinkingBudget=0` | `thinkingBudget=-1`（自动） | `thinkingBudget=512` | `thinkingBudget=4096` | `thinkingBudget=16000` |

### 8.4.2 `Usage` 扩展三个 cache 字段

```go
type Usage struct {
    PromptTokens        int
    CompletionTokens    int
    ReasoningTokens     int
    CachedPromptTokens  int  // R5 新增：OpenAI cached_input
    CacheCreationTokens int  // R5 新增：Anthropic cache_creation_input_tokens
    CacheReadTokens     int  // R5 新增：Anthropic cache_read_input_tokens
    TotalTokens         int
    CostUSD             float64
}
```

各 Provider 的映射：

| Canonical 字段 | OpenAI 字段 | Anthropic 字段 | Gemini 字段 |
|---|---|---|---|
| `PromptTokens` | `usage.prompt_tokens`（含 cached） | `usage.input_tokens`（不含 cache_creation/cache_read） | `usageMetadata.promptTokenCount` |
| `CompletionTokens` | `usage.completion_tokens`（含 reasoning） | `usage.output_tokens`（含 thinking） | `usageMetadata.candidatesTokenCount` |
| `ReasoningTokens` | `usage.completion_tokens_details.reasoning_tokens` | 由 Codec 单独提取（见 §8.7.2） | `usageMetadata.thoughtsTokenCount` |
| `CachedPromptTokens` | `usage.prompt_tokens_details.cached_tokens` | — | `cachedContentTokenCount` |
| `CacheCreationTokens` | — | `usage.cache_creation_input_tokens` | — |
| `CacheReadTokens` | — | `usage.cache_read_input_tokens` | — |

### 8.4.3 `StreamEvent` 新增 `StreamBlockBoundary`

```go
type StreamEventType int

const (
    StreamDelta         StreamEventType = iota
    StreamFinal
    StreamError
    StreamBlockBoundary  // R5 新增
)

type StreamEvent struct {
    Type     StreamEventType
    Delta    Delta
    Final    *FinalResponse
    Boundary *BlockBoundary  // R5 新增
    Err      error
}

type BlockBoundary struct {
    BlockType ContentBlockType  // text / thinking / redacted_thinking / tool_use / tool_result
    IsStart   bool              // true=start; false=stop
    Index     int               // 块在 message 中的序号
}
```

各 Provider 的合成方式：

| Provider | 合成方式 |
|---|---|
| Anthropic | 直接透传 `content_block_start` / `content_block_stop` 事件 |
| OpenAI / DeepSeek | Provider 子包**合成**：看到第一个 `delta.reasoning_content` 时合成 `(thinking, IsStart=true)`；看到第一个 `delta.content` 时合成前序 thinking 的 stop + text 的 start；流结束时合成所有未关闭块的 stop |
| Gemini | 解析 part 类型变化，类似 OpenAI 合成 |

UI 用法：CLI 与 Web UI 可订阅 `StreamBlockBoundary` 来精准渲染折叠面板（先弹一个空的"思考中…"框，再往里塞内容）。如果 UI 不订阅，行为与 R3 一致（看 `Delta.Thinking` / `Delta.Content` 切状态）。

---

## 8.5 流式契约统一翻译

### 8.5.1 双轨 tool_call 增量

R3 已规定 `Delta.ToolCallDelta` 是流式 tool call 的载体。R5 实化为**双轨**：

| 轨道 | 用途 | 内容 |
|---|---|---|
| **增量轨**（StreamDelta） | UI 流式动效 | `ToolCallDelta { Index, ID, Name, ArgsDiff }`，`ArgsDiff` 累加**原始 JSON 片段**（不解析）|
| **最终轨**（StreamFinal） | Loop 消费 | `FinalResponse.Message.Content` 中的 `BlockToolUse` 已解析为 `ToolInput map[string]any` |

Loop 永远只用最终轨；增量轨仅供 Sink 渲染。Provider 子包负责在内部累积完整 JSON，并在流结束时一次性 unmarshal。

### 8.5.2 OpenAI 流式 tool_call 翻译

OpenAI `delta.tool_calls[i]` 的字段到达顺序：
1. 第 1 chunk：`{index, id, type:"function", function:{name}}`
2. 后续 chunks：`{index, function:{arguments:"<JSON 片段>"}}`
3. 流结束（`finish_reason:"tool_calls"`）

Codec 行为：
- 每来一个 chunk 发一个 `StreamDelta { ToolCallDelta:{Index, ID, Name, ArgsDiff:<本 chunk 的 arguments 片段>} }`
- Provider 内部维护 `map[index]*partialToolCall { ID, Name, ArgsBuf bytes.Buffer }`
- 流结束时 unmarshal 每个 `ArgsBuf` 为 `map[string]any`，组装 `BlockToolUse`，发 `StreamFinal`

### 8.5.3 Anthropic 流式 tool_call 翻译

Anthropic 流式事件序列：
1. `message_start`
2. `content_block_start { index, content_block: { type:"tool_use", id, name, input:{} } }`
3. `content_block_delta { index, delta: { type:"input_json_delta", partial_json:"..." } }` × N
4. `content_block_stop { index }`
5. `message_delta { stop_reason, usage }`
6. `message_stop`

Codec 行为：
- `content_block_start` 发 `StreamBlockBoundary { BlockType:tool_use, IsStart:true, Index }`
- `input_json_delta` 发 `StreamDelta { ToolCallDelta:{ Index, ArgsDiff:partial_json } }`（ID/Name 在 start 已知，每次都填上方便 UI）
- `content_block_stop` 发 `StreamBlockBoundary { ..., IsStart:false }`
- `message_stop` 发 `StreamFinal`

### 8.5.4 Gemini 流式 tool_call 翻译

Gemini `functionCall` 在 part 中一次性给出完整 args（不分片），无字段累加。

Codec 行为：
- 收到 part 中的 functionCall，发 `StreamBlockBoundary { tool_use, IsStart:true }` → `StreamDelta { ToolCallDelta:{ ID:gen_uuid7, Name, ArgsDiff:json.Marshal(args) } }` → `StreamBlockBoundary { tool_use, IsStart:false }`，与其它 provider 视觉对齐
- Gemini 不带 tool_call_id：Codec 自动生成 `tooluse-{uuidv7}` 临时 ID（D45 规则）

### 8.5.5 流式断开处理

| 情况 | 行为 |
|---|---|
| ctx 取消 | channel 立即关闭；Loop 看到 channel close 后从 ctx.Err() 判断为中断；Provider 不发 StreamError |
| 网络中断 | Provider 发一个 `StreamError { Err }` 后关闭 channel；Loop 视为 `StopError`；**不重试**（避免重复扣费 + 已生成内容丢失） |
| Provider 返回 4xx | 在初始化阶段（`Stream(ctx, req) error` 返回值）就返回错误；不进 channel |
| Provider 返回 5xx / 429 / 网络错误 | 在初始化阶段由 `network.WithRetry` 重试；初始化成功后流中途出现的错误不重试 |

---

## 8.6 function calling 适配

### 8.6.1 Schema 字段名规范化

Canonical `ToolSpec.Schema` 用 **OpenAI 风格 JSON Schema**。各 Provider 的 rename：

| Provider | 字段 |
|---|---|
| OpenAI | 直接用：`tools: [{type:"function", function:{name, description, parameters}}]` |
| Anthropic | rename：`tools: [{name, description, input_schema}]`（`schema` → `input_schema`） |
| Gemini | rename：`tools: [{functionDeclarations: [{name, description, parameters}]}]` |

### 8.6.2 ToolChoice 映射

| Canonical | OpenAI | Anthropic | Gemini |
|---|---|---|---|
| `ToolChoiceAuto` | `tool_choice:"auto"` | `tool_choice:{type:"auto"}` | `mode:"AUTO"` |
| `ToolChoiceNone` | `tool_choice:"none"` | `tool_choice:{type:"none"}`（实际上 Anthropic 不显式发 tools 即可）| `mode:"NONE"` |
| `ToolChoiceRequired` | `tool_choice:"required"` | `tool_choice:{type:"any"}` | `mode:"ANY"` |
| `ToolChoiceSpecific` | `tool_choice:{type:"function", function:{name}}` | `tool_choice:{type:"tool", name}` | `mode:"ANY", allowedFunctionNames:[name]` |

### 8.6.3 工具结果回传

R3 已确认：**Canonical 永远用 `user + ToolResultBlock`**。各 Provider 的翻译：

| Provider | Canonical → Wire |
|---|---|
| OpenAI | `user + ToolResultBlock(ref=X, output=Y, is_error=E)` → `{role:"tool", tool_call_id:X, content:Y}`（独立成一条；OpenAI 不区分 is_error，由内容描述）|
| Anthropic | `{role:"user", content:[{type:"tool_result", tool_use_id:X, content:Y, is_error:E}]}`（与 Canonical 1:1） |
| Gemini | `{role:"user", parts:[{functionResponse:{name:N, response:{result:Y}}}]}`（注：Gemini 用 name 而非 id 关联——Codec 需从历史中根据 ToolUseID 查回 Name） |

### 8.6.4 缺 tool_use_id 容错

罕见但存在：模型未在 ToolUse 块给 ID。Codec 行为：
- 自动生成 `tooluse-{uuidv7-no-dash}` 作为临时 ID
- `slog.Warn("tool_use missing id; auto-generated", "provider", ..., "name", ..., "synthetic_id", ...)`
- 流程不被中断

---

## 8.7 思考模式（Thinking）协议适配

### 8.7.1 OpenAI / DeepSeek

| 项 | OpenAI o-series | DeepSeek `deepseek-reasoner` |
|---|---|---|
| 启用方式 | 请求体 `reasoning.effort` | 模型名本身决定（无请求字段）|
| 流式事件 | `delta.reasoning_content` | `delta.reasoning_content` |
| 思考 token 计数 | `usage.completion_tokens_details.reasoning_tokens` | `usage.reasoning_tokens` |
| 签名 | `reasoning.encrypted_content`（仅 ZDR 账户必须回传） | 无 |
| 加密思考 | 无显式概念 | 无 |

Codec 处理：
- 流式：`delta.reasoning_content` → `StreamDelta { Thinking: <text> }`；同时合成 `BlockBoundary` 事件（§8.4.3）
- 持久化：拼成完整后写为 `BlockThinking { Thinking: <text>, ThinkingSignature: <encrypted_content if any> }`
- 回传：把 `ContentBlock.ThinkingSignature` 填到下一轮请求的 `reasoning.encrypted_content`（D36+D37）

### 8.7.2 Anthropic

| 项 | Anthropic |
|---|---|
| 启用方式 | 顶层 `thinking: { type:"enabled", budget_tokens: N }` |
| 流式事件 | `content_block_start` (type=thinking) + `content_block_delta` (type=thinking_delta / signature_delta) + `content_block_stop` |
| 思考 token 计数 | 由 Codec 从 `usage.output_tokens` 中**根据 stop_reason / 块类型分布**单独计算（Anthropic 不直接给 reasoning_tokens 字段）|
| 签名 | `thinking.signature`（**所有账户**必须原样回传） |
| 加密思考 | `redacted_thinking`（仅安全审查触发时返回，Anthropic 内部加密文本）|

**ReasoningTokens 单独提取的算法**（D40 fallback 之一）：
- 维护一个 `thinkingByteCount`：流式 thinking 块累计字符数
- 维护一个 `textByteCount`：流式 text 块累计字符数
- `usage.output_tokens` 来自 message_delta
- `ReasoningTokens ≈ output_tokens × thinkingByteCount / (thinkingByteCount + textByteCount)`（按字符比例分摊）
- Anthropic 未来如直接给 `usage.thinking_tokens` 字段，优先使用，移除分摊

> 这是 fallback 估算；Anthropic 文档未保证精确，但 cost 计算误差可控。一旦 SDK 提供精确字段立即切换。

### 8.7.3 Gemini

| 项 | Gemini |
|---|---|
| 启用方式 | `thinkingConfig.thinkingBudget`（-1=自动；0=禁用；正数=budget） |
| 流式事件 | part 中 `thoughtSummary` 字段 |
| 思考 token 计数 | `usageMetadata.thoughtsTokenCount` |
| 签名 | 无（Gemini 不要求回传） |
| 加密思考 | 无 |

Codec 处理：thoughtSummary 直接进 `Delta.Thinking`；签名留空。

### 8.7.4 思考块的"用户可见性"与渲染

| ContentBlock | UserVisibility | UI 渲染 |
|---|---|---|
| `BlockThinking`（含或不含签名） | 取自当前 assistant 消息的 UserVisibility（一般是 visible） | 折叠面板，可展开看完整思考；CLI 默认隐藏（`/thinking on` 显示）|
| `BlockRedactedThinking` | 同上 | 不可展开的"已加密思考"占位块（D39） |

### 8.7.5 不支持思考但请求启用

`Capabilities.SupportsThinking=false` 但 `Request.EnableThinking=true` 时，Provider：
1. 写 warn 日志：`"thinking requested but model X does not support thinking; ignored"`
2. 正常发起请求（不传 thinking 字段）
3. 不返回错误

---

## 8.8 Pricing 与 Cost 计算

### 8.8.1 内置表结构

```go
// internal/llm/<provider>/pricing.go
package <provider>

import "mini-agent/internal/llm"

type ModelInfo struct {
    Capabilities llm.Capabilities

    // 单价：USD per million tokens（per MTok）
    InputPerMTok          float64
    OutputPerMTok         float64
    ReasoningPerMTok      float64  // 思考 token 单价（多数 provider 与 Output 同价；OpenAI o-series 单独价）
    CachedInputPerMTok    float64  // OpenAI cached_input
    CacheCreationPerMTok  float64  // Anthropic cache_creation
    CacheReadPerMTok      float64  // Anthropic cache_read
}

var modelTable = map[string]*ModelInfo{
    // 见 §8.8.3 P0 最小集
}
```

### 8.8.2 Cost 计算公式

```go
func ComputeCost(u *llm.Usage, mi *ModelInfo) float64 {
    nonCachedInput := u.PromptTokens - u.CachedPromptTokens - u.CacheReadTokens
    cost := 0.0
    cost += float64(nonCachedInput)        * mi.InputPerMTok         / 1_000_000
    cost += float64(u.CachedPromptTokens)  * mi.CachedInputPerMTok   / 1_000_000
    cost += float64(u.CacheCreationTokens) * mi.CacheCreationPerMTok / 1_000_000
    cost += float64(u.CacheReadTokens)     * mi.CacheReadPerMTok     / 1_000_000
    cost += float64(u.CompletionTokens-u.ReasoningTokens) * mi.OutputPerMTok    / 1_000_000
    cost += float64(u.ReasoningTokens)                    * mi.ReasoningPerMTok / 1_000_000
    return cost
}
```

注：`u.CompletionTokens` 已含 reasoning_tokens，所以 output 部分要扣掉再算。

### 8.8.3 P0 内置最小集

> 价格基线：2026-Q1 公开报价；用户可在 config `llm.pricing_overrides` 覆盖（D41）。

#### OpenAI 兼容子包

| Model | ContextWindow | MaxOutput | SupportsThinking | Input/MTok | Output/MTok | Reasoning/MTok | CachedInput/MTok |
|---|---|---|---|---|---|---|---|
| `deepseek-chat` | 128_000 | 8_192 | false | 0.27 | 1.10 | 1.10 | 0.07 |
| `deepseek-reasoner`（含 deepseek-v4-pro 别名）| 128_000 | 8_192 | true | 0.55 | 2.19 | 2.19 | 0.14 |
| `gpt-4o` | 128_000 | 16_384 | false | 2.50 | 10.00 | 10.00 | 1.25 |
| `gpt-4.1` | 1_000_000 | 32_768 | false | 2.00 | 8.00 | 8.00 | 0.50 |
| `o3` | 200_000 | 100_000 | true | 2.00 | 8.00 | 8.00 | 0.50 |
| `o3-mini` | 200_000 | 100_000 | true | 1.10 | 4.40 | 4.40 | 0.55 |

注：`deepseek-v4-pro` 是需求文档中的主力模型名；如官方实际名为 `deepseek-reasoner`，在 `modelTable` 中加同义条目（同一 `ModelInfo` 指针）。

#### Anthropic 子包

| Model | ContextWindow | MaxOutput | SupportsThinking | Input | Output | Reasoning | CacheCreation | CacheRead |
|---|---|---|---|---|---|---|---|---|
| `claude-sonnet-4-5` | 200_000 | 64_000 | true | 3.00 | 15.00 | 15.00 | 3.75 | 0.30 |
| `claude-haiku-4-5` | 200_000 | 64_000 | true | 1.00 | 5.00 | 5.00 | 1.25 | 0.10 |

#### Gemini 子包（P1）

| Model | ContextWindow | MaxOutput | SupportsThinking | Input | Output | Reasoning |
|---|---|---|---|---|---|---|
| `gemini-2.5-flash` | 1_000_000 | 65_536 | true | 0.30 | 2.50 | 2.50 |
| `gemini-2.5-pro` | 2_000_000 | 65_536 | true | 1.25 | 10.00 | 10.00 |

> 上述价格为参考基线；实现期以官方最新价为准，并通过 `llm.pricing_overrides` 让用户随时校准。

### 8.8.4 模型不在表中

```go
func (p *Provider) Capabilities() llm.Capabilities {
    info, ok := modelTable[p.cfg.Model]
    if !ok {
        if p.cfg.ForceThinking {
            return defaultCapabilitiesForceThinking()
        }
        slog.Warn("model not in builtin table; using fallback caps",
            "provider", p.Name(), "model", p.cfg.Model)
        return defaultCapabilities()
    }
    return info.Capabilities
}

func defaultCapabilities() llm.Capabilities {
    return llm.Capabilities{
        Model: "<unknown>", ContextWindow: 8192, MaxOutputTokens: 4096,
        SupportsTools: true, SupportsStreaming: true, SupportsThinking: false,
    }
}
```

`force_thinking=true` 来自 config，让 self-hosted 思考模型可手动启用（D48）。

---

## 8.9 Token 估算

### 8.9.1 接口

```go
// internal/llm/tokenest/tokenest.go
package tokenest

import "mini-agent/internal/llm"

type Estimator interface {
    EstimateMessages(messages []*llm.Message) int
    EstimateText(text string) int
}
```

每个 Provider 子包持有一个 Estimator 实例：

```go
// internal/llm/openai/provider.go
type Provider struct {
    // ...
    estimator tokenest.Estimator
}

// EstimateTokens 暴露给 agent.maybeCompact 使用
func (p *Provider) EstimateTokens(messages []*llm.Message) int {
    return p.estimator.EstimateMessages(messages)
}
```

### 8.9.2 实现选择

| Provider | Estimator | 库 / 算法 |
|---|---|---|
| OpenAI 兼容（含 DeepSeek） | `tokenest.NewTiktoken(model)` | `pkoukk/tiktoken-go` —— 加载 `o200k_base` / `cl100k_base` BPE |
| Anthropic | `tokenest.NewCharRatio(0.6, 0.25)` | 中文 0.6 字符/token、英文 0.25 字符/token 加权 |
| Gemini | `tokenest.NewCharRatio(0.6, 0.25)` | 同上 |

> 选择理由：tiktoken-go 对 OpenAI 系准确（误差<5%），但对 Anthropic 偏差较大（Claude 用 BPE 不同）；字符比例近似的优势是脱离外部依赖、足够快、用于"压缩触发判定"的精度足够（误差 ±15%）。

### 8.9.3 用途边界

| 场景 | 用估算 or 用 Provider 返回 |
|---|---|
| `agent.maybeCompact` 判定是否触发 | **估算**（请求前还没有 usage）|
| 写入 `messages.tokens` 字段（messages 表）| **估算**（per-message） |
| 写入 `usage_log` 表 | **Provider 返回**（精确）|
| `/cost` 命令展示 | **Provider 返回**（精确） |

**估算只服务于"压缩触发判定"，绝不进 Usage**——避免污染计费。

---

## 8.10 网络层：重试与超时

### 8.10.1 重试策略（共享子包 `internal/llm/network/`）

```go
// internal/llm/network/retry.go
package network

type RetryConfig struct {
    Max         int           // 默认 3
    BackoffBase time.Duration // 默认 1s
    Jitter      float64       // 默认 0.2 (±20%)
}

func WithRetry(ctx context.Context, cfg *RetryConfig, fn func(ctx context.Context) (any, error)) (any, error) {
    var lastErr error
    for attempt := 0; attempt <= cfg.Max; attempt++ {
        if err := ctx.Err(); err != nil { return nil, err }
        result, err := fn(ctx)
        if err == nil { return result, nil }
        if !shouldRetry(err) { return nil, err }
        if attempt == cfg.Max { lastErr = err; break }
        sleep := backoffDuration(attempt, cfg, err)
        select {
        case <-ctx.Done(): return nil, ctx.Err()
        case <-time.After(sleep):
        }
        lastErr = err
    }
    return nil, fmt.Errorf("retry exhausted: %w", lastErr)
}
```

### 8.10.2 重试触发条件

```go
func shouldRetry(err error) bool {
    var herr *HTTPError
    if errors.As(err, &herr) {
        switch {
        case herr.StatusCode >= 500:    return true   // 5xx
        case herr.StatusCode == 429:    return true   // rate limit
        case herr.StatusCode == 408:    return true   // request timeout
        }
        return false                                  // 其它 4xx 不重试
    }
    // 网络错误（连接失败 / 读超时 / DNS 失败）
    var netErr net.Error
    if errors.As(err, &netErr) { return true }
    if errors.Is(err, io.ErrUnexpectedEOF) { return true }
    return false
}
```

### 8.10.3 退避算法（指数 + jitter + Retry-After 优先）

```go
func backoffDuration(attempt int, cfg *RetryConfig, err error) time.Duration {
    // Retry-After 优先
    var herr *HTTPError
    if errors.As(err, &herr) && herr.RetryAfter > 0 {
        return herr.RetryAfter
    }
    // 指数退避：base * 2^attempt
    base := cfg.BackoffBase * time.Duration(1<<attempt)  // 1s/2s/4s/8s...
    // jitter ± Jitter%
    jitter := time.Duration(float64(base) * cfg.Jitter * (rand.Float64()*2 - 1))
    return base + jitter
}
```

### 8.10.4 超时分级

```go
// internal/llm/network/timeout.go
type TimeoutConfig struct {
    Request time.Duration // 默认 120s（单次请求；含等待 + 读流总时长）
    Total   time.Duration // 默认 300s（含重试的总时长）
}

func WithTimeout(parent context.Context, cfg *TimeoutConfig) (context.Context, context.CancelFunc) {
    return context.WithTimeout(parent, cfg.Total)
}
```

实现期 Provider 调用方式：

```go
func (p *Provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
    totalCtx, cancel := network.WithTimeout(ctx, p.cfg.Timeout)

    raw, err := network.WithRetry(totalCtx, p.cfg.Retry, func(c context.Context) (any, error) {
        reqCtx, reqCancel := context.WithTimeout(c, p.cfg.Timeout.Request)
        defer reqCancel()
        return p.sdk.Stream(reqCtx, buildAPIRequest(req))
    })
    if err != nil { cancel(); return nil, err }

    ch := make(chan llm.StreamEvent, 16)
    go func() {
        defer cancel()
        defer close(ch)
        p.consumeStream(totalCtx, raw, ch)
    }()
    return ch, nil
}
```

### 8.10.5 流式中途断开

如 §8.5.5 所述：流式过程中（即 `network.WithRetry` 返回成功**之后**）的断开**不重试**：
- channel 中发一个 `StreamError { Err: ... }` 后关闭
- agent loop 视为 `StopError`
- 已收到的 chunks 已通过 Sink 推送给用户（"半截消息"现象）
- 用户在 REPL/Web UI 看到错误后可手动重发

---

## 8.11 Provider Registry 与模型切换

### 8.11.1 Registry 接口

```go
// internal/llm/registry.go
package llm

type Registry interface {
    Register(p Provider) error                  // name 不能重复
    Get(name string) (Provider, bool)
    GetByModel(modelRef string) (Provider, error)  // 按 "provider:model" 或裸 model 查询
    List() []Provider
    Active() Provider                            // 当前激活的 provider（受 /model 切换影响）
    SetActive(modelRef string) error
}
```

### 8.11.2 模型引用语法（D46）

| 输入 | 解析 |
|---|---|
| `claude-sonnet-4-5` | 不带前缀，按内置 model→provider 映射猜测；`claude-*` → `anthropic` |
| `anthropic:claude-sonnet-4-5` | 显式前缀；目标 provider name=`anthropic` |
| `deepseek:deepseek-chat` | 目标 provider name=`deepseek`（OpenAI 兼容子包注册时的实例名）|
| `gpt-4o` | 不带前缀；猜测 → 失败时报错（因为可能是 `openai` 也可能是某 self-hosted 同名）|

**建议规范**：用户配置 / `/model` 命令尽量用 `provider:model` 格式避免歧义。

### 8.11.3 Provider 注册时机

启动时（`internal/bootstrap/`）：
1. 读取 `config.LLM.Providers`
2. 遍历每个 provider 配置；若 `api_key` 为空则跳过（不注册）
3. OpenAI 兼容子包可一次注册多个实例（每个 base_url 一个）；name 取 config 中的 `name` 字段
4. 启动后 `Registry.SetActive(config.LLM.ActiveModel)`；找不到时 fallback 到第一个已注册 provider 并 warn

### 8.11.4 跨 Provider 切换思考块清理

`/model` 切换时，agent 层（`internal/agent/model_switch.go`）执行：

```go
func (l *Loop) SwitchModel(ctx context.Context, modelRef string) error {
    oldP := l.provider
    newP, err := l.registry.GetByModel(modelRef)
    if err != nil { return err }

    // 同一 provider 内切换（Anthropic 改换 sonnet→haiku）：无需清理
    if oldP.Name() == newP.Name() {
        l.provider = newP
        return nil
    }

    // 跨 provider：归档历史中所有含 thinking/redacted_thinking 块的 assistant 消息 +
    //              插入"已切换 model"system 提示
    return l.applyCrossProviderSwitch(ctx, newP)
}

func (l *Loop) applyCrossProviderSwitch(ctx context.Context, newP llm.Provider) error {
    msgs, err := l.sessRepo.ListLiveMessages(ctx, l.sessionID)
    if err != nil { return err }

    var archiveIDs []string
    for _, m := range msgs {
        if hasThinkingBlock(&m) {
            archiveIDs = append(archiveIDs, m.ID)
        }
    }
    if len(archiveIDs) == 0 {
        l.provider = newP
        return nil
    }

    notice := buildCrossSwitchNotice(l.provider.Name(), newP.Name(), len(archiveIDs))
    summary := session.Message{
        SessionID:      l.sessionID,
        Role:           session.RoleSystem,
        Blocks:         []llm.ContentBlock{{Type: llm.BlockText, Text: notice}},
        SourceProvider: "",
        UserVisibility: session.UserSystem,
        OriginalIDs:    archiveIDs,
    }
    if err := l.sessRepo.ApplyCompaction(ctx, l.sessionID, archiveIDs, []session.Message{summary}); err != nil {
        return err
    }
    l.provider = newP
    return nil
}
```

关键约束：
- 只清理含 thinking/redacted_thinking 块的消息；普通 text 消息保留 live
- 通过 `ApplyCompaction` 走"归档+插 summary"路径，符合"消息永不物理删除"（D20）
- summary 用 `UserSystem` 可见性，UI 默认显示为系统提示（用户能看到发生了切换）

---

## 8.12 Provider 子包内部结构示例

### 8.12.1 OpenAI 子包结构

```go
// internal/llm/openai/provider.go
package openai

type Provider struct {
    cfg       *Config
    sdk       *openai.Client
    estimator tokenest.Estimator
}

type Config struct {
    Name           string  // 实例名（"deepseek" / "openai-real" / "kimi" 等）
    BaseURL        string
    APIKey         string
    DefaultModel   string
    Model          string  // 当前激活模型；可被 SetActive 修改
    Timeout        *network.TimeoutConfig
    Retry          *network.RetryConfig
    ForceThinking  bool
}

func New(cfg *Config) (*Provider, error) {
    sdk := openai.NewClient(option.WithAPIKey(cfg.APIKey), option.WithBaseURL(cfg.BaseURL))
    est := tokenest.NewTiktoken(cfg.DefaultModel)  // 缺省按 default model 装载 BPE
    return &Provider{cfg: cfg, sdk: sdk, estimator: est}, nil
}

func (p *Provider) Name() string             { return p.cfg.Name }
func (p *Provider) Capabilities() llm.Capabilities { /* 见 §8.8.4 */ }
func (p *Provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.StreamEvent, error) { /* 见 §8.10.4 */ }
```

### 8.12.2 Anthropic 子包结构

```go
// internal/llm/anthropic/provider.go
package anthropic

type Provider struct {
    cfg       *Config
    sdk       *anthropic.Client
    estimator tokenest.Estimator
}

type Config struct {
    APIKey        string
    DefaultModel  string
    Model         string
    Timeout       *network.TimeoutConfig
    Retry         *network.RetryConfig
    ForceThinking bool
}

func New(cfg *Config) (*Provider, error) { /* ... */ }

func (p *Provider) Name() string             { return "anthropic" }
func (p *Provider) Capabilities() llm.Capabilities { /* 见 §8.8.4 */ }
func (p *Provider) Stream(ctx context.Context, req llm.Request) (<-chan llm.StreamEvent, error) { /* ... */ }
```

---

## 8.13 关键决定（D32–D48）

| 编号 | 决定 |
|---|---|
| **D32** | P0 同时支持 OpenAI 兼容（含 DeepSeek/真 OpenAI/Kimi/Qwen 等）+ Anthropic 双 provider；Gemini P1（Iter-4）；MCP P3 |
| **D33** | Anthropic 走 `anthropics/anthropic-sdk-go` 官方 SDK；Gemini 走 `google.golang.org/genai`；OpenAI 兼容沿用已选定的 `openai-go` |
| **D34** | 流式 tool_call 双轨：`ToolCallDelta.ArgsDiff` 累积原始 JSON 片段供 UI 流式动效；`StreamFinal.Message` 给完整解析后的 ToolUse 块供 Loop 使用 |
| **D35** | `StreamEvent` 新增 `StreamBlockBoundary` 事件类型（含 `BlockType` + `IsStart` + `Index`）；OpenAI 子包合成；Anthropic 子包透传 |
| **D36** | OpenAI `reasoning` / DeepSeek `reasoning_content` / Anthropic `thinking` 统一进 `Delta.Thinking`；`ContentBlock.ThinkingSignature` 是否非空区分有签名/无签名 |
| **D37** | `llm.Request` 新增 `ThinkingEffort string`（`""/"low"/"medium"/"high"`），各 provider 自行映射到 budget_tokens / effort / thinkingBudget |
| **D38** | `SupportsThinking=false` 但 `EnableThinking=true` 时，Provider 打 warn 后忽略，不报错 |
| **D39** | redacted_thinking 在 UI 显示**不可展开的"已加密思考"占位块**；持久化层完整保留 signature |
| **D40** | Provider usage 缺失：Provider 子包按需选择 fallback（OpenAI 显式开 include_usage；Anthropic 用字符比例分摊 reasoning_tokens）并写日志告警；canonical 兜底返回 `Usage{}` 零值；不阻断主流程 |
| **D41** | 单价表写在 `internal/llm/<provider>/pricing.go`；config 段 `llm.pricing_overrides` 可覆盖；Capabilities 同表（不调 `/v1/models`）；模型不在表内退化默认值 `{8192, 4096, true, true, false}` + warn |
| **D42** | `llm.Usage` 扩展三字段：`CachedPromptTokens` / `CacheCreationTokens` / `CacheReadTokens`；`usage_log` 表新增同名三列；CostUSD 按各自单价分别计算 |
| **D43** | Token 估算由各 Provider 子包自实现：OpenAI 系用 `pkoukk/tiktoken-go`；Anthropic / Gemini 用字符×系数（中文 0.6 / 英文 0.25）；估算只服务于"压缩触发"，不进 Usage |
| **D44** | 网络层：重试触发 5xx + 429 + 408 + 网络错误；指数退避 1s/2s/4s ± 20% jitter，Retry-After 优先；流式中途断开不重试，半截消息 + StopError 上抛；超时配置化：`request_timeout=120s` / `total_timeout=300s` |
| **D45** | function calling：Canonical Schema 用 OpenAI 风格 JSON Schema；Anthropic 子包做 `schema → input_schema` rename；missing tool_use_id 自动生成 `tooluse-{uuidv7}` + warn；canonical 永远用 `user + ToolResultBlock`；ToolChoice 各 provider 各自映射 |
| **D46** | 模型切换：`/model <provider>:<name>` 显式前缀；启动时按 config 中已配 api_key 的 provider 注册；跨 provider 切换在 `internal/agent/model_switch.go` 调 `ApplyCompaction` 自动归档历史思考块（不物理删除）|
| **D47** | OpenAI 兼容子包支持多 base_url：config 段 `llm.providers.openai_compat: [{name, base_url, api_key, default_model, force_thinking}, ...]` 数组，每项注册为独立 Provider 实例（name 唯一）|
| **D48** | `SupportsThinking` 由内置表显式标注；config `llm.providers.*.force_thinking=true` 可覆盖（用于 self-hosted 思考模型）|

---

## 8.14 留待后续轮次

| 议题 | 归属 |
|---|---|
| `/model` `/cost` 等斜杠命令的精确语义 | R9 |
| `network.HTTPError` 类型的精确字段（StatusCode、RetryAfter、ErrorCode） | 实现期 |
| OpenAI 兼容族（Kimi / Qwen 等）的 model 内置表条目 | 按需逐个补；不阻塞 P0 |
| Gemini 子包详细实现 | Iter-4 |
| MCP Provider 适配 | R13 |
| Anthropic prompt caching 的 `cache_control` 块如何由 agent 层主动注入 | 实现期评估（P0 不主动注入，被动接收 Anthropic 缓存）|
| Provider 实例热重载（运行时改 config 后重新注册） | 不支持（与 R4 D25 一致） |
| 跨 provider 切换 summary 的具体文案 | R6（与 ReAct prompt 文案合并） |
