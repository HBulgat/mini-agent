// Package agentsmd locates and merges the global (~/.mini-agent/AGENTS.md)
// and project-level (<cwd>/AGENTS.md) instruction files. Lookup does NOT
// recurse upwards (D27); /cd reloads the project-level entry.
//
// The merged text is wrapped in <project_guidelines>...</project_guidelines>
// inside the agent.prepareInitialHistory composition (D52, D54).
//
// Status: skeleton only. Implementation tracked by T2.8.
package agentsmd
