# 2. 关键决定（D1–D86）

本章列出系统设计阶段累积的关键决定。每条决定都有明确的接受范围与理由。

## 2.1 R1 决定（D1–D10）

| 编号 | 决定 | 理由 |
|---|---|---|
| **D1** | 不分 domain / application / infrastructure 层；按功能模块平铺 | 一周交付窗口紧，模块数量可控（约 16 个），DDD 分层是 overkill；用户明确拒绝 DDD |
| **D2** | 全部代码在 `internal/`，不引入 `pkg/` | 项目不对外提供 SDK 复用（需求层未提及），全部逻辑视为私有；后期若有复用需求再从 internal 提升 |
| **D3** | 依赖装配走 `internal/bootstrap` 手写 DI，不引入 wire 等代码生成框架 | 模块数量可控；手写更直观、调试方便；wire 对一周项目过重 |
| **D4** | 接口在**提供方自己的包内**定义（风格 A） | 与 Java/C#/TypeScript 习惯一致；接口与实现就近，阅读代码直观；用户明确选择此风格 |
| **D5** | 前端独立工程位于 `web/`，不嵌套到 internal | 前端独立 package.json 与构建链；后端通过 `go:embed` 在打包时可选嵌入构建产物（R10 详定） |
| **D6** | Tool 实现集中在 `internal/tool/<分类>/`，每个工具一个子包 | 便于按分类管理；权限矩阵按子包归类清晰 |
| **D7** | SQLite 迁移文件 `go:embed` 到二进制；sqlc 生成代码提交 git | 用户拿到 mini-agent 二进制即可自动建库；sqlc 生成代码提交 git 避免运行时生成 |
| **D8** | cobra 子命令清单：默认 root → REPL；`serve` → 启 Web 后端；`migrate` → 显式数据库迁移；`version` → 版本信息 | 与 Claude Code / aider 习惯一致；管理命令通过 REPL 内 `/tools` 等斜杠命令解决，不开新子命令 |
| **D9** | 一次性 prompt 模式通过 root 命令的 `-p` / `--print` 实现 | 与 Claude Code / aider 习惯一致；无需新子命令 |
| **D10** | **agent 与用户的双向交互统一通过 `internal/uio` 抽象**：`Sink`（单向输出）+ `Prompter`（双向请求）；CLI 与 webapi 各自实现这两个接口；中断信号通过 ctx 取消阻塞的 Prompter 调用 | 让 agent 包不知道自己跑在 CLI 还是 Web 里；权限审批、ask_user、Ctrl+C 中断等场景的实现统一收敛 |

## 2.2 R3 决定（D11–D24）

| 编号 | 决定 | 理由 |
|---|---|---|
| **D11** | 表结构：`sessions / messages / todos / usage_log` 四张业务表 + `schema_migrations`；不引入 traces 表 | Trace 走日志文件（与 R1 一致）；usage 单独成表便于聚合统计 |
| **D12** | UUIDv7 作为所有主键；TEXT 列存储；用 `github.com/google/uuid` v1.6+ | time-ordered 索引友好；分布式安全；标准 36 字符表达便于调试 |
| **D13** | usage 单独走 `usage_log` 表，每次 LLM 响应记一条；统计通过 SUM 聚合 | 多 model 价格灵活；`Session.UsageTotal` 是聚合视图字段，不是存储字段 |
| **D14** | 消息内容用 Canonical ContentBlock 列表表达，存为 `blocks_json` TEXT；新增 `source_provider` 列记录来源协议 | ContentBlock 兼具 OpenAI 与 Anthropic 表达力；JSON 列简单可扩展 |
| **D15** | 启动时自动执行 `migrate.Up()`；提供 `mini-agent migrate` 显式触发 | 用户拿到二进制即可使用；`migrate` 子命令便于排障 |
| **D16** | PRAGMA：WAL + foreign_keys=ON + busy_timeout=5000 | WAL 支持 CLI/webapi 同进程并发读；FK 强制；超时缓解短暂竞争 |
| **D17** | 事务策略：高频单条 INSERT 不上事务；ApplyCompaction / ReplaceTodos 上事务；只读不上事务 | 性能与原子性平衡 |
| **D18** | **三层数据结构**：Canonical `llm.Message`（含 ContentBlock）/ 存储 `session.Message`（blocks_json + visibility + user_visibility + source_provider）/ 视图 `view.Item`（按 Kind 拆解）三层各司其职 | 让 LLM 协议 / 持久化 / UI 展示三件事解耦，互不污染 |
| **D19** | 每个 Provider 自带 Codec，负责协议层 ↔ Canonical 的双向转换；agent loop 只看到 Canonical | 新增 Provider 时仅改自己的子包；agent / session 不变 |
| **D20** | 消息**永不物理删除**；压缩通过 `visibility` 切换实现（live → archived），并插入 `summary` 消息 | 保留完整可追溯历史；UI 可展开归档查看；用户对压缩的安全感更高 |
| **D21** | `llm.ContentBlock` 新增 `Thinking` / `RedactedThinking` 类型；保留 `ThinkingSignature` 字段以兼容 Anthropic | Anthropic 多轮思考必须回传 signature；Canonical 必须能装下 |
| **D22** | `llm.Usage` 与 `usage_log` 表新增 `ReasoningTokens` 字段；`/cost` 与 Web UI 面板单独披露 | 思考 token 价格通常与回答 token 不同；用户应能看到 |
| **D23** | 上下文压缩对 thinking 的规则：同一 assistant 消息内 thinking + tool_use 不可拆开；Body 划分以**消息**为最小单位 | Anthropic 硬性要求：thinking + tool_use 缺一不可，否则签名验证失败 |
| **D24** | 消息引入正交的两个可见性维度：`Visibility`（LLM 可见性，live/archived/summary）+ `UserVisibility`（用户可见性，visible/hidden/system）；Repository 提供 `ListLiveMessages` / `ListVisibleMessages` / `ListAllMessages` 三种查询入口 | 把"LLM 看到的"和"用户看到的"正交切分；prompt 注入对用户隐藏，不污染 UI |

