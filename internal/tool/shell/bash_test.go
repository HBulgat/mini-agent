// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

//go:build !windows

package shell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/HBulgat/mini-agent/internal/tool"
	"github.com/HBulgat/mini-agent/internal/tool/testkit"
)

// ============================================================
// Shared testkit suite
// ============================================================

// BashSuite injects the tool factory.
type BashSuite struct {
	testkit.ToolTestSuite

	tmpDir string
}

func TestBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash tool requires POSIX shell")
	}
	s := new(BashSuite)
	s.NewTool = func() tool.Tool {
		return NewBash(func() string { return s.tmpDir })
	}
	suite.Run(t, s)
}

func (s *BashSuite) SetupTest() {
	s.tmpDir = s.T().TempDir()
	s.HappyArgs = map[string]any{"command": "echo hi"}
}

func (s *BashSuite) TestSchemaGolden() {
	s.GoldenPath = filepath.Join("testdata", "bash.schema.golden.json")
	s.ToolTestSuite.TestSchemaGolden()
}

// ------------------------------------------------------------
// Behaviour
// ------------------------------------------------------------

// TestEchoStdout: echo "hello" produces "hello" on stdout, exit 0.
func (s *BashSuite) TestEchoStdout() {
	res := mustBash(s.T(), s.tmpDir, map[string]any{"command": "echo hello"})
	s.Contains(res.Content, "exit_code=0")
	s.Contains(res.Content, "<stdout>\nhello\n")
	s.Contains(res.Display, "exit=0")
	s.Contains(res.Display, "→ ok")
	s.False(res.ForcedTruncated)
}

// TestNonZeroExit: a failing command produces a Result, NOT an error.
func (s *BashSuite) TestNonZeroExit() {
	res := mustBash(s.T(), s.tmpDir, map[string]any{"command": "exit 7"})
	s.Contains(res.Content, "exit_code=7")
	s.Contains(res.Display, "exit=7")
	s.Contains(res.Display, "→ fail")
}

// TestStderr: stderr is captured separately from stdout.
func (s *BashSuite) TestStderr() {
	res := mustBash(s.T(), s.tmpDir, map[string]any{
		"command": "echo out; echo err 1>&2",
	})
	s.Contains(res.Content, "<stdout>\nout\n")
	s.Contains(res.Content, "<stderr>\nerr\n")
}

// TestStdoutCap: > 50 KB of output is truncated and flagged.
func (s *BashSuite) TestStdoutCap() {
	res := mustBash(s.T(), s.tmpDir, map[string]any{
		"command": "yes x | head -c 102400",
	})
	s.True(res.ForcedTruncated, "expected ForcedTruncated=true at cap")
	s.Contains(res.Content, "stdout truncated")
	s.Contains(res.Display, "[truncated]")
}

// TestStderrCap: > 50 KB on stderr is truncated and flagged.
func (s *BashSuite) TestStderrCap() {
	res := mustBash(s.T(), s.tmpDir, map[string]any{
		"command": "yes x | head -c 102400 1>&2",
	})
	s.True(res.ForcedTruncated)
	s.Contains(res.Content, "stderr truncated")
}

// TestTimeout: a long-running command is killed and ErrTimeout returns.
func (s *BashSuite) TestTimeout() {
	tl := NewBash(func() string { return s.tmpDir })
	one := 1
	start := time.Now()
	_, err := tl.Invoke(context.Background(), map[string]any{
		"command":     "sleep 30",
		"timeout_sec": one,
	})
	elapsed := time.Since(start)
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrTimeout, te.Code)
	s.Less(elapsed, 5*time.Second, "timeout should fire quickly; got %v", elapsed)
}

// TestCtxCancel: cancelling the parent context kills the subprocess.
func (s *BashSuite) TestCtxCancel() {
	tl := NewBash(func() string { return s.tmpDir })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, err := tl.Invoke(ctx, map[string]any{"command": "sleep 30"})
	elapsed := time.Since(start)
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInterrupted, te.Code)
	s.Less(elapsed, 5*time.Second)
}

// TestStdin: payload appears on the subprocess stdin.
func (s *BashSuite) TestStdin() {
	res := mustBash(s.T(), s.tmpDir, map[string]any{
		"command": "cat",
		"stdin":   "hello stdin",
	})
	s.Contains(res.Content, "<stdout>\nhello stdin")
}

