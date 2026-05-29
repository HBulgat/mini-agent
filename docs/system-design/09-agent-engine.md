# 9. Agent 执行引擎细化（R6）

> Status: ✅ R6 已锁定（2026-05-24）
> 范围：ReAct 系统 prompt 文案、AGENTS.md 与 skill 列表注入模板、失败计数器算法、多 tool_calls 执行策略、子 agent 失败格式化、跨 provider 切换 summary、ctx 取消语义、中断时 tool_use ↔ tool_result 配对处理
> 关联：填充 [`05-core-abstractions.md`](05-core-abstractions.md) §5.9 (`agent`) 中的所有"留待 R6"项；与 [`07-config-and-rules.md`](07-config-and-rules.md) AGENTS.md 加载契约配合；与 [`08-llm-providers.md`](08-llm-providers.md) §8.11.4 跨 provider 切换流程衔接

---

## 9.1 文件位置

```
internal/agent/
├── agent.go                  # Runner / Loop / Spawner（接口与构造，已在 §5.9）
├── loop.go                   # ReAct 主循环
├── exec.go                   # 单 tool_call 执行 + 多 tool_calls 分桶并行
├── failure_counter.go        # sync.Map 实现
├── signature.go              # canonicalJSON + sha256 签名
├── prompts.go                # PromptBuilder（embed + template 渲染）
├── prompts/
│   └── system.md             # go:embed 的内置系统 prompt
├── compaction_trigger.go     # maybeCompact（已在 §5.9.9，R6 含 archiveIDs/summaries 收集细节）
├── model_switch.go           # 跨 provider 切换 + 自动归档思考块（D62 配套，与 §8.11.4 一致）
└── interrupt.go              # ctx 取消时的 tool_use ↔ tool_result 配对收尾
```

---

## 9.2 内置系统 prompt（`prompts/system.md`）

`go:embed` 内嵌；启动时通过 `text/template` 渲染环境变量后注入。

```markdown
You are mini-agent, an AI coding assistant operating in a local workspace.

## How to work
- Use the provided tools to read, search, edit, and execute commands.
- Prefer reading and exploring before making changes.
- After modifying files, briefly verify the change.
- When unclear about user intent, use the `ask_user` tool to clarify.

## Safety
- Never run destructive commands (mass deletions, force pushes, system files).
- Respect the user's permission mode; if a tool call is denied, propose an alternative.
- Do not fabricate file contents — read with `read_file` first.

## Environment
- cwd: {{.Cwd}}
- os: {{.OS}}
- time: {{.Time}}
```

设计约束（D49–D51）：
- 极简（约 110 词）：仅角色 + tool 使用 + 安全提示
- **不引导** todo / write_plan / task 工作流——让模型自行决定
- 环境信息最小集：cwd + OS + ISO 8601 时间（含时区）；不含 shell / Go version / 用户名
- **不支持配置文件覆盖**——保持行为一致性；改文案需要重编译

---

## 9.3 PromptBuilder

```go
// internal/agent/prompts.go
package agent

import (
    "bytes"
    _ "embed"
    "runtime"
    "text/template"
    "time"

    "mini-agent/internal/llm"
)

//go:embed prompts/system.md
var systemPromptTemplate string

type PromptBuilder struct {
    tpl *template.Template
}

func NewPromptBuilder() (*PromptBuilder, error) {
    tpl, err := template.New("system").Parse(systemPromptTemplate)
    if err != nil { return nil, err }
    return &PromptBuilder{tpl: tpl}, nil
}

// BuildSystem 渲染内置系统 prompt（D54 第 1 条）
func (b *PromptBuilder) BuildSystem(cwd string) (string, error) {
    var buf bytes.Buffer
    err := b.tpl.Execute(&buf, struct {
        Cwd  string
        OS   string
        Time string
    }{
        Cwd:  cwd,
        OS:   runtime.GOOS,
        Time: time.Now().Format(time.RFC3339),
    })
    if err != nil { return "", err }
    return buf.String(), nil
}

// BuildSkillList 渲染 skill 列表（D54 第 2 条）；空列表返回 ""
func BuildSkillList(skills []*Skill) string {
    if len(skills) == 0 { return "" }
    var buf bytes.Buffer
    buf.WriteString("## Available Skills\n\n")
    buf.WriteString("The following skills are available. To activate a skill (load its full instructions into context), use the `skill` tool with the skill name.\n\n")
    for _, s := range skills {
        fmt.Fprintf(&buf, "- **%s**: %s\n", s.Name, s.Description)
    }
    buf.WriteString("\nIf no skill matches the task, proceed without loading any skill.\n")
    return buf.String()
}

// BuildAgentsMD 用 D52 标签包裹（D54 第 3 条）；空内容返回 ""
func BuildAgentsMD(content string) string {
    if content == "" { return "" }
    return "<project_guidelines>\n" + content + "\n</project_guidelines>"
}
```