## 2.3 R4 决定（D25–D31）

| 编号 | 决定 | 理由 |
|---|---|---|
| **D25** | 配置加载三层优先级：默认值 < 配置文件 < CLI flag。**不读环境变量**作为正式配置源 | 减少配置层级混乱；敏感数据若以后需要走 env 再单独评估 |
| **D26** | 配置文件结构按子模块分组（11 顶层 key ~37 字段）；敏感字段（api_key）在 `String()` / 日志输出时屏蔽 | 与 internal 模块一一对应便于阅读与维护 |
| **D27** | AGENTS.md 查找：全局 `~/.mini-agent/AGENTS.md` + 项目级 `<cwd>/AGENTS.md`（**不向上递归**）；`/cd` 后重新加载项目级；用 `---` 拼接为合并文本注入；UserVisibility=system | 不向上递归避免 monorepo 误命中；显式 cwd 覆盖更可控 |
| **D28** | 白黑名单规则文件位置可配置（默认 `~/.mini-agent/permissions.yaml`）；文件不存在视为无用户规则 | 与配置文件同级别；缺省为空保证开箱即用 |
| **D29** | 规则三种粒度（command / path / tool）+ 两种类型（allow / deny）；`allow` 优先级高于模式判定，但**不能覆盖硬黑名单** | allow 让用户在 plan 模式下也能开局部口子；硬黑名单是兜底安全防线 |
| **D30** | 规则评估顺序：硬黑名单 → 用户规则（按文件顺序首个命中）→ 模式判定 → 审批；`path` 粒度用 doublestar glob 匹配，支持 `${cwd}` / `${home}` / `~` 变量展开 | 顺序固定避免歧义；doublestar 是 Go 生态最常用的扩展 glob |
| **D31** | Go 代码"指针优先，例外明确"风格：① receiver 用 `*T`；② 大结构体在私有 helper、批处理切片用指针；③ 接口（公开契约）方法参数保留值类型；④ 高频调用 + 小事件结构（如 uio Sink）保留值；⑤ 不对 slice/map/channel/func/interface/string/error 加 `*T` | 平衡性能与可读性；接口契约不强制 nil 检查 |

## 2.4 R5 决定（D32–D48）

