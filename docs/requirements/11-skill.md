# 11. Skill 模块

## 11.1 概念与定位

**Skill** 是模型可按需加载的领域知识包，用于在特定场景下为 agent 注入专业流程、SOP、参考资料与可执行脚本。Skill 由模型自主决定何时使用，遵循"渐进披露"思想：启动时模型只看到 skill 列表与简短描述，命中时才加载完整 SKILL.md，需要更深入资源时再按需读取。

Skill 与既有概念的边界：

| 概念 | 用途 | 与 Skill 的区别 |
|---|---|---|
| 子 agent (`task`) | 派生独立子流程，同步阻塞返回结果 | Skill 是知识包，不是执行单元 |
| 工具 | 模型调用的固定能力 | Skill 不可定义新工具 |
| `AGENTS.md` | 项目级**永久**注入上下文 | Skill 是**按需**加载的知识包 |

Skill 与上述三者**并列共存，互不替代**。

## 11.2 Skill 文件规范

Skill **完全对齐** Claude Code / CodeBuddy 的 Skill 规范：

- 每个 skill 是一个目录，目录名即 skill 名（小写连字符命名，例如 `requirements-analysis`）。
- 必含 `SKILL.md` 文件，文件首部必须为 YAML frontmatter，至少包含两个字段：
  - `name`：必须与目录名一致
  - `description`：用于让模型判断何时加载该 skill 的简短描述
- 可选子目录：
  - `scripts/`：skill 携带的可执行脚本
  - `references/`：详细参考资料，由模型按需通过 `read_file` 读取
  - `assets/`：skill 输出时使用的资源文件（模板、图片等）

具体格式细节遵循 Claude Code Skill 规范，本需求文档不再重复列出。

## 11.3 触发与加载

### 11.3.1 触发方式

Skill 支持以下两种触发方式：

1. **模型自动判断**：模型根据用户输入判断是否命中某个 skill 的 description，主动调用 `skill_tool` 加载。
2. **用户显式触发**：用户在 REPL 中输入 `/skill <name>`，等价于在下一条 user message 前注入"请使用 `<name>` skill"提示，让模型加载该 skill 后再继续。

### 11.3.2 启动时注入

启动时（或会话恢复时），系统**必须**把所有可用 skill 的 `name + description` 作为列表注入系统提示词，以便模型感知可用 skill 集合。

### 11.3.3 加载方式

- Skill 内容**按需加载**进入上下文：仅在模型调用 `skill_tool` 后，对应 skill 的 SKILL.md 全文才会注入上下文。
- skill 中的 `references/` 与 `assets/`**不**由 `skill_tool` 自动加载；模型在读完 SKILL.md 后，根据 SKILL.md 的指引自行通过 `read_file` 工具按需读取。
- skill 中的 `scripts/`**不**由 `skill_tool` 自动执行；模型按 SKILL.md 指引使用现有 `bash` 工具调用脚本。

## 11.4 专用工具：`skill_tool`

系统**必须**为模型提供唯一一个 skill 相关工具：

| 工具名 | 职责 |
|---|---|
| `skill_tool` | 加载指定 skill 的 SKILL.md 内容到对话上下文 |

- **不**提供 `list_skills` 工具：B4 已经在系统提示词注入 skill 列表，重复列举属于冗余设计。
- **不**提供 `read_skill_resource` 工具：references / scripts / assets 由模型通过现有 `read_file` / `bash` 工具按需访问。
- **不**提供 `run_skill_script` 工具：脚本执行复用 `bash` 工具，避免新增独立的执行入口。

`skill_tool` 的具体参数 schema 与返回结构留待设计阶段确定。

## 11.5 Skill 来源与查找位置

系统支持两个 skill 查找位置：

| 位置 | 路径 | 优先级 |
|---|---|---|
| 项目级 | `<cwd>/.mini-agent/skills/<name>/` | 高 |
| 用户级 | `~/.mini-agent/skills/<name>/` | 低 |

查找规则：

- 同名 skill 同时存在于项目级与用户级时，**项目级覆盖用户级**。
- **不**支持向上递归查找项目级 skill（仅检查当前 cwd 下的 `.mini-agent/skills/`）。
- **不**内置任何 skill：MVP 阶段产品自身不附带任何 skill，仅提供加载机制。
- **不**提供 skill 包管理命令（如 `install` / `update`）。用户必须手动将 skill 目录放置到上述两个位置之一。

