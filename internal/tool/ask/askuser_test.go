// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

package ask

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/HBulgat/mini-agent/internal/tool"
	"github.com/HBulgat/mini-agent/internal/tool/testkit"
	"github.com/HBulgat/mini-agent/internal/uio"
)

// ============================================================
// Test fixtures: a programmable Prompter
// ============================================================

// fakePrompter records the AskUser invocations and returns canned
// answers / errors. Other Prompter methods are unused by ask_user but
// must be present to satisfy the interface.
type fakePrompter struct {
	answer string
	err    error

	// captured holds the last QuestionRequest we received.
	captured uio.QuestionRequest
	calls    int
}

func (f *fakePrompter) AskApproval(_ context.Context, _ uio.ApprovalRequest) (uio.ApprovalDecision, error) {
	return uio.DecisionDeny, errors.New("not used in ask_user tests")
}

func (f *fakePrompter) AskUser(_ context.Context, req uio.QuestionRequest) (string, error) {
	f.captured = req
	f.calls++
	return f.answer, f.err
}

func (f *fakePrompter) AskChoice(_ context.Context, _ uio.ChoiceRequest) (string, error) {
	return "", errors.New("not used in ask_user tests")
}

// ============================================================
// Shared testkit suite
// ============================================================

// AskUserSuite wires the testkit baseline to a fakePrompter that
// returns a canned "yes" answer so every happy-path-style test (incl.
// testkit's TestCtxCancel) doesn't block.
type AskUserSuite struct {
	testkit.ToolTestSuite

	prompter *fakePrompter
}

func TestAskUser(t *testing.T) {
	s := new(AskUserSuite)
	s.NewTool = func() tool.Tool {
		return NewAskUser(s.prompter)
	}
	suite.Run(t, s)
}

// SetupTest creates a fresh prompter with a canned reply so the
// testkit's CtxCancel test (which Invokes with HappyArgs) gets a
// quick return rather than blocking.
func (s *AskUserSuite) SetupTest() {
	s.prompter = &fakePrompter{answer: "yes"}
	s.HappyArgs = map[string]any{"question": "Proceed?"}
}

func (s *AskUserSuite) TestSchemaGolden() {
	s.GoldenPath = filepath.Join("testdata", "ask_user.schema.golden.json")
	s.ToolTestSuite.TestSchemaGolden()
}

// ------------------------------------------------------------
// Behaviour
// ------------------------------------------------------------

// TestEchoesAnswer: the operator's reply round-trips into Result.
func (s *AskUserSuite) TestEchoesAnswer() {
	s.prompter.answer = "definitely"
	tl := s.NewTool()
	res, err := tl.Invoke(context.Background(), map[string]any{
		"question": "Are you sure?",
	})
	s.Require().NoError(err)
	s.Contains(res.Content, "<question>Are you sure?</question>")
	s.Contains(res.Content, "<answer>definitely</answer>")
	s.Contains(res.Display, "Are you sure?")
	s.Contains(res.Display, "definitely")
}

// TestPassesHint: optional hint flows through to the prompter.
func (s *AskUserSuite) TestPassesHint() {
	tl := s.NewTool()
	_, err := tl.Invoke(context.Background(), map[string]any{
		"question": "Continue?",
		"hint":     "y/n",
	})
	s.Require().NoError(err)
	s.Equal("Continue?", s.prompter.captured.Question)
	s.Equal("y/n", s.prompter.captured.Hint)
}

// TestSingleInvokeOneCall confirms we don't accidentally re-prompt.
func (s *AskUserSuite) TestSingleInvokeOneCall() {
	tl := s.NewTool()
	_, _ = tl.Invoke(context.Background(), map[string]any{"question": "?"})
	s.Equal(1, s.prompter.calls)
}

// TestEmptyQuestion rejects with ErrInvalidArgs.
func (s *AskUserSuite) TestEmptyQuestion() {
	tl := s.NewTool()
	_, err := tl.Invoke(context.Background(), map[string]any{"question": "   "})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestMissingQuestion rejects with ErrInvalidArgs.
func (s *AskUserSuite) TestMissingQuestion() {
	tl := s.NewTool()
	_, err := tl.Invoke(context.Background(), map[string]any{})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestUnknownFieldRejected: strict decode fires on typos.
func (s *AskUserSuite) TestUnknownFieldRejected() {
	tl := s.NewTool()
	_, err := tl.Invoke(context.Background(), map[string]any{
		"question": "ok?",
		"bogus":    true,
	})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestNilPrompterFailsClosed: a tool wired with nil prompter returns
// ErrInvalidArgs rather than panicking on the call.
func (s *AskUserSuite) TestNilPrompterFailsClosed() {
	tl := NewAskUser(nil)
	_, err := tl.Invoke(context.Background(), map[string]any{"question": "?"})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrInvalidArgs, te.Code)
}

// TestPrompterErrorPropagates: a non-ctx prompter error becomes ErrIO.
func (s *AskUserSuite) TestPrompterErrorPropagates() {
	s.prompter.err = errors.New("stdin closed")
	tl := s.NewTool()
	_, err := tl.Invoke(context.Background(), map[string]any{"question": "?"})
	s.Require().Error(err)
	var te *tool.Error
	s.True(errors.As(err, &te))
	s.Equal(tool.ErrIO, te.Code)
}

// TestCategoryIsMeta — Meta tools bypass write/execute approvals so
// the gate's matrixDecision must always allow them.
func (s *AskUserSuite) TestCategoryIsMeta() {
	tl := s.NewTool()
	s.Equal(tool.CategoryMeta, tl.Category())
}

// TestEscapesXMLChars: < > & in question/answer don't corrupt the tag.
func (s *AskUserSuite) TestEscapesXMLChars() {
	s.prompter.answer = "answer with <tag> & ampersand"
	tl := s.NewTool()
	res, err := tl.Invoke(context.Background(), map[string]any{
		"question": "what about <html> & xml?",
	})
	s.Require().NoError(err)
	s.Contains(res.Content, "&lt;html&gt;")
	s.Contains(res.Content, "&amp;")
	s.Contains(res.Content, "&lt;tag&gt;")
}

// ============================================================
// Pure-function tests
// ============================================================

// TestSnippet covers the trace-line truncation helper.
func TestSnippet(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"hi", 60, "hi"},
		{strings.Repeat("a", 60), 60, strings.Repeat("a", 60)},
		{strings.Repeat("a", 61), 60, strings.Repeat("a", 57) + "..."},
		{"", 60, ""},
	}
	for _, c := range cases {
		if got := snippet(c.in, c.n); got != c.want {
			t.Errorf("snippet(%q, %d) = %q; want %q", c.in, c.n, got, c.want)
		}
	}
}

// TestEscapeForTag covers the minimal XML-ish escape.
func TestEscapeForTag(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{"a<b", "a&lt;b"},
		{"a>b", "a&gt;b"},
		{"a&b", "a&amp;b"},
		{"a<b>&c", "a&lt;b&gt;&amp;c"},
		{"", ""},
	}
	for _, c := range cases {
		if got := escapeForTag(c.in); got != c.want {
			t.Errorf("escapeForTag(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}