| 编号 | 决定 | 理由 |
|---|---|---|
| **D32** | P0 同时支持 OpenAI 兼容（含 DeepSeek/真 OpenAI/Kimi/Qwen）+ Anthropic 双 provider；Gemini P1（Iter-4）；MCP P3 | Anthropic thinking + signature 是 Canonical 抽象的"试金石"，必须 P0 验证 |
| **D33** | Anthropic 走 `anthropics/anthropic-sdk-go` 官方 SDK；Gemini 走 `google.golang.org/genai`；OpenAI 兼容沿用已选定的 `openai-go` | 官方 SDK 处理 thinking + signature 协议复杂细节，自封风险高 |
| **D34** | 流式 tool_call 双轨：`ToolCallDelta.ArgsDiff` 累积原始 JSON 片段供 UI；`StreamFinal.Message` 给完整解析后的 ToolUse 块供 Loop | UI 流式动效 + Loop 简单 |
| **D35** | `StreamEvent` 新增 `StreamBlockBoundary` 事件类型（含 `BlockType + IsStart + Index`）；OpenAI 子包合成；Anthropic 子包透传 | UI 渲染折叠面板更精准；不订阅时不影响 |
| **D36** | OpenAI `reasoning` / DeepSeek `reasoning_content` / Anthropic `thinking` 统一进 `Delta.Thinking`；`ContentBlock.ThinkingSignature` 是否非空区分有/无签名 | 维持 R3 单一字段语义，避免接口膨胀 |
| **D37** | `llm.Request` 新增 `ThinkingEffort string`（""/low/medium/high），各 provider 自行映射到 budget_tokens / effort / thinkingBudget | 用户视角统一；provider-specific 数字不进 canonical |
| **D38** | `SupportsThinking=false` 但 `EnableThinking=true` 时，Provider 打 warn 后忽略，不报错 | 不阻塞主流程 |
| **D39** | redacted_thinking 在 UI 显示**不可展开的"已加密思考"占位块**；持久化层完整保留 signature | 用户知情 + 协议完整性 |
| **D40** | Provider usage 缺失：Provider 子包按需选择 fallback（OpenAI 显式开 include_usage；Anthropic 用字符比例分摊 reasoning_tokens）并写日志告警；canonical 兜底返回 `Usage{}` 零值；不阻断主流程 | 各 provider 行为差异大，统一兜底 |
| **D41** | 单价表写在 `internal/llm/<provider>/pricing.go`；config 段 `llm.pricing_overrides` 可覆盖；Capabilities 同表（不调 `/v1/models`）；模型不在表内退化默认值 `{8192, 4096, true, true, false}` + warn | 启动可控、脱机可用 |
| **D42** | `llm.Usage` 扩展三字段：`CachedPromptTokens` / `CacheCreationTokens` / `CacheReadTokens`；`usage_log` 表新增同名三列；CostUSD 按各自单价分别计算 | Anthropic prompt caching 价差极大（cache_read 是 1/10 价），不算会显著高估 |
| **D43** | Token 估算由各 Provider 子包自实现：OpenAI 系用 `pkoukk/tiktoken-go`；Anthropic / Gemini 用字符×系数（中文 0.6 / 英文 0.25）；估算只服务于"压缩触发"，不进 Usage | 计费精度由 Provider usage 保证；估算只用于触发判定 |
| **D44** | 网络层：重试触发 5xx + 429 + 408 + 网络错误；指数退避 1s/2s/4s ± 20% jitter，Retry-After 优先；流式中途断开不重试，半截消息 + StopError 上抛；超时配置化：`request_timeout=120s` / `total_timeout=300s` | 流式重试会重复扣费 + 内容丢失 |
| **D45** | function calling：Canonical Schema 用 OpenAI 风格 JSON Schema；Anthropic 子包做 `schema → input_schema` rename；missing tool_use_id 自动生成 `tooluse-{uuidv7}` + warn；canonical 永远用 `user + ToolResultBlock`；ToolChoice 各 provider 各自映射 | OpenAI 风格 JSON Schema 最通用；rename 简单 |
| **D46** | 模型切换：`/model <provider>:<name>` 显式前缀；启动时按 config 中已配 api_key 的 provider 注册；跨 provider 切换在 `internal/agent/model_switch.go` 调 `ApplyCompaction` 自动归档历史思考块（不物理删除）| 思考块跨 provider 签名不兼容，必须清理；走 ApplyCompaction 符合 D20 |
| **D47** | OpenAI 兼容子包支持多 base_url：config 段 `llm.providers.openai_compat: [{name, base_url, api_key, default_model, force_thinking}, ...]` 数组，每项注册为独立 Provider 实例（name 唯一）| 国产族（DeepSeek/Kimi/Qwen）+ 真 OpenAI 可同时挂载 |
| **D48** | `SupportsThinking` 由内置表显式标注；config `llm.providers.*.force_thinking=true` 可覆盖（用于 self-hosted 思考模型）| 自定义思考模型可手动启用 |

