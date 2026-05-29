// Copyright (c) 2026 mini-agent contributors
// SPDX-License-Identifier: MIT

// Package ask hosts the agent's Meta-category tools that talk back
// to the operator. Today that means just `ask_user`.
//
// Why a separate package vs collapsing into `internal/tool/fs`:
//
//   - Different dependency surface: ask_user pulls in the uio
//     interfaces, none of which the fs/search/shell packages need.
//   - Different mental model: ask_user is human-in-the-loop signal
//     flow, not file-system manipulation.
//
// All P0 tools across packages must share the testkit invariants
// (R7-1' D82) and the schema-golden file convention (D83). See
// askuser_test.go for how this package satisfies them.
package ask