---

## 9.4 `prepareInitialHistory` 实化

D54/D55：3 条独立 system 消息；不持久化原文，每次启动即时生成。

```go
// internal/agent/loop.go
func (l *Loop) prepareInitialHistory(ctx context.Context, in *RunInput) ([]*llm.Message, error) {
    cwd := getCwdFromCtx(ctx) // 从 SessionService 注入
    history := make([]*llm.Message, 0, 8)

    // [1] 内置系统 prompt
    sys, err := l.prompts.BuildSystem(cwd)
    if err != nil { return nil, err }
    history = append(history, newSystemMsg(sys))

    // [2] skill 列表
    skills, err := l.skillLoader.List(ctx)
    if err != nil { return nil, err }
    if skillText := BuildSkillList(skills); skillText != "" {
        history = append(history, newSystemMsg(skillText))
    }

    // [3] AGENTS.md
    md, err := l.agentsmdLoader.Load(ctx, cwd)
    if err != nil { /* warn 并继续，不阻塞 */ }
    if mdWrapped := BuildAgentsMD(md); mdWrapped != "" {
        history = append(history, newSystemMsg(mdWrapped))
    }

    // 恢复 session：追加所有 Visibility ∈ {live, summary} 的非 system 持久化消息
    if in.IsResume {
        persisted, err := l.sessRepo.ListLiveMessages(ctx, in.SessionID)
        if err != nil { return nil, err }
        for _, m := range persisted {
            if m.Role == session.RoleSystem { continue } // R6/D55：system 不持久化，跳过
            history = append(history, m.ToLLM())
        }
    }

    // 当前 user 输入
    history = append(history, newUserMsg(in.UserMessage))
    return history, nil
}

func newSystemMsg(text string) *llm.Message {
    return &llm.Message{
        Role:    llm.RoleSystem,
        Content: []llm.ContentBlock{{Type: llm.BlockText, Text: text}},
    }
}
```

**Anthropic Codec 合并约定**（D54 配套）：
- Anthropic API 顶层只接受单一 system 字段
- Codec 在 `canonicalToAPIRequest` 时遍历所有 `Role=System` 消息，按 `\n\n---\n\n` 拼接为一个字符串，写入顶层 `system` 字段
- 拼接顺序按 history 中出现顺序

---

## 9.5 失败计数器（D56 + D57）

### 9.5.1 Signature 算法

```go
// internal/agent/signature.go
package agent

import (
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "sort"
)

// canonicalJSON 把 map 按 key 排序后 marshal；保证 args 顺序无关
func canonicalJSON(v any) ([]byte, error) {
    return marshalCanonical(v)
}

func marshalCanonical(v any) ([]byte, error) {
    switch x := v.(type) {
    case map[string]any:
        keys := make([]string, 0, len(x))
        for k := range x { keys = append(keys, k) }
        sort.Strings(keys)
        var buf bytes.Buffer
        buf.WriteByte('{')
        for i, k := range keys {
            if i > 0 { buf.WriteByte(',') }
            kb, _ := json.Marshal(k)
            buf.Write(kb)
            buf.WriteByte(':')
            vb, err := marshalCanonical(x[k])
            if err != nil { return nil, err }
            buf.Write(vb)
        }
        buf.WriteByte('}')
        return buf.Bytes(), nil
    case []any:
        var buf bytes.Buffer
        buf.WriteByte('[')
        for i, e := range x {
            if i > 0 { buf.WriteByte(',') }
            eb, err := marshalCanonical(e)
            if err != nil { return nil, err }
            buf.Write(eb)
        }
        buf.WriteByte(']')
        return buf.Bytes(), nil
    default:
        return json.Marshal(v)
    }
}

// signature D56：tool_name + ":" + sha256(canonicalJSON(args))[:16]
func signature(toolName string, args map[string]any) string {
    cj, err := canonicalJSON(args)
    if err != nil { return toolName + ":<unhashable>" }
    sum := sha256.Sum256(cj)
    return toolName + ":" + hex.EncodeToString(sum[:8]) // [:8] 字节 = 16 hex 字符
}
```

