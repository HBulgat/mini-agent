# mini-agent 系统设计文档

> Status: 进行中（迭代式产出）
> 上一轮锁定：R7-1'（工具实现模板 + read_file 详细设计，2026-05-24）
> 已锁定轮次：元决策 / R1 / R2 / R3 / R4 / R5 / R6 / R7-1'

本目录存放 mini-agent 项目经过多轮系统设计讨论后达成一致的设计文档。设计文档**回答"怎么做"**：目录结构、模块划分、接口形态、数据库 schema、配置字段、API 协议、关键算法等。需求层（"做什么"）请阅读 [`../requirements/`](../requirements/)。

## 文档索引

| 文件 | 内容 | 状态 |
|---|---|---|
| [00-meta-decisions.md](00-meta-decisions.md) | 元决策（文档粒度、技术栈选型） | ✅ 已锁定 |
| [01-overall-architecture.md](01-overall-architecture.md) | 总体架构、目录结构、模块清单、模块依赖图 | ✅ 已锁定 |
| [02-key-decisions.md](02-key-decisions.md) | D1–D86 关键决定 | ✅ 已锁定（R7-1' 追加 D68–D86） |
| [03-uio-abstraction.md](03-uio-abstraction.md) | agent ↔ 用户交互通道抽象（Sink + Prompter） | ✅ 已锁定 |
| [04-tool-catalog.md](04-tool-catalog.md) | 工具最小完备集合 + 权限模式 × 工具矩阵 | ✅ 已锁定 |
| [05-core-abstractions.md](05-core-abstractions.md) | 9 个核心模块的接口签名、领域模型、协作时序（R2 + R3 + R5 + R6 + R7 修订） | ✅ 已锁定 |
| [06-session-storage.md](06-session-storage.md) | SQLite schema、迁移、ID 策略、Codec、思考模式与可见性存储约束（R3 + R5 修订） | ✅ 已锁定 |
| [07-config-and-rules.md](07-config-and-rules.md) | 配置文件字段、AGENTS.md 查找/合并、白黑名单规则文件、硬黑名单结构（R4 + R5 修订 llm 段） | ✅ 已锁定 |
| [08-llm-providers.md](08-llm-providers.md) | LLM Provider 适配（OpenAI 兼容/Anthropic/Gemini）、流式契约、function calling、思考模式协议、usage/cost、token 估算、网络层重试（R5） | ✅ 已锁定 |
| [09-agent-engine.md](09-agent-engine.md) | Agent 执行引擎细化：系统 prompt 模板、失败计数器、多 tool_calls 分桶并行、子 agent 失败格式化、跨 provider 切换、ctx 取消语义、中断配对（R6） | ✅ 已锁定 |
| [10-tool-template-and-readfile.md](10-tool-template-and-readfile.md) | 工具实现统一模板（6 条规范 + read_file 完整设计 + testkit 套件 + 8 个 P0 工具骨架）（R7-1'） | ✅ 已锁定 |
| [ROADMAP.md](ROADMAP.md) | 后续设计阶段路线图（R7-2 / R8–R14） | 进行中 |

## 与需求文档的对齐

本设计文档严格基于已锁定的需求文档：

- [`../requirements/02-functional.md`](../requirements/02-functional.md) — 功能性需求
- [`../requirements/04-permissions.md`](../requirements/04-permissions.md) — 权限与安全
- [`../requirements/05-extensibility.md`](../requirements/05-extensibility.md) — 扩展性
- [`../requirements/06-phasing.md`](../requirements/06-phasing.md) — 阶段划分
- [`../requirements/07-out-of-scope.md`](../requirements/07-out-of-scope.md) — 排除项
- [`../requirements/08-deferred-to-design.md`](../requirements/08-deferred-to-design.md) — 设计阶段议题清单
- [`../requirements/11-skill.md`](../requirements/11-skill.md) — Skill 模块

如设计阶段发现需要新增功能或调整需求范围，**必须**回到需求层走"同意需求分析结论"流程，不在设计文档中偷偷扩需求。

## 阅读顺序

1. 先读 `00-meta-decisions.md` 了解技术栈
2. 再读 `01-overall-architecture.md` 了解模块全貌
3. 然后读 `02-key-decisions.md` 知道关键决策（D1–D86）
4. 重点关注 `03-uio-abstraction.md`（解耦 agent 与 CLI/Web 的核心机制）
5. 工具相关读 `04-tool-catalog.md` + `10-tool-template-and-readfile.md`
6. 接口签名与领域模型读 `05-core-abstractions.md`（R2 + R3 + R5 + R6 + R7 修订）
7. 数据存储与协议适配读 `06-session-storage.md`（R3 + R5 修订）
8. 配置 / AGENTS.md / 权限规则读 `07-config-and-rules.md`（R4 + R5 修订）
9. LLM Provider 适配读 `08-llm-providers.md`（R5）
10. Agent 执行引擎细节读 `09-agent-engine.md`（R6）
11. 工具实现模板读 `10-tool-template-and-readfile.md`（R7-1'）
12. 后续设计内容查看 `ROADMAP.md`