// TestCwd: relative cwd is resolved against the session cwd.
func (s *BashSuite) TestCwd() {
	subDir := filepath.Join(s.tmpDir, "sub")
	s.Require().NoError(os.MkdirAll(subDir, 0o755))
	res := mustBash(s.T(), s.tmpDir, map[string]any{
		"command": "pwd",
		"cwd":     "sub",
	})
	s.Contains(res.Content, subDir)
}

// TestCwdNotADirectory: file as cwd returns ErrInvalidArgs.
func (s *BashSuite) TestCwdNotADirectory() {
	s.Require().NoError(os.WriteFile(
		filepath.Join(s.tmpDir, "f"), []byte("x"), 0o644))
	tl := NewBash(func() string { return s.tmpDir })
	_, err := tl.Invoke(context.Background(), map[string]any{
		"command": "true",
		"cwd":     "f",
	})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestEnvOverrides: variables propagate to the child process.
func (s *BashSuite) TestEnvOverrides() {
	res := mustBash(s.T(), s.tmpDir, map[string]any{
		"command": "echo $MINI_AGENT_TEST_VAR",
		"env_overrides": map[string]string{
			"MINI_AGENT_TEST_VAR": "secret-token",
		},
	})
	s.Contains(res.Content, "secret-token")
}

// TestEnvOverrideDeletion: empty value removes the variable.
//
// We use a custom variable rather than PATH because bash itself
// re-populates PATH if it's empty in the inherited env.
func (s *BashSuite) TestEnvOverrideDeletion() {
	// First, set the variable in the parent env.
	s.T().Setenv("MINI_AGENT_DELETE_ME", "should-be-gone")

	// Now ask bash to print it; with the override-as-delete it should
	// expand to empty.
	res := mustBash(s.T(), s.tmpDir, map[string]any{
		"command": "echo \"V=[$MINI_AGENT_DELETE_ME]\"",
		"env_overrides": map[string]string{
			"MINI_AGENT_DELETE_ME": "",
		},
	})
	s.Contains(res.Content, "V=[]")
}

// TestEmptyCommand rejects whitespace-only commands.
func (s *BashSuite) TestEmptyCommand() {
	_, err := s.NewTool().Invoke(context.Background(),
		map[string]any{"command": "   "})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestNegativeTimeout rejects timeout_sec <= 0.
func (s *BashSuite) TestNegativeTimeout() {
	bad := -1
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"command":     "true",
		"timeout_sec": bad,
	})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestTimeoutClamp: huge timeout_sec is silently clamped to 300.
func (s *BashSuite) TestTimeoutClamp() {
	huge := 99999
	res := mustBash(s.T(), s.tmpDir, map[string]any{
		"command":     "echo ok",
		"timeout_sec": huge,
	})
	s.Contains(res.Content, "<stdout>\nok\n")
}

// TestUnknownFieldRejected: strict decode rejects typos.
func (s *BashSuite) TestUnknownFieldRejected() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"command": "true",
		"bogus":   true,
	})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestPipeCompound verifies that && / | / ; chains are handled by
// the shell (i.e. we don't accidentally pass them as literal args).
func (s *BashSuite) TestPipeCompound() {
	res := mustBash(s.T(), s.tmpDir, map[string]any{
		"command": "echo foo && echo bar | tr a-z A-Z",
	})
	s.Contains(res.Content, "foo")
	s.Contains(res.Content, "BAR")
}

// TestProcessGroupKill verifies that killing the parent shell takes
// out spawned children too. We start `bash -c "sleep 60 & wait"` —
// without process-group kill the `sleep 60` would outlive the parent.
// The timeout firing promptly is the user-observable signal.
func (s *BashSuite) TestProcessGroupKill() {
	tl := NewBash(func() string { return s.tmpDir })
	one := 1
	start := time.Now()
	_, err := tl.Invoke(context.Background(), map[string]any{
		"command":     "sleep 30 & sleep 30 & wait",
		"timeout_sec": one,
	})
	elapsed := time.Since(start)
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrTimeout, te.Code)
	s.Less(elapsed, 5*time.Second)
}