设计约束：
- **不规范化路径**（D56 + C2 决定）：`./go.mod` 与 `go.mod` 不同 signature；模型换写法即重置计数（视为"换方式"）
- **canonicalJSON 仅做 map key 排序**：递归处理嵌套 map / array；其它类型走标准 `json.Marshal`
- **取 sha256 前 8 字节（16 hex）**：碰撞概率足够低；签名短便于 trace 字段

### 9.5.2 失败计数器实现

```go
// internal/agent/failure_counter.go
package agent

import "sync"

type failureCounter struct {
    m sync.Map // key: string(signature) → value: *atomic.Int64
}

func newFailureCounter() *failureCounter { return &failureCounter{} }

// Increment 返回递增后的失败次数
func (c *failureCounter) Increment(sig string) int {
    v, _ := c.m.LoadOrStore(sig, new(atomic.Int64))
    return int(v.(*atomic.Int64).Add(1))
}

// Reset 工具成功 / signature 改变后清零
func (c *failureCounter) Reset(sig string) {
    c.m.Delete(sig)
}
```

**重要约束**（D66）：失败计数器在子 agent 与主 agent 之间**不共享**——`Spawner.Spawn` 时新建一个 `*Loop`，`failureCounter` 是 Loop 字段，自然不共享。子 agent 是独立任务上下文，重置计数符合直觉。

**权限拒绝不计入**（与 R2 §5.5.5 一致）：`executeToolCall` 在 `Decision in {Deny, DenyHard}` 路径直接返回，不调用 `failureCounter.Increment`。

---

## 9.6 多 tool_calls 执行策略（D58 + D59）

### 9.6.1 Category 分桶逻辑

```go
// internal/agent/exec.go
func (l *Loop) executeToolCalls(
    ctx context.Context,
    calls []*llm.ContentBlock, // BlockToolUse 块
    in *RunInput,
    fc *failureCounter,
) ([]*llm.Message, error) {
    // 按出现顺序保留索引；同时分桶
    type indexed struct {
        idx  int
        call *llm.ContentBlock
    }
    var readonly, sequential []indexed
    for i, c := range calls {
        tl, ok := l.registry.Get(c.ToolName)
        if !ok || tl.Category() == tool.CategoryReadOnly {
            readonly = append(readonly, indexed{i, c})
        } else {
            sequential = append(sequential, indexed{i, c})
        }
    }

    results := make([]*llm.Message, len(calls)) // 按原 idx 填回

    // 第 1 批：readonly 并行（errgroup，但不取消兄弟）
    if len(readonly) > 0 {
        var wg sync.WaitGroup
        for _, item := range readonly {
            wg.Add(1)
            go func(it indexed) {
                defer wg.Done()
                results[it.idx] = l.executeToolCall(ctx, it.call, in, fc)
            }(item)
        }
        wg.Wait()
    }

    // 第 2 批：其它串行（并继承前批的中断状态）
    for _, item := range sequential {
        if err := ctx.Err(); err != nil {
            // 后续未执行的 tool_call 合成 interrupted tool_result（D64）
            for j := item.idx; j < len(calls); j++ {
                if results[j] == nil {
                    results[j] = synthesizeInterruptedResult(calls[j])
                }
            }
            return results, err
        }
        results[item.idx] = l.executeToolCall(ctx, item.call, in, fc)
    }
    return results, nil
}
```

设计要点：
- **第 1 批 readonly 并行**：用 `sync.WaitGroup`（不是 `errgroup`），**任一失败不取消兄弟**——D59 明确"各自独立完成；所有结果都返给模型"
- **第 2 批顺序保持原 idx**：写回的 history 顺序与模型发出的 tool_call 顺序一致；OpenAI 规范要求 tool_result 顺序与 tool_call_id 配对
- **并行批先执行、串行批后执行**：避免"写文件后再读文件"出现竞态时的不可预测性
- **ctx 取消颗粒**（D63）：每个工具 `Invoke(ctx, ...)` 自己响应；exec 层在批切换时检查 `ctx.Err()` 提前退出
- **失败计数器 sync.Map（D57）**：天然支持并发安全的 Increment/Reset

