# 系统设计阶段路线图

本文档跟踪系统设计阶段各轮次的进展。

## 整体流程

需求层（已锁定）→ 系统设计层（进行中，多轮迭代）→ 实现阶段

设计阶段产出在 `docs/system-design/`，每锁定一轮就追加文件。最后一轮收敛后，由用户回复**"同意系统设计结论"**作为最终批准。

## 轮次进度

| 轮次 | 主题 | 状态 | 输出文件 |
|---|---|---|---|
| 元决策 | 文档粒度、技术栈选型 | ✅ 已锁定 | `00-meta-decisions.md` |
| **R1** | 总体架构、目录结构、模块清单、关键决定（D1–D10）、UIO 抽象、工具集合、权限矩阵 | ✅ 已锁定 | `01-overall-architecture.md` / `02-key-decisions.md` / `03-uio-abstraction.md` / `04-tool-catalog.md` |
| **R2** | 核心抽象与领域模型（接口签名）：trace / uio / llm / tool / permission / skill / compaction / session / agent | ✅ 已锁定 | `05-core-abstractions.md` |
| **R3** | SQLite schema + 迁移 + ID 策略 + Codec + 思考模式与可见性存储约束 + 三层数据结构 | ✅ 已锁定 | `06-session-storage.md`（含对 `05-core-abstractions.md` 的就地修订） |
| **R4** | 配置文件字段（viper）、AGENTS.md 查找/合并、白黑名单文件格式、Go 指针使用约定（D31） | ✅ 已锁定 | `07-config-and-rules.md` |
| **R5** | LLM Provider 适配（OpenAI 兼容/Anthropic/Gemini）、流式契约、function calling、思考模式协议、usage/cost、token 估算、网络层重试 | ✅ 已锁定 | `08-llm-providers.md`（含对 05/06/07 的就地修订）|
| **R6** | Agent 执行引擎细化：系统 prompt、失败计数器、多 tool_calls 分桶并行、子 agent 失败格式化、跨 provider 切换、ctx 取消语义、中断配对 | ✅ 已锁定 | `09-agent-engine.md`（含对 05 §5.9 的就地修订）|
| **R7-1'** | 工具实现模板（6 条统一规范 + read_file 完整设计 + testkit 套件 + 8 个 P0 工具骨架）| ✅ 已锁定 | `10-tool-template-and-readfile.md`（含对 05 §5.4 的就地修订）|
| R7-2 | P1/P2 工具 schema（write_plan / task / skill_tool / web_fetch / web_search） | ⏳ 按 Iter-3/Iter-4 进度触发 | 待定 |
| R8 | 上下文压缩：Compactor 接口、三种策略算法、触发与保留 | ⏳ 待开始 | 待定 |
| R9 | CLI / REPL：cobra 命令树、斜杠命令派发、中断处理细节 | ⏳ 待开始 | 待定 |
| R10 | Web UI 后端 API：接口列表、SSE 流式契约、权限审批流 | ⏳ 待开始 | 待定 |
| R11 | 前端架构：目录结构、状态分层、组件划分 | ⏳ 待开始 | 待定 |
| R12 | 日志与 Trace：JSON Lines schema、轮转、span 关系 | ⏳ 待开始 | 待定 |
| R13 | MCP Client（P3）：三种传输统一抽象、Server 配置 | ⏳ 待开始 | 待定 |
| R14 | 收敛 + 跨模块端到端时序图（5 个关键场景） | ⏳ 待开始 | 待定 |

## R1 + R2 + R3 + R4 + R5 已锁定结论速查

**架构（R1）**
- 不分层架构，按模块平铺
- 接口在提供方包内定义（风格 A）
- `internal/uio` 统一抽象 agent ↔ 用户交互（Sink + Prompter）
- 16 个 internal 模块的目录结构已敲定
- 关键决定 D1–D10

**工具（R1）**
- 14 个工具的最小完备集合（其中 P0：read/write/edit/list/delete/grep/glob/bash/ask_user）
- 工具与 4 种权限模式的全部组合矩阵已敲定

**核心抽象（R2 + R3 + R5 修订）**
- `trace`：Event / Recorder / TraceID-SpanID 父子关系；新增 LLMReasoningChunk
- `uio`：Sink（含 EmitThinkingToken）/ Prompter / ApprovalDecision 三态
- `llm`：Provider / Stream channel / Message（含 ContentBlock 多模态：Text/Thinking/RedactedThinking/ToolUse/ToolResult）/ Usage（含 ReasoningTokens + 三个 cache 字段）/ Request（含 ThinkingEffort）/ StreamEvent（含 BlockBoundary）
- `tool`：Tool / Registry / Result / Category / Error 标准
- `permission`：Gate / Mode 4 态 / Decision 4 态 / 判定流程图
- `skill`：Loader / 项目级覆盖用户级合并
- `compaction`：Compactor / Plan(Pinned+Body) / 触发逻辑由 agent 持有；同 assistant 消息内 thinking+tool_use 不可拆开
- `session`：Repository / 落盘频次 / Message（用 Blocks + Visibility + UserVisibility + SourceProvider）
- `agent`：Runner / Loop / Spawner / 子 agent 复用同一 Loop 类型 / 不引入显式状态枚举

**存储（R3 + R5 修订）**
- SQLite 4 表 + schema_migrations；不引入 traces 表
- UUIDv7 主键
- ContentBlock 用 `blocks_json` 存储；新增 `source_provider`、`visibility`、`user_visibility` 列
- usage 单独走 `usage_log` 表，按 SUM 聚合；R5 新增三个 cache 列
- 消息**永不物理删除**：压缩通过 visibility 切换 + 插入 summary 消息（`ApplyCompaction`）
- 思考 ContentBlock 的 `signature` 必须原样持久化与回传
- 关键决定 D11–D24