## 2.5 R6 决定（D49–D67）

| 编号 | 决定 | 理由 |
|---|---|---|
| **D49** | ReAct 系统 prompt 极简（约 200 字内）：仅角色 + tool 使用规范 + 安全提示；不引导 todo / write_plan / task 工作流 | 让模型自主决定方法论；减少固定流程束缚；具体方法论交给 AGENTS.md 与 skill |
| **D50** | 系统 prompt 通过 `go:embed internal/agent/prompts/system.md` 内嵌；不支持配置文件覆盖 | 保持行为一致性；改文案需要重编译，避免用户改坏 |
| **D51** | 系统 prompt 环境信息最小集：`cwd` + `os` + `time`（ISO 8601）；不含 shell / Go version / 用户名 | 减少 prompt 噪音；其它信息让模型用工具探查 |
| **D52** | AGENTS.md 注入用 `<project_guidelines>...</project_guidelines>` 标签包裹 | 明确边界便于压缩 / Pinned 划分 |
| **D53** | AGENTS.md 截断（>1MB）后注入末尾追加 `[...truncated, X bytes total]` 让模型知情；写 trace 警告 | 模型能感知"不完整"，避免误以为已读完整规范 |
| **D54** | `prepareInitialHistory` 拼装 3 条独立 system 消息：[1] 内置 prompt / [2] skill 列表 / [3] AGENTS.md；OpenAI 直接发多条；Anthropic Codec 在转换时按 `\n\n---\n\n` 拼接为顶层 system 字段 | 三段语义独立便于单独修改；多 system 兼容 OpenAI 与 Anthropic |
| **D55** | system 消息**不持久化**（保持 R3 一致）：每次启动按当前 cwd / skill / AGENTS.md / config 即时生成；恢复 session 时也即时重建 | cwd / skill / AGENTS.md 更新能立刻生效，避免持久化版本与新启动不一致 |
| **D56** | 失败计数器 signature 算法：`tool_name + ":" + sha256(canonicalJSON(args))[:16]`；canonicalJSON 仅做 map key 排序，不做路径规范化 | map key 顺序是序列化噪音必须消除；路径规范化会带 IO 开销且语义复杂 |
| **D57** | 失败计数器存储用 `sync.Map`，value 为 `*atomic.Int64`；成功后立即 Delete；权限拒绝不计入 | 多 tool_calls 并行时天然并发安全；权限拒绝是策略性而非工具失败 |
| **D58** | 多 tool_calls 执行：按 Category 分桶——`CategoryReadOnly` 工具用 `sync.WaitGroup` 并行；其它 Category 串行；并行批先于串行批 | ReadOnly 无副作用可安全并行；Write/Execute 有副作用必须可预测顺序 |
| **D59** | 并行 readonly 失败时各自独立完成（不取消兄弟）；所有结果（含失败）按原 tool_call 顺序写回 history | 避免一个失败牵连整批；模型能从全部结果中决策 |
| **D60** | 子 agent 失败 / 半成功 tool_result 用结构化文本：`stop_reason / steps_completed / side_effects / last_assistant_output / error`；side_effects 用 SubOutput.WriteCount + ExecCount 直传（P0 简化）| 主 agent 能精准了解子任务进度与副作用，不会盲目重做 |
| **D61** | 子 agent 部分成功视为半成功（不视为失败）：副作用保留；用 D60 同结构告知主 agent；主 agent 自行决定续做还是回滚 | 文件 IO 副作用不可回滚是事实，必须告知；让主 agent 决策最准确 |
| **D62** | 跨 provider 切换 summary 模板：`[Model switched from {old} to {new}; {N} previous assistant messages with reasoning blocks were archived to maintain protocol compatibility.]`；UserVisibility=visible | 用户主动 /model 引发的副作用必须让用户看到反馈，不藏在 system 区 |
| **D63** | ctx 取消颗粒度到工具内部：所有 `tool.Invoke` 必须真正响应 ctx；R7 各工具 schema 同步约束 | bash 长跑命令必须能 SIGKILL 子进程；只读工具用 Go 标准 IO 自动响应；多写代码很少 |
| **D64** | 中断时已发出但未执行的 tool_call：合成 tool_result `[interrupted before tool was invoked]` 与 tool_use_id 配对，保证 OpenAI/Anthropic 协议合法性 | 协议硬约束：tool_use 必须配对 tool_result，否则下一轮请求被拒 |
| **D65** | 子 agent 工具集 = 主 agent 全部工具（含 task）；嵌套深度由 task 工具自身在 Invoke 入口检查 `DepthFrom(ctx) >= SubAgentDepthMax` 时拒绝 | 工具集对称简化设计；嵌套限制集中到一处避免散落判断 |
| **D66** | 失败计数器在子 agent 与主 agent 之间不共享（各自 `*Loop` 独立持有 `*sync.Map`）| 子 agent 是独立任务上下文，重置计数符合直觉 |
| **D67** | Loop / Spawner receiver 全用 `*Loop` / `*Spawner`（D31）；`failureCounter` 作为 `*sync.Map` 字段；`prompts/system.md` 内容在 `NewPromptBuilder` 时一次性 embed 读入并解析 template，缓存 `*template.Template` 字段 | D31 指针约定贯彻；template 解析有开销不应每次 prepareInitialHistory 重复 |

