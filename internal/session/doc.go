// Package session — see session.go for the domain model and Repository
// interface. The SQLite-backed implementation lives in the `store`
// sub-package. The in-tree migration files (`migrations/`) are embedded
// into the binary by store/db.go.
//
// Reference: docs/system-design/05-core-abstractions.md §5.8
//            docs/system-design/06-session-storage.md (R3 + R5)
package session