### 9.6.2 单 tool_call 执行（替换 R2 §5.9.5 伪代码）

```go
func (l *Loop) executeToolCall(
    ctx context.Context,
    call *llm.ContentBlock,
    in *RunInput,
    fc *failureCounter,
) *llm.Message {
    tl, ok := l.registry.Get(call.ToolName)
    if !ok { return toolErrMsg(call, "unknown tool: "+call.ToolName) }

    // ToolInput 已是 map[string]any（D34 双轨：Final 时 Provider 已 unmarshal）
    args := call.ToolInput

    op := buildOperation(call, tl, args)
    decision, err := l.permGate.Check(ctx, op, l.permGate.GetMode(), in.Prompter)
    if err != nil { return toolErrMsg(call, err.Error()) }

    switch decision.Decision {
    case permission.DecisionDenyHard:
        return toolErrMsg(call, "blocked by hard denylist: "+decision.Reason)
    case permission.DecisionDeny:
        return toolErrMsg(call, "permission denied: "+decision.Reason)
    }

    in.Sink.EmitToolCallStart(uio.ToolCallStartEvent{
        CallID: call.ToolUseID, Name: call.ToolName, Args: args, StartAt: time.Now(),
    })
    start := time.Now()
    result, invErr := tl.Invoke(ctx, args)
    elapsed := time.Since(start)
    in.Sink.EmitToolCallEnd(uio.ToolCallEndEvent{
        CallID: call.ToolUseID, Name: call.ToolName,
        Succeeded: invErr == nil, Display: result.Display, Err: invErr, Duration: elapsed,
    })

    sig := signature(call.ToolName, args)
    if invErr != nil {
        n := fc.Increment(sig)
        msg := invErr.Error()
        if n >= l.cfg.ToolRetryMax {
            msg += "\n\n[Note] This approach has failed " + strconv.Itoa(n) +
                " times in a row — please try a different way."
        }
        return toolResultMsg(call, msg, true /*isError*/)
    }
    fc.Reset(sig)
    return toolResultMsg(call, result.Content, false)
}

// toolResultMsg 构造 user + ToolResultBlock（canonical 形式，见 D45/F3）
func toolResultMsg(call *llm.ContentBlock, output string, isError bool) *llm.Message {
    return &llm.Message{
        Role: llm.RoleUser,
        Content: []llm.ContentBlock{{
            Type:         llm.BlockToolResult,
            ToolUseRefID: call.ToolUseID,
            Output:       output,
            IsError:      isError,
        }},
    }
}
```

---

## 9.7 中断时的 tool_use ↔ tool_result 配对（D64）

### 9.7.1 问题背景

OpenAI 与 Anthropic 协议都有硬约束：
- 一条 assistant 消息中的每个 `tool_use` 块**必须**在后续历史中有对应的 `tool_result`
- 否则下一轮请求会被服务端拒绝（Anthropic 返回 400；OpenAI 行为不一致但通常报错）

中断场景（用户 Ctrl+C / Web UI 取消）：模型已发出 N 个 tool_call，但只执行了前 K 个就被中断，剩余 N-K 个没执行——**必须为它们合成一条占位 tool_result**。

### 9.7.2 合成函数

```go
// internal/agent/interrupt.go
func synthesizeInterruptedResult(call *llm.ContentBlock) *llm.Message {
    return &llm.Message{
        Role: llm.RoleUser,
        Content: []llm.ContentBlock{{
            Type:         llm.BlockToolResult,
            ToolUseRefID: call.ToolUseID,
            Output:       "[interrupted before tool was invoked]",
            IsError:      true,
        }},
    }
}
```

### 9.7.3 Loop 主循环中断处理

