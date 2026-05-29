// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

// Package shell hosts the `bash` tool, the only Execute-category tool
// in P0. The tool deliberately delegates parsing of `&&`, `||`, `;`,
// `|`, `$()` etc. to the system shell rather than parsing them
// ourselves: by spawning `/bin/bash -c <command>` we get full POSIX
// shell semantics for free, and the agent's permission gate already
// matches the hard-blacklist patterns against the raw command string
// (no token-level explosion required).
package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/invopop/jsonschema"

	"github.com/HBulgat/mini-agent/internal/tool"
)

// Bash is the `bash` tool. Each Invoke spawns a fresh `/bin/bash -c`
// subprocess in its own process group so we can kill descendants on
// timeout / ctx cancel.
type Bash struct {
	cwd func() string
}

// NewBash builds the tool. cwd resolves the optional `cwd` arg.
func NewBash(cwd func() string) *Bash {
	if cwd == nil {
		cwd = func() string { return "" }
	}
	return &Bash{cwd: cwd}
}

// Caps & defaults — all hardcoded, not user-tunable. The 50 KB stream
// cap matches the figure quoted in §10.7 and is comfortably below the
// context window of every supported model.
const (
	defaultBashTimeoutSec = 60
	maxBashTimeoutSec     = 300
	maxStreamBytes        = 50 * 1024 // per-stream cap (stdout, stderr)
	killGracePeriod       = 1 * time.Second
)

// shellPath is the binary we invoke. On non-POSIX systems Invoke
// short-circuits with ErrInvalidArgs so we never silently call the
// wrong shell.
const shellPath = "/bin/bash"

// BashArgs is the JSON input. *int / map preserves the unset semantic.
type BashArgs struct {
	// Command is the shell snippet to execute. Required. The shell
	// itself parses && / || / ; / | / $() / backticks, so callers
	// don't need to escape anything beyond normal shell quoting.
	Command string `json:"command" jsonschema:"required,description=Shell command to execute via /bin/bash -c. Supports the full POSIX feature set including pipes, redirects, command substitution, and compound commands."`

	// Cwd optionally overrides the session cwd for this call. Relative
	// values resolve against the session cwd.
	Cwd string `json:"cwd,omitempty" jsonschema:"description=Working directory for the command. Relative paths resolve against the session cwd; defaults to the session cwd when omitted."`

	// Stdin is fed to the subprocess on stdin (no length cap on the
	// caller side — large stdin is OK because the agent rarely emits
	// huge payloads).
	Stdin string `json:"stdin,omitempty" jsonschema:"description=Standard input to pipe into the command; empty string means /dev/null."`

	// TimeoutSec caps wall-clock runtime. Default 60 s; max 300 s.
	// Exceeding the cap is silently clamped (we don't ErrInvalidArgs
	// because LLMs sometimes generate enormous values; clamping lets
	// the call succeed with a reasonable bound).
	TimeoutSec *int `json:"timeout_sec,omitempty" jsonschema:"description=Wall-clock timeout in seconds; default 60; clamped to [1, 300]."`

	// EnvOverrides extends/overrides the inherited environment. Keys
	// are env names; empty-string values delete the variable.
	EnvOverrides map[string]string `json:"env_overrides,omitempty" jsonschema:"description=Environment variables to set. Empty value removes the variable. Merged on top of the parent process environment."`
}

// ============================================================
// tool.Tool
// ============================================================

func (b *Bash) Name() string { return "bash" }

