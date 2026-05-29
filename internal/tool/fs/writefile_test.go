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
// WriteFileSuite — testkit baseline + tool-specific cases
// ============================================================

type WriteFileSuite struct {
	testkit.ToolTestSuite
	tmpDir string
}

func TestWriteFile(t *testing.T) {
	s := new(WriteFileSuite)
	s.NewTool = func() tool.Tool {
		return NewWriteFile(func() string { return s.tmpDir })
	}
	suite.Run(t, s)
}

func (s *WriteFileSuite) SetupTest() {
	s.tmpDir = s.T().TempDir()
	// Happy-path arg: writes a tiny file at <tmp>/hello.txt.
	s.HappyArgs = map[string]any{
		"path":    "hello.txt",
		"content": "hello world\n",
	}
}

func (s *WriteFileSuite) TestSchemaGolden() {
	s.GoldenPath = filepath.Join("testdata", "write_file.schema.golden.json")
	s.ToolTestSuite.TestSchemaGolden()
}

// ------------------------------------------------------------
// Happy-path
// ------------------------------------------------------------

func (s *WriteFileSuite) TestCreatesFile() {
	res, err := s.NewTool().Invoke(context.Background(), s.HappyArgs)
	s.Require().NoError(err)
	s.Contains(res.Display, "created")
	s.Contains(res.Content, "<write_file ")
	s.Contains(res.Content, "</write_file>")

	got, err := os.ReadFile(filepath.Join(s.tmpDir, "hello.txt"))
	s.Require().NoError(err)
	s.Equal("hello world\n", string(got))
}

func (s *WriteFileSuite) TestOverwritesExisting() {
	// Pre-create.
	target := filepath.Join(s.tmpDir, "hello.txt")
	s.Require().NoError(os.WriteFile(target, []byte("OLD"), 0o644))

	res, err := s.NewTool().Invoke(context.Background(), s.HappyArgs)
	s.Require().NoError(err)
	s.Contains(res.Display, "overwritten")

	got, err := os.ReadFile(target)
	s.Require().NoError(err)
	s.Equal("hello world\n", string(got))
}

func (s *WriteFileSuite) TestEmptyContentAllowed() {
	res, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":    "empty.txt",
		"content": "",
	})
	s.Require().NoError(err)
	s.Contains(res.Display, "created")

	st, err := os.Stat(filepath.Join(s.tmpDir, "empty.txt"))
	s.Require().NoError(err)
	s.Equal(int64(0), st.Size())
}

// ------------------------------------------------------------
// mkdir_parents
// ------------------------------------------------------------

func (s *WriteFileSuite) TestMkdirParentsDefaultTrue() {
	res, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":    "nested/dir/x.txt",
		"content": "deep",
	})
	s.Require().NoError(err)
	s.NotEmpty(res.Display)

	got, err := os.ReadFile(filepath.Join(s.tmpDir, "nested/dir/x.txt"))
	s.Require().NoError(err)
	s.Equal("deep", string(got))
}

func (s *WriteFileSuite) TestMkdirParentsFalseFailsClean() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":          "missing/x.txt",
		"content":       "x",
		"mkdir_parents": false,
	})
	s.requireToolErr(err, tool.ErrNotFound)
}

func (s *WriteFileSuite) TestMkdirParentsExplicitTrue() {
	mkdir := true
	_ = mkdir
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":          "deep/path/y.txt",
		"content":       "y",
		"mkdir_parents": true,
	})
	s.Require().NoError(err)
}

// ------------------------------------------------------------
// Error paths
// ------------------------------------------------------------

func (s *WriteFileSuite) TestEmptyPathRejected() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":    "",
		"content": "x",
	})
	s.requireToolErr(err, tool.ErrInvalidArgs)
}

func (s *WriteFileSuite) TestUnknownFieldRejected() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":     "a.txt",
		"content":  "x",
		"weird_xx": 1,
	})
	s.requireToolErr(err, tool.ErrInvalidArgs)
}

func (s *WriteFileSuite) TestContentTooLargeRejected() {
	big := strings.Repeat("a", writeFileMaxBytes+1)
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":    "big.txt",
		"content": big,
	})
	s.requireToolErr(err, tool.ErrTooLarge)
}

// ------------------------------------------------------------
// Atomicity sketch
// ------------------------------------------------------------

// TestAtomicWriteCleansUpOnNoFailure verifies that a successful
// happy path leaves no .mini-agent-write-* temp files around.
// (We can't easily inject a rename failure without complex fs
// hooks, but we can at least verify the happy path is clean.)
func (s *WriteFileSuite) TestAtomicWriteCleansUp() {
	_, err := s.NewTool().Invoke(context.Background(), s.HappyArgs)
	s.Require().NoError(err)

	entries, err := os.ReadDir(s.tmpDir)
	s.Require().NoError(err)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".mini-agent-write-") {
			s.T().Errorf("leftover temp file %q", e.Name())
		}
	}
}

// ------------------------------------------------------------
// helper (per-suite to avoid coupling)
// ------------------------------------------------------------

func (s *WriteFileSuite) requireToolErr(err error, code tool.ErrorCode) {
	s.T().Helper()
	s.Require().Error(err)
	var te *tool.Error
	if !errors.As(err, &te) {
		s.FailNowf("not a *tool.Error", "got %T: %v", err, err)
	}
	s.Equal(code, te.Code, "wrong code; full message: %s", te.Message)
}
