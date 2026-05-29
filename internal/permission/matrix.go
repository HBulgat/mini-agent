package permission

import "github.com/HBulgat/mini-agent/internal/tool"

// matrixDecision implements the (mode × category) lookup from
// docs/system-design/04-tool-catalog.md §4.3.
//
// We compute this purely from (mode, category) — no tool name, no
// argument inspection — so the decision is fast and obviously
// matches the published table. Tool-specific carve-outs (e.g. "bash
// in --yes still hits hard blacklist") happen in earlier steps of
// gate.Check, not here.
//
// The four-state return values:
//
//	DecisionAllow         — proceed, no prompt
//	DecisionDeny          — refuse outright (currently only ModePlan
//	                         on write/execute/network)
//	DecisionNeedApproval — need to ask the user
//
// DecisionDenyHard is reserved for the hard blacklist; this matrix
// never returns it.
func matrixDecision(mode Mode, cat tool.Category) Decision {
	// Read-only and Meta tools are always allowed across every mode.
	// This mirrors §4.3's left columns ("✅ in every mode").
	if cat == tool.CategoryReadOnly || cat == tool.CategoryMeta {
		return DecisionAllow
	}

	// Plan mode: anything that mutates state or hits the network is
	// refused. The agent is supposed to *plan*, not *act*.
	if mode == ModePlan {
		return DecisionDeny
	}

	// Yes mode: everything except hard-blacklisted entries auto-passes.
	if mode == ModeYes {
		return DecisionAllow
	}

	// Auto-edit: writes auto-pass; execute / network still ask.
	if mode == ModeAutoEdit && cat == tool.CategoryWrite {
		return DecisionAllow
	}

	// Default mode: every non-read/non-meta call asks.
	// (auto-edit's exec/network falls through here as well.)
	return DecisionNeedApproval
}