```go
// internal/agent/loop.go (Run 主循环节选)
events, err := l.provider.Stream(ctx, llm.Request{Messages: history, Tools: ...})
if err != nil { return errResult(err) }

msg, usage, _, err := l.consumeStream(ctx, events, in.Sink)
if err != nil { /* StreamError 路径 */ }

// 持久化 assistant 消息
l.sessRepo.AppendMessage(ctx, session.FromLLM(in.SessionID, nextSeq(), msg, session.WithUserVisibility(session.UserVisible)))
history = append(history, msg)

toolCalls := extractToolUseBlocks(msg)
if len(toolCalls) == 0 {
    return RunResult{StopReason: StopEndTurn, Steps: step, Usage: totalUsage}, nil
}

results, err := l.executeToolCalls(ctx, toolCalls, in, failureCounter)
// 即使 err = ctx.Canceled，results 中已包含部分真实结果 + 部分合成 interrupted（来自 §9.6.1）
for _, r := range results {
    history = append(history, r)
    l.sessRepo.AppendMessage(ctx, session.FromLLM(in.SessionID, nextSeq(), r))
}
if errors.Is(err, context.Canceled) {
    return RunResult{StopReason: StopInterrupted, Steps: step, Usage: totalUsage}, nil
}
```

设计要点：
- 即使中断也**先把 results 全部写进 history 与持久化**，再返回 `StopInterrupted`
- 这样下次 `IsResume` 启动时 `ListLiveMessages` 拿到的历史是协议合法的（每个 tool_use 都有 tool_result）

---

## 9.8 子 agent 失败 / 半成功格式化（D60 + D61）

### 9.8.1 模板

子 agent 完成（无论成功/失败/半成功）时，task 工具的 tool_result 内容用以下结构化模板：

```
Sub-agent stopped: {stop_reason}
Steps completed: {steps}/{max_steps}
Side effects: {N file(s) modified, M command(s) executed}
Last assistant output: {trimmed_last_msg}
{if error}
Error: {error_message}
{end if}
```

字段语义：

| 字段 | 来源 |
|---|---|
| `stop_reason` | `SubOutput.StopReason`（end_turn / max_steps / interrupted / error）|
| `steps` | `SubOutput.Steps` |
| `max_steps` | `cfg.MaxSteps` |
| `Side effects` | 从 trace 聚合：扫描 `TypeToolCallEnd` 事件中 Category=Write/Execute 的成功调用计数 |
| `trimmed_last_msg` | 子 agent 最后一条 user-visible assistant 消息的文本，截断到 500 字符（不足不补）|
| `error_message` | `SubOutput.Err.Error()`；nil 时省略整行 |

### 9.8.2 实现位置

```go
// internal/tool/task/format.go
func formatSubAgentResult(out *agent.SubOutput, recorder trace.Recorder) string {
    var buf bytes.Buffer
    fmt.Fprintf(&buf, "Sub-agent stopped: %s\n", out.StopReason)
    fmt.Fprintf(&buf, "Steps completed: %d/%d\n", out.Steps, out.MaxSteps)

    sideEffects := summarizeSideEffects(recorder, out.TraceID)
    fmt.Fprintf(&buf, "Side effects: %s\n", sideEffects)

    lastMsg := truncate(out.LastAssistantMsg, 500)
    if lastMsg != "" {
        fmt.Fprintf(&buf, "Last assistant output: %s\n", lastMsg)
    }
    if out.Err != nil {
        fmt.Fprintf(&buf, "Error: %s\n", out.Err.Error())
    }
    return buf.String()
}

func summarizeSideEffects(rec trace.Recorder, traceID trace.TraceID) string {
    // 实现期：从 trace 查询 traceID 子树下的 ToolCallEnd 事件，
    // 按 Category (Write/Execute) 聚合成功次数
    // P0 简化：先用 SubOutput 字段直接传（让 sub Loop 自己累计），不走 trace 查询
    return "<aggregated from sub agent trace>"
}
```

> **简化策略**：P0 阶段 `SubOutput` 直接增加 `WriteCount` / `ExecCount` 字段由 sub Loop 自累计，避免 trace 反查。R12 完整 trace schema 落地后可改成查 trace。

### 9.8.3 SubOutput 扩充

```go
// internal/agent/agent.go
type SubOutput struct {
    Result            string  // 改名为 last_assistant_msg 更准确（P0 保留 Result 兼容）
    LastAssistantMsg  string  // R6 新增：最后一条 user-visible assistant 文本（500 截断前的原文）
    StopReason        StopReason
    Steps             int
    MaxSteps          int     // R6 新增
    Usage             llm.Usage
    WriteCount        int     // R6 新增：副作用统计（Write 类工具成功调用数）
    ExecCount         int     // R6 新增：副作用统计（Execute 类工具成功调用数）
    Err               error
    TraceID           trace.TraceID  // R6 新增：用于 trace 反查（P0 不用）
}
```