## 2.6 R7-1' 决定（D68–D86）

| 编号 | 决定 | 理由 |
|---|---|---|
| **D68** | 不为每个工具单独走集中设计轮次；用 read_file 走通"工具实现"统一模板，其它工具按模板填空 | 9 个 P0 工具有大量重复模式；模板化复制比逐个讨论快 |
| **D69** | JSON Schema 由 `github.com/invopop/jsonschema` 反射生成；每个工具内部定义 `<ToolName>Args` typed struct + jsonschema tag | 活跃维护、OpenAI 风格、struct tag 友好；避免手写 schema 与 struct 不同步 |
| **D70** | required 由 jsonschema tag 标注；可选字段 description 末尾 `; default: X`；description 全英文 | 与系统 prompt 一致；模型对英文 description 理解最稳 |
| **D71** | 工具 description 写"长描述"风格：含 when to use / when NOT to use / 注意事项 | 极简描述容易让模型误用；参考 OpenAI/Anthropic 官方建议 |
| **D72** | `tool.Result` 字段调整（修订 R2）：删 `Truncated`，新增 `UserLimited` + `ForcedTruncated` | 区分用户主动 limit（健康）vs 工具强制截断（异常），便于压缩与 UI 展示 |
| **D73** | `Result.Content` 用 `<file>` `<dir>` `<grep>` 等结构化标签包装；警告内嵌为单独行附在闭合标签后 | 与 D52 `<project_guidelines>` 风格一致；模型识别边界更准 |
| **D74** | `Result.Display` 格式：`<tool> <subject> (<size>) → <action>`；如截断追加 `[truncated]` | CLI/Web UI 简短摘要友好 |
| **D75** | 错误码扩充（R7 新增）：`ErrTooLarge` + `ErrAmbiguous`，加上 R2 已有共 9 种 | 输出超限与多义性失败语义独立 |
| **D76** | 错误消息全英文；"换方式"提示文案由 agent loop 在 tool_result content 末尾追加（不是工具职责）：`This approach has failed N times in a row — please try a different way.` | 工具职责单一；提示由 R6 D60 触发，agent 层统一文案 |
| **D77** | ctx 错误强约定：`Canceled→ErrInterrupted`；`DeadlineExceeded→ErrTimeout`；工具如有特殊语义可在 doc.go override | 强约定避免工具间不一致；保留 escape hatch |
| **D78** | 工具内部约定俗成两个私有 helper：`decodeArgs(input map[string]any) (*<Args>, error)` + `validateArgs(*<Args>) error`；不进 Tool interface | 类型安全 + 业务校验分离；不污染公共契约 |
| **D79** | read_file 行为：仅文本（NUL 探测前 8KB）；行号单位 offset/limit；总是带 6 位右对齐行号前缀；默认 max_bytes=200KB 上限 1MB | 文本工具简单可靠；二进制走 bash；行号格式与 grep/edit_file 引用一致 |
| **D80** | read_file Content 格式：`<file path lines total_lines range bytes>` 标签包装；强制截断追加 `[warning: ...]` 行 | 元信息便于模型理解输出范围；warning 让模型主动续读 |
| **D81** | read_file Display 格式：`read_file <name> (<size>, <total_lines> lines) → showing <range> [truncated]?` | 简短摘要 |
| **D82** | 通用测试套件 `internal/tool/testkit` 用 testify/suite；含 SchemaNonEmpty / InvalidArgsRejected / CtxCancel / SchemaGolden 四个公共用例 | 新工具按套件填空，测试速度快；强制覆盖最低门槛 |
| **D83** | Schema golden file 校验：`testdata/<tool>.schema.golden.json`；改 schema 必须 `make update-tool-goldens` 同步；CI 强制比对 | 防止 description / schema 偏离；变更 review 时一目了然 |
| **D84** | Registry.Register 启动期校验：name 非空、description 非空、schema 非空且含 `type` 字段、name 不冲突 | 启动期暴露问题，避免运行时报错 |
| **D85** | 8 个其它 P0 工具按 §10.7 表格填空实现；bash 命令解析与硬黑名单完整规则集留实现期（T2.4）针对性补充 | 避免 R7 陷入"每个工具都讨论一遍"；bash 复杂度高需要原型验证 |
| **D86** | `ask_user` 在 `--yes` 模式下仍然要求用户回答 | ask_user 是元工具，与执行风险无关 |