**配置与规则（R4 + R5 修订 llm 段）**
- 配置加载三层：默认 < 文件 < CLI；不读环境变量
- 配置文件 11 顶层 key；`llm` 段支持多 Provider 实例 + 多 base_url + pricing_overrides + network 子段
- 所有 provider 的 api_key 屏蔽
- AGENTS.md 全局 + cwd 当前层（不向上递归）+ `---` 拼接合并；UserVisibility=system
- 白黑名单规则三粒度（command/path/tool）+ 两类型（allow/deny）
- 评估顺序：硬黑名单 → 用户规则首个命中 → 模式判定 → 审批
- doublestar glob + `${cwd}` / `${home}` / `~` 变量展开
- Go 代码"指针优先"风格（D31）
- 关键决定 D25–D31

**LLM Provider 适配（R5）**
- P0：OpenAI 兼容（含 DeepSeek/Kimi/Qwen/真 OpenAI/self-hosted） + Anthropic 双 provider
- P1：Gemini；P3：MCP
- 流式 tool_call 双轨（增量 JSON 片段供 UI / Final 解析后供 Loop）
- StreamBlockBoundary 事件（OpenAI 合成；Anthropic 透传）
- ThinkingEffort 字段（""/low/medium/high）；各 provider 自映射 budget_tokens / effort / thinkingBudget
- 三个 cache 字段（CachedPromptTokens / CacheCreationTokens / CacheReadTokens）+ CostUSD 分别计费
- 内置 model 表（pricing.go）+ config pricing_overrides；模型不在表内退化默认值
- Token 估算：OpenAI 用 tiktoken-go；Anthropic / Gemini 用字符×系数；只服务于压缩触发
- 网络层：5xx + 429 + 408 + 网络错误重试；指数退避 + jitter；Retry-After 优先；流式中途不重试
- 跨 provider 切换通过 `internal/agent/model_switch.go` 调 ApplyCompaction 自动归档思考块
- 关键决定 D32–D48

**Agent 执行引擎（R6）**
- 内置系统 prompt 极简（约 110 词）+ go:embed；不可被 config 覆盖
- prepareInitialHistory 拼装 3 条独立 system 消息（prompt / skill 列表 / AGENTS.md）；system 不持久化
- AGENTS.md 用 `<project_guidelines>` 标签包裹
- 失败计数器：sync.Map + signature(tool_name + canonicalJSON(args) sha256)；子/主 agent 不共享
- 多 tool_calls 按 Category 分桶：ReadOnly 并行（不取消兄弟）；其它串行
- 中断时未执行 tool_call 合成 `[interrupted before tool was invoked]` 与 tool_use_id 配对
- 子 agent 失败 / 半成功用结构化模板（stop_reason / steps / side_effects / last_msg / error）
- 跨 provider 切换 summary 文案 UserVisibility=visible
- ctx 取消颗粒度到 tool.Invoke 内部
- 关键决定 D49–D67

**工具实现模板（R7-1'）**
- 不为每个工具单独走集中设计轮次；read_file 走通模板，其它工具按模板填空
- JSON Schema 用 `invopop/jsonschema` 反射生成 `<ToolName>Args` typed struct
- description 长描述风格（when to use / NOT use）；全英文
- Result 字段调整：删 Truncated；新增 UserLimited + ForcedTruncated（修订 R2）
- ErrorCode 新增 ErrTooLarge + ErrAmbiguous（修订 R2）
- ctx 错误强约定：Canceled→ErrInterrupted；DeadlineExceeded→ErrTimeout
- 工具私有 helper：decodeArgs + validateArgs（不进 Tool interface）
- read_file：仅文本（NUL 探测）、行号 offset/limit、6 位行号前缀、`<file>` 标签包装、默认 200KB 上限 1MB
- testify/suite 共享套件 + Schema golden file 强校验
- Registry.Register 启动期 schema 校验
- 关键决定 D68–D86

## 留待后续轮次的关键议题

来源：需求层 `08-deferred-to-design.md` + R1–R7-1' 讨论中的留白。

| 议题 | 归属轮次 |
|---|---|
| 8 个其它 P0 工具的具体实现（按 R7-1' 模板填空）| 实现期（T1.6 / T2.3 / T2.4 / T2.5）|
| bash 命令的 shellwords 解析与复合命令拆解（含硬黑名单完整规则集） | 实现期（T2.4） |
| AlwaysApprove / RememberApproval 等价性判定 | 实现期（permission 模块） |
| edit_file 多义性失败时的 diff 渲染细节 | 实现期 |
| 5 个 P1/P2 工具（write_plan / task / skill_tool / web_fetch / web_search）的 schema | R7-2（按 Iter-3/Iter-4 进度触发）|
| web_fetch 的 HTML→markdown 转换库选型 | R7-2 / Iter-4 实现期 |
| Compactor 三种策略的具体算法、prompt 模板 | R8 |
| 压缩后 skill 列表重新注入的具体函数 | R8 |
| 压缩失败回退策略 | R8 |
| `mini-agent migrate` 子命令 CLI flag 设计 | R9 |
| `/model` `/cost` `/thinking` `/show-hidden` `/show-system` `/show-archived` 斜杠命令的精确语义 | R9 |
| `view.Item` 字段定稿 + BuildConversation 实现 | R9 / R11 |
| 各 trace 事件的 Fields schema、JSON Lines 序列化格式、思考事件字段 | R12 |
| SSE 事件名与 payload 格式 | R10 |
| 前端工程结构、antd 组件划分、思考折叠面板、hidden 调试开关 | R11 |
| MCP Server 注册与命名冲突处理 | R13 |
