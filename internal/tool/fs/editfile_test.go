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
// EditFileSuite — testkit baseline + tool-specific cases
// ============================================================

type EditFileSuite struct {
	testkit.ToolTestSuite
	tmpDir string
}

func TestEditFile(t *testing.T) {
	s := new(EditFileSuite)
	s.NewTool = func() tool.Tool {
		return NewEditFile(func() string { return s.tmpDir })
	}
	suite.Run(t, s)
}

func (s *EditFileSuite) SetupTest() {
	s.tmpDir = s.T().TempDir()
	// Pre-create a small fixture so the happy-path call has a target.
	target := filepath.Join(s.tmpDir, "fix.go")
	s.Require().NoError(os.WriteFile(target,
		[]byte("package main\n\nfunc Hello() string { return \"old\" }\n"), 0o644))
	s.HappyArgs = map[string]any{
		"path":    "fix.go",
		"old_str": "old",
		"new_str": "new",
	}
}

func (s *EditFileSuite) TestSchemaGolden() {
	s.GoldenPath = filepath.Join("testdata", "edit_file.schema.golden.json")
	s.ToolTestSuite.TestSchemaGolden()
}

// ------------------------------------------------------------
// Happy paths
// ------------------------------------------------------------

func (s *EditFileSuite) TestSingleReplace() {
	res, err := s.NewTool().Invoke(context.Background(), s.HappyArgs)
	s.Require().NoError(err)
	s.Contains(res.Display, "edit_file fix.go")
	s.Contains(res.Display, "1 replacement")

	got, err := os.ReadFile(filepath.Join(s.tmpDir, "fix.go"))
	s.Require().NoError(err)
	s.Contains(string(got), `return "new"`)
	s.NotContains(string(got), `return "old"`)
}

func (s *EditFileSuite) TestEmptyNewStrDeletes() {
	target := filepath.Join(s.tmpDir, "drop.txt")
	s.Require().NoError(os.WriteFile(target, []byte("keep DELETE keep\n"), 0o644))

	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":    "drop.txt",
		"old_str": " DELETE",
		"new_str": "",
	})
	s.Require().NoError(err)

	got, err := os.ReadFile(target)
	s.Require().NoError(err)
	s.Equal("keep keep\n", string(got))
}

func (s *EditFileSuite) TestExpectedOccurrencesGlobalReplace() {
	target := filepath.Join(s.tmpDir, "many.txt")
	s.Require().NoError(os.WriteFile(target, []byte("foo\nfoo\nfoo\n"), 0o644))

	expect := 3
	res, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":                 "many.txt",
		"old_str":              "foo",
		"new_str":              "bar",
		"expected_occurrences": expect,
	})
	s.Require().NoError(err)
	s.Contains(res.Display, "3 replacements")

	got, err := os.ReadFile(target)
	s.Require().NoError(err)
	s.Equal("bar\nbar\nbar\n", string(got))
}

// ------------------------------------------------------------
// Error paths
// ------------------------------------------------------------

func (s *EditFileSuite) TestNotFoundOldStr() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":    "fix.go",
		"old_str": "this string does not exist",
		"new_str": "x",
	})
	s.requireToolErr(err, tool.ErrNotFound)
}

func (s *EditFileSuite) TestAmbiguousMatchSingle() {
	target := filepath.Join(s.tmpDir, "ambig.txt")
	s.Require().NoError(os.WriteFile(target, []byte("dup\nfiller\ndup\nfiller\ndup\n"), 0o644))

	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":    "ambig.txt",
		"old_str": "dup",
		"new_str": "x",
	})
	s.requireToolErr(err, tool.ErrAmbiguous)

	// Error message should include the first 3 line numbers.
	for _, want := range []string{"1", "3", "5"} {
		s.Contains(err.Error(), want, "error should mention line %s", want)
	}
}

func (s *EditFileSuite) TestAmbiguousMatchExpectedMismatch() {
	target := filepath.Join(s.tmpDir, "miscount.txt")
	s.Require().NoError(os.WriteFile(target, []byte("a a a\n"), 0o644))

	expect := 2
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":                 "miscount.txt",
		"old_str":              "a",
		"new_str":              "b",
		"expected_occurrences": expect,
	})
	s.requireToolErr(err, tool.ErrAmbiguous)
}

