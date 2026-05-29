// Package task hosts the task tool — synchronous sub-agent dispatcher,
// nesting depth ≤ 1 (enforced inside Invoke per D65). Sub-agent
// failures / partial successes surface via the structured template in
// D60/D61.
//
// Status: skeleton only. Implementation tracked by T3.4 (depends on
// R7-2 schema lock-in for task input).
package task
