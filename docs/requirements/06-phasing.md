# 6. 阶段划分与优先级

设计与需求一次性产出完整方案；实现按下列阶段推进。具体每阶段包含的工具清单、模块划分等细节留待设计阶段。需求层只锁定**阶段所交付的用户可见能力**。

## P0 — MVP（Day 1–3）

CLI 形态可独立运行的最小可用版本：

- 配置加载（默认路径 + 命令行覆盖）
- 接入 DeepSeek-V4-Pro（OpenAI 兼容协议） + 流式输出 + 原生 Function Calling + Token 统计
- 文件读、写、编辑、列目录类基础工具
- shell 执行工具
- 内容搜索与文件名匹配类基础工具
- ReAct 循环 + 步数上限 + 工具失败重试
- CLI 交互式 REPL + 全部斜杠命令的核心子集
- SQLite 会话持久化
- 权限四模式 + 命令询问
- `AGENTS.md` 加载

## P1 — 增强（Day 4–5）

- 上下文自动压缩（摘要式策略先落地）
- `write_plan` 工具与 todo 列表
- 子 agent (`task`) 工具
- 详细 Trace 落地至日志文件
- 命令级 / 路径级 / 工具级白黑名单
- 多 LLM Provider 抽象（Anthropic、Gemini 等）
- **Skill 加载机制**：`skill_tool`、启动时 skill 列表注入系统提示词、`/skill <name>` 命令、扫描用户级与项目级两个查找位置、压缩后重新注入 skill 列表

## P2 — Web UI（Day 6–7）

- 后端 HTTP API
- 独立 React 前端
- 流式消息渲染、Markdown / 代码高亮 / diff
- 会话列表、Todo 面板、Trace 面板、配置编辑、权限审批卡片
- 文件树与文件预览 / diff
- 多 cwd 切换
- **Skill 详情查看面板**：列出可用 skill 与 description，支持查看 SKILL.md 内容预览

## P3 — 后续（一周窗口外）

- MCP Client（stdio / SSE / Streamable HTTP）
- 上下文压缩的滑动窗口式与分层式策略
- 异步子 agent（如有需求时再评估）

> 上述阶段时间是预期推进顺序而非强制截止，超期或调整以实际推进情况为准。
