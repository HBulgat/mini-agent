// Package view holds the third data layer: a UI-oriented projection
// of session.Message into discrete Items (text / thinking / tool_call
// / summary). Both CLI and Web UI consume the same BuildConversation
// output to stay behaviorally aligned (Risk R-5).
//
// Status: skeleton only. Schema and BuildConversation finalised in
// R9 / R11.
package view
