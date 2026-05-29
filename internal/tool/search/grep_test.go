// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

package search

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/HBulgat/mini-agent/internal/tool"
	"github.com/HBulgat/mini-agent/internal/tool/testkit"
)

// ============================================================
// Shared testkit suite
// ============================================================

// GrepSuite injects the tool factory and a fixture directory.
type GrepSuite struct {
	testkit.ToolTestSuite

	tmpDir string
}

func TestGrep(t *testing.T) {
	s := new(GrepSuite)
	s.NewTool = func() tool.Tool {
		return NewGrep(func() string { return s.tmpDir })
	}
	suite.Run(t, s)
}

// SetupTest builds a small fixture and pins HappyArgs.
func (s *GrepSuite) SetupTest() {
	s.tmpDir = s.T().TempDir()
	mustWrite(s.T(), s.tmpDir, "a.go", "package main\nfunc Foo() {}\n// TODO: rename\n")
	mustWrite(s.T(), s.tmpDir, "b.go", "package main\nfunc Bar() {}\n")
	s.HappyArgs = map[string]any{"pattern": "func", "path": "."}
}

func (s *GrepSuite) TestSchemaGolden() {
	s.GoldenPath = filepath.Join("testdata", "grep.schema.golden.json")
	s.ToolTestSuite.TestSchemaGolden()
}

// ------------------------------------------------------------
// Behaviour
// ------------------------------------------------------------

// TestContentMode_HappyPath: default mode emits "<path>:<line>:<text>"
// for every match.
func (s *GrepSuite) TestContentMode_HappyPath() {
	res := mustGrep(s.T(), s.tmpDir, map[string]any{
		"pattern": "func",
		"path":    ".",
	})
	s.Contains(res.Content, "a.go:2:func Foo()")
	s.Contains(res.Content, "b.go:2:func Bar()")
	s.Contains(res.Display, "grep . (")
	s.Contains(res.Display, "matches")
}

// TestFilesWithMatchesMode: only paths, one per line.
func (s *GrepSuite) TestFilesWithMatchesMode() {
	res := mustGrep(s.T(), s.tmpDir, map[string]any{
		"pattern":     "TODO",
		"path":        ".",
		"output_mode": "files_with_matches",
	})
	s.Contains(res.Content, "a.go")
	s.NotContains(res.Content, "b.go", "b.go has no TODO")
	// In files_with_matches mode the body line should not contain ":2:"
	s.NotContains(res.Content, ":2:")
}

// TestCountMode: per-file counts.
func (s *GrepSuite) TestCountMode() {
	res := mustGrep(s.T(), s.tmpDir, map[string]any{
		"pattern":     "package",
		"path":        ".",
		"output_mode": "count",
	})
	// Both files have one "package main" line.
	s.Contains(res.Content, "a.go:1")
	s.Contains(res.Content, "b.go:1")
}

// TestSingleFilePath: when path is a regular file, only that file is
// scanned.
func (s *GrepSuite) TestSingleFilePath() {
	res := mustGrep(s.T(), s.tmpDir, map[string]any{
		"pattern": "Foo",
		"path":    "a.go",
	})
	s.Contains(res.Content, "func Foo()")
	s.NotContains(res.Content, "Bar")
}

// TestContextLines: -B and -A produce surrounding lines marked with '-'.
func (s *GrepSuite) TestContextLines() {
	mustWrite(s.T(), s.tmpDir, "ctx.go",
		"line1\nline2\nMATCH\nline4\nline5\n")
	before := 1
	after := 1
	res := mustGrep(s.T(), s.tmpDir, map[string]any{
		"pattern":        "MATCH",
		"path":           "ctx.go",
		"context_before": before,
		"context_after":  after,
	})
	// Match line uses ':' separator.
	s.Contains(res.Content, ":3:MATCH")
	// Context lines use '-' separator.
	s.Contains(res.Content, "-2-line2")
	s.Contains(res.Content, "-4-line4")
	// Outside-window lines should NOT be present.
	s.NotContains(res.Content, "line1")
	s.NotContains(res.Content, "line5")
}

