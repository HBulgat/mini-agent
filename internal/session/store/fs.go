package store

import (
	"os"
)

// ensureDir is the minimal mkdir-p helper. We keep it in its own file
// so db.go's surface stays focused on database concerns.
func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}