// ============================================================
// Pure-function tests (no subprocess)
// ============================================================

// TestMergeEnv covers the env-merge helper without spawning a process.
func TestMergeEnv(t *testing.T) {
	cases := []struct {
		base      []string
		overrides map[string]string
		wantKV    map[string]string // key → expected value (or "" if absent)
		desc      string
	}{
		{
			base:      []string{"A=1", "B=2"},
			overrides: nil,
			wantKV:    map[string]string{"A": "1", "B": "2"},
			desc:      "nil overrides preserves base",
		},
		{
			base:      []string{"A=1", "B=2"},
			overrides: map[string]string{"A": "999"},
			wantKV:    map[string]string{"A": "999", "B": "2"},
			desc:      "override existing key",
		},
		{
			base:      []string{"A=1", "B=2"},
			overrides: map[string]string{"C": "new"},
			wantKV:    map[string]string{"A": "1", "B": "2", "C": "new"},
			desc:      "add new key",
		},
		{
			base:      []string{"A=1", "B=2"},
			overrides: map[string]string{"A": ""},
			wantKV:    map[string]string{"A": "", "B": "2"},
			desc:      "delete key via empty value",
		},
		{
			base:      []string{"A=1"},
			overrides: map[string]string{"NOTHERE": ""},
			wantKV:    map[string]string{"A": "1"},
			desc:      "delete absent key is no-op",
		},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			out := mergeEnv(c.base, c.overrides)
			seen := map[string]string{}
			for _, kv := range out {
				if eq := strings.IndexByte(kv, '='); eq > 0 {
					seen[kv[:eq]] = kv[eq+1:]
				}
			}
			for k, want := range c.wantKV {
				got, present := seen[k]
				if want == "" {
					if present && got != "" {
						t.Errorf("expected %q to be absent/empty; got %q", k, got)
					}
					continue
				}
				if !present {
					t.Errorf("expected %q=%q but key absent", k, want)
				} else if got != want {
					t.Errorf("expected %q=%q got %q", k, want, got)
				}
			}
		})
	}
}

// TestClampInt covers the timeout clamp helper.
func TestClampInt(t *testing.T) {
	cases := []struct {
		v, lo, hi, want int
	}{
		{50, 1, 300, 50},
		{0, 1, 300, 1},
		{-5, 1, 300, 1},
		{500, 1, 300, 300},
		{1, 1, 300, 1},
		{300, 1, 300, 300},
	}
	for _, c := range cases {
		if got := clampInt(c.v, c.lo, c.hi); got != c.want {
			t.Errorf("clampInt(%d, %d, %d) = %d; want %d", c.v, c.lo, c.hi, got, c.want)
		}
	}
}

// TestCappedBuffer ensures we cap correctly and report overflow.
func TestCappedBuffer(t *testing.T) {
	cb := newCappedBuffer(5)

	n, _ := cb.Write([]byte("abc"))
	if n != 3 {
		t.Errorf("Write(3) = %d", n)
	}
	out, over := cb.snapshot()
	if string(out) != "abc" || over {
		t.Errorf("after 3B: got %q over=%v", out, over)
	}

	n, _ = cb.Write([]byte("defgh"))
	if n != 5 {
		t.Errorf("Write past cap should report full length accepted; got %d", n)
	}
	out, over = cb.snapshot()
	if string(out) != "abcde" {
		t.Errorf("buffer should hold first 5 bytes only; got %q", out)
	}
	if !over {
		t.Errorf("over flag should be true after exceeding cap")
	}

	n, _ = cb.Write([]byte("ignored"))
	if n != 7 {
		t.Errorf("post-cap Write should still report acceptance; got %d", n)
	}
	out, over = cb.snapshot()
	if string(out) != "abcde" {
		t.Errorf("buffer must not grow past cap; got %q", out)
	}
	if !over {
		t.Errorf("over flag should remain true")
	}
}

// ============================================================
// helpers
// ============================================================

func mustBash(t *testing.T, cwd string, args map[string]any) tool.Result {
	t.Helper()
	tl := NewBash(func() string { return cwd })
	res, err := tl.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("Bash.Invoke: %v", err)
	}
	return res
}
