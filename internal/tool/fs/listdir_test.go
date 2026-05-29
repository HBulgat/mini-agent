// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

package fs

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
// Test suite — embeds the shared testkit baseline + per-tool cases.
// ============================================================

// ListDirSuite injects the tool factory and a tmpDir-resident
// happy-path fixture. SetupTest re-creates the tmpDir for every test
// method so cases stay isolated.
type ListDirSuite struct {
	testkit.ToolTestSuite

	tmpDir string
}

// TestListDir is the test entry point.
func TestListDir(t *testing.T) {
	s := new(ListDirSuite)
	s.NewTool = func() tool.Tool {
		return NewListDir(func() string { return s.tmpDir })
	}
	suite.Run(t, s)
}

// SetupTest builds a tiny fixture directory and points HappyArgs at
// the listing root ".".
func (s *ListDirSuite) SetupTest() {
	s.tmpDir = s.T().TempDir()
	s.Require().NoError(os.WriteFile(
		filepath.Join(s.tmpDir, "a.txt"), []byte("hi"), 0o644))
	s.HappyArgs = map[string]any{"path": "."}
}

// TestSchemaGolden pins the schema's golden file for D83 compare.
func (s *ListDirSuite) TestSchemaGolden() {
	s.GoldenPath = filepath.Join("testdata", "list_dir.schema.golden.json")
	s.ToolTestSuite.TestSchemaGolden()
}

// ------------------------------------------------------------
// Tool-specific tests
// ------------------------------------------------------------

// TestNonRecursiveListsDirectChildrenOnly is the default-mode happy
// path: only top-level entries appear.
func (s *ListDirSuite) TestNonRecursiveListsDirectChildrenOnly() {
	root := makeTree(s.T(), map[string]string{
		"a.txt":         "hi",
		"sub/b.txt":     "deep",
		"sub/inner/c":   "deeper",
		"README.md":     "docs",
	})
	res := mustListDir(s.T(), root, map[string]any{"path": "."})
	s.Contains(res.Content, "a.txt")
	s.Contains(res.Content, "README.md")
	s.NotContains(res.Content, "b.txt", "non-recursive must not list nested entries")
}

// TestRecursiveDescends verifies recursive=true emits every entry
// under the root (subject to ignore set).
func (s *ListDirSuite) TestRecursiveDescends() {
	root := makeTree(s.T(), map[string]string{
		"a.txt":     "hi",
		"sub/b.txt": "deep",
	})
	rec := true
	res := mustListDir(s.T(), root, map[string]any{"path": ".", "recursive": rec})
	for _, want := range []string{"a.txt", "sub", "sub/b.txt"} {
		s.Contains(res.Content, want, "recursive listing missing %q", want)
	}
}

// TestDefaultIgnoresSkipDotGit verifies the default ignore set hides
// .git/ contents (a common LLM noise source).
func (s *ListDirSuite) TestDefaultIgnoresSkipDotGit() {
	root := makeTree(s.T(), map[string]string{
		"src/main.go":    "package main",
		".git/HEAD":      "refs/heads/master",
		".git/objects/x": "binary",
		"node_modules/d": "stuff",
	})
	rec := true
	res := mustListDir(s.T(), root, map[string]any{"path": ".", "recursive": rec})
	s.NotContains(res.Content, ".git", ".git should be hidden by default")
	s.NotContains(res.Content, "node_modules", "node_modules should be hidden by default")
	s.Contains(res.Content, "src", "src should still appear")
}

// TestExplicitIgnoreReplacesDefaults: passing ignore_patterns turns
// off the default set entirely.
func (s *ListDirSuite) TestExplicitIgnoreReplacesDefaults() {
	root := makeTree(s.T(), map[string]string{
		"src/main.go": "package main",
		".git/HEAD":   "refs/heads/master",
	})
	rec := true
	res := mustListDir(s.T(), root, map[string]any{
		"path":            ".",
		"recursive":       rec,
		"ignore_patterns": []string{"src/"},
	})
	s.Contains(res.Content, ".git", "explicit ignore should not preserve defaults")
	s.NotContains(res.Content, "src/main.go", "src should be hidden by user pattern")
}

// TestEmptyIgnoreDisablesAllFiltering covers "pass [] to disable
// all filtering".
func (s *ListDirSuite) TestEmptyIgnoreDisablesAllFiltering() {
	root := makeTree(s.T(), map[string]string{
		".git/HEAD":   "refs/heads/master",
		"src/main.go": "code",
	})
	rec := true
	res := mustListDir(s.T(), root, map[string]any{
		"path":            ".",
		"recursive":       rec,
		"ignore_patterns": []string{},
	})
	s.Contains(res.Content, ".git", "empty ignore should disable filtering")
}

