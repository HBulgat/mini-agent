// Package compaction defines the Compactor contract and hosts
// strategy implementations under summarize/, sliding/, hierarchical/.
// Trigger logic lives inside agent.maybeCompact — this package only
// answers "how to compact", not "when".
//
// Status: skeleton only. R8 will lock summarize prompt / sliding
// window / hierarchical hierarchy parameters. Implementation tracked
// by T3.1 / T3.2.
package compaction
