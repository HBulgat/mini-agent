package permission

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HBulgat/mini-agent/internal/tool"
	"github.com/HBulgat/mini-agent/internal/uio"
)

// ============================================================
// Helpers
// ============================================================

// emptyRules returns a RuleSet containing only the hard blacklist.
// Used when a test wants to focus on matrix / approval behaviour
// without user rules interfering.
func emptyRules() *RuleSet {
	return &RuleSet{HardDenylist: hardDenylist()}
}

// fakePrompter is a uio.Prompter that returns a pre-baked decision
// (or error) without UI. It records how many times AskApproval was
// called so tests can verify the gate avoided / triggered the prompt.
type fakePrompter struct {
	dec   uio.ApprovalDecision
	err   error
	calls int
	last  uio.ApprovalRequest
}

func (f *fakePrompter) AskApproval(_ context.Context, req uio.ApprovalRequest) (uio.ApprovalDecision, error) {
	f.calls++
	f.last = req
	return f.dec, f.err
}
func (f *fakePrompter) AskUser(_ context.Context, _ uio.QuestionRequest) (string, error) {
	return "", errors.New("not used")
}
func (f *fakePrompter) AskChoice(_ context.Context, _ uio.ChoiceRequest) (string, error) {
	return "", errors.New("not used")
}

// ============================================================
// Substitutor
// ============================================================