// TestNotADirectory: a regular file as path is ErrInvalidArgs.
func (s *ListDirSuite) TestNotADirectory() {
	root := makeTree(s.T(), map[string]string{"a.txt": "hi"})
	tl := NewListDir(func() string { return root })
	_, err := tl.Invoke(context.Background(), map[string]any{"path": "a.txt"})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestPathDoesNotExist surfaces ErrNotFound for absent paths.
func (s *ListDirSuite) TestPathDoesNotExist() {
	root := s.T().TempDir()
	tl := NewListDir(func() string { return root })
	_, err := tl.Invoke(context.Background(), map[string]any{"path": "no-such"})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrNotFound, te.Code)
}

// TestDisplayFormat sanity-checks the D74 format.
func (s *ListDirSuite) TestDisplayFormat() {
	res := mustListDir(s.T(), s.tmpDir, map[string]any{"path": "."})
	s.True(strings.HasPrefix(res.Display, "list_dir . ("),
		"display prefix wrong: %q", res.Display)
	s.Contains(res.Display, "→ ok")
}

// TestEmptyPath rejects "" with ErrInvalidArgs.
func (s *ListDirSuite) TestEmptyPath() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{"path": ""})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestUnknownFieldRejected ensures DisallowUnknownFields fires.
func (s *ListDirSuite) TestUnknownFieldRejected() {
	_, err := s.NewTool().Invoke(context.Background(),
		map[string]any{"path": ".", "bogus": true})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// ============================================================
// matchIgnore exhaustive table (package-private, tested directly)
// ============================================================

// TestMatchIgnore covers the full pattern-matching matrix.
func TestMatchIgnore(t *testing.T) {
	cases := []struct {
		patterns []string
		rel      string
		isDir    bool
		want     bool
		desc     string
	}{
		{[]string{".git/"}, ".git", true, true, "dir-only pattern hits dir"},
		{[]string{".git/"}, ".git", false, false, "dir-only pattern misses file with same name"},
		{[]string{"node_modules/"}, "src/node_modules", true, true, "dir-only seg-match nested"},
		{[]string{"*.pyc"}, "foo.pyc", false, true, "glob basename hit"},
		{[]string{"*.pyc"}, "foo.go", false, false, "glob basename miss"},
		{[]string{"*.pyc"}, "src/foo.pyc", false, true, "glob basename hits in subdir"},
		{[]string{"src/*"}, "src/main.go", false, true, "path glob hit"},
		{[]string{"src/*"}, "lib/main.go", false, false, "path glob miss"},
		{nil, "anything", false, false, "nil patterns = pass-through"},
		{[]string{}, "anything", false, false, "empty patterns = pass-through"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			got := matchIgnore(c.patterns, c.rel, c.isDir)
			if got != c.want {
				t.Errorf("matchIgnore(%v, %q, %v) = %v want %v",
					c.patterns, c.rel, c.isDir, got, c.want)
			}
		})
	}
}

// ============================================================
// Truncation test (lives outside the suite because it creates 5001
// files and needs a non-suite TempDir to avoid sharing with siblings)
// ============================================================

// TestListDir_TruncationAtCap ensures exceeding maxListDirEntries
// surfaces ForcedTruncated=true and the warning marker. Skipped under
// -short because building 5001 files is a few hundred ms.
func TestListDir_TruncationAtCap(t *testing.T) {
	if testing.Short() {
		t.Skip("creates 5001 files; run without -short to exercise")
	}
	root := t.TempDir()
	for i := 0; i <= maxListDirEntries; i++ {
		f := filepath.Join(root, "f"+itoa(i)+".txt")
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tl := NewListDir(func() string { return root })
	res, err := tl.Invoke(context.Background(), map[string]any{"path": "."})
	if err != nil {
		t.Fatal(err)
	}
	if !res.ForcedTruncated {
		t.Error("expected ForcedTruncated=true at the cap")
	}
	if !strings.Contains(res.Content, "truncated") {
		t.Errorf("content should mention truncation; got: %s", res.Content)
	}
	if !strings.Contains(res.Display, "[truncated]") {
		t.Errorf("display should mention truncation; got: %s", res.Display)
	}
}

// ============================================================
// helpers
// ============================================================

// makeTree builds a temporary directory tree from a map of relative
// path → content. Parent directories are created automatically.
func makeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// mustListDir runs ListDir.Invoke and t.Fatals on error.
func mustListDir(t *testing.T, cwd string, args map[string]any) tool.Result {
	t.Helper()
	tl := NewListDir(func() string { return cwd })
	res, err := tl.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("ListDir.Invoke: %v", err)
	}
	return res
}

// itoa is a tiny strconv.Itoa replacement to keep imports tight.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
