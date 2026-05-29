// Package store is the SQLite-backed Repository implementation. It
// wraps the sqlc-generated query layer in `store/gen/`, layers transaction
// management on top, and converts between the storage row shape (`gen.*`)
// and the domain model (`session.*`).
//
// Files in this package:
//
//	db.go     — connection lifecycle, PRAGMAs, golang-migrate driver
//	store.go  — the Store struct + Repository method implementations
//	codec.go  — blocks_json / original_ids_json (de)serialization
//
// Reference: docs/system-design/06-session-storage.md (R3 + R5)
package store