## 2.7 决定的影响范围（增量）

| 决定 | 影响的模块 |
|---|---|
| D25 | `internal/config` / `internal/cli/cmd` |
| D26 | `internal/config` / `internal/logs`（屏蔽敏感字段） |
| D27 | `internal/agentsmd` / `internal/agent`（prepareInitialHistory）|
| D28 | `internal/permission`（rules.go） |
| D29 | `internal/permission` / 用户规则文件 |
| D30 | `internal/permission`（matchUserRule）|
| D31 | 全部 Go 代码（实现期约束） |
| D32–D33 | `internal/llm/{openai,anthropic,gemini}/` 子包结构 |
| D34–D35 | `internal/llm/llm.go`（StreamEvent 字段）+ 各 codec |
| D36–D37 | `internal/llm/llm.go`（Request.ThinkingEffort + Delta.Thinking）+ 各 codec |
| D38–D39 | 各 provider codec + Web/CLI UI 渲染 |
| D40 | 各 provider codec |
| D41 | `internal/llm/<provider>/pricing.go` + `internal/config` |
| D42 | `internal/llm/llm.go`（Usage cache 字段）+ `internal/session/migrations` + `usage_log` 表 |
| D43 | `internal/llm/tokenest/`（共享子包）+ 各 provider |
| D44 | `internal/llm/network/`（共享子包）+ `internal/config` |
| D45 | 各 provider codec |
| D46 | `internal/agent/model_switch.go` + `internal/cli/repl/`（/model 命令）|
| D47 | `internal/config` + `internal/bootstrap`（注册时机）|
| D48 | 各 provider pricing.go + `internal/config` |
| D49–D55 | `internal/agent/prompts.go` + `internal/agent/prompts/system.md` + `internal/agent/loop.go`（prepareInitialHistory）|
| D56–D57, D66 | `internal/agent/signature.go` + `internal/agent/failure_counter.go` + `internal/agent/loop.go` |
| D58–D59 | `internal/agent/exec.go`（多 tool_calls 分桶并行） |
| D60–D61 | `internal/tool/task/format.go` + `internal/agent/agent.go`（SubOutput 扩充字段）|
| D62 | `internal/agent/model_switch.go` |
| D63 | 全部工具 `Invoke` 实现（R7 强制） + `internal/agent/loop.go` |
| D64 | `internal/agent/interrupt.go` + `internal/agent/exec.go` |
| D65 | `internal/tool/task/task.go`（DepthFrom 检查） |
| D67 | `internal/agent/*`（指针约定贯彻） |
| D68, D85 | `internal/tool/*` 全部子包（按模板实现）|
| D69–D71 | 每个工具的 Args struct + Schema()/Description() 实现 |
| D72 | `internal/tool/tool.go`（Result 结构体）+ 调用 Result 的所有处 |
| D73–D74 | 每个工具的 Invoke() Result 拼装 |
| D75 | `internal/tool/tool.go`（ErrorCode 常量）|
| D76 | `internal/agent/exec.go`（追加 hint）|
| D77 | 每个工具的 mapCtxError helper |
| D78 | 每个工具的 decodeArgs / validateArgs 私有 helper |
| D79–D81 | `internal/tool/fs/readfile.go` |
| D82 | `internal/tool/testkit/`（共享测试套件）|
| D83 | `Makefile`（update-tool-goldens 任务）+ 各工具 testdata/ |
| D84 | `internal/tool/registry.go`（启动校验）|
| D86 | `internal/tool/ask/`（实现细节）|
