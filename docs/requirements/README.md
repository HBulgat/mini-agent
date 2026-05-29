# mini-agent 需求文档

> Status: Approved
> Date: 2026-05-23
> Approval phrase received: "同意需求分析结论"

本目录存放 mini-agent 项目经过多轮需求分析后达成一致的最终需求文档。本文档**只描述产品要做什么、不做什么、边界在哪、验收标准是什么**，不包含任何系统设计层面的内容（目录结构、表结构、配置文件字段、协议选型、库选型、类设计等均留待设计阶段）。

## 文档索引

| 文件 | 内容 |
|---|---|
| [01-overview.md](01-overview.md) | 项目背景、目标、产品形态、用户角色与使用场景 |
| [02-functional.md](02-functional.md) | 功能性需求（LLM 接入、Agent 执行、工具系统、上下文、会话、CLI、Web UI 等） |
| [03-non-functional.md](03-non-functional.md) | 非功能性需求（性能、可靠性、可观测性、安全） |
| [04-permissions.md](04-permissions.md) | 权限与安全模型 |
| [05-extensibility.md](05-extensibility.md) | 扩展性需求 |
| [06-phasing.md](06-phasing.md) | 阶段划分与优先级 |
| [07-out-of-scope.md](07-out-of-scope.md) | 明确不做的范围 |
| [08-deferred-to-design.md](08-deferred-to-design.md) | 延迟至设计阶段的议题清单 |
| [09-acceptance.md](09-acceptance.md) | 验收标准 |
| [10-open-questions.md](10-open-questions.md) | 遗留问题与假设 |
| [11-skill.md](11-skill.md) | Skill 模块（领域知识包加载机制） |

## 阅读顺序建议

- 第一次阅读：按编号顺序通读
- 进入设计阶段前必读：`08-deferred-to-design.md`、`07-out-of-scope.md`
- 验收时必读：`09-acceptance.md`
