//go:build !unix

package fs

import (
	"fmt"
	"os"
	"path/filepath"
)

// acquireLock is a no-op on platforms without flock: one writer per root is
// still the rule, but the OS does not enforce it here.
func (b *Backend) acquireLock() error {
	path := filepath.Join(b.root, lockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("fs: open lock %s: %w", path, err)
	}
	b.lock = f
	return nil
}

func (b *Backend) releaseLock() error {
	if b.lock == nil {
		return nil
	}
	defer b.lock.Close()
	return nil
}
