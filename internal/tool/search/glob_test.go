// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

package search

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/HBulgat/mini-agent/internal/tool"
	"github.com/HBulgat/mini-agent/internal/tool/testkit"
)

// GlobSuite injects the tool factory.
type GlobSuite struct {
	testkit.ToolTestSuite

	tmpDir string
}

func TestGlob(t *testing.T) {
	s := new(GlobSuite)
	s.NewTool = func() tool.Tool {
		return NewGlob(func() string { return s.tmpDir })
	}
	suite.Run(t, s)
}

func (s *GlobSuite) SetupTest() {
	s.tmpDir = s.T().TempDir()
	mustWrite(s.T(), s.tmpDir, "a.go", "// a")
	mustWrite(s.T(), s.tmpDir, "src/b.go", "// b")
	mustWrite(s.T(), s.tmpDir, "src/inner/c.go", "// c")
	mustWrite(s.T(), s.tmpDir, "README.md", "# readme")
	s.HappyArgs = map[string]any{"pattern": "*.go"}
}

func (s *GlobSuite) TestSchemaGolden() {
	s.GoldenPath = filepath.Join("testdata", "glob.schema.golden.json")
	s.ToolTestSuite.TestSchemaGolden()
}

// ------------------------------------------------------------
// Behaviour
// ------------------------------------------------------------

// TestSimpleGlob: top-level *.go matches a.go but not nested files.
func (s *GlobSuite) TestSimpleGlob() {
	res := mustGlob(s.T(), s.tmpDir, map[string]any{"pattern": "*.go"})
	s.Contains(res.Content, "a.go")
	s.NotContains(res.Content, "b.go", "non-recursive must not match nested")
}

// TestDoubleStar: **/* recurses into every directory.
func (s *GlobSuite) TestDoubleStar() {
	res := mustGlob(s.T(), s.tmpDir, map[string]any{"pattern": "**/*.go"})
	s.Contains(res.Content, "a.go")
	s.Contains(res.Content, "src/b.go")
	s.Contains(res.Content, "src/inner/c.go")
}

// TestSubdirectoryGlob: src/**/* limits the walk to that subtree.
func (s *GlobSuite) TestSubdirectoryGlob() {
	res := mustGlob(s.T(), s.tmpDir, map[string]any{"pattern": "src/**/*.go"})
	s.Contains(res.Content, "src/b.go")
	s.Contains(res.Content, "src/inner/c.go")
	s.NotContains(res.Content, "a.go")
}

// TestAlternation: {go,md} in doublestar.
func (s *GlobSuite) TestAlternation() {
	res := mustGlob(s.T(), s.tmpDir, map[string]any{"pattern": "*.{go,md}"})
	s.Contains(res.Content, "a.go")
	s.Contains(res.Content, "README.md")
}

// TestNoMatches still succeeds — the result just has 0 entries.
func (s *GlobSuite) TestNoMatches() {
	res := mustGlob(s.T(), s.tmpDir, map[string]any{"pattern": "*.rs"})
	s.Contains(res.Content, "matches=0")
}

// TestMissingPattern rejects empty pattern.
func (s *GlobSuite) TestMissingPattern() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestInvalidPattern rejects malformed glob syntax.
func (s *GlobSuite) TestInvalidPattern() {
	// Unclosed bracket is ill-formed in doublestar.
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"pattern": "[unterminated",
	})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestUnknownFieldRejected: strict decode.
func (s *GlobSuite) TestUnknownFieldRejected() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"pattern": "*.go",
		"bogus":   true,
	})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestNoDefaultIgnore: glob deliberately does NOT skip .git/ — by
// design (it's the user's pattern that controls scope).
func (s *GlobSuite) TestNoDefaultIgnore() {
	mustWrite(s.T(), s.tmpDir, ".git/HEAD", "ref")
	res := mustGlob(s.T(), s.tmpDir, map[string]any{"pattern": "**/HEAD"})
	s.Contains(res.Content, ".git/HEAD",
		"glob should NOT apply default ignore set")
}

// TestSorted: results come back sorted by path so output is stable.
func (s *GlobSuite) TestSorted() {
	res := mustGlob(s.T(), s.tmpDir, map[string]any{"pattern": "**/*.go"})
	// Find positions of each go file in the content.
	aIdx := strings.Index(res.Content, "a.go")
	bIdx := strings.Index(res.Content, "src/b.go")
	cIdx := strings.Index(res.Content, "src/inner/c.go")
	s.True(aIdx >= 0 && bIdx >= 0 && cIdx >= 0)
	s.Less(aIdx, bIdx, "a.go should come before src/b.go")
	s.Less(bIdx, cIdx, "src/b.go should come before src/inner/c.go")
}

// TestDisplayFormat sanity-checks D74 format.
func (s *GlobSuite) TestDisplayFormat() {
	res := mustGlob(s.T(), s.tmpDir, map[string]any{"pattern": "*.go"})
	s.True(strings.HasPrefix(res.Display, "glob *.go ("),
		"display prefix wrong: %q", res.Display)
	s.Contains(res.Display, "→ ok")
}

// TestSplitAbs covers the absolute-pattern handling helper.
func TestSplitAbs(t *testing.T) {
	cases := []struct {
		in       string
		wantRoot string
		wantRel  string
	}{
		{"/usr/lib/*.so", "/usr/lib", "*.so"},
		{"/etc/*", "/etc", "*"},
		{"/home/u/**/*.go", "/home/u", "**/*.go"},
		{"/var/log/syslog", "/var/log/syslog", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			gotRoot, gotRel := splitAbs(c.in)
			if gotRoot != c.wantRoot || gotRel != c.wantRel {
				t.Errorf("splitAbs(%q) = (%q, %q), want (%q, %q)",
					c.in, gotRoot, gotRel, c.wantRoot, c.wantRel)
			}
		})
	}
}

// ============================================================
// helpers
// ============================================================

func mustGlob(t *testing.T, cwd string, args map[string]any) tool.Result {
	t.Helper()
	tl := NewGlob(func() string { return cwd })
	res, err := tl.Invoke(context.Background(), args)
	if err != nil {
		t.Fatalf("Glob.Invoke: %v", err)
	}
	return res
}
