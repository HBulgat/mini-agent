# 4. 工具最小完备集合

本章给出 mini-agent 的工具总览：清单、阶段归属、权限分类、权限模式 × 工具矩阵。具体工具的入参 schema、出参结构、错误语义留待 R7 详定。

## 4.1 工具清单

| 工具 | 阶段 | 权限分类 | 关键约束 |
|---|---|---|---|
| `read_file` | P0 | 只读 | 支持 offset/limit 分页 |
| `write_file` | P0 | 写 | 覆盖写 |
| `edit_file` | P0 | 写 | **要求 old_str 在文件中唯一出现**，否则报错 |
| `list_dir` | P0 | 只读 | 支持忽略 pattern |
| `delete_file` | P0 | 写 | 受路径黑名单约束 |
| `grep` | P0 | 只读 | 基于 ripgrep 语义 |
| `glob` | P0 | 只读 | 文件名 pattern |
| `bash` | P0 | 执行 | 含 timeout；硬黑名单生效 |
| `ask_user` | **P0** | 元工具 | 通过 `uio.Prompter.AskUser` 询问用户 |
| `write_plan` | P1 | 元工具 | **全量覆盖语义**：每次调用传完整 todo 列表 |
| `task` | P1 | 元工具 | 派生子 agent；嵌套深度上限 1 |
| `skill_tool` | P1 | 只读元工具 | 加载 SKILL.md 到上下文 |
| `web_fetch` | P1 | 网络 | 抓网页转 markdown |
| `web_search` | P2 | 网络 | 搜索引擎 |

## 4.2 权限分类的定义

| 分类 | 说明 | 包含工具 |
|---|---|---|
| **只读** | 仅读取信息，不修改文件系统、不执行外部进程、不发起网络请求 | `read_file` / `list_dir` / `grep` / `glob` / `skill_tool` |
| **写** | 修改文件系统（创建、覆盖、删除文件） | `write_file` / `edit_file` / `delete_file` |
| **执行** | 通过 shell 执行外部进程 | `bash` |
| **网络** | 发起对外网络请求 | `web_fetch` / `web_search` |
| **元工具** | 不直接产生外部副作用，但影响 agent 内部状态或与用户交互 | `write_plan` / `task` / `ask_user` |

## 4.3 权限模式 × 工具矩阵

| 工具 | 默认 | `--auto-edit` | `--yes` | `--plan` |
|---|---|---|---|---|
| `read_file` | ✅ 免确认 | ✅ | ✅ | ✅ |
| `list_dir` | ✅ | ✅ | ✅ | ✅ |
| `grep` | ✅ | ✅ | ✅ | ✅ |
| `glob` | ✅ | ✅ | ✅ | ✅ |
| `skill_tool` | ✅ | ✅ | ✅ | ✅ |
| `write_file` | ⚠️ 询问 | ✅ 免确认 | ✅ | ❌ 拒绝 |
| `edit_file` | ⚠️ | ✅ | ✅ | ❌ |
| `delete_file` | ⚠️ | ✅ | ✅ | ❌ |
| `bash` | ⚠️ | ⚠️ | ✅（硬黑名单除外） | ❌ |
| `web_fetch` | ⚠️ | ⚠️ | ✅ | ❌（网络访问视同执行） |
| `web_search` | ⚠️ | ⚠️ | ✅ | ❌ |
| `write_plan` | ✅ | ✅ | ✅ | ✅ |
| `task` | ✅ | ✅ | ✅ | ✅ ★ |
| `ask_user` | ✅ | ✅ | ✅ | ✅ |

★ 注：`task` 工具本身在 `--plan` 下允许调用，但**子 agent 可调用的工具同样受 `--plan` 模式约束**——即子 agent 在 plan 模式下也只能调用只读工具与元工具，不能写文件 / 执行 shell / 网络访问。

## 4.4 硬黑名单

`bash` 工具的硬黑名单**即使在 `--yes` 模式下也必须拒绝**，且不可被用户配置覆盖。具体清单留待 R7 详定，至少覆盖：

- 递归删除根目录或用户家目录的危险命令（如 `rm -rf /`、`rm -rf ~`、`rm -rf $HOME`）
- Fork 炸弹（如 `:(){:|:&};:`）
- 写入或修改系统级关键路径（如 `/etc/passwd`、`/etc/sudoers`）
- 改变系统启动项（`/etc/init.d`、`launchctl` 写系统服务等）

Skill 脚本通过 `bash` 工具执行时同样受硬黑名单管控（详见需求文档 `04-permissions.md` §4.6）。

## 4.5 白黑名单粒度

需求文档 §4.2 要求支持三种白黑名单粒度，工具实现层面对应如下：

| 粒度 | 应用对象 | 工具 |
|---|---|---|
| 命令级 | 命令字符串 / 命令模板 | `bash` |
| 路径级 | 文件路径 / 路径前缀 | `write_file` / `edit_file` / `delete_file` / `read_file`（可选） |
| 工具级 | 整个工具是否启用 | 所有工具 |

规则文件格式与默认路径留待 R4 详定。

## 4.6 已被排除的工具

需求层与本设计阶段已明确**不做**的工具（保持与 `07-out-of-scope.md` 一致）：

- `read_lints`：不做 IDE 集成
- `apply_patch`（多文件 diff 一次应用）：`edit_file` 单点替换够用
- `finish` 工具：循环依赖自然结束 / 步数上限 / 用户中断
- `list_skills`：B4 已在系统提示词注入 skill 列表
- `read_skill_resource`：模型通过现有 `read_file` / `bash` 工具按需访问 skill 资源
- `run_skill_script`：脚本执行复用 `bash`

## 4.7 留待后续轮次确定的细节

| 议题 | 轮次 |
|---|---|
| 每个工具的入参 JSON Schema | R7 |
| 每个工具的出参结构 | R7 |
| 错误码与错误消息约定 | R7 |
| 工具失败的"换方式"提示文案 | R7 |
| 硬黑名单的完整规则集 | R7 |
| 白黑名单文件格式（YAML 字段） | R4 |
| `task` 工具的 input 参数（子 agent 入口 prompt 形式） | R6 / R7 |
| `web_fetch` 的网页 → markdown 转换策略 | R7（P1 才需要） |

## 4.8 待 R9 设计的斜杠命令补充

R3 引入思考模式与可见性两个维度后，CLI 需要新增以下斜杠命令（具体语义、缩写、帮助文案在 R9 详定）：

| 命令 | 用途 | 默认状态 | 来源 |
|---|---|---|---|
| `/thinking on/off` | 切换 CLI 是否实时显示模型思考 token | off（默认隐藏） | R3 思考模式 |
| `/show-hidden on/off` | 切换是否显示 UserVisibility=hidden 的消息（如 `/skill` 注入提示、skill_tool 全文） | off | R3 用户可见性 |
| `/show-system on/off` | 切换是否显示 UserVisibility=system 的消息（系统 prompt、AGENTS.md 等） | off | R3 用户可见性 |
| `/show-archived on/off` | 切换是否显示 Visibility=archived 的归档消息 | off | R3 LLM 可见性 |

这些命令属于产品内置（不属于"用户级斜杠命令扩展"——后者已在 `07-out-of-scope.md` 排除）。