---

## 9.9 跨 Provider 切换 summary 文案（D62）

`internal/agent/model_switch.go`（与 §8.11.4 衔接）插入的 system 消息内容模板：

```go
// internal/agent/model_switch.go
const crossProviderSwitchTemplate =
    "[Model switched from %s to %s; %d previous assistant messages with reasoning blocks were archived to maintain protocol compatibility.]"

func buildCrossSwitchNotice(oldProvider, newProvider string, archivedCount int) string {
    return fmt.Sprintf(crossProviderSwitchTemplate, oldProvider, newProvider, archivedCount)
}
```

ApplyCompaction 时插入的 summary 消息：
- `Role: system`
- `Content: [{Type: BlockText, Text: <上述模板渲染结果>}]`
- **`UserVisibility: visible`**（D62 + F2：让用户在对话流中直接看到反馈）
- `OriginalIDs: [被归档的 N 条 message ID]`

---

## 9.10 ctx 取消语义（D63 完整契约）

| 层级 | 行为 |
|---|---|
| **Loop 主循环** | 每次进入 step 前检查 `ctx.Err()`；流式响应通过 channel 关闭中断；多 tool_calls 批切换时检查 |
| **Provider.Stream** | 内部 `network.WithRetry` 接 ctx；channel 在 ctx 取消时立即关闭（D44）|
| **tool.Invoke(ctx, ...)** | **所有工具必须真正响应 ctx**（R7 强制约束）：<br>• read_file / list_dir / grep：用 `os` 包的标准 IO（自动响应）<br>• bash：`exec.CommandContext`，杀进程组（SIGKILL）<br>• ask_user：`Prompter.AskUser` 接 ctx，自然中断<br>• task（子 agent）：透传 ctx 给子 Loop |
| **permission.Gate.Check** | `Prompter.AskApproval` 接 ctx；ctx 取消时返回 `Decision=Deny + Reason="interrupted"` |
| **session.Repository** | 所有方法接 ctx；ctx 取消时停止数据库操作并返回 ctx.Err() |

**约束传导**：R7 编写每个工具的 schema 时，必须在"ctx 响应方式"一栏明确列出。

---

## 9.11 子 agent 工具集（D65）

子 agent 拥有**与主 agent 完全相同的工具集**（包括 `task` 工具自身）。

```go
// internal/agent/spawn.go
func (s *Spawner) Spawn(parentCtx context.Context, in *SubInput) (*SubOutput, error) {
    childCtx := WithDepth(parentCtx, DepthFrom(parentCtx)+1)

    childLoop := New(s.deps, s.cfg)  // 新建一个 Loop（共享 Deps 中的所有依赖）
    // failureCounter 是 Loop 字段，自然不共享（D66）
    // ToolRegistry 共享同一实例：子 agent 看到全部工具

    return childLoop.Run(childCtx, &RunInput{
        SessionID: in.SessionID,
        UserMessage: in.Prompt,
        Sink: in.Sink, Prompter: in.Prompter,
        IsResume: false,  // 子 agent 总是从空 history 开始（仅父级 inheritedSkills 由 prepareInitialHistory 处理）
    })
}
```

`task` 工具自身在 `Invoke` 入口检查嵌套深度：

```go
// internal/tool/task/task.go
func (t *TaskTool) Invoke(ctx context.Context, input map[string]any) (tool.Result, error) {
    if agent.DepthFrom(ctx) >= t.cfg.SubAgentDepthMax {
        return tool.Result{}, &tool.Error{
            Code:    tool.ErrPermissionDenied,
            Message: "sub-agent nesting depth exceeded (max=" + strconv.Itoa(t.cfg.SubAgentDepthMax) + ")",
        }
    }
    // ... 正常派生
}
```

---

## 9.12 R6 关键决定（D49–D67）

