package permission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/HBulgat/mini-agent/internal/tool"
	"github.com/HBulgat/mini-agent/internal/uio"
)

// gate is the default Gate implementation. It is concurrency-safe by
// construction (every mutable field sits behind mu) so the agent loop
// can dispatch read-only tools in parallel batches and still trust the
// gate's view of mode + remembered approvals.
type gate struct {
	mu sync.RWMutex

	// rules is the immutable rule set produced by LoadRules. Pointer
	// (per D31) because the slice can grow large and we don't want
	// per-Check copies.
	rules *RuleSet

	// sub provides ${cwd} / ${home} / ~ expansion for user rule
	// patterns. Recreated by SetCwd; old expansions don't change
	// retroactively.
	sub *Substitutor

	// mode is the active permission mode. Mutated by SetMode and
	// read by every Check / CheckRulesOnly call.
	mode Mode

	// approved is the in-memory set of "approve always" decisions.
	// Keyed by approvalKey(op) — same tool name + same path/command
	// hashed digest. Session-scoped only (cleared by NewGate).
	approved map[string]struct{}
}

// NewGate constructs a Gate with the given rules + mode. The cwd is
// captured for variable substitution; pass "" to disable ${cwd}
// expansion. Subsequent /cwd switches should call SetCwd to refresh.
//
// rules MUST be non-nil; callers that want "no user rules" should
// pass &RuleSet{HardDenylist: hardDenylist()} (or call LoadRules("")).
func NewGate(rules *RuleSet, mode Mode, cwd string) (Gate, error) {
	if rules == nil {
		return nil, errors.New("permission: NewGate: rules must not be nil")
	}
	return &gate{
		rules:    rules,
		sub:      NewSubstitutor(cwd),
		mode:     mode,
		approved: make(map[string]struct{}),
	}, nil
}

// Check is the public 5-step entrypoint. Returns a terminal Decision
// (Allow / Deny / DenyHard) — never NeedApproval. Errors propagate
// from the Prompter (e.g. ctx cancellation during the prompt phase).
func (g *gate) Check(ctx context.Context, op Operation, mode Mode, prompter uio.Prompter) (Result, error) {
	// Steps 1–4: rule layers + matrix.
	pre := g.CheckRulesOnly(op, mode)
	if pre.Decision != DecisionNeedApproval {
		return pre, nil
	}

	// Step 5: matrix said "ask user". Skip the prompt if the user
	// previously chose "approve always" for an equivalent operation.
	if g.isApproved(op) {
		return Result{Decision: DecisionAllow, Reason: "remembered approval"}, nil
	}

	// Honour ctx before launching the prompter — saves a round-trip
	// when the user already hit Ctrl+C between rule check and prompt.
	if err := ctx.Err(); err != nil {
		return Result{Decision: DecisionDeny, Reason: "interrupted by user"}, nil
	}

	if prompter == nil {
		// No prompter wired (e.g. early bootstrap, automated tests).
		// Fail closed — refusing is safer than silently allowing.
		return Result{Decision: DecisionDeny, Reason: "approval required but no prompter available"}, nil
	}

	req := uio.ApprovalRequest{
		ToolName:    op.ToolName,
		Args:        op.Args,
		Risk:        riskOf(op.Category),
		Reason:      "this operation requires explicit approval",
		Description: describeOperation(op),
	}
	dec, err := prompter.AskApproval(ctx, req)
	if err != nil {
		// Cancellation, no-prompter sentinel, or other Prompter
		// failure. Treat as Deny so the agent surfaces "I tried to
		// X but you cancelled" rather than crashing.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Result{Decision: DecisionDeny, Reason: "interrupted by user"}, nil
		}
		return Result{Decision: DecisionDeny, Reason: err.Error()}, err
	}
	switch dec {
	case uio.DecisionApprove:
		return Result{Decision: DecisionAllow, Reason: "approved by user"}, nil
	case uio.DecisionApproveAlways:
		g.RememberApproval(op)
		return Result{Decision: DecisionAllow, Reason: "approved (remembered for session)"}, nil
	case uio.DecisionDeny:
		return Result{Decision: DecisionDeny, Reason: "denied by user"}, nil
	default:
		// Defensive fallback: the prompter returned an unknown
		// value — refuse rather than guess.
		return Result{Decision: DecisionDeny, Reason: "unknown approval decision"}, nil
	}
}

