# 10. 工具实现模板与 read_file 详细设计（R7-1'）

> Status: ✅ R7-1' 已锁定（2026-05-24）
> 范围：用 `read_file` 一个工具走通"工具实现"的统一模板；其它 8 个 P0 工具 + 5 个 P1/P2 工具按本模板填空（schema 细节在各工具实现时单独锁定，不再走集中设计轮次）
> 关联：实化 [`05-core-abstractions.md`](05-core-abstractions.md) §5.4 (`tool`) 的接口；填实 [`04-tool-catalog.md`](04-tool-catalog.md) §4.7 留白；为 R6 §9.5 的 `failure_counter` 提供具体工具的失败语义参考
>
> **R7-1' 修订点**（对 R2 已锁定接口）：`tool.Result` 删除 `Truncated bool` 字段，新增 `UserLimited bool` + `ForcedTruncated bool` 两个字段（详见 §10.4 与对 05 的就地修订）

---

## 10.1 设计哲学

R7 不再为每个工具单独走"问答→落盘"流程。理由：

- 9 个 P0 工具有大量重复模式（参数解析、错误码、ctx 响应、Display 模板）
- 设计 1 个工具走通模板，其它 8 个按模板填空，开发速度反而更快
- 实现期遇到工具特有的边界条件（如 bash 命令解析）再单独评估

本章给出**所有工具必须遵守的 6 条统一规范** + `read_file` 的完整实现作为可复制范例。

---

## 10.2 6 条统一规范

### R1 · JSON Schema 风格

- **工具**：`github.com/invopop/jsonschema`（活跃维护、OpenAI 风格、struct tag 友好）
- **来源**：每个工具内部定义 `<ToolName>Args` typed struct，由库反射生成 schema
- **Tag 约定**：
  ```go
  type ReadFileArgs struct {
      Path     string `json:"path" jsonschema:"required,description=Absolute or relative path to read"`
      Offset   int    `json:"offset,omitempty" jsonschema:"description=Starting line (1-based); default 1"`
      Limit    int    `json:"limit,omitempty" jsonschema:"description=Max lines to read; default 200"`
      MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"description=Hard byte limit; default 200KB max 1MB"`
  }
  ```
- **必填**：`required` 由 jsonschema tag 标注
- **可选**：`,omitempty` + tag description 末尾以 `; default: X` 标明默认值
- **语言**：description 全部英文（与系统 prompt 一致）
- **描述详尽度**：长描述（含 when to use / when NOT to use / 注意事项）。极简描述容易让模型误用——参考 OpenAI/Anthropic 官方建议

### R2 · `tool.Result` 字段划分

| 字段 | 给谁看 | 内容 |
|---|---|---|
| `Content` | LLM | 纯结果数据 + 元信息标签包装 + 警告内嵌；详见 §10.4.4 |
| `Display` | 用户 UI（CLI/Web） | 简短摘要（一两行）+ 截断的短预览（前 5 行）|
| `UserLimited` | trace + UI | 用户主动传 limit/offset 限制了输出 |
| `ForcedTruncated` | trace + UI | 工具自身上限触发的强制截断（如 200KB） |

### R3 · 错误码

R2 §5.4.3 已定的 7 种 + R7 新增 2 种：

| 错误码 | 何时使用 |
|---|---|
| `ErrInvalidArgs` | 参数解析失败、必填缺失、值越界、二进制文件等"用户/模型输入错误" |
| `ErrPermissionDenied` | Gate 拒绝（含硬黑名单、用户黑名单、plan 模式拒绝）|
| `ErrNotFound` | 文件/路径/工具不存在 |
| `ErrIO` | OS 级 IO 错误（磁盘满、read failed、连接重置等）|
| `ErrTimeout` | `context.DeadlineExceeded`（强约定，§10.2 R5）|
| `ErrInterrupted` | `context.Canceled`（强约定，§10.2 R5）|
| `ErrToolInternal` | 工具实现 bug（panic recover 后转出）|
| **`ErrTooLarge`**（R7 新增）| 输出体积超工具自身上限（与 ForcedTruncated=true 同时返回）|
| **`ErrAmbiguous`**（R7 新增）| 多义性失败（如 edit_file 的 old_str 不唯一）|

### R4 · 错误消息语言与"换方式"提示

- 所有错误消息**全英文**（与系统 prompt 对齐）
- "换方式"提示文案（R6 D60 触发）：

  ```
  This approach has failed N times in a row — please try a different way.
  ```

  由 agent loop 在 tool_result content 末尾追加（不是工具自身职责）

### R5 · ctx 响应（强约定）

| 错误来源 | 错误码 |
|---|---|
| `errors.Is(err, context.Canceled)` | `ErrInterrupted` |
| `errors.Is(err, context.DeadlineExceeded)` | `ErrTimeout` |

工具内部 IO 必须真正响应 ctx（D63）：
- `os.ReadFile` 不接 ctx，但读循环可用 `for { select { case <-ctx.Done(): ...; default: f.Read(buf) } }` 显式检查
- 网络/外部进程必须用 `*WithContext` 系列 API
- 短同步操作（read 200KB 文件）可视为"自然完成快于 ctx 取消"，无需检查

工具如有特殊语义（例如把 DeadlineExceeded 当作部分成功而非错误），可在自身 doc.go 注释说明并 override；不写 doc.go 视为遵守强约定。

### R6 · 实现骨架（私有约定）

`Tool` interface 仍是 R2 已定的 5 个方法（`Name / Description / Schema / Category / Invoke`）。每个工具内部建议但不强制的两个私有方法：

```go
// 私有：解析 input 到 typed struct，捕获 JSON 反序列化错误
func (t *ReadFile) decodeArgs(input map[string]any) (*ReadFileArgs, error)