func (b *Bash) Description() string {
	return strings.TrimSpace(`
Execute a shell command using /bin/bash -c.

When to use:
- Running build / test / format commands (make, go test, npm run, ...)
- File operations not covered by the dedicated tools (chmod, mv, tar, ...)
- Inspecting system state (ps, df, uname, ...)
- Anything that benefits from pipes, redirects, or command substitution

When NOT to use:
- Reading a single file's contents — use read_file
- Mutating a file's contents — use write_file / edit_file
- Removing a single file — use delete_file
- Listing a directory — use list_dir
- Searching content — use grep
- Searching by filename — use glob

Notes:
- Pipes, &&, ||, ;, redirects, $() and backticks all work — you do
  NOT need to escape them; the system shell parses the command string.
- The hard blacklist (e.g. rm -rf /, fork bombs, /etc/passwd writes)
  is enforced by the permission gate BEFORE this tool runs.
- stdout and stderr are each capped at 50 KB; output beyond the cap
  is truncated and the truncation is flagged in Result.
- timeout_sec defaults to 60 s, max 300 s. On timeout the entire
  process group is SIGKILL'd.
- A non-zero exit code is NOT an error; the Result includes exit_code
  so the caller can decide whether to retry, fix, or give up.
`)
}

func (b *Bash) Schema() map[string]any {
	r := &jsonschema.Reflector{
		ExpandedStruct:             true,
		DoNotReference:             true,
		RequiredFromJSONSchemaTags: true,
		Anonymous:                  true,
		AllowAdditionalProperties:  false,
	}
	return schemaToMap(r.Reflect(&BashArgs{}))
}

func (b *Bash) Category() tool.Category { return tool.CategoryExecute }

// Invoke runs the command. On a Windows host (no /bin/bash) it returns
// ErrInvalidArgs immediately so the agent loop can surface a clear
// error rather than a confusing "no such file" from exec.
func (b *Bash) Invoke(ctx context.Context, input map[string]any) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, tool.MapCtxError(err)
	}
	if runtime.GOOS == "windows" {
		return tool.Result{}, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: "bash tool requires a POSIX shell at /bin/bash; Windows is not supported in P0",
		}
	}

	args, err := b.decodeArgs(input)
	if err != nil {
		return tool.Result{}, err
	}
	if err := b.validateArgs(args); err != nil {
		return tool.Result{}, err
	}

	timeoutSec := defaultBashTimeoutSec
	if args.TimeoutSec != nil {
		timeoutSec = *args.TimeoutSec
	}
	timeoutSec = clampInt(timeoutSec, 1, maxBashTimeoutSec)

	cwd := strings.TrimSpace(args.Cwd)
	if cwd == "" {
		cwd = b.cwd()
	} else if !filepath.IsAbs(cwd) {
		// Resolve relative cwd against the session cwd.
		base := b.cwd()
		if base != "" {
			cwd = filepath.Join(base, cwd)
		}
	}
	cwd = filepath.Clean(cwd)
	if cwd != "" {
		if info, statErr := os.Stat(cwd); statErr != nil {
			return tool.Result{}, tool.MapIOError(statErr)
		} else if !info.IsDir() {
			return tool.Result{}, &tool.Error{
				Code:    tool.ErrInvalidArgs,
				Message: fmt.Sprintf("cwd %q is not a directory", args.Cwd),
			}
		}
	}

	env := mergeEnv(os.Environ(), args.EnvOverrides)

	return b.runCommand(ctx, args.Command, cwd, env, args.Stdin, timeoutSec)
}

// ============================================================
// argument decoding & validation
// ============================================================

func (b *Bash) decodeArgs(input map[string]any) (*BashArgs, error) {
	if input == nil {
		return nil, &tool.Error{Code: tool.ErrInvalidArgs, Message: "input is nil"}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, &tool.Error{Code: tool.ErrInvalidArgs, Message: fmt.Sprintf("re-marshal input: %v", err)}
	}
	var out BashArgs
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return nil, &tool.Error{Code: tool.ErrInvalidArgs, Message: err.Error()}
	}
	return &out, nil
}

func (b *Bash) validateArgs(a *BashArgs) error {
	if strings.TrimSpace(a.Command) == "" {
		return &tool.Error{Code: tool.ErrInvalidArgs, Message: "command is required"}
	}
	if a.TimeoutSec != nil && *a.TimeoutSec <= 0 {
		return &tool.Error{Code: tool.ErrInvalidArgs, Message: "timeout_sec must be positive"}
	}
	return nil
}

