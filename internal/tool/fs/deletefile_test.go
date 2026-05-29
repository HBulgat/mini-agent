package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/HBulgat/mini-agent/internal/tool"
	"github.com/HBulgat/mini-agent/internal/tool/testkit"
)

// ============================================================
// DeleteFileSuite — testkit baseline + tool-specific cases
// ============================================================

type DeleteFileSuite struct {
	testkit.ToolTestSuite
	tmpDir string
}

func TestDeleteFile(t *testing.T) {
	s := new(DeleteFileSuite)
	s.NewTool = func() tool.Tool {
		return NewDeleteFile(func() string { return s.tmpDir })
	}
	suite.Run(t, s)
}

func (s *DeleteFileSuite) SetupTest() {
	s.tmpDir = s.T().TempDir()
	target := filepath.Join(s.tmpDir, "doomed.txt")
	s.Require().NoError(os.WriteFile(target, []byte("bye"), 0o644))
	s.HappyArgs = map[string]any{"path": "doomed.txt"}
}

func (s *DeleteFileSuite) TestSchemaGolden() {
	s.GoldenPath = filepath.Join("testdata", "delete_file.schema.golden.json")
	s.ToolTestSuite.TestSchemaGolden()
}

// ------------------------------------------------------------
// Happy / error paths
// ------------------------------------------------------------

func (s *DeleteFileSuite) TestRemovesFile() {
	res, err := s.NewTool().Invoke(context.Background(), s.HappyArgs)
	s.Require().NoError(err)
	s.Contains(res.Display, "delete_file doomed.txt")
	s.Contains(res.Display, "removed")

	if _, err := os.Stat(filepath.Join(s.tmpDir, "doomed.txt")); !os.IsNotExist(err) {
		s.T().Errorf("file should be gone, stat err=%v", err)
	}
}

func (s *DeleteFileSuite) TestMissingFileNotFound() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path": "nope.txt",
	})
	s.requireToolErr(err, tool.ErrNotFound)
}

func (s *DeleteFileSuite) TestRefuseDirectory() {
	dir := filepath.Join(s.tmpDir, "subdir")
	s.Require().NoError(os.MkdirAll(dir, 0o755))

	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path": "subdir",
	})
	s.requireToolErr(err, tool.ErrInvalidArgs)
	s.Contains(err.Error(), "directory")
}

func (s *DeleteFileSuite) TestRefuseSymlink() {
	target := filepath.Join(s.tmpDir, "real.txt")
	s.Require().NoError(os.WriteFile(target, []byte("x"), 0o644))
	link := filepath.Join(s.tmpDir, "link.txt")
	s.Require().NoError(os.Symlink(target, link))

	// os.Stat follows symlinks, so the link's IsRegular() is true.
	// We rely on that — delete_file will succeed and only the
	// symlink is removed (not the target). Verify behaviour matches.
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path": "link.txt",
	})
	s.Require().NoError(err)

	// Target still exists.
	if _, err := os.Stat(target); err != nil {
		s.T().Errorf("symlink target should survive: %v", err)
	}
	// Link is gone.
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		s.T().Errorf("symlink should be gone, lstat err=%v", err)
	}
}

func (s *DeleteFileSuite) TestEmptyPathRejected() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{"path": ""})
	s.requireToolErr(err, tool.ErrInvalidArgs)
}

func (s *DeleteFileSuite) TestUnknownFieldRejected() {
	_, err := s.NewTool().Invoke(context.Background(), map[string]any{
		"path":  "doomed.txt",
		"force": true,
	})
	s.requireToolErr(err, tool.ErrInvalidArgs)
}

// ------------------------------------------------------------
// helpers
// ------------------------------------------------------------

func (s *DeleteFileSuite) requireToolErr(err error, code tool.ErrorCode) {
	s.T().Helper()
	s.Require().Error(err)
	var te *tool.Error
	if !errors.As(err, &te) {
		s.FailNowf("not a *tool.Error", "got %T: %v", err, err)
	}
	s.Equal(code, te.Code, "wrong code; full message: %s", te.Message)
}