func TestSubstitutor_Expand(t *testing.T) {
	s := &Substitutor{Cwd: "/proj/main", Home: "/home/u"}

	cases := []struct {
		in, want string
	}{
		{"${cwd}/foo", "/proj/main/foo"},
		{"$cwd/foo", "/proj/main/foo"}, // tolerated un-braced form
		{"${home}/.config", "/home/u/.config"},
		{"$home/.config", "/home/u/.config"},
		{"~/foo", "/home/u/foo"},
		{"~", "/home/u"},
		{"/etc/passwd", "/etc/passwd"},
		{"/proj/~/embedded", "/proj/~/embedded"}, // mid-string ~ left alone
	}
	for _, c := range cases {
		if got := s.Expand(c.in); got != c.want {
			t.Errorf("Expand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSubstitutor_EmptyDoesNothing(t *testing.T) {
	s := &Substitutor{} // both empty
	in := "${cwd}/${home}/~"
	if got := s.Expand(in); got != in {
		t.Errorf("Expand should leave unset vars untouched; got %q", got)
	}
}

// ============================================================
// matchUserRule
// ============================================================

func TestMatchUserRule_Tool(t *testing.T) {
	r := &UserRule{Type: RuleDeny, Granularity: GranTool, Pattern: "web_search"}
	op := &Operation{ToolName: "web_search"}
	if !matchUserRule(r, op, NewSubstitutor("")) {
		t.Error("exact tool name should match")
	}
	op.ToolName = "web_fetch"
	if matchUserRule(r, op, NewSubstitutor("")) {
		t.Error("different tool name should not match")
	}
}

func TestMatchUserRule_CommandOnlyAppliesToBash(t *testing.T) {
	r := &UserRule{Type: RuleDeny, Granularity: GranCommand, Pattern: "rm *"}

	// bash: matches
	op := &Operation{ToolName: "bash", Command: "rm foo.txt"}
	if !matchUserRule(r, op, NewSubstitutor("")) {
		t.Error("bash command rule should fire on rm")
	}
	// non-bash: skipped even if Command is somehow present
	op = &Operation{ToolName: "read_file", Command: "rm foo.txt"}
	if matchUserRule(r, op, NewSubstitutor("")) {
		t.Error("command rule should ignore non-bash tools")
	}
}

func TestMatchUserRule_PathDoublestar(t *testing.T) {
	r := &UserRule{Type: RuleAllow, Granularity: GranPath, Pattern: "${cwd}/**"}
	sub := &Substitutor{Cwd: "/proj"}

	// nested file inside cwd
	op := &Operation{ToolName: "read_file", Path: "/proj/sub/dir/file.go"}
	if !matchUserRule(r, op, sub) {
		t.Error("** glob should match nested file inside cwd")
	}
	// outside cwd
	op = &Operation{ToolName: "read_file", Path: "/elsewhere/x.go"}
	if matchUserRule(r, op, sub) {
		t.Error("path outside cwd should not match")
	}
}

func TestMatchUserRule_ToolNameScope(t *testing.T) {
	// Path rule scoped to one tool
	r := &UserRule{
		Type:        RuleAllow,
		Granularity: GranPath,
		ToolName:    "read_file",
		Pattern:     "/etc/**",
	}
	op := &Operation{ToolName: "read_file", Path: "/etc/hosts"}
	if !matchUserRule(r, op, NewSubstitutor("")) {
		t.Error("scoped rule should fire for matching tool")
	}
	op = &Operation{ToolName: "write_file", Path: "/etc/hosts"}
	if matchUserRule(r, op, NewSubstitutor("")) {
		t.Error("scoped rule should NOT fire for other tools")
	}
}

// ============================================================
// matchHardDeny
// ============================================================

func TestMatchHardDeny_RmRfRoot(t *testing.T) {
	rules := hardDenylist()
	for _, cmd := range []string{
		"rm -rf /",
		"rm -rf /*",
		"rm  -rf  /", // extra spaces — normaliseSpaces collapses them
	} {
		op := &Operation{ToolName: "bash", Command: cmd}
		hit := false
		for _, r := range rules {
			if matchHardDeny(r, op) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("hard blacklist missed dangerous cmd %q", cmd)
		}
	}
}

func TestMatchHardDeny_ForkBomb(t *testing.T) {
	rules := hardDenylist()
	for _, cmd := range []string{
		":(){:|:&};:",
		":(){ :|:& };:", // canonical spelling with spaces
	} {
		op := &Operation{ToolName: "bash", Command: cmd}
		hit := false
		for _, r := range rules {
			if matchHardDeny(r, op) {
				hit = true
				break
			}
		}
		if !hit {
			t.Errorf("fork bomb spelling %q should be blocked", cmd)
		}
	}
}

func TestMatchHardDeny_PathToolEtc(t *testing.T) {
	rules := hardDenylist()
	op := &Operation{ToolName: "write_file", Path: "/etc/passwd"}
	hit := false
	for _, r := range rules {
		if matchHardDeny(r, op) {
			hit = true
			break
		}
	}
	if !hit {
		t.Error("write_file → /etc/passwd should be hard-blocked")
	}
}

func TestMatchHardDeny_HarmlessCmd(t *testing.T) {
	rules := hardDenylist()
	op := &Operation{ToolName: "bash", Command: "ls -la"}
	for _, r := range rules {
		if matchHardDeny(r, op) {
			t.Errorf("harmless ls should not match hard rule %q", r.Pattern)
		}
	}
}

// ============================================================
// matrixDecision
// ============================================================

func TestMatrixDecision(t *testing.T) {
	cases := []struct {
		name string
		mode Mode
		cat  tool.Category
		want Decision
	}{
		{"default+read-only allows", ModeDefault, tool.CategoryReadOnly, DecisionAllow},
		{"default+meta allows", ModeDefault, tool.CategoryMeta, DecisionAllow},
		{"default+write asks", ModeDefault, tool.CategoryWrite, DecisionNeedApproval},
		{"default+execute asks", ModeDefault, tool.CategoryExecute, DecisionNeedApproval},
		{"default+network asks", ModeDefault, tool.CategoryNetwork, DecisionNeedApproval},

		{"auto-edit+write allows", ModeAutoEdit, tool.CategoryWrite, DecisionAllow},
		{"auto-edit+execute asks", ModeAutoEdit, tool.CategoryExecute, DecisionNeedApproval},
		{"auto-edit+network asks", ModeAutoEdit, tool.CategoryNetwork, DecisionNeedApproval},

		{"yes+execute allows", ModeYes, tool.CategoryExecute, DecisionAllow},
		{"yes+network allows", ModeYes, tool.CategoryNetwork, DecisionAllow},
		{"yes+write allows", ModeYes, tool.CategoryWrite, DecisionAllow},

		{"plan+write denies", ModePlan, tool.CategoryWrite, DecisionDeny},
		{"plan+execute denies", ModePlan, tool.CategoryExecute, DecisionDeny},
		{"plan+network denies", ModePlan, tool.CategoryNetwork, DecisionDeny},
		{"plan+read-only allows", ModePlan, tool.CategoryReadOnly, DecisionAllow},
		{"plan+meta allows", ModePlan, tool.CategoryMeta, DecisionAllow},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := matrixDecision(c.mode, c.cat); got != c.want {
				t.Errorf("matrixDecision(%v, %v) = %v, want %v", c.mode, c.cat, got, c.want)
			}
		})
	}
}

// ============================================================
// CheckRulesOnly — full 4-step path without prompting
// ============================================================

func TestCheckRulesOnly_HardBlacklistOverridesYesMode(t *testing.T) {
	g, err := NewGate(emptyRules(), ModeYes, "/proj")
	if err != nil {
		t.Fatal(err)
	}
	op := Operation{ToolName: "bash", Category: tool.CategoryExecute, Command: "rm -rf /"}
	r := g.CheckRulesOnly(op, ModeYes)
	if r.Decision != DecisionDenyHard {
		t.Fatalf("want DenyHard, got %v (reason: %s)", r.Decision, r.Reason)
	}
}

func TestCheckRulesOnly_UserDenyBeforeAllow(t *testing.T) {
	rs := emptyRules()
	rs.UserRules = []*UserRule{
		// We use a non-blacklisted command so the assertion measures
		// user-rule precedence (deny-before-allow) rather than the
		// hard blacklist short-circuit. T2.4 expanded the blacklist
		// to cover "git push --force origin main", so we test on a
		// branch that's safe to push.
		{Type: RuleDeny, Granularity: GranCommand, Pattern: "git push --force* feature*"},
		{Type: RuleAllow, Granularity: GranCommand, Pattern: "git *"},
	}
	g, _ := NewGate(rs, ModeDefault, "/proj")
	op := Operation{ToolName: "bash", Category: tool.CategoryExecute, Command: "git push --force origin feature-x"}
	r := g.CheckRulesOnly(op, ModeDefault)
	if r.Decision != DecisionDeny {
		t.Errorf("deny rule should fire first; got %v", r.Decision)
	}
}

func TestCheckRulesOnly_AllowBypassesMatrix(t *testing.T) {
	// In default mode, write would normally trigger NeedApproval.
	// An explicit allow rule should short-circuit it.
	rs := emptyRules()
	rs.UserRules = []*UserRule{
		{Type: RuleAllow, Granularity: GranPath, Pattern: "${cwd}/**"},
	}
	g, _ := NewGate(rs, ModeDefault, "/proj")
	op := Operation{ToolName: "write_file", Category: tool.CategoryWrite, Path: "/proj/foo.go"}
	r := g.CheckRulesOnly(op, ModeDefault)
	if r.Decision != DecisionAllow {
		t.Errorf("allow rule should bypass matrix; got %v (reason: %s)", r.Decision, r.Reason)
	}
}

func TestCheckRulesOnly_AllowDoesNotBypassHardDeny(t *testing.T) {
	rs := emptyRules()
	rs.UserRules = []*UserRule{
		{Type: RuleAllow, Granularity: GranTool, Pattern: "bash"},
	}
	g, _ := NewGate(rs, ModeYes, "/proj")
	op := Operation{ToolName: "bash", Category: tool.CategoryExecute, Command: "rm -rf /"}
	r := g.CheckRulesOnly(op, ModeYes)
	if r.Decision != DecisionDenyHard {
		t.Errorf("hard deny must beat user allow; got %v", r.Decision)
	}
}

func TestCheckRulesOnly_PlanRefusesWrite(t *testing.T) {
	g, _ := NewGate(emptyRules(), ModePlan, "/proj")
	op := Operation{ToolName: "write_file", Category: tool.CategoryWrite, Path: "/tmp/a.txt"}
	r := g.CheckRulesOnly(op, ModePlan)
	if r.Decision != DecisionDeny {
		t.Errorf("plan mode should deny write; got %v", r.Decision)
	}
}

// ============================================================
// Check — full flow including Prompter
// ============================================================

func TestCheck_ApprovalApprove(t *testing.T) {
	g, _ := NewGate(emptyRules(), ModeDefault, "/proj")
	prompter := &fakePrompter{dec: uio.DecisionApprove}
	op := Operation{ToolName: "write_file", Category: tool.CategoryWrite, Path: "/tmp/a.txt"}

	r, err := g.Check(context.Background(), op, ModeDefault, prompter)
	if err != nil {
		t.Fatal(err)
	}
	if r.Decision != DecisionAllow {
		t.Errorf("Approve decision should yield Allow; got %v", r.Decision)
	}
	if prompter.calls != 1 {
		t.Errorf("AskApproval should be called once; got %d", prompter.calls)
	}
	if prompter.last.Risk != uio.RiskMedium {
		t.Errorf("write should map to RiskMedium; got %v", prompter.last.Risk)
	}
}

func TestCheck_ApprovalDeny(t *testing.T) {
	g, _ := NewGate(emptyRules(), ModeDefault, "/proj")
	prompter := &fakePrompter{dec: uio.DecisionDeny}
	op := Operation{ToolName: "bash", Category: tool.CategoryExecute, Command: "ls"}

	r, err := g.Check(context.Background(), op, ModeDefault, prompter)
	if err != nil {
		t.Fatal(err)
	}
	if r.Decision != DecisionDeny {
		t.Errorf("Deny decision should yield Deny; got %v", r.Decision)
	}
	if prompter.last.Risk != uio.RiskHigh {
		t.Errorf("execute should map to RiskHigh; got %v", prompter.last.Risk)
	}
}

func TestCheck_ApprovalAlwaysRemembers(t *testing.T) {
	g, _ := NewGate(emptyRules(), ModeDefault, "/proj")
	prompter := &fakePrompter{dec: uio.DecisionApproveAlways}
	op := Operation{ToolName: "write_file", Category: tool.CategoryWrite, Path: "/tmp/a.txt"}

	// First call prompts.
	r1, _ := g.Check(context.Background(), op, ModeDefault, prompter)
	if r1.Decision != DecisionAllow {
		t.Fatalf("first call should Allow; got %v", r1.Decision)
	}
	if prompter.calls != 1 {
		t.Errorf("first call should prompt once; got %d", prompter.calls)
	}

	// Second call to same path should bypass prompter.
	r2, _ := g.Check(context.Background(), op, ModeDefault, prompter)
	if r2.Decision != DecisionAllow {
		t.Errorf("remembered approval should Allow; got %v", r2.Decision)
	}
	if prompter.calls != 1 {
		t.Errorf("remembered approval should NOT re-prompt; calls=%d", prompter.calls)
	}

	// Different path should prompt again.
	op2 := op
	op2.Path = "/tmp/b.txt"
	_, _ = g.Check(context.Background(), op2, ModeDefault, prompter)
	if prompter.calls != 2 {
		t.Errorf("different path should re-prompt; calls=%d", prompter.calls)
	}
}

func TestCheck_NilPrompterFailsClosed(t *testing.T) {
	g, _ := NewGate(emptyRules(), ModeDefault, "/proj")
	op := Operation{ToolName: "write_file", Category: tool.CategoryWrite, Path: "/tmp/a.txt"}
	r, err := g.Check(context.Background(), op, ModeDefault, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Decision != DecisionDeny {
		t.Errorf("nil prompter should result in Deny; got %v", r.Decision)
	}
}

func TestCheck_CtxCancelledBeforePrompt(t *testing.T) {
	g, _ := NewGate(emptyRules(), ModeDefault, "/proj")
	prompter := &fakePrompter{dec: uio.DecisionApprove}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	op := Operation{ToolName: "write_file", Category: tool.CategoryWrite, Path: "/tmp/a.txt"}
	r, err := g.Check(ctx, op, ModeDefault, prompter)
	if err != nil {
		t.Fatal(err)
	}
	if r.Decision != DecisionDeny {
		t.Errorf("cancelled ctx should yield Deny; got %v", r.Decision)
	}
	if prompter.calls != 0 {
		t.Errorf("prompter should not be invoked under cancelled ctx; calls=%d", prompter.calls)
	}
	if !strings.Contains(r.Reason, "interrupted") {
		t.Errorf("reason should mention interrupted; got %q", r.Reason)
	}
}

func TestCheck_AllowedByMatrixSkipsPrompter(t *testing.T) {
	g, _ := NewGate(emptyRules(), ModeYes, "/proj")
	prompter := &fakePrompter{dec: uio.DecisionDeny} // would deny if asked
	op := Operation{ToolName: "bash", Category: tool.CategoryExecute, Command: "ls"}

	r, _ := g.Check(context.Background(), op, ModeYes, prompter)
	if r.Decision != DecisionAllow {
		t.Errorf("yes mode should allow without prompt; got %v", r.Decision)
	}
	if prompter.calls != 0 {
		t.Errorf("prompter should be skipped on matrix-allow; calls=%d", prompter.calls)
	}
}

// ============================================================
// SetMode / GetMode
// ============================================================

func TestSetGetMode(t *testing.T) {
	g, _ := NewGate(emptyRules(), ModeDefault, "/proj")
	if g.GetMode() != ModeDefault {
		t.Errorf("initial mode should be default; got %v", g.GetMode())
	}
	g.SetMode(ModeYes)
	if g.GetMode() != ModeYes {
		t.Errorf("SetMode failed; got %v", g.GetMode())
	}
}

// ============================================================
// LoadRules — YAML parsing
// ============================================================

func TestLoadRules_MissingFileReturnsHardOnly(t *testing.T) {
	rs, err := LoadRules(filepath.Join(t.TempDir(), "no-such-file.yaml"))
	if err != nil {
		t.Fatalf("missing file should be benign; got %v", err)
	}
	if len(rs.UserRules) != 0 {
		t.Errorf("expected no user rules; got %d", len(rs.UserRules))
	}
	if len(rs.HardDenylist) == 0 {
		t.Error("hard blacklist should always populate")
	}
}

func TestLoadRules_EmptyPathReturnsHardOnly(t *testing.T) {
	rs, err := LoadRules("")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.UserRules) != 0 {
		t.Errorf("empty path should produce zero user rules")
	}
}

func TestLoadRules_ValidYAML(t *testing.T) {
	yaml := `rules:
  - type: deny
    granularity: command
    pattern: "git push --force*"
    reason: "no force push"
  - type: allow
    granularity: path
    pattern: "${cwd}/**"
    reason: "project files"
  - type: deny
    granularity: tool
    pattern: "web_search"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "permissions.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadRules(path)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(rs.UserRules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(rs.UserRules))
	}
	if rs.UserRules[0].Type != RuleDeny || rs.UserRules[0].Granularity != GranCommand {
		t.Errorf("rule[0] mismatch: %+v", rs.UserRules[0])
	}
	if rs.UserRules[1].Type != RuleAllow || rs.UserRules[1].Granularity != GranPath {
		t.Errorf("rule[1] mismatch: %+v", rs.UserRules[1])
	}
	if rs.UserRules[2].Type != RuleDeny || rs.UserRules[2].Granularity != GranTool {
		t.Errorf("rule[2] mismatch: %+v", rs.UserRules[2])
	}
}

func TestLoadRules_InvalidYAML(t *testing.T) {
	yaml := `rules:
  - type: invalid_type
    granularity: command
    pattern: "x"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadRules(path)
	if err == nil {
		t.Error("invalid type should produce error")
	}
}

// ============================================================
// Decision / RuleType / Granularity strings
// ============================================================

func TestEnumStrings(t *testing.T) {
	if DecisionAllow.String() != "allow" {
		t.Error("DecisionAllow.String")
	}
	if DecisionDenyHard.String() != "deny_hard" {
		t.Error("DecisionDenyHard.String")
	}
	if RuleAllow.String() != "allow" {
		t.Error("RuleAllow.String")
	}
	if GranCommand.String() != "command" {
		t.Error("GranCommand.String")
	}
}

// ============================================================
// approvalKey — equivalence
// ============================================================

func TestApprovalKey_PathEquivalence(t *testing.T) {
	op1 := Operation{ToolName: "write_file", Path: "/a.txt", Args: map[string]any{"content": "v1"}}
	op2 := Operation{ToolName: "write_file", Path: "/a.txt", Args: map[string]any{"content": "v2"}}
	if approvalKey(op1) != approvalKey(op2) {
		t.Error("same path should produce same key regardless of other args")
	}
}

func TestApprovalKey_DifferentPath(t *testing.T) {
	op1 := Operation{ToolName: "write_file", Path: "/a.txt"}
	op2 := Operation{ToolName: "write_file", Path: "/b.txt"}
	if approvalKey(op1) == approvalKey(op2) {
		t.Error("different paths should differ in key")
	}
}

func TestApprovalKey_CommandFallback(t *testing.T) {
	op1 := Operation{ToolName: "bash", Command: "ls -la"}
	op2 := Operation{ToolName: "bash", Command: "ls  -la"} // extra space
	// normaliseSpaces collapses → same key
	if approvalKey(op1) != approvalKey(op2) {
		t.Error("normalised whitespace commands should share key")
	}
}

func TestApprovalKey_ArgsFallback(t *testing.T) {
	// Tool with neither Path nor Command: keys derived from args.
	op1 := Operation{ToolName: "ask_user", Args: map[string]any{"q": "hi"}}
	op2 := Operation{ToolName: "ask_user", Args: map[string]any{"q": "hi"}}
	if approvalKey(op1) != approvalKey(op2) {
		t.Error("identical args should produce identical key")
	}
	op3 := Operation{ToolName: "ask_user", Args: map[string]any{"q": "different"}}
	if approvalKey(op1) == approvalKey(op3) {
		t.Error("different args should produce different keys")
	}
}

// ============================================================
// NewGate guard rails
// ============================================================

func TestNewGate_NilRules(t *testing.T) {
	if _, err := NewGate(nil, ModeDefault, "/x"); err == nil {
		t.Error("nil rules should be rejected")
	}
}