// TestInvalidRegex returns ErrInvalidArgs on bad RE2 syntax.
func (s *GrepSuite) TestInvalidRegex() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"pattern": "[",
		"path":    ".",
	})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestUnknownOutputMode rejects bogus mode strings.
func (s *GrepSuite) TestUnknownOutputMode() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"pattern":     "x",
		"path":        ".",
		"output_mode": "frobnicate",
	})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestNegativeContext rejects context_before < 0.
func (s *GrepSuite) TestNegativeContext() {
	bad := -1
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"pattern":        "x",
		"path":           ".",
		"context_before": bad,
	})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestPathDoesNotExist surfaces ErrNotFound.
func (s *GrepSuite) TestPathDoesNotExist() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"pattern": "x",
		"path":    "no-such-dir",
	})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrNotFound, te.Code)
}

// TestMissingPattern returns ErrInvalidArgs.
func (s *GrepSuite) TestMissingPattern() {
	_, err := s.NewTool().Invoke(context.Background(),
		map[string]any{"path": "."})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestUnknownFieldRejected: strict decode rejects typos.
func (s *GrepSuite) TestUnknownFieldRejected() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"pattern": "x",
		"path":    ".",
		"bogus":   true,
	})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestDefaultIgnoreSkipsGitDir verifies grep's default ignore set
// hides .git/ contents during recursive walks.
func (s *GrepSuite) TestDefaultIgnoreSkipsGitDir() {
	mustWrite(s.T(), s.tmpDir, ".git/HEAD", "ref: refs/heads/master\n")
	mustWrite(s.T(), s.tmpDir, "src/main.go", "package main\n// findme\n")
	res := mustGrep(s.T(), s.tmpDir, map[string]any{
		"pattern": "findme",
		"path":    ".",
	})
	s.Contains(res.Content, "src/main.go")
	s.NotContains(res.Content, ".git/HEAD",
		".git should be hidden by default")
}

// TestMultilineFlag: (?s) is auto-prepended when multiline=true.
func (s *GrepSuite) TestMultilineFlag() {
	mustWrite(s.T(), s.tmpDir, "ml.txt", "before MATCH after\n")
	multi := true
	res := mustGrep(s.T(), s.tmpDir, map[string]any{
		"pattern":   "before.MATCH",
		"path":      "ml.txt",
		"multiline": multi,
	})
	s.Contains(res.Content, "MATCH")
}

// TestOversizeFileSkipped: files > 10MB are skipped + flagged.
//
// We mock this by writing 11MB of zeros and asserting the warning
// appears. Slow-ish (~50ms) so we skip under -short.
func (s *GrepSuite) TestOversizeFileSkipped() {
	if testing.Short() {
		s.T().Skip("writes 11MB; run without -short to exercise")
	}
	big := strings.Repeat("x", 11*1024*1024)
	mustWrite(s.T(), s.tmpDir, "big.txt", big)
	res := mustGrep(s.T(), s.tmpDir, map[string]any{
		"pattern": "x",
		"path":    "big.txt",
	})
	s.Contains(res.Content, "skipped",
		"oversize file should produce a warning")
}

// ============================================================
// matchIgnore (search package's local copy) sanity check
// ============================================================

func TestSearchMatchIgnore(t *testing.T) {
	if matchIgnore(nil, "x", false) {
		t.Error("nil patterns must pass-through")
	}
	if !matchIgnore([]string{".git/"}, ".git", true) {
		t.Error("dir-only pattern must hit a directory")
	}
	if matchIgnore([]string{".git/"}, ".git", false) {
		t.Error("dir-only pattern must NOT hit a regular file")
	}
}

// ============================================================
// helpers
// ============================================================

func mustWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustGrep(t *testing.T, cwd string, args map[string]any) tool.Result {
	t.Helper()
	tl := NewGrep(func() string { return cwd })
	res, err := tl.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("Grep.Invoke: %v", err)
	}
	return res
}