// 私有：业务级校验（路径非空、limit > 0 等）
func (t *ReadFile) validateArgs(args *ReadFileArgs) error
```

两个 helper 失败都返回 `ErrInvalidArgs`。

---

## 10.3 read_file 完整设计

### 10.3.1 入参

```go
package fs

type ReadFileArgs struct {
    Path     string `json:"path" jsonschema:"required,description=Absolute or relative path to a text file"`
    Offset   int    `json:"offset,omitempty" jsonschema:"description=Starting line (1-based); default 1"`
    Limit    int    `json:"limit,omitempty" jsonschema:"description=Max lines to read from offset; default 200"`
    MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"description=Hard cap on returned bytes; default 200000 (200 KB) max 1048576 (1 MB)"`
}
```

默认值：
- `offset = 1`（不传或 0 等价于 1）
- `limit = 200`
- `max_bytes = 200_000`，上限 `1_048_576`

### 10.3.2 行为契约

| 步骤 | 行为 | 错误码 |
|---|---|---|
| 1. decodeArgs | json 反序列化 input → ReadFileArgs | `ErrInvalidArgs` |
| 2. validateArgs | path 非空；offset/limit/max_bytes 范围合法（>=0；max_bytes <= 1MB）| `ErrInvalidArgs` |
| 3. 路径解析 | 相对路径 → 基于 session.Cwd 转绝对路径 | — |
| 4. 二进制探测 | 读前 8KB；含 NUL 字节 → 拒绝 | `ErrInvalidArgs`（消息指引用 bash + 工具）|
| 5. 全文件读取 | `os.ReadFile`（一次性；ctx 不显式检查）| `ErrIO` / `ErrNotFound` |
| 6. 按行切片 | 取 `lines[offset-1 : offset-1+limit]` | — |
| 7. 字节截断 | 拼成结果后超 `max_bytes` 截断尾部 | — |
| 8. 行号前缀 | 每行格式：`{lineno:>6}:{content}`（6 位右对齐 + 冒号 + 内容）| — |
| 9. 包装与警告 | §10.3.4 的 `<file>` 标签包装 + 截断警告附行 | — |

### 10.3.3 出参（Result）

```go
type Result struct {
    Content         string  // 模型可见
    Display         string  // 用户 UI 可见
    UserLimited     bool    // user 传 offset/limit 主动限制
    ForcedTruncated bool    // max_bytes / 二进制 / 文件实际行数 > limit 等
}
```

### 10.3.4 Content 格式

```
<file path="/abs/path/main.go" lines="10" total_lines="120" range="1-10" bytes="234">
     1:package main
     2:
     3:import "fmt"
     4:
     5:func main() {
     6:    fmt.Println("hello")
     7:}
     8:
     9:// some comment
    10:
</file>
```

如发生强制截断（行数被 limit 切 / max_bytes 被切），追加一行：

```
[warning: showing 200 of 1500 lines. Use offset/limit to read the rest.]
```

或：

```
[warning: byte limit reached at 200000; content truncated. Pass max_bytes or narrow range.]
```

### 10.3.5 Display 格式

```
read_file main.go (1.2 KB, 120 lines) → showing 1–10
```

如截断：

```
read_file big.log (12 MB, 50000 lines) → showing 1–200 [truncated]
```

### 10.3.6 实现骨架

```go
// internal/tool/fs/readfile.go
package fs

import (
    "bytes"
    "context"
    "errors"
    "fmt"
    "io/fs"
    "os"
    "strings"

    "github.com/invopop/jsonschema"

    "mini-agent/internal/tool"
)

const (
    defaultOffset    = 1
    defaultLimit     = 200
    defaultMaxBytes  = 200_000
    hardCapMaxBytes  = 1_048_576
    binaryProbeBytes = 8 * 1024
    lineNumberWidth  = 6
)

type ReadFile struct {
    cwd func() string  // 注入 session.Cwd（避免与 cwd 切换耦合）
}

func New(cwd func() string) *ReadFile {
    return &ReadFile{cwd: cwd}
}

func (r *ReadFile) Name() string { return "read_file" }

func (r *ReadFile) Description() string {
    return strings.TrimSpace(`
Read text file content from the local filesystem.

When to use:
- Inspect source code, configuration, documentation
- Verify file content before/after modification

When NOT to use:
- Reading binary files (use bash with file/xxd instead)
- Listing directory contents (use list_dir)
- Searching across files (use grep)

Notes:
- Output includes 1-based line number prefix in "%6d:" format
- Default reads first 200 lines; use offset/limit to paginate
- File-level hard cap is 1 MB (max_bytes)
`)
}

func (r *ReadFile) Schema() map[string]any {
    refl := jsonschema.Reflector{ExpandedStruct: true}
    return jsonSchemaToMap(refl.Reflect(&ReadFileArgs{}))
}

func (r *ReadFile) Category() tool.Category { return tool.CategoryReadOnly }

func (r *ReadFile) Invoke(ctx context.Context, input map[string]any) (tool.Result, error) {
    args, err := r.decodeArgs(input)
    if err != nil { return tool.Result{}, err }
    if err := r.validateArgs(args); err != nil { return tool.Result{}, err }

    abs := r.resolvePath(args.Path)

    // 二进制探测
    if isBinary, err := probeBinary(abs); err != nil {
        return tool.Result{}, mapIOError(err)
    } else if isBinary {
        return tool.Result{}, &tool.Error{
            Code: tool.ErrInvalidArgs,
            Message: "binary file is not supported; use bash with appropriate tools (e.g., file/xxd) instead",
        }
    }

    raw, err := os.ReadFile(abs)
    if err != nil { return tool.Result{}, mapIOError(err) }

    if err := ctx.Err(); err != nil { return tool.Result{}, mapCtxError(err) }

    lines := splitLines(raw)
    totalLines := len(lines)

    offset := args.Offset
    if offset == 0 { offset = defaultOffset }
    limit := args.Limit
    if limit == 0 { limit = defaultLimit }
    maxBytes := args.MaxBytes
    if maxBytes == 0 { maxBytes = defaultMaxBytes }
    if maxBytes > hardCapMaxBytes { maxBytes = hardCapMaxBytes }

    userLimited := args.Offset > 1 || (args.Limit > 0 && args.Limit < totalLines)

    end := offset - 1 + limit
    if end > totalLines { end = totalLines }
    selected := lines[offset-1 : end]

    body, byteTruncated := joinWithLineNumbers(selected, offset, maxBytes)
    forcedTruncated := byteTruncated || end < totalLines && !userLimited

    var sb strings.Builder
    fmt.Fprintf(&sb, `<file path=%q lines=%q total_lines=%q range=%q bytes=%q>`+"\n",
        abs, fmt.Sprint(end-offset+1), fmt.Sprint(totalLines),
        fmt.Sprintf("%d-%d", offset, end), fmt.Sprint(len(body)))
    sb.WriteString(body)
    sb.WriteString("</file>\n")
    if byteTruncated {
        fmt.Fprintf(&sb, "[warning: byte limit reached at %d; content truncated. Pass max_bytes or narrow range.]\n", maxBytes)
    } else if end < totalLines {
        fmt.Fprintf(&sb, "[warning: showing %d of %d lines. Use offset/limit to read the rest.]\n",
            end-offset+1, totalLines)
    }

    return tool.Result{
        Content: sb.String(),
        Display: buildDisplay(abs, totalLines, offset, end, len(raw), forcedTruncated || userLimited),
        UserLimited: userLimited,
        ForcedTruncated: forcedTruncated,
    }, nil
}

func mapIOError(err error) error {
    if errors.Is(err, fs.ErrNotExist) {
        return &tool.Error{Code: tool.ErrNotFound, Message: err.Error(), Cause: err}
    }
    return &tool.Error{Code: tool.ErrIO, Message: err.Error(), Cause: err}
}

func mapCtxError(err error) error {
    switch {
    case errors.Is(err, context.Canceled):
        return &tool.Error{Code: tool.ErrInterrupted, Message: "interrupted", Cause: err}
    case errors.Is(err, context.DeadlineExceeded):
        return &tool.Error{Code: tool.ErrTimeout, Message: "timeout", Cause: err}
    }
    return err
}
```

### 10.3.7 二进制探测

```go
func probeBinary(path string) (bool, error) {
    f, err := os.Open(path)
    if err != nil { return false, err }
    defer f.Close()
    buf := make([]byte, binaryProbeBytes)
    n, err := f.Read(buf)
    if err != nil && !errors.Is(err, io.EOF) { return false, err }
    return bytes.IndexByte(buf[:n], 0) >= 0, nil
}
```

UTF-8 BOM 等不影响（NUL 字节是文本文件不会出现的强信号）。

### 10.3.8 单元测试

每个工具的测试套件由 `internal/tool/testkit` 共享（§10.5），新工具按 testify/suite 填空。`read_file` 的测试至少覆盖：

| 用例 | 覆盖错误码 |
|---|---|
| 读小文本文件全文 | — |
| 读大文件触发 ForcedTruncated（行数）| — |
| 读大文件触发 ForcedTruncated（max_bytes）| — |
| 用户 offset > 1 触发 UserLimited | — |
| 文件不存在 | `ErrNotFound` |
| 二进制文件 | `ErrInvalidArgs` |
| 路径权限 OS denied | `ErrIO` |
| ctx 取消 | `ErrInterrupted` |
| ctx 超时（人造 deadline）| `ErrTimeout` |
| 参数 JSON 解析失败 | `ErrInvalidArgs` |
| Schema 反射结果与 golden 一致 | — |

---

## 10.4 对 R2 已锁定接口的修订（§5.4）

`tool.Result` 字段调整：

```go
// R2 原版
type Result struct {
    Content   string
    Display   string
    Truncated bool
}

// R7-1' 修订
type Result struct {
    Content         string
    Display         string
    UserLimited     bool   // 用户主动 limit/offset
    ForcedTruncated bool   // 工具自身上限触发
}
```

理由：原 `Truncated` 二态无法区分"用户主动限制（健康）"vs"工具被迫截断（异常）"，对压缩 / Pinned 决策不利。

---

## 10.5 通用测试套件（`internal/tool/testkit`）

```go
// internal/tool/testkit/suite.go
package testkit

import (
    "context"
    "github.com/stretchr/testify/suite"
    "mini-agent/internal/tool"
)

type ToolTestSuite struct {
    suite.Suite
    NewTool   func() tool.Tool          // 子类注入：构造工具
    HappyArgs map[string]any            // 子类注入：成功路径的入参（fixture）
}

func (s *ToolTestSuite) TestSchemaNonEmpty() {
    t := s.NewTool()
    schema := t.Schema()
    s.NotEmpty(schema)
    s.NotEmpty(t.Description())
    s.NotEmpty(t.Name())
}

func (s *ToolTestSuite) TestInvalidArgsRejected() {
    t := s.NewTool()
    _, err := t.Invoke(context.Background(), map[string]any{"__nonexistent__": 1})
    s.requireToolErr(err, tool.ErrInvalidArgs)
}

func (s *ToolTestSuite) TestCtxCancel() {
    t := s.NewTool()
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    _, err := t.Invoke(ctx, s.HappyArgs)
    s.requireToolErr(err, tool.ErrInterrupted)
}

func (s *ToolTestSuite) TestSchemaGolden() {
    // 子类覆写：比对 testdata/<tool>.schema.golden.json
}

func (s *ToolTestSuite) requireToolErr(err error, code tool.ErrorCode) {
    var te *tool.Error
    s.Require().ErrorAs(err, &te)
    s.Equal(code, te.Code)
}
```

读 file 的测试嵌入：

```go
// internal/tool/fs/readfile_test.go
type ReadFileSuite struct {
    testkit.ToolTestSuite
    tmpDir string
}

func TestReadFile(t *testing.T) {
    s := &ReadFileSuite{}
    s.NewTool = func() tool.Tool { return New(func() string { return s.tmpDir }) }
    suite.Run(t, s)
}

func (s *ReadFileSuite) SetupTest() {
    s.tmpDir = s.T().TempDir()
    s.HappyArgs = map[string]any{"path": writeFile(s.tmpDir, "a.txt", "hello\nworld\n")}
}

// 工具特有的测试
func (s *ReadFileSuite) TestBinaryRejected() { ... }
func (s *ReadFileSuite) TestUserLimited() { ... }
func (s *ReadFileSuite) TestForcedTruncatedByMaxBytes() { ... }
func (s *ReadFileSuite) TestSchemaGolden() {
    got := s.NewTool().Schema()
    expected := loadJSON(s.T(), "testdata/readfile.schema.golden.json")
    s.Equal(expected, got)
}
```

约定：
- `testdata/<tool>.schema.golden.json` 是 schema 的 golden file
- 改 schema 必须 `make update-tool-goldens`（脚本：所有工具 New().Schema() → marshal → 覆写 golden file）
- CI 中跑 `go test ./...`，golden 不一致直接失败

---

## 10.6 启动注册时的 schema 校验

```go
// internal/tool/registry.go（R7-1' 增强）
func (r *registry) Register(t Tool) error {
    if t.Name() == "" { return errors.New("tool: empty name") }
    if t.Description() == "" { return fmt.Errorf("tool %q: empty description", t.Name()) }
    schema := t.Schema()
    if schema == nil { return fmt.Errorf("tool %q: nil schema", t.Name()) }
    if _, ok := schema["type"]; !ok {
        return fmt.Errorf("tool %q: schema missing 'type' field", t.Name())
    }
    if _, exists := r.byName[t.Name()]; exists {
        return fmt.Errorf("tool %q: name conflict", t.Name())
    }
    r.byName[t.Name()] = t
    return nil
}
```

启动期校验 + golden file CI 校验，足以确保 description / schema 不偏离。

---

## 10.7 8 个 P0 工具的"待填空骨架"

每个工具按 §10.2 + §10.3 的 read_file 模板实现。下表给出 Args 字段 + Category + 关键约束（实现期对照填空，无需再走设计轮次）：

| 工具 | Category | Args 字段 | 关键约束 |
|---|---|---|---|
| `write_file` | Write | `path` / `content` / `mkdir_parents`(默认 true) | 不存在自动 MkdirAll；写完 fsync |
| `edit_file` | Write | `path` / `old_str` / `new_str` / `expected_occurrences`(默认 1) | old_str 不唯一返回 `ErrAmbiguous` 含前 3 处行号 |
| `delete_file` | Write | `path` | 仅文件；目录走 bash |
| `list_dir` | ReadOnly | `path` / `recursive`(默认 false) / `ignore_patterns`(默认含 `.git/` `node_modules/` 等) | 默认非递归 |
| `grep` | ReadOnly | `pattern` / `path` / `output_mode`(content/files_with_matches/count) / `multiline`(默认 false) / `context_before` / `context_after` | Go regexp(RE2) |
| `glob` | ReadOnly | `pattern` / `cwd`(默认 session.Cwd) | doublestar glob |
| `bash` | Execute | `command` / `cwd` / `stdin`(可选) / `timeout_sec`(默认 60，上限 300) / `env_overrides` | 完整继承 env；硬黑名单先于 Gate；shellwords 解析；50KB stdout/stderr 上限 |
| `ask_user` | Meta | `question` / `hint`(可选) | --yes 模式仍然询问（D? 待确认时统一为永远问） |

> **注**：bash 工具的硬黑名单完整规则集与命令解析算法的精细化，留到实现期（T2.4）针对性补充。R7-1' 仅锁"采用 shellwords 解析 + 复合命令拆解为 token 后逐个匹配"的方向。

---

## 10.8 R7-1' 关键决定（D68–D86）

| 编号 | 决定 |
|---|---|
| **D68** | 不为每个工具单独走集中设计轮次；用 read_file 走通"工具实现"的统一模板，其它工具按模板填空 |
| **D69** | JSON Schema 由 `github.com/invopop/jsonschema` 反射生成；每个工具内部定义 `<ToolName>Args` typed struct + jsonschema tag |
| **D70** | required 由 jsonschema tag 标注；可选字段 description 末尾以 `; default: X` 标明默认值；description 全英文 |
| **D71** | 工具 description 写"长描述"风格：含 when to use / when NOT to use / 注意事项 |
| **D72** | `tool.Result` 字段调整（修订 R2）：删 `Truncated`，新增 `UserLimited` + `ForcedTruncated` 区分用户主动限制 vs 工具强制截断 |
| **D73** | `Result.Content` 用 `<file>` `<dir>` `<grep>` 等结构化标签包装；警告内嵌为单独行附在闭合标签后 |
| **D74** | `Result.Display` 格式：`<tool> <subject> (<size>) → <action>`；如截断追加 `[truncated]` 标记 |
| **D75** | 错误码扩充（R7 新增）：`ErrTooLarge` + `ErrAmbiguous`，加上 R2 已有的 7 种共 9 种 |
| **D76** | 错误消息全英文；"换方式"提示文案由 agent loop 在 tool_result content 末尾追加（不是工具自身职责）：`This approach has failed N times in a row — please try a different way.` |
| **D77** | ctx 错误强约定：`Canceled→ErrInterrupted`；`DeadlineExceeded→ErrTimeout`；工具如有特殊语义可在 doc.go override |
| **D78** | 工具内部约定俗成两个私有 helper：`decodeArgs(input map[string]any) (*<Args>, error)` + `validateArgs(*<Args>) error`；不进 Tool interface |
| **D79** | read_file 行为：仅文本（NUL 探测 8KB）；行号单位 offset/limit；总是带 6 位右对齐行号前缀；默认 max_bytes=200KB 上限 1MB |
| **D80** | read_file Content 格式：`<file path lines total_lines range bytes>` 标签包装；强制截断追加 `[warning: ...]` 行 |
| **D81** | read_file Display 格式：`read_file <name> (<size>, <total_lines> lines) → showing <range> [truncated]?` |
| **D82** | 通用测试套件 `internal/tool/testkit` 用 testify/suite；包含 SchemaNonEmpty / InvalidArgsRejected / CtxCancel / SchemaGolden 四个公共用例 |
| **D83** | Schema golden file 校验：`testdata/<tool>.schema.golden.json`；改 schema 必须 `make update-tool-goldens` 同步；CI 强制比对 |
| **D84** | Registry.Register 启动期校验：name 非空、description 非空、schema 非空且含 `type` 字段、name 不冲突 |
| **D85** | 8 个其它 P0 工具按 §10.7 表格填空实现；bash 命令解析与硬黑名单完整规则集留实现期（T2.4）针对性补充 |
| **D86** | `ask_user` 在 `--yes` 模式下仍然要求用户回答（与执行风险无关）|

---

## 10.9 留待实现期 / 后续轮次

| 议题 | 归属 |
|---|---|
| bash 命令的 shellwords 解析与复合命令拆解算法（`&&` `||` `;` `|` `$()` 反引号） | 实现期（T2.4） |
| 硬黑名单完整规则集（rm -rf 各种变体、fork bomb、`/etc/passwd` 写入等） | 实现期（T2.4）|
| AlwaysApprove 等价性判定（参数完全相同 / 工具名级 / 路径前缀级） | 实现期（permission 模块）|
| RememberApproval 范围（session 内 vs 持久化）| 实现期 |
| edit_file 多义性失败时的 diff 渲染细节（前 3 处的上下文行数） | 实现期 |
| grep output_mode 的具体输出格式（含 ripgrep 风格 vs JSON）| 实现期 |
| 5 个 P1/P2 工具（write_plan / task / skill_tool / web_fetch / web_search）的 schema | 推到 R7-2（按 Iter-3/Iter-4 进度触发）|
| web_fetch 的 HTML→markdown 转换库选型 | R7-2 / Iter-4 实现期 |