| 编号 | 决定 |
|---|---|
| **D49** | ReAct 系统 prompt 极简（约 200 字内）：仅角色 + tool 使用规范 + 安全提示；不引导 todo / write_plan / task 工作流；具体方法论交给 AGENTS.md 与 skill |
| **D50** | 系统 prompt 通过 `go:embed internal/agent/prompts/system.md` 内嵌；不支持配置文件覆盖 |
| **D51** | 系统 prompt 环境信息最小集：`cwd` + `os` + `time`（ISO 8601）；不含 shell / Go version / 用户名 |
| **D52** | AGENTS.md 注入用 `<project_guidelines>...</project_guidelines>` 标签包裹；明确边界便于压缩 |
| **D53** | AGENTS.md 截断（>1MB）后注入末尾追加 `[...truncated, X bytes total]` 让模型知情；写 trace 警告 |
| **D54** | `prepareInitialHistory` 拼装 3 条独立 system 消息：[1] 内置 prompt / [2] skill 列表 / [3] AGENTS.md；OpenAI 直接发多条；Anthropic Codec 在转换时按 `\n\n---\n\n` 拼接为顶层 system 字段 |
| **D55** | system 消息**不持久化**（保持 R3 一致）：每次启动按当前 cwd / skill / AGENTS.md / config 即时生成；恢复 session 时也即时重建 |
| **D56** | 失败计数器 signature 算法：`tool_name + ":" + sha256(canonicalJSON(args))[:16]`；canonicalJSON 仅做 map key 排序，不做路径规范化 |
| **D57** | 失败计数器存储用 `sync.Map`，value 为 `*atomic.Int64`；成功后立即 Delete；权限拒绝不计入 |
| **D58** | 多 tool_calls 执行：按 Category 分桶——`CategoryReadOnly` 工具用 `sync.WaitGroup` 并行；其它 Category 串行；并行批先于串行批 |
| **D59** | 并行 readonly 失败时各自独立完成（不取消兄弟）；所有结果（含失败）按原 tool_call 顺序写回 history |
| **D60** | 子 agent 失败 / 半成功 tool_result 用结构化文本：`stop_reason / steps_completed / side_effects / last_assistant_output / error`；side_effects 用 SubOutput.WriteCount + ExecCount 直传（P0 简化）|
| **D61** | 子 agent 部分成功视为半成功（不视为失败）：副作用保留；用 D60 同结构告知主 agent；主 agent 自行决定续做还是回滚 |
| **D62** | 跨 provider 切换 summary 模板：`[Model switched from {old} to {new}; {N} previous assistant messages with reasoning blocks were archived to maintain protocol compatibility.]`；UserVisibility=visible（D46/F2 配套）|
| **D63** | ctx 取消颗粒度到工具内部：所有 `tool.Invoke` 必须真正响应 ctx；R7 各工具 schema 同步约束 |
| **D64** | 中断时已发出但未执行的 tool_call：合成 tool_result `[interrupted before tool was invoked]` 与 tool_use_id 配对，保证 OpenAI/Anthropic 协议合法性 |
| **D65** | 子 agent 工具集 = 主 agent 全部工具（含 task）；嵌套深度由 task 工具自身在 Invoke 入口检查 `DepthFrom(ctx) >= SubAgentDepthMax` 时拒绝 |
| **D66** | 失败计数器在子 agent 与主 agent 之间不共享（各自 `*Loop` 独立持有 `*sync.Map`）|
| **D67** | Loop / Spawner receiver 全用 `*Loop` / `*Spawner`（D31）；`failureCounter` 作为 `*sync.Map` 字段；`prompts/system.md` 内容在 `NewPromptBuilder` 时一次性 embed 读入并解析 template，缓存 `*template.Template` 字段 |

---

## 9.13 留待后续轮次

| 议题 | 归属 |
|---|---|
| 每个工具的 ctx 响应实现细节（read_file / bash / ask_user 等）| R7 |
| `signature` 在多模态 args（含 base64 图片）下的性能问题 | 实现期评估 |
| `summarizeSideEffects` 走 trace 反查实现（P0 用 SubOutput 字段直传） | R12 落地后回头优化 |
| 跨 provider summary 文案的多语言版本 | 不在 P0 范围；如有需求重新走需求 |
| 系统 prompt 的多语言版本 | 同上 |
| 多 tool_calls 并行的资源限制（goroutine 泄漏 / 文件 IO 风暴）| 实现期 + 压测 |