func (s *EditFileSuite) TestRefuseDirectoryEdit() {
	dir := filepath.Join(s.tmpDir, "subdir")
	s.Require().NoError(os.MkdirAll(dir, 0o755))

	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":    "subdir",
		"old_str": "x",
		"new_str": "y",
	})
	s.requireToolErr(err, tool.ErrInvalidArgs)
	s.Contains(err.Error(), "directory")
}

func (s *EditFileSuite) TestRefuseMissingFile() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":    "no_such_file.txt",
		"old_str": "x",
		"new_str": "y",
	})
	s.requireToolErr(err, tool.ErrNotFound)
}

func (s *EditFileSuite) TestRejectEmptyOldStr() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":    "fix.go",
		"old_str": "",
		"new_str": "y",
	})
	s.requireToolErr(err, tool.ErrInvalidArgs)
}

func (s *EditFileSuite) TestRejectIdenticalOldNew() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":    "fix.go",
		"old_str": "old",
		"new_str": "old",
	})
	s.requireToolErr(err, tool.ErrInvalidArgs)
	s.Contains(err.Error(), "nothing would change")
}

func (s *EditFileSuite) TestRejectExpectedZero() {
	zero := 0
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":                 "fix.go",
		"old_str":              "old",
		"new_str":              "new",
		"expected_occurrences": zero,
	})
	s.requireToolErr(err, tool.ErrInvalidArgs)
}

func (s *EditFileSuite) TestRejectFileTooLarge() {
	target := filepath.Join(s.tmpDir, "huge.bin")
	// 3 MB > editFileMaxBytes (2 MB)
	big := strings.Repeat("a", editFileMaxBytes+100)
	s.Require().NoError(os.WriteFile(target, []byte(big), 0o644))

	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":    "huge.bin",
		"old_str": "a",
		"new_str": "b",
	})
	s.requireToolErr(err, tool.ErrTooLarge)
}

func (s *EditFileSuite) TestRejectUnknownField() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":    "fix.go",
		"old_str": "old",
		"new_str": "new",
		"weird":   1,
	})
	s.requireToolErr(err, tool.ErrInvalidArgs)
}

// ------------------------------------------------------------
// findOccurrenceLines — internal helper
// ------------------------------------------------------------

func TestFindOccurrenceLines(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		needle string
		max    int
		want   []int
	}{
		{"simple", "alpha\nbeta\nalpha\n", "alpha", 5, []int{1, 3}},
		{"max caps results", "x\nx\nx\nx\nx\n", "x", 3, []int{1, 2, 3}},
		{"needle at start", "abc\ndef\n", "abc", 5, []int{1}},
		{"empty needle", "x\n", "", 5, nil},
		{"empty raw", "", "x", 5, nil},
		{"max zero", "x\n", "x", 0, nil},
		{"no match", "alpha\n", "zeta", 5, []int{}},
		{
			"multi-line needle",
			"a\nstart\nmid\nend\nb\nstart\nmid\nend\n",
			"start\nmid\nend",
			5,
			[]int{2, 6},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := findOccurrenceLines([]byte(c.raw), c.needle, c.max)
			// Normalise the "no match" case: our impl returns
			// the empty slice from len(hits) > 0 path; nil and []int{}
			// are equivalent for this test.
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if len(got) != len(c.want) {
				t.Fatalf("len mismatch: got %v want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("line[%d] = %d, want %d", i, got[i], c.want[i])
				}
			}
		})
	}
}

// ------------------------------------------------------------
// helpers
// ------------------------------------------------------------

func (s *EditFileSuite) requireToolErr(err error, code tool.ErrorCode) {
	s.T().Helper()
	s.Require().Error(err)
	var te *tool.Error
	if !errors.As(err, &te) {
		s.FailNowf("not a *tool.Error", "got %T: %v", err, err)
	}
	s.Equal(code, te.Code, "wrong code; full message: %s", te.Message)
}