// clampInt restricts v to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// mergeEnv overlays overrides on top of the parent env. Empty value
// in overrides means "delete this variable".
func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	idx := make(map[string]int, len(base))
	out := make([]string, len(base))
	copy(out, base)
	for i, kv := range out {
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			idx[kv[:eq]] = i
		}
	}
	for k, v := range overrides {
		if v == "" {
			// delete: remove if present, otherwise no-op
			if i, ok := idx[k]; ok {
				out[i] = "" // will be filtered in the next pass
				delete(idx, k)
			}
			continue
		}
		entry := k + "=" + v
		if i, ok := idx[k]; ok {
			out[i] = entry
		} else {
			out = append(out, entry)
			idx[k] = len(out) - 1
		}
	}
	// Filter empty placeholders left from deletions.
	cleaned := out[:0]
	for _, kv := range out {
		if kv != "" {
			cleaned = append(cleaned, kv)
		}
	}
	return cleaned
}

// ============================================================
// runCommand: spawn /bin/bash -c, manage process group, cap output
// ============================================================

// runCommand owns the subprocess lifecycle. Its single goal is to
// produce a Result that captures stdout / stderr / exit_code in a way
// the agent loop can hand back to the LLM. It does NOT translate
// non-zero exits into errors — LLMs handle that case themselves.
func (b *Bash) runCommand(
	ctx context.Context, command, cwd string, env []string, stdin string, timeoutSec int,
) (tool.Result, error) {
	// Build a child ctx with the per-call timeout. We deliberately
	// keep the parent ctx as the outer cancel source so a user
	// interrupt (Ctrl+C) propagates immediately.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, shellPath, "-c", command)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = env
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	// Process-group setup so we can SIGKILL descendants on timeout.
	// On Linux/macOS, Setpgid:true makes the child the leader of a
	// new pgid; killing -pgid then takes out the whole tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Capped pipes for stdout / stderr.
	stdoutBuf := newCappedBuffer(maxStreamBytes)
	stderrBuf := newCappedBuffer(maxStreamBytes)
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	// Spawn.
	if err := cmd.Start(); err != nil {
		return tool.Result{}, &tool.Error{
			Code:    tool.ErrInvalidArgs,
			Message: fmt.Sprintf("failed to start /bin/bash: %v", err),
			Cause:   err,
		}
	}

	// CommandContext already SIGKILLs the leader on ctx done, but
	// we want the *whole pgid* taken out — children spawned by the
	// shell can otherwise outlive their parent. We arrange a
	// background goroutine to do that explicit pgid kill.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var (
		waitErr     error
		killedByCtx bool
	)
	select {
	case waitErr = <-done:
		// Normal completion (zero or non-zero exit).
	case <-ctx.Done():
		killedByCtx = true
		// SIGKILL the whole process group; ignore errors (the
		// process may have just exited).
		if cmd.Process != nil {
			pgid, pgerr := syscall.Getpgid(cmd.Process.Pid)
			if pgerr == nil {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			} else {
				_ = cmd.Process.Kill()
			}
		}
		// Give the child a beat to die so cmd.Wait drains.
		select {
		case waitErr = <-done:
		case <-time.After(killGracePeriod):
			// Wait took too long; we've already SIGKILLed. The
			// child should be a zombie shortly.
			waitErr = <-done
		}
	}

	stdout, stdoutTrunc := stdoutBuf.snapshot()
	stderr, stderrTrunc := stderrBuf.snapshot()

	// Translate ctx.Err() into the agent-side error contract.
	if killedByCtx {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return tool.Result{}, &tool.Error{
				Code:    tool.ErrTimeout,
				Message: fmt.Sprintf("command timed out after %ds", timeoutSec),
			}
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return tool.Result{}, &tool.Error{
				Code:    tool.ErrInterrupted,
				Message: "command was interrupted",
			}
		}
	}

	// Extract exit code. exec.ExitError carries it; nil error means 0.
	exitCode := 0
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			// Genuine exec failure (e.g. binary not found mid-run).
			return tool.Result{}, &tool.Error{
				Code:    tool.ErrInvalidArgs,
				Message: fmt.Sprintf("subprocess failure: %v", waitErr),
				Cause:   waitErr,
			}
		}
	}

	truncated := stdoutTrunc || stderrTrunc

	return tool.Result{
		Content:         buildBashContent(command, exitCode, stdout, stderr, stdoutTrunc, stderrTrunc),
		Display:         buildBashDisplay(command, exitCode, len(stdout), len(stderr), truncated),
		ForcedTruncated: truncated,
	}, nil
}