## 11.6 能力边界

### 11.6.1 可执行脚本

Skill **可以**包含可执行脚本（位于 `scripts/` 子目录）。脚本的执行：

- **完全复用现有 `bash` 工具**：模型在 SKILL.md 指引下使用 `bash` 工具调用脚本。
- 因此 skill 脚本**天然受 `bash` 工具的全部权限约束**：
  - **内置硬黑名单**对 skill 脚本同样生效（即使在 `--yes` 模式下也会拦截危险操作）。
  - **四种权限模式**对 skill 脚本生效：
    - `--plan`：禁止执行 skill 脚本（与禁止 bash 一致）
    - 默认：每次执行需用户确认（与默认 bash 行为一致）
    - `--auto-edit`：仍需确认（与默认 bash 行为一致）
    - `--yes`：免确认（硬黑名单除外）
  - **白黑名单**（命令级 / 路径级 / 工具级）对 skill 脚本生效。

### 11.6.2 不允许的能力

Skill **不可**：

- 定义子 agent / 角色化 agent（`07-out-of-scope.md` 仍排除角色化预定义子 agent）。
- 定义新工具（`07-out-of-scope.md` 仍排除运行时工具扩展）。
- 自定义新的斜杠命令。
- 修改系统 Prompt 或固定行为（仅注入对话上下文）。

## 11.7 与其他模块的交互

### 11.7.1 与 `AGENTS.md`

- 二者并存：`AGENTS.md` 在会话启动时永久注入；skill 在模型按需调用时临时注入。
- 二者不冲突：`AGENTS.md` 描述项目级约定，skill 描述领域 SOP。

### 11.7.2 与子 agent (`task`)

- 子 agent 派生时**继承父 agent 已加载的 skills**：父 agent 已通过 `skill_tool` 加载到上下文的 skill 内容，会随上下文一起传递给子 agent。
- 子 agent 可以独立调用 `skill_tool` 加载新的 skill。

### 11.7.3 与 Trace 日志

- skill 加载事件**必须**写入 trace（事件包含 skill 名、加载时机、来源路径）。
- skill 内部脚本执行**复用 `bash` 工具的 trace 路径**，自然进入 trace。

### 11.7.4 与上下文压缩

- skill 注入到上下文的内容**视同普通 system 消息**，可被压缩策略摘要或截断。
- 压缩之后系统**必须重新注入**启动时的 skill 列表（`name + description`），保证模型在压缩后仍能感知可用 skill 集合。
- 已加载的具体 SKILL.md 内容若被压缩，模型可在需要时重新调用 `skill_tool` 加载。

## 11.8 CLI 交互

新增唯一斜杠命令：

| 命令 | 行为 |
|---|---|
| `/skill <name>` | 用户显式触发：等价于在下一条 user message 前注入"请使用 `<name>` skill"提示，让模型加载该 skill 后再继续 |

不引入：

- `/skill list` / `/skills`（启动时注入的 description 列表已可见）
- `/skill info <name>`
- `/skill reload`（用户新增 skill 后需重启 mini-agent；不在 MVP 提供热加载）

## 11.9 Web UI 交互

Web UI **仅**提供以下 skill 相关功能（列入 P2 阶段）：

- **Skill 详情查看面板**：列出当前可用 skill 与各自 description；点击查看具体 skill 的 SKILL.md 内容预览。
- **不**提供 skill 编辑、删除、安装等管理功能。
- **不**提供 skill 加载状态指示（加载行为通过 Trace 面板观察即可）。

## 11.10 阶段归属

- **Skill 加载机制**（`skill_tool`、启动时列表注入、`/skill <name>` 命令、扫描两个查找位置、压缩后重新注入）：**P1 阶段**。
- **Web UI Skill 详情面板**：**P2 阶段**（与 Web UI 同步）。

## 11.11 不在范围内

以下能力明确**不实现**，与 `07-out-of-scope.md` 保持一致：

- Skill 包管理命令（install / update / remove）
- Skill 热加载（增删改后无需重启）
- Skill 内置发布
- Skill 通过自身定义新工具或新斜杠命令
- Skill 通过自身定义角色化子 agent
- 用户级 skill 与项目级 skill 的递归向上查找
