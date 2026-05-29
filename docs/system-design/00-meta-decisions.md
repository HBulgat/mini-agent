# 0. 元决策

本章记录系统设计阶段开始前的元决策：文档输出粒度、技术栈选型。这些决策影响后续所有设计文档的形态。

## 0.1 文档粒度

采纳**混合粒度**（C 模式）：

- **核心模块**走完备版：含 mermaid 时序图、模块依赖图、关键伪代码、接口签名
  - 总体架构、领域模型、Agent Loop、上下文压缩、权限系统、LLM Provider、UIO 抽象、Web UI 后端 API
- **外围模块**走轻量版：表格 + 文字
  - CLI 启动参数、日志字段表、配置字段表、SQLite schema 列表

## 0.2 输出位置

设计文档全部输出到 `docs/system-design/`。

## 0.3 技术栈

### Go 侧

| 维度 | 选型 | 理由 |
|---|---|---|
| HTTP 框架 | **gin** | 用户指定 |
| CLI 框架 | **spf13/cobra** | 事实标准；与 viper 同作者天然集成；kubectl/docker/gh 都在用 |
| 配置库 | **spf13/viper** | 用户指定；与 cobra 集成 |
| 日志库 | **log/slog**（Go 1.21+ 标准库）+ 自封一层 | 用户指定。封装层职责：注入 trace/span_id；屏蔽敏感字段（API Key 等）；JSON Lines handler；日志轮转适配 |
| 日志轮转 | **gopkg.in/natefinch/lumberjack.v2** | 标准生态；slog handler 包一层 lumberjack writer |
| SQLite 驱动 | **modernc.org/sqlite**（纯 Go） | 不依赖 cgo，跨平台编译零障碍 |
| ORM / SQL 层 | **sqlc** | 把 SQL 编译为类型安全 Go 代码；零运行时反射；学习成本低 |
| Schema 迁移 | **golang-migrate/migrate** | 最常用；与 sqlc 配合良好（migrate 管 DDL，sqlc 生成查询代码） |
| OpenAI SDK | **openai/openai-go**（官方 v1+） | 用户指定；DeepSeek 通过 base_url 走同一 SDK |
| HTTP 客户端 / 重试 | 标准 `net/http` + 自封重试 | 等间隔 3 次重试很简单，不引入 retry 库 |
| 测试 | `testing` + **stretchr/testify** + **golang/mock** | testify 是事实标准；gomock 用于隔离 LLM Provider |
| 集成测试 | 内置 `httptest` + sqlite `:memory:` | 不引入 docker / testcontainers |

### 前端侧

| 维度 | 选型 | 理由 |
|---|---|---|
| 框架 | **React 18 + TypeScript** | 需求层已锁 React |
| 构建工具 | **Vite** | 启动秒级、HMR 快；优于 CRA（已停维护）和 Next.js（不需要 SSR） |
| UI 库 | **Ant Design v5** + `@ant-design/icons` + `@ant-design/x` | 用户指定；@ant-design/x 是 AI 对话场景组件库 |
| 状态管理 | **Zustand** | 轻量（~1KB）、心智模型简单；适合一周项目 |
| 服务端状态 | **TanStack Query (React Query) v5** | 处理 REST 缓存、loading、refetch |
| 流式接收 | 原生 **EventSource (SSE)** + 自封 hook | 后端走 SSE 推送 |
| Markdown 渲染 | **react-markdown** + **remark-gfm** | 生态最稳；代码块用 **shiki** 或 **highlight.js** |
| Diff 渲染 | **react-diff-viewer-continued** | 最活跃的 React diff 组件 |
| 路由 | **react-router v6** | 事实标准 |
| HTTP 客户端 | **axios** | 拦截器和错误处理更省事 |
| 测试 | **Vitest** + **React Testing Library** | 与 Vite 同生态；零配置 |
| 包管理 | **pnpm** | 快、磁盘省 |

### 工具链

| 维度 | 选型 |
|---|---|
| 静态检查 | go vet + staticcheck |
| Makefile | 提供，串起 build / test / migrate / sqlc generate / web build |
| 提交规范 | 不强制（一周项目不引入 husky 等） |