// CheckRulesOnly runs steps 1–4 without prompting. Returns
// DecisionNeedApproval when the matrix says "ask"; the caller (Check
// itself or an external bash preflight) decides what to do next.
func (g *gate) CheckRulesOnly(op Operation, mode Mode) Result {
	g.mu.RLock()
	rules := g.rules
	sub := g.sub
	g.mu.RUnlock()

	// Step 1: hard blacklist — non-overridable.
	for _, r := range rules.HardDenylist {
		if matchHardDeny(r, &op) {
			return Result{Decision: DecisionDenyHard, Reason: r.Reason}
		}
	}

	// Step 2 + 3: user rules in declaration order. First match wins
	// per §7.3.6 — deny rules ahead of allow rules in the file
	// always win unless an earlier allow short-circuits.
	for _, r := range rules.UserRules {
		if !matchUserRule(r, &op, sub) {
			continue
		}
		switch r.Type {
		case RuleDeny:
			return Result{Decision: DecisionDeny, Reason: r.Reason}
		case RuleAllow:
			return Result{Decision: DecisionAllow, Reason: r.Reason}
		}
	}

	// Step 4: pure mode × category matrix.
	d := matrixDecision(mode, op.Category)
	switch d {
	case DecisionAllow:
		return Result{Decision: DecisionAllow, Reason: "mode allows " + op.Category.String()}
	case DecisionDeny:
		return Result{Decision: DecisionDeny, Reason: "mode " + mode.String() + " forbids " + op.Category.String()}
	default: // NeedApproval
		return Result{Decision: DecisionNeedApproval, Reason: "approval required by mode " + mode.String()}
	}
}

// SetMode swaps the active permission mode. The new value applies to
// every subsequent Check call; in-flight checks complete with their
// captured mode (ie no race against a /mode toggle mid-prompt).
func (g *gate) SetMode(mode Mode) {
	g.mu.Lock()
	g.mode = mode
	g.mu.Unlock()
}

// GetMode returns the currently-set mode.
func (g *gate) GetMode() Mode {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.mode
}

// RememberApproval records the operation so subsequent equivalent
// calls skip the prompt. Equivalence rule (R7-deferred): same tool
// name + same path or command. Two write_file calls to the same path
// share an approval; a write_file to a different path does not.
func (g *gate) RememberApproval(op Operation) {
	key := approvalKey(op)
	g.mu.Lock()
	g.approved[key] = struct{}{}
	g.mu.Unlock()
}

// SetCwd refreshes the substitutor with a new ${cwd} value. Called
// by the REPL when the user issues `/cwd path`.
func (g *gate) SetCwd(cwd string) {
	g.mu.Lock()
	g.sub = NewSubstitutor(cwd)
	g.mu.Unlock()
}

// isApproved looks up a remembered approval. Internal helper, not on
// the Gate interface.
func (g *gate) isApproved(op Operation) bool {
	key := approvalKey(op)
	g.mu.RLock()
	_, ok := g.approved[key]
	g.mu.RUnlock()
	return ok
}

// approvalKey computes the equivalence key for RememberApproval. The
// rule (R7-deferred): same tool name + same Path (filesystem tools)
// or same Command (bash). For Meta tools and tools with neither, we
// fall back to a deterministic hash of the args map so identical
// argument sets share a key.
func approvalKey(op Operation) string {
	switch {
	case op.Path != "":
		return op.ToolName + "|path|" + op.Path
	case op.Command != "":
		return op.ToolName + "|cmd|" + normaliseSpaces(op.Command)
	default:
		return op.ToolName + "|args|" + hashArgs(op.Args)
	}
}

// hashArgs produces a deterministic short digest of the input args.
// We sort keys before serializing so map iteration order doesn't
// affect the result.
func hashArgs(args map[string]any) string {
	if len(args) == 0 {
		return "empty"
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte('=')
		v, _ := json.Marshal(args[k])
		sb.Write(v)
		sb.WriteByte('|')
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:8]) // first 8 bytes is enough for collision safety in a session
}

// riskOf maps tool category to UI risk level. Conservative bias —
// when in doubt, escalate (the prompt UI can dial back).
func riskOf(cat tool.Category) uio.RiskLevel {
	switch cat {
	case tool.CategoryReadOnly, tool.CategoryMeta:
		return uio.RiskLow
	case tool.CategoryWrite:
		return uio.RiskMedium
	case tool.CategoryExecute, tool.CategoryNetwork:
		return uio.RiskHigh
	default:
		return uio.RiskMedium
	}
}

// describeOperation produces the multi-line "what's about to happen"
// string the UI shows below the approval prompt. We keep it short —
// the UI may already render the args as a pretty JSON tree.
func describeOperation(op Operation) string {
	var sb strings.Builder
	sb.WriteString("Tool: ")
	sb.WriteString(op.ToolName)
	if op.Path != "" {
		sb.WriteString("\nPath: ")
		sb.WriteString(op.Path)
	}
	if op.Command != "" {
		sb.WriteString("\nCommand: ")
		sb.WriteString(op.Command)
	}
	return sb.String()
}