// ============================================================
// cappedBuffer: io.Writer that stops accepting bytes past capN
// ============================================================

// cappedBuffer is a tiny io.Writer that records up to capN bytes of
// output and silently discards the rest. We track whether we ever
// hit the cap so the Result can flag truncation.
type cappedBuffer struct {
	mu    sync.Mutex
	buf   []byte
	capN  int
	over  bool
}

func newCappedBuffer(capN int) *cappedBuffer {
	return &cappedBuffer{buf: make([]byte, 0, capN), capN: capN}
}

// Write is the io.Writer entry point.
func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	remaining := c.capN - len(c.buf)
	if remaining <= 0 {
		c.over = true
		// Pretend we accepted everything so the writer doesn't loop.
		return len(p), nil
	}
	if len(p) > remaining {
		c.buf = append(c.buf, p[:remaining]...)
		c.over = true
		return len(p), nil
	}
	c.buf = append(c.buf, p...)
	return len(p), nil
}

// snapshot returns the captured bytes plus whether we overflowed.
func (c *cappedBuffer) snapshot() ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]byte, len(c.buf))
	copy(out, c.buf)
	return out, c.over
}

// Compile-time check that cappedBuffer satisfies io.Writer.
var _ io.Writer = (*cappedBuffer)(nil)

// ============================================================
// content / display
// ============================================================

// buildBashContent renders the LLM-facing payload. Format:
//
//	<bash exit_code=N>
//	<stdout>...</stdout>
//	<stderr>...</stderr>
//	</bash>
//	[warning: stdout truncated at 50 KB]
//
// We always emit both <stdout> and <stderr> blocks, even when empty,
// so the LLM doesn't have to parse the absence of one section as
// "empty" vs "field missing".
func buildBashContent(command string, exitCode int, stdout, stderr []byte, stdoutTrunc, stderrTrunc bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<bash exit_code=%d>\n", exitCode)
	b.WriteString("<stdout>\n")
	b.Write(stdout)
	if len(stdout) > 0 && stdout[len(stdout)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString("</stdout>\n")
	b.WriteString("<stderr>\n")
	b.Write(stderr)
	if len(stderr) > 0 && stderr[len(stderr)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString("</stderr>\n")
	b.WriteString("</bash>")
	if stdoutTrunc {
		fmt.Fprintf(&b, "\n[warning: stdout truncated at %d bytes]", maxStreamBytes)
	}
	if stderrTrunc {
		fmt.Fprintf(&b, "\n[warning: stderr truncated at %d bytes]", maxStreamBytes)
	}
	return b.String()
}

// buildBashDisplay renders the user-facing one-liner per D74:
//
//	bash <command snippet> (exit=N, <so>B/<se>B) → ok|fail [truncated]?
//
// We snippet the command at 60 chars so the display stays tidy when
// users run `cd /tmp && tar -xzf big.tar.gz && ./configure --prefix=...`
func buildBashDisplay(command string, exitCode, stdoutN, stderrN int, truncated bool) string {
	snippet := command
	if len(snippet) > 60 {
		snippet = snippet[:57] + "..."
	}
	verdict := "ok"
	if exitCode != 0 {
		verdict = "fail"
	}
	suffix := ""
	if truncated {
		suffix = " [truncated]"
	}
	return fmt.Sprintf("bash %q (exit=%d, %dB/%dB) → %s%s",
		snippet, exitCode, stdoutN, stderrN, verdict, suffix)
}

// schemaToMap converts a *jsonschema.Schema to map[string]any via JSON
// round-trip — same shape used by the fs and search packages.
func schemaToMap(s any) map[string]any {
	if s == nil {
		return map[string]any{}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}
